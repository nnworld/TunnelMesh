package agent

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// The tests below drive the echo stream through the same two entry points the
// server uses: Dialer.dialStreamPayload for the wire itself and
// StreamDispatcher.Handle for the capability gate around it. Nothing here opens a
// real ping socket, because net.ipv4.ping_group_range is not available to the
// test user; the socket is the injected fakeICMPConn from icmp_echo_test.go.

func encodeEchoRequest(t *testing.T, request protocol.ICMPEchoRequest) []byte {
	t.Helper()
	encoded, err := protocol.EncodeICMPEchoRequest(request)
	if err != nil {
		t.Fatalf("encode echo request: %v", err)
	}
	return encoded
}

func echoOpenFrame(t *testing.T, streamID uint32, proto, host string, port int) protocol.Frame {
	t.Helper()
	encoded, err := protocol.EncodeStreamOpenPayload(StreamOpenPayload{Protocol: proto, TargetHost: host, TargetPort: port})
	if err != nil {
		t.Fatalf("encode open payload: %v", err)
	}
	return protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: streamID, Payload: encoded}
}

func echoAckFrame(t *testing.T, capabilities ...string) protocol.Frame {
	t.Helper()
	encoded, err := protocol.EncodeAgentMetadataAckPayload(protocol.AgentMetadataAckPayload{Accepted: true, Capabilities: capabilities})
	if err != nil {
		t.Fatalf("encode ack payload: %v", err)
	}
	return protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameAgentMetadataAck, Payload: encoded}
}

func nextFrame(t *testing.T, frames <-chan protocol.Frame, timeout time.Duration) protocol.Frame {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(timeout):
		t.Fatal("the dispatcher sent no frame")
	}
	return protocol.Frame{}
}

func assertNotDialled(t *testing.T, dialled <-chan string) {
	t.Helper()
	select {
	case proto := <-dialled:
		t.Fatalf("dispatcher dialled %q while the capability was off", proto)
	case <-time.After(50 * time.Millisecond):
	}
}

// recordingDispatcher returns a dispatcher whose dial function reports the
// protocol it was asked for instead of touching the network, which is what makes
// "the gate refused before dialling" distinguishable from "the dial failed".
func recordingDispatcher(frames chan<- protocol.Frame, dialled chan<- string) *StreamDispatcher {
	return NewStreamDispatcherWithConfig(Dialer{}, nil, func(frame protocol.Frame) error {
		frames <- frame
		return nil
	}, DialExecutorConfig{}, func(_ context.Context, payload protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
		dialled <- payload.Protocol
		return nopAgentConn{}, nil
	})
}

func TestDialStreamPayloadRefusesICMPEchoWithoutAnEngine(t *testing.T) {
	dialer := Dialer{}
	stream, err := dialer.dialStreamPayload(context.Background(), StreamOpenPayload{
		Protocol: protocol.StreamProtocolICMPEcho, TargetHost: "10.0.0.5",
	})
	if err == nil {
		_ = stream.Close()
		t.Fatal("dialStreamPayload(icmp-echo) with no engine = nil error, want an explicit refusal")
	}
	if !strings.Contains(err.Error(), "icmp") {
		t.Fatalf("error = %v, want it to name icmp", err)
	}
}

func TestICMPEchoStreamAnswersOneRequestWithOneReply(t *testing.T) {
	conn := newFakeICMPConn()
	echoer := testEchoer(conn, time.Second, 4)
	defer echoer.Close()

	var hooks []string
	dialer := Dialer{ICMPEcho: echoer, Policy: func(_ context.Context, proto, host string, port int) error {
		hooks = append(hooks, proto+"/"+host+":"+strconv.Itoa(port))
		return nil
	}}
	stream, err := dialer.dialStreamPayload(context.Background(), StreamOpenPayload{
		Protocol: protocol.StreamProtocolICMPEcho, TargetHost: "10.0.0.5", TargetPort: 0,
	})
	if err != nil {
		t.Fatalf("dialStreamPayload(icmp-echo) error = %v, want a stream", err)
	}
	defer stream.Close()
	if len(hooks) != 1 || hooks[0] != "icmp/10.0.0.5:0" {
		t.Fatalf("policy hook saw %v, want one call with icmp/10.0.0.5:0", hooks)
	}

	request := protocol.ICMPEchoRequest{CorrelationID: "corr-1", Identifier: 7, Sequence: 9, Data: []byte("tunnelmesh")}
	encoded := encodeEchoRequest(t, request)
	written, err := stream.Write(encoded)
	if err != nil {
		t.Fatalf("Write(request) error = %v, want nil", err)
	}
	if written != len(encoded) {
		t.Fatalf("Write(request) = %d bytes, want %d", written, len(encoded))
	}

	waitForWrite(t, conn, 1)
	sent := parseEcho(t, conn.writes()[0])
	conn.deliverReply(t, 31337, sent.Seq, []byte("tunnelmesh"))

	buffer := make([]byte, protocol.MaxDatagram)
	count, err := stream.Read(buffer)
	if err != nil {
		t.Fatalf("Read(reply) error = %v, want one datagram", err)
	}
	reply, err := protocol.DecodeICMPEchoReply(buffer[:count])
	if err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.CorrelationID != "corr-1" {
		t.Fatalf("reply correlation = %q, want corr-1", reply.CorrelationID)
	}
	if reply.Identifier != 7 || reply.Sequence != 9 {
		t.Fatalf("reply identifiers = %d/%d, want the peer's own 7/9 rather than the wire values", reply.Identifier, reply.Sequence)
	}
	if reply.Status != protocol.ICMPEchoStatusOK {
		t.Fatalf("reply status = %q, want ok", reply.Status)
	}
	if string(reply.Data) != "tunnelmesh" {
		t.Fatalf("reply data = %q, want tunnelmesh", reply.Data)
	}
	if _, err := stream.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("second Read error = %v, want io.EOF after the single reply", err)
	}
}

func TestICMPEchoStreamRefusesTargetsItMustNotEcho(t *testing.T) {
	echoer := testEchoer(newFakeICMPConn(), time.Second, 4)
	defer echoer.Close()

	cases := []struct {
		name   string
		host   string
		denied bool
	}{
		{name: "link-local metadata address", host: "169.254.169.254"},
		{name: "multicast group", host: "224.0.0.1"},
		{name: "unspecified address", host: "0.0.0.0"},
		{name: "hostname instead of a literal address", host: "intranet.example.com"},
		{name: "address the policy hook denies", host: "10.0.0.5", denied: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := Dialer{ICMPEcho: echoer}
			if tc.denied {
				candidate.Policy = func(context.Context, string, string, int) error {
					return errors.New("policy: icmp denied")
				}
			}
			stream, err := candidate.dialStreamPayload(context.Background(), StreamOpenPayload{
				Protocol: protocol.StreamProtocolICMPEcho, TargetHost: tc.host,
			})
			if err == nil {
				_ = stream.Close()
				t.Fatalf("dialStreamPayload(%q) = nil error, want a refusal", tc.host)
			}
		})
	}
}

func TestICMPEchoStreamAcceptsExactlyOneRequest(t *testing.T) {
	conn := newFakeICMPConn()
	echoer := testEchoer(conn, time.Second, 4)
	defer echoer.Close()
	stream, err := Dialer{ICMPEcho: echoer}.dialStreamPayload(context.Background(), StreamOpenPayload{
		Protocol: protocol.StreamProtocolICMPEcho, TargetHost: "10.0.0.5",
	})
	if err != nil {
		t.Fatalf("dialStreamPayload error = %v, want a stream", err)
	}
	defer stream.Close()

	if _, err := stream.Write([]byte("{not json")); err == nil {
		t.Fatal("Write(garbage) = nil error, want a decode failure")
	}
	request := encodeEchoRequest(t, protocol.ICMPEchoRequest{CorrelationID: "corr-2", Identifier: 1, Sequence: 1})
	if _, err := stream.Write(request); err != nil {
		t.Fatalf("Write(request) error = %v, want nil", err)
	}
	if _, err := stream.Write(request); err == nil {
		t.Fatal("second Write = nil error, want a single-request refusal")
	}
}

func TestStreamDispatcherRefusesICMPEchoUntilTheCapabilityIsNegotiated(t *testing.T) {
	frames := make(chan protocol.Frame, 8)
	dialled := make(chan string, 4)
	dispatcher := recordingDispatcher(frames, dialled)
	defer dispatcher.Close()

	if err := dispatcher.Handle(echoOpenFrame(t, 91, protocol.StreamProtocolICMPEcho, "10.0.0.5", 0)); err != nil {
		t.Fatalf("Handle(icmp-echo) error = %v, want a rejection frame instead", err)
	}
	frame := nextFrame(t, frames, time.Second)
	if frame.Type != protocol.FrameReset || frame.StreamID != 91 {
		t.Fatalf("rejection frame = %+v, want RESET for stream 91", frame)
	}
	assertNotDialled(t, dialled)

	dispatcher.SetICMPEchoEnabled(true)
	if err := dispatcher.Handle(echoOpenFrame(t, 92, protocol.StreamProtocolICMPEcho, "10.0.0.5", 0)); err != nil {
		t.Fatalf("Handle(icmp-echo) after enable error = %v, want nil", err)
	}
	select {
	case proto := <-dialled:
		if proto != protocol.StreamProtocolICMPEcho {
			t.Fatalf("dialled protocol = %q, want icmp-echo", proto)
		}
	case <-time.After(time.Second):
		t.Fatal("the stream was not dialled after the capability was enabled")
	}
}

func TestStreamDispatcherKeepsThePortRuleForEveryProtocolButICMP(t *testing.T) {
	frames := make(chan protocol.Frame, 8)
	dialled := make(chan string, 4)
	dispatcher := recordingDispatcher(frames, dialled)
	defer dispatcher.Close()
	dispatcher.SetICMPEchoEnabled(true)

	for _, tc := range []struct {
		name   string
		proto  string
		port   int
		stream uint32
	}{
		{name: "tcp needs a port", proto: "tcp", port: 0, stream: 93},
		{name: "udp needs a port", proto: "udp", port: 0, stream: 94},
		{name: "icmp-echo is bounded by the port range", proto: protocol.StreamProtocolICMPEcho, port: 70000, stream: 95},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := dispatcher.Handle(echoOpenFrame(t, tc.stream, tc.proto, "10.0.0.5", tc.port))
			if err == nil || err.Error() != "agent: invalid stream target" {
				t.Fatalf("Handle(%s port %d) error = %v, want agent: invalid stream target", tc.proto, tc.port, err)
			}
		})
	}

	if err := dispatcher.Handle(echoOpenFrame(t, 96, protocol.StreamProtocolICMPEcho, "10.0.0.5", 0)); err != nil {
		t.Fatalf("Handle(icmp-echo port 0) error = %v, want nil because icmp has no port", err)
	}
	select {
	case proto := <-dialled:
		if proto != protocol.StreamProtocolICMPEcho {
			t.Fatalf("dialled protocol = %q, want icmp-echo", proto)
		}
	case <-time.After(time.Second):
		t.Fatal("port 0 was not accepted for icmp-echo")
	}
}

func TestMetadataAckDrivesBothNegotiatedGates(t *testing.T) {
	frames := make(chan protocol.Frame, 8)
	dialled := make(chan string, 4)
	dispatcher := recordingDispatcher(frames, dialled)
	defer dispatcher.Close()
	handler := &connectionPoolHandler{dispatcher: dispatcher}

	// The strict-open capability alone must not imply the echo capability: the
	// two flags are negotiated independently and the ack loop drives both.
	if err := handler.Handle(echoAckFrame(t, protocol.CapabilityStreamOpenResult)); err != nil {
		t.Fatalf("Handle(ack) error = %v, want nil", err)
	}
	// Strict open results need the flag on the frame as well as the capability
	// in the ack, so the refusal is reported as OPEN_RESULT rather than RESET.
	strict := echoOpenFrame(t, 97, protocol.StreamProtocolICMPEcho, "10.0.0.5", 0)
	strict.Flags = protocol.FlagStrictOpen
	if err := dispatcher.Handle(strict); err != nil {
		t.Fatalf("Handle(icmp-echo) error = %v, want a rejection frame instead", err)
	}
	frame := nextFrame(t, frames, time.Second)
	if frame.Type != protocol.FrameOpenResult || frame.StreamID != 97 {
		t.Fatalf("rejection frame = %+v, want OPEN_RESULT for stream 97", frame)
	}
	result, err := protocol.DecodeOpenResultPayload(frame.Payload)
	if err != nil {
		t.Fatalf("decode open result: %v", err)
	}
	if result.Accepted || result.Code != protocol.OpenResultCodeUnsupportedCapability {
		t.Fatalf("open result = %+v, want refused with unsupported_capability", result)
	}
	assertNotDialled(t, dialled)

	if err := handler.Handle(echoAckFrame(t, protocol.CapabilityStreamOpenResult, protocol.CapabilityStreamICMPEcho)); err != nil {
		t.Fatalf("Handle(ack) error = %v, want nil", err)
	}
	if err := dispatcher.Handle(echoOpenFrame(t, 98, protocol.StreamProtocolICMPEcho, "10.0.0.5", 0)); err != nil {
		t.Fatalf("Handle(icmp-echo) after the ack error = %v, want nil", err)
	}
	select {
	case proto := <-dialled:
		if proto != protocol.StreamProtocolICMPEcho {
			t.Fatalf("dialled protocol = %q, want icmp-echo", proto)
		}
	case <-time.After(time.Second):
		t.Fatal("the acked capability did not open the echo gate")
	}
}
