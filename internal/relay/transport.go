package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"sync"
)

var ErrTransportClosed = errors.New("relay: transport closed")

// BoundedTransport serializes writes and bounds queued frames, providing
// deterministic backpressure instead of unbounded memory growth.
type BoundedTransport struct {
	mu       sync.Mutex
	conn     io.ReadWriteCloser
	queue    chan []byte
	done     chan struct{}
	closed   bool
	writeErr error
}

func (t *BoundedTransport) Err() error { t.mu.Lock(); defer t.mu.Unlock(); return t.writeErr }

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
			if t.closed {
				t.mu.Unlock()
				continue
			}
			_, err := t.conn.Write(b)
			if err != nil {
				t.writeErr = err
				t.closed = true
				select {
				case <-t.done:
				default:
					close(t.done)
				}
			}
			t.mu.Unlock()
		case <-t.done:
			return
		}
	}
}
func (t *BoundedTransport) Write(p []byte) (int, error) {
	b := append([]byte(nil), p...)
	t.mu.Lock()
	if t.closed {
		err := t.writeErr
		t.mu.Unlock()
		if err != nil {
			return 0, errors.Join(ErrTransportClosed, err)
		}
		return 0, ErrTransportClosed
	}
	t.mu.Unlock()
	select {
	case t.queue <- b:
		return len(p), nil
	default:
		return 0, ErrBackpressure
	}
}
func (t *BoundedTransport) Read(p []byte) (int, error) { return t.conn.Read(p) }
func (t *BoundedTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	select {
	case <-t.done:
	default:
		close(t.done)
	}
	return t.conn.Close()
}

// GRPCRelayTransport is an explicit mTLS-ready HTTP/2 relay adapter. The
// generated RPC client is injected so this package does not own a proto
// schema; callers must construct it from a grpc.ClientConn using TLSConfig.
type GRPCRelayTransport struct {
	StreamTransport
	TLSConfig *tls.Config
}

func NewGRPCRelayTransport(open func(context.Context, StreamRequest) (io.ReadWriteCloser, error), cfg *tls.Config) (*GRPCRelayTransport, error) {
	if cfg == nil || len(cfg.Certificates) == 0 || cfg.RootCAs == nil {
		return nil, errors.New("relay: mTLS requires client certificate and root CAs")
	}
	return &GRPCRelayTransport{StreamTransport: StreamTransport{Open: open}, TLSConfig: cfg}, nil
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
