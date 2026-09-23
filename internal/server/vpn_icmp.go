//go:build vpn

package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

const (
	// vpnICMPDefaultMaxConcurrent and vpnICMPDefaultTimeout mirror the loader's
	// defaults for server.vpn.icmp_max_concurrent and icmp_timeout, so a gateway
	// assembled without a validated configuration behaves like one assembled with
	// it. config.ValidateVPN already requires both to be positive whenever echo is
	// enabled; these are the fallback for a caller that skipped the validator.
	vpnICMPDefaultMaxConcurrent = 64
	vpnICMPDefaultTimeout       = 5 * time.Second
	// vpnICMPReadChunk is one Read from an agent stream. The reply is a single
	// small JSON datagram, so the chunk is sized for it rather than for the
	// protocol's datagram bound; accumulation is what handles a relay that hands
	// the datagram over in pieces.
	vpnICMPReadChunk = 1024
)

// errVPNICMPReplyTooLarge reports an agent that kept writing past the protocol's
// datagram bound. It is distinct from a decode failure because the remedy differs:
// a truncated or malformed reply is one lost echo, an unbounded one is a peer of
// the gateway misusing a stream.
var errVPNICMPReplyTooLarge = errors.New("vpn: the icmp echo reply is larger than the protocol allows")

// vpnICMPRelay carries ICMP echo between tunnel peers and internal hosts.
//
// Echo is relayed rather than answered (D3) because the answer has to come from
// the internal network: a gateway that answered its own pings would prove the
// tunnel is up while saying nothing about the host the peer asked about, which is
// the one thing a user runs ping for.
//
// Each in-flight echo holds one agent-side stream, so it is accounted as a flow
// (D16): it appears in the flows endpoint as "icmp-echo", it is charged against
// the peer's and the gateway's flow ceilings, and it additionally holds one slot
// of the node-level semaphore. Both ceilings refuse rather than queue, because a
// queue would turn a limit meant to contain a flood into a delay applied to
// everybody else's echoes.
type vpnICMPRelay struct {
	gateway   *vpnGateway
	semaphore chan struct{}

	// instance makes correlation identifiers unique across process restarts, and
	// counter makes them unique within one. An agent that still holds a pending
	// echo from a previous life of this gateway must not be able to match a new
	// request to it.
	instance string
	counter  atomic.Uint64
}

func newVPNICMPRelay(gateway *vpnGateway) *vpnICMPRelay {
	limit := gateway.deps.Config.ICMPMaxConcurrent
	if limit <= 0 {
		limit = vpnICMPDefaultMaxConcurrent
	}
	return &vpnICMPRelay{
		gateway:   gateway,
		semaphore: make(chan struct{}, limit),
		instance:  newVPNICMPInstanceID(),
	}
}

// newVPNICMPInstanceID returns this relay's short random identity.
//
// A clock-based fallback is kept because the identifier only has to be unlikely
// to collide, and refusing to relay echoes because the system entropy source
// failed would be a disproportionate answer.
func newVPNICMPInstanceID() string {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// inFlight reports how many echo slots are held right now.
func (r *vpnICMPRelay) inFlight() int { return len(r.semaphore) }

// acquire takes one echo slot without waiting. Refusing immediately is the point
// of the ceiling: an echo a peer sent five seconds ago is not an echo it wants an
// answer to now.
func (r *vpnICMPRelay) acquire() bool {
	select {
	case r.semaphore <- struct{}{}:
		return true
	default:
		return false
	}
}

// release gives one slot back. It is called exactly once per successful acquire,
// which the echo's own once-guard is what guarantees.
func (r *vpnICMPRelay) release() {
	select {
	case <-r.semaphore:
	default:
	}
}

// correlationID allocates the gateway's handle for one in-flight echo.
func (r *vpnICMPRelay) correlationID() string {
	return "vpnecho-" + r.instance + "-" + strconv.FormatUint(r.counter.Add(1), 36)
}

// vpnICMPEcho is one in-flight echo: the flow that accounts for it, the stream
// carrying it, and the header fields the reply has to be forged from.
type vpnICMPEcho struct {
	relay *vpnICMPRelay
	key   vpnFlowKey
	flow  *vpnFlow
	entry vpnPeerEntry

	// target and peer are the two addresses of the reply, and identifier and
	// sequence are the values the peer's own ping used. The identifiers are
	// restored from here rather than taken from the agent's answer: an
	// unprivileged ping socket has its identifier rewritten by the kernel, so what
	// comes back from the internal network is not what the peer sent, and a reply
	// carrying the wrong pair is a reply the peer silently discards.
	target     netip.Addr
	peer       netip.Addr
	identifier uint16
	sequence   uint16
	payload    []byte

	// correlation is the only field the agent echoes back verbatim, which is what
	// makes a late or duplicated answer attributable.
	correlation string

	// mu guards the three resources whose lifetime is decided by two goroutines:
	// this echo's own and whichever one closes its flow.
	mu     sync.Mutex
	stream io.ReadWriteCloser
	cancel context.CancelFunc
	timer  *time.Timer

	started     bool
	timedOut    atomic.Bool
	retiring    atomic.Bool
	failed      atomic.Bool
	releaseOnce sync.Once
}

// serve handles one allowed echo request.
//
// Everything that can refuse does so synchronously, on the decryption goroutine,
// before any resource is allocated: the node switch, the semaphore and the flow
// registration. Only the dial and the wait for an answer move to a goroutine,
// because those are bounded by timeouts a peer controls.
func (r *vpnICMPRelay) serve(_ context.Context, entry vpnPeerEntry, parsed vpnWirePacket) {
	g := r.gateway
	label := protocol.StreamProtocolICMPEcho

	// The node switch is re-read per packet rather than at construction so that
	// flipping server.vpn.icmp_enabled off and reloading takes effect on traffic
	// that is already arriving. The peer's own switch was enforced upstream, by the
	// policy the peer table built for it.
	if !g.deps.Config.ICMPEnabled {
		g.denyPacket(label, parsed, entry, vpn.ClassICMPUnsupported, "icmp echo is disabled on this gateway")
		return
	}
	if !r.acquire() {
		g.denyPacket(label, parsed, entry, vpn.ClassCapacityExhausted,
			"the gateway is already relaying its maximum number of icmp echoes")
		return
	}
	echo := &vpnICMPEcho{
		relay:       r,
		key:         vpnICMPTuple(entry, parsed),
		entry:       entry,
		target:      parsed.Dst,
		peer:        parsed.Src,
		identifier:  parsed.ICMPID,
		sequence:    parsed.ICMPSeq,
		payload:     append([]byte(nil), parsed.Payload...),
		correlation: r.correlationID(),
	}
	// The flow is registered before the dial starts, so an echo that is still
	// being opened is charged against the ceilings. Registering only on success
	// would let a peer whose egress agent is slow hold twice its budget in
	// half-open dials.
	flow, created, err := g.flows.register(vpnFlowRegistration{
		Key:       echo.key,
		Target:    parsed.Dst.String(),
		Port:      0,
		PeerLimit: entry.PeerLimit,
		// abort rather than retire: the table has already detached the flow when
		// it runs this, and calling retire from inside a table operation is how a
		// teardown ends up re-entering the once-guard that is running it.
		OnClose: echo.abort,
	})
	if err != nil {
		r.release()
		class := vpn.ClassCapacityExhausted
		if errors.Is(err, errVPNFlowTableClosed) {
			// A closed table is a gateway that is shutting down, not one that is
			// full. The distinction matters because capacity is a tuning problem
			// and shutdown is not a problem at all.
			class = vpn.ClassEgressUnavailable
		}
		g.denyPacket(label, parsed, entry, class, err.Error())
		return
	}
	if !created {
		r.release()
		// The same peer sent the same (identifier, sequence) twice while the first
		// is still in flight. The first will answer, so the duplicate is dropped
		// rather than relayed twice; capacity_exhausted is the published class for
		// "the gateway is holding less than the peer asked it to".
		g.denyPacket(label, parsed, entry, vpn.ClassCapacityExhausted, "an identical echo is already in flight")
		return
	}
	echo.flow = flow
	go echo.run(g.baseCtx)
}

// vpnICMPTuple builds the flow key of one echo.
//
// The ICMP identifier and sequence occupy the port slots of the key. An echo has
// no ports, and a key that left them at zero would collapse every echo a peer
// sends into one flow: the second ping of a burst would be refused as a duplicate
// of the first, and the console would show one flow for a whole ping run.
func vpnICMPTuple(entry vpnPeerEntry, parsed vpnWirePacket) vpnFlowKey {
	return vpnFlowKey{
		PeerID:   entry.PeerID,
		Protocol: protocol.StreamProtocolICMPEcho,
		Src:      netip.AddrPortFrom(parsed.Src, parsed.ICMPID),
		Dst:      netip.AddrPortFrom(parsed.Dst, parsed.ICMPSeq),
	}
}

// run opens the agent-side stream, asks the question and reports the answer.
//
// It runs on its own goroutine because both the capability probe and the dial can
// take as long as their timeouts, and serve runs on wireguard-go's decryption
// goroutine: blocking there would stall every peer the gateway serves.
func (e *vpnICMPEcho) run(ctx context.Context) {
	g := e.relay.gateway
	label := protocol.StreamProtocolICMPEcho
	defer e.retire()
	if e.aborted() {
		// The gateway closed, or the peer was revoked, between registration and this
		// goroutine starting. There is nobody left to answer.
		return
	}
	if !e.capable(ctx) {
		return
	}

	// The request is encoded before the stream is opened. Encoding cannot block,
	// and doing it first means a payload the protocol cannot carry is refused
	// without spending a dial on it.
	encoded, err := protocol.EncodeICMPEchoRequest(protocol.ICMPEchoRequest{
		CorrelationID: e.correlation,
		Identifier:    e.identifier,
		Sequence:      e.sequence,
		Data:          e.payload,
	})
	if err != nil {
		e.fail(vpn.ClassStackError, err.Error())
		return
	}

	stream, cancel, err := openVPNEgress(ctx, g.deps.Opener, relay.StreamRequest{
		NodeID:     g.deps.NodeID,
		AgentID:    e.entry.AgentID,
		Protocol:   label,
		TargetHost: e.target.String(),
		// An echo addresses a host. The protocol has no port for ICMP and the agent
		// does not read one, so zero is the honest value rather than a made-up one.
		TargetPort: 0,
	}, g.deps.Config.ConnectTimeout)
	if err != nil {
		if ctx.Err() != nil || e.aborted() {
			return
		}
		e.fail(vpnEgressClass(err), err.Error())
		return
	}
	// The timer starts with the stream, not with the packet: connect_timeout has
	// already bounded the dial, and icmp_timeout is the budget for the answer.
	timer := time.AfterFunc(e.timeout(), func() {
		e.timedOut.Store(true)
		e.abort()
	})
	if !e.attach(stream, cancel, timer) {
		return
	}
	g.metrics.Stream(label, "started", "")

	n, err := stream.Write(encoded)
	if n > 0 {
		e.flow.AddSent(int64(n))
		e.flow.Touch()
		g.metrics.Bytes("ingress", label, int64(n))
	}
	if err != nil {
		e.failUnlessClosing(ctx, vpn.ClassEgressUnavailable, err.Error())
		return
	}

	reply, err := e.readReply(stream)
	if err != nil {
		e.classifyReadFailure(ctx, err)
		return
	}
	if reply.CorrelationID != e.correlation {
		// One stream carries one echo, so an answer for another correlation is an
		// agent that mixed two up. Forwarding it would tell a peer something about
		// a host it did not ask about.
		e.fail(vpn.ClassStackError, "the agent answered a different echo")
		return
	}
	switch reply.Status {
	case protocol.ICMPEchoStatusOK:
		e.emit(reply)
	case protocol.ICMPEchoStatusTimeout:
		e.fail(vpn.ClassICMPTimeout, "the internal host did not answer the echo in time")
	case protocol.ICMPEchoStatusUnsupported:
		e.fail(vpn.ClassICMPUnsupported, "the egress agent cannot send icmp echo")
	case protocol.ICMPEchoStatusCapacityExhausted:
		e.fail(vpn.ClassCapacityExhausted, "the egress agent's echo budget is exhausted")
	case protocol.ICMPEchoStatusUnreachable:
		e.fail(vpn.ClassEgressUnavailable, "the egress agent could not reach the target")
	case protocol.ICMPEchoStatusCancelled:
		e.fail(vpn.ClassStackError, "the egress agent cancelled the echo")
	default:
		// Unreachable while protocol.DecodeICMPEchoReply refuses an unknown status.
		// It stays because a status added to the protocol without a case here must
		// fail closed rather than be reported as a success.
		e.fail(vpn.ClassStackError, "the agent answered with a status this gateway does not know")
	}
}

// capable re-asks the capability question at dial time (D16).
//
// The peer was issued with ICMP only if some agent advertised the capability, but
// that was at issue time: the agent may have reconnected since, or the peer may
// have been patched onto a different one. An explicit "unsupported" is refused
// here so the peer's ping fails with a count an operator can find, instead of
// spending a stream on a dial that cannot be served. "Unverified" is passed
// through, exactly as the management API passes it through: another node holds the
// connection, and refusing on "cannot see it" would make echo fail at random
// depending on which node a packet arrived at.
//
// The probe is bounded by icmp_timeout rather than by connect_timeout. An echo
// whose capability question takes longer than the echo itself is worth answering
// has already lost its peer, and holding a concurrency slot for a hung registry
// would let one dependency exhaust the node's whole ICMP budget.
func (e *vpnICMPEcho) capable(ctx context.Context) bool {
	g := e.relay.gateway
	probe := g.deps.Capabilities
	if probe == nil {
		return true
	}
	probeCtx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	state, err := probe.ProbeICMPEcho(probeCtx, e.entry.AgentID)
	if err != nil {
		// The probe's answer is what decides, not its error: a probe that could not
		// reach the registry says so, and the state it returns with the error is
		// still the honest one to act on.
		slog.Debug("vpn_icmp_capability_probe_failed", "agent_id", e.entry.AgentID, "error", err)
	}
	if state == CapabilityUnsupported {
		e.fail(vpn.ClassICMPUnsupported, "the egress agent does not answer icmp echo")
		return false
	}
	return true
}

// timeout is the budget for one answer.
func (e *vpnICMPEcho) timeout() time.Duration {
	timeout := e.relay.gateway.deps.Config.ICMPTimeout
	if timeout <= 0 {
		return vpnICMPDefaultTimeout
	}
	return timeout
}

// attach takes ownership of a freshly opened stream and reports whether the echo
// still exists to use it.
//
// The check under the mutex is what closes the race with abort: an echo revoked
// while its dial was in flight would otherwise store a stream nobody will ever
// close, and an agent-side socket that outlives the peer it was opened for is
// exactly what revocation is supposed to end.
func (e *vpnICMPEcho) attach(stream io.ReadWriteCloser, cancel context.CancelFunc, timer *time.Timer) bool {
	e.mu.Lock()
	if e.retiring.Load() {
		e.mu.Unlock()
		timer.Stop()
		cancel()
		_ = stream.Close()
		return false
	}
	e.stream, e.cancel, e.timer, e.started = stream, cancel, timer, true
	e.mu.Unlock()
	return true
}

// readReply collects the agent's single answer.
//
// The datagram is decoded after every Read rather than at EOF. A relay may hand
// one datagram over in pieces, and waiting for the stream to close would make the
// echo depend on the agent closing promptly - a dependency whose failure mode is a
// timeout that looks like the internal host being down.
func (e *vpnICMPEcho) readReply(stream io.Reader) (protocol.ICMPEchoReply, error) {
	g := e.relay.gateway
	label := protocol.StreamProtocolICMPEcho
	accumulated := make([]byte, 0, 256)
	chunk := make([]byte, vpnICMPReadChunk)
	for {
		n, err := stream.Read(chunk)
		if n > 0 {
			e.flow.AddReceived(int64(n))
			e.flow.Touch()
			g.metrics.Bytes("egress", label, int64(n))
			accumulated = append(accumulated, chunk[:n]...)
			if len(accumulated) > protocol.MaxDatagram {
				return protocol.ICMPEchoReply{}, errVPNICMPReplyTooLarge
			}
			if reply, decodeErr := protocol.DecodeICMPEchoReply(accumulated); decodeErr == nil {
				return reply, nil
			}
		}
		if err != nil {
			return protocol.ICMPEchoReply{}, err
		}
	}
}

// classifyReadFailure names a read that did not produce an answer.
func (e *vpnICMPEcho) classifyReadFailure(ctx context.Context, err error) {
	switch {
	case e.timedOut.Load():
		e.fail(vpn.ClassICMPTimeout, "the internal host did not answer within server.vpn.icmp_timeout")
	case e.aborted():
		// The flow was closed from outside - a revocation, the idle reaper,
		// shutdown. Whoever closed it already said why; counting a drop here would
		// report one event twice and blame the internal host for it.
	case errors.Is(err, errVPNICMPReplyTooLarge):
		e.fail(vpn.ClassStackError, err.Error())
	case errors.Is(err, io.EOF):
		e.fail(vpn.ClassEgressUnavailable, "the agent ended the echo without answering")
	case ctx.Err() != nil:
		// The gateway is shutting down. Its own teardown reports that; a drop
		// counted here would name the internal host as the cause.
	default:
		e.fail(vpn.ClassStackError, err.Error())
	}
}

// failUnlessClosing counts a failure unless the gateway is on its way out.
func (e *vpnICMPEcho) failUnlessClosing(ctx context.Context, class vpn.ErrorClass, reason string) {
	if ctx.Err() != nil || e.aborted() {
		return
	}
	e.fail(class, reason)
}

// fail counts one refused echo and folds it into the audit window. It records at
// most one class per echo: the first thing that went wrong is the one an operator
// needs, and a cascade of counters for one lost ping would make the metric
// useless for telling a timeout from a refusal.
func (e *vpnICMPEcho) fail(class vpn.ErrorClass, reason string) {
	if !e.failed.CompareAndSwap(false, true) {
		return
	}
	e.relay.gateway.denyPacket(protocol.StreamProtocolICMPEcho, e.denialPacket(), e.entry, class, reason)
}

// denialPacket rebuilds the shape a denial record needs. The payload is never
// carried: an audit entry names the target and the /24 it lives in, and nothing
// else about what a peer sent.
func (e *vpnICMPEcho) denialPacket() vpnWirePacket {
	return vpnWirePacket{
		Version:  4,
		Protocol: vpn.ProtocolICMP,
		Src:      e.peer,
		Dst:      e.target,
		ICMPType: 8,
		ICMPID:   e.identifier,
		ICMPSeq:  e.sequence,
	}
}

// emit forges the echo reply and hands it to the device.
//
// The reply is built here rather than routed through netstack, for the reason D3
// gives for UDP: the stack has never heard of either address, and asking it to
// send would produce a route lookup failure or an ICMP error the gateway did not
// decide to send. This is also the only ICMP datagram the gateway ever emits, and
// it emits it only for an echo it relayed, which is what keeps the promise that a
// peer cannot make it generate an error on its behalf.
func (e *vpnICMPEcho) emit(reply protocol.ICMPEchoReply) {
	g := e.relay.gateway
	label := protocol.StreamProtocolICMPEcho
	size := vpnWireIPv4HeaderMinimum + vpnWireICMPHeader + len(reply.Data)
	if size > g.mtu {
		// The tunnel cannot carry it and the gateway does not fragment, so the reply
		// is dropped where the peer can at least see it in the metrics. The request
		// fitted, so an answer that does not means the internal host padded it.
		g.denyPacket(label, e.denialPacket(), e.entry, vpn.ClassOversizeDropped,
			"the echo reply does not fit the tunnel mtu and was not fragmented")
		return
	}
	packet := buildIPv4ICMPEchoReply(e.target, e.peer, e.identifier, e.sequence, reply.Data)
	if err := g.device.emit(packet); err != nil {
		// The device's outbound queue is bounded, and a full one means the
		// WireGuard side is not keeping up. Dropping is the honest answer for a
		// datagram protocol; the peer's ping simply does not get an answer.
		g.denyPacket(label, e.denialPacket(), e.entry, vpn.ClassCapacityExhausted,
			"the outbound queue of the tunnel device is full")
		slog.Debug("vpn_icmp_emit_failed", "peer_id", e.entry.PeerID, "error", err)
	}
}

// buildIPv4ICMPEchoReply assembles one complete echo reply with correct
// checksums.
//
// The ICMP checksum is computed rather than left at zero. Unlike UDP, where zero
// means "not computed", a zero ICMP checksum is simply wrong, and a peer's stack
// would discard the reply without a word - which from the user's side is
// indistinguishable from the internal host being down.
func buildIPv4ICMPEchoReply(source, destination netip.Addr, identifier, sequence uint16, payload []byte) []byte {
	total := vpnWireIPv4HeaderMinimum + vpnWireICMPHeader + len(payload)
	packet := make([]byte, total)
	packet[0] = 4<<4 | vpnWireIPv4HeaderMinimum/4
	binary.BigEndian.PutUint16(packet[2:4], uint16(total))
	packet[8] = 64 // ttl
	packet[9] = vpn.ProtocolICMP
	copy(packet[12:16], source.AsSlice())
	copy(packet[16:20], destination.AsSlice())
	binary.BigEndian.PutUint16(packet[10:12], vpnIPv4Checksum(packet[:vpnWireIPv4HeaderMinimum], 0))

	// Type 0 code 0 is echo reply. The identifier and sequence are the peer's, and
	// the payload is what the internal host echoed back: ping compares it, so
	// substituting the request's bytes would hide a host that answered with
	// something else.
	message := packet[vpnWireIPv4HeaderMinimum:]
	message[0] = 0
	message[1] = 0
	binary.BigEndian.PutUint16(message[4:6], identifier)
	binary.BigEndian.PutUint16(message[6:8], sequence)
	copy(message[vpnWireICMPHeader:], payload)
	binary.BigEndian.PutUint16(message[2:4], vpnIPv4Checksum(message, 0))
	return packet
}

// aborted reports whether the echo has been torn down.
func (e *vpnICMPEcho) aborted() bool { return e.retiring.Load() }

// abort releases everything the echo holds except its flow-table entry.
//
// It is the flow's teardown hook, so every reason an echo can end reaches it: the
// answer arriving, the timeout firing, a peer being revoked, the idle reaper and
// gateway shutdown. Releasing the semaphore here rather than in run is what makes
// a revocation give the slot back even though run is blocked in a Read.
func (e *vpnICMPEcho) abort() {
	// Set before the mutex is taken: attach checks this flag while holding the same
	// mutex, so an echo torn down during its dial closes the stream it was handed
	// instead of storing it where nothing will ever look again.
	e.retiring.Store(true)
	e.releaseOnce.Do(func() {
		e.mu.Lock()
		stream, cancel, timer, started := e.stream, e.cancel, e.timer, e.started
		e.stream, e.cancel, e.timer = nil, nil, nil
		e.mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		if cancel != nil {
			cancel()
		}
		if stream != nil {
			// Closing is what unblocks a Read that is waiting for an answer that is
			// never coming. An agent-side stream left open would keep its echo
			// against the agent's own budget for as long as the session lives.
			_ = stream.Close()
		}
		if started {
			e.relay.gateway.metrics.Stream(protocol.StreamProtocolICMPEcho, "closed", "")
		}
		e.relay.release()
	})
}

// retire ends the echo: it releases the echo's own resources and then takes its
// flow out of the table.
//
// The order matters. Removing the entry fires the flow's teardown hook, which is
// abort, so doing it the other way round would leave a window where the flow still
// counts against the peer's budget but nothing can release it any more.
func (e *vpnICMPEcho) retire() {
	e.abort()
	e.relay.gateway.flows.remove(e.key)
}
