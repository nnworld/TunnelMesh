package vpn_test

import (
	"net"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// TestErrorClassConstantsMatchTheSpec pins every data-plane error_class from the
// design spec §12.2. These strings become Prometheus label values and audit
// reasons, so they are a published contract: renaming one silently splits a
// dashboard series in two.
func TestErrorClassConstantsMatchTheSpec(t *testing.T) {
	want := map[vpn.ErrorClass]string{
		vpn.ClassPeerUnknown:         "peer_unknown",
		vpn.ClassPeerRevoked:         "peer_revoked",
		vpn.ClassPeerExpired:         "peer_expired",
		vpn.ClassTargetDenied:        "target_denied",
		vpn.ClassMetadataDenied:      "metadata_denied",
		vpn.ClassPortDenied:          "port_denied",
		vpn.ClassProtocolUnsupported: "protocol_unsupported",
		vpn.ClassFragmentDropped:     "fragment_dropped",
		vpn.ClassOversizeDropped:     "oversize_dropped",
		vpn.ClassCapacityExhausted:   "capacity_exhausted",
		vpn.ClassRateLimited:         "rate_limited",
		vpn.ClassEgressUnavailable:   "egress_unavailable",
		vpn.ClassEgressTimeout:       "egress_timeout",
		vpn.ClassICMPUnsupported:     "icmp_unsupported",
		vpn.ClassICMPTimeout:         "icmp_timeout",
		vpn.ClassStackError:          "stack_error",
	}
	seen := make(map[string]vpn.ErrorClass, len(want))
	for class, text := range want {
		if string(class) != text {
			t.Errorf("error class = %q, want %q", string(class), text)
		}
		if class == "" {
			t.Error("an error class must not be empty")
		}
		if previous, duplicate := seen[text]; duplicate {
			t.Errorf("%q is claimed by two constants (%q and %q)", text, previous, class)
		}
		seen[text] = class
	}
	if len(seen) != 16 {
		t.Fatalf("expected the 16 spec classes, got %d", len(seen))
	}
}

func policyFor(t *testing.T, mutate func(*vpn.PeerSpec), mtu int) vpn.PacketPolicy {
	t.Helper()
	spec := validSpec()
	spec.AllowedIPs = cidrs("10.0.0.0/8", "192.0.2.0/24")
	spec.AllowedPorts = []int{443, 8080}
	// The baseline peer is granted private space in its allowlist, so the switch
	// that lets it reach that space is on. Tests that exercise the switch itself
	// turn it back off.
	spec.AllowPrivateTargets = true
	if mutate != nil {
		mutate(&spec)
	}
	policy, err := vpn.NewPacketPolicy(spec, mtu)
	if err != nil {
		t.Fatalf("NewPacketPolicy: %v", err)
	}
	return policy
}

func packet(protocol uint8, dest string, port, size int) vpn.Packet {
	return vpn.Packet{
		Protocol: protocol,
		Source:   net.ParseIP("10.64.5.7"),
		Dest:     net.ParseIP(dest),
		Port:     port,
		Size:     size,
	}
}

func assertClass(t *testing.T, err error, want vpn.ErrorClass) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got nil", want)
	}
	packetErr, ok := err.(*vpn.PacketError)
	if !ok {
		t.Fatalf("error %v is not a *vpn.PacketError", err)
	}
	if packetErr.ErrorClass() != want {
		t.Fatalf("error class = %q, want %q", packetErr.ErrorClass(), want)
	}
	if packetErr.Error() == "" {
		t.Fatal("a packet error must render a non-empty message")
	}
}

func TestPacketPolicyAllowsAMatchingFlow(t *testing.T) {
	policy := policyFor(t, nil, 1420)
	if err := policy.Allow(packet(vpn.ProtocolTCP, "10.1.2.3", 443, 120)); err != nil {
		t.Fatalf("a permitted flow was denied: %v", err)
	}
	if err := policy.Allow(packet(vpn.ProtocolUDP, "192.0.2.9", 8080, 1420)); err != nil {
		t.Fatalf("a permitted UDP flow at exactly the MTU was denied: %v", err)
	}
	// An empty port allowlist means no port restriction, the opposite of an
	// empty address allowlist.
	open := policyFor(t, func(s *vpn.PeerSpec) { s.AllowedPorts = nil }, 1420)
	if err := open.Allow(packet(vpn.ProtocolTCP, "10.1.2.3", 1, 120)); err != nil {
		t.Fatalf("an empty port allowlist must permit every port: %v", err)
	}
}

func TestPacketPolicyDropsFragments(t *testing.T) {
	policy := policyFor(t, nil, 1420)
	fragmented := packet(vpn.ProtocolTCP, "10.1.2.3", 443, 120)
	fragmented.Fragment = true
	assertClass(t, policy.Allow(fragmented), vpn.ClassFragmentDropped)

	// Order matters: a fragment aimed at the cloud metadata address must still
	// be reported as a fragment, because the fragment check runs first and a
	// non-first fragment carries no port to evaluate anyway.
	metadata := packet(vpn.ProtocolTCP, "169.254.169.254", 443, 120)
	metadata.Fragment = true
	assertClass(t, policy.Allow(metadata), vpn.ClassFragmentDropped)
}

func TestPacketPolicyDropsOversizePackets(t *testing.T) {
	policy := policyFor(t, nil, 1420)
	assertClass(t, policy.Allow(packet(vpn.ProtocolTCP, "10.1.2.3", 443, 1421)), vpn.ClassOversizeDropped)
	if err := policy.Allow(packet(vpn.ProtocolTCP, "10.1.2.3", 443, 1420)); err != nil {
		t.Fatalf("a packet of exactly the MTU must pass: %v", err)
	}
	// The size check precedes the protocol whitelist, so an oversized packet of
	// an unsupported protocol is reported as oversized.
	assertClass(t, policy.Allow(packet(47, "10.1.2.3", 0, 9000)), vpn.ClassOversizeDropped)
}

func TestPacketPolicyWhitelistsProtocols(t *testing.T) {
	policy := policyFor(t, nil, 1420)
	for _, protocol := range []uint8{0, 2, 47, 50, 58, 132, 255} {
		assertClass(t, policy.Allow(packet(protocol, "10.1.2.3", 443, 120)), vpn.ClassProtocolUnsupported)
	}
	// ICMPv6 is a different protocol number and is not on the whitelist either.
	assertClass(t, policy.Allow(packet(58, "10.1.2.3", 0, 120)), vpn.ClassProtocolUnsupported)
}

func TestPacketPolicyGatesICMPOnThePeerSwitch(t *testing.T) {
	// ICMP is off by default in this spec, so echo must be refused with a class
	// of its own rather than the generic protocol rejection: the operator needs
	// to know the fix is a peer setting, not a missing protocol.
	off := policyFor(t, func(s *vpn.PeerSpec) { s.ICMPEnabled = false }, 1420)
	assertClass(t, off.Allow(packet(vpn.ProtocolICMP, "10.1.2.3", 0, 84)), vpn.ClassICMPUnsupported)

	on := policyFor(t, func(s *vpn.PeerSpec) { s.ICMPEnabled = true }, 1420)
	if err := on.Allow(packet(vpn.ProtocolICMP, "10.1.2.3", 0, 84)); err != nil {
		t.Fatalf("an ICMP-enabled peer must be able to echo: %v", err)
	}
	// ICMP carries no port, so the port allowlist must not be applied to it.
	on2 := policyFor(t, func(s *vpn.PeerSpec) { s.ICMPEnabled = true; s.AllowedPorts = []int{443} }, 1420)
	if err := on2.Allow(packet(vpn.ProtocolICMP, "10.1.2.3", 0, 84)); err != nil {
		t.Fatalf("ICMP must ignore the port allowlist: %v", err)
	}
	// The address allowlist still applies.
	assertClass(t, on.Allow(packet(vpn.ProtocolICMP, "8.8.8.8", 0, 84)), vpn.ClassTargetDenied)
}

func TestPacketPolicyDeniesDangerousAddressesUnconditionally(t *testing.T) {
	// A peer that explicitly allows link-local space still must not reach the
	// cloud metadata address: the deny list outranks the allowlist, exactly as
	// it does for the tp-* proxy entry and for agent policies.
	permissive := policyFor(t, func(s *vpn.PeerSpec) {
		s.AllowedIPs = cidrs("0.0.0.0/0")
		s.AllowPrivateTargets = true
		s.AllowedPorts = nil
	}, 1420)
	for _, dest := range []string{"169.254.169.254", "169.254.1.1", "224.0.0.1", "0.0.0.0", "255.255.255.255"} {
		assertClass(t, permissive.Allow(packet(vpn.ProtocolTCP, dest, 443, 120)), vpn.ClassMetadataDenied)
	}
}

func TestPacketPolicyGatesPrivateTargetsOnTheSwitch(t *testing.T) {
	strict := policyFor(t, func(s *vpn.PeerSpec) { s.AllowPrivateTargets = false }, 1420)
	assertClass(t, strict.Allow(packet(vpn.ProtocolTCP, "10.1.2.3", 443, 120)), vpn.ClassTargetDenied)

	loose := policyFor(t, func(s *vpn.PeerSpec) { s.AllowPrivateTargets = true }, 1420)
	if err := loose.Allow(packet(vpn.ProtocolTCP, "10.1.2.3", 443, 120)); err != nil {
		t.Fatalf("a peer allowed to reach private targets was denied: %v", err)
	}
	// Loopback is private for this purpose even though RFC1918 does not list it.
	assertClass(t, strict.Allow(packet(vpn.ProtocolTCP, "127.0.0.1", 443, 120)), vpn.ClassTargetDenied)
}

func TestPacketPolicyEnforcesTheAddressAllowlist(t *testing.T) {
	policy := policyFor(t, nil, 1420)
	assertClass(t, policy.Allow(packet(vpn.ProtocolTCP, "8.8.8.8", 443, 120)), vpn.ClassTargetDenied)
	assertClass(t, policy.Allow(packet(vpn.ProtocolTCP, "192.0.3.1", 443, 120)), vpn.ClassTargetDenied)
	if err := policy.Allow(packet(vpn.ProtocolTCP, "192.0.2.254", 443, 120)); err != nil {
		t.Fatalf("an address inside an allowed CIDR was denied: %v", err)
	}
}

// TestEmptyAllowedIPsDeniesEverything pins the asymmetry that is easiest to get
// backwards. An empty allowed_ips means no destination is reachable; an empty
// allowed_ports means every port is reachable. Getting this wrong in the
// permissive direction would turn a misconfigured peer into an open relay.
func TestEmptyAllowedIPsDeniesEverything(t *testing.T) {
	policy := policyFor(t, func(s *vpn.PeerSpec) {
		s.AllowedIPs = nil
		s.AllowedPorts = nil
		s.AllowPrivateTargets = true
	}, 1420)
	for _, dest := range []string{"10.1.2.3", "8.8.8.8", "192.0.2.1"} {
		assertClass(t, policy.Allow(packet(vpn.ProtocolTCP, dest, 443, 120)), vpn.ClassTargetDenied)
	}
}

func TestPacketPolicyEnforcesThePortAllowlist(t *testing.T) {
	policy := policyFor(t, nil, 1420)
	assertClass(t, policy.Allow(packet(vpn.ProtocolTCP, "10.1.2.3", 22, 120)), vpn.ClassPortDenied)
	assertClass(t, policy.Allow(packet(vpn.ProtocolUDP, "10.1.2.3", 53, 120)), vpn.ClassPortDenied)
	// A malformed port is a denial rather than a pass-through: the port check is
	// the last gate, so failing open here would be the worst possible default.
	assertClass(t, policy.Allow(packet(vpn.ProtocolTCP, "10.1.2.3", 0, 120)), vpn.ClassPortDenied)
	assertClass(t, policy.Allow(packet(vpn.ProtocolTCP, "10.1.2.3", 70000, 120)), vpn.ClassPortDenied)
	if err := policy.Allow(packet(vpn.ProtocolTCP, "10.1.2.3", 8080, 120)); err != nil {
		t.Fatalf("an allowed port was denied: %v", err)
	}
}

func TestPacketPolicyRejectsNonIPv4Destinations(t *testing.T) {
	policy := policyFor(t, func(s *vpn.PeerSpec) {
		s.AllowedIPs = cidrs("0.0.0.0/0")
		s.AllowPrivateTargets = true
	}, 1420)
	v6 := packet(vpn.ProtocolTCP, "2001:db8::1", 443, 120)
	assertClass(t, policy.Allow(v6), vpn.ClassTargetDenied)

	missing := packet(vpn.ProtocolTCP, "", 443, 120)
	missing.Dest = nil
	assertClass(t, policy.Allow(missing), vpn.ClassTargetDenied)
}

// TestPacketPolicyEvaluationOrder walks the whole chain with one packet that
// violates every rule at once, then removes the violations from the front. Each
// step must report the earliest remaining violation, which is what makes the
// emitted error_class usable for diagnosis rather than arbitrary.
func TestPacketPolicyEvaluationOrder(t *testing.T) {
	policy := policyFor(t, func(s *vpn.PeerSpec) {
		s.AllowPrivateTargets = false
		s.ICMPEnabled = false
		s.AllowedIPs = cidrs("192.0.2.0/24")
		s.AllowedPorts = []int{443}
	}, 1420)

	fragmented := vpn.Packet{Protocol: 47, Source: net.ParseIP("10.64.5.7"), Dest: net.ParseIP("10.1.2.3"), Port: 22, Size: 9000, Fragment: true}
	assertClass(t, policy.Allow(fragmented), vpn.ClassFragmentDropped)

	oversize := fragmented
	oversize.Fragment = false
	assertClass(t, policy.Allow(oversize), vpn.ClassOversizeDropped)

	badProtocol := oversize
	badProtocol.Size = 120
	assertClass(t, policy.Allow(badProtocol), vpn.ClassProtocolUnsupported)

	icmp := badProtocol
	icmp.Protocol = vpn.ProtocolICMP
	icmp.Port = 0
	assertClass(t, policy.Allow(icmp), vpn.ClassICMPUnsupported)

	private := badProtocol
	private.Protocol = vpn.ProtocolTCP
	private.Port = 22
	assertClass(t, policy.Allow(private), vpn.ClassTargetDenied)

	metadata := private
	metadata.Dest = net.ParseIP("169.254.169.254")
	assertClass(t, policy.Allow(metadata), vpn.ClassMetadataDenied)

	wrongPort := badProtocol
	wrongPort.Protocol = vpn.ProtocolTCP
	wrongPort.Dest = net.ParseIP("192.0.2.9")
	wrongPort.Port = 22
	assertClass(t, policy.Allow(wrongPort), vpn.ClassPortDenied)

	allowed := wrongPort
	allowed.Port = 443
	if err := policy.Allow(allowed); err != nil {
		t.Fatalf("the fully conforming packet was denied: %v", err)
	}
}

func TestNewPacketPolicyRejectsBadInput(t *testing.T) {
	spec := validSpec()
	spec.PublicKey = "not-a-key"
	if _, err := vpn.NewPacketPolicy(spec, 1420); err == nil {
		t.Error("an invalid spec must be rejected")
	}
	if _, err := vpn.NewPacketPolicy(validSpecWithPublicCIDRs(), 0); err == nil {
		t.Error("a non-positive MTU must be rejected")
	}
	if _, err := vpn.NewPacketPolicy(validSpecWithPublicCIDRs(), -1); err == nil {
		t.Error("a negative MTU must be rejected")
	}
	if _, err := vpn.NewPacketPolicy(validSpecWithPublicCIDRs(), 1420); err != nil {
		t.Errorf("a valid spec was rejected: %v", err)
	}
}

func validSpecWithPublicCIDRs() vpn.PeerSpec {
	spec := validSpec()
	spec.AllowedIPs = cidrs("192.0.2.0/24")
	spec.AllowedPorts = []int{443}
	return spec
}
