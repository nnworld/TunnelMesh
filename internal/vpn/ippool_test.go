package vpn_test

import (
	"net"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

func mustPool(t *testing.T, cidr string, size int) vpn.Pool {
	t.Helper()
	pool, err := vpn.ParsePool(cidr, size)
	if err != nil {
		t.Fatalf("ParsePool(%q, %d) = %v", cidr, size, err)
	}
	return pool
}

func TestParsePoolCarvesNodeSubnets(t *testing.T) {
	pool := mustPool(t, "10.64.0.0/16", 24)
	subnets := pool.Subnets()
	if len(subnets) != 256 {
		t.Fatalf("subnet count = %d, want 256", len(subnets))
	}
	if got := subnets[0].String(); got != "10.64.0.0/24" {
		t.Errorf("first subnet = %s, want 10.64.0.0/24", got)
	}
	if got := subnets[len(subnets)-1].String(); got != "10.64.255.0/24" {
		t.Errorf("last subnet = %s, want 10.64.255.0/24", got)
	}
	// Carving must partition the pool exactly: sorted, contiguous and
	// non-overlapping. A gap would strand addresses nobody can ever be given,
	// and an overlap would let two nodes lease the same space.
	previous := pool.Network().IP
	for i, subnet := range subnets {
		if !subnet.IP.Equal(previous) {
			t.Fatalf("subnet %d starts at %s, want %s", i, subnet.IP, previous)
		}
		ones, bits := subnet.Mask.Size()
		if ones != 24 || bits != 32 {
			t.Fatalf("subnet %d prefix = %d/%d, want /24", i, ones, bits)
		}
		previous = nextNetwork(subnet)
	}
	if !previous.Equal(nextNetwork(pool.Network())) {
		t.Fatalf("carving ended at %s, want the pool boundary %s", previous, nextNetwork(pool.Network()))
	}
}

func TestParsePoolRejectsInvalidConfiguration(t *testing.T) {
	cases := map[string]struct {
		cidr string
		size int
	}{
		"not a cidr":                 {"10.64.0.0", 24},
		"empty cidr":                 {"", 24},
		"garbage cidr":               {"ten-sixty-four", 24},
		"ipv6 pool":                  {"fd00::/64", 72},
		"ipv4-mapped ipv6 pool":      {"::ffff:10.64.0.0/112", 120},
		"subnet size equal to pool":  {"10.64.0.0/16", 16},
		"subnet size smaller":        {"10.64.0.0/16", 8},
		"subnet size zero":           {"10.64.0.0/16", 0},
		"negative subnet size":       {"10.64.0.0/16", -1},
		"subnet size 31 has no host": {"10.64.0.0/16", 31},
		"subnet size 32 has no host": {"10.64.0.0/16", 32},
		// A /8 carved into /24s is 65536 subnets. Materialising that from one
		// typo'd configuration line is a memory exhaustion vector, so the count
		// is capped.
		"too many subnets": {"10.0.0.0/8", 24},
		// Link-local, unspecified and multicast pools would hand peers
		// addresses that can never route.
		"link local pool":     {"169.254.0.0/16", 24},
		"unspecified pool":    {"0.0.0.0/8", 16},
		"multicast pool":      {"224.0.0.0/4", 12},
		"metadata address":    {"169.254.169.254/32", 32},
		"public range 100.65": {"100.65.0.0/16", 24}, // valid: CGNAT space is fine
	}
	for name, tc := range cases {
		pool, err := vpn.ParsePool(tc.cidr, tc.size)
		if name == "public range 100.65" {
			if err != nil {
				t.Errorf("%s: shared address space must be accepted: %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: ParsePool(%q, %d) accepted an invalid pool", name, tc.cidr, tc.size)
			continue
		}
		apiErr, ok := err.(*vpn.Error)
		if !ok {
			t.Errorf("%s: error %v is not a *vpn.Error", name, err)
			continue
		}
		if apiErr.Code != "vpn_ip_pool_invalid" || apiErr.Status != 400 {
			t.Errorf("%s: got %d/%s, want 400/vpn_ip_pool_invalid", name, apiErr.Status, apiErr.Code)
		}
		_ = pool
	}
}

func TestParsePoolAcceptsTheSubnetCountBoundary(t *testing.T) {
	// 4096 is the documented cap: /8 to /20 is exactly 2^12 subnets and must
	// be accepted, while one more bit must not.
	if _, err := vpn.ParsePool("10.0.0.0/8", 20); err != nil {
		t.Fatalf("4096 subnets must be accepted: %v", err)
	}
	if _, err := vpn.ParsePool("10.0.0.0/8", 21); err == nil {
		t.Fatal("8192 subnets must be rejected")
	}
	// The narrowest legal pool is a /24 carved into /30s: 256 addresses over 4
	// addresses per subnet.
	pool := mustPool(t, "10.64.0.0/24", 30)
	if got := len(pool.Subnets()); got != 64 {
		t.Fatalf("/24 into /30 gives %d subnets, want 64", got)
	}
}

func TestParsePoolMasksAnUnalignedNetworkAddress(t *testing.T) {
	// net.ParseCIDR semantics: the host bits of the configured address are
	// ignored rather than rejected, so "10.64.7.9/16" behaves like the pool
	// an operator meant to write.
	pool := mustPool(t, "10.64.7.9/16", 24)
	if got := pool.Network().String(); got != "10.64.0.0/16" {
		t.Fatalf("network = %s, want 10.64.0.0/16", got)
	}
}

func TestPoolNodeSubnetValidatesAgainstThePool(t *testing.T) {
	pool := mustPool(t, "10.64.0.0/16", 24)
	subnet, err := pool.NodeSubnet("10.64.5.0/24")
	if err != nil {
		t.Fatalf("NodeSubnet: %v", err)
	}
	if subnet.String() != "10.64.5.0/24" {
		t.Fatalf("subnet = %s, want 10.64.5.0/24", subnet.String())
	}
	for name, cidr := range map[string]string{
		"outside the pool": "10.65.0.0/24",
		"wrong prefix":     "10.64.5.0/25",
		"wider than pool":  "10.64.0.0/8",
		"not a cidr":       "10.64.5.0",
		"empty":            "",
		"ipv6":             "fd00::/64",
		"public space":     "8.8.8.0/24",
		"unaligned host":   "10.64.5.7/24",
		"link local":       "169.254.5.0/24",
	} {
		if _, err := pool.NodeSubnet(cidr); err == nil {
			t.Errorf("%s: NodeSubnet(%q) was accepted", name, cidr)
		}
	}
}

func TestPoolNodeAddressReservesTheFirstUsableAddress(t *testing.T) {
	pool := mustPool(t, "10.64.0.0/16", 24)
	subnet, err := pool.NodeSubnet("10.64.5.0/24")
	if err != nil {
		t.Fatalf("NodeSubnet: %v", err)
	}
	nodeAddress, err := pool.NodeAddress(subnet)
	if err != nil {
		t.Fatalf("NodeAddress: %v", err)
	}
	// The node's own VPN interface address. It is reserved rather than handed
	// out, otherwise the gateway and the first peer would both claim 10.64.5.1
	// and the tunnel would black-hole.
	if got := nodeAddress.String(); got != "10.64.5.1" {
		t.Fatalf("node address = %s, want 10.64.5.1", got)
	}
	// The reserved address must never be allocatable, even when nothing else is
	// taken.
	allocated, err := pool.AllocateAddress(subnet, func(net.IP) bool { return false })
	if err != nil {
		t.Fatalf("AllocateAddress: %v", err)
	}
	if allocated.Equal(nodeAddress) {
		t.Fatal("AllocateAddress handed out the node's own address")
	}
	if got := allocated.String(); got != "10.64.5.2" {
		t.Fatalf("first allocation = %s, want 10.64.5.2", got)
	}
}

func TestPoolAllocateAddressSkipsTakenNetworkAndBroadcast(t *testing.T) {
	pool := mustPool(t, "10.64.0.0/16", 24)
	subnet, err := pool.NodeSubnet("10.64.5.0/24")
	if err != nil {
		t.Fatalf("NodeSubnet: %v", err)
	}
	taken := map[string]bool{"10.64.5.2": true, "10.64.5.3": true, "10.64.5.4": true}
	got, err := pool.AllocateAddress(subnet, func(ip net.IP) bool { return taken[ip.String()] })
	if err != nil {
		t.Fatalf("AllocateAddress: %v", err)
	}
	if got.String() != "10.64.5.5" {
		t.Fatalf("allocation = %s, want 10.64.5.5", got)
	}

	// The broadcast address must never be handed out, so declaring every other
	// address free leaves nothing allocatable.
	broadcast := net.ParseIP("10.64.5.255")
	if got, err := pool.AllocateAddress(subnet, func(ip net.IP) bool {
		return !ip.Equal(broadcast)
	}); err == nil {
		t.Fatalf("expected exhaustion when only the broadcast address is free, got %s", got)
	}
}

func TestPoolAllocateAddressReportsExhaustion(t *testing.T) {
	pool := mustPool(t, "10.64.0.0/16", 24)
	subnet, err := pool.NodeSubnet("10.64.5.0/24")
	if err != nil {
		t.Fatalf("NodeSubnet: %v", err)
	}
	_, err = pool.AllocateAddress(subnet, func(net.IP) bool { return true })
	if err == nil {
		t.Fatal("expected exhaustion when every address is taken")
	}
	apiErr, ok := err.(*vpn.Error)
	if !ok {
		t.Fatalf("error %v is not a *vpn.Error", err)
	}
	if apiErr.Code != "vpn_ip_pool_exhausted" || apiErr.Status != 409 {
		t.Fatalf("got %d/%s, want 409/vpn_ip_pool_exhausted", apiErr.Status, apiErr.Code)
	}

	// Allocation must consult the caller's occupancy callback rather than
	// deciding on its own, because the authoritative fact that an address is
	// taken lives in vpn_peers.UNIQUE(node_id, vpn_ip).
	var probed []string
	if _, err := pool.AllocateAddress(subnet, func(ip net.IP) bool {
		probed = append(probed, ip.String())
		return false
	}); err != nil {
		t.Fatalf("AllocateAddress: %v", err)
	}
	if len(probed) == 0 {
		t.Fatal("the occupancy callback was never consulted")
	}
	if probed[0] == "10.64.5.0" {
		t.Fatal("the network address must not be probed")
	}
}

func TestPoolAllocateAddressOnTheSmallestLegalSubnet(t *testing.T) {
	pool := mustPool(t, "10.64.0.0/24", 30)
	subnet, err := pool.NodeSubnet("10.64.0.4/30")
	if err != nil {
		t.Fatalf("NodeSubnet: %v", err)
	}
	// A /30 holds .4 (network), .5 and .6 (usable), .7 (broadcast). With .5
	// reserved for the node interface exactly one address remains for peers.
	nodeAddress, err := pool.NodeAddress(subnet)
	if err != nil {
		t.Fatalf("NodeAddress: %v", err)
	}
	if nodeAddress.String() != "10.64.0.5" {
		t.Fatalf("node address = %s, want 10.64.0.5", nodeAddress)
	}
	got, err := pool.AllocateAddress(subnet, func(net.IP) bool { return false })
	if err != nil {
		t.Fatalf("AllocateAddress: %v", err)
	}
	if got.String() != "10.64.0.6" {
		t.Fatalf("allocation = %s, want 10.64.0.6", got)
	}
	if _, err := pool.AllocateAddress(subnet, func(ip net.IP) bool {
		return ip.Equal(net.ParseIP("10.64.0.6"))
	}); err == nil {
		t.Fatal("a /30 with its only host address taken must report exhaustion")
	}
}

func TestPoolContains(t *testing.T) {
	pool := mustPool(t, "10.64.0.0/16", 24)
	for _, raw := range []string{"10.64.0.0", "10.64.0.1", "10.64.255.255", "10.64.128.7"} {
		if !pool.Contains(net.ParseIP(raw)) {
			t.Errorf("%s must be inside the pool", raw)
		}
	}
	for _, raw := range []string{"10.63.255.255", "10.65.0.0", "8.8.8.8", "192.168.1.1"} {
		if pool.Contains(net.ParseIP(raw)) {
			t.Errorf("%s must be outside the pool", raw)
		}
	}
	if pool.Contains(nil) {
		t.Error("a nil address must not be inside the pool")
	}
	if pool.Contains(net.ParseIP("fd00::1")) {
		t.Error("an IPv6 address must not be inside an IPv4 pool")
	}
}

// TestSubnetsReturnsACopy guards against a caller mutating the carved list and
// silently changing what every later allocation can see.
func TestSubnetsReturnsACopy(t *testing.T) {
	pool := mustPool(t, "10.64.0.0/16", 24)
	first := pool.Subnets()
	first[0] = nil
	second := pool.Subnets()
	if second[0] == nil || second[0].String() != "10.64.0.0/24" {
		t.Fatal("mutating a returned slice must not affect the pool")
	}
}

func nextNetwork(n *net.IPNet) net.IP {
	ip := n.IP.To4()
	out := make(net.IP, len(ip))
	copy(out, ip)
	ones, _ := n.Mask.Size()
	// Add 2^(32-ones) to the network address, carrying across the four bytes.
	carry := 1 << (32 - ones)
	for i := 3; i >= 0 && carry > 0; i-- {
		sum := int(out[i]) + carry&0xff
		out[i] = byte(sum & 0xff)
		carry = (carry >> 8) + (sum >> 8)
	}
	return out
}
