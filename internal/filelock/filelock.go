// Package filelock provides a cross-platform advisory lock held for the lifetime
// of an open file handle.
//
// The kernel releases the lock when the handle closes, including after a crash or
// SIGKILL, so a lock file left behind by a dead process never blocks the next
// start. A sentinel file created with O_EXCL would, until someone deleted it by
// hand.
package filelock

import "errors"

// ErrLocked means another holder owns the lock.
var ErrLocked = errors.New("filelock: already locked")

// Lock is a held lock.
type Lock interface {
	// Release drops the lock. It is safe to call more than once.
	Release() error
}
