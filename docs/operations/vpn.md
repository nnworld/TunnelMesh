# VPN 网关运维

内嵌 VPN 网关的容量、租约、拒绝分类、指标现状与故障处理。

部署与放行见 [VPN 网关部署](../deployment/vpn-gateway.md)；
配置键的取值范围见 [配置说明](configuration.md#内嵌-vpn-网关wireguard)；
用户侧现象与自助排查见 [VPN 使用者帮助](../user-guide/vpn.md)。

## 运行时形态

网关是 Server 进程内的一组 goroutine 加一个**进程内内存 TUN 设备**，没有内核网络接口、
没有 `/dev/net/tun`、不需要 `CAP_NET_ADMIN` 或 `CAP_NET_RAW`。这决定了运维上的几件事：

- `ip link` / `wg show` 在网关宿主机上**看不到任何接口**。要确认网关活着只能看日志、指标和
  `/api/v1/vpn-nodes`。
- 宿主机重启或进程重启后数据面状态全部丢失，但这是无损的：流表、监听器注册表、peer 内存视图、
  拒绝聚合器全在内存，重建靠数据库里的 `vpn_peers` 与 `vpn_ip_leases`。
- 不存在“接口卡死需要 down/up”这类操作。止损手段是重启进程或关开关。

启动日志（顺序即装配顺序）：

```
vpn_gateway_started  node_id=server-node-1 listen=0.0.0.0:51820 port=51820 mtu=1420 peers=N
vpn_gateway_enabled  node_id=server-node-1 listen=0.0.0.0:51820 endpoint_host=gw-1.mesh.example.com
vpn_peers_loaded     node_id=server-node-1 peers=N refused=M
```

`refused` 非 0 时要看紧邻的 `vpn_peer_load_refused` 行，它给出行不能服务的原因
（典型是 `node_id` 不属于本节点）。加载 peer 集有 30s 上限：一个等不到数据库的网关
会占着 UDP 端口却不应答任何握手，那是从外部最难诊断的故障形态，所以宁可启动失败。

## IP 池容量与耗尽

每个节点从 `server.vpn.ip_pool` 里租约一个 `node_subnet_size` 前缀的子网，
peer 的 `/32` 从本节点子网里分配。子网第一个可用地址留给节点自己的 VPN 接口，所以：

| `node_subnet_size` | 可分配 peer 数 |
| --- | --- |
| `/24` | 253 |
| `/23` | 509 |
| `/22` | 1021 |
| `/30` | 1 |

**耗尽的两级**，别混：

1. **节点子网耗尽** → 签发返回 409 `vpn_ip_pool_exhausted`。这是本节点的地址用完了，
   其它节点可能还有余量。扩池只能靠改 `node_subnet_size`（需要全集群一致 + 重启）或扩 `ip_pool`。
2. **`max_peers` 触顶** → 签发返回 503 `vpn_capacity_exhausted`。这是策略上限，不是地址用完，
   调大 `server.vpn.max_peers` 即可。

水位从 `/api/v1/vpn-nodes` 的 `allocated` / `capacity` 读。这两个计数器读自子网租约与 peer 表，
不是从前缀推算的——推算出来的容量是签发方未必能兑现的承诺（第一个地址被保留、租约可能丢失）。
后台在这两个值缺失时渲染成 em dash，而不是显示“已分配 0”。

**建议告警阈值**：`allocated / capacity > 0.8` 预警，`> 0.95` 严重。地址池扩不动，
所以这个告警要有能落地的处置动作（清理过期 peer、吊销长期不握手的 peer）。

吊销不回收地址：公钥与地址都不复用，这是刻意的，避免一个新 peer 继承旧 peer 在内网侧留下的
ACL 或日志归因。所以**长期运行的节点子网会被历史 peer 逐渐占满**，规划时按“累计签发数”
而不是“并发在线数”估容量。

## 子网租约与 stale epoch

租约 TTL 1 分钟（`vpnSubnetLeaseTTL`），续租间隔 TTL/3 = 20 秒，单次读写超时 10 秒。
续租是 compare-and-set，携带行里的 `lease_holder` 与 `epoch` 原样回写——续租循环自己发明 epoch
会在第一轮就把本节点从自己的子网里 fence 出去。

续租结果打点 `tunnelmesh_registry_lease_total{operation="vpn_subnet_renew",result,error_class}`：

| `result` / `error_class` | 含义 | 处置 |
| --- | --- | --- |
| `success` | 正常 | — |
| `failed` / `list_failed` | 连本节点持有的租约都列不出来（数据库不可达） | 看 `vpn_subnet_list_failed`；**已加载的 peer 继续服务**，这是降级不是停机 |
| `failed` / `stale_epoch` | 别的节点现在持有这个子网 | 看 `vpn_subnet_lease_lost`；见下 |
| `failed` / `error` | 其它续租失败 | 看 `vpn_subnet_renew_failed` |

**stale epoch 的处理是刻意的**：续租循环放弃这个子网（再续也只会同样失败），
但**已经在服务的 peer 与流一律不动**。一次数据库分歧不是把正在工作的流量打成黑洞的理由；
新签发由 repository 层 fence，不由这个循环 fence。

后果是：一个失去租约的节点会继续服务存量 peer，直到进程重启。要判断节点是否还持有子网，
看 `vpn_subnet_lease_lost` 是否出现过，以及 `/api/v1/vpn-nodes` 的 `subnet` 字段。
两个节点同时认为自己持有同一子网时，双方都能服务存量，但只有一方能新签发。

## drain 与 5 分钟止损

`shutdown_timeout`（默认 15s）是退出时等待在途流排空的上限，可为 0（不等待）。
等待期间每 20ms 看一次流表——用轮询而不是信号，因为流可以因四种原因结束
（peer 关闭、agent 关闭、idle reaper、吊销），一个要被逐一通知的计数器就是第四个能把 teardown 写错的地方。

超时则强制关闭在途流，并在退出日志里报告：

```
vpn_gateway_stopped  node_id=... port=51820 waited=15s in_flight_at_timeout=3 flows_forced=3 \
  steps=drain>listeners>await_flows>flows>endpoint>peers>stack>background>denials
```

`steps` 是实际执行顺序，卡住时看它停在哪一步。`waited=0s` 且 `in_flight_at_timeout=0` 是干净退出。

**止损两条路，都不需要回滚代码**：

1. `server.vpn.enabled: false` + 重启 → 进程不创建任何 VPN 资源，行为与旧版本完全一致；
   管理 API 写操作返回 409 `vpn_node_disabled`，读操作返回空列表。
2. 换回不带 `-tags vpn` 的二进制 → 数据面根本不在进程里，最强止损。

数据面无持久化状态，回滚不需要数据补偿、迁移或清理。

## 拒绝原因对照

IP 层没有响应通道，网关**无法把错误码回传给用户**。所有拒绝在用户侧都表现为“连不通”或
“ping 不通”，没有 RST、没有 ICMP 错误、没有提示页。这是刻意的：回一个
`Destination Unreachable` 等于告诉对端“这个地址存在，只是不让你访问”，而那正是出口策略要隐藏的事实。

排障因此**只能在服务端做**。`error_class` 全集（16 个，已发布即契约，不新增）：

| `error_class` | 触发条件 | 用户侧现象 |
| --- | --- | --- |
| `peer_unknown` | 公钥不在本节点的 peer 内存视图里 | 握手不成功 |
| `peer_revoked` | peer 已吊销 | 握手不成功，在途流被切断 |
| `peer_expired` | peer 已过期 | 握手不成功 |
| `target_denied` | 目标不在 peer 的 `allowed_ips` 内 | 连不通 |
| `metadata_denied` | 目标是云 metadata 地址（169.254.169.254） | 连不通 |
| `port_denied` | 目标端口不在 `allowed_ports` 内 | 连不通 |
| `protocol_unsupported` | IP 协议不是 TCP/UDP/ICMP-echo（GRE、SCTP、组播等） | 连不通，且**静默** |
| `fragment_dropped` | 非首片分片包 | 大包/分片场景连不通 |
| `oversize_dropped` | 超过隧道 MTU | UDP 大包丢失 |
| `capacity_exhausted` | 达到 `max_flows_per_peer` / `max_flows_total` / `icmp_max_concurrent`，或出向队列满 | 新连接失败，存量不受影响 |
| `rate_limited` | 超过 `packet_rate_per_peer` | 间歇性丢包 |
| `egress_unavailable` | 出口 Agent 不可达或连接失败 | 连不通 |
| `egress_timeout` | `connect_timeout` 内没建成到目标的连接 | 连接超时 |
| `icmp_unsupported` | 出口 Agent 未协商 `stream_icmp_echo.v1` | `ping` 不通但 TCP 通 |
| `icmp_timeout` | `icmp_timeout` 内没有 echo 应答 | `ping` 超时 |
| `stack_error` | 网关内部不变量破裂（监听器注册冲突、agent 串了 correlation 等） | 不定，**必须查日志** |

`stack_error` 与其它类不同：它不是策略决定的结果，而是网关自己出了问题。出现即告警，
并按紧邻的 `vpn_stack_*` 日志定位。

### 审计

拒绝写审计事件 `vpn_packet_denied`，含 `reason`、`peerId`、目标地址与协议，**不含**任何密钥与包内容。

为避免审计洪泛，同一 `peer + reason + 目标 /24` 在窗口内聚合计数后再落库：窗口 1 分钟
（`vpnDenialWindow`），活跃桶上限 4096（`vpnDenialMaxKeys`）。所以审计里的 `count`
是一分钟内的次数，不是一次一条；桶数触顶时新的桶会被丢弃（而不是无限增长），
这时**指标仍然是准的**，只有审计的粒度会退化。

目标地址按 `/24` 聚合是刻意的：一个扫描 256 个地址的 peer 产生的是一条审计而不是 256 条，
而运维需要知道的正是“这个 peer 在扫一个网段”。

## 指标现状

**这一版复用既有向量，没有 VPN 专用指标。** 规格 §13 列的 6 个
`tunnelmesh_vpn_*` 向量属阶段 8，尚未实现。当前实际能看到的是：

| 指标 | 标签 | VPN 下的含义 |
| --- | --- | --- |
| `tunnelmesh_bytes_total` | `component="vpn"`, `direction`, `protocol` | 隧道载荷字节。`direction` 为 `ingress`（peer→内网）/`egress`（内网→peer）；`protocol` 为 `tcp`/`udp`/`icmp-echo` |
| `tunnelmesh_streams_total` | `protocol`, `result`, `error_class` | 流的结果计数。**丢包也走这里**，`result="rejected"` |
| `tunnelmesh_streams_active` | `protocol` | 活跃流 gauge |
| `tunnelmesh_stream_errors_total` | `protocol`, `error_class` | 流错误 |
| `tunnelmesh_registry_lease_total` | `operation="vpn_subnet_renew"`, `result`, `error_class` | 子网租约续租 |

### 一个必须知道的精度损失

丢包复用 `tunnelmesh_streams_total{result="rejected"}`，而 `ObserveStream` 同时维护活跃流 gauge，
在 `rejected` 上会**减一次却没有对应的加一次**。所以一阵密集拒绝会把 `tcp` 的
`tunnelmesh_streams_active` 拉到低于真实活跃流数，直到下一次 accept 把它校正回来。

**counter 本身是精确的**，告警读的是 counter，所以告警不受影响。
受影响的是 gauge——**不要用 `tunnelmesh_streams_active` 做容量判断或告警**，
活跃流的权威来源是 `/api/v1/vpn-peers/{peerId}/flows` 与 `/api/v1/vpn-nodes` 的 `peers`。

阶段 8 会用专用的 `tunnelmesh_vpn_packets_dropped_total` 替换这个 body，包路径不变
（网关依赖的是 `vpnMetrics` 小接口而不是 `*observability.Metrics`，就是为了这次替换）。
在那之前，Grafana 的 VPN Row 与告警规则应按 counter 写。

### 标签基数

`error_class` 是 16 个已发布常量之一，这正是它能做 Prometheus 标签的原因。
peer ID、VPN IP、目标地址**一律不是标签**——它们是审计字段。任何用请求内容拼标签的改动
都会让 registry 无界增长。

## 结构化事件

按用途分组，全部带 `node_id`：

**生命周期**：`vpn_gateway_started`、`vpn_gateway_enabled`、`vpn_gateway_stopped`、
`vpn_gateway_close_failed`、`vpn_peers_loaded`、`vpn_peer_load_refused`、`vpn_handler_missing`、
`vpn_background_loop_stop_timeout`

**peer 热更新**：`vpn_peer_apply_failed`、`vpn_peer_device_install_failed`、
`vpn_peer_device_remove_failed`、`vpn_peer_device_remove_render_failed`、`vpn_peer_key_rotated`、
`vpn_peer_flows_stopped`

**子网与租约**：`vpn_node_address_learned`、`vpn_node_address_unavailable`、
`vpn_node_address_pool_invalid`、`vpn_subnet_list_failed`、`vpn_subnet_renew_failed`、
`vpn_subnet_lease_lost`

**数据面**：`vpn_packet_denied_audit_failed`、`vpn_egress_open_failed`、`vpn_egress_open_timeout`、
`vpn_tcp_accept_failed`、`vpn_tcp_unusable_peer_address`、`vpn_udp_emit_failed`、
`vpn_udp_stream_ended`、`vpn_icmp_emit_failed`、`vpn_icmp_capability_probe_failed`、
`vpn_flows_reaped`、`vpn_listeners_reclaimed`、`vpn_device_error`

**协议栈**：`vpn_stack_unmatched_tcp`、`vpn_stack_address_conflict`、
`vpn_stack_listener_close_failed`、`vpn_stack_release_failed`、`vpn_stack_remove_nic_failed`

两个刻意限流的：

- `vpn_stack_unmatched_tcp` **每进程每网关只打一行**。一个探测端口的 peer 否则会每段一行；
  量在 counter 里，形状在日志里。所以看到一行不代表只发生了一次，看它的 `count` 字段。
- wireguard-go 自己的错误文本经 `vpnDeviceLogger` 限流到每秒最多一行（`vpnDeviceLogInterval`）。

`vpn_stack_unmatched_tcp` 的 `count` 非 0 且持续增长，说明有 TCP 段找不到监听器：
要么是对端在探测，要么是监听器注册顺序出了问题（后者是 D4 要保证的不变量，出现即 bug）。

## 常见故障

| 现象 | 先看什么 | 典型根因 |
| --- | --- | --- |
| 所有用户握手失败 | `ss -lunp` 有没有那个 UDP 端口；启动日志有没有 `vpn_gateway_started` | 安全组没放行；`listen` 写了内网地址；二进制没带 `-tags vpn`（此时进程根本起不来） |
| 部分用户握手失败 | `tunnelmesh_streams_total{error_class="peer_unknown"\|"peer_revoked"\|"peer_expired"}` | 密钥轮换后没重新下发；peer 过期 |
| 握手成功但全部连不通 | `error_class="egress_unavailable"` / `egress_timeout` | 出口 Agent 掉线；Agent 宿主机到内网服务不通 |
| 个别目标连不通 | `error_class="target_denied"` / `port_denied` + `vpn_packet_denied` 审计 | peer 策略没放开；用户误以为 `AllowedIPs` 是全流量 |
| `ping` 不通但 TCP 通 | `error_class="icmp_unsupported"` | Agent 没协商 `stream_icmp_echo.v1`；宿主机 `net.ipv4.ping_group_range` 没放开 |
| 大包/UDP 应用异常 | `error_class="fragment_dropped"` / `oversize_dropped"` | 不支持分片；UDP 报文超过路径 MTU |
| 新连接失败、存量正常 | `error_class="capacity_exhausted"` | `max_flows_per_peer` / `max_flows_total` / `icmp_max_concurrent` 触顶 |
| 间歇性丢包 | `error_class="rate_limited"` | `packet_rate_per_peer` 偏低 |
| 签发返回 409 `vpn_node_disabled` | `/api/v1/vpn-nodes` 的 `enabled`；启动日志 | `server.vpn.enabled: false`；或 `TUNNELMESH_VPN_NODE_PRIVATE_KEY` 不可用（此时数据面启动会失败，管理面降级） |
| 签发返回 409 `vpn_ip_pool_exhausted` | `/api/v1/vpn-nodes` 的 `allocated`/`capacity` | 本节点子网用完；累计签发数（含已吊销）占满 |
| `flows` 接口返回 501 | 该 peer 的 `node_id` 是否是本节点 | 集群里 peer 由别的节点服务；或本进程无数据面。**501 不是空列表**，空列表才表示“在本节点且空闲” |
| 退出很慢 / 强制断流 | `vpn_gateway_stopped` 的 `waited`、`flows_forced`、`steps` | `shutdown_timeout` 内有长连接未结束；调大超时或接受强制断流 |

排障路径遵循 AGENTS.md 的约定：access_log → app_log → trace_id 链路 → 根因。
VPN 流的 `Metadata` 携带 traceparent，所以一条 VPN 流可以和它经 Agent 出去的那一腿串在同一个 trace 里。
逻辑 traceroute（`POST /api/v1/agents/{agentId}/trace`）可以验证 Agent 到内网目标那一腿，
但**不能**验证用户到网关那一腿——那是 WireGuard，不是本产品的逻辑链路。

## 相关文档

- [VPN 网关部署](../deployment/vpn-gateway.md)：构建变体、放行、密钥注入、IP 池规划、验证与回滚
- [配置说明 · 内嵌 VPN 网关](configuration.md#内嵌-vpn-网关wireguard)：全部键与取值范围
- [可观测性](observability.md)：统一 Grafana Dashboard 与告警阈值
- [SLO](slo.md)、[容量与压测](capacity.md)
- [故障排查](troubleshooting.md)：跨组件的通用排障入口
- [ADR 0002](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)：公网入口与特权边界决策
