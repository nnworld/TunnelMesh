package client

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
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
//
// The lock prefers the client state directory: the service unit mounts /etc read-only,
// and a lock beside the configuration failed every start with EROFS. A process that
// cannot create the state directory (a non-root user, a test runner) falls back to the
// configuration directory, so it can still start.
func LockPathForConfig(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		return ""
	}
	return lockPathWithFallback(clientStateRoot(configPath), configPath)
}

// lockPathWithFallback returns the state-root lock when its directory can be created,
// and the configuration-directory lock otherwise. Creating the directory here keeps the
// answer honest: the path returned is one acquireRuntimeLock can actually open.
func lockPathWithFallback(stateRoot, configPath string) string {
	primary := lockPathIn(stateRoot, configPath)
	if err := os.MkdirAll(filepath.Dir(primary), 0o750); err == nil {
		return primary
	}
	return filepath.Join(filepath.Dir(configPath), LockFileName)
}

// clientStateRoot is the directory a client keeps its restart-surviving files in. It
// is the directory of the default instance id, which the service unit and the
// installer already make writable.
func clientStateRoot(configPath string) string {
	return filepath.Dir(DefaultClientInstanceIDPathForConfig(configPath))
}

// lockPathIn places one configuration's lock under a state root. The configuration
// path is hashed so every configuration gets its own directory without the state
// root having to know any file names; the absolute path is hashed so the same file
// always maps to the same lock regardless of how it was spelled.
func lockPathIn(stateRoot, configPath string) string {
	trimmed := strings.TrimSpace(configPath)
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		abs = filepath.Clean(trimmed)
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(stateRoot, "locks", hex.EncodeToString(sum[:8]), LockFileName)
}

// runtimeLock is a held advisory lock. Release is idempotent because both an
// explicit Stop and a deferred cleanup can reach it.
type runtimeLock interface {
	Release() error
}
