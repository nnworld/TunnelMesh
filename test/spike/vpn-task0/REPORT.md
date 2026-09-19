# VPN 网关 Task 0 可行性报告

- 日期：2026-09-19
- 执行环境：darwin/arm64，macOS 14.6（Darwin 23.6.0），非容器，`id -u` = 501（非 root），
  `/dev/net/tun` 不存在；capability 集合在 macOS 上不可测量（Linux 专有）
- 规格：`docs/superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md` 第 2.2 节
- 计划：`docs/superpowers/plans/2026-09-19-vpn-task0-feasibility.md`
- 修订：2026-09-19 最终评审后就地校正实测证据（grep 行号、探针输出文本、模块体积），
  评审发现与逐条修正记录见计划文末「补记计划」第二轮

## 结论

**Server 侧设计的全部技术前提成立（Task 1-4 全 PASS），Agent 侧的 ICMP 前提（Task 5）因本环境无
Linux 主机而未执行。** 因此本次结论是**有条件通过**：可以进入阶段 1，但 Task 5 必须在一台 Linux
主机上补跑并回填本报告，才能开工阶段 7（Agent ICMP）。详见文末"决策门禁"。

三项已验证的核心主张：

1. gVisor netstack 能**终结**目标为任意内网 IP:端口的 TCP 连接并交出 `net.Conn`，
   交出时自带真实的目标地址与用户 VPN 地址（`local=198.51.100.7:8080 remote=192.0.2.10:40000`）。
2. UDP 无需 netstack 终结：transport handler 返回 `true` 即可消费任意目标数据报，
   回包经 `WritePackets` 从链路出向队列发出，无需 socket、路由或地址配置。
3. WireGuard 可完全运行在内存态 `tun.Device` 上：两个进程内设备完成真实 Noise 握手并承载一条
   TCP 连接，全程无 `/dev/net/tun`、非 root。

## 钉死版本

| 模块 | 版本 | 备注 |
| --- | --- | --- |
| gvisor.dev/gvisor | v0.0.0-20250503011706-39ed1f5ac29c | wireguard-go 自身 pin 的版本；`@latest`（v0.0.0-20260919054224-26f3455a4cb9）不可 `go build`，原因见下 |
| golang.zx2c4.com/wireguard | v0.0.0-20260522210424-ecfc5a8d5446 | 自带 `tun/netstack` 内存 TUN |
| golang.org/x/crypto | v0.57.0 | curve25519，替代不存在的 `wgctrl/wgtypes` |
| golang.org/x/net | v0.59.0 | `icmp` 包 |
| go 指令 | 1.26.3 | 主模块当前为 1.26.0，引入 gVisor 后需抬升；实际工具链为 go1.27.1 |

## Task 1：gVisor 可导入性

`@latest` 的实际报错（撰写计划时实测）：

    found packages stack (addressable_endpoint_state.go) and bridge (bridge_test.go)

根因：该 revision 的 `pkg/tcpip/stack/bridge_test.go` 声明 `package bridge_test`，同目录其余文件为
`package stack`，合法外部测试包名只能是 `stack_test`，且该文件无 `//go:build` 约束。gVisor 上游用
Bazel 构建，不保证每个 pseudo-version 可被 `go build` 导入。被钉死的
`v0.0.0-20250503011706-39ed1f5ac29c` 该目录下只有 `bridge.go`/`bridge_mutex.go`，无测试文件。
选用它的额外理由：它正是 `golang.zx2c4.com/wireguard` 自身 `go.mod` 中 pin 的版本，
wireguard-go 与 gVisor 的兼容性由上游保证。

`go run ./deps` 与 `go test ./deps -count=1` 的输出：

    PASS: gvisor + wireguard-go + x/net/icmp importable by the go tool
          go: go1.27.1 os: darwin arch: arm64
          uid: 501 /dev/net/tun absent: true
    exit=0

    ok  	github.com/tunnelmesh/tunnelmesh/test/spike/vpn-task0/deps	0.407s

`deps` 只有一个测试 `TestPinnedPackagesAreImportable`（PASS）。计划中的
`TestGoVersionIsHighEnoughForGVisor` 在评审后删除：它断言的是本机工具链版本，而不是本 Task 的
任何设计主张；`go.mod` 的 `go 1.26.3` 指令已由工具链自身强制（低于该版本直接拒绝构建），
该测试属于重复门禁，留着只会在换机器时给出误导性红灯。
主模块 `go.mod`/`go.sum` 全程无改动，根目录 `go build ./...` 与 `go vet ./...` 通过。

## Task 2：netstack 任意目标 TCP 终结

命令：`go run ./tcpintercept`（连续 7 次运行输出逐字节一致）

    OK: stack answered SYN-ACK for unowned dst 198.51.100.7:8080 from 198.51.100.7:8080
    PASS: netstack terminated arbitrary-destination TCP after 1 handler call(s); local=198.51.100.7:8080 remote=192.0.2.10:40000
    exit=0

评审修正：`OK` 行现在先校验 SYN 标志（`flags&header.TCPFlagSyn == 0` 直接 FAIL）。RST-ACK 同样
携带 `ack=clientISN+1` 且四元组相同，修正前该行有可能把一个未经核验的包称作 SYN-ACK。
修正后复跑输出逐字未变（`exit=0`），即原结论不受影响。

三个必要条件各自的实测证据（缺任一条都失败，均为撰写计划时实测）：

| 条件 | 缺失时的实测症状 |
| --- | --- |
| `SetPromiscuousMode(nicID, true)` | IPv4 层计入 `InvalidDestinationAddressesReceived` 后丢包，transport handler 一次都不触发（`handlerCalls=0`） |
| SYN 时 `AddProtocolAddress(dst/32)` | 监听器 bind 成功但栈选不出 SYN-ACK 源地址，**静默不发任何包**：`ip.out sent=0`、`outErrs=0`，极难排查 |
| `gonet.ListenTCP` 精确 bind 到该地址 | 直接 bind 未拥有的地址报 `bind tcp 198.51.100.7:8080: bad local address`；通配 bind（`0.0.0.0:port`）可 bind 成功但因缺上一条依然发不出 SYN-ACK，且同端口所有目标 IP 会共用一个监听器 |

"handler 只触发一次即成功"的说明：`stack/nic.go` 的 `DeliverTransportPacket` 先走
`demux.deliverPacket`（nic.go:879），命中即返回；只有未命中才调 `defaultHandler`（nic.go:884）。
因此 `1 handler call` 表示监听器注册成功后，该连接后续报文全部由栈自身 demux 消费——
**这是设计成立的证据，不是丢包**。

## Task 3：netstack UDP 中继与回程

命令：`go run ./udpintercept`（连续 3 次运行输出一致）

    PASS: netstack consumed UDP 192.0.2.10:40000 -> 198.51.100.7:53 len=20 and emitted the reply (38 bytes, IP proto 17) towards the peer; nothing else egressed in the following 300ms, so no ICMP port-unreachable was generated
          transport handler invoked 1 time(s); no socket, route or address setup needed
    exit=0

回程包四条断言的实际值（证明包是朝用户方向发出，而非又一次入向投递）：

| 断言 | 实测值 |
| --- | --- |
| `ip.SourceAddress()` | `198.51.100.7`（内网服务地址，即回程源） |
| `ip.DestinationAddress()` | `192.0.2.10`（用户 VPN 地址） |
| 端口 | `53 -> 40000`，与入向 `40000 -> 53` 对调 |
| 载荷 | `dns-answer` |
| 附加断言 | `ip.TransportProtocol()` = 17（UDP），即栈**没有**回 ICMP port-unreachable |

回程走 `ep.WritePackets(list)`，包落到链路端点出向队列，也就是 wireguard-go 从内存 TUN `Read`
时取包的同一个队列。**`InjectInbound` 不能当回包用**：它会被当作入向报文处理，出向队列始终为空
而超时失败——这是刻意的防回归设计。

评审修正（两处，均已复跑验证）：

1. "无 ICMP port-unreachable"原先的论据是"出向队列按序排空，所以 ICMP 若存在就会是这一包"。
   该推理不成立：回包由 handler 里的 goroutine 写出，与假想 ICMP 的先后无序。现改为
   `drainFor(ep, 300ms)` **正向证明**回包之后再无任何包出向；若出现 IP proto 1 直接 FAIL。
   这就是输出行改写为 "nothing else egressed in the following 300ms" 的原因。
2. `egressOf` 的超时（2s）现在严格小于外层 `time.After`（3s），且超时关闭 channel 会向 `select`
   投递一个 nil slice——此前该 nil 会直接进入 `header.IPv4(raw)` 触发 panic，把预期的诊断信息换成
   goroutine dump。现已显式判空并走同一条 FAIL 分支。

回归证据（在 `mktemp -d` 副本中改代码复跑，**未入库**）：把 `emitReply` 的 `WritePackets` 换成
`InjectInbound` 后，探针输出

    FAIL: no reply egressed; handlerCalls=339019 captured=[192.0.2.10:40000 -> 198.51.100.7:53 len=20 198.51.100.7:53 -> 192.0.2.10:40000 len=10 192.0.2.10:40000 -> 198.51.100.7:53 len=10] ... (339019 entries total)
    exit status 1

即 exit=1、0 次 panic、诊断输出被截断为可读长度（`summarizeCaptured()` 最多打印 3 条 + 总数）。
回包被重新当作入向投递后 handler 无限自激（339019 次），正是本探针要防的回归；
这也说明"用 `InjectInbound` 发回程"的错误写法**不会**静默通过。

## Task 4：wireguard-go 内存态 tun.Device

命令：`go vet ./wgbridge && go run ./wgbridge`（连续 3 次运行均 PASS，端口每次不同属预期）

    OK: wireguard-go accepted an in-memory tun.Device; server bound to UDP :49745 (wildcard), client endpoint 127.0.0.1:49745
    PASS: WireGuard handshake + TCP round trip over memory TUN; server saw 10.64.0.2:23730 -> "hello-over-wireguard-memory-tun"
          env: darwin/arm64 uid=501, /dev/net/tun absent=true, CAP_NET_ADMIN=n/a (only Linux exposes a capability mask)
    exit=0

这一条输出同时证明四件事：wireguard-go 接受了内存 TUN（`tun.Device` 断言 + 三次 `IpcSet` 全部成功）；
Noise 握手在真实 UDP socket 上完成；TCP 载荷端到端到达且字节一致；运行环境非 root 且无 `/dev/net/tun`。

评审修正：原 `OK` 行写的是 "listening on UDP 127.0.0.1:port"，**这是错的**。
`conn.NewDefaultBind()` 走 `ListenPacket(ctx, network, ":"+port)`，即绑定**通配地址**（所有接口），
`127.0.0.1` 只是告知客户端去拨的 endpoint。阶段 1 的部署文档必须据此写明：VPN 数据面持有的是
一个**通配 UDP 端口**，对外暴露面与防火墙/安全组规则由部署方负责，不能默认它只在回环上。

`/dev/net/tun` 与 capability 状态（`grep -n 'CreateTUN\|os.OpenFile\|/dev/net' wgbridge/main.go`，
评审后复跑的真实输出）：

    1:// Probe: can a WireGuard tunnel run entirely in memory, with NO /dev/net/tun
    43:	// tun.Device.File() returns nil and nothing ever opens /dev/net/tun.
    137:		fmt.Printf("      env: %s/%s uid=%d, /dev/net/tun absent=%v, CAP_NET_ADMIN=%s\n",
    138:			runtime.GOOS, runtime.GOARCH, os.Getuid(), !fileExists("/dev/net/tun"), capNetAdmin())

**命中数与计划 Expected 一致（4 处）**；行号从计划的 1/41/131/132 下移为 1/43/137/138，
原因是补记修正为 `capNetAdmin()` 增加了 import 与注释、`OK` 行改写又增加了解释性注释，
探针语义未变。报告初版直接照抄了计划 Expected 的行号而未复跑 grep，已在评审后修正。
其中 1/43 是注释，137 是输出格式串，138 是用于断言设备不存在的 `fileExists` 调用；`CreateTUN` 与
`os.OpenFile` 均 0 命中，即没有任何打开设备节点的代码。密钥全部在运行时由 `crypto/rand` 生成，
仓库内无任何密钥材料。

`capNetAdmin()` 在 Linux 上读 `/proc/self/status` 的 `CapEff` 并测试第 12 位，在非 Linux 上如实
输出 `n/a`——**不得**把它写成已测量的事实。本次为 darwin，故 capability 集合未被测量。

`CreateNetTUN` 不可直接复用的原因：它不调用 `SetPromiscuousMode`，也不暴露 `*stack.Stack` 与
`*channel.Endpoint`，因此无法安装拦截钩子、无法 `AddProtocolAddress`、无法用 `WritePackets` 发
UDP 回程。本探针的 PASS **不覆盖**任意目标拦截能力，那由 Task 2 单独证明。

**Linux 复跑（计划 Task 4 Step 4）未执行**：本环境无 Linux 主机，docker/podman/colima/lima/
nerdctl/vagrant/qemu/lxc 均不存在。

## Task 5：非特权 ICMP datagram socket（Linux）

**未在 Linux 执行。** 本环境为 darwin/arm64，无任何 Linux 主机或容器运行时；探针在 darwin 上按
设计输出 `SKIP: must run on Linux; result on darwin is void` 并以退出码 0 结束，**该 SKIP 不构成
PASS**。未触碰任何宿主机 sysctl，未使用 sudo。

已完成的代码级验证：

- `go vet ./icmpsock` 与 `GOOS=linux go vet ./icmpsock` 均无输出（干净）
- `GOOS=linux` 下 amd64 与 arm64 交叉编译均通过
- `gofmt -l` 干净；与计划代码块 `diff` 为空（逐字节一致）

待 Linux 操作者执行的命令（原样摘自计划 Task 5 Step 3-4）：

    # 未设置 sysctl 时，预期 FAIL 且 exit=1，证明该能力确实受 sysctl 门控
    sysctl net.ipv4.ping_group_range
    cd test/spike/vpn-task0 && go run ./icmpsock; echo "exit=$?"

    # 一次性设置后重跑，预期 PASS 且 exit=0；记录 id 对比行
    sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
    cd test/spike/vpn-task0 && go run ./icmpsock; echo "exit=$?"

    # 共享主机上验证完毕后恢复原值，并在本报告记录已恢复
    sudo sysctl -w net.ipv4.ping_group_range="1 0"

未测得的关键事实：内核是否改写 ICMP id（spec §6.2 "关联不使用内核分配的 ICMP id" 的实证支持）。
该设计即使 id 未被改写也依然成立（用 stream 侧关联 ID 更稳健），但结论必须回填实测值。

本报告 Task 5 各字段应填：未设置 sysctl 输出 = 未执行；设置后输出 = 未执行；
id 是否被改写 = 未测得；sysctl 是否已恢复 = 不适用（未修改）；结论 = **未在 Linux 执行**。

## Task 6：依赖影响

| 指标 | 值 |
| --- | --- |
| spike 模块依赖条目数（`go list -m all \| wc -l`） | 96 |
| TCP 探针包数（`go list -deps ./tcpintercept`） | 169 |
| UDP 探针包数（`go list -deps ./udpintercept`） | 162 |
| WireGuard 探针包数（`go list -deps ./wgbridge`） | 203 |
| gVisor 模块体积（module cache，被钉死的 revision） | 21M |
| TCP 探针二进制体积 | 6,454,034 B（约 6.2 MB） |
| 当前 tunnelmesh-server 二进制体积 | 39,442,898 B（约 37.6 MB） |

判读：探针二进制 6.2 MB 是"gVisor + wireguard-go 全量链接"的规模参考，**不等于**把它们编进
`tunnelmesh-server` 的边际增量（两者共享大量 stdlib，且 server 已含 Vue 后台 embed 产物）。
真实的二进制增量必须在阶段 3 引入 `//go:build vpn` 后，用带与不带该 tag 的两次构建实测，
本报告不作推测。module cache 体积只影响开发机与 CI 缓存，不进入发布产物；被钉死的 revision 占 21M。
（评审修正：初版写的 102M 是本机 `gvisor.dev/` 目录合计值，其中 81M 是撰写计划时实验过、已被否决的
`@latest`（v0.0.0-20260919054224-26f3455a4cb9）留下的本地残留，与本方案无关，也不随仓库分发。）
这组数字支持 spec §5.4 的"build tag 隔离 + 单独 release 变体"决策：默认产物不应承担该增量。

## 必须回流到阶段 1 的设计修正

下列各项须在 ADR 0002 与活文档改写中落地。1-6 出自计划正文，7-9 是本次执行新增的实测发现。

1. **`vpn_device.go` 不能直接复用 `CreateNetTUN`。** 它不开 promiscuous，也不暴露
   `*stack.Stack` 与 `*channel.Endpoint`。需以 `tun/netstack/tun.go` 的 `netTun`（约 190 行）
   为蓝本自行实现，并额外暴露 stack 与 endpoint。
2. **任意目标 TCP 需要按连接目标动态 `AddProtocolAddress(dst/32)`，且必须给出地址回收策略。**
   本次探针的 `listen()` 只加不删，长跑网关会无界增长。建议：按目标地址引用计数，最后一个流
   关闭时 `RemoveAddress`；或设 LRU 上限并在超限时拒绝新目标。
3. **UDP 回程路径是 `ep.WritePackets`，不是 `InjectInbound`。** spec §5.2 的 UDP 分支需据此改写。
   另需注意 `WritePackets` **不做 MTU 校验**：实测把 2028 字节的包写到 MTU=1420 的链路端点，
   返回 `n=1 err=<nil>` 并原样出队（`link/channel/channel.go:278-291` 只入队，唯一错误路径是缓冲区
   不足）。spec 已规定超限 UDP 直接丢弃，网关必须自己执行该限制，不能指望链路层兜底。
4. **注入 netstack 的原始包必须放进 `PacketBufferOptions.Payload`**，不能 `NetworkHeader().Push()`：
   `parse.IPv4` 自己从 `Data()` 里 `Consume` 出网络头，直接 Push 会让 `Data()` 为空并被判畸形
   （实测 `ip.malformed=50`）。`netTun.Write` 的写法是正确的。
5. **钉死版本的 `tcpip.Error` 不实现 `error`**（只是 `fmt.Stringer`），错误处理封装签名需据此设计。
6. **`SetTransportProtocolHandler` 只在 demux 未命中时被调用**（`stack/nic.go:879/884`），
   流表与钩子语义的描述需据此修正。
7. **每个新四元组的首个 SYN 必然被丢弃**，因为钩子是在 demux 未命中时才触发、监听器注册是异步的。
   生产建连延迟因此包含一次客户端 RTO（Linux 初始 RTO 为 1s）。`vpn_flows.go` 必须预算或规避：
   可选方案是为已发布服务预热监听器，或在钩子内同步完成 `AddProtocolAddress` + `ListenTCP`
   后仍返回 `false` 并接受首包丢失。此项影响用户可感知的首连延迟，须在阶段 4 设计中明确取舍。

   同一 (addr,port) 的**并发**注册语义已实测（32 个 goroutine 抢一个全新 tuple，连续 3 次结果
   完全一致）：`AddProtocolAddress` 恰好 1 个成功、31 个返回 `*tcpip.ErrDuplicateAddress`；
   `ListenTCP` 恰好 1 个成功、31 个返回 `bind tcp 198.51.100.9:9090: port is in use`。
   因此阶段 4 的钩子实现必须按 (addr,port) 做 singleflight/去重，并把这两个错误当作
   "别的流已经建好了"**成功**返回；失败方绝不能去 `RemoveAddress` 或关闭监听器，
   否则会拆掉赢家的地址与监听器，造成正在服务的流被中断。
   （`tcpip.Error` 不实现 `error`，判别只能用类型断言 `terr.(*tcpip.ErrDuplicateAddress)`，
   `errors.As` 无法编译——与上文第 5 项一致。）
8. **Server 侧的特权主张尚未在 Linux 上取证。** 在 macOS 上"`/dev/net/tun` 不存在"是平凡为真的，
   `CAP_NET_ADMIN` 也无法测量。必须在非 root、`--cap-drop=ALL` 的 Linux 容器中复跑 Task 2-4，
   并把 `grep Cap /proc/self/status` 的实际值回填本报告，才能声称特权主张已取证。
9. **Task 5 待补跑。** 在 Linux 主机上执行上文命令，回填 id 改写实测值与 sysctl 恢复情况。
   阶段 7（Agent ICMP）开工前必须关闭。

## 评审顺带发现的仓库既有问题（不在本分支修复）

以下问题在评审 spike 时被顺带发现，均**早于本分支**且不属于 Task 0 范围，本分支不改，
记录在此以免被误认为已处理：

- **CI 缺 Go 门禁。** `.github/workflows/ci.yml` 的 `build-test` job 只跑前端
  `npm test -- --run` / `npm run build` 与 `./scripts/verify-web-embed.sh`，`setup-go` 之后
  没有任何 `go build` / `go vet` / `go test` 步骤，与 `AGENTS.md` §13「CI 质量门禁：lint +
  覆盖率 + build」不符。本次全部 Go 验证都是在本地手工执行的。
- **`Dockerfile` 的 Go 版本落后。** 第 3 行 `ARG GO_VERSION=1.23`，而 `go.mod` 已是 `go 1.26.0`；
  阶段 1 为满足 gVisor 抬升到 1.26.3 时，必须同步、显式地抬高该 ARG，否则镜像构建会用旧工具链失败。
- **`.dockerignore` 未排除 `test/`。** spike 模块（含 gVisor 依赖）会被带进 Docker 构建上下文，
  徒增上下文体积。
- **根模块路径与远端仓库名不一致。** 模块路径是 `github.com/tunnelmesh/tunnelmesh`，
  远端是 `nnworld/TunnelMesh`。spike 模块正确继承了根路径，本分支不改。

## 决策门禁

- Task 1-4 全 PASS：**Server 侧设计成立**，可进入 spec §15 阶段 1（约束反转 + ADR 0002 + 活文档改写），
  随后阶段 3（Schema v15）、阶段 4-6（Server 侧实现）。
- Task 5 未执行、第 8 项 Linux 取证未完成：**这两项是阶段 7（Agent ICMP）与"特权主张"对外表述的
  开工门禁**，不阻塞阶段 1-6 的 Server 侧工作，但必须在阶段 7 之前关闭并回填本报告。
- 无任何一项 FAIL，因此**不触发** spec §2.2 的回退路径；netstack 方案存活。
- 后续若有人在 spike 模块执行 `go get -u`，`deps` 的回归测试会失败而不是让下游任务莫名崩溃。
