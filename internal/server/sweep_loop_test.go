package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// sweepRecorder is the shared double for the loop tests: it counts rounds,
// returns a scripted error for the first N of them, and can cancel the context
// from inside a round to describe a shutdown that lands mid-drain.
type sweepRecorder struct {
	mu        sync.Mutex
	calls     int
	failFirst int
	cancelIn  int
	err       error
	onFirst   func()
}

func (r *sweepRecorder) pass(ctx context.Context) error {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if r.onFirst != nil && call == 1 {
		r.onFirst()
	}
	if r.cancelIn > 0 && call == r.cancelIn {
		return context.Canceled
	}
	if call <= r.failFirst {
		return r.err
	}
	return nil
}

func (r *sweepRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// waitForSweeps polls until the loop has completed the wanted number of rounds.
// It returns false on timeout so a test can fail with the counts it observed
// rather than hanging the whole package.
func waitForSweeps(t *testing.T, recorder *sweepRecorder, want int) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if recorder.count() >= want {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func captureSweepLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

// The first pass runs before the first tick, which is what makes an upgrade
// converge immediately instead of waiting out one interval.
func TestRunSweepLoopSweepsBeforeTheFirstTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &sweepRecorder{}
	done := make(chan error, 1)
	go func() { done <- runSweepLoop(ctx, "test", time.Hour, recorder.pass) }()

	if !waitForSweeps(t, recorder, 1) {
		t.Fatal("no sweep ran before the first tick of an hour")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context canceled", err)
	}
}

// One failed round must not retire the cleanup job: this is the whole point of
// the shared loop, and the assertion that a pass still runs after the scripted
// failures is what distinguishes it from the copies it replaces.
func TestRunSweepLoopSurvivesFailedPasses(t *testing.T) {
	dbErr := errors.New("database is not reachable")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &sweepRecorder{failFirst: 3, err: dbErr}
	done := make(chan error, 1)
	go func() { done <- runSweepLoop(ctx, "test", 5*time.Millisecond, recorder.pass) }()

	if !waitForSweeps(t, recorder, 5) {
		cancel()
		<-done
		t.Fatalf("rounds = %d, want at least 5: the loop stopped after a failed pass", recorder.count())
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context canceled", err)
	}
	if recorder.count() <= 3 {
		t.Fatalf("rounds = %d, want the rounds after the failure to have run", recorder.count())
	}
}

// A failure has to be findable in the journal without naming the rows it was
// working on, so the event name carries the sweeper and the payload is only the
// error text.
func TestRunSweepLoopLogsFailuresWithoutNamingRows(t *testing.T) {
	logs := captureSweepLogs(t)
	dbErr := errors.New("database is not reachable")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &sweepRecorder{failFirst: 3, err: dbErr}
	done := make(chan error, 1)
	go func() { done <- runSweepLoop(ctx, "test", 5*time.Millisecond, recorder.pass) }()

	if !waitForSweeps(t, recorder, 3) {
		cancel()
		<-done
		t.Fatalf("rounds = %d, want 3 failed rounds to log", recorder.count())
	}
	cancel()
	<-done

	body := logs.String()
	if !strings.Contains(body, `"msg":"test_sweep_failed"`) {
		t.Fatalf("logs do not contain the sweep failure event: %q", body)
	}
	if !strings.Contains(body, "database is not reachable") {
		t.Fatalf("logs do not carry the error text: %q", body)
	}
	if strings.Contains(strings.ToLower(body), "dsn") || strings.Contains(strings.ToLower(body), "token") {
		t.Fatalf("sweep failure log leaks credential material: %q", body)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		events = append(events, event)
	}
	if len(events) < 3 {
		t.Fatalf("failure events = %d, want one per failed round", len(events))
	}
	for _, event := range events {
		if event["msg"] != "test_sweep_failed" || event["level"] != "ERROR" {
			t.Fatalf("unexpected failure event: %#v", event)
		}
	}
}

// A shutdown that lands inside a round is not a fault, so it must end the loop
// with a cancellation the caller can filter, and must not open the log with a
// false alarm.
func TestRunSweepLoopStopsWhenCancellationLooksLikeAShutdown(t *testing.T) {
	logs := captureSweepLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &sweepRecorder{cancelIn: 1, onFirst: cancel}
	done := make(chan error, 1)
	go func() { done <- runSweepLoop(ctx, "test", 5*time.Millisecond, recorder.pass) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the loop did not treat a cancellation inside a round as a shutdown")
	}
	if body := logs.String(); strings.Contains(body, "_sweep_failed") {
		t.Fatalf("a shutdown mid-round logged a sweep failure: %q", body)
	}
}

// A tick that lands in the same instant as the cancellation is dropped instead
// of starting work that can only fail, which keeps shutdown logs clean.
func TestRunSweepLoopSkipsPassesAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := &sweepRecorder{failFirst: 1, err: errors.New("must not run")}
	if err := runSweepLoop(ctx, "test", time.Millisecond, recorder.pass); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context canceled", err)
	}
	if got := recorder.count(); got != 0 {
		t.Fatalf("rounds = %d, want a cancelled context to start no work", got)
	}
}

// time.NewTicker panics on a non-positive interval, and a panic in this
// goroutine would take the whole Server process with it. The constructors
// normalise intervals today; the guard keeps a future caller from turning a
// cleanup job into a process-wide outage.
func TestRunSweepLoopRejectsNonPositiveInterval(t *testing.T) {
	recorder := &sweepRecorder{}
	for _, interval := range []time.Duration{0, -time.Second} {
		if err := runSweepLoop(context.Background(), "test", interval, recorder.pass); err == nil {
			t.Fatalf("interval %s returned nil, want an error instead of a panic", interval)
		}
	}
	if got := recorder.count(); got != 0 {
		t.Fatalf("rounds = %d, want no pass to run for an invalid interval", got)
	}
}
