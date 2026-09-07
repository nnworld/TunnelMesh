# 代理协议模块

TunnelMesh 的公网入口继续只监听 HTTP/HTTPS/WebSocket。内部会话通过能力协商启用具体模块，并由 service token scope、Agent policy、目标 CIDR/端口白名单、超时和配额共同约束。

当前协议基础包括：

- TCP stream：面向连接的双向字节流。
- UDP association：保留 datagram 边界，单个 datagram 受大小上限限制。
- HTTP CONNECT：解析 `CONNECT host:port HTTP/1.1`，拒绝非法 Host、端口和 CR/LF 注入。
- SOCKS5 CONNECT：支持 IPv4、IPv6 和域名地址；UDP ASSOCIATE 使用相同 association 生命周期并受 UDP 配额约束。
- PROXY protocol v2：当前安全解析 TCP/IPv4 地址头，后续地址族由能力协商显式启用。
- TLS SNI passthrough：只转发经过路由策略允许的 SNI，不终止端到端 TLS。
- Unix socket / Windows named pipe：仅在 Agent 本机目标策略明确允许时使用。
- DNS proxy：仅代理配置的 DNS 目标，禁止把任意 DNS 解析器当作 SSRF 绕过。

握手前必须完成 capability negotiation。未协商的模块不得被客户端强行使用；协议错误使用稳定错误码，流控窗口耗尽返回 `RESOURCE_EXHAUSTED`，关闭阶段使用 GOAWAY/drain 避免新流进入排空会话。

## 限制与安全

代理模块不会执行任意远程命令，也不实现 ICMP、TUN/L2 VPN 或 P2P NAT traversal。所有目标地址在 Agent 侧再次校验，解析结果重新进行私网/回环/链路本地限制检查。错误日志只记录协议、错误码和 trace id，不记录认证头或会话字节。
