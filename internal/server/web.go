package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// webDist contains the production Vite bundle. API and WebSocket paths are
// intentionally mounted before the SPA so they can never be swallowed by the
// history fallback.
//
//go:embed web_dist
var webDist embed.FS

func NewWebHandler(api http.Handler, ws http.Handler, handlers ...http.Handler) http.Handler {
	var clientWS, healthHandler http.Handler
	if len(handlers) > 0 {
		clientWS = handlers[0]
	}
	if len(handlers) > 1 {
		healthHandler = handlers[1]
	}
	return NewWebHandlerWithManagedRoutes(api, ws, clientWS, healthHandler, nil)
}

// ManagedRouteDispatcher is tried only after reserved API, health, and
// WebSocket paths, and before the SPA history fallback.
type ManagedRouteDispatcher interface {
	TryServeHTTP(http.ResponseWriter, *http.Request) bool
}

func NewWebHandlerWithManagedRoutes(api, ws, clientWS, health http.Handler, managed ManagedRouteDispatcher) http.Handler {
	root, _ := fs.Sub(webDist, "web_dist")
	static := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			http.NotFound(w, r)
			return
		}
		if managed != nil && managed.TryServeHTTP(w, r) {
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
