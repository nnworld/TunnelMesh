package storage

import (
	"context"
	"testing"
	"time"
)

// TestAgentCursorResumesPagination pins the contract AgentCursor exists for: a
// caller that filters rows out of a page must be able to resume from the last row
// it returned. A bare agent ID would decode unchanged, fail the composite split and
// silently restart from page one, so the cursor has to round-trip through List.
func TestAgentCursorResumesPagination(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, DriverSQLite, "file:agent-cursor-test?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	// created_at DESC ordering means the newest row pages first.
	for _, agent := range []Agent{
		{ID: "agent-a", Name: "a", OwnerUserID: "owner", Enabled: true, CreatedAt: base},
		{ID: "agent-b", Name: "b", OwnerUserID: "owner", Enabled: true, CreatedAt: base.Add(time.Minute)},
		{ID: "agent-c", Name: "c", OwnerUserID: "owner", Enabled: true, CreatedAt: base.Add(2 * time.Minute)},
	} {
		if err := db.Agents().Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}

	first, err := db.Agents().List(ctx, AgentListFilter{OwnerUserID: "owner"}, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || !first.HasMore {
		t.Fatalf("first page = %d items, hasMore=%v", len(first.Items), first.HasMore)
	}
	// Resume from the last emitted row rather than from the page's own cursor, which
	// is what a filtering caller has to do.
	resumed, err := db.Agents().List(ctx, AgentListFilter{OwnerUserID: "owner"}, AgentCursor(first.Items[len(first.Items)-1]), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Items) != 1 || resumed.Items[0].ID != "agent-a" {
		t.Fatalf("resumed page = %+v, want exactly [agent-a]", resumed.Items)
	}
	for _, item := range resumed.Items {
		for _, seen := range first.Items {
			if item.ID == seen.ID {
				t.Fatalf("cursor repeated %q across pages", item.ID)
			}
		}
	}
}

// TestAgentCursorRejectsBareID documents why the helper is not optional: a bare ID
// is accepted by decodeCursor but yields no composite key, so List ignores it.
func TestAgentCursorRejectsBareID(t *testing.T) {
	if _, _, ok := decodeCompositeCursor("agent-a"); ok {
		t.Fatal("a bare agent ID decoded as a composite cursor; the fallback would mask pagination bugs")
	}
	if _, _, ok := decodeCompositeCursor(AgentCursor(Agent{ID: "agent-a", CreatedAt: time.Now()})); !ok {
		t.Fatal("AgentCursor did not produce a decodable composite cursor")
	}
}
