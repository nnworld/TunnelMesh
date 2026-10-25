package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

// A supervisor decides whether to restart a worker from its exit code, so a
// config the binary rejects must be told apart from an ordinary crash.
func TestRunWithUnreadableConfigExitsWithConfigCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("mode: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, root := range map[string]func() []string{
		"agent":  func() []string { return []string{"--config", path, "run"} },
		"client": func() []string { return []string{"--config", path, "run"} },
	} {
		t.Run(name, func(t *testing.T) {
			cmd := NewAgentRoot()
			if name == "client" {
				cmd = NewClientRoot()
			}
			cmd.SetArgs(root())
			cmd.SetOut(os.Stderr)
			cmd.SetErr(os.Stderr)
			err := Execute(context.Background(), cmd)
			if err == nil {
				t.Fatal("run accepted a malformed config")
			}
			if got := workerproto.ExitCodeOf(err); got != workerproto.ExitConfig {
				t.Fatalf("exit code = %d, want %d (config error); err = %v", got, workerproto.ExitConfig, err)
			}
		})
	}
}

func TestClientRunWithoutServerURLExitsWithConfigCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.yaml")
	if err := os.WriteFile(path, []byte("mode: local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := NewClientRoot()
	cmd.SetArgs([]string{"--config", path, "run"})
	cmd.SetOut(os.Stderr)
	cmd.SetErr(os.Stderr)
	err := Execute(context.Background(), cmd)
	if got := workerproto.ExitCodeOf(err); got != workerproto.ExitConfig {
		t.Fatalf("exit code = %d, want %d; err = %v", got, workerproto.ExitConfig, err)
	}
}

func TestClassifyRuntimeStartError(t *testing.T) {
	// A held lock is retryable: it is normal while the previous worker is still
	// shutting down.
	if got := workerproto.ExitCodeOf(classifyStartError(fmt.Errorf("start: %w", client.ErrRuntimeAlreadyRunning))); got != workerproto.ExitLockHeld {
		t.Fatalf("lock conflict exit code = %d, want %d", got, workerproto.ExitLockHeld)
	}
	other := errors.New("listen tcp 127.0.0.1:1: address already in use")
	if got := workerproto.ExitCodeOf(classifyStartError(other)); got != workerproto.ExitGeneric {
		t.Fatalf("ordinary start failure exit code = %d, want %d", got, workerproto.ExitGeneric)
	}
	if classifyStartError(nil) != nil {
		t.Fatal("classifyStartError(nil) must stay nil")
	}
}

// Being asked to stop is not a failure: SIGTERM from systemd or the supervisor
// must not turn into a non-zero exit that triggers a restart.
func TestExitCodeFor(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()
	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want int
	}{
		{"success", live, nil, 0},
		{"cancelled by signal", cancelled, context.Canceled, 0},
		{"cancelled error but not asked to stop", live, context.Canceled, workerproto.ExitGeneric},
		{"config error", live, &workerproto.ExitError{Code: workerproto.ExitConfig, Err: errors.New("bad")}, workerproto.ExitConfig},
		{"plain error while stopping", cancelled, errors.New("boom"), workerproto.ExitGeneric},
	}
	for _, tc := range cases {
		if got := ExitCodeFor(tc.ctx, tc.err); got != tc.want {
			t.Errorf("%s: ExitCodeFor() = %d, want %d", tc.name, got, tc.want)
		}
	}
}
