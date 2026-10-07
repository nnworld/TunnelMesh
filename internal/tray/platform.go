package tray

import "runtime"

// Platform identifiers exposed to the settings window.
//
// The interface used to say "macOS" and "menu bar" in its own wording, which was true
// while there was only one shell. With a Windows shell over the same logic, the wording
// has to follow the platform the binary actually runs on, so the name is decided here and
// the front end only ever chooses between a small closed set of strings.
const (
	PlatformMacOS   = "macos"
	PlatformWindows = "windows"
)

// platformName maps a Go GOOS value onto the interface vocabulary. Anything without a
// shell of its own maps to the empty string, which the front end renders with neutral
// wording rather than guessing.
func platformName(goos string) string {
	switch goos {
	case "darwin":
		return PlatformMacOS
	case "windows":
		return PlatformWindows
	default:
		return ""
	}
}

// Platform is the platform name for this process.
func Platform() string { return platformName(runtime.GOOS) }
