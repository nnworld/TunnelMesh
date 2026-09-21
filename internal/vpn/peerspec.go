package vpn

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxPeerNameLength and MaxPeerDescriptionLength match the VARCHAR(255)
	// columns in migrations/ddl.sql. Checking them here turns a truncated row
	// or a driver error into a 400 the operator can act on.
	MaxPeerNameLength        = 255
	MaxPeerDescriptionLength = 255

	// policySeparator is the single delimiter of the canonical allowed_ips and
	// allowed_ports text. It is a constant so the encoder and the parser can
	// never disagree about it.
	policySeparator = ","

	minPort = 1
	maxPort = 65535
)

// PeerSpec is one VPN peer, fully specified and parsed into typed values.
//
// It is not a mirror of the 22-column storage row: CreatedAt, the sealed
// private-key columns and the status lifecycle are persistence concerns and live
// only in storage.VPNPeer. PeerSpec holds what the gateway needs in order to
// build a WireGuard configuration and to decide whether a packet may leave, and
// its policy fields are parsed types ([]*net.IPNet, []int) rather than the
// opaque text the database stores. The one place that maps between the two is
// internal/server/vpn_peer_service.go.
//
// Some fields are supplied by the caller and some are assigned by the server:
//
//	Caller supplied  Name, Description, AllowedIPs, AllowedPorts,
//	                 AllowPrivateTargets, ICMPEnabled, MaxConcurrentFlows,
//	                 PacketRateLimit, ExpiresAt, AgentID
//	Server assigned  PublicKey (generated), VPNIP (allocated from the pool),
//	                 NodeID (derived from the serving node)
//
// Both kinds live in one struct because a PATCH reuses the same path: the
// service loads the stored peer into a PeerSpec, applies the change and
// re-validates, so a partial update can never bypass a check that creation
// enforced.
type PeerSpec struct {
	Name                string
	Description         string
	PublicKey           string
	VPNIP               net.IP
	NodeID              string
	AgentID             string
	AllowedIPs          []*net.IPNet
	AllowedPorts        []int
	AllowPrivateTargets bool
	ICMPEnabled         bool
	MaxConcurrentFlows  int
	PacketRateLimit     int
	ExpiresAt           *time.Time
}

// Validate reports the first reason this spec cannot be persisted, as the stable
// vpn_peer_invalid code. It is the whole-record check: the caller-supplied
// fields plus the three the server assigns (public key, address, node).
//
// now is a parameter rather than a call to time.Now so an expiry rule stays
// deterministic under test and so a caller that validates a batch uses one
// instant for all of it.
func (s PeerSpec) Validate(now time.Time) error {
	if err := s.ValidateRequest(now); err != nil {
		return err
	}
	if err := ValidatePublicKey(s.PublicKey); err != nil {
		return err
	}
	if s.VPNIP == nil || s.VPNIP.To4() == nil {
		return peerInvalid("vpn address is required and must be ipv4")
	}
	if strings.TrimSpace(s.NodeID) == "" {
		return peerInvalid("node is required")
	}
	return nil
}

// ValidateRequest checks what a caller may supply, before the server has
// generated a key pair or allocated an address. Issuing validates through this
// entry point so a request is rejected on its own merits rather than on fields
// the caller never sent.
func (s PeerSpec) ValidateRequest(now time.Time) error {
	if err := s.ValidateFields(); err != nil {
		return err
	}
	// The expiry rule lives here rather than in ValidateFields: it is the one
	// caller field whose validity depends on the clock, and a patch that only
	// renames a peer must stay usable after the peer has expired.
	if s.ExpiresAt != nil && !s.ExpiresAt.After(now) {
		return peerInvalid("expires_at must be in the future")
	}
	return nil
}

// ValidateFields checks the caller-supplied fields with no clock involved. A
// partial update re-validates the merged record through it, so an expired peer
// can still be renamed or have its policy narrowed while every other rule that
// creation enforced continues to apply.
func (s PeerSpec) ValidateFields() error {
	if strings.TrimSpace(s.Name) == "" {
		return peerInvalid("name is required")
	}
	if err := validateLabel("name", s.Name, MaxPeerNameLength); err != nil {
		return err
	}
	if err := validateLabel("description", s.Description, MaxPeerDescriptionLength); err != nil {
		return err
	}
	if strings.TrimSpace(s.AgentID) == "" {
		return peerInvalid("agent is required")
	}
	for _, network := range s.AllowedIPs {
		if network == nil {
			return peerInvalid("allowed_ips contains an empty entry")
		}
		ones, bits := network.Mask.Size()
		// The egress policy is IPv4-only because routing.Policy.Validate
		// rejects anything without a four-byte form, so an IPv6 entry could
		// never match and would silently widen nothing while looking like a rule.
		if bits != ipv4Bits || network.IP.To4() == nil {
			return peerInvalid(fmt.Sprintf("allowed_ips entry %s must be an ipv4 cidr", network))
		}
		if ones < 0 || ones > ipv4Bits {
			return peerInvalid(fmt.Sprintf("allowed_ips entry %s has an invalid prefix length", network))
		}
	}
	for _, port := range s.AllowedPorts {
		if port < minPort || port > maxPort {
			return peerInvalid(fmt.Sprintf("allowed_ports entry %d must be between %d and %d", port, minPort, maxPort))
		}
	}
	if s.MaxConcurrentFlows < 0 {
		return peerInvalid("max_concurrent_flows must not be negative")
	}
	if s.PacketRateLimit < 0 {
		return peerInvalid("packet_rate_limit must not be negative")
	}
	return nil
}

// EncodedAllowedIPs returns the canonical text form of this spec's allowed_ips.
func (s PeerSpec) EncodedAllowedIPs() string { return EncodeAllowedIPs(s.AllowedIPs) }

// EncodedAllowedPorts returns the canonical text form of this spec's
// allowed_ports.
func (s PeerSpec) EncodedAllowedPorts() string { return EncodeAllowedPorts(s.AllowedPorts) }

// EncodeAllowedIPs renders a CIDR set as the canonical allowed_ips text:
// network-masked, sorted, deduplicated and comma separated with no spaces.
//
// This package is the only place that defines that text form. storage treats the
// column as opaque, so if two spellings of one set produced two strings, merely
// reordering a list would show up in the audit log as a policy change and would
// break Idempotency-Key replay.
//
// Sorting is lexicographic over net.IPNet.String, which is not numeric order:
// "10.10.0.0/16" sorts before "10.2.0.0/16". That is deliberate and pinned by a
// test, because the value of the rule is reproducibility, not readability, and
// changing it later would invalidate every string already stored.
//
// A nil entry or one without a four-byte form is skipped rather than rendered.
// Validate rejects both, so the skip is unreachable for a validated spec; if it
// ever did happen, dropping the entry makes the policy stricter, never looser,
// which is the safe direction for a security allowlist.
func EncodeAllowedIPs(networks []*net.IPNet) string {
	seen := make(map[string]struct{}, len(networks))
	texts := make([]string, 0, len(networks))
	for _, network := range networks {
		if network == nil {
			continue
		}
		four := network.IP.To4()
		if four == nil {
			continue
		}
		ones, bits := network.Mask.Size()
		if bits != ipv4Bits {
			continue
		}
		mask := net.CIDRMask(ones, ipv4Bits)
		canonical := (&net.IPNet{IP: four.Mask(mask), Mask: mask}).String()
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		texts = append(texts, canonical)
	}
	sort.Strings(texts)
	return strings.Join(texts, policySeparator)
}

// EncodeAllowedPorts renders a port set as the canonical allowed_ports text:
// ascending, deduplicated and comma separated.
//
// An empty set encodes to the empty string and means "no port restriction". That
// is the opposite of allowed_ips, where the empty string means "no destination is
// reachable". The asymmetry is inherited from WireGuard, where an absent
// AllowedIPs blocks everything, and it is pinned by TestEmptySetSemantics.
func EncodeAllowedPorts(ports []int) string {
	seen := make(map[int]struct{}, len(ports))
	ordered := make([]int, 0, len(ports))
	for _, port := range ports {
		if port < minPort || port > maxPort {
			continue
		}
		if _, duplicate := seen[port]; duplicate {
			continue
		}
		seen[port] = struct{}{}
		ordered = append(ordered, port)
	}
	sort.Ints(ordered)
	texts := make([]string, len(ordered))
	for i, port := range ordered {
		texts[i] = strconv.Itoa(port)
	}
	return strings.Join(texts, policySeparator)
}

// ParseAllowedIPs reads canonical allowed_ips text back into CIDRs. The empty
// string parses to the empty set, which means nothing is reachable.
//
// Parsing is strict about structure but forgiving about host bits: an empty
// entry is corruption and must fail loudly, while "10.1.5.7/16" is normalised to
// its network so a re-encode reproduces the canonical text exactly.
func ParseAllowedIPs(encoded string) ([]*net.IPNet, error) {
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" {
		return nil, nil
	}
	tokens := strings.Split(trimmed, policySeparator)
	out := make([]*net.IPNet, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, peerInvalid("allowed_ips contains an empty entry")
		}
		_, network, err := net.ParseCIDR(token)
		if err != nil {
			return nil, peerInvalid(fmt.Sprintf("allowed_ips entry %q is not a valid cidr: %v", token, err))
		}
		ones, bits := network.Mask.Size()
		if bits != ipv4Bits || network.IP.To4() == nil {
			return nil, peerInvalid(fmt.Sprintf("allowed_ips entry %q must be an ipv4 cidr", token))
		}
		out = append(out, &net.IPNet{IP: network.IP.To4(), Mask: net.CIDRMask(ones, ipv4Bits)})
	}
	return out, nil
}

// ParseAllowedIPList parses the list form a JSON request body carries, as
// opposed to the comma-joined text ParseAllowedIPs reads back from storage.
//
// It exists so the two entry points cannot disagree about what a legal CIDR is.
// Parsing each element separately also matters for safety: joining a caller's
// array and handing it to ParseAllowedIPs would let one element containing a
// comma silently become two rules.
//
// An empty or nil list is legal and means "no destination is reachable"; see
// EncodeAllowedPorts for why the empty port list means the opposite.
func ParseAllowedIPList(values []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, peerInvalid("allowed_ips contains an empty entry")
		}
		if strings.Contains(trimmed, policySeparator) {
			return nil, peerInvalid(fmt.Sprintf("allowed_ips entry %q must be a single cidr, not a list", value))
		}
		parsed, err := ParseAllowedIPs(trimmed)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed...)
	}
	return out, nil
}

// ParseAllowedPorts reads canonical allowed_ports text back into ports. The
// empty string parses to the empty set, which means every port is allowed.
//
// Range syntax such as "80-90" is deliberately rejected even though
// routing.ParsePorts accepts it: the canonical form is a flat ascending list, and
// accepting a shorthand would let text into storage that EncodeAllowedPorts
// cannot reproduce byte for byte. Callers that want to accept range syntax from a
// human expand it through routing.ParsePorts first.
func ParseAllowedPorts(encoded string) ([]int, error) {
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" {
		return nil, nil
	}
	tokens := strings.Split(trimmed, policySeparator)
	out := make([]int, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, peerInvalid("allowed_ports contains an empty entry")
		}
		port, err := strconv.Atoi(token)
		if err != nil {
			return nil, peerInvalid(fmt.Sprintf("allowed_ports entry %q is not an integer", token))
		}
		if port < minPort || port > maxPort {
			return nil, peerInvalid(fmt.Sprintf("allowed_ports entry %d must be between %d and %d", port, minPort, maxPort))
		}
		out = append(out, port)
	}
	return out, nil
}

// validateLabel checks a free-text field against its column width and rejects
// control characters. The width stops a silent truncation in MySQL; the control
// character rule stops a name from injecting a second line into a log entry or a
// rendered configuration file.
func validateLabel(field, value string, max int) error {
	if utf8.RuneCountInString(value) > max {
		return peerInvalid(fmt.Sprintf("%s must be at most %d characters", field, max))
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return peerInvalid(fmt.Sprintf("%s must not contain control characters", field))
		}
	}
	return nil
}

func peerInvalid(reason string) *Error {
	return ErrPeerInvalid.WithMessage("vpn peer is invalid: " + reason)
}
