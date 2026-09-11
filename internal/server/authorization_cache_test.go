package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

type recordingStreamAuthorizer struct {
	calls  atomic.Int32
	allow  func(protocol.StreamOpenPayload) bool
	start  chan struct{}
	releas chan struct{}
}

func (a *recordingStreamAuthorizer) Authorize(_ context.Context, _ ClientSessionPrincipal, request protocol.StreamOpenPayload) error {
	a.calls.Add(1)
	if a.start != nil {
		a.start <- struct{}{}
		<-a.releas
	}
	if a.allow == nil || a.allow(request) {
		return nil
	}
	return auth.ErrForbidden
}

type fakeRevisionSource struct {
	revision atomic.Uint64
	err      atomic.Value
}

func (s *fakeRevisionSource) Current(context.Context) (uint64, error) {
	if err, ok := s.err.Load().(error); ok && err != nil {
		return 0, err
	}
	return s.revision.Load(), nil
}

func cacheTestConfig() AuthorizationCacheConfig {
	return AuthorizationCacheConfig{
		Enabled: true, PositiveTTL: time.Minute, NegativeTTL: time.Minute,
		RevisionPollInterval: time.Millisecond, MaxStaleOnPollError: 10 * time.Millisecond, MaxEntries: 16,
	}
}

func cachePrincipal() ClientSessionPrincipal {
	return ClientSessionPrincipal{ConnectionID: "connection", Identity: auth.TokenIdentity{TokenID: "token", Type: "client"}}
}

func cacheRequest(agentID string) protocol.StreamOpenPayload {
	return protocol.StreamOpenPayload{AgentID: agentID, Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22}
}

func TestCachingStreamAuthorizerCachesPositiveAndNegativeDecisions(t *testing.T) {
	inner := &recordingStreamAuthorizer{allow: func(request protocol.StreamOpenPayload) bool { return request.AgentID == "allowed" }}
	revisions := &fakeRevisionSource{}
	revisions.revision.Store(1)
	authorizer, cleanup, err := NewCachingStreamAuthorizer(inner, revisions, cacheTestConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()

	allowed := cacheRequest("allowed")
	denied := cacheRequest("denied")
	for i := 0; i < 2; i++ {
		if err := authorizer.Authorize(context.Background(), cachePrincipal(), allowed); err != nil {
			t.Fatalf("Authorize(allowed) error=%v", err)
		}
		if err := authorizer.Authorize(context.Background(), cachePrincipal(), denied); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("Authorize(denied) error=%v, want forbidden", err)
		}
	}
	if inner.calls.Load() != 2 {
		t.Fatalf("inner calls=%d, want 2", inner.calls.Load())
	}
}

func TestCachingStreamAuthorizerKeyEncodesPortNumerically(t *testing.T) {
	principal := cachePrincipal()
	first := cacheRequest("agent")
	first.TargetPort = 55296
	second := first
	second.TargetPort = 55297

	if authorizationCacheKey(principal, first) == authorizationCacheKey(principal, second) {
		t.Fatal("authorization cache keys collided for ports 55296 and 55297")
	}
}

func TestCachingStreamAuthorizerCoalescesConcurrentIdenticalRequests(t *testing.T) {
	inner := &recordingStreamAuthorizer{start: make(chan struct{}, 1), releas: make(chan struct{})}
	revisions := &fakeRevisionSource{}
	revisions.revision.Store(1)
	authorizer, cleanup, err := NewCachingStreamAuthorizer(inner, revisions, cacheTestConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- authorizer.Authorize(context.Background(), cachePrincipal(), cacheRequest("agent"))
		}()
	}
	select {
	case <-inner.start:
	case <-time.After(time.Second):
		t.Fatal("inner authorizer did not start")
	}
	close(inner.releas)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls.Load() != 1 {
		t.Fatalf("inner calls=%d, want 1", inner.calls.Load())
	}
}

func TestCachingStreamAuthorizerRevisionChangeInvalidatesEntries(t *testing.T) {
	inner := &recordingStreamAuthorizer{}
	revisions := &fakeRevisionSource{}
	revisions.revision.Store(1)
	authorizer, cleanup, err := NewCachingStreamAuthorizer(inner, revisions, cacheTestConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	if err := authorizer.Authorize(context.Background(), cachePrincipal(), cacheRequest("agent")); err != nil {
		t.Fatal(err)
	}

	revisions.revision.Store(2)
	deadline := time.Now().Add(time.Second)
	for inner.calls.Load() == 1 && time.Now().Before(deadline) {
		if err := authorizer.Authorize(context.Background(), cachePrincipal(), cacheRequest("agent")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if inner.calls.Load() != 2 {
		t.Fatalf("inner calls=%d after revision change, want 2", inner.calls.Load())
	}
}

func TestCachingStreamAuthorizerFailsClosedWhenRevisionSourceIsStale(t *testing.T) {
	inner := &recordingStreamAuthorizer{}
	revisions := &fakeRevisionSource{}
	revisions.revision.Store(1)
	authorizer, cleanup, err := NewCachingStreamAuthorizer(inner, revisions, cacheTestConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	if err := authorizer.Authorize(context.Background(), cachePrincipal(), cacheRequest("agent")); err != nil {
		t.Fatal(err)
	}

	revisions.err.Store(errors.New("database unavailable"))
	time.Sleep(20 * time.Millisecond)
	if err := authorizer.Authorize(context.Background(), cachePrincipal(), cacheRequest("agent")); err != nil {
		t.Fatal(err)
	}
	if inner.calls.Load() != 2 {
		t.Fatalf("inner calls=%d after stale revision source, want bypass", inner.calls.Load())
	}
}

func TestCachingStreamAuthorizerDisabledBypassesCache(t *testing.T) {
	inner := &recordingStreamAuthorizer{}
	config := cacheTestConfig()
	config.Enabled = false
	authorizer, cleanup, err := NewCachingStreamAuthorizer(inner, &fakeRevisionSource{}, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	for i := 0; i < 2; i++ {
		if err := authorizer.Authorize(context.Background(), cachePrincipal(), cacheRequest("agent")); err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls.Load() != 2 {
		t.Fatalf("inner calls=%d, want bypass on every request", inner.calls.Load())
	}
}
