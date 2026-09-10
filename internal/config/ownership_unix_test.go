//go:build unix

package config_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestInitializeNodeIDPreservesConfigOwnership(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(configPath, []byte("mode: cluster\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.InitializeNodeID(configPath, filepath.Join(dir, "node-id")); err != nil {
		t.Fatalf("InitializeNodeID() error = %v", err)
	}
	after, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := before.Sys().(*syscall.Stat_t)
	got := after.Sys().(*syscall.Stat_t)
	if got.Uid != want.Uid || got.Gid != want.Gid {
		t.Fatalf("ownership = %d:%d, want %d:%d", got.Uid, got.Gid, want.Uid, want.Gid)
	}
}
