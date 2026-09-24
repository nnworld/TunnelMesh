package client

import "sync"

// serveSignal is the one-slot report for a local ingress loop.
//
// Every forwarder in this package runs its accept or read loop in a goroutine
// started by Start, which means the loop can only fail *after* Start has already
// returned success. Without a report channel the process would keep running - and
// keep reporting a healthy tunnel - with a bound but deaf listener, which is the
// failure that costs the most time to diagnose. publish never blocks, so a caller
// that is not watching cannot wedge the loop.
type serveSignal struct {
	mu     sync.Mutex
	err    chan error
	closed bool
	done   bool
}

func newServeSignal() *serveSignal {
	return &serveSignal{err: make(chan error, 1)}
}

// publish records a failure. A nil error means a clean stop and is dropped: the
// channel's only contract is "something went wrong".
func (s *serveSignal) publish(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Shutdown and a failing Accept race; once the caller has closed the
	// forwarder, the pending error is expected noise, not a reportable failure.
	if s.closed {
		return
	}
	s.done = true
	select {
	case s.err <- err:
	default:
	}
}

// close releases watchers of a deliberately closed listener.
//
// Without it a goroutine waiting on Err() stays parked for the lifetime of the
// run context, so each reconnect of a single-forward command would leak one.
func (s *serveSignal) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.err)
}

// reported reports whether a failure has been recorded.
func (s *serveSignal) reported() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// Err returns the read side of the report. It is closed once the forwarder is
// deliberately closed, so a watcher can tell a clean stop from a failure.
func (s *serveSignal) Err() <-chan error {
	if s == nil {
		return nil
	}
	return s.err
}
