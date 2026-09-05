package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/cli"
)

func TestServerRootExposesConfigurationCommands(t *testing.T) {
	root := cli.NewServerRoot()
	for _, name := range []string{"run", "check-config", "init-db", "print-config"} {
		if root.CommandPath() == "" {
			t.Fatal("root command has no path")
		}
		if findCommand(root, name) == nil {
			t.Fatalf("server root missing %q command", name)
		}
	}
}

func TestAgentAndClientRootsExposeCommandShells(t *testing.T) {
	agent := cli.NewAgentRoot()
	for _, name := range []string{"run", "register", "check-config", "id"} {
		if findCommand(agent, name) == nil {
			t.Fatalf("agent root missing %q command", name)
		}
	}
	client := cli.NewClientRoot()
	for _, name := range []string{"login", "agent", "tunnel", "forward", "publish", "proxy", "stop", "status"} {
		if findCommand(client, name) == nil {
			t.Fatalf("client root missing %q command", name)
		}
	}
}

func TestCheckConfigCommandLoadsCLIOverrides(t *testing.T) {
	root := cli.NewServerRoot()
	root.SetArgs([]string{"check-config", "--mode", "local", "--storage.driver", "sqlite"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "valid") {
		t.Fatalf("output = %q, want validation confirmation", out.String())
	}
}

func TestPrintConfigFlatTCPBridgeFlagCanDisableBridge(t *testing.T) {
	root := cli.NewServerRoot()
	root.SetArgs([]string{"print-config", "--server.tcp_bridge_enabled=false"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(out.String(), `"enabled": true`) {
		t.Fatalf("output = %q, flat bridge flag was ignored", out.String())
	}
	if !strings.Contains(out.String(), `"enabled": false`) {
		t.Fatalf("output = %q, want disabled nested bridge value", out.String())
	}
}

func findCommand(root *cobra.Command, name string) *cobra.Command {
	for _, command := range root.Commands() {
		if command.Name() == name {
			return command
		}
	}
	return nil
}
