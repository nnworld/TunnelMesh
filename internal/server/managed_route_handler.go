package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/routing"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	managedRoutePageSize = 500
	managedRouteCacheTTL = 5 * time.Second
)

// ManagedRouteTable caches the database route snapshot for a short interval.
// The TTL keeps cluster nodes eventually consistent without querying MySQL on
// every public request.
type ManagedRouteTable struct {
	db            *storage.DB
	dynamicSuffix string
	ttl           time.Duration

	mu            sync.RWMutex
	resolver      *routing.RouteResolver
	loadedAt      time.Time
	lastAttemptAt time.Time
}

func NewManagedRouteTable(db *storage.DB, dynamicSuffix string, ttl time.Duration) *ManagedRouteTable {
	if ttl <= 0 {
		ttl = managedRouteCacheTTL
	}
	return &ManagedRouteTable{db: db, dynamicSuffix: dynamicSuffix, ttl: ttl}
}

// Resolver returns a consistent immutable resolver snapshot. A stale snapshot
// is preferred over failing public traffic when a refresh transiently fails.
func (t *ManagedRouteTable) Resolver(ctx context.Context) (*routing.RouteResolver, error) {
	if t == nil {
		return nil, errors.New("managed route table unavailable")
	}
	now := time.Now()
	t.mu.RLock()
	if t.resolver != nil && now.Sub(t.lastAttemptAt) < t.ttl {
		resolver := t.resolver
		t.mu.RUnlock()
		return resolver, nil
	}
	t.mu.RUnlock()

	t.mu.Lock()
	defer t.mu.Unlock()
	now = time.Now()
	if t.resolver != nil && now.Sub(t.lastAttemptAt) < t.ttl {
		return t.resolver, nil
	}
	t.lastAttemptAt = now
	routes, err := loadManagedRoutes(ctx, t.db)
	if err != nil {
		if t.resolver != nil {
			slog.WarnContext(ctx, "managed route refresh failed; using cached routes", "error", err.Error())
			return t.resolver, nil
		}
		return nil, err
	}
	t.resolver = routing.NewRouteResolver(routes, routing.WithDynamicSuffix(t.dynamicSuffix))
	t.loadedAt = now
	return t.resolver, nil
}

func loadManagedRoutes(ctx context.Context, db *storage.DB) ([]routing.Route, error) {
	if db == nil || db.Tunnels() == nil {
		return nil, errors.New("managed route repository unavailable")
	}
	var routes []routing.Route
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		page, err := db.Tunnels().List(ctx, cursor, managedRoutePageSize)
		if err != nil {
			return nil, err
		}
		for _, tunnel := range page.Items {
			if !managedTunnelActive(tunnel) {
				continue
			}
			var config struct {
				HostHeader    string `json:"hostHeader"`
				TargetScheme  string `json:"targetScheme"`
				TLSServerName string `json:"tlsServerName"`
			}
			if tunnel.Config != "" {
				if err := json.Unmarshal([]byte(tunnel.Config), &config); err != nil {
					return nil, errors.New("managed route config is invalid: " + tunnel.ID)
				}
			}
			routes = append(routes, routing.Route{
				ID: tunnel.ID, RouteID: tunnel.ID, Domain: tunnel.Domain,
				PathPrefix: tunnel.PathPrefix, AgentID: tunnel.AgentID,
				TargetHost: tunnel.TargetHost, TargetPort: tunnel.TargetPort,
				Protocol: tunnel.Protocol, HostHeader: config.HostHeader,
				TargetScheme: config.TargetScheme, TLSServerName: config.TLSServerName,
			})
		}
		if !page.HasMore {
			return routes, nil
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return nil, errors.New("managed route pagination did not advance")
		}
		if _, ok := seenCursors[page.NextCursor]; ok {
			return nil, errors.New("managed route pagination repeated a cursor")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func managedTunnelActive(tunnel storage.Tunnel) bool {
	return tunnel.Status == "active" && tunnel.Domain != "" && tunnel.AgentID != "" &&
		tunnel.TargetHost != "" && tunnel.TargetPort >= 1 && tunnel.TargetPort <= 65535
}

// ManagedRouteHandler dispatches matched public Hosts to an Agent stream. It
// returns false for unmatched Hosts so the embedded admin SPA remains usable.
type ManagedRouteHandler struct {
	Table *ManagedRouteTable
	Proxy *HTTPProxyHandler
}

func NewManagedRouteHandler(table *ManagedRouteTable, proxy *HTTPProxyHandler) *ManagedRouteHandler {
	return &ManagedRouteHandler{Table: table, Proxy: proxy}
}

func (h *ManagedRouteHandler) TryServeHTTP(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.Table == nil {
		return false
	}
	resolver, err := h.Table.Resolver(r.Context())
	if err != nil {
		http.Error(w, "managed routes unavailable", http.StatusServiceUnavailable)
		return true
	}
	route, err := resolver.ResolveHTTP(r.Host, r.URL.Path)
	if errors.Is(err, routing.ErrRouteNotFound) {
		return false
	}
	if err != nil {
		http.Error(w, "route forbidden", http.StatusForbidden)
		return true
	}
	h.Proxy.ServeRoute(w, r, route)
	return true
}
