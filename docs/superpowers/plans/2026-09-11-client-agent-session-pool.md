# Client Agent Session Pool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `tunnelmesh-client run` 按 Agent 分组维护 WebSocket 连接池，在保留 stream 多路复用的同时，实现每个 Agent 至少一条通道、同 Agent 可扩容多通道，并提供三平台服务模板和全协议 YAML 示例。

**Architecture:** 本地 forward 先启动并持有 `PooledStreamOpener`；该 opener 按 `agent_id` 选择连接池，再从池内健康 session 中按活跃 stream 和 RTT 选择一条 WebSocket 打开流。WebSocket 断线只重建连接，不重建本地 listener。默认 `min=1,max=1` 保持兼容；配置多个 Agent 时会为每个 Agent 独立建池。

**Tech Stack:** Go、Cobra、Viper、WebSocket stream 多路复用、systemd、launchd、WinSW、Markdown。

**Spec:** `docs/superpowers/specs/2026-09-11-client-agent-session-pool-design.md`

## Global Constraints

- 不使用 worktree，只在 `/opt/app/workspace/TunnelMesh` 当前 `main` 工作区修改。
- 不回滚、覆盖或整理现有未提交改动；只修改本任务相关文件。
- 不改变 Server/Agent 协议，不做数据库 Schema 变更。
- 不实现多个 `server_url`。
- 不把 Client token、本地代理用户名或密码写入文档真实值、日志或部署模板。
- 所有生产默认仍为 loopback 监听；非 loopback 必须显式 `allow_remote: true` 并启用认证。
- 每个新增行为先写失败测试，再实现。
- 未获用户明确授权，不执行 commit/push/merge。

---

### Task 1: 扩展 Client 配置模型和校验

**Files:**

- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**

- Produces:

```go
type ClientConnectionConfig struct {
    Min                int
    Max                int
    HighWatermark      int
    LowWatermark       int
    EvaluationInterval time.Duration
    Cooldown           time.Duration
}

type ClientConfig struct {
    ServerURL        string
    Token            string
    Tunnels          []TunnelConfig
    Connections      ClientConnectionConfig
    Stream           ClientStreamConfig
    RemoteValidation RemoteValidationConfig
}
```

- YAML 字段为 `client.connections.min/max/high_watermark/low_watermark/evaluation_interval/cooldown`。
- `TunnelConfig.Protocol` 合法值扩展为 `tcp`、`udp`、`http`、`socks5`、`http-proxy`。

- [ ] **Step 1: 写失败测试**

在 `internal/config/config_test.go` 增加：

```go
func TestLoadClientConnectionPoolDefaultsAndValidation(t *testing.T) {
    cfg, err := config.Load(context.Background(), config.ConfigOptions{})
    if err != nil {
        t.Fatal(err)
    }
    want := config.ClientConnectionConfig{
        Min: 1, Max: 1, HighWatermark: 16, LowWatermark: 2,
        EvaluationInterval: 10 * time.Second, Cooldown: 30 * time.Second,
    }
    if cfg.Client.Connections != want {
        t.Fatalf("Client.Connections = %+v, want %+v", cfg.Client.Connections, want)
    }
}
```

并增加表驱动测试覆盖：

```go
cases := []struct{
    name string
    edit func(*config.ClientConnectionConfig)
    want string
}{
    {"min below one", func(c *config.ClientConnectionConfig) { c.Min = 0 }, "client connections min must be at least 1"},
    {"max below min", func(c *config.ClientConnectionConfig) { c.Min, c.Max = 2, 1 }, "client connections max must be greater than or equal to min"},
    {"max above limit", func(c *config.ClientConnectionConfig) { c.Max = 17 }, "client connections max must be at most 16"},
    {"low above high", func(c *config.ClientConnectionConfig) { c.LowWatermark = 17 }, "client connections low watermark must be less than or equal to high watermark"},
    {"invalid interval", func(c *config.ClientConnectionConfig) { c.EvaluationInterval = 0 }, "client connections evaluation interval must be positive"},
    {"invalid cooldown", func(c *config.ClientConnectionConfig) { c.Cooldown = 0 }, "client connections cooldown must be positive"},
}
```

另增加 `TestLoadAndValidateHTTPProxyTunnel`，配置：

```yaml
client:
  tunnels:
    - name: http-proxy
      protocol: http-proxy
      listen: 127.0.0.1:18081
      agent_id: agent-web
      auth_mode: none
```

并断言非法值被拒绝：

- `auth_mode: password` 拒绝，HTTP proxy 只允许 `none/basic`；
- `listen: 0.0.0.0:18081` 未设置 `allow_remote` 拒绝；
- `allow_remote: true` 且 `auth_mode: none` 拒绝。

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/config -run 'Test(LoadClientConnectionPool|LoadAndValidateHTTPProxyTunnel)' -count=1
```

预期：字段或校验不存在导致编译失败或断言失败。

- [ ] **Step 3: 最小实现**

- 在 `setDefaults` 中加入 Client 连接池默认值。
- 新增 `validateClientConnections`。
- 在 `Validate` 中调用。
- 扩展 `validateClientTunnels`：
  - `http-proxy` 必须有 `listen` 和 `agent_id`；
  - 认证只允许 `none/basic`；
  - 非 loopback 必须显式允许且启用 Basic auth。

- [ ] **Step 4: 验证通过**

```bash
go test ./internal/config -run 'Test(LoadClientConnectionPool|LoadAndValidateHTTPProxyTunnel)' -count=1
```

---

### Task 2: 实现 Client Agent Session Pool

**Files:**

- Create: `internal/client/session_pool.go`
- Test: `internal/client/session_pool_test.go`

**Interfaces:**

- Produces:

```go
type SessionPoolConfig struct {
    Min                int
    Max                int
    HighWatermark      int
    LowWatermark       int
    EvaluationInterval time.Duration
    Cooldown           time.Duration
}

type SessionPoolRunner func(ctx context.Context, serverURL, token string, onReady func(*Session) error) error

type SessionPoolManagerOptions struct {
    ServerURL string
    Token     string
    Config    SessionPoolConfig
    Runner    SessionPoolRunner
    WebSocket WebSocketRunOptions
}

func NewSessionPoolManager(options SessionPoolManagerOptions) *SessionPoolManager
func (m *SessionPoolManager) Opener(agentID string) StreamOpener
func (m *SessionPoolManager) Run(ctx context.Context) error
```

- `Opener(agentID)` 返回的 opener 同时实现：

```go
OpenStream(ctx context.Context, request StreamRequest) (io.ReadWriteCloser, error)
OpenStreamResult(ctx context.Context, request StreamRequest) (io.ReadWriteCloser, protocol.OpenResultPayload, error)
```

这样 TCP/UDP/HTTP 和 strict-open SOCKS5 都能使用同一个池。

- [ ] **Step 1: 写失败测试：每个 Agent 独立连接**

测试构造两个 fake runner transport，配置两个 Agent，调用 `Run`，断言：

```go
if manager.OpenCount("agent-a") != 1 { t.Fatal("agent-a should have one WebSocket") }
if manager.OpenCount("agent-b") != 1 { t.Fatal("agent-b should have one WebSocket") }
```

再分别向两个 Agent 打开 stream，断帧：

```go
payload, err := protocol.DecodeStreamOpenPayload(open.Payload)
if payload.AgentID != wantAgentID { t.Fatalf("agent = %q, want %q", payload.AgentID, wantAgentID) }
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/client -run TestSessionPoolCreatesOneConnectionPerAgent -count=1
```

预期：`NewSessionPoolManager` 不存在。

- [ ] **Step 3: 实现分组池和 stream 选择**

实现要点：

1. `Run` 去重收集 `agentID -> pool`。
2. 每个池初始启动 `min` 个连接槽。
3. 每个槽调用 `Runner`，`onReady` 注册 session。
4. `onReady` 启动监控 goroutine，`session.Wait(ctx)` 返回后注销 session。
5. `Opener(agentID).OpenStream` 从该 Agent 池选择活跃 stream 最少、RTT 最低的 ready session。
6. 返回包装 stream，`Close` 时释放活跃计数。
7. 无可用 session 返回 `ErrAgentSessionUnavailable`。

- [ ] **Step 4: 写失败测试：活跃计数与选择**

构造同一 Agent 的两个 session，让第一个已有 2 个活跃 stream，第二个 0 个；再打开 stream，断言选择第二个。关闭包装 stream 后断言活跃数回落。

运行：

```bash
go test ./internal/client -run TestSessionPoolSelectsLeastActiveSession -count=1
```

- [ ] **Step 5: 实现活跃计数**

在 pool session 记录：

```go
type pooledSession struct {
    id       string
    session  *Session
    active   atomic.Int64
    lastRTT  atomic.Int64
}
```

- [ ] **Step 6: 写失败测试：扩容和缩容**

配置 `min=1,max=2,high_watermark=1,low_watermark=0,evaluation_interval=10ms,cooldown=10ms`：

1. 打开 1 个 stream 后等待第二个 runner 被调用；
2. 关闭 stream 后等待第二个连接槽被取消；
3. 断言最终连接数回到 `min`。

运行：

```bash
go test ./internal/client -run TestSessionPoolScalesUpAndDown -count=1
```

- [ ] **Step 7: 实现扩缩容控制器**

- 每 `evaluation_interval` 评估一次。
- 所有已有连接的活跃 stream 均达到 `high_watermark` 且当前连接数小于 `max` 时扩容一个槽。
- 当前连接数大于 `min` 且所有连接活跃数小于等于 `low_watermark` 时缩容最新槽。
- 同一方向决策受 `cooldown` 限制。
- 扩缩容只影响连接槽，不影响本地 listener。

- [ ] **Step 8: 写失败测试：重连不丢失本地 opener**

让 fake runner 第一次 session 关闭后重新调用 `onReady`，再打开 stream；断言新 stream 成功且本地 opener 仍可用。

运行：

```bash
go test ./internal/client -run TestSessionPoolReconnectsWithoutReplacingOpener -count=1
```

- [ ] **Step 9: 验证包内测试**

```bash
go test ./internal/client -run 'TestSessionPool' -count=1
go test -race ./internal/client -run 'TestSessionPool' -count=1
```

---

### Task 3: 改造 CLI run 并支持 HTTP proxy YAML

**Files:**

- Modify: `internal/cli/client_run.go`
- Modify: `internal/cli/root.go`
- Test: `internal/cli/client_runtime_test.go`

**Interfaces:**

- Consumes: `client.NewSessionPoolManager`、`manager.Opener(agentID)`、`manager.Run(ctx)`。
- Produces:
  - `run` 使用连接池；
  - `client.tunnels.protocol=http-proxy` 可启动 `client.NewHTTPProxyForward`。

- [ ] **Step 1: 写失败测试：两个 Agent 使用两条 WebSocket**

测试配置：

```yaml
client:
  server_url: ws://server.example/ws/client
  token: client-secret
  tunnels:
    - name: socks-a
      protocol: socks5
      listen: 127.0.0.1:10866
      agent_id: agent-a
    - name: socks-b
      protocol: socks5
      listen: 127.0.0.1:10867
      agent_id: agent-b
```

替换可注入 runner，记录每次调用的 transport；执行 `run` 后分别向 10866、10867 发 SOCKS5 CONNECT。断言：

- 两个 OPEN payload 的 Agent ID 分别正确；
- 两个 OPEN frame 来自两个不同 transport；
- 本地进程只有一个 `run` 命令入口。

运行：

```bash
go test ./internal/cli -run TestClientRunUsesOneWebSocketPerAgent -count=1
```

- [ ] **Step 2: 写失败测试：HTTP proxy YAML**

配置 `protocol: http-proxy`，启动后通过本地代理发送：

```http
CONNECT 127.0.0.1:3000 HTTP/1.1
Host: 127.0.0.1:3000
```

断言收到 `200 Connection established`，并从 transport 读取 OPEN payload：

```go
payload.Protocol == "tcp"
payload.TargetHost == "127.0.0.1"
payload.TargetPort == 3000
```

运行：

```bash
go test ./internal/cli -run TestClientRunStartsHTTPProxyTunnel -count=1
```

- [ ] **Step 3: 改造 run 生命周期**

实现顺序：

1. 校验 `client.server_url`、`client.token` 和 `client.tunnels`。
2. 创建 `client.SessionPoolManager`。
3. 对每个 tunnel 调用 `newConfiguredClientForward(manager.Opener(tunnel.AgentID), ...)`。
4. 启动全部本地 listener。
5. 调用 `manager.Run(cmd.Context())` 阻塞。
6. `defer` 反向关闭本地 forward。

禁止再把 listener 创建放在 WebSocket `onReady` 回调中。

- [ ] **Step 4: 扩展 forward 工厂**

在 `newConfiguredClientForward` 中新增 `http-proxy` 分支，构造 `client.NewHTTPProxyForward`。Basic auth 继续读取：

```text
TUNNELMESH_HTTP_PROXY_USERNAME
TUNNELMESH_HTTP_PROXY_PASSWORD
```

- [ ] **Step 5: 验证 CLI**

```bash
go test ./internal/cli -run 'TestClientRun(UsesOneWebSocketPerAgent|StartsHTTPProxyTunnel)' -count=1
go test -race ./internal/cli -run 'TestClientRun' -count=1
```

---

### Task 4: 增加三平台 Client 服务模板和安装支持

**Files:**

- Create: `deploy/systemd/tunnelmesh-client.service`
- Create: `deploy/macos/tunnelmesh-client.plist`
- Create: `deploy/windows/tunnelmesh-client-service.xml`
- Modify: `deploy/install/linux-install.sh`
- Modify: `deploy/install/macos-install.sh`
- Modify: `deploy/install/windows-install.ps1`
- Test: `deploy/install/install_templates_test.go`

**Interfaces:**

- Linux role: `client`
- macOS role: `client`
- Windows role: `client`
- Client 配置路径：
  - Linux：`/etc/tunnelmesh/client.yaml`
  - macOS：`$HOME/.config/tunnelmesh/client.yaml`
  - Windows：`C:\ProgramData\TunnelMesh\client.yaml`

- [ ] **Step 1: 写失败测试**

新增 Go 测试：

```go
func TestClientServiceTemplatesExistAndContainRunCommand(t *testing.T) {
    templates := []string{
        "../systemd/tunnelmesh-client.service",
        "../macos/tunnelmesh-client.plist",
        "../windows/tunnelmesh-client-service.xml",
    }
    for _, name := range templates {
        data, err := os.ReadFile(name)
        if err != nil { t.Fatal(err) }
        if !bytes.Contains(data, []byte("run")) { t.Fatalf("%s does not run client", name) }
    }
}
```

同时测试三个安装脚本都接受 `client` role。

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./deploy/install -run TestClientServiceTemplates -count=1
```

预期：模板不存在。

- [ ] **Step 3: 添加 Linux systemd**

要求：

- `User=tunnelmesh`
- `EnvironmentFile=/etc/tunnelmesh/client.env`
- `ExecStartPre=/usr/local/bin/tunnelmesh-client --config /etc/tunnelmesh/client.yaml check-config`
- `ExecStart=/usr/local/bin/tunnelmesh-client --config /etc/tunnelmesh/client.yaml run`
- `Restart=on-failure`
- `ProtectSystem=strict`
- `ReadWritePaths=/var/lib/tunnelmesh-client`

- [ ] **Step 4: 添加 macOS LaunchAgent**

模板包含：

```xml
<string>__HOME__/.local/bin/tunnelmesh-client</string>
<string>--config</string>
<string>__HOME__/.config/tunnelmesh/client.yaml</string>
<string>run</string>
```

安装脚本将 `__HOME__` 替换为实际用户 home。

- [ ] **Step 5: 添加 Windows WinSW XML**

模板包含：

```xml
<executable>tunnelmesh-client.exe</executable>
<arguments>--config "C:\ProgramData\TunnelMesh\client.yaml" run</arguments>
```

安装脚本按 role 复制并生成对应 XML。

- [ ] **Step 6: 更新安装脚本**

- Linux `--role client` 安装 binary 和 unit。
- macOS `client` role 安装 LaunchAgent。
- Windows `-Role client` 安装 WinSW service。

- [ ] **Step 7: 验证模板**

```bash
systemd-analyze verify deploy/systemd/tunnelmesh-client.service
plutil -lint deploy/macos/tunnelmesh-client.plist
go test ./deploy/install -count=1
```

如当前环境缺少 `plutil` 或 `systemd-analyze`，记录环境限制并用 Go XML/plist 结构测试替代。

---

### Task 5: 补全 YAML 和跨平台部署文档

**Files:**

- Create: `docs/operations/client-configuration-examples.md`
- Modify: `docs/operations/config-examples.md`
- Modify: `docs/user-guide/client.md`
- Modify: `docs/deployment/linux-systemd.md`
- Modify: `docs/deployment/windows-service.md`
- Create: `docs/deployment/macos-launchagent.md`
- Modify: `docs/README.md`

**Interfaces:**

- 一个完整 YAML 示例覆盖连接池和全部协议。
- 三个平台各一份服务安装说明。

- [ ] **Step 1: 新增全协议示例**

示例必须包含 TCP、UDP、HTTP、SOCKS5、远程 SOCKS5、HTTP proxy、连接池、远程校验和两个 Agent 的独立池配置。

核心结构：

```yaml
client:
  connections:
    min: 1
    max: 4
    high_watermark: 16
    low_watermark: 2
    evaluation_interval: 10s
    cooldown: 30s
  tunnels:
    - name: postgres
      protocol: tcp
      listen: 127.0.0.1:15432
      agent_id: agent-db
      target_host: db.internal
      target_port: 5432
    - name: dns
      protocol: udp
      listen: 127.0.0.1:15353
      agent_id: agent-net
      target_host: 10.0.0.53
      target_port: 53
    - name: internal-web
      protocol: http
      listen: 127.0.0.1:18080
      agent_id: agent-web
      target_host: web.internal
      target_port: 8080
    - name: socks-loopback
      protocol: socks5
      listen: 127.0.0.1:10866
      agent_id: agent-devbox
      auth_mode: none
    - name: http-proxy
      protocol: http-proxy
      listen: 127.0.0.1:18081
      agent_id: agent-devbox
      auth_mode: none
```

另提供远程 SOCKS5、远程 HTTP proxy 和 `client.remote_validation` 示例。

- [ ] **Step 2: 更新部署文档**

- Linux：安装、启用、查看日志、回滚。
- macOS：LaunchAgent 安装、重载、日志。
- Windows：WinSW 安装、服务状态、日志、卸载。

- [ ] **Step 3: 文档链接**

在 `docs/README.md` 和相关页面加入 Client 连接池配置示例、macOS LaunchAgent、Linux Client systemd、Windows Client service 的链接。

---

### Task 6: 全量验证和本机升级

**Files:**

- No new source files.

**Interfaces:**

- 所有测试和静态检查通过。
- 本机 macOS client 切换到新连接池版本。

- [ ] **Step 1: 全量验证**

```bash
go test ./... -count=1
go test -race ./internal/client ./internal/cli
go vet ./...
git diff --check
```

- [ ] **Step 2: 构建本机二进制**

```bash
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  go build -trimpath -ldflags='-s -w' \
  -o /tmp/tunnelmesh-client-pool ./cmd/tunnelmesh-client
```

- [ ] **Step 3: 更新本机配置**

在 `/Users/z-yuchangjun/.config/tunnelmesh/client.yaml` 增加：

```yaml
client:
  connections:
    min: 1
    max: 4
    high_watermark: 16
    low_watermark: 2
    evaluation_interval: 10s
    cooldown: 30s
```

保留现有 10866 和 10867 tunnel。

- [ ] **Step 4: 重启并验证**

```bash
launchctl bootout gui/$(id -u)/com.tunnelmesh.client || true
install -m 0755 /tmp/tunnelmesh-client-pool ~/.local/bin/tunnelmesh-client
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.tunnelmesh.client.plist
lsof -nP -iTCP:10866 -sTCP:LISTEN
lsof -nP -iTCP:10867 -sTCP:LISTEN
ps -axo pid,command | grep '[t]unnelmesh-client'
```

预期：

- 两个端口由同一个进程监听；
- 进程内有两个 Agent 分组；
- Server `/metrics` 中 `mode="client"` 活跃连接数至少为 2；
- 10866 和 10867 的 SOCKS5 请求分别路由到配置的 Agent。

## 自检清单

- [ ] 每个 Agent 至少一条 WebSocket。
- [ ] 同一 Agent 支持多条 WebSocket 扩容。
- [ ] 单条 WebSocket 内继续 stream 多路复用。
- [ ] WS 重连不重建本地 listener。
- [ ] TCP/UDP/HTTP/SOCKS5/HTTP-proxy 均有 YAML 示例。
- [ ] Linux/macOS/Windows 服务模板齐全。
- [ ] 默认配置兼容单 Agent 单连接。
- [ ] 敏感信息不进入文档、日志和模板。
