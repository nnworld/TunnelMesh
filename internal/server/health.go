package server

import (
	"context"
	"encoding/json"
	"net/http"
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

// NewHealthHandler mounts liveness, readiness, and Prometheus endpoints.
func NewHealthHandler(checker HealthChecker, metrics http.Handler) http.Handler {
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
