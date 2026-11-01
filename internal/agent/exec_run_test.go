package agent

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipWithoutPOSIXShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("script execution tests use a POSIX shell")
	}
}

func testExecRunner(cfg ExecConfig) *execRunner {
	cfg.Enabled = true
	return newExecRunner(cfg)
}

func TestExecRunSeparatesStdoutStderrAndExitCode(t *testing.T) {
	skipWithoutPOSIXShell(t)
	var stdout, stderr bytes.Buffer
	result, err := testExecRunner(ExecConfig{}).Run(context.Background(), []byte("echo out; echo err 1>&2; exit 3"), 10*time.Second, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode != 3 || result.TimedOut || result.Truncated {
		t.Fatalf("result = %+v", result)
	}
	if strings.TrimSpace(stdout.String()) != "out" || strings.TrimSpace(stderr.String()) != "err" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// The script runs in a child process with a fixed, small environment. A secret the
// agent holds in its own environment must never be visible to the script.
func TestExecRunDoesNotInheritAgentEnvironment(t *testing.T) {
	skipWithoutPOSIXShell(t)
	t.Setenv("TUNNELMESH_AGENT_TOKEN", "tm_secret_do_not_leak")
	var stdout bytes.Buffer
	if _, err := testExecRunner(ExecConfig{}).Run(context.Background(), []byte("env"), 10*time.Second, &stdout, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(stdout.String(), "tm_secret_do_not_leak") || strings.Contains(stdout.String(), "TUNNELMESH_AGENT_TOKEN") {
		t.Fatalf("script saw the agent environment:\n%s", stdout.String())
	}
}

// A timeout must end the whole process group, including background children that
// would otherwise keep running after the shell exits.
func TestExecRunTimeoutKillsTheProcessGroup(t *testing.T) {
	skipWithoutPOSIXShell(t)
	start := time.Now()
	var out bytes.Buffer
	result, err := testExecRunner(ExecConfig{}).Run(context.Background(), []byte("sleep 30 & sleep 30; wait"), 300*time.Millisecond, &out, &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.TimedOut {
		t.Fatalf("result = %+v, want TimedOut", result)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took %s; children survived the group kill", elapsed)
	}
}

func TestExecRunStopsAtTheOutputCap(t *testing.T) {
	skipWithoutPOSIXShell(t)
	var out bytes.Buffer
	result, err := testExecRunner(ExecConfig{MaxOutputBytes: 64}).Run(context.Background(), []byte("i=0; while [ $i -lt 2000 ]; do echo line-$i; i=$((i+1)); done"), 10*time.Second, &out, &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.Truncated {
		t.Fatalf("result = %+v, want Truncated", result)
	}
	if out.Len() > 64 {
		t.Fatalf("wrote %d bytes past the 64-byte cap", out.Len())
	}
}

func TestExecRunRefusesWhenConcurrencyIsExhausted(t *testing.T) {
	skipWithoutPOSIXShell(t)
	runner := testExecRunner(ExecConfig{MaxConcurrent: 1})
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var out bytes.Buffer
		close(started)
		_, _ = runner.Run(context.Background(), []byte("sleep 1"), 5*time.Second, &out, &out)
	}()
	<-started
	time.Sleep(100 * time.Millisecond)
	var out bytes.Buffer
	if _, err := runner.Run(context.Background(), []byte("echo hi"), time.Second, &out, &out); !errors.Is(err, ErrExecBusy) {
		t.Fatalf("second run err = %v, want ErrExecBusy", err)
	}
	<-done
}

func TestExecRunRejectsOversizedScript(t *testing.T) {
	var out bytes.Buffer
	_, err := testExecRunner(ExecConfig{MaxScriptBytes: 16}).Run(context.Background(), bytes.Repeat([]byte("x"), 17), time.Second, &out, &out)
	if !errors.Is(err, ErrExecScriptTooLarge) {
		t.Fatalf("err = %v, want ErrExecScriptTooLarge", err)
	}
}

func TestExecRunnerDisabledRefusesEverything(t *testing.T) {
	var out bytes.Buffer
	runner := newExecRunner(ExecConfig{Enabled: false})
	if _, err := runner.Run(context.Background(), []byte("echo hi"), time.Second, &out, &out); !errors.Is(err, ErrExecDisabled) {
		t.Fatalf("err = %v, want ErrExecDisabled", err)
	}
}
