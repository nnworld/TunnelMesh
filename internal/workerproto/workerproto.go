// Package workerproto is the contract between a supervisor and the worker
// process it launches: environment variables, exit codes and the readiness file.
//
// Both sides import this package, so the two can never disagree about a code or
// a file name, and a worker built from an older release keeps meaning the same
// thing to a newer supervisor.
package workerproto

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	// EnvWorker marks a process as a worker. An environment variable is used
	// instead of a flag so a binary that predates this contract does not fail on an
	// unknown argument; it simply runs its own supervisor, never reports ready, and
	// is rolled back by the health window.
	EnvWorker = "TUNNELMESH_WORKER"
	// EnvReadyDir names the per-launch directory the worker writes ReadyFileName to.
	EnvReadyDir = "TUNNELMESH_WORKER_READY_DIR"

	// ReadyFileName is the readiness document inside the ready directory.
	ReadyFileName = "ready.json"
)

// Exit codes. They are a contract with systemd unit files
// (RestartPreventExitStatus) and with operators, so they follow sysexits.h where
// a matching meaning exists.
const (
	ExitOK      = 0
	ExitGeneric = 1
	// ExitSupervisorGaveUp means the supervisor stopped restarting a failing worker
	// and left recovery to the outer service manager.
	ExitSupervisorGaveUp = 70
	// ExitLockHeld means another process holds the client lock. It is retryable,
	// because it is normal while the previous worker is still shutting down.
	ExitLockHeld = 75
	// ExitAuthFatal means the credentials were rejected for good; retrying cannot help.
	ExitAuthFatal = 77
	// ExitConfig means the configuration is invalid or not accepted by this binary.
	ExitConfig = 78
)

// ReadyInfo is what a worker publishes once it is connected and the Server has
// acknowledged it.
type ReadyInfo struct {
	PID     int    `json:"pid"`
	Version string `json:"version"`
	// ServerVersion is empty when the Server is too old to announce it.
	ServerVersion string    `json:"server_version,omitempty"`
	ReadyAt       time.Time `json:"ready_at"`
}

// WriteReady publishes info atomically. The supervisor polls the file while the
// worker may rewrite it after a reconnect, so a reader must see the old or the new
// document, never a partial one.
func WriteReady(dir string, info ReadyInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".ready-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, ReadyFileName))
}

// ReadReady returns the published document. A missing file is not an error: it
// means the worker is not ready yet.
func ReadReady(dir string) (ReadyInfo, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, ReadyFileName))
	if errors.Is(err, os.ErrNotExist) {
		return ReadyInfo{}, false, nil
	}
	if err != nil {
		return ReadyInfo{}, false, err
	}
	var info ReadyInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return ReadyInfo{}, false, err
	}
	return info, true, nil
}

// ExitError carries a process exit code through the error chain so `main` can
// exit with it instead of the blanket 1 every failure used to produce.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return "exit " + strconv.Itoa(e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// ExitCodeOf maps an error to a process exit code: 0 for nil, the code of the
// first ExitError in the chain, and ExitGeneric for anything else.
func ExitCodeOf(err error) int {
	if err == nil {
		return ExitOK
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}
	return ExitGeneric
}
