# Agent 与 Server 之间增加 TCP（TLS）与 QUIC 传输

> 状态：已实施。设计决策见 [ADR 0004](../../architecture/adr/0004-agent-raw-tcp-and-quic-transports.md)，协议见 [Agent 裸传输协议](../../protocol/agent-transports.md)。

## 目标

Agent 与 Server 之前只有 WebSocket（`/ws/agent`）。在只放行裸 TCP 或 UDP 的网络中，WebSocket 依赖的 HTTP 入口不可用。本计划增加两种可选传输，默认关闭，对现有部署没有影响。

## 已确认的决策

| 项 | 决定 |
| --- | --- |
| UDP | QUIC（quic-go），整条帧流放在一个双向流上，帧格式不变 |
| TCP | 与 WebSocket 同一信任模型：`tls://` 为默认，明文 `tcp://` 需要显式允许或回环 |
| Server 监听 | 独立端口，默认关闭 |
| 范围 | 仅 Agent 与 Server 之间；Client 通道和 Client 内嵌的 Agent 行为不变 |

## 实施结果

### 阶段 1：传输无关的重构（行为不变）

- `ws_agent.go` 中的服务循环改为接受 `agentFrameConn` 接口，不再绑定 `*WSFrameTransport`。
- 从 `serveAgentWS` 提取了 `serveAgentConnection`。WebSocket 特有的超时切换改为 `afterHello` 回调。Server 现有测试在未修改的情况下全部通过。
- `reconnect.IsRejected` 同时识别新的 `RejectedError` 与原有的 WebSocket 判断。

**与原计划的偏差**：原计划的 1c（把运行器与选项类型改名为 `runServerConnection` 与 `ServerConnectionPoolOptions`）没有做。改名会造成大量无收益的改动，且 `WebSocketPoolOptions` 被 Client 内嵌的 Agent 使用。拨号分发放在 `dialAgentTransport` 中完成，运行器只调用它。

### 阶段 2：preface、配置与 TCP/TLS

- `protocol/preface.go`：preface 编解码，令牌以 `[redacted]` 形式格式化，附带模糊测试。
- `protocol/stream_conn.go`：基于字节流的帧传输，Send 时串行写入，写超时后关闭，空闲超时在每次读取前设置。TCP、TLS 与 QUIC 共用。
- 配置：`agent.server_url` 支持 `tls://`、`tcp://`、`quic://`；新增 `server.agent_transports` 与 `agent.transport.tls_ca_file`。校验规则在 `config_test` 中逐项覆盖。
- Agent：`dialAgentTransport` 按 scheme 分发；`tls` 与 `tcp` 经过 `DialContextForScheme`，代理选择与 `wss`、`ws` 一致。
- Server：`agent_stream_listener.go` 实现 TCP 与 TLS 监听，包含未认证预算、preface 超时、失败次数封锁与 TLS 握手。
- 指标标签：Agent 为 `websocket`、`tcp_tls`、`tcp`、`quic`；Server 为 `agent_tcp`、`agent_quic`。

**未完成的部分**：原计划的 2f（审计记录中的 transport 字段）未实现。连接指标已按传输区分。

### 阶段 3：QUIC

- 依赖 `github.com/quic-go/quic-go` v0.61.0（2026-07-21 发布，MIT）。
- `server/agent_quic_listener.go`：每个连接只允许一个双向流（`MaxIncomingStreams = 1`），空闲超时 60 秒，保活 20 秒，ALPN 为 `tunnelmesh-agent/1`。
- Agent：`dialQUICTransport` 使用 TLS 1.3 与相同的 ALPN，在唯一的流上发送 preface。
- 关闭流即关闭连接，避免泄漏。

**与原计划的偏差**：原计划的 QUIC 地址验证（Retry）与 QUIC 层的负载感知限流没有实现，依赖 quic-go 的默认行为与上述预算。

## 测试

- `internal/protocol`：preface 表驱动测试、模糊测试、`StreamConn` 在 `net.Pipe` 上的往返、并发发送、空闲超时、写超时、幂等关闭（均在 `-race` 下通过）。
- `internal/config`：scheme 与明文门禁的组合、quic 代理限制、Server 监听校验。
- `internal/agent`：拨号分发、preface 应答映射（拒绝与忙碌分别处理）、TLS 配置。
- `internal/server`：原始监听器的 preface 行为、慢速 preface、各类预算上限；真实 Agent 通过 `tcp://`、`tls://`、`quic://` 注册并落库；错误令牌不注册；ALPN 不匹配时握手失败；单流约束。

## 验证

```
go test ./internal/protocol ./internal/config ./internal/agent ./internal/reconnect ./internal/server -count=1 -race
GOOS=linux|darwin|windows GOARCH=amd64|arm64 CGO_ENABLED=0 go build ./cmd/...
```

三个平台的 server、agent、client 均在 `CGO_ENABLED=0` 下编译成功。

## 未完成与后续

- 审计 transport 字段（2f）。
- 服务端信号处理（`cmd/tunnelmesh-server` 目前没有 SIGTERM 处理）。
- QUIC 的 Retry 地址验证与负载感知限流。
- SOCKS5 UDP ASSOCIATE，使 `quic://` 能经过代理。
