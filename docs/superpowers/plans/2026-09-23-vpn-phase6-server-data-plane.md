# VPN 网关 阶段 6：Server 数据面（WireGuard + netstack）Implementation Plan

- 日期：2026-09-23
- 分支：`codex/vpn-phase6-server-data-plane`（stacked 在 `codex/vpn-phase7-agent-icmp` 之上，基点 `a859223`）
- 规格：[内嵌 VPN 网关（WireGuard）设计](../specs/2026-09-19-embedded-vpn-gateway-design.md) §2.2、§3、§4.3、§5.2、§5.3、§5.4、§8、§11、§12.2、§13、§14、§15 阶段 6、§18 验收 1-7 与 10
- 决策载体：[ADR 0002](../../architecture/adr/0002-public-ingress-and-embedded-vpn.md)（内存态 TUN + netstack 已批准，Server 不需要 `CAP_NET_ADMIN`）
- 技术前提：`test/spike/vpn-task0/REPORT.md`（Task 1-4 全 PASS）与该报告「必须回流到阶段 1 的设计修正」1-7 项
- 前置阶段：[阶段 4 PR 记录](../../pull-requests/2026-09-21-vpn-phase4-pure-logic-and-api.md)（纯逻辑与管理 API、`NodePublicKey` 留空、`flows`/`vpn-nodes` 返回 501）、[阶段 7 PR 记录](../../pull-requests/2026-09-23-vpn-phase7-agent-icmp.md)（`icmp-echo` 协议、能力协商、`streamProtocolCapability`、`VPNAgentCapabilityProbe`）

## 1. 目标

让 `tunnelmesh-server` 在 `-tags vpn` 构建下真正持有一个 WireGuard 网关，把用户 VPN 流量经既有 relay 送到 Agent：

1. 主模块引入 `gvisor.dev/gvisor` 与 `golang.zx2c4.com/wireguard`，且**只有** `//go:build vpn` 文件引用它们；默认构建的 `tunnelmesh-server` 不链接任何一个。
2. 内存态 `tun.Device` 桥接 netstack `channel.Endpoint`（无 `/dev/net/tun`、无 `CAP_NET_ADMIN`，只绑定一个 UDP 端口）。
3. 逐包管线：源 IP 反查 peer → 令牌桶限速 → `vpn.PacketPolicy` → 按 IP 协议分派；每一次丢弃都落到规格 §12.2 已发布的 `error_class` 并进入聚合审计 `vpn_packet_denied`。
4. TCP 在 netstack 内终结（promiscuous + 按目标动态 `AddProtocolAddress` + `gonet.ListenTCP` + 引用计数回收），accepted conn 经 `relay.NodeTransport.OpenStream(protocol="tcp")` 与内网服务对接。
5. UDP 数据报经 `OpenStream(protocol="udp")` 中继，回程包由网关自建 IPv4/UDP 头并经 `WritePackets` 出向。
6. ICMP echo 经阶段 7 的 `icmp-echo` 流中继（`protocol.EncodeICMPEchoRequest/DecodeICMPEchoReply`），应答同样自建 ICMP echo reply 出向；能力不足按 `icmp_unsupported` 丢弃且不开流。
7. peer 集合以数据库为权威、内存为副本：启动全量装载，签发/修改/轮换/吊销后热加载；吊销与轮换**立即**断流并使旧公钥失效。
8. 节点身份从 `TUNNELMESH_VPN_NODE_PRIVATE_KEY` 注入，`VPNPeerServiceDeps.NodePublicKey` 不再为空，`config:reveal` 从恒 409 变为可用。
9. 生命周期：`shutdown_timeout` 内 drain、空闲流回收、子网租约周期续租、关闭顺序确定（流 → WireGuard device → NIC/stack → goroutine）。
10. `GET /api/v1/vpn-peers/{peerId}/flows` 与 `GET /api/v1/vpn-nodes` 从 501 变为真实数据；控制台展示活跃流、去掉常驻的 `vpn.dataPlanePending` 提示。
11. 快速失败：无 tag 构建 + `enabled: true` → 启动失败并输出 `this binary was built without VPN support; rebuild with -tags vpn`；tagged 构建 + 缺节点私钥 → 启动失败并点名环境变量。
12. 文档：新增用户/部署/运维三份 VPN 文档，更正阶段 4「`TUNNELMESH_VPN_NODE_PRIVATE_KEY` 本版不被读取」等已过时表述，实测并记录带/不带 tag 的二进制体积增量。

## 2. 非目标

- **不做阶段 8 的可观测性与发布面**：不新增 `tunnelmesh_vpn_*` 指标向量、不动 `deploy/grafana`、不加告警规则、不写 SLO、不改 `scripts/build-release.sh` 的产物矩阵、不建 `test/e2e/vpn/`。本阶段只复用现有向量（见 D12）。
- 不做 Schema 变更：`SchemaVersion` 保持 15，`migrations/` 零改动，不建 `vpn_flows` 表（规格 §7.1 明确运行时流状态不入库）。
- 不新增稳定错误码：`internal/vpn/errors.go` 的 10 个码集合不变，`internal/server/vpn_peer_openapi_test.go` 的双向相等守卫必须继续绿灯。
- 不做 IPv6、不做 IP 分片重组、不做 ICMP echo 之外的任何 ICMP 类型、不做 P2P/NAT traversal、不做任意远程命令执行（规格 §4.3、ADR 0002 保留的禁令）。
- 不改 Agent：阶段 7 已交付 `icmp-echo` 与 TCP/UDP 出口，本阶段只是第一个真正调用它们的一方。
- 不改 `Dockerfile`：见 D1（本阶段不抬 `go` 指令，因此 Task 0 记录的 `ARG GO_VERSION=1.23` 落后问题不由本阶段触发，仍属阶段 8 的部署面）。
- 不做跨节点 VPN 状态/流查询 RPC：`vpn-nodes` 只报告本节点，`flows` 只服务本网关承载的 peer（见 D13）。
- 不实现源地址保留：内网侧看到的仍是 Agent 宿主机地址（规格 §4.3 与 ADR 0002 已定）。

## 3. 前置核实结论（实测，非推断）

以下每条都在本机（darwin/arm64、go1.27.1）实测过，命令与结果一并记录：

1. **`go` 指令不需要抬到 1.26.3（对阶段 1/3 记录的事实更正）。** 两个依赖各自的 `go.mod` 都声明 `go 1.23.1`
   （`~/go/pkg/mod/gvisor.dev/gvisor@v0.0.0-20250503011706-39ed1f5ac29c/go.mod` 第 3 行、
   `~/go/pkg/mod/golang.zx2c4.com/wireguard@v0.0.0-20260522210424-ecfc5a8d5446/go.mod` 第 3 行）。
   在 `go.mod` 保持 `go 1.26.0` 的副本里加入两个依赖后，`go build -tags vpn`、`go vet -tags vpn`、
   `go test -tags vpn`（含 `-race`）与不带 tag 的 `go build ./...` 全部通过。
   因此阶段 1 PR 记录里「抬升 go 指令到 1.26.3（gVisor 要求）时必须同步抬高 Dockerfile ARG」的前提不成立，
   本阶段 `go.mod` 只新增 require 行，`Dockerfile` 不改。
2. **离线可解析。** `GOPROXY=off go get gvisor.dev/gvisor@v0.0.0-20250503011706-39ed1f5ac29c
   golang.zx2c4.com/wireguard@v0.0.0-20260522210424-ecfc5a8d5446` 成功，只新增
   `gvisor.dev/gvisor`、`golang.zx2c4.com/wireguard` 两条直接依赖与
   `github.com/google/btree v1.1.2`、`golang.zx2c4.com/wintun`、`golang.org/x/time v0.7.0` 三条 indirect；
   `golang.org/x/sys v0.48.0`、`x/net v0.59.0`、`x/crypto v0.57.0` 版本**不变**（与主模块已有版本一致）。
   加入后主模块 `go list -m all | wc -l` = 228（spike 模块的 96 条与此无关，两者是独立 module）。
3. **不运行 `go mod tidy`。** 实测离线 `go mod tidy` 会因 grpc 的测试依赖 `gonum.org/v1/gonum` 无法解析而失败
   （`module lookup disabled by GOPROXY=off`），与本阶段无关且早于本阶段；`go.mod`/`go.sum` 由 `go get` 精确写入。
4. **全 tagged 包会打断默认构建。** 一个只有 `//go:build vpn` 文件的包，在不带 tag 时
   `go test ./internal/vpnprobe/` 输出 `FAIL ... [setup failed]`（`build constraints exclude all Go files`）。
   因此本阶段所有 tagged 文件都放进 `internal/server`（该包有大量 untagged 文件），**不新建 vpn 专属包**。
5. **数据面要用的 gVisor / wireguard-go 符号在钉死版本下全部存在且可编译**：`stack.New`、`CreateNIC`、
   `SetPromiscuousMode`、`SetRouteTable`、`SetTransportProtocolHandler`、`AddProtocolAddress`、`RemoveAddress`、
   `channel.New`、`Endpoint.WritePackets`、`Endpoint.InjectInbound`、`stack.NewPacketBuffer`+`PacketBufferOptions.Payload`、
   `stack.PacketBufferList`、`gonet.ListenTCP`、`header.IPv4EmptySubnet`、`tcpip.AddrFrom4Slice`、
   `device.NewDevice`、`conn.Bind`、`tun.Device`。一个覆盖以上全部符号的编译探针在 `go 1.26.0` 下
   `go vet -tags vpn` 与 `go test -tags vpn` 均通过。
6. **`tcpip.Error` 不实现 `error`**（只是 `fmt.Stringer`），因此错误判别只能用类型断言
   `terr.(*tcpip.ErrDuplicateAddress)`，`errors.As` 无法编译（Task 0 报告第 5、7 项，已在源码复核）。
7. **`channel.Endpoint.WritePackets` 不做 MTU 校验**：队列满时返回 `*tcpip.ErrNoBufferSpace`，其余情况原样入队
   （`pkg/tcpip/link/channel/channel.go:277-291`）。超限丢弃必须由网关自己执行（Task 0 第 3 项）。
8. **`WriteNotify` 在队列锁外调用**（`pkg/tcpip/link/channel/channel.go:99-104` 注释 "Send notification outside of lock"），
   所以在通知回调里 `ep.Read()` 排空不会与写入方死锁。上游 `tun/netstack/tun.go` 的 `incomingPacket` 是**无缓冲** channel，
   `WriteNotify` 会阻塞栈的出向路径；本阶段不复用它（D8）。
9. **WireGuard UAPI 支持增量改 peer**：`device/uapi.go:236` 的 `replace_peers=true` 会 `RemoveAllPeers()`，
   而 `:317` 支持单个 peer 的 `remove=true`、`:363` 支持 `replace_allowed_ips=true`、`:373` 支持 `allowed_ip`（前缀 `-` 表示删除）。
   因此热加载可以只改一个 peer，不必重建全部 peer 的握手状态（D11）。
10. **`conn.Bind` 接口只有 6 个方法**（`conn/conn.go:34-58`：`Open(port) ([]ReceiveFunc, uint16, error)`、`Close`、
    `SetMark`、`Send`、`ParseEndpoint`、`BatchSize`），`conn.NewDefaultBind()` 走 `ListenPacket(ctx, network, ":"+port)`
    即**通配**绑定（Task 0 已实测并纠正过 "listening on 127.0.0.1" 的错误表述）。要尊重 `server.vpn.listen` 的 host
    必须自己实现 Bind（D7）。
11. **ICMP 走 netstack 是可行的但不必要**：`pkg/tcpip/transport/icmp/protocol.go:91-95` 的 `ParsePorts` 对 ICMPv4
    返回 `(0, hdr.Ident(), nil)`，即注册 `icmp.NewProtocol4` 后 `SetTransportProtocolHandler` 能被调到。
    但 `stack/nic.go:846-851` 显示未注册的传输协议直接计 `unknownL4ProtocolRcvdPacketCounts` 并判
    `TransportPacketProtocolUnreachable`，而 demux 未命中且 handler 返回 false 时会走
    `HandleUnknownDestinationPacket` → 可能由栈自己产生 ICMP 差错包。为保证「网关不主动构造 ICMP echo 之外的任何 ICMP」，
    UDP 与 ICMP 都在网关自己的管线里处理，netstack 只注册 TCP（D3）。
12. **首个 SYN 被丢弃是可以消除的**：Task 0 第 7 项的根因是「钩子在 demux 未命中时才触发、监听器注册是异步的」。
    本设计的注册点在**网关自己的入向管线**里、`InjectInbound` 之前同步完成，因此 demux 必然命中，
    不存在客户端 RTO 惩罚（D4）。ADR 0002 的 Consequences 里那条「production connect latency includes one client RTO」
    在本阶段被工程手段消除，PR 记录里要写明这一点。
13. **阶段 7 交付的接口可直接消费**：`protocol.StreamProtocolICMPEcho`/`EncodeICMPEchoRequest`/`DecodeICMPEchoReply`、
    `server.VPNAgentCapabilityProbe`（三态）、`server.streamProtocolCapability`（开 `icmp-echo` 流选路必须经它）。
    `VPNPeerServiceDeps.NodePublicKey` 当前为空 ⇒ `config:reveal` 恒 409 `vpn_node_disabled`
    （`internal/server/vpn_peer_service.go:950` `nodeIdentity()`）。
14. **前端已按 200 形状写好**：`web/src/api/vpn.ts` 的 `VpnNodeStatus`/`VpnFlow` 每个字段都可选、
    `listVpnPeerFlows` 已实现但视图未用（`web/src/tests/vpn.spec.ts:176` 断言源码里不出现该函数名）、
    `web/src/views/VpnPeers.vue:10` 常驻 `vpn.dataPlanePending` 提示、`web/src/views/Servers.vue:28-40` 已渲染节点区块。
    本阶段要让这三处从「占位」变成「真实」，其中 `vpn.spec.ts:176` 的守卫必须反向。
15. **OpenAPI 守卫测试写死了 2 个 phase 标记**：`internal/server/vpn_peer_openapi_test.go:112-118` 断言
    `x-tunnelmesh-phase` 恰好出现 2 次。两个端点落地后标记必须移除，该断言改为 0。
16. **二进制体积增量尚未实测**：Task 0 报告明确「真实增量必须在引入 `//go:build vpn` 后用带/不带 tag 的两次构建实测」。
    本阶段是唯一引入该 tag 的阶段，因此这项实测落在 T15，数字写进 PR 记录，不作推测。

## 4. 架构决策

### D1：`go.mod` 只加依赖，不抬 `go` 指令，`Dockerfile` 不动

第 3 节第 1 条实测推翻了「gVisor 要求 go 1.26.3」的前提。抬 `go` 指令会连带抬高 CI（`go-version-file: go.mod`）
与镜像工具链的要求，而收益为零，违反 KISS 与「配置变更要有真实动因」。因此：
`go.mod` 新增 2 条直接 require 与 3 条 indirect，`go 1.26.0` 保持不变；`Dockerfile` 的 `ARG GO_VERSION=1.23`
是 Task 0 已记录的既有问题，不由本阶段触发也不由本阶段修（属阶段 8 的部署面），本阶段只在 PR 记录里重申。

### D2：build tag 的边界按「是否导入重依赖」划，不按「是否属于数据面」划

规格 §5.2 写的是「数据面文件全部带 `//go:build vpn`」。本阶段按更严格的判据执行：**只有 import 了 gvisor 或
wireguard 的文件才带 tag**。流表、令牌桶、拒绝聚合、报文解析、peer 表这五类是纯 Go（`encoding/binary`、`net/netip`、
`internal/vpn`、`storage`、`relay` 接口），不带 tag。理由：

- 它们在默认构建里就能被 `go test ./...` 与 `-race` 覆盖，不必等 tagged 门禁，反馈更快；
- 结构上保证隔离：一旦这些文件误 import gvisor，默认构建**立刻编译失败**，而不是悄悄把重依赖带进所有部署；
- 规格 §5.4 的意图是「不用 VPN 的部署二进制不含 netstack 与 wireguard-go」，纯逻辑文件不违反该意图。

这是对规格 §5.2 的显式偏离，记入 PR 记录的「与计划的偏差」。tagged 文件仍严格用规格给出的文件名
（`vpn_device.go`、`vpn_stack.go`、`vpn_packet.go`、`vpn_flows.go`→拆分见 D2.1、`vpn_icmp.go`、`vpn_lifecycle.go`、
`vpn_gateway.go`），新增 `vpn_bind.go`（规格未预见需要自定义 Bind，见 D7）、`vpn_wire.go`、`vpn_tcp.go`、`vpn_udp.go`、
`vpn_peers.go`、`vpn_ratelimit.go`、`vpn_denials.go`、`vpn_runtime.go`、`vpn_stub.go`。

#### D2.1：`vpn_flows.go` 是 untagged 的流表，协议中继各自成文件

规格把「per-peer 流表、并发与速率限额、空闲回收、`openEgress` 纪律」都放进 `vpn_flows.go`。本阶段拆成：
`vpn_flows.go`（untagged：流表 + 上限 + 空闲回收 + 快照）、`vpn_ratelimit.go`（untagged：令牌桶）、
`vpn_tcp.go` / `vpn_udp.go` / `vpn_icmp.go`（tagged：三条协议路径各自的 openEgress 与数据搬运）。
`openEgress` 纪律（goroutine + cancel + 迟到流回收）在三条路径共用一个 untagged 帮助函数
`openVPNEgress`（放在 `vpn_flows.go`），照 `internal/server/proxy_entry.go:568` 的范式实现，不复用其代码
（`ProxyEntry.openEgress` 是方法且绑定了 `ProxyEntry.config`，强行复用会违反接口隔离）。

### D3：netstack 只终结 TCP；UDP 与 ICMP 在网关管线内直接中继

见第 3 节第 11 条。收益：栈里不注册 udp/icmp 传输协议 ⇒ 栈永远不会替我们产生 ICMP port-unreachable 或
destination-unreachable，「网关只构造 ICMP echo reply」这条边界由结构保证而不是由测试保证；UDP 数据报边界
天然精确（一次 Read = 一个数据报 = 一次 `WritePackets`）；每个包都经过同一条策略与计数路径。
代价：UDP/ICMP 的 IPv4 头与校验和由网关自己组装，这段代码必须有单测覆盖（T8、T9）。
入向 UDP 校验和**不重复校验**：WireGuard 已对每个数据报做 AEAD 认证，坏校验和只可能来自恶意 peer，
其后果是内网服务忽略一个坏数据报；为此新增一个 `error_class` 不值得（已发布的类集合是契约）。此取舍写入用户文档。

### D4：监听器在 `InjectInbound` 之前同步注册，消除首包丢失

入向管线在判定「TCP 且该 (目标地址, 目标端口) 没有监听器」时，**同步**完成
`AddProtocolAddress(dst/32)` + `gonet.ListenTCP` + 启动 accept 泵，然后才 `InjectInbound`。
因此 demux 必然命中，首个 SYN 不被丢弃，用户不会白等一次客户端 RTO（Linux 初始 RTO 1s）。
非 SYN 段命中未注册四元组时同样注册（让栈按标准 TCP 回 RST，而不是静默吃掉），受同一套上限约束。
这消除了 Task 0 第 7 项与 ADR 0002 Consequences 里那条延迟惩罚。

### D5：注册冲突按「不变量破裂」处理，绝不动不属于自己的监听器

Task 0 实测过 32 个 goroutine 抢同一四元组时恰好 1 个成功、31 个拿到 `*tcpip.ErrDuplicateAddress` 与
`port is in use`，并警告失败方不得 `RemoveAddress` 或关闭监听器。本设计里注册发生在网关自己的互斥锁内，
**不存在并发失败方**；因此这两个错误只可能是「registry 与 stack 状态不一致」（例如上一代 gateway 泄漏），
处理为：计 `stack_error`、丢包、`slog.Error` 一次，**不**做 `RemoveAddress`、**不**关闭任何监听器。

### D6：目标地址按引用计数回收

`listenerRegistry` 以 `netip.AddrPort` 为键，值为 `{ln *gonet.TCPListener, refs int}`；`refs` 是「已 accept 的连接数 +
在途 accept」。归零时关闭监听器并 `RemoveAddress(dstAddr)`，避免 Task 0 第 2 项警告的无界增长。
回收与新 SYN 的竞争是安全的：注册在包路径内同步完成，地址被移除后到达的 SYN 会重新注册。
`RemoveAddress` 失败只计数并记日志（地址残留不影响正确性，只影响内存）。

### D7：自定义 `conn.Bind`，绑定 `server.vpn.listen` 的 host 而不只是端口

`conn.NewDefaultBind()` 忽略 host（第 3 节第 10 条）。`server.vpn.listen` 是 `host:port` 且被
`validateVPN` 强制（`internal/config/config.go:829-831`），因此实现 `vpnBind`：单个 `net.UDPConn` 精确绑定该地址，
`Open(port)` 用配置里的 host + 传入的 port（port 为 0 时用配置端口），`BatchSize()=1`（不做 GSO），
`SetMark` 返回不支持错误（网关从不设 fwmark），`ParseEndpoint` 返回携带 `netip.AddrPort` 的 `vpnEndpoint`。
这样 `listen: "10.0.0.1:51820"` 就真的只绑在该地址上，暴露面与配置一致，也让集成测可以绑回环。

### D8：设备出向队列有界，满则丢并计数；不复用上游 `netTun`

上游 `tun/netstack/tun.go` 的 `incomingPacket` 是无缓冲 channel，`WriteNotify` 会阻塞 netstack 的出向路径
（第 3 节第 8 条）。本阶段的 `memoryDevice` 用容量 1024 的 buffered channel + 非阻塞发送，满则丢弃并计
`capacity_exhausted`，同时暴露丢弃计数供阶段 8 的告警使用。`WriteNotify` 里循环 `ep.Read()` 直到 nil
（一次通知可能对应多个包），`Read` 一次返回一个包并 `DecRef`，`Close` 后 `Read` 返回 `os.ErrClosed`。
设备不打开任何文件：`File()` 返回 nil，且测试断言进程内不存在 `/dev/net/tun` 的打开路径。

### D9：逐包管线的判定顺序是安全语义，不可重排

`parse → 非 IPv4/分片 → 源 IP 反查 peer（peer_unknown / peer_revoked / peer_expired）→ 令牌桶（rate_limited）
→ 目标是网关自身 VPN 地址（target_denied）→ vpn.PacketPolicy.Allow（fragment/oversize/protocol/metadata/target/port）
→ 分派`。其中：

- 分片与超限在最前，因为非首片没有端口，先放行再判端口等于没有端口白名单（`internal/vpn/policy.go:186-190` 已定此序）；
- 网关自身 VPN 地址（`Pool.NodeAddress`）恒拒：它不是内网服务，把它交给 Agent 只会得到一次无意义的连接失败；
- 策略**逐包**评估而不是建流时评估一次：peer 可以被 PATCH 改策略、被吊销、到期，正在服务的连接必须立刻失去授权
  （规格 §18 验收 5）。已建立的 TCP 流在后续包被拒时关闭，计对应 class。

### D10：peer 集合是数据库的内存副本，写库成功后同步；同步失败不回滚数据库

`VPNPeerService` 在 Issue/Rotate/Update/Revoke 写库成功后调用 `VPNPeerSink`（新接口，untagged）：
`ApplyPeer(storage.VPNPeer)` / `RemovePeer(peerID)`。sink 为 nil 时是 no-op（现有测试不受影响）。
应用失败（例如 UAPI 拒绝）记 `slog.Error` + 计数，**不**回滚数据库：AGENTS.md 的单一数据源原则要求 DB 是权威，
内存视图落后于 DB 时的修复手段是重启网关（从 `ListByNode` 全量重装），而不是让管理 API 报一个已经生效的写为失败。
Rotate 改变公钥 ⇒ applier 先 `remove=true` 旧公钥再加新公钥，旧公钥立即失效（验收 6）。
Revoke ⇒ 移除 WireGuard peer + 立即关闭该 peer 全部在途流（验收 5）。
过期 peer 在装载与逐包两处都判（`expires_at` 已过 ⇒ `peer_expired`），不依赖后台清理任务。

### D11：WireGuard peer 更新用增量 UAPI

见第 3 节第 9 条。`replace_peers=true` 会拆掉所有 peer 的握手状态，一次改名就让全节点重连，
与 AGENTS.md「故障隔离」相悖。因此：新增/更新 = `public_key=<hex>\nreplace_allowed_ips=true\nallowed_ip=<vpnIP>/32\n`，
删除 = `public_key=<hex>\nremove=true\n`。节点自身身份（`private_key` + `listen_port`）只在启动时设置一次。
UAPI 字符串由 untagged 的 `internal/vpn/uapi.go` 渲染并做黄金文件测试，tagged 侧只负责 `IpcSet`
（这样密钥格式的正确性在默认构建里就能测）。

### D12：指标只复用现有向量，专用向量留给阶段 8

- 字节：`ObserveBytes("vpn", "ingress"|"egress", protocol, n)`（`bytes_total` 有 `component` 标签）。
- 流：`ObserveStream(protocol, result, errorClass)`；实测 `NormalizeProtocol`（`internal/observability/metrics.go:205-212`）
  会把 `icmp-echo` 归一成 `unknown`，因此本阶段把 `icmp-echo` 加进白名单（新增一个低基数标签值，不新增指标）。
- 租约：续租失败走 `ObserveRegistryLease("vpn_subnet_renew", result, class)`。
- 网关只依赖一个 untagged 的 `vpnMetrics` 小接口（依赖倒置），阶段 8 增加 `tunnelmesh_vpn_*` 向量时改适配器即可，
  数据面代码零改动。
- **诚实记录**：`streams_total`/`streams_active`/`stream_errors_total` 没有 `component` 标签，
  因此 VPN 流与既有 Agent 隧道流共用同一组序列，直到阶段 8 的专用向量落地。这一点写进 PR 记录与运维文档。

### D13：`flows` 与 `vpn-nodes` 的可观测边界

- `GET /api/v1/vpn-peers/{peerId}/flows`：**只有**本网关正在承载该 peer 时才答 200（`{items:[...]}`，可为空）；
  本节点没有运行中的网关、或该 peer 属于别的节点时答 501 `vpn_not_implemented`，消息说明「本节点无法观测该 peer 的流」。
  200 + 空列表会被读成「该 peer 没有流量」，而真相可能是「流量在别的节点上」，因此不选它。稳定码集合不变。
- `GET /api/v1/vpn-nodes`：答 200，`items` 里是**本节点**一条：`nodeId`、`enabled`、`listen`、`endpointHost`
  来自配置，`subnet`/`allocated` 来自 `VPNIPLeaseRepository.ListByHolder`，`capacity` 由子网前缀算出（`hosts-3`，
  与 `Pool.AllocateAddress` 的可用区间一致），`peers` 来自 `CountByNode`，`icmpCapable` = `cfg.ICMPEnabled` 且网关在运行。
  该端点在 tagged 与 untagged 构建下都可用（untagged 构建里 `enabled` 必为 false，否则启动已失败）。
  跨节点的 fleet 视图需要集群 RPC，与阶段 7 的能力可见性技术债合并记录，不在本阶段做。
- `docs/api/openapi.yaml`：两个端点补 200 响应 schema，移除 `x-tunnelmesh-phase: 6` 标记，
  重写 501 的描述（不再是「本版未实现」而是「本节点无法观测」）；守卫测试的 `phases != 2` 改为 `!= 0`。

### D14：drain 是有时限的终止，不是 GoAway

WireGuard 客户端没有「去别的节点重连」的语义，因此 VPN 的 drain 不套用 `FrameGoAway`。`Close()` 顺序固定：
标记 draining（新流一律拒，计 `capacity_exhausted`）→ 关闭全部 TCP 监听器并 `RemoveAddress` →
等待在途流至多 `shutdown_timeout` → 强制关闭剩余流 → 关闭 WireGuard device（连带 UDP socket 与 tun）→
`RemoveNIC` + `stack.Close()` + `ep.Close()` → 停止空闲回收与租约续租 goroutine → 冲刷拒绝聚合器。
5 分钟止损路径仍是「关 `server.vpn.enabled` 重启」（ADR 0002），不依赖 drain 成功。

### D15：子网租约周期续租，续租失败不拆流

网关启动后按 `vpnSubnetLeaseTTL/3`（`internal/server/vpn_peer_api.go:23` 当前为 1min ⇒ 20s）续租本节点持有的每个子网
（`VPNIPLeaseRepository.Renew`）。这回答阶段 4 PR 记录的开放问题：1 分钟 TTL 是合适的，
因为续租是周期性的且不在包路径上。续租返回 `ErrVPNIPLeaseStaleEpoch` 时：`slog.Error` +
`ObserveRegistryLease(...,"failed","stale_epoch")` + 停止该子网的续租循环，但**不**关闭在途 peer——
一次数据库抖动不应该把正在服务的流量黑洞掉；新的签发会被既有的 lease fence 挡住（`mapLeaseError` → 500）。

### D16：ICMP 在途计入 per-peer 流上限，且开流前复核能力

每个在途 echo 都占用一条 Agent 侧 stream，因此它就是一条流：进流表（`flows` 端点里以 `icmp-echo` 呈现）、
计入 `max_flows_per_peer` 与全局上限，另外受节点级 `icmp_max_concurrent` 信号量约束。
任一超限立刻丢包并计 `capacity_exhausted`，**不排队**（与阶段 7 D9 同理：排队会让限额变成空话）。
开流前调用 `VPNAgentCapabilityProbe.ProbeICMPEcho`：`CapabilityUnsupported` ⇒ 丢弃并计 `icmp_unsupported`，不开流；
`CapabilityUnverified` ⇒ 照阶段 7 D7 放行，由 Agent 的应答状态决定结果；
应答 `status=unsupported` ⇒ `icmp_unsupported`，`timeout` ⇒ `icmp_timeout`，`capacity_exhausted` ⇒ 同名 class，
`unreachable` ⇒ `egress_unavailable`，`cancelled` ⇒ `stack_error`，`ok` ⇒ 构造 echo reply 出向。
关联 ID 由网关分配（阶段 7 已定：不使用内核改写的 ICMP id），并写入 `protocol.ICMPEchoRequest.CorrelationID`。

## 5. 全局约束

1. 每个任务先写失败测试、实测红灯原文，再写最小实现；红灯原文首行写进该任务的提交 body（AGENTS.md TDD 流程）。
2. `go.mod`/`go.sum` 只允许出现第 3 节第 2 条列出的新增行；不得移动任何既有依赖的版本；不运行 `go mod tidy`。
3. `migrations/**`、`internal/storage/db.go` 的 `SchemaVersion`（15）、`internal/vpn/errors.go` 的 10 个稳定码、
   OpenAPI 的 vpn 路径集合（9 条）全部不变。允许改的只有：两个端点的响应 schema/描述、`x-tunnelmesh-phase` 标记的移除。
4. 任何 import gvisor 或 wireguard 的文件必须以 `//go:build vpn` 开头；门禁里用
   `go list -deps ./cmd/tunnelmesh-server | grep -Ec 'gvisor|golang.zx2c4.com/wireguard'` 必须为 `0` 来证明。
5. 私钥、Token、DSN、包载荷、握手字节不进日志/审计/指标。审计只含 peer ID、`error_class`、目标网段（/24）与聚合计数；
   指标标签只用已发布的低基数值，peer ID、VPN IP、完整目标地址不得作为标签（规格 §13）。
6. 一个任务一个提交，`<type>(<scope>): <subject>`，subject ≤50 字符、祈使句、无句号。
7. 时点记录零改写：阶段 1/3/4/5/7 的计划与 PR 记录只链接不修改；对它们的事实更正（尤其 D1 关于 `go` 指令的更正）
   只写在本计划与本阶段的 PR 记录里。
8. 门禁跑两遍：默认与 `-tags vpn`（`go test ./... -count=1`、`go test -race ./... -timeout 30m -count=1`、`go vet ./...`）。
   T13 动前端，因此前端门禁与 `scripts/verify-web-embed.sh` 也要跑。
9. 中间提交（T1-T11）不把网关装配进 `ServerRuntime`，装配在 T12 一次完成。理由：避免出现「配置开了、
   进程也不报错、但半装配的网关既不监听也不转发」的静默中间态。T12 之后规格 §18 验收 10 成立。
10. tagged 集成测必须在**无特权**环境可跑（不打开 `/dev/net/tun`、不需要 root、不需要 `ping_group_range`）：
    TCP/UDP/ICMP 的入向都用 `ep.InjectInbound` 或直接调用管线函数，出向从 `ep.Read()` 取，
    端到端矩阵测用两个进程内 WireGuard device 经回环 UDP 对接（照 `test/spike/vpn-task0/wgbridge` 范式）。

## 6. 文件清单

| 文件 | 任务 | tag | 内容 |
| --- | --- | --- | --- |
| `docs/superpowers/plans/2026-09-23-vpn-phase6-server-data-plane.md` | — | — | 本计划 |
| `go.mod`、`go.sum` | T1 | — | 新增 gvisor、wireguard 与 3 条 indirect |
| `internal/server/vpn_runtime.go` | T1 | untagged | seam：`VPNDataPlane`、`VPNPeerSink`、`VPNFlowSnapshot`、`VPNNodeStatus`、`VPNGatewayDeps`、`vpnMetrics` 与适配器、`ErrVPNBuildTagMissing`、`startVPNGateway`、`openVPNEgress` |
| `internal/server/vpn_stub.go` | T1 | `!vpn` | `vpnBuildTagged=false`、`newVPNGateway` 返回 `ErrVPNBuildTagMissing` |
| `internal/server/vpn_gateway.go` | T1 | `vpn` | `vpnBuildTagged=true`、`vpnGateway` 骨架、`newVPNGateway` |
| `internal/server/vpn_runtime_test.go` | T1 | untagged | 快速失败三态、依赖隔离断言 |
| `internal/vpn/nodekey.go`、`nodekey_test.go` | T2 | untagged | `NodeIdentity`、`NodeIdentityFromEnvironment`、`ErrNodeKeyMissing`/`ErrNodeKeyInvalid` |
| `internal/vpn/uapi.go`、`uapi_test.go` | T2 | untagged | WireGuard UAPI 行渲染（节点身份、单 peer 增改、单 peer 删除）+ 黄金文件 |
| `internal/server/vpn_peer_api.go` | T2、T13 | untagged | `SetVPN(cfg,nodeID,nodePublicKey)`、`SetVPNDataPlane`、`flows`/`vpn-nodes` 真实实现 |
| `internal/server/vpn_peer_api_test.go` | T2、T13 | untagged | reveal 从 409 变 200；flows/vpn-nodes 响应与边界 |
| `internal/server/vpn_peer_service.go` | T2、T7 | untagged | 消费 `NodePublicKey`；写库成功后调用 `VPNPeerSink` |
| `internal/server/vpn_peer_service_test.go` | T7 | untagged | sink 调用时序与失败不回滚 |
| `internal/server/vpn_device.go`、`vpn_device_test.go` | T3 | `vpn` | `memoryDevice`：`tun.Device` + `channel.Notification`、有界队列、丢弃计数 |
| `internal/server/vpn_stack.go`、`vpn_stack_test.go` | T4 | `vpn` | `newVPNStack`、promiscuous、路由表、TCP handler、`listenerRegistry` |
| `internal/server/vpn_wire.go`、`vpn_wire_test.go` | T5 | untagged | IPv4/TCP/UDP/ICMP 头解析成 `vpnWirePacket` |
| `internal/server/vpn_flows.go`、`vpn_flows_test.go` | T6 | untagged | 流表、上限、空闲回收、快照、`openVPNEgress` |
| `internal/server/vpn_ratelimit.go`、`vpn_ratelimit_test.go` | T6 | untagged | 令牌桶（0 = 不限） |
| `internal/server/vpn_denials.go`、`vpn_denials_test.go` | T6 | untagged | `vpn_packet_denied` 窗口聚合 + 有界键数 + 冲刷 |
| `internal/server/vpn_peers.go`、`vpn_peers_test.go` | T7 | untagged | peer 表：源 IP 索引、策略、状态/到期、Apply/Remove、applier 接口 |
| `internal/server/vpn_packet.go`、`vpn_packet_test.go` | T8 | `vpn` | 入向管线与分派、拒绝计数 |
| `internal/server/vpn_udp.go`、`vpn_udp_test.go` | T8 | `vpn` | UDP 流中继、回程包构造、`WritePackets` |
| `internal/server/vpn_icmp.go`、`vpn_icmp_test.go` | T9 | `vpn` | echo 中继、关联 ID、能力复核、reply 构造 |
| `internal/observability/metrics.go`、`metrics_test.go` | T9 | untagged | `NormalizeProtocol` 接受 `icmp-echo` |
| `internal/server/vpn_tcp.go`、`vpn_tcp_test.go` | T10 | `vpn` | accept 泵、上限、openEgress、splice、字节计数 |
| `internal/server/vpn_bind.go`、`vpn_bind_test.go` | T11 | `vpn` | `vpnBind`（`conn.Bind`）、`vpnEndpoint` |
| `internal/server/vpn_lifecycle.go`、`vpn_lifecycle_test.go` | T12 | `vpn` | `Start`/`Drain`/`Close` 顺序、空闲回收 goroutine、租约续租 |
| `internal/server/runtime.go` | T12 | untagged | 装配 gateway、注入 API、shutdown 关闭 |
| `docs/api/openapi.yaml` | T13 | — | flows/vpn-nodes 的 200 schema、移除 phase 标记 |
| `internal/server/vpn_peer_openapi_test.go` | T13 | untagged | `phases` 2 → 0 |
| `web/src/views/VpnPeers.vue`、`web/src/tests/vpn.spec.ts`、`web/src/i18n/messages/{zh-CN,en-US}.ts` | T13 | — | 活跃流抽屉/表格、去掉 `dataPlanePending`、守卫反向、i18n 中英对齐 |
| `internal/server/vpn_gateway_test.go` | T14 | `vpn` | 端到端矩阵：真实 WireGuard 握手 + TCP/UDP/ICMP + 吊销即断 + 策略拒绝 |
| `docs/user-guide/vpn.md`、`docs/deployment/vpn-gateway.md`、`docs/operations/vpn.md` | T15 | — | 新增三份文档 |
| `docs/operations/configuration.md`、`docs/operations/config-examples.md`、`docs/architecture/overview.md`、`docs/user-guide/server-admin.md`、`README.md`、`README.zh-CN.md`、`docs/README.md` | T15 | — | 活文档更新与索引重生成 |
| `docs/pull-requests/2026-09-23-vpn-phase6-server-data-plane.md` | T15 | — | PR 记录（含二进制体积实测） |

### 明确不改

- `migrations/**`、`internal/storage/**`（Repository 契约阶段 3 已定，本阶段只消费）、`Dockerfile`、`scripts/build-release.sh`。
- `internal/agent/**`、`internal/client/**`、`internal/protocol/**`（阶段 7 已交付 `icmp-echo` 与编解码，本阶段只调用）。
- `internal/vpn/errors.go` 的 10 个稳定码、`internal/vpn/policy.go` 的判定顺序与已发布 `error_class` 集合。
- 阶段 1/3/4/5/7 的计划与 PR 记录、ADR 0002、设计规格、`test/spike/**`。
- `deploy/**`（Grafana Row、告警规则属阶段 8）、`.github/workflows/ci.yml`（CI 缺 Go 门禁是 Task 0 记录的既有问题，属阶段 8）。
- `web/package.json`、`web/package-lock.json`（不新增前端依赖）。

## 7. 任务分解

### T1：依赖、构建隔离与快速失败

**Step 1（红灯）**：`internal/server/vpn_runtime_test.go` 断言
`vpnBuildTagged == false`（默认构建）、`startVPNGateway` 在 `Enabled:false` 时返回 nil 网关且无错误、
在 `Enabled:true` 且无 tag 时返回的错误 `errors.Is(err, ErrVPNBuildTagMissing)` 且消息含 `-tags vpn`、
在 `Enabled:true` 且缺 `TUNNELMESH_VPN_NODE_PRIVATE_KEY` 时返回的错误消息点名该环境变量（用 tagged 构建也要成立，
因此该检查在 untagged 代码里）、`go list -deps ./cmd/tunnelmesh-server` 不含 gvisor/wireguard。
Run `go test ./internal/server/ -run VPNRuntime -count=1` → 编译失败 `undefined: startVPNGateway`。

**Step 2（实现）**：`go get` 两个依赖（`GOPROXY=off`，版本与 spike 钉死的一致）；写 `vpn_runtime.go`（seam 类型 +
`startVPNGateway`：先查 `vpnBuildTagged`，再查节点私钥，再调 `newVPNGateway`）；写 `vpn_stub.go`（`!vpn`）；
写 `vpn_gateway.go`（`vpn`，骨架：保存 deps、`Close` 幂等、`PeerFlows` 返回空、`NodeStatus` 由配置组装、
`ApplyPeer`/`RemovePeer` 暂返回 nil，后续任务逐个替换）。

**Step 3（绿灯 + 提交）**：`go build ./... && go build -tags vpn ./... && go test ./internal/server/ -run VPN -count=1
&& go test -tags vpn ./internal/server/ -run VPN -count=1` 全绿。提交 `feat(server): isolate the vpn data plane build`。

### T2：节点身份与 reveal 打通

**Step 1（红灯）**：`internal/vpn/nodekey_test.go` 断言 base64（含 raw/std）与 hex 两种编码都能解析、
32 字节全零被拒、非 32 字节被拒、缺失环境变量返回 `ErrNodeKeyMissing`、
派生公钥与 `GenerateKeyPair` 的公钥格式一致（`ValidatePublicKey` 通过）；
`internal/vpn/uapi_test.go` 断言节点身份行、单 peer 增改行（含 `replace_allowed_ips=true`）、单 peer 删除行
（含 `remove=true`）与 `testdata/` 黄金文件逐字节一致，且**任何一行都不含私钥以外的密钥材料泄漏**（peer 块只含公钥）。
`internal/server/vpn_peer_api_test.go` 更正：装配了节点公钥后 `config:reveal` 返回 200 且 ini 含 `[Peer] PublicKey`。
Run → `undefined: vpn.NodeIdentityFromEnvironment`。

**Step 2（实现）**：`internal/vpn/nodekey.go`（复用 `DecodeKey`/`DerivePublicKey`/`ValidatePrivateKey`）、
`internal/vpn/uapi.go`；`SetVPN` 增第三参 `nodePublicKey`；`API.SetVPNDataPlane(VPNDataPlane)`；
`VPNPeerService` 在 Issue/Rotate/Update/Revoke 写库成功后调用 sink（本任务先接一个 no-op sink 字段，T7 补真实语义测试）。
更新 `SetVPN` 的 2 个调用点（`runtime.go:164`、`vpn_peer_api_test.go:292`）。

**Step 3（绿灯 + 提交）**：`go test ./internal/vpn/ ./internal/server/ -count=1` 绿。
提交 `feat(vpn): inject the node wireguard identity`。

### T3：内存态 TUN 设备（tagged）

**Step 1（红灯）**：`internal/server/vpn_device_test.go`（`//go:build vpn`）断言
`newMemoryDevice(mtu, queueSize)` 返回的对象满足 `tun.Device` 与 `channel.Notification`；
`Write` 一个合法 IPv4 包后能从注入侧读到它（用 `stack` 无关的直接断言：设备把包交给 `InjectInbound`，
测试用一个记录型 endpoint 或真实 `channel.Endpoint` + `ep.Read()`）；`WriteNotify` 后 `Read` 取到出向包且字节一致；
队列满时 `WriteNotify` 不阻塞、丢弃计数 +1；`Close` 后 `Read` 返回 `os.ErrClosed`、`Close` 幂等；
`File()` 返回 nil；`MTU()` 返回配置值；`BatchSize()` 返回 1；`Events()` 至少给出一个 `tun.EventUp`。
Run `go test -tags vpn ./internal/server/ -run VPNDevice -count=1` → 编译失败 `undefined: newMemoryDevice`。

**Step 2（实现）**：`vpn_device.go` 照 `tun/netstack/tun.go` 的 `netTun` 结构自行实现（D8），
不导入 `wireguard/tun/netstack` 包（避免连带引入它那份无缓冲队列与 DNS 代码）。

**Step 3（绿灯 + 提交）**：`go test -tags vpn ./internal/server/ -run VPNDevice -count=1 -race` 绿。
提交 `feat(server): bridge wireguard to an in-memory tun`。

### T4：netstack 装配与 TCP 监听器注册表（tagged）

**Step 1（红灯）**：`internal/server/vpn_stack_test.go` 断言
`newVPNStack(mtu)` 后 `SetPromiscuousMode` 生效（对未拥有目标的 SYN 能出 SYN-ACK）；
`registerTCPListener(dstAddr, dstPort)` 首次调用后注入 SYN 能收到 SYN-ACK（校验 `flags&TCPFlagSyn != 0`，
照 Task 0 评审修正，不能把 RST-ACK 当 SYN-ACK）、`Accept` 返回的 conn `RemoteAddr` 是注入方地址；
同一 (addr,port) 二次调用返回同一个监听器且 `refs` 递增；`release` 到 0 后监听器关闭且地址被移除
（再次注入 SYN 无 SYN-ACK）；未注册四元组的**非 SYN** 段被消费（handler 返回 true）且出向队列在 300ms 内
不出现 IP 协议号 1 的包（照 Task 0 的 `drainFor` 正向证明法）；`AddProtocolAddress` 返回
`*tcpip.ErrDuplicateAddress` 时计 `stack_error` 且不调用 `RemoveAddress`（用注入的假 stack 钩子或先手动占用地址制造冲突）。
Run → `undefined: newVPNStack`。

**Step 2（实现）**：`vpn_stack.go`：`stack.New`（只注册 `ipv4.NewProtocol` + `tcp.NewProtocol`，见 D3）、
`channel.New(1024, mtu, "")`、`CreateNIC`、`SetPromiscuousMode`、默认路由、
`SetTransportProtocolHandler(tcp, ...)`（demux 未命中时消费并计数）、`listenerRegistry`（互斥锁 + 引用计数 + D5/D6）。

**Step 3（绿灯 + 提交）**：`go test -tags vpn ./internal/server/ -run VPNStack -count=1 -race` 绿。
提交 `feat(server): terminate vpn tcp in netstack`。

### T5：报文解析（untagged）

**Step 1（红灯）**：`internal/server/vpn_wire_test.go` 断言
`parseVPNWirePacket` 对合法 IPv4+TCP/UDP/ICMP 返回正确的 `{Version, Protocol, Src, Dst, Fragment, Size, SrcPort, DstPort,
ICMPType, ICMPCode, ICMPID, ICMPSeq, Payload}`；IHL>5 的选项头被正确跳过；`MF=1` 或 `frag_offset>0` 标记为分片；
非 IPv4（version 6、version 4 但长度不足）返回明确错误；截断的 TCP/UDP/ICMP 头返回错误而不是 panic；
ICMP echo request（type 8 code 0）与 echo reply（type 0）被区分，其它类型（0x0b TTL exceeded）被标记为不支持；
UDP 载荷偏移与长度正确（含 `Length` 字段与实际字节不一致时以实际字节为准并标记畸形）。
Run → `undefined: parseVPNWirePacket`。

**Step 2（实现）**：`vpn_wire.go`，纯 `encoding/binary` + `net/netip`，不导入 gvisor。

**Step 3（绿灯 + 提交）**：`go test ./internal/server/ -run VPNWire -count=1` 绿。
提交 `feat(server): parse vpn tunnel packets`。

### T6：流表、令牌桶与拒绝聚合（untagged）

**Step 1（红灯）**：三个测试文件。
`vpn_flows_test.go`：登记/查询/移除；per-peer 上限与全局上限触发 `capacity_exhausted`；空闲回收只关掉超过
`idle_timeout` 的流；`Snapshot(peerID)` 的字段与 `VPNFlowSnapshot` 一致且不含载荷；并发登记同一 key 只产生一条流
（32 goroutine 竞争，照 Task 0 的并发实测范式）；`openVPNEgress` 在 opener 为 nil 时返回 `egress_unavailable`、
在 opener 阻塞超过 `connect_timeout` 时返回 `egress_timeout` 且**迟到成功的流被关闭**（不泄漏，照
`proxy_entry.go:568` 的 abandon 范式）。
`vpn_ratelimit_test.go`：`rate=0` 恒放行；令牌按时间补充（注入 clock）；突发不超过桶容量；并发调用不产生负令牌。
`vpn_denials_test.go`：同一 (peerID, class, /24) 在窗口内只写一条审计且 `count` 正确；窗口翻滚后重新计数；
键数超过上限后新键落入溢出桶而不是无界增长；审计 details 不含目标完整地址与任何密钥；`Flush` 在关闭时被调用。
Run → `undefined: newVPNFlowTable`。

**Step 2（实现）**：三个文件 + `VPNFlowSnapshot` 字段定形（`id`、`protocol`、`target`、`port`、`startedAt`、
`bytesSent`、`bytesReceived`，与 `web/src/api/vpn.ts` 的 `VpnFlow` 逐字段对齐）。

**Step 3（绿灯 + 提交）**：`go test ./internal/server/ -run 'VPNFlow|VPNRate|VPNDenial' -count=1 -race` 绿。
提交 `feat(server): track vpn flows limits and denials`。

### T7：peer 表与热加载（untagged）

**Step 1（红灯）**：`internal/server/vpn_peers_test.go` 断言
`ApplyPeer` 后按源 IP 能查到 peer 与其 `vpn.PacketPolicy`；`status=disabled`/`revoked` 的 peer 查不到（分别计
`peer_revoked`）；`expires_at` 已过 ⇒ `peer_expired`；同一 IP 换成另一个 peer ID ⇒ 旧索引被替换；
`RemovePeer` 后查询返回 `peer_unknown` 且回调 `onRemove` 被调用一次（网关用它断流）；
公钥变化时回调 `onReplacePublicKey(old, new)`（网关用它做 UAPI 增量更新，D10/D11）；
`ApplyPeer` 对 `allowed_ips` 为空或非法的行返回错误而不是静默建成一个全通策略；
`SnapshotForNode` 给出 `peers` 计数与 `subnet`/`allocated` 所需的输入。
`internal/server/vpn_peer_service_test.go` 新增：Issue/Rotate/Update/Revoke 成功后 sink 各被调用一次且顺序在写库之后；
sink 返回错误时**数据库里的行仍然存在**（不回滚）且错误被记录。
Run → `undefined: newVPNPeerTable`。

**Step 2（实现）**：`vpn_peers.go`（`vpnPeerTable`：`map[peerID]entry` + `map[vpnIP]peerID` 双索引、回调钩子）；
`VPNPeerService` 接 `VPNPeerSink`；`SetVPNDataPlane` 把网关注册为 sink。

**Step 3（绿灯 + 提交）**：`go test ./internal/server/ -run 'VPNPeer' -count=1 -race` 绿。
提交 `feat(server): hot reload vpn peers`。

### T8：入向管线与 UDP 中继（tagged）

**Step 1（红灯）**：`vpn_packet_test.go` 用假 peer 表 + 假 metrics + 假 audit 覆盖 D9 的每一个分支，
断言丢弃时计数的 class 恰好是 `peer_unknown`/`peer_revoked`/`peer_expired`/`rate_limited`/`target_denied`/
`fragment_dropped`/`oversize_dropped`/`protocol_unsupported`/`metadata_denied`/`port_denied`，
且合法包被分派到对应协议处理器；非 IPv4 包被丢弃并计 `protocol_unsupported`。
`vpn_udp_test.go`：一个入向 UDP 数据报 ⇒ 假 opener 收到一条 `relay.StreamRequest{Protocol:"udp", AgentID:peer.AgentID,
TargetHost:dst, TargetPort:dstPort}`；从假流写回的应答 ⇒ 出向队列出现一个 IPv4/UDP 包，其源=内网服务地址、
目的=peer VPN 地址、端口对调、载荷一致、IP 协议号 17（照 Task 3 的四条断言）；同一四元组的第二个数据报复用同一条流
（opener 只被调用一次）；超过 MTU 的应答被丢弃并计 `oversize_dropped`；流关闭后四元组被回收，
下一个数据报重新开流；per-peer 流上限触发时数据报被丢弃并计 `capacity_exhausted` 且不开流。
Run `go test -tags vpn ./internal/server/ -run 'VPNPacket|VPNUDP' -count=1` → `undefined: (*vpnGateway).handlePacket`。

**Step 2（实现）**：`vpn_packet.go`（管线 + 分派 + 计数 + 拒绝聚合入口）、`vpn_udp.go`
（`udpFlowTable` 复用 T6 流表、`openVPNEgress`、回程包构造 `buildIPv4UDP`、`WritePackets`）。

**Step 3（绿灯 + 提交）**：`go test -tags vpn ./internal/server/ -run 'VPNPacket|VPNUDP' -count=1 -race` 绿。
提交 `feat(server): relay vpn udp datagrams`。

### T9：ICMP echo 中继（tagged）

**Step 1（红灯）**：`vpn_icmp_test.go`：echo request ⇒ 假 opener 收到
`relay.StreamRequest{Protocol:"icmp-echo", TargetHost:dst, TargetPort:0}` 且写入的数据报能被
`protocol.DecodeICMPEchoRequest` 解出、`CorrelationID` 非空、`Identifier`/`Sequence` 与入向包一致；
写回一个 `status:"ok"` 的 `ICMPEchoReply` ⇒ 出向队列出现 ICMP echo reply（type 0、code 0、id/seq 与请求一致、
载荷一致、校验和正确、源=目标内网地址、目的=peer VPN 地址）；`status:"timeout"` ⇒ 计 `icmp_timeout` 且不出向；
`status:"unsupported"` ⇒ `icmp_unsupported`；`status:"capacity_exhausted"` ⇒ `capacity_exhausted`；
`status:"unreachable"` ⇒ `egress_unavailable`；`status:"cancelled"` ⇒ `stack_error`；
probe 返回 `CapabilityUnsupported` ⇒ 不开流且计 `icmp_unsupported`；`CapabilityUnverified` ⇒ 开流；
节点 `icmp_enabled=false` 或 peer `icmp_enabled=false` ⇒ 由 T8 的策略链拦下（此处断言不会走到开流）；
在途数达到 `icmp_max_concurrent` ⇒ 计 `capacity_exhausted` 且不开流；超过 `icmp_timeout` 未回应答 ⇒ 计 `icmp_timeout`
并释放在途名额；ICMP echo 之外的类型（TTL exceeded）⇒ 计 `protocol_unsupported` 且不开流。
`internal/observability/metrics_test.go`：`NormalizeProtocol("icmp-echo") == "icmp-echo"`。
Run → `undefined: (*vpnGateway).handleICMPEcho`。

**Step 2（实现）**：`vpn_icmp.go`（关联 ID 分配、在途信号量、超时 `time.AfterFunc`、reply 构造 `buildIPv4ICMPEchoReply`）；
`internal/observability/metrics.go` 的 `NormalizeProtocol` 白名单加 `icmp-echo`。

**Step 3（绿灯 + 提交）**：`go test -tags vpn ./internal/server/ -run VPNICMP -count=1 -race` 与
`go test ./internal/observability/ -count=1` 绿。提交 `feat(server): relay vpn icmp echo`。

### T10：TCP 中继（tagged）

**Step 1（红灯）**：`vpn_tcp_test.go`：注入完整三次握手（SYN → 读 SYN-ACK → ACK）后
假 opener 收到 `relay.StreamRequest{Protocol:"tcp", TargetHost:dst, TargetPort:dstPort}`；
从 accepted conn 写入的字节出现在假流里、假流写回的字节出现在出向队列的 TCP 段载荷里；
半关闭（对端 FIN）⇒ 假流被 `Close` 且流表条目移除、监听器 `refs` 递减；假流 EOF ⇒ conn 被关闭；
`connect_timeout` 超时 ⇒ conn 被关闭且计 `egress_timeout`；opener 返回错误 ⇒ 计 `egress_unavailable`；
peer 在 accept 之后被吊销 ⇒ 该 conn 被关闭（`RemovePeer` 的 `onRemove` 回调生效）；
per-peer 流上限 ⇒ 新连接立刻被关闭且计 `capacity_exhausted`；`idle_timeout` 内无字节 ⇒ 连接被回收；
两个 peer 连同一个 (dst,port) ⇒ 只有一个监听器、两条流、各自的 `RemoteAddr` 正确映射回各自 peer。
Run → `undefined: (*vpnGateway).serveTCPConn`。

**Step 2（实现）**：`vpn_tcp.go`：accept 泵 goroutine、peer 反查、上限检查、`openVPNEgress`、
复用 `spliceWithIdleTimeout`（`internal/server/proxy_entry.go:799`，untagged，同包可直接调用）、
字节计数与 `ObserveBytes`/`ObserveStream`、监听器引用计数释放。

**Step 3（绿灯 + 提交）**：`go test -tags vpn ./internal/server/ -run VPNTCP -count=1 -race` 绿。
提交 `feat(server): relay vpn tcp streams`。

### T11：WireGuard 端点与自定义 Bind（tagged）

**Step 1（红灯）**：`vpn_bind_test.go`：`newVPNBind("127.0.0.1:0")` 后 `Open(0)` 返回实际端口且
`net.ListenUDP` 到该端口会失败（证明端口真的被占用、绑定在指定 host 上）；`Send` 一个数据报能被对端 UDP socket 收到；
`ParseEndpoint("127.0.0.1:1234")` 的 `DstToString`/`DstIP` 正确、非法字符串返回错误；`BatchSize()==1`；
`SetMark(1)` 返回不支持错误；`Close` 后 `ReceiveFunc` 返回 `net.ErrClosed`。
`vpn_gateway_test.go` 的第一段（本任务只写握手部分）：两个 `device.NewDevice`（服务端用 `vpnBind` + `memoryDevice` +
`newVPNStack`，客户端用上游 `tun/netstack.CreateNetTUN`）经回环完成真实 Noise 握手，
`IpcGet` 里能看到该 peer 的 handshake 时间戳非零，且服务端 `ApplyPeer` 之后客户端能被路由到（`allowed_ip` 生效）。
Run → `undefined: newVPNBind`。

**Step 2（实现）**：`vpn_bind.go`（`vpnBind`、`vpnEndpoint`、`ReceiveFunc`）；
`vpn_gateway.go` 补齐：创建 `memoryDevice` + `newVPNStack` + `device.NewDevice`、启动时 `IpcSet` 节点身份与
`listen_port`、`ApplyPeer`/`RemovePeer` 走 `vpn.RenderPeerUAPI`/`RenderRemoveUAPI` + `IpcSet`、
把 peer 表回调接到 UAPI、从 `ListByNode` 全量装载。

**Step 3（绿灯 + 提交）**：`go test -tags vpn ./internal/server/ -run 'VPNBind|VPNGateway' -count=1 -race` 绿。
提交 `feat(server): serve the wireguard endpoint`。

### T12：生命周期与运行时装配（tagged + untagged）

**Step 1（红灯）**：`vpn_lifecycle_test.go`：`Start(ctx)` 后 UDP 端口可被外部 socket 探测到；
`Close()` 在 `shutdown_timeout` 内返回且顺序正确（用记录型桩断言：先拒新流、再关监听器、再关流、
再关 device、最后关 stack；断言方法调用序列而不是靠时序猜测）；drain 期间新流被拒并计 `capacity_exhausted`；
在途流在 `shutdown_timeout` 到点后被强制关闭；`Close` 幂等且不 panic；空闲回收 goroutine 与租约续租 goroutine
在 `Close` 后退出（用 `goleak` 风格的手写断言：等待 done channel 关闭）；
租约续租按 `TTL/3` 调用 `Renew`，返回 `ErrVPNIPLeaseStaleEpoch` 时停止该子网的续租且不关闭在途 peer。
`internal/server/runtime_test.go`（或既有 runtime 测试文件）：`VPN.Enabled=true` 且无 tag ⇒ `NewServerRuntime` 返回
含 `-tags vpn` 的错误；`Enabled=false` ⇒ 不创建任何 VPN 资源（网关为 nil、无 UDP 端口被绑定）。
Run → `undefined: (*vpnGateway).Start`。

**Step 2（实现）**：`vpn_lifecycle.go`（D14 的顺序、D15 的续租）；`runtime.go` 装配（`startVPNGateway` →
`runtime.vpnGateway`、`API.SetVPNDataPlane`、`ServeListener` 的 shutdown 链里 `Close`、`ServerRuntime.Close` 里兜底）。

**Step 3（绿灯 + 提交）**：`go test -tags vpn ./internal/server/ -run 'VPNLifecycle|Runtime' -count=1 -race` 与
默认构建同范围测试绿。提交 `feat(server): run the vpn gateway lifecycle`。

### T13：管理 API 与控制台

**Step 1（红灯）**：`vpn_peer_api_test.go` 新增：装了假 `VPNDataPlane` 后 `GET /flows` 返回 200 且 `items`
逐字段等于快照；网关为 nil 时仍 501 `vpn_not_implemented`；peer 属于别的节点时 501；
越权（普通用户读他人 peer 的 flows）⇒ 404 而不是 501（先解析 peer 再问网关，避免用状态码探测归属）；
`GET /vpn-nodes` 返回 200 且本节点条目的 `enabled`/`listen`/`endpointHost`/`subnet`/`allocated`/`capacity`/`peers`/
`icmpCapable` 与夹具一致；untagged 构建下 `enabled=false` 时 `icmpCapable=false`。
`vpn_peer_openapi_test.go`：`phases != 0` 断言（当前是 `!= 2`，先改测试让它红）。
`web/src/tests/vpn.spec.ts`：把 `expect(source).not.toContain('listVpnPeerFlows')` 反向为断言 `VpnPeers.vue`
确实用了它并渲染活跃流；删除 `vpn.dataPlanePending` 的断言。
Run → 失败（handler 仍返回 501；前端守卫仍反向）。

**Step 2（实现）**：`vpn_peer_api.go` 的两个 handler；`docs/api/openapi.yaml` 补 `VPNFlowListEnvelope`/
`VPNNodeListEnvelope` schema、移除两个 `x-tunnelmesh-phase`、重写 501 描述；
`web/src/views/VpnPeers.vue` 增活跃流抽屉（照既有使用说明抽屉范式）、去掉常驻提示；
`web/src/i18n/messages/{zh-CN,en-US}.ts` 增删对应键（中英对齐，`vpn.spec.ts` 的 i18n 守卫会检查）。

**Step 3（绿灯 + 提交）**：`go test ./internal/server/ -run VPN -count=1` + `cd web && npm test -- --run && npm run build`
+ `bash scripts/verify-web-embed.sh` 绿。提交 `feat(server): expose vpn flows and node status`。

### T14：端到端矩阵测（tagged）

**Step 1（红灯）**：`vpn_gateway_test.go` 扩展成矩阵：一个真实 WireGuard 客户端（上游 `CreateNetTUN`）
经回环 UDP 连到被测网关，网关的 opener 是一个把字节回显/记录的假 Agent transport。断言规格 §18 的 1-6：
握手成功且 peer 拿到 VPN IP（客户端从自己的 netstack 里 ping 不通网关自身地址 ⇒ `target_denied` 计数 +1）；
TCP：客户端 `DialContextTCPAddrPort` 到内网目标 ⇒ 假 Agent 收到 `Protocol:"tcp"` 与正确目标 ⇒ 双向字节一致 ⇒
首包**没有**被丢弃（断言握手在 200ms 内完成，这是 D4 的正向证据，若回退到异步注册该断言会因 1s RTO 而失败）；
UDP：一个数据报往返，出向包的源/目的/端口/载荷与 Task 3 的四条断言一致；
ICMP：echo request ⇒ 假 Agent 收到 `Protocol:"icmp-echo"` ⇒ 回 `status:"ok"` ⇒ 客户端收到 echo reply；
吊销：`RemovePeer` 后在途 TCP 流被关闭、客户端新的握手不被接受（`IpcGet` 里没有该 peer）；
轮换：旧公钥立即失效（用旧密钥的客户端握手不成功）；
策略：目标不在 `allowed_ips` ⇒ 客户端连不通且 `target_denied` 计数 +1、审计里出现一条聚合的 `vpn_packet_denied`；
云 metadata 地址（169.254.169.254）⇒ `metadata_denied`；不支持的协议（构造 IP 协议号 47 的 GRE 包注入）⇒
`protocol_unsupported`；分片包 ⇒ `fragment_dropped`。
Run → 失败（矩阵里若干断言尚未成立，逐条列出红灯原文）。

**Step 2（实现）**：只修实现，不改断言（除非断言本身与规格冲突，此时在 PR 记录里写明）。

**Step 3（绿灯 + 提交）**：`go test -tags vpn ./internal/server/ -run VPNGateway -count=3 -race` 连续 3 次绿
（照 Task 0 的「连续 3 次」范式，排除端口与调度偶发）。提交 `test(server): prove the vpn data plane end to end`。

### T15：文档、体积实测与 PR 记录

1. 实测二进制增量：`go build -o /tmp/tm-server-notag ./cmd/tunnelmesh-server` 与
   `go build -tags vpn -o /tmp/tm-server-vpn ./cmd/tunnelmesh-server`，记录两个字节数与差值（Task 0 明确要求实测、不得推测）。
2. 新增 `docs/user-guide/vpn.md`（导入配置、各平台步骤、能力边界逐条照规格 §4.3、
   「IP 层没有错误通道，失败只表现为连不通」照规格 §12.2、UDP 校验和不重复校验的取舍、排障）；
   `docs/deployment/vpn-gateway.md`（公网 UDP 放行、通配 vs 指定 host 绑定、`endpoint_host` DNS、
   `TUNNELMESH_VPN_NODE_PRIVATE_KEY` 注入、IP 池与子网规划、`-tags vpn` 构建与体积增量实测值、
   无 tag 构建 + `enabled: true` 的快速失败信息、`CAP_NET_ADMIN` 明确不需要）；
   `docs/operations/vpn.md`（IP 池容量与耗尽、drain 与 5 分钟止损、租约续租与 stale epoch、
   `error_class` 全集与含义、当前指标复用现状与阶段 8 的专用向量、常见故障）。
3. 更新 `docs/operations/configuration.md`（`TUNNELMESH_VPN_NODE_PRIVATE_KEY` 从「本版不被读取」改为已消费，
   补全部 `server.vpn.*` 键的取值范围与默认值）、`docs/operations/config-examples.md`（三份示例同步）、
   `docs/architecture/overview.md`（数据面链路与 ASCII 图）、`docs/user-guide/server-admin.md`（VPN 章节：
   reveal 可用、活跃流可见、节点状态可见、集群限制）、`README.md:15`/`README.zh-CN.md:15` 的组件表补 VPN 端点。
4. `python3 scripts/gen_doc_index.py` 连跑两次幂等；相对链接死链核验；`go test ./scripts/ -count=1`
   （`doc_claims_test.go` 的反转主张守卫必须继续绿）。
5. 写 `docs/pull-requests/2026-09-23-vpn-phase6-server-data-plane.md`，格式照阶段 7 的 PR 记录：
   标题、目标分支、关联记录、摘要、用户影响、API/Schema/配置影响、安全与授权影响、
   测试证据（红灯表 + 未执行表 + 体积实测）、与计划的偏差（D1/D2/D3/D4 对规格与既有记录的更正）、
   顺带发现、发布步骤、回滚步骤、Reviewer 关注点、集成状态。
6. 提交 `docs(vpn): describe the server data plane`，随后跑第 9 节全量门禁，push。

## 8. 任务间接口

- `VPNDataPlane`（untagged，T1 定形，T11/T12 实现）：
  `ApplyPeer(storage.VPNPeer) error`、`RemovePeer(peerID string) error`、
  `PeerFlows(peerID string) ([]VPNFlowSnapshot, bool)`（bool = 本节点是否承载该 peer，D13）、
  `NodeStatus(ctx) (VPNNodeStatus, error)`、`Start(ctx) error`、`Close() error`。
- `VPNPeerSink`（untagged，T1 定形，T7 消费）：`ApplyPeer(storage.VPNPeer) error`、`RemovePeer(peerID string) error`；
  `VPNDataPlane` 是它的超集，网关同时充当两者。
- `VPNFlowSnapshot`：`ID string`、`Protocol string`、`Target string`、`Port int`、`StartedAt time.Time`、
  `BytesSent int64`、`BytesReceived int64`；JSON 键与 `web/src/api/vpn.ts` 的 `VpnFlow` 一致。
- `VPNNodeStatus`：`NodeID`、`Enabled`、`Listen`、`EndpointHost`、`Subnet`、`Allocated`、`Capacity`、`Peers`、
  `ICMPCapable`；JSON 键与 `VpnNodeStatus` 一致。
- `VPNGatewayDeps`（untagged）：`Config config.VPNConfig`、`Node vpn.NodeIdentity`、`NodeID string`、
  `Opener relay.NodeTransport`、`Peers storage.VPNPeerRepository`、`Leases storage.VPNIPLeaseRepository`、
  `Audits storage.AuditRepository`、`Metrics vpnMetrics`、`Capabilities VPNAgentCapabilityProbe`、`Now func() time.Time`。
- `vpnMetrics`（untagged 小接口，D12）：`Bytes(direction, protocol string, n int64)`、
  `Stream(protocol, result, errorClass string)`、`PacketDropped(direction, errorClass string)`、
  `Lease(result, errorClass string)`；`observabilityVPNMetrics` 是它到 `*observability.Metrics` 的适配器。
- `vpn.NodeIdentity`（T2）：`PrivateKey string`、`PublicKey string`；`vpn.NodeIdentityFromEnvironment() (NodeIdentity, error)`。
- `vpn.RenderNodeUAPI(NodeIdentity, listenPort int) (string, error)`、
  `vpn.RenderPeerUAPI(publicKey string, vpnIP net.IP) (string, error)`、
  `vpn.RenderPeerRemoveUAPI(publicKey string) (string, error)`（T2 定形，T11 消费）。
- `parseVPNWirePacket([]byte) (vpnWirePacket, error)`（T5 定形，T8/T9/T10 消费）。
- `newVPNFlowTable(cfg) *vpnFlowTable`、`newPacketBucket(rate int, now func() time.Time) *packetBucket`、
  `newDenialAggregator(audits, window, maxKeys) *denialAggregator`（T6 定形，T8-T12 消费）。
- `openVPNEgress(ctx, opener, request, timeout) (io.ReadWriteCloser, context.CancelFunc, error)`（T6 定形，
  T9/T10 与 T8 的 UDP 共用；语义照 `proxy_entry.go:568`，含迟到流回收）。
- `newVPNPeerTable(hooks) *vpnPeerTable`（T7 定形，T8-T12 消费）。
- `newMemoryDevice(mtu, queueSize int) (*memoryDevice, error)`（T3）、`newVPNStack(mtu int) (*vpnStack, error)`（T4）、
  `newVPNBind(listen string) (*vpnBind, error)`（T11）：三者由 T11 的 `vpnGateway` 组装。
- 阶段 7 交付、本阶段直接消费：`protocol.StreamProtocolICMPEcho`、`protocol.EncodeICMPEchoRequest`、
  `protocol.DecodeICMPEchoReply`、`server.VPNAgentCapabilityProbe`、`server.streamProtocolCapability`、
  `server.CapabilitySupported/Unsupported/Unverified`。

## 9. 整体验证

```bash
# 默认构建
go build ./...
go test ./... -count=1
go test -race ./... -timeout 30m -count=1
go vet ./...
# 带 tag（规格 §14 的门禁命令）
go build -tags vpn ./...
go test -tags vpn ./... -count=1
go test -tags vpn -race ./... -timeout 30m -count=1
go vet -tags vpn ./...
# 隔离与格式
go list -deps ./cmd/tunnelmesh-server | grep -Ec 'gvisor|golang.zx2c4.com/wireguard'   # 必须为 0
go list -deps -tags vpn ./cmd/tunnelmesh-server | grep -Ec 'gvisor'                    # 必须 > 0
gofmt -l internal scripts
git diff --check
go test ./scripts/ -count=1
# 依赖面只允许出现计划里的新增行
git diff --stat codex/vpn-phase7-agent-icmp..HEAD -- go.mod go.sum
git status --porcelain migrations web/package.json web/package-lock.json Dockerfile    # 必须为空
# 前端
cd web && npm test -- --run && npm run build && cd .. && bash scripts/verify-web-embed.sh
# 文档索引与死链
python3 scripts/gen_doc_index.py && python3 scripts/gen_doc_index.py && git status --porcelain docs
# 二进制体积实测（写进 PR 记录）
go build -o /tmp/tm-server-notag ./cmd/tunnelmesh-server && stat -f%z /tmp/tm-server-notag
go build -tags vpn -o /tmp/tm-server-vpn ./cmd/tunnelmesh-server && stat -f%z /tmp/tm-server-vpn
```

本阶段没有 Schema 变更，双方言迁移测不适用。Docker/Compose 验证不适用（`Dockerfile` 与 compose 文件零改动，
见 D1）；容器内的非 root 运行与「无 `CAP_NET_ADMIN`」主张仍受 Task 0 第 8 项的 Linux 取证门禁约束，
本机（macOS）无法取证，PR 记录里必须写成「未在 Linux 取证 + 原因」，不得写成已通过。
真实 `wg` 客户端的端到端验证属阶段 8 的 `test/e2e/vpn/`。

## 10. 回滚注意事项

- 回滚顺序：T15 → T14 → T13 → T12 → T11 → T10 → T9 → T8 → T7 → T6 → T5 → T4 → T3 → T2 → T1。
  T1 必须最后回滚：`go.mod` 的两个依赖被 T3/T4/T11 的 tagged 文件引用，只回滚 T1 会让 `-tags vpn` 构建失败
  （默认构建不受影响，因为 tagged 文件被排除）。
- T2 改了 `SetVPN` 的签名与 `VPNPeerServiceDeps.NodePublicKey` 的来源；回滚 T2 会让 `config:reveal` 回到恒 409
  `vpn_node_disabled`，已签发的 peer 行不受影响（密文与公钥都在库里，只是渲染不出 ini）。
- T13 移除了 OpenAPI 的两个 `x-tunnelmesh-phase` 标记并把守卫测试的期望值从 2 改成 0；
  回滚 T13 必须同时回滚这两处，否则守卫测试红。
- **不回滚代码也能止损**：`server.vpn.enabled: false` + 重启即可让进程不创建任何 VPN 资源（ADR 0002 的 5 分钟止损路径）；
  默认构建（不带 `-tags vpn`）本身就不含数据面，是最强的止损。
- 数据面无持久化状态：流表、监听器注册表、peer 内存视图、拒绝聚合器全在内存，回滚不需要数据补偿、不需要迁移、
  不需要清理。数据库里的 `vpn_peers`/`vpn_ip_leases` 行在回滚后仍可被阶段 4/5 的管理 API 读写。
- 回滚后 UDP 端口不再被持有，云安全组里为该端口开的放行规则可以保留（无监听者时只是丢包），
  但运维文档要说明这一点，避免「端口开着但没人听」被误判为故障。
- 与阶段 7 的关系：本阶段是 `icmp-echo` 的第一个调用方。回滚本阶段后 Agent 的 ICMP 引擎闲置但无害
  （能力仍通告，Server 不再开流），不需要同时回滚阶段 7。
- 与阶段 8 的关系：本阶段只复用现有指标向量。回滚本阶段会让 `streams_total{protocol="icmp-echo"}` 这条序列
  停止增长（`NormalizeProtocol` 的白名单新增项可以保留，它不产生数据）；阶段 8 的专用向量不受影响。
