package server

import (
	"context"
	"errors"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

// TestAgentRelayTransportBoundsActiveStreamsPerAgent guards the node-local
// ceiling: max_concurrent_opens only bounds how many opens are being processed at
// once, so without a level cap one Client can pin an arbitrary number of live
// streams (and their Agent-side target connections) onto one Agent.
func TestAgentRelayTransportBoundsActiveStreamsPerAgent(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	transport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-capacity", NodeID: "node", Epoch: 1,
		Capabilities: []string{protocol.CapabilityStreamOpenResult},
	}, transport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{MaxActiveStreamsPerAgent: 1})
	defer mux.Close()

	first, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-capacity", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if frame := receiveAgentRelayFrame(t, transport); frame.Type != protocol.FrameOpenStream {
		t.Fatalf("first open sent frame type %d, want OPEN_STREAM", frame.Type)
	}

	second, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-capacity", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
	})
	if second != nil {
		_ = second.Close()
		t.Fatal("the second stream was accepted past the per-agent ceiling")
	}
	if !errors.Is(err, ErrAgentRelayStreamCapacity) {
		t.Fatalf("second open error = %v, want %v", err, ErrAgentRelayStreamCapacity)
	}
	if got := mux.ActiveStreams("agent-capacity", "legacy"); got != 1 {
		t.Fatalf("ActiveStreams() = %d, want the refused open to have registered nothing", got)
	}
}
