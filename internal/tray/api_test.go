package tray

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

const testSecret = "unit-test-secret"

// testAssets stands in for the embedded webview bundle.
func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte(`<!doctype html><title>TunnelMesh</title>`)},
		"assets/app.js": &fstest.MapFile{Data: []byte(`console.log("tray")`)},
	}
}

func newTestAPI(t *testing.T, mutate func(*AppOptions)) (*APIServer, *App) {
	t.Helper()
	app, _ := newTestApp(t, mutate)
	api, err := NewAPIServer(APIOptions{App: app, Assets: testAssets(), Secret: testSecret})
	if err != nil {
		t.Fatalf("NewAPIServer: %v", err)
	}
	t.Cleanup(func() { _ = api.Shutdown(context.Background()) })
	return api, app
}

func call(t *testing.T, api *APIServer, method, path string, body any, mutate func(*http.Request)) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("X-Tray-Secret", testSecret)
	if mutate != nil {
		mutate(request)
	}
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, request)
	var parsed envelope
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("response is not the JSON envelope: %v (%s)", err, rec.Body.String())
		}
	}
	return rec, parsed
}

// envelope mirrors the project-wide {code,msg,data} response shape.
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func decode(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode data: %v (%s)", err, raw)
	}
}

func TestAPIRequiresTheSecret(t *testing.T) {
	api, _ := newTestAPI(t, nil)
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"absent", func(r *http.Request) { r.Header.Del("X-Tray-Secret") }},
		{"wrong header", func(r *http.Request) { r.Header.Set("X-Tray-Secret", "nope") }},
		{"wrong bearer", func(r *http.Request) {
			r.Header.Del("X-Tray-Secret")
			r.Header.Set("Authorization", "Bearer nope")
		}},
		{"wrong cookie", func(r *http.Request) {
			r.Header.Del("X-Tray-Secret")
			r.AddCookie(&http.Cookie{Name: secretCookieName, Value: "nope"})
		}},
		{"wrong query", func(r *http.Request) { r.Header.Del("X-Tray-Secret") }},
	} {
		recorder, parsed := call(t, api, http.MethodGet, "/api/settings", nil, test.mutate)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", test.name, recorder.Code)
		}
		if parsed.Code != http.StatusUnauthorized {
			t.Fatalf("%s: envelope code = %d", test.name, parsed.Code)
		}
	}

	recorder, parsed := call(t, api, http.MethodGet, "/api/settings?secret=nope", nil, func(r *http.Request) {
		r.Header.Del("X-Tray-Secret")
	})
	if recorder.Code != http.StatusUnauthorized || parsed.Msg == "" {
		t.Fatalf("query secret: status = %d msg = %q", recorder.Code, parsed.Msg)
	}
}

func TestAPIAcceptsEverySecretCarrier(t *testing.T) {
	api, _ := newTestAPI(t, nil)
	carriers := []func(*http.Request){
		func(r *http.Request) {},
		func(r *http.Request) {
			r.Header.Del("X-Tray-Secret")
			r.Header.Set("Authorization", "Bearer "+testSecret)
		},
		func(r *http.Request) {
			r.Header.Del("X-Tray-Secret")
			r.AddCookie(&http.Cookie{Name: secretCookieName, Value: testSecret})
		},
		func(r *http.Request) { r.Header.Del("X-Tray-Secret") },
	}
	paths := []string{"/api/settings", "/api/settings?secret=" + testSecret}
	for index, carrier := range carriers {
		path := paths[0]
		if index == len(carriers)-1 {
			path = paths[1]
		}
		recorder, parsed := call(t, api, http.MethodGet, path, nil, carrier)
		if recorder.Code != http.StatusOK {
			t.Fatalf("carrier %d: status = %d body = %s", index, recorder.Code, recorder.Body.String())
		}
		var view SettingsView
		decode(t, parsed.Data, &view)
		if view.Language != LanguageSystem {
			t.Fatalf("carrier %d: view = %+v", index, view)
		}
		// The document request has to hand the secret to same-origin subresources, which
		// is what the cookie is for.
		if recorder.Header().Get("Set-Cookie") == "" && index == len(carriers)-1 {
			t.Fatal("the query-secret request must set the cookie for later requests")
		}
	}
}

func TestAPIRejectsForeignOrigins(t *testing.T) {
	api, _ := newTestAPI(t, nil)
	recorder, _ := call(t, api, http.MethodGet, "/api/settings", nil, func(r *http.Request) {
		r.Header.Set("Origin", "http://evil.example")
	})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a cross-origin call", recorder.Code)
	}
}

func TestAPIServesTheUIWithoutTheSecret(t *testing.T) {
	api, _ := newTestAPI(t, nil)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	api.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "TunnelMesh") {
		t.Fatalf("index = %d %q", recorder.Code, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	recorder = httptest.NewRecorder()
	api.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "tray") {
		t.Fatalf("asset = %d %q", recorder.Code, recorder.Body.String())
	}
	// An unknown path is a 404 rather than the document: the tray UI has no client-side
	// routes, and a history fallback would hand HTML to a request expecting a module.
	request = httptest.NewRequest(http.MethodGet, "/nope.txt", nil)
	recorder = httptest.NewRecorder()
	api.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d", recorder.Code)
	}
}

func TestAPISettingsRoundTrip(t *testing.T) {
	api, app := newTestAPI(t, func(options *AppOptions) { options.Autostart = &fakeAutostart{supported: true} })
	off := false
	recorder, parsed := call(t, api, http.MethodPut, "/api/settings", SettingsUpdate{
		Language: LanguageZhCN, Theme: ThemeLight, MinimizeToTray: &off,
	}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var saved SettingsView
	decode(t, parsed.Data, &saved)
	if saved.Language != LanguageZhCN || saved.Theme != ThemeLight || saved.MinimizeToTray {
		t.Fatalf("saved = %+v", saved)
	}
	_, again := call(t, api, http.MethodGet, "/api/settings", nil, nil)
	var view SettingsView
	decode(t, again.Data, &view)
	if view.Language != LanguageZhCN {
		t.Fatalf("the save did not persist: %+v", view)
	}
	if prefs, err := app.Preferences(); err != nil || prefs.Theme != ThemeLight {
		t.Fatalf("preferences = %+v (%v)", prefs, err)
	}
}

func TestAPISettingsRejectsMalformedBody(t *testing.T) {
	api, _ := newTestAPI(t, nil)
	recorder, parsed := call(t, api, http.MethodPut, "/api/settings", nil, func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader("{not json"))
		r.ContentLength = int64(len("{not json"))
	})
	if recorder.Code != http.StatusBadRequest || parsed.Msg == "" {
		t.Fatalf("status = %d envelope = %+v", recorder.Code, parsed)
	}
}

func TestAPIRoutingRoundTripMasksTheToken(t *testing.T) {
	api, app := newTestAPI(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))

	recorder, parsed := call(t, api, http.MethodGet, "/api/routing", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "stored-token") {
		t.Fatalf("the token crossed the local API boundary: %s", recorder.Body.String())
	}
	var view RoutingView
	decode(t, parsed.Data, &view)
	if !view.TokenPresent || len(view.Tunnels) != 1 {
		t.Fatalf("view = %+v", view)
	}

	replacement := "api-token"
	update := RoutingUpdate{Mode: view.Mode, ServerURL: view.ServerURL, Token: &replacement, Tunnels: view.Tunnels}
	recorder, parsed = call(t, api, http.MethodPut, "/api/routing", update, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var result RoutingSaveResult
	decode(t, parsed.Data, &result)
	if storedToken(t, app) != "api-token" {
		t.Fatalf("stored token = %q", storedToken(t, app))
	}
	if strings.Contains(recorder.Body.String(), "api-token") {
		t.Fatalf("the response echoed the token back: %s", recorder.Body.String())
	}
	if !result.Routing.TokenPresent {
		t.Fatal("TokenPresent = false after a successful save")
	}
}

func TestAPIRoutingRejectsInvalidConfiguration(t *testing.T) {
	api, app := newTestAPI(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	view := mustRouting(t, app)
	update := RoutingUpdate{Mode: config.ModeCluster, ServerURL: view.ServerURL, Tunnels: view.Tunnels}
	recorder, parsed := call(t, api, http.MethodPut, "/api/routing", update, nil)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(parsed.Msg, "not usable") {
		t.Fatalf("msg = %q", parsed.Msg)
	}
}

func TestAPIAgentsProxiesTheServer(t *testing.T) {
	server := agentsEndpoint(t)
	api, app := newTestAPI(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	recorder, parsed := call(t, api, http.MethodGet, "/api/agents?serverUrl="+strings.Replace(server.URL, "http://", "ws://", 1), nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var agents []AgentRef
	decode(t, parsed.Data, &agents)
	if len(agents) != 1 || agents[0].ID != "agent-a" || !agents[0].Online {
		t.Fatalf("agents = %+v", agents)
	}
}

func TestAPIAgentsReportsTokenProblems(t *testing.T) {
	api, _ := newTestAPI(t, nil)
	rec, parsed := call(t, api, http.MethodGet, "/api/agents", nil, nil)
	// 400 rather than 401: a 401 means "this webview lost its secret" to the UI, while
	// here the *remote* credential is missing and the fix belongs in the routing tab.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(parsed.Msg, "token") {
		t.Fatalf("msg = %q, want it to name the missing token", parsed.Msg)
	}
}

func TestAPIValidateReturnsTheReport(t *testing.T) {
	server := agentsEndpoint(t)
	api, app := newTestAPI(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	view := mustRouting(t, app)
	update := RoutingUpdate{
		Mode: view.Mode, ServerURL: strings.Replace(server.URL, "http://", "ws://", 1) + "/ws/client",
		Tunnels: view.Tunnels,
	}
	recorder, parsed := call(t, api, http.MethodPost, "/api/validate", update, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var report ValidationReport
	decode(t, parsed.Data, &report)
	if !report.Valid {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Checks) == 0 {
		t.Fatal("the report must be itemised")
	}
}

func TestAPIValidateWithoutABodyChecksTheStoredConfiguration(t *testing.T) {
	api, app := newTestAPI(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	recorder, parsed := call(t, api, http.MethodPost, "/api/validate", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var report ValidationReport
	decode(t, parsed.Data, &report)
	if len(report.Checks) == 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.Valid {
		t.Fatalf("the stored configuration points at an unreachable server, so the report must not claim it is valid: %+v", report)
	}
}

func TestAPIStatsAndAbout(t *testing.T) {
	api, app := newTestAPI(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))

	_, parsed := call(t, api, http.MethodGet, "/api/stats", nil, nil)
	var stats StatsView
	decode(t, parsed.Data, &stats)
	if stats.Running || len(stats.Tunnels) != 1 {
		t.Fatalf("stats = %+v", stats)
	}

	recorder, parsed := call(t, api, http.MethodGet, "/api/about", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("about status = %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "stored-token") {
		t.Fatalf("the about response leaked the token: %s", recorder.Body.String())
	}
	var about AboutView
	decode(t, parsed.Data, &about)
	if about.WebsiteURL != WebsiteURL || about.License != License {
		t.Fatalf("about = %+v", about)
	}
}

func TestAPIActions(t *testing.T) {
	var opened, shown, quit int
	api, app := newTestAPI(t, func(options *AppOptions) {
		options.OpenURL = func(string) error { opened++; return nil }
	})
	writeClientConfig(t, app, loopbackTunnel(22))
	api.SetWindowHandlers(func() { shown++ }, func() { quit++ })

	for path, count := range map[string]*int{
		"/api/actions/open-website": &opened,
		"/api/actions/show-window":  &shown,
	} {
		recorder, _ := call(t, api, http.MethodPost, path, nil, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %s", path, recorder.Code, recorder.Body.String())
		}
		if *count != 1 {
			t.Fatalf("%s did not reach its handler (%d)", path, *count)
		}
	}

	recorder, _ := call(t, api, http.MethodPost, "/api/actions/start", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("start status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !app.RuntimeRunning() {
		t.Fatal("the start action did not start the client")
	}
	recorder, _ = call(t, api, http.MethodPost, "/api/actions/stop", nil, nil)
	if recorder.Code != http.StatusOK || app.RuntimeRunning() {
		t.Fatalf("stop status = %d running = %v", recorder.Code, app.RuntimeRunning())
	}

	// Quitting ends the process, so the handler only records the request; the shell
	// polls it. Asserting the flag keeps the test free of process games.
	recorder, _ = call(t, api, http.MethodPost, "/api/actions/quit", nil, nil)
	if recorder.Code != http.StatusOK || !app.QuitRequested() || quit != 1 {
		t.Fatalf("quit status = %d requested = %v handler = %d", recorder.Code, app.QuitRequested(), quit)
	}
}

func TestAPIStartReportsAContestedLock(t *testing.T) {
	app, _ := newTestApp(t, nil)
	writeClientConfig(t, app, loopbackTunnel(22))
	if err := app.StartRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	other, err := NewApp(AppOptions{ConfigDir: app.paths.ConfigDir, Runner: blockingRunner(), InstanceID: "second", Autostart: &fakeAutostart{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	second, err := NewAPIServer(APIOptions{App: other, Assets: testAssets(), Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Shutdown(context.Background()) }()

	recorder, parsed := call(t, second, http.MethodPost, "/api/actions/start", nil, nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(parsed.Msg, "already running") {
		t.Fatalf("msg = %q, want the mutual-exclusion explanation", parsed.Msg)
	}
}

func TestAPIMethodNotAllowedAndUnknownRoutes(t *testing.T) {
	api, _ := newTestAPI(t, nil)
	recorder, _ := call(t, api, http.MethodDelete, "/api/settings", nil, nil)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("delete status = %d", recorder.Code)
	}
	recorder, parsed := call(t, api, http.MethodGet, "/api/nope", nil, nil)
	if recorder.Code != http.StatusNotFound || parsed.Code != http.StatusNotFound {
		t.Fatalf("unknown route = %d %+v", recorder.Code, parsed)
	}
	recorder, _ = call(t, api, http.MethodGet, "/api/settings", nil, func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(strings.Repeat("a", 2<<20)))
		r.ContentLength = 2 << 20
	})
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413", recorder.Code)
	}
}

func TestAPIBindsLoopbackOnly(t *testing.T) {
	app, _ := newTestApp(t, nil)
	api, err := NewAPIServer(APIOptions{App: app, Assets: testAssets()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = api.Shutdown(context.Background()) }()
	if err := api.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	host, _, err := net.SplitHostPort(api.Addr())
	if err != nil {
		t.Fatalf("Addr = %q: %v", api.Addr(), err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("the local API must bind loopback, got host %q", host)
	}
	if len(api.Secret()) < 32 {
		t.Fatalf("generated secret is too short: %d bytes", len(api.Secret()))
	}
	if !strings.Contains(api.URL(), "secret=") {
		t.Fatalf("the initial URL must carry the secret: %q", api.URL())
	}

	// The served socket really answers, and really refuses a request without the secret.
	base := "http://" + api.Addr() + "/"
	response, err := http.Get(base + "api/settings")
	if err != nil {
		t.Fatalf("GET through the listener: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status through the listener = %d, want 401", response.StatusCode)
	}
	request, err := http.NewRequest(http.MethodGet, base+"api/settings", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Tray-Secret", api.Secret())
	authorized, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("authorized GET: %v", err)
	}
	defer func() { _ = authorized.Body.Close() }()
	if authorized.StatusCode != http.StatusOK {
		t.Fatalf("authorized status = %d, want 200", authorized.StatusCode)
	}
}

func TestGenerateSecretIsUnique(t *testing.T) {
	first, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two generated secrets collided")
	}
	if len(first) != len(second) {
		t.Fatalf("secret lengths differ: %d vs %d", len(first), len(second))
	}
}

func TestAPIWithoutAssetsServesAPlaceholder(t *testing.T) {
	app, _ := newTestApp(t, nil)
	api, err := NewAPIServer(APIOptions{App: app, Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = api.Shutdown(context.Background()) }()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	api.Handler().ServeHTTP(recorder, request)
	// A tray built without the webview bundle still has to say so instead of serving an
	// empty 200 that looks like a rendering bug.
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "not bundled") {
		t.Fatalf("placeholder = %d %q", recorder.Code, recorder.Body.String())
	}
}

// TestAPISettingsQuickPanelRoundTrip is the transport for the general tab radio.
func TestAPISettingsQuickPanelRoundTrip(t *testing.T) {
	api, _ := newTestAPI(t, nil)
	_, parsed := call(t, api, http.MethodGet, "/api/settings", nil, nil)
	var first SettingsView
	decode(t, parsed.Data, &first)
	if first.QuickPanel {
		t.Fatal("the API must report the quick panel as off by default")
	}

	on := true
	recorder, parsed := call(t, api, http.MethodPut, "/api/settings", SettingsUpdate{QuickPanel: &on}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var saved SettingsView
	decode(t, parsed.Data, &saved)
	if !saved.QuickPanel {
		t.Fatalf("saved view = %+v", saved)
	}

	recorder, parsed = call(t, api, http.MethodGet, "/api/settings", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d", recorder.Code)
	}
	var reread SettingsView
	decode(t, parsed.Data, &reread)
	if !reread.QuickPanel {
		t.Fatalf("re-read view = %+v, want the quick panel on", reread)
	}
}

// TestAPIPanelURL checks the address the quick panel's web view is pointed at. The hash
// is part of the contract with the bundle: it is how one embedded application renders two
// different windows without a second HTTP surface.
func TestAPIPanelURL(t *testing.T) {
	api, _ := newTestAPI(t, nil)
	if got := api.PanelURL(); got != api.URL()+"#/panel" {
		t.Fatalf("PanelURL() = %q, want %q", got, api.URL()+"#/panel")
	}
	if !strings.Contains(api.PanelURL(), "secret=") {
		t.Fatalf("PanelURL() = %q, want the launch secret so the panel can call the API", api.PanelURL())
	}
}
