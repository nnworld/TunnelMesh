package supervisor

import (
	"context"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

// Candidate is a downloaded, verified and preflighted version ready to be tried.
type Candidate struct {
	Version string
	// Path is the binary to launch.
	Path string
}

// Upgrades is the release side of the supervisor. The supervisor owns the
// process lifecycle and the trial's time windows; everything about releases,
// downloads and persisted state lives behind this interface, so the part that has
// to keep running for months stays free of that code and can be tested without it.
type Upgrades interface {
	// Resume reconciles persisted state after the supervisor itself restarted. A
	// trial that was in progress when the previous supervisor died is treated as
	// failed here.
	Resume() error
	// Binary is the executable to launch now; "" means the installed binary. trial
	// is true while the version has not yet been confirmed.
	Binary() (path string, trial bool)
	// NextCheck is the wait before the next release check, counted from the moment
	// the worker is ready. first is true for the first check of a worker's life. A
	// value of zero or less disables checking.
	NextCheck(first bool) time.Duration
	// Check looks for a newer version and, if one is allowed, downloads and
	// preflights it. A nil candidate with a nil error means nothing to do.
	Check(ctx context.Context, running workerproto.ReadyInfo) (*Candidate, error)
	// Activate makes the candidate the version to launch, on trial.
	Activate(c Candidate) error
	// Commit confirms the trial version.
	Commit() error
	// Rollback abandons the trial version and selects the previous one.
	Rollback(reason string) error
}
