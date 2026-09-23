package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingDispatcher stands in for ManagedRouteHandler. match decides whether
// the Host belongs to a managed route, which is the only fact the dispatch
// order depends on; the real handler resolves it from the route table.
type recordingDispatcher struct {
	match  func(host, path string) bool
	calls  []string
	served []string
	// unavailable reproduces a route table that cannot be loaded, where the real
	// dispatcher writes 503 and claims the request.
	unavailable bool
}

func (d *recordingDispatcher) TryServeHTTP(w http.ResponseWriter, r *http.Request) bool {
	d.calls = append(d.calls, r.URL.Path)
	if d.unavailable {
		http.Error(w, "managed routes unavailable", http.StatusServiceUnavailable)
		return true
	}
	if d.match == nil || !d.match(r.Host, r.URL.Path) {
		return false
	}
	d.served = append(d.served, r.URL.Path)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("upstream"))
	return true
}

type dispatchFixture struct {
	handler  http.Handler
	api      *markerHandler
	ws       *markerHandler
	clientWS *markerHandler
	health   *markerHandler
	webSSH   *markerHandler
	managed  *recordingDispatcher
}

type markerHandler struct {
	name  string
	calls []string
}

func (m *markerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.calls = append(m.calls, r.URL.Path)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(m.name))
}

func newDispatchFixture(match func(host, path string) bool) *dispatchFixture {
	f := &dispatchFixture{
		api:      &markerHandler{name: "api"},
		ws:       &markerHandler{name: "agent-ws"},
		clientWS: &markerHandler{name: "client-ws"},
		health:   &markerHandler{name: "health"},
		webSSH:   &markerHandler{name: "webssh"},
		managed:  &recordingDispatcher{match: match},
	}
	f.handler = NewWebHandlerWithManagedRoutes(f.api, f.ws, f.clientWS, f.health, f.webSSH, f.managed)
	return f
}

func (f *dispatchFixture) get(host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func (f *dispatchFixture) servedBy(rec *httptest.ResponseRecorder) string {
	return strings.TrimSpace(rec.Body.String())
}

// The reported defect: https://tm-6000d.claw.qihoo.net/api/skill/claw/cate
// returned the management API envelope
// {"code":404,"msg":"Not Found","data":{"error":"not found"}} because the /api/
// prefix was matched before the Host was ever consulted, while /skills on the
// same Host worked. A Host in the managed-route namespace must hand every path
// to the route.
func TestManagedNamespaceHostServesEveryPath(t *testing.T) {
	host := "tm-6000d.claw.qihoo.net"
	f := newDispatchFixture(func(h, _ string) bool { return h == host })
	for _, path := range []string{
		"/api/skill/claw/cate",
		"/api/v1/tokens",
		"/ws/chat",
		"/ws/agent",
		"/ws/client",
		"/health/live",
		"/metrics",
		"/skills",
	} {
		rec := f.get(host, path)
		if got := f.servedBy(rec); got != "upstream" {
			t.Fatalf("%s on a managed-route host was served by %q, want the upstream route", path, got)
		}
	}
}

// An explicit-domain route owns its Host too, so its /api/ and /ws/ paths must
// not be swallowed by the control plane.
func TestExplicitDomainRouteServesUpstreamAPIAndWSPaths(t *testing.T) {
	host := "git.example.com"
	f := newDispatchFixture(func(h, _ string) bool { return h == host })
	for _, path := range []string{"/api/v1/repos", "/ws/git", "/health", "/readyz"} {
		rec := f.get(host, path)
		if got := f.servedBy(rec); got != "upstream" {
			t.Fatalf("%s on an explicit-domain route was served by %q, want the upstream route", path, got)
		}
	}
}

// TunnelMesh's own protocol and operations endpoints stay reachable on a Host
// that is not in the managed-route namespace. Losing /ws/agent would stop every
// Agent behind that origin from reconnecting, and a health probe that must
// resolve routes first would let a route-table outage pull healthy nodes out of
// the load balancer.
func TestControlPlaneEndpointsReservedOnExplicitDomainRoute(t *testing.T) {
	host := "git.example.com"
	f := newDispatchFixture(func(h, _ string) bool { return h == host })
	cases := map[string]string{
		"/ws/agent":    "agent-ws",
		"/ws/client":   "client-ws",
		"/health/live": "health",
		"/metrics":     "health",
	}
	for path, want := range cases {
		rec := f.get(host, path)
		if got := f.servedBy(rec); got != want {
			t.Fatalf("%s was served by %q, want the control plane handler %q", path, got, want)
		}
	}
	for _, path := range []string{"/ws/agent", "/ws/client", "/health/live", "/metrics"} {
		for _, called := range f.managed.calls {
			if called == path {
				t.Fatalf("route resolution was consulted for reserved control-plane path %s", path)
			}
		}
	}
}

// The control-plane origin must behave exactly as before: reserved prefixes
// ahead of the SPA history fallback.
func TestControlPlaneHostKeepsReservedPathOrder(t *testing.T) {
	host := "tunnelmesh-admin.claw.qihoo.net"
	f := newDispatchFixture(func(h, _ string) bool { return strings.HasPrefix(h, "tm-") })
	cases := map[string]string{
		"/api/v1/tokens":  "api",
		"/health/live":    "health",
		"/metrics":        "health",
		"/ws/agent":       "agent-ws",
		"/ws/client":      "client-ws",
		"/ws/webssh/tick": "webssh",
	}
	for path, want := range cases {
		rec := f.get(host, path)
		if got := f.servedBy(rec); got != want {
			t.Fatalf("%s on the control-plane origin was served by %q, want %q", path, got, want)
		}
	}
	if rec := f.get(host, "/ws/unknown"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown /ws/ path returned %d, want 404", rec.Code)
	}
	if rec := f.get(host, "/some/spa/route"); rec.Code != http.StatusOK {
		t.Fatalf("SPA fallback returned %d, want 200", rec.Code)
	}
}

// A tm-* name that no route claims must keep the behaviour it has today rather
// than hard-failing, so the predicate alone never takes a Host away from the
// control plane.
func TestUnmatchedManagedNamespaceHostFallsThrough(t *testing.T) {
	host := "tm-unclaimed.claw.qihoo.net"
	f := newDispatchFixture(func(h, _ string) bool { return h == "tm-6000d.claw.qihoo.net" })
	if rec := f.get(host, "/api/v1/tokens"); f.servedBy(rec) != "api" {
		t.Fatalf("unmatched managed-namespace host did not fall through to the management API")
	}
	if rec := f.get(host, "/health/live"); f.servedBy(rec) != "health" {
		t.Fatalf("unmatched managed-namespace host lost its health endpoint")
	}
}

// Health and metrics on the control-plane origin must not depend on route
// resolution being available.
func TestControlPlaneHealthSurvivesRouteTableOutage(t *testing.T) {
	host := "tunnelmesh-admin.claw.qihoo.net"
	f := newDispatchFixture(func(h, _ string) bool { return true })
	f.managed.unavailable = true
	for _, path := range []string{"/health/live", "/metrics", "/ws/agent", "/ws/client"} {
		rec := f.get(host, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned %d during a route-table outage, want 200", path, rec.Code)
		}
	}
	if len(f.managed.calls) != 0 {
		t.Fatalf("route table was consulted %v for reserved control-plane paths", f.managed.calls)
	}
}
