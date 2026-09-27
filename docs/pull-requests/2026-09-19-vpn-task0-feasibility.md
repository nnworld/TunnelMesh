# VPN 网关 Task 0：设计规格与可行性验证 spike

## 标题

`test(spike): prove the embedded VPN gateway's technical premises (Task 0)`

本记录覆盖分支上的全部 12 个提交（规格 → 计划 → Task 1-6 → 报告 → 第一轮补记 → 评审修正 →
第二轮补记），而不只是最后一个提交。

## 目标分支

`main`

## 关联记录

- 设计规格：[内嵌 VPN 网关（WireGuard）设计](../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md)
- 实施计划：[VPN 网关 Task 0 可行性验证 Implementation Plan](../superpowers/plans/2026-09-19-vpn-task0-feasibility.md)
  （含两轮「补记计划」，正文按时点记录保留）
- 可行性报告：`test/spike/vpn-task0/REPORT.md`（spike 目录内，不属于 `docs/` 索引范围）

## 摘要

这是「内嵌 VPN 网关（WireGuard）」特性的 Task 0：**只做设计定稿与技术验证，不含任何产品代码**。
交付三份产物：设计规格（475 行）、实施计划（1600 行）、以及一个刻意与主模块隔离的一次性 spike
模块 `test/spike/vpn-task0/`（5 个探针 + 可行性报告）。

验证目标是规格 §2.2 列出的 5 项「若为假则整个设计作废」的前提。**结论：Task 1-4 全 PASS，
无任何一项 FAIL，因此不触发规格 §2.2 的中止判据，netstack 方案存活，可进入阶段 1；
Task 5（Linux 非特权 ICMP datagram socket）因本环境无 Linux 主机而未执行，记录为「未在 Linux 执行」，
不计为 PASS。**

三项已实测的核心主张（完整证据见 `test/spike/vpn-task0/REPORT.md`）：

1. gVisor netstack 能**终结**目标为任意内网 `IP:端口` 的 TCP 连接并交出 `net.Conn`，
   交出时自带真实目标地址与用户 VPN 地址（`local=198.51.100.7:8080 remote=192.0.2.10:40000`）。
2. UDP 无需 netstack 终结：transport handler 返回 `true` 即可消费任意目标数据报，
   回包经 `WritePackets` 从链路出向队列发出，无需 socket、路由或地址配置。
3. WireGuard 可完全运行在内存态 `tun.Device` 上：两个进程内设备完成真实 Noise 握手并承载一条
   TCP 连接，全程无 `/dev/net/tun`、非 root。

## 用户影响

- **运行时零影响。** 没有任何产品代码、配置项、API、Schema 或前端变更；已部署的 Server / Agent /
  Client 行为完全不变，无需重启、无需迁移。
- 对使用者的唯一影响是**文档**：新增一份尚未实现的设计规格与一份可行性报告。`docs/user-guide/`、
  `docs/deployment/`、`docs/operations/` 均未改动，因此不会出现「文档承诺了但产品没有」的落差。
- 对贡献者的影响：`test/spike/vpn-task0/` 是独立 module（自带 `go.mod`），不被主模块引用，
  不参与根模块 `go build ./...` / `go test ./...`，也不进入发布产物。目录内 README 写明了
  「不要执行 `go get -u`」的原因（会把 gVisor 抬回无法被标准 Go 工具链导入的 revision）。
- 本 PR **不反转** `AGENTS.md` 中「继续不实现：ICMP、TUN/L2 VPN」的约束（`AGENTS.md` 未改动）。
  约束反转是阶段 1 的工作，必须在产品代码落地前由用户确认；本 PR 只提供决策依据。

## API、Schema 与配置影响

- **API**：无。`docs/api/openapi.yaml` 未改动，没有新增或变更任何路径。
- **Schema**：无。`migrations/` 未改动，`SchemaVersion` 仍为 14。规格 §7 的 Schema v15 只是设计草案。
- **配置**：无新增配置项。规格 §8 列出的 VPN 配置键同属草案，本 PR 一个都没有引入。
- **依赖**：**根 `go.mod` / `go.sum` 零改动**（`git status --porcelain go.mod go.sum` 为空）。
  spike 模块自行钉死版本：`gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c`
  （wireguard-go 自身 pin 的版本；`@latest` 的 `pkg/tcpip/stack/bridge_test.go` 声明了非法的
  `package bridge_test`，无法被 `go build` 导入）、`golang.zx2c4.com/wireguard
  v0.0.0-20260522210424-ecfc5a8d5446`、`golang.org/x/crypto v0.57.0`、`golang.org/x/net v0.59.0`，
  `go 1.26.3`。
- **发布产物体积**：不变。探针二进制 6,454,034 B（约 6.2 MB）只是「gVisor + wireguard-go 全量链接」
  的规模参考，**不等于**把它们编进 `tunnelmesh-server` 的边际增量；真实增量必须在阶段 3 引入
  `//go:build vpn` 后用带/不带 tag 的两次构建实测，报告不作推测。当前 `tunnelmesh-server` 为
  39,442,898 B。spike 的 module cache 占用（被钉死 revision 21M）只影响开发机与 CI 缓存。
- **文档索引**：`docs/superpowers/plans/README.md`、`docs/superpowers/specs/README.md`、
  `docs/pull-requests/README.md` 由 `scripts/gen_doc_index.py` 重新生成，未手工编辑。

## 安全与授权影响

- **仓库内无任何密钥材料。** WireGuard 私钥/公钥在探针运行时由 `crypto/rand` 生成，进程退出即消失；
  对分支 diff 做私钥块与凭据模式扫描，0 命中。
- **探针不触碰宿主机特权面。** 不开 `/dev/net/tun`（`CreateTUN` 与 `os.OpenFile` 均 0 命中）、
  不改路由表、不改 sysctl、不使用 sudo；全部逻辑跑在进程内的 gVisor netstack 与内存 TUN 上。
- `icmpsock` 在 darwin 上直接输出 SKIP 并以 0 退出，不发任何 ICMP；只有在 Linux 上、且操作者显式设置
  `net.ipv4.ping_group_range` 后才真正发包。REPORT.md 已写明验证完毕必须恢复原值并回填恢复情况。
- **授权模型未改动**：本 PR 不涉及任何认证、鉴权或审计路径。
- **需要 reviewer 记住的暴露面事实**：`conn.NewDefaultBind()` 走 `ListenPacket(ctx, network, ":"+port)`，
  绑定的是**通配 UDP 端口**而不是回环。阶段 1 的部署文档必须写明 VPN 数据面持有一个通配端口，
  防火墙/安全组由部署方负责。
- **尚未取证的对外主张**：「无需 root、无需 `CAP_NET_ADMIN`」在 macOS 上是平凡为真的
  （`/dev/net/tun` 本就不存在，capability 集合也不可测量）。必须在非 root、`--cap-drop=ALL` 的
  Linux 容器中复跑 Task 2-4 并回填 `grep Cap /proc/self/status` 实测值，才能对外声称已取证。
  在此之前，任何文档都不得把它写成已验证事实——REPORT.md 与计划补记均已按此措辞。
- **生产实现必须承接的两条安全/稳定性语义**（实测得出，见报告回流清单第 3、7 项）：
  `channel.Endpoint.WritePackets` 不做 MTU 校验（2028 字节包写到 MTU=1420 端点返回
  `n=1 err=<nil>` 并原样出队），超限丢弃必须由网关自己执行；同一 (addr,port) 的并发注册恰好
  1 个成功、其余返回 `*tcpip.ErrDuplicateAddress` 与 `bind tcp ...: port is in use`，
  阶段 4 必须做 singleflight 去重并把这两个错误当成功，失败方不得 `RemoveAddress` 或关闭监听器，
  否则会拆掉赢家的地址与监听器、中断在服务的流。

## 测试证据

spike 模块（`cd test/spike/vpn-task0`，评审修正后全部复跑）：

- `go run ./deps` → `PASS: gvisor + wireguard-go + x/net/icmp importable by the go tool`，
  `go1.27.1 darwin/arm64 uid=501`，`/dev/net/tun absent: true`，`exit=0`
- `go test ./deps -count=1` → `ok ... 0.407s`（1 个测试 `TestPinnedPackagesAreImportable`；
  计划中那个断言本机工具链版本的测试已在评审后删除）
- `go run ./tcpintercept` → `PASS`（连续 7 次运行输出逐字节一致）
- `go run ./udpintercept` → `PASS`（连续 3 次一致）
- `go run ./wgbridge` → `PASS`（连续 3 次通过，端口每次不同属预期）
- `go run ./icmpsock` → `SKIP: must run on Linux; result on darwin is void`（**不计为 PASS**）
- `gofmt -l .` 无输出；`go vet ./...` 与 `GOOS=linux go vet ./...` 均干净
- `go list -m all | wc -l` = 96；`go list -deps` 包数 tcpintercept=169 / udpintercept=162 /
  wgbridge=203
- 防回归证明（在 `mktemp -d` 副本中改代码复跑，**未入库**）：把 `emitReply` 的 `WritePackets`
  换成 `InjectInbound`，探针输出 `FAIL: no reply egressed; handlerCalls=339019 ... (339019 entries
  total)`、`exit status 1`、0 次 panic。即「用 `InjectInbound` 发回程」的错误写法不会静默通过。

仓库根：

- `go build ./...` 通过；`go vet ./...` 干净
- `git diff --check` 干净（含 `git diff --check origin/main..HEAD`）
- `python3 scripts/gen_doc_index.py` 幂等，重复执行无 diff
- 分支 diff 的私钥块 / `client_secret` / 口令 / 恢复码模式扫描：0 命中
- 根模块 `go test ./... -count=1` 在 Task 6 报告定稿时执行并全绿；评审修正只改动 spike 模块
  （独立 module，不在根测试范围内）与文档，故本轮未重跑，且用户已明确要求本分支直接提交不走
  `go test`
- 前端未改动，`npm test` / `npm run build` / `verify-web-embed.sh` 不适用

## 发布步骤

1. 合并即可。无迁移、无配置、无二进制变化，不需要重启任何进程，也不需要注入任何环境变量。
2. 不需要通知运维。注意 `.dockerignore` 目前未排除 `test/`，spike 目录会进入 Docker 构建上下文
   （只影响上下文体积，不影响产物）——这是本分支刻意不修的既有问题之一，见 Reviewer 关注点。
3. 阶段 1 开工前必须先关闭 REPORT.md「决策门禁」的两项：非 root Linux 容器复跑 Task 2-4 取证
   capability；Task 5 在 Linux 补跑并回填 ICMP id 改写实测值。
4. 阶段 1 抬升 `go.mod` 的 go 指令到 1.26.3（gVisor 要求）时，必须同步、显式地抬高
   `Dockerfile` 第 3 行的 `ARG GO_VERSION=1.23`，否则镜像构建会用旧工具链失败。

## 回滚步骤

- `git revert` 合并提交即可完全回滚：被删除的只有文档与 spike 目录，没有 Schema、配置或运行时状态，
  因此不需要数据库备份、不需要停机、不需要回退任何二进制，5 分钟内可完成。
- 回滚后执行 `python3 scripts/gen_doc_index.py` 重新生成四份索引，避免索引指向已删除的记录。
- 本 PR 未产生任何数据，回滚不存在数据补偿问题。

## Reviewer 关注点

- `test/spike/vpn-task0/tcpintercept/main.go`：promiscuous + 按目标动态 `AddProtocolAddress(dst/32)`
  + `gonet.ListenTCP` 精确 bind，三件套缺一不可。REPORT.md 的表格逐条列出各自缺失时的实测症状，
  其中「监听器 bind 成功但栈静默不发任何包」在生产里极难排查。
- `test/spike/vpn-task0/udpintercept/main.go`：回程必须走 `WritePackets`；`drainFor(ep, 300ms)`
  是「栈不会回 ICMP port-unreachable」这一主张的唯一正向证据（评审前用的是不成立的队列有序推理）。
- `test/spike/vpn-task0/wgbridge/main.go`：确认没有任何打开设备节点的代码路径；`capNetAdmin()`
  在非 Linux 上如实输出 `n/a`，不得被读成已测量的事实。
- `test/spike/vpn-task0/REPORT.md` 的「必须回流到阶段 1 的设计修正」9 项：这是阶段 1 ADR 0002 与
  规格修订的直接输入。其中第 7 项（每个新四元组的首个 SYN 必被丢弃 → 首连延迟包含一次客户端 RTO）
  影响用户可感知体验，必须在阶段 4 明确取舍。
- 两轮补记的关系：计划正文与第一轮补记按「时点记录不可改写」原样保留，所有修正只在第二轮补记追加；
  凡与第一轮冲突的数字（gVisor module cache 体积、`deps` 测试个数）以第二轮为准。
- 评审顺带发现、**本分支刻意不修**的四个既有问题（详见 REPORT.md 同名小节）：`ci.yml` 在
  `setup-go` 之后没有任何 `go build` / `go vet` / `go test` 步骤（与 `AGENTS.md` §13 的 CI 门禁要求
  不符）；`Dockerfile` 的 `ARG GO_VERSION=1.23` 落后于 `go.mod` 的 `go 1.26.0`；`.dockerignore`
  未排除 `test/`；根模块路径 `github.com/tunnelmesh/tunnelmesh` 与远端 `nnworld/TunnelMesh` 不一致。
- 两项未关闭门禁（不阻塞本 PR，阻塞阶段 7 与「特权主张」对外表述）：Linux 非 root 复跑取证、
  Task 5 补跑。

## 集成状态

设计规格、实施计划、Task 0 全部探针与可行性报告已在 `codex/vpn-task0-feasibility-plan` 上完成，
并通过整分支独立评审（范围 `6d3ddec..9c8515d`，结论 APPROVED WITH MINOR FINDINGS：1 项 Important +
7 项 Minor，逐条复核为真、全部修复并复验）。PR 复审与合并待进行；阶段 1（约束反转 + ADR 0002 +
活文档改写）尚未开工，等待用户确认。
