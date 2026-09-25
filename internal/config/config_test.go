package config_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestDefaultAgentInstanceIDPathMatchesSystemdWritableStateDirectory(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source file")
	}
	unitPath := filepath.Join(filepath.Dir(file), "..", "..", "deploy", "systemd", "tunnelmesh-agent.service")
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read agent systemd unit: %v", err)
	}

	var writableDirectory string
	for _, line := range strings.Split(string(unit), "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "ReadWritePaths="); found {
			writableDirectory = value
			break
		}
	}
	if writableDirectory == "" {
		t.Fatal("agent systemd unit has no ReadWritePaths")
	}

	got := filepath.Dir(config.DefaultAgentInstanceIDPath)
	if got != writableDirectory {
		t.Fatalf("DefaultAgentInstanceIDPath directory = %q, want systemd writable directory %q", got, writableDirectory)
	}
}

func TestLoadDefaultsToLocalSQLiteDatabaseRegistryAndTCPBridge(t *testing.T) {
	t.Setenv("TUNNELMESH_MODE", "")
	t.Setenv("TUNNELMESH_STORAGE_DRIVER", "")
	t.Setenv("TUNNELMESH_REGISTRY_TYPE", "")
	t.Setenv("TUNNELMESH_SERVER_TCP_BRIDGE_ENABLED", "")

	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Mode != config.ModeLocal {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, config.ModeLocal)
	}
	if cfg.Storage.Driver != config.StorageSQLite {
		t.Fatalf("Storage.Driver = %q, want %q", cfg.Storage.Driver, config.StorageSQLite)
	}
	if !cfg.Storage.AutoInit {
		t.Fatal("Storage.AutoInit = false, want true")
	}
	if cfg.Registry.Type != config.RegistryDatabase {
		t.Fatalf("Registry.Type = %q, want %q", cfg.Registry.Type, config.RegistryDatabase)
	}
	if !cfg.Server.TCPBridge.Enabled {
		t.Fatal("Server.TCPBridge.Enabled = false, want true")
	}
}

func TestLoadDownloadsDefaultRepository(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Downloads.GitHubRepository != "nnworld/TunnelMesh" {
		t.Fatalf("Downloads.GitHubRepository = %q, want nnworld/TunnelMesh", cfg.Downloads.GitHubRepository)
	}
}

func TestValidateDownloadsRepository(t *testing.T) {
	base, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cases := []struct {
		name       string
		repository string
		valid      bool
	}{
		{name: "default", repository: config.DefaultGitHubRepository, valid: true},
		{name: "organization and name", repository: "example/release", valid: true},
		{name: "omitted", repository: "", valid: true},
		{name: "extra path segment", repository: "example/release/extra", valid: false},
		{name: "missing owner", repository: "/release", valid: false},
		{name: "missing name", repository: "example/", valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Downloads.GitHubRepository = tc.repository
			err := config.Validate(cfg)
			if tc.valid && err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
			if !tc.valid && (err == nil || !strings.Contains(err.Error(), "downloads github repository")) {
				t.Fatalf("Validate() error = %v, want downloads github repository error", err)
			}
		})
	}
}

func TestLoadAgentConnectionPoolDefaultsAndValidation(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := config.AgentConnectionConfig{
		Min: 1, Max: 1, HighWatermark: 16, LowWatermark: 2,
		EvaluationInterval: 10 * time.Second, Cooldown: 30 * time.Second,
	}
	if cfg.Agent.Connections != want {
		t.Fatalf("Agent.Connections = %+v, want %+v", cfg.Agent.Connections, want)
	}
	base := config.Config{Mode: config.ModeLocal, Storage: config.StorageConfig{Driver: config.StorageSQLite}, Registry: config.RegistryConfig{Type: config.RegistryDatabase}}
	base.Agent.ServerURL = "wss://tunnel.example.com/ws/agent"
	base.Agent.Connections = want
	cases := []struct {
		name string
		edit func(*config.AgentConnectionConfig)
		want string
	}{
		{name: "min below one", edit: func(c *config.AgentConnectionConfig) { c.Min = 0 }, want: "agent connections min must be at least 1"},
		{name: "max below min", edit: func(c *config.AgentConnectionConfig) { c.Min, c.Max = 2, 1 }, want: "agent connections max must be greater than or equal to min"},
		{name: "former ceiling is legal now", edit: func(c *config.AgentConnectionConfig) { c.Max = 64 }},
		{name: "ceiling inclusive", edit: func(c *config.AgentConnectionConfig) { c.Max = 512 }},
		{name: "max above limit", edit: func(c *config.AgentConnectionConfig) { c.Max = 513 }, want: "agent connections max must be at most 512"},
		{name: "low above high", edit: func(c *config.AgentConnectionConfig) { c.Max, c.LowWatermark = 8, 17 }, want: "agent connections low watermark must be less than or equal to high watermark"},
		{name: "invalid evaluation interval", edit: func(c *config.AgentConnectionConfig) { c.EvaluationInterval = 0 }, want: "agent connections evaluation interval must be positive"},
		{name: "invalid cooldown", edit: func(c *config.AgentConnectionConfig) { c.Cooldown = 0 }, want: "agent connections cooldown must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.edit(&cfg.Agent.Connections)
			err := config.Validate(cfg)
			// An empty want means the pool size itself must be accepted: a ceiling
			// change is only correct in both directions if the passing case is
			// asserted too. The fixture leaves unrelated sections unset on purpose,
			// so only the connection rules are under test here.
			if tc.want == "" {
				if err != nil && strings.Contains(err.Error(), "agent connections") {
					t.Fatalf("Validate() error = %v, want this Agent pool accepted", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadStreamLatencyDefaults(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantServerStream := config.ServerStreamConfig{
		MaxConcurrentOpens: 256, MaxActivePerAgent: 1024, MaxPendingOpens: 1024,
		InitialWindow: 262144, WindowUpdateThreshold: 131072, MaxFramePayload: 32768,
	}
	if cfg.Server.Stream != wantServerStream {
		t.Fatalf("Server.Stream = %+v, want %+v", cfg.Server.Stream, wantServerStream)
	}
	wantAuthCache := config.AuthorizationCacheConfig{
		Enabled: true, LocalPositiveTTL: 5 * time.Second, ClusterPositiveTTL: 5 * time.Minute,
		NegativeTTL: 3 * time.Second, RevisionPollInterval: 2 * time.Second,
		MaxStaleOnPollError: 5 * time.Second, MaxEntries: 100000,
	}
	if cfg.Server.AuthorizationCache != wantAuthCache {
		t.Fatalf("Server.AuthorizationCache = %+v, want %+v", cfg.Server.AuthorizationCache, wantAuthCache)
	}
	wantAgentStreams := config.AgentStreamConfig{
		MaxConcurrentDials: 32, MaxActive: 1024, MaxPendingDials: 128, ConnectTimeout: 5 * time.Second,
		OpenTimeout: 8 * time.Second, InboundBufferBytes: 262144,
		ICMPBindAddress: "0.0.0.0", ICMPTimeout: 5 * time.Second, ICMPMaxConcurrent: 64,
	}
	if cfg.Agent.Streams != wantAgentStreams {
		t.Fatalf("Agent.Streams = %+v, want %+v", cfg.Agent.Streams, wantAgentStreams)
	}
	wantClientStream := config.ClientStreamConfig{OpenTimeout: 8 * time.Second, InboundBufferBytes: 262144}
	if cfg.Client.Stream != wantClientStream {
		t.Fatalf("Client.Stream = %+v, want %+v", cfg.Client.Stream, wantClientStream)
	}
	wantRemoteValidation := config.RemoteValidationConfig{
		PositiveTTL: 15 * time.Second, NegativeTTL: 2 * time.Second,
		Timeout: 3 * time.Second, MaxEntries: 10000,
	}
	if cfg.Client.RemoteValidation != wantRemoteValidation {
		t.Fatalf("Client.RemoteValidation = %+v, want %+v", cfg.Client.RemoteValidation, wantRemoteValidation)
	}
}

func applyStreamLatencyDefaults(cfg *config.Config) {
	cfg.Server.Stream = config.ServerStreamConfig{
		MaxConcurrentOpens: 256, MaxActivePerAgent: 1024, MaxPendingOpens: 1024, InitialWindow: 262144,
		WindowUpdateThreshold: 131072, MaxFramePayload: 32768,
	}
	cfg.Server.AuthorizationCache = config.AuthorizationCacheConfig{
		Enabled: true, LocalPositiveTTL: 5 * time.Second, ClusterPositiveTTL: 5 * time.Minute,
		NegativeTTL: 3 * time.Second, RevisionPollInterval: 2 * time.Second,
		MaxStaleOnPollError: 5 * time.Second, MaxEntries: 100000,
	}
	cfg.Agent.Streams = config.AgentStreamConfig{
		MaxConcurrentDials: 32, MaxActive: 1024, MaxPendingDials: 128, ConnectTimeout: 5 * time.Second,
		OpenTimeout: 8 * time.Second, InboundBufferBytes: 262144,
		ICMPBindAddress: "0.0.0.0", ICMPTimeout: 5 * time.Second, ICMPMaxConcurrent: 64,
	}
	cfg.Client.Stream = config.ClientStreamConfig{OpenTimeout: 8 * time.Second, InboundBufferBytes: 262144}
	cfg.Client.RemoteValidation = config.RemoteValidationConfig{
		PositiveTTL: 15 * time.Second, NegativeTTL: 2 * time.Second,
		Timeout: 3 * time.Second, MaxEntries: 10000,
	}
}

func TestLoadAgentICMPStreamDefaultsAndValidation(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	streams := cfg.Agent.Streams
	if streams.ICMPEnabled {
		t.Error("ICMPEnabled default = true, want false so an upgrade never opens a ping socket by itself")
	}
	if streams.ICMPBindAddress != "0.0.0.0" {
		t.Errorf("ICMPBindAddress default = %q, want 0.0.0.0", streams.ICMPBindAddress)
	}
	if streams.ICMPTimeout != 5*time.Second {
		t.Errorf("ICMPTimeout default = %s, want 5s", streams.ICMPTimeout)
	}
	if streams.ICMPMaxConcurrent != 64 {
		t.Errorf("ICMPMaxConcurrent default = %d, want 64", streams.ICMPMaxConcurrent)
	}

	t.Setenv("TUNNELMESH_AGENT_STREAMS_ICMP_ENABLED", "true")
	t.Setenv("TUNNELMESH_AGENT_STREAMS_ICMP_BIND_ADDRESS", "127.0.0.1")
	t.Setenv("TUNNELMESH_AGENT_STREAMS_ICMP_TIMEOUT", "2s")
	t.Setenv("TUNNELMESH_AGENT_STREAMS_ICMP_MAX_CONCURRENT", "8")
	overridden, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("Load() with the environment set error = %v", err)
	}
	got := overridden.Agent.Streams
	if !got.ICMPEnabled || got.ICMPBindAddress != "127.0.0.1" || got.ICMPTimeout != 2*time.Second || got.ICMPMaxConcurrent != 8 {
		t.Fatalf("Agent.Streams icmp = %+v, want the four environment overrides applied", got)
	}
	if err := config.Validate(overridden); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}

	cases := []struct {
		name string
		edit func(*config.Config)
		want string
	}{
		{name: "agent icmp timeout", edit: func(c *config.Config) { c.Agent.Streams.ICMPTimeout = 0 }, want: "agent stream icmp timeout must be positive while icmp is enabled"},
		{name: "agent icmp concurrency", edit: func(c *config.Config) { c.Agent.Streams.ICMPMaxConcurrent = -1 }, want: "agent stream icmp max concurrent must be positive while icmp is enabled"},
		{name: "agent icmp bind address", edit: func(c *config.Config) { c.Agent.Streams.ICMPBindAddress = "agent.example.com" }, want: "agent stream icmp bind address must be an ip address while icmp is enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broken := overridden
			tc.edit(&broken)
			err := config.Validate(broken)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}

	// A disabled engine is never validated: the agent does not open the socket,
	// so refusing to start over an unset address would be a self-inflicted
	// outage for every deployment that does not use ICMP.
	disabled := overridden
	disabled.Agent.Streams.ICMPEnabled = false
	disabled.Agent.Streams.ICMPTimeout = 0
	disabled.Agent.Streams.ICMPMaxConcurrent = 0
	disabled.Agent.Streams.ICMPBindAddress = ""
	if err := config.Validate(disabled); err != nil {
		t.Fatalf("Validate() with icmp disabled error = %v, want nil", err)
	}
}

func TestValidateStreamLatencyLimits(t *testing.T) {
	base, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*config.Config)
		want string
	}{
		{name: "server concurrent opens", edit: func(c *config.Config) { c.Server.Stream.MaxConcurrentOpens = 0 }, want: "server stream max concurrent opens must be positive"},
		{name: "server pending opens", edit: func(c *config.Config) { c.Server.Stream.MaxPendingOpens = -1 }, want: "server stream max pending opens must be positive"},
		{name: "server initial window", edit: func(c *config.Config) { c.Server.Stream.InitialWindow = 0 }, want: "server stream initial window must be positive"},
		{name: "server window threshold", edit: func(c *config.Config) { c.Server.Stream.WindowUpdateThreshold = 262145 }, want: "server stream window update threshold must be positive and no greater than the initial window"},
		{name: "server frame payload", edit: func(c *config.Config) { c.Server.Stream.MaxFramePayload = 1 << 20 }, want: "server stream max frame payload must be 32768"},
		{name: "server window headroom", edit: func(c *config.Config) {
			c.Server.Stream.InitialWindow = 65536
			c.Server.Stream.WindowUpdateThreshold = 40000
		}, want: "server stream initial window must exceed the update threshold by at least 32768 bytes"},
		{name: "auth cache positive ttl", edit: func(c *config.Config) { c.Server.AuthorizationCache.LocalPositiveTTL = 0 }, want: "authorization cache local positive TTL must be positive"},
		{name: "auth cache entries", edit: func(c *config.Config) { c.Server.AuthorizationCache.MaxEntries = 0 }, want: "authorization cache max entries must be positive"},
		{name: "agent concurrent dials", edit: func(c *config.Config) { c.Agent.Streams.MaxConcurrentDials = 0 }, want: "agent stream max concurrent dials must be positive"},
		{name: "agent pending dials", edit: func(c *config.Config) { c.Agent.Streams.MaxPendingDials = -1 }, want: "agent stream max pending dials must be positive"},
		{name: "agent connect timeout", edit: func(c *config.Config) { c.Agent.Streams.ConnectTimeout = 0 }, want: "agent stream connect timeout must be positive"},
		{name: "agent inbound buffer", edit: func(c *config.Config) { c.Agent.Streams.InboundBufferBytes = -1 }, want: "agent stream inbound buffer bytes must be at least 65536"},
		{name: "client inbound buffer", edit: func(c *config.Config) { c.Client.Stream.InboundBufferBytes = 32768 }, want: "client stream inbound buffer bytes must be at least 65536"},
		{name: "client open timeout", edit: func(c *config.Config) { c.Client.Stream.OpenTimeout = 0 }, want: "client stream open timeout must be positive"},
		{name: "remote validation ttl", edit: func(c *config.Config) { c.Client.RemoteValidation.PositiveTTL = -time.Second }, want: "remote validation positive TTL must be positive"},
		{name: "remote validation entries", edit: func(c *config.Config) { c.Client.RemoteValidation.MaxEntries = 0 }, want: "remote validation max entries must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.edit(&cfg)
			err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadGeneratesStableAgentInstanceID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-instance-id")
	cfg, err := config.Load(context.Background(), config.ConfigOptions{AgentInstanceIDPath: path})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !strings.HasPrefix(cfg.Agent.InstanceID, "agent-") {
		t.Fatalf("generated Agent.InstanceID = %q, want agent- prefix", cfg.Agent.InstanceID)
	}
	again, err := config.Load(context.Background(), config.ConfigOptions{AgentInstanceIDPath: path})
	if err != nil {
		t.Fatalf("Load(again) error = %v", err)
	}
	if again.Agent.InstanceID != cfg.Agent.InstanceID {
		t.Fatalf("generated Agent.InstanceID changed: %q -> %q", cfg.Agent.InstanceID, again.Agent.InstanceID)
	}

	explicitPath := filepath.Join(dir, "unused-agent-instance-id")
	explicit, err := config.Load(context.Background(), config.ConfigOptions{
		AgentInstanceIDPath: explicitPath,
		CLI:                 map[string]any{"agent.instance_id": "agent-node-a"},
	})
	if err != nil {
		t.Fatalf("Load(explicit) error = %v", err)
	}
	if explicit.Agent.InstanceID != "agent-node-a" {
		t.Fatalf("explicit Agent.InstanceID = %q", explicit.Agent.InstanceID)
	}
	if _, err := os.Stat(explicitPath); !os.IsNotExist(err) {
		t.Fatalf("explicit instance ID unexpectedly wrote persistence file: %v", err)
	}
}

func TestLoadPrecedenceIsCLIThenEnvironmentThenFileThenDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tunnelmesh.yaml")
	if err := os.WriteFile(path, []byte("mode: cluster\nstorage:\n  driver: mysql\n  auto_init: false\n  mysql:\n    dsn: file-dsn\n    tls: true\nregistry:\n  type: etcd\nnode:\n  id: file-node\nserver:\n  tcp_bridge:\n    enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNNELMESH_MODE", "local")
	t.Setenv("TUNNELMESH_STORAGE_AUTO_INIT", "true")
	t.Setenv("TUNNELMESH_REGISTRY_TYPE", "database")
	t.Setenv("TUNNELMESH_SERVER_TCP_BRIDGE_ENABLED", "true")

	cfg, err := config.Load(context.Background(), config.ConfigOptions{
		ConfigFile: path,
		CLI: map[string]any{
			"mode":                      "cluster",
			"storage.auto_init":         false,
			"server.tcp_bridge.enabled": false,
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Mode != config.ModeCluster {
		t.Fatalf("CLI mode did not win: %q", cfg.Mode)
	}
	if cfg.Storage.AutoInit {
		t.Fatal("CLI storage.auto_init did not win")
	}
	if cfg.Registry.Type != config.RegistryDatabase {
		t.Fatalf("environment registry.type did not win over file: %q", cfg.Registry.Type)
	}
	if cfg.Server.TCPBridge.Enabled {
		t.Fatal("CLI TCP bridge value did not win")
	}
}

func TestLoadAndValidateMultipleSOCKS5Tunnels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	yaml := `mode: local
client:
  server_url: wss://tunnel.example.com/ws/client
  token: client-secret
  tunnels:
    - name: socks-a
      protocol: socks5
      listen: 127.0.0.1:10866
      agent_id: agent-a
    - name: socks-b
      protocol: socks5
      listen: 127.0.0.1:10867
      agent_id: agent-b
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Client.Tunnels) != 2 {
		t.Fatalf("tunnels = %d, want 2", len(cfg.Client.Tunnels))
	}
	if cfg.Client.Tunnels[0].Protocol != "socks5" || cfg.Client.Tunnels[0].ListenAddr != "127.0.0.1:10866" || cfg.Client.Tunnels[0].AgentID != "agent-a" {
		t.Fatalf("first tunnel = %+v", cfg.Client.Tunnels[0])
	}
	if cfg.Client.Tunnels[1].Protocol != "socks5" || cfg.Client.Tunnels[1].ListenAddr != "127.0.0.1:10867" || cfg.Client.Tunnels[1].AgentID != "agent-b" {
		t.Fatalf("second tunnel = %+v", cfg.Client.Tunnels[1])
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate(valid SOCKS5 tunnels) error = %v", err)
	}
}

func TestLoadClientConnectionPoolDefaultsAndValidation(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := config.ClientConnectionConfig{
		Min: 1, Max: 1, HighWatermark: 16, LowWatermark: 2,
		EvaluationInterval: 10 * time.Second, Cooldown: 30 * time.Second,
	}
	if cfg.Client.Connections != want {
		t.Fatalf("Client.Connections = %+v, want %+v", cfg.Client.Connections, want)
	}

	base := config.Config{Mode: config.ModeLocal, Storage: config.StorageConfig{Driver: config.StorageSQLite}, Registry: config.RegistryConfig{Type: config.RegistryDatabase}}
	base.Client.ServerURL = "wss://tunnel.example.com/ws/client"
	base.Client.Connections = want
	cases := []struct {
		name string
		edit func(*config.ClientConnectionConfig)
		want string
	}{
		{name: "min below one", edit: func(c *config.ClientConnectionConfig) { c.Min = 0 }, want: "client connections min must be at least 1"},
		{name: "max below min", edit: func(c *config.ClientConnectionConfig) { c.Min, c.Max = 2, 1 }, want: "client connections max must be greater than or equal to min"},
		{name: "max above limit", edit: func(c *config.ClientConnectionConfig) { c.Max = 17 }, want: "client connections max must be at most 16"},
		{name: "low above high", edit: func(c *config.ClientConnectionConfig) { c.LowWatermark = 17 }, want: "client connections low watermark must be less than or equal to high watermark"},
		{name: "invalid interval", edit: func(c *config.ClientConnectionConfig) { c.EvaluationInterval = 0 }, want: "client connections evaluation interval must be positive"},
		{name: "invalid cooldown", edit: func(c *config.ClientConnectionConfig) { c.Cooldown = 0 }, want: "client connections cooldown must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.edit(&cfg.Client.Connections)
			err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadAndValidateHTTPProxyTunnel(t *testing.T) {
	valid := `mode: local
client:
  server_url: wss://tunnel.example.com/ws/client
  token: client-secret
  tunnels:
    - name: http-proxy
      protocol: http-proxy
      listen: 127.0.0.1:18081
      agent_id: agent-web
      auth_mode: none
`
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load(valid) error = %v", err)
	}
	if len(cfg.Client.Tunnels) != 1 || cfg.Client.Tunnels[0].Protocol != "http-proxy" {
		t.Fatalf("tunnels = %+v, want one http-proxy tunnel", cfg.Client.Tunnels)
	}

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "invalid auth mode",
			yaml: "client:\n  tunnels:\n    - protocol: http-proxy\n      listen: 127.0.0.1:18081\n      agent_id: agent-web\n      auth_mode: password\n",
			want: "http-proxy auth mode must be none or basic",
		},
		{
			name: "remote listener without permission",
			yaml: "client:\n  tunnels:\n    - protocol: http-proxy\n      listen: 0.0.0.0:18081\n      agent_id: agent-web\n",
			want: "non-loopback http-proxy tunnel requires allow_remote",
		},
		{
			name: "remote listener without basic auth",
			yaml: "client:\n  tunnels:\n    - protocol: http-proxy\n      listen: 0.0.0.0:18081\n      agent_id: agent-web\n      allow_remote: true\n",
			want: "non-loopback http-proxy tunnel requires basic auth",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "client.yaml")
			if err := os.WriteFile(path, []byte(test.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
			if err == nil {
				err = config.Validate(cfg)
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsInvalidSOCKS5TunnelConfiguration(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "missing agent id",
			yaml: "client:\n  tunnels:\n    - protocol: socks5\n      listen: 127.0.0.1:10866\n",
			want: "requires agent_id",
		},
		{
			name: "duplicate listen address",
			yaml: "client:\n  tunnels:\n    - protocol: socks5\n      listen: 127.0.0.1:10866\n      agent_id: agent-a\n    - protocol: socks5\n      listen: 127.0.0.1:10866\n      agent_id: agent-b\n",
			want: "duplicate tunnel listen address",
		},
		{
			name: "invalid auth mode",
			yaml: "client:\n  tunnels:\n    - protocol: socks5\n      listen: 127.0.0.1:10866\n      agent_id: agent-a\n      auth_mode: token\n",
			want: "socks5 auth mode must be none or password",
		},
		{
			name: "remote tcp listener without explicit permission",
			yaml: "client:\n  tunnels:\n    - protocol: tcp\n      listen: 0.0.0.0:10867\n      agent_id: agent-a\n      target_host: 10.0.0.10\n      target_port: 5432\n",
			want: "non-loopback tcp tunnel requires allow_remote",
		},
		{
			name: "remote udp listener without explicit permission",
			yaml: "client:\n  tunnels:\n    - protocol: udp\n      listen: 0.0.0.0:10868\n      agent_id: agent-a\n      target_host: 10.0.0.10\n      target_port: 5432\n",
			want: "non-loopback udp tunnel requires allow_remote",
		},
		{
			name: "remote http listener without explicit permission",
			yaml: "client:\n  tunnels:\n    - protocol: http\n      listen: 0.0.0.0:10869\n      agent_id: agent-a\n      target_host: 10.0.0.10\n      target_port: 8080\n",
			want: "non-loopback http tunnel requires allow_remote",
		},
		{
			name: "remote listener without explicit permission",
			yaml: "client:\n  tunnels:\n    - protocol: socks5\n      listen: 0.0.0.0:10866\n      agent_id: agent-a\n",
			want: "non-loopback socks5 tunnel requires allow_remote",
		},
		{
			name: "remote listener without password auth",
			yaml: "client:\n  tunnels:\n    - protocol: socks5\n      listen: 0.0.0.0:10866\n      agent_id: agent-a\n      allow_remote: true\n",
			want: "non-loopback socks5 tunnel requires password auth",
		},
		{
			name: "unsupported protocol",
			yaml: "client:\n  tunnels:\n    - protocol: quic\n      listen: 127.0.0.1:10866\n      agent_id: agent-a\n",
			want: "unsupported tunnel protocol",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "client.yaml")
			if err := os.WriteFile(path, []byte(test.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
			if err == nil {
				err = config.Validate(cfg)
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadAndValidateDynamicRouteSuffix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tunnelmesh.yaml")
	if err := os.WriteFile(path, []byte("server:\n  dynamic_suffix: apps.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.DynamicSuffix != "apps.example.com" {
		t.Fatalf("Server.DynamicSuffix = %q, want %q", cfg.Server.DynamicSuffix, "apps.example.com")
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	t.Setenv("TUNNELMESH_SERVER_DYNAMIC_SUFFIX", "apps.example.com")
	cfg, err = config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("Load(environment) error = %v", err)
	}
	if cfg.Server.DynamicSuffix != "apps.example.com" {
		t.Fatalf("environment suffix = %q, want %q", cfg.Server.DynamicSuffix, "apps.example.com")
	}

	cfg.Server.DynamicSuffix = "*.example.com"
	if err := config.Validate(cfg); err == nil || !strings.Contains(err.Error(), "dynamic route suffix") {
		t.Fatalf("Validate(wildcard) error = %v, want dynamic route suffix error", err)
	}
}

func TestValidateRejectsIncompleteClusterAndUnknownRegistry(t *testing.T) {
	base := config.Config{Mode: config.ModeCluster}
	if err := config.Validate(base); err == nil {
		t.Fatal("Validate() error = nil for incomplete cluster config")
	} else {
		for _, want := range []string{"mysql dsn"} {
			if !strings.Contains(strings.ToLower(err.Error()), want) {
				t.Errorf("Validate() error %q does not mention %q", err, want)
			}
		}
	}

	bad := config.Config{}
	bad.Registry.Type = "consul"
	if err := config.Validate(bad); err == nil || !strings.Contains(strings.ToLower(err.Error()), "registry") {
		t.Fatalf("Validate() error = %v, want invalid registry error", err)
	}
}

func TestValidateAllowsClusterWithoutExplicitNodeID(t *testing.T) {
	cfg := config.Config{
		Mode:     config.ModeCluster,
		Storage:  config.StorageConfig{Driver: config.StorageMySQL, MySQL: config.MySQLConfig{DSN: "user:pass@tcp(db:3306)/tunnelmesh"}},
		Registry: config.RegistryConfig{Type: config.RegistryDatabase},
	}
	applyStreamLatencyDefaults(&cfg)
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v, want generated node identity to be allowed", err)
	}
}

func TestLoadGeneratesNodeIDOnlyWhenClusterIdentityIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node-id")
	cfg, err := config.Load(context.Background(), config.ConfigOptions{
		NodeIDPath: path,
		CLI: map[string]any{
			"mode":              config.ModeCluster,
			"storage.driver":    config.StorageMySQL,
			"storage.mysql.dsn": "db",
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !strings.HasPrefix(cfg.Node.ID, "server-") {
		t.Fatalf("generated node ID = %q, want server- prefix", cfg.Node.ID)
	}
	explicit, err := config.Load(context.Background(), config.ConfigOptions{
		NodeIDPath: filepath.Join(dir, "unused-node-id"),
		CLI: map[string]any{
			"mode":              config.ModeCluster,
			"storage.driver":    config.StorageMySQL,
			"storage.mysql.dsn": "db",
			"node.id":           "server-explicit",
		},
	})
	if err != nil {
		t.Fatalf("Load(explicit node) error = %v", err)
	}
	if explicit.Node.ID != "server-explicit" {
		t.Fatalf("explicit node ID = %q, want server-explicit", explicit.Node.ID)
	}
	if _, err := os.Stat(filepath.Join(dir, "unused-node-id")); !os.IsNotExist(err) {
		t.Fatalf("explicit node ID unexpectedly wrote persistence file: %v", err)
	}
}

func TestValidateAllowsClusterWithoutMySQLTLS(t *testing.T) {
	cfg := config.Config{
		Mode:     config.ModeCluster,
		Storage:  config.StorageConfig{Driver: config.StorageMySQL, MySQL: config.MySQLConfig{DSN: "user:pass@tcp(db:3306)/tunnelmesh", TLS: false}},
		Registry: config.RegistryConfig{Type: config.RegistryDatabase},
		Node:     config.NodeConfig{ID: "server-1"},
	}
	applyStreamLatencyDefaults(&cfg)
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v, want nil when MySQL TLS is disabled", err)
	}
}

func TestEnsureNodeIDPersistsAndReusesGeneratedID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node-id")
	first, err := config.EnsureNodeID(path)
	if err != nil {
		t.Fatalf("EnsureNodeID() error = %v", err)
	}
	if first == "" {
		t.Fatal("EnsureNodeID() returned an empty ID")
	}
	second, err := config.EnsureNodeID(path)
	if err != nil {
		t.Fatalf("EnsureNodeID() second call error = %v", err)
	}
	if second != first {
		t.Fatalf("EnsureNodeID() changed identity from %q to %q", first, second)
	}
}

func TestInitializeNodeIDWritesGeneratedIDToYAML(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(configPath, []byte("# keep this comment\nmode: cluster\nstorage:\n  driver: mysql\n  mysql:\n    dsn: db\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	id, err := config.InitializeNodeID(configPath, filepath.Join(dir, "node-id"))
	if err != nil {
		t.Fatalf("InitializeNodeID() error = %v", err)
	}
	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: configPath, NodeIDPath: filepath.Join(dir, "node-id")})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Node.ID != id {
		t.Fatalf("config node.id = %q, want %q", cfg.Node.ID, id)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# keep this comment") {
		t.Fatalf("InitializeNodeID() discarded YAML comments: %s", data)
	}
}

func TestInitializeNodeIDKeepsExplicitConfigIdentity(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(configPath, []byte("mode: cluster\nnode:\n  id: server-explicit\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	id, err := config.InitializeNodeID(configPath, filepath.Join(dir, "node-id"))
	if err != nil {
		t.Fatalf("InitializeNodeID() error = %v", err)
	}
	if id != "server-explicit" {
		t.Fatalf("InitializeNodeID() = %q, want explicit identity", id)
	}
}

func TestLoadPersistsGeneratedNodeIDOnlyWhenRequested(t *testing.T) {
	writeConfig := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "server.yaml")
		contents := "# keep this comment\nmode: cluster\nstorage:\n  driver: mysql\n  mysql:\n    dsn: db\nregistry:\n  type: database\n"
		if err := os.WriteFile(path, []byte(contents), 0o640); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("persists generated identity", func(t *testing.T) {
		path := writeConfig(t)
		cfg, err := config.Load(context.Background(), config.ConfigOptions{
			ConfigFile:                     path,
			NodeIDPath:                     filepath.Join(t.TempDir(), "node-id"),
			PersistGeneratedNodeIDToConfig: true,
		})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !strings.HasPrefix(cfg.Node.ID, "server-") {
			t.Fatalf("generated node ID = %q, want server- prefix", cfg.Node.ID)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "# keep this comment") || !strings.Contains(string(data), "node:\n  id: server-") {
			t.Fatalf("generated node ID was not written with comments: %s", data)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("config mode = %o, want 0640", info.Mode().Perm())
		}
	})

	t.Run("does not persist by default", func(t *testing.T) {
		path := writeConfig(t)
		if _, err := config.Load(context.Background(), config.ConfigOptions{
			ConfigFile: path,
			NodeIDPath: filepath.Join(t.TempDir(), "node-id"),
		}); err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "node:") {
			t.Fatalf("default Load unexpectedly rewrote config: %s", data)
		}
	})

	t.Run("keeps explicit config identity", func(t *testing.T) {
		path := writeConfig(t)
		if err := os.WriteFile(path, []byte("# keep this comment\nmode: cluster\nnode:\n  id: server-explicit\nstorage:\n  driver: mysql\n  mysql:\n    dsn: db\nregistry:\n  type: database\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(context.Background(), config.ConfigOptions{
			ConfigFile:                     path,
			NodeIDPath:                     filepath.Join(t.TempDir(), "node-id"),
			PersistGeneratedNodeIDToConfig: true,
		})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Node.ID != "server-explicit" {
			t.Fatalf("node ID = %q, want explicit identity", cfg.Node.ID)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "id: server-explicit") || !strings.Contains(string(data), "# keep this comment") {
			t.Fatalf("explicit identity was rewritten: %s", data)
		}
	})

	t.Run("keeps CLI identity and does not rewrite config", func(t *testing.T) {
		path := writeConfig(t)
		nodeIDPath := filepath.Join(t.TempDir(), "node-id")
		cfg, err := config.Load(context.Background(), config.ConfigOptions{
			ConfigFile:                     path,
			NodeIDPath:                     nodeIDPath,
			PersistGeneratedNodeIDToConfig: true,
			CLI:                            map[string]any{"node.id": "server-cli"},
		})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Node.ID != "server-cli" {
			t.Fatalf("node ID = %q, want CLI identity", cfg.Node.ID)
		}
		if _, err := os.Stat(nodeIDPath); !os.IsNotExist(err) {
			t.Fatalf("CLI identity unexpectedly wrote persistence file: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "node:") {
			t.Fatalf("CLI identity unexpectedly rewrote config: %s", data)
		}
	})
}

func TestLoadHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := config.Load(ctx, config.ConfigOptions{}); err == nil {
		t.Fatal("Load() error = nil for cancelled context")
	}
}

func TestValidateMetadataSourcesRejectsUnsafeEntries(t *testing.T) {
	cases := []struct {
		name   string
		source config.MetadataSource
		want   string
	}{
		{name: "invalid name", source: config.MetadataSource{Name: "bad/name", Source: "env", Key: "REGION"}, want: "name"},
		{name: "relative file", source: config.MetadataSource{Name: "device", Source: "file", Path: "etc/machine-id"}, want: "absolute"},
		{name: "sensitive name", source: config.MetadataSource{Name: "api_token", Source: "env", Key: "TOKEN"}, want: "sensitive"},
		{name: "wildcard env", source: config.MetadataSource{Name: "region", Source: "env", Key: "REGION_*"}, want: "wildcard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Mode: config.ModeLocal, Storage: config.StorageConfig{Driver: config.StorageSQLite}, Registry: config.RegistryConfig{Type: config.RegistryDatabase}}
			cfg.Agent.Metadata = []config.MetadataSource{tc.source}
			err := config.Validate(cfg)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateClientMetadataSourcesRejectsUnsafeEntries(t *testing.T) {
	cfg := config.Config{Mode: config.ModeLocal, Storage: config.StorageConfig{Driver: config.StorageSQLite}, Registry: config.RegistryConfig{Type: config.RegistryDatabase}}
	cfg.Client.Metadata = []config.MetadataSource{{Name: "api_key", Source: "env", Key: "CLIENT_API_KEY"}}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Validate() error = %v, want sensitive client metadata name", err)
	}
}

func TestValidateMetadataSourcesRejectsCrossSourceFields(t *testing.T) {
	cases := []struct {
		name   string
		source config.MetadataSource
		want   string
	}{
		{name: "file with env key", source: config.MetadataSource{Name: "device", Source: "file", Path: "/etc/machine-id", Key: "DEVICE"}, want: "env key"},
		{name: "env with file path", source: config.MetadataSource{Name: "region", Source: "env", Key: "REGION", Path: "/etc/region"}, want: "file path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, loadErr := config.Load(context.Background(), config.ConfigOptions{})
			if loadErr != nil {
				t.Fatalf("Load() error = %v", loadErr)
			}
			cfg.Agent.Metadata = []config.MetadataSource{tc.source}
			err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadDecodesAgentMetadataSources(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metadata.yaml")
	contents := "mode: local\nstorage:\n  driver: sqlite\nagent:\n  metadata:\n    - name: region\n      source: env\n      key: TUNNELMESH_REGION\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Agent.Metadata) != 1 || cfg.Agent.Metadata[0].Name != "region" {
		t.Fatalf("metadata = %+v", cfg.Agent.Metadata)
	}
}

func TestLoadReadsAgentBearerTokenFromEnvironmentWithoutRedactionLeak(t *testing.T) {
	t.Setenv("TUNNELMESH_AGENT_SERVER_URL", "wss://server.example/ws/agent")
	t.Setenv("TUNNELMESH_AGENT_ID", "agent-1")
	t.Setenv("TUNNELMESH_AGENT_TOKEN", "secret-token")
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Token != "secret-token" {
		t.Fatalf("agent token=%q", cfg.Agent.Token)
	}
	data, err := cfg.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-token") {
		t.Fatalf("redacted config leaked agent token: %s", data)
	}
}

func TestLoadClientTokenPrecedenceAndRedaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client-token.yaml")
	if err := os.WriteFile(path, []byte("client:\n  server_url: wss://file.example/ws/client\n  token: file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNNELMESH_CLIENT_TOKEN", "env-secret")
	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path, CLI: map[string]any{"client.token": "cli-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Client.Token != "cli-secret" {
		t.Fatalf("Client.Token = %q, want CLI value", cfg.Client.Token)
	}
	data, err := cfg.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"file-secret", "env-secret", "cli-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("redacted config leaked %q: %s", secret, data)
		}
	}
	if strings.Contains(string(data), "/file/key.pem") {
		t.Fatalf("redacted config leaked relay private key path: %s", data)
	}
}

func TestLoadSecurityAndNativeTLSDefaults(t *testing.T) {
	t.Setenv("TUNNELMESH_SECURITY_ALLOW_LEGACY_CONNECTION_TOKENS", "")
	t.Setenv("TUNNELMESH_TLS_ENABLED", "")

	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.AllowLegacyConnectionTokens {
		t.Fatal("legacy connection tokens are enabled by default")
	}
	if cfg.TLS.Enabled {
		t.Fatal("native TLS is enabled by default")
	}
	if cfg.TLS.MinVersion != "1.2" {
		t.Fatalf("TLS.MinVersion = %q, want 1.2", cfg.TLS.MinVersion)
	}
}

func TestLoadSecurityAndNativeTLSPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "security.yaml")
	contents := "security:\n  allowed_hosts: [file.example]\n  allowed_origins: [https://file.example]\n  allow_legacy_connection_tokens: false\ntls:\n  enabled: false\n  cert_file: file-cert.pem\n  key_file: file-key.pem\n  min_version: \"1.2\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNNELMESH_SECURITY_ALLOWED_HOSTS", "env.example,second.example")
	t.Setenv("TUNNELMESH_SECURITY_ALLOW_LEGACY_CONNECTION_TOKENS", "true")

	cfg, err := config.Load(context.Background(), config.ConfigOptions{
		ConfigFile: path,
		CLI: map[string]any{
			"security.allowed_origins": []string{"https://cli.example"},
			"tls.enabled":              true,
			"tls.cert_file":            "cli-cert.pem",
			"tls.key_file":             "cli-key.pem",
			"tls.min_version":          "1.3",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Security.AllowedHosts) != 2 || cfg.Security.AllowedHosts[0] != "env.example" || cfg.Security.AllowedHosts[1] != "second.example" {
		t.Fatalf("AllowedHosts = %v, want environment value", cfg.Security.AllowedHosts)
	}
	if len(cfg.Security.AllowedOrigins) != 1 || cfg.Security.AllowedOrigins[0] != "https://cli.example" {
		t.Fatalf("AllowedOrigins = %v, want CLI value", cfg.Security.AllowedOrigins)
	}
	if !cfg.Security.AllowLegacyConnectionTokens {
		t.Fatal("environment legacy migration flag did not win over file")
	}
	if !cfg.TLS.Enabled || cfg.TLS.CertFile != "cli-cert.pem" || cfg.TLS.KeyFile != "cli-key.pem" || cfg.TLS.MinVersion != "1.3" {
		t.Fatalf("TLS = %+v, want CLI values", cfg.TLS)
	}
}

func TestValidateRejectsUnsafeSecurityAndNativeTLSConfiguration(t *testing.T) {
	base := config.Config{Mode: config.ModeLocal, Storage: config.StorageConfig{Driver: config.StorageSQLite}, Registry: config.RegistryConfig{Type: config.RegistryDatabase}}
	cases := []struct {
		name string
		edit func(*config.Config)
		want string
	}{
		{name: "host with userinfo delimiter", edit: func(cfg *config.Config) { cfg.Security.AllowedHosts = []string{"allowed.example@evil.example"} }, want: "allowed host"},
		{name: "host with invalid port", edit: func(cfg *config.Config) { cfg.Security.AllowedHosts = []string{"allowed.example:not-a-port"} }, want: "allowed host"},
		{name: "origin with path", edit: func(cfg *config.Config) { cfg.Security.AllowedOrigins = []string{"https://allowed.example/path"} }, want: "allowed origin"},
		{name: "origin with websocket scheme", edit: func(cfg *config.Config) { cfg.Security.AllowedOrigins = []string{"wss://allowed.example"} }, want: "allowed origin"},
		{name: "TLS missing key", edit: func(cfg *config.Config) {
			cfg.TLS = config.TLSConfig{Enabled: true, CertFile: "cert.pem", MinVersion: "1.2"}
		}, want: "private key"},
		{name: "TLS invalid minimum", edit: func(cfg *config.Config) {
			cfg.TLS = config.TLSConfig{Enabled: true, CertFile: "cert.pem", KeyFile: "key.pem", MinVersion: "1.1"}
		}, want: "minimum version"},
		{name: "relative agent URL", edit: func(cfg *config.Config) { cfg.Agent.ServerURL = "/ws/agent" }, want: "agent server url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.edit(&cfg)
			err := config.Validate(cfg)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRedactedJSONHidesNativeTLSPrivateKeyPath(t *testing.T) {
	cfg := config.Config{TLS: config.TLSConfig{KeyFile: "/secrets/native-tls.key"}}
	data, err := cfg.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/secrets/native-tls.key") {
		t.Fatalf("redacted config leaked TLS private key path: %s", data)
	}
}

func TestLoadRelayConfigPrecedenceAndRedaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "relay.yaml")
	file := "mode: cluster\nstorage:\n  driver: mysql\n  mysql:\n    dsn: file-dsn\n    tls: true\nnode:\n  id: file-node\nserver:\n  relay:\n    enabled: true\n    listen: 127.0.0.1:9443\n    ca: /file/ca.pem\n    cert: /file/cert.pem\n    key: /file/key.pem\n    server_name: file.relay\n    node_token: file-secret\n"
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNNELMESH_SERVER_RELAY_NODE_TOKEN", "env-secret")
	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path, CLI: map[string]any{
		"node.id":                  "cli-node",
		"server.relay.endpoint":    "relay.example:9443",
		"server.relay.node_token":  "cli-secret",
		"server.relay.server_name": "cli.relay",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Node.ID != "cli-node" || cfg.Server.Relay.Endpoint != "relay.example:9443" || cfg.Server.Relay.NodeToken != "cli-secret" {
		t.Fatalf("relay precedence mismatch: node=%q relay=%+v", cfg.Node.ID, cfg.Server.Relay)
	}
	data, err := cfg.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"file-secret", "env-secret", "cli-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("redacted config leaked %q: %s", secret, data)
		}
	}
}

func TestValidateRelayRequiresClusterIdentityTokenAndAbsoluteTLSMaterial(t *testing.T) {
	cfg := config.Config{Mode: config.ModeCluster, Node: config.NodeConfig{ID: "node-a"}, Storage: config.StorageConfig{Driver: config.StorageMySQL, MySQL: config.MySQLConfig{DSN: "mysql://db", TLS: true}}, Registry: config.RegistryConfig{Type: config.RegistryDatabase}, Server: config.ServerConfig{Relay: config.RelayConfig{Enabled: true, Listen: ":9443", Endpoint: "relay.example:9443", CA: "ca.pem", Cert: "cert.pem", Key: "key.pem", ServerName: "relay.local"}}}
	if err := config.Validate(cfg); err == nil || !strings.Contains(err.Error(), "absolute") || !strings.Contains(err.Error(), "node token") {
		t.Fatalf("Validate() error = %v, want absolute paths and node token failures", err)
	}
	cfg.Server.Relay.CA, cfg.Server.Relay.Cert, cfg.Server.Relay.Key, cfg.Server.Relay.NodeToken = "/ca.pem", "/cert.pem", "/key.pem", "secret"
	applyStreamLatencyDefaults(&cfg)
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate(valid relay) error = %v", err)
	}
	cfg.Server.Relay.Listen = ""
	cfg.Server.Relay.Endpoint = "relay.example:9443"
	if err := config.Validate(cfg); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("Validate(endpoint-only relay) error = %v, want listen requirement", err)
	}
}

func TestValidateRelayRequiresAdvertisedEndpoint(t *testing.T) {
	cfg := config.Config{Mode: config.ModeCluster, Node: config.NodeConfig{ID: "node-a"}, Storage: config.StorageConfig{Driver: config.StorageMySQL, MySQL: config.MySQLConfig{DSN: "mysql://db", TLS: true}}, Registry: config.RegistryConfig{Type: config.RegistryDatabase}, Server: config.ServerConfig{Relay: config.RelayConfig{Enabled: true, Listen: ":9443", Endpoint: "relay.example:9443", CA: "/ca.pem", Cert: "/cert.pem", Key: "/key.pem", ServerName: "relay.local", NodeToken: "secret"}}}
	applyStreamLatencyDefaults(&cfg)
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate(valid relay) error = %v", err)
	}
	cfg.Server.Relay.Endpoint = ""
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("Validate(missing endpoint) error = %v, want endpoint requirement", err)
	}
}

func TestInferRelayEndpointUsesListenHostAndPort(t *testing.T) {
	endpoint, err := config.InferRelayEndpoint("10.1.2.3:9443")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "10.1.2.3:9443" {
		t.Fatalf("InferRelayEndpoint() = %q, want 10.1.2.3:9443", endpoint)
	}
}

func TestLoadInfersRelayEndpointAndAcceptsPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.yaml")
	file := "mode: cluster\nstorage:\n  driver: mysql\n  mysql:\n    dsn: mysql://db\nnode:\n  id: node-a\nserver:\n  relay:\n    enabled: true\n    listen: 127.0.0.1:9443\n    node_token: secret\n"
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Relay.Endpoint != "127.0.0.1:9443" {
		t.Fatalf("inferred relay endpoint = %q, want 127.0.0.1:9443", cfg.Server.Relay.Endpoint)
	}
	if cfg.Server.Relay.CA != "" || cfg.Server.Relay.Cert != "" || cfg.Server.Relay.Key != "" || cfg.Server.Relay.ServerName != "" {
		t.Fatalf("plaintext relay unexpectedly has TLS config: %+v", cfg.Server.Relay)
	}
}

func TestValidateRelayRejectsPartialTLSMaterial(t *testing.T) {
	base := config.Config{
		Mode: config.ModeCluster, Node: config.NodeConfig{ID: "node-a"},
		Storage:  config.StorageConfig{Driver: config.StorageMySQL, MySQL: config.MySQLConfig{DSN: "mysql://db"}},
		Registry: config.RegistryConfig{Type: config.RegistryDatabase},
		Server: config.ServerConfig{Relay: config.RelayConfig{
			Enabled: true, Listen: ":9443", Endpoint: "relay.example:9443", NodeToken: "secret",
		}},
	}
	partial := base
	partial.Server.Relay.CA = "/ca.pem"
	if err := config.Validate(partial); err == nil || !strings.Contains(err.Error(), "all CA, certificate, and key fields") {
		t.Fatalf("Validate(partial TLS) error = %v, want complete TLS material error", err)
	}

	missingServerName := base
	missingServerName.Server.Relay.CA = "/ca.pem"
	missingServerName.Server.Relay.Cert = "/cert.pem"
	missingServerName.Server.Relay.Key = "/key.pem"
	if err := config.Validate(missingServerName); err == nil || !strings.Contains(err.Error(), "server name") {
		t.Fatalf("Validate(missing server name) error = %v, want server name requirement", err)
	}

	plaintext := base
	applyStreamLatencyDefaults(&plaintext)
	if err := config.Validate(plaintext); err != nil {
		t.Fatalf("Validate(plaintext relay) error = %v", err)
	}

	disabled := base
	applyStreamLatencyDefaults(&disabled)
	disabled.Server.Relay.Enabled = false
	disabled.Server.Relay.Listen = ""
	disabled.Server.Relay.Endpoint = ""
	disabled.Server.Relay.NodeToken = ""
	if err := config.Validate(disabled); err != nil {
		t.Fatalf("Validate(disabled relay) error = %v", err)
	}
}

func TestProxyEntryDefaultsAndFlagOverride(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.Server.ProxyEntry.Enabled {
		t.Fatal("proxy entry must default to disabled")
	}
	if cfg.Server.ProxyEntry.Listen != "127.0.0.1:8089" {
		t.Fatalf("listen default = %q", cfg.Server.ProxyEntry.Listen)
	}
	if got := cfg.Server.ProxyEntry.TrustedProxies; len(got) != 2 || got[0] != "127.0.0.1/32" || got[1] != "::1/128" {
		t.Fatalf("trusted proxies default = %v", got)
	}
	if cfg.Server.ProxyEntry.RouteHeader != "X-TunnelMesh-Route" ||
		cfg.Server.ProxyEntry.ClientIPHeader != "X-TunnelMesh-Client-IP" ||
		cfg.Server.ProxyEntry.ClientPortHeader != "X-TunnelMesh-Client-Port" {
		t.Fatal("trusted header defaults changed")
	}
	if cfg.Server.ProxyEntry.ConnectTimeout != 10*time.Second ||
		cfg.Server.ProxyEntry.IdleTimeout != 300*time.Second ||
		cfg.Server.ProxyEntry.ShutdownTimeout != 30*time.Second {
		t.Fatal("timeout defaults changed")
	}
	if cfg.Server.ProxyEntry.MaxConcurrentTunnels != 512 ||
		cfg.Server.ProxyEntry.MaxHeaderBytes != 16384 ||
		cfg.Server.ProxyEntry.AuthBackoffThreshold != 5 {
		t.Fatal("limit defaults changed")
	}
}

func TestValidateProxyEntryAllowsWildcardTrustedProxies(t *testing.T) {
	base := func() config.Config {
		cfg, err := config.Load(context.Background(), config.ConfigOptions{})
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		cfg.Server.ProxyEntry.Enabled = true
		cfg.Server.ProxyEntry.DomainSuffix = "tm.example.com"
		return cfg
	}
	ok := base()
	if err := config.Validate(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	missingSuffix := base()
	missingSuffix.Server.ProxyEntry.DomainSuffix = ""
	if err := config.Validate(missingSuffix); err == nil {
		t.Fatal("enabled proxy entry requires domain_suffix")
	}
	badCIDR := base()
	badCIDR.Server.ProxyEntry.TrustedProxies = []string{"10.0.0.0/33"}
	if err := config.Validate(badCIDR); err == nil {
		t.Fatal("invalid trusted proxy CIDR must fail")
	}
	wildcardTrust := base()
	wildcardTrust.Server.ProxyEntry.Listen = "0.0.0.0:8089"
	wildcardTrust.Server.ProxyEntry.TrustedProxies = []string{"0.0.0.0/0"}
	if err := config.Validate(wildcardTrust); err != nil {
		t.Fatalf("valid wildcard trusted proxy config rejected: %v", err)
	}
	ipv6WildcardTrust := base()
	ipv6WildcardTrust.Server.ProxyEntry.Listen = "0.0.0.0:8089"
	ipv6WildcardTrust.Server.ProxyEntry.TrustedProxies = []string{"::/0"}
	if err := config.Validate(ipv6WildcardTrust); err != nil {
		t.Fatalf("valid IPv6 wildcard trusted proxy config rejected: %v", err)
	}
}

// vpnDocumentedExample is the server.vpn block from the embedded VPN gateway
// design spec §8, verbatim. Loading it in a test is what keeps the documentation
// and the loader honest: if a key is renamed here and not in the spec, or the
// spec advertises a key the loader ignores, this test fails.
const vpnDocumentedExample = `mode: local
server:
  vpn:
    enabled: true
    listen: "0.0.0.0:51820"
    endpoint_host: "gw-1.mesh.example.com"
    ip_pool: "10.64.0.0/16"
    node_subnet_size: 24
    mtu: 1420
    max_peers: 0
    max_flows_per_peer: 128
    max_flows_total: 0
    packet_rate_per_peer: 0
    connect_timeout: 10s
    idle_timeout: 120s
    shutdown_timeout: 15s
    icmp_enabled: true
    icmp_timeout: 5s
    icmp_max_concurrent: 64
`

func loadVPNFile(t *testing.T, body string) config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vpn.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

func TestVPNDefaults(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	vpn := cfg.Server.VPN
	if vpn.Enabled {
		t.Fatal("server.vpn must default to disabled so an upgrade changes nothing")
	}
	if vpn.Listen != "0.0.0.0:51820" {
		t.Errorf("listen default = %q", vpn.Listen)
	}
	if vpn.EndpointHost != "gw-1.mesh.example.com" {
		t.Errorf("endpoint_host default = %q", vpn.EndpointHost)
	}
	if vpn.IPPool != "10.64.0.0/16" {
		t.Errorf("ip_pool default = %q", vpn.IPPool)
	}
	if vpn.NodeSubnetSize != 24 {
		t.Errorf("node_subnet_size default = %d", vpn.NodeSubnetSize)
	}
	if vpn.MTU != 1420 {
		t.Errorf("mtu default = %d", vpn.MTU)
	}
	if vpn.MaxPeers != 0 || vpn.MaxFlowsTotal != 0 || vpn.PacketRatePerPeer != 0 {
		t.Errorf("the zero-means-unlimited defaults changed: max_peers=%d max_flows_total=%d packet_rate_per_peer=%d",
			vpn.MaxPeers, vpn.MaxFlowsTotal, vpn.PacketRatePerPeer)
	}
	if vpn.MaxFlowsPerPeer != 128 {
		t.Errorf("max_flows_per_peer default = %d", vpn.MaxFlowsPerPeer)
	}
	if vpn.ConnectTimeout != 10*time.Second || vpn.IdleTimeout != 120*time.Second || vpn.ShutdownTimeout != 15*time.Second {
		t.Errorf("timeout defaults changed: %s %s %s", vpn.ConnectTimeout, vpn.IdleTimeout, vpn.ShutdownTimeout)
	}
	if !vpn.ICMPEnabled {
		t.Error("icmp_enabled must default to true so the capability is governed by agent negotiation, not by a silent default")
	}
	if vpn.ICMPTimeout != 5*time.Second || vpn.ICMPMaxConcurrent != 64 {
		t.Errorf("icmp defaults changed: %s %d", vpn.ICMPTimeout, vpn.ICMPMaxConcurrent)
	}
	// The defaults must themselves be a valid configuration. A shipped default
	// that fails Validate would make every operator's first "enabled: true" an
	// exercise in guessing which key is wrong.
	enabled := cfg
	enabled.Server.VPN.Enabled = true
	if err := config.Validate(enabled); err != nil {
		t.Fatalf("the documented defaults must validate once enabled: %v", err)
	}
}

func TestValidateVPNAcceptsTheDocumentedExample(t *testing.T) {
	cfg := loadVPNFile(t, vpnDocumentedExample)
	if !cfg.Server.VPN.Enabled {
		t.Fatal("the documented example enables the gateway")
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("the documented example must validate: %v", err)
	}
}

// TestValidateVPNSkipsEverythingWhenDisabled is the upgrade safety property. A
// deployment that never touches server.vpn must keep loading even if the keys it
// inherited from an example file are nonsense.
func TestValidateVPNSkipsEverythingWhenDisabled(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Server.VPN.Enabled = false
	cfg.Server.VPN.Listen = "not-a-listen-address"
	cfg.Server.VPN.EndpointHost = ""
	cfg.Server.VPN.IPPool = "10.64.0.0/33"
	cfg.Server.VPN.NodeSubnetSize = 99
	cfg.Server.VPN.MTU = 1
	cfg.Server.VPN.MaxPeers = -5
	cfg.Server.VPN.ICMPTimeout = 0
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("a disabled gateway must contribute no problems: %v", err)
	}
}

func TestValidateVPNRejectsBadValues(t *testing.T) {
	base := func() config.Config {
		cfg := loadVPNFile(t, vpnDocumentedExample)
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("baseline must validate: %v", err)
		}
		return cfg
	}
	cases := map[string]func(*config.Config){
		"listen without a port":   func(c *config.Config) { c.Server.VPN.Listen = "0.0.0.0" },
		"empty listen":            func(c *config.Config) { c.Server.VPN.Listen = "" },
		"empty endpoint host":     func(c *config.Config) { c.Server.VPN.EndpointHost = "" },
		"endpoint host with port": func(c *config.Config) { c.Server.VPN.EndpointHost = "gw-1.mesh.example.com:51820" },
		"endpoint host invalid":   func(c *config.Config) { c.Server.VPN.EndpointHost = "not a host" },
		"empty ip pool":           func(c *config.Config) { c.Server.VPN.IPPool = "" },
		"ip pool not a cidr":      func(c *config.Config) { c.Server.VPN.IPPool = "10.64.0.0" },
		"ip pool bad prefix":      func(c *config.Config) { c.Server.VPN.IPPool = "10.64.0.0/33" },
		"ip pool ipv6":            func(c *config.Config) { c.Server.VPN.IPPool = "fd00::/64" },
		"ip pool link local":      func(c *config.Config) { c.Server.VPN.IPPool = "169.254.0.0/16" },
		"ip pool multicast":       func(c *config.Config) { c.Server.VPN.IPPool = "224.0.0.0/4" },
		// One typo turning a /8 into 65536 subnets is a startup memory
		// exhaustion vector, so the count cap must surface at load time.
		"ip pool too many subnets": func(c *config.Config) {
			c.Server.VPN.IPPool = "10.0.0.0/8"
			c.Server.VPN.NodeSubnetSize = 24
		},
		"subnet size equal to pool":   func(c *config.Config) { c.Server.VPN.NodeSubnetSize = 16 },
		"subnet size wider than pool": func(c *config.Config) { c.Server.VPN.NodeSubnetSize = 8 },
		"subnet size above /30":       func(c *config.Config) { c.Server.VPN.NodeSubnetSize = 31 },
		"subnet size zero":            func(c *config.Config) { c.Server.VPN.NodeSubnetSize = 0 },
		"mtu zero":                    func(c *config.Config) { c.Server.VPN.MTU = 0 },
		"mtu below the floor":         func(c *config.Config) { c.Server.VPN.MTU = 575 },
		"mtu above the ceiling":       func(c *config.Config) { c.Server.VPN.MTU = 1501 },
		"negative max peers":          func(c *config.Config) { c.Server.VPN.MaxPeers = -1 },
		"negative flows per peer":     func(c *config.Config) { c.Server.VPN.MaxFlowsPerPeer = -1 },
		"negative flows total":        func(c *config.Config) { c.Server.VPN.MaxFlowsTotal = -1 },
		"negative packet rate":        func(c *config.Config) { c.Server.VPN.PacketRatePerPeer = -1 },
		"zero connect timeout":        func(c *config.Config) { c.Server.VPN.ConnectTimeout = 0 },
		"negative connect timeout":    func(c *config.Config) { c.Server.VPN.ConnectTimeout = -time.Second },
		"zero idle timeout":           func(c *config.Config) { c.Server.VPN.IdleTimeout = 0 },
		"negative shutdown timeout":   func(c *config.Config) { c.Server.VPN.ShutdownTimeout = -time.Second },
		"icmp timeout zero":           func(c *config.Config) { c.Server.VPN.ICMPTimeout = 0 },
		"icmp concurrency zero":       func(c *config.Config) { c.Server.VPN.ICMPMaxConcurrent = 0 },
		"icmp concurrency negative":   func(c *config.Config) { c.Server.VPN.ICMPMaxConcurrent = -1 },
	}
	for name, mutate := range cases {
		cfg := base()
		mutate(&cfg)
		if err := config.Validate(cfg); err == nil {
			t.Errorf("%s: Validate accepted an invalid server.vpn section", name)
		}
	}
}

// TestValidateVPNICMPDisabledSkipsICMPBounds keeps a peer that never asked for
// ping working even with placeholder icmp timings, so turning the capability off
// cannot itself break startup.
func TestValidateVPNICMPDisabledSkipsICMPBounds(t *testing.T) {
	cfg := loadVPNFile(t, vpnDocumentedExample)
	cfg.Server.VPN.ICMPEnabled = false
	cfg.Server.VPN.ICMPTimeout = 0
	cfg.Server.VPN.ICMPMaxConcurrent = 0
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("icmp bounds must not apply while icmp is disabled: %v", err)
	}
}

func TestValidateVPNAcceptsTheLegalBoundaries(t *testing.T) {
	cfg := loadVPNFile(t, vpnDocumentedExample)
	cfg.Server.VPN.MTU = 576
	cfg.Server.VPN.ShutdownTimeout = 0
	cfg.Server.VPN.MaxPeers = 1
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("the lower boundaries must be accepted: %v", err)
	}

	cfg = loadVPNFile(t, vpnDocumentedExample)
	cfg.Server.VPN.MTU = 1500
	cfg.Server.VPN.IPPool = "10.64.0.0/24"
	cfg.Server.VPN.NodeSubnetSize = 30
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("the narrowest legal pool must be accepted: %v", err)
	}

	cfg = loadVPNFile(t, vpnDocumentedExample)
	cfg.Server.VPN.IPPool = "100.64.0.0/16"
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("carrier-grade NAT space must be accepted: %v", err)
	}

	cfg = loadVPNFile(t, vpnDocumentedExample)
	cfg.Server.VPN.IPPool = "10.0.0.0/8"
	cfg.Server.VPN.NodeSubnetSize = 20
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("exactly the subnet count cap must be accepted: %v", err)
	}
}

func TestVPNEnvironmentOverridesFileAndCLIOverridesEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vpn.yaml")
	if err := os.WriteFile(path, []byte(vpnDocumentedExample), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("TUNNELMESH_SERVER_VPN_IP_POOL", "10.99.0.0/16")
	t.Setenv("TUNNELMESH_SERVER_VPN_MTU", "1300")
	t.Setenv("TUNNELMESH_SERVER_VPN_NODE_SUBNET_SIZE", "25")

	cfg, err := config.Load(context.Background(), config.ConfigOptions{
		ConfigFile: path,
		CLI: map[string]any{
			"server.vpn.mtu": 1380,
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Server.VPN.IPPool; got != "10.99.0.0/16" {
		t.Errorf("environment did not override the file ip_pool: %q", got)
	}
	if got := cfg.Server.VPN.NodeSubnetSize; got != 25 {
		t.Errorf("environment did not override node_subnet_size: %d", got)
	}
	if got := cfg.Server.VPN.MTU; got != 1380 {
		t.Errorf("CLI did not override the environment mtu: %d", got)
	}
	// Untouched keys still come from the file.
	if got := cfg.Server.VPN.EndpointHost; got != "gw-1.mesh.example.com" {
		t.Errorf("the file value for endpoint_host was lost: %q", got)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("the overridden configuration must still validate: %v", err)
	}
}

// TestVPNLoadFailsOnAnUnusablePool is the fast-fail requirement. An operator
// must learn that ip_pool is unusable at startup, with a message naming the key,
// rather than at the first peer issuance.
func TestVPNLoadFailsOnAnUnusablePool(t *testing.T) {
	cfg := loadVPNFile(t, vpnDocumentedExample)
	cfg.Server.VPN.IPPool = "10.64.0.0/33"
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("an unusable ip_pool must fail validation")
	}
	message := err.Error()
	if !strings.Contains(message, "vpn") {
		t.Errorf("the error must name the vpn section, got %q", message)
	}
	if !strings.Contains(message, "ip_pool") {
		t.Errorf("the error must name the offending key, got %q", message)
	}
}

// TestValidateServerHTTPBoundsRejectsNegatives pins that an operator cannot turn a
// bound off with a negative number: zero means "built-in safe default", so a
// negative value is a typo that must fail fast at startup.
func TestValidateServerHTTPBoundsRejectsNegatives(t *testing.T) {
	base, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*config.Config)
		want string
	}{
		{name: "read header timeout", edit: func(c *config.Config) { c.Server.HTTP.ReadHeaderTimeout = -time.Second }, want: "server http read header timeout must not be negative"},
		{name: "idle timeout", edit: func(c *config.Config) { c.Server.HTTP.IdleTimeout = -time.Second }, want: "server http idle timeout must not be negative"},
		{name: "max header bytes", edit: func(c *config.Config) { c.Server.HTTP.MaxHeaderBytes = -1 }, want: "server http max header bytes must not be negative"},
		{name: "body timeout", edit: func(c *config.Config) { c.Server.HTTP.BodyTimeout = -time.Second }, want: "server http body timeout must not be negative"},
		{name: "max connections", edit: func(c *config.Config) { c.Server.HTTP.MaxConcurrentConnections = -5 }, want: "server http max connections must not be negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.edit(&cfg)
			if err := config.Validate(cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
	// Zero connection shedding is the documented default and must validate, so an
	// upgrade cannot start refusing connections.
	cfg := base
	cfg.Server.HTTP = config.ServerHTTPConfig{}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate() with the zero HTTP block error = %v, want no error", err)
	}
}

// TestServerHTTPDefaultsMatchSafeFloors keeps the viper defaults and the
// WithSafeDefaults floors in sync, which is what lets a hand-built RuntimeConfig be
// as protected as a loaded configuration file.
func TestServerHTTPDefaultsMatchSafeFloors(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.HTTP.MaxConcurrentConnections != 0 {
		t.Fatalf("MaxConcurrentConnections = %d, want 0 so upgrading changes nothing", cfg.Server.HTTP.MaxConcurrentConnections)
	}
	if got, want := cfg.Server.HTTP, (config.ServerHTTPConfig{}).WithSafeDefaults(); got != want {
		t.Fatalf("loaded HTTP block = %+v, want the safe defaults %+v", got, want)
	}
}

// TestServerHTTPEnvironmentOverrides proves the new keys follow the same
// TUNNELMESH_ prefix rule as every other setting.
func TestServerHTTPEnvironmentOverrides(t *testing.T) {
	t.Setenv("TUNNELMESH_SERVER_HTTP_IDLE_TIMEOUT", "45s")
	t.Setenv("TUNNELMESH_SERVER_HTTP_MAX_CONNECTIONS", "64")
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.HTTP.IdleTimeout != 45*time.Second {
		t.Fatalf("IdleTimeout = %v, want 45s", cfg.Server.HTTP.IdleTimeout)
	}
	if cfg.Server.HTTP.MaxConcurrentConnections != 64 {
		t.Fatalf("MaxConcurrentConnections = %d, want 64", cfg.Server.HTTP.MaxConcurrentConnections)
	}
}

// TestServerAgentConnectionCapacityDefaults pins that the advertised and enforced
// per-Agent connection ceiling come from one key, and that dropping the block keeps
// the number the protocol has always advertised.
func TestServerAgentConnectionCapacityDefaults(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Agents.MaxConnectionsPerAgent != config.DefaultAgentMaxConnectionsPerAgent {
		t.Fatalf("MaxConnectionsPerAgent = %d, want the default %d", cfg.Server.Agents.MaxConnectionsPerAgent, config.DefaultAgentMaxConnectionsPerAgent)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	// Zero is the "apply the default" value, so a hand-built Config cannot be
	// rejected for omitting the block, and only a negative number is a typo.
	zero := cfg
	zero.Server.Agents.MaxConnectionsPerAgent = 0
	if err := config.Validate(zero); err != nil {
		t.Fatalf("Validate() with a zero ceiling error = %v, want the default ceiling", err)
	}
	negative := cfg
	negative.Server.Agents.MaxConnectionsPerAgent = -1
	if err := config.Validate(negative); err == nil || !strings.Contains(err.Error(), "server agents max connections per agent must not be negative") {
		t.Fatalf("Validate() with a negative ceiling error = %v, want a refusal", err)
	}
	t.Setenv("TUNNELMESH_SERVER_AGENTS_MAX_CONNECTIONS_PER_AGENT", "8")
	fromEnv, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("Load() with the environment override error = %v", err)
	}
	if fromEnv.Server.Agents.MaxConnectionsPerAgent != 8 {
		t.Fatalf("MaxConnectionsPerAgent = %d, want the environment value 8", fromEnv.Server.Agents.MaxConnectionsPerAgent)
	}
}

// TestServerMetricsTokenPolicy covers the three states an operator can choose: no
// token (endpoint open as before), a strong token, and a token too short to be worth
// brute-forcing across a public listener.
func TestServerMetricsTokenPolicy(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Metrics.Token != "" {
		t.Fatalf("default token = %q, want empty so /metrics keeps its current behaviour", cfg.Server.Metrics.Token)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate() with no metrics token error = %v", err)
	}

	short := cfg
	short.Server.Metrics.Token = "abc123"
	if err := config.Validate(short); err == nil || !strings.Contains(err.Error(), "server metrics token must be at least 16 characters") {
		t.Fatalf("Validate() with a short token error = %v, want a refusal", err)
	}

	strong := cfg
	strong.Server.Metrics.Token = "0123456789abcdef0123456789abcdef"
	if err := config.Validate(strong); err != nil {
		t.Fatalf("Validate() with a strong token error = %v", err)
	}
	// The guard exists to keep the credential out of console output and logs.
	data, err := strong.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "0123456789abcdef") {
		t.Fatalf("RedactedJSON() leaked the metrics token: %s", data)
	}

	t.Setenv("TUNNELMESH_SERVER_METRICS_TOKEN", "env-token-value-long-enough")
	fromEnv, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatalf("Load() with the environment token error = %v", err)
	}
	if fromEnv.Server.Metrics.Token != "env-token-value-long-enough" {
		t.Fatalf("token = %q, want the environment value", fromEnv.Server.Metrics.Token)
	}
}
