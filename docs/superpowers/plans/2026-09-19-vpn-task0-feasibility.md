# VPN 网关 Task 0 可行性验证 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用可运行的探针代码验证内嵌 VPN 网关设计的四项技术前提，在写任何生产代码之前确认方案成立或触发回退。

**Architecture:** 一次性 spike 模块 `test/spike/vpn-task0/`（独立 `go.mod`，不被主模块引用），内含五个独立可执行探针，分别验证 gVisor 可导入性、netstack 任意目标 TCP 终结、UDP 数据报中继与回程、wireguard-go 内存态 `tun.Device` 端到端隧道、非特权 ICMP datagram socket。每个探针自带 PASS/FAIL 退出码，汇总进 `REPORT.md`。

**Tech Stack:** Go 1.26.3、`gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c`（netstack）、`golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446`、`golang.org/x/crypto`、`golang.org/x/net/icmp`。

**Spec:** `docs/superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md`（第 2.2 节定义本计划的中止判据与回退路径；第 15 节阶段 2）

## 本计划的前置核实结论（撰写计划时已在 `/tmp` 沙箱实测）

下列结论**已经真实运行验证**，本计划的每个探针代码都是已跑通的版本，执行者的任务是把它落进仓库、复现输出并写入 `REPORT.md`，而不是重新设计：

| 验证项 | 结论 | 关键证据 |
| --- | --- | --- |
| gVisor 可被标准 Go 工具链导入 | **成立**，但必须钉死 `v0.0.0-20250503011706-39ed1f5ac29c`（wireguard-go 自身 pin 的版本）。`@latest`（`v0.0.0-20260919054224-26f3455a4cb9`）不可 `go build`：该 revision 的 `pkg/tcpip/stack/` 含 `bridge_test.go` 且声明 `package bridge_test`，而同目录为 `package stack`；被钉死的旧 revision 该目录下只有 `bridge.go`/`bridge_mutex.go`，无测试文件 | Task 1 |
| netstack 终结任意目标 TCP | **成立**，但 `SetPromiscuousMode` 单独不够。必须三步：promiscuous + SYN 时 `AddProtocolAddress(dst/32)` + `gonet.ListenTCP` 精确 bind。缺第二步则 SYN-ACK 无源地址、静默不发（`ip.out sent=0`）；只做通配 bind 则报 `bad local address` | `PASS: netstack terminated arbitrary-destination TCP ... local=198.51.100.7:8080 remote=192.0.2.10:40000` |
| UDP 中继 | **成立且更简单**：`SetTransportProtocolHandler(udp, …)` 返回 `true` 即可消费任意目标数据报，无需 socket/路由/地址配置；回程用 `ep.WritePackets` 推入链路出向队列（wireguard-go 正是从这里取包）。**spec §5.2 原设想的"用 `InjectInbound` 当回包"是错的**，那会被当作入向包处理 | `PASS: netstack consumed UDP ... and emitted the reply (38 bytes) towards the peer` |
| wireguard-go 内存态 `tun.Device` | **成立**，且上游自带 `golang.zx2c4.com/wireguard/tun/netstack.CreateNetTUN`。两个进程内设备完成真实 Noise 握手并承载 TCP 连接，`/dev/net/tun` 不存在、`uid=501` 非 root、无 `CAP_NET_ADMIN` | `PASS: WireGuard handshake + TCP round trip over memory TUN ... /dev/net/tun absent=true` |
| 非特权 ICMP datagram socket | 探针已编译通过且 `GOOS=linux go vet` 干净，但**必须在 Linux 上运行才有结论**（macOS 无 `ping_group_range`） | Task 5 |

### 必须回流到阶段 1（ADR 0002 / spec 修订）的设计修正

本计划**不修改** spec 与任何活文档（时点记录不可改写）。以下修正必须在阶段 1 的 ADR 0002 与活文档改写中落地：

1. **`vpn_device.go` 不能直接复用 `CreateNetTUN`。** 它不调用 `SetPromiscuousMode`，也不暴露 `*stack.Stack` 与 `*channel.Endpoint`，因此无法安装拦截钩子、无法 `AddProtocolAddress`、无法用 `WritePackets` 发 UDP 回程。网关需要自己的内存 TUN 实现（`CreateNetTUN` 的 `netTun` 约 190 行可直接作为蓝本），并额外暴露 stack 与 endpoint。
2. **任意目标 TCP 需要按连接目标动态 `AddProtocolAddress(dst/32)`。** 这引入地址表增长问题，必须在设计中明确回收策略（引用计数归零即 `RemoveAddress`，或 LRU 上限）。
3. **UDP 回程路径是 `WritePackets`，不是 `InjectInbound`。** spec §5.2 的 UDP 分支需据此改写。
4. **注入 netstack 的原始包必须放进 `PacketBufferOptions.Payload`**，不能 `NetworkHeader().Push()`：`parse.IPv4` 自己从 `Data()` 里 `Consume` 出网络头，直接 Push 会让 `Data()` 为空并被判为 malformed（实测 `ip.malformed=50`）。`netTun.Write` 的写法是正确的。
5. **钉死版本的 `tcpip.Error` 不实现 `error`**（只是 `fmt.Stringer`），生产代码的错误处理封装签名需据此设计。
6. **`SetTransportProtocolHandler` 只在 demux 未命中时被调用**（`stack/nic.go` `DeliverTransportPacket`：先 `demux.deliverPacket`，命中即返回）。因此"每个新四元组只触发一次钩子"是**成功信号**而非丢包，spec 的流表设计应据此描述。

## Global Constraints

- spike 模块使用独立 `go.mod`；**主模块 `go.mod`/`go.sum` 不得出现任何改动**，`go test ./...` 在仓库根不会构建嵌套模块。
- spike 代码是一次性产物，**不得被 `internal/`、`cmd/` 或 `web/` 下任何文件 import**。
- 探针一律用 `go run ./<pkg>` 或 `go vet ./<pkg>` 验证，**不要用裸的 `go build ./<pkg>`**：spike 模块根下每个包目录名与默认输出二进制同名，`go build ./deps` 会报 `build output "deps" already exists and is a directory`。需要产物时显式指定 `-o`（Task 6 Step 1 就是这么做的）。
- 本计划**不得修改任何生产文件**，不得修改 `AGENTS.md`，不得改写 `docs/` 下任何活文档（约束反转与文档改写是阶段 1 的独立计划）。
- Task 1-4 的 netstack/WireGuard 探针必须在**没有 `/dev/net/tun`、没有 `CAP_NET_ADMIN`、非 root** 的环境下运行成功。这是本设计的核心主张，Task 4 必须显式记录运行环境。Task 5 不受此约束：**探针进程本身**仍是非特权的，但它验证的能力由宿主机一次性 `net.ipv4.ping_group_range` 门控，因此设置该 sysctl 需要 root——这正是设计要向用户交代的唯一宿主前置条件，不是探针自身的特权需求。
- Task 5 必须在 **Linux** 上运行。macOS 的 ICMP datagram socket 语义与 `net.ipv4.ping_group_range` 无关，在 macOS 上的结果无效，不得写入报告；探针在非 Linux 上输出 `SKIP` 并以退出码 0 结束，报告必须注明"未在 Linux 执行"而不是当作 PASS。
- 已核实可用的依赖基线（Task 1 必须原样钉死，不得 `@latest`）：

```
go 1.26.3

require (
	golang.org/x/crypto v0.57.0
	golang.org/x/net v0.59.0
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c
)

require (
	github.com/google/btree v1.1.2 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
)
```

- 已核实的 gVisor API 符号（行号基于 `v0.0.0-20250503011706-39ed1f5ac29c`；探针只允许使用这些，不得凭记忆臆造）：
  - `stack.New(stack.Options{NetworkProtocols, TransportProtocols, HandleLocal})`
  - `(*stack.Stack).CreateNIC(tcpip.NICID, stack.LinkEndpoint) tcpip.Error`
  - `(*stack.Stack).SetPromiscuousMode(tcpip.NICID, bool) tcpip.Error` — `stack.go:1675`
  - `(*stack.Stack).SetRouteTable([]tcpip.Route)` — `stack.go:738`
  - `(*stack.Stack).SetTransportProtocolHandler(tcpip.TransportProtocolNumber, func(stack.TransportEndpointID, *stack.PacketBuffer) bool)` — `stack.go:517`
  - `(*stack.Stack).AddProtocolAddress(tcpip.NICID, tcpip.ProtocolAddress, stack.AddressProperties) tcpip.Error`
  - `(*stack.Stack).Stats() tcpip.Stats`
  - `stack.NewPacketBuffer(stack.PacketBufferOptions{ReserveHeaderBytes int, Payload buffer.Buffer, IsForwardedPacket bool, OnRelease func()})`
  - `(*stack.PacketBuffer).ToView() *buffer.View` — `packet_buffer.go:308`
  - `stack.PacketHeader.Push(size int) []byte`、`stack.PacketHeader.View() *buffer.View` — `packet_buffer.go:493`
  - `stack.PacketBufferList.PushBack(*stack.PacketBuffer)` — `packet_buffer_list.go:61`
  - `channel.New(size int, mtu uint32, linkAddr tcpip.LinkAddress) *channel.Endpoint`
  - `(*channel.Endpoint).InjectInbound(tcpip.NetworkProtocolNumber, *stack.PacketBuffer)` — `channel.go:203`
  - `(*channel.Endpoint).Read() *stack.PacketBuffer` — `channel.go:176`
  - `(*channel.Endpoint).WritePackets(stack.PacketBufferList) (int, tcpip.Error)` — `channel.go:278`
  - `(*channel.Endpoint).AddNotify(channel.Notification) *channel.NotificationHandle` — `channel.go:298`；`channel.Notification` 的唯一方法是 `WriteNotify()` — `channel.go:30`
  - `gonet.ListenTCP(*stack.Stack, tcpip.FullAddress, tcpip.NetworkProtocolNumber) (*gonet.TCPListener, error)` — `adapters/gonet/gonet.go:76`
  - `(*gonet.TCPListener).Accept() (net.Conn, error)` — `gonet.go:260`
  - `gonet.DialUDP(*stack.Stack, laddr, raddr *tcpip.FullAddress, network tcpip.NetworkProtocolNumber) (*gonet.UDPConn, error)` — `gonet.go:569`
  - `tcpip.AddrFrom4Slice([]byte) tcpip.Address`、`tcpip.Address.WithPrefix()`
  - `header.IPv4`、`header.TCP`、`header.UDP`、`header.PseudoHeaderChecksum`、`header.IPv4EmptySubnet`、`header.IPv4ProtocolNumber`
  - `tcpip.Error` 是 `interface { isError(); IgnoreStats() bool; fmt.Stringer }` — `pkg/tcpip/errors.go:25`，**不实现 `error`**
- 钉死版本与 `@latest` 的已知 API 差异（写代码时按钉死版本）：
  - `buffer.View` 是 **struct**，取字节用 `.AsSlice()` 或 `.ToSlice()`，不能直接转换成 `header.TCP`
  - `pkt.TransportHeader().View()` 返回 `*buffer.View`，因此是 `header.TCP(pkt.TransportHeader().View().AsSlice())`
  - `header.TCPFlags` 没有 `Syn()`/`Ack()` 方法，用位运算 `flags&header.TCPFlagSyn != 0`
  - TCP 确认号访问器是 `AckNumber()`，不是 `AcknowledgmentNumber()`
  - `gonet` 包内没有 `ListenUDP`，只有 `DialUDP`
  - `tcpip.Stats.NICs` 是 struct（`Tx`/`Rx`/`TxPacketsDroppedNoBufferSpace`），不是切片；`tcpip.TCPStats` 无 `ListenOverflows`；`tcpip.IPStats` 无 `OutgoingNoRoute`/`NoSourceAddress`
- 已核实的 wireguard-go API 符号：
  - `tun.Device` 接口的全部 9 个方法：`File() *os.File`、`Read(bufs [][]byte, sizes []int, offset int) (int, error)`、`Write(bufs [][]byte, offset int) (int, error)`、`MTU() (int, error)`、`Name() (string, error)`、`Events() <-chan tun.Event`、`Close() error`、`BatchSize() int`
  - `device.NewDevice(tunDevice tun.Device, bind conn.Bind, logger *device.Logger) *device.Device` — `device/device.go:284`
  - `device.Logger{Verbosef, Errorf func(format string, args ...any)}`、`device.DiscardLogf`
  - `conn.NewDefaultBind() conn.Bind` — `conn/default.go:10`
  - `netstack.CreateNetTUN(localAddresses, dnsServers []netip.Addr, mtu int) (tun.Device, *netstack.Net, error)` — `tun/netstack/tun.go:53`
  - `(*netstack.Net).ListenTCPAddrPort(netip.AddrPort) (*gonet.TCPListener, error)`、`(*netstack.Net).DialContextTCPAddrPort(context.Context, netip.AddrPort) (*gonet.TCPConn, error)`
  - **`wgctrl/wgtypes` 不在 `golang.zx2c4.com/wireguard` 模块内**（该模块只有 `conn`/`device`/`ipc`/`ratelimiter`/`replay`/`rwcancel`/`tai64n`/`tun`）。密钥生成用 `crypto/rand` + `golang.org/x/crypto/curve25519`，与 wireguard-go 的 IPC 协议（hex 字符串）直接兼容。
---

### Task 1: 钉死可被标准 Go 工具链导入的 gVisor revision

`gvisor.dev/gvisor@latest`（pseudo-version `v0.0.0-20260919054224-26f3455a4cb9`）**无法通过 `go build`**：其 `pkg/tcpip/stack/bridge_test.go` 声明 `package bridge_test`，而同目录其余文件是 `package stack`（合法的外部测试包名只能是 `stack_test`），且该文件没有 `//go:build` 约束。任何 import `pkg/tcpip/stack` 的包都会失败：

```
found packages stack (addressable_endpoint_state.go) and bridge (bridge_test.go)
```

gVisor 上游用 Bazel 构建，不保证每个 pseudo-version 都能被 `go build` 导入。**已实测确认可用的 revision 是 `v0.0.0-20250503011706-39ed1f5ac29c`**，它正是 `golang.zx2c4.com/wireguard` 自身 `go.mod` 中 pin 的版本，因此 wireguard-go 与 gVisor 的兼容性由上游保证；该 revision 的 `pkg/tcpip/stack/` 目录下只有 `bridge.go` 与 `bridge_mutex.go`，没有任何测试文件。

本 Task 的目标是把这个结论固化成仓库里可复现、可回归的依赖钉定，而不是让后来者再去踩一遍 `@latest`。

**Files:**
- Create: `test/spike/vpn-task0/go.mod`
- Create: `test/spike/vpn-task0/README.md`
- Create: `test/spike/vpn-task0/deps/deps.go`
- Create: `test/spike/vpn-task0/deps/deps_test.go`

**Interfaces:**
- Consumes: 无（首个 Task）。
- Produces: `test/spike/vpn-task0/go.mod` 中钉死的 gVisor / wireguard-go 版本，Task 2-5 全部依赖它；`deps` 可执行探针输出运行环境信息，供 Task 6 写入报告。

- [ ] **Step 1: 建立隔离模块并钉死版本**

创建 `test/spike/vpn-task0/go.mod`：

```
module github.com/tunnelmesh/tunnelmesh/test/spike/vpn-task0

go 1.26.3
```

创建 `test/spike/vpn-task0/README.md`：

```markdown
# VPN 网关 Task 0 可行性 spike（一次性产物）

本目录是一次性技术验证代码，**不是产品代码**，不被主模块引用（独立 `go.mod`）。
结论汇总见 `REPORT.md`，设计依据见
`docs/superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md` 第 2.2 节。

依赖版本是**刻意钉死**的：`gvisor.dev/gvisor@latest` 无法被标准 Go 工具链导入
（`pkg/tcpip/stack/bridge_test.go` 声明了非法的 `package bridge_test`），必须使用
wireguard-go 自身 pin 的 `v0.0.0-20250503011706-39ed1f5ac29c`。不要执行
`go get -u`，那会把 gVisor 抬回不可构建的 revision。

运行全部探针（`icmpsock` 需 Linux，其余平台输出 SKIP）：

    cd test/spike/vpn-task0
    for p in deps tcpintercept udpintercept wgbridge icmpsock; do
      go run ./$p; echo "$p exit=$?"
    done
```

创建 `test/spike/vpn-task0/deps/deps.go`：

```go
// Command deps pins the spike's dependency versions, proves the module graph is
// importable by the standard Go toolchain, and prints the execution environment
// the other probes' privilege claims are made against. gVisor is built with
// Bazel upstream and does not guarantee that every pseudo-version compiles under
// `go build`, so this probe is the gate for every other task.
package main

import (
	"fmt"
	"os"
	"runtime"

	_ "golang.org/x/crypto/curve25519"
	_ "golang.org/x/net/icmp"
	_ "golang.zx2c4.com/wireguard/conn"
	_ "golang.zx2c4.com/wireguard/device"
	_ "golang.zx2c4.com/wireguard/tun"
	_ "golang.zx2c4.com/wireguard/tun/netstack"
	_ "gvisor.dev/gvisor/pkg/buffer"
	_ "gvisor.dev/gvisor/pkg/tcpip"
	_ "gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	_ "gvisor.dev/gvisor/pkg/tcpip/header"
	_ "gvisor.dev/gvisor/pkg/tcpip/link/channel"
	_ "gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	_ "gvisor.dev/gvisor/pkg/tcpip/stack"
	_ "gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	_ "gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

func main() {
	fmt.Println("PASS: gvisor + wireguard-go + x/net/icmp importable by the go tool")
	fmt.Println("      go:", runtime.Version(), "os:", runtime.GOOS, "arch:", runtime.GOARCH)
	fmt.Println("      uid:", os.Getuid(), "/dev/net/tun absent:", !exists("/dev/net/tun"))
}

func exists(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
```

- [ ] **Step 2: 生成 go.sum 并构建**

Run: `cd test/spike/vpn-task0 && go mod tidy && go vet ./deps && go run ./deps`

Expected: `go mod tidy` 后 `go.mod` 的 require 块与 Global Constraints 中列出的基线一致（4 条直接依赖 + 4 条 indirect）；`go run ./deps` 输出 `PASS: gvisor + wireguard-go + x/net/icmp importable by the go tool` 与环境行，退出码 0。

若报错为 `found packages stack ... and bridge ...`，说明 gVisor 被抬到了不可构建的 revision：检查是否误跑过 `go get -u`，用 `go get gvisor.dev/gvisor@v0.0.0-20250503011706-39ed1f5ac29c` 降回。

- [ ] **Step 3: 写回归测试守住这个钉定**

创建 `test/spike/vpn-task0/deps/deps_test.go`：

```go
package main

import (
	"go/build"
	"runtime"
	"strings"
	"testing"
)

// TestPinnedPackagesAreImportable guards the exact failure found at gvisor
// v0.0.0-20260919054224-26f3455a4cb9: pkg/tcpip/stack/bridge_test.go declares
// `package bridge_test` in a directory whose package is `stack`, so the go tool
// cannot load the directory at all. If a future `go get -u` re-breaks the pin,
// this test fails here instead of in a downstream task.
func TestPinnedPackagesAreImportable(t *testing.T) {
	for _, path := range []string{
		"gvisor.dev/gvisor/pkg/buffer",
		"gvisor.dev/gvisor/pkg/tcpip",
		"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet",
		"gvisor.dev/gvisor/pkg/tcpip/header",
		"gvisor.dev/gvisor/pkg/tcpip/link/channel",
		"gvisor.dev/gvisor/pkg/tcpip/network/ipv4",
		"gvisor.dev/gvisor/pkg/tcpip/stack",
		"gvisor.dev/gvisor/pkg/tcpip/transport/tcp",
		"gvisor.dev/gvisor/pkg/tcpip/transport/udp",
		"golang.zx2c4.com/wireguard/conn",
		"golang.zx2c4.com/wireguard/device",
		"golang.zx2c4.com/wireguard/tun",
		"golang.zx2c4.com/wireguard/tun/netstack",
		"golang.org/x/crypto/curve25519",
		"golang.org/x/net/icmp",
	} {
		if _, err := build.Import(path, ".", 0); err != nil {
			t.Errorf("cannot import %s: %v", path, err)
		}
	}
}

// TestGoVersionIsHighEnoughForGVisor records that gVisor forces the go
// directive up from the main module's 1.26.0; the spike module declares 1.26.3.
func TestGoVersionIsHighEnoughForGVisor(t *testing.T) {
	v := strings.TrimPrefix(runtime.Version(), "go")
	if strings.HasPrefix(v, "1.26.0") {
		t.Errorf("toolchain %s predates the go directive gVisor requires (1.26.3)", v)
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd test/spike/vpn-task0 && go test ./deps -v -count=1`
Expected: 2 个测试 PASS，退出码 0。

- [ ] **Step 5: 确认主模块未被污染**

Run: `cd /opt/app/workspace/TunnelMesh && git status --porcelain go.mod go.sum && go build ./... && go vet ./...`
Expected: `git status` 对 `go.mod`/`go.sum` **无输出**；`go build`/`go vet` 退出码 0。

- [ ] **Step 6: 记录并判定**

把钉死的版本写入 `REPORT.md`：gVisor 版本、wireguard-go 版本、go 指令版本、`go list -m all | wc -l` 的依赖条目数。

**中止判据（spec §2.2）**：若该 revision 也变为不可导入（例如上游重写历史或 module proxy 撤下），先尝试 wireguard-go 更早的 pin 与 Tailscale/tun2socks 当前 pin 的 gVisor 版本：

```bash
go mod download github.com/xjasonlyu/tun2socks/v2@latest
grep gvisor "$(go env GOMODCACHE)/cache/download/github.com/xjasonlyu/tun2socks/v2/@v/"*.mod
```

若全部候选都不可导入，则 **netstack 方案死亡**：立即停止 Task 2-5，在 `REPORT.md` 记录结论，并按 spec §2.2 上报用户。可选替代是把用户态栈放到 Agent 侧（违背"Agent 零特权零重依赖"结论）、或回退到真实 TUN + 内核转发（需要 Agent `CAP_NET_ADMIN` 与 nftables，已被部署约束排除）。**不得静默切换方案。**

- [ ] **Step 7: Commit**

```bash
git add test/spike/vpn-task0
git commit -m "test(spike): pin an importable gvisor revision for the vpn probe"
```

---

### Task 2: 验证 netstack 终结任意目标 TCP 并交出 net.Conn

设计主张：Server 侧 netstack 能接受并**终结**目标为任意内网 IP:端口的 TCP 连接，交出 `net.Conn` 供 splice 到 TunnelMesh stream。

前置核实已确认机制成立，但**三个条件缺一不可**，本 Task 的探针就是把这三条同时钉死：

1. `SetPromiscuousMode(nicID, true)` —— 使 IPv4 层用 `allowTemp=true` 调用 `AcquireAssignedAddress`，从而为任意目标地址合成临时地址端点并本地投递，而不是计入 `InvalidDestinationAddressesReceived` 后丢弃（`pkg/tcpip/network/ipv4/ipv4.go:1171`、`pkg/tcpip/stack/addressable_endpoint_state.go:552`）。
2. SYN 时 `AddProtocolAddress(nicID, dst/32)` —— 否则栈在回 SYN-ACK 时选不出源地址，**静默不发任何包**（实测 `ip.out sent=0`、`outErrs=0`）。
3. `gonet.ListenTCP` **精确 bind** 到该地址 —— 直接 bind 一个栈未拥有的地址会报 `bad local address`；通配 bind（`0.0.0.0:port`）虽能 bind 成功，但因为缺条件 2 依然发不出 SYN-ACK，且会让同端口的所有目标 IP 共用一个监听器。

**Files:**
- Create: `test/spike/vpn-task0/tcpintercept/main.go`

**Interfaces:**
- Consumes: Task 1 钉死的 `go.mod`。
- Produces: `tcpintercept` 可执行探针，退出码 0 = PASS。结论决定 spec §5.2 `vpn_flows.go` 的按需监听器实现方式，并把"需要动态 `AddProtocolAddress` + 地址回收策略"这一约束回流给阶段 1。

- [ ] **Step 1: 写探针**

创建 `test/spike/vpn-task0/tcpintercept/main.go`（已实测通过的版本，勿改动机制）：

```go
// Probe: can gVisor netstack terminate a TCP connection whose destination is an
// ARBITRARY internal address the stack does not own, and hand us a net.Conn --
// with no /dev/net/tun and no CAP_NET_ADMIN? This is the highest-risk
// assumption of the embedded VPN gateway design.
package main

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

const (
	nicID     = tcpip.NICID(1)
	mtu       = 1420
	clientIP  = "192.0.2.10"
	clientPt  = uint16(40000)
	serverIP  = "198.51.100.7"
	serverPt  = uint16(8080)
	clientISN = uint32(1000)
)

var (
	accepted     = make(chan string, 1)
	mu           sync.Mutex
	handlerCalls int
	listenErrs   []string
)

func main() {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})

	ep := channel.New(256, mtu, "")
	must("CreateNIC", s.CreateNIC(nicID, ep))
	// Promiscuous mode is what lets the IPv4 layer deliver a frame whose
	// destination is not one of the stack's own addresses: AcquireAssignedAddress
	// is called with allowTemp=nic.Promiscuous(), so gVisor synthesises a
	// temporary address endpoint for the arbitrary destination instead of
	// counting InvalidDestinationAddressesReceived and dropping.
	must("SetPromiscuousMode", s.SetPromiscuousMode(nicID, true))
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})

	// The interception hook. gVisor only calls defaultHandler when the demux
	// table has no matching endpoint (stack/nic.go DeliverTransportPacket), so
	// seeing exactly one call per new tuple is the success signal: every later
	// segment of that connection is consumed by the listener itself.
	//
	// Two steps are required per new tuple, and this probe proves both:
	//  1. AddProtocolAddress for the arbitrary destination. Without it the stack
	//     has no source address for the SYN-ACK and silently emits nothing.
	//  2. gonet.ListenTCP bound to that exact address. Binding an address the
	//     stack does not own fails with tcpip.ErrBadLocalAddress.
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
		mu.Lock()
		handlerCalls++
		mu.Unlock()
		th := header.TCP(pkt.TransportHeader().View().AsSlice())
		flags := th.Flags()
		if flags&header.TCPFlagSyn == 0 || flags&header.TCPFlagAck != 0 {
			return false
		}
		go listen(s, id.LocalAddress, id.LocalPort)
		return false
	})

	// --- 3-way handshake, driven entirely by injected packets -----------------
	// The hook registers the listener from a goroutine, so the very first SYN is
	// demuxed before the endpoint exists and is dropped. A real client
	// retransmits, so re-injecting the same SYN models normal TCP behaviour
	// rather than hiding a race in the production design.
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				ep.InjectInbound(ipv4.ProtocolNumber, buildTCP(clientISN, 0, header.TCPFlagSyn))
			}
		}
	}()
	ep.InjectInbound(ipv4.ProtocolNumber, buildTCP(clientISN, 0, header.TCPFlagSyn))

	synAck, ok := waitOutbound(ep, 5*time.Second)
	if !ok {
		close(stop)
		st := s.Stats()
		fmt.Printf("      STATS ip: recv=%d valid=%d malformed=%d invalidDst=%d invalidSrc=%d preroutingDrop=%d\n",
			st.IP.PacketsReceived.Value(), st.IP.ValidPacketsReceived.Value(),
			st.IP.MalformedPacketsReceived.Value(), st.IP.InvalidDestinationAddressesReceived.Value(),
			st.IP.InvalidSourceAddressesReceived.Value(), st.IP.IPTablesPreroutingDropped.Value())
		fmt.Printf("      STATS ip.out: sent=%d outErrs=%d\n",
			st.IP.PacketsSent.Value(), st.IP.OutgoingPacketErrors.Value())
		fmt.Printf("      STATS tcp: invalidSegs=%d failedPortRes=%d estabResets=%d activeOpens=%d passiveOpens=%d currEstab=%d\n",
			st.TCP.InvalidSegmentsReceived.Value(), st.TCP.FailedPortReservations.Value(),
			st.TCP.EstablishedResets.Value(), st.TCP.ActiveConnectionOpenings.Value(),
			st.TCP.PassiveConnectionOpenings.Value(), st.TCP.CurrentEstablished.Value())
		fmt.Printf("      STATS nic: txDroppedNoBuf=%d\n", st.NICs.TxPacketsDroppedNoBufferSpace.Value())
			report("FAIL: no SYN-ACK egressed within 5s")
	}
	close(stop)
	if synAck.AckNumber() != clientISN+1 {
		report(fmt.Sprintf("FAIL: SYN-ACK ack=%d, want %d", synAck.AckNumber(), clientISN+1))
	}
	if !synAck.IsSource(serverIP, serverPt) || !synAck.IsDest(clientIP, clientPt) {
		report(fmt.Sprintf("FAIL: SYN-ACK tuple %s:%d -> %s:%d",
			synAck.Src(), synAck.SrcPort(), synAck.Dst(), synAck.DstPort()))
	}
	fmt.Printf("OK: stack answered SYN-ACK for unowned dst %s:%d from %s:%d\n",
		serverIP, serverPt, synAck.Src(), synAck.SrcPort())

	ep.InjectInbound(ipv4.ProtocolNumber,
		buildTCP(clientISN+1, synAck.SequenceNumber()+1, header.TCPFlagAck))

	select {
	case got := <-accepted:
		mu.Lock()
		calls := handlerCalls
		mu.Unlock()
		fmt.Printf("PASS: netstack terminated arbitrary-destination TCP after %d handler call(s); %s\n", calls, got)
		return
	case <-time.After(3 * time.Second):
		report("FAIL: Accept did not return within 3s after the final ACK")
	}
}

func listen(s *stack.Stack, addr tcpip.Address, port uint16) {
	if terr := s.AddProtocolAddress(nicID, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: addr.WithPrefix(),
	}, stack.AddressProperties{}); terr != nil {
		mu.Lock()
		listenErrs = append(listenErrs, fmt.Sprintf("AddProtocolAddress %s -> %v", addr, terr))
		mu.Unlock()
		return
	}
	l, err := gonet.ListenTCP(s, tcpip.FullAddress{Addr: addr, Port: port}, ipv4.ProtocolNumber)
	if err != nil {
		mu.Lock()
		listenErrs = append(listenErrs, fmt.Sprintf("bind %s:%d -> %v", addr, port, err))
		mu.Unlock()
		return
	}
	defer l.Close()
	c, err := l.Accept()
	if err != nil {
		report(fmt.Sprintf("FAIL Accept: %v", err))
		return
	}
	defer c.Close()
	accepted <- fmt.Sprintf("local=%s remote=%s", c.LocalAddr(), c.RemoteAddr())
}

// outPkt is a parsed egress IPv4/TCP packet captured from the link endpoint.
type outPkt struct {
	raw []byte
}

func (p outPkt) ip() header.IPv4      { return header.IPv4(p.raw) }
func (p outPkt) tcp() header.TCP      { return header.TCP(p.raw[p.ip().HeaderLength():]) }
func (p outPkt) Src() string          { return p.ip().SourceAddress().String() }
func (p outPkt) Dst() string          { return p.ip().DestinationAddress().String() }
func (p outPkt) SrcPort() uint16      { return p.tcp().SourcePort() }
func (p outPkt) DstPort() uint16      { return p.tcp().DestinationPort() }
func (p outPkt) SequenceNumber() uint32 { return p.tcp().SequenceNumber() }
func (p outPkt) AckNumber() uint32    { return p.tcp().AckNumber() }
func (p outPkt) IsSource(ip string, port uint16) bool {
	return p.Src() == ip && p.SrcPort() == port
}
func (p outPkt) IsDest(ip string, port uint16) bool {
	return p.Dst() == ip && p.DstPort() == port
}

// waitOutbound polls the channel endpoint for the next IPv4/TCP packet the stack
// emits towards the peer. In production this is exactly the packet the VPN
// gateway hands to wireguard-go for encryption.
func waitOutbound(ep *channel.Endpoint, timeout time.Duration) (outPkt, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pkt := ep.Read(); pkt != nil {
			raw := pkt.ToView().AsSlice()
			pkt.DecRef()
			if len(raw) >= header.IPv4MinimumSize+header.TCPMinimumSize {
				return outPkt{raw: raw}, true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return outPkt{}, false
}

// buildTCP assembles a complete raw IPv4+TCP segment. It is handed to the stack
// as Payload -- never pushed into NetworkHeader -- because parse.IPv4 pulls the
// header out of Data() itself; pushing directly leaves Data() empty and the
// packet is rejected as malformed.
func buildTCP(seq, ack uint32, flags header.TCPFlags) *stack.PacketBuffer {
	srcAddr := tcpip.AddrFrom4Slice(net.ParseIP(clientIP).To4())
	dstAddr := tcpip.AddrFrom4Slice(net.ParseIP(serverIP).To4())
	total := header.IPv4MinimumSize + header.TCPMinimumSize
	raw := make([]byte, total)
	ip := header.IPv4(raw)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(total),
		TTL:         64,
		Protocol:    uint8(tcp.ProtocolNumber),
		SrcAddr:     srcAddr,
		DstAddr:     dstAddr,
	})
	ip.SetChecksum(^ip.CalculateChecksum())
	th := header.TCP(raw[header.IPv4MinimumSize:])
	th.Encode(&header.TCPFields{
		SrcPort:    clientPt,
		DstPort:    serverPt,
		SeqNum:     seq,
		AckNum:     ack,
		DataOffset: header.TCPMinimumSize,
		Flags:      flags,
		WindowSize: 64240,
	})
	th.SetChecksum(^th.CalculateChecksum(header.PseudoHeaderChecksum(
		tcp.ProtocolNumber, srcAddr, dstAddr, uint16(header.TCPMinimumSize))))
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(raw),
	})
	pkt.NetworkProtocolNumber = header.IPv4ProtocolNumber
	return pkt
}

func report(msg string) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Println(msg)
	fmt.Printf("      handlerCalls=%d listenErrs=%v\n", handlerCalls, listenErrs)
	os.Exit(1)
}

// must prints the gVisor error and exits. tcpip.Error deliberately does not
// implement error in this revision (it is only a fmt.Stringer), so the helper
// takes tcpip.Error rather than error.
func must(stage string, err tcpip.Error) {
	if err != nil {
		fmt.Printf("FAIL %s: %v\n", stage, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: 运行并记录特权状态**

Run: `cd test/spike/vpn-task0 && go run ./tcpintercept; echo "exit=$?"`

Expected（前置核实的真实输出，端口/序号可能不同）：

```
OK: stack answered SYN-ACK for unowned dst 198.51.100.7:8080 from 198.51.100.7:8080
PASS: netstack terminated arbitrary-destination TCP after 1 handler call(s); local=198.51.100.7:8080 remote=192.0.2.10:40000
exit=0
```

再确认运行环境无特权（这是设计主张的一部分）：

Run: `id -u; ls /dev/net/tun 2>&1; grep -c 'CreateTUN\|/dev/net' tcpintercept/main.go`

Expected: `id -u` 非 0；`/dev/net/tun` 不存在或不可访问；`grep` 计数为 1（仅注释中提及）。

- [ ] **Step 3: 确认"handler 只触发一次"是成功信号**

`stack/nic.go` 的 `DeliverTransportPacket` 先走 `demux.deliverPacket`，命中即返回，只有未命中才调 `defaultHandler`。因此探针输出 `after 1 handler call(s)` 表示监听器注册成功后，后续报文全部由栈自身 demux 消费——**这是设计成立的证据，不是丢包**。把这一点写进 `REPORT.md` 的 Task 2 段，避免后来者误读。

- [ ] **Step 4: 若 FAIL 则按 stats 定位**

探针失败时会打印 gVisor stats。判读表：

| 症状 | 根因 | 处理 |
| --- | --- | --- |
| `malformed=N`（N=注入数） | 原始包被 `NetworkHeader().Push()` 而不是放进 `Payload`；`parse.IPv4` 从 `Data()` 里 `Consume` 网络头，Data 为空即判畸形 | 保持 `PacketBufferOptions{Payload: buffer.MakeWithData(raw)}` 写法 |
| `invalidDst=N` | promiscuous 未生效 | 检查 `SetPromiscuousMode` 返回值 |
| `handlerCalls>0` 但 `sent=0`、`listenErrs` 含 `bad local address` | 缺 `AddProtocolAddress` | 保持条件 2 |
| `handlerCalls=0` | 包没到 L4，看 `malformed`/`invalidDst` | 按上两行处理 |

若确认机制不成立，触发 spec §2.2 中止判据并上报，**不得**改为在探针里预先枚举目标 IP 后一次性 `AddAddress`（那等于承认只能支持预配置的目标地址，与需求不符）。

- [ ] **Step 5: Commit**

```bash
git add test/spike/vpn-task0/tcpintercept
git commit -m "test(spike): probe netstack arbitrary-destination tcp termination"
```

---

### Task 3: 验证 netstack UDP 数据报中继与回程

UDP 无握手，因此**不需要 netstack 终结 UDP**：`SetTransportProtocolHandler(udp.ProtocolNumber, h)` 返回 `true` 即可直接消费任意目标的数据报（栈不再走 `HandleUnknownDestinationPacket`，因此也不会给用户回 ICMP port-unreachable），自行解析 `header.UDP` 取出五元组与载荷交给 TunnelMesh stream。

**回程不能用 `InjectInbound`**——那会被当作入向报文处理。正确路径是把构造好的原始 IPv4+UDP 包 `PacketBufferList.PushBack` 后调用 `ep.WritePackets(list)`，包会落到链路端点的出向队列，也就是 wireguard-go 从内存 TUN `Read` 时取包的同一个队列。前置核实已确认该路径成立，且**不需要**任何 socket、路由或 `AddProtocolAddress`。

这比 TCP 路径简单得多，也是 spec §5.2 UDP 分支应当采用的实现（需按此改写）。

**Files:**
- Create: `test/spike/vpn-task0/udpintercept/main.go`

**Interfaces:**
- Consumes: Task 1 钉死的 `go.mod`。
- Produces: `udpintercept` 探针，退出码 0 = PASS。结论决定 spec §5.2 `vpn_packet.go` 的 UDP 分支与回程实现。

- [ ] **Step 1: 写探针**

创建 `test/spike/vpn-task0/udpintercept/main.go`（已实测通过的版本）：

```go
// Probe: can the VPN gateway relay UDP to/from an ARBITRARY internal address
// without netstack UDP termination? Proves both directions:
//
//	inbound  - SetTransportProtocolHandler(udp) receives the datagram even
//	           though the stack owns no address for it, and returning true
//	           consumes it so no ICMP port-unreachable is generated.
//	outbound - a hand-built reply pushed through the link endpoint's
//	           WritePackets appears on the same queue wireguard-go drains,
//	           so it reaches the peer without any socket or route setup.
package main

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

const (
	nicID     = tcpip.NICID(1)
	mtu       = 1420
	clientIP  = "192.0.2.10"
	clientPt  = uint16(40000)
	serverIP  = "198.51.100.7"
	serverPt  = uint16(53)
	reqBody   = "tunnelmesh-udp-probe"
	replyBody = "dns-answer"
)

var (
	mu           sync.Mutex
	handlerCalls int
	captured     []string
	icmpGenerated int
)

func main() {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{udp.NewProtocol},
	})
	ep := channel.New(256, mtu, "")
	must("CreateNIC", s.CreateNIC(nicID, ep))
	must("SetPromiscuousMode", s.SetPromiscuousMode(nicID, true))
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})

	s.SetTransportProtocolHandler(udp.ProtocolNumber, func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
		mu.Lock()
		handlerCalls++
		mu.Unlock()
		uh := header.UDP(pkt.TransportHeader().View().AsSlice())
		body := pkt.Data().AsRange().ToSlice()
		mu.Lock()
		captured = append(captured, fmt.Sprintf("%s:%d -> %s:%d len=%d",
			id.RemoteAddress, uh.SourcePort(), id.LocalAddress, uh.DestinationPort(), len(body)))
		mu.Unlock()
		// Relay the datagram to the internal service, then emit the answer back
		// towards the peer through the link endpoint.
		go emitReply(ep, id, replyBody)
		// Returning true tells the stack the datagram is fully handled, so it
		// must not fall through to HandleUnknownDestinationPacket and send an
		// ICMP port-unreachable back to the user.
		return true
	})

	ep.InjectInbound(ipv4.ProtocolNumber, buildUDP(reqBody))

	select {
	case raw := <-egressOf(ep, 3*time.Second):
		ip := header.IPv4(raw)
		uh := header.UDP(raw[ip.HeaderLength():])
		body := raw[ip.HeaderLength()+header.UDPMinimumSize:]
		mu.Lock()
		calls, cap0 := handlerCalls, captured
		mu.Unlock()
		if len(cap0) != 1 {
			fail(fmt.Sprintf("captured %d datagrams, want 1: %v", len(cap0), cap0))
		}
		if string(body) != replyBody {
			fail(fmt.Sprintf("reply body %q, want %q", body, replyBody))
		}
		if ip.SourceAddress().String() != serverIP || ip.DestinationAddress().String() != clientIP {
			fail(fmt.Sprintf("reply tuple %s -> %s", ip.SourceAddress(), ip.DestinationAddress()))
		}
		if uh.SourcePort() != serverPt || uh.DestinationPort() != clientPt {
			fail(fmt.Sprintf("reply ports %d -> %d", uh.SourcePort(), uh.DestinationPort()))
		}
		fmt.Printf("PASS: netstack consumed UDP %s and emitted the reply (%d bytes) towards the peer\n", cap0[0], len(raw))
		fmt.Printf("      transport handler invoked %d time(s); no socket, route or address setup needed\n", calls)
		return
	case <-time.After(3 * time.Second):
		mu.Lock()
		fmt.Printf("FAIL: no reply egressed; handlerCalls=%d captured=%v\n", handlerCalls, captured)
		mu.Unlock()
		os.Exit(1)
	}
}

// egressOf returns the raw bytes of the next packet the stack (or we) put on
// the link endpoint's outbound queue -- exactly what wireguard-go would read
// from the memory TUN and encrypt.
func egressOf(ep *channel.Endpoint, timeout time.Duration) <-chan []byte {
	out := make(chan []byte, 1)
	go func() {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if pkt := ep.Read(); pkt != nil {
				raw := pkt.ToView().AsSlice()
				pkt.DecRef()
				out <- raw
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		close(out)
	}()
	return out
}

// emitReply builds the answer datagram and pushes it straight onto the link
// endpoint's outbound queue. The gateway owns this queue in both directions, so
// no netstack UDP socket, source address or route is required.
func emitReply(ep *channel.Endpoint, id stack.TransportEndpointID, body string) {
	raw := buildRawUDP(id.LocalAddress, id.LocalPort, id.RemoteAddress, id.RemotePort, body)
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(raw),
	})
	pkt.NetworkProtocolNumber = header.IPv4ProtocolNumber
	var list stack.PacketBufferList
	list.PushBack(pkt)
	if _, terr := ep.WritePackets(list); terr != nil {
		fail(fmt.Sprintf("WritePackets -> %v", terr))
	}
}

// buildRawUDP assembles a complete raw IPv4+UDP datagram with valid checksums.
func buildRawUDP(srcAddr tcpip.Address, srcPort uint16, dstAddr tcpip.Address, dstPort uint16, body string) []byte {
	total := header.IPv4MinimumSize + header.UDPMinimumSize + len(body)
	raw := make([]byte, total)
	ip := header.IPv4(raw)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(total),
		TTL:         64,
		Protocol:    uint8(udp.ProtocolNumber),
		SrcAddr:     srcAddr,
		DstAddr:     dstAddr,
	})
	ip.SetChecksum(^ip.CalculateChecksum())
	uh := header.UDP(raw[header.IPv4MinimumSize:])
	uh.Encode(&header.UDPFields{
		SrcPort: srcPort,
		DstPort: dstPort,
		Length:  uint16(header.UDPMinimumSize + len(body)),
	})
	copy(raw[header.IPv4MinimumSize+header.UDPMinimumSize:], body)
	uh.SetChecksum(^uh.CalculateChecksum(header.PseudoHeaderChecksum(
		udp.ProtocolNumber, srcAddr, dstAddr,
		uint16(header.UDPMinimumSize+len(body)))))
	return raw
}

func buildUDP(body string) *stack.PacketBuffer {
	srcAddr := tcpip.AddrFrom4Slice(net.ParseIP(clientIP).To4())
	dstAddr := tcpip.AddrFrom4Slice(net.ParseIP(serverIP).To4())
	raw := buildRawUDP(srcAddr, clientPt, dstAddr, serverPt, body)
	// Passed as Payload, never pushed into NetworkHeader: the stack's parse.IPv4
	// pulls the header out of Data() itself.
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(raw),
	})
	pkt.NetworkProtocolNumber = header.IPv4ProtocolNumber
	return pkt
}

func fail(msg string) {
	fmt.Println("FAIL:", msg)
	os.Exit(1)
}

func must(stage string, err tcpip.Error) {
	if err != nil {
		fmt.Printf("FAIL %s: %v\n", stage, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: 运行**

Run: `cd test/spike/vpn-task0 && go run ./udpintercept; echo "exit=$?"`

Expected（前置核实的真实输出）：

```
PASS: netstack consumed UDP 192.0.2.10:40000 -> 198.51.100.7:53 len=20 and emitted the reply (38 bytes) towards the peer
      transport handler invoked 1 time(s); no socket, route or address setup needed
exit=0
```

- [ ] **Step 3: 确认回程方向正确**

探针校验了回程包的 `ip.SourceAddress()==198.51.100.7`、`ip.DestinationAddress()==192.0.2.10`、端口对调、载荷为 `dns-answer`。这四条断言共同证明包是**朝用户方向**发出的，而不是又一次入向投递。把这四条写进 `REPORT.md`。

若有人把它改成 `ep.InjectInbound(...)` 当回包，探针会因出向队列始终为空而超时失败——这是刻意的防回归设计，**不得**为了让它通过而放宽断言。

- [ ] **Step 4: Commit**

```bash
git add test/spike/vpn-task0/udpintercept
git commit -m "test(spike): probe netstack udp relay and egress path"
```

---

### Task 4: 验证 wireguard-go 在内存态 tun.Device 上跑通端到端隧道

设计主张：wireguard-go 的 `device.NewDevice` 只需要一个满足 `tun.Device` 的实现，该实现可以是纯内存的，因此 Server **不需要 `/dev/net/tun`，也不需要 `CAP_NET_ADMIN`**。这是 spec §2.2 的头号验证项，也是"特权不进入控制面进程"这一结论的唯一依据。

前置核实已确认：wireguard-go **自带** `tun/netstack` 包，`CreateNetTUN(localAddresses, dnsServers []netip.Addr, mtu int) (tun.Device, *Net, error)` 返回一个由 gVisor netstack + `channel` 链路端点支撑的 `tun.Device`，`File()` 返回 `nil`，全程不打开任何设备节点。探针据此搭建**两个进程内 WireGuard 设备**，经真实 UDP loopback bind 完成 Noise 握手，并承载一条真实 TCP 连接。

同时记录一条必须回流到阶段 1 的约束：`CreateNetTUN` **不调用** `SetPromiscuousMode`，也不暴露 `*stack.Stack` 与 `*channel.Endpoint`，因此无法直接用于任意目标拦截（Task 2）与 UDP 回程（Task 3）。生产的 `vpn_device.go` 需要以 `netTun`（`tun/netstack/tun.go`，约 190 行）为蓝本自行实现，并额外暴露 stack 与 endpoint。

**Files:**
- Create: `test/spike/vpn-task0/wgbridge/main.go`

**Interfaces:**
- Consumes: Task 1 钉死的 `go.mod`（含 `golang.org/x/crypto/curve25519`）。
- Produces: `wgbridge` 探针，退出码 0 = PASS。结论直接决定 spec §5.2 `vpn_device.go` 是否成立，以及 §11 特权威胁项是否需要按 §2.2 回退路径改写。

- [ ] **Step 1: 写探针**

创建 `test/spike/vpn-task0/wgbridge/main.go`（已实测通过的版本）。注意密钥用 `crypto/rand` + `curve25519` 生成：`wgctrl/wgtypes` 是独立模块，**不在** `golang.zx2c4.com/wireguard` 内，不得为 spike 引入。

```go
// Probe: can a WireGuard tunnel run entirely in memory, with NO /dev/net/tun
// and NO CAP_NET_ADMIN? Two in-process wireguard-go devices are attached to
// gVisor-netstack-backed tun.Device implementations (upstream
// golang.zx2c4.com/wireguard/tun/netstack), complete a real Noise handshake
// over a UDP loopback bind, and carry a real TCP connection end to end.
//
// This is the load-bearing claim of the embedded VPN gateway design: the VPN
// data plane can live inside tunnelmesh-server without granting it any kernel
// networking privilege.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"os"
	"runtime"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

const (
	mtu          = 1420
	serverVPNIP  = "10.64.0.1"
	clientVPNIP  = "10.64.0.2"
	serverTCPPort = uint16(8080)
	wantBody      = "hello-over-wireguard-memory-tun"
)

func main() {
	// CreateNetTUN returns a tun.Device backed purely by a gVisor netstack and a
	// channel link endpoint -- there is no file descriptor behind it, so
	// tun.Device.File() returns nil and nothing ever opens /dev/net/tun.
	serverTUN, serverNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(serverVPNIP)}, nil, mtu)
	if err != nil {
		fail("CreateNetTUN(server)", err)
	}
	clientTUN, clientNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(clientVPNIP)}, nil, mtu)
	if err != nil {
		fail("CreateNetTUN(client)", err)
	}
	var _ tun.Device = serverTUN // compile-time proof of the interface

	logger := &device.Logger{Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf}
	serverDev := device.NewDevice(serverTUN, conn.NewDefaultBind(), logger)
	clientDev := device.NewDevice(clientTUN, conn.NewDefaultBind(), logger)
	defer serverDev.Close()
	defer clientDev.Close()

	serverPriv := newKey()
	clientPriv := newKey()
	serverPub, err := curve25519.X25519(serverPriv, curve25519.Basepoint)
	if err != nil {
		fail("server public key", err)
	}
	clientPub, err := curve25519.X25519(clientPriv, curve25519.Basepoint)
	if err != nil {
		fail("client public key", err)
	}

	if err := serverDev.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=0\nreplace_peers=true\n", hex.EncodeToString(serverPriv))); err != nil {
		fail("server IpcSet identity", err)
	}
	listenPort := currentListenPort(serverDev)
	if listenPort == 0 {
		fail("could not read the server's WireGuard listen port", nil)
	}
	fmt.Printf("OK: wireguard-go accepted an in-memory tun.Device; server listening on UDP 127.0.0.1:%d\n", listenPort)

	if err := serverDev.IpcSet(fmt.Sprintf("public_key=%s\nallowed_ip=%s/32\n", hex.EncodeToString(clientPub), clientVPNIP)); err != nil {
		fail("server IpcSet peer", err)
	}
	if err := clientDev.IpcSet(fmt.Sprintf(
		"private_key=%s\nlisten_port=0\nreplace_peers=true\npublic_key=%s\nendpoint=127.0.0.1:%d\nallowed_ip=%s/32\npersistent_keepalive_interval=0\n",
		hex.EncodeToString(clientPriv), hex.EncodeToString(serverPub), listenPort, serverVPNIP)); err != nil {
		fail("client IpcSet", err)
	}

	// Server side: terminate the VPN-borne TCP connection inside its own
	// netstack, exactly as the gateway will before splicing to a TunnelMesh stream.
	ln, err := serverNet.ListenTCPAddrPort(netip.AddrPortFrom(netip.MustParseAddr(serverVPNIP), serverTCPPort))
	if err != nil {
		fail("server ListenTCPAddrPort", err)
	}
	defer ln.Close()

	type accepted struct {
		remote string
		body   string
	}
	got := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(c)
		got <- accepted{remote: c.RemoteAddr().String(), body: string(b)}
	}()

	// Client side: dial through its own netstack; the packet leaves via the
	// memory TUN, is encrypted by wireguard-go, and crosses a real UDP socket.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cc, err := clientNet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(netip.MustParseAddr(serverVPNIP), serverTCPPort))
	if err != nil {
		fail("client DialContextTCPAddrPort (handshake did not complete)", err)
	}
	if _, err := cc.Write([]byte(wantBody)); err != nil {
		fail("client write", err)
	}
	cc.CloseWrite()

	select {
	case a := <-got:
		if a.body != wantBody {
			fail(fmt.Sprintf("payload %q, want %q", a.body, wantBody), nil)
		}
		fmt.Printf("PASS: WireGuard handshake + TCP round trip over memory TUN; server saw %s -> %q\n", a.remote, a.body)
		fmt.Printf("      env: %s/%s uid=%d, /dev/net/tun absent=%v, CAP_NET_ADMIN=none\n",
			runtime.GOOS, runtime.GOARCH, os.Getuid(), !fileExists("/dev/net/tun"))
		return
	case <-time.After(15 * time.Second):
		fail("no data accepted on the server side within 15s", nil)
	}
}

// newKey returns 32 random bytes suitable as a WireGuard X25519 private key.
// wgctrl/wgtypes is a separate module and is deliberately not added to the
// spike; crypto/rand + curve25519 is all the IPC protocol needs.
func newKey() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		fail("generate key", err)
	}
	return k
}

// currentListenPort reads back the port the kernel assigned for listen_port=0.
func currentListenPort(d *device.Device) int {
	out, err := d.IpcGet()
	if err != nil {
		return 0
	}
	var port int
	for _, line := range splitLines(out) {
		if n, ok := cutPrefix(line, "listen_port="); ok {
			fmt.Sscanf(n, "%d", &port)
		}
	}
	return port
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}

func fileExists(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func fail(stage string, err error) {
	if err != nil {
		fmt.Printf("FAIL %s: %v\n", stage, err)
	} else {
		fmt.Printf("FAIL %s\n", stage)
	}
	os.Exit(1)
}
```

- [ ] **Step 2: 运行**

Run: `cd test/spike/vpn-task0 && go run ./wgbridge; echo "exit=$?"`

Expected（前置核实的真实输出，端口会变化）：

```
OK: wireguard-go accepted an in-memory tun.Device; server listening on UDP 127.0.0.1:60482
PASS: WireGuard handshake + TCP round trip over memory TUN; server saw 10.64.0.2:29771 -> "hello-over-wireguard-memory-tun"
      env: darwin/arm64 uid=501, /dev/net/tun absent=true, CAP_NET_ADMIN=none
exit=0
```

这条输出同时证明四件事，全部要抄进 `REPORT.md`：wireguard-go 接受了内存 TUN；Noise 握手在真实 UDP socket 上完成；TCP 载荷端到端到达；运行环境非 root 且无 `/dev/net/tun`。

- [ ] **Step 3: 确认未触碰 /dev/net/tun**

Run: `grep -n 'CreateTUN\|os.OpenFile\|/dev/net' test/spike/vpn-task0/wgbridge/main.go`

Expected（前置核实的真实结果，共 4 处命中）：

```
1:// Probe: can a WireGuard tunnel run entirely in memory, with NO /dev/net/tun
41:	// tun.Device.File() returns nil and nothing ever opens /dev/net/tun.
131:		fmt.Printf("      env: %s/%s uid=%d, /dev/net/tun absent=%v, CAP_NET_ADMIN=none\n",
132:			runtime.GOOS, runtime.GOARCH, os.Getuid(), !fileExists("/dev/net/tun"))
```

其中 1/41/131 是注释与输出文本，132 是**用于断言设备不存在**的 `fileExists` 调用；`CreateTUN` 与 `os.OpenFile` 均为 0 命中，即没有任何打开设备节点的代码。同时在 Linux 上补一次 `grep Cap /proc/self/status` 或 `capsh --print`，把实际 capability 集合抄进报告。

- [ ] **Step 4: 在 Linux 上复跑**

Run（Linux 容器或主机，非 root）：`cd test/spike/vpn-task0 && go run ./wgbridge`
Expected: 同样 PASS。macOS 上的结果已经足够证明机制（纯用户态、与内核网络栈无关），但生产目标是 Linux，报告必须包含一次 Linux 运行的环境行。

- [ ] **Step 5: Commit**

```bash
git add test/spike/vpn-task0/wgbridge
git commit -m "test(spike): prove wireguard-go end-to-end tunnel on a memory tun"
```

---
### Task 5: 验证非特权 ICMP datagram socket（必须 Linux）

设计主张：Agent 侧只需宿主机一次性设置 `net.ipv4.ping_group_range`，即可用非特权 ICMP datagram socket 发出 echo 请求，从而实现"内网可 ping"。同时必须确认**内核会改写 ICMP id**——这是 spec §6.2 决定"关联用 Server 侧 ID 而非 ICMP id"的依据。

**Files:**
- Create: `test/spike/vpn-task0/icmpsock/main.go`

**Interfaces:**
- Consumes: Task 1 钉死的 `go.mod`（`golang.org/x/net` 已在仓库主模块中，spike 模块需自行 require）。
- Produces: `icmpsock` 探针，退出码 0 = PASS，并输出内核分配的 ICMP id 与请求 id 的对比，供 spec §6.2 的关联设计定稿。

本探针代码**已核实**：在 darwin/arm64 上 `go build` 通过、`GOOS=linux go vet` 无告警、运行时正确输出 `SKIP: must run on Linux; result on darwin is void` 并以退出码 0 结束。因此本 Task 的剩余风险只在 Linux 内核的 `ping_group_range` 行为，不在代码。

- [ ] **Step 1: 先在当前平台确认可编译且正确 SKIP**

Run: `cd test/spike/vpn-task0 && go vet ./icmpsock && GOOS=linux go vet ./icmpsock && go run ./icmpsock; echo "exit=$?"`
Expected: 构建与 vet 均无输出；运行输出 `SKIP: must run on Linux; result on <平台> is void`，`exit=0`。**这个 SKIP 不得写入 `REPORT.md` 的结论行**，只作为代码正确性的旁证。

- [ ] **Step 2: 写探针**（若 Step 1 已能构建，说明文件已存在，跳过本步）

创建 `test/spike/vpn-task0/icmpsock/main.go`：

```go
// Command icmpsock proves an unprivileged ICMP datagram socket works when
// net.ipv4.ping_group_range covers the process GID, and reports whether the
// kernel rewrites the ICMP identifier. MUST run on Linux: macOS has no
// ping_group_range and its ICMP semantics differ, so a macOS result is void.
package main

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

func main() {
	if runtime.GOOS != "linux" {
		fmt.Println("SKIP: must run on Linux; result on", runtime.GOOS, "is void")
		os.Exit(0)
	}
	target := os.Getenv("TM_SPIKE_ICMP_TARGET")
	if target == "" {
		target = "127.0.0.1"
	}

	// "udp4" selects the unprivileged ICMP datagram socket (SOCK_DGRAM,
	// IPPROTO_ICMP), which requires net.ipv4.ping_group_range to cover our GID.
	c, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		fmt.Printf("FAIL: cannot open unprivileged ping socket: %v\n", err)
		fmt.Println("      check: sysctl net.ipv4.ping_group_range")
		os.Exit(1)
	}
	defer c.Close()

	const wantID = 0x1234
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho, Code: 0,
		Body: &icmp.Echo{ID: wantID, Seq: 1, Data: []byte("tunnelmesh-task0")},
	}
	b, err := msg.Marshal(nil)
	if err != nil {
		fmt.Println("FAIL marshal:", err)
		os.Exit(1)
	}
	dst := &net.UDPAddr{IP: net.ParseIP(target)}
	if _, err := c.WriteTo(b, dst); err != nil {
		fmt.Printf("FAIL: write to %s: %v\n", target, err)
		os.Exit(1)
	}

	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	reply := make([]byte, 1500)
	n, peer, err := c.ReadFrom(reply)
	if err != nil {
		fmt.Printf("FAIL: no echo reply from %s: %v\n", target, err)
		os.Exit(1)
	}
	rm, err := icmp.ParseMessage(1, reply[:n])
	if err != nil {
		fmt.Println("FAIL parse:", err)
		os.Exit(1)
	}
	echo, ok := rm.Body.(*icmp.Echo)
	if !ok {
		fmt.Printf("FAIL: unexpected reply body type %T from %v\n", rm.Body, peer)
		os.Exit(1)
	}
	fmt.Printf("PASS: unprivileged ping socket works; target=%s reply_seq=%d\n", target, echo.Seq)
	fmt.Printf("      requested id=0x%04x, kernel-visible id=0x%04x\n", wantID, echo.ID)
	if echo.ID != wantID {
		fmt.Println("      CONFIRMED: kernel rewrites the ICMP id -> the gateway must")
		fmt.Println("      correlate replies with its own id carried in the stream,")
		fmt.Println("      never with the ICMP id (spec section 6.2).")
	} else {
		fmt.Println("      NOTE: id preserved on this kernel; still correlate by stream id.")
	}
}
```

- [ ] **Step 3: 在 Linux 上运行（未设置 sysctl，预期失败）**

Run（Linux 主机或容器）：`sysctl net.ipv4.ping_group_range; cd test/spike/vpn-task0 && go run ./icmpsock; echo "exit=$?"`
Expected: 若当前 `ping_group_range` 为 `1 0`（内核默认，即禁用），探针输出 `FAIL: cannot open unprivileged ping socket`，`exit=1`。**这个失败是预期的**，它证明该能力确实受 sysctl 门控。

- [ ] **Step 4: 设置 sysctl 后重跑（预期通过）**

Run（需要 root 一次性设置）：`sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"` 然后 `cd test/spike/vpn-task0 && go run ./icmpsock; echo "exit=$?"`
Expected: 输出 `PASS: unprivileged ping socket works` 与 id 对比行，`exit=1` 变为 `exit=0`。

- [ ] **Step 5: 记录 id 改写结论**

把 Step 4 输出的 `requested id` 与 `kernel-visible id` 原样抄进 `REPORT.md`。若内核改写了 id，则 spec §6.2 "关联不使用内核分配的 ICMP id" 得到实证支持；若未改写，该设计仍然成立（用 stream 侧关联 ID 更稳健），在报告中注明即可。

- [ ] **Step 6: Commit**

```bash
git add test/spike/vpn-task0/icmpsock
git commit -m "test(spike): probe unprivileged icmp datagram socket on linux"
```

---

### Task 6: 依赖影响评估与汇总报告

**Files:**
- Create: `test/spike/vpn-task0/REPORT.md`
- Modify: `test/spike/vpn-task0/README.md`（补运行前提与结论指针）

**Interfaces:**
- Consumes: Task 1-5 的全部输出与退出码。
- Produces: `REPORT.md`，作为 spec §15 阶段 2 的交付物与阶段 3 的开工门禁；其中钉死的版本号将被阶段 3 之后的生产 `go.mod` 直接采用，"必须回流到阶段 1 的设计修正"清单将直接驱动 ADR 0002 的撰写。

- [ ] **Step 1: 采集依赖影响数据**

Run:

```bash
cd test/spike/vpn-task0
go list -m all | wc -l                      # spike 模块总依赖条目数
go list -deps ./tcpintercept | wc -l        # TCP 探针的包数
go list -deps ./wgbridge | wc -l            # WireGuard 探针的包数
du -sh "$(go env GOMODCACHE)/gvisor.dev"    # gVisor 模块体积
go build -o /tmp/tm-probe ./tcpintercept && ls -l /tmp/tm-probe   # 探针二进制体积
cd /opt/app/workspace/TunnelMesh && go build -o /tmp/tm-server ./cmd/tunnelmesh-server && ls -l /tmp/tm-server
```

把六个数字记入报告：spike 依赖条目数、TCP 探针包数、WireGuard 探针包数、gVisor 模块体积、探针二进制体积、当前 `tunnelmesh-server` 二进制体积。这组数字是 spec §5.4 "build tag 隔离"决策的量化依据——它要回答的是"把 VPN 编进默认产物会让二进制涨多少"。

- [ ] **Step 2: 写 REPORT.md**

创建 `test/spike/vpn-task0/REPORT.md`，逐项填写实际输出（**不得**填写预期值或占位符；没有跑出来的项写"未执行"并说明原因）：

```markdown
# VPN 网关 Task 0 可行性报告

- 日期：<实际执行日期>
- 执行环境：<GOOS/GOARCH、内核版本、是否容器、id -u、有无 CAP_NET_ADMIN、/dev/net/tun 是否存在>
- 规格：`docs/superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md` 第 2.2 节
- 计划：`docs/superpowers/plans/2026-09-19-vpn-task0-feasibility.md`

## 结论

<一句话：全部通过 / 某项失败触发回退>

## 钉死版本

| 模块 | 版本 | 备注 |
| --- | --- | --- |
| gvisor.dev/gvisor | v0.0.0-20250503011706-39ed1f5ac29c | wireguard-go 自身 pin 的版本；`@latest`（v0.0.0-20260919054224-26f3455a4cb9）不可 `go build`，原因见下 |
| golang.zx2c4.com/wireguard | v0.0.0-20260522210424-ecfc5a8d5446 | 自带 `tun/netstack` 内存 TUN |
| golang.org/x/crypto | v0.57.0 | curve25519，替代不存在的 `wgctrl/wgtypes` |
| golang.org/x/net | v0.59.0 | `icmp` 包 |
| go 指令 | 1.26.3 | 主模块当前为 1.26.0，引入 gVisor 后需抬升 |

## Task 1：gVisor 可导入性

`@latest` 的实际报错：

    <粘贴完整报错>

根因：该 revision 的 `pkg/tcpip/stack/bridge_test.go` 声明 `package bridge_test`，同目录其余
文件为 `package stack`，合法外部测试包名只能是 `stack_test`，且该文件无 `//go:build` 约束。
gVisor 上游用 Bazel 构建，不保证每个 pseudo-version 可被 `go build` 导入。被钉死的
`v0.0.0-20250503011706-39ed1f5ac29c` 该目录下只有 `bridge.go`/`bridge_mutex.go`，无测试文件。

`go run ./deps` 与 `go test ./deps -v -count=1` 的输出：<粘贴>

## Task 2：netstack 任意目标 TCP 终结

命令：`go run ./tcpintercept`
输出：<粘贴>
三个必要条件各自的实测证据（promiscuous / AddProtocolAddress / 精确 bind）：<粘贴>
"handler 只触发一次即成功"的说明：<粘贴 Step 3 结论>
结论：<PASS/FAIL>

## Task 3：netstack UDP 中继与回程

命令：`go run ./udpintercept`
输出：<粘贴>
回程包四条断言（源 IP、目的 IP、端口对调、载荷）的实际值：<粘贴>
结论：<PASS/FAIL>

## Task 4：wireguard-go 内存态 tun.Device

命令：`go vet ./wgbridge && go run ./wgbridge`
输出（darwin）：<粘贴>
输出（Linux，非 root）：<粘贴>
`/dev/net/tun` 与 capability 状态：<粘贴 Step 3 的 grep 结果与 `capsh --print`>
`CreateNetTUN` 不可直接复用的原因：<粘贴>
结论：<PASS/FAIL>

## Task 5：非特权 ICMP datagram socket（Linux）

未设置 sysctl 时的输出：<粘贴>
设置 `net.ipv4.ping_group_range="0 2147483647"` 后的输出：<粘贴>
ICMP id 是否被内核改写：<粘贴 id 对比行>
sysctl 是否已恢复原值：<是/否，附命令>
结论：<PASS/FAIL/未在 Linux 执行>

## Task 6：依赖影响

| 指标 | 值 |
| --- | --- |
| spike 模块依赖条目数 | |
| TCP 探针包数 | |
| WireGuard 探针包数 | |
| gVisor 模块体积 | |
| 探针二进制体积 | |
| 当前 tunnelmesh-server 二进制体积 | |

## 必须回流到阶段 1 的设计修正

逐项确认下列修正已在 ADR 0002 / spec 修订计划中登记（计划正文的对应小节列出了完整理由）：

1. `vpn_device.go` 不能直接复用 `CreateNetTUN`，需以 `netTun` 为蓝本自行实现并暴露 stack 与 endpoint。
2. 任意目标 TCP 需要按连接目标动态 `AddProtocolAddress(dst/32)`，必须给出地址回收策略。
3. UDP 回程走 `ep.WritePackets`，不是 `InjectInbound`。
4. 注入 netstack 的原始包必须放进 `PacketBufferOptions.Payload`。
5. 钉死版本的 `tcpip.Error` 不实现 `error`，错误处理封装需据此设计。
6. `SetTransportProtocolHandler` 只在 demux 未命中时调用，流表描述需据此修正。

## 决策门禁

- 全部 PASS：进入 spec §15 阶段 1（约束反转 + ADR 0002 + 活文档改写），随后阶段 3（Schema v15）。
- 任一 FAIL：按 spec §2.2 上报用户，说明失败项、已尝试的替代、以及回退方案的代价。
  **不得静默切换方案，不得在报告未定稿前开始任何生产代码。**
```

- [ ] **Step 3: 更新 spike README**

在 `test/spike/vpn-task0/README.md` 末尾追加：

```markdown
## 结论

见 `REPORT.md`。本目录为一次性验证产物，不被主模块引用；保留它是为了让上述结论可复现。
`icmpsock` 需在 Linux 上运行，且需要 root 一次性设置 `net.ipv4.ping_group_range`；
其余四个探针在任意平台、任意权限下都应 PASS。
```

- [ ] **Step 4: 验证主仓库门禁未被 spike 破坏**

Run:

```bash
cd /opt/app/workspace/TunnelMesh
git status --porcelain go.mod go.sum
go build ./... && go vet ./... && go test ./... -count=1
git diff --check
```

Expected: `git status` 对 `go.mod`/`go.sum` 无输出；三条 Go 命令全部退出码 0；`git diff --check` 无输出。

- [ ] **Step 5: 重新生成文档索引**

Run: `python3 scripts/gen_doc_index.py && git diff --stat docs/superpowers/plans/README.md`
Expected: `REPORT.md` 不在索引范围内（它不在 `docs/` 下），因此索引应无变化；若有变化说明误把记录文件放进了 `docs/`，需修正位置。

- [ ] **Step 6: Commit**

```bash
git add test/spike/vpn-task0
git commit -m "docs(spike): record vpn task 0 feasibility report"
```

---

## 回滚注意事项

- 本计划只新增 `test/spike/vpn-task0/` 下的文件，**不修改任何生产代码、配置、`AGENTS.md` 或 `docs/` 下的活文档**。回滚即 `git revert` 对应提交，无运行时影响。
- spike 模块使用独立 `go.mod`，因此不会改变主模块依赖图，也不会影响 `scripts/build-release.sh` 的产物。若发现主模块 `go.mod`/`go.sum` 出现改动，说明隔离失败，必须先修正再继续（Task 1 Step 5 与 Task 6 Step 4 是这道门禁）。
- Task 5 Step 4 会修改宿主机的 `net.ipv4.ping_group_range`。这是一次性 sysctl 且**不持久**（重启后回到默认 `1 0`）。若在共享主机上执行，验证完成后应恢复原值：`sudo sysctl -w net.ipv4.ping_group_range="1 0"`，并在报告中记录已恢复。
- 若 Task 1 触发中止判据，本计划的全部产出仅为 spike 代码与一份 FAIL 报告；此时 spec §2.2 要求上报用户并重新决策，**阶段 1 的文档改写不得开工**，否则仓库会声称支持尚不存在的能力。
- 本计划执行完成后，spike 目录**保留**在仓库中（不删除），以便阶段 3-6 的实现者随时复现结论；它不参与 `go test ./...`，也不进入发布产物。

---

## 补记计划（2026-09-19 执行后追加，只记录已验证事实）

本节依据 `docs/development/documentation.md` 的"时点记录不可改写"条款追加：计划正文原样保留，
执行中发现的偏差与实测结论只在此追加，不回改上文。

### 执行结果

| Task | 结论 | 提交 |
| --- | --- | --- |
| Task 1 | PASS：gVisor 钉定生效，`go run ./deps` 与 2 个测试全通过，主模块 `go.mod`/`go.sum` 无改动 | `58deb92` |
| Task 2 | PASS：任意目标 TCP 终结成立，连续 7 次运行输出逐字节一致 | `589a818` |
| Task 3 | PASS：UDP 消费与 `WritePackets` 回程成立，连续 3 次运行一致 | `00fcde6` |
| Task 4 | PASS：内存 TUN 上的 WireGuard 握手 + TCP 往返成立，连续 3 次运行通过 | `bfd9b14` |
| Task 5 | **未在 Linux 执行**（本环境 darwin/arm64，无 Linux 主机与任何容器运行时）；代码级验证完成：`go vet`、`GOOS=linux go vet`、linux/amd64+arm64 交叉编译、`gofmt`、与计划代码块 `diff` 为空 | `c87a72f` |
| Task 6 | 完成：`REPORT.md` 定稿，依赖影响数据采集完毕 | 见本次提交 |

执行环境：darwin/arm64，macOS 14.6（Darwin 23.6.0），非容器，`id -u` = 501，`/dev/net/tun` 不存在。
无任何 Task 触发 spec §2.2 中止判据，netstack 方案存活。

依赖影响实测：spike 模块依赖 96 条；`go list -deps` 包数 tcpintercept=169、udpintercept=162、
wgbridge=203；gVisor module cache 102M；TCP 探针二进制 6,454,034 B；当前 `tunnelmesh-server`
二进制 39,442,898 B。

### 执行中对探针代码的修正（与本计划上文代码块的偏差）

上文代码块在沙箱中验证时遗留了三处缺陷，执行阶段发现并修正。**修正后的仓库代码是权威版本**，
上文代码块保留原样以维持时点记录：

1. `udpintercept/main.go`：删除声明但从未使用的 `icmpGenerated` 变量（死状态，且它的名字长度导致
   `var` 块对齐被 `gofmt` 判为不合规）；改为新增一条**有实义的正向断言**——
   `ip.TransportProtocol() != udp.ProtocolNumber` 即失败。这把 Task 3 正文原本只是"声称"的
   "栈不会回 ICMP port-unreachable"变成了被断言的事实（实测回程包 IP proto = 17）。
2. `wgbridge/main.go`：原文的 `CAP_NET_ADMIN=none` 是 `Printf` 里的**硬编码字面量，不是测量值**，
   在 macOS 上会被误读为已取证。改为 `capNetAdmin()`：Linux 上读 `/proc/self/status` 的 `CapEff`
   并测试第 12 位（命中则输出 `PRESENT ... -- the claim is NOT evidenced`），非 Linux 如实输出
   `n/a (only Linux exposes a capability mask)`。同时把手写的 `splitLines`/`cutPrefix` 换成
   `strings.Split`/`strings.CutPrefix` + `strconv.Atoi`。
3. `tcpintercept/main.go`、`udpintercept/main.go`、`wgbridge/main.go` 三个文件执行 `gofmt -w`
   （仅空白对齐，`gofmt -d` 确认零语义变更）。`deps/`、`icmpsock/` 本就合规。

修正后复验：`gofmt -l .` 无输出；`go vet ./...` 与 `GOOS=linux go vet ./...` 均干净；
五个探针全部按预期 PASS/SKIP。

### 执行阶段新增、须回流阶段 1 的实测发现

已写入 `test/spike/vpn-task0/REPORT.md` 的"必须回流到阶段 1 的设计修正"第 7-9 项：

7. **每个新四元组的首个 SYN 必然被丢弃**（钩子只在 demux 未命中时触发，而监听器注册是异步的），
   因此生产建连延迟包含一次客户端 RTO（Linux 初始 RTO 1s）。`vpn_flows.go` 必须预算或规避。
   此项影响用户可感知的首连延迟，须在阶段 4 设计中明确取舍。
8. **Server 侧特权主张尚未在 Linux 上取证**：macOS 上"`/dev/net/tun` 不存在"平凡为真，
   `CAP_NET_ADMIN` 亦无法测量。必须在非 root、`--cap-drop=ALL` 的 Linux 容器中复跑 Task 2-4，
   把 `grep Cap /proc/self/status` 实际值回填报告，方可对外声称特权主张已取证。
9. **Task 5 待补跑**，命令已原样写入 `REPORT.md`；阶段 7（Agent ICMP）开工前必须关闭。

第 7-9 项构成阶段 7 与"特权主张对外表述"的开工门禁，但**不阻塞**阶段 1-6 的 Server 侧工作。

### 流程记录

- 按 `superpowers:subagent-driven-development` 执行：Task 1 单独派发（关键路径阻塞项），
  Task 2-5 在 `go.mod` 就位后并行派发（四个独立包目录，无文件重叠）。
- 并行派发时**提交动作由控制方按序执行**，实现者不碰 git，以规避同一工作树内并发
  `git add`/`git commit` 争用索引。
- 后端拒绝模型覆盖（`gpt-5.6-luna` 返回"模型不存在"），故所有子代理继承会话模型；
  据此将独立评审集中在四个探针任务与最终整分支评审，Task 1（纯依赖钉定与转写）由控制方
  逐字节核验替代。以上均记录于 `.superpowers/sdd/2026-09-19-vpn-task0-feasibility/progress.md`。

---

## 补记计划（2026-09-19 最终评审后追加第二轮，只记录已验证事实）

本节同样依据 `docs/development/documentation.md` 的"时点记录不可改写"条款追加：计划正文与第一轮
补记原样保留，本轮修正与新增实测只在此追加。凡与第一轮补记冲突的数字，以本节为准。

整分支评审范围为 `6d3ddec..9c8515d`（评审 diff 存于
`.superpowers/sdd/2026-09-19-vpn-task0-feasibility/review-6d3ddec..9c8515d.diff`），
结论 **APPROVED WITH MINOR FINDINGS**：1 项 Important + 7 项 Minor。8 项发现逐条独立复核均为真，
全部已修复并复验；未触发 spec §2.2 中止判据，Task 1-4 的 PASS 结论不变。

### 评审发现与本轮修正

1. **[Important] `REPORT.md` Task 4 的 grep 证据块是照抄计划 Expected，未复跑。** 计划 Expected 为
   行 1/41/131/132，第一轮补记修正 2 之后真实命中已下移为 **1/43/137/138**。命中数一致（4 处），
   `CreateTUN` 与 `os.OpenFile` 仍为 0 命中，结论不变；但"与计划 Expected 完全一致"的表述不成立。
   已就地更正为真实输出，并写明行号下移的原因（`capNetAdmin()` 的 import/注释 + 本轮 `OK` 行注释）。
2. **[Minor] Task 4 的 `OK` 行称 server 监听 `127.0.0.1:<port>`，实为通配绑定。**
   `conn.NewDefaultBind()` 走 `ListenPacket(ctx, network, ":"+port)`，绑定所有接口；`127.0.0.1`
   只是告知客户端去拨的 endpoint。输出行已改写为 `server bound to UDP :<port> (wildcard),
   client endpoint 127.0.0.1:<port>`，并把"VPN 数据面持有一个通配 UDP 端口、暴露面由部署方负责"
   列为阶段 1 部署文档必须承载的事实。
3. **[Minor] Task 3 "无 ICMP port-unreachable" 的论据不成立。** 原注释称"出向队列按序排空，
   所以 ICMP 若存在就会是这一包"，但回包由 handler 内的 goroutine 写出，与假想 ICMP 的先后无序。
   改为 `drainFor(ep, 300ms)` 正向证明回包之后再无任何包出向，并新增 `icmpv4ProtocolNumber = 1`
   常量，出现 IP proto 1 直接 FAIL。输出行相应改写为 "nothing else egressed in the following 300ms"。
4. **[Minor] `egressOf` 超时路径会 panic 而非报告。** 它在超时时关闭 channel，向 `select` 投递
   nil slice，`header.IPv4(nil)` 随即 panic，把预期诊断换成 goroutine dump——而"出向队列为空"
   恰是本探针要防的回归。内层超时改为 2s（严格小于外层 3s），并显式判空走同一条 FAIL 分支。
5. **[Minor] Task 2 的 `OK` 行在校验 SYN 标志之前就断言收到 SYN-ACK。** RST-ACK 同样携带
   `ack=clientISN+1` 且四元组相同。已加 `flags&header.TCPFlagSyn == 0 → FAIL`；修正后复跑输出
   逐字未变，说明原结论本就正确，缺的只是证据强度。
6. **[Minor] `deps/deps_test.go` 的 `TestGoVersionIsHighEnoughForGVisor` 测的是本机工具链，
   不是本 Task 的任何设计主张**，且 `go.mod` 的 `go 1.26.3` 指令已由工具链强制。已删除该测试
   （连带清理 `runtime`/`strings` import）。**第一轮补记中"2 个测试全通过"的记录随之过期：
   现为 1 个测试 `TestPinnedPackagesAreImportable`。**
7. **[Minor] `REPORT.md` Task 6 的 "gVisor 模块体积 102M" 是本机 `gvisor.dev/` 目录合计值。**
   被钉死的 revision 实占 **21M**，另 81M 是撰写计划时实验过、已被否决的 `@latest`
   （`v0.0.0-20260919054224-26f3455a4cb9`）留下的本地残留，与本方案无关。**第一轮补记中的同一
   数字随之过期。**
8. **[Minor] FAIL 诊断直接打印 `captured` 全量。** 回归场景下该切片无界（实测 339019 条），
   诊断输出反而不可读。已加 `summarizeCaptured()`：最多打印 3 条 + 总条数。

`go.sum` 本轮新增 50 行，全部是 `/go.mod h1:` 哈希（闭合模块图所需），`go.mod` 的 require 版本
一条未变；主模块 `go.mod`/`go.sum` 依旧零改动（`git status --porcelain go.mod go.sum` 为空）。

### 本轮复验与回归证据

- 五个探针复跑：`deps` PASS、`tcpintercept` PASS、`udpintercept` PASS、`wgbridge` PASS、
  `icmpsock` SKIP（darwin 上无效，不构成 PASS）；`go test ./deps -count=1` → `ok ... 0.407s`。
- `gofmt -l .` 无输出；`go vet ./...` 与 `GOOS=linux go vet ./...` 均干净；
  仓库根 `go build ./...` 与 `go vet ./...` 通过。
- 防回归证明（在 `mktemp -d` 副本中改代码复跑，**未入库**）：把 `emitReply` 的 `WritePackets`
  换成 `InjectInbound`，探针输出 `FAIL: no reply egressed; handlerCalls=339019 captured=[...]
  ... (339019 entries total)`、`exit status 1`、**0 次 panic**、0 段 goroutine dump。
  即第 3、4、8 项修正确实生效，且"用 `InjectInbound` 发回程"的错误写法不会静默通过。
- 依赖影响数据复测未变：模块 96 条，`go list -deps` tcp=169 / udp=162 / wg=203，
  TCP 探针二进制 6,454,034 B，`tunnelmesh-server` 39,442,898 B。

### 本轮新增实测（已写入 `REPORT.md` 回流清单第 3、7 项）

以下两项均用一次性 scratch 程序在 `mktemp -d` 副本中实测，**未入库**：

- **同一 (addr,port) 的并发注册语义。** 32 个 goroutine 抢一个全新 tuple（198.51.100.9:9090），
  连续 3 次结果完全一致：`AddProtocolAddress` 恰好 1 个成功、31 个返回
  `*tcpip.ErrDuplicateAddress`（`duplicate address`）；`ListenTCP` 恰好 1 个成功、31 个返回
  `bind tcp 198.51.100.9:9090: port is in use`。→ 阶段 4 的钩子必须按 (addr,port) 做
  singleflight/去重，把这两个错误当作"别的流已建好"**成功**返回；失败方绝不能 `RemoveAddress`
  或关闭监听器，否则会拆掉赢家的地址与监听器、中断正在服务的流。另：`tcpip.Error` 不实现
  `error`，`errors.As` 无法编译（vet 直接报错），判别只能用类型断言——与回流清单第 5 项一致。
- **`channel.Endpoint.WritePackets` 不做 MTU 校验。** 把 2028 字节的 IPv4+UDP 包写到 MTU=1420 的
  链路端点，返回 `n=1 err=<nil>` 并原样出队（源码 `pkg/tcpip/link/channel/channel.go:278-291`
  只入队，唯一错误路径是 `ErrNoBufferSpace`）。→ spec 已规定超限 UDP 直接丢弃，网关必须自己执行，
  不能指望链路层兜底。

### 评审顺带发现的仓库既有问题（不在本分支修复）

四项均早于本分支、不属于 Task 0 范围，已记入 `REPORT.md` 同名小节，此处只作索引：
CI 缺 Go 门禁（`ci.yml` 在 `setup-go` 之后没有任何 `go build`/`go vet`/`go test`）；
`Dockerfile` 第 3 行 `ARG GO_VERSION=1.23` 落后于 `go.mod` 的 `go 1.26.0`；
`.dockerignore` 未排除 `test/`；根模块路径 `github.com/tunnelmesh/tunnelmesh` 与远端
`nnworld/TunnelMesh` 不一致。

### 门禁状态（未变）

- Task 1-4 PASS，Server 侧设计成立，可进入 spec §15 阶段 1。
- Task 5 仍为**未在 Linux 执行**；回流清单第 8 项（非 root、`--cap-drop=ALL` 的 Linux 容器复跑
  Task 2-4 并回填 capability 实测值）仍未完成。二者依旧是阶段 7（Agent ICMP）与"特权主张"
  对外表述的开工门禁，不阻塞阶段 1-6。
