package vpn_test

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

var specNow = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

func validSpec() vpn.PeerSpec {
	return vpn.PeerSpec{
		Name:               "laptop",
		Description:        "engineering laptop",
		PublicKey:          rfcAlicePublic,
		VPNIP:              net.ParseIP("10.64.5.7"),
		NodeID:             "node-1",
		AgentID:            "agent-1",
		AllowedIPs:         cidrs("10.0.0.0/8"),
		AllowedPorts:       []int{443},
		MaxConcurrentFlows: 128,
		PacketRateLimit:    0,
	}
}

func cidrs(raw ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(raw))
	for _, item := range raw {
		_, network, err := net.ParseCIDR(item)
		if err != nil {
			panic(err)
		}
		out = append(out, network)
	}
	return out
}

func TestPeerSpecValidateAcceptsABaselineSpec(t *testing.T) {
	if err := validSpec().Validate(specNow); err != nil {
		t.Fatalf("a fully specified peer was rejected: %v", err)
	}
}

// TestPeerSpecValidateRejectsEachBadField mutates one field at a time so a
// missing check fails with the name of the field it stopped enforcing.
func TestPeerSpecValidateRejectsEachBadField(t *testing.T) {
	future := specNow.Add(24 * time.Hour)
	cases := map[string]func(*vpn.PeerSpec){
		"empty name":             func(s *vpn.PeerSpec) { s.Name = "" },
		"blank name":             func(s *vpn.PeerSpec) { s.Name = "   " },
		"name with newline":      func(s *vpn.PeerSpec) { s.Name = "a\nb" },
		"name with control char": func(s *vpn.PeerSpec) { s.Name = "a\x00b" },
		"name too long":          func(s *vpn.PeerSpec) { s.Name = strings.Repeat("n", 256) },
		"description too long":   func(s *vpn.PeerSpec) { s.Description = strings.Repeat("d", 256) },
		"description newline":    func(s *vpn.PeerSpec) { s.Description = "a\nb" },
		"empty public key":       func(s *vpn.PeerSpec) { s.PublicKey = "" },
		"invalid public key":     func(s *vpn.PeerSpec) { s.PublicKey = "not-a-key" },
		"all zero public key":    func(s *vpn.PeerSpec) { s.PublicKey = vpn.EncodeKey(make([]byte, 32)) },
		"nil vpn ip":             func(s *vpn.PeerSpec) { s.VPNIP = nil },
		"empty node id":          func(s *vpn.PeerSpec) { s.NodeID = "" },
		"empty agent id":         func(s *vpn.PeerSpec) { s.AgentID = "" },
		"negative flows":         func(s *vpn.PeerSpec) { s.MaxConcurrentFlows = -1 },
		"negative rate":          func(s *vpn.PeerSpec) { s.PacketRateLimit = -1 },
		"expires in the past":    func(s *vpn.PeerSpec) { past := specNow.Add(-time.Second); s.ExpiresAt = &past },
		"expires exactly now":    func(s *vpn.PeerSpec) { now := specNow; s.ExpiresAt = &now },
		"nil allowed ip entry":   func(s *vpn.PeerSpec) { s.AllowedIPs = []*net.IPNet{nil} },
		"port zero":              func(s *vpn.PeerSpec) { s.AllowedPorts = []int{0} },
		"port too large":         func(s *vpn.PeerSpec) { s.AllowedPorts = []int{65536} },
		"negative port":          func(s *vpn.PeerSpec) { s.AllowedPorts = []int{-1} },
	}
	for name, mutate := range cases {
		spec := validSpec()
		mutate(&spec)
		err := spec.Validate(specNow)
		if err == nil {
			t.Errorf("%s: Validate accepted an invalid spec", name)
			continue
		}
		apiErr, ok := err.(*vpn.Error)
		if !ok {
			t.Errorf("%s: error %v is not a *vpn.Error", name, err)
			continue
		}
		if apiErr.Code != "vpn_peer_invalid" || apiErr.Status != 400 {
			t.Errorf("%s: got %d/%s, want 400/vpn_peer_invalid", name, apiErr.Status, apiErr.Code)
		}
	}
	// A future expiry is the only accepted non-zero ExpiresAt.
	spec := validSpec()
	spec.ExpiresAt = &future
	if err := spec.Validate(specNow); err != nil {
		t.Errorf("a future expiry must be accepted: %v", err)
	}
}

// TestEncodeAllowedIPsIsCanonical pins the exact text form. Two spellings of one
// set must produce one string, otherwise reordering a CIDR list shows up in the
// audit log as a policy change and breaks Idempotency-Key replay.
func TestEncodeAllowedIPsIsCanonical(t *testing.T) {
	got := vpn.EncodeAllowedIPs(cidrs("10.1.0.0/16", "10.0.0.0/16", "10.1.0.0/16"))
	if want := "10.0.0.0/16,10.1.0.0/16"; got != want {
		t.Fatalf("EncodeAllowedIPs = %q, want %q", got, want)
	}
	if got := vpn.EncodeAllowedIPs(nil); got != "" {
		t.Fatalf("the empty set must encode to the empty string, got %q", got)
	}
	// Host bits are masked, so an unaligned spelling collapses onto the network.
	if got := vpn.EncodeAllowedIPs(cidrs("10.1.5.7/16")); got != "10.1.0.0/16" {
		t.Fatalf("EncodeAllowedIPs = %q, want 10.1.0.0/16", got)
	}
}

// TestEncodeAllowedIPsSortsLexicographically pins the ordering rule itself.
// Lexicographic and numeric order disagree here (10.10 sorts before 10.2), so
// this test is what stops a well-meaning "improvement" to numeric sorting from
// silently invalidating every allowed_ips string already in the database.
func TestEncodeAllowedIPsSortsLexicographically(t *testing.T) {
	got := vpn.EncodeAllowedIPs(cidrs("10.2.0.0/16", "10.10.0.0/16"))
	if want := "10.10.0.0/16,10.2.0.0/16"; got != want {
		t.Fatalf("EncodeAllowedIPs = %q, want %q", got, want)
	}
	// Different prefix lengths sharing a prefix must also be stable.
	got = vpn.EncodeAllowedIPs(cidrs("10.0.0.0/8", "10.0.0.0/16", "10.0.0.0/24"))
	if want := "10.0.0.0/16,10.0.0.0/24,10.0.0.0/8"; got != want {
		t.Fatalf("EncodeAllowedIPs = %q, want %q", got, want)
	}
}

func TestEncodeAllowedPortsIsCanonical(t *testing.T) {
	if got := vpn.EncodeAllowedPorts([]int{443, 22, 443, 80}); got != "22,80,443" {
		t.Fatalf("EncodeAllowedPorts = %q, want \"22,80,443\"", got)
	}
	if got := vpn.EncodeAllowedPorts(nil); got != "" {
		t.Fatalf("the empty set must encode to the empty string, got %q", got)
	}
	if got := vpn.EncodeAllowedPorts([]int{1, 65535}); got != "1,65535" {
		t.Fatalf("EncodeAllowedPorts = %q, want \"1,65535\"", got)
	}
}

func TestAllowedPolicyRoundTrip(t *testing.T) {
	original := cidrs("192.168.0.0/16", "10.0.0.0/8", "172.16.5.0/24")
	encoded := vpn.EncodeAllowedIPs(original)
	parsed, err := vpn.ParseAllowedIPs(encoded)
	if err != nil {
		t.Fatalf("ParseAllowedIPs: %v", err)
	}
	if vpn.EncodeAllowedIPs(parsed) != encoded {
		t.Fatalf("round trip changed the encoding: %q then %q", encoded, vpn.EncodeAllowedIPs(parsed))
	}
	if len(parsed) != 3 {
		t.Fatalf("parsed %d cidrs, want 3", len(parsed))
	}

	ports := []int{22, 443, 8080}
	portText := vpn.EncodeAllowedPorts(ports)
	parsedPorts, err := vpn.ParseAllowedPorts(portText)
	if err != nil {
		t.Fatalf("ParseAllowedPorts: %v", err)
	}
	if vpn.EncodeAllowedPorts(parsedPorts) != portText {
		t.Fatalf("round trip changed the port encoding: %q then %q", portText, vpn.EncodeAllowedPorts(parsedPorts))
	}

	// The empty encoding must survive a round trip as the empty set, since the
	// two empty cases mean opposite things (see TestEmptySetSemantics).
	if parsed, err := vpn.ParseAllowedIPs(""); err != nil || len(parsed) != 0 {
		t.Fatalf("ParseAllowedIPs(\"\") = %v, %v; want empty and no error", parsed, err)
	}
	if parsed, err := vpn.ParseAllowedPorts(""); err != nil || len(parsed) != 0 {
		t.Fatalf("ParseAllowedPorts(\"\") = %v, %v; want empty and no error", parsed, err)
	}
}

// TestEmptySetSemantics documents the one asymmetry in the encoding that is easy
// to get backwards: an empty allowed_ips denies every destination, while an
// empty allowed_ports permits every port. Both are stored as the empty string.
func TestEmptySetSemantics(t *testing.T) {
	spec := validSpec()
	spec.AllowedIPs = nil
	spec.AllowedPorts = nil
	if err := spec.Validate(specNow); err != nil {
		t.Fatalf("an empty policy is a valid, if useless, specification: %v", err)
	}
	if got := spec.EncodedAllowedIPs(); got != "" {
		t.Fatalf("EncodedAllowedIPs = %q, want empty", got)
	}
	if got := spec.EncodedAllowedPorts(); got != "" {
		t.Fatalf("EncodedAllowedPorts = %q, want empty", got)
	}
}

func TestParseAllowedIPsRejectsMalformedInput(t *testing.T) {
	for name, input := range map[string]string{
		"garbage":          "not-a-cidr",
		"bare address":     "10.0.0.1",
		"prefix too large": "10.0.0.0/33",
		"negative prefix":  "10.0.0.0/-1",
		"ipv6":             "fd00::/64",
		"ipv4 mapped":      "::ffff:10.0.0.0/112",
		"empty token":      "10.0.0.0/8,,10.1.0.0/16",
		"trailing comma":   "10.0.0.0/8,",
		"leading comma":    ",10.0.0.0/8",
		"semicolon":        "10.0.0.0/8;10.1.0.0/16",
	} {
		if _, err := vpn.ParseAllowedIPs(input); err == nil {
			t.Errorf("%s: ParseAllowedIPs(%q) was accepted", name, input)
		} else if apiErr, ok := err.(*vpn.Error); !ok || apiErr.Code != "vpn_peer_invalid" {
			t.Errorf("%s: got %v, want a vpn_peer_invalid error", name, err)
		}
	}
}

func TestParseAllowedPortsRejectsMalformedInput(t *testing.T) {
	for name, input := range map[string]string{
		"garbage":     "http",
		"zero":        "0",
		"negative":    "-1",
		"too large":   "65536",
		"empty token": "80,,443",
		// Ranges are deliberately not accepted. The canonical form is a flat
		// ascending list, so accepting "80-90" would mean stored text that this
		// package cannot reproduce byte for byte.
		"range":          "80-90",
		"trailing comma": "80,",
		"float":          "80.0",
		"hex":            "0x50",
	} {
		if _, err := vpn.ParseAllowedPorts(input); err == nil {
			t.Errorf("%s: ParseAllowedPorts(%q) was accepted", name, input)
		} else if apiErr, ok := err.(*vpn.Error); !ok || apiErr.Code != "vpn_peer_invalid" {
			t.Errorf("%s: got %v, want a vpn_peer_invalid error", name, err)
		}
	}
}

func TestParseAllowedIPsNormalisesHostBits(t *testing.T) {
	parsed, err := vpn.ParseAllowedIPs("10.1.5.7/16")
	if err != nil {
		t.Fatalf("ParseAllowedIPs: %v", err)
	}
	if len(parsed) != 1 || parsed[0].String() != "10.1.0.0/16" {
		t.Fatalf("parsed = %v, want 10.1.0.0/16", parsed)
	}
}

func TestParseAllowedPortsAcceptsASinglePort(t *testing.T) {
	parsed, err := vpn.ParseAllowedPorts("443")
	if err != nil {
		t.Fatalf("ParseAllowedPorts: %v", err)
	}
	if len(parsed) != 1 || parsed[0] != 443 {
		t.Fatalf("parsed = %v, want [443]", parsed)
	}
}

func TestParseAllowedIPList(t *testing.T) {
	parsed, err := vpn.ParseAllowedIPList([]string{"10.1.0.0/16", " 10.0.0.0/16 "})
	if err != nil {
		t.Fatalf("ParseAllowedIPList: %v", err)
	}
	if got := vpn.EncodeAllowedIPs(parsed); got != "10.0.0.0/16,10.1.0.0/16" {
		t.Fatalf("encoded = %q", got)
	}
	if parsed, err := vpn.ParseAllowedIPList(nil); err != nil || len(parsed) != 0 {
		t.Fatalf("a nil list must parse to the empty set, got %v %v", parsed, err)
	}
	for name, values := range map[string][]string{
		"empty element":  {"10.0.0.0/8", "  "},
		"embedded comma": {"10.0.0.0/8,10.1.0.0/16"},
		"not a cidr":     {"10.0.0.1"},
		"ipv6":           {"fd00::/64"},
		"prefix too big": {"10.0.0.0/33"},
	} {
		if _, err := vpn.ParseAllowedIPList(values); err == nil {
			t.Errorf("%s: ParseAllowedIPList(%v) was accepted", name, values)
		} else if apiErr, ok := err.(*vpn.Error); !ok || apiErr.Code != "vpn_peer_invalid" {
			t.Errorf("%s: got %v, want a vpn_peer_invalid error", name, err)
		}
	}
}
