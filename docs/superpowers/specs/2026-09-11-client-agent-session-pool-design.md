# Client Agent Session Pool Design

## 背景

当前 `tunnelmesh-client run` 使用一条 Client WebSocket 会话承载所有本地入口。虽然协议层已经支持 stream 多路复用，但所有 Agent 共享一条物理连接，无法按 Agent 隔离网络故障，也无法通过多条连接分摊高并发流量。

## 目标

- 保留 stream 多路复用：每条 WebSocket 内仍可承载多个并发 stream。
- 按 `agent_id` 分组维护 Client WebSocket 连接池。
- 每个出现的 Agent 至少维护一条 WebSocket；多个本地端口指向同一 Agent 时共享该 Agent 的连接池。
- 同一 Agent 可配置多条 WebSocket，新建 stream 按连接健康度和活跃 stream 数选择。
- 本地 listener 与 WebSocket 生命周期解耦：WS 重连不重建本地端口，也不触发端口绑定冲突。
- `client.tunnels` 支持 TCP、UDP、HTTP、SOCKS5 和标准 HTTP proxy。
- 提供 Linux systemd、macOS LaunchAgent、Windows WinSW 服务模板。
- 提供覆盖全部协议、认证、远程监听、远程校验和连接池的 YAML 示例。

## 非目标

- 不实现多个 `server_url`。
- 不做 stream 在线迁移；stream 固定在打开它的 WebSocket 上。
- 不拆分单个 stream 到多条连接。
- 不改变 Server、Agent 协议和数据库 Schema。
- 不把本地代理认证密码写入 YAML；密码继续通过环境变量或 Secret Manager 注入。

## 配置模型

连接池配置作用于每个 Agent 分组：

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

- `min`：每个 Agent 至少保持的 WebSocket 数，默认 1。
- `max`：每个 Agent 最多允许的 WebSocket 数，默认 1。
- `high_watermark`：现有连接活跃 stream 达到该值时允许扩容。
- `low_watermark`：超出 `min` 的连接持续低于该值时允许缩容。
- `evaluation_interval`：连接池评估周期。
- `cooldown`：两次扩容或缩容之间的最小间隔。

默认 `min=1,max=1` 保持兼容。若配置中有两个 Agent，则至少建立两条 WebSocket，每个 Agent 一条。

## 连接池架构

新增 Client Session Pool Manager：

```text
CLI run
  ├── TCP/UDP/HTTP/SOCKS5/HTTP-proxy forwarders
  │     └── PooledStreamOpener
  │           ├── agent-a pool
  │           │     ├── ws-a1
  │           │     └── ws-a2
  │           └── agent-b pool
  │                 └── ws-b1
```

职责边界：

- Forward 只负责本地协议入口和目标请求构造。
- `PooledStreamOpener` 按 `StreamRequest.AgentID` 选择 Agent 分组，再从该分组选择一条健康 session 打开 stream。
- Session pool 负责 WebSocket 建立、重连、扩缩容、健康剔除和活跃 stream 计数。
- stream 打开后固定在所选 session 上；关闭 stream 时释放计数。

选择策略：

1. 过滤未 ready、已关闭的 session。
2. 优先选择活跃 stream 最少的 session。
3. 活跃数相同时选择最近心跳 RTT 较低者。
4. 没有可用 session 时返回明确的 `agent unavailable` 错误。

## 生命周期

1. `run` 加载并校验配置。
2. 创建 Session Pool Manager。
3. 为所有本地 tunnel 创建 forward，并使用 `manager.Opener(agentID)` 作为 stream opener。
4. 启动本地 listener。
5. Manager 为去重后的每个 Agent 启动至少 `min` 条 WebSocket。
6. WebSocket 断开后在原连接槽内自动重连；本地 listener 不关闭。
7. 达到高水位且未超过 `max` 时扩容；持续低于低水位且高于 `min` 时缩容。
8. 进程收到退出信号时先关闭本地 listener，再关闭全部 WebSocket。

## 协议支持

`client.tunnels.protocol` 支持：

- `tcp`
- `udp`
- `http`
- `socks5`
- `http-proxy`

SOCKS5 和 HTTP proxy 继续支持：

- `auth_mode`
- `allow_remote`
- `auth_url`
- 远程校验缓存配置

HTTP proxy 的认证模式为 `none` 或 `basic`；SOCKS5 为 `none` 或 `password`。

## 部署

新增：

- `deploy/systemd/tunnelmesh-client.service`
- `deploy/macos/tunnelmesh-client.plist`
- `deploy/windows/tunnelmesh-client-service.xml`

安装脚本支持 `role=client`：

- Linux：systemd unit。
- macOS：用户 LaunchAgent。
- Windows：WinSW service XML。

## 安全

- 所有 WebSocket 独立使用同一个 Client service token 认证。
- Token 和本地代理密码不写入日志。
- 每个 stream 仍由 Server 重新校验 token scope、Agent policy、协议、CIDR 和端口。
- 非 loopback 监听必须显式 `allow_remote: true`，且启用对应密码/Basic 认证。

## 测试

- 配置解析与校验：连接池范围、协议字段、远程监听和认证。
- 连接池：多 Agent 分组、最少连接、stream 选择、活跃计数释放、扩容、缩容和重连。
- CLI run：两个 Agent 使用两条独立 WebSocket，本地端口不因重连重建。
- HTTP proxy YAML 启动路径。
- 部署模板：systemd verify、plist lint、WinSW XML 解析。
- 全量 `go test ./...`、`go vet ./...`、`git diff --check`。
