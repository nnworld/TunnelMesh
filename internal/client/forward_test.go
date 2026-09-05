package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type testStream struct {
	mu     sync.Mutex
	read   *bytes.Buffer
	writes bytes.Buffer
	closed chan struct{}
}

func newTestStream(read []byte) *testStream {
	return &testStream{read: bytes.NewBuffer(read), closed: make(chan struct{})}
}
func (s *testStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.read.Len() == 0 {
		return 0, io.EOF
	}
	return s.read.Read(p)
}
func (s *testStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes.Write(p)
}
func (s *testStream) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}
func (s *testStream) Written() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.writes.Bytes()...)
}

type openerFunc func(context.Context, StreamRequest) (io.ReadWriteCloser, error)

func (f openerFunc) OpenStream(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
	return f(ctx, req)
}

func TestTCPForwardListenerLifecycleAndByteCopy(t *testing.T) {
	remote := newTestStream([]byte("remote-reply"))
	opened := make(chan StreamRequest, 1)
	fwd, err := NewTCPForward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		opened <- req
		return remote, nil
	}), TCPForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "agent-a", TargetHost: "127.0.0.1", TargetPort: 8080})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fwd.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()
	c, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("local-request")); err != nil {
		t.Fatal(err)
	}
	req := <-opened
	if req.Protocol != "tcp" || req.AgentID != "agent-a" || req.TargetPort != 8080 {
		t.Fatalf("unexpected open request: %+v", req)
	}
	deadline := time.Now().Add(time.Second)
	for len(remote.Written()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := make([]byte, len("remote-reply"))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "remote-reply" {
		t.Fatalf("reply=%q", got)
	}
	if string(remote.Written()) != "local-request" {
		t.Fatalf("remote received=%q", remote.Written())
	}
	if err := fwd.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialTimeout("tcp", fwd.Addr().String(), 50*time.Millisecond); err == nil {
		t.Fatal("listener still accepting after Close")
	}
}

type testDatagram struct {
	mu     sync.Mutex
	writes [][]byte
	reads  chan []byte
	closed chan struct{}
}

func newTestDatagram() *testDatagram {
	return &testDatagram{reads: make(chan []byte, 4), closed: make(chan struct{})}
}
func (d *testDatagram) WriteDatagram(p []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes = append(d.writes, append([]byte(nil), p...))
	return nil
}
func (d *testDatagram) ReadDatagram() ([]byte, error) {
	select {
	case p := <-d.reads:
		return p, nil
	case <-d.closed:
		return nil, io.EOF
	}
}
func (d *testDatagram) Close() error {
	select {
	case <-d.closed:
	default:
		close(d.closed)
	}
	return nil
}
func (d *testDatagram) Read([]byte) (int, error) { return 0, io.EOF }
func (d *testDatagram) Write(p []byte) (int, error) {
	return len(p), d.WriteDatagram(p)
}

func TestUDPAssociationMapsSourceAndExpiresIdleEntries(t *testing.T) {
	d := newTestDatagram()
	opened := 0
	m, err := NewUDPAssociationManager(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		opened++
		if req.Protocol != "udp" {
			t.Fatalf("protocol=%q", req.Protocol)
		}
		return d, nil
	}), UDPAssociationConfig{TargetHost: "service", TargetPort: 5353, IdleTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}
	if err := m.HandleDatagram(context.Background(), addr, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := m.HandleDatagram(context.Background(), addr, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if opened != 1 || m.Len() != 1 {
		t.Fatalf("opened=%d len=%d", opened, m.Len())
	}
	if removed := m.Expire(time.Now().Add(2 * time.Second)); removed != 1 || m.Len() != 0 {
		t.Fatalf("removed=%d len=%d", removed, m.Len())
	}
}

func TestHTTPForwardUsesLogicalStream(t *testing.T) {
	remote := newTestStream([]byte("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello"))
	fwd, err := NewHTTPForward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		if req.Protocol != "http" || req.TargetHost != "service" || req.TargetPort != 8080 {
			t.Fatalf("request=%+v", req)
		}
		return remote, nil
	}), HTTPForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "a", TargetHost: "service", TargetPort: 8080})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fwd.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()
	resp, err := http.Get("http://" + fwd.Addr().String() + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello" || resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if !strings.Contains(string(remote.Written()), "GET /hello HTTP/1.1") {
		t.Fatalf("upstream request=%q", remote.Written())
	}
}

func TestProxyStdioCopiesBytesBothDirections(t *testing.T) {
	remote := newTestStream([]byte("from-remote"))
	var out bytes.Buffer
	err := ProxyStdio(context.Background(), strings.NewReader("from-stdin"), &out, remote)
	if err != nil {
		t.Fatal(err)
	}
	if string(remote.Written()) != "from-stdin" || out.String() != "from-remote" {
		t.Fatalf("remote=%q out=%q", remote.Written(), out.String())
	}
}

func TestRetryingOpenerReconnectsAfterTransientFailure(t *testing.T) {
	remote := newTestStream(nil)
	attempts := 0
	opener := NewRetryingOpener(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("disconnected")
		}
		return remote, nil
	}), RetryConfig{MaxAttempts: 2, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if _, err := opener.OpenStream(context.Background(), StreamRequest{Protocol: "tcp", TargetHost: "h", TargetPort: 1}); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d", attempts)
	}
}
