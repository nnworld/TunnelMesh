package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ErrChecksumMismatch means a downloaded archive does not match its SHA256SUMS entry.
var ErrChecksumMismatch = errors.New("selfupdate: archive does not match its SHA256SUMS entry")

// Limits bound what one upgrade may download and how hard it tries.
type Limits struct {
	MaxArchive int64
	MaxBinary  int64
	MaxSums    int64
	Retries    int
	BaseDelay  time.Duration
}

// DefaultLimits are generous for a Go binary (tens of MiB) yet small enough that a
// hostile source cannot fill a disk.
func DefaultLimits() Limits {
	return Limits{MaxArchive: 256 << 20, MaxBinary: 200 << 20, MaxSums: 1 << 20, Retries: 3, BaseDelay: 2 * time.Second}
}

// Fetcher downloads, verifies and unpacks a release binary.
type Fetcher struct {
	Source Source
	Limits Limits
	// Sleep waits between attempts; tests replace it so they do not sleep.
	Sleep func(ctx context.Context, d time.Duration) error
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// FetchBinary downloads the role's archive for the platform, checks it against the
// release's SHA256SUMS, and extracts only the role binary into stagingDir. It
// returns the binary's path. On any failure nothing is left in stagingDir.
//
// The checksum is fetched first and the archive is not downloaded at all unless
// the checksum file lists it, so nothing is spent on a file nothing vouches for.
func (f *Fetcher) FetchBinary(ctx context.Context, rel Release, role, goos, goarch, stagingDir string) (string, error) {
	asset := AssetName(rel.Version, goos, goarch)
	binaryName := "tunnelmesh-" + role
	kind := ArchiveTarGz
	if goos == "windows" {
		binaryName += ".exe"
		kind = ArchiveZip
	}

	var want string
	if err := f.retry(ctx, func() error {
		sums, err := f.fetchSums(ctx, rel)
		if err != nil {
			return err
		}
		digest, ok := sums[asset]
		if !ok {
			return permanent(fmt.Errorf("selfupdate: %s does not list %s", ChecksumsAsset, asset))
		}
		want = digest
		return nil
	}); err != nil {
		return "", err
	}

	archivePath := filepath.Join(stagingDir, asset)
	binaryPath := filepath.Join(stagingDir, binaryName)
	cleanup := func() {
		_ = os.Remove(archivePath)
		_ = os.Remove(binaryPath)
	}
	if err := f.retry(ctx, func() error {
		_ = os.Remove(archivePath)
		got, err := f.download(ctx, rel, asset, archivePath, f.Limits.MaxArchive)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%w: got %s, want %s", ErrChecksumMismatch, got, want)
		}
		return nil
	}); err != nil {
		cleanup()
		return "", err
	}
	if err := ExtractOne(archivePath, kind, binaryName, binaryPath, f.Limits.MaxBinary); err != nil {
		cleanup()
		return "", err
	}
	// The archive has served its purpose; only the verified binary stays.
	_ = os.Remove(archivePath)
	return binaryPath, nil
}

func (f *Fetcher) fetchSums(ctx context.Context, rel Release) (map[string]string, error) {
	body, err := f.Source.Fetch(ctx, rel, ChecksumsAsset, f.Limits.MaxSums)
	if err != nil {
		return nil, classify(err)
	}
	defer body.Close()
	return ParseSHA256SUMS(body)
}

// download streams one asset to path and returns the SHA-256 of what was written.
func (f *Fetcher) download(ctx context.Context, rel Release, asset, path string, max int64) (string, error) {
	body, err := f.Source.Fetch(ctx, rel, asset, max)
	if err != nil {
		return "", classify(err)
	}
	defer body.Close()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", permanent(err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, hash), body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return "", classify(copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", permanent(closeErr)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func permanent(err error) error { return permanentError{err} }

// classify marks the failures that a retry cannot fix.
func classify(err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrTooLarge) {
		return permanent(err)
	}
	return err
}

// retry runs op up to Limits.Retries times with a growing wait, stopping early
// for permanent errors and when ctx ends.
func (f *Fetcher) retry(ctx context.Context, op func() error) error {
	attempts := f.Limits.Retries
	if attempts < 1 {
		attempts = 1
	}
	sleep := f.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	delay := f.Limits.BaseDelay
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = op(); err == nil {
			return nil
		}
		var perm permanentError
		if errors.As(err, &perm) || attempt == attempts {
			break
		}
		if sleepErr := sleep(ctx, delay); sleepErr != nil {
			return sleepErr
		}
		delay *= 2
	}
	var perm permanentError
	if errors.As(err, &perm) {
		return perm.err
	}
	return err
}
