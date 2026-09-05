package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"io"
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

type streamConn struct {
	writes [][]byte
	closed bool
}

func (c *streamConn) Read([]byte) (int, error) { return 0, io.EOF }
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
