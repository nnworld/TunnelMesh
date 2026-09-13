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

// ManagedRouteDispatcher is tried only after reserved API, health, and
// WebSocket paths, and before the SPA history fallback.
type ManagedRouteDispatcher interface {
	TryServeHTTP(http.ResponseWriter, *http.Request) bool
}

func NewWebHandlerWithManagedRoutes(api, ws, clientWS, health, webSSH http.Handler, managed ManagedRouteDispatcher) http.Handler {
	root, _ := fs.Sub(webDist, "web_dist")
	static := staticFileServer(root)
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
