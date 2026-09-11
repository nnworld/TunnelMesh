package server

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

type AuthorizationCacheConfig struct {
	Enabled              bool
	PositiveTTL          time.Duration
	NegativeTTL          time.Duration
	RevisionPollInterval time.Duration
	MaxStaleOnPollError  time.Duration
	MaxEntries           int
}

type AuthorizationRevisionSource interface {
	Current(context.Context) (uint64, error)
}

type authorizationCacheEntry struct {
	allowed    bool
	err        error
	expiresAt  time.Time
	lastAccess time.Time
}

type authorizationCacheCall struct {
	done    chan struct{}
	allowed bool
	err     error
}

type cachingStreamAuthorizer struct {
	inner      StreamAuthorizer
	revisions  AuthorizationRevisionSource
	config     AuthorizationCacheConfig
	metrics    *observability.Metrics
	mu         sync.Mutex
	entries    map[string]authorizationCacheEntry
	inFlight   map[string]*authorizationCacheCall
	generation uint64
	lastGood   time.Time
	healthy    bool
	notify     chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
}

func NewCachingStreamAuthorizer(inner StreamAuthorizer, revisions AuthorizationRevisionSource, config AuthorizationCacheConfig, metrics *observability.Metrics) (StreamAuthorizer, func() error, error) {
	if inner == nil {
		return nil, nil, errors.New("server: inner stream authorizer is required")
	}
	if config.Enabled {
		if revisions == nil {
			return nil, nil, errors.New("server: authorization revision source is required")
		}
		if config.PositiveTTL <= 0 || config.NegativeTTL <= 0 ||
			config.RevisionPollInterval <= 0 || config.MaxStaleOnPollError <= 0 || config.MaxEntries <= 0 {
			return nil, nil, errors.New("server: authorization cache configuration is invalid")
		}
	}
	cache := &cachingStreamAuthorizer{
		inner: inner, revisions: revisions, config: config, metrics: metrics,
		entries: make(map[string]authorizationCacheEntry), inFlight: make(map[string]*authorizationCacheCall),
		notify: make(chan struct{}, 1), done: make(chan struct{}),
	}
	if config.Enabled {
		cache.pollRevision(context.Background())
		go cache.pollLoop()
	}
	return cache, cache.Close, nil
}

func (a *cachingStreamAuthorizer) Authorize(ctx context.Context, principal ClientSessionPrincipal, request protocol.StreamOpenPayload) error {
	if a == nil || a.inner == nil {
		return errors.New("server: stream authorizer unavailable")
	}
	if !a.config.Enabled {
		return a.inner.Authorize(ctx, principal, request)
	}
	key := authorizationCacheKey(principal, request)
	now := time.Now()
	a.mu.Lock()
	// Check the entry and in-flight call in one critical section. Otherwise a
	// request can miss the old cache before a result is stored, then miss the
	// in-flight entry after it is deleted, and duplicate the inner call.
	if entry, ok := a.lookupLocked(key, now); ok {
		a.mu.Unlock()
		a.observeCache("hit", entry.allowed)
		if entry.allowed {
			return nil
		}
		return entry.err
	}
	if call := a.inFlight[key]; call != nil {
		a.mu.Unlock()
		select {
		case <-call.done:
			if call.allowed {
				a.observeCache("coalesced", true)
				return nil
			}
			a.observeCache("coalesced", false)
			return call.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	call := &authorizationCacheCall{done: make(chan struct{})}
	a.inFlight[key] = call
	a.mu.Unlock()

	err := a.inner.Authorize(ctx, principal, request)
	allowed := err == nil
	a.store(key, allowed, err, time.Now())
	// Publish the result before closing done. Waiters read these fields
	// without the lock after the channel closes, so assigning afterward would
	// create a data race and could expose zero values.
	call.allowed, call.err = allowed, err
	a.mu.Lock()
	delete(a.inFlight, key)
	a.mu.Unlock()
	close(call.done)
	a.observeCache("miss", allowed)
	return err
}

func (a *cachingStreamAuthorizer) lookup(key string, now time.Time) (authorizationCacheEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lookupLocked(key, now)
}

func (a *cachingStreamAuthorizer) lookupLocked(key string, now time.Time) (authorizationCacheEntry, bool) {
	entry, ok := a.entries[key]
	if !ok || now.After(entry.expiresAt) {
		if ok {
			delete(a.entries, key)
		}
		return authorizationCacheEntry{}, false
	}
	// A stale revision source must never continue authorizing from an old
	// positive entry. Negative entries remain safe and time-bounded.
	if entry.allowed && !a.healthy && (a.lastGood.IsZero() || now.Sub(a.lastGood) > a.config.MaxStaleOnPollError) {
		delete(a.entries, key)
		return authorizationCacheEntry{}, false
	}
	entry.lastAccess = now
	a.entries[key] = entry
	return entry, true
}

func (a *cachingStreamAuthorizer) store(key string, allowed bool, authorizationErr error, now time.Time) {
	ttl := a.config.NegativeTTL
	if allowed {
		ttl = a.config.PositiveTTL
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.entries) >= a.config.MaxEntries {
		a.evictLocked(now)
	}
	a.entries[key] = authorizationCacheEntry{allowed: allowed, err: authorizationErr, expiresAt: now.Add(ttl), lastAccess: now}
}

func (a *cachingStreamAuthorizer) evictLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for key, entry := range a.entries {
		if oldestKey == "" || entry.lastAccess.Before(oldest) {
			oldestKey, oldest = key, entry.lastAccess
		}
	}
	if oldestKey != "" {
		delete(a.entries, oldestKey)
	}
}

func (a *cachingStreamAuthorizer) pollLoop() {
	ticker := time.NewTicker(a.config.RevisionPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.pollRevision(context.Background())
		case <-a.notify:
			a.pollRevision(context.Background())
		case <-a.done:
			return
		}
	}
}

func (a *cachingStreamAuthorizer) pollRevision(ctx context.Context) {
	revision, err := a.revisions.Current(ctx)
	now := time.Now()
	a.mu.Lock()
	if err != nil {
		a.healthy = false
	} else {
		a.healthy = true
		a.lastGood = now
		if revision != a.generation {
			a.generation = revision
			a.entries = make(map[string]authorizationCacheEntry)
		}
	}
	a.mu.Unlock()
	if a.metrics != nil {
		if err != nil {
			a.metrics.ObserveAuthorizationRevisionPoll("failure", observability.NormalizeErrorClass(err))
		} else {
			a.metrics.SetAuthorizationRevision(revision)
			a.metrics.ObserveAuthorizationRevisionPoll("success", "")
		}
	}
}

// NotifyAuthorizationChange requests an immediate revision poll. Local
// mutation paths use it to avoid waiting for the next cluster poll tick.
func (a *cachingStreamAuthorizer) NotifyAuthorizationChange() {
	if a == nil || !a.config.Enabled {
		return
	}
	select {
	case a.notify <- struct{}{}:
	default:
	}
}

func (a *cachingStreamAuthorizer) Unhealthy() bool {
	if a == nil || !a.config.Enabled {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.healthy
}

func (a *cachingStreamAuthorizer) Close() error {
	if a == nil {
		return nil
	}
	a.closeOnce.Do(func() { close(a.done) })
	return nil
}

func (a *cachingStreamAuthorizer) observeCache(cache string, allowed bool) {
	if a.metrics == nil {
		return
	}
	result := "deny"
	if allowed {
		result = "allow"
	}
	a.metrics.ObserveAuthorizationCache(cache, result)
}

func authorizationCacheKey(principal ClientSessionPrincipal, request protocol.StreamOpenPayload) string {
	return principal.Identity.TokenID + "\x00" + principal.Identity.OwnerUserID + "\x00" +
		string(principal.Identity.Type) + "\x00" + request.AgentID + "\x00" +
		request.Protocol + "\x00" + request.TargetHost + "\x00" + strconv.Itoa(request.TargetPort)
}
