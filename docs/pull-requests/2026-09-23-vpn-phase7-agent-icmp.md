# VPN 网关 阶段 7：Agent ICMP echo 与能力协商

## 标题

`feat(agent): serve icmp echo streams behind a negotiated capability`

本记录覆盖分支上阶段 7 的全部提交（计划 → 协议契约 → echo 引擎 → 锁修正 → 流适配与 ack 门禁 →
Agent 配置 → 能力通告 → CLI 装配 → 选择器修复 → 签发门禁 → 文档与前端文案），而不只是最后一个提交。

## 目标分支

`main`

本分支 stacked 在 `codex/vpn-phase5-admin-console` 之上。阶段 4 与阶段 5 的 PR 合并进 `main` 后，
必须先 `git rebase --onto main codex/vpn-phase5-admin-console` 再更新本 PR，
否则 diff 会重复包含阶段 5 的产物（控制台 VPN 页与节点页 VPN 区块）。

## 关联记录

- 实施计划：[VPN 网关 阶段 7：Agent ICMP echo 与能力协商 Implementation Plan](../superpowers/plans/2026-09-23-vpn-phase7-agent-icmp.md)
- 设计规格：[内嵌 VPN 网关（WireGuard）设计](../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md)（§6.1 能力协商、§6.2 ICMP echo、§12.1 稳定码、§14 测试策略、§15 阶段 7、§18 验收 8 与 9）
- 决策载体：[ADR 0002: Open a public UDP ingress for an embedded VPN gateway](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)（ICMP echo 已批准；P2P NAT traversal 与任意远程命令执行仍禁止）
- 前置阶段：[阶段 5：管理台 VPN 页](2026-09-22-vpn-phase5-admin-console.md)、[阶段 4：纯逻辑包与管理 API](2026-09-21-vpn-phase4-pure-logic-and-api.md)（`refuseICMP` 一律拒绝）
- 协议文档：[代理协议模块 · ICMP echo 与 `stream_icmp_echo.v1`](../protocol/proxy-modules.md)
- 配置文档：[配置参考 · Stream 延迟与授权缓存](../operations/configuration.md)、[配置示例](../operations/config-examples.md)
- 用户文档：[Agent 使用指南](../user-guide/agent.md)、[管理员指南 · VPN 网关 peer 管理](../user-guide/server-admin.md)

## 摘要

阶段 7 让 Agent 具备“替 Server 发一个 ICMP echo 并把应答带回来”的能力，并让管理面据此做**真实判定**
而不是阶段 4 的一律拒绝。**本阶段结束时 `ping` 仍然不通**：没有任何进程发起 `icmp-echo` 流，
Server 数据面（WireGuard 端点、内存态 TUN、逐包策略链）属阶段 6。本阶段交付的是从线路契约到签发门禁的
一整条能力链，以及顺带修掉的一个既有选路缺陷。

七类产物：

1. **协议契约**（`internal/protocol/icmp.go` 134 行 + `icmp_test.go` 150 行）：
   `StreamProtocolICMPEcho = "icmp-echo"` 与 `CapabilityStreamICMPEcho = "stream_icmp_echo.v1"`，
   `ICMPEchoRequest`/`ICMPEchoReply` 及其编解码（受 `MaxDatagram` 约束、空 correlation 拒绝、
   未知字段容忍），以及闭合状态枚举 `ok`/`timeout`/`capacity_exhausted`/`unreachable`/`unsupported`/`cancelled`。
   契约只有一处定义，Agent 在阶段 7 解码、Server 在阶段 6 编码。
2. **Agent echo 引擎**（`internal/agent/icmp_echo.go` 425 行 + `icmp_echo_test.go` 550 行）：
   一个非特权 ping socket 服务所有在途 echo。内核会改写 ICMP id，因此 id 不能用于关联；
   引擎为每个在途 echo 分配唯一的 wire sequence，并在应答到达时逐字节比对 payload，
   防止 16 位序号回绕后撞上旧关联。回给 Server 的应答携带 peer 自己的 identifier/sequence。
   超时、超预算、目标不可达是**应答**而不是错误，因为 IP 没有错误通道。
   `OpenEchoer` 把权限失败映射成 `ErrPingGroupRangeRequired`，消息里写明需要设置的 sysctl。
3. **流适配与 ack 门禁**（`internal/agent/dialer.go` +165 行、`session.go`、`connection_pool.go`、
   `icmp_stream_test.go` 327 行）：一条 `icmp-echo` 流沿用 UDP 的数据报语义，一次 Write 一个请求、
   一次 Read 一个应答、之后 EOF。能力门禁默认关闭，只由 Server 的 ack 打开；
   `target_port: 0` 只对 `icmp-echo` 放宽。
4. **配置与装配**（`internal/config/config.go` +30、`internal/cli/root.go` +45、
   `agent_runtime_test.go` +254）：`agent.streams.icmp_enabled`（默认 `false`）、`icmp_bind_address`、
   `icmp_timeout`、`icmp_max_concurrent` 四个键；`AgentStreamCapabilities(streams, icmpReady)` 只在
   “配置开启且引擎就绪”时通告；socket 打不开时记 Error 到 stderr、不通告能力、**Agent 不退出**。
5. **既有缺陷修复**（`internal/server/connection_selector.go`）：`Select` 把流协议名当能力名比较，
   导致任何通告了能力的会话都不被选中，本地选路落空后走远端路径并报 `relay: node disconnected`。
   这影响的是现网的 tp-* 与 WebSSH 选路，不只是本阶段新增的 ICMP 出口。
6. **签发门禁**（`internal/server/vpn_icmp_gate.go` 111 行 + `vpn_icmp_gate_test.go` 143 行、
   `vpn_peer_service.go` +92）：`refuseICMP` 换成 `authorizeICMP`，按
   `Supported`/`Unsupported`/`Unverified` 三态判定；审计详情新增 `icmpCapability: verified|unverified`。
7. **文档与前端文案**：协议文档新增 ICMP echo 一节；配置文档新增 4 个键、`ping_group_range` 一次性设置；
   Agent 指南新增能力条目、主机要求与“能签发但 ping 不通”的排查顺序；管理员指南写清集群限制与
   `unverified` 语义；OpenAPI 只改描述文字；前端 `vpn.form.icmpEnabledHelp` 与
   `vpn.errors.agentCapabilityMissing` 不再宣称“本版一律拒绝”。

**零改动项**（与计划一致）：`go.mod`/`go.sum`（`golang.org/x/net` 已是主模块直接依赖）、
`migrations/`、`SchemaVersion`（保持 15）、OpenAPI 路径集合与 10 个稳定错误码集合、
`web/package.json`/`web/package-lock.json`。

## 用户影响

- **默认无变化**：`agent.streams.icmp_enabled` 默认 `false`，升级二进制不会在任何主机上打开 ping socket，
  也不会通告新能力；Server 侧因此仍然拒签 ICMP peer，与阶段 4/5 行为一致。
- **选路缺陷修复对所有用户生效**：`Select` 不再误杀通告了能力的会话。修复前，只要 Agent 通告了任何能力
  （现网 Agent 恒通告 `stream_open_result.v1`），本地选路就一定落空并回落到远端 relay 路径；
  单节点部署因此报 `relay: node disconnected`。这是 tp-*、WebSSH 与后续 VPN 出口共用的代码路径。
- **开启 ICMP 需要两步**：主机一次性 `sysctl -w net.ipv4.ping_group_range='0 2147483647'`，
  以及 `agent.streams.icmp_enabled: true`。缺任一步时 Agent 在 stderr 报一条 Error、不通告能力，
  但继续服务既有隧道；Server 侧签发返回 409 `vpn_agent_capability_missing`。
- **控制台文案更正**：原先告诉用户“本版服务端会拒绝”，现在说明真正的两个前提（Agent 在线且已协商能力、
  节点开关打开），并把节点开关列为 409 的一个原因。
- **仍然 ping 不通**：数据面在阶段 6。对外通知不得把本阶段描述成“VPN 可用”或“支持 ping”。

## API、Schema 与配置影响

### API

无新增路径、无新增稳定错误码。`docs/api/openapi.yaml` 只改了两处描述文字：

| 位置 | 变更 |
|---|---|
| `VPNPeerCreateRequest.icmpEnabled` | 由“本版一律拒绝”改为“需要节点开关 + 出口 Agent 已协商 `stream_icmp_echo.v1`；集群中无法核实时仍签发并在审计记 `icmpCapability: unverified`” |
| `VPNPeerPatchRequest.icmpEnabled` | 由“本版一律拒绝”改为“置 true 会按出口 Agent 能力重新校验” |

`internal/server/vpn_peer_openapi_test.go` 对 10 个稳定码与路径集合的双向相等守卫继续绿灯。

### Schema

**无变更**。`migrations/` 与 `SchemaVersion`（15）零改动，双方言迁移测与升级文档因此不适用。
审计的 `icmpCapability` 是 `audit_logs.details` JSON blob 里的一个新键，不是列，
旧代码不写、审计消费方（控制台 JSON 详情）对未知键容忍，无需迁移。

### 配置

新增 4 个 Agent 键（默认值登记在 `internal/config/config.go` 的 defaults map，与 `server.vpn.icmp_*` 同名同默认）：

| 键 | 默认值 | 校验 |
|---|---|---|
| `agent.streams.icmp_enabled` | `false` | — |
| `agent.streams.icmp_bind_address` | `0.0.0.0` | 仅 `icmp_enabled=true` 时校验，必须是 IP（ping socket 绑地址而非 `host:port`） |
| `agent.streams.icmp_timeout` | `5s` | 仅 `icmp_enabled=true` 时校验，必须为正 |
| `agent.streams.icmp_max_concurrent` | `64` | 仅 `icmp_enabled=true` 时校验，必须为正 |

校验只在开启时执行，与 `server.vpn.icmp_*` 的既有范式一致：关掉 ICMP 不会因为遗留的占位取值而无法启动，
而一个未开启的引擎永远不会被打开，因此“能启动即能用”成立。环境变量等价：`TUNNELMESH_AGENT_STREAMS_ICMP_*`。

**没有新增命令行 flag**：这四个键沿用既有的 `--agent.streams.*` 通用键路径，与 `max_concurrent_dials` 等一致。

### 协议

新增一个流协议值与一个能力，均遵循“未知即安全拒绝”：

- `OPEN_STREAM.protocol = "icmp-echo"`：旧 Agent 走 `dialStreamPayload` 的 default 分支返回
  `agent: unsupported stream protocol`，不崩溃；旧 Server 不会发它。
- `stream_icmp_echo.v1`：进入 `NegotiateCapabilities` 的交集，Agent 通告、Server ack、Agent 校验 ack。
  ack 未包含该能力时 Agent 拒绝服务 `icmp-echo` 开流，因此“通告了但对端不认”不会留下半开状态。

## 安全与授权影响

- **能力门禁 fail-closed**：`StreamDispatcher.icmpEcho` 默认 `false`，只由 `FrameAgentMetadataAck` 打开。
  一个从未收到 ack 的 Agent 拒绝 echo 开流，返回 `unsupported_capability` 而不是尝试执行。
- **目标地址双重校验**：`routing.IsDangerousAddress`（与代理模块共用的 SSRF 规则单一来源）
  加引擎自身的 `validateEchoTarget`（地址族、未指定、组播、链路本地）。`169.254.0.0/16`（云 metadata）
  在 Agent 侧被拒，因此一个被攻陷的控制面不能把 Agent 当成 metadata oracle。
  目标是字面 IP；主机名被拒而不是被解析，因为 Server 在开流前已解析，
  Agent 自行解析会回答一个与请求不同的问题。私网与回环目标在 Agent 侧允许：
  peer 能否到达它们由 Server 的逐包策略决定（阶段 6）。
- **PolicyHook 保持生效**：echo 开流以 `("icmp", host, 0)` 走既有 policy，端口固定为 0。
  传 wire 上的端口值会让一个端口白名单去裁决一个只关于地址的问题。
- **进程级预算**：`icmp_max_concurrent` 在 Agent 内做全局信号量，超限立即回 `capacity_exhausted`
  而不排队。Server 看不到一个 Agent 还在替哪些 peer 服务，所以这是防止一批 VPN peer
  把出口 Agent 变成反射器的最后一道防线；排队会让 Server 的 per-peer 限额变成一句空话。
- **只放开 echo**：不实现 ICMPv6、不实现 traceroute、不放开其它 ICMP 类型。
  引擎的读循环对非 echo-reply 数据报直接丢弃。P2P NAT traversal 与任意远程命令执行仍按 ADR 0002 禁止。
- **签发门禁不泄露 Agent 归属**：`Update` 路径的探测**移到 `authorizeAgent` 之后**。
  先探测会回答一个关于调用方可能无权使用的 Agent 的问题，而“409 缺能力”与“Agent 不可用”
  之间的差异本身就是信息。
- **无敏感数据落地**：审计只记 peer ID、agent ID、node ID 与 `icmpCapability`；
  ICMP payload、私钥、Token、DSN 不进日志、审计与指标。
- **权限失败明确报错**：`ErrPingGroupRangeRequired` 消息里带 sysctl 名与修复命令，
  不静默降级成“能力没通告但没人知道为什么”。

## 测试证据

### 红灯（每个任务实测，非推断）

| 任务 | 红灯原文首行 |
|---|---|
| T1 协议 | `internal/protocol/icmp_test.go:15:14: undefined: protocol.CapabilityStreamICMPEcho` |
| T2 引擎 | `internal/agent/icmp_echo_test.go:129:22: undefined: ICMPPacketConn` |
| 锁修正 | `WARNING: DATA RACE` / `Write at 0x00c000308190 by goroutine 154: internal/agent.(*Echoer).failAll() icmp_echo.go:360` / `--- FAIL: TestEchoerReadsItsFailureCauseUnderTheLock (0.04s)` |
| T3 流适配 | `internal/agent/icmp_stream_test.go:101:19: unknown field ICMPEcho in struct literal of type Dialer` |
| T5 配置 | `internal/config/config_test.go:218:13: streams.ICMPEnabled undefined (type config.AgentStreamConfig has no field or method ICMPEnabled)` |
| T4 通告 | `internal/agent/dial_executor_test.go:212:63: too many arguments in call to AgentStreamCapabilities` |
| T6 装配 | `internal/cli/agent_runtime_test.go:207:108: too many arguments in call to NewAgentDispatcherFactory` |
| T7 选择器 | `internal/server/connection_selector_test.go:105:14: undefined: streamProtocolCapability` |
| T8 门禁 | `internal/server/vpn_icmp_gate_test.go:35:17: undefined: probeAgentICMPEcho` |

T9 是文档与前端文案，不产生新行为，因此没有红灯；其守卫是既有的 `scripts/doc_claims_test.go`
（文档主张）、`internal/server/vpn_peer_openapi_test.go`（稳定码与路径集合）与前端 i18n 键对齐测试。

### 新增/修改的测试

| 文件 | 测试函数 | 覆盖 |
|---|---|---|
| `internal/protocol/icmp_test.go` | 6 | 请求/应答往返、`MaxDatagram` 上限、未知字段容忍、空 correlation 拒绝、非法状态拒绝、状态集合稳定 |
| `internal/agent/icmp_echo_test.go` | 12 | 内核改写 id 后仍正确关联、并发多路复用与乱序应答、payload 不匹配的伪造应答被丢弃、超时不泄露关联、超预算立即拒绝、未匹配与非 echo 数据报、关闭使所有在途 echo 失败、ctx 取消释放关联、非法目标、不可达作为应答、`ping_group_range` 缺失时的错误消息、**失败原因必须在锁内读取** |
| `internal/agent/icmp_stream_test.go` | 7 | nil 引擎明确拒绝、一请求一应答后 EOF（含 PolicyHook 以 `("icmp", host, 0)` 调用）、四类非法目标、单请求契约与非法数据报、能力未开启时 RESET 且不拨号、端口规则只对 ICMP 放宽、ack 独立驱动两个门禁 |
| `internal/agent/dial_executor_test.go` | +1 | 能力集合随“配置 × 就绪”变化（三种组合） |
| `internal/config/config_test.go` | +1 | 四个默认值、环境变量覆盖、三类非法值快速失败、关闭时不校验 |
| `internal/cli/agent_runtime_test.go` | +2 | 工厂通告能力并**把引擎真的装进 dialer**（端到端：开流 → 写请求 → 引擎发包 → 回应答 → DATA 帧带回 peer 自己的 identifier/sequence）、socket 被拒时不致命且配置正确传递 |
| `internal/server/connection_selector_test.go` | +2 | 协议 → 能力映射表、`tcp` 不再误杀通告了能力的会话、`icmp-echo` 命中唯一有能力的那条 |
| `internal/server/vpn_icmp_gate_test.go` | 2（含 7 个子测试） | 健康会话有能力 → Supported 且不查注册中心、健康会话无能力 → Unsupported、会话已关闭 → 回落集群、无任何连接 → Unsupported、只有本节点陈旧租约 → Unsupported、注册中心失败如实上报、什么都没装配 → fail closed；适配器**晚解析** session manager 与 cluster lister |
| `internal/server/vpn_peer_service_test.go` | +2（替换 1） | 签发七态：Supported 成功且审计 `verified`、Unverified 成功且审计 `unverified`、Unsupported 409、探针错误是内部错误而非能力判定、节点开关是上限且不询问探针、无探针 fail closed、不请求 ICMP 时不探测；`Update` 三态：无能力 409、unverified 成功且审计、改名不触发探测 |
| `internal/server/vpn_peer_api_test.go` | +1 | HTTP 层端到端：真实会话协商了能力 + 节点开关打开 → 201 且 `icmpEnabled: true` |

### 门禁执行结果

```bash
go test ./... -count=1                       # 全部 ok（protocol/agent/server/config/cli/e2e 等）
go test -race ./... -timeout 30m -count=1    # 全部 ok，无 DATA RACE
go vet ./...                                 # 无输出
gofmt -l internal scripts                    # 无输出
git diff --check                             # clean
go test ./scripts/ -count=1                  # ok（文档主张守卫）
cd web && npm test -- --run                  # 38 files / 323 tests 全绿
cd web && npm run build                      # 构建成功并同步 web_dist
bash scripts/verify-web-embed.sh             # web/dist 与 internal/server/web_dist 一致
python3 scripts/gen_doc_index.py             # 连续两次幂等，索引无变化
git status --porcelain go.mod go.sum migrations web/package.json web/package-lock.json   # 空
```

`internal/server` 41.2s、`internal/agent` 3.1s、`internal/cli` 1.3s、`internal/config` 0.6s、
`internal/protocol` 0.6s（`-count=1`）。分支上每个提交都单独 `go vet ./...` 通过（含测试文件），
因此任意提交点都可编译。

### 未执行的验证与原因

| 验证 | 状态 | 原因 |
|---|---|---|
| 真实 ping socket 收发 | **未执行** | 本机（macOS）与 CI 都不满足 `net.ipv4.ping_group_range`；`icmp.ListenPacket("udp4", …)` 会返回 EACCES。引擎以 `ICMPPacketConn` 接口注入测试，权限失败路径由 `listenICMPPacket` 变量替换覆盖，因此“缺 sysctl 时明确报错”是被测过的，但“真实内核改写 id 后仍能关联”只在假连接上验证过 |
| 端到端 `ping` 通 | **未执行** | 阶段 6 才有发起方：没有 TUN、没有 netstack、没有 UDP 51820 监听，没有任何进程会开 `icmp-echo` 流 |
| `test/e2e/vpn/` | **未创建** | 属阶段 8；规格 §14 要求环境不满足时 SKIP 且退出码 0 |
| Docker/Compose 验证 | **不适用** | 本阶段没有 Dockerfile、Compose 或部署产物变更 |
| 双方言迁移测与升级文档 | **不适用** | 无 Schema 变更 |

## 与计划的偏差（全部为收紧或事实更正，无功能缩水）

1. **T4 与 T5 的实施顺序对调**。计划 §7 把 T4（能力通告）排在 T5（Agent 配置）之前，
   但 T4 的实现要读 `streams.ICMPEnabled`，那是 T5 才引入的字段；计划 §10 的回滚顺序
   （T5 先于 T4 回滚）恰好反映了真实依赖。实施按 **T5 → T4** 执行，两个提交都独立编译并通过门禁。
2. **`AgentStreamCapabilities` 有两个调用点，不是一个**。计划 §3.3 记为“唯一调用点在
   `internal/cli/root.go:378`”，实测 `internal/e2e/socks5_web_page_latency_test.go:522` 也在调用。
   漏改会让 `go vet ./...` 与 `go test ./internal/e2e/` 编译失败。已修正，并通过
   `git commit --fixup` + `rebase --autosquash` 折叠进 T4 提交，保证 T4 单独 checkout 也可编译。
3. **多出一个提交 `fix(agent): read the echo failure under the lock`**。T2（`49fc462`）的 `Send`
   在解锁后调用 `failureLocked()`，违反其“调用方持 `e.mu`”的文档契约；读循环在 `failAll` 里
   持锁写同一字段。缺陷在 T2 时是潜伏的——只有 T3 让 echo 从自己的 goroutine 调用 `Send`
   之后，`-race` 才报出来。修在独立提交里，因为它改的是已提交的 T2 代码，与 T3 的文件集不相交。
4. **`readBack` 的缓冲区对 `icmp-echo` 按 `MaxDatagram` 分配**（`session.go`）。计划 T3 未列这一项，
   但默认 32 KiB 会截断一个由单个数据报构成的应答，而截断后没有第二次 Read 可以补齐。
5. **门禁实现的名字与形状**：计划把它叫 `clusterAgentICMPProbe`（一个类型）。实施拆成
   纯函数 `probeAgentICMPEcho(ctx, sessions, connections, localNodeID, agentID)` 加一个
   `clusterAgentICMPProbe` 适配器，因为 `SetVPN`（`runtime.go:164`）在
   `SetClusterAgentConnections`（`runtime.go:213`）**之前**执行：装配时捕获 lister 会冻结一个 nil，
   进程整个生命周期里所有 ICMP 签发都会 fail closed。适配器每次调用现取，纯函数则可以在没有
   API、数据库和注册中心的情况下被测。
6. **端口校验抽成 `validStreamTargetPort(proto, port)`**（计划描述为内联分支）。抽出来是因为
   “只有 `icmp-echo` 允许 0”这条规则需要独立可测，且 `Handle` 里的条件已经四个合取项。
7. **nil 探针 fail closed**（计划 D7 只覆盖了集群三态）。`AgentCapabilities` 为 nil 是装配错误，
   不是集群状态，因此判为 `Unsupported` 并给出独立消息（“本节点没有配置能力探针”）。
   生产装配总会注入（`SetVPN`），所以这条只在嵌入方漏接线时生效——而那正是应该拒绝的时候。
8. **`Update` 的探测顺序移到 `authorizeAgent` 之后**（计划未指定）。理由见“安全与授权影响”。
9. **T8 对阶段 4 测试的更正范围比计划小**。计划列出 `vpn_peer_service_test.go` 与
   `vpn_peer_api_test.go` 都要更正“一律 409”的断言；实测后者的 fixture 本来就把
   `server.vpn.icmp_enabled` 留空（= `false`），因此 409 仍然成立，只是**原因从“本版不支持”
   变成“节点开关关闭”**。断言保留、注释更正，另加一个走真实探针的 201 用例覆盖能力齐备的路径。
   前者的 `TestVPNPeerServiceICMPRequiresAnAgentCapability` 被七态用例替换。

## 顺带发现、本阶段刻意不修的既有问题

1. **`connection_selector.go` 的能力误比较**（已修，T7）。修前 `Select(agentID, "tcp")` 在任何
   通告了能力的会话上都落空。这是现网缺陷，不是本阶段引入的；修它是阶段 6 的前置条件，
   因为 VPN 出口开流要走同一条选路。
2. **集群级能力可见性缺失**（**技术债，推迟到阶段 8 评估**）。`registry.NodeOwner` 没有能力字段，
   `agent_connection_leases` 没有能力列，`agents.capabilities` 是**用户声明的标签**、
   不是运行时协商结果，挪用它会破坏单一数据源。因此跨节点的判定只能是 `Unverified`。
   要做成精确判定需二选一：
   - `agent_connection_leases` 增一列（Schema v16，MINOR，需 expand/contract 与双方言增量脚本）；
   - relay 增一个能力查询 RPC，照 `ClusterAgentConnectionCloseService`
     （`internal/server/agent_connection_api.go:82-140`）的 `dialRelayNode` + gRPC 范式。
   两者都不属于阶段 7 的最小闭环。当前取舍是 `Unverified` **放行**并在审计标注，
   理由见“安全与授权影响”与管理员指南：随机 409 比“签发成功、运行时按 `icmp_unsupported`
   拒绝并计数”更难排查。
3. **`docs/protocol/websocket.md:7` 的时点描述已过期**：它把 flow control 与 UDP association
   列为“后续协议增强项”，而两者早已落地。本阶段不改它——时点记录零改写，
   且更正它属于协议文档的整体复核，不该夹在一个 ICMP 变更里。
4. **`vpn_peer_service_test.go` 的 `newVPNServiceFixture` 每次都建新库**，因此状态化用例必须逐条
   重建 fixture。这不是缺陷，但让七态用例比看起来更长；抽象它属于测试基建重构，不在本阶段范围。

## 发布步骤

1. **先升级 Server，再升级 Agent**。顺序反过来也安全（旧 Server 不发 `icmp-echo`，
   新 Agent 的引擎闲置），但先升 Server 能让能力协商在 Agent 上线的那一刻就完成。
2. Server 侧无配置变更即可上线：`server.vpn.icmp_enabled` 默认 `true` 是**上限**，
   没有 Agent 通告能力时签发仍返回 409，行为与阶段 5 一致。
3. Agent 侧默认无变化：`agent.streams.icmp_enabled` 默认 `false`。要启用 ICMP 出口的部署按顺序执行：
   ```bash
   # 1) 主机一次性放开非特权 ping socket（可收窄到 Agent 运行 gid）
   sysctl -w net.ipv4.ping_group_range='0 2147483647'
   echo 'net.ipv4.ping_group_range = 0 2147483647' > /etc/sysctl.d/99-tunnelmesh-icmp.conf
   sysctl --system

   # 2) 打开 Agent 配置
   #    agent.streams.icmp_enabled: true

   # 3) 校验后重启
   tunnelmesh-agent check-config
   ```
   启动后确认 stderr **没有** `agent icmp echo is unavailable`，
   并在管理台确认签发 ICMP peer 不再返回 409。
4. **不要对外宣布 `ping` 可用**：数据面在阶段 6，本阶段结束后仍然 ping 不通。
5. 灰度顺序：先在一台 Agent 上开启，观察 `agent icmp echo is unavailable` 与签发审计里的
   `icmpCapability`；确认稳定后再铺开。

## 回滚步骤

- **不回滚代码即可止损**：把 `agent.streams.icmp_enabled` 改回 `false` 并重启 Agent。
  ping socket 关闭、能力不再通告、Server 侧门禁自动退回 409。5 分钟内可完成，
  不涉及数据库、防火墙或证书动作。
- **代码回滚顺序**：T9 → T8 → T7 → T6 → T4 → T5 → T3 → 锁修正 → T2 → T1。
  T1 必须最后回滚（`protocol` 常量与编解码被 T2/T3/T7/T8 全部引用），单独回滚 T1 会编译失败。
- **能力是协商出来的，因此两端可以独立回滚**：只回滚 Agent → 旧 Agent 不通告
  `stream_icmp_echo.v1`，Server 门禁自动判 `Unsupported` → 409，无需同时回滚 Server；
  只回滚 Server → 旧 Server 不发 `icmp-echo`，新 Agent 的引擎闲置。
- **回滚 T7 需要单独评估**：它是既有缺陷修复，回滚会让 `Select(agentID, "tcp")` 重新误杀
  通告了能力的会话，即 tp-* 与 WebSSH 的选路回到缺陷状态。回滚前必须确认没有其他路径依赖它。
- **数据无需迁移**：本阶段没有 Schema 变更。已签发的 ICMP peer 行保持 `icmp_enabled=1`，
  回滚后只是不能再新增或改回 `true`。
- **审计的新键可容忍**：`icmpCapability` 回滚后旧代码不再写；控制台按 JSON 详情原样渲染，
  对未知键与缺失键都容忍。

## Reviewer 关注点

1. **`icmpEchoStream` 为什么自建 context**（`internal/agent/dialer.go`）。
   `DialExecutor.worker` 在 `e.dial(ctx, …)` 返回后立刻 `cancel()`，而 echo 按设计要比拨号活得久。
   借用传入的 ctx 会让每个 echo 在发出前就被取消。`Close()` 是唯一取消点。
2. **关联为什么用 wire sequence 而不是 ICMP id**（`internal/agent/icmp_echo.go`）。
   非特权 ping socket 下内核改写 id；一个进程只有一个 socket 却要服务多条并发 echo。
   payload 逐字节比对是防序号回绕撞上旧关联的第二道锁。
3. **超时/超预算/不可达为什么是应答而不是错误**。IP 没有错误通道；把它们变成错误会让
   peer 的 `ping` 挂着而不是收到一个答复。状态是闭合枚举，阶段 6 直接映射 `error_class`。
4. **门禁默认关闭是否覆盖所有路径**（`internal/agent/session.go` 的 `Handle`）。
   拒绝发生在 `d.mu.Lock()` 内、重复流检查之前，读的是锁内的 `d.icmpEcho`；
   `rejectStreamMode` 按 strict 标志分别发 `OPEN_RESULT` 或 `RESET`。
5. **`Unverified` 放行是否可接受**（`internal/server/vpn_icmp_gate.go`）。
   这是本阶段最大的判断，理由与替代方案都写在 D7 与管理员指南里；
   审计的 `icmpCapability` 是它的可观测补偿。若 Reviewer 认为应当拒绝，
   改动点只有 `authorizeICMP` 的一个 case，但请同时接受“签发随请求落点随机失败”的后果。
6. **T7 的行为变化面**。修复后本地会话重新参与选路，远端 relay 回落的触发条件变严。
   `internal/e2e` 全绿说明既有链路没有依赖旧行为，但这是本 PR 影响面最广的一处，
   建议单独复核 `connection_selector_test.go` 的新增两用例。
7. **`readBack` 缓冲区**。`icmp-echo` 按 `MaxDatagram`（64 KiB）分配，UDP 仍按 `MaxPayload`（1 MiB），
   其余协议仍按 32 KiB。三档而不是两档，是因为一个 ICMP 应答由单个数据报构成，
   截断后没有第二次 Read 可以补齐。
8. **探针的晚解析**。`clusterAgentICMPProbe` 每次调用现取 `a.agentSessions` 与
   `a.clusterConnections`。若将来 `SetVPN` 与 `SetClusterAgentConnections` 的调用顺序被调整，
   `TestAPIAgentICMPProbeResolvesClusterStateLate` 会失败，这是有意的守卫。

## 集成状态

- 分支：`codex/vpn-phase7-agent-icmp`（11 个提交，stacked 在已推送的 `codex/vpn-phase5-admin-console` 之上）
- 变更规模：34 个文件，+3393 / −59
- 门禁：`go test ./... -count=1`、`go test -race ./... -timeout 30m -count=1`、`go vet ./...`、
  `gofmt -l internal scripts`、`git diff --check`、`go test ./scripts/ -count=1`、
  `npm test -- --run`（38/323）、`npm run build`、`scripts/verify-web-embed.sh`、
  `scripts/gen_doc_index.py` 两次幂等、docs 相对链接死链核验（none）——全部通过
- 零改动守卫：`go.mod`、`go.sum`、`migrations/`、`SchemaVersion`、OpenAPI 路径集合、
  10 个稳定错误码集合、`web/package.json`、`web/package-lock.json`
- 时点记录：阶段 4/5 的计划与 PR 记录只链接、未修改；对它们的事实更正只写在本文件
- 后续阶段：阶段 6（Server 数据面）消费本阶段的 `streamProtocolCapability` 选路、
  `VPNAgentCapabilityProbe` 接口与 `protocol.ICMPEcho*` 编解码；
  阶段 8（可观测性与部署）承接集群能力可见性技术债、Prometheus 指标、`test/e2e/vpn/` 与部署文档
