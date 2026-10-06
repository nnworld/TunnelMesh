package client

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLockPathForConfigDerivesFromConfigDir(t *testing.T) {
	if got := LockPathForConfig(""); got != "" {
		t.Fatalf("empty config path must not produce a lock path, got %q", got)
	}
	if got := LockPathForConfig("   "); got != "" {
		t.Fatalf("blank config path must not produce a lock path, got %q", got)
	}
	got := LockPathForConfig(filepath.Join(t.TempDir(), "nested", "client.yaml"))
	if want := filepath.Join(filepath.Dir(got), LockFileName); got != want {
		t.Fatalf("lock path = %q, want %q", got, want)
	}
	if filepath.Base(got) != LockFileName {
		t.Fatalf("lock path = %q, want base %q", got, LockFileName)
	}
}

// TestAcquireRuntimeLockIsMutuallyExclusive pins the guarantee the tray and
// `client run` depend on: two runtimes sharing one configuration file cannot both
// hold the lock, and releasing it hands it to the next caller.
func TestAcquireRuntimeLockIsMutuallyExclusive(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("advisory locking is only implemented on Unix")
	}
	path := filepath.Join(t.TempDir(), "client.lock")

	first, err := acquireRuntimeLock(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if first == nil {
		t.Fatal("first acquire returned a nil lock")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("lock file was not created: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("lock file permissions = %v, want no group or world access", info.Mode().Perm())
	}

	if _, err := acquireRuntimeLock(path); !errors.Is(err, ErrRuntimeAlreadyRunning) {
		t.Fatalf("second acquire error = %v, want ErrRuntimeAlreadyRunning", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	second, err := acquireRuntimeLock(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	defer func() { _ = second.Release() }()

	// Release must be idempotent: Stop and a deferred cleanup can both call it.
	if err := second.Release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("repeat release: %v", err)
	}
}

// TestAcquireRuntimeLockEmptyPathIsNoop keeps callers that have no configuration
// file (one-shot forwards, in-memory configs) working without a lock.
func TestAcquireRuntimeLockEmptyPathIsNoop(t *testing.T) {
	lock, err := acquireRuntimeLock("")
	if err != nil {
		t.Fatalf("empty path acquire: %v", err)
	}
	if lock != nil {
		t.Fatal("empty path must not produce a lock")
	}
}

// TestAcquireRuntimeLockIsIndependentPerConfig documents the intended scope of the
// mutex: it coordinates runtimes that share a configuration, not every client on
// the host. An operator running two clients from two files is a supported setup.
func TestAcquireRuntimeLockIsIndependentPerConfig(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("advisory locking is only implemented on Unix")
	}
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "a", "client.yaml")
	secondPath := filepath.Join(dir, "b", "client.yaml")

	first, err := acquireRuntimeLock(LockPathForConfig(firstPath))
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = first.Release() }()

	second, err := acquireRuntimeLock(LockPathForConfig(secondPath))
	if err != nil {
		t.Fatalf("second config acquire: %v", err)
	}
	defer func() { _ = second.Release() }()
}
