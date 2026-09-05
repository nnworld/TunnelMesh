package relay

import (
	"context"
	"crypto/tls"
	"io"
	"sync"
)

// BoundedTransport serializes writes and bounds queued frames, providing
// deterministic backpressure instead of unbounded memory growth.
type BoundedTransport struct {
	mu    sync.Mutex
	conn  io.ReadWriteCloser
	queue chan []byte
	done  chan struct{}
}

func NewBoundedTransport(conn io.ReadWriteCloser, size int) *BoundedTransport {
	if size <= 0 {
		size = 64
	}
	t := &BoundedTransport{conn: conn, queue: make(chan []byte, size), done: make(chan struct{})}
	go t.loop()
	return t
}
func (t *BoundedTransport) loop() {
	for {
		select {
		case b := <-t.queue:
			t.mu.Lock()
			_, _ = t.conn.Write(b)
			t.mu.Unlock()
		case <-t.done:
			return
		}
	}
}
func (t *BoundedTransport) Write(p []byte) (int, error) {
	b := append([]byte(nil), p...)
	select {
	case t.queue <- b:
		return len(p), nil
	default:
		return 0, ErrBackpressure
	}
}
func (t *BoundedTransport) Read(p []byte) (int, error) { return t.conn.Read(p) }
func (t *BoundedTransport) Close() error {
	select {
	case <-t.done:
	default:
		close(t.done)
	}
	return t.conn.Close()
}

// TLSConfig is intentionally passed in by callers so certificate policy is
// explicit; relay does not silently disable peer verification.
func NewTLSClientConfig(serverName string, roots *tls.Config) *tls.Config {
	if roots == nil {
		roots = &tls.Config{}
	}
	c := roots.Clone()
	c.ServerName = serverName
	return c
}

type StreamTransport struct {
	Open func(context.Context, StreamRequest) (io.ReadWriteCloser, error)
}

func (t StreamTransport) OpenStream(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
	if t.Open == nil {
		return nil, ErrNodeDisconnected
	}
	return t.Open(ctx, req)
}
func (t StreamTransport) Close() error { return nil }
