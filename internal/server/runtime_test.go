package server

import (
	"context"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestNewServerRuntimeWiresAgentMetadataPersistence(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:server-runtime?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := runtime.AgentSessions.Register(context.Background(), AgentRegistration{AgentID: "agent-runtime", NodeID: "node-runtime", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	ack, err := session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{
		AgentID: "agent-runtime", NodeID: "node-runtime", Epoch: 1, Revision: 1,
		Items: []protocol.AgentMetadataItem{{Name: "region", Source: "env", Value: "east"}},
	})
	if err != nil || !ack.Accepted {
		t.Fatalf("metadata ack=%+v err=%v", ack, err)
	}
	metadata, err := db.Metadata().Get(context.Background(), "agent-runtime")
	if err != nil || metadata.AgentID != "agent-runtime" || metadata.NodeID != "node-runtime" {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
}
