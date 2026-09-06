package server

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestMetadataManagerFencesIdentityEpochAndRevision(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{})
	session, err := m.Register(context.Background(), AgentRegistration{AgentID: "agent-1", NodeID: "node-1", Epoch: 4}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	accepted := protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 4, Revision: 1}
	ack, err := session.HandleMetadata(context.Background(), accepted)
	if err != nil || !ack.Accepted {
		t.Fatalf("first update ack=%+v err=%v", ack, err)
	}
	ack, err = session.HandleMetadata(context.Background(), accepted)
	if err != nil || !ack.Accepted || !ack.Idempotent {
		t.Fatalf("replay ack=%+v err=%v", ack, err)
	}
	ack, err = session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 4, Revision: 0})
	if err != nil || ack.Accepted || len(ack.Errors) == 0 {
		t.Fatalf("lower revision ack=%+v err=%v", ack, err)
	}
	ack, err = session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "other", Epoch: 4, Revision: 2})
	if err != nil || ack.Accepted || len(ack.Errors) == 0 {
		t.Fatalf("identity mismatch ack=%+v err=%v", ack, err)
	}
	ack, err = session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 3, Revision: 2})
	if err != nil || ack.Accepted || len(ack.Errors) == 0 {
		t.Fatalf("epoch mismatch ack=%+v err=%v", ack, err)
	}
}

func TestMetadataManagerReturnsFieldLevelCallbackErrors(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{MetadataCallback: func(_ context.Context, _ AgentRegistration, _ protocol.AgentMetadataPayload) []protocol.AgentMetadataError {
		return []protocol.AgentMetadataError{{Name: "region", Code: "rejected", Message: "not allowlisted"}}
	}})
	session, err := m.Register(context.Background(), AgentRegistration{AgentID: "agent-1", NodeID: "node-1", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	ack, err := session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 1, Revision: 1})
	if err != nil || ack.Accepted || len(ack.Errors) != 1 || ack.Errors[0].Code != "rejected" {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
	if errors.Is(err, ErrSessionClosed) {
		t.Fatal("metadata callback error closed session")
	}
}

func TestMetadataManagerRejectsZeroRevisionAndConflictingReplay(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{})
	session, err := m.Register(context.Background(), AgentRegistration{AgentID: "agent-1", NodeID: "node-1", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	ack, err := session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 1})
	if err != nil || ack.Accepted || len(ack.Errors) == 0 || ack.Errors[0].Code != "invalid_revision" {
		t.Fatalf("zero revision ack=%+v err=%v", ack, err)
	}
	first := protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 1, Revision: 1, Items: []protocol.AgentMetadataItem{{Name: "region", Source: "env", Value: "east"}}}
	if ack, err = session.HandleMetadata(context.Background(), first); err != nil || !ack.Accepted {
		t.Fatalf("first ack=%+v err=%v", ack, err)
	}
	conflict := first
	conflict.Items[0].Value = "west"
	ack, err = session.HandleMetadata(context.Background(), conflict)
	if err != nil || ack.Accepted || len(ack.Errors) == 0 || ack.Errors[0].Code != "revision_conflict" {
		t.Fatalf("conflicting replay ack=%+v err=%v", ack, err)
	}
}

type metadataSessionWSConn struct {
	mu     sync.Mutex
	reads  [][]byte
	writes [][]byte
	closed bool
}

func (c *metadataSessionWSConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reads) == 0 {
		return 0, nil, io.EOF
	}
	payload := c.reads[0]
	c.reads = c.reads[1:]
	return 2, payload, nil
}
func (c *metadataSessionWSConn) WriteMessage(_ int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), payload...))
	return nil
}
func (c *metadataSessionWSConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func TestServeAgentSessionRemovesSessionAfterTransportEOF(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	conn := &metadataSessionWSConn{}
	tr := NewWSFrameTransport(conn)
	registration := AgentRegistration{AgentID: "agent-eof", NodeID: "node-1", Epoch: 1}
	if err := ServeAgentSession(context.Background(), manager, registration, tr, nil); err != nil {
		t.Fatalf("ServeAgentSession() error = %v", err)
	}
	if _, ok := manager.Get(registration.AgentID); ok {
		t.Fatal("session remained registered after transport EOF")
	}
	if len(conn.writes) != 0 {
		t.Fatalf("unexpected writes: %d", len(conn.writes))
	}
}
