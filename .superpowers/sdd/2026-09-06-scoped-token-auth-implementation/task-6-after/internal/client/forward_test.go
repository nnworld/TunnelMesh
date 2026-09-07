package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

type receiveTransport struct {
	sent chan protocol.Frame
	recv chan protocol.Frame
	done chan struct{}
}

func (t *receiveTransport) Send(f protocol.Frame) error { t.sent <- f; return nil }
func (t *receiveTransport) Receive() (protocol.Frame, error) {
	select {
	case f := <-t.recv:
		return f, nil
	case <-t.done:
		return protocol.Frame{}, io.EOF
	}
}
func (t *receiveTransport) Close() error {
	select {
	case <-t.done:
	default:
		close(t.done)
	}
	return nil
}

type testStream struct {
	mu     sync.Mutex
	read   *bytes.Buffer
	writes bytes.Buffer
	closed chan struct{}
}

type closeWriteStream struct {
	*testStream
	muClosedWrite sync.Mutex
	closedWrite   bool
}

func (s *closeWriteStream) CloseWrite() error {
	s.muClosedWrite.Lock()
	s.closedWrite = true
	s.muClosedWrite.Unlock()
	return nil
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

type datagramOpenerFunc func(context.Context, StreamRequest) (DatagramStream, error)

func (f datagramOpenerFunc) OpenStream(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
	return nil, errors.New("datagram opener")
}
func (f datagramOpenerFunc) OpenDatagram(ctx context.Context, req StreamRequest) (DatagramStream, error) {
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

func TestUDPAssociationUsesDatagramMessagesWithoutByteFraming(t *testing.T) {
	d := newTestDatagram()
	m, err := NewUDPAssociationManager(datagramOpenerFunc(func(_ context.Context, req StreamRequest) (DatagramStream, error) {
		if req.Protocol != "udp" {
			t.Fatalf("protocol=%q", req.Protocol)
		}
		return d, nil
	}), UDPAssociationConfig{TargetHost: "service", TargetPort: 5353})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.HandleDatagram(context.Background(), &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}, []byte("one")); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.writes) != 1 || string(d.writes[0]) != "one" {
		t.Fatalf("writes=%q", d.writes)
	}
}

func TestUDPAssociationDoesNotHoldManagerLockWhileOpening(t *testing.T) {
	started := make(chan StreamRequest, 2)
	release := make(chan struct{})
	d := newTestDatagram()
	m, err := NewUDPAssociationManager(datagramOpenerFunc(func(_ context.Context, req StreamRequest) (DatagramStream, error) {
		started <- req
		<-release
		return d, nil
	}), UDPAssociationConfig{TargetHost: "service", TargetPort: 5353})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		done <- m.HandleDatagram(context.Background(), &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}, []byte("one"))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first open did not start")
	}
	go func() {
		done <- m.HandleDatagram(context.Background(), &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2}, []byte("two"))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("second source was serialized behind first opener")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
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

func TestHTTPForwardCanCloseImmediatelyAfterStart(t *testing.T) {
	for i := 0; i < 100; i++ {
		fwd, err := NewHTTPForward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
			return nil, errors.New("unused")
		}), HTTPForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "a", TargetHost: "service", TargetPort: 8080})
		if err != nil {
			t.Fatal(err)
		}
		if err := fwd.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := fwd.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	}
}

func TestHTTPForwardUpgradeBridgesRawBytes(t *testing.T) {
	remote := newTestStream([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\nremote-data"))
	fwd, err := NewHTTPForward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
		if req.Protocol != "http" {
			t.Fatalf("protocol=%q", req.Protocol)
		}
		return remote, nil
	}), HTTPForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "a", TargetHost: "service", TargetPort: 8080})
	if err != nil {
		t.Fatal(err)
	}
	if err := fwd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()
	c, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = io.WriteString(c, "GET /chat HTTP/1.1\r\nHost: service\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	var got strings.Builder
	buf := make([]byte, 256)
	for {
		n, err := c.Read(buf)
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

func TestProxyStdioNilContextAndEOFDoNotBlock(t *testing.T) {
	remote := newTestStream(nil)
	var out bytes.Buffer
	if err := ProxyStdio(nil, strings.NewReader(""), &out, remote); err != nil {
		t.Fatal(err)
	}
}

func TestProxyStdioHalfClosesStreamAfterStdinEOF(t *testing.T) {
	remote := &closeWriteStream{testStream: newTestStream([]byte("reply"))}
	var out bytes.Buffer
	if err := ProxyStdio(context.Background(), strings.NewReader("request"), &out, remote); err != nil {
		t.Fatal(err)
	}
	remote.muClosedWrite.Lock()
	closedWrite := remote.closedWrite
	remote.muClosedWrite.Unlock()
	if !closedWrite || out.String() != "reply" || string(remote.Written()) != "request" {
		t.Fatalf("closeWrite=%v out=%q written=%q", closedWrite, out.String(), remote.Written())
	}
}

func TestSessionStreamDrainsDataQueuedBeforeHalfCloseAndIncludesAgentID(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 2), recv: make(chan protocol.Frame, 2), done: make(chan struct{})}
	s := NewSession(tr)
	stream, err := s.OpenStreamConn(context.Background(), StreamRequest{AgentID: "agent-7", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	open := <-tr.sent
	var payload StreamOpenPayload
	if err := json.Unmarshal(open.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AgentID != "agent-7" {
		t.Fatalf("agent id=%q", payload.AgentID)
	}
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("tail")}
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}
	got := make([]byte, 4)
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "tail" {
		t.Fatalf("got=%q", got)
	}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal err=%v", err)
	}
}

func TestSessionDatagramDrainsQueuedDataBeforeHalfClose(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 2), recv: make(chan protocol.Frame, 2), done: make(chan struct{})}
	s := NewSession(tr)
	ds, err := s.OpenDatagram(context.Background(), StreamRequest{Protocol: "udp", TargetHost: "h", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	open := <-tr.sent
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("datagram")}
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}
	got, err := ds.ReadDatagram()
	if err != nil || string(got) != "datagram" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := ds.ReadDatagram(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal err=%v", err)
	}
}

func TestSessionHalfCloseWriteKeepsRemoteResponseReadable(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 3), recv: make(chan protocol.Frame, 2), done: make(chan struct{})}
	s := NewSession(tr)
	stream, err := s.OpenStreamConn(context.Background(), StreamRequest{Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	open := <-tr.sent
	if c, ok := stream.(interface{ CloseWrite() error }); !ok {
		t.Fatal("stream does not expose directional half-close")
	} else if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	closeFrame := <-tr.sent
	if closeFrame.Type != protocol.FrameHalfClose {
		t.Fatalf("frame=%+v", closeFrame)
	}
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("delayed-reply")}
	got := make([]byte, len("delayed-reply"))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "delayed-reply" {
		t.Fatalf("got=%q", got)
	}
}

func TestSessionRemoteHalfCloseKeepsLocalWriteSideOpen(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 3), recv: make(chan protocol.Frame, 2), done: make(chan struct{})}
	s := NewSession(tr)
	stream, err := s.OpenStreamConn(context.Background(), StreamRequest{Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	open := <-tr.sent
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read() error = %v, want EOF", err)
	}
	if _, err := stream.Write([]byte("final-request")); err != nil {
		t.Fatalf("Write() after remote half-close error = %v", err)
	}
	data := <-tr.sent
	if data.Type != protocol.FrameData || string(data.Payload) != "final-request" {
		t.Fatalf("DATA frame = %+v", data)
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
