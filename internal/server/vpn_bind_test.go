//go:build vpn

package server

import (
	"bytes"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

// The bind is the only part of the VPN data plane that touches the host's
// network stack: the tunnel device, the TCP stack and the peer set are all
// memory. That makes its two properties worth pinning separately. The address it
// holds is the address server.vpn.listen named, which is the difference between a
// gateway an operator placed on one interface and one that answered on all of
// them; and a closed bind reports net.ErrClosed rather than parking a receive
// goroutine wireguard-go is waiting for, which is what a rebind or a shutdown
// would otherwise hang on.

const vpnBindReadBuffer = 2048

// openVPNBind builds a bind on a loopback address and leaves it open. The port is
// always zero in the fixture configuration, so the operating system chooses one
// and the test reads back whatever it chose.
func openVPNBind(t *testing.T, listen string) (*vpnBind, []conn.ReceiveFunc, uint16) {
	t.Helper()
	bind, err := newVPNBind(listen)
	if err != nil {
		t.Fatalf("newVPNBind(%q): %v", listen, err)
	}
	t.Cleanup(func() { _ = bind.Close() })
	fns, port, err := bind.Open(0)
	if err != nil {
		t.Fatalf("Open(%q): %v", listen, err)
	}
	if len(fns) != 1 {
		t.Fatalf("Open returned %d receive functions, want exactly 1: the tunnel is ipv4 only", len(fns))
	}
	if fns[0] == nil {
		t.Fatal("Open returned a nil receive function")
	}
	if port == 0 {
		t.Fatal("Open reported port 0, want the port the socket actually holds")
	}
	return bind, fns, port
}

// vpnReceiveSlots is one batch worth of the buffers wireguard-go hands a receive
// function. BatchSize is 1, so a single slot is the whole batch.
func vpnReceiveSlots() ([][]byte, []int, []conn.Endpoint) {
	return [][]byte{make([]byte, vpnBindReadBuffer)}, make([]int, 1), make([]conn.Endpoint, 1)
}

// receiveWithin runs one receive call and fails the test if it does not return in
// time. A receive on a live socket blocks until a datagram arrives, so an
// assertion that simply called it would hang the whole package on a regression
// instead of reporting one.
func receiveWithin(t *testing.T, fn conn.ReceiveFunc, packets [][]byte, sizes []int, eps []conn.Endpoint, within time.Duration) (int, error) {
	t.Helper()
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := fn(packets, sizes, eps)
		done <- result{n, err}
	}()
	select {
	case got := <-done:
		return got.n, got.err
	case <-time.After(within):
		t.Fatalf("the receive function did not return within %s", within)
		return 0, nil
	}
}

func TestVPNBindHoldsTheAddressItWasConfiguredWith(t *testing.T) {
	bind, _, port := openVPNBind(t, "127.0.0.1:0")

	// Holding the port is the whole claim, and a second socket on the same
	// address and port can only fail if the first one is really bound there.
	// That is also what proves the bind honoured the host half of the
	// configuration instead of going wildcard the way conn.NewDefaultBind does.
	other, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(port)})
	if err == nil {
		_ = other.Close()
		t.Error("a second socket bound the gateway's address and port, so the bind is not holding them")
	}
	if got := bind.localPort(); got != port {
		t.Errorf("localPort() = %d, want the port Open reported: %d", got, port)
	}
}

func TestVPNBindCarriesOneDatagramEachWay(t *testing.T) {
	bind, fns, port := openVPNBind(t, "127.0.0.1:0")

	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("peer socket: %v", err)
	}
	defer func() { _ = peer.Close() }()

	request := []byte("handshake-initiation")
	if _, err := peer.WriteToUDP(request, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(port)}); err != nil {
		t.Fatalf("peer write: %v", err)
	}

	packets, sizes, eps := vpnReceiveSlots()
	n, err := receiveWithin(t, fns[0], packets, sizes, eps, 5*time.Second)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if n != 1 {
		t.Fatalf("receive reported %d datagrams, want 1", n)
	}
	if sizes[0] != len(request) {
		t.Errorf("receive reported size %d, want %d", sizes[0], len(request))
	}
	if !bytes.Equal(packets[0][:sizes[0]], request) {
		t.Errorf("receive delivered %q, want %q", packets[0][:sizes[0]], request)
	}

	// The endpoint of a received datagram names its sender, because the only use
	// wireguard-go has for it is to send the reply back there.
	peerAddr := peer.LocalAddr().(*net.UDPAddr).AddrPort()
	if got := eps[0].DstToString(); got != peerAddr.String() {
		t.Errorf("the endpoint names %q, want the sender %q", got, peerAddr)
	}
	if got := eps[0].DstIP(); got != peerAddr.Addr() {
		t.Errorf("DstIP = %v, want %v", got, peerAddr.Addr())
	}

	reply := []byte("handshake-response")
	if err := bind.Send([][]byte{reply}, eps[0]); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("peer deadline: %v", err)
	}
	buffer := make([]byte, vpnBindReadBuffer)
	read, _, err := peer.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("peer read: %v", err)
	}
	if !bytes.Equal(buffer[:read], reply) {
		t.Errorf("the peer received %q, want %q", buffer[:read], reply)
	}
}

func TestVPNBindParsesEndpoints(t *testing.T) {
	bind, err := newVPNBind("127.0.0.1:0")
	if err != nil {
		t.Fatalf("newVPNBind: %v", err)
	}
	defer func() { _ = bind.Close() }()

	endpoint, err := bind.ParseEndpoint("127.0.0.1:1234")
	if err != nil {
		t.Fatalf("ParseEndpoint: %v", err)
	}
	want := netip.MustParseAddrPort("127.0.0.1:1234")
	if got := endpoint.DstToString(); got != want.String() {
		t.Errorf("DstToString = %q, want %q", got, want)
	}
	if got := endpoint.DstIP(); got != want.Addr() {
		t.Errorf("DstIP = %v, want %v", got, want.Addr())
	}
	// DstToBytes feeds the mac2 cookie calculation, so it must be the same
	// spelling the upstream bind produces rather than one this package invented.
	expected, err := want.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if got := endpoint.DstToBytes(); !bytes.Equal(got, expected) {
		t.Errorf("DstToBytes = %x, want %x", got, expected)
	}
	// No sticky source address is tracked: the gateway answers from one socket on
	// one address, so there is nothing to remember and ClearSrc has nothing to do.
	if got := endpoint.SrcToString(); got != "" {
		t.Errorf("SrcToString = %q, want the empty string", got)
	}
	if got := endpoint.SrcIP(); got.IsValid() {
		t.Errorf("SrcIP = %v, want an invalid address", got)
	}
	endpoint.ClearSrc()
	if got := endpoint.SrcIP(); got.IsValid() {
		t.Errorf("SrcIP after ClearSrc = %v, want an invalid address", got)
	}

	for _, invalid := range []string{"", "not-an-endpoint", "127.0.0.1", ":1234", "127.0.0.1:70000", "127.0.0.1:"} {
		if _, err := bind.ParseEndpoint(invalid); err == nil {
			t.Errorf("ParseEndpoint(%q) succeeded, want a refusal", invalid)
		}
	}
}

func TestVPNBindReportsOneBatchAndRefusesAMark(t *testing.T) {
	bind, err := newVPNBind("127.0.0.1:0")
	if err != nil {
		t.Fatalf("newVPNBind: %v", err)
	}
	defer func() { _ = bind.Close() }()

	if got := bind.BatchSize(); got != 1 {
		t.Errorf("BatchSize = %d, want 1: one socket, one datagram per call", got)
	}
	// Claiming to have set a mark that was not set would leave an operator
	// believing a routing policy is in force. The configuration protocol has a
	// fwmark line, so the refusal has to be an error and not a silent no-op.
	if err := bind.SetMark(1); err == nil {
		t.Error("SetMark reported success for a socket mark this bind cannot set")
	}
}

func TestVPNBindRefusesASecondOpen(t *testing.T) {
	bind, _, _ := openVPNBind(t, "127.0.0.1:0")

	if _, _, err := bind.Open(0); !errors.Is(err, conn.ErrBindAlreadyOpen) {
		t.Errorf("a second Open returned %v, want conn.ErrBindAlreadyOpen", err)
	}
}

func TestVPNBindReopensAfterClose(t *testing.T) {
	bind, _, port := openVPNBind(t, "127.0.0.1:0")

	if err := bind.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := bind.Close(); err != nil {
		t.Errorf("a second Close returned %v, want nil: Close is idempotent", err)
	}

	// wireguard-go's BindUpdate closes the bind before every reopen, so a bind
	// that stayed closed would turn every later port change into a startup
	// failure. The port is asked for explicitly this time, which is what a
	// listen_port line does.
	fns, reopened, err := bind.Open(port)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	if len(fns) != 1 {
		t.Errorf("the reopened bind returned %d receive functions, want 1", len(fns))
	}
	if reopened != port {
		t.Errorf("the reopened bind holds port %d, want the same port %d", reopened, port)
	}
}

func TestVPNBindReportsClosedAfterClose(t *testing.T) {
	bind, fns, _ := openVPNBind(t, "127.0.0.1:0")

	// A receive already parked in the socket has to be woken by Close. That is
	// not a nicety: wireguard-go waits for every receive goroutine to leave
	// before it finishes a rebind or a shutdown, so a parked reader is a hang.
	packets, sizes, eps := vpnReceiveSlots()
	blocked := make(chan error, 1)
	go func() {
		_, err := fns[0](packets, sizes, eps)
		blocked <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := bind.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-blocked:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("a parked receive returned %v after Close, want net.ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not wake a receive that was parked in the socket")
	}

	if _, err := fns[0](packets, sizes, eps); !errors.Is(err, net.ErrClosed) {
		t.Errorf("a receive after Close returned %v, want net.ErrClosed", err)
	}
	sender, err := newVPNBind("127.0.0.1:0")
	if err != nil {
		t.Fatalf("newVPNBind: %v", err)
	}
	defer func() { _ = sender.Close() }()
	endpoint, err := sender.ParseEndpoint("127.0.0.1:9")
	if err != nil {
		t.Fatalf("ParseEndpoint: %v", err)
	}
	if err := bind.Send([][]byte{[]byte("late")}, endpoint); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Send after Close returned %v, want net.ErrClosed", err)
	}
}

func TestVPNBindRefusesAnEndpointItDidNotParse(t *testing.T) {
	bind, _, _ := openVPNBind(t, "127.0.0.1:0")

	// The upstream endpoint type satisfies the same interface, so nothing in the
	// compiler stops a caller handing one over. Accepting it would send the
	// datagram to an address read out of a different struct's memory layout.
	foreign := &conn.StdNetEndpoint{AddrPort: netip.MustParseAddrPort("127.0.0.1:9")}
	if err := bind.Send([][]byte{[]byte("stray")}, foreign); !errors.Is(err, conn.ErrWrongEndpointType) {
		t.Errorf("Send with a foreign endpoint returned %v, want conn.ErrWrongEndpointType", err)
	}
}

func TestVPNBindRefusesAnUnusableListenAddress(t *testing.T) {
	for name, listen := range map[string]string{
		"empty":          "",
		"no-port":        "127.0.0.1",
		"not-an-address": "no-port-here",
		"port-too-big":   "127.0.0.1:70000",
		"negative-port":  "127.0.0.1:-1",
		// A name is refused on purpose. server.vpn.endpoint_host is the name a
		// client dials and server.vpn.listen is the address this process holds;
		// a name here would be resolved again at every rebind, so a DNS change
		// would silently move the gateway's endpoint out from under its peers.
		"dns-name": "gw-1.mesh.example.com:51820",
		// The tunnel is IPv4 end to end: the address pool, the packet parser and
		// the egress policy all are. Listening on an IPv6 address would accept
		// handshakes the gateway could never carry.
		"ipv6": "[::1]:51820",
	} {
		if _, err := newVPNBind(listen); err == nil {
			t.Errorf("%s: newVPNBind(%q) succeeded, want a refusal", name, listen)
		}
	}
}

func TestVPNBindAcceptsAWildcardHost(t *testing.T) {
	// An empty host and 0.0.0.0 both mean "every interface", which is the
	// documented default and the shape a gateway behind a NAT needs.
	for _, listen := range []string{":0", "0.0.0.0:0"} {
		bind, _, port := openVPNBind(t, listen)
		if port == 0 {
			t.Errorf("newVPNBind(%q) reported port 0", listen)
		}
		if err := bind.SetMark(0); err != nil {
			// A mark of zero is the absence of a mark, which needs no socket
			// option and is what wireguard-go sends when fwmark is unset.
			t.Errorf("SetMark(0) on %q returned %v, want nil", listen, err)
		}
	}
}
