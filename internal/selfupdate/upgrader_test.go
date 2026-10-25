package selfupdate

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/semver"
	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

var testNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

type fakePreflight struct {
	mu    sync.Mutex
	err   error
	calls []string
}

func (f *fakePreflight) Check(_ context.Context, bin, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, bin+"|"+target)
	return f.err
}

type upgraderFixture struct {
	t         *testing.T
	store     *Store
	source    *fakeSource
	preflight *fakePreflight
	upgrader  *Upgrader
}

// newUpgraderFixture builds an Upgrader over a temp state directory. installed is
// the version of the binary the supervisor itself is.
func newUpgraderFixture(t *testing.T, installed string) *upgraderFixture {
	t.Helper()
	f := &upgraderFixture{t: t, source: newFakeSource(), preflight: &fakePreflight{}}
	f.store = &Store{Dir: filepath.Join(t.TempDir(), "updates"), Role: "client", GOOS: "linux"}
	f.upgrader = f.build(installed)
	return f
}

// build makes another Upgrader over the same state directory, which is what a
// restarted supervisor is.
func (f *upgraderFixture) build(installed string) *Upgrader {
	fetcher, _ := newTestFetcher(f.source)
	return &Upgrader{
		Store: f.store, Source: f.source, Fetcher: fetcher, Preflight: f.preflight,
		Role: "client", GOOS: "linux", GOARCH: "amd64", InstalledVersion: installed,
		CheckInterval: 6 * time.Hour, BadVersionTTL: 24 * time.Hour, KeepVersions: 2,
		Now: func() time.Time { return testNow }, Rand: rand.New(rand.NewSource(1)),
	}
}

func (f *upgraderFixture) release(version, content string) {
	f.t.Helper()
	parsed, err := semver.Parse(version)
	if err != nil {
		f.t.Fatal(err)
	}
	asset := AssetName(parsed, "linux", "amd64")
	archive := tarGzBytes(f.t, "./tunnelmesh-client", content)
	f.source.files[asset] = archive
	// SHA256SUMS can list several releases.
	line := sha(archive) + "  " + asset + "\n"
	f.source.files["SHA256SUMS"] = append(f.source.files["SHA256SUMS"], []byte(line)...)
	f.source.latest, f.source.latestOK = parsed, true
}

func running(version, server string) workerproto.ReadyInfo {
	return workerproto.ReadyInfo{PID: 1, Version: version, ServerVersion: server}
}

func (f *upgraderFixture) state() State {
	f.t.Helper()
	st, err := f.store.Load()
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func TestCheckDownloadsPreflightsAndReturnsACandidate(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	f.release("v1.4.0", "NEW")
	cand, err := f.upgrader.Check(context.Background(), running("v1.3.0", "v1.4.0"))
	if err != nil || cand == nil {
		t.Fatalf("Check() = %+v, %v, want a candidate", cand, err)
	}
	if cand.Version != "v1.4.0" {
		t.Fatalf("candidate = %+v", cand)
	}
	if data, err := os.ReadFile(cand.Path); err != nil || string(data) != "NEW" {
		t.Fatalf("candidate binary = %q, %v", data, err)
	}
	if len(f.preflight.calls) != 1 || !strings.HasPrefix(f.preflight.calls[0], cand.Path) {
		t.Fatalf("preflight calls = %v, want one against the candidate", f.preflight.calls)
	}
	// Nothing is activated by a check: that is the supervisor's decision.
	if st := f.state(); st.Current != "" || st.Pending != nil {
		t.Fatalf("state = %+v after Check, want it untouched", st)
	}
}

func TestCheckDoesNotDownloadWhenThePolicySaysNo(t *testing.T) {
	for name, tc := range map[string]struct{ current, server string }{
		"Server is older":    {"v1.3.0", "v1.3.0"},
		"Server unknown":     {"v1.3.0", ""},
		"development build":  {"dev", "v1.4.0"},
		"already up to date": {"v1.4.0", "v1.4.0"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpgraderFixture(t, tc.current)
			f.release("v1.4.0", "NEW")
			cand, err := f.upgrader.Check(context.Background(), running(tc.current, tc.server))
			if err != nil || cand != nil {
				t.Fatalf("Check() = %+v, %v, want nothing to do", cand, err)
			}
			if n := f.source.calls[AssetName(semver.Version{Major: 1, Minor: 4}, "linux", "amd64")]; n != 0 {
				t.Fatalf("the archive was fetched %d times although the policy forbids the upgrade", n)
			}
		})
	}
}

func TestCheckFetchesTheServerCeilingNotTheLatestRelease(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	f.release("v1.4.0", "FOUR")
	f.release("v1.5.0", "FIVE")
	cand, err := f.upgrader.Check(context.Background(), running("v1.3.0", "v1.4.0"))
	if err != nil || cand == nil || cand.Version != "v1.4.0" {
		t.Fatalf("Check() = %+v, %v, want v1.4.0 (the Server's version), not the newer release", cand, err)
	}
	if data, _ := os.ReadFile(cand.Path); string(data) != "FOUR" {
		t.Fatalf("binary = %q", data)
	}
}

func TestCheckReportsAnUnreachableSourceWithoutChangingState(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	f.source.latestOK = false
	if cand, err := f.upgrader.Check(context.Background(), running("v1.3.0", "v1.4.0")); err == nil || cand != nil {
		t.Fatalf("Check() = %+v, %v, want an error", cand, err)
	}
	if st := f.state(); st.Current != "" || len(st.Bad) != 0 {
		t.Fatalf("state = %+v, want an unreachable source to leave it alone", st)
	}
}

func TestCheckLeavesNothingBehindForAChecksumMismatch(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	f.release("v1.4.0", "NEW")
	f.source.files["SHA256SUMS"] = []byte(strings.Repeat("0", 64) + "  " + AssetName(semver.Version{Major: 1, Minor: 4}, "linux", "amd64") + "\n")
	if cand, err := f.upgrader.Check(context.Background(), running("v1.3.0", "v1.4.0")); !errors.Is(err, ErrChecksumMismatch) || cand != nil {
		t.Fatalf("Check() = %+v, %v, want ErrChecksumMismatch", cand, err)
	}
	if _, err := os.Stat(filepath.Join(f.store.Dir, "versions")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(f.store.Dir, "versions"))
		if len(entries) != 0 {
			t.Fatalf("versions holds %v after a rejected download", entries)
		}
	}
	if len(f.preflight.calls) != 0 {
		t.Fatal("an unverified binary was executed by the preflight")
	}
	if f.state().IsBad("v1.4.0", testNow) {
		t.Fatal("a corrupt download must not blacklist a good release")
	}
}

func TestCheckBlacklistsAVersionThatFailsPreflight(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	f.release("v1.4.0", "NEW")
	f.preflight.err = errors.New("check-config: unknown mode")
	cand, err := f.upgrader.Check(context.Background(), running("v1.3.0", "v1.4.0"))
	if err == nil || cand != nil {
		t.Fatalf("Check() = %+v, %v, want the preflight error", cand, err)
	}
	st := f.state()
	if !st.IsBad("v1.4.0", testNow) || !strings.Contains(st.Bad["v1.4.0"].Reason, "preflight") {
		t.Fatalf("state = %+v, want v1.4.0 marked bad with a preflight reason", st)
	}
	if _, statErr := os.Stat(filepath.Join(f.store.Dir, "versions", "v1.4.0")); statErr == nil {
		t.Fatal("the rejected version was kept on disk")
	}
	// A second check is now a no-op until the mark expires, instead of downloading again.
	f.source.calls = map[string]int{}
	if cand, err := f.upgrader.Check(context.Background(), running("v1.3.0", "v1.4.0")); cand != nil || err != nil {
		t.Fatalf("second Check() = %+v, %v, want nothing to do", cand, err)
	}
	if len(f.source.calls) != 0 && f.source.calls[AssetName(semver.Version{Major: 1, Minor: 4}, "linux", "amd64")] != 0 {
		t.Fatal("a blacklisted version was downloaded again")
	}
}

func candidateFor(t *testing.T, f *upgraderFixture) (*Upgrader, string) {
	t.Helper()
	f.release("v1.4.0", "NEW")
	cand, err := f.upgrader.Check(context.Background(), running("v1.3.0", "v1.4.0"))
	if err != nil || cand == nil {
		t.Fatalf("Check() = %+v, %v", cand, err)
	}
	if err := f.upgrader.Activate(*cand); err != nil {
		t.Fatal(err)
	}
	return f.upgrader, cand.Path
}

func TestActivateRecordsATrialInOneAtomicWrite(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	up, path := candidateFor(t, f)
	st := f.state()
	if st.Current != "v1.4.0" || st.Pending == nil || st.Pending.Target != "v1.4.0" {
		t.Fatalf("state = %+v, want v1.4.0 current and pending", st)
	}
	if got, trial := up.Binary(); got != path || !trial {
		t.Fatalf("Binary() = %q, %v, want the candidate on trial", got, trial)
	}
}

func TestCommitConfirmsTheTrial(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	up, path := candidateFor(t, f)
	if err := up.Commit(); err != nil {
		t.Fatal(err)
	}
	if st := f.state(); st.Pending != nil || st.Current != "v1.4.0" {
		t.Fatalf("state = %+v, want the upgrade confirmed", st)
	}
	if got, trial := up.Binary(); got != path || trial {
		t.Fatalf("Binary() = %q, %v, want the version no longer on trial", got, trial)
	}
}

func TestRollbackReturnsToTheBaselineAndBlacklistsTheVersion(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	up, _ := candidateFor(t, f)
	if err := up.Rollback("not ready"); err != nil {
		t.Fatal(err)
	}
	st := f.state()
	if st.Current != "" || st.Pending != nil || !st.IsBad("v1.4.0", testNow) || st.Bad["v1.4.0"].Reason != "not ready" {
		t.Fatalf("state = %+v, want the baseline restored and v1.4.0 blacklisted", st)
	}
	if got, trial := up.Binary(); got != "" || trial {
		t.Fatalf("Binary() = %q, %v, want the installed binary", got, trial)
	}
	if !st.Bad["v1.4.0"].Until.Equal(testNow.Add(24 * time.Hour)) {
		t.Fatalf("blacklist until %v, want the configured TTL", st.Bad["v1.4.0"].Until)
	}
}

// A later upgrade that fails must return to the version that was working, not to
// the installed baseline.
func TestRollbackReturnsToThePreviousUpgradedVersion(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	up, path14 := candidateFor(t, f)
	if err := up.Commit(); err != nil {
		t.Fatal(err)
	}
	f.release("v1.5.0", "FIVE")
	cand, err := up.Check(context.Background(), running("v1.4.0", "v1.5.0"))
	if err != nil || cand == nil {
		t.Fatalf("Check() = %+v, %v", cand, err)
	}
	if err := up.Activate(*cand); err != nil {
		t.Fatal(err)
	}
	if err := up.Rollback("crashed"); err != nil {
		t.Fatal(err)
	}
	if got, trial := up.Binary(); got != path14 || trial {
		t.Fatalf("Binary() = %q, %v, want v1.4.0, the version that was working", got, trial)
	}
}

// The supervisor can be killed at any moment. Whatever it left behind must resolve
// to a consistent version on the next start.
func TestResumeRollsBackATrialTheSupervisorDiedDuring(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	candidateFor(t, f)
	restarted := f.build("v1.3.0")
	if err := restarted.Resume(); err != nil {
		t.Fatal(err)
	}
	st := f.state()
	if st.Current != "" || st.Pending != nil || !st.IsBad("v1.4.0", testNow) {
		t.Fatalf("state = %+v, want the half-finished trial rolled back and blacklisted", st)
	}
	if got, trial := restarted.Binary(); got != "" || trial {
		t.Fatalf("Binary() = %q, %v, want the baseline", got, trial)
	}
}

func TestResumeKeepsAConfirmedUpgrade(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	up, path := candidateFor(t, f)
	if err := up.Commit(); err != nil {
		t.Fatal(err)
	}
	restarted := f.build("v1.3.0")
	if err := restarted.Resume(); err != nil {
		t.Fatal(err)
	}
	if got, trial := restarted.Binary(); got != path || trial {
		t.Fatalf("Binary() = %q, %v, want the confirmed upgrade kept", got, trial)
	}
}

// An administrator who reinstalls a newer release must not be silently held back
// on an older downloaded version.
func TestAReinstalledNewerBaselineWins(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	up, _ := candidateFor(t, f)
	if err := up.Commit(); err != nil {
		t.Fatal(err)
	}
	reinstalled := f.build("v1.6.0")
	if err := reinstalled.Resume(); err != nil {
		t.Fatal(err)
	}
	if got, _ := reinstalled.Binary(); got != "" {
		t.Fatalf("Binary() = %q, want the newer installed binary", got)
	}
	if st := f.state(); st.Current != "" {
		t.Fatalf("state = %+v, want the stale version forgotten", st)
	}
}

func TestBinaryFallsBackToTheBaselineWhenTheStateCannotBeTrusted(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	up, path := candidateFor(t, f)
	if err := up.Commit(); err != nil {
		t.Fatal(err)
	}
	// The file the state names has vanished.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, trial := up.Binary(); got != "" || trial {
		t.Fatalf("Binary() with a missing file = %q, %v, want the baseline", got, trial)
	}
	// The state names something that is not a release version at all.
	if err := f.store.Save(State{Current: "../../etc/evil"}); err != nil {
		t.Fatal(err)
	}
	if got, trial := up.Binary(); got != "" || trial {
		t.Fatalf("Binary() with a hostile state = %q, %v, want the baseline", got, trial)
	}
}

// A crash at any step must leave state that the next start can resolve.
func TestFaultInjectionLeavesRecoverableState(t *testing.T) {
	for _, point := range []string{FaultAfterDownload, FaultAfterInstall, FaultBeforeActivate, FaultAfterActivate} {
		t.Run(point, func(t *testing.T) {
			f := newUpgraderFixture(t, "v1.3.0")
			f.release("v1.4.0", "NEW")
			f.upgrader.FaultHook = func(p string) error {
				if p == point {
					return errors.New("injected crash")
				}
				return nil
			}
			cand, err := f.upgrader.Check(context.Background(), running("v1.3.0", "v1.4.0"))
			if err == nil && cand != nil {
				err = f.upgrader.Activate(*cand)
			}
			if err == nil {
				t.Fatalf("the fault at %s was not triggered", point)
			}
			restarted := f.build("v1.3.0")
			if err := restarted.Resume(); err != nil {
				t.Fatal(err)
			}
			st := f.state()
			if st.Pending != nil {
				t.Fatalf("state = %+v, want no trial left pending after recovery", st)
			}
			if got, trial := restarted.Binary(); got != "" || trial {
				t.Fatalf("Binary() = %q, %v, want the baseline after a crash at %s", got, trial, point)
			}
		})
	}
}

func TestNextCheckSpreadsAFleetOutAndCanBeDisabled(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	for i := 0; i < 50; i++ {
		first := f.upgrader.NextCheck(true)
		if first < time.Minute || first > 6*time.Minute {
			t.Fatalf("first check in %v, want 1m to 6m after the worker is ready", first)
		}
		later := f.upgrader.NextCheck(false)
		if later < 6*time.Hour || later > 6*time.Hour+90*time.Minute {
			t.Fatalf("later check in %v, want the interval plus up to 25%% jitter", later)
		}
	}
	f.upgrader.CheckInterval = 0
	if f.upgrader.NextCheck(true) > 0 || f.upgrader.NextCheck(false) > 0 {
		t.Fatal("a zero interval must disable checking")
	}
}

func TestPruneAfterCommitKeepsOnlyTheConfiguredVersions(t *testing.T) {
	f := newUpgraderFixture(t, "v1.0.0")
	f.upgrader.KeepVersions = 1
	for _, v := range []string{"v1.1.0", "v1.2.0", "v1.3.0"} {
		dir := filepath.Join(f.store.Dir, "versions", v)
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(filepath.Join(dir, "tunnelmesh-client"), []byte(v), 0o755)
	}
	f.release("v1.4.0", "NEW")
	cand, err := f.upgrader.Check(context.Background(), running("v1.3.0", "v1.4.0"))
	if err != nil || cand == nil {
		t.Fatalf("Check() = %+v, %v", cand, err)
	}
	if err := f.upgrader.Activate(*cand); err != nil {
		t.Fatal(err)
	}
	if err := f.upgrader.Commit(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(f.store.Dir, "versions"))
	left := map[string]bool{}
	for _, e := range entries {
		left[e.Name()] = true
	}
	if !left["v1.4.0"] {
		t.Fatalf("the committed version was pruned: %v", left)
	}
	if left["v1.1.0"] || left["v1.2.0"] {
		t.Fatalf("old versions survived the prune: %v", left)
	}
}

// blockingSource never answers, like a connection that stalled mid-transfer.
type blockingSource struct{ fakeSource }

func (b *blockingSource) Latest(ctx context.Context) (Release, error) {
	<-ctx.Done()
	return Release{}, ctx.Err()
}

func TestCheckGivesUpOnASourceThatStalls(t *testing.T) {
	f := newUpgraderFixture(t, "v1.3.0")
	f.upgrader.Source = &blockingSource{}
	f.upgrader.CheckTimeout = 50 * time.Millisecond
	started := time.Now()
	cand, err := f.upgrader.Check(context.Background(), running("v1.3.0", "v1.4.0"))
	if err == nil || cand != nil {
		t.Fatalf("Check() = %+v, %v, want a timeout error", cand, err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Check took %v, want it bounded by CheckTimeout", elapsed)
	}
}
