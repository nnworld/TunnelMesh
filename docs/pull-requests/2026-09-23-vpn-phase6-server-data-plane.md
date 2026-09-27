# VPN 网关 阶段 6：Server 数据面

## 标题

`feat(server): run the vpn gateway lifecycle`

本记录覆盖分支上阶段 6 的全部 16 个提交（计划 → 构建隔离 → 节点身份 → 内存 TUN → netstack TCP 终结 →
包解析 → 流表与限速与拒绝聚合 → peer 热更新 → UDP 中继 → ICMP 中继 → TCP 中继 → WireGuard 端点 →
网关生命周期 → flows 与节点状态 → 端到端矩阵 → 文档），而不只是标题对应的那一个。

## 目标分支

`main`

本分支 stacked 在 `codex/vpn-phase7-agent-icmp` 之上。阶段 7 及更早阶段的 PR 合并进 `main` 后，
必须先 `git rebase --onto main codex/vpn-phase7-agent-icmp` 再更新本 PR，
否则 diff 会重复包含阶段 7 的产物（Agent ICMP echo 引擎、能力协商与签发门禁）。

## 关联记录

- 实施计划：[VPN 网关 阶段 6：Server 数据面 Implementation Plan](../superpowers/plans/2026-09-23-vpn-phase6-server-data-plane.md)
- 设计规格：[内嵌 VPN 网关（WireGuard）设计](../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md)（§5.2 数据面、§5.4 依赖与构建隔离、§8 配置项、§12.2 数据面错误语义、§13 可观测性、§14 测试策略、§15 阶段 6、§18 验收 1-6）
- 决策载体：[ADR 0002: Open a public UDP ingress for an embedded VPN gateway](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)
- 前置阶段：[阶段 7：Agent ICMP echo 与能力协商](2026-09-23-vpn-phase7-agent-icmp.md)、[阶段 5：管理台 VPN 页](2026-09-22-vpn-phase5-admin-console.md)、[阶段 4：纯逻辑包与管理 API](2026-09-21-vpn-phase4-pure-logic-and-api.md)、[阶段 3：Schema v15](2026-09-20-vpn-phase3-schema-v15.md)、[阶段 1：约束反转](2026-09-20-vpn-phase1-constraint-reversal.md)、[Task 0 可行性](2026-09-19-vpn-task0-feasibility.md)
- 本阶段新增文档：[VPN 使用者帮助](../user-guide/vpn.md)、[VPN 网关部署](../deployment/vpn-gateway.md)、[VPN 网关运维](../operations/vpn.md)
- 本阶段更新的活文档：[架构概览](../architecture/overview.md)、[配置说明](../operations/configuration.md)、[配置示例](../operations/config-examples.md)、[Server 管理后台](../user-guide/server-admin.md)、[文档索引](../README.md)

## 摘要

阶段 6 把 Server 从“能签发 WireGuard peer 但承载不了一个包”变成“真的持有一个公网 UDP 端点并中继流量”。
数据面全部在 `//go:build vpn` 后面：不带 tag 的二进制的依赖图里没有 gVisor 也没有 wireguard-go，
隔离由构建保证而不是靠代码评审。

一条包的路径是：`vpnBind`（只绑 `listen` 写明的 host，仅 IPv4）→ wireguard-go device（Noise 握手、
密钥、重放保护、按 peer 的 `AllowedIPs` 过滤源地址）→ `memoryDevice`（进程内 TUN，不开
`/dev/net/tun`，入向先复制再交给管线）→ `handlePacket` 七步逐包决策 → 按协议分派到 TCP / UDP /
ICMP-echo 三条腿 → 三条腿都经 `relay.NodeTransport.OpenStream`，所以集群模式下出口 Agent 挂在别的
Server 节点上时自动走既有 relay。

三处设计决定值得单独说：

1. **监听器在 `InjectInbound` 之前同步注册**（D4）。 demux 必然命中，首个 SYN 不被丢弃，
   用户不会白等一次客户端 RTO。这消除了 Task 0 第 7 项与 ADR 0002 Consequences 里那条延迟惩罚。
2. **拒绝一律静默**。不回 RST、不回 ICMP 错误：回一个不可达就等于承认该地址存在，
   而那正是出口策略要隐藏的事实。代价是 IP 层没有错误通道，用户侧只能看到“连不通”，
   排障必须在服务端做——这一取舍写进了用户文档。
3. **未匹配的 TCP 段被消费而不是返回 false**。返回 false 会交给 gVisor 的 unknown-destination 路径，
   它会回 RST，与第 2 条冲突。

## 用户影响

- **不带 `-tags vpn` 的发行二进制：零变化**。默认构建的依赖图与行为与阶段 5 完全一致，
  管理面照旧，`config:reveal` 照旧返回 409 `vpn_node_disabled`。
- **带 tag 且 `enabled: false`：零变化**。进程不创建任何 VPN 资源，不读
  `TUNNELMESH_VPN_NODE_PRIVATE_KEY`。
- **带 tag 且 `enabled: true`**：进程真的监听 `server.vpn.listen` 指定的公网 UDP 端口，
  `config:reveal` 开始返回可用的 wg-quick 配置，`flows` 与 `vpn-nodes` 两个接口开始返回真实数据，
  控制台的活跃流抽屉与 IP 池水位从“本版未提供”变成实际数字。
- **不带 tag 却配 `enabled: true`：启动失败**，报
  `server runtime: vpn gateway: this binary was built without VPN support; rebuild with -tags vpn`。
  这是刻意的：一个不含数据面的进程如果照常报告 ready，用户看到的会是“配置导入成功但全部超时”，
  而线索离构建方式很远。
- 能力边界不变，仍是规格 §4.3 那五条（源地址不保留、仅 ICMP echo、仅三种 IP 协议、不支持分片、
  Agent 不能主动发起），现在**逐条写进了用户文档**而不是只在规格里。

## API、Schema 与配置影响

### API

无新增路径。阶段 5 注册的两个 501 路由改为真实应答，OpenAPI 同步：

- `GET /api/v1/vpn-peers/{peerId}/flows`：从 `501 vpn_not_implemented` 改为读网关内存。
  可见性先解析（不存在与不属于你都是 404，路由不能被用来探测标识符），再问网关。
  **本节点不服务该 peer 时仍答 501 而不是空列表**：“没有流量”和“流量在别的节点”是两个事实，
  只有后者能告诉运维该去哪儿看。无 cursor，因为单 peer 的流集受 `max_flows_per_peer` 约束，
  分两页读会描述两个不同时刻。
- `GET /api/v1/vpn-nodes`：两种构建都答 200。无网关的节点报告 `enabled: false` 并省略计数器
  （控制台渲染成 em dash）。这里答 501 会在每一个不带 vpn tag 的 Server 上读作
  “整个集群没有节点提供 VPN”，那是一个进程无法作出的关于集群的断言。
  `allocated`/`capacity` 读自子网租约与 peer 表，不从前缀推算——推算出来的容量是签发方未必能兑现的承诺。
  计数器读库失败时返回 500 而不是省略字段：缺失读作“未知”，而数据库慢不是未知。
- `openapi.yaml` 移除两个 `x-tunnelmesh-phase` 标记，新增 `VPNFlowListEnvelope`/`VPNNodeListEnvelope`，
  并更正 `icmpEnabled` 描述里“ICMP 尚未实现”的过时主张。守卫测试的期望值从 2 改成 0。

### Schema

**无变更**。`migrations/`、`SchemaVersion`、增量链零改动，双方言迁移测不适用。
数据面没有持久化状态：流表、监听器注册表、peer 内存视图、拒绝聚合器全在内存。
数据库仍是权威来源，内存视图可以由重启重建。

### 配置

无新增键。`server.vpn.*` 全部 15 个键在阶段 4 已定义，本阶段让它们**真的被消费**。
两处语义变化写进了配置文档：

- `TUNNELMESH_VPN_NODE_PRIVATE_KEY` 从“本版还不读取”改为已消费，并给出两条启动失败原文。
  base64 与 64 字符 hex 都接受（`wg(8)` 打 base64，多数 keygen 一行命令打 hex，只认一种会让操作员
  拿到一条无法据以行动的报错）。
- `server.vpn.listen` 的 host 部分现在是**真的绑定地址**，不再是“反正上游监听所有接口”。
  上游 wireguard-go 的默认 bind 监听 `":"+port`，网关换成了自己的 bind。

### 协议

无新 frame。本阶段是阶段 7 定义的 `protocol.ICMPEcho*` 编解码与 `stream_icmp_echo.v1` 能力的**第一个消费方**；
TCP/UDP 走既有的 `OpenStream` 协议，未改动状态机。

## 安全与授权影响

- **节点私钥只走环境变量**，不进配置文件、不进日志、不进审计、不进指标。
  节点身份在 `startVPNGateway` 内部读取而不是作为 `VPNGatewayDeps` 字段传递，
  这样没有任何装配路径能忘记它，私钥也不会经过一个可能被日志打印、被渲染进支持包、
  或被测试失败消息比较的结构体。
- **peer 私钥仍只以 AES-256-GCM 密文入库**，reveal 是全产品唯一一处明文出现的地方，
  配 `Cache-Control: no-store`、显式确认头、`Idempotency-Key` 与审计。reveal 刻意不走幂等存储：
  那个存储持久化响应体，而这一条携带密钥材料。
- **策略逐包执行，不在流建立时缓存**。peer 可以在连接存续期间被打补丁、被吊销、到期，
  缓存在流上的决定会把这三种情况全部继续服务下去。
- **指标标签基数受约束**。`error_class` 是 16 个已发布常量之一，这正是它能做 Prometheus 标签的原因；
  peer ID、VPN IP、目标地址一律不是标签，它们是审计字段。
- **审计聚合有双上限**：窗口 1 分钟，活跃桶 4096。桶数触顶时新桶被丢弃而不是无限增长，
  此时指标仍然精确，只有审计粒度退化。目标地址按 `/24` 聚合，一个扫 256 个地址的 peer
  产生一条审计而不是 256 条。
- **入向包在交给管线前复制**。wireguard-go 在 `Write` 返回的瞬间把消息缓冲区还给池，
  持有原切片的 handler 会读到另一个包正在被解密进去的内存——这是 `-race` 不一定能稳定抓到的损坏。
- **device 的错误日志按每秒一条限流并携带被抑制条数**。device 对每个无法处理的数据报报一次错，
  而这些数据报来自公网：不限流等于让任何能到达 UDP 端口的主机灌满进程日志。
- **不需要 `CAP_NET_ADMIN`，也不需要 `CAP_NET_RAW`**。TUN 是进程内内存设备，`File()` 返回 nil；
  ICMP echo 由 Agent 侧的非特权 ping socket 发出（阶段 7）。
- **未匹配的 TCP 段被消费**，不交给 gVisor 的 unknown-destination 路径回 RST。

## 测试证据

### 红灯（每个任务实测，非推断）

| 任务 | 红灯原文首行 |
|---|---|
| T1 构建隔离 | `internal/vpn/nodekey_test.go:17:15: undefined: vpn.NodePrivateKeyEnv` ／ `internal/server/vpn_runtime_stub_test.go:18:5: undefined: vpnBuildTagged` |
| T2 节点身份 | `internal/vpn/uapi_test.go:43:19: undefined: vpn.EncodeKeyHex` ／ `internal/server/runtime.go:164:54: undefined: vpnNodePublicKey` |
| T3 内存 TUN | `internal/server/vpn_device_test.go:89:62: undefined: memoryDevice` |
| T4 netstack TCP 终结 | `internal/server/vpn_stack_test.go:175:41: undefined: vpnStack` |
| T5 包解析 | `internal/server/vpn_wire_test.go:59:17: undefined: parseVPNWirePacket` |
| T6 流表/限速/拒绝聚合 | `internal/server/vpn_denials_test.go:48:22: undefined: vpnPacketDeniedAction` |
| T7 peer 热更新 | `internal/server/vpn_peers_test.go:25:13: undefined: vpnPeerEntry` |
| T8 UDP 中继 | `internal/server/vpn_packet_test.go:218:27: gateway.device undefined (type *vpnGateway has no field or method device)` ／ `internal/observability/metrics_test.go:108: NormalizeProtocol(icmp-echo) = "unknown", want "icmp-echo"` |
| T9 ICMP 中继 | `internal/server/vpn_icmp_test.go:34:48: undefined: vpnICMPRelay` ／ `internal/server/vpn_icmp_test.go:545:13: undefined: buildIPv4ICMPEchoReply` |
| T10 TCP 中继 | `internal/server/vpn_tcp_test.go:29:47: undefined: vpnTCPRelay` |
| T11 WireGuard 端点 | `internal/server/vpn_bind_test.go:30:49: undefined: vpnBind` ／ `internal/vpn/uapi_test.go:82:18: undefined: vpn.RenderNodeIdentityUAPI` |
| T12 生命周期 | tagged：`internal/server/vpn_lifecycle_test.go:132:18: fixture.gateway.leaseRenewInterval undefined (type *vpnGateway has no field or method leaseRenewInterval)`；default：`internal/server/vpn_runtime_stub_test.go:100:13: runtime.vpnGateway undefined (type *ServerRuntime has no field or method vpnGateway)` |
| T13 flows 与节点状态 | Go：`typechecking failed with "api.setVPNNodeView undefined"`；web：`AssertionError: expected <div data-v-117905c8 ...(2)>...(2)</div> to be null` ／ `Error: missing button vpn.flowsOpen` ／ `AssertionError: expected '<template>...' to contain 'listVpnPeerFlows'` |
| T14 端到端矩阵 | `vpn_e2e_test.go:880: the peer received no echo reply: i/o timeout; every drop was []` ／ `vpn_e2e_test.go:827: the connection took 474.09975ms to establish, want under 200ms, so the first segment was dropped and retransmitted`（后者在 `-race` 下 10/10 复现） |

T15 是文档与体积实测，不产生新行为，因此没有红灯；其守卫是既有的 `scripts/doc_claims_test.go`
（文档主张反转守卫）、`internal/server/vpn_peer_openapi_test.go`（稳定码与路径集合）、
前端 i18n 键对齐测试，以及本阶段新增执行的相对链接死链核验。

T14 的两处红灯都**不是网关缺陷**，且都是被证明而不是被假设的：

- echo reply 按 `buildIPv4ICMPEchoReply` 的原样字节喂进一个只含一个 ping socket 的独立 gVisor netstack。
  读取方在注入前就阻塞时它读得到，在注入后才开始读时超时。wireguard-go 的 netstack `PingConn`
  在 `ReadFrom` 内部注册 waiter，而 gVisor 的 wait queue 不会把事件重放给注册晚于事件的 waiter，
  所以先到的 reply 被排进 socket 却再也不会被通告。gonet 的 TCP 与 UDP socket 先尝试读再等待，
  这就是为什么只有 ICMP 这一例暴露它。用例现在在 echo 发出前就把读取方停好——真实 ping 的顺序。
- 计划规定的 200ms 首包界在竞态检测器下达不到：同一次拨号在 10 次 `-race` 运行里测得 201–492ms，
  不带检测器时 20–40ms。这个界报告的是机器而不是代码，而计划自己的门禁要跑 `-race`。
  现在拆成两条断言：与负载无关的那条直接命名 D4 的机制（找不到监听器的段会落到栈的默认处理器并被计数，
  所以计数必须为 0）；时间那条仍覆盖“首段在栈之上丢失”这一默认处理器看不见的情况，
  界取 700ms——高于全部实测区间，低于 gVisor 的 1 秒初始 RTO 300ms，而后者才是它成为证据的原因。

### 新增/修改的测试

| 文件 | 覆盖 |
|---|---|
| `internal/vpn/nodekey_test.go` | base64 与 hex 两种拼写、全零标量拒绝、非 x25519 标量拒绝、空值拒绝、公钥派生一致 |
| `internal/vpn/uapi_test.go` | 节点/节点身份/peer/peer 移除四份 UAPI 渲染的 golden 文件（`internal/vpn/testdata/`）、`listen_port=0` 的两种读法分歧 |
| `internal/server/vpn_runtime_test.go`、`vpn_runtime_stub_test.go`、`vpn_runtime_vpn_test.go` | 装配点的三条分支：`enabled:false` 返回 nil plane、无 tag 拒绝并给出重建提示、有 tag 时缺密钥与配置非法都在启动失败；stub 与 tagged 两侧各自守卫 |
| `internal/server/vpn_device_test.go` | 内存 TUN 的构造校验、入向复制、出向有界队列满时丢弃并计数、`injectTCP` 只接受 TCP、`emit` 绕过栈、关闭后读写报 `os.ErrClosed` |
| `internal/server/vpn_stack_test.go` | 监听器同步注册使首个 SYN 命中 demux（`unmatchedTCP()==0`）、引用计数与最后一次释放归还地址、重复地址按不变量破裂处理且不 `RemoveAddress`、默认处理器消费而不回 RST |
| `internal/server/vpn_wire_test.go` | IPv4 头解析、三种协议的传输头、非首片识别、UDP 长度与实际字节不一致时以字节为准、畸形包分类 |
| `internal/server/vpn_flows_test.go`、`vpn_ratelimit_test.go`、`vpn_denials_test.go` | 流表按四元组与按 (identifier, sequence) 去重、并发上限取更严者、令牌桶、聚合窗口滚动、桶数触顶、审计写入失败不吞计数 |
| `internal/server/vpn_peers_test.go` | 热更新 apply/remove、按源 IP 反查、吊销与到期的分类、不属于本节点的行被拒绝加载并计数 |
| `internal/server/vpn_udp_test.go` | association 生命周期、回程数据报的 IPv4 头与 UDP 校验和、数据报边界、超时回收 |
| `internal/server/vpn_icmp_test.go` | echo 关联、identifier/sequence 还原、校验和、六类 agent 应答状态到 `error_class` 的映射、correlation 不匹配拒绝、`abort` 的四条触发路径、并发名额释放 |
| `internal/server/vpn_tcp_test.go` | 双向字节一致、accept 泵、pump 退役用 CAS 而非 `sync.Once`、`ensurePump` 失败不双计指标、空闲回收不早于 netstack 的 TIME_WAIT |
| `internal/server/vpn_bind_test.go` | 只绑指定 host、仅 IPv4、`SetMark(0)` 成功而非 0 报不支持、批量大小恒为 1 |
| `internal/server/vpn_lifecycle_test.go` | `Start` 失败即整体 `Close`、drain 步骤顺序、`shutdown_timeout` 超时强制断流并报告 `flows_forced`、租约续租间隔为 TTL/3、stale epoch 时放弃续租但保留存量 peer 与流、后台循环停止超时只记日志 |
| `internal/server/vpn_gateway_test.go` | 网关首次遇到真实 WireGuard peer：握手、peer 集装载、热更新落到 device、吊销后 key 从 device 消失、轮换后旧 key 不再被应答、重启后 peer 集恢复、不属于本节点的行不被安装 |
| `internal/server/vpn_e2e_test.go` | 规格 §18.1–§18.6 的端到端矩阵，见下 |
| `internal/server/vpn_peer_api_test.go`、`vpn_peer_openapi_test.go` | flows 与 vpn-nodes 的未授权、越权、非本节点、幂等与错误响应；OpenAPI 的 `x-tunnelmesh-phase` 计数守卫从 2 改为 0 |
| `internal/observability/metrics_test.go` | `NormalizeProtocol("icmp-echo")` 不再退化成 `unknown` |
| `scripts/vpn_build_tag_test.go` | 依赖隔离守卫：默认构建的依赖图里没有 gVisor 与 wireguard-go |
| `web/src/tests/vpn.spec.ts` | 活跃流抽屉真的调用 `listVpnPeerFlows` 并渲染、501 渲染成 info 提示而不是空表格、`vpn.dataPlanePending` 断言删除、中英 i18n 键对齐 |

端到端矩阵（`vpn_e2e_test.go`，8 个用例）用**真实的上游 wireguard-go 客户端**经回环 UDP 连到被测网关，
两端都是真实现，只有中间的网络是 loopback：

| 用例 | 断言 |
|---|---|
| 握手给出隧道地址 | peer 用自己拿到的 key 完成 Noise 握手，网关的 `allowed_ip` 是它的 `/32`，endpoint 学到回环地址 |
| TCP 往返保留首段 | 双向字节一致；`unmatchedTCP()==0` 且拨号在 700ms 内完成（D4 正向证据）；同一次往返在 flows 接口里可见 |
| UDP 往返保留四元组 | 已连接 UDP socket 只接受源为对端、目的为本端地址与端口的数据报，所以读到的回程必然是地址、端口、校验和都对的 |
| ICMP echo 往返 | agent 收到的目标端口为 0（echo 没有端口，编一个数字去匹配 TCP/UDP 是不诚实的）、identifier 非 0；回程的 type=0、identifier 是网关交给 agent 的那个、sequence 与载荷是 peer 自己的 |
| 吊销结束流并退役密钥 | 流被拆、key 从 device 消失、用该 key 的握手在具名窗口内不被应答 |
| 轮换使旧身份失效 | 新 key 可握手，旧 key 在具名窗口内不能 |
| 未授权目标不可达且有审计 | 三类拒绝（`target_denied`／`metadata_denied`／`port_denied`）各自计数，审计里是聚合后的 `vpn_packet_denied` 且归属正确的 peer |
| 不支持的数据报被计数且不被应答 | GRE（IP 协议 47）与非首片分片各计一次；等一个具名窗口后客户端收到的回程包数为 0 |

后两类由 `vpnRawWireClient` 发送——它仍是一个真实 Noise 会话，只是隧道接口由测试持有。
存在的原因是主机栈产生不了这两种数据报：gVisor 的 netstack 既不发起 GRE，也不会在测试要求时
发出一个非首片分片。把它们在加密层之下注入只会证明 `handlePacket` 的行为，证明不了隧道。

### 门禁执行结果

```bash
go build ./...                                          # ok
go build -tags vpn ./...                                # ok
go test ./... -count=1                                  # 23 个包全 ok（internal/server 45.8s）
go test -tags vpn ./... -count=1                        # 23 个包全 ok（internal/server 55.7s）
go test -race ./... -timeout 30m -count=1               # exit 0，23 个包全 ok，无 DATA RACE（internal/server 490.6s）
go test -tags vpn -race ./... -timeout 30m -count=1     # exit 0，23 个包全 ok，无 DATA RACE（internal/server 559.5s）
go vet ./...                                            # 无输出
go vet -tags vpn ./...                                  # 无输出
gofmt -l internal scripts                               # 无输出
git diff --check                                        # clean
go test ./scripts/ -count=1                             # ok（文档主张守卫、安装脚本守卫、build tag 守卫）

# 构建隔离（规格 §14 的门禁命令）
go list -deps ./cmd/tunnelmesh-server | grep -Ec 'gvisor|golang.zx2c4.com/wireguard'   # 0
go list -deps -tags vpn ./cmd/tunnelmesh-server | grep -Ec 'gvisor'                    # 40

# 端到端矩阵稳定性（Task 0 的「连续 3 次」范式）
go test -tags vpn ./internal/server/ -run VPNE2E -count=1 -v                    # 8/8 pass
go test -tags vpn ./internal/server/ -run 'VPNGateway|VPNE2E' -count=3 -race    # ok 60.4s

# 前端
cd web && npm test -- --run              # 38 files / 327 tests 全绿
cd web && npm run build                  # 构建成功并 sync 到 internal/server/web_dist
bash scripts/verify-web-embed.sh         # web/dist 与 internal/server/web_dist 一致

# 文档
python3 scripts/gen_doc_index.py         # 连续两次幂等，四个索引无变化
git status --porcelain docs              # 只有本阶段的 3 个新文档与 5 个活文档更新

# 零改动守卫
git diff --stat codex/vpn-phase7-agent-icmp..HEAD -- go.mod go.sum   # go.mod +5 / go.sum +10，全部是计划内的依赖行
git status --porcelain migrations web/package.json web/package-lock.json Dockerfile   # 空
```

### 二进制体积实测

计划 Task 0 要求实测、不得推测。本机 `Darwin arm64`、`go version go1.27.1 darwin/arm64`，
未 strip、未加 `-ldflags "-s -w"`：

```bash
go build -o /tmp/tm-server-notag ./cmd/tunnelmesh-server && stat -f%z /tmp/tm-server-notag
go build -tags vpn -o /tmp/tm-server-vpn ./cmd/tunnelmesh-server && stat -f%z /tmp/tm-server-vpn
```

| 构建 | 字节数 |
|---|---|
| 默认（不含数据面） | 39,822,754 |
| `-tags vpn`（含数据面） | 45,243,154 |
| 增量 | **+5,420,400 字节（5,420,400 / 1,048,576 ≈ 5.17 MiB，相对默认构建 +13.6%）** |

这是 darwin/arm64 的单点实测，不是跨平台结论；linux/amd64 的数值需要在那台机器上重测。
发行构建矩阵当前**不含** `-tags vpn`，需要数据面的部署自行构建。

### 未执行的验证与原因

| 验证 | 状态 | 原因 |
|---|---|---|
| 容器内非 root 运行 | **未在 Linux 取证** | 本机是 macOS，没有 Linux 容器运行时。主张由代码结构支撑（`memoryDevice` 不打开 `/dev/net/tun`，`File()` 返回 nil，`vpnBind` 只用普通 UDP socket），但**没有实测**，不得写成已通过 |
| “不需要 `CAP_NET_ADMIN`” | **未在 Linux 取证** | 同上。Task 0 的第 8 项把这条列为 Linux 取证门禁项，本阶段环境不满足 |
| 真实 `wg` / `wg-quick` 客户端接入 | **未执行** | 属阶段 8 的 `test/e2e/vpn/`。本阶段的 wire peer 是上游 wireguard-go 的 in-memory TUN：两端都是真实现，Noise 握手与加解密都是真的，但没有跑过内核 TUN 与真实客户端 GUI |
| Linux 上 gVisor / wireguard-go 的行为差异 | **未取证** | 本机 macOS。内存 TUN 与用户态栈不依赖内核网络特性，因此差异面小，但未经 Linux 实测 |
| Docker / Compose 验证 | **不适用** | 本阶段 `Dockerfile` 与 compose 文件零改动（见「顺带发现」里 `GO_VERSION` 落后一条，属阶段 8） |
| 双方言迁移测与升级文档 | **不适用** | 无 Schema 变更 |
| 多节点集群下的跨节点 relay 实测 | **未执行** | 端到端矩阵的 opener 是本进程内的假 Agent transport，覆盖的是 `OpenStream` 契约而不是集群选路。集群选路由既有 `internal/e2e` 覆盖，本阶段未新增用例 |

## 与计划的偏差（全部为收紧或事实更正，无功能缩水）

各任务的偏差在对应提交的 body 里逐条记录，这里按任务汇总要点。

1. **T3/T4**：`newMemoryDevice` 的入向 handler 是**必填构造参数**而不是 setter。一个能不带策略钩子
   构造出来的设备会编译通过、启动成功、然后静默跳过每一项 peer/限速/目标检查。
   `newVPNStack` 的签名是 `(device, metrics)` 而不是计划里的 `(mtu)`：栈复用设备自己的 link endpoint，
   而不是自己再造一个。
2. **T7**：peer 视图的配置以结构体承载而不是散参数。
3. **T8**：并发流上限取 peer 与节点两者中**更严**的一个；非 echo 的 ICMP 在管线层就拒绝
   （网关承诺绝不发出不是自己构造的 ICMP，代答 TTL 超时或不可达就等于替 peer 生成一个）；
   `NormalizeProtocol` 的归一化提前，`icmp-echo` 不再退化成 `unknown`。
4. **T10**：三个协议 handler 的签名统一为 `(ctx, peer, parsed, packet)`，让 `dispatch` 只有一种调用形状；
   `vpn_stack_test.go` 的 `vpnTCPSegment` 夹具原来只算伪首部与 TCP 头的校验和、漏掉 payload，
   带数据的段会被 netstack 静默丢弃——已修，否则 T10 的双向字节断言会以错误的原因通过；
   `reapIdle` 增加 2 分钟回收下限，不早于 netstack 自身的 TIME_WAIT 回收端口；
   pump 退役用 `claimed atomic.Bool` 的 CAS 取代 `sync.Once`（`Once` 只保证执行一次，
   不保证执行者是引用的持有者，`-race` 下已退役的 pump 会释放新 pump 持有的引用并关掉别人的监听器）；
   新增 `denyAudited`，`ensurePump` 失败时只补审计不再计一次指标，避免同一事件双计。
5. **T11**：新增 `vpn.RenderNodeIdentityUAPI`（只渲染 `private_key` 块）。计划让 T11 直接消费 T2 的
   `RenderNodeUAPI`，但它拒绝 `listen_port=0`（既有测试明确断言，因为 `wg(8)` 把 0 读作“停止监听”），
   而 `config.ValidateVPN` 接受 `listen: host:0` 且 wireguard-go 的 `BindUpdate` 把 0 读作“让内核选一个”。
   省略该行是两种读法唯一都成立的写法，因此按端口是否为 0 分两条渲染路径，而不是放宽既有契约。
   另外：`Start` 必须显式 `device.Up()`（wireguard-go 只为已 up 的 device 打开 bind，而 `memoryDevice`
   不像上游 netstack TUN 那样发 `tun.EventUp`）；`Start` 失败即整体 `Close`，不留半启动状态；
   `SetMark(0)` 返回 nil、非 0 返回不支持错误（谎称设置成功会让操作员以为一条路由策略在生效）；
   device 的 `Errorf` 按每秒一条限流并携带被抑制条数。
6. **T12**：`Start`/`serve`/`Close`/`release`/`isClosed` 从 `vpn_gateway.go` 移进新的 `vpn_lifecycle.go`
   （这才是文件清单给它的职责），`vpn_gateway.go` 保留装配与 peer/device 管理；drain 步骤是
   `beginDrain` 而不是 `Drain`（`vpnGateway` 未导出，teardown 之外没有调用方，导出名会宣传一个不存在的 API）。
7. **T13**：`/vpn-nodes` 不加管理员门禁（它报告的是本进程的节点视图，与 `dashboard` 同级）；
   `NodeStatus` 读库失败返回 500 而不是省略计数器；`flows` 不带 cursor；`icmpEnabled` 描述更正；
   前端指标文案从四个从未注册的 `tunnelmesh_vpn_*` 改成实际存在的三个向量；
   删除 `vpn.dataPlanePending` 而不是留着不渲染（一个活过“能承载流量”这次发布的键会被下一条横幅复用）。
8. **T14**：矩阵放在新文件 `vpn_e2e_test.go` 而不是扩展 `vpn_gateway_test.go`
   （后者已经是“网关遇到真实 wire peer”的那个文件，AGENTS.md 要求单文件不越过自己的职责；
   两个文件共享夹具，所以它们各自证明什么若有差异仍然可见）；
   被吊销的 peer 收不到 FIN（退役它的 key 就是吊销的含义，而 FIN 无法为一个 device 已不持有的 key 加密），
   用例按规格 §18.5 的服务端语义断言并在断言处说明；
   稳定性命令实际是 `-run 'VPNGateway|VPNE2E'`（计划的 `-run VPNGateway` 匹配不到矩阵自己的名字，
   会把旧用例跑三遍而新用例一遍不跑）；
   **计划规定的 200ms 首包界改为「`unmatchedTCP()==0` + 700ms」两条断言**，理由与实测数据见红灯一节。
   这是本阶段唯一一处改动断言而非只改实现，计划 §T14 Step 2 允许“断言本身与规格冲突”时改动并要求记录：
   冲突在这里是计划内部的——200ms 的界与计划 §9 自己要求的 `-race` 门禁不可能同时满足。
9. **T15**：文档按计划新增三份、更新六处；额外执行了计划只说“核验”的相对链接死链检查
   （10 个新增/改动文件 0 死链；全仓 39 处既有死链全部位于 `docs/superpowers/plans|specs/` 的时点记录里，
   按 `docs/development/documentation.md` 的不可改写原则保持原样，本阶段不动）。

## 顺带发现、本阶段刻意不修的既有问题

1. **CI 没有 Go 门禁**。仓库的工作流不跑 `go test` / `go vet` / `gofmt`，所以本阶段的全部门禁
   都是本机手工执行。属既有问题，不在阶段 6 范围内。
2. **`Dockerfile` 的 `GO_VERSION=1.23` 落后于 `go.mod`**。本阶段零改动 Dockerfile，
   但一旦要发行 `-tags vpn` 的容器镜像，这个版本就必须先跟上（gVisor 对 Go 版本敏感）。属阶段 8。
3. **`.dockerignore` 未排除 `test/`**。会把 spike 与 e2e 目录带进构建上下文。属阶段 8。
4. **发行构建矩阵不含 `-tags vpn`**。见
   [跨平台可执行文件打包](../deployment/binary-release.md)。要不要发一个 VPN 变体是产品决定，
   本阶段只把体积增量实测出来供决策（+5.17 MiB）。
5. **`tunnelmesh_streams_active` 在拒绝突发时被拉低**。丢包复用
   `tunnelmesh_streams_total{result="rejected"}`，而 `ObserveStream` 同时维护活跃流 gauge，
   在 `rejected` 上减一次却没有对应的加一次，所以一阵密集拒绝会把 tcp 的 gauge 拉到低于真实活跃流数，
   直到下一次 accept 校正。counter 本身精确，告警读 counter 所以不受影响。
   规格 §13 的 `tunnelmesh_vpn_packets_dropped_total` 属阶段 8，届时替换适配器 body、包路径不变
   （网关依赖 `vpnMetrics` 小接口而不是 `*observability.Metrics`，就是为了这次替换）。
   这条已写进运维文档，并明确“不要用 `tunnelmesh_streams_active` 做容量判断或告警”。
6. **UDP 数据报边界依赖时序**。一个 association 上的多个数据报若在同一批里到达，
   边界由读的顺序保证；本阶段的用例逐个发送，因此没有覆盖“同批多数据报”的边界。
   属阶段 8 的 `test/e2e/vpn/` 该补的。
7. **仓库根模块路径与远端不一致**。`go.mod` 是 `github.com/tunnelmesh/tunnelmesh`，
   远端是 `git@github.com:nnworld/TunnelMesh.git`。既有问题，本阶段不动。

## 发布步骤

1. **决定要不要发 VPN 变体**。默认发行二进制不带 `-tags vpn`，升级它对现有部署是零变化。
   要开数据面必须自行构建：`go build -tags vpn -o tunnelmesh-server ./cmd/tunnelmesh-server`。
2. **先升 Agent，再升 Server**。阶段 7 的 ICMP echo 能力是 Server 侧签发门禁的前提；
   顺序反过来也安全（Server 不发 `icmp-echo`，Agent 的引擎闲置），但先升 Agent 能让能力协商
   在网关上线那一刻就完成。
3. **放行公网 UDP**。这一步在启用之前做，且不经 Nginx/OpenResty：
   ```bash
   nft add rule inet filter input udp dport 51820 iifname "eth0" accept
   # 或 firewall-cmd --permanent --add-port=51820/udp && firewall-cmd --reload
   # 或 ufw allow 51820/udp
   ```
   云安全组同理单独加一条 UDP 入站规则。
4. **注入节点私钥并确认 DNS**：
   ```bash
   wg genkey        # 或 openssl rand 32 | xxd -p -c 64
   # → TUNNELMESH_VPN_NODE_PRIVATE_KEY，chmod 600 的 EnvironmentFile / Secret
   dig +short gw-1.mesh.example.com   # 必须解析到网关实际监听的公网地址，TTL 建议 ≤ 60s
   ```
   **私钥一旦更换，所有已下发配置立即失效**，必须全部重新 reveal 导入。轮换前先准备好重新下发的通道。
5. **灰度顺序**：先在一个节点上 `server.vpn.enabled: true`，`check-config` 通过后重启，确认启动日志里
   `vpn_gateway_started` → `vpn_gateway_enabled` → `vpn_peers_loaded` 三行都在、`refused=0`、
   `ss -lunp` 看得到那个 UDP 端口、`/api/v1/vpn-nodes` 返回 `enabled: true` 且 `subnet` 非空。
   然后签发一个测试 peer、reveal、用真实客户端接入，再观察核心指标后铺开。
6. **集群里每个节点必须配置相同的 `ip_pool` 与 `node_subnet_size`**，否则子网租约会互相拒绝。
7. **不要对外宣布 `ping` 可用**，直到出口 Agent 那一侧的
   `net.ipv4.ping_group_range` 与 `agent.streams.icmp_enabled` 都到位（阶段 7 的发布步骤）。

## 回滚步骤

- **不回滚代码即可止损**，两条路，都在 5 分钟内：
  1. `server.vpn.enabled: false` + 重启 → 进程不创建任何 VPN 资源，行为与旧版本完全一致；
     管理 API 写操作返回 409 `vpn_node_disabled`，读操作返回空列表。
  2. 换回不带 `-tags vpn` 的二进制 → 数据面根本不在进程里，最强止损。
- **数据面没有持久化状态**：流表、监听器注册表、peer 内存视图、拒绝聚合器全在内存。
  回滚不需要数据补偿、不需要迁移、不需要清理。数据库里的 `vpn_peers` / `vpn_ip_leases` 行
  在回滚后仍可被阶段 4/5 的管理 API 读写。
- **代码回滚顺序**：T15 → T14 → T13 → T12 → T11 → T10 → T9 → T8 → T7 → T6 → T5 → T4 → T3 → T2 → T1。
  T1 必须最后回滚：`go.mod` 的两个依赖被 T3/T4/T11 的 tagged 文件引用，只回滚 T1 会让
  `-tags vpn` 构建失败（默认构建不受影响，因为 tagged 文件被排除）。
- **回滚 T2 需要单独评估**：它改了 `SetVPN` 的签名与 `VPNPeerServiceDeps.NodePublicKey` 的来源。
  回滚后 `config:reveal` 回到恒 409 `vpn_node_disabled`，已签发的 peer 行不受影响
  （密文与公钥都在库里，只是渲染不出 ini）。
- **回滚 T13 必须同时回滚两处**：它移除了 OpenAPI 的两个 `x-tunnelmesh-phase` 标记并把守卫测试的
  期望值从 2 改成 0，只回滚一处守卫测试就红。
- **退出时的断流是可预期的**：`shutdown_timeout`（默认 15s）是 drain 上限，超时强制关闭在途流并在
  `vpn_gateway_stopped` 里报告 `in_flight_at_timeout` 与 `flows_forced`。回滚前若要避免断流，
  先把用户侧通知发出去，或临时调大 `shutdown_timeout`。
- **失去租约的节点会继续服务存量 peer 直到重启**（刻意设计：一次数据库分歧不是把正在工作的流量
  打成黑洞的理由）。回滚或重启后要确认 `vpn_subnet_lease_lost` 没有在新的进程里再次出现。

## Reviewer 关注点

1. **构建隔离是不是真的**（`scripts/vpn_build_tag_test.go` + 两条 `go list -deps` 命令）。
   这是本阶段最重要的结构性质：不带 tag 的二进制的依赖图里必须没有 gVisor 与 wireguard-go。
   任何一个不带 `//go:build vpn` 的文件引用了它们，隔离就退化成一句承诺。
2. **监听器注册与 `InjectInbound` 的顺序**（`internal/server/vpn_stack.go` 的 `registerTCP` 与
   `internal/server/vpn_tcp.go` 的调用点）。D4 的全部价值在这个顺序上，而顺序是编译器看不见的性质。
   守卫是 `unmatchedTCP()==0`，出现在 `vpn_stack_test.go` 与 `vpn_e2e_test.go` 两处。
3. **默认 TCP 处理器为什么消费而不返回 false**（`vpn_stack.go` 的 `handleUnmatchedTCP`）。
   返回 false 会走到 gVisor 的 unknown-destination 路径并回 RST，与“拒绝一律静默”冲突。
   注释里写了理由，但这是一个很容易被后来的重构“顺手改回标准做法”的地方。
4. **`emit` 与 `injectTCP` 的分工**（`internal/server/vpn_device.go`）。
   网关自己构造的包（UDP 回程、ICMP echo reply）走 `emit`，绕过栈；只有 TCP 段走 `injectTCP`。
   `injectTCP` 显式拒绝非 TCP，因为 gVisor 会对未注册的传输协议自己回一个 ICMP protocol-unreachable。
   这让“netstack 只终结 TCP”成为结构性质而不是管线要在每条路径上自己守住的承诺。
5. **入向包必须复制**（`memoryDevice.Write`）。wireguard-go 在 `Write` 返回的瞬间把缓冲区还给池。
   这是 `-race` 不一定能稳定抓到的损坏，只能靠读代码确认。
6. **`vpnMetrics.Dropped` 的 gauge 副作用**（`internal/server/vpn_runtime.go` 的适配器）。
   复用 `ObserveStream(protocol, "rejected", class)` 会在活跃流 gauge 上减一次而没有对应的加一次。
   这是**已知的、被记录在适配器注释里的精度损失**，counter 精确，阶段 8 替换。
   Reviewer 需要判断的是：在阶段 8 之前，这个 imprecision 是否可接受到能发布。
7. **拒绝聚合的桶数上限**（`internal/server/vpn_denials.go`）。桶满时新桶被丢弃，
   此时指标仍精确、只有审计粒度退化。这是防审计洪泛与审计完整性之间的取舍，
   上限 4096 与窗口 1 分钟都是常量而不是配置项。
8. **`/vpn-nodes` 为什么不加管理员门禁、为什么答 200 而不是 501**（`internal/server/vpn_peer_api.go`）。
   它报告的是本进程的节点视图，与 `dashboard` 同级；答 501 会在每个不带 tag 的 Server 上
   读作一个关于整个集群的断言。
9. **`flows` 为什么在无网关时答 501 而不是空列表**。同一个问题反过来：“没有流量”与
   “流量在别的节点”是两个事实，只有后者能告诉运维去哪儿看。前端把 501 渲染成 info 提示
   而不是空表格，这一配对是这一处设计的全部意义，改任何一边都要改另一边。
10. **T14 对断言的改动**（`vpn_e2e_test.go` 的 `vpnE2EFirstSegmentBound` 与 `vpnE2EPingReader`）。
    这是本阶段唯一改动断言而非只改实现的地方，计划允许但要求记录。
    两处都有实测数据支撑（`-race` 10/10 复现 201–492ms；独立 gVisor netstack 的注入实验），
    请重点复核“换掉的断言是否仍然证明原来要证明的事”：`unmatchedTCP()==0` 覆盖 D4 的机制本身，
    700ms 覆盖“首段在栈之上丢失”，ping reader 的前置阻塞覆盖“reply 到达早于读取”。

## 集成状态

- 分支：`codex/vpn-phase6-server-data-plane`（16 个提交，stacked 在 `codex/vpn-phase7-agent-icmp` 之上）
- 变更规模：76 个文件，+18,028 / −146（不含本 PR 记录文件自身）
- 门禁：`go build ./...`、`go build -tags vpn ./...`、`go test ./... -count=1`、
  `go test -tags vpn ./... -count=1`、`go test -race ./... -timeout 30m -count=1`、
  `go test -tags vpn -race ./... -timeout 30m -count=1`、`go vet ./...`、`go vet -tags vpn ./...`、
  `gofmt -l internal scripts`、`git diff --check`、`go test ./scripts/ -count=1`、
  两条 `go list -deps` 隔离门禁、`-run 'VPNGateway|VPNE2E' -count=3 -race`、
  `npm test -- --run`（38 files / 327 tests）、`npm run build`、`scripts/verify-web-embed.sh`、
  `scripts/gen_doc_index.py` 两次幂等、docs 相对链接死链核验（新增/改动文件 0 死链）——全部通过
- 零改动守卫：`migrations/`、`SchemaVersion`、`Dockerfile`、compose 文件、`web/package.json`、
  `web/package-lock.json`、OpenAPI 路径集合、10 个稳定错误码集合；
  `go.mod` +5 行 / `go.sum` +10 行全部是计划内的依赖（`golang.zx2c4.com/wireguard`、
  `gvisor.dev/gvisor` 两个直接依赖，`github.com/google/btree`、`golang.org/x/time`、
  `golang.zx2c4.com/wintun` 三个间接依赖）
- 时点记录：阶段 1/3/4/5/7 与 Task 0 的计划、规格、PR 记录**只链接、未修改**；
  对它们的事实更正只写在本文件。`docs/superpowers/plans|specs/` 里 39 处既有死链按不可改写原则保持原样
- 活文档：新增 3 份（`user-guide/vpn.md`、`deployment/vpn-gateway.md`、`operations/vpn.md`），
  更新 6 处（`architecture/overview.md`、`operations/configuration.md`、`operations/config-examples.md`、
  `user-guide/server-admin.md`、`docs/README.md`、根 `README.md` 与 `README.zh-CN.md`），
  `docs/README.md` 无孤儿文档
- 后续阶段：阶段 8（可观测性与部署）承接 `tunnelmesh_vpn_*` 专用指标向量、Grafana 的 VPN Row 与告警规则、
  `test/e2e/vpn/`、`Dockerfile` 的 `GO_VERSION`、`.dockerignore`、发行矩阵的 VPN 变体、
  以及本阶段未能在 Linux 取证的“非 root + 无 `CAP_NET_ADMIN`”两项
## 补记：合并 `main` 后的 Schema 版本重编号（v15 → v16）

本节是 `AGENTS.md` 允许的补记，只记录已验证事实。上文「集成状态」里的提交数、变更规模与
「零改动守卫：`migrations/`、`SchemaVersion`、增量链零改动」描述的是**合并 `main` 之前**的状态，
以本节为准。

### 触发原因

本分支的阶段 3 把 VPN 存储落在 `migrations/incremental/v0014_to_v0015/`，`SchemaVersion = 15`。
在本分支开发期间，`main` 发布了 v1.2.4（`2fafd7f`），用**同一个** `v0014_to_v0015` 目录和
**同一个** `SchemaVersion = 15` 修复了 `connection_epoch` 的 32 位钳制缺陷。合并时两者在
`migrations/incremental/v0014_to_v0015/{mysql,sqlite}.sql` 上产生 add/add 冲突。

`AGENTS.md` 规定「已发布的增量脚本禁止修改、重排或删除；修复已发布迁移必须新增下一个 Schema
版本」。`main` 的 v15 已随 v1.2.4 发布，本分支的 v15 未发布，因此由 VPN 迁移让位。

### 处理

| 项 | 合并前（本分支） | 合并后 |
| --- | --- | --- |
| `connection_epoch` 加宽 | — | `migrations/incremental/v0014_to_v0015/`（保留 `main` 原文，逐字未改） |
| VPN 两张表与五个索引 | `migrations/incremental/v0014_to_v0015/` | `migrations/incremental/v0015_to_v0016/` |
| `internal/storage/db.go` `SchemaVersion` | 15 | 16，并新增 `case 15` 迁移分支 |
| `migrations/embed.go` | `V14ToV15{MySQL,SQLite}` | 追加 `V15ToV16{MySQL,SQLite}` |
| `migrations/ddl.sql` | VPN 表 + `connection_epoch INTEGER` | 两侧自动合并：VPN 表 + `connection_epoch BIGINT` |

两步互相独立：v15 只 `ALTER` 两张租约表的列宽，v16 只 `CREATE` 新对象，链按相邻版本顺序执行，
多版本升级不得跳步。

### 同步改动

- 迁移测试全部重编号：`TestSQLiteV14ToV15VPNMigration` → `TestSQLiteV15ToV16VPNMigration`、
  `TestMySQLV14ToV15VPNMigrationsAreAdjacentAndDialectSafe` → `TestMySQLV15ToV16VPNMigrationsAreAdjacentAndDialectSafe`、
  `TestMySQLV14ToV15VPNMigration` → `TestMySQLV15ToV16VPNMigration`、`prepareV14Base` → `prepareV15Base`。
  `main` 的 `TestMySQLV14ToV15WidensConnectionEpoch` 与 `TestSQLiteV14ToV15LeaseEpochMigration`
  保持原名（它们测的仍是 v14→v15 这一步），只把 `SchemaVersion` 断言从 15 调到 16。
- `TestSchemaVersionIs15` → `TestSchemaVersionIs16`。
- `TestSQLiteV14ToV15LeaseEpochMigration` 的收尾断言由硬编码 `!= 15` 改为 `!= SchemaVersion`：
  `auto_init` 总是迁移到链尾，v14 起点现在会一路走到 v16。测试注释同步说明「相邻链缺一步会以
  `missing adjacent migration` 失败」，原有意图（v14→v15 步可执行、int64 fencing token 往返）未削弱。
- `docs/operations/schema-upgrades.md`：新增 `## v15 to v16` 段（由原 `## v14 to v15` 段重编号而来，
  并补写「为什么是 v16 而不是 v15」），`main` 的 `## v14 to v15` 段逐字保留在其后；
  `## v13 to v14` 及更早的段落与合并基线逐字一致（`difflib` 校验 0 差异）。
- 同文档「Five-minute stop-loss」一节按阶段 6 的既成事实改写：`server.vpn.*` 已经存在，
  止损首选 `server.vpn.enabled: false` 配置开关而不是回滚发布。
- `docs/user-guide/server-admin.md`：当前 Schema 版本 v15 → v16。
- 阶段 3 的计划与 PR 记录（`2026-09-20-vpn-phase3-schema-v15.md`）按时点记录不可改写原则
  **未修改**，其中的 v15 编号是当时的真实决策；重编号这一事实只记录在本节与 `schema-upgrades.md`。

### 其它冲突处理

- `web/src/i18n/messages/{en-US,zh-CN}.ts`：两侧改动正交（本分支加 `servers.vpn.*`，`main` 加
  `clients.metadataStale`、`clients.leaseState*`），逐行取本分支的 `servers` 行与 `main` 的 `clients` 行；
  合并前用 base 三方比对确认过正交性。
- `docs/README.md`：文档地图三行合并，同时保留 `一键安装` 与 `VPN 网关`。
- `docs/pull-requests/README.md`、`docs/superpowers/{plans,specs}/README.md`、
  `docs/architecture/adr/README.md`：由 `scripts/gen_doc_index.py` 重新生成，连续两跑输出幂等。
- `internal/storage/{client_repository_test.go,mysql_test.go}`：两侧新增的测试全部保留。

### 验证（合并后实测）

| 命令 | 结果 |
| --- | --- |
| `go build ./...` / `go build -tags vpn ./...` | 通过 |
| `go test ./... -count=1` | 24 包全绿 |
| `go test -tags vpn ./... -count=1` | 24 包全绿 |
| `go test -tags vpn ./internal/server/ -run 'VPNGateway\|VPNE2E' -count=1 -v` | 32 个用例全绿 |
| `go test -race -tags vpn ./internal/{storage,server,client,agent,protocol,e2e}/... ./scripts/ -count=1 -timeout 30m` | 全绿，`DATA RACE` 0 次（`internal/server` 510s） |
| `go vet ./...` / `go vet -tags vpn ./...` | 通过 |
| `gofmt -l internal scripts cmd deploy` | 无输出 |
| `git diff --check` / `git diff --cached --check` | 无输出 |
| `cd web && npm test -- --run` | 40 files / 335 tests 全绿 |
| `cd web && npm run build` + `scripts/verify-web-embed.sh` | 通过，`web/dist` 与 `internal/server/web_dist` 一致 |

`main` 带进来的 `deploy/install/oneclick` 的 `TestShellFunctionSuite` 在**有控制终端**的环境下会
阻塞在 `/dev/tty` 交互提示（`==> 认证方式（socks5 只支持 none|password）`）直到 10 分钟超时，
与本次合并无关（无控制终端时该套件 1.2s 通过）。本节所有 `go test` 均通过 `setsid` 脱离控制终端执行。
