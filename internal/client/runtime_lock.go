package client

import (
	"errors"
	"path/filepath"
	"strings"
)

// LockFileName is the advisory lock that makes two clients sharing one
// configuration file mutually exclusive.
const LockFileName = "client.lock"

// ErrRuntimeAlreadyRunning reports that another TunnelMesh client already holds the
// lock for the same configuration.
//
// The message names the situation rather than the mechanism: an operator who sees
// it needs to know a second client is running, not that flock returned EWOULDBLOCK.
var ErrRuntimeAlreadyRunning = errors.New("client: another TunnelMesh client is already running with this configuration")

// LockPathForConfig derives the advisory lock path from a configuration file path.
//
// Deriving it instead of fixing one global path is what makes the mutex mean the
// right thing: the tray and `client run` conflict exactly when they drive the same
// configuration, and stay independent when an operator deliberately runs two clients
// from two files. An empty path means there is no configuration file to coordinate
// on, so no lock is taken.
func LockPathForConfig(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), LockFileName)
}

// runtimeLock is a held advisory lock. Release is idempotent because both an
// explicit Stop and a deferred cleanup can reach it.
type runtimeLock interface {
	Release() error
}
