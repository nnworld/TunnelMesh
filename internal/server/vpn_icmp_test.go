//go:build vpn

package server

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/header"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// vpnICMPEchoID is the identifier the fixture peer's ping socket uses. The value
// itself is arbitrary; what the assertions pin is that the gateway restores it
// verbatim, because a peer's stack matches a reply on the (identifier, sequence)
// pair it sent and silently discards anything else. Restoring it is the server's
// job rather than the agent's: an unprivileged ping socket has its identifier
// rewritten by the kernel, so the number that comes back from the internal
// network is not the number the peer sent.
const vpnICMPEchoID = uint16(4242)

// useVPNICMPRelay installs the relay under test in place of the pipeline's spy.
//
// The fixture installs a spy so the dispatch assertions of the pipeline tests can
// run before a relay exists. The relay tests need the real handler and say so
// explicitly, which keeps both sets of assertions honest about which handler they
// exercised.
func (f *vpnGatewayFixture) useVPNICMPRelay() *vpnICMPRelay {
	f.gateway.serveICMP = f.gateway.icmp.serve
	return f.gateway.icmp
}

// vpnICMPEchoSend injects one echo request from the fixture peer into the
// pipeline, exactly as a decrypted tunnel datagram would arrive.
func vpnICMPEchoSend(fixture *vpnGatewayFixture, sequence uint16, payload []byte) {
	fixture.gateway.handlePacket(vpnWireICMP(vpnPipePeerIP, vpnPipeServiceIP, 8, 0, vpnICMPEchoID, sequence, payload))
}

// vpnICMPExchange sends one echo and returns the agent-side stream carrying it,
// after the request has been written to that stream.
//
// Waiting for the write and not only for the dial is what makes the assertions
// that follow deterministic: the echo runs on its own goroutine, so reading the
// stream's recorded writes straight after the dial would race the writer.
func vpnICMPExchange(t *testing.T, fixture *vpnGatewayFixture, sequence uint16, payload []byte) *fakeAgentStream {
	t.Helper()
	before := fixture.opener.openCount()
	vpnICMPEchoSend(fixture, sequence, payload)
	waitForOpener(t, fixture.opener, before+1)
	stream := fixture.opener.lastStream()
	if stream == nil {
		t.Fatal("the opener returned no stream")
	}
	waitForStreamWrites(t, stream, 1)
	return stream
}

// vpnICMPRequest decodes the request the gateway wrote on one stream.
func vpnICMPRequest(t *testing.T, stream *fakeAgentStream) protocol.ICMPEchoRequest {
	t.Helper()
	writes := stream.recordedWrites()
	if len(writes) == 0 {
		t.Fatal("the gateway wrote no echo request")
	}
	request, err := protocol.DecodeICMPEchoRequest(writes[0])
	if err != nil {
		t.Fatalf("DecodeICMPEchoRequest: %v", err)
	}
	return request
}

// vpnICMPAnswer hands one agent reply back to the gateway. The identifiers are
// the request's, which is what a correct agent does.
func vpnICMPAnswer(t *testing.T, stream *fakeAgentStream, request protocol.ICMPEchoRequest, status string, data []byte) {
	t.Helper()
	encoded, err := protocol.EncodeICMPEchoReply(protocol.ICMPEchoReply{
		CorrelationID: request.CorrelationID,
		Identifier:    request.Identifier,
		Sequence:      request.Sequence,
		Data:          data,
		Status:        status,
		RTTMillis:     3,
	})
	if err != nil {
		t.Fatalf("EncodeICMPEchoReply: %v", err)
	}
	stream.inject <- encoded
}

// waitForICMPReply blocks until the device emits an ICMP datagram and returns it.
func waitForICMPReply(t *testing.T, fixture *vpnGatewayFixture) vpnOutPacket {
	t.Helper()
	packet, ok := vpnWaitOutbound(t, fixture.device, vpnPipeWait, func(candidate vpnOutPacket) bool {
		return candidate.transport() == header.ICMPv4ProtocolNumber
	})
	if !ok {
		t.Fatalf("no icmp datagram was emitted; drops were %v", fixture.metrics.droppedRecords())
	}
	return packet
}

// vpnOutICMP decodes the ICMP message of an emitted packet.
func vpnOutICMP(t *testing.T, packet vpnOutPacket) header.ICMPv4 {
	t.Helper()
	offset := int(packet.ip().HeaderLength())
	if len(packet.raw) < offset+header.ICMPv4MinimumSize {
		t.Fatalf("the emitted packet is too short to hold an icmp message: %d bytes", len(packet.raw))
	}
	return header.ICMPv4(packet.raw[offset:])
}

// vpnOutIPv4Checksum recomputes the IP header checksum of an emitted packet with
// its own checksum field zeroed, which is the state RFC 791 requires while the sum
// is taken.
func vpnOutIPv4Checksum(packet vpnOutPacket) uint16 {
	ipHeader := append([]byte(nil), packet.raw[:packet.ip().HeaderLength()]...)
	ipHeader[10] = 0
	ipHeader[11] = 0
	return vpnIPv4HeaderChecksum(ipHeader)
}

// waitForInFlight blocks until the relay holds want echo slots.
func waitForInFlight(t *testing.T, relay *vpnICMPRelay, want int) {
	t.Helper()
	deadline := time.Now().Add(vpnPipeWait)
	for time.Now().Before(deadline) {
		if relay.inFlight() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the relay holds %d in-flight echoes, want %d", relay.inFlight(), want)
}

// streamRecords returns the stream outcomes the gateway published.
func (m *countingVPNMetrics) streamRecords() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.streams...)
}

func TestVPNICMPRelaysAnEchoAndForgesTheReply(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	relay := fixture.useVPNICMPRelay()
	fixture.applyPeer(t, nil)

	stream := vpnICMPExchange(t, fixture, 7, []byte("ping"))

	requests := fixture.opener.opened()
	if len(requests) != 1 {
		t.Fatalf("the opener was called %d times, want 1", len(requests))
	}
	dialled := requests[0]
	if dialled.Protocol != protocol.StreamProtocolICMPEcho {
		t.Errorf("Protocol = %q, want %q", dialled.Protocol, protocol.StreamProtocolICMPEcho)
	}
	if dialled.TargetHost != vpnPipeServiceIP {
		t.Errorf("TargetHost = %q, want %q", dialled.TargetHost, vpnPipeServiceIP)
	}
	if dialled.TargetPort != 0 {
		t.Errorf("TargetPort = %d, want 0: an echo addresses a host, not a port", dialled.TargetPort)
	}
	if dialled.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want the peer's egress agent", dialled.AgentID)
	}

	sent := vpnICMPRequest(t, stream)
	if sent.CorrelationID == "" {
		t.Error("CorrelationID is empty, so a late reply could be matched to the wrong echo")
	}
	if sent.Identifier != vpnICMPEchoID || sent.Sequence != 7 {
		t.Errorf("identifiers = %d/%d, want %d/7", sent.Identifier, sent.Sequence, vpnICMPEchoID)
	}
	if string(sent.Data) != "ping" {
		t.Errorf("Data = %q, want the peer's payload", sent.Data)
	}

	vpnICMPAnswer(t, stream, sent, protocol.ICMPEchoStatusOK, []byte("ping"))
	packet := waitForICMPReply(t, fixture)

	if got := packet.ip().SourceAddress().String(); got != vpnPipeServiceIP {
		t.Errorf("source = %s, want %s: the reply must look like it came from the target", got, vpnPipeServiceIP)
	}
	if got := packet.ip().DestinationAddress().String(); got != vpnPipePeerIP {
		t.Errorf("destination = %s, want %s", got, vpnPipePeerIP)
	}
	if got, want := packet.ip().Checksum(), vpnOutIPv4Checksum(packet); got != want {
		t.Errorf("ip checksum = %#04x, want %#04x", got, want)
	}
	message := vpnOutICMP(t, packet)
	if message.Type() != header.ICMPv4EchoReply {
		t.Errorf("type = %d, want %d (echo reply)", message.Type(), header.ICMPv4EchoReply)
	}
	if message.Code() != 0 {
		t.Errorf("code = %d, want 0", message.Code())
	}
	if message.Ident() != vpnICMPEchoID || message.Sequence() != 7 {
		t.Errorf("identifiers = %d/%d, want the request's %d/7", message.Ident(), message.Sequence(), vpnICMPEchoID)
	}
	if string(message.Payload()) != "ping" {
		t.Errorf("payload = %q, want the echoed bytes", message.Payload())
	}
	if got, want := message.Checksum(), vpnICMPChecksum(message); got != want {
		t.Errorf("icmp checksum = %#04x, want %#04x", got, want)
	}
	if records := fixture.metrics.droppedRecords(); len(records) != 0 {
		t.Errorf("a served echo was counted as dropped: %v", records)
	}
	if got := fixture.metrics.streamRecords(); len(got) == 0 {
		t.Error("no stream outcome was published for a relayed echo")
	}

	waitForInFlight(t, relay, 0)
	if got := fixture.gateway.flows.countPeer("peer-a"); got != 0 {
		t.Errorf("the peer still holds %d flows after its echo was answered, want 0", got)
	}
}

// TestVPNICMPMapsEveryReplyStatusOntoItsClass pins the mapping the protocol
// comments promise: every status an agent can report has a published error_class,
// and a failed echo never produces a datagram.
func TestVPNICMPMapsEveryReplyStatusOntoItsClass(t *testing.T) {
	for _, tc := range []struct {
		status string
		class  vpn.ErrorClass
	}{
		{protocol.ICMPEchoStatusTimeout, vpn.ClassICMPTimeout},
		{protocol.ICMPEchoStatusUnsupported, vpn.ClassICMPUnsupported},
		{protocol.ICMPEchoStatusCapacityExhausted, vpn.ClassCapacityExhausted},
		{protocol.ICMPEchoStatusUnreachable, vpn.ClassEgressUnavailable},
		{protocol.ICMPEchoStatusCancelled, vpn.ClassStackError},
	} {
		t.Run(tc.status, func(t *testing.T) {
			fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
			relay := fixture.useVPNICMPRelay()
			fixture.applyPeer(t, nil)

			stream := vpnICMPExchange(t, fixture, 7, []byte("ping"))
			vpnICMPAnswer(t, stream, vpnICMPRequest(t, stream), tc.status, nil)

			waitForDropped(t, fixture, protocol.StreamProtocolICMPEcho, tc.class, 1)
			if packets := vpnDrainOutbound(fixture.device, 200*time.Millisecond); len(packets) != 0 {
				t.Errorf("%d packets were emitted for a failed echo, want none", len(packets))
			}
			waitForInFlight(t, relay, 0)
			if got := fixture.gateway.flows.countPeer("peer-a"); got != 0 {
				t.Errorf("the peer still holds %d flows after a failed echo, want 0", got)
			}
		})
	}
}

// TestVPNICMPHandlesEveryPublishedStatus is the guard behind the table above: a
// status added to the protocol without a mapping here would otherwise reach the
// pipeline as a label no metric can name.
func TestVPNICMPHandlesEveryPublishedStatus(t *testing.T) {
	handled := map[string]bool{
		protocol.ICMPEchoStatusOK:                true,
		protocol.ICMPEchoStatusTimeout:           true,
		protocol.ICMPEchoStatusUnsupported:       true,
		protocol.ICMPEchoStatusCapacityExhausted: true,
		protocol.ICMPEchoStatusUnreachable:       true,
		protocol.ICMPEchoStatusCancelled:         true,
	}
	for _, status := range protocol.ICMPEchoStatuses() {
		if !handled[status] {
			t.Errorf("protocol status %q has no error_class mapping in the icmp relay", status)
		}
	}
}

func TestVPNICMPRechecksTheAgentCapabilityBeforeDialling(t *testing.T) {
	t.Run("an agent that cannot echo is never dialled", func(t *testing.T) {
		fixture := newVPNGatewayFixture(t, config.VPNConfig{}, func(deps *VPNGatewayDeps) {
			deps.Capabilities = &stubDataPlaneCapabilityProbe{state: CapabilityUnsupported}
		})
		relay := fixture.useVPNICMPRelay()
		fixture.applyPeer(t, nil)

		vpnICMPEchoSend(fixture, 7, []byte("ping"))

		waitForDropped(t, fixture, protocol.StreamProtocolICMPEcho, vpn.ClassICMPUnsupported, 1)
		if got := fixture.opener.openCount(); got != 0 {
			t.Errorf("the opener was called %d times, want 0: dialling an agent without the capability buys one guaranteed failure", got)
		}
		waitForInFlight(t, relay, 0)
	})

	t.Run("an agent this node cannot see is still tried", func(t *testing.T) {
		fixture := newVPNGatewayFixture(t, config.VPNConfig{}, func(deps *VPNGatewayDeps) {
			deps.Capabilities = &stubDataPlaneCapabilityProbe{state: CapabilityUnverified}
		})
		fixture.useVPNICMPRelay()
		fixture.applyPeer(t, nil)

		vpnICMPExchange(t, fixture, 7, []byte("ping"))

		if records := fixture.metrics.droppedRecords(); len(records) != 0 {
			t.Errorf("an unverified capability was treated as a refusal: %v", records)
		}
	})

	t.Run("a probe that fails does not choose the answer", func(t *testing.T) {
		fixture := newVPNGatewayFixture(t, config.VPNConfig{}, func(deps *VPNGatewayDeps) {
			deps.Capabilities = &stubDataPlaneCapabilityProbe{
				state: CapabilityUnverified,
				err:   errors.New("the registry is unreachable"),
			}
		})
		fixture.useVPNICMPRelay()
		fixture.applyPeer(t, nil)

		vpnICMPExchange(t, fixture, 7, []byte("ping"))

		if got := fixture.opener.openCount(); got != 1 {
			t.Errorf("the opener was called %d times, want 1", got)
		}
	})
}

func TestVPNICMPHonoursTheNodeAndPeerSwitches(t *testing.T) {
	t.Run("the node has echo disabled", func(t *testing.T) {
		cfg := enabledVPNTestConfig(t)
		cfg.ICMPEnabled = false
		fixture := newVPNGatewayFixture(t, cfg, nil)
		relay := fixture.useVPNICMPRelay()
		fixture.applyPeer(t, nil)

		vpnICMPEchoSend(fixture, 7, []byte("ping"))

		waitForDropped(t, fixture, protocol.StreamProtocolICMPEcho, vpn.ClassICMPUnsupported, 1)
		if got := fixture.opener.openCount(); got != 0 {
			t.Errorf("the opener was called %d times, want 0", got)
		}
		waitForInFlight(t, relay, 0)
	})

	t.Run("the peer has echo disabled", func(t *testing.T) {
		fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
		relay := fixture.useVPNICMPRelay()
		fixture.applyPeer(t, func(row *storage.VPNPeer) { row.ICMPEnabled = false })

		vpnICMPEchoSend(fixture, 7, []byte("ping"))

		waitForDropped(t, fixture, protocol.StreamProtocolICMPEcho, vpn.ClassICMPUnsupported, 1)
		if got := fixture.opener.openCount(); got != 0 {
			t.Errorf("the opener was called %d times, want 0", got)
		}
		waitForInFlight(t, relay, 0)
	})

	t.Run("an icmp type that is not echo", func(t *testing.T) {
		fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
		fixture.useVPNICMPRelay()
		fixture.applyPeer(t, nil)

		// A TTL-exceeded the peer sent into the tunnel. Relaying it would mean the
		// gateway emitted an ICMP datagram it did not decide to send.
		fixture.gateway.handlePacket(vpnWireICMP(vpnPipePeerIP, vpnPipeServiceIP, 0x0b, 0, 0, 0, nil))

		waitForDropped(t, fixture, protocol.StreamProtocolICMPEcho, vpn.ClassProtocolUnsupported, 1)
		if got := fixture.opener.openCount(); got != 0 {
			t.Errorf("the opener was called %d times, want 0", got)
		}
	})
}

// TestVPNICMPBoundsTheEchoesInFlight pins D16: the node-level semaphore refuses
// rather than queues, and every in-flight echo is a flow the console can see and
// the per-peer budget is charged for.
func TestVPNICMPBoundsTheEchoesInFlight(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	cfg.ICMPMaxConcurrent = 2
	fixture := newVPNGatewayFixture(t, cfg, nil)
	relay := fixture.useVPNICMPRelay()
	fixture.applyPeer(t, nil)

	vpnICMPEchoSend(fixture, 1, []byte("ping"))
	vpnICMPEchoSend(fixture, 2, []byte("ping"))
	waitForOpener(t, fixture.opener, 2)
	waitForInFlight(t, relay, 2)

	if got := fixture.gateway.flows.countPeer("peer-a"); got != 2 {
		t.Errorf("the peer holds %d flows, want the 2 echoes in flight", got)
	}
	for _, flow := range fixture.gateway.flows.snapshot("peer-a") {
		if flow.Protocol != protocol.StreamProtocolICMPEcho {
			t.Errorf("flow protocol = %q, want %q", flow.Protocol, protocol.StreamProtocolICMPEcho)
		}
		if flow.Target != vpnPipeServiceIP {
			t.Errorf("flow target = %q, want %q", flow.Target, vpnPipeServiceIP)
		}
		if flow.Port != 0 {
			t.Errorf("flow port = %d, want 0: an echo has no port", flow.Port)
		}
	}

	vpnICMPEchoSend(fixture, 3, []byte("ping"))

	waitForDropped(t, fixture, protocol.StreamProtocolICMPEcho, vpn.ClassCapacityExhausted, 1)
	if got := fixture.opener.openCount(); got != 2 {
		t.Errorf("the opener was called %d times, want 2: a refused echo must not dial", got)
	}
	waitForInFlight(t, relay, 2)
}

func TestVPNICMPChargesThePerPeerFlowBudget(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	relay := fixture.useVPNICMPRelay()
	fixture.applyPeer(t, func(row *storage.VPNPeer) { row.MaxConcurrentFlows = 1 })

	vpnICMPEchoSend(fixture, 1, []byte("ping"))
	waitForOpener(t, fixture.opener, 1)
	vpnICMPEchoSend(fixture, 2, []byte("ping"))

	waitForDropped(t, fixture, protocol.StreamProtocolICMPEcho, vpn.ClassCapacityExhausted, 1)
	if got := fixture.opener.openCount(); got != 1 {
		t.Errorf("the opener was called %d times, want 1", got)
	}
	// The slot the refused echo took is given back, or one refused packet would
	// permanently shrink the node's budget.
	waitForInFlight(t, relay, 0)
}

func TestVPNICMPTimesOutAndReleasesItsSlot(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	cfg.ICMPTimeout = 60 * time.Millisecond
	cfg.ICMPMaxConcurrent = 1
	fixture := newVPNGatewayFixture(t, cfg, nil)
	relay := fixture.useVPNICMPRelay()
	fixture.applyPeer(t, nil)

	vpnICMPEchoSend(fixture, 1, []byte("ping"))
	waitForOpener(t, fixture.opener, 1)
	stream := fixture.opener.lastStream()

	waitForDropped(t, fixture, protocol.StreamProtocolICMPEcho, vpn.ClassICMPTimeout, 1)
	waitForInFlight(t, relay, 0)
	select {
	case <-stream.closed:
	case <-time.After(vpnPipeWait):
		t.Error("the stream of a timed-out echo was left open, so the agent keeps an echo nobody will read")
	}
	if got := fixture.gateway.flows.countPeer("peer-a"); got != 0 {
		t.Errorf("the peer still holds %d flows after a timeout, want 0", got)
	}

	// The released slot is usable: with a ceiling of one this only succeeds if the
	// timed-out echo really gave its slot back.
	vpnICMPEchoSend(fixture, 2, []byte("ping"))
	waitForOpener(t, fixture.opener, 2)
}

func TestVPNICMPRefusesAReplyForAnotherEcho(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	relay := fixture.useVPNICMPRelay()
	fixture.applyPeer(t, nil)

	stream := vpnICMPExchange(t, fixture, 7, []byte("ping"))
	encoded, err := protocol.EncodeICMPEchoReply(protocol.ICMPEchoReply{
		CorrelationID: "vpnecho-somebody-else",
		Status:        protocol.ICMPEchoStatusOK,
	})
	if err != nil {
		t.Fatalf("EncodeICMPEchoReply: %v", err)
	}
	stream.inject <- encoded

	waitForDropped(t, fixture, protocol.StreamProtocolICMPEcho, vpn.ClassStackError, 1)
	if packets := vpnDrainOutbound(fixture.device, 200*time.Millisecond); len(packets) != 0 {
		t.Errorf("%d packets were emitted for a reply that belongs to another echo, want none", len(packets))
	}
	waitForInFlight(t, relay, 0)
}

func TestVPNICMPCountsAnAnswerlessStream(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	relay := fixture.useVPNICMPRelay()
	fixture.applyPeer(t, nil)

	stream := vpnICMPExchange(t, fixture, 7, []byte("ping"))
	// The agent ended the stream without answering, which is what a relay that
	// could not reach the egress agent looks like from here.
	if err := stream.Close(); err != nil {
		t.Fatalf("closing the fake stream: %v", err)
	}

	waitForDropped(t, fixture, protocol.StreamProtocolICMPEcho, vpn.ClassEgressUnavailable, 1)
	waitForInFlight(t, relay, 0)
}

func TestVPNICMPDropsAReplyThatDoesNotFitTheTunnel(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	fixture := newVPNGatewayFixture(t, cfg, nil)
	relay := fixture.useVPNICMPRelay()
	fixture.applyPeer(t, nil)

	stream := vpnICMPExchange(t, fixture, 7, []byte("ping"))
	vpnICMPAnswer(t, stream, vpnICMPRequest(t, stream), protocol.ICMPEchoStatusOK, make([]byte, cfg.MTU))

	waitForDropped(t, fixture, protocol.StreamProtocolICMPEcho, vpn.ClassOversizeDropped, 1)
	if packets := vpnDrainOutbound(fixture.device, 200*time.Millisecond); len(packets) != 0 {
		t.Errorf("%d packets were emitted for an oversized reply, want none", len(packets))
	}
	waitForInFlight(t, relay, 0)
}

// TestVPNICMPRevocationEndsAnInFlightEcho proves that revoking a peer reaches an
// echo that is already waiting on an agent: the row is committed, the key is about
// to come off the device, and the stream must not outlive either.
func TestVPNICMPRevocationEndsAnInFlightEcho(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	relay := fixture.useVPNICMPRelay()
	fixture.applyPeer(t, nil)

	stream := vpnICMPExchange(t, fixture, 7, []byte("ping"))
	if err := fixture.gateway.RemovePeer("peer-a"); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}

	select {
	case <-stream.closed:
	case <-time.After(vpnPipeWait):
		t.Error("the stream of a revoked peer was left open")
	}
	waitForInFlight(t, relay, 0)
	if got := fixture.gateway.flows.countPeer("peer-a"); got != 0 {
		t.Errorf("a revoked peer still holds %d flows, want 0", got)
	}
}

// TestVPNICMPBuildsAUsableReply checks the encoder on its own, including the two
// shapes a peer's stack is strictest about: the checksum and an odd-length
// payload, which the one's complement sum has to pad rather than skip.
func TestVPNICMPBuildsAUsableReply(t *testing.T) {
	source := netip.MustParseAddr(vpnPipeServiceIP)
	destination := netip.MustParseAddr(vpnPipePeerIP)
	for _, payload := range [][]byte{nil, []byte("x"), []byte("0123456789")} {
		packet := buildIPv4ICMPEchoReply(source, destination, vpnICMPEchoID, 9, payload)
		out := vpnOutPacket{raw: packet}
		if got := out.ip().TransportProtocol(); got != header.ICMPv4ProtocolNumber {
			t.Fatalf("transport protocol = %d, want icmp", got)
		}
		if int(out.ip().TotalLength()) != len(packet) {
			t.Fatalf("total length = %d, want %d", out.ip().TotalLength(), len(packet))
		}
		if got, want := out.ip().Checksum(), vpnOutIPv4Checksum(out); got != want {
			t.Errorf("ip checksum = %#04x, want %#04x", got, want)
		}
		message := vpnOutICMP(t, out)
		if message.Type() != header.ICMPv4EchoReply || message.Code() != 0 {
			t.Errorf("type/code = %d/%d, want 0/0", message.Type(), message.Code())
		}
		if message.Ident() != vpnICMPEchoID || message.Sequence() != 9 {
			t.Errorf("identifiers = %d/%d, want %d/9", message.Ident(), message.Sequence(), vpnICMPEchoID)
		}
		if string(message.Payload()) != string(payload) {
			t.Errorf("payload = %q, want %q", message.Payload(), payload)
		}
		if got, want := message.Checksum(), vpnICMPChecksum(message); got != want {
			t.Errorf("icmp checksum = %#04x, want %#04x for payload %q", got, want, payload)
		}
	}
}
