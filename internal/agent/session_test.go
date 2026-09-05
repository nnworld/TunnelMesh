package agent

import (
	"context"
	"errors"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
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
