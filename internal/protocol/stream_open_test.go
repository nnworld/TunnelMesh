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

func TestStreamOpenPayloadCarriesUpstreamDomainAndTLSOptions(t *testing.T) {
	payload, err := json.Marshal(protocol.StreamOpenPayload{
		Protocol:      "http",
		TargetHost:    "10.0.0.1",
		TargetPort:    443,
		TargetScheme:  "https",
		HostHeader:    "service.internal.example.com",
		TLSServerName: "service.internal.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"protocol":"http","target_host":"10.0.0.1","target_port":443,"target_scheme":"https","host_header":"service.internal.example.com","tls_server_name":"service.internal.example.com"}`
	if string(payload) != want {
		t.Fatalf("payload = %s, want %s", payload, want)
	}
}
