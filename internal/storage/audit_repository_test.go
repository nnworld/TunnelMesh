package storage

import (
	"context"
	"testing"
	"time"
)

func TestAuditRepositoryListsNewestFirstWithStablePagination(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := db.Audits()
	base := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	records := []AuditLog{
		{ID: "z-audit-oldest", Action: "old", ResourceType: "test", CreatedAt: base},
		{ID: "b-audit-tie", Action: "tie-low-id", ResourceType: "test", CreatedAt: base.Add(time.Second)},
		{ID: "m-audit-tie", Action: "tie-high-id", ResourceType: "test", CreatedAt: base.Add(time.Second)},
		{ID: "a-audit-newest", Action: "new", ResourceType: "test", CreatedAt: base.Add(2 * time.Second)},
	}
	for _, record := range records {
		if err := repo.Create(ctx, record); err != nil {
			t.Fatalf("create audit %s: %v", record.ID, err)
		}
	}

	first, err := repo.List(ctx, AuditFilter{}, "", 2)
	if err != nil {
		t.Fatalf("list first audit page: %v", err)
	}
	assertAuditIDs(t, first.Items, "a-audit-newest", "m-audit-tie")
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first audit page = %+v, want another page and cursor", first)
	}

	second, err := repo.List(ctx, AuditFilter{}, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("list second audit page: %v", err)
	}
	assertAuditIDs(t, second.Items, "b-audit-tie", "z-audit-oldest")
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("second audit page = %+v, want final page", second)
	}

	legacy, err := repo.List(ctx, AuditFilter{}, encodeCursor("m-audit-tie"), 10)
	if err != nil {
		t.Fatalf("list with legacy ID cursor: %v", err)
	}
	assertAuditIDs(t, legacy.Items, "b-audit-tie", "z-audit-oldest")
}

func TestAuditRepositoryAppliesFiltersWithStablePagination(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := db.Audits()
	base := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	records := []AuditLog{
		{ID: "audit-filter-old", ActorUserID: "actor-1", Action: "route.updated", ResourceType: "tunnel", ResourceID: "route-1", CreatedAt: base},
		{ID: "audit-filter-new", ActorUserID: "actor-1", Action: "route.updated", ResourceType: "tunnel", ResourceID: "route-1", CreatedAt: base.Add(time.Minute)},
		{ID: "audit-filter-noise", ActorUserID: "actor-2", Action: "agent.created", ResourceType: "agent", ResourceID: "agent-1", CreatedAt: base.Add(2 * time.Minute)},
	}
	for _, record := range records {
		if err := repo.Create(ctx, record); err != nil {
			t.Fatalf("create audit %s: %v", record.ID, err)
		}
	}

	filter := AuditFilter{
		ActorUserID:  "actor-1",
		Action:       "route.updated",
		ResourceType: "tunnel",
		ResourceID:   "route-1",
		CreatedFrom:  &[]time.Time{base}[0],
		CreatedTo:    &[]time.Time{base.Add(2 * time.Minute)}[0],
	}
	first, err := repo.List(ctx, filter, "", 1)
	if err != nil {
		t.Fatalf("list filtered first page: %v", err)
	}
	assertAuditIDs(t, first.Items, "audit-filter-new")
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("filtered first page = %+v, want another page", first)
	}

	second, err := repo.List(ctx, filter, first.NextCursor, 10)
	if err != nil {
		t.Fatalf("list filtered second page: %v", err)
	}
	assertAuditIDs(t, second.Items, "audit-filter-old")
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("filtered second page = %+v, want final page", second)
	}
}

func assertAuditIDs(t *testing.T, items []AuditLog, want ...string) {
	t.Helper()
	got := make([]string, len(items))
	for i, item := range items {
		got[i] = item.ID
	}
	if len(got) != len(want) {
		t.Fatalf("audit IDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("audit IDs = %v, want %v", got, want)
		}
	}
}

// TestAuditRepositoryPurgeOlderThanBatchesByCutoff pins the retention primitive:
// only rows strictly older than the cutoff go, the batch limit is honoured so a
// never-pruned table cannot be emptied in one long transaction, and the ids are
// picked deterministically (oldest first) so repeated passes make progress.
func TestAuditRepositoryPurgeOlderThanBatchesByCutoff(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := db.Audits()
	cutoff := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := repo.Create(ctx, AuditLog{
			ID:     "audit-old-" + string(rune('a'+i)),
			Action: "expired", ResourceType: "test",
			CreatedAt: cutoff.Add(-time.Duration(5-i) * time.Hour),
		}); err != nil {
			t.Fatalf("create old audit %d: %v", i, err)
		}
	}
	kept := []AuditLog{
		{ID: "audit-at-cutoff", Action: "boundary", ResourceType: "test", CreatedAt: cutoff},
		{ID: "audit-newer", Action: "new", ResourceType: "test", CreatedAt: cutoff.Add(time.Hour)},
	}
	for _, record := range kept {
		if err := repo.Create(ctx, record); err != nil {
			t.Fatalf("create %s: %v", record.ID, err)
		}
	}

	deleted, err := repo.PurgeOlderThan(ctx, cutoff, 2)
	if err != nil {
		t.Fatalf("PurgeOlderThan() error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("PurgeOlderThan() deleted = %d, want the batch size 2", deleted)
	}
	page, err := repo.List(ctx, AuditFilter{}, "", 50)
	if err != nil {
		t.Fatalf("list after purge: %v", err)
	}
	if len(page.Items) != 5 {
		t.Fatalf("rows after one batch = %d, want 3 old + 2 kept", len(page.Items))
	}
	for _, record := range page.Items {
		if record.CreatedAt.Before(cutoff) && record.Action != "expired" {
			t.Fatalf("unexpected survivor %q", record.ID)
		}
	}
	// A boundary row created exactly at the cutoff survives: retention is inclusive,
	// so "keep 30 days" cannot delete the record written at the moment the window
	// closed and make the count off by one.
	if _, err := repo.PurgeOlderThan(ctx, cutoff, 100); err != nil {
		t.Fatalf("second PurgeOlderThan() error = %v", err)
	}
	page, err = repo.List(ctx, AuditFilter{}, "", 50)
	if err != nil {
		t.Fatalf("list after second purge: %v", err)
	}
	assertAuditIDs(t, page.Items, "audit-newer", "audit-at-cutoff")
	if again, err := repo.PurgeOlderThan(ctx, cutoff, 100); err != nil || again != 0 {
		t.Fatalf("PurgeOlderThan() on an empty range = (%d, %v), want (0, nil)", again, err)
	}
}
