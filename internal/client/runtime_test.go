package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// runtimeTestConfig builds the smallest configuration a run accepts. The instance
// identity is supplied inline so the test never writes to the packaged identity path.
func runtimeTestConfig(tunnels ...config.TunnelConfig) config.Config {
	cfg := config.Config{Mode: config.ModeLocal}
	cfg.Client.ServerURL = "ws://server.example/ws/client"
	cfg.Client.Token = "client-secret"
	cfg.Client.InstanceID = "runtime-test-instance"
	cfg.Client.Connections.Min = 1
	cfg.Client.Connections.Max = 1
	cfg.Client.Tunnels = tunnels
	return cfg
}

func TestNewRuntimeFromConfigPreconditions(t *testing.T) {
	ctx := context.Background()
	tunnel := config.TunnelConfig{Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:0", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22}

	noURL := runtimeTestConfig(tunnel)
	noURL.Client.ServerURL = ""
	if _, err := NewRuntimeFromConfig(ctx, noURL, RuntimeOptions{}); err == nil || err.Error() != "client run requires client.server_url and client.token" {
		t.Fatalf("missing server_url error = %v", err)
	}

	noToken := runtimeTestConfig(tunnel)
	noToken.Client.Token = ""
	if _, err := NewRuntimeFromConfig(ctx, noToken, RuntimeOptions{}); err == nil || err.Error() != "client run requires client.server_url and client.token" {
		t.Fatalf("missing token error = %v", err)
	}

	if _, err := NewRuntimeFromConfig(ctx, runtimeTestConfig(), RuntimeOptions{}); err == nil || err.Error() != "client run requires at least one client.tunnels entry" {
		t.Fatalf("no tunnels error = %v", err)
	}

	badProtocol := runtimeTestConfig(config.TunnelConfig{Name: "x", Protocol: "quic", ListenAddr: "127.0.0.1:0", AgentID: "agent-a"})
	if _, err := NewRuntimeFromConfig(ctx, badProtocol, RuntimeOptions{}); err == nil || !strings.Contains(err.Error(), `unsupported tunnel protocol "quic"`) {
		t.Fatalf("bad protocol error = %v", err)
	}
}

// TestNewConfiguredForwardDescriptions pins the operator-facing transcript. These
// strings are what `client run` has always printed, and both the CLI and the tray
// now render them, so a silent rewording would break scripts that match on them.
func TestNewConfiguredForwardDescriptions(t *testing.T) {
	cfg := config.ClientConfig{}
	for _, test := range []struct {
		tunnel config.TunnelConfig
		want   string
	}{
		{config.TunnelConfig{Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:8080", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22}, "tcp tunnel web listening on 127.0.0.1:8080"},
		{config.TunnelConfig{Name: "dns", Protocol: "udp", ListenAddr: "127.0.0.1:53", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 53}, "udp tunnel dns listening on 127.0.0.1:53"},
		{config.TunnelConfig{Name: "site", Protocol: "http", ListenAddr: "127.0.0.1:8081", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 80}, "http tunnel site listening on 127.0.0.1:8081"},
		{config.TunnelConfig{Protocol: "socks5", ListenAddr: "127.0.0.1:1080", AgentID: "agent-a"}, "socks5 tunnel unnamed listening on 127.0.0.1:1080"},
		{config.TunnelConfig{Name: "proxy", Protocol: "http-proxy", ListenAddr: "127.0.0.1:8888", AgentID: "agent-a"}, "http-proxy tunnel proxy listening on 127.0.0.1:8888"},
	} {
		manager := NewSessionPoolManager(SessionPoolManagerOptions{ServerURL: "ws://s/ws/client", Token: "t"})
		forward, description, err := NewConfiguredForward(manager.Opener(test.tunnel.AgentID), cfg, test.tunnel)
		if err != nil {
			t.Fatalf("%s: %v", test.tunnel.Protocol, err)
		}
		if description != test.want {
			t.Fatalf("%s description = %q, want %q", test.tunnel.Protocol, description, test.want)
		}
		if forward == nil {
			t.Fatalf("%s produced a nil forward", test.tunnel.Protocol)
		}
	}
}

func TestRuntimeStartBindsListenersAndReportsStats(t *testing.T) {
	calls := make(chan *poolTestTransport, 4)
	var stdout bytes.Buffer
	rt, err := NewRuntimeFromConfig(context.Background(), runtimeTestConfig(
		config.TunnelConfig{Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:0", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22},
	), RuntimeOptions{Stdout: &stdout, Runner: newPoolTestRunner(calls), SkipLock: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop() }()

	if got := stdout.String(); got != "tcp tunnel web listening on 127.0.0.1:0\n" {
		t.Fatalf("stdout = %q", got)
	}
	waitPoolTransport(t, calls)
	waitForCondition(t, 2*time.Second, func() bool { return rt.Stats().Connected })

	stats := rt.Stats()
	if !stats.Running {
		t.Fatal("stats.Running = false after a successful start")
	}
	if stats.ServerURL != "ws://server.example/ws/client" {
		t.Fatalf("stats.ServerURL = %q", stats.ServerURL)
	}
	if stats.Uptime <= 0 {
		t.Fatalf("stats.Uptime = %v, want a positive duration", stats.Uptime)
	}
	if len(stats.Tunnels) != 1 {
		t.Fatalf("tunnels = %+v", stats.Tunnels)
	}
	tunnel := stats.Tunnels[0]
	if tunnel.Name != "web" || tunnel.Protocol != "tcp" || tunnel.AgentID != "agent-a" || tunnel.Target != "10.0.0.8:22" {
		t.Fatalf("tunnel status = %+v", tunnel)
	}
	if tunnel.State != TunnelStateListening {
		t.Fatalf("tunnel state = %q, want %q", tunnel.State, TunnelStateListening)
	}
	if len(stats.Agents) != 1 || stats.Agents[0].AgentID != "agent-a" || stats.Agents[0].ReadySessions != 1 {
		t.Fatalf("agent stats = %+v", stats.Agents)
	}

	if err := rt.Start(ctx); err == nil {
		t.Fatal("a second Start must be rejected")
	}
}

// TestRuntimeIsMutuallyExclusivePerConfig is the guarantee the tray and `client run`
// depend on: two runtimes driven by the same configuration file cannot both serve it.
func TestRuntimeIsMutuallyExclusivePerConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "client.yaml")
	tunnel := config.TunnelConfig{Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:0", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22}
	newRuntime := func() *Runtime {
		t.Helper()
		calls := make(chan *poolTestTransport, 4)
		rt, err := NewRuntimeFromConfig(context.Background(), runtimeTestConfig(tunnel), RuntimeOptions{
			ConfigPath: configPath, Runner: newPoolTestRunner(calls),
		})
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := newRuntime()
	if err := first.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	second := newRuntime()
	if err := second.Start(ctx); !errors.Is(err, ErrRuntimeAlreadyRunning) {
		t.Fatalf("second Start error = %v, want ErrRuntimeAlreadyRunning", err)
	}
	// A rejected start must not leave the runtime claiming it is running.
	if stats := second.Stats(); stats.Running {
		t.Fatal("the rejected runtime reports itself as running")
	}

	if err := first.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	stopped := first.Stats()
	if stopped.Running {
		t.Fatal("stats.Running stayed true after Stop")
	}
	if len(stopped.Tunnels) != 1 || stopped.Tunnels[0].State != TunnelStateStopped {
		t.Fatalf("tunnel state after stop = %+v", stopped.Tunnels)
	}

	// The lock has to be handed over, otherwise a supervisor could never restart.
	third := newRuntime()
	if err := third.Start(ctx); err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
	if err := third.Stop(); err != nil {
		t.Fatalf("third Stop: %v", err)
	}
}

// TestRuntimeLockIsScopedToConfiguration keeps the supported two-client setup
// working: an operator running two configurations side by side is not a conflict.
func TestRuntimeLockIsScopedToConfiguration(t *testing.T) {
	dir := t.TempDir()
	tunnel := config.TunnelConfig{Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:0", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := func(configPath string) *Runtime {
		t.Helper()
		calls := make(chan *poolTestTransport, 4)
		rt, err := NewRuntimeFromConfig(context.Background(), runtimeTestConfig(tunnel), RuntimeOptions{
			ConfigPath: configPath, Runner: newPoolTestRunner(calls),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.Start(ctx); err != nil {
			t.Fatalf("Start(%s): %v", configPath, err)
		}
		return rt
	}
	first := start(filepath.Join(dir, "a", "client.yaml"))
	defer func() { _ = first.Stop() }()
	second := start(filepath.Join(dir, "b", "client.yaml"))
	defer func() { _ = second.Stop() }()
}

// TestRuntimeCountsReconnects covers the only instability signal the pool does not
// otherwise surface: it re-establishes a dropped WebSocket without telling anyone.
func TestRuntimeCountsReconnects(t *testing.T) {
	calls := make(chan *poolTestTransport, 8)
	rt, err := NewRuntimeFromConfig(context.Background(), runtimeTestConfig(
		config.TunnelConfig{Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:0", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22},
	), RuntimeOptions{Runner: newPoolTestRunner(calls), SkipLock: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Stop() }()

	waitPoolTransport(t, calls)
	waitForCondition(t, 2*time.Second, func() bool { return rt.Stats().Reconnects == 0 })
	rt.mu.Lock()
	rt.attempts[1] = 3
	rt.mu.Unlock()
	if got := rt.Stats().Reconnects; got != 2 {
		t.Fatalf("Reconnects = %d, want 2 (three attempts on one slot)", got)
	}
}

func TestTunnelDisplayName(t *testing.T) {
	if got := TunnelDisplayName(config.TunnelConfig{Name: "  web  "}); got != "web" {
		t.Fatalf("TunnelDisplayName = %q, want trimmed name", got)
	}
	if got := TunnelDisplayName(config.TunnelConfig{}); got != "unnamed" {
		t.Fatalf("TunnelDisplayName = %q, want unnamed", got)
	}
}

// TestBroadcastErrDeliversToEveryReceiver pins the fan-out the Runtime relies on.
// Two goroutines reading one channel would each see the failure only half the time.
func TestBroadcastErrDeliversToEveryReceiver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := make(chan error, 1)
	receivers := broadcastErr(ctx, source, 3)
	if len(receivers) != 3 {
		t.Fatalf("receivers = %d, want 3", len(receivers))
	}
	want := errors.New("accept failed")
	source <- want
	for i, receiver := range receivers {
		select {
		case got := <-receiver:
			if !errors.Is(got, want) {
				t.Fatalf("receiver %d got %v, want %v", i, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("receiver %d never saw the failure", i)
		}
	}
}

func TestBroadcastErrNilSourceIsInert(t *testing.T) {
	receivers := broadcastErr(context.Background(), nil, 2)
	if len(receivers) != 2 {
		t.Fatalf("receivers = %d, want 2", len(receivers))
	}
	for i, receiver := range receivers {
		if receiver != nil {
			t.Fatalf("receiver %d = %v, want nil for a nil source", i, receiver)
		}
	}
}

func TestRuntimeWaitReportsUnstartedRuntime(t *testing.T) {
	rt, err := NewRuntimeFromConfig(context.Background(), runtimeTestConfig(
		config.TunnelConfig{Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:0", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22},
	), RuntimeOptions{SkipLock: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Wait(context.Background()); err == nil {
		t.Fatal("Wait on an unstarted runtime must fail rather than block forever")
	}
	// Stop before Start must be safe: a deferred cleanup cannot know how far the
	// caller got.
	if err := rt.Stop(); err != nil {
		t.Fatalf("Stop before Start: %v", err)
	}
}

func TestRuntimeStopEndsWait(t *testing.T) {
	calls := make(chan *poolTestTransport, 4)
	rt, err := NewRuntimeFromConfig(context.Background(), runtimeTestConfig(
		config.TunnelConfig{Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:0", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22},
	), RuntimeOptions{Runner: newPoolTestRunner(calls), SkipLock: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitPoolTransport(t, calls)

	done := make(chan error, 1)
	go func() { done <- rt.Wait(context.Background()) }()
	if err := rt.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait after Stop = %v, want nil or context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after Stop")
	}
	if got := fmt.Sprint(rt.Stats().Running); got != "false" {
		t.Fatalf("Running = %s after Stop", got)
	}
}
