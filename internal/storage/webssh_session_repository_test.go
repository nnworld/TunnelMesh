package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWebSSHSessionTicketConsumedOnce(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	session := WebSSHSession{
		ID: "webssh-1", OwnerUserID: "user-a", RemoteServerID: "server-1",
		AgentID: "agent-a", OwnerNodeID: "server-node-a", TicketHash: "hash",
		TicketExpiresAt: now.Add(30 * time.Second), Status: WebSSHSessionPending,
		CreatedAt: now, ExpiresAt: now.Add(8 * time.Hour),
	}
	if _, err := db.WebSSHSessions().Create(ctx, session); err != nil {
		t.Fatalf("create WebSSH session: %v", err)
	}
	active, err := db.WebSSHSessions().ConsumeTicket(ctx, "webssh-1", "hash", now.Add(time.Second))
	if err != nil {
		t.Fatalf("consume valid ticket: %v", err)
	}
	if active.Status != WebSSHSessionActive || active.ConnectedAt == nil {
		t.Fatalf("active session = %+v", active)
	}
	if _, err := db.WebSSHSessions().ConsumeTicket(ctx, "webssh-1", "hash", now.Add(2*time.Second)); !errors.Is(err, ErrWebSSHTicketInvalid) {
		t.Fatalf("second ticket error = %v, want ErrWebSSHTicketInvalid", err)
	}

	activeSessions, err := db.WebSSHSessions().ListActiveByOwner(ctx, "user-a", now.Add(3*time.Second))
	if err != nil || len(activeSessions) != 1 {
		t.Fatalf("active sessions = %+v, err = %v", activeSessions, err)
	}
}

func TestWebSSHSessionCloseActiveByNode(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, session := range []WebSSHSession{
		{
			ID: "webssh-local", OwnerUserID: "user-a", RemoteServerID: "server-1",
			AgentID: "agent-a", OwnerNodeID: "node-a", TicketHash: "hash-a",
			TicketExpiresAt: now.Add(time.Minute), Status: WebSSHSessionActive,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		},
		{
			ID: "webssh-remote", OwnerUserID: "user-a", RemoteServerID: "server-1",
			AgentID: "agent-a", OwnerNodeID: "node-b", TicketHash: "hash-b",
			TicketExpiresAt: now.Add(time.Minute), Status: WebSSHSessionActive,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		},
	} {
		if _, err := db.WebSSHSessions().Create(ctx, session); err != nil {
			t.Fatalf("create WebSSH session %s: %v", session.ID, err)
		}
	}

	closed, err := db.WebSSHSessions().CloseActiveByNode(ctx, "node-a", now, "node_restarted")
	if err != nil {
		t.Fatalf("close active sessions by node: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed = %d, want 1", closed)
	}
	local, err := db.WebSSHSessions().Get(ctx, "webssh-local")
	if err != nil || local.Status != WebSSHSessionClosed || local.CloseReason != "node_restarted" || local.ClosedAt == nil {
		t.Fatalf("local session = %+v, err = %v", local, err)
	}
	remote, err := db.WebSSHSessions().Get(ctx, "webssh-remote")
	if err != nil || remote.Status != WebSSHSessionActive {
		t.Fatalf("remote session = %+v, err = %v", remote, err)
	}
}

// TestWebSSHSessionListActivePage pins the cursor pagination the admin console
// relies on so a user can release a stuck active-session quota. Closed rows,
// expired rows and other owners' rows must never appear, otherwise the console
// would offer to disconnect sessions it does not own.
func TestWebSSHSessionListActivePage(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, session := range []WebSSHSession{
		{ID: "webssh-a1", OwnerUserID: "user-a", Status: WebSSHSessionActive},
		{ID: "webssh-a2", OwnerUserID: "user-a", Status: WebSSHSessionActive},
		{ID: "webssh-a3", OwnerUserID: "user-a", Status: WebSSHSessionActive},
		{ID: "webssh-a4", OwnerUserID: "user-a", Status: WebSSHSessionClosed},
		{ID: "webssh-b1", OwnerUserID: "user-b", Status: WebSSHSessionActive},
	} {
		session.RemoteServerID = "server-1"
		session.AgentID = "agent-a"
		session.OwnerNodeID = "node-a"
		session.TicketHash = "hash-" + session.ID
		session.TicketExpiresAt = now.Add(time.Minute)
		session.CreatedAt = now
		session.ExpiresAt = now.Add(time.Hour)
		if _, err := db.WebSSHSessions().Create(ctx, session); err != nil {
			t.Fatalf("create WebSSH session %s: %v", session.ID, err)
		}
	}

	first, err := db.WebSSHSessions().ListActivePage(ctx, "user-a", now, "", 2)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "webssh-a1" || first.Items[1].ID != "webssh-a2" {
		t.Fatalf("first page items = %+v", first.Items)
	}
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want HasMore with a cursor", first)
	}

	second, err := db.WebSSHSessions().ListActivePage(ctx, "user-a", now, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "webssh-a3" {
		t.Fatalf("second page items = %+v", second.Items)
	}
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("second page = %+v, want the final page", second)
	}

	expired, err := db.WebSSHSessions().ListActivePage(ctx, "user-a", now.Add(2*time.Hour), "", 10)
	if err != nil {
		t.Fatalf("list expired page: %v", err)
	}
	if len(expired.Items) != 0 {
		t.Fatalf("expired page items = %+v, want none", expired.Items)
	}

	unknown, err := db.WebSSHSessions().ListActivePage(ctx, "user-a", now, encodeCursor("webssh-zz"), 10)
	if err != nil {
		t.Fatalf("list with unknown cursor: %v", err)
	}
	if len(unknown.Items) != 0 || unknown.HasMore {
		t.Fatalf("unknown cursor page = %+v, want empty", unknown)
	}

	defaulted, err := db.WebSSHSessions().ListActivePage(ctx, "user-a", now, "", 0)
	if err != nil {
		t.Fatalf("list with default limit: %v", err)
	}
	if len(defaulted.Items) != 3 {
		t.Fatalf("default limit items = %+v, want every active row", defaulted.Items)
	}
}
