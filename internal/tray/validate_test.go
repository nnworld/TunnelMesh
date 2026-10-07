package tray

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// agentsServer serves the client-scoped Agent list with a fixed status, so a test can
// drive each classification the interface has to distinguish.
func agentsServer(t *testing.T, status int, agents []AgentRef) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/client/agents" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			if status == http.StatusServiceUnavailable {
				_, _ = w.Write([]byte(`{"code":503,"msg":"client_token_validator_unavailable"}`))
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "msg": "OK",
			"data": map[string]any{"items": agents, "nextCursor": "", "hasMore": false},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func validSettings(serverURL string) ClientSettings {
	return ClientSettings{
		Mode:      config.ModeLocal,
		ServerURL: serverURL,
		Token:     "client-secret",
		Tunnels: []config.TunnelConfig{{
			Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:0", AgentID: "agent-a",
			TargetHost: "10.0.0.8", TargetPort: 80,
		}},
	}
}

func newTestValidator(t *testing.T, _ *httptest.Server) *Validator {
	t.Helper()
	store := NewConfigStore(filepath.Join(t.TempDir(), ClientConfigFileName))
	validator := NewValidator(NewServerClient(0), store)
	validator.SetBindProbes(true)
	return validator
}

// checkByID returns one check from a report, failing the test when it is absent: a
// missing check is a silent gap in the report the interface renders.
func checkByID(t *testing.T, report ValidationReport, id string) CheckResult {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("report has no %q check: %+v", id, report.Checks)
	return CheckResult{}
}

func TestValidateAcceptsAUsableConfiguration(t *testing.T) {
	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	report := newTestValidator(t, server).Validate(context.Background(), validSettings(testServerURL(t, server)))

	if !report.Valid {
		t.Fatalf("report is invalid: %+v", report.Checks)
	}
	for _, id := range []string{CheckServerURLFormat, CheckServerReachable, CheckTokenPresent, CheckTokenValid, CheckTunnelsPresent, CheckTunnelProtocol, CheckTunnelAgent, CheckTunnelTarget, CheckConfigValid} {
		if got := checkByID(t, report, id); got.Status != StatusPassed {
			t.Fatalf("check %s = %+v, want passed", id, got)
		}
	}
	if len(report.Agents) != 1 || report.Agents[0].ID != "agent-a" {
		t.Fatalf("report agents = %+v, want the picker to be refreshed as a side effect", report.Agents)
	}
}

func TestValidateRejectsMalformedServerURL(t *testing.T) {
	report := newTestValidator(t, agentsServer(t, http.StatusOK, nil)).
		Validate(context.Background(), validSettings("https://server.example/ws/client"))

	if report.Valid {
		t.Fatal("an https server URL must not validate")
	}
	if got := checkByID(t, report, CheckServerURLFormat); got.Status != StatusFailed {
		t.Fatalf("serverUrl.format = %+v, want failed", got)
	}
	// Nothing downstream can run without a usable origin.
	for _, id := range []string{CheckServerReachable, CheckTokenValid} {
		if got := checkByID(t, report, id); got.Status != StatusSkipped {
			t.Fatalf("%s = %+v, want skipped", id, got)
		}
	}
}

func TestValidateReportsUnreachableServer(t *testing.T) {
	settings := validSettings("ws://127.0.0.1:1/ws/client")
	report := newTestValidator(t, agentsServer(t, http.StatusOK, nil)).Validate(context.Background(), settings)

	if report.Valid {
		t.Fatal("an unreachable server must not validate")
	}
	if got := checkByID(t, report, CheckServerReachable); got.Status != StatusFailed {
		t.Fatalf("serverUrl.reachable = %+v, want failed", got)
	}
}

func TestValidateReportsRejectedToken(t *testing.T) {
	server := agentsServer(t, http.StatusUnauthorized, nil)
	report := newTestValidator(t, server).Validate(context.Background(), validSettings(testServerURL(t, server)))

	if report.Valid {
		t.Fatal("a rejected token must not validate")
	}
	check := checkByID(t, report, CheckTokenValid)
	if check.Status != StatusFailed || !strings.Contains(check.Message, ErrInvalidToken.Error()) {
		t.Fatalf("token.valid = %+v, want a failure naming the rejected token", check)
	}
}

func TestValidateReportsServerWithoutEndpoint(t *testing.T) {
	server := agentsServer(t, http.StatusServiceUnavailable, nil)
	report := newTestValidator(t, server).Validate(context.Background(), validSettings(testServerURL(t, server)))

	check := checkByID(t, report, CheckAgentsAvailable)
	if check.Status != StatusFailed || !strings.Contains(check.Message, ErrServerTooOld.Error()) {
		t.Fatalf("agents.available = %+v, want a failure naming the missing endpoint", check)
	}
	// The token itself was never judged, so it must not be reported as invalid.
	if got := checkByID(t, report, CheckTokenValid); got.Status != StatusSkipped {
		t.Fatalf("token.valid = %+v, want skipped", got)
	}
	if report.Valid {
		t.Fatal("a Server that cannot list Agents cannot back the picker")
	}
}

func TestValidateRequiresATunnel(t *testing.T) {
	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	settings := validSettings(testServerURL(t, server))
	settings.Tunnels = nil
	report := newTestValidator(t, server).Validate(context.Background(), settings)

	if got := checkByID(t, report, CheckTunnelsPresent); got.Status != StatusFailed {
		t.Fatalf("tunnels.present = %+v, want failed", got)
	}
	if report.Valid {
		t.Fatal("a configuration with no tunnel cannot run")
	}
}

// TestValidateFlagsUnknownAgent is the check that only server data can provide: a
// syntactically perfect tunnel aimed at an Agent this token cannot reach.
func TestValidateFlagsUnknownAgent(t *testing.T) {
	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-other", Name: "other", Online: true}})
	report := newTestValidator(t, server).Validate(context.Background(), validSettings(testServerURL(t, server)))

	check := checkByID(t, report, CheckTunnelAgent)
	if check.Status != StatusFailed {
		t.Fatalf("tunnel.agent = %+v, want failed for an out-of-scope Agent", check)
	}
	if check.Index != 1 || check.Tunnel != "web" {
		t.Fatalf("tunnel.agent attribution = %+v, want tunnel 1 named web", check)
	}
}

// TestValidateWarnsAboutOfflineAgent keeps an offline Agent a warning rather than an
// error: the tunnel is correctly configured and will work once the Agent reconnects.
func TestValidateWarnsAboutOfflineAgent(t *testing.T) {
	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: false}})
	report := newTestValidator(t, server).Validate(context.Background(), validSettings(testServerURL(t, server)))

	if got := checkByID(t, report, CheckTunnelAgent); got.Status != StatusWarning {
		t.Fatalf("tunnel.agent = %+v, want a warning", got)
	}
	if !report.Valid {
		t.Fatal("an offline Agent must not make an otherwise valid configuration invalid")
	}
}

// TestValidateProbesListenAddress covers the only check that needs the live host: an
// address already held by another process cannot be bound.
func TestValidateProbesListenAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	settings := validSettings(testServerURL(t, server))
	settings.Tunnels[0].ListenAddr = listener.Addr().String()

	report := newTestValidator(t, server).Validate(context.Background(), settings)
	check := checkByID(t, report, CheckTunnelListen)
	if check.Status != StatusFailed || !strings.Contains(check.Message, "use") {
		t.Fatalf("tunnel.listen = %+v, want a failure explaining the address is taken", check)
	}
}

// TestValidateSkipsBindProbeWhileRunning prevents a false alarm: while the tray hosts
// the client, the tray itself holds every listener, so probing would always fail.
func TestValidateSkipsBindProbeWhileRunning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	settings := validSettings(testServerURL(t, server))
	settings.Tunnels[0].ListenAddr = listener.Addr().String()

	validator := newTestValidator(t, server)
	validator.SetBindProbes(false)
	report := validator.Validate(context.Background(), settings)
	if got := checkByID(t, report, CheckTunnelListen); got.Status != StatusSkipped {
		t.Fatalf("tunnel.listen = %+v, want skipped while the runtime holds the listeners", got)
	}
}

func TestValidateRequiresProxyCredentials(t *testing.T) {
	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	settings := validSettings(testServerURL(t, server))
	settings.Tunnels[0] = config.TunnelConfig{
		Name: "socks", Protocol: "socks5", ListenAddr: "127.0.0.1:0", AgentID: "agent-a", AuthMode: "password",
	}
	t.Setenv("TUNNELMESH_SOCKS5_USERNAME", "")
	t.Setenv("TUNNELMESH_SOCKS5_PASSWORD", "")

	report := newTestValidator(t, server).Validate(context.Background(), settings)
	check := checkByID(t, report, CheckTunnelCredentials)
	if check.Status != StatusFailed || !strings.Contains(check.Message, "TUNNELMESH_SOCKS5_PASSWORD") {
		t.Fatalf("tunnel.credentials = %+v, want a failure naming the missing variable", check)
	}
}

// TestValidateSurfacesModeStorageCoupling is why validation runs through the real
// loader: cluster mode without MySQL is rejected by config.Validate, and the interface
// has to say so before launching a client that cannot start.
func TestValidateSurfacesModeStorageCoupling(t *testing.T) {
	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	settings := validSettings(testServerURL(t, server))
	settings.Mode = config.ModeCluster

	report := newTestValidator(t, server).Validate(context.Background(), settings)
	check := checkByID(t, report, CheckConfigValid)
	if check.Status != StatusFailed || !strings.Contains(check.Message, "cluster mode requires mysql") {
		t.Fatalf("config.valid = %+v, want the storage coupling", check)
	}
	if report.Valid {
		t.Fatal("cluster mode without MySQL storage must not validate")
	}
}

func TestValidateReportsRejectedNonLoopbackListener(t *testing.T) {
	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	settings := validSettings(testServerURL(t, server))
	settings.Tunnels[0].ListenAddr = "0.0.0.0:0"

	report := newTestValidator(t, server).Validate(context.Background(), settings)
	check := checkByID(t, report, CheckConfigValid)
	if check.Status != StatusFailed || !strings.Contains(check.Message, "allow_remote") {
		t.Fatalf("config.valid = %+v, want the non-loopback guard", check)
	}
}

func TestValidationReportValidIgnoresWarnings(t *testing.T) {
	report := ValidationReport{Checks: []CheckResult{
		{ID: "a", Status: StatusPassed},
		{ID: "b", Status: StatusWarning},
		{ID: "c", Status: StatusSkipped},
	}}
	if !report.OK() {
		t.Fatal("warnings and skips must not invalidate a report")
	}
	report.Checks = append(report.Checks, CheckResult{ID: "d", Status: StatusFailed})
	if report.OK() {
		t.Fatal("one failed check must invalidate the report")
	}
	if errors.Is(nil, errors.New("x")) {
		t.Fatal("unreachable")
	}
}

// TestValidateRejectsUnusableAuthURL covers the one field the runtime only discovers at
// request time.
//
// A proxy tunnel with auth_url set asks that endpoint on every connection, so a value that
// is not an absolute http(s) URL makes the tunnel refuse everything at runtime. The forwarder
// builds the request with http.NewRequestWithContext, which is the rule the check mirrors.
func TestValidateRejectsUnusableAuthURL(t *testing.T) {
	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	settings := validSettings(testServerURL(t, server))
	settings.Tunnels[0] = config.TunnelConfig{
		Name: "socks", Protocol: "socks5", ListenAddr: "127.0.0.1:0", AgentID: "agent-a",
		AuthURL: "auth.example/verify",
	}

	report := newTestValidator(t, server).Validate(context.Background(), settings)
	check := checkByID(t, report, CheckTunnelAuthURL)
	if check.Status != StatusFailed {
		t.Fatalf("tunnel.authUrl = %+v, want a failure for a URL the forwarder cannot request", check)
	}
}

// TestValidateAcceptsAWellFormedAuthURL is the other half: a valid endpoint is not a problem,
// and reporting one would train the operator to ignore the report.
func TestValidateAcceptsAWellFormedAuthURL(t *testing.T) {
	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	settings := validSettings(testServerURL(t, server))
	settings.Tunnels[0] = config.TunnelConfig{
		Name: "socks", Protocol: "socks5", ListenAddr: "127.0.0.1:0", AgentID: "agent-a",
		AuthMode: "password", AuthURL: "https://auth.example/verify",
	}
	t.Setenv("TUNNELMESH_SOCKS5_USERNAME", "alice")
	t.Setenv("TUNNELMESH_SOCKS5_PASSWORD", "hunter2")

	report := newTestValidator(t, server).Validate(context.Background(), settings)
	if check := checkByID(t, report, CheckTunnelAuthURL); check.Status != StatusPassed {
		t.Fatalf("tunnel.authUrl = %+v, want passed", check)
	}
}

// TestValidateWarnsWhenAuthURLCannotApply is the silent-no-op case: only the proxy
// forwarders consult a remote validator, so the field on a raw tunnel is a mistake the
// operator has to be told about rather than one that quietly does nothing.
func TestValidateWarnsWhenAuthURLCannotApply(t *testing.T) {
	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	settings := validSettings(testServerURL(t, server))
	settings.Tunnels[0] = config.TunnelConfig{
		Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:0", AgentID: "agent-a",
		TargetHost: "10.0.0.8", TargetPort: 80, AuthURL: "https://auth.example/verify",
	}

	report := newTestValidator(t, server).Validate(context.Background(), settings)
	check := checkByID(t, report, CheckTunnelAuthURL)
	if check.Status != StatusWarning {
		t.Fatalf("tunnel.authUrl = %+v, want a warning that the protocol ignores it", check)
	}
}

// TestValidateStaysSilentWithoutAnAuthURL keeps the report honest: an empty auth_url means
// remote validation is off, which is the common case and not a finding.
func TestValidateStaysSilentWithoutAnAuthURL(t *testing.T) {
	server := agentsServer(t, http.StatusOK, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	report := newTestValidator(t, server).Validate(context.Background(), validSettings(testServerURL(t, server)))
	for _, check := range report.Checks {
		if check.ID == CheckTunnelAuthURL {
			t.Fatalf("report carries %q for a tunnel that sets no auth_url: %+v", CheckTunnelAuthURL, check)
		}
	}
}

// blockedHealthServer is the deployment shape a reverse proxy produces: /health/ready is
// refused with 403 by the proxy in front of the Server, while the API path answers.
func blockedHealthServer(t *testing.T, agents []AgentRef) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/ready" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "msg": "OK",
			"data": map[string]any{"items": agents, "nextCursor": "", "hasMore": false},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// TestValidateWarnsWhenTheHealthEndpointIsRefused keeps the checker honest about a proxy
// that blocks /health/ready: the origin answered, so the address is right and the checks
// that go through the API have to run rather than be skipped.
func TestValidateWarnsWhenTheHealthEndpointIsRefused(t *testing.T) {
	server := blockedHealthServer(t, []AgentRef{{ID: "agent-a", Name: "office", Online: true}})
	report := newTestValidator(t, server).Validate(context.Background(), validSettings(testServerURL(t, server)))

	check := checkByID(t, report, CheckServerReachable)
	if check.Status != StatusWarning {
		t.Fatalf("serverUrl.reachable = %+v, want a warning for an answered-but-refused probe", check)
	}
	if !strings.Contains(check.Message, "403") {
		t.Fatalf("serverUrl.reachable message = %q, want the refused status", check.Message)
	}
	if got := checkByID(t, report, CheckTokenValid); got.Status != StatusPassed {
		t.Fatalf("serverUrl.token = %+v, want the token check to run because the API answers", got)
	}
	if !report.Valid {
		t.Fatalf("a refused health endpoint must not invalidate a working configuration: %+v", report.Checks)
	}
}
