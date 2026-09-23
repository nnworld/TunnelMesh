//go:build vpn

package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"

	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// vpnTCPLabel is the protocol label the metrics and the flow table use. It is the
// same value the agent-side stream carries, so one protocol is one series.
const vpnTCPLabel = "tcp"

// vpnTCPReclaimFloor is the shortest time a tuple stays claimed after its last
// connection ended.
//
// It is longer than the stack's own tcp.DefaultTCPTimeWaitTimeout of 60 seconds on
// purpose. Reclaiming a port the stack is still holding for a closing connection
// makes the next registration fail with "address in use", which costs the peer a
// retransmission: the failure is self-healing, but it is also entirely avoidable by
// waiting. server.vpn.idle_timeout governs how long a connection may stay silent,
// which is a different question from how long an address may stay claimed, and a
// deployment that sets it to a few seconds should not inherit a reconnect penalty
// it never asked for.
const vpnTCPReclaimFloor = 2 * time.Minute

// vpnTCPRelay terminates tunnel TCP in the userspace stack and splices each
// connection to an agent stream.
//
// TCP is the one protocol the gateway terminates rather than relays (D3): a
// peer's stack expects a real TCP peer, with a handshake, retransmission and
// congestion control, and none of that can be relayed byte-for-byte over a stream
// without the internal service seeing the tunnel's loss characteristics as its
// own. netstack answers for it, and the relay's job is the bookkeeping around
// that: which listener serves which internal address, which peer owns which
// accepted connection, and when both are given back.
type vpnTCPRelay struct {
	gateway *vpnGateway

	// mu guards pumps and is held across a registration, which is what makes "one
	// listener per internal tuple" true under concurrency rather than merely
	// likely. It is never held while a connection is being served.
	mu    sync.Mutex
	pumps map[netip.AddrPort]*vpnTCPPump
}

func newVPNTCPRelay(gateway *vpnGateway) *vpnTCPRelay {
	return &vpnTCPRelay{gateway: gateway, pumps: make(map[netip.AddrPort]*vpnTCPPump)}
}

// serve handles one allowed TCP segment.
//
// A SYN makes the stack able to answer the tuple before the segment that needs it
// is injected (D4); every other segment only needs the listener to still exist.
// Injecting is the last step and the only door into netstack, so a segment this
// function refuses never reaches the stack's default handler, which would count it
// a second time and answer for a decision the gateway already made.
func (r *vpnTCPRelay) serve(_ context.Context, entry vpnPeerEntry, parsed vpnWirePacket, packet []byte) {
	g := r.gateway
	target := netip.AddrPortFrom(parsed.Dst, uint16(parsed.DstPort))
	if parsed.isTCPSYN() {
		if err := r.ensurePump(target); err != nil {
			// Audited here and counted by the stack, which is the component that
			// failed and the one that knows whether the fault was a conflict with
			// another owner or a claim the stack refused.
			g.denyAudited(vpnTCPLabel, parsed, entry, vpnStackClass(err), err.Error())
			return
		}
	} else if !r.serving(target) {
		// The connection this segment belongs to is not one this gateway is
		// serving: it predates a restart, or its listener was reclaimed while the
		// peer still thought it was up. Silence is the answer, per the promise that
		// the gateway leaks nothing about an address a peer is not allowed to
		// reach - a RST would confirm the port is there.
		g.denyPacket(vpnTCPLabel, parsed, entry, vpn.ClassEgressUnavailable,
			"this gateway is not serving a connection for the segment")
		return
	}
	if err := g.stack.injectTCP(packet); err != nil {
		g.denyPacket(vpnTCPLabel, parsed, entry, vpnStackClass(err), err.Error())
	}
}

// ensurePump claims an internal tuple inside the stack and starts the accept loop
// for it, or reuses the one an earlier SYN created.
//
// It runs on the packet path and therefore never blocks on anything but the
// registry lock: the calls into the stack are memory operations, and a SYN that
// waited for a dial would be a SYN that arrived after the peer's retransmission.
func (r *vpnTCPRelay) ensurePump(target netip.AddrPort) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.pumps[target]; ok && !existing.stopped.Load() {
		// Touching on every SYN is what keeps the idle sweep from reclaiming a
		// listener a peer is actively connecting to.
		existing.touch()
		return nil
	}
	listener, err := r.gateway.stack.registerTCP(target)
	if err != nil {
		return err
	}
	pump := &vpnTCPPump{relay: r, target: target, listener: listener}
	// The registration above took the pump's own reference on the listener, and
	// claimed records that it is still ours to give back.
	pump.claimed.Store(true)
	pump.touch()
	r.pumps[target] = pump
	go pump.run()
	return nil
}

// serving reports whether a listener is registered for a tuple.
func (r *vpnTCPRelay) serving(target netip.AddrPort) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	pump, ok := r.pumps[target]
	return ok && !pump.stopped.Load()
}

// forget drops one pump from the registry. The identity check is what makes a
// retire that lost a race harmless: a newer pump for the same tuple is left alone.
func (r *vpnTCPRelay) forget(target netip.AddrPort, pump *vpnTCPPump) {
	r.mu.Lock()
	if current, ok := r.pumps[target]; ok && current == pump {
		delete(r.pumps, target)
	}
	r.mu.Unlock()
}

// reapIdle gives back the listeners no connection is using any more.
//
// Without it the registry would grow for as long as the process runs, because
// every internal address any peer ever reached would stay claimed inside the
// stack. A pump is reclaimed only when nothing is spliced through it and no SYN
// arrived for a whole idle_timeout, so a live connection can never lose its
// listener; the address is simply claimed again by the next SYN.
//
// It returns how many it reclaimed, which is what makes the rule observable in a
// test rather than inferred from one.
func (r *vpnTCPRelay) reapIdle() int {
	idle := r.gateway.deps.Config.IdleTimeout
	if idle <= 0 {
		return 0
	}
	if idle < vpnTCPReclaimFloor {
		idle = vpnTCPReclaimFloor
	}
	now := r.gateway.now()
	r.mu.Lock()
	reaped := 0
	for target, pump := range r.pumps {
		if pump.active.Load() > 0 {
			continue
		}
		if now.Sub(time.Unix(0, pump.lastActivity.Load())) < idle {
			continue
		}
		// Marked before the reference is released, under the same lock a SYN takes:
		// a registration that runs concurrently either sees a live pump and touches
		// it, or sees a stopped one and claims the tuple again itself.
		delete(r.pumps, target)
		pump.stopped.Store(true)
		// The claim is taken away here rather than by the accept loop that is about
		// to wake up. That loop's exit is asynchronous, and by the time it runs a new
		// pump may hold the tuple: releasing twice would hand back a reference that
		// is no longer this pump's and close a listener connections are being
		// accepted through.
		if pump.claimed.CompareAndSwap(true, false) {
			r.gateway.stack.releaseTCP(target)
			reaped++
		}
	}
	r.mu.Unlock()
	return reaped
}

// pumpCount reports how many listeners the relay holds. It exists for tests.
func (r *vpnTCPRelay) pumpCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pumps)
}

// close retires every pump. The connections themselves belong to the flow table,
// whose teardown reaches each session; what is left here is the guarantee that no
// accept loop and no claimed address outlives the gateway.
func (r *vpnTCPRelay) close() {
	r.mu.Lock()
	pumps := make([]*vpnTCPPump, 0, len(r.pumps))
	for target, pump := range r.pumps {
		pumps = append(pumps, pump)
		delete(r.pumps, target)
	}
	r.mu.Unlock()
	for _, pump := range pumps {
		pump.retire()
	}
}

// vpnTCPPump owns one listener and the connections accepted through it.
type vpnTCPPump struct {
	relay    *vpnTCPRelay
	target   netip.AddrPort
	listener *gonet.TCPListener

	// active counts the connections being served. It is what the idle sweep reads
	// to know a listener is still in use, and it is decremented by the connection
	// rather than by the pump so a long-lived splice cannot be reclaimed under
	// itself.
	active       atomic.Int64
	lastActivity atomic.Int64
	// stopped says the pump no longer serves SYNs, and claimed says it still owes
	// the registry one reference. They are separate because the two facts are
	// decided by different goroutines: the sweep can stop a pump whose accept loop
	// has not woken up yet, and only one of them may then release the reference.
	stopped atomic.Bool
	claimed atomic.Bool
}

// touch records that something happened on this tuple.
func (p *vpnTCPPump) touch() {
	p.lastActivity.Store(p.relay.gateway.now().UnixNano())
}

// run accepts connections until the listener goes away.
func (p *vpnTCPPump) run() {
	g := p.relay.gateway
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			// The normal way out is the listener being closed, either by the idle
			// sweep or by shutdown, and that is not worth a line in the log.
			if !p.stopped.Load() {
				slog.Debug("vpn_tcp_accept_failed", "target", p.target.String(), "error", err)
			}
			p.retire()
			return
		}
		p.touch()
		// The connection takes its own reference on the listener, so the tuple is
		// reclaimed only when the pump and every connection through it are gone.
		if _, terr := g.stack.registerTCP(p.target); terr != nil {
			_ = conn.Close()
			g.metrics.Dropped(vpnTCPLabel, vpnStackClass(terr))
			p.retire()
			return
		}
		p.active.Add(1)
		go p.relay.serveConn(p, conn)
	}
}

// untrack ends one connection's claim on the listener.
func (p *vpnTCPPump) untrack() {
	p.active.Add(-1)
	p.touch()
	p.relay.gateway.stack.releaseTCP(p.target)
}

// retire gives the pump's own reference back, exactly once.
//
// Exactly once is the whole content of this function. A pump is retired by
// whichever happens first of its accept loop ending and the idle sweep claiming
// it, and the loser of that race must not release a reference the winner already
// released: the tuple may have been claimed again in between, and a second
// release would close somebody else's listener.
func (p *vpnTCPPump) retire() {
	// Set before the claim is touched: a SYN arriving while this runs has to see a
	// stopped pump and claim the tuple again itself, rather than be handed a
	// listener whose reference is about to disappear.
	p.stopped.Store(true)
	if !p.claimed.CompareAndSwap(true, false) {
		return
	}
	p.relay.forget(p.target, p)
	p.relay.gateway.stack.releaseTCP(p.target)
}

// serveConn splices one accepted connection to the internal service.
//
// The peer is resolved from the accepted connection rather than carried over from
// the SYN that caused the listener to be registered. One listener serves every
// peer that reaches the tuple, so the SYN's owner and the connection's owner are
// not necessarily the same peer, and trusting the earlier answer would let one
// peer's traffic be dialled through another peer's egress agent.
func (r *vpnTCPRelay) serveConn(pump *vpnTCPPump, conn net.Conn) {
	g := r.gateway
	defer pump.untrack()
	defer conn.Close()

	remote, port, ok := vpnTCPConnPeer(conn.RemoteAddr())
	if !ok {
		slog.Error("vpn_tcp_unusable_peer_address",
			"target", pump.target.String(), "remote", conn.RemoteAddr().String())
		g.metrics.Dropped(vpnTCPLabel, vpn.ClassStackError)
		return
	}
	packet := vpnTCPConnPacket(remote, port, pump.target)
	entry, class, serving := g.peers.lookupByIP(remote)
	if !serving {
		g.denyPacket(vpnTCPLabel, packet, entry, class, vpnPeerRefusalReason(class))
		return
	}
	// The policy is checked again here even though the packet path checked the
	// SYN. A peer can be patched between the two, and the connection is the thing
	// that will carry bytes for minutes: deciding it once, on a packet that has
	// not even been accepted yet, is the weaker of the two places to decide.
	// Size is zero because the MTU rule belongs to a datagram, and every segment
	// of this connection is measured against it on the packet path anyway.
	if err := entry.Policy.Allow(packet.policyPacket()); err != nil {
		g.denyPolicy(vpnTCPLabel, packet, entry, err)
		return
	}

	session := &vpnTCPSession{gateway: g, pump: pump, entry: entry, conn: conn, packet: packet}
	flow, created, err := g.flows.register(vpnFlowRegistration{
		Key: vpnFlowKey{
			PeerID:   entry.PeerID,
			Protocol: vpnTCPLabel,
			Src:      netip.AddrPortFrom(remote, port),
			Dst:      pump.target,
		},
		Target:    pump.target.Addr().String(),
		Port:      int(pump.target.Port()),
		PeerLimit: entry.PeerLimit,
		// teardown rather than a bare close: a revocation, the idle reaper and
		// shutdown all reach the session through the flow table, and none of them
		// know an agent stream and a netstack connection are attached to it.
		OnClose: session.teardown,
	})
	if err != nil {
		ceiling := vpn.ClassCapacityExhausted
		if errors.Is(err, errVPNFlowTableClosed) {
			ceiling = vpn.ClassEgressUnavailable
		}
		g.denyPacket(vpnTCPLabel, packet, entry, ceiling, err.Error())
		return
	}
	if !created {
		// The stack handed out a tuple a flow is already registered for. It should
		// not happen, and splicing a second agent stream onto one four-tuple would
		// make two owners of one connection, so the newcomer is closed instead.
		g.denyPacket(vpnTCPLabel, packet, entry, vpn.ClassStackError,
			"the stack accepted a second connection for a tuple that is already served")
		return
	}
	session.flow = flow

	stream, cancel, err := openVPNEgress(g.baseCtx, g.deps.Opener, relay.StreamRequest{
		NodeID:     g.deps.NodeID,
		AgentID:    entry.AgentID,
		Protocol:   vpnTCPLabel,
		TargetHost: pump.target.Addr().String(),
		TargetPort: int(pump.target.Port()),
	}, g.deps.Config.ConnectTimeout)
	if err != nil {
		session.failUnlessClosing(vpnEgressClass(err), err.Error())
		g.flows.remove(session.key())
		return
	}
	session.attach(stream, cancel)
	g.metrics.Stream(vpnTCPLabel, "started", "")

	// The existing splice owns both directions, the idle watchdog and the
	// half-close handling. Reusing it is deliberate: it is the same code path every
	// other tunnel in this server uses, so a fix to it fixes the VPN plane too.
	_, _, reason := spliceWithIdleTimeout(session.countedConn(), stream, g.deps.Config.IdleTimeout)
	g.metrics.Stream(vpnTCPLabel, vpnTCPStreamResult(reason), "")
	g.flows.remove(session.key())
	session.teardown()
}

// vpnTCPStreamResult maps the splice's outcome onto the stream metric's result
// label.
//
// An idle timeout is reported as a close rather than as an error: the peer stopped
// sending, which is how a connection ends most of the time, and counting it as a
// failure would make the error rate follow the traffic pattern instead of the
// fault rate.
func vpnTCPStreamResult(reason string) string {
	if reason == "error" {
		return "failed"
	}
	return "closed"
}

// vpnTCPSession is one spliced connection and everything that has to be released
// when it ends.
type vpnTCPSession struct {
	gateway *vpnGateway
	pump    *vpnTCPPump
	entry   vpnPeerEntry
	conn    net.Conn
	flow    *vpnFlow
	// packet is the shape a denial record needs: both addresses and both ports of
	// the connection. It is kept instead of the segment, whose payload aliases a
	// buffer the device owns.
	packet vpnWirePacket

	mu        sync.Mutex
	stream    io.ReadWriteCloser
	cancel    context.CancelFunc
	closeOnce sync.Once
	failed    atomic.Bool
}

// key is the session's flow-table key.
func (s *vpnTCPSession) key() vpnFlowKey { return s.flow.key }

// attach records the agent stream so a teardown from outside can close it.
func (s *vpnTCPSession) attach(stream io.ReadWriteCloser, cancel context.CancelFunc) {
	s.mu.Lock()
	if s.stream != nil {
		// Unreachable: one session opens one stream. Closing the newcomer rather
		// than the recorded one keeps a late dial from taking over a live splice.
		s.mu.Unlock()
		cancel()
		_ = stream.Close()
		return
	}
	s.stream, s.cancel = stream, cancel
	s.mu.Unlock()
}

// teardown ends the session from either side.
//
// Both resources are closed, and in this order, because each is what unblocks a
// different copy: closing the stream ends the read from the internal service, and
// closing the connection ends the read from the peer. Closing only one would leave
// the splice waiting on the other until the idle watchdog noticed.
func (s *vpnTCPSession) teardown() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		stream, cancel := s.stream, s.cancel
		s.stream, s.cancel = nil, nil
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if stream != nil {
			_ = stream.Close()
		}
		_ = s.conn.Close()
	})
}

// fail counts one refused or broken connection. At most one class is recorded per
// session: the first thing that went wrong is the one an operator needs.
func (s *vpnTCPSession) fail(class vpn.ErrorClass, reason string) {
	if !s.failed.CompareAndSwap(false, true) {
		return
	}
	s.gateway.denyPacket(vpnTCPLabel, s.packet, s.entry, class, reason)
}

// failUnlessClosing counts a failure unless the gateway is on its way out, in
// which case its own shutdown is the cause and naming the internal service would
// be wrong.
func (s *vpnTCPSession) failUnlessClosing(class vpn.ErrorClass, reason string) {
	if s.gateway.baseCtx.Err() != nil {
		return
	}
	s.fail(class, reason)
}

// countedConn wraps the tunnel side of a splice so the flow's counters and the
// byte metrics move while bytes move, rather than only when the connection ends.
//
// Only this side is wrapped. Counting both would report every byte twice, since a
// splice reads one and writes the other.
func (s *vpnTCPSession) countedConn() net.Conn {
	g := s.gateway
	return vpnCountedConn{
		Conn: s.conn,
		onRead: func(n int) {
			s.flow.AddSent(int64(n))
			s.flow.Touch()
			g.metrics.Bytes("ingress", vpnTCPLabel, int64(n))
		},
		onWrite: func(n int) {
			s.flow.AddReceived(int64(n))
			s.flow.Touch()
			g.metrics.Bytes("egress", vpnTCPLabel, int64(n))
		},
	}
}

// vpnCountedConn is a net.Conn that reports what passed through it.
type vpnCountedConn struct {
	net.Conn
	onRead  func(int)
	onWrite func(int)
}

func (c vpnCountedConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	if n > 0 {
		c.onRead(n)
	}
	return n, err
}

func (c vpnCountedConn) Write(buffer []byte) (int, error) {
	n, err := c.Conn.Write(buffer)
	if n > 0 {
		c.onWrite(n)
	}
	return n, err
}

// vpnTCPConnPeer reads the peer's tunnel address out of an accepted connection.
func vpnTCPConnPeer(remote net.Addr) (netip.Addr, uint16, bool) {
	if address, ok := remote.(*net.TCPAddr); ok {
		parsed := address.AddrPort()
		// Unmap because the peer table is keyed on the IPv4 addresses the pool
		// issues, and a four-in-six form of one would not be found in it.
		return parsed.Addr().Unmap(), uint16(parsed.Port()), parsed.Addr().IsValid()
	}
	parsed, err := netip.ParseAddrPort(remote.String())
	if err != nil {
		return netip.Addr{}, 0, false
	}
	return parsed.Addr().Unmap(), uint16(parsed.Port()), true
}

// vpnTCPConnPacket is the shape a denial record and a policy check need for a
// connection, built from the accepted socket rather than from a segment.
func vpnTCPConnPacket(remote netip.Addr, port uint16, target netip.AddrPort) vpnWirePacket {
	return vpnWirePacket{
		Version:  4,
		Protocol: vpn.ProtocolTCP,
		Src:      remote,
		Dst:      target.Addr(),
		SrcPort:  int(port),
		DstPort:  int(target.Port()),
	}
}

// vpnStackClass maps a stack-level failure onto its published label.
//
// A closed stack is reported as the egress being unavailable rather than as a
// stack error, because it is what a gateway that is shutting down looks like from
// here, and shutdown is not a fault an operator should be paged for.
func vpnStackClass(err error) vpn.ErrorClass {
	if errors.Is(err, errVPNStackClosed) {
		return vpn.ClassEgressUnavailable
	}
	return vpn.ClassStackError
}
