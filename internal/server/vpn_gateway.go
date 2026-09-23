//go:build vpn

package server

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// vpnBuildTagged is true only in a binary built with -tags vpn, which is the only
// binary that links gVisor and wireguard-go.
const vpnBuildTagged = true

// vpnDefaultMTU is the fallback when a caller assembles a gateway without a
// validated configuration. It is the WireGuard convention: 1420 leaves 72 bytes
// of headroom under a 1500 byte Ethernet path for the encapsulation.
const vpnDefaultMTU = 1420

// vpnDenialWindow and vpnDenialMaxKeys bound the audit aggregation. They live
// here rather than in the aggregator's defaults because the window is a property
// of this deployment's audit expectations, not of the data structure.
const (
	vpnDenialWindow  = time.Minute
	vpnDenialMaxKeys = 4096
)

// vpnUnlimitedBucketOnce builds the shared bucket used by peers with no rate
// limit. Sharing one instance keeps an unlimited deployment from allocating a
// bucket per peer, which for a gateway with tens of thousands of peers is the
// difference between a map that matters and one that does not.
var vpnUnlimitedBucketOnce = sync.OnceValue(func() *packetBucket { return newPacketBucket(0, nil) })

// vpnGateway is the embedded WireGuard gateway.
//
// It is composed the way ProxyEntry is: one struct holding an already-validated
// configuration, the dependencies it may touch, and the runtime state it owns.
// Every dependency degrades rather than panics when nil, because a gateway that
// crashes the process takes the management API and every existing tunnel with it,
// and ADR 0002 requires the VPN plane to be fault-isolated from them.
type vpnGateway struct {
	deps     VPNGatewayDeps
	identity vpn.NodeIdentity
	metrics  vpnMetrics
	nowFn    func() time.Time
	mtu      int

	// device and stack exist from construction. Neither binds a socket: the UDP
	// endpoint belongs to the WireGuard device that Start assembles, which is why a
	// gateway that was constructed but never started costs a few megabytes and no
	// file descriptors.
	device *memoryDevice
	stack  *vpnStack

	peers   *vpnPeerTable
	flows   *vpnFlowTable
	denials *denialAggregator

	tcp  *vpnTCPRelay
	udp  *vpnUDPRelay
	icmp *vpnICMPRelay

	// bucketMu guards the per-peer token buckets. It is separate from every other
	// lock because a bucket is consulted once per packet and must never wait on a
	// management write.
	bucketMu sync.Mutex
	buckets  map[string]*packetBucket

	// selfMu guards selfAddress, which is learned from the subnet lease after
	// construction and read on the packet path.
	selfMu      sync.RWMutex
	selfAddress netip.Addr

	// The protocol handlers are fields rather than direct calls for two reasons. A
	// build whose stack is not wired can say so honestly instead of panicking, and
	// the pipeline's dispatch can be tested before either relay exists.
	// Each handler takes the parsed header and the datagram it came from. Only
	// TCP needs the bytes: it is the one protocol the gateway terminates in the
	// stack, so it is the one that has to hand the segment over.
	serveTCP  func(context.Context, vpnPeerEntry, vpnWirePacket, []byte)
	serveUDP  func(context.Context, vpnPeerEntry, vpnWirePacket, []byte)
	serveICMP func(context.Context, vpnPeerEntry, vpnWirePacket, []byte)

	// baseCtx is cancelled by Close and bounds every stream the gateway opens.
	baseCtx    context.Context
	cancelBase context.CancelFunc

	closeOnce sync.Once
	closed    chan struct{}
}

// newVPNGateway validates the composition and returns a gateway that is not yet
// serving. Binding the UDP port, loading the peer set from the database and
// spawning the background loops are Start's job, so a construction failure cannot
// leave a half-running goroutine behind.
func newVPNGateway(ctx context.Context, deps VPNGatewayDeps, identity vpn.NodeIdentity) (VPNDataPlane, error) {
	if identity.PrivateKey == "" || identity.PublicKey == "" {
		return nil, vpn.ErrNodeKeyMissing
	}
	nowFn := deps.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	mtu := deps.Config.MTU
	if mtu < vpn.MinTunnelMTU || mtu > vpn.MaxTunnelMTU {
		mtu = vpnDefaultMTU
	}
	if ctx == nil {
		ctx = context.Background()
	}
	baseCtx, cancelBase := context.WithCancel(ctx)
	gateway := &vpnGateway{
		deps:       deps,
		identity:   identity,
		metrics:    newVPNMetrics(deps.Metrics),
		nowFn:      nowFn,
		mtu:        mtu,
		buckets:    make(map[string]*packetBucket),
		baseCtx:    baseCtx,
		cancelBase: cancelBase,
		closed:     make(chan struct{}),
	}
	gateway.peers = newVPNPeerTable(vpnPeerTableConfig{
		MTU:    mtu,
		NodeID: deps.NodeID,
		Now:    nowFn,
		Hooks: vpnPeerHooks{
			onApply:  gateway.peerApplied,
			onRemove: gateway.peerRemoved,
		},
	})
	gateway.flows = newVPNFlowTable(vpnFlowTableConfig{
		MaxFlowsPerPeer: deps.Config.MaxFlowsPerPeer,
		MaxFlowsTotal:   deps.Config.MaxFlowsTotal,
		IdleTimeout:     deps.Config.IdleTimeout,
		Now:             nowFn,
	})
	gateway.denials = newDenialAggregator(deps.Audits, vpnDenialWindow, vpnDenialMaxKeys)
	device, err := newMemoryDevice(mtu, vpnDeviceQueueSizeDefault, gateway.handlePacket)
	if err != nil {
		gateway.release()
		return nil, err
	}
	gateway.device = device
	assembled, err := newVPNStack(device, gateway.metrics)
	if err != nil {
		gateway.release()
		return nil, err
	}
	gateway.stack = assembled
	gateway.tcp = newVPNTCPRelay(gateway)
	gateway.serveTCP = gateway.tcp.serve
	gateway.udp = newVPNUDPRelay(gateway)
	gateway.serveUDP = gateway.udp.serve
	gateway.icmp = newVPNICMPRelay(gateway)
	gateway.serveICMP = gateway.icmp.serve
	return gateway, nil
}

func (g *vpnGateway) now() time.Time { return g.nowFn() }

// Start is where the gateway acquires the resources it holds for the rest of the
// process lifetime.
func (g *vpnGateway) Start(_ context.Context) error { return nil }

// ApplyPeer and RemovePeer are the hot-reload side of VPNPeerSink. The database
// write has already committed when they are called, so a failure here is reported
// and counted but never rolled back: the row is the authority, and a memory view
// that disagrees with it is repaired by restarting the gateway.
func (g *vpnGateway) ApplyPeer(peer storage.VPNPeer) error {
	if err := g.peers.ApplyPeer(peer); err != nil {
		slog.Error("vpn_peer_apply_failed", "peer_id", peer.ID, "error", err)
		return err
	}
	return nil
}

func (g *vpnGateway) RemovePeer(peerID string) error { return g.peers.RemovePeer(peerID) }

// PeerFlows reports that this gateway serves no peer yet, which is the honest
// answer until the flow table exists. The false return is what makes the API
// answer 501 rather than an empty list.
func (g *vpnGateway) PeerFlows(_ string) ([]VPNFlowSnapshot, bool) { return nil, false }

// NodeStatus answers from configuration. The counters that need the database and
// the running data plane are left unset so the console renders an em dash instead
// of a zero that would claim the pool is empty.
func (g *vpnGateway) NodeStatus(_ context.Context) (VPNNodeStatus, error) {
	return VPNNodeStatus{
		NodeID:       g.deps.NodeID,
		Enabled:      g.deps.Config.Enabled,
		Listen:       g.deps.Config.Listen,
		EndpointHost: g.deps.Config.EndpointHost,
	}, nil
}

// peerApplied reacts to a peer becoming servable, or to its row changing.
//
// The cached token bucket is dropped so the next packet rebuilds it from the new
// rate. Keeping the old one would mean a management write that raised or lowered
// a peer's budget had no effect until the process restarted, which is the kind of
// silent disagreement between the database and the running gateway that D10
// exists to prevent.
func (g *vpnGateway) peerApplied(entry vpnPeerEntry) { g.forgetBucket(entry.PeerID) }

// peerRemoved stops a peer that is no longer servable.
//
// Closing its flows is what makes revocation immediate: the row is committed, the
// key is about to come off the device, and a TCP connection that is already
// spliced would otherwise keep carrying bytes to an internal service for as long
// as the agent held the other end open.
func (g *vpnGateway) peerRemoved(entry vpnPeerEntry) {
	g.forgetBucket(entry.PeerID)
	if stopped := g.flows.closePeer(entry.PeerID); stopped > 0 {
		slog.Info("vpn_peer_flows_stopped", "peer_id", entry.PeerID, "flows", stopped)
	}
}

// bucketFor returns the token bucket that bounds one peer's packet rate.
//
// The peer's own limit wins over server.vpn.packet_rate_per_peer, which is the
// gateway-wide default for peers that do not set one. Zero at both levels means
// unlimited, and the shared bucket makes that case allocation-free.
func (g *vpnGateway) bucketFor(entry vpnPeerEntry) *packetBucket {
	rate := entry.RateLimit
	if rate <= 0 {
		rate = g.deps.Config.PacketRatePerPeer
	}
	if rate <= 0 {
		return vpnUnlimitedBucketOnce()
	}
	g.bucketMu.Lock()
	defer g.bucketMu.Unlock()
	if bucket, ok := g.buckets[entry.PeerID]; ok {
		return bucket
	}
	bucket := newPacketBucket(rate, g.nowFn)
	g.buckets[entry.PeerID] = bucket
	return bucket
}

func (g *vpnGateway) forgetBucket(peerID string) {
	g.bucketMu.Lock()
	delete(g.buckets, peerID)
	g.bucketMu.Unlock()
}

// setSelfAddress records this node's own VPN address, the first address of its
// leased subnet.
//
// It is set after construction because the address comes from a database lease
// acquired during Start. Packets addressed to it are refused: it is the gateway's
// identity on the tunnel, not an internal service, and handing it to an agent
// would buy one guaranteed connection failure per packet.
func (g *vpnGateway) setSelfAddress(address netip.Addr) {
	g.selfMu.Lock()
	g.selfAddress = address
	g.selfMu.Unlock()
}

func (g *vpnGateway) isSelfAddress(address netip.Addr) bool {
	g.selfMu.RLock()
	defer g.selfMu.RUnlock()
	return g.selfAddress.IsValid() && g.selfAddress == address
}

// isClosed reports whether Close has run.
func (g *vpnGateway) isClosed() bool {
	select {
	case <-g.closed:
		return true
	default:
		return false
	}
}

// Close releases the gateway exactly once.
func (g *vpnGateway) Close() error {
	g.closeOnce.Do(g.release)
	return nil
}

// release is the teardown body. It is also what a failed construction calls, so
// an error halfway through assembly cannot leave a device, a stack or a denial
// aggregator behind.
//
// The order matters and follows D14: stop accepting new work first, then the
// flows, then the device, then the stack. Closing the flow table before the
// device means a handler that tries to emit a reply during teardown meets a
// closed device and returns, rather than queueing a packet nobody will encrypt.
func (g *vpnGateway) release() {
	select {
	case <-g.closed:
	default:
		close(g.closed)
	}
	if g.cancelBase != nil {
		g.cancelBase()
	}
	if g.flows != nil {
		g.flows.close()
	}
	if g.tcp != nil {
		g.tcp.close()
	}
	if g.udp != nil {
		g.udp.close()
	}
	if g.denials != nil {
		// A fresh context rather than baseCtx: baseCtx was just cancelled, and the
		// last window of denials is exactly the one an operator investigating a
		// shutdown wants to see.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = g.denials.Close(ctx)
		cancel()
	}
	if g.peers != nil {
		g.peers.close()
	}
	if g.device != nil {
		_ = g.device.Close()
	}
	if g.stack != nil {
		_ = g.stack.close()
	}
}
