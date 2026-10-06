package cli

import (
	"context"

	"github.com/tunnelmesh/tunnelmesh/internal/client"
)

// forwardServeWatcher adapts the shared client supervisor to the names this package
// has always used.
//
// The implementation lives in internal/client so the tray runtime and the one-shot
// forward commands cannot drift apart on a concurrency rule this subtle: a listener
// that goes deaf has to end the run, and the earliest failure has to win over the
// context.Canceled that cancelling the run produces.
type forwardServeWatcher struct{ inner *client.ForwardServeWatcher }

func newForwardServeWatcher(capacity int) *forwardServeWatcher {
	return &forwardServeWatcher{inner: client.NewForwardServeWatcher(capacity)}
}

func (w *forwardServeWatcher) watch(ctx context.Context, name string, stop func(), errs <-chan error) {
	w.inner.Watch(ctx, name, stop, errs)
}

func (w *forwardServeWatcher) Err() <-chan error { return w.inner.Err() }

func (w *forwardServeWatcher) firstErr() error { return w.inner.FirstErr() }

func superviseClientRun(run func() error, watcher *forwardServeWatcher) error {
	return client.SuperviseRun(run, watcher.inner)
}

func forwardServeResult(watcher *forwardServeWatcher, runErr error) error {
	return client.ForwardServeResult(watcher.inner, runErr)
}
