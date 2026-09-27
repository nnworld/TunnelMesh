package server

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

var (
	// errVPNPeerRowUnusable reports a stored row the gateway cannot turn into a
	// peer it is willing to serve.
	//
	// The alternative - loading it with whatever defaults the missing fields imply
	// - is the one that must never happen. An empty allowed_ips read back as an
	// empty allowlist is fail-closed, but a malformed one that parsed to nothing
	// and a missing public key both look like "a peer exists" from the management
	// API while carrying no traffic, which an operator can only diagnose by
	// reading the gateway log. Refusing the row puts the reason in that log at the
	// moment the row was applied.
	errVPNPeerRowUnusable = errors.New("vpn: the peer row cannot be served")
	// errVPNPeerTableClosed reports a write attempted after shutdown.
	errVPNPeerTableClosed = errors.New("vpn: the peer table is closed")
)

// vpnPeerEntry is one peer as the data plane holds it: the identity the WireGuard
// device needs, the routing facts the flow table needs, and the pre-built egress
// policy the packet path evaluates.
//
// The policy is built once, at apply time, rather than per packet. Constructing it
// runs the row through vpn.PeerSpec validation and parses its CIDRs, which is
// work that belongs to a management write and not to a hot path.
type vpnPeerEntry struct {
	PeerID    string
	PublicKey string
	VPNIP     netip.Addr
	NodeID    string
	AgentID   string
	Status    storage.VPNPeerStatus
	Policy    vpn.PacketPolicy
	// PeerLimit and RateLimit are the per-peer ceilings from the row. Zero means
	// "fall back to the gateway-wide server.vpn value", which is the same
	// convention the flow table and the token bucket already follow.
	PeerLimit   int
	RateLimit   int
	ICMPEnabled bool
	ExpiresAt   *time.Time
}

// usable reports whether a packet from this peer may be served right now, and
// which published error_class explains it when it may not.
//
// Both checks run at lookup time rather than at apply time. A peer can be revoked
// by a management write on another node, and it can simply age out while its
// tunnel is up; neither event produces an ApplyPeer call, and a policy that was
// evaluated once at load would keep serving both.
func (e vpnPeerEntry) usable(now time.Time) (vpn.ErrorClass, bool) {
	if e.Status != storage.VPNPeerStatusActive {
		return vpn.ClassPeerRevoked, false
	}
	if e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
		return vpn.ClassPeerExpired, false
	}
	return "", true
}

// vpnPeerHooks are the notifications the gateway needs to keep the WireGuard
// device and the flow table in step with the peer rows.
//
// They are called with no table lock held but under a writer lock that serialises
// them against each other, so the order a single ApplyPeer produces - drop the old
// key, then install the new one - is the order the device sees even when two
// management writes race. A hook must not call back into the table.
type vpnPeerHooks struct {
	// onApply reports a peer that should be serving: the gateway installs its
	// public key and address on the device.
	onApply func(entry vpnPeerEntry)
	// onRemove reports a peer that must stop serving: the gateway removes its key
	// from the device and tears down its flows. It fires for an explicit removal,
	// for a row whose status is no longer active, and for a peer displaced by
	// another one taking over its address.
	onRemove func(entry vpnPeerEntry)
	// onReplacePublicKey reports a rotation. The old key is passed first because
	// the device must be told to drop it before it is told about the new one: a
	// peer block that replaced the key in place would leave the old identity able
	// to complete a handshake until the device was next reloaded.
	onReplacePublicKey func(oldKey, newKey string, entry vpnPeerEntry)
}

// vpnPeerTableConfig carries what the table needs to build a policy and to fire
// hooks.
type vpnPeerTableConfig struct {
	// MTU is server.vpn.mtu. It belongs to the gateway rather than to a peer row
	// because a packet larger than the tunnel MTU cannot be forwarded no matter
	// which peer sent it.
	MTU int
	// NodeID, when set, refuses rows that belong to another server node. A node
	// must never install a peer whose traffic another gateway owns: the two would
	// both answer for the same public key and neither would be able to reach the
	// agent the row names.
	NodeID string
	Hooks  vpnPeerHooks
	Now    func() time.Time
}

// vpnPeerTable is the in-memory copy of this node's peer rows.
//
// The database stays the authority (D10). This table is a read-optimised index
// over it: the packet path resolves a source address to a peer and a policy with
// one read lock and no allocation, which is what makes a per-packet policy
// evaluation affordable. It can be rebuilt at any time from ListByNode, which is
// exactly what the gateway does when it starts and what repairs a table that fell
// behind the database.
//
// Two indexes are maintained together under one lock: by peer ID, which is what a
// management write names, and by VPN address, which is what a packet carries. They
// cannot disagree, because every mutation updates both and evicts whatever the
// address index pointed at before.
type vpnPeerTable struct {
	mtu    int
	nodeID string
	hooks  vpnPeerHooks
	nowFn  func() time.Time

	// writerMu serialises whole apply/remove operations, hooks included. It is
	// separate from mu so a hook that blocks - a UAPI write to the device - does
	// not stop the packet path from resolving a peer.
	writerMu sync.Mutex

	mu     sync.RWMutex
	peers  map[string]vpnPeerEntry
	byIP   map[netip.Addr]string
	closed bool
}

func newVPNPeerTable(cfg vpnPeerTableConfig) *vpnPeerTable {
	table := &vpnPeerTable{
		mtu:    cfg.MTU,
		nodeID: cfg.NodeID,
		hooks:  cfg.Hooks,
		nowFn:  cfg.Now,
		peers:  make(map[string]vpnPeerEntry),
		byIP:   make(map[netip.Addr]string),
	}
	if table.nowFn == nil {
		table.nowFn = func() time.Time { return time.Now().UTC() }
	}
	if table.mtu <= 0 {
		table.mtu = vpn.MinTunnelMTU
	}
	return table
}

func (t *vpnPeerTable) now() time.Time { return t.nowFn() }

// ApplyPeer installs or updates one row. It implements VPNPeerSink's write side.
//
// A row that cannot be served is refused rather than half-loaded, and the refusal
// is reported to the caller. The management service logs and audits it without
// rolling the write back: the row is already committed, so the database is right
// and the memory copy is behind, which a gateway restart repairs.
func (t *vpnPeerTable) ApplyPeer(row storage.VPNPeer) error {
	t.writerMu.Lock()
	defer t.writerMu.Unlock()

	if t.isClosed() {
		return errVPNPeerTableClosed
	}
	if t.nodeID != "" && row.NodeID != t.nodeID {
		return fmt.Errorf("%w: it belongs to node %q, not this node", errVPNPeerRowUnusable, "<another node>")
	}
	spec, err := vpnPeerSpecFromRow(row)
	if err != nil {
		return err
	}
	address, err := netip.ParseAddr(strings.TrimSpace(row.VPNIP))
	if err != nil || !address.Is4() {
		return fmt.Errorf("%w: vpn_ip must be a single ipv4 address", errVPNPeerRowUnusable)
	}
	address = address.Unmap()
	entry := vpnPeerEntry{
		PeerID:      row.ID,
		PublicKey:   row.PublicKey,
		VPNIP:       address,
		NodeID:      row.NodeID,
		AgentID:     row.AgentID,
		Status:      row.Status,
		PeerLimit:   row.MaxConcurrentFlows,
		RateLimit:   row.PacketRateLimit,
		ICMPEnabled: row.ICMPEnabled,
		ExpiresAt:   row.ExpiresAt,
	}
	// A policy is built only for a row that could serve a packet right now.
	// vpn.NewPacketPolicy validates the spec against the clock, so an already
	// expired row cannot produce one; it is still indexed, because a packet from
	// its address has to be reported as peer_expired rather than as an address
	// nobody ever heard of.
	if _, usable := entry.usable(t.now()); usable {
		policy, err := vpn.NewPacketPolicy(spec, t.mtu)
		if err != nil {
			return fmt.Errorf("%w: %v", errVPNPeerRowUnusable, err)
		}
		entry.Policy = policy
	}

	t.mu.Lock()
	previous, existed := t.peers[row.ID]
	var displaced *vpnPeerEntry
	if holder, ok := t.byIP[address]; ok && holder != row.ID {
		// The address moved to a different peer. The displaced entry has to go:
		// leaving it registered would mean one address resolving to two peers, and
		// a revocation of the old one would then tear down the new one's flows.
		if stale, found := t.peers[holder]; found {
			displaced = &stale
		}
		delete(t.peers, holder)
	}
	if existed && previous.VPNIP != address {
		if holder, ok := t.byIP[previous.VPNIP]; ok && holder == row.ID {
			delete(t.byIP, previous.VPNIP)
		}
	}
	replacedKey := ""
	if existed && previous.PublicKey != entry.PublicKey {
		replacedKey = previous.PublicKey
	}
	t.peers[row.ID] = entry
	t.byIP[address] = row.ID
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return errVPNPeerTableClosed
	}

	if displaced != nil && t.hooks.onRemove != nil {
		t.hooks.onRemove(*displaced)
	}
	if replacedKey != "" && t.hooks.onReplacePublicKey != nil {
		t.hooks.onReplacePublicKey(replacedKey, entry.PublicKey, entry)
	}
	if _, usable := entry.usable(t.now()); !usable {
		if t.hooks.onRemove != nil {
			t.hooks.onRemove(entry)
		}
	} else if t.hooks.onApply != nil {
		t.hooks.onApply(entry)
	}
	return nil
}

// RemovePeer drops one peer and reports it to the gateway, which removes its key
// from the device and closes its flows.
//
// Removing a peer that is not registered is not an error: a revocation and a
// shutdown can both ask for the same peer, and neither can know which ran first.
func (t *vpnPeerTable) RemovePeer(peerID string) error {
	t.writerMu.Lock()
	defer t.writerMu.Unlock()

	t.mu.Lock()
	entry, ok := t.peers[peerID]
	if !ok {
		t.mu.Unlock()
		return nil
	}
	delete(t.peers, peerID)
	if holder, found := t.byIP[entry.VPNIP]; found && holder == peerID {
		delete(t.byIP, entry.VPNIP)
	}
	t.mu.Unlock()

	if t.hooks.onRemove != nil {
		t.hooks.onRemove(entry)
	}
	return nil
}

// lookupByIP resolves the source address of a decrypted packet to a peer.
//
// The class it returns alongside a negative answer is one of the three published
// peer classes, so the packet path can count the refusal without a second lookup
// and without guessing which of "never issued", "revoked" and "expired" it was.
// The entry is returned even on a refusal, because the aggregated audit event
// names the peer that was refused.
func (t *vpnPeerTable) lookupByIP(address netip.Addr) (vpnPeerEntry, vpn.ErrorClass, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return vpnPeerEntry{}, vpn.ClassPeerUnknown, false
	}
	peerID, ok := t.byIP[address.Unmap()]
	if !ok {
		return vpnPeerEntry{}, vpn.ClassPeerUnknown, false
	}
	entry := t.peers[peerID]
	if class, usable := entry.usable(t.now()); !usable {
		return entry, class, false
	}
	return entry, "", true
}

// lookupByID returns one peer as the table holds it, whatever its status.
func (t *vpnPeerTable) lookupByID(peerID string) (vpnPeerEntry, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	entry, ok := t.peers[peerID]
	return entry, ok
}

// entries returns every row the table holds, including the ones that are not
// serving. The gateway uses it to render a full device configuration at startup
// and to decide which keys a reload has to remove.
func (t *vpnPeerTable) entries() []vpnPeerEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]vpnPeerEntry, 0, len(t.peers))
	for _, entry := range t.peers {
		out = append(out, entry)
	}
	return out
}

// count reports how many rows the table holds.
func (t *vpnPeerTable) count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.peers)
}

// activeCount reports how many of them could serve a packet right now.
func (t *vpnPeerTable) activeCount() int {
	now := t.now()
	t.mu.RLock()
	defer t.mu.RUnlock()
	active := 0
	for _, entry := range t.peers {
		if _, usable := entry.usable(now); usable {
			active++
		}
	}
	return active
}

// vpnPeerSnapshot is the peer side of what GET /api/v1/vpn-nodes reports.
type vpnPeerSnapshot struct {
	// Active is the number of peers that could serve a packet now. It is the
	// number an operator means by "how many peers does this node have".
	Active int
	// Total includes rows that are disabled, revoked or expired. The difference
	// between the two is the number of identities this node still holds a key for
	// but is not serving, which is worth seeing rather than having to query.
	Total int
	// Addresses are the VPN addresses of the serving peers. It is the input the
	// node status needs to cross-check the subnet lease's derived allocated
	// counter against what is actually in the table.
	Addresses []netip.Addr
}

// SnapshotForNode projects the table onto the management API's node view.
func (t *vpnPeerTable) SnapshotForNode() vpnPeerSnapshot {
	now := t.now()
	t.mu.RLock()
	defer t.mu.RUnlock()
	snapshot := vpnPeerSnapshot{Total: len(t.peers), Addresses: make([]netip.Addr, 0, len(t.peers))}
	for _, entry := range t.peers {
		if _, usable := entry.usable(now); usable {
			snapshot.Active++
			snapshot.Addresses = append(snapshot.Addresses, entry.VPNIP)
		}
	}
	return snapshot
}

// close empties the table and refuses later writes. It is idempotent.
//
// The hooks are deliberately not fired. Close runs while the gateway is shutting
// down, after the device has stopped accepting configuration and the flow table
// has already been closed; telling either of them about every peer on the way out
// would be noise at best and a write to a closed device at worst.
func (t *vpnPeerTable) close() {
	t.writerMu.Lock()
	defer t.writerMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	t.peers = make(map[string]vpnPeerEntry)
	t.byIP = make(map[netip.Addr]string)
}

func (t *vpnPeerTable) isClosed() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.closed
}

// vpnPeerSpecFromRow rebuilds the validated spec a stored row was created from.
//
// It re-validates rather than trusting the row. The database is the authority on
// what was requested, not on what is safe to serve now: a row written by an older
// version, restored from a backup, or edited by hand has to meet the same rules a
// new issue does before the gateway will carry its packets.
func vpnPeerSpecFromRow(row storage.VPNPeer) (vpn.PeerSpec, error) {
	if strings.TrimSpace(row.ID) == "" {
		return vpn.PeerSpec{}, fmt.Errorf("%w: it has no id", errVPNPeerRowUnusable)
	}
	if err := vpn.ValidatePublicKey(row.PublicKey); err != nil {
		// The message never carries the supplied value: these errors are logged and
		// audited, and a public key is exactly the kind of text that ends up pasted
		// into a ticket.
		return vpn.PeerSpec{}, fmt.Errorf("%w: its public key is unusable: %v", errVPNPeerRowUnusable, err)
	}
	if strings.TrimSpace(row.VPNIP) == "" {
		return vpn.PeerSpec{}, fmt.Errorf("%w: it has no vpn address", errVPNPeerRowUnusable)
	}
	networks, err := vpn.ParseAllowedIPs(row.AllowedIPs)
	if err != nil {
		return vpn.PeerSpec{}, fmt.Errorf("%w: %v", errVPNPeerRowUnusable, err)
	}
	if len(networks) == 0 {
		return vpn.PeerSpec{}, fmt.Errorf("%w: its allowed_ips is empty, so it could reach nothing and would read as a broken tunnel", errVPNPeerRowUnusable)
	}
	ports, err := vpn.ParseAllowedPorts(row.AllowedPorts)
	if err != nil {
		return vpn.PeerSpec{}, fmt.Errorf("%w: %v", errVPNPeerRowUnusable, err)
	}
	address, err := netip.ParseAddr(strings.TrimSpace(row.VPNIP))
	if err != nil || !address.Is4() {
		return vpn.PeerSpec{}, fmt.Errorf("%w: its vpn address must be a single ipv4 address", errVPNPeerRowUnusable)
	}
	spec := vpn.PeerSpec{
		Name:                row.Name,
		Description:         row.Description,
		PublicKey:           row.PublicKey,
		VPNIP:               net.IP(address.Unmap().AsSlice()),
		NodeID:              row.NodeID,
		AgentID:             row.AgentID,
		AllowedIPs:          networks,
		AllowedPorts:        ports,
		AllowPrivateTargets: row.AllowPrivateTargets,
		ICMPEnabled:         row.ICMPEnabled,
		MaxConcurrentFlows:  row.MaxConcurrentFlows,
		PacketRateLimit:     row.PacketRateLimit,
		ExpiresAt:           row.ExpiresAt,
	}
	if err := spec.ValidateFields(); err != nil {
		return vpn.PeerSpec{}, fmt.Errorf("%w: %v", errVPNPeerRowUnusable, err)
	}
	return spec, nil
}
