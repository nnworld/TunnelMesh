// Package config owns loading, validation, and redacted rendering of
// TunnelMesh configuration.  It is intentionally independent from command
// handlers so server, agent, and client can share one source of truth.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/viper"
	"golang.org/x/net/http/httpguts"
)

const (
	ModeLocal   = "local"
	ModeCluster = "cluster"

	StorageSQLite = "sqlite"
	StorageMySQL  = "mysql"

	RegistryDatabase = "database"
	RegistryEtcd     = "etcd"
)

// ConfigOptions controls the sources used by Load.  CLI and Overrides are
// applied last; Overrides is retained as a convenient alias for callers that
// do not want to name their values "CLI".  Env is useful for tests and
// embedders; process environment variables are always read as well.
type ConfigOptions struct {
	ConfigFile string
	// ConfigPath and File are accepted aliases for ConfigFile for callers that
	// model the option after a CLI flag or a generic file source.
	ConfigPath string
	File       string
	CLI        any
	Overrides  any
	Env        any
	Set        any
	Viper      *viper.Viper
}

type Config struct {
	Mode     string         `mapstructure:"mode" json:"mode" yaml:"mode"`
	Storage  StorageConfig  `mapstructure:"storage" json:"storage" yaml:"storage"`
	Registry RegistryConfig `mapstructure:"registry" json:"registry" yaml:"registry"`
	Node     NodeConfig     `mapstructure:"node" json:"node" yaml:"node"`
	Server   ServerConfig   `mapstructure:"server" json:"server" yaml:"server"`
	Security SecurityConfig `mapstructure:"security" json:"security" yaml:"security"`
	TLS      TLSConfig      `mapstructure:"tls" json:"tls" yaml:"tls"`
	Agent    AgentConfig    `mapstructure:"agent" json:"agent" yaml:"agent"`
	Client   ClientConfig   `mapstructure:"client" json:"client" yaml:"client"`
}

type StorageConfig struct {
	Driver     string       `mapstructure:"driver" json:"driver" yaml:"driver"`
	SQLite     SQLiteConfig `mapstructure:"sqlite" json:"sqlite" yaml:"sqlite"`
	AutoInit   bool         `mapstructure:"auto_init" json:"auto_init" yaml:"auto_init"`
	MySQL      MySQLConfig  `mapstructure:"mysql" json:"mysql" yaml:"mysql"`
	SQLitePath string       `mapstructure:"-" json:"-" yaml:"-"`
	MySQLDSN   string       `mapstructure:"-" json:"-" yaml:"-"`
	MySQLTLS   bool         `mapstructure:"-" json:"-" yaml:"-"`
}

type SQLiteConfig struct {
	Path string `mapstructure:"path" json:"path" yaml:"path"`
}

type MySQLConfig struct {
	DSN  string `mapstructure:"dsn" json:"dsn" yaml:"dsn"`
	TLS  bool   `mapstructure:"tls" json:"tls" yaml:"tls"`
	CA   string `mapstructure:"ca" json:"ca" yaml:"ca"`
	Cert string `mapstructure:"cert" json:"cert" yaml:"cert"`
	Key  string `mapstructure:"key" json:"key" yaml:"key"`
}

type RegistryConfig struct {
	Type          string   `mapstructure:"type" json:"type" yaml:"type"`
	Endpoints     []string `mapstructure:"endpoints" json:"endpoints" yaml:"endpoints"`
	EtcdEndpoints []string `mapstructure:"-" json:"-" yaml:"-"`
}

type NodeConfig struct {
	ID       string `mapstructure:"id" json:"id" yaml:"id"`
	Identity string `mapstructure:"-" json:"-" yaml:"-"`
}

type ServerConfig struct {
	HTTPAddr         string          `mapstructure:"http_addr" json:"http_addr" yaml:"http_addr"`
	HTTPSAddr        string          `mapstructure:"https_addr" json:"https_addr" yaml:"https_addr"`
	AgentWSAddr      string          `mapstructure:"agent_ws_addr" json:"agent_ws_addr" yaml:"agent_ws_addr"`
	ClientWSAddr     string          `mapstructure:"client_ws_addr" json:"client_ws_addr" yaml:"client_ws_addr"`
	TCPBridge        TCPBridgeConfig `mapstructure:"tcp_bridge" json:"tcp_bridge" yaml:"tcp_bridge"`
	TCPBridgeEnabled bool            `mapstructure:"tcp_bridge_enabled" json:"tcp_bridge_enabled" yaml:"tcp_bridge_enabled"`
	Relay            RelayConfig     `mapstructure:"relay" json:"relay" yaml:"relay"`
}

type TCPBridgeConfig struct {
	Enabled  bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	Path     string `mapstructure:"path" json:"path" yaml:"path"`
	MaxBytes int64  `mapstructure:"max_bytes" json:"max_bytes" yaml:"max_bytes"`
}

// RelayConfig is deliberately separate from public HTTP/native TLS settings.
// NodeToken is never serialized in config output.
type RelayConfig struct {
	Enabled    bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	Listen     string `mapstructure:"listen" json:"listen" yaml:"listen"`
	Endpoint   string `mapstructure:"endpoint" json:"endpoint" yaml:"endpoint"`
	CA         string `mapstructure:"ca" json:"ca" yaml:"ca"`
	Cert       string `mapstructure:"cert" json:"cert" yaml:"cert"`
	Key        string `mapstructure:"key" json:"key" yaml:"key"`
	ServerName string `mapstructure:"server_name" json:"server_name" yaml:"server_name"`
	NodeToken  string `mapstructure:"node_token" json:"-" yaml:"-"`
}

type SecurityConfig struct {
	AllowedHosts   []string `mapstructure:"allowed_hosts" json:"allowed_hosts" yaml:"allowed_hosts"`
	AllowedOrigins []string `mapstructure:"allowed_origins" json:"allowed_origins" yaml:"allowed_origins"`
	// Deprecated: legacy management connection tokens are removed in v0.3.0.
	AllowLegacyConnectionTokens bool `mapstructure:"allow_legacy_connection_tokens" json:"allow_legacy_connection_tokens" yaml:"allow_legacy_connection_tokens"`
}

type TLSConfig struct {
	Enabled    bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	CertFile   string `mapstructure:"cert_file" json:"cert_file" yaml:"cert_file"`
	KeyFile    string `mapstructure:"key_file" json:"key_file" yaml:"key_file"`
	MinVersion string `mapstructure:"min_version" json:"min_version" yaml:"min_version"`
}

type AgentConfig struct {
	ServerURL string           `mapstructure:"server_url" json:"server_url" yaml:"server_url"`
	ID        string           `mapstructure:"id" json:"id" yaml:"id"`
	Token     string           `mapstructure:"token" json:"-" yaml:"-"`
	Metadata  []MetadataSource `mapstructure:"metadata" json:"metadata" yaml:"metadata"`
}

// MetadataSource is an explicit allowlisted host value source. File sources
// read one configured absolute path; env sources read one configured key.
type MetadataSource struct {
	Name   string `mapstructure:"name" json:"name" yaml:"name"`
	Source string `mapstructure:"source" json:"source" yaml:"source"`
	Path   string `mapstructure:"path" json:"path" yaml:"path"`
	Key    string `mapstructure:"key" json:"key" yaml:"key"`
}

const (
	MetadataMaxFields       = 32
	MetadataFieldMaxBytes   = 4 << 10
	MetadataPayloadMaxBytes = 32 << 10
)

type ClientConfig struct {
	ServerURL string         `mapstructure:"server_url" json:"server_url" yaml:"server_url"`
	Token     string         `mapstructure:"token" json:"-" yaml:"-"`
	Tunnels   []TunnelConfig `mapstructure:"tunnels" json:"tunnels" yaml:"tunnels"`
}

// TunnelConfig describes a client tunnel loaded from a configuration file.
// CLI flags can override these fields for one-off forwards.
type TunnelConfig struct {
	Name       string `mapstructure:"name" json:"name" yaml:"name"`
	Protocol   string `mapstructure:"protocol" json:"protocol" yaml:"protocol"`
	ListenAddr string `mapstructure:"listen" json:"listen" yaml:"listen"`
	AgentID    string `mapstructure:"agent_id" json:"agent_id" yaml:"agent_id"`
	TargetHost string `mapstructure:"target_host" json:"target_host" yaml:"target_host"`
	TargetPort int    `mapstructure:"target_port" json:"target_port" yaml:"target_port"`
}

// Load applies the documented precedence: CLI > environment > file >
// defaults.  Viper is used only as the source merger; the returned value is a
// typed immutable snapshot suitable for passing into services.
func Load(ctx context.Context, opts ConfigOptions) (Config, error) {
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	v := opts.Viper
	if v == nil {
		v = viper.New()
	}
	setDefaults(v)
	bindEnvironment(v)

	configFile := opts.ConfigFile
	if configFile == "" {
		configFile = opts.ConfigPath
	}
	if configFile == "" {
		configFile = opts.File
	}
	if configFile != "" {
		v.SetConfigFile(configFile)
		if err := v.ReadInConfig(); err != nil {
			return Config{}, fmt.Errorf("read config file: %w", err)
		}
	}
	setValues(v, opts.Env)
	setValues(v, opts.Overrides)
	setValues(v, opts.Set)
	setValues(v, opts.CLI)

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	// Support both the nested spelling used in files and the flat flag spelling
	// used by Cobra. The nested key is canonical, while the flat key is an
	// explicit input alias only. It intentionally has no default: otherwise its
	// default would mask an explicit nested value during precedence resolution.
	cfg.Server.TCPBridge.Enabled = v.GetBool("server.tcp_bridge.enabled")
	if shouldUseFlatBridgeAlias(opts, v) {
		cfg.Server.TCPBridge.Enabled = v.GetBool("server.tcp_bridge_enabled")
	}
	cfg.Server.TCPBridgeEnabled = cfg.Server.TCPBridge.Enabled
	cfg.Storage.SQLitePath = cfg.Storage.SQLite.Path
	cfg.Storage.MySQLDSN = cfg.Storage.MySQL.DSN
	cfg.Storage.MySQLTLS = cfg.Storage.MySQL.TLS
	cfg.Registry.EtcdEndpoints = append([]string(nil), cfg.Registry.Endpoints...)
	cfg.Node.Identity = cfg.Node.ID
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func setValues(v *viper.Viper, source any) {
	switch values := source.(type) {
	case map[string]any:
		for key, value := range values {
			v.Set(normalizeKey(key), value)
		}
	case map[string]string:
		for key, value := range values {
			v.Set(normalizeKey(key), value)
		}
	case nil:
		return
	}
}

func normalizeKey(key string) string {
	if key == "server.tcp_bridge_enabled" {
		return "server.tcp_bridge.enabled"
	}
	return key
}

func shouldUseFlatBridgeAlias(opts ConfigOptions, v *viper.Viper) bool {
	// Values supplied by callers have an unambiguous precedence order. Since
	// setValues normalizes aliases, a caller-provided flat key is already
	// represented by the canonical key and does not need alias resolution.
	for _, source := range []any{opts.CLI, opts.Set, opts.Overrides, opts.Env} {
		if hasKey(source, "server.tcp_bridge.enabled") || hasKey(source, "server.tcp_bridge_enabled") {
			return false
		}
	}
	// The nested and flat environment spellings map to the same environment
	// variable under TUNNELMESH_*; canonical Viper resolution already handled
	// it, so do not apply the flat alias a second time.
	if _, ok := os.LookupEnv("TUNNELMESH_SERVER_TCP_BRIDGE_ENABLED"); ok {
		return false
	}
	// A flat key in a config file remains useful for backwards compatibility.
	return v.InConfig("server.tcp_bridge_enabled") && !v.InConfig("server.tcp_bridge.enabled")
}

func hasKey(source any, want string) bool {
	switch values := source.(type) {
	case map[string]any:
		_, ok := values[want]
		if ok {
			return true
		}
		_, ok = values["server.tcp_bridge_enabled"]
		return want == "server.tcp_bridge.enabled" && ok
	case map[string]string:
		_, ok := values[want]
		if ok {
			return true
		}
		_, ok = values["server.tcp_bridge_enabled"]
		return want == "server.tcp_bridge.enabled" && ok
	default:
		return false
	}
}

func setDefaults(v *viper.Viper) {
	defaults := map[string]any{
		"mode":                                    ModeLocal,
		"storage.driver":                          StorageSQLite,
		"storage.sqlite.path":                     "tunnelmesh.db",
		"storage.auto_init":                       true,
		"storage.mysql.tls":                       false,
		"registry.type":                           RegistryDatabase,
		"registry.endpoints":                      []string{},
		"server.http_addr":                        ":80",
		"server.https_addr":                       ":443",
		"server.agent_ws_addr":                    ":443",
		"server.client_ws_addr":                   ":443",
		"server.tcp_bridge.enabled":               true,
		"server.tcp_bridge.path":                  "/ws/tcp",
		"server.tcp_bridge.max_bytes":             int64(64 << 10),
		"server.relay.enabled":                    false,
		"server.relay.listen":                     "",
		"server.relay.endpoint":                   "",
		"server.relay.ca":                         "",
		"server.relay.cert":                       "",
		"server.relay.key":                        "",
		"server.relay.server_name":                "",
		"security.allowed_hosts":                  []string{},
		"security.allowed_origins":                []string{},
		"security.allow_legacy_connection_tokens": false,
		"tls.enabled":                             false,
		"tls.min_version":                         "1.2",
	}
	for key, value := range defaults {
		v.SetDefault(key, value)
	}
}

func bindEnvironment(v *viper.Viper) {
	v.SetEnvPrefix("TUNNELMESH")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
	keys := []string{
		"mode", "storage.driver", "storage.sqlite.path", "storage.auto_init",
		"storage.mysql.dsn", "storage.mysql.tls", "storage.mysql.ca", "storage.mysql.cert", "storage.mysql.key",
		"registry.type", "registry.endpoints", "node.id", "server.http_addr", "server.https_addr",
		"server.agent_ws_addr", "server.client_ws_addr", "server.tcp_bridge.enabled", "server.tcp_bridge_enabled",
		"security.allowed_hosts", "security.allowed_origins", "security.allow_legacy_connection_tokens",
		"tls.enabled", "tls.cert_file", "tls.key_file", "tls.min_version",
		"server.relay.enabled", "server.relay.listen", "server.relay.endpoint", "server.relay.ca", "server.relay.cert", "server.relay.key", "server.relay.server_name", "server.relay.node_token",
		"agent.server_url", "agent.id", "agent.token", "client.server_url", "client.token",
	}
	for _, key := range keys {
		_ = v.BindEnv(key)
	}
}

func Validate(cfg Config) error {
	var problems []string
	problems = append(problems, validateMetadataSources(cfg.Agent.Metadata)...)
	problems = append(problems, validateSecurity(cfg.Security)...)
	problems = append(problems, validateTLS(cfg.TLS)...)
	problems = append(problems, validateRelay(cfg.Mode, cfg.Node.ID, cfg.Server.Relay)...)
	if cfg.Agent.ServerURL != "" && !validWebSocketURL(cfg.Agent.ServerURL) {
		problems = append(problems, "agent server URL must be an absolute ws:// or wss:// URL")
	}
	if cfg.Client.ServerURL != "" && !validWebSocketURL(cfg.Client.ServerURL) {
		problems = append(problems, "client server URL must be an absolute ws:// or wss:// URL")
	}
	switch cfg.Mode {
	case ModeLocal:
		if cfg.Storage.Driver != StorageSQLite {
			problems = append(problems, "local mode requires sqlite storage driver")
		}
	case ModeCluster:
		if cfg.Storage.Driver != StorageMySQL {
			problems = append(problems, "cluster mode requires mysql storage driver")
		}
		if strings.TrimSpace(cfg.Storage.MySQL.DSN) == "" {
			problems = append(problems, "cluster mode requires mysql DSN")
		}
		if !cfg.Storage.MySQL.TLS {
			problems = append(problems, "cluster mode requires mysql TLS")
		}
		if strings.TrimSpace(cfg.Node.ID) == "" {
			problems = append(problems, "cluster mode requires node identity")
		}
	default:
		problems = append(problems, fmt.Sprintf("invalid mode %q", cfg.Mode))
	}
	switch cfg.Storage.Driver {
	case StorageSQLite, StorageMySQL:
	default:
		problems = append(problems, fmt.Sprintf("invalid storage driver %q", cfg.Storage.Driver))
	}
	switch cfg.Registry.Type {
	case RegistryDatabase:
	case RegistryEtcd:
		if len(cfg.Registry.Endpoints) == 0 {
			problems = append(problems, "etcd registry requires endpoints")
		}
	default:
		problems = append(problems, fmt.Sprintf("invalid registry type %q", cfg.Registry.Type))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validateSecurity(cfg SecurityConfig) []string {
	var problems []string
	for _, host := range cfg.AllowedHosts {
		if _, ok := NormalizeAllowedHost(host); !ok {
			problems = append(problems, fmt.Sprintf("allowed host %q is invalid", host))
		}
	}
	for _, origin := range cfg.AllowedOrigins {
		if !validHTTPOrigin(origin) {
			problems = append(problems, fmt.Sprintf("allowed origin %q must be an absolute http/https origin", origin))
		}
	}
	return problems
}

// NormalizeAllowedHost validates an exact HTTP Host allowlist entry and
// normalizes only its case. Ports remain part of the exact match.
func NormalizeAllowedHost(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "@") || !httpguts.ValidHostHeader(raw) {
		return "", false
	}
	host := raw
	if strings.HasPrefix(raw, "[") {
		end := strings.IndexByte(raw, ']')
		if end < 0 || net.ParseIP(raw[1:end]) == nil {
			return "", false
		}
		remainder := raw[end+1:]
		if remainder != "" && (remainder[0] != ':' || !validHostPort(remainder[1:])) {
			return "", false
		}
		return strings.ToLower(raw), true
	}
	if strings.Count(raw, ":") > 1 {
		return "", false
	}
	if strings.Contains(raw, ":") {
		var port string
		var err error
		host, port, err = net.SplitHostPort(raw)
		if err != nil || !validHostPort(port) {
			return "", false
		}
	}
	if net.ParseIP(host) == nil && !validDNSHost(host) {
		return "", false
	}
	return strings.ToLower(raw), true
}

func validHostPort(raw string) bool {
	port, err := strconv.Atoi(raw)
	return err == nil && port >= 1 && port <= 65535
}

func validDNSHost(raw string) bool {
	if strings.HasSuffix(raw, ".") {
		raw = strings.TrimSuffix(raw, ".")
	}
	if raw == "" || len(raw) > 253 {
		return false
	}
	for _, label := range strings.Split(raw, ".") {
		if len(label) == 0 || len(label) > 63 || !asciiAlphaNumeric(label[0]) || !asciiAlphaNumeric(label[len(label)-1]) {
			return false
		}
		for i := 1; i < len(label)-1; i++ {
			if !asciiAlphaNumeric(label[i]) && label[i] != '-' {
				return false
			}
		}
	}
	return true
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func validHTTPOrigin(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return false
	}
	return u.Path == "" && u.RawPath == "" && u.RawQuery == "" && u.Fragment == ""
}

func validWebSocketURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	return u.IsAbs()
}

func validateTLS(cfg TLSConfig) []string {
	if !cfg.Enabled {
		return nil
	}
	var problems []string
	if strings.TrimSpace(cfg.CertFile) == "" {
		problems = append(problems, "native TLS requires a certificate file")
	}
	if strings.TrimSpace(cfg.KeyFile) == "" {
		problems = append(problems, "native TLS requires a private key file")
	}
	if cfg.MinVersion != "1.2" && cfg.MinVersion != "1.3" {
		problems = append(problems, "native TLS minimum version must be 1.2 or 1.3")
	}
	return problems
}

func validateRelay(mode, nodeID string, cfg RelayConfig) []string {
	if !cfg.Enabled {
		return nil
	}
	var problems []string
	if mode != ModeCluster {
		problems = append(problems, "server relay requires cluster mode")
	}
	if strings.TrimSpace(nodeID) == "" {
		problems = append(problems, "server relay requires node identity")
	}
	if strings.TrimSpace(cfg.Listen) == "" {
		problems = append(problems, "server relay requires listen address")
	}
	if strings.TrimSpace(cfg.CA) == "" || !filepath.IsAbs(cfg.CA) {
		problems = append(problems, "server relay CA path must be absolute")
	}
	if strings.TrimSpace(cfg.Cert) == "" || !filepath.IsAbs(cfg.Cert) {
		problems = append(problems, "server relay certificate path must be absolute")
	}
	if strings.TrimSpace(cfg.Key) == "" || !filepath.IsAbs(cfg.Key) {
		problems = append(problems, "server relay private key path must be absolute")
	}
	if strings.TrimSpace(cfg.ServerName) == "" {
		problems = append(problems, "server relay server name is required")
	}
	if strings.TrimSpace(cfg.NodeToken) == "" {
		problems = append(problems, "server relay node token is required")
	}
	return problems
}

var metadataNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var metadataSensitivePattern = regexp.MustCompile(`(?i)(password|token|secret|private[_-]?key|dsn)`)

func validateMetadataSources(sources []MetadataSource) []string {
	var problems []string
	if len(sources) > MetadataMaxFields {
		problems = append(problems, fmt.Sprintf("metadata field count exceeds %d", MetadataMaxFields))
	}
	seen := make(map[string]struct{}, len(sources))
	for i, source := range sources {
		prefix := fmt.Sprintf("metadata[%d]", i)
		if source.Name == "" || len(source.Name) > 64 || !metadataNamePattern.MatchString(source.Name) {
			problems = append(problems, prefix+" name must match [a-zA-Z0-9_.-] and be at most 64 characters")
		}
		if metadataSensitivePattern.MatchString(source.Name) {
			problems = append(problems, prefix+" name is sensitive and cannot be reported")
		}
		if _, ok := seen[source.Name]; ok {
			problems = append(problems, prefix+" name is duplicated")
		}
		seen[source.Name] = struct{}{}
		switch source.Source {
		case "file":
			if !filepath.IsAbs(source.Path) {
				problems = append(problems, prefix+" file path must be absolute")
			}
			if source.Key != "" {
				problems = append(problems, prefix+" file source cannot set env key")
			}
		case "env":
			if source.Key == "" {
				problems = append(problems, prefix+" env source requires a key")
			} else if strings.ContainsAny(source.Key, "*?[]") {
				problems = append(problems, prefix+" env key cannot contain wildcard")
			}
			if source.Path != "" {
				problems = append(problems, prefix+" env source cannot set file path")
			}
		default:
			problems = append(problems, prefix+" source must be file or env")
		}
	}
	return problems
}

// RedactedJSON returns a stable, machine-readable representation safe for
// console output.  DSNs and private key paths are intentionally omitted.
func (c Config) RedactedJSON() ([]byte, error) {
	copy := c
	copy.Storage.MySQL.DSN = redact(copy.Storage.MySQL.DSN)
	copy.Storage.MySQL.Key = redact(copy.Storage.MySQL.Key)
	copy.TLS.KeyFile = redact(copy.TLS.KeyFile)
	copy.Server.Relay.Key = redact(copy.Server.Relay.Key)
	copy.Server.Relay.NodeToken = redact(copy.Server.Relay.NodeToken)
	return json.MarshalIndent(copy, "", "  ")
}

func redact(value string) string {
	if value == "" {
		return ""
	}
	return "[redacted]"
}

// ConfigFileExists reports whether a path points to a regular file.  It is a
// small helper for command shells that want to provide a friendly diagnostic.
func ConfigFileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
