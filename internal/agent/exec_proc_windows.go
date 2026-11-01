//go:build windows

package agent

import "os/exec"

// Windows has no process groups in this implementation. A timeout ends the shell
// process only; grandchildren it started are not terminated. This gap is recorded in
// ADR 0005 and must be closed with a job object before the feature is enabled there.
func configureProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func exitSignaled(*exec.ExitError) bool { return false }
