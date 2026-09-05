// Package config owns loading, validation, and redacted rendering of
// TunnelMesh configuration.  It is intentionally independent from command
// handlers so server, agent, and client can share one source of truth.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
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
}

type TCPBridgeConfig struct {
	Enabled  bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	Path     string `mapstructure:"path" json:"path" yaml:"path"`
	MaxBytes int64  `mapstructure:"max_bytes" json:"max_bytes" yaml:"max_bytes"`
}

type AgentConfig struct {
	ServerURL string `mapstructure:"server_url" json:"server_url" yaml:"server_url"`
	ID        string `mapstructure:"id" json:"id" yaml:"id"`
}

type ClientConfig struct {
	ServerURL string         `mapstructure:"server_url" json:"server_url" yaml:"server_url"`
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
		"mode":                        ModeLocal,
		"storage.driver":              StorageSQLite,
		"storage.sqlite.path":         "tunnelmesh.db",
		"storage.auto_init":           true,
		"storage.mysql.tls":           false,
		"registry.type":               RegistryDatabase,
		"registry.endpoints":          []string{},
		"server.http_addr":            ":80",
		"server.https_addr":           ":443",
		"server.agent_ws_addr":        ":443",
		"server.client_ws_addr":       ":443",
		"server.tcp_bridge.enabled":   true,
		"server.tcp_bridge.path":      "/ws/tcp",
		"server.tcp_bridge.max_bytes": int64(64 << 10),
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
		"agent.server_url", "agent.id", "client.server_url",
	}
	for _, key := range keys {
		_ = v.BindEnv(key)
	}
}

func Validate(cfg Config) error {
	var problems []string
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

// RedactedJSON returns a stable, machine-readable representation safe for
// console output.  DSNs and private key paths are intentionally omitted.
func (c Config) RedactedJSON() ([]byte, error) {
	copy := c
	copy.Storage.MySQL.DSN = redact(copy.Storage.MySQL.DSN)
	copy.Storage.MySQL.Key = redact(copy.Storage.MySQL.Key)
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
