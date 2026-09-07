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

func NewWebHandler(api http.Handler, ws http.Handler) http.Handler {
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
		if r.URL.Path == "/ws/agent" {
			if ws == nil {
				http.NotFound(w, r)
			} else {
				ws.ServeHTTP(w, r)
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
