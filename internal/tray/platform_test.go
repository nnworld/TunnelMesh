package tray

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

// TestPlatformNameMapsGoosToTheInterfaceVocabulary pins the value the settings window
// uses to pick its wording. The interface must not hardcode "macOS" once there are two
// shells, so the name is derived here and stays a small closed set.
func TestPlatformNameMapsGoosToTheInterfaceVocabulary(t *testing.T) {
	cases := map[string]string{
		"darwin":  PlatformMacOS,
		"windows": PlatformWindows,
		"linux":   "",
		"":        "",
	}
	for goos, want := range cases {
		if got := platformName(goos); got != want {
			t.Fatalf("platformName(%q) = %q, want %q", goos, got, want)
		}
	}
	if got := platformName(runtime.GOOS); got != Platform() {
		t.Fatalf("Platform() = %q, want %q for GOOS %s", Platform(), got, runtime.GOOS)
	}
}

// assertPlatformField checks the settings payload for the platform field the window
// branches on. A known platform must be reported; the empty one (an untagged build, or a
// GOOS with no tray shell) must not be reported as something else, and `omitempty` is
// allowed to drop the key entirely - the front end then uses its neutral wording.
func assertPlatformField(t *testing.T, encoded []byte, want string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode settings payload: %v", err)
	}
	value, ok := payload["platform"]
	if want == "" {
		if ok && value != "" {
			t.Fatalf("an unknown platform must not claim %v", value)
		}
		return
	}
	if !ok {
		t.Fatalf("platform %q: settings payload has no platform field: %s", want, encoded)
	}
	if value != want {
		t.Fatalf("platform = %v, want %q", value, want)
	}
}

// TestPlatformFieldContract covers the JSON shape the settings window branches on. The
// empty value is not hypothetical: an untagged or unsupported GOOS builds and tests this
// package on Linux, where Platform() is empty, so the contract has to say what that means
// instead of asserting a key that encoding::omitempty is free to drop.
func TestPlatformFieldContract(t *testing.T) {
	for _, platform := range []string{PlatformMacOS, PlatformWindows, ""} {
		encoded, err := json.Marshal(SettingsView{Platform: platform})
		if err != nil {
			t.Fatalf("marshal %q: %v", platform, err)
		}
		assertPlatformField(t, encoded, platform)
	}
}

// TestSettingsCarriesThePlatformWithoutCarryingSecrets keeps the new field honest: it is
// the only addition to the general tab payload, and the tab must never leak a token.
func TestSettingsCarriesThePlatformWithoutCarryingSecrets(t *testing.T) {
	app, _ := newTestApp(t, nil)
	settings, err := app.Settings(context.Background())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings.Platform != Platform() {
		t.Fatalf("settings.Platform = %q, want %q", settings.Platform, Platform())
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	assertPlatformField(t, encoded, Platform())
	if strings.Contains(strings.ToLower(string(encoded)), "token") {
		t.Fatalf("settings payload mentions a token: %s", encoded)
	}
}
