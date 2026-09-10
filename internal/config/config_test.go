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
		{name: "max above limit", edit: func(c *config.AgentConnectionConfig) { c.Max = 65 }, want: "agent connections max must be at most 64"},
		{name: "low above high", edit: func(c *config.AgentConnectionConfig) { c.Max, c.LowWatermark = 8, 17 }, want: "agent connections low watermark must be less than or equal to high watermark"},
		{name: "invalid evaluation interval", edit: func(c *config.AgentConnectionConfig) { c.EvaluationInterval = 0 }, want: "agent connections evaluation interval must be positive"},
		{name: "invalid cooldown", edit: func(c *config.AgentConnectionConfig) { c.Cooldown = 0 }, want: "agent connections cooldown must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.edit(&cfg.Agent.Connections)
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

func TestLoadAndValidateDynamicRouteSuffix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tunnelmesh.yaml")
	if err := os.WriteFile(path, []byte("server:\n  dynamic_suffix: claw.qihoo.net\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.DynamicSuffix != "claw.qihoo.net" {
		t.Fatalf("Server.DynamicSuffix = %q, want %q", cfg.Server.DynamicSuffix, "claw.qihoo.net")
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
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate(valid relay) error = %v", err)
	}
	cfg.Server.Relay.Endpoint = ""
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("Validate(missing endpoint) error = %v, want endpoint requirement", err)
	}
}
