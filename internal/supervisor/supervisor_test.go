package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

func TestLaunchesWorkerWithSuperviseContract(t *testing.T) {
	h := startSupervisor(t, nil)
	h.waitLaunches(1)
	spec := h.launcher.child(0).spec
	if spec.Bin != "/usr/local/bin/tunnelmesh-client" {
		t.Fatalf("Bin = %q, want the installed binary when no upgrade is active", spec.Bin)
	}
	if strings.Join(spec.Args, " ") != "--config /etc/x.yaml run" {
		t.Fatalf("Args = %v, want the supervisor's own arguments passed through", spec.Args)
	}
	if spec.ReadyDir == "" {
		t.Fatal("the worker needs a ready directory")
	}
}

func TestRestartsCrashedWorkerAfterBackoff(t *testing.T) {
	h := startSupervisor(t, nil)
	h.waitLaunches(1)
	h.launcher.child(0).Exit(1)

	// The restart must wait: restarting a crashing worker in a tight loop would
	// hammer the Server and the CPU.
	time.Sleep(30 * time.Millisecond)
	if got := h.launcher.launches(); got != 1 {
		t.Fatalf("launches = %d right after the crash, want 1 (backoff not honoured)", got)
	}
	h.waitLaunches(2)

	h.cancel()
	if code := h.exitCode(2 * time.Second); code != workerproto.ExitOK {
		t.Fatalf("exit code = %d, want 0 after a requested stop", code)
	}
}

func TestBackoffGrowsAcrossConsecutiveCrashes(t *testing.T) {
	h := startSupervisor(t, func(c *Config) { c.BackoffBase, c.BackoffMax = 4*time.Second, 64*time.Second })
	h.waitLaunches(1)
	h.launcher.child(0).Exit(1)
	start := h.clock.Now()
	h.waitLaunches(2)
	first := h.clock.Now().Sub(start)
	h.launcher.child(1).Exit(1)
	start = h.clock.Now()
	h.waitLaunches(3)
	second := h.clock.Now().Sub(start)
	// Equal jitter keeps each wait within [d/2, d], so the second wait (d = 8s) can
	// never be shorter than the first wait's upper bound (4s).
	if first < 2*time.Second || first > 6*time.Second || second < 4*time.Second {
		t.Fatalf("waits = %v then %v, want roughly 4s then 8s", first, second)
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestConfigErrorEndsTheSupervisorInsteadOfLooping(t *testing.T) {
	h := startSupervisor(t, nil)
	h.waitLaunches(1)
	h.launcher.child(0).Exit(workerproto.ExitConfig)
	if code := h.exitCode(2 * time.Second); code != workerproto.ExitConfig {
		t.Fatalf("exit code = %d, want %d", code, workerproto.ExitConfig)
	}
	if got := h.launcher.launches(); got != 1 {
		t.Fatalf("launches = %d, want 1: a bad config does not heal by restarting", got)
	}
}

func TestFatalAuthEndsTheSupervisor(t *testing.T) {
	h := startSupervisor(t, nil)
	h.waitLaunches(1)
	h.launcher.child(0).Exit(workerproto.ExitAuthFatal)
	if code := h.exitCode(2 * time.Second); code != workerproto.ExitAuthFatal {
		t.Fatalf("exit code = %d, want %d", code, workerproto.ExitAuthFatal)
	}
}

// A held client.lock is normal while the previous worker is still shutting down,
// so it is retried quickly and is not counted as a crash.
func TestLockHeldIsRetriedWithoutCountingAsACrash(t *testing.T) {
	h := startSupervisor(t, func(c *Config) { c.GiveUpAfter = 2 })
	for i := 0; i < 4; i++ {
		h.waitLaunches(i + 1)
		h.launcher.child(i).Exit(workerproto.ExitLockHeld)
	}
	h.waitLaunches(5)
	select {
	case code := <-h.code:
		t.Fatalf("supervisor exited with %d while only the lock was contended", code)
	default:
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestLockHeldForeverEventuallyGivesUp(t *testing.T) {
	h := startSupervisor(t, func(c *Config) { c.LockRetries = 3 })
	for i := 0; i < 4; i++ {
		h.waitLaunches(i + 1)
		h.launcher.child(i).Exit(workerproto.ExitLockHeld)
	}
	if code := h.exitCode(2 * time.Second); code != workerproto.ExitLockHeld {
		t.Fatalf("exit code = %d, want %d", code, workerproto.ExitLockHeld)
	}
}

// Leaving recovery to the outer service manager is the point of exit 70: the
// supervisor must not loop forever on a worker that never gets going.
func TestGivesUpAfterConsecutiveCrashesWithoutEverBecomingReady(t *testing.T) {
	h := startSupervisor(t, func(c *Config) { c.GiveUpAfter = 3 })
	for i := 0; i < 3; i++ {
		h.waitLaunches(i + 1)
		h.launcher.child(i).Exit(1)
	}
	if code := h.exitCode(5 * time.Second); code != workerproto.ExitSupervisorGaveUp {
		t.Fatalf("exit code = %d, want %d", code, workerproto.ExitSupervisorGaveUp)
	}
}

// A worker that gets as far as READY is working, even if it crashes later, so
// reaching READY resets the give-up counter.
func TestReadinessResetsTheGiveUpCounter(t *testing.T) {
	h := startSupervisor(t, func(c *Config) { c.GiveUpAfter = 2 })
	for i := 0; i < 6; i++ {
		h.waitLaunches(i + 1)
		child := h.launcher.child(i)
		h.writeReady(child, "v1.0.0")
		h.awaitReady()
		child.Exit(1)
	}
	h.waitLaunches(7)
	select {
	case code := <-h.code:
		t.Fatalf("supervisor gave up (%d) although every worker reached READY", code)
	default:
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestReportsReadinessAndServerVersion(t *testing.T) {
	h := startSupervisor(t, nil)
	h.waitLaunches(1)
	h.writeReady(h.launcher.child(0), "v1.4.0")
	info := h.awaitReady()
	if info.ServerVersion != "v1.4.0" || info.PID != h.launcher.child(0).pid {
		t.Fatalf("ReadyInfo = %+v, want the worker's published document", info)
	}
	h.cancel()
	h.exitCode(2 * time.Second)
}

func TestStopSignalIsForwardedToTheWorkerAndNotRestarted(t *testing.T) {
	h := startSupervisor(t, func(c *Config) { c.StopGrace = 7 * time.Second })
	h.waitLaunches(1)
	h.cancel()
	if code := h.exitCode(2 * time.Second); code != workerproto.ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	stopped, grace := h.launcher.child(0).wasStopped()
	if !stopped || grace != 7*time.Second {
		t.Fatalf("worker stopped=%v grace=%v, want it stopped with the configured grace", stopped, grace)
	}
	if got := h.launcher.launches(); got != 1 {
		t.Fatalf("launches = %d, want 1: no restart after a requested stop", got)
	}
}

// A worker that returns 0 on its own chose to end; mirroring the old single
// process, the supervisor ends too instead of resurrecting it.
func TestCleanExitEndsTheSupervisor(t *testing.T) {
	h := startSupervisor(t, nil)
	h.waitLaunches(1)
	h.launcher.child(0).Exit(0)
	if code := h.exitCode(2 * time.Second); code != workerproto.ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := h.launcher.launches(); got != 1 {
		t.Fatalf("launches = %d, want 1", got)
	}
}

// A read-only or missing state directory must never stop the service from
// starting: the supervisor falls back to a temporary ready directory.
func TestUnwritableRunDirFallsBackToATemporaryReadyDirectory(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A path below a regular file can never be created, on any platform.
	h := startSupervisor(t, func(c *Config) { c.RunDir = filepath.Join(blocker, "run") })
	h.waitLaunches(1)
	child := h.launcher.child(0)
	if child.spec.ReadyDir == "" {
		t.Fatal("no ready directory was provided; readiness could never be observed")
	}
	if strings.HasPrefix(child.spec.ReadyDir, blocker) {
		t.Fatalf("ReadyDir %q is below the unwritable path", child.spec.ReadyDir)
	}
	h.writeReady(child, "v1.2.0")
	if info := h.awaitReady(); info.ServerVersion != "v1.2.0" {
		t.Fatalf("ReadyInfo = %+v, want readiness observed through the fallback directory", info)
	}
	h.cancel()
	h.exitCode(2 * time.Second)
	if _, err := os.Stat(child.spec.ReadyDir); err == nil {
		t.Fatal("the temporary ready directory was not cleaned up")
	}
}
