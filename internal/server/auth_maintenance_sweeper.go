package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// DefaultAuthMaintenanceSweepInterval is how often expired identity rows are
// collected. The tables are small and bounded by policy, so the interval is
// chosen for a quiet database rather than for promptness: an expired challenge is
// already rejected by its expiry check long before its row disappears.
const DefaultAuthMaintenanceSweepInterval = time.Minute

// DefaultAuthMaintenanceRetention is how long an expired or revoked row is kept.
// Retention exists so an incident can be reconstructed from the audit log: the
// audit row references a device or challenge id, and deleting the referenced row
// immediately would leave a dangling identifier in the trail.
const DefaultAuthMaintenanceRetention = 7 * 24 * time.Hour

// AuthGauges is the sweeper's observability seam. It is a separate interface from
// AuthMetrics so the login handlers and the background loop each depend only on
// the series they publish.
type AuthGauges interface {
	SetAuthTrustedDevices(float64)
	SetAuthPendingChallenges(float64)
	SetAuthBlockedBuckets(float64)
}

// AuthMaintenanceSweeper deletes expired authentication challenges, expired and
// long-revoked trusted devices, and stale login throttle buckets, then publishes
// the three identity gauges.
//
// Every statement is an idempotent DELETE bounded by a timestamp, so each node in
// a cluster may run its own sweeper: a duplicate pass deletes nothing and costs
// one index scan. That is deliberately preferred over a distributed lock, which
// would add a failure mode to a job whose only requirement is that it eventually
// runs somewhere.
type AuthMaintenanceSweeper struct {
	db        *storage.DB
	interval  time.Duration
	retention time.Duration
	gauges    AuthGauges
	now       func() time.Time
}

// NewAuthMaintenanceSweeper builds the sweeper. Non-positive interval and
// retention fall back to the documented defaults so a partially filled
// configuration cannot turn the job into a hot loop or delete rows immediately.
func NewAuthMaintenanceSweeper(db *storage.DB, interval, retention time.Duration) *AuthMaintenanceSweeper {
	if interval <= 0 {
		interval = DefaultAuthMaintenanceSweepInterval
	}
	if retention <= 0 {
		retention = DefaultAuthMaintenanceRetention
	}
	return &AuthMaintenanceSweeper{
		db:        db,
		interval:  interval,
		retention: retention,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// SetGauges installs the metrics seam. A nil value disables publishing, which
// keeps the sweeper usable in tests and in a deployment without a registry.
func (s *AuthMaintenanceSweeper) SetGauges(gauges AuthGauges) {
	if s != nil {
		s.gauges = gauges
	}
}

// SetClock overrides the time source for tests.
func (s *AuthMaintenanceSweeper) SetClock(now func() time.Time) {
	if s != nil && now != nil {
		s.now = now
	}
}

// Run sweeps once at startup and then on every tick until the context is
// cancelled. A failed sweep is logged and retried on the next tick rather than
// terminating the loop: the job is hygiene, and stopping it because one pass hit
// a transient database error would let the tables grow without bound.
func (s *AuthMaintenanceSweeper) Run(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrSessionClosed
	}
	s.sweepAndLog(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// A tick that lands in the same instant as the cancellation is
			// dropped instead of starting work that can only fail, which keeps a
			// shutdown log free of spurious database errors.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.sweepAndLog(ctx)
		}
	}
}

func (s *AuthMaintenanceSweeper) sweepAndLog(ctx context.Context) {
	challenges, devices, err := s.SweepOnce(ctx)
	if err != nil {
		// The error carries no identifier, so logging it cannot leak a subject,
		// a challenge id, or a device token.
		slog.ErrorContext(ctx, "auth_maintenance_sweep_failed", "error", err)
		return
	}
	if challenges > 0 || devices > 0 {
		slog.InfoContext(ctx, "auth_maintenance_sweep", "challenges", challenges, "devices", devices)
	}
}

// SweepOnce performs one collection pass and republishes the gauges. The counts
// are the number of challenge and device rows deleted.
func (s *AuthMaintenanceSweeper) SweepOnce(ctx context.Context) (int64, int64, error) {
	if s == nil || s.db == nil {
		return 0, 0, ErrSessionClosed
	}
	now := s.now()
	// Challenges are short-lived by construction, so their own expiry is the
	// retention. Devices and throttle buckets are kept for the configured window.
	cutoff := now.Add(-s.retention)

	var challenges, devices int64
	err := s.db.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		deleted, err := repos.Challenges.DeleteExpired(ctx, now)
		if err != nil {
			return err
		}
		challenges = deleted
		deleted, err = repos.Devices.DeleteExpired(ctx, cutoff)
		if err != nil {
			return err
		}
		devices = deleted
		// A throttle bucket is a rolling counter, not an audit record. Once it has
		// been idle for the retention window it can no longer affect a decision,
		// and leaving it behind would grow the table with every username an
		// attacker guesses.
		if _, err := repos.LoginAttempts.DeleteExpired(ctx, cutoff); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	s.publishGauges(ctx)
	return challenges, devices, nil
}

// publishGauges samples the three identity populations. A failed sample is
// logged and skipped: a stale gauge is misleading, but a sweeper that stops
// deleting rows because the metrics endpoint is unhappy is worse.
func (s *AuthMaintenanceSweeper) publishGauges(ctx context.Context) {
	if s.gauges == nil {
		return
	}
	var (
		devices    int
		challenges int
		blocked    int
	)
	err := s.db.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		var err error
		if devices, err = repos.Devices.CountActiveAll(ctx); err != nil {
			return err
		}
		if challenges, err = repos.Challenges.CountPending(ctx); err != nil {
			return err
		}
		blocked, err = repos.LoginAttempts.CountBlocked(ctx)
		return err
	})
	if err != nil {
		slog.ErrorContext(ctx, "auth_maintenance_gauges_failed", "error", err)
		return
	}
	s.gauges.SetAuthTrustedDevices(float64(devices))
	s.gauges.SetAuthPendingChallenges(float64(challenges))
	s.gauges.SetAuthBlockedBuckets(float64(blocked))
}
