// Package reconnect holds the retry policy shared by the Agent and Client
// WebSocket loops, so both recover from a dropped link the same way.
package reconnect

import (
	"errors"
	"math/rand"
	"time"

	"golang.org/x/net/websocket"
)

const (
	// StableAfter is how long a session must have lived before the next failure
	// is treated as a fresh incident. Without it the attempt counter only grows,
	// and a link that ran for days waits the maximum backoff after a single drop.
	StableAfter = time.Minute

	// rejectedThreshold is the number of consecutive handshake rejections after
	// which the retry ceiling is raised. x/net reports a non-101 upgrade as a bare
	// ErrBadStatus without the HTTP code, so a revoked token (401/403) cannot be
	// told apart from a proxy 502; repetition is the only available signal.
	rejectedThreshold = 3

	// RejectedMaxBackoff is the ceiling used once rejections keep repeating. A
	// revoked token will not recover on its own, so hammering every 30s only adds
	// load and log noise; a transient 502 still recovers within minutes.
	RejectedMaxBackoff = 5 * time.Minute
)

// Backoff tracks consecutive failures of one reconnect loop. It is not safe for
// concurrent use; each loop owns its own instance.
type Backoff struct {
	base, max time.Duration
	rng       *rand.Rand
	attempt   int
	rejected  int
}

// New returns a Backoff. rng must be non-nil.
func New(base, max time.Duration, rng *rand.Rand) *Backoff {
	return &Backoff{base: base, max: max, rng: rng}
}

// AfterDialFailure records a failed dial or handshake and returns the wait.
func (b *Backoff) AfterDialFailure(err error) time.Duration {
	b.attempt++
	if IsRejected(err) {
		b.rejected++
	} else {
		b.rejected = 0
	}
	ceiling := b.max
	if b.rejected >= rejectedThreshold && ceiling < RejectedMaxBackoff {
		ceiling = RejectedMaxBackoff
	}
	return Delay(b.base, ceiling, b.attempt, b.rng)
}

// AfterSession records the end of an established session and returns the wait.
// A session that outlived StableAfter resets the counters first.
func (b *Backoff) AfterSession(lived time.Duration) time.Duration {
	if lived >= StableAfter {
		b.attempt = 0
	}
	b.rejected = 0
	b.attempt++
	return Delay(b.base, b.max, b.attempt, b.rng)
}

// Delay is the wait before attempt number attempt (1-based): exponential from
// base, capped at max, with equal jitter so a fleet that lost the Server at the
// same instant spreads its reconnects over [delay/2, delay] instead of ±20%.
func Delay(base, max time.Duration, attempt int, rng *rand.Rand) time.Duration {
	delay := base
	for i := 1; i < attempt && delay < max; i++ {
		delay *= 2
	}
	if delay > max {
		delay = max
	}
	half := delay / 2
	return half + time.Duration(rng.Float64()*float64(delay-half))
}

// IsRejected reports whether err is the Server answering the upgrade with a
// non-101 status.
func IsRejected(err error) bool {
	var dialErr *websocket.DialError
	return errors.As(err, &dialErr) && errors.Is(dialErr.Err, websocket.ErrBadStatus)
}
