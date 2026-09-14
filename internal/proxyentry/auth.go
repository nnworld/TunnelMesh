package proxyentry

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// CredentialSecret is the decrypted username/password pair for one proxy_basic
// credential. It is deliberately short-lived: the resolver returns it, Authorize
// compares it, and nothing stores it. It must never reach a log, an audit
// record or a metric label.
type CredentialSecret struct{ Username, Password string }

// SecretResolver is implemented by the server on top of
// storage.CredentialRepository plus auth.SecretStore.
type SecretResolver interface {
	ProxyBasicSecret(ctx context.Context, credentialID string) (CredentialSecret, error)
}

// ErrSecretStoreUnavailable is returned by SecretResolver implementations when
// decryption cannot even be attempted (missing TUNNELMESH_TOKEN_ENCRYPTION_KEY,
// store failure). It is distinguished from a wrong password on purpose: the
// client is not at fault, so the attempt must not be counted and the response is
// a 503 rather than a 407.
var ErrSecretStoreUnavailable = errors.New("proxyentry: credential secret store unavailable")

const (
	// backoffBase is the first penalty window. It doubles per additional
	// failure and is capped at backoffMax, so a brute-force loop converges on a
	// 15 minute wait instead of growing without bound.
	backoffBase = 30 * time.Second
	backoffMax  = 15 * time.Minute
	// attemptTTL is how long a counter survives without a new failure. It equals
	// backoffMax so a client that stops trying eventually gets a clean slate,
	// while the map cannot grow without bound.
	attemptTTL = 15 * time.Minute
	// sweepLimit bounds the opportunistic cleanup so lock hold time stays
	// predictable under load; there is no background goroutine to do it.
	sweepLimit = 64
	// maxBackoffShift keeps the doubling from overflowing a Duration.
	maxBackoffShift = 5
)

type authAttempt struct {
	failures    int
	until       time.Time
	lastFailure time.Time
}

// Authenticator verifies Basic credentials and throttles repeated failures.
//
// Throttling is keyed by route AND client IP, not by route alone: one client
// brute-forcing a shared route must not lock out every other user of it.
type Authenticator struct {
	resolver  SecretResolver
	threshold int

	mu       sync.Mutex
	now      func() time.Time
	attempts map[string]*authAttempt
}

func NewAuthenticator(resolver SecretResolver, backoffThreshold int) *Authenticator {
	if backoffThreshold <= 0 {
		backoffThreshold = 1
	}
	return &Authenticator{
		resolver:  resolver,
		threshold: backoffThreshold,
		now:       time.Now,
		attempts:  map[string]*authAttempt{},
	}
}

// SetClock replaces the time source. Test-only; production uses time.Now.
func (a *Authenticator) SetClock(now func() time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	a.now = now
}

// Attempts reports the current failure count for one route/client pair. Used by
// tests and by diagnostics; it is not an authorization input.
func (a *Authenticator) Attempts(routeID, clientIP string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.attempts[attemptKey(routeID, clientIP)]
	if entry == nil {
		return 0
	}
	return entry.failures
}

// Authorize verifies the Proxy-Authorization header for one request.
//
// Order matters. The backoff check runs before the resolver is called so a
// throttled client cannot use the entry as a password-oracle or make the server
// do decryption work per attempt.
func (a *Authenticator) Authorize(ctx context.Context, route Route, clientIP net.IP, header string) error {
	if !route.RequiresAuth() {
		// A none-mode route does not authenticate at all. Stray credentials are
		// accepted rather than rejected: clients routinely send cached proxy
		// credentials to every proxy they talk to.
		return nil
	}
	client := ""
	if clientIP != nil {
		client = clientIP.String()
	}
	key := attemptKey(route.ID, client)

	a.sweep(key)

	if route.CredentialID == "" {
		// Misconfigured route: it asks for auth but has no credential to check
		// against. Fail closed, and count it so the backoff still protects the
		// secret store from being probed through a broken route.
		a.recordFailure(key)
		return ErrAuthFailed
	}
	if a.inBackoff(key) {
		return ErrAuthBackoff
	}

	username, password, ok := parseBasicHeader(header)
	if !ok {
		a.recordFailure(key)
		return ErrAuthRequired
	}
	secret, err := a.resolver.ProxyBasicSecret(ctx, route.CredentialID)
	if err != nil {
		if errors.Is(err, ErrSecretStoreUnavailable) {
			return ErrSecretUnavailable
		}
		// Any other resolver error (missing credential, revoked, wrong type) is
		// indistinguishable from bad credentials to the caller, so it is
		// reported as a plain auth failure and prevents credential-ID probing.
		a.recordFailure(key)
		return ErrAuthFailed
	}
	if !constantTimeEqual(username, secret.Username) || !constantTimeEqual(password, secret.Password) {
		a.recordFailure(key)
		return ErrAuthFailed
	}
	a.clearAttempts(key)
	return nil
}

// sweep expires stale state for one key and opportunistically for up to
// sweepLimit others.
//
// An expired backoff window is cleared but the failure count is kept. Resetting
// the count too would let an attacker get a fresh 30s window forever by simply
// waiting it out, so the penalty has to keep doubling.
func (a *Authenticator) sweep(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if entry := a.attempts[key]; entry != nil {
		if now.Sub(entry.lastFailure) > attemptTTL {
			delete(a.attempts, key)
		} else {
			clearExpiredBackoff(entry, now)
		}
	}
	checked := 0
	for other, entry := range a.attempts {
		if checked >= sweepLimit {
			break
		}
		checked++
		if other == key {
			continue
		}
		if now.Sub(entry.lastFailure) > attemptTTL {
			delete(a.attempts, other)
			continue
		}
		clearExpiredBackoff(entry, now)
	}
}

func clearExpiredBackoff(entry *authAttempt, now time.Time) {
	if !entry.until.IsZero() && !entry.until.After(now) {
		entry.until = time.Time{}
	}
}

func (a *Authenticator) recordFailure(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	entry := a.attempts[key]
	if entry == nil {
		entry = &authAttempt{}
		a.attempts[key] = entry
	}
	entry.failures++
	entry.lastFailure = now
	if entry.failures >= a.threshold {
		entry.until = now.Add(backoffFor(entry.failures, a.threshold))
	}
}

func (a *Authenticator) inBackoff(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.attempts[key]
	if entry == nil || entry.until.IsZero() {
		return false
	}
	return entry.until.After(a.now())
}

func (a *Authenticator) clearAttempts(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.attempts, key)
}

// backoffFor returns 30s at the threshold, doubling per extra failure and
// capped at 15m.
func backoffFor(failures, threshold int) time.Duration {
	shift := failures - threshold
	if shift < 0 {
		shift = 0
	}
	if shift > maxBackoffShift {
		return backoffMax
	}
	window := backoffBase << shift
	if window <= 0 || window > backoffMax {
		return backoffMax
	}
	return window
}

func attemptKey(routeID, clientIP string) string { return routeID + "|" + clientIP }

// parseBasicHeader splits a Proxy-Authorization value.
//
// RFC 7617 forbids a colon in the userid but allows it in the password, so the
// value is split on the first colon only. Requiring exactly one colon would
// reject legal passwords, which is a real lockout given the 407 challenge
// advertises charset="UTF-8".
func parseBasicHeader(header string) (username, password string, ok bool) {
	const prefix = "Basic "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	user, pass, found := strings.Cut(string(decoded), ":")
	if !found || user == "" {
		return "", "", false
	}
	return user, pass, true
}

// constantTimeEqual hashes both sides first so the comparison is always over
// equal-length buffers and cannot leak a length difference through timing.
func constantTimeEqual(a, b string) bool {
	sumA, sumB := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(sumA[:], sumB[:]) == 1
}
