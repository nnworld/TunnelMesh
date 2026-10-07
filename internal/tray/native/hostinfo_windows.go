//go:build tray && windows

package native

// Host facts and display geometry that the shell needs and Win32 spreads over three APIs.
//
// They live apart from the message loop because none of them is about windows: the about
// tab shows a Windows version, the menu needs a locale, and the quick panel needs to know
// which monitor the cursor is on.

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// registryString reads one REG_SZ, or the empty string.
//
// Every reader here treats a missing key as "unknown" rather than as an error: the about tab
// has to render something, and a tray that refuses to start because a version string moved
// would be a worse outcome than a version string that is missing.
func registryString(root registry.Key, path, name string) string {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _, err := key.GetStringValue(name)
	if err != nil {
		return ""
	}
	return value
}

// registryDWORD reads one REG_DWORD, reporting whether it was there.
func registryDWORD(root registry.Key, path, name string) (uint32, bool) {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer key.Close()
	value, _, err := key.GetIntegerValue(name)
	if err != nil {
		return 0, false
	}
	return uint32(value), true
}

// windowsVersion spells the OS the way an operator would recognise it.
//
// The registry is the source: GetVersionEx is deliberately wrong for applications that do
// not declare themselves compatible with Windows 10, which the tray does not. Windows 11
// also keeps reporting "Windows 10" in ProductName, so the build number is what separates
// the two rather than the product name.
func windowsVersion() string {
	const path = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	build := registryString(registry.LOCAL_MACHINE, path, "CurrentBuildNumber")
	edition := registryString(registry.LOCAL_MACHINE, path, "ProductName")
	display := registryString(registry.LOCAL_MACHINE, path, "DisplayVersion")

	family := edition
	if number, err := strconv.Atoi(build); err == nil {
		switch {
		case number >= 22000:
			family = "Windows 11"
		case strings.HasPrefix(edition, "Windows 10"):
			family = "Windows 10"
		}
	}
	parts := []string{family}
	if display != "" {
		parts = append(parts, display)
	}
	if build != "" {
		parts = append(parts, "Build "+build)
	}
	return strings.Join(filterEmpty(parts), " · ")
}

// archName is the architecture of the machine, not of this executable.
//
// The two differ on an ARM64 Windows running the amd64 build through emulation, and the about
// tab is where an operator checks whether they installed the right package. The environment
// variable is what the emulator itself publishes for that reason: it names the host, where
// runtime.GOARCH would name the instruction set this process was compiled for.
func archName() string {
	switch strings.ToUpper(os.Getenv("PROCESSOR_ARCHITECTURE")) {
	case "AMD64", "X64":
		return "amd64"
	case "ARM64":
		return "arm64"
	case "INTEL", "X86":
		return "386"
	default:
		return runtime.GOARCH
	}
}

// monitorWorkArea returns the desktop area of the monitor under a point, minus its taskbar.
//
// The panel is clamped to that rather than to the virtual desktop, because on a second
// display whose origin is negative, clamping to the primary work area slides the panel back
// onto the first screen and leaves the operator wondering where their click went.
func monitorWorkArea(at point) (windows.Rect, bool) {
	// MONITOR_DEFAULTTONEAREST keeps a panel under a cursor parked on a bezel between two
	// displays from being placed on a monitor the cursor is not on.
	const monitorDefaultToNearest = 2
	handle, _, _ := procMonitorFromPoint.Call(uintptr(unsafe.Pointer(&at)), monitorDefaultToNearest)
	if handle == 0 {
		return windows.Rect{}, false
	}
	info := monitorInfo{}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r1, _, _ := procGetMonitorInfoW.Call(handle, uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		return windows.Rect{}, false
	}
	return info.workArea, true
}

// applyTitlebarTheme matches the window frame to the system's light or dark app mode.
//
// The page follows the tray's own theme setting; the frame is drawn by the system and would
// otherwise stay light in a dark taskbar. Reading the personalisation key rather than the
// saved preference is deliberate: the setting window is created before the web view has
// reported the theme it resolved, and a frame that changes colour mid-paint looks broken.
func applyTitlebarTheme(hwnd windows.Handle) {
	if hwnd == 0 {
		return
	}
	light, ok := registryDWORD(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, "AppsUseLightTheme")
	if !ok {
		return
	}
	dark := uint32(0)
	if light == 0 {
		dark = 1
	}
	value := uintptr(unsafe.Pointer(&dark))
	// 20 is DWMWA_USE_IMMERSIVE_DARK_MODE; 19 is the same attribute under the build in which
	// it first shipped, and the call that fails costs nothing.
	r1, _, _ := procDwmSetWindowAttribute.Call(uintptr(hwnd), dwmWaUseImmersiveDarkMode, value, unsafe.Sizeof(dark))
	if r1 != 0 {
		procDwmSetWindowAttribute.Call(uintptr(hwnd), dwmWaUseImmersiveDarkMode-1, value, unsafe.Sizeof(dark))
	}
}

func filterEmpty(values []string) []string {
	kept := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			kept = append(kept, value)
		}
	}
	return kept
}
