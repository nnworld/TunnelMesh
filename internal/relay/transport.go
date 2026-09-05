package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

var ErrTransportClosed = errors.New("relay: transport closed")

// BoundedTransport serializes writes and bounds queued frames, providing
// deterministic backpressure instead of unbounded memory growth.
type BoundedTransport struct {
	mu       sync.Mutex
	writeMu  sync.Mutex
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
			closed := t.closed
			t.mu.Unlock()
			if closed {
				continue
			}
			t.writeMu.Lock()
			t.mu.Lock()
			closed = t.closed
			t.mu.Unlock()
			if closed {
				t.writeMu.Unlock()
				continue
			}
			_, err := t.conn.Write(b)
			t.writeMu.Unlock()
			t.mu.Lock()
			if err != nil {
				t.writeErr = err
				t.closed = true
			}
			t.mu.Unlock()
			if err != nil {
				select {
				case <-t.done:
				default:
					close(t.done)
				}
				_ = t.conn.Close()
				return
			}
		case <-t.done:
			return
		}
	}
}
func (t *BoundedTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	if t.closed {
		err := t.writeErr
		t.mu.Unlock()
		if err != nil {
			return 0, errors.Join(ErrTransportClosed, err)
		}
		return 0, ErrTransportClosed
	}
	b := append([]byte(nil), p...)
	select {
	case t.queue <- b:
		t.mu.Unlock()
		return len(p), nil
	default:
		t.mu.Unlock()
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

// GRPCNodeTransport is a concrete HTTP/2 bidirectional-stream adapter. The
// relay service name is intentionally stable so independent server versions
// can share the stream contract without generated Go code in this package.
type GRPCNodeTransport struct {
	conn      *grpc.ClientConn
	tlsConfig *tls.Config
}

var relayStreamDesc = &grpc.StreamDesc{StreamName: "OpenStream", ServerStreams: true, ClientStreams: true}

func DialGRPCNode(ctx context.Context, endpoint string, cfg *tls.Config) (*GRPCNodeTransport, error) {
	if cfg == nil || len(cfg.Certificates) == 0 || cfg.RootCAs == nil {
		return nil, errors.New("relay: mTLS requires client certificate and root CAs")
	}
	conn, err := grpc.DialContext(ctx, endpoint, grpc.WithTransportCredentials(credentials.NewTLS(cfg)), grpc.WithBlock())
	if err != nil {
		return nil, err
	}
	return &GRPCNodeTransport{conn: conn, tlsConfig: cfg}, nil
}
func (n *GRPCNodeTransport) OpenStream(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
	if n == nil || n.conn == nil {
		return nil, ErrNodeDisconnected
	}
	s, err := n.conn.NewStream(ctx, relayStreamDesc, "/tunnelmesh.relay.v1.Relay/OpenStream")
	if err != nil {
		return nil, err
	}
	meta := map[string]any{"node_id": req.NodeID, "agent_id": req.AgentID, "epoch": req.Epoch, "stream_id": req.StreamID, "protocol": req.Protocol, "target_host": req.TargetHost, "target_port": req.TargetPort}
	msg, err := structpb.NewStruct(meta)
	if err != nil {
		_ = s.CloseSend()
		return nil, err
	}
	if err := s.SendMsg(msg); err != nil {
		_ = s.CloseSend()
		return nil, err
	}
	return &grpcStreamConn{stream: s}, nil
}
func (n *GRPCNodeTransport) Close() error {
	if n == nil || n.conn == nil {
		return nil
	}
	return n.conn.Close()
}

type grpcStreamConn struct{ stream grpc.ClientStream }

func (c *grpcStreamConn) Read(p []byte) (int, error) {
	var msg wrapperspb.BytesValue
	if err := c.stream.RecvMsg(&msg); err != nil {
		return 0, err
	}
	return copy(p, msg.Value), nil
}
func (c *grpcStreamConn) Write(p []byte) (int, error) {
	b := append([]byte(nil), p...)
	if err := c.stream.SendMsg(&wrapperspb.BytesValue{Value: b}); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (c *grpcStreamConn) Close() error { return c.stream.CloseSend() }

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
