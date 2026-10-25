//go:build windows

package supervisor

import (
	"os"
	"os/exec"
	"syscall"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrlE = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

const ctrlBreakEvent = 1

// prepareCommand gives the worker its own process group, which is what lets a
// CTRL_BREAK event be aimed at it alone.
func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// terminate sends CTRL_BREAK to the worker's process group, which Go delivers as
// os.Interrupt. Under a service there is no console and this fails; the closed
// stdin lifeline and the kill after the grace period then do the job.
func terminate(p *os.Process) {
	_, _, _ = procGenerateConsoleCtrlE.Call(uintptr(ctrlBreakEvent), uintptr(p.Pid))
}
