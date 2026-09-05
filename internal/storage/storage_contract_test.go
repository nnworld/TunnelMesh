package storage

import (
	"context"
	"testing"
	"time"
)

func runRepositoryContract(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	users := db.Users()
	user := User{ID: "user-1", Username: "alice", Role: "admin", PasswordHash: "hash"}
	if err := users.Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	got, err := users.Get(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.Username != user.Username || got.Role != user.Role {
		t.Fatalf("user = %+v", got)
	}
	if _, err := users.GetByUsername(ctx, user.Username); err != nil {
		t.Fatalf("get user by name: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := users.Create(ctx, User{ID: "user-" + string(rune('2'+i)), Username: "u" + string(rune('2'+i)), Role: "user"}); err != nil {
			t.Fatalf("create page user: %v", err)
		}
	}
	page, err := users.List(ctx, "", 2)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page = %+v", page)
	}
	page2, err := users.List(ctx, page.NextCursor, 2)
	if err != nil {
		t.Fatalf("list users page 2: %v", err)
	}
	if len(page2.Items) == 0 {
		t.Fatal("page 2 is empty")
	}

	if err := db.Tokens().Create(ctx, APIToken{ID: "tok-1", UserID: user.ID, TokenHash: "token-hash", IdempotencyKey: "idem-1"}); err != nil {
		t.Fatalf("create token: %v", err)
	}
	token, err := db.Tokens().GetByHash(ctx, "token-hash")
	if err != nil || token.UserID != user.ID {
		t.Fatalf("token = %+v err=%v", token, err)
	}
	if err := db.Tokens().Revoke(ctx, token.ID, time.Now()); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	agent := Agent{ID: "agent-1", Name: "edge", OwnerUserID: user.ID, Capabilities: `{"tcp":true}`}
	if err := db.Agents().Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := db.Agents().Get(ctx, agent.ID); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if err := db.Policies().Create(ctx, AgentPolicy{ID: "policy-1", AgentID: agent.ID, TargetHost: "10.0.0.1", TargetPort: 22, Protocol: "tcp"}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if err := db.Tunnels().Create(ctx, Tunnel{ID: "tunnel-1", AgentID: agent.ID, Protocol: "tcp", Domain: "ssh.example.test", TargetHost: "10.0.0.1", TargetPort: 22, Config: `{}`}); err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	if _, err := db.Tunnels().Get(ctx, "tunnel-1"); err != nil {
		t.Fatalf("get tunnel: %v", err)
	}

	if err := db.Nodes().Create(ctx, ServerNode{ID: "node-1", Address: "127.0.0.1:8080", Epoch: 1}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if _, err := db.Nodes().Get(ctx, "node-1"); err != nil {
		t.Fatalf("get node: %v", err)
	}
	lease, err := db.Leases().Acquire(ctx, AgentLease{AgentID: agent.ID, NodeID: "node-1", TTL: time.Minute})
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if lease.Epoch == 0 {
		t.Fatal("lease epoch was not assigned")
	}
	if err := db.Leases().Renew(ctx, agent.ID, lease.Epoch, time.Minute); err != nil {
		t.Fatalf("renew lease: %v", err)
	}
	if err := db.Leases().Release(ctx, agent.ID, lease.Epoch); err != nil {
		t.Fatalf("release lease: %v", err)
	}

	if err := db.Audits().Create(ctx, AuditLog{ID: "audit-1", ActorUserID: user.ID, Action: "create", ResourceType: "agent", ResourceID: agent.ID, Details: `{}`}); err != nil {
		t.Fatalf("create audit: %v", err)
	}
	audits, err := db.Audits().List(ctx, "", 10)
	if err != nil || len(audits.Items) != 1 {
		t.Fatalf("audits = %+v err=%v", audits, err)
	}

	if err := db.Idempotency().Put(ctx, IdempotencyRecord{Key: "request-1", UserID: user.ID, Response: `{"ok":true}`}); err != nil {
		t.Fatalf("put idempotency: %v", err)
	}
	idem, err := db.Idempotency().Get(ctx, "request-1")
	if err != nil || idem.Response != `{"ok":true}` {
		t.Fatalf("idempotency = %+v err=%v", idem, err)
	}
}

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenSQLite(context.Background(), "file:contract?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
