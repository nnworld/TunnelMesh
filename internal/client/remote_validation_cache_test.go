package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func remoteValidationTestConfig(endpoint string) RemoteValidationCacheConfig {
	return RemoteValidationCacheConfig{
		Endpoint: endpoint, PositiveTTL: time.Minute, NegativeTTL: time.Minute,
		Timeout: time.Second, MaxEntries: 16,
	}
}

func TestRemoteValidatorWithCacheCachesPositiveAndNegativeDecisions(t *testing.T) {
	requests := 0
	server := newRemoteValidationServer(&requests, func(r *http.Request) bool {
		return !strings.Contains(r.URL.Path, "deny")
	})
	defer server.Close()
	validator := NewRemoteValidatorWithCache(remoteValidationTestConfig(server.URL + "/allow"))
	defer validator.Close()

	allowed := RemoteValidationRequest{Protocol: "socks5", AgentID: "agent-a", TargetHost: "service.internal", TargetPort: 443}
	if !validator.Validate(context.Background(), allowed) || !validator.Validate(context.Background(), allowed) {
		t.Fatal("allowed remote validation decision was not cached")
	}
	if requests != 1 {
		t.Fatalf("allow requests=%d, want 1", requests)
	}

	validator.Invalidate()
	validator.endpoint = server.URL + "/deny"
	denied := allowed
	denied.TargetPort = 80
	if validator.Validate(context.Background(), denied) || validator.Validate(context.Background(), denied) {
		t.Fatal("denied remote validation decision was cached as allowed")
	}
	if requests != 2 {
		t.Fatalf("total requests=%d, want 2", requests)
	}
}

func TestRemoteValidatorWithCacheCachesNetworkErrorsAsNegative(t *testing.T) {
	var calls int
	transport := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("network unavailable")
	})
	validator := NewRemoteValidatorWithCache(remoteValidationTestConfig("http://auth.internal/validate"))
	validator.client.Transport = transport
	defer validator.Close()
	request := RemoteValidationRequest{Protocol: "socks5", AgentID: "agent-a", TargetHost: "service.internal", TargetPort: 443}

	for i := 0; i < 2; i++ {
		if validator.Validate(context.Background(), request) {
			t.Fatal("network error was allowed")
		}
	}
	if calls != 1 {
		t.Fatalf("network error calls=%d, want 1", calls)
	}
}

func TestRemoteValidatorWithCacheCoalescesConcurrentRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int
	var mu sync.Mutex
	transport := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		started <- struct{}{}
		<-release
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	validator := NewRemoteValidatorWithCache(remoteValidationTestConfig("http://auth.internal/validate"))
	validator.client.Transport = transport
	defer validator.Close()
	request := RemoteValidationRequest{Protocol: "socks5", AgentID: "agent-a", TargetHost: "service.internal", TargetPort: 443, Username: "alice", Password: "secret"}

	var wg sync.WaitGroup
	results := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- validator.Validate(context.Background(), request)
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(results)
	for allowed := range results {
		if !allowed {
			t.Fatal("coalesced remote validation was denied")
		}
	}
	if calls != 1 {
		t.Fatalf("concurrent calls=%d, want 1", calls)
	}
}

func TestRemoteValidatorWithCacheSeparatesCredentials(t *testing.T) {
	var calls int
	server := newRemoteValidationServer(&calls, func(_ *http.Request) bool { return true })
	defer server.Close()
	validator := NewRemoteValidatorWithCache(remoteValidationTestConfig(server.URL))
	defer validator.Close()

	first := RemoteValidationRequest{Protocol: "socks5", AgentID: "agent-a", TargetHost: "service.internal", TargetPort: 443, Username: "alice", Password: "secret"}
	second := first
	second.Username, second.Password = "bob", "other-secret"
	if !validator.Validate(context.Background(), first) || !validator.Validate(context.Background(), second) {
		t.Fatal("remote validation denied valid credentials")
	}
	if calls != 2 {
		t.Fatalf("credential-isolated calls=%d, want 2", calls)
	}
}

func TestRemoteValidatorWithCacheEvictsBoundedEntries(t *testing.T) {
	var calls int
	server := newRemoteValidationServer(&calls, func(_ *http.Request) bool { return true })
	defer server.Close()
	config := remoteValidationTestConfig(server.URL)
	config.MaxEntries = 1
	validator := NewRemoteValidatorWithCache(config)
	defer validator.Close()

	first := RemoteValidationRequest{Protocol: "socks5", AgentID: "agent-a", TargetHost: "first.internal", TargetPort: 443}
	second := RemoteValidationRequest{Protocol: "socks5", AgentID: "agent-a", TargetHost: "second.internal", TargetPort: 443}
	if !validator.Validate(context.Background(), first) || !validator.Validate(context.Background(), second) {
		t.Fatal("bounded cache denied valid requests")
	}
	if len(validator.cache.entries) != 1 {
		t.Fatalf("cached entries=%d, want 1", len(validator.cache.entries))
	}
}

func TestRemoteValidatorWithCacheCloseDrainsTransportAndEntries(t *testing.T) {
	transport := &closeIdleTransport{roundTrip: roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})}
	validator := NewRemoteValidatorWithCache(remoteValidationTestConfig("http://auth.internal/validate"))
	validator.client.Transport = transport
	request := RemoteValidationRequest{Protocol: "socks5", AgentID: "agent-a", TargetHost: "service.internal", TargetPort: 443}
	if !validator.Validate(context.Background(), request) {
		t.Fatal("remote validation denied a 2xx response")
	}

	validator.Close()

	if !transport.closedIdle {
		t.Fatal("Close did not close idle transport connections")
	}
	if len(validator.cache.entries) != 0 {
		t.Fatalf("cached entries after Close=%d, want 0", len(validator.cache.entries))
	}
}

func TestRemoteValidatorWithCacheMetricsUseBoundedLabels(t *testing.T) {
	metrics := &recordingRemoteValidationMetrics{}
	var calls int
	server := newRemoteValidationServer(&calls, func(_ *http.Request) bool { return true })
	defer server.Close()
	validator := NewRemoteValidatorWithCache(remoteValidationTestConfig(server.URL))
	validator.SetMetrics(metrics)
	defer validator.Close()
	request := RemoteValidationRequest{Protocol: "socks5", AgentID: "agent-a", TargetHost: "service.internal", TargetPort: 443, Username: "alice", Password: "secret"}

	if !validator.Validate(context.Background(), request) || !validator.Validate(context.Background(), request) {
		t.Fatal("remote validation denied valid requests")
	}
	if len(metrics.labels) == 0 {
		t.Fatal("remote validation cache metric was not emitted")
	}
	for _, labels := range metrics.labels {
		for _, label := range labels {
			for _, forbidden := range []string{"alice", "secret", "service.internal", "agent-a"} {
				if strings.Contains(label, forbidden) {
					t.Fatalf("metric label %q contains sensitive value %q", label, forbidden)
				}
			}
		}
	}
}

type recordingRemoteValidationMetrics struct {
	labels [][]string
}

func (m *recordingRemoteValidationMetrics) ObserveRemoteValidationCache(cache, result string) {
	m.labels = append(m.labels, []string{cache, result})
}

type remoteValidationServerFunc func(*http.Request) bool

func newRemoteValidationServer(calls *int, allow remoteValidationServerFunc) *httptest.Server {
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*calls++
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		if allow(r) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
}
