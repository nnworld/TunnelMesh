package server

import (
	"context"
	"errors"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const DefaultClientMetadataSweepInterval = 30 * time.Second

// ClientMetadataSweeper marks client metadata past its TTL as stale. Marking
// is idempotent, so a retry after a partial failure is safe.
type ClientMetadataSweeper struct {
	instances storage.ClientInstanceRepository
	interval  time.Duration
}

func NewClientMetadataSweeper(instances storage.ClientInstanceRepository, interval time.Duration) *ClientMetadataSweeper {
	if interval <= 0 {
		interval = DefaultClientMetadataSweepInterval
	}
	return &ClientMetadataSweeper{instances: instances, interval: interval}
}

func (s *ClientMetadataSweeper) Run(ctx context.Context) error {
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

// Sweep reconciles Client instance rows and returns how many rows it changed.
// Expiring lapsed metadata and reaping rows of Clients that never identified
// themselves are independent facts, so both are attempted and both failures stay
// observable. Each step is idempotent and the reap only matches rows with no live
// lease, so a retry after a partial failure is safe and a reconnecting Client is
// never dropped mid-handshake.
func (s *ClientMetadataSweeper) Sweep(ctx context.Context, now time.Time) (int64, error) {
	if s == nil || s.instances == nil {
		return 0, ErrSessionClosed
	}
	expired, expiredErr := s.instances.MarkExpired(ctx, now)
	purged, purgeErr := s.instances.PurgeUnreported(ctx, now)
	return expired + purged, errors.Join(expiredErr, purgeErr)
}
