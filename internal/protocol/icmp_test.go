package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestICMPEchoWireIdentifiersAreStable(t *testing.T) {
	// Both strings are on the wire: the capability travels in the hello and the
	// ack, the protocol value in OPEN_STREAM. Renaming either one silently
	// breaks every already-deployed agent, so they are pinned by a test.
	if protocol.CapabilityStreamICMPEcho != "stream_icmp_echo.v1" {
		t.Fatalf("capability = %q", protocol.CapabilityStreamICMPEcho)
	}
	if protocol.StreamProtocolICMPEcho != "icmp-echo" {
		t.Fatalf("protocol = %q", protocol.StreamProtocolICMPEcho)
	}
}

func TestICMPEchoRequestIsOneDatagramPerInFlightEcho(t *testing.T) {
	encoded, err := protocol.EncodeICMPEchoRequest(protocol.ICMPEchoRequest{
		CorrelationID: "echo-1", Identifier: 4242, Sequence: 7, Data: []byte("tunnelmesh"),
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"correlation_id":"echo-1","identifier":4242,"sequence":7,"data":"dHVubmVsbWVzaA=="}`
	if string(encoded) != want {
		t.Fatalf("request = %s, want %s", encoded, want)
	}
	decoded, err := protocol.DecodeICMPEchoRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.CorrelationID != "echo-1" || decoded.Identifier != 4242 || decoded.Sequence != 7 || string(decoded.Data) != "tunnelmesh" {
		t.Fatalf("round trip = %+v", decoded)
	}
}

func TestICMPEchoReplyCarriesTheOriginalIdentifiersAndAStatus(t *testing.T) {
	encoded, err := protocol.EncodeICMPEchoReply(protocol.ICMPEchoReply{
		CorrelationID: "echo-1", Identifier: 4242, Sequence: 7,
		Data: []byte("tunnelmesh"), Status: protocol.ICMPEchoStatusOK, RTTMillis: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := protocol.DecodeICMPEchoReply(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Status != protocol.ICMPEchoStatusOK || decoded.RTTMillis != 12 || decoded.Identifier != 4242 || decoded.Sequence != 7 {
		t.Fatalf("round trip = %+v", decoded)
	}
	// The identifier and sequence a VPN peer sent are what the server has to put
	// back into the reply it forges, so a failure answer must carry them too.
	failed, err := protocol.EncodeICMPEchoReply(protocol.ICMPEchoReply{
		CorrelationID: "echo-2", Identifier: 9, Sequence: 3, Status: protocol.ICMPEchoStatusTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	decodedFailed, err := protocol.DecodeICMPEchoReply(failed)
	if err != nil {
		t.Fatal(err)
	}
	if decodedFailed.Status != protocol.ICMPEchoStatusTimeout || decodedFailed.Identifier != 9 || decodedFailed.Sequence != 3 {
		t.Fatalf("failure round trip = %+v", decodedFailed)
	}
}

func TestICMPEchoMessagesRejectOversizeAndMalformedInput(t *testing.T) {
	tooLarge := protocol.ICMPEchoRequest{CorrelationID: "echo-1", Data: make([]byte, protocol.MaxDatagram)}
	if _, err := protocol.EncodeICMPEchoRequest(tooLarge); err != protocol.ErrDatagramTooLarge {
		t.Fatalf("encode oversize err = %v", err)
	}
	if _, err := protocol.DecodeICMPEchoRequest(make([]byte, protocol.MaxDatagram+1)); err != protocol.ErrDatagramTooLarge {
		t.Fatalf("decode oversize err = %v", err)
	}
	if _, err := protocol.DecodeICMPEchoRequest([]byte(`{"sequence":1}`)); err == nil {
		t.Fatal("a request without a correlation id cannot be answered")
	}
	if _, err := protocol.DecodeICMPEchoReply(nil); err == nil {
		t.Fatal("empty payload must be rejected")
	}
	if _, err := protocol.DecodeICMPEchoReply([]byte(`{"correlation_id":"echo-1","status":"exploded"}`)); err == nil {
		t.Fatal("an unknown status must be rejected rather than passed to the policy layer")
	}
	if _, err := protocol.DecodeICMPEchoRequest([]byte(`{"correlation_id":"echo-1",`)); err == nil {
		t.Fatal("truncated json must be rejected")
	}
}

func TestICMPEchoIgnoresUnknownFieldsForForwardCompatibility(t *testing.T) {
	// A newer server may add a field; an older agent has to keep working
	// instead of refusing the echo. encoding/json already ignores unknown
	// keys, and this test is what stops a future strict decoder.
	decoded, err := protocol.DecodeICMPEchoRequest([]byte(`{"correlation_id":"echo-9","identifier":1,"sequence":2,"data":"aGk=","future_field":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.CorrelationID != "echo-9" || string(decoded.Data) != "hi" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestICMPEchoStatusesAreAClosedEnumeration(t *testing.T) {
	want := []string{
		protocol.ICMPEchoStatusOK, protocol.ICMPEchoStatusTimeout, protocol.ICMPEchoStatusCapacityExhausted,
		protocol.ICMPEchoStatusUnreachable, protocol.ICMPEchoStatusUnsupported, protocol.ICMPEchoStatusCancelled,
	}
	got := protocol.ICMPEchoStatuses()
	if len(got) != len(want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("statuses[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Phase 6 maps every one of these onto a data-plane error_class label, so a
	// status that is not in the enumeration is a metric that cannot be emitted.
	for _, status := range got {
		if strings.TrimSpace(status) == "" {
			t.Fatalf("empty status in %v", got)
		}
	}
	if !contains(protocol.ICMPEchoStatuses(), protocol.ICMPEchoStatusTimeout) {
		t.Fatal("timeout must be part of the enumeration")
	}
	encoded, err := json.Marshal(protocol.ICMPEchoStatuses())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "capacity_exhausted") {
		t.Fatalf("statuses json = %s", encoded)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
