package server

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// sweepPass is the only variable step of a periodic cleanup loop: one round of
// work, which owns its own success logging and metrics.
type sweepPass func(ctx context.Context) error

// errInvalidSweepInterval guards the ticker argument. A panic in one of these
// goroutines would take the whole Server process down, so a cleanup job must
// never be able to produce one; a non-positive interval is a wiring mistake and
// is reported as such instead.
var errInvalidSweepInterval = errors.New("sweep interval must be positive")

// runSweepLoop is the one periodic-cleanup policy in the server: run a round
// immediately, then again on the interval, until the context is cancelled.
//
// A failed round means only "this round did not happen". The next tick retries,
// so a database restart, a connection-pool blip or a single statement timeout
// cannot permanently retire a cleanup job. That distinction is the reason this
// helper exists: the copies it replaces treated the first error as fatal, the
// loop goroutine exited, and nothing reported it until ServerRuntime.Close.
// Failures are logged as "<name>_sweep_failed" so a round that keeps failing is
// findable in the journal while the process stays up, and so the counter metrics
// published by the pass keep receiving samples for the alerts to read.
//
// Only a cancellation returns. A round is never started against a context that
// is already done - it could only fail, and that would pollute shutdown logs -
// and an in-flight cancellation is reported as the cancellation itself, which is
// what ServerRuntime.Close already filters out.
func runSweepLoop(ctx context.Context, name string, interval time.Duration, pass sweepPass) error {
	if interval <= 0 {
		return errInvalidSweepInterval
	}
	if err := runSweepPass(ctx, name, pass); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := runSweepPass(ctx, name, pass); err != nil {
				return err
			}
		}
	}
}

// runSweepPass performs one round and separates the three possible outcomes:
// success, a cancellation caused by shutdown, and a failure worth alerting on.
// Only the second is returned; the third has been logged and must not end the
// loop.
func runSweepPass(ctx context.Context, name string, pass sweepPass) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	err := pass(ctx)
	if err == nil {
		return nil
	}
	// A shutdown that lands mid-round is not a fault: the next node's sweep
	// finishes the job, and a restart must not start with a false alarm.
	if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return ctx.Err()
	}
	// The error text is whatever the repository returned; passes must not put row
	// identifiers, DSNs or credentials into it, which is why none of them wrap SQL
	// arguments today.
	slog.ErrorContext(ctx, name+"_sweep_failed", "error", err)
	return nil
}
