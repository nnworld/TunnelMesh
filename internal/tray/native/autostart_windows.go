//go:build tray && windows

package native

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// runKey and runValue are where Windows looks when it starts a user's session.
//
// HKEY_CURRENT_USER needs no elevation, which is what keeps the installer per-user and the
// switch usable for an operator who is not an administrator. The machine-wide Run key would
// need one, and a tray installed into one user's profile has no business starting in another
// user's session.
const (
	runKey        = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue      = "TunnelMeshClient"
	quoteSurround = '"'
)

// Autostart keeps the general tab's "start when I sign in" switch backed by the registry.
//
// It lives here rather than in internal/tray because that package must build with
// CGO_ENABLED=0 on every platform, and the only code allowed to know which platforms can is
// the code already behind the "tray" build tag.
type Autostart struct{}

// NewAutostart builds the start-up manager.
func NewAutostart() *Autostart { return &Autostart{} }

// Supported is always true: the Run key exists on every Windows the tray builds for.
func (a *Autostart) Supported() bool { return true }

// Enabled reports whether the value points at this executable.
//
// The path is part of the answer rather than an implementation detail: an application moved
// or reinstalled elsewhere leaves a value behind that starts nothing, and a switch that reads
// "on" over a dead path is lying in the interface.
func (a *Autostart) Enabled() (bool, error) {
	recorded, found, err := a.read()
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	current, err := executablePath()
	if err != nil {
		return false, err
	}
	return samePath(recorded, current), nil
}

// SetEnabled writes or removes the value.
//
// Writing the current path every time is what makes a moved installation self-healing: the
// tray calls this at start-up through SyncAutostart, so the login entry follows the exe
// without the operator touching the switch.
func (a *Autostart) SetEnabled(enabled bool) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if !enabled {
		if err := key.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	current, err := executablePath()
	if err != nil {
		return err
	}
	return key.SetStringValue(runValue, quote(current))
}

func (a *Autostart) read() (string, bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer key.Close()
	value, _, err := key.GetStringValue(runValue)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return value, true, nil
}

// executablePath is the running binary, which is also the installed one: the tray is a
// single file inside the install directory, not a helper launched out of a bundle.
func executablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

// quote wraps the path the way Run values expect, because a path under "C:\Users\Some Name\"
// is otherwise two arguments.
func quote(path string) string {
	return string(quoteSurround) + path + string(quoteSurround)
}

// samePath compares a stored command with this executable, allowing for the quoting and for
// the case-insensitive filesystem.
func samePath(recorded, current string) bool {
	stored := strings.TrimSpace(recorded)
	if len(stored) >= 2 && stored[0] == quoteSurround && stored[len(stored)-1] == quoteSurround {
		stored = stored[1 : len(stored)-1]
	}
	clean := func(value string) string {
		return strings.ToLower(strings.TrimRight(filepath.Clean(value), "\\"))
	}
	return clean(stored) == clean(current)
}
