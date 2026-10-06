package macos

import (
	"strings"
	"testing"
)

// trayShim is the Objective-C source of the macOS shell.
//
// It is compiled only under "-tags tray" on darwin, so no test in the default build can
// execute it. These guards pin the behaviour that was verified on a macOS host by driving a
// WKWebView with synthetic key events: with no main menu ⌘V leaves the field empty, with a
// main menu carrying the standard Edit actions ⌘V pastes, and with a local key-down monitor
// that re-dispatches ⌃V through the responder chain Ctrl+V pastes too.
const trayShim = "../../internal/tray/native/tray_darwin.m"

// TestShimInstallsAMainMenuForEditingShortcuts is the paste regression.
//
// NSApplication matches ⌘C, ⌘V and ⌘Z against the *main menu* before the key window ever
// sees the event. A menu-bar (accessory) application that only builds an NSStatusItem menu
// has no main menu at all, so every text field in the settings window silently refuses the
// clipboard shortcuts and the operator reads that as a broken application.
func TestShimInstallsAMainMenuForEditingShortcuts(t *testing.T) {
	shim := readOrFail(t, trayShim)

	if !strings.Contains(shim, "setMainMenu:") {
		t.Fatalf("%s must install an application main menu; without one no Edit key equivalent "+
			"reaches the WKWebView and ⌘V does nothing", trayShim)
	}
	// The actions have to be the standard responder-chain selectors with the standard
	// key equivalents, and they must be validated against the responder chain rather than
	// aimed at a fixed target, which is what makes them work inside a web form.
	for _, want := range []string{
		`@selector(paste:)`, `@selector(copy:)`, `@selector(cut:)`,
		`@selector(selectAll:)`, `@selector(undo:)`, `@selector(redo:)`,
		`keyEquivalent:@"v"`, `keyEquivalent:@"c"`, `keyEquivalent:@"x"`,
		`keyEquivalent:@"a"`, `keyEquivalent:@"z"`,
	} {
		if !strings.Contains(shim, want) {
			t.Errorf("%s must wire the standard editing shortcut %s", trayShim, want)
		}
	}
	// ⌘Q has to run the same path as 退出 in the tray menu: stopping the runtime, hiding
	// the window and then ending the run loop. A bare terminate: would drop tunnels.
	if !strings.Contains(shim, "TMActionQuit") {
		t.Errorf("%s must route the main menu's quit item through the tray's own quit action", trayShim)
	}
}

// TestShimMapsControlKeyEditingShortcuts covers the Windows habit the operator asked for.
//
// A local monitor is the only place that can rewrite a key press before AppKit's text
// system claims it, and it is fed by the event-fetch path rather than by
// -[NSApplication sendEvent:]. The mask argument is an NSEventMask, not an NSEventType:
// passing the type value watches the wrong events entirely and the handler never runs,
// which is exactly the mistake this guard makes impossible to repeat.
func TestShimMapsControlKeyEditingShortcuts(t *testing.T) {
	shim := readOrFail(t, trayShim)

	if !strings.Contains(shim, "addLocalMonitorForEventsMatchingMask:NSEventMaskKeyDown") {
		t.Errorf("%s must watch key downs with the NSEventMaskKeyDown mask, not the event type", trayShim)
	}
	if strings.Contains(shim, "MatchingMask:NSEventTypeKeyDown") {
		t.Errorf("%s passes an NSEventType where an NSEventMask is required; the monitor would never fire", trayShim)
	}
	// The rewrite must be an explicit responder-chain dispatch, because the menu only binds
	// the Command variants.
	if !strings.Contains(shim, "sendAction:") {
		t.Errorf("%s must dispatch the Control variant through -[NSApplication sendAction:to:from:]", trayShim)
	}
	// Control alone: Command, Option and Shift have to keep their normal meaning, and
	// ⌃A/⌃E/⌃E are Cocoa's own line-editing bindings, so they must not be remapped.
	for _, forbidden := range []string{`isEqualToString:@"a"`, `isEqualToString:@"e"`} {
		if strings.Contains(shim, forbidden) {
			t.Errorf("%s must not remap %s: Cocoa binds ⌃A to beginning-of-line already", trayShim, forbidden)
		}
	}
}

// TestShimTurnsOffSystemTextAssists keeps macOS from rewriting what the operator types.
//
// Which lever works was measured rather than assumed, using NSSpellChecker's own class
// properties on a macOS 14 host with every text assist switched on:
//
//   - registerDefaults: switches automatic spelling correction off, and does nothing at all
//     for automatic capitalisation, which AppKit reads from the persistent domains only.
//   - the bundle's own persistent domain switches capitalisation off. That is the one lever
//     that does, so the shim has to write it.
//
// Writing the app domain is acceptable precisely because it is this bundle's own preference
// file: the system-wide switch under System Settings > Keyboard > Text is untouched, so
// every other application keeps behaving the way the operator chose.
func TestShimTurnsOffSystemTextAssists(t *testing.T) {
	shim := readOrFail(t, trayShim)

	// Both levers, because they cover different assists.
	if !strings.Contains(shim, "registerDefaults:") {
		t.Errorf("%s must register the text-assist overrides, which is what reaches spelling correction", trayShim)
	}
	if !strings.Contains(shim, "setBool:NO forKey:key") {
		t.Errorf("%s must also write the overrides into the bundle's own domain; registering them "+
			"leaves automatic capitalisation on", trayShim)
	}
	// Asking "is there already a value?" is not a way to detect an operator's own choice:
	// the lookup falls through to the global domain, which System Settings writes for
	// NSAutomaticCapitalizationEnabled, and the one key that needs the write is skipped.
	// Measured on a macOS 14 host, so the write has to be unconditional.
	if strings.Contains(shim, "objectForKey:key") {
		t.Errorf("%s must not gate the text-assist write on a lookup that sees the global domain", trayShim)
	}
	for _, key := range []string{
		"NSAutomaticCapitalizationEnabled",
		"NSAutomaticSpellingCorrectionEnabled",
		"NSAutomaticTextReplacementEnabled",
		"NSAutomaticQuoteSubstitutionEnabled",
	} {
		if !strings.Contains(shim, `@"`+key+`"`) {
			t.Errorf("%s must override the %s text-assist default for the settings window", trayShim, key)
		}
	}
	// The global domain belongs to the user, not to this application, and naming another
	// suite is how a process reaches it. The name itself is fine inside a comment, so the
	// guard is on the call that would do the damage.
	for _, forbidden := range []string{"initWithSuiteName:", "setvolatileDomainForName:"} {
		if strings.Contains(shim, forbidden) {
			t.Errorf("%s must not write outside this bundle's own preference domain (found %q)", trayShim, forbidden)
		}
	}
}
