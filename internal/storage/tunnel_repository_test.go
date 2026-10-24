package storage

import (
	"context"
	"testing"
)

func TestTunnelSortedListFiltersKeywordBeforePaging(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, tunnel := range []Tunnel{
		{ID: "route-alpha", AgentID: "agent-a", Domain: "alpha.example.test", Protocol: "http"},
		{ID: "route-beta", AgentID: "agent-a", Domain: "beta.example.test", Protocol: "http"},
	} {
		if err := db.Tunnels().Create(ctx, tunnel); err != nil {
			t.Fatal(err)
		}
	}
	sorted, ok := db.Tunnels().(FilteredSortedTunnelRepository)
	if !ok {
		t.Fatal("tunnel repository does not support filtered sorting")
	}
	page, err := sorted.ListSortedFiltered(ctx, TunnelListFilter{Keyword: "beta"}, SortSpec{Column: "created_at", Descending: true, Composite: true}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Domain != "beta.example.test" {
		t.Fatalf("filtered tunnels = %#v", page.Items)
	}
}
