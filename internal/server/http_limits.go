package server

import (
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// newHTTPServer builds the management-plane HTTP server from configuration.
//
// What is deliberately absent is ReadTimeout and WriteTimeout: one port serves the
// console, /api/v1 and hijacked WebSocket upgrades, and a connection-wide write
// deadline would sever every long-lived tunnel. The safe equivalents are
// IdleTimeout for quiet keep-alives, ReadHeaderTimeout plus MaxHeaderBytes for slow
// header sends, and the per-request body deadline installed by
// withAPIBodyTimeout for slow bodies.
func newHTTPServer(limits config.ServerHTTPConfig, handler http.Handler) *http.Server {
	safe := limits.WithSafeDefaults()
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: safe.ReadHeaderTimeout,
		IdleTimeout:       safe.IdleTimeout,
		MaxHeaderBytes:    safe.MaxHeaderBytes,
	}
}

// withAPIBodyTimeout bounds how long a single management API request may take to
// deliver its body.
//
// It is a per-request read deadline rather than http.Server.ReadTimeout because
// the same listener carries upgrades that must live for hours. A transport that
// cannot carry a deadline is passed through unbounded: this is a hardening
// measure, and failing an otherwise valid request because the test used an
// in-memory connection would be worse than the slow client it defends against.
func withAPIBodyTimeout(next http.Handler, timeout time.Duration) http.Handler {
	if timeout <= 0 || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			slog.Debug("api_body_deadline_unsupported", "path", r.URL.Path, "error", err)
			next.ServeHTTP(w, r)
			return
		}
		// Clear the deadline before the connection can be reused, so the budget of
		// one request never kills the next one on the same keep-alive connection.
		defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
		next.ServeHTTP(w, r)
	})
}

// connAdmissionListener caps the number of concurrently open connections on the
// management listener. max <= 0 admits everything, which is the default so an
// upgrade cannot start shedding traffic on its own.
//
// Over-capacity connections are closed instead of queued: a parked connection still
// holds the descriptor and kernel buffer that this limit exists to protect, and a
// fast failure tells the client and the reverse proxy that the node is full rather
// than letting both wait for a timeout.
type connAdmissionListener struct {
	net.Listener
	max      int
	open     atomic.Int64
	onReject func()
}

func newConnAdmissionListener(inner net.Listener, max int, onReject func()) *connAdmissionListener {
	return &connAdmissionListener{Listener: inner, max: max, onReject: onReject}
}

func (l *connAdmissionListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if !l.admit() {
			l.reject(conn)
			continue
		}
		return l.tracked(conn), nil
	}
}

// admit takes a slot, returning false when the listener is at capacity.
func (l *connAdmissionListener) admit() bool {
	if l.max <= 0 {
		l.open.Add(1)
		return true
	}
	if int(l.open.Add(1)) <= l.max {
		return true
	}
	l.open.Add(-1)
	return false
}

func (l *connAdmissionListener) reject(conn net.Conn) {
	if l.onReject != nil {
		l.onReject()
	}
	_ = conn.Close()
}

// tracked returns the connection with a Close that releases its slot. Without it
// the counter would only ever grow and the listener would shed everything.
func (l *connAdmissionListener) tracked(conn net.Conn) net.Conn {
	release := sync.OnceFunc(func() { l.open.Add(-1) })
	return &admittedConn{Conn: conn, release: release}
}

type admittedConn struct {
	net.Conn
	release func()
}

func (c *admittedConn) Close() error {
	c.release()
	return c.Conn.Close()
}
