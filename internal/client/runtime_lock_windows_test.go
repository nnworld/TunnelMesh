//go:build windows

package client

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestAcquireRuntimeLockRejectsASecondProcessOnWindows is the Windows half of the
// guarantee the tray and `client run` rely on. It runs the current process twice so
// the contention is real: a lock that only survives within one process would let the
// tray and the CLI both start.
func TestAcquireRuntimeLockRejectsASecondProcessOnWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.lock")
	lock, err := acquireRuntimeLock(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if lock == nil {
		t.Fatal("first acquire returned a nil lock")
	}
	defer func() { _ = lock.Release() }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("lock file was not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("lock file carries no pid, so a contested lock cannot be diagnosed")
	}

	// LockFileEx is per-handle, so a second handle in this process must also be refused:
	// that is what keeps a tray that restarted its runtime from double-binding.
	if _, err := acquireRuntimeLock(path); !errors.Is(err, ErrRuntimeAlreadyRunning) {
		t.Fatalf("second acquire error = %v, want ErrRuntimeAlreadyRunning", err)
	}
}

// TestReleaseRuntimeLockOnWindowsFreesTheFile mirrors the Unix case: release is
// idempotent because Stop and a deferred cleanup both reach it.
func TestReleaseRuntimeLockOnWindowsFreesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.lock")
	lock, err := acquireRuntimeLock(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("repeat release: %v", err)
	}
	again, err := acquireRuntimeLock(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = again.Release()
}
