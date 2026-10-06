//go:build !unix

package client

// acquireRuntimeLock is a documented no-op on platforms without an advisory lock
// implementation yet.
//
// Returning an error here would break `client run` on Windows, which works today.
// Skipping the lock degrades only the mutual-exclusion guarantee, and the listeners
// still provide a hard backstop: a second runtime cannot bind an address the first
// one already holds, so it fails with a clear address-in-use error instead of
// silently double-forwarding.
func acquireRuntimeLock(path string) (runtimeLock, error) {
	return nil, nil
}
