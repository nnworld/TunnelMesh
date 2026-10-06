package tray

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// fakeAutostart records login-item requests so the general tab can be tested without
// touching the real SMAppService.
type fakeAutostart struct {
	supported bool
	enabled   bool
	failWith  error
	calls     []bool
}

func (f *fakeAutostart) Supported() bool { return f.supported }

func (f *fakeAutostart) Enabled() (bool, error) { return f.enabled, nil }

func (f *fakeAutostart) SetEnabled(enabled bool) error {
	f.calls = append(f.calls, enabled)
	if f.failWith != nil {
		return f.failWith
	}
	f.enabled = enabled
	return nil
}

// blockingRunner keeps a session slot parked for the lifetime of the run. The pool
// tolerates a runner that never reports a session; it simply shows zero ready
// sessions, which is all the statistics tests need.
func blockingRunner() client.SessionPoolRunner {
	return func(ctx context.Context, _, _ string, _ func(*client.Session) error, _ client.WebSocketRunOptions) error {
		<-ctx.Done()
		return ctx.Err()
	}
}

// agentsEndpoint answers the client-scoped Agent list with a single online Agent.
func agentsEndpoint(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "msg": "OK",
			"data": map[string]any{
				"items":      []AgentRef{{ID: "agent-a", Name: "office", Online: true}},
				"nextCursor": "", "hasMore": false,
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func newTestApp(t *testing.T, mutate func(*AppOptions)) (*App, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	options := AppOptions{Runner: blockingRunner(), InstanceID: "tray-test", Autostart: &fakeAutostart{supported: true}}
	if mutate != nil {
		mutate(&options)
	}
	app, err := NewApp(options)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app, filepath.Join(home, ".config", ConfigDirName)
}

// writeClientConfig stores a configuration the runtime accepts.
func writeClientConfig(t *testing.T, app *App, tunnels ...config.TunnelConfig) {
	t.Helper()
	settings := ClientSettings{
		Mode:      config.ModeLocal,
		ServerURL: "ws://127.0.0.1:1/ws/client",
		Token:     "stored-token",
		Tunnels:   tunnels,
	}
	if err := app.config.Save(settings); err != nil {
		t.Fatalf("save configuration: %v", err)
	}
}

func loopbackTunnel(port int) config.TunnelConfig {
	return config.TunnelConfig{
		Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:0", AgentID: "agent-a",
		TargetHost: "10.0.0.8", TargetPort: port,
	}
}

func TestAppSettingsDefaults(t *testing.T) {
	app, dir := newTestApp(t, nil)
	view, err := app.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Language != LanguageSystem || view.Theme != ThemeSystem {
		t.Fatalf("defaults = %+v", view)
	}
	if view.ConfigDir != dir {
		t.Fatalf("ConfigDir = %q, want %q", view.ConfigDir, dir)
	}
	if view.ClientConfigPath != filepath.Join(dir, ClientConfigFileName) {
		t.Fatalf("ClientConfigPath = %q", view.ClientConfigPath)
	}
	if view.PrefsPath != filepath.Join(dir, PrefsFileName) {
		t.Fatalf("PrefsPath = %q", view.PrefsPath)
	}
	if !view.LaunchAtLogin || !view.LaunchAtLoginSupported {
		t.Fatalf("launch at login = %+v", view)
	}
	if !view.MinimizeToTray {
		t.Fatal("MinimizeToTray must default to true")
	}
	if len(view.Languages) != 3 || len(view.Themes) != 3 || len(view.Modes) != 2 {
		t.Fatalf("option lists = %+v", view)
	}
	if len(view.Protocols) != len(client.SupportedTunnelProtocols()) {
		t.Fatalf("Protocols = %+v", view.Protocols)
	}
}

func TestAppSaveSettingsPersists(t *testing.T) {
	autostart := &fakeAutostart{supported: true}
	app, dir := newTestApp(t, func(options *AppOptions) { options.Autostart = autostart })
	off := false
	view, err := app.SaveSettings(context.Background(), SettingsUpdate{
		Language: LanguageEnUS, Theme: ThemeDark,
		LaunchAtLogin: &off, MinimizeToTray: &off,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Language != LanguageEnUS || view.Theme != ThemeDark {
		t.Fatalf("view = %+v", view)
	}
	if view.LaunchAtLogin || view.MinimizeToTray {
		t.Fatalf("view = %+v", view)
	}
	stored, err := os.ReadFile(filepath.Join(dir, PrefsFileName))
	if err != nil {
		t.Fatalf("preferences were not written: %v", err)
	}
	if !strings.Contains(string(stored), `"en-US"`) || !strings.Contains(string(stored), `"dark"`) {
		t.Fatalf("stored preferences = %s", stored)
	}
	info, err := os.Stat(filepath.Join(dir, PrefsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("preferences mode = %v, want 0600", info.Mode().Perm())
	}
	if len(autostart.calls) != 1 || autostart.calls[0] {
		t.Fatalf("login item calls = %v, want a single disable", autostart.calls)
	}
}

func TestAppSaveSettingsNormalizesUnknownValues(t *testing.T) {
	app, _ := newTestApp(t, nil)
	view, err := app.SaveSettings(context.Background(), SettingsUpdate{Language: "klingon", Theme: "neon"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Language != LanguageSystem || view.Theme != ThemeSystem {
		t.Fatalf("unsupported values must fall back to the defaults, got %+v", view)
	}
}

func TestAppSaveSettingsRepointsConfigDir(t *testing.T) {
	app, _ := newTestApp(t, nil)
	other := t.TempDir()
	view, err := app.SaveSettings(context.Background(), SettingsUpdate{ConfigDir: other})
	if err != nil {
		t.Fatal(err)
	}
	if view.ConfigDir != other || view.ClientConfigPath != filepath.Join(other, ClientConfigFileName) {
		t.Fatalf("view = %+v", view)
	}
	if app.Paths().Lock != filepath.Join(other, LockFileName) {
		t.Fatalf("lock path = %q, want it derived from the new configuration directory", app.Paths().Lock)
	}
	// A directory without client.yaml still has to produce an editable form.
	routing, err := app.Routing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if routing.TokenPresent || len(routing.Tunnels) != 0 {
		t.Fatalf("routing = %+v", routing)
	}
	if routing.ConfigPath != filepath.Join(other, ClientConfigFileName) {
		t.Fatalf("routing.ConfigPath = %q", routing.ConfigPath)
	}
}

func TestAppSaveSettingsRejectsUnusableConfigDir(t *testing.T) {
	app, _ := newTestApp(t, nil)
	before := app.Paths()
	if _, err := app.SaveSettings(context.Background(), SettingsUpdate{ConfigDir: "   "}); err == nil {
		t.Fatal("a blank configuration directory must be rejected")
	}
	if app.Paths() != before {
		t.Fatal("a rejected save must not repoint the stores")
	}
}

func TestAppSaveSettingsLaunchAtLoginFailureKeepsPreference(t *testing.T) {
	autostart := &fakeAutostart{supported: true, failWith: errors.New("smappservice refused")}
	app, _ := newTestApp(t, func(options *AppOptions) { options.Autostart = autostart })
	on := true
	if _, err := app.SaveSettings(context.Background(), SettingsUpdate{LaunchAtLogin: &on}); err == nil {
		t.Fatal("a refused login item must be reported instead of silently saved")
	} else if !strings.Contains(err.Error(), "smappservice refused") {
		t.Fatalf("error = %v", err)
	}
	view, err := app.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !view.LaunchAtLogin {
		t.Fatal("the stored preference must not claim a login item that was never registered")
	}
}

func TestAppSaveSettingsReportsUnsupportedAutostart(t *testing.T) {
	app, _ := newTestApp(t, func(options *AppOptions) { options.Autostart = &fakeAutostart{} })
	on := true
	view, err := app.SaveSettings(context.Background(), SettingsUpdate{LaunchAtLogin: &on})
	if err != nil {
		t.Fatal(err)
	}
	if view.LaunchAtLoginSupported {
		t.Fatal("LaunchAtLoginSupported = true for an unsupported platform")
	}
	if !view.LaunchAtLogin {
		t.Fatal("the intent must still be recorded so a supported platform applies it later")
	}
}

func TestAppRoutingNeverReturnsTheToken(t *testing.T) {
	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	view := mustRouting(t, app)
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "stored-token") {
		t.Fatalf("the routing view leaked the token: %s", encoded)
	}
	if !view.TokenPresent {
		t.Fatal("TokenPresent = false although client.yaml carries a token")
	}
	if len(view.Tunnels) != 1 || view.Tunnels[0].Name != "web" {
		t.Fatalf("tunnels = %+v", view.Tunnels)
	}
	if view.Mode != config.ModeLocal {
		t.Fatalf("Mode = %q", view.Mode)
	}
}

func TestAppSaveRoutingTokenSemantics(t *testing.T) {
	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	ctx := context.Background()

	// A nil token means "the form did not touch it", so the stored value survives.
	if _, err := app.SaveRouting(ctx, routingUpdateFrom(t, app, nil)); err != nil {
		t.Fatal(err)
	}
	if got := storedToken(t, app); got != "stored-token" {
		t.Fatalf("token = %q, want the stored value to survive a nil update", got)
	}

	replacement := "rotated-token"
	if _, err := app.SaveRouting(ctx, routingUpdateFrom(t, app, &replacement)); err != nil {
		t.Fatal(err)
	}
	if got := storedToken(t, app); got != "rotated-token" {
		t.Fatalf("token = %q, want the replacement", got)
	}

	empty := ""
	if _, err := app.SaveRouting(ctx, routingUpdateFrom(t, app, &empty)); err != nil {
		t.Fatal(err)
	}
	if got := storedToken(t, app); got != "" {
		t.Fatalf("token = %q, want an explicit empty string to clear it", got)
	}

	info, err := os.Stat(app.paths.ClientYAML)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("client.yaml mode = %v, want 0600 because it carries the token", info.Mode().Perm())
	}
}

func TestAppSaveRoutingRejectsInvalidConfiguration(t *testing.T) {
	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	update := routingUpdateFrom(t, app, nil)
	// cluster mode without MySQL storage is exactly the mistake the interface has to
	// stop before it reaches config.Load at launch.
	update.Mode = config.ModeCluster
	if _, err := app.SaveRouting(context.Background(), update); err == nil {
		t.Fatal("cluster mode without mysql storage must be rejected")
	}
	settings, err := app.config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.Mode != config.ModeLocal {
		t.Fatalf("a rejected save changed the file: mode = %q", settings.Mode)
	}
}

func TestAppSaveRoutingRestartsARunningClient(t *testing.T) {
	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	ctx := context.Background()
	if err := app.StartRuntime(ctx); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	if !app.Stats(ctx).Running {
		t.Fatal("the runtime did not start")
	}
	update := routingUpdateFrom(t, app, nil)
	update.Tunnels[0].Name = "renamed"
	result, err := app.SaveRouting(ctx, update)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Restarted {
		t.Fatalf("saving the routing while the client runs must restart it: %+v", result)
	}
	if result.RuntimeError != "" {
		t.Fatalf("RuntimeError = %q", result.RuntimeError)
	}
	stats := app.Stats(ctx)
	if !stats.Running || len(stats.Tunnels) != 1 || stats.Tunnels[0].Name != "renamed" {
		t.Fatalf("stats after restart = %+v", stats)
	}
}

func TestAppSaveRoutingKeepsRunningClientOnInvalidConfig(t *testing.T) {
	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	ctx := context.Background()
	if err := app.StartRuntime(ctx); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	update := routingUpdateFrom(t, app, nil)
	update.Tunnels[0].Protocol = "quic"
	if _, err := app.SaveRouting(ctx, update); err == nil {
		t.Fatal("an unsupported protocol must be rejected")
	}
	if !app.RuntimeRunning() {
		t.Fatal("a rejected save must not tear down the running client")
	}
}

func TestAppAgentsUsesTheStoredToken(t *testing.T) {
	server := agentsEndpoint(t)
	var authorization string
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "msg": "OK",
			"data": map[string]any{
				"items":      []AgentRef{{ID: "agent-a", Name: "office", Online: true}},
				"nextCursor": "", "hasMore": false,
			},
		})
	})

	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	agents, err := app.Agents(context.Background(), strings.Replace(server.URL, "http://", "ws://", 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "agent-a" || !agents[0].Online {
		t.Fatalf("agents = %+v", agents)
	}
	if authorization != "Bearer stored-token" {
		t.Fatalf("Authorization = %q, want the stored token to be used and never exposed to the UI", authorization)
	}
	if _, err := app.Agents(context.Background(), ""); err == nil {
		t.Fatal("an unusable server URL must be reported")
	}
}

func TestAppAgentsWithoutToken(t *testing.T) {
	app, _ := newTestApp(t, nil)
	if _, err := app.Agents(context.Background(), "http://127.0.0.1:1"); !errors.Is(err, ErrMissingToken) {
		t.Fatalf("error = %v, want ErrMissingToken", err)
	}
}

func TestAppValidateUsesThePendingForm(t *testing.T) {
	server := agentsEndpoint(t)
	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	update := routingUpdateFrom(t, app, nil)
	update.ServerURL = strings.Replace(server.URL, "http://", "ws://", 1) + "/ws/client"
	report, err := app.Validate(context.Background(), &update)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid {
		t.Fatalf("report = %+v", report)
	}
	token, _ := report.find(CheckTokenValid)
	if token.Status != StatusPassed {
		t.Fatalf("token check = %+v", token)
	}
	if len(report.Agents) != 1 {
		t.Fatalf("the validation run must reuse the agent list it fetched: %+v", report.Agents)
	}
	// Validating the stored configuration must be possible without a form.
	if _, err := app.Validate(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestAppValidateSkipsBindProbesWhileRunning(t *testing.T) {
	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	if err := app.StartRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The tray holds every listener, so a probe would report the operator's own
	// tunnels as unavailable.
	update := routingUpdateFrom(t, app, nil)
	report, err := app.Validate(context.Background(), &update)
	if err != nil {
		t.Fatal(err)
	}
	listen, ok := report.find(CheckTunnelListen)
	if !ok || listen.Status != StatusSkipped {
		t.Fatalf("listen check while running = %+v (found=%v)", listen, ok)
	}
	if app.validator.bindProbes {
		t.Fatal("bind probes must be disabled while the runtime holds the listeners")
	}
}

func TestAppRuntimeLifecycleAndStats(t *testing.T) {
	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	ctx := context.Background()

	stats := app.Stats(ctx)
	if stats.Running {
		t.Fatal("a fresh tray must not report a running client")
	}
	if len(stats.Tunnels) != 1 || stats.Tunnels[0].Name != "web" || stats.Tunnels[0].State != client.TunnelStateStopped {
		t.Fatalf("the statistics tab must describe the configured tunnels even when stopped: %+v", stats.Tunnels)
	}

	if err := app.StartRuntime(ctx); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	// Starting twice is a no-op rather than an error: the interface polls and offers
	// a start button, and a race between them must not surface as a failure.
	if err := app.StartRuntime(ctx); err != nil {
		t.Fatalf("a second StartRuntime must be idempotent: %v", err)
	}
	stats = app.Stats(ctx)
	if !stats.Running {
		t.Fatal("Running = false after StartRuntime")
	}
	if stats.ServerURL != "ws://127.0.0.1:1/ws/client" {
		t.Fatalf("ServerURL = %q", stats.ServerURL)
	}
	if stats.Totals.Tunnels != 1 || stats.Totals.Listening != 1 {
		t.Fatalf("totals = %+v", stats.Totals)
	}
	if stats.RuntimeError != "" {
		t.Fatalf("RuntimeError = %q after a clean start", stats.RuntimeError)
	}

	if err := app.StopRuntime(); err != nil {
		t.Fatalf("StopRuntime: %v", err)
	}
	if app.RuntimeRunning() {
		t.Fatal("RuntimeRunning = true after StopRuntime")
	}
	if err := app.StartRuntime(ctx); err != nil {
		t.Fatalf("the lock must be released by StopRuntime: %v", err)
	}
}

func TestAppStartRuntimeReportsIncompleteConfiguration(t *testing.T) {
	app, _ := newTestApp(t, nil)
	if err := app.StartRuntime(context.Background()); err == nil {
		t.Fatal("starting without a configuration must fail")
	}
	stats := app.Stats(context.Background())
	if stats.RuntimeError == "" {
		t.Fatalf("the failure must stay visible in the statistics tab: %+v", stats)
	}
	if stats.Running {
		t.Fatal("Running = true after a failed start")
	}
}

func TestAppStartRuntimeReportsContestedLock(t *testing.T) {
	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	ctx := context.Background()
	if err := app.StartRuntime(ctx); err != nil {
		t.Fatal(err)
	}

	// A second App over the same configuration is what `client run` looks like from
	// here: it must be refused with the documented error and say so in the stats.
	other, err := NewApp(AppOptions{ConfigDir: app.paths.ConfigDir, Runner: blockingRunner(), InstanceID: "tray-test-2", Autostart: &fakeAutostart{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if err := other.StartRuntime(ctx); !errors.Is(err, client.ErrRuntimeAlreadyRunning) {
		t.Fatalf("error = %v, want client.ErrRuntimeAlreadyRunning", err)
	}
	stats := other.Stats(ctx)
	if !stats.LockBlocked {
		t.Fatalf("LockBlocked must explain why the client is not running: %+v", stats)
	}
	if !strings.Contains(stats.RuntimeError, "already running") {
		t.Fatalf("RuntimeError = %q", stats.RuntimeError)
	}
}

func TestAppAboutExcludesSecrets(t *testing.T) {
	app, dir := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	about := app.About(context.Background())
	encoded, err := json.Marshal(about)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "stored-token") {
		t.Fatalf("the about view leaked the token: %s", encoded)
	}
	if about.ConfigDir != dir || about.ClientConfigPath != filepath.Join(dir, ClientConfigFileName) {
		t.Fatalf("about = %+v", about)
	}
	if about.TunnelCount != 1 || !about.TokenPresent {
		t.Fatalf("about = %+v", about)
	}
	if about.WebsiteURL != "https://github.com/"+config.DefaultGitHubRepository {
		t.Fatalf("WebsiteURL = %q", about.WebsiteURL)
	}
	if about.InstanceID != "tray-test" {
		t.Fatalf("InstanceID = %q", about.InstanceID)
	}
	if about.System.GOOS == "" || about.System.Arch == "" {
		t.Fatalf("System = %+v", about.System)
	}
	if about.Version == "" {
		t.Fatal("Version must come from the build identity")
	}
}

func TestAppOpenWebsiteUsesTheShellHook(t *testing.T) {
	var opened []string
	app, _ := newTestApp(t, func(options *AppOptions) {
		options.OpenURL = func(target string) error {
			opened = append(opened, target)
			return nil
		}
	})
	if err := app.OpenWebsite(); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != "https://github.com/"+config.DefaultGitHubRepository {
		t.Fatalf("opened = %+v", opened)
	}
}

func TestAppPreferenceChangesAreObservable(t *testing.T) {
	var seen []Preferences
	app, _ := newTestApp(t, func(options *AppOptions) {
		options.OnPreferencesChanged = func(prefs Preferences) { seen = append(seen, prefs) }
	})
	off := false
	if _, err := app.SaveSettings(context.Background(), SettingsUpdate{MinimizeToTray: &off}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].MinimizeToTray {
		t.Fatalf("the shell was not told about the new close behaviour: %+v", seen)
	}
}

func TestAppStopRuntimeIsBounded(t *testing.T) {
	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	if err := app.StartRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.StopRuntime() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StopRuntime: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopRuntime did not return")
	}
}

func routingUpdateFrom(t *testing.T, app *App, token *string) RoutingUpdate {
	t.Helper()
	view := mustRouting(t, app)
	return RoutingUpdate{
		Mode:      view.Mode,
		ServerURL: view.ServerURL,
		Token:     token,
		Tunnels:   view.Tunnels,
	}
}

func mustRouting(t *testing.T, app *App) RoutingView {
	t.Helper()
	view, err := app.Routing(context.Background())
	if err != nil {
		t.Fatalf("Routing: %v", err)
	}
	return view
}

func storedToken(t *testing.T, app *App) string {
	t.Helper()
	settings, err := app.config.Load()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	return settings.Token
}
