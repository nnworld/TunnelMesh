package tray

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Supported interface languages. LanguageSystem means "follow the operating system",
// which is the default and is represented by a sentinel rather than by an empty
// string so an unset value and a deliberate choice stay distinguishable.
const (
	LanguageSystem = "system"
	LanguageZhCN   = "zh-CN"
	LanguageEnUS   = "en-US"
)

// Supported appearance themes.
const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
)

// Preferences are the tray-only settings from the general tab.
type Preferences struct {
	Language string `json:"language"`
	Theme    string `json:"theme"`
	// ConfigDir is empty until an operator chooses one, which means "use the
	// resolved default". Persisting the resolved default instead would freeze it and
	// stop following XDG_CONFIG_HOME.
	ConfigDir string `json:"configDir"`
	// LaunchAtLogin is a pointer so "never asked" stays distinct from "turned off".
	// The requirement is that launch at login is on the first time the tray opens,
	// and an operator who turns it off must not have it switched back on.
	LaunchAtLogin *bool `json:"launchAtLogin,omitempty"`
	// MinimizeToTray decides whether closing the window hides it or quits the tray.
	MinimizeToTray bool `json:"minimizeToTray"`
	// QuickPanel turns the menu-bar left click into a compact status panel. It is off by
	// default: a tray that stops showing its menu on its own is a behaviour change the
	// operator has to ask for, and a missing key in an older tray.json has to keep the
	// menu.
	QuickPanel bool `json:"quickPanel"`
}

// LaunchAtLoginEnabled reports the effective launch-at-login choice.
func (p Preferences) LaunchAtLoginEnabled() bool {
	return p.LaunchAtLogin == nil || *p.LaunchAtLogin
}

// DefaultPreferences returns the documented first-run defaults.
func DefaultPreferences() Preferences {
	return Preferences{
		Language:       LanguageSystem,
		Theme:          ThemeSystem,
		LaunchAtLogin:  boolPtr(true),
		MinimizeToTray: true,
	}
}

// PrefsStore reads and writes the preferences file.
//
// It is safe for concurrent use: the settings window can save while the tray reads
// the same values to decide how to handle a window close.
type PrefsStore struct {
	path string

	mu     sync.Mutex
	cached *Preferences
}

// NewPrefsStore binds a store to a preferences path.
func NewPrefsStore(path string) *PrefsStore { return &PrefsStore{path: path} }

// Path returns the backing file.
func (s *PrefsStore) Path() string { return s.path }

// Load returns the stored preferences, falling back to the defaults.
//
// A missing, unreadable or corrupt file yields defaults rather than an error. The
// tray must still open when its own preferences are damaged: refusing to start would
// leave the operator with no interface to fix them through, and the tunnels would
// stay down for a cosmetic reason.
func (s *PrefsStore) Load() (Preferences, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil {
		return *s.cached, nil
	}
	prefs := DefaultPreferences()
	data, err := os.ReadFile(s.path)
	if err == nil {
		var stored Preferences
		if json.Unmarshal(data, &stored) == nil {
			prefs = normalizePreferences(stored)
		}
	}
	s.cached = &prefs
	return prefs, nil
}

// Save writes the preferences atomically with owner-only permissions.
//
// The write goes to a temporary file in the same directory and is renamed into place
// so a crash mid-write cannot leave a truncated file that the next start would read
// as "no preferences".
func (s *PrefsStore) Save(prefs Preferences) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefs = normalizePreferences(prefs)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := atomicWrite(s.path, data, 0o600); err != nil {
		return err
	}
	s.cached = &prefs
	return nil
}

// normalizePreferences replaces values the interface does not support with the
// documented defaults. The file is user-editable, so an unknown locale or theme has
// to degrade to something renderable instead of producing an empty interface.
func normalizePreferences(prefs Preferences) Preferences {
	if !validLanguage(prefs.Language) {
		prefs.Language = LanguageSystem
	}
	if !validTheme(prefs.Theme) {
		prefs.Theme = ThemeSystem
	}
	if prefs.LaunchAtLogin == nil {
		prefs.LaunchAtLogin = boolPtr(true)
	}
	return prefs
}

func validLanguage(value string) bool {
	switch value {
	case LanguageSystem, LanguageZhCN, LanguageEnUS:
		return true
	default:
		return false
	}
}

func validTheme(value string) bool {
	switch value {
	case ThemeSystem, ThemeLight, ThemeDark:
		return true
	default:
		return false
	}
}

// atomicWrite writes data to path through a temporary file in the same directory.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	// The permission is applied explicitly rather than relying on CreateTemp's 0600
	// combined with the umask, because the configuration may hold a token.
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

func boolPtr(value bool) *bool { return &value }
