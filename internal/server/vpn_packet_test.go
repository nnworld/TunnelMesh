//go:build vpn

package server

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/header"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// The addresses follow the documentation convention used by the stack tests:
// 10.64.0.0/16 is the tunnel address pool and 192.168.0.0/16 is the internal
// space a peer is allowed to reach.
const (
	vpnPipePeerIP    = "10.64.0.7"
	vpnPipeSelfIP    = "10.64.0.1"
	vpnPipeServiceIP = "192.168.1.20"
	vpnPipeWait      = 2 * time.Second
)

// vpnTransportUDP is the gvisor protocol number an emitted datagram reports. It
// is spelled out here so the assertions below compare against the stack's own
// constant rather than against a byte the parser happens to have read.
const vpnTransportUDP = header.UDPProtocolNumber

// fakeAgentStream is one relay stream whose two directions a test drives by hand.
type fakeAgentStream struct {
	written chan []byte
	inject  chan []byte
	closed  chan struct{}

	closeOnce sync.Once
	writeMu   sync.Mutex
	writes    [][]byte

	// readMu guards pending, the tail of an injected datagram a short Read did not
	// take. Serving the remainder instead of dropping it is what makes the fake
	// honest about a relay that hands one datagram over in pieces, which is the
	// case the icmp relay's accumulating reader exists for.
	readMu  sync.Mutex
	pending []byte
}

func newFakeAgentStream() *fakeAgentStream {
	return &fakeAgentStream{
		written: make(chan []byte, 64),
		inject:  make(chan []byte, 64),
		closed:  make(chan struct{}),
	}
}

func (s *fakeAgentStream) Read(buffer []byte) (int, error) {
	for {
		s.readMu.Lock()
		if len(s.pending) > 0 {
			n := copy(buffer, s.pending)
			s.pending = s.pending[n:]
			s.readMu.Unlock()
			return n, nil
		}
		s.readMu.Unlock()
		select {
		case payload, ok := <-s.inject:
			if !ok {
				return 0, io.EOF
			}
			s.readMu.Lock()
			s.pending = payload
			s.readMu.Unlock()
		case <-s.closed:
			return 0, io.EOF
		}
	}
}

func (s *fakeAgentStream) Write(payload []byte) (int, error) {
	s.writeMu.Lock()
	s.writes = append(s.writes, append([]byte(nil), payload...))
	s.writeMu.Unlock()
	select {
	case <-s.closed:
		return 0, io.ErrClosedPipe
	default:
	}
	select {
	case s.written <- append([]byte(nil), payload...):
	default:
	}
	return len(payload), nil
}

func (s *fakeAgentStream) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.writeMu.Lock()
		s.writes = nil
		s.writeMu.Unlock()
	})
	return nil
}

func (s *fakeAgentStream) recordedWrites() [][]byte {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return append([][]byte(nil), s.writes...)
}

// fakeAgentOpener is the relay transport the data-plane tests dial through.
type fakeAgentOpener struct {
	mu       sync.Mutex
	requests []relay.StreamRequest
	streams  []*fakeAgentStream
	err      error
	block    chan struct{}
}

func newFakeAgentOpener() *fakeAgentOpener {
	return &fakeAgentOpener{}
}

func (o *fakeAgentOpener) OpenStream(_ context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
	if o.block != nil {
		<-o.block
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests = append(o.requests, request)
	if o.err != nil {
		return nil, o.err
	}
	stream := newFakeAgentStream()
	o.streams = append(o.streams, stream)
	return stream, nil
}

func (o *fakeAgentOpener) Close() error { return nil }

func (o *fakeAgentOpener) opened() []relay.StreamRequest {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]relay.StreamRequest(nil), o.requests...)
}

func (o *fakeAgentOpener) openCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.requests)
}

func (o *fakeAgentOpener) lastStream() *fakeAgentStream {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.streams) == 0 {
		return nil
	}
	return o.streams[len(o.streams)-1]
}

func (o *fakeAgentOpener) streamAt(index int) *fakeAgentStream {
	o.mu.Lock()
	defer o.mu.Unlock()
	if index >= len(o.streams) {
		return nil
	}
	return o.streams[index]
}

// vpnGatewayFixture assembles a gateway that is not serving a WireGuard endpoint.
//
// Everything the packet pipeline touches is real - the memory device, the peer
// table, the flow table, the denial aggregator - and only the two edges a test
// needs to observe are replaced: the relay transport the flows dial through and
// the metrics set the drops are counted in. The TCP and ICMP handlers are spies
// until the tasks that implement them install the real ones, which is what lets
// the pipeline's dispatch be asserted before either protocol exists.
type vpnGatewayFixture struct {
	gateway *vpnGateway
	device  *memoryDevice
	metrics *countingVPNMetrics
	audits  *recordingAuditRepository
	opener  *fakeAgentOpener
	clock   *controllableClock

	mu        sync.Mutex
	tcpCalls  []vpnWirePacket
	icmpCalls []vpnWirePacket
}

func newVPNGatewayFixture(t *testing.T, cfg config.VPNConfig, mutate func(*VPNGatewayDeps)) *vpnGatewayFixture {
	t.Helper()
	if cfg.MTU == 0 {
		cfg = enabledVPNTestConfig(t)
	}
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	identity, err := vpn.ParseNodeIdentity(pair.PrivateKey)
	if err != nil {
		t.Fatalf("ParseNodeIdentity: %v", err)
	}
	fixture := &vpnGatewayFixture{
		metrics: &countingVPNMetrics{},
		audits:  &recordingAuditRepository{},
		opener:  newFakeAgentOpener(),
		clock:   newControllableClock(time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)),
	}
	deps := VPNGatewayDeps{
		Config:       cfg,
		NodeID:       "server-node-1",
		Opener:       fixture.opener,
		Audits:       fixture.audits,
		Metrics:      nil,
		Capabilities: &stubDataPlaneCapabilityProbe{state: CapabilitySupported},
		Now:          fixture.clock.Now,
	}
	if mutate != nil {
		mutate(&deps)
	}
	plane, err := newVPNGateway(context.Background(), deps, identity)
	if err != nil {
		t.Fatalf("newVPNGateway: %v", err)
	}
	gateway, ok := plane.(*vpnGateway)
	if !ok {
		t.Fatalf("newVPNGateway returned %T, want *vpnGateway", plane)
	}
	// The metrics interface is installed directly rather than through deps, which
	// only carries the process-wide *observability.Metrics.
	gateway.metrics = fixture.metrics
	fixture.gateway = gateway
	fixture.device = gateway.device
	gateway.setSelfAddress(netip.MustParseAddr(vpnPipeSelfIP))
	gateway.serveTCP = fixture.spyTCP
	gateway.serveICMP = fixture.spyICMP
	t.Cleanup(func() { _ = gateway.Close() })
	return fixture
}

func (f *vpnGatewayFixture) spyTCP(_ context.Context, _ vpnPeerEntry, parsed vpnWirePacket, _ []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tcpCalls = append(f.tcpCalls, parsed)
}

func (f *vpnGatewayFixture) spyICMP(_ context.Context, _ vpnPeerEntry, parsed vpnWirePacket, _ []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.icmpCalls = append(f.icmpCalls, parsed)
}

func (f *vpnGatewayFixture) tcpCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tcpCalls)
}

// tcpCallsRecorded returns a copy of the packets the TCP handler was handed.
//
// A copy rather than the slice itself because the handler appends to it from the
// device's decryption goroutine, and a test that ranged over the live slice would
// be reading a slice header another goroutine was writing.
func (f *vpnGatewayFixture) tcpCallsRecorded() []vpnWirePacket {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]vpnWirePacket(nil), f.tcpCalls...)
}

func (f *vpnGatewayFixture) icmpCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.icmpCalls)
}

// applyPeer installs one serving peer with the policy a test names.
//
// The port allowlist is widened over the row fixture's because the data-plane
// tests drive DNS-shaped UDP as well as TLS-shaped TCP, and a case that failed on
// the port rule would be asserting the policy rather than the pipeline. A case
// that wants a port refusal says so by narrowing the list itself.
func (f *vpnGatewayFixture) applyPeer(t *testing.T, mutate func(*storage.VPNPeer)) vpnPeerEntry {
	t.Helper()
	row := vpnPeerRow("peer-a", vpnPeerKeyA, vpnPipePeerIP)
	row.NodeID = "server-node-1"
	row.AllowedPorts = "53,443,8080"
	if mutate != nil {
		mutate(&row)
	}
	return f.applyPeerRow(t, row)
}

// applyPeerRow installs one prepared row and returns the entry the peer table
// holds for it.
//
// The entry is returned even when the peer is not serving: a revoked or an
// expired row is still held, and a test that asserts the refusal needs the peer
// ID the audit entry names.
func (f *vpnGatewayFixture) applyPeerRow(t *testing.T, row storage.VPNPeer) vpnPeerEntry {
	t.Helper()
	if err := f.gateway.peers.ApplyPeer(row); err != nil {
		t.Fatalf("ApplyPeer: %v", err)
	}
	entry, _, _ := f.gateway.peers.lookupByIP(netip.MustParseAddr(row.VPNIP))
	return entry
}

// countDroppedWith counts the drops of one protocol and class.
func (m *countingVPNMetrics) countDroppedWith(protocol string, class vpn.ErrorClass) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := protocol + "/" + string(class)
	found := 0
	for _, record := range m.dropped {
		if record == want {
			found++
		}
	}
	return found
}

// bytesRecords returns the byte counters the gateway published.
func (m *countingVPNMetrics) bytesRecords() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.bytes...)
}

// stubDataPlaneCapabilityProbe answers the ICMP capability question without a
// session manager.
type stubDataPlaneCapabilityProbe struct {
	state AgentCapabilityState
	err   error
}

func (p *stubDataPlaneCapabilityProbe) ProbeICMPEcho(context.Context, string) (AgentCapabilityState, error) {
	return p.state, p.err
}

// vpnPipePacket builds one tunnel datagram from the fixture peer to a service.
func vpnPipePacket(protocol uint8, dst string, dstPort uint16, payload []byte) []byte {
	switch protocol {
	case vpnTestProtocolUDP:
		return vpnWireUDP(vpnPipePeerIP, dst, 51000, dstPort, payload)
	case vpnTestProtocolICMP:
		return vpnWireICMP(vpnPipePeerIP, dst, 8, 0, 4242, 7, payload)
	default:
		return vpnWireTCP(vpnPipePeerIP, dst, 41000, dstPort, 0x02, payload)
	}
}

func TestVPNPacketPipelineDispatchesAnAllowedTCPPacket(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil))

	if fixture.tcpCallCount() != 1 {
		t.Fatalf("the tcp handler ran %d times, want 1", fixture.tcpCallCount())
	}
	if records := fixture.metrics.droppedRecords(); len(records) != 0 {
		t.Errorf("an allowed packet was dropped: %v", records)
	}
}

func TestVPNPacketPipelineDispatchesAnAllowedUDPPacket(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolUDP, vpnPipeServiceIP, 53, []byte("query")))
	waitForOpener(t, fixture.opener, 1)

	if packets := vpnDrainOutbound(fixture.device, 200*time.Millisecond); len(packets) != 0 {
		t.Errorf("%d packets were emitted for a datagram nobody answered, want none", len(packets))
	}
	if fixture.opener.openCount() != 1 {
		t.Fatalf("the opener was called %d times, want 1", fixture.opener.openCount())
	}
}

func TestVPNPacketPipelineDispatchesAnAllowedICMPPacket(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolICMP, vpnPipeServiceIP, 0, []byte("ping")))

	if fixture.icmpCallCount() != 1 {
		t.Fatalf("the icmp handler ran %d times, want 1", fixture.icmpCallCount())
	}
}

func TestVPNPacketPipelineCountsEveryDenialClass(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		class    vpn.ErrorClass
		peer     func(*storage.VPNPeer)
		packet   func() []byte
		noPeer   bool
	}{
		{
			name:     "an address nobody issued",
			protocol: "tcp",
			class:    vpn.ClassPeerUnknown,
			noPeer:   true,
			packet:   func() []byte { return vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil) },
		},
		{
			name:     "a revoked peer",
			protocol: "tcp",
			class:    vpn.ClassPeerRevoked,
			peer:     func(row *storage.VPNPeer) { row.Status = storage.VPNPeerStatusRevoked },
			packet:   func() []byte { return vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil) },
		},
		{
			name:     "an expired peer",
			protocol: "tcp",
			class:    vpn.ClassPeerExpired,
			peer: func(row *storage.VPNPeer) {
				past := time.Date(2026, 9, 23, 11, 0, 0, 0, time.UTC)
				row.ExpiresAt = &past
			},
			packet: func() []byte { return vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil) },
		},
		{
			name:     "a fragmented datagram",
			protocol: "tcp",
			class:    vpn.ClassFragmentDropped,
			packet: func() []byte {
				packet := vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil)
				binary.BigEndian.PutUint16(packet[6:8], 0x2000)
				return packet
			},
		},
		{
			name:     "a datagram larger than the tunnel mtu",
			protocol: "tcp",
			class:    vpn.ClassOversizeDropped,
			packet:   func() []byte { return vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, make([]byte, 1500)) },
		},
		{
			name:     "an ip protocol that is not forwarded",
			protocol: "unknown",
			class:    vpn.ClassProtocolUnsupported,
			packet: func() []byte {
				// GRE: a real protocol that the egress whitelist does not carry.
				return vpnIPv4Packet(47, net.ParseIP(vpnPipePeerIP), net.ParseIP(vpnPipeServiceIP), []byte("gre"))
			},
		},
		{
			name:     "the cloud metadata service",
			protocol: "tcp",
			class:    vpn.ClassMetadataDenied,
			packet:   func() []byte { return vpnPipePacket(vpnTestProtocolTCP, "169.254.169.254", 443, nil) },
		},
		{
			name:     "a destination outside the allowlist",
			protocol: "tcp",
			class:    vpn.ClassTargetDenied,
			packet:   func() []byte { return vpnPipePacket(vpnTestProtocolTCP, "172.16.9.9", 443, nil) },
		},
		{
			name:     "the gateway's own vpn address",
			protocol: "tcp",
			class:    vpn.ClassTargetDenied,
			packet:   func() []byte { return vpnPipePacket(vpnTestProtocolTCP, vpnPipeSelfIP, 443, nil) },
		},
		{
			name:     "a private destination the peer may not reach",
			protocol: "tcp",
			class:    vpn.ClassTargetDenied,
			peer:     func(row *storage.VPNPeer) { row.AllowPrivateTargets = false },
			packet:   func() []byte { return vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil) },
		},
		{
			name:     "a port outside the allowlist",
			protocol: "tcp",
			class:    vpn.ClassPortDenied,
			packet:   func() []byte { return vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 22, nil) },
		},
		{
			name:     "a udp port outside the allowlist",
			protocol: "udp",
			class:    vpn.ClassPortDenied,
			packet:   func() []byte { return vpnPipePacket(vpnTestProtocolUDP, vpnPipeServiceIP, 5353, []byte("query")) },
		},
		{
			name:     "icmp echo while the peer has icmp off",
			protocol: "icmp-echo",
			class:    vpn.ClassICMPUnsupported,
			peer:     func(row *storage.VPNPeer) { row.ICMPEnabled = false },
			packet:   func() []byte { return vpnPipePacket(vpnTestProtocolICMP, vpnPipeServiceIP, 0, nil) },
		},
		{
			name:     "an icmp type that is not echo",
			protocol: "icmp-echo",
			class:    vpn.ClassProtocolUnsupported,
			packet: func() []byte {
				return vpnWireICMP(vpnPipePeerIP, vpnPipeServiceIP, 0x0b, 0, 0, 0, nil)
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
			if !testCase.noPeer {
				fixture.applyPeer(t, testCase.peer)
			}
			fixture.gateway.handlePacket(testCase.packet())

			if got := fixture.metrics.countDroppedWith(testCase.protocol, testCase.class); got != 1 {
				t.Errorf("countDroppedWith(%q, %q) = %d, want 1; all drops were %v",
					testCase.protocol, testCase.class, got, fixture.metrics.droppedRecords())
			}
			if fixture.tcpCallCount() != 0 || fixture.icmpCallCount() != 0 {
				t.Error("a denied packet reached a protocol handler")
			}
			if fixture.opener.openCount() != 0 {
				t.Error("a denied packet opened an agent stream")
			}
		})
	}
}

func TestVPNPacketPipelineCountsANonIPv4Datagram(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	fixture.gateway.handlePacket(vpnIPv6VersionOf(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil)))

	if got := fixture.metrics.countDroppedWith("unknown", vpn.ClassProtocolUnsupported); got != 1 {
		t.Errorf("countDroppedWith(unknown, protocol_unsupported) = %d, want 1; drops were %v",
			got, fixture.metrics.droppedRecords())
	}
}

func TestVPNPacketPipelineCountsATruncatedDatagram(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	packet := vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, []byte("payload"))
	fixture.gateway.handlePacket(packet[:24])

	if got := fixture.metrics.countDroppedWith("unknown", vpn.ClassProtocolUnsupported); got != 1 {
		t.Errorf("a truncated datagram was counted %d times, want 1; drops were %v",
			got, fixture.metrics.droppedRecords())
	}
	if fixture.tcpCallCount() != 0 {
		t.Error("a truncated datagram reached the tcp handler")
	}
}

func TestVPNPacketPipelineWritesOneAggregatedAuditEntry(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)

	for range 3 {
		fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 22, nil))
	}
	fixture.gateway.denials.Flush(context.Background())

	entries := fixture.audits.recorded()
	if len(entries) != 1 {
		t.Fatalf("%d audit entries were written for three identical denials, want 1", len(entries))
	}
	if entries[0].Action != vpnPacketDeniedAction {
		t.Errorf("Action = %q, want %q", entries[0].Action, vpnPacketDeniedAction)
	}
	if entries[0].ResourceID != "peer-a" {
		t.Errorf("ResourceID = %q, want the peer that was refused", entries[0].ResourceID)
	}
}

func TestVPNPacketPipelineRateLimitsAPeer(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, func(row *storage.VPNPeer) { row.PacketRateLimit = 2 })

	for range 5 {
		fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil))
	}

	if got := fixture.metrics.countDroppedWith("tcp", vpn.ClassRateLimited); got != 3 {
		t.Errorf("rate_limited was counted %d times, want 3 of 5 packets; drops were %v",
			got, fixture.metrics.droppedRecords())
	}
	if fixture.tcpCallCount() != 2 {
		t.Errorf("the tcp handler ran %d times, want the 2 packets the bucket allowed", fixture.tcpCallCount())
	}

	// The bucket refills from the gateway's clock, so an idle peer recovers its
	// allowance without the gateway having to be told.
	fixture.clock.Advance(2 * time.Second)
	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil))
	if fixture.tcpCallCount() != 3 {
		t.Errorf("the tcp handler ran %d times after the bucket refilled, want 3", fixture.tcpCallCount())
	}
}

func TestVPNPacketPipelineFallsBackToTheGatewayRate(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	cfg.PacketRatePerPeer = 1
	fixture := newVPNGatewayFixture(t, cfg, nil)
	fixture.applyPeer(t, func(row *storage.VPNPeer) { row.PacketRateLimit = 0 })

	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil))
	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil))

	if got := fixture.metrics.countDroppedWith("tcp", vpn.ClassRateLimited); got != 1 {
		t.Errorf("rate_limited = %d, want 1 from the gateway-wide default; drops were %v",
			got, fixture.metrics.droppedRecords())
	}
}

func TestVPNPacketPipelineRebuildsTheBucketWhenTheRateChanges(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, func(row *storage.VPNPeer) { row.PacketRateLimit = 1 })

	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil))
	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil))
	if got := fixture.metrics.countDroppedWith("tcp", vpn.ClassRateLimited); got != 1 {
		t.Fatalf("rate_limited = %d before the change, want 1", got)
	}

	// A management write raises the peer's budget. The cached bucket belongs to the
	// old rate, so applying the row has to drop it.
	fixture.applyPeer(t, func(row *storage.VPNPeer) { row.PacketRateLimit = 100 })
	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil))
	if got := fixture.metrics.countDroppedWith("tcp", vpn.ClassRateLimited); got != 1 {
		t.Errorf("rate_limited = %d after the rate was raised, want it to stay at 1; drops were %v",
			got, fixture.metrics.droppedRecords())
	}
}

func TestVPNPacketPipelineCountsCapacityWhenThePeerIsFull(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	cfg.MaxFlowsPerPeer = 1
	fixture := newVPNGatewayFixture(t, cfg, nil)
	fixture.applyPeer(t, nil)

	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolUDP, vpnPipeServiceIP, 53, []byte("one")))
	waitForOpener(t, fixture.opener, 1)
	fixture.gateway.handlePacket(vpnWireUDP(vpnPipePeerIP, vpnPipeServiceIP, 51001, 53, []byte("two")))

	if got := fixture.metrics.countDroppedWith("udp", vpn.ClassCapacityExhausted); got != 1 {
		t.Errorf("capacity_exhausted = %d, want 1; drops were %v", got, fixture.metrics.droppedRecords())
	}
	if fixture.opener.openCount() != 1 {
		t.Errorf("the opener was called %d times, want 1: a refused datagram must not dial", fixture.opener.openCount())
	}
}

func TestVPNPacketPipelineRefusesAnUnwiredProtocolHandler(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)
	fixture.applyPeer(t, nil)
	fixture.gateway.serveTCP = nil

	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil))

	if got := fixture.metrics.countDroppedWith("tcp", vpn.ClassStackError); got != 1 {
		t.Errorf("stack_error = %d, want 1; drops were %v", got, fixture.metrics.droppedRecords())
	}
}

func TestVPNPacketPipelineCountsDropsOfAnUnknownPeerWithoutAPeerID(t *testing.T) {
	fixture := newVPNGatewayFixture(t, config.VPNConfig{}, nil)

	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolTCP, vpnPipeServiceIP, 443, nil))
	fixture.gateway.denials.Flush(context.Background())

	entries := fixture.audits.recorded()
	if len(entries) != 1 {
		t.Fatalf("%d audit entries were written, want 1", len(entries))
	}
	if entries[0].ResourceID != "" {
		t.Errorf("ResourceID = %q, want empty: no peer owns this address", entries[0].ResourceID)
	}
}

// waitForOpener blocks until the fake transport has been dialled want times.
func waitForOpener(t *testing.T, opener *fakeAgentOpener, want int) {
	t.Helper()
	deadline := time.Now().Add(vpnPipeWait)
	for time.Now().Before(deadline) {
		if opener.openCount() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the opener was called %d times, want %d", opener.openCount(), want)
}

// vpnOutUDP decodes the UDP header of an emitted packet.
func vpnOutUDP(t *testing.T, packet vpnOutPacket) (srcPort, dstPort uint16, payload []byte) {
	t.Helper()
	offset := int(packet.ip().HeaderLength())
	if len(packet.raw) < offset+vpnWireUDPHeader {
		t.Fatalf("emitted packet is too short to hold a udp header: %d bytes", len(packet.raw))
	}
	body := packet.raw[offset:]
	srcPort = binary.BigEndian.Uint16(body[0:2])
	dstPort = binary.BigEndian.Uint16(body[2:4])
	payload = append([]byte(nil), body[vpnWireUDPHeader:]...)
	return srcPort, dstPort, payload
}
