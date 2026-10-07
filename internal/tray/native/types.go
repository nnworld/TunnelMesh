//go:build tray && (darwin || windows)

// Package native is the operating-system shell around the tray: the notification-area
// item, the settings window, the embedded web view that renders the interface and the
// start-up registration.
//
// It is the only package in the module that touches a platform GUI API, and it sits behind
// the "tray" build tag for the same reason the VPN gateway sits behind "vpn": a default
// CGO_ENABLED=0 build must not need a toolchain that can link WebKit, and the cross
// platform release matrix must not produce a binary that does.
//
// The shell is hand-written on both platforms for one reason: driving the event loop from
// several libraries at once is how tray apps end up with two owners of the main thread,
// and the tray needs to stay the only one. macOS implements this surface over Cocoa,
// WebKit and SMAppService through cgo (native.go plus tray_darwin.m); Windows implements it
// over Win32 and WebView2 in pure Go (tray_windows.go), which is what keeps the Windows
// tray cross-compilable with CGO_ENABLED=0.
//
// types.go holds what both platforms share, so the exported surface of this package is one
// contract with two implementations rather than two similar-looking packages. That is what
// lets cmd/tunnelmesh-client-tray keep a single main.go for both shells.
package native

import "errors"

// Menu actions the shell reports back. The values are part of the contract between the
// platform shell and the callbacks below, so they are named rather than numbered inline.
const (
	actionOpenMain = 0
	actionOpenSite = 1
	actionQuit     = 2
)

// Handlers receives the shell's callbacks. Every field is optional; a nil field makes
// the corresponding menu entry a no-op rather than a crash, which matters because
// the window is still usable while the Go side is starting up.
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
	URL string
	// PanelURL is the same address carrying the same secret, routed to the compact quick
	// panel. The shell falls back to the menu when it is empty, because a panel with
	// nothing to load would leave the operator with no menu and no window.
	PanelURL         string
	WindowTitle      string
	StatusTooltip    string
	OpenMainLabel    string
	OpenWebsiteLabel string
	QuitLabel        string
	MinimizeToTray   bool
	// QuickPanel decides what a left click on the notification-area item does: the panel
	// when on, the menu when off. The shell keeps asking Go afterwards, so a save in the
	// general tab changes the next click without a relaunch.
	QuickPanel bool
	// WidthFraction and HeightFraction size the window against the screen. Zero selects
	// the default of one half, which is the documented initial size.
	WidthFraction  float64
	HeightFraction float64
	// WebViewDataDir is where the embedded browser keeps its own profile. macOS shares the
	// WKWebView data store with the system and ignores this; on Windows it is what stops the
	// settings window from being created against the machine-wide Edge profile.
	WebViewDataDir string
}

// SystemInfo is the operating system identity for the about tab.
type SystemInfo struct {
	OS        string `json:"os"`
	OSVersion string `json:"osVersion"`
	Arch      string `json:"arch"`
}

var (
	handlers Handlers

	// errUnsupported is what a shell call returns when the platform cannot serve it,
	// rather than what makes the tray fail to start.
	errUnsupported = errors.New("native: this tray shell is only available in a -tags tray build on macOS or Windows")
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

// fraction clamps a screen fraction into a usable range.
//
// The lower bound exists because a window smaller than a fifth of the screen cannot hold
// the four tabs, and the upper bound because a window that covers the whole screen leaves
// the operator no way to reach what is behind it.
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
