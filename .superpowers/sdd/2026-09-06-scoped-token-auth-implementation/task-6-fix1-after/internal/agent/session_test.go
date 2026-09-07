package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"golang.org/x/net/websocket"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type blockingTransport struct{ closed chan struct{} }

func (t *blockingTransport) Send(protocol.Frame) error { return nil }
func (t *blockingTransport) Receive() (protocol.Frame, error) {
	<-t.closed
	return protocol.Frame{}, errors.New("closed")
}
func (t *blockingTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}
func TestSessionRunContextCancelClosesReceive(t *testing.T) {
	tr := &blockingTransport{closed: make(chan struct{})}
	s := NewSession(tr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not unblock")
	}
}

type heartbeatTransport struct {
	sent   chan protocol.Frame
	closed chan struct{}
}

func (t *heartbeatTransport) Send(f protocol.Frame) error {
	select {
	case t.sent <- f:
		return nil
	case <-t.closed:
		return errors.New("transport closed")
	}
}
func (t *heartbeatTransport) Receive() (protocol.Frame, error) {
	<-t.closed
	return protocol.Frame{}, errors.New("closed")
}
func (t *heartbeatTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}

func TestSessionRunSendsHeartbeatPing(t *testing.T) {
	tr := &heartbeatTransport{sent: make(chan protocol.Frame, 1), closed: make(chan struct{})}
	s := NewSession(tr)
	s.HeartbeatInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()
	select {
	case frame := <-tr.sent:
		if frame.Type != protocol.FramePing {
			t.Fatalf("heartbeat frame=%+v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat ping was not sent")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session did not stop")
	}
}

type streamConn struct {
	writes [][]byte
	closed bool
	read   []byte
}

type blockingStreamConn struct {
	*streamConn
	release chan struct{}
}

func (c *blockingStreamConn) Read([]byte) (int, error) {
	<-c.release
	return 0, io.EOF
}

func (c *streamConn) Read(p []byte) (int, error) {
	if len(c.read) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.read)
	c.read = c.read[n:]
	return n, nil
}
func (c *streamConn) Write(p []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}
func (c *streamConn) Close() error { c.closed = true; return nil }
func TestStreamDispatcherOpensAndWritesTarget(t *testing.T) {
	conn := &streamConn{}
	d := NewStreamDispatcher(Dialer{Policy: func(context.Context, string, string, int) error { return nil }}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil })
	p, _ := json.Marshal(StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 80})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 7, Payload: p}); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 7, Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if string(conn.writes[0]) != "x" {
		t.Fatalf("writes=%q", conn.writes)
	}
}

func TestDefaultStreamDispatcherSupportsHTTPLogicalStreams(t *testing.T) {
	conn := &streamConn{}
	called := false
	d := NewStreamDispatcher(Dialer{HTTPStream: func(context.Context, string, int) (io.ReadWriteCloser, error) {
		called = true
		return conn, nil
	}}, nil)
	p, _ := json.Marshal(StreamOpenPayload{Protocol: "http", TargetHost: "service", TargetPort: 8080})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 12, Payload: p}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("HTTP logical stream did not use the injected HTTP dialer")
	}
}

func TestDialHTTPStreamUsesHTTPPolicyNamespace(t *testing.T) {
	var got string
	d := Dialer{Policy: func(_ context.Context, proto, _ string, _ int) error { got = proto; return errors.New("blocked") }}
	if _, err := d.DialHTTPStream(context.Background(), "service", 8080); err == nil || got != "http" {
		t.Fatalf("err=%v policy protocol=%q", err, got)
	}
}

func TestStreamDispatcherReadsTargetBackToFrameCallback(t *testing.T) {
	conn := &streamConn{read: []byte("reply")}
	gotc := make(chan protocol.Frame, 1)
	d := NewStreamDispatcherWithCallback(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(f protocol.Frame) {
		if f.Type == protocol.FrameData {
			gotc <- f
		}
	})
	p, _ := json.Marshal(StreamOpenPayload{Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 8, Payload: p}); err != nil {
		t.Fatal(err)
	}
	var got protocol.Frame
	select {
	case got = <-gotc:
	case <-time.After(time.Second):
		t.Fatal("callback timeout")
	}
	if string(got.Payload) != "reply" || got.Type != protocol.FrameData {
		t.Fatalf("frame=%+v", got)
	}
}

func TestStreamDispatcherRejectsDuplicateStreamID(t *testing.T) {
	first := &blockingStreamConn{streamConn: &streamConn{}, release: make(chan struct{})}
	defer close(first.release)
	second := &streamConn{}
	count := 0
	d := NewStreamDispatcher(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) {
		count++
		if count == 1 {
			return first, nil
		}
		return second, nil
	})
	p, _ := json.Marshal(StreamOpenPayload{Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	f := protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 10, Payload: p}
	if err := d.Handle(f); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(f); !errors.Is(err, ErrDuplicateStream) {
		t.Fatalf("err=%v", err)
	}
	if first.closed || !second.closed {
		t.Fatalf("duplicate stream lifecycle first=%v second=%v", first.closed, second.closed)
	}
}

func TestStaleReadBackCannotDeleteReusedStreamID(t *testing.T) {
	d := NewStreamDispatcher(Dialer{}, nil)
	old, newer := &streamConn{}, &streamConn{}
	d.mu.Lock()
	d.generation++
	oldEntry := &streamEntry{conn: old, generation: d.generation}
	d.streams[11] = oldEntry
	d.generation++
	d.streams[11] = &streamEntry{conn: newer, generation: d.generation}
	d.mu.Unlock()
	d.readBack(11, oldEntry)
	d.mu.Lock()
	_, ok := d.streams[11]
	d.mu.Unlock()
	if !ok {
		t.Fatal("stale reader deleted replacement stream")
	}
}

func TestDialWebSocketRejectsNonWebSocketURL(t *testing.T) {
	_, err := DialWebSocket(context.Background(), "https://server.example/ws/agent", "agent-token")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "invalid websocket url") {
		t.Fatalf("DialWebSocket() error = %v, want invalid websocket URL", err)
	}
}

func TestDialWebSocketDoesNotSkipServerCertificateVerification(t *testing.T) {
	server := httptest.NewTLSServer(websocket.Handler(func(conn *websocket.Conn) { _ = conn.Close() }))
	defer server.Close()
	serverURL := "wss" + strings.TrimPrefix(server.URL, "https") + "/ws/agent"
	if transport, err := DialWebSocket(context.Background(), serverURL, "agent-token"); err == nil {
		_ = transport.Close()
		t.Fatal("DialWebSocket accepted an untrusted server certificate")
	}
}
