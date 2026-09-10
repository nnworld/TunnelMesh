package routing

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

var ErrRouteNotFound = errors.New("routing: route not found")

// Route is both an explicit managed route and the result of a dynamic route.
type Route struct {
	ID            string
	RouteID       string // compatibility alias used by API payloads
	Domain        string
	PathPrefix    string
	AgentID       string
	TargetHost    string
	TargetPort    int
	Protocol      string
	HostHeader    string
	TargetScheme  string
	TLSServerName string
	AllowedCIDRs  []string
	AllowedPorts  []int
	Dynamic       bool
}

type resolverOptions struct {
	dynamicSuffix string
	policy        Policy
}
type RouteTable []Route
type RouteResolverConfig struct {
	Routes        []Route
	DynamicSuffix string
	Policy        Policy
}
type ResolverOption func(*resolverOptions)

func WithDynamicSuffix(suffix string) ResolverOption {
	return func(o *resolverOptions) {
		o.dynamicSuffix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(suffix)), ".")
	}
}
func WithPolicy(p Policy) ResolverOption { return func(o *resolverOptions) { o.policy = p } }

type RouteResolver struct {
	routes        []Route
	dynamicSuffix string
	policy        Policy
}

func NewRouteResolver(source any, options ...ResolverOption) *RouteResolver {
	o := resolverOptions{dynamicSuffix: "apps.example.com"}
	var routes []Route
	switch v := source.(type) {
	case []Route:
		routes = v
	case RouteTable:
		routes = []Route(v)
	case *RouteTable:
		if v != nil {
			routes = []Route(*v)
		}
	case RouteResolverConfig:
		routes = v.Routes
		if v.DynamicSuffix != "" {
			o.dynamicSuffix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v.DynamicSuffix)), ".")
		}
		o.policy = v.Policy
	case *RouteResolverConfig:
		if v != nil {
			routes = v.Routes
			if v.DynamicSuffix != "" {
				o.dynamicSuffix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v.DynamicSuffix)), ".")
			}
			o.policy = v.Policy
		}
	case nil:
	default:
		// Unsupported sources intentionally produce an empty resolver; callers
		// receive ErrRouteNotFound instead of a panic at request time.
	}
	for _, option := range options {
		if option != nil {
			option(&o)
		}
	}
	copyRoutes := append([]Route(nil), routes...)
	return &RouteResolver{routes: copyRoutes, dynamicSuffix: o.dynamicSuffix, policy: o.policy}
}

func NewResolver(routes []Route, options ...ResolverOption) *RouteResolver {
	return NewRouteResolver(routes, options...)
}

// ResolveHTTP accepts host,path or routeID,host,path. The optional route ID is
// useful to callers that already resolved an explicit managed route and has the
// highest precedence by design.
func (r *RouteResolver) ResolveHTTP(args ...string) (Route, error) {
	if r == nil {
		return Route{}, ErrRouteNotFound
	}
	var routeID, host, path string
	switch len(args) {
	case 2:
		host, path = args[0], args[1]
	case 3:
		routeID, host, path = args[0], args[1], args[2]
	default:
		return Route{}, ErrRouteNotFound
	}
	if routeID != "" {
		for _, route := range r.routes {
			if route.ID == routeID || route.RouteID == routeID {
				if err := validateRoutePolicy(route); err != nil {
					return Route{}, err
				}
				if route.ID == "" {
					route.ID = route.RouteID
				}
				return route, nil
			}
		}
	}
	host = normalizeHost(host)
	if host == "" {
		return Route{}, ErrRouteNotFound
	}
	if path == "" {
		path = "/"
	}
	// Explicit route precedence is stable and independent of insertion order.
	var candidates []Route
	for _, route := range r.routes {
		if !domainMatch(route.Domain, host) {
			continue
		}
		if route.PathPrefix != "" && !pathMatch(route.PathPrefix, path) {
			continue
		}
		candidates = append(candidates, route)
	}
	if len(candidates) > 0 {
		sort.SliceStable(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			ad, bd := domainRank(a.Domain, host), domainRank(b.Domain, host)
			if ad != bd {
				return ad > bd
			}
			if len(a.PathPrefix) != len(b.PathPrefix) {
				return len(a.PathPrefix) > len(b.PathPrefix)
			}
			return a.ID < b.ID
		})
		selected := candidates[0]
		if selected.ID == "" {
			selected.ID = selected.RouteID
		}
		if err := validateRoutePolicy(selected); err != nil {
			return Route{}, err
		}
		return selected, nil
	}
	if r.dynamicSuffix != "" && strings.HasSuffix(host, "."+r.dynamicSuffix) {
		d, err := ParseDynamicHost(host)
		if errors.Is(err, ErrDangerousAddress) {
			return Route{}, err
		}
		if err == nil && strings.EqualFold(d.Domain, r.dynamicSuffix) {
			p := r.policy
			if err := p.Validate(d.IP, d.Port); err != nil {
				return Route{}, err
			}
			return Route{ID: "dynamic:" + d.String(), RouteID: "dynamic:" + d.String(), Domain: host, AgentID: d.AgentID, TargetHost: d.TargetHost, TargetPort: d.Port, Protocol: "http", Dynamic: true}, nil
		}
	}
	return Route{}, ErrRouteNotFound
}

func validateRoutePolicy(route Route) error {
	if len(route.AllowedCIDRs) == 0 && len(route.AllowedPorts) == 0 {
		return nil
	}
	ip := net.ParseIP(strings.TrimSpace(route.TargetHost))
	if ip == nil {
		return nil
	}
	p, err := NewPolicy(route.AllowedCIDRs, route.AllowedPorts)
	if err != nil {
		return err
	}
	return p.Validate(ip, route.TargetPort)
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if i := strings.IndexByte(host, ':'); i >= 0 {
		// Host headers may carry a port; only strip it when it is a valid
		// bracketless host:port pair, leaving malformed names unmatched.
		if parsedHost, port, err := net.SplitHostPort(host); err == nil && port != "" {
			if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
				return ""
			}
			host = parsedHost
		}
	}
	return strings.TrimSuffix(host, ".")
}

func domainMatch(pattern, host string) bool {
	pattern = normalizeHost(pattern)
	if pattern == "" {
		return false
	}
	if pattern == host {
		return true
	}
	if strings.HasPrefix(pattern, "tm-*.") {
		base := strings.TrimPrefix(pattern, "tm-*")
		if !strings.HasSuffix(host, base) {
			return false
		}
		label := strings.TrimSuffix(host, base)
		label = strings.TrimSuffix(label, ".")
		return strings.HasPrefix(label, "tm-") && validDNSLabel(label)
	}
	return false
}

func domainRank(pattern, host string) int {
	pattern = normalizeHost(pattern)
	host = normalizeHost(host)
	if pattern == host {
		return 3
	}
	if strings.HasPrefix(pattern, "tm-*.") {
		return 1
	}
	return 0
}

// ValidateDomainPattern accepts exact DNS names and the one supported
// explicit wildcard form, tm-*.example.com. Generic wildcards are rejected so
// operators cannot accidentally expose every subdomain through one route.
func ValidateDomainPattern(raw string) error {
	pattern := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if pattern == "" {
		return nil
	}
	if strings.Contains(pattern, "*") {
		if !strings.HasPrefix(pattern, "tm-*.") || strings.Count(pattern, "*") != 1 {
			return fmt.Errorf("unsupported domain wildcard %q: only tm-*.example.com is allowed", raw)
		}
		base := strings.TrimPrefix(pattern, "tm-*")
		if !validDNSName(base[1:]) {
			return fmt.Errorf("invalid wildcard domain suffix %q", raw)
		}
		return nil
	}
	if !validDNSName(pattern) {
		return fmt.Errorf("invalid domain %q", raw)
	}
	return nil
}

func validDNSName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !validDNSLabel(label) {
			return false
		}
	}
	return true
}

func validDNSLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		ch := label[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
			continue
		}
		return false
	}
	return true
}

func pathMatch(prefix, path string) bool {
	if prefix == "/" {
		return true
	}
	return strings.HasPrefix(path, prefix) && (len(path) == len(prefix) || strings.HasSuffix(prefix, "/") || path[len(prefix)] == '/')
}
