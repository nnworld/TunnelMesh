package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/semver"
	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

// ExecPreflight proves a downloaded binary works by running it: it must report the
// version it was published as, and it must accept the configuration this node
// runs with. Checking the configuration with the new binary is what catches the
// typical upgrade failure, a new version that rejects an existing config, before
// the worker is ever switched.
type ExecPreflight struct {
	// Args are the supervisor's own arguments (for example `--config X run`); the
	// check-config invocation is derived from them.
	Args []string
	// Env is the environment the worker gets, which carries the tokens and the
	// TUNNELMESH_* overrides check-config must see. The worker marker is removed.
	Env []string
	// Timeout bounds each execution; zero means 30s.
	Timeout time.Duration
}

// maxPreflightOutput bounds how much of a failing binary's output is kept for the
// error message.
const maxPreflightOutput = 2048

// Check implements Preflighter.
func (p ExecPreflight) Check(ctx context.Context, bin, target string) error {
	out, err := p.run(ctx, bin, "--version")
	if err != nil {
		return fmt.Errorf("--version: %w", err)
	}
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return fmt.Errorf("--version printed %q, want \"<name> <version> ...\"", out)
	}
	reported, err := semver.Parse(fields[1])
	wanted, wantErr := semver.Parse(target)
	if wantErr != nil || err != nil || reported != wanted {
		return fmt.Errorf("the binary reports version %q, but %s was published", fields[1], target)
	}
	args, err := checkConfigArgs(p.Args)
	if err != nil {
		return err
	}
	if _, err := p.run(ctx, bin, args...); err != nil {
		return fmt.Errorf("check-config: %w", err)
	}
	return nil
}

// run executes bin and returns its combined output. A failure to start (a file
// the antivirus is still scanning, say) is retried a few times; a binary that
// started and failed is not.
func (p ExecPreflight) run(ctx context.Context, bin string, args ...string) (string, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		var buf bytes.Buffer
		cmd := exec.CommandContext(runCtx, bin, args...)
		cmd.Env = withoutWorkerMarker(p.Env)
		cmd.Stdout, cmd.Stderr = &buf, &buf
		err := cmd.Run()
		cancel()
		out := strings.TrimSpace(buf.String())
		if len(out) > maxPreflightOutput {
			out = out[:maxPreflightOutput] + "…"
		}
		if err == nil {
			return out, nil
		}
		lastErr = fmt.Errorf("%w: %s", err, out)
		var started *exec.ExitError
		if errors.As(err, &started) || ctx.Err() != nil || runCtx.Err() != nil {
			return out, lastErr
		}
		select {
		case <-ctx.Done():
			return out, lastErr
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", lastErr
}

func withoutWorkerMarker(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, workerproto.EnvWorker+"=") || strings.HasPrefix(kv, workerproto.EnvReadyDir+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// checkConfigArgs turns the supervisor's `... run ...` arguments into the same
// invocation of `check-config`, so the new binary validates exactly the
// configuration the worker would use.
func checkConfigArgs(args []string) ([]string, error) {
	for i, arg := range args {
		if arg == "run" {
			out := append([]string(nil), args...)
			out[i] = "check-config"
			return out, nil
		}
	}
	return nil, fmt.Errorf("cannot derive a check-config invocation from the arguments %v", args)
}
