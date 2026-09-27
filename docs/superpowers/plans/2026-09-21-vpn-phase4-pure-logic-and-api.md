# VPN 网关 阶段 4：纯逻辑包与管理 API Implementation Plan

- 日期：2026-09-21
- 分支：`codex/vpn-phase4-pure-logic-and-api`（stacked 在 `codex/vpn-phase3-schema-v15` 之上）
- 规格：[内嵌 VPN 网关（WireGuard）设计](../specs/2026-09-19-embedded-vpn-gateway-design.md) §5.1、§5.3、§8、§9、§11、§12.1、§14、§15 阶段 4
- 决策载体：[ADR 0002](../../architecture/adr/0002-public-ingress-and-embedded-vpn.md)
- 前置阶段：[阶段 3 PR 记录](../../pull-requests/2026-09-20-vpn-phase3-schema-v15.md)（Schema v15、`VPNPeerRepository`、`VPNIPLeaseRepository`）

## 1. 目标

交付规格 §15 的阶段 4：

1. **`internal/vpn/` 纯逻辑包**：密钥、错误码、IP 池切分与分配、逐包策略、peer 规格校验与规范编码、
   WireGuard ini 渲染。零 I/O、零新依赖、无 build tag，因此可完全单元测试。
2. **`server.vpn` 配置**：形状对标 `internal/config/config.go:152` 的 `ProxyEntryConfig`，
   默认 `enabled: false`，全部键有默认值与校验。
3. **`internal/server/vpn_peer_service.go`**：签发、吊销、轮换、reveal、列表、详情的业务编排。
4. **`internal/server/vpn_peer_api.go`**：规格 §9 的 9 个端点中**本阶段可独立成立的 7 个**，
   挂到 `internal/server/api.go:331` 的 switch。
5. **`docs/api/openapi.yaml` 同步**与全套 API 测试（未授权、越权、分页、幂等、错误响应）。

## 2. 非目标

- 不写数据面：`internal/server/vpn_device.go`/`vpn_stack.go`/`vpn_packet.go`/`vpn_flows.go`/
  `vpn_icmp.go`/`vpn_lifecycle.go` 属阶段 6。
- **不新增任何 Go 依赖**：`gvisor.dev/gvisor` 与 `golang.zx2c4.com/wireguard-go` 属阶段 6，
  只被 `//go:build vpn` 文件引用。`go.mod`/`go.sum` 必须零改动。
- 不改 Schema：`SchemaVersion` 保持 15，`migrations/` 零改动。阶段 3 的两张表已够用。
- 不动前端：`VpnPeers.vue`、`api/vpn.ts`、导航与 i18n 属阶段 5，`web/` 零改动。
- 不动 Agent：ICMP 能力协商与 `internal/agent/icmp_echo.go` 属阶段 7。
- 不做 Grafana Row、告警规则、build tag 变体与发布脚本：属阶段 8。
- **两个数据面依赖端点本阶段不实现**：`GET /api/v1/vpn-peers/{peerId}/flows` 需要阶段 6 的内存流表，
  `GET /api/v1/vpn-nodes` 需要阶段 6 的网关运行时状态。二者返回 `501 vpn_not_implemented`
  并在 OpenAPI 中标注 `x-tunnelmesh-phase: 6`，不返回伪造的空数据（对齐 `AGENTS.md`
  「优雅降级不得返回假数据」与阶段 1 「不宣称未验证能力」的纪律）。

## 3. 前置核实结论（实测，非推断）

1. **标准库可以生成 WireGuard 兼容密钥。** `crypto/ecdh` 的 X25519 与 RFC 7748 §6.1 官方向量逐字节一致
   （实测：alice/bob 的 base-point 乘法与共享密钥三项全部 `true`）。WireGuard 的密钥就是
   32 字节 X25519 标量的标准 base64，因此 `keys.go` 只需 `crypto/ecdh` + `encoding/base64`。
   `ecdh.X25519().NewPrivateKey(raw)` 接受任意 32 字节并在标量乘时执行 RFC 7748 的 clamping，
   与 wireguard-go 的推导等价；公钥 base64 长度恒为 44 字符，`vpn_peers.public_key VARCHAR(64)` 足够。
2. **`routing.Policy` 可直接包装。** `internal/routing/policy.go:24` 的
   `NewPolicy(cidrs []string, ports any)` 与 `:102` 的 `Validate(ip net.IP, port int)`，
   加上 `:129` 的 `IsDangerousAddress`，正是 `internal/proxyentry/target.go:25` 已经用过的手法。
   注意 `routing.Policy.Validate` **只接受 IPv4**（`ip.To4() == nil` 即拒），
   因此 VPN 策略天然继承「IPv4-only」这条既有约束。
3. **`proxyentry.IsPrivateTarget` 已是可复用的私网判定**（`internal/proxyentry/target.go:113`）：
   `IsPrivate || IsLoopback || IsLinkLocalUnicast || IsLinkLocalMulticast || IsUnspecified`。
   `internal/vpn` 不得重复实现，但也不能 import `internal/proxyentry`（那是 tp-* 入口的包）。
   决策见 D6。
4. **错误码风格**：`internal/proxyentry/errors.go:58` 的 `Error{Status, Code, message, headers}` +
   `NewError(...)` + 包级 `var Err* = NewError(...)` 集中声明，且注释要求「codes 是公开契约，
   改名需 deprecation 窗口」。`credential_secret_unavailable` 已存在，规格 §12.1 要求复用它。
5. **API 挂载点**：`internal/server/api.go:331` 的 `switch parts[0]`；响应封装是
   `apiEnvelope{Code, Msg, Data}`（`api.go:285`），错误走 `writeAPIError(w, status, msg)`
   把 `msg` 放进 `data.error`；`decodeJSON` 强制 `DisallowUnknownFields` 且限 1 MiB；
   `queryLimit` 默认 50、上限 500；`splitPath` 已剥掉 `/api/v1` 前缀。
6. **reveal 范式**：`internal/server/token_api.go:71` 起——要求 `Idempotency-Key`、
   确认头、`acknowledgeRisk`，响应 `Cache-Control: no-store`，并写审计。
   `credential_api.go:120` 的 `handleExtractSSHPublicKey` 是同一纪律的简化版。
7. **配置默认值集中在一张 map**：`internal/config/config.go:503` 起以
   `"server.webssh.ticket_ttl": 30 * time.Second` 的形式声明；`server.vpn.*` 必须同样登记，
   否则 `mapstructure` 解码后是零值而不是文档承诺的默认值。
8. **存储层能力已就绪**（阶段 3）：`db.VPNPeers()` 提供
   `Create/Get/GetByPublicKey/GetByNodeAndIP/Update/SetStatus/List/ListByNode/CountByNode/CountByOwner`，
   `db.VPNIPLeases()` 提供 `AcquireSubnet/Renew/Release/Get/ListByHolder/AddAllocated`；
   sentinel 为 `ErrVPNPeerConflict`、`ErrVPNPeerRevoked`、`ErrVPNIPLeaseHeld`、`ErrVPNIPLeaseStaleEpoch`。
   `vpn_peers.allowed_ips`/`allowed_ports` 是**存储层不解析的不透明文本**，
   其规范编码的唯一所有者就是本阶段要写的 `internal/vpn`。
9. **`storage.VPNPeer` 有 22 列**，其中 `PrivateKeyCiphertext`/`Nonce`/`KeyID`/`Version` 四列
   与 `credentials.secret_*` 同构，密封走 `internal/auth/secret_store.go`；
   `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 缺失时既有身份/凭据特性返回 503 `credential_secret_unavailable`。
10. **文档主张守卫仍然生效**：`go test ./scripts/` 会扫描 `AGENTS.md`、双语 README、`docs/`、`deploy/`
    五个活文档根。阶段 4 新增的用户文档**不得宣称 VPN 数据面已可用**——本阶段交付的是管理面，
    用户还不能真正连通。措辞必须是「已批准、分阶段实施中、当前版本尚未提供数据面」。
11. **`docs/api/openapi.yaml` 存在且是接口契约的唯一权威**，`AGENTS.md` 要求每次接口行为变更同步更新。
12. **时点记录不可改写**：阶段 3 的计划、PR 记录与 ADR 0002 只能新增不能修改；
    本阶段引用它们时用链接，不改其正文。

## 4. 架构决策

### D1：`internal/vpn` 是纯函数包，不 import 存储与 HTTP

`internal/vpn` 只依赖标准库与 `internal/routing`。它不认识 `storage.VPNPeer`、不认识
`http.ResponseWriter`，因此可以脱离数据库与网络做穷举单元测试，也不会把持久化细节渗进领域逻辑。
持久化模型与领域模型之间的映射只存在于 `internal/server/vpn_peer_service.go` 一处。

### D2：领域侧只定义 `PeerSpec`，不复制 22 列的存储模型

`storage.VPNPeer` 是持久化记录（含 `CreatedAt`、`PrivateKeyVersion` 等只有存储关心的字段）。
`vpn.PeerSpec` 是**校验并解析后的输入**：`AllowedIPs` 是 `[]*net.IPNet`、`AllowedPorts` 是 `[]int`，
而不是字符串。二者不是同一层的东西，复制一份 22 字段的镜像结构只会制造漂移。
`PeerSpec` 同时是 `allowed_ips`/`allowed_ports` 规范编码的唯一所有者（D3）。

### D3：规范编码由 `internal/vpn` 独占

阶段 3 承诺「存储层从不解析 `allowed_ips`/`allowed_ports`」，那个承诺要成立，
就必须有且只有一个地方定义它们的文本形式：

- `vpn.EncodeAllowedIPs([]*net.IPNet) string`：按 `String()` 输出、**排序去重**、逗号分隔、无空格。
- `vpn.EncodeAllowedPorts([]int) string`：升序去重、逗号分隔；空集合编码为空串（= 不限端口）。
- `vpn.ParseAllowedIPs(string)` / `vpn.ParseAllowedPorts(string)`：反向解析，非法输入返回 `ErrPeerInvalid`。

排序是必需的：同一组 CIDR 的两种书写顺序必须编码成同一个字符串，否则
「改了一次顺序」会在审计里显示成一次策略变更，幂等键也会失效。

### D4：IP 池切分是纯函数，占用判定靠回调，持久化仍归 Repository

`vpn.Pool` 只回答「这个池能切出哪些 `/N`」「这个 `/N` 里哪些 `/32` 可用」，
**不查库**。`AllocateAddress(subnet, taken func(net.IP) bool)` 用回调把「已占用」的判断交给调用方，
调用方（Service）用 `VPNPeerRepository.GetByNodeAndIP` 实现它。这样纯逻辑可测，
权威事实仍在 `vpn_peers.UNIQUE(node_id, vpn_ip)`，与阶段 3 的设计一致。

约束：

- `ip_pool` 必须是 IPv4、前缀长度 ≤ 24，`node_subnet_size` 必须满足
  `poolBits < size ≤ 30`（`/30` 才有 2 个可用 `/32`）。
- 切出的子网总数上限 **4096**，超出即 `ParsePool` 失败。没有上限时一个 `/8` 配 `/24`
  会产生 65536 个 `*net.IPNet`，一次配置错误就能把内存打满。
- **每个子网的第一个可用地址保留给节点自己的 VPN 接口**（`Pool.NodeAddress`），
  `AllocateAddress` 从第二个可用地址开始，且跳过网络地址与广播地址。
  不保留的话 Server 的 netstack 会和第一个 peer 抢同一个 `/32`。

### D5：逐包策略包装 `routing.Policy`，并叠加 VPN 专属规则

`vpn.PacketPolicy` 的判定顺序**是安全语义的一部分**，不可调换：

1. 分片 → `fragment_dropped`（非首片无法判定端口，放行即等于绕过端口白名单）。
2. 超长 → `oversize_dropped`（`MTU` 来自配置）。
3. 协议号白名单：TCP=6、UDP=17、ICMP=1；其余 → `protocol_unsupported`。
   ICMP 且 peer 未开 `icmp_enabled` → `icmp_unsupported`。
4. `routing.IsDangerousAddress(dst)` → `metadata_denied`（云 metadata、组播、非路由地址恒拒，
   与 `tp-*` 和 Agent policy 共用同一 deny list）。
5. `!allowPrivateTargets && IsPrivateTarget(dst)` → `target_denied`。
6. peer `AllowedIPs` 不含 dst → `target_denied`。
7. TCP/UDP 且端口不在 `AllowedPorts` → `port_denied`。

`error_class` 字符串必须与规格 §12.2 逐字一致，因为它们会直接成为 Prometheus 的低基数标签值。
`peer_unknown`/`peer_revoked`/`peer_expired`/`capacity_exhausted`/`rate_limited`/`egress_*`/`icmp_timeout`/
`stack_error` 属阶段 6 的数据面，本阶段只定义常量、不产生它们。

### D6：私网判定下沉到 `internal/vpn`，`proxyentry` 改为委托

`proxyentry.IsPrivateTarget`（`target.go:113`）是导出的既有 API，`internal/vpn` 需要同一个判定，
但两者互不 import（`proxyentry` 是 tp-* 入口包，`vpn` 是 VPN 领域包）。三个选项：

- (a) 在 `internal/vpn` 复制一份 → 违反 DRY，且两份实现迟早漂移，而这是一个**安全判定**。
- (b) `internal/vpn` import `internal/proxyentry` → 让 VPN 领域依赖 tp-* 入口，方向错误。
- (c) **把实现下沉到 `internal/routing`（两包都已依赖它），`proxyentry.IsPrivateTarget` 保留为
  一行委托**，签名与行为完全不变。

选 (c)。`internal/routing` 已经是「目标地址是否危险」的权威包（`IsDangerousAddress` 就在那里），
私网判定与它同层。委托保持 `proxyentry` 的导出面不变，因此不破坏任何既有调用方与测试。

### D7：`server.vpn.enabled: false` 时零 VPN 资源，且不注册路由以外的任何东西

配置默认关闭。关闭时：不生成密钥、不切 IP 池、不启动任何 goroutine、不注册 peer 热加载。
管理 API 的 7 个端点**仍然注册**（它们是既有 `/api/v1` 路由树的一部分），
但 Service 在 `enabled: false` 时对写操作返回 `409 vpn_node_disabled`，
读操作返回空列表——这样前端在阶段 5 可以先接上，而用户不会以为签发成功了。
`server.vpn.enabled: true` 而二进制没有 vpn tag 时的快速失败属阶段 6（数据面才需要 tag）。

### D8：签发是「生成密钥 → 选节点子网 → 分配 /32 → 校验 Agent → 落库」的单一事务边界

顺序不可调换，理由：

- 密钥必须**先**生成，因为 `vpn_peers.public_key` 是 UNIQUE，冲突要在落库前就知道。
- 子网租约必须**先于** /32 分配拿到（`VPNIPLeases().AcquireSubnet`），
  否则两个节点可能同时认为自己拥有同一个 `/24`。
- Agent 能力校验必须在落库前：规格 §12.1 的 `409 vpn_agent_capability_missing` 是
  「peer 要求 ICMP 但出口 Agent 未协商 `stream_icmp_echo.v1`」。
  **阶段 7 才有该能力协商**，因此本阶段：`icmp_enabled: true` 的签发请求一律返回
  `409 vpn_agent_capability_missing`，并在错误信息里写明该能力属阶段 7。
  这不是偷懒——返回成功会造出一个用户以为能 ping、实际不能 ping 的 peer。
- 分配成功后必须 `AddAllocated(+1)`；落库失败必须回滚计数（`AddAllocated(-1)`），
  否则计数器会永久漂移。计数器只是派生值（阶段 3 已明确），但漂移会让耗尽判定失效。

### D9：吊销与轮换复用阶段 3 的终态语义

- 吊销 = `SetStatus(revoked)`，**不删行**（阶段 3 的 Repository 没有 `Delete`）。
  `404 vpn_peer_not_found` 用于「不存在或对当前主体不可见」；吊销后的 peer 仍然可读，
  但 `ListByNode` 不再返回它，因此数据面装载不到。
- 轮换 = 生成新密钥对 → `Update` 覆盖四个密封列与 `public_key` → 返回新配置摘要。
  旧公钥立即失效，因为 WireGuard 握手只认 `public_key`。
  轮换**不换 IP**：peer 的地址是用户配置的一部分，换掉它等于静默破坏用户的连通性。
- 对已吊销 peer 轮换 → `409 vpn_peer_conflict`（`ErrVPNPeerRevoked` 映射），
  因为吊销是终态，复活一个退役密钥正是阶段 3 刻意禁止的。

### D10：reveal 严格照 `tokens/{tokenId}/reveal` 范式

`POST /api/v1/vpn-peers/{peerId}/config:reveal` 要求 `X-VPN-Config-Reveal-Confirm`、
`Idempotency-Key`、body 里 `acknowledgeRisk: true`；响应 `Cache-Control: no-store`；写审计。
返回的是**完整 ini（含 peer 私钥）**，因此：

- 私钥只在响应体里出现一次，不落日志、不进指标、不进审计详情。
- 审计记录的 `resource_type`/`resource_id` 只写 peer ID，`details` 不含任何密钥材料。
- 密文存储不可用 → `503 credential_secret_unavailable`（复用既有码，不新造）。
- 普通用户只能 reveal 自己的 peer；管理员可 reveal 任意 peer。权限在服务端判定，
  不信任 body 里的 owner。

### D11：权限过滤在分页语义内完成

`GET /api/v1/vpn-peers` 对普通用户注入 `VPNPeerFilter{OwnerUserID: principal.UserID}`，
管理员不注入。这条在阶段 3 已由 Repository 契约测试守住（owner 过滤是 `WHERE` 条件），
本阶段的 API 测试必须**从 HTTP 层**再验一次：用户 A 的列表里不能出现用户 B 的 peer，
且分页游标续页也不能出现。

### D12：两个未实现端点返回 501 而不是 200 空数据

`GET .../flows` 与 `GET /api/v1/vpn-nodes` 返回
`501 vpn_not_implemented`，msg 指明所属阶段。理由：返回 `200 {"items":[]}` 会让调用方
（尤其是阶段 5 的前端）把「还没实现」误读成「没有活跃流」，
而这正是 `AGENTS.md`「优雅降级不得返回兜底假数据」要防的事。

## 5. 全局约束

1. 每个任务先写失败测试，实测红灯原文，再写最小实现（`AGENTS.md` TDD 流程）。
2. `go.mod`/`go.sum` 零改动。任何 `import` 新第三方包都视为违规。
3. `migrations/`、`internal/storage/`、`web/`、`deploy/` 零改动（阶段 3 已交付存储层）。
   **唯一例外**：D6 会改 `internal/proxyentry/target.go` 与其测试，以及 `internal/routing/policy.go`。
4. 一个任务一个提交，`<type>(<scope>): <subject>`，subject ≤50 字、祈使句、无句号。
5. 不执行 `git commit`/`push` 之外的破坏性 git 操作；本阶段已获用户授权提交与推送。
6. 日志、审计、指标、错误信息一律不含私钥、包载荷、握手字节、DSN、Token。
7. `error_class` 与 `code` 字符串是公开契约，逐字来自规格 §12.1/§12.2，不得自创或改名。
8. 新增用户文档不得宣称数据面可用（前置核实结论 10）。
9. 时点记录（阶段 3 的计划/PR 记录、ADR 0002、既有 spec）零改写。
10. 阶段 4 不引入 `//go:build vpn`：`internal/vpn` 与管理 API 在所有构建变体里都存在。

## 6. 文件清单

| 文件 | 任务 | 内容 |
| --- | --- | --- |
| `docs/superpowers/plans/2026-09-21-vpn-phase4-pure-logic-and-api.md` | — | 本计划 |
| `internal/routing/policy.go` | T1 | 新增 `IsPrivateTarget(ip net.IP) bool`（从 `proxyentry` 下沉，D6） |
| `internal/proxyentry/target.go` | T1 | `IsPrivateTarget` 改为委托 `routing.IsPrivateTarget`，导出签名不变 |
| `internal/routing/routing_test.go` | T1 | `IsPrivateTarget` 的表驱动测试 |
| `internal/vpn/doc.go` | T1 | 包注释：职责、边界、为什么不 import 存储与 HTTP |
| `internal/vpn/errors.go` | T1 | `Error{Status,Code,message}`、`NewError`、规格 §12.1 的 9 个稳定码 |
| `internal/vpn/errors_test.go` | T1 | 码唯一性、状态码正确性、`Error()`/`errors.Is` 行为 |
| `internal/vpn/keys.go` | T2 | `GenerateKeyPair`、`EncodeKey`/`DecodeKey`、`ValidatePublicKey` |
| `internal/vpn/keys_test.go` | T2 | RFC 7748 向量、长度/字符集校验、往返、拒绝非法 base64 |
| `internal/vpn/ippool.go` | T3 | `ParsePool`、`Pool.Subnets/NodeSubnet/NodeAddress/AllocateAddress/Contains` |
| `internal/vpn/ippool_test.go` | T3 | 切分数量、边界、上限、耗尽、网络/广播/节点地址跳过 |
| `internal/vpn/peerspec.go` | T4 | `PeerSpec`、`Validate`、`Encode/ParseAllowedIPs`、`Encode/ParseAllowedPorts` |
| `internal/vpn/peerspec_test.go` | T4 | 规范编码的排序去重与往返、非法输入、空集合语义 |
| `internal/vpn/policy.go` | T5 | `PacketPolicy`、`Packet`、`Allow`、`error_class` 常量 |
| `internal/vpn/policy_test.go` | T5 | D5 的 7 步判定顺序逐步覆盖，含分片与协议号白名单 |
| `internal/vpn/config.go` | T6 | `RenderPeerConfig`（ini）、`RenderInterface`、`RenderPeerSection` |
| `internal/vpn/config_test.go` + `internal/vpn/testdata/*.ini` | T6 | 黄金文件测试 |
| `internal/config/config.go` | T7 | `VPNConfig` 结构、`ServerConfig.VPN` 字段、`server.vpn.*` 默认值与校验 |
| `internal/config/config_test.go` | T7 | 默认值、非法值快速失败、优先级（flag > env > file > default） |
| `docs/operations/configuration.md`、`docs/operations/config-examples.md` | T7 | 全部键、取值范围、默认值；三份示例同步 |
| `internal/server/vpn_peer_service.go` | T8 | `VPNPeerService`：签发/吊销/轮换/reveal/列表/详情 |
| `internal/server/vpn_peer_service_test.go` | T8 | D8 的顺序与回滚、D9 终态、权限、配额、密钥密封 |
| `internal/server/vpn_peer_api.go` | T9 | 7 个端点的 Handler + 2 个 501 |
| `internal/server/vpn_peer_api_test.go` | T9 | 未授权、越权、分页、幂等、错误响应、`no-store` |
| `internal/server/api.go` | T9 | switch 新增 `case "vpn-peers"` 与 `case "vpn-nodes"`；Service 装配 |
| `docs/api/openapi.yaml` | T10 | 9 条路径、schema、稳定错误码、`x-tunnelmesh-phase` 标注 |
| `docs/pull-requests/2026-09-21-vpn-phase4-pure-logic-and-api.md` | T11 | PR 记录 |
| `docs/pull-requests/README.md` 等四份索引 | T11 | `scripts/gen_doc_index.py` 生成 |

### 明确不改

- `migrations/`、`internal/storage/**`（阶段 3 交付，本阶段只消费其接口）。
- `web/`、`deploy/`、`Dockerfile`、`scripts/build-release.sh`。
- 阶段 3 的计划、PR 记录、ADR 0002、既有 spec（时点记录零改写）。
- `internal/server/proxy_entry.go` 与 `tp-*` 相关行为（D6 只动 `IsPrivateTarget` 的实现位置）。

## 7. 任务分解

### T1：私网判定下沉 + 错误码

**Step 1（红灯）**：在 `internal/routing/routing_test.go` 新增 `TestIsPrivateTarget`，
覆盖 `10.0.0.1`、`192.168.1.1`、`172.16.0.1`、`127.0.0.1`、`169.254.1.1`、`0.0.0.0`、
`::1`、`8.8.8.8`、`224.0.0.1`、`nil`；在 `internal/vpn/errors_test.go` 新增
`TestErrorCodesAreStableAndUnique`（断言规格 §12.1 的 9 个码逐字存在、状态码正确、码不重复）。
Run `go test ./internal/routing/ ./internal/vpn/ -count=1` → 预期编译失败 `undefined: routing.IsPrivateTarget`。

**Step 2（实现）**：`routing.IsPrivateTarget` 用 `proxyentry` 的现有实现体；
`proxyentry.IsPrivateTarget` 改成 `return routing.IsPrivateTarget(ip)` 并把注释指向新位置；
`internal/vpn/doc.go` 与 `errors.go`。

**Step 3（绿灯 + 提交）**：`go test ./internal/routing/ ./internal/proxyentry/ ./internal/vpn/ -count=1`
→ `ok`。提交 `refactor(routing): move the private target check down a layer`。

### T2：密钥

**Step 1（红灯）**：`keys_test.go` 断言
`GenerateKeyPair` 返回 44 字符 base64 私钥与公钥、`DecodeKey(EncodeKey(x))==x`、
RFC 7748 alice 向量（私钥 hex `7707…2c2a` → 公钥 base64 `hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo=`）、
`ValidatePublicKey` 拒绝 43/45 字符、非 base64、以及全零私钥导出的公钥可被接受但全零公钥被拒绝。
Run → 编译失败 `undefined: GenerateKeyPair`。

**Step 2（实现）**：`crypto/ecdh` + `encoding/base64`；`ValidatePublicKey` 必须
`DecodeKey` 成功且长度为 32 字节，再用 `ecdh.X25519().NewPublicKey` 确认可导入。

**Step 3（绿灯 + 提交）**：`feat(vpn): generate wireguard keys with the stdlib`。

### T3：IP 池

**Step 1（红灯）**：`ippool_test.go` 覆盖
`10.64.0.0/16` + `/24` → 256 个子网且首尾正确；`node_subnet_size` ≤ poolBits 或 > 30 → `ErrIPPoolInvalid`；
IPv6 池 → `ErrIPPoolInvalid`；`/8` + `/24`（65536 > 4096）→ `ErrIPPoolInvalid`；
`NodeAddress` 是子网第一个可用地址；`AllocateAddress` 跳过网络地址、节点地址与广播地址；
`taken` 回调全部返回 true 时得到 `ErrIPPoolExhausted`；`/30` 子网只剩 1 个可分配地址。
Run → 编译失败 `undefined: ParsePool`。

**Step 2（实现）**：纯函数 + 回调；子网上限常量 `maxNodeSubnets = 4096`。

**Step 3（绿灯 + 提交）**：`feat(vpn): carve node subnets out of the ip pool`。

### T4：PeerSpec 与规范编码

**Step 1（红灯）**：`peerspec_test.go` 覆盖
`EncodeAllowedIPs` 排序去重（`["10.1.0.0/16","10.0.0.0/16","10.1.0.0/16"]` → `"10.0.0.0/16,10.1.0.0/16"`）、
`EncodeAllowedPorts` 升序去重、空端口集合 → 空串、`Parse∘Encode == 原集合`、
非法 CIDR/端口/负数/超范围端口 → `ErrPeerInvalid`、`PeerSpec.Validate` 逐字段
（空公钥、非法公钥、空 VPNIP、`MaxConcurrentFlows<0`、`PacketRateLimit<0`、名称超长）。
Run → 编译失败 `undefined: PeerSpec`。

**Step 2（实现）**。

**Step 3（绿灯 + 提交）**：`feat(vpn): own the canonical peer policy encoding`。

### T5：逐包策略

**Step 1（红灯）**：`policy_test.go` 按 D5 的 7 步**逐步**构造用例，
每步断言返回错误的 `ErrorClass()` 精确等于规格 §12.2 的字符串；
额外覆盖：判定顺序（一个既是分片又指向 metadata 的包必须报 `fragment_dropped`，
因为分片在前）、ICMP 关闭时 `icmp_unsupported`、IPv6 目标被 `routing.Policy` 拒绝、
`AllowedIPs` 为空时的语义（= 不允许任何目标，**不是**「不限」，与端口的空集合语义相反，
必须在注释与测试里都写明）。
Run → 编译失败 `undefined: PacketPolicy`。

**Step 2（实现）**：包装 `routing.Policy` + `routing.IsPrivateTarget`。

**Step 3（绿灯 + 提交）**：`feat(vpn): evaluate per packet egress policy`。

### T6：ini 渲染与黄金文件

**Step 1（红灯）**：`config_test.go` 用 `testdata/peer.ini` 做黄金文件比对；
先写测试与**空的** golden 文件使其失败。
Run → FAIL（golden 不匹配）。

**Step 2（实现）**：`RenderPeerConfig(PeerConfigInput) (string, error)`，
输出 `[Interface]`（`PrivateKey`、`Address = <vpnIP>/32`、`MTU`）与
`[Peer]`（`PublicKey` = 节点公钥、`AllowedIPs = 0.0.0.0/0` 或 peer 策略、`Endpoint`、
`PersistentKeepalive = 25`）。渲染必须对值做 ini 转义校验（拒绝含 `\n`、`;`、`#` 的值），
否则一个恶意 peer 名称可以注入额外配置段。

**Step 3（绿灯 + 提交）**：`feat(vpn): render wireguard peer configuration`。

### T7：`server.vpn` 配置

**Step 1（红灯）**：`config_test.go` 断言全部 15 个键的默认值（规格 §8 逐字）、
`ip_pool` 非法 → 加载失败并给出可操作的错误、`node_subnet_size` 越界 → 失败、
`mtu` 越界（< 576 或 > 1500）→ 失败、环境变量覆盖文件、flag 覆盖环境变量。
Run → 编译失败 `undefined: VPNConfig`。

**Step 2（实现）**：`VPNConfig` 结构 + `ServerConfig.VPN` + 默认值 map + `validate`；
`docs/operations/configuration.md` 与 `config-examples.md` 同步（本地/集群 MySQL/集群 etcd 三份示例）。

**Step 3（绿灯 + 提交）**：`feat(config): add the server vpn section`。

### T8：Service

**Step 1（红灯）**：`vpn_peer_service_test.go` 用真实 SQLite（照阶段 3 的 `newVPNTestDB` 思路）覆盖
签发成功路径（密钥生成、IP 分配、计数 +1、四个密封列非空且明文不入库）、
`icmp_enabled: true` → `409 vpn_agent_capability_missing`、
池耗尽 → `409 vpn_ip_pool_exhausted`、`max_peers` 触顶 → `503 vpn_capacity_exhausted`、
落库失败时计数回滚、吊销后 `ListByNode` 不含它、对已吊销 peer 轮换 → `409`、
普通用户读他人 peer → `404 vpn_peer_not_found`（**不是** 403，避免枚举）、
`enabled: false` 时写操作 → `409 vpn_node_disabled`。
Run → 编译失败 `undefined: VPNPeerService`。

**Step 2（实现）**：`internal/server/vpn_peer_service.go`；密钥密封复用 `internal/auth/secret_store.go`。

**Step 3（绿灯 + 提交）**：`feat(server): orchestrate vpn peer lifecycle`。

### T9：API

**Step 1（红灯）**：`vpn_peer_api_test.go` 从 HTTP 层覆盖 7 个端点的
未授权（401）、越权（404 而非 403）、分页（D11：A 的任何一页都不含 B 的行）、
幂等（同一 `Idempotency-Key` 重放 `POST` 返回同一结果、不产生第二个 peer）、
reveal 的三个前置条件各自缺失时的 400、reveal 响应的 `Cache-Control: no-store`、
两个 501 端点。
Run → 编译失败 `a.vpnPeerService undefined`。

**Step 2（实现）**：`vpn_peer_api.go` + `api.go` 的 switch 与装配。

**Step 3（绿灯 + 提交）**：`feat(server): expose the vpn peer management api`。

### T10：OpenAPI

**Step 1**：`docs/api/openapi.yaml` 新增 9 条路径、`VPNPeer*` schema、稳定错误码枚举、
两条 `x-tunnelmesh-phase: 6` 标注。校验 YAML 可解析且与实现的路径逐字一致
（用一个测试断言 openapi 里出现的 `/api/v1/vpn-*` 路径集合与 `api.go` 实际注册的一致）。

**Step 2（提交）**：`docs(api): specify the vpn peer endpoints`。

### T11：门禁、索引、PR 记录

全量门禁 → 重新生成索引两次确认幂等 → 时点记录零改写核验 → 死链核验 →
写 `docs/pull-requests/2026-09-21-vpn-phase4-pure-logic-and-api.md` →
提交 `docs(pull-requests): record vpn pure logic and api`。

## 8. 整体验证

```bash
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -timeout 30m -count=1
gofmt -l internal/vpn internal/routing internal/proxyentry internal/config internal/server
git diff --check
git status --porcelain go.mod go.sum     # 必须为空
go test ./scripts/ -count=1              # 文档主张守卫
python3 scripts/gen_doc_index.py && python3 scripts/gen_doc_index.py && git status --porcelain
git diff --name-status codex/vpn-phase3-schema-v15..HEAD -- docs/superpowers docs/pull-requests docs/architecture/adr
```

前端未改动，`npm test -- --run` 与 `npm run build` 不是本阶段门禁。
无 Docker/Compose 改动，容器验证不适用。
无 Schema 改动，MySQL 侧迁移测试不适用；Service 与 API 测试跑在 SQLite 上，
MySQL 契约由阶段 3 的 Repository 测试覆盖（本机无实例时如实 SKIP）。

## 9. 回滚注意事项

- 本阶段全部是新增文件 + 两处受控修改（`routing.IsPrivateTarget` 下沉、`api.go` switch 两个 case），
  `git revert` 对应提交即可，无数据迁移、无配置迁移。
- **D6 是唯一触及既有行为的改动**：`proxyentry.IsPrivateTarget` 的导出签名与语义不变，
  回滚它只需把实现体搬回去；`internal/routing` 的新函数是纯增量。
- `server.vpn.enabled` 默认 `false`，因此即使本阶段被合并而不回滚，
  已部署实例的行为也完全不变——这本身就是止损开关。
- 若阶段 5 的前端已经接上而阶段 4 被回滚，前端会收到 404；
  因此阶段 4 与阶段 5 的合并顺序不可颠倒（规格 §15 的依赖链 4 → 5）。
- 稳定错误码一旦发布不得改名；若本阶段未合并即废弃，`vpn_*` 码可重新设计，
  但已写进 `docs/api/openapi.yaml` 的码在合并后即成契约。
