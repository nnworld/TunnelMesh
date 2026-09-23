package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

var (
	// errVPNFlowCapacity reports that a flow could not be registered because a
	// ceiling was reached. It maps onto the published capacity_exhausted class,
	// which is the same class the device's bounded outbound queue reports: both
	// mean "the gateway is holding less than the peer asked it to", and an
	// operator reading the metric should not have to know which of the two it was.
	errVPNFlowCapacity = errors.New("vpn: the flow limit is reached")
	// errVPNFlowTableClosed reports a registration attempted after shutdown. It is
	// distinct from the capacity error because the remedy differs: a full table
	// may drain, a closed one never will.
	errVPNFlowTableClosed = errors.New("vpn: the flow table is closed")
	// errVPNEgressUnavailable reports that no agent stream could be opened.
	errVPNEgressUnavailable = errors.New("vpn: no agent transport is available")
	// errVPNEgressTimeout reports an agent that did not accept the stream inside
	// server.vpn.connect_timeout.
	errVPNEgressTimeout = errors.New("vpn: the agent did not accept the stream in time")
)

// defaultVPNConnectTimeout bounds a stream open when the configuration does not
// say otherwise. It matches the server.vpn.connect_timeout default so a gateway
// assembled without a loader behaves like one assembled with it.
const defaultVPNConnectTimeout = 10 * time.Second

// vpnFlowKey identifies one flow: the peer that sent it, the protocol it carries
// and both endpoints of the tunnel-side four-tuple.
//
// The peer is part of the key even though the source address already implies it.
// Two peers can hold the same VPN address only through a bug, and keying on the
// peer means such a bug produces two flows that can be counted and closed
// independently instead of one flow whose state two owners fight over.
type vpnFlowKey struct {
	PeerID   string
	Protocol string
	Src      netip.AddrPort
	Dst      netip.AddrPort
}

func (k vpnFlowKey) String() string {
	return k.PeerID + " " + k.Protocol + " " + k.Src.String() + " -> " + k.Dst.String()
}

// vpnFlowRegistration is one request to add a flow.
type vpnFlowRegistration struct {
	Key    vpnFlowKey
	Target string
	Port   int
	// PeerLimit overrides the table default for this flow's peer. It comes from
	// vpn_peers.max_concurrent_flows, so a peer the operator gave a smaller budget
	// is held to it; zero means "use the gateway-wide server.vpn.max_flows_per_peer".
	PeerLimit int
	// OnClose runs exactly once, when the flow leaves the table for any reason:
	// an explicit remove, the idle reaper, a peer being revoked or the gateway
	// shutting down. The handler closes the agent-side stream with it, which is
	// what makes a revocation take effect on traffic that is already flowing.
	OnClose func()
}

// vpnFlow is one registered flow and its counters.
//
// The counters are atomics rather than fields behind the table lock because they
// are written on every packet of a live flow and read by the management API. A
// shared lock would make one peer's console refresh contend with another peer's
// data path.
type vpnFlow struct {
	id        string
	key       vpnFlowKey
	target    string
	port      int
	startedAt time.Time

	lastSeen  atomic.Int64
	bytesSent atomic.Int64
	bytesRecv atomic.Int64

	onClose   func()
	closeOnce sync.Once
	closed    atomic.Bool
}

// ID is the flow's public identifier. It is generated once and never changes, so
// an operator watching the console can follow one flow across refreshes.
func (f *vpnFlow) ID() string { return f.id }

// Protocol is the tunnel protocol the flow carries.
func (f *vpnFlow) Protocol() string { return f.key.Protocol }

// Closed reports whether the flow has left the table.
func (f *vpnFlow) Closed() bool { return f.closed.Load() }

// Touch records activity. The idle reaper reads it, so a flow that is carrying
// bytes is never reaped even if it never sends one in the other direction.
func (f *vpnFlow) Touch() { f.lastSeen.Store(time.Now().UnixNano()) }

// AddSent counts bytes the peer sent towards the internal service.
func (f *vpnFlow) AddSent(n int64) {
	if n > 0 {
		f.bytesSent.Add(n)
	}
}

// AddReceived counts bytes the internal service sent back to the peer.
func (f *vpnFlow) AddReceived(n int64) {
	if n > 0 {
		f.bytesRecv.Add(n)
	}
}

// Close runs the flow's teardown exactly once. It does not touch the table: the
// table owns registration, and having both directions call each other is how a
// teardown ends up deadlocked on the lock it is being called under.
func (f *vpnFlow) Close() {
	f.closeOnce.Do(func() {
		f.closed.Store(true)
		if f.onClose != nil {
			f.onClose()
		}
	})
}

// idleFor reports how long the flow has been inactive as of now.
func (f *vpnFlow) idleFor(now time.Time) time.Duration {
	last := time.Unix(0, f.lastSeen.Load())
	return now.Sub(last)
}

// vpnFlowTableConfig carries the ceilings and the clock.
type vpnFlowTableConfig struct {
	// MaxFlowsPerPeer and MaxFlowsTotal follow the configuration convention that
	// zero means unlimited, so a deployment that sets neither inherits no
	// artificial ceiling.
	MaxFlowsPerPeer int
	MaxFlowsTotal   int
	IdleTimeout     time.Duration
	Now             func() time.Time
	NewID           func() string
}

// vpnFlowTable is the gateway's runtime flow state.
//
// It is the authority on how many flows exist, which is what makes the per-peer
// and global ceilings enforceable under concurrency: the check and the insert
// happen under one lock, so two goroutines cannot both see room for one more.
// Nothing here is persisted, per the design spec: a flow is a property of a live
// tunnel, and a table that survived a restart would describe connections that no
// longer exist.
type vpnFlowTable struct {
	perPeerLimit int
	totalLimit   int
	idle         time.Duration
	nowFn        func() time.Time
	newID        func() string

	mu     sync.Mutex
	flows  map[vpnFlowKey]*vpnFlow
	byPeer map[string]map[vpnFlowKey]struct{}
	closed bool
}

func newVPNFlowTable(cfg vpnFlowTableConfig) *vpnFlowTable {
	table := &vpnFlowTable{
		perPeerLimit: cfg.MaxFlowsPerPeer,
		totalLimit:   cfg.MaxFlowsTotal,
		idle:         cfg.IdleTimeout,
		nowFn:        cfg.Now,
		newID:        cfg.NewID,
		flows:        make(map[vpnFlowKey]*vpnFlow),
		byPeer:       make(map[string]map[vpnFlowKey]struct{}),
	}
	if table.nowFn == nil {
		table.nowFn = func() time.Time { return time.Now().UTC() }
	}
	if table.newID == nil {
		table.newID = newVPNFlowID
	}
	if table.perPeerLimit < 0 {
		table.perPeerLimit = 0
	}
	if table.totalLimit < 0 {
		table.totalLimit = 0
	}
	if table.idle < 0 {
		table.idle = 0
	}
	return table
}

// newVPNFlowID generates a flow identifier.
//
// It is not a database ID and carries no meaning beyond uniqueness inside one
// process lifetime: flows are never persisted, so an identifier that survives a
// restart would point at nothing.
func newVPNFlowID() string {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "vpnflow-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "vpnflow-" + base64.RawURLEncoding.EncodeToString(raw)
}

func (t *vpnFlowTable) now() time.Time { return t.nowFn() }

// register adds a flow, or returns the one already registered for the key.
//
// Returning the existing flow instead of an error is what makes the packet path
// safe under concurrency: two segments of one connection can arrive on different
// WireGuard decryption goroutines, and the loser of that race must be able to
// proceed with the winner's flow rather than drop the segment. The boolean tells
// the caller whether it owns the registration and must therefore attach its own
// resources to it.
func (t *vpnFlowTable) register(reg vpnFlowRegistration) (*vpnFlow, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, false, errVPNFlowTableClosed
	}
	if existing, ok := t.flows[reg.Key]; ok {
		return existing, false, nil
	}
	limit := reg.PeerLimit
	if limit < 0 {
		limit = 0
	}
	if limit == 0 {
		limit = t.perPeerLimit
	}
	if limit > 0 && len(t.byPeer[reg.Key.PeerID]) >= limit {
		return nil, false, fmt.Errorf("%w: peer %s holds %d flows", errVPNFlowCapacity, reg.Key.PeerID, limit)
	}
	if t.totalLimit > 0 && len(t.flows) >= t.totalLimit {
		return nil, false, fmt.Errorf("%w: the gateway holds %d flows", errVPNFlowCapacity, t.totalLimit)
	}
	now := t.now()
	flow := &vpnFlow{
		id:        t.newID(),
		key:       reg.Key,
		target:    reg.Target,
		port:      reg.Port,
		startedAt: now,
		onClose:   reg.OnClose,
	}
	flow.lastSeen.Store(now.UnixNano())
	t.flows[reg.Key] = flow
	peers, ok := t.byPeer[reg.Key.PeerID]
	if !ok {
		peers = make(map[vpnFlowKey]struct{})
		t.byPeer[reg.Key.PeerID] = peers
	}
	peers[reg.Key] = struct{}{}
	return flow, true, nil
}

// lookup finds a registered flow.
func (t *vpnFlowTable) lookup(key vpnFlowKey) (*vpnFlow, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	flow, ok := t.flows[key]
	return flow, ok
}

// remove unregisters a flow and runs its teardown. Removing a key that is not
// registered is not an error: the idle reaper, a protocol handler finishing and
// shutdown all call this, and none of them can know whether another already did.
func (t *vpnFlowTable) remove(key vpnFlowKey) {
	t.mu.Lock()
	flow := t.detachLocked(key)
	t.mu.Unlock()
	if flow != nil {
		flow.Close()
	}
}

// detachLocked unregisters a flow without closing it and returns it. The caller
// holds mu and owns the teardown, which is what lets the bulk operations close
// flows outside the lock.
func (t *vpnFlowTable) detachLocked(key vpnFlowKey) *vpnFlow {
	flow, ok := t.flows[key]
	if !ok {
		return nil
	}
	delete(t.flows, key)
	if peers, found := t.byPeer[key.PeerID]; found {
		delete(peers, key)
		if len(peers) == 0 {
			delete(t.byPeer, key.PeerID)
		}
	}
	return flow
}

// closePeer tears down every flow of one peer and reports how many there were.
//
// It is what makes revocation immediate: the management API has already committed
// the new status, and the flows of a peer that no longer exists must not keep
// carrying bytes to an internal service.
func (t *vpnFlowTable) closePeer(peerID string) int {
	t.mu.Lock()
	var flows []*vpnFlow
	if peers, ok := t.byPeer[peerID]; ok {
		for key := range peers {
			if flow := t.detachLocked(key); flow != nil {
				flows = append(flows, flow)
			}
		}
		delete(t.byPeer, peerID)
	}
	t.mu.Unlock()
	for _, flow := range flows {
		flow.Close()
	}
	return len(flows)
}

// reapIdle closes the flows that have been inactive for at least the idle
// timeout and reports how many were closed.
//
// Teardown runs after the lock is released. A handler closes an agent-side stream
// there, which can block on a socket, and holding the table lock across it would
// stall the packet path of every other peer for the duration.
func (t *vpnFlowTable) reapIdle() int {
	if t.idle <= 0 {
		return 0
	}
	now := t.now()
	t.mu.Lock()
	var expired []*vpnFlow
	for key, flow := range t.flows {
		if flow.idleFor(now) >= t.idle {
			if detached := t.detachLocked(key); detached != nil {
				expired = append(expired, detached)
			}
		}
	}
	t.mu.Unlock()
	for _, flow := range expired {
		flow.Close()
	}
	return len(expired)
}

// count reports how many flows the gateway holds.
func (t *vpnFlowTable) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.flows)
}

// countPeer reports how many flows one peer holds.
func (t *vpnFlowTable) countPeer(peerID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byPeer[peerID])
}

// snapshot projects one peer's flows onto the management API shape.
//
// The result is ordered by start time so a console refresh does not reshuffle the
// table, and it carries no payload, no key material and no agent-side socket
// detail: it is the same narrow projection VPNFlowSnapshot declares.
func (t *vpnFlowTable) snapshot(peerID string) []VPNFlowSnapshot {
	t.mu.Lock()
	keys := make([]vpnFlowKey, 0, len(t.byPeer[peerID]))
	for key := range t.byPeer[peerID] {
		keys = append(keys, key)
	}
	flows := make([]*vpnFlow, 0, len(keys))
	for _, key := range keys {
		if flow, ok := t.flows[key]; ok {
			flows = append(flows, flow)
		}
	}
	t.mu.Unlock()

	sort.Slice(flows, func(i, j int) bool {
		if flows[i].startedAt.Equal(flows[j].startedAt) {
			return flows[i].id < flows[j].id
		}
		return flows[i].startedAt.Before(flows[j].startedAt)
	})
	items := make([]VPNFlowSnapshot, 0, len(flows))
	for _, flow := range flows {
		items = append(items, VPNFlowSnapshot{
			ID:            flow.id,
			Protocol:      flow.key.Protocol,
			Target:        flow.target,
			Port:          flow.port,
			StartedAt:     flow.startedAt,
			BytesSent:     flow.bytesSent.Load(),
			BytesReceived: flow.bytesRecv.Load(),
		})
	}
	return items
}

// close tears down every flow and refuses later registrations. It is idempotent
// and reports how many flows it closed, so the shutdown path can say what it did.
func (t *vpnFlowTable) close() int {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return 0
	}
	t.closed = true
	flows := make([]*vpnFlow, 0, len(t.flows))
	for key := range t.flows {
		if flow := t.detachLocked(key); flow != nil {
			flows = append(flows, flow)
		}
	}
	t.mu.Unlock()
	for _, flow := range flows {
		flow.Close()
	}
	return len(flows)
}

// openVPNEgress dials the agent-side stream a VPN flow needs and bounds the open
// with a timer.
//
// It is the gateway's counterpart of ProxyEntry.openEgress and shares that
// function's two hard-won properties, without sharing its code:
//
//   - A deadline context is not used for the open. relay.GRPCNodeTransport creates
//     the client stream from the context it is handed, so a deadline that fires
//     after a successful open would kill a healthy tunnel. The caller owns the
//     returned cancel func for the flow's whole lifetime.
//   - A stream that arrives after the timeout is closed rather than dropped. An
//     abandoned open is still an open on the agent side, and leaving it would leak
//     one internal connection per timed-out dial.
func openVPNEgress(ctx context.Context, opener relay.NodeTransport, request relay.StreamRequest, timeout time.Duration) (io.ReadWriteCloser, context.CancelFunc, error) {
	if opener == nil {
		return nil, func() {}, errVPNEgressUnavailable
	}
	if timeout <= 0 {
		timeout = defaultVPNConnectTimeout
	}
	openCtx, cancelOpen := context.WithCancel(ctx)
	type openResult struct {
		stream io.ReadWriteCloser
		err    error
	}
	results := make(chan openResult, 1)
	go func() {
		stream, err := opener.OpenStream(openCtx, request)
		results <- openResult{stream: stream, err: err}
	}()
	// abandon cancels the dial and reaps a stream that arrives late.
	abandon := func() {
		cancelOpen()
		go func() {
			if res := <-results; res.stream != nil {
				_ = res.stream.Close()
			}
		}()
	}

	target := net.JoinHostPort(request.TargetHost, strconv.Itoa(request.TargetPort))
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-results:
		if res.err != nil {
			cancelOpen()
			slog.Warn("vpn_egress_open_failed",
				"agent_id", request.AgentID, "protocol", request.Protocol, "target", target, "error", res.err)
			return nil, func() {}, errVPNEgressUnavailable
		}
		return res.stream, cancelOpen, nil
	case <-timer.C:
		abandon()
		slog.Warn("vpn_egress_open_timeout",
			"agent_id", request.AgentID, "protocol", request.Protocol, "target", target, "timeout", timeout.String())
		return nil, func() {}, errVPNEgressTimeout
	case <-ctx.Done():
		abandon()
		return nil, func() {}, ctx.Err()
	}
}
