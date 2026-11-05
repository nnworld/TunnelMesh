package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// recordingClientConnectionRepo is a single-method double: the reaper must only
// ever reach for DeleteExpired, and embedding the interface makes any new call
// in the sweeper panic in tests instead of quietly passing.
type recordingClientConnectionRepo struct {
	storage.ClientConnectionRepository
	cutoffs []time.Time
	limits  []int
	reaped  int64
	err     error
}

func (r *recordingClientConnectionRepo) DeleteExpired(_ context.Context, at time.Time, limit int) (int64, error) {
	r.cutoffs = append(r.cutoffs, at)
	r.limits = append(r.limits, limit)
	return r.reaped, r.err
}

type recordedSweep struct {
	result     string
	errorClass string
}

type recordingReapGauges struct {
	calls []recordedSweep
}

func (r *recordingReapGauges) ObserveClientConnectionLeaseSweep(result, errorClass string) {
	r.calls = append(r.calls, recordedSweep{result: result, errorClass: errorClass})
}

func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// The retention window, not the expiry instant, decides what goes: a lease that
// lapsed minutes ago is still the evidence an operator needs to explain a
// restart, and the drawer is supposed to be able to show it.
func TestClientConnectionSweeperReapsOnlyPastTheRetentionWindow(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	repo := &recordingClientConnectionRepo{reaped: 7}
	sweeper := NewClientConnectionSweeper(repo, time.Minute, 24*time.Hour)
	sweeper.SetClock(fixedClock(now))

	reaped, err := sweeper.SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reaped != 7 {
		t.Fatalf("reaped = %d, want 7", reaped)
	}
	if len(repo.cutoffs) != 1 {
		t.Fatalf("cutoffs = %d, want one pass", len(repo.cutoffs))
	}
	if want := now.Add(-24 * time.Hour).UTC(); !repo.cutoffs[0].Equal(want) {
		t.Fatalf("cutoff = %s, want %s", repo.cutoffs[0], want)
	}
	if repo.limits[0] != storage.DefaultClientConnectionDeleteBatch {
		t.Fatalf("batch limit = %d, want the bounded default %d", repo.limits[0], storage.DefaultClientConnectionDeleteBatch)
	}
}

// A partially filled configuration must not turn the job into a hot loop, and
// it must not shorten the evidence window to zero: both defaults are what the
// operator-visible behaviour is documented against.
func TestClientConnectionSweeperFallsBackToDocumentedDefaults(t *testing.T) {
	sweeper := NewClientConnectionSweeper(&recordingClientConnectionRepo{}, 0, 0)
	if sweeper.interval != DefaultClientConnectionSweepInterval {
		t.Fatalf("interval = %s, want %s", sweeper.interval, DefaultClientConnectionSweepInterval)
	}
	if sweeper.retention != DefaultClientConnectionLeaseRetention {
		t.Fatalf("retention = %s, want %s", sweeper.retention, DefaultClientConnectionLeaseRetention)
	}
	if DefaultClientConnectionLeaseRetention < DefaultClientConnectionLeaseTTL {
		t.Fatal("the retention window must outlast the lease TTL, otherwise the history view is empty by construction")
	}
}

// The repository failure has to reach both the caller and the metrics: a
// reaper that silently stops reaping is exactly the defect this job exists to
// fix, and it must not look like a clean run.
func TestClientConnectionSweeperReportsRepositoryFailure(t *testing.T) {
	repo := &recordingClientConnectionRepo{err: errors.New("deadlock")}
	gauges := &recordingReapGauges{}
	sweeper := NewClientConnectionSweeper(repo, time.Minute, time.Hour)
	sweeper.SetGauges(gauges)
	sweeper.SetClock(fixedClock(time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)))

	if _, err := sweeper.SweepOnce(context.Background()); err == nil {
		t.Fatal("SweepOnce error = nil, want the repository failure to surface")
	}
	if len(gauges.calls) != 1 || gauges.calls[0].result != "error" {
		t.Fatalf("gauges = %#v, want one error sample", gauges.calls)
	}
	if gauges.calls[0].errorClass == "" {
		t.Fatal("error class is empty, so the failure cannot be grouped in the dashboard")
	}

	clean := &recordingClientConnectionRepo{reaped: 3}
	sweeper.leases = clean
	if _, err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gauges.calls[1].result != "ok" {
		t.Fatalf("success sample = %#v, want result ok", gauges.calls[1])
	}
}

// Run reaps before the first tick so an upgrade that leaves a hundred thousand
// corpses behind starts converging immediately, and it returns on cancellation
// the same way every other runtime sweeper does.
func TestClientConnectionSweeperRunSweepsBeforeTheFirstTick(t *testing.T) {
	repo := &recordingClientConnectionRepo{}
	sweeper := NewClientConnectionSweeper(repo, 10*time.Millisecond, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for len(repo.cutoffs) < 2 {
		select {
		case <-deadline:
			t.Fatalf("passes = %d, want at least two", len(repo.cutoffs))
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context canceled", err)
	}
}

// A deployment without a registry, and a test that never installs one, must
// still be able to run the job.
func TestClientConnectionSweeperWithoutGaugesOrRepository(t *testing.T) {
	sweeper := NewClientConnectionSweeper(nil, time.Minute, time.Hour)
	if err := sweeper.Run(context.Background()); err == nil {
		t.Fatal("Run with no repository returned nil")
	}
	var nilSweeper *ClientConnectionSweeper
	if _, err := nilSweeper.SweepOnce(context.Background()); err == nil {
		t.Fatal("nil receiver must not report a completed sweep")
	}
	nilSweeper.SetClock(fixedClock(time.Now()))
	nilSweeper.SetGauges(&recordingReapGauges{})
}
