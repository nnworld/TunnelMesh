//go:build unix

package supervisor

import (
	"os"
	"os/exec"
	"syscall"
)

// prepareCommand puts the worker in its own process group so a Ctrl-C aimed at
// the supervisor's terminal does not reach it directly; the supervisor decides
// when and how the worker is stopped.
func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate asks the worker to shut down gracefully.
func terminate(p *os.Process) { _ = p.Signal(syscall.SIGTERM) }
