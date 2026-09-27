//go:build vpn

package server

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
)

// vpnBindBatchSize is the number of datagrams one receive or send call carries.
//
// One is a deliberate ceiling rather than a missed optimisation. The generic
// batch paths upstream exist to amortise recvmmsg and UDP segmentation offload,
// neither of which a single IPv4 socket on a single address can use here, and a
// batch larger than one would make the gateway's own drop accounting ambiguous:
// a partial batch reports one size per slot and this bind has no way to say which
// slot a counted packet came from.
const vpnBindBatchSize = 1

var (
	// errVPNBindMarkUnsupported reports a socket mark this bind cannot set.
	//
	// The configuration protocol has a fwmark line, so an operator can ask for
	// one. Returning nil would say the mark was applied and leave a routing
	// policy believed to be in force that is not, which is worse than refusing:
	// the refusal names the option that has to go.
	errVPNBindMarkUnsupported = errors.New("vpn: this bind cannot set a socket mark")
	// errVPNBindNotOpen reports a send or receive attempted while no socket is
	// held. It is deliberately net.ErrClosed rather than a new sentinel, because
	// that is exactly what the bind contract requires a closed bind to report and
	// wireguard-go tests for it to decide whether a receive loop should exit.
	errVPNBindNotOpen = net.ErrClosed
)

var (
	_ conn.Bind     = (*vpnBind)(nil)
	_ conn.Endpoint = (*vpnEndpoint)(nil)
)

// vpnBind is the gateway's WireGuard socket.
//
// It replaces conn.NewDefaultBind for one reason: the upstream bind listens on
// ":"+port, which is every interface, and server.vpn.listen names a host as well
// as a port. A gateway an operator placed on one address has to actually be on
// that address, because the alternative answers handshakes on interfaces the
// deployment's firewall rules were never written for.
//
// It is IPv4 only, which matches the rest of the data plane: the address pool,
// the packet parser and the egress policy are all IPv4, so a v6 listener would
// accept handshakes the gateway could not carry a single packet for.
type vpnBind struct {
	// network, host and configuredPort are parsed once and never change, so they
	// are read without the lock.
	network string
	host    netip.Addr
	// configuredPort is the port server.vpn.listen named. Zero means the
	// operating system chooses, and it is what makes Open(0) - the call
	// wireguard-go makes when the device has no listen_port line - mean "the
	// configured address" rather than "a random address on every interface".
	configuredPort int

	// mu guards the socket. It is held only for the pointer swap and never across
	// a read or a write, so a send blocked in the kernel does not stop a receive
	// from being woken by Close.
	mu     sync.Mutex
	socket *net.UDPConn
	bound  uint16
}

// newVPNBind parses server.vpn.listen. It does not bind anything: Open does, and
// wireguard-go calls Open when the device is brought up.
func newVPNBind(listen string) (*vpnBind, error) {
	trimmed := strings.TrimSpace(listen)
	hostText, portText, err := net.SplitHostPort(trimmed)
	if err != nil {
		return nil, fmt.Errorf("vpn: server.vpn.listen %q must be a host:port address for the public udp endpoint: %w", trimmed, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("vpn: server.vpn.listen %q must name a port between 0 and 65535, where 0 asks the operating system to choose one", trimmed)
	}
	bind := &vpnBind{network: "udp4", configuredPort: port}
	if hostText == "" {
		// An empty host is the wildcard, which is a legitimate deployment choice
		// for a gateway behind a NAT that owns every address on the box.
		return bind, nil
	}
	parsed, err := netip.ParseAddr(hostText)
	if err != nil {
		return nil, fmt.Errorf("vpn: server.vpn.listen %q must name an ip address: a dns name belongs in server.vpn.endpoint_host, because a name here would be resolved again at every rebind and could move the endpoint out from under its peers", trimmed)
	}
	if parsed.Is6() && !parsed.Is4In6() {
		return nil, fmt.Errorf("vpn: server.vpn.listen %q is an ipv6 address and the tunnel carries ipv4 only", trimmed)
	}
	bind.host = parsed.Unmap()
	return bind, nil
}

// port reports the configured port, which is what the gateway needs to decide
// whether the device can be told a listen_port line at all.
func (b *vpnBind) port() int { return b.configuredPort }

// localPort reports the port the socket actually holds, or zero when there is no
// socket. It differs from port whenever the configuration asked for an ephemeral
// one, and it is the number that has to be logged and reported: it is the port
// peers are reaching.
func (b *vpnBind) localPort() uint16 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bound
}

// Open binds the socket and returns the single receive loop wireguard-go should
// run. A port of zero means "use the configured one", which may itself be zero.
//
// Open after Close binds again rather than failing. That is not leniency:
// wireguard-go's BindUpdate closes the bind before every reopen, so a bind that
// stayed closed would turn every later listen_port change into a permanent
// failure to listen.
func (b *vpnBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.socket != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	wanted := int(port)
	if wanted == 0 {
		wanted = b.configuredPort
	}
	var listenIP net.IP
	if b.host.IsValid() {
		listenIP = net.IP(b.host.AsSlice())
	}
	socket, err := net.ListenUDP(b.network, &net.UDPAddr{IP: listenIP, Port: wanted})
	if err != nil {
		return nil, 0, fmt.Errorf("vpn: bind the wireguard endpoint on server.vpn.listen %s: %w", b.listenDescription(wanted), err)
	}
	local, ok := socket.LocalAddr().(*net.UDPAddr)
	if !ok {
		_ = socket.Close()
		return nil, 0, fmt.Errorf("vpn: the wireguard endpoint bound %s, which is not a udp address", socket.LocalAddr())
	}
	b.socket = socket
	b.bound = uint16(local.Port)
	return []conn.ReceiveFunc{b.receive}, b.bound, nil
}

// listenDescription names the address an operator has to look at when a bind
// fails. It is built from the parsed values rather than from the configuration
// string so that a refused ephemeral port still reports the address involved.
func (b *vpnBind) listenDescription(port int) string {
	host := ""
	if b.host.IsValid() {
		host = b.host.String()
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// Close releases the socket and wakes anything parked in it. It is idempotent,
// and it may be followed by another Open.
func (b *vpnBind) Close() error {
	b.mu.Lock()
	socket := b.socket
	b.socket = nil
	b.bound = 0
	b.mu.Unlock()
	if socket == nil {
		return nil
	}
	return socket.Close()
}

// SetMark refuses every mark but the absent one.
func (b *vpnBind) SetMark(mark uint32) error {
	if mark == 0 {
		return nil
	}
	return errVPNBindMarkUnsupported
}

// Send writes datagrams to a peer's endpoint.
func (b *vpnBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	target, ok := ep.(*vpnEndpoint)
	if !ok {
		// The upstream endpoint type satisfies the same interface, so nothing in
		// the compiler stops a caller handing one over. Accepting it would read a
		// destination address out of another struct's layout and send the datagram
		// somewhere nobody asked for.
		return conn.ErrWrongEndpointType
	}
	b.mu.Lock()
	socket := b.socket
	b.mu.Unlock()
	if socket == nil {
		return errVPNBindNotOpen
	}
	for _, buf := range bufs {
		if _, err := socket.WriteToUDPAddrPort(buf, target.dst); err != nil {
			return err
		}
	}
	return nil
}

// ParseEndpoint turns the endpoint line of a peer configuration into an address
// this bind can send to.
func (b *vpnBind) ParseEndpoint(text string) (conn.Endpoint, error) {
	parsed, err := netip.ParseAddrPort(strings.TrimSpace(text))
	if err != nil {
		return nil, fmt.Errorf("vpn: %q is not an ip:port endpoint: %w", text, err)
	}
	return &vpnEndpoint{dst: parsed}, nil
}

// BatchSize reports the one-datagram batch this bind works in.
func (b *vpnBind) BatchSize() int { return vpnBindBatchSize }

// receive reads one datagram into the first slot of the batch.
//
// The socket pointer is taken under the lock and then used outside it. Holding
// the lock across the read would make Close wait for a datagram that may never
// arrive, and Close is what wireguard-go calls to wake this very goroutine.
func (b *vpnBind) receive(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
	if len(packets) == 0 || len(sizes) == 0 || len(eps) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	socket := b.socket
	b.mu.Unlock()
	if socket == nil {
		return 0, errVPNBindNotOpen
	}
	read, from, err := socket.ReadFromUDPAddrPort(packets[0])
	if err != nil {
		return 0, err
	}
	sizes[0] = read
	// The endpoint of a received datagram names its sender, because the only use
	// the device has for it is to send the reply back to the address it arrived
	// from. No source address is recorded: the gateway answers from one socket on
	// one address, so there is nothing sticky to remember.
	eps[0] = &vpnEndpoint{dst: from}
	return 1, nil
}

// vpnEndpoint is one peer address as this bind sees it.
type vpnEndpoint struct {
	dst netip.AddrPort
	// src is kept even though nothing populates it, because the interface requires
	// the two halves and a sticky source address is the one thing a single-socket
	// gateway cannot offer. Leaving the field out would make SrcIP a method that
	// lies about why it has no answer.
	src netip.Addr
}

// ClearSrc forgets a cached source address.
func (e *vpnEndpoint) ClearSrc() { e.src = netip.Addr{} }

// SrcToString reports the local address datagrams to this peer leave from.
func (e *vpnEndpoint) SrcToString() string {
	if !e.src.IsValid() {
		return ""
	}
	return e.src.String()
}

// SrcIP reports the local address datagrams to this peer leave from.
func (e *vpnEndpoint) SrcIP() netip.Addr { return e.src }

// DstToString reports the peer's address in the ip:port form the configuration
// protocol uses.
func (e *vpnEndpoint) DstToString() string { return e.dst.String() }

// DstIP reports the peer's address without its port.
func (e *vpnEndpoint) DstIP() netip.Addr { return e.dst.Addr() }

// DstToBytes renders the peer's address for the mac2 cookie calculation, in the
// same spelling the upstream bind uses so the two implementations agree about
// what a cookie was computed over.
func (e *vpnEndpoint) DstToBytes() []byte {
	encoded, err := e.dst.MarshalBinary()
	if err != nil {
		return nil
	}
	return encoded
}
