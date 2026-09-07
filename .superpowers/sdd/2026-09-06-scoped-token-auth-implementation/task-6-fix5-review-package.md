# Task 6 fix round 5 review package

Fix base: Task 6 fix round 4 reviewed snapshot (HEAD remained `163fe12121d2f839ea4bf4907f55a8e7dd835057`)

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix5-after/internal/server/agent_relay_transport.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix5-after/internal/server/agent_relay_transport_test.go differ
```

## Full fix-only diff

```diff
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix5-after/internal/server/agent_relay_transport.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport.go	2026-09-06 20:13:13
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix5-after/internal/server/agent_relay_transport.go	2026-09-06 20:22:42
@@ -150,6 +150,9 @@
 // HandleAgentFrame preserves the legacy Epoch-based callback API. Ambiguous
 // same-epoch reconnects fail closed rather than guessing a connection.
 func (t *AgentRelayTransport) HandleAgentFrame(agentID string, epoch int64, frame protocol.Frame) error {
+	if t == nil {
+		return errAgentRelayClosed
+	}
 	serverGeneration, err := t.manager.resolveServerGeneration(agentID, epoch)
 	if err != nil {
 		return err
@@ -181,6 +184,9 @@
 // closed for an ambiguous same-epoch reconnect instead of touching the live
 // replacement generation.
 func (t *AgentRelayTransport) FailAgentGeneration(agentID string, epoch int64) {
+	if t == nil {
+		return
+	}
 	serverGeneration, err := t.manager.resolveServerGeneration(agentID, epoch)
 	if err != nil {
 		return
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix5-after/internal/server/agent_relay_transport_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix4-after/internal/server/agent_relay_transport_test.go	2026-09-06 20:13:13
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-fix5-after/internal/server/agent_relay_transport_test.go	2026-09-06 20:22:42
@@ -13,6 +13,17 @@
 	"github.com/tunnelmesh/tunnelmesh/internal/relay"
 )
 
+func TestAgentRelayTransportLegacyWrappersNilReceiverFailClosed(t *testing.T) {
+	var mux *AgentRelayTransport
+
+	if err := mux.HandleAgentFrame("agent", 1, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); !errors.Is(err, errAgentRelayClosed) {
+		t.Fatalf("nil HandleAgentFrame() error = %v, want %v", err, errAgentRelayClosed)
+	}
+
+	// Legacy teardown is best-effort; a nil receiver must remain a safe no-op.
+	mux.FailAgentGeneration("agent", 1)
+}
+
 func TestAgentRelayTransportAllocatesIndependentWireIDsAndFencesReconnectGeneration(t *testing.T) {
 	manager := NewAgentSessionManager(AgentSessionConfig{})
 	firstTransport := newFakeTransport()
```
