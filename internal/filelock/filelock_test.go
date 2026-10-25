package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireIsExclusiveUntilReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "x.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire error = %v, want ErrLocked", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	_ = second.Release()
}

func TestReleaseIsIdempotent(t *testing.T) {
	l, err := Acquire(filepath.Join(t.TempDir(), "x.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

// A lock file left behind by a process that died must not block the next start,
// which is why the kernel lock is used instead of a sentinel file.
func TestAStaleLockFileDoesNotBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire over a stale file: %v", err)
	}
	_ = l.Release()
}

func TestAcquireCreatesMissingDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "x.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Release()
}
