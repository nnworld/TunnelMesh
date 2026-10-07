package tray

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPreferencesMatchDocumentedDefaults(t *testing.T) {
	prefs := DefaultPreferences()
	if prefs.Language != LanguageSystem {
		t.Fatalf("Language = %q, want follow-system", prefs.Language)
	}
	if prefs.Theme != ThemeSystem {
		t.Fatalf("Theme = %q, want follow-system", prefs.Theme)
	}
	// The requirement is that launch-at-login is on the first time the tray opens.
	if prefs.LaunchAtLogin == nil || !*prefs.LaunchAtLogin {
		t.Fatalf("LaunchAtLogin = %v, want true on first run", prefs.LaunchAtLogin)
	}
	if !prefs.MinimizeToTray {
		t.Fatal("MinimizeToTray = false, want a background-resident tray by default")
	}
	if prefs.ConfigDir != "" {
		t.Fatalf("ConfigDir = %q, want empty so the resolved default is used", prefs.ConfigDir)
	}
}

func TestPreferencesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewPrefsStore(filepath.Join(dir, PrefsFileName))

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load on a missing file must succeed with defaults: %v", err)
	}
	if loaded.Language != LanguageSystem || loaded.LaunchAtLogin == nil || !*loaded.LaunchAtLogin {
		t.Fatalf("defaults = %+v", loaded)
	}

	want := Preferences{
		Language:       LanguageZhCN,
		Theme:          ThemeDark,
		ConfigDir:      filepath.Join(dir, "cfg"),
		LaunchAtLogin:  boolPtr(false),
		MinimizeToTray: false,
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != want.Language || got.Theme != want.Theme || got.ConfigDir != want.ConfigDir {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if got.LaunchAtLogin == nil || *got.LaunchAtLogin {
		t.Fatalf("LaunchAtLogin = %v, want an explicit false to survive", got.LaunchAtLogin)
	}
	if got.MinimizeToTray {
		t.Fatal("MinimizeToTray = true, want the stored false")
	}

	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("preferences permissions = %v, want no group or world access", info.Mode().Perm())
	}
}

// TestPreferencesRejectUnknownValues covers a hand-edited file. Falling back to the
// documented default beats rendering an interface in a locale that does not exist.
func TestPreferencesRejectUnknownValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, PrefsFileName)
	if err := os.WriteFile(path, []byte(`{"language":"fr-FR","theme":"solarized","launchAtLogin":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewPrefsStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != LanguageSystem || got.Theme != ThemeSystem {
		t.Fatalf("unknown values = %+v, want follow-system fallbacks", got)
	}
	if got.LaunchAtLogin == nil || !*got.LaunchAtLogin {
		t.Fatalf("LaunchAtLogin = %v, want the first-run default when unset", got.LaunchAtLogin)
	}
}

func TestPreferencesCorruptFileFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, PrefsFileName)
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewPrefsStore(path).Load()
	if err != nil {
		t.Fatalf("a corrupt preferences file must not stop the tray from opening: %v", err)
	}
	if got.Theme != ThemeSystem {
		t.Fatalf("theme = %q, want the default", got.Theme)
	}
}

func TestValidLanguagesAndThemes(t *testing.T) {
	for _, value := range []string{LanguageSystem, LanguageZhCN, LanguageEnUS} {
		if !validLanguage(value) {
			t.Fatalf("validLanguage(%q) = false", value)
		}
	}
	if validLanguage("fr-FR") || validLanguage("") {
		t.Fatal("validLanguage accepted an unsupported value")
	}
	for _, value := range []string{ThemeSystem, ThemeLight, ThemeDark} {
		if !validTheme(value) {
			t.Fatalf("validTheme(%q) = false", value)
		}
	}
	if validTheme("solarized") || validTheme("") {
		t.Fatal("validTheme accepted an unsupported value")
	}
}

// TestQuickPanelDefaultsToOff pins the requirement that the menu-bar quick panel is off
// until the operator asks for it. A tray that hijacks the left click on first run would
// be a behaviour change nobody consented to.
func TestQuickPanelDefaultsToOff(t *testing.T) {
	if DefaultPreferences().QuickPanel {
		t.Fatal("QuickPanel = true, want the quick panel off by default")
	}
	dir := t.TempDir()
	store := NewPrefsStore(filepath.Join(dir, PrefsFileName))
	prefs, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if prefs.QuickPanel {
		t.Fatalf("loaded QuickPanel = true, want false for a file that never mentioned it")
	}

	// A hand written file that predates the field must load as "off" rather than as
	// "unset", which is what keeps an upgrade from flipping the behaviour.
	if err := os.WriteFile(store.Path(), []byte(`{"language":"zh-CN","minimizeToTray":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened := NewPrefsStore(store.Path())
	legacy, err := reopened.Load()
	if err != nil {
		t.Fatal(err)
	}
	if legacy.QuickPanel || legacy.Language != LanguageZhCN {
		t.Fatalf("legacy file loaded as %+v", legacy)
	}
}

// TestQuickPanelRoundTrip covers the general tab's radio button: the choice has to
// survive a restart of the tray, not just the current session.
func TestQuickPanelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewPrefsStore(filepath.Join(dir, PrefsFileName))
	if err := store.Save(Preferences{QuickPanel: true}); err != nil {
		t.Fatal(err)
	}
	got, err := NewPrefsStore(store.Path()).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.QuickPanel {
		t.Fatalf("QuickPanel = false, want the stored true; stored file = %+v", got)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"quickPanel": true`) {
		t.Fatalf("stored preferences = %s, want an explicit quickPanel key", data)
	}

	// Turning it back off has to be persisted too: the cache must not resurrect true.
	if err := store.Save(Preferences{QuickPanel: false}); err != nil {
		t.Fatal(err)
	}
	off, err := NewPrefsStore(store.Path()).Load()
	if err != nil {
		t.Fatal(err)
	}
	if off.QuickPanel {
		t.Fatal("QuickPanel = true after being turned off")
	}
}
