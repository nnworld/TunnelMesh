package server

import (
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/build"
)

// resolveServerVersion is the version this Server reports to clients and agents in
// their metadata acks. They cap their self-upgrade at it, so a peer never runs
// ahead of the Server it talks to.
//
// configured comes from RuntimeConfig.Version and exists so a test can run a Server
// that claims a version without rewriting the process-wide build variables, which
// other goroutines read. Unset means the build's own version; a development build
// reports "dev", which a peer reads as "not a release" and never upgrades towards.
func resolveServerVersion(configured string) string {
	if v := strings.TrimSpace(configured); v != "" {
		return v
	}
	return build.Current().Version
}
