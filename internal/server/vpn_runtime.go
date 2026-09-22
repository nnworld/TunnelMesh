package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// ErrVPNBuildTagMissing is what an enabled gateway returns from a binary built
// without the "vpn" tag.
//
// The message is part of the deployment contract: it is quoted in
// docs/deployment/vpn-gateway.md and it names the flag to rebuild with, because
// the alternative - ignoring the setting - would leave an operator with a
// configuration that looks correct, a security group opened for UDP 51820, and
// nothing listening on it. Failing fast is the same stance the storage layer
// takes when auto-init is off and the schema does not match.
var ErrVPNBuildTagMissing = errors.New("this binary was built without VPN support; rebuild with -tags vpn")

// VPNFlowSnapshot is one active flow as the management API reports it.
//
// The JSON keys are the contract web/src/api/vpn.ts already declares, and the
// projection is deliberately narrow: no payload, no key material, no agent-side
// socket details. Target and port are included because an operator diagnosing
// "why is this peer slow" needs to know which internal service the flow goes to;
// they are never used as metric labels, where they would be high cardinality.
type VPNFlowSnapshot struct {
	ID            string    `json:"id"`
	Protocol      string    `json:"protocol"`
	Target        string    `json:"target"`
	Port          int       `json:"port"`
	StartedAt     time.Time `json:"startedAt"`
	BytesSent     int64     `json:"bytesSent"`
	BytesReceived int64     `json:"bytesReceived"`
}

// VPNNodeStatus is this node's gateway status.
//
// The pointer counters distinguish "zero" from "unknown": a node that has not
// leased a subnet yet has no allocated count to report, and rendering that as 0
// would tell an operator the pool is empty rather than unused. The console prints
// an em dash for a missing value.
type VPNNodeStatus struct {
	NodeID       string `json:"nodeId"`
	Enabled      bool   `json:"enabled"`
	Listen       string `json:"listen,omitempty"`
	EndpointHost string `json:"endpointHost,omitempty"`
	Subnet       string `json:"subnet,omitempty"`
	Allocated    *int   `json:"allocated,omitempty"`
	Capacity     *int   `json:"capacity,omitempty"`
	Peers        *int   `json:"peers,omitempty"`
	ICMPCapable  bool   `json:"icmpCapable"`
}

// VPNPeerSink is the write side of the peer hot-reload path. The management
// service calls it after a peer row has been committed, so the database stays the
// single source of truth and the in-memory view is a copy that can be rebuilt by
// restarting the gateway.
type VPNPeerSink interface {
	ApplyPeer(peer storage.VPNPeer) error
	RemovePeer(peerID string) error
}

// VPNDataPlane is everything the untagged control plane may ask the gateway to
// do. It is an interface rather than a concrete type so that no untagged file can
// reach a field that only exists in a tagged build, which is what keeps the heavy
// dependencies out of the default binary by construction rather than by review.
type VPNDataPlane interface {
	VPNPeerSink
	// Start begins the background work the gateway owns: the idle-flow reaper and
	// the subnet lease renewal. It is separate from construction so a failure to
	// bind the UDP port is reported before any goroutine exists.
	Start(ctx context.Context) error
	// PeerFlows returns the active flows of one peer. The boolean reports whether
	// this node's gateway serves that peer at all; a false answer is what makes
	// the API answer 501 instead of an empty list that would read as "no
	// traffic" for a peer whose traffic is on another node.
	PeerFlows(peerID string) ([]VPNFlowSnapshot, bool)
	// NodeStatus reports this node only. A fleet-wide view would need a cluster
	// RPC that does not exist, and inventing one silently is how a console ends
	// up showing a partial fleet as if it were the whole fleet.
	NodeStatus(ctx context.Context) (VPNNodeStatus, error)
	// Close drains and releases everything the gateway holds. It is idempotent.
	Close() error
}

// vpnMetrics is the observability seam of the data plane.
//
// It is an interface for two reasons. It lets a test assert which counters a
// packet path touched without standing up a Prometheus registry, and it confines
// the knowledge of which existing vectors the gateway reuses to one adapter, so
// the dedicated tunnelmesh_vpn_* vectors can be added later without editing the
// packet path.
type vpnMetrics interface {
	// Bytes counts tunnel payload bytes. Direction is "ingress" for bytes coming
	// from a peer and "egress" for bytes going back to it.
	Bytes(direction, protocol string, n int64)
	// Stream records one flow outcome and keeps the active-flow gauge honest.
	Stream(protocol, result, errorClass string)
	// Lease records a subnet lease renewal outcome.
	Lease(operation, result, errorClass string)
}

// observabilityVPNMetrics adapts the process-wide metrics set. A nil *Metrics is
// replaced by a no-op rather than panicking, matching the degradation rule
// NewProxyEntry already follows for its own optional dependencies.
type observabilityVPNMetrics struct{ metrics *observability.Metrics }

func newVPNMetrics(metrics *observability.Metrics) vpnMetrics {
	if metrics == nil {
		return nopVPNMetrics{}
	}
	return observabilityVPNMetrics{metrics: metrics}
}

func (m observabilityVPNMetrics) Bytes(direction, protocol string, n int64) {
	m.metrics.ObserveBytes("vpn", direction, protocol, n)
}

func (m observabilityVPNMetrics) Stream(protocol, result, errorClass string) {
	m.metrics.ObserveStream(protocol, result, errorClass)
}

func (m observabilityVPNMetrics) Lease(operation, result, errorClass string) {
	m.metrics.ObserveRegistryLease(operation, result, errorClass)
}

type nopVPNMetrics struct{}

func (nopVPNMetrics) Bytes(string, string, int64)   {}
func (nopVPNMetrics) Stream(string, string, string) {}
func (nopVPNMetrics) Lease(string, string, string)  {}

// VPNGatewayDeps carries everything the gateway is allowed to touch.
//
// The node identity is deliberately not a field: it is read from the environment
// inside startVPNGateway so that no assembly path can forget it, and so the key
// never travels through a struct that might be logged, rendered into a support
// bundle or compared in a test failure message.
type VPNGatewayDeps struct {
	Config config.VPNConfig
	// NodeID scopes which peers this gateway loads and which subnet lease it
	// renews. It is the same identity the registry and the peer rows use.
	NodeID string
	// Opener is the agent-facing transport, local sessions plus the cluster
	// connection selector, so an egress agent attached to another Server node is
	// reached through the existing relay without extra wiring.
	Opener relay.NodeTransport
	Peers  storage.VPNPeerRepository
	Leases storage.VPNIPLeaseRepository
	Audits storage.AuditRepository
	// Metrics may be nil, which degrades to a no-op rather than a panic.
	Metrics *observability.Metrics
	// Capabilities answers whether an egress agent can serve ICMP echo. It is
	// the same probe the peer service uses at issue time, consulted again before
	// a stream is opened because an agent can have reconnected without the
	// capability in the meantime.
	Capabilities VPNAgentCapabilityProbe
	Now          func() time.Time
}

// startVPNGateway is the single assembly point of the VPN data plane.
//
// The order of its checks is the order an operator needs them reported in: the
// build tag first, because nothing else can work without it; the node identity
// second, because it is a secret the deployment must inject; the configuration
// last, because it is the part the operator can edit without a rebuild. A
// disabled gateway returns a nil data plane and no error, which is what makes
// "off" cost nothing.
func startVPNGateway(ctx context.Context, deps VPNGatewayDeps) (VPNDataPlane, error) {
	if !deps.Config.Enabled {
		return nil, nil
	}
	if !vpnBuildTagged {
		return nil, ErrVPNBuildTagMissing
	}
	identity, err := vpn.NodeIdentityFromEnvironment()
	if err != nil {
		return nil, err
	}
	if err := validateVPNGatewayConfig(deps.Config); err != nil {
		return nil, err
	}
	gateway, err := newVPNGateway(ctx, deps, identity)
	if err != nil {
		return nil, err
	}
	if gateway == nil {
		return nil, errors.New("vpn gateway was constructed as nil")
	}
	return gateway, nil
}

// validateVPNGatewayConfig refuses a server.vpn section the gateway could not
// serve.
//
// It is a separate function rather than an inline check so both builds can test
// the rules directly: in an untagged binary startVPNGateway answers with the build
// tag error before it ever looks at the configuration, which would otherwise make
// every configuration assertion pass for the wrong reason.
//
// The rules are config.ValidateVPN's, not a second copy of them. The loader runs
// them for a configuration read from a file, and this runs them for one assembled
// by an embedder that never called Load; one implementation is what keeps the two
// from drifting about which sections are usable.
func validateVPNGatewayConfig(cfg config.VPNConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if problems := config.ValidateVPN(cfg); len(problems) > 0 {
		return fmt.Errorf("server.vpn is not usable: %s", strings.Join(problems, "; "))
	}
	return nil
}
