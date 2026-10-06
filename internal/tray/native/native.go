//go:build tray && darwin

// Package native is the macOS shell around the tray: the menu-bar item, the settings
// window, the WKWebView that renders the embedded interface and the login item.
//
// It is the only package in the module that touches Cocoa, and it sits behind the "tray"
// build tag for the same reason the VPN gateway sits behind "vpn": a default
// CGO_ENABLED=0 build must not need a toolchain that can link WebKit, and the cross
// platform release matrix must not produce a binary that does.
//
// One shim owns the Cocoa run loop. Driving NSApplication from several libraries at once
// is how menu-bar apps end up with two event loops fighting over the main thread, so
// everything native lives in tray_darwin.m and Go only calls in and receives callbacks.
package native

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -fmodules
#cgo LDFLAGS: -framework Cocoa -framework WebKit -framework ServiceManagement -framework Foundation

#include <stdlib.h>

// TMTrayConfig is the whole of what the shell needs to come up. Labels are passed in
// rather than hardcoded so the menu follows the interface language preference.
typedef struct {
	const char *url;
	const char *windowTitle;
	const char *statusTooltip;
	const char *openMainLabel;
	const char *openWebsiteLabel;
	const char *quitLabel;
	int minimizeToTray;
	double widthFraction;
	double heightFraction;
} TMTrayConfig;

void TMTrayRun(TMTrayConfig config);
void TMTrayStop(void);
void TMTrayShowWindow(void);
void TMTrayHideWindow(void);
void TMTraySetMinimizeToTray(int enabled);
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

// Menu actions the shell reports back. The values are part of the contract between
// tray_darwin.m and the callbacks below, so they are named rather than numbered inline.
const (
	actionOpenMain = 0
	actionOpenSite = 1
	actionQuit     = 2
)

// Handlers receives the shell's callbacks. Every field is optional; a nil field makes
// the corresponding menu entry a no-op rather than a crash, which matters because the
// window is still usable while the Go side is starting up.
type Handlers struct {
	// OpenMain is called when the operator picks "打开主界面". The shell has already
	// brought the window forward; this is a notification, not a request.
	OpenMain func()
	// OpenWebsite is called for "打开官网首页". Routing it through Go keeps one
	// implementation of "open a URL", shared with the about tab's links.
	OpenWebsite func() error
	// Quit is called for "退出" and for a window close when minimize-to-tray is off.
	Quit func()
	// MinimizeToTray is consulted when a window closes. It is a callback rather than a
	// cached flag so the general tab's switch takes effect without telling the shell.
	MinimizeToTray func() bool
	// WindowVisibilityChanged reports the window becoming visible or hidden.
	WindowVisibilityChanged func(visible bool)
}

// Config describes the shell to start.
type Config struct {
	// URL is the loopback address carrying the launch secret.
	URL              string
	WindowTitle      string
	StatusTooltip    string
	OpenMainLabel    string
	OpenWebsiteLabel string
	QuitLabel        string
	MinimizeToTray   bool
	// WidthFraction and HeightFraction size the window against the main screen. Zero
	// selects the default of one half, which is the documented initial size.
	WidthFraction  float64
	HeightFraction float64
}

var (
	handlers       Handlers
	errUnsupported = errors.New("native: the macOS tray shell is only available in a -tags tray build on macOS")
)

// SetHandlers installs the callbacks before Run. The shell keeps a pointer to the same
// struct, so this must happen first: a menu click before Run has no receiver otherwise.
func SetHandlers(next Handlers) { handlers = next }

// DefaultConfig fills the fields an operator does not configure.
func DefaultConfig(url string) Config {
	return Config{
		URL:              url,
		WindowTitle:      "TunnelMesh Client",
		StatusTooltip:    "TunnelMesh Client",
		OpenMainLabel:    "Open Dashboard",
		OpenWebsiteLabel: "Open Website",
		QuitLabel:        "Quit",
		MinimizeToTray:   true,
		WidthFraction:    0.5,
		HeightFraction:   0.5,
	}
}

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
	cTitle := C.CString(config.WindowTitle)
	cTooltip := C.CString(config.StatusTooltip)
	cOpenMain := C.CString(config.OpenMainLabel)
	cOpenSite := C.CString(config.OpenWebsiteLabel)
	cQuit := C.CString(config.QuitLabel)
	defer func() {
		C.free(unsafe.Pointer(cURL))
		C.free(unsafe.Pointer(cTitle))
		C.free(unsafe.Pointer(cTooltip))
		C.free(unsafe.Pointer(cOpenMain))
		C.free(unsafe.Pointer(cOpenSite))
		C.free(unsafe.Pointer(cQuit))
	}()

	C.TMTrayRun(C.TMTrayConfig{
		url:              cURL,
		windowTitle:      cTitle,
		statusTooltip:    cTooltip,
		openMainLabel:    cOpenMain,
		openWebsiteLabel: cOpenSite,
		quitLabel:        cQuit,
		minimizeToTray:   cBool(config.MinimizeToTray),
		widthFraction:    C.double(fraction(config.WidthFraction)),
		heightFraction:   C.double(fraction(config.HeightFraction)),
	})
	return nil
}

// fraction clamps a screen fraction into a usable range.
func fraction(value float64) float64 {
	if value <= 0 {
		return 0.5
	}
	if value > 0.9 {
		return 0.9
	}
	if value < 0.2 {
		return 0.2
	}
	return value
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

// SystemInfo is the decoded shape of SystemInfoJSON.
type SystemInfo struct {
	OS        string `json:"os"`
	OSVersion string `json:"osVersion"`
	Arch      string `json:"arch"`
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
