package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestNewHTTPServerAppliesConfiguredLimits(t *testing.T) {
	srv := newHTTPServer(config.ServerHTTPConfig{
		ReadHeaderTimeout: 3 * time.Second,
		IdleTimeout:       7 * time.Second,
		MaxHeaderBytes:    4096,
		BodyTimeout:       11 * time.Second,
	}, http.NotFoundHandler())

	if srv.ReadHeaderTimeout != 3*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v, want 3s", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 7*time.Second {
		t.Fatalf("IdleTimeout = %v, want 7s", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 4096 {
		t.Fatalf("MaxHeaderBytes = %d, want 4096", srv.MaxHeaderBytes)
	}
	// A ReadTimeout would kill long-lived WebSocket and streaming responses, so the
	// body bound must live on the request path instead of the connection.
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Fatalf("connection-wide timeouts would break hijacked upgrades: read=%v write=%v", srv.ReadTimeout, srv.WriteTimeout)
	}
}

func TestNewHTTPServerKeepsSafeDefaultsWhenUnset(t *testing.T) {
	// Hand-built runtimes in tests and embedders pass a zero HTTP block. They must
	// not silently lose the header protection the server has always had.
	srv := newHTTPServer(config.ServerHTTPConfig{}, http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 {
		t.Fatalf("ReadHeaderTimeout = %v, want a positive default", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Fatalf("IdleTimeout = %v, want a positive default", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Fatalf("MaxHeaderBytes = %d, want a positive default", srv.MaxHeaderBytes)
	}
}

// pipeListener is a net.Listener whose connections are injected, so the admission
// wrapper can be tested without sockets.
type pipeListener struct {
	mu      sync.Mutex
	pending chan net.Conn
	closed  chan struct{}
	once    sync.Once
	count   int
}

func newPipeListener(buffer int) *pipeListener {
	return &pipeListener{pending: make(chan net.Conn, buffer), closed: make(chan struct{})}
}

func (l *pipeListener) push() net.Conn {
	l.mu.Lock()
	defer l.mu.Unlock()
	local, remote := net.Pipe()
	select {
	case l.pending <- local:
	case <-l.closed:
	default:
	}
	l.count++
	return remote
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.pending:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{} }

// closedWithin reports whether the peer of a net.Pipe connection was closed, which
// is how the test observes a rejection without blocking forever if the limiter
// forgets to shed the connection.
func closedWithin(t *testing.T, conn net.Conn) bool {
	t.Helper()
	result := make(chan bool, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		result <- err != nil
	}()
	select {
	case closed := <-result:
		return closed
	case <-time.After(2 * time.Second):
		return false
	}
}

func TestConnAdmissionListenerRejectsBeyondMax(t *testing.T) {
	var rejections atomic.Int64
	inner := newPipeListener(4)
	limited := newConnAdmissionListener(inner, 1, func() { rejections.Add(1) })

	inner.push()
	first, err := limited.Accept()
	if err != nil {
		t.Fatalf("first Accept() error = %v", err)
	}

	// The second connection exceeds the cap, so the wrapper has to close it and keep
	// looking for one it can admit.
	over := inner.push()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := limited.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	if !closedWithin(t, over) {
		t.Fatal("the over-capacity connection must be closed by the limiter")
	}
	if got := rejections.Load(); got != 1 {
		t.Fatalf("rejections = %d, want 1", got)
	}
	select {
	case admitted := <-accepted:
		t.Fatalf("the over-capacity connection must not be admitted while %v is open, got %v", first, admitted)
	case <-time.After(100 * time.Millisecond):
	}

	// Releasing the admitted slot is what lets the waiting Accept serve the next one.
	if err := first.Close(); err != nil {
		t.Fatalf("first.Close() error = %v", err)
	}
	third := inner.push()
	_ = third
	select {
	case conn := <-accepted:
		if err := conn.Close(); err != nil {
			t.Fatalf("admitted.Close() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("freeing a slot did not admit the next connection")
	}
	if err := limited.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestConnAdmissionListenerUnlimitedByDefault(t *testing.T) {
	inner := newPipeListener(2)
	limited := newConnAdmissionListener(inner, 0, func() { t.Fatal("an unlimited listener must not reject") })
	for i := 0; i < 2; i++ {
		remote := inner.push()
		_ = remote
		conn, err := limited.Accept()
		if err != nil {
			t.Fatalf("Accept() %d error = %v", i, err)
		}
		defer conn.Close()
	}
}

func TestAPIBodyTimeoutStopsAStalledRequest(t *testing.T) {
	readErr := make(chan error, 1)
	handler := withAPIBodyTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		readErr <- err
		if err != nil {
			w.WriteHeader(http.StatusRequestTimeout)
		}
	}), 150*time.Millisecond)
	server := httptest.NewServer(handler)
	defer server.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	defer conn.Close()
	// Content-Length promises more than the client ever sends, so the handler blocks
	// in Read until the per-request deadline fires.
	if _, err := conn.Write([]byte("POST /api/v1/routes HTTP/1.1\r\nHost: test\r\nContent-Length: 100\r\n\r\npartial")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("ReadAll must fail once the body deadline passes")
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("ReadAll error = %v, want a network timeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the body deadline was never applied")
	}
}

func TestAPIBodyTimeoutDisabledLeavesReaderAlone(t *testing.T) {
	done := make(chan error, 1)
	handler := withAPIBodyTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4)
		_, err := io.ReadFull(r.Body, buf)
		done <- err
	}), 0)
	server := httptest.NewServer(handler)
	defer server.Close()
	resp, err := http.Post(server.URL+"/api/v1/routes", "application/json", strings.NewReader("abcd"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReadFull error = %v, want the body to be readable", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never read the body")
	}
}

// TestServeListenerShedsConnectionsBeyondCap is the wiring check: the cap has to
// reach the listener that actually accepts, otherwise the setting is decoration.
func TestServeListenerShedsConnectionsBeyondCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := openHTTPLimitsDatabase(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		HTTP: config.ServerHTTPConfig{MaxConcurrentConnections: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(ctx, listener) }()

	// The first connection holds the only slot by staying quiet, which is exactly
	// what an attacker or a saturated reverse proxy does.
	held, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer held.Close()
	if err := waitForShedConn(t, listener.Addr().String()); err != nil {
		t.Fatalf("second connection: %v", err)
	}
	_ = held.Close()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial after the slot freed: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET /health/live HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !strings.Contains(string(body), "200") || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("health response = %q", body)
	}
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("ServeListener returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ServeListener did not return after cancel")
	}
}

// waitForShedConn dials the management listener and requires the server to hang up
// without a response byte. That is what shedding looks like from outside: the
// connection is accepted, counted and closed, so a client that never sent a
// request sees EOF rather than a keep-alive that waits forever.
func waitForShedConn(t *testing.T, addr string) error {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	n, err := conn.Read(make([]byte, 1))
	if n > 0 {
		return errors.New("the over-capacity connection received a response")
	}
	if err == nil {
		return errors.New("the over-capacity connection read without error")
	}
	if !isConnectionEnded(err) {
		return err
	}
	return nil
}

// openHTTPLimitsDatabase gives the runtime the metadata store it requires without
// touching the filesystem.
func openHTTPLimitsDatabase(ctx context.Context, name string) (*storage.DB, error) {
	return storage.OpenSQLite(ctx, "file:"+strings.NewReplacer(" ", "_").Replace(name)+"?mode=memory&cache=shared")
}

// isConnectionEnded accepts either a reset or an EOF: both mean the peer stopped
// talking, which is what a shed connection looks like from outside.
func isConnectionEnded(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED)
}
