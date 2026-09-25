package agent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// ackFrame builds the frame a Server sends back after a metadata report. The ack is
// the only place where the Agent learns the ceiling the Server will actually enforce,
// so dropping that field is what makes "Agent wants 8, Server allows 4" invisible.
func ackFrame(t *testing.T, payload protocol.AgentMetadataAckPayload) protocol.Frame {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameAgentMetadataAck, Payload: encoded}
}

// captureAgentLogs redirects the default logger for the duration of the test.
func captureAgentLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

// TestPoolHandlerWarnsWhenServerCeilingIsBelowConfiguredPool is the discoverability
// guard: a pool that cannot be satisfied must say so, instead of letting the Agent
// retry connections the Server keeps refusing.
func TestPoolHandlerWarnsWhenServerCeilingIsBelowConfiguredPool(t *testing.T) {
	logs := captureAgentLogs(t)
	h := &connectionPoolHandler{agentID: "agent-devbox", configuredMax: 8}
	if err := h.Handle(ackFrame(t, protocol.AgentMetadataAckPayload{
		AgentID: "agent-devbox", Accepted: true, ConnectionPoolSupported: true, MaxConnectionsPerAgent: 4,
	})); err != nil {
		t.Fatal(err)
	}
	got := logs.String()
	if !strings.Contains(got, "level=WARN") || !strings.Contains(got, "agent connection pool exceeds the server ceiling") {
		t.Fatalf("logs = %q, want a warning naming the mismatched ceiling", got)
	}
	// The numbers, not just the fact: an operator must be able to tell which side to move.
	for _, want := range []string{"configured_max=8", "server_max=4"} {
		if !strings.Contains(got, want) {
			t.Fatalf("logs = %q, want %s", got, want)
		}
	}
	// Repeated acks (metadata reports are periodic) must not spam the log.
	logs.Reset()
	if err := h.Handle(ackFrame(t, protocol.AgentMetadataAckPayload{
		AgentID: "agent-devbox", Accepted: true, ConnectionPoolSupported: true, MaxConnectionsPerAgent: 4,
	})); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "exceeds the server ceiling") {
		t.Fatalf("logs = %q, want the warning emitted once per distinct ceiling", logs.String())
	}
}

// TestPoolHandlerStaysSilentWhenTheCeilingFits pins the negative cases: a ceiling
// that accommodates the pool, and an old Server that reports no ceiling at all (the
// field is omitempty, so 0 must never be read as "no connections allowed").
func TestPoolHandlerStaysSilentWhenTheCeilingFits(t *testing.T) {
	for _, tc := range []struct {
		name          string
		configured    int
		serverCeiling int
	}{
		{name: "ceiling fits", configured: 8, serverCeiling: 16},
		{name: "ceiling equal", configured: 8, serverCeiling: 8},
		{name: "legacy server omits it", configured: 8, serverCeiling: 0},
		{name: "single connection cannot be refused", configured: 1, serverCeiling: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureAgentLogs(t)
			h := &connectionPoolHandler{agentID: "agent-devbox", configuredMax: tc.configured}
			if err := h.Handle(ackFrame(t, protocol.AgentMetadataAckPayload{
				AgentID: "agent-devbox", Accepted: true, ConnectionPoolSupported: true, MaxConnectionsPerAgent: tc.serverCeiling,
			})); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(logs.String(), "exceeds the server ceiling") {
				t.Fatalf("logs = %q, want no ceiling warning", logs.String())
			}
		})
	}
}
