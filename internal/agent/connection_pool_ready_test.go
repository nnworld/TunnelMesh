package agent

import (
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func readyAckFrame(t *testing.T, ack protocol.AgentMetadataAckPayload) protocol.Frame {
	t.Helper()
	payload, err := protocol.EncodeAgentMetadataAckPayload(ack)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameAgentMetadataAck, Payload: payload}
}

// An accepted ack is the Agent's proof that it is connected and known to the
// Server, which is what a supervisor waits for before trusting a new version.
func TestPoolHandlerReportsReadyOnAnAcceptedAck(t *testing.T) {
	var got []string
	h := &connectionPoolHandler{agentID: "agent-a", onReady: func(serverVersion string) { got = append(got, serverVersion) }}
	if err := h.Handle(readyAckFrame(t, protocol.AgentMetadataAckPayload{AgentID: "agent-a", Accepted: true, ServerVersion: "v1.4.0"})); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "v1.4.0" {
		t.Fatalf("onReady calls = %v, want one call with the Server version", got)
	}
}

func TestPoolHandlerDoesNotReportReadyForARejectedAckOrOtherFrames(t *testing.T) {
	calls := 0
	h := &connectionPoolHandler{agentID: "agent-a", onReady: func(string) { calls++ }}
	rejected := protocol.AgentMetadataAckPayload{AgentID: "agent-a", Accepted: false, Errors: []protocol.AgentMetadataError{{Name: "revision", Code: "invalid_revision"}}}
	if err := h.Handle(readyAckFrame(t, rejected)); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("onReady called %d times, want 0", calls)
	}
}

// A Server that predates server_version still acknowledges the Agent, so the
// Agent is ready; the empty version just means upgrades stay off.
func TestPoolHandlerReportsReadyWithAnEmptyVersionFromAnOlderServer(t *testing.T) {
	var got []string
	h := &connectionPoolHandler{agentID: "agent-a", onReady: func(v string) { got = append(got, v) }}
	if err := h.Handle(readyAckFrame(t, protocol.AgentMetadataAckPayload{AgentID: "agent-a", Accepted: true})); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("onReady calls = %q, want one call with an empty version", got)
	}
}

func TestPoolHandlerWithoutAReadyHookStillWorks(t *testing.T) {
	h := &connectionPoolHandler{agentID: "agent-a"}
	if err := h.Handle(readyAckFrame(t, protocol.AgentMetadataAckPayload{AgentID: "agent-a", Accepted: true})); err != nil {
		t.Fatal(err)
	}
}
