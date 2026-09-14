package proxyentry

import (
	"errors"
	"net"
	"strings"
)

// ErrInvalidCIDR is returned by the write path (management API) when an ACL
// entry cannot be parsed. It is a distinct sentinel so the API layer can map it
// to a 400 while the runtime path never sees a parse error at all.
var ErrInvalidCIDR = errors.New("invalid CIDR")

// SourceACL is an immutable, fail-closed source address allowlist.
//
// It is a value type on purpose: a route snapshot carries its own ACL, so
// rebuilding the snapshot cannot race with requests holding an older copy.
//
// Two different "deny everything" states exist and must stay distinguishable:
// an empty list is a deliberate administrator choice (the route is not open to
// any source yet), while broken means the stored value could not be parsed at
// runtime. Both deny, but only broken is surfaced in logs and metrics as a
// configuration fault.
type SourceACL struct {
	networks []*net.IPNet
	broken   bool
}

// NewSourceACL parses every entry. A single invalid entry fails the whole call
// and yields DenyAllSourceACL(): silently skipping a bad entry would change the
// effective policy without the administrator knowing in which direction.
func NewSourceACL(cidrs []string) (SourceACL, error) {
	if len(cidrs) == 0 {
		return SourceACL{}, nil
	}
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, raw := range cidrs {
		normalized, err := NormalizeCIDR(raw)
		if err != nil {
			return DenyAllSourceACL(), err
		}
		_, network, err := net.ParseCIDR(normalized)
		if err != nil {
			return DenyAllSourceACL(), err
		}
		networks = append(networks, network)
	}
	return SourceACL{networks: networks}, nil
}

// DenyAllSourceACL is the fail-closed value used when a stored ACL cannot be
// parsed at runtime.
func DenyAllSourceACL() SourceACL {
	return SourceACL{broken: true}
}

// Broken reports whether construction failed. An empty (nil-entry) ACL is not
// broken; it is a valid deny-all configuration.
func (a SourceACL) Broken() bool { return a.broken }

// Len returns the number of parsed networks, used by the management API to
// echo the effective policy back without exposing internals.
func (a SourceACL) Len() int { return len(a.networks) }

// Allow reports whether ip may use the route. An empty ACL denies everything:
// opening a route to the internet requires an explicit 0.0.0.0/0.
func (a SourceACL) Allow(ip net.IP) bool {
	if a.broken || len(a.networks) == 0 || len(ip) == 0 {
		return false
	}
	// Normalize to the 4-byte form so an IPv4 address arriving as a v4-in-v6
	// mapped 16-byte value still matches an IPv4 network.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, network := range a.networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseClientIP strictly parses the client address taken from the trusted
// header. net.ParseIP already rejects the "ip:port" and comma-separated forms
// that X-Forwarded-For style headers use, which is exactly what we want: the
// front end must send one bare address, and anything else is a deployment bug
// that has to fail closed rather than be guessed at.
//
// A parse failure returns ErrRouteIdentityInvalid (403) rather than a dedicated
// code: the stable error-code set is fixed at twelve entries, and a missing or
// malformed client-IP header means the front end did not deliver a usable
// identity, which is what that code already describes.
func ParseClientIP(raw string) (net.IP, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, ErrRouteIdentityInvalid
	}
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return nil, ErrRouteIdentityInvalid
	}
	return ip, nil
}

// NormalizeCIDR canonicalizes one ACL entry, adding /32 or /128 to a bare IP so
// the stored value is always an explicit network. Host bits are masked off
// ("11.71.85.7/24" becomes "11.71.85.0/24") so two spellings of the same
// network cannot both be stored and later compare unequal.
func NormalizeCIDR(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", ErrInvalidCIDR
	}
	if !strings.Contains(value, "/") {
		ip := net.ParseIP(value)
		if ip == nil {
			return "", ErrInvalidCIDR
		}
		if ip.To4() != nil {
			value += "/32"
		} else {
			value += "/128"
		}
	}
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		return "", ErrInvalidCIDR
	}
	return network.String(), nil
}
