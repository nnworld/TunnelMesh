package server

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

func TestAgentRelayTransportLegacyWrappersNilReceiverFailClosed(t *testing.T) {
	var mux *AgentRelayTransport

	if err := mux.HandleAgentFrame("agent", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); !errors.Is(err, errAgentRelayClosed) {
		t.Fatalf("nil HandleAgentFrame() error = %v, want %v", err, errAgentRelayClosed)
	}

	// Legacy teardown is best-effort; a nil receiver must remain a safe no-op.
	mux.FailAgentGeneration("agent", 1)
}

func TestAgentRelayTransportOpenStreamResultWaitsForStrictFailure(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-strict-open", NodeID: "node", Epoch: 1,
		Capabilities: []string{protocol.CapabilityStreamOpenResult},
	}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()

	type openResult struct {
		stream io.ReadWriteCloser
		result relay.RelayOpenResult
		err    error
	}
	resultCh := make(chan openResult, 1)
	go func() {
		stream, result, openErr := mux.OpenStreamResult(context.Background(), relay.StreamRequest{
			AgentID: "agent-strict-open", StrictOpen: true, Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22, InitialWindow: 262144,
		})
		resultCh <- openResult{stream: stream, result: result, err: openErr}
	}()

	open := receiveAgentRelayFrame(t, agentTransport)
	if open.Type != protocol.FrameOpenStream || open.Flags&protocol.FlagStrictOpen == 0 || open.Window != 262144 {
		t.Fatalf("OPEN frame = %+v, want strict OPEN_STREAM with window 262144", open)
	}
	select {
	case got := <-resultCh:
		t.Fatalf("OpenStreamResult returned before OPEN_RESULT: %+v", got)
	default:
	}

	payload, err := protocol.EncodeOpenResultPayload(protocol.OpenResultPayload{
		Accepted: false, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeConnectionRefused,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mux.handleAgentFrameGeneration("agent-strict-open", session.serverGeneration, protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: open.StreamID, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-resultCh:
		if got.stream != nil || got.err != nil {
			t.Fatalf("OpenStreamResult result = stream %v, err %v, want nil stream and nil error", got.stream, got.err)
		}
		if got.result.Payload.Accepted || got.result.Payload.Stage != protocol.OpenResultStageConnect ||
			got.result.Payload.Code != protocol.OpenResultCodeConnectionRefused {
			t.Fatalf("OpenResult = %+v, want connection_refused failure", got.result.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for strict OPEN_RESULT")
	}
}

func TestAgentRelayTransportStrictOpenRejectsLegacyAgentWithoutSending(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	_, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-legacy-open", NodeID: "node", Epoch: 1}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	stream, result, openErr := mux.OpenStreamResult(ctx, relay.StreamRequest{
		AgentID: "agent-legacy-open", StrictOpen: true, Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
	})
	if stream != nil || openErr != nil || result.Payload.Accepted || result.Payload.Code != protocol.OpenResultCodeUnsupportedCapability {
		t.Fatalf("OpenStreamResult stream=%v err=%v result=%+v, want unsupported capability", stream, openErr, result.Payload)
	}
	select {
	case frame := <-agentTransport.sent:
		t.Fatalf("legacy Agent received frame=%+v, want no strict OPEN", frame)
	default:
	}
}

func TestAgentRelayTransportPropagatesWindowControlFrames(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-window", NodeID: "node", Epoch: 1}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()

	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-window", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22, InitialWindow: 262144,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	open := receiveAgentRelayFrame(t, agentTransport)
	if open.Type != protocol.FrameOpenStream || open.Window != 262144 {
		t.Fatalf("OPEN=%+v, want the requested initial window 262144", open)
	}

	controlWriter, ok := stream.(interface{ WriteControl(protocol.Frame) error })
	if !ok {
		t.Fatal("relay stream does not support control writes")
	}
	if err := controlWriter.WriteControl(protocol.Frame{Type: protocol.FrameWindowUpdate, Window: 8}); err != nil {
		t.Fatal(err)
	}
	forwarded := receiveAgentRelayFrame(t, agentTransport)
	if forwarded.Type != protocol.FrameWindowUpdate || forwarded.StreamID != open.StreamID || forwarded.Window != 8 {
		t.Fatalf("forwarded WINDOW_UPDATE=%+v", forwarded)
	}
	if err := mux.handleAgentFrameGeneration("agent-window", session.serverGeneration, protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: open.StreamID, Window: 16,
	}); err != nil {
		t.Fatal(err)
	}
	controlReader, ok := stream.(interface{ ReadControl() (protocol.Frame, bool) })
	if !ok {
		t.Fatal("relay stream does not support control reads")
	}
	control, ok := controlReader.ReadControl()
	if !ok || control.Type != protocol.FrameWindowUpdate || control.Window != 16 {
		t.Fatalf("Agent WINDOW_UPDATE control=%+v ok=%v", control, ok)
	}
}

func TestAgentRelayTransportOpenStreamResultPropagatesStableFailureCodes(t *testing.T) {
	tests := []struct {
		stage protocol.OpenResultStage
		code  protocol.OpenResultCode
	}{
		{protocol.OpenResultStageAuthorization, protocol.OpenResultCodeForbidden},
		{protocol.OpenResultStageRelay, protocol.OpenResultCodeAgentOffline},
		{protocol.OpenResultStageQueue, protocol.OpenResultCodeQueueFull},
		{protocol.OpenResultStageRelay, protocol.OpenResultCodeTimeout},
		{protocol.OpenResultStageDNS, protocol.OpenResultCodeNetworkUnreachable},
		{protocol.OpenResultStageDNS, protocol.OpenResultCodeHostUnreachable},
		{protocol.OpenResultStageConnect, protocol.OpenResultCodeConnectionRefused},
		{protocol.OpenResultStageSelection, protocol.OpenResultCodeUnsupportedCapability},
		{protocol.OpenResultStageProtocol, protocol.OpenResultCodeInternalError},
	}
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			manager := NewAgentSessionManager(AgentSessionConfig{})
			agentTransport := newFakeTransport()
			session, err := manager.Register(context.Background(), AgentRegistration{
				AgentID: "agent-stable", NodeID: "node", Epoch: 1,
				Capabilities: []string{protocol.CapabilityStreamOpenResult},
			}, agentTransport)
			if err != nil {
				t.Fatal(err)
			}
			mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
			defer mux.Close()
			resultCh := make(chan relay.RelayOpenResult, 1)
			go func() {
				_, result, openErr := mux.OpenStreamResult(context.Background(), relay.StreamRequest{
					AgentID: "agent-stable", StrictOpen: true, Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
				})
				if openErr != nil {
					t.Errorf("OpenStreamResult error = %v", openErr)
				}
				resultCh <- result
			}()
			open := receiveAgentRelayFrame(t, agentTransport)
			payload, err := protocol.EncodeOpenResultPayload(protocol.OpenResultPayload{Accepted: false, Stage: test.stage, Code: test.code})
			if err != nil {
				t.Fatal(err)
			}
			if err := mux.handleAgentFrameGeneration("agent-stable", session.serverGeneration, protocol.Frame{
				Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: open.StreamID, Payload: payload,
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-resultCh:
				if result.Payload.Accepted || result.Payload.Stage != test.stage || result.Payload.Code != test.code {
					t.Fatalf("result=%+v, want %s/%s", result.Payload, test.stage, test.code)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for OPEN_RESULT")
			}
		})
	}
}

func TestAgentRelayTransportOpenStreamResultTimeoutResetsOnlyStream(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-open-timeout", NodeID: "node", Epoch: 1,
		Capabilities: []string{protocol.CapabilityStreamOpenResult},
	}, agentTransport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stream, result, err := mux.OpenStreamResult(ctx, relay.StreamRequest{
		AgentID: "agent-open-timeout", StrictOpen: true, Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
	})
	if stream != nil || err == nil || result.Payload.Accepted || result.Payload.Code != protocol.OpenResultCodeTimeout {
		t.Fatalf("stream=%v result=%+v err=%v, want timeout failure", stream, result.Payload, err)
	}
	receiveAgentRelayFrame(t, agentTransport)
	if frame := receiveAgentRelayFrame(t, agentTransport); frame.Type != protocol.FrameReset {
		t.Fatalf("timeout response=%+v, want RESET", frame)
	}
}

func TestAgentRelayTransportOpenStreamResultDuplicateAndEarlyDataAreIsolated(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-open-duplicate", NodeID: "node", Epoch: 1,
		Capabilities: []string{protocol.CapabilityStreamOpenResult},
	}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()
	type openResult struct {
		stream io.ReadWriteCloser
		result relay.RelayOpenResult
	}
	resultCh := make(chan openResult, 1)
	go func() {
		stream, result, openErr := mux.OpenStreamResult(context.Background(), relay.StreamRequest{
			AgentID: "agent-open-duplicate", StrictOpen: true, Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
		})
		if openErr != nil {
			t.Errorf("OpenStreamResult error = %v", openErr)
		}
		resultCh <- openResult{stream: stream, result: result}
	}()
	open := receiveAgentRelayFrame(t, agentTransport)
	if err := mux.handleAgentFrameGeneration("agent-open-duplicate", session.serverGeneration, protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("early"),
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := protocol.EncodeOpenResultPayload(protocol.OpenResultPayload{Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK})
	if err != nil {
		t.Fatal(err)
	}
	if err := mux.handleAgentFrameGeneration("agent-open-duplicate", session.serverGeneration, protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: open.StreamID, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	var stream io.ReadWriteCloser
	select {
	case got := <-resultCh:
		stream = got.stream
		if stream == nil || !got.result.Payload.Accepted || got.result.Payload.Code != protocol.OpenResultCodeOK {
			t.Fatalf("stream=%v result=%+v, want successful stream", stream, got.result.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for successful OPEN_RESULT")
	}
	buffer := make([]byte, len("early"))
	if _, err := io.ReadFull(stream, buffer); err != nil || string(buffer) != "early" {
		t.Fatalf("early data=%q err=%v", buffer, err)
	}
	// A duplicate terminal result must reset only this stream.
	if err := mux.handleAgentFrameGeneration("agent-open-duplicate", session.serverGeneration, protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: open.StreamID, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if frame := receiveAgentRelayFrame(t, agentTransport); frame.Type != protocol.FrameReset || frame.StreamID != open.StreamID {
		t.Fatalf("duplicate result response=%+v, want stream RESET", frame)
	}
}

func TestAgentRelayTransportAllocatesIndependentWireIDsAndFencesReconnectGeneration(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	firstTransport := newFakeTransport()
	firstSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-mux", NodeID: "node-a", Epoch: 1}, firstTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
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

func TestAgentRelayTransportResolvesDynamicAgentIDCaseInsensitively(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-TFJXVxtPivP8KnXb", NodeID: "node-a", Epoch: 1,
	}, agentTransport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()

	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID:                "agent-tfjxvxtpivp8knxb",
		CaseInsensitiveAgentID: true,
		Protocol:               "http",
		TargetHost:             "127.0.0.1",
		TargetPort:             3000,
		TargetScheme:           "https",
		HostHeader:             "service.internal.example.com",
		TLSServerName:          "service.internal.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	open := receiveAgentRelayFrame(t, agentTransport)
	payload, err := protocol.DecodeStreamOpenPayload(open.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if payload.AgentID != "agent-TFJXVxtPivP8KnXb" || payload.TargetHost != "127.0.0.1" || payload.TargetPort != 3000 ||
		payload.TargetScheme != "https" || payload.HostHeader != "service.internal.example.com" ||
		payload.TLSServerName != "service.internal.example.com" {
		t.Fatalf("open payload = %#v", payload)
	}
}

func TestAgentRelayTransportKeysStreamsByAgentConnection(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	firstTransport := newFakeTransport()
	first, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-pool", NodeID: "node-a", ConnectionID: "conn-a", ConnectionEpoch: 1, Epoch: 1,
	}, firstTransport)
	if err != nil {
		t.Fatal(err)
	}
	secondTransport := newFakeTransport()
	second, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-pool", NodeID: "node-b", ConnectionID: "conn-b", ConnectionEpoch: 1, Epoch: 1,
	}, secondTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()

	firstStream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-pool", TargetConnectionID: "conn-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer firstStream.Close()
	secondStream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-pool", TargetConnectionID: "conn-b", Protocol: "tcp", TargetHost: "10.0.0.2", TargetPort: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer secondStream.Close()
	firstOpen := receiveAgentRelayFrame(t, firstTransport)
	secondOpen := receiveAgentRelayFrame(t, secondTransport)
	if firstOpen.StreamID != 1 || secondOpen.StreamID != 1 {
		t.Fatalf("wire IDs = %d, %d; want both 1", firstOpen.StreamID, secondOpen.StreamID)
	}

	if err := mux.handleAgentFrameGeneration("agent-pool", first.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: firstOpen.StreamID, Payload: []byte("first")}); err != nil {
		t.Fatal(err)
	}
	if err := mux.handleAgentFrameGeneration("agent-pool", second.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: secondOpen.StreamID, Payload: []byte("second")}); err != nil {
		t.Fatal(err)
	}
	assertAgentRelayData(t, firstStream, "first")
	assertAgentRelayData(t, secondStream, "second")

	mux.failAgentGeneration("agent-pool", first.serverGeneration)
	if _, err := firstStream.Read(make([]byte, 1)); !errors.Is(err, relay.ErrNodeDisconnected) {
		t.Fatalf("closed connection Read() error = %v, want ErrNodeDisconnected", err)
	}
	if _, err := secondStream.Write([]byte("still-alive")); err != nil {
		t.Fatalf("other connection Write() error = %v", err)
	}
}

func TestAgentRelayTransportRejectsAmbiguousCaseInsensitiveAgentID(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	for _, id := range []string{"agent-Abc", "agent-abc"} {
		if _, err := manager.Register(context.Background(), AgentRegistration{
			AgentID: id, NodeID: "node-" + id, Epoch: 1,
		}, newFakeTransport()); err != nil {
			t.Fatal(err)
		}
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()

	if _, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-abc", CaseInsensitiveAgentID: true,
		Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 3000,
	}); !errors.Is(err, ErrAmbiguousAgentID) {
		t.Fatalf("OpenStream() error = %v, want ErrAmbiguousAgentID", err)
	}
}

func assertAgentRelayData(t *testing.T, stream io.ReadWriteCloser, want string) {
	t.Helper()
	buffer := make([]byte, len(want))
	if _, err := io.ReadFull(stream, buffer); err != nil || string(buffer) != want {
		t.Fatalf("data = %q, err = %v; want %q", buffer, err, want)
	}
}

func TestAgentRelayTransportSameEpochReplacementFencesDelayedOldCallbacksAndTeardown(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	oldTransport := newFakeTransport()
	oldSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-same-epoch", NodeID: "node-old", Epoch: 1}, oldTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
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
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
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
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
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
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
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
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
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
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
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
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()
	mux.allocators[agentRelayGeneration{
		agentID: "agent-exhaust", connectionID: firstSession.ConnectionID, connectionEpoch: firstSession.ConnectionEpoch, serverGeneration: firstSession.serverGeneration,
	}] = &agentRelayIDAllocator{next: math.MaxUint32 - 1}
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
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
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

func TestAgentRelayStreamAdvertisesDefaultWindowAndReleasesItOnRead(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-window-default", NodeID: "node", Epoch: 1}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()

	// Server-originated opens must advertise a receive window: without one the
	// Agent sends unbounded and overflows the inbound frame buffer.
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-window-default", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	open := receiveAgentRelayFrame(t, agentTransport)
	if open.Type != protocol.FrameOpenStream || open.Window != protocol.DefaultServerReceiveWindow {
		t.Fatalf("OPEN=%+v, want window %d", open, protocol.DefaultServerReceiveWindow)
	}

	payload := make([]byte, protocol.MaxStreamFrame)
	for i := 0; i < 4; i++ {
		if err := mux.handleAgentFrameGeneration("agent-window-default", session.serverGeneration, protocol.Frame{
			Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	buffer := make([]byte, protocol.MaxStreamFrame)
	for i := 0; i < 4; i++ {
		n, readErr := stream.Read(buffer)
		if readErr != nil || n != len(buffer) {
			t.Fatalf("Read=%d,%v want %d,nil", n, readErr, len(buffer))
		}
	}
	update := receiveAgentRelayFrame(t, agentTransport)
	if update.Type != protocol.FrameWindowUpdate || update.StreamID != open.StreamID || update.Window < protocol.DefaultWindowUpdateThreshold {
		t.Fatalf("WINDOW_UPDATE=%+v, want at least %d bytes released", update, protocol.DefaultWindowUpdateThreshold)
	}
}

func TestAgentRelayStreamWriteBlocksOnAgentSendWindowAndResumesOnUpdate(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-send-window", NodeID: "node", Epoch: 1}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()

	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-send-window", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
	})
	if err != nil {
		t.Fatal(err)
	}
	open := receiveAgentRelayFrame(t, agentTransport)

	// Exhaust the credit the Agent grants by default.
	full := make([]byte, protocol.DefaultAgentReceiveWindow)
	if _, err := stream.Write(full); err != nil {
		t.Fatalf("Write within window failed: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := stream.Write([]byte("x"))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Write beyond the Agent window returned %v, want it to block", err)
	case <-time.After(150 * time.Millisecond):
	}

	if err := mux.handleAgentFrameGeneration("agent-send-window", session.serverGeneration, protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: open.StreamID, Window: 4096,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("blocked Write after WINDOW_UPDATE: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked Write did not resume after WINDOW_UPDATE")
	}
}

func TestAgentRelayStreamCloseWakesBlockedWrite(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-send-window-close", NodeID: "node", Epoch: 1}, agentTransport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()

	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-send-window-close", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
	})
	if err != nil {
		t.Fatal(err)
	}
	receiveAgentRelayFrame(t, agentTransport)
	if _, err := stream.Write(make([]byte, protocol.DefaultAgentReceiveWindow)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := stream.Write([]byte("x"))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked Write succeeded after Close, want error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked Write did not wake on Close")
	}
}

// TestAgentRelayStreamAcceptsManySmallFramesWithinWindow guards the byte/slot
// unit mismatch: the receive window is accounted in bytes, so a peer that
// stays inside its window using many small DATA frames must never be reset.
func TestAgentRelayStreamAcceptsManySmallFramesWithinWindow(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-small-frames", NodeID: "node", Epoch: 1}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()

	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-small-frames", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	open := receiveAgentRelayFrame(t, agentTransport)

	// Drain control traffic in the background so WINDOW_UPDATE bookkeeping does
	// not stall, and record any RESET the relay emits.
	var resetSeen atomic.Bool
	stopDrain := make(chan struct{})
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			select {
			case frame := <-agentTransport.sent:
				if frame.Type == protocol.FrameReset {
					resetSeen.Store(true)
				}
			case <-stopDrain:
				return
			}
		}
	}()
	defer func() {
		close(stopDrain)
		<-drainDone
	}()

	const frameSize = 1024
	frames := int(protocol.DefaultServerReceiveWindow/frameSize) - 1
	for i := 0; i < frames; i++ {
		payload := make([]byte, frameSize)
		for j := range payload {
			payload[j] = byte(i)
		}
		if err := mux.handleAgentFrameGeneration("agent-small-frames", session.serverGeneration, protocol.Frame{
			Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: payload,
		}); err != nil {
			t.Fatalf("frame %d rejected: %v", i, err)
		}
	}

	// A consumer draining in chunks smaller than a frame must still see every
	// byte intact and in order. The odd size keeps partial-frame boundaries in
	// play without turning the assertion itself into a benchmark.
	got := make([]byte, 0, frames*frameSize)
	small := make([]byte, 63)
	for len(got) < frames*frameSize {
		n, readErr := stream.Read(small)
		if readErr != nil {
			t.Fatalf("Read after %d bytes: %v", len(got), readErr)
		}
		if n == 0 {
			t.Fatalf("Read returned 0,nil after %d bytes", len(got))
		}
		got = append(got, small[:n]...)
	}
	for i := 0; i < frames; i++ {
		for j := 0; j < frameSize; j++ {
			if got[i*frameSize+j] != byte(i) {
				t.Fatalf("byte %d = %d, want %d", i*frameSize+j, got[i*frameSize+j], byte(i))
			}
		}
	}
	if resetSeen.Load() {
		t.Fatal("relay emitted RESET for a peer that stayed inside its receive window")
	}
}

// TestAgentRelayStreamSurvivesUndrainedWindowUpdates guards the bulk-upload
// kill switch: WINDOW_UPDATE credit is applied to the send state before the
// frame is mirrored into controlCh, and the WebSSH/SFTP consumer never polls
// ReadControl. Failing the stream when that mirror queue filled turned every
// upload larger than the control queue into a dead channel.
func TestAgentRelayStreamSurvivesUndrainedWindowUpdates(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-window-flood", NodeID: "node", Epoch: 1}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()

	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-window-flood", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	open := receiveAgentRelayFrame(t, agentTransport)

	// More updates than the control queue can hold, with nobody draining it.
	updates := 40
	for i := 0; i < updates; i++ {
		if err := mux.handleAgentFrameGeneration("agent-window-flood", session.serverGeneration, protocol.Frame{
			Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: open.StreamID, Window: 1024,
		}); err != nil {
			t.Fatalf("WINDOW_UPDATE %d rejected: %v", i, err)
		}
	}

	// The stream must still be writable: the credit is authoritative, the
	// mirrored frame is not.
	payload := make([]byte, 4096)
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("Write after %d undrained WINDOW_UPDATEs: %v", updates, err)
	}
	for {
		frame, ok := agentTransport.tryReceive()
		if !ok {
			break
		}
		if frame.Type == protocol.FrameReset {
			t.Fatalf("unexpected RESET after undrained WINDOW_UPDATE flood: %+v", frame)
		}
	}
}

// A window the Agent can never refill by one whole frame would stall the stream
// forever, because waiting for credit has no timeout. The Server clamps it to the
// window it can actually honour instead of honouring the number it was given.
func TestAgentRelayClampsUndersizedPeerWindow(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-clamp", NodeID: "node", Epoch: 1}, agentTransport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{})
	defer mux.Close()
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-clamp", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22, InitialWindow: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	open := receiveAgentRelayFrame(t, agentTransport)
	if open.Window != protocol.DefaultServerReceiveWindow {
		t.Fatalf("OPEN window = %d, want the clamped %d", open.Window, protocol.DefaultServerReceiveWindow)
	}
}

// `server.stream.initial_window` must reach the data plane: it is both the credit
// advertised to the Agent and the size of the buffer that enforces it.
func TestAgentRelayHonoursConfiguredWindow(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	agentTransport := newFakeTransport()
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-configured", NodeID: "node", Epoch: 1}, agentTransport); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager, AgentRelayWindowConfig{AdvertisedWindow: 262144, UpdateThreshold: 131072})
	defer mux.Close()
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-configured", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	open := receiveAgentRelayFrame(t, agentTransport)
	if open.Window != 262144 {
		t.Fatalf("OPEN window = %d, want the configured 262144", open.Window)
	}
	relayStream, ok := stream.(*agentRelayStream)
	if !ok {
		t.Fatalf("stream type %T", stream)
	}
	if relayStream.receiveBudget != 262144 {
		t.Fatalf("receiveBudget = %d, want it sized from the configured window", relayStream.receiveBudget)
	}
	if got := relayStream.transport.windows.updateThreshold(uint32(relayStream.receiveBudget)); got != 131072 {
		t.Fatalf("updateThreshold = %d, want the configured 131072", got)
	}
}

// A configured threshold that would leave less than one frame of headroom is not
// honoured, because a sender that cannot fit a frame has no way to ask for credit.
func TestAgentRelayRejectsWindowWithoutFrameHeadroom(t *testing.T) {
	windows := AgentRelayWindowConfig{AdvertisedWindow: 262144, UpdateThreshold: 262144 - 1024}
	if got := windows.updateThreshold(windows.advertisedWindow()); got != protocol.DefaultWindowUpdateThreshold {
		t.Fatalf("updateThreshold = %d, want the safe default", got)
	}
	if got := (AgentRelayWindowConfig{AdvertisedWindow: 1 << 30}).advertisedWindow(); got != protocol.DefaultServerReceiveWindow {
		t.Fatalf("advertisedWindow = %d, want the cap for an unverifiable request", got)
	}
}
