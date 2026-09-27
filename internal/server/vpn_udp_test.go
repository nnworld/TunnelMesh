//go:build vpn

package server

import (
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// vpnUDPSend sends one datagram into the pipeline without waiting for anything.
func vpnUDPSend(fixture *vpnGatewayFixture, payload []byte) {
	fixture.gateway.handlePacket(vpnWireUDP(vpnPipePeerIP, vpnPipeServiceIP, 51000, 53, payload))
}

// vpnUDPExchange sends one datagram, waits for the association the gateway dialled
// for it, and returns that stream once the datagram has been written to it.
//
// Waiting for the write rather than only for the dial is what makes the assertions
// that follow deterministic: the stream is opened on its own goroutine, so a test
// that read the stream's recorded writes straight after the dial would be racing
// the writer it is trying to observe.
func vpnUDPExchange(t *testing.T, fixture *vpnGatewayFixture, payload []byte) *fakeAgentStream {
	t.Helper()
	before := fixture.opener.openCount()
	vpnUDPSend(fixture, payload)
	waitForOpener(t, fixture.opener, before+1)
	stream := fixture.opener.lastStream()
	if stream == nil {
		t.Fatal("the opener returned no stream")
	}
	waitForStreamWrites(t, stream, 1)
	return stream
}

// waitForStreamWrites blocks until the stream has received at least want writes.
func waitForStreamWrites(t *testing.T, stream *fakeAgentStream, want int) {
	t.Helper()
	deadline := time.Now().Add(vpnPipeWait)
	for time.Now().Before(deadline) {
		if len(stream.recordedWrites()) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the stream received %d writes, want %d", len(stream.recordedWrites()), want)
}

func TestVPNUDPOpensOneStreamPerTuple(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	vpnUDPExchange(t, fixture, []byte("query"))

	requests := fixture.opener.opened()
	if len(requests) != 1 {
		t.Fatalf("the opener was called %d times, want 1", len(requests))
	}
	request := requests[0]
	if request.Protocol != "udp" {
		t.Errorf("Protocol = %q, want %q", request.Protocol, "udp")
	}
	if request.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want the peer's egress agent", request.AgentID)
	}
	if request.TargetHost != vpnPipeServiceIP {
		t.Errorf("TargetHost = %q, want %q", request.TargetHost, vpnPipeServiceIP)
	}
	if request.TargetPort != 53 {
		t.Errorf("TargetPort = %d, want 53", request.TargetPort)
	}
	if request.NodeID != "server-node-1" {
		t.Errorf("NodeID = %q, want this node", request.NodeID)
	}
}

func TestVPNUDPForwardsTheDatagramAndRelaysTheReply(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	stream := vpnUDPExchange(t, fixture, []byte("query"))

	writes := stream.recordedWrites()
	if len(writes) != 1 {
		t.Fatalf("the stream received %d writes, want 1", len(writes))
	}
	if string(writes[0]) != "query" {
		t.Errorf("the stream received %q, want the datagram payload", writes[0])
	}

	stream.inject <- []byte("answer")
	packet, ok := vpnWaitOutbound(t, fixture.device, vpnPipeWait, func(packet vpnOutPacket) bool {
		return packet.transport() == vpnTransportUDP
	})
	if !ok {
		t.Fatal("no udp packet was emitted towards the peer")
	}
	if got := packet.ip().SourceAddress().String(); got != vpnPipeServiceIP {
		t.Errorf("source = %s, want the internal service address", got)
	}
	if got := packet.ip().DestinationAddress().String(); got != vpnPipePeerIP {
		t.Errorf("destination = %s, want the peer's vpn address", got)
	}
	srcPort, dstPort, payload := vpnOutUDP(t, packet)
	if srcPort != 53 || dstPort != 51000 {
		t.Errorf("ports = %d/%d, want the request's ports swapped", srcPort, dstPort)
	}
	if string(payload) != "answer" {
		t.Errorf("payload = %q, want %q", payload, "answer")
	}
	if len(packet.raw) > fixture.gateway.mtu {
		t.Errorf("the emitted packet is %d bytes, larger than the %d byte mtu", len(packet.raw), fixture.gateway.mtu)
	}
	if got := vpnIPv4HeaderChecksum(packet.raw[:packet.ip().HeaderLength()]); got != 0 {
		t.Errorf("the emitted header checksums to %d, want zero for a correct header", got)
	}
}

func TestVPNUDPReusesOneStreamForTheSameTuple(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	stream := vpnUDPExchange(t, fixture, []byte("first"))
	fixture.gateway.handlePacket(vpnWireUDP(vpnPipePeerIP, vpnPipeServiceIP, 51000, 53, []byte("second")))

	deadline := time.Now().Add(vpnPipeWait)
	for time.Now().Before(deadline) && len(stream.recordedWrites()) < 2 {
		time.Sleep(2 * time.Millisecond)
	}
	if fixture.opener.openCount() != 1 {
		t.Errorf("the opener was called %d times for one tuple, want 1", fixture.opener.openCount())
	}
	writes := stream.recordedWrites()
	if len(writes) != 2 {
		t.Fatalf("the stream received %d writes, want 2", len(writes))
	}
	if string(writes[1]) != "second" {
		t.Errorf("the second write was %q, want %q", writes[1], "second")
	}
}

func TestVPNUDPASecondTupleGetsItsOwnStream(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	vpnUDPExchange(t, fixture, []byte("first"))
	fixture.gateway.handlePacket(vpnWireUDP(vpnPipePeerIP, vpnPipeServiceIP, 51001, 53, []byte("second")))
	waitForOpener(t, fixture.opener, 2)

	if fixture.opener.streamAt(0) == fixture.opener.streamAt(1) {
		t.Error("two different source ports shared one stream")
	}
	if fixture.gateway.flows.countPeer("peer-a") != 2 {
		t.Errorf("the peer holds %d flows, want 2", fixture.gateway.flows.countPeer("peer-a"))
	}
}

func TestVPNUDPDropsAReplyLargerThanTheMTU(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	cfg.MTU = 1280
	fixture := newVPNGatewayFixture(t, cfg, nil)
	fixture.applyPeer(t, nil)

	stream := vpnUDPExchange(t, fixture, []byte("query"))
	stream.inject <- make([]byte, cfg.MTU)

	if packets := vpnDrainOutbound(fixture.device, 300*time.Millisecond); len(packets) != 0 {
		t.Errorf("%d packets were emitted for an oversized reply, want none", len(packets))
	}
	if got := fixture.metrics.countDroppedWith("udp", vpn.ClassOversizeDropped); got != 1 {
		t.Errorf("oversize_dropped = %d, want 1; drops were %v", got, fixture.metrics.droppedRecords())
	}
	// The flow survives one undeliverable reply: a service that sends one large
	// datagram must not cost the peer its whole association.
	if fixture.gateway.flows.countPeer("peer-a") != 1 {
		t.Errorf("the peer holds %d flows after an oversized reply, want 1", fixture.gateway.flows.countPeer("peer-a"))
	}
}

func TestVPNUDPReopensAfterTheStreamEnds(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	stream := vpnUDPExchange(t, fixture, []byte("query"))
	close(stream.inject)

	deadline := time.Now().Add(vpnPipeWait)
	for time.Now().Before(deadline) && fixture.gateway.flows.countPeer("peer-a") > 0 {
		time.Sleep(2 * time.Millisecond)
	}
	if fixture.gateway.flows.countPeer("peer-a") != 0 {
		t.Fatal("the flow was not reclaimed after the stream ended")
	}

	vpnUDPExchange(t, fixture, []byte("again"))
	if fixture.opener.openCount() != 2 {
		t.Errorf("the opener was called %d times, want 2: a closed tuple must be dialled again", fixture.opener.openCount())
	}
}

func TestVPNUDPCountsCapacityFromThePeerRow(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, func(row *storage.VPNPeer) { row.MaxConcurrentFlows = 1 })

	vpnUDPExchange(t, fixture, []byte("first"))
	fixture.gateway.handlePacket(vpnWireUDP(vpnPipePeerIP, vpnPipeServiceIP, 51001, 53, []byte("second")))

	if got := fixture.metrics.countDroppedWith("udp", vpn.ClassCapacityExhausted); got != 1 {
		t.Errorf("capacity_exhausted = %d, want 1; drops were %v", got, fixture.metrics.droppedRecords())
	}
	if fixture.opener.openCount() != 1 {
		t.Errorf("the opener was called %d times, want 1: a refused datagram must not dial", fixture.opener.openCount())
	}
}

func TestVPNUDPClosesTheFlowWhenThePeerIsRevoked(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	stream := vpnUDPExchange(t, fixture, []byte("query"))
	if err := fixture.gateway.RemovePeer("peer-a"); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}

	select {
	case <-stream.closed:
	case <-time.After(vpnPipeWait):
		t.Fatal("revoking a peer left its udp stream open")
	}
	if fixture.gateway.flows.countPeer("peer-a") != 0 {
		t.Errorf("the revoked peer still holds %d flows, want 0", fixture.gateway.flows.countPeer("peer-a"))
	}
	// The tuple is gone with the peer, so a later datagram from a re-issued peer
	// starts a fresh association rather than writing into a closed stream.
	if fixture.gateway.flows.count() != 0 {
		t.Errorf("the gateway holds %d flows after the revocation, want 0", fixture.gateway.flows.count())
	}
}

func TestVPNUDPReportsAnUnavailableEgress(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)
	fixture.gateway.deps.Opener = nil

	fixture.gateway.handlePacket(vpnWireUDP(vpnPipePeerIP, vpnPipeServiceIP, 51000, 53, []byte("query")))

	waitForDropped(t, fixture, "udp", vpn.ClassEgressUnavailable, 1)
	if fixture.gateway.flows.countPeer("peer-a") != 0 {
		t.Errorf("a failed dial left %d flows behind, want 0", fixture.gateway.flows.countPeer("peer-a"))
	}
}

func TestVPNUDPCountsBytesInBothDirections(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	stream := vpnUDPExchange(t, fixture, []byte("query"))
	stream.inject <- []byte("a much longer answer")

	if _, ok := vpnWaitOutbound(t, fixture.device, vpnPipeWait, func(packet vpnOutPacket) bool {
		return packet.transport() == vpnTransportUDP
	}); !ok {
		t.Fatal("no reply was emitted")
	}
	items := fixture.gateway.flows.snapshot("peer-a")
	if len(items) != 1 {
		t.Fatalf("the peer holds %d flows, want 1", len(items))
	}
	if items[0].BytesSent != int64(len("query")) {
		t.Errorf("BytesSent = %d, want %d", items[0].BytesSent, len("query"))
	}
	if items[0].BytesReceived != int64(len("a much longer answer")) {
		t.Errorf("BytesReceived = %d, want %d", items[0].BytesReceived, len("a much longer answer"))
	}
	if items[0].Protocol != "udp" || items[0].Target != vpnPipeServiceIP || items[0].Port != 53 {
		t.Errorf("snapshot = %+v, want a udp flow to %s:53", items[0], vpnPipeServiceIP)
	}
	if records := fixture.metrics.bytesRecords(); len(records) == 0 {
		t.Error("no byte metric was recorded for a relayed datagram")
	}
}

func TestVPNUDPRejectsADatagramLargerThanTheTunnelMTU(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	// One byte of payload past the MTU is enough. The size rule is the policy's,
	// and it runs before a stream is opened, so this asserts the ordering: an
	// undeliverable datagram must cost a counter and not a dial to an agent that
	// could never answer it.
	payload := make([]byte, fixture.gateway.mtu-vpnWireIPv4HeaderMinimum-vpnWireUDPHeader+1)
	fixture.gateway.handlePacket(vpnWireUDP(vpnPipePeerIP, vpnPipeServiceIP, 51000, 53, payload))

	if got := fixture.metrics.countDroppedWith("udp", vpn.ClassOversizeDropped); got != 1 {
		t.Errorf("oversize_dropped = %d, want 1; drops were %v", got, fixture.metrics.droppedRecords())
	}
	if fixture.opener.openCount() != 0 {
		t.Errorf("the opener was called %d times for an oversized datagram, want 0", fixture.opener.openCount())
	}
	if fixture.gateway.flows.count() != 0 {
		t.Errorf("an oversized datagram left %d flows behind, want 0", fixture.gateway.flows.count())
	}
}

func TestVPNUDPIsRefusedAfterTheGatewayCloses(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	if err := fixture.gateway.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fixture.gateway.handlePacket(vpnWireUDP(vpnPipePeerIP, vpnPipeServiceIP, 51000, 53, []byte("query")))

	if fixture.opener.openCount() != 0 {
		t.Errorf("a closed gateway dialled %d streams, want 0", fixture.opener.openCount())
	}
	// A closed gateway holds no peers, so the datagram is refused at the lookup
	// rather than at the dial. What matters is that it never reaches the transport.
	if got := fixture.metrics.countDroppedWith("udp", vpn.ClassPeerUnknown); got != 1 {
		t.Errorf("peer_unknown = %d, want 1; drops were %v", got, fixture.metrics.droppedRecords())
	}
}

// waitForDropped blocks until one class has been counted want times.
func waitForDropped(t *testing.T, fixture *vpnGatewayFixture, protocol string, class vpn.ErrorClass, want int) {
	t.Helper()
	deadline := time.Now().Add(vpnPipeWait)
	for time.Now().Before(deadline) {
		if got := fixture.metrics.countDroppedWith(protocol, class); got >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Errorf("%s/%s was counted %d times, want %d; drops were %v",
		protocol, class, fixture.metrics.countDroppedWith(protocol, class), want, fixture.metrics.droppedRecords())
}
