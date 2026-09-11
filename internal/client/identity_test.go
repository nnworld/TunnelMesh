package client_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestEnsureClientInstanceIDIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client-instance-id")
	first, err := client.EnsureClientInstanceID(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.EnsureClientInstanceID(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("instance identity changed: %q -> %q", first, second)
	}
	if !strings.HasPrefix(first, "client-") || strings.ToLower(first) != first {
		t.Fatalf("instance identity must be lowercase and client-prefixed: %q", first)
	}
}

func TestDefaultClientInstanceIDPathUsesDarwinUserStateDirectory(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin user state path is only applicable on macOS")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Library", "Application Support", "TunnelMesh", "client-instance-id")
	if got := client.DefaultClientInstanceIDPath(); got != want {
		t.Fatalf("DefaultClientInstanceIDPath() = %q, want %q", got, want)
	}
}

func TestClientConfigAcceptsExplicitInstanceID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.yaml")
	contents := "mode: local\nclient:\n  server_url: wss://server.example/ws/client\n  instance_id: client-explicit-001\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Client.InstanceID != "client-explicit-001" {
		t.Fatalf("explicit instance ID = %q", cfg.Client.InstanceID)
	}
}
