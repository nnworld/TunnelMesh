# VPN 网关 阶段 4：纯逻辑包与管理 API

## 标题

`feat(vpn): land the pure logic package and the peer management api`

本记录覆盖分支上阶段 4 的全部提交（计划 → 私网判定下沉与错误码 → 密钥 → IP 池 → PeerSpec 与规范编码 →
逐包策略 → ini 渲染 → `server.vpn` 配置 → 规格校验拆分 → Service → API → OpenAPI → 收口），
而不只是最后一个提交。

## 目标分支

`main`

本分支 stacked 在 `codex/vpn-phase3-schema-v15` 之上。阶段 1 与阶段 3 的 PR 合并进 `main` 后，
必须先 `git rebase --onto main codex/vpn-phase3-schema-v15` 再更新本 PR，
否则 diff 会重复包含阶段 3 的产物（Schema v15 的两张表与两个 Repository）。

## 关联记录

- 实施计划：[VPN 网关 阶段 4：纯逻辑包与管理 API Implementation Plan](../superpowers/plans/2026-09-21-vpn-phase4-pure-logic-and-api.md)
- 设计规格：[内嵌 VPN 网关（WireGuard）设计](../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md)（§5.1 密钥、§8 配置、§9 管理 API、§12.1/§12.2 稳定码、§14 测试策略、§15 阶段 4）
- 决策载体：[ADR 0002: Open a public UDP ingress for an embedded VPN gateway](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)（`Status: Accepted`）
- 前置阶段：[VPN 网关 阶段 3：Schema v15 存储层](2026-09-20-vpn-phase3-schema-v15.md)、[VPN 网关 阶段 1：约束反转与 ADR 0002](2026-09-20-vpn-phase1-constraint-reversal.md)
- 接口契约：[docs/api/openapi.yaml](../api/openapi.yaml) 的 6 条 `/api/v1/vpn-*` 路径
- 配置文档：[配置参考 · 内嵌 VPN 网关](../operations/configuration.md)、[配置示例](../operations/config-examples.md)

## 摘要

阶段 4 交付内嵌 VPN 网关的**管理面**：一个零 I/O 的纯逻辑包、`server.vpn` 配置块、peer 生命周期
Service，以及规格 §9 的 9 个 HTTP 操作与同步的 OpenAPI 契约。**本阶段不转发一个数据包**：
没有 TUN 设备、没有 netstack、没有 UDP 监听、没有 `//go:build vpn`、没有新增 Go 依赖
（`go.mod`/`go.sum` 零改动）、没有 Schema 改动（`SchemaVersion` 保持 15）、没有前端与部署产物改动。

五类产物：

1. **`internal/vpn/` 纯逻辑包**（7 个实现文件 1297 行 + 6 个测试文件 1471 行 + 2 份黄金 ini）：
   `errors.go` 是 10 个稳定错误码的唯一声明处；`keys.go` 只用 `crypto/ecdh` 与 `encoding/base64`
   生成 WireGuard 兼容密钥（与 RFC 7748 §6.1 官方向量逐字节一致）；`ippool.go` 把 `ip_pool` 切成
   每节点一个 `/N`（上限 4096 个子网，第一个可用地址保留给节点自己的接口）；`peerspec.go` 是
   `allowed_ips`/`allowed_ports` 规范编码的**唯一所有者**（排序去重，因此同一组 CIDR 的两种书写顺序
   编码成同一字符串）；`policy.go` 是 7 步逐包判定；`config.go` 渲染 wg-quick ini。
   全包只 import 标准库与 `internal/routing`，不认识 `storage.VPNPeer`，也不认识 `http.ResponseWriter`，
   因此可以脱离数据库与网络穷举单测。
2. **`server.vpn` 配置块**（`internal/config/config.go` +129 行，16 个键全部登记默认值）：
   形状对标 `server.proxy_entry`，默认 `enabled: false`；`ip_pool` 与 `node_subnet_size`
   直接交给 `vpn.ParsePool` 校验，所以「能启动」等价于「真的能分出地址」，加载器与分配器不会各说一套。
   节点自身的 WireGuard 私钥**不是配置项**（只从环境变量注入），因为它会被复制、备份、打进支持包。
3. **`VPNPeerService`**（`internal/server/vpn_peer_service.go`，989 行）：签发、读取、列表、修改、
   轮换、吊销、reveal。它是 22 列持久化行与领域 `PeerSpec` 之间**唯一**的映射处，
   这正是 `internal/vpn` 能保持纯净的原因。签发顺序是安全论证的一部分（见「安全与授权影响」）。
4. **管理 API**（`internal/server/vpn_peer_api.go`，580 行 + 装配 3 处）：7 个端点可用，
   2 个数据面依赖端点注册后以 `501 vpn_not_implemented` 拒绝。
   错误渲染把稳定码放进 `data.error`、人类可读细节放进 `msg`，与 `writeProxyEntryError` 同一分工，
   因此一个客户端解析器同时覆盖 tp-* 入口与管理 API。
5. **OpenAPI 同步 + 3 个契约守卫测试**（`docs/api/openapi.yaml` +309 行、
   `internal/server/vpn_peer_openapi_test.go` 193 行）：文档里的操作集合等于实际派发的集合、
   恰好两个操作带 `x-tunnelmesh-phase: 6`、文档公布的码枚举与 `internal/vpn/errors.go` 的 sentinel
   双向相等、每条文档化操作都被活 API 以非 404 应答。served-operation 列表是字面量写死的，
   因此「加了路由没写文档」或「写了文档没有路由」都会在构建期失败，而不是等到复审。

## 用户影响

- **默认零影响。** `server.vpn.enabled` 默认 `false`，升级后进程不生成密钥、不切 IP 池、
  不启动任何 goroutine、不监听任何新端口。已部署的 Server / Agent / Client 行为完全不变，
  不需要迁移、不需要新环境变量、不需要改 `Dockerfile`/`deploy/`/nginx/前端产物。
- **本版能做什么**：开启 `server.vpn` 后，管理 API 可以签发、列出、查看、修改、轮换、吊销 peer，
  每次操作写审计。这些 peer 是真实的数据库记录，占用真实的地址，阶段 6 的数据面会直接装载它们，
  **不需要重新签发**。
- **本版不能做什么（必须如实告知用户）**：
  - 不能连通。没有任何进程监听 `server.vpn.listen`，防火墙也不需要为本版放行 UDP 51820。
  - **不能下载客户端配置文件**。`POST /api/v1/vpn-peers/{peerId}/config:reveal` 在本版恒返回
    `409 vpn_node_disabled`（消息为「this node has no usable wireguard public key…」），
    因为节点还没有自己的网关身份。渲染一个 `[Peer] PublicKey` 为空的 ini 只会让用户在自己的机器上
    遇到一个指不回服务端设置的导入错误，所以选择明确拒绝。
  - `GET /api/v1/vpn-peers/{peerId}/flows` 与 `GET /api/v1/vpn-nodes` 返回 `501 vpn_not_implemented`，
    **不是** `200 {"items":[]}`：空列表会被控制台读成「没有活跃流」/「没有节点服务 VPN」，
    而这正是 `AGENTS.md`「优雅降级不得返回兜底假数据」要防的事。
  - `icmpEnabled: true` 的签发请求一律 `409 vpn_agent_capability_missing`。能力协商
    （`stream_icmp_echo.v1`）属阶段 7，本版返回成功会造出一个用户以为能 ping、实际不能 ping 的 peer。
- **签发需要 `TUNNELMESH_TOKEN_ENCRYPTION_KEY`。** peer 私钥以 AES-256-GCM 密封入库（与凭据密文同构），
  密钥缺失时签发与 reveal 返回 `503 credential_secret_unavailable`，不降级为明文存储。
  这是既有身份/凭据特性已经在用的同一个变量，不是本版新增的注入项。
- **集群里每个节点必须配置相同的 `ip_pool` 与 `node_subnet_size`**，否则子网租约会互相拒绝。
  节点通过 `vpn_ip_leases` 各抢占一个子网并由 epoch fencing 保护；节点重启后**复用同一个子网**，
  因为换到别的 `/24` 会让已签发的 peer 全部持有网关不再应答的地址。

## API、Schema 与配置影响

**API（9 个操作 / 6 条路径，全部在 `/api/v1`，统一 `{ code, msg, data }`）**

| 方法与路径 | 本版行为 |
| --- | --- |
| `GET /api/v1/vpn-peers` | cursor 分页列表；owner 限制由 principal 推导，不接受查询参数放宽 |
| `POST /api/v1/vpn-peers` | 签发；响应含 `configRevealPath` |
| `GET /api/v1/vpn-peers/{peerId}` | 详情；不可见与不存在同为 `404 vpn_peer_not_found` |
| `PATCH /api/v1/vpn-peers/{peerId}` | 部分更新；指针字段承载「是否出现」，空数组是有意义的值 |
| `DELETE /api/v1/vpn-peers/{peerId}` | 吊销（终态，不删行） |
| `POST /api/v1/vpn-peers/{peerId}/rotate` | 轮换密钥对，**不换 IP** |
| `POST /api/v1/vpn-peers/{peerId}/config:reveal` | 本版恒 `409 vpn_node_disabled`；契约与纪律已落地 |
| `GET /api/v1/vpn-peers/{peerId}/flows` | `501 vpn_not_implemented`，`x-tunnelmesh-phase: 6` |
| `GET /api/v1/vpn-nodes` | `501 vpn_not_implemented`，`x-tunnelmesh-phase: 6` |

- 分页沿用既有 `queryLimit`（默认 50、上限 500）与 cursor 语义，不使用 offset；
  Service 再用 `clampVPNListLimit` 夹一次同样的 50/500，因此直接调用 Service 的调用方
  （阶段 5 的后台聚合、阶段 6 的装载）也拿不到超过上限的一页。
- `Idempotency-Key` 覆盖 create / patch / revoke / rotate 四个写操作；
  **reveal 刻意不入幂等库**——该库持久化响应体，而这个响应体含私钥。reveal 仍强制要求幂等键，
  但只把它当作确认信号，不用作缓存键（与 `tokens/{tokenId}/reveal` 同一取舍）。
- **10 个稳定错误码**：规格 §12.1 表列的 9 个（含复用的 `credential_secret_unavailable`）
  加上计划 D12 引入的 `vpn_not_implemented`(501)。合并后这些字符串即成公开契约，不得改名。
- 路由按「形状优先、方法其次」派发：未知子资源是 404，已知子资源用错动词是 405，二者不合并，
  否则控制台无法区分拼写错误与实现缺陷。

**Schema**：零改动。`SchemaVersion` 保持 `15`，`migrations/` 与 `internal/storage/**` 零 diff，
本版只消费阶段 3 交付的 `db.VPNPeers()` / `db.VPNIPLeases()` 两个接口与四个 sentinel。
因此本版**不需要数据库迁移、不需要维护窗口、不需要备份窗口**。

**配置**：新增 16 个 `server.vpn.*` 键（`enabled`/`listen`/`endpoint_host`/`ip_pool`/
`node_subnet_size`/`mtu`/`max_peers`/`max_flows_per_peer`/`max_flows_total`/`packet_rate_per_peer`/
`connect_timeout`/`idle_timeout`/`shutdown_timeout`/`icmp_enabled`/`icmp_timeout`/
`icmp_max_concurrent`），全部登记进默认值 map，因此 `mapstructure` 解码后不是零值而是文档承诺的默认值。
三个「0 表示不限」的计数沿用 `max_concurrent_tunnels` 的既有约定；`icmp_timeout` 与
`icmp_max_concurrent` 只在 `icmp_enabled=true` 时校验，所以关掉 ICMP 不会因为遗留占位值而无法启动。
`enabled: false` 时 `validateVPN` 不贡献任何 problem，因此从示例文件继承来的过期 vpn 块不会卡住启动。
命令行 flag 与 `TUNNELMESH_SERVER_VPN_*` 环境变量等价，优先级仍是 flag > env > file > default。
`docs/operations/configuration.md`（+72 行）给出全部键的默认值、取值范围与必填条件，
`docs/operations/config-examples.md`（+90 行）在本地 / 集群 MySQL / 集群 etcd 三份示例里同步。

## 安全与授权影响

- **签发顺序就是安全论证。** Agent 授权与节点配额在任何分配之前完成，所以被拒的请求不留痕迹；
  子网租约先于 `/32` 分配，因为两个节点绝不能同时认为自己拥有同一个 `/24`；密钥对在分配之后生成，
  所以拿不到地址的请求不产生密钥材料；派生计数器在 insert 前一刻 +1，而 insert 是唯一还能失败的步骤，
  因此也是唯一需要回滚的步骤（`AddAllocated(-1)`，尽力而为且不上报——计数器偏高只损失容量，
  不影响正确性，防止一址两配的权威约束是 `vpn_peers.UNIQUE(node_id, vpn_ip)`）。
- **越权读一律 404，不是 403。** 403 会确认 ID 存在，让外部逐个枚举 peer ID。
  可见性解析发生在 reveal 的三个前置条件之前，也发生在 flows 路由的 501 之前，
  因此这两条路径都不能被用来探测哪些 peer ID 存在。
- **权限过滤在分页语义内完成。** owner 限制被注入 `VPNPeerFilter` 由 Repository 的 `WHERE` 执行，
  不是查完再丢弃，因此无权行不可能出现在任何一页（`AGENTS.md` 同名条款）。
- **私钥只在一个响应体里出现一次。** 四个密封列（ciphertext/nonce/key_id/version）
  在 `vpnPeerResponse` 类型里**根本不存在**，不是 `omitempty`：类型缺席才能防止未来某个字段
  顺手把密文回显进浏览器缓存、代理日志或控制台状态。reveal 要求
  `X-VPN-Config-Reveal-Confirm` + `Idempotency-Key` + `acknowledgeRisk: true`，
  响应 `Cache-Control: no-store`，并写审计；审计只记「发生了 reveal、哪个 peer、哪个节点」，
  details 不含任何密钥材料。日志、指标、错误消息同样不含私钥、包载荷、握手字节、DSN 与 Token。
- **逐包策略的 7 步判定顺序是安全语义，不可调换**：分片 → `fragment_dropped`（非首片无法判定端口，
  放行等于绕过端口白名单）；超长 → `oversize_dropped`；协议号白名单（TCP=6/UDP=17/ICMP=1，
  ICMP 还要 peer 开启）；`routing.IsDangerousAddress` → `metadata_denied`（云 metadata、组播、
  非路由地址恒拒，与 tp-* 和 Agent policy 共用同一 deny list）；私网开关 → `target_denied`；
  peer `AllowedIPs` 不含目标 → `target_denied`；端口不在 `AllowedPorts` → `port_denied`。
  `error_class` 字符串逐字来自规格 §12.2，因为它们会直接成为 Prometheus 的低基数标签值；
  其中 `peer_unknown`/`egress_*`/`icmp_timeout`/`stack_error` 等本版只定义常量、不产生。
- **私网判定只有一份实现（D6）。** `proxyentry.IsPrivateTarget` 是导出的既有 API，
  `internal/vpn` 需要同一判定，但两者互不 import。三个选项里：复制一份违反 DRY 且这是安全判定；
  让 VPN 领域 import tp-* 入口方向错误；因此把实现下沉到两包都已依赖的 `internal/routing`
  （它已经是 `IsDangerousAddress` 的权威包），`proxyentry.IsPrivateTarget` 保留为一行委托，
  导出签名与语义逐字不变，既有调用方与测试零改动。
- **ini 渲染带注入守卫。** 每个渲染值都过一遍拒绝控制字符、`;`、`#` 与空格的检查。
  今天它是纵深防御（每个字段各有校验器），但 ini 在换行处终止值、在 `;`/`#` 处开始注释，
  所以将来任何新增字段若绕过守卫就是一个配置注入洞。base64 的 `=` 填充仍然合法
  （每个 32 字节密钥都以 `=` 结尾）。空的出口策略被拒绝而不是渲染出来：
  一个什么都不路由的配置文件看起来是配好的，用户会当成隧道故障去排查。
- **不实现的能力仍然不实现。** 本版没有 ICMP 发送、没有 TUN/L2、没有 P2P NAT traversal、
  没有任意远程命令执行；`icmp_enabled` 只是一个持久化的策略位，语义由阶段 6/7 落地。
- **未接线的构建不像「没有这个功能」。** Service 由 `SetVPN` 在运行时装配，未装配时为 nil，
  Handler 答 `503`（而不是 404），因为 404 会被读成「无此端点」。

## 测试证据

TDD 全程实测，每个提交的红灯原文如下（不是估算，是当时终端输出的首行）：

| 提交 | 任务 | 红灯原文（首行） |
| --- | --- | --- |
| `a7a76b6` | T1 私网判定下沉 + 错误码 | `internal/routing/routing_test.go:152:7: undefined: IsPrivateTarget` |
| `32bf2b8` | T2 密钥 | `internal/vpn/keys_test.go:30:19: undefined: vpn.DerivePublicKey` |
| `a18bd52` | T3 IP 池 | `internal/vpn/ippool_test.go:10:56: undefined: vpn.Pool` |
| `e5ae6fa` | T4 PeerSpec 与规范编码 | `internal/vpn/peerspec_test.go:14:22: undefined: vpn.PeerSpec` |
| `5b13ad6` | T5 逐包策略 | `internal/vpn/policy_test.go:15:18: undefined: vpn.ErrorClass` |
| `2a11fb8` | T6 ini 渲染与黄金文件 | `internal/vpn/config_test.go:13:30: undefined: vpn.PeerConfig` |
| `1c9cc7a` | T7 `server.vpn` 配置 | `internal/config/config_test.go:1266:20: cfg.Server.VPN undefined (type config.ServerConfig has no field or method VPN)` |
| `37f40ca` | 规格校验拆分 | `peerspec_test.go:270:11: undefined: vpn.ParseAllowedIPList` |
| `950ad86` | T8 Service | `vpn_peer_service_test.go:28:18: undefined: server.VPNPeerService` |
| `4434b41` | T9 API | `vpn_peer_api_test.go:66:6: api.SetVPNPeerService undefined` |
| `b3b97fb` | T10 OpenAPI | 计划未设独立红灯步骤（见「与计划的偏差 8」），文档与 3 个守卫测试同一提交落地 |

T6 的黄金文件是在实现存在之前照 wg-quick 格式手写的，这使它们是契约而不是「代码碰巧产出的快照」。

绿灯与门禁（全部实测，非引用）：

- `go build ./...` 通过；`go vet ./...` 干净。
- `go test ./... -count=1`：23 个包 `ok`、5 个包 `no test files`、0 个 `FAIL`。
- `go test -race ./... -timeout 30m -count=1`：全绿，退出码 `0`，无 `DATA RACE`、无 `FAIL`
  （最慢 `internal/server` 443.684s、`internal/storage` 13.441s）。
- `gofmt -l internal/vpn internal/routing internal/proxyentry internal/config internal/server` 无输出。
- `git diff --check` 干净；`git status --porcelain go.mod go.sum` 为空（**依赖零改动**，
  `gvisor.dev/gvisor` 与 `golang.zx2c4.com/wireguard-go` 属阶段 6）。
- 文档主张守卫 `go test ./scripts/ -count=1` → `ok`（本版新增用户文档没有宣称数据面可用）。
- 新增测试函数：`internal/vpn` **55** 个（config 8 / errors 3 / ippool 11 / keys 7 / peerspec 12 /
  policy 14）；`internal/server` 的 service **21** 个、api **12** 个、openapi 契约 **3** 个；
  `internal/config` 与 `internal/routing` 是在既有测试文件里新增用例（+273 行 / +30 行）。
- 覆盖的关键行为：密钥与 RFC 7748 官方向量逐字节一致、非法 base64 与长度被拒；子网切分数量与
  4096 上限、网络/广播/节点地址被跳过、耗尽返回 `vpn_ip_pool_exhausted`；规范编码的排序去重与往返、
  含逗号的数组元素被拒；7 步策略逐步覆盖（含分片与协议号白名单）；ini 黄金文件与注入守卫；
  16 个配置键的默认值、优先级（flag > env > file > default）、非法值快速失败、
  规格示例被逐字加载；Service 侧的签发顺序与计数回滚、吊销终态、已吊销 peer 轮换 → 409、
  他人 peer → 404、`enabled: false` → 409、`max_peers` 触顶 → 503、密钥密封后明文不入库；
  API 侧的 401 / 404（而非 403）/ 分页内权限过滤 / 幂等重放不产生第二个 peer /
  reveal 三个前置条件各自缺失时的 400 / `Cache-Control: no-store` / 两个 501。
- 变异校验（改动实现后测试必须失败，已实测）：去掉 ini 里 `=` 两侧的空格 → 黄金文件失败；
  去掉 `;`/`#` 守卫 → 构造的 endpoint 用例失败（这类输入能活过 `net.SplitHostPort`，
  因此是唯一能验证守卫的路径）。
- MySQL 侧不适用：本版无 Schema 改动，Service 与 API 测试跑在 SQLite 上，
  MySQL 契约由阶段 3 的 Repository 测试覆盖（本机无实例时如实 SKIP）。
  前端未改动，`npm test -- --run` 与 `npm run build` 不是本版门禁；无 Docker/Compose 改动。

## 与计划的偏差（全部为收紧或事实更正，无功能缩水）

1. **多出一个提交 `37f40ca`。** 计划把 `PeerSpec` 校验一次做完（T4），实现 Service 时才发现部分更新
   必须走同一套字段规则、但**不能**继承「过期时间必须在未来」这一条——否则一个已过期的 peer
   连改名和收窄策略都做不了，运维就没有办法缩短它的生命周期。于是拆成
   `ValidateFields`（调用方提供的规则）/ `ValidateRequest`（签发与写 `expires_at` 的 patch 追加未来性）/
   `Validate`（含服务端赋值的身份字段的整记录校验），并新增 `ParseAllowedIPList`：
   JSON 数组逐项解析，因为把调用方的数组 join 起来再交给 `ParseAllowedIPs`
   会让一个含逗号的元素变成两条规则。
2. **配置键是 16 个，计划 T7 写「全部 15 个键」**（漏数一个）。默认值 map、校验函数与文档三处都是
   16 个，实测一致；计划文本是时点记录，不改写，差异记在这里。
3. **`internal/vpn/errors.go` 交付 10 个稳定码，规格 §12.1 表列 9 个。** 多出的
   `vpn_not_implemented`(501) 由计划 D12 引入（两个数据面依赖端点返回 501 而不是 200 空数据）。
   规格是时点记录不改写；OpenAPI 的码枚举与 sentinel 双向相等由守卫测试保证。
4. **计划 §6 的文件清单未列 `internal/server/runtime.go` 与 `internal/cli/root.go`。**
   `SetVPN` 的装配必须经过这两处（`RuntimeConfig` 新增 `VPN` 字段 + 一行装配、
   `server` 子命令传入 `cfg.Server.VPN`）。因此计划 §9 说的「两处受控修改」实际是四处：
   `proxyentry/target.go` 委托、`api.go` 两个 case、`runtime.go` 一个字段与一行、`cli/root.go` 一行传参。
   四处都是纯增量接线，无行为变更。
5. **规格 §14 的两条配置测本阶段不实现**：「`enabled: true` 但缺
   `TUNNELMESH_VPN_NODE_PRIVATE_KEY` 时快速失败」与「无 tag 构建下 `enabled: true` 快速失败」都属阶段 6。
   本版没有 build tag（计划全局约束 10），也没有任何东西监听 UDP，`enabled: true` 只买到管理面，
   因此这两条快速失败还没有可失败的对象。文档已写明该环境变量本版不被读取。
6. **`config:reveal` 在本版恒返回 409。** 计划 D10 描述的是它可用时的纪律；实现保留
   `VPNPeerServiceDeps.NodePublicKey` 供阶段 6 的网关运行时注入，
   测试用显式注入身份的 Service 覆盖成功路径、`no-store` 与审计，
   运行时装配（`SetVPN`）则不传身份，因此真实部署得到 409 而不是一份坏配置。
7. **T11 收口时更正了活文档措辞。** `docs/operations/configuration.md`（2 处）与
   `docs/operations/config-examples.md`（三份示例里的同一段注释）原先写成「可以签发、吊销、轮换 peer
   并下发客户端配置文件」，与本版 reveal 恒 409 的事实不符；同时补明
   `TUNNELMESH_VPN_NODE_PRIVATE_KEY` 本版不被读取、由阶段 6 消费。这是事实更正，不是功能变更；
   更正后两份文档相对阶段 3 分别是 +72 行与 +90 行（T7 提交时为 +72 与 +87，差值即本次改写）。
8. **T10 没有独立的红灯步骤。** 计划 T10 只写了 Step 1（文档 + 一个断言路径集合一致的测试）与
   Step 2（提交），没有「先跑失败测试」这一步，因此本记录不声称有红灯原文。实际交付的是 3 个守卫测试
   （操作集合等于派发集合且恰好两个带 phase 标记、码枚举与 sentinel 双向相等、
   每条文档化操作被活 API 以非 404 应答），它们的作用是**从此刻起**让文档与实现不能各自漂移；
   契约文档本身是先写文档后补测试的顺序，这一条如实记录而不追溯美化。

## 顺带发现、本阶段刻意不修的既有问题

按「只修根因、不顺手改无关代码」的纪律，以下四项只记录不修改：

1. **幂等层没有「响应不可持久化」这个一等概念。** `a.mutate` 会把响应体写进幂等存储，
   因此两处 reveal（既有 `tokens/{tokenId}/reveal` 与本版 `vpn-peers/{peerId}/config:reveal`）
   都只能各自绕开它、靠自己记住「这个响应含密钥」。根治要给幂等层加一个 no-store 标志或
   响应体豁免机制，属独立计划；本版沿用既有手法并在注释里写明原因。
2. **`routing.Policy.Validate` 只接受 IPv4**（`ip.To4() == nil` 即拒），
   `vpn.PacketPolicy` 包装它，因此 VPN 策略天然继承「IPv4-only」。规格未把 IPv6 列入本阶段目标，
   故不修；但阶段 6 的 netstack 若要接 IPv6，必须先改 `internal/routing`，
   而不是在 `internal/vpn` 里另开一条判定路径。
3. **`internal/server/api.go` 的顶层 `switch parts[0]` 继续变长**（本版 +2 个 case）。
   这是既有的派发结构，重构成路由表会触及所有既有端点，属独立重构，不在本阶段。
4. **`internal/vpn` 与 `internal/proxyentry` 各自持有一套「稳定码 + HTTP 状态」的 Error 类型**
   （`vpn.Error` 与 `proxyentry.Error` 形状相同）。本版按计划照抄范式而不是抽公共类型：
   抽出来意味着改动既有 tp-* 入口的公开错误面，风险大于收益。若将来出现第三套，再按 DRY 抽象。

## 发布步骤

1. **合并顺序不可颠倒**：阶段 1 → 阶段 3 → 阶段 4；阶段 5（管理后台）必须在本阶段之后合并，
   否则前端会收到 404。核心模块改动需独立复审。
2. 本版**不需要**数据库迁移、不需要备份窗口、不需要新环境变量、不需要改 `Dockerfile`/`deploy/`/
   nginx/前端产物。直接滚动升级即可。
3. 默认发布即安全：`server.vpn.enabled` 保持 `false`，升级后行为与旧版逐字一致。
4. 若要灰度开启**管理面**：确认 `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 已注入 →
   每个节点写入**相同**的 `server.vpn.ip_pool` 与 `node_subnet_size` → 填 `endpoint_host`
   （只写主机名，不带端口）→ `tunnelmesh-server check-config` 通过 → 先升一个节点，
   观察健康检查与既有链路（登录、Agent 注册、托管路由、`tp-*`）→ 再滚动其余节点。
5. **不要在本版对外宣布 VPN 可用**，也不要为它放行公网 UDP：`listen` 即使配置了也没有进程监听。
   对外措辞统一为「已批准、分阶段实施中、当前版本提供管理面，数据面尚未随发行版提供」。
6. 升级后校验：`enabled: false` 时 `GET /api/v1/vpn-peers` 返回 200 空列表、写操作返回
   `409 vpn_node_disabled`；`enabled: true` 时签发一个测试 peer 得到 201 与 `vpnIp`，
   `config:reveal` 得到 `409 vpn_node_disabled`，`GET /api/v1/vpn-nodes` 得到 `501 vpn_not_implemented`；
   `SELECT COUNT(*) FROM vpn_peers` 与签发次数一致；`vpn_ip_leases` 里本节点持有一个子网且
   `allocated_count` 与 peer 数一致；审计里出现 `vpn_peer_issued` 且 details 只含
   `nodeId`/`agentId`/`vpnIp`；升级日志不含 DSN、密码、Token 与任何密钥材料。
7. 清理灰度数据：吊销测试 peer（`DELETE`）而不是删行——吊销是终态且没有 Delete 路径，
   退役的公钥与它占过的地址保持可审计。

## 回滚步骤

- **代码侧**：本版全部是新增文件 + 四处受控接线，`git revert` 对应提交即可，无数据迁移、无配置迁移。
  回滚必须以任务边界为单位成组进行（T10 → T9 → T8 → … → T1）：
  只回滚 T9 会留下无人调用的 Service（仍可编译）；只回滚 T1 会让 `proxyentry` 的委托指向
  不存在的 `routing.IsPrivateTarget`，**编译直接失败**。因此 T1 必须最后回滚，
  或与它的全部消费者同时回滚。
- **5 分钟止损**：`server.vpn.enabled: false` 就是止损开关，不需要回滚二进制即可关掉全部写操作
  （读操作返回空列表，写操作返回 `409 vpn_node_disabled`）。本版没有数据面，
  因此不存在「关掉监听」这个动作。
- **数据侧**：已签发的 peer 行保留在 `vpn_peers` 里，回滚代码不会删它们；
  重新上线也**不需要重新签发**（公钥与地址由唯一约束保护，是既成事实）。
  若确需清空，先确认没有用户已经拿到过配置，再按阶段 3 的回滚说明处理表本身。
- **契约侧**：10 个稳定错误码一经合并即成公开契约，不得改名（需要 deprecation 窗口）。
  若本 PR 未合并即废弃，`vpn_*` 码可以重新设计；但 `docs/api/openapi.yaml` 里已发布的枚举
  在合并后受同一约束。
- **D6 的回滚**：`proxyentry.IsPrivateTarget` 的导出签名与语义未变，回滚它只需把实现体搬回
  `internal/proxyentry/target.go`；`internal/routing` 的新函数是纯增量，删掉不影响既有调用方。

## Reviewer 关注点

1. **`config:reveal` 恒 409 是否可接受？** 这是本版最需要产品判断的一条：端点、契约、审计、
   `no-store` 与全部测试都已就位，只差阶段 6 注入节点身份。替代方案是现在就让 Server
   生成并持久化自己的 WireGuard 密钥对，但那会把「节点身份从哪来」这个数据面决策
   提前到管理面，且与规格 §5.1「节点私钥只从环境变量注入」冲突。请确认这个边界划得对。
2. **`icmpEnabled: true` 一律 409 `vpn_agent_capability_missing`。** 错误消息里写明了该能力属阶段 7。
   请确认「拒绝签发」比「签发一个不能 ping 的 peer」更符合产品预期。
3. **`allocateAddress` 每个候选地址查一次库**（`GetByNodeAndIP`），最坏情况下一次签发在一个
   `/24` 里做 253 次点查。权威约束是 `vpn_peers.UNIQUE(node_id, vpn_ip)`，
   因此即使两次并发签发选中同一地址，也只有一个 insert 成功（另一个映射为
   `409 vpn_peer_conflict` 并提示可重试）。请确认这个「查询只是优化、唯一索引才是正确性」的取舍
   在阶段 6 的批量装载里仍然成立；若要消除点查，应改成一次 `ListByNode` 取回已占用集合，
   属独立优化。
4. **每节点一个子网、重启复用。** `leaseNodeSubnet` 先 `ListByHolder` 找自己已持有的子网并续租，
   找不到才升序扫描抢占一个无活租约的子网。租约 TTL 是 1 分钟
   （`vpnSubnetLeaseTTL`，与 Repository 默认一致），因此一个停止签发的节点最多 1 分钟后
   其子网可被接管，而接管会继承 `allocated_count`（阶段 3 的既有语义）。
   请确认 1 分钟这个值在阶段 6 引入周期性续租 goroutine 后仍然合适。
5. **`vpn_node_disabled` 承载了两种含义**：配置开关关闭，以及节点缺少网关身份
   （`vpnNodeMisconfigured` 复用它并附上具体原因）。规格 §12.1 只定义了前者。
   合并这两种语义的理由是：两者对调用方的动作相同（去检查 `server.vpn`），
   而为一个「节点配置不完整」新增第 11 个稳定码会把契约面扩大。请确认这个取舍可接受。
6. **4096 子网上限**是 `ParsePool` 的硬失败条件：没有上限时一个 `/8` 配 `/24` 会产生 65536 个
   `*net.IPNet`，一次配置错误就能把内存打满。`Pool.Subnets()` 每次调用都**深拷贝**整份切片
   （返回副本是为了让调用方无法改动后续分配看到的列表），目前只出现在
   `leaseNodeSubnet` 的兜底扫描路径上——节点一旦持有租约就走续租分支，不再调用它。
   请确认阶段 6 的周期性续租与网关装载不会把它挪进热路径。
7. **OpenAPI 的 `VPNPeer` projection 用 `additionalProperties: false`**，
   因此「四个密封列缺席」是契约而不是习惯：将来任何字段都不可能悄悄把 ciphertext、nonce 或 key id
   回显进浏览器缓存。请确认这个约束不会被后续的 schema 生成工具抹掉。
8. **时点记录零改写核验**：`git diff --name-status codex/vpn-phase3-schema-v15..HEAD --
   docs/superpowers docs/pull-requests docs/architecture/adr` 只出现本阶段计划与本记录的 `A`
   以及生成索引的 `M`；**没有任何既有 spec/plan/PR 记录或已接受 ADR 被修改**。
   本版对阶段 3 计划里的事实更正（配置键数量）也只记录在本文件，没有回改计划正文。
9. **活文档措辞**：`README.md`、`README.zh-CN.md` 关于 VPN 的句子仍是阶段 1 写的
   「已批准并分阶段实施，当前版本尚未提供」，本版没有改动它们，因为该措辞仍然为真。
   `docs/operations/configuration.md` 与 `config-examples.md` 的更正见「与计划的偏差 7」。

## 集成状态

阶段 4 的全部任务已在 `codex/vpn-phase4-pure-logic-and-api` 上完成并推送：私网判定下沉与错误码、
密钥、IP 池、PeerSpec 与规范编码、逐包策略、ini 渲染、`server.vpn` 配置、规格校验拆分、Service、
API、OpenAPI 同步、全量门禁与本记录。仓库门禁全绿（含 `-race`），依赖零改动，
Schema 与前端零改动，文档主张守卫绿灯，文档索引由 `scripts/gen_doc_index.py` 生成且幂等。

阶段 5-8 全部以本阶段为前提：管理后台（阶段 5，消费这 9 个操作与 10 个稳定码）、
Server 数据面（阶段 6，注入节点身份并让 `config:reveal`、`flows`、`vpn-nodes` 真正可用）、
Agent ICMP 扩展（阶段 7，让 `icmpEnabled` 可以签发）、可观测性与部署（阶段 8）。
回滚本阶段即冻结 VPN 特性的全部后续阶段，但不影响任何既有能力
（本版零 Schema 改动、零依赖、零前端、零部署产物，默认配置下行为与旧版逐字一致）。
PR 复审与合并待进行；合并顺序上必须在阶段 1 与阶段 3 之后。
