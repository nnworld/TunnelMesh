package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

var ErrHTTPProxyUnavailable = errors.New("http proxy: unavailable")

// HTTPProxyHandler serves managed HTTP routes through an Agent logical stream.
// The opener is injected so this package remains independent from a concrete
// WebSocket or gRPC implementation.
type HTTPProxyHandler struct {
	Resolver *routing.RouteResolver
	Opener   relay.NodeTransport
	Timeout  time.Duration
}

func NewHTTPProxyHandler(resolver *routing.RouteResolver, opener relay.NodeTransport) *HTTPProxyHandler {
	return &HTTPProxyHandler{Resolver: resolver, Opener: opener}
}

func (h *HTTPProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Resolver == nil || h.Opener == nil {
		http.Error(w, "http proxy unavailable", http.StatusServiceUnavailable)
		return
	}
	route, err := h.Resolver.ResolveHTTP(r.Host, r.URL.Path)
	if err != nil {
		if errors.Is(err, routing.ErrRouteNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), http.StatusForbidden)
		}
		return
	}
	if isWebSocketUpgrade(r) {
		h.handleUpgrade(w, r, route)
		return
	}
	ctx := r.Context()
	if h.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.Timeout)
		defer cancel()
	}
	stream, err := h.Opener.OpenStream(ctx, relay.StreamRequest{AgentID: route.AgentID, Protocol: "http", TargetHost: route.TargetHost, TargetPort: route.TargetPort})
	if err != nil {
		http.Error(w, "target unavailable", http.StatusBadGateway)
		return
	}
	defer stream.Close()
	upstreamReq := r.Clone(ctx)
	upstreamReq.RequestURI = ""
	if route.TargetHost != "" {
		upstreamReq.Host = route.TargetHost
		upstreamReq.URL.Host = route.TargetHost
	}
	// Route metadata must not leak to the target service.
	upstreamReq.Header.Del("X-TunnelMesh-Agent")
	if err := upstreamReq.Write(stream); err != nil {
		http.Error(w, "target write failed", http.StatusBadGateway)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(stream), upstreamReq)
	if err != nil {
		http.Error(w, "target response failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

func (h *HTTPProxyHandler) handleUpgrade(w http.ResponseWriter, r *http.Request, route routing.Route) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket upgrade unsupported", http.StatusHTTPVersionNotSupported)
		return
	}
	ctx := r.Context()
	if h.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.Timeout)
		defer cancel()
	}
	stream, err := h.Opener.OpenStream(ctx, relay.StreamRequest{AgentID: route.AgentID, Protocol: "http", TargetHost: route.TargetHost, TargetPort: route.TargetPort})
	if err != nil {
		http.Error(w, "target unavailable", http.StatusBadGateway)
		return
	}
	defer stream.Close()
	clientConn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer clientConn.Close()
	if err := r.Write(stream); err != nil {
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		return
	}
	if err := resp.Write(rw); err != nil {
		return
	}
	_ = rw.Flush()
	// Once the 101 response has been sent the stream is an opaque byte pipe.
	go io.Copy(stream, clientConn)
	_, _ = io.Copy(clientConn, stream)
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func copyResponse(w http.ResponseWriter, resp *http.Response) {
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// streamNetConn gives callers that need net.Conn (for example an http.Transport
// or a WebSocket implementation) a conservative adapter over a logical stream.
type streamNetConn struct {
	io.ReadWriteCloser
	remote net.Addr
}

func (c *streamNetConn) LocalAddr() net.Addr              { return tunnelAddr("local") }
func (c *streamNetConn) RemoteAddr() net.Addr             { return c.remote }
func (c *streamNetConn) SetDeadline(time.Time) error      { return nil }
func (c *streamNetConn) SetReadDeadline(time.Time) error  { return nil }
func (c *streamNetConn) SetWriteDeadline(time.Time) error { return nil }

type tunnelAddr string

func (a tunnelAddr) Network() string { return "tunnelmesh" }
func (a tunnelAddr) String() string  { return string(a) }
