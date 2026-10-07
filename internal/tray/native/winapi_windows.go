//go:build tray && windows

package native

// The Win32 surface the tray shell needs.
//
// golang.org/x/sys/windows covers the calling convention but not the windowing,
// notification-area or menu APIs, and importing a full Win32 binding would pull in a
// generated surface far larger than the tray uses. What is declared here is exactly the
// set the shell touches, loaded lazily so a function missing from an older build fails at
// the call site, where the shell can log it, rather than at process start.

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	procRegisterClassExW          = user32.NewProc("RegisterClassExW")
	procCreateWindowExW           = user32.NewProc("CreateWindowExW")
	procDefWindowProcW            = user32.NewProc("DefWindowProcW")
	procDestroyWindow             = user32.NewProc("DestroyWindow")
	procShowWindow                = user32.NewProc("ShowWindow")
	procSetForegroundWindow       = user32.NewProc("SetForegroundWindow")
	procBringWindowToTop          = user32.NewProc("BringWindowToTop")
	procIsWindowVisible           = user32.NewProc("IsWindowVisible")
	procGetWindowRect             = user32.NewProc("GetWindowRect")
	procSetWindowPos              = user32.NewProc("SetWindowPos")
	procGetClientRect             = user32.NewProc("GetClientRect")
	procGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
	procSystemParametersInfoW     = user32.NewProc("SystemParametersInfoW")
	procGetMessageW               = user32.NewProc("GetMessageW")
	procTranslateMessage          = user32.NewProc("TranslateMessage")
	procDispatchMessageW          = user32.NewProc("DispatchMessageW")
	procPostMessageW              = user32.NewProc("PostMessageW")
	procPostQuitMessage           = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu           = user32.NewProc("CreatePopupMenu")
	procAppendMenuW               = user32.NewProc("AppendMenuW")
	procDestroyMenu               = user32.NewProc("DestroyMenu")
	procTrackPopupMenu            = user32.NewProc("TrackPopupMenu")
	procGetCursorPos              = user32.NewProc("GetCursorPos")
	procMonitorFromPoint          = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW           = user32.NewProc("GetMonitorInfoW")
	procSetTimer                  = user32.NewProc("SetTimer")
	procKillTimer                 = user32.NewProc("KillTimer")
	procLoadCursorW               = user32.NewProc("LoadCursorW")
	procCreateIconFromResourceEx  = user32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon               = user32.NewProc("DestroyIcon")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDpiForWindow           = user32.NewProc("GetDpiForWindow")

	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")

	procNotifyIconData     = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW      = shell32.NewProc("ShellExecuteW")
	procSetAppUserModelID  = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procSetProcessDpiAware = shcore.NewProc("SetProcessDpiAwareness")
	procCreateMutexW       = kernel32.NewProc("CreateMutexW")
	procSetLastError       = kernel32.NewProc("SetLastError")
	procGetLastErrorID     = kernel32.NewProc("GetLastError")
)

// Window messages and styles used by the shell. Named because a bare 0x02E0 in a window
// procedure is unreadable, and the values are part of the Win32 contract, not ours.
const (
	wmNull          = 0x0000
	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmActivate      = 0x0006
	wmClose         = 0x0010
	wmQuit          = 0x0012
	wmGetMinMax     = 0x0024
	wmContextMenu   = 0x007B
	wmLButtonUp     = 0x0202
	wmLButtonDown   = 0x0201
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmDpiChanged    = 0x02E0
	wmApp           = 0x8000

	waInactive = 0
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsPopup            = 0x80000000
	wsVisible          = 0x10000000

	wsExToolwindow = 0x00000080
	wsExTopmost    = 0x00000008
	wsExAppwindow  = 0x00040000
	wsExWindowEdge = 0x00000100
)

const (
	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
	swpFrameChg   = 0x0020

	hwndTop        = ^uintptr(0)
	hwndNotTopmost = ^uintptr(1)

	smCxIcon   = 11
	smCyIcon   = 12
	smCxSmIcon = 49
	smCySmIcon = 50

	spiGetWorkArea = 0x0030

	monitorDefaultToNull    = 1
	monitorDefaultToPrimary = 0

	dwmWaUseImmersiveDarkMode = 20

	mfString    = 0x00000000
	mfSeparator = 0x00000800

	tpmReturnCmd   = 0x00000100
	tpmRightButton = 0x00000002

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	idcArrow = uintptr(32512)

	dpiAwarenessPerMonitorV2 = ^uintptr(3)
	processPerMonitorAware   = 2

	iconVersion3 = 0x00030000

	// errorAlreadyExists is what CreateMutexW leaves behind when another tray already holds
	// the named mutex. x/sys spells the constant differently across releases, so it is
	// written once here.
	errorAlreadyExists = 183
)

type point struct{ x, y int32 }

// minMaxInfo is MINMAXINFO: five POINTs, the third of which is the one the shell sets.
type minMaxInfo struct {
	reserved     point
	maxSize      point
	maxPosition  point
	minTrackSize point
	maxTrackSize point
}

// msg is MSG without the cbSize field that only exists on x64; the layout below matches
// both, because the trailing POINT keeps the struct 8-byte aligned either way.
type msg struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

// wndClassEx is WNDCLASSEXW. cbSize is set from unsafe.Sizeof so the padding the C header
// relies on is the padding the compiler gives us.
type wndClassEx struct {
	cbSize      uint32
	style       uint32
	lpfnWndProc uintptr
	cbClsExtra  int32
	cbWndExtra  int32
	hInstance   windows.Handle
	hIcon       windows.Handle
	hCursor     windows.Handle
	hBackground windows.Handle
	menuName    *uint16
	className   *uint16
	hIconSm     windows.Handle
}

// notifyIconData is NOTIFYICONDATAW. The two unions (uVersion/timeout and the trailing
// overlay handle) are widened to the largest member, which is what the header does too.
type notifyIconData struct {
	cbSize           uint32
	hWnd             windows.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            windows.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwFlags          uint32
	guidItem         [16]byte
	hIconOverlay     windows.Handle
	dwReserved       uint32
}

// monitorInfo is MONITORINFO: the header repeats the size because GetMonitorInfoW refuses
// a structure it cannot identify.
type monitorInfo struct {
	cbSize    uint32
	rectangle windows.Rect
	workArea  windows.Rect
	flags     uint32
}

// bitmapInfoHeader is the BITMAPINFOHEADER used to hand an icon's pixels to
// CreateIconFromResourceEx.
type bitmapInfoHeader struct {
	size          uint32
	width         int32
	height        int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPelsPerMeter int32
	yPelsPerMeter int32
	clrUsed       uint32
	clrImportant  uint32
}

// utf16Ptr converts a Go string, failing loudly only for embedded NULs, which no label in
// this shell can contain.
func utf16Ptr(value string) *uint16 {
	converted, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return nil
	}
	return converted
}

// copyUTF16 writes a string into a fixed UTF-16 buffer as Win32 structures expect, always
// keeping room for the terminator.
func copyUTF16(buffer []uint16, value string) {
	for i := range buffer {
		buffer[i] = 0
	}
	converted, err := windows.UTF16FromString(value)
	if err != nil {
		return
	}
	copy(buffer, converted)
}

// scaleForWindow turns a logical (96 dpi) size into physical pixels for one window, which
// is how the panel keeps the same visual size macOS gives it at any display scale.
func scaleForWindow(hwnd windows.Handle) float64 {
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

// workArea returns the desktop minus taskbars and app-docked windows, in physical pixels.
// Sizing the window against it rather than against the full screen is what keeps a 50%
// window from being placed under the taskbar.
func workArea() (windows.Rect, bool) {
	var rect windows.Rect
	r1, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&rect)), 0)
	return rect, r1 != 0
}

// cursorPos is the mouse position in physical screen coordinates.
func cursorPos() (point, bool) {
	var position point
	r1, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&position)))
	return position, r1 != 0
}
