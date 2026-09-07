# Task 6 fix round 4 review package

Fix base: Task 6 fix round 3 reviewed snapshot (HEAD remained `163fe12121d2f839ea4bf4907f55a8e7dd835057`)

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/runtime.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/runtime.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/session_manager.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/session_manager.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/session_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/session_test.go differ
```

## Full fix-only diff

```diff
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport.go	2026-09-06 19:52:40
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport.go	2026-09-06 20:13:13
@@ -80,7 +80,7 @@
 	stream := &agentRelayStream{transport: t, agentID: request.AgentID, serverGeneration: session.serverGeneration, wireID: uint32(allocator.next), readCh: make(chan []byte, 16), done: make(chan struct{})}
 	t.streams[stream.key()] = stream
 	t.mu.Unlock()
-	if err := t.manager.SendGeneration(stream.agentID, stream.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: stream.wireID, Payload: payload}); err != nil {
+	if err := t.manager.sendServerGeneration(stream.agentID, stream.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: stream.wireID, Payload: payload}); err != nil {
 		t.detach(stream)
 		stream.fail(err)
 		return nil, err
@@ -91,7 +91,7 @@
 // HandleAgentFrame is the single inbound dispatch point used by the Agent
 // session callback. Exact process-local connection fencing prevents stale
 // reconnect frames from being delivered to a replacement with the same Epoch.
-func (t *AgentRelayTransport) HandleAgentFrame(agentID string, serverGeneration uint64, frame protocol.Frame) error {
+func (t *AgentRelayTransport) handleAgentFrameGeneration(agentID string, serverGeneration uint64, frame protocol.Frame) error {
 	if t == nil || t.manager == nil {
 		return errAgentRelayClosed
 	}
@@ -142,12 +142,22 @@
 	session.mu.RUnlock()
 	t.manager.mu.RUnlock()
 	if resetStream {
-		_ = t.manager.SendGeneration(agentID, serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: frame.StreamID})
+		_ = t.manager.sendServerGeneration(agentID, serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: frame.StreamID})
 	}
 	return nil
 }
 
-func (t *AgentRelayTransport) FailAgentGeneration(agentID string, serverGeneration uint64) {
+// HandleAgentFrame preserves the legacy Epoch-based callback API. Ambiguous
+// same-epoch reconnects fail closed rather than guessing a connection.
+func (t *AgentRelayTransport) HandleAgentFrame(agentID string, epoch int64, frame protocol.Frame) error {
+	serverGeneration, err := t.manager.resolveServerGeneration(agentID, epoch)
+	if err != nil {
+		return err
+	}
+	return t.handleAgentFrameGeneration(agentID, serverGeneration, frame)
+}
+
+func (t *AgentRelayTransport) failAgentGeneration(agentID string, serverGeneration uint64) {
 	if t == nil {
 		return
 	}
@@ -167,6 +177,17 @@
 	}
 }
 
+// FailAgentGeneration preserves the legacy Epoch-based teardown API. It fails
+// closed for an ambiguous same-epoch reconnect instead of touching the live
+// replacement generation.
+func (t *AgentRelayTransport) FailAgentGeneration(agentID string, epoch int64) {
+	serverGeneration, err := t.manager.resolveServerGeneration(agentID, epoch)
+	if err != nil {
+		return
+	}
+	t.failAgentGeneration(agentID, serverGeneration)
+}
+
 func (t *AgentRelayTransport) Close() error {
 	if t == nil {
 		return nil
@@ -275,7 +296,7 @@
 		return 0, err
 	}
 	s.mu.Unlock()
-	if err := s.transport.manager.SendGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.wireID, Payload: append([]byte(nil), payload...)}); err != nil {
+	if err := s.transport.manager.sendServerGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.wireID, Payload: append([]byte(nil), payload...)}); err != nil {
 		s.transport.detach(s)
 		s.fail(err)
 		return 0, err
@@ -297,7 +318,7 @@
 	s.localHalf = true
 	complete := s.remoteHalf
 	s.mu.Unlock()
-	if err := s.transport.manager.SendGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: s.wireID}); err != nil {
+	if err := s.transport.manager.sendServerGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: s.wireID}); err != nil {
 		s.transport.detach(s)
 		s.fail(err)
 		return err
@@ -316,7 +337,7 @@
 		terminal := s.err != nil && !errors.Is(s.err, io.EOF)
 		s.mu.Unlock()
 		if !complete && !terminal {
-			_ = s.transport.manager.SendGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: s.wireID})
+			_ = s.transport.manager.sendServerGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: s.wireID})
 		}
 		s.fail(io.ErrClosedPipe)
 	})
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/agent_relay_transport_test.go	2026-09-06 19:52:40
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport_test.go	2026-09-06 20:13:13
@@ -41,7 +41,7 @@
 	if err != nil {
 		t.Fatal(err)
 	}
-	mux.FailAgentGeneration("agent-mux", firstSession.serverGeneration)
+	mux.failAgentGeneration("agent-mux", firstSession.serverGeneration)
 	if _, err := first.Read(make([]byte, 1)); !errors.Is(err, relay.ErrNodeDisconnected) {
 		t.Fatalf("old generation Read() error = %v, want ErrNodeDisconnected", err)
 	}
@@ -54,10 +54,10 @@
 		t.Fatal(err)
 	}
 	currentOpen := receiveAgentRelayFrame(t, newTransport)
-	if err := mux.HandleAgentFrame("agent-mux", firstSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("stale-data")}); err != nil {
+	if err := mux.handleAgentFrameGeneration("agent-mux", firstSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("stale-data")}); err != nil {
 		t.Fatal(err)
 	}
-	if err := mux.HandleAgentFrame("agent-mux", newSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("current-data")}); err != nil {
+	if err := mux.handleAgentFrameGeneration("agent-mux", newSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: currentOpen.StreamID, Payload: []byte("current-data")}); err != nil {
 		t.Fatal(err)
 	}
 	buffer := make([]byte, len("current-data"))
@@ -99,15 +99,20 @@
 	if oldOpen.StreamID != 1 || newOpen.StreamID != 1 {
 		t.Fatalf("per-connection wire IDs = old %d, new %d; want independent ID 1 allocators", oldOpen.StreamID, newOpen.StreamID)
 	}
+	legacyEpoch := int64(1)
+	if err := mux.HandleAgentFrame("agent-same-epoch", legacyEpoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: newOpen.StreamID, Payload: []byte("legacy-must-not-route")}); !errors.Is(err, ErrServerGeneration) {
+		t.Fatalf("legacy same-epoch callback error = %v, want ErrServerGeneration", err)
+	}
+	mux.FailAgentGeneration("agent-same-epoch", legacyEpoch)
 
-	if err := mux.HandleAgentFrame("agent-same-epoch", oldSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: oldOpen.StreamID, Payload: []byte("stale")}); err != nil {
+	if err := mux.handleAgentFrameGeneration("agent-same-epoch", oldSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: oldOpen.StreamID, Payload: []byte("stale")}); err != nil {
 		t.Fatal(err)
 	}
-	mux.FailAgentGeneration("agent-same-epoch", oldSession.serverGeneration)
+	mux.failAgentGeneration("agent-same-epoch", oldSession.serverGeneration)
 	if _, err := oldStream.Read(make([]byte, 1)); !errors.Is(err, relay.ErrNodeDisconnected) {
 		t.Fatalf("old stream Read() error = %v, want ErrNodeDisconnected", err)
 	}
-	if err := mux.HandleAgentFrame("agent-same-epoch", newSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: newOpen.StreamID, Payload: []byte("current")}); err != nil {
+	if err := mux.handleAgentFrameGeneration("agent-same-epoch", newSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: newOpen.StreamID, Payload: []byte("current")}); err != nil {
 		t.Fatal(err)
 	}
 	buffer := make([]byte, len("current"))
@@ -116,6 +121,29 @@
 	}
 }
 
+func TestAgentRelayTransportLegacyEpochCallbackRoutesUniqueCurrentSession(t *testing.T) {
+	manager := NewAgentSessionManager(AgentSessionConfig{})
+	transport := newFakeTransport()
+	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-legacy-api", NodeID: "node", Epoch: 7}, transport); err != nil {
+		t.Fatal(err)
+	}
+	mux := NewAgentRelayTransport(manager)
+	defer mux.Close()
+	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-legacy-api", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
+	if err != nil {
+		t.Fatal(err)
+	}
+	open := receiveAgentRelayFrame(t, transport)
+	legacyEpoch := int64(7)
+	if err := mux.HandleAgentFrame("agent-legacy-api", legacyEpoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("legacy-data")}); err != nil {
+		t.Fatalf("legacy callback error = %v, want nil", err)
+	}
+	buffer := make([]byte, len("legacy-data"))
+	if _, err := io.ReadFull(stream, buffer); err != nil || string(buffer) != "legacy-data" {
+		t.Fatalf("legacy callback data = %q, err = %v", buffer, err)
+	}
+}
+
 func TestAgentRelayTransportConcurrentOldTeardownAndSameEpochRegistration(t *testing.T) {
 	manager := NewAgentSessionManager(AgentSessionConfig{})
 	oldSession, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-generation-race", NodeID: "node-old", Epoch: 1}, newFakeTransport())
@@ -138,7 +166,7 @@
 	go func() {
 		defer teardown.Done()
 		<-start
-		mux.FailAgentGeneration("agent-generation-race", oldSession.serverGeneration)
+		mux.failAgentGeneration("agent-generation-race", oldSession.serverGeneration)
 	}()
 	go func() {
 		<-start
@@ -157,7 +185,7 @@
 		t.Fatal(err)
 	}
 	open := receiveAgentRelayFrame(t, registered.transport)
-	if err := mux.HandleAgentFrame("agent-generation-race", registered.session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("alive")}); err != nil {
+	if err := mux.handleAgentFrameGeneration("agent-generation-race", registered.session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("alive")}); err != nil {
 		t.Fatal(err)
 	}
 	buffer := make([]byte, len("alive"))
@@ -180,13 +208,13 @@
 		t.Fatal(err)
 	}
 	open := receiveAgentRelayFrame(t, agentTransport)
-	if err := mux.HandleAgentFrame("agent-reset", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
+	if err := mux.handleAgentFrameGeneration("agent-reset", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
 		t.Fatal(err)
 	}
 	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
 		t.Fatalf("Read() after HALF_CLOSE error = %v, want EOF", err)
 	}
-	if err := mux.HandleAgentFrame("agent-reset", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
+	if err := mux.handleAgentFrameGeneration("agent-reset", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
 		t.Fatal(err)
 	}
 	if _, err := stream.Write([]byte("must-not-send")); !errors.Is(err, protocol.ErrStreamReset) {
@@ -211,10 +239,10 @@
 	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-fence", NodeID: "node-new", Epoch: 2}, newFakeTransport()); err != nil {
 		t.Fatal(err)
 	}
-	if err := mux.HandleAgentFrame("agent-fence", oldSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("stale-data")}); err != nil {
+	if err := mux.handleAgentFrameGeneration("agent-fence", oldSession.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("stale-data")}); err != nil {
 		t.Fatal(err)
 	}
-	mux.FailAgentGeneration("agent-fence", oldSession.serverGeneration)
+	mux.failAgentGeneration("agent-fence", oldSession.serverGeneration)
 	buffer := make([]byte, len("stale-data"))
 	if n, err := stream.Read(buffer); n != 0 || !errors.Is(err, relay.ErrNodeDisconnected) {
 		t.Fatalf("Read() after replacement = (%d, %v), payload %q; want (0, ErrNodeDisconnected)", n, err, buffer[:n])
@@ -235,7 +263,7 @@
 		t.Fatal(err)
 	}
 	open := receiveAgentRelayFrame(t, agentTransport)
-	if err := mux.HandleAgentFrame("agent-no-echo", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
+	if err := mux.handleAgentFrameGeneration("agent-no-echo", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: open.StreamID}); err != nil {
 		t.Fatal(err)
 	}
 	if err := stream.Close(); err != nil {
@@ -277,7 +305,7 @@
 	if err != nil {
 		t.Fatal(err)
 	}
-	mux.FailAgentGeneration("agent-exhaust", firstSession.serverGeneration)
+	mux.failAgentGeneration("agent-exhaust", firstSession.serverGeneration)
 	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{AgentID: "agent-exhaust", Protocol: "tcp", TargetHost: "10.0.0.10", TargetPort: 22})
 	if err != nil {
 		t.Fatal(err)
@@ -305,10 +333,10 @@
 		t.Fatal(err)
 	}
 	open := receiveAgentRelayFrame(t, agentTransport)
-	if err := mux.HandleAgentFrame("agent-half-data", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
+	if err := mux.handleAgentFrameGeneration("agent-half-data", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}); err != nil {
 		t.Fatal(err)
 	}
-	if err := mux.HandleAgentFrame("agent-half-data", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("after-eof")}); err != nil {
+	if err := mux.handleAgentFrameGeneration("agent-half-data", session.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("after-eof")}); err != nil {
 		t.Fatal(err)
 	}
 	if _, err := stream.Write([]byte("must-not-send")); !errors.Is(err, protocol.ErrInvalidFrame) {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/runtime.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/runtime.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/runtime.go	2026-09-06 19:52:40
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/runtime.go	2026-09-06 20:13:13
@@ -183,9 +183,9 @@
 		return
 	}
 	_ = serveAgentSession(ctx, r.AgentSessions, registration, transport, &initial, func(session *AgentSession, frame protocol.Frame) error {
-		return r.LocalAgentRelay.HandleAgentFrame(registration.AgentID, session.serverGeneration, frame)
+		return r.LocalAgentRelay.handleAgentFrameGeneration(registration.AgentID, session.serverGeneration, frame)
 	}, func(session *AgentSession) {
-		r.LocalAgentRelay.FailAgentGeneration(registration.AgentID, session.serverGeneration)
+		r.LocalAgentRelay.failAgentGeneration(registration.AgentID, session.serverGeneration)
 	})
 }
 
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/session_manager.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/session_manager.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/session_manager.go	2026-09-06 19:52:40
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/session_manager.go	2026-09-06 20:13:13
@@ -94,6 +94,7 @@
 	mu                   sync.RWMutex
 	sessions             map[string]*AgentSession
 	nextServerGeneration uint64
+	retiredEpochs        map[string]map[int64]struct{}
 	cfg                  AgentSessionConfig
 }
 
@@ -104,7 +105,7 @@
 	if cfg.MetadataTTL <= 0 {
 		cfg.MetadataTTL = 5 * time.Minute
 	}
-	return &AgentSessionManager{sessions: make(map[string]*AgentSession), cfg: cfg}
+	return &AgentSessionManager{sessions: make(map[string]*AgentSession), retiredEpochs: make(map[string]map[int64]struct{}), cfg: cfg}
 }
 func (m *AgentSessionManager) Register(ctx context.Context, req AgentRegistration, tr FrameTransport) (*AgentSession, error) {
 	if m == nil || tr == nil || strings.TrimSpace(req.AgentID) == "" || strings.TrimSpace(req.NodeID) == "" || req.Epoch <= 0 {
@@ -383,7 +384,7 @@
 // SendGeneration queues a frame only when the exact process-local Agent
 // connection incarnation is still current. Protocol Epoch remains a separate
 // persisted business generation and is not sufficient to fence reconnects.
-func (m *AgentSessionManager) SendGeneration(id string, serverGeneration uint64, f protocol.Frame) error {
+func (m *AgentSessionManager) sendServerGeneration(id string, serverGeneration uint64, f protocol.Frame) error {
 	if m == nil {
 		return ErrSessionClosed
 	}
@@ -422,6 +423,37 @@
 		return ErrBackpressure
 	}
 }
+
+// SendGeneration preserves the legacy Epoch-based API. New internal callers
+// must use sendServerGeneration so reconnect incarnations remain exact-fenced.
+func (m *AgentSessionManager) SendGeneration(id string, epoch int64, f protocol.Frame) error {
+	serverGeneration, err := m.resolveServerGeneration(id, epoch)
+	if err != nil {
+		return err
+	}
+	return m.sendServerGeneration(id, serverGeneration, f)
+}
+
+func (m *AgentSessionManager) resolveServerGeneration(id string, epoch int64) (uint64, error) {
+	if m == nil {
+		return 0, ErrSessionClosed
+	}
+	m.mu.RLock()
+	defer m.mu.RUnlock()
+	s := m.sessions[id]
+	if s == nil {
+		return 0, ErrSessionClosed
+	}
+	if s.Epoch != epoch {
+		return 0, ErrEpoch
+	}
+	if epochs := m.retiredEpochs[id]; epochs != nil {
+		if _, ambiguous := epochs[epoch]; ambiguous {
+			return 0, ErrServerGeneration
+		}
+	}
+	return s.serverGeneration, nil
+}
 func (m *AgentSessionManager) GoAway(id string) error {
 	s, ok := m.Get(id)
 	if !ok {
@@ -518,6 +550,10 @@
 		_ = s.Close()
 		m.markMetadataStale(context.Background(), s)
 		delete(m.sessions, id)
+		if m.retiredEpochs[id] == nil {
+			m.retiredEpochs[id] = make(map[int64]struct{})
+		}
+		m.retiredEpochs[id][s.Epoch] = struct{}{}
 	}
 	m.mu.Unlock()
 }
@@ -535,6 +571,12 @@
 			m.markMetadataStale(context.Background(), expected)
 		}
 		delete(m.sessions, id)
+		if expected != nil {
+			if m.retiredEpochs[id] == nil {
+				m.retiredEpochs[id] = make(map[int64]struct{})
+			}
+			m.retiredEpochs[id][expected.Epoch] = struct{}{}
+		}
 	}
 	m.mu.Unlock()
 }
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/session_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/session_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix3-after/internal/server/session_test.go	2026-09-06 19:52:40
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/session_test.go	2026-09-06 20:13:13
@@ -224,7 +224,7 @@
 	if err != nil {
 		t.Fatal(err)
 	}
-	if err := manager.SendGeneration("generation-agent", first.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); !errors.Is(err, ErrServerGeneration) {
+	if err := manager.sendServerGeneration("generation-agent", first.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); !errors.Is(err, ErrServerGeneration) {
 		t.Fatalf("SendGeneration(old same-epoch incarnation) error = %v, want ErrServerGeneration", err)
 	}
 	third, err := manager.Register(context.Background(), AgentRegistration{AgentID: "generation-agent", NodeID: "node-c", Epoch: 2}, newFakeTransport())
```
