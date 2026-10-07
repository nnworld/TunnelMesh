//go:build tray && windows

package native

// The Windows tray shell: one OS thread, one message loop, one owner of the queue.
//
// Everything native lives here for the same reason the Cocoa shim owns its run loop on
// macOS. A tray that lets two libraries pump the same thread is a tray with menus that stop
// answering and a web view that paints over its own window, so the notification-area icon,
// the settings window, the quick panel and their messages have exactly one owner.
//
// It is written against Win32 directly rather than through a desktop framework because a
// framework would own the run loop this shell has to share, and because staying in pure Go
// is what lets the tray cross-compile with CGO_ENABLED=0 from the same Linux job that builds
// every other binary in the release matrix.

import (
	"errors"
	"log"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Window classes, the single-instance mutex and the App User Model ID.
//
// The identifier is what groups the taskbar button and any notification under this tray
// rather than under the console host that happened to launch it.
const (
	mainWindowClass  = "TunnelMeshClientMainWindow"
	panelWindowClass = "TunnelMeshClientQuickPanel"
	hostWindowClass  = "TunnelMeshClientTrayHost"

	singleInstanceMutex = "Local\\TunnelMeshClientTray"
	appUserModelID      = "com.tunnelmesh.client-tray"

	// menu identifiers are the contract between the popup menu and the dispatcher below.
	menuIDOpenMain = 1
	menuIDOpenSite = 2
	menuIDQuit     = 3

	// panelWidth and panelHeight are the quick panel in logical pixels: the same compact
	// box macOS shows, anchored to the cursor rather than to a menu-bar item.
	panelWidth  = 340
	panelHeight = 420

	// panelReopenGuard matches the macOS shell. Clicking the icon to dismiss the panel
	// queues the deactivation and the mouse-up back to back, and without the guard the panel
	// closes and immediately reopens, which reads as an icon that stopped answering.
	panelReopenGuard = 250 * time.Millisecond

	// Window sizes the operator cannot trade away: the four tabs and the routing table stop
	// being the same interface below this, and a window that covers the whole screen leaves
	// nothing behind it to look at.
	minimumWidth  = 720
	minimumHeight = 480

	// Shell messages posted to the host window. They have to go through the queue because
	// the senders include goroutines that are not on the UI thread.
	wmMsgTray       = wmApp + 1
	wmMsgEmbedMain  = wmApp + 2
	wmMsgEmbedPanel = wmApp + 3
	wmMsgQuitShell  = wmApp + 4
)

// shell is the running instance.
//
// Win32 window procedures are plain functions with no receiver, so the shell they act on is
// reachable through this variable for exactly as long as Run is on the stack.
var shell *winShell

// The window procedures are registered once per class. windows.NewCallback hands a Go
// function to native code that keeps the pointer forever, so the handles live in package
// variables rather than in the shell that comes and goes.
var (
	mainWindowProc  = windows.NewCallback(dispatchWindowMessage)
	panelWindowProc = windows.NewCallback(dispatchWindowMessage)
	hostWindowProc  = windows.NewCallback(dispatchWindowMessage)
)

type winShell struct {
	config  Config
	dataDir string

	// host is a zero-sized, never-shown window that owns the notification-area icon and the
	// popup menu. A message-only window cannot be used: the menu needs a window it can bring
	// to the foreground, and HWND_MESSAGE is not one.
	host  windows.Handle
	main  windows.Handle
	panel windows.Handle

	mainView  *webView
	panelView *webView

	// runtimeVersion is empty when the machine has no WebView2 runtime. It is the one failure
	// the shell can detect before it starts, and the one it must not crash on.
	runtimeVersion string

	minimizeToTray bool
	quickPanel     bool

	panelShown     bool
	panelDismissed time.Time
}

// Run starts the shell and blocks until it stops.
//
// The caller must be on the process's main thread: cmd/tunnelmesh-client-tray pins itself
// there in init, which is also what keeps the single-threaded COM apartment WebView2 wants
// on one thread for the life of the process.
func Run(config Config) error {
	if config.URL == "" {
		return errors.New("native: the settings window needs a URL to load")
	}
	if shell != nil {
		return errors.New("native: the tray shell is already running")
	}
	runtime.LockOSThread()
	release, err := acquireSingleInstance()
	if err != nil {
		return err
	}
	defer release()

	setProcessDPIAware()
	setAppUserModelID()

	instance := &winShell{
		config:         config,
		dataDir:        config.WebViewDataDir,
		minimizeToTray: config.MinimizeToTray,
		quickPanel:     config.QuickPanel,
		runtimeVersion: webview2Runtime(),
	}
	if instance.runtimeVersion == "" {
		log.Printf("tray: %v; the settings window will open in the system browser", errNoRenderer)
	}
	shell = instance
	if err := instance.start(); err != nil {
		shell = nil
		return err
	}
	instance.messageLoop()
	instance.teardown()
	shell = nil
	return nil
}

// Stop asks the shell to end its loop.
//
// It is safe from any goroutine, which is what lets the shutdown signal handler and the
// menu's 退出 both reach it: posting rather than calling keeps the run loop the only thing
// that touches windows on this thread.
func Stop() {
	if shell == nil {
		return
	}
	post(shell.host, wmMsgQuitShell)
}

// ShowWindow brings the settings window forward.
//
// With no WebView2 runtime there is no window to show, and the documented fallback is the
// system browser on the same loopback address: the settings stay reachable, which matters
// more than the shape they arrive in.
func ShowWindow() {
	if shell == nil {
		return
	}
	if !shell.hasRenderer() {
		_ = OpenURL(shell.config.URL)
		return
	}
	shell.showMain()
	notifyVisibility(true)
}

// HideWindow withdraws the settings window without quitting.
func HideWindow() {
	if shell == nil {
		return
	}
	shell.hideMain()
	notifyVisibility(false)
}

// SetMinimizeToTray updates what a window close means.
func SetMinimizeToTray(enabled bool) {
	if shell != nil {
		shell.minimizeToTray = enabled
	}
}

// SetQuickPanel updates what a left click on the notification-area icon means.
func SetQuickPanel(enabled bool) {
	if shell != nil {
		shell.quickPanel = enabled
	}
}

// RendererName identifies the web view that draws the interface, for the about tab.
//
// "browser" is the degraded case where no WebView2 runtime was found, and the interface
// turns it into an explanation the operator can read.
func RendererName() string {
	if shell == nil {
		return "webview2"
	}
	if shell.runtimeVersion == "" {
		return "browser"
	}
	return "webview2"
}

// RendererDetail is the runtime version, or the reason there is none.
func RendererDetail() string {
	if shell == nil || shell.runtimeVersion == "" {
		return errNoRenderer.Error()
	}
	return shell.runtimeVersion
}

// OpenURL hands a URL to the system browser.
func OpenURL(target string) error {
	if target == "" {
		return errors.New("native: no URL to open")
	}
	result, _, _ := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr("open"))),
		uintptr(unsafe.Pointer(utf16Ptr(target))), 0, 0, uintptr(windows.SW_SHOW))
	// ShellExecuteW reports failure as a small HINSTANCE: any value at or below 32 is one of
	// the SE_ERR_ codes rather than a handle.
	if result <= 32 {
		return windows.Errno(result)
	}
	return nil
}

// System describes the host for the about tab.
func System() SystemInfo {
	return SystemInfo{OS: "windows", OSVersion: windowsVersion(), Arch: archName()}
}

// PreferredLanguage is the operator's Windows display locale as a BCP-47 tag.
//
// The menu is built before the web view can report the locale it resolved, and a
// notification-area tooltip in the wrong language is visible before a window ever opens.
func PreferredLanguage() string {
	return registryString(windows.HKEY_CURRENT_USER, `Control Panel\International`, "LocaleName")
}

// start registers the window classes and creates every window the shell owns.
func (s *winShell) start() error {
	if err := registerClass(hostWindowClass, hostWindowProc, false); err != nil {
		return err
	}
	if err := registerClass(mainWindowClass, mainWindowProc, true); err != nil {
		return err
	}
	if err := registerClass(panelWindowClass, panelWindowProc, true); err != nil {
		return err
	}

	s.host = createWindow(0, hostWindowClass, "TunnelMesh Client", wsPopup, 0, 0, 0, 0, 0)
	if s.host == 0 {
		return errors.New("native: cannot create the tray host window")
	}
	if !s.setTrayIcon(nimAdd, s.tooltip()) {
		return errors.New("native: the notification area refused the tray icon")
	}

	s.main = createWindow(0, mainWindowClass, s.config.WindowTitle, wsOverlappedWindow,
		0, 0, 0, 0, s.host)
	if s.main == 0 {
		return errors.New("native: cannot create the settings window")
	}
	s.sizeMainWindow()

	if s.hasRenderer() {
		s.mainView = newWebView(s.main, s.config.URL, s.dataDir)
		if s.config.PanelURL != "" {
			s.panel = createWindow(wsExToolwindow|wsExTopmost, panelWindowClass, s.config.WindowTitle,
				wsPopup, 0, 0, 0, 0, s.host)
			if s.panel != 0 {
				s.panelView = newWebView(s.panel, s.config.PanelURL, s.dataDir)
			}
		}
	}

	// Embedding pumps messages until the runtime finishes creating the controller, so it is
	// scheduled inside the loop rather than run before it.
	post(s.host, wmMsgEmbedMain)
	if s.panel != 0 {
		post(s.host, wmMsgEmbedPanel)
	}
	return nil
}

// messageLoop is the standard Win32 pump. Stopping when GetMessage reports WM_QUIT is what
// lets Stop, a window close and a shutdown signal all end the process through one door.
func (s *winShell) messageLoop() {
	var message msg
	for {
		received, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(received) == 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
}

// teardown removes the notification-area icon before the window behind it goes away.
//
// Shell_NotifyIcon keeps a stale icon on the bar until Explorer restarts if the icon is left
// behind, and an icon that answers nothing is worse than no icon.
func (s *winShell) teardown() {
	s.mainView.shutdown()
	s.panelView.shutdown()
	s.setTrayIcon(nimDelete, "")
	for _, hwnd := range []windows.Handle{s.panel, s.main, s.host} {
		if hwnd != 0 {
			procDestroyWindow.Call(uintptr(hwnd))
		}
	}
}

// hasRenderer reports whether anything can be embedded at all.
func (s *winShell) hasRenderer() bool { return s.runtimeVersion != "" }

// dispatchWindowMessage is the one window procedure behind all three windows. Win32 hands a
// procedure no state, so it routes on the handle it was called with.
func dispatchWindowMessage(hwnd, message, wParam, lParam uintptr) uintptr {
	if shell == nil {
		return defWindowProc(hwnd, message, wParam, lParam)
	}
	switch uint32(message) {
	case wmGetMinMax:
		shell.enforceMinimumSize(hwnd, lParam)
		return 0
	case wmSize:
		shell.resizeView(hwnd, lParam)
		return 0
	case wmDpiChanged:
		shell.applyDpiChange(hwnd, lParam)
		return 0
	case wmClose:
		return shell.handleClose(hwnd)
	case wmActivate:
		return shell.handleActivate(hwnd, wParam)
	case wmMsgTray:
		shell.handleTrayMouse(lParam)
		return 0
	case wmMsgEmbedMain:
		shell.mainView.embed()
		return 0
	case wmMsgEmbedPanel:
		shell.panelView.embed()
		return 0
	case wmMsgQuitShell:
		procPostQuitMessage.Call(0)
		return 0
	}
	return defWindowProc(hwnd, message, wParam, lParam)
}

// resizeView keeps the web view glued to its window.
//
// The minimised size is zero, and a controller sized to zero starts logging lost surfaces,
// so a close is the point at which the web view is left where it is rather than shrunk.
func (s *winShell) resizeView(hwnd, lParam uintptr) {
	if uint32(lParam) == windows.SW_MINIMIZE {
		return
	}
	switch hwnd {
	case uintptr(s.main):
		s.mainView.resize()
	case uintptr(s.panel):
		s.panelView.resize()
	}
}

// handleClose applies the general tab's choice to a window close.
//
// Hiding keeps the tunnels up, which is the reason the tray exists; quitting is what the
// menu's 退出 means, and the window close means the same thing once the operator turns the
// minimize-to-tray switch off.
func (s *winShell) handleClose(hwnd uintptr) uintptr {
	switch hwnd {
	case uintptr(s.panel):
		s.hidePanel()
		return 0
	case uintptr(s.main):
		if s.shouldMinimize() {
			s.hideMain()
			notifyVisibility(false)
			return 0
		}
		s.dispatch(actionQuit)
		return 0
	}
	return defWindowProc(hwnd, wmClose, 0, 0)
}

// handleActivate dismisses the quick panel the moment it loses activation.
//
// The panel is a status glance rather than a window to keep around, and an operator who
// clicks anywhere else expects it gone. Windows reports the loss on the top-level window
// even though the web view holds the focus, which is why this is the only place to check.
func (s *winShell) handleActivate(hwnd, wParam uintptr) uintptr {
	if hwnd == uintptr(s.panel) && lowWord(wParam) == waInactive && s.panelShown {
		s.hidePanel()
		return 0
	}
	return defWindowProc(hwnd, wmActivate, wParam, 0)
}

// handleTrayMouse turns notification-area messages into a menu, a panel or a window.
//
// The mouse-up rather than the click-down is what is examined, because a menu opened on the
// down event closes again when the button is released over it.
func (s *winShell) handleTrayMouse(lParam uintptr) {
	switch uint32(lParam) {
	case wmLButtonUp:
		if s.quickPanel && s.panel != 0 {
			s.togglePanel()
			return
		}
		s.showMenu()
	case wmLButtonDblClk:
		ShowWindow()
	case wmRButtonUp, wmContextMenu:
		s.showMenu()
	}
}

// showMenu opens the three-item menu at the cursor.
//
// Taking the foreground before TrackPopupMenu and posting a null message afterwards is the
// documented workaround for a menu that stays on screen after a click elsewhere: without the
// ownership swap the menu never learns it lost activation.
func (s *winShell) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendMenu(menu, mfString, menuIDOpenMain, s.config.OpenMainLabel)
	appendMenu(menu, mfString, menuIDOpenSite, s.config.OpenWebsiteLabel)
	appendMenu(menu, mfSeparator, 0, "")
	appendMenu(menu, mfString, menuIDQuit, s.config.QuitLabel)

	position, _ := cursorPos()
	procSetForegroundWindow.Call(uintptr(s.host))
	command, _, _ := procTrackPopupMenu.Call(menu, uintptr(tpmRightButton|tpmReturnCmd),
		uintptr(position.x), uintptr(position.y), 0, uintptr(s.host), 0)
	post(s.host, wmNull)

	switch uint32(command) {
	case menuIDOpenMain:
		ShowWindow()
	case menuIDOpenSite:
		s.dispatch(actionOpenSite)
	case menuIDQuit:
		s.dispatch(actionQuit)
	}
}

// dispatch calls a Go handler from the shell's own thread.
func (s *winShell) dispatch(action int) {
	switch action {
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

// shouldMinimize asks Go what a window close means.
//
// The callback is preferred over the pushed flag because the operator can change the switch
// while the window is open; falling back to hide keeps the tunnels up when the preferences
// file cannot be read, which is the less damaging mistake of the two.
func (s *winShell) shouldMinimize() bool {
	if handlers.MinimizeToTray == nil {
		return s.minimizeToTray
	}
	return handlers.MinimizeToTray()
}

// togglePanel shows or hides the quick panel at the cursor.
func (s *winShell) togglePanel() {
	if s.panelShown {
		s.hidePanel()
		return
	}
	if time.Since(s.panelDismissed) < panelReopenGuard {
		// This click is the dismissal itself arriving again, not a request to reopen.
		return
	}
	s.showPanel()
}

func (s *winShell) showPanel() {
	if s.panel == 0 {
		return
	}
	position, _ := cursorPos()
	scale := scaleForWindow(s.panel)
	width := int32(float64(panelWidth) * scale)
	height := int32(float64(panelHeight) * scale)
	left, top := position.x-width/2, position.y-4
	// The panel is anchored to the monitor under the cursor, not to the whole virtual
	// desktop: on a second display whose origin is negative, clamping against the primary
	// work area would push it back onto the first screen.
	if monitor, found := monitorWorkArea(position); found {
		left, top = clampToRect(left, top, width, height, monitor)
	}
	placeWindow(s.panel, left, top, width, height, 0)
	procShowWindow.Call(uintptr(s.panel), windows.SW_SHOW)
	procSetForegroundWindow.Call(uintptr(s.panel))
	s.panelShown = true
	s.panelView.resize()
	applyTitlebarTheme(s.panel)
}

func (s *winShell) hidePanel() {
	if s.panel == 0 {
		return
	}
	procShowWindow.Call(uintptr(s.panel), windows.SW_HIDE)
	s.panelShown = false
	s.panelDismissed = time.Now()
}

func (s *winShell) showMain() {
	procShowWindow.Call(uintptr(s.main), windows.SW_RESTORE)
	procSetForegroundWindow.Call(uintptr(s.main))
	procBringWindowToTop.Call(uintptr(s.main))
	applyTitlebarTheme(s.main)
}

func (s *winShell) hideMain() {
	procShowWindow.Call(uintptr(s.main), windows.SW_HIDE)
}

// sizeMainWindow puts the window at the configured fraction of the work area and centres it.
//
// The work area rather than the screen: 50% of a display that still includes the taskbar is
// a window whose bottom row of controls hides behind it.
func (s *winShell) sizeMainWindow() {
	work, ok := workArea()
	if !ok {
		return
	}
	scale := scaleForWindow(s.main)
	width := float64(work.Right-work.Left) * fraction(s.config.WidthFraction)
	height := float64(work.Bottom-work.Top) * fraction(s.config.HeightFraction)
	if minimum := float64(minimumWidth) * scale; width < minimum {
		width = minimum
	}
	if minimum := float64(minimumHeight) * scale; height < minimum {
		height = minimum
	}
	left, top := clampToRect(
		work.Left+(work.Right-work.Left-int32(width))/2,
		work.Top+(work.Bottom-work.Top-int32(height))/2,
		int32(width), int32(height), work)
	placeWindow(s.main, left, top, int32(width), int32(height), swpNoZOrder|swpNoActivate)
}

// enforceMinimumSize keeps the window usable when it is dragged smaller.
func (s *winShell) enforceMinimumSize(hwnd, lParam uintptr) {
	if lParam == 0 {
		return
	}
	scale := scaleForWindow(hwndOf(hwnd))
	info := (*minMaxInfo)(pointerFromLParam(lParam))
	info.minTrackSize.x = int32(float64(minimumWidth) * scale)
	info.minTrackSize.y = int32(float64(minimumHeight) * scale)
}

// applyDpiChange moves and resizes to the rectangle the system suggests, then lets the web
// view follow, so a window dragged to a display at another scale ends up the same physical
// size rather than the same number of pixels.
func (s *winShell) applyDpiChange(hwnd, lParam uintptr) {
	if lParam == 0 {
		return
	}
	suggested := (*windows.Rect)(pointerFromLParam(lParam))
	placeWindow(hwndOf(hwnd), suggested.Left, suggested.Top,
		suggested.Right-suggested.Left, suggested.Bottom-suggested.Top, swpNoZOrder)
	shell.resizeView(hwnd, wmSize)
}

// tooltip is the notification-area hover text, with the renderer problem spelled out when
// there is one.
//
// The language comes from the labels the shell was handed rather than from a second
// preference read, so the tooltip cannot disagree with the menu under it.
func (s *winShell) tooltip() string {
	if s.hasRenderer() {
		return s.config.StatusTooltip
	}
	if hasChineseLabels(s.config) {
		return s.config.StatusTooltip + " · 需要安装 Microsoft Edge WebView2 运行时"
	}
	return s.config.StatusTooltip + " · Microsoft Edge WebView2 runtime required"
}

// setTrayIcon is the whole Shell_NotifyIcon call, sized for the current display so the icon
// stays readable on a 150% or 200% taskbar instead of being scaled by the shell.
func (s *winShell) setTrayIcon(message uint32, tooltip string) bool {
	if s.host == 0 {
		return false
	}
	edge := int(metric(smCxSmIcon))
	if edge <= 0 {
		edge = 16
	}
	data := notifyIconData{}
	data.cbSize = uint32(unsafe.Sizeof(data))
	data.hWnd = s.host
	data.uID = 1
	data.uCallbackMessage = wmMsgTray
	data.uFlags = nifMessage | nifIcon | nifTip
	data.hIcon = trayIcon(edge)
	copyUTF16(data.szTip[:], tooltip)
	r1, _, _ := procNotifyIconData.Call(uintptr(message), uintptr(unsafe.Pointer(&data)))
	return r1 != 0
}

// notifyVisibility tells Go what happened to the window.
func notifyVisibility(visible bool) {
	if handlers.WindowVisibilityChanged != nil {
		handlers.WindowVisibilityChanged(visible)
	}
}

// acquireSingleInstance takes the process-wide mutex and returns its release.
//
// The kernel owns the lifetime, so a tray killed by Task Manager leaves nothing behind that
// would stop the next one from starting.
func acquireSingleInstance() (func(), error) {
	name, err := windows.UTF16PtrFromString(singleInstanceMutex)
	if err != nil {
		return nil, err
	}
	// The prior value has to be cleared: CreateMutexW succeeds when it creates and when it
	// opens, and the last error is the only thing that tells the two apart.
	procSetLastError.Call(0)
	handle, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return nil, err
	}
	if code, _, _ := procGetLastErrorID.Call(); uint32(code) == errorAlreadyExists {
		return nil, errors.New("native: another TunnelMesh client tray is already running")
	}
	return func() { _ = windows.CloseHandle(windows.Handle(handle)) }, nil
}

// setProcessDPIAware asks for per-monitor v2 awareness before any window exists.
//
// The call is refused once a window has been created, and a shell that learns about scale
// factors late sizes its 50% window against the wrong display. shcore is the Windows 8.1
// fallback for a system that predates the context API.
func setProcessDPIAware() {
	r1, _, _ := procSetProcessDpiAwarenessCtx.Call(uintptr(dpiAwarenessPerMonitorV2))
	if r1 != 0 {
		return
	}
	procSetProcessDpiAware.Call(processPerMonitorAware)
}

// setAppUserModelID groups the taskbar button under the tray rather than under the console.
func setAppUserModelID() {
	value, err := windows.UTF16PtrFromString(appUserModelID)
	if err != nil {
		return
	}
	_, _, _ = procSetAppUserModelID.Call(uintptr(unsafe.Pointer(value)))
}

// registerClass installs one window class.
//
// The background brush is COLOR_WINDOW+1: the web view paints over the client area as soon
// as it exists, and a null brush would leave uninitialised pixels for the frame or two
// before the page arrives.
func registerClass(name string, procedure uintptr, withIcon bool) error {
	className, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	class := wndClassEx{
		lpfnWndProc: procedure,
		hCursor:     windows.Handle(cursor),
		hBackground: windows.Handle(1),
		className:   className,
	}
	if withIcon {
		class.hIcon = trayIcon(32)
		class.hIconSm = trayIcon(16)
	}
	class.cbSize = uint32(unsafe.Sizeof(class))
	atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return err
	}
	return nil
}

func createWindow(exStyle uintptr, class, title string, style uintptr, x, y, width, height int32, parent windows.Handle) windows.Handle {
	className, err := windows.UTF16PtrFromString(class)
	if err != nil {
		return 0
	}
	hwnd, _, _ := procCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr(title))), style, uintptr(x), uintptr(y),
		uintptr(width), uintptr(height), uintptr(parent), 0, 0, 0)
	return windows.Handle(hwnd)
}

// placeWindow moves, sizes and repaints in one call so the web view sees one WM_SIZE.
func placeWindow(hwnd windows.Handle, left, top, width, height int32, flags uintptr) {
	procSetWindowPos.Call(uintptr(hwnd), uintptr(hwndTop), uintptr(left), uintptr(top),
		uintptr(width), uintptr(height), flags)
}

// clampToRect keeps a box inside a work area, preferring to show the whole thing over
// keeping the requested corner.
func clampToRect(left, top, width, height int32, area windows.Rect) (int32, int32) {
	if left+width > area.Right {
		left = area.Right - width
	}
	if top+height > area.Bottom {
		top = area.Bottom - height
	}
	if left < area.Left {
		left = area.Left
	}
	if top < area.Top {
		top = area.Top
	}
	return left, top
}

func appendMenu(menu, flags, identifier uintptr, label string) {
	procAppendMenuW.Call(menu, flags, identifier, uintptr(unsafe.Pointer(utf16Ptr(label))))
}

func post(hwnd windows.Handle, message uint32) {
	procPostMessageW.Call(uintptr(hwnd), uintptr(message), 0, 0)
}

func defWindowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	r1, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r1
}

func metric(index int32) int32 {
	r1, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int32(r1)
}

func lowWord(value uintptr) uint32 { return uint32(value) & 0xFFFF }

// pointerFromLParam reinterprets a message parameter as the pointer the message documents.
//
// A window procedure receives its structures only through LPARAM, and the memory belongs to
// the operating system for the duration of the call, which is the case go vet's unsafeptr
// check cannot distinguish from the arithmetic it exists to catch. Reading through the
// variable rather than converting it keeps the check on for the rest of the module, and the
// one place that needs the reinterpretation is the one place with a comment.
func pointerFromLParam(value uintptr) unsafe.Pointer {
	parameter := value
	return *(*unsafe.Pointer)(unsafe.Pointer(&parameter))
}

func hwndOf(value uintptr) windows.Handle { return windows.Handle(value) }

// hasChineseLabels reports whether the shell was handed a Chinese menu. The labels are the
// only language signal the shell owns, and they are the ones the menu below the tooltip
// shows, so the two cannot drift.
func hasChineseLabels(config Config) bool {
	return strings.IndexFunc(config.OpenMainLabel+config.QuitLabel, func(r rune) bool {
		return r > unicode.MaxASCII
	}) >= 0
}
