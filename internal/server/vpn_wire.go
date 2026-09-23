package server

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// Sizes of the headers the tunnel parser has to walk. They are spelled out here
// rather than taken from gvisor's header package because this file is part of the
// default build, and importing gvisor into it would link the stack into every
// binary that does not need it - which is the one property D2 exists to protect.
const (
	vpnWireIPv4HeaderMinimum = 20
	vpnWireTCPHeaderMinimum  = 20
	vpnWireUDPHeader         = 8
	vpnWireICMPHeader        = 8
)

// TCP flag bits the gateway reads. Only SYN matters: it is the trigger for
// registering a listener before the segment is injected, so the first segment of
// a connection is never dropped by a stack that has not been told the address
// exists yet (D4).
const (
	vpnWireTCPSYN = 0x02
	vpnWireTCPACK = 0x10
)

var (
	// errVPNWireNotIPv4 reports a datagram of the wrong address family. The
	// tunnel is IPv4 end to end - the address pool, the egress policy and this
	// parser all are - so anything else is refused at the door rather than being
	// counted as an unsupported protocol deeper in the pipeline.
	errVPNWireNotIPv4 = errors.New("vpn: the tunnel carries ipv4 only")
	// errVPNWireShortPacket reports a datagram too short to hold the IPv4 header
	// it declares. It is distinct from the transport error because the caller
	// counts them differently: one is not an IP packet at all, the other is an IP
	// packet whose payload was cut off.
	errVPNWireShortPacket = errors.New("vpn: packet is shorter than the ipv4 header it declares")
	// errVPNWireTruncatedTransport reports a transport header that cannot be
	// read. Every branch of the parser bounds-checks before slicing, so a
	// truncated datagram produces this error instead of a panic in the gateway
	// process - a panic there would take the management API and every existing
	// tunnel down with it.
	errVPNWireTruncatedTransport = errors.New("vpn: the transport header is truncated")
)

// vpnWirePacket is one decrypted tunnel datagram reduced to the fields a policy
// decision, a flow key and a reply header need.
//
// It is a description of a packet and never holds the packet: Payload aliases the
// caller's buffer rather than copying it, so the pipeline must finish with a
// datagram before the device reuses the slice it handed over. That is safe
// because memoryDevice.Write clones every decrypted datagram before calling the
// handler, and nothing else injects into the pipeline.
//
// Size is the IP total length, which is the number server.vpn.mtu is compared
// against: the tunnel carries whole IP datagrams, so the MTU bound applies to the
// datagram and not to its payload.
type vpnWirePacket struct {
	Version  uint8
	Protocol uint8
	Src      netip.Addr
	Dst      netip.Addr
	Size     int
	// Fragment reports a datagram that carries the more-fragments flag or a
	// non-zero offset. The gateway forwards neither, and it does not reassemble,
	// so a fragmented datagram never reaches a transport parser: its body is not a
	// transport header when it is not the first fragment, and reading one would
	// produce ports that belong to nobody.
	Fragment bool
	// Malformed reports a length field that disagreed with the bytes received.
	// The parser trusts the bytes, because they are what actually arrived, and
	// records the disagreement so the pipeline can count it rather than silently
	// forwarding a datagram whose sender and receiver disagree about its shape.
	Malformed bool
	SrcPort   int
	DstPort   int
	TCPFlags  uint8
	ICMPType  uint8
	ICMPCode  uint8
	ICMPID    uint16
	ICMPSeq   uint16
	Payload   []byte
}

// parseVPNWirePacket reads one datagram.
//
// It is deliberately allocation-light and free of side effects: no counter is
// touched and no error is logged, because this runs once per packet on the hot
// path and the caller owns the decision about what a refusal means. The zero
// value is returned with every error, so a caller cannot mistake a partial parse
// for a usable one.
func parseVPNWirePacket(packet []byte) (vpnWirePacket, error) {
	if len(packet) < vpnWireIPv4HeaderMinimum {
		return vpnWirePacket{}, errVPNWireShortPacket
	}
	if packet[0]>>4 != 4 {
		return vpnWirePacket{}, errVPNWireNotIPv4
	}
	headerLength := int(packet[0]&0x0f) * 4
	if headerLength < vpnWireIPv4HeaderMinimum || len(packet) < headerLength {
		return vpnWirePacket{}, errVPNWireShortPacket
	}
	totalLength := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLength < headerLength {
		// The header cannot even cover itself, so there is no transport body to
		// look for and no honest Size to report.
		return vpnWirePacket{}, errVPNWireShortPacket
	}
	parsed := vpnWirePacket{
		Version:  4,
		Protocol: packet[9],
		Src:      netip.AddrFrom4([4]byte(packet[12:16])),
		Dst:      netip.AddrFrom4([4]byte(packet[16:20])),
		Size:     totalLength,
	}
	flagsAndOffset := binary.BigEndian.Uint16(packet[6:8])
	parsed.Fragment = flagsAndOffset&0x2000 != 0 || flagsAndOffset&0x1fff != 0

	// end is where the datagram stops. A frame may be padded past the IP total
	// length, and the padding is not part of the packet, so the declared length
	// wins; a declared length beyond what arrived is truncated and is recorded as
	// malformed rather than trusted.
	end := totalLength
	if end > len(packet) {
		end = len(packet)
		parsed.Size = end
		parsed.Malformed = true
	}
	body := packet[headerLength:end]
	if parsed.Fragment {
		// Everything after the IP header is opaque here. Copying it would make the
		// parser the second owner of bytes that are about to be dropped, so the
		// payload is left nil and the pipeline reports fragment_dropped.
		return parsed, nil
	}
	switch parsed.Protocol {
	case vpn.ProtocolTCP:
		if err := parseVPNTCP(&parsed, body); err != nil {
			return vpnWirePacket{}, err
		}
	case vpn.ProtocolUDP:
		if err := parseVPNUDP(&parsed, body); err != nil {
			return vpnWirePacket{}, err
		}
	case vpn.ProtocolICMP:
		if err := parseVPNICMP(&parsed, body); err != nil {
			return vpnWirePacket{}, err
		}
	default:
		// A protocol the gateway does not forward still parses: the pipeline needs
		// the header to count protocol_unsupported and to name the protocol in the
		// aggregated audit event. Its body is not interpreted.
		parsed.Payload = body
	}
	return parsed, nil
}

// parseVPNTCP reads a TCP header, skipping the options the data offset declares.
func parseVPNTCP(parsed *vpnWirePacket, body []byte) error {
	if len(body) < vpnWireTCPHeaderMinimum {
		return errVPNWireTruncatedTransport
	}
	dataOffset := int(body[12]>>4) * 4
	if dataOffset < vpnWireTCPHeaderMinimum || len(body) < dataOffset {
		return errVPNWireTruncatedTransport
	}
	parsed.SrcPort = int(binary.BigEndian.Uint16(body[0:2]))
	parsed.DstPort = int(binary.BigEndian.Uint16(body[2:4]))
	parsed.TCPFlags = body[13]
	parsed.Payload = body[dataOffset:]
	return nil
}

// parseVPNUDP reads a datagram header.
//
// The length field is checked against the bytes that arrived and the bytes win.
// A field larger than the datagram would read past the buffer; a field smaller
// than it would hide payload the sender actually transmitted. Neither is worth
// refusing a packet over - a UDP length that disagrees with the frame is what a
// path with a broken middlebox looks like - so the disagreement is recorded and
// the real bytes are forwarded.
func parseVPNUDP(parsed *vpnWirePacket, body []byte) error {
	if len(body) < vpnWireUDPHeader {
		return errVPNWireTruncatedTransport
	}
	parsed.SrcPort = int(binary.BigEndian.Uint16(body[0:2]))
	parsed.DstPort = int(binary.BigEndian.Uint16(body[2:4]))
	if int(binary.BigEndian.Uint16(body[4:6])) != len(body) {
		parsed.Malformed = true
	}
	parsed.Payload = body[vpnWireUDPHeader:]
	return nil
}

// parseVPNICMP reads a message header.
//
// The checksum is not verified. The datagram arrived inside a WireGuard tunnel
// that was itself authenticated and decrypted, so a corruption the Noise layer
// did not catch is not something an ICMP checksum would catch either, and the
// reply the gateway builds is checksummed by construction.
func parseVPNICMP(parsed *vpnWirePacket, body []byte) error {
	if len(body) < vpnWireICMPHeader {
		return errVPNWireTruncatedTransport
	}
	parsed.ICMPType = body[0]
	parsed.ICMPCode = body[1]
	parsed.ICMPID = binary.BigEndian.Uint16(body[4:6])
	parsed.ICMPSeq = binary.BigEndian.Uint16(body[6:8])
	parsed.Payload = body[vpnWireICMPHeader:]
	return nil
}

// isTCPSYN reports a bare SYN, the segment that opens a connection.
//
// A SYN+ACK is excluded on purpose: it is the second half of a handshake the
// gateway itself is answering, so treating it as a new connection would register
// a listener for an address the peer is trying to reach in the other direction.
func (p vpnWirePacket) isTCPSYN() bool {
	return p.Protocol == vpn.ProtocolTCP && p.TCPFlags&vpnWireTCPSYN != 0 && p.TCPFlags&vpnWireTCPACK == 0
}

// isICMPEchoRequest reports a type 8 code 0 message, the only ICMP the gateway
// relays. Every other type is refused upstream of the relay, which is what keeps
// the promise that the gateway never emits an ICMP datagram it did not build.
func (p vpnWirePacket) isICMPEchoRequest() bool {
	return p.Protocol == vpn.ProtocolICMP && p.ICMPType == 8 && p.ICMPCode == 0
}

// isICMPEchoReply reports a type 0 code 0 message. A peer sending one into the
// tunnel has nothing to talk to - the gateway is not a host on the internal
// network - so it is counted and dropped rather than relayed.
func (p vpnWirePacket) isICMPEchoReply() bool {
	return p.Protocol == vpn.ProtocolICMP && p.ICMPType == 0 && p.ICMPCode == 0
}

// policyPacket projects the header onto the shape vpn.PacketPolicy evaluates.
//
// The conversion exists so the gateway and the pure-logic policy cannot disagree
// about which fields a decision reads: the allowlist, the MTU bound, the fragment
// rule and the port rule all run against this one projection.
func (p vpnWirePacket) policyPacket() vpn.Packet {
	return vpn.Packet{
		Protocol: p.Protocol,
		Source:   net.IP(p.Src.AsSlice()),
		Dest:     net.IP(p.Dst.AsSlice()),
		Port:     p.DstPort,
		Size:     p.Size,
		Fragment: p.Fragment,
	}
}
