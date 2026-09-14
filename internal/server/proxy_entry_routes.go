package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// ProxyRoute returns one tp-* route from the cached snapshot. Lookups are
// case-insensitive because the key comes from SNI, and refresh on the same TTL
// as the HTTP resolver so a new route takes effect without a restart.
//
// A disabled route is still returned (with Active() == false): the handler must
// answer it with exactly the same 403 as an unknown route, and filtering it out
// here would make the two cases distinguishable only by re-reading the table.
//
// Like Resolver, a stale snapshot is preferred over failing live proxy traffic
// when a refresh transiently errors.
func (t *ManagedRouteTable) ProxyRoute(ctx context.Context, domain string) (proxyentry.Route, bool) {
	if t == nil {
		return proxyentry.Route{}, false
	}
	key := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if key == "" {
		return proxyentry.Route{}, false
	}
	now := time.Now()
	t.mu.RLock()
	if t.proxyRoutes != nil && now.Sub(t.proxyAttemptAt) < t.ttl {
		route, ok := t.proxyRoutes[key]
		t.mu.RUnlock()
		return route, ok
	}
	t.mu.RUnlock()

	t.mu.Lock()
	defer t.mu.Unlock()
	now = time.Now()
	if t.proxyRoutes != nil && now.Sub(t.proxyAttemptAt) < t.ttl {
		route, ok := t.proxyRoutes[key]
		return route, ok
	}
	t.proxyAttemptAt = now
	routes, err := loadProxyRoutes(ctx, t.db)
	if err != nil {
		if t.proxyRoutes != nil {
			slog.WarnContext(ctx, "proxy route refresh failed; using cached routes", "error", err.Error())
			route, ok := t.proxyRoutes[key]
			return route, ok
		}
		slog.WarnContext(ctx, "proxy route snapshot unavailable", "error", err.Error())
		return proxyentry.Route{}, false
	}
	snapshot := make(map[string]proxyentry.Route, len(routes))
	for _, route := range routes {
		snapshot[route.Domain] = route
	}
	t.proxyRoutes = snapshot
	t.proxyLoadedAt = now
	route, ok := snapshot[key]
	return route, ok
}

// proxyRouteConfig is the on-disk JSON shape of tunnels.config for one tp-*
// route. AllowPrivateTargets is a pointer so "absent" can be told apart from an
// explicit false, which is what makes the documented default (true) work without
// a migration for rows written before the field existed.
type proxyRouteConfig struct {
	AuthMode             string   `json:"authMode"`
	CredentialID         string   `json:"credentialId"`
	SourceCIDRs          []string `json:"sourceCIDRs"`
	TargetCIDRs          []string `json:"targetCIDRs"`
	TargetPorts          []int    `json:"targetPorts"`
	AllowPrivateTargets  *bool    `json:"allowPrivateTargets"`
	MaxConcurrentTunnels int      `json:"maxConcurrentTunnels"`
	Description          string   `json:"description"`
}

// proxyRouteNamePattern is the tp-<name>.<suffix> shape. The name is capped at
// 32 lowercase DNS-safe characters because it becomes the left-most label of a
// public hostname and the lookup key OpenResty derives from SNI.
var proxyRouteNamePattern = regexp.MustCompile(`^tp-[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?\..+$`)

// loadProxyRoutes pages the same tunnels table used by managed HTTP routes and
// decodes the proxy-specific config JSON. It mirrors loadManagedRoutes,
// including its pagination guards: a cursor that does not advance would
// otherwise spin forever on a malformed page.
func loadProxyRoutes(ctx context.Context, db *storage.DB) ([]proxyentry.Route, error) {
	if db == nil || db.Tunnels() == nil {
		return nil, errors.New("proxy route repository unavailable")
	}
	var routes []proxyentry.Route
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		page, err := db.Tunnels().List(ctx, cursor, managedRoutePageSize)
		if err != nil {
			return nil, err
		}
		for _, tunnel := range page.Items {
			if tunnel.Protocol != storage.ProtocolHTTPProxy {
				continue
			}
			if tunnel.Domain == "" || tunnel.AgentID == "" {
				continue
			}
			cfg, err := proxyRouteConfigOf(tunnel.Config)
			if err != nil {
				return nil, errors.New("proxy route config is invalid: " + tunnel.ID)
			}
			authMode := cfg.AuthMode
			domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(tunnel.Domain), "."))
			routes = append(routes, proxyentry.Route{
				ID: tunnel.ID, Domain: domain, Name: proxyentry.RouteKey(domain).Name(),
				AgentID: tunnel.AgentID, AuthMode: authMode, CredentialID: strings.TrimSpace(cfg.CredentialID),
				SourceCIDRs: cfg.SourceCIDRs, TargetCIDRs: cfg.TargetCIDRs, TargetPorts: cfg.TargetPorts,
				AllowPrivateTargets: *cfg.AllowPrivateTargets, MaxConcurrentTunnels: cfg.MaxConcurrentTunnels,
				Status: tunnel.Status,
			})
		}
		if !page.HasMore {
			return routes, nil
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return nil, errors.New("proxy route pagination did not advance")
		}
		if _, ok := seenCursors[page.NextCursor]; ok {
			return nil, errors.New("proxy route pagination repeated a cursor")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func boolPtr(v bool) *bool { return &v }

// proxyRouteConfigOf decodes a stored tunnels.config back into the typed shape
// and applies the documented defaults (authMode=none, allowPrivateTargets=true).
// It is the single read path for the config JSON: loadProxyRoutes uses it for
// the data plane and publicProxyTunnel uses it for the admin API, so the two can
// never disagree about what an absent field means.
func proxyRouteConfigOf(raw string) (proxyRouteConfig, error) {
	cfg := proxyRouteConfig{AuthMode: proxyentry.AuthModeNone, AllowPrivateTargets: boolPtr(true)}
	trimmed := strings.TrimSpace(raw)
	if trimmed != "" && trimmed != "{}" {
		if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
			return proxyRouteConfig{}, err
		}
	}
	if cfg.AllowPrivateTargets == nil {
		cfg.AllowPrivateTargets = boolPtr(true)
	}
	if strings.TrimSpace(cfg.AuthMode) == "" {
		cfg.AuthMode = proxyentry.AuthModeNone
	}
	return cfg, nil
}

// normalizeProxyRouteConfig validates one proxy route policy and returns the
// canonical JSON stored in tunnels.config. The second return value is the
// caller-facing 400 message and is empty when the policy is valid.
//
// Normalization is deterministic on purpose: deduped and sorted lists mean two
// equivalent writes produce byte-identical config, which keeps idempotent
// replays comparable and stops spurious "changed" diffs in the admin UI.
func normalizeProxyRouteConfig(in proxyRouteConfig) (string, string) {
	authMode := strings.ToLower(strings.TrimSpace(in.AuthMode))
	if authMode == "" {
		authMode = proxyentry.AuthModeNone
	}
	if authMode != proxyentry.AuthModeNone && authMode != proxyentry.AuthModeBasic {
		return "", "authMode must be none or basic"
	}
	credentialID := strings.TrimSpace(in.CredentialID)
	if authMode == proxyentry.AuthModeBasic && credentialID == "" {
		return "", "credentialId is required when authMode is basic"
	}
	if authMode == proxyentry.AuthModeNone {
		// Dropping the reference matters: a route that no longer authenticates
		// must not keep pointing the data plane at a secret it never uses.
		credentialID = ""
	}
	sourceCIDRs, message := normalizeCIDRList(in.SourceCIDRs, "sourceCIDRs")
	if message != "" {
		return "", message
	}
	targetCIDRs, message := normalizeCIDRList(in.TargetCIDRs, "targetCIDRs")
	if message != "" {
		return "", message
	}
	for _, port := range in.TargetPorts {
		if port < 1 || port > 65535 {
			return "", "targetPorts must be between 1 and 65535"
		}
	}
	targetPorts := normalizePortList(in.TargetPorts)
	if in.MaxConcurrentTunnels < 0 {
		return "", "maxConcurrentTunnels must be >= 0"
	}
	description := strings.TrimSpace(in.Description)
	if len(description) > 256 {
		return "", "description must be at most 256 characters"
	}
	allowPrivate := true
	if in.AllowPrivateTargets != nil {
		allowPrivate = *in.AllowPrivateTargets
	}
	// Reuse the runtime policy so an accepted write can never produce a route
	// the data plane would then refuse to build.
	if _, err := proxyentry.NewTargetPolicy(targetCIDRs, targetPorts, allowPrivate); err != nil {
		return "", "target policy is invalid: " + err.Error()
	}
	if _, err := proxyentry.NewSourceACL(sourceCIDRs); err != nil && len(sourceCIDRs) > 0 {
		return "", "sourceCIDRs are invalid"
	}
	out := proxyRouteConfig{
		AuthMode: authMode, CredentialID: credentialID,
		SourceCIDRs: sourceCIDRs, TargetCIDRs: targetCIDRs, TargetPorts: targetPorts,
		AllowPrivateTargets: &allowPrivate, MaxConcurrentTunnels: in.MaxConcurrentTunnels,
		Description: description,
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", "invalid proxy route config"
	}
	return string(encoded), ""
}

// normalizeCIDRList accepts bare IPs by adding the host mask, drops duplicates
// and keeps the caller's order so the UI list stays stable. The result is always
// non-nil: an empty allowlist must round-trip as [] and not as null.
func normalizeCIDRList(raw []string, field string) ([]string, string) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		normalized, err := proxyentry.NormalizeCIDR(item)
		if err != nil {
			return nil, field + " contains an invalid entry: " + strings.TrimSpace(item)
		}
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, ""
}

// normalizePortList dedupes and sorts. routing.normalizePorts is not reusable
// here: it only coerces the loosely typed policy input and keeps duplicates.
func normalizePortList(raw []int) []int {
	out := make([]int, 0, len(raw))
	seen := make(map[int]struct{}, len(raw))
	for _, port := range raw {
		if _, dup := seen[port]; dup {
			continue
		}
		seen[port] = struct{}{}
		out = append(out, port)
	}
	sort.Ints(out)
	return out
}

// validateProxyRouteDomain enforces the tp-<name>.<suffix> shape. It is checked
// in addition to routing.ValidateDomainPattern because the tp- prefix is what
// makes a hostname reachable through the proxy entry's wildcard server block,
// and the name length cap keeps the derived SNI label usable.
func validateProxyRouteDomain(domain string) error {
	if !proxyRouteNamePattern.MatchString(domain) {
		return errors.New("http-proxy domain must look like tp-<name>.<suffix> with a lowercase name of at most 32 characters")
	}
	return nil
}
