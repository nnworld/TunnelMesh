package server

import (
	"context"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// TestAuditRetentionSweeperDeletesOnlyExpiredRows pins the compliance contract: the
// window is measured from now, a row inside it is never touched, and a disabled
// policy removes nothing at all.
func TestAuditRetentionSweeperDeletesOnlyExpiredRows(t *testing.T) {
	db := newSweeperDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	seed := func(id string, age time.Duration) {
		if err := db.Audits().Create(ctx, storage.AuditLog{
			ID: id, Action: "agent_connection_closed", ResourceType: "agent", ResourceID: "agent-1",
			CreatedAt: now.Add(-age),
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	seed("audit-29d", 29*24*time.Hour)
	seed("audit-31d", 31*24*time.Hour)
	seed("audit-90d", 90*24*time.Hour)

	sweeper := NewAuditRetentionSweeper(db.Audits(), 30, time.Hour)
	if sweeper == nil {
		t.Fatal("NewAuditRetentionSweeper() = nil for a positive retention")
	}
	sweeper.SetClock(func() time.Time { return now })
	deleted, err := sweeper.SweepOnce(ctx)
	if err != nil {
		t.Fatalf("SweepOnce() error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("SweepOnce() deleted = %d, want the two rows older than 30 days", deleted)
	}
	page, err := db.Audits().List(ctx, storage.AuditFilter{}, "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "audit-29d" {
		t.Fatalf("survivors = %+v, want only audit-29d", page.Items)
	}
	// A second pass must be a no-op, which is what lets every cluster node run its
	// own sweeper without a distributed lock.
	if again, err := sweeper.SweepOnce(ctx); err != nil || again != 0 {
		t.Fatalf("second SweepOnce() = (%d, %v), want (0, nil)", again, err)
	}
}

// TestAuditRetentionSweeperRunsInBatches proves the batch bound is real: a table that
// has never been pruned must be drained in several short transactions, because a
// single mass delete on MySQL 5.6 holds InnoDB long enough to stall the metadata
// path that is still writing audit rows.
func TestAuditRetentionSweeperRunsInBatches(t *testing.T) {
	db := newSweeperDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := db.Audits().Create(ctx, storage.AuditLog{
			ID: "audit-" + string(rune('a'+i)), Action: "old", ResourceType: "agent",
			CreatedAt: now.Add(-40 * 24 * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	sweeper := NewAuditRetentionSweeper(db.Audits(), 30, time.Hour)
	sweeper.SetClock(func() time.Time { return now })
	sweeper.SetBatchSize(2)
	batches := 0
	sweeper.SetPurgeObserver(func(deleted int) { batches++ })
	deleted, err := sweeper.SweepOnce(ctx)
	if err != nil {
		t.Fatalf("SweepOnce() error = %v", err)
	}
	if deleted != 5 {
		t.Fatalf("SweepOnce() deleted = %d, want all five rows", deleted)
	}
	if batches < 3 {
		t.Fatalf("purge calls = %d, want at least 3 batches of 2 for 5 rows", batches)
	}
}

// TestAuditRetentionSweeperDisabledByDefault is the whole point of the zero default:
// an upgrade must never start deleting evidence nobody asked it to remove.
func TestAuditRetentionSweeperDisabledByDefault(t *testing.T) {
	db := newSweeperDB(t)
	if sweeper := NewAuditRetentionSweeper(db.Audits(), 0, time.Hour); sweeper != nil {
		t.Fatalf("NewAuditRetentionSweeper() = %p for a zero retention, want nil", sweeper)
	}
	if sweeper := NewAuditRetentionSweeper(db.Audits(), -1, time.Hour); sweeper != nil {
		t.Fatalf("NewAuditRetentionSweeper() = %p for a negative retention, want nil", sweeper)
	}
	if err := (*AuditRetentionSweeper)(nil).Run(context.Background()); err == nil {
		t.Fatal("Run() on a disabled sweeper must report it is closed")
	}
}

// TestNewServerRuntimeStartsAuditRetentionOnlyWhenConfigured is the wiring check:
// a positive retention_days must produce a running sweeper, and Close must stop it,
// or the documented policy is decoration.
func TestNewServerRuntimeStartsAuditRetentionOnlyWhenConfigured(t *testing.T) {
	db := newSweeperDB(t)
	ctx := context.Background()
	if err := db.Audits().Create(ctx, storage.AuditLog{
		ID: "audit-expired", Action: "old", ResourceType: "agent",
		CreatedAt: time.Now().UTC().Add(-400 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	disabled, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.auditRetentionSweeper != nil {
		t.Fatal("the default runtime must not delete audit history")
	}
	disabled.Close()

	enabled, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{Audit: config.ServerAuditConfig{RetentionDays: 30}})
	if err != nil {
		t.Fatal(err)
	}
	if enabled.auditRetentionSweeper == nil {
		t.Fatal("a positive retention_days must start the sweeper")
	}
	if got := enabled.auditRetentionSweeper.retention; got != 30*24*time.Hour {
		t.Fatalf("sweeper retention = %v, want the configured 30 days", got)
	}
	// The startup sweep is a goroutine, so counting rows here would race it: whether
	// it already drained the row or not, an explicit pass must leave nothing expired,
	// and Close must not hang. That is the wiring contract; row counts belong to the
	// sweeper's own tests above.
	enabled.Close()
	if deleted, err := enabled.auditRetentionSweeper.SweepOnce(ctx); err != nil || deleted > 1 {
		t.Fatalf("SweepOnce() = (%d, %v), want at most the one expired row", deleted, err)
	}
	page, err := db.Audits().List(ctx, storage.AuditFilter{}, "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("survivors = %+v, want the expired row removed by the enabled sweeper", page.Items)
	}
}
