package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

// scriptedEgress records the exact request bytes the entry forwarded and answers
// with a canned response, so origin-form rewriting and header stripping can be
// asserted without dialing a real target.
type scriptedEgress struct {
	response string
	err      error

	mu       sync.Mutex
	got      string
	requests []relay.StreamRequest
}

func (s *scriptedEgress) OpenStream(_ context.Context, req relay.StreamRequest) (io.ReadWriteCloser, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	client, upstream := net.Pipe()
	go func() {
		defer func() { _ = upstream.Close() }()
		reader := bufio.NewReader(upstream)
		var b strings.Builder
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			b.WriteString(line)
			if line == "\r\n" {
				break
			}
		}
		s.mu.Lock()
		s.got = b.String()
		s.mu.Unlock()
		_, _ = io.WriteString(upstream, s.response)
	}()
	return client, nil
}

func (s *scriptedEgress) Close() error { return nil }

func (s *scriptedEgress) forwarded() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got
}

func (s *scriptedEgress) streamRequest() relay.StreamRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return relay.StreamRequest{}
	}
	return s.requests[0]
}

// dialProxyAbsolute hand-writes an absolute-form proxy request. net/http cannot
// be used: it will not emit the trusted-peer headers on a plain request and it
// normalizes the absolute-form URL away.
func dialProxyAbsolute(t *testing.T, addr, target, route, clientIP string, extra map[string]string) *http.Response {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var b strings.Builder
	authority := strings.TrimPrefix(strings.TrimPrefix(target, "http://"), "https://")
	if idx := strings.IndexByte(authority, '/'); idx >= 0 {
		authority = authority[:idx]
	}
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\n", target, authority)
	fmt.Fprintf(&b, "X-TunnelMesh-Route: %s\r\nX-TunnelMesh-Client-IP: %s\r\nX-TunnelMesh-Client-Port: 41234\r\n", route, clientIP)
	for key, value := range extra {
		fmt.Fprintf(&b, "%s: %s\r\n", key, value)
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read absolute-form response: %v", err)
	}
	return resp
}

func proxyAbsoluteRoutes() stubProxyRoutes {
	return stubProxyRoutes{"tp-demo.tm.example.com": {
		ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
		AuthMode: proxyentry.AuthModeNone, SourceCIDRs: openEverySource, AllowPrivateTargets: true,
		Status: proxyentry.StatusActive,
	}}
}

func TestProxyEntryAbsoluteFormRewritesToOriginForm(t *testing.T) {
	egress := &scriptedEgress{response: "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nX-Target: real\r\n\r\nhello"}
	addr, entry := newProxyEntryFixture(t, proxyAbsoluteRoutes(), egress, proxyEntryTestConfig("127.0.0.1/32"))

	resp := dialProxyAbsolute(t, addr, "http://93.184.216.34:8080/status", "tp-demo.tm.example.com", "11.71.85.7", map[string]string{
		"Proxy-Authorization": "Basic ZGVtbzpzM2NyZXQ=",
		"Proxy-Connection":    "keep-alive",
		"X-TunnelMesh-Extra":  "must-not-leak",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Target"); got != "real" {
		t.Fatalf("upstream header = %q", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q", body)
	}

	forwarded := egress.forwarded()
	if !strings.HasPrefix(forwarded, "GET /status HTTP/1.1\r\n") {
		t.Fatalf("request line not rewritten to origin-form: %q", forwarded)
	}
	if !strings.Contains(forwarded, "\r\nHost: 93.184.216.34:8080\r\n") {
		t.Fatalf("Host header = %q", forwarded)
	}
	for _, banned := range []string{"Proxy-Authorization", "Proxy-Connection", "X-Tunnelmesh-Route", "X-Tunnelmesh-Client-Ip", "X-Tunnelmesh-Client-Port", "X-Tunnelmesh-Extra"} {
		if strings.Contains(forwarded, banned) {
			t.Fatalf("%s leaked upstream: %q", banned, forwarded)
		}
	}

	req := egress.streamRequest()
	if req.AgentID != "agent-1" || req.Protocol != "http" || req.TargetHost != "93.184.216.34" || req.TargetPort != 8080 || req.TargetScheme != "http" {
		t.Fatalf("stream request = %#v", req)
	}
	if got := entry.ActiveTunnels(); got != 0 {
		t.Fatalf("absolute-form request left %d slots reserved", got)
	}
}

func TestProxyEntryAbsoluteFormDefaultsPortByScheme(t *testing.T) {
	cases := []struct {
		raw        string
		hostHeader string
		wantHost   string
		wantPort   int
		wantScheme string
	}{
		{"http://intranet.example.com/", "intranet.example.com", "intranet.example.com", 80, "http"},
		{"https://intranet.example.com/", "intranet.example.com", "intranet.example.com", 443, "https"},
		{"https://[fd00::1]:8443/api", "[fd00::1]:8443", "fd00::1", 8443, "https"},
		// OpenResty's location / rewrites the request line to origin-form and
		// forwards the authority in Host. HTTPS targets never take this path
		// because they always arrive as CONNECT.
		{"/status", "93.184.216.34:8080", "93.184.216.34", 8080, "http"},
		{"/", "intranet.example.com", "intranet.example.com", 80, "http"},
	}
	for _, tc := range cases {
		t.Run(tc.raw+"|"+tc.hostHeader, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.raw, nil)
			req.Host = tc.hostHeader
			host, port, scheme, err := splitProxyTarget(req)
			if err != nil {
				t.Fatalf("%s (Host %s): %v", tc.raw, tc.hostHeader, err)
			}
			if host != tc.wantHost || port != tc.wantPort || scheme != tc.wantScheme {
				t.Fatalf("%s (Host %s) -> %s:%d/%s", tc.raw, tc.hostHeader, host, port, scheme)
			}
		})
	}
	// httptest.NewRequest always fills Host, so the "no authority anywhere"
	// case has to clear it explicitly.
	orphan := httptest.NewRequest(http.MethodGet, "/relative", nil)
	orphan.Host = ""
	if _, _, _, err := splitProxyTarget(orphan); !errors.Is(err, proxyentry.ErrTargetInvalid) {
		t.Fatalf("origin-form without a Host authority err = %v, want ErrTargetInvalid", err)
	}
	if _, _, _, err := splitProxyTarget(httptest.NewRequest(http.MethodGet, "ftp://intranet.example.com/", nil)); !errors.Is(err, proxyentry.ErrTargetInvalid) {
		t.Fatalf("non-http scheme err = %v, want ErrTargetInvalid", err)
	}
}

func TestProxyEntryAbsoluteFormDeniesAndReportsEgress(t *testing.T) {
	denied := &scriptedEgress{response: "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"}
	addr, entry := newProxyEntryFixture(t, proxyAbsoluteRoutes(), denied, proxyEntryTestConfig("127.0.0.1/32"))
	resp := dialProxyAbsolute(t, addr, "http://169.254.169.254/latest/meta-data/", "tp-demo.tm.example.com", "11.71.85.7", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("metadata target status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "proxy_target_denied") {
		t.Fatalf("body = %q", body)
	}
	if len(denied.forwarded()) != 0 {
		t.Fatalf("denied target was still forwarded: %q", denied.forwarded())
	}
	if got := entry.ActiveTunnels(); got != 0 {
		t.Fatalf("denied request left %d slots reserved", got)
	}

	failing := &scriptedEgress{err: errors.New("agent offline")}
	failAddr, _ := newProxyEntryFixture(t, proxyAbsoluteRoutes(), failing, proxyEntryTestConfig("127.0.0.1/32"))
	failResp := dialProxyAbsolute(t, failAddr, "http://93.184.216.34:8080/status", "tp-demo.tm.example.com", "11.71.85.7", nil)
	defer failResp.Body.Close()
	if failResp.StatusCode != http.StatusBadGateway {
		t.Fatalf("egress failure status = %d", failResp.StatusCode)
	}
	failBody, _ := io.ReadAll(failResp.Body)
	if !strings.Contains(string(failBody), "proxy_egress_unavailable") {
		t.Fatalf("body = %q", failBody)
	}
}

// A route restricted to specific target ports must apply to domain targets too,
// otherwise the port allowlist is bypassed by simply using a hostname.
func TestProxyEntryAbsoluteFormEnforcesTargetPortsForDomains(t *testing.T) {
	routes := stubProxyRoutes{"tp-demo.tm.example.com": {
		ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
		AuthMode: proxyentry.AuthModeNone, SourceCIDRs: openEverySource, AllowPrivateTargets: true,
		TargetPorts: []int{8080}, Status: proxyentry.StatusActive,
	}}
	egress := &scriptedEgress{response: "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"}
	addr, _ := newProxyEntryFixture(t, routes, egress, proxyEntryTestConfig("127.0.0.1/32"))

	denied := dialProxyAbsolute(t, addr, "http://intranet.example.com:9090/status", "tp-demo.tm.example.com", "11.71.85.7", nil)
	defer denied.Body.Close()
	body, _ := io.ReadAll(denied.Body)
	if denied.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "proxy_target_denied") {
		t.Fatalf("off-list port = %d %q", denied.StatusCode, body)
	}

	allowed := dialProxyAbsolute(t, addr, "http://intranet.example.com:8080/status", "tp-demo.tm.example.com", "11.71.85.7", nil)
	defer allowed.Body.Close()
	if allowed.StatusCode != http.StatusOK {
		allowedBody, _ := io.ReadAll(allowed.Body)
		t.Fatalf("listed port status = %d body = %q", allowed.StatusCode, allowedBody)
	}
}
