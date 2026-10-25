package supervisor

import (
	"context"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

const helperEnv = "SUPERVISOR_TEST_HELPER"

// TestMain lets the test binary double as the worker: the launcher is pointed at
// os.Args[0] with helperEnv set, which exercises the real exec, stdin lifeline and
// signal paths without shipping a separate fixture binary.
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		runHelper(mode)
		return
	}
	os.Exit(m.Run())
}

func runHelper(mode string) {
	switch {
	case strings.HasPrefix(mode, "exit:"):
		code, _ := strconv.Atoi(strings.TrimPrefix(mode, "exit:"))
		os.Exit(code)
	case mode == "env":
		out := os.Getenv("SUPERVISOR_TEST_OUT")
		_ = os.WriteFile(out, []byte(os.Getenv(workerproto.EnvWorker)+"|"+os.Getenv(workerproto.EnvReadyDir)), 0o600)
		os.Exit(0)
	case mode == "stdin-eof":
		// A worker that shuts itself down when the supervisor's lifeline closes. It
		// ignores signals so that EOF is the only thing that can end it.
		signal.Ignore(syscall.SIGTERM, os.Interrupt)
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case mode == "sigterm":
		// Never reads stdin, so only the signal can end it.
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, os.Interrupt)
		<-ch
		os.Exit(7)
	case mode == "stubborn":
		signal.Ignore(syscall.SIGTERM, os.Interrupt)
		// A plain `select {}` would make the runtime abort on deadlock.
		time.Sleep(time.Hour)
	case mode == "crash-then-stay":
		// Crashes twice, then behaves, so the supervisor's restart is visible.
		state := os.Getenv("SUPERVISOR_TEST_STATE")
		data, _ := os.ReadFile(state)
		n, _ := strconv.Atoi(string(data))
		_ = os.WriteFile(state, []byte(strconv.Itoa(n+1)), 0o600)
		if n < 2 {
			os.Exit(1)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(99)
}

func helperSpec(mode string, extra ...string) Spec {
	return Spec{Bin: os.Args[0], Env: append(append(os.Environ(), helperEnv+"="+mode), extra...)}
}

func waitResult(t *testing.T, c Child, timeout time.Duration) ExitResult {
	t.Helper()
	select {
	case res := <-c.Wait():
		return res
	case <-time.After(timeout):
		t.Fatal("worker did not exit")
		return ExitResult{}
	}
}

func TestExecLauncherPassesTheWorkerContract(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env.txt")
	spec := helperSpec("env", "SUPERVISOR_TEST_OUT="+out)
	spec.ReadyDir = "/tmp/ready-dir"
	child, err := (ExecLauncher{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if res := waitResult(t, child, 10*time.Second); res.Code != 0 {
		t.Fatalf("worker exited with %+v", res)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "1|/tmp/ready-dir" {
		t.Fatalf("worker saw %q, want the worker flag and ready dir", data)
	}
}

func TestExecLauncherReportsTheExitCode(t *testing.T) {
	child, err := (ExecLauncher{}).Start(context.Background(), helperSpec("exit:78"))
	if err != nil {
		t.Fatal(err)
	}
	if res := waitResult(t, child, 10*time.Second); res.Code != workerproto.ExitConfig {
		t.Fatalf("exit code = %d, want %d", res.Code, workerproto.ExitConfig)
	}
}

func TestExecLauncherReportsAMissingBinary(t *testing.T) {
	if _, err := (ExecLauncher{}).Start(context.Background(), Spec{Bin: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("Start() accepted a binary that does not exist")
	}
}

// The lifeline is what keeps a worker from outliving a supervisor that was
// killed: closing stdin is the only shutdown signal that works the same on every
// platform, including a Windows service with no console.
func TestStopClosesTheLifelineAndTheWorkerExits(t *testing.T) {
	child, err := (ExecLauncher{}).Start(context.Background(), helperSpec("stdin-eof"))
	if err != nil {
		t.Fatal(err)
	}
	// Give the helper time to install its signal handling.
	time.Sleep(300 * time.Millisecond)
	started := time.Now()
	if err := child.Stop(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Stop took %v, want it to return as soon as the worker exits", elapsed)
	}
	if res := waitResult(t, child, time.Second); res.Code != 0 {
		t.Fatalf("exit = %+v, want a clean exit on EOF", res)
	}
}

func TestStopKillsAWorkerThatIgnoresEverythingAfterTheGrace(t *testing.T) {
	child, err := (ExecLauncher{}).Start(context.Background(), helperSpec("stubborn"))
	if err != nil {
		t.Fatal(err)
	}
	// Give the helper time to install its signal handler.
	time.Sleep(300 * time.Millisecond)
	started := time.Now()
	if err := child.Stop(400 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 350*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("Stop took %v, want about the 400ms grace before the kill", elapsed)
	}
	if res := waitResult(t, child, time.Second); res.Code == 0 {
		t.Fatalf("exit = %+v, want a non-zero code for a killed worker", res)
	}
}

func TestSupervisorRestartsARealCrashingProcess(t *testing.T) {
	state := filepath.Join(t.TempDir(), "count")
	sup := New(Config{
		Role: "client", Installed: os.Args[0], Launcher: ExecLauncher{},
		Env:         append(os.Environ(), helperEnv+"=crash-then-stay", "SUPERVISOR_TEST_STATE="+state),
		RunDir:      t.TempDir(),
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		StopGrace: 5 * time.Second, ReadyPoll: 20 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code := make(chan int, 1)
	go func() { code <- sup.Run(ctx) }()

	deadline := time.Now().Add(15 * time.Second)
	for {
		data, _ := os.ReadFile(state)
		if n, _ := strconv.Atoi(string(data)); n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the supervisor did not restart the crashing worker")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case got := <-code:
		if got != workerproto.ExitOK {
			t.Fatalf("exit code = %d, want 0 after a requested stop", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor did not stop")
	}
}
