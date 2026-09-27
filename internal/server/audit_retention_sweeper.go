package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	// DefaultAuditRetentionSweepInterval is hourly: the rows it removes are measured in
	// days, so a more frequent pass costs scans and buys nothing.
	DefaultAuditRetentionSweepInterval = time.Hour
	// DefaultAuditRetentionBatchRows bounds one DELETE transaction. A deployment that
	// turns retention on after years of permanent audit writes must not discover that
	// the first sweep locks the table the management API still writes to.
	DefaultAuditRetentionBatchRows = 1000
)

// AuditRetentionSweeper removes audit rows older than the configured retention
// window.
//
// Retention is off unless an operator asks for it, and the sweeper is a plain
// idempotent DELETE bounded by a timestamp, so every Server node may run its own
// pass: a duplicate deletes nothing. That is the same trade the identity maintenance
// sweeper makes - no distributed lock, no new failure mode, eventual removal.
//
// The cutoff is computed once per pass, never per batch, so a long drain cannot
// migrate rows out of the window while it runs.
type AuditRetentionSweeper struct {
	audits    storage.AuditRepository
	retention time.Duration
	interval  time.Duration
	batchSize int
	now       func() time.Time
	onPurge   func(int)
}

// NewAuditRetentionSweeper returns nil when retention is disabled, which is the
// documented meaning of server.audit.retention_days <= 0. Callers must treat a nil
// sweeper as "never started", and the nil receiver stays safe for the same reason.
func NewAuditRetentionSweeper(audits storage.AuditRepository, retentionDays int, interval time.Duration) *AuditRetentionSweeper {
	if audits == nil || retentionDays <= 0 {
		return nil
	}
	if interval <= 0 {
		interval = DefaultAuditRetentionSweepInterval
	}
	return &AuditRetentionSweeper{
		audits:    audits,
		retention: time.Duration(retentionDays) * 24 * time.Hour,
		interval:  interval,
		batchSize: DefaultAuditRetentionBatchRows,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// SetClock overrides the time source for tests.
func (s *AuditRetentionSweeper) SetClock(now func() time.Time) {
	if s != nil && now != nil {
		s.now = now
	}
}

// SetBatchSize overrides the rows removed per transaction, for tests and for a
// deployment whose metadata table cannot afford the default batch.
func (s *AuditRetentionSweeper) SetBatchSize(rows int) {
	if s != nil && rows > 0 {
		s.batchSize = rows
	}
}

// SetPurgeObserver installs a per-batch hook. It exists so a test can prove the
// batching is real without counting rows after every pass.
func (s *AuditRetentionSweeper) SetPurgeObserver(onPurge func(deleted int)) {
	if s != nil {
		s.onPurge = onPurge
	}
}

// SweepOnce drains every expired row and returns the total it removed.
func (s *AuditRetentionSweeper) SweepOnce(ctx context.Context) (int, error) {
	if s == nil || s.audits == nil {
		return 0, ErrSessionClosed
	}
	cutoff := s.now().Add(-s.retention)
	total := 0
	for {
		deleted, err := s.audits.PurgeOlderThan(ctx, cutoff, s.batchSize)
		if s.onPurge != nil {
			s.onPurge(deleted)
		}
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted < s.batchSize {
			return total, nil
		}
		// A full batch only means more work: stop promptly on cancellation so a
		// shutdown does not wait for a drain it cannot finish anyway.
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// Run sweeps once at startup and then on every tick until ctx is cancelled.
func (s *AuditRetentionSweeper) Run(ctx context.Context) error {
	if s == nil || s.audits == nil {
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
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.sweepAndLog(ctx)
		}
	}
}

func (s *AuditRetentionSweeper) sweepAndLog(ctx context.Context) {
	started := time.Now()
	deleted, err := s.SweepOnce(ctx)
	switch {
	case errors.Is(err, context.Canceled):
		// A shutdown that lands mid-drain is not a fault: the next node's sweeper
		// finishes the job, and a restart must not start with a false alarm.
		return
	case err != nil:
		// The error carries no row identifiers, so a failure log cannot leak the
		// subject or resource of an audit record.
		slog.ErrorContext(ctx, "audit_retention_sweep_failed", "error", err)
		return
	}
	if deleted > 0 {
		slog.InfoContext(ctx, "audit_retention_swept",
			"deleted_rows", deleted, "retention_days", int64(s.retention/(24*time.Hour)),
			"duration_ms", time.Since(started).Milliseconds())
	}
}
