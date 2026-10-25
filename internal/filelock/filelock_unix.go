//go:build unix

package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type flock struct {
	file     *os.File
	once     sync.Once
	released error
}

// Acquire takes the lock at path without waiting, creating the file and its
// directories when needed.
func Acquire(path string) (Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &flock{file: file}, nil
}

func (l *flock) Release() error {
	l.once.Do(func() {
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		l.released = l.file.Close()
	})
	return l.released
}
