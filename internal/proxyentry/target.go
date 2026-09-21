package proxyentry

import (
	"net"
	"regexp"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

// proxyHostnamePattern bounds a target hostname to DNS-legal characters. It is
// intentionally permissive about label structure (the agent resolves and
// re-validates the answer) but strict about the characters that could be used to
// smuggle a path, a userinfo section or a second host into the dial target.
var proxyHostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$`)

// TargetPolicy decides whether one requested target may be reached through a
// route. It is the Server-side half of a two-sided check: the agent validates
// the resolved address again before dialing, so a policy bypass here still does
// not produce an unchecked connection.
//
// It wraps routing.Policy rather than reimplementing it so that the deny list
// (metadata, multicast, non-routable, IPv6) stays identical to the one already
// applied to agent policies.
type TargetPolicy struct {
	policy              routing.Policy
	allowPrivateTargets bool
	restrictCIDRs       bool
}

// NewTargetPolicy builds a policy from a route's target CIDRs, target ports and
// private-target switch. An invalid CIDR or port fails here, at route-write
// time, rather than at request time.
func NewTargetPolicy(cidrs []string, ports []int, allowPrivateTargets bool) (TargetPolicy, error) {
	policy, err := routing.NewPolicy(cidrs, ports)
	if err != nil {
		return TargetPolicy{}, err
	}
	return TargetPolicy{
		policy:              policy,
		allowPrivateTargets: allowPrivateTargets,
		restrictCIDRs:       len(cidrs) > 0,
	}, nil
}

// Validate checks a target given as a hostname or an IP literal.
func (p TargetPolicy) Validate(host string, port int) error {
	if port < 1 || port > 65535 {
		return ErrTargetInvalid
	}
	trimmed := strings.TrimSpace(host)
	if trimmed == "" || len(trimmed) > 253 || strings.ContainsAny(trimmed, " /\t\r\n") {
		return ErrTargetInvalid
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return p.ValidateIP(ip, port)
	}
	if !validProxyHostname(trimmed) {
		return ErrTargetInvalid
	}
	// A CIDR allowlist cannot be evaluated before resolution, so when one is
	// configured domains are denied here exactly like routing.Policy denies them
	// for agent policies. Without an allowlist the domain is deferred to the
	// agent, which re-validates the resolved address.
	if p.restrictCIDRs {
		return ErrTargetDenied
	}
	// The port, unlike the CIDR, is known before resolution. Enforcing it here
	// matters: the agent applies its own generic SSRF policy, not this route's
	// port allowlist, so deferring it would silently bypass the restriction for
	// every domain target.
	if !p.portAllowed(port) {
		return ErrTargetDenied
	}
	return nil
}

// ValidateIP checks an already-parsed address.
func (p TargetPolicy) ValidateIP(ip net.IP, port int) error {
	if ip == nil {
		return ErrTargetInvalid
	}
	if port < 1 || port > 65535 {
		return ErrTargetInvalid
	}
	// Denied unconditionally, regardless of allowPrivateTargets: the cloud
	// metadata address and the multicast/reserved ranges are never a legitimate
	// proxy target.
	if routing.IsDangerousAddress(ip) {
		return ErrTargetDenied
	}
	if !p.allowPrivateTargets && IsPrivateTarget(ip) {
		return ErrTargetDenied
	}
	if err := p.policy.Validate(ip, port); err != nil {
		return ErrTargetDenied
	}
	return nil
}

func (p TargetPolicy) portAllowed(port int) bool {
	if len(p.policy.Ports) == 0 {
		return true
	}
	_, ok := p.policy.Ports[port]
	return ok
}

// IsPrivateTarget reports whether an address belongs to a range that is not
// publicly routable. The implementation lives in routing.IsPrivateTarget so the
// embedded VPN gateway and the tp-* entry share one authoritative security
// decision; this wrapper keeps proxyentry's exported surface unchanged.
func IsPrivateTarget(ip net.IP) bool {
	return routing.IsPrivateTarget(ip)
}

func validProxyHostname(host string) bool {
	if strings.Contains(host, "..") || strings.HasSuffix(host, ".") {
		return false
	}
	return proxyHostnamePattern.MatchString(host)
}
