package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/tunnelmesh/tunnelmesh/internal/semver"
)

// Release files a client or agent may fetch. Anything else in the directory, and
// any name that is not exactly one of these shapes, is invisible: the name comes
// from the request path, which the caller controls.
var releaseFilePattern = regexp.MustCompile(`^(SHA256SUMS|manifest\.json|tunnelmesh-v\d+\.\d+\.\d+(-pre)?-(linux|darwin|windows)-(amd64|arm64)\.(tar\.gz|zip))$`)

var (
	errUpdateNotFound = errors.New("update file not found")
	errNoManifest     = errors.New("no release is published")
)

// UpdateService serves a directory laid out like a GitHub release (SHA256SUMS,
// manifest.json and the archives) to clients and agents that upgrade themselves.
//
// It is opt-in: with no directory configured the endpoints answer 404 and clients
// default to GitHub. Distribution from the Server concentrates trust in it, since
// a compromised Server could hand every client a malicious binary.
type UpdateService struct {
	dir string

	// maxConcurrent bounds downloads server-wide and maxPerToken per credential, so
	// a fleet restarting together, or one runaway client, cannot exhaust the Server.
	maxConcurrent int
	maxPerToken   int

	mu       sync.Mutex
	active   int
	perToken map[string]int
}

// NewUpdateService returns nil when dir is empty, which keeps distribution off.
func NewUpdateService(dir string) *UpdateService {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	return &UpdateService{dir: dir, maxConcurrent: 8, maxPerToken: 2, perToken: make(map[string]int)}
}

// acquire takes a download slot for tokenID; the returned func gives it back.
func (s *UpdateService) acquire(tokenID string) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active >= s.maxConcurrent || s.perToken[tokenID] >= s.maxPerToken {
		return nil, false
	}
	s.active++
	s.perToken[tokenID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.active--
			if s.perToken[tokenID]--; s.perToken[tokenID] <= 0 {
				delete(s.perToken, tokenID)
			}
		})
	}, true
}

// Latest is the version the manifest announces. It must be a release version.
func (s *UpdateService) Latest() (string, error) {
	file, err := s.open("manifest.json")
	if err != nil {
		if errors.Is(err, errUpdateNotFound) {
			return "", errNoManifest
		}
		return "", err
	}
	defer file.Close()
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(file).Decode(&manifest); err != nil {
		return "", fmt.Errorf("manifest.json is unreadable: %w", err)
	}
	version, err := semver.Parse(manifest.Version)
	if err != nil {
		return "", fmt.Errorf("manifest.json names %q, which is not a release version", manifest.Version)
	}
	return version.String(), nil
}

// Open returns the named release file. Only names in the release layout are
// opened, symlinks that lead out of the directory are refused, and only regular
// files are served.
func (s *UpdateService) Open(name string) (*os.File, os.FileInfo, error) {
	file, err := s.open(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func (s *UpdateService) open(name string) (*os.File, error) {
	if !releaseFilePattern.MatchString(name) {
		return nil, errUpdateNotFound
	}
	root, err := filepath.EvalSymlinks(s.dir)
	if err != nil {
		return nil, errUpdateNotFound
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, name))
	if err != nil {
		return nil, errUpdateNotFound
	}
	// The directory itself may be a symlink, but a file inside it must not lead
	// anywhere else.
	if filepath.Dir(resolved) != root {
		return nil, errUpdateNotFound
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errUpdateNotFound
	}
	return os.Open(resolved)
}
