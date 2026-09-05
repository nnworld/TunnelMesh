package server

import (
	"context"
	"errors"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"testing"
	"time"
)

type fakeTransport struct {
	sent   chan protocol.Frame
	closed chan struct{}
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{sent: make(chan protocol.Frame, 8), closed: make(chan struct{})}
}
func (f *fakeTransport) Send(fr protocol.Frame) error {
	select {
	case f.sent <- fr:
		return nil
	default:
		return ErrBackpressure
	}
}
func (f *fakeTransport) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}

func TestAgentSessionRegistrationNegotiatesAndHeartbeats(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{SupportedCapabilities: []string{"tcp", "udp"}})
	tr := newFakeTransport()
	s, err := m.Register(context.Background(), AgentRegistration{AgentID: "a1", NodeID: "n1", Epoch: 2, Capabilities: []string{"tcp", "udp"}}, tr)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Supports("udp") {
		t.Fatal("udp not negotiated")
	}
	if err := m.Heartbeat("a1", 2); err != nil {
		t.Fatal(err)
	}
	if err := m.Heartbeat("a1", 1); !errors.Is(err, ErrEpoch) {
		t.Fatalf("expected epoch fencing, got %v", err)
	}
}
func TestAgentSessionGoAwayAndBackpressure(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{QueueSize: 1})
	tr := newFakeTransport()
	_, _ = m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n", Epoch: 1}, tr)
	if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); err != nil {
		t.Fatal(err)
	}
	if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("got %v", err)
	}
	if err := m.GoAway("a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("got %v", err)
	}
}
func TestClientSessionOpenLocalRouting(t *testing.T) {
	c := NewClientSessionManager()
	tr := newFakeTransport()
	c.Register("u", tr)
	if err := c.OpenStream(context.Background(), "u", StreamOpenRequest{StreamID: 7, Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 80}); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-tr.sent:
		if f.Type != protocol.FrameOpenStream || f.StreamID != 7 {
			t.Fatalf("%+v", f)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}
