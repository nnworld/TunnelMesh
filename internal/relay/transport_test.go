package relay

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
)

func TestRelayMetadataPreservesUpstreamDomainAndTLSOptions(t *testing.T) {
	req := StreamRequest{
		NodeID: "node-a", AgentID: "agent-a", StreamID: 7, Protocol: "http",
		TargetConnectionID: "conn-a", TargetConnectionEpoch: 9,
		TargetHost: "10.0.0.1", TargetPort: 443, TargetScheme: "https",
		HostHeader: "service.internal.example.com", TLSServerName: "service.internal.example.com",
	}

	meta, err := structpb.NewStruct(relayStreamMetadata(req))
	if err != nil {
		t.Fatal(err)
	}
	got := streamRequestFromRelayMetadata(meta.GetFields())

	if !reflect.DeepEqual(got, req) {
		t.Fatalf("metadata round trip = %#v, want %#v", got, req)
	}
}
