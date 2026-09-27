package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// ComponentStatus is the bounded readiness state of one runtime dependency.
// Error is retained for internal diagnostics but is never serialized by the
// health endpoint, because dependency errors can contain credentials or URLs.
type ComponentStatus struct {
	Name    string
	Healthy bool
	Error   string
}

// HealthChecker supplies process liveness and dependency readiness checks.
type HealthChecker interface {
	Live(context.Context) error
	Ready(context.Context) []ComponentStatus
}

// MetricsPolicy guards the Prometheus endpoint.
//
// A registry leaks more than numbers: metric names and label values describe the
// internal topology, build versions and traffic shape of the deployment, which is
// more than an unauthenticated internet listener should hand out. The zero value
// allows every request, so a Server behind a reverse proxy that already restricts
// /metrics keeps its current behaviour.
type MetricsPolicy struct {
	// Token is the bearer credential a scraper must present. It is only ever supplied
	// through server.metrics.token or TUNNELMESH_SERVER_METRICS_TOKEN, and never
	// logged or serialized.
	Token string
}

// NewHealthHandler mounts liveness, readiness, and Prometheus endpoints. At most one
// MetricsPolicy is honoured; extra values are ignored rather than merged, so a
// caller cannot accidentally widen the guard by passing two.
func NewHealthHandler(checker HealthChecker, metrics http.Handler, metricsPolicy ...MetricsPolicy) http.Handler {
	var policy MetricsPolicy
	if len(metricsPolicy) > 0 {
		policy = metricsPolicy[0]
	}
	requiredToken := strings.TrimSpace(policy.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/health/live":
			if checker != nil && checker.Live(r.Context()) != nil {
				writeHealthJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unhealthy"})
				return
			}
			writeHealthJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		case "/health/ready":
			components := []ComponentStatus(nil)
			if checker != nil {
				components = checker.Ready(r.Context())
			}
			ready := len(components) > 0
			safe := make([]map[string]any, 0, len(components))
			for _, component := range components {
				if !component.Healthy {
					ready = false
				}
				safe = append(safe, map[string]any{"name": component.Name, "healthy": component.Healthy})
			}
			status := http.StatusOK
			state := "ready"
			if !ready {
				status = http.StatusServiceUnavailable
				state = "not_ready"
			}
			writeHealthJSON(w, status, map[string]any{"status": state, "components": safe})
		case "/metrics":
			if metrics == nil {
				http.NotFound(w, r)
				return
			}
			// Health probes stay open above this guard: a monitoring credential must
			// never become a reason the load balancer declares the node dead.
			if requiredToken != "" && !presentsBearerToken(r, requiredToken) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			metrics.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func writeHealthJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// presentsBearerToken reports whether the request carries the expected bearer token.
//
// The comparison is constant time so an attacker cannot binary search the token one
// byte at a time over many scrapes, and only the conventional Authorization form is
// accepted: a token in a query string would land in access logs.
func presentsBearerToken(r *http.Request, expected string) bool {
	presented := bearerToken(r)
	if presented == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}
