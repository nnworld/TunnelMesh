package server

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

type fakeWS struct {
	msgs [][]byte
	out  [][]byte
}

type blockingWS struct{ closed chan struct{} }

func (w *blockingWS) ReadMessage() (int, []byte, error) {
	<-w.closed
	return 0, nil, io.EOF
}
func (w *blockingWS) WriteMessage(int, []byte) error { return nil }
func (w *blockingWS) Close() error {
	select {
	case <-w.closed:
	default:
		close(w.closed)
	}
	return nil
}

type blockingStream struct{ closed chan struct{} }

func (s *blockingStream) Read([]byte) (int, error) {
	<-s.closed
	return 0, io.EOF
}
func (s *blockingStream) Write(p []byte) (int, error) {
	select {
	case <-s.closed:
		return 0, io.ErrClosedPipe
	default:
		return len(p), nil
	}
}
func (s *blockingStream) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

func TestTCPBridgeContextTimeoutUnblocksBothDirections(t *testing.T) {
	stream := &blockingStream{closed: make(chan struct{})}
	ws := &blockingWS{closed: make(chan struct{})}
	h := &TCPBridgeHandler{Resolver: routing.NewRouteResolver([]routing.Route{{Domain: "example.com", AgentID: "a", TargetHost: "10.0.0.1", TargetPort: 22}}), Opener: bridgeOpener{conn: stream}, Timeout: 10 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- h.Handle(context.Background(), ws, "example.com") }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop after timeout")
	}
}

func (w *fakeWS) ReadMessage() (int, []byte, error) {
	if len(w.msgs) == 0 {
		return 0, nil, io.EOF
	}
	b := w.msgs[0]
	w.msgs = w.msgs[1:]
	return 2, b, nil
}
func (w *fakeWS) WriteMessage(_ int, b []byte) error {
	w.out = append(w.out, append([]byte(nil), b...))
	return nil
}
func (w *fakeWS) Close() error { return nil }

type echoConn struct {
	strings.Builder
	closed chan struct{}
}

func (c *echoConn) Read(p []byte) (int, error)  { <-c.closed; return 0, io.EOF }
func (c *echoConn) Write(p []byte) (int, error) { return c.Builder.Write(p) }
func (c *echoConn) CloseWrite() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}
func (c *echoConn) Close() error { return c.CloseWrite() }

type bridgeOpener struct{ conn io.ReadWriteCloser }

func (o bridgeOpener) OpenStream(context.Context, relay.StreamRequest) (io.ReadWriteCloser, error) {
	return o.conn, nil
}
func (o bridgeOpener) Close() error { return nil }

func TestTCPBridgeHandleBinaryAndLimit(t *testing.T) {
	conn := &echoConn{closed: make(chan struct{})}
	h := &TCPBridgeHandler{Resolver: routing.NewRouteResolver([]routing.Route{{Domain: "example.com", AgentID: "a", TargetHost: "10.0.0.1", TargetPort: 22}}), Opener: bridgeOpener{conn: conn}, MaxBytes: 64 << 10}
	ws := &fakeWS{msgs: [][]byte{[]byte("ssh")}}
	if err := h.Handle(context.Background(), ws, "example.com"); err != nil {
		t.Fatal(err)
	}
	if conn.String() != "ssh" {
		t.Fatalf("got %q", conn.String())
	}
}

func TestTCPBridgeAuditIncludesUserAndTargetContext(t *testing.T) {
	conn := &echoConn{closed: make(chan struct{})}
	ws := &fakeWS{msgs: [][]byte{[]byte("ssh")}}
	var events []TCPBridgeAuditEvent
	h := &TCPBridgeHandler{
		Resolver: routing.NewRouteResolver([]routing.Route{{Domain: "example.com", AgentID: "agent-ssh", TargetHost: "10.0.0.8", TargetPort: 22}}),
		Opener:   bridgeOpener{conn: conn},
		Audit: func(_ context.Context, event TCPBridgeAuditEvent) {
			events = append(events, event)
		},
	}
	ctx := withPrincipal(context.Background(), auth.Principal{UserID: "user-1", Username: "alice", Role: "user"})
	if err := h.Handle(ctx, ws, "example.com"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("audit events=%+v", events)
	}
	for _, event := range events {
		if event.AgentID != "agent-ssh" || event.TargetHost != "10.0.0.8" || event.TargetPort != 22 || event.UserID != "user-1" || event.Username != "alice" {
			t.Fatalf("audit context=%+v", event)
		}
	}
	if events[0].Action != "tcp_proxy.open" || events[1].Action != "tcp_proxy.close" {
		t.Fatalf("audit actions=%+v", events)
	}
}
