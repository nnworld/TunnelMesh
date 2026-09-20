package storage

import (
	"context"
	"testing"
	"time"
)

func TestClientInstanceRepositoryUpsertIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	instance := ClientInstance{
		ID: "client-instance-1", OwnerUserID: "owner-1", InstanceID: "client-0123456789abcdef",
		Metadata: `{"version":"v1.2.3"}`, Capabilities: `["client_metadata.v1"]`,
		ReportedAt: now, LastSeenAt: now, ExpiresAt: &expires, UpdatedAt: now,
	}
	persisted, err := db.ClientInstances().Upsert(ctx, instance)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ID != instance.ID {
		t.Fatalf("persisted ID = %q, want %q", persisted.ID, instance.ID)
	}
	instance.Metadata = `{"version":"v1.2.4"}`
	if _, err := db.ClientInstances().Upsert(ctx, instance); err != nil {
		t.Fatal(err)
	}
	got, err := db.ClientInstances().GetByOwnerAndInstance(ctx, "owner-1", "client-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "client-instance-1" || got.Metadata != `{"version":"v1.2.4"}` {
		t.Fatalf("upsert result = %#v", got)
	}
}

func TestClientConnectionRepositoryFiltersOwnerBeforePagination(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	createClientConnectionAt(t, db, "connection-1", "owner-1", "client-instance-1", now)
	createClientConnectionAt(t, db, "connection-2", "owner-2", "client-instance-2", now)
	page, err := db.ClientConnections().List(ctx, ClientConnectionFilter{OwnerUserID: "owner-1"}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].OwnerUserID != "owner-1" {
		t.Fatalf("owner page = %#v", page.Items)
	}
}

func TestClientInstanceRepositoryMarksExpiredMetadata(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	createClientInstance(t, db, "client-instance-1", "owner-1", "client-local-1", now.Add(-time.Minute))
	changed, err := db.ClientInstances().MarkExpired(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
	got, err := db.ClientInstances().Get(context.Background(), "client-instance-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stale {
		t.Fatal("expired metadata was not marked stale")
	}
}

func TestClientInstanceRepositoryFiltersExactAgentID(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	instances := []ClientInstance{
		{
			ID: "client-instance-agent-1", OwnerUserID: "owner-1", InstanceID: "client-agent-1",
			Metadata: `{"agentIds":["agent-1"]}`, Capabilities: `["client_metadata.v1"]`,
			ReportedAt: now, LastSeenAt: now, ExpiresAt: &expires, UpdatedAt: now,
		},
		{
			ID: "client-instance-agent-12", OwnerUserID: "owner-1", InstanceID: "client-agent-12",
			Metadata: `{"agentIds":["agent-12"]}`, Capabilities: `["client_metadata.v1"]`,
			ReportedAt: now, LastSeenAt: now, ExpiresAt: &expires, UpdatedAt: now,
		},
	}
	for _, instance := range instances {
		if _, err := db.ClientInstances().Upsert(context.Background(), instance); err != nil {
			t.Fatal(err)
		}
	}

	page, err := db.ClientInstances().List(context.Background(), ClientInstanceFilter{AgentID: "agent-1"}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "client-instance-agent-1" {
		t.Fatalf("agent ID page = %#v", page.Items)
	}
}

func TestClientConnectionRegisterDefaultsTimestampsAndHealth(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	createClientInstance(t, db, "client-instance-defaults", "owner-1", "client-local-1", now)
	lease, err := db.ClientConnections().Register(context.Background(), ClientConnectionLease{
		ConnectionID: "connection-defaults", ClientInstanceID: "client-instance-defaults",
		TokenID: "token-1", OwnerUserID: "owner-1", ServerNodeID: "server-1",
		ConnectionEpoch: 1, AcquiredAt: now, ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.UpdatedAt.IsZero() || lease.HealthScore != 100 {
		t.Fatalf("lease defaults = %#v", lease)
	}
}

func TestSchemaVersionIs15(t *testing.T) {
	if SchemaVersion != 15 {
		t.Fatalf("SchemaVersion = %d, want 15", SchemaVersion)
	}
}

func createClientConnection(t *testing.T, db *DB, connectionID, ownerUserID, clientInstanceID string) {
	t.Helper()
	createClientConnectionAt(t, db, connectionID, ownerUserID, clientInstanceID, time.Now().UTC())
}

func createClientConnectionAt(t *testing.T, db *DB, connectionID, ownerUserID, clientInstanceID string, now time.Time) {
	t.Helper()
	createClientInstance(t, db, clientInstanceID, ownerUserID, "instance-"+clientInstanceID, now)
	lease := ClientConnectionLease{
		ConnectionID: connectionID, ClientInstanceID: clientInstanceID, TokenID: "token-" + connectionID,
		OwnerUserID: ownerUserID, ServerNodeID: "server-1", ConnectionEpoch: 1,
		AcquiredAt: now, ExpiresAt: now.Add(time.Minute), UpdatedAt: now,
	}
	if _, err := db.ClientConnections().Register(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
}

func createClientInstance(t *testing.T, db *DB, id, ownerUserID, instanceID string, now time.Time) {
	t.Helper()
	expires := now.Add(time.Minute)
	instance := ClientInstance{
		ID: id, OwnerUserID: ownerUserID, InstanceID: instanceID, Metadata: `{}`,
		Capabilities: `["client_metadata.v1"]`, ReportedAt: now, LastSeenAt: now,
		ExpiresAt: &expires, UpdatedAt: now,
	}
	if _, err := db.ClientInstances().Upsert(context.Background(), instance); err != nil {
		t.Fatal(err)
	}
}
