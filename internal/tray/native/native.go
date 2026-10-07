//go:build tray && darwin

package native

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -fmodules
#cgo LDFLAGS: -framework Cocoa -framework WebKit -framework ServiceManagement -framework Foundation

#include <stdlib.h>

// TMTrayConfig is the whole of what the shell needs to come up. Labels are passed in
// rather than hardcoded so the menu follows the interface language preference.
typedef struct {
	const char *url;
	const char *panelURL;
	const char *windowTitle;
	const char *statusTooltip;
	const char *openMainLabel;
	const char *openWebsiteLabel;
	const char *quitLabel;
	int minimizeToTray;
	int quickPanel;
	double widthFraction;
	double heightFraction;
} TMTrayConfig;

void TMTrayRun(TMTrayConfig config);
void TMTrayStop(void);
void TMTrayShowWindow(void);
void TMTrayHideWindow(void);
void TMTraySetMinimizeToTray(int enabled);
void TMTraySetQuickPanel(int enabled);
void TMTrayOpenURL(const char *url);
char *TMTrayLoginItemSet(int enabled);
int TMTrayLoginItemStatus(void);
char *TMTraySystemJSON(void);
char *TMTrayPreferredLanguage(void);
void TMTrayFreeString(char *value);
*/
import "C"

import (
	"encoding/json"
	"errors"
	"runtime"
	"unsafe"
)

// Run starts the Cocoa application and blocks until the shell stops.
//
// The caller must be on the process's main thread: NSApplication is main-thread only, and
// cmd/tunnelmesh-client-tray pins itself there with runtime.LockOSThread in init.
func Run(config Config) error {
	if config.URL == "" {
		return errors.New("native: the settings window needs a URL to load")
	}
	runtime.LockOSThread()
	cURL := C.CString(config.URL)
	cPanel := C.CString(config.PanelURL)
	cTitle := C.CString(config.WindowTitle)
	cTooltip := C.CString(config.StatusTooltip)
	cOpenMain := C.CString(config.OpenMainLabel)
	cOpenSite := C.CString(config.OpenWebsiteLabel)
	cQuit := C.CString(config.QuitLabel)
	defer func() {
		C.free(unsafe.Pointer(cURL))
		C.free(unsafe.Pointer(cPanel))
		C.free(unsafe.Pointer(cTitle))
		C.free(unsafe.Pointer(cTooltip))
		C.free(unsafe.Pointer(cOpenMain))
		C.free(unsafe.Pointer(cOpenSite))
		C.free(unsafe.Pointer(cQuit))
	}()

	C.TMTrayRun(C.TMTrayConfig{
		url:              cURL,
		panelURL:         cPanel,
		windowTitle:      cTitle,
		statusTooltip:    cTooltip,
		openMainLabel:    cOpenMain,
		openWebsiteLabel: cOpenSite,
		quitLabel:        cQuit,
		minimizeToTray:   cBool(config.MinimizeToTray),
		quickPanel:       cBool(config.QuickPanel),
		widthFraction:    C.double(fraction(config.WidthFraction)),
		heightFraction:   C.double(fraction(config.HeightFraction)),
	})
	return nil
}

func cBool(value bool) C.int {
	if value {
		return 1
	}
	return 0
}

// Stop asks the shell to end its run loop. Run returns shortly afterwards.
func Stop() { C.TMTrayStop() }

// ShowWindow brings the settings window forward, creating it if needed.
func ShowWindow() { C.TMTrayShowWindow() }

// HideWindow withdraws the settings window without quitting.
func HideWindow() { C.TMTrayHideWindow() }

// SetMinimizeToTray updates what a window close means.
func SetMinimizeToTray(enabled bool) { C.TMTraySetMinimizeToTray(cBool(enabled)) }

// SetQuickPanel updates what a left click on the menu-bar item means.
func SetQuickPanel(enabled bool) { C.TMTraySetQuickPanel(cBool(enabled)) }

// OpenURL hands a URL to the system browser.
func OpenURL(target string) error {
	if target == "" {
		return errors.New("native: no URL to open")
	}
	cTarget := C.CString(target)
	defer C.free(unsafe.Pointer(cTarget))
	C.TMTrayOpenURL(cTarget)
	return nil
}

// LoginItemStatus is the SMAppService registration state.
type LoginItemStatus int

// Mirrors SMAppServiceStatus plus the two states the Objective-C side adds.
const (
	LoginItemNotRegistered LoginItemStatus = iota
	LoginItemEnabled
	LoginItemRequiresApproval
	LoginItemNotFound
	LoginItemUnsupported
	LoginItemUnknown
)

// Enabled reports whether the login item is registered and active.
func (s LoginItemStatus) Enabled() bool { return s == LoginItemEnabled }

// Supported reports whether this macOS can register a login item at all.
func (s LoginItemStatus) Supported() bool { return s != LoginItemUnsupported && s != LoginItemUnknown }

// LoginItemSet registers or unregisters the app as a login item.
//
// The error is returned rather than logged because the interface has to show it: an
// unsigned or ad-hoc signed bundle is refused by SMAppService, and a switch that flips
// back on its own without an explanation looks broken.
func LoginItemSet(enabled bool) error {
	cResult := C.TMTrayLoginItemSet(cBool(enabled))
	if cResult == nil {
		return nil
	}
	defer C.TMTrayFreeString(cResult)
	return errors.New(C.GoString(cResult))
}

// LoginItemStatus reads the current registration state.
func LoginItemStatusOf() LoginItemStatus { return LoginItemStatus(int(C.TMTrayLoginItemStatus())) }

// SystemInfoJSON is the operating system identity for the about tab.
func SystemInfoJSON() string {
	cValue := C.TMTraySystemJSON()
	if cValue == nil {
		return ""
	}
	defer C.TMTrayFreeString(cValue)
	return C.GoString(cValue)
}

// System describes the host macOS.
func System() SystemInfo {
	var info SystemInfo
	if raw := SystemInfoJSON(); raw != "" {
		_ = json.Unmarshal([]byte(raw), &info)
	}
	if info.Arch == "" {
		info.Arch = runtime.GOARCH
	}
	return info
}

// RendererName identifies the web view that draws the settings window.
//
// It is the same answer on every macOS, so it is a constant rather than a probe; the
// Windows shell reports "browser" when the WebView2 runtime is missing, which is the case
// the about tab exists to explain.
func RendererName() string { return "wkwebview" }

// RendererDetail is empty on macOS: WKWebView is part of the operating system and the
// reported system version already identifies it.
func RendererDetail() string { return "" }

// PreferredLanguage is the operator's first macOS UI language, as a BCP-47 tag.
//
// The menu is built before the webview can tell Go which locale it resolved, and a
// menu-bar item in the wrong language is visible before the window is ever opened, so the
// shell reports the system choice directly.
func PreferredLanguage() string {
	cValue := C.TMTrayPreferredLanguage()
	if cValue == nil {
		return ""
	}
	defer C.TMTrayFreeString(cValue)
	return C.GoString(cValue)
}

//export TunnelMeshTrayMenuAction
func TunnelMeshTrayMenuAction(action C.int) {
	switch int(action) {
	case actionOpenMain:
		if handlers.OpenMain != nil {
			handlers.OpenMain()
		}
	case actionOpenSite:
		if handlers.OpenWebsite != nil {
			_ = handlers.OpenWebsite()
		}
	case actionQuit:
		if handlers.Quit != nil {
			handlers.Quit()
		}
	}
}

//export TunnelMeshTrayMinimizeToTray
func TunnelMeshTrayMinimizeToTray() C.int {
	if handlers.MinimizeToTray == nil {
		return 1
	}
	if handlers.MinimizeToTray() {
		return 1
	}
	return 0
}

//export TunnelMeshTrayWindowVisibilityChanged
func TunnelMeshTrayWindowVisibilityChanged(visible C.int) {
	if handlers.WindowVisibilityChanged != nil {
		handlers.WindowVisibilityChanged(int(visible) != 0)
	}
}

var _ = errUnsupported
