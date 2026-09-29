package storage

import (
	"context"
	"testing"
	"time"
)

// runAgentListRepositoryContract pins the paging contract the management console
// depends on. It runs on every dialect the contract suite is started with, so
// MySQL 5.6 sees the same assertions as the SQLite unit tests.
func runAgentListRepositoryContract(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	const (
		owner      = "user-agent-list"
		otherOwner = "user-agent-list-other"
	)
	for _, id := range []string{owner, otherOwner} {
		if err := db.Users().Create(ctx, User{ID: id, Username: "agent-list-" + id, Role: "user"}); err != nil {
			t.Fatalf("create owner %s: %v", id, err)
		}
	}
	// Fixed, whole-second timestamps keep the fixture deterministic and avoid
	// relying on the wall clock to separate rows created in one statement batch.
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seed := []Agent{
		{ID: "agent-list-a", Name: "edge-alpha", OwnerUserID: owner, Capabilities: "{}", Enabled: true, CreatedAt: base},
		{ID: "agent-list-b", Name: "edge-beta", OwnerUserID: owner, Capabilities: "{}", Enabled: true, CreatedAt: base.Add(24 * time.Hour)},
		{ID: "agent-list-c", Name: "other-team", OwnerUserID: otherOwner, Capabilities: "{}", Enabled: true, CreatedAt: base.Add(48 * time.Hour)},
		{ID: "agent-list-d", Name: "edge-delta", OwnerUserID: owner, Capabilities: "{}", Enabled: true, CreatedAt: base.Add(72 * time.Hour)},
	}
	for _, agent := range seed {
		if err := db.Agents().Create(ctx, agent); err != nil {
			t.Fatalf("create %s: %v", agent.ID, err)
		}
	}

	// Newest first, because the console pages this order: page 1 has to be the
	// most recently registered agents, not the smallest ids.
	page, err := db.Agents().List(ctx, AgentListFilter{OwnerUserID: owner}, "", 2)
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(page.Items) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page = %+v", page)
	}
	if page.Items[0].ID != "agent-list-d" || page.Items[1].ID != "agent-list-b" {
		t.Fatalf("first page ids = %q, %q, want newest first", page.Items[0].ID, page.Items[1].ID)
	}

	next, err := db.Agents().List(ctx, AgentListFilter{OwnerUserID: owner}, page.NextCursor, 2)
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(next.Items) != 1 || next.Items[0].ID != "agent-list-a" || next.HasMore {
		t.Fatalf("second page = %+v", next)
	}

	// The keyword narrows inside the paging semantics: filtering after paging
	// would silently drop matching rows past the first page.
	filtered, err := db.Agents().List(ctx, AgentListFilter{OwnerUserID: owner, Keyword: "edge-b"}, "", 10)
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].ID != "agent-list-b" {
		t.Fatalf("filtered page = %+v, want only agent-list-b", filtered.Items)
	}
	byID, err := db.Agents().List(ctx, AgentListFilter{OwnerUserID: owner, Keyword: "agent-list-d"}, "", 10)
	if err != nil {
		t.Fatalf("list by id keyword: %v", err)
	}
	if len(byID.Items) != 1 || byID.Items[0].ID != "agent-list-d" {
		t.Fatalf("id keyword page = %+v, want only agent-list-d", byID.Items)
	}

	// Another owner's rows must never surface, and an unrelated keyword must
	// return an empty page rather than falling back to the unfiltered list.
	if other, err := db.Agents().List(ctx, AgentListFilter{OwnerUserID: otherOwner}, "", 10); err != nil || len(other.Items) != 1 || other.Items[0].ID != "agent-list-c" {
		t.Fatalf("other owner page = %+v err=%v", other.Items, err)
	}
	if none, err := db.Agents().List(ctx, AgentListFilter{OwnerUserID: owner, Keyword: "no-such-name"}, "", 10); err != nil || len(none.Items) != 0 {
		t.Fatalf("no-match page = %+v err=%v", none.Items, err)
	}
	// Walking one row at a time is what proves the (created_at, id) key: a page
	// boundary that orders by one column and pages by another repeats or drops a
	// row here instead of at an operator comparing two screens.
	walked := []string{}
	for cursor := ""; ; {
		step, err := db.Agents().List(ctx, AgentListFilter{OwnerUserID: owner, Keyword: "edge-"}, cursor, 1)
		if err != nil {
			t.Fatalf("walk page: %v", err)
		}
		for _, agent := range step.Items {
			walked = append(walked, agent.ID)
		}
		if !step.HasMore || step.NextCursor == "" {
			break
		}
		cursor = step.NextCursor
	}
	if len(walked) != 3 || walked[0] != "agent-list-d" || walked[1] != "agent-list-b" || walked[2] != "agent-list-a" {
		t.Fatalf("walked = %v, want agent-list-d, agent-list-b, agent-list-a", walked)
	}
}
