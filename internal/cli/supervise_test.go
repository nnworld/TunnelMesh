package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/build"
	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/server"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/supervisor"
	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

func TestReadyReporterWritesOnceAndAgainWhenTheServerVersionChanges(t *testing.T) {
	dir := t.TempDir()
	rep := newReadyReporter(dir, "v1.3.0")
	rep.Report("")
	info, ok, err := workerproto.ReadReady(dir)
	if err != nil || !ok || info.Version != "v1.3.0" || info.ServerVersion != "" || info.PID != os.Getpid() {
		t.Fatalf("first report = %+v, %v, %v", info, ok, err)
	}
	first := info.ReadyAt
	time.Sleep(5 * time.Millisecond)
	rep.Report("") // unchanged: no rewrite, so the supervisor is not woken for nothing
	if again, _, _ := workerproto.ReadReady(dir); !again.ReadyAt.Equal(first) {
		t.Fatal("an unchanged report rewrote the ready file")
	}
	rep.Report("v1.4.0")
	if updated, _, _ := workerproto.ReadReady(dir); updated.ServerVersion != "v1.4.0" {
		t.Fatalf("after the version appeared: %+v", updated)
	}
}

func TestReadyReporterWithoutADirectoryIsANoOp(t *testing.T) {
	newReadyReporter("", "v1.3.0").Report("v1.4.0") // must not panic or write anywhere
	var nilReporter *readyReporter
	nilReporter.Report("v1.4.0")
}

// swapLifeline replaces the worker's stdin for one test.
func swapLifeline(t *testing.T) *io.PipeWriter {
	t.Helper()
	reader, writer := io.Pipe()
	previous := workerLifeline
	workerLifeline = reader
	t.Cleanup(func() { workerLifeline = previous; _ = writer.Close() })
	return writer
}

func TestClientWorkerReportsReadyWithTheServerVersionAndStopsWhenTheLifelineCloses(t *testing.T) {
	previous := runClientSessionPool
	defer func() { runClientSessionPool = previous }()
	readyDir := t.TempDir()
	t.Setenv(workerproto.EnvWorker, "1")
	t.Setenv(workerproto.EnvReadyDir, readyDir)
	lifeline := swapLifeline(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	yaml := "mode: local\nclient:\n  server_url: wss://server.example/ws/client\n  token: client-secret\n  instance_id_path: " +
		filepath.Join(dir, "client-instance-id") + "\n  tunnels:\n    - name: socks\n      protocol: socks5\n      listen: " +
		reserveLoopbackListenAddress(t) + "\n      agent_id: agent-a\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	runClientSessionPool = func(ctx context.Context, _, _ string, onReady func(*client.Session) error, _ client.WebSocketRunOptions) error {
		transport := &cliFrameTransport{incoming: make(chan protocol.Frame, 4), sent: make(chan protocol.Frame, 16)}
		session := client.NewSession(transport)
		session.Start()
		if err := onReady(session); err != nil {
			return err
		}
		ack, err := protocol.EncodeClientMetadataAckPayload(protocol.ClientMetadataAckPayload{Accepted: true, ServerVersion: "v1.4.0"})
		if err != nil {
			return err
		}
		transport.incoming <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameClientMetadataAck, Payload: ack}
		<-ctx.Done()
		_ = session.Close()
		return ctx.Err()
	}

	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--config", path, "run"})
	done := make(chan error, 1)
	go func() { done <- Execute(context.Background(), root) }()

	waitForCondition(t, 5*time.Second, func() bool {
		info, ok, _ := workerproto.ReadReady(readyDir)
		return ok && info.ServerVersion == "v1.4.0" && info.Version == build.Current().Version
	}, "the client worker never published READY with the Server version")

	// The supervisor closing its end is how a worker learns it must go, even if the
	// supervisor was killed and could not send a signal.
	_ = lifeline.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker returned %v, want a clean exit when the lifeline closes", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the worker kept running after its lifeline closed")
	}
}

func TestAgentWorkerReportsReadyThroughTheRealServerAck(t *testing.T) {
	ctx := context.Background()
	readyDir := t.TempDir()
	t.Setenv(workerproto.EnvWorker, "1")
	t.Setenv(workerproto.EnvReadyDir, readyDir)
	lifeline := swapLifeline(t)

	db, err := storage.OpenSQLite(ctx, "file:agent-worker-ready?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := auth.NewAuthService(db).CreateUser(ctx, "agent-worker-owner", "agent-worker-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "agent-worker", Name: "agent-worker", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	agentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "agent-worker"})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()
	runtime, err := server.NewServerRuntime(db, server.AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	go func() { _ = runtime.ServeListener(serverCtx, listener) }()

	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	body := fmt.Sprintf("mode: local\nnode:\n  id: agent-worker-node\nagent:\n  instance_id: agent-worker-instance\n  server_url: ws://%s/ws/agent\n  id: agent-worker\n  token: %s\n", listener.Addr().String(), agentToken.Secret)
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewAgentRoot()
	root.SetArgs([]string{"run", "--config", configPath})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	done := make(chan error, 1)
	go func() { done <- Execute(context.Background(), root) }()

	// The Server announces its own version, which in a test binary is the build's.
	waitForCondition(t, 5*time.Second, func() bool {
		info, ok, _ := workerproto.ReadReady(readyDir)
		return ok && info.ServerVersion == build.Current().Version && info.PID == os.Getpid()
	}, "the agent worker never published READY after the Server's ack")
	_ = lifeline.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker returned %v, want a clean exit when the lifeline closes", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the agent worker kept running after its lifeline closed")
	}
}

// scriptedLauncher stands in for the real process launcher.
type scriptedLauncher struct {
	mu    sync.Mutex
	specs []supervisor.Spec
	exit  int
}

func (l *scriptedLauncher) Start(_ context.Context, spec supervisor.Spec) (supervisor.Child, error) {
	l.mu.Lock()
	l.specs = append(l.specs, spec)
	l.mu.Unlock()
	child := &scriptedChild{exit: make(chan supervisor.ExitResult, 1)}
	child.exit <- supervisor.ExitResult{Code: l.exit}
	return child, nil
}

type scriptedChild struct{ exit chan supervisor.ExitResult }

func (c *scriptedChild) PID() int                           { return 4242 }
func (c *scriptedChild) Wait() <-chan supervisor.ExitResult { return c.exit }
func (c *scriptedChild) Stop(time.Duration) error           { return nil }

func TestRunWithSupervisionHandsOverToTheSupervisor(t *testing.T) {
	t.Setenv("TUNNELMESH_CLIENT_UPDATE_SUPERVISE", "true")
	launcher := &scriptedLauncher{exit: workerproto.ExitConfig}
	previousLauncher, previousExe, previousArgs := newLauncher, executablePath, processArgs
	defer func() { newLauncher, executablePath, processArgs = previousLauncher, previousExe, previousArgs }()
	newLauncher = func() supervisor.Launcher { return launcher }
	executablePath = func() (string, error) { return "/opt/tunnelmesh/tunnelmesh-client", nil }

	stateDir := t.TempDir()
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	yaml := "mode: local\nclient:\n  server_url: wss://server.example/ws/client\n  token: client-secret\n  instance_id_path: " +
		filepath.Join(dir, "client-instance-id") + "\n  update:\n    enabled: false\n    state_dir: " + stateDir +
		"\n  tunnels:\n    - name: socks\n      protocol: socks5\n      listen: " + reserveLoopbackListenAddress(t) + "\n      agent_id: agent-a\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	processArgs = func() []string { return []string{"--config", path, "run"} }

	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--config", path, "run"})
	err := Execute(context.Background(), root)

	// The scripted worker exits 78, and the supervisor passes that code on so the
	// outer service manager sees the real reason.
	if got := workerproto.ExitCodeOf(err); got != workerproto.ExitConfig {
		t.Fatalf("exit code = %d, want %d; err = %v", got, workerproto.ExitConfig, err)
	}
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	if len(launcher.specs) != 1 {
		t.Fatalf("launches = %d, want 1", len(launcher.specs))
	}
	spec := launcher.specs[0]
	if spec.Bin != "/opt/tunnelmesh/tunnelmesh-client" {
		t.Fatalf("Bin = %q, want the installed executable", spec.Bin)
	}
	if strings.Join(spec.Args, " ") != "--config "+path+" run" {
		t.Fatalf("Args = %v, want the supervisor's own arguments", spec.Args)
	}
	if !strings.HasPrefix(spec.ReadyDir, filepath.Join(stateDir, "updates", "run")) {
		t.Fatalf("ReadyDir = %q, want it under <state_dir>/updates/run", spec.ReadyDir)
	}
}

func TestRunWithoutSupervisionRunsInProcess(t *testing.T) {
	// supervise=false (the package default) must leave `run` exactly as it was: no
	// launcher is ever consulted.
	previous := newLauncher
	defer func() { newLauncher = previous }()
	newLauncher = func() supervisor.Launcher {
		t.Fatal("a launcher was requested although supervise=false")
		return nil
	}
	path := filepath.Join(t.TempDir(), "client.yaml")
	if err := os.WriteFile(path, []byte("mode: local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--config", path, "run"})
	// Missing server_url and token fail in-process with the precondition error.
	if err := Execute(context.Background(), root); workerproto.ExitCodeOf(err) != workerproto.ExitConfig {
		t.Fatalf("err = %v, want the in-process configuration error", err)
	}
}

// Supervision is a convenience, never a reason for the service to fail to start.
func TestRunFallsBackToRunningDirectlyWhenTheExecutableCannotBeLocated(t *testing.T) {
	t.Setenv("TUNNELMESH_CLIENT_UPDATE_SUPERVISE", "true")
	previousExe, previousLauncher := executablePath, newLauncher
	defer func() { executablePath, newLauncher = previousExe, previousLauncher }()
	executablePath = func() (string, error) { return "", fmt.Errorf("no /proc") }
	newLauncher = func() supervisor.Launcher {
		t.Fatal("a launcher was requested although the executable is unknown")
		return nil
	}
	path := filepath.Join(t.TempDir(), "client.yaml")
	if err := os.WriteFile(path, []byte("mode: local\nclient:\n  update:\n    enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--config", path, "run"})
	// It ran in-process: the runtime's own precondition error, not a supervisor failure.
	err := Execute(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "client run requires client.server_url") {
		t.Fatalf("err = %v, want the in-process precondition error", err)
	}
}

// With upgrades enabled the supervisor launches the version the upgrade state
// selects instead of the installed binary.
func TestSupervisorLaunchesTheUpgradedVersionFromTheState(t *testing.T) {
	t.Setenv("TUNNELMESH_CLIENT_UPDATE_SUPERVISE", "true")
	t.Setenv("TUNNELMESH_CLIENT_UPDATE_ENABLED", "true")
	launcher := &scriptedLauncher{exit: workerproto.ExitConfig}
	previousLauncher, previousExe, previousArgs := newLauncher, executablePath, processArgs
	defer func() { newLauncher, executablePath, processArgs = previousLauncher, previousExe, previousArgs }()
	newLauncher = func() supervisor.Launcher { return launcher }
	executablePath = func() (string, error) { return "/opt/tunnelmesh/tunnelmesh-client", nil }

	stateDir := t.TempDir()
	versionDir := filepath.Join(stateDir, "updates", "versions", "v1.4.0")
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	upgraded := filepath.Join(versionDir, "tunnelmesh-client")
	if runtime.GOOS == "windows" {
		upgraded += ".exe"
	}
	if err := os.WriteFile(upgraded, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "updates", "state.json"), []byte(`{"schema":1,"current":"v1.4.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	yaml := "mode: local\nclient:\n  server_url: wss://server.example/ws/client\n  token: client-secret\n  instance_id_path: " +
		filepath.Join(dir, "client-instance-id") + "\n  update:\n    state_dir: " + stateDir +
		"\n  tunnels:\n    - name: socks\n      protocol: socks5\n      listen: " + reserveLoopbackListenAddress(t) + "\n      agent_id: agent-a\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	processArgs = func() []string { return []string{"--config", path, "run"} }

	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--config", path, "run"})
	_ = Execute(context.Background(), root)

	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	if len(launcher.specs) == 0 {
		t.Fatal("nothing was launched")
	}
	if launcher.specs[0].Bin != upgraded {
		t.Fatalf("Bin = %q, want the upgraded version %q", launcher.specs[0].Bin, upgraded)
	}
}

// If the state directory cannot be used, supervision continues without upgrades
// instead of failing the service.
func TestSupervisorWithAnUnusableStateDirectoryStillRestartsWithoutUpgrades(t *testing.T) {
	t.Setenv("TUNNELMESH_CLIENT_UPDATE_SUPERVISE", "true")
	t.Setenv("TUNNELMESH_CLIENT_UPDATE_ENABLED", "true")
	launcher := &scriptedLauncher{exit: workerproto.ExitConfig}
	previousLauncher, previousExe, previousArgs := newLauncher, executablePath, processArgs
	defer func() { newLauncher, executablePath, processArgs = previousLauncher, previousExe, previousArgs }()
	newLauncher = func() supervisor.Launcher { return launcher }
	executablePath = func() (string, error) { return "/opt/tunnelmesh/tunnelmesh-client", nil }

	// A path below a regular file can never be created.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	yaml := "mode: local\nclient:\n  server_url: wss://server.example/ws/client\n  token: client-secret\n  instance_id_path: " +
		filepath.Join(dir, "client-instance-id") + "\n  update:\n    state_dir: " + filepath.Join(blocker, "state") +
		"\n  tunnels:\n    - name: socks\n      protocol: socks5\n      listen: " + reserveLoopbackListenAddress(t) + "\n      agent_id: agent-a\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	processArgs = func() []string { return []string{"--config", path, "run"} }

	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--config", path, "run"})
	err := Execute(context.Background(), root)
	if got := workerproto.ExitCodeOf(err); got != workerproto.ExitConfig {
		t.Fatalf("exit code = %d, want the worker's %d passed through; err = %v", got, workerproto.ExitConfig, err)
	}
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	if len(launcher.specs) != 1 || launcher.specs[0].Bin != "/opt/tunnelmesh/tunnelmesh-client" {
		t.Fatalf("specs = %+v, want the installed binary to have been launched", launcher.specs)
	}
}
