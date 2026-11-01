# ADR 0004：Agent 与 Server 之间增加裸 TCP（TLS）与 QUIC 传输

- 状态：已接受
- 日期：2026-11-01
- 相关：[ADR 0002 公网入口与嵌入式 VPN](0002-public-ingress-and-embedded-vpn.md)、[Agent 裸传输协议](../../protocol/agent-transports.md)、[实施计划](../../superpowers/plans/2026-11-01-agent-raw-transports.md)

## 背景

Agent 与 Server 之前只有 WebSocket 一条通道，它依赖 HTTP 入口。在只放行裸 TCP 或 UDP 的网络、或希望绕开 HTTP 反向代理的部署中，这条路径不可用。

## 决策

1. **TCP 使用 TLS 1.2 起的 `tls://`**，与 `wss://` 的信任模型一致。明文 `tcp://` 只在回环地址或显式 `allow_insecure_transport` 下允许。
2. **UDP 使用 QUIC（quic-go），TLS 1.3，ALPN `tunnelmesh-agent/1`**。整个帧协议放在单个双向流上，帧格式不变。
3. **认证使用固定格式的 preface**（`TMAG` 头加令牌），在任何帧之前完成，并由独立的小解析器读取。不新增帧类型，因此不需要为认证引入能力协商。
4. **Server 端使用独立端口，默认关闭**，不与 HTTPS 端口复用，避免改变现有入口的行为。
5. **认证之后复用 WebSocket 的核心逻辑**（`serveAgentConnection`）。传输层只负责字节进出，帧语义保持单一实现。
6. **未认证阶段的预算由监听器自己承担**。HTTP 栈原本会吸收慢速头部与连接洪泛，裸监听器必须自己限制总数、每地址数量、preface 超时与失败次数。
7. **QUIC 代理限制**：QUIC 是 UDP，而 Agent 支持的 HTTP CONNECT 与 SOCKS5 代理只处理 TCP。配置校验拒绝 `quic://` 与任何非 direct 代理模式的组合。SOCKS5 UDP ASSOCIATE 不在本次范围内。

## 备选方案

- **在 `AgentHello` 中加入令牌字段**：会让认证与帧语义耦合，且未认证字节会进入帧解码器。
- **自研可靠 UDP 层**：需要分片、重传、乱序重组与拥塞控制，工作量大且容易出安全问题。QUIC 已经提供这些能力，并自带 TLS 1.3。
- **TCP 与 HTTPS 共用端口，按协议嗅探**：会改变现有入口的行为，并且增加对 HTTP 入口的复杂度，因此拒绝。

## 影响

- 新增依赖 `github.com/quic-go/quic-go`（MIT）。实测 Linux amd64 下 Server 增加约 1.6 MiB，Agent 增加约 1.5 MiB。
- 新增 Server 配置块 `server.agent_transports`，默认关闭，升级不改变现有部署。
- 认证失败的地址会被短暂拒绝。这是预算的一部分，不影响其他地址。
- 单一 QUIC 流与 TCP 一样存在队头阻塞，这是协议层面的已知代价。
- 尚未实现的部分：审计记录中的 transport 字段，以及服务端的信号处理（`cmd/tunnelmesh-server` 目前不处理 SIGTERM）。

## 回滚

清空 `server.agent_transports` 中的监听地址并重启 Server，或把 Agent 的 `server_url` 改回 `wss://`。协议帧没有变化，也没有数据迁移。
