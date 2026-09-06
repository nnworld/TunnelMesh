package server

import (
	"context"
	"errors"
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
