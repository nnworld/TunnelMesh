package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

type stubProxyRoutes map[string]proxyentry.Route

func (s stubProxyRoutes) ProxyRoute(_ context.Context, domain string) (proxyentry.Route, bool) {
	route, ok := s[domain]
	return route, ok
}

type stubProxySecrets struct{ username, password string }

func (s stubProxySecrets) ProxyBasicSecret(context.Context, string) (proxyentry.CredentialSecret, error) {
	return proxyentry.CredentialSecret{Username: s.username, Password: s.password}, nil
}

// stubEgress echoes everything the client sends, standing in for an Agent
// connection to the real target. Requests are mutex-guarded because OpenStream
// runs on a worker goroutine while the test asserts on the snapshot.
type stubEgress struct {
	delay time.Duration
	err   error

	mu       sync.Mutex
	requests []relay.StreamRequest
}

func (s *stubEgress) OpenStream(ctx context.Context, req relay.StreamRequest) (io.ReadWriteCloser, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.delay):
		}
	}
	client, upstream := net.Pipe()
	go func() {
		defer upstream.Close()
		reader := bufio.NewReader(upstream)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			// The echo goes back out through upstream: client is the end the
			// entry holds, so writing to it would loop back into the stub.
			if _, err := io.WriteString(upstream, "echo:"+line); err != nil {
				return
			}
		}
	}()
	return client, nil
}

func (s *stubEgress) snapshot() []relay.StreamRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]relay.StreamRequest(nil), s.requests...)
}

func (s *stubEgress) Close() error { return nil }

// blockingEgress keeps every opened stream alive until the test closes it, so
// the per-route concurrency limit can be exercised deterministically.
type blockingEgress struct {
	opened chan struct{}

	mu    sync.Mutex
	conns []io.ReadWriteCloser
}

func (b *blockingEgress) OpenStream(context.Context, relay.StreamRequest) (io.ReadWriteCloser, error) {
	client, upstream := net.Pipe()
	b.mu.Lock()
	b.conns = append(b.conns, client, upstream)
	b.mu.Unlock()
	select {
	case b.opened <- struct{}{}:
	default:
	}
	return client, nil
}

func (b *blockingEgress) Close() error { return nil }

// egress is typed as relay.NodeTransport so later tasks can substitute a
// scripted or blocking transport without touching the call sites.
func newProxyEntryFixture(t *testing.T, routes stubProxyRoutes, egress relay.NodeTransport, cfg config.ProxyEntryConfig) (string, *ProxyEntry) {
	t.Helper()
	entry := NewProxyEntry(cfg, routes, egress, stubProxySecrets{username: "demo", password: "s3cret"}, nil, nil)
	srv := httptest.NewServer(entry.Handler())
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), entry
}

// dialProxyAndConnect hand-writes a CONNECT carrying the trusted-peer headers,
// because the Go HTTP client cannot be made to emit them.
func dialProxyAndConnect(t *testing.T, addr, target, route, clientIP, proxyAuth string) (net.Conn, *bufio.ReadWriter) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	fmt.Fprintf(&b, "X-TunnelMesh-Route: %s\r\nX-TunnelMesh-Client-IP: %s\r\nX-TunnelMesh-Client-Port: 41234\r\n", route, clientIP)
	if proxyAuth != "" {
		fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", base64.StdEncoding.EncodeToString([]byte(proxyAuth)))
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	return conn, bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
}

// readProxyResponse parses the status line and headers by hand.
//
// http.ReadResponse cannot be used here: with no request to correlate against
// it treats the tunnel bytes following a 200 CONNECT reply as a body and blocks
// forever waiting for EOF.
func readProxyResponse(t *testing.T, rw *bufio.ReadWriter) (int, http.Header) {
	t.Helper()
	line, err := rw.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("malformed status line %q", line)
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("malformed status code in %q: %v", line, err)
	}
	header := http.Header{}
	for {
		field, err := rw.ReadString('\n')
		if err != nil {
			t.Fatalf("read response headers: %v", err)
		}
		field = strings.TrimRight(field, "\r\n")
		if field == "" {
			return code, header
		}
		key, value, ok := strings.Cut(field, ":")
		if !ok {
			t.Fatalf("malformed response header %q", field)
		}
		header.Add(strings.TrimSpace(key), strings.TrimSpace(value))
	}
}

// openEverySource keeps the fixture routes focused on the behaviour under test.
// A route with an empty source CIDR list denies everything by design, so tests
// that are not about the ACL have to say "any source" explicitly.
var openEverySource = []string{"0.0.0.0/0"}

func TestProxyEntryConnectTunnelRoundTrip(t *testing.T) {
	routes := stubProxyRoutes{"tp-demo.tm.example.com": {
		ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
		AuthMode: proxyentry.AuthModeNone, SourceCIDRs: []string{"11.71.85.0/24"},
		AllowPrivateTargets: true, Status: proxyentry.StatusActive,
	}}
	egress := &stubEgress{}
	addr, entry := newProxyEntryFixture(t, routes, egress, proxyEntryTestConfig("127.0.0.1/32"))
	conn, rw := dialProxyAndConnect(t, addr, "93.184.216.34:443", "tp-demo.tm.example.com", "11.71.85.7", "")
	status, _ := readProxyResponse(t, rw)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	line, err := rw.ReadString('\n')
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if line != "echo:ping\n" {
		t.Fatalf("echo = %q", line)
	}
	requests := egress.snapshot()
	if len(requests) != 1 {
		t.Fatalf("egress requests = %#v", requests)
	}
	req := requests[0]
	if req.AgentID != "agent-1" || req.Protocol != "tcp" || req.TargetHost != "93.184.216.34" || req.TargetPort != 443 {
		t.Fatalf("stream request = %#v", req)
	}
	if got := entry.ActiveTunnels(); got != 1 {
		t.Fatalf("ActiveTunnels while connected = %d", got)
	}
	_ = conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	for entry.ActiveTunnels() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := entry.ActiveTunnels(); got != 0 {
		t.Fatalf("ActiveTunnels after close = %d", got)
	}
}

func TestProxyEntryDenials(t *testing.T) {
	basic := proxyentry.Route{
		ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
		AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1",
		SourceCIDRs: []string{"11.71.85.0/24"}, AllowPrivateTargets: true, Status: proxyentry.StatusActive,
	}
	routes := stubProxyRoutes{"tp-demo.tm.example.com": basic}
	cases := []struct {
		name      string
		route     string
		clientIP  string
		auth      string
		target    string
		wantCode  int
		wantError string
	}{
		{"unknown route", "tp-nope.tm.example.com", "11.71.85.7", "", "93.184.216.34:443", http.StatusForbidden, "proxy_route_unavailable"},
		{"foreign suffix", "tp-demo.evil.test", "11.71.85.7", "", "93.184.216.34:443", http.StatusForbidden, "proxy_route_identity_invalid"},
		{"missing route header", "", "11.71.85.7", "", "93.184.216.34:443", http.StatusForbidden, "proxy_route_identity_invalid"},
		{"missing client ip", "tp-demo.tm.example.com", "", "", "93.184.216.34:443", http.StatusForbidden, "proxy_route_identity_invalid"},
		{"acl deny", "tp-demo.tm.example.com", "10.0.0.9", "", "93.184.216.34:443", http.StatusForbidden, "proxy_source_denied"},
		{"auth missing", "tp-demo.tm.example.com", "11.71.85.7", "", "93.184.216.34:443", http.StatusProxyAuthRequired, "proxy_auth_required"},
		{"auth wrong", "tp-demo.tm.example.com", "11.71.85.7", "demo:bad", "93.184.216.34:443", http.StatusProxyAuthRequired, "proxy_auth_failed"},
		{"metadata target", "tp-demo.tm.example.com", "11.71.85.7", "demo:s3cret", "169.254.169.254:80", http.StatusForbidden, "proxy_target_denied"},
		{"bad port", "tp-demo.tm.example.com", "11.71.85.7", "demo:s3cret", "93.184.216.34:0", http.StatusBadRequest, "proxy_target_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, entry := newProxyEntryFixture(t, routes, &stubEgress{}, proxyEntryTestConfig("127.0.0.1/32"))
			_, rw := dialProxyAndConnect(t, addr, tc.target, tc.route, tc.clientIP, tc.auth)
			status, header := readProxyResponse(t, rw)
			if status != tc.wantCode {
				t.Fatalf("status = %d want %d", status, tc.wantCode)
			}
			if status == http.StatusProxyAuthRequired && header.Get("Proxy-Authenticate") == "" {
				t.Fatal("407 must carry Proxy-Authenticate")
			}
			if !strings.Contains(header.Get("Content-Type"), "application/json") {
				t.Fatalf("content type = %q", header.Get("Content-Type"))
			}
			body, _ := io.ReadAll(rw.Reader)
			if !strings.Contains(string(body), tc.wantError) {
				t.Fatalf("body = %q want code %q", body, tc.wantError)
			}
			if strings.Contains(string(body), "s3cret") {
				t.Fatalf("denial body echoed a credential: %q", body)
			}
			if got := entry.ActiveTunnels(); got != 0 {
				t.Fatalf("denied request left %d tunnels reserved", got)
			}
		})
	}
}

func TestProxyEntryEgressErrors(t *testing.T) {
	routes := stubProxyRoutes{"tp-demo.tm.example.com": {
		ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
		AuthMode: proxyentry.AuthModeNone, SourceCIDRs: openEverySource,
		AllowPrivateTargets: true, Status: proxyentry.StatusActive,
	}}
	cfg := proxyEntryTestConfig("127.0.0.1/32")

	failing := &stubEgress{err: errors.New("agent offline")}
	addr, entry := newProxyEntryFixture(t, routes, failing, cfg)
	_, rw := dialProxyAndConnect(t, addr, "93.184.216.34:443", "tp-demo.tm.example.com", "10.0.0.1", "")
	status, _ := readProxyResponse(t, rw)
	body, _ := io.ReadAll(rw.Reader)
	if status != http.StatusBadGateway || !strings.Contains(string(body), "proxy_egress_unavailable") {
		t.Fatalf("egress failure = %d %q", status, body)
	}
	if got := entry.ActiveTunnels(); got != 0 {
		t.Fatalf("failed open left %d tunnels reserved", got)
	}

	slowCfg := cfg
	slowCfg.ConnectTimeout = 50 * time.Millisecond
	slow := &stubEgress{delay: 2 * time.Second}
	slowAddr, _ := newProxyEntryFixture(t, routes, slow, slowCfg)
	_, slowRW := dialProxyAndConnect(t, slowAddr, "93.184.216.34:443", "tp-demo.tm.example.com", "10.0.0.1", "")
	status, _ = readProxyResponse(t, slowRW)
	body, _ = io.ReadAll(slowRW.Reader)
	if status != http.StatusGatewayTimeout || !strings.Contains(string(body), "proxy_egress_timeout") {
		t.Fatalf("timeout = %d %q", status, body)
	}
}

func TestProxyEntryCapacityLimit(t *testing.T) {
	routes := stubProxyRoutes{"tp-demo.tm.example.com": {
		ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
		AuthMode: proxyentry.AuthModeNone, SourceCIDRs: openEverySource, AllowPrivateTargets: true,
		MaxConcurrentTunnels: 1, Status: proxyentry.StatusActive,
	}}
	blocking := &blockingEgress{opened: make(chan struct{}, 4)}
	addr, entry := newProxyEntryFixture(t, routes, blocking, proxyEntryTestConfig("127.0.0.1/32"))

	first, firstRW := dialProxyAndConnect(t, addr, "93.184.216.34:443", "tp-demo.tm.example.com", "10.0.0.1", "")
	if status, _ := readProxyResponse(t, firstRW); status != http.StatusOK {
		t.Fatalf("first tunnel status = %d", status)
	}
	<-blocking.opened
	if got := entry.ActiveTunnels(); got != 1 {
		t.Fatalf("ActiveTunnels while connected = %d", got)
	}

	_, secondRW := dialProxyAndConnect(t, addr, "93.184.216.34:443", "tp-demo.tm.example.com", "10.0.0.2", "")
	status, header := readProxyResponse(t, secondRW)
	if status != http.StatusServiceUnavailable || header.Get("Retry-After") != "5" {
		t.Fatalf("second tunnel = %d Retry-After=%q", status, header.Get("Retry-After"))
	}
	body, _ := io.ReadAll(secondRW.Reader)
	if !strings.Contains(string(body), "proxy_capacity_exhausted") {
		t.Fatalf("body = %q", body)
	}

	// Ending the first tunnel must release its slot again.
	_ = first.Close()
	deadline := time.Now().Add(5 * time.Second)
	for entry.ActiveTunnels() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := entry.ActiveTunnels(); got != 0 {
		t.Fatalf("slot not released, ActiveTunnels = %d", got)
	}
}

// A disabled route must be indistinguishable from a route that never existed,
// otherwise the entry becomes a tp-* name oracle.
func TestProxyEntryDisabledRouteIsIndistinguishable(t *testing.T) {
	routes := stubProxyRoutes{"tp-demo.tm.example.com": {
		ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
		AuthMode: proxyentry.AuthModeNone, SourceCIDRs: openEverySource,
		AllowPrivateTargets: true, Status: proxyentry.StatusDisabled,
	}}
	addr, _ := newProxyEntryFixture(t, routes, &stubEgress{}, proxyEntryTestConfig("127.0.0.1/32"))
	_, rw := dialProxyAndConnect(t, addr, "93.184.216.34:443", "tp-demo.tm.example.com", "10.0.0.1", "")
	status, _ := readProxyResponse(t, rw)
	body, _ := io.ReadAll(rw.Reader)
	if status != http.StatusForbidden || !strings.Contains(string(body), "proxy_route_unavailable") {
		t.Fatalf("disabled route = %d %q", status, body)
	}
}

func TestClassifyTunnelResult(t *testing.T) {
	dnsTimeout := &net.DNSError{IsTimeout: true}
	cases := []struct {
		name      string
		idleFired bool
		errs      []error
		want      string
	}{
		{"both directions clean", false, []error{nil, io.EOF}, "success"},
		{"shutdown artifacts are clean", false, []error{net.ErrClosed, os.ErrDeadlineExceeded}, "success"},
		{"closed pipe is clean", false, []error{io.ErrClosedPipe, nil}, "success"},
		{"deadline error is clean", false, []error{dnsTimeout, nil}, "success"},
		{"transport fault is an error", false, []error{nil, errors.New("relay reset by peer")}, "error"},
		{"idle watchdog wins", true, []error{errors.New("relay reset by peer")}, "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyTunnelResult(tc.idleFired, tc.errs...); got != tc.want {
				t.Fatalf("classifyTunnelResult(%v, %v) = %q, want %q", tc.idleFired, tc.errs, got, tc.want)
			}
		})
	}
}
