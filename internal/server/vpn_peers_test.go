package server

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

const (
	vpnPeerKeyA = "3p7bfXt9wbTTW2HC7OQ1Nz+DQ8hbeGdNrfx+FG+IK08="
	vpnPeerKeyB = "BAsSGSAnLjU8Q0pRWF9mbXR7gomQl56lrLO6wcjP1t0="
	vpnPeerKeyC = "IyoxOD9GTVRbYmlwd36FjJOaoaivtr3Ey9LZ4Ofu9QE="
	vpnPeerKeyD = "Ym98iZajsL3K1+TxAxAdKjdEUV5reIWSn6y5xtPg7fo="
)

// recordingPeerHooks captures the callbacks the gateway relies on to keep the
// WireGuard device and the flow table in step with the peer rows.
type recordingPeerHooks struct {
	mu       sync.Mutex
	applied  []vpnPeerEntry
	removed  []vpnPeerEntry
	replaced [][2]string
}

func (h *recordingPeerHooks) hooks() vpnPeerHooks {
	return vpnPeerHooks{
		onApply:  func(entry vpnPeerEntry) { h.record(func() { h.applied = append(h.applied, entry) }) },
		onRemove: func(entry vpnPeerEntry) { h.record(func() { h.removed = append(h.removed, entry) }) },
		onReplacePublicKey: func(oldKey, newKey string, _ vpnPeerEntry) {
			h.record(func() { h.replaced = append(h.replaced, [2]string{oldKey, newKey}) })
		},
	}
}

func (h *recordingPeerHooks) record(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn()
}

func (h *recordingPeerHooks) applyCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.applied)
}

func (h *recordingPeerHooks) removeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.removed)
}

func (h *recordingPeerHooks) replacements() [][2]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][2]string(nil), h.replaced...)
}

func vpnPeerRow(peerID, publicKey, vpnIP string) storage.VPNPeer {
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	return storage.VPNPeer{
		ID:                  peerID,
		Name:                "laptop-" + peerID,
		OwnerID:             "user-owner",
		PublicKey:           publicKey,
		VPNIP:               vpnIP,
		NodeID:              "server-node-1",
		AgentID:             "agent-1",
		AllowedIPs:          "10.0.0.0/8,192.168.0.0/16",
		AllowedPorts:        "443,8080",
		AllowPrivateTargets: true,
		ICMPEnabled:         true,
		MaxConcurrentFlows:  32,
		PacketRateLimit:     1000,
		ExpiresAt:           &expires,
		Status:              storage.VPNPeerStatusActive,
	}
}

func newVPNPeerTableFixture(t *testing.T) (*vpnPeerTable, *recordingPeerHooks, *controllableClock) {
	t.Helper()
	clock := newControllableClock(time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC))
	hooks := &recordingPeerHooks{}
	table := newVPNPeerTable(vpnPeerTableConfig{MTU: 1420, Hooks: hooks.hooks(), Now: clock.Now})
	t.Cleanup(func() { table.close() })
	return table, hooks, clock
}

func TestVPNPeerTableAppliesAndResolvesAPeerByItsAddress(t *testing.T) {
	table, hooks, _ := newVPNPeerTableFixture(t)

	if err := table.ApplyPeer(vpnPeerRow("peer-a", vpnPeerKeyA, "10.64.0.7")); err != nil {
		t.Fatalf("ApplyPeer() error = %v, want nil", err)
	}
	entry, class, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.7"))
	if !ok {
		t.Fatalf("lookupByIP() failed with class %q, want the applied peer", class)
	}
	if entry.PeerID != "peer-a" || entry.PublicKey != vpnPeerKeyA || entry.AgentID != "agent-1" {
		t.Errorf("entry = %+v, want peer-a with its key and agent", entry)
	}
	if entry.PeerLimit != 32 || entry.RateLimit != 1000 || !entry.ICMPEnabled {
		t.Errorf("limits = %d/%d icmp=%v, want 32/1000/true", entry.PeerLimit, entry.RateLimit, entry.ICMPEnabled)
	}
	if hooks.applyCount() != 1 {
		t.Errorf("onApply ran %d times, want 1", hooks.applyCount())
	}

	// The policy carried by the entry is the one the packet path evaluates, so a
	// permitted packet and a refused one must both be decided here rather than by a
	// second copy of the rules.
	allowed := entry.Policy.Allow(vpn.Packet{
		Protocol: vpn.ProtocolTCP,
		Source:   net.ParseIP("10.64.0.7"),
		Dest:     net.ParseIP("10.1.2.3"),
		Port:     443,
		Size:     120,
	})
	if allowed != nil {
		t.Errorf("Allow(permitted) = %v, want nil", allowed)
	}
	refused := entry.Policy.Allow(vpn.Packet{
		Protocol: vpn.ProtocolTCP,
		Source:   net.ParseIP("10.64.0.7"),
		Dest:     net.ParseIP("10.1.2.3"),
		Port:     22,
		Size:     120,
	})
	if refused == nil {
		t.Fatal("Allow(port 22) = nil, want a port denial")
	}
	var packetErr *vpn.PacketError
	if !errors.As(refused, &packetErr) {
		t.Fatalf("Allow(port 22) returned %T, want a *vpn.PacketError", refused)
	}
	if packetErr.ErrorClass() != vpn.ClassPortDenied {
		t.Errorf("class = %q, want %q", packetErr.ErrorClass(), vpn.ClassPortDenied)
	}
}

func TestVPNPeerTableReportsUnknownDisabledAndExpiredPeers(t *testing.T) {
	table, _, clock := newVPNPeerTableFixture(t)

	if _, class, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.99")); ok || class != vpn.ClassPeerUnknown {
		t.Errorf("an address that was never applied = %v, %q; want not found with %q", ok, class, vpn.ClassPeerUnknown)
	}

	disabled := vpnPeerRow("peer-disabled", vpnPeerKeyA, "10.64.0.8")
	disabled.Status = storage.VPNPeerStatusDisabled
	if err := table.ApplyPeer(disabled); err != nil {
		t.Fatalf("ApplyPeer(disabled) error = %v, want nil", err)
	}
	if _, class, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.8")); ok || class != vpn.ClassPeerRevoked {
		t.Errorf("a disabled peer = %v, %q; want not found with %q", ok, class, vpn.ClassPeerRevoked)
	}

	revoked := vpnPeerRow("peer-revoked", vpnPeerKeyB, "10.64.0.9")
	revoked.Status = storage.VPNPeerStatusRevoked
	if err := table.ApplyPeer(revoked); err != nil {
		t.Fatalf("ApplyPeer(revoked) error = %v, want nil", err)
	}
	if _, class, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.9")); ok || class != vpn.ClassPeerRevoked {
		t.Errorf("a revoked peer = %v, %q; want not found with %q", ok, class, vpn.ClassPeerRevoked)
	}

	expiring := vpnPeerRow("peer-expiring", vpnPeerKeyC, "10.64.0.10")
	if err := table.ApplyPeer(expiring); err != nil {
		t.Fatalf("ApplyPeer(expiring) error = %v, want nil", err)
	}
	if _, _, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.10")); !ok {
		t.Fatal("a peer whose expiry is in the future was refused")
	}
	// The expiry is evaluated at lookup time, not at apply time: a peer that ages
	// out while its tunnel is up must stop being served without a reload.
	clock.Advance(400 * 24 * time.Hour)
	if _, class, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.10")); ok || class != vpn.ClassPeerExpired {
		t.Errorf("an expired peer = %v, %q; want not found with %q", ok, class, vpn.ClassPeerExpired)
	}
}

func TestVPNPeerTableReplacesTheIndexOfAReusedAddress(t *testing.T) {
	table, hooks, _ := newVPNPeerTableFixture(t)

	if err := table.ApplyPeer(vpnPeerRow("peer-old", vpnPeerKeyA, "10.64.0.7")); err != nil {
		t.Fatalf("ApplyPeer(old) error = %v", err)
	}
	if err := table.ApplyPeer(vpnPeerRow("peer-new", vpnPeerKeyB, "10.64.0.7")); err != nil {
		t.Fatalf("ApplyPeer(new) error = %v", err)
	}

	entry, _, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.7"))
	if !ok {
		t.Fatal("lookupByIP() found nothing after the address moved")
	}
	if entry.PeerID != "peer-new" {
		t.Errorf("PeerID = %q, want the peer that most recently claimed the address", entry.PeerID)
	}
	// The displaced peer must not stay reachable under its own ID with the address
	// it no longer holds, or a revocation of it would tear down the new peer's flows.
	if _, found := table.lookupByID("peer-old"); found {
		t.Error("the displaced peer is still registered")
	}
	if table.count() != 1 {
		t.Errorf("count() = %d, want 1", table.count())
	}
	if hooks.removeCount() != 1 {
		t.Errorf("onRemove ran %d times, want 1 for the peer the address was taken from", hooks.removeCount())
	}
}

func TestVPNPeerTableRemovePeerClearsTheIndexAndNotifiesOnce(t *testing.T) {
	table, hooks, _ := newVPNPeerTableFixture(t)

	if err := table.ApplyPeer(vpnPeerRow("peer-a", vpnPeerKeyA, "10.64.0.7")); err != nil {
		t.Fatalf("ApplyPeer() error = %v", err)
	}
	if err := table.RemovePeer("peer-a"); err != nil {
		t.Fatalf("RemovePeer() error = %v, want nil", err)
	}
	if _, class, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.7")); ok || class != vpn.ClassPeerUnknown {
		t.Errorf("a removed peer = %v, %q; want not found with %q", ok, class, vpn.ClassPeerUnknown)
	}
	if _, found := table.lookupByID("peer-a"); found {
		t.Error("lookupByID still finds a removed peer")
	}
	if hooks.removeCount() != 1 {
		t.Errorf("onRemove ran %d times, want exactly 1", hooks.removeCount())
	}
	// Removing an unknown peer is not an error and must not notify: shutdown and a
	// late revocation can both ask for the same peer, and neither knows which ran.
	if err := table.RemovePeer("peer-a"); err != nil {
		t.Errorf("a second RemovePeer() error = %v, want nil", err)
	}
	if err := table.RemovePeer("peer-never-seen"); err != nil {
		t.Errorf("RemovePeer(unknown) error = %v, want nil", err)
	}
	if hooks.removeCount() != 1 {
		t.Errorf("onRemove ran %d times after repeated removals, want 1", hooks.removeCount())
	}
}

func TestVPNPeerTableReportsAPublicKeyReplacement(t *testing.T) {
	table, hooks, _ := newVPNPeerTableFixture(t)

	if err := table.ApplyPeer(vpnPeerRow("peer-a", vpnPeerKeyA, "10.64.0.7")); err != nil {
		t.Fatalf("ApplyPeer() error = %v", err)
	}
	rotated := vpnPeerRow("peer-a", vpnPeerKeyB, "10.64.0.7")
	if err := table.ApplyPeer(rotated); err != nil {
		t.Fatalf("ApplyPeer(rotated) error = %v", err)
	}

	replacements := hooks.replacements()
	if len(replacements) != 1 {
		t.Fatalf("onReplacePublicKey ran %d times, want 1", len(replacements))
	}
	if replacements[0] != [2]string{vpnPeerKeyA, vpnPeerKeyB} {
		t.Errorf("onReplacePublicKey(%q, %q), want the old key first so the device can drop it", replacements[0][0], replacements[0][1])
	}
	entry, _, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.7"))
	if !ok || entry.PublicKey != vpnPeerKeyB {
		t.Errorf("entry = %+v, %v; want the rotated key", entry, ok)
	}
	// Re-applying the same row changes nothing and must not report a replacement:
	// the gateway reloads the whole table on a restart and would otherwise tell the
	// device to drop every key it just installed.
	if err := table.ApplyPeer(rotated); err != nil {
		t.Fatalf("ApplyPeer(unchanged) error = %v", err)
	}
	if len(hooks.replacements()) != 1 {
		t.Errorf("onReplacePublicKey ran %d times after a no-op apply, want 1", len(hooks.replacements()))
	}
}

func TestVPNPeerTableRefusesARowItCannotTurnIntoAClosedPolicy(t *testing.T) {
	table, hooks, _ := newVPNPeerTableFixture(t)

	cases := map[string]func(*storage.VPNPeer){
		"empty allowed_ips":    func(row *storage.VPNPeer) { row.AllowedIPs = "" },
		"blank allowed_ips":    func(row *storage.VPNPeer) { row.AllowedIPs = "   " },
		"malformed cidr":       func(row *storage.VPNPeer) { row.AllowedIPs = "10.0.0.0/8,not-a-cidr" },
		"ipv6 cidr":            func(row *storage.VPNPeer) { row.AllowedIPs = "fd00::/8" },
		"malformed ports":      func(row *storage.VPNPeer) { row.AllowedPorts = "80-90" },
		"missing public key":   func(row *storage.VPNPeer) { row.PublicKey = "" },
		"invalid public key":   func(row *storage.VPNPeer) { row.PublicKey = "not base64 at all" },
		"missing vpn address":  func(row *storage.VPNPeer) { row.VPNIP = "" },
		"non-ipv4 vpn address": func(row *storage.VPNPeer) { row.VPNIP = "fd00::1" },
		"missing agent":        func(row *storage.VPNPeer) { row.AgentID = "" },
		"missing name":         func(row *storage.VPNPeer) { row.Name = "" },
		"missing peer id":      func(row *storage.VPNPeer) { row.ID = "" },
	}
	for name, mutate := range cases {
		row := vpnPeerRow("peer-a", vpnPeerKeyA, "10.64.0.7")
		mutate(&row)
		err := table.ApplyPeer(row)
		if err == nil {
			t.Errorf("%s: ApplyPeer() error = nil, want a refusal", name)
			continue
		}
		if _, _, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.7")); ok {
			t.Errorf("%s: the refused row was indexed anyway", name)
		}
	}
	if hooks.applyCount() != 0 {
		t.Errorf("onApply ran %d times for rows that were all refused, want 0", hooks.applyCount())
	}
	// An empty allowlist must never become an allow-all policy. It is refused
	// rather than loaded as deny-all because a peer that is active and can reach
	// nothing is indistinguishable, from the user's side, from a broken tunnel.
	row := vpnPeerRow("peer-a", vpnPeerKeyA, "10.64.0.7")
	row.AllowedIPs = ""
	if err := table.ApplyPeer(row); err == nil {
		t.Fatal("an empty allowlist was accepted")
	}
}

func TestVPNPeerTableSnapshotReportsTheNodeView(t *testing.T) {
	table, _, _ := newVPNPeerTableFixture(t)

	for index, key := range []string{vpnPeerKeyA, vpnPeerKeyB, vpnPeerKeyC} {
		if err := table.ApplyPeer(vpnPeerRow("peer-"+itoa(index), key, "10.64.0."+itoa(index+1))); err != nil {
			t.Fatalf("ApplyPeer(%d) error = %v", index, err)
		}
	}
	revoked := vpnPeerRow("peer-revoked", vpnPeerKeyD, "10.64.0.50")
	revoked.Status = storage.VPNPeerStatusRevoked
	if err := table.ApplyPeer(revoked); err != nil {
		t.Fatalf("ApplyPeer(revoked) error = %v", err)
	}

	snapshot := table.SnapshotForNode()
	if snapshot.Active != 3 {
		t.Errorf("Active = %d, want 3: a revoked row is not a serving peer", snapshot.Active)
	}
	if snapshot.Total != 4 {
		t.Errorf("Total = %d, want 4", snapshot.Total)
	}
	if len(snapshot.Addresses) != 3 {
		t.Fatalf("Addresses has %d entries, want the 3 serving ones", len(snapshot.Addresses))
	}
	seen := map[string]bool{}
	for _, addr := range snapshot.Addresses {
		seen[addr.String()] = true
	}
	for _, want := range []string{"10.64.0.1", "10.64.0.2", "10.64.0.3"} {
		if !seen[want] {
			t.Errorf("Addresses is missing %s", want)
		}
	}
	if seen["10.64.0.50"] {
		t.Error("Addresses contains a revoked peer's address")
	}
}

func TestVPNPeerTableConcurrentApplyIsSerialised(t *testing.T) {
	table, _, _ := newVPNPeerTableFixture(t)

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for index := range 32 {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			row := vpnPeerRow("peer-"+itoa(index%4), vpnPeerKeyA, "10.64.0."+itoa(index%4+1))
			if err := table.ApplyPeer(row); err != nil {
				t.Errorf("ApplyPeer() error = %v", err)
			}
		}()
	}
	start.Done()
	done.Wait()

	if table.count() != 4 {
		t.Errorf("count() = %d, want 4 distinct peers", table.count())
	}
	for index := range 4 {
		if _, _, ok := table.lookupByIP(netip.MustParseAddr("10.64.0." + itoa(index+1))); !ok {
			t.Errorf("the address of peer-%d was lost to a concurrent apply", index)
		}
	}
}

func TestVPNPeerTableCloseRefusesLaterWrites(t *testing.T) {
	table, _, _ := newVPNPeerTableFixture(t)
	if err := table.ApplyPeer(vpnPeerRow("peer-a", vpnPeerKeyA, "10.64.0.7")); err != nil {
		t.Fatalf("ApplyPeer() error = %v", err)
	}

	table.close()
	if err := table.ApplyPeer(vpnPeerRow("peer-b", vpnPeerKeyB, "10.64.0.8")); err == nil {
		t.Error("ApplyPeer() after close = nil, want a refusal")
	}
	if _, _, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.7")); ok {
		t.Error("a closed table still resolves peers")
	}
	table.close() // idempotent
}

func TestVPNPeerTableRefusesARowOfAnotherNode(t *testing.T) {
	clock := newControllableClock(time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC))
	hooks := &recordingPeerHooks{}
	table := newVPNPeerTable(vpnPeerTableConfig{
		MTU:    1420,
		NodeID: "server-node-1",
		Hooks:  hooks.hooks(),
		Now:    clock.Now,
	})
	t.Cleanup(func() { table.close() })

	foreign := vpnPeerRow("peer-foreign", vpnPeerKeyA, "10.64.0.7")
	foreign.NodeID = "server-node-2"
	if err := table.ApplyPeer(foreign); err == nil {
		t.Fatal("ApplyPeer() accepted a row belonging to another node")
	}
	if _, _, ok := table.lookupByIP(netip.MustParseAddr("10.64.0.7")); ok {
		t.Error("the foreign row was indexed anyway")
	}
	if hooks.applyCount() != 0 || hooks.removeCount() != 0 {
		t.Errorf("hooks fired %d/%d times for a refused row, want 0/0", hooks.applyCount(), hooks.removeCount())
	}
	if err := table.ApplyPeer(vpnPeerRow("peer-own", vpnPeerKeyB, "10.64.0.8")); err != nil {
		t.Errorf("ApplyPeer(own node) error = %v, want nil", err)
	}
}
