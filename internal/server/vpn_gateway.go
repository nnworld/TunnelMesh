//go:build vpn

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/device"

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

// errVPNGatewayClosed reports a Start attempted on a gateway that has been
// closed.
//
// It is a distinct error rather than a silent no-op because both callers that can
// hit it need to know the data plane is gone. A runtime shutdown racing a health
// check would otherwise report a healthy gateway, and a restart that reused the
// old object would otherwise believe it came up while holding no socket at all.
var errVPNGatewayClosed = errors.New("vpn: the gateway is closed")

// vpnPeerLoadTimeout bounds the one database read Start makes. A gateway that
// waited forever for its peer set would hold a bound UDP port while answering no
// handshake, which is the failure an operator can least diagnose from outside.
const vpnPeerLoadTimeout = 30 * time.Second

// vpnDeviceLogInterval bounds how often wireguard-go's own error text reaches the
// process log. See vpnDeviceLogger for why it is bounded at all.
const vpnDeviceLogInterval = time.Second

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

	// mu guards the WireGuard endpoint. It is separate from every other lock here
	// because the endpoint is written by management operations and read by the
	// peer hooks on the packet path, and neither may wait on the other.
	//
	// It is never held across a call into the device. IpcSet serialises on the
	// device's own mutex and can block behind an in-flight handshake, so a hook
	// that waited for one would stall the peer table for every other peer on the
	// node.
	mu        sync.Mutex
	wg        *device.Device
	bind      *vpnBind
	boundPort uint16
	started   bool

	// bucketMu guards the per-peer token buckets. It is separate from every other
	// lock because a bucket is consulted once per packet and must never wait on a
	// management write.
	bucketMu sync.Mutex
	buckets  map[string]*packetBucket

	// selfMu guards selfAddress, which is learned from the subnet lease after
	// construction and read on the packet path.
	selfMu      sync.RWMutex
	selfAddress netip.Addr

	// The lifecycle state below is owned by vpn_lifecycle.go, which is where the
	// loops it describes are started and stopped.
	//
	// bgCtx is separate from baseCtx because the two end at different moments.
	// baseCtx bounds the streams the gateway opens and is cancelled by the forced
	// step of the teardown; bgCtx survives until the loops are stopped, so a long
	// drain cannot let the subnet lease expire while peers are still being served.
	bgOnce           sync.Once
	bgMu             sync.Mutex
	bgStarted        bool
	bgStopped        bool
	bgCtx            context.Context
	cancelBackground context.CancelFunc
	reaperDone       chan struct{}
	renewalDone      chan struct{}
	// leaseRenewInterval and idleReapInterval default to a fraction of the lease
	// TTL and of server.vpn.idle_timeout. They are fields so a test can observe
	// several passes instead of waiting out a production interval.
	leaseRenewInterval time.Duration
	idleReapInterval   time.Duration
	// shutdownMu guards shutdownTrace, the record of which teardown steps ran and
	// in which order. It is logged at the end of a shutdown and asserted against
	// D14 by the lifecycle tests.
	shutdownMu    sync.Mutex
	shutdownTrace []string

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
	bgCtx, cancelBackground := context.WithCancel(ctx)
	gateway := &vpnGateway{
		deps:             deps,
		identity:         identity,
		metrics:          newVPNMetrics(deps.Metrics),
		nowFn:            nowFn,
		mtu:              mtu,
		buckets:          make(map[string]*packetBucket),
		baseCtx:          baseCtx,
		cancelBase:       cancelBase,
		bgCtx:            bgCtx,
		cancelBackground: cancelBackground,
		// Both channels exist from construction and are closed by their loop. A
		// teardown that only waited on channels a started loop created would have to
		// know whether Start ran, which is exactly the state a failed construction
		// does not have.
		reaperDone:  make(chan struct{}),
		renewalDone: make(chan struct{}),
		closed:      make(chan struct{}),
	}
	gateway.peers = newVPNPeerTable(vpnPeerTableConfig{
		MTU:    mtu,
		NodeID: deps.NodeID,
		Now:    nowFn,
		Hooks: vpnPeerHooks{
			onApply:            gateway.peerApplied,
			onRemove:           gateway.peerRemoved,
			onReplacePublicKey: gateway.peerKeyReplaced,
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

// renderNodeConfig picks the identity block the device is started with.
//
// A configured port is rendered into the block. An ephemeral one is not, because
// listen_port=0 means "stop listening" to every WireGuard implementation that
// follows wg(8) and "choose one for me" to wireguard-go's bind, and the only
// spelling both agree on is to leave the line out and let vpnBind supply the port
// it was configured with.
func (g *vpnGateway) renderNodeConfig(bind *vpnBind) (string, error) {
	if port := bind.port(); port > 0 {
		return vpn.RenderNodeUAPI(g.identity, port)
	}
	return vpn.RenderNodeIdentityUAPI(g.identity)
}

// loadPeers copies this node's peer rows out of the database and onto the device.
//
// A row the peer table refuses is logged and skipped rather than failing the
// start: one malformed row must not take the gateway down for every peer that is
// fine, and the refusal is already reported with the reason at the moment it
// happened. The read itself failing is different. Without it the gateway would
// refuse every packet as an unknown peer while the management API kept reporting
// the data plane as enabled, and the database is the authority (D10), so an
// unreadable one is a startup failure.
func (g *vpnGateway) loadPeers(ctx context.Context) error {
	if g.deps.Peers == nil {
		return nil
	}
	loadCtx, cancel := context.WithTimeout(ctx, vpnPeerLoadTimeout)
	defer cancel()
	rows, err := g.deps.Peers.ListByNode(loadCtx, g.deps.NodeID)
	if err != nil {
		return fmt.Errorf("vpn: load the peer set for node %q: %w", g.deps.NodeID, err)
	}
	refused := 0
	for _, row := range rows {
		if applyErr := g.peers.ApplyPeer(row); applyErr != nil {
			refused++
			slog.Error("vpn_peer_load_refused", "peer_id", row.ID, "error", applyErr)
		}
	}
	slog.Info("vpn_peers_loaded", "node_id", g.deps.NodeID, "peers", len(rows)-refused, "refused", refused)
	return nil
}

// localPort reports the UDP port the endpoint actually holds, or zero when the
// gateway is not serving.
//
// It is not the configured port. When server.vpn.listen names port 0 the
// operating system chose one at Start, and this is the only place that knows
// which, so it is what the startup log and the node status report.
func (g *vpnGateway) localPort() uint16 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.boundPort
}

// endpoint returns the WireGuard device, or nil when the gateway is not serving.
//
// The pointer is copied out under the lock and used after it is released. Every
// call into the device can block, and holding mu across one would let a handshake
// stall a management write.
func (g *vpnGateway) endpoint() *device.Device {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.wg
}

// installDevicePeer puts one peer's key and tunnel address on the device.
func (g *vpnGateway) installDevicePeer(entry vpnPeerEntry) error {
	tunnel := g.endpoint()
	if tunnel == nil {
		// Not serving yet, or already torn down. The peer table is the memory copy
		// either way, and a Start that follows will load it from the database.
		return nil
	}
	block, err := vpn.RenderPeerUAPI(entry.PublicKey, net.IP(entry.VPNIP.AsSlice()))
	if err != nil {
		return err
	}
	if err := tunnel.IpcSet(block); err != nil {
		return fmt.Errorf("install peer %s on the wireguard device: %w", entry.PeerID, err)
	}
	return nil
}

// removeDevicePeer takes one key off the device.
//
// Removal is by public key rather than by peer id or address, which is what makes
// a revocation safe against reuse: an address handed to a different peer later
// cannot resurrect the identity that was just revoked.
func (g *vpnGateway) removeDevicePeer(peerID, publicKey string) {
	tunnel := g.endpoint()
	if tunnel == nil {
		return
	}
	block, err := vpn.RenderPeerRemoveUAPI(publicKey)
	if err != nil {
		slog.Error("vpn_peer_device_remove_render_failed", "peer_id", peerID, "error", err)
		return
	}
	if err := tunnel.IpcSet(block); err != nil {
		slog.Error("vpn_peer_device_remove_failed", "peer_id", peerID, "error", err)
	}
}

// stopEndpoint releases the WireGuard device and the socket it holds.
//
// Closing the device closes the tunnel device it was handed as well, so this is
// the point after which no datagram can be decrypted into the pipeline and none
// can be encrypted out of it. The bind is closed again afterwards because a
// failure before the device existed leaves it holding a socket on its own, and
// Close on both is idempotent.
func (g *vpnGateway) stopEndpoint() {
	g.mu.Lock()
	tunnel := g.wg
	bind := g.bind
	g.wg = nil
	g.bind = nil
	g.boundPort = 0
	g.started = false
	g.mu.Unlock()
	if tunnel != nil {
		tunnel.Close()
	}
	if bind != nil {
		_ = bind.Close()
	}
}

// vpnDeviceLogThrottle rate limits the device's error text.
type vpnDeviceLogThrottle struct {
	mu         sync.Mutex
	last       time.Time
	suppressed int
}

func (l *vpnDeviceLogThrottle) errorf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	l.mu.Lock()
	now := time.Now()
	if !l.last.IsZero() && now.Sub(l.last) < vpnDeviceLogInterval {
		l.suppressed++
		l.mu.Unlock()
		return
	}
	suppressed := l.suppressed
	l.suppressed = 0
	l.last = now
	l.mu.Unlock()
	slog.Error("vpn_device_error", "error", message, "suppressed_since_last", suppressed)
}

// vpnDeviceLogger adapts wireguard-go's two-level logger to slog.
//
// Verbose output is discarded. It is per packet and per handshake, so a gateway
// carrying real traffic would produce more of it than the rest of the process
// combined, and there is no configuration surface in this product that asks for
// it.
//
// Errors are throttled rather than forwarded, because the device reports one per
// datagram it cannot handle and the peers that produce those datagrams are on the
// public internet. An unthrottled forward would let any host that can reach the
// UDP port fill the log, which is a denial of service against every other
// diagnostic the process writes. The suppressed count travels with the next
// message that is allowed through, so the volume is still visible.
func vpnDeviceLogger() *device.Logger {
	throttle := &vpnDeviceLogThrottle{}
	return &device.Logger{
		Verbosef: device.DiscardLogf,
		Errorf:   throttle.errorf,
	}
}

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
//
// A device install that fails is logged and left. The row is committed and stays
// committed, because the database is the authority and rolling it back from a
// memory hook would be the second source of truth D10 forbids. The cost is
// fail-closed: wireguard-go drops the datagrams of a peer it has never heard of
// before the pipeline sees them, so a failed install produces a peer that cannot
// connect, never one that connects without authority.
func (g *vpnGateway) peerApplied(entry vpnPeerEntry) {
	g.forgetBucket(entry.PeerID)
	if err := g.installDevicePeer(entry); err != nil {
		slog.Error("vpn_peer_device_install_failed", "peer_id", entry.PeerID, "error", err)
	}
}

// peerKeyReplaced retires the identity a rotation displaced.
//
// The peer table fires this before onApply, and that order is the only safe one:
// removing the old key first means there is no instant in which both identities
// are answerable, so a rotation takes effect the moment the management write
// commits rather than the moment somebody remembers to revoke the old key.
func (g *vpnGateway) peerKeyReplaced(oldKey, _ string, entry vpnPeerEntry) {
	slog.Info("vpn_peer_key_rotated", "peer_id", entry.PeerID)
	g.removeDevicePeer(entry.PeerID, oldKey)
}

// peerRemoved stops a peer that is no longer servable.
//
// Closing its flows is what makes revocation immediate: the row is committed, the
// key is about to come off the device, and a TCP connection that is already
// spliced would otherwise keep carrying bytes to an internal service for as long
// as the agent held the other end open.
func (g *vpnGateway) peerRemoved(entry vpnPeerEntry) {
	g.forgetBucket(entry.PeerID)
	g.removeDevicePeer(entry.PeerID, entry.PublicKey)
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
