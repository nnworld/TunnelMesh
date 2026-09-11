package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/cli"
)

func TestServerRootExposesConfigurationCommands(t *testing.T) {
	root := cli.NewServerRoot()
	for _, name := range []string{"run", "check-config", "init-db", "init-node-id", "print-config", "admin"} {
		if root.CommandPath() == "" {
			t.Fatal("root command has no path")
		}
		if findCommand(root, name) == nil {
			t.Fatalf("server root missing %q command", name)
		}
	}
}

func TestServerAdminExposesCredentialRegeneration(t *testing.T) {
	root := cli.NewServerRoot()
	admin := findCommand(root, "admin")
	if admin == nil || findCommand(admin, "regenerate-credentials") == nil {
		t.Fatal("admin credential regeneration command missing")
	}
	regen := findCommand(admin, "regenerate-credentials")
	if regen.Flag("confirm") == nil {
		t.Fatal("regeneration command missing --confirm")
	}
}

func TestServerAdminExposesBootstrap(t *testing.T) {
	root := cli.NewServerRoot()
	admin := findCommand(root, "admin")
	if admin == nil || findCommand(admin, "bootstrap") == nil {
		t.Fatal("admin bootstrap command missing")
	}
}

func TestServerAdminBootstrapCreatesCredentialsOnlyForEmptyDatabase(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "server.yaml")
	contents := "mode: local\nstorage:\n  driver: sqlite\n  auto_init: true\n  sqlite:\n    path: " + filepath.Join(dir, "runtime.db") + "\n"
	if err := os.WriteFile(configFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	root := cli.NewServerRoot()
	root.SetArgs([]string{"admin", "bootstrap", "--config", configFile})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("bootstrap Execute() error = %v", err)
	}
	printed := out.String()
	if !strings.Contains(printed, "admin username: ") || !strings.Contains(printed, "admin password: ") {
		t.Fatalf("bootstrap output = %q", printed)
	}

	second := cli.NewServerRoot()
	second.SetArgs([]string{"admin", "bootstrap", "--config", configFile})
	second.SetOut(&out)
	second.SetErr(&out)
	err := second.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "admin account already exists") {
		t.Fatalf("second bootstrap error = %v, want existing-admin error", err)
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
	if client.PersistentFlags().Lookup("client.token") == nil {
		t.Fatal("client root missing --client.token")
	}
	for _, name := range []string{"login", "agent", "tunnel", "forward", "publish", "proxy", "stop", "status"} {
		if findCommand(client, name) == nil {
			t.Fatalf("client root missing %q command", name)
		}
	}
}

func TestClientRootExposesSOCKS5Forward(t *testing.T) {
	client := cli.NewClientRoot()
	forward := findCommand(client, "forward")
	if forward == nil {
		t.Fatal("client root missing forward command")
	}
	socks5 := findCommand(forward, "socks5")
	if socks5 == nil {
		t.Fatal("client forward command missing socks5")
	}
	for _, flag := range []string{"listen", "agent", "auth", "allow-remote"} {
		if socks5.Flag(flag) == nil {
			t.Fatalf("socks5 command missing --%s", flag)
		}
	}
	if socks5.Flag("auth-url") == nil {
		t.Fatal("socks5 command missing --auth-url")
	}
}

func TestClientRootExposesHTTPProxyForward(t *testing.T) {
	client := cli.NewClientRoot()
	forward := findCommand(client, "forward")
	if forward == nil {
		t.Fatal("client root missing forward command")
	}
	httpProxy := findCommand(forward, "http-proxy")
	if httpProxy == nil {
		t.Fatal("client forward command missing http-proxy")
	}
	for _, flag := range []string{"listen", "agent", "auth", "allow-remote"} {
		if httpProxy.Flag(flag) == nil {
			t.Fatalf("http-proxy command missing --%s", flag)
		}
	}
	if httpProxy.Flag("auth-url") == nil {
		t.Fatal("http-proxy command missing --auth-url")
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
	var printed struct {
		Server struct {
			TCPBridge struct {
				Enabled bool `json:"enabled"`
			} `json:"tcp_bridge"`
			TCPBridgeEnabled bool `json:"tcp_bridge_enabled"`
		} `json:"server"`
	}
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
		t.Fatalf("decode print-config output: %v\n%s", err, out.String())
	}
	if printed.Server.TCPBridge.Enabled || printed.Server.TCPBridgeEnabled {
		t.Fatalf("flat bridge flag was ignored: %+v", printed.Server)
	}
}

func TestPrintConfigAcceptsSecurityAndNativeTLSFlags(t *testing.T) {
	root := cli.NewServerRoot()
	root.SetArgs([]string{
		"print-config",
		"--security.allowed_hosts", "secure.example",
		"--security.allowed_origins", "https://secure.example",
		"--security.allow_legacy_connection_tokens",
		"--tls.enabled",
		"--tls.cert_file", "server-cert.pem",
		"--tls.key_file", "/secrets/server-key.pem",
		"--tls.min_version", "1.3",
	})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	printed := out.String()
	for _, want := range []string{"secure.example", "https://secure.example", "server-cert.pem", `"min_version": "1.3"`, `"allow_legacy_connection_tokens": true`} {
		if !strings.Contains(printed, want) {
			t.Fatalf("output = %q, want %q", printed, want)
		}
	}
	if strings.Contains(printed, "/secrets/server-key.pem") {
		t.Fatalf("output leaked TLS private key path: %q", printed)
	}
}

func TestServerRunFailsFastWhenNativeTLSCertificateCannotLoad(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "server.yaml")
	contents := "mode: local\nstorage:\n  driver: sqlite\n  sqlite:\n    path: " + filepath.Join(dir, "runtime.db") + "\nserver:\n  http_addr: 127.0.0.1:0\ntls:\n  enabled: true\n  cert_file: " + filepath.Join(dir, "missing.crt") + "\n  key_file: " + filepath.Join(dir, "missing.key") + "\n  min_version: \"1.2\"\n"
	if err := os.WriteFile(configFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	root := cli.NewServerRoot()
	root.SetArgs([]string{"run", "--config", configFile})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := root.ExecuteContext(ctx)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "native tls certificate") {
		t.Fatalf("Execute() error = %v, want native TLS certificate load failure", err)
	}
	if strings.Contains(strings.ToLower(out.String()), "server listening") {
		t.Fatalf("server reported readiness before loading TLS certificate: %q", out.String())
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
