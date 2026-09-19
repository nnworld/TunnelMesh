# 内嵌 VPN 网关（WireGuard）设计

- 日期：2026-09-19
- 状态：设计已确认，待编写实施计划
- 分类：Architectural（brainstorming 路径）
- 关联文档：[托管路由 HTTP 代理入口（tp-*）设计](2026-09-13-managed-route-http-proxy-entry-design.md)、[架构概览](../../architecture/overview.md)、[配置参考](../../operations/configuration.md)、[测试与验证](../../development/testing.md)
- 待创建：`docs/architecture/adr/0002-public-ingress-and-embedded-vpn.md`（本设计的决策载体，见第 16 节；实施阶段 1 交付后回填为链接）

## 1. 背景与目标

`tp-*` HTTP 代理入口解决了"免安装浏览器/curl 代理"，但只覆盖 HTTP CONNECT 与绝对形式请求。用户若要用原生 VPN 客户端（系统级路由、任意应用的透明代理、ping 内网），今天仍必须在本机安装并运行 `tunnelmesh-client`。

本次新增内嵌 VPN 网关：管理员在后台签发 WireGuard peer 配置，用户用**原生 WireGuard 客户端**导入即可，Server 公网 UDP 端口作为 VPN 端点，出口仍由指定 Agent 承担，访问控制仍由 Server 统一执行。

目标：

1. Server 内置 WireGuard 端点，签发/吊销 peer 配置，分配 VPN IP。
2. VPN 流量经既有 stream 链路到达指定 Agent，Agent 用现有拨号能力连接内网目标。
3. 支持 TCP、UDP 与 ICMP echo（内网可 ping）。
4. 目标访问控制在 Server 侧逐包执行，复用既有 `routing.Policy`。
5. Agent 不新增特权、不维护防火墙规则、不需要内网路由改造。
6. 完整的管理后台：peer 生命周期、IP 池、节点 VPN 状态、配置下发与私钥 reveal。

非目标（本轮明确不做）：

- 不实现 OpenVPN、IPsec、L2TP、Shadowsocks 等其它 VPN/代理协议。
- 不实现 P2P NAT traversal（`AGENTS.md` 保留项）。
- 不实现任意远程命令执行（`AGENTS.md` 保留项）。
- 不转发 L2 以太网帧；不做真正的端到端 L3（源地址在 Agent 侧被改写，见第 4.3 节能力边界）。
- 不支持 IPv6 数据面（WireGuard 传输与 netstack 均具备能力，但内网目标以 IPv4 为主，ICMPv6/NDP 复杂度显著，留待后续）。
- 不做一 peer 多出口负载与故障转移。
- 不引入第四个二进制；VPN 数据面运行在 `tunnelmesh-server` 进程内。

## 2. 前置事实与硬约束（已核实）

| 事实 | 证据 | 影响 |
|---|---|---|
| `relay.StreamRequest` 已含 `Protocol`/`TargetHost`/`TargetPort` | `internal/relay/service.go:19` | IP 五元组可直接映射为 stream 请求，relay 层零改动 |
| Agent 已按 `tcp`/`udp`/`http` 分派，未知协议安全拒绝 | `internal/agent/dialer.go:105` 的 switch，default 返回 `agent: unsupported stream protocol` | 旧 Agent 收到新协议天然拒绝，无需额外兼容分支 |
| `Dialer.DialTCP`/`DialUDP` 是现成的内网出口 | `internal/agent/dialer.go:44`、`:93` | TCP/UDP 数据面 Agent 侧零改动 |
| `PolicyHook` 在生产路径未装配 | `internal/agent/dial_executor.go:72` 与 `internal/cli/root.go:379` 均传入 `Policy` 为 nil 的 `Dialer` | Agent 用户态目标校验今天是空操作；策略执行点必须放在 Server 侧 |
| 目标校验的权威实现是 `routing.Policy` | `internal/routing/policy.go:102` `Validate(ip, port)`；`internal/proxyentry/target.go:25` 以包装而非重写的方式复用它 | VPN 目标校验同样包装 `routing.Policy`，deny list 不漂移 |
| 能力协商机制已就绪 | `internal/protocol/capabilities.go:12` 常量表、`internal/agent/session.go:726` `SetCapabilities`、`internal/agent/connection_pool.go:79` ack 校验 | 新增 `stream_icmp_echo.v1` 照现有范式，无需新机制 |
| 数据报边界语义已就绪 | `internal/protocol/udp.go` `UDPAssociation`：一个数据报 = 一个 `FrameData`，不加分帧字节 | ICMP 请求/应答按数据报传输，天然保持边界 |
| association 生命周期范式已就绪 | `internal/client/udp_assoc.go:57` `UDPAssociationManager`、`:155` `Expire` | ICMP 会话表照此范式实现，但不复用代码（语义不同，见第 6.2 节） |
| 节点身份与租约设施已就绪 | `internal/registry/database.go:48`/`:96`/`:223`、`internal/config/config.go:991` `EnsureNodeID`、`:39` `DefaultNodeIDPath` | peer 归属与 IP 池租约复用现有注册设施，不新建节点注册表 |
| 密文托管与 reveal 范式已就绪 | `internal/auth/secret_store.go`、`internal/server/credential_service.go`；AGENTS.md 已批准的 `POST /api/v1/tokens/{tokenId}/reveal` | peer 私钥复用同一套 AES-256-GCM + 确认头 + `no-store` + 审计范式 |
| 当前 Schema 版本 | `internal/storage/db.go:23` `SchemaVersion = 14`；最新增量 `migrations/incremental/v0013_to_v0014` | 本轮为 v15，新增 `migrations/incremental/v0014_to_v0015/{mysql,sqlite}.sql` |
| 优雅停机语义已就绪 | `internal/protocol/frame.go:37` `FrameGoAway`、`internal/server/session_manager.go:709` `GoAway` | VPN drain 沿用同一语义，不新造停机协议 |
| Server 侧子系统配置门禁范式已就绪 | `internal/config/config.go:152` `ProxyEntryConfig`、`:168` `WebSSHConfig`、`:204` `RelayConfig` | `server.vpn` 是第四个同形状 opt-in 子系统 |
| 管理 API 分派是首段路径 switch | `internal/server/api.go:331` | 新增 `case "vpn-peers"`、`case "vpn-nodes"` 即可挂载 |
| 后台导航是数组驱动 | `web/src/layouts/AppShell.vue` 的 `menuItems()` | 新增一项 `[path, i18nKey]` 即完成导航接入 |
| `golang.org/x/net` 已是直接依赖 | `go.mod` | Agent 侧 `golang.org/x/net/icmp` 不引入新模块 |
| 仓库当前零 TUN/WireGuard 痕迹 | `git grep -iE 'wireguard\|/dev/net/tun\|NET_ADMIN'` 无命中 | 全部为新增，无历史包袱 |

### 2.1 部署环境约束（用户已确认）

| 约束 | 影响 |
|---|---|
| 内网网关路由表无权修改 | 排除"路由注入 + 保留源地址"的真端到端 L3 方案；回程必须落在 Agent 宿主机已有可达性上 |
| Agent 无法维护 nftables | 排除 Agent 内核转发方案；策略执行点全部留在 Server 用户态 |
| Agent 宿主机可接受一次性 `net.ipv4.ping_group_range` sysctl | 非特权 ICMP datagram socket 可用，内网 ping 得以实现 |
| 不引入第四个二进制 | VPN 数据面进 `tunnelmesh-server` 进程，用 build tag 隔离重依赖 |
| Server 所在主机可配置公网 IP | WireGuard UDP 端口直接暴露，无需 nginx `stream{}` 每端口配置 |

### 2.2 关键技术风险（Task 0 必须实测）

wireguard-go 的 `device.Device` 需要一个 `tun.Device` 实现。本设计**不使用真实 `/dev/net/tun`**，而是提供一个内存态 `tun.Device`，其读写直接桥接到 gVisor netstack 的 `stack.LinkEndpoint`（Tailscale 的 `tstun` 即此范式）。若该桥接成立，则：

- Server 进程**不需要 `CAP_NET_ADMIN`、不需要 `/dev/net/tun`**，只需绑定一个非特权 UDP 端口。
- netstack 集成测试可在无特权 CI 中运行。

中止判据：若 wireguard-go 与 netstack 无法在同一进程内对接（`tun.Device` 的批量读写语义与 `LinkEndpoint` 不兼容），则回退到"真实 TUN + netstack 从 TUN 读包"方案，此时 Server 必须获得 `CAP_NET_ADMIN` 与 `/dev/net/tun`，第 11 节的特权威胁项与第 16 节的部署文档必须相应改写，并重新征求用户确认。回退决定必须在 Task 0 报告中明确记录，不得静默切换。

## 3. 架构总览

```text
原生 WireGuard 客户端（用户机器，无需安装 tunnelmesh-client）
  └─ 加密 UDP → Server 公网 IP:server.vpn.listen
       └─ tunnelmesh-server 进程内 VPN 网关（build tag: vpn）
            · wireguard-go device：握手、密钥、重放保护
            · 内存态 tun.Device ←→ gVisor netstack（无 /dev/net/tun，无 CAP_NET_ADMIN）
            · vpn_packet：按源 IP 反查 peer → 策略链 → 分派
                 ├─ TCP/UDP → netstack 终结 → relay.NodeTransport.OpenStream
                 │              （protocol="tcp"|"udp"，集群自动跨节点）
                 │                └─ Agent 现有 Dialer.DialTCP/DialUDP → 内网目标
                 └─ ICMP echo → relay.NodeTransport.OpenStream（protocol="icmp-echo"）
                                  └─ Agent 非特权 ping socket → 内网目标
```

分层遵循 Handler → Service → Repository：

| 层 | 归属 | 职责 |
|---|---|---|
| 纯逻辑 | `internal/vpn/` | 密钥、peer 模型、IP 池、目标策略、错误码、ini 渲染。无 I/O 所有权，可完整单测 |
| 数据面 | `internal/server/vpn_*.go` | WireGuard 端点、netstack 桥接、逐包策略、流生命周期、drain |
| 控制面 | `internal/server/vpn_peer_service.go` | 签发、吊销、轮换、reveal、IP 池分配编排 |
| 持久化 | `internal/storage/vpn_repository.go` | peer 与 IP 池租约的 Repository 实现 |
| Agent 扩展 | `internal/agent/icmp_echo.go` | 唯一 Agent 新增文件：ping socket 与请求关联 |
| 接口 | `docs/api/openapi.yaml` | 管理 API 契约 |
| 前端 | `web/src/views/VpnPeers.vue` 等 | 管理后台 |

## 4. 入口形态与能力边界

### 4.1 入口

- 传输：单个公网 UDP 端口（默认 51820），由 `server.vpn.listen` 指定。
- 端点标识：下发给用户的 `Endpoint` 使用**节点 DNS 名**（`server.vpn.endpoint_host`，如 `gw-1.mesh.example.com`）而非裸 IP，使故障转移更换 IP 时用户配置无需重发。
- 与既有 443 入口完全独立：VPN 不经 OpenResty/Nginx，由 Server 直接持有 UDP socket。云安全组与主机防火墙需单独放行该端口。

### 4.2 身份与地址

- peer 身份来自 WireGuard 公钥（协议层强认证），不依赖 IP。
- 每个 peer 分配一个 /32 VPN IP，来自节点 IP 池；该 /32 同时写入 WireGuard peer 的 `AllowedIPs`，由 WireGuard 自身过滤源地址。Server 再做一次源 IP ↔ peer 绑定校验作为纵深防御。

### 4.3 能力边界（必须在用户文档中显式声明）

支持：IPv4 的 TCP、UDP、ICMP echo。

不支持，且在你已给出的约束下无法支持：

- **源地址不保留**：内网侧看到的源地址是 Agent 宿主机地址，不是用户 VPN IP，因此无法在内网按 VPN 用户做 ACL 或日志归因。
- **仅 ICMP echo**：traceroute 的 TTL 超时、目的不可达、分片需要等其它 ICMP 类型不支持（非特权 ping socket 只暴露 echo）。
- **仅 TCP/UDP/ICMP-echo 三种 IP 协议**：GRE、SCTP、IPsec 嵌套、组播一律丢弃并计数。
- **不支持 IP 分片**：分片包丢弃并计数。TCP 因在 Server 侧终结、两条腿独立协商 MSS，不存在 PMTUD 黑洞；UDP 数据报超过路径 MTU 时丢弃。
- **Agent 不能主动向用户侧发起连接**。

若以上任一项不可接受，唯一出路是放开第 2.1 节的某条部署约束（Agent `CAP_NET_ADMIN`、第四个特权组件、或内网路由权限），需重新设计。

## 5. Server 侧设计

### 5.1 纯逻辑包 `internal/vpn/`

| 文件 | 内容 |
|---|---|
| `keys.go` | WireGuard 密钥对生成、base64 编解码、公钥格式校验 |
| `peer.go` | `Peer` 领域模型（ID、Name、PublicKey、AllowedIPs、NodeID、AgentID、Status、OwnerID、限额、ICMP 开关、到期时间）与字段校验 |
| `ippool.go` | 从 `server.vpn.ip_pool` 按 `node_subnet_size` 切出节点子网，再从中分配/释放 /32；冲突检测与耗尽判定。纯函数，持久化由 Repository 承担 |
| `policy.go` | 逐包目标校验，**包装** `routing.Policy`（手法同 `internal/proxyentry/target.go:25`），叠加 peer `AllowedIPs`、端口白名单、内网目标开关、IP 协议号白名单（仅 TCP=6、UDP=17、ICMP=1，其余丢弃并计 `protocol_unsupported`）、分片拒绝 |
| `config.go` | WireGuard ini 渲染（`[Interface]`/`[Peer]`），黄金文件测试对象 |
| `errors.go` | 稳定错误码，风格对标 `internal/proxyentry/errors.go:58` |

### 5.2 数据面 `internal/server/vpn_*.go`（全部带 `//go:build vpn`）

- `vpn_gateway.go`：`Gateway` 结构体，形状照抄 `internal/server/proxy_entry.go:68`（`config` / `peers` / `opener relay.NodeTransport` / `metrics` / `audits` / 活跃计数），构造函数对 nil 依赖降级而非 panic（同 `proxy_entry.go:85` 注释所述范式）。
- `vpn_device.go`：内存态 `tun.Device` 实现，桥接 wireguard-go 与 netstack `LinkEndpoint`。
- `vpn_stack.go`：netstack `stack.Stack` 装配、NIC 注册、路由表（默认路由指向 VPN 数据面处理器）、MTU 设定。
- `vpn_packet.go`：从 netstack 取包 → 源 IP 反查 peer → 策略链 → 分派 TCP/UDP 至流终结器或 ICMP 至 echo 通道。
- `vpn_flows.go`：per-peer 流表、并发与速率限额、空闲回收、`openEgress` 纪律（照 `internal/server/proxy_entry.go:568`：goroutine + cancel + 迟到流回收，避免泄漏）。
- `vpn_icmp.go`：ICMP echo 请求队列、Server 侧关联 ID 分配与应答匹配。
- `vpn_lifecycle.go`：启动、drain（沿用 `GoAway` 语义）、TUN/device 关闭顺序。

### 5.3 控制面与持久化

- `internal/server/vpn_peer_service.go`：签发（生成密钥对 → 分配 IP → 校验出口 Agent 能力 → 落库 → 热加载到 WireGuard 端点）、吊销（立即移除 peer 并断流）、轮换、reveal。
- `internal/server/vpn_peer_api.go`：HTTP Handler，只做协议解析、鉴权与响应。
- `internal/storage/vpn_repository.go`：`VPNPeerRepository` 与 `VPNIPLeaseRepository` 接口实现，登记进 `internal/storage/repository.go`。
- peer 热加载：WireGuard 端点维护 peer 集合的内存视图，由 Service 在写库成功后同步；节点重启时从 DB 全量装载。DB 是权威来源，内存视图是副本（`AGENTS.md` 单一数据源原则）。

### 5.4 依赖与构建隔离

- 新增直接依赖：`golang.zx2c4.com/wireguard`、`gvisor.dev/gvisor`。二者仅被 `//go:build vpn` 文件引用。
- 无 tag 构建的 `tunnelmesh-server` 不含这两个依赖；若配置 `server.vpn.enabled: true`，启动时**快速失败**并输出明确信息（"this binary was built without VPN support; rebuild with -tags vpn"），不得静默忽略（对齐 `AGENTS.md` auto-init 关闭时的快速失败原则）。
- `scripts/build-release.sh:71` 的 `binaries` 数组不变，产物矩阵新增一个 `tunnelmesh-server`（`-tags vpn`）变体；变体命名与归档布局属对外契约，改动需同步 `docs/deployment/binary-release.md`。

## 6. Agent 侧设计

### 6.1 协议扩展

- `relay.StreamRequest.Protocol` 新增值 `"icmp-echo"`。`internal/agent/dialer.go:105` 的 switch 新增 `case "icmp-echo"`。
- 新增能力常量 `protocol.CapabilityStreamICMPEcho = "stream_icmp_echo.v1"`（`internal/protocol/capabilities.go:12` 常量表），经 `internal/agent/session.go:726` `SetCapabilities` 通告、`internal/agent/connection_pool.go:79` ack 校验。
- 未协商成功该能力的 Agent：TCP/UDP 仍可作 VPN 出口（既有能力），ICMP 请求由 Server 直接丢弃并计 `icmp_unsupported`；签发 peer 时若 `icmp_enabled=true` 而出口 Agent 不具备该能力，返回 409 与稳定错误码。

### 6.2 `internal/agent/icmp_echo.go`

- 用 `golang.org/x/net/icmp` 的 `icmp.ListenPacket("udp4", ...)` 创建非特权 ICMP datagram socket；`ping_group_range` 未设置时返回明确错误并上报，不静默失败。
- 关联**不使用内核分配的 ICMP id**（内核会改写它），改用 Server 在 stream 消息中携带的关联 ID；应答按该 ID 回写。
- 并发上限、超时、空闲回收照 `internal/client/udp_assoc.go` 的生命周期范式实现，但**不复用其代码**：client 侧 association 是"一个客户端源地址一条流"，这里是"一个在途 echo 请求一条关联"，语义不同，强行复用会违反接口隔离原则。
- 数据报边界由 `internal/protocol/udp.go` 的 `UDPAssociation` 保证。
- Agent 侧总量上限独立于 Server 的 per-peer 上限，作为最后一道防线。

## 7. 数据模型（Schema v15）

### 7.1 新增表

- `vpn_peers`：`id`、`name`、`owner_id`、`public_key`（唯一）、`private_key_ciphertext`、`private_key_nonce`、`private_key_key_id`、`private_key_version`、`vpn_ip`、`node_id`、`agent_id`、`allowed_ips`、`allowed_ports`、`allow_private_targets`、`icmp_enabled`、`max_concurrent_flows`、`packet_rate_limit`、`expires_at`、`status`、`description`、`created_at`、`updated_at`。
  - 唯一约束：`public_key`；`UNIQUE(node_id, vpn_ip)`。
  - 索引：`(owner_id, id)` 支撑分页内权限过滤；`(node_id, status)` 支撑节点装载。
- `vpn_ip_leases`：`id`、`node_id`、`subnet`、`allocated_count`、`lease_holder`、`lease_expires_at`、`epoch`。
  - 唯一约束：`UNIQUE(node_id, subnet)`；`epoch` 参与 fencing，防止过期持有者写回。
- `vpn_flows`：**不建表**。运行时流状态不入库、不进 Prometheus 高基数标签（`AGENTS.md` 可观测性约束），只以内存计数与低基数指标呈现。

### 7.2 迁移

- `internal/storage/db.go:23` `SchemaVersion` 提升至 15。
- 同步更新 `migrations/ddl.sql`（全量权威）与新增 `migrations/incremental/v0014_to_v0015/{mysql,sqlite}.sql`。
- 新增列全部为新表，不改动既有表，满足 `AGENTS.md` MINOR 的向后兼容要求：滚动升级期间新旧 Server 可同时访问数据库。
- 迁移必须可安全重试：执行前校验源版本为 14，成功后再推进 `schema_meta.version`。
- 双方言兼容性必须测试；MySQL DDL 隐式提交，脚本不得依赖整体事务回滚，需提供失败恢复步骤。

## 8. 配置项

新增 `server.vpn`（形状对标 `internal/config/config.go:152` `ProxyEntryConfig`）：

```yaml
server:
  vpn:
    enabled: false                       # 默认关闭；关闭时进程不创建任何 VPN 资源
    listen: "0.0.0.0:51820"              # 公网 UDP
    endpoint_host: "gw-1.mesh.example.com"  # 下发给用户的 Endpoint DNS 名
    ip_pool: "10.64.0.0/16"
    node_subnet_size: 24                 # 每节点切一个 /24
    mtu: 1420                            # WireGuard 开销 72 字节
    max_peers: 0                         # 0 = 不限
    max_flows_per_peer: 128
    max_flows_total: 0                   # 0 = 不限
    packet_rate_per_peer: 0              # 每秒包数，0 = 不限
    connect_timeout: 10s                 # 开流超时
    idle_timeout: 120s                   # 流空闲回收
    shutdown_timeout: 15s                # drain 上限
    icmp_enabled: true
    icmp_timeout: 5s
    icmp_max_concurrent: 64
```

- 节点 WireGuard 私钥**只从环境变量注入**：`TUNNELMESH_VPN_NODE_PRIVATE_KEY`（base64）。禁止写入配置文件或代码库，禁止出现在日志、审计与指标中。缺失且 `enabled: true` 时启动快速失败。
- peer 私钥密文用现有 `TUNNELMESH_TOKEN_ENCRYPTION_KEY`（AES-256-GCM）加密，复用 `internal/auth/secret_store.go`；密钥不可用时 reveal 返回 503 与 `credential_secret_unavailable`，不降级为明文。
- 配置优先级、`auto-init`、密钥注入与 Schema 变更必须同时验证 SQLite 与 MySQL。
- `docs/operations/configuration.md` 与 `docs/operations/config-examples.md` 必须同步补全全部键、取值范围与默认值。

## 9. 管理 API

前缀 `/api/v1`，响应统一 `{ code, msg, data }`，列表用 cursor 分页，写操作支持 `Idempotency-Key`。挂载点为 `internal/server/api.go:331` 的 switch 新增两个 case。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/vpn-peers` | 列表。**权限过滤必须在分页语义内完成**：普通用户只见自己的 peer，管理员见全部 |
| POST | `/api/v1/vpn-peers` | 创建并签发；返回不含私钥的摘要与一次性 reveal 提示 |
| GET | `/api/v1/vpn-peers/{peerId}` | 详情，不含私钥 |
| PATCH | `/api/v1/vpn-peers/{peerId}` | 改名、AllowedIPs、端口、内网开关、ICMP 开关、限额、状态、有效期、备注 |
| DELETE | `/api/v1/vpn-peers/{peerId}` | 吊销：立即从 WireGuard 端点移除并断开在途流 |
| POST | `/api/v1/vpn-peers/{peerId}/rotate` | 轮换密钥对，旧公钥立即失效，返回新配置摘要 |
| POST | `/api/v1/vpn-peers/{peerId}/config:reveal` | 取完整 ini（含私钥）。照已批准的 `tokens/{tokenId}/reveal` 范式：需 `X-VPN-Config-Reveal-Confirm`、`Idempotency-Key`、`acknowledgeRisk=true`；响应 `Cache-Control: no-store`；写审计 |
| GET | `/api/v1/vpn-peers/{peerId}/flows` | 当前活跃流摘要（协议、目标、起止时间、字节数），不含载荷 |
| GET | `/api/v1/vpn-nodes` | 各节点 VPN 状态：是否启用、监听地址、endpoint、IP 池与已分配数、peer 数、是否具备 ICMP 能力 |

约束：

- 所有权限校验在服务端完成，不信任客户端传入的 owner、agent 或 role。
- `agentId` 与 `nodeId` 必须由服务端校验存在性与能力，不接受客户端断言。
- 每个端点必须覆盖未授权、越权、分页、幂等与错误响应测试。
- `docs/api/openapi.yaml` 必须同步更新。

## 10. 管理后台

### 10.1 导航与路由

- `web/src/router.ts` 新增 `{path:'/vpn', component:VpnPeers, meta:{auth:true}}`。
- `web/src/layouts/AppShell.vue` 的 `menuItems()` 数组在 `/credentials` 之后插入 `['/vpn', 'navigation.vpn']`。
- 页面本身不按 `admin` 门禁（与 `/routes`、`/credentials` 一致），可见范围由服务端分页内权限过滤决定。

### 10.2 新增文件

| 文件 | 职责 |
|---|---|
| `web/src/views/VpnPeers.vue` | 列表 + 创建/编辑抽屉 + 使用说明抽屉 |
| `web/src/api/vpn.ts` | API 客户端，范式照 `web/src/api/credentials.ts` |
| `web/src/tests/vpn.spec.ts` | 组件与交互测试，范式照 `web/src/tests/routes.spec.ts` |

复用既有组件：`PageHeader.vue`、`StatusTag.vue`、`DataState.vue`；私钥一次性展示复用 `TokenSecretDialog.vue` 的交互范式。

### 10.3 列表

列：名称、状态、VPN IP、出口 Agent、AllowedIPs 条数、ICMP、到期时间、操作。活跃流数**不进列表**（照 `tp-*` 的做法避免横向滚动条），指引用户看 Grafana。列表页顶部展示 IP 池使用概览（总量/已分配/本节点子网剩余）。

### 10.4 创建/编辑表单

名称、出口 Agent（复用 `Routes.vue` 的 agent 搜索下拉）、授权目标网段（CIDR 多值）、授权端口（可选多值）、允许访问内网目标开关、允许 ICMP 开关、并发流上限、包速率上限、有效期（可选）、备注（256 字内）。

前端校验必须与服务端一致：CIDR 格式、端口 1-65535 整数、名称字符集与长度。校验失败给出字段级错误，不提交。

### 10.5 使用说明抽屉

范式照 `Routes.vue` 的 `el-drawer` + `usage-code`：

- 完整 WireGuard ini。私钥默认遮蔽，点"显示"调用 `config:reveal`，一次性展示 + 复制按钮 + 明确的"离开页面后需重新 reveal 并再次确认风险"提示。
- 各平台导入指引：Windows / macOS / iOS / Android / Linux（`wg-quick`）。
- MTU 说明与为什么不需要改客户端路由。
- **能力边界声明**（第 4.3 节全部条目），避免用户把不支持的协议当成故障。
- 排障指引：数据面无错误码回传，失败只表现为连不通；给出应查看的指标名与审计事件名。
- 提供 `.conf` 文件下载与一键复制。**本轮不做二维码**：二维码需要引入新的前端依赖，而 ini 文本复制已能满足全部平台的导入流程，按 YAGNI 排除。

### 10.6 节点 VPN 状态

`web/src/views/Servers.vue` 增加 VPN 区块：该节点是否启用、监听地址、endpoint DNS 名、IP 池子网与已分配数、peer 数、出口 Agent 是否具备 ICMP 能力。数据来自 `GET /api/v1/vpn-nodes`。

### 10.7 国际化

`web/src/i18n/messages/zh-CN.ts` 与 `en-US.ts` 必须同步新增 `navigation.vpn` 与完整 `vpn.*` 文案，键集合两侧一致（现有测试已守护 i18n 键对齐）。

### 10.8 前端门禁

`cd web && npm test -- --run && npm run build`，并执行 `scripts/verify-web-embed.sh` 确认 Go embed 产物已更新。

## 11. 安全模型（威胁与对策）

| 威胁 | 对策 |
|---|---|
| 用户伪造源 IP 冒充他人 peer | WireGuard 按 peer `AllowedIPs`(/32) 过滤；Server 再做源 IP ↔ peer 绑定校验 |
| 访问云 metadata / 组播 / 非路由地址 | `routing.Policy` 恒拒，与 `tp-*`、Agent policy 共用同一 deny list |
| 内网横向扫描 | peer `AllowedIPs` 限定目标网段 + 端口白名单 + 包速率限制 + 并发流上限 + 审计 |
| peer 私钥泄露 | 密文入库、reveal 需确认头 + 幂等键 + `no-store` + 审计、支持立即 rotate 与吊销 |
| Server 被攻破 | 攻击者获得节点私钥与全部 peer 私钥密文；`TUNNELMESH_TOKEN_ENCRYPTION_KEY` 只从环境注入，reveal 全程审计可事后取证 |
| WireGuard 握手洪泛 | 只接受已注册公钥的握手；握手速率限制；未注册公钥丢弃并计数 |
| Agent ping socket 被当作 ICMP 洪泛源 | Server per-peer ICMP 并发与速率上限 + Agent 侧独立总量上限 |
| netstack 内存膨胀 / goroutine 泄漏拖垮管理 API | 数据面独立 goroutine 池 + 有界队列 + recover 边界；per-peer 与全局流上限；内存水位告警；逼近上限时拒绝新流而非崩溃 |
| VPN 面故障波及既有能力 | VPN 与 HTTP/WS/`tp-*` 完全独立；`enabled: false` 时不创建任何 VPN 资源；止损为关开关重启 |
| 重依赖污染所有部署 | `//go:build vpn` 隔离；无 tag 构建不含 netstack 与 wireguard-go |
| 特权（仅在 2.2 节回退方案下成立） | 首选方案 Server 不需要 `CAP_NET_ADMIN`；若回退到真实 TUN，则用 systemd `AmbientCapabilities=CAP_NET_ADMIN` 或 Docker `--cap-add=NET_ADMIN`（禁止 `--privileged`），并在 ADR 中显式记录进程级提权的接受理由 |
| 日志与指标泄露敏感信息 | 日志、审计、指标一律不含私钥、包载荷、握手字节；peer ID 不作为 Prometheus 标签（高基数禁令） |

Agent 侧安全模型**不变**：不新增特权、不装防火墙规则、不改内核参数（除一次性 `ping_group_range`，由运维设置而非 Agent 维护）。

## 12. 错误语义

### 12.1 管理 API

沿用 `{ code, msg, data }` 与 HTTP 状态码。新增稳定错误码（风格对标 `internal/proxyentry/errors.go:58`）：

| 状态码 | code | 触发条件 |
|---|---|---|
| 400 | `vpn_peer_invalid` | 字段校验失败（名称、CIDR、端口、限额） |
| 400 | `vpn_ip_pool_invalid` | `ip_pool` 或 `node_subnet_size` 配置非法 |
| 404 | `vpn_peer_not_found` | peer 不存在或对当前主体不可见 |
| 409 | `vpn_peer_conflict` | 名称或公钥冲突 |
| 409 | `vpn_agent_capability_missing` | 出口 Agent 未协商 `stream_icmp_echo.v1` 而 peer 要求 ICMP |
| 409 | `vpn_ip_pool_exhausted` | 节点子网 /32 已分配完 |
| 409 | `vpn_node_disabled` | 目标节点未启用 VPN |
| 503 | `credential_secret_unavailable` | 密文存储不可用（复用现有码） |
| 503 | `vpn_capacity_exhausted` | 达到 `max_peers` |

### 12.2 数据面

IP 层没有响应通道，**无法把错误码回传给用户**。失败只表现为连不通或 ping 不通，与 `docs/user-guide/http-proxy-entry.md:170` 已记录的 `tp-*` 行为一致。用户文档必须显式说明这一点。

数据面结果通过指标 `error_class` 标签暴露（低基数）：`peer_unknown`、`peer_revoked`、`peer_expired`、`target_denied`、`metadata_denied`、`port_denied`、`protocol_unsupported`、`fragment_dropped`、`oversize_dropped`、`capacity_exhausted`、`rate_limited`、`egress_unavailable`、`egress_timeout`、`icmp_unsupported`、`icmp_timeout`、`stack_error`。

拒绝事件写审计 `vpn_packet_denied`，含 reason、peer ID、目标地址与协议，**不含**任何密钥与包内容。为避免审计洪泛，同一 peer + reason + 目标网段在窗口内聚合计数后再落库。

## 13. 可观测性

优先复用现有向量（`internal/observability/metrics.go:82`-`:85`），用 `component="vpn"`、`protocol="tcp"|"udp"|"icmp-echo"` 区分：`tunnelmesh_bytes_total`、`tunnelmesh_streams_active`、`tunnelmesh_streams_total`、`tunnelmesh_stream_errors_total`。

新增 VPN 专有指标：

| 指标 | 类型 | 标签 |
|---|---|---|
| `tunnelmesh_vpn_peers_active` | Gauge | `node_id` |
| `tunnelmesh_vpn_handshake_total` | Counter | `result`, `error_class` |
| `tunnelmesh_vpn_packets_dropped_total` | Counter | `direction`, `error_class` |
| `tunnelmesh_vpn_flows_duration_seconds` | Histogram | `protocol`, `result` |
| `tunnelmesh_vpn_ip_pool_allocated` | Gauge | `node_id` |
| `tunnelmesh_vpn_icmp_inflight` | Gauge | `node_id` |

标签一律低基数；peer ID、VPN IP、目标地址**不得**作为标签。

结构化事件与 traceparent 传播沿用 `internal/observability/`；VPN 流的 `Metadata` 字段携带 traceparent（同 `internal/server/proxy_entry.go` 的 `traceparentFromRequest` 做法），使 traceroute 能贯穿 VPN 路径。

Grafana：在统一 Dashboard 新增 Row `VPN Gateway`，照 `HTTP Proxy Entry` Row 范式，含 peer 数、活跃流、握手成败、丢包分类、IP 池水位、ICMP 在途数；受 `deploy/grafana/dashboard_schema_test.go` 守护。告警规则加入 `deploy/prometheus/`：握手失败率、丢包率、IP 池耗尽、内存水位、drain 超时。

SLO：VPN 数据面可用率与 P99 建流时延单独定义，写入 `docs/operations/slo.md`。

## 14. 测试策略（TDD）

按 `docs/development/testing.md` 分层矩阵，每项先写失败测试再实现。

| 层级 | 覆盖 |
|---|---|
| 纯逻辑单测 `internal/vpn/` | IP 池切分/分配/释放/冲突/耗尽；`AllowedIPs` 解析与匹配；目标策略（metadata 恒拒、组播、非路由、私网开关、端口白名单、协议白名单、分片拒绝）；ini 渲染黄金文件；错误码集合稳定性 |
| netstack 集成测（tag `vpn`） | 用注入式 stack 而非真实 `/dev/net/tun`，**CI 无需特权**；覆盖 TCP 建连、半关闭、EOF、重复 ID、窗口耗尽、超时；UDP 数据报边界与乱序；ICMP echo 关联与超时 |
| Agent ICMP 单测 | ping socket 以接口注入；覆盖内核改写 ICMP id 后关联仍正确、超时、并发上限、`ping_group_range` 缺失时明确报错 |
| 能力协商测 | 新旧 Agent 混合；旧 Agent 收到 `icmp-echo` 走 `dialer.go:105` default 安全拒绝且不崩溃；Server 据此拒签要求 ICMP 的 peer |
| Repository 契约测 | `vpn_peers` 与 `vpn_ip_leases` 双方言（SQLite + MySQL）；唯一约束、租约过期、epoch fencing、并发分配竞争 |
| 迁移测 | `v0014_to_v0015` 双方言；空库全量 DDL 与旧库增量升级的最终 Schema 必须一致；`auto-init` 关闭时版本不匹配必须快速失败并给出版本信息 |
| API 测 | 未授权、越权（普通用户读不到他人 peer）、cursor 分页内权限过滤、幂等键重放一致、吊销后立即断流、吊销后重签、rotate 使旧公钥失效、reveal 的 `no-store` 与审计落库、出口 Agent 能力缺失 409 |
| 配置测 | 全部键的默认值、优先级（命令行 > 环境变量 > 配置文件 > 默认值）、`enabled: true` 但缺 `TUNNELMESH_VPN_NODE_PRIVATE_KEY` 时快速失败、无 tag 构建下 `enabled: true` 快速失败、`ip_pool`/`node_subnet_size` 非法时报错 |
| 前端测 | `web/src/tests/vpn.spec.ts`：列表渲染、表单校验、reveal 交互与遮蔽、i18n 键中英对齐 |
| E2E `test/e2e/vpn/` | 真实 `wg` 客户端 + Server + Agent + 内网目标：ping 通、TCP 通、UDP 通、吊销即断、非支持协议被丢弃并计数、超限触发拒绝。需要 `ping_group_range` 与公网/回环 UDP 可达，因此照 `test/e2e/proxy-entry/` 范式做成**环境不满足则 SKIP 且退出码 0**；PR 的 Test Evidence 必须写"未执行 + 原因"，不得写成已通过 |

门禁命令：

```bash
go test ./... -count=1
go test -race ./... -timeout 30m
go vet ./...
go test -tags vpn ./... -count=1
go test -tags vpn -race ./... -timeout 30m
go vet -tags vpn ./...
git diff --check
cd web && npm test -- --run && npm run build && cd .. && ./scripts/verify-web-embed.sh
```

## 15. 实施阶段与中止判据

阶段划分（每份计划可独立测试与评审，按 `AGENTS.md` 分别写入 `docs/superpowers/plans/`）：

1. **约束反转与 ADR**：ADR 0002、`AGENTS.md:11` 与 `:148` 修订、12 处活文档改写、`docs/README.md` 登记。必须先行，因为它解除后续所有工作的前提。
2. **Task 0 技术验证**：第 2.2 节的 wireguard-go ↔ netstack 桥接可行性。**中止判据见 2.2**；结论必须写进报告，回退需重新征求用户确认。
3. **Schema v15**：全量 DDL + 双方言增量 + Repository + 契约测 + 迁移测。
4. **纯逻辑与管理 API**：`internal/vpn/`、`vpn_peer_service.go`、`vpn_peer_api.go`、OpenAPI、全套 API 测。
5. **管理后台**：`VpnPeers.vue`、`api/vpn.ts`、导航与路由、i18n、`Servers.vue` VPN 区块、前端测与构建。
6. **Server 数据面**：`vpn_device.go`、`vpn_stack.go`、`vpn_packet.go`、`vpn_flows.go`、`vpn_icmp.go`、`vpn_lifecycle.go`、策略链、限额、drain、指标。
7. **Agent ICMP 扩展**：`icmp_echo.go`、能力协商、协议扩展与兼容测。
8. **可观测性与部署**：Grafana Row、告警规则、SLO、build tag 变体与发布脚本、部署文档（含 `ping_group_range` 与安全组放行）、E2E。

依赖关系：1 → 2 → 3 → 4 → 5；6 与 7 依赖 2 与 3；8 依赖 6 与 7。阶段 5 可与 6、7 并行。

## 16. 文档交付

新增：

- `docs/architecture/adr/0002-public-ingress-and-embedded-vpn.md`：记录解除 `AGENTS.md:11`、部分修订 `:148`（解除 TUN/L2 VPN，改写 ICMP 措辞为"不主动构造 ICMP，但 ICMP echo 经非特权 ping socket 支持"，保留 P2P NAT traversal 与任意远程命令执行）、Agent 特权边界结论、策略执行点留在 Server 的理由、以及 2.2 节回退时的特权接受理由。
- `docs/user-guide/vpn.md`：用户视角——导入配置、各平台步骤、能力边界、排障。
- `docs/deployment/vpn-gateway.md`：部署视角——公网 UDP 放行、`endpoint_host` DNS、`TUNNELMESH_VPN_NODE_PRIVATE_KEY` 注入、IP 池规划、build tag 变体、回退方案下的 `CAP_NET_ADMIN`。
- `docs/operations/vpn.md`：运维视角——IP 池容量、发布断流告知、drain 与止损、指标与告警、SLO、常见故障。
- `docs/operations/agent-host-requirements.md` 或在现有 Agent 文档中补 `net.ipv4.ping_group_range` 的一次性设置说明与校验命令。

更新：

- `AGENTS.md:11`、`AGENTS.md:148`
- `README.md:15`、`README.zh-CN.md:15`、`docs/README.md:100`
- `docs/architecture/overview.md:24`（含新增 ASCII 链路图）
- `docs/deployment/nginx.md:194`、`docs/deployment/openresty-proxy-entry.md:11`
- `docs/operations/network-probes.md:74`、`docs/operations/configuration.md`、`docs/operations/config-examples.md`
- `docs/protocol/proxy-modules.md:3`
- `docs/user-guide/client.md`（44/48/74/117/446 行）、`docs/user-guide/http-proxy-entry.md:170`、`docs/user-guide/managed-http-route.md:71`、`docs/user-guide/server-admin.md:105`
- `docs/api/openapi.yaml`、`deploy/README.md`、`deploy/grafana/README.md`

历史 specs/plans/PR 中"不支持公网 UDP""公网只开放 HTTP/HTTPS/WebSocket"等表述按 `docs/development/documentation.md:64` 的时点记录不可改写原则**保持原样**，反转只由 ADR 0002 承载。

新增或改名记录后必须执行 `python3 scripts/gen_doc_index.py` 重新生成索引，并确认 `docs/README.md` 无孤儿文档。

## 17. 已决策记录

| 决策 | 选择 | 理由 |
|---|---|---|
| VPN 协议 | 只做 WireGuard | UDP 单端口、协议面小、Go 生态成熟、peer 配置是单个 ini；OpenVPN 需 TLS + 双协议 + 大得多的代码面，目标未覆盖更多 |
| 数据面归属 | `tunnelmesh-server` 进程内，不引入第四个二进制 | 用户明确要求；用 build tag 隔离重依赖以保住分发边界 |
| TUN 实现 | 内存态 `tun.Device` 桥接 netstack，不用 `/dev/net/tun` | 消除 Server 的 `CAP_NET_ADMIN` 需求，使 CI 无特权可测；Task 0 验证，失败则回退并重新确认 |
| TCP/UDP 终结位置 | Server 侧 netstack | Agent 侧零改动、零特权、零新依赖；MSS 两腿独立协商，无 PMTUD 黑洞 |
| ICMP 实现 | Agent 非特权 ping socket + Server 侧关联 ID | 满足"内网可 ping"且 Agent 不需要 `CAP_NET_ADMIN`；不依赖被内核改写的 ICMP id |
| 策略执行点 | Server 用户态，包装 `routing.Policy` | Agent 无 nftables 能力；且 `Dialer.Policy` 生产路径未装配，Agent 侧本无有效执行点 |
| 源地址处理 | Agent 侧改写为宿主机地址 | 内网网关路由不可改，回程只能落在 Agent 已有可达性上 |
| peer 私钥 | Server 生成并 AES-256-GCM 加密托管 | 运维体验好，与既有 `proxy_basic` 凭据范式一致；reveal 走确认头 + `no-store` + 审计 |
| 重依赖隔离 | `//go:build vpn` + 发布变体 | 不用 VPN 的部署二进制不含 netstack 与 wireguard-go |
| `AGENTS.md:148` | 只解除 TUN/L2 VPN，改写 ICMP，保留 P2P 与 RCE | 四条禁令互相独立；P2P 与 RCE 与本目标正交且纯风险 |
| IPv6 | 本轮不做 | 内网目标以 IPv4 为主，ICMPv6/NDP 复杂度显著，YAGNI |

## 18. 验收标准

1. 管理员可在后台创建 peer，下载 WireGuard ini，用原生客户端导入后获得 VPN IP。
2. 从 VPN 接口可访问被 `AllowedIPs` 授权的内网 TCP 与 UDP 服务。
3. 从 VPN 接口可 `ping` 通被授权的内网主机；ICMP echo 之外的类型被丢弃并计数。
4. 未授权目标（含云 metadata、组播、非路由地址、`AllowedIPs` 之外）一律不可达，且产生 `vpn_packet_denied` 审计与对应 `error_class` 指标。
5. 吊销 peer 后其在途流立即断开，新握手被拒绝。
6. rotate 后旧公钥立即失效。
7. reveal 响应带 `Cache-Control: no-store`，产生审计记录，且列表、日志与指标中任何位置都不出现私钥。
8. Agent 侧无新增特权、无防火墙规则、无内核参数改动（`ping_group_range` 由运维一次性设置）；未设置时 ICMP 明确报错而非静默失败。
9. 旧版 Agent（未协商 `stream_icmp_echo.v1`）收到 `icmp-echo` 安全拒绝且不崩溃；Server 拒签要求 ICMP 的 peer 并返回 409。
10. `server.vpn.enabled: false` 时进程不创建任何 VPN 资源，无 tag 构建下设为 `true` 时启动快速失败。
11. Schema v15 在 SQLite 与 MySQL 下均通过空库全量初始化与 v14 增量升级，最终 Schema 一致。
12. 集群模式下 peer 归属与 IP 池租约经 `internal/registry` 正确 fencing，过期持有者无法写回。
13. 关闭 VPN 面（改开关重启）不影响既有 HTTP/WS/`tp-*` 能力，构成 5 分钟止损路径。
14. 第 14 节全部门禁命令通过，带与不带 `-tags vpn` 各跑一遍。
15. 第 16 节全部文档交付完成，`python3 scripts/gen_doc_index.py` 无 diff 漂移，`docs/README.md` 无孤儿文档，历史时点记录未被改写。
