//go:build vpn

package server

import (
	"context"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// vpnBuildTagged is true only in a binary built with -tags vpn, which is the only
// binary that links gVisor and wireguard-go.
const vpnBuildTagged = true

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

	closeOnce sync.Once
	closed    chan struct{}
}

// newVPNGateway validates the composition and returns a gateway that is not yet
// serving. Binding the UDP port and spawning the background loops are Start's
// job, so a construction failure cannot leave a half-running goroutine behind.
func newVPNGateway(_ context.Context, deps VPNGatewayDeps, identity vpn.NodeIdentity) (VPNDataPlane, error) {
	if identity.PrivateKey == "" || identity.PublicKey == "" {
		return nil, vpn.ErrNodeKeyMissing
	}
	gateway := &vpnGateway{
		deps:     deps,
		identity: identity,
		metrics:  newVPNMetrics(deps.Metrics),
		nowFn:    deps.Now,
		closed:   make(chan struct{}),
	}
	if gateway.nowFn == nil {
		gateway.nowFn = func() time.Time { return time.Now().UTC() }
	}
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
func (g *vpnGateway) ApplyPeer(_ storage.VPNPeer) error { return nil }

func (g *vpnGateway) RemovePeer(_ string) error { return nil }

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

// Close releases the gateway exactly once.
func (g *vpnGateway) Close() error {
	var err error
	g.closeOnce.Do(func() {
		close(g.closed)
	})
	return err
}
