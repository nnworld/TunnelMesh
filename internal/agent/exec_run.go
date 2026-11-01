package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// ExecConfig bounds script execution on this agent. The zero value is not usable on
// its own: withDefaults fills every bound, and Enabled must be set on purpose.
type ExecConfig struct {
	Enabled        bool
	Shell          string
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	MaxOutputBytes int
	MaxScriptBytes int
	MaxConcurrent  int
}

const (
	defaultExecTimeout     = 60 * time.Second
	defaultExecMaxTimeout  = 10 * time.Minute
	defaultExecOutputBytes = 1 << 20
	defaultExecScriptBytes = 64 << 10
	defaultExecConcurrent  = 2
)

func (c ExecConfig) withDefaults() ExecConfig {
	if c.DefaultTimeout <= 0 {
		c.DefaultTimeout = defaultExecTimeout
	}
	if c.MaxTimeout <= 0 {
		c.MaxTimeout = defaultExecMaxTimeout
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = defaultExecOutputBytes
	}
	if c.MaxScriptBytes <= 0 {
		c.MaxScriptBytes = defaultExecScriptBytes
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = defaultExecConcurrent
	}
	return c
}

var (
	ErrExecDisabled       = errors.New("agent: script execution is disabled on this agent")
	ErrExecBusy           = errors.New("agent: script execution concurrency limit reached")
	ErrExecScriptTooLarge = errors.New("agent: script exceeds the configured size limit")
)

// ExecResult describes how a run ended. It never contains script text or output.
type ExecResult struct {
	ExitCode  int
	Signaled  bool
	TimedOut  bool
	Truncated bool
	Duration  time.Duration
}

// execRunner runs scripts. Its concurrency is a fixed set of slots, so a full agent
// refuses new work immediately instead of queueing it behind a stuck script.
type execRunner struct {
	cfg   ExecConfig
	slots chan struct{}
}

func newExecRunner(cfg ExecConfig) *execRunner {
	cfg = cfg.withDefaults()
	return &execRunner{cfg: cfg, slots: make(chan struct{}, cfg.MaxConcurrent)}
}

// Config returns the effective bounds, for advertising and for tests.
func (r *execRunner) Config() ExecConfig { return r.cfg }

// Run executes script under timeout and copies its output to stdout and stderr,
// both capped together at MaxOutputBytes. The script travels on the child's standard
// input, never in its argument list, so it is absent from the process table.
func (r *execRunner) Run(ctx context.Context, script []byte, timeout time.Duration, stdout, stderr io.Writer) (ExecResult, error) {
	if r == nil || !r.cfg.Enabled {
		return ExecResult{}, ErrExecDisabled
	}
	if len(script) > r.cfg.MaxScriptBytes {
		return ExecResult{}, ErrExecScriptTooLarge
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		return ExecResult{}, ErrExecBusy
	}
	if timeout <= 0 {
		timeout = r.cfg.DefaultTimeout
	}
	if timeout > r.cfg.MaxTimeout {
		timeout = r.cfg.MaxTimeout
	}
	workDir, err := os.MkdirTemp("", "tm-exec-")
	if err != nil {
		return ExecResult{}, err
	}
	defer os.RemoveAll(workDir)

	shell, args := r.shellCommand()
	cmd := exec.Command(shell, args...)
	cmd.Dir = workDir
	cmd.Env = execEnvironment(workDir)
	cmd.Stdin = bytes.NewReader(script)
	limit := &outputLimit{remaining: r.cfg.MaxOutputBytes}
	cmd.Stdout = limit.writer(stdout)
	cmd.Stderr = limit.writer(stderr)
	configureProcessGroup(cmd)

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return ExecResult{}, err
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var result ExecResult
	select {
	case err := <-waited:
		result = resultFromWait(err)
	case <-timer.C:
		result.TimedOut = true
		killProcessGroup(cmd)
		<-waited
	case <-ctx.Done():
		killProcessGroup(cmd)
		<-waited
		return ExecResult{}, ctx.Err()
	}
	result.Duration = time.Since(started)
	result.Truncated = limit.truncated()
	// The local record keeps the facts an operator needs to correlate with the server
	// audit: a digest of the script and the outcome. Neither the script nor its output
	// is written here.
	digest := sha256.Sum256(script)
	slog.Info("agent_exec_finished",
		"script_sha256", hex.EncodeToString(digest[:]),
		"script_bytes", len(script),
		"exit_code", result.ExitCode,
		"signaled", result.Signaled,
		"timed_out", result.TimedOut,
		"truncated", result.Truncated,
		"duration_ms", result.Duration.Milliseconds())
	return result, nil
}

func (r *execRunner) shellCommand() (string, []string) {
	if r.cfg.Shell != "" {
		return r.cfg.Shell, nil
	}
	if runtime.GOOS == "windows" {
		return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", "-"}
	}
	return "/bin/sh", []string{"-s"}
}

// execEnvironment builds the child's environment from scratch. Nothing is copied from
// the agent process, so its token and other secrets cannot reach the script.
func execEnvironment(workDir string) []string {
	if runtime.GOOS == "windows" {
		return []string{
			"PATH=" + os.Getenv("PATH"),
			"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
			"TEMP=" + workDir,
			"TMP=" + workDir,
			"USERPROFILE=" + workDir,
		}
	}
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + workDir,
		"TMPDIR=" + workDir,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"SHELL=/bin/sh",
	}
}

// resultFromWait maps the wait error onto the exit code and signal, which are the
// two facts the operator needs to tell "failed" from "was killed".
func resultFromWait(err error) ExecResult {
	if err == nil {
		return ExecResult{ExitCode: 0}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ExecResult{ExitCode: exitErr.ExitCode(), Signaled: exitSignaled(exitErr)}
	}
	return ExecResult{ExitCode: -1}
}

// outputLimit enforces one byte budget shared by stdout and stderr. Once it is spent,
// further output is discarded and the run is marked truncated; the process keeps
// running so the caller still learns its exit status.
type outputLimit struct {
	mu        sync.Mutex
	remaining int
	dropped   bool
}

func (l *outputLimit) writer(dst io.Writer) io.Writer {
	return writerFunc(func(p []byte) (int, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		n := len(p)
		if l.remaining <= 0 {
			l.dropped = true
			return n, nil
		}
		if len(p) > l.remaining {
			p = p[:l.remaining]
			l.dropped = true
		}
		l.remaining -= len(p)
		if _, err := dst.Write(p); err != nil {
			return 0, err
		}
		return n, nil
	})
}

func (l *outputLimit) truncated() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// ExecConfigFromSettings maps the operator's configuration onto the runner's bounds.
func ExecConfigFromSettings(settings config.AgentExecConfig) ExecConfig {
	return ExecConfig{
		Enabled:        settings.Enabled,
		Shell:          settings.Shell,
		DefaultTimeout: settings.DefaultTimeout,
		MaxTimeout:     settings.MaxTimeout,
		MaxOutputBytes: settings.MaxOutputBytes,
		MaxScriptBytes: settings.MaxScriptBytes,
		MaxConcurrent:  settings.MaxConcurrent,
	}
}
