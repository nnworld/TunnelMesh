package supervisor

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

// ExecLauncher starts workers as child processes.
type ExecLauncher struct{}

type execChild struct {
	cmd   *exec.Cmd
	stdin interface{ Close() error }
	exit  chan ExitResult
	done  chan struct{}
}

// Start runs spec.Bin with the worker contract variables added to the
// environment. The worker's stdin is a pipe the supervisor keeps open: EOF on it
// means the supervisor is gone, so a worker never outlives a supervisor that was
// killed and keeps holding the client lock.
func (ExecLauncher) Start(_ context.Context, spec Spec) (Child, error) {
	// exec.Command, not CommandContext: stopping goes through Stop, which asks
	// politely first, instead of the context's immediate kill.
	cmd := exec.Command(spec.Bin, spec.Args...)
	env := spec.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = workerEnv(env, spec.ReadyDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	prepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	c := &execChild{cmd: cmd, stdin: stdin, exit: make(chan ExitResult, 1), done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		c.exit <- ExitResult{Code: cmd.ProcessState.ExitCode(), Err: err}
		close(c.done)
	}()
	return c, nil
}

// workerEnv returns base with the worker contract variables replaced, so a
// supervisor that itself inherited them can never pass stale values on.
func workerEnv(base []string, readyDir string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, workerproto.EnvWorker+"=") || strings.HasPrefix(kv, workerproto.EnvReadyDir+"=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, workerproto.EnvWorker+"=1")
	if readyDir != "" {
		out = append(out, workerproto.EnvReadyDir+"="+readyDir)
	}
	return out
}

func (c *execChild) PID() int                { return c.cmd.Process.Pid }
func (c *execChild) Wait() <-chan ExitResult { return c.exit }

// Stop closes the lifeline, asks the worker to terminate, and kills it if it has
// not gone within grace.
func (c *execChild) Stop(grace time.Duration) error {
	_ = c.stdin.Close()
	terminate(c.cmd.Process)
	select {
	case <-c.done:
		return nil
	case <-time.After(grace):
	}
	_ = c.cmd.Process.Kill()
	<-c.done
	return nil
}
