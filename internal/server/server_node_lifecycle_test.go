package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestServerNodeLifecycleRegistersAndHeartbeats(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:server-node-lifecycle?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	lifecycle := NewServerNodeLifecycle(db, "server-a", "127.0.0.1:9443", 10*time.Millisecond, 50*time.Millisecond)
	if err := lifecycle.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	node, err := db.Nodes().Get(ctx, "server-a")
	if err != nil {
		t.Fatal(err)
	}
	if node.Name != "server-a" || node.Address != "127.0.0.1:9443" || !node.Enabled || node.DeletedAt != nil {
		t.Fatalf("registered node = %+v", node)
	}
	if node.LastSeenAt == nil || node.ExpiresAt == nil || !node.ExpiresAt.After(*node.LastSeenAt) {
		t.Fatalf("registered heartbeat fields = %+v", node)
	}
	firstSeen := *node.LastSeenAt

	var secondSeen time.Time
	deadline := time.Now().Add(2 * time.Second)
	for secondSeen.Before(firstSeen) {
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat did not advance last_seen_at: first=%s latest=%s", firstSeen, secondSeen)
		}
		time.Sleep(5 * time.Millisecond)
		node, err = db.Nodes().Get(ctx, "server-a")
		if err != nil {
			t.Fatal(err)
		}
		if node.LastSeenAt != nil {
			secondSeen = *node.LastSeenAt
		}
	}

	if err := lifecycle.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	closedSeen := secondSeen
	time.Sleep(25 * time.Millisecond)
	node, err = db.Nodes().Get(ctx, "server-a")
	if err != nil {
		t.Fatal(err)
	}
	if node.LastSeenAt == nil || !node.LastSeenAt.Equal(closedSeen) {
		t.Fatalf("heartbeat continued after Close: before=%s after=%s", closedSeen, node.LastSeenAt)
	}
}

func TestServerNodeLifecycleRejectsDisabledAndDeletedNodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(storage.ServerNode) storage.ServerNode
		want string
	}{
		{name: "disabled", set: func(node storage.ServerNode) storage.ServerNode {
			node.Enabled = false
			return node
		}, want: "disabled"},
		{name: "deleted", set: func(node storage.ServerNode) storage.ServerNode {
			deletedAt := time.Now().UTC()
			node.DeletedAt = &deletedAt
			return node
		}, want: "deleted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.OpenSQLite(ctx, "file:server-node-lifecycle-"+tc.name+"?mode=memory&cache=shared")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			node := storage.ServerNode{ID: "server-a", Name: "server-a", Address: "127.0.0.1:9443", Epoch: 1, Enabled: true}
			if err := db.Nodes().Create(ctx, node); err != nil {
				t.Fatal(err)
			}
			if err := db.Nodes().Update(ctx, tc.set(node)); err != nil {
				t.Fatal(err)
			}
			lifecycle := NewServerNodeLifecycle(db, "server-a", "127.0.0.1:9443", time.Minute, 2*time.Minute)
			err = lifecycle.Start(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Start() error = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestNewServerRuntimeStartsServerNodeLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:server-runtime-node-lifecycle?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		NodeID: "server-runtime",
		Relay:  config.RelayConfig{Endpoint: "127.0.0.1:9443"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	node, err := db.Nodes().Get(ctx, "server-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if node.Address != "127.0.0.1:9443" || !node.Enabled || node.DeletedAt != nil {
		t.Fatalf("runtime registered node = %+v", node)
	}
}
