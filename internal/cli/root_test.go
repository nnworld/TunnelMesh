package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/cli"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestServerRootExposesConfigurationCommands(t *testing.T) {
	root := cli.NewServerRoot()
	for _, name := range []string{"run", "check-config", "doctor", "init-db", "init-node-id", "print-config", "admin"} {
		if root.CommandPath() == "" {
			t.Fatal("root command has no path")
		}
		if findCommand(root, name) == nil {
			t.Fatalf("server root missing %q command", name)
		}
	}
}

func TestAllRootsExposeDoctorCommand(t *testing.T) {
	roots := map[string]func() *cobra.Command{
		"server": cli.NewServerRoot,
		"agent":  cli.NewAgentRoot,
		"client": cli.NewClientRoot,
	}

	for name, factory := range roots {
		t.Run(name, func(t *testing.T) {
			if findCommand(factory(), "doctor") == nil {
				t.Fatalf("%s root missing doctor command", name)
			}
		})
	}
}

func TestServerDoctorValidatesStorage(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "doctor.db")
	db, err := storage.OpenConfig(context.Background(), config.StorageConfig{
		Driver:   storage.DriverSQLite,
		AutoInit: true,
		SQLite:   config.SQLiteConfig{Path: databasePath},
	})
	if err != nil {
		t.Fatalf("initialize test database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	configFile := filepath.Join(dir, "server.yaml")
	contents := "mode: local\nstorage:\n  driver: sqlite\n  auto_init: true\n  sqlite:\n    path: " + databasePath + "\n"
	if err := os.WriteFile(configFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	root := cli.NewServerRoot()
	root.SetArgs([]string{"doctor", "--config", configFile})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("doctor Execute() error = %v", err)
	}
	for _, want := range []string{"configuration: ok", "storage: ok (sqlite)"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("doctor output %q does not contain %q", output.String(), want)
		}
	}
}

func TestServerDoctorDoesNotInitializeMissingSchema(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "missing.db")
	if err := os.WriteFile(databasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "server.yaml")
	contents := "mode: local\nstorage:\n  driver: sqlite\n  auto_init: true\n  sqlite:\n    path: " + databasePath + "\n"
	if err := os.WriteFile(configFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	root := cli.NewServerRoot()
	root.SetArgs([]string{"doctor", "--config", configFile})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("doctor unexpectedly succeeded with a missing schema")
	}
	info, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("doctor modified missing database; size = %d", info.Size())
	}
}

func TestAgentDoctorChecksUnauthenticatedHealthEndpoint(t *testing.T) {
	var requestPath string
	var authorizationHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		authorizationHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "agent.yaml")
	serverURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/agent"
	contents := "mode: local\nagent:\n  server_url: " + serverURL + "\n"
	if err := os.WriteFile(configFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	root := cli.NewAgentRoot()
	root.SetArgs([]string{"doctor", "--config", configFile})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("doctor Execute() error = %v", err)
	}
	if requestPath != "/health/ready" {
		t.Fatalf("health path = %q, want /health/ready", requestPath)
	}
	if authorizationHeader != "" {
		t.Fatalf("doctor sent Authorization header %q", authorizationHeader)
	}
	for _, want := range []string{"configuration: ok", "server health: ok"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("doctor output %q does not contain %q", output.String(), want)
		}
	}
}

func TestRootsExposeBuildVersion(t *testing.T) {
	roots := map[string]func() *cobra.Command{
		"tunnelmesh-server": cli.NewServerRoot,
		"tunnelmesh-agent":  cli.NewAgentRoot,
		"tunnelmesh-client": cli.NewClientRoot,
	}
	for name, factory := range roots {
		t.Run(name, func(t *testing.T) {
			root := factory()
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs([]string{"--version"})
			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{name, "commit=", "built="} {
				if !strings.Contains(output.String(), expected) {
					t.Fatalf("--version output %q does not contain %q", output.String(), expected)
				}
			}
		})
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

func TestServerRootExposesWebSSHConfigurationFlags(t *testing.T) {
	root := cli.NewServerRoot()
	args := []string{
		"print-config",
		"--server.webssh.enabled=false",
		"--server.webssh.ticket_ttl=45s",
		"--server.webssh.session_ttl=12h",
		"--server.webssh.max_active_sessions_per_user=7",
		"--server.webssh.open_timeout=15s",
		"--server.webssh.idle_timeout=2m",
		"--server.webssh.max_message_bytes=131072",
	}
	root.SetArgs(args)
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var printed struct {
		Server struct {
			WebSSH struct {
				Enabled               bool          `json:"enabled"`
				TicketTTL             time.Duration `json:"ticket_ttl"`
				SessionTTL            time.Duration `json:"session_ttl"`
				MaxActiveSessionsUser int           `json:"max_active_sessions_per_user"`
				OpenTimeout           time.Duration `json:"open_timeout"`
				IdleTimeout           time.Duration `json:"idle_timeout"`
				MaxMessageBytes       int           `json:"max_message_bytes"`
			} `json:"webssh"`
		} `json:"server"`
	}
	if err := json.Unmarshal(output.Bytes(), &printed); err != nil {
		t.Fatalf("unmarshal printed config: %v output=%s", err, output.String())
	}
	got := printed.Server.WebSSH
	if got.Enabled || got.TicketTTL != 45*time.Second || got.SessionTTL != 12*time.Hour || got.MaxActiveSessionsUser != 7 ||
		got.OpenTimeout != 15*time.Second || got.IdleTimeout != 2*time.Minute || got.MaxMessageBytes != 131072 {
		t.Fatalf("webssh config = %+v", got)
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

// TestServerRunValidatesNativeTLSBeforeOpeningStorage pins the startup order.
// Certificate material is a cheap local file read, while opening storage can run
// a full schema migration and per-table verification. Reporting the TLS problem
// first keeps `run` failing fast and keeps the diagnostic independent of
// database latency, which matters because schema growth keeps making the storage
// step slower.
func TestServerRunValidatesNativeTLSBeforeOpeningStorage(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "server.yaml")
	// The SQLite path points into a directory that does not exist, so opening
	// storage would also fail. The TLS error must win because it is checked first.
	contents := "mode: local\nstorage:\n  driver: sqlite\n  sqlite:\n    path: " + filepath.Join(dir, "missing-dir", "runtime.db") + "\nserver:\n  http_addr: 127.0.0.1:0\ntls:\n  enabled: true\n  cert_file: " + filepath.Join(dir, "missing.crt") + "\n  key_file: " + filepath.Join(dir, "missing.key") + "\n  min_version: \"1.2\"\n"
	if err := os.WriteFile(configFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	root := cli.NewServerRoot()
	root.SetArgs([]string{"run", "--config", configFile})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "native tls certificate") {
		t.Fatalf("Execute() error = %v, want native TLS certificate load failure before storage is opened", err)
	}
}
