package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Dir: filepath.Join(t.TempDir(), "updates"), Role: "client"}
}

func TestLoadWithoutAFileIsAnEmptyState(t *testing.T) {
	st, err := newTestStore(t).Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != "" || st.Previous != "" || st.Pending != nil || len(st.Bad) != 0 {
		t.Fatalf("state = %+v, want the empty baseline", st)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	want := State{
		Current: "v1.4.0", Previous: "v1.3.5", LastCheck: now,
		Pending: &Pending{Target: "v1.4.0", From: "v1.3.5", StartedAt: now},
		Bad:     map[string]BadMark{"v1.3.9": {Reason: "never ready", Until: now.Add(time.Hour)}},
	}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Current != want.Current || got.Previous != want.Previous || got.Pending == nil || *got.Pending != *want.Pending ||
		!got.LastCheck.Equal(now) || got.Bad["v1.3.9"].Reason != "never ready" {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("leftover temporary file %q", e.Name())
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(s.Dir, stateFileName))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("state file mode = %o, want 600", info.Mode().Perm())
		}
	}
}

// A damaged or foreign state file must never keep the service from starting: the
// safe reading of "I cannot tell" is the installed baseline.
func TestLoadTreatsACorruptOrNewerStateAsEmpty(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt":       "{not json",
		"empty":         "",
		"newer schema":  `{"schema": 99, "current": "v9.9.9"}`,
		"wrong shape":   `["a","b"]`,
		"unknown field": `{"schema": 1, "current": "v1.0.0", "extra": true}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			if err := os.MkdirAll(s.Dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.Dir, stateFileName), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			st, err := s.Load()
			if err != nil {
				t.Fatalf("Load() error = %v, want it to recover", err)
			}
			// "unknown field" is the same schema with a field this build does not know,
			// which is still readable; every other case must collapse to the baseline.
			if name != "unknown field" && st.Current != "" {
				t.Fatalf("Load() = %+v, want the empty baseline", st)
			}
		})
	}
}

func TestBadVersionsExpire(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	var st State
	st.MarkBad("v1.4.0", "never ready", now, time.Hour)
	if !st.IsBad("v1.4.0", now.Add(59*time.Minute)) {
		t.Fatal("a version marked bad must stay bad for the TTL")
	}
	if st.IsBad("v1.4.0", now.Add(time.Hour)) {
		t.Fatal("a version must be retried once the TTL has passed")
	}
	if st.IsBad("v1.5.0", now) {
		t.Fatal("an unrelated version is not bad")
	}
	st.Expire(now.Add(2 * time.Hour))
	if len(st.Bad) != 0 {
		t.Fatalf("Expire() left %v", st.Bad)
	}
}

func TestBinaryPathLayout(t *testing.T) {
	s := newTestStore(t)
	s.GOOS = "linux"
	got, err := s.BinaryPath("v1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(s.Dir, "versions", "v1.4.0", "tunnelmesh-client"); got != want {
		t.Fatalf("BinaryPath() = %q, want %q", got, want)
	}
	s.GOOS = "windows"
	got, _ = s.BinaryPath("v1.4.0")
	if !strings.HasSuffix(got, "tunnelmesh-client.exe") {
		t.Fatalf("BinaryPath() = %q, want a .exe on Windows", got)
	}
}

// state.json is a file on disk, so whatever it names must not be able to point
// the supervisor at a binary outside the versions directory.
func TestBinaryPathRejectsAnythingThatIsNotAReleaseVersion(t *testing.T) {
	s := newTestStore(t)
	for _, bad := range []string{"", "dev", "../../etc/passwd", "v1.2.3/../../x", "/abs/path", `v1.2.3\..\x`, "v1.2", "latest"} {
		if path, err := s.BinaryPath(bad); err == nil {
			t.Errorf("BinaryPath(%q) = %q, want an error", bad, path)
		}
	}
}

func TestInstallMovesTheStagedBinaryIntoTheVersionsDirectory(t *testing.T) {
	s := newTestStore(t)
	s.GOOS = "linux"
	staging, err := s.NewStagingDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "tunnelmesh-client"), []byte("binary-v1.4.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := s.Install(staging, "v1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "binary-v1.4.0" {
		t.Fatalf("installed binary = %q, %v", data, err)
	}
	if _, err := os.Stat(staging); err == nil {
		t.Fatal("the staging directory was not consumed")
	}
	// Installing the same version again replaces it, which is what a retry after a
	// failed preflight needs.
	staging2, _ := s.NewStagingDir()
	if err := os.WriteFile(filepath.Join(staging2, "tunnelmesh-client"), []byte("rebuilt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Install(staging2, "v1.4.0"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "rebuilt" {
		t.Fatalf("second install left %q", data)
	}
}

func TestPruneKeepsTheNewestVersionsAndEverythingInUse(t *testing.T) {
	s := newTestStore(t)
	s.GOOS = "linux"
	for _, v := range []string{"v1.0.0", "v1.1.0", "v1.2.0", "v1.3.0", "v1.10.0"} {
		dir := filepath.Join(s.Dir, "versions", v)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tunnelmesh-client"), []byte(v), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// An unrelated directory is not ours to delete.
	if err := os.MkdirAll(filepath.Join(s.Dir, "versions", "keep-me"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Leftover staging from a crashed download is garbage.
	if _, err := s.NewStagingDir(); err != nil {
		t.Fatal(err)
	}

	if err := s.Prune(2, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	left := map[string]bool{}
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "versions"))
	for _, e := range entries {
		left[e.Name()] = true
	}
	// Newest two by semver (v1.10.0 > v1.3.0, not lexical) plus the one in use.
	for _, want := range []string{"v1.10.0", "v1.3.0", "v1.0.0", "keep-me"} {
		if !left[want] {
			t.Errorf("Prune removed %s", want)
		}
	}
	for _, gone := range []string{"v1.1.0", "v1.2.0"} {
		if left[gone] {
			t.Errorf("Prune kept %s", gone)
		}
	}
	if staged, _ := os.ReadDir(filepath.Join(s.Dir, "staging")); len(staged) != 0 {
		t.Fatalf("staging not cleaned: %v", staged)
	}
}

func TestLockExcludesASecondSupervisor(t *testing.T) {
	s := newTestStore(t)
	first, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lock(); err == nil {
		t.Fatal("a second supervisor acquired the state directory lock")
	}
	_ = first.Release()
	again, err := s.Lock()
	if err != nil {
		t.Fatalf("Lock after Release: %v", err)
	}
	_ = again.Release()
}
