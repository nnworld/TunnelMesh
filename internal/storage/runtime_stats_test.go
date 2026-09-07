package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuntimeStatsRepositoryAppendListCursorAndRetention(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	ctx := context.Background()
	lease, err := db.Leases().Acquire(ctx, AgentLease{AgentID: "agent-1", NodeID: "node-1", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	repo := db.RuntimeStats()
	base := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := repo.Append(ctx, AgentRuntimeStats{ID: "stat-" + string(rune('a'+i)), AgentID: "agent-1", NodeID: lease.NodeID, Epoch: lease.Epoch, WindowStart: base.Add(time.Duration(i) * time.Minute), WindowEnd: base.Add(time.Duration(i+1) * time.Minute), Connections: int64(i + 1), BytesIn: int64(i * 10)}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := repo.ListRange(ctx, "agent-1", base, base.Add(10*time.Minute), "", 2)
	if err != nil || len(page.Items) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	page2, err := repo.ListRange(ctx, "agent-1", base, base.Add(10*time.Minute), page.NextCursor, 2)
	if err != nil || len(page2.Items) != 1 || page2.Items[0].WindowStart.Before(page.Items[1].WindowStart) {
		t.Fatalf("page2=%+v err=%v", page2, err)
	}
	if err := repo.DeleteBefore(ctx, "agent-1", base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	remaining, err := repo.ListRange(ctx, "agent-1", base, base.Add(10*time.Minute), "", 10)
	if err != nil || len(remaining.Items) != 1 {
		t.Fatalf("remaining=%+v err=%v", remaining, err)
	}
}

func TestRuntimeStatsRepositoryRejectsStaleEpoch(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	ctx := context.Background()
	lease, err := db.Leases().Acquire(ctx, AgentLease{AgentID: "agent-2", NodeID: "node-1", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Leases().Release(ctx, lease.AgentID, lease.Epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Leases().Acquire(ctx, AgentLease{AgentID: "agent-2", NodeID: "node-2", TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	err = db.RuntimeStats().Append(ctx, AgentRuntimeStats{AgentID: "agent-2", NodeID: lease.NodeID, Epoch: lease.Epoch, WindowStart: time.Now().UTC()})
	if !errors.Is(err, ErrRuntimeStatsStaleEpoch) {
		t.Fatalf("err=%v, want ErrRuntimeStatsStaleEpoch", err)
	}
}

func TestProbeResultRepositoryBoundsErrorClassAndPaginates(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	ctx := context.Background()
	lease, err := db.Leases().Acquire(ctx, AgentLease{AgentID: "agent-3", NodeID: "node-1", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	repo := db.ProbeResults()
	for i := 0; i < 2; i++ {
		if err := repo.Create(ctx, AgentProbeResult{ProbeID: "probe-" + string(rune('a'+i)), AgentID: "agent-3", NodeID: lease.NodeID, Epoch: lease.Epoch, Kind: "tcp", Result: "failure", ErrorClass: "dns_timeout", Duration: 1200 * time.Microsecond, ObservedAt: time.Now().UTC().Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := repo.ListByAgent(ctx, "agent-3", "", 1)
	if err != nil || len(page.Items) != 1 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if err := repo.Create(ctx, AgentProbeResult{ProbeID: "bad", AgentID: "agent-3", ErrorClass: "raw target response body leaked"}); err == nil {
		t.Fatal("expected bounded error class rejection")
	}
}
