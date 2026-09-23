package vpn

import (
	"fmt"
	"net"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

// IP protocol numbers on the egress whitelist. Nothing else is forwarded, which
// keeps the gateway from becoming a transport for protocols it cannot account
// for or rate limit.
const (
	ProtocolICMP uint8 = 1
	ProtocolTCP  uint8 = 6
	ProtocolUDP  uint8 = 17
)

// ErrorClass is the low-cardinality reason a packet was not forwarded. It is the
// label value on the VPN packet metrics and the reason field of the
// vpn_packet_denied audit event, so the set is fixed and published: adding a
// class is fine, renaming one splits a dashboard series in two.
//
// The strings come verbatim from the embedded VPN gateway design spec §12.2.
type ErrorClass string

const (
	ClassPeerUnknown         ErrorClass = "peer_unknown"
	ClassPeerRevoked         ErrorClass = "peer_revoked"
	ClassPeerExpired         ErrorClass = "peer_expired"
	ClassTargetDenied        ErrorClass = "target_denied"
	ClassMetadataDenied      ErrorClass = "metadata_denied"
	ClassPortDenied          ErrorClass = "port_denied"
	ClassProtocolUnsupported ErrorClass = "protocol_unsupported"
	ClassFragmentDropped     ErrorClass = "fragment_dropped"
	ClassOversizeDropped     ErrorClass = "oversize_dropped"
	ClassCapacityExhausted   ErrorClass = "capacity_exhausted"
	ClassRateLimited         ErrorClass = "rate_limited"
	ClassEgressUnavailable   ErrorClass = "egress_unavailable"
	ClassEgressTimeout       ErrorClass = "egress_timeout"
	ClassICMPUnsupported     ErrorClass = "icmp_unsupported"
	ClassICMPTimeout         ErrorClass = "icmp_timeout"
	ClassStackError          ErrorClass = "stack_error"
)

// allErrorClasses is the published set in declaration order. It exists so a
// caller that must bound itself by the set - the data plane's denial aggregator
// keeps one overflow bucket per class - derives that bound from the contract
// rather than from a number that would silently drift when a class is added.
var allErrorClasses = []ErrorClass{
	ClassPeerUnknown, ClassPeerRevoked, ClassPeerExpired, ClassTargetDenied,
	ClassMetadataDenied, ClassPortDenied, ClassProtocolUnsupported, ClassFragmentDropped,
	ClassOversizeDropped, ClassCapacityExhausted, ClassRateLimited, ClassEgressUnavailable,
	ClassEgressTimeout, ClassICMPUnsupported, ClassICMPTimeout, ClassStackError,
}

// AllErrorClasses returns every published error_class.
//
// The slice is a copy, so a caller cannot reorder or extend the contract from
// the outside; the constants above are the only definition site.
func AllErrorClasses() []ErrorClass {
	return append([]ErrorClass(nil), allErrorClasses...)
}

// PacketPolicy emits only the fragment, oversize, protocol, metadata, target and
// port classes. The remaining constants above belong to the same published label
// set but are produced by the data plane: peer lookup, capacity and rate limiting
// happen once a packet has arrived on a tunnel, and egress and stack failures
// happen while a flow is being opened. They are declared here so the whole set
// has one definition site and cannot drift.

// PacketError is a packet the policy refused to forward.
//
// It is deliberately not an HTTP error: the IP layer has no response channel, so
// a denial is never reported to the user. It surfaces only as a metric label, an
// aggregated audit event and this in-process error, which is why it carries the
// class separately from the human-readable reason.
type PacketError struct {
	class  ErrorClass
	reason string
}

func denied(class ErrorClass, reason string) *PacketError {
	return &PacketError{class: class, reason: reason}
}

func (e *PacketError) Error() string {
	if e == nil {
		return ""
	}
	if e.reason == "" {
		return string(e.class)
	}
	return string(e.class) + ": " + e.reason
}

// ErrorClass returns the published label value for this denial.
func (e *PacketError) ErrorClass() ErrorClass {
	if e == nil {
		return ""
	}
	return e.class
}

// Reason returns the operator-facing detail behind the class. It never contains
// key material or packet payload; a destination address is not included either,
// because the audit event already records it as a separate field and duplicating
// it here would put addresses into every log line that prints an error.
func (e *PacketError) Reason() string {
	if e == nil {
		return ""
	}
	return e.reason
}

// Packet is the header metadata a policy decision needs. It is a description of
// a packet rather than the packet itself: no payload bytes are ever copied into
// it, which is what makes it safe to log and to keep in a denial counter.
//
// Source is carried for completeness but is not evaluated here. The data plane
// resolves the source address to a peer first and then selects that peer's
// policy, so by the time Allow runs the source is already trusted to belong to
// the tunnel it arrived on.
type Packet struct {
	Protocol uint8
	Source   net.IP
	Dest     net.IP
	Port     int
	Size     int
	Fragment bool
}

// PacketPolicy decides whether one peer's packet may leave the gateway.
//
// It reuses routing for the two address decisions that package already owns,
// IsDangerousAddress and IsPrivateTarget, so the VPN gateway, the tp-* proxy
// entry and the agent policies cannot disagree about what a dangerous or private
// destination is. Port matching reuses the shape of routing.Policy.Ports.
//
// Address allowlist matching is implemented here rather than delegated to
// routing.Policy on purpose: routing treats an empty CIDR list as "no
// restriction", while an empty VPN allowed_ips must mean "nothing is reachable".
// Delegating that one field would invert the safest default in the system.
type PacketPolicy struct {
	allowedIPs          []*net.IPNet
	ports               map[int]struct{}
	allowPrivateTargets bool
	icmpEnabled         bool
	mtu                 int
}

// NewPacketPolicy builds the egress policy for one peer.
//
// mtu comes from server.vpn.mtu rather than from the peer row, because a packet
// larger than the tunnel MTU cannot be forwarded no matter which peer sent it.
func NewPacketPolicy(spec PeerSpec, mtu int) (PacketPolicy, error) {
	// Reusing Validate keeps the two entry points honest: a policy can only be
	// built for a spec that could also have been persisted. It is called once at
	// construction, never on the packet path. A peer whose expiry has already
	// passed therefore cannot get a policy at all, which is the fail-closed
	// answer for a retired identity.
	if err := spec.Validate(time.Now()); err != nil {
		return PacketPolicy{}, err
	}
	if mtu < 1 {
		return PacketPolicy{}, fmt.Errorf("vpn: mtu must be at least 1, got %d", mtu)
	}
	ports := make(map[int]struct{}, len(spec.AllowedPorts))
	for _, port := range spec.AllowedPorts {
		ports[port] = struct{}{}
	}
	networks := make([]*net.IPNet, 0, len(spec.AllowedIPs))
	for _, network := range spec.AllowedIPs {
		networks = append(networks, network)
	}
	return PacketPolicy{
		allowedIPs:          networks,
		ports:               ports,
		allowPrivateTargets: spec.AllowPrivateTargets,
		icmpEnabled:         spec.ICMPEnabled,
		mtu:                 mtu,
	}, nil
}

// Allow reports whether a packet may be forwarded, returning a *PacketError with
// the published error_class when it may not.
//
// The order of the checks is part of the security semantics, not an
// implementation detail, and must not be rearranged:
//
//  1. Fragments first. A non-first fragment carries no port, so evaluating the
//     port allowlist after accepting one would be equivalent to not having it.
//  2. Oversize next, because a packet that cannot fit the tunnel is dropped
//     regardless of where it was going.
//  3. The protocol whitelist, so an unsupported protocol is named as such rather
//     than being reported as a destination problem.
//  4. Dangerous addresses. This outranks the peer's own allowlist: a peer that
//     was granted 0.0.0.0/0 still cannot reach the cloud metadata service.
//  5. Private targets, gated on the per-peer switch.
//  6. The address allowlist.
//  7. The port allowlist, last, because it is the only check that needs a
//     transport-layer header.
//
// Every branch fails closed. An unknown protocol, a malformed port and a missing
// destination are all denials.
func (p PacketPolicy) Allow(pkt Packet) error {
	if pkt.Fragment {
		return denied(ClassFragmentDropped, "fragmented packets are not forwarded")
	}
	if pkt.Size > p.mtu {
		return denied(ClassOversizeDropped, fmt.Sprintf("packet of %d bytes exceeds the %d byte tunnel mtu", pkt.Size, p.mtu))
	}
	switch pkt.Protocol {
	case ProtocolTCP, ProtocolUDP:
	case ProtocolICMP:
		if !p.icmpEnabled {
			return denied(ClassICMPUnsupported, "icmp is not enabled for this peer")
		}
	default:
		return denied(ClassProtocolUnsupported, fmt.Sprintf("ip protocol %d is not forwarded", pkt.Protocol))
	}
	if pkt.Dest == nil || pkt.Dest.To4() == nil {
		return denied(ClassTargetDenied, "destination must be an ipv4 address")
	}
	if routing.IsDangerousAddress(pkt.Dest) {
		return denied(ClassMetadataDenied, "destination is in a reserved, multicast or metadata range")
	}
	if !p.allowPrivateTargets && routing.IsPrivateTarget(pkt.Dest) {
		return denied(ClassTargetDenied, "destination is a private address and this peer may not reach private targets")
	}
	if !p.containsDest(pkt.Dest) {
		return denied(ClassTargetDenied, "destination is outside the peer allowed_ips")
	}
	if pkt.Protocol == ProtocolICMP {
		// ICMP has no port, so the port allowlist does not apply. Applying it
		// anyway would make every echo request fail whenever a peer restricts
		// ports, which reads as a broken tunnel rather than a policy decision.
		return nil
	}
	if pkt.Port < minPort || pkt.Port > maxPort {
		return denied(ClassPortDenied, fmt.Sprintf("port %d is not a valid tcp/udp port", pkt.Port))
	}
	if len(p.ports) > 0 {
		if _, ok := p.ports[pkt.Port]; !ok {
			return denied(ClassPortDenied, fmt.Sprintf("port %d is not in the peer allowed_ports", pkt.Port))
		}
	}
	return nil
}

// containsDest matches the destination against the peer's address allowlist.
//
// An empty allowlist denies everything. That is the opposite of an empty port
// allowlist, which permits every port, and the asymmetry is inherited from
// WireGuard where an absent AllowedIPs blocks all traffic. Failing closed here
// is what stops a peer created with a missing policy from becoming an open relay.
func (p PacketPolicy) containsDest(dest net.IP) bool {
	four := dest.To4()
	if four == nil {
		return false
	}
	for _, network := range p.allowedIPs {
		if network != nil && network.Contains(four) {
			return true
		}
	}
	return false
}
