# Task 5 review package

Base HEAD: `163fe12121d2f839ea4bf4907f55a8e7dd835057`

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/agent/session_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/agent/session_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/agent/websocket.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/agent/websocket.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/cli/root.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/cli/root.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/cli/root_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/cli/root_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/config/config.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/config/config.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/config/config_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/config/config_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/middleware.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/middleware.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/runtime.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/runtime_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/session_manager.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/session_manager.go differ
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server: tls_listener.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server: tls_listener_test.go
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/web.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/web.go differ
```

## Full task-only diff

```diff
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/agent/session_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/agent/session_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/agent/session_test.go	2026-09-06 16:14:44
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/agent/session_test.go	2026-09-06 16:50:34
@@ -5,7 +5,10 @@
 	"encoding/json"
 	"errors"
 	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+	"golang.org/x/net/websocket"
 	"io"
+	"net/http/httptest"
+	"strings"
 	"testing"
 	"time"
 )
@@ -212,5 +215,22 @@
 	d.mu.Unlock()
 	if !ok {
 		t.Fatal("stale reader deleted replacement stream")
+	}
+}
+
+func TestDialWebSocketRejectsNonWebSocketURL(t *testing.T) {
+	_, err := DialWebSocket(context.Background(), "https://server.example/ws/agent", "agent-token")
+	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "invalid websocket url") {
+		t.Fatalf("DialWebSocket() error = %v, want invalid websocket URL", err)
+	}
+}
+
+func TestDialWebSocketDoesNotSkipServerCertificateVerification(t *testing.T) {
+	server := httptest.NewTLSServer(websocket.Handler(func(conn *websocket.Conn) { _ = conn.Close() }))
+	defer server.Close()
+	serverURL := "wss" + strings.TrimPrefix(server.URL, "https") + "/ws/agent"
+	if transport, err := DialWebSocket(context.Background(), serverURL, "agent-token"); err == nil {
+		_ = transport.Close()
+		t.Fatal("DialWebSocket accepted an untrusted server certificate")
 	}
 }
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/agent/websocket.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/agent/websocket.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/agent/websocket.go	2026-09-06 16:14:44
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/agent/websocket.go	2026-09-06 16:50:34
@@ -108,6 +108,9 @@
 		return nil, ErrAgentTokenRequired
 	}
 	u, err := parseWebSocketURL(serverURL)
+	if err != nil {
+		return nil, err
+	}
 	if err := ctx.Err(); err != nil {
 		return nil, err
 	}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/cli/root.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/cli/root.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/cli/root.go	2026-09-06 16:50:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/cli/root.go	2026-09-06 16:50:34
@@ -36,13 +36,20 @@
 func NewClientCommand() *cobra.Command { return NewClientRoot() }
 
 type rootOptions struct {
-	configFile string
-	mode       string
-	storage    string
-	autoInit   bool
-	registry   string
-	nodeID     string
-	bridge     bool
+	configFile                  string
+	mode                        string
+	storage                     string
+	autoInit                    bool
+	registry                    string
+	nodeID                      string
+	bridge                      bool
+	allowedHosts                []string
+	allowedOrigins              []string
+	allowLegacyConnectionTokens bool
+	tlsEnabled                  bool
+	tlsCertFile                 string
+	tlsKeyFile                  string
+	tlsMinVersion               string
 }
 
 func newRoot(use string, factory func(*rootOptions) []*cobra.Command) *cobra.Command {
@@ -67,6 +74,13 @@
 	flags.BoolVar(&opts.bridge, "server.tcp_bridge.enabled", false, "enable TCP-over-WebSocket bridge")
 	flags.BoolVar(&opts.bridge, "tcp-bridge", false, "enable TCP-over-WebSocket bridge")
 	flags.Bool("server.tcp_bridge_enabled", false, "enable TCP-over-WebSocket bridge (flat spelling)")
+	flags.StringSliceVar(&opts.allowedHosts, "security.allowed_hosts", nil, "exact Host values accepted by the Agent WebSocket endpoint")
+	flags.StringSliceVar(&opts.allowedOrigins, "security.allowed_origins", nil, "exact http/https Origin values accepted by Agent WebSocket")
+	flags.BoolVar(&opts.allowLegacyConnectionTokens, "security.allow_legacy_connection_tokens", false, "temporarily accept deprecated management tokens for Agent connections")
+	flags.BoolVar(&opts.tlsEnabled, "tls.enabled", false, "enable native TLS termination")
+	flags.StringVar(&opts.tlsCertFile, "tls.cert_file", "", "native TLS certificate file")
+	flags.StringVar(&opts.tlsKeyFile, "tls.key_file", "", "native TLS private key file")
+	flags.StringVar(&opts.tlsMinVersion, "tls.min_version", "", "native TLS minimum version (1.2 or 1.3)")
 	root.AddCommand(factory(opts)...)
 	return root
 }
@@ -79,11 +93,12 @@
 				return err
 			}
 			defer db.Close()
-			runtime, err := server.NewServerRuntime(db, server.AgentSessionConfig{})
+			runtime, err := server.NewServerRuntime(db, server.AgentSessionConfig{}, server.RuntimeConfig{Security: cfg.Security, TLS: cfg.TLS})
 			if err != nil {
 				return err
 			}
-			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "server listening on %s in %s mode\n", cfg.Server.HTTPAddr, cfg.Mode)
+			defer runtime.Close()
+			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "server starting on %s in %s mode\n", cfg.Server.HTTPAddr, cfg.Mode)
 			return runtime.Serve(cmd.Context(), cfg.Server.HTTPAddr)
 		}),
 		configCommand(opts, "check-config", "validate configuration and exit", func(cmd *cobra.Command, _ config.Config) error {
@@ -317,6 +332,27 @@
 		if err == nil {
 			values["server.tcp_bridge_enabled"] = value
 		}
+	}
+	if flags.Changed("security.allowed_hosts") {
+		values["security.allowed_hosts"] = append([]string(nil), opts.allowedHosts...)
+	}
+	if flags.Changed("security.allowed_origins") {
+		values["security.allowed_origins"] = append([]string(nil), opts.allowedOrigins...)
+	}
+	if flags.Changed("security.allow_legacy_connection_tokens") {
+		values["security.allow_legacy_connection_tokens"] = opts.allowLegacyConnectionTokens
+	}
+	if flags.Changed("tls.enabled") {
+		values["tls.enabled"] = opts.tlsEnabled
+	}
+	if flags.Changed("tls.cert_file") {
+		values["tls.cert_file"] = opts.tlsCertFile
+	}
+	if flags.Changed("tls.key_file") {
+		values["tls.key_file"] = opts.tlsKeyFile
+	}
+	if flags.Changed("tls.min_version") {
+		values["tls.min_version"] = opts.tlsMinVersion
 	}
 	return values
 }
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/cli/root_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/cli/root_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/cli/root_test.go	2026-09-06 16:50:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/cli/root_test.go	2026-09-06 16:50:34
@@ -3,8 +3,11 @@
 import (
 	"bytes"
 	"context"
+	"os"
+	"path/filepath"
 	"strings"
 	"testing"
+	"time"
 
 	"github.com/spf13/cobra"
 	"github.com/tunnelmesh/tunnelmesh/internal/cli"
@@ -77,6 +80,58 @@
 	}
 	if !strings.Contains(out.String(), `"enabled": false`) {
 		t.Fatalf("output = %q, want disabled nested bridge value", out.String())
+	}
+}
+
+func TestPrintConfigAcceptsSecurityAndNativeTLSFlags(t *testing.T) {
+	root := cli.NewServerRoot()
+	root.SetArgs([]string{
+		"print-config",
+		"--security.allowed_hosts", "secure.example",
+		"--security.allowed_origins", "https://secure.example",
+		"--security.allow_legacy_connection_tokens",
+		"--tls.enabled",
+		"--tls.cert_file", "server-cert.pem",
+		"--tls.key_file", "/secrets/server-key.pem",
+		"--tls.min_version", "1.3",
+	})
+	var out bytes.Buffer
+	root.SetOut(&out)
+	root.SetErr(&out)
+	if err := root.ExecuteContext(context.Background()); err != nil {
+		t.Fatalf("Execute() error = %v", err)
+	}
+	printed := out.String()
+	for _, want := range []string{"secure.example", "https://secure.example", "server-cert.pem", `"min_version": "1.3"`, `"allow_legacy_connection_tokens": true`} {
+		if !strings.Contains(printed, want) {
+			t.Fatalf("output = %q, want %q", printed, want)
+		}
+	}
+	if strings.Contains(printed, "/secrets/server-key.pem") {
+		t.Fatalf("output leaked TLS private key path: %q", printed)
+	}
+}
+
+func TestServerRunFailsFastWhenNativeTLSCertificateCannotLoad(t *testing.T) {
+	dir := t.TempDir()
+	configFile := filepath.Join(dir, "server.yaml")
+	contents := "mode: local\nstorage:\n  driver: sqlite\n  sqlite:\n    path: " + filepath.Join(dir, "runtime.db") + "\nserver:\n  http_addr: 127.0.0.1:0\ntls:\n  enabled: true\n  cert_file: " + filepath.Join(dir, "missing.crt") + "\n  key_file: " + filepath.Join(dir, "missing.key") + "\n  min_version: \"1.2\"\n"
+	if err := os.WriteFile(configFile, []byte(contents), 0o600); err != nil {
+		t.Fatal(err)
+	}
+	root := cli.NewServerRoot()
+	root.SetArgs([]string{"run", "--config", configFile})
+	var out bytes.Buffer
+	root.SetOut(&out)
+	root.SetErr(&out)
+	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
+	defer cancel()
+	err := root.ExecuteContext(ctx)
+	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "native tls certificate") {
+		t.Fatalf("Execute() error = %v, want native TLS certificate load failure", err)
+	}
+	if strings.Contains(strings.ToLower(out.String()), "server listening") {
+		t.Fatalf("server reported readiness before loading TLS certificate: %q", out.String())
 	}
 }
 
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/config/config.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/config/config.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/config/config.go	2026-09-06 16:14:44
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/config/config.go	2026-09-06 16:50:34
@@ -8,12 +8,16 @@
 	"encoding/json"
 	"errors"
 	"fmt"
+	"net"
+	"net/url"
 	"os"
 	"path/filepath"
 	"regexp"
+	"strconv"
 	"strings"
 
 	"github.com/spf13/viper"
+	"golang.org/x/net/http/httpguts"
 )
 
 const (
@@ -50,6 +54,8 @@
 	Registry RegistryConfig `mapstructure:"registry" json:"registry" yaml:"registry"`
 	Node     NodeConfig     `mapstructure:"node" json:"node" yaml:"node"`
 	Server   ServerConfig   `mapstructure:"server" json:"server" yaml:"server"`
+	Security SecurityConfig `mapstructure:"security" json:"security" yaml:"security"`
+	TLS      TLSConfig      `mapstructure:"tls" json:"tls" yaml:"tls"`
 	Agent    AgentConfig    `mapstructure:"agent" json:"agent" yaml:"agent"`
 	Client   ClientConfig   `mapstructure:"client" json:"client" yaml:"client"`
 }
@@ -102,6 +108,20 @@
 	MaxBytes int64  `mapstructure:"max_bytes" json:"max_bytes" yaml:"max_bytes"`
 }
 
+type SecurityConfig struct {
+	AllowedHosts   []string `mapstructure:"allowed_hosts" json:"allowed_hosts" yaml:"allowed_hosts"`
+	AllowedOrigins []string `mapstructure:"allowed_origins" json:"allowed_origins" yaml:"allowed_origins"`
+	// Deprecated: legacy management connection tokens are removed in v0.3.0.
+	AllowLegacyConnectionTokens bool `mapstructure:"allow_legacy_connection_tokens" json:"allow_legacy_connection_tokens" yaml:"allow_legacy_connection_tokens"`
+}
+
+type TLSConfig struct {
+	Enabled    bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
+	CertFile   string `mapstructure:"cert_file" json:"cert_file" yaml:"cert_file"`
+	KeyFile    string `mapstructure:"key_file" json:"key_file" yaml:"key_file"`
+	MinVersion string `mapstructure:"min_version" json:"min_version" yaml:"min_version"`
+}
+
 type AgentConfig struct {
 	ServerURL string           `mapstructure:"server_url" json:"server_url" yaml:"server_url"`
 	ID        string           `mapstructure:"id" json:"id" yaml:"id"`
@@ -260,20 +280,25 @@
 
 func setDefaults(v *viper.Viper) {
 	defaults := map[string]any{
-		"mode":                        ModeLocal,
-		"storage.driver":              StorageSQLite,
-		"storage.sqlite.path":         "tunnelmesh.db",
-		"storage.auto_init":           true,
-		"storage.mysql.tls":           false,
-		"registry.type":               RegistryDatabase,
-		"registry.endpoints":          []string{},
-		"server.http_addr":            ":80",
-		"server.https_addr":           ":443",
-		"server.agent_ws_addr":        ":443",
-		"server.client_ws_addr":       ":443",
-		"server.tcp_bridge.enabled":   true,
-		"server.tcp_bridge.path":      "/ws/tcp",
-		"server.tcp_bridge.max_bytes": int64(64 << 10),
+		"mode":                                    ModeLocal,
+		"storage.driver":                          StorageSQLite,
+		"storage.sqlite.path":                     "tunnelmesh.db",
+		"storage.auto_init":                       true,
+		"storage.mysql.tls":                       false,
+		"registry.type":                           RegistryDatabase,
+		"registry.endpoints":                      []string{},
+		"server.http_addr":                        ":80",
+		"server.https_addr":                       ":443",
+		"server.agent_ws_addr":                    ":443",
+		"server.client_ws_addr":                   ":443",
+		"server.tcp_bridge.enabled":               true,
+		"server.tcp_bridge.path":                  "/ws/tcp",
+		"server.tcp_bridge.max_bytes":             int64(64 << 10),
+		"security.allowed_hosts":                  []string{},
+		"security.allowed_origins":                []string{},
+		"security.allow_legacy_connection_tokens": false,
+		"tls.enabled":                             false,
+		"tls.min_version":                         "1.2",
 	}
 	for key, value := range defaults {
 		v.SetDefault(key, value)
@@ -289,6 +314,8 @@
 		"storage.mysql.dsn", "storage.mysql.tls", "storage.mysql.ca", "storage.mysql.cert", "storage.mysql.key",
 		"registry.type", "registry.endpoints", "node.id", "server.http_addr", "server.https_addr",
 		"server.agent_ws_addr", "server.client_ws_addr", "server.tcp_bridge.enabled", "server.tcp_bridge_enabled",
+		"security.allowed_hosts", "security.allowed_origins", "security.allow_legacy_connection_tokens",
+		"tls.enabled", "tls.cert_file", "tls.key_file", "tls.min_version",
 		"agent.server_url", "agent.id", "agent.token", "client.server_url",
 	}
 	for _, key := range keys {
@@ -299,6 +326,14 @@
 func Validate(cfg Config) error {
 	var problems []string
 	problems = append(problems, validateMetadataSources(cfg.Agent.Metadata)...)
+	problems = append(problems, validateSecurity(cfg.Security)...)
+	problems = append(problems, validateTLS(cfg.TLS)...)
+	if cfg.Agent.ServerURL != "" && !validWebSocketURL(cfg.Agent.ServerURL) {
+		problems = append(problems, "agent server URL must be an absolute ws:// or wss:// URL")
+	}
+	if cfg.Client.ServerURL != "" && !validWebSocketURL(cfg.Client.ServerURL) {
+		problems = append(problems, "client server URL must be an absolute ws:// or wss:// URL")
+	}
 	switch cfg.Mode {
 	case ModeLocal:
 		if cfg.Storage.Driver != StorageSQLite {
@@ -340,6 +375,119 @@
 	return nil
 }
 
+func validateSecurity(cfg SecurityConfig) []string {
+	var problems []string
+	for _, host := range cfg.AllowedHosts {
+		if _, ok := NormalizeAllowedHost(host); !ok {
+			problems = append(problems, fmt.Sprintf("allowed host %q is invalid", host))
+		}
+	}
+	for _, origin := range cfg.AllowedOrigins {
+		if !validHTTPOrigin(origin) {
+			problems = append(problems, fmt.Sprintf("allowed origin %q must be an absolute http/https origin", origin))
+		}
+	}
+	return problems
+}
+
+// NormalizeAllowedHost validates an exact HTTP Host allowlist entry and
+// normalizes only its case. Ports remain part of the exact match.
+func NormalizeAllowedHost(raw string) (string, bool) {
+	raw = strings.TrimSpace(raw)
+	if raw == "" || strings.Contains(raw, "@") || !httpguts.ValidHostHeader(raw) {
+		return "", false
+	}
+	host := raw
+	if strings.HasPrefix(raw, "[") {
+		end := strings.IndexByte(raw, ']')
+		if end < 0 || net.ParseIP(raw[1:end]) == nil {
+			return "", false
+		}
+		remainder := raw[end+1:]
+		if remainder != "" && (remainder[0] != ':' || !validHostPort(remainder[1:])) {
+			return "", false
+		}
+		return strings.ToLower(raw), true
+	}
+	if strings.Count(raw, ":") > 1 {
+		return "", false
+	}
+	if strings.Contains(raw, ":") {
+		var port string
+		var err error
+		host, port, err = net.SplitHostPort(raw)
+		if err != nil || !validHostPort(port) {
+			return "", false
+		}
+	}
+	if net.ParseIP(host) == nil && !validDNSHost(host) {
+		return "", false
+	}
+	return strings.ToLower(raw), true
+}
+
+func validHostPort(raw string) bool {
+	port, err := strconv.Atoi(raw)
+	return err == nil && port >= 1 && port <= 65535
+}
+
+func validDNSHost(raw string) bool {
+	if strings.HasSuffix(raw, ".") {
+		raw = strings.TrimSuffix(raw, ".")
+	}
+	if raw == "" || len(raw) > 253 {
+		return false
+	}
+	for _, label := range strings.Split(raw, ".") {
+		if len(label) == 0 || len(label) > 63 || !asciiAlphaNumeric(label[0]) || !asciiAlphaNumeric(label[len(label)-1]) {
+			return false
+		}
+		for i := 1; i < len(label)-1; i++ {
+			if !asciiAlphaNumeric(label[i]) && label[i] != '-' {
+				return false
+			}
+		}
+	}
+	return true
+}
+
+func asciiAlphaNumeric(value byte) bool {
+	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
+}
+
+func validHTTPOrigin(raw string) bool {
+	u, err := url.Parse(strings.TrimSpace(raw))
+	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
+		return false
+	}
+	return u.Path == "" && u.RawPath == "" && u.RawQuery == "" && u.Fragment == ""
+}
+
+func validWebSocketURL(raw string) bool {
+	u, err := url.Parse(strings.TrimSpace(raw))
+	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil || u.Fragment != "" {
+		return false
+	}
+	return u.IsAbs()
+}
+
+func validateTLS(cfg TLSConfig) []string {
+	if !cfg.Enabled {
+		return nil
+	}
+	var problems []string
+	if strings.TrimSpace(cfg.CertFile) == "" {
+		problems = append(problems, "native TLS requires a certificate file")
+	}
+	if strings.TrimSpace(cfg.KeyFile) == "" {
+		problems = append(problems, "native TLS requires a private key file")
+	}
+	if cfg.MinVersion != "1.2" && cfg.MinVersion != "1.3" {
+		problems = append(problems, "native TLS minimum version must be 1.2 or 1.3")
+	}
+	return problems
+}
+
 var metadataNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
 var metadataSensitivePattern = regexp.MustCompile(`(?i)(password|token|secret|private[_-]?key|dsn)`)
 
@@ -391,6 +539,7 @@
 	copy := c
 	copy.Storage.MySQL.DSN = redact(copy.Storage.MySQL.DSN)
 	copy.Storage.MySQL.Key = redact(copy.Storage.MySQL.Key)
+	copy.TLS.KeyFile = redact(copy.TLS.KeyFile)
 	return json.MarshalIndent(copy, "", "  ")
 }
 
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/config/config_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/config/config_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/config/config_test.go	2026-09-06 16:14:44
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/config/config_test.go	2026-09-06 16:50:34
@@ -158,3 +158,101 @@
 		t.Fatalf("redacted config leaked agent token: %s", data)
 	}
 }
+
+func TestLoadSecurityAndNativeTLSDefaults(t *testing.T) {
+	t.Setenv("TUNNELMESH_SECURITY_ALLOW_LEGACY_CONNECTION_TOKENS", "")
+	t.Setenv("TUNNELMESH_TLS_ENABLED", "")
+
+	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if cfg.Security.AllowLegacyConnectionTokens {
+		t.Fatal("legacy connection tokens are enabled by default")
+	}
+	if cfg.TLS.Enabled {
+		t.Fatal("native TLS is enabled by default")
+	}
+	if cfg.TLS.MinVersion != "1.2" {
+		t.Fatalf("TLS.MinVersion = %q, want 1.2", cfg.TLS.MinVersion)
+	}
+}
+
+func TestLoadSecurityAndNativeTLSPrecedence(t *testing.T) {
+	dir := t.TempDir()
+	path := filepath.Join(dir, "security.yaml")
+	contents := "security:\n  allowed_hosts: [file.example]\n  allowed_origins: [https://file.example]\n  allow_legacy_connection_tokens: false\ntls:\n  enabled: false\n  cert_file: file-cert.pem\n  key_file: file-key.pem\n  min_version: \"1.2\"\n"
+	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
+		t.Fatal(err)
+	}
+	t.Setenv("TUNNELMESH_SECURITY_ALLOWED_HOSTS", "env.example,second.example")
+	t.Setenv("TUNNELMESH_SECURITY_ALLOW_LEGACY_CONNECTION_TOKENS", "true")
+
+	cfg, err := config.Load(context.Background(), config.ConfigOptions{
+		ConfigFile: path,
+		CLI: map[string]any{
+			"security.allowed_origins": []string{"https://cli.example"},
+			"tls.enabled":              true,
+			"tls.cert_file":            "cli-cert.pem",
+			"tls.key_file":             "cli-key.pem",
+			"tls.min_version":          "1.3",
+		},
+	})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if len(cfg.Security.AllowedHosts) != 2 || cfg.Security.AllowedHosts[0] != "env.example" || cfg.Security.AllowedHosts[1] != "second.example" {
+		t.Fatalf("AllowedHosts = %v, want environment value", cfg.Security.AllowedHosts)
+	}
+	if len(cfg.Security.AllowedOrigins) != 1 || cfg.Security.AllowedOrigins[0] != "https://cli.example" {
+		t.Fatalf("AllowedOrigins = %v, want CLI value", cfg.Security.AllowedOrigins)
+	}
+	if !cfg.Security.AllowLegacyConnectionTokens {
+		t.Fatal("environment legacy migration flag did not win over file")
+	}
+	if !cfg.TLS.Enabled || cfg.TLS.CertFile != "cli-cert.pem" || cfg.TLS.KeyFile != "cli-key.pem" || cfg.TLS.MinVersion != "1.3" {
+		t.Fatalf("TLS = %+v, want CLI values", cfg.TLS)
+	}
+}
+
+func TestValidateRejectsUnsafeSecurityAndNativeTLSConfiguration(t *testing.T) {
+	base := config.Config{Mode: config.ModeLocal, Storage: config.StorageConfig{Driver: config.StorageSQLite}, Registry: config.RegistryConfig{Type: config.RegistryDatabase}}
+	cases := []struct {
+		name string
+		edit func(*config.Config)
+		want string
+	}{
+		{name: "host with userinfo delimiter", edit: func(cfg *config.Config) { cfg.Security.AllowedHosts = []string{"allowed.example@evil.example"} }, want: "allowed host"},
+		{name: "host with invalid port", edit: func(cfg *config.Config) { cfg.Security.AllowedHosts = []string{"allowed.example:not-a-port"} }, want: "allowed host"},
+		{name: "origin with path", edit: func(cfg *config.Config) { cfg.Security.AllowedOrigins = []string{"https://allowed.example/path"} }, want: "allowed origin"},
+		{name: "origin with websocket scheme", edit: func(cfg *config.Config) { cfg.Security.AllowedOrigins = []string{"wss://allowed.example"} }, want: "allowed origin"},
+		{name: "TLS missing key", edit: func(cfg *config.Config) {
+			cfg.TLS = config.TLSConfig{Enabled: true, CertFile: "cert.pem", MinVersion: "1.2"}
+		}, want: "private key"},
+		{name: "TLS invalid minimum", edit: func(cfg *config.Config) {
+			cfg.TLS = config.TLSConfig{Enabled: true, CertFile: "cert.pem", KeyFile: "key.pem", MinVersion: "1.1"}
+		}, want: "minimum version"},
+		{name: "relative agent URL", edit: func(cfg *config.Config) { cfg.Agent.ServerURL = "/ws/agent" }, want: "agent server url"},
+	}
+	for _, tc := range cases {
+		t.Run(tc.name, func(t *testing.T) {
+			cfg := base
+			tc.edit(&cfg)
+			err := config.Validate(cfg)
+			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
+				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
+			}
+		})
+	}
+}
+
+func TestRedactedJSONHidesNativeTLSPrivateKeyPath(t *testing.T) {
+	cfg := config.Config{TLS: config.TLSConfig{KeyFile: "/secrets/native-tls.key"}}
+	data, err := cfg.RedactedJSON()
+	if err != nil {
+		t.Fatal(err)
+	}
+	if strings.Contains(string(data), "/secrets/native-tls.key") {
+		t.Fatalf("redacted config leaked TLS private key path: %s", data)
+	}
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/middleware.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/middleware.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/middleware.go	2026-09-06 16:14:44
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/middleware.go	2026-09-06 16:50:34
@@ -2,10 +2,15 @@
 
 import (
 	"context"
+	"fmt"
 	"net/http"
+	"net/url"
 	"strings"
 
+	"golang.org/x/net/websocket"
+
 	"github.com/tunnelmesh/tunnelmesh/internal/auth"
+	"github.com/tunnelmesh/tunnelmesh/internal/config"
 )
 
 type principalContextKey struct{}
@@ -27,4 +32,70 @@
 		return ""
 	}
 	return strings.TrimSpace(v[7:])
+}
+
+func agentWebSocketHandshake(security config.SecurityConfig) func(*websocket.Config, *http.Request) error {
+	return func(wsConfig *websocket.Config, r *http.Request) error {
+		if bearerToken(r) == "" {
+			return auth.ErrUnauthenticated
+		}
+		if len(security.AllowedHosts) > 0 && !exactHostAllowed(r.Host, security.AllowedHosts) {
+			return fmt.Errorf("websocket host is not allowed")
+		}
+		if len(security.AllowedOrigins) == 0 {
+			origin, err := websocket.Origin(wsConfig, r)
+			if err != nil || origin == nil {
+				return fmt.Errorf("websocket origin is invalid")
+			}
+			wsConfig.Origin = origin
+			return nil
+		}
+		values := r.Header.Values("Origin")
+		if len(values) != 1 {
+			return fmt.Errorf("websocket origin is not allowed")
+		}
+		origin, normalized, ok := normalizeOrigin(values[0])
+		if !ok || !containsNormalizedOrigin(normalized, security.AllowedOrigins) {
+			return fmt.Errorf("websocket origin is not allowed")
+		}
+		wsConfig.Origin = origin
+		return nil
+	}
+}
+
+func exactHostAllowed(raw string, allowed []string) bool {
+	want, ok := normalizeHost(raw)
+	if !ok {
+		return false
+	}
+	for _, candidate := range allowed {
+		if normalized, valid := normalizeHost(candidate); valid && normalized == want {
+			return true
+		}
+	}
+	return false
+}
+
+func normalizeHost(raw string) (string, bool) {
+	return config.NormalizeAllowedHost(raw)
+}
+
+func containsNormalizedOrigin(want string, allowed []string) bool {
+	for _, candidate := range allowed {
+		_, normalized, ok := normalizeOrigin(candidate)
+		if ok && normalized == want {
+			return true
+		}
+	}
+	return false
+}
+
+func normalizeOrigin(raw string) (*url.URL, string, bool) {
+	u, err := url.Parse(strings.TrimSpace(raw))
+	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
+		return nil, "", false
+	}
+	u.Scheme = strings.ToLower(u.Scheme)
+	u.Host = strings.ToLower(u.Host)
+	return u, u.String(), true
 }
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/runtime.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/runtime.go	2026-09-06 16:14:44
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime.go	2026-09-06 16:50:34
@@ -3,6 +3,7 @@
 import (
 	"context"
 	"errors"
+	"log/slog"
 	"net"
 	"net/http"
 	"strings"
@@ -11,12 +12,18 @@
 	"golang.org/x/net/websocket"
 
 	"github.com/tunnelmesh/tunnelmesh/internal/auth"
+	"github.com/tunnelmesh/tunnelmesh/internal/config"
 	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
 	"github.com/tunnelmesh/tunnelmesh/internal/storage"
 )
 
 var ErrRuntimeDatabaseRequired = errors.New("server runtime: database is required")
 
+type RuntimeConfig struct {
+	Security config.SecurityConfig
+	TLS      config.TLSConfig
+}
+
 // ServerRuntime is the process-scoped server wiring shared by HTTP handlers
 // and authenticated Agent WebSocket handlers. The caller owns DB lifecycle.
 type ServerRuntime struct {
@@ -24,51 +31,51 @@
 	AgentSessions *AgentSessionManager
 	API           *API
 	Auth          *auth.AuthService
-	sessionConfig AgentSessionConfig
+	Credentials   *auth.CredentialService
+	config        RuntimeConfig
 }
 
 // NewServerRuntime creates the server runtime with durable Agent metadata
 // persistence enabled. The CLI server run command and embedders should create
 // one runtime at startup, then pass AgentSessions to ServeAgentSession.
-func NewServerRuntime(db *storage.DB, cfg AgentSessionConfig) (*ServerRuntime, error) {
+func NewServerRuntime(db *storage.DB, cfg AgentSessionConfig, options ...RuntimeConfig) (*ServerRuntime, error) {
 	if db == nil {
 		return nil, ErrRuntimeDatabaseRequired
 	}
 	authService := auth.NewAuthService(db)
-	if cfg.Authenticate == nil {
-		cfg.Authenticate = func(ctx context.Context, registration AgentRegistration) error {
-			return authenticateAgent(ctx, db, authService, registration)
-		}
+	credentials := auth.NewCredentialService(db)
+	var runtimeConfig RuntimeConfig
+	if len(options) > 0 {
+		runtimeConfig = options[0]
+		runtimeConfig.Security.AllowedHosts = append([]string(nil), runtimeConfig.Security.AllowedHosts...)
+		runtimeConfig.Security.AllowedOrigins = append([]string(nil), runtimeConfig.Security.AllowedOrigins...)
 	}
-	return &ServerRuntime{DB: db, AgentSessions: NewAgentSessionManagerWithMetadata(db.Metadata(), cfg), API: NewAPI(db, authService), Auth: authService, sessionConfig: cfg}, nil
+	return &ServerRuntime{DB: db, AgentSessions: NewAgentSessionManagerWithMetadata(db.Metadata(), cfg), API: NewAPI(db, authService), Auth: authService, Credentials: credentials, config: runtimeConfig}, nil
 }
 
-func authenticateAgent(ctx context.Context, db *storage.DB, authService *auth.AuthService, registration AgentRegistration) error {
-	principal, err := authService.ValidateToken(ctx, registration.Token)
-	if err != nil {
-		return err
+// Close releases runtime-owned background workers. The caller continues to
+// own the database lifecycle.
+func (r *ServerRuntime) Close() error {
+	if r == nil || r.Credentials == nil {
+		return nil
 	}
-	configured, err := db.Agents().Get(ctx, registration.AgentID)
-	if err != nil || !configured.Enabled {
-		return auth.ErrForbidden
-	}
-	if principal.Role != "admin" && configured.OwnerUserID != principal.UserID {
-		return auth.ErrForbidden
-	}
-	return nil
+	return r.Credentials.Close()
 }
 
 // Handler exposes management API, embedded web assets, and the Agent WebSocket
-// endpoint under /ws/agent. Agent authentication is intentionally injected via
-// AgentSessionConfig.Authenticate because agent credentials are deployment
-// specific and are not inferred from user API tokens.
+// endpoint under /ws/agent. Scoped Agent credentials are validated before the
+// optional AgentSessionConfig.Authenticate policy runs.
 func (r *ServerRuntime) Handler() http.Handler {
 	if r == nil {
 		return http.NotFoundHandler()
 	}
-	return NewWebHandler(r.API.Handler(), websocket.Handler(r.serveAgentWS))
+	return NewWebHandler(r.API.Handler(), r.agentWebSocketHandler())
 }
 
+func (r *ServerRuntime) agentWebSocketHandler() http.Handler {
+	return websocket.Server{Handler: r.serveAgentWS, Handshake: agentWebSocketHandshake(r.config.Security)}
+}
+
 func (r *ServerRuntime) serveAgentWS(conn *websocket.Conn) {
 	ctx := context.Background()
 	if conn.Request() != nil {
@@ -86,14 +93,46 @@
 		_ = transport.Close()
 		return
 	}
-	registration := AgentRegistration{AgentID: payload.AgentID, NodeID: payload.NodeID, Epoch: payload.Epoch, Token: bearerToken(conn.Request())}
-	if r.sessionConfig.Authenticate == nil {
+	tokenID, err := r.authenticateAgentConnection(ctx, bearerToken(conn.Request()), payload.AgentID)
+	if err != nil {
 		_ = transport.Close()
 		return
 	}
+	registration := AgentRegistration{AgentID: payload.AgentID, NodeID: payload.NodeID, Epoch: payload.Epoch, TokenID: tokenID}
 	_ = ServeAgentSessionWithInitialFrame(ctx, r.AgentSessions, registration, transport, initial, nil)
 }
 
+func (r *ServerRuntime) authenticateAgentConnection(ctx context.Context, raw, agentID string) (string, error) {
+	identity, err := r.Credentials.ValidateAs(ctx, raw, storage.TokenTypeAgent)
+	if err == nil && identity.AgentID == agentID {
+		return identity.TokenID, nil
+	}
+	// Deprecated: management connection tokens are removed in v0.3.0. This
+	// migration path is disabled unless explicitly enabled in configuration.
+	if !r.config.Security.AllowLegacyConnectionTokens {
+		return "", auth.ErrUnauthenticated
+	}
+	principal, err := r.Auth.ValidateToken(ctx, raw)
+	if err != nil {
+		return "", auth.ErrUnauthenticated
+	}
+	agent, err := r.DB.Agents().Get(ctx, agentID)
+	if err != nil || !agent.Enabled || (principal.Role != "admin" && agent.OwnerUserID != principal.UserID) {
+		return "", auth.ErrForbidden
+	}
+	if err := r.DB.Audits().Create(ctx, storage.AuditLog{
+		ActorUserID:  principal.UserID,
+		Action:       "deprecated_connection_token",
+		ResourceType: "agent",
+		ResourceID:   agentID,
+		Details:      `{"removalVersion":"v0.3.0"}`,
+	}); err != nil {
+		return "", err
+	}
+	slog.WarnContext(ctx, "deprecated_connection_token", "agent_id", agentID, "user_id", principal.UserID, "removal_version", "v0.3.0")
+	return "", nil
+}
+
 // Serve binds a TCP listener on addr and blocks until ctx cancellation or a
 // serving error. Shutdown is graceful and closes active HTTP/WebSocket conns.
 func (r *ServerRuntime) Serve(ctx context.Context, addr string) error {
@@ -108,9 +147,14 @@
 	if r == nil || ln == nil {
 		return ErrRuntimeDatabaseRequired
 	}
+	serveListener, err := nativeTLSListener(ln, r.config.TLS)
+	if err != nil {
+		_ = ln.Close()
+		return err
+	}
 	srv := &http.Server{Handler: r.Handler(), ReadHeaderTimeout: 10 * time.Second}
 	errCh := make(chan error, 1)
-	go func() { errCh <- srv.Serve(ln) }()
+	go func() { errCh <- srv.Serve(serveListener) }()
 	select {
 	case err := <-errCh:
 		if errors.Is(err, http.ErrServerClosed) {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/runtime_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/runtime_test.go	2026-09-06 16:14:44
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime_test.go	2026-09-06 16:50:34
@@ -1,14 +1,20 @@
 package server
 
 import (
+	"bufio"
 	"bytes"
 	"context"
 	"encoding/json"
 	"errors"
+	"fmt"
 	"io"
+	"log/slog"
 	"math/rand"
 	"net"
 	"net/http"
+	"net/http/httptest"
+	"net/url"
+	"strings"
 	"testing"
 	"time"
 
@@ -31,6 +37,7 @@
 	if err != nil {
 		t.Fatal(err)
 	}
+	defer runtime.Close()
 	session, err := runtime.AgentSessions.Register(context.Background(), AgentRegistration{AgentID: "agent-runtime", NodeID: "node-runtime", Epoch: 1}, newFakeTransport())
 	if err != nil {
 		t.Fatal(err)
@@ -48,6 +55,31 @@
 	}
 }
 
+func TestAgentWebSocketRouteIsExact(t *testing.T) {
+	ws := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
+		w.WriteHeader(http.StatusNoContent)
+	})
+	handler := NewWebHandler(http.NotFoundHandler(), ws)
+
+	for _, tc := range []struct {
+		path string
+		want int
+	}{
+		{path: "/ws/agent", want: http.StatusNoContent},
+		{path: "/ws/client", want: http.StatusNotFound},
+		{path: "/ws/agent/other", want: http.StatusNotFound},
+		{path: "/ws/unknown", want: http.StatusNotFound},
+	} {
+		t.Run(tc.path, func(t *testing.T) {
+			recorder := httptest.NewRecorder()
+			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
+			if recorder.Code != tc.want {
+				t.Fatalf("GET %s status = %d, want %d", tc.path, recorder.Code, tc.want)
+			}
+		})
+	}
+}
+
 func TestServerRuntimeServesAPIAndAgentWebSocket(t *testing.T) {
 	db, err := storage.OpenSQLite(context.Background(), "file:runtime-http?mode=memory&cache=shared")
 	if err != nil {
@@ -66,10 +98,17 @@
 	if err != nil {
 		t.Fatal(err)
 	}
+	credentials := auth.NewCredentialService(db)
+	agentToken, err := credentials.Create(context.Background(), auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: user.ID, AgentID: "agent-ws"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	_ = credentials.Close()
 	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
 	if err != nil {
 		t.Fatal(err)
 	}
+	defer runtime.Close()
 	listener, err := net.Listen("tcp", "127.0.0.1:0")
 	if err != nil {
 		t.Fatal(err)
@@ -113,7 +152,7 @@
 	}
 	_ = invalidWS.Close()
 
-	config.Header.Set("Authorization", "Bearer "+login.Token)
+	config.Header.Set("Authorization", "Bearer "+agentToken.Secret)
 	ws, err := websocket.DialConfig(config)
 	if err != nil {
 		t.Fatal(err)
@@ -176,21 +215,25 @@
 	if err != nil {
 		t.Fatal(err)
 	}
-	login, err := authService.Login(context.Background(), user.Username, "agent-client-pass")
-	if err != nil {
-		t.Fatal(err)
-	}
 	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "agent-client", Name: "agent-client", OwnerUserID: user.ID, Enabled: true}); err != nil {
 		t.Fatal(err)
 	}
-	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
+	credentials := auth.NewCredentialService(db)
+	agentToken, err := credentials.Create(context.Background(), auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: user.ID, AgentID: "agent-client"})
 	if err != nil {
 		t.Fatal(err)
 	}
+	_ = credentials.Close()
 	listener, err := net.Listen("tcp", "127.0.0.1:0")
 	if err != nil {
 		t.Fatal(err)
 	}
+	address := listener.Addr().String()
+	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{Security: config.SecurityConfig{AllowedHosts: []string{address}, AllowedOrigins: []string{"http://" + address}}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer runtime.Close()
 	ctx, cancel := context.WithCancel(context.Background())
 	defer cancel()
 	serveErr := make(chan error, 1)
@@ -200,7 +243,7 @@
 	agentCtx, agentCancel := context.WithCancel(context.Background())
 	agentErr := make(chan error, 1)
 	go func() {
-		agentErr <- agent.RunWebSocket(agentCtx, "ws://"+listener.Addr().String()+"/ws/agent", login.Token, "agent-client", "node-client", 1, agent.NewMetadataCollector([]config.MetadataSource{{Name: "region", Source: "env", Key: "TUNNELMESH_AGENT_REGION"}}), nil)
+		agentErr <- agent.RunWebSocket(agentCtx, "ws://"+address+"/ws/agent", agentToken.Secret, "agent-client", "node-client", 1, agent.NewMetadataCollector([]config.MetadataSource{{Name: "region", Source: "env", Key: "TUNNELMESH_AGENT_REGION"}}), nil)
 	}()
 	deadline := time.Now().Add(2 * time.Second)
 	for {
@@ -269,6 +312,7 @@
 	if err != nil {
 		t.Fatal(err)
 	}
+	defer runtime.Close()
 	listener, err := net.Listen("tcp", "127.0.0.1:0")
 	if err != nil {
 		t.Fatal(err)
@@ -317,6 +361,344 @@
 	}
 }
 
+func TestAgentTokenAuthenticationBoundaries(t *testing.T) {
+	ctx := context.Background()
+	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-token-boundaries?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	authService := auth.NewAuthService(db)
+	owner, err := authService.CreateUser(ctx, "token-boundary-owner", "owner-pass", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	management, err := authService.Login(ctx, owner.Username, "owner-pass")
+	if err != nil {
+		t.Fatal(err)
+	}
+	for _, agentID := range []string{"agent-token-valid", "agent-token-other", "agent-token-disabled", "agent-token-expired", "agent-token-revoked"} {
+		if err := db.Agents().Create(ctx, storage.Agent{ID: agentID, Name: agentID, OwnerUserID: owner.ID, Enabled: true}); err != nil {
+			t.Fatal(err)
+		}
+	}
+	credentials := auth.NewCredentialService(db)
+	defer credentials.Close()
+	createAgentToken := func(agentID string, expiresAt *time.Time) auth.CreatedToken {
+		t.Helper()
+		created, createErr := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: agentID, ExpiresAt: expiresAt})
+		if createErr != nil {
+			t.Fatal(createErr)
+		}
+		return created
+	}
+	valid := createAgentToken("agent-token-valid", nil)
+	other := createAgentToken("agent-token-other", nil)
+	disabled := createAgentToken("agent-token-disabled", nil)
+	future := time.Now().UTC().Add(time.Hour)
+	expired := createAgentToken("agent-token-expired", &future)
+	revoked := createAgentToken("agent-token-revoked", nil)
+	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := credentials.Revoke(ctx, revoked.TokenID); err != nil {
+		t.Fatal(err)
+	}
+	if _, err := db.SQL().ExecContext(ctx, `UPDATE service_tokens SET expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), expired.TokenID); err != nil {
+		t.Fatal(err)
+	}
+	disabledAgent, err := db.Agents().Get(ctx, "agent-token-disabled")
+	if err != nil {
+		t.Fatal(err)
+	}
+	disabledAgent.Enabled = false
+	if err := db.Agents().Update(ctx, disabledAgent); err != nil {
+		t.Fatal(err)
+	}
+
+	registrations := make(chan AgentRegistration, 1)
+	runtime, err := NewServerRuntime(db, AgentSessionConfig{Authenticate: func(_ context.Context, registration AgentRegistration) error {
+		select {
+		case registrations <- registration:
+		default:
+		}
+		return nil
+	}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer runtime.Close()
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	serverCtx, cancel := context.WithCancel(ctx)
+	defer cancel()
+	serveErr := make(chan error, 1)
+	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
+	defer func() {
+		cancel()
+		<-serveErr
+	}()
+
+	tests := []struct {
+		name       string
+		token      string
+		agentID    string
+		queryToken string
+		cookie     string
+	}{
+		{name: "missing token", agentID: "agent-token-valid"},
+		{name: "management token", token: management.Token, agentID: "agent-token-valid"},
+		{name: "client token", token: clientToken.Secret, agentID: "agent-token-valid"},
+		{name: "wrong agent binding", token: other.Secret, agentID: "agent-token-valid"},
+		{name: "disabled agent", token: disabled.Secret, agentID: "agent-token-disabled"},
+		{name: "revoked token", token: revoked.Secret, agentID: "agent-token-revoked"},
+		{name: "expired token", token: expired.Secret, agentID: "agent-token-expired"},
+		{name: "query token", agentID: "agent-token-valid", queryToken: valid.Secret},
+		{name: "cookie token", agentID: "agent-token-valid", cookie: "token=" + valid.Secret},
+	}
+	for i, tc := range tests {
+		t.Run(tc.name, func(t *testing.T) {
+			if agentWebSocketAccepted(t, listener.Addr().String(), tc.token, tc.agentID, int64(i+1), tc.queryToken, tc.cookie) {
+				t.Fatal("unauthorized Agent received a metadata acknowledgement")
+			}
+		})
+	}
+	if !agentWebSocketAccepted(t, listener.Addr().String(), valid.Secret, "agent-token-valid", 100, "", "") {
+		t.Fatal("valid Agent token was rejected")
+	}
+	select {
+	case registration := <-registrations:
+		if registration.TokenID != valid.TokenID {
+			t.Fatalf("registration TokenID = %q, want %q", registration.TokenID, valid.TokenID)
+		}
+	case <-time.After(time.Second):
+		t.Fatal("valid Agent registration was not observed")
+	}
+}
+
+func TestAgentWebSocketHostAndOriginAllowlist(t *testing.T) {
+	ctx := context.Background()
+	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-allowlist?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	authService := auth.NewAuthService(db)
+	owner, err := authService.CreateUser(ctx, "allowlist-owner", "owner-pass", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Agents().Create(ctx, storage.Agent{ID: "allowlist-agent", Name: "allowlist-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
+		t.Fatal(err)
+	}
+	credentials := auth.NewCredentialService(db)
+	created, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "allowlist-agent"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	_ = credentials.Close()
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	address := listener.Addr().String()
+	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{Security: config.SecurityConfig{
+		AllowedHosts:   []string{address, "EXAMPLE.COM"},
+		AllowedOrigins: []string{"http://" + address},
+	}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer runtime.Close()
+	serverCtx, cancel := context.WithCancel(ctx)
+	defer cancel()
+	serveErr := make(chan error, 1)
+	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
+	defer func() {
+		cancel()
+		<-serveErr
+	}()
+
+	if !agentWebSocketAcceptedFromOrigin(t, address, created.Secret, "allowlist-agent", 1, "http://"+address) {
+		t.Fatal("Agent dialer Origin matching the Server URL was rejected")
+	}
+	if agentWebSocketAcceptedFromOrigin(t, address, created.Secret, "allowlist-agent", 2, "https://evil.example") {
+		t.Fatal("unlisted Origin completed an Agent WebSocket session")
+	}
+	if status := rawWebSocketHandshakeStatus(t, address, "evil.example", "http://"+address, created.Secret); status != http.StatusForbidden {
+		t.Fatalf("unlisted Host handshake status = %d, want %d", status, http.StatusForbidden)
+	}
+	if status := rawWebSocketHandshakeStatus(t, address, "example.com@evil.example", "http://"+address, created.Secret); status == http.StatusSwitchingProtocols {
+		t.Fatal("malicious Host completed a WebSocket upgrade")
+	}
+	if status := rawWebSocketHandshakeStatus(t, address, "example.com", "http://"+address, created.Secret); status != http.StatusSwitchingProtocols {
+		t.Fatalf("case-normalized allowed Host status = %d, want %d", status, http.StatusSwitchingProtocols)
+	}
+}
+
+func TestAgentLegacyManagementTokenRequiresMigrationFlagAndAuditsWarning(t *testing.T) {
+	ctx := context.Background()
+	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-legacy-token?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	authService := auth.NewAuthService(db)
+	owner, err := authService.CreateUser(ctx, "legacy-owner", "owner-pass", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	login, err := authService.Login(ctx, owner.Username, "owner-pass")
+	if err != nil {
+		t.Fatal(err)
+	}
+	other, err := authService.CreateUser(ctx, "legacy-other", "other-pass", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	otherLogin, err := authService.Login(ctx, other.Username, "other-pass")
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Agents().Create(ctx, storage.Agent{ID: "legacy-agent", Name: "legacy-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Agents().Create(ctx, storage.Agent{ID: "legacy-disabled-agent", Name: "legacy-disabled-agent", OwnerUserID: owner.ID, Enabled: false}); err != nil {
+		t.Fatal(err)
+	}
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{Security: config.SecurityConfig{AllowLegacyConnectionTokens: true}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer runtime.Close()
+	var logs bytes.Buffer
+	previousLogger := slog.Default()
+	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
+	t.Cleanup(func() { slog.SetDefault(previousLogger) })
+	serverCtx, cancel := context.WithCancel(ctx)
+	defer cancel()
+	serveErr := make(chan error, 1)
+	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
+	defer func() {
+		cancel()
+		<-serveErr
+	}()
+
+	if !agentWebSocketAccepted(t, listener.Addr().String(), login.Token, "legacy-agent", 1, "", "") {
+		t.Fatal("legacy management token was rejected while migration flag was enabled")
+	}
+	if agentWebSocketAccepted(t, listener.Addr().String(), otherLogin.Token, "legacy-agent", 2, "", "") {
+		t.Fatal("legacy management token from another owner was accepted")
+	}
+	if agentWebSocketAccepted(t, listener.Addr().String(), login.Token, "legacy-disabled-agent", 1, "", "") {
+		t.Fatal("legacy management token connected a disabled Agent")
+	}
+	if !strings.Contains(logs.String(), "deprecated_connection_token") {
+		t.Fatalf("warning log = %q, want deprecated_connection_token", logs.String())
+	}
+	if strings.Contains(logs.String(), login.Token) {
+		t.Fatal("warning log leaked raw management token")
+	}
+	audits, err := db.Audits().List(ctx, "", 100)
+	if err != nil {
+		t.Fatal(err)
+	}
+	found := false
+	for _, audit := range audits.Items {
+		if audit.Action != "deprecated_connection_token" {
+			continue
+		}
+		found = true
+		if audit.ActorUserID != owner.ID || audit.ResourceID != "legacy-agent" {
+			t.Fatalf("legacy audit = %+v", audit)
+		}
+		if strings.Contains(audit.Details, login.Token) {
+			t.Fatal("legacy audit leaked raw management token")
+		}
+	}
+	if !found {
+		t.Fatalf("deprecated connection audit missing: %+v", audits.Items)
+	}
+}
+
+func agentWebSocketAccepted(t *testing.T, address, token, agentID string, epoch int64, queryToken, cookie string) bool {
+	t.Helper()
+	return agentWebSocketAcceptedWithOptions(t, address, token, agentID, epoch, queryToken, cookie, "http://"+address)
+}
+
+func agentWebSocketAcceptedFromOrigin(t *testing.T, address, token, agentID string, epoch int64, origin string) bool {
+	t.Helper()
+	return agentWebSocketAcceptedWithOptions(t, address, token, agentID, epoch, "", "", origin)
+}
+
+func agentWebSocketAcceptedWithOptions(t *testing.T, address, token, agentID string, epoch int64, queryToken, cookie, origin string) bool {
+	t.Helper()
+	location := &url.URL{Scheme: "ws", Host: address, Path: "/ws/agent"}
+	if queryToken != "" {
+		location.RawQuery = "token=" + url.QueryEscape(queryToken)
+	}
+	config, err := websocket.NewConfig(location.String(), origin)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if token != "" {
+		config.Header.Set("Authorization", "Bearer "+token)
+	}
+	if cookie != "" {
+		config.Header.Set("Cookie", cookie)
+	}
+	ws, err := websocket.DialConfig(config)
+	if err != nil {
+		return false
+	}
+	defer ws.Close()
+	payload, err := protocol.EncodeAgentMetadataPayload(protocol.AgentMetadataPayload{AgentID: agentID, NodeID: "node-token-boundary", Epoch: epoch, Revision: 1})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := websocket.Message.Send(ws, frameBytes(t, protocol.FrameAgentHello, payload)); err != nil {
+		return false
+	}
+	_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
+	var ackBytes []byte
+	if err := websocket.Message.Receive(ws, &ackBytes); err != nil {
+		return false
+	}
+	frame, err := protocol.NewDecoder(bytes.NewReader(ackBytes)).ReadFrame()
+	if err != nil || frame.Type != protocol.FrameAgentMetadataAck {
+		return false
+	}
+	ack, err := protocol.DecodeAgentMetadataAckPayload(frame.Payload)
+	return err == nil && ack.Accepted
+}
+
+func rawWebSocketHandshakeStatus(t *testing.T, address, host, origin, token string) int {
+	t.Helper()
+	conn, err := net.Dial("tcp", address)
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer conn.Close()
+	request := fmt.Sprintf("GET /ws/agent HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nAuthorization: Bearer %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", host, origin, token)
+	if _, err := conn.Write([]byte(request)); err != nil {
+		t.Fatal(err)
+	}
+	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
+	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer response.Body.Close()
+	return response.StatusCode
+}
+
 func TestRealAgentReconnectsWithNewEpochAfterServerClosesSession(t *testing.T) {
 	db, err := storage.OpenSQLite(context.Background(), "file:runtime-agent-reconnect?mode=memory&cache=shared")
 	if err != nil {
@@ -331,14 +713,17 @@
 	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "reconnect-agent", Name: "reconnect-agent", OwnerUserID: user.ID, Enabled: true}); err != nil {
 		t.Fatal(err)
 	}
-	login, err := authService.Login(context.Background(), user.Username, "reconnect-pass")
+	credentials := auth.NewCredentialService(db)
+	agentToken, err := credentials.Create(context.Background(), auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: user.ID, AgentID: "reconnect-agent"})
 	if err != nil {
 		t.Fatal(err)
 	}
+	_ = credentials.Close()
 	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
 	if err != nil {
 		t.Fatal(err)
 	}
+	defer runtime.Close()
 	listener, err := net.Listen("tcp", "127.0.0.1:0")
 	if err != nil {
 		t.Fatal(err)
@@ -352,7 +737,7 @@
 	agentCtx, agentCancel := context.WithCancel(context.Background())
 	agentErr := make(chan error, 1)
 	go func() {
-		agentErr <- agent.RunWebSocketWithOptions(agentCtx, "ws://"+listener.Addr().String()+"/ws/agent", login.Token, "reconnect-agent", "node-reconnect", 1, agent.NewMetadataCollector([]config.MetadataSource{{Name: "region", Source: "env", Key: "TUNNELMESH_RECONNECT_REGION"}}), nil, agent.WebSocketRunOptions{BaseBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond, Rand: rand.New(rand.NewSource(1))})
+		agentErr <- agent.RunWebSocketWithOptions(agentCtx, "ws://"+listener.Addr().String()+"/ws/agent", agentToken.Secret, "reconnect-agent", "node-reconnect", 1, agent.NewMetadataCollector([]config.MetadataSource{{Name: "region", Source: "env", Key: "TUNNELMESH_RECONNECT_REGION"}}), nil, agent.WebSocketRunOptions{BaseBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond, Rand: rand.New(rand.NewSource(1))})
 	}()
 	waitForEpoch := func(want int64) storage.AgentRuntimeMetadata {
 		deadline := time.Now().Add(2 * time.Second)
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/session_manager.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/session_manager.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/session_manager.go	2026-09-06 16:14:44
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/session_manager.go	2026-09-06 16:50:34
@@ -34,9 +34,12 @@
 }
 
 type AgentRegistration struct {
-	AgentID, NodeID, Token string
-	Epoch                  int64
-	Capabilities           []string
+	AgentID, NodeID string
+	// TokenID is the non-secret credential identifier used by audit/metrics.
+	// Raw bearer tokens must not be retained in session state.
+	TokenID      string
+	Epoch        int64
+	Capabilities []string
 }
 type AgentSessionConfig struct {
 	SupportedCapabilities []string
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/tls_listener.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/tls_listener.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/tls_listener.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/tls_listener.go	2026-09-06 16:50:34
@@ -0,0 +1,43 @@
+package server
+
+import (
+	"crypto/tls"
+	"fmt"
+	"net"
+	"strings"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/config"
+)
+
+// nativeTLSListener applies optional process-native TLS termination. The
+// disabled case deliberately returns the original listener for internal HTTP
+// deployments behind Nginx.
+func nativeTLSListener(listener net.Listener, cfg config.TLSConfig) (net.Listener, error) {
+	if listener == nil {
+		return nil, fmt.Errorf("native TLS listener is required")
+	}
+	if !cfg.Enabled {
+		return listener, nil
+	}
+	if strings.TrimSpace(cfg.CertFile) == "" || strings.TrimSpace(cfg.KeyFile) == "" {
+		return nil, fmt.Errorf("native TLS requires certificate and private key files")
+	}
+	var minVersion uint16
+	switch cfg.MinVersion {
+	case "1.2":
+		minVersion = tls.VersionTLS12
+	case "1.3":
+		minVersion = tls.VersionTLS13
+	default:
+		return nil, fmt.Errorf("native TLS minimum version must be 1.2 or 1.3")
+	}
+	pair, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
+	if err != nil {
+		return nil, fmt.Errorf("load native TLS certificate: %w", err)
+	}
+	tlsConfig := &tls.Config{
+		Certificates: []tls.Certificate{pair},
+		MinVersion:   minVersion,
+	}
+	return tls.NewListener(listener, tlsConfig), nil
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/tls_listener_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/tls_listener_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/tls_listener_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/tls_listener_test.go	2026-09-06 16:50:34
@@ -0,0 +1,193 @@
+package server
+
+import (
+	"bufio"
+	"context"
+	"crypto/rand"
+	"crypto/rsa"
+	"crypto/tls"
+	"crypto/x509"
+	"crypto/x509/pkix"
+	"encoding/pem"
+	"io"
+	"math/big"
+	"net"
+	"net/http"
+	"os"
+	"path/filepath"
+	"strings"
+	"testing"
+	"time"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/config"
+	"github.com/tunnelmesh/tunnelmesh/internal/storage"
+)
+
+func TestNativeTLSDisabledPreservesInternalHTTPListener(t *testing.T) {
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer listener.Close()
+
+	wrapped, err := nativeTLSListener(listener, config.TLSConfig{Enabled: false})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if wrapped != listener {
+		t.Fatal("disabled native TLS replaced the internal HTTP listener")
+	}
+}
+
+func TestNativeTLSRejectsMismatchedCertificateAndKeyBeforeServing(t *testing.T) {
+	certFile, _, _ := writeNativeTLSCertificate(t)
+	_, otherKeyFile, _ := writeNativeTLSCertificate(t)
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	address := listener.Addr().String()
+	db, err := storage.OpenSQLite(context.Background(), "file:native-tls-mismatch?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{TLS: config.TLSConfig{Enabled: true, CertFile: certFile, KeyFile: otherKeyFile, MinVersion: "1.2"}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer runtime.Close()
+
+	if err := runtime.ServeListener(context.Background(), listener); err == nil {
+		t.Fatal("ServeListener accepted a mismatched certificate and private key")
+	}
+	if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
+		_ = conn.Close()
+		t.Fatal("listener remained open after native TLS setup failure")
+	}
+}
+
+func TestNativeTLSServesTLS12AndRejectsPlaintext(t *testing.T) {
+	certFile, keyFile, roots := writeNativeTLSCertificate(t)
+	db, err := storage.OpenSQLite(context.Background(), "file:native-tls-runtime?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{TLS: config.TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, MinVersion: "1.2"}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer runtime.Close()
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	address := listener.Addr().String()
+	serverCtx, cancel := context.WithCancel(context.Background())
+	serveErr := make(chan error, 1)
+	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
+	defer func() {
+		cancel()
+		<-serveErr
+	}()
+
+	dialer := &net.Dialer{Timeout: time.Second}
+	if conn, err := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}); err == nil {
+		_ = conn.Close()
+		t.Fatal("native TLS accepted a TLS 1.1 client")
+	}
+	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}}}
+	response, err := client.Get("https://" + address + "/")
+	if err != nil {
+		t.Fatal(err)
+	}
+	_, _ = io.Copy(io.Discard, response.Body)
+	_ = response.Body.Close()
+	if response.StatusCode != http.StatusOK {
+		t.Fatalf("TLS 1.2 response status = %d, want %d", response.StatusCode, http.StatusOK)
+	}
+
+	plain, err := net.DialTimeout("tcp", address, time.Second)
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer plain.Close()
+	if _, err := plain.Write([]byte("GET / HTTP/1.1\r\nHost: " + address + "\r\nConnection: close\r\n\r\n")); err != nil {
+		t.Fatal(err)
+	}
+	_ = plain.SetReadDeadline(time.Now().Add(time.Second))
+	line, _ := bufio.NewReader(plain).ReadString('\n')
+	if strings.Contains(line, "200 OK") {
+		t.Fatalf("native TLS listener served plaintext HTTP: %q", line)
+	}
+}
+
+func TestNativeTLS13RejectsTLS12(t *testing.T) {
+	certFile, keyFile, roots := writeNativeTLSCertificate(t)
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer listener.Close()
+	wrapped, err := nativeTLSListener(listener, config.TLSConfig{Enabled: true, CertFile: certFile, KeyFile: keyFile, MinVersion: "1.3"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
+	serveErr := make(chan error, 1)
+	go func() { serveErr <- server.Serve(wrapped) }()
+	defer func() {
+		_ = server.Close()
+		<-serveErr
+	}()
+	dialer := &net.Dialer{Timeout: time.Second}
+	if conn, err := tls.DialWithDialer(dialer, "tcp", listener.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}); err == nil {
+		_ = conn.Close()
+		t.Fatal("TLS 1.3 minimum accepted a TLS 1.2 client")
+	}
+	conn, err := tls.DialWithDialer(dialer, "tcp", listener.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13})
+	if err != nil {
+		t.Fatalf("TLS 1.3 client failed: %v", err)
+	}
+	_ = conn.Close()
+}
+
+func writeNativeTLSCertificate(t *testing.T) (string, string, *x509.CertPool) {
+	t.Helper()
+	key, err := rsa.GenerateKey(rand.Reader, 2048)
+	if err != nil {
+		t.Fatal(err)
+	}
+	now := time.Now().UTC()
+	template := &x509.Certificate{
+		SerialNumber: big.NewInt(20260906),
+		Subject:      pkix.Name{CommonName: "localhost"},
+		NotBefore:    now.Add(-time.Minute),
+		NotAfter:     now.Add(time.Hour),
+		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
+		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
+		DNSNames:     []string{"localhost"},
+		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
+	}
+	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
+	if err != nil {
+		t.Fatal(err)
+	}
+	dir := t.TempDir()
+	certFile := filepath.Join(dir, "server.crt")
+	keyFile := filepath.Join(dir, "server.key")
+	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
+		t.Fatal(err)
+	}
+	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
+		t.Fatal(err)
+	}
+	certificate, err := x509.ParseCertificate(der)
+	if err != nil {
+		t.Fatal(err)
+	}
+	roots := x509.NewCertPool()
+	roots.AddCert(certificate)
+	return certFile, keyFile, roots
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/web.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/web.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-before/internal/server/web.go	2026-09-06 16:14:44
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/web.go	2026-09-06 16:50:34
@@ -26,12 +26,16 @@
 			}
 			return
 		}
-		if strings.HasPrefix(r.URL.Path, "/ws/") {
+		if r.URL.Path == "/ws/agent" {
 			if ws == nil {
 				http.NotFound(w, r)
 			} else {
 				ws.ServeHTTP(w, r)
 			}
+			return
+		}
+		if strings.HasPrefix(r.URL.Path, "/ws/") {
+			http.NotFound(w, r)
 			return
 		}
 		path := strings.TrimPrefix(r.URL.Path, "/")
```
