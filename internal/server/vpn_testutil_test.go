package server

import (
	"encoding/binary"
	"net"
)

// IP protocol numbers used by the vpn packet fixtures. They are spelled out
// rather than taken from gvisor's header package so this helper stays usable in
// a default build, where the tagged dependencies are not linked.
const (
	vpnTestProtocolICMP uint8 = 1
	vpnTestProtocolTCP  uint8 = 6
	vpnTestProtocolUDP  uint8 = 17
)

// vpnIPv4Packet builds a syntactically complete IPv4 packet with a correct
// header checksum.
//
// It is a test fixture rather than a production encoder on purpose: the gateway
// assembles reply headers in the packet path, and that code needs an independent
// implementation to be checked against. Reusing the production encoder to build
// expectations would make the two agree by construction and prove nothing.
func vpnIPv4Packet(protocol uint8, src, dst net.IP, payload []byte) []byte {
	const headerLength = 20
	packet := make([]byte, headerLength+len(payload))
	packet[0] = 4<<4 | headerLength/4
	packet[1] = 0 // differentiated services / ECN
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	binary.BigEndian.PutUint16(packet[4:6], 1) // identification
	binary.BigEndian.PutUint16(packet[6:8], 0) // flags and fragment offset: "not a fragment"
	packet[8] = 64                             // ttl
	packet[9] = protocol
	copy(packet[12:16], src.To4())
	copy(packet[16:20], dst.To4())
	// The checksum field is still zero here, which is the state RFC 791 requires
	// it to be in while the sum is computed.
	binary.BigEndian.PutUint16(packet[10:12], vpnIPv4HeaderChecksum(packet[:headerLength]))
	copy(packet[headerLength:], payload)
	return packet
}

// vpnIPv4HeaderChecksum is the one's complement sum of RFC 791 §3.1.
func vpnIPv4HeaderChecksum(header []byte) uint16 {
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

// vpnIPv6VersionOf rewrites the version nibble of a packet so a test can feed the
// IPv4-only paths an IPv6 datagram without building a real one. Only the version
// matters: every check the gateway makes on the wrong-address-family path reads
// that nibble and nothing else.
func vpnIPv6VersionOf(packet []byte) []byte {
	rewritten := append([]byte(nil), packet...)
	rewritten[0] = 6<<4 | rewritten[0]&0x0f
	return rewritten
}
