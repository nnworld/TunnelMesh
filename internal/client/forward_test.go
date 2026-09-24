package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
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

type delayedRemoteStream struct {
	release   chan struct{}
	remaining []byte
	closed    chan struct{}
}

func (s *delayedRemoteStream) Read(p []byte) (int, error) {
	<-s.release
	if len(s.remaining) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.remaining)
	s.remaining = s.remaining[n:]
	return n, nil
}

func (*delayedRemoteStream) Write(p []byte) (int, error) { return len(p), nil }
func (*delayedRemoteStream) CloseWrite() error           { return nil }
func (s *delayedRemoteStream) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

type bridgeLocalStream struct {
	requests       chan []byte
	responses      chan []byte
	closeResponses sync.Once
}

func (s *bridgeLocalStream) Read(p []byte) (int, error) {
	data, ok := <-s.requests
	if !ok {
		return 0, io.EOF
	}
	return copy(p, data), nil
}

func (s *bridgeLocalStream) Write(p []byte) (int, error) {
	s.responses <- append([]byte(nil), p...)
	return len(p), nil
}

func (s *bridgeLocalStream) CloseWrite() error {
	s.closeResponses.Do(func() { close(s.responses) })
	return nil
}

func (s *bridgeLocalStream) Close() error { return s.CloseWrite() }

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

func TestBridgeWaitsForDelayedResponseAfterLocalHalfClose(t *testing.T) {
	response := bytes.Repeat([]byte("delayed-response"), 32*1024)
	remote := &delayedRemoteStream{
		release:   make(chan struct{}),
		remaining: response,
		closed:    make(chan struct{}),
	}
	requests := make(chan []byte, 1)
	requests <- []byte("request")
	close(requests)
	local := &bridgeLocalStream{
		requests:  requests,
		responses: make(chan []byte),
	}
	done := make(chan error, 1)
	go func() { done <- bridge(local, remote) }()

	time.AfterFunc(1500*time.Millisecond, func() { close(remote.release) })
	received := make([]byte, 0, len(response))
	for data := range local.responses {
		received = append(received, data...)
	}
	if !bytes.Equal(received, response) {
		t.Fatalf("received %d bytes, want %d", len(received), len(response))
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("bridge error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not finish")
	}
}

func TestSessionOpenStreamResultWaitsForStrictOpenResult(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 2), recv: make(chan protocol.Frame, 2), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenStrict)
	if session.OpenMode() != SessionOpenStrict {
		t.Fatalf("OpenMode()=%v, want strict", session.OpenMode())
	}

	type openResult struct {
		stream io.ReadWriteCloser
		result protocol.OpenResultPayload
		err    error
	}
	resultCh := make(chan openResult, 1)
	go func() {
		stream, result, err := session.OpenStreamResult(context.Background(), StreamRequest{
			StreamID: 7, AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 22,
		})
		resultCh <- openResult{stream: stream, result: result, err: err}
	}()

	select {
	case open := <-tr.sent:
		if open.Type != protocol.FrameOpenStream || open.Flags&protocol.FlagStrictOpen == 0 || open.StreamID != 7 {
			t.Fatalf("OPEN frame=%+v, want strict stream 7", open)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OPEN frame")
	}
	select {
	case got := <-resultCh:
		t.Fatalf("OpenStreamResult returned before OPEN_RESULT: %+v", got)
	default:
	}

	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 7, Payload: []byte("early")}
	payload, err := protocol.EncodeOpenResultPayload(protocol.OpenResultPayload{
		Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: 7, Payload: payload}

	select {
	case got := <-resultCh:
		if got.err != nil || got.stream == nil || !got.result.Accepted || got.result.Code != protocol.OpenResultCodeOK {
			t.Fatalf("result stream=%v result=%+v err=%v", got.stream, got.result, got.err)
		}
		buffer := make([]byte, len("early"))
		if _, err := io.ReadFull(got.stream, buffer); err != nil || string(buffer) != "early" {
			t.Fatalf("early data=%q err=%v", buffer, err)
		}
		_ = got.stream.Close()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for successful OPEN_RESULT")
	}
}

func TestSessionOpenStreamResultFlowControlDeliversDataAfterResult(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 2), recv: make(chan protocol.Frame, 2), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenFlowControl)
	type openResult struct {
		stream io.ReadWriteCloser
		err    error
	}
	resultCh := make(chan openResult, 1)
	go func() {
		stream, _, err := session.OpenStreamResult(context.Background(), StreamRequest{
			StreamID: 7, AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 22,
		})
		resultCh <- openResult{stream: stream, err: err}
	}()
	open := <-tr.sent
	if open.Window == 0 {
		t.Fatalf("OPEN window=%d, want flow-control window", open.Window)
	}
	payload, err := protocol.EncodeOpenResultPayload(protocol.OpenResultPayload{
		Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: 7, Payload: payload}
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 7, Payload: []byte("flow-data")}
	select {
	case got := <-resultCh:
		if got.err != nil || got.stream == nil {
			t.Fatalf("result stream=%v err=%v", got.stream, got.err)
		}
		buffer := make([]byte, len("flow-data"))
		if _, err := io.ReadFull(got.stream, buffer); err != nil || string(buffer) != "flow-data" {
			t.Fatalf("data=%q err=%v, want flow-data", buffer, err)
		}
		_ = got.stream.Close()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for successful OPEN_RESULT")
	}
}

func TestSessionOpenerOpenStreamUsesStrictResultForStrictSession(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 2), recv: make(chan protocol.Frame, 2), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenStrict)
	resultCh := make(chan io.ReadWriteCloser, 1)
	go func() {
		stream, err := NewSessionOpener(session).OpenStream(context.Background(), StreamRequest{
			StreamID: 13, AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 22,
		})
		if err != nil {
			t.Errorf("OpenStream() error=%v", err)
		}
		resultCh <- stream
	}()

	open := <-tr.sent
	if open.Type != protocol.FrameOpenStream || open.Flags&protocol.FlagStrictOpen == 0 || open.StreamID != 13 {
		t.Fatalf("OPEN frame=%+v, want strict stream 13", open)
	}
	select {
	case stream := <-resultCh:
		_ = stream.Close()
		t.Fatal("OpenStream returned before OPEN_RESULT")
	default:
	}
	payload, err := protocol.EncodeOpenResultPayload(protocol.OpenResultPayload{
		Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: 13, Payload: payload}
	select {
	case stream := <-resultCh:
		if stream == nil {
			t.Fatal("strict OpenStream did not return a stream")
		}
		_ = stream.Close()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for successful OPEN_RESULT")
	}
}

func TestSessionOpenStreamResultTimeoutResetsOnlyCurrentStream(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 2), recv: make(chan protocol.Frame, 2), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenStrict)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stream, result, err := session.OpenStreamResult(ctx, StreamRequest{StreamID: 9, AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 22})
	if stream != nil || err == nil || result.Accepted || result.Code != protocol.OpenResultCodeTimeout || !result.Retryable {
		t.Fatalf("stream=%v result=%+v err=%v, want retryable timeout", stream, result, err)
	}
	open := <-tr.sent
	if open.Type != protocol.FrameOpenStream || open.StreamID != 9 {
		t.Fatalf("OPEN frame=%+v, want stream 9", open)
	}
	if frame := <-tr.sent; frame.Type != protocol.FrameReset || frame.StreamID != 9 {
		t.Fatalf("timeout frame=%+v, want RESET for stream 9", frame)
	}
}

func TestSessionOpenStreamResultDuplicateResultResetsStream(t *testing.T) {
	tr := &receiveTransport{sent: make(chan protocol.Frame, 3), recv: make(chan protocol.Frame, 3), done: make(chan struct{})}
	session := NewSessionWithOpenMode(tr, SessionOpenStrict)
	resultCh := make(chan io.ReadWriteCloser, 1)
	go func() {
		stream, result, err := session.OpenStreamResult(context.Background(), StreamRequest{StreamID: 11, AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 22})
		if err != nil || !result.Accepted {
			t.Errorf("OpenStreamResult result=%+v err=%v", result, err)
		}
		resultCh <- stream
	}()
	open := <-tr.sent
	payload, err := protocol.EncodeOpenResultPayload(protocol.OpenResultPayload{Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK})
	if err != nil {
		t.Fatal(err)
	}
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: open.StreamID, Payload: payload}
	stream := <-resultCh
	if stream == nil {
		t.Fatal("successful result did not expose stream")
	}
	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: open.StreamID, Payload: payload}
	if frame := <-tr.sent; frame.Type != protocol.FrameReset || frame.StreamID != open.StreamID {
		t.Fatalf("duplicate result frame=%+v, want stream RESET", frame)
	}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, protocol.ErrInvalidFrame) {
		t.Fatalf("stream Read() after duplicate result error=%v, want ErrInvalidFrame", err)
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

// gatedUpgradeStream behaves like a live upgraded tunnel: it answers the HTTP
// response, then blocks in Read until release is closed, and records whether its
// write side was half-closed instead of killed.
type gatedUpgradeStream struct {
	mu         sync.Mutex
	writes     bytes.Buffer
	header     []byte
	body       []byte
	headerRead bool
	bodyRead   bool
	release    chan struct{}
	closed     chan struct{}
	halfClosed chan struct{}
	once       sync.Once
	onceBody   sync.Once
}

func newGatedUpgradeStream(header, body []byte) *gatedUpgradeStream {
	return &gatedUpgradeStream{
		header: header, body: body,
		release: make(chan struct{}), closed: make(chan struct{}), halfClosed: make(chan struct{}),
	}
}

func (s *gatedUpgradeStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if !s.headerRead && len(s.header) > 0 {
		s.headerRead = true
		n := copy(p, s.header)
		s.mu.Unlock()
		return n, nil
	}
	if !s.bodyRead && len(s.body) > 0 {
		s.mu.Unlock()
		select {
		case <-s.release:
		case <-s.closed:
			return 0, io.EOF
		}
		s.mu.Lock()
		if s.bodyRead {
			s.mu.Unlock()
			<-s.closed
			return 0, io.EOF
		}
		s.bodyRead = true
		n := copy(p, s.body)
		s.mu.Unlock()
		return n, nil
	}
	s.mu.Unlock()
	if len(s.body) == 0 {
		return 0, io.EOF
	}
	<-s.closed
	return 0, io.EOF
}

func (s *gatedUpgradeStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.closed:
		return 0, io.ErrClosedPipe
	default:
	}
	return s.writes.Write(p)
}

func (s *gatedUpgradeStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *gatedUpgradeStream) CloseWrite() error {
	select {
	case <-s.halfClosed:
	default:
		close(s.halfClosed)
	}
	return nil
}

func (s *gatedUpgradeStream) Written() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.writes.Bytes()...)
}

func (s *gatedUpgradeStream) releaseBody() { s.onceBody.Do(func() { close(s.release) }) }

func (s *gatedUpgradeStream) halfClosedWithin(timeout time.Duration) bool {
	select {
	case <-s.halfClosed:
		return true
	case <-time.After(timeout):
		return false
	}
}

// upgradeResponseWriter is the minimal http.ResponseWriter a hijacked upgrade
// needs: Header and Write come from the recorder so the error paths stay
// readable, while Hijack hands back a pipe plus exactly the bytes a browser
// pipelined behind its upgrade request.
type upgradeResponseWriter struct {
	*httptest.ResponseRecorder
	conn     net.Conn
	leftover *bufio.Reader
}

func (w *upgradeResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(w.leftover, bufio.NewWriter(w.conn)), nil
}

func newUpgradeResponseWriter(t *testing.T, leftover []byte) (*upgradeResponseWriter, net.Conn) {
	t.Helper()
	// A loopback pair, not net.Pipe: the handler half-closes the browser side,
	// which only a real *net.TCPConn can do.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			conn = nil
		}
		accepted <- conn
	}()
	localSide, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	handlerSide := <-accepted
	listener.Close()
	if handlerSide == nil {
		t.Fatal("upgrade connection was never accepted")
	}
	reader := bufio.NewReader(bytes.NewReader(leftover))
	if len(leftover) > 0 {
		// net/http has already read from the socket by the time it hands the
		// connection over, so prime the buffer the same way instead of testing
		// against an empty one.
		if _, err := reader.Peek(len(leftover)); err != nil {
			t.Fatal(err)
		}
	}
	return &upgradeResponseWriter{
		ResponseRecorder: httptest.NewRecorder(),
		conn:             handlerSide,
		leftover:         reader,
	}, localSide
}

// upgradeLocal accumulates what the handler writes so a test can wait for one
// sequence without dropping the bytes that arrived with it.
type upgradeLocal struct {
	conn net.Conn
	buf  []byte
}

func (l *upgradeLocal) waitFor(t *testing.T, needle string, timeout time.Duration) string {
	t.Helper()
	chunk := make([]byte, 512)
	deadline := time.Now().Add(timeout)
	for !strings.Contains(string(l.buf), needle) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q, read %q", needle, l.buf)
		}
		_ = l.conn.SetReadDeadline(deadline)
		n, err := l.conn.Read(chunk)
		l.buf = append(l.buf, chunk[:n]...)
		if err != nil {
			t.Fatalf("local upgrade connection while waiting for %q: %v (read %q)", needle, err, l.buf)
		}
	}
	return string(l.buf)
}

const upgradeResponse = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"

func newUpgradeRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://service/chat", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	return req
}

func TestBufferedStreamCloseWriteDelegates(t *testing.T) {
	stream := newGatedUpgradeStream(nil, nil)
	covered := &bufferedStream{Reader: strings.NewReader(""), Writer: stream, closer: stream}
	if err := covered.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	if !stream.halfClosedWithin(time.Second) {
		t.Fatal("CloseWrite did not reach the wrapped stream, so bridge will close both directions")
	}
	plain := &bufferedStream{Reader: strings.NewReader(""), Writer: io.Discard, closer: stream}
	if err := plain.CloseWrite(); err == nil {
		t.Fatal("a writer without half-close must say so instead of pretending the direction stayed open")
	}
}

func TestForwardHTTPUpgradeForwardsBufferedClientBytes(t *testing.T) {
	remote := newTestStream([]byte(upgradeResponse))
	writer, localSide := newUpgradeResponseWriter(t, []byte("early-frame"))
	local := &upgradeLocal{conn: localSide}
	done := make(chan struct{})
	go func() {
		defer close(done)
		forwardHTTP(writer, newUpgradeRequest(), remote)
	}()
	local.waitFor(t, "101 Switching Protocols", 2*time.Second)
	_ = localSide.Close()
	<-done
	if !strings.Contains(string(remote.Written()), "early-frame") {
		t.Fatalf("bytes the server had already buffered were dropped: %q", remote.Written())
	}
}

func TestForwardHTTPUpgradeKeepsDownloadOpenAfterLocalHalfClose(t *testing.T) {
	remote := newGatedUpgradeStream([]byte(upgradeResponse), []byte("late-response"))
	writer, localSide := newUpgradeResponseWriter(t, nil)
	local := &upgradeLocal{conn: localSide}
	done := make(chan struct{})
	go func() {
		defer close(done)
		forwardHTTP(writer, newUpgradeRequest(), remote)
	}()
	local.waitFor(t, "101 Switching Protocols", 2*time.Second)
	if _, err := localSide.Write([]byte("upload")); err != nil {
		t.Fatalf("local write: %v", err)
	}
	halfCloser, ok := localSide.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("the test connection must support half-close")
	}
	if err := halfCloser.CloseWrite(); err != nil {
		t.Fatalf("local CloseWrite: %v", err)
	}
	if !remote.halfClosedWithin(500 * time.Millisecond) {
		t.Fatal("the local half-close never reached the tunnel stream")
	}
	remote.releaseBody()
	local.waitFor(t, "late-response", 2*time.Second)
	if !strings.Contains(string(remote.Written()), "upload") {
		t.Fatalf("uploaded bytes never reached the tunnel: %q", remote.Written())
	}
	_ = remote.Close()
	_ = localSide.Close()
	<-done
}

func TestForwardHTTPLogsTruncatedResponseBody(t *testing.T) {
	// The target advertises more body than it ever delivers, so the browser only
	// sees ERR_CONTENT_LENGTH_MISMATCH. Without a log line that failure is
	// invisible on both ends, which is how the truncated-download reports went
	// unexplained.
	remote := newTestStream([]byte("HTTP/1.1 200 OK\r\nContent-Length: 11\r\n\r\nshort"))
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	forwardHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://service/assets/app.js", nil), remote)
	message := logs.String()
	for _, want := range []string{"client_response_truncated", "level=WARN", "status_code=200", "content_length=11", "bytes_copied=5"} {
		if !strings.Contains(message, want) {
			t.Fatalf("log missing %q, got: %s", want, message)
		}
	}
}

// fixedClock makes "used at the same instant" testable, which is the only case
// where a full association table must refuse rather than evict.
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixedClock) nowFunc() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(by)
}

func newFixedClock(start time.Time) *fixedClock { return &fixedClock{now: start} }

func udpSource(port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: port}
}

func TestUDPAssociationLimitEvictsLeastRecentlyUsed(t *testing.T) {
	var created []*testDatagram
	clock := newFixedClock(time.Unix(1777000000, 0))
	m, err := NewUDPAssociationManager(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		stream := newTestDatagram()
		created = append(created, stream)
		return stream, nil
	}), UDPAssociationConfig{TargetHost: "service", TargetPort: 5353, IdleTimeout: time.Minute, MaxAssociations: 2})
	if err != nil {
		t.Fatal(err)
	}
	m.now = clock.nowFunc
	for _, port := range []int{1111, 2222, 3333} {
		clock.advance(time.Second)
		if err := m.HandleDatagram(context.Background(), udpSource(port), []byte("ping")); err != nil {
			t.Fatalf("source %d: %v", port, err)
		}
	}
	if m.Len() != 2 {
		t.Fatalf("associations=%d, want the limit of 2 to hold", m.Len())
	}
	select {
	case <-created[0].closed:
	default:
		t.Fatal("the oldest association was not closed to make room")
	}
	if err := created[1].WriteDatagram(nil); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Second)
	if err := m.HandleDatagram(context.Background(), udpSource(2222), []byte("again")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-created[2].closed:
		t.Fatal("a source that is still being used must outrank the newest idle entry")
	default:
	}
}

func TestUDPAssociationLimitDropsWhenNothingIdle(t *testing.T) {
	opened := 0
	clock := newFixedClock(time.Unix(1777000000, 0))
	m, err := NewUDPAssociationManager(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
		opened++
		return newTestDatagram(), nil
	}), UDPAssociationConfig{TargetHost: "service", TargetPort: 5353, IdleTimeout: time.Minute, MaxAssociations: 2})
	if err != nil {
		t.Fatal(err)
	}
	m.now = clock.nowFunc
	for _, port := range []int{1111, 2222} {
		if err := m.HandleDatagram(context.Background(), udpSource(port), []byte("ping")); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.HandleDatagram(context.Background(), udpSource(3333), []byte("flood")); !errors.Is(err, ErrTooManyUDPAssociations) {
		t.Fatalf("error = %v, want %v", err, ErrTooManyUDPAssociations)
	}
	if opened != 2 || m.Len() != 2 {
		t.Fatalf("opened=%d associations=%d, want the refused packet to open nothing", opened, m.Len())
	}
}
