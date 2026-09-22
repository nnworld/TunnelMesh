# VPN 网关 阶段 7：Agent ICMP echo 与能力协商 Implementation Plan

- 日期：2026-09-23
- 分支：`codex/vpn-phase7-agent-icmp`（stacked 在 `codex/vpn-phase5-admin-console` 之上）
- 规格：[内嵌 VPN 网关（WireGuard）设计](../specs/2026-09-19-embedded-vpn-gateway-design.md) §6.1、§6.2、§12.1、§14（能力协商测 / Agent ICMP 单测）、§15 阶段 7、§18 验收 8 与 9
- 决策载体：[ADR 0002](../../architecture/adr/0002-public-ingress-and-embedded-vpn.md)（ICMP echo 已批准，P2P 与 RCE 禁令保留）
- 前置阶段：[阶段 4 PR 记录](../../pull-requests/2026-09-21-vpn-phase4-pure-logic-and-api.md)（`refuseICMP` 一律拒绝）、[阶段 5 PR 记录](../../pull-requests/2026-09-22-vpn-phase5-admin-console.md)（`vpn.form.icmpEnabledHelp` 文案）

## 1. 目标

让 Agent 具备“替 Server 发一个 ICMP echo 并把应答带回来”的能力，并让管理面据此做真实判定：

1. `protocol.CapabilityStreamICMPEcho = "stream_icmp_echo.v1"` 进入能力协商，Agent 通告、Server ack、Agent 校验 ack。
2. `relay.StreamRequest.Protocol` / `StreamOpenPayload.Protocol` 新增 `"icmp-echo"`，Agent 侧 `dialStreamPayload` 新增该 case，旧 Agent 走 default 安全拒绝且不崩溃。
3. `internal/agent/icmp_echo.go`：非特权 ping socket（`x/net/icmp` 的 `udp4`）、关联 ID 由 Server 携带、并发上限、超时、关闭语义、`ping_group_range` 缺失时明确报错。
4. `internal/protocol/icmp.go`：echo 请求/应答的线路编解码（一个在途 echo = 一条 stream = 一个数据报）。
5. Agent 配置新增 4 个键并注册默认值；`tunnelmesh-agent run` 装配引擎、退出时关闭、开不了 ping socket 时不通告能力并大声报错。
6. Server 签发/修改 peer 时把阶段 4 的“一律拒绝”换成**真实能力判定**（`vpn_agent_capability_missing` 只在真的缺能力或无法判定时给出对应消息）。
7. 修复 `AgentConnectionSelector.Select` 把“流协议名”当“能力名”比较的既有缺陷（VPN 出口选路会踩到它）。
8. 文档与前端文案同步：`ping_group_range` 一次性设置、新协议值、`icmpEnabledHelp` 不再宣称“本版一律拒绝”。

## 2. 非目标

- **不写 Server 数据面**（阶段 6）：没有 TUN、没有 netstack、没有 UDP 51820 监听、没有任何进程发起 `icmp-echo` stream。本阶段结束时 ICMP 仍然 ping 不通，因为没有人发请求。
- 不新增 Go 模块：`golang.org/x/net v0.59.0` 已是主模块直接依赖，`go list -deps golang.org/x/net/icmp` 实测可解析，`go.mod`/`go.sum` 零改动。
- 不做 `//go:build vpn` 隔离：Agent 侧只依赖 `x/net/icmp`（已在依赖树内），加 tag 只会让默认构建的 Agent 少一个能力而不省任何依赖；数据面隔离属阶段 6。
- 不建 `vpn_flows` 表、不加 Prometheus 指标（阶段 8）、不做 Schema 变更（`SchemaVersion` 保持 15）。
- 不实现 ICMPv6、不做 traceroute、不放开 ICMP echo 之外的任何类型（规格 §18 验收 3：其余类型丢弃并计数，计数在阶段 6 的 Server 侧策略链）。
- 不做跨节点能力查询 RPC（见 D7 的显式取舍与技术债记录）。

## 3. 前置核实结论（实测，非推断）

1. `go list -deps golang.org/x/net/icmp` 成功输出 `golang.org/x/net/icmp`，且 `git status --porcelain go.mod go.sum` 为空 → 引入该包**不改依赖清单**。
2. 能力常量表在 `internal/protocol/capabilities.go:11-15`，现有三个 `stream_*.v1`；`NegotiateCapabilities` 取客户端与服务端 features 的交集并排序。
3. Agent 通告点是 `internal/agent/dial_executor.go:101` 的 `AgentStreamCapabilities(streams)`，它当前 `_ = streams` 并恒定返回 `[]string{CapabilityStreamOpenResult}`；调用点在 `internal/cli/root.go:378` 的 `NewAgentDispatcherFactory`。
4. Server 侧会话保存的是协商结果：`internal/server/session_manager.go:149-159` 只有在 `cfg.SupportedCapabilities` 非空时才做交集，而生产装配从未设置它（`internal/cli/root.go`、`internal/server/runtime.go` 均无赋值），因此 `neg = req.Capabilities`；ack 回填同一份列表（`session_manager.go:226`），Agent 可在 `internal/agent/connection_pool.go:74-86` 的 ack 分支里校验。
5. Agent 的流分派入口是 `internal/agent/session.go:127` 的 `StreamDispatcher.Handle`，其中 `f.StreamID == 0 || p.TargetHost == "" || p.TargetPort < 1 || p.TargetPort > 65535` 一律判为 `invalid stream target`：**ICMP 没有端口**，这条校验必须为 `icmp-echo` 放宽。
6. `dialStreamPayload`（`internal/agent/dialer.go:104`）返回 `io.ReadWriteCloser`，UDP 走 `net.Conn` 因此“一次 Write = 一个数据报、一次 Read = 一个数据报”，`internal/protocol/udp.go` 的 `UDPAssociation` 不加任何帧头。ICMP 适配器必须保持同样的数据报语义。
7. **既有缺陷（实测）**：`internal/server/connection_selector.go:103` 的
   `(protocol != "" && len(session.Capabilities) > 0 && !session.Supports(protocol))`
   把流协议名与能力名比较。用一次性 scratch 测试验证：会话通告 `stream_open_result.v1` 时
   `Select(ctx, agent, "")` → 命中本地连接；`Select(ctx, agent, "tcp")` → `relay: node disconnected`；
   `Select(ctx, agent, "stream_open_result.v1")` → 命中。而 `internal/server/proxy_entry.go:292`
   与 `:377`、`internal/server/webssh_broker.go:126` 传的正是 `"tcp"`/`"http"`，
   `internal/relay/selection.go:45,74` 直接把 `request.Protocol` 交给 `Select`。
   阶段 6 的 VPN 出口会走同一条选路，因此本阶段必须修掉它（T7）。
8. 阶段 4 的拒绝点是 `internal/server/vpn_peer_service.go:649-660` 的 `refuseICMP`，
   被 `Create`（`:208`）与 `Patch`（`:343`）调用；`VPNServiceConfig.ICMPEnabled` 是节点级上限，
   `VPNPeerServiceDeps` 目前没有任何会话/注册中心视图，需要新增一个注入点。
9. 集群可见性：`registry.NodeOwner`（`internal/registry/registry.go:41-56`）不含能力字段，
   `agent_connection_leases`（`migrations/ddl.sql:254-268`）也没有能力列；
   `agents.capabilities`（`internal/storage/repository.go:1122`）是**用户在 API 里声明**的标签
   （`internal/server/api.go:158`、`:480`、`:1760` 与 OpenAPI `AgentRequest.capabilities`），
   不是运行时协商结果，因此不能用它承载协商能力（会破坏单一数据源）。
10. 跨节点已有 gRPC 通道：`ClusterAgentConnectionCloseService`（`internal/server/agent_connection_api.go:82-140`）
    用 `dialRelayNode` + `relay.CloseAgentConnectionRequest` 找到 owner 节点执行关闭。
    加一个能力查询 RPC 需要改 relay 的 proto 与服务端，属独立工作量（见 D7）。
11. Agent 配置块是 `config.AgentStreamConfig`（`internal/config/config.go:294-300`），
    默认值登记在 `internal/config/config.go:603-607` 的 `agent.streams.*`；
    `NewAgentDispatcherFactory(cfg.Agent.Streams)` 已经拿到整个结构体，扩展点天然存在。

## 4. 架构决策

### D1：一条 stream = 一个在途 echo，关联 ID 走数据报而不走 `StreamOpenPayload`

规格 §6.2 要求“一个在途 echo 请求一条关联”。落地方式：Server 用 `protocol: "icmp-echo"`、
`target_host: <目标 IP>`、`target_port: 0` 开一条 stream，然后在这条 stream 上写**一个数据报**
（`ICMPEchoRequest`），Agent 回**一个数据报**（`ICMPEchoReply`）后返回 EOF。

关联 ID 放在数据报里而不是 `StreamOpenPayload` 的新字段，理由：
`StreamOpenPayload` 是所有协议共用的开流载荷，为单一协议加字段会让其余协议的调用方
面对一个永远为空的键；而数据报本来就是本协议的私有线路格式，放在 `internal/protocol/icmp.go`
里由 Server 与 Agent 共同 import，契约只有一处定义。

### D2：不复用内核改写的 ICMP id，用 Agent 自己分配的 wire sequence 做多路复用

非特权 ping socket（`SOCK_DGRAM` + `IPPROTO_ICMP`）下内核会把 ICMP id 改写成 socket 自己的标识，
因此**id 不能用于关联**（规格 §6.2 明确禁止）。一个 Agent 进程只有一个 ping socket，
却要服务多条并发 echo，所以 Agent 为每个在途 echo 分配一个**唯一的 wire sequence**（16 位空间，
跳过占用中的值），维护 `wireSeq → pending` 映射；应答到达时按 wireSeq 找回关联，
并**校验应答 data 与请求 data 逐字节相同**（防 seq 回绕后撞上旧关联）。
回给 Server 的应答里带的是 Server 的 `correlation_id` 与**原始的** identifier/sequence，
因此 VPN peer 看到的 id/seq 与它发出的一致。

### D3：ping socket 以接口注入，`ping_group_range` 缺失是明确错误而不是静默降级

`icmp.ListenPacket("udp4", bind)` 在 `net.ipv4.ping_group_range` 不含进程 gid 时返回 EACCES/EPERM。
引擎的构造分两层：`NewEchoer(conn ICMPPacketConn, cfg EchoerConfig)`（纯逻辑，可注入假连接穷举测试）
与 `OpenEchoer(cfg)`（真实 socket，把权限错误映射成 `ErrPingGroupRangeRequired`，消息里写明
需要设置的 sysctl 与校验命令）。测试覆盖“内核改写 id 后关联仍正确”“超时”“并发上限”
“缺 `ping_group_range` 时明确报错”四项（规格 §14 的 Agent ICMP 单测行）。

### D4：开不了 ping socket 时 Agent 不通告能力，但**不**退出

`icmp_enabled: true` 而 socket 打不开：记 Error 级结构化日志（含 sysctl 名与当前 gid 提示）、
不通告 `stream_icmp_echo.v1`、其余能力照常。理由：Agent 还承担 TCP/UDP 出口与既有隧道，
因为一个可选能力把整个 Agent 拉下线，违反“故障隔离”与“消除单点”；
而“不通告能力”会让 Server 拒签 ICMP peer，问题恰好暴露在运维会采取行动的地方。
规格 §18 验收 8 要求的是“明确报错而非静默失败”，日志 + 能力缺失 + 文档三处都满足。

### D5：能力只在“配置开启且引擎就绪”时通告；ack 未确认则拒绝服务

`AgentStreamCapabilities(streams)` 增加 `icmpReady bool` 参数（签名变更为
`AgentStreamCapabilities(streams config.AgentStreamConfig, icmpReady bool)`），
只有 `streams.ICMPEnabled && icmpReady` 才追加能力。
Agent 收到 `FrameAgentMetadataAck` 后按 `ack.Capabilities` 校验：
未包含 `stream_icmp_echo.v1` 时 `dispatcher.SetICMPEchoEnabled(false)`，
此后 `icmp-echo` 开流一律安全拒绝。这是规格 §6.1 的“ack 校验”，
也保证“通告了但对端不认”时不会有半开状态。

### D6：`icmp-echo` 的开流校验单独放宽端口，其余协议逐字不变

`StreamDispatcher.Handle` 的目标校验改为：`TargetHost` 必填不变；
`TargetPort` 在 `Protocol == "icmp-echo"` 时允许 0（ICMP 无端口），其余协议仍要求 1–65535。
不引入“端口可选”的通用放宽，避免给既有协议开一个静默失败面。

### D7：签发门禁按“本节点可判定”执行，无法判定时**放行并在审计里标注 unverified**

判定顺序（`clusterAgentICMPProbe`）：
1. 本节点有该 Agent 的健康会话且通告了能力 → `Supported`。
2. 本节点有健康会话但都没有该能力 → `Unsupported`（409 `vpn_agent_capability_missing`，消息点名能力）。
3. 本节点无会话、集群里其他节点持有连接租约 → `Unverified`。
4. 集群里也查不到任何连接 → `Unsupported`（消息说明 Agent 未连接，无法核实）。

`Unverified` 选择**放行**而不是拒绝：管理 API 在集群里可能落在任意节点，
若“查不到就拒”，控制台签发 ICMP peer 会随请求落点随机失败，用户看到的是“偶发 409”，
这比“签发成功、运行时由数据面按 `icmp_unsupported` 拒绝并计数”更难排查。
放行的同时把 `icmpCapability: verified|unverified` 写进签发/修改的审计 details（JSON blob，无 Schema 变更），
并在用户文档里写明集群限制与后续方案。
**技术债（记入 PR 记录）**：集群级能力可见性需要二选一——
`agent_connection_leases` 增列（Schema v16，MINOR）或 relay 增一个能力查询 RPC（照
`ClusterAgentConnectionCloseService` 范式）；两者都不属于阶段 7 的最小闭环，推迟到阶段 8 评估。
节点级上限 `server.vpn.icmp_enabled=false` 时一律 409（同一个稳定码，消息说明是节点开关），
不新增第 11 个稳定码（阶段 4 PR 记录已就“扩大契约面”做过同样取舍）。

### D8：选择器按“协议 → 所需能力”映射，修掉既有的字符串误比较

`Select(ctx, agentID, protocol)` 的语义保持“传流协议名”，内部改为
`required := streamProtocolCapability(protocol)`：`"icmp-echo" → CapabilityStreamICMPEcho`，
其余（`tcp`/`udp`/`http`/`""`）→ `""`（基线能力，任何持有会话的 Agent 都能服务）。
只有 `required != ""` 时才调用 `session.Supports(required)`。
这同时修好第 3 节第 7 条实测到的既有缺陷：`"tcp"` 不再被当成能力名而误杀全部会话。

### D9：Agent 侧总量上限是最后一道防线，独立于 Server 的 per-peer 上限

`agent.streams.icmp_max_concurrent`（默认 64）在 Agent 进程内做全局信号量；
超限时立刻回 `capacity_exhausted` 状态的应答（不排队、不阻塞 stream），
因为排队会把 Server 的 per-peer 限额变成一句空话。超时用 `agent.streams.icmp_timeout`（默认 5s），
超时回 `timeout` 状态。状态字符串是稳定枚举，阶段 6 直接映射到 `error_class`。

## 5. 全局约束

1. 每个任务先写失败测试、实测红灯原文，再写最小实现（`AGENTS.md` TDD 流程）。
2. `go.mod`/`go.sum` 零改动；`migrations/`、`SchemaVersion`、`docs/api/openapi.yaml` 的路径集合零改动
   （只允许改 `vpn_agent_capability_missing` 的**描述文字**，因为它不再恒成立）。
3. 不新增稳定错误码：ICMP 相关拒绝继续用 `vpn_agent_capability_missing`（10 个码的集合不变，
   `internal/server/vpn_peer_openapi_test.go` 的双向相等守卫必须继续绿灯）。
4. 新协议值与新能力必须“未知即安全拒绝”：旧 Agent 收到 `icmp-echo` 走 default 分支返回错误、
   旧 Server 不会发 `icmp-echo`；能力未 ack 时 Agent 拒绝服务。
5. 私钥、Token、DSN、ICMP 载荷不进日志与审计；审计只记 peer ID、agent ID 与能力判定结果。
6. 一个任务一个提交，`<type>(<scope>): <subject>`，subject ≤50 字、祈使句、无句号，body 记红灯原文首行。
7. 时点记录零改写：阶段 4/5 的计划与 PR 记录只链接不修改；本计划对它们的事实更正只写在本文件与 PR 记录里。
8. 完整 Go 门禁必须恢复执行：`go test ./... -count=1`、`go test -race ./... -timeout 30m -count=1`、
   `go vet ./...`、`gofmt -l`、`git diff --check`；本阶段动了前端文案，因此前端门禁与嵌入校验也要跑。

## 6. 文件清单

| 文件 | 任务 | 内容 |
| --- | --- | --- |
| `docs/superpowers/plans/2026-09-23-vpn-phase7-agent-icmp.md` | — | 本计划 |
| `internal/protocol/capabilities.go` | T1 | `CapabilityStreamICMPEcho` 常量 |
| `internal/protocol/icmp.go` | T1 | `StreamProtocolICMPEcho`、`ICMPEchoRequest/Reply`、状态枚举与编解码 |
| `internal/protocol/icmp_test.go` | T1 | 往返、超限、未知字段容忍、状态集合稳定 |
| `internal/agent/icmp_echo.go` | T2 | `ICMPPacketConn` 接口、`Echoer`、seq 分配、超时、上限、`OpenEchoer` |
| `internal/agent/icmp_echo_test.go` | T2 | 假连接穷举：内核改写 id、超时、上限、未匹配应答、关闭、权限错误 |
| `internal/agent/dialer.go` | T3 | `Dialer.ICMPEcho` 字段 + `case "icmp-echo"` + echo stream 适配器 |
| `internal/agent/session.go` | T3 | 端口校验放宽、`SetICMPEchoEnabled`、未启用时安全拒绝 |
| `internal/agent/icmp_stream_test.go` | T3 | 端到端：开流→写请求→读应答→EOF；nil 引擎拒绝；未 ack 拒绝 |
| `internal/agent/connection_pool.go` | T3 | ack 分支识别新能力并驱动 `SetICMPEchoEnabled` |
| `internal/agent/dial_executor.go` | T4 | `AgentStreamCapabilities(streams, icmpReady)` |
| `internal/agent/dial_executor_test.go` | T4 | 能力集合随配置与就绪状态变化 |
| `internal/config/config.go` | T5 | `agent.streams.icmp_*` 4 个键、默认值、校验 |
| `internal/config/config_test.go` | T5 | 默认值、优先级、非法值快速失败 |
| `internal/cli/root.go` | T6 | 引擎装配、注入 Dialer、退避关闭、失败不通告 |
| `internal/cli/agent_runtime_test.go` | T6 | 能力集合与引擎生命周期 |
| `internal/server/connection_selector.go` | T7 | `streamProtocolCapability` 映射 |
| `internal/server/connection_selector_test.go` | T7 | `tcp` 不再误杀、`icmp-echo` 按能力过滤 |
| `internal/server/vpn_peer_service.go` | T8 | `VPNAgentCapabilityProbe`、`authorizeICMP` 取代 `refuseICMP`、审计标注 |
| `internal/server/vpn_icmp_gate.go` | T8 | `clusterAgentICMPProbe` 实现（会话 + 注册中心） |
| `internal/server/vpn_peer_service_test.go` | T8 | 支持/不支持/未连接/未核实/节点上限五种判定 |
| `internal/server/vpn_peer_api_test.go` | T8 | 更正阶段 4 的“一律 409”断言 |
| `internal/server/runtime.go` | T8 | 装配 probe |
| `docs/protocol/proxy-modules.md`、`docs/operations/configuration.md`、`docs/operations/config-examples.md`、`docs/user-guide/agent.md`、`docs/user-guide/server-admin.md`、`docs/api/openapi.yaml` | T9 | 新协议值、新配置键、`ping_group_range`、ICMP 文案更正 |
| `web/src/i18n/messages/{zh-CN,en-US}.ts` | T9 | `vpn.form.icmpEnabledHelp` 与 `vpn.errors.agentCapabilityMissing` 文案更正 |
| `docs/pull-requests/2026-09-23-vpn-phase7-agent-icmp.md` | T10 | PR 记录 |

### 明确不改

- `go.mod`、`go.sum`、`migrations/**`、`internal/storage/**`、`internal/vpn/**` 的错误码集合。
- 阶段 4/5 的计划与 PR 记录、ADR 0002、既有 spec。
- `web/package.json`、`web/package-lock.json`（不新增依赖）。
- `internal/client/**`（`udp_assoc.go` 只作范式参考，规格 §6.2 明确不复用其代码）。

## 7. 任务分解

### T1：协议扩展（`internal/protocol`）

**Step 1（红灯）**：`internal/protocol/icmp_test.go` 断言
`CapabilityStreamICMPEcho == "stream_icmp_echo.v1"`、`StreamProtocolICMPEcho == "icmp-echo"`、
`ICMPEchoRequest`/`ICMPEchoReply` 编解码往返、超过 `MaxDatagram` 拒绝、
未知 JSON 字段被忽略（前向兼容）、状态枚举恰好是
`{ok, timeout, capacity_exhausted, unreachable, unsupported, cancelled}` 六个。
Run `go test ./internal/protocol/ -run ICMP -count=1` → 编译失败 `undefined: protocol.ICMPEchoRequest`。

**Step 2（实现）**：`capabilities.go` 加一个常量；`icmp.go` 加类型与 `Encode/Decode`（照
`stream_open.go` 的 `MaxPayload` 守卫范式）。

**Step 3（绿灯 + 提交）**：`feat(protocol): add the icmp echo stream contract`。

### T2：Agent echo 引擎（`internal/agent/icmp_echo.go`）

**Step 1（红灯）**：`icmp_echo_test.go` 用假 `ICMPPacketConn` 覆盖：
一个 echo 往返（内核把 id 改写成别的值，关联仍按 wireSeq 命中）；两个并发 echo 各自拿到自己的应答；
应答 data 不匹配时丢弃并超时；超时回 `timeout`；在途数达到上限时立刻回 `capacity_exhausted`；
未匹配的应答不 panic、不影响其他关联；`Close` 让全部在途立刻失败且读循环退出；
`OpenEchoer` 在 listen 失败时返回 `ErrPingGroupRangeRequired`（用注入的 listen 函数模拟 EACCES）。
Run → 编译失败 `undefined: agent.NewEchoer`。

**Step 2（实现）**：`Echoer`（`pending map[uint16]*pendingEcho` + `sync.Mutex`、读循环、
`time.AfterFunc` 超时、信号量计数、seq 分配跳过占用值）、`Request`/`Reply` 值类型、
`OpenEchoer` 与 `ErrPingGroupRangeRequired`。不复用 `internal/client/udp_assoc.go` 的代码。

**Step 3（绿灯 + 提交）**：`feat(agent): answer icmp echo over a ping socket`。

### T3：流适配与 ack 门禁

**Step 1（红灯）**：`icmp_stream_test.go` 断言
`dialStreamPayload(ctx, {Protocol:"icmp-echo", TargetHost:"10.0.0.5"})` 在 `Dialer.ICMPEcho` 为 nil 时
返回“不支持”错误而不是 panic；注入引擎后写一个 `ICMPEchoRequest` 数据报能读回一个
`ICMPEchoReply` 数据报且随后 EOF；`StreamDispatcher` 在 `SetICMPEchoEnabled(false)` 时拒绝
`icmp-echo` 开流；`target_port: 0` 对 `icmp-echo` 合法、对 `tcp` 仍非法；
`connectionPoolHandler` 收到不含新能力的 ack 后把开关关掉。
Run → 失败（case 不存在，`icmp-echo` 落到 default）。

**Step 2（实现）**：`Dialer.ICMPEcho *Echoer`、`icmpEchoStream`（`io.ReadWriteCloser`，
一次 Write 一个请求、一次 Read 一个应答、之后 EOF）、`dialStreamPayload` 新 case（先
`d.check(ctx, "icmp", host, 0)` 走既有 PolicyHook）、`StreamDispatcher` 的端口校验分支与
`SetICMPEchoEnabled`、`connection_pool.go` 的 ack 识别。

**Step 3（绿灯 + 提交）**：`feat(agent): serve icmp echo streams`。

### T4：能力通告

**Step 1（红灯）**：`dial_executor_test.go` 断言
`AgentStreamCapabilities(cfg, false)` 只含 `stream_open_result.v1`、
`AgentStreamCapabilities(cfg{ICMPEnabled:true}, true)` 追加 `stream_icmp_echo.v1`、
`AgentStreamCapabilities(cfg{ICMPEnabled:true}, false)` 不追加（配置开了但引擎没起来）。
Run → 编译失败（参数个数不符）。

**Step 2（实现）**：改签名与实现，更新唯一调用点（T6 会再改装配）。

**Step 3（绿灯 + 提交）**：`feat(agent): advertise the icmp echo capability`。

### T5：Agent 配置

**Step 1（红灯）**：`config_test.go` 断言四个默认值
（`agent.streams.icmp_enabled=false`、`icmp_bind_address="0.0.0.0"`、`icmp_timeout=5s`、
`icmp_max_concurrent=64`）、环境变量与命令行覆盖、`icmp_timeout<=0` 与
`icmp_max_concurrent<0` 快速失败、`icmp_bind_address` 非法快速失败。
Run → 失败 `cfg.Agent.Streams.ICMPEnabled undefined`。

**Step 2（实现）**：`AgentStreamConfig` 四个字段 + 默认值 map + 校验分支（照
`server.vpn` 的“能启动即能用”范式）。

**Step 3（绿灯 + 提交）**：`feat(config): add the agent icmp stream keys`。

### T6：CLI 装配

**Step 1（红灯）**：`agent_runtime_test.go` 断言
`NewAgentDispatcherFactory(streams, nil)` 通告的能力不含 ICMP；
传入一个就绪引擎的工厂函数时含 ICMP；工厂创建的 Dialer 带着该引擎；
`icmp_enabled: true` 而 `OpenEchoer` 失败时能力集合不含 ICMP 且返回一个可断言的错误/日志信号。
Run → 编译失败（工厂签名不符）。

**Step 2（实现）**：`NewAgentDispatcherFactory(streams, echoer)`；`agent run` 路径按配置开引擎、
失败时记 Error 并以 nil 引擎继续、把引擎交给连接池的关闭链（进程退出即 `Close`）。

**Step 3（绿灯 + 提交）**：`feat(cli): wire the agent icmp echo engine`。

### T7：选择器协议→能力映射（既有缺陷根因修复）

**Step 1（红灯）**：`connection_selector_test.go` 新增两用例：
会话通告 `stream_open_result.v1` 时 `Select(ctx, agent, "tcp")` 必须命中本地连接
（当前实测返回 `relay: node disconnected`）；两条会话只有一条通告 `stream_icmp_echo.v1` 时
`Select(ctx, agent, "icmp-echo")` 必须命中那一条。
Run → 第一条失败。

**Step 2（实现）**：`streamProtocolCapability` + `Select` 的过滤条件改写（D8）。

**Step 3（绿灯 + 提交）**：`fix(server): map stream protocols to capabilities`。

### T8：签发门禁换成真实判定

**Step 1（红灯）**：`vpn_peer_service_test.go` 新增五例（Supported / Unsupported /
Unverified / AgentNotConnected / 节点上限关闭），并**更正**阶段 4 那条“一律 409”的断言；
`vpn_peer_api_test.go` 同步更正。Run → 失败（`refuseICMP` 仍恒拒绝）。

**Step 2（实现）**：`VPNAgentCapabilityProbe` 接口 + `clusterAgentICMPProbe`（`vpn_icmp_gate.go`）+
`authorizeICMP` 取代 `refuseICMP` + 审计 details 增 `icmpCapability` + `runtime.go` 装配。
10 个稳定码集合不变。

**Step 3（绿灯 + 提交）**：`feat(server): gate vpn icmp on agent capability`。

### T9：文档与前端文案

改 6 份活文档与 2 份 i18n 文案：新协议值与能力（`docs/protocol/proxy-modules.md`）、
4 个配置键与 `ping_group_range` 一次性设置（`docs/operations/configuration.md`、`config-examples.md`、
`docs/user-guide/agent.md`）、签发门禁的集群限制与 `unverified` 语义
（`docs/user-guide/server-admin.md` 的 VPN 章节）、`vpn_agent_capability_missing` 的描述
（`docs/api/openapi.yaml`，仅描述文字）、前端 `vpn.form.icmpEnabledHelp` 与
`vpn.errors.agentCapabilityMissing`（不再宣称“本版一律拒绝”，改为“需要出口 Agent 在线且已协商能力”）。
提交 `docs(vpn): describe the agent icmp capability`。

### T10：门禁与 PR 记录

`go test ./... -count=1` → `go test -race ./... -timeout 30m -count=1` → `go vet ./...` →
`gofmt -l internal docs scripts` → `git diff --check` → `go test ./scripts/ -count=1` →
`cd web && npm test -- --run && npm run build && cd .. && bash scripts/verify-web-embed.sh` →
`git status --porcelain go.mod go.sum migrations web/package.json web/package-lock.json` 为空 →
`python3 scripts/gen_doc_index.py` 两次幂等 → 死链核验 → 写 PR 记录 → 提交 → push。

## 8. 任务间接口

- `protocol.ICMPEchoRequest{CorrelationID string; Identifier, Sequence uint16; Data []byte}`，
  `protocol.ICMPEchoReply{CorrelationID string; Identifier, Sequence uint16; Data []byte; Status string; RTTMillis int64}`；
  两者都由 `EncodeICMPEchoRequest/DecodeICMPEchoRequest`、`EncodeICMPEchoReply/DecodeICMPEchoReply` 序列化，
  受 `MaxDatagram` 约束。T3 的适配器与阶段 6 的 Server 侧共用这一对函数。
- `agent.ICMPPacketConn`：`ReadFrom([]byte) (int, net.Addr, error)`、`WriteTo([]byte, net.Addr) (int, error)`、
  `SetReadDeadline(time.Time) error`、`Close() error`、`LocalAddr() net.Addr`（`*icmp.PacketConn` 天然满足）。
- `agent.Echoer.Send(ctx, request EchoRequest) (EchoReply, error)`，
  `EchoRequest{CorrelationID string; Target netip.Addr; Identifier, Sequence uint16; Data []byte}`，
  `EchoReply{Identifier, Sequence uint16; Data []byte; Status string; RTT time.Duration}`。
  T3 的适配器只调这一个方法。
- `agent.Dialer.ICMPEcho *Echoer`：nil 即“本进程不提供 ICMP”，`dialStreamPayload` 返回明确错误。
- `agent.AgentStreamCapabilities(config.AgentStreamConfig, bool) []string`：T4 定形，T6 消费。
- `server.VPNAgentCapabilityProbe.ProbeICMPEcho(ctx, agentID) (AgentCapabilityState, error)`，
  `AgentCapabilityState ∈ {CapabilitySupported, CapabilityUnsupported, CapabilityUnverified}`：
  T8 定形，阶段 6 的数据面可复用同一接口做“开流前预判”。
- `server.streamProtocolCapability(string) string`：T7 定形，阶段 6 开 `icmp-echo` 流时依赖它选到有能力连接。

## 9. 整体验证

```bash
go test ./... -count=1
go test -race ./... -timeout 30m -count=1
go vet ./...
gofmt -l internal scripts
go test ./scripts/ -count=1
git diff --check
git status --porcelain go.mod go.sum migrations web/package.json web/package-lock.json   # 必须为空
cd web && npm test -- --run && npm run build && cd .. && bash scripts/verify-web-embed.sh
python3 scripts/gen_doc_index.py && python3 scripts/gen_doc_index.py && git status --porcelain
git diff --name-status codex/vpn-phase5-admin-console..HEAD -- docs/superpowers docs/pull-requests docs/architecture/adr
```

本阶段没有 Schema 变更，因此双方言迁移测与升级文档不适用；没有 Docker/Compose 变更，
容器验证不适用。ICMP 的真实收发依赖 `net.ipv4.ping_group_range`，本机（macOS）与 CI 都不满足，
因此 ping socket 以接口注入做单测，真实 socket 的行为记入 PR 记录的“未执行 + 原因”，
端到端验证留给阶段 8 的 `test/e2e/vpn/`（规格 §14：环境不满足则 SKIP 且退出码 0）。

## 10. 回滚注意事项

- 回滚顺序与依赖：T8 → T7 → T6 → T5 → T4 → T3 → T2 → T1。T1 必须最后回滚
  （`protocol` 常量与编解码被 T2/T3/T7/T8 全部引用），只回滚 T1 会**编译失败**。
- T7 是既有缺陷修复：回滚它会让 `Select(agentID, "tcp")` 重新误杀通告了能力的会话，
  即 tp-* 与 WebSSH 的选路回到缺陷状态。回滚 T7 前必须确认没有其他路径依赖它。
- 能力是**协商**出来的：回滚 Agent 后旧 Agent 不通告 `stream_icmp_echo.v1`，
  Server 侧门禁自动退回“Unsupported → 409”，不需要同时回滚 Server；
  反之只回滚 Server 也安全（旧 Server 不发 `icmp-echo`，Agent 的引擎闲置）。
- `agent.streams.icmp_enabled` 默认 `false`，因此**不回滚代码也能止损**：改配置重启 Agent 即可
  关掉 ping socket 与能力通告，5 分钟内可完成，无需数据库或防火墙动作。
- 审计 details 里的 `icmpCapability` 是新增键，回滚后旧代码不再写它；审计消费方（控制台 JSON 详情）
  对未知键容忍，无需迁移。
- 阶段 4/5 的行为差异：回滚 T8 会让 `icmpEnabled: true` 的签发重新变成“一律 409”，
  已经签发成功的 ICMP peer 行保持 `icmp_enabled=1`（数据不变），只是不能再新增或改回 true。
