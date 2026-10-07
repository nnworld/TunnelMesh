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

// nativeGo and trayMain are the other two halves of the quick panel contract. The shell
// can only be reviewed as a whole: a flag that Go never pushes, or a popover whose URL the
// Go side never builds, both compile and both do nothing.
const (
	nativeGo = "../../internal/tray/native/native.go"
	trayMain = "../../cmd/tunnelmesh-client-tray/main.go"
)

// TestShellRoutesTheQuickPanelFromTheMenuBarItem pins the menu-bar click behaviour.
//
// NSStatusBarButton shows its attached menu for every click, and while a menu is attached
// the button's action never fires. So the quick panel cannot be layered on top of the
// current wiring: the shell has to own the click, pop the menu up itself for the right
// button, and open the panel for the left one. Each string below is the load-bearing part
// of that, in the order the events arrive.
func TestShellRoutesTheQuickPanelFromTheMenuBarItem(t *testing.T) {
	bridge := readOrFail(t, nativeGo)
	shim := readOrFail(t, trayShim)

	// The config struct is declared twice, once per compiler, and has to stay identical.
	for _, source := range []struct{ name, text string }{{nativeGo, bridge}, {trayShim, shim}} {
		for _, field := range []string{"const char *panelURL;", "int quickPanel;"} {
			if !strings.Contains(source.text, field) {
				t.Errorf("%s must declare %s; the two TMTrayConfig definitions are compiled separately", source.name, field)
			}
		}
	}
	if !strings.Contains(bridge, "void TMTraySetQuickPanel(int enabled);") {
		t.Errorf("%s must export TMTraySetQuickPanel so a save in the general tab reaches the shell", nativeGo)
	}
	if !strings.Contains(shim, "void TMTraySetQuickPanel(int enabled)") {
		t.Errorf("%s must implement TMTraySetQuickPanel", trayShim)
	}

	// The panel is a real web view pointed at the panel route, not a re-skinned window.
	for _, want := range []string{"NSPopover", "NSPopoverBehaviorTransient", "panelURLString"} {
		if !strings.Contains(shim, want) {
			t.Errorf("%s must build the quick panel with %s", trayShim, want)
		}
	}

	// Left click takeover: an action on the status button, and no permanently attached menu.
	if !strings.Contains(shim, "sendActionOn:") {
		t.Errorf("%s must ask the status button for the mouse events it has to route", trayShim)
	}
	if strings.Contains(shim, "statusItem.menu = menu") {
		t.Fatalf("%s must not attach the menu to the status item: while a menu is attached "+
			"NSStatusBarButton swallows every click and the panel can never open", trayShim)
	}
	// The menu still has to be reachable, because 打开官网首页 and 退出 are the only way out
	// of a stuck client. Right click (and ⌃-click, which AppKit reports as a right click for
	// status items) is the documented gesture.
	for _, want := range []string{"popUpMenuPositioningItem:", "NSEventTypeRightMouseDown", "NSEventModifierFlagControl"} {
		if !strings.Contains(shim, want) {
			t.Errorf("%s must keep the menu reachable via %s", trayShim, want)
		}
	}
	// The flag is read when the click happens, so turning the setting off takes effect
	// without relaunching; a cached decision made at start-up would be a stale second copy
	// of a preference that has one authoritative source in tray.json.
	if !strings.Contains(shim, "self.quickPanel") {
		t.Errorf("%s must consult the quick-panel flag at click time", trayShim)
	}
	// A panel that outlives 退出 would keep an event source alive past the run loop.
	if !strings.Contains(shim, "closeQuickPanel") {
		t.Errorf("%s must close the quick panel before the shell stops", trayShim)
	}
}

// TestTrayMainWiresThePanelAndWindowActions covers the Go half of the same feature.
//
// The panel's buttons call /api/actions/show-window and /api/actions/quit, and those
// handlers only work once the shell installs them; a tray that serves the panel but never
// wires the actions shows buttons that answer 501.
func TestTrayMainWiresThePanelAndWindowActions(t *testing.T) {
	main := readOrFail(t, trayMain)
	for _, want := range []string{"api.PanelURL()", "native.SetQuickPanel", "api.SetWindowHandlers", "prefs.QuickPanel"} {
		if !strings.Contains(main, want) {
			t.Errorf("%s must wire the quick panel with %s", trayMain, want)
		}
	}
}

// TestShimPrefersTheBrandGlyphInTheMenuBar keeps the menu bar and Finder showing one mark.
//
// The system symbol was the original drawing, and it is still the right fallback for a bare
// binary run out of a checkout, so the guard is about order: the bundle's own glyph has to
// be tried first, and it has to be marked as a template, because a status item that ignores
// the menu-bar appearance looks broken in dark mode.
func TestShimPrefersTheBrandGlyphInTheMenuBar(t *testing.T) {
	shim := readOrFail(t, trayShim)

	if !strings.Contains(shim, `imageNamed:@"TunnelMeshMenuBar"`) {
		t.Errorf("%s must load the bundled brand glyph for the status item, so the menu bar matches Finder", trayShim)
	}
	if !strings.Contains(shim, "template = YES") {
		t.Errorf("%s must mark the status-item image as a template so the system tints it", trayShim)
	}
	symbol := strings.Index(shim, "imageWithSystemSymbolName")
	if symbol < 0 {
		t.Fatalf("%s must keep a system-symbol fallback for a bundle without resources", trayShim)
	}
	if named := strings.Index(shim, `imageNamed:@"TunnelMeshMenuBar"`); named > symbol {
		t.Errorf("%s tries the SF Symbol before the bundled glyph; the fallback has to stay the fallback", trayShim)
	}
	if !strings.Contains(shim, `if (@available(macOS 11.0, *))`) {
		t.Errorf("%s must guard the SF Symbol API with @available rather than an expression, which "+
			"does not actually gate availability", trayShim)
	}
}

// TestShimMakesTheQuickPanelKeyAndDismissable covers what a popover needs beyond being on
// screen.
//
// An accessory application usually has no key window at all, and a web view only receives
// keyboard input in the key window; a control in a non-key window can also swallow the
// first click. The other half is Escape: the panel is dismissed by clicking elsewhere, and
// an operator who reaches for Escape must not have the panel sit there, because a panel
// that cannot be dismissed by keyboard reads as a frozen application.
func TestShimMakesTheQuickPanelKeyAndDismissable(t *testing.T) {
	shim := readOrFail(t, trayShim)

	if !strings.Contains(shim, "makeKeyAndOrderFront:") {
		t.Errorf("%s must make the panel's own window key after showing the popover, or the web view "+
			"never sees the keyboard and the first click can be dropped", trayShim)
	}
	if !strings.Contains(shim, "keyCode == 53") {
		t.Errorf("%s must dismiss the quick panel on Escape", trayShim)
	}
	// The Escape handler has to be scoped to the panel being shown: an unbound Escape would
	// be stolen from the settings window, where it closes popovers and dialogs of its own.
	if !strings.Contains(shim, "panelPopover.isShown") {
		t.Errorf("%s must only treat Escape as a panel dismissal while the panel is shown", trayShim)
	}
}

// TestShimIgnoresCloseOfAHiddenWindow keeps a stray close from ending the client.
//
// windowShouldClose: is where "closing the window means hide, not quit" is decided, and the
// quit branch is legitimate only for a window the operator can actually see. A cancel
// operation that reaches the hidden settings window (a dismissed panel, a key equivalent
// aimed at the wrong responder) must not stop the tunnels.
func TestShimIgnoresCloseOfAHiddenWindow(t *testing.T) {
	shim := readOrFail(t, trayShim)

	shouldClose := shim[strings.Index(shim, "- (BOOL)windowShouldClose:"):]
	if !strings.Contains(shouldClose, "isVisible") {
		t.Errorf("%s must ignore a close for a window that is not visible; with minimize-to-tray off "+
			"that path quits the client and drops every tunnel", trayShim)
	}
}

// TestShimActsOnMouseUpNotMouseDown is the nested-run-loop regression.
//
// -[NSStatusBarButton rightMouseDown:] enters -[NSCell trackMouse:...untilMouseUp:], which
// owns the main thread until the mouse is released. Sending the action on mouse-down runs
// -[NSMenu popUpMenuPositioningItem:...] inside that loop: the menu's own tracking consumes
// the mouse-up, the button's tracking never sees it, and the main thread stays parked there
// forever. Measured on a macOS host: after right-click then Escape, the icon stopped
// answering and SIGTERM left the process running, because TMTrayStop queues its work onto
// the very thread that is blocked. Acting on mouse-up leaves the loop before the menu runs.
func TestShimActsOnMouseUpNotMouseDown(t *testing.T) {
	shim := readOrFail(t, trayShim)

	if !strings.Contains(shim, "sendActionOn:(NSEventMaskLeftMouseUp | NSEventMaskRightMouseUp)") {
		t.Errorf("%s must send the status-item action on mouse-up; on mouse-down the menu would be "+
			"popped up inside the button's own mouse tracking, which never ends", trayShim)
	}
	if strings.Contains(shim, "sendActionOn:(NSEventMaskLeftMouseDown | NSEventMaskRightMouseDown)") {
		t.Errorf("%s must not ask for the mouse-down masks on the status item", trayShim)
	}
	// Both release gestures still have to be recognised, or a right click would open the
	// panel: the event that triggers the action is now the up event.
	if !strings.Contains(shim, "NSEventTypeRightMouseUp") {
		t.Errorf("%s must treat the right mouse-up as the menu gesture", trayShim)
	}
}

// TestTrayMainBoundsItsOwnShutdown keeps SIGTERM meaningful.
//
// The clean path is native.Stop() returning from native.Run so main can unwind. If the main
// thread is parked in AppKit tracking, that never happens, and a logout would wait for the
// process to be killed hard with the runtime lock and the tunnels still held. A bounded wait
// turns that into a logged, deliberate exit.
func TestTrayMainBoundsItsOwnShutdown(t *testing.T) {
	main := readOrFail(t, trayMain)
	for _, want := range []string{"shutdownWatchdog", "os.Exit"} {
		if !strings.Contains(main, want) {
			t.Errorf("%s must bound the wait for the shell to stop (looking for %q)", trayMain, want)
		}
	}
}
