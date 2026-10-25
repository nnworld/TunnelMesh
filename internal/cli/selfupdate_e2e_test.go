package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/selfupdate"
	"github.com/tunnelmesh/tunnelmesh/internal/semver"
	"github.com/tunnelmesh/tunnelmesh/internal/server"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/supervisor"
)

var (
	builtMu       sync.Mutex
	builtBinaries = map[string]string{}
	// builtDir holds every binary buildAgent produced. It outlives any single test,
	// because the binaries are cached across tests, and TestMain removes it.
	builtDir string
)

func removeBuiltBinaries() {
	builtMu.Lock()
	defer builtMu.Unlock()
	if builtDir != "" {
		_ = os.RemoveAll(builtDir)
	}
}

// buildAgent compiles the real agent with the given version baked in, once per
// version per test run.
func buildAgent(t *testing.T, version string) string {
	t.Helper()
	builtMu.Lock()
	defer builtMu.Unlock()
	if path, ok := builtBinaries[version]; ok {
		return path
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if builtDir == "" {
		dir, err := os.MkdirTemp("", "tm-e2e-bin-")
		if err != nil {
			t.Fatal(err)
		}
		builtDir = dir
	}
	out := filepath.Join(builtDir, version, "tunnelmesh-agent")
	cmd := exec.Command("go", "build", "-trimpath",
		"-ldflags", "-X github.com/tunnelmesh/tunnelmesh/internal/build.Version="+version,
		"-o", out, "./cmd/tunnelmesh-agent")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the agent %s: %v\n%s", version, err, output)
	}
	builtBinaries[version] = out
	return out
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// releaseTarball packs binary the way scripts/build-release.sh packs a release:
// entries under "./" with no top-level directory.
func releaseTarball(t *testing.T, binary string) []byte {
	t.Helper()
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		body []byte
		mode int64
	}{{"./README.md", []byte("readme"), 0o644}, {"./tunnelmesh-agent", data, 0o755}} {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeGitHubFor serves one release (v1.4.0) with the given binary.
func fakeGitHubFor(t *testing.T, binary string) *httptest.Server {
	t.Helper()
	version := semver.Version{Major: 1, Minor: 4}
	asset := selfupdate.AssetName(version, runtime.GOOS, runtime.GOARCH)
	archive := releaseTarball(t, binary)
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/acme/tunnelmesh/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/acme/tunnelmesh/releases/tag/v1.4.0", http.StatusFound)
	})
	mux.HandleFunc("/acme/tunnelmesh/releases/download/v1.4.0/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, sums)
	})
	mux.HandleFunc("/acme/tunnelmesh/releases/download/v1.4.0/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type recordingLauncher struct {
	mu   sync.Mutex
	bins []string
}

func (r *recordingLauncher) Start(ctx context.Context, spec supervisor.Spec) (supervisor.Child, error) {
	r.mu.Lock()
	r.bins = append(r.bins, spec.Bin)
	r.mu.Unlock()
	return supervisor.ExecLauncher{}.Start(ctx, spec)
}

func (r *recordingLauncher) launched() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bins...)
}

type upgradeE2E struct {
	baseline   string
	stateDir   string
	server     *server.ServerRuntime
	launcher   *recordingLauncher
	stopAgent  context.CancelFunc
	agentDone  chan error
	statePath  string
	configPath string
}

func (e *upgradeE2E) state(t *testing.T) selfupdate.State {
	t.Helper()
	store := &selfupdate.Store{Dir: filepath.Join(e.stateDir, "updates"), Role: "agent"}
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// startUpgradeE2E runs a real Server in-process and an `agent run` supervisor over
// the real baseline v1.3.0 binary, pointed at a fake GitHub that publishes v1.4.0.
func startUpgradeE2E(t *testing.T, releaseBinary string, fromServer bool) *upgradeE2E {
	t.Helper()
	if testing.Short() {
		t.Skip("builds real binaries; skipped with -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the release is a tar.gz here")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go toolchain is needed to build the test binaries")
	}
	baseline := buildAgent(t, "v1.3.0")
	var github *httptest.Server
	var updatesDir string
	if fromServer {
		updatesDir = publishReleaseDir(t, releaseBinary)
	} else {
		github = fakeGitHubFor(t, releaseBinary)
	}

	// The Server announces its own version, which is what bounds the upgrade. It is a
	// parameter of the Server rather than a rewrite of the process-wide build
	// variables, which other goroutines read.
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, fmt.Sprintf("file:selfupdate-e2e-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	owner, err := auth.NewAuthService(db).CreateUser(ctx, "e2e-owner", "e2e-owner-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "agent-e2e", Name: "agent-e2e", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	token, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "agent-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()
	runtimeServer, err := server.NewServerRuntime(db, server.AgentSessionConfig{}, server.RuntimeConfig{Version: "v1.4.0", Updates: config.ServerUpdatesConfig{Dir: updatesDir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtimeServer.Close() })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, stopServer := context.WithCancel(ctx)
	t.Cleanup(stopServer)
	go func() { _ = runtimeServer.ServeListener(serverCtx, listener) }()

	e := &upgradeE2E{baseline: baseline, stateDir: filepath.Join(t.TempDir(), "state"), server: runtimeServer, launcher: &recordingLauncher{}, agentDone: make(chan error, 1)}
	e.configPath = filepath.Join(t.TempDir(), "agent.yaml")
	source := "source: github\n    github:\n      repository: acme/tunnelmesh\n      mirror_base_url: " + func() string {
		if github != nil {
			return github.URL
		}
		return ""
	}()
	if fromServer {
		source = "source: server"
	}
	body := fmt.Sprintf(`mode: local
node:
  id: agent-e2e-node
agent:
  instance_id: agent-e2e-instance
  server_url: ws://%s/ws/agent
  id: agent-e2e
  token: %s
  update:
    state_dir: %s
    health_timeout: 30s
    stable_window: 0s
    %s
`, listener.Addr().String(), token.Secret, e.stateDir, source)
	if err := os.WriteFile(e.configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TUNNELMESH_AGENT_UPDATE_ENABLED", "true")
	t.Setenv("TUNNELMESH_AGENT_UPDATE_SUPERVISE", "true")
	previousLauncher, previousExe, previousArgs, previousInstalled, previousHook := newLauncher, executablePath, processArgs, installedVersion, upgraderHook
	t.Cleanup(func() {
		newLauncher, executablePath, processArgs, installedVersion, upgraderHook = previousLauncher, previousExe, previousArgs, previousInstalled, previousHook
	})
	newLauncher = func() supervisor.Launcher { return e.launcher }
	executablePath = func() (string, error) { return baseline, nil }
	processArgs = func() []string { return []string{"--config", e.configPath, "run"} }
	installedVersion = func() string { return "v1.3.0" }
	upgraderHook = func(u *selfupdate.Upgrader) {
		// Do not wait minutes for the first release check.
		u.FirstCheckDelay, u.FirstCheckJitter = 20*time.Millisecond, time.Millisecond
	}

	agentCtx, stop := context.WithCancel(ctx)
	e.stopAgent = stop
	root := NewAgentRoot()
	root.SetArgs([]string{"--config", e.configPath, "run"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	go func() { e.agentDone <- Execute(agentCtx, root) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-e.agentDone:
		case <-time.After(30 * time.Second):
			t.Error("the supervisor did not stop")
		}
	})
	return e
}

func (e *upgradeE2E) waitForAgentSession(t *testing.T, why string) {
	t.Helper()
	waitForCondition(t, 60*time.Second, func() bool {
		_, ok := e.server.AgentSessions.Get("agent-e2e")
		return ok
	}, why)
}

func TestSelfUpgradeEndToEndWithRealBinaries(t *testing.T) {
	e := startUpgradeE2E(t, buildAgent(t, "v1.4.0"), false)
	before := fileDigest(t, e.baseline)

	e.waitForAgentSession(t, "the baseline agent never connected to the Server")
	waitForCondition(t, 90*time.Second, func() bool {
		st := e.state(t)
		return st.Current == "v1.4.0" && st.Pending == nil
	}, "the agent was never upgraded to v1.4.0 and confirmed")
	e.waitForAgentSession(t, "the upgraded agent never connected to the Server")

	bins := e.launcher.launched()
	if len(bins) < 2 || bins[0] != e.baseline {
		t.Fatalf("launches = %v, want the baseline first", bins)
	}
	upgraded := filepath.Join(e.stateDir, "updates", "versions", "v1.4.0", "tunnelmesh-agent")
	if bins[len(bins)-1] != upgraded {
		t.Fatalf("launches = %v, want the last one to be the upgraded binary %s", bins, upgraded)
	}
	// The installed binary is never modified: that is what makes the upgrade work
	// under a read-only installation directory and lets a rollback be a pointer switch.
	if after := fileDigest(t, e.baseline); after != before {
		t.Fatal("the installed binary was modified by the upgrade")
	}
	// The upgraded worker really is the new version.
	out, err := exec.Command(upgraded, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "v1.4.0") {
		t.Fatalf("upgraded binary --version = %q, %v", out, err)
	}
}

// The release passes the preflight, so the upgrade is attempted, but the worker
// dies the moment it starts. The agent must end up running the old version again.
func TestSelfUpgradeRollsBackEndToEndWhenTheNewVersionCrashes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real binaries; skipped with -short")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(e2eBadReleaseEnv, "1")
	e := startUpgradeE2E(t, self, false)

	e.waitForAgentSession(t, "the baseline agent never connected to the Server")
	waitForCondition(t, 90*time.Second, func() bool {
		st := e.state(t)
		_, bad := st.Bad["v1.4.0"]
		return bad && st.Current == "" && st.Pending == nil
	}, "the failed upgrade was never rolled back and blacklisted")
	e.waitForAgentSession(t, "the rolled-back agent never reconnected to the Server")

	bins := e.launcher.launched()
	candidate := filepath.Join(e.stateDir, "updates", "versions", "v1.4.0", "tunnelmesh-agent")
	if len(bins) < 3 || bins[0] != e.baseline || bins[1] != candidate || bins[len(bins)-1] != e.baseline {
		t.Fatalf("launches = %v, want baseline, then the crashing candidate, then the baseline again", bins)
	}
	data, err := os.ReadFile(filepath.Join(e.stateDir, "updates", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if _, kept := st["pending"]; kept {
		t.Fatalf("state.json still holds a pending trial: %s", data)
	}
	// The failed version is not kept on disk.
	if _, err := os.Stat(filepath.Dir(candidate)); err == nil {
		t.Fatal("the failed version's files were left behind")
	}
}

// publishReleaseDir lays a v1.4.0 release out the way server.updates.dir expects.
func publishReleaseDir(t *testing.T, binary string) string {
	t.Helper()
	dir := t.TempDir()
	version := semver.Version{Major: 1, Minor: 4}
	asset := selfupdate.AssetName(version, runtime.GOOS, runtime.GOARCH)
	archive := releaseTarball(t, binary)
	sum := sha256.Sum256(archive)
	for name, body := range map[string][]byte{
		asset:           archive,
		"SHA256SUMS":    []byte(hex.EncodeToString(sum[:]) + "  " + asset + "\n"),
		"manifest.json": []byte(`{"version":"v1.4.0"}`),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The Server itself is the release source: the agent never touches GitHub.
func TestSelfUpgradeFromTheServerEndToEnd(t *testing.T) {
	e := startUpgradeE2E(t, buildAgent(t, "v1.4.0"), true)
	e.waitForAgentSession(t, "the baseline agent never connected to the Server")
	waitForCondition(t, 90*time.Second, func() bool {
		st := e.state(t)
		return st.Current == "v1.4.0" && st.Pending == nil
	}, "the agent was never upgraded from the Server's own distribution")
	e.waitForAgentSession(t, "the upgraded agent never connected to the Server")
}
