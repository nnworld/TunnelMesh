package server

import (
	"context"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const DefaultWebSSHSweepInterval = time.Minute

// WebSSHSweeper converges pending tickets and active sessions whose durable
// TTL has passed. Both repository updates are idempotent.
type WebSSHSweeper struct {
	sessions storage.WebSSHSessionRepository
	interval time.Duration
}

func NewWebSSHSweeper(sessions storage.WebSSHSessionRepository, interval time.Duration) *WebSSHSweeper {
	if interval <= 0 {
		interval = DefaultWebSSHSweepInterval
	}
	return &WebSSHSweeper{sessions: sessions, interval: interval}
}

// Run converges WebSSH rows once before the first tick, then on the interval.
//
// One unreachable query must not retire the job: a pending ticket nothing
// expires stays redeemable-looking, and an active session nothing closes keeps an
// Agent stream and its target connection open. Both are repaired by a later
// round, so the loop logs the failure and keeps ticking.
func (s *WebSSHSweeper) Run(ctx context.Context) error {
	// A missing repository is a wiring error rather than a retryable round, so it
	// reports immediately instead of entering a loop that can only fail.
	if s == nil || s.sessions == nil {
		return ErrWebSSHSessionUnavailable
	}
	return runSweepLoop(ctx, "webssh_session", s.interval, func(ctx context.Context) error {
		_, err := s.Sweep(ctx, time.Now().UTC())
		return err
	})
}

func (s *WebSSHSweeper) Sweep(ctx context.Context, now time.Time) (int64, error) {
	if s == nil || s.sessions == nil {
		return 0, ErrWebSSHSessionUnavailable
	}
	pending, err := s.sessions.ExpirePending(ctx, now)
	if err != nil {
		return pending, err
	}
	active, err := s.sessions.CloseExpiredActive(ctx, now)
	return pending + active, err
}
