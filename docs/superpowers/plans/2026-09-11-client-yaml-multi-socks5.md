# Client YAML 多 SOCKS5 入口实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `tunnelmesh-client` 通过一个进程和一个 WebSocket 会话，从 `client.yaml` 启动多个本地 SOCKS5 入口。

**Architecture:** 扩展 `client.tunnels` 配置模型，新增 `protocol: socks5` 及其认证、监听和远程校验字段；新增 client `run` 命令，在一个进程内按配置创建多个 forward，并共享同一个 WebSocket session opener。保留现有 `forward socks5` 命令，不破坏兼容性。

**Tech Stack:** Go、Cobra、Viper、TunnelMesh Client、Vitest 不涉及。

**Spec:** `docs/user-guide/client.md`、`docs/operations/config-examples.md`、`internal/cli/root.go`、`internal/config/config.go`。

## Global Constraints

- 不改变现有 `forward socks5` 命令行为。
- 不改变 Agent/Server 协议。
- `client.tunnels` 中同一时刻可以配置多个 `socks5` 条目。
- 所有 SOCKS5 入口必须共享同一个 Client WebSocket 会话。
- 配置校验必须拒绝重复监听地址、空 Agent ID、非法认证模式和非 loopback 监听未显式允许的情况。
- 生产默认只监听 loopback。

---

### Task 1: 扩展配置模型和校验

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- `TunnelConfig` 新增字段：
  - `Protocol string`
  - `ListenAddr string`
  - `AgentID string`
  - `AuthMode string`
  - `AllowRemote bool`
  - `AuthURL string`
- 保留现有 `TargetHost`、`TargetPort` 字段用于 `tcp/udp/http`。

- [ ] **步骤 1：写失败测试**

新增测试：

```go
func TestLoadClientYAMLWithMultipleSOCKS5Tunnels(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "client.yaml")
    yaml := `mode: local
client:
  server_url: wss://example.com/ws/client
  token: test-token
  tunnels:
    - name: socks-a
      protocol: socks5
      listen: 127.0.0.1:10866
      agent_id: agent-a
    - name: socks-b
      protocol: socks5
      listen: 127.0.0.1:10867
      agent_id: agent-b
`
    if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil { t.Fatal(err) }
    cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path})
    if err != nil { t.Fatal(err) }
    if len(cfg.Client.Tunnels) != 2 { t.Fatalf("tunnels = %d, want 2", len(cfg.Client.Tunnels)) }
    if cfg.Client.Tunnels[0].Protocol != "socks5" || cfg.Client.Tunnels[0].ListenAddr != "127.0.0.1:10866" { t.Fatalf("first tunnel = %+v", cfg.Client.Tunnels[0]) }
    if cfg.Client.Tunnels[1].Protocol != "socks5" || cfg.Client.Tunnels[1].ListenAddr != "127.0.0.1:10867" { t.Fatalf("second tunnel = %+v", cfg.Client.Tunnels[1]) }
}
```

- [ ] **步骤 2：运行测试确认失败**

```bash
go test ./internal/config -run TestLoadClientYAMLWithMultipleSOCKS5Tunnels -count=1
```

预期失败：`Protocol` 或 `ListenAddr` 字段不存在，或 YAML 解析失败。

- [ ] **步骤 3：实现最小配置模型**

在 `TunnelConfig` 中新增上述字段，并在 `Validate` 中加入：

- `protocol` 必须是 `tcp`、`udp`、`http`、`socks5` 之一。
- `socks5` 必须提供 `listen` 和 `agent_id`。
- 非默认认证只能是 `none` 或 `password`。
- 非本机监听必须显式 `allow_remote: true`。
- 监听地址不能重复。

- [ ] **步骤 4：运行测试确认通过**

```bash
go test ./internal/config -run TestLoadClientYAMLWithMultipleSOCKS5Tunnels -count=1
```

---

### Task 2: 新增 client run 命令

**Files:**
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/client_runtime_test.go`

**Interfaces:**
- 新增命令：`tunnelmesh-client run`
- 输入：`--config client.yaml`
- 行为：读取 `client.tunnels`，在一个 WebSocket 会话内启动所有入口。

- [ ] **步骤 1：写失败测试**

新增测试：

```go
func TestClientRunStartsMultipleSOCKS5Tunnels(t *testing.T) {
    // 使用 fake WebSocket session 和 fake listener
    // 断言一个进程内同时监听 10866 和 10867
    // 断言两个入口使用同一个 session opener
}
```

- [ ] **步骤 2：运行测试确认失败**

```bash
go test ./internal/cli -run TestClientRunStartsMultipleSOCKS5Tunnels -count=1
```

预期失败：`run` 命令不存在。

- [ ] **步骤 3：实现最小 run 命令**

实现逻辑：

1. 加载配置。
2. 建立 Client WebSocket。
3. 遍历 `cfg.Client.Tunnels`。
4. 对 `socks5` 创建 `client.NewSOCKS5Forward`。
5. 所有 forward 共享同一个 `client.NewSessionOpener(session)`。
6. 启动全部 forward 后阻塞等待，任一 forward 退出则关闭其他 forward 并返回错误。

- [ ] **步骤 4：运行测试确认通过**

```bash
go test ./internal/cli -run TestClientRunStartsMultipleSOCKS5Tunnels -count=1
```

---

### Task 3: 更新文档和本机服务

**Files:**
- Modify: `docs/user-guide/client.md`
- Modify: `docs/operations/config-examples.md`
- Modify: `/Users/z-yuchangjun/.config/tunnelmesh/client.yaml`
- Modify: `/Users/z-yuchangjun/Library/LaunchAgents/com.tunnelmesh.client.plist`

**Interfaces:**
- YAML 示例展示两个 SOCKS5 入口。
- LaunchAgent 只启动一个 `tunnelmesh-client run --config ...` 进程。

- [ ] **步骤 1：更新文档**

在文档中加入：

```yaml
client:
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

- [ ] **步骤 2：更新本机配置**

把本机 `client.yaml` 改为两个 SOCKS5 tunnel。

- [ ] **步骤 3：更新 LaunchAgent**

`ProgramArguments` 改为：

```text
/Users/z-yuchangjun/.local/bin/tunnelmesh-client
--config /Users/z-yuchangjun/.config/tunnelmesh/client.yaml
run
```

删除临时的 `tunnelmesh-client-socks5-multi` 脚本。

- [ ] **步骤 4：重启并验证**

```bash
launchctl kickstart -k gui/$(id -u)/com.tunnelmesh.client
lsof -nP -iTCP:10866 -sTCP:LISTEN
lsof -nP -iTCP:10867 -sTCP:LISTEN
ps aux | rg '[t]unnelmesh-client'
```

预期：

- 10866、10867 都在监听。
- 只有一个 `tunnelmesh-client` 主进程。

---

## 验证命令

```bash
go test ./internal/config -run TestLoadClientYAMLWithMultipleSOCKS5Tunnels -count=1
go test ./internal/cli -run TestClientRunStartsMultipleSOCKS5Tunnels -count=1
go test ./... -count=1
go vet ./...
git diff --check
```

## 回滚

- 代码回滚恢复 `internal/config/config.go`、`internal/cli/root.go` 和测试。
- 本机回滚恢复 `client.yaml` 和 `com.tunnelmesh.client.plist`。
- 不涉及数据库或协议迁移。
