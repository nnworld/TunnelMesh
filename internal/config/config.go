// Package config owns loading, validation, and redacted rendering of
// TunnelMesh configuration.  It is intentionally independent from command
// handlers so server, agent, and client can share one source of truth.
package config

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
	"golang.org/x/net/http/httpguts"
	"gopkg.in/yaml.v3"
)

const (
	ModeLocal   = "local"
	ModeCluster = "cluster"

	StorageSQLite = "sqlite"
	StorageMySQL  = "mysql"

	RegistryDatabase = "database"
	RegistryEtcd     = "etcd"
	// DefaultNodeIDPath is writable by the packaged systemd service and keeps
	// generated cluster identities outside the read-only /etc configuration.
	DefaultNodeIDPath = "/var/lib/tunnelmesh/node-id"
	// DefaultAgentInstanceIDPath keeps a stable physical Agent instance identity
	// outside the read-only /etc configuration. It must stay inside the Agent
	// systemd unit's ReadWritePaths directory.
	DefaultAgentInstanceIDPath = "/var/lib/tunnelmesh-agent/agent-instance-id"
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
	// NodeIDPath persists an automatically generated cluster node identity.
	// It is intentionally opt-in so read-only config validation has no side effects.
	NodeIDPath string
	// PersistGeneratedNodeIDToConfig writes a generated node.id back into the
	// YAML source. Only long-running Server startup enables this side effect.
	PersistGeneratedNodeIDToConfig bool
	// AgentInstanceIDPath persists a stable physical Agent instance identity.
	AgentInstanceIDPath string
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
	HTTPAddr           string                   `mapstructure:"http_addr" json:"http_addr" yaml:"http_addr"`
	HTTPSAddr          string                   `mapstructure:"https_addr" json:"https_addr" yaml:"https_addr"`
	AgentWSAddr        string                   `mapstructure:"agent_ws_addr" json:"agent_ws_addr" yaml:"agent_ws_addr"`
	ClientWSAddr       string                   `mapstructure:"client_ws_addr" json:"client_ws_addr" yaml:"client_ws_addr"`
	DynamicSuffix      string                   `mapstructure:"dynamic_suffix" json:"dynamic_suffix" yaml:"dynamic_suffix"`
	TCPBridge          TCPBridgeConfig          `mapstructure:"tcp_bridge" json:"tcp_bridge" yaml:"tcp_bridge"`
	TCPBridgeEnabled   bool                     `mapstructure:"tcp_bridge_enabled" json:"tcp_bridge_enabled" yaml:"tcp_bridge_enabled"`
	Relay              RelayConfig              `mapstructure:"relay" json:"relay" yaml:"relay"`
	Stream             ServerStreamConfig       `mapstructure:"stream" json:"stream" yaml:"stream"`
	AuthorizationCache AuthorizationCacheConfig `mapstructure:"authorization_cache" json:"authorization_cache" yaml:"authorization_cache"`
}

type ServerStreamConfig struct {
	MaxConcurrentOpens    int `mapstructure:"max_concurrent_opens" json:"max_concurrent_opens" yaml:"max_concurrent_opens"`
	MaxPendingOpens       int `mapstructure:"max_pending_opens" json:"max_pending_opens" yaml:"max_pending_opens"`
	InitialWindow         int `mapstructure:"initial_window" json:"initial_window" yaml:"initial_window"`
	WindowUpdateThreshold int `mapstructure:"window_update_threshold" json:"window_update_threshold" yaml:"window_update_threshold"`
	MaxFramePayload       int `mapstructure:"max_frame_payload" json:"max_frame_payload" yaml:"max_frame_payload"`
}

type AuthorizationCacheConfig struct {
	Enabled              bool          `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	LocalPositiveTTL     time.Duration `mapstructure:"local_positive_ttl" json:"local_positive_ttl" yaml:"local_positive_ttl"`
	ClusterPositiveTTL   time.Duration `mapstructure:"cluster_positive_ttl" json:"cluster_positive_ttl" yaml:"cluster_positive_ttl"`
	NegativeTTL          time.Duration `mapstructure:"negative_ttl" json:"negative_ttl" yaml:"negative_ttl"`
	RevisionPollInterval time.Duration `mapstructure:"revision_poll_interval" json:"revision_poll_interval" yaml:"revision_poll_interval"`
	MaxStaleOnPollError  time.Duration `mapstructure:"max_stale_on_poll_error" json:"max_stale_on_poll_error" yaml:"max_stale_on_poll_error"`
	MaxEntries           int           `mapstructure:"max_entries" json:"max_entries" yaml:"max_entries"`
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
	ServerURL   string                `mapstructure:"server_url" json:"server_url" yaml:"server_url"`
	ID          string                `mapstructure:"id" json:"id" yaml:"id"`
	InstanceID  string                `mapstructure:"instance_id" json:"instance_id" yaml:"instance_id"`
	Token       string                `mapstructure:"token" json:"-" yaml:"-"`
	Connections AgentConnectionConfig `mapstructure:"connections" json:"connections" yaml:"connections"`
	Streams     AgentStreamConfig     `mapstructure:"streams" json:"streams" yaml:"streams"`
	Metadata    []MetadataSource      `mapstructure:"metadata" json:"metadata" yaml:"metadata"`
}

type AgentStreamConfig struct {
	MaxConcurrentDials int           `mapstructure:"max_concurrent_dials" json:"max_concurrent_dials" yaml:"max_concurrent_dials"`
	MaxPendingDials    int           `mapstructure:"max_pending_dials" json:"max_pending_dials" yaml:"max_pending_dials"`
	ConnectTimeout     time.Duration `mapstructure:"connect_timeout" json:"connect_timeout" yaml:"connect_timeout"`
	OpenTimeout        time.Duration `mapstructure:"open_timeout" json:"open_timeout" yaml:"open_timeout"`
	InboundBufferBytes int           `mapstructure:"inbound_buffer_bytes" json:"inbound_buffer_bytes" yaml:"inbound_buffer_bytes"`
}

type AgentConnectionConfig struct {
	Min                int           `mapstructure:"min" json:"min" yaml:"min"`
	Max                int           `mapstructure:"max" json:"max" yaml:"max"`
	HighWatermark      int           `mapstructure:"high_watermark" json:"high_watermark" yaml:"high_watermark"`
	LowWatermark       int           `mapstructure:"low_watermark" json:"low_watermark" yaml:"low_watermark"`
	EvaluationInterval time.Duration `mapstructure:"evaluation_interval" json:"evaluation_interval" yaml:"evaluation_interval"`
	Cooldown           time.Duration `mapstructure:"cooldown" json:"cooldown" yaml:"cooldown"`
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
	ServerURL        string                 `mapstructure:"server_url" json:"server_url" yaml:"server_url"`
	Token            string                 `mapstructure:"token" json:"-" yaml:"-"`
	Tunnels          []TunnelConfig         `mapstructure:"tunnels" json:"tunnels" yaml:"tunnels"`
	Stream           ClientStreamConfig     `mapstructure:"stream" json:"stream" yaml:"stream"`
	RemoteValidation RemoteValidationConfig `mapstructure:"remote_validation" json:"remote_validation" yaml:"remote_validation"`
}

type ClientStreamConfig struct {
	OpenTimeout        time.Duration `mapstructure:"open_timeout" json:"open_timeout" yaml:"open_timeout"`
	InboundBufferBytes int           `mapstructure:"inbound_buffer_bytes" json:"inbound_buffer_bytes" yaml:"inbound_buffer_bytes"`
}

type RemoteValidationConfig struct {
	PositiveTTL time.Duration `mapstructure:"positive_ttl" json:"positive_ttl" yaml:"positive_ttl"`
	NegativeTTL time.Duration `mapstructure:"negative_ttl" json:"negative_ttl" yaml:"negative_ttl"`
	Timeout     time.Duration `mapstructure:"timeout" json:"timeout" yaml:"timeout"`
	MaxEntries  int           `mapstructure:"max_entries" json:"max_entries" yaml:"max_entries"`
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
	if cfg.Mode == ModeCluster && strings.TrimSpace(cfg.Node.ID) == "" && opts.PersistGeneratedNodeIDToConfig {
		if strings.TrimSpace(configFile) == "" {
			return Config{}, errors.New("persist generated cluster node identity requires a config file")
		}
		id, err := InitializeNodeID(configFile, opts.NodeIDPath)
		if err != nil {
			return Config{}, fmt.Errorf("initialize cluster node identity: %w", err)
		}
		cfg.Node.ID, cfg.Node.Identity = id, id
	}
	if cfg.Mode == ModeCluster && strings.TrimSpace(cfg.Node.ID) == "" && strings.TrimSpace(opts.NodeIDPath) != "" {
		id, err := EnsureNodeID(opts.NodeIDPath)
		if err != nil {
			return Config{}, fmt.Errorf("ensure cluster node identity: %w", err)
		}
		cfg.Node.ID, cfg.Node.Identity = id, id
	}
	if strings.TrimSpace(cfg.Agent.InstanceID) == "" && strings.TrimSpace(opts.AgentInstanceIDPath) != "" {
		id, err := EnsureAgentInstanceID(opts.AgentInstanceIDPath)
		if err != nil {
			return Config{}, fmt.Errorf("ensure agent instance identity: %w", err)
		}
		cfg.Agent.InstanceID = id
	}
	if cfg.Server.Relay.Enabled && strings.TrimSpace(cfg.Server.Relay.Endpoint) == "" && strings.TrimSpace(cfg.Server.Relay.Listen) != "" {
		endpoint, err := InferRelayEndpoint(cfg.Server.Relay.Listen)
		if err != nil {
			return Config{}, fmt.Errorf("infer relay endpoint: %w", err)
		}
		cfg.Server.Relay.Endpoint = endpoint
	}
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
		"mode":                                               ModeLocal,
		"storage.driver":                                     StorageSQLite,
		"storage.sqlite.path":                                "tunnelmesh.db",
		"storage.auto_init":                                  true,
		"storage.mysql.tls":                                  false,
		"registry.type":                                      RegistryDatabase,
		"registry.endpoints":                                 []string{},
		"server.http_addr":                                   ":80",
		"server.https_addr":                                  ":443",
		"server.agent_ws_addr":                               ":443",
		"server.client_ws_addr":                              ":443",
		"server.dynamic_suffix":                              "apps.example.com",
		"server.tcp_bridge.enabled":                          true,
		"server.tcp_bridge.path":                             "/ws/tcp",
		"server.tcp_bridge.max_bytes":                        int64(64 << 10),
		"server.relay.enabled":                               false,
		"server.relay.listen":                                "",
		"server.relay.endpoint":                              "",
		"server.relay.ca":                                    "",
		"server.relay.cert":                                  "",
		"server.relay.key":                                   "",
		"server.relay.server_name":                           "",
		"server.stream.max_concurrent_opens":                 256,
		"server.stream.max_pending_opens":                    1024,
		"server.stream.initial_window":                       262144,
		"server.stream.window_update_threshold":              131072,
		"server.stream.max_frame_payload":                    32768,
		"server.authorization_cache.enabled":                 true,
		"server.authorization_cache.local_positive_ttl":      5 * time.Second,
		"server.authorization_cache.cluster_positive_ttl":    5 * time.Minute,
		"server.authorization_cache.negative_ttl":            3 * time.Second,
		"server.authorization_cache.revision_poll_interval":  2 * time.Second,
		"server.authorization_cache.max_stale_on_poll_error": 5 * time.Second,
		"server.authorization_cache.max_entries":             100000,
		"security.allowed_hosts":                             []string{},
		"security.allowed_origins":                           []string{},
		"security.allow_legacy_connection_tokens":            false,
		"tls.enabled":                                        false,
		"tls.min_version":                                    "1.2",
		"agent.connections.min":                              1,
		"agent.connections.max":                              1,
		"agent.connections.high_watermark":                   16,
		"agent.connections.low_watermark":                    2,
		"agent.connections.evaluation_interval":              10 * time.Second,
		"agent.connections.cooldown":                         30 * time.Second,
		"agent.streams.max_concurrent_dials":                 32,
		"agent.streams.max_pending_dials":                    128,
		"agent.streams.connect_timeout":                      5 * time.Second,
		"agent.streams.open_timeout":                         8 * time.Second,
		"agent.streams.inbound_buffer_bytes":                 262144,
		"client.stream.open_timeout":                         8 * time.Second,
		"client.stream.inbound_buffer_bytes":                 262144,
		"client.remote_validation.positive_ttl":              15 * time.Second,
		"client.remote_validation.negative_ttl":              2 * time.Second,
		"client.remote_validation.timeout":                   3 * time.Second,
		"client.remote_validation.max_entries":               10000,
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
		"server.agent_ws_addr", "server.client_ws_addr", "server.dynamic_suffix", "server.tcp_bridge.enabled", "server.tcp_bridge_enabled",
		"security.allowed_hosts", "security.allowed_origins", "security.allow_legacy_connection_tokens",
		"tls.enabled", "tls.cert_file", "tls.key_file", "tls.min_version",
		"server.relay.enabled", "server.relay.listen", "server.relay.endpoint", "server.relay.ca", "server.relay.cert", "server.relay.key", "server.relay.server_name", "server.relay.node_token",
		"server.authorization_cache.enabled",
		"agent.server_url", "agent.id", "agent.instance_id", "agent.token", "client.server_url", "client.token",
	}
	for _, key := range keys {
		_ = v.BindEnv(key)
	}
}

func Validate(cfg Config) error {
	var problems []string
	problems = append(problems, validateServerStream(cfg.Server.Stream)...)
	problems = append(problems, validateAuthorizationCache(cfg.Server.AuthorizationCache)...)
	problems = append(problems, validateAgentStreams(cfg.Agent.Streams)...)
	problems = append(problems, validateClientStreams(cfg.Client.Stream)...)
	problems = append(problems, validateRemoteValidation(cfg.Client.RemoteValidation)...)
	problems = append(problems, validateMetadataSources(cfg.Agent.Metadata)...)
	problems = append(problems, validateSecurity(cfg.Security)...)
	problems = append(problems, validateTLS(cfg.TLS)...)
	problems = append(problems, validateRelay(cfg.Mode, cfg.Node.ID, cfg.Server.Relay)...)
	if suffix := strings.TrimSpace(cfg.Server.DynamicSuffix); suffix != "" && !validDynamicSuffix(suffix) {
		problems = append(problems, fmt.Sprintf("dynamic route suffix %q must be a DNS domain without a wildcard", suffix))
	}
	if cfg.Agent.ServerURL != "" && !validWebSocketURL(cfg.Agent.ServerURL) {
		problems = append(problems, "agent server URL must be an absolute ws:// or wss:// URL")
	}
	if cfg.Agent.ServerURL != "" {
		problems = append(problems, validateAgentConnections(cfg.Agent.Connections)...)
	}
	if id := strings.TrimSpace(cfg.Agent.InstanceID); id != "" && strings.ContainsAny(id, " \t\r\n") {
		problems = append(problems, "agent instance ID must not contain whitespace")
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

func validateServerStream(cfg ServerStreamConfig) []string {
	var problems []string
	if cfg.MaxConcurrentOpens <= 0 {
		problems = append(problems, "server stream max concurrent opens must be positive")
	}
	if cfg.MaxPendingOpens <= 0 {
		problems = append(problems, "server stream max pending opens must be positive")
	}
	if cfg.InitialWindow <= 0 {
		problems = append(problems, "server stream initial window must be positive")
	}
	if cfg.WindowUpdateThreshold <= 0 || cfg.WindowUpdateThreshold > cfg.InitialWindow {
		problems = append(problems, "server stream window update threshold must be positive and no greater than the initial window")
	}
	if cfg.MaxFramePayload <= 0 || cfg.MaxFramePayload > 1<<20 {
		problems = append(problems, "server stream max frame payload must be positive and no greater than 1048576")
	}
	return problems
}

func validateAuthorizationCache(cfg AuthorizationCacheConfig) []string {
	var problems []string
	if !cfg.Enabled {
		return problems
	}
	if cfg.LocalPositiveTTL <= 0 {
		problems = append(problems, "authorization cache local positive TTL must be positive")
	}
	if cfg.ClusterPositiveTTL <= 0 {
		problems = append(problems, "authorization cache cluster positive TTL must be positive")
	}
	if cfg.NegativeTTL <= 0 {
		problems = append(problems, "authorization cache negative TTL must be positive")
	}
	if cfg.RevisionPollInterval <= 0 {
		problems = append(problems, "authorization cache revision poll interval must be positive")
	}
	if cfg.MaxStaleOnPollError <= 0 {
		problems = append(problems, "authorization cache max stale on poll error must be positive")
	}
	if cfg.MaxEntries <= 0 {
		problems = append(problems, "authorization cache max entries must be positive")
	}
	return problems
}

func validateAgentStreams(cfg AgentStreamConfig) []string {
	var problems []string
	if cfg.MaxConcurrentDials <= 0 {
		problems = append(problems, "agent stream max concurrent dials must be positive")
	}
	if cfg.MaxPendingDials <= 0 {
		problems = append(problems, "agent stream max pending dials must be positive")
	}
	if cfg.ConnectTimeout <= 0 {
		problems = append(problems, "agent stream connect timeout must be positive")
	}
	if cfg.OpenTimeout <= 0 {
		problems = append(problems, "agent stream open timeout must be positive")
	}
	if cfg.InboundBufferBytes <= 0 {
		problems = append(problems, "agent stream inbound buffer bytes must be positive")
	}
	return problems
}

func validateClientStreams(cfg ClientStreamConfig) []string {
	var problems []string
	if cfg.OpenTimeout <= 0 {
		problems = append(problems, "client stream open timeout must be positive")
	}
	if cfg.InboundBufferBytes <= 0 {
		problems = append(problems, "client stream inbound buffer bytes must be positive")
	}
	return problems
}

func validateRemoteValidation(cfg RemoteValidationConfig) []string {
	var problems []string
	if cfg.PositiveTTL <= 0 {
		problems = append(problems, "remote validation positive TTL must be positive")
	}
	if cfg.NegativeTTL <= 0 {
		problems = append(problems, "remote validation negative TTL must be positive")
	}
	if cfg.Timeout <= 0 {
		problems = append(problems, "remote validation timeout must be positive")
	}
	if cfg.MaxEntries <= 0 {
		problems = append(problems, "remote validation max entries must be positive")
	}
	return problems
}

func validateAgentConnections(c AgentConnectionConfig) []string {
	var problems []string
	if c.Min < 1 {
		problems = append(problems, "agent connections min must be at least 1")
	}
	if c.Max < c.Min || c.Max > 64 {
		if c.Max < c.Min {
			problems = append(problems, "agent connections max must be greater than or equal to min")
		}
		if c.Max > 64 {
			problems = append(problems, "agent connections max must be at most 64")
		}
	}
	if c.LowWatermark > c.HighWatermark {
		problems = append(problems, "agent connections low watermark must be less than or equal to high watermark")
	}
	if c.EvaluationInterval <= 0 {
		problems = append(problems, "agent connections evaluation interval must be positive")
	}
	if c.Cooldown <= 0 {
		problems = append(problems, "agent connections cooldown must be positive")
	}
	return problems
}

// EnsureNodeID returns the existing persisted node identity or creates one.
// The exclusive create prevents concurrent server starts from acquiring
// different identities; the generated value is opaque and contains no host data.
func EnsureNodeID(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("node identity path is required")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", err
		}
	}
	if data, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate node identity: %w", err)
	}
	id := "server-" + hex.EncodeToString(entropy[:])
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, writeErr := file.WriteString(id + "\n"); writeErr != nil {
			_ = file.Close()
			return "", writeErr
		}
		if closeErr := file.Close(); closeErr != nil {
			return "", closeErr
		}
		return id, nil
	} else if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if id = strings.TrimSpace(string(data)); id == "" {
		return "", errors.New("persisted node identity is empty")
	}
	return id, nil
}

// EnsureAgentInstanceID returns or creates a stable physical Agent instance
// identity. It is separate from node identity because a logical Agent can run
// on many physical hosts while the Server process has its own node identity.
func EnsureAgentInstanceID(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("agent instance identity path is required")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", err
		}
	}
	if data, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate agent instance identity: %w", err)
	}
	id := "agent-" + hex.EncodeToString(entropy[:])
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, writeErr := file.WriteString(id + "\n"); writeErr != nil {
			_ = file.Close()
			return "", writeErr
		}
		if closeErr := file.Close(); closeErr != nil {
			return "", closeErr
		}
		return id, nil
	} else if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if data, readErr := os.ReadFile(path); readErr != nil {
		return "", readErr
	} else {
		id := strings.TrimSpace(string(data))
		if id == "" {
			return "", errors.New("agent instance identity file is empty")
		}
		return id, nil
	}
}

// InitializeNodeID generates or reuses a node identity and writes it into the
// YAML config's node.id field using an atomic replacement.
func InitializeNodeID(configPath, nodeIDPath string) (string, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return "", errors.New("config path is required")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return "", fmt.Errorf("decode YAML config: %w", err)
	}
	root, err := yamlMappingRoot(&document)
	if err != nil {
		return "", err
	}
	node := yamlMappingValue(root, "node")
	if node != nil && node.Kind == yaml.MappingNode {
		if configured := yamlMappingValue(node, "id"); configured != nil && strings.TrimSpace(configured.Value) != "" {
			return strings.TrimSpace(configured.Value), nil
		}
	}
	id, err := EnsureNodeID(nodeIDPath)
	if err != nil {
		return "", err
	}
	if node == nil || node.Kind != yaml.MappingNode {
		node = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "node"}, node,
		)
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "id"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: id},
	)
	info, err := os.Stat(configPath)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(configPath), ".tunnelmesh-config-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return "", err
	}
	encoder := yaml.NewEncoder(tmp)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := encoder.Close(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := preserveOwnership(tmpName, info); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, configPath); err != nil {
		return "", err
	}
	return id, nil
}

func yamlMappingRoot(document *yaml.Node) (*yaml.Node, error) {
	if document == nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("YAML config root must be a mapping")
	}
	return document.Content[0], nil
}

func yamlMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
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

func validDynamicSuffix(raw string) bool {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ".")
	return raw != "" && !strings.Contains(raw, "*") && validDNSHost(raw)
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

// InferRelayEndpoint derives the address other Server nodes should use from
// the relay listen address. A specific listen IP is preserved; wildcard
// listeners are replaced with a usable address from this host.
func InferRelayEndpoint(listen string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "", fmt.Errorf("parse relay listen address: %w", err)
	}
	if port == "" {
		return "", errors.New("relay listen port is required")
	}
	if parsed := net.ParseIP(host); parsed != nil && !parsed.IsUnspecified() {
		return net.JoinHostPort(parsed.String(), port), nil
	}
	ip, err := selectRelayAdvertiseIP(localRelayAdvertiseAddresses())
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip.String(), port), nil
}

func localRelayAdvertiseAddresses() []net.Addr {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var addrs []net.Addr
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		ifaceAddrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		addrs = append(addrs, ifaceAddrs...)
	}
	return addrs
}

func selectRelayAdvertiseIP(addrs []net.Addr) (net.IP, error) {
	var ipv4, ipv6 []net.IP
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP == nil {
			continue
		}
		ip := ipNet.IP
		// Interface address lists should not contain broadcast entries, but a
		// future provider could return one. Global unicast also excludes
		// loopback, link-local, multicast, and unspecified candidates.
		if !ip.IsGlobalUnicast() {
			continue
		}
		if ip.To4() != nil {
			ipv4 = append(ipv4, ip)
		} else {
			ipv6 = append(ipv6, ip)
		}
	}
	candidates := ipv4
	if len(candidates) == 0 {
		candidates = ipv6
	}
	if len(candidates) == 0 {
		return nil, errors.New("no usable relay advertise address; configure server.relay.endpoint explicitly")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].String() < candidates[j].String() })
	return candidates[0], nil
}

// relayTLSMode makes the all-or-nothing certificate policy explicit: omitted
// material is plaintext, complete material is mTLS, and partial material is an
// error so a bad deployment cannot silently lose transport security.
func relayTLSMode(cfg RelayConfig) (useTLS bool, err error) {
	materialCount := 0
	for _, value := range []string{cfg.CA, cfg.Cert, cfg.Key} {
		if strings.TrimSpace(value) != "" {
			materialCount++
		}
	}
	if materialCount == 0 {
		return false, nil
	}
	if materialCount != 3 {
		return false, errors.New("server relay requires all CA, certificate, and key fields for mTLS or none for plaintext")
	}

	var problems []string
	if !filepath.IsAbs(strings.TrimSpace(cfg.CA)) {
		problems = append(problems, "server relay CA path must be absolute")
	}
	if !filepath.IsAbs(strings.TrimSpace(cfg.Cert)) {
		problems = append(problems, "server relay certificate path must be absolute")
	}
	if !filepath.IsAbs(strings.TrimSpace(cfg.Key)) {
		problems = append(problems, "server relay private key path must be absolute")
	}
	if strings.TrimSpace(cfg.ServerName) == "" {
		problems = append(problems, "server relay server name is required")
	}
	if len(problems) > 0 {
		return true, errors.New(strings.Join(problems, "; "))
	}
	return true, nil
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
	if strings.TrimSpace(cfg.Endpoint) == "" {
		problems = append(problems, "server relay requires a reachable endpoint address")
	}
	// Keep certificate policy in one place so config validation and future
	// callers cannot disagree about plaintext versus mTLS.
	if _, err := relayTLSMode(cfg); err != nil {
		problems = append(problems, err.Error())
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
