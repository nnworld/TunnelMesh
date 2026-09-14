package server

import (
	"context"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func createProxyRouteFixture(t *testing.T, db *storage.DB, id, domain, status, config string) {
	t.Helper()
	err := db.Tunnels().Create(context.Background(), storage.Tunnel{
		ID: id, AgentID: "agent-" + id, Protocol: storage.ProtocolHTTPProxy, Domain: domain,
		PathPrefix: "/", TargetHost: storage.ProxyTargetWildcard, TargetPort: 0, Status: status,
		Config: config, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoadProxyRoutesDecodesPolicyConfig(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:proxy-route-snapshot?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createProxyRouteFixture(t, db, "p1", "tp-demo.tm.example.com", "active",
		`{"authMode":"basic","credentialId":"c1","sourceCIDRs":["11.71.85.0/24"],"targetCIDRs":["10.10.0.0/16"],"targetPorts":[443],"allowPrivateTargets":false,"maxConcurrentTunnels":7,"description":"demo"}`)

	routes, err := loadProxyRoutes(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %#v", routes)
	}
	got := routes[0]
	if got.ID != "p1" || got.Domain != "tp-demo.tm.example.com" || got.Name != "demo" || got.AgentID != "agent-p1" {
		t.Fatalf("identity = %#v", got)
	}
	if got.AuthMode != "basic" || got.CredentialID != "c1" || got.AllowPrivateTargets || got.MaxConcurrentTunnels != 7 {
		t.Fatalf("policy = %#v", got)
	}
	if len(got.SourceCIDRs) != 1 || got.SourceCIDRs[0] != "11.71.85.0/24" || len(got.TargetCIDRs) != 1 || len(got.TargetPorts) != 1 {
		t.Fatalf("allowlists = %#v", got)
	}
}

func TestLoadProxyRoutesDefaultsAndInvalidConfig(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:proxy-route-defaults?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createProxyRouteFixture(t, db, "p2", "tp-plain.tm.example.com", "active", `{}`)
	routes, err := loadProxyRoutes(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if routes[0].AuthMode != "none" || !routes[0].AllowPrivateTargets || routes[0].MaxConcurrentTunnels != 0 {
		t.Fatalf("defaults = %#v", routes[0])
	}
	createProxyRouteFixture(t, db, "p3", "tp-bad.tm.example.com", "active", `{"authMode":`)
	if _, err := loadProxyRoutes(context.Background(), db); err == nil {
		t.Fatal("invalid config JSON must fail the snapshot")
	}
}

func TestLoadProxyRoutesSkipsIncompleteRows(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:proxy-route-incomplete?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// A row with no domain cannot be looked up by SNI, and a row with no agent
	// has no egress; both are dropped rather than stored as unusable routes.
	createProxyRouteFixture(t, db, "p-nodomain", "", "active", `{}`)
	createProxyRouteFixture(t, db, "p-good", "tp-good.tm.example.com", "active", `{}`)
	if err := db.Tunnels().Create(context.Background(), storage.Tunnel{
		ID: "p-noagent", Protocol: storage.ProtocolHTTPProxy, Domain: "tp-noagent.tm.example.com",
		PathPrefix: "/", TargetHost: storage.ProxyTargetWildcard, TargetPort: 0, Status: "active",
		Config: `{}`, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	routes, err := loadProxyRoutes(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].ID != "p-good" {
		t.Fatalf("routes = %#v", routes)
	}
}

func TestProxyRoutesStayOutOfManagedHTTPRoutes(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:proxy-route-isolation?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createManagedRouteFixture(t, db, "http-route", `{"targetScheme":"https"}`)
	// A proxy route with a valid target port must still not leak into the
	// reverse-proxy resolver: managedTunnelActive would accept it, so the
	// protocol skip is what keeps the wildcard domain from matching tp-*.
	err = db.Tunnels().Create(context.Background(), storage.Tunnel{
		ID: "leak", AgentID: "agent-leak", Protocol: storage.ProtocolHTTPProxy,
		Domain: "tp-leak.example.com", PathPrefix: "/", TargetHost: "10.0.0.9", TargetPort: 443,
		Status: "active", Config: `{}`, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := loadManagedRoutes(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].ID != "http-route" {
		t.Fatalf("managed HTTP routes = %#v", routes)
	}
}

func TestManagedRouteTableProxyRouteLookupAndTTL(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:proxy-route-table?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	table := NewManagedRouteTable(db, "apps.example.com", 20*time.Millisecond)
	if _, ok := table.ProxyRoute(context.Background(), "tp-demo.tm.example.com"); ok {
		t.Fatal("unknown route must not resolve")
	}
	createProxyRouteFixture(t, db, "p1", "tp-demo.tm.example.com", "active", `{}`)
	// The first lookup populated an (empty) snapshot, so the TTL has to expire
	// before the new row becomes visible. Without this wait the test would only
	// pass against a cache that never serves a fresh hit.
	time.Sleep(40 * time.Millisecond)
	route, ok := table.ProxyRoute(context.Background(), "TP-DEMO.tm.example.com")
	if !ok || route.ID != "p1" {
		t.Fatalf("lookup = %v %v", ok, route)
	}
	// Disabled routes stay in the snapshot so the handler can answer 403 with
	// the same shape as an unknown route.
	createProxyRouteFixture(t, db, "p9", "tp-off.tm.example.com", "disabled", `{}`)
	time.Sleep(40 * time.Millisecond)
	off, ok := table.ProxyRoute(context.Background(), "tp-off.tm.example.com")
	if !ok || off.Active() {
		t.Fatalf("disabled route = %v %v", ok, off)
	}
}

func TestManagedRouteTableProxyCacheDoesNotDisturbHTTPResolver(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:proxy-route-independent-ttl?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	table := NewManagedRouteTable(db, "apps.example.com", 20*time.Millisecond)

	createManagedRouteFixture(t, db, "http-route", `{}`)
	if _, err := table.Resolver(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Touching the proxy snapshot must not refresh the HTTP resolver's clock,
	// otherwise a newly added reverse-proxy route would stay invisible for an
	// extra TTL whenever proxy traffic happens to be flowing.
	createProxyRouteFixture(t, db, "p1", "tp-demo.tm.example.com", "active", `{}`)
	if _, ok := table.ProxyRoute(context.Background(), "tp-demo.tm.example.com"); !ok {
		t.Fatal("proxy route should resolve")
	}
	createManagedRouteFixture(t, db, "http-route-2", `{}`)
	time.Sleep(40 * time.Millisecond)
	resolver, err := table.Resolver(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolver == nil {
		t.Fatal("resolver must reload after its own TTL")
	}
	// And the reverse: an HTTP refresh must not pin the proxy snapshot stale.
	createProxyRouteFixture(t, db, "p2", "tp-second.tm.example.com", "active", `{}`)
	time.Sleep(40 * time.Millisecond)
	if _, err := table.Resolver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := table.ProxyRoute(context.Background(), "tp-second.tm.example.com"); !ok {
		t.Fatal("proxy snapshot must refresh on its own TTL")
	}
}
