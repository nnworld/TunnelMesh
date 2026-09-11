package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type RemoteValidationRequest struct {
	Protocol   string `json:"protocol"`
	AgentID    string `json:"agentId"`
	TargetHost string `json:"targetHost"`
	TargetPort int    `json:"targetPort"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
}

type RemoteValidator struct {
	endpoint  string
	client    *http.Client
	config    RemoteValidationCacheConfig
	cache     *remoteValidationCache
	metrics   remoteValidationMetrics
	closeOnce sync.Once
}

func NewRemoteValidator(endpoint string) *RemoteValidator {
	return NewRemoteValidatorWithCache(RemoteValidationCacheConfig{Endpoint: endpoint})
}

func newForwardRemoteValidator(authURL string, config RemoteValidationCacheConfig) *RemoteValidator {
	if config.Endpoint == "" {
		config.Endpoint = authURL
	}
	return NewRemoteValidatorWithCache(config)
}

type RemoteValidationCacheConfig struct {
	Endpoint    string
	PositiveTTL time.Duration
	NegativeTTL time.Duration
	Timeout     time.Duration
	MaxEntries  int
}

type remoteValidationMetrics interface {
	ObserveRemoteValidationCache(cache, result string)
}

func NewRemoteValidatorWithCache(config RemoteValidationCacheConfig) *RemoteValidator {
	if config.PositiveTTL <= 0 {
		config.PositiveTTL = 15 * time.Second
	}
	if config.NegativeTTL <= 0 {
		config.NegativeTTL = 2 * time.Second
	}
	if config.Timeout <= 0 {
		config.Timeout = 3 * time.Second
	}
	if config.MaxEntries <= 0 {
		config.MaxEntries = 10000
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &RemoteValidator{
		endpoint: strings.TrimSpace(config.Endpoint),
		client:   &http.Client{Transport: transport, Timeout: config.Timeout},
		config:   config,
		cache:    newRemoteValidationCache(config.MaxEntries),
	}
}

func (v *RemoteValidator) Close() {
	if v == nil || v.client == nil {
		return
	}
	v.closeOnce.Do(func() {
		v.client.CloseIdleConnections()
		if v.cache != nil {
			v.cache.close()
		}
	})
}

func (v *RemoteValidator) SetMetrics(metrics remoteValidationMetrics) {
	if v != nil {
		v.metrics = metrics
	}
}

func (v *RemoteValidator) Invalidate() {
	if v != nil && v.cache != nil {
		v.cache.invalidate()
	}
}

func (v *RemoteValidator) Validate(ctx context.Context, request RemoteValidationRequest) bool {
	if v == nil || v.endpoint == "" {
		return true
	}
	if v.cache == nil {
		v.cache = newRemoteValidationCache(10000)
	}
	if v.config.PositiveTTL <= 0 {
		v.config.PositiveTTL = 15 * time.Second
	}
	if v.config.NegativeTTL <= 0 {
		v.config.NegativeTTL = 2 * time.Second
	}
	if v.config.Timeout <= 0 {
		v.config.Timeout = 3 * time.Second
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, v.config.Timeout)
	defer cancel()

	key := v.cache.cacheKey(v.endpoint, request)
	now := time.Now()
	v.cache.mu.Lock()
	if entry, ok := v.cache.lookupLocked(key, now); ok {
		v.cache.mu.Unlock()
		v.observeRemoteValidation("hit", entry.kind)
		return entry.allowed
	}
	if call := v.cache.inFlight[key]; call != nil {
		v.cache.mu.Unlock()
		select {
		case <-call.done:
			v.observeRemoteValidation("coalesced", "allow")
			return call.allowed
		case <-ctx.Done():
			return false
		}
	}
	call := &remoteValidationCall{done: make(chan struct{})}
	v.cache.inFlight[key] = call
	v.cache.mu.Unlock()

	payload, err := json.Marshal(request)
	if err != nil {
		v.finishRemoteValidation(key, call, false, "network_error", time.Now())
		return false
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, bytes.NewReader(payload))
	if err != nil {
		v.finishRemoteValidation(key, call, false, "network_error", time.Now())
		return false
	}
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := v.client.Do(httpRequest)
	if err != nil {
		v.finishRemoteValidation(key, call, false, "network_error", time.Now())
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	allowed := response.StatusCode >= 200 && response.StatusCode < 300
	kind := "deny"
	if allowed {
		kind = "allow"
	}
	v.finishRemoteValidation(key, call, allowed, kind, time.Now())
	return allowed
}

func (v *RemoteValidator) finishRemoteValidation(key string, call *remoteValidationCall, allowed bool, kind string, now time.Time) {
	ttl := v.config.NegativeTTL
	if allowed {
		ttl = v.config.PositiveTTL
	}
	v.cache.store(key, remoteValidationCacheEntry{allowed: allowed, kind: kind, expiresAt: now.Add(ttl), lastAccess: now}, now)
	call.allowed = allowed
	v.cache.mu.Lock()
	delete(v.cache.inFlight, key)
	v.cache.mu.Unlock()
	close(call.done)
	v.observeRemoteValidation("miss", kind)
}

func (v *RemoteValidator) observeRemoteValidation(cacheResult, decision string) {
	if v.metrics != nil {
		v.metrics.ObserveRemoteValidationCache(cacheResult, decision)
	}
}
