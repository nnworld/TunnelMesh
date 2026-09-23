package server

import (
	"embed"
	"io/fs"
	"net"
	"net/http"
	"strings"
)

// webDist contains the production Vite bundle. API and WebSocket paths are
// intentionally mounted before the SPA so they can never be swallowed by the
// history fallback.
//
// all: 是必需的：go:embed 默认排除以 _ 或 . 开头的文件，而 Vite 会产出
// _baseClone-*.js 这类 lodash 辅助 chunk；漏嵌后这些请求会落到 SPA history
// fallback 返回 index.html，浏览器无法把它当 ES module，引用它的路由视图白屏。
//
//go:embed all:web_dist
var webDist embed.FS

func NewWebHandler(api http.Handler, ws http.Handler, handlers ...http.Handler) http.Handler {
	var clientWS, healthHandler http.Handler
	if len(handlers) > 0 {
		clientWS = handlers[0]
	}
	if len(handlers) > 1 {
		healthHandler = handlers[1]
	}
	return NewWebHandlerWithManagedRoutes(api, ws, clientWS, healthHandler, nil, nil)
}

// ManagedRouteDispatcher resolves a request against the managed route table and
// reports whether it claimed the request. Dispatch is Host-scoped, not
// path-scoped: a Host that belongs to a route hands every path to that route,
// because the reserved control-plane prefixes exist to keep the SPA history
// fallback from swallowing the console's own API and WebSocket calls, not to
// claim paths on somebody else's origin.
type ManagedRouteDispatcher interface {
	TryServeHTTP(http.ResponseWriter, *http.Request) bool
}

// managedNamespaceLabelPrefix is the DNS label prefix TunnelMesh allocates for
// managed routes: `tm-*` wildcard routes and dynamic agent/ip/port hostnames.
// Names in this namespace are user traffic by construction, so such a Host is
// never the admin console origin, the Agent fleet origin, or a load-balancer
// health target, and no path on it is reserved for the control plane.
const managedNamespaceLabelPrefix = "tm-"

// isManagedNamespaceHost reports whether host is in the managed-route namespace.
// It is a pure string predicate on purpose: deciding whether a Host belongs to
// the control plane must not require route resolution, otherwise a route-table
// outage would also take down the console and the health probes.
func isManagedNamespaceHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if bare, _, err := net.SplitHostPort(host); err == nil {
		host = bare
	}
	host = strings.TrimSuffix(host, ".")
	if i := strings.IndexByte(host, '.'); i >= 0 {
		host = host[:i]
	}
	return strings.HasPrefix(host, managedNamespaceLabelPrefix)
}

// isControlPlaneReservedPath reports whether path is one of TunnelMesh's own
// protocol or operations endpoints. These stay with the control plane on a Host
// that is not in the managed-route namespace: an explicit route domain is chosen
// by an operator and could collide with the origin Agents dial or the origin a
// load balancer probes, and losing either has a much larger blast radius than
// one proxied request.
func isControlPlaneReservedPath(path string) bool {
	switch path {
	case "/ws/agent", "/ws/client", "/metrics":
		return true
	}
	return strings.HasPrefix(path, "/health/")
}

func NewWebHandlerWithManagedRoutes(api, ws, clientWS, health, webSSH http.Handler, managed ManagedRouteDispatcher) http.Handler {
	root, _ := fs.Sub(webDist, "web_dist")
	static := staticFileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A Host in the managed-route namespace belongs entirely to its route,
		// so it is dispatched before any control-plane prefix is considered.
		// Falling through when no route claims the name keeps an unmatched tm-*
		// Host behaving exactly as it did before.
		if managed != nil && isManagedNamespaceHost(r.Host) {
			if managed.TryServeHTTP(w, r) {
				return
			}
		} else if managed != nil && !isControlPlaneReservedPath(r.URL.Path) {
			// Every other Host that matches a route belongs to that route too,
			// including its /api/ and /ws/ paths. Reserved endpoints skip route
			// resolution entirely rather than losing to it, so the Agent fleet
			// and health probes stay reachable during a route-table outage.
			if managed.TryServeHTTP(w, r) {
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if api == nil {
				http.NotFound(w, r)
			} else {
				api.ServeHTTP(w, r)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/health/") || r.URL.Path == "/metrics" {
			if health == nil {
				http.NotFound(w, r)
			} else {
				health.ServeHTTP(w, r)
			}
			return
		}
		if r.URL.Path == "/ws/agent" {
			if ws == nil {
				http.NotFound(w, r)
			} else {
				ws.ServeHTTP(w, r)
			}
			return
		}
		if r.URL.Path == "/ws/client" {
			if clientWS == nil {
				http.NotFound(w, r)
			} else {
				clientWS.ServeHTTP(w, r)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/ws/webssh/") {
			if webSSH == nil {
				http.NotFound(w, r)
			} else {
				webSSH.ServeHTTP(w, r)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if f, err := root.Open(path); err == nil {
				_ = f.Close()
				static.ServeHTTP(w, r)
				return
			}
		}
		r.URL.Path = "/"
		static.ServeHTTP(w, r)
	})
}

// staticFileServer wraps the embedded file server and pins the WASM media
// type. Host mime databases often lack a .wasm entry, which made browsers
// reject streaming compilation and fall back to slower ArrayBuffer decoding.
func staticFileServer(root fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".wasm") {
			w.Header().Set("Content-Type", "application/wasm")
		}
		fileServer.ServeHTTP(w, r)
	})
}
