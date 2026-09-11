package protocol_test

import (
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"testing"
)

func TestCapabilityNegotiationIntersectsVersionsAndFeatures(t *testing.T) {
	client := protocol.CapabilityHello{Versions: []uint8{1, 2}, Features: []string{"tcp", "udp", "socks5"}}
	server := protocol.CapabilityHello{Versions: []uint8{2, 3}, Features: []string{"udp", "http_connect"}}
	negotiated, err := protocol.NegotiateCapabilities(client, server)
	if err != nil {
		t.Fatal(err)
	}
	if negotiated.Version != 2 || len(negotiated.Features) != 1 || negotiated.Features[0] != "udp" {
		t.Fatalf("negotiated = %+v", negotiated)
	}
}

func TestCapabilityNegotiationRejectsNoCommonVersion(t *testing.T) {
	_, err := protocol.NegotiateCapabilities(protocol.CapabilityHello{Versions: []uint8{1}}, protocol.CapabilityHello{Versions: []uint8{2}})
	if err != protocol.ErrNoCommonVersion {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamLatencyCapabilities(t *testing.T) {
	if protocol.CapabilityStreamOpenResult != "stream_open_result.v1" {
		t.Fatalf("open result capability = %q", protocol.CapabilityStreamOpenResult)
	}
	if protocol.CapabilityStreamFlowControl != "stream_flow_control.v1" {
		t.Fatalf("flow control capability = %q", protocol.CapabilityStreamFlowControl)
	}
	if protocol.CapabilityStreamFairWriter != "stream_fair_writer.v1" {
		t.Fatalf("fair writer capability = %q", protocol.CapabilityStreamFairWriter)
	}
}
