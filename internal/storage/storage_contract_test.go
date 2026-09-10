package storage

import (
	"context"
	"database/sql"
	"errors"
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

	runServiceTokenRepositoryContract(t, db)

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
	audits, err := db.Audits().List(ctx, AuditFilter{}, "", 10)
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

func TestIdempotencyExpiredRecordsAreNotReplayed(t *testing.T) {
	db := newTestDB(t)
	expired := time.Now().UTC().Add(-time.Minute)
	if err := db.Idempotency().Put(context.Background(), IdempotencyRecord{Key: "expired", Response: `{"ok":true}`, ExpiresAt: &expired}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Idempotency().Get(context.Background(), "expired"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Get expired record error = %v, want sql.ErrNoRows", err)
	}
}

func TestAgentConnectionLeaseRepositoryContract(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	base := AgentLease{
		AgentID: "agent-connection-leases", NodeID: "node-a", InstanceID: "instance-a",
		ServerNodeID: "server-a", ConnectionEpoch: 1, ActiveStreams: 0, HealthScore: 100,
		TTL: time.Minute, AcquiredAt: now, UpdatedAt: now,
	}
	first := base
	first.ConnectionID = "conn-a"
	first, err := db.Leases().RegisterConnection(ctx, first)
	if err != nil {
		t.Fatalf("register first connection: %v", err)
	}
	second := base
	second.ConnectionID = "conn-b"
	second.NodeID = "node-b"
	second.InstanceID = "instance-b"
	second.ServerNodeID = "server-b"
	second, err = db.Leases().RegisterConnection(ctx, second)
	if err != nil {
		t.Fatalf("register second connection: %v", err)
	}
	active, err := db.Leases().ListActiveByAgent(ctx, base.AgentID)
	if err != nil {
		t.Fatalf("list active connections: %v", err)
	}
	if len(active) != 2 || active[0].ConnectionID != "conn-a" || active[1].ConnectionID != "conn-b" {
		t.Fatalf("active connections = %+v, want ordered conn-a and conn-b", active)
	}

	first.ConnectionEpoch = 2
	first.ActiveStreams = 3
	first.HealthScore = 87
	replacement, err := db.Leases().RegisterConnection(ctx, first)
	if err != nil {
		t.Fatalf("replace connection: %v", err)
	}
	if replacement.ConnectionEpoch != 2 {
		t.Fatalf("replacement epoch = %d, want 2", replacement.ConnectionEpoch)
	}
	if err := db.Leases().UpdateConnectionStats(ctx, AgentLease{AgentID: base.AgentID, ConnectionID: first.ConnectionID, ConnectionEpoch: 1, ActiveStreams: 9}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale update error = %v, want sql.ErrNoRows", err)
	}
	if err := db.Leases().RenewConnection(ctx, base.AgentID, first.ConnectionID, 1, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale renew error = %v, want sql.ErrNoRows", err)
	}
	if err := db.Leases().ReleaseConnection(ctx, base.AgentID, first.ConnectionID, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale release error = %v, want sql.ErrNoRows", err)
	}
	if err := db.Leases().UpdateConnectionStats(ctx, first); err != nil {
		t.Fatalf("update connection stats: %v", err)
	}
	if err := db.Leases().RenewConnection(ctx, base.AgentID, first.ConnectionID, first.ConnectionEpoch, time.Minute); err != nil {
		t.Fatalf("renew connection: %v", err)
	}

	expired := base
	expired.ConnectionID = "conn-expired"
	expired.TTL = time.Millisecond
	if _, err := db.Leases().RegisterConnection(ctx, expired); err != nil {
		t.Fatalf("register expired connection: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	active, err = db.Leases().ListActiveByAgent(ctx, base.AgentID)
	if err != nil {
		t.Fatalf("list active after expiry: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active after expiry = %+v, want conn-a and conn-b only", active)
	}
	if err := db.Leases().ReleaseConnection(ctx, base.AgentID, second.ConnectionID, second.ConnectionEpoch); err != nil {
		t.Fatalf("release second connection: %v", err)
	}
}

func TestServerNodeRepositoryLifecycleAndStatsContract(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	nodes := db.Nodes()

	node := ServerNode{
		ID: "server-a", Name: "edge-a", Address: "127.0.0.1:9443",
		Epoch: 7, Enabled: true,
	}
	if err := nodes.Ensure(ctx, node); err != nil {
		t.Fatalf("ensure node: %v", err)
	}
	found, err := nodes.Get(ctx, node.ID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if found.Name != "edge-a" || !found.Enabled || found.DeletedAt != nil {
		t.Fatalf("node = %+v, want enabled edge-a", found)
	}
	startup := node
	startup.Epoch = 1
	if err := nodes.Ensure(ctx, startup); err != nil {
		t.Fatalf("ensure startup node: %v", err)
	}
	found, err = nodes.Get(ctx, node.ID)
	if err != nil {
		t.Fatalf("get startup node: %v", err)
	}
	if found.Epoch != 7 {
		t.Fatalf("Ensure changed managed epoch to %d, want 7", found.Epoch)
	}

	disabled := found
	disabled.Enabled = false
	deletedAt := time.Now().UTC().Truncate(time.Microsecond)
	disabled.DeletedAt = &deletedAt
	if err := nodes.Update(ctx, disabled); err != nil {
		t.Fatalf("disable node: %v", err)
	}
	if err := nodes.Ensure(ctx, node); err != nil {
		t.Fatalf("ensure existing node: %v", err)
	}
	found, err = nodes.Get(ctx, node.ID)
	if err != nil {
		t.Fatalf("get managed node: %v", err)
	}
	if found.Enabled || found.DeletedAt == nil {
		t.Fatalf("Ensure re-enabled managed node: %+v", found)
	}

	managed := found
	managed.Enabled = true
	managed.DeletedAt = nil
	if err := nodes.Update(ctx, managed); err != nil {
		t.Fatalf("restore node: %v", err)
	}
	lastSeen := time.Now().UTC().Truncate(time.Microsecond)
	expires := lastSeen.Add(90 * time.Second)
	if err := nodes.Touch(ctx, node.ID, lastSeen, expires); err != nil {
		t.Fatalf("touch node: %v", err)
	}
	found, err = nodes.Get(ctx, node.ID)
	if err != nil {
		t.Fatalf("get touched node: %v", err)
	}
	if found.LastSeenAt == nil || !found.LastSeenAt.Equal(lastSeen) || found.ExpiresAt == nil || !found.ExpiresAt.Equal(expires) {
		t.Fatalf("touched node = %+v, want lastSeen=%s expires=%s", found, lastSeen, expires)
	}

	lease := AgentLease{
		AgentID: "server-node-stats-agent", NodeID: "server-a",
		ConnectionID: "conn-active", ServerNodeID: "server-a",
		ActiveStreams: 3, HealthScore: 90, TTL: time.Minute,
	}
	if _, err := db.Leases().RegisterConnection(ctx, lease); err != nil {
		t.Fatalf("register active lease: %v", err)
	}
	expired := lease
	expired.ConnectionID = "conn-expired"
	expired.TTL = -time.Minute
	if _, err := db.Leases().RegisterConnection(ctx, expired); err != nil {
		t.Fatalf("register expired lease: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE agent_connection_leases SET expires_at=? WHERE connection_id=?`, tm(time.Now().UTC().Add(-time.Minute)), "conn-expired"); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	stats, err := nodes.StatsByNodeIDs(ctx, []string{"server-a", "server-missing"})
	if err != nil {
		t.Fatalf("stats by node IDs: %v", err)
	}
	got, ok := stats["server-a"]
	if !ok {
		t.Fatalf("stats = %+v, want server-a", stats)
	}
	if got.ActiveConnections != 1 || got.ActiveStreams != 3 || got.HealthScore != 90 {
		t.Fatalf("server-a stats = %+v, want one active connection, three streams, health 90", got)
	}
	if _, ok := stats["server-missing"]; ok {
		t.Fatalf("stats = %+v, want no entry for missing node", stats)
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
