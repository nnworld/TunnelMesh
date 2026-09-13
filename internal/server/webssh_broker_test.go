package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type fakeWebSSHWS struct {
	mu       sync.Mutex
	readCh   chan fakeWSMessage
	writes   [][]byte
	closed   bool
	closeCh  chan struct{}
	closeOne sync.Once
}

type fakeWSMessage struct {
	typ  int
	data []byte
	err  error
}

func newFakeWebSSHWS() *fakeWebSSHWS {
	return &fakeWebSSHWS{readCh: make(chan fakeWSMessage), closeCh: make(chan struct{})}
}

func (w *fakeWebSSHWS) ReadMessage() (int, []byte, error) {
	select {
	case msg := <-w.readCh:
		return msg.typ, msg.data, msg.err
	case <-w.closeCh:
		return 0, nil, io.EOF
	}
}

func (w *fakeWebSSHWS) WriteMessage(_ int, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return io.ErrClosedPipe
	}
	w.writes = append(w.writes, append([]byte(nil), data...))
	return nil
}

func (w *fakeWebSSHWS) Close() error {
	w.closeOne.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		close(w.closeCh)
	})
	return nil
}

func (w *fakeWebSSHWS) send(typ int, data []byte) { w.readCh <- fakeWSMessage{typ: typ, data: data} }
func (w *fakeWebSSHWS) written() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.writes...)
}

type fakeRelayStream struct {
	readCh   chan []byte
	closed   chan struct{}
	closeOne sync.Once
}

func newFakeRelayStream() *fakeRelayStream {
	return &fakeRelayStream{readCh: make(chan []byte), closed: make(chan struct{})}
}

func (s *fakeRelayStream) Read(buf []byte) (int, error) {
	select {
	case data := <-s.readCh:
		return copy(buf, data), nil
	case <-s.closed:
		return 0, io.EOF
	}
}

func (s *fakeRelayStream) Write(p []byte) (int, error) {
	s.readCh <- append([]byte(nil), p...)
	return len(p), nil
}
func (s *fakeRelayStream) Close() error {
	s.closeOne.Do(func() { close(s.closed) })
	return nil
}

type fakeWebSSHOpener struct {
	request relay.StreamRequest
	stream  *fakeRelayStream
	err     error
}

func (o *fakeWebSSHOpener) OpenStream(_ context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
	o.request = request
	if o.err != nil {
		return nil, o.err
	}
	return o.stream, nil
}
func (o *fakeWebSSHOpener) Close() error { return nil }

func webSSHBrokerFixture(t *testing.T) (*storage.DB, *WebSSHSessionService, *fakeWebSSHOpener, *WebSSHBroker, auth.Principal) {
	t.Helper()
	db, service, actor := webSSHServiceFixture(t)
	opener := &fakeWebSSHOpener{stream: newFakeRelayStream()}
	broker := NewWebSSHBroker(WebSSHBrokerDeps{Sessions: service, Opener: opener, MaxMessageBytes: 65536})
	return db, service, opener, broker, actor
}

func TestWebSSHBrokerBidirectionalBytes(t *testing.T) {
	_, service, opener, broker, actor := webSSHBrokerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticket, err := service.Create(ctx, actor, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	ws := newFakeWebSSHWS()
	go func() { ws.send(2, []byte("browser")) }()
	result := make(chan error, 1)
	go func() { result <- broker.Handle(ctx, ws, ticket.SessionID, ticket.Ticket) }()
	select {
	case data := <-opener.stream.readCh:
		if string(data) != "browser" {
			t.Fatalf("relay bytes = %q", data)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for browser bytes")
	}
	opener.stream.readCh <- []byte("agent")
	deadline := time.After(time.Second)
	for {
		writes := ws.written()
		if len(writes) == 1 && string(writes[0]) == "agent" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("websocket writes = %q", writes)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-result; err != nil && err != context.Canceled {
		t.Fatalf("handle error = %v", err)
	}
}

func TestWebSSHBrokerRejectsInvalidTicketWithoutOpeningRelay(t *testing.T) {
	_, service, opener, broker, actor := webSSHBrokerFixture(t)
	ctx := context.Background()
	ticket, err := service.Create(ctx, actor, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Handle(ctx, newFakeWebSSHWS(), ticket.SessionID, "wrong"); err != storage.ErrWebSSHTicketInvalid {
		t.Fatalf("invalid ticket error = %v", err)
	}
	if opener.request.AgentID != "" {
		t.Fatal("relay was opened before ticket authentication")
	}
}

func TestWebSSHBrokerAgentOfflineClosesDurableSession(t *testing.T) {
	db, service, opener, broker, actor := webSSHBrokerFixture(t)
	opener.err = errors.New("agent offline")
	ctx := context.Background()
	ticket, err := service.Create(ctx, actor, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	err = broker.Handle(ctx, newFakeWebSSHWS(), ticket.SessionID, ticket.Ticket)
	if err == nil || !strings.Contains(err.Error(), "agent offline") {
		t.Fatalf("agent offline error = %v", err)
	}
	session, err := db.WebSSHSessions().Get(ctx, ticket.SessionID)
	if err != nil || session.Status != storage.WebSSHSessionClosed || session.CloseReason != "agent_unreachable" {
		t.Fatalf("durable session = %+v, err=%v", session, err)
	}
}

// 远端 sshd 关闭通道（例如主机侧准入拒绝后立即断开）后，owning 节点必须把会话
// 回写 closed。否则 active 行会一直占用每用户配额直到 session_ttl，用户几次连接
// 之后就会被 "活跃 SSH 会话数已达上限" 锁死。
func TestWebSSHBrokerClosesDurableSessionWhenRemoteStreamEnds(t *testing.T) {
	db, service, opener, broker, actor := webSSHBrokerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticket, err := service.Create(ctx, actor, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	ws := newFakeWebSSHWS()
	result := make(chan error, 1)
	go func() { result <- broker.Handle(ctx, ws, ticket.SessionID, ticket.Ticket) }()
	waitForBridge(t, broker)

	_ = opener.stream.Close()
	if err := <-result; err != nil {
		t.Fatalf("handle error = %v", err)
	}
	session, err := db.WebSSHSessions().Get(ctx, ticket.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != storage.WebSSHSessionClosed {
		t.Fatalf("session status = %s, want closed after the remote stream ended", session.Status)
	}
	if session.CloseReason != "remote_closed" {
		t.Fatalf("close reason = %q, want remote_closed", session.CloseReason)
	}
}

// 浏览器关闭页面或断开 WebSocket 时同样必须回写 closed：标签页关闭不会调用管理
// API，服务端是唯一权威。
func TestWebSSHBrokerClosesDurableSessionWhenBrowserDisconnects(t *testing.T) {
	db, service, _, broker, actor := webSSHBrokerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticket, err := service.Create(ctx, actor, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	ws := newFakeWebSSHWS()
	result := make(chan error, 1)
	go func() { result <- broker.Handle(ctx, ws, ticket.SessionID, ticket.Ticket) }()
	waitForBridge(t, broker)

	_ = ws.Close()
	if err := <-result; err != nil {
		t.Fatalf("handle error = %v", err)
	}
	session, err := db.WebSSHSessions().Get(ctx, ticket.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != storage.WebSSHSessionClosed {
		t.Fatalf("session status = %s, want closed after the browser websocket ended", session.Status)
	}
	if session.CloseReason != "client_disconnected" {
		t.Fatalf("close reason = %q, want client_disconnected", session.CloseReason)
	}
}

func waitForBridge(t *testing.T, broker *WebSSHBroker) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for broker.ActiveCount() != 1 {
		select {
		case <-deadline:
			t.Fatalf("broker active sessions = %d, want 1", broker.ActiveCount())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestWebSSHBrokerRejectsNonBinaryAndOversizedMessages(t *testing.T) {
	_, service, opener, broker, actor := webSSHBrokerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticket, err := service.Create(ctx, actor, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	ws := newFakeWebSSHWS()
	go func() { ws.send(1, []byte("text")) }()
	if err := broker.Handle(ctx, ws, ticket.SessionID, ticket.Ticket); !errors.Is(err, ErrWebSSHMessageInvalid) {
		t.Fatalf("non-binary error = %v", err)
	}
	select {
	case <-opener.stream.closed:
	case <-time.After(time.Second):
		t.Fatal("relay was not closed")
	}

	ticket, err = service.Create(ctx, actor, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	opener.stream = newFakeRelayStream()
	ws = newFakeWebSSHWS()
	go func() { ws.send(2, bytes.Repeat([]byte("x"), 65537)) }()
	if err := broker.Handle(ctx, ws, ticket.SessionID, ticket.Ticket); !errors.Is(err, ErrWebSSHMessageInvalid) {
		t.Fatalf("oversized error = %v", err)
	}
}

func TestWebSSHBrokerNonBinaryClosesAndLocalCloseRegistry(t *testing.T) {
	_, service, opener, broker, actor := webSSHBrokerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticket, err := service.Create(ctx, actor, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	ws := newFakeWebSSHWS()
	result := make(chan error, 1)
	go func() { result <- broker.Handle(ctx, ws, ticket.SessionID, ticket.Ticket) }()
	deadline := time.After(time.Second)
	for broker.ActiveCount() != 1 {
		select {
		case <-deadline:
			t.Fatal("session was not registered")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := broker.CloseLocal(ticket.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("expected broker result after close")
	}
	select {
	case <-opener.stream.closed:
	case <-time.After(time.Second):
		t.Fatal("relay stream was not closed")
	}
	select {
	case <-ws.closeCh:
	case <-time.After(time.Second):
		t.Fatal("websocket was not closed")
	}
}

func TestWebSSHBrokerAppliesIdleReadDeadline(t *testing.T) {
	_, service, opener, broker, actor := webSSHBrokerFixture(t)
	service.SetLimits(WebSSHSessionLimits{IdleTimeout: 20 * time.Millisecond})
	ctx := context.Background()
	ticket, err := service.Create(ctx, actor, "server-a", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	ws := newDeadlineWebSSHWS()
	err = broker.Handle(ctx, ws, ticket.SessionID, ticket.Ticket)
	if err == nil || !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("idle error = %v", err)
	}
	select {
	case <-opener.stream.closed:
	case <-time.After(time.Second):
		t.Fatal("relay stream was not closed after idle timeout")
	}
}

type deadlineWebSSHWS struct {
	deadline atomic.Value
	closeCh  chan struct{}
	closeOne sync.Once
}

func newDeadlineWebSSHWS() *deadlineWebSSHWS {
	ws := &deadlineWebSSHWS{closeCh: make(chan struct{})}
	ws.deadline.Store(time.Now().Add(time.Hour))
	return ws
}

func (w *deadlineWebSSHWS) SetReadDeadline(deadline time.Time) error {
	w.deadline.Store(deadline)
	return nil
}

func (w *deadlineWebSSHWS) ReadMessage() (int, []byte, error) {
	deadline := w.deadline.Load().(time.Time)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-timer.C:
		return 0, nil, errors.New("webssh idle timeout")
	case <-w.closeCh:
		return 0, nil, io.EOF
	}
}

func (w *deadlineWebSSHWS) WriteMessage(_ int, _ []byte) error { return nil }

func (w *deadlineWebSSHWS) Close() error {
	w.closeOne.Do(func() { close(w.closeCh) })
	return nil
}
