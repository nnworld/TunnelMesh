// Package supervisor runs a worker process, restarts it when it crashes, and
// gives up in a way an outer service manager (systemd, launchd, WinSW, a
// container runtime) can act on.
//
// The supervisor is deliberately small and does not know about releases. The
// upgrade logic composes with it through the Config hooks, which keeps the part
// that must keep running for months free of download and archive code.
package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/reconnect"
	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

// ExitResult is how a worker process ended.
type ExitResult struct {
	Code int
	Err  error
}

// Spec describes one worker launch.
type Spec struct {
	Bin      string
	Args     []string
	Env      []string
	ReadyDir string
}

// Child is a running worker.
type Child interface {
	PID() int
	// Wait delivers exactly one result when the process has ended.
	Wait() <-chan ExitResult
	// Stop asks the worker to shut down, waits up to grace, then kills it. It
	// returns once the process is gone.
	Stop(grace time.Duration) error
}

// Launcher starts workers. It is an interface so tests can script a worker
// without spawning a process.
type Launcher interface {
	Start(ctx context.Context, spec Spec) (Child, error)
}

// Clock is the time source. Backoff and readiness polling go through it so tests
// do not sleep.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Config configures a Supervisor.
type Config struct {
	Role string
	// Installed is the binary to run when no upgraded version is active.
	Installed string
	// Args and Env are passed to the worker unchanged; the supervisor only adds the
	// worker contract variables.
	Args []string
	Env  []string
	// RunDir is the parent of the per-launch ready directories.
	RunDir   string
	Launcher Launcher
	Clock    Clock

	BackoffBase, BackoffMax time.Duration
	StopGrace               time.Duration
	// ReadyPoll is how often the ready file is checked.
	ReadyPoll time.Duration
	// GiveUpAfter is the number of consecutive launches that crash without ever
	// reaching READY before the supervisor exits 70.
	GiveUpAfter int
	// LockRetries caps consecutive "lock held" exits; LockRetryDelay is the wait
	// between them.
	LockRetries    int
	LockRetryDelay time.Duration

	// OnReady is called when a worker first reports READY, and again if the Server
	// version it reports changes (after a reconnect to an upgraded Server).
	OnReady func(workerproto.ReadyInfo)
	Rand    *rand.Rand

	// Upgrades is optional; without it the supervisor only restarts crashes.
	Upgrades Upgrades
	// HealthTimeout is how long a version on trial may take to become ready, and
	// StableWindow how long it must then stay up before the upgrade is committed.
	HealthTimeout time.Duration
	StableWindow  time.Duration
}

// Supervisor owns the worker lifecycle.
type Supervisor struct {
	cfg Config
	log *slog.Logger
}

// New applies defaults to cfg and returns a Supervisor.
func New(cfg Config) *Supervisor {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = time.Minute
	}
	if cfg.StopGrace <= 0 {
		cfg.StopGrace = 10 * time.Second
	}
	if cfg.ReadyPoll <= 0 {
		cfg.ReadyPoll = 500 * time.Millisecond
	}
	if cfg.GiveUpAfter <= 0 {
		cfg.GiveUpAfter = 5
	}
	if cfg.LockRetries <= 0 {
		cfg.LockRetries = 10
	}
	if cfg.LockRetryDelay <= 0 {
		cfg.LockRetryDelay = time.Second
	}
	if cfg.HealthTimeout <= 0 {
		cfg.HealthTimeout = 2 * time.Minute
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return &Supervisor{cfg: cfg, log: slog.With("component", "supervisor", "role", cfg.Role)}
}

type runResult struct {
	exit      ExitResult
	ready     bool
	stopped   bool
	lived     time.Duration
	launchErr error
	// upgrade is set when the worker was stopped to try a newer version.
	upgrade *Candidate
	// trialFailed says why a version on trial must be rolled back.
	trialFailed string
}

// binary is what to launch next and whether it is a version on trial.
func (s *Supervisor) binary() (string, bool) {
	if s.cfg.Upgrades != nil {
		if path, trial := s.cfg.Upgrades.Binary(); path != "" {
			return path, trial
		}
	}
	return s.cfg.Installed, false
}

// Run supervises the worker until it is told to stop, the worker ends for good,
// or the supervisor gives up. The return value is the process exit code.
func (s *Supervisor) Run(ctx context.Context) int {
	backoff := reconnect.New(s.cfg.BackoffBase, s.cfg.BackoffMax, s.cfg.Rand)
	neverReady := 0
	lockRetries := 0
	if s.cfg.Upgrades != nil {
		if err := s.cfg.Upgrades.Resume(); err != nil {
			s.log.Warn("cannot reconcile the upgrade state; continuing with what it says", "error", err)
		}
	}
	for launch := 1; ; launch++ {
		if ctx.Err() != nil {
			return workerproto.ExitOK
		}
		bin, trial := s.binary()
		run := s.runOnce(ctx, launch, bin, trial)
		if run.stopped {
			return workerproto.ExitOK
		}
		if run.upgrade != nil {
			// Stopping for an upgrade is not a failure, so there is no backoff.
			if err := s.cfg.Upgrades.Activate(*run.upgrade); err != nil {
				s.log.Error("cannot activate the upgrade; staying on the current version", "version", run.upgrade.Version, "error", err)
			}
			neverReady, lockRetries = 0, 0
			continue
		}
		if run.trialFailed != "" {
			s.log.Error("upgrade failed; rolling back", "reason", run.trialFailed)
			if err := s.cfg.Upgrades.Rollback(run.trialFailed); err != nil {
				s.log.Error("rollback failed", "error", err)
			}
			// The failure belongs to the abandoned version, not to the one we return to.
			neverReady, lockRetries = 0, 0
			continue
		}
		if run.launchErr == nil {
			switch code := run.exit.Code; code {
			case workerproto.ExitOK:
				// The worker chose to end. Mirroring the old single process, so does
				// the supervisor, instead of resurrecting something that finished.
				return workerproto.ExitOK
			case workerproto.ExitConfig, workerproto.ExitAuthFatal:
				// Neither heals by restarting. Exiting with the same code lets the
				// outer manager (RestartPreventExitStatus) stop looping too.
				s.log.Error("worker cannot run; not restarting", "exit_code", code)
				return code
			case workerproto.ExitLockHeld:
				lockRetries++
				if lockRetries > s.cfg.LockRetries {
					s.log.Error("client lock still held; giving up", "retries", s.cfg.LockRetries)
					return workerproto.ExitLockHeld
				}
				// Normal while the previous worker is still shutting down, so it is
				// not counted as a crash and not backed off.
				if !s.sleep(ctx, s.cfg.LockRetryDelay) {
					return workerproto.ExitOK
				}
				continue
			}
		}
		lockRetries = 0
		if run.ready {
			neverReady = 0
		} else {
			neverReady++
		}
		if neverReady >= s.cfg.GiveUpAfter {
			s.log.Error("worker keeps crashing before it becomes ready; giving up so the service manager can act",
				"consecutive_failures", neverReady)
			return workerproto.ExitSupervisorGaveUp
		}
		delay := backoff.AfterSession(run.lived)
		s.log.Warn("worker exited; restarting", "exit_code", run.exit.Code, "launch_error", run.launchErr,
			"ran_for", run.lived.Round(time.Second), "restart_in", delay.Round(time.Millisecond))
		if !s.sleep(ctx, delay) {
			return workerproto.ExitOK
		}
	}
}

// sleep waits for d and reports false if the supervisor was told to stop meanwhile.
func (s *Supervisor) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-s.cfg.Clock.After(d):
		return true
	}
}

// stopChild asks the worker to stop and waits for it to be gone.
func (s *Supervisor) stopChild(child Child) {
	if err := child.Stop(s.cfg.StopGrace); err != nil {
		s.log.Warn("stopping worker", "error", err)
	}
	select {
	case <-child.Wait():
	default:
	}
}

type checkOutcome struct {
	candidate *Candidate
	err       error
}

// runOnce starts one worker and watches it until it exits, the supervisor is
// stopped, or an upgrade decision ends it. While trial is true the version has not
// been confirmed: it must become ready within HealthTimeout and then stay up for
// StableWindow, otherwise runOnce reports trialFailed.
func (s *Supervisor) runOnce(ctx context.Context, launch int, bin string, trial bool) runResult {
	readyDir, cleanup := s.newReadyDir(launch)
	defer cleanup()
	child, err := s.cfg.Launcher.Start(ctx, Spec{
		Bin: bin, Args: s.cfg.Args, Env: s.cfg.Env, ReadyDir: readyDir,
	})
	if err != nil {
		res := runResult{launchErr: err}
		if trial {
			res.trialFailed = fmt.Sprintf("the new version could not be started: %v", err)
		}
		return res
	}
	clock := s.cfg.Clock
	started := clock.Now()
	lived := func() time.Duration { return clock.Now().Sub(started) }

	ready := false
	lastServer := ""
	var running workerproto.ReadyInfo
	var healthC, stableC, checkC <-chan time.Time
	var checkDone chan checkOutcome
	var cancelCheck context.CancelFunc
	firstCheck := true
	defer func() {
		if cancelCheck != nil {
			cancelCheck()
		}
	}()

	if trial {
		healthC = clock.After(s.cfg.HealthTimeout)
	}
	scheduleCheck := func() {
		if s.cfg.Upgrades == nil || trial {
			return
		}
		if d := s.cfg.Upgrades.NextCheck(firstCheck); d > 0 {
			checkC = clock.After(d)
		}
		firstCheck = false
	}
	commit := func() {
		if err := s.cfg.Upgrades.Commit(); err != nil {
			s.log.Error("cannot record the upgrade as confirmed", "error", err)
		}
		trial, stableC = false, nil
		scheduleCheck()
	}
	onFirstReady := func() {
		if trial {
			healthC = nil
			if s.cfg.StableWindow <= 0 {
				commit()
			} else {
				stableC = clock.After(s.cfg.StableWindow)
			}
			return
		}
		scheduleCheck()
	}
	checkReady := func() {
		if readyDir == "" {
			return
		}
		info, ok, err := workerproto.ReadReady(readyDir)
		if err != nil || !ok {
			return
		}
		if !ready || info.ServerVersion != lastServer {
			first := !ready
			ready, lastServer, running = true, info.ServerVersion, info
			if s.cfg.OnReady != nil {
				s.cfg.OnReady(info)
			}
			if first {
				onFirstReady()
			}
		}
	}

	poll := clock.After(s.cfg.ReadyPoll)
	for {
		select {
		case <-ctx.Done():
			s.stopChild(child)
			return runResult{stopped: true, lived: lived()}
		case res := <-child.Wait():
			// A worker can publish READY and exit between two polls.
			checkReady()
			out := runResult{exit: res, ready: ready, lived: lived()}
			// A held lock is normal while the previous worker is still shutting down,
			// so it is retried rather than counted against the version on trial.
			if trial && res.Code != workerproto.ExitLockHeld {
				out.trialFailed = fmt.Sprintf("the new version exited with code %d during its trial", res.Code)
			}
			return out
		case <-poll:
			poll = clock.After(s.cfg.ReadyPoll)
			checkReady()
		case <-healthC:
			s.stopChild(child)
			return runResult{lived: lived(), trialFailed: fmt.Sprintf("the new version was not ready within %s", s.cfg.HealthTimeout)}
		case <-stableC:
			commit()
		case <-checkC:
			checkC = nil
			checkCtx, cancel := context.WithCancel(ctx)
			cancelCheck = cancel
			checkDone = make(chan checkOutcome, 1)
			info, done := running, checkDone
			go func() {
				candidate, err := s.cfg.Upgrades.Check(checkCtx, info)
				done <- checkOutcome{candidate, err}
			}()
		case out := <-checkDone:
			checkDone = nil
			if cancelCheck != nil {
				cancelCheck()
				cancelCheck = nil
			}
			if out.err != nil {
				s.log.Warn("release check failed; will try again later", "error", out.err)
			}
			if out.candidate == nil {
				scheduleCheck()
				continue
			}
			s.log.Info("a newer version is available; restarting the worker to try it", "version", out.candidate.Version)
			s.stopChild(child)
			return runResult{upgrade: out.candidate, lived: lived()}
		}
	}
}

// newReadyDir creates a fresh directory for one launch. A new directory each time
// means a stale ready file from an earlier worker can never be mistaken for the
// current one, so no nonce is needed.
//
// It never fails startup: if RunDir cannot be created (a read-only state
// directory under ProtectSystem=strict, say) a temporary directory is used, and if
// even that fails the worker runs without readiness being observed.
func (s *Supervisor) newReadyDir(launch int) (string, func()) {
	if s.cfg.RunDir != "" {
		dir := filepath.Join(s.cfg.RunDir, strconv.Itoa(launch))
		_ = os.RemoveAll(dir)
		err := os.MkdirAll(dir, 0o700)
		if err == nil {
			return dir, func() { _ = os.RemoveAll(dir) }
		}
		s.log.Warn("cannot create the ready directory in the state directory; using a temporary one", "dir", dir, "error", err)
	}
	dir, err := os.MkdirTemp("", "tunnelmesh-ready-*")
	if err != nil {
		s.log.Warn("cannot create a temporary ready directory; readiness will not be observed", "error", err)
		return "", func() {}
	}
	return dir, func() { _ = os.RemoveAll(dir) }
}
