package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
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
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
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

type RelayServer interface {
	OpenStream(RelayOpenStreamServer) error
	CloseAgentConnection(context.Context, CloseAgentConnectionRequest) error
}
type RelayOpenStreamServer interface {
	Send(*wrapperspb.BytesValue) error
	Recv() (*wrapperspb.BytesValue, error)
	grpc.ServerStream
}
type relayServer struct {
	handler      func(context.Context, StreamRequest) (io.ReadWriteCloser, error)
	closeHandler CloseAgentConnectionFunc
}

func NewRelayServer(handler func(context.Context, StreamRequest) (io.ReadWriteCloser, error)) RelayServer {
	return &relayServer{handler: handler}
}

func NewRelayServerWithClose(handler func(context.Context, StreamRequest) (io.ReadWriteCloser, error), closeHandler CloseAgentConnectionFunc) RelayServer {
	return &relayServer{handler: handler, closeHandler: closeHandler}
}
func RegisterRelayServer(s grpc.ServiceRegistrar, srv RelayServer) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "tunnelmesh.relay.v1.Relay", HandlerType: (*RelayServer)(nil),
		Methods: []grpc.MethodDesc{{MethodName: "CloseAgentConnection", Handler: relayCloseAgentConnectionHandler}},
		Streams: []grpc.StreamDesc{{StreamName: "OpenStream", Handler: relayOpenStreamHandler, ServerStreams: true, ClientStreams: true}},
	}, srv)
}
func relayOpenStreamHandler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(RelayServer).OpenStream(&relayOpenStreamServer{ServerStream: stream})
}

func relayCloseAgentConnectionHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(structpb.Struct)
	if err := dec(in); err != nil {
		return nil, err
	}
	req := closeAgentConnectionRequestFromMetadata(in.GetFields())
	if interceptor == nil {
		return nil, srv.(RelayServer).CloseAgentConnection(ctx, req)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/tunnelmesh.relay.v1.Relay/CloseAgentConnection"}
	handler := func(ctx context.Context, request interface{}) (interface{}, error) {
		return nil, srv.(RelayServer).CloseAgentConnection(ctx, request.(CloseAgentConnectionRequest))
	}
	return interceptor(ctx, req, info, handler)
}

type relayOpenStreamServer struct{ grpc.ServerStream }

func (s *relayOpenStreamServer) Send(v *wrapperspb.BytesValue) error {
	return s.ServerStream.SendMsg(v)
}
func (s *relayOpenStreamServer) Recv() (*wrapperspb.BytesValue, error) {
	v := new(wrapperspb.BytesValue)
	if err := s.ServerStream.RecvMsg(v); err != nil {
		return nil, err
	}
	return v, nil
}
func (r *relayServer) OpenStream(stream RelayOpenStreamServer) error {
	var first structpb.Struct
	if err := stream.RecvMsg(&first); err != nil {
		return err
	}
	req := streamRequestFromRelayMetadata(first.GetFields())
	conn, err := r.handler(stream.Context(), req)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, e := conn.Read(buf)
			if n > 0 {
				_ = stream.Send(&wrapperspb.BytesValue{Value: append([]byte(nil), buf[:n]...)})
			}
			if e != nil {
				return
			}
		}
	}()
	for {
		msg, e := stream.Recv()
		if e != nil {
			return e
		}
		if _, e = conn.Write(msg.Value); e != nil {
			return e
		}
	}
}

func (r *relayServer) CloseAgentConnection(ctx context.Context, req CloseAgentConnectionRequest) error {
	if r.closeHandler == nil {
		return status.Error(codes.Unimplemented, "relay close control is not configured")
	}
	if req.AgentID == "" || req.ConnectionID == "" || req.ConnectionEpoch <= 0 || req.RequestedByNodeID == "" {
		return status.Error(codes.InvalidArgument, "relay close request is invalid")
	}
	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return r.closeHandler(closeCtx, req)
}

var relayStreamDesc = &grpc.StreamDesc{StreamName: "OpenStream", ServerStreams: true, ClientStreams: true}

func DialGRPCNode(ctx context.Context, endpoint string, cfg *tls.Config) (*GRPCNodeTransport, error) {
	if cfg == nil || len(cfg.Certificates) == 0 || cfg.RootCAs == nil {
		return nil, errors.New("relay: mTLS requires client certificate and root CAs")
	}
	if cfg.ServerName == "" || cfg.InsecureSkipVerify || cfg.MinVersion < tls.VersionTLS12 {
		return nil, errors.New("relay: client TLS requires server name, verification, and TLS 1.2+")
	}
	conn, err := grpc.DialContext(ctx, endpoint, grpc.WithTransportCredentials(credentials.NewTLS(cfg)), grpc.WithBlock())
	if err != nil {
		return nil, err
	}
	return &GRPCNodeTransport{conn: conn, tlsConfig: cfg}, nil
}

// DialAuthenticatedGRPCNode creates the production server-node relay client.
// Authentication metadata is attached by an interceptor to every stream.
func DialAuthenticatedGRPCNode(ctx context.Context, endpoint, nodeID string, epoch int64, rawToken string, cfg *tls.Config) (*GRPCNodeTransport, error) {
	if strings.TrimSpace(nodeID) == "" || epoch <= 0 || strings.TrimSpace(rawToken) == "" {
		return nil, errors.New("relay: server-node identity is required")
	}
	if cfg == nil || len(cfg.Certificates) == 0 || cfg.RootCAs == nil || cfg.ServerName == "" || cfg.InsecureSkipVerify || cfg.MinVersion < tls.VersionTLS12 {
		return nil, errors.New("relay: authenticated mTLS requires client certificate, root CAs, server name, and TLS 1.2+")
	}
	conn, err := grpc.DialContext(ctx, endpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithChainStreamInterceptor(NewServerNodeStreamClientInterceptor(nodeID, epoch, rawToken)),
		grpc.WithChainUnaryInterceptor(NewServerNodeUnaryClientInterceptor(nodeID, epoch, rawToken)),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, err
	}
	return &GRPCNodeTransport{conn: conn, tlsConfig: cfg}, nil
}

// DialGRPCNodeAuthenticated is retained as an explicit spelling for callers
// migrating from the unauthenticated low-level constructor.
func DialGRPCNodeAuthenticated(ctx context.Context, endpoint, nodeID string, epoch int64, rawToken string, cfg *tls.Config) (*GRPCNodeTransport, error) {
	return DialAuthenticatedGRPCNode(ctx, endpoint, nodeID, epoch, rawToken, cfg)
}
func (n *GRPCNodeTransport) OpenStream(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
	if n == nil || n.conn == nil {
		return nil, ErrNodeDisconnected
	}
	s, err := n.conn.NewStream(ctx, relayStreamDesc, "/tunnelmesh.relay.v1.Relay/OpenStream")
	if err != nil {
		return nil, err
	}
	meta := map[string]any(relayStreamMetadata(req))
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

func (n *GRPCNodeTransport) CloseAgentConnection(ctx context.Context, req CloseAgentConnectionRequest) error {
	if n == nil || n.conn == nil {
		return ErrNodeDisconnected
	}
	if req.AgentID == "" || req.ConnectionID == "" || req.ConnectionEpoch <= 0 || req.RequestedByNodeID == "" {
		return status.Error(codes.InvalidArgument, "relay close request is invalid")
	}
	msg, err := structpb.NewStruct(closeAgentConnectionMetadata(req))
	if err != nil {
		return err
	}
	return n.conn.Invoke(ctx, "/tunnelmesh.relay.v1.Relay/CloseAgentConnection", msg, &emptypb.Empty{})
}

func relayStreamMetadata(req StreamRequest) map[string]any {
	return map[string]any{
		"node_id": req.NodeID, "agent_id": req.AgentID,
		"case_insensitive_agent_id": req.CaseInsensitiveAgentID,
		"epoch":                     req.Epoch, "stream_id": req.StreamID,
		"target_connection_id":    req.TargetConnectionID,
		"target_connection_epoch": req.TargetConnectionEpoch,
		"protocol":                req.Protocol, "target_host": req.TargetHost,
		"target_port": req.TargetPort, "target_scheme": req.TargetScheme,
		"host_header": req.HostHeader, "tls_server_name": req.TLSServerName,
	}
}

func streamRequestFromRelayMetadata(fields map[string]*structpb.Value) StreamRequest {
	return StreamRequest{
		NodeID: fields["node_id"].GetStringValue(), AgentID: fields["agent_id"].GetStringValue(),
		CaseInsensitiveAgentID: fields["case_insensitive_agent_id"].GetBoolValue(),
		Epoch:                  int64(fields["epoch"].GetNumberValue()),
		TargetConnectionID:     fields["target_connection_id"].GetStringValue(),
		TargetConnectionEpoch:  int64(fields["target_connection_epoch"].GetNumberValue()),
		StreamID:               uint32(fields["stream_id"].GetNumberValue()),
		Protocol:               fields["protocol"].GetStringValue(),
		TargetHost:             fields["target_host"].GetStringValue(),
		TargetPort:             int(fields["target_port"].GetNumberValue()),
		TargetScheme:           fields["target_scheme"].GetStringValue(),
		HostHeader:             fields["host_header"].GetStringValue(),
		TLSServerName:          fields["tls_server_name"].GetStringValue(),
	}
}

func closeAgentConnectionMetadata(req CloseAgentConnectionRequest) map[string]any {
	return map[string]any{
		"agent_id":             req.AgentID,
		"connection_id":        req.ConnectionID,
		"connection_epoch":     req.ConnectionEpoch,
		"requested_by_node_id": req.RequestedByNodeID,
	}
}

func closeAgentConnectionRequestFromMetadata(fields map[string]*structpb.Value) CloseAgentConnectionRequest {
	return CloseAgentConnectionRequest{
		AgentID:           fields["agent_id"].GetStringValue(),
		ConnectionID:      fields["connection_id"].GetStringValue(),
		ConnectionEpoch:   int64(fields["connection_epoch"].GetNumberValue()),
		RequestedByNodeID: fields["requested_by_node_id"].GetStringValue(),
	}
}
func (n *GRPCNodeTransport) Close() error {
	if n == nil || n.conn == nil {
		return nil
	}
	return n.conn.Close()
}

type grpcStreamConn struct {
	stream  grpc.ClientStream
	mu      sync.Mutex
	pending []byte
}

func (c *grpcStreamConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	var msg wrapperspb.BytesValue
	if err := c.stream.RecvMsg(&msg); err != nil {
		return 0, err
	}
	n := copy(p, msg.Value)
	if n < len(msg.Value) {
		c.pending = append(c.pending, msg.Value[n:]...)
	}
	return n, nil
}
func (c *grpcStreamConn) Write(p []byte) (int, error) {
	b := append([]byte(nil), p...)
	if err := c.stream.SendMsg(&wrapperspb.BytesValue{Value: b}); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (c *grpcStreamConn) CloseWrite() error { return c.stream.CloseSend() }
func (c *grpcStreamConn) Close() error      { return c.CloseWrite() }

// TLSConfig is intentionally passed in by callers so certificate policy is
// explicit; relay does not silently disable peer verification.
func NewTLSClientConfig(serverName string, roots *tls.Config) *tls.Config {
	if roots == nil {
		roots = &tls.Config{}
	}
	c := roots.Clone()
	c.ServerName = serverName
	c.MinVersion = tls.VersionTLS12
	c.InsecureSkipVerify = false
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
