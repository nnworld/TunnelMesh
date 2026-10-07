// TunnelMesh macOS tray shell.
//
// This file owns the Cocoa run loop. Everything the Go side can ask for - the status
// item, the settings window, the WKWebView, the login item and the system browser - is
// implemented here so that no second library ever competes for the main thread.
//
// It is compiled by cgo because it lives next to native.go, and only in a
// "-tags tray" build on darwin.

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <ServiceManagement/ServiceManagement.h>
#import <stdlib.h>
#import <string.h>

// Callbacks implemented in Go (see the //export directives in native.go). They are
// declared by hand because cgo does not include its generated export header in the
// sibling C sources of the same package.
extern void TunnelMeshTrayMenuAction(int action);
extern int TunnelMeshTrayMinimizeToTray(void);
extern void TunnelMeshTrayWindowVisibilityChanged(int visible);

// Must stay byte-identical to the typedef in native.go's cgo preamble: cgo compiles the
// two files separately, so neither sees the other's declaration.
typedef struct {
	const char *url;
	const char *panelURL;
	const char *windowTitle;
	const char *statusTooltip;
	const char *openMainLabel;
	const char *openWebsiteLabel;
	const char *quitLabel;
	int minimizeToTray;
	int quickPanel;
	double widthFraction;
	double heightFraction;
} TMTrayConfig;

// Menu item tags. They mirror the action constants in native.go.
enum {
	TMActionOpenMain = 0,
	TMActionOpenWebsite = 1,
	TMActionQuit = 2,
};

// Minimum window size. Below this the settings window cannot show a routing form and a
// validation report side by side, which is the whole point of the interface.
static const CGFloat TMMinWindowWidth = 720.0;
static const CGFloat TMMinWindowHeight = 480.0;

// Quick panel size. It is a fixed compact surface rather than a fraction of the screen:
// the panel answers "is it up, and how much is flowing" in one glance, and a second
// resizable window would need layout work the settings window already did.
static const CGFloat TMPanelWidth = 340.0;
static const CGFloat TMPanelHeight = 420.0;

// Guards the reopen after a transient popover dismisses itself.
//
// A transient NSPopover closes on the mouse-down outside it, and that mouse-down is the
// same event that reaches the status item's action. Without the guard a second click
// closes the panel and immediately opens it again, which reads as an icon that stopped
// working: the close and the action land within a millisecond of each other, so a quarter
// second is what separates "toggle" from "open again".
static const NSTimeInterval TMPanelReopenGuard = 0.25;

// Runs a block on the main thread. The Go side calls into here from HTTP handler
// goroutines, and every AppKit mutation has to happen where AppKit lives.
static void TMOnMainSync(dispatch_block_t block) {
	if ([NSThread isMainThread]) {
		block();
		return;
	}
	dispatch_sync(dispatch_get_main_queue(), block);
}

// Queues a block on the main thread without waiting for it.
//
// Stopping the application uses this rather than TMOnMainSync. The signal handler is
// installed before NSApp starts running, so a SIGTERM can arrive while the main thread is
// still in Go and not draining the main queue at all; a dispatch_sync from the handler
// goroutine would then block forever and the process would ignore the signal it was
// trying to honour. Queued instead, the block runs as soon as the run loop starts, which
// stops it again immediately - the right outcome for a signal that beat the shell to it.
static void TMOnMainAsync(dispatch_block_t block) {
	if ([NSThread isMainThread]) {
		block();
		return;
	}
	dispatch_async(dispatch_get_main_queue(), block);
}

// Ends the Cocoa run loop.
//
// [NSApp stop:] on its own only sets a flag that NSApplication checks between events. A
// menu-bar app whose window is hidden has no pending events, so the run loop stays
// blocked in its event wait and the process never exits: 退出 and SIGTERM both appear to
// do nothing while the tunnels keep running. Posting an application-defined event is the
// documented way to wake the loop so it observes the flag.
static void TMStopApplication(void) {
	[NSApp stop:nil];
	NSEvent *wake = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
	                                   location:NSMakePoint(0, 0)
	                              modifierFlags:0
	                                  timestamp:0
	                               windowNumber:0
	                                    context:nil
	                                    subtype:0
	                                      data1:0
	                                      data2:0];
	if (wake != nil) {
		[NSApp postEvent:wake atStart:YES];
	}
}

// Turns off the macOS text assists that rewrite characters the operator typed.
//
// Every field in the settings window holds something that has to stay byte-exact - a server
// address, a token, a host:port pair - and "Capitalize words automatically", spelling
// correction and text substitution all change characters after the operator has typed them.
//
// Two mechanisms are needed, and which one does what was measured with NSSpellChecker's own
// class properties on a host with every assist switched on: registering the overrides
// switches automatic spelling correction off and does nothing at all for automatic
// capitalisation, which AppKit reads from the persistent domains only. Writing the
// capitalisation override into this bundle's own domain is the only thing that works.
//
// That file belongs to this application, so the system-wide switch stays untouched and every
// other program keeps behaving the way the operator chose. The write is unconditional on
// purpose: asking whether the key already has a value is no test of "the operator chose
// this", because the lookup falls through to the global domain, where System Settings has
// been writing NSAutomaticCapitalizationEnabled for decades. Measured on this host, that
// mistake skipped the one key that needed writing and persisted the other four.
//
// The web view runs in a process of its own, so the settings form also marks every technical
// field autocapitalize=none and spellcheck=false: the two halves cover the same problem, and
// the attributes are the part that reaches the web content process for certain.
static void TMDisableSystemTextAssists(void) {
	NSArray<NSString *> *keys = @[
		@"NSAutomaticCapitalizationEnabled",
		@"NSAutomaticSpellingCorrectionEnabled",
		@"NSAutomaticTextReplacementEnabled",
		@"NSAutomaticQuoteSubstitutionEnabled",
		@"NSAutomaticDashSubstitutionEnabled",
	];
	NSUserDefaults *defaults = [NSUserDefaults standardUserDefaults];
	NSMutableDictionary<NSString *, NSNumber *> *overrides = [NSMutableDictionary dictionary];
	for (NSString *key in keys) {
		overrides[key] = @NO;
		[defaults setBool:NO forKey:key];
	}
	[defaults registerDefaults:overrides];
}

// Maps the Control clipboard chords onto the editing actions their Command equivalents get
// from the main menu.
//
// Windows-trained hands reach for Ctrl+V, and the operator asked for it. The Command
// variants need nothing here because the main menu binds them, so this handler only has to
// recognise a bare Control press and put the matching action on the responder chain
// itself, swallowing the key only when something accepted it.
//
// Two details are load-bearing, and both were measured rather than assumed. The mask is an
// NSEventMask, not an NSEventType: handing it the type value watches unrelated events and
// the handler never runs. And a local monitor is fed by the application's event-fetch path,
// not by -[NSApplication sendEvent:], so the action is dispatched here rather than by
// rewriting the event's modifiers and hoping the menu matches it afterwards.
//
// ⌃A and ⌃E are deliberately not remapped: Cocoa already binds them to the start and end
// of a line inside a text field, and taking that away would be a worse surprise than the
// one being fixed.
static id gControlShortcutMonitor = nil;

static void TMInstallControlKeyShortcuts(void) {
	gControlShortcutMonitor = [NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskKeyDown
	                                                                handler:^NSEvent *(NSEvent *event) {
		NSEventModifierFlags flags = event.modifierFlags & NSEventModifierFlagDeviceIndependentFlagsMask;
		if (flags != NSEventModifierFlagControl) {
			return event;
		}
		NSString *key = event.charactersIgnoringModifiers.lowercaseString;
		SEL action = nil;
		if ([key isEqualToString:@"v"]) {
			action = @selector(paste:);
		} else if ([key isEqualToString:@"c"]) {
			action = @selector(copy:);
		} else if ([key isEqualToString:@"x"]) {
			action = @selector(cut:);
		}
		if (action != nil && [NSApp sendAction:action to:nil from:[NSApp keyWindow].firstResponder]) {
			return nil;
		}
		return event;
	}];
}

@interface TMTrayController : NSObject <NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate, NSPopoverDelegate>
@property(nonatomic, strong) NSStatusItem *statusItem;
@property(nonatomic, strong) NSWindow *window;
@property(nonatomic, strong) WKWebView *webView;
@property(nonatomic, copy) NSString *urlString;
@property(nonatomic, assign) BOOL minimizeToTray;
// The status menu lives on the controller rather than on NSStatusItem.menu: while a menu
// is attached, NSStatusBarButton opens it for every click and never sends its action,
// which would leave the quick panel unreachable.
@property(nonatomic, strong) NSMenu *statusMenu;
@property(nonatomic, copy) NSString *panelURLString;
@property(nonatomic, assign) BOOL quickPanel;
@property(nonatomic, strong) NSPopover *panelPopover;
@property(nonatomic, strong) WKWebView *panelWebView;
@property(nonatomic, assign) NSTimeInterval panelDismissedAt;
@property(nonatomic, assign) CGFloat widthFraction;
@property(nonatomic, assign) CGFloat heightFraction;
@end

// One controller per process, retained for the lifetime of the run loop. ARC would
// otherwise release it as soon as TMTrayRun's autorelease pool drained.
static TMTrayController *gController = nil;

// Installed after the controller exists; see TMInstallPanelDismissShortcut below.
static id gPanelEscapeMonitor = nil;

@implementation TMTrayController

- (instancetype)initWithConfig:(TMTrayConfig)config {
	self = [super init];
	if (self == nil) {
		return nil;
	}
	_urlString = config.url ? [NSString stringWithUTF8String:config.url] : @"";
	_panelURLString = config.panelURL ? [NSString stringWithUTF8String:config.panelURL] : @"";
	_minimizeToTray = config.minimizeToTray != 0;
	_quickPanel = config.quickPanel != 0;
	_widthFraction = config.widthFraction > 0 ? (CGFloat)config.widthFraction : 0.5;
	_heightFraction = config.heightFraction > 0 ? (CGFloat)config.heightFraction : 0.5;

	NSString *windowTitle = config.windowTitle ? [NSString stringWithUTF8String:config.windowTitle] : @"TunnelMesh Client";
	NSString *tooltip = config.statusTooltip ? [NSString stringWithUTF8String:config.statusTooltip] : windowTitle;
	NSString *openMain = config.openMainLabel ? [NSString stringWithUTF8String:config.openMainLabel] : @"Open Dashboard";
	NSString *openWebsite = config.openWebsiteLabel ? [NSString stringWithUTF8String:config.openWebsiteLabel] : @"Open Website";
	NSString *quit = config.quitLabel ? [NSString stringWithUTF8String:config.quitLabel] : @"Quit";

	[self buildStatusItem:tooltip openMain:openMain openWebsite:openWebsite quit:quit];
	[self buildWindow:windowTitle];
	return self;
}

- (void)buildStatusItem:(NSString *)tooltip openMain:(NSString *)openMain openWebsite:(NSString *)openWebsite quit:(NSString *)quit {
	self.statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
	self.statusItem.button.toolTip = tooltip;

	// The bundle's own glyph first, so the menu bar and Finder show one mark. imageNamed
	// resolves TunnelMeshMenuBar.png plus its @2x and @3x siblings into one multi-scale
	// image, which is what keeps the 18pt version crisp on a Retina display instead of
	// blowing up a single bitmap.
	//
	// Marked as a template because that is how the glyph was drawn: the system tints it,
	// so it follows the menu bar's light and dark appearance the way every status item
	// does. A coloured copy would read as a sticker on a dark bar.
	NSImage *mark = [NSImage imageNamed:@"TunnelMeshMenuBar"];
	if (mark != nil) {
		mark.template = YES;
		if (mark.accessibilityDescription == nil) {
			mark.accessibilityDescription = tooltip;
		}
	}
	if (mark == nil) {
		// Running the bare binary from a checkout has no Resources directory, and an SF
		// Symbol of the same family beats the generic tile.
		if (@available(macOS 11.0, *)) {
			mark = [NSImage imageWithSystemSymbolName:@"network" accessibilityDescription:tooltip];
		}
	}
	if (mark != nil) {
		self.statusItem.button.image = mark;
		self.statusItem.button.imagePosition = NSImageOnly;
	} else {
		self.statusItem.button.title = @"TM";
	}

	NSMenu *menu = [[NSMenu alloc] init];
	menu.autoenablesItems = NO;
	[menu addItem:[self menuItem:openMain action:TMActionOpenMain keyEquivalent:@"o"]];
	[menu addItem:[self menuItem:openWebsite action:TMActionOpenWebsite keyEquivalent:@""]];
	[menu addItem:[NSMenuItem separatorItem]];
	[menu addItem:[self menuItem:quit action:TMActionQuit keyEquivalent:@"q"]];
	self.statusMenu = menu;

	// Routing the click here is the only way to get it: the button's default behaviour is
	// "open the attached menu", and an attached menu never sends an action. Left click now
	// means the panel when the operator asked for the panel, and the menu stays one right
	// click (or ⌃-click) away either way.
	//
	// On mouse-up, deliberately. The button's own tracking loop (-[NSCell
	// trackMouse:inRect:ofView:untilMouseUp:]) owns the main thread from mouse-down until
	// the release, and popping a menu or showing a popover from inside it means the menu's
	// tracking eats the mouse-up that would end the loop. The main thread then never returns
	// to the run loop, the icon stops answering, and a queued TMTrayStop is never run.
	self.statusItem.button.target = self;
	self.statusItem.button.action = @selector(statusItemClicked:);
	[self.statusItem.button sendActionOn:(NSEventMaskLeftMouseUp | NSEventMaskRightMouseUp)];
}

- (NSMenuItem *)menuItem:(NSString *)title action:(NSInteger)tag keyEquivalent:(NSString *)key {
	NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:title action:@selector(menuAction:) keyEquivalent:key];
	item.tag = tag;
	item.target = self;
	return item;
}

	- (void)menuAction:(NSMenuItem *)sender {
		switch (sender.tag) {
			case TMActionOpenMain:
				[self showWindow];
				TunnelMeshTrayMenuAction(TMActionOpenMain);
				break;
			case TMActionOpenWebsite:
				// The Go side owns "open a URL" so the menu and the about tab share one path.
				TunnelMeshTrayMenuAction(TMActionOpenWebsite);
				break;
			case TMActionQuit:
				[self requestQuit];
				break;
			default:
				break;
		}
	}

// Routes a click on the menu-bar item.
//
// The preference is read at click time rather than decided at start-up: the operator can
// change it in the general tab while the settings window is open, and the next click on
// the icon has to honour it. Right click and ⌃-click always mean the menu, so 退出 and
// 打开官网首页 stay reachable even when the panel is on or the panel content is stuck.
- (void)statusItemClicked:(id)sender {
	NSEvent *event = [NSApp currentEvent];
	BOOL controlDown = (event.modifierFlags & NSEventModifierFlagControl) != 0;
	BOOL rightClick = event.type == NSEventTypeRightMouseDown || event.type == NSEventTypeRightMouseUp;
	if (rightClick || controlDown || !self.quickPanel) {
		[self popUpStatusMenu];
		return;
	}
	NSTimeInterval since = [NSDate timeIntervalSinceReferenceDate] - self.panelDismissedAt;
	if (self.panelPopover.isShown || since < TMPanelReopenGuard) {
		[self closeQuickPanel];
		return;
	}
	[self showQuickPanel];
}

// Shows the status menu under the icon.
//
// Popping the menu up by hand is the cost of owning the click. NSZeroPoint is not used:
// the status button is not flipped on every release, so asking for the bottom edge
// explicitly is what keeps the menu from covering the icon itself.
- (void)popUpStatusMenu {
	NSMenu *menu = self.statusMenu;
	NSStatusBarButton *button = self.statusItem.button;
	if (menu == nil || button == nil) {
		return;
	}
	[self closeQuickPanel];
	[button highlight:YES];
	[menu popUpMenuPositioningItem:nil atLocation:NSMakePoint(0, NSMaxY(button.bounds)) inView:button];
	[button highlight:NO];
}

// Builds the panel once and keeps it.
//
// Recreating it per click would reload the document every time, which turns a glance at
// the icon into a visible refresh and re-runs the interface's start-up requests.
- (void)buildQuickPanel {
	WKWebViewConfiguration *configuration = [[WKWebViewConfiguration alloc] init];
	configuration.defaultWebpagePreferences.allowsContentJavaScript = YES;
	WKWebView *panel = [[WKWebView alloc]
	    initWithFrame:NSMakeRect(0, 0, TMPanelWidth, TMPanelHeight)
	        configuration:configuration];
	panel.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
	panel.navigationDelegate = self;
	panel.allowsMagnification = NO;

	NSViewController *host = [[NSViewController alloc] init];
	host.view = panel;
	host.preferredContentSize = NSMakeSize(TMPanelWidth, TMPanelHeight);

	NSPopover *popover = [[NSPopover alloc] init];
	// Transient rather than application-defined: clicking anywhere else, or into the
	// settings window, has to dismiss the panel without this shell tracking key windows.
	popover.behavior = NSPopoverBehaviorTransient;
	popover.animates = YES;
	popover.contentViewController = host;
	popover.contentSize = host.preferredContentSize;
	popover.delegate = self;

	self.panelWebView = panel;
	self.panelPopover = popover;
	[self loadPanelURL];
}

- (void)loadPanelURL {
	NSURL *url = [NSURL URLWithString:self.panelURLString];
	if (url == nil || self.panelWebView == nil) {
		return;
	}
	[self.panelWebView loadRequest:[NSURLRequest requestWithURL:url]];
}

- (void)showQuickPanel {
	if (self.panelURLString.length == 0) {
		// Nothing to render. Falling back to the menu beats refusing the click, which
		// would look like a dead icon.
		[self popUpStatusMenu];
		return;
	}
	if (self.panelPopover == nil) {
		[self buildQuickPanel];
	}
	// An accessory application is normally not active, and a popover whose window is not
	// key drops the first click on its buttons.
	[NSApp activateIgnoringOtherApps:YES];
	[self.panelPopover showRelativeToRect:self.statusItem.button.bounds
	                               ofView:self.statusItem.button
	                        preferredEdge:NSMinYEdge];
	// The popover's window has to be key. An accessory application normally has no key
	// window at all, and WKWebView routes the keyboard only to the key window, so without
	// this the panel looks frozen the moment the operator reaches for Escape. The window
	// exists only after the popover has been placed, hence the deferred retry.
	[self makeQuickPanelKeyAfterShowing:self.panelWebView.window];
}

- (void)makeQuickPanelKeyAfterShowing:(NSWindow *)panelWindow {
	if (panelWindow != nil) {
		[panelWindow makeKeyAndOrderFront:nil];
		return;
	}
	dispatch_async(dispatch_get_main_queue(), ^{
		[self.panelWebView.window makeKeyAndOrderFront:nil];
	});
}

- (void)closeQuickPanel {
	if (self.panelPopover != nil && self.panelPopover.isShown) {
		[self.panelPopover close];
	}
}

- (void)popoverWillClose:(NSNotification *)notification {
	self.panelDismissedAt = [NSDate timeIntervalSinceReferenceDate];
}

- (void)setQuickPanel:(BOOL)enabled {
	TMOnMainSync(^{
		self.quickPanel = enabled;
		if (!enabled) {
			// Turning the setting off must not strand a panel that is already on screen.
			[self closeQuickPanel];
		}
	});
}

// Installs the application main menu.
//
// A menu-bar client has no menu bar of its own until one of its windows is key, so it is
// tempting to skip this. That is not cosmetic: -[NSApplication sendEvent:] matches ⌘X, ⌘C,
// ⌘V, ⌘Z and ⌘A against the main menu before the key window ever sees the event, so an
// application without one has text fields that silently refuse every clipboard shortcut.
// Measured on a macOS host against this exact window: with no main menu ⌘V leaves the
// field empty, with the menu below the same keystroke pastes.
- (void)installMainMenu {
	NSMenu *mainMenu = [[NSMenu alloc] init];

	NSMenuItem *appItem = [[NSMenuItem alloc] init];
	[mainMenu addItem:appItem];
	NSMenu *appMenu = [[NSMenu alloc] initWithTitle:@"TunnelMesh Client"];
	// ⌘Q runs the tray menu's 退出 rather than terminate:, because stopping the runtime and
	// releasing the lock file is what makes the next start work.
	NSMenuItem *quit = [[NSMenuItem alloc] initWithTitle:@"Quit TunnelMesh Client"
	                                              action:@selector(menuAction:)
	                                       keyEquivalent:@"q"];
	quit.target = self;
	quit.tag = TMActionQuit;
	[appMenu addItem:quit];
	[appItem setSubmenu:appMenu];

	NSMenuItem *editItem = [[NSMenuItem alloc] init];
	[mainMenu addItem:editItem];
	NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"Edit"];
	// Every action is aimed at nil, which is AppKit's "ask the responder chain": it is what
	// lets one item drive a WKWebView field today and a native control later.
	[editMenu addItemWithTitle:@"Undo" action:@selector(undo:) keyEquivalent:@"z"];
	NSMenuItem *redo = [editMenu addItemWithTitle:@"Redo" action:@selector(redo:) keyEquivalent:@"z"];
	redo.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagShift;
	[editMenu addItem:[NSMenuItem separatorItem]];
	[editMenu addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
	[editMenu addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
	[editMenu addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
	[editMenu addItemWithTitle:@"Delete" action:@selector(delete:) keyEquivalent:@""];
	[editMenu addItemWithTitle:@"Select All" action:@selector(selectAll:) keyEquivalent:@"a"];
	[editItem setSubmenu:editMenu];

	[NSApp setMainMenu:mainMenu];
}

- (void)requestQuit {
	TunnelMeshTrayMenuAction(TMActionQuit);
	[self closeQuickPanel];
	[self hideWindow];
	// Stopping here as well as in the Go handler keeps 退出 working even if the handler
	// is not installed yet; [NSApp stop:] is idempotent.
	TMStopApplication();
}

- (void)buildWindow:(NSString *)title {
	NSScreen *screen = [NSScreen mainScreen];
	NSRect visible = screen ? screen.visibleFrame : NSMakeRect(0, 0, 1440, 900);
	CGFloat width = visible.size.width * self.widthFraction;
	CGFloat height = visible.size.height * self.heightFraction;
	// The minimum wins over the fraction on a small screen: a window too small to use is
	// worse than one that covers more than half the display.
	width = MAX(TMMinWindowWidth, MIN(width, visible.size.width * 0.9));
	height = MAX(TMMinWindowHeight, MIN(height, visible.size.height * 0.9));
	CGFloat originX = visible.origin.x + (visible.size.width - width) / 2.0;
	CGFloat originY = visible.origin.y + (visible.size.height - height) / 2.0;

	NSWindow *window = [[NSWindow alloc]
	    initWithContentRect:NSMakeRect(originX, originY, width, height)
	              styleMask:(NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
	                         NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable)
	                backing:NSBackingStoreBuffered
	                  defer:NO];
	window.title = title;
	window.delegate = self;
	window.contentMinSize = NSMakeSize(TMMinWindowWidth, TMMinWindowHeight);
	// The window is recreated on demand by 打开主界面, so closing it must not dealloc it
	// behind the controller's back.
	window.releasedWhenClosed = NO;
	window.level = NSNormalWindowLevel;
	window.collectionBehavior = NSWindowCollectionBehaviorDefault;

	WKWebViewConfiguration *configuration = [[WKWebViewConfiguration alloc] init];
	// Stated explicitly rather than left to the default: the whole interface is a Vue
	// application, and a future WebKit default change must not silently blank the window.
	configuration.defaultWebpagePreferences.allowsContentJavaScript = YES;
	WKWebView *webView = [[WKWebView alloc] initWithFrame:window.contentView.bounds configuration:configuration];
	webView.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
	webView.navigationDelegate = self;
	webView.translatesAutoresizingMaskIntoConstraints = YES;
	[window.contentView addSubview:webView];

	self.window = window;
	self.webView = webView;
	[self loadSettingsURL];
}

- (void)loadSettingsURL {
	NSURL *url = [NSURL URLWithString:self.urlString];
	if (url == nil) {
		return;
	}
	[self.webView loadRequest:[NSURLRequest requestWithURL:url]];
}

- (BOOL)isLocalURL:(NSURL *)url {
	if (url == nil) {
		return NO;
	}
	// Only the tray's own loopback origin may be rendered. Anything else is opened in the
	// system browser instead, so the webview never becomes a general-purpose browser that
	// happens to hold the launch secret.
	if (![url.scheme.lowercaseString isEqualToString:@"http"]) {
		return NO;
	}
	NSString *host = url.host.lowercaseString;
	BOOL localHost = [host isEqualToString:@"127.0.0.1"] || [host isEqualToString:@"localhost"] ||
	                 [host isEqualToString:@"::1"];
	if (!localHost) {
		return NO;
	}
	NSURL *origin = [NSURL URLWithString:self.urlString];
	if (origin == nil || origin.port == nil || url.port == nil) {
		return localHost;
	}
	return [origin.port isEqualToNumber:url.port];
}

- (void)showWindow {
	TMOnMainSync(^{
		// The panel and the settings window are two renderings of one interface. Leaving
		// the panel up while the window opens puts the same numbers on screen twice.
		[self closeQuickPanel];
		if (self.window == nil) {
			[self buildWindow:@"TunnelMesh Client"];
		}
		[NSApp activateIgnoringOtherApps:YES];
		[self.window makeKeyAndOrderFront:nil];
		TunnelMeshTrayWindowVisibilityChanged(1);
	});
}

- (void)hideWindow {
	TMOnMainSync(^{
		if (self.window != nil && self.window.isVisible) {
			[self.window orderOut:nil];
			TunnelMeshTrayWindowVisibilityChanged(0);
		}
	});
}

- (void)setMinimizeToTray:(BOOL)enabled {
	TMOnMainSync(^{
		self.minimizeToTray = enabled;
	});
}

- (BOOL)windowShouldClose:(NSWindow *)sender {
	if (!sender.isVisible) {
		// Nothing on screen is being closed. A cancel operation can reach this window
		// through the responder chain, and honouring it would stop the tunnels with no
		// window in sight - the worst possible outcome for a keystroke nobody meant.
		return NO;
	}
	// Consulting Go rather than a cached flag means the general tab's switch takes effect
	// immediately, without the shell having to be told.
	if (TunnelMeshTrayMinimizeToTray()) {
		[sender orderOut:nil];
		TunnelMeshTrayWindowVisibilityChanged(0);
		return NO;
	}
	[sender orderOut:nil];
	TunnelMeshTrayWindowVisibilityChanged(0);
	[self requestQuit];
	return NO;
}

- (void)webView:(WKWebView *)webView
    decidePolicyForNavigationAction:(WKNavigationAction *)navigationAction
                    decisionHandler:(void (^)(WKNavigationActionPolicy))decisionHandler {
	NSURL *url = navigationAction.request.URL;
	if ([self isLocalURL:url]) {
		decisionHandler(WKNavigationActionPolicyAllow);
		return;
	}
	if (url != nil && navigationAction.navigationType == WKNavigationTypeLinkActivated) {
		[[NSWorkspace sharedWorkspace] openURL:url];
	}
	decisionHandler(WKNavigationActionPolicyCancel);
}

- (void)webView:(WKWebView *)webView didFailNavigation:(WKNavigation *)navigation withError:(NSError *)error {
	// A window that failed to load is worse than one that reloads: the operator sees a
	// blank pane with no way to recover except quitting from the menu.
	if (webView == self.webView && error.code != NSURLErrorCancelled) {
		dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(1.0 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
			[self loadSettingsURL];
		});
		return;
	}
	// The panel reloads on the same terms. NSURLErrorCancelled is skipped because closing
	// a transient popover mid-load cancels its own navigation, and reloading then would
	// rebuild a view the operator never asked to see again.
	if (webView == self.panelWebView && error.code != NSURLErrorCancelled) {
		dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(1.0 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
			[self loadPanelURL];
		});
	}
}

@end

// Lets Escape dismiss the quick panel.
//
// Scoped to the panel being shown, because the same key cancels dialogs and dropdowns
// inside the settings window: taking it away there would be a worse surprise than not
// handling it. A local monitor is the only place that sees the event before the web view's
// own process does.
static void TMInstallPanelDismissShortcut(void) {
	gPanelEscapeMonitor = [NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskKeyDown
	                                                           handler:^NSEvent *(NSEvent *event) {
		if (event.keyCode == 53 && gController.panelPopover.isShown) {
			[gController closeQuickPanel];
			return nil;
		}
		return event;
	}];
}

void TMTrayRun(TMTrayConfig config) {
	@autoreleasepool {
		TMDisableSystemTextAssists();
		[NSApplication sharedApplication];
		// Accessory: the client lives in the menu bar, not in the Dock or the app switcher.
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		gController = [[TMTrayController alloc] initWithConfig:config];
		[NSApp setDelegate:gController];
		[gController installMainMenu];
		TMInstallControlKeyShortcuts();
		TMInstallPanelDismissShortcut();
		[NSApp run];
	}
}

void TMTrayStop(void) {
	TMOnMainAsync(^{
		[gController closeQuickPanel];
		TMStopApplication();
	});
}

void TMTrayShowWindow(void) {
	[gController showWindow];
}

void TMTrayHideWindow(void) {
	[gController hideWindow];
}

void TMTraySetMinimizeToTray(int enabled) {
	[gController setMinimizeToTray:(enabled != 0)];
}

void TMTraySetQuickPanel(int enabled) {
	[gController setQuickPanel:(enabled != 0)];
}

void TMTrayOpenURL(const char *url) {
	if (url == NULL) {
		return;
	}
	NSString *target = [NSString stringWithUTF8String:url];
	NSURL *parsed = [NSURL URLWithString:target];
	if (parsed == nil) {
		return;
	}
	TMOnMainSync(^{
		[[NSWorkspace sharedWorkspace] openURL:parsed];
	});
}

char *TMTrayLoginItemSet(int enabled) {
	__block NSString *message = nil;
	TMOnMainSync(^{
		if (@available(macOS 13.0, *)) {
			SMAppService *service = [SMAppService mainAppService];
			SMAppServiceStatus status = service.status;
			// Turning the switch off when nothing is registered must be a no-op. Both
			// "not registered" and "not found" mean that: SMAppService reports NotFound for
			// a bundle it has no record of at this path, and unregistering it fails with a
			// bare "Operation not permitted" that the interface would surface as a broken
			// switch. Launching twice from a freshly unpacked bundle hits this every time.
			if (enabled == 0 && (status == SMAppServiceStatusNotRegistered ||
			                     status == SMAppServiceStatusNotFound)) {
				return;
			}
			NSError *error = nil;
			BOOL ok = enabled != 0 ? [service registerAndReturnError:&error] : [service unregisterAndReturnError:&error];
			if (!ok) {
				message = error.localizedDescription ?: @"the login item was refused";
			}
			return;
		}
		message = @"launch at login needs macOS 13 or newer";
	});
	if (message == nil) {
		return NULL;
	}
	return strdup([message UTF8String]);
}

int TMTrayLoginItemStatus(void) {
	__block int status = 5; // LoginItemUnknown
	TMOnMainSync(^{
		if (@available(macOS 13.0, *)) {
			status = (int)[SMAppService mainAppService].status;
			return;
		}
		status = 4; // LoginItemUnsupported
	});
	return status;
}

char *TMTraySystemJSON(void) {
	__block NSString *json = nil;
	TMOnMainSync(^{
		NSProcessInfo *info = [NSProcessInfo processInfo];
		NSOperatingSystemVersion version = info.operatingSystemVersion;
		NSString *osVersion = [NSString stringWithFormat:@"%ld.%ld.%ld", (long)version.majorVersion,
		                                                 (long)version.minorVersion, (long)version.patchVersion];
		NSMutableDictionary *payload = [NSMutableDictionary dictionary];
		payload[@"os"] = @"macOS";
		payload[@"osVersion"] = osVersion;
#if defined(__arm64__)
		payload[@"arch"] = @"arm64";
#elif defined(__x86_64__)
		payload[@"arch"] = @"amd64";
#else
		payload[@"arch"] = @"unknown";
#endif
		NSData *encoded = [NSJSONSerialization dataWithJSONObject:payload options:0 error:NULL];
		if (encoded != nil) {
			json = [[NSString alloc] initWithData:encoded encoding:NSUTF8StringEncoding];
		}
	});
	if (json == nil) {
		return NULL;
	}
	return strdup([json UTF8String]);
}

char *TMTrayPreferredLanguage(void) {
	__block NSString *language = nil;
	TMOnMainSync(^{
		NSArray<NSString *> *preferred = [NSLocale preferredLanguages];
		language = preferred.firstObject;
	});
	if (language == nil) {
		return NULL;
	}
	return strdup([language UTF8String]);
}

void TMTrayFreeString(char *value) {
	if (value != NULL) {
		free(value);
	}
}
