//go:build vpn

package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// vpnStackNICID is the single NIC the gateway's stack owns. It is a constant
// because there is only ever one link endpoint - the memory TUN device - and a
// stack that could grow a second one would have two places to look for the reason
// a packet did not arrive.
const vpnStackNICID = tcpip.NICID(1)

var (
	// errVPNStackAddressConflict reports that the address a listener needs is
	// already claimed by something this registry does not know about.
	//
	// It is a distinct error rather than a wrapped gvisor one because the remedy is
	// different from every other registration failure: nothing may be cleaned up.
	// The address belongs to whoever claimed it first, most likely a listener from
	// a previous gateway generation that leaked, and removing it would break a
	// connection that is currently carrying traffic.
	errVPNStackAddressConflict = errors.New("vpn: the stack already has this address from another owner")
	// errVPNStackClosed reports use of a stack that has been shut down.
	errVPNStackClosed = errors.New("vpn: the stack is closed")
)

// vpnTCPListener is one claimed (address, port) pair and how many flows are using
// it.
//
// The reference count is what makes reclamation possible. Without it the set of
// addresses the stack owns would grow for as long as the process runs, because
// every internal service any peer ever reached would stay claimed forever; with
// it, an address is given back when the last flow through it ends and simply
// reclaimed again by the next SYN.
type vpnTCPListener struct {
	listener *gonet.TCPListener
	address  tcpip.Address
	refs     int
}

// vpnStack is the userspace TCP/IP stack that terminates tunnel TCP.
//
// It is assembled with exactly two protocols, ipv4 and tcp, and that minimalism is
// a safety property rather than an optimisation. The gateway promises it will
// never emit an ICMP datagram other than an echo reply it built itself; a stack
// with icmp or udp registered would answer a peer on the gateway's behalf whenever
// it thought an error had occurred. What keeps that promise true in practice is the
// device refusing to inject anything but TCP, so the stack never even sees a
// datagram it would feel obliged to answer.
//
// Promiscuous mode is what makes the whole design work. The destinations a peer
// reaches are internal addresses the stack has never been told about, and without
// promiscuous mode gvisor counts them as invalid-destination and drops them. With
// it, plus an address claimed at registration time, the stack will complete a
// handshake for an address it does not own and hand back a net.Conn.
type vpnStack struct {
	device   *memoryDevice
	netstack *stack.Stack
	metrics  vpnMetrics

	// mu guards the listener registry and is held across the whole of a
	// registration, including the calls into the stack. That serialisation is
	// deliberate: two goroutines racing to claim one address is how a registration
	// ends up holding an address it did not create, and it is the reason a
	// duplicate-address error can only ever mean the registry and the stack
	// disagree rather than that a concurrent caller got there first.
	mu        sync.Mutex
	listeners map[netip.AddrPort]*vpnTCPListener
	closed    bool

	// unmatched counts the segments that reached the default handler, which only
	// happens when no listener was registered for the tuple. It should stay at zero
	// in a healthy gateway, so it is the number to alert on.
	unmatched       atomic.Int64
	unmatchedLogged atomic.Bool
	conflictLogged  atomic.Bool
}

// newVPNStack builds the stack on top of a device that already exists.
//
// The device supplies the link endpoint rather than the stack creating its own
// because there must be exactly one queue between netstack and wireguard-go. Two
// endpoints would mean the stack's replies and the gateway's own emitted packets
// travelled different paths to the peer, and only one of them would be encrypted.
func newVPNStack(device *memoryDevice, metrics vpnMetrics) (*vpnStack, error) {
	if device == nil {
		return nil, errors.New("vpn: the stack needs a tun device")
	}
	if metrics == nil {
		metrics = nopVPNMetrics{}
	}
	assembled := &vpnStack{
		device: device,
		netstack: stack.New(stack.Options{
			NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
			TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
		}),
		metrics:   metrics,
		listeners: make(map[netip.AddrPort]*vpnTCPListener),
	}
	if terr := assembled.netstack.CreateNIC(vpnStackNICID, device.endpoint()); terr != nil {
		return nil, fmt.Errorf("vpn: attach the tun device to the stack: %v", terr)
	}
	if terr := assembled.netstack.SetPromiscuousMode(vpnStackNICID, true); terr != nil {
		return nil, fmt.Errorf("vpn: enable promiscuous mode: %v", terr)
	}
	// A default route out of the only NIC, so the stack has somewhere to send a
	// SYN-ACK for an address it claimed a moment ago.
	assembled.netstack.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: vpnStackNICID}})
	assembled.netstack.SetTransportProtocolHandler(tcp.ProtocolNumber, assembled.handleUnmatchedTCP)
	return assembled, nil
}

// handleUnmatchedTCP is the stack's default TCP handler, reached only when the
// demux table has no endpoint for the tuple.
//
// It consumes the segment. Returning false would hand it to gvisor's unknown
// destination path, which answers with a RST - and a RST tells the peer that the
// address exists and is reachable, which is exactly the fact the egress policy may
// have decided to withhold. Silence is the only answer that leaks nothing.
//
// It must not take the registry lock: this runs on the stack's receive path while
// a registration may be holding the lock and calling into the stack, so locking
// here would deadlock the gateway. Everything it touches is atomic.
func (s *vpnStack) handleUnmatchedTCP(id stack.TransportEndpointID, _ *stack.PacketBuffer) bool {
	count := s.unmatched.Add(1)
	s.metrics.Dropped("tcp", vpn.ClassStackError)
	if !s.unmatchedLogged.CompareAndSwap(false, true) {
		return true
	}
	// Logged once per process lifetime per gateway, because a peer that probes
	// ports would otherwise produce one line per segment. The counter carries the
	// volume; the log line carries the shape.
	slog.Error("vpn_stack_unmatched_tcp",
		"count", count,
		"localAddress", id.LocalAddress.String(),
		"localPort", id.LocalPort,
		"remoteAddress", id.RemoteAddress.String(),
		"remotePort", id.RemotePort)
	return true
}

// registerTCP claims an internal address inside the stack and starts listening on
// it, or returns the listener an earlier caller already created.
//
// It is called from the packet path, synchronously and before the segment that
// needs it is injected. That ordering is what removes the first-SYN penalty: a
// listener registered after injection would leave the demux empty for the segment
// already in flight, and the peer would have to wait a full retransmission timeout
// before anything answered.
func (s *vpnStack) registerTCP(target netip.AddrPort) (*gonet.TCPListener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errVPNStackClosed
	}
	if existing, ok := s.listeners[target]; ok {
		existing.refs++
		return existing.listener, nil
	}
	address := tcpip.AddrFrom4Slice(target.Addr().AsSlice())
	if terr := s.netstack.AddProtocolAddress(vpnStackNICID, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: address.WithPrefix(),
	}, stack.AddressProperties{}); terr != nil {
		if _, duplicate := terr.(*tcpip.ErrDuplicateAddress); duplicate {
			s.reportConflict(target, terr)
			return nil, errVPNStackAddressConflict
		}
		s.reportStackError("vpn_stack_claim_failed", target, terr)
		return nil, fmt.Errorf("vpn: claim %s inside the stack: %v", target, terr)
	}
	listener, err := gonet.ListenTCP(s.netstack, tcpip.FullAddress{Addr: address, Port: target.Port()}, ipv4.ProtocolNumber)
	if err != nil {
		// This address is ours: the claim above succeeded, so giving it back is
		// cleaning up after ourselves rather than interfering with another owner.
		// Leaving it claimed would strand an address that nothing listens on and
		// make every later registration for the same service fail as a conflict.
		if terr := s.netstack.RemoveAddress(vpnStackNICID, address); terr != nil {
			slog.Warn("vpn_stack_release_failed", "target", target.String(), "error", terr.String())
		}
		s.reportStackError("vpn_stack_listen_failed", target, err)
		return nil, fmt.Errorf("vpn: listen on %s inside the stack: %w", target, err)
	}
	s.listeners[target] = &vpnTCPListener{listener: listener, address: address, refs: 1}
	return listener, nil
}

// releaseTCP drops one reference and, on the last one, closes the listener and
// gives the address back to the stack.
//
// Releasing a target that is not registered is not an error. Both the idle reaper
// and shutdown call this, and neither can know whether the other already did, so a
// second call has to be harmless rather than something a caller must guard.
func (s *vpnStack) releaseTCP(target netip.AddrPort) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.listeners[target]
	if !ok {
		return
	}
	entry.refs--
	if entry.refs > 0 {
		return
	}
	delete(s.listeners, target)
	s.closeListenerLocked(target, entry)
}

// closeListenerLocked shuts one listener down and releases its address. The caller
// holds mu.
func (s *vpnStack) closeListenerLocked(target netip.AddrPort, entry *vpnTCPListener) {
	if err := entry.listener.Close(); err != nil {
		slog.Warn("vpn_stack_listener_close_failed", "target", target.String(), "error", err.Error())
	}
	// A failure here costs memory, not correctness: nothing listens on the address
	// any more, so no packet can be served by it, and the next registration for the
	// same service will report a conflict that points straight at the leak.
	if terr := s.netstack.RemoveAddress(vpnStackNICID, entry.address); terr != nil {
		s.reportStackError("vpn_stack_release_failed", target, terr)
	}
}

// lookupTCP reports whether a listener is registered for a tuple. The packet path
// uses it to decide between registering and simply injecting.
func (s *vpnStack) lookupTCP(target netip.AddrPort) (*gonet.TCPListener, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.listeners[target]
	if !ok {
		return nil, false
	}
	return entry.listener, true
}

// listenerRefs reports how many references a tuple has, and is what makes the
// reclamation rule observable in a test rather than inferred from one.
func (s *vpnStack) listenerRefs(target netip.AddrPort) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.listeners[target]
	if !ok {
		return 0
	}
	return entry.refs
}

// unmatchedTCP reports how many segments reached the default handler.
func (s *vpnStack) unmatchedTCP() int64 { return s.unmatched.Load() }

// injectTCP hands one segment to the stack through the device.
func (s *vpnStack) injectTCP(packet []byte) error { return s.device.injectTCP(packet) }

// close releases every listener and shuts the stack down. It is idempotent.
func (s *vpnStack) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for target, entry := range s.listeners {
		s.closeListenerLocked(target, entry)
		delete(s.listeners, target)
	}
	// RemoveNIC releases the addresses and the link endpoint; Close stops the stack
	// timers. Both are needed: Close alone leaves the NIC registered against a
	// device that is about to disappear.
	if terr := s.netstack.RemoveNIC(vpnStackNICID); terr != nil {
		slog.Warn("vpn_stack_remove_nic_failed", "error", terr.String())
	}
	s.netstack.Close()
	return nil
}

// reportConflict records a duplicate-address registration.
//
// It deliberately removes nothing. The address is held by an owner this registry
// does not know about, and the two candidate cleanups are both worse than leaving
// it alone: removing the address breaks a listener that may be serving traffic, and
// reusing it would let two listeners answer for one service. The failure is
// counted, logged once, and left for an operator to investigate.
func (s *vpnStack) reportConflict(target netip.AddrPort, terr tcpip.Error) {
	s.metrics.Dropped("tcp", vpn.ClassStackError)
	if !s.conflictLogged.CompareAndSwap(false, true) {
		return
	}
	slog.Error("vpn_stack_address_conflict",
		"target", target.String(),
		"error", terr.String(),
		"action", "none: the address belongs to another owner and was not removed")
}

// reportStackError counts one stack-level failure and logs the first of its kind.
func (s *vpnStack) reportStackError(event string, target netip.AddrPort, cause any) {
	s.metrics.Dropped("tcp", vpn.ClassStackError)
	slog.Error(event, "target", target.String(), "error", fmt.Sprint(cause))
}
