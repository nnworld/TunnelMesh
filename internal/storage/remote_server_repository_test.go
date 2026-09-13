package storage

import (
	"context"
	"testing"
	"time"
)

func TestRemoteServerRepositoryOwnerFilterAndCursor(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, name := range []string{"a", "b", "c"} {
		server := RemoteServer{
			OwnerUserID: "user-a", Name: name, Host: "10.0.0.8", Port: 22,
			DefaultUsername: "deploy", AgentID: "agent-a", Enabled: true,
			CreatedAt: now, UpdatedAt: now,
		}
		if _, err := db.RemoteServers().Create(ctx, server); err != nil {
			t.Fatalf("create remote server %s: %v", name, err)
		}
	}

	page, err := db.RemoteServers().List(ctx, RemoteServerFilter{OwnerUserID: "user-a"}, "", 2)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(page.Items) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page = %+v", page)
	}
	next, err := db.RemoteServers().List(ctx, RemoteServerFilter{OwnerUserID: "user-a"}, page.NextCursor, 2)
	if err != nil {
		t.Fatalf("list next page: %v", err)
	}
	if len(next.Items) != 1 || next.HasMore {
		t.Fatalf("next page = %+v", next)
	}
	other, err := db.RemoteServers().List(ctx, RemoteServerFilter{OwnerUserID: "user-b"}, "", 10)
	if err != nil {
		t.Fatalf("list other owner: %v", err)
	}
	if len(other.Items) != 0 {
		t.Fatalf("other owner data leaked: %+v", other.Items)
	}
}
