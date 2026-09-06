package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

var (
	ErrBridgeNotBinary = errors.New("tcp bridge: binary websocket message required")
	ErrBridgeTooLarge  = errors.New("tcp bridge: websocket message exceeds limit")
	ErrBridgeRefused   = errors.New("tcp bridge: target refused")
)

// WSUpgrader is deliberately small so the server package does not force a
// particular WebSocket implementation on embedders.
type WSUpgrader interface {
	Upgrade(http.ResponseWriter, *http.Request) (WSConn, error)
}

// TCPBridgeAuditEvent carries connection metadata only. It deliberately has
// no payload field so SSH bytes, credentials, and command contents cannot
// enter audit records through the bridge hook.
type TCPBridgeAuditEvent struct {
	Action     string
	UserID     string
	Username   string
	AgentID    string
	TargetHost string
	TargetPort int
	Error      string
}

// TCPBridgeHandler maps exactly one WebSocket connection to one agent stream.
// It is intended for raw SSH/websocat bytes and therefore never base64 encodes
// or coalesces application messages.
type TCPBridgeHandler struct {
	Resolver *routing.RouteResolver
	Opener   relay.NodeTransport
	Upgrade  WSUpgrader
	MaxBytes int64
	Timeout  time.Duration
	Audit    func(context.Context, TCPBridgeAuditEvent)
}

func (h *TCPBridgeHandler) maxBytes() int64 {
	if h == nil || h.MaxBytes <= 0 || h.MaxBytes > 64<<10 {
		return 64 << 10
	}
	return h.MaxBytes
}

func (h *TCPBridgeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Resolver == nil || h.Opener == nil || h.Upgrade == nil {
		http.Error(w, "tcp bridge unavailable", http.StatusServiceUnavailable)
		return
	}
	route, err := h.Resolver.ResolveHTTP(r.Host, r.URL.Path)
	if err != nil {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}
	ctx := r.Context()
	if h.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.Timeout)
		defer cancel()
	}
	stream, err := h.Opener.OpenStream(ctx, relay.StreamRequest{AgentID: route.AgentID, Protocol: "tcp", TargetHost: route.TargetHost, TargetPort: route.TargetPort})
	if err != nil {
		h.emitAudit(ctx, "tcp_proxy.open", route, err)
		http.Error(w, "target refused", http.StatusBadGateway)
		return
	}
	h.emitAudit(ctx, "tcp_proxy.open", route, nil)
	defer stream.Close()
	ws, err := h.Upgrade.Upgrade(w, r)
	if err != nil {
		h.emitAudit(ctx, "tcp_proxy.close", route, err)
		return
	}
	defer ws.Close()
	err = h.bridge(ctx, ws, stream)
	h.emitAudit(ctx, "tcp_proxy.close", route, err)
}

// Handle is useful to tests and adapters which already completed a WebSocket
// upgrade. host may include a Host header port.
func (h *TCPBridgeHandler) Handle(ctx context.Context, ws WSConn, host string) error {
	if h == nil || h.Resolver == nil || h.Opener == nil {
		return ErrBridgeRefused
	}
	if h.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.Timeout)
		defer cancel()
	}
	route, err := h.Resolver.ResolveHTTP(host, "/")
	if err != nil {
		return err
	}
	stream, err := h.Opener.OpenStream(ctx, relay.StreamRequest{AgentID: route.AgentID, Protocol: "tcp", TargetHost: route.TargetHost, TargetPort: route.TargetPort})
	if err != nil {
		h.emitAudit(ctx, "tcp_proxy.open", route, err)
		return ErrBridgeRefused
	}
	h.emitAudit(ctx, "tcp_proxy.open", route, nil)
	defer stream.Close()
	err = h.bridge(ctx, ws, stream)
	h.emitAudit(ctx, "tcp_proxy.close", route, err)
	return err
}

func (h *TCPBridgeHandler) emitAudit(ctx context.Context, action string, route routing.Route, bridgeErr error) {
	if h == nil || h.Audit == nil {
		return
	}
	event := TCPBridgeAuditEvent{Action: action, AgentID: route.AgentID, TargetHost: route.TargetHost, TargetPort: route.TargetPort}
	if p, ok := principalFromContext(ctx); ok {
		event.UserID, event.Username = p.UserID, p.Username
	}
	if bridgeErr != nil {
		event.Error = bridgeErr.Error()
	}
	h.Audit(ctx, event)
}

func (h *TCPBridgeHandler) bridge(ctx context.Context, ws WSConn, stream io.ReadWriteCloser) error {
	if ws == nil || stream == nil {
		return ErrBridgeRefused
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			// Closing both ends is required because ReadMessage and Read may be
			// blocked in library code that does not observe context directly.
			_ = stream.Close()
			_ = ws.Close()
		case <-watchDone:
		}
	}()
	var wg sync.WaitGroup
	type bridgeResult struct {
		direction int // 0 = websocket->stream, 1 = stream->websocket
		err       error
	}
	errCh := make(chan bridgeResult, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errCh <- bridgeResult{direction: 0, err: h.copyWS(ctx, ws, stream)} }()
	go func() { defer wg.Done(); errCh <- bridgeResult{direction: 1, err: h.copyTCP(ctx, ws, stream)} }()
	var first error
	for i := 0; i < 2; i++ {
		result := <-errCh
		err := result.err
		if first == nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			first = err
			cancel()
			_ = stream.Close()
			_ = ws.Close()
		} else if errors.Is(err, io.EOF) {
			if result.direction == 0 {
				// Preserve TCP half-close when available; otherwise a generic
				// logical stream cannot guarantee that the peer will observe EOF.
				if _, ok := stream.(interface{ CloseWrite() error }); !ok {
					cancel()
					_ = stream.Close()
					_ = ws.Close()
				}
			} else {
				// Target EOF terminates the public WebSocket.
				cancel()
				_ = stream.Close()
				_ = ws.Close()
			}
		}
	}
	_ = stream.Close()
	_ = ws.Close()
	wg.Wait()
	return first
}

func (h *TCPBridgeHandler) copyWS(ctx context.Context, ws WSConn, stream io.Writer) error {
	limit := h.maxBytes()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		typ, data, err := ws.ReadMessage()
		if err != nil {
			if cw, ok := stream.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			return err
		}
		if typ != 2 {
			return ErrBridgeNotBinary
		}
		if int64(len(data)) > limit {
			return ErrBridgeTooLarge
		}
		if err := writeAll(stream, data); err != nil {
			return err
		}
	}
}

func (h *TCPBridgeHandler) copyTCP(ctx context.Context, ws WSConn, stream io.Reader) error {
	buf := make([]byte, 32<<10)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := stream.Read(buf)
		if n > 0 {
			if e := ws.WriteMessage(2, append([]byte(nil), buf[:n]...)); e != nil {
				return e
			}
		}
		if err != nil {
			return err
		}
	}
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// tcpConnAdapter supplies net.Conn semantics for callers that need deadline
// enforcement around an io stream. It is intentionally internal to this file.
type tcpConnAdapter struct{ io.ReadWriteCloser }

func (c tcpConnAdapter) LocalAddr() net.Addr              { return bridgeAddr("local") }
func (c tcpConnAdapter) RemoteAddr() net.Addr             { return bridgeAddr("agent") }
func (c tcpConnAdapter) SetDeadline(time.Time) error      { return nil }
func (c tcpConnAdapter) SetReadDeadline(time.Time) error  { return nil }
func (c tcpConnAdapter) SetWriteDeadline(time.Time) error { return nil }

type bridgeAddr string

func (a bridgeAddr) Network() string { return "tunnelmesh" }
func (a bridgeAddr) String() string  { return string(a) }
