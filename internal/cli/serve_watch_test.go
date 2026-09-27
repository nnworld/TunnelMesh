package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// waitWatcherReport blocks until the watcher reports so a broken watcher fails the
// test loudly instead of passing because nothing ever arrived.
func waitWatcherReport(t *testing.T, errs <-chan error) error {
	t.Helper()
	select {
	case err := <-errs:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("forward serve watcher did not report the listener failure")
		return nil
	}
}

func TestForwardServeWatcherReportsFailureAndStops(t *testing.T) {
	watcher := newForwardServeWatcher(1)
	reported := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := false
	watcher.watch(ctx, "tcp tunnel web", func() { stopped = true }, reported)

	reported <- errors.New("accept tcp 127.0.0.1:8080: too many open files")

	err := waitWatcherReport(t, watcher.Err())
	if err == nil {
		t.Fatal("watcher reported a nil error")
	}
	if !strings.Contains(err.Error(), "tcp tunnel web") {
		t.Fatalf("error must name the tunnel that died, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "too many open files") {
		t.Fatalf("error must keep the cause, got %q", err.Error())
	}
	if stopped != true {
		t.Fatal("watcher must cancel the run so the command can unwind")
	}
	if first := watcher.firstErr(); first == nil {
		t.Fatal("firstErr must be set after a report")
	}
}

func TestForwardServeWatcherIgnoresCleanShutdown(t *testing.T) {
	watcher := newForwardServeWatcher(1)
	reported := make(chan error)
	close(reported)
	stopped := false
	watcher.watch(context.Background(), "udp tunnel dns", func() { stopped = true }, reported)

	time.Sleep(20 * time.Millisecond)
	if err := watcher.firstErr(); err != nil {
		t.Fatalf("a closed channel means a clean stop, got %v", err)
	}
	if stopped {
		t.Fatal("a clean stop must not cancel the run")
	}
}

// TestForwardServeWatcherKeepsTheFirstReportedFailure pins the error that reaches
// the operator: with several tunnels in one process, the earliest death is the
// cause and the rest are collateral.
func TestForwardServeWatcherKeepsTheFirstReportedFailure(t *testing.T) {
	watcher := newForwardServeWatcher(2)
	first := make(chan error, 1)
	first <- errors.New("first listener died")
	watcher.watch(context.Background(), "tcp tunnel a", func() {}, first)
	waitWatcherReport(t, watcher.Err())

	second := make(chan error, 1)
	second <- errors.New("second listener died")
	watcher.watch(context.Background(), "tcp tunnel b", func() {}, second)
	waitWatcherReport(t, watcher.Err())

	if err := watcher.firstErr(); err == nil || !strings.Contains(err.Error(), "tunnel a") {
		t.Fatalf("firstErr must name the first tunnel to die, got %v", err)
	}
}

func TestSuperviseClientRunPrefersListenerFailure(t *testing.T) {
	watcher := newForwardServeWatcher(1)
	reported := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watcher.watch(ctx, "tcp tunnel web", cancel, reported)
	runReturned := make(chan struct{})

	go func() { reported <- errors.New("accept failed: too many open files") }()
	err := superviseClientRun(func() error {
		<-ctx.Done()
		close(runReturned)
		return ctx.Err()
	}, watcher)

	select {
	case <-runReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("the run must be cancelled when a listener dies")
	}
	if err == nil || !strings.Contains(err.Error(), "too many open files") {
		t.Fatalf("superviseClientRun() error = %v, want the listener failure", err)
	}
}

func TestSuperviseClientRunReturnsSessionPoolError(t *testing.T) {
	watcher := newForwardServeWatcher(1)
	err := superviseClientRun(func() error { return errors.New("pool offline") }, watcher)
	if err == nil || err.Error() != "pool offline" {
		t.Fatalf("superviseClientRun() error = %v, want the session pool error", err)
	}
}

func TestForwardServeResultPrefersListenerFailure(t *testing.T) {
	watcher := newForwardServeWatcher(1)
	reported := make(chan error, 1)
	watcher.watch(context.Background(), "socks5 forward 127.0.0.1:1080", func() {}, reported)
	reported <- errors.New("accept failed: protocol not supported")
	waitWatcherReport(t, watcher.Err())

	if err := forwardServeResult(watcher, context.Canceled); err == nil || !strings.Contains(err.Error(), "accept failed") {
		t.Fatalf("forwardServeResult() = %v, want the listener failure over context.Canceled", err)
	}
	if err := forwardServeResult(newForwardServeWatcher(1), nil); err != nil {
		t.Fatalf("forwardServeResult() = %v, want nil when no listener failed", err)
	}
}
