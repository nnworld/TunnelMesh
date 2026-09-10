package server

import (
	"context"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func createManagedRouteFixture(t *testing.T, db *storage.DB, id, config string) {
	t.Helper()
	err := db.Tunnels().Create(context.Background(), storage.Tunnel{
		ID: id, AgentID: "agent-" + id, Protocol: "http", Domain: id + ".example.com",
		PathPrefix: "/", TargetHost: "10.0.0.8", TargetPort: 443, Status: "active",
		Config: config, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoadManagedRoutesDecodesUpstreamOptions(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:managed-route-options?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createManagedRouteFixture(t, db, "domain-route", `{"hostHeader":"service.internal.example.com","targetScheme":"https","tlsServerName":"service.internal.example.com"}`)

	routes, err := loadManagedRoutes(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %#v", routes)
	}
	got := routes[0]
	if got.HostHeader != "service.internal.example.com" || got.TargetScheme != "https" || got.TLSServerName != "service.internal.example.com" {
		t.Fatalf("upstream options = %#v", got)
	}
}

func TestLoadManagedRoutesRejectsInvalidConfig(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:managed-route-invalid-config?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createManagedRouteFixture(t, db, "bad-route", `{`)

	if _, err := loadManagedRoutes(context.Background(), db); err == nil {
		t.Fatal("expected invalid config error")
	}
}
