//go:build vpn

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// vpnProtocolUnparsed is the metric label for a datagram whose IP header could
// not be read, and for a protocol the gateway has no name for. It is a fixed
// string rather than a rendering of the protocol number because a peer that sent
// garbage would otherwise be able to choose a label value, and metric labels are
// the one place an unbounded string becomes a memory leak.
const vpnProtocolUnparsed = "unknown"

// vpnHandlerMissingLogged keeps one unwired handler from producing a log line per
// packet. It can only happen in a build whose protocol relay was not installed,
// which is a startup fact and not a per-packet one.
var vpnHandlerMissingLogged sync.Map

// handlePacket is the device's inbound handler: the whole packet pipeline for one
// decrypted datagram.
//
// The order of the checks is the security semantics, not an implementation detail
// (D9), and every refusal is counted with one of the sixteen published
// error_class values and folded into an aggregated audit entry. Nothing here
// allocates on the allowed path beyond what the protocol handler does, because
// this runs once per packet on wireguard-go's decryption goroutine and a stall
// here is a stall for every peer.
//
// It never returns an error and never panics. wireguard-go calls it from its own
// read loop, so an error would have nowhere to go and a panic would take the
// process - and with it the management API and every existing tunnel - down.
func (g *vpnGateway) handlePacket(packet []byte) {
	ctx := g.baseCtx
	parsed, err := parseVPNWirePacket(packet)
	if err != nil {
		g.denyUnparsed(err)
		return
	}
	label := vpnProtocolLabel(parsed.Protocol)

	// Fragments first, before the peer is resolved. A non-first fragment carries no
	// transport header, so anything that needed a port would be guessing; and the
	// gateway does not reassemble, so there is no later stage that could make sense
	// of it. The peer is looked up anyway, but only to name it in the audit entry -
	// a fragment flood is attributed to whoever sent it rather than logged as
	// anonymous noise.
	if parsed.Fragment {
		attributed, _, _ := g.peers.lookupByIP(parsed.Src)
		g.denyPacket(label, parsed, attributed, vpn.ClassFragmentDropped, "fragmented packets are not forwarded")
		return
	}

	entry, class, ok := g.peers.lookupByIP(parsed.Src)
	if !ok {
		g.denyPacket(label, parsed, entry, class, vpnPeerRefusalReason(class))
		return
	}
	if !g.bucketFor(entry).Allow() {
		g.denyPacket(label, parsed, entry, vpn.ClassRateLimited, "the peer exceeded its packet rate limit")
		return
	}
	if g.isSelfAddress(parsed.Dst) {
		g.denyPacket(label, parsed, entry, vpn.ClassTargetDenied, "the gateway's own vpn address is not an internal service")
		return
	}
	// The policy runs per packet rather than once per flow. A peer can be patched,
	// revoked or expire while a connection is up, and a decision cached at flow
	// creation would keep serving all three.
	if err := entry.Policy.Allow(parsed.policyPacket()); err != nil {
		g.denyPolicy(label, parsed, entry, err)
		return
	}

	switch parsed.Protocol {
	case vpn.ProtocolTCP:
		g.dispatch("tcp", g.serveTCP, label, ctx, parsed, entry, packet)
	case vpn.ProtocolUDP:
		g.dispatch("udp", g.serveUDP, label, ctx, parsed, entry, packet)
	case vpn.ProtocolICMP:
		// Only echo is relayed. The gateway promises it never emits an ICMP
		// datagram it did not build itself, and answering a TTL-exceeded or a
		// destination-unreachable would mean generating one on a peer's behalf.
		if !parsed.isICMPEchoRequest() {
			g.denyPacket(label, parsed, entry, vpn.ClassProtocolUnsupported,
				fmt.Sprintf("icmp type %d code %d is not relayed", parsed.ICMPType, parsed.ICMPCode))
			return
		}
		g.dispatch("icmp", g.serveICMP, label, ctx, parsed, entry, packet)
	default:
		// Unreachable: the protocol whitelist above already refused everything
		// else. It stays because a policy change that widens the whitelist must
		// fail closed here rather than fall through to a nil handler.
		g.denyPacket(label, parsed, entry, vpn.ClassProtocolUnsupported,
			fmt.Sprintf("ip protocol %d is not forwarded", parsed.Protocol))
	}
}

// dispatch hands an allowed packet to its protocol handler, or counts the absence
// of one.
func (g *vpnGateway) dispatch(name string, handler func(context.Context, vpnPeerEntry, vpnWirePacket, []byte), label string, ctx context.Context, parsed vpnWirePacket, entry vpnPeerEntry, packet []byte) {
	if handler == nil {
		if _, loaded := vpnHandlerMissingLogged.LoadOrStore(name, struct{}{}); !loaded {
			slog.Error("vpn_handler_missing", "protocol", name,
				"action", "packets of this protocol are counted as stack_error and dropped")
		}
		g.denyPacket(label, parsed, entry, vpn.ClassStackError, "the "+name+" relay is not installed in this build")
		return
	}
	handler(ctx, entry, parsed, packet)
}

// denyUnparsed counts a datagram that could not be read at all.
//
// There is no peer to attribute it to and no protocol to label it with, so the
// record carries neither: an address that was never parsed cannot be trusted
// enough to look up, and the label set is fixed.
func (g *vpnGateway) denyUnparsed(cause error) {
	g.metrics.Dropped(vpnProtocolUnparsed, vpn.ClassProtocolUnsupported)
	g.denials.Record(g.baseCtx, vpnDenial{
		Class:    vpn.ClassProtocolUnsupported,
		Reason:   "the datagram could not be parsed: " + cause.Error(),
		Protocol: vpnProtocolUnparsed,
	})
}

// denyPacket counts one refusal and folds it into the audit window.
func (g *vpnGateway) denyPacket(label string, parsed vpnWirePacket, entry vpnPeerEntry, class vpn.ErrorClass, reason string) {
	g.metrics.Dropped(label, class)
	g.denials.Record(g.baseCtx, vpnDenial{
		PeerID:   entry.PeerID,
		Class:    class,
		Reason:   reason,
		Protocol: label,
		Dest:     parsed.Dst,
		Port:     parsed.DstPort,
	})
}

// denyAudited folds one refusal into the audit window without counting it a
// second time.
//
// It exists for the failures a component below the pipeline has already reported
// through the same series. The stack counts a claim or a listen fault as
// tcp/stack_error itself, because it cannot know which packet - if any - provoked
// it; a second increment for that packet would make one event look like two and
// would put every threshold built on the series out by a factor nobody could name.
func (g *vpnGateway) denyAudited(label string, parsed vpnWirePacket, entry vpnPeerEntry, class vpn.ErrorClass, reason string) {
	g.denials.Record(g.baseCtx, vpnDenial{
		PeerID:   entry.PeerID,
		Class:    class,
		Reason:   reason,
		Protocol: label,
		Dest:     parsed.Dst,
		Port:     parsed.DstPort,
	})
}

// denyPolicy maps a policy refusal onto its published class.
func (g *vpnGateway) denyPolicy(label string, parsed vpnWirePacket, entry vpnPeerEntry, err error) {
	var packetErr *vpn.PacketError
	if errors.As(err, &packetErr) {
		g.denyPacket(label, parsed, entry, packetErr.ErrorClass(), packetErr.Reason())
		return
	}
	g.denyPacket(label, parsed, entry, vpn.ClassStackError, err.Error())
}

// vpnPeerRefusalReason explains a peer lookup that did not produce a serving peer.
//
// The three cases are distinguished because they have three different remedies:
// an unknown address means a client is configured against a gateway that never
// issued it, a revoked one means somebody deliberately stopped this peer, and an
// expired one means a renewal is overdue.
func vpnPeerRefusalReason(class vpn.ErrorClass) string {
	switch class {
	case vpn.ClassPeerRevoked:
		return "the peer is disabled or revoked"
	case vpn.ClassPeerExpired:
		return "the peer configuration has expired"
	default:
		return "the source address does not belong to a peer of this gateway"
	}
}

// vpnProtocolLabel maps an IP protocol number onto the metric label the design
// spec fixes: "tcp", "udp" or "icmp-echo".
//
// ICMP is labelled with the stream protocol name rather than "icmp" because the
// only ICMP the gateway relays is echo, and the label has to match the value the
// agent-side stream carries, or one protocol would appear as two in a dashboard.
func vpnProtocolLabel(number uint8) string {
	switch number {
	case vpn.ProtocolTCP:
		return "tcp"
	case vpn.ProtocolUDP:
		return "udp"
	case vpn.ProtocolICMP:
		return protocol.StreamProtocolICMPEcho
	default:
		return vpnProtocolUnparsed
	}
}

// vpnTuple builds the flow key a datagram belongs to.
func vpnTuple(entry vpnPeerEntry, parsed vpnWirePacket, name string) vpnFlowKey {
	return vpnFlowKey{
		PeerID:   entry.PeerID,
		Protocol: name,
		Src:      netip.AddrPortFrom(parsed.Src, uint16(parsed.SrcPort)),
		Dst:      netip.AddrPortFrom(parsed.Dst, uint16(parsed.DstPort)),
	}
}
