//go:build unix

package client

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// flockLock holds an advisory lock for the lifetime of an open file descriptor.
//
// flock is used rather than an O_EXCL sentinel file because the kernel drops the
// lock when the descriptor closes, including on a crash or SIGKILL. A sentinel file
// would survive the process that created it and block every later start until
// someone noticed and deleted it by hand.
type flockLock struct {
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
	// The lock file carries no secrets, but it does live next to a configuration
	// that may, so it inherits the same restrictive permission.
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrRuntimeAlreadyRunning
		}
		return nil, err
	}
	// Recording the holder is best effort and purely diagnostic: it lets an operator
	// who finds a contested lock identify the process without a debugger.
	if err := file.Truncate(0); err == nil {
		_, _ = file.WriteString(strconv.Itoa(os.Getpid()))
	}
	return &flockLock{file: file}, nil
}

func (l *flockLock) Release() error {
	l.release.Do(func() {
		// Explicitly unlock before closing so a later acquire in the same process
		// cannot observe a lock still held by the descriptor being torn down.
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		l.released = l.file.Close()
	})
	return l.released
}
