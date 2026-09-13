package server

import (
	"context"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func webSSHServiceFixture(t *testing.T) (*storage.DB, *WebSSHSessionService, auth.Principal) {
	t.Helper()
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:webssh-service-"+t.Name()+"?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	owner := auth.Principal{UserID: "user-a", Username: "alice", Role: "user"}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "agent-a", Name: "agent-a", OwnerUserID: owner.UserID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RemoteServers().Create(ctx, storage.RemoteServer{
		ID: "server-a", OwnerUserID: owner.UserID, Name: "server-a", Host: "10.0.0.8", Port: 22,
		DefaultUsername: "deploy", AgentID: "agent-a", Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Leases().RegisterConnection(ctx, storage.AgentLease{
		AgentID: "agent-a", ConnectionID: "conn-a", ServerNodeID: "node-a", InstanceID: "instance-a",
		NodeID: "agent-node-a", ConnectionEpoch: 1, TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	service := NewWebSSHSessionService(db.WebSSHSessions(), db.RemoteServers(), db.Credentials(), db.Agents(), db.Leases(), db.Audits(), "node-a")
	return db, service, owner
}

func TestWebSSHSessionCreateAuthenticateAndClose(t *testing.T) {
	db, service, owner := webSSHServiceFixture(t)
	ctx := context.Background()
	ticket, err := service.Create(ctx, owner, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if ticket.SessionID == "" || ticket.Ticket == "" || ticket.WebSocketPath != "/ws/webssh/"+ticket.SessionID {
		t.Fatalf("ticket = %+v", ticket)
	}
	session, server, err := service.AuthenticateTicket(ctx, ticket.SessionID, ticket.Ticket, time.Now().UTC())
	if err != nil || session.ID != ticket.SessionID || server.ID != "server-a" {
		t.Fatalf("authenticate = %+v/%+v, err=%v", session, server, err)
	}
	if _, _, err := service.AuthenticateTicket(ctx, ticket.SessionID, ticket.Ticket, time.Now().UTC()); err == nil {
		t.Fatal("ticket was reusable")
	}
	if err := service.Close(ctx, owner, ticket.SessionID, "user_closed"); err != nil {
		t.Fatalf("close session: %v", err)
	}
	if err := service.Close(ctx, owner, ticket.SessionID, "user_closed"); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	closed, err := db.WebSSHSessions().Get(ctx, ticket.SessionID)
	if err != nil || closed.Status != storage.WebSSHSessionClosed || closed.CloseReason != "user_closed" {
		t.Fatalf("closed session = %+v, err=%v", closed, err)
	}
}

func TestWebSSHSessionCreateRejectsInvalidState(t *testing.T) {
	_, service, owner := webSSHServiceFixture(t)
	ctx := context.Background()
	if _, err := service.Create(ctx, owner, "missing", CreateWebSSHSessionInput{Username: "deploy"}); err == nil {
		t.Fatal("missing server accepted")
	}
	if _, err := service.Create(ctx, owner, "server-a", CreateWebSSHSessionInput{Username: ""}); err == nil {
		t.Fatal("empty username accepted")
	}
	emptyNodeService := NewWebSSHSessionService(nil, nil, nil, nil, nil, nil, "")
	if _, err := emptyNodeService.Create(ctx, owner, "server-a", CreateWebSSHSessionInput{Username: "deploy"}); err == nil {
		t.Fatal("empty local node accepted")
	}
}

func TestWebSSHSessionServiceClosesStaleLocalSessionsAfterRestart(t *testing.T) {
	db, service, owner := webSSHServiceFixture(t)
	ctx := context.Background()
	ticket, err := service.Create(ctx, owner, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, err := service.AuthenticateTicket(ctx, ticket.SessionID, ticket.Ticket, time.Now().UTC()); err != nil {
		t.Fatalf("activate session: %v", err)
	}

	closed, err := service.CloseStaleLocalSessions(ctx, time.Now().UTC(), "node_restarted")
	if err != nil {
		t.Fatalf("close stale local sessions: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed = %d, want 1", closed)
	}
	session, err := db.WebSSHSessions().Get(ctx, ticket.SessionID)
	if err != nil || session.Status != storage.WebSSHSessionClosed || session.CloseReason != "node_restarted" {
		t.Fatalf("stale session = %+v, err = %v", session, err)
	}
	if _, err := service.Create(ctx, owner, "server-a", CreateWebSSHSessionInput{Username: "deploy"}); err != nil {
		t.Fatalf("create after stale cleanup: %v", err)
	}
}

func TestWebSSHSessionAuthenticationRejectsExpiredAndWrongTicket(t *testing.T) {
	db, service, owner := webSSHServiceFixture(t)
	ctx := context.Background()
	ticket, err := service.Create(ctx, owner, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.AuthenticateTicket(ctx, ticket.SessionID, "wrong", time.Now().UTC()); err != storage.ErrWebSSHTicketInvalid {
		t.Fatalf("wrong ticket error = %v", err)
	}
	if _, err := db.WebSSHSessions().ExpirePending(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.AuthenticateTicket(ctx, ticket.SessionID, ticket.Ticket, time.Now().UTC()); err != storage.ErrWebSSHTicketInvalid {
		t.Fatalf("expired ticket error = %v", err)
	}
}

func TestWebSSHSessionCloseAuthorization(t *testing.T) {
	_, service, owner := webSSHServiceFixture(t)
	ctx := context.Background()
	other := auth.Principal{UserID: "user-b", Role: "user"}
	admin := auth.Principal{UserID: "admin-a", Role: "admin"}
	ticket, err := service.Create(ctx, owner, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(ctx, other, ticket.SessionID, "user_closed"); err != ErrResourceForbidden {
		t.Fatalf("other owner error = %v", err)
	}
	if err := service.Close(ctx, admin, ticket.SessionID, "admin_closed"); err != nil {
		t.Fatalf("admin close = %v", err)
	}
}

// TestWebSSHSessionServiceListIsOwnerScoped pins that the console list only ever
// exposes the caller's own sessions. Cross-user visibility would let one account
// disconnect another tenant's shell, so the owner filter is forced server-side
// rather than derived from the request.
func TestWebSSHSessionServiceListIsOwnerScoped(t *testing.T) {
	db, service, alice := webSSHServiceFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, session := range []storage.WebSSHSession{
		{ID: "webssh-alice-1", OwnerUserID: "user-a", Status: storage.WebSSHSessionActive},
		{ID: "webssh-alice-2", OwnerUserID: "user-a", Status: storage.WebSSHSessionActive},
		{ID: "webssh-alice-closed", OwnerUserID: "user-a", Status: storage.WebSSHSessionClosed},
		{ID: "webssh-bob-1", OwnerUserID: "user-b", Status: storage.WebSSHSessionActive},
	} {
		session.RemoteServerID = "server-a"
		session.AgentID = "agent-a"
		session.OwnerNodeID = "node-a"
		session.TicketHash = "hash-" + session.ID
		session.TicketExpiresAt = now.Add(time.Minute)
		session.CreatedAt = now
		session.ExpiresAt = now.Add(time.Hour)
		if _, err := db.WebSSHSessions().Create(ctx, session); err != nil {
			t.Fatalf("create session %s: %v", session.ID, err)
		}
	}

	page, err := service.List(ctx, alice, "", 50)
	if err != nil {
		t.Fatalf("list alice sessions: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != "webssh-alice-1" || page.Items[1].ID != "webssh-alice-2" {
		t.Fatalf("alice page = %+v", page.Items)
	}

	bob := auth.Principal{UserID: "user-b", Username: "bob", Role: "user"}
	bobPage, err := service.List(ctx, bob, "", 50)
	if err != nil {
		t.Fatalf("list bob sessions: %v", err)
	}
	if len(bobPage.Items) != 1 || bobPage.Items[0].ID != "webssh-bob-1" {
		t.Fatalf("bob page = %+v", bobPage.Items)
	}

	// Admins also list only their own sessions: cross-user management needs its
	// own authorization and audit model and is deliberately out of scope.
	admin := auth.Principal{UserID: "admin-a", Username: "root", Role: "admin"}
	adminPage, err := service.List(ctx, admin, "", 50)
	if err != nil {
		t.Fatalf("list admin sessions: %v", err)
	}
	if len(adminPage.Items) != 0 {
		t.Fatalf("admin page = %+v, want only the admin's own sessions", adminPage.Items)
	}

	paged, err := service.List(ctx, alice, "", 1)
	if err != nil {
		t.Fatalf("list alice first page: %v", err)
	}
	if len(paged.Items) != 1 || !paged.HasMore || paged.NextCursor == "" {
		t.Fatalf("paged = %+v, want one item with a cursor", paged)
	}
	rest, err := service.List(ctx, alice, paged.NextCursor, 1)
	if err != nil {
		t.Fatalf("list alice second page: %v", err)
	}
	if len(rest.Items) != 1 || rest.Items[0].ID != "webssh-alice-2" || rest.HasMore {
		t.Fatalf("rest = %+v", rest)
	}

	var nilService *WebSSHSessionService
	if _, err := nilService.List(ctx, alice, "", 50); err != ErrWebSSHSessionUnavailable {
		t.Fatalf("nil service error = %v", err)
	}
}
