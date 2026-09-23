//go:build vpn

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// The addresses are the documentation ones: 192.0.2.0/24 is a client on the
// tunnel and 198.51.100.7 is an internal service the gateway does not own an
// address for, which is the whole difficulty the stack has to solve.
const (
	vpnStackClientIP   = "192.0.2.10"
	vpnStackServiceIP  = "198.51.100.7"
	vpnStackClientISN  = uint32(1000)
	vpnStackDrainWait  = 300 * time.Millisecond
	vpnStackAcceptWait = 5 * time.Second
)

// countingVPNMetrics records what the stack reports instead of publishing it, so
// a test can assert which counter a failure touched.
type countingVPNMetrics struct {
	mu      sync.Mutex
	bytes   []string
	streams []string
	dropped []string
	leases  []string
}

func (m *countingVPNMetrics) Bytes(direction, protocol string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bytes = append(m.bytes, fmt.Sprintf("%s/%s/%d", direction, protocol, n))
}

func (m *countingVPNMetrics) Stream(protocol, result, errorClass string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streams = append(m.streams, fmt.Sprintf("%s/%s/%s", protocol, result, errorClass))
}

func (m *countingVPNMetrics) Dropped(protocol string, class vpn.ErrorClass) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropped = append(m.dropped, fmt.Sprintf("%s/%s", protocol, class))
}

func (m *countingVPNMetrics) Lease(operation, result, errorClass string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leases = append(m.leases, fmt.Sprintf("%s/%s/%s", operation, result, errorClass))
}

func (m *countingVPNMetrics) droppedRecords() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.dropped...)
}

func (m *countingVPNMetrics) countDropped(class vpn.ErrorClass) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	found := 0
	for _, record := range m.dropped {
		if record == "tcp/"+string(class) {
			found++
		}
	}
	return found
}

// vpnTCPSegment builds one TCP segment with a correct checksum, which gvisor
// verifies on the way in: a segment with a bad checksum is counted as invalid and
// silently dropped, so a test that forgot it would fail for the wrong reason.
func vpnTCPSegment(src, dst net.IP, srcPort, dstPort uint16, seq, ack uint32, flags header.TCPFlags, payload []byte) []byte {
	srcAddr := tcpip.AddrFrom4Slice(src.To4())
	dstAddr := tcpip.AddrFrom4Slice(dst.To4())
	segment := make([]byte, header.TCPMinimumSize+len(payload))
	tcpHeader := header.TCP(segment)
	tcpHeader.Encode(&header.TCPFields{
		SrcPort: srcPort, DstPort: dstPort,
		SeqNum: seq, AckNum: ack,
		DataOffset: header.TCPMinimumSize,
		Flags:      flags,
		WindowSize: 64240,
	})
	copy(segment[header.TCPMinimumSize:], payload)
	tcpHeader.SetChecksum(^tcpHeader.CalculateChecksum(header.PseudoHeaderChecksum(
		tcp.ProtocolNumber, srcAddr, dstAddr, uint16(len(segment)))))
	return segment
}

func vpnTCPPacket(src, dst net.IP, srcPort, dstPort uint16, seq, ack uint32, flags header.TCPFlags, payload []byte) []byte {
	return vpnIPv4Packet(vpnTestProtocolTCP, src, dst, vpnTCPSegment(src, dst, srcPort, dstPort, seq, ack, flags, payload))
}

// vpnOutPacket is one packet the stack or the gateway put on the wire towards the
// peer.
type vpnOutPacket struct{ raw []byte }

func (p vpnOutPacket) ip() header.IPv4 { return header.IPv4(p.raw) }

func (p vpnOutPacket) transport() tcpip.TransportProtocolNumber { return p.ip().TransportProtocol() }

func (p vpnOutPacket) tcp() header.TCP {
	if p.transport() != tcp.ProtocolNumber || len(p.raw) < int(p.ip().HeaderLength())+header.TCPMinimumSize {
		return nil
	}
	return header.TCP(p.raw[p.ip().HeaderLength():])
}

func (p vpnOutPacket) describe() string {
	if segment := p.tcp(); segment != nil {
		return fmt.Sprintf("%s:%d->%s:%d proto=tcp flags=0x%02x seq=%d ack=%d",
			p.ip().SourceAddress(), segment.SourcePort(), p.ip().DestinationAddress(), segment.DestinationPort(),
			uint8(segment.Flags()), segment.SequenceNumber(), segment.AckNumber())
	}
	return fmt.Sprintf("%s->%s proto=%d len=%d", p.ip().SourceAddress(), p.ip().DestinationAddress(), p.transport(), len(p.raw))
}

// vpnReadOutbound waits for one packet and returns false when the wait expires.
//
// Waiting on the device rather than polling it is what keeps the positive cases
// fast: a handshake that completes in a millisecond must not cost the whole
// timeout, or a suite that proves five properties takes five timeouts to run.
func vpnReadOutbound(device *memoryDevice, wait time.Duration) (vpnOutPacket, bool) {
	if wait <= 0 {
		return vpnOutPacket{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	buffer := make([]byte, vpnDeviceMessageSize)
	sizes := []int{0}
	n, err := device.ReadContext(ctx, [][]byte{buffer}, sizes, vpnDeviceTransportOffset)
	if err != nil || n != 1 {
		return vpnOutPacket{}, false
	}
	return vpnOutPacket{raw: append([]byte(nil), buffer[vpnDeviceTransportOffset:vpnDeviceTransportOffset+sizes[0]]...)}, true
}

// vpnDrainOutbound collects everything the device emits inside a window.
//
// It is the only honest way to assert that the stack did NOT emit something: a
// single read that finds nothing proves nothing about the next millisecond, so the
// whole window is consumed and every packet in it is inspected.
func vpnDrainOutbound(device *memoryDevice, window time.Duration) []vpnOutPacket {
	deadline := time.Now().Add(window)
	var packets []vpnOutPacket
	for {
		packet, ok := vpnReadOutbound(device, time.Until(deadline))
		if !ok {
			return packets
		}
		packets = append(packets, packet)
	}
}

// vpnWaitOutbound returns the first emitted packet that matches, discarding
// anything else the stack produced on the way.
func vpnWaitOutbound(t *testing.T, device *memoryDevice, timeout time.Duration, match func(vpnOutPacket) bool) (vpnOutPacket, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		packet, ok := vpnReadOutbound(device, time.Until(deadline))
		if !ok {
			return vpnOutPacket{}, false
		}
		if match(packet) {
			return packet, true
		}
	}
}

func newVPNStackFixture(t *testing.T) (*vpnStack, *memoryDevice, *countingVPNMetrics) {
	t.Helper()
	device, handler := newVPNDeviceFixture(t, 1420, 256)
	if handler == nil {
		t.Fatal("the device fixture returned no handler")
	}
	metrics := &countingVPNMetrics{}
	assembled, err := newVPNStack(device, metrics)
	if err != nil {
		t.Fatalf("newVPNStack: %v", err)
	}
	t.Cleanup(func() { _ = assembled.close() })
	return assembled, device, metrics
}

func vpnStackTarget(t *testing.T, port uint16) netip.AddrPort {
	t.Helper()
	address, err := netip.ParseAddr(vpnStackServiceIP)
	if err != nil {
		t.Fatalf("parse the service address: %v", err)
	}
	return netip.AddrPortFrom(address, port)
}

// handshakeTo drives a three-way handshake against a registered listener and
// returns the SYN-ACK the stack produced.
func handshakeTo(t *testing.T, assembled *vpnStack, device *memoryDevice, target netip.AddrPort, clientPort uint16, expectReply bool) vpnOutPacket {
	t.Helper()
	client := net.ParseIP(vpnStackClientIP)
	service := net.IP(target.Addr().AsSlice())
	packet := vpnTCPPacket(client, service, clientPort, target.Port(), vpnStackClientISN, 0, header.TCPFlagSyn, nil)
	if err := device.injectTCP(packet); err != nil {
		t.Fatalf("inject the SYN: %v", err)
	}
	if !expectReply {
		for _, emitted := range vpnDrainOutbound(device, vpnStackDrainWait) {
			if segment := emitted.tcp(); segment != nil && segment.Flags()&header.TCPFlagSyn != 0 {
				t.Fatalf("the stack answered a SYN it should have ignored: %s", emitted.describe())
			}
		}
		return vpnOutPacket{}
	}
	reply, ok := vpnWaitOutbound(t, device, vpnStackAcceptWait, func(candidate vpnOutPacket) bool {
		segment := candidate.tcp()
		return segment != nil && segment.Flags()&header.TCPFlagSyn != 0 && segment.Flags()&header.TCPFlagAck != 0
	})
	if !ok {
		t.Fatalf("no SYN-ACK for %s:%d within %s", target, clientPort, vpnStackAcceptWait)
	}
	if got := reply.ip().SourceAddress().String(); got != target.Addr().String() {
		t.Errorf("the SYN-ACK claims to come from %s, want %s", got, target.Addr())
	}
	if got := reply.ip().DestinationAddress().String(); got != vpnStackClientIP {
		t.Errorf("the SYN-ACK goes to %s, want %s", got, vpnStackClientIP)
	}
	if got := reply.tcp().AckNumber(); got != vpnStackClientISN+1 {
		t.Errorf("the SYN-ACK acknowledges %d, want %d", got, vpnStackClientISN+1)
	}
	// Completing the handshake is what makes Accept return, so a test that only
	// wanted the SYN-ACK has already proved its point by the time this is sent.
	_ = assembled
	final := vpnTCPPacket(client, service, clientPort, target.Port(), vpnStackClientISN+1, reply.tcp().SequenceNumber()+1, header.TCPFlagAck, nil)
	if err := device.injectTCP(final); err != nil {
		t.Fatalf("inject the final ACK: %v", err)
	}
	return reply
}

// TestVPNStackTerminatesTCPForAnAddressItDoesNotOwn is the assumption the whole
// gateway rests on: netstack will complete a handshake for an internal address it
// has never heard of and hand back a net.Conn, with no kernel interface and no
// CAP_NET_ADMIN anywhere in the process.
func TestVPNStackTerminatesTCPForAnAddressItDoesNotOwn(t *testing.T) {
	assembled, device, _ := newVPNStackFixture(t)
	target := vpnStackTarget(t, 8080)

	listener, err := assembled.registerTCP(target)
	if err != nil {
		t.Fatalf("registerTCP: %v", err)
	}
	type acceptResult struct {
		local, remote string
		err           error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- acceptResult{err: err}
			return
		}
		defer conn.Close()
		accepted <- acceptResult{local: conn.LocalAddr().String(), remote: conn.RemoteAddr().String()}
	}()

	handshakeTo(t, assembled, device, target, 40000, true)

	select {
	case result := <-accepted:
		if result.err != nil {
			t.Fatalf("Accept: %v", result.err)
		}
		if result.remote != vpnStackClientIP+":40000" {
			t.Errorf("the accepted connection reports the peer as %q", result.remote)
		}
		if result.local != vpnStackServiceIP+":8080" {
			t.Errorf("the accepted connection reports the service as %q", result.local)
		}
	case <-time.After(vpnStackAcceptWait):
		t.Fatal("Accept did not return after the handshake completed")
	}
	if got := assembled.unmatchedTCP(); got != 0 {
		t.Errorf("the stack saw %d unmatched tcp segments, want 0 once a listener is registered before the SYN", got)
	}
}

// TestVPNStackRegistersBeforeTheSYNArrives pins D4. The listener exists before
// the segment is injected, so the demux always hits and the first SYN is never
// dropped - which is what removes the one-client-RTO penalty the design spec
// listed as an accepted consequence.
func TestVPNStackRegistersBeforeTheSYNArrives(t *testing.T) {
	assembled, device, _ := newVPNStackFixture(t)
	target := vpnStackTarget(t, 9090)
	if _, err := assembled.registerTCP(target); err != nil {
		t.Fatalf("registerTCP: %v", err)
	}
	// A single SYN, injected once, with no retransmission to rescue it.
	client := net.ParseIP(vpnStackClientIP)
	service := net.IP(target.Addr().AsSlice())
	if err := device.injectTCP(vpnTCPPacket(client, service, 41000, target.Port(), vpnStackClientISN, 0, header.TCPFlagSyn, nil)); err != nil {
		t.Fatalf("inject the SYN: %v", err)
	}
	if _, ok := vpnWaitOutbound(t, device, vpnStackAcceptWait, func(candidate vpnOutPacket) bool {
		segment := candidate.tcp()
		return segment != nil && segment.Flags()&header.TCPFlagSyn != 0
	}); !ok {
		t.Fatal("the first SYN was dropped, so a client would wait a full RTO before its retransmission is answered")
	}
	if got := assembled.unmatchedTCP(); got != 0 {
		t.Errorf("unmatchedTCP = %d, want 0: the default handler must not be what serves a registered tuple", got)
	}
}

func TestVPNStackRegisterIsReferenceCounted(t *testing.T) {
	assembled, device, _ := newVPNStackFixture(t)
	target := vpnStackTarget(t, 8081)

	first, err := assembled.registerTCP(target)
	if err != nil {
		t.Fatalf("the first registerTCP: %v", err)
	}
	second, err := assembled.registerTCP(target)
	if err != nil {
		t.Fatalf("the second registerTCP: %v", err)
	}
	if first != second {
		t.Error("a second registration built a second listener, so two flows to one service would race for the backlog")
	}
	if got := assembled.listenerRefs(target); got != 2 {
		t.Errorf("refs = %d after two registrations, want 2", got)
	}

	// One release leaves the listener serving: the address is still claimed and a
	// fresh handshake still completes.
	assembled.releaseTCP(target)
	if got := assembled.listenerRefs(target); got != 1 {
		t.Errorf("refs = %d after one release, want 1", got)
	}
	if _, ok := assembled.lookupTCP(target); !ok {
		t.Fatal("the listener was removed while a reference was still held")
	}
	go func() {
		if conn, err := second.Accept(); err == nil {
			_ = conn.Close()
		}
	}()
	handshakeTo(t, assembled, device, target, 42000, true)

	// The last release closes the listener and gives the address back, which is
	// what stops the set of claimed addresses from growing without bound as peers
	// come and go.
	assembled.releaseTCP(target)
	if got := assembled.listenerRefs(target); got != 0 {
		t.Errorf("refs = %d after the final release, want 0", got)
	}
	if _, ok := assembled.lookupTCP(target); ok {
		t.Error("the listener is still registered after its last reference was released")
	}
	// Releasing a target that is not registered must be harmless: the flow reaper
	// and the shutdown path both call it, and neither knows whether the other got
	// there first.
	assembled.releaseTCP(target)
	handshakeTo(t, assembled, device, target, 43000, false)
}

// TestVPNStackConsumesSegmentsItDoesNotServe pins the safety net behind D4. When
// a segment still reaches the default handler, the gateway consumes it rather
// than letting the stack answer: an RST would confirm to a peer that the address
// exists and is reachable, which is information the egress policy may have
// deliberately withheld.
func TestVPNStackConsumesSegmentsItDoesNotServe(t *testing.T) {
	assembled, device, metrics := newVPNStackFixture(t)
	client := net.ParseIP(vpnStackClientIP)
	service := net.ParseIP(vpnStackServiceIP)

	// A bare ACK for a tuple nothing is listening on.
	stray := vpnTCPPacket(client, service, 44000, 8082, vpnStackClientISN, vpnStackClientISN+1, header.TCPFlagAck, nil)
	if err := device.injectTCP(stray); err != nil {
		t.Fatalf("inject the stray segment: %v", err)
	}
	for _, emitted := range vpnDrainOutbound(device, vpnStackDrainWait) {
		if emitted.transport() == header.ICMPv4ProtocolNumber {
			t.Errorf("the stack emitted an ICMP datagram the gateway never constructed: %s", emitted.describe())
		}
		if segment := emitted.tcp(); segment != nil && segment.Flags()&header.TCPFlagRst != 0 {
			t.Errorf("the stack emitted a RST for a tuple the policy did not serve: %s", emitted.describe())
		}
	}
	if got := assembled.unmatchedTCP(); got != 1 {
		t.Errorf("unmatchedTCP = %d, want 1", got)
	}
	if got := metrics.countDropped(vpn.ClassStackError); got != 1 {
		t.Errorf("stack_error was counted %d times, want 1 (%v)", got, metrics.droppedRecords())
	}
}

// TestVPNStackRefusesAnAddressAnotherOwnerHolds pins D5. A duplicate address means
// the registry and the stack disagree, which is an invariant violation rather than
// a runtime condition, and the remedy is emphatically not to remove the address:
// it belongs to whoever claimed it first, and taking it away would break a
// listener that is currently serving traffic.
func TestVPNStackRefusesAnAddressAnotherOwnerHolds(t *testing.T) {
	assembled, _, metrics := newVPNStackFixture(t)
	target := vpnStackTarget(t, 8083)
	address := tcpip.AddrFrom4Slice(target.Addr().AsSlice())

	claim := func() tcpip.Error {
		return assembled.netstack.AddProtocolAddress(vpnStackNICID, tcpip.ProtocolAddress{
			Protocol:          ipv4.ProtocolNumber,
			AddressWithPrefix: address.WithPrefix(),
		}, stack.AddressProperties{})
	}
	if terr := claim(); terr != nil {
		t.Fatalf("occupying the address first: %v", terr)
	}
	if _, err := assembled.registerTCP(target); !errors.Is(err, errVPNStackAddressConflict) {
		t.Fatalf("registerTCP = %v, want errVPNStackAddressConflict", err)
	}
	if got := metrics.countDropped(vpn.ClassStackError); got != 1 {
		t.Errorf("stack_error was counted %d times, want 1 (%v)", got, metrics.droppedRecords())
	}
	if _, ok := assembled.lookupTCP(target); ok {
		t.Error("a refused registration left a listener behind")
	}
	// The address is still claimed by its original owner. If registerTCP had
	// "cleaned up" after itself, this second claim would have succeeded.
	if _, duplicate := claim().(*tcpip.ErrDuplicateAddress); !duplicate {
		t.Error("registerTCP removed an address it does not own, breaking whoever claimed it first")
	}
}

func TestVPNStackCloseReleasesEveryListener(t *testing.T) {
	assembled, device, _ := newVPNStackFixture(t)
	target := vpnStackTarget(t, 8084)
	if _, err := assembled.registerTCP(target); err != nil {
		t.Fatalf("registerTCP: %v", err)
	}
	if err := assembled.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := assembled.close(); err != nil {
		t.Errorf("a second close = %v, want nil", err)
	}
	if got := assembled.listenerRefs(target); got != 0 {
		t.Errorf("%d listeners survived close", got)
	}
	if _, err := assembled.registerTCP(target); !errors.Is(err, errVPNStackClosed) {
		t.Errorf("registerTCP after close = %v, want errVPNStackClosed", err)
	}
	// Injecting into a closed stack must not panic: shutdown races with packets
	// that were already decrypted.
	client := net.ParseIP(vpnStackClientIP)
	if err := device.injectTCP(vpnTCPPacket(client, net.IP(target.Addr().AsSlice()), 45000, target.Port(), vpnStackClientISN, 0, header.TCPFlagSyn, nil)); err != nil {
		t.Fatalf("inject after close: %v", err)
	}
}
