// Probe: can the VPN gateway relay UDP to/from an ARBITRARY internal address
// without netstack UDP termination? Proves both directions:
//
//	inbound  - SetTransportProtocolHandler(udp) receives the datagram even
//	           though the stack owns no address for it, and returning true
//	           consumes it so no ICMP port-unreachable is generated.
//	outbound - a hand-built reply pushed through the link endpoint's
//	           WritePackets appears on the same queue wireguard-go drains,
//	           so it reaches the peer without any socket or route setup.
package main

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

const (
	nicID     = tcpip.NICID(1)
	mtu       = 1420
	clientIP  = "192.0.2.10"
	clientPt  = uint16(40000)
	serverIP  = "198.51.100.7"
	serverPt  = uint16(53)
	reqBody   = "tunnelmesh-udp-probe"
	replyBody = "dns-answer"
)

var (
	mu           sync.Mutex
	handlerCalls int
	captured     []string
)

func main() {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{udp.NewProtocol},
	})
	ep := channel.New(256, mtu, "")
	must("CreateNIC", s.CreateNIC(nicID, ep))
	must("SetPromiscuousMode", s.SetPromiscuousMode(nicID, true))
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})

	s.SetTransportProtocolHandler(udp.ProtocolNumber, func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
		mu.Lock()
		handlerCalls++
		mu.Unlock()
		uh := header.UDP(pkt.TransportHeader().View().AsSlice())
		body := pkt.Data().AsRange().ToSlice()
		mu.Lock()
		captured = append(captured, fmt.Sprintf("%s:%d -> %s:%d len=%d",
			id.RemoteAddress, uh.SourcePort(), id.LocalAddress, uh.DestinationPort(), len(body)))
		mu.Unlock()
		// Relay the datagram to the internal service, then emit the answer back
		// towards the peer through the link endpoint.
		go emitReply(ep, id, replyBody)
		// Returning true tells the stack the datagram is fully handled, so it
		// must not fall through to HandleUnknownDestinationPacket and send an
		// ICMP port-unreachable back to the user.
		return true
	})

	ep.InjectInbound(ipv4.ProtocolNumber, buildUDP(reqBody))

	select {
	case raw := <-egressOf(ep, 2*time.Second):
		// egressOf closes its channel on timeout, which delivers a nil slice to
		// this case rather than falling through to the outer time.After. Guard it
		// explicitly: a nil raw would panic inside header.IPv4 and replace the
		// intended diagnostic with a goroutine dump -- and the empty-egress case
		// is precisely the regression this probe exists to catch.
		if len(raw) == 0 {
			mu.Lock()
			fmt.Printf("FAIL: no reply egressed; handlerCalls=%d captured=%s\n", handlerCalls, summarizeCaptured())
			mu.Unlock()
			os.Exit(1)
		}
		ip := header.IPv4(raw)
		uh := header.UDP(raw[ip.HeaderLength():])
		body := raw[ip.HeaderLength()+header.UDPMinimumSize:]
		mu.Lock()
		calls, cap0 := handlerCalls, captured
		mu.Unlock()
		if len(cap0) != 1 {
			fail(fmt.Sprintf("captured %d datagrams, want 1: %v", len(cap0), cap0))
		}
		if string(body) != replyBody {
			fail(fmt.Sprintf("reply body %q, want %q", body, replyBody))
		}
		if ip.SourceAddress().String() != serverIP || ip.DestinationAddress().String() != clientIP {
			fail(fmt.Sprintf("reply tuple %s -> %s", ip.SourceAddress(), ip.DestinationAddress()))
		}
		if uh.SourcePort() != serverPt || uh.DestinationPort() != clientPt {
			fail(fmt.Sprintf("reply ports %d -> %d", uh.SourcePort(), uh.DestinationPort()))
		}
		// The reply itself must be UDP, not an ICMP error.
		if ip.TransportProtocol() != udp.ProtocolNumber {
			fail(fmt.Sprintf("egress packet is IP protocol %d, want %d (UDP)",
				ip.TransportProtocol(), udp.ProtocolNumber))
		}
		// Checking only the first dequeued packet cannot prove the ABSENCE of a
		// second one: the reply is written from a goroutine, so ordering against a
		// hypothetical ICMP is not guaranteed. Drain for a further window and fail
		// on anything else, which is what actually backs the "no ICMP
		// port-unreachable" claim -- returning true from the handler must keep the
		// datagram out of HandleUnknownDestinationPacket entirely.
		if extra := drainFor(ep, 300*time.Millisecond); len(extra) > 0 {
			for _, pkt := range extra {
				if header.IPv4(pkt).TransportProtocol() == icmpv4ProtocolNumber {
					fail(fmt.Sprintf("stack emitted an ICMP reply (%d bytes) despite the handler consuming the datagram", len(pkt)))
				}
			}
			fail(fmt.Sprintf("%d unexpected extra egress packet(s) after the reply", len(extra)))
		}
		fmt.Printf("PASS: netstack consumed UDP %s and emitted the reply (%d bytes, IP proto %d) towards the peer; nothing else egressed in the following 300ms, so no ICMP port-unreachable was generated\n",
			cap0[0], len(raw), ip.TransportProtocol())
		fmt.Printf("      transport handler invoked %d time(s); no socket, route or address setup needed\n", calls)
		return
	case <-time.After(3 * time.Second):
		mu.Lock()
		fmt.Printf("FAIL: no reply egressed; handlerCalls=%d captured=%s\n", handlerCalls, summarizeCaptured())
		mu.Unlock()
		os.Exit(1)
	}
}

// summarizeCaptured renders the handler capture log for a FAIL diagnostic. A
// regression can make the handler re-enter itself indefinitely (injecting the
// reply back inbound re-delivers it to this same handler), so the slice is
// unbounded; print a short prefix plus the total rather than 100k entries.
// The caller must hold mu.
func summarizeCaptured() string {
	const maxEntries = 3
	if len(captured) <= maxEntries {
		return fmt.Sprintf("%v", captured)
	}
	return fmt.Sprintf("%v ... (%d entries total)", captured[:maxEntries], len(captured))
}

// icmpv4ProtocolNumber is IP protocol 1, the number an ICMP port-unreachable
// would carry. Spelled out rather than pulled from a gVisor constant so the
// assertion cannot silently track a renamed symbol.
const icmpv4ProtocolNumber = 1

// drainFor collects every packet that reaches the outbound queue within the
// window, so the caller can assert that nothing unexpected followed the reply.
func drainFor(ep *channel.Endpoint, window time.Duration) [][]byte {
	var out [][]byte
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		pkt := ep.Read()
		if pkt == nil {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		raw := pkt.ToView().AsSlice()
		pkt.DecRef()
		out = append(out, raw)
	}
	return out
}

// egressOf returns the raw bytes of the next packet the stack (or we) put on
// the link endpoint's outbound queue -- exactly what wireguard-go would read
// from the memory TUN and encrypt.
func egressOf(ep *channel.Endpoint, timeout time.Duration) <-chan []byte {
	out := make(chan []byte, 1)
	go func() {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if pkt := ep.Read(); pkt != nil {
				raw := pkt.ToView().AsSlice()
				pkt.DecRef()
				out <- raw
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		close(out)
	}()
	return out
}

// emitReply builds the answer datagram and pushes it straight onto the link
// endpoint's outbound queue. The gateway owns this queue in both directions, so
// no netstack UDP socket, source address or route is required.
func emitReply(ep *channel.Endpoint, id stack.TransportEndpointID, body string) {
	raw := buildRawUDP(id.LocalAddress, id.LocalPort, id.RemoteAddress, id.RemotePort, body)
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(raw),
	})
	pkt.NetworkProtocolNumber = header.IPv4ProtocolNumber
	var list stack.PacketBufferList
	list.PushBack(pkt)
	if _, terr := ep.WritePackets(list); terr != nil {
		fail(fmt.Sprintf("WritePackets -> %v", terr))
	}
}

// buildRawUDP assembles a complete raw IPv4+UDP datagram with valid checksums.
func buildRawUDP(srcAddr tcpip.Address, srcPort uint16, dstAddr tcpip.Address, dstPort uint16, body string) []byte {
	total := header.IPv4MinimumSize + header.UDPMinimumSize + len(body)
	raw := make([]byte, total)
	ip := header.IPv4(raw)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(total),
		TTL:         64,
		Protocol:    uint8(udp.ProtocolNumber),
		SrcAddr:     srcAddr,
		DstAddr:     dstAddr,
	})
	ip.SetChecksum(^ip.CalculateChecksum())
	uh := header.UDP(raw[header.IPv4MinimumSize:])
	uh.Encode(&header.UDPFields{
		SrcPort: srcPort,
		DstPort: dstPort,
		Length:  uint16(header.UDPMinimumSize + len(body)),
	})
	copy(raw[header.IPv4MinimumSize+header.UDPMinimumSize:], body)
	uh.SetChecksum(^uh.CalculateChecksum(header.PseudoHeaderChecksum(
		udp.ProtocolNumber, srcAddr, dstAddr,
		uint16(header.UDPMinimumSize+len(body)))))
	return raw
}

func buildUDP(body string) *stack.PacketBuffer {
	srcAddr := tcpip.AddrFrom4Slice(net.ParseIP(clientIP).To4())
	dstAddr := tcpip.AddrFrom4Slice(net.ParseIP(serverIP).To4())
	raw := buildRawUDP(srcAddr, clientPt, dstAddr, serverPt, body)
	// Passed as Payload, never pushed into NetworkHeader: the stack's parse.IPv4
	// pulls the header out of Data() itself.
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(raw),
	})
	pkt.NetworkProtocolNumber = header.IPv4ProtocolNumber
	return pkt
}

func fail(msg string) {
	fmt.Println("FAIL:", msg)
	os.Exit(1)
}

func must(stage string, err tcpip.Error) {
	if err != nil {
		fmt.Printf("FAIL %s: %v\n", stage, err)
		os.Exit(1)
	}
}
