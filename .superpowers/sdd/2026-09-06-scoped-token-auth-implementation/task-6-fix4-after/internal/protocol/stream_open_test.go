package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestStreamOpenPayloadIsTheSharedWireModel(t *testing.T) {
	payload, err := json.Marshal(protocol.StreamOpenPayload{
		AgentID:    "agent-a",
		Protocol:   "udp",
		TargetHost: "10.0.0.8",
		TargetPort: 5353,
		Metadata:   []byte("dns"),
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"agent_id":"agent-a","protocol":"udp","target_host":"10.0.0.8","target_port":5353,"metadata":"ZG5z"}`
	if string(payload) != want {
		t.Fatalf("payload = %s, want %s", payload, want)
	}
}
