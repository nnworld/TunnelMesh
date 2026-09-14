package proxyentry

import "context"

// Route lifecycle and authentication modes as stored in tunnels.status and in
// the route config JSON. They are duplicated here on purpose: proxyentry must
// not import internal/storage, otherwise the policy kernel would drag the whole
// persistence layer into every consumer.
const (
	AuthModeNone  = "none"
	AuthModeBasic = "basic"

	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// Route is the policy-relevant snapshot of one tp-* managed route.
//
// It is a value snapshot rather than a live handle: the server rebuilds these
// from the managed-route table on a short TTL, which is what makes a new route
// take effect without a restart. Target host/port are intentionally absent --
// for a proxy route the target comes from each request, and the stored
// target_host/target_port are the sentinels "*" / 0.
type Route struct {
	ID      string
	Domain  string // lowercase full host, e.g. tp-demo.tm.example.com
	Name    string // demo
	AgentID string

	AuthMode     string
	CredentialID string

	// SourceCIDRs is the client-side allowlist. Empty means deny everything:
	// an administrator who wants to open the route to the internet has to say
	// so explicitly with 0.0.0.0/0.
	SourceCIDRs []string
	// TargetCIDRs and TargetPorts further restrict what the route may reach.
	// Empty means "no additional restriction beyond the dangerous-address deny
	// list and AllowPrivateTargets".
	TargetCIDRs []string
	TargetPorts []int

	AllowPrivateTargets  bool
	MaxConcurrentTunnels int
	Status               string
}

// Active reports whether the route may serve traffic. Anything that is not
// explicitly active (including an empty status from a malformed row) is treated
// as inactive so a bad row fails closed.
func (r Route) Active() bool { return r.Status == StatusActive }

// RequiresAuth reports whether Basic credentials must be verified. Unknown or
// empty auth modes do not require auth here; they are rejected earlier by
// config/API validation, and this method only answers the runtime question.
func (r Route) RequiresAuth() bool { return r.AuthMode == AuthModeBasic }

// RouteSource is implemented by the server-side managed-route snapshot. The
// bool return distinguishes "no such route" from "route exists but unusable"
// without an extra error type: callers map a miss to the same 403 as a disabled
// route so the two are indistinguishable from outside.
type RouteSource interface {
	ProxyRoute(ctx context.Context, domain string) (Route, bool)
}
