package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/build"
	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/selfupdate"
	"github.com/tunnelmesh/tunnelmesh/internal/supervisor"
	sharedtransport "github.com/tunnelmesh/tunnelmesh/internal/transport"
	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

// Seams for tests. Production code never reassigns them.
var (
	// workerLifeline is the pipe the supervisor holds open. EOF on it means the
	// supervisor is gone, so a worker never outlives a killed supervisor and keeps
	// holding the client lock.
	workerLifeline io.Reader = os.Stdin
	newLauncher              = func() supervisor.Launcher { return supervisor.ExecLauncher{} }
	executablePath           = os.Executable
	processArgs              = func() []string { return os.Args[1:] }
	// installedVersion is the version of the binary that is running as the
	// supervisor: the baseline every downloaded version is compared with.
	installedVersion = func() string { return build.Current().Version }
	// upgraderHook lets tests adjust the Upgrader before it is used.
	upgraderHook = func(*selfupdate.Upgrader) {}
)

// roleWork is the body of a `run` command: it serves until ctx ends or it fails,
// and calls report(serverVersion) whenever it is connected and acknowledged.
type roleWork func(ctx context.Context, report func(serverVersion string)) error

// managedRole describes the process lifecycle one `run` command is under.
type managedRole struct {
	name   string
	update config.UpdateConfig
	// stateDir is where the supervisor keeps its working files when
	// update.state_dir is not set.
	stateDir string
	// transport carries the update traffic through the same proxy the connection to
	// the Server uses.
	transport config.TransportConfig
	// serverURL and token reach the Server for update.source=server.
	serverURL, token string
	// defaultRepository is downloads.github_repository, used when
	// update.github.repository is empty.
	defaultRepository string
}

// runManaged runs work as a worker, under a supervisor, or directly, depending on
// how this process was started and on `update.supervise`.
//
//   - Started by a supervisor (TUNNELMESH_WORKER=1): run as the worker.
//   - update.supervise=true: become the supervisor and launch the worker.
//   - update.supervise=false: run directly, exactly as before this feature.
func runManaged(cmd interface{ Context() context.Context }, role managedRole, work roleWork) error {
	if os.Getenv(workerproto.EnvWorker) == "1" {
		return runAsWorker(cmd.Context(), work)
	}
	if !role.update.Supervise {
		return work(cmd.Context(), func(string) {})
	}
	exe, err := executablePath()
	if err != nil {
		// Supervision is a convenience, never a reason for the service to fail to
		// start: without a path to relaunch there is nothing to supervise.
		slog.Warn("cannot locate the running executable; running without a supervisor", "error", err)
		return work(cmd.Context(), func(string) {})
	}
	return runAsSupervisor(cmd.Context(), role, exe)
}

func runAsWorker(ctx context.Context, work roleWork) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var lifelineClosed atomic.Bool
	go func() {
		_, _ = io.Copy(io.Discard, workerLifeline)
		lifelineClosed.Store(true)
		cancel()
	}()
	reporter := newReadyReporter(os.Getenv(workerproto.EnvReadyDir), build.Current().Version)
	err := work(ctx, reporter.Report)
	// Being told to go through the lifeline is a normal stop, not a failure.
	if lifelineClosed.Load() && (err == nil || errors.Is(err, context.Canceled)) {
		return nil
	}
	return err
}

func runAsSupervisor(ctx context.Context, role managedRole, exe string) error {
	stateDir := strings.TrimSpace(role.update.StateDir)
	if stateDir == "" {
		stateDir = role.stateDir
	}
	upgrades, release := buildUpgrades(role, stateDir)
	defer release()
	sup := supervisor.New(supervisor.Config{
		Role: role.name, Installed: exe, Args: processArgs(), Env: os.Environ(),
		RunDir:   filepath.Join(stateDir, "updates", "run"),
		Launcher: newLauncher(),
		Upgrades: upgrades, HealthTimeout: role.update.HealthTimeout, StableWindow: role.update.StableWindow,
	})
	code := sup.Run(ctx)
	if code == workerproto.ExitOK {
		return nil
	}
	return &workerproto.ExitError{Code: code, Err: fmt.Errorf("%s supervisor exited with code %d", role.name, code)}
}

// readyReporter publishes the worker's readiness for the supervisor.
type readyReporter struct {
	dir, version string

	mu     sync.Mutex
	wrote  bool
	server string
}

func newReadyReporter(dir, version string) *readyReporter {
	return &readyReporter{dir: dir, version: version}
}

// Report records that the worker is connected and acknowledged. It rewrites the
// file only when the Server version changed, because the supervisor reacts to
// every change and the file is rewritten after each reconnect.
func (r *readyReporter) Report(serverVersion string) {
	if r == nil || r.dir == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wrote && r.server == serverVersion {
		return
	}
	err := workerproto.WriteReady(r.dir, workerproto.ReadyInfo{
		PID: os.Getpid(), Version: r.version, ServerVersion: serverVersion, ReadyAt: time.Now().UTC(),
	})
	if err != nil {
		slog.Warn("cannot publish readiness to the supervisor", "dir", r.dir, "error", err)
		return
	}
	r.wrote, r.server = true, serverVersion
}

// checkTimeout bounds one whole release check, download included. The HTTP client
// has no overall timeout so a slow link can finish a large file, which means a
// stalled connection would otherwise wait forever.
const checkTimeout = 15 * time.Minute

// buildUpgrades assembles the upgrade machinery, or returns nil when upgrades are
// off or cannot run. It never fails the service: an unusable state directory, a
// lock held by another supervisor, or a source that cannot be built all degrade to
// supervision without upgrades, with a warning that says why.
func buildUpgrades(role managedRole, stateDir string) (supervisor.Upgrades, func()) {
	noop := func() {}
	if !role.update.Enabled {
		return nil, noop
	}
	store := &selfupdate.Store{Dir: filepath.Join(stateDir, "updates"), Role: role.name}
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		slog.Warn("self-update disabled: the state directory is not usable", "dir", store.Dir, "error", err)
		return nil, noop
	}
	lock, err := store.Lock()
	if err != nil {
		slog.Warn("self-update disabled: cannot lock the state directory", "dir", store.Dir, "error", err)
		return nil, noop
	}
	source, err := newReleaseSource(role)
	if err != nil {
		slog.Warn("self-update disabled: no usable release source", "source", role.update.Source, "error", err)
		_ = lock.Release()
		return nil, noop
	}
	upgrader := &selfupdate.Upgrader{
		Store: store, Source: source,
		Fetcher:   &selfupdate.Fetcher{Source: source, Limits: selfupdate.DefaultLimits()},
		Preflight: selfupdate.ExecPreflight{Args: processArgs(), Env: os.Environ()},
		Role:      role.name, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		InstalledVersion: installedVersion(),
		CheckInterval:    role.update.CheckInterval, BadVersionTTL: role.update.BadVersionTTL,
		KeepVersions: role.update.KeepVersions, CheckTimeout: checkTimeout,
	}
	upgraderHook(upgrader)
	return upgrader, func() { _ = lock.Release() }
}

// newReleaseSource builds the configured source. Its HTTP traffic goes through
// the transport the role already uses for the Server, so a proxy that is required
// to reach the Server is also used to reach the release host.
func newReleaseSource(role managedRole) (selfupdate.Source, error) {
	dialer, err := sharedtransport.FromConfig(role.transport)
	if err != nil {
		return nil, fmt.Errorf("configure the update transport: %w", err)
	}
	switch role.update.Source {
	case config.UpdateSourceGitHub:
		repository := strings.TrimSpace(role.update.GitHub.Repository)
		if repository == "" {
			repository = role.defaultRepository
		}
		return &selfupdate.GitHubSource{
			Repo: repository, MirrorBase: role.update.GitHub.MirrorBaseURL,
			HTTP:  updateHTTPClient(dialer),
			Token: strings.TrimSpace(os.Getenv("TUNNELMESH_GITHUB_TOKEN")),
		}, nil
	case config.UpdateSourceServer:
		base, err := client.HTTPURLFromWebSocket(role.serverURL, "/")
		if err != nil {
			return nil, fmt.Errorf("derive the Server address from %s: %w", role.serverURL, err)
		}
		return &selfupdate.ServerSource{BaseURL: base, Role: role.name, Token: role.token, HTTP: updateHTTPClient(dialer)}, nil
	default:
		return nil, fmt.Errorf("update source %q is not available", role.update.Source)
	}
}

// updateHTTPClient has no overall timeout on purpose (see checkTimeout) but bounds
// every phase that can stall before the body starts.
func updateHTTPClient(dialer *sharedtransport.Dialer) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContextForScheme(ctx, network, addr, "https")
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceAttemptHTTP2:     true,
	}}
}
