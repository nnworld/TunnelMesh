package routing

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

var (
	ErrCIDRNotAllowed = errors.New("routing: target address is outside allowed cidrs")
	ErrPortNotAllowed = errors.New("routing: target port is not allowed")
)

// Policy is evaluated for dynamic routes. Empty CIDR/port lists mean no
// explicit allowlist restriction; dangerous address classes are always denied.
type Policy struct {
	CIDRs       []*net.IPNet
	Ports       map[int]struct{}
	DenyPrivate bool
}

func NewPolicy(cidrs []string, ports any) (Policy, error) {
	p := Policy{Ports: make(map[int]struct{})}
	for _, raw := range cidrs {
		_, n, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return Policy{}, fmt.Errorf("invalid cidr %q: %w", raw, err)
		}
		p.CIDRs = append(p.CIDRs, n)
	}
	portList, err := normalizePorts(ports)
	if err != nil {
		return Policy{}, err
	}
	for _, port := range portList {
		if port < 1 || port > 65535 {
			return Policy{}, fmt.Errorf("invalid port %d", port)
		}
		p.Ports[port] = struct{}{}
	}
	return p, nil
}

func normalizePorts(value any) ([]int, error) {
	switch ports := value.(type) {
	case nil:
		return nil, nil
	case []int:
		return ports, nil
	case []string:
		var out []int
		for _, raw := range ports {
			p, err := ParsePorts(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, p...)
		}
		return out, nil
	case string:
		return ParsePorts(ports)
	default:
		return nil, fmt.Errorf("unsupported port allowlist type %T", value)
	}
}

// ParsePorts accepts comma-separated ports and inclusive ranges used by
// policy records and CLI flags.
func ParsePorts(raw string) ([]int, error) {
	var out []int
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if strings.Contains(token, "-") {
			parts := strings.Split(token, "-")
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid port range %q", token)
			}
			lo, e1 := strconv.Atoi(parts[0])
			hi, e2 := strconv.Atoi(parts[1])
			if e1 != nil || e2 != nil || lo < 1 || hi > 65535 || lo > hi {
				return nil, fmt.Errorf("invalid port range %q", token)
			}
			for p := lo; p <= hi; p++ {
				out = append(out, p)
			}
			continue
		}
		p, err := strconv.Atoi(token)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid port %q", token)
		}
		out = append(out, p)
	}
	return out, nil
}

func (p Policy) Validate(ip net.IP, port int) error {
	if ip == nil || ip.To4() == nil || IsDangerousAddress(ip) {
		return ErrDangerousAddress
	}
	if len(p.CIDRs) > 0 {
		allowed := false
		for _, n := range p.CIDRs {
			if n.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrCIDRNotAllowed
		}
	}
	if len(p.Ports) > 0 {
		if _, ok := p.Ports[port]; !ok {
			return ErrPortNotAllowed
		}
	}
	if port < 1 || port > 65535 {
		return ErrPortNotAllowed
	}
	return nil
}

func IsDangerousAddress(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil {
		return true
	}
	// Dynamic targets are dialed by the Agent, so loopback is a valid
	// agent-local service address. Other non-routable and metadata ranges
	// remain denied.
	return ip[0] == 0 || ip[0] >= 224 || (ip[0] == 169 && ip[1] == 254) || ip.Equal(net.IPv4(169, 254, 169, 254))
}

// IsPrivateTarget reports whether an address belongs to a range that is not
// publicly routable. It is deliberately broader than net.IP.IsPrivate:
// loopback, link-local and the unspecified address all count as "inside the
// target's own network" for the purposes of an allow-private-targets switch.
//
// It lives here, next to IsDangerousAddress, because routing is the package
// that already owns "is this destination reachable/legitimate". Both the tp-*
// proxy entry and the embedded VPN gateway need the identical answer, and
// duplicating a security decision across two packages is how the two copies
// drift apart. Callers that used to implement it locally now delegate here.
//
// A nil or empty address is not private; it is invalid, and validation is the
// caller's job (Policy.Validate rejects it with ErrDangerousAddress).
func IsPrivateTarget(ip net.IP) bool {
	if len(ip) == 0 {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
