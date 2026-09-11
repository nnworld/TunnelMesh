package client

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

type remoteValidationCacheEntry struct {
	allowed    bool
	kind       string
	expiresAt  time.Time
	lastAccess time.Time
}

type remoteValidationCall struct {
	done    chan struct{}
	allowed bool
}

type remoteValidationCache struct {
	mu       sync.Mutex
	entries  map[string]remoteValidationCacheEntry
	inFlight map[string]*remoteValidationCall
	key      []byte
	max      int
	closed   bool
}

func newRemoteValidationCache(maxEntries int) *remoteValidationCache {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// Random generation practically does not fail on supported platforms.
		// Keep a per-process fallback rather than deriving a stable key from
		// credentials, which could make cache entries survive process restarts.
		fallback := sha256.Sum256([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
		key = fallback[:]
	}
	return &remoteValidationCache{
		entries: make(map[string]remoteValidationCacheEntry), inFlight: make(map[string]*remoteValidationCall),
		key: key, max: maxEntries,
	}
}

func (c *remoteValidationCache) cacheKey(endpoint string, request RemoteValidationRequest) string {
	credentialMAC := hmac.New(sha256.New, c.key)
	_, _ = credentialMAC.Write([]byte(request.Username))
	_, _ = credentialMAC.Write([]byte{0})
	_, _ = credentialMAC.Write([]byte(request.Password))
	endpointMAC := hmac.New(sha256.New, c.key)
	_, _ = endpointMAC.Write([]byte(endpoint))

	return strings.ToLower(strings.TrimSpace(request.Protocol)) + "\x00" +
		strings.TrimSpace(request.AgentID) + "\x00" +
		strings.ToLower(strings.TrimSpace(request.TargetHost)) + "\x00" +
		strconv.Itoa(request.TargetPort) + "\x00" +
		hex.EncodeToString(endpointMAC.Sum(nil)) + "\x00" +
		hex.EncodeToString(credentialMAC.Sum(nil))
}

func (c *remoteValidationCache) lookup(key string, now time.Time) (remoteValidationCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lookupLocked(key, now)
}

func (c *remoteValidationCache) lookupLocked(key string, now time.Time) (remoteValidationCacheEntry, bool) {
	if c.closed {
		return remoteValidationCacheEntry{}, false
	}
	entry, ok := c.entries[key]
	if !ok || now.After(entry.expiresAt) {
		if ok {
			delete(c.entries, key)
		}
		return remoteValidationCacheEntry{}, false
	}
	entry.lastAccess = now
	c.entries[key] = entry
	return entry, true
}

func (c *remoteValidationCache) store(key string, entry remoteValidationCacheEntry, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if len(c.entries) >= c.max {
		c.evictLocked(now)
	}
	c.entries[key] = entry
}

func (c *remoteValidationCache) evictLocked(now time.Time) {
	oldestKey := ""
	var oldest time.Time
	for key, entry := range c.entries {
		if oldestKey == "" || entry.lastAccess.Before(oldest) {
			oldestKey, oldest = key, entry.lastAccess
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func (c *remoteValidationCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.entries = make(map[string]remoteValidationCacheEntry)
	}
}

func (c *remoteValidationCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.entries = make(map[string]remoteValidationCacheEntry)
	c.inFlight = make(map[string]*remoteValidationCall)
}
