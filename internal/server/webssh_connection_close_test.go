package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func createClusterWebSSHSession(t *testing.T, db *storage.DB, id, ownerNode string) storage.WebSSHSession {
	t.Helper()
	created, err := db.WebSSHSessions().Create(context.Background(), storage.WebSSHSession{
		ID: id, OwnerUserID: "user-a", RemoteServerID: "server-a", AgentID: "agent-a",
		OwnerNodeID: ownerNode, TicketHash: "hash", TicketExpiresAt: time.Now().Add(time.Minute),
		Status: storage.WebSSHSessionActive, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

type fakeWebSSHRelayClient struct {
	request relay.CloseWebSSHConnectionRequest
	closed  bool
}

func TestClusterWebSSHCloseMapsUnavailableOwner(t *testing.T) {
	db, _, _, _, _ := webSSHBrokerFixture(t)
	ctx := context.Background()
	created := createClusterWebSSHSession(t, db, "session-remote", "node-b")
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "node-b", Name: "node-b", Address: "127.0.0.1:9", Epoch: 7, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	closer := NewClusterWebSSHConnectionCloseService(db.WebSSHSessions(), db.Nodes(), nil, "node-a", func(context.Context, string, int64) (WebSSHConnectionRelayClient, error) {
		return nil, errors.New("connection refused")
	})
	if err := closer.Close(ctx, created.ID); err != ErrWebSSHConnectionNodeUnavailable {
		t.Fatalf("unavailable owner error = %v", err)
	}
	session, err := db.WebSSHSessions().Get(ctx, created.ID)
	if err != nil || session.Status != storage.WebSSHSessionActive {
		t.Fatalf("durable session = %+v, err=%v", session, err)
	}
}

func (c *fakeWebSSHRelayClient) CloseWebSSHConnection(_ context.Context, request relay.CloseWebSSHConnectionRequest) error {
	c.request = request
	return nil
}

func (c *fakeWebSSHRelayClient) Close() error { c.closed = true; return nil }

func TestClusterWebSSHCloseRoutesLocal(t *testing.T) {
	db, _, _, broker, _ := webSSHBrokerFixture(t)
	ctx := context.Background()
	createClusterWebSSHSession(t, db, "session-local", "node-a")
	closer := NewClusterWebSSHConnectionCloseService(db.WebSSHSessions(), db.Nodes(), broker, "node-a", nil)
	if err := closer.Close(ctx, "session-local"); err != nil {
		t.Fatal(err)
	}
}

func TestClusterWebSSHCloseRoutesRemote(t *testing.T) {
	db, _, _, broker, _ := webSSHBrokerFixture(t)
	ctx := context.Background()
	createClusterWebSSHSession(t, db, "session-remote", "node-b")
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "node-b", Name: "node-b", Address: "127.0.0.1:9", Epoch: 7, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	client := &fakeWebSSHRelayClient{}
	closer := NewClusterWebSSHConnectionCloseService(db.WebSSHSessions(), db.Nodes(), broker, "node-a", func(context.Context, string, int64) (WebSSHConnectionRelayClient, error) {
		return client, nil
	})
	if err := closer.Close(ctx, "session-remote"); err != nil {
		t.Fatal(err)
	}
	if client.request.SessionID != "session-remote" || client.request.RequestedByNodeID != "node-a" || !client.closed {
		t.Fatalf("relay request = %+v", client.request)
	}
}
