# Task 6 fix round 3 review package

Fix base: Task 6 fix round 2 reviewed snapshot (HEAD remained `163fe12121d2f839ea4bf4907f55a8e7dd835057`)

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/runtime.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/runtime.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/session_manager.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/session_manager.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/ws_agent.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/ws_agent.go differ
```

## Full fix-only diff

```diff
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport.go	2026-09-06 19:30:16
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport.go	2026-09-06 19:52:40
@@ -18,7 +18,7 @@
 
 // AgentRelayTransport multiplexes local relay streams over the currently
 // registered Agent WebSocket sessions. Agent wire IDs are monotonic and never
-// reused within one authenticated Agent epoch.
+// reused within one process-local authenticated Agent connection incarnation.
 type AgentRelayTransport struct {
 	manager    *AgentSessionManager
 	mu         sync.Mutex
@@ -32,8 +32,8 @@
 }
 
 type agentRelayGeneration struct {
-	agentID string
-	epoch   int64
+	agentID          string
+	serverGeneration uint64
 }
 
 type agentRelayStreamKey struct {
@@ -66,7 +66,7 @@
 		t.mu.Unlock()
 		return nil, errAgentRelayClosed
 	}
-	generation := agentRelayGeneration{agentID: request.AgentID, epoch: session.Epoch}
+	generation := agentRelayGeneration{agentID: request.AgentID, serverGeneration: session.serverGeneration}
 	allocator := t.allocators[generation]
 	if allocator == nil {
 		allocator = &agentRelayIDAllocator{}
@@ -77,10 +77,10 @@
 		return nil, errAgentRelayWireIDsExhausted
 	}
 	allocator.next++
-	stream := &agentRelayStream{transport: t, agentID: request.AgentID, epoch: session.Epoch, wireID: uint32(allocator.next), readCh: make(chan []byte, 16), done: make(chan struct{})}
+	stream := &agentRelayStream{transport: t, agentID: request.AgentID, serverGeneration: session.serverGeneration, wireID: uint32(allocator.next), readCh: make(chan []byte, 16), done: make(chan struct{})}
 	t.streams[stream.key()] = stream
 	t.mu.Unlock()
-	if err := t.manager.SendGeneration(stream.agentID, stream.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: stream.wireID, Payload: payload}); err != nil {
+	if err := t.manager.SendGeneration(stream.agentID, stream.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: stream.wireID, Payload: payload}); err != nil {
 		t.detach(stream)
 		stream.fail(err)
 		return nil, err
@@ -89,9 +89,9 @@
 }
 
 // HandleAgentFrame is the single inbound dispatch point used by the Agent
-// session callback. Epoch fencing prevents stale reconnect frames from being
-// delivered to streams opened on the replacement generation.
-func (t *AgentRelayTransport) HandleAgentFrame(agentID string, epoch int64, frame protocol.Frame) error {
+// session callback. Exact process-local connection fencing prevents stale
+// reconnect frames from being delivered to a replacement with the same Epoch.
+func (t *AgentRelayTransport) HandleAgentFrame(agentID string, serverGeneration uint64, frame protocol.Frame) error {
 	if t == nil || t.manager == nil {
 		return errAgentRelayClosed
 	}
@@ -103,7 +103,7 @@
 	// replacement was registered but before the old session cleanup ran.
 	t.manager.mu.RLock()
 	session := t.manager.sessions[agentID]
-	if session == nil || session.Epoch != epoch {
+	if session == nil || session.serverGeneration != serverGeneration {
 		t.manager.mu.RUnlock()
 		return nil
 	}
@@ -113,10 +113,10 @@
 		t.manager.mu.RUnlock()
 		return nil
 	}
-	key := agentRelayStreamKey{agentRelayGeneration: agentRelayGeneration{agentID: agentID, epoch: epoch}, wireID: frame.StreamID}
+	key := agentRelayStreamKey{agentRelayGeneration: agentRelayGeneration{agentID: agentID, serverGeneration: serverGeneration}, wireID: frame.StreamID}
 	t.mu.Lock()
 	stream := t.streams[key]
-	if stream == nil || stream.agentID != agentID || stream.epoch != epoch {
+	if stream == nil || stream.agentID != agentID || stream.serverGeneration != serverGeneration {
 		t.mu.Unlock()
 		session.mu.RUnlock()
 		t.manager.mu.RUnlock()
@@ -142,20 +142,20 @@
 	session.mu.RUnlock()
 	t.manager.mu.RUnlock()
 	if resetStream {
-		_ = t.manager.SendGeneration(agentID, epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: frame.StreamID})
+		_ = t.manager.SendGeneration(agentID, serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: frame.StreamID})
 	}
 	return nil
 }
 
-func (t *AgentRelayTransport) FailAgentGeneration(agentID string, epoch int64) {
+func (t *AgentRelayTransport) FailAgentGeneration(agentID string, serverGeneration uint64) {
 	if t == nil {
 		return
 	}
 	t.mu.Lock()
 	var failed []*agentRelayStream
-	generation := agentRelayGeneration{agentID: agentID, epoch: epoch}
+	generation := agentRelayGeneration{agentID: agentID, serverGeneration: serverGeneration}
 	for key, stream := range t.streams {
-		if stream.agentID == agentID && stream.epoch == epoch {
+		if stream.agentID == agentID && stream.serverGeneration == serverGeneration {
 			delete(t.streams, key)
 			failed = append(failed, stream)
 		}
@@ -202,23 +202,23 @@
 }
 
 type agentRelayStream struct {
-	transport  *AgentRelayTransport
-	agentID    string
-	epoch      int64
-	wireID     uint32
-	readCh     chan []byte
-	done       chan struct{}
-	doneOnce   sync.Once
-	closeOnce  sync.Once
-	mu         sync.Mutex
-	readBuf    []byte
-	err        error
-	localHalf  bool
-	remoteHalf bool
+	transport        *AgentRelayTransport
+	agentID          string
+	serverGeneration uint64
+	wireID           uint32
+	readCh           chan []byte
+	done             chan struct{}
+	doneOnce         sync.Once
+	closeOnce        sync.Once
+	mu               sync.Mutex
+	readBuf          []byte
+	err              error
+	localHalf        bool
+	remoteHalf       bool
 }
 
 func (s *agentRelayStream) key() agentRelayStreamKey {
-	return agentRelayStreamKey{agentRelayGeneration: agentRelayGeneration{agentID: s.agentID, epoch: s.epoch}, wireID: s.wireID}
+	return agentRelayStreamKey{agentRelayGeneration: agentRelayGeneration{agentID: s.agentID, serverGeneration: s.serverGeneration}, wireID: s.wireID}
 }
 
 func (s *agentRelayStream) Read(buffer []byte) (int, error) {
@@ -275,7 +275,7 @@
 		return 0, err
 	}
 	s.mu.Unlock()
-	if err := s.transport.manager.SendGeneration(s.agentID, s.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.wireID, Payload: append([]byte(nil), payload...)}); err != nil {
+	if err := s.transport.manager.SendGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.wireID, Payload: append([]byte(nil), payload...)}); err != nil {
 		s.transport.detach(s)
 		s.fail(err)
 		return 0, err
@@ -297,7 +297,7 @@
 	s.localHalf = true
 	complete := s.remoteHalf
 	s.mu.Unlock()
-	if err := s.transport.manager.SendGeneration(s.agentID, s.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: s.wireID}); err != nil {
+	if err := s.transport.manager.SendGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: s.wireID}); err != nil {
 		s.transport.detach(s)
 		s.fail(err)
 		return err
@@ -316,7 +316,7 @@
 		terminal := s.err != nil && !errors.Is(s.err, io.EOF)
 		s.mu.Unlock()
 		if !complete && !terminal {
-			_ = s.transport.manager.SendGeneration(s.agentID, s.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: s.wireID})
+			_ = s.transport.manager.SendGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: s.wireID})
 		}
 		s.fail(io.ErrClosedPipe)
 	})
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/agent_relay_transport_test.go	2026-09-06 19:30:16
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport_test.go	2026-09-06 19:52:40
@@ -5,6 +5,7 @@
 	"errors"
 	"io"
 	"math"
+	"sync"
 	"testing"
 	"time"
 
@@ -15,7 +16,8 @@
 func TestAgentRelayTransportAllocatesIndependentWireIDsAndFencesReconnectGeneration(t *testing.T) {
 	manager := NewAgentSessionManager(AgentSessionConfig{})
 	firstTransport := newFakeTransport()
-	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-mux", NodeID: "node-a", Epoch: 1}, firstTransport); err != nil {
+	firstSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-mux", NodeID: "node-a", Epoch: 1}, firstTransport)
+	if err != nil {
 		t.Fatal(err)
 	}
 	mux := NewAgentRelayTransport(manager)
@@ -35,10 +37,11 @@
 	}
 
 	newTransport := newFakeTransport()
-	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-mux", NodeID: "node-b", Epoch: 2}, newTransport); err != nil {
+	newSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-mux", NodeID: "node-b", Epoch: 2}, newTransport)
+	if err != nil {
 		t.Fatal(err)
 	}
-	mux.FailAgentGeneration("agent-mux", 1)
+	mux.FailAgentGeneration("agent-mux", firstSession.serverGeneration)
 	if _, err := first.Read(make([]byte, 1)); !errors.Is(err, relay.ErrNodeDisconnected) {
 		t.Fatalf("old generation Read() error = %v, want ErrNodeDisconnected", err)
 	}
@@ -51,10 +54,10 @@
 		t.Fatal(err)
 	}
 	currentOpen := receiveAgentRelayFrame(t, newTransport)
-	if err := mux.HandleAgentFrame("agent-mux", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("stale-data")}); err != nil {
+	if err := mux.HandleAgentFrame("agent-mux", firstSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("stale-data")}); err != nil {
 		t.Fatal(err)
 	}
-	if err := mux.HandleAgentFrame("agent-mux", 2, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("current-data")}); err != nil {
+	if err := mux.HandleAgentFrame("agent-mux", newSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("current-data")}); err != nil {
 		t.Fatal(err)
 	}
 	buffer := make([]byte, len("current-data"))
@@ -64,10 +67,110 @@
 	_ = current.Close()
 }
 
+func TestAgentRelayTransportSameEpochReplacementFencesDelayedOldCallbacksAndTeardown(t *testing.T) {
+	manager := NewAgentSessionManager(AgentSessionConfig{})
+	oldTransport := newFakeTransport()
+	oldSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-same-epoch", NodeID: "node-old", Epoch: 1}, oldTransport)
+	if err != nil {
+		t.Fatal(err)
+	}
+	mux := NewAgentRelayTransport(manager)
+	defer mux.Close()
+	oldStream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-same-epoch", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	oldOpen := receiveAgentRelayFrame(t, oldTransport)
+	manager.RemoveSession("agent-same-epoch", oldSession)
+
+	newTransport := newFakeTransport()
+	newSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-same-epoch", NodeID: "node-new", Epoch: 1}, newTransport)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if newSession.serverGeneration == oldSession.serverGeneration {
+		t.Fatal("same-epoch replacement reused the old server connection generation")
+	}
+	newStream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-same-epoch", Protocol: "tcp", TargetHost: "10.0.0.9", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	newOpen := receiveAgentRelayFrame(t, newTransport)
+	if oldOpen.StreamID != 1 || newOpen.StreamID != 1 {
+		t.Fatalf("per-connection wire IDs = old %d, new %d; want independent ID 1 allocators", oldOpen.StreamID, newOpen.StreamID)
+	}
+
+	if err := mux.HandleAgentFrame("agent-same-epoch", oldSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: oldOpen.StreamID, Payload: []byte("stale")}); err != nil {
+		t.Fatal(err)
+	}
+	mux.FailAgentGeneration("agent-same-epoch", oldSession.serverGeneration)
+	if _, err := oldStream.Read(make([]byte, 1)); !errors.Is(err, relay.ErrNodeDisconnected) {
+		t.Fatalf("old stream Read() error = %v, want ErrNodeDisconnected", err)
+	}
+	if err := mux.HandleAgentFrame("agent-same-epoch", newSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: newOpen.StreamID, Payload: []byte("current")}); err != nil {
+		t.Fatal(err)
+	}
+	buffer := make([]byte, len("current"))
+	if _, err := io.ReadFull(newStream, buffer); err != nil || string(buffer) != "current" {
+		t.Fatalf("new same-epoch stream data = %q, err = %v", buffer, err)
+	}
+}
+
+func TestAgentRelayTransportConcurrentOldTeardownAndSameEpochRegistration(t *testing.T) {
+	manager := NewAgentSessionManager(AgentSessionConfig{})
+	oldSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-generation-race", NodeID: "node-old", Epoch: 1}, newFakeTransport())
+	if err != nil {
+		t.Fatal(err)
+	}
+	mux := NewAgentRelayTransport(manager)
+	defer mux.Close()
+	manager.RemoveSession("agent-generation-race", oldSession)
+
+	start := make(chan struct{})
+	type registrationResult struct {
+		session   *AgentSession
+		transport *fakeTransport
+		err       error
+	}
+	result := make(chan registrationResult, 1)
+	var teardown sync.WaitGroup
+	teardown.Add(1)
+	go func() {
+		defer teardown.Done()
+		<-start
+		mux.FailAgentGeneration("agent-generation-race", oldSession.serverGeneration)
+	}()
+	go func() {
+		<-start
+		transport := newFakeTransport()
+		session, registerErr := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-generation-race", NodeID: "node-new", Epoch: 1}, transport)
+		result <- registrationResult{session: session, transport: transport, err: registerErr}
+	}()
+	close(start)
+	registered := <-result
+	teardown.Wait()
+	if registered.err != nil {
+		t.Fatal(registered.err)
+	}
+	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-generation-race", Protocol: "tcp", TargetHost: "10.0.0.10", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	open := receiveAgentRelayFrame(t, registered.transport)
+	if err := mux.HandleAgentFrame("agent-generation-race", registered.session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("alive")}); err != nil {
+		t.Fatal(err)
+	}
+	buffer := make([]byte, len("alive"))
+	if _, err := io.ReadFull(stream, buffer); err != nil || string(buffer) != "alive" {
+		t.Fatalf("replacement stream data = %q, err = %v", buffer, err)
+	}
+}
+
 func TestAgentRelayTransportResetOverridesPriorRemoteHalfClose(t *testing.T) {
 	manager := NewAgentSessionManager(AgentSessionConfig{})
 	agentTransport := newFakeTransport()
-	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-reset", NodeID: "node-reset", Epoch: 1}, agentTransport); err != nil {
+	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-reset", NodeID: "node-reset", Epoch: 1}, agentTransport)
+	if err != nil {
 		t.Fatal(err)
 	}
 	mux := NewAgentRelayTransport(manager)
@@ -77,13 +180,13 @@
 		t.Fatal(err)
 	}
 	open := receiveAgentRelayFrame(t, agentTransport)
-	if err := mux.HandleAgentFrame("agent-reset", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
+	if err := mux.HandleAgentFrame("agent-reset", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
 		t.Fatal(err)
 	}
 	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
 		t.Fatalf("Read() after HALF_CLOSE error = %v, want EOF", err)
 	}
-	if err := mux.HandleAgentFrame("agent-reset", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
+	if err := mux.HandleAgentFrame("agent-reset", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
 		t.Fatal(err)
 	}
 	if _, err := stream.Write([]byte("must-not-send")); !errors.Is(err, protocol.ErrStreamReset) {
@@ -94,7 +197,8 @@
 func TestAgentRelayTransportRejectsOldGenerationDataAfterReplacementRegistration(t *testing.T) {
 	manager := NewAgentSessionManager(AgentSessionConfig{})
 	oldTransport := newFakeTransport()
-	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-fence", NodeID: "node-old", Epoch: 1}, oldTransport); err != nil {
+	oldSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-fence", NodeID: "node-old", Epoch: 1}, oldTransport)
+	if err != nil {
 		t.Fatal(err)
 	}
 	mux := NewAgentRelayTransport(manager)
@@ -107,10 +211,10 @@
 	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-fence", NodeID: "node-new", Epoch: 2}, newFakeTransport()); err != nil {
 		t.Fatal(err)
 	}
-	if err := mux.HandleAgentFrame("agent-fence", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("stale-data")}); err != nil {
+	if err := mux.HandleAgentFrame("agent-fence", oldSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("stale-data")}); err != nil {
 		t.Fatal(err)
 	}
-	mux.FailAgentGeneration("agent-fence", 1)
+	mux.FailAgentGeneration("agent-fence", oldSession.serverGeneration)
 	buffer := make([]byte, len("stale-data"))
 	if n, err := stream.Read(buffer); n != 0 || !errors.Is(err, relay.ErrNodeDisconnected) {
 		t.Fatalf("Read() after replacement = (%d, %v), payload %q; want (0, ErrNodeDisconnected)", n, err, buffer[:n])
@@ -120,7 +224,8 @@
 func TestAgentRelayTransportCloseDoesNotEchoRemoteReset(t *testing.T) {
 	manager := NewAgentSessionManager(AgentSessionConfig{})
 	agentTransport := newFakeTransport()
-	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-no-echo", NodeID: "node-no-echo", Epoch: 1}, agentTransport); err != nil {
+	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-no-echo", NodeID: "node-no-echo", Epoch: 1}, agentTransport)
+	if err != nil {
 		t.Fatal(err)
 	}
 	mux := NewAgentRelayTransport(manager)
@@ -130,7 +235,7 @@
 		t.Fatal(err)
 	}
 	open := receiveAgentRelayFrame(t, agentTransport)
-	if err := mux.HandleAgentFrame("agent-no-echo", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
+	if err := mux.HandleAgentFrame("agent-no-echo", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
 		t.Fatal(err)
 	}
 	if err := stream.Close(); err != nil {
@@ -146,12 +251,13 @@
 func TestAgentRelayTransportRejectsWireIDWrapAndResetsForNewEpoch(t *testing.T) {
 	manager := NewAgentSessionManager(AgentSessionConfig{})
 	firstTransport := newFakeTransport()
-	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-exhaust", NodeID: "node-old", Epoch: 1}, firstTransport); err != nil {
+	firstSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-exhaust", NodeID: "node-old", Epoch: 1}, firstTransport)
+	if err != nil {
 		t.Fatal(err)
 	}
 	mux := NewAgentRelayTransport(manager)
 	defer mux.Close()
-	mux.allocators[agentRelayGeneration{agentID: "agent-exhaust", epoch: 1}] = &agentRelayIDAllocator{next: math.MaxUint32 - 1}
+	mux.allocators[agentRelayGeneration{agentID: "agent-exhaust", serverGeneration: firstSession.serverGeneration}] = &agentRelayIDAllocator{next: math.MaxUint32 - 1}
 	last, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-exhaust", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
 	if err != nil {
 		t.Fatal(err)
@@ -167,10 +273,11 @@
 	}
 
 	secondTransport := newFakeTransport()
-	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-exhaust", NodeID: "node-new", Epoch: 2}, secondTransport); err != nil {
+	secondSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-exhaust", NodeID: "node-new", Epoch: 2}, secondTransport)
+	if err != nil {
 		t.Fatal(err)
 	}
-	mux.FailAgentGeneration("agent-exhaust", 1)
+	mux.FailAgentGeneration("agent-exhaust", firstSession.serverGeneration)
 	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-exhaust", Protocol: "tcp", TargetHost: "10.0.0.10", TargetPort: 22})
 	if err != nil {
 		t.Fatal(err)
@@ -178,13 +285,17 @@
 	if frame := receiveAgentRelayFrame(t, secondTransport); frame.StreamID != 1 {
 		t.Fatalf("new epoch wire ID = %d, want 1", frame.StreamID)
 	}
+	if secondSession.serverGeneration == firstSession.serverGeneration {
+		t.Fatal("new epoch reused the old server connection generation")
+	}
 	_ = stream.Close()
 }
 
 func TestAgentRelayTransportRejectsDataAfterRemoteHalfClose(t *testing.T) {
 	manager := NewAgentSessionManager(AgentSessionConfig{})
 	agentTransport := newFakeTransport()
-	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-half-data", NodeID: "node-half-data", Epoch: 1}, agentTransport); err != nil {
+	session, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-half-data", NodeID: "node-half-data", Epoch: 1}, agentTransport)
+	if err != nil {
 		t.Fatal(err)
 	}
 	mux := NewAgentRelayTransport(manager)
@@ -194,10 +305,10 @@
 		t.Fatal(err)
 	}
 	open := receiveAgentRelayFrame(t, agentTransport)
-	if err := mux.HandleAgentFrame("agent-half-data", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
+	if err := mux.HandleAgentFrame("agent-half-data", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
 		t.Fatal(err)
 	}
-	if err := mux.HandleAgentFrame("agent-half-data", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("after-eof")}); err != nil {
+	if err := mux.HandleAgentFrame("agent-half-data", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("after-eof")}); err != nil {
 		t.Fatal(err)
 	}
 	if _, err := stream.Write([]byte("must-not-send")); !errors.Is(err, protocol.ErrInvalidFrame) {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/runtime.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/runtime.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/runtime.go	2026-09-06 19:30:16
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/runtime.go	2026-09-06 19:52:40
@@ -182,9 +182,10 @@
 		_ = transport.Close()
 		return
 	}
-	defer r.LocalAgentRelay.FailAgentGeneration(registration.AgentID, registration.Epoch)
-	_ = ServeAgentSessionWithInitialFrame(ctx, r.AgentSessions, registration, transport, initial, func(frame protocol.Frame) error {
-		return r.LocalAgentRelay.HandleAgentFrame(registration.AgentID, registration.Epoch, frame)
+	_ = serveAgentSession(ctx, r.AgentSessions, registration, transport, &initial, func(session *AgentSession, frame protocol.Frame) error {
+		return r.LocalAgentRelay.HandleAgentFrame(registration.AgentID, session.serverGeneration, frame)
+	}, func(session *AgentSession) {
+		r.LocalAgentRelay.FailAgentGeneration(registration.AgentID, session.serverGeneration)
 	})
 }
 
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/session_manager.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/session_manager.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/session_manager.go	2026-09-06 19:30:16
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/session_manager.go	2026-09-06 19:52:40
@@ -15,11 +15,13 @@
 )
 
 var (
-	ErrEpoch          = errors.New("session: stale epoch")
-	ErrBackpressure   = errors.New("session: writer backpressure")
-	ErrSessionClosed  = errors.New("session: closed")
-	ErrAuthentication = errors.New("session: authentication failed")
-	ErrCapability     = errors.New("session: unsupported capability")
+	ErrEpoch                     = errors.New("session: stale epoch")
+	ErrBackpressure              = errors.New("session: writer backpressure")
+	ErrSessionClosed             = errors.New("session: closed")
+	ErrAuthentication            = errors.New("session: authentication failed")
+	ErrCapability                = errors.New("session: unsupported capability")
+	ErrServerGeneration          = errors.New("session: stale server connection generation")
+	ErrServerGenerationExhausted = errors.New("session: server connection generation exhausted")
 )
 
 // MetadataCallback applies server-side allowlist/policy checks. Returning
@@ -53,6 +55,7 @@
 type AgentSession struct {
 	AgentID, NodeID  string
 	Epoch            int64
+	serverGeneration uint64
 	Capabilities     []string
 	transport        FrameTransport
 	mu               sync.RWMutex
@@ -88,9 +91,10 @@
 }
 
 type AgentSessionManager struct {
-	mu       sync.RWMutex
-	sessions map[string]*AgentSession
-	cfg      AgentSessionConfig
+	mu                   sync.RWMutex
+	sessions             map[string]*AgentSession
+	nextServerGeneration uint64
+	cfg                  AgentSessionConfig
 }
 
 func NewAgentSessionManager(cfg AgentSessionConfig) *AgentSessionManager {
@@ -124,7 +128,6 @@
 		}
 	}
 	s := &AgentSession{AgentID: req.AgentID, NodeID: req.NodeID, Epoch: req.Epoch, registration: req, metadataCallback: m.cfg.MetadataCallback, metadataService: m.cfg.MetadataService, metadataTTL: m.cfg.MetadataTTL, Capabilities: neg, transport: tr, lastHeartbeat: time.Now().UTC(), queue: make(chan protocol.Frame, m.cfg.QueueSize), slots: make(chan struct{}, m.cfg.QueueSize), stop: make(chan struct{}), drain: make(chan chan struct{})}
-	go s.writer()
 	m.mu.Lock()
 	old := m.sessions[req.AgentID]
 	if old != nil && old.Epoch >= req.Epoch {
@@ -132,8 +135,16 @@
 		_ = s.Close()
 		return nil, ErrEpoch
 	}
+	if m.nextServerGeneration == math.MaxUint64 {
+		m.mu.Unlock()
+		_ = s.Close()
+		return nil, ErrServerGenerationExhausted
+	}
+	m.nextServerGeneration++
+	s.serverGeneration = m.nextServerGeneration
 	m.sessions[req.AgentID] = s
 	m.mu.Unlock()
+	go s.writer()
 	if old != nil {
 		_ = old.Close()
 	}
@@ -369,10 +380,10 @@
 	}
 }
 
-// SendGeneration queues a frame only when the expected Agent epoch is still
-// current. Holding the manager read lock fences replacement registration
-// between generation validation and queue admission.
-func (m *AgentSessionManager) SendGeneration(id string, epoch int64, f protocol.Frame) error {
+// SendGeneration queues a frame only when the exact process-local Agent
+// connection incarnation is still current. Protocol Epoch remains a separate
+// persisted business generation and is not sufficient to fence reconnects.
+func (m *AgentSessionManager) SendGeneration(id string, serverGeneration uint64, f protocol.Frame) error {
 	if m == nil {
 		return ErrSessionClosed
 	}
@@ -382,9 +393,9 @@
 		m.mu.RUnlock()
 		return ErrSessionClosed
 	}
-	if s.Epoch != epoch {
+	if s.serverGeneration != serverGeneration {
 		m.mu.RUnlock()
-		return ErrEpoch
+		return ErrServerGeneration
 	}
 	s.mu.RLock()
 	if s.closed || s.closing {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/ws_agent.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/ws_agent.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix2-after/internal/server/ws_agent.go	2026-09-06 19:52:40
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/ws_agent.go	2026-09-06 19:52:40
@@ -69,7 +69,12 @@
 // control frames through the fenced session manager while preserving the
 // existing callback path for stream frames.
 func ServeAgentSession(ctx context.Context, manager *AgentSessionManager, registration AgentRegistration, tr *WSFrameTransport, onFrame func(protocol.Frame) error) error {
-	return serveAgentSession(ctx, manager, registration, tr, nil, onFrame)
+	return serveAgentSession(ctx, manager, registration, tr, nil, func(_ *AgentSession, frame protocol.Frame) error {
+		if onFrame == nil {
+			return nil
+		}
+		return onFrame(frame)
+	}, nil)
 }
 
 // ServeAgentSessionWithInitialFrame is used by WebSocket adapters that must
@@ -77,10 +82,15 @@
 // The frame is processed through the same metadata fencing path as all later
 // frames, then the session remains attached to the transport until EOF.
 func ServeAgentSessionWithInitialFrame(ctx context.Context, manager *AgentSessionManager, registration AgentRegistration, tr *WSFrameTransport, initial protocol.Frame, onFrame func(protocol.Frame) error) error {
-	return serveAgentSession(ctx, manager, registration, tr, &initial, onFrame)
+	return serveAgentSession(ctx, manager, registration, tr, &initial, func(_ *AgentSession, frame protocol.Frame) error {
+		if onFrame == nil {
+			return nil
+		}
+		return onFrame(frame)
+	}, nil)
 }
 
-func serveAgentSession(ctx context.Context, manager *AgentSessionManager, registration AgentRegistration, tr *WSFrameTransport, initial *protocol.Frame, onFrame func(protocol.Frame) error) error {
+func serveAgentSession(ctx context.Context, manager *AgentSessionManager, registration AgentRegistration, tr *WSFrameTransport, initial *protocol.Frame, onFrame func(*AgentSession, protocol.Frame) error, onClose func(*AgentSession)) error {
 	if manager == nil || tr == nil {
 		return ErrSessionClosed
 	}
@@ -88,11 +98,26 @@
 	if err != nil {
 		return err
 	}
-	defer manager.RemoveSession(registration.AgentID, session)
+	defer func() {
+		manager.RemoveSession(registration.AgentID, session)
+		if onClose != nil {
+			onClose(session)
+		}
+	}()
 	handle := func(frame protocol.Frame) error {
+		if frame.Type == protocol.FramePing || frame.Type == protocol.FramePong {
+			// Heartbeats keep both the live session timestamp and unchanged
+			// metadata snapshots fresh. A transient metadata-store failure must
+			// not tear down an otherwise healthy forwarding channel.
+			_ = manager.RefreshMetadataLease(ctx, registration.AgentID, registration.Epoch)
+			if frame.Type == protocol.FramePing {
+				return tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong, Payload: frame.Payload})
+			}
+			return nil
+		}
 		if frame.Type != protocol.FrameAgentHello && frame.Type != protocol.FrameAgentMetadataUpdate {
 			if onFrame != nil {
-				return onFrame(frame)
+				return onFrame(session, frame)
 			}
 			return nil
 		}
```
