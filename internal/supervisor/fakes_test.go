package supervisor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

// fakeClock only moves when a test says so, which makes backoff and health
// windows deterministic.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	due time.Time
	ch  chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{due: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch
}

// Advance moves time forward and fires every timer that became due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var keep []*fakeTimer
	var fire []*fakeTimer
	for _, t := range c.timers {
		if !t.due.After(c.now) {
			fire = append(fire, t)
		} else {
			keep = append(keep, t)
		}
	}
	c.timers = keep
	now := c.now
	c.mu.Unlock()
	for _, t := range fire {
		t.ch <- now
	}
}

// fakeChild is a worker whose lifetime the test scripts.
type fakeChild struct {
	pid      int
	spec     Spec
	exit     chan ExitResult
	mu       sync.Mutex
	stopped  bool
	stopWith time.Duration
}

func (c *fakeChild) PID() int                { return c.pid }
func (c *fakeChild) Wait() <-chan ExitResult { return c.exit }
func (c *fakeChild) Exit(code int)           { c.exit <- ExitResult{Code: code} }
func (c *fakeChild) wasStopped() (bool, time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped, c.stopWith
}

// Stop behaves like a worker that honours SIGTERM.
func (c *fakeChild) Stop(grace time.Duration) error {
	c.mu.Lock()
	c.stopped, c.stopWith = true, grace
	c.mu.Unlock()
	c.exit <- ExitResult{Code: workerproto.ExitOK}
	return nil
}

// fakeLauncher hands out scripted children and records every launch.
type fakeLauncher struct {
	mu       sync.Mutex
	children []*fakeChild
	// onStart lets a test script the child that is being started (for example to
	// publish a ready file) before the supervisor sees it.
	onStart func(n int, c *fakeChild)
	err     error
}

func (l *fakeLauncher) Start(_ context.Context, spec Spec) (Child, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	c := &fakeChild{pid: 1000 + len(l.children), spec: spec, exit: make(chan ExitResult, 1)}
	l.children = append(l.children, c)
	if l.onStart != nil {
		l.onStart(len(l.children), c)
	}
	return c, nil
}

func (l *fakeLauncher) launches() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.children)
}

func (l *fakeLauncher) child(i int) *fakeChild {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.children[i]
}

// harness runs a Supervisor in the background and exposes its exit code.
type harness struct {
	t        *testing.T
	clock    *fakeClock
	launcher *fakeLauncher
	cancel   context.CancelFunc
	code     chan int
	ready    chan workerproto.ReadyInfo
}

func startSupervisor(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	h := &harness{t: t, clock: newFakeClock(), launcher: &fakeLauncher{}, code: make(chan int, 1), ready: make(chan workerproto.ReadyInfo, 16)}
	cfg := Config{
		Role: "client", Installed: "/usr/local/bin/tunnelmesh-client", Args: []string{"--config", "/etc/x.yaml", "run"},
		RunDir: t.TempDir(), Launcher: h.launcher, Clock: h.clock,
		BackoffBase: time.Second, BackoffMax: 60 * time.Second,
		StopGrace: 10 * time.Second, ReadyPoll: 500 * time.Millisecond,
		OnReady: func(info workerproto.ReadyInfo) { h.ready <- info },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	go func() { h.code <- New(cfg).Run(ctx) }()
	return h
}

// advanceUntil steps the fake clock until cond holds, yielding between steps so
// the supervisor goroutine can react. It fails the test instead of hanging.
func (h *harness) advanceUntil(cond func() bool, step time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatal("condition was not reached")
		}
		time.Sleep(2 * time.Millisecond)
		if cond() {
			return
		}
		h.clock.Advance(step)
	}
}

func (h *harness) waitLaunches(n int) {
	h.t.Helper()
	h.advanceUntil(func() bool { return h.launcher.launches() >= n }, time.Second)
}

func (h *harness) exitCode(timeout time.Duration) int {
	h.t.Helper()
	select {
	case code := <-h.code:
		return code
	case <-time.After(timeout):
		h.t.Fatal("supervisor did not exit")
		return -1
	}
}

func (h *harness) writeReady(c *fakeChild, serverVersion string) {
	h.t.Helper()
	if err := workerproto.WriteReady(c.spec.ReadyDir, workerproto.ReadyInfo{PID: c.pid, Version: "v1.0.0", ServerVersion: serverVersion, ReadyAt: h.clock.Now()}); err != nil {
		h.t.Fatal(err)
	}
}

// awaitReady steps the clock until the supervisor reports the worker's READY.
func (h *harness) awaitReady() workerproto.ReadyInfo {
	h.t.Helper()
	var info workerproto.ReadyInfo
	h.advanceUntil(func() bool {
		select {
		case info = <-h.ready:
			return true
		default:
			return false
		}
	}, 500*time.Millisecond)
	return info
}
