package proxyentry

import (
	"net/http"
	"regexp"
	"strings"
)

// proxyRouteNamePattern is the <name> part of tp-<name>.<suffix>. The 32-char
// bound keeps a single DNS label legal and keeps RouteKey short enough to be a
// low-cardinality Prometheus label.
var proxyRouteNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// RouteKey is the normalized lowercase full proxy host, e.g.
// tp-demo.tm.example.com. Normalizing once at the boundary means every
// downstream lookup (route table, metrics, audit) can compare plain strings.
type RouteKey string

// Name strips the tp- prefix and the domain suffix, returning "demo" for
// "tp-demo.tm.example.com". Used for log and audit readability.
func (k RouteKey) Name() string {
	host := strings.TrimPrefix(string(k), "tp-")
	if idx := strings.IndexByte(host, '.'); idx > 0 {
		host = host[:idx]
	}
	return host
}

// RouteIdentity extracts the route a proxy request belongs to.
type RouteIdentity interface {
	Resolve(r *http.Request) (RouteKey, error)
}

// HeaderRouteIdentity trusts a header that only the OpenResty front end may
// set. The internal listener rejects untrusted peers before any request is
// read, and OpenResty rebuilds the header from SNI instead of forwarding the
// client's copy, so in a correctly deployed topology this value is never
// attacker-controlled.
type HeaderRouteIdentity struct {
	Header       string
	DomainSuffix string
}

func NewHeaderRouteIdentity(header, domainSuffix string) HeaderRouteIdentity {
	return HeaderRouteIdentity{Header: header, DomainSuffix: normalizeSuffix(domainSuffix)}
}

// Resolve requires exactly one header value. Zero means OpenResty did not
// inject it (misconfiguration); more than one means someone tried to smuggle a
// second identity past the front end. Both are rejected identically.
func (h HeaderRouteIdentity) Resolve(r *http.Request) (RouteKey, error) {
	values := r.Header.Values(h.Header)
	if len(values) != 1 {
		return "", ErrRouteIdentityInvalid
	}
	return normalizeRouteKey(values[0], h.DomainSuffix)
}

// SNIRouteIdentity is the A2 fallback path: the Server terminates TLS itself
// and the route name comes from the handshake rather than from a header. It is
// kept so the entry can run without OpenResty, at the cost of the Server having
// to hold the wildcard certificate.
type SNIRouteIdentity struct{ DomainSuffix string }

func NewSNIRouteIdentity(domainSuffix string) SNIRouteIdentity {
	return SNIRouteIdentity{DomainSuffix: normalizeSuffix(domainSuffix)}
}

func (s SNIRouteIdentity) Resolve(r *http.Request) (RouteKey, error) {
	if r.TLS == nil || strings.TrimSpace(r.TLS.ServerName) == "" {
		return "", ErrRouteIdentityInvalid
	}
	return normalizeRouteKey(r.TLS.ServerName, s.DomainSuffix)
}

func normalizeSuffix(raw string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
}

// normalizeRouteKey accepts the bare name ("demo"), the prefixed name
// ("tp-demo") or the full host ("tp-demo.tm.example.com") and always returns
// the lowercase full host.
//
// The suffix check uses HasSuffix on "."+suffix rather than comparing the first
// label, so a host that merely contains the suffix deeper in the name
// (tp-x.tm.example.com.evil.test) is rejected instead of being resolved to
// someone else's route.
func normalizeRouteKey(raw, suffix string) (RouteKey, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" || suffix == "" {
		return "", ErrRouteIdentityInvalid
	}
	host := value
	if strings.Contains(host, ".") {
		dotted := "." + suffix
		if !strings.HasSuffix(host, dotted) {
			return "", ErrRouteIdentityInvalid
		}
		host = strings.TrimSuffix(host, dotted)
	}
	name := strings.TrimPrefix(host, "tp-")
	if name == "" || !proxyRouteNamePattern.MatchString(name) {
		return "", ErrRouteIdentityInvalid
	}
	return RouteKey("tp-" + name + "." + suffix), nil
}

// Compile-time assertion: both identities satisfy RouteIdentity, so wiring the
// wrong one in fails here rather than at request time.
var (
	_ RouteIdentity = HeaderRouteIdentity{}
	_ RouteIdentity = SNIRouteIdentity{}
)
