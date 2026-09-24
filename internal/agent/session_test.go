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
	"sync/atomic"
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

type blockingAgentFrameTransport struct {
	mu          sync.Mutex
	sent        chan protocol.Frame
	dataStarted chan struct{}
	release     chan struct{}
	closed      chan struct{}
	once        sync.Once
}

func newBlockingAgentFrameTransport() *blockingAgentFrameTransport {
	return &blockingAgentFrameTransport{
		sent: make(chan protocol.Frame, 4), dataStarted: make(chan struct{}),
		release: make(chan struct{}), closed: make(chan struct{}),
	}
}

func (t *blockingAgentFrameTransport) Send(frame protocol.Frame) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if frame.Type == protocol.FrameData {
		t.once.Do(func() { close(t.dataStarted) })
		select {
		case <-t.release:
		case <-t.closed:
			return errors.New("transport closed")
		}
	}
	select {
	case t.sent <- frame:
		return nil
	case <-t.closed:
		return errors.New("transport closed")
	}
}

func (t *blockingAgentFrameTransport) Receive() (protocol.Frame, error) {
	<-t.closed
	return protocol.Frame{}, errors.New("transport closed")
}

func (t *blockingAgentFrameTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}

func TestSessionSlowStreamDoesNotBlockControlFrame(t *testing.T) {
	transport := newBlockingAgentFrameTransport()
	session := NewSession(transport)
	defer session.Close()

	dataDone := make(chan error, 1)
	go func() {
		dataDone <- session.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 1, Payload: []byte("slow-data")})
	}()
	select {
	case <-transport.dataStarted:
	case <-time.After(time.Second):
		t.Fatal("slow stream did not reach the transport")
	}
	controlDone := make(chan error, 1)
	go func() {
		controlDone <- session.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong, Payload: []byte("pong")})
	}()
	select {
	case err := <-controlDone:
		if err != nil {
			close(transport.release)
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		close(transport.release)
		t.Fatal("slow stream blocked control frame")
	}
	close(transport.release)
	select {
	case err := <-dataDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow stream send did not finish")
	}

	for i := 0; i < 2; i++ {
		select {
		case frame := <-transport.sent:
			if i == 0 && (frame.Type != protocol.FrameData || frame.StreamID != 1) {
				t.Fatalf("first frame=%+v, want stream 1 DATA", frame)
			}
			if i == 1 && frame.Type != protocol.FramePong {
				t.Fatalf("second frame=%+v, want PONG", frame)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for frame %d", i)
		}
	}
}

func TestSessionExposesWriterQueueWaitP95(t *testing.T) {
	transport := newBlockingAgentFrameTransport()
	session := NewSession(transport)
	defer func() {
		close(transport.release)
		_ = session.Close()
	}()

	first := protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 1, Payload: []byte("first")}
	second := protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 2, Payload: []byte("second")}
	if err := session.Send(first); err != nil {
		t.Fatal(err)
	}
	if err := session.Send(second); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for session.WriterQueueWaitP95() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := session.WriterQueueWaitP95(); got <= 0 {
		t.Fatalf("WriterQueueWaitP95()=%v, want positive sample while transport is blocked", got)
	}
}

type flowTestConn struct {
	reads  chan []byte
	writes chan []byte
	closed chan struct{}
	once   sync.Once
}

func newFlowTestConn(reads ...[]byte) *flowTestConn {
	conn := &flowTestConn{reads: make(chan []byte, len(reads)), writes: make(chan []byte, 8), closed: make(chan struct{})}
	for _, read := range reads {
		conn.reads <- read
	}
	return conn
}

func (c *flowTestConn) Read(buffer []byte) (int, error) {
	select {
	case payload := <-c.reads:
		return copy(buffer, payload), nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *flowTestConn) Write(payload []byte) (int, error) {
	select {
	case c.writes <- append([]byte(nil), payload...):
		return len(payload), nil
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
}

func (c *flowTestConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func TestStreamDispatcherFlowControlStopsAtAdvertisedWindow(t *testing.T) {
	conn := newFlowTestConn([]byte("0123456789abcdef"), []byte("01234567"))
	frames := make(chan protocol.Frame, 8)
	dispatcher := NewStreamDispatcherWithSender(
		Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil },
		func(frame protocol.Frame) error { frames <- frame; return nil },
	)
	defer dispatcher.Close()
	payload, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{Protocol: "tcp", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 41, Window: 16, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	waitStreamInstalled(t, dispatcher, 41)

	select {
	case frame := <-frames:
		if frame.Type != protocol.FrameData || frame.StreamID != 41 || len(frame.Payload) != 16 {
			t.Fatalf("first DATA=%+v, want 16 bytes for stream 41", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("first window-sized DATA was not sent")
	}
	select {
	case frame := <-frames:
		t.Fatalf("DATA exceeded advertised window: %+v", frame)
	case <-time.After(100 * time.Millisecond):
	}
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: 41, Window: 8}); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-frames:
		if frame.Type != protocol.FrameData || frame.StreamID != 41 || len(frame.Payload) != 8 {
			t.Fatalf("post-update DATA=%+v, want 8 bytes for stream 41", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("DATA after WINDOW_UPDATE was not sent")
	}
}

func TestStreamDispatcherResetIgnoresTargetCloseError(t *testing.T) {
	conn := &closeErrorConn{}
	dispatcher := NewStreamDispatcherWithSender(
		Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil },
		func(protocol.Frame) error { return nil },
	)
	defer dispatcher.Close()
	payload, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{Protocol: "tcp", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 51, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	waitStreamInstalled(t, dispatcher, 51)
	_ = conn.Close()
	err = dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: 51})
	if err != nil {
		t.Fatalf("RESET error=%v, want stream-local cleanup only", err)
	}
}

func TestStreamDispatcherLateWindowUpdateIsIgnored(t *testing.T) {
	dispatcher := NewStreamDispatcherWithSender(
		Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return nopAgentConn{}, nil },
		func(protocol.Frame) error { return nil },
	)
	defer dispatcher.Close()
	err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: 61, Window: 128})
	if err != nil {
		t.Fatalf("late WINDOW_UPDATE error=%v, want nil", err)
	}
}

func TestStreamDispatcherLateResetIsIgnored(t *testing.T) {
	dispatcher := NewStreamDispatcherWithSender(
		Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return nopAgentConn{}, nil },
		func(protocol.Frame) error { return nil },
	)
	defer dispatcher.Close()
	err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: 71})
	if err != nil {
		t.Fatalf("late RESET error=%v, want nil", err)
	}
}

func TestStreamDispatcherLateDataAndHalfCloseAreStreamLocal(t *testing.T) {
	sent := make(chan protocol.Frame, 2)
	dispatcher := NewStreamDispatcherWithSender(
		Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return nopAgentConn{}, nil },
		func(frame protocol.Frame) error { sent <- frame; return nil },
	)
	defer dispatcher.Close()
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 81, Payload: []byte("late")}); err != nil {
		t.Fatalf("late DATA error=%v, want nil", err)
	}
	select {
	case frame := <-sent:
		if frame.Type != protocol.FrameReset || frame.StreamID != 81 {
			t.Fatalf("late DATA response=%+v, want stream 81 RESET", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("late DATA did not reset the stale stream")
	}
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 81}); err != nil {
		t.Fatalf("late HALF_CLOSE error=%v, want nil", err)
	}
}

type nopAgentConn struct{}

func (nopAgentConn) Read([]byte) (int, error)  { return 0, io.EOF }
func (nopAgentConn) Write([]byte) (int, error) { return 0, nil }
func (nopAgentConn) Close() error              { return nil }

type closeErrorConn struct{ closed bool }

func (c *closeErrorConn) Read([]byte) (int, error) { return 0, io.EOF }
func (closeErrorConn) Write([]byte) (int, error)   { return 0, nil }
func (c *closeErrorConn) Close() error {
	if c.closed {
		return errors.New("target already closed")
	}
	c.closed = true
	return nil
}

func TestStreamDispatcherSendsWindowUpdateAfterTargetWrite(t *testing.T) {
	conn := newFlowTestConn()
	frames := make(chan protocol.Frame, 4)
	dispatcher := NewStreamDispatcherWithSender(
		Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil },
		func(frame protocol.Frame) error { frames <- frame; return nil },
	)
	defer dispatcher.Close()
	payload, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{Protocol: "tcp", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 42, Window: 262144, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	waitStreamInstalled(t, dispatcher, 42)

	data := make([]byte, 131072)
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 42, Payload: data}); err != nil {
		t.Fatal(err)
	}
	select {
	case written := <-conn.writes:
		if len(written) != len(data) {
			t.Fatalf("target write length=%d, want %d", len(written), len(data))
		}
	case <-time.After(time.Second):
		t.Fatal("target write did not complete")
	}
	select {
	case frame := <-frames:
		if frame.Type != protocol.FrameWindowUpdate || frame.StreamID != 42 || frame.Window != 131072 {
			t.Fatalf("WINDOW_UPDATE=%+v, want 131072 bytes for stream 42", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("threshold WINDOW_UPDATE was not sent")
	}
}

func TestStreamDispatcherExposesLatencySignals(t *testing.T) {
	dial := newControlledDialFunc()
	firstRelease := make(chan struct{})
	firstStarted := dial.stage(501, func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
		<-firstRelease
		return &streamConn{read: []byte("first-byte")}, nil
	})
	dial.stage(502, func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
		return &streamConn{}, nil
	})
	frames := make(chan protocol.Frame, 4)
	dispatcher := NewStreamDispatcherWithConfig(Dialer{}, nil, func(frame protocol.Frame) error {
		frames <- frame
		return nil
	}, DialExecutorConfig{MaxConcurrent: 1, MaxPending: 1, ConnectTimeout: time.Second, OpenTimeout: time.Second}, dial.dial)
	defer dispatcher.Close()

	open := func(id uint32, port int) protocol.Frame {
		payload, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{Protocol: "tcp", TargetHost: "host", TargetPort: port})
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: id, Payload: payload}
	}
	if err := dispatcher.Handle(open(51, 501)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first dial did not start")
	}
	if err := dispatcher.Handle(open(52, 502)); err != nil {
		t.Fatal(err)
	}
	if got := dispatcher.PendingDials(); got != 1 {
		t.Fatalf("PendingDials()=%d, want queued second dial", got)
	}
	if got := dispatcher.OpenP95(); got != 0 {
		t.Fatalf("OpenP95() before completion=%v, want zero", got)
	}

	close(firstRelease)
	waitStreamInstalled(t, dispatcher, 51)
	deadline := time.After(time.Second)
	for {
		select {
		case frame := <-frames:
			if frame.Type == protocol.FrameData && frame.StreamID == 51 {
				if string(frame.Payload) != "first-byte" {
					t.Fatalf("first DATA payload=%q", frame.Payload)
				}
				goto dataReceived
			}
		case <-deadline:
			t.Fatal("first-byte DATA was not sent")
		}
	}
dataReceived:
	if got := dispatcher.OpenP95(); got <= 0 {
		t.Fatalf("OpenP95()=%v, want positive sample", got)
	}
	if got := dispatcher.TTFBP95(); got <= 0 {
		t.Fatalf("TTFBP95()=%v, want positive sample", got)
	}
}

type streamConn struct {
	mu     sync.Mutex
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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}
func (c *streamConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
func (c *streamConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}
func (c *streamConn) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.writes)
}
func (c *streamConn) firstWrite() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.writes) == 0 {
		return ""
	}
	return string(c.writes[0])
}

func waitStreamInstalled(t *testing.T, dispatcher *StreamDispatcher, id uint32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dispatcher.mu.Lock()
		_, installed := dispatcher.streams[id]
		dispatcher.mu.Unlock()
		if installed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	dispatcher.mu.Lock()
	_, pending := dispatcher.pending[id]
	dispatcher.mu.Unlock()
	t.Fatalf("timed out waiting for stream %d installation (pending=%v)", id, pending)
}

func waitStreamFailure(t *testing.T, dispatcher *StreamDispatcher, id uint32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dispatcher.mu.Lock()
		_, installed := dispatcher.streams[id]
		_, pending := dispatcher.pending[id]
		dispatcher.mu.Unlock()
		if !installed && !pending {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for stream %d failure", id)
}

func TestStreamDispatcherOpensAndWritesTarget(t *testing.T) {
	conn := &streamConn{}
	d := NewStreamDispatcher(Dialer{Policy: func(context.Context, string, string, int) error { return nil }}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) {
		return conn, nil
	})
	p, _ := json.Marshal(StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 80})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 7, Payload: p}); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 7, Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for conn.writeCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := conn.firstWrite(); got != "x" {
		t.Fatalf("first write=%q", got)
	}
}

func TestStreamDispatcherDialDoesNotBlockReadyStream(t *testing.T) {
	dial := newControlledDialFunc()
	dial.stage(1, nil)
	dial.stage(2, func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
		return &streamConn{}, nil
	})
	sent := make(chan protocol.Frame, 4)
	dispatcher := NewStreamDispatcherWithConfig(Dialer{}, nil, func(frame protocol.Frame) error {
		sent <- frame
		return nil
	}, DialExecutorConfig{MaxConcurrent: 2, MaxPending: 2, ConnectTimeout: time.Second, OpenTimeout: time.Second}, dial.dial)
	defer dispatcher.Close()
	dispatcher.SetOpenResultEnabled(true)

	blocked, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "service.internal", TargetPort: 1})
	ready, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "service.internal", TargetPort: 2})
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, Flags: protocol.FlagStrictOpen, StreamID: 101, Payload: blocked}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, Flags: protocol.FlagStrictOpen, StreamID: 102, Payload: ready}); err != nil {
		t.Fatal(err)
	}

	var result protocol.OpenResultPayload
	select {
	case frame := <-sent:
		if frame.Type != protocol.FrameOpenResult || frame.StreamID != 102 {
			t.Fatalf("ready stream frame=%+v, want OPEN_RESULT", frame)
		}
		result, _ = protocol.DecodeOpenResultPayload(frame.Payload)
	case <-time.After(2 * time.Second):
		t.Fatal("ready dial was blocked by another target dial")
	}
	if !result.Accepted || result.Code != protocol.OpenResultCodeOK {
		t.Fatalf("ready result=%+v, want success", result)
	}
	dial.release(1)
}

func TestStreamDispatcherStrictFailureIsPerStreamOpenResult(t *testing.T) {
	dial := newControlledDialFunc()
	dial.stage(1, func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	})
	sent := make(chan protocol.Frame, 2)
	dispatcher := NewStreamDispatcherWithConfig(Dialer{}, nil, func(frame protocol.Frame) error {
		sent <- frame
		return nil
	}, DialExecutorConfig{MaxConcurrent: 1, MaxPending: 1, ConnectTimeout: time.Second, OpenTimeout: time.Second}, dial.dial)
	defer dispatcher.Close()
	dispatcher.SetOpenResultEnabled(true)
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "service.internal", TargetPort: 1})
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, Flags: protocol.FlagStrictOpen, StreamID: 111, Payload: payload}); err != nil {
		t.Fatalf("stream dial failure became session error: %v", err)
	}
	select {
	case frame := <-sent:
		if frame.Type != protocol.FrameOpenResult || frame.StreamID != 111 {
			t.Fatalf("strict failure frame=%+v, want OPEN_RESULT", frame)
		}
		result, _ := protocol.DecodeOpenResultPayload(frame.Payload)
		if result.Accepted || result.Code != protocol.OpenResultCodeConnectionRefused || result.Stage != protocol.OpenResultStageConnect {
			t.Fatalf("strict failure result=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for strict OPEN_RESULT")
	}
}

func TestStreamDispatcherLegacyFailureSendsOnlyReset(t *testing.T) {
	dial := newControlledDialFunc()
	dial.stage(1, func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	})
	sent := make(chan protocol.Frame, 2)
	dispatcher := NewStreamDispatcherWithConfig(Dialer{}, nil, func(frame protocol.Frame) error {
		sent <- frame
		return nil
	}, DialExecutorConfig{MaxConcurrent: 1, MaxPending: 1, ConnectTimeout: time.Second, OpenTimeout: time.Second}, dial.dial)
	defer dispatcher.Close()
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "service.internal", TargetPort: 1})
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 112, Payload: payload}); err != nil {
		t.Fatalf("legacy dial failure became session error: %v", err)
	}
	select {
	case frame := <-sent:
		if frame.Type != protocol.FrameReset || frame.StreamID != 112 {
			t.Fatalf("legacy failure frame=%+v, want RESET", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for legacy RESET")
	}
}

func TestStreamDispatcherTargetWriteFailureResetsOnlyCurrentStream(t *testing.T) {
	first := &writeErrorDispatcherConn{directionalDispatcherConn: newDirectionalDispatcherConn(), err: errors.New("target write failed")}
	second := newDirectionalDispatcherConn()
	dial := func(_ context.Context, payload protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
		if payload.TargetPort == 1 {
			return first, nil
		}
		return second, nil
	}
	sent := make(chan protocol.Frame, 4)
	dispatcher := NewStreamDispatcherWithConfig(Dialer{}, nil, func(frame protocol.Frame) error {
		sent <- frame
		return nil
	}, DialExecutorConfig{MaxConcurrent: 2, MaxPending: 2, ConnectTimeout: time.Second, OpenTimeout: time.Second}, dial)
	defer dispatcher.Close()
	dispatcher.SetOpenResultEnabled(true)
	firstPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "service.internal", TargetPort: 1})
	secondPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "service.internal", TargetPort: 2})
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, Flags: protocol.FlagStrictOpen, StreamID: 121, Payload: firstPayload}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, Flags: protocol.FlagStrictOpen, StreamID: 122, Payload: secondPayload}); err != nil {
		t.Fatal(err)
	}
	seenOpenResults := map[uint32]bool{}
	for len(seenOpenResults) != 2 {
		select {
		case frame := <-sent:
			if frame.Type != protocol.FrameOpenResult || (frame.StreamID != 121 && frame.StreamID != 122) {
				t.Fatalf("setup frame=%+v, want OPEN_RESULT", frame)
			}
			seenOpenResults[frame.StreamID] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for both OPEN_RESULT frames, seen=%v", seenOpenResults)
		}
	}
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 121, Payload: []byte("request")}); err != nil {
		t.Fatalf("target write failure became session error: %v", err)
	}
	select {
	case frame := <-sent:
		if frame.Type != protocol.FrameReset || frame.StreamID != 121 {
			t.Fatalf("write failure frame=%+v, want current stream RESET", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for target write RESET")
	}
	if got := dispatcher.ActiveStreams(); got != 1 {
		t.Fatalf("active streams=%d, want only the healthy stream", got)
	}
}

func TestConnectionPoolHandlerEnablesOpenResultFromServerAck(t *testing.T) {
	dispatcher := NewStreamDispatcher(Dialer{}, nil)
	defer dispatcher.Close()
	handler := &connectionPoolHandler{dispatcher: dispatcher}
	ackPayload, err := protocol.EncodeAgentMetadataAckPayload(protocol.AgentMetadataAckPayload{
		Accepted: true, Capabilities: []string{protocol.CapabilityStreamOpenResult},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameAgentMetadataAck, Payload: ackPayload}); err != nil {
		t.Fatal(err)
	}
	dispatcher.mu.Lock()
	enabled := dispatcher.openResult
	dispatcher.mu.Unlock()
	if !enabled {
		t.Fatal("server capability ACK did not enable strict open result")
	}
}

func TestDefaultStreamDispatcherSupportsHTTPLogicalStreams(t *testing.T) {
	conn := &streamConn{}
	called := make(chan struct{})
	d := NewStreamDispatcher(Dialer{HTTPStream: func(context.Context, string, int) (io.ReadWriteCloser, error) {
		close(called)
		return conn, nil
	}}, nil)
	p, _ := json.Marshal(StreamOpenPayload{Protocol: "http", TargetHost: "service", TargetPort: 8080})
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 12, Payload: p}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
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
	waitStreamInstalled(t, d, 8)
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
	waitStreamInstalled(t, d, 21)
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
	waitStreamInstalled(t, d, 22)
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
	waitStreamInstalled(t, d, 23)
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
	waitStreamInstalled(t, d, 27)
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
	waitStreamInstalled(t, d, 28)
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
	waitStreamInstalled(t, d, 24)
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
	first := newDirectionalDispatcherConn()
	second := &streamConn{}
	count := 0
	dialStarted := make(chan struct{}, 1)
	d := NewStreamDispatcher(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) {
		count++
		if count == 1 {
			dialStarted <- struct{}{}
			return first, nil
		}
		return second, nil
	})
	p, _ := json.Marshal(StreamOpenPayload{Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	f := protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 10, Payload: p}
	if err := d.Handle(f); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dialStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first stream dial did not start")
	}
	if err := d.Handle(f); err != nil {
		t.Fatalf("duplicate stream became session error: %v", err)
	}
	if count != 1 {
		t.Fatalf("duplicate stream caused %d target dials, want 1", count)
	}
	waitStreamInstalled(t, d, 10)
	if got := d.ActiveStreams(); got != 1 {
		t.Fatalf("active streams=%d, want first stream to remain active", got)
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
	waitStreamInstalled(t, d, 31)
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

	reset := make(chan protocol.Frame, 1)
	d := NewStreamDispatcherWithSender(Dialer{Timeout: 2 * time.Second}, nil, func(frame protocol.Frame) error {
		if frame.Type == protocol.FrameReset {
			reset <- frame
		}
		return nil
	})
	defer d.Close()
	payload, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{
		Protocol: "http", TargetHost: "127.0.0.1", TargetPort: address.Port,
		TargetScheme: "https", TLSServerName: "service.internal.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 33, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-reset:
		if frame.StreamID != 33 {
			t.Fatalf("TLS failure reset stream=%d, want 33", frame.StreamID)
		}
	case <-time.After(2 * time.Second):
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

// slowTransport stands in for an Agent WebSocket that drains more slowly than
// the target service writes, which is the ordinary condition behind a large
// response. It credits the stream back the way the Server does, so the pump is
// limited by the queue rather than by a window that never refills.
type slowTransport struct {
	release   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	credit    func(n int)
	dataBytes atomic.Int64
}

func (t *slowTransport) Send(frame protocol.Frame) error {
	select {
	case <-t.closed:
		return errors.New("transport closed")
	default:
	}
	<-t.release
	if frame.Type != protocol.FrameData {
		return nil
	}
	t.dataBytes.Add(int64(len(frame.Payload)))
	if t.credit != nil {
		t.credit(len(frame.Payload))
	}
	return nil
}

func (t *slowTransport) Receive() (protocol.Frame, error) {
	<-t.closed
	return protocol.Frame{}, errors.New("closed")
}

func (t *slowTransport) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

// TestStreamDispatcherLargeResponseSurvivesSlowWriter reproduces the truncated
// managed-route response a browser reports as an interrupted download. The
// target replies with a body larger than the per-stream writer queue while the
// WebSocket drains slowly. The Server credits every Agent stream with
// DefaultServerReceiveWindow and the pump consumes that credit before it
// enqueues, so this peer is behaving exactly as credited: the queue must be able
// to hold what the credit allows, and the pump must never treat a full queue as
// a reason to abandon the response.
func TestStreamDispatcherLargeResponseSurvivesSlowWriter(t *testing.T) {
	const chunkSize = 32 << 10
	const chunks = 32 // 1 MiB: twice the credit the Server advertises per stream
	const total = chunkSize * chunks
	const streamID = uint32(7)

	chunk := make([]byte, chunkSize)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	reads := make([][]byte, chunks)
	for i := range reads {
		reads[i] = append([]byte(nil), chunk...)
	}
	conn := newFlowTestConn(reads...)

	transport := &slowTransport{release: make(chan struct{}), closed: make(chan struct{})}
	session := NewSession(transport)
	defer session.Close()

	dispatcher := NewStreamDispatcherWithSender(
		Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return conn, nil },
		session.Send,
	)
	defer dispatcher.Close()
	// Credit returns the way the Server returns it: once the bytes are on the
	// wire the consumer has taken them, so the pump is never window-bound here.
	transport.credit = func(n int) {
		_ = dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: streamID, Window: uint32(n)})
	}

	payload, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{Protocol: "http", TargetHost: "host", TargetPort: 80})
	if err != nil {
		t.Fatal(err)
	}
	open := protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: streamID, Window: protocol.DefaultServerReceiveWindow, Payload: payload}
	if err := dispatcher.Handle(open); err != nil {
		t.Fatal(err)
	}

	// The pump fills the queue within microseconds, long before a polling loop
	// could observe the stream, so the teardown signal to watch is the target
	// connection: removeAndClose closes it and forgets the stream.
	select {
	case <-conn.closed:
		t.Fatalf("relay pump abandoned the response; only %d of %d bytes reached the transport", transport.dataBytes.Load(), total)
	case <-time.After(500 * time.Millisecond):
	}

	close(transport.release)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && transport.dataBytes.Load() < total {
		select {
		case <-conn.closed:
			t.Fatalf("relay pump abandoned the response mid-body; delivered %d of %d bytes", transport.dataBytes.Load(), total)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := transport.dataBytes.Load(); got != total {
		t.Fatalf("delivered %d bytes, want the full %d-byte response", got, total)
	}
}

// TestSessionWriterQueueCoversAdvertisedCredit keeps the sizing invariant from
// drifting. The pump consumes send credit before it enqueues, so bytes waiting
// in the per-stream queue can never exceed the credit the peer granted; a queue
// smaller than that credit makes ErrStreamQueueFull reachable for a fully
// compliant peer, and both relay pumps treat that error as fatal.
func TestSessionWriterQueueCoversAdvertisedCredit(t *testing.T) {
	if streamQueueBytes < protocol.DefaultServerReceiveWindow {
		t.Fatalf("session writer queue = %d bytes, want at least the %d-byte credit the Server advertises", streamQueueBytes, protocol.DefaultServerReceiveWindow)
	}
}

type stalledWriteConn struct {
	reads     chan dispatcherReadResult
	writes    chan []byte
	unblock   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func newStalledWriteConn() *stalledWriteConn {
	return &stalledWriteConn{
		reads: make(chan dispatcherReadResult, 4), writes: make(chan []byte, 16),
		unblock: make(chan struct{}), closed: make(chan struct{}),
	}
}

func (c *stalledWriteConn) Read(p []byte) (int, error) {
	select {
	case result := <-c.reads:
		if result.err != nil {
			return 0, result.err
		}
		return copy(p, result.payload), nil
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
}

func (c *stalledWriteConn) Write(p []byte) (int, error) {
	select {
	case <-c.unblock:
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
	select {
	case c.writes <- append([]byte(nil), p...):
		return len(p), nil
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
}

func (c *stalledWriteConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func openAgentStream(t *testing.T, d *StreamDispatcher, id uint32, port int) {
	t.Helper()
	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: port})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: id, Payload: payload}); err != nil {
		t.Fatalf("open stream %d: %v", id, err)
	}
	waitStreamInstalled(t, d, id)
}

func handleWithin(t *testing.T, d *StreamDispatcher, frame protocol.Frame, context string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- d.Handle(frame) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: Handle error=%v", context, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: Handle did not return; the Agent receive loop is blocked on one stream", context)
	}
}

func TestAgentInboundWriteReturnsBeforeTargetConsumes(t *testing.T) {
	stalled := newStalledWriteConn()
	ready := newDirectionalDispatcherConn()
	d := NewStreamDispatcherWithSender(Dialer{}, func(_ context.Context, _ string, _ string, port int) (io.ReadWriteCloser, error) {
		if port == 1234 {
			return stalled, nil
		}
		return ready, nil
	}, func(protocol.Frame) error { return nil })
	defer d.Close()

	openAgentStream(t, d, 41, 1234)
	openAgentStream(t, d, 42, 5678)
	handleWithin(t, d, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 41, Payload: []byte("first")}, "DATA to stalled stream")
	handleWithin(t, d, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 41, Payload: []byte("second")}, "second DATA to stalled stream")
	handleWithin(t, d, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 42, Payload: []byte("other")}, "DATA to healthy stream")

	close(stalled.unblock)
	for _, want := range []string{"first", "second"} {
		select {
		case got := <-stalled.writes:
			if string(got) != want {
				t.Fatalf("stalled target wrote %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %q to reach the stalled target", want)
		}
	}
	select {
	case got := <-ready.writes:
		if string(got) != "other" {
			t.Fatalf("healthy target wrote %q, want other", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("healthy stream never received its bytes")
	}
}

func TestAgentInboundWindowAbuseResetsOnlyOffendingStream(t *testing.T) {
	stalled := newStalledWriteConn()
	defer stalled.Close()
	ready := newDirectionalDispatcherConn()
	sent := make(chan protocol.Frame, 32)
	d := NewStreamDispatcherWithSender(Dialer{}, func(_ context.Context, _ string, _ string, port int) (io.ReadWriteCloser, error) {
		if port == 1234 {
			return stalled, nil
		}
		return ready, nil
	}, func(frame protocol.Frame) error { sent <- frame; return nil })
	defer d.Close()

	openAgentStream(t, d, 43, 1234)
	openAgentStream(t, d, 44, 5678)
	// Never release the stalled target, so no inbound credit is ever returned and
	// the peer keeps pushing far past the advertised window.
	chunk := make([]byte, protocol.MaxStreamFrame)
	for i := range chunk {
		chunk[i] = 'x'
	}
	var reset protocol.Frame
	for attempt := 0; attempt < 64; attempt++ {
		handleWithin(t, d, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 43, Payload: chunk}, "flood DATA")
		select {
		case frame := <-sent:
			if frame.Type == protocol.FrameReset && frame.StreamID == 43 {
				reset = frame
			}
		default:
		}
		if reset.Type == protocol.FrameReset {
			break
		}
	}
	if reset.Type != protocol.FrameReset {
		t.Fatal("window abuse did not reset the offending stream")
	}
	handleWithin(t, d, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 44, Payload: []byte("still usable")}, "DATA after resetting one stream")
	select {
	case got := <-ready.writes:
		if string(got) != "still usable" {
			t.Fatalf("surviving stream wrote %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("one abusive stream took the whole dispatcher down")
	}
}

func TestAgentStreamResetUnblocksTargetWritePump(t *testing.T) {
	stalled := newStalledWriteConn()
	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return stalled, nil }, func(protocol.Frame) error { return nil })
	defer d.Close()

	openAgentStream(t, d, 45, 1234)
	d.mu.Lock()
	entry := d.streams[45]
	d.mu.Unlock()
	if entry == nil {
		t.Fatal("stream 45 is not installed")
	}
	handleWithin(t, d, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 45, Payload: []byte("queued")}, "DATA before RESET")
	handleWithin(t, d, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: 45}, "RESET")
	select {
	case <-entry.pumpDone:
	case <-time.After(2 * time.Second):
		t.Fatal("RESET did not stop the per-stream target writer")
	}
	closeDone := make(chan struct{})
	go func() { _ = d.Close(); close(closeDone) }()
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Close is waiting on a target writer that should already be gone")
	}
}

type recordingTargetConn struct {
	mu     sync.Mutex
	writes bytes.Buffer
	closed chan struct{}
	once   sync.Once
}

func newRecordingTargetConn() *recordingTargetConn {
	return &recordingTargetConn{closed: make(chan struct{})}
}

func (c *recordingTargetConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.ErrClosedPipe
}

func (c *recordingTargetConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes.Write(p)
}

func (c *recordingTargetConn) written() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes.String()
}

func (c *recordingTargetConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func TestAgentCreditsEarlyDataReceivedWhileDialing(t *testing.T) {
	dial := newControlledDialFunc()
	target := newRecordingTargetConn()
	// Hold the dial open until every early byte is queued, so the test always
	// exercises the dial-pending buffer rather than racing with the dial.
	proceed := make(chan struct{})
	started := dial.stage(9000, func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
		<-proceed
		return target, nil
	})
	sent := make(chan protocol.Frame, 64)
	dispatcher := NewStreamDispatcherWithConfig(Dialer{}, nil, func(frame protocol.Frame) error { sent <- frame; return nil },
		DialExecutorConfig{MaxConcurrent: 1, MaxPending: 4, ConnectTimeout: 5 * time.Second, OpenTimeout: 10 * time.Second}, dial.dial)
	defer dispatcher.Close()

	payload, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{Protocol: "tcp", TargetHost: "host", TargetPort: 9000})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 51, Window: protocol.DefaultServerReceiveWindow, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dial did not start")
	}
	chunk := bytes.Repeat([]byte("z"), 25600)
	const total = 8 * 25600
	for i := 0; i < 8; i++ {
		if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 51, Payload: chunk}); err != nil {
			t.Fatalf("early DATA %d: %v", i, err)
		}
	}
	close(proceed)
	waitStreamInstalled(t, dispatcher, 51)

	deadline := time.Now().Add(5 * time.Second)
	for len(target.written()) < total && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(target.written()); got != total {
		t.Fatalf("target received %d of %d early bytes", got, total)
	}
	// The point of the test: bytes that landed before the dial finished must
	// return credit like any other inbound byte, or the Server's send window is
	// silently smaller and a bulk upload deadlocks once it is exhausted.
	var credited uint32
	for time.Now().Before(deadline) && credited < defaultAgentWindowUpdateThreshold {
		select {
		case frame := <-sent:
			if frame.Type == protocol.FrameWindowUpdate && frame.StreamID == 51 {
				credited += frame.Window
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if credited < defaultAgentWindowUpdateThreshold {
		t.Fatalf("early DATA returned %d credit, want at least %d", credited, defaultAgentWindowUpdateThreshold)
	}
}

func TestAgentPendingBufferOverflowResetsStream(t *testing.T) {
	dial := newControlledDialFunc()
	// No staged result: the dial stays parked until the stream is cancelled, so
	// every DATA below really lands in the dial-pending buffer instead of racing
	// the per-stream pump that drains an installed stream.
	started := dial.stage(9001, nil)
	sent := make(chan protocol.Frame, 64)
	dispatcher := NewStreamDispatcherWithConfig(Dialer{}, nil, func(frame protocol.Frame) error { sent <- frame; return nil },
		DialExecutorConfig{MaxConcurrent: 1, MaxPending: 4, ConnectTimeout: 5 * time.Second, OpenTimeout: 10 * time.Second}, dial.dial)
	defer dispatcher.Close()

	payload, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{Protocol: "tcp", TargetHost: "host", TargetPort: 9001})
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 52, Window: protocol.DefaultServerReceiveWindow, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dial did not start")
	}
	chunk := bytes.Repeat([]byte("q"), protocol.MaxStreamFrame)
	for i := 0; i < defaultInboundQueueBytes/protocol.MaxStreamFrame+2; i++ {
		if err := dispatcher.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 52, Payload: chunk}); err != nil {
			t.Fatalf("DATA %d while dialing: %v", i, err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case frame := <-sent:
			if frame.Type == protocol.FrameReset && frame.StreamID == 52 {
				waitStreamFailure(t, dispatcher, 52)
				return
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("an overflowing dial-pending buffer did not reset its own stream")
}

// `agent.streams.inbound_buffer_bytes` has to size the per-stream queue, or the
// documented knob is decoration and a deployment cannot trade memory for burst.
func TestAgentInboundBufferComesFromConfiguration(t *testing.T) {
	target := newRecordingTargetConn()
	d := NewStreamDispatcherWithSender(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) { return target, nil }, func(protocol.Frame) error { return nil })
	defer d.Close()
	if err := d.SetInboundBufferBytes(protocol.MaxStreamFrame); !errors.Is(err, ErrInboundBufferTooSmall) {
		t.Fatalf("a one-frame queue is smaller than the credit this Agent grants: err=%v", err)
	}
	if err := d.SetInboundBufferBytes(2 * protocol.MaxStreamFrame); err != nil {
		t.Fatal(err)
	}
	openAgentStream(t, d, 61, 1234)
	d.mu.Lock()
	entry := d.streams[61]
	d.mu.Unlock()
	if entry == nil {
		t.Fatal("stream was never installed")
	}
	if got := entry.inbound.Capacity(); got != 2*protocol.MaxStreamFrame {
		t.Fatalf("inbound queue capacity = %d, want the configured %d", got, 2*protocol.MaxStreamFrame)
	}
}
