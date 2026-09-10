package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTTPProxyForwardAbsoluteFormForwardsDynamicTarget(t *testing.T) {
	remote := newTestStream([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi"))
	opened := make(chan StreamRequest, 1)
	fwd, err := NewHTTPProxyForward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		opened <- req
		return remote, nil
	}), HTTPProxyForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fwd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()

	conn, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := "GET http://service.internal:8080/path?x=1 HTTP/1.1\r\nHost: service.internal:8080\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "hi" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	select {
	case req := <-opened:
		if req.AgentID != "agent-a" || req.Protocol != "http" || req.TargetHost != "service.internal" || req.TargetPort != 8080 {
			t.Fatalf("open request=%+v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("stream was not opened")
	}
	waitForRemoteWrite(t, remote)
	written := string(remote.Written())
	if !strings.Contains(written, "GET /path?x=1 HTTP/1.1\r\n") {
		t.Fatalf("upstream request line=%q", written)
	}
	if !strings.Contains(written, "Host: service.internal:8080\r\n") {
		t.Fatalf("upstream Host header missing: %q", written)
	}
}

func TestHTTPProxyForwardConnectTunnelsTCP(t *testing.T) {
	remote := newConnectTestStream([]byte("tunnel-reply"))
	opened := make(chan StreamRequest, 1)
	fwd, err := NewHTTPProxyForward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		opened <- req
		return remote, nil
	}), HTTPProxyForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fwd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()

	conn, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("CONNECT service.internal:8443 HTTP/1.1\r\nHost: service.internal:8443\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	// CONNECT responses and tunnel bytes can arrive in the same TCP segment.
	// Reuse one buffered reader so bytes consumed with the response are not
	// stranded in a discarded bufio.Reader and missed by the direct read below.
	connReader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(connReader, nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT response=%+v err=%v", resp, err)
	}
	if _, err := conn.Write([]byte("tunnel-request")); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-opened:
		if req.Protocol != "tcp" || req.TargetHost != "service.internal" || req.TargetPort != 8443 {
			t.Fatalf("open request=%+v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("stream was not opened")
	}
	waitForRemoteWrite(t, remote)
	got := make([]byte, len("tunnel-reply"))
	if _, err := io.ReadFull(connReader, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "tunnel-reply" {
		t.Fatalf("reply=%q", got)
	}
}

type connectTestStream struct {
	mu     sync.Mutex
	read   *bytes.Buffer
	writes bytes.Buffer
	closed chan struct{}
}

func newConnectTestStream(read []byte) *connectTestStream {
	return &connectTestStream{read: bytes.NewBuffer(read), closed: make(chan struct{})}
}

func (s *connectTestStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if s.read.Len() > 0 {
		n, err := s.read.Read(p)
		s.mu.Unlock()
		return n, err
	}
	s.mu.Unlock()
	<-s.closed
	return 0, io.EOF
}

func (s *connectTestStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes.Write(p)
}

func (s *connectTestStream) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

func (s *connectTestStream) Written() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.writes.Bytes()...)
}

func TestHTTPProxyForwardBasicAuthSucceedsAndStripsProxyHeaders(t *testing.T) {
	remote := newTestStream([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
	opened := make(chan StreamRequest, 1)
	fwd, err := NewHTTPProxyForward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		opened <- req
		return remote, nil
	}), HTTPProxyForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: "agent-a", AuthMode: HTTPProxyAuthBasic,
		Username: "alice", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fwd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()

	conn, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	credentials := base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	request := "GET http://service.internal:8080/ HTTP/1.1\r\nHost: service.internal:8080\r\nProxy-Authorization: Basic " + credentials + "\r\nProxy-Connection: keep-alive\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
	defer resp.Body.Close()
	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("stream was not opened after valid credentials")
	}
	waitForRemoteWrite(t, remote)
	written := string(remote.Written())
	if strings.Contains(strings.ToLower(written), "proxy-authorization") || strings.Contains(strings.ToLower(written), "proxy-connection") {
		t.Fatalf("proxy headers leaked upstream: %q", written)
	}
}

func TestHTTPProxyForwardBasicAuthFailsWithoutOpeningStream(t *testing.T) {
	opened := false
	fwd, err := NewHTTPProxyForward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		opened = true
		return nil, io.EOF
	}), HTTPProxyForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: "agent-a", AuthMode: HTTPProxyAuthBasic,
		Username: "alice", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fwd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()

	conn, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	credentials := base64.StdEncoding.EncodeToString([]byte("alice:wrong"))
	request := "GET http://service.internal:8080/ HTTP/1.1\r\nHost: service.internal:8080\r\nProxy-Authorization: Basic " + credentials + "\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
	if resp.Header.Get("Proxy-Authenticate") == "" {
		t.Fatal("407 response missing Proxy-Authenticate challenge")
	}
	if opened {
		t.Fatal("stream was opened after invalid credentials")
	}
}

func TestHTTPProxyForwardBasicAuthRequiresHeader(t *testing.T) {
	opened := false
	fwd, err := NewHTTPProxyForward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		opened = true
		return nil, io.EOF
	}), HTTPProxyForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: "agent-a", AuthMode: HTTPProxyAuthBasic,
		Username: "alice", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fwd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()

	conn, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := "GET http://service.internal:8080/ HTTP/1.1\r\nHost: service.internal:8080\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
	if resp.Header.Get("Proxy-Authenticate") == "" {
		t.Fatal("407 response missing Proxy-Authenticate challenge")
	}
	if opened {
		t.Fatal("stream was opened without proxy credentials")
	}
}

func TestHTTPProxyForwardWebSocketUpgradeBridgesRawBytes(t *testing.T) {
	remote := newTestStream([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\nremote-data"))
	opened := make(chan StreamRequest, 1)
	fwd, err := NewHTTPProxyForward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		opened <- req
		return remote, nil
	}), HTTPProxyForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fwd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()

	conn, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := "GET http://service.internal:8080/chat HTTP/1.1\r\nHost: service.internal:8080\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var got strings.Builder
	buf := make([]byte, 256)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
			if strings.Contains(got.String(), "101 Switching Protocols") && strings.Contains(got.String(), "remote-data") {
				break
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	select {
	case req := <-opened:
		if req.Protocol != "http" || req.TargetHost != "service.internal" || req.TargetPort != 8080 {
			t.Fatalf("open request=%+v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("stream was not opened for WebSocket upgrade")
	}
}

func TestHTTPProxyForwardRejectsNonLoopbackWithoutExplicitAllowRemote(t *testing.T) {
	_, err := NewHTTPProxyForward(nil, HTTPProxyForwardConfig{ListenAddr: "0.0.0.0:8080", AgentID: "agent-a"})
	if err == nil {
		t.Fatal("non-loopback listener was accepted without --allow-remote")
	}
}

func TestHTTPProxyForwardRequiresBasicAuthForNonLoopback(t *testing.T) {
	_, err := NewHTTPProxyForward(nil, HTTPProxyForwardConfig{
		ListenAddr: "0.0.0.0:8080", AgentID: "agent-a", AllowRemote: true,
	})
	if err == nil {
		t.Fatal("non-loopback no-auth listener was accepted")
	}
	_, err = NewHTTPProxyForward(nil, HTTPProxyForwardConfig{
		ListenAddr: "0.0.0.0:8080", AgentID: "agent-a", AllowRemote: true, AuthMode: HTTPProxyAuthBasic,
	})
	if err == nil {
		t.Fatal("basic auth was accepted without credentials")
	}
}

func TestHTTPProxyForwardRemoteValidationAllows(t *testing.T) {
	var got RemoteValidationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	remote := newTestStream([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
	opened := make(chan StreamRequest, 1)
	fwd, err := NewHTTPProxyForward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		opened <- req
		return remote, nil
	}), HTTPProxyForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: "agent-a", AuthMode: HTTPProxyAuthBasic,
		Username: "alice", Password: "secret", AuthURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fwd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()

	conn, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	credentials := base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	request := "GET http://service.internal:8080/ HTTP/1.1\r\nHost: service.internal:8080\r\nProxy-Authorization: Basic " + credentials + "\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
	defer resp.Body.Close()
	want := RemoteValidationRequest{
		Protocol: "http-proxy", AgentID: "agent-a", TargetHost: "service.internal", TargetPort: 8080,
		Username: "alice", Password: "secret",
	}
	if got != want {
		t.Fatalf("remote request=%+v, want %+v", got, want)
	}
	select {
	case req := <-opened:
		if req.Protocol != "http" || req.TargetHost != "service.internal" || req.TargetPort != 8080 {
			t.Fatalf("open request=%+v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("stream was not opened after remote validation allowed it")
	}
}

func TestHTTPProxyForwardRemoteValidationDenies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	opened := false
	fwd, err := NewHTTPProxyForward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		opened = true
		return nil, io.EOF
	}), HTTPProxyForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: "agent-a", AuthURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fwd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()

	conn, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := "GET http://service.internal:8080/ HTTP/1.1\r\nHost: service.internal:8080\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
	if opened {
		t.Fatal("stream was opened after remote validation denied it")
	}
}

func TestHTTPProxyForwardCloseClosesRemoteValidatorIdleConnections(t *testing.T) {
	transport := &closeIdleTransport{roundTrip: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
	})}
	fwd, err := NewHTTPProxyForward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		return nil, io.EOF
	}), HTTPProxyForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	fwd.validator = &RemoteValidator{
		endpoint: "http://auth.internal/validate",
		client:   &http.Client{Transport: transport},
	}

	if err := fwd.Close(); err != nil {
		t.Fatal(err)
	}

	if !transport.closedIdle {
		t.Fatal("HTTP proxy forward Close did not close remote validator idle connections")
	}
}
