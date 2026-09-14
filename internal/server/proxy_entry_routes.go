package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
			cfg := proxyRouteConfig{AllowPrivateTargets: boolPtr(true)}
			if tunnel.Config != "" {
				if err := json.Unmarshal([]byte(tunnel.Config), &cfg); err != nil {
					return nil, errors.New("proxy route config is invalid: " + tunnel.ID)
				}
			}
			if cfg.AllowPrivateTargets == nil {
				cfg.AllowPrivateTargets = boolPtr(true)
			}
			authMode := strings.ToLower(strings.TrimSpace(cfg.AuthMode))
			if authMode == "" {
				authMode = proxyentry.AuthModeNone
			}
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
