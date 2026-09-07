# Task 6 fix round 1 review package

Fix base: Task 6 initial reviewed snapshot (HEAD remained `163fe12121d2f839ea4bf4907f55a8e7dd835057`)

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/agent/session_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/session_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/websocket.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/client/websocket.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/websocket_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/client/websocket_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/relay/transport.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/relay/transport.go differ
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/relay: transport_closewrite_test.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server: agent_relay_transport.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server: agent_relay_transport_test.go
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/runtime.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/runtime.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/session_manager.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/session_manager.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/ws_client.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/ws_client_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client_test.go differ
```

## Full fix-only diff

```diff
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/agent/session_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/session_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/agent/session_test.go	2026-09-06 18:38:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/agent/session_test.go	2026-09-06 18:38:01
@@ -101,6 +101,16 @@
 	read   []byte
 }
 
+type blockingStreamConn struct {
+	*streamConn
+	release chan struct{}
+}
+
+func (c *blockingStreamConn) Read([]byte) (int, error) {
+	<-c.release
+	return 0, io.EOF
+}
+
 func (c *streamConn) Read(p []byte) (int, error) {
 	if len(c.read) == 0 {
 		return 0, io.EOF
@@ -177,7 +187,9 @@
 }
 
 func TestStreamDispatcherRejectsDuplicateStreamID(t *testing.T) {
-	first, second := &streamConn{}, &streamConn{}
+	first := &blockingStreamConn{streamConn: &streamConn{}, release: make(chan struct{})}
+	defer close(first.release)
+	second := &streamConn{}
 	count := 0
 	d := NewStreamDispatcher(Dialer{}, func(context.Context, string, string, int) (io.ReadWriteCloser, error) {
 		count++
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/websocket.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/client/websocket.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/websocket.go	2026-09-06 17:56:59
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/client/websocket.go	2026-09-06 18:38:01
@@ -134,7 +134,11 @@
 	if delay > max {
 		delay = max
 	}
-	return time.Duration(float64(delay) * (0.8 + rng.Float64()*0.4))
+	delay = time.Duration(float64(delay) * (0.8 + rng.Float64()*0.4))
+	if delay > max {
+		return max
+	}
+	return delay
 }
 
 type clientWebSocketTransport struct {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/websocket_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/client/websocket_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/websocket_test.go	2026-09-06 17:56:59
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/client/websocket_test.go	2026-09-06 18:38:01
@@ -123,6 +123,19 @@
 	}
 }
 
+func TestClientWSReconnectJitterNeverExceedsMaxBackoff(t *testing.T) {
+	rng := rand.New(maximumRandSource{})
+	const maximum = 100 * time.Millisecond
+	if got := clientReconnectDelay(maximum, maximum, 1, rng); got > maximum {
+		t.Fatalf("clientReconnectDelay() = %v, want <= %v", got, maximum)
+	}
+}
+
+type maximumRandSource struct{}
+
+func (maximumRandSource) Int63() int64 { return 3 << 61 }
+func (maximumRandSource) Seed(int64)   {}
+
 func TestClientWSDialerSendsNoCookieOrQueryToken(t *testing.T) {
 	listener, err := net.Listen("tcp", "127.0.0.1:0")
 	if err != nil {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/relay/transport.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/relay/transport.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/relay/transport.go	2026-09-06 18:38:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/relay/transport.go	2026-09-06 18:38:01
@@ -277,7 +277,8 @@
 	}
 	return len(p), nil
 }
-func (c *grpcStreamConn) Close() error { return c.stream.CloseSend() }
+func (c *grpcStreamConn) CloseWrite() error { return c.stream.CloseSend() }
+func (c *grpcStreamConn) Close() error      { return c.CloseWrite() }
 
 // TLSConfig is intentionally passed in by callers so certificate policy is
 // explicit; relay does not silently disable peer verification.
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/relay/transport_closewrite_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/relay/transport_closewrite_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/relay/transport_closewrite_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/relay/transport_closewrite_test.go	2026-09-06 18:38:01
@@ -0,0 +1,32 @@
+package relay
+
+import (
+	"context"
+	"testing"
+
+	"google.golang.org/grpc/metadata"
+)
+
+func TestGRPCStreamConnExposesDirectionalCloseWrite(t *testing.T) {
+	fake := &fakeClientStream{}
+	conn := &grpcStreamConn{stream: fake}
+	halfCloser, ok := any(conn).(interface{ CloseWrite() error })
+	if !ok {
+		t.Fatal("grpcStreamConn does not expose CloseWrite")
+	}
+	if err := halfCloser.CloseWrite(); err != nil {
+		t.Fatal(err)
+	}
+	if fake.closeSendCalls != 1 {
+		t.Fatalf("CloseSend calls = %d, want 1", fake.closeSendCalls)
+	}
+}
+
+type fakeClientStream struct{ closeSendCalls int }
+
+func (*fakeClientStream) Header() (metadata.MD, error) { return nil, nil }
+func (*fakeClientStream) Trailer() metadata.MD         { return nil }
+func (s *fakeClientStream) CloseSend() error           { s.closeSendCalls++; return nil }
+func (*fakeClientStream) Context() context.Context     { return context.Background() }
+func (*fakeClientStream) SendMsg(any) error            { return nil }
+func (*fakeClientStream) RecvMsg(any) error            { return nil }
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/agent_relay_transport.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/agent_relay_transport.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/agent_relay_transport.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/agent_relay_transport.go	2026-09-06 18:38:01
@@ -0,0 +1,335 @@
+package server
+
+import (
+	"context"
+	"errors"
+	"io"
+	"sync"
+	"sync/atomic"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+	"github.com/tunnelmesh/tunnelmesh/internal/relay"
+)
+
+var errAgentRelayClosed = errors.New("server: local Agent relay closed")
+
+// AgentRelayTransport multiplexes local relay streams over the currently
+// registered Agent WebSocket sessions. Agent wire IDs are process-wide and
+// independent from Client connection/stream IDs.
+type AgentRelayTransport struct {
+	manager *AgentSessionManager
+	nextID  atomic.Uint32
+	mu      sync.Mutex
+	streams map[uint32]*agentRelayStream
+	closed  bool
+}
+
+func NewAgentRelayTransport(manager *AgentSessionManager) *AgentRelayTransport {
+	return &AgentRelayTransport{manager: manager, streams: make(map[uint32]*agentRelayStream)}
+}
+
+func (t *AgentRelayTransport) OpenStream(ctx context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
+	if t == nil || t.manager == nil || request.AgentID == "" || request.TargetHost == "" || request.TargetPort < 1 || request.TargetPort > 65535 {
+		return nil, relay.ErrNodeDisconnected
+	}
+	if ctx == nil {
+		ctx = context.Background()
+	}
+	if err := ctx.Err(); err != nil {
+		return nil, err
+	}
+	session, ok := t.manager.Get(request.AgentID)
+	if !ok {
+		return nil, relay.ErrNodeDisconnected
+	}
+	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: request.AgentID, Protocol: request.Protocol, TargetHost: request.TargetHost, TargetPort: request.TargetPort, Metadata: append([]byte(nil), request.Metadata...)})
+	if err != nil {
+		return nil, err
+	}
+	stream := &agentRelayStream{transport: t, agentID: request.AgentID, epoch: session.Epoch, readCh: make(chan []byte, 16), done: make(chan struct{})}
+	t.mu.Lock()
+	if t.closed {
+		t.mu.Unlock()
+		return nil, errAgentRelayClosed
+	}
+	for {
+		stream.wireID = t.nextID.Add(1)
+		if stream.wireID != 0 {
+			if _, exists := t.streams[stream.wireID]; !exists {
+				break
+			}
+		}
+	}
+	t.streams[stream.wireID] = stream
+	t.mu.Unlock()
+	if err := t.manager.SendGeneration(stream.agentID, stream.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: stream.wireID, Payload: payload}); err != nil {
+		t.detach(stream)
+		stream.fail(err)
+		return nil, err
+	}
+	return stream, nil
+}
+
+// HandleAgentFrame is the single inbound dispatch point used by the Agent
+// session callback. Epoch fencing prevents stale reconnect frames from being
+// delivered to streams opened on the replacement generation.
+func (t *AgentRelayTransport) HandleAgentFrame(agentID string, epoch int64, frame protocol.Frame) error {
+	if t == nil || t.manager == nil {
+		return errAgentRelayClosed
+	}
+	if err := frame.Validate(); err != nil {
+		return err
+	}
+	// Hold the current Agent generation stable through frame admission. Without
+	// this fence, an old WebSocket callback could deliver a frame after its
+	// replacement was registered but before the old session cleanup ran.
+	t.manager.mu.RLock()
+	session := t.manager.sessions[agentID]
+	if session == nil || session.Epoch != epoch {
+		t.manager.mu.RUnlock()
+		return nil
+	}
+	session.mu.RLock()
+	if session.closed || session.closing {
+		session.mu.RUnlock()
+		t.manager.mu.RUnlock()
+		return nil
+	}
+	t.mu.Lock()
+	stream := t.streams[frame.StreamID]
+	if stream == nil || stream.agentID != agentID || stream.epoch != epoch {
+		t.mu.Unlock()
+		session.mu.RUnlock()
+		t.manager.mu.RUnlock()
+		return nil
+	}
+	var resetBackpressure bool
+	switch frame.Type {
+	case protocol.FrameData:
+		if !stream.push(frame.Payload) {
+			delete(t.streams, frame.StreamID)
+			stream.fail(relay.ErrBackpressure)
+			resetBackpressure = true
+		}
+	case protocol.FrameHalfClose:
+		if stream.remoteHalfClose() {
+			delete(t.streams, frame.StreamID)
+		}
+	case protocol.FrameReset:
+		delete(t.streams, frame.StreamID)
+		stream.fail(protocol.ErrStreamReset)
+	}
+	t.mu.Unlock()
+	session.mu.RUnlock()
+	t.manager.mu.RUnlock()
+	if resetBackpressure {
+		_ = t.manager.SendGeneration(agentID, epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: frame.StreamID})
+	}
+	return nil
+}
+
+func (t *AgentRelayTransport) FailAgentGeneration(agentID string, epoch int64) {
+	if t == nil {
+		return
+	}
+	t.mu.Lock()
+	var failed []*agentRelayStream
+	for id, stream := range t.streams {
+		if stream.agentID == agentID && stream.epoch == epoch {
+			delete(t.streams, id)
+			failed = append(failed, stream)
+		}
+	}
+	t.mu.Unlock()
+	for _, stream := range failed {
+		stream.fail(relay.ErrNodeDisconnected)
+	}
+}
+
+func (t *AgentRelayTransport) Close() error {
+	if t == nil {
+		return nil
+	}
+	t.mu.Lock()
+	if t.closed {
+		t.mu.Unlock()
+		return nil
+	}
+	t.closed = true
+	streams := make([]*agentRelayStream, 0, len(t.streams))
+	for id, stream := range t.streams {
+		delete(t.streams, id)
+		streams = append(streams, stream)
+	}
+	t.mu.Unlock()
+	for _, stream := range streams {
+		stream.fail(errAgentRelayClosed)
+	}
+	return nil
+}
+
+func (t *AgentRelayTransport) detach(stream *agentRelayStream) {
+	if t == nil || stream == nil {
+		return
+	}
+	t.mu.Lock()
+	if t.streams[stream.wireID] == stream {
+		delete(t.streams, stream.wireID)
+	}
+	t.mu.Unlock()
+}
+
+type agentRelayStream struct {
+	transport  *AgentRelayTransport
+	agentID    string
+	epoch      int64
+	wireID     uint32
+	readCh     chan []byte
+	done       chan struct{}
+	doneOnce   sync.Once
+	closeOnce  sync.Once
+	mu         sync.Mutex
+	readBuf    []byte
+	err        error
+	localHalf  bool
+	remoteHalf bool
+}
+
+func (s *agentRelayStream) Read(buffer []byte) (int, error) {
+	if len(buffer) == 0 {
+		return 0, nil
+	}
+	for {
+		s.mu.Lock()
+		if len(s.readBuf) > 0 {
+			n := copy(buffer, s.readBuf)
+			s.readBuf = s.readBuf[n:]
+			s.mu.Unlock()
+			return n, nil
+		}
+		s.mu.Unlock()
+		select {
+		case payload := <-s.readCh:
+			s.mu.Lock()
+			s.readBuf = append(s.readBuf, payload...)
+			s.mu.Unlock()
+			continue
+		default:
+		}
+		s.mu.Lock()
+		err := s.err
+		s.mu.Unlock()
+		if err != nil {
+			return 0, err
+		}
+		select {
+		case payload := <-s.readCh:
+			s.mu.Lock()
+			s.readBuf = append(s.readBuf, payload...)
+			s.mu.Unlock()
+		case <-s.done:
+		}
+	}
+}
+
+func (s *agentRelayStream) Write(payload []byte) (int, error) {
+	if len(payload) == 0 {
+		return 0, nil
+	}
+	if len(payload) > protocol.MaxPayload {
+		return 0, protocol.ErrPayloadTooLarge
+	}
+	s.mu.Lock()
+	if (s.err != nil && !errors.Is(s.err, io.EOF)) || s.localHalf {
+		err := s.err
+		if err == nil {
+			err = io.ErrClosedPipe
+		}
+		s.mu.Unlock()
+		return 0, err
+	}
+	s.mu.Unlock()
+	if err := s.transport.manager.SendGeneration(s.agentID, s.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.wireID, Payload: append([]byte(nil), payload...)}); err != nil {
+		s.transport.detach(s)
+		s.fail(err)
+		return 0, err
+	}
+	return len(payload), nil
+}
+
+func (s *agentRelayStream) CloseWrite() error {
+	s.mu.Lock()
+	if s.err != nil && !errors.Is(s.err, io.EOF) {
+		err := s.err
+		s.mu.Unlock()
+		return err
+	}
+	if s.localHalf {
+		s.mu.Unlock()
+		return nil
+	}
+	s.localHalf = true
+	complete := s.remoteHalf
+	s.mu.Unlock()
+	if err := s.transport.manager.SendGeneration(s.agentID, s.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: s.wireID}); err != nil {
+		s.transport.detach(s)
+		s.fail(err)
+		return err
+	}
+	if complete {
+		s.transport.detach(s)
+	}
+	return nil
+}
+
+func (s *agentRelayStream) Close() error {
+	s.closeOnce.Do(func() {
+		s.transport.detach(s)
+		s.mu.Lock()
+		complete := s.localHalf && s.remoteHalf
+		terminal := s.err != nil && !errors.Is(s.err, io.EOF)
+		s.mu.Unlock()
+		if !complete && !terminal {
+			_ = s.transport.manager.SendGeneration(s.agentID, s.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: s.wireID})
+		}
+		s.fail(io.ErrClosedPipe)
+	})
+	return nil
+}
+
+func (s *agentRelayStream) push(payload []byte) bool {
+	select {
+	case s.readCh <- append([]byte(nil), payload...):
+		return true
+	case <-s.done:
+		return false
+	default:
+		return false
+	}
+}
+
+func (s *agentRelayStream) remoteHalfClose() bool {
+	s.mu.Lock()
+	if s.remoteHalf {
+		s.mu.Unlock()
+		return false
+	}
+	s.remoteHalf = true
+	complete := s.localHalf
+	s.mu.Unlock()
+	s.finish(io.EOF)
+	return complete
+}
+
+func (s *agentRelayStream) fail(err error) { s.finish(err) }
+
+func (s *agentRelayStream) finish(err error) {
+	s.mu.Lock()
+	// A remote HALF_CLOSE publishes EOF for the read direction, but a later
+	// RESET or generation failure must still terminate the write direction.
+	if s.err == nil || (errors.Is(s.err, io.EOF) && !errors.Is(err, io.EOF)) {
+		s.err = err
+	}
+	s.mu.Unlock()
+	s.doneOnce.Do(func() { close(s.done) })
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/agent_relay_transport_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/agent_relay_transport_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/agent_relay_transport_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/agent_relay_transport_test.go	2026-09-06 18:38:01
@@ -0,0 +1,154 @@
+package server
+
+import (
+	"context"
+	"errors"
+	"io"
+	"testing"
+	"time"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+	"github.com/tunnelmesh/tunnelmesh/internal/relay"
+)
+
+func TestAgentRelayTransportAllocatesIndependentWireIDsAndFencesReconnectGeneration(t *testing.T) {
+	manager := NewAgentSessionManager(AgentSessionConfig{})
+	firstTransport := newFakeTransport()
+	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-mux", NodeID: "node-a", Epoch: 1}, firstTransport); err != nil {
+		t.Fatal(err)
+	}
+	mux := NewAgentRelayTransport(manager)
+	defer mux.Close()
+	first, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-mux", StreamID: 7, Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	second, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-mux", StreamID: 7, Protocol: "tcp", TargetHost: "10.0.0.9", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	firstOpen := receiveAgentRelayFrame(t, firstTransport)
+	secondOpen := receiveAgentRelayFrame(t, firstTransport)
+	if firstOpen.StreamID == 0 || secondOpen.StreamID == 0 || firstOpen.StreamID == secondOpen.StreamID {
+		t.Fatalf("Agent wire IDs = %d and %d, want distinct non-zero IDs", firstOpen.StreamID, secondOpen.StreamID)
+	}
+
+	newTransport := newFakeTransport()
+	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-mux", NodeID: "node-b", Epoch: 2}, newTransport); err != nil {
+		t.Fatal(err)
+	}
+	mux.FailAgentGeneration("agent-mux", 1)
+	if _, err := first.Read(make([]byte, 1)); !errors.Is(err, relay.ErrNodeDisconnected) {
+		t.Fatalf("old generation Read() error = %v, want ErrNodeDisconnected", err)
+	}
+	if _, err := second.Write([]byte("stale")); !errors.Is(err, relay.ErrNodeDisconnected) {
+		t.Fatalf("old generation Write() error = %v, want ErrNodeDisconnected", err)
+	}
+
+	current, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-mux", Protocol: "tcp", TargetHost: "10.0.0.10", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	currentOpen := receiveAgentRelayFrame(t, newTransport)
+	if err := mux.HandleAgentFrame("agent-mux", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("stale-data")}); err != nil {
+		t.Fatal(err)
+	}
+	if err := mux.HandleAgentFrame("agent-mux", 2, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("current-data")}); err != nil {
+		t.Fatal(err)
+	}
+	buffer := make([]byte, len("current-data"))
+	if _, err := io.ReadFull(current, buffer); err != nil || string(buffer) != "current-data" {
+		t.Fatalf("current generation data = %q, err = %v", buffer, err)
+	}
+	_ = current.Close()
+}
+
+func TestAgentRelayTransportResetOverridesPriorRemoteHalfClose(t *testing.T) {
+	manager := NewAgentSessionManager(AgentSessionConfig{})
+	agentTransport := newFakeTransport()
+	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-reset", NodeID: "node-reset", Epoch: 1}, agentTransport); err != nil {
+		t.Fatal(err)
+	}
+	mux := NewAgentRelayTransport(manager)
+	defer mux.Close()
+	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-reset", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	open := receiveAgentRelayFrame(t, agentTransport)
+	if err := mux.HandleAgentFrame("agent-reset", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
+		t.Fatal(err)
+	}
+	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
+		t.Fatalf("Read() after HALF_CLOSE error = %v, want EOF", err)
+	}
+	if err := mux.HandleAgentFrame("agent-reset", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
+		t.Fatal(err)
+	}
+	if _, err := stream.Write([]byte("must-not-send")); !errors.Is(err, protocol.ErrStreamReset) {
+		t.Fatalf("Write() after RESET error = %v, want ErrStreamReset", err)
+	}
+}
+
+func TestAgentRelayTransportRejectsOldGenerationDataAfterReplacementRegistration(t *testing.T) {
+	manager := NewAgentSessionManager(AgentSessionConfig{})
+	oldTransport := newFakeTransport()
+	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-fence", NodeID: "node-old", Epoch: 1}, oldTransport); err != nil {
+		t.Fatal(err)
+	}
+	mux := NewAgentRelayTransport(manager)
+	defer mux.Close()
+	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-fence", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	open := receiveAgentRelayFrame(t, oldTransport)
+	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-fence", NodeID: "node-new", Epoch: 2}, newFakeTransport()); err != nil {
+		t.Fatal(err)
+	}
+	if err := mux.HandleAgentFrame("agent-fence", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("stale-data")}); err != nil {
+		t.Fatal(err)
+	}
+	mux.FailAgentGeneration("agent-fence", 1)
+	buffer := make([]byte, len("stale-data"))
+	if n, err := stream.Read(buffer); n != 0 || !errors.Is(err, relay.ErrNodeDisconnected) {
+		t.Fatalf("Read() after replacement = (%d, %v), payload %q; want (0, ErrNodeDisconnected)", n, err, buffer[:n])
+	}
+}
+
+func TestAgentRelayTransportCloseDoesNotEchoRemoteReset(t *testing.T) {
+	manager := NewAgentSessionManager(AgentSessionConfig{})
+	agentTransport := newFakeTransport()
+	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-no-echo", NodeID: "node-no-echo", Epoch: 1}, agentTransport); err != nil {
+		t.Fatal(err)
+	}
+	mux := NewAgentRelayTransport(manager)
+	defer mux.Close()
+	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-no-echo", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	open := receiveAgentRelayFrame(t, agentTransport)
+	if err := mux.HandleAgentFrame("agent-no-echo", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
+		t.Fatal(err)
+	}
+	if err := stream.Close(); err != nil {
+		t.Fatal(err)
+	}
+	select {
+	case frame := <-agentTransport.sent:
+		t.Fatalf("Close() echoed terminal Agent frame: %+v", frame)
+	case <-time.After(50 * time.Millisecond):
+	}
+}
+
+func receiveAgentRelayFrame(t *testing.T, transport *fakeTransport) protocol.Frame {
+	t.Helper()
+	select {
+	case frame := <-transport.sent:
+		return frame
+	case <-time.After(time.Second):
+		t.Fatal("timed out waiting for Agent relay frame")
+		return protocol.Frame{}
+	}
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/runtime.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/runtime.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/runtime.go	2026-09-06 17:56:59
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/runtime.go	2026-09-06 18:38:01
@@ -44,6 +44,7 @@
 	Credentials      *auth.CredentialService
 	ClientAuthorizer StreamAuthorizer
 	ClientTransport  relay.NodeTransport
+	LocalAgentRelay  *AgentRelayTransport
 	config           RuntimeConfig
 }
 
@@ -62,16 +63,25 @@
 		runtimeConfig.Security.AllowedHosts = append([]string(nil), runtimeConfig.Security.AllowedHosts...)
 		runtimeConfig.Security.AllowedOrigins = append([]string(nil), runtimeConfig.Security.AllowedOrigins...)
 	}
-	return &ServerRuntime{DB: db, AgentSessions: NewAgentSessionManagerWithMetadata(db.Metadata(), cfg), ClientSessions: NewClientSessionManager(), API: NewAPI(db, authService), Auth: authService, Credentials: credentials, ClientAuthorizer: NewCredentialStreamAuthorizer(credentials), config: runtimeConfig}, nil
+	agentSessions := NewAgentSessionManagerWithMetadata(db.Metadata(), cfg)
+	localAgentRelay := NewAgentRelayTransport(agentSessions)
+	return &ServerRuntime{DB: db, AgentSessions: agentSessions, ClientSessions: NewClientSessionManager(), API: NewAPI(db, authService), Auth: authService, Credentials: credentials, ClientAuthorizer: NewCredentialStreamAuthorizer(credentials), ClientTransport: localAgentRelay, LocalAgentRelay: localAgentRelay, config: runtimeConfig}, nil
 }
 
 // Close releases runtime-owned background workers. The caller continues to
 // own the database lifecycle.
 func (r *ServerRuntime) Close() error {
-	if r == nil || r.Credentials == nil {
+	if r == nil {
 		return nil
 	}
-	return r.Credentials.Close()
+	var relayErr, credentialErr error
+	if r.LocalAgentRelay != nil {
+		relayErr = r.LocalAgentRelay.Close()
+	}
+	if r.Credentials != nil {
+		credentialErr = r.Credentials.Close()
+	}
+	return errors.Join(relayErr, credentialErr)
 }
 
 // Handler exposes management API, embedded web assets, and the Agent WebSocket
@@ -168,7 +178,14 @@
 		return
 	}
 	registration := AgentRegistration{AgentID: payload.AgentID, NodeID: payload.NodeID, Epoch: payload.Epoch, TokenID: tokenID}
-	_ = ServeAgentSessionWithInitialFrame(ctx, r.AgentSessions, registration, transport, initial, nil)
+	if r.LocalAgentRelay == nil {
+		_ = transport.Close()
+		return
+	}
+	defer r.LocalAgentRelay.FailAgentGeneration(registration.AgentID, registration.Epoch)
+	_ = ServeAgentSessionWithInitialFrame(ctx, r.AgentSessions, registration, transport, initial, func(frame protocol.Frame) error {
+		return r.LocalAgentRelay.HandleAgentFrame(registration.AgentID, registration.Epoch, frame)
+	})
 }
 
 func (r *ServerRuntime) preauthenticateAgentConnection(ctx context.Context, raw string) (agentConnectionAuthentication, error) {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/session_manager.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/session_manager.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/session_manager.go	2026-09-06 17:56:59
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/session_manager.go	2026-09-06 18:38:01
@@ -368,6 +368,49 @@
 		return ErrBackpressure
 	}
 }
+
+// SendGeneration queues a frame only when the expected Agent epoch is still
+// current. Holding the manager read lock fences replacement registration
+// between generation validation and queue admission.
+func (m *AgentSessionManager) SendGeneration(id string, epoch int64, f protocol.Frame) error {
+	if m == nil {
+		return ErrSessionClosed
+	}
+	m.mu.RLock()
+	s := m.sessions[id]
+	if s == nil {
+		m.mu.RUnlock()
+		return ErrSessionClosed
+	}
+	if s.Epoch != epoch {
+		m.mu.RUnlock()
+		return ErrEpoch
+	}
+	s.mu.RLock()
+	if s.closed || s.closing {
+		s.mu.RUnlock()
+		m.mu.RUnlock()
+		return ErrSessionClosed
+	}
+	select {
+	case s.slots <- struct{}{}:
+	default:
+		s.mu.RUnlock()
+		m.mu.RUnlock()
+		return ErrBackpressure
+	}
+	select {
+	case s.queue <- f:
+		s.mu.RUnlock()
+		m.mu.RUnlock()
+		return nil
+	default:
+		<-s.slots
+		s.mu.RUnlock()
+		m.mu.RUnlock()
+		return ErrBackpressure
+	}
+}
 func (m *AgentSessionManager) GoAway(id string) error {
 	s, ok := m.Get(id)
 	if !ok {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/ws_client.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/ws_client.go	2026-09-06 17:56:59
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client.go	2026-09-06 18:38:01
@@ -4,6 +4,7 @@
 	"context"
 	"errors"
 	"io"
+	"strings"
 	"sync"
 
 	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
@@ -29,6 +30,7 @@
 
 type clientRelayStream struct {
 	conn             io.ReadWriteCloser
+	protocol         string
 	clientHalfClosed bool
 	relayHalfClosed  bool
 }
@@ -44,6 +46,7 @@
 	}
 	var mu sync.Mutex
 	streams := make(map[uint32]*clientRelayStream)
+	seen := make(map[uint32]struct{})
 	closeStream := func(id uint32) {
 		mu.Lock()
 		stream := streams[id]
@@ -97,7 +100,8 @@
 			return nil
 		case protocol.FrameOpenStream:
 			mu.Lock()
-			_, duplicate := streams[frame.StreamID]
+			_, duplicate := seen[frame.StreamID]
+			seen[frame.StreamID] = struct{}{}
 			mu.Unlock()
 			if duplicate {
 				_ = reset(frame.StreamID)
@@ -113,7 +117,7 @@
 				_ = reset(frame.StreamID)
 				continue
 			}
-			stream := &clientRelayStream{conn: conn}
+			stream := &clientRelayStream{conn: conn, protocol: request.Protocol}
 			mu.Lock()
 			streams[frame.StreamID] = stream
 			mu.Unlock()
@@ -145,8 +149,11 @@
 				_ = reset(frame.StreamID)
 				continue
 			}
-			if halfCloser, ok := stream.conn.(interface{ CloseWrite() error }); ok {
-				_ = halfCloser.CloseWrite()
+			halfCloser, ok := stream.conn.(interface{ CloseWrite() error })
+			if !ok || halfCloser.CloseWrite() != nil {
+				closeStream(frame.StreamID)
+				_ = reset(frame.StreamID)
+				continue
 			}
 			mu.Lock()
 			complete := stream.relayHalfClosed
@@ -170,11 +177,26 @@
 }
 
 func relayToClient(id uint32, stream *clientRelayStream, tr FrameTransport, mu *sync.Mutex, streams map[uint32]*clientRelayStream) {
-	buffer := make([]byte, 32<<10)
+	bufferSize := 32 << 10
+	if strings.EqualFold(stream.protocol, "udp") {
+		bufferSize = protocol.MaxPayload
+	}
+	buffer := make([]byte, bufferSize)
 	for {
 		n, err := stream.conn.Read(buffer)
 		if n > 0 {
+			mu.Lock()
+			current := streams[id]
+			mu.Unlock()
+			if current != stream {
+				return
+			}
 			if sendErr := tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: id, Payload: append([]byte(nil), buffer[:n]...)}); sendErr != nil {
+				mu.Lock()
+				if streams[id] == stream {
+					delete(streams, id)
+				}
+				mu.Unlock()
 				_ = stream.conn.Close()
 				return
 			}
@@ -190,8 +212,18 @@
 			if current != stream {
 				return
 			}
+			if !errors.Is(err, io.EOF) {
+				mu.Lock()
+				if streams[id] == stream {
+					delete(streams, id)
+				}
+				mu.Unlock()
+				_ = stream.conn.Close()
+				_ = tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: id, Payload: []byte(clientStreamResetMessage)})
+				return
+			}
 			_ = tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: id})
-			if complete || !errors.Is(err, io.EOF) {
+			if complete {
 				mu.Lock()
 				if streams[id] == stream {
 					delete(streams, id)
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/ws_client_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/ws_client_test.go	2026-09-06 17:56:59
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix1-after/internal/server/ws_client_test.go	2026-09-06 18:38:01
@@ -183,6 +183,94 @@
 	}
 }
 
+func TestClientWSRuntimeDefaultTransportBridgesRegisteredAgentBidirectionally(t *testing.T) {
+	ctx := context.Background()
+	db, err := storage.OpenSQLite(ctx, "file:client-ws-runtime-agent?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	authService := auth.NewAuthService(db)
+	owner, err := authService.CreateUser(ctx, "runtime-client-owner", "runtime-client-password", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Agents().Create(ctx, storage.Agent{ID: "runtime-agent", Name: "runtime-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
+		t.Fatal(err)
+	}
+	now := time.Now().UTC()
+	if err := db.Policies().Create(ctx, storage.AgentPolicy{ID: "runtime-client-policy", AgentID: "runtime-agent", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", CreatedAt: now, UpdatedAt: now}); err != nil {
+		t.Fatal(err)
+	}
+	credentials := auth.NewCredentialService(db)
+	agentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "runtime-agent"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID, Scope: auth.TokenScope{AgentIDs: []string{"runtime-agent"}, Protocols: []string{"tcp"}, TargetPorts: []int{22}}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	_ = credentials.Close()
+
+	_, address, stop := startClientWSTestRuntime(t, db)
+	defer stop()
+	agentConfig, _ := websocket.NewConfig("ws://"+address+"/ws/agent", "http://"+address)
+	agentConfig.Header.Set("Authorization", "Bearer "+agentToken.Secret)
+	agentConn, err := websocket.DialConfig(agentConfig)
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer agentConn.Close()
+	hello, _ := protocol.EncodeAgentMetadataPayload(protocol.AgentMetadataPayload{AgentID: "runtime-agent", NodeID: "runtime-node", Epoch: 1, Revision: 1, ReportedAt: time.Now().UTC(), Items: []protocol.AgentMetadataItem{}})
+	if err := websocket.Message.Send(agentConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameAgentHello, Payload: hello})); err != nil {
+		t.Fatal(err)
+	}
+	if ack := receiveClientServerFrame(t, agentConn); ack.Type != protocol.FrameAgentMetadataAck {
+		t.Fatalf("agent hello response = %+v", ack)
+	}
+
+	clientConfig, _ := websocket.NewConfig("ws://"+address+"/ws/client", "http://"+address)
+	clientConfig.Header.Set("Authorization", "Bearer "+clientToken.Secret)
+	clientConn, err := websocket.DialConfig(clientConfig)
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer clientConn.Close()
+	openPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "runtime-agent", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
+	if err := websocket.Message.Send(clientConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 11, Payload: openPayload})); err != nil {
+		t.Fatal(err)
+	}
+	agentOpen := receiveClientServerFrame(t, agentConn)
+	if agentOpen.Type != protocol.FrameOpenStream || agentOpen.StreamID == 0 {
+		t.Fatalf("Agent OPEN = %+v", agentOpen)
+	}
+	if err := websocket.Message.Send(clientConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 11, Payload: []byte("client-to-agent")})); err != nil {
+		t.Fatal(err)
+	}
+	if frame := receiveClientServerFrame(t, agentConn); frame.Type != protocol.FrameData || frame.StreamID != agentOpen.StreamID || string(frame.Payload) != "client-to-agent" {
+		t.Fatalf("Agent DATA = %+v", frame)
+	}
+	if err := websocket.Message.Send(agentConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: agentOpen.StreamID, Payload: []byte("agent-to-client")})); err != nil {
+		t.Fatal(err)
+	}
+	if frame := receiveClientServerFrame(t, clientConn); frame.Type != protocol.FrameData || frame.StreamID != 11 || string(frame.Payload) != "agent-to-client" {
+		t.Fatalf("Client DATA = %+v", frame)
+	}
+	if err := websocket.Message.Send(clientConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 11})); err != nil {
+		t.Fatal(err)
+	}
+	if frame := receiveClientServerFrame(t, agentConn); frame.Type != protocol.FrameHalfClose || frame.StreamID != agentOpen.StreamID {
+		t.Fatalf("Agent HALF_CLOSE = %+v", frame)
+	}
+	if err := websocket.Message.Send(agentConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: agentOpen.StreamID})); err != nil {
+		t.Fatal(err)
+	}
+	if frame := receiveClientServerFrame(t, clientConn); frame.Type != protocol.FrameHalfClose || frame.StreamID != 11 {
+		t.Fatalf("Client HALF_CLOSE = %+v", frame)
+	}
+}
+
 func TestServeClientSessionRejectsInvalidStreamTransitionsWithoutPanicking(t *testing.T) {
 	transport := newScriptedClientTransport(
 		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 99, Payload: []byte("unknown")},
@@ -227,6 +315,146 @@
 	}
 }
 
+func TestServeClientSessionNeverReusesStreamIDOrForwardsLateOldData(t *testing.T) {
+	transport := newChannelClientTransport()
+	old := newLateReadConn()
+	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{old, newLateReadConn()}, opened: make(chan relay.StreamRequest, 2)}
+	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
+	if err != nil {
+		t.Fatal(err)
+	}
+	done := make(chan error, 1)
+	go func() {
+		done <- ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-reuse", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
+	}()
+	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 5, Payload: payload}
+	<-opener.opened
+	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: 5}
+	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 5, Payload: payload}
+	reset := receiveChannelClientFrame(t, transport)
+	if reset.Type != protocol.FrameReset || reset.StreamID != 5 {
+		t.Fatalf("reused ID response = %+v", reset)
+	}
+	select {
+	case request := <-opener.opened:
+		t.Fatalf("reused ID opened a second relay stream: %+v", request)
+	case <-time.After(50 * time.Millisecond):
+	}
+	old.reads <- readResult{payload: []byte("late-secret")}
+	select {
+	case frame := <-transport.sent:
+		if frame.Type == protocol.FrameData {
+			t.Fatalf("late old DATA reached Client: %+v", frame)
+		}
+	case <-time.After(50 * time.Millisecond):
+	}
+	close(transport.receive)
+	if err := <-done; err != nil {
+		t.Fatal(err)
+	}
+}
+
+func TestServeClientSessionNeverReusesStreamIDAfterCompleteHalfClose(t *testing.T) {
+	transport := newChannelClientTransport()
+	conn := newDirectionalReadConn()
+	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{conn, newDirectionalReadConn()}, opened: make(chan relay.StreamRequest, 2)}
+	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
+	done := make(chan error, 1)
+	go func() {
+		done <- ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-complete-reuse", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
+	}()
+	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 12, Payload: payload}
+	<-opener.opened
+	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 12}
+	select {
+	case <-conn.writeClosed:
+	case <-time.After(time.Second):
+		t.Fatal("relay CloseWrite was not called")
+	}
+	conn.reads <- readResult{err: io.EOF}
+	if frame := receiveChannelClientFrame(t, transport); frame.Type != protocol.FrameHalfClose || frame.StreamID != 12 {
+		t.Fatalf("complete response = %+v", frame)
+	}
+	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 12, Payload: payload}
+	if frame := receiveChannelClientFrame(t, transport); frame.Type != protocol.FrameReset || frame.StreamID != 12 {
+		t.Fatalf("reused completed ID response = %+v", frame)
+	}
+	select {
+	case request := <-opener.opened:
+		t.Fatalf("completed stream ID opened again: %+v", request)
+	case <-time.After(50 * time.Millisecond):
+	}
+	close(transport.receive)
+	if err := <-done; err != nil {
+		t.Fatal(err)
+	}
+}
+
+func TestServeClientSessionResetsHalfCloseWithoutDirectionalRelaySupport(t *testing.T) {
+	transport := newChannelClientTransport()
+	conn := newLateReadConn()
+	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{conn}, opened: make(chan relay.StreamRequest, 1)}
+	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
+	done := make(chan error, 1)
+	go func() {
+		done <- ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-half-close", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
+	}()
+	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 6, Payload: payload}
+	<-opener.opened
+	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 6}
+	frame := receiveChannelClientFrame(t, transport)
+	if frame.Type != protocol.FrameReset || frame.StreamID != 6 {
+		t.Fatalf("half-close response = %+v, want RESET", frame)
+	}
+	close(transport.receive)
+	if err := <-done; err != nil {
+		t.Fatal(err)
+	}
+}
+
+func TestServeClientSessionMapsNonEOFRelayReadErrorToReset(t *testing.T) {
+	transport := newChannelClientTransport()
+	conn := newLateReadConn()
+	conn.reads <- readResult{err: errors.New("private relay failure")}
+	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{conn}, opened: make(chan relay.StreamRequest, 1)}
+	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
+	done := make(chan error, 1)
+	go func() {
+		done <- ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-read-error", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
+	}()
+	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 8, Payload: payload}
+	frame := receiveChannelClientFrame(t, transport)
+	if frame.Type != protocol.FrameReset || frame.StreamID != 8 || string(frame.Payload) != clientStreamResetMessage {
+		t.Fatalf("relay error response = %+v, want bounded RESET", frame)
+	}
+	close(transport.receive)
+	if err := <-done; err != nil {
+		t.Fatal(err)
+	}
+}
+
+func TestServeClientSessionPreservesLargeUDPFrameBoundary(t *testing.T) {
+	transport := newChannelClientTransport()
+	payload := bytes.Repeat([]byte("u"), 64<<10)
+	conn := &singlePayloadConn{payload: payload, release: make(chan struct{})}
+	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{conn}, opened: make(chan relay.StreamRequest, 1)}
+	openPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "udp", TargetHost: "h", TargetPort: 53})
+	done := make(chan error, 1)
+	go func() {
+		done <- ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-udp-boundary", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
+	}()
+	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 14, Payload: openPayload}
+	frame := receiveChannelClientFrame(t, transport)
+	if frame.Type != protocol.FrameData || frame.StreamID != 14 || len(frame.Payload) != len(payload) {
+		t.Fatalf("UDP DATA type=%d id=%d bytes=%d, want one %d-byte frame", frame.Type, frame.StreamID, len(frame.Payload), len(payload))
+	}
+	_ = conn.Close()
+	close(transport.receive)
+	if err := <-done; err != nil {
+		t.Fatal(err)
+	}
+}
+
 type streamAuthorizerFunc func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error
 
 func (f streamAuthorizerFunc) Authorize(ctx context.Context, principal ClientSessionPrincipal, request protocol.StreamOpenPayload) error {
@@ -280,6 +508,108 @@
 	receive []protocol.Frame
 	sent    []protocol.Frame
 }
+
+type channelClientTransport struct {
+	receive chan protocol.Frame
+	sent    chan protocol.Frame
+}
+
+func newChannelClientTransport() *channelClientTransport {
+	return &channelClientTransport{receive: make(chan protocol.Frame, 8), sent: make(chan protocol.Frame, 8)}
+}
+
+func receiveChannelClientFrame(t *testing.T, transport *channelClientTransport) protocol.Frame {
+	t.Helper()
+	select {
+	case frame := <-transport.sent:
+		return frame
+	case <-time.After(time.Second):
+		t.Fatal("timed out waiting for Client frame")
+		return protocol.Frame{}
+	}
+}
+
+func (t *channelClientTransport) Receive() (protocol.Frame, error) {
+	frame, ok := <-t.receive
+	if !ok {
+		return protocol.Frame{}, io.EOF
+	}
+	return frame, nil
+}
+func (t *channelClientTransport) Send(frame protocol.Frame) error { t.sent <- frame; return nil }
+func (t *channelClientTransport) Close() error                    { return nil }
+
+type readResult struct {
+	payload []byte
+	err     error
+}
+
+type lateReadConn struct {
+	reads chan readResult
+}
+
+func newLateReadConn() *lateReadConn { return &lateReadConn{reads: make(chan readResult, 4)} }
+func (c *lateReadConn) Read(buffer []byte) (int, error) {
+	result := <-c.reads
+	return copy(buffer, result.payload), result.err
+}
+func (c *lateReadConn) Write(payload []byte) (int, error) { return len(payload), nil }
+func (c *lateReadConn) Close() error                      { return nil }
+
+type directionalReadConn struct {
+	*lateReadConn
+	writeClosed chan struct{}
+	once        sync.Once
+}
+
+type singlePayloadConn struct {
+	payload []byte
+	release chan struct{}
+	once    sync.Once
+}
+
+func (c *singlePayloadConn) Read(buffer []byte) (int, error) {
+	if len(c.payload) > 0 {
+		n := copy(buffer, c.payload)
+		c.payload = c.payload[n:]
+		return n, nil
+	}
+	<-c.release
+	return 0, io.EOF
+}
+func (c *singlePayloadConn) Write(payload []byte) (int, error) { return len(payload), nil }
+func (c *singlePayloadConn) CloseWrite() error                 { return nil }
+func (c *singlePayloadConn) Close() error {
+	c.once.Do(func() { close(c.release) })
+	return nil
+}
+
+func newDirectionalReadConn() *directionalReadConn {
+	return &directionalReadConn{lateReadConn: newLateReadConn(), writeClosed: make(chan struct{})}
+}
+func (c *directionalReadConn) CloseWrite() error {
+	c.once.Do(func() { close(c.writeClosed) })
+	return nil
+}
+
+type queuedNodeTransport struct {
+	mu          sync.Mutex
+	connections []io.ReadWriteCloser
+	opened      chan relay.StreamRequest
+}
+
+func (t *queuedNodeTransport) OpenStream(_ context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
+	t.mu.Lock()
+	defer t.mu.Unlock()
+	t.opened <- request
+	if len(t.connections) == 0 {
+		return nil, errors.New("no queued connection")
+	}
+	conn := t.connections[0]
+	t.connections = t.connections[1:]
+	return conn, nil
+}
+func (t *queuedNodeTransport) Close() error { return nil }
 
 func newScriptedClientTransport(frames ...protocol.Frame) *scriptedClientTransport {
 	return &scriptedClientTransport{receive: frames}
```
