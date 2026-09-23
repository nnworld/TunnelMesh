//go:build vpn

package server

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

const (
	// vpnUDPOutboxSize bounds the datagrams waiting for one association's stream.
	// A full outbox means the agent is not keeping up, and the honest response to
	// that on a datagram protocol is to drop and count: queueing without bound
	// would turn one slow peer into memory pressure for the whole gateway.
	vpnUDPOutboxSize = 256
	// vpnUDPReadBuffer is what one Read from an agent stream may return. It is the
	// protocol's datagram bound rather than the tunnel MTU so an oversized reply is
	// seen, counted and dropped instead of being silently truncated into a datagram
	// the peer's stack would reject.
	vpnUDPReadBuffer = protocol.MaxDatagram
)

// vpnUDPRelay carries UDP datagrams between tunnel peers and internal services.
//
// UDP is relayed by the gateway rather than terminated in netstack (D3). A stack
// that terminated UDP would have to own a socket per association and answer for
// ports the gateway never chose; relaying keeps one stream per four-tuple and
// leaves the gateway in control of what it emits.
type vpnUDPRelay struct {
	gateway *vpnGateway

	mu    sync.Mutex
	flows map[vpnFlowKey]*vpnUDPFlow
}

func newVPNUDPRelay(gateway *vpnGateway) *vpnUDPRelay {
	return &vpnUDPRelay{gateway: gateway, flows: make(map[vpnFlowKey]*vpnUDPFlow)}
}

// vpnUDPFlow is one relayed association: the flow-table entry that accounts for
// it, the agent-side stream that carries it, and the two addresses a reply has to
// be built from.
type vpnUDPFlow struct {
	relay       *vpnUDPRelay
	key         vpnFlowKey
	entry       vpnPeerEntry
	flow        *vpnFlow
	serviceAddr netip.AddrPort
	peerAddr    netip.AddrPort
	// denialDest and denialPort are what an audit entry names when the stream
	// itself fails. The parsed packet is not kept: its payload aliases a buffer
	// the device owns, and holding it would keep the whole datagram alive for the
	// lifetime of the association.
	denialDest netip.Addr
	denialPort int

	outbox chan []byte
	ready  chan struct{}
	done   chan struct{}

	readyOnce sync.Once
	once      sync.Once

	mu     sync.Mutex
	stream io.ReadWriteCloser
	cancel context.CancelFunc
	err    error
}

// serve handles one allowed UDP datagram.
//
// The association is looked up and, if it does not exist, registered and inserted
// under one lock. That single critical section is what makes two datagrams of one
// four-tuple arriving on different decryption goroutines converge on one stream:
// without it both would see an empty table and both would dial, and the peer
// would get two answers to one query.
func (r *vpnUDPRelay) serve(_ context.Context, entry vpnPeerEntry, parsed vpnWirePacket) {
	g := r.gateway
	label := "udp"
	key := vpnTuple(entry, parsed, label)

	r.mu.Lock()
	if existing, ok := r.flows[key]; ok {
		r.mu.Unlock()
		existing.enqueue(parsed)
		return
	}
	// The flow is registered before the dial starts so the per-peer and global
	// ceilings are charged for an association that is still being opened. Charging
	// only on success would let a peer with a slow egress agent hold twice its
	// budget in half-open dials.
	association := &vpnUDPFlow{
		relay:       r,
		key:         key,
		entry:       entry,
		serviceAddr: netip.AddrPortFrom(parsed.Dst, uint16(parsed.DstPort)),
		peerAddr:    netip.AddrPortFrom(parsed.Src, uint16(parsed.SrcPort)),
		denialDest:  parsed.Dst,
		denialPort:  parsed.DstPort,
		outbox:      make(chan []byte, vpnUDPOutboxSize),
		ready:       make(chan struct{}),
		done:        make(chan struct{}),
	}
	flow, created, err := g.flows.register(vpnFlowRegistration{
		Key:       key,
		Target:    parsed.Dst.String(),
		Port:      parsed.DstPort,
		PeerLimit: entry.PeerLimit,
		// retire is the flow's teardown hook, so the association is released for
		// every reason a flow can end: the stream failing, the idle reaper, a peer
		// being revoked and the gateway shutting down all reach it through here.
		OnClose: association.retire,
	})
	if err != nil {
		r.mu.Unlock()
		class := vpn.ClassCapacityExhausted
		if errors.Is(err, errVPNFlowTableClosed) {
			// A closed table is a gateway that is shutting down, not one that is
			// full. The distinction matters because capacity is a tuning problem
			// and shutdown is not a problem at all.
			class = vpn.ClassEgressUnavailable
		}
		g.denyPacket(label, parsed, entry, class, err.Error())
		return
	}
	if !created {
		existing, ok := r.flows[key]
		r.mu.Unlock()
		if !ok {
			// Unreachable while the two maps are only mutated under r.mu and a flow
			// is detached before its association is forgotten. It is counted rather
			// than ignored because a silent drop here would be indistinguishable
			// from a policy refusal.
			g.denyPacket(label, parsed, entry, vpn.ClassStackError, "a udp flow exists without its association")
			return
		}
		existing.enqueue(parsed)
		return
	}
	association.flow = flow
	r.flows[key] = association
	r.mu.Unlock()

	// Queued before the goroutine starts, so the datagram that caused the dial is
	// the first one written rather than a second-class citizen behind a race.
	association.enqueue(parsed)
	go association.run(g.baseCtx)
}

// enqueue hands one datagram to the association's writer.
func (a *vpnUDPFlow) enqueue(parsed vpnWirePacket) {
	g := a.relay.gateway
	payload := append([]byte(nil), parsed.Payload...)
	select {
	case a.outbox <- payload:
	default:
		g.metrics.Dropped("udp", vpn.ClassCapacityExhausted)
		g.denials.Record(g.baseCtx, vpnDenial{
			PeerID:   a.entry.PeerID,
			Class:    vpn.ClassCapacityExhausted,
			Reason:   "the udp send queue for this association is full",
			Protocol: "udp",
			Dest:     a.denialDest,
			Port:     a.denialPort,
		})
	}
}

// run opens the agent-side stream and then services both directions.
//
// It runs on its own goroutine because openVPNEgress may take the whole
// connect_timeout, and handlePacket runs on wireguard-go's decryption goroutine:
// blocking there would stall every peer the gateway serves, not just this one.
func (a *vpnUDPFlow) run(ctx context.Context) {
	g := a.relay.gateway
	request := relay.StreamRequest{
		NodeID:     g.deps.NodeID,
		AgentID:    a.entry.AgentID,
		Protocol:   "udp",
		TargetHost: a.serviceAddr.Addr().String(),
		TargetPort: int(a.serviceAddr.Port()),
	}
	stream, cancel, err := openVPNEgress(ctx, g.deps.Opener, request, g.deps.Config.ConnectTimeout)
	a.mu.Lock()
	a.stream, a.cancel, a.err = stream, cancel, err
	a.mu.Unlock()
	a.readyOnce.Do(func() { close(a.ready) })
	if err != nil {
		g.denyPacket("udp", a.denialPacket(), a.entry, vpnEgressClass(err), err.Error())
		a.teardown()
		return
	}
	g.metrics.Stream("udp", "started", "")
	go a.pump(stream)
	a.writeLoop(stream)
}

// denialPacket rebuilds the shape a denial record needs from what the association
// kept. Only the destination and the port are carried, never the payload.
func (a *vpnUDPFlow) denialPacket() vpnWirePacket {
	return vpnWirePacket{Version: 4, Protocol: vpn.ProtocolUDP, Dst: a.denialDest, DstPort: a.denialPort}
}

// vpnEgressClass maps a stream-open failure onto its published label.
func vpnEgressClass(err error) vpn.ErrorClass {
	switch {
	case errors.Is(err, errVPNEgressTimeout):
		return vpn.ClassEgressTimeout
	case errors.Is(err, errVPNFlowTableClosed):
		return vpn.ClassEgressUnavailable
	default:
		return vpn.ClassEgressUnavailable
	}
}

// writeLoop drains the outbox into the stream.
//
// One goroutine owns the write side so datagrams keep the order the peer sent
// them in. Writing from handlePacket's goroutine instead would have made the
// packet path block on the agent's flow control, which is exactly what the
// outbox exists to avoid.
func (a *vpnUDPFlow) writeLoop(stream io.ReadWriteCloser) {
	g := a.relay.gateway
	for {
		select {
		case <-a.done:
			return
		case payload := <-a.outbox:
			n, err := stream.Write(payload)
			if n > 0 {
				a.flow.AddSent(int64(n))
				a.flow.Touch()
				g.metrics.Bytes("ingress", "udp", int64(n))
			}
			if err != nil {
				a.teardown()
				return
			}
		}
	}
}

// pump reads the agent's replies and emits them towards the peer.
func (a *vpnUDPFlow) pump(stream io.ReadWriteCloser) {
	g := a.relay.gateway
	buffer := make([]byte, vpnUDPReadBuffer)
	for {
		n, err := stream.Read(buffer)
		if n > 0 {
			a.flow.AddReceived(int64(n))
			a.flow.Touch()
			g.metrics.Bytes("egress", "udp", int64(n))
			g.emitUDP(a, buffer[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !g.isClosed() {
				slog.Debug("vpn_udp_stream_ended", "peer_id", a.entry.PeerID, "target", a.serviceAddr.String(), "error", err)
			}
			a.teardown()
			return
		}
	}
}

// emitUDP builds one IPv4/UDP datagram and hands it to the device.
//
// The packet is assembled here rather than routed through netstack (D3): the
// stack has never heard of either address, and asking it to send would produce a
// route lookup failure or, worse, an ICMP error the gateway did not decide to
// send.
func (g *vpnGateway) emitUDP(association *vpnUDPFlow, payload []byte) {
	if len(payload) == 0 {
		return
	}
	packet := buildIPv4UDP(association.serviceAddr, association.peerAddr, payload)
	if len(packet) > g.mtu {
		// The tunnel cannot carry it and the gateway does not fragment, so the
		// datagram is dropped where the peer can at least see it in the metrics.
		// A real IP stack would fragment; refusing to is the documented trade-off
		// for a gateway that never emits an ICMP it did not build.
		g.metrics.Dropped("udp", vpn.ClassOversizeDropped)
		g.denials.Record(g.baseCtx, vpnDenial{
			PeerID:   association.entry.PeerID,
			Class:    vpn.ClassOversizeDropped,
			Reason:   "the reply does not fit the tunnel mtu and was not fragmented",
			Protocol: "udp",
			Dest:     association.denialDest,
			Port:     association.denialPort,
		})
		return
	}
	if err := g.device.emit(packet); err != nil {
		g.metrics.Dropped("udp", vpn.ClassCapacityExhausted)
		slog.Debug("vpn_udp_emit_failed", "peer_id", association.entry.PeerID, "error", err)
		return
	}
}

// buildIPv4UDP assembles one complete datagram with correct header checksums.
//
// The UDP checksum is computed rather than left at zero. Zero is legal in IPv4
// and means "not computed", but a receiver that cannot tell a zero checksum from
// a corrupt one is a receiver that forwards corruption, and the cost here is one
// pass over a datagram that is at most an MTU long.
func buildIPv4UDP(source, destination netip.AddrPort, payload []byte) []byte {
	total := vpnWireIPv4HeaderMinimum + vpnWireUDPHeader + len(payload)
	packet := make([]byte, total)
	packet[0] = 4<<4 | vpnWireIPv4HeaderMinimum/4
	binary.BigEndian.PutUint16(packet[2:4], uint16(total))
	packet[8] = 64 // ttl
	packet[9] = vpn.ProtocolUDP
	copy(packet[12:16], source.Addr().AsSlice())
	copy(packet[16:20], destination.Addr().AsSlice())
	binary.BigEndian.PutUint16(packet[10:12], vpnIPv4Checksum(packet[:vpnWireIPv4HeaderMinimum], 0))

	body := packet[vpnWireIPv4HeaderMinimum:]
	binary.BigEndian.PutUint16(body[0:2], uint16(source.Port()))
	binary.BigEndian.PutUint16(body[2:4], uint16(destination.Port()))
	binary.BigEndian.PutUint16(body[4:6], uint16(vpnWireUDPHeader+len(payload)))
	copy(body[vpnWireUDPHeader:], payload)
	binary.BigEndian.PutUint16(body[6:8], vpnUDPChecksum(packet[12:16], packet[16:20], body))
	return packet
}

// vpnIPv4Checksum is the one's complement sum of RFC 791 §3.1 over a header whose
// checksum field is already zero.
func vpnIPv4Checksum(header []byte, _ uint16) uint16 {
	var sum uint32
	for i := 0; i+1 < len(header); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(header[i : i+2]))
	}
	if len(header)%2 == 1 {
		sum += uint32(header[len(header)-1]) << 8
	}
	for sum > 0xffff {
		sum = sum>>16 + sum&0xffff
	}
	return ^uint16(sum)
}

// vpnUDPChecksum is the RFC 768 sum over the IPv4 pseudo-header and the UDP
// header plus payload.
func vpnUDPChecksum(source, destination, body []byte) uint16 {
	var sum uint32
	sum += uint32(binary.BigEndian.Uint16(source[0:2])) + uint32(binary.BigEndian.Uint16(source[2:4]))
	sum += uint32(binary.BigEndian.Uint16(destination[0:2])) + uint32(binary.BigEndian.Uint16(destination[2:4]))
	sum += uint32(vpn.ProtocolUDP)
	sum += uint32(len(body))
	for i := 0; i+1 < len(body); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(body[i : i+2]))
	}
	if len(body)%2 == 1 {
		sum += uint32(body[len(body)-1]) << 8
	}
	for sum > 0xffff {
		sum = sum>>16 + sum&0xffff
	}
	checksum := ^uint16(sum)
	if checksum == 0 {
		// Zero means "no checksum" on the wire, so a sum that lands on it is sent
		// as all ones: the two are equivalent arithmetically and only the non-zero
		// one is distinguishable from an uncomputed header.
		checksum = 0xffff
	}
	return checksum
}

// retire is the flow table's teardown hook: it releases the stream and takes the
// association out of the relay's map.
//
// It has to close the stream and not merely unmap the association, because most
// of the reasons a flow ends come from outside the relay - the idle reaper, a
// revocation, shutdown - and none of them know an agent-side datagram socket is
// attached to the entry they are closing.
func (a *vpnUDPFlow) retire() {
	a.releaseStream()
	a.relay.forget(a.key)
}

// releaseStream closes the association's stream exactly once.
func (a *vpnUDPFlow) releaseStream() {
	a.once.Do(func() {
		// done stops the writer, and closing ready wakes anything that was waiting
		// for a stream that is never going to arrive.
		close(a.done)
		a.readyOnce.Do(func() { close(a.ready) })
		a.mu.Lock()
		stream, cancel := a.stream, a.cancel
		a.stream, a.cancel = nil, nil
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if stream != nil {
			_ = stream.Close()
		}
		a.relay.gateway.metrics.Stream("udp", "closed", "")
	})
}

// teardown ends the association from inside the relay.
//
// The stream goes first and the flow-table entry second, because removing the
// entry fires OnClose, which is what unmaps the association. Removing it first
// would leave a window where a new datagram finds no association, cannot register
// a flow either, and is dropped for a reason no metric could name.
func (a *vpnUDPFlow) teardown() {
	a.releaseStream()
	a.relay.gateway.flows.remove(a.key)
}

// forget removes one association from the relay's map.
func (r *vpnUDPRelay) forget(key vpnFlowKey) {
	r.mu.Lock()
	delete(r.flows, key)
	r.mu.Unlock()
}

// close releases every association.
//
// It is a safety net rather than the normal path: the gateway closes the flow
// table first, and that already fires every association's retire. What is left
// here is an association whose flow entry was somehow never registered, and the
// guarantee that no agent-side socket outlives the gateway.
func (r *vpnUDPRelay) close() {
	r.mu.Lock()
	associations := make([]*vpnUDPFlow, 0, len(r.flows))
	for key, association := range r.flows {
		associations = append(associations, association)
		delete(r.flows, key)
	}
	r.mu.Unlock()
	for _, association := range associations {
		association.releaseStream()
	}
}
