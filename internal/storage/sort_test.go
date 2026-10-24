package storage

import (
	"context"
	"testing"
	"time"
)

func TestSortSpecBuildsStableOrderAndCursorPredicate(t *testing.T) {
	spec := SortSpec{Column: "created_at", Descending: true, Composite: true}
	if got := spec.OrderBy(); got != "created_at DESC, id DESC" {
		t.Fatalf("OrderBy() = %q", got)
	}
	predicate, args := spec.CursorCondition("2026-10-01T12:00:00Z", "row-1")
	if predicate != "(created_at < ? OR (created_at = ? AND id < ?))" {
		t.Fatalf("CursorCondition() predicate = %q", predicate)
	}
	if len(args) != 3 || args[0] != args[1] || args[2] != "row-1" {
		t.Fatalf("CursorCondition() args = %#v", args)
	}
}

func TestSortCursorRoundTripPreservesCompositeValues(t *testing.T) {
	wantTime := time.Date(2026, 10, 1, 12, 0, 0, 123000000, time.UTC)
	cursor := EncodeSortCursor(wantTime, "row-7")
	gotTime, gotID, ok := DecodeSortCursor(cursor)
	if !ok || !gotTime.Equal(wantTime) || gotID != "row-7" {
		t.Fatalf("DecodeSortCursor(%q) = %v, %q, %v", cursor, gotTime, gotID, ok)
	}
}

func TestSortSpecUsesMatchingSeekDirectionForID(t *testing.T) {
	for _, test := range []struct {
		name       string
		descending bool
		want       string
	}{
		{name: "descending", descending: true, want: "id < ?"},
		{name: "ascending", descending: false, want: "id > ?"},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := SortSpec{Column: "id", Descending: test.descending}
			predicate, args := spec.CursorCondition("", "row-1")
			if predicate != test.want {
				t.Fatalf("CursorCondition() = %q, want %q", predicate, test.want)
			}
			if len(args) != 1 || args[0] != "row-1" {
				t.Fatalf("CursorCondition() args = %#v", args)
			}
		})
	}
}

func TestSortSpecNextCursorMatchesCompositeShape(t *testing.T) {
	if got := decodeCursor((SortSpec{Column: "id"}).NextCursor("ignored", "row-1")); got != "row-1" {
		t.Fatalf("ID cursor = %q, want row-1", got)
	}
	if got := decodeCursor((SortSpec{Column: "domain", Composite: true}).NextCursor("example.test", "row-1")); got != "example.test\x00row-1" {
		t.Fatalf("composite cursor = %q", got)
	}
}

func TestAgentSortedIDDescendingCursorContinuesDownward(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, id := range []string{"agent-a", "agent-b", "agent-c"} {
		if err := db.Agents().Create(ctx, Agent{ID: id, Name: id, OwnerUserID: "owner", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}

	spec := SortSpec{Column: "id", Descending: true}
	first, err := db.Agents().List(ctx, AgentListFilter{Sort: spec}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].ID != "agent-c" {
		t.Fatalf("first page = %#v, want agent-c", first.Items)
	}
	second, err := db.Agents().List(ctx, AgentListFilter{Sort: spec}, first.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "agent-b" {
		t.Fatalf("second page = %#v, want agent-b", second.Items)
	}
}
