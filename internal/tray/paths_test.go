package tray

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigDirHonoursXDG(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	if got, err := DefaultConfigDir(); err != nil || got != filepath.Join(xdg, ConfigDirName) {
		t.Fatalf("DefaultConfigDir() = %q, %v, want %q", got, err, filepath.Join(xdg, ConfigDirName))
	}

	t.Setenv("XDG_CONFIG_HOME", "   ")
	got, err := DefaultConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	// The documented default is ~/.config/tunnelmesh even on macOS, where
	// os.UserConfigDir would point at ~/Library/Application Support instead.
	if want := filepath.Join(home, ".config", ConfigDirName); got != want {
		t.Fatalf("DefaultConfigDir() = %q, want %q", got, want)
	}
}

func TestDefaultConfigDirWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := DefaultConfigDir(); err == nil {
		t.Fatal("an unresolvable home directory must fail rather than silently use a relative path")
	}
}

// TestDefaultPathsSeparatesPreferencesFromConfiguration pins the bootstrap order:
// preferences must be findable before the configured directory is known, so they
// cannot live inside the directory they select.
func TestDefaultPathsSeparatesPreferencesFromConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(home, ".config", ConfigDirName)
	if paths.ConfigDir != wantDir || paths.PrefsDir != wantDir {
		t.Fatalf("paths = %+v, want both dirs %q", paths, wantDir)
	}
	if paths.Prefs != filepath.Join(wantDir, PrefsFileName) {
		t.Fatalf("Prefs = %q", paths.Prefs)
	}
	if paths.ClientYAML != filepath.Join(wantDir, ClientConfigFileName) {
		t.Fatalf("ClientYAML = %q", paths.ClientYAML)
	}
	if paths.Lock != filepath.Join(wantDir, LockFileName) {
		t.Fatalf("Lock = %q", paths.Lock)
	}
	if !strings.HasSuffix(paths.Lock, "client.lock") {
		t.Fatalf("Lock = %q, want the client.lock name the runtime derives", paths.Lock)
	}
}

func TestResolvePathsUsesConfiguredDirForConfigurationOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	custom := filepath.Join(t.TempDir(), "elsewhere")
	paths, err := ResolvePaths(custom)
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigDir != custom || paths.ClientYAML != filepath.Join(custom, ClientConfigFileName) {
		t.Fatalf("paths = %+v, want configuration under %q", paths, custom)
	}
	if paths.PrefsDir != filepath.Join(home, ".config", ConfigDirName) {
		t.Fatalf("PrefsDir = %q, want the default directory", paths.PrefsDir)
	}
}

func TestResolvePathsRejectsBlank(t *testing.T) {
	if _, err := ResolvePaths("   "); err == nil {
		t.Fatal("a blank configuration directory must be rejected")
	}
}

func TestResolvePathsExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	paths, err := ResolvePaths("~/tm")
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigDir != filepath.Join(home, "tm") {
		t.Fatalf("ConfigDir = %q, want %q", paths.ConfigDir, filepath.Join(home, "tm"))
	}
	_ = os.Getenv("HOME")
}
