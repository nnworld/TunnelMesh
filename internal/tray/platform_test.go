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
	if !strings.Contains(string(encoded), `"platform":`) {
		t.Fatalf("settings payload has no platform field: %s", encoded)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "token") {
		t.Fatalf("settings payload mentions a token: %s", encoded)
	}
}
