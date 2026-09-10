package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"golang.org/x/net/websocket"
	"io"
	"math/big"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingTransport struct{ closed chan struct{} }

func (t *blockingTransport) Send(protocol.Frame) error { return nil }
func (t *blockingTransport) Receive() (protocol.Frame, error) {
	<-t.closed
	return protocol.Frame{}, errors.New("closed")
}
func (t *blockingTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}
func TestSessionRunContextCancelClosesReceive(t *testing.T) {
	tr := &blockingTransport{closed: make(chan struct{})}
	s := NewSession(tr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not unblock")
	}
}

type heartbeatTransport struct {
	sent   chan protocol.Frame
	closed chan struct{}
}

func (t *heartbeatTransport) Send(f protocol.Frame) error {
	select {
	case t.sent <- f:
		return nil
	case <-t.closed:
		return errors.New("transport closed")
	}
}
func (t *heartbeatTransport) Receive() (protocol.Frame, error) {
	<-t.closed
	return protocol.Frame{}, errors.New("closed")
}
func (t *heartbeatTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}

func TestSessionRunSendsHeartbeatPing(t *testing.T) {
	tr := &heartbeatTransport{sent: make(chan protocol.Frame, 1), closed: make(chan struct{})}
	s := NewSession(tr)
	s.HeartbeatInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()
	select {
	case frame := <-tr.sent:
		if frame.Type != protocol.FramePing {
			t.Fatalf("heartbeat frame=%+v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat ping was not sent")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session did not stop")
	}
}

type streamConn struct {
	writes [][]byte
	closed bool
	read   []byte
}

type blockingStreamConn struct {
	*streamConn
	release     chan struct{}
	releaseOnce sync.Once
}

func (c *blockingStreamConn) Read([]byte) (int, error) {
	<-c.release
	return 0, io.EOF
}
func (c *blockingStreamConn) Close() error {
	c.releaseOnce.Do(func() {
		close(c.release)
		_ = c.streamConn.Close()
	})
	return nil
}

type dispatcherReadResult struct {
	payload []byte
	err     error
}

type directionalDispatcherConn struct {
	reads       chan dispatcherReadResult
	writes      chan []byte
	writeClosed chan struct{}
	closed      chan struct{}
	writeOnce   sync.Once
	closeOnce   sync.Once
}

type waitableDispatcherConn struct {
	readStarted chan struct{}
	closeCalled chan struct{}
	allowExit   chan struct{}
	readOnce    sync.Once
	closeOnce   sync.Once
}

type writeErrorDispatcherConn struct {
	*directionalDispatcherConn
	written int
	err     error
}

func (c *writeErrorDispatcherConn) Write([]byte) (int, error) { return c.written, c.err }

func newWaitableDispatcherConn() *waitableDispatcherConn {
	return &waitableDispatcherConn{readStarted: make(chan struct{}), closeCalled: make(chan struct{}), allowExit: make(chan struct{})}
}

func (c *waitableDispatcherConn) Read([]byte) (int, error) {
	c.readOnce.Do(func() { close(c.readStarted) })
	<-c.allowExit
	return 0, io.EOF
}
func (*waitableDispatcherConn) Write(payload []byte) (int, error) { return len(payload), nil }
func (c *waitableDispatcherConn) Close() error {
	c.closeOnce.Do(func() { close(c.closeCalled) })
	return nil
}

func newDirectionalDispatcherConn() *directionalDispatcherConn {
	return &directionalDispatcherConn{reads: make(chan dispatcherReadResult, 4), writes: make(chan []byte, 4), writeClosed: make(chan struct{}), closed: make(chan struct{})}
}

func (c *directionalDispatcherConn) Read(buffer []byte) (int, error) {
	select {
	case result := <-c.reads:
		return copy(buffer, result.payload), result.err
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
}
func (c *directionalDispatcherConn) Write(payload []byte) (int, error) {
	c.writes <- append([]byte(nil), payload...)
	return len(payload), nil
}
func (c *directionalDispatcherConn) CloseWrite() error {
	c.writeOnce.Do(func() { close(c.writeClosed) })
	return nil
}
func (c *directionalDispatcherConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *streamConn) Read(p []byte) (int, error) {
	if len(c.read) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.read)
	c.read = c.read[n:]
	return n, nil
}
func (c *streamConn) Write(p []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}
func (c *streamConn) Close() error { c.closed = true; return nil }
func TestStreamDispatcherOpensAndWritesTarget(t *testing.T) {
	conn := &streamConn{}
	d := NewStreamDispatcher(Dialer{Policy: func(context.Context, string, string, int) error { return nil }}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil })
	p, _ := json.Marshal(StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 80})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 7, Payload: p}); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 7, Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if string(conn.writes[0]) != "x" {
		t.Fatalf("writes=%q", conn.writes)
	}
}

func TestDefaultStreamDispatcherSupportsHTTPLogicalStreams(t *testing.T) {
	conn := &streamConn{}
	called := false
	d := NewStreamDispatcher(Dialer{HTTPStream: func(context.Context, string, int) (io.ReadWriteCloser, error) {
		called = true
		return conn, nil
	}}, nil)
	p, _ := json.Marshal(StreamOpenPayload{Protocol: "http", TargetHost: "service", TargetPort: 8080})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 12, Payload: p}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("HTTP logical stream did not use the injected HTTP dialer")
	}
}

func TestDialHTTPStreamUsesHTTPPolicyNamespace(t *testing.T) {
	var got string
	d := Dialer{Policy: func(_ context.Context, proto, _ string, _ int) error { got = proto; return errors.New("blocked") }}
	if _, err := d.DialHTTPStream(context.Background(), "service", 8080); err == nil || got != "http" {
		t.Fatalf("err=%v policy protocol=%q", err, got)
	}
}

func TestStreamDispatcherReadsTargetBackToFrameCallback(t *testing.T) {
	conn := &streamConn{read: []byte("reply")}
	gotc := make(chan protocol.Frame, 1)
	d := NewStreamDispatcherWithCallback(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(f protocol.Frame) {
		if f.Type == protocol.FrameData {
			gotc <- f
		}
	})
	p, _ := json.Marshal(StreamOpenPayload{Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 8, Payload: p}); err != nil {
		t.Fatal(err)
	}
	var got protocol.Frame
	select {
	case got = <-gotc:
	case <-time.After(time.Second):
		t.Fatal("callback timeout")
	}
	if string(got.Payload) != "reply" || got.Type != protocol.FrameData {
		t.Fatalf("frame=%+v", got)
	}
}

func TestStreamDispatcherHalfCloseKeepsTargetReadSideUntilResponseEOF(t *testing.T) {
	conn := newDirectionalDispatcherConn()
	sent := make(chan protocol.Frame, 4)
	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
		sent <- frame
		return nil
	})
	defer d.Close()
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 21, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 21}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.writeClosed:
	case <-time.After(time.Second):
		t.Fatal("target CloseWrite was not called")
	}
	select {
	case <-conn.closed:
		t.Fatal("target was fully closed before its response")
	default:
	}
	conn.reads <- dispatcherReadResult{payload: []byte("response")}
	conn.reads <- dispatcherReadResult{err: io.EOF}
	data := <-sent
	if data.Type != protocol.FrameData || data.StreamID != 21 || string(data.Payload) != "response" {
		t.Fatalf("target response frame = %+v", data)
	}
	halfClose := <-sent
	if halfClose.Type != protocol.FrameHalfClose || halfClose.StreamID != 21 {
		t.Fatalf("target EOF frame = %+v", halfClose)
	}
	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		t.Fatal("target was not closed after both directions half-closed")
	}
}

func TestStreamDispatcherResetsHalfCloseWhenTargetLacksCloseWrite(t *testing.T) {
	conn := &blockingStreamConn{streamConn: &streamConn{}, release: make(chan struct{})}
	defer conn.Close()
	sent := make(chan protocol.Frame, 2)
	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
		sent <- frame
		return nil
	})
	defer d.Close()
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 22, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 22}); err != nil {
		t.Fatalf("Handle(HALF_CLOSE) error = %v, want per-stream RESET", err)
	}
	frame := <-sent
	if frame.Type != protocol.FrameReset || frame.StreamID != 22 || len(frame.Payload) == 0 || len(frame.Payload) > 128 {
		t.Fatalf("unsupported half-close frame = %+v, want bounded RESET", frame)
	}
	if !conn.closed {
		t.Fatal("unsupported half-close did not close target")
	}
}

func TestStreamDispatcherMapsNonEOFReadErrorToBoundedReset(t *testing.T) {
	conn := newDirectionalDispatcherConn()
	sent := make(chan protocol.Frame, 2)
	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
		sent <- frame
		return nil
	})
	defer d.Close()
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 23, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	conn.reads <- dispatcherReadResult{err: errors.New("private target failure")}
	frame := <-sent
	if frame.Type != protocol.FrameReset || frame.StreamID != 23 || len(frame.Payload) == 0 || len(frame.Payload) > 128 {
		t.Fatalf("target read error frame = %+v, want bounded RESET", frame)
	}
}

func TestStreamDispatcherMapsTargetWriteErrorToBoundedReset(t *testing.T) {
	conn := &writeErrorDispatcherConn{directionalDispatcherConn: newDirectionalDispatcherConn(), err: errors.New("target datagram too large")}
	sent := make(chan protocol.Frame, 1)
	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
		sent <- frame
		return nil
	})
	defer d.Close()
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "udp", TargetHost: "127.0.0.1", TargetPort: 53})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 27, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 27, Payload: []byte("datagram")}); err != nil {
		t.Fatalf("Handle(DATA) error = %v, want per-stream RESET", err)
	}
	select {
	case frame := <-sent:
		if frame.Type != protocol.FrameReset || frame.StreamID != 27 || len(frame.Payload) == 0 || len(frame.Payload) > 128 {
			t.Fatalf("target write error frame = %+v, want bounded RESET", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for target-write RESET")
	}
	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		t.Fatal("target write error did not close target")
	}
}

func TestStreamDispatcherMapsTargetShortWriteToBoundedReset(t *testing.T) {
	conn := &writeErrorDispatcherConn{directionalDispatcherConn: newDirectionalDispatcherConn(), written: 3}
	sent := make(chan protocol.Frame, 1)
	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
		sent <- frame
		return nil
	})
	defer d.Close()
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 28, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 28, Payload: []byte("partial")}); err != nil {
		t.Fatalf("Handle(DATA) error = %v, want per-stream RESET", err)
	}
	select {
	case frame := <-sent:
		if frame.Type != protocol.FrameReset || frame.StreamID != 28 {
			t.Fatalf("target short-write frame = %+v, want RESET", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for target-short-write RESET")
	}
}

func TestStreamDispatcherRejectsDataAfterInboundHalfClose(t *testing.T) {
	conn := newDirectionalDispatcherConn()
	sent := make(chan protocol.Frame, 2)
	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
		sent <- frame
		return nil
	})
	defer d.Close()
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 24, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 24}); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 24, Payload: []byte("after-half-close")}); err != nil {
		t.Fatalf("Handle(DATA after HALF_CLOSE) error = %v, want per-stream RESET", err)
	}
	select {
	case payload := <-conn.writes:
		t.Fatalf("DATA after HALF_CLOSE reached target: %q", payload)
	default:
	}
	select {
	case frame := <-sent:
		if frame.Type != protocol.FrameReset || frame.StreamID != 24 {
			t.Fatalf("DATA-after-HALF_CLOSE response = %+v, want RESET", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for DATA-after-HALF_CLOSE RESET")
	}
}

func TestStreamDispatcherPreservesLargeUDPDatagramBoundary(t *testing.T) {
	conn := newDirectionalDispatcherConn()
	payload := bytes.Repeat([]byte("u"), 60<<10)
	conn.reads <- dispatcherReadResult{payload: payload}
	conn.reads <- dispatcherReadResult{err: io.EOF}
	sent := make(chan protocol.Frame, 2)
	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil }, func(frame protocol.Frame) error {
		sent <- frame
		return nil
	})
	defer d.Close()
	openPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "udp", TargetHost: "127.0.0.1", TargetPort: 53})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 25, Payload: openPayload}); err != nil {
		t.Fatal(err)
	}
	frame := <-sent
	if frame.Type != protocol.FrameData || frame.StreamID != 25 || len(frame.Payload) != len(payload) {
		t.Fatalf("UDP DATA type=%d id=%d bytes=%d, want one %d-byte frame", frame.Type, frame.StreamID, len(frame.Payload), len(payload))
	}
}

func TestStreamDispatcherCloseWaitsForTargetReaderExit(t *testing.T) {
	conn := newWaitableDispatcherConn()
	d := NewStreamDispatcher(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil })
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 22})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 26, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.readStarted:
	case <-time.After(time.Second):
		t.Fatal("target reader did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	select {
	case <-conn.closeCalled:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not close target")
	}
	select {
	case err := <-closed:
		t.Fatalf("dispatcher Close returned before reader exit: %v", err)
	default:
	}
	close(conn.allowExit)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher Close did not finish after reader exit")
	}
}

func TestStreamDispatcherRejectsDuplicateStreamID(t *testing.T) {
	first := &blockingStreamConn{streamConn: &streamConn{}, release: make(chan struct{})}
	defer first.Close()
	second := &streamConn{}
	count := 0
	d := NewStreamDispatcher(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) {
		count++
		if count == 1 {
			return first, nil
		}
		return second, nil
	})
	p, _ := json.Marshal(StreamOpenPayload{Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	f := protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 10, Payload: p}
	if err := d.Handle(f); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(f); !errors.Is(err, ErrDuplicateStream) {
		t.Fatalf("err=%v", err)
	}
	if first.closed || !second.closed {
		t.Fatalf("duplicate stream lifecycle first=%v second=%v", first.closed, second.closed)
	}
}

func TestStaleReadBackCannotDeleteReusedStreamID(t *testing.T) {
	d := NewStreamDispatcher(Dialer{}, nil)
	old, newer := &streamConn{}, &streamConn{}
	d.mu.Lock()
	d.generation++
	oldEntry := &streamEntry{conn: old, generation: d.generation}
	d.streams[11] = oldEntry
	d.generation++
	d.streams[11] = &streamEntry{conn: newer, generation: d.generation}
	d.readers.Add(1)
	d.mu.Unlock()
	d.readBack(11, oldEntry)
	d.mu.Lock()
	_, ok := d.streams[11]
	d.mu.Unlock()
	if !ok {
		t.Fatal("stale reader deleted replacement stream")
	}
}

func TestDialWebSocketRejectsNonWebSocketURL(t *testing.T) {
	_, err := DialWebSocket(context.Background(), "https://server.example/ws/agent", "agent-token")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "invalid websocket url") {
		t.Fatalf("DialWebSocket() error = %v, want invalid websocket URL", err)
	}
}

func TestDialWebSocketDoesNotSkipServerCertificateVerification(t *testing.T) {
	server := httptest.NewTLSServer(websocket.Handler(func(conn *websocket.Conn) { _ = conn.Close() }))
	defer server.Close()
	serverURL := "wss" + strings.TrimPrefix(server.URL, "https") + "/ws/agent"
	if transport, err := DialWebSocket(context.Background(), serverURL, "agent-token"); err == nil {
		_ = transport.Close()
		t.Fatal("DialWebSocket accepted an untrusted server certificate")
	}
}

func TestStreamDispatcherHTTPSUsesConfiguredHostAndSNI(t *testing.T) {
	certificate, roots := testAgentTLSCertificate(t, "service.internal.example.com", nil)
	serverName := make(chan string, 1)
	responses := make(chan string, 1)
	address := startAgentTLSServer(t, certificate, serverName, responses, tls.VersionTLS12)

	dataFrames := make(chan []byte, 4)
	d := NewStreamDispatcherWithSender(Dialer{Timeout: 2 * time.Second, TLSRootCAs: roots}, nil, func(frame protocol.Frame) error {
		if frame.Type == protocol.FrameData {
			dataFrames <- frame.Payload
		}
		return nil
	})
	payload, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{
		Protocol: "http", TargetHost: "127.0.0.1", TargetPort: address.Port,
		TargetScheme: "https", HostHeader: "service.internal.example.com",
		TLSServerName: "service.internal.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 31, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	request := "GET / HTTP/1.1\r\nHost: service.internal.example.com\r\n\r\n"
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 31, Payload: []byte(request)}); err != nil {
		t.Fatal(err)
	}

	select {
	case name := <-serverName:
		if name != "service.internal.example.com" {
			t.Fatalf("TLS SNI = %q", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TLS server did not receive SNI")
	}
	if response := waitAgentResponse(dataFrames); !strings.Contains(response, "tls12:ok") {
		t.Fatalf("TLS response = %q", response)
	}
}

func TestStreamDispatcherHTTPSDefaultsSNIToTargetHost(t *testing.T) {
	certificate, roots := testAgentTLSCertificate(t, "localhost", nil)
	serverName := make(chan string, 1)
	responses := make(chan string, 1)
	address := startAgentTLSServer(t, certificate, serverName, responses, 0)

	d := NewStreamDispatcher(Dialer{Timeout: 2 * time.Second, TLSRootCAs: roots}, nil)
	payload, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{
		Protocol: "http", TargetHost: "localhost", TargetPort: address.Port, TargetScheme: "https",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 32, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	select {
	case name := <-serverName:
		if name != "localhost" {
			t.Fatalf("default TLS SNI = %q", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TLS server did not receive default SNI")
	}
}

func TestStreamDispatcherHTTPSRejectsUntrustedCertificate(t *testing.T) {
	certificate, _ := testAgentTLSCertificate(t, "service.internal.example.com", nil)
	serverName := make(chan string, 1)
	responses := make(chan string, 1)
	address := startAgentTLSServer(t, certificate, serverName, responses, 0)

	d := NewStreamDispatcher(Dialer{Timeout: 2 * time.Second}, nil)
	payload, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{
		Protocol: "http", TargetHost: "127.0.0.1", TargetPort: address.Port,
		TargetScheme: "https", TLSServerName: "service.internal.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 33, Payload: payload}); err == nil {
		t.Fatal("HTTPS stream accepted an untrusted certificate")
	}
}

func testAgentTLSCertificate(t *testing.T, dnsName string, ip net.IP) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkixName("TunnelMesh Test CA"),
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkixName(dnsName),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{dnsName},
	}
	if ip != nil {
		leafTemplate.IPAddresses = []net.IP{ip}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{
		Certificate: [][]byte{leafDER, caDER},
		PrivateKey:  leafKey,
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	return certificate, roots
}

func pkixName(commonName string) pkix.Name {
	return pkix.Name{CommonName: commonName}
}

func startAgentTLSServer(t *testing.T, certificate tls.Certificate, serverName chan<- string, responses chan<- string, maxVersion uint16) *net.TCPAddr {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		config := &tls.Config{
			Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12,
			GetConfigForClient: func(info *tls.ClientHelloInfo) (*tls.Config, error) {
				serverName <- info.ServerName
				return nil, nil
			},
		}
		if maxVersion != 0 {
			config.MaxVersion = maxVersion
		}
		tlsConn := tls.Server(conn, config)
		_ = tlsConn.SetDeadline(time.Now().Add(2 * time.Second))
		if err := tlsConn.Handshake(); err != nil {
			_ = tlsConn.Close()
			return
		}
		request, err := bufio.NewReader(tlsConn).ReadString('\n')
		if err == nil {
			version := "tls-other"
			if tlsConn.ConnectionState().Version == tls.VersionTLS12 {
				version = "tls12"
			}
			_, _ = fmt.Fprintf(tlsConn, "HTTP/1.1 200 OK\r\nContent-Length: 6\r\n\r\n%s:ok", version)
		}
		_ = tlsConn.Close()
		responses <- request
	}()
	return listener.Addr().(*net.TCPAddr)
}

func waitAgentResponse(frames <-chan []byte) string {
	var response string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case payload := <-frames:
			response += string(payload)
			if strings.Contains(response, ":ok") {
				return response
			}
		case <-deadline:
			return response
		}
	}
}
