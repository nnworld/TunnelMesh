package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeHealthChecker struct {
	liveErr error
	ready   []ComponentStatus
}

func (f fakeHealthChecker) Live(context.Context) error              { return f.liveErr }
func (f fakeHealthChecker) Ready(context.Context) []ComponentStatus { return f.ready }

func TestHealthHandlerLivenessDoesNotDependOnReadiness(t *testing.T) {
	h := NewHealthHandler(fakeHealthChecker{ready: []ComponentStatus{{Name: "database", Healthy: false}}}, nil)
	r := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("live status=%d, want %d", w.Code, http.StatusOK)
	}
}

func TestHealthHandlerReadinessReturns503WithoutSensitiveDetails(t *testing.T) {
	h := NewHealthHandler(fakeHealthChecker{ready: []ComponentStatus{{Name: "database", Healthy: false, Error: "mysql://user:secret@host/db"}}}, nil)
	r := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status=%d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	body := w.Body.String()
	if strings.Contains(body, "mysql://") || strings.Contains(body, "secret") {
		t.Fatalf("readiness leaked sensitive detail: %q", body)
	}
}

func TestHealthHandlerMetricsUsesPrometheusContentType(t *testing.T) {
	metrics := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP tunnelmesh_ready readiness\n"))
	})
	h := NewHealthHandler(fakeHealthChecker{}, metrics)
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics status=%d content-type=%q", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestWebHandlerHealthRoutesPrecedeSPAFallback(t *testing.T) {
	h := NewWebHandler(http.NotFoundHandler(), nil, nil, NewHealthHandler(fakeHealthChecker{}, http.NotFoundHandler()))
	for _, path := range []string{"/health/live", "/health/ready", "/metrics"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "<!doctype html") {
			t.Fatalf("%s was swallowed by SPA fallback", path)
		}
	}
}
