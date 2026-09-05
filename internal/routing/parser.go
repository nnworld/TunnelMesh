package routing

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

var (
	ErrMalformedDynamicHost = errors.New("routing: malformed dynamic host")
	ErrDangerousAddress     = errors.New("routing: dangerous target address")
	ErrDNSLabelTooLong      = errors.New("routing: dns label exceeds 63 bytes")
)

// DynamicHost is the target encoded in a wildcard DNS label. The encoding is
// deliberately plain text so operators can read and copy it from DNS records.
type DynamicHost struct {
	AgentID    string
	TargetHost string
	IPText     string
	IP         net.IP
	Port       int
	TargetPort int
	Domain     string
}

// ParseDynamicHost parses <agent-id>-<a>-<b>-<c>-<d>-<port>.<suffix>. Agent IDs
// may contain hyphens; the final five fields are always interpreted as the
// IPv4 octets and decimal port.
func ParseDynamicHost(host string, expectedSuffix ...string) (DynamicHost, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || len(host) > 253 {
		return DynamicHost{}, ErrMalformedDynamicHost
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			if len(label) > 63 {
				return DynamicHost{}, ErrDNSLabelTooLong
			}
			return DynamicHost{}, ErrMalformedDynamicHost
		}
		for i, ch := range label {
			if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
				if (i == 0 || i == len(label)-1) && ch == '-' {
					return DynamicHost{}, ErrMalformedDynamicHost
				}
				continue
			}
			return DynamicHost{}, ErrMalformedDynamicHost
		}
	}
	if len(labels) == 0 || len(labels[0]) == 0 || len(labels[0]) > 63 {
		if len(labels) > 0 && len(labels[0]) > 63 {
			return DynamicHost{}, ErrDNSLabelTooLong
		}
		return DynamicHost{}, ErrMalformedDynamicHost
	}
	parts := strings.Split(labels[0], "-")
	if len(parts) < 6 {
		return DynamicHost{}, ErrMalformedDynamicHost
	}
	octets := make([]int, 4)
	for i := 0; i < 4; i++ {
		v, err := strconv.Atoi(parts[len(parts)-5+i])
		if err != nil || v < 0 || v > 255 {
			return DynamicHost{}, ErrMalformedDynamicHost
		}
		octets[i] = v
	}
	port, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || port < 1 || port > 65535 {
		return DynamicHost{}, ErrMalformedDynamicHost
	}
	agent := strings.Join(parts[:len(parts)-5], "-")
	if agent == "" || len(agent) > 63 || strings.HasPrefix(agent, "-") || strings.HasSuffix(agent, "-") {
		return DynamicHost{}, ErrMalformedDynamicHost
	}
	ip := net.IPv4(byte(octets[0]), byte(octets[1]), byte(octets[2]), byte(octets[3]))
	if IsDangerousAddress(ip) {
		return DynamicHost{}, ErrDangerousAddress
	}
	domain := strings.Join(labels[1:], ".")
	if len(expectedSuffix) > 0 && strings.TrimSuffix(strings.ToLower(strings.TrimSpace(expectedSuffix[0])), ".") != domain {
		return DynamicHost{}, ErrMalformedDynamicHost
	}
	return DynamicHost{AgentID: agent, TargetHost: ip.String(), IPText: ip.String(), IP: ip, Port: port, TargetPort: port, Domain: domain}, nil
}

func (d DynamicHost) String() string {
	if d.IP == nil {
		return ""
	}
	o := d.IP.To4()
	if o == nil {
		return ""
	}
	return fmt.Sprintf("%s-%d-%d-%d-%d-%d", d.AgentID, o[0], o[1], o[2], o[3], d.Port)
}
