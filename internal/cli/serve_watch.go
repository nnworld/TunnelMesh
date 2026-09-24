package cli

import (
	"context"
	"fmt"
	"sync"
)

// forwardServeWatcher turns "a local listener stopped serving" into an error the
// command layer can return.
//
// Every client forwarder runs its accept or read loop in a goroutine started by
// Start, so the loop can only fail *after* Start has already reported success.
// Without a watcher the process stays up, keeps its WebSocket, and keeps
// advertising a tunnel whose port is bound but deaf - the single most expensive
// client failure to diagnose from outside. The watcher therefore both records the
// first failure and cancels the run, so a supervisor restarts the client.
type forwardServeWatcher struct {
	mu    sync.Mutex
	first error
	errs  chan error
}

func newForwardServeWatcher(capacity int) *forwardServeWatcher {
	if capacity < 1 {
		capacity = 1
	}
	return &forwardServeWatcher{errs: make(chan error, capacity)}
}

// watch observes one listener's failure channel for the lifetime of ctx.
//
// name is the human-readable tunnel description, which is what makes the exit
// message actionable when several tunnels share a process. stop is called after
// the failure is recorded, never before, so a caller that wakes on stop can read
// firstErr without racing it.
func (w *forwardServeWatcher) watch(ctx context.Context, name string, stop func(), errs <-chan error) {
	if w == nil || errs == nil {
		return
	}
	go func() {
		var err error
		select {
		case <-ctx.Done():
			return
		case reported, open := <-errs:
			if !open || reported == nil {
				return
			}
			err = fmt.Errorf("%s stopped: %w", name, reported)
		}
		w.mu.Lock()
		if w.first == nil {
			w.first = err
		}
		w.mu.Unlock()
		if stop != nil {
			stop()
		}
		select {
		case w.errs <- err:
		default:
		}
	}()
}

// Err returns the stream of reported listener failures.
func (w *forwardServeWatcher) Err() <-chan error {
	if w == nil {
		return nil
	}
	return w.errs
}

// firstErr returns the earliest recorded failure, or nil.
func (w *forwardServeWatcher) firstErr() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.first
}

// superviseClientRun waits for the session pool while a listener failure takes
// precedence over it.
//
// A dead listener cancels the run, which usually makes the pool return
// context.Canceled. Reporting that instead would hide the real cause, so the
// recorded failure wins.
func superviseClientRun(run func() error, watcher *forwardServeWatcher) error {
	results := make(chan error, 1)
	go func() { results <- run() }()
	var err error
	select {
	case err = <-results:
	case <-watcher.Err():
		err = nil
	}
	return forwardServeResult(watcher, err)
}

// forwardServeResult prefers a recorded listener failure over the run error.
//
// When a listener dies the run context is cancelled, so the WebSocket loop comes
// back with context.Canceled. Reporting that symptom would send the operator to the
// network instead of to the port that could not keep serving.
func forwardServeResult(watcher *forwardServeWatcher, runErr error) error {
	if failure := watcher.firstErr(); failure != nil {
		return failure
	}
	return runErr
}
