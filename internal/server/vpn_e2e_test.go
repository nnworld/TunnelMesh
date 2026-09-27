//go:build vpn

package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// This file is the acceptance matrix of §18 of the embedded VPN gateway design.
//
// Everything below the crypto is real: a real wireguard-go client on one side, the
// gateway's own device, stack, peer table, flow table and three protocol relays in
// the middle, and a fake egress agent that answers on the other. The two edges a
// test has to observe are the only things replaced, which is what makes a green
// run here evidence about the data plane rather than about a mock.
//
// The cases a host stack cannot produce on demand - a GRE datagram and a non-first
// fragment - are driven through vpnRawWireClient, which is still a real Noise
// session. They enter at the same point a host stack's datagrams do.

const (
	// vpnE2EWait bounds a wait for something the gateway does on its own
	// goroutine. It is short because every case here is in-process over loopback,
	// and a long bound turns a broken assertion into a slow suite instead of a
	// failed one.
	vpnE2EWait = 5 * time.Second
	// vpnE2EFirstSegmentBound is the temporal half of D4's positive evidence. The
	// peer's stack retransmits a SYN only after gvisor's tcp.InitialRTO, which is
	// one second, so a bound has to sit below that second to be evidence of
	// anything at all. It does not sit near zero, because a bound a healthy gateway
	// can exceed reports the machine rather than the code: ten -race runs of the
	// dial measured 201-492ms on darwin/arm64 against 20-40ms without the detector,
	// and the plan's 200ms was unreachable for that reason alone. 700ms keeps the
	// whole measured range and still leaves 300ms below the retransmit signature.
	// The unmatched-segment count in the same case is the load-independent half.
	vpnE2EFirstSegmentBound = 700 * time.Millisecond
	// vpnE2ERefusalWait is how long a dial towards a refused target is given
	// before the test concludes the target is unreachable. The gateway answers a
	// refusal with silence, so the only thing that ends the dial is this bound.
	vpnE2ERefusalWait = 600 * time.Millisecond
	// vpnE2ESettle is a window in which nothing is expected to happen. Reading a
	// counter straight after an action only proves the action has not been
	// processed yet, so an absence is asserted by waiting a named window first.
	vpnE2ESettle = 100 * time.Millisecond
	// vpnE2EStaleHandshakeWait bounds the negative assertion of §18.6. The client
	// sends one keepalive per second, so three intervals without a handshake is
	// three opportunities the gateway declined rather than one unlucky loss.
	vpnE2EStaleHandshakeWait = 3 * time.Second
	// vpnE2EProtocolGRE is an IP protocol number the gateway does not forward. It
	// is spelled out rather than named from a header package because the point of
	// the case is a number the whitelist does not contain.
	vpnE2EProtocolGRE uint8 = 47
)

// vpnE2EConfig is the configuration the matrix runs under.
//
// The timeouts are the ones a deployment would use rather than the one second the
// pipeline tests use. A one second idle timeout is what lets those tests observe a
// reaper inside a test run; here it would end a splice while the test was still
// asserting on it, and the failure would be about the fixture's tuning.
func vpnE2EConfig(t *testing.T) config.VPNConfig {
	t.Helper()
	cfg := enabledVPNTestConfig(t)
	cfg.ConnectTimeout = 5 * time.Second
	cfg.IdleTimeout = 30 * time.Second
	cfg.ICMPTimeout = 10 * time.Second
	return cfg
}

// vpnE2EPeer is one real client plus the key it authenticated with.
//
// The key travels with the client because §18.6 is about the old identity: proving
// a rotation retired it needs a second handshake attempt made with the key that
// was replaced, and only the peer that holds that key can make one.
type vpnE2EPeer struct {
	client *vpnWireClient
	pair   vpn.KeyPair
}

// startVPNE2EGateway brings up a serving gateway with its real relays and one real
// client attached to it.
func startVPNE2EGateway(t *testing.T, mutate func(*storage.VPNPeer)) (*vpnGatewayFixture, *vpnEchoAgent, *vpnE2EPeer) {
	t.Helper()
	fixture, agent := startVPNE2EServingGateway(t)
	return fixture, agent, vpnE2EDial(t, fixture, "peer-a", vpnWireClientIP, mutate)
}

// startVPNE2EServingGateway brings up the gateway alone, for a case that attaches
// its own flavour of peer.
func startVPNE2EServingGateway(t *testing.T) (*vpnGatewayFixture, *vpnEchoAgent) {
	t.Helper()
	fixture := newVPNGatewayFixture(t, vpnE2EConfig(t), nil)
	// The pipeline tests replace the TCP and ICMP handlers with spies so a dispatch
	// can be asserted before a relay exists. Here the relays are the subject, so
	// the real ones are put back and the spies stay out of the path entirely. The
	// UDP relay was never replaced.
	fixture.useVPNTCPRelay()
	fixture.useVPNICMPRelay()
	agent := startVPNEchoAgent(t, fixture, "ok")
	if err := fixture.gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return fixture, agent
}

// vpnE2EIssuePeer installs one peer row through the same ApplyPeer the management
// API calls, and returns the key pair a client would have to hold to be it.
func vpnE2EIssuePeer(t *testing.T, fixture *vpnGatewayFixture, peerID, vpnIP string, mutate func(*storage.VPNPeer)) vpn.KeyPair {
	t.Helper()
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	row := vpnPeerRow(peerID, pair.PublicKey, vpnIP)
	row.NodeID = "server-node-1"
	// Widened over the row fixture's two ports for the same reason the pipeline
	// tests widen it: the matrix drives DNS-shaped UDP as well as TLS-shaped TCP,
	// and a case that failed on the port rule would be asserting the policy while
	// claiming to assert a relay. The port refusal has its own case below.
	row.AllowedPorts = "53,443,8080"
	if mutate != nil {
		mutate(&row)
	}
	if err := fixture.gateway.ApplyPeer(row); err != nil {
		t.Fatalf("ApplyPeer(%s): %v", peerID, err)
	}
	return pair
}

// vpnE2EDial issues one peer and attaches a real host-stack client to it.
func vpnE2EDial(t *testing.T, fixture *vpnGatewayFixture, peerID, vpnIP string, mutate func(*storage.VPNPeer)) *vpnE2EPeer {
	t.Helper()
	pair := vpnE2EIssuePeer(t, fixture, peerID, vpnIP, mutate)
	return &vpnE2EPeer{client: newVPNWireClient(t, fixture.gateway, pair, vpnIP), pair: pair}
}

// vpnE2EDialRaw issues one peer and attaches a client whose tunnel interface takes
// datagrams from the test. See vpnRawWireClient for why two flavours exist.
func vpnE2EDialRaw(t *testing.T, fixture *vpnGatewayFixture, peerID, vpnIP string, mutate func(*storage.VPNPeer)) *vpnRawWireClient {
	t.Helper()
	pair := vpnE2EIssuePeer(t, fixture, peerID, vpnIP, mutate)
	return newVPNRawWireClient(t, fixture.gateway, pair)
}

// vpnClientUAPI renders the configuration lines that point one client key at the
// gateway under test.
//
// Both client flavours share it. A rendered key or endpoint that differed between
// them would mean the two prove different things about the same gateway, and the
// difference would be invisible in a green run.
func vpnClientUAPI(t *testing.T, gateway *vpnGateway, pair vpn.KeyPair) string {
	t.Helper()
	port := gateway.localPort()
	if port == 0 {
		t.Fatal("the gateway is not holding a UDP port, so there is nothing to dial")
	}
	privateHex, err := vpn.EncodeKeyHex(pair.PrivateKey)
	if err != nil {
		t.Fatalf("EncodeKeyHex(private): %v", err)
	}
	serverHex, err := vpn.EncodeKeyHex(gateway.identity.PublicKey)
	if err != nil {
		t.Fatalf("EncodeKeyHex(node): %v", err)
	}
	return strings.Join([]string{
		"private_key=" + privateHex,
		"listen_port=0",
		"replace_peers=true",
		"public_key=" + serverHex,
		fmt.Sprintf("endpoint=127.0.0.1:%d", port),
		"allowed_ip=0.0.0.0/0",
		fmt.Sprintf("persistent_keepalive_interval=%d", vpnWireKeepalive),
		"",
	}, "\n")
}

// vpnWaitDeviceHandshake reports whether the gateway completed a handshake with one
// public key inside a bound.
//
// The bounded form is what a revocation and a rotation need. "This key stopped
// working" is an absence, and an absence can only be asserted by waiting a named
// time and finding nothing; the unbounded waitHandshake can only assert the
// opposite.
func vpnWaitDeviceHandshake(gateway *vpnGateway, publicKeyHex string, bound time.Duration) bool {
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if peer, ok := vpnFindDevicePeer(gateway.wg, publicKeyHex); ok {
			if secs, err := strconv.ParseInt(peer["last_handshake_time_sec"], 10, 64); err == nil && secs != 0 {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// vpnRawWireClient is a real WireGuard peer whose tunnel interface is a
// memoryDevice, so the test chooses the datagrams it sends.
//
// It exists because two of the matrix's cases cannot be produced by a host stack:
// gvisor's netstack neither originates GRE nor emits a non-first fragment when a
// test asks for one. Injecting those below the crypto instead would have proved
// something about handlePacket and nothing about the tunnel, so they are encrypted
// by wireguard-go here and decrypted by the gateway there like any other datagram.
type vpnRawWireClient struct {
	gateway      *vpnGateway
	dev          *device.Device
	tun          *memoryDevice
	publicKeyHex string

	mu       sync.Mutex
	received [][]byte
}

func newVPNRawWireClient(t *testing.T, gateway *vpnGateway, pair vpn.KeyPair) *vpnRawWireClient {
	t.Helper()
	client := &vpnRawWireClient{gateway: gateway}
	// The handler is the client's own receive path: everything the gateway sends
	// back is recorded, which is what lets a case assert that a refused datagram
	// was answered with silence rather than with an ICMP error the gateway never
	// decided to emit.
	tun, err := newMemoryDevice(gateway.mtu, 256, client.record)
	if err != nil {
		t.Fatalf("newMemoryDevice: %v", err)
	}
	client.tun = tun
	dev := device.NewDevice(tun, conn.NewDefaultBind(), vpnDeviceLogger())
	t.Cleanup(dev.Close)
	client.dev = dev
	publicHex, err := vpn.EncodeKeyHex(pair.PublicKey)
	if err != nil {
		t.Fatalf("EncodeKeyHex(public): %v", err)
	}
	client.publicKeyHex = publicHex
	if err := dev.IpcSet(vpnClientUAPI(t, gateway, pair)); err != nil {
		t.Fatalf("client IpcSet: %v", err)
	}
	return client
}

func (c *vpnRawWireClient) record(packet []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.received = append(c.received, append([]byte(nil), packet...))
}

// inbound returns every datagram the gateway has sent back so far.
func (c *vpnRawWireClient) inbound() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.received...)
}

// send pushes one datagram into the tunnel.
func (c *vpnRawWireClient) send(t *testing.T, packet []byte) {
	t.Helper()
	if err := c.tun.emit(packet); err != nil {
		t.Fatalf("the client could not put its datagram on the tunnel: %v", err)
	}
}

func (c *vpnRawWireClient) waitHandshake(t *testing.T) {
	t.Helper()
	if !vpnWaitDeviceHandshake(c.gateway, c.publicKeyHex, vpnWireHandshakeWait) {
		t.Fatalf("the gateway recorded no handshake with the raw peer within %s", vpnWireHandshakeWait)
	}
}

// vpnEchoAgent is the egress agent the matrix installs. It answers every stream the
// gateway opens and keeps what it was handed.
//
// Keeping it is not redundant with the stream's own record: the gateway closes a
// stream as soon as it is done with one, and Close drops the recorded writes, so a
// test that read them afterwards would find nothing and could not tell an
// unopened stream from a closed one.
type vpnEchoAgent struct {
	icmpStatus string

	mu       sync.Mutex
	payloads map[string][][]byte
	echoes   []protocol.ICMPEchoRequest

	stop    chan struct{}
	running sync.WaitGroup
}

// startVPNEchoAgent installs the agent for the whole test.
func startVPNEchoAgent(t *testing.T, fixture *vpnGatewayFixture, icmpStatus string) *vpnEchoAgent {
	t.Helper()
	agent := &vpnEchoAgent{
		icmpStatus: icmpStatus,
		payloads:   make(map[string][][]byte),
		stop:       make(chan struct{}),
	}
	fixture.opener.setOnOpen(func(request relay.StreamRequest, stream *fakeAgentStream) {
		agent.running.Add(1)
		go func() {
			defer agent.running.Done()
			agent.serve(request, stream)
		}()
	})
	t.Cleanup(agent.shutdown)
	return agent
}

func (a *vpnEchoAgent) shutdown() {
	close(a.stop)
	a.running.Wait()
}

func (a *vpnEchoAgent) serve(request relay.StreamRequest, stream *fakeAgentStream) {
	if request.Protocol == protocol.StreamProtocolICMPEcho {
		a.serveEcho(stream)
		return
	}
	a.serveBytes(request.Protocol, stream)
}

// serveBytes returns everything the gateway writes on one stream.
//
// Echoing is the whole of the fake's behaviour, and it is what makes a round trip
// prove anything: bytes that come back to the peer travelled peer -> gateway ->
// agent -> gateway -> peer, and a four-tuple, a checksum or a length the gateway
// got wrong on either leg stops them arriving.
func (a *vpnEchoAgent) serveBytes(label string, stream *fakeAgentStream) {
	for {
		select {
		case <-a.stop:
			return
		case payload := <-stream.written:
			a.record(label, payload)
			select {
			case stream.inject <- payload:
			case <-a.stop:
				return
			}
		}
	}
}

// serveEcho plays an agent that can ping: it decodes the request the gateway
// encoded and answers with the same identifiers and data, which is what a correct
// agent does and what the peer's own stack matches a reply on.
func (a *vpnEchoAgent) serveEcho(stream *fakeAgentStream) {
	for {
		select {
		case <-a.stop:
			return
		case encoded := <-stream.written:
			request, err := protocol.DecodeICMPEchoRequest(encoded)
			if err != nil {
				return
			}
			a.recordEcho(request)
			reply, err := protocol.EncodeICMPEchoReply(protocol.ICMPEchoReply{
				CorrelationID: request.CorrelationID,
				Identifier:    request.Identifier,
				Sequence:      request.Sequence,
				Data:          request.Data,
				Status:        a.icmpStatus,
				RTTMillis:     2,
			})
			if err != nil {
				return
			}
			select {
			case stream.inject <- reply:
			case <-a.stop:
				return
			}
		}
	}
}

func (a *vpnEchoAgent) record(label string, payload []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.payloads[label] = append(a.payloads[label], append([]byte(nil), payload...))
}

func (a *vpnEchoAgent) recordEcho(request protocol.ICMPEchoRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.echoes = append(a.echoes, request)
}

// waitWrote blocks until the gateway has handed the agent n payloads of one
// protocol and returns them.
//
// Waiting is the point: the agent runs on the gateway's own goroutine, so a test
// that read straight after its write returned would be racing it.
func (a *vpnEchoAgent) waitWrote(t *testing.T, label string, want int) [][]byte {
	t.Helper()
	deadline := time.Now().Add(vpnE2EWait)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		got := append([][]byte(nil), a.payloads[label]...)
		a.mu.Unlock()
		if len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the agent was handed %d %s payload(s) within %s, want %d", len(a.payloadsOf(label)), label, vpnE2EWait, want)
	return nil
}

// wroteCount reports how many payloads of one protocol the agent has been handed.
// It exists for the negative half of an assertion, where the evidence is a count
// that does not move.
func (a *vpnEchoAgent) wroteCount(label string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.payloads[label])
}

func (a *vpnEchoAgent) payloadsOf(label string) [][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.payloads[label]
}

// waitEcho blocks until the agent was asked to carry one icmp echo and returns the
// decoded request.
func (a *vpnEchoAgent) waitEcho(t *testing.T) protocol.ICMPEchoRequest {
	t.Helper()
	deadline := time.Now().Add(vpnE2EWait)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		echoes := append([]protocol.ICMPEchoRequest(nil), a.echoes...)
		a.mu.Unlock()
		if len(echoes) > 0 {
			return echoes[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the agent was never asked to carry an icmp echo within %s", vpnE2EWait)
	return protocol.ICMPEchoRequest{}
}

// vpnE2EWaitRequest blocks until the gateway opened a stream of one protocol and
// returns the request that carried it.
//
// The request is the subject of an assertion rather than a means to one: it names
// the egress agent the gateway chose, the protocol label the agent-side stream
// carries and the internal address the flow was dialled to, and a wrong value in
// any of the three is a routing fault no byte comparison would catch.
func vpnE2EWaitRequest(t *testing.T, fixture *vpnGatewayFixture, wantProtocol string) relay.StreamRequest {
	t.Helper()
	deadline := time.Now().Add(vpnE2EWait)
	for time.Now().Before(deadline) {
		for _, request := range fixture.opener.opened() {
			if request.Protocol == wantProtocol {
				return request
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the gateway opened no %s stream within %s; drops were %v", wantProtocol, vpnE2EWait, fixture.metrics.droppedRecords())
	return relay.StreamRequest{}
}

// vpnE2EWaitStream blocks until the gateway has opened n streams and returns the
// nth.
func vpnE2EWaitStream(t *testing.T, fixture *vpnGatewayFixture, index int) *fakeAgentStream {
	t.Helper()
	deadline := time.Now().Add(vpnE2EWait)
	for time.Now().Before(deadline) {
		if stream := fixture.opener.streamAt(index); stream != nil {
			return stream
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the gateway opened no stream at index %d within %s", index, vpnE2EWait)
	return nil
}

// vpnE2EWaitDropped blocks until one more drop of a protocol and class has been
// counted.
//
// Waiting rather than reading the counter once is what makes a refusal assertion
// safe: the drop is counted on the decryption goroutine, which is not the
// goroutine the peer's dial returned on.
func vpnE2EWaitDropped(t *testing.T, fixture *vpnGatewayFixture, label string, class vpn.ErrorClass, want int) {
	t.Helper()
	deadline := time.Now().Add(vpnE2EWait)
	for time.Now().Before(deadline) {
		if got := fixture.metrics.countDroppedWith(label, class); got >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the gateway counted %d %s/%s drops, want at least %d; every drop was %v",
		fixture.metrics.countDroppedWith(label, class), label, class, want, fixture.metrics.droppedRecords())
}

// vpnE2EWaitPeerFlows blocks until the flow table holds the number of flows named
// for one peer.
func vpnE2EWaitPeerFlows(t *testing.T, fixture *vpnGatewayFixture, peerID string, want int) {
	t.Helper()
	deadline := time.Now().Add(vpnE2EWait)
	for time.Now().Before(deadline) {
		if got := fixture.gateway.flows.countPeer(peerID); got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the flow table holds %d flow(s) for %s, want %d",
		fixture.gateway.flows.countPeer(peerID), peerID, want)
}

// vpnE2EWaitCountedFlow blocks until one peer's single flow has counted bytes in
// both directions and returns it.
//
// The snapshot is what the management API serves, so waiting on it rather than on
// the metric vector ties §18.2 to the console: a flow list that reported zero for
// a connection which just carried bytes would be phase 6's own regression.
func vpnE2EWaitCountedFlow(t *testing.T, fixture *vpnGatewayFixture, peerID string) VPNFlowSnapshot {
	t.Helper()
	deadline := time.Now().Add(vpnE2EWait)
	for time.Now().Before(deadline) {
		flows := fixture.gateway.flows.snapshot(peerID)
		if len(flows) == 1 && flows[0].BytesSent > 0 && flows[0].BytesReceived > 0 {
			return flows[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no flow of %s counted bytes in both directions within %s; the table holds %v",
		peerID, vpnE2EWait, fixture.gateway.flows.snapshot(peerID))
	return VPNFlowSnapshot{}
}

// vpnE2EWaitStreamClosed blocks until the gateway closed its end of one agent
// stream.
func vpnE2EWaitStreamClosed(t *testing.T, stream *fakeAgentStream) {
	t.Helper()
	deadline := time.Now().Add(vpnE2EWait)
	for time.Now().Before(deadline) {
		if stream.isClosed() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the gateway left the agent stream open after the peer it serves was revoked")
}

// vpnIPv4FragmentOf rewrites the flags and offset of a fixture packet so it arrives
// as a non-first fragment.
//
// The body is left alone on purpose. The gateway refuses a fragment before it reads
// a transport header, so a well-formed body would be proving something the pipeline
// never looks at. The header checksum is recomputed because the field it covers was
// just rewritten, and a datagram with a bad checksum would be refused for the wrong
// reason.
func vpnIPv4FragmentOf(packet []byte, offset uint16) []byte {
	fragmented := append([]byte(nil), packet...)
	// 0x2000 is the more-fragments flag of RFC 791 §3.1.
	binary.BigEndian.PutUint16(fragmented[6:8], 0x2000|offset)
	fragmented[10] = 0
	fragmented[11] = 0
	binary.BigEndian.PutUint16(fragmented[10:12], vpnIPv4HeaderChecksum(fragmented[:vpnWireIPv4HeaderMinimum]))
	return fragmented
}

// vpnE2EDialTCP opens one connection from the peer to an authorized internal
// service and reports how long it took.
//
// The failure message carries the gateway's own drop records because "the peer
// could not connect" is the symptom the data plane produces for every refusal it
// makes, and the class that caused it is the only thing distinguishing one from
// another.
func vpnE2EDialTCP(t *testing.T, fixture *vpnGatewayFixture, client *vpnWireClient, target string) (net.Conn, time.Duration) {
	t.Helper()
	dialCtx, cancel := context.WithTimeout(context.Background(), vpnE2EWait)
	defer cancel()
	started := time.Now()
	conn, err := client.net.DialContextTCPAddrPort(dialCtx, netip.MustParseAddrPort(target))
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("the peer could not reach the authorized service %s: %v; every drop was %v", target, err, fixture.metrics.droppedRecords())
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, elapsed
}

// vpnE2EWaitRead blocks until the peer has read back what the agent echoed.
func vpnE2EWaitRead(t *testing.T, conn net.Conn, want string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(vpnE2EWait))
	buffer := make([]byte, len(want))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("the peer read nothing back from the internal service: %v", err)
	}
	if string(buffer) != want {
		t.Errorf("the peer read %q back, want %q", buffer, want)
	}
}

// vpnClientHandshakeWithin reports whether one client's own device recorded a
// completed handshake inside a bound.
//
// Reading the client rather than the gateway is what makes the negative form
// meaningful: the gateway not holding a key is a fact about a map, while a client
// that asked and was never answered is a fact about the wire, and §18.6 is about
// the wire.
func vpnClientHandshakeWithin(dev *device.Device, bound time.Duration) bool {
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		for _, peer := range vpnDevicePeers(dev) {
			if secs, err := strconv.ParseInt(peer["last_handshake_time_sec"], 10, 64); err == nil && secs != 0 {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// vpnE2EPing opens the peer's ping socket towards one host.
//
// The local address is left unset on purpose. Binding one puts gvisor's ping
// endpoint in a state its own write path reports as invalid, and the socket needs
// no local address: the identifier it uses is chosen by the stack, which is exactly
// the identifier the gateway has to restore for the reply to be matched.
func vpnE2EPing(t *testing.T, peer *vpnE2EPeer, target string) net.Conn {
	t.Helper()
	ping, err := peer.client.net.DialPingAddr(netip.Addr{}, netip.MustParseAddr(target))
	if err != nil {
		t.Fatalf("the peer could not open a ping socket towards %s: %v", target, err)
	}
	t.Cleanup(func() { _ = ping.Close() })
	return ping
}

// vpnE2EPingReply is one echo reply exactly as the peer's own socket handed it over.
type vpnE2EPingReply struct {
	message []byte
	err     error
}

// vpnE2EPingReader is a ping socket that is already blocked in a read.
//
// Blocking before the echo is written is not a stylistic preference, it is the only
// ordering that can observe a reply. The upstream netstack ping socket registers its
// waiter inside ReadFrom, and a gvisor wait queue does not replay an event to a
// waiter that was not registered when the event fired: a reply that arrives first is
// queued in the socket and then never announced, so the peer waits out its deadline
// holding an answer it cannot see. A real ping blocks in recvfrom before its echo
// leaves for the same reason, and gonet's tcp and udp sockets do not have the gap
// because they attempt the read before they wait.
type vpnE2EPingReader struct {
	conn    net.Conn
	replies chan vpnE2EPingReply
	buffer  []byte
}

// vpnE2EPingAwait opens the peer's ping socket towards one host and parks a reader on
// it, so that the reply to whatever the caller writes next cannot be missed.
func vpnE2EPingAwait(t *testing.T, peer *vpnE2EPeer, target string) *vpnE2EPingReader {
	t.Helper()
	reader := &vpnE2EPingReader{
		conn:    vpnE2EPing(t, peer, target),
		replies: make(chan vpnE2EPingReply, 1),
		buffer:  make([]byte, 64),
	}
	// The deadline belongs to the socket rather than to a select, so a reply that
	// never comes ends the reader's goroutine instead of leaking it past the test.
	_ = reader.conn.SetReadDeadline(time.Now().Add(vpnE2EWait))
	go func() {
		n, err := reader.conn.Read(reader.buffer)
		reader.replies <- vpnE2EPingReply{message: reader.buffer[:n], err: err}
	}()
	// The waiter has to be registered before the echo is written, or the ordering
	// this helper exists to guarantee is not the ordering it produced. Registration
	// is one mutex away while the echo still has a gateway, a relay and an agent to
	// cross, so the settle is generous rather than tight.
	time.Sleep(20 * time.Millisecond)
	return reader
}

// send writes one echo request towards the host the reader is pointed at.
func (r *vpnE2EPingReader) send(t *testing.T, sequence uint16, payload string) {
	t.Helper()
	if _, err := r.conn.Write(vpnE2EICMPEchoRequest(sequence, []byte(payload))); err != nil {
		t.Fatalf("the peer could not send an echo towards %s: %v", r.conn.RemoteAddr(), err)
	}
}

// wait blocks until the peer's socket has read one reply, and reports the gateway's
// own drop records if it never does: "the peer got no answer" is the symptom every
// refusal in the data plane produces, and the class is what distinguishes one from
// another.
func (r *vpnE2EPingReader) wait(t *testing.T, fixture *vpnGatewayFixture) []byte {
	t.Helper()
	reply := <-r.replies
	if reply.err != nil {
		t.Fatalf("the peer received no echo reply: %v; every drop was %v", reply.err, fixture.metrics.droppedRecords())
	}
	return reply.message
}

// vpnE2EICMPEchoRequest builds what a ping socket writes.
//
// gvisor's ping endpoint takes a whole ICMP header rather than only the data: it
// overrides the identifier with the socket's own, requires type 8 code 0, and
// recomputes the checksum. The identifier is therefore a placeholder here and the
// checksum may stay zero, while the sequence is the caller's and survives the trip.
func vpnE2EICMPEchoRequest(sequence uint16, payload []byte) []byte {
	message := make([]byte, vpnWireICMPHeader+len(payload))
	message[0] = 8 // echo request
	message[1] = 0
	binary.BigEndian.PutUint16(message[6:8], sequence)
	copy(message[vpnWireICMPHeader:], payload)
	return message
}

// vpnE2EICMPEchoReplyOf splits what a ping socket reads back.
//
// The socket's data includes the ICMP header and nothing above it, so the reply's
// identifier is readable here. That is what makes "the gateway restored the
// peer's identifier" assertable from the peer's own side rather than only from the
// datagram the device emitted.
func vpnE2EICMPEchoReplyOf(t *testing.T, message []byte) (icmpType uint8, identifier, sequence uint16, data []byte) {
	t.Helper()
	if len(message) < vpnWireICMPHeader {
		t.Fatalf("the peer read %d byte(s) back, want at least an icmp header", len(message))
	}
	return message[0],
		binary.BigEndian.Uint16(message[4:6]),
		binary.BigEndian.Uint16(message[6:8]),
		message[vpnWireICMPHeader:]
}

// vpnE2EDeniedClasses flushes the aggregator and returns the error_class of every
// vpn_packet_denied entry with the count it carried.
//
// Flushing is what makes the assertion deterministic. A bucket is written when its
// window rolls over, and the fixture's clock is one a test moves by hand, so without
// the flush the entries would still be inside the aggregator and the case would be
// asserting nothing at all.
func vpnE2EDeniedClasses(t *testing.T, fixture *vpnGatewayFixture) map[string]int {
	t.Helper()
	fixture.gateway.denials.Flush(context.Background())
	classes := make(map[string]int)
	for _, entry := range fixture.audits.recorded() {
		if entry.Action != vpnPacketDeniedAction {
			t.Errorf("audit action = %q, want %q", entry.Action, vpnPacketDeniedAction)
			continue
		}
		var details map[string]any
		if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
			t.Fatalf("details %q are not json: %v", entry.Details, err)
		}
		class, _ := details["errorClass"].(string)
		if class == "" {
			t.Errorf("details %q name no error_class", entry.Details)
			continue
		}
		if peerID, _ := details["peerId"].(string); peerID != "peer-a" {
			t.Errorf("details %q attribute the refusal to %q, want the peer that sent it", entry.Details, peerID)
		}
		count, _ := details["count"].(float64)
		classes[class] += int(count)
	}
	return classes
}

// TestVPNE2EHandshakeGivesThePeerItsTunnelAddress is §18.1 without the download: a
// real client authenticates with the key the management plane issued and ends up
// holding the tunnel address that was allocated to it.
func TestVPNE2EHandshakeGivesThePeerItsTunnelAddress(t *testing.T) {
	fixture, _, peer := startVPNE2EGateway(t, nil)
	peer.client.waitHandshake(t)

	held, ok := peer.client.devicePeer()
	if !ok {
		t.Fatal("the gateway does not list the peer it just handshook with")
	}
	if got := held["allowed_ip"]; got != vpnWireClientIP+"/32" {
		t.Errorf("the gateway allowed %q for this peer, want %s/32", got, vpnWireClientIP)
	}
	if got := held["endpoint"]; !strings.HasPrefix(got, "127.0.0.1:") {
		t.Errorf("the gateway learned endpoint %q, want the client's loopback address", got)
	}

	// The gateway's own tunnel address is not an internal service, and answering a
	// ping for it would make the gateway a host on the network it relays to. The
	// refusal is counted rather than silent, because from the peer's side a refused
	// ping and a dead host are the same experience.
	ping := vpnE2EPing(t, peer, vpnPipeSelfIP)
	if _, err := ping.Write(vpnE2EICMPEchoRequest(1, []byte("self"))); err != nil {
		t.Fatalf("the peer could not send an echo towards the gateway's own address: %v", err)
	}
	vpnE2EWaitDropped(t, fixture, protocol.StreamProtocolICMPEcho, vpn.ClassTargetDenied, 1)
}

// TestVPNE2ETCPRoundTripKeepsTheFirstSegment is §18.2 for tcp, and the positive
// evidence for D4.
func TestVPNE2ETCPRoundTripKeepsTheFirstSegment(t *testing.T) {
	fixture, agent, peer := startVPNE2EGateway(t, nil)
	peer.client.waitHandshake(t)

	conn, elapsed := vpnE2EDialTCP(t, fixture, peer.client, vpnWireServiceIP+":443")

	// The listener is registered on the packet path before the segment that needs
	// it is injected, so the peer's SYN is answered the first time. That claim is
	// asserted twice, because the two halves fail differently.
	//
	// The count is the half that does not depend on how busy the machine is. D4's
	// mechanism is the demux being empty when the segment arrives, and a segment
	// that finds nothing in the demux reaches the stack's default handler and is
	// counted there, so a gateway that registered its listener after injecting
	// would have to leave this at zero by dropping the SYN somewhere the stack
	// never sees.
	if unmatched := fixture.gateway.stack.unmatchedTCP(); unmatched != 0 {
		t.Errorf("%d segment(s) reached the stack's default handler, want 0: the listener has to be registered before the SYN is injected", unmatched)
	}
	// The elapsed time is the half that also covers a first segment lost above the
	// stack, in the policy or the relay, where no default handler would ever see
	// it. What makes it evidence rather than noise is the one second initial rto:
	// anything that forced a retransmit pays that whole second, so the bound below
	// has room for scheduling and none for a retransmit.
	if elapsed > vpnE2EFirstSegmentBound {
		t.Errorf("the connection took %s to establish, want under %s, so the first segment was dropped and retransmitted", elapsed, vpnE2EFirstSegmentBound)
	}

	const payload = "tunnelmesh-tcp-round-trip"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("the peer could not write to the internal service: %v", err)
	}
	wrote := agent.waitWrote(t, vpnTCPLabel, 1)
	if string(wrote[0]) != payload {
		t.Errorf("the agent was handed %q, want the peer's %q", wrote[0], payload)
	}
	vpnE2EWaitRead(t, conn, payload)

	request := vpnE2EWaitRequest(t, fixture, vpnTCPLabel)
	if request.TargetHost != vpnWireServiceIP {
		t.Errorf("the gateway dialled %q through the agent, want the internal service the peer addressed", request.TargetHost)
	}
	if request.TargetPort != 443 {
		t.Errorf("the gateway dialled port %d, want 443", request.TargetPort)
	}
	if request.AgentID != "agent-1" {
		t.Errorf("the gateway dialled through agent %q, want the peer's egress agent", request.AgentID)
	}
	if request.NodeID != "server-node-1" {
		t.Errorf("the gateway named node %q on the stream, want its own", request.NodeID)
	}

	// The same round trip has to be visible in the flow list the management API
	// serves, or the console reports zero for a connection that just carried bytes.
	flow := vpnE2EWaitCountedFlow(t, fixture, "peer-a")
	if flow.Protocol != vpnTCPLabel || flow.Target != vpnWireServiceIP || flow.Port != 443 {
		t.Errorf("the flow list describes %s to %s:%d, want tcp to %s:443", flow.Protocol, flow.Target, flow.Port, vpnWireServiceIP)
	}
}

// TestVPNE2EUDPRoundTripPreservesTheTuple is §18.2 for udp.
func TestVPNE2EUDPRoundTripPreservesTheTuple(t *testing.T) {
	fixture, agent, peer := startVPNE2EGateway(t, nil)
	peer.client.waitHandshake(t)

	service := netip.MustParseAddrPort(vpnWireServiceIP + ":53")
	local := netip.MustParseAddrPort(vpnWireClientIP + ":41000")
	conn, err := peer.client.net.DialUDPAddrPort(local, service)
	if err != nil {
		t.Fatalf("the peer could not open a udp socket towards an authorized service: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	const query = "tunnelmesh-dns-query"
	if _, err := conn.Write([]byte(query)); err != nil {
		t.Fatalf("the peer could not send its datagram: %v", err)
	}
	wrote := agent.waitWrote(t, "udp", 1)
	if string(wrote[0]) != query {
		t.Errorf("the agent was handed %q, want the datagram's payload %q", wrote[0], query)
	}

	request := vpnE2EWaitRequest(t, fixture, "udp")
	if request.TargetHost != vpnWireServiceIP || request.TargetPort != 53 {
		t.Errorf("the gateway dialled %s:%d through the agent, want %s:53", request.TargetHost, request.TargetPort, vpnWireServiceIP)
	}

	// The read is the tuple assertion. A connected udp socket accepts a datagram
	// only when its source is the connected peer and its destination is this
	// socket's own address and port, so a reply the gateway built with a wrong
	// address, a wrong port or a wrong checksum never arrives here: the peer's
	// stack drops it and this read times out.
	_ = conn.SetReadDeadline(time.Now().Add(vpnE2EWait))
	buffer := make([]byte, 64)
	n, err := conn.Read(buffer)
	if err != nil {
		t.Fatalf("the peer received no reply datagram: %v; every drop was %v", err, fixture.metrics.droppedRecords())
	}
	if string(buffer[:n]) != query {
		t.Errorf("the peer read %q back, want %q", buffer[:n], query)
	}
}

// TestVPNE2EICMPEchoRoundTrip is §18.3's positive half.
func TestVPNE2EICMPEchoRoundTrip(t *testing.T) {
	fixture, agent, peer := startVPNE2EGateway(t, nil)
	peer.client.waitHandshake(t)

	reader := vpnE2EPingAwait(t, peer, vpnWireServiceIP)

	const payload = "tunnelmesh-echo"
	const sequence = uint16(7)
	reader.send(t, sequence, payload)

	request := vpnE2EWaitRequest(t, fixture, protocol.StreamProtocolICMPEcho)
	if request.TargetHost != vpnWireServiceIP {
		t.Errorf("the gateway asked the agent to ping %q, want %q", request.TargetHost, vpnWireServiceIP)
	}
	// An echo addresses a host. The protocol has no port and the agent reads none,
	// so zero is the honest value rather than a number invented to match the tcp
	// and udp cases.
	if request.TargetPort != 0 {
		t.Errorf("the gateway named port %d on an icmp stream, want 0", request.TargetPort)
	}

	echo := agent.waitEcho(t)
	if string(echo.Data) != payload {
		t.Errorf("the agent was asked to carry %q, want the peer's %q", echo.Data, payload)
	}
	if echo.Identifier == 0 {
		t.Error("the gateway handed the agent an echo with no identifier, so no reply could be matched to it")
	}

	// The reply is the assertion that matters. The peer's stack discards an echo
	// reply whose identifier is not the one its own socket chose, so a reply that
	// is readable here is one the gateway restored correctly and checksummed
	// correctly. The sequence and the data are compared as well, because a reply
	// that matched on the identifier alone would still be the wrong answer to a
	// different ping.
	icmpType, identifier, gotSequence, data := vpnE2EICMPEchoReplyOf(t, reader.wait(t, fixture))
	if icmpType != 0 {
		t.Errorf("the peer read icmp type %d back, want 0 (echo reply)", icmpType)
	}
	if identifier == 0 || identifier != echo.Identifier {
		t.Errorf("the reply carried identifier %d, want the %d the gateway gave the agent", identifier, echo.Identifier)
	}
	if gotSequence != sequence {
		t.Errorf("the reply carried sequence %d, want the peer's %d", gotSequence, sequence)
	}
	if string(data) != payload {
		t.Errorf("the peer read %q back, want %q", data, payload)
	}
}

// TestVPNE2ERevocationEndsTheFlowAndRetiresTheKey is §18.5.
func TestVPNE2ERevocationEndsTheFlowAndRetiresTheKey(t *testing.T) {
	fixture, agent, peer := startVPNE2EGateway(t, nil)
	peer.client.waitHandshake(t)

	const payload = "carried before the revocation"
	conn, _ := vpnE2EDialTCP(t, fixture, peer.client, vpnWireServiceIP+":443")
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("the peer could not write to the internal service: %v", err)
	}
	agent.waitWrote(t, vpnTCPLabel, 1)
	vpnE2EWaitRead(t, conn, payload)
	stream := vpnE2EWaitStream(t, fixture, 0)
	vpnE2EWaitPeerFlows(t, fixture, "peer-a", 1)

	if err := fixture.gateway.RemovePeer("peer-a"); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}

	// The flow is gone and the agent side of it is closed, so the connection stops
	// carrying bytes to the internal service at the moment of the revocation rather
	// than whenever the peer or the agent happens to notice.
	vpnE2EWaitPeerFlows(t, fixture, "peer-a", 0)
	vpnE2EWaitStreamClosed(t, stream)

	// The key is off the device, which is what makes a new handshake impossible: a
	// device that still held it would answer keepalives, and nobody looking at the
	// management API could tell.
	//
	// It is also why the revoked peer never sees a FIN. The teardown emits one, but
	// wireguard-go has no peer left to encrypt it to, so the datagram is dropped on
	// the server side and the peer's socket ends on its own timeout instead. That is
	// what removing a WireGuard key means rather than a gap in the teardown, and
	// §18.5's "immediately disconnected" is about the server side, which is what the
	// two assertions above and the one below establish.
	peer.client.waitGone(t)

	// Bytes the revoked peer pushes after this point go nowhere. The assertion is
	// made on the agent side rather than on the peer's write, because a write into
	// the peer's own stack succeeds for as long as the socket has buffer, and a
	// buffer is not an internal service.
	_, _ = conn.Write([]byte("after the revocation"))
	time.Sleep(vpnE2ESettle)
	if got := agent.wroteCount(vpnTCPLabel); got != 1 {
		t.Errorf("the agent was handed %d tcp payload(s) after the revocation, want the 1 it carried before it", got)
	}
}

// TestVPNE2ERotationStopsTheOldIdentity is §18.6.
func TestVPNE2ERotationStopsTheOldIdentity(t *testing.T) {
	fixture, _, peer := startVPNE2EGateway(t, nil)
	peer.client.waitHandshake(t)

	// Issuing the same peer again with a new key is what the management API's
	// rotate does, and the row carries the same tunnel address: a rotation replaces
	// an identity, it does not allocate a second one.
	rotated := vpnE2EIssuePeer(t, fixture, "peer-a", vpnWireClientIP, nil)
	fresh := newVPNWireClient(t, fixture.gateway, rotated, vpnWireClientIP)
	fresh.waitHandshake(t)

	// A client holding the replaced key asks for a handshake and is never answered.
	// Three keepalive intervals is the bound: the client sends one per second, so
	// this is three opportunities the gateway declined rather than one lost
	// datagram.
	stale := newVPNWireClient(t, fixture.gateway, peer.pair, vpnWireSecondIP)
	if vpnClientHandshakeWithin(stale.dev, vpnE2EStaleHandshakeWait) {
		t.Error("the gateway answered a handshake made with the key the rotation replaced")
	}
	// The mechanism, stated as well as the behaviour: the old key is not on the
	// device, so there is nothing for that handshake to be matched against.
	for _, key := range vpnDevicePeerKeys(fixture.gateway.wg) {
		if key == peer.client.publicKeyHex {
			t.Error("the pre-rotation key is still on the device")
		}
	}
}

// TestVPNE2EUnauthorisedTargetsAreUnreachableAndAudited is §18.4 for the refusals a
// host stack produces on its own.
func TestVPNE2EUnauthorisedTargetsAreUnreachableAndAudited(t *testing.T) {
	fixture, _, peer := startVPNE2EGateway(t, nil)
	peer.client.waitHandshake(t)

	for _, tc := range []struct {
		name   string
		target string
		class  vpn.ErrorClass
	}{
		{"a target outside the address allowlist", "172.16.5.5:443", vpn.ClassTargetDenied},
		{"the cloud metadata address", "169.254.169.254:443", vpn.ClassMetadataDenied},
		{"a port outside the allowlist", vpnWireServiceAlt + ":9999", vpn.ClassPortDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := fixture.metrics.countDroppedWith(vpnTCPLabel, tc.class)
			dialCtx, cancel := context.WithTimeout(context.Background(), vpnE2ERefusalWait)
			defer cancel()
			conn, err := peer.client.net.DialContextTCPAddrPort(dialCtx, netip.MustParseAddrPort(tc.target))
			if err == nil {
				_ = conn.Close()
				t.Fatalf("the peer reached %s, which its policy does not allow", tc.target)
			}
			// Both halves of §18.4 are needed. A dial that failed on its own would
			// also fail on a gateway whose tunnel was simply down, so the counter is
			// what distinguishes a policy refusal from a broken data plane, and its
			// class is what an operator's dashboard reads.
			vpnE2EWaitDropped(t, fixture, vpnTCPLabel, tc.class, before+1)
		})
	}

	classes := vpnE2EDeniedClasses(t, fixture)
	for _, want := range []vpn.ErrorClass{vpn.ClassTargetDenied, vpn.ClassMetadataDenied, vpn.ClassPortDenied} {
		if classes[string(want)] == 0 {
			t.Errorf("no vpn_packet_denied entry carried error_class %s; the entries named %v", want, classes)
		}
	}
	// Three targets, each probed by a stack that retransmits its SYN, so well over
	// three datagrams were refused. The other half of §18.4 is that they reach the
	// audit log as one entry per class per peer: a peer that probes a thousand
	// addresses must not be able to write a thousand rows.
	if entries := len(fixture.audits.recorded()); entries != len(classes) {
		t.Errorf("%d audit entries were written for %d distinct error classes, so a refusal was not aggregated", entries, len(classes))
	}
}

// TestVPNE2EUnsupportedDatagramsAreCountedAndNotAnswered is §18.4 for the two
// refusals a host stack will not produce on demand. See vpnRawWireClient for why
// they still travel through a real Noise session.
func TestVPNE2EUnsupportedDatagramsAreCountedAndNotAnswered(t *testing.T) {
	fixture, _ := startVPNE2EServingGateway(t)
	raw := vpnE2EDialRaw(t, fixture, "peer-raw", vpnWireSecondIP, nil)
	raw.waitHandshake(t)

	source := net.ParseIP(vpnWireSecondIP)
	service := net.ParseIP(vpnWireServiceIP)

	t.Run("an ip protocol the whitelist does not contain", func(t *testing.T) {
		before := fixture.metrics.countDroppedWith(vpnProtocolUnparsed, vpn.ClassProtocolUnsupported)
		raw.send(t, vpnIPv4Packet(vpnE2EProtocolGRE, source, service, []byte("gre")))
		// The label is "unknown" rather than a rendering of 47: a peer that chose
		// the protocol number would otherwise choose a metric label value, and
		// metric labels are the one place an unbounded string becomes a leak.
		vpnE2EWaitDropped(t, fixture, vpnProtocolUnparsed, vpn.ClassProtocolUnsupported, before+1)
	})

	t.Run("a non-first fragment", func(t *testing.T) {
		before := fixture.metrics.countDroppedWith("udp", vpn.ClassFragmentDropped)
		raw.send(t, vpnIPv4FragmentOf(vpnIPv4Packet(vpnTestProtocolUDP, source, service, make([]byte, 64)), 8))
		vpnE2EWaitDropped(t, fixture, "udp", vpn.ClassFragmentDropped, before+1)
	})

	// The gateway promises it never emits an ICMP it did not build for a relayed
	// echo, so both refusals must be silent: a destination-unreachable or a
	// fragmentation-needed would be an answer the gateway did not decide to give,
	// and the peer's stack would read it as one.
	//
	// The settle is what makes the absence honest. Both datagrams were refused on
	// the same goroutine that counted them, so anything the gateway meant to send
	// back was already queued by the time the counter moved; the window is for the
	// loopback datagram to arrive, not for a decision to be made.
	time.Sleep(100 * time.Millisecond)
	if received := raw.inbound(); len(received) != 0 {
		t.Errorf("the gateway answered a refused datagram with %d packet(s) of %d byte(s)", len(received), len(received[0]))
	}
}
