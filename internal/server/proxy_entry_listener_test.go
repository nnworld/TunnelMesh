package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// proxyEntryTestConfig builds a valid enabled configuration on an ephemeral
// port. Trusted proxies are passed in because every test below is really about
// who is allowed to reach the handler.
func proxyEntryTestConfig(trusted ...string) config.ProxyEntryConfig {
	return config.ProxyEntryConfig{
		Enabled: true, Listen: "127.0.0.1:0", TrustedProxies: trusted,
		DomainSuffix:     "tm.example.com",
		RouteHeader:      "X-TunnelMesh-Route",
		ClientIPHeader:   "X-TunnelMesh-Client-IP",
		ClientPortHeader: "X-TunnelMesh-Client-Port",
		ConnectTimeout:   time.Second, IdleTimeout: 2 * time.Second, ShutdownTimeout: time.Second,
		MaxConcurrentTunnels: 2, MaxHeaderBytes: 16384, AuthBackoffThreshold: 5,
	}
}

func startProxyEntryListener(t *testing.T, cfg config.ProxyEntryConfig, handler http.Handler) *ProxyEntryListener {
	t.Helper()
	listener, err := NewProxyEntryListener(cfg, handler, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- listener.Serve(ctx) }()
	if !waitForProxyEntryAddr(listener, 3*time.Second) {
		cancel()
		t.Fatalf("listener did not bind: %v", <-done)
	}
	t.Cleanup(func() { cancel(); <-done })
	return listener
}

func waitForProxyEntryAddr(listener *ProxyEntryListener, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for listener.Addr() == nil {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
	return true
}

func TestNewProxyEntryListenerValidatesTrustedProxies(t *testing.T) {
	handler := http.NotFoundHandler()
	if _, err := NewProxyEntryListener(proxyEntryTestConfig(), handler, nil); err == nil {
		t.Fatal("empty trusted_proxies accepted")
	}
	if _, err := NewProxyEntryListener(proxyEntryTestConfig("not-a-cidr"), handler, nil); err == nil {
		t.Fatal("malformed trusted_proxies entry accepted")
	}
	if _, err := NewProxyEntryListener(proxyEntryTestConfig("127.0.0.1/32"), nil, nil); err == nil {
		t.Fatal("nil handler accepted")
	}
	// A listener that was never served must not report an address.
	listener, err := NewProxyEntryListener(proxyEntryTestConfig("127.0.0.1/32"), handler, nil)
	if err != nil {
		t.Fatal(err)
	}
	if listener.Addr() != nil {
		t.Fatalf("Addr() = %v before Serve", listener.Addr())
	}
}

// The trusted-peer gate is the only thing that makes the injected route and
// client headers believable, so a loopback peer must reach the handler with
// those headers intact.
func TestProxyEntryListenerServesTrustedPeer(t *testing.T) {
	type observed struct {
		route    string
		clientIP string
	}
	seen := make(chan observed, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- observed{route: r.Header.Get("X-TunnelMesh-Route"), clientIP: r.Header.Get("X-TunnelMesh-Client-IP")}
		w.Header().Set("X-Echo-Route", r.Header.Get("X-TunnelMesh-Route"))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	listener := startProxyEntryListener(t, proxyEntryTestConfig("127.0.0.1/32"), handler)

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := "GET http://target.example/ HTTP/1.1\r\n" +
		"Host: target.example\r\n" +
		"Connection: close\r\n" +
		"X-TunnelMesh-Route: tp-demo.tm.example.com\r\n" +
		"X-TunnelMesh-Client-IP: 203.0.113.7\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !bytes.Contains(body, []byte("200 OK")) || !bytes.Contains(body, []byte("X-Echo-Route: tp-demo.tm.example.com")) {
		t.Fatalf("response = %q", body)
	}
	select {
	case got := <-seen:
		if got.route != "tp-demo.tm.example.com" || got.clientIP != "203.0.113.7" {
			t.Fatalf("handler observed %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("handler never ran")
	}
}

// A peer outside the allowlist must be dropped before a single request byte is
// parsed: no response, and the handler must never see the forged headers.
func TestProxyEntryListenerClosesUntrustedPeerBeforeReading(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler must not run for an untrusted peer")
	})
	listener := startProxyEntryListener(t, proxyEntryTestConfig("10.9.9.9/32"), handler)

	// Reading first proves the close is a clean FIN with no response at all.
	quiet, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer quiet.Close()
	_ = quiet.SetReadDeadline(time.Now().Add(2 * time.Second))
	body, err := io.ReadAll(quiet)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected read error: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("untrusted peer received %q", body)
	}

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"))
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	body, err = io.ReadAll(conn)
	// The socket is closed while the request bytes are still unread, so the
	// kernel answers with RST rather than FIN. Either way the peer must not get
	// a single response byte.
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("unexpected read error: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("untrusted peer received %q", body)
	}
}

// Shutdown must stop accepting and return nil, otherwise the CLI turns a clean
// SIGTERM into a non-zero exit code.
func TestProxyEntryListenerStopsOnContextCancel(t *testing.T) {
	cfg := proxyEntryTestConfig("127.0.0.1/32")
	listener, err := NewProxyEntryListener(cfg, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- listener.Serve(ctx) }()
	if !waitForProxyEntryAddr(listener, 3*time.Second) {
		cancel()
		t.Fatalf("listener did not bind: %v", <-done)
	}
	addr := listener.Addr().String()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	if listener.Addr() != nil {
		t.Fatalf("Addr() = %v after shutdown", listener.Addr())
	}
	if conn, err := net.Dial("tcp", addr); err == nil {
		_ = conn.Close()
		t.Fatal("listener still accepts after shutdown")
	}
}

// A bind failure has to surface as an error from Serve so the runtime can fail
// the whole process instead of serving without a proxy entry.
func TestProxyEntryListenerReportsBindFailure(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	cfg := proxyEntryTestConfig("127.0.0.1/32")
	cfg.Listen = blocker.Addr().String()
	listener, err := NewProxyEntryListener(cfg, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Serve(context.Background()); err == nil {
		t.Fatal("Serve succeeded on an address that is already in use")
	}
}

// Wiring check: an enabled entry must actually be served by ServeListener and
// must release its port on shutdown. Without this the listener can be
// constructed and still never accept a connection.
func TestServeListenerStartsAndStopsProxyEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := storage.OpenSQLite(ctx, "file:runtime-proxy-entry?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{ProxyEntry: proxyEntryTestConfig("127.0.0.1/32")})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if !runtime.ProxyEntryEnabled || runtime.proxyEntry == nil {
		t.Fatal("proxy entry was not initialized")
	}

	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpListener.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(ctx, httpListener) }()
	if !waitForProxyEntryAddr(runtime.proxyEntry, 3*time.Second) {
		t.Fatal("proxy entry never bound")
	}

	conn, err := net.Dial("tcp", runtime.proxyEntry.Addr().String())
	if err != nil {
		t.Fatalf("dial proxy entry: %v", err)
	}
	defer conn.Close()
	// The runtime mounts the real entry, so a request without the trusted-peer
	// headers must be rejected by the policy engine rather than proxied.
	request := "CONNECT 93.184.216.34:443 HTTP/1.1\r\nHost: 93.184.216.34:443\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read entry response: %v", err)
	}
	if !bytes.Contains(body, []byte("403")) || !bytes.Contains(body, []byte("proxy_route_identity_invalid")) {
		t.Fatalf("entry response = %q", body)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("ServeListener returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeListener did not return after cancel")
	}
	if runtime.proxyEntry.Addr() != nil {
		t.Fatalf("proxy entry still bound after shutdown: %v", runtime.proxyEntry.Addr())
	}
}
