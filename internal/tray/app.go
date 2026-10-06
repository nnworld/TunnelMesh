package tray

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/tunnelmesh/tunnelmesh/internal/build"
	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
)

// AutostartManager toggles the operating system login item.
//
// It is an interface because the only real implementation is macOS specific and lives
// behind the "tray" build tag. Everything that consumes it - the settings window, the
// tests, and a future Windows port - depends on this shape instead.
type AutostartManager interface {
	// Supported reports whether this platform can register a login item at all.
	Supported() bool
	// Enabled reads the current registration state.
	Enabled() (bool, error)
	// SetEnabled registers or unregisters the login item.
	SetEnabled(enabled bool) error
}

// unsupportedAutostart is the fallback where no login-item API is wired in. Reporting
// "unsupported" rather than failing lets the rest of the general tab stay usable, and
// the preference is still recorded so a later platform can apply it.
type unsupportedAutostart struct{}

func (unsupportedAutostart) Supported() bool        { return false }
func (unsupportedAutostart) Enabled() (bool, error) { return false, nil }
func (unsupportedAutostart) SetEnabled(bool) error  { return nil }

// UnsupportedAutostart returns an AutostartManager that reports the feature as absent.
func UnsupportedAutostart() AutostartManager { return unsupportedAutostart{} }

// WebsiteURL is the "打开官网首页" target. The repository is the product's public home
// page, and reusing the configuration constant keeps the tray and the download API
// pointing at the same place.
const WebsiteURL = "https://github.com/" + config.DefaultGitHubRepository

// ReleasesURL is where "检查更新" sends the operator. The tray does not phone home on
// its own: a menu-bar client that silently queries a remote host on a timer is a
// surprise nobody asked for.
const ReleasesURL = WebsiteURL + "/releases"

// DocsURL points at the in-repository documentation.
const DocsURL = WebsiteURL + "/tree/main/docs"

// License identifies the project licence shown on the about tab.
const License = "Apache-2.0"

// ErrConfigUnusable marks a failure the operator fixes by editing the routing form,
// as opposed to a local transport failure. The API maps it onto 422 so the interface
// can point at the offending field instead of reporting a generic error.
var ErrConfigUnusable = errors.New("tray: the routing configuration is not usable")

// serverProbeTTL bounds how often the statistics tab contacts the Server. Polling the
// interface every two seconds must not turn into a health-check every two seconds.
const serverProbeTTL = 5 * time.Second

// serverProbeTimeout bounds one reachability probe so a black-holed address cannot pin
// the background refresh.
const serverProbeTimeout = 3 * time.Second

// App is the tray's application layer: it owns the preferences, the shared client
// configuration, the hosted runtime and the management-API client, and exposes them as
// views the settings window can render.
//
// The loopback API in api.go is a transport for this type and holds no logic of its
// own, which is what keeps every decision here testable without a webview, a socket or
// a macOS host.
type App struct {
	options   AppOptions
	server    *ServerClient
	autostart AutostartManager
	registry  *prometheus.Registry
	metrics   *observability.Metrics

	// runCtx outlives every HTTP request. The runtime is started from a request
	// handler but must not stop when that handler returns, so it is anchored here.
	runCtx    context.Context
	runCancel context.CancelFunc

	mu            sync.Mutex
	prefs         *PrefsStore
	paths         Paths
	config        *ConfigStore
	validator     *Validator
	runtime       *client.Runtime
	runtimeErr    error
	lockBlocked   bool
	autostartErr  string
	probe         serverProbeState
	quitRequested bool
}

// AppOptions configures an App. Every field is optional.
type AppOptions struct {
	// ConfigDir overrides the configuration directory. Empty resolves it from the
	// stored preferences, which is what the shipped tray does.
	ConfigDir string
	// Autostart supplies the login-item implementation.
	Autostart AutostartManager
	// Server supplies the management-API client.
	Server *ServerClient
	// Runner overrides the physical WebSocket runner. The tray leaves it nil in
	// production and tests inject a parked transport.
	Runner client.SessionPoolRunner
	// InstanceID pins the client identity. Empty keeps the persisted identity file.
	InstanceID string
	// Registry receives the client metrics. A nil value allocates a private one so
	// the statistics tab can gather byte counters without touching the global
	// registry.
	Registry *prometheus.Registry
	// OpenURL hands a URL to the operating system. Only the native shell can really
	// do this; without it the menu reports that it cannot open a browser.
	OpenURL func(target string) error
	// OnPreferencesChanged is called after every successful save so the shell can
	// follow the "close minimizes to tray" switch without polling.
	OnPreferencesChanged func(Preferences)
	// SystemProbe reports the operating system identity for the about tab.
	SystemProbe func() SystemInfo
	// ServerTimeout bounds management-API calls.
	ServerTimeout time.Duration
}

// serverProbeState caches one reachability answer.
type serverProbeState struct {
	target    string
	reachable bool
	detail    string
	at        time.Time
	inflight  bool
}

// NewApp resolves the configuration locations and builds the application layer.
func NewApp(options AppOptions) (*App, error) {
	prefsStore, paths, err := initialStores(options.ConfigDir)
	if err != nil {
		return nil, err
	}
	server := options.Server
	if server == nil {
		server = NewServerClient(options.ServerTimeout)
	}
	autostart := options.Autostart
	if autostart == nil {
		autostart = UnsupportedAutostart()
	}
	registry := options.Registry
	if registry == nil {
		registry = prometheus.NewRegistry()
	}
	runCtx, cancel := context.WithCancel(context.Background())
	app := &App{
		options:   options,
		server:    server,
		autostart: autostart,
		registry:  registry,
		metrics:   observability.NewMetrics(registry),
		prefs:     prefsStore,
		paths:     paths,
		runCtx:    runCtx,
		runCancel: cancel,
	}
	app.config = NewConfigStore(paths.ClientYAML)
	app.validator = NewValidator(server, app.config)
	return app, nil
}

// initialStores resolves the preferences file and the configuration directory.
//
// Preferences always live in the default directory, because they record which
// directory is active; an explicit ConfigDir only redirects client.yaml.
func initialStores(configDirOverride string) (*PrefsStore, Paths, error) {
	defaults, err := DefaultPaths()
	if err != nil {
		return nil, Paths{}, err
	}
	store := NewPrefsStore(defaults.Prefs)
	prefs, err := store.Load()
	if err != nil {
		return nil, Paths{}, err
	}
	target := strings.TrimSpace(prefs.ConfigDir)
	if override := strings.TrimSpace(configDirOverride); override != "" {
		target = override
	}
	if target == "" {
		return store, defaults, nil
	}
	paths, err := ResolvePaths(target)
	if err != nil {
		return nil, Paths{}, err
	}
	return store, paths, nil
}

// Paths returns the currently resolved filesystem locations.
func (a *App) Paths() Paths {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.paths
}

// Close stops the hosted client and releases the run context. It is idempotent.
func (a *App) Close() error {
	err := a.StopRuntime()
	a.mu.Lock()
	cancel := a.runCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return err
}

// SettingsView is the general tab.
type SettingsView struct {
	Language         string `json:"language"`
	Theme            string `json:"theme"`
	ConfigDir        string `json:"configDir"`
	ClientConfigPath string `json:"clientConfigPath"`
	PrefsPath        string `json:"prefsPath"`
	LockPath         string `json:"lockPath"`
	LaunchAtLogin    bool   `json:"launchAtLogin"`
	// LaunchAtLoginSupported is false where the platform has no login-item API, so the
	// interface can disable the switch instead of pretending it worked.
	LaunchAtLoginSupported bool `json:"launchAtLoginSupported"`
	// LaunchAtLoginError carries the reason the last registration attempt failed. An
	// unsigned or ad-hoc signed bundle is refused by SMAppService, and the operator has
	// to be told rather than left looking at a switch that silently does nothing.
	LaunchAtLoginError string   `json:"launchAtLoginError,omitempty"`
	MinimizeToTray     bool     `json:"minimizeToTray"`
	Languages          []string `json:"languages"`
	Themes             []string `json:"themes"`
	Modes              []string `json:"modes"`
	Protocols          []string `json:"protocols"`
}

// SettingsUpdate is a general-tab save. Pointer fields keep "not supplied" distinct
// from "turned off", which matters for the login item.
type SettingsUpdate struct {
	Language       string `json:"language"`
	Theme          string `json:"theme"`
	ConfigDir      string `json:"configDir"`
	LaunchAtLogin  *bool  `json:"launchAtLogin,omitempty"`
	MinimizeToTray *bool  `json:"minimizeToTray,omitempty"`
}

// Settings renders the general tab.
func (a *App) Settings(ctx context.Context) (SettingsView, error) {
	prefs, err := a.loadPrefs()
	if err != nil {
		return SettingsView{}, err
	}
	a.mu.Lock()
	paths, autostartErr := a.paths, a.autostartErr
	a.mu.Unlock()
	return SettingsView{
		Language:               prefs.Language,
		Theme:                  prefs.Theme,
		ConfigDir:              paths.ConfigDir,
		ClientConfigPath:       paths.ClientYAML,
		PrefsPath:              paths.Prefs,
		LockPath:               paths.Lock,
		LaunchAtLogin:          prefs.LaunchAtLoginEnabled(),
		LaunchAtLoginSupported: a.autostart.Supported(),
		LaunchAtLoginError:     autostartErr,
		MinimizeToTray:         prefs.MinimizeToTray,
		Languages:              []string{LanguageSystem, LanguageZhCN, LanguageEnUS},
		Themes:                 []string{ThemeSystem, ThemeLight, ThemeDark},
		Modes:                  []string{config.ModeLocal, config.ModeCluster},
		Protocols:              client.SupportedTunnelProtocols(),
	}, nil
}

// SaveSettings applies a general-tab save.
//
// The login item is registered before the preference is written, and a refusal aborts
// the save: recording "launch at login: on" for a bundle the system rejected would make
// the interface claim something that is not true.
func (a *App) SaveSettings(ctx context.Context, update SettingsUpdate) (SettingsView, error) {
	prefs, err := a.loadPrefs()
	if err != nil {
		return SettingsView{}, err
	}
	if update.Language != "" {
		prefs.Language = update.Language
	}
	if update.Theme != "" {
		prefs.Theme = update.Theme
	}
	if update.MinimizeToTray != nil {
		prefs.MinimizeToTray = *update.MinimizeToTray
	}

	var paths Paths
	repointed := false
	if raw := update.ConfigDir; raw != "" {
		resolved, err := ResolvePaths(raw)
		if err != nil {
			return SettingsView{}, err
		}
		paths, repointed = resolved, true
		prefs.ConfigDir = resolved.ConfigDir
	}

	if update.LaunchAtLogin != nil {
		if err := a.applyAutostart(*update.LaunchAtLogin); err != nil {
			return SettingsView{}, err
		}
		prefs.LaunchAtLogin = boolPtr(*update.LaunchAtLogin)
	}

	if repointed {
		// The hosted client belongs to the previous configuration. Leaving it running
		// while the interface shows a different directory would misreport which tunnels
		// are up, so it is stopped and the operator starts it again from the new file.
		if err := a.StopRuntime(); err != nil {
			return SettingsView{}, err
		}
		a.mu.Lock()
		a.paths = paths
		a.config = NewConfigStore(paths.ClientYAML)
		a.validator = NewValidator(a.server, a.config)
		a.mu.Unlock()
	}

	if err := a.prefs.Save(prefs); err != nil {
		return SettingsView{}, err
	}
	if a.options.OnPreferencesChanged != nil {
		a.options.OnPreferencesChanged(prefs)
	}
	return a.Settings(ctx)
}

// applyAutostart registers or unregisters the login item and records the outcome.
func (a *App) applyAutostart(enabled bool) error {
	a.mu.Lock()
	a.autostartErr = ""
	a.mu.Unlock()
	if !a.autostart.Supported() {
		return nil
	}
	if err := a.autostart.SetEnabled(enabled); err != nil {
		a.mu.Lock()
		a.autostartErr = err.Error()
		a.mu.Unlock()
		return fmt.Errorf("tray: cannot change launch at login: %w", err)
	}
	return nil
}

// SyncAutostart applies the stored preference at start-up.
//
// The preference is the intent and the login item is the fact; they drift when a bundle
// is replaced, moved or re-signed. Reconciling them once at launch keeps the switch in
// the interface honest, and a failure is recorded for display instead of aborting the
// tray: tunnels matter more than a login item.
func (a *App) SyncAutostart() error {
	prefs, err := a.loadPrefs()
	if err != nil {
		return err
	}
	return a.applyAutostart(prefs.LaunchAtLoginEnabled())
}

func (a *App) loadPrefs() (Preferences, error) {
	prefs, err := a.prefs.Load()
	if err != nil {
		return Preferences{}, err
	}
	return prefs, nil
}

// Preferences returns the stored interface preferences. The native shell reads this to
// decide what a window close means.
func (a *App) Preferences() (Preferences, error) { return a.loadPrefs() }

// TunnelView is one client.tunnels entry as the routing tab edits it.
type TunnelView struct {
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	Listen      string `json:"listen"`
	AgentID     string `json:"agentId"`
	TargetHost  string `json:"targetHost"`
	TargetPort  int    `json:"targetPort"`
	AuthMode    string `json:"authMode,omitempty"`
	AllowRemote bool   `json:"allowRemote"`
	AuthURL     string `json:"authUrl,omitempty"`
}

func tunnelViewOf(tunnel config.TunnelConfig) TunnelView {
	return TunnelView{
		Name: tunnel.Name, Protocol: strings.ToLower(strings.TrimSpace(tunnel.Protocol)),
		Listen: tunnel.ListenAddr, AgentID: tunnel.AgentID,
		TargetHost: tunnel.TargetHost, TargetPort: tunnel.TargetPort,
		AuthMode: tunnel.AuthMode, AllowRemote: tunnel.AllowRemote, AuthURL: tunnel.AuthURL,
	}
}

func (v TunnelView) toConfig() config.TunnelConfig {
	return config.TunnelConfig{
		Name: strings.TrimSpace(v.Name), Protocol: strings.ToLower(strings.TrimSpace(v.Protocol)),
		ListenAddr: strings.TrimSpace(v.Listen), AgentID: strings.TrimSpace(v.AgentID),
		TargetHost: strings.TrimSpace(v.TargetHost), TargetPort: v.TargetPort,
		AuthMode: strings.TrimSpace(v.AuthMode), AllowRemote: v.AllowRemote,
		AuthURL: strings.TrimSpace(v.AuthURL),
	}
}

// RoutingView is the routing tab. The token is deliberately absent: the webview only
// ever learns whether one is stored, so a compromised renderer cannot exfiltrate it.
type RoutingView struct {
	Mode         string       `json:"mode"`
	ServerURL    string       `json:"serverUrl"`
	TokenPresent bool         `json:"tokenPresent"`
	Tunnels      []TunnelView `json:"tunnels"`
	ConfigPath   string       `json:"configPath"`
	Running      bool         `json:"running"`
	Protocols    []string     `json:"protocols"`
}

// RoutingUpdate is a routing-tab save.
type RoutingUpdate struct {
	Mode      string `json:"mode"`
	ServerURL string `json:"serverUrl"`
	// Token is tri-state: absent keeps the stored value, an empty string clears it and
	// any other value replaces it. A pointer is the only encoding that can tell "the
	// form never showed the secret" apart from "the operator deleted it".
	Token   *string      `json:"token"`
	Tunnels []TunnelView `json:"tunnels"`
}

// RoutingSaveResult reports what a save did to the hosted client.
type RoutingSaveResult struct {
	Routing   RoutingView `json:"routing"`
	Restarted bool        `json:"restarted"`
	// RuntimeError carries a restart failure. The configuration was still written: the
	// file is the operator's intent, and hiding it because the client could not come up
	// would lose the edit.
	RuntimeError string `json:"runtimeError,omitempty"`
}

// Routing renders the routing tab from client.yaml.
func (a *App) Routing(ctx context.Context) (RoutingView, error) {
	settings, paths, err := a.storedSettings()
	if err != nil {
		return RoutingView{}, err
	}
	return RoutingView{
		Mode:         settings.Mode,
		ServerURL:    settings.ServerURL,
		TokenPresent: strings.TrimSpace(settings.Token) != "",
		Tunnels:      tunnelViews(settings.Tunnels),
		ConfigPath:   paths.ClientYAML,
		Running:      a.RuntimeRunning(),
		Protocols:    client.SupportedTunnelProtocols(),
	}, nil
}

func tunnelViews(tunnels []config.TunnelConfig) []TunnelView {
	views := make([]TunnelView, 0, len(tunnels))
	for _, tunnel := range tunnels {
		views = append(views, tunnelViewOf(tunnel))
	}
	return views
}

// SaveRouting validates, writes client.yaml and restarts a running client.
//
// Validation happens through the real loader before anything touches disk, so a
// rejected save leaves the previous configuration - and the tunnels it carries - intact.
func (a *App) SaveRouting(ctx context.Context, update RoutingUpdate) (RoutingSaveResult, error) {
	candidate, err := a.candidateSettings(update)
	if err != nil {
		return RoutingSaveResult{}, err
	}
	a.mu.Lock()
	store := a.config
	a.mu.Unlock()
	// The authoritative structural check: cluster mode without MySQL storage, a
	// non-loopback listener without allow_remote and an unknown protocol all have to be
	// caught here rather than at launch.
	if _, err := store.BuildConfig(ctx, candidate); err != nil {
		return RoutingSaveResult{}, fmt.Errorf("%w: %v", ErrConfigUnusable, err)
	}
	if err := store.Save(candidate); err != nil {
		return RoutingSaveResult{}, err
	}

	result := RoutingSaveResult{}
	wasRunning := a.RuntimeRunning()
	if wasRunning {
		if err := a.RestartRuntime(ctx); err != nil {
			result.RuntimeError = err.Error()
		} else {
			result.Restarted = true
		}
	}
	result.Routing, err = a.Routing(ctx)
	if err != nil {
		return RoutingSaveResult{}, err
	}
	return result, nil
}

// candidateSettings merges a form submission with the stored token.
func (a *App) candidateSettings(update RoutingUpdate) (ClientSettings, error) {
	stored, _, err := a.storedSettings()
	if err != nil {
		return ClientSettings{}, err
	}
	token := stored.Token
	if update.Token != nil {
		token = strings.TrimSpace(*update.Token)
	}
	mode := strings.TrimSpace(update.Mode)
	if mode == "" {
		mode = config.ModeLocal
	}
	tunnels := make([]config.TunnelConfig, 0, len(update.Tunnels))
	for _, view := range update.Tunnels {
		tunnels = append(tunnels, view.toConfig())
	}
	return ClientSettings{
		Mode:      mode,
		ServerURL: strings.TrimSpace(update.ServerURL),
		Token:     token,
		Tunnels:   tunnels,
	}, nil
}

func (a *App) storedSettings() (ClientSettings, Paths, error) {
	a.mu.Lock()
	store, paths := a.config, a.paths
	a.mu.Unlock()
	settings, err := store.Load()
	if err != nil {
		return ClientSettings{}, Paths{}, err
	}
	if strings.TrimSpace(settings.Mode) == "" {
		settings.Mode = config.ModeLocal
	}
	return settings, paths, nil
}

// Agents returns the Agents the stored token may use.
//
// serverURL may be blank to use the configured one, which is what the routing tab does
// before the form is saved. The token is never accepted from the caller: it stays in Go.
func (a *App) Agents(ctx context.Context, serverURL string) ([]AgentRef, error) {
	settings, _, err := a.storedSettings()
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(settings.Token)
	if token == "" {
		return nil, ErrMissingToken
	}
	target := strings.TrimSpace(serverURL)
	if target == "" {
		target = settings.ServerURL
	}
	if strings.TrimSpace(target) == "" {
		return nil, errors.New("tray: client.server_url is required")
	}
	return a.server.ListAgents(ctx, target, token)
}

// Validate runs the routing tab's 检测 button.
//
// A nil update validates the stored configuration; a supplied update validates the form
// as it stands, which is what lets an operator check a change before saving it.
func (a *App) Validate(ctx context.Context, update *RoutingUpdate) (ValidationReport, error) {
	var settings ClientSettings
	var err error
	if update == nil {
		settings, _, err = a.storedSettings()
	} else {
		settings, err = a.candidateSettings(*update)
	}
	if err != nil {
		return ValidationReport{}, err
	}
	a.mu.Lock()
	validator := a.validator
	a.mu.Unlock()
	// Listen addresses are only probe-bound while the tray is not holding them.
	validator.SetBindProbes(!a.RuntimeRunning())
	return validator.Validate(ctx, settings), nil
}

// TunnelStat is one row of the statistics table.
type TunnelStat struct {
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	Listen    string `json:"listen"`
	AgentID   string `json:"agentId"`
	Target    string `json:"target,omitempty"`
	State     string `json:"state"`
	LastError string `json:"lastError,omitempty"`
}

// AgentStat is one per-Agent connection pool.
type AgentStat struct {
	AgentID       string `json:"agentId"`
	OpenSlots     int    `json:"openSlots"`
	ReadySessions int    `json:"readySessions"`
	ActiveStreams int    `json:"activeStreams"`
}

// StatTotals summarizes the run.
type StatTotals struct {
	Tunnels       int `json:"tunnels"`
	Listening     int `json:"listening"`
	Failed        int `json:"failed"`
	Agents        int `json:"agents"`
	OpenSlots     int `json:"openSlots"`
	ReadySessions int `json:"readySessions"`
	ActiveStreams int `json:"activeStreams"`
	Reconnects    int `json:"reconnects"`
	// BytesInbound counts frame payload the client session read from the Server. Only
	// the inbound direction is instrumented today, so the view reports one honest
	// counter instead of a symmetric pair with a fabricated half.
	BytesInbound int64 `json:"bytesInbound"`
}

// StatsView is the statistics tab.
type StatsView struct {
	Running         bool         `json:"running"`
	StartedAt       time.Time    `json:"startedAt,omitempty"`
	UptimeSeconds   int64        `json:"uptimeSeconds"`
	ServerURL       string       `json:"serverUrl,omitempty"`
	ServerReachable bool         `json:"serverReachable"`
	ServerProbe     string       `json:"serverProbe,omitempty"`
	Connected       bool         `json:"connected"`
	LockBlocked     bool         `json:"lockBlocked"`
	RuntimeError    string       `json:"runtimeError,omitempty"`
	Tunnels         []TunnelStat `json:"tunnels"`
	Agents          []AgentStat  `json:"agents"`
	Totals          StatTotals   `json:"totals"`
	CollectedAt     time.Time    `json:"collectedAt"`
}

// Stats renders the statistics tab. It never blocks on the network: the reachability
// answer is cached and refreshed in the background, because the tab polls every couple
// of seconds and must stay responsive when the Server is unreachable.
func (a *App) Stats(ctx context.Context) StatsView {
	view := StatsView{CollectedAt: time.Now().UTC(), Tunnels: []TunnelStat{}, Agents: []AgentStat{}}

	a.mu.Lock()
	rt, runtimeErr, lockBlocked := a.runtime, a.runtimeErr, a.lockBlocked
	store := a.config
	a.mu.Unlock()

	if rt != nil {
		stats := rt.Stats()
		view.Running = stats.Running
		view.StartedAt = stats.StartedAt
		view.UptimeSeconds = int64(stats.Uptime.Seconds())
		view.ServerURL = stats.ServerURL
		view.Connected = stats.Connected
		view.Totals.Reconnects = stats.Reconnects
		for _, tunnel := range stats.Tunnels {
			view.Tunnels = append(view.Tunnels, TunnelStat{
				Name: tunnel.Name, Protocol: tunnel.Protocol, Listen: tunnel.ListenAddr,
				AgentID: tunnel.AgentID, Target: tunnel.Target, State: tunnel.State,
				LastError: tunnel.LastError,
			})
		}
		for _, agent := range stats.Agents {
			view.Agents = append(view.Agents, AgentStat{
				AgentID: agent.AgentID, OpenSlots: agent.OpenSlots,
				ReadySessions: agent.ReadySessions, ActiveStreams: agent.ActiveStreams,
			})
			view.Totals.Agents++
			view.Totals.OpenSlots += agent.OpenSlots
			view.Totals.ReadySessions += agent.ReadySessions
			view.Totals.ActiveStreams += agent.ActiveStreams
		}
	} else if settings, err := store.Load(); err == nil {
		// Nothing is running, but the tab still has to show what is configured: an empty
		// table gives no hint that a stopped client has tunnels waiting.
		view.ServerURL = settings.ServerURL
		for _, tunnel := range settings.Tunnels {
			view.Tunnels = append(view.Tunnels, TunnelStat{
				Name:     client.TunnelDisplayName(tunnel),
				Protocol: strings.ToLower(strings.TrimSpace(tunnel.Protocol)),
				Listen:   tunnel.ListenAddr, AgentID: strings.TrimSpace(tunnel.AgentID),
				Target: tunnelTargetOf(tunnel), State: client.TunnelStateStopped,
			})
		}
	}
	if runtimeErr != nil {
		view.RuntimeError = runtimeErr.Error()
	}
	view.LockBlocked = lockBlocked

	view.Totals.Tunnels = len(view.Tunnels)
	for _, tunnel := range view.Tunnels {
		switch tunnel.State {
		case client.TunnelStateListening:
			view.Totals.Listening++
		case client.TunnelStateFailed:
			view.Totals.Failed++
		}
	}
	view.Totals.BytesInbound = a.inboundBytes()
	view.ServerReachable, view.ServerProbe = a.refreshServerProbe(view.ServerURL)
	return view
}

func tunnelTargetOf(tunnel config.TunnelConfig) string {
	if strings.TrimSpace(tunnel.TargetHost) == "" || tunnel.TargetPort == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", strings.TrimSpace(tunnel.TargetHost), tunnel.TargetPort)
}

// inboundBytes gathers the client's inbound frame counter from the private registry.
func (a *App) inboundBytes() int64 {
	if a.registry == nil {
		return 0
	}
	families, err := a.registry.Gather()
	if err != nil {
		return 0
	}
	var total int64
	for _, family := range families {
		if family.GetName() != "tunnelmesh_bytes_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			var component, direction string
			for _, pair := range metric.GetLabel() {
				switch pair.GetName() {
				case "component":
					component = pair.GetValue()
				case "direction":
					direction = pair.GetValue()
				}
			}
			if component != "client" || direction != "inbound" {
				continue
			}
			total += int64(metric.GetCounter().GetValue())
		}
	}
	return total
}

// refreshServerProbe returns the cached reachability answer and schedules a background
// refresh when it went stale.
func (a *App) refreshServerProbe(serverURL string) (bool, string) {
	serverURL = strings.TrimSpace(serverURL)
	a.mu.Lock()
	if serverURL == "" {
		a.probe = serverProbeState{}
		a.mu.Unlock()
		return false, ""
	}
	if a.probe.target == serverURL && time.Since(a.probe.at) < serverProbeTTL {
		reachable, detail := a.probe.reachable, a.probe.detail
		a.mu.Unlock()
		return reachable, detail
	}
	if a.probe.inflight && a.probe.target == serverURL {
		reachable, detail := a.probe.reachable, a.probe.detail
		a.mu.Unlock()
		return reachable, detail
	}
	reachable, detail := a.probe.reachable, a.probe.detail
	if a.probe.target != serverURL {
		reachable, detail = false, ""
	}
	a.probe.inflight = true
	a.probe.target = serverURL
	client := a.server
	a.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), serverProbeTimeout)
		defer cancel()
		err := client.CheckHealth(ctx, serverURL)
		a.mu.Lock()
		a.probe.inflight = false
		a.probe.at = time.Now()
		a.probe.reachable = err == nil
		if err != nil {
			a.probe.detail = err.Error()
		} else {
			a.probe.detail = ""
		}
		a.mu.Unlock()
	}()
	return reachable, detail
}

// RuntimeRunning reports whether the tray currently hosts the client.
func (a *App) RuntimeRunning() bool {
	a.mu.Lock()
	rt := a.runtime
	a.mu.Unlock()
	return rt != nil && rt.Stats().Running
}

// StartRuntime loads client.yaml and hosts the tunnels in this process.
//
// It is idempotent, and it reports the two failures the interface has to explain
// separately: an unusable configuration and a lock held by another client.
func (a *App) StartRuntime(ctx context.Context) error {
	a.mu.Lock()
	if a.runtime != nil && a.runtime.Stats().Running {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	settings, paths, err := a.storedSettings()
	if err != nil {
		return a.recordStartFailure(fmt.Errorf("%w: %v", ErrConfigUnusable, err))
	}
	a.mu.Lock()
	store := a.config
	a.mu.Unlock()
	cfg, err := store.BuildConfig(ctx, settings)
	if err != nil {
		return a.recordStartFailure(fmt.Errorf("%w: %v", ErrConfigUnusable, err))
	}
	rt, err := client.NewRuntimeFromConfig(ctx, cfg, client.RuntimeOptions{
		ConfigPath: paths.ClientYAML,
		InstanceID: a.options.InstanceID,
		Metrics:    a.metrics,
		Runner:     a.options.Runner,
	})
	if err != nil {
		// The preconditions here are the operator-facing "client run requires ..."
		// messages, which are configuration problems rather than transport failures.
		return a.recordStartFailure(fmt.Errorf("%w: %v", ErrConfigUnusable, err))
	}
	a.mu.Lock()
	runCtx := a.runCtx
	a.mu.Unlock()
	if err := rt.Start(runCtx); err != nil {
		_ = rt.Stop()
		if errors.Is(err, client.ErrRuntimeAlreadyRunning) {
			return a.recordStartFailure(err)
		}
		return a.recordStartFailure(fmt.Errorf("%w: %v", ErrConfigUnusable, err))
	}
	a.mu.Lock()
	a.runtime = rt
	a.runtimeErr = nil
	a.lockBlocked = false
	a.mu.Unlock()
	// The run ended on its own: record why so the statistics tab can explain a client
	// that stopped without anybody asking it to.
	go a.watchRuntime(rt)
	return nil
}

// watchRuntime records a spontaneous end of the run.
func (a *App) watchRuntime(rt *client.Runtime) {
	err := rt.Wait(a.runCtx)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runtime != rt {
		return
	}
	a.runtime = nil
	if err != nil && !errors.Is(err, context.Canceled) {
		a.runtimeErr = err
	}
}

func (a *App) recordStartFailure(err error) error {
	a.mu.Lock()
	a.runtime = nil
	a.runtimeErr = err
	a.lockBlocked = errors.Is(err, client.ErrRuntimeAlreadyRunning)
	a.mu.Unlock()
	return err
}

// StopRuntime ends the hosted client and releases the advisory lock. It is idempotent.
func (a *App) StopRuntime() error {
	a.mu.Lock()
	rt := a.runtime
	a.runtime = nil
	a.mu.Unlock()
	if rt == nil {
		return nil
	}
	return rt.Stop()
}

// RestartRuntime stops and starts the hosted client so saved routing takes effect.
func (a *App) RestartRuntime(ctx context.Context) error {
	if err := a.StopRuntime(); err != nil {
		return err
	}
	return a.StartRuntime(ctx)
}

// SystemInfo identifies the host for the about tab.
type SystemInfo struct {
	GOOS      string `json:"goos"`
	Arch      string `json:"arch"`
	OSVersion string `json:"osVersion,omitempty"`
}

func defaultSystemInfo() SystemInfo {
	return SystemInfo{GOOS: runtime.GOOS, Arch: runtime.GOARCH}
}

// AboutView is the about tab. It carries no secret: the token is reduced to a boolean.
type AboutView struct {
	Version   string     `json:"version"`
	Commit    string     `json:"commit"`
	BuildTime string     `json:"buildTime"`
	System    SystemInfo `json:"system"`

	ConfigDir        string `json:"configDir"`
	ClientConfigPath string `json:"clientConfigPath"`
	PrefsPath        string `json:"prefsPath"`
	LockPath         string `json:"lockPath"`
	InstanceID       string `json:"instanceId,omitempty"`
	InstanceIDPath   string `json:"instanceIdPath,omitempty"`

	Mode         string `json:"mode"`
	ServerURL    string `json:"serverUrl,omitempty"`
	TunnelCount  int    `json:"tunnelCount"`
	TokenPresent bool   `json:"tokenPresent"`
	Running      bool   `json:"running"`

	WebsiteURL  string `json:"websiteUrl"`
	ReleasesURL string `json:"releasesUrl"`
	DocsURL     string `json:"docsUrl"`
	License     string `json:"license"`
}

// About renders the about tab.
func (a *App) About(ctx context.Context) AboutView {
	info := build.Current()
	system := defaultSystemInfo()
	if a.options.SystemProbe != nil {
		if probed := a.options.SystemProbe(); probed.GOOS != "" {
			system = probed
		}
	}
	settings, paths, err := a.storedSettings()
	view := AboutView{
		Version: info.Version, Commit: info.Commit, BuildTime: info.BuildTime,
		System:           system,
		ConfigDir:        paths.ConfigDir,
		ClientConfigPath: paths.ClientYAML,
		PrefsPath:        paths.Prefs,
		LockPath:         paths.Lock,
		Mode:             config.ModeLocal,
		WebsiteURL:       WebsiteURL, ReleasesURL: ReleasesURL, DocsURL: DocsURL, License: License,
		Running: a.RuntimeRunning(),
	}
	if err != nil {
		return view
	}
	view.Mode = settings.Mode
	view.ServerURL = settings.ServerURL
	view.TunnelCount = len(settings.Tunnels)
	view.TokenPresent = strings.TrimSpace(settings.Token) != ""

	a.mu.Lock()
	store := a.config
	a.mu.Unlock()
	if cfg, cfgErr := store.BuildConfig(ctx, settings); cfgErr == nil {
		view.InstanceID, view.InstanceIDPath = instanceIdentity(cfg)
	}
	// An injected identity wins: it is what the hosted runtime will report, and the
	// about tab has to describe the running client rather than the file on disk.
	if override := strings.TrimSpace(a.options.InstanceID); override != "" {
		view.InstanceID = override
	}
	if view.InstanceIDPath == "" {
		view.InstanceIDPath = client.DefaultClientInstanceIDPath()
	}
	return view
}

// instanceIdentity reports the client identity without creating it.
//
// EnsureClientInstanceID writes the file, which is right at launch and wrong for a
// display-only view: opening the about tab must not leave state behind.
func instanceIdentity(cfg config.Config) (string, string) {
	path := strings.TrimSpace(cfg.Client.InstanceIDPath)
	if path == "" {
		path = client.DefaultClientInstanceIDPath()
	}
	if id := strings.TrimSpace(cfg.Client.InstanceID); id != "" {
		return id, path
	}
	if data, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(data)), path
	}
	return "", path
}

// OpenURL hands a URL to the operating system.
func (a *App) OpenURL(target string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("tray: no URL to open")
	}
	if a.options.OpenURL == nil {
		return errors.New("tray: opening a browser needs the native tray shell")
	}
	return a.options.OpenURL(target)
}

// OpenWebsite implements the tray menu's "打开官网首页".
func (a *App) OpenWebsite() error { return a.OpenURL(WebsiteURL) }

// OpenReleases implements the about tab's "检查更新".
func (a *App) OpenReleases() error { return a.OpenURL(ReleasesURL) }

// OpenDocs implements the about tab's documentation link.
func (a *App) OpenDocs() error { return a.OpenURL(DocsURL) }

// RequestQuit marks the tray as shutting down. The native shell polls this when a window
// closes so "关闭时最小化到系统托盘" off ends the process instead of hiding it.
func (a *App) RequestQuit() {
	a.mu.Lock()
	a.quitRequested = true
	a.mu.Unlock()
}

// QuitRequested reports whether a quit was asked for.
func (a *App) QuitRequested() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.quitRequested
}
