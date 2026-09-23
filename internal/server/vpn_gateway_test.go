//go:build vpn

package server

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// This file is the first place the gateway meets a real WireGuard peer. Every
// other data-plane test drives the pipeline by injecting a decrypted datagram,
// which is the right granularity for a policy assertion and the wrong one for an
// endpoint assertion: whether the Noise handshake completes, whether a revoked
// key stops being answered and whether the peer set survives a restart are all
// properties of the device and of nothing below it.
//
// The peer here is the upstream implementation's own in-memory tunnel, so both
// ends are real wireguard-go devices and only the network between them is a
// loopback UDP socket.

const (
	vpnWireClientIP   = "10.64.0.7"
	vpnWireSecondIP   = "10.64.0.8"
	vpnWireServiceIP  = "192.168.1.20"
	vpnWireServiceAlt = "192.168.1.21"
	// vpnWireHandshakeWait bounds a wait for a handshake that keepalives drive
	// once a second. It is generous because the assertion it protects is "the
	// handshake happens at all", and a failure here has to mean the endpoint is
	// broken rather than that the machine was busy.
	vpnWireHandshakeWait = 20 * time.Second
	// vpnWireKeepalive is the interval that makes a peer with nothing to send
	// still complete a handshake, which is what lets the handshake be asserted
	// on its own instead of as a side effect of a connection.
	vpnWireKeepalive = 1
)

// vpnWireClient is one real WireGuard peer pointed at the gateway under test.
type vpnWireClient struct {
	gateway      *vpnGateway
	dev          *device.Device
	net          *netstack.Net
	vpnIP        string
	publicKeyHex string
}

func (c *vpnWireClient) Close() { c.dev.Close() }

// devicePeer returns the gateway's own view of this client, read back over the
// configuration protocol. Reading it back rather than inspecting a Go field is
// the point: it is the same view a `wg show` on a real deployment would print,
// so a key that is present here is a key the device will answer a handshake for.
func (c *vpnWireClient) devicePeer() (map[string]string, bool) {
	return vpnFindDevicePeer(c.gateway.wg, c.publicKeyHex)
}

// waitHandshake blocks until the gateway records a completed handshake for this
// client, and fails the test if it never does.
func (c *vpnWireClient) waitHandshake(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(vpnWireHandshakeWait)
	for time.Now().Before(deadline) {
		if peer, ok := c.devicePeer(); ok {
			if secs, err := strconv.ParseInt(peer["last_handshake_time_sec"], 10, 64); err == nil && secs != 0 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the gateway recorded no handshake with this peer within %s", vpnWireHandshakeWait)
}

// waitGone blocks until the gateway no longer knows this client at all.
func (c *vpnWireClient) waitGone(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(vpnWireHandshakeWait)
	for time.Now().Before(deadline) {
		if _, ok := c.devicePeer(); !ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the gateway still holds this peer's key after it was removed")
}

// vpnFindDevicePeer returns one peer's block out of the configuration protocol's
// get operation.
func vpnFindDevicePeer(dev *device.Device, publicKeyHex string) (map[string]string, bool) {
	for _, peer := range vpnDevicePeers(dev) {
		if peer["public_key"] == publicKeyHex {
			return peer, true
		}
	}
	return nil, false
}

// vpnDevicePeers splits the get operation's output into one map per peer.
//
// The output is a flat stream of key=value lines with no separator between
// peers, so a block is everything from one public_key line up to the next. The
// device-level lines - private_key, listen_port, fwmark - come first and belong
// to no peer, which is why a line seen before the first public_key is dropped
// rather than attributed to one.
func vpnDevicePeers(dev *device.Device) []map[string]string {
	if dev == nil {
		return nil
	}
	out, err := dev.IpcGet()
	if err != nil {
		return nil
	}
	var peers []map[string]string
	var current map[string]string
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "public_key" {
			current = map[string]string{"public_key": value}
			peers = append(peers, current)
			continue
		}
		if current != nil {
			current[key] = value
		}
	}
	return peers
}

// vpnDevicePeerKeys lists every public key the device currently holds.
func vpnDevicePeerKeys(dev *device.Device) []string {
	if dev == nil {
		return nil
	}
	out, err := dev.IpcGet()
	if err != nil {
		return nil
	}
	var keys []string
	for _, line := range strings.Split(out, "\n") {
		if value, ok := strings.CutPrefix(line, "public_key="); ok {
			keys = append(keys, value)
		}
	}
	return keys
}

// startVPNGatewayWithWireClient brings up a serving gateway and one real peer.
//
// The peer is installed through the same ApplyPeer the management API calls, and
// after Start rather than before it, so the hot-reload path into the device is
// what the tests below actually exercise.
func startVPNGatewayWithWireClient(t *testing.T, mutate func(*storage.VPNPeer)) (*vpnGatewayFixture, *vpnWireClient) {
	t.Helper()
	fixture := newVPNGatewayFixture(t, enabledVPNTestConfig(t), nil)
	gateway := fixture.gateway
	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	row := vpnPeerRow("peer-a", pair.PublicKey, vpnWireClientIP)
	row.NodeID = "server-node-1"
	row.AllowedPorts = "53,443,8080"
	if mutate != nil {
		mutate(&row)
	}
	if err := gateway.ApplyPeer(row); err != nil {
		t.Fatalf("ApplyPeer: %v", err)
	}
	client := newVPNWireClient(t, gateway, pair, vpnWireClientIP)
	return fixture, client
}

// newVPNWireClient configures an upstream wireguard-go device to dial the
// gateway over loopback.
//
// It uses the upstream in-memory tunnel rather than the gateway's own because a
// peer that shared the implementation under test could not fail it: the two ends
// have to disagree about nothing except the network in between.
func newVPNWireClient(t *testing.T, gateway *vpnGateway, pair vpn.KeyPair, vpnIP string) *vpnWireClient {
	t.Helper()
	port := gateway.localPort()
	if port == 0 {
		t.Fatal("the gateway is not holding a UDP port, so there is nothing to dial")
	}
	tunnel, peerNet, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr(vpnIP)}, nil, gateway.mtu)
	if err != nil {
		t.Fatalf("CreateNetTUN: %v", err)
	}
	dev := device.NewDevice(tunnel, conn.NewDefaultBind(), vpnDeviceLogger())
	t.Cleanup(dev.Close)
	privateHex, err := vpn.EncodeKeyHex(pair.PrivateKey)
	if err != nil {
		t.Fatalf("EncodeKeyHex(private): %v", err)
	}
	publicHex, err := vpn.EncodeKeyHex(pair.PublicKey)
	if err != nil {
		t.Fatalf("EncodeKeyHex(public): %v", err)
	}
	serverHex, err := vpn.EncodeKeyHex(gateway.identity.PublicKey)
	if err != nil {
		t.Fatalf("EncodeKeyHex(node): %v", err)
	}
	clientConfig := strings.Join([]string{
		"private_key=" + privateHex,
		"listen_port=0",
		"replace_peers=true",
		"public_key=" + serverHex,
		fmt.Sprintf("endpoint=127.0.0.1:%d", port),
		"allowed_ip=0.0.0.0/0",
		fmt.Sprintf("persistent_keepalive_interval=%d", vpnWireKeepalive),
		"",
	}, "\n")
	if err := dev.IpcSet(clientConfig); err != nil {
		t.Fatalf("client IpcSet: %v", err)
	}
	return &vpnWireClient{gateway: gateway, dev: dev, net: peerNet, vpnIP: vpnIP, publicKeyHex: publicHex}
}

// stubVPNPeerRepository answers the one repository call Start makes.
//
// The embedded interface is the same trick failingVPNPeerRepository uses: a
// data-plane test that reached a management method would rather panic than get a
// zero value back, because a silent zero here would be indistinguishable from an
// empty peer set and the test would pass for the wrong reason.
type stubVPNPeerRepository struct {
	storage.VPNPeerRepository

	mu     sync.Mutex
	rows   []storage.VPNPeer
	err    error
	called []string
}

func (r *stubVPNPeerRepository) ListByNode(_ context.Context, nodeID string) ([]storage.VPNPeer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = append(r.called, nodeID)
	if r.err != nil {
		return nil, r.err
	}
	return append([]storage.VPNPeer(nil), r.rows...), nil
}

func (r *stubVPNPeerRepository) loads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.called...)
}

func TestVPNGatewayHoldsTheConfiguredUDPEndpoint(t *testing.T) {
	fixture := newVPNGatewayFixture(t, enabledVPNTestConfig(t), nil)
	gateway := fixture.gateway
	if got := gateway.localPort(); got != 0 {
		t.Fatalf("a gateway that has not started reports port %d, want 0", got)
	}
	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	port := gateway.localPort()
	if port == 0 {
		t.Fatal("Start left the gateway without a UDP port")
	}

	// The port being unavailable to anybody else is the assertion that matters.
	// Reading it back from the gateway would only prove the gateway believes it
	// is listening, which is exactly the belief a bind failure should overturn.
	other, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(port)})
	if err == nil {
		_ = other.Close()
		t.Error("a second socket bound the gateway's endpoint, so the gateway is not holding it")
	}
}

func TestVPNGatewayStartIsIdempotent(t *testing.T) {
	fixture := newVPNGatewayFixture(t, enabledVPNTestConfig(t), nil)
	gateway := fixture.gateway
	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := gateway.localPort()
	// A second Start is not an operator mistake to guard against so much as a
	// shutdown-race one: the runtime closes and the health path can still call
	// into a component that was started twice by an earlier refactor. Either way
	// the second call must not bind a second socket or drop the first.
	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("a second Start returned %v, want nil", err)
	}
	if got := gateway.localPort(); got != first {
		t.Errorf("a second Start moved the endpoint from port %d to %d", first, got)
	}
}

func TestVPNGatewayCompletesARealWireGuardHandshake(t *testing.T) {
	_, client := startVPNGatewayWithWireClient(t, nil)

	client.waitHandshake(t)

	peer, ok := client.devicePeer()
	if !ok {
		t.Fatal("the gateway does not list the peer it just handshook with")
	}
	if got := peer["endpoint"]; !strings.HasPrefix(got, "127.0.0.1:") {
		t.Errorf("the gateway learned endpoint %q, want the client's loopback address", got)
	}
	if got := peer["allowed_ip"]; got != vpnWireClientIP+"/32" {
		t.Errorf("the gateway allowed %q for this peer, want %s/32", got, vpnWireClientIP)
	}
}

func TestVPNGatewayRoutesAnAppliedPeerIntoThePipeline(t *testing.T) {
	fixture, client := startVPNGatewayWithWireClient(t, nil)
	client.waitHandshake(t)

	// The tunnel is up but nothing has been decrypted yet. One connection attempt
	// is the cheapest way to make the peer send a real payload packet, and the
	// pipeline's TCP handler is still the fixture's spy, so what has to be true
	// for this to pass is narrow: the datagram was accepted by allowed_ip, it was
	// decrypted with this peer's keys, and the source address it carried resolved
	// back to the peer the gateway installed.
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dialed := make(chan error, 1)
	go func() {
		dialConn, err := client.net.DialContextTCPAddrPort(dialCtx, netip.MustParseAddrPort(vpnWireServiceIP+":8080"))
		if err == nil {
			_ = dialConn.Close()
		}
		dialed <- err
	}()

	deadline := time.Now().Add(vpnWireHandshakeWait)
	for time.Now().Before(deadline) {
		if fixture.tcpCallCount() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	parsed := fixture.tcpCallsRecorded()
	if len(parsed) == 0 {
		t.Fatalf("no tunnel packet reached the pipeline; the dial reported %v", <-dialed)
	}
	first := parsed[0]
	if got := first.Src.String(); got != vpnWireClientIP {
		t.Errorf("the pipeline saw source %s, want the peer's tunnel address %s", got, vpnWireClientIP)
	}
	if got := first.Dst.String(); got != vpnWireServiceIP {
		t.Errorf("the pipeline saw destination %s, want %s", got, vpnWireServiceIP)
	}
	if first.DstPort != 8080 {
		t.Errorf("the pipeline saw destination port %d, want 8080", first.DstPort)
	}
	if first.TCPFlags&vpnWireTCPSYN == 0 {
		t.Errorf("the pipeline saw tcp flags %#x, want a syn", first.TCPFlags)
	}
}

func TestVPNGatewayRemovePeerTakesTheKeyOffTheDevice(t *testing.T) {
	fixture, client := startVPNGatewayWithWireClient(t, nil)
	client.waitHandshake(t)

	if err := fixture.gateway.RemovePeer("peer-a"); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}
	client.waitGone(t)

	// The key being gone is what makes a revocation immediate. A peer that is
	// still on the device can keep sending keepalives and be answered, and the
	// operator who revoked it has no way to see that from the management API.
	for _, key := range vpnDevicePeerKeys(fixture.gateway.wg) {
		if key == client.publicKeyHex {
			t.Error("the revoked peer's key is still on the device")
		}
	}
}

func TestVPNGatewayKeyRotationRetiresTheOldKey(t *testing.T) {
	fixture, client := startVPNGatewayWithWireClient(t, nil)
	client.waitHandshake(t)

	rotated, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	row := vpnPeerRow("peer-a", rotated.PublicKey, vpnWireClientIP)
	row.NodeID = "server-node-1"
	row.AllowedPorts = "53,443,8080"
	if err := fixture.gateway.ApplyPeer(row); err != nil {
		t.Fatalf("ApplyPeer with a rotated key: %v", err)
	}

	rotatedHex, err := vpn.EncodeKeyHex(rotated.PublicKey)
	if err != nil {
		t.Fatalf("EncodeKeyHex: %v", err)
	}
	keys := vpnDevicePeerKeys(fixture.gateway.wg)
	foundRotated := false
	for _, key := range keys {
		if key == client.publicKeyHex {
			t.Error("the pre-rotation key is still on the device, so the old identity can still handshake")
		}
		if key == rotatedHex {
			foundRotated = true
		}
	}
	if !foundRotated {
		t.Errorf("the rotated key %s is not on the device (have %v)", rotatedHex, keys)
	}
}

func TestVPNGatewayStartLoadsTheNodePeerSet(t *testing.T) {
	first, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	second, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	foreign, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	rows := []storage.VPNPeer{
		vpnPeerRow("peer-a", first.PublicKey, vpnWireClientIP),
		vpnPeerRow("peer-b", second.PublicKey, vpnWireSecondIP),
	}
	rows[1].VPNIP = vpnWireSecondIP
	// A row that belongs to another server node is refused by the peer table even
	// though the repository was asked for this node's rows only. Both filters
	// exist because the second one is the only thing standing between a
	// mis-scoped query and two gateways answering for one public key.
	other := vpnPeerRow("peer-foreign", foreign.PublicKey, "10.64.9.9")
	other.NodeID = "server-node-2"
	repository := &stubVPNPeerRepository{rows: append(rows, other)}

	fixture := newVPNGatewayFixture(t, enabledVPNTestConfig(t), func(deps *VPNGatewayDeps) {
		deps.Peers = repository
	})
	if err := fixture.gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if loads := repository.loads(); len(loads) != 1 || loads[0] != "server-node-1" {
		t.Errorf("Start loaded peers for %v, want one call for server-node-1", loads)
	}
	keys := vpnDevicePeerKeys(fixture.gateway.wg)
	for name, pair := range map[string]vpn.KeyPair{"peer-a": first, "peer-b": second} {
		want, err := vpn.EncodeKeyHex(pair.PublicKey)
		if err != nil {
			t.Fatalf("EncodeKeyHex(%s): %v", name, err)
		}
		found := false
		for _, key := range keys {
			if key == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was not installed on the device at startup (have %d keys)", name, len(keys))
		}
	}
	foreignHex, err := vpn.EncodeKeyHex(foreign.PublicKey)
	if err != nil {
		t.Fatalf("EncodeKeyHex(foreign): %v", err)
	}
	for _, key := range keys {
		if key == foreignHex {
			t.Error("a peer belonging to another server node was installed on this device")
		}
	}
}

func TestVPNGatewayStartReportsARepositoryItCannotRead(t *testing.T) {
	repository := &stubVPNPeerRepository{err: storage.ErrVPNIPLeaseStaleEpoch}
	fixture := newVPNGatewayFixture(t, enabledVPNTestConfig(t), func(deps *VPNGatewayDeps) {
		deps.Peers = repository
	})
	// Starting without a peer set is not a degraded mode the gateway can limp
	// through: every packet would be refused as an unknown peer and the
	// management API would still report the gateway as enabled. The database is
	// the authority, so an unreadable one is a startup failure.
	if err := fixture.gateway.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded with a repository that cannot be read")
	}
	if got := fixture.gateway.localPort(); got != 0 {
		t.Errorf("a failed Start left the gateway holding port %d", got)
	}
}

func TestVPNGatewayStartRefusesAnUnusableListenAddress(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	cfg.Listen = "gw-1.mesh.example.com:51820"
	fixture := newVPNGatewayFixture(t, cfg, nil)
	err := fixture.gateway.Start(context.Background())
	if err == nil {
		t.Fatal("Start accepted a DNS name where server.vpn.listen wants an address")
	}
	if !strings.Contains(err.Error(), "server.vpn.listen") {
		t.Errorf("Start reported %v, want an error that names the key the operator has to edit", err)
	}
}

func TestVPNGatewayStartReportsATakenPort(t *testing.T) {
	holder, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("holder socket: %v", err)
	}
	defer func() { _ = holder.Close() }()
	taken := holder.LocalAddr().(*net.UDPAddr).Port

	cfg := enabledVPNTestConfig(t)
	cfg.Listen = fmt.Sprintf("127.0.0.1:%d", taken)
	fixture := newVPNGatewayFixture(t, cfg, nil)
	startErr := fixture.gateway.Start(context.Background())
	if startErr == nil {
		t.Fatal("Start succeeded on a port another socket already holds")
	}
	if !strings.Contains(startErr.Error(), strconv.Itoa(taken)) {
		t.Errorf("Start reported %v, want an error that names port %d", startErr, taken)
	}
}

func TestVPNGatewayCloseReleasesTheEndpoint(t *testing.T) {
	fixture := newVPNGatewayFixture(t, enabledVPNTestConfig(t), nil)
	gateway := fixture.gateway
	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	port := gateway.localPort()

	if err := gateway.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Closing has to give the port back. A gateway that leaked it would make a
	// restart on the same node fail with an address-in-use error, which is the
	// one failure mode that turns a routine deploy into an outage.
	deadline := time.Now().Add(10 * time.Second)
	for {
		probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(port)})
		if err == nil {
			_ = probe.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the gateway's port %d was still held 10s after Close: %v", port, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := gateway.localPort(); got != 0 {
		t.Errorf("localPort() after Close = %d, want 0", got)
	}
	if err := gateway.Start(context.Background()); err == nil {
		t.Error("Start succeeded on a gateway that has been closed")
	}
}

func TestVPNGatewayStartRefusesWhenTheNodeKeyIsMissing(t *testing.T) {
	// newVPNGateway already refuses an empty identity, and this asserts the
	// message names the environment variable an operator has to inject rather
	// than the internal field that was empty.
	_, err := newVPNGateway(context.Background(), VPNGatewayDeps{
		Config: enabledVPNTestConfig(t),
		NodeID: "server-node-1",
	}, vpn.NodeIdentity{})
	if err == nil {
		t.Fatal("newVPNGateway accepted an empty node identity")
	}
	if !strings.Contains(err.Error(), vpn.NodePrivateKeyEnv) {
		t.Errorf("newVPNGateway reported %v, want an error naming %s", err, vpn.NodePrivateKeyEnv)
	}
}
