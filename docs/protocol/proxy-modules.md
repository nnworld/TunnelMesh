# 代理协议模块

TunnelMesh 的公网入口继续只监听 HTTP/HTTPS/WebSocket。内部会话通过能力协商启用具体模块，并由 service token scope、Agent policy、目标 CIDR/端口白名单、超时和配额共同约束。

当前协议基础包括：

- TCP stream：面向连接的双向字节流。
- UDP association：保留 datagram 边界，单个 datagram 受大小上限限制。
- HTTP CONNECT：解析 `CONNECT host:port HTTP/1.1`，拒绝非法 Host、端口和 CR/LF 注入。
- 标准 HTTP 代理：Client 本地入口支持 absolute-form HTTP、`CONNECT` 隧道、WebSocket upgrade 和可选 Basic 认证；仅支持 HTTP/1.1。
- SOCKS5 CONNECT：Client 本地入口支持 IPv4、IPv6 和域名地址，可选 RFC 1929 用户名密码认证；`BIND` 和 `UDP ASSOCIATE` 不支持。
- PROXY protocol v2：当前安全解析 TCP/IPv4 地址头，后续地址族由能力协商显式启用。
- TLS SNI passthrough：只转发经过路由策略允许的 SNI，不终止端到端 TLS。
- Unix socket / Windows named pipe：仅在 Agent 本机目标策略明确允许时使用。
- DNS proxy：仅代理配置的 DNS 目标，禁止把任意 DNS 解析器当作 SSRF 绕过。

握手前必须完成 capability negotiation。未协商的模块不得被客户端强行使用；协议错误使用稳定错误码，流控窗口耗尽返回 `RESOURCE_EXHAUSTED`，关闭阶段使用 GOAWAY/drain 避免新流进入排空会话。

## Strict open 与 OPEN_RESULT

Client WebSocket 通过子协议选择打开语义：

- `tunnelmesh.v1.open-result.flow-control`：strict open + flow control。
- `tunnelmesh.v1.open-result`：strict open。
- `tunnelmesh.v1`：legacy open，`OPEN_STREAM` 发送成功即建立流。

strict open 要求完整链路协商 `stream_open_result.v1`：Client → Server、Server → Agent/远端 Server 均具备能力后才启用。Server 发给 Agent 的 `OPEN_STREAM` 会设置 `FlagStrictOpen`；Agent 必须先完成策略校验、DNS 解析和目标连接，再返回带稳定 stage/code 的 `OPEN_RESULT`。失败结果只 reset 对应流，不影响同一 WebSocket 会话中的其它流。

跨 Server gRPC relay 在 strict 模式下于字节流开头返回一个有界结果帧：4 字节 ASCII magic `TMR1`、4 字节大端 JSON 长度和不超过 1 KiB 的 `OPEN_RESULT` JSON。入口 Server 只有收到 `accepted=true` 才向 Client 返回成功。远端节点不支持该能力、返回非结果帧或结果格式非法时，入口返回 `unsupported_capability`，不会把普通数据伪装成打开成功。

legacy Agent、legacy Client 和 legacy 远端 Server 继续使用原字节流语义。strict Client 遇到任一 legacy 节点时降级为明确的 `unsupported_capability` 失败，不自动伪造成功结果。

## 流隔离、公平写与流控

数据面按“连接一个调度器、每流一个有界队列”执行：

- Client 入站 TCP stream 和 UDP association 均使用默认 256 KiB 的按字节有界队列；队列满时只对当前流发送 `RESET`，中央 WebSocket 接收循环继续处理其它流和 PING/PONG。
- Agent、Server 和 Client 的出站 DATA 进入 per-stream 队列，由 deficit round-robin 写出；控制帧（PING/PONG、OPEN_RESULT、RESET、HALF_CLOSE、WINDOW_UPDATE、GOAWAY）优先于 DATA。
- 单个 bulk 流不能连续占用两个调度回合；新激活的流获得下一个回合，避免小流量页面请求的首字节被大下载长期延迟。
- 控制队列满视为连接级背压；数据队列满只影响当前流。队列在入队边界复制 payload 一次，避免接收缓冲区被慢消费者复用后篡改。

选择 `tunnelmesh.v1.open-result.flow-control` 后，`OPEN_STREAM.Window` 携带初始接收窗口，当前默认为 256 KiB。发送方在发送 DATA 前扣减 send window，超过窗口返回 `RESOURCE_EXHAUSTED`；接收方消费数据后累计到 128 KiB 阈值，发送对应增量的 `WINDOW_UPDATE`。窗口更新为 0、导致 uint32 溢出或作用于已关闭流时按协议错误处理。

用于建缓冲的窗口数值一律经 `protocol.NegotiateReceiveWindow` 夹紧：小于 `128 KiB + 32 KiB`（阈值加一个整帧）或超过 512 KiB 的数值回落为默认窗口。原因是所有 pump 等待 credit 都没有超时，一个永远无法再容纳整帧的窗口会让流永久停摆；过大的数值则会把执行窗口的缓冲区撑成不可验证的尺寸。夹紧不改变对端通告的 credit：Server→Client 一跳严格按 Client 通告的窗口发送，`relayToClient` 把每次读取限制在当前可用 credit 内，因此小于整帧的窗口只会变慢。三条不变量与被否决的替代方案见 [ADR 0002](../architecture/adr/0002-dataplane-window-credit-invariants.md)。

本地 Agent relay 连接支持带外 `WINDOW_UPDATE` 控制读写，不会把控制帧混入 DATA 字节流。跨 Server gRPC relay 使用一个单字节 envelope 区分消息：`0x00` 表示数据，`0x01` 表示编码后的协议控制帧。该 envelope 是 relay 内部封装，不改变 Client/Agent 的 WebSocket wire format。

Client 发给 Server 的 `WINDOW_UPDATE` 只补充 Server→Client 这一跳的发送窗口，不能镜像给 Agent。Agent→Server 的额度由 Server 消费 Agent relay 字节后独立回补。两条链路的额度都在 Server 终止，避免同一字节增量被重复计 credit 后冲破 Agent 侧接收缓冲。

legacy 子协议不启用 `OPEN_STREAM.Window` 语义，仍使用有界队列和 `RESET` 保护内存；已建立的 strict/flow-control 连接不会中途切换语义。

## 限制与安全

代理模块不会执行任意远程命令，也不实现 ICMP、TUN/L2 VPN 或 P2P NAT traversal。所有目标地址在 Agent 侧再次校验，解析结果重新进行私网/回环/链路本地限制检查。错误日志只记录协议、错误码和 trace id，不记录认证头或会话字节。
