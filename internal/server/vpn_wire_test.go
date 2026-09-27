package server

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
)

// vpnWireTCP builds one IPv4/TCP packet without relying on the production parser
// or on gvisor, so both builds can use it.
func vpnWireTCP(src, dst string, srcPort, dstPort uint16, flags uint8, payload []byte) []byte {
	segment := make([]byte, 20+len(payload))
	binary.BigEndian.PutUint16(segment[0:2], srcPort)
	binary.BigEndian.PutUint16(segment[2:4], dstPort)
	binary.BigEndian.PutUint32(segment[4:8], 1000) // sequence
	binary.BigEndian.PutUint32(segment[8:12], 0)   // acknowledgement
	segment[12] = 5 << 4                           // data offset: five 32-bit words, no options
	segment[13] = flags
	binary.BigEndian.PutUint16(segment[14:16], 8192) // window
	copy(segment[20:], payload)
	return vpnIPv4Packet(vpnTestProtocolTCP, netip.MustParseAddr(src).AsSlice(), netip.MustParseAddr(dst).AsSlice(), segment)
}

// vpnWireUDP builds one IPv4/UDP datagram whose length field agrees with the
// payload unless the caller overwrites it.
func vpnWireUDP(src, dst string, srcPort, dstPort uint16, payload []byte) []byte {
	datagram := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(datagram[0:2], srcPort)
	binary.BigEndian.PutUint16(datagram[2:4], dstPort)
	binary.BigEndian.PutUint16(datagram[4:6], uint16(len(datagram)))
	copy(datagram[8:], payload)
	return vpnIPv4Packet(vpnTestProtocolUDP, netip.MustParseAddr(src).AsSlice(), netip.MustParseAddr(dst).AsSlice(), datagram)
}

// vpnWireICMP builds one IPv4/ICMP message with a correct checksum.
func vpnWireICMP(src, dst string, icmpType, icmpCode uint8, id, seq uint16, payload []byte) []byte {
	message := make([]byte, 8+len(payload))
	message[0] = icmpType
	message[1] = icmpCode
	binary.BigEndian.PutUint16(message[4:6], id)
	binary.BigEndian.PutUint16(message[6:8], seq)
	copy(message[8:], payload)
	binary.BigEndian.PutUint16(message[2:4], vpnICMPChecksum(message))
	return vpnIPv4Packet(vpnTestProtocolICMP, netip.MustParseAddr(src).AsSlice(), netip.MustParseAddr(dst).AsSlice(), message)
}

// vpnICMPChecksum is the RFC 792 sum, computed with the checksum field zeroed.
func vpnICMPChecksum(message []byte) uint16 {
	zeroed := append([]byte(nil), message...)
	zeroed[2] = 0
	zeroed[3] = 0
	return vpnIPv4HeaderChecksum(zeroed)
}

func TestVPNWireParsesTCP(t *testing.T) {
	packet := vpnWireTCP("10.64.0.7", "192.168.1.20", 41000, 8080, 0x02, []byte("hello"))

	parsed, err := parseVPNWirePacket(packet)
	if err != nil {
		t.Fatalf("parseVPNWirePacket() error = %v, want nil", err)
	}
	if parsed.Version != 4 {
		t.Errorf("Version = %d, want 4", parsed.Version)
	}
	if parsed.Protocol != vpnTestProtocolTCP {
		t.Errorf("Protocol = %d, want %d", parsed.Protocol, vpnTestProtocolTCP)
	}
	if parsed.Src != netip.MustParseAddr("10.64.0.7") {
		t.Errorf("Src = %v, want 10.64.0.7", parsed.Src)
	}
	if parsed.Dst != netip.MustParseAddr("192.168.1.20") {
		t.Errorf("Dst = %v, want 192.168.1.20", parsed.Dst)
	}
	if parsed.SrcPort != 41000 || parsed.DstPort != 8080 {
		t.Errorf("ports = %d/%d, want 41000/8080", parsed.SrcPort, parsed.DstPort)
	}
	if parsed.Fragment {
		t.Error("Fragment = true, want false for an unfragmented packet")
	}
	if parsed.Malformed {
		t.Error("Malformed = true, want false for a well-formed packet")
	}
	if parsed.Size != len(packet) {
		t.Errorf("Size = %d, want %d", parsed.Size, len(packet))
	}
	if string(parsed.Payload) != "hello" {
		t.Errorf("Payload = %q, want %q", parsed.Payload, "hello")
	}
	if !parsed.isTCPSYN() {
		t.Error("isTCPSYN() = false, want true for a bare SYN")
	}
}

func TestVPNWireParsesUDP(t *testing.T) {
	packet := vpnWireUDP("10.64.0.9", "172.16.4.5", 51000, 53, []byte("query"))

	parsed, err := parseVPNWirePacket(packet)
	if err != nil {
		t.Fatalf("parseVPNWirePacket() error = %v, want nil", err)
	}
	if parsed.Protocol != vpnTestProtocolUDP {
		t.Errorf("Protocol = %d, want %d", parsed.Protocol, vpnTestProtocolUDP)
	}
	if parsed.SrcPort != 51000 || parsed.DstPort != 53 {
		t.Errorf("ports = %d/%d, want 51000/53", parsed.SrcPort, parsed.DstPort)
	}
	if string(parsed.Payload) != "query" {
		t.Errorf("Payload = %q, want %q", parsed.Payload, "query")
	}
	if parsed.Malformed {
		t.Error("Malformed = true, want false when the length field agrees")
	}
}

func TestVPNWireUDPLengthFieldDisagreementUsesTheActualBytes(t *testing.T) {
	packet := vpnWireUDP("10.64.0.9", "172.16.4.5", 51000, 53, []byte("query"))
	// Claim a length that covers four payload bytes more than were sent. A parser
	// that trusted the field would read past the end of the buffer.
	binary.BigEndian.PutUint16(packet[20+4:20+6], uint16(len(packet)-20+4))

	parsed, err := parseVPNWirePacket(packet)
	if err != nil {
		t.Fatalf("parseVPNWirePacket() error = %v, want nil", err)
	}
	if !parsed.Malformed {
		t.Error("Malformed = false, want true when the udp length field disagrees")
	}
	if string(parsed.Payload) != "query" {
		t.Errorf("Payload = %q, want the bytes that were actually received", parsed.Payload)
	}
}

func TestVPNWireUDPTruncatedHeaderIsRefused(t *testing.T) {
	packet := vpnWireUDP("10.64.0.9", "172.16.4.5", 51000, 53, []byte("query"))
	truncated := packet[:len(packet)-6] // leaves three of the eight header bytes

	if _, err := parseVPNWirePacket(truncated); !errors.Is(err, errVPNWireTruncatedTransport) {
		t.Fatalf("error = %v, want %v", err, errVPNWireTruncatedTransport)
	}
}

func TestVPNWireDistinguishesICMPEchoFromOtherTypes(t *testing.T) {
	request := vpnWireICMP("10.64.0.11", "192.168.9.9", 8, 0, 4242, 7, []byte("ping"))
	parsed, err := parseVPNWirePacket(request)
	if err != nil {
		t.Fatalf("parseVPNWirePacket(echo request) error = %v, want nil", err)
	}
	if parsed.ICMPType != 8 || parsed.ICMPCode != 0 {
		t.Errorf("icmp type/code = %d/%d, want 8/0", parsed.ICMPType, parsed.ICMPCode)
	}
	if parsed.ICMPID != 4242 || parsed.ICMPSeq != 7 {
		t.Errorf("icmp id/seq = %d/%d, want 4242/7", parsed.ICMPID, parsed.ICMPSeq)
	}
	if string(parsed.Payload) != "ping" {
		t.Errorf("Payload = %q, want %q", parsed.Payload, "ping")
	}
	if !parsed.isICMPEchoRequest() {
		t.Error("isICMPEchoRequest() = false, want true")
	}
	if parsed.isICMPEchoReply() {
		t.Error("isICMPEchoReply() = true, want false for a request")
	}

	reply := vpnWireICMP("192.168.9.9", "10.64.0.11", 0, 0, 4242, 7, []byte("pong"))
	parsedReply, err := parseVPNWirePacket(reply)
	if err != nil {
		t.Fatalf("parseVPNWirePacket(echo reply) error = %v, want nil", err)
	}
	if !parsedReply.isICMPEchoReply() {
		t.Error("isICMPEchoReply() = false, want true")
	}
	if parsedReply.isICMPEchoRequest() {
		t.Error("isICMPEchoRequest() = true, want false for a reply")
	}

	// TTL exceeded: a real ICMP message the gateway must never answer or relay.
	exceeded := vpnWireICMP("10.64.0.11", "192.168.9.9", 0x0b, 0, 0, 0, nil)
	parsedExceeded, err := parseVPNWirePacket(exceeded)
	if err != nil {
		t.Fatalf("parseVPNWirePacket(ttl exceeded) error = %v, want nil", err)
	}
	if parsedExceeded.ICMPType != 0x0b {
		t.Errorf("ICMPType = %d, want 11", parsedExceeded.ICMPType)
	}
	if parsedExceeded.isICMPEchoRequest() || parsedExceeded.isICMPEchoReply() {
		t.Error("a ttl-exceeded message was reported as an echo")
	}
}

func TestVPNWireICMPTruncatedHeaderIsRefused(t *testing.T) {
	packet := vpnWireICMP("10.64.0.11", "192.168.9.9", 8, 0, 1, 2, []byte("ping"))
	truncated := packet[:20+5]

	if _, err := parseVPNWirePacket(truncated); !errors.Is(err, errVPNWireTruncatedTransport) {
		t.Fatalf("error = %v, want %v", err, errVPNWireTruncatedTransport)
	}
}

func TestVPNWireSkipsIPv4Options(t *testing.T) {
	payload := []byte("segment")
	segment := make([]byte, 24+len(payload))
	binary.BigEndian.PutUint16(segment[0:2], 41000)
	binary.BigEndian.PutUint16(segment[2:4], 9000)
	binary.BigEndian.PutUint32(segment[4:8], 7)
	segment[12] = 6 << 4 // data offset: six words, one option word present
	segment[13] = 0x18   // PSH+ACK
	copy(segment[24:], payload)

	packet := make([]byte, 24+len(segment))
	packet[0] = 4<<4 | 6 // IHL 6: one word of options
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64
	packet[9] = vpnTestProtocolTCP
	// One maximum-segment-size option fills the extra word.
	copy(packet[20:], []byte{0x02, 0x04, 0x05, 0xb4})
	binary.BigEndian.PutUint16(packet[10:12], vpnIPv4HeaderChecksum(packet[:24]))
	copy(packet[24:], segment)

	parsed, err := parseVPNWirePacket(packet)
	if err != nil {
		t.Fatalf("parseVPNWirePacket() error = %v, want nil", err)
	}
	if parsed.SrcPort != 41000 || parsed.DstPort != 9000 {
		t.Errorf("ports = %d/%d, want 41000/9000 after skipping the option word", parsed.SrcPort, parsed.DstPort)
	}
	if string(parsed.Payload) != string(payload) {
		t.Errorf("Payload = %q, want %q", parsed.Payload, payload)
	}
	if parsed.Size != len(packet) {
		t.Errorf("Size = %d, want %d", parsed.Size, len(packet))
	}
}

func TestVPNWireMarksFragments(t *testing.T) {
	base := vpnWireTCP("10.64.0.7", "192.168.1.20", 41000, 8080, 0x02, []byte("hello"))

	moreFragments := append([]byte(nil), base...)
	binary.BigEndian.PutUint16(moreFragments[6:8], 0x2000) // MF set
	parsed, err := parseVPNWirePacket(moreFragments)
	if err != nil {
		t.Fatalf("parseVPNWirePacket(MF) error = %v, want nil", err)
	}
	if !parsed.Fragment {
		t.Error("Fragment = false, want true when the more-fragments flag is set")
	}

	offset := append([]byte(nil), base...)
	binary.BigEndian.PutUint16(offset[6:8], 185) // non-zero fragment offset
	parsedOffset, err := parseVPNWirePacket(offset)
	if err != nil {
		t.Fatalf("parseVPNWirePacket(offset) error = %v, want nil", err)
	}
	if !parsedOffset.Fragment {
		t.Error("Fragment = false, want true for a non-first fragment")
	}
}

func TestVPNWireRefusesNonIPv4AndShortPackets(t *testing.T) {
	packet := vpnWireTCP("10.64.0.7", "192.168.1.20", 41000, 8080, 0x02, nil)

	if _, err := parseVPNWirePacket(vpnIPv6VersionOf(packet)); !errors.Is(err, errVPNWireNotIPv4) {
		t.Errorf("ipv6 error = %v, want %v", err, errVPNWireNotIPv4)
	}
	if _, err := parseVPNWirePacket(packet[:12]); !errors.Is(err, errVPNWireShortPacket) {
		t.Errorf("short error = %v, want %v", err, errVPNWireShortPacket)
	}
	if _, err := parseVPNWirePacket(nil); !errors.Is(err, errVPNWireShortPacket) {
		t.Errorf("empty error = %v, want %v", err, errVPNWireShortPacket)
	}
	// A header that claims more option words than were sent is short, not
	// truncated at the transport layer: nothing was ever read past the IP header.
	lying := append([]byte(nil), packet...)
	lying[0] = 4<<4 | 15
	if _, err := parseVPNWirePacket(lying); !errors.Is(err, errVPNWireShortPacket) {
		t.Errorf("lying ihl error = %v, want %v", err, errVPNWireShortPacket)
	}
}

func TestVPNWireRefusesATruncatedTCPHeader(t *testing.T) {
	packet := vpnWireTCP("10.64.0.7", "192.168.1.20", 41000, 8080, 0x02, []byte("hello"))

	if _, err := parseVPNWirePacket(packet[:20+8]); !errors.Is(err, errVPNWireTruncatedTransport) {
		t.Fatalf("error = %v, want %v", err, errVPNWireTruncatedTransport)
	}
}

func TestVPNWireTotalLengthFieldShorterThanTheBytes(t *testing.T) {
	packet := vpnWireTCP("10.64.0.7", "192.168.1.20", 41000, 8080, 0x18, []byte("hello"))
	// Ethernet padding is legal: a frame may carry more bytes than the IP header
	// accounts for. The parser must trust the header field and ignore the padding.
	padded := append(packet, 0, 0, 0, 0)

	parsed, err := parseVPNWirePacket(padded)
	if err != nil {
		t.Fatalf("parseVPNWirePacket() error = %v, want nil", err)
	}
	if parsed.Size != len(packet) {
		t.Errorf("Size = %d, want %d from the total-length field", parsed.Size, len(packet))
	}
	if string(parsed.Payload) != "hello" {
		t.Errorf("Payload = %q, want %q without the padding", parsed.Payload, "hello")
	}
	if parsed.Malformed {
		t.Error("Malformed = true, want false: padding is not a malformed packet")
	}
}

func TestVPNWireTCPDataOffsetSmallerThanTheMinimum(t *testing.T) {
	packet := vpnWireTCP("10.64.0.7", "192.168.1.20", 41000, 8080, 0x02, []byte("hello"))
	packet[20+12] = 3 << 4 // claims a 12-byte header, which cannot hold the ports

	if _, err := parseVPNWirePacket(packet); !errors.Is(err, errVPNWireTruncatedTransport) {
		t.Fatalf("error = %v, want %v", err, errVPNWireTruncatedTransport)
	}
}

func TestVPNWirePolicyPacketProjectsTheHeader(t *testing.T) {
	packet := vpnWireUDP("10.64.0.9", "172.16.4.5", 51000, 53, []byte("query"))

	parsed, err := parseVPNWirePacket(packet)
	if err != nil {
		t.Fatalf("parseVPNWirePacket() error = %v, want nil", err)
	}
	decision := parsed.policyPacket()
	if decision.Protocol != vpnTestProtocolUDP {
		t.Errorf("Protocol = %d, want %d", decision.Protocol, vpnTestProtocolUDP)
	}
	if decision.Dest.String() != "172.16.4.5" {
		t.Errorf("Dest = %v, want 172.16.4.5", decision.Dest)
	}
	if decision.Source.String() != "10.64.0.9" {
		t.Errorf("Source = %v, want 10.64.0.9", decision.Source)
	}
	if decision.Port != 53 {
		t.Errorf("Port = %d, want 53", decision.Port)
	}
	if decision.Size != len(packet) {
		t.Errorf("Size = %d, want %d", decision.Size, len(packet))
	}
	if decision.Fragment {
		t.Error("Fragment = true, want false")
	}
}
