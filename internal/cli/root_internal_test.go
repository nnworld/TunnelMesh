package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestServerRunWritesMissingNodeIDToConfig(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "server.yaml")
	contents := "# keep this comment\nmode: cluster\nstorage:\n  driver: mysql\n  mysql:\n    dsn: db\nregistry:\n  type: database\n"
	if err := os.WriteFile(configFile, []byte(contents), 0o640); err != nil {
		t.Fatal(err)
	}
	nodeIDPath := filepath.Join(dir, "node-id")
	var loaded config.Config
	var opts *rootOptions
	root := newRoot("tunnelmesh-server", func(o *rootOptions) []*cobra.Command {
		opts = o
		return []*cobra.Command{configCommand(o, "run", "test run", func(_ *cobra.Command, cfg config.Config) error {
			loaded = cfg
			return errors.New("stop after config load")
		})}
	})
	opts.nodeIDPath = nodeIDPath
	root.SetArgs([]string{"run", "--config", configFile})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "stop after config load") {
		t.Fatalf("Execute() error = %v, want test sentinel", err)
	}
	if !strings.HasPrefix(loaded.Node.ID, "server-") {
		t.Fatalf("run node ID = %q, want generated identity", loaded.Node.ID)
	}
	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# keep this comment") || !strings.Contains(string(data), "node:\n  id: server-") {
		t.Fatalf("run did not persist generated node ID: %s", data)
	}
}

func TestServerCheckConfigDoesNotWriteMissingNodeIDToConfig(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "server.yaml")
	contents := "mode: cluster\nstorage:\n  driver: mysql\n  mysql:\n    dsn: db\nregistry:\n  type: database\n"
	if err := os.WriteFile(configFile, []byte(contents), 0o640); err != nil {
		t.Fatal(err)
	}
	var opts *rootOptions
	root := newRoot("tunnelmesh-server", func(o *rootOptions) []*cobra.Command {
		opts = o
		return []*cobra.Command{configCommand(o, "check-config", "test check", func(*cobra.Command, config.Config) error { return nil })}
	})
	opts.nodeIDPath = filepath.Join(dir, "node-id")
	root.SetArgs([]string{"check-config", "--config", configFile})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "node:") {
		t.Fatalf("check-config unexpectedly rewrote config: %s", data)
	}
	if _, err := os.Stat(opts.nodeIDPath); !os.IsNotExist(err) {
		t.Fatalf("check-config unexpectedly wrote node identity file: %v", err)
	}
}
