// Probe: can gVisor netstack terminate a TCP connection whose destination is an
// ARBITRARY internal address the stack does not own, and hand us a net.Conn --
// with no /dev/net/tun and no CAP_NET_ADMIN? This is the highest-risk
// assumption of the embedded VPN gateway design.
package main

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

const (
	nicID     = tcpip.NICID(1)
	mtu       = 1420
	clientIP  = "192.0.2.10"
	clientPt  = uint16(40000)
	serverIP  = "198.51.100.7"
	serverPt  = uint16(8080)
	clientISN = uint32(1000)
)

var (
	accepted     = make(chan string, 1)
	mu           sync.Mutex
	handlerCalls int
	listenErrs   []string
)

func main() {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})

	ep := channel.New(256, mtu, "")
	must("CreateNIC", s.CreateNIC(nicID, ep))
	// Promiscuous mode is what lets the IPv4 layer deliver a frame whose
	// destination is not one of the stack's own addresses: AcquireAssignedAddress
	// is called with allowTemp=nic.Promiscuous(), so gVisor synthesises a
	// temporary address endpoint for the arbitrary destination instead of
	// counting InvalidDestinationAddressesReceived and dropping.
	must("SetPromiscuousMode", s.SetPromiscuousMode(nicID, true))
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})

	// The interception hook. gVisor only calls defaultHandler when the demux
	// table has no matching endpoint (stack/nic.go DeliverTransportPacket), so
	// seeing exactly one call per new tuple is the success signal: every later
	// segment of that connection is consumed by the listener itself.
	//
	// Two steps are required per new tuple, and this probe proves both:
	//  1. AddProtocolAddress for the arbitrary destination. Without it the stack
	//     has no source address for the SYN-ACK and silently emits nothing.
	//  2. gonet.ListenTCP bound to that exact address. Binding an address the
	//     stack does not own fails with tcpip.ErrBadLocalAddress.
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
		mu.Lock()
		handlerCalls++
		mu.Unlock()
		th := header.TCP(pkt.TransportHeader().View().AsSlice())
		flags := th.Flags()
		if flags&header.TCPFlagSyn == 0 || flags&header.TCPFlagAck != 0 {
			return false
		}
		go listen(s, id.LocalAddress, id.LocalPort)
		return false
	})

	// --- 3-way handshake, driven entirely by injected packets -----------------
	// The hook registers the listener from a goroutine, so the very first SYN is
	// demuxed before the endpoint exists and is dropped. A real client
	// retransmits, so re-injecting the same SYN models normal TCP behaviour
	// rather than hiding a race in the production design.
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				ep.InjectInbound(ipv4.ProtocolNumber, buildTCP(clientISN, 0, header.TCPFlagSyn))
			}
		}
	}()
	ep.InjectInbound(ipv4.ProtocolNumber, buildTCP(clientISN, 0, header.TCPFlagSyn))

	synAck, ok := waitOutbound(ep, 5*time.Second)
	if !ok {
		close(stop)
		st := s.Stats()
		fmt.Printf("      STATS ip: recv=%d valid=%d malformed=%d invalidDst=%d invalidSrc=%d preroutingDrop=%d\n",
			st.IP.PacketsReceived.Value(), st.IP.ValidPacketsReceived.Value(),
			st.IP.MalformedPacketsReceived.Value(), st.IP.InvalidDestinationAddressesReceived.Value(),
			st.IP.InvalidSourceAddressesReceived.Value(), st.IP.IPTablesPreroutingDropped.Value())
		fmt.Printf("      STATS ip.out: sent=%d outErrs=%d\n",
			st.IP.PacketsSent.Value(), st.IP.OutgoingPacketErrors.Value())
		fmt.Printf("      STATS tcp: invalidSegs=%d failedPortRes=%d estabResets=%d activeOpens=%d passiveOpens=%d currEstab=%d\n",
			st.TCP.InvalidSegmentsReceived.Value(), st.TCP.FailedPortReservations.Value(),
			st.TCP.EstablishedResets.Value(), st.TCP.ActiveConnectionOpenings.Value(),
			st.TCP.PassiveConnectionOpenings.Value(), st.TCP.CurrentEstablished.Value())
		fmt.Printf("      STATS nic: txDroppedNoBuf=%d\n", st.NICs.TxPacketsDroppedNoBufferSpace.Value())
		report("FAIL: no SYN-ACK egressed within 5s")
	}
	close(stop)
	// A RST-ACK would also carry ack=clientISN+1 and the same tuple, so check the
	// flags before calling this packet a SYN-ACK. It could never make Accept
	// return, but the intermediate OK line must not name a packet it did not verify.
	if synAck.tcp().Flags()&header.TCPFlagSyn == 0 {
		report(fmt.Sprintf("FAIL: reply flags 0x%02x are not SYN-ACK", uint8(synAck.tcp().Flags())))
	}
	if synAck.AckNumber() != clientISN+1 {
		report(fmt.Sprintf("FAIL: SYN-ACK ack=%d, want %d", synAck.AckNumber(), clientISN+1))
	}
	if !synAck.IsSource(serverIP, serverPt) || !synAck.IsDest(clientIP, clientPt) {
		report(fmt.Sprintf("FAIL: SYN-ACK tuple %s:%d -> %s:%d",
			synAck.Src(), synAck.SrcPort(), synAck.Dst(), synAck.DstPort()))
	}
	fmt.Printf("OK: stack answered SYN-ACK for unowned dst %s:%d from %s:%d\n",
		serverIP, serverPt, synAck.Src(), synAck.SrcPort())

	ep.InjectInbound(ipv4.ProtocolNumber,
		buildTCP(clientISN+1, synAck.SequenceNumber()+1, header.TCPFlagAck))

	select {
	case got := <-accepted:
		mu.Lock()
		calls := handlerCalls
		mu.Unlock()
		fmt.Printf("PASS: netstack terminated arbitrary-destination TCP after %d handler call(s); %s\n", calls, got)
		return
	case <-time.After(3 * time.Second):
		report("FAIL: Accept did not return within 3s after the final ACK")
	}
}

func listen(s *stack.Stack, addr tcpip.Address, port uint16) {
	if terr := s.AddProtocolAddress(nicID, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: addr.WithPrefix(),
	}, stack.AddressProperties{}); terr != nil {
		mu.Lock()
		listenErrs = append(listenErrs, fmt.Sprintf("AddProtocolAddress %s -> %v", addr, terr))
		mu.Unlock()
		return
	}
	l, err := gonet.ListenTCP(s, tcpip.FullAddress{Addr: addr, Port: port}, ipv4.ProtocolNumber)
	if err != nil {
		mu.Lock()
		listenErrs = append(listenErrs, fmt.Sprintf("bind %s:%d -> %v", addr, port, err))
		mu.Unlock()
		return
	}
	defer l.Close()
	c, err := l.Accept()
	if err != nil {
		report(fmt.Sprintf("FAIL Accept: %v", err))
		return
	}
	defer c.Close()
	accepted <- fmt.Sprintf("local=%s remote=%s", c.LocalAddr(), c.RemoteAddr())
}

// outPkt is a parsed egress IPv4/TCP packet captured from the link endpoint.
type outPkt struct {
	raw []byte
}

func (p outPkt) ip() header.IPv4        { return header.IPv4(p.raw) }
func (p outPkt) tcp() header.TCP        { return header.TCP(p.raw[p.ip().HeaderLength():]) }
func (p outPkt) Src() string            { return p.ip().SourceAddress().String() }
func (p outPkt) Dst() string            { return p.ip().DestinationAddress().String() }
func (p outPkt) SrcPort() uint16        { return p.tcp().SourcePort() }
func (p outPkt) DstPort() uint16        { return p.tcp().DestinationPort() }
func (p outPkt) SequenceNumber() uint32 { return p.tcp().SequenceNumber() }
func (p outPkt) AckNumber() uint32      { return p.tcp().AckNumber() }
func (p outPkt) IsSource(ip string, port uint16) bool {
	return p.Src() == ip && p.SrcPort() == port
}
func (p outPkt) IsDest(ip string, port uint16) bool {
	return p.Dst() == ip && p.DstPort() == port
}

// waitOutbound polls the channel endpoint for the next IPv4/TCP packet the stack
// emits towards the peer. In production this is exactly the packet the VPN
// gateway hands to wireguard-go for encryption.
func waitOutbound(ep *channel.Endpoint, timeout time.Duration) (outPkt, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pkt := ep.Read(); pkt != nil {
			raw := pkt.ToView().AsSlice()
			pkt.DecRef()
			if len(raw) >= header.IPv4MinimumSize+header.TCPMinimumSize {
				return outPkt{raw: raw}, true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return outPkt{}, false
}

// buildTCP assembles a complete raw IPv4+TCP segment. It is handed to the stack
// as Payload -- never pushed into NetworkHeader -- because parse.IPv4 pulls the
// header out of Data() itself; pushing directly leaves Data() empty and the
// packet is rejected as malformed.
func buildTCP(seq, ack uint32, flags header.TCPFlags) *stack.PacketBuffer {
	srcAddr := tcpip.AddrFrom4Slice(net.ParseIP(clientIP).To4())
	dstAddr := tcpip.AddrFrom4Slice(net.ParseIP(serverIP).To4())
	total := header.IPv4MinimumSize + header.TCPMinimumSize
	raw := make([]byte, total)
	ip := header.IPv4(raw)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(total),
		TTL:         64,
		Protocol:    uint8(tcp.ProtocolNumber),
		SrcAddr:     srcAddr,
		DstAddr:     dstAddr,
	})
	ip.SetChecksum(^ip.CalculateChecksum())
	th := header.TCP(raw[header.IPv4MinimumSize:])
	th.Encode(&header.TCPFields{
		SrcPort:    clientPt,
		DstPort:    serverPt,
		SeqNum:     seq,
		AckNum:     ack,
		DataOffset: header.TCPMinimumSize,
		Flags:      flags,
		WindowSize: 64240,
	})
	th.SetChecksum(^th.CalculateChecksum(header.PseudoHeaderChecksum(
		tcp.ProtocolNumber, srcAddr, dstAddr, uint16(header.TCPMinimumSize))))
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(raw),
	})
	pkt.NetworkProtocolNumber = header.IPv4ProtocolNumber
	return pkt
}

func report(msg string) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Println(msg)
	fmt.Printf("      handlerCalls=%d listenErrs=%v\n", handlerCalls, listenErrs)
	os.Exit(1)
}

// must prints the gVisor error and exits. tcpip.Error deliberately does not
// implement error in this revision (it is only a fmt.Stringer), so the helper
// takes tcpip.Error rather than error.
func must(stage string, err tcpip.Error) {
	if err != nil {
		fmt.Printf("FAIL %s: %v\n", stage, err)
		os.Exit(1)
	}
}
