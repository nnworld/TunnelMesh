package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/semver"
)

// fakeSource serves a release from memory and can fail on demand.
type fakeSource struct {
	mu    sync.Mutex
	files map[string][]byte
	// failures is how many times Fetch of an asset fails before it succeeds.
	failures map[string]int
	calls    map[string]int
	latest   semver.Version
	latestOK bool
}

func newFakeSource() *fakeSource {
	return &fakeSource{files: map[string][]byte{}, failures: map[string]int{}, calls: map[string]int{}}
}

func (f *fakeSource) Name() string { return "fake" }

func (f *fakeSource) Latest(context.Context) (Release, error) {
	if !f.latestOK {
		return Release{}, errors.New("fake: no latest")
	}
	return Release{Version: f.latest, Tag: f.latest.String()}, nil
}

func (f *fakeSource) Fetch(_ context.Context, _ Release, asset string, maxBytes int64) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[asset]++
	if f.failures[asset] > 0 {
		f.failures[asset]--
		return nil, errors.New("fake: connection reset")
	}
	data, ok := f.files[asset]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, asset)
	}
	return LimitBody(io.NopCloser(bytes.NewReader(data)), maxBytes), nil
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func tarGzBytes(t *testing.T, name, content string) []byte {
	t.Helper()
	path := makeTarGz(t, archiveEntry{name: "./README.md", content: "r"}, archiveEntry{name: name, content: content})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// release publishes a v1.4.0 client archive for linux/amd64 and its checksum file.
func (f *fakeSource) publish(t *testing.T, content string) (Release, string) {
	t.Helper()
	version := semver.Version{Major: 1, Minor: 4}
	asset := AssetName(version, "linux", "amd64")
	archive := tarGzBytes(t, "./tunnelmesh-client", content)
	f.files[asset] = archive
	f.files["SHA256SUMS"] = []byte(sha(archive) + "  " + asset + "\n")
	return Release{Version: version, Tag: version.String()}, asset
}

func newTestFetcher(src Source) (*Fetcher, *[]time.Duration) {
	var sleeps []time.Duration
	return &Fetcher{
		Source: src, Limits: Limits{MaxArchive: 1 << 20, MaxBinary: 1 << 20, MaxSums: 4096, Retries: 3, BaseDelay: time.Second},
		Sleep: func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil },
	}, &sleeps
}

func TestAssetName(t *testing.T) {
	v := semver.Version{Major: 1, Minor: 3, Patch: 5}
	for _, tc := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "tunnelmesh-v1.3.5-linux-amd64.tar.gz"},
		{"darwin", "arm64", "tunnelmesh-v1.3.5-darwin-arm64.tar.gz"},
		{"windows", "amd64", "tunnelmesh-v1.3.5-windows-amd64.zip"},
	} {
		if got := AssetName(v, tc.goos, tc.goarch); got != tc.want {
			t.Errorf("AssetName(%s/%s) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestFetchBinaryDownloadsVerifiesAndExtracts(t *testing.T) {
	src := newFakeSource()
	rel, asset := src.publish(t, "NEW-CLIENT")
	f, _ := newTestFetcher(src)
	staging := t.TempDir()

	path, err := f.FetchBinary(context.Background(), rel, "client", "linux", "amd64", staging)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(staging, "tunnelmesh-client") {
		t.Fatalf("path = %q, want the binary inside the staging directory", path)
	}
	if data, _ := os.ReadFile(path); string(data) != "NEW-CLIENT" {
		t.Fatalf("binary = %q", data)
	}
	if _, err := os.Stat(filepath.Join(staging, asset)); err == nil {
		t.Fatal("the downloaded archive was left in the staging directory")
	}
}

func TestFetchBinaryRefusesAChecksumMismatch(t *testing.T) {
	src := newFakeSource()
	rel, asset := src.publish(t, "NEW-CLIENT")
	// The checksum file vouches for different bytes than the archive holds.
	src.files["SHA256SUMS"] = []byte(strings.Repeat("0", 64) + "  " + asset + "\n")
	f, _ := newTestFetcher(src)
	staging := t.TempDir()

	_, err := f.FetchBinary(context.Background(), rel, "client", "linux", "amd64", staging)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	if entries, _ := os.ReadDir(staging); len(entries) != 0 {
		t.Fatalf("staging holds %v after a rejected download", entries)
	}
}

func TestFetchBinaryRefusesAnArchiveTheChecksumFileDoesNotList(t *testing.T) {
	src := newFakeSource()
	rel, _ := src.publish(t, "NEW-CLIENT")
	src.files["SHA256SUMS"] = []byte(strings.Repeat("a", 64) + "  tunnelmesh-v1.4.0-darwin-arm64.tar.gz\n")
	f, _ := newTestFetcher(src)
	if _, err := f.FetchBinary(context.Background(), rel, "client", "linux", "amd64", t.TempDir()); err == nil || !strings.Contains(err.Error(), "does not list") {
		t.Fatalf("err = %v, want a missing-checksum error", err)
	}
	if src.calls[AssetName(rel.Version, "linux", "amd64")] != 0 {
		t.Fatal("the archive was downloaded although nothing vouches for it")
	}
}

func TestFetchBinaryRetriesTransientFailuresWithBackoff(t *testing.T) {
	src := newFakeSource()
	rel, asset := src.publish(t, "NEW-CLIENT")
	src.failures[asset] = 2
	f, sleeps := newTestFetcher(src)
	if _, err := f.FetchBinary(context.Background(), rel, "client", "linux", "amd64", t.TempDir()); err != nil {
		t.Fatalf("FetchBinary() error = %v, want success on the third attempt", err)
	}
	if src.calls[asset] != 3 {
		t.Fatalf("archive fetched %d times, want 3", src.calls[asset])
	}
	if len(*sleeps) != 2 || (*sleeps)[1] <= (*sleeps)[0]/2 {
		t.Fatalf("sleeps = %v, want two growing waits", *sleeps)
	}
}

func TestFetchBinaryGivesUpAfterTheConfiguredAttempts(t *testing.T) {
	src := newFakeSource()
	rel, asset := src.publish(t, "NEW-CLIENT")
	src.failures[asset] = 99
	f, _ := newTestFetcher(src)
	if _, err := f.FetchBinary(context.Background(), rel, "client", "linux", "amd64", t.TempDir()); err == nil {
		t.Fatal("FetchBinary() succeeded although every attempt failed")
	}
	if src.calls[asset] != 3 {
		t.Fatalf("archive fetched %d times, want exactly the 3 configured attempts", src.calls[asset])
	}
}

// A missing release or an oversize file will not change on a second try.
func TestFetchBinaryDoesNotRetryPermanentFailures(t *testing.T) {
	src := newFakeSource()
	rel := Release{Version: semver.Version{Major: 1, Minor: 4}, Tag: "v1.4.0"}
	f, _ := newTestFetcher(src)
	if _, err := f.FetchBinary(context.Background(), rel, "client", "linux", "amd64", t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if src.calls["SHA256SUMS"] != 1 {
		t.Fatalf("SHA256SUMS requested %d times, want 1", src.calls["SHA256SUMS"])
	}
}

func TestFetchBinaryEnforcesTheArchiveSizeLimit(t *testing.T) {
	src := newFakeSource()
	rel, _ := src.publish(t, incompressible(8192))
	f, _ := newTestFetcher(src)
	f.Limits.MaxArchive = 1024
	f.Limits.MaxBinary = 1 << 20
	staging := t.TempDir()
	if _, err := f.FetchBinary(context.Background(), rel, "client", "linux", "amd64", staging); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if entries, _ := os.ReadDir(staging); len(entries) != 0 {
		t.Fatalf("staging holds %v", entries)
	}
}

func TestFetchBinaryStopsWhenTheContextEnds(t *testing.T) {
	src := newFakeSource()
	rel, asset := src.publish(t, "NEW-CLIENT")
	src.failures[asset] = 99
	f, _ := newTestFetcher(src)
	ctx, cancel := context.WithCancel(context.Background())
	f.Sleep = func(context.Context, time.Duration) error { cancel(); return ctx.Err() }
	_, err := f.FetchBinary(ctx, rel, "client", "linux", "amd64", t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if src.calls[asset] != 1 {
		t.Fatalf("archive fetched %d times after cancellation, want 1", src.calls[asset])
	}
}

func TestFetchBinaryForWindowsUsesTheZipAndTheExeName(t *testing.T) {
	src := newFakeSource()
	version := semver.Version{Major: 1, Minor: 4}
	asset := AssetName(version, "windows", "amd64")
	archive, err := os.ReadFile(makeZip(t, archiveEntry{name: "tunnelmesh-client.exe", content: "WIN"}))
	if err != nil {
		t.Fatal(err)
	}
	src.files[asset] = archive
	src.files["SHA256SUMS"] = []byte(sha(archive) + "  " + asset + "\n")
	f, _ := newTestFetcher(src)
	path, err := f.FetchBinary(context.Background(), Release{Version: version, Tag: version.String()}, "client", "windows", "amd64", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "tunnelmesh-client.exe") {
		t.Fatalf("path = %q", path)
	}
	if data, _ := os.ReadFile(path); string(data) != "WIN" {
		t.Fatalf("binary = %q", data)
	}
}

func TestLimitBodyFailsPastTheLimit(t *testing.T) {
	body := LimitBody(io.NopCloser(strings.NewReader(strings.Repeat("x", 100))), 10)
	if _, err := io.ReadAll(body); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadAll error = %v, want ErrTooLarge", err)
	}
	ok := LimitBody(io.NopCloser(strings.NewReader("short")), 10)
	if data, err := io.ReadAll(ok); err != nil || string(data) != "short" {
		t.Fatalf("ReadAll = %q, %v", data, err)
	}
	// Exactly at the limit is allowed.
	exact := LimitBody(io.NopCloser(strings.NewReader(strings.Repeat("x", 10))), 10)
	if _, err := io.ReadAll(exact); err != nil {
		t.Fatalf("a body exactly at the limit failed: %v", err)
	}
}

// incompressible returns bytes gzip cannot shrink, so a size limit is really tested
// rather than defeated by compression.
func incompressible(n int) string {
	b := make([]byte, n)
	state := uint32(2463534242)
	for i := range b {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		b[i] = byte(state)
	}
	return string(b)
}
