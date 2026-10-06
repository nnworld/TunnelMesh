package tray

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestConfigStoreLoadMissingFileReturnsDefaults(t *testing.T) {
	store := NewConfigStore(filepath.Join(t.TempDir(), ClientConfigFileName))
	settings, err := store.Load()
	if err != nil {
		t.Fatalf("Load on a missing file must succeed: %v", err)
	}
	if settings.Mode != config.ModeLocal {
		t.Fatalf("Mode = %q, want %q", settings.Mode, config.ModeLocal)
	}
	if settings.ServerURL != "" || settings.Token != "" || len(settings.Tunnels) != 0 {
		t.Fatalf("defaults = %+v", settings)
	}
	if store.Exists() {
		t.Fatal("Exists() = true for a file that was never written")
	}
}

func TestConfigStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ClientConfigFileName)
	store := NewConfigStore(path)
	want := ClientSettings{
		Mode:      config.ModeLocal,
		ServerURL: "wss://server.example/ws/client",
		Token:     "super-secret-token",
		Tunnels: []config.TunnelConfig{
			{Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:8080", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 80},
			// The non-loopback listener is the shape that makes config.Validate demand
			// allow_remote, so the round trip has to carry it: a store that dropped the field
			// would save a configuration the tray's own 检测 then rejects.
			{Name: "proxy", Protocol: "socks5", ListenAddr: "0.0.0.0:1080", AgentID: "agent-b",
				AuthMode: "password", AllowRemote: true},
		},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != want.Mode || got.ServerURL != want.ServerURL || got.Token != want.Token {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if len(got.Tunnels) != 2 {
		t.Fatalf("tunnels = %+v", got.Tunnels)
	}
	if got.Tunnels[0] != want.Tunnels[0] || got.Tunnels[1] != want.Tunnels[1] {
		t.Fatalf("tunnels = %+v, want %+v", got.Tunnels, want.Tunnels)
	}
	// Read from the file rather than from memory: Load() above proves the store keeps it,
	// this proves the key actually reaches client.yaml for the CLI and for an operator
	// editing the file by hand.
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "allow_remote: true") {
		t.Errorf("client.yaml must carry allow_remote, got:\n%s", written)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("client.yaml permissions = %v, want owner-only; it holds a token", info.Mode().Perm())
	}
	if !store.Exists() {
		t.Fatal("Exists() = false after Save")
	}
}

// TestConfigStorePreservesForeignKeysAndComments is the reason the store edits a YAML
// node tree instead of re-marshalling a struct: client.yaml is shared with the CLI and
// may carry storage, connection-pool and operator comments the tray does not own.
func TestConfigStorePreservesForeignKeysAndComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ClientConfigFileName)
	original := `# Operator notes must survive a tray save.
mode: local
storage:
  driver: sqlite
  sqlite:
    path: /tmp/kept.db
client:
  # The pool tuning below is not editable in the tray.
  connections:
    min: 3
    max: 9
  server_url: ws://old.example/ws/client
  token: old-token
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewConfigStore(path)
	settings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.ServerURL != "ws://old.example/ws/client" || settings.Token != "old-token" {
		t.Fatalf("loaded = %+v", settings)
	}

	settings.ServerURL = "wss://new.example/ws/client"
	settings.Token = "new-token"
	settings.Tunnels = []config.TunnelConfig{{Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:8080", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 80}}
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	written := string(data)
	for _, want := range []string{
		"# Operator notes must survive a tray save.",
		"# The pool tuning below is not editable in the tray.",
		"driver: sqlite",
		"path: /tmp/kept.db",
		"min: 3",
		"max: 9",
		"wss://new.example/ws/client",
		"new-token",
	} {
		if !strings.Contains(written, want) {
			t.Fatalf("saved client.yaml lost %q:\n%s", want, written)
		}
	}
	if strings.Contains(written, "old-token") || strings.Contains(written, "old.example") {
		t.Fatalf("stale values survived the save:\n%s", written)
	}

	// The foreign keys must still parse through the real loader, not just look right.
	cfg, err := store.LoadConfig(context.Background())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Storage.SQLite.Path != "/tmp/kept.db" || cfg.Client.Connections.Min != 3 || cfg.Client.Connections.Max != 9 {
		t.Fatalf("foreign keys = %+v", cfg.Client.Connections)
	}
}

// TestConfigStoreTokenIsReadableByExistingClient is the load-bearing guarantee behind
// storing the token in client.yaml: ClientConfig.Token carries yaml:"-", yet
// config.Load decodes through viper and mapstructure, so the file value is picked up
// without changing the existing client. RedactedJSON must still hide it.
func TestConfigStoreTokenIsReadableByExistingClient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ClientConfigFileName)
	store := NewConfigStore(path)
	if err := store.Save(ClientSettings{
		Mode:      config.ModeLocal,
		ServerURL: "wss://server.example/ws/client",
		Token:     "super-secret-token",
		Tunnels: []config.TunnelConfig{
			{Name: "web", Protocol: "tcp", ListenAddr: "127.0.0.1:8080", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 80},
		},
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Client.Token != "super-secret-token" {
		t.Fatalf("config.Load did not read client.token from the file, got %q", cfg.Client.Token)
	}
	if cfg.Client.ServerURL != "wss://server.example/ws/client" || len(cfg.Client.Tunnels) != 1 {
		t.Fatalf("loaded config = %+v", cfg.Client)
	}

	redacted, err := cfg.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(redacted), "super-secret-token") {
		t.Fatalf("RedactedJSON leaked the token: %s", redacted)
	}
}

func TestConfigStoreSurfacesInvalidConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ClientConfigFileName)
	store := NewConfigStore(path)
	// cluster mode requires MySQL storage, which a client configuration never has.
	// The tray has to be able to surface that instead of launching a doomed client.
	if err := store.Save(ClientSettings{Mode: config.ModeCluster, ServerURL: "wss://server.example/ws/client", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadConfig(context.Background()); err == nil {
		t.Fatal("cluster mode without MySQL storage must fail validation")
	} else if !strings.Contains(err.Error(), "cluster mode requires mysql") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestConfigStoreSaveEmptyTunnelsWritesEmptySequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), ClientConfigFileName)
	store := NewConfigStore(path)
	if err := store.Save(ClientSettings{Mode: config.ModeLocal, ServerURL: "wss://s.example/ws/client", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "null") {
		t.Fatalf("empty tunnels rendered as null:\n%s", data)
	}
	settings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Tunnels) != 0 {
		t.Fatalf("tunnels = %+v, want none", settings.Tunnels)
	}
}
