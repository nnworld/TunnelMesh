package vpn

import (
	"fmt"
	"net"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

const (
	// maxNodeSubnets bounds how many subnets one pool may be carved into.
	// Without it a single typo such as ip_pool "10.0.0.0/8" with
	// node_subnet_size 24 would materialise 65536 *net.IPNet at startup, so one
	// misconfiguration line becomes a memory exhaustion vector.
	maxNodeSubnets = 4096

	// maxNodeSubnetBits is /30, the narrowest prefix that still holds two
	// usable host addresses: one reserved for the node's own VPN interface and
	// one left for a peer. A /31 has no host addresses at all.
	maxNodeSubnetBits = 30

	// maxPoolBits is /24. A pool narrower than this cannot be subdivided, since
	// a node subnet must be strictly smaller than the pool.
	maxPoolBits = 24

	ipv4Bits = 32
)

// Pool is server.vpn.ip_pool carved into per-node subnets of
// server.vpn.node_subnet_size.
//
// It is a pure value: parsing validates the configuration once, and every later
// method is arithmetic on the parsed result. It never queries the database.
// Occupancy is supplied by the caller as a callback because the authoritative
// fact that an address is taken is vpn_peers.UNIQUE(node_id, vpn_ip); a Pool
// that kept its own idea of occupancy would be a second source of truth that
// can only drift from the first.
type Pool struct {
	network *net.IPNet
	size    int
	subnets []*net.IPNet
}

// ParsePool validates an address pool and carves it into node subnets.
//
// The rules exist to fail fast on operator input rather than to hand out
// addresses that cannot work:
//
//   - IPv4 only. The whole routing stack is IPv4-only already, because
//     routing.Policy.Validate rejects anything without a four-byte form, and a
//     VPN pool that the egress policy could never evaluate would be worse than
//     a rejected configuration.
//   - The pool must be /24 or wider, and the node subnet strictly narrower than
//     the pool but no narrower than /30.
//   - The pool must not sit in a range routing.IsDangerousAddress denies, so a
//     link-local, unspecified or multicast pool is refused here instead of
//     producing peers that are denied at packet time.
//   - At most maxNodeSubnets subnets.
func ParsePool(cidr string, nodeSubnetSize int) (Pool, error) {
	_, network, size, err := parseIPv4Network(cidr)
	if err != nil {
		return Pool{}, err
	}
	if size > maxPoolBits {
		return Pool{}, poolInvalid(fmt.Sprintf("ip_pool %s must be /%d or wider", network, maxPoolBits))
	}
	if nodeSubnetSize <= size {
		return Pool{}, poolInvalid(fmt.Sprintf("node_subnet_size %d must be narrower than the ip_pool prefix /%d", nodeSubnetSize, size))
	}
	if nodeSubnetSize > maxNodeSubnetBits {
		return Pool{}, poolInvalid(fmt.Sprintf("node_subnet_size %d must be /%d or wider so a subnet keeps at least two usable addresses", nodeSubnetSize, maxNodeSubnetBits))
	}
	count := 1 << (nodeSubnetSize - size)
	if count > maxNodeSubnets {
		return Pool{}, poolInvalid(fmt.Sprintf("ip_pool %s with node_subnet_size /%d yields %d subnets, the maximum is %d", network, nodeSubnetSize, count, maxNodeSubnets))
	}
	// Refusing a pool whose addresses the egress policy would deny anyway keeps
	// the failure at configuration time, where the operator can still read it.
	if routing.IsDangerousAddress(network.IP) {
		return Pool{}, poolInvalid(fmt.Sprintf("ip_pool %s is in a non-routable or reserved range", network))
	}
	pool := Pool{network: network, size: nodeSubnetSize, subnets: make([]*net.IPNet, 0, count)}
	step := 1 << (ipv4Bits - nodeSubnetSize)
	for offset := 0; offset < count; offset++ {
		base := addIPv4(network.IP, offset*step)
		pool.subnets = append(pool.subnets, &net.IPNet{
			IP:   base,
			Mask: net.CIDRMask(nodeSubnetSize, ipv4Bits),
		})
	}
	return pool, nil
}

// Network returns a copy of the parsed pool CIDR.
func (p Pool) Network() *net.IPNet {
	return cloneNetwork(p.network)
}

// Size returns the node subnet prefix length.
func (p Pool) Size() int { return p.size }

// Subnets returns a copy of every node subnet in ascending order.
//
// A copy is returned so a caller cannot mutate the carved list and silently
// change what later allocations see. The order is stable because the service
// walks it deterministically when looking for a subnet it can lease.
func (p Pool) Subnets() []*net.IPNet {
	out := make([]*net.IPNet, len(p.subnets))
	for i, subnet := range p.subnets {
		out[i] = cloneNetwork(subnet)
	}
	return out
}

// NodeSubnet resolves and validates one leased subnet against this pool.
//
// The service calls it with the subnet string read back from vpn_ip_leases, so
// a pool that changed under a running cluster, or a row written by an older
// configuration, fails here instead of allocating an address outside the
// configured range. The input must already be canonical: host bits set
// ("10.64.5.7/24") are rejected rather than silently masked, because that
// string came from storage and masking it would hide corruption.
func (p Pool) NodeSubnet(cidr string) (*net.IPNet, error) {
	literal, network, size, err := parseIPv4Network(cidr)
	if err != nil {
		return nil, err
	}
	// A leased subnet string is written by this package, so it is always
	// canonical. Host bits mean the row was corrupted or hand-edited, and
	// masking them here would hide that.
	if !literal.Equal(network.IP) {
		return nil, poolInvalid(fmt.Sprintf("subnet %s is not canonical, the network address is %s", cidr, network))
	}
	if size != p.size {
		return nil, poolInvalid(fmt.Sprintf("subnet %s has prefix /%d, this node uses /%d", network, size, p.size))
	}
	if !p.network.Contains(network.IP) {
		return nil, poolInvalid(fmt.Sprintf("subnet %s is not inside ip_pool %s", network, p.network))
	}
	return network, nil
}

// NodeAddress returns the address the node's own VPN interface must use: the
// first usable host address of the subnet.
//
// It is reserved and never handed to a peer. Without the reservation the
// gateway and the first peer would both be configured with the same /32, and
// the tunnel would black-hole in a way that looks like a routing bug rather
// than an address collision.
func (p Pool) NodeAddress(subnet *net.IPNet) (net.IP, error) {
	normalized, err := p.validatedSubnet(subnet)
	if err != nil {
		return nil, err
	}
	return addIPv4(normalized.IP, 1), nil
}

// AllocateAddress returns the lowest free host address in a subnet.
//
// It skips the network address, the broadcast address and the address reserved
// by NodeAddress, and consults taken for everything else. A nil taken is
// treated as "nothing is occupied": the database unique index on
// (node_id, vpn_ip) is still the authoritative guard, so the worst case is a
// rejected insert rather than a duplicated address.
//
// Addresses are probed in ascending order, which keeps allocation stable and
// makes a leaked address reusable as soon as it is released.
func (p Pool) AllocateAddress(subnet *net.IPNet, taken func(net.IP) bool) (net.IP, error) {
	normalized, err := p.validatedSubnet(subnet)
	if err != nil {
		return nil, err
	}
	hosts := 1 << (ipv4Bits - p.size)
	// First host is normalized.IP+1 (reserved for the node), last host is
	// normalized.IP+hosts-2 (normalized.IP+hosts-1 is the broadcast address).
	for offset := 2; offset <= hosts-2; offset++ {
		candidate := addIPv4(normalized.IP, offset)
		if taken != nil && taken(candidate) {
			continue
		}
		return candidate, nil
	}
	return nil, ErrIPPoolExhausted.WithMessage(fmt.Sprintf("subnet %s has no free address left", normalized))
}

// Contains reports whether an address lies inside the pool.
func (p Pool) Contains(ip net.IP) bool {
	if p.network == nil || ip == nil {
		return false
	}
	four := ip.To4()
	if four == nil {
		return false
	}
	return p.network.Contains(four)
}

// validatedSubnet normalises a caller-supplied subnet through NodeSubnet so a
// hand-built or foreign *net.IPNet cannot make the arithmetic below operate on
// a prefix length this pool never carved.
func (p Pool) validatedSubnet(subnet *net.IPNet) (*net.IPNet, error) {
	if subnet == nil {
		return nil, poolInvalid("subnet is required")
	}
	return p.NodeSubnet(subnet.String())
}

// parseIPv4Network parses a CIDR and returns it normalised to four-byte form
// with its host bits masked off. It reports the pool-invalid code because every
// caller is validating operator configuration or stored configuration text.
// parseIPv4Network parses a CIDR and returns both the address exactly as
// written and the network it belongs to, normalised to four-byte form. Returning
// both lets ParsePool follow net.ParseCIDR's forgiving semantics for operator
// input while NodeSubnet holds stored configuration text to the canonical form.
func parseIPv4Network(cidr string) (net.IP, *net.IPNet, int, error) {
	trimmed := strings.TrimSpace(cidr)
	if trimmed == "" {
		return nil, nil, 0, poolInvalid("ip_pool must not be empty")
	}
	literal, network, err := net.ParseCIDR(trimmed)
	if err != nil {
		return nil, nil, 0, poolInvalid(fmt.Sprintf("%q is not a valid cidr: %v", trimmed, err))
	}
	ones, bits := network.Mask.Size()
	// bits != 32 covers IPv6 and the IPv4-mapped form, whose To4 succeeds but
	// whose mask is 128 bits wide.
	if bits != ipv4Bits || network.IP.To4() == nil {
		return nil, nil, 0, poolInvalid(fmt.Sprintf("%q must be an ipv4 cidr", trimmed))
	}
	mask := net.CIDRMask(ones, ipv4Bits)
	return literal.To4(), &net.IPNet{IP: network.IP.To4().Mask(mask), Mask: mask}, ones, nil
}

// addIPv4 returns a fresh four-byte address offset bytes after base. The result
// is always a new slice, so a caller that keeps it cannot observe a later
// allocation reusing the same backing array.
func addIPv4(base net.IP, offset int) net.IP {
	value := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	value += uint32(offset)
	return net.IPv4(byte(value>>24), byte(value>>16), byte(value>>8), byte(value)).To4()
}

func cloneNetwork(network *net.IPNet) *net.IPNet {
	if network == nil {
		return nil
	}
	ip := make(net.IP, len(network.IP))
	copy(ip, network.IP)
	mask := make(net.IPMask, len(network.Mask))
	copy(mask, network.Mask)
	return &net.IPNet{IP: ip, Mask: mask}
}

func poolInvalid(reason string) *Error {
	return ErrIPPoolInvalid.WithMessage("vpn ip pool is invalid: " + reason)
}
