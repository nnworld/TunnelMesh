package server

import (
	"context"
	"errors"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// TestRegisterEnforcesMaxConnectionsPerAgent is the capacity guard: one Agent
// credential must not be able to pin an unbounded number of WebSockets on a node,
// because every connection carries its own writer goroutine and frame budget.
func TestRegisterEnforcesMaxConnectionsPerAgent(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{MaxConnectionsPerAgent: 2})
	ctx := context.Background()
	for _, connectionID := range []string{"conn-1", "conn-2"} {
		if _, err := manager.Register(ctx, AgentRegistration{AgentID: "agent-1", NodeID: "node-1", Epoch: 1, ConnectionID: connectionID}, newFakeTransport()); err != nil {
			t.Fatalf("Register(%s) error = %v", connectionID, err)
		}
	}
	_, err := manager.Register(ctx, AgentRegistration{AgentID: "agent-1", NodeID: "node-1", Epoch: 2, ConnectionID: "conn-3"}, newFakeTransport())
	if !errors.Is(err, ErrAgentConnectionCapacity) {
		t.Fatalf("Register() error = %v, want ErrAgentConnectionCapacity", err)
	}
	// A different Agent has its own budget, so a full neighbour cannot lock it out.
	if _, err := manager.Register(ctx, AgentRegistration{AgentID: "agent-2", NodeID: "node-1", Epoch: 1, ConnectionID: "conn-1"}, newFakeTransport()); err != nil {
		t.Fatalf("Register(agent-2) error = %v, want an independent budget", err)
	}
	// Replacing one of the existing connections is not growth, so fencing must still
	// win over the capacity check.
	if _, err := manager.Register(ctx, AgentRegistration{AgentID: "agent-1", NodeID: "node-1", Epoch: 3, ConnectionID: "conn-1"}, newFakeTransport()); err != nil {
		t.Fatalf("Register(replacement) error = %v", err)
	}
}

// TestAgentMetadataAckAdvertisesConfiguredCapacity keeps the number the Agent is
// told about equal to the number the Server enforces. An ACK that advertises more
// than the node allows teaches the Agent to open connections that are then refused.
func TestAgentMetadataAckAdvertisesConfiguredCapacity(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{MaxConnectionsPerAgent: 3})
	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-1", NodeID: "node-1", Epoch: 1, ConnectionID: "conn-1"}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	ack, err := session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{AgentID: "agent-1", Epoch: 1, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ack.MaxConnectionsPerAgent != 3 {
		t.Fatalf("ack.MaxConnectionsPerAgent = %d, want the configured 3", ack.MaxConnectionsPerAgent)
	}
	if got := manager.MaxConnectionsPerAgent(); got != 3 {
		t.Fatalf("MaxConnectionsPerAgent() = %d, want 3", got)
	}
}

// TestAgentSessionManagerDefaultsConnectionCapacity pins the default to the number
// the protocol already advertised, so enforcing it cannot break a pool that was
// configured to the documented Agent maximum.
func TestAgentSessionManagerDefaultsConnectionCapacity(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	if got := manager.MaxConnectionsPerAgent(); got != config.DefaultAgentMaxConnectionsPerAgent {
		t.Fatalf("MaxConnectionsPerAgent() = %d, want the historical %d", got, config.DefaultAgentMaxConnectionsPerAgent)
	}
}
