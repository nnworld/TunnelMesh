package config_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

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

func TestValidateRejectsIncompleteClusterAndUnknownRegistry(t *testing.T) {
	base := config.Config{Mode: config.ModeCluster}
	if err := config.Validate(base); err == nil {
		t.Fatal("Validate() error = nil for incomplete cluster config")
	} else {
		for _, want := range []string{"mysql dsn", "tls", "node"} {
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
