//go:build windows

package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

type winLock struct {
	file     *os.File
	once     sync.Once
	released error
}

// Acquire takes an exclusive region lock without waiting. LockFileEx is released
// by the kernel when the handle closes, so a crash leaves nothing behind.
func Acquire(path string) (Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.Errno(windows.ERROR_LOCK_VIOLATION)) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &winLock{file: file}, nil
}

func (l *winLock) Release() error {
	l.once.Do(func() {
		overlapped := new(windows.Overlapped)
		_ = windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, overlapped)
		l.released = l.file.Close()
	})
	return l.released
}
