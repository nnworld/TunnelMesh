//go:build tray && windows

package native

// The WebView2 surface.
//
// go-webview2 loads WebView2Loader.dll from its own bundle into memory, so the tray ships
// as one executable with no DLL beside it, and the browser process is the machine-wide
// Edge WebView2 runtime rather than an embedded copy of Chromium.
//
// Two things about this library shape the shell. Embed has to be called from inside the
// message loop, because it pumps messages itself until the asynchronous environment and
// controller creation completes; a second pump started from outside the loop is how a
// window ends up with two owners of its queue. And a failure inside the library ends in
// os.Exit, which no callback can intercept, so the runtime is probed before anything is
// embedded instead of being discovered by crashing.

import (
	"errors"
	"log"
	"os"
	"sync"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

// devtoolsEnvironment lets an operator open the web inspector on a machine where the tray
// is the only way to reach the page. It stays off unless asked for, because a settings
// window that answers F12 for everyone is a support conversation waiting to happen.
const devtoolsEnvironment = "TUNNELMESH_TRAY_DEVTOOLS"

// webView holds one web view inside one window.
type webView struct {
	hwnd     windows.Handle
	url      string
	dataPath string
	chromium *edge.Chromium

	mu    sync.Mutex
	ready bool
}

func newWebView(hwnd windows.Handle, url, dataPath string) *webView {
	return &webView{hwnd: hwnd, url: url, dataPath: dataPath, chromium: edge.NewChromium()}
}

// webview2Runtime asks the loader what it can actually start, without starting anything.
// An empty answer means the machine has no runtime and the shell must degrade.
func webview2Runtime() string {
	version, err := webviewloader.GetAvailableCoreWebView2BrowserVersionString("")
	if err != nil {
		return ""
	}
	return version
}

// embed creates the web view. It must run on the message-loop thread after the loop has
// started; the shell posts it there.
func (v *webView) embed() bool {
	if v == nil || v.hwnd == 0 {
		return false
	}
	v.chromium.DataPath = v.dataPath
	v.chromium.Debug = false
	v.chromium.SetErrorCallback(func(err error) {
		if err != nil {
			log.Printf("webview2: %v", err)
		}
	})
	if !v.chromium.Embed(uintptr(v.hwnd)) {
		return false
	}
	v.applySettings()
	v.chromium.Navigate(v.url)
	v.mark(true)
	v.chromium.Resize()
	return true
}

// applySettings turns off the browser chrome a tray settings window should not have.
//
// The default context menu is only kept when the developer switch is on: right-clicking a
// settings page and getting "Back"/"Reload"/"Save as" teaches users that the window is a
// browser, and "Reload" against a loopback address they cannot read is a support call.
func (v *webView) applySettings() {
	settings, err := v.chromium.GetSettings()
	if err != nil {
		log.Printf("webview2: settings unavailable: %v", err)
		return
	}
	devtools := os.Getenv(devtoolsEnvironment) == "1"
	_ = settings.PutIsStatusBarEnabled(false)
	_ = settings.PutAreDevToolsEnabled(devtools)
	_ = settings.PutAreDefaultContextMenusEnabled(devtools)
	_ = settings.PutIsZoomControlEnabled(false)
	// An autofill offer inside a window that edits a service token is a data leak waiting
	// for somebody to accept it, and the password manager has no better claim on it.
	_ = v.chromium.PutIsGeneralAutofillEnabled(false)
	_ = v.chromium.PutIsPasswordAutosaveEnabled(false)
}

func (v *webView) mark(ready bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.ready = ready
}

// resize follows the window. Calls before the view exists are ignored rather than fatal,
// because a WM_SIZE can arrive while the controller is still being created.
func (v *webView) resize() {
	if v == nil {
		return
	}
	v.mu.Lock()
	ready := v.ready
	v.mu.Unlock()
	if !ready {
		return
	}
	v.chromium.Resize()
}

// shutdown tells the controller to stop painting before its window is destroyed, which is
// what keeps a closing tray from logging a lost rendering surface on every exit.
func (v *webView) shutdown() {
	if v == nil {
		return
	}
	v.mu.Lock()
	ready := v.ready
	v.mu.Unlock()
	if !ready {
		return
	}
	v.chromium.ShuttingDown()
}

// errNoRenderer explains why the shell fell back to the system browser.
var errNoRenderer = errors.New("native: the Microsoft Edge WebView2 runtime is not installed")
