package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
)

const (
	// proxyEntryReadHeaderTimeout bounds how long a peer may hold a connection
	// open without completing a request line and headers. OpenResty relays
	// immediately, so anything slower than this is a slow-loris attempt.
	proxyEntryReadHeaderTimeout = 10 * time.Second
	// proxyEntryDefaultShutdownTimeout is used when shutdown_timeout is zero.
	// Active CONNECT tunnels are hijacked and therefore not drained by
	// http.Server.Shutdown; they are torn down separately by the entry.
	proxyEntryDefaultShutdownTimeout = 30 * time.Second
)

// ProxyEntryListener serves the internal plaintext listener that OpenResty
// relays `tp-*` managed-proxy traffic to.
//
// Requests arriving here carry forgeable headers: the route identity and the
// real client IP/port are injected by the relay, not derived from the socket.
// The TCP peer allowlist is therefore the single thing that makes those headers
// trustworthy, and it is enforced in Accept -- before a request byte is read,
// so an untrusted host cannot spend policy-engine time or appear in a route's
// auth-backoff counters.
type ProxyEntryListener struct {
	cfg     config.ProxyEntryConfig
	handler http.Handler
	metrics *observability.Metrics
	trusted []*net.IPNet
	// addr is published once the socket is bound and cleared on shutdown, so
	// Addr() doubles as a "is it listening yet" signal for callers and tests.
	addr atomic.Pointer[net.Addr]
}

// NewProxyEntryListener validates the trust boundary up front. Configuration
// errors are returned here rather than at Serve time so a typo in
// trusted_proxies fails the process during startup instead of silently
// disabling the entry later.
func NewProxyEntryListener(cfg config.ProxyEntryConfig, handler http.Handler, metrics *observability.Metrics) (*ProxyEntryListener, error) {
	if handler == nil {
		return nil, errors.New("proxy entry listener requires a handler")
	}
	trusted, err := parseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	return &ProxyEntryListener{cfg: cfg, handler: handler, metrics: metrics, trusted: trusted}, nil
}

func parseTrustedProxies(cidrs []string) ([]*net.IPNet, error) {
	if len(cidrs) == 0 {
		return nil, errors.New("proxy entry requires at least one trusted proxy CIDR")
	}
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, raw := range cidrs {
		trimmed := strings.TrimSpace(raw)
		_, network, err := net.ParseCIDR(trimmed)
		if err != nil {
			return nil, fmt.Errorf("proxy entry trusted proxy %q is not a valid CIDR: %w", raw, err)
		}
		networks = append(networks, network)
	}
	return networks, nil
}

// Serve binds the configured address and blocks until ctx is cancelled or
// serving fails. A clean cancellation returns nil; a bind or accept failure
// returns an error so the runtime can fail the process loudly.
func (l *ProxyEntryListener) Serve(ctx context.Context) error {
	if l == nil {
		return errors.New("proxy entry listener is not configured")
	}
	ln, err := net.Listen("tcp", l.cfg.Listen)
	if err != nil {
		return fmt.Errorf("proxy entry listen %s: %w", l.cfg.Listen, err)
	}
	bound := ln.Addr()
	l.addr.Store(&bound)
	slog.InfoContext(ctx, "proxy_entry_listening",
		"addr", bound.String(),
		"trusted_proxies", len(l.trusted),
		"max_concurrent_tunnels", l.cfg.MaxConcurrentTunnels)

	srv := &http.Server{
		Handler:           l.handler,
		ReadHeaderTimeout: proxyEntryReadHeaderTimeout,
		MaxHeaderBytes:    l.cfg.MaxHeaderBytes,
	}
	gated := &trustedPeerListener{
		inner:   ln,
		trusted: l.trusted,
		onDeny: func(remote net.Addr) {
			// Only the peer address is logged. The bytes an untrusted host sent
			// are deliberately discarded unread and never recorded.
			slog.WarnContext(ctx, "proxy_entry_untrusted_peer", "remote", remote.String())
		},
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(gated) }()

	shutdown := func() {
		timeout := l.cfg.ShutdownTimeout
		if timeout <= 0 {
			timeout = proxyEntryDefaultShutdownTimeout
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		// Shutdown already closes the tracked listener; the explicit Close keeps
		// the socket from surviving a Shutdown timeout, and its error is
		// uninteresting once we are tearing down.
		_ = srv.Shutdown(shutdownCtx)
		_ = ln.Close()
		l.addr.Store(nil)
	}

	select {
	case err := <-errCh:
		shutdown()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("proxy entry serve failed: %w", err)
	case <-ctx.Done():
		shutdown()
		// errCh is buffered, so the goroutine exits without a reader here.
		return nil
	}
}

// Addr reports the bound address, or nil before Serve binds and after shutdown.
func (l *ProxyEntryListener) Addr() net.Addr {
	if l == nil {
		return nil
	}
	if addr := l.addr.Load(); addr != nil {
		return *addr
	}
	return nil
}

// trustedPeerListener drops connections from hosts outside the configured
// allowlist inside Accept, before http.Server reads anything from them.
type trustedPeerListener struct {
	inner   net.Listener
	trusted []*net.IPNet
	onDeny  func(remote net.Addr)
}

func (l *trustedPeerListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.inner.Accept()
		if err != nil {
			return nil, err
		}
		if peerAllowed(conn.RemoteAddr(), l.trusted) {
			return conn, nil
		}
		if l.onDeny != nil {
			l.onDeny(conn.RemoteAddr())
		}
		_ = conn.Close()
	}
}

func (l *trustedPeerListener) Addr() net.Addr { return l.inner.Addr() }
func (l *trustedPeerListener) Close() error   { return l.inner.Close() }

// peerAllowed resolves the socket peer against the allowlist. A peer address
// that is not an IP (unix socket, malformed addr) fails closed.
func peerAllowed(remote net.Addr, trusted []*net.IPNet) bool {
	if remote == nil {
		return false
	}
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil {
		host = remote.String()
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	for _, network := range trusted {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}
