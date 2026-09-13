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

func (s *WebSSHSweeper) Run(ctx context.Context) error {
	if _, err := s.Sweep(ctx, time.Now().UTC()); err != nil {
		return err
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := s.Sweep(ctx, time.Now().UTC()); err != nil {
				return err
			}
		}
	}
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
