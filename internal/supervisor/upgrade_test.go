package supervisor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

// fakeUpgrades scripts the release side so the supervisor's trial logic can be
// tested on its own.
type fakeUpgrades struct {
	mu        sync.Mutex
	current   string // "" means the installed binary
	previous  string
	trial     bool
	candidate *Candidate
	checkErr  error
	interval  time.Duration
	calls     []string
}

func newFakeUpgrades() *fakeUpgrades { return &fakeUpgrades{interval: 60 * time.Second} }

func (f *fakeUpgrades) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeUpgrades) Resume() error { f.record("resume"); return nil }
func (f *fakeUpgrades) Binary() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current, f.trial
}
func (f *fakeUpgrades) NextCheck(first bool) time.Duration { return f.interval }
func (f *fakeUpgrades) Check(_ context.Context, _ workerproto.ReadyInfo) (*Candidate, error) {
	f.record("check")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checkErr != nil {
		return nil, f.checkErr
	}
	c := f.candidate
	f.candidate = nil
	return c, nil
}
func (f *fakeUpgrades) Activate(c Candidate) error {
	f.record("activate:" + c.Version)
	f.mu.Lock()
	f.previous, f.current, f.trial = f.current, c.Path, true
	f.mu.Unlock()
	return nil
}
func (f *fakeUpgrades) Commit() error {
	f.record("commit")
	f.mu.Lock()
	f.trial = false
	f.mu.Unlock()
	return nil
}
func (f *fakeUpgrades) Rollback(reason string) error {
	f.record("rollback:" + reason)
	f.mu.Lock()
	f.current, f.trial = f.previous, false
	f.mu.Unlock()
	return nil
}
func (f *fakeUpgrades) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
func (f *fakeUpgrades) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func startWithUpgrades(t *testing.T, up *fakeUpgrades) *harness {
	return startSupervisor(t, func(c *Config) {
		c.Upgrades = up
		c.HealthTimeout = 30 * time.Second
		c.StableWindow = 20 * time.Second
	})
}

// readyWorker publishes READY for launch i and waits until the supervisor saw it.
func (h *harness) readyWorker(i int) {
	h.t.Helper()
	h.writeReady(h.launcher.child(i), "v1.4.0")
	h.awaitReady()
}

func TestStartupReconcilesBeforeTheFirstLaunch(t *testing.T) {
	up := newFakeUpgrades()
	up.current = "/state/versions/v1.3.0/tunnelmesh-client"
	h := startWithUpgrades(t, up)
	h.waitLaunches(1)
	if up.count("resume") != 1 {
		t.Fatalf("Resume called %d times, want once before the first launch", up.count("resume"))
	}
	if got := h.launcher.child(0).spec.Bin; got != up.current {
		t.Fatalf("Bin = %q, want the version the upgrade state selects", got)
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestACheckRunsOnlyOnceTheWorkerIsReady(t *testing.T) {
	up := newFakeUpgrades()
	h := startWithUpgrades(t, up)
	h.waitLaunches(1)
	// Not ready yet: however long it takes, nothing is checked.
	for i := 0; i < 5; i++ {
		h.clock.Advance(time.Minute)
		time.Sleep(2 * time.Millisecond)
	}
	if up.has("check") {
		t.Fatal("a release check ran before the worker reported READY")
	}
	h.readyWorker(0)
	h.advanceUntil(func() bool { return up.has("check") }, 10*time.Second)
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestACheckThatFindsNothingKeepsTheWorkerRunning(t *testing.T) {
	up := newFakeUpgrades()
	h := startWithUpgrades(t, up)
	h.waitLaunches(1)
	h.readyWorker(0)
	h.advanceUntil(func() bool { return up.count("check") >= 2 }, 10*time.Second)
	if h.launcher.launches() != 1 {
		t.Fatalf("launches = %d, want the worker left alone when there is nothing to upgrade to", h.launcher.launches())
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestACheckErrorIsRetriedLaterAndNeverTouchesTheWorker(t *testing.T) {
	up := newFakeUpgrades()
	up.checkErr = errors.New("source unreachable")
	h := startWithUpgrades(t, up)
	h.waitLaunches(1)
	h.readyWorker(0)
	h.advanceUntil(func() bool { return up.count("check") >= 2 }, 10*time.Second)
	if h.launcher.launches() != 1 || stoppedAny(h) {
		t.Fatal("a failed release check disturbed the running worker")
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func stoppedAny(h *harness) bool {
	for i := 0; i < h.launcher.launches(); i++ {
		if stopped, _ := h.launcher.child(i).wasStopped(); stopped {
			return true
		}
	}
	return false
}

// upgradeToCandidate drives a worker to READY and lets the check find a candidate.
func upgradeToCandidate(t *testing.T, up *fakeUpgrades) *harness {
	t.Helper()
	up.candidate = &Candidate{Version: "v1.4.0", Path: "/state/versions/v1.4.0/tunnelmesh-client"}
	h := startWithUpgrades(t, up)
	h.waitLaunches(1)
	h.readyWorker(0)
	h.waitLaunches(2)
	return h
}

func TestAnUpgradeStopsTheWorkerGracefullyAndRelaunchesOnTrial(t *testing.T) {
	up := newFakeUpgrades()
	h := upgradeToCandidate(t, up)
	if stopped, _ := h.launcher.child(0).wasStopped(); !stopped {
		t.Fatal("the old worker was not stopped before the upgrade")
	}
	if !up.has("activate:v1.4.0") {
		t.Fatalf("calls = %v, want the upgrade activated", up.calls)
	}
	if got := h.launcher.child(1).spec.Bin; got != "/state/versions/v1.4.0/tunnelmesh-client" {
		t.Fatalf("second launch Bin = %q, want the candidate", got)
	}
	// Stopping for an upgrade is not a crash, so no backoff separates the launches.
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestATrialIsCommittedOnceTheWorkerStaysUpThroughTheStableWindow(t *testing.T) {
	up := newFakeUpgrades()
	h := upgradeToCandidate(t, up)
	h.readyWorker(1)
	if up.has("commit") {
		t.Fatal("committed at READY, before the stable window")
	}
	h.advanceUntil(func() bool { return up.has("commit") }, time.Second)
	if up.has("rollback") {
		t.Fatalf("calls = %v, want no rollback for a healthy upgrade", up.calls)
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestATrialWorkerThatNeverBecomesReadyIsRolledBack(t *testing.T) {
	up := newFakeUpgrades()
	h := upgradeToCandidate(t, up)
	h.waitLaunches(3) // the rollback relaunches the previous version
	if !up.has("rollback:") {
		t.Fatalf("calls = %v, want a rollback", up.calls)
	}
	if stopped, _ := h.launcher.child(1).wasStopped(); !stopped {
		t.Fatal("the unhealthy candidate was left running")
	}
	if got := h.launcher.child(2).spec.Bin; got != h.launcher.child(0).spec.Bin {
		t.Fatalf("after rollback Bin = %q, want the previous version %q", got, h.launcher.child(0).spec.Bin)
	}
	if up.has("commit") {
		t.Fatal("an unhealthy candidate was committed")
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestATrialWorkerThatCrashesInsideTheStableWindowIsRolledBack(t *testing.T) {
	up := newFakeUpgrades()
	h := upgradeToCandidate(t, up)
	h.readyWorker(1)
	h.launcher.child(1).Exit(1)
	h.waitLaunches(3)
	if !up.has("rollback:") || up.has("commit") {
		t.Fatalf("calls = %v, want a rollback and no commit", up.calls)
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

// The new version rejecting the existing configuration is the typical upgrade
// failure; it must roll back instead of ending the supervisor with 78.
func TestATrialWorkerThatRejectsTheConfigIsRolledBack(t *testing.T) {
	up := newFakeUpgrades()
	h := upgradeToCandidate(t, up)
	h.launcher.child(1).Exit(workerproto.ExitConfig)
	h.waitLaunches(3)
	select {
	case code := <-h.code:
		t.Fatalf("the supervisor exited with %d instead of rolling back", code)
	default:
	}
	if !up.has("rollback:") {
		t.Fatalf("calls = %v, want a rollback", up.calls)
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

// A held client.lock is normal while the old worker finishes shutting down.
func TestALockConflictDuringATrialIsRetriedNotRolledBack(t *testing.T) {
	up := newFakeUpgrades()
	h := upgradeToCandidate(t, up)
	h.launcher.child(1).Exit(workerproto.ExitLockHeld)
	h.waitLaunches(3)
	if up.has("rollback:") {
		t.Fatalf("calls = %v, want no rollback for a contended lock", up.calls)
	}
	if got := h.launcher.child(2).spec.Bin; got != "/state/versions/v1.4.0/tunnelmesh-client" {
		t.Fatalf("retry Bin = %q, want the candidate again", got)
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestNoReleaseCheckRunsWhileATrialIsPending(t *testing.T) {
	up := newFakeUpgrades()
	h := upgradeToCandidate(t, up)
	checks := up.count("check")
	// Time passes with the trial worker not yet stable.
	for i := 0; i < 5; i++ {
		h.clock.Advance(5 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	if up.count("check") != checks {
		t.Fatal("a release check ran during a trial")
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestAWorkerThatEndsItselfCleanlyDuringATrialIsRolledBack(t *testing.T) {
	up := newFakeUpgrades()
	h := upgradeToCandidate(t, up)
	h.launcher.child(1).Exit(0)
	h.waitLaunches(3)
	if !up.has("rollback:") {
		t.Fatalf("calls = %v, want a rollback", up.calls)
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestWithoutUpgradesTheSupervisorUsesTheInstalledBinary(t *testing.T) {
	h := startSupervisor(t, nil)
	h.waitLaunches(1)
	if got := h.launcher.child(0).spec.Bin; got != "/usr/local/bin/tunnelmesh-client" {
		t.Fatalf("Bin = %q, want the installed binary", got)
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}
