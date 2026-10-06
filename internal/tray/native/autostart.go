//go:build tray && darwin

package native

import "sync"

// Autostart adapts the macOS login item to tray.AutostartManager.
//
// It lives here rather than in internal/tray on purpose: internal/tray is the tray's
// logic and must build with CGO_ENABLED=0 on every platform, so the only code allowed to
// reach SMAppService is the code already behind the "tray" build tag.
type Autostart struct {
	// mu serializes registration because SMAppService keeps one system-wide record per
	// bundle: two concurrent register calls would race on it.
	mu sync.Mutex
}

// NewAutostart builds the login-item manager.
func NewAutostart() *Autostart { return &Autostart{} }

// Supported reports whether this macOS exposes login items.
func (a *Autostart) Supported() bool { return LoginItemStatusOf().Supported() }

// Enabled reads the registration state.
//
// RequiresApproval is reported as enabled-with-a-problem rather than as off: the item is
// registered, and telling the operator it is off would invite them to register it again.
func (a *Autostart) Enabled() (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch LoginItemStatusOf() {
	case LoginItemEnabled:
		return true, nil
	case LoginItemRequiresApproval:
		return true, nil
	case LoginItemNotRegistered, LoginItemNotFound:
		return false, nil
	case LoginItemUnsupported:
		return false, nil
	default:
		return false, nil
	}
}

// SetEnabled registers or unregisters the app bundle as a login item.
func (a *Autostart) SetEnabled(enabled bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	status := LoginItemStatusOf()
	if status == LoginItemUnsupported {
		return errUnsupported
	}
	return LoginItemSet(enabled)
}
