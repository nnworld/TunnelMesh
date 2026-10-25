package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

// SetServerVersion sets the version this Server announces; empty means the build's.
func (a *API) SetServerVersion(version string) {
	if a != nil {
		a.serverVersion = version
	}
}

// SetUpdates enables release distribution from dir; an empty dir keeps it off.
func (a *API) SetUpdates(dir string) {
	if a != nil {
		a.updates = NewUpdateService(dir)
	}
}

// serveUpdates answers /updates/latest and /updates/files/<name> for a caller the
// scoped handler already authenticated. rest is the path below ".../updates/".
//
// Both token namespaces (agent and client) reach it, so a client and an agent
// fetch a release through the same code and the same limits.
func (a *API) serveUpdates(w http.ResponseWriter, r *http.Request, identity auth.TokenIdentity, rest string) {
	if a.updates == nil {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch {
	case rest == "latest":
		version, err := a.updates.Latest()
		switch {
		case errors.Is(err, errNoManifest):
			writeAPIError(w, http.StatusNotFound, "no release is published")
		case err != nil:
			writeAPIError(w, http.StatusInternalServerError, "release manifest is invalid")
		default:
			// Only the two versions: a token must not learn anything else about the
			// Server through this endpoint.
			writeJSON(w, http.StatusOK, map[string]string{"version": version, "server_version": resolveServerVersion(a.serverVersion)})
		}
	case strings.HasPrefix(rest, "files/"):
		a.serveUpdateFile(w, r, identity, strings.TrimPrefix(rest, "files/"))
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

func (a *API) serveUpdateFile(w http.ResponseWriter, r *http.Request, identity auth.TokenIdentity, name string) {
	release, ok := a.updates.acquire(identity.TokenID)
	if !ok {
		w.Header().Set("Retry-After", "30")
		writeAPIError(w, http.StatusServiceUnavailable, "too many concurrent downloads")
		return
	}
	defer release()
	file, info, err := a.updates.Open(name)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// ServeContent provides Range and If-Modified-Since, so an interrupted download
	// resumes instead of starting over.
	http.ServeContent(w, r, name, info.ModTime().UTC().Truncate(time.Second), file)
}
