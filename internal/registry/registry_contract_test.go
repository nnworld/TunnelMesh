package registry

import (
	"context"
	"errors"
	"testing"
	"time"
)

type registryFactory func(t *testing.T) NodeRegistry

func runRegistryContract(t *testing.T, factory registryFactory) {
	t.Helper()
	ctx := context.Background()
	r := factory(t)
	defer r.Close()

	events, err := r.Watch(ctx, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := r.Register(ctx, NodeRegistration{NodeID: "node-a", Address: "10.0.0.1", AgentID: "agent-1", TTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if owner.Epoch != 1 || owner.NodeID != "node-a" {
		t.Fatalf("unexpected owner: %+v", owner)
	}
	select {
	case ev := <-events:
		if ev.Type != EventRegistered {
			t.Fatalf("event=%+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("register event timeout")
	}

	resolved, err := r.ResolveAgent(ctx, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Epoch != owner.Epoch {
		t.Fatalf("resolved=%+v owner=%+v", resolved, owner)
	}

	renewed, err := r.KeepAlive(ctx, owner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.After(owner.ExpiresAt) {
		t.Fatalf("renewal did not extend expiry: %v <= %v", renewed.ExpiresAt, owner.ExpiresAt)
	}
	select {
	case ev := <-events:
		if ev.Type != EventUpdated {
			t.Fatalf("event=%+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("renew event timeout")
	}

	if _, err := r.Register(ctx, NodeRegistration{NodeID: "node-b", Address: "10.0.0.2", AgentID: "agent-1", TTL: time.Second}); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("expected held, got %v", err)
	}

	if err := r.Revoke(ctx, owner); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if ev.Type != EventRevoked {
			t.Fatalf("event=%+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("revoke event timeout")
	}
	if _, err := r.ResolveAgent(ctx, "agent-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found after revoke, got %v", err)
	}
	if _, err := r.KeepAlive(ctx, owner, time.Second); !errors.Is(err, ErrFencing) {
		t.Fatalf("expected fencing, got %v", err)
	}

	owner, err = r.Register(ctx, NodeRegistration{NodeID: "node-b", Address: "10.0.0.2", AgentID: "agent-1", TTL: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := r.ResolveAgent(ctx, "agent-1"); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expected expired, got %v", err)
	}
	takeover, err := r.Register(ctx, NodeRegistration{NodeID: "node-c", Address: "10.0.0.3", AgentID: "agent-1", TTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if takeover.Epoch <= owner.Epoch {
		t.Fatalf("epoch did not fence takeover: old=%d new=%d", owner.Epoch, takeover.Epoch)
	}
	if _, err := r.KeepAlive(ctx, owner, time.Second); !errors.Is(err, ErrFencing) {
		t.Fatalf("stale owner accepted: %v", err)
	}
}
