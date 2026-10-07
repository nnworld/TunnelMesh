//go:build !unix && !windows

package client

// acquireRuntimeLock is a documented no-op on platforms that have no advisory lock
// implementation, which after the macOS and Windows trays is only the BSDs and plan9.
//
// Returning an error here would break `client run` on those platforms, which works
// today. Skipping the lock degrades only the mutual-exclusion guarantee, and the
// listeners still provide a hard backstop: a second runtime cannot bind an address the
// first one already holds, so it fails with a clear address-in-use error instead of
// silently double-forwarding.
func acquireRuntimeLock(path string) (runtimeLock, error) {
	return nil, nil
}
