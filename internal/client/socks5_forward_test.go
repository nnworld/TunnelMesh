package client

import (
	"bytes"
	"context"
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

func TestSOCKS5ForwardConnectsDynamicTCPTarget(t *testing.T) {
	remote := newTestStream([]byte("remote-reply"))
	opened := make(chan StreamRequest, 1)
	fwd, err := NewSOCKS5Forward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		opened <- req
		return remote, nil
	}), SOCKS5ForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "agent-a"})
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
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	selection := make([]byte, 2)
	if _, err := io.ReadFull(conn, selection); err != nil || !bytes.Equal(selection, []byte{5, 0}) {
		t.Fatalf("method selection=%x err=%v", selection, err)
	}
	request := append([]byte{5, 1, 0, 3, 11}, []byte("example.com")...)
	request = append(request, 0, 80)
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0 {
		t.Fatalf("reply=%x err=%v", reply, err)
	}
	if _, err := conn.Write([]byte("local-request")); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-opened:
		if req.AgentID != "agent-a" || req.Protocol != "tcp" || req.TargetHost != "example.com" || req.TargetPort != 80 {
			t.Fatalf("open request=%+v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("stream was not opened")
	}
	waitForRemoteWrite(t, remote)
	if string(remote.Written()) != "local-request" {
		t.Fatalf("remote received=%q", remote.Written())
	}
}

func TestSOCKS5ForwardRejectsUnsupportedMethodWithoutOpeningStream(t *testing.T) {
	opened := false
	fwd, err := NewSOCKS5Forward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		opened = true
		return nil, io.EOF
	}), SOCKS5ForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "agent-a"})
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
	if _, err := conn.Write([]byte{5, 1, 1}); err != nil {
		t.Fatal(err)
	}
	selection := make([]byte, 2)
	if _, err := io.ReadFull(conn, selection); err != nil || !bytes.Equal(selection, []byte{5, 0xff}) {
		t.Fatalf("method selection=%x err=%v", selection, err)
	}
	if opened {
		t.Fatal("stream was opened for an unsupported method")
	}
}

func TestSOCKS5ForwardRejectsUnsupportedCommandWithoutOpeningStream(t *testing.T) {
	opened := false
	fwd, err := NewSOCKS5Forward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		opened = true
		return nil, io.EOF
	}), SOCKS5ForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "agent-a"})
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
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	selection := make([]byte, 2)
	if _, err := io.ReadFull(conn, selection); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{5, 3, 0, 1, 127, 0, 0, 1, 0, 80}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 7 {
		t.Fatalf("reply=%x err=%v", reply, err)
	}
	if opened {
		t.Fatal("stream was opened for an unsupported command")
	}
}

func TestSOCKS5ForwardRejectsNonLoopbackWithoutExplicitAllowRemote(t *testing.T) {
	_, err := NewSOCKS5Forward(nil, SOCKS5ForwardConfig{ListenAddr: "0.0.0.0:1080", AgentID: "agent-a"})
	if err == nil {
		t.Fatal("non-loopback listener was accepted without --allow-remote")
	}
}

func TestSOCKS5ForwardRequiresPasswordAuthForNonLoopback(t *testing.T) {
	_, err := NewSOCKS5Forward(nil, SOCKS5ForwardConfig{
		ListenAddr: "0.0.0.0:1080", AgentID: "agent-a", AllowRemote: true,
	})
	if err == nil {
		t.Fatal("non-loopback no-auth listener was accepted")
	}
	_, err = NewSOCKS5Forward(nil, SOCKS5ForwardConfig{
		ListenAddr: "0.0.0.0:1080", AgentID: "agent-a", AllowRemote: true, AuthMode: SOCKS5AuthPassword,
	})
	if err == nil {
		t.Fatal("password mode was accepted without credentials")
	}
}

func TestSOCKS5ForwardRejectsOversizedCredentials(t *testing.T) {
	_, err := NewSOCKS5Forward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		return nil, io.EOF
	}), SOCKS5ForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: "agent-a", AuthMode: SOCKS5AuthPassword,
		Username: strings.Repeat("a", 256), Password: "secret",
	})
	if err == nil {
		t.Fatal("oversized SOCKS5 username was accepted")
	}
	_, err = NewSOCKS5Forward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		return nil, io.EOF
	}), SOCKS5ForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: "agent-a", AuthMode: SOCKS5AuthPassword,
		Username: "alice", Password: strings.Repeat("a", 256),
	})
	if err == nil {
		t.Fatal("oversized SOCKS5 password was accepted")
	}
}

func TestSOCKS5ForwardUsernamePasswordAuthSucceeds(t *testing.T) {
	remote := newTestStream(nil)
	opened := make(chan StreamRequest, 1)
	fwd, err := NewSOCKS5Forward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		opened <- req
		return remote, nil
	}), SOCKS5ForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: "agent-a", AuthMode: SOCKS5AuthPassword,
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
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	selection := make([]byte, 2)
	if _, err := io.ReadFull(conn, selection); err != nil || !bytes.Equal(selection, []byte{5, 2}) {
		t.Fatalf("method selection=%x err=%v", selection, err)
	}
	credentials := append([]byte{1, 5}, []byte("alice")...)
	credentials = append(credentials, 6)
	credentials = append(credentials, []byte("secret")...)
	if _, err := conn.Write(credentials); err != nil {
		t.Fatal(err)
	}
	authReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, authReply); err != nil || !bytes.Equal(authReply, []byte{1, 0}) {
		t.Fatalf("auth reply=%x err=%v", authReply, err)
	}
	if _, err := conn.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 80}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0 {
		t.Fatalf("reply=%x err=%v", reply, err)
	}
	select {
	case req := <-opened:
		if req.Protocol != "tcp" || req.TargetHost != "127.0.0.1" || req.TargetPort != 80 {
			t.Fatalf("open request=%+v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("stream was not opened after valid credentials")
	}
}

func TestSOCKS5ForwardUsernamePasswordAuthFailsWithoutOpeningStream(t *testing.T) {
	opened := false
	fwd, err := NewSOCKS5Forward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		opened = true
		return nil, io.EOF
	}), SOCKS5ForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: "agent-a", AuthMode: SOCKS5AuthPassword,
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
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	selection := make([]byte, 2)
	if _, err := io.ReadFull(conn, selection); err != nil {
		t.Fatal(err)
	}
	credentials := append([]byte{1, 5}, []byte("alice")...)
	credentials = append(credentials, 5)
	credentials = append(credentials, []byte("wrong")...)
	if _, err := conn.Write(credentials); err != nil {
		t.Fatal(err)
	}
	authReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, authReply); err != nil || !bytes.Equal(authReply, []byte{1, 1}) {
		t.Fatalf("auth reply=%x err=%v", authReply, err)
	}
	if opened {
		t.Fatal("stream was opened after invalid credentials")
	}
}

func TestSOCKS5ForwardClosesActiveConnections(t *testing.T) {
	remote := newBlockingStream()
	fwd, err := NewSOCKS5Forward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		return remote, nil
	}), SOCKS5ForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fwd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	selection := make([]byte, 2)
	if _, err := io.ReadFull(conn, selection); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 80}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if err := fwd.Close(); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != io.EOF {
		t.Fatalf("active connection read error=%v, want EOF", err)
	}
}

func TestSOCKS5ForwardRemoteValidationAllows(t *testing.T) {
	var got RemoteValidationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	remote := newTestStream(nil)
	opened := make(chan StreamRequest, 1)
	fwd, err := NewSOCKS5Forward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		opened <- req
		return remote, nil
	}), SOCKS5ForwardConfig{
		ListenAddr: "127.0.0.1:0", AgentID: "agent-a", AuthMode: SOCKS5AuthPassword,
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
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	selection := make([]byte, 2)
	if _, err := io.ReadFull(conn, selection); err != nil {
		t.Fatal(err)
	}
	credentials := append([]byte{1, 5}, []byte("alice")...)
	credentials = append(credentials, 6)
	credentials = append(credentials, []byte("secret")...)
	if _, err := conn.Write(credentials); err != nil {
		t.Fatal(err)
	}
	authReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, authReply); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 80}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0 {
		t.Fatalf("reply=%x, want success", reply)
	}
	want := RemoteValidationRequest{
		Protocol: "socks5", AgentID: "agent-a", TargetHost: "127.0.0.1", TargetPort: 80,
		Username: "alice", Password: "secret",
	}
	if got != want {
		t.Fatalf("remote request=%+v, want %+v", got, want)
	}
	select {
	case req := <-opened:
		if req.Protocol != "tcp" || req.TargetHost != "127.0.0.1" || req.TargetPort != 80 {
			t.Fatalf("open request=%+v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("stream was not opened after remote validation allowed it")
	}
}

func TestSOCKS5ForwardRemoteValidationDenies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	opened := false
	fwd, err := NewSOCKS5Forward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		opened = true
		return nil, io.EOF
	}), SOCKS5ForwardConfig{
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
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	selection := make([]byte, 2)
	if _, err := io.ReadFull(conn, selection); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 80}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 2 {
		t.Fatalf("reply=%x, want connection not allowed", reply)
	}
	if opened {
		t.Fatal("stream was opened after remote validation denied it")
	}
}

func TestSOCKS5ForwardCloseClosesRemoteValidatorIdleConnections(t *testing.T) {
	transport := &closeIdleTransport{roundTrip: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
	})}
	fwd, err := NewSOCKS5Forward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		return nil, io.EOF
	}), SOCKS5ForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "agent-a"})
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
		t.Fatal("SOCKS5 forward Close did not close remote validator idle connections")
	}
}

func waitForRemoteWrite(t *testing.T, stream interface{ Written() []byte }) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(stream.Written()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(stream.Written()) == 0 {
		t.Fatal("remote stream received no data")
	}
}

type blockingStream struct {
	mu     sync.Mutex
	writes bytes.Buffer
	closed chan struct{}
}

func newBlockingStream() *blockingStream {
	return &blockingStream{closed: make(chan struct{})}
}

func (s *blockingStream) Read([]byte) (int, error) {
	<-s.closed
	return 0, io.EOF
}

func (s *blockingStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes.Write(p)
}

func (s *blockingStream) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}
