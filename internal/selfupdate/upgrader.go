package selfupdate

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/semver"
	"github.com/tunnelmesh/tunnelmesh/internal/supervisor"
	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

// Fault points for tests. A FaultHook that returns an error at one of them makes
// the step fail right there, with whatever was persisted so far left on disk,
// which is how a crash at that moment looks to the next supervisor.
const (
	FaultAfterDownload  = "after_download"
	FaultAfterInstall   = "after_install"
	FaultBeforeActivate = "before_activate"
	FaultAfterActivate  = "after_activate"
)

// Preflighter proves a downloaded binary can run before the worker is switched
// to it.
type Preflighter interface {
	Check(ctx context.Context, bin, target string) error
}

// Upgrader decides, fetches and records upgrades. It implements
// supervisor.Upgrades; the supervisor owns the process and the trial's clock.
type Upgrader struct {
	Store     *Store
	Source    Source
	Fetcher   *Fetcher
	Preflight Preflighter

	Role, GOOS, GOARCH string
	// InstalledVersion is the supervisor's own build, the baseline every
	// downloaded version is compared with.
	InstalledVersion string

	CheckInterval time.Duration
	BadVersionTTL time.Duration
	KeepVersions  int
	// CheckTimeout bounds a whole Check, download included; zero means no bound.
	CheckTimeout time.Duration
	// FirstCheckDelay and FirstCheckJitter place a worker's first check after it
	// became ready; zero means one minute and five minutes. They exist so tests do
	// not wait minutes, and are not exposed as configuration.
	FirstCheckDelay, FirstCheckJitter time.Duration

	Now       func() time.Time
	Rand      *rand.Rand
	FaultHook func(point string) error

	// mu serialises read-modify-write cycles on the state file; downloads run
	// outside it.
	mu sync.Mutex
}

var _ supervisor.Upgrades = (*Upgrader)(nil)

func (u *Upgrader) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Upgrader) fault(point string) error {
	if u.FaultHook == nil {
		return nil
	}
	return u.FaultHook(point)
}

// Resume reconciles state left by a previous supervisor. A trial still pending
// means that supervisor died before the version was confirmed, so the upgrade is
// treated as failed. A downloaded version older than the installed binary (an
// administrator reinstalled) is forgotten. Stale downloads are cleaned up.
func (u *Upgrader) Resume() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	st, err := u.Store.Load()
	if err != nil {
		return err
	}
	if st.Pending != nil {
		slog.Warn("a previous upgrade trial was interrupted; rolling it back", "version", st.Pending.Target)
		u.rollbackInState(&st, "the supervisor stopped during the upgrade trial")
	}
	if u.installedIsNewer(st.Current) {
		slog.Info("the installed binary is newer than the downloaded version; using the installed binary", "downloaded", st.Current)
		st.Current, st.Previous = "", ""
	}
	st.Expire(u.now())
	if err := u.Store.Save(st); err != nil {
		return err
	}
	return u.Store.Prune(u.KeepVersions, st.Current, st.Previous)
}

func (u *Upgrader) installedIsNewer(current string) bool {
	if current == "" {
		return false
	}
	cur, err := semver.Parse(current)
	if err != nil {
		return false
	}
	installed, err := semver.Parse(u.InstalledVersion)
	if err != nil {
		return false
	}
	return installed.Compare(cur) >= 0
}

// Binary returns the executable to launch. Anything about the recorded state it
// cannot trust (a name that is not a release version, a file that is gone, a
// downloaded version the installed binary has overtaken) resolves to the installed
// binary, because the service must be able to start.
func (u *Upgrader) Binary() (string, bool) {
	st, err := u.Store.Load()
	if err != nil || st.Current == "" {
		return "", false
	}
	path, err := u.Store.BinaryPath(st.Current)
	if err != nil {
		slog.Warn("the upgrade state names an invalid version; using the installed binary", "version", st.Current)
		return "", false
	}
	if u.installedIsNewer(st.Current) {
		return "", false
	}
	if _, err := os.Stat(path); err != nil {
		slog.Warn("the recorded version's binary is missing; using the installed binary", "version", st.Current, "path", path)
		return "", false
	}
	return path, st.Pending != nil && st.Pending.Target == st.Current
}

// NextCheck spreads a fleet's checks out. The first check follows readiness by one
// to six minutes, so a fleet that restarted together does not ask at once; later
// checks add up to a quarter of the interval. A non-positive interval disables
// checking.
func (u *Upgrader) NextCheck(first bool) time.Duration {
	if u.CheckInterval <= 0 {
		return 0
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.Rand == nil {
		u.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if first {
		delay, jitter := u.FirstCheckDelay, u.FirstCheckJitter
		if delay <= 0 {
			delay = time.Minute
		}
		if jitter <= 0 {
			jitter = 5 * time.Minute
		}
		return delay + time.Duration(u.Rand.Int63n(int64(jitter)))
	}
	return u.CheckInterval + time.Duration(u.Rand.Int63n(int64(u.CheckInterval/4)+1))
}

// Check finds the newest version the policy allows and, when there is one,
// downloads it, verifies it and runs the preflight. Nothing is activated: that is
// the supervisor's decision.
func (u *Upgrader) Check(ctx context.Context, running workerproto.ReadyInfo) (*supervisor.Candidate, error) {
	if u.CheckTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, u.CheckTimeout)
		defer cancel()
	}
	rel, err := u.Source.Latest(ctx)
	if err != nil {
		return nil, fmt.Errorf("find the latest release from %s: %w", u.Source.Name(), err)
	}
	st, err := u.Store.Load()
	if err != nil {
		return nil, err
	}
	decision := Planner{Current: running.Version, State: st, Now: u.now}.Decide(rel, running.ServerVersion)
	u.recordCheck()
	if !decision.Upgrade {
		slog.Info("no upgrade", "reason", decision.Reason, "running", running.Version, "latest", rel.Tag, "server", running.ServerVersion)
		return nil, nil
	}
	target := Release{Version: decision.Target, Tag: decision.Target.String()}
	slog.Info("upgrading", "from", running.Version, "to", target.Tag, "source", u.Source.Name())

	staging, err := u.Store.NewStagingDir()
	if err != nil {
		return nil, err
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(staging)
		}
	}()
	if _, err := u.Fetcher.FetchBinary(ctx, target, u.Role, u.GOOS, u.GOARCH, staging); err != nil {
		return nil, fmt.Errorf("download %s: %w", target.Tag, err)
	}
	if err := u.fault(FaultAfterDownload); err != nil {
		return nil, err
	}
	path, err := u.Store.Install(staging, target.Tag)
	if err != nil {
		return nil, err
	}
	installed = true
	if err := u.fault(FaultAfterInstall); err != nil {
		return nil, err
	}
	if err := u.Preflight.Check(ctx, path, target.Tag); err != nil {
		// Shutting down is not the version's fault, so it is not blacklisted.
		if ctx.Err() == nil {
			u.blacklist(target.Tag, "preflight: "+err.Error())
		}
		return nil, fmt.Errorf("preflight of %s failed: %w", target.Tag, err)
	}
	return &supervisor.Candidate{Version: target.Tag, Path: path}, nil
}

func (u *Upgrader) recordCheck() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if st, err := u.Store.Load(); err == nil {
		st.LastCheck = u.now()
		_ = u.Store.Save(st)
	}
}

// blacklist marks a version bad and removes its files.
func (u *Upgrader) blacklist(version, reason string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if st, err := u.Store.Load(); err == nil {
		st.MarkBad(version, reason, u.now(), u.BadVersionTTL)
		_ = u.Store.Save(st)
	}
	if dir, err := u.Store.versionDir(version); err == nil {
		_ = os.RemoveAll(dir)
	}
}

// Activate makes the candidate current and records the trial in one atomic
// write, so a crash leaves either no trace of the upgrade or a pending trial that
// the next start rolls back.
func (u *Upgrader) Activate(c supervisor.Candidate) error {
	if err := u.fault(FaultBeforeActivate); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	st, err := u.Store.Load()
	if err != nil {
		return err
	}
	st.Pending = &Pending{Target: c.Version, From: st.Current, StartedAt: u.now()}
	st.Previous = st.Current
	st.Current = c.Version
	if err := u.Store.Save(st); err != nil {
		return err
	}
	return u.fault(FaultAfterActivate)
}

// Commit confirms the version on trial and trims old downloads.
func (u *Upgrader) Commit() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	st, err := u.Store.Load()
	if err != nil {
		return err
	}
	st.Pending = nil
	st.Expire(u.now())
	if err := u.Store.Save(st); err != nil {
		return err
	}
	return u.Store.Prune(u.KeepVersions, st.Current, st.Previous)
}

// Rollback abandons the version on trial and goes back to the one before it.
func (u *Upgrader) Rollback(reason string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	st, err := u.Store.Load()
	if err != nil {
		return err
	}
	u.rollbackInState(&st, reason)
	return u.Store.Save(st)
}

// rollbackInState does the bookkeeping of a rollback on st: the abandoned version
// is blacklisted for BadVersionTTL and removed from disk.
func (u *Upgrader) rollbackInState(st *State, reason string) {
	abandoned := st.Current
	if st.Pending != nil {
		abandoned = st.Pending.Target
	}
	if abandoned != "" {
		st.MarkBad(abandoned, reason, u.now(), u.BadVersionTTL)
		if dir, err := u.Store.versionDir(abandoned); err == nil {
			_ = os.RemoveAll(dir)
		}
	}
	st.Current, st.Previous, st.Pending = st.Previous, "", nil
}
