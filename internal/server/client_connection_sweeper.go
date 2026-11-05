package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	// DefaultClientConnectionSweepInterval is a minute: a lease lives for 90
	// seconds, so a slower pass would leave expired rows visible as if they were
	// connections, and a faster one costs a delete for rows nobody has read yet.
	DefaultClientConnectionSweepInterval = time.Minute
	// DefaultClientConnectionLeaseRetention is how long a lapsed lease stays
	// readable before it is reaped. The window has to outlast the TTL so the
	// history view can still answer "which connection died when", and it is what
	// bounds the row count per instance: without it, every crash and restart of
	// a long-lived Client added a row that nothing ever removed.
	DefaultClientConnectionLeaseRetention = 24 * time.Hour
)

// ClientConnectionReapGauges is the reaper's observability seam. It is a
// separate interface from Metrics so the background loop depends only on the
// one series it publishes, and a deployment without a registry can leave it nil.
type ClientConnectionReapGauges interface {
	ObserveClientConnectionLeaseSweep(result, errorClass string)
}

// ClientConnectionSweeper reaps client connection leases that expired longer
// ago than the retention window.
//
// Release by id and epoch is the only other way a lease leaves the table, and it
// needs a Client that shut down cooperatively. Killing the process, losing power
// or dropping the network all leave a row whose only remaining fact is a
// timestamp, so retirement has to be driven by time rather than by caller
// cooperation.
//
// Every Server node may run its own pass without a distributed lock: the delete
// is bounded by a timestamp and idempotent, so a concurrent pass on the same
// rows simply reports zero. That is the same trade audit retention and identity
// maintenance make, and it keeps a cleanup job from becoming a new failure mode.
type ClientConnectionSweeper struct {
	leases    storage.ClientConnectionRepository
	interval  time.Duration
	retention time.Duration
	batchSize int
	now       func() time.Time
	gauges    ClientConnectionReapGauges
}

// NewClientConnectionSweeper builds the reaper. Non-positive interval and
// retention fall back to the documented defaults so a partially filled
// configuration can never turn the job into a hot loop or delete leases the
// moment they lapse.
func NewClientConnectionSweeper(leases storage.ClientConnectionRepository, interval, retention time.Duration) *ClientConnectionSweeper {
	if interval <= 0 {
		interval = DefaultClientConnectionSweepInterval
	}
	if retention <= 0 {
		retention = DefaultClientConnectionLeaseRetention
	}
	return &ClientConnectionSweeper{
		leases:    leases,
		interval:  interval,
		retention: retention,
		batchSize: storage.DefaultClientConnectionDeleteBatch,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// SetGauges installs the metrics seam. A nil value disables publishing, which
// keeps the reaper usable in tests and without a registry.
func (s *ClientConnectionSweeper) SetGauges(gauges ClientConnectionReapGauges) {
	if s != nil {
		s.gauges = gauges
	}
}

// SetClock overrides the time source for tests.
func (s *ClientConnectionSweeper) SetClock(now func() time.Time) {
	if s != nil && now != nil {
		s.now = now
	}
}

// Run reaps once before the first tick, then on the interval. Sweeping first is
// what makes an upgrade converge immediately: on a table that was never pruned,
// the backlog exists the moment the new binary starts.
//
// A failed round is logged and retried on the next tick rather than returned. This
// reaper is the only path out of the leases table for a Client that was killed
// instead of stopped, so a loop that ends on the first database error would
// restore the unbounded growth the job exists to prevent - and it would do so
// silently, because the error was only ever readable at Close.
func (s *ClientConnectionSweeper) Run(ctx context.Context) error {
	// A missing repository is a wiring error rather than a retryable round, so it
	// reports immediately instead of entering a loop that can only fail.
	if s == nil || s.leases == nil {
		return errors.New("client connection lease sweeper is unavailable")
	}
	return runSweepLoop(ctx, "client_connection_lease", s.interval, func(ctx context.Context) error {
		_, err := s.SweepOnce(ctx)
		return err
	})
}

// SweepOnce reaps one bounded batch of leases older than the retention window
// and reports how many rows it removed.
//
// The cutoff is computed from the injected clock so a test can describe the
// window exactly, and the result is published even when nothing was reaped: a
// reaper that stopped running has to be distinguishable from one that found
// nothing to do.
func (s *ClientConnectionSweeper) SweepOnce(ctx context.Context) (int64, error) {
	if s == nil || s.leases == nil {
		return 0, errors.New("client connection lease sweeper is unavailable")
	}
	started := time.Now()
	cutoff := s.now().UTC().Add(-s.retention)
	reaped, err := s.leases.DeleteExpired(ctx, cutoff, s.batchSize)
	if s.gauges != nil {
		result := "ok"
		errorClass := ""
		if err != nil {
			result = "error"
			errorClass = observability.NormalizeErrorClass(err)
		}
		s.gauges.ObserveClientConnectionLeaseSweep(result, errorClass)
	}
	// Only the count and the cost are logged: connection ids and instance ids are
	// observable elsewhere and have no place in a routine maintenance line.
	slog.Info("client connection lease sweep",
		"cutoff", cutoff.Format(time.RFC3339Nano),
		"reaped", reaped,
		"elapsed_ms", time.Since(started).Milliseconds(),
		"error", err)
	return reaped, err
}
