//go:build windows

package client

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows byte-range locking lives in kernel32 and has no Go wrapper, so it is reached
// through a lazy import: the tray and the CLI both start on machines where the loader
// resolves this at first use rather than at link time.
var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")

	// exclusiveLock combines the two flags that make the call a mutex probe: take the
	// whole region exclusively and return at once instead of queueing.
	exclusiveLock = uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)

	// errLockViolation is what LockFileEx reports when another handle already owns the
	// region, and the only failure that means "a client is running" rather than "the
	// file system refused us".
	errLockViolation = syscall.Errno(windows.ERROR_LOCK_VIOLATION)
)

// windowsLock holds an exclusive region lock for the lifetime of an open handle.
//
// LockFileEx is used rather than an O_EXCL sentinel file for the same reason flock is on
// Unix: the kernel releases the lock when the handle closes, including after a crash, so
// a stale file left by a killed process cannot block every later start until somebody
// deletes it by hand.
type windowsLock struct {
	file     *os.File
	release  sync.Once
	released error
}

func acquireRuntimeLock(path string) (runtimeLock, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	// Opening rather than creating exclusively is deliberate: a second process must be
	// refused by the lock, so the error it reports names the other client instead of a
	// sharing violation. The file sits next to a configuration that may carry a token,
	// so it keeps the same restrictive permission as on Unix.
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFileRegion(file.Fd()); err != nil {
		_ = file.Close()
		if errors.Is(err, errLockViolation) {
			return nil, ErrRuntimeAlreadyRunning
		}
		return nil, err
	}
	// Recording the holder is best effort and purely diagnostic: it lets an operator who
	// finds a contested lock identify the process without a debugger.
	if err := file.Truncate(0); err == nil {
		_, _ = file.WriteString(strconv.Itoa(os.Getpid()))
	}
	return &windowsLock{file: file}, nil
}

// lockFileRegion takes a one-byte exclusive lock at offset zero, failing immediately when
// another handle already holds it.
func lockFileRegion(fd uintptr) error {
	var overlapped windows.Overlapped
	r1, _, err := procLockFileEx.Call(fd, uintptr(exclusiveLock), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if r1 != 0 {
		return nil
	}
	return err
}

// Release drops the region before closing, so a later acquire in the same process cannot
// observe a lock still held by a descriptor that is being torn down.
func (l *windowsLock) Release() error {
	l.release.Do(func() {
		var overlapped windows.Overlapped
		_, _, _ = procUnlockFileEx.Call(l.file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
		l.released = l.file.Close()
	})
	return l.released
}
