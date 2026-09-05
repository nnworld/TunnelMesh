package server

import (
	"context"
	"encoding/json"
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

func TestClientSessionOpenStreamEncodesRouteFields(t *testing.T) {
	c := NewClientSessionManager()
	tr := newFakeTransport()
	c.Register("u", tr)
	if err := c.OpenStream(context.Background(), "u", StreamOpenRequest{StreamID: 9, Protocol: "udp", TargetHost: "10.0.0.2", TargetPort: 5353, Metadata: []byte("m")}); err != nil {
		t.Fatal(err)
	}
	f := <-tr.sent
	var got map[string]any
	if err := json.Unmarshal(f.Payload, &got); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if got["protocol"] != "udp" || got["target_host"] != "10.0.0.2" || got["target_port"] != float64(5353) {
		t.Fatalf("route payload=%v", got)
	}
}

func TestGoAwayDrainsQueuedFramesBeforeTerminal(t *testing.T) {
	tr := newFakeTransport()
	m := NewAgentSessionManager(AgentSessionConfig{QueueSize: 8})
	_, _ = m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n", Epoch: 1}, tr)
	for i := 0; i < 3; i++ {
		if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, StreamID: uint32(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.GoAway("a"); err != nil {
		t.Fatal(err)
	}
	seen := make([]protocol.Frame, 0, 4)
	for len(seen) < 4 {
		select {
		case f := <-tr.sent:
			seen = append(seen, f)
		case <-time.After(time.Second):
			t.Fatalf("frames=%d", len(seen))
		}
	}
	for i, f := range seen {
		if i < 3 && f.Type == protocol.FrameGoAway {
			t.Fatalf("GOAWAY before queued frame at %d", i)
		}
	}
	if seen[3].Type != protocol.FrameGoAway {
		t.Fatalf("terminal=%+v", seen[3])
	}
}

type fakeWSConn struct {
	typ     int
	payload []byte
}

func (c *fakeWSConn) ReadMessage() (int, []byte, error) { return c.typ, c.payload, nil }
func (c *fakeWSConn) WriteMessage(int, []byte) error    { return nil }
func (c *fakeWSConn) Close() error                      { return nil }
func TestWSFrameTransportRejectsTextMessages(t *testing.T) {
	c := &fakeWSConn{typ: 1}
	tr := NewWSFrameTransport(c)
	if _, err := tr.Receive(); !errors.Is(err, ErrNonBinaryMessage) {
		t.Fatalf("err=%v", err)
	}
}

func TestRegisterRejectsStaleEpoch(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{})
	_, _ = m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n", Epoch: 4}, newFakeTransport())
	if _, err := m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n2", Epoch: 3}, newFakeTransport()); !errors.Is(err, ErrEpoch) {
		t.Fatalf("err=%v", err)
	}
}

type failingTransport struct{ fakeTransport }

func (f *failingTransport) Send(protocol.Frame) error { return errors.New("write failed") }
func TestWriterErrorClosesSession(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{QueueSize: 1})
	_, _ = m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n", Epoch: 1}, &failingTransport{fakeTransport: *newFakeTransport()})
	if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("err=%v", err)
	}
}
