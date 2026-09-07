package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestRuntimeMetadataRepositoryFencingAndIdempotentReplay(t *testing.T) {
	db := newTestDB(t)
	repo := db.Metadata()
	ctx := context.Background()
	base := AgentRuntimeMetadata{AgentID: "agent-1", NodeID: "node-a", Epoch: 3, Revision: 1, Metadata: `{"items":[{"name":"region","source":"env","value":"cn"}]}`, ReportedAt: time.Now().UTC()}
	if err := repo.Upsert(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := repo.Upsert(ctx, base); err != nil {
		t.Fatalf("equal revision replay should be idempotent: %v", err)
	}
	if err := repo.Upsert(ctx, AgentRuntimeMetadata{AgentID: base.AgentID, NodeID: base.NodeID, Epoch: 3, Revision: 0, Metadata: `{}`}); !errors.Is(err, ErrMetadataStale) {
		t.Fatalf("lower revision error=%v", err)
	}
	if err := repo.Upsert(ctx, AgentRuntimeMetadata{AgentID: base.AgentID, NodeID: base.NodeID, Epoch: 2, Revision: 9, Metadata: `{}`}); !errors.Is(err, ErrMetadataStale) {
		t.Fatalf("lower epoch error=%v", err)
	}
	newer := base
	newer.Revision = 2
	newer.Metadata = `{"items":[]}`
	if err := repo.Upsert(ctx, newer); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, base.AgentID)
	if err != nil || got.Revision != 2 || got.Metadata != newer.Metadata || got.Stale {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestRuntimeMetadataRepositoryEpochTakeoverAndStaleMark(t *testing.T) {
	db := newTestDB(t)
	repo := db.Metadata()
	ctx := context.Background()
	if err := repo.Upsert(ctx, AgentRuntimeMetadata{AgentID: "agent-1", NodeID: "node-a", Epoch: 1, Revision: 4, Metadata: `{}`}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkStale(ctx, "agent-1", 1); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "agent-1")
	if err != nil || !got.Stale {
		t.Fatalf("stale=%+v err=%v", got, err)
	}
	if err := repo.Upsert(ctx, AgentRuntimeMetadata{AgentID: "agent-1", NodeID: "node-b", Epoch: 2, Revision: 1, Metadata: `{}`}); err != nil {
		t.Fatal(err)
	}
	got, err = repo.Get(ctx, "agent-1")
	if err != nil || got.Epoch != 2 || got.NodeID != "node-b" || got.Stale {
		t.Fatalf("takeover=%+v err=%v", got, err)
	}
	if _, err := repo.Get(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing err=%v", err)
	}
}

func TestRuntimeMetadataRepositoryTouchRefreshesOnlyMatchingEpoch(t *testing.T) {
	db := newTestDB(t)
	repo := db.Metadata()
	ctx := context.Background()
	if err := repo.Upsert(ctx, AgentRuntimeMetadata{AgentID: "agent-1", NodeID: "node-a", Epoch: 2, Revision: 1, Metadata: `{}`, Stale: true}); err != nil {
		t.Fatal(err)
	}
	seen := time.Now().UTC()
	expires := seen.Add(time.Minute)
	if err := repo.Touch(ctx, "agent-1", 2, seen, expires); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "agent-1")
	if err != nil || got.Stale || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("touched=%+v err=%v", got, err)
	}
	if err := repo.Touch(ctx, "agent-1", 1, seen, expires); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale epoch touch err=%v", err)
	}
}

func TestRuntimeMetadataRepositoryCursorPagination(t *testing.T) {
	db := newTestDB(t)
	repo := db.Metadata()
	ctx := context.Background()
	for _, id := range []string{"agent-a", "agent-b", "agent-c"} {
		if err := repo.Upsert(ctx, AgentRuntimeMetadata{AgentID: id, NodeID: "node", Epoch: 1, Revision: 1, Metadata: `{}`}); err != nil {
			t.Fatal(err)
		}
	}
	p1, err := repo.List(ctx, "", 2)
	if err != nil || len(p1.Items) != 2 || !p1.HasMore || p1.NextCursor == "" {
		t.Fatalf("p1=%+v err=%v", p1, err)
	}
	p2, err := repo.List(ctx, p1.NextCursor, 2)
	if err != nil || len(p2.Items) != 1 || p2.Items[0].AgentID != "agent-c" {
		t.Fatalf("p2=%+v err=%v", p2, err)
	}
}
