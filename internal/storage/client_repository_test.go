package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
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

func TestSchemaVersionIs16(t *testing.T) {
	if SchemaVersion != 16 {
		t.Fatalf("SchemaVersion = %d, want 16", SchemaVersion)
	}
}

// TestClientConnectionLeaseStoresFullInt64Epoch pins the fencing-token
// contract: the Server derives connection_epoch from 8 random bytes, so the
// column must round-trip values beyond the signed 32-bit range. When MySQL
// stored this column as INTEGER the value was clamped to 2147483647 and every
// epoch-predicated write silently matched zero rows, which expired the lease
// and left the client observability page reporting a disconnected client.
func TestClientConnectionLeaseStoresFullInt64Epoch(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	const epoch = int64(math.MaxInt32) + 4242
	createClientInstance(t, db, "client-instance-epoch", "owner-1", "client-epoch-1", now)
	lease := ClientConnectionLease{
		ConnectionID: "connection-epoch", ClientInstanceID: "client-instance-epoch",
		TokenID: "token-epoch", OwnerUserID: "owner-1", ServerNodeID: "server-1",
		ConnectionEpoch: epoch, ActiveStreams: 3, AcquiredAt: now,
		ExpiresAt: now.Add(time.Minute), UpdatedAt: now,
	}
	if _, err := db.ClientConnections().Register(ctx, lease); err != nil {
		t.Fatal(err)
	}
	stored, err := db.ClientConnections().Get(ctx, lease.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConnectionEpoch != epoch {
		t.Fatalf("stored connection_epoch = %d, want %d (column truncated a 64-bit fencing token)", stored.ConnectionEpoch, epoch)
	}
	if err := db.ClientConnections().Renew(ctx, lease.ConnectionID, epoch, 90*time.Second); err != nil {
		t.Fatalf("renew with the authoritative epoch: %v", err)
	}
	lease.ActiveStreams = 5
	if err := db.ClientConnections().UpdateStats(ctx, lease); err != nil {
		t.Fatalf("update stats with the authoritative epoch: %v", err)
	}
	if err := db.ClientConnections().Release(ctx, lease.ConnectionID, epoch); err != nil {
		t.Fatalf("release with the authoritative epoch: %v", err)
	}
	if _, err := db.ClientConnections().Get(ctx, lease.ConnectionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("lease after release = %v, want sql.ErrNoRows", err)
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

// putClientInstance stores an instance with an explicit metadata document so a
// test can model "CLIENT_HELLO seen" (metadata carries instance_id) against
// "never reported" (the literal {} written by RegisterLegacy).
func putClientInstance(t *testing.T, db *DB, id, ownerUserID, instanceID, metadata string, stale bool, now, expiresAt time.Time) {
	t.Helper()
	instance := ClientInstance{
		ID: id, OwnerUserID: ownerUserID, InstanceID: instanceID, Metadata: metadata,
		Capabilities: `["client_metadata.v1"]`, ReportedAt: now, LastSeenAt: now,
		ExpiresAt: &expiresAt, Stale: stale, UpdatedAt: now,
	}
	if _, err := db.ClientInstances().Upsert(context.Background(), instance); err != nil {
		t.Fatal(err)
	}
}

func putClientLease(t *testing.T, db *DB, connectionID, clientInstanceID, ownerUserID string, activeStreams int64, expiresAt, now time.Time) {
	t.Helper()
	lease := ClientConnectionLease{
		ConnectionID: connectionID, ClientInstanceID: clientInstanceID, TokenID: "token-" + connectionID,
		OwnerUserID: ownerUserID, ServerNodeID: "server-1", ConnectionEpoch: 1, ActiveStreams: activeStreams,
		AcquiredAt: now.Add(-time.Hour), ExpiresAt: expiresAt, UpdatedAt: now,
	}
	if _, err := db.ClientConnections().Register(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
}

func TestClientInstanceUpsertNormalizesNullCapabilities(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	expires := now.Add(time.Minute)
	// The Client protocol field is `capabilities,omitempty`, so a client that
	// never advertises capabilities marshals to JSON null, not "[]".
	for i, raw := range []string{"null", ""} {
		instanceID := fmt.Sprintf("client-normalize-%d", i)
		if _, err := db.ClientInstances().Upsert(ctx, ClientInstance{
			OwnerUserID: "owner-1", InstanceID: instanceID, Metadata: `{"instance_id":"` + instanceID + `"}`,
			Capabilities: raw, ReportedAt: now, LastSeenAt: now, ExpiresAt: &expires, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		got, err := db.ClientInstances().GetByOwnerAndInstance(ctx, "owner-1", instanceID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Capabilities != "[]" {
			t.Fatalf("capabilities for %q = %q, want []", raw, got.Capabilities)
		}
	}
}

func TestClientInstanceSummaryMatchesFilter(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	live := now.Add(time.Minute)
	dead := now.Add(-time.Minute)
	putClientInstance(t, db, "ci-reported", "owner-1", "client-reported", `{"instance_id":"client-reported"}`, false, now, live)
	putClientInstance(t, db, "ci-unreported", "owner-1", "legacy-connection-1", `{}`, false, now, live)
	putClientInstance(t, db, "ci-ghost", "owner-1", "legacy-connection-2", `{}`, true, dead, dead)
	putClientInstance(t, db, "ci-expired", "owner-2", "client-other-owner", `{"instance_id":"client-other-owner"}`, true, dead, dead)
	putClientLease(t, db, "l-1", "ci-reported", "owner-1", 2, live, now)
	putClientLease(t, db, "l-2", "ci-unreported", "owner-1", 1, live, now)
	putClientLease(t, db, "l-3", "ci-ghost", "owner-1", 0, dead, now)

	summary, err := db.ClientInstances().Summarize(ctx, ClientInstanceFilter{OwnerUserID: "owner-1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := ClientInstanceSummary{Total: 3, Online: 2, ActiveConnections: 2, ActiveStreams: 3, MetadataUnavailable: 2}
	if summary != want {
		t.Fatalf("owner summary = %+v, want %+v", summary, want)
	}
	reported, err := db.ClientInstances().Summarize(ctx, ClientInstanceFilter{OwnerUserID: "owner-1", MetadataState: "reported"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if reported != (ClientInstanceSummary{Total: 1, Online: 1, ActiveConnections: 1, ActiveStreams: 2}) {
		t.Fatalf("reported summary = %+v", reported)
	}
	expired, err := db.ClientInstances().Summarize(ctx, ClientInstanceFilter{MetadataState: "expired"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if expired != (ClientInstanceSummary{Total: 1, MetadataStale: 1}) {
		t.Fatalf("expired summary = %+v", expired)
	}
	empty, err := db.ClientInstances().Summarize(ctx, ClientInstanceFilter{OwnerUserID: "owner-absent"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.IsZero() {
		t.Fatalf("empty summary = %+v, want zero", empty)
	}
}

func TestClientInstanceStatusFilterIsPresenceOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	live := now.Add(time.Minute)
	dead := now.Add(-time.Minute)
	putClientInstance(t, db, "ci-online", "owner-1", "client-online", `{"instance_id":"client-online"}`, false, now, live)
	putClientInstance(t, db, "ci-offline-stale", "owner-1", "client-offline", `{"instance_id":"client-offline"}`, true, dead, dead)
	// Presence follows the physical lease only: the stale row has no lease, so
	// it must read offline instead of "metadata expired".
	putClientLease(t, db, "l-online", "ci-online", "owner-1", 0, live, now)
	for _, tc := range []struct {
		status string
		want   int
	}{{"online", 1}, {"offline", 1}} {
		page, err := db.ClientInstances().List(ctx, ClientInstanceFilter{OwnerUserID: "owner-1", Status: tc.status}, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != tc.want {
			t.Fatalf("status %q matched %d rows, want %d", tc.status, len(page.Items), tc.want)
		}
	}
}

func TestClientInstanceDeleteUnreportedKeepsReportedRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	expires := now.Add(time.Minute)
	putClientInstance(t, db, "ci-legacy", "owner-1", "legacy-connection-9", `{}`, false, now, expires)
	putClientInstance(t, db, "ci-hello", "owner-1", "client-hello", `{"instance_id":"client-hello"}`, false, now, expires)
	if err := db.ClientInstances().DeleteUnreported(ctx, "ci-legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClientInstances().Get(ctx, "ci-legacy"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("legacy row error = %v, want ErrNoRows", err)
	}
	// A row that gained a CLIENT_HELLO must never be dropped by a late Release.
	if err := db.ClientInstances().DeleteUnreported(ctx, "ci-hello"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("reported row delete error = %v, want ErrNoRows", err)
	}
	if _, err := db.ClientInstances().Get(ctx, "ci-hello"); err != nil {
		t.Fatalf("reported row was deleted: %v", err)
	}
}

func TestClientInstancePurgeUnreportedOrphans(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	live := now.Add(time.Minute)
	dead := now.Add(-2 * time.Minute)
	putClientInstance(t, db, "ci-alive", "owner-1", "legacy-connection-1", `{}`, false, now, live)
	putClientInstance(t, db, "ci-orphan", "owner-1", "legacy-connection-2", `{}`, true, dead, dead)
	putClientInstance(t, db, "ci-reported", "owner-1", "client-reported", `{"instance_id":"client-reported"}`, true, dead, dead)
	putClientLease(t, db, "l-alive", "ci-alive", "owner-1", 0, live, now)
	purged, err := db.ClientInstances().PurgeUnreported(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d, want 1", purged)
	}
	for id, present := range map[string]bool{"ci-alive": true, "ci-orphan": false, "ci-reported": true} {
		_, err := db.ClientInstances().Get(ctx, id)
		if present != (err == nil) {
			t.Fatalf("row %s presence = %v (err %v), want %v", id, err == nil, err, present)
		}
	}
}
