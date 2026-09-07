package server

import (
	"context"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

func TestAgentRelayTransportAllocatesIndependentWireIDsAndFencesReconnectGeneration(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	firstTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-mux", NodeID: "node-a", Epoch: 1}, firstTransport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	first, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-mux", StreamID: 7, Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-mux", StreamID: 7, Protocol: "tcp", TargetHost: "10.0.0.9", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	firstOpen := receiveAgentRelayFrame(t, firstTransport)
	secondOpen := receiveAgentRelayFrame(t, firstTransport)
	if firstOpen.StreamID == 0 || secondOpen.StreamID == 0 || firstOpen.StreamID == secondOpen.StreamID {
		t.Fatalf("Agent wire IDs = %d and %d, want distinct non-zero IDs", firstOpen.StreamID, secondOpen.StreamID)
	}

	newTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-mux", NodeID: "node-b", Epoch: 2}, newTransport); err != nil {
		t.Fatal(err)
	}
	mux.FailAgentGeneration("agent-mux", 1)
	if _, err := first.Read(make([]byte, 1)); !errors.Is(err, relay.ErrNodeDisconnected) {
		t.Fatalf("old generation Read() error = %v, want ErrNodeDisconnected", err)
	}
	if _, err := second.Write([]byte("stale")); !errors.Is(err, relay.ErrNodeDisconnected) {
		t.Fatalf("old generation Write() error = %v, want ErrNodeDisconnected", err)
	}

	current, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-mux", Protocol: "tcp", TargetHost: "10.0.0.10", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	currentOpen := receiveAgentRelayFrame(t, newTransport)
	if err := mux.HandleAgentFrame("agent-mux", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("stale-data")}); err != nil {
		t.Fatal(err)
	}
	if err := mux.HandleAgentFrame("agent-mux", 2, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("current-data")}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("current-data"))
	if _, err := io.ReadFull(current, buffer); err != nil || string(buffer) != "current-data" {
		t.Fatalf("current generation data = %q, err = %v", buffer, err)
	}
	_ = current.Close()
}

func TestAgentRelayTransportResetOverridesPriorRemoteHalfClose(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-reset", NodeID: "node-reset", Epoch: 1}, agentTransport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-reset", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	open := receiveAgentRelayFrame(t, agentTransport)
	if err := mux.HandleAgentFrame("agent-reset", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read() after HALF_CLOSE error = %v, want EOF", err)
	}
	if err := mux.HandleAgentFrame("agent-reset", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("must-not-send")); !errors.Is(err, protocol.ErrStreamReset) {
		t.Fatalf("Write() after RESET error = %v, want ErrStreamReset", err)
	}
}

func TestAgentRelayTransportRejectsOldGenerationDataAfterReplacementRegistration(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	oldTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-fence", NodeID: "node-old", Epoch: 1}, oldTransport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-fence", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	open := receiveAgentRelayFrame(t, oldTransport)
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-fence", NodeID: "node-new", Epoch: 2}, newFakeTransport()); err != nil {
		t.Fatal(err)
	}
	if err := mux.HandleAgentFrame("agent-fence", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("stale-data")}); err != nil {
		t.Fatal(err)
	}
	mux.FailAgentGeneration("agent-fence", 1)
	buffer := make([]byte, len("stale-data"))
	if n, err := stream.Read(buffer); n != 0 || !errors.Is(err, relay.ErrNodeDisconnected) {
		t.Fatalf("Read() after replacement = (%d, %v), payload %q; want (0, ErrNodeDisconnected)", n, err, buffer[:n])
	}
}

func TestAgentRelayTransportCloseDoesNotEchoRemoteReset(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-no-echo", NodeID: "node-no-echo", Epoch: 1}, agentTransport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-no-echo", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	open := receiveAgentRelayFrame(t, agentTransport)
	if err := mux.HandleAgentFrame("agent-no-echo", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-agentTransport.sent:
		t.Fatalf("Close() echoed terminal Agent frame: %+v", frame)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAgentRelayTransportRejectsWireIDWrapAndResetsForNewEpoch(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	firstTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-exhaust", NodeID: "node-old", Epoch: 1}, firstTransport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	mux.allocators[agentRelayGeneration{agentID: "agent-exhaust", epoch: 1}] = &agentRelayIDAllocator{next: math.MaxUint32 - 1}
	last, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-exhaust", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	if frame := receiveAgentRelayFrame(t, firstTransport); frame.StreamID != math.MaxUint32 {
		t.Fatalf("last wire ID = %d, want MaxUint32", frame.StreamID)
	}
	_ = last.Close()
	_ = receiveAgentRelayFrame(t, firstTransport)
	if stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-exhaust", Protocol: "tcp", TargetHost: "10.0.0.9", TargetPort: 22}); err == nil {
		_ = stream.Close()
		t.Fatal("OpenStream reused a retired wire ID after MaxUint32")
	}

	secondTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-exhaust", NodeID: "node-new", Epoch: 2}, secondTransport); err != nil {
		t.Fatal(err)
	}
	mux.FailAgentGeneration("agent-exhaust", 1)
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-exhaust", Protocol: "tcp", TargetHost: "10.0.0.10", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	if frame := receiveAgentRelayFrame(t, secondTransport); frame.StreamID != 1 {
		t.Fatalf("new epoch wire ID = %d, want 1", frame.StreamID)
	}
	_ = stream.Close()
}

func TestAgentRelayTransportRejectsDataAfterRemoteHalfClose(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-half-data", NodeID: "node-half-data", Epoch: 1}, agentTransport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-half-data", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	open := receiveAgentRelayFrame(t, agentTransport)
	if err := mux.HandleAgentFrame("agent-half-data", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
		t.Fatal(err)
	}
	if err := mux.HandleAgentFrame("agent-half-data", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("after-eof")}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("must-not-send")); !errors.Is(err, protocol.ErrInvalidFrame) {
		t.Fatalf("Write() after DATA following HALF_CLOSE error = %v, want ErrInvalidFrame", err)
	}
	if frame := receiveAgentRelayFrame(t, agentTransport); frame.Type != protocol.FrameReset || frame.StreamID != open.StreamID {
		t.Fatalf("Agent protocol-error response = %+v, want RESET", frame)
	}
}

func receiveAgentRelayFrame(t *testing.T, transport *fakeTransport) protocol.Frame {
	t.Helper()
	select {
	case frame := <-transport.sent:
		return frame
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Agent relay frame")
		return protocol.Frame{}
	}
}
