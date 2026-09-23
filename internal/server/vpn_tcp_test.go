//go:build vpn

package server

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/header"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

const (
	// vpnTCPServicePort is in the fixture peer's port allowlist, so a case that
	// fails on the port rule would be asserting the policy rather than the relay.
	vpnTCPServicePort = uint16(443)
	// vpnTCPSecondPeerIP is a second tunnel address, used to prove that two peers
	// reaching one service are kept apart.
	vpnTCPSecondPeerIP = "10.64.0.8"
)

// useVPNTCPRelay installs the relay under test in place of the pipeline's spy.
func (f *vpnGatewayFixture) useVPNTCPRelay() *vpnTCPRelay {
	f.gateway.serveTCP = f.gateway.tcp.serve
	return f.gateway.tcp
}

// applyNamedPeer installs a peer with its own identity and egress agent.
func (f *vpnGatewayFixture) applyNamedPeer(t *testing.T, peerID, publicKey, vpnIP, agentID string) vpnPeerEntry {
	t.Helper()
	row := vpnPeerRow(peerID, publicKey, vpnIP)
	row.NodeID = "server-node-1"
	row.AllowedPorts = "53,443,8080"
	row.AgentID = agentID
	if err := f.gateway.peers.ApplyPeer(row); err != nil {
		t.Fatalf("ApplyPeer(%s): %v", peerID, err)
	}
	entry, _, _ := f.gateway.peers.lookupByIP(netip.MustParseAddr(vpnIP))
	return entry
}

// vpnTCPClient is the tunnel side of one connection, driven by hand through the
// whole packet pipeline rather than through a socket.
//
// Driving the pipeline is the point: a test that injected straight into the stack
// would prove netstack works and nothing about the gateway in front of it.
type vpnTCPClient struct {
	t       *testing.T
	fixture *vpnGatewayFixture
	peerIP  string
	port    uint16
	target  uint16
	seq     uint32
	ack     uint32
}

// vpnTCPDial completes a three-way handshake through the pipeline and returns the
// client side of it.
func vpnTCPDial(t *testing.T, fixture *vpnGatewayFixture, peerIP string, target, clientPort uint16) *vpnTCPClient {
	t.Helper()
	client := &vpnTCPClient{
		t: t, fixture: fixture, peerIP: peerIP,
		port: clientPort, target: target, seq: vpnStackClientISN,
	}
	client.inject(vpnStackClientISN, 0, header.TCPFlagSyn, nil)
	reply, ok := vpnWaitOutbound(t, fixture.device, vpnPipeWait, func(candidate vpnOutPacket) bool {
		segment := candidate.tcp()
		return segment != nil && segment.Flags()&header.TCPFlagSyn != 0 && segment.Flags()&header.TCPFlagAck != 0
	})
	if !ok {
		t.Fatalf("the gateway answered no SYN for %s:%d from port %d; drops were %v",
			vpnPipeServiceIP, target, clientPort, fixture.metrics.droppedRecords())
	}
	client.seq = vpnStackClientISN + 1
	client.ack = reply.tcp().SequenceNumber() + 1
	client.inject(client.seq, client.ack, header.TCPFlagAck, nil)
	return client
}

// inject sends one segment through the pipeline.
func (c *vpnTCPClient) inject(seq, ack uint32, flags header.TCPFlags, payload []byte) {
	c.t.Helper()
	packet := vpnTCPPacket(net.ParseIP(c.peerIP), net.ParseIP(vpnPipeServiceIP), c.port, c.target, seq, ack, flags, payload)
	c.fixture.gateway.handlePacket(packet)
}

// send writes payload bytes as the peer would.
func (c *vpnTCPClient) send(payload []byte) {
	c.t.Helper()
	c.inject(c.seq, c.ack, header.TCPFlagAck|header.TCPFlagPsh, payload)
	c.seq += uint32(len(payload))
}

// finish half-closes the connection from the peer's side.
func (c *vpnTCPClient) finish() {
	c.t.Helper()
	c.inject(c.seq, c.ack, header.TCPFlagAck|header.TCPFlagFin, nil)
	c.seq++
}

// finishGracefully half-closes and then acknowledges the gateway's own FIN, which
// is what a real peer does and what lets the stack release the tuple instead of
// lingering in LAST_ACK.
func (c *vpnTCPClient) finishGracefully() {
	c.t.Helper()
	c.finish()
	closed := c.expectSegment("closing", func(segment header.TCP) bool {
		return segment.Flags()&(header.TCPFlagFin|header.TCPFlagRst) != 0
	})
	c.ack = closed.SequenceNumber() + 1
	c.inject(c.seq, c.ack, header.TCPFlagAck, nil)
}

// expectSegment waits for one outbound segment addressed to this client that the
// predicate accepts, discarding the acknowledgements on the way.
func (c *vpnTCPClient) expectSegment(what string, match func(header.TCP) bool) header.TCP {
	c.t.Helper()
	packet, ok := vpnWaitOutbound(c.t, c.fixture.device, vpnPipeWait, func(candidate vpnOutPacket) bool {
		segment := candidate.tcp()
		if segment == nil || segment.DestinationPort() != c.port {
			return false
		}
		return match(segment)
	})
	if !ok {
		c.t.Fatalf("no %s segment reached the peer on port %d; drops were %v", what, c.port, c.fixture.metrics.droppedRecords())
	}
	return packet.tcp()
}

// expectPayload waits for the bytes the internal service sent back.
func (c *vpnTCPClient) expectPayload(want string) {
	c.t.Helper()
	c.expectSegment("data", func(segment header.TCP) bool { return string(segment.Payload()) == want })
}

// expectClosed waits for the gateway to end the connection.
func (c *vpnTCPClient) expectClosed() {
	c.t.Helper()
	c.expectSegment("closing", func(segment header.TCP) bool {
		return segment.Flags()&(header.TCPFlagFin|header.TCPFlagRst) != 0
	})
}

// vpnTCPTarget is the (address, port) pair the stack claims for a service.
func vpnTCPTarget(t *testing.T, port uint16) netip.AddrPort {
	t.Helper()
	return netip.AddrPortFrom(netip.MustParseAddr(vpnPipeServiceIP), port)
}

// waitForListenerRefs blocks until the stack holds want references on a tuple.
func waitForListenerRefs(t *testing.T, fixture *vpnGatewayFixture, target netip.AddrPort, want int) {
	t.Helper()
	deadline := time.Now().Add(vpnPipeWait)
	for time.Now().Before(deadline) {
		if got := fixture.gateway.stack.listenerRefs(target); got == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the stack holds %d references on %s, want %d", fixture.gateway.stack.listenerRefs(target), target, want)
}

// waitForTupleFree blocks until the stack will hand a tuple out again.
//
// A connection that has just closed keeps its port reserved for a moment, and a
// test that reclaims on the controllable clock has not given the stack the real
// time a deployment gives it: server.vpn.idle_timeout is at least two minutes in
// practice, which is longer than the stack's own 60 second TIME_WAIT. Waiting for
// the tuple rather than sleeping a guessed interval is what makes the assertion
// that follows about the gateway and not about the scheduler.
func waitForTupleFree(t *testing.T, fixture *vpnGatewayFixture, target netip.AddrPort) {
	t.Helper()
	deadline := time.Now().Add(vpnPipeWait)
	for time.Now().Before(deadline) {
		if _, err := fixture.gateway.stack.registerTCP(target); err == nil {
			fixture.gateway.stack.releaseTCP(target)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the stack never released %s", target)
}

// waitForStreamClosed blocks until the gateway released an agent stream.
func waitForStreamClosed(t *testing.T, stream *fakeAgentStream) {
	t.Helper()
	select {
	case <-stream.closed:
	case <-time.After(vpnPipeWait):
		t.Fatal("the agent stream was left open")
	}
}

// waitForPeerFlows blocks until a peer holds want flows.
func waitForPeerFlows(t *testing.T, fixture *vpnGatewayFixture, peerID string, want int) {
	t.Helper()
	deadline := time.Now().Add(vpnPipeWait)
	for time.Now().Before(deadline) {
		if got := fixture.gateway.flows.countPeer(peerID); got == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s holds %d flows, want %d", peerID, fixture.gateway.flows.countPeer(peerID), want)
}

func TestVPNTCPOpensAnAgentStreamForAnAcceptedConnection(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.useVPNTCPRelay()
	fixture.applyPeer(t, nil)
	target := vpnTCPTarget(t, vpnTCPServicePort)

	vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41000)
	waitForOpener(t, fixture.opener, 1)

	requests := fixture.opener.opened()
	if len(requests) != 1 {
		t.Fatalf("the opener was called %d times, want 1", len(requests))
	}
	dialled := requests[0]
	if dialled.Protocol != "tcp" {
		t.Errorf("Protocol = %q, want %q", dialled.Protocol, "tcp")
	}
	if dialled.TargetHost != vpnPipeServiceIP {
		t.Errorf("TargetHost = %q, want %q", dialled.TargetHost, vpnPipeServiceIP)
	}
	if dialled.TargetPort != int(vpnTCPServicePort) {
		t.Errorf("TargetPort = %d, want %d", dialled.TargetPort, vpnTCPServicePort)
	}
	if dialled.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want the peer's egress agent", dialled.AgentID)
	}
	if dialled.NodeID != "server-node-1" {
		t.Errorf("NodeID = %q, want this node", dialled.NodeID)
	}

	waitForPeerFlows(t, fixture, "peer-a", 1)
	flows := fixture.gateway.flows.snapshot("peer-a")
	if len(flows) != 1 {
		t.Fatalf("%d flows were reported, want 1", len(flows))
	}
	if flows[0].Protocol != "tcp" || flows[0].Target != vpnPipeServiceIP || flows[0].Port != int(vpnTCPServicePort) {
		t.Errorf("flow = %+v, want a tcp flow to %s:%d", flows[0], vpnPipeServiceIP, vpnTCPServicePort)
	}
	// One reference belongs to the listener the SYN registered and one to the
	// connection that was accepted through it.
	waitForListenerRefs(t, fixture, target, 2)
	if got := fixture.gateway.stack.unmatchedTCP(); got != 0 {
		t.Errorf("%d segments reached the stack's default handler, want 0", got)
	}
	if records := fixture.metrics.droppedRecords(); len(records) != 0 {
		t.Errorf("an allowed connection was counted as dropped: %v", records)
	}
}

func TestVPNTCPCarriesBytesInBothDirections(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.useVPNTCPRelay()
	fixture.applyPeer(t, nil)

	client := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41000)
	waitForOpener(t, fixture.opener, 1)
	stream := fixture.opener.lastStream()

	client.send([]byte("hello"))
	waitForStreamWrites(t, stream, 1)
	if got := string(stream.recordedWrites()[0]); got != "hello" {
		t.Errorf("the agent received %q, want %q", got, "hello")
	}

	stream.inject <- []byte("world")
	client.expectPayload("world")

	waitForPeerFlows(t, fixture, "peer-a", 1)
	flows := fixture.gateway.flows.snapshot("peer-a")
	if len(flows) != 1 {
		t.Fatalf("%d flows were reported, want 1", len(flows))
	}
	if flows[0].BytesSent != 5 || flows[0].BytesReceived != 5 {
		t.Errorf("flow bytes = %d sent / %d received, want 5 / 5", flows[0].BytesSent, flows[0].BytesReceived)
	}
	records := fixture.metrics.bytesRecords()
	var ingress, egress bool
	for _, record := range records {
		ingress = ingress || record == "ingress/tcp/5"
		egress = egress || record == "egress/tcp/5"
	}
	if !ingress || !egress {
		t.Errorf("byte counters = %v, want one ingress/tcp/5 and one egress/tcp/5", records)
	}
}

func TestVPNTCPEndsTheStreamWhenThePeerHalfCloses(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.useVPNTCPRelay()
	fixture.applyPeer(t, nil)
	target := vpnTCPTarget(t, vpnTCPServicePort)

	client := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41000)
	waitForOpener(t, fixture.opener, 1)
	stream := fixture.opener.lastStream()
	waitForListenerRefs(t, fixture, target, 2)

	client.finish()

	waitForStreamClosed(t, stream)
	waitForPeerFlows(t, fixture, "peer-a", 0)
	waitForListenerRefs(t, fixture, target, 1)
}

func TestVPNTCPClosesTheConnectionWhenTheAgentEndsTheStream(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.useVPNTCPRelay()
	fixture.applyPeer(t, nil)

	client := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41000)
	waitForOpener(t, fixture.opener, 1)
	stream := fixture.opener.lastStream()

	if err := stream.Close(); err != nil {
		t.Fatalf("closing the fake stream: %v", err)
	}

	client.expectClosed()
	waitForPeerFlows(t, fixture, "peer-a", 0)
}

func TestVPNTCPCountsADialThatTimesOut(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	cfg.ConnectTimeout = 60 * time.Millisecond
	fixture := newVPNGatewayFixture(t, cfg, nil)
	fixture.useVPNTCPRelay()
	fixture.applyPeer(t, nil)
	// The agent never accepts. The channel is closed at the end so the abandoned
	// dial can unwind instead of leaking a goroutine the race detector would
	// still be watching when the next test starts.
	fixture.opener.block = make(chan struct{})
	defer close(fixture.opener.block)

	client := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41000)

	waitForDropped(t, fixture, "tcp", vpn.ClassEgressTimeout, 1)
	client.expectClosed()
	waitForPeerFlows(t, fixture, "peer-a", 0)
}

func TestVPNTCPCountsADialTheAgentRefuses(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.useVPNTCPRelay()
	fixture.applyPeer(t, nil)
	fixture.opener.err = errors.New("the agent is not connected to this node")

	client := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41000)

	waitForDropped(t, fixture, "tcp", vpn.ClassEgressUnavailable, 1)
	client.expectClosed()
	waitForPeerFlows(t, fixture, "peer-a", 0)
}

func TestVPNTCPRefusesAConnectionOverThePerPeerCeiling(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.useVPNTCPRelay()
	fixture.applyPeer(t, func(row *storage.VPNPeer) { row.MaxConcurrentFlows = 1 })

	first := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41000)
	_ = first
	waitForOpener(t, fixture.opener, 1)
	waitForPeerFlows(t, fixture, "peer-a", 1)

	second := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41001)

	waitForDropped(t, fixture, "tcp", vpn.ClassCapacityExhausted, 1)
	second.expectClosed()
	if got := fixture.opener.openCount(); got != 1 {
		t.Errorf("the opener was called %d times, want 1: a refused connection must not dial", got)
	}
	waitForPeerFlows(t, fixture, "peer-a", 1)
}

func TestVPNTCPRevocationClosesAnAcceptedConnection(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.useVPNTCPRelay()
	fixture.applyPeer(t, nil)

	client := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41000)
	waitForOpener(t, fixture.opener, 1)
	stream := fixture.opener.lastStream()

	if err := fixture.gateway.RemovePeer("peer-a"); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}

	waitForStreamClosed(t, stream)
	client.expectClosed()
	waitForPeerFlows(t, fixture, "peer-a", 0)
}

func TestVPNTCPReapsAnIdleConnection(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	cfg.IdleTimeout = 120 * time.Millisecond
	fixture := newVPNGatewayFixture(t, cfg, nil)
	fixture.useVPNTCPRelay()
	fixture.applyPeer(t, nil)

	client := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41000)
	waitForOpener(t, fixture.opener, 1)
	stream := fixture.opener.lastStream()

	waitForStreamClosed(t, stream)
	client.expectClosed()
	waitForPeerFlows(t, fixture, "peer-a", 0)
	if got := fixture.metrics.droppedRecords(); len(got) != 0 {
		t.Errorf("an idle connection was counted as a refusal: %v", got)
	}
}

// TestVPNTCPSharesOneListenerBetweenTwoPeers pins the two properties that make
// one listener per internal service safe: the address is claimed once, and each
// accepted connection is attributed back to the peer that sent the SYN.
func TestVPNTCPSharesOneListenerBetweenTwoPeers(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.useVPNTCPRelay()
	fixture.applyPeer(t, nil)
	fixture.applyNamedPeer(t, "peer-b", vpnPeerKeyB, vpnTCPSecondPeerIP, "agent-2")
	target := vpnTCPTarget(t, vpnTCPServicePort)

	first := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41000)
	second := vpnTCPDial(t, fixture, vpnTCPSecondPeerIP, vpnTCPServicePort, 41001)
	waitForOpener(t, fixture.opener, 2)

	// One listener, plus one reference per accepted connection.
	waitForListenerRefs(t, fixture, target, 3)
	waitForPeerFlows(t, fixture, "peer-a", 1)
	waitForPeerFlows(t, fixture, "peer-b", 1)

	agents := map[string]string{}
	for index, request := range fixture.opener.opened() {
		stream := fixture.opener.streamAt(index)
		if stream == nil {
			t.Fatalf("the opener recorded no stream for request %d", index)
		}
		agents[request.AgentID] = ""
		_ = stream
	}
	if len(agents) != 2 || agents["agent-1"] != "" || agents["agent-2"] != "" {
		t.Errorf("the connections were dialled through %v, want one each through agent-1 and agent-2", agents)
	}

	// The bytes each peer sends reach the agent that peer was issued with, which
	// is the reverse lookup being right rather than merely present.
	first.send([]byte("from-a"))
	second.send([]byte("from-b"))
	firstStream, secondStream := fixture.opener.streamAt(0), fixture.opener.streamAt(1)
	if firstStream == nil || secondStream == nil {
		t.Fatal("the opener recorded fewer streams than requests")
	}
	waitForStreamWrites(t, firstStream, 1)
	waitForStreamWrites(t, secondStream, 1)
	got := map[string]string{
		string(firstStream.recordedWrites()[0]):  fixture.opener.opened()[0].AgentID,
		string(secondStream.recordedWrites()[0]): fixture.opener.opened()[1].AgentID,
	}
	if got["from-a"] != "agent-1" || got["from-b"] != "agent-2" {
		t.Errorf("payload to agent = %v, want from-a through agent-1 and from-b through agent-2", got)
	}
	if unmatched := fixture.gateway.stack.unmatchedTCP(); unmatched != 0 {
		t.Errorf("%d segments reached the stack's default handler, want 0", unmatched)
	}
}

// TestVPNTCPRefusesASegmentNobodyIsServing covers the segment that arrives for a
// connection this gateway has no state for: one that predates a restart, or whose
// flow was already reaped. It is refused here rather than injected, because
// injecting it would make the stack's default handler answer for a decision the
// gateway already made.
func TestVPNTCPRefusesASegmentNobodyIsServing(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.useVPNTCPRelay()
	fixture.applyPeer(t, nil)

	fixture.gateway.handlePacket(vpnTCPPacket(
		net.ParseIP(vpnPipePeerIP), net.ParseIP(vpnPipeServiceIP), 41000, 8080,
		5000, 9000, header.TCPFlagAck|header.TCPFlagPsh, []byte("stale")))

	waitForDropped(t, fixture, "tcp", vpn.ClassEgressUnavailable, 1)
	if got := fixture.gateway.stack.unmatchedTCP(); got != 0 {
		t.Errorf("%d segments reached the stack's default handler, want 0", got)
	}
	if got := fixture.gateway.stack.listenerRefs(vpnTCPTarget(t, 8080)); got != 0 {
		t.Errorf("a listener was claimed for a segment that was refused, refs = %d", got)
	}
	if got := fixture.opener.openCount(); got != 0 {
		t.Errorf("the opener was called %d times, want 0", got)
	}
}

// TestVPNTCPReclaimsAnIdleListener proves the registry does not grow for as long
// as the process runs: once the last connection through a tuple is gone and no new
// SYN arrives, the address is given back to the stack.
func TestVPNTCPReclaimsAnIdleListener(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	relay := fixture.useVPNTCPRelay()
	fixture.applyPeer(t, nil)
	target := vpnTCPTarget(t, vpnTCPServicePort)

	client := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41000)
	waitForOpener(t, fixture.opener, 1)
	waitForListenerRefs(t, fixture, target, 2)

	if reaped := relay.reapIdle(); reaped != 0 {
		t.Errorf("reapIdle() = %d while a connection is live, want 0", reaped)
	}

	client.finishGracefully()
	waitForListenerRefs(t, fixture, target, 1)

	// Past the reclaim floor rather than past idle_timeout: a tuple is held for at
	// least as long as the stack holds it for a closing connection, so that
	// re-claiming a port never races the stack's own bookkeeping.
	fixture.clock.Advance(3 * time.Minute)
	if reaped := relay.reapIdle(); reaped != 1 {
		t.Errorf("reapIdle() = %d, want the idle listener reclaimed", reaped)
	}
	if got := fixture.gateway.stack.listenerRefs(target); got != 0 {
		t.Errorf("the stack still holds %d references on %s, want 0", got, target)
	}
	waitForTupleFree(t, fixture, target)

	// A reclaimed tuple is served again by the next SYN rather than refused.
	again := vpnTCPDial(t, fixture, vpnPipePeerIP, vpnTCPServicePort, 41002)
	_ = again
	waitForOpener(t, fixture.opener, 2)
	waitForListenerRefs(t, fixture, target, 2)
}
