//go:build unix

package agent

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup starts the script in its own process group so a timeout can
// end the shell and every child it started, not only the shell itself.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func exitSignaled(err *exec.ExitError) bool {
	status, ok := err.Sys().(syscall.WaitStatus)
	return ok && status.Signaled()
}
