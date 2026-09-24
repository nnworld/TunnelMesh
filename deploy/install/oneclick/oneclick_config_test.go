package oneclick

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// renderYAML 用测试驱动脚本渲染角色配置，返回文件路径。
// 这是防止「脚本渲染的键名与 internal/config 漂移」的权威手段：
// 渲染结果必须能被真实的 config.Load 解析，且字段值与输入一致。
func renderYAML(t *testing.T, role string, args ...string) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	env := append(os.Environ(),
		"TM_ONECLICK_LIB=tunnelmesh-install-common.sh",
		"TM_ONECLICK_ALLOW_STDIN=1",
		// token 只能来自环境变量；渲染 YAML 时不需要，但 config.Load 的
		// agent/client 校验会用到，因此显式注入测试值。
		"TUNNELMESH_AGENT_TOKEN=test-agent-token",
		"TUNNELMESH_CLIENT_TOKEN=test-client-token",
	)
	// 渲染脚本靠 tm_ans_set 预置全部答案，本不该提问；走 runBash 同样是为了给意外的
	// 交互提示一个有上限、可定位的失败，而不是挂死到包级超时。
	out, errBuf, err := runBash(t, bash, env, nil, bashTimeout,
		filepath.Join("testdata", "render_yaml.sh"), append([]string{role}, args...)...)
	if err != nil {
		t.Fatalf("render %s yaml: %v\nstderr:\n%s", role, err, errBuf)
	}
	path := filepath.Join(t.TempDir(), role+".yaml")
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return path
}

func loadConfig(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
	if err != nil {
		t.Fatalf("config.Load(%s): %v", path, err)
	}
	return cfg
}

func TestServerLocalSQLiteConfigRenders(t *testing.T) {
	path := renderYAML(t, "server",
		"mode=local", "http_addr=127.0.0.1:8080", "dynamic_suffix=apps.example.com",
		"storage_driver=sqlite", "sqlite_path=/var/tmp/tm/tunnelmesh.db", "auto_init=yes",
		"registry_type=database", "webssh_enabled=yes", "proxy_entry_enabled=no", "tls_enabled=no")
	cfg := loadConfig(t, path)
	if cfg.Mode != "local" {
		t.Errorf("mode = %q, want local", cfg.Mode)
	}
	if cfg.Storage.Driver != "sqlite" || !cfg.Storage.AutoInit {
		t.Errorf("storage = %+v, want sqlite + auto_init", cfg.Storage)
	}
	if cfg.Storage.SQLite.Path != "/var/tmp/tm/tunnelmesh.db" {
		t.Errorf("sqlite path = %q", cfg.Storage.SQLite.Path)
	}
	if cfg.Server.HTTPAddr != "127.0.0.1:8080" || cfg.Server.DynamicSuffix != "apps.example.com" {
		t.Errorf("server = %+v", cfg.Server)
	}
	if !cfg.Server.WebSSH.Enabled || cfg.Server.ProxyEntry.Enabled || cfg.TLS.Enabled {
		t.Errorf("feature flags wrong: webssh=%v proxy_entry=%v tls=%v",
			cfg.Server.WebSSH.Enabled, cfg.Server.ProxyEntry.Enabled, cfg.TLS.Enabled)
	}
	if cfg.Registry.Type != "database" {
		t.Errorf("registry = %q", cfg.Registry.Type)
	}
}

func TestServerClusterMySQLRelayConfigRenders(t *testing.T) {
	// DSN 与 relay node token 是敏感值，安装时只写进 env 文件、由 systemd/launchd 注入进程环境，
	// YAML 里不出现。这里用 t.Setenv 复现「服务进程已加载 env 文件」的状态，
	// 才能校验 config.Load 对 cluster + relay 的完整验证链。
	t.Setenv("TUNNELMESH_STORAGE_MYSQL_DSN", "user:pw@tcp(db:3306)/tunnelmesh?parseTime=true")
	t.Setenv("TUNNELMESH_SERVER_RELAY_NODE_TOKEN", "node-token")
	path := renderYAML(t, "server",
		"mode=cluster", "http_addr=127.0.0.1:8080", "storage_driver=mysql", "auto_init=yes", "node_id=server-1",
		"mysql_tls=no", "registry_type=database", "relay_enabled=yes",
		"relay_listen=0.0.0.0:9443", "relay_endpoint=server-1.internal.example.com:9443",
		"allowed_hosts=tunnel.example.com", "allowed_origins=https://tunnel.example.com",
		"env=TUNNELMESH_STORAGE_MYSQL_DSN=user:pw@tcp(db:3306)/tunnelmesh?parseTime=true",
		"env=TUNNELMESH_SERVER_RELAY_NODE_TOKEN=node-token")
	cfg := loadConfig(t, path)
	if cfg.Mode != "cluster" || cfg.Storage.Driver != "mysql" {
		t.Errorf("mode/storage = %q/%q", cfg.Mode, cfg.Storage.Driver)
	}
	if !cfg.Server.Relay.Enabled || cfg.Server.Relay.Listen != "0.0.0.0:9443" {
		t.Errorf("relay = %+v", cfg.Server.Relay)
	}
	if cfg.Server.Relay.Endpoint != "server-1.internal.example.com:9443" {
		t.Errorf("relay endpoint = %q", cfg.Server.Relay.Endpoint)
	}
	if len(cfg.Security.AllowedHosts) != 1 || cfg.Security.AllowedHosts[0] != "tunnel.example.com" {
		t.Errorf("allowed_hosts = %v", cfg.Security.AllowedHosts)
	}
	if len(cfg.Security.AllowedOrigins) != 1 {
		t.Errorf("allowed_origins = %v", cfg.Security.AllowedOrigins)
	}
}

func TestAgentConfigRenders(t *testing.T) {
	path := renderYAML(t, "agent",
		"server_url=wss://tunnel.example.com/ws/agent", "agent_id=agent-devbox",
		"conn_min=1", "conn_max=2", "metadata_name=device_id", "metadata_source=file", "metadata_path=/etc/machine-id")
	cfg := loadConfig(t, path)
	if cfg.Agent.ServerURL != "wss://tunnel.example.com/ws/agent" || cfg.Agent.ID != "agent-devbox" {
		t.Errorf("agent = %+v", cfg.Agent)
	}
	if cfg.Agent.Connections.Min != 1 || cfg.Agent.Connections.Max != 2 {
		t.Errorf("connections = %+v", cfg.Agent.Connections)
	}
	if len(cfg.Agent.Metadata) != 1 || cfg.Agent.Metadata[0].Name != "device_id" || cfg.Agent.Metadata[0].Source != "file" {
		t.Errorf("metadata = %+v", cfg.Agent.Metadata)
	}
}

func TestClientConfigRendersAllProtocols(t *testing.T) {
	path := renderYAML(t, "client",
		"server_url=wss://tunnel.example.com/ws/client",
		"tunnel=pg:tcp:127.0.0.1:15432:agent-db:db.internal:5432",
		"tunnel=dns:udp:127.0.0.1:15353:agent-net:10.0.0.53:53",
		"tunnel=web:http:127.0.0.1:18080:agent-web:127.0.0.1:8080",
		"tunnel=socks:socks5:127.0.0.1:10866:agent-a::")
	cfg := loadConfig(t, path)
	if cfg.Client.ServerURL != "wss://tunnel.example.com/ws/client" {
		t.Errorf("client server_url = %q", cfg.Client.ServerURL)
	}
	want := []struct {
		name, protocol, listen, agent, host string
		port                                int
	}{
		{"pg", "tcp", "127.0.0.1:15432", "agent-db", "db.internal", 5432},
		{"dns", "udp", "127.0.0.1:15353", "agent-net", "10.0.0.53", 53},
		{"web", "http", "127.0.0.1:18080", "agent-web", "127.0.0.1", 8080},
		{"socks", "socks5", "127.0.0.1:10866", "agent-a", "", 0},
	}
	if len(cfg.Client.Tunnels) != len(want) {
		t.Fatalf("tunnels = %d, want %d (%+v)", len(cfg.Client.Tunnels), len(want), cfg.Client.Tunnels)
	}
	for i, w := range want {
		got := cfg.Client.Tunnels[i]
		if got.Name != w.name || got.Protocol != w.protocol || got.ListenAddr != w.listen ||
			got.AgentID != w.agent || got.TargetHost != w.host || got.TargetPort != w.port {
			t.Errorf("tunnel[%d] = %+v, want %+v", i, got, w)
		}
	}
}

func TestRenderedConfigNeverContainsSecrets(t *testing.T) {
	for _, role := range []string{"server", "agent", "client"} {
		args := []string{"server_url=wss://tunnel.example.com/ws/" + role}
		if role == "server" {
			args = append(args, "mode=local", "storage_driver=sqlite", "sqlite_path=/var/tmp/tm/x.db",
				"env=TUNNELMESH_STORAGE_MYSQL_DSN=user:pw@tcp(db:3306)/tm")
		}
		path := renderYAML(t, role, args...)
		body := readFile(t, path)
		// 断言的是「秘密值本身没有落盘」，而不是环境变量名：渲染出的 YAML 会带注释
		// 说明 token 由哪个环境变量注入，那是文档，不是泄露。
		for _, bad := range []string{"pw@tcp", "token:", "password", "test-agent-token", "test-client-token", "node-token"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s yaml leaked %q:\n%s", role, bad, body)
			}
		}
	}
}
