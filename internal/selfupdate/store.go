// Package selfupdate implements automatic upgrade for the client and the agent:
// finding a newer release, fetching and verifying it, and the on-disk state that
// lets a failed upgrade be rolled back.
package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/filelock"
	"github.com/tunnelmesh/tunnelmesh/internal/semver"
)

const (
	stateFileName = "state.json"
	stateSchema   = 1
	lockFileName  = "supervisor.lock"
)

// Pending marks an upgrade that was activated but not yet confirmed healthy. If a
// supervisor starts and finds one, the previous run died mid-trial and the upgrade
// is treated as failed.
type Pending struct {
	Target    string    `json:"target"`
	From      string    `json:"from"`
	StartedAt time.Time `json:"started_at"`
}

// BadMark records a version that failed an upgrade and must not be retried until
// Until.
type BadMark struct {
	Reason string    `json:"reason"`
	Until  time.Time `json:"until"`
}

// State is the persisted upgrade state. Current and Previous name versions
// downloaded under versions/; an empty string means the installed baseline.
type State struct {
	Schema    int                `json:"schema"`
	Current   string             `json:"current,omitempty"`
	Previous  string             `json:"previous,omitempty"`
	Pending   *Pending           `json:"pending,omitempty"`
	Bad       map[string]BadMark `json:"bad,omitempty"`
	LastCheck time.Time          `json:"last_check,omitempty"`
}

// IsBad reports whether version failed an upgrade recently enough to be skipped.
func (s State) IsBad(version string, now time.Time) bool {
	mark, ok := s.Bad[version]
	return ok && now.Before(mark.Until)
}

// MarkBad records a failed version for ttl.
func (s *State) MarkBad(version, reason string, now time.Time, ttl time.Duration) {
	if s.Bad == nil {
		s.Bad = make(map[string]BadMark)
	}
	s.Bad[version] = BadMark{Reason: reason, Until: now.Add(ttl)}
}

// Expire drops bad-version marks whose time has passed.
func (s *State) Expire(now time.Time) {
	for version, mark := range s.Bad {
		if !now.Before(mark.Until) {
			delete(s.Bad, version)
		}
	}
}

// Store owns the update directory (`<state>/updates`):
//
//	state.json            current/previous/pending/bad
//	supervisor.lock       one supervisor per directory
//	versions/<ver>/       one downloaded binary each
//	staging/              downloads in progress
type Store struct {
	Dir  string
	Role string
	// GOOS is only overridden by tests; it decides the .exe suffix.
	GOOS string
}

func (s *Store) goos() string {
	if s.GOOS != "" {
		return s.GOOS
	}
	return runtime.GOOS
}

// Load reads the state. Anything it cannot trust (a missing, damaged or newer
// file) reads as the empty state, which means "run the installed binary": the
// service must start even when this file is broken.
func (s *Store) Load() (State, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, stateFileName))
	if errors.Is(err, os.ErrNotExist) {
		return State{Schema: stateSchema}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		slog.Warn("ignoring unreadable update state; falling back to the installed binary", "error", err)
		return State{Schema: stateSchema}, nil
	}
	if st.Schema > stateSchema {
		slog.Warn("ignoring update state written by a newer release; falling back to the installed binary", "schema", st.Schema)
		return State{Schema: stateSchema}, nil
	}
	st.Schema = stateSchema
	return st, nil
}

// Save writes the state atomically: a reader sees the old or the new document,
// and a crash mid-write leaves the old one.
func (s *Store) Save(st State) error {
	st.Schema = stateSchema
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(s.Dir, stateFileName)); err != nil {
		return err
	}
	syncDir(s.Dir)
	return nil
}

// syncDir makes a rename durable. It is best effort: not every platform allows
// opening a directory, and the state file is also re-validated on load.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

func (s *Store) binaryName() string {
	name := "tunnelmesh-" + s.Role
	if s.goos() == "windows" {
		name += ".exe"
	}
	return name
}

// versionDir validates version before building a path from it. state.json is a
// file on disk, so whatever it names must never be able to point outside versions/.
func (s *Store) versionDir(version string) (string, error) {
	parsed, err := semver.Parse(version)
	if err != nil || parsed.String() != version {
		return "", fmt.Errorf("selfupdate: %q is not a release version", version)
	}
	return filepath.Join(s.Dir, "versions", version), nil
}

// BinaryPath is where the binary of version lives (or will live).
func (s *Store) BinaryPath(version string) (string, error) {
	dir, err := s.versionDir(version)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, s.binaryName()), nil
}

// NewStagingDir creates a fresh directory for one download.
func (s *Store) NewStagingDir() (string, error) {
	root := filepath.Join(s.Dir, "staging")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, "dl-*")
}

// Install moves a verified, extracted staging directory into versions/<version>,
// replacing a previous attempt at the same version. The directory rename is what
// makes the version appear all at once.
func (s *Store) Install(stagingDir, version string) (string, error) {
	target, err := s.versionDir(version)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	if err := os.Rename(stagingDir, target); err != nil {
		return "", err
	}
	syncDir(filepath.Dir(target))
	return filepath.Join(target, s.binaryName()), nil
}

// Prune deletes downloaded versions beyond the keep newest, never touching
// those in inUse, and clears leftover staging directories from interrupted
// downloads. Directories whose name is not a release version are left alone.
func (s *Store) Prune(keep int, inUse ...string) error {
	_ = os.RemoveAll(filepath.Join(s.Dir, "staging"))
	entries, err := os.ReadDir(filepath.Join(s.Dir, "versions"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	protected := map[string]bool{}
	for _, v := range inUse {
		protected[v] = true
	}
	type found struct {
		name string
		ver  semver.Version
	}
	var versions []found
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		v, err := semver.Parse(e.Name())
		if err != nil || v.String() != e.Name() {
			continue
		}
		versions = append(versions, found{e.Name(), v})
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].ver.Compare(versions[j].ver) > 0 })
	var firstErr error
	for i, v := range versions {
		if i < keep || protected[v.name] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.Dir, "versions", v.name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Lock takes the per-directory supervisor lock so two supervisors never upgrade
// the same state at once.
func (s *Store) Lock() (filelock.Lock, error) {
	return filelock.Acquire(filepath.Join(s.Dir, lockFileName))
}
