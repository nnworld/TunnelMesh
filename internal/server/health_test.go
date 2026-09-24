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

func stubMetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# HELP tunnelmesh_test_metric\n"))
	})
}

// TestHealthHandlerMetricsStaysOpenWithoutAPolicy pins the default: an upgrade that
// does not set server.metrics.token must not lock its own monitoring out.
func TestHealthHandlerMetricsStaysOpenWithoutAPolicy(t *testing.T) {
	for name, policy := range map[string][]MetricsPolicy{
		"no policy":   nil,
		"empty token": {{Token: ""}},
	} {
		t.Run(name, func(t *testing.T) {
			h := NewHealthHandler(fakeHealthChecker{}, stubMetricsHandler(), policy...)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 while no token is configured", w.Code)
			}
		})
	}
}

// TestHealthHandlerMetricsRequiresBearerToken is the protection itself: the metric
// names, build info and traffic volumes in a Prometheus endpoint describe internal
// topology, so a Server exposed directly to the internet needs one switch to stop
// the whole registry being public.
func TestHealthHandlerMetricsRequiresBearerToken(t *testing.T) {
	h := NewHealthHandler(fakeHealthChecker{}, stubMetricsHandler(), MetricsPolicy{Token: "s3cret-token"})
	cases := []struct {
		name   string
		header string
		want   int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic c3NyZXQtdG9rZW4=", want: http.StatusUnauthorized},
		{name: "wrong token", header: "Bearer wrong-token", want: http.StatusUnauthorized},
		{name: "prefix of the real token", header: "Bearer s3cret", want: http.StatusUnauthorized},
		{name: "no separator", header: "Bears3cret-token", want: http.StatusUnauthorized},
		{name: "correct", header: "Bearer s3cret-token", want: http.StatusOK},
		{name: "case-insensitive scheme", header: "bearer s3cret-token", want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if tc.want != http.StatusOK {
				if strings.Contains(w.Body.String(), "tunnelmesh_test_metric") {
					t.Fatal("a rejected scrape must not return metric output")
				}
				if got := w.Header().Get("WWW-Authenticate"); got == "" {
					t.Fatal("a rejected scrape must advertise the scheme it expects")
				}
			}
			if strings.Contains(w.Body.String(), "s3cret-token") {
				t.Fatal("the response leaked the configured token")
			}
		})
	}
	// Liveness and readiness are for the load balancer and must stay open: protecting
	// metrics cannot be allowed to black-hole health probes.
	for _, path := range []string{"/health/live", "/health/ready"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code == http.StatusUnauthorized {
			t.Fatalf("%s was challenged, want it to stay open", path)
		}
	}
}
