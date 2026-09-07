package server

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

func TestAgentRelayTransportAllocatesIndependentWireIDsAndFencesReconnectGeneration(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	firstTransport := newFakeTransport()
	firstSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-mux", NodeID: "node-a", Epoch: 1}, firstTransport)
	if err != nil {
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
	newSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-mux", NodeID: "node-b", Epoch: 2}, newTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux.failAgentGeneration("agent-mux", firstSession.serverGeneration)
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
	if err := mux.handleAgentFrameGeneration("agent-mux", firstSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("stale-data")}); err != nil {
		t.Fatal(err)
	}
	if err := mux.handleAgentFrameGeneration("agent-mux", newSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("current-data")}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("current-data"))
	if _, err := io.ReadFull(current, buffer); err != nil || string(buffer) != "current-data" {
		t.Fatalf("current generation data = %q, err = %v", buffer, err)
	}
	_ = current.Close()
}

func TestAgentRelayTransportSameEpochReplacementFencesDelayedOldCallbacksAndTeardown(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	oldTransport := newFakeTransport()
	oldSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-same-epoch", NodeID: "node-old", Epoch: 1}, oldTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	oldStream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-same-epoch", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	oldOpen := receiveAgentRelayFrame(t, oldTransport)
	manager.RemoveSession("agent-same-epoch", oldSession)

	newTransport := newFakeTransport()
	newSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-same-epoch", NodeID: "node-new", Epoch: 1}, newTransport)
	if err != nil {
		t.Fatal(err)
	}
	if newSession.serverGeneration == oldSession.serverGeneration {
		t.Fatal("same-epoch replacement reused the old server connection generation")
	}
	newStream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-same-epoch", Protocol: "tcp", TargetHost: "10.0.0.9", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	newOpen := receiveAgentRelayFrame(t, newTransport)
	if oldOpen.StreamID != 1 || newOpen.StreamID != 1 {
		t.Fatalf("per-connection wire IDs = old %d, new %d; want independent ID 1 allocators", oldOpen.StreamID, newOpen.StreamID)
	}
	legacyEpoch := int64(1)
	if err := mux.HandleAgentFrame("agent-same-epoch", legacyEpoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: newOpen.StreamID, Payload: []byte("legacy-must-not-route")}); !errors.Is(err, ErrServerGeneration) {
		t.Fatalf("legacy same-epoch callback error = %v, want ErrServerGeneration", err)
	}
	mux.FailAgentGeneration("agent-same-epoch", legacyEpoch)

	if err := mux.handleAgentFrameGeneration("agent-same-epoch", oldSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: oldOpen.StreamID, Payload: []byte("stale")}); err != nil {
		t.Fatal(err)
	}
	mux.failAgentGeneration("agent-same-epoch", oldSession.serverGeneration)
	if _, err := oldStream.Read(make([]byte, 1)); !errors.Is(err, relay.ErrNodeDisconnected) {
		t.Fatalf("old stream Read() error = %v, want ErrNodeDisconnected", err)
	}
	if err := mux.handleAgentFrameGeneration("agent-same-epoch", newSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: newOpen.StreamID, Payload: []byte("current")}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("current"))
	if _, err := io.ReadFull(newStream, buffer); err != nil || string(buffer) != "current" {
		t.Fatalf("new same-epoch stream data = %q, err = %v", buffer, err)
	}
}

func TestAgentRelayTransportLegacyEpochCallbackRoutesUniqueCurrentSession(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	transport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-legacy-api", NodeID: "node", Epoch: 7}, transport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-legacy-api", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	open := receiveAgentRelayFrame(t, transport)
	legacyEpoch := int64(7)
	if err := mux.HandleAgentFrame("agent-legacy-api", legacyEpoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("legacy-data")}); err != nil {
		t.Fatalf("legacy callback error = %v, want nil", err)
	}
	buffer := make([]byte, len("legacy-data"))
	if _, err := io.ReadFull(stream, buffer); err != nil || string(buffer) != "legacy-data" {
		t.Fatalf("legacy callback data = %q, err = %v", buffer, err)
	}
}

func TestAgentRelayTransportConcurrentOldTeardownAndSameEpochRegistration(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	oldSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-generation-race", NodeID: "node-old", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	manager.RemoveSession("agent-generation-race", oldSession)

	start := make(chan struct{})
	type registrationResult struct {
		session   *AgentSession
		transport *fakeTransport
		err       error
	}
	result := make(chan registrationResult, 1)
	var teardown sync.WaitGroup
	teardown.Add(1)
	go func() {
		defer teardown.Done()
		<-start
		mux.failAgentGeneration("agent-generation-race", oldSession.serverGeneration)
	}()
	go func() {
		<-start
		transport := newFakeTransport()
		session, registerErr := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-generation-race", NodeID: "node-new", Epoch: 1}, transport)
		result <- registrationResult{session: session, transport: transport, err: registerErr}
	}()
	close(start)
	registered := <-result
	teardown.Wait()
	if registered.err != nil {
		t.Fatal(registered.err)
	}
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-generation-race", Protocol: "tcp", TargetHost: "10.0.0.10", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	open := receiveAgentRelayFrame(t, registered.transport)
	if err := mux.handleAgentFrameGeneration("agent-generation-race", registered.session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("alive")}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("alive"))
	if _, err := io.ReadFull(stream, buffer); err != nil || string(buffer) != "alive" {
		t.Fatalf("replacement stream data = %q, err = %v", buffer, err)
	}
}

func TestAgentRelayTransportResetOverridesPriorRemoteHalfClose(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-reset", NodeID: "node-reset", Epoch: 1}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-reset", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	open := receiveAgentRelayFrame(t, agentTransport)
	if err := mux.handleAgentFrameGeneration("agent-reset", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read() after HALF_CLOSE error = %v, want EOF", err)
	}
	if err := mux.handleAgentFrameGeneration("agent-reset", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("must-not-send")); !errors.Is(err, protocol.ErrStreamReset) {
		t.Fatalf("Write() after RESET error = %v, want ErrStreamReset", err)
	}
}

func TestAgentRelayTransportRejectsOldGenerationDataAfterReplacementRegistration(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	oldTransport := newFakeTransport()
	oldSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-fence", NodeID: "node-old", Epoch: 1}, oldTransport)
	if err != nil {
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
	if err := mux.handleAgentFrameGeneration("agent-fence", oldSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("stale-data")}); err != nil {
		t.Fatal(err)
	}
	mux.failAgentGeneration("agent-fence", oldSession.serverGeneration)
	buffer := make([]byte, len("stale-data"))
	if n, err := stream.Read(buffer); n != 0 || !errors.Is(err, relay.ErrNodeDisconnected) {
		t.Fatalf("Read() after replacement = (%d, %v), payload %q; want (0, ErrNodeDisconnected)", n, err, buffer[:n])
	}
}

func TestAgentRelayTransportCloseDoesNotEchoRemoteReset(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-no-echo", NodeID: "node-no-echo", Epoch: 1}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-no-echo", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	open := receiveAgentRelayFrame(t, agentTransport)
	if err := mux.handleAgentFrameGeneration("agent-no-echo", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
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
	firstSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-exhaust", NodeID: "node-old", Epoch: 1}, firstTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	mux.allocators[agentRelayGeneration{agentID: "agent-exhaust", serverGeneration: firstSession.serverGeneration}] = &agentRelayIDAllocator{next: math.MaxUint32 - 1}
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
	secondSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-exhaust", NodeID: "node-new", Epoch: 2}, secondTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux.failAgentGeneration("agent-exhaust", firstSession.serverGeneration)
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-exhaust", Protocol: "tcp", TargetHost: "10.0.0.10", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	if frame := receiveAgentRelayFrame(t, secondTransport); frame.StreamID != 1 {
		t.Fatalf("new epoch wire ID = %d, want 1", frame.StreamID)
	}
	if secondSession.serverGeneration == firstSession.serverGeneration {
		t.Fatal("new epoch reused the old server connection generation")
	}
	_ = stream.Close()
}

func TestAgentRelayTransportRejectsDataAfterRemoteHalfClose(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-half-data", NodeID: "node-half-data", Epoch: 1}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-half-data", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	open := receiveAgentRelayFrame(t, agentTransport)
	if err := mux.handleAgentFrameGeneration("agent-half-data", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
		t.Fatal(err)
	}
	if err := mux.handleAgentFrameGeneration("agent-half-data", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("after-eof")}); err != nil {
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
