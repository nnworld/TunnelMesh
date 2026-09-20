# VPN 网关 阶段 1：约束反转与 ADR 0002 Implementation Plan

- 日期：2026-09-20
- 状态：**待用户确认**。按 `AGENTS.md`「Plan 与 PR 要求」，未获明确确认前不得开始实现、不得修改任何活文档。
- 规格：[内嵌 VPN 网关（WireGuard）设计](../specs/2026-09-19-embedded-vpn-gateway-design.md) §15 阶段 1、§16 文档交付、§17 已决策记录
- 前置证据：[VPN 网关 Task 0 可行性验证 Implementation Plan](2026-09-19-vpn-task0-feasibility.md) 与 `test/spike/vpn-task0/REPORT.md`
  （Task 1-4 全 PASS、无 FAIL、未触发规格 §2.2 回退；Task 5 与非 root Linux capability 取证仍是阶段 7 的门禁，不阻塞本阶段）
- 分支：`codex/vpn-phase1-constraint-reversal`，stacked 在 `codex/vpn-task0-feasibility-plan`（`371556e`）之上
- 交付物：ADR 0002、`AGENTS.md` 两处修订、21 个活文档的 29 处主张改写、一个长期防回归的文档主张守卫测试

## 目标

解除「公网入口只使用 HTTP/HTTPS/WebSocket、Server 不监听公网 UDP、继续不实现 ICMP 与 TUN/L2 VPN」
这组工程约束，使规格 §15 的阶段 3-8 可以合法落地；同时保证**没有任何活文档宣称尚未发布的能力**。

本阶段不写一行产品代码，不改 Schema、不改 API、不改配置、不改前端。它的产物是：
一个决策载体（ADR 0002）、一份修订后的工程规范（`AGENTS.md`）、一套改写后的活文档，
以及一个把「约束反转」变成长效门禁的测试。

非目标（本阶段明确不做）：

- 不实现 `internal/vpn/`、数据面、管理 API、Schema v15、前端页面（阶段 3-6）。
- 不新建 `docs/user-guide/vpn.md`、`docs/deployment/vpn-gateway.md`、`docs/operations/vpn.md`：
  这三份是**能力文档**，必须与实现同批交付（阶段 4/6/8），提前建会产生只有骨架的孤儿文档。
- 不改 `docs/api/openapi.yaml`、`docs/operations/configuration.md`、`docs/operations/config-examples.md`、
  `deploy/README.md`、`deploy/grafana/README.md`：规格 §16 把它们列在整个特性的交付清单里，
  但它们描述的是尚不存在的配置键与接口，属于阶段 4/6/8。
- 不动 `.dockerignore`、根模块路径、CI 门禁（后者由 `codex/ci-go-gate-and-docker-go-version` 单独处理）。
- 不改写任何时点记录（`docs/superpowers/**`、`docs/pull-requests/**`、已接受的 ADR）。

## 技术栈

Go 标准库（`os`、`io/fs`、`path/filepath`、`strings`、`testing`）用于守卫测试；其余全部是 Markdown。
不引入任何新依赖，不改 `go.mod`/`go.sum`。

## Global Constraints

1. **不得虚假承诺。** 阶段 1 之后，任何活文档都不得写成"已支持 VPN / 已支持 ping 内网"。统一措辞是
   "已批准、分阶段实施中、当前版本尚未提供"，并链接 ADR 0002。能力文档随阶段 4/6/8 的实现一起改写。
2. **时点记录不可改写。** `docs/development/documentation.md`「时点记录不可改写」条款适用于
   `docs/superpowers/**`、`docs/pull-requests/**` 与已接受的 ADR。历史上写下的"不支持公网 UDP"
   等表述**保持原样**，反转只由 ADR 0002 承载。守卫测试据此排除这三类目录。
3. **只反转全局主张，保留仍然为真的局部主张。** 例如"`publish` 不能把 `tcp`/`udp` 作为公网监听协议"、
   "tp-* 代理入口不新增公网监听端口"、"托管路由只需暴露 80/443"在 VPN 落地后依然为真，
   必须**限定范围**而不是删除。每处的分类见「精确文件清单」。
4. **ADR 0002 是唯一的决策载体。** 其它文档只引用它，不复述决策理由，避免多份真相漂移
   （`AGENTS.md`「单一数据源」）。
5. **索引由脚本生成。** 新增 ADR 与 PR 记录后必须执行 `python3 scripts/gen_doc_index.py`，
   不得手工编辑四个 `README.md` 索引。
6. **禁止占位内容。** 本计划不含 `TBD`/`TODO`；每个任务都能独立验证与评审。
7. **提交纪律。** 每个任务一次提交，格式 `<type>(<scope>): <subject>`，subject 祈使句、≤50 字符、不加句号。
   未经用户明确授权不执行 push/merge。

## 本计划的前置核实结论（撰写计划时已实测）

以下数字与清单是撰写本计划时在 `codex/vpn-task0-feasibility-plan`（`371556e`，Go 代码与 `main` 一致）
上实测得到的，不是估计值：

1. **违规主张的真实规模是 29 处、分布在 21 个文件**，规格 §16 估算的"12 处活文档改写"偏小。
   规格漏掉的有：`docs/en/deployment/production.md`、`docs/en/operations/security.md`、
   `docs/en/user-guide/sso-and-mfa.md`、`docs/user-guide/sso-and-mfa.md`、`docs/community/` 下 4 个文件、
   `README.md:390`、`README.zh-CN.md:379`、`docs/protocol/proxy-modules.md:50`。
   规格是时点记录，不回改；本计划以实测清单为准。
2. **24 条禁用短语共命中 41 次，落在 29 个唯一行上**（同一行常被中英文两条短语各命中一次）。
   守卫测试按**行**计数，因此红灯基线是 29，不是 41。
3. **`internal/server/web_dist` 不入库**，且 `internal/server/web_test.go:80` 断言嵌入内容非空、
   `:106` 断言 `index.html` 可读。因此任何跑 `go test ./...` 的验证都必须先 `cd web && npm run build`；
   本阶段的守卫测试放在 `scripts` 包，`go test ./scripts/` 不依赖前端产物。
4. **`scripts` 包已有同类守卫先例**：`scripts/install_script_test.go` 是 `package scripts`，
   以包目录为 CWD 读取 `install.sh` 并断言契约字符串。新测试沿用同一包与同一路径约定（`../` 回到仓库根）。
5. **ADR 现状**：`docs/architecture/adr/` 只有 `0001-scoped-service-tokens.md`（英文，`- Status: Accepted`，
   结构为 Context / Decision / Consequences / Rejected alternatives）与生成的 `README.md`。
   `adr/README.md` 的约定要求：编号单调递增永不复用、复制现有 ADR 结构、正文链接触发决策的计划或规格、
   Status 取值 `Proposed`/`Accepted`/`Deprecated`/`Superseded by NNNN`。
6. **索引脚本的关联规则**：`scripts/gen_doc_index.py` 用文件名 slug 与正文中
   `(plans|specs|pull-requests|adr)/<file>.md` 形式的引用建立交叉引用。因此本计划正文已链接规格，
   阶段末的 PR 记录必须链接本计划与规格，否则索引只能靠 slug 猜测。
7. **`docs/README.md:82` 已链接 ADR 索引**，ADR 0002 生成索引后自动可达，不构成孤儿文档；
   `docs/README.md` 本阶段只需要改写第 100 行的入口主张。
8. **`go test -race ./...` 当前全绿**（本机 wall 约 10 分钟，`internal/server` 441s），
   即本阶段不会继承既有红灯。

## 架构决策

### D1：约束反转由 ADR 0002 承载，活文档只改主张、不复述理由

`AGENTS.md`「重大架构变更写 ADR」。ADR 0002 记录：被解除的约束原文、解除理由、Agent 特权边界结论、
策略执行点留在 Server 的理由、规格 §2.2 回退方案下的特权接受理由、以及被保留的禁令（P2P、RCE）。
活文档统一以一句话 + 链接引用它。

### D2：ADR 0002 用英文撰写

`adr/README.md` 的新增步骤第 1 条是"复制现有 ADR 的结构"，唯一先例 `0001` 是英文；
ADR 系列保持同语言便于对照。`docs/development/documentation.md` 的中文默认规则针对面向用户的说明文档，
此处按目录内既有约定优先。**若用户要求中文，只需改 Task 2 的产出语言，其余任务不受影响。**

### D3：活文档采用「现状 + 已批准方向」双句写法

删除绝对禁令，替换为两句：第一句陈述当前版本真实能力，第二句说明 VPN 网关已按 ADR 0002 批准、
分阶段实施、尚未发布。这样既解除实现前提，又不产生虚假承诺；阶段 8 交付时再把第二句改写为能力说明。

### D4：`AGENTS.md:148` 部分反转，不整行删除

按规格 §17 的决策：解除 TUN/L2 VPN，把 ICMP 改写为"不主动构造 ICMP；ICMP echo 经 Agent 非特权
ping socket 支持"，**保留** P2P NAT traversal 与任意远程命令执行两条禁令（与本目标正交且纯风险）。
同时补一条 VPN 数据面的实现约束（进程内 WireGuard + 内存态 TUN、无 `/dev/net/tun`、无 `CAP_NET_ADMIN`、
不转发 L2 帧、`//go:build vpn` 隔离、`enabled: false` 时不创建资源），使后续阶段的实现有规范可依。

### D5：守卫测试放在 `scripts` 包，用禁用短语清单实现

选项对比：

- `scripts/doc_claims_test.go`（**选定**）：沿用 `install_script_test.go` 的既有约定，
  随 `go test ./...` 一起跑，无需新增 CI 步骤；以 `../` 访问仓库根。
- `docs/` 下新建 Go 包：`docs/` 目前没有任何 `.go` 文件，为一个测试新建包会引入 `doc.go` 与
  目录职责变更，成本高于收益。
- 独立的 shell/python 检查脚本：不会随 `go test ./...` 执行，容易变成没人跑的摆设。

实现用**精确子串匹配**而不是语义判断：脆弱是刻意的，主张一旦回潮就红灯。代价是换一种说法就绕过去，
因此守卫测试是补充而不是替代评审；这一点写进测试注释与 PR 记录。

### D6：`docs/architecture/overview.md` 补一张标注为"实施中"的链路图

规格 §16 要求 overview 含新增 ASCII 链路图。图直接取自规格 §3 的架构总览（不新画，避免两份图漂移），
放在一个新的小节里，标题与首句都写明"已批准、实施中、当前版本未提供"。

## 精确文件清单

### 新增（3 个）

| 文件 | 任务 | 内容 |
| --- | --- | --- |
| `scripts/doc_claims_test.go` | Task 1 | 文档主张守卫测试（完整源码见 Task 1） |
| `docs/architecture/adr/0002-public-ingress-and-embedded-vpn.md` | Task 2 | 决策载体，`- Status: Accepted` |
| `docs/pull-requests/2026-09-20-vpn-phase1-constraint-reversal.md` | Task 8 | 本阶段的 PR 记录 |

### 修改：禁用主张清单（29 处，按任务分组）

分类含义：**反转** = 该主张整体作废，改写为「现状 + 已批准方向」；
**限定** = 主张对某个具体功能仍为真，加范围限定词并去掉全局口吻；
**部分反转** = 一句话里既有作废主张又有仍然为真的主张，拆开处理。

Task 3（`AGENTS.md`，2 处）：

| 位置 | 现状 | 分类 |
| --- | --- | --- |
| `AGENTS.md:11` | 公网入口默认只使用 HTTP/HTTPS/WebSocket；公网 UDP 不作为服务端监听能力 | 反转 |
| `AGENTS.md:148` | 继续不实现：ICMP、TUN/L2 VPN、P2P NAT traversal、任意远程命令执行 | 部分反转（D4） |

Task 4（双语 README，5 处）：

| 位置 | 现状 | 分类 |
| --- | --- | --- |
| `README.md:15` | Public ingress is HTTP/HTTPS/WSS only — the Server never listens for public UDP. | 反转 |
| `README.md:390` | Deliberately not implemented: ICMP, TUN/L2 VPN, P2P NAT traversal, and arbitrary remote command execution | 部分反转 |
| `README.zh-CN.md:14` | 公网入口只使用 HTTP/HTTPS/WSS， | 反转 |
| `README.zh-CN.md:15` | Server 不监听公网 UDP。 | 反转（与 :14 同一句，合并改写） |
| `README.zh-CN.md:379` | 明确不实现：ICMP、TUN/L2 VPN、P2P NAT traversal 和任意远程命令执行 | 部分反转 |

Task 5（中文活文档，13 处 / 10 个文件）：

| 位置 | 现状 | 分类 |
| --- | --- | --- |
| `docs/architecture/overview.md:24` | 公网模式只需要 HTTP/HTTPS/WSS 入口；公网 UDP 不作为 Server 监听能力（同句后半的 tp-* 描述仍为真） | 部分反转 + 新增 D6 小节 |
| `docs/protocol/proxy-modules.md:3` | TunnelMesh 的公网入口继续只监听 HTTP/HTTPS/WebSocket | 限定（限定为"代理模块的公网入口"） |
| `docs/protocol/proxy-modules.md:50` | 代理模块不会执行任意远程命令，也不实现 ICMP、TUN/L2 VPN 或 P2P NAT traversal | 部分反转（保留 RCE 与 P2P，ICMP/TUN 指向 VPN 网关） |
| `docs/deployment/nginx.md:194` | 泛域名只解决 HTTP/HTTPS/WSS 路由，不提供公网 UDP 监听 | 限定（nginx 泛域名确实不解决 UDP；补一句 VPN 端口不经 nginx、需单独放行） |
| `docs/deployment/openresty-proxy-entry.md:11` | 公网入口只允许 HTTP/HTTPS/WebSocket | 限定（限定为 tp-* 代理入口） |
| `docs/operations/network-probes.md:74` | 公网 UDP 不由 Server 监听；UDP 探针从 Agent 所在网络发起 | 部分反转（探针语义不变，删掉全局主张） |
| `docs/user-guide/client.md:48` | 公网 Server 入口不开放公网 UDP；UDP 仅支持通过 `forward udp` 从用户侧发起 | 限定（限定为 Client 能力；`forward udp` 描述保留） |
| `docs/user-guide/client.md:74` | 公网入口不直接监听 UDP；需要公网 UDP 时应放置 UDP 网关 | 限定（同上，并把"另一种形态见 VPN 网关"作为指引） |
| `docs/user-guide/client.md:446` | Server 公网入口不新增 SOCKS5 监听，公网仍只提供 HTTP/HTTPS/WebSocket | 部分反转（SOCKS5 结论保留，删掉后半句全局主张） |
| `docs/user-guide/http-proxy-entry.md:170` | 不支持公网 UDP 入口。公网侧只有 HTTP/HTTPS/WebSocket | 限定（限定为 tp-* 代理入口） |
| `docs/user-guide/managed-http-route.md:71` | 动态 wildcard 只适用于 HTTP/HTTPS/WebSocket 入口，不提供公网 UDP | 限定（限定为动态 wildcard） |
| `docs/user-guide/server-admin.md:105` | 公网 Server 仍只暴露 80/443，不提供公网 UDP | 限定（限定为动态域名功能） |
| `docs/user-guide/sso-and-mfa.md:507` | 以下能力刻意不在本期范围内：ICMP、TUN/L2 VPN、P2P NAT 穿透、任意远程命令执行 | 部分反转 |

Task 6（英文活文档，3 处）：

| 位置 | 现状 | 分类 |
| --- | --- | --- |
| `docs/en/deployment/production.md:19` | Public ingress supports only HTTP, HTTPS, and WebSocket over TLS/WSS. Public UDP is not exposed by the Server. | 反转 |
| `docs/en/operations/security.md:12` | Public ingress is HTTP/HTTPS/WSS only. The Server does not expose public UDP. | 反转 |
| `docs/en/user-guide/sso-and-mfa.md:569` | deliberately not implemented: ICMP, TUN/L2 VPN, P2P NAT traversal, ... | 部分反转 |

Task 7（社区与增长文档，5 处）：

| 位置 | 现状 | 分类 |
| --- | --- | --- |
| `docs/community/comparison.md:13` | 表格行 `Public ingress \| HTTP/HTTPS/WSS only` | 反转（表格单元格，措辞需短） |
| `docs/community/comparison.md:54` | intentionally does not implement ICMP, TUN/L2 VPN, ...；public Server ingress is HTTP/HTTPS/WSS only；UDP ... not as a public Server listener | 部分反转（一行内三处主张） |
| `docs/community/roadmap.md:42` | Explicitly not planned: ICMP, TUN/L2 VPN, P2P NAT traversal, and arbitrary remote command execution | 部分反转（VPN 与 ICMP echo 从"not planned"移入"进行中"，P2P/RCE 保留） |
| `docs/community/distribution-plan.md:55` | 社交文案 `Public ingress is HTTP/HTTPS/WSS only.` | 反转（文案长度敏感，改写后仍需是一句可直接发布的话） |
| `docs/community/why-tunnelmesh-needs-a-control-plane.md:43` | Public ingress is HTTP/HTTPS/WSS only, and the Server does not expose public UDP | 反转 |

Task 8（索引与登记，1 处 + 生成物）：

| 位置 | 现状 | 分类 |
| --- | --- | --- |
| `docs/README.md:100` | 公网入口只使用 HTTP/HTTPS/WSS，Server 不监听公网 UDP | 反转 |
| `docs/architecture/adr/README.md`、`docs/pull-requests/README.md`、`docs/superpowers/plans/README.md`、`docs/superpowers/specs/README.md` | — | 由 `gen_doc_index.py` 生成，禁止手工编辑 |

### 明确保留、不改写的位置（评审时需确认判断正确）

| 位置 | 为什么仍然为真 |
| --- | --- |
| `README.md:72`、`README.zh-CN.md:71`（组件表的 "HTTP/HTTPS/WSS ingress"） | 描述 Server 现有职责，没有 "only" 绝对口吻；VPN 端点在阶段 6 落地时随组件表一起补 |
| `docs/user-guide/client.md:44`（`publish http` → HTTP/HTTPS/WebSocket 表格行） | `publish` 的能力边界与 VPN 无关 |
| `docs/user-guide/client.md:117`（不能把 `tcp`/`udp` 作为 `publish` 的公网监听协议） | 同上，VPN 网关不经 `publish` |
| `docs/user-guide/managed-http-route.md:3`（托管路由只需暴露 80/443） | 对托管路由仍为真；VPN 是独立入口 |
| `docs/architecture/overview.md:24` 后半句（tp-* 不新增公网监听端口，与 443 由 SNI 分流） | 对 tp-* 仍为真 |
| 全部 `docs/superpowers/**`、`docs/pull-requests/**`、`docs/architecture/adr/0001-*` | 时点记录不可改写（Global Constraint 2） |

## 任务分解

### Task 1: 文档主张守卫测试（红灯先行）

**Files:** 新增 `scripts/doc_claims_test.go`

- [ ] **Step 1: 写测试**

完整源码（`package scripts`，与 `install_script_test.go` 同包同 CWD 约定）：

```go
package scripts

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reversedClaim is one absolute statement that ADR 0002 retires. Matching is
// plain substring search on purpose: the brittleness is the feature, so a
// reintroduced claim fails the build instead of waiting for a reader to notice.
// It cannot catch the same claim in new words, so this test supplements review
// rather than replacing it.
type reversedClaim struct {
	phrase string
	why    string
}

var reversedClaims = []reversedClaim{
	{"never listens for public UDP", "ADR 0002 允许 VPN 网关持有一个公网 UDP 端口"},
	{"does not expose public UDP", "同上"},
	{"Public UDP is not exposed", "同上"},
	{"not as a public Server listener", "UDP 已成为 Server 侧公网监听能力之一"},
	{"不监听公网 UDP", "ADR 0002 允许 VPN 网关持有一个公网 UDP 端口"},
	{"公网 UDP 不作为", "同上"},
	{"不提供公网 UDP", "必须限定到具体功能，不得作为全局主张"},
	{"不支持公网 UDP", "同上"},
	{"不开放公网 UDP", "同上"},
	{"不直接监听 UDP", "同上"},
	{"公网 UDP 不由 Server 监听", "同上"},
	{"HTTP/HTTPS/WSS only", "公网入口不再只有 HTTP/HTTPS/WSS"},
	{"only HTTP, HTTPS, and WebSocket", "同上"},
	{"只使用 HTTP/HTTPS/WSS", "同上"},
	{"只使用 HTTP/HTTPS/WebSocket", "同上"},
	{"只监听 HTTP/HTTPS/WebSocket", "必须限定到代理模块，不得作为全局主张"},
	{"只需要 HTTP/HTTPS/WSS 入口", "公网入口不再只有 HTTP/HTTPS/WSS"},
	{"只允许 HTTP/HTTPS/WebSocket", "必须限定到 tp-* 代理入口"},
	{"公网仍只提供 HTTP/HTTPS/WebSocket", "公网入口不再只有 HTTP/HTTPS/WebSocket"},
	{"公网侧只有 HTTP/HTTPS/WebSocket", "必须限定到 tp-* 代理入口"},
	{"ICMP、TUN/L2 VPN", "ICMP echo 与内存态 TUN 已由 ADR 0002 批准；P2P 与 RCE 禁令保留"},
	{"ICMP, TUN/L2 VPN", "同上"},
	{"Explicitly not planned: ICMP", "ICMP echo 已进入路线图"},
	{"does not implement ICMP", "ICMP echo 已批准实施；改写时必须限定范围"},
}

// liveDocRoots describe current behaviour and must stay truthful. Record
// directories are excluded: plans, specs, PR records and accepted ADRs are
// point-in-time evidence and keep the wording they were written with
// (docs/development/documentation.md, 时点记录不可改写).
var liveDocRoots = []string{
	"../AGENTS.md",
	"../README.md",
	"../README.zh-CN.md",
	"../docs",
	"../deploy",
}

var immutableRecordDirs = []string{
	"docs/superpowers/",
	"docs/pull-requests/",
	"docs/architecture/adr/",
}

var skipDirs = map[string]bool{
	"node_modules": true,
	"web_dist":     true,
	".git":         true,
}

func isImmutableRecord(path string) bool {
	clean := strings.TrimPrefix(filepath.ToSlash(path), "../")
	for _, dir := range immutableRecordDirs {
		if strings.HasPrefix(clean, dir) {
			return true
		}
	}
	return false
}

// scanFile reports one violation per offending line, not per matched phrase: a
// single line often trips both the Chinese and the English pattern, and the
// baseline this guard was written against is 29 lines across 21 files.
func scanFile(t *testing.T, path string) int {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	violations := 0
	for i, line := range strings.Split(string(data), "\n") {
		var matched []string
		for _, claim := range reversedClaims {
			if strings.Contains(line, claim.phrase) {
				matched = append(matched, claim.phrase)
			}
		}
		if len(matched) == 0 {
			continue
		}
		violations++
		t.Errorf("%s:%d still asserts a constraint reversed by ADR 0002 %q\n\tline: %s",
			strings.TrimPrefix(filepath.ToSlash(path), "../"), i+1, matched, strings.TrimSpace(line))
	}
	return violations
}

func TestLiveDocsDoNotAssertReversedConstraints(t *testing.T) {
	total := 0
	for _, root := range liveDocRoots {
		info, err := os.Stat(root)
		if err != nil {
			t.Fatalf("stat %s: %v", root, err)
		}
		if !info.IsDir() {
			total += scanFile(t, root)
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".md") || isImmutableRecord(path) {
				return nil
			}
			total += scanFile(t, path)
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", root, walkErr)
		}
	}
	if total > 0 {
		t.Errorf("%d live-document line(s) across the repo still assert a reversed constraint", total)
	}
}

// TestADR0002RecordsTheConstraintReversal keeps the decision carrier honest:
// the reversal is only legitimate while an accepted ADR states it, links the
// design spec and the Task 0 evidence, and keeps the two bans that survive.
func TestADR0002RecordsTheConstraintReversal(t *testing.T) {
	const path = "../docs/architecture/adr/0002-public-ingress-and-embedded-vpn.md"

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ADR 0002 is the decision carrier for the constraint reversal: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"- Status: Accepted",
		"WireGuard",
		"netstack",
		"CAP_NET_ADMIN",
		"AGENTS.md",
		"P2P NAT traversal",
		"2026-09-19-embedded-vpn-gateway-design.md",
		"vpn-task0/REPORT.md",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ADR 0002 is missing required content %q", want)
		}
	}
}
```

- [ ] **Step 2: 确认红灯**

Run: `go test ./scripts/ -run 'TestLiveDocsDoNotAssertReversedConstraints|TestADR0002RecordsTheConstraintReversal' -count=1`

Expected（撰写计划时实测的基线）：

- `TestADR0002RecordsTheConstraintReversal` FAIL：
  `ADR 0002 is the decision carrier for the constraint reversal: open ../docs/architecture/adr/0002-public-ingress-and-embedded-vpn.md: no such file or directory`
- `TestLiveDocsDoNotAssertReversedConstraints` FAIL：逐条列出 **29** 处违规（21 个文件、41 条短语命中），
  末行为 `29 live-document line(s) across the repo still assert a reversed constraint`
- 退出码非 0

若实测违规数不是 29，说明清单已漂移：**先更新本计划的清单再动手改文档**，不得直接删短语让测试变绿。

- [ ] **Step 3: 确认守卫测试不误伤既有内容**

Run: `go test ./scripts/ -run TestInstallScriptContract -count=1` → PASS（同包既有测试不受影响）
Run: `gofmt -l scripts/` → 无输出；`go vet ./scripts/` → 干净

- [ ] **Step 4: Commit**

```bash
git add scripts/doc_claims_test.go
git commit -m "test(scripts): guard live docs against reversed claims"
```

### Task 2: ADR 0002

**Files:** 新增 `docs/architecture/adr/0002-public-ingress-and-embedded-vpn.md`；生成 `docs/architecture/adr/README.md`

- [ ] **Step 1: 写 ADR**

结构复制 `0001-scoped-service-tokens.md`（英文，D2）：

```markdown
# ADR 0002: Open a public UDP ingress for an embedded VPN gateway

- Status: Accepted
- Date: 2026-09-20

## Context
## Decision
## Consequences
## Rejected alternatives
```

正文必须覆盖（每一点都有出处，不得新增未经确认的决策）：

1. **Context**：`tp-*` 只覆盖 HTTP CONNECT 与绝对形式请求；原生 VPN 客户端用户今天必须安装
   `tunnelmesh-client`。被反转的约束原文引用两处：`AGENTS.md:11`（公网入口只使用
   HTTP/HTTPS/WebSocket、公网 UDP 不作为服务端监听能力）与 `AGENTS.md:148`
   （继续不实现 ICMP、TUN/L2 VPN、P2P NAT traversal、任意远程命令执行）。
   链接[设计规格](../specs/2026-09-19-embedded-vpn-gateway-design.md)与 Task 0 证据
   `test/spike/vpn-task0/REPORT.md`（守卫测试断言正文含 `vpn-task0/REPORT.md` 字样）。
2. **Decision**：
   - Server 进程内嵌 WireGuard 端点，持有**一个**公网 UDP 端口（默认 51820），不经 nginx/OpenResty；
     云安全组与主机防火墙需单独放行。
   - 数据面用 gVisor netstack + 内存态 `tun.Device`，**不打开 `/dev/net/tun`、不要求 `CAP_NET_ADMIN`**；
     证据是 Task 0 的 Task 1-4 全 PASS。
   - `AGENTS.md:148` 只解除 TUN/L2 VPN；ICMP 改写为"不主动构造 ICMP，ICMP echo 经 Agent 非特权
     ping socket 支持"；**保留** P2P NAT traversal 与任意远程命令执行两条禁令（守卫测试断言正文含
     `P2P NAT traversal`）。
   - 策略执行点留在 Server 用户态并包装 `routing.Policy`；Agent 不新增特权、不维护 nftables、
     不改内网路由（三条部署约束来自规格 §2.1，用户已确认）。
   - 重依赖用 `//go:build vpn` 隔离，发布产物提供带/不带 tag 两个变体；`server.vpn.enabled: false`
     时不创建任何 VPN 资源。
   - 不引入第四个二进制。
3. **Consequences**：
   - 公网暴露面新增一个 UDP 端口，握手洪泛与丢包分类必须有指标与告警（规格 §11、§13）。
   - 源地址不保留、仅 ICMP echo、不支持分片与 IPv6 数据面等能力边界必须在用户文档显式声明（规格 §4.3）。
   - 首连延迟包含一次客户端 RTO（Task 0 回流清单第 7 项），阶段 4 必须明确取舍。
   - 阶段 7（Agent ICMP）开工前必须关闭两项门禁：非 root Linux 容器复跑 Task 2-4 取证 capability、
     Task 5 在 Linux 补跑。**在关闭之前，"无需 root/无需 CAP_NET_ADMIN" 只能写成待取证主张。**
   - 活文档在能力交付前只写"已批准、实施中、尚未提供"（D3）。
4. **Rejected alternatives**：
   - 真实 `/dev/net/tun` + `CAP_NET_ADMIN`：进程级提权，CI 需特权，被 Task 0 的内存 TUN 结果否决；
     若规格 §2.2 的桥接前提被推翻则回退到此方案，并须在 systemd `AmbientCapabilities=CAP_NET_ADMIN`
     或 Docker `--cap-add=NET_ADMIN`（禁止 `--privileged`）下运行，接受理由必须补记进本 ADR。
   - 第四个特权二进制（独立 VPN 网关进程）：用户明确否决。
   - Agent 侧内核转发 + 路由注入：内网网关路由表无权修改、Agent 无法维护 nftables（规格 §2.1）。
   - OpenVPN / IPsec / Shadowsocks：协议面与代码面显著更大，目标未覆盖更多（规格 §17）。
   - 真端到端 L3 保留源地址：需要内网路由权限，已被部署约束排除。

- [ ] **Step 2: 重新生成 ADR 索引**

Run: `python3 scripts/gen_doc_index.py`
Expected: `docs/architecture/adr/README.md` 的清单出现 0002 行、Status 列为 `Accepted`；
其余三个索引无 diff。

- [ ] **Step 3: 确认 ADR 断言转绿**

Run: `go test ./scripts/ -run TestADR0002RecordsTheConstraintReversal -count=1` → PASS
Run: `go test ./scripts/ -run TestLiveDocsDoNotAssertReversedConstraints -count=1` → **仍 FAIL，仍是 29 处**
（ADR 目录被守卫测试排除，本任务不应改变违规数；若数字变化说明扫描范围写错了）

- [ ] **Step 4: Commit**

```bash
git add docs/architecture/adr/0002-public-ingress-and-embedded-vpn.md docs/architecture/adr/README.md
git commit -m "docs(adr): accept public udp ingress for vpn gateway"
```

### Task 3: `AGENTS.md` 约束修订

**Files:** `AGENTS.md`

- [ ] **Step 1: 改写第 11 行**

现状：

```
公网入口默认只使用 HTTP/HTTPS/WebSocket；公网 UDP 不作为服务端监听能力。内部转发支持 TCP、UDP、HTTP 和 WebSocket 场景。
```

改为（D3、D4；必须保留"内部转发"那半句）：

```
公网入口以 HTTP/HTTPS/WebSocket 为主；启用内嵌 VPN 网关（见 docs/architecture/adr/0002-public-ingress-and-embedded-vpn.md）时，Server 额外监听一个公网 UDP 端口作为 WireGuard 端点，该端口不经反向代理、不参与 HTTP 路由，必须独立放行、限流与监控。内部转发支持 TCP、UDP、HTTP 和 WebSocket 场景。
```

- [ ] **Step 2: 改写第 148 行**

现状：

```
- 继续不实现：ICMP、TUN/L2 VPN、P2P NAT traversal、任意远程命令执行。SSH 支持仅限现有 stdio/WebSocket 代理链路，不能扩展为通用命令执行 API。
```

改为两条（第一条保留仍然有效的禁令，第二条给出 VPN 数据面的实现约束）：

```
- 继续不实现：P2P NAT traversal、任意远程命令执行。SSH 支持仅限现有 stdio/WebSocket 代理链路，不能扩展为通用命令执行 API。
- VPN 数据面按 ADR 0002 实现：WireGuard 端点与内存态 TUN（gVisor netstack）运行在 tunnelmesh-server 进程内，不打开 /dev/net/tun、不要求 CAP_NET_ADMIN、不转发 L2 以太网帧；ICMP 只支持 echo（Agent 侧非特权 ping socket），Server 不主动构造其它 ICMP 类型；重依赖用 //go:build vpn 隔离，server.vpn.enabled 为 false 时不创建任何 VPN 资源。该能力分阶段实施，交付前活文档不得宣称可用。
```

- [ ] **Step 3: 确认本任务范围转绿**

Run: `go test ./scripts/ -run TestLiveDocsDoNotAssertReversedConstraints -count=1 2>&1 | grep -c '^AGENTS.md:'` → `0`
Run: 同一命令的总违规数从 29 降到 **27**

- [ ] **Step 4: Commit**

```bash
git add AGENTS.md
git commit -m "docs(agents): reverse the udp ingress and vpn bans"
```

### Task 4: 双语 README

**Files:** `README.md`、`README.zh-CN.md`

- [ ] **Step 1: `README.md:15`**

现状：`Public ingress is HTTP/HTTPS/WSS only — the Server never listens for public UDP.`
改为：`Public ingress is HTTP/HTTPS/WSS. An embedded WireGuard VPN gateway that adds one public UDP port is approved by ADR 0002 and landing in phases; it is not available yet.`

- [ ] **Step 2: `README.md:390`**

现状以 `Deliberately not implemented: ICMP, TUN/L2 VPN, P2P NAT traversal, and arbitrary remote command execution.` 开头。
改为保留 P2P 与 RCE 两条禁令，并把 ICMP/TUN 改写为已批准方向：
`Deliberately not implemented: P2P NAT traversal and arbitrary remote command execution. SSH support is limited to the existing stdio/WebSocket proxy path. ICMP echo over an embedded WireGuard gateway is approved by ADR 0002 and in progress; L2 frames are never forwarded.`
（后半句 SSH 限定必须原样保留。）

- [ ] **Step 3: `README.zh-CN.md:14-15`**

现状两行合成一句：`……可观测性管理。公网入口只使用 HTTP/HTTPS/WSS，\nServer 不监听公网 UDP。`
改为：`……可观测性管理。公网入口以 HTTP/HTTPS/WSS 为主；内嵌 WireGuard VPN 网关（额外一个公网 UDP 端口）已由 ADR 0002 批准并分阶段实施，当前版本尚未提供。`

- [ ] **Step 4: `README.zh-CN.md:379`**

与 Step 2 对应的中文版：保留 P2P 与任意远程命令执行、保留 SSH 限定句，
把 ICMP/TUN 改写为"ICMP echo 经内嵌 WireGuard 网关（ADR 0002）已批准、实施中；不转发 L2 帧"。

- [ ] **Step 5: 确认本任务范围转绿**

Run: `go test ./scripts/ -run TestLiveDocsDoNotAssertReversedConstraints -count=1 2>&1 | grep -cE '^README(\.zh-CN)?\.md:'` → `0`
总违规数从 27 降到 **22**

- [ ] **Step 6: Commit**

```bash
git add README.md README.zh-CN.md
git commit -m "docs(readme): state the approved vpn ingress direction"
```

### Task 5: 中文活文档（10 个文件、13 处）

**Files:** `docs/architecture/overview.md`、`docs/protocol/proxy-modules.md`、`docs/deployment/nginx.md`、
`docs/deployment/openresty-proxy-entry.md`、`docs/operations/network-probes.md`、`docs/user-guide/client.md`、
`docs/user-guide/http-proxy-entry.md`、`docs/user-guide/managed-http-route.md`、`docs/user-guide/server-admin.md`、
`docs/user-guide/sso-and-mfa.md`

每处的改写规则（分类见「精确文件清单」）：

- **限定类**（proxy-modules.md:3、nginx.md:194、openresty-proxy-entry.md:11、client.md:48、client.md:74、
  http-proxy-entry.md:170、managed-http-route.md:71、server-admin.md:105）：
  把主语从"公网入口/公网 Server"收窄到具体功能（代理模块、nginx 泛域名、tp-* 入口、`forward udp`、
  动态 wildcard、动态域名），删掉"只/不提供公网 UDP"这类全局口吻；确有必要时补一句
  "VPN 网关的公网 UDP 端口是独立入口，不经本功能，见 ADR 0002"。
  链接写法（从 `docs/<subdir>/` 出发）：`[ADR 0002](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)`。
- **部分反转类**（overview.md:24、proxy-modules.md:50、network-probes.md:74、client.md:446、
  sso-and-mfa.md:507）：拆句处理——作废的全局主张按 D3 改写，仍然为真的部分原样保留。
  `sso-and-mfa.md:507` 的清单里删掉 ICMP 与 TUN/L2 VPN、保留 P2P NAT 穿透与任意远程命令执行，
  并补一句"ICMP echo 与内嵌 VPN 网关见 ADR 0002（实施中）"。
- **overview.md 额外要求（D6）**：在第 24 行所在小节之后新增一个小节
  `## 内嵌 VPN 网关（已批准，实施中）`，首句写明"当前版本尚未提供，本节描述 ADR 0002 批准的目标架构"，
  然后原样复制规格 §3 的 ASCII 链路图（不得另画一张），并链接规格与 ADR。
  链接写法（从 `docs/architecture/` 出发）：`[ADR 0002](adr/0002-public-ingress-and-embedded-vpn.md)`、
  `[设计规格](../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md)`。

- [ ] **Step 1: 改写 13 处**
- [ ] **Step 2: 确认本任务范围转绿**

Run: `go test ./scripts/ -run TestLiveDocsDoNotAssertReversedConstraints -count=1 2>&1 | grep -cE '^docs/(architecture|protocol|deployment|operations|user-guide)/'` → `0`
总违规数从 22 降到 **9**（剩余：`docs/en/` 3 处、`docs/community/` 5 处、`docs/README.md` 1 处；最后一处归 Task 8）

- [ ] **Step 3: 链接可达性**

Run: 对新增的每个相对链接执行 `test -f`（命令见「整体验证」第 6 条），本任务新增链接 0 处失效
Run: `git diff --check` → 干净

- [ ] **Step 4: Commit**

```bash
git add docs/architecture/overview.md docs/protocol/proxy-modules.md docs/deployment/nginx.md \
        docs/deployment/openresty-proxy-entry.md docs/operations/network-probes.md \
        docs/user-guide/client.md docs/user-guide/http-proxy-entry.md \
        docs/user-guide/managed-http-route.md docs/user-guide/server-admin.md \
        docs/user-guide/sso-and-mfa.md
git commit -m "docs: scope ingress claims to the features they describe"
```

### Task 6: 英文活文档（3 个文件、3 处）

**Files:** `docs/en/deployment/production.md`、`docs/en/operations/security.md`、`docs/en/user-guide/sso-and-mfa.md`

- `production.md:19`：`Public ingress supports only HTTP, HTTPS, and WebSocket over TLS/WSS. Public UDP is not exposed by the Server.`
  → `Public ingress is HTTP, HTTPS, and WebSocket over TLS/WSS. An embedded WireGuard VPN gateway that adds one public UDP port is approved by ADR 0002 and landing in phases; it is not available in this release.`
- `security.md:12`：`- Public ingress is HTTP/HTTPS/WSS only. The Server does not expose public UDP.`
  → `- Public ingress is HTTP/HTTPS/WSS. The approved VPN gateway (ADR 0002, in progress) will add exactly one public UDP port; until it ships, the Server exposes no public UDP.`
  注意：这句必须避免 `does not expose public UDP` 这个禁用短语，上面的写法用 `exposes no public UDP` 规避，
  但语义仍是"当前不暴露"——这是**如实陈述现状**，不是全局禁令，符合 D3。
  （守卫测试匹配的是 `does not expose public UDP` 子串，改写后不再命中。）
- `sso-and-mfa.md:569`：与 Task 5 中 `docs/user-guide/sso-and-mfa.md:507` 同步的英文版，
  从 not-implemented 清单中移除 ICMP 与 TUN/L2 VPN，保留 P2P 与 RCE，补 ADR 0002 指引。
  链接写法（从 `docs/en/user-guide/` 出发）：`[ADR 0002](../../architecture/adr/0002-public-ingress-and-embedded-vpn.md)`。

- [ ] **Step 1: 改写 3 处**
- [ ] **Step 2: 确认本任务范围转绿**

Run: `go test ./scripts/ -run TestLiveDocsDoNotAssertReversedConstraints -count=1 2>&1 | grep -c '^docs/en/'` → `0`
总违规数从 9 降到 **6**（剩余：`docs/community/` 5 处 + `docs/README.md` 1 处）

- [ ] **Step 3: Commit**

```bash
git add docs/en/deployment/production.md docs/en/operations/security.md docs/en/user-guide/sso-and-mfa.md
git commit -m "docs(en): align ingress wording with adr 0002"
```

### Task 7: 社区与增长文档（4 个文件、5 处）

**Files:** `docs/community/comparison.md`、`docs/community/roadmap.md`、`docs/community/distribution-plan.md`、
`docs/community/why-tunnelmesh-needs-a-control-plane.md`

- `comparison.md:13`（表格单元格，长度敏感）：`HTTP/HTTPS/WSS only` → `HTTP/HTTPS/WSS (+ WireGuard UDP, in progress)`
- `comparison.md:54`：一行内三处主张，按 D3/部分反转处理——`does not implement ICMP, TUN/L2 VPN` 改为
  `does not implement P2P NAT traversal or arbitrary remote command execution`，
  `public Server ingress is HTTP/HTTPS/WSS only` 改为 `public Server ingress is HTTP/HTTPS/WSS today`，
  `UDP is supported for internal forwarding and local client listeners, not as a public Server listener`
  改为 `UDP is supported for internal forwarding and local client listeners; a public UDP WireGuard endpoint is approved by ADR 0002 and in progress`。
- `roadmap.md:42`：把 ICMP echo 与内嵌 VPN 网关从 `Explicitly not planned` 移出，
  写进该文件的"下一步/进行中"小节（沿用文件既有小节结构，不新增章节层级），
  `Explicitly not planned` 只保留 P2P NAT traversal 与任意远程命令执行。
- `distribution-plan.md:55`：社交文案，`Public ingress is HTTP/HTTPS/WSS only.` →
  `Public ingress is HTTP/HTTPS/WSS.`（文案不加未发布能力，避免过度承诺；ADR 指引不适合社交文案长度）
- `why-tunnelmesh-needs-a-control-plane.md:43`：`Public ingress is HTTP/HTTPS/WSS only, and the Server does not expose public UDP.`
  → `Public ingress is HTTP/HTTPS/WSS; the Agent never exposes a public listener.`（前半句按 D3 改写，
  后半句"Agent 不暴露公网监听器"仍然为真，必须保留）

链接写法（从 `docs/community/` 出发）：`[ADR 0002](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)`；
社交文案与表格单元格内不加链接。

- [ ] **Step 1: 改写 5 处**
- [ ] **Step 2: 确认本任务范围转绿**

Run: `go test ./scripts/ -run TestLiveDocsDoNotAssertReversedConstraints -count=1 2>&1 | grep -c '^docs/community/'` → `0`
总违规数从 6 降到 **1**（只剩 `docs/README.md:100`，归 Task 8）

- [ ] **Step 3: Commit**

```bash
git add docs/community/comparison.md docs/community/roadmap.md docs/community/distribution-plan.md \
        docs/community/why-tunnelmesh-needs-a-control-plane.md
git commit -m "docs(community): drop absolute ingress claims"
```

### Task 8: `docs/README.md`、索引、PR 记录与全量门禁

**Files:** `docs/README.md`、`docs/pull-requests/2026-09-20-vpn-phase1-constraint-reversal.md`、
四个生成的索引 README

- [ ] **Step 1: `docs/README.md:100`**

现状：`……生产环境建议通过环境变量或外部配置文件注入敏感配置，公网入口只使用 HTTP/HTTPS/WSS，Server 不监听公网 UDP。`
改为：`……生产环境建议通过环境变量或外部配置文件注入敏感配置。公网入口以 HTTP/HTTPS/WSS 为主；内嵌 VPN 网关启用后会额外监听一个公网 UDP 端口（ADR 0002，实施中，当前版本尚未提供）。`
ADR 0002 的可达性已由本文件第 82 行的 ADR 索引链接保证，无需新增登记项（前置核实结论 7）。

- [ ] **Step 2: 守卫测试全绿**

Run: `go test ./scripts/ -count=1`
Expected: `ok github.com/tunnelmesh/tunnelmesh/scripts`，两个测试都 PASS，**违规数 0**

- [ ] **Step 3: 写 PR 记录**

新增 `docs/pull-requests/2026-09-20-vpn-phase1-constraint-reversal.md`，覆盖 `AGENTS.md` 要求的全部小节
（标题、目标分支、摘要、用户影响、API/Schema/配置影响、安全与授权影响、测试证据、发布步骤、回滚步骤、
Reviewer 关注点、集成状态），正文必须链接本计划与规格（前置核实结论 6：否则索引无法建立关联）。
测试证据必须写明红灯基线 29 → 绿灯 0 的实际输出，以及未执行的验证与原因（无容器运行时 → 不涉及；
前端未改动 → `npm` 门禁不适用）。

- [ ] **Step 4: 重新生成索引并确认幂等**

Run: `python3 scripts/gen_doc_index.py && python3 scripts/gen_doc_index.py && git diff --stat`
Expected: PR 索引新增本记录并关联到本计划与规格；plans 索引把本计划关联到规格与 PR 记录；
第二次执行无新 diff。

- [ ] **Step 5: 全量门禁**

```bash
go build ./... && go vet ./...
go test ./... -count=1
go test -race ./... -timeout 30m
git diff --check
```

Expected: 全部通过。`go test ./...` 需要 `internal/server/web_dist` 存在（前置核实结论 3），
执行前先 `cd web && npm ci && npm run build && cd ..`；本阶段不改前端，构建只为满足 embed 断言。

- [ ] **Step 6: 时点记录零改写核验**

```bash
BASE=codex/vpn-task0-feasibility-plan
git diff --name-status "$BASE"..HEAD -- docs/superpowers docs/pull-requests docs/architecture/adr
```

Expected: 只出现 `A`（新增：本计划、ADR 0002、本阶段 PR 记录）与生成索引 `M`
（`adr/README.md`、`pull-requests/README.md`、`superpowers/plans/README.md`）；
**不得出现任何既有 spec/plan/PR 记录被 `M`**。

- [ ] **Step 7: 链接可达性核验**

```bash
BASE=codex/vpn-task0-feasibility-plan
git diff --name-only "$BASE"..HEAD -- '*.md' | while read -r f; do
  d=$(dirname "$f")
  rg -o '\]\(([^)#]+\.md)' "$f" | sed 's/^](//' | while read -r l; do
    case "$l" in http*) continue ;; esac
    test -f "$d/$l" || echo "BROKEN: $f -> $l"
  done
done
```

Expected: 本阶段新增链接 0 条 `BROKEN`。若输出中出现**改动前就已存在**的死链，记录为既有问题、
不在本阶段修复（`AGENTS.md`：不修不相关缺陷）。

- [ ] **Step 8: Commit**

```bash
git add docs/README.md docs/pull-requests/2026-09-20-vpn-phase1-constraint-reversal.md \
        docs/pull-requests/README.md docs/architecture/adr/README.md docs/superpowers/plans/README.md
git commit -m "docs: finish the vpn constraint reversal and record the pr"
```

## 任务间接口

- **Task 1 → Task 2-8**：`reversedClaims` 是全阶段唯一的"禁用主张"权威清单。任何任务都不得为了
  让测试变绿而删除或放宽短语；确需调整（例如发现某短语只出现在必须保留的真实主张里）必须先改
  Task 1 的清单、在提交信息里说明理由，并同步更新本计划的清单表。
- **Task 2 → Task 3-8**：ADR 文件名 `0002-public-ingress-and-embedded-vpn.md` 与 `- Status: Accepted`
  行是所有文档链接与守卫测试的锚点，改名会同时打破两者。
- **Task 3 → Task 4-8**：`AGENTS.md` 的新措辞（"以…为主；启用 VPN 网关时额外监听一个公网 UDP 端口"）
  是其余文档改写的语义基准，中英双语版本必须语义等价。
- **Task 5/6 → 彼此**：`docs/user-guide/sso-and-mfa.md:507` 与 `docs/en/user-guide/sso-and-mfa.md:569`
  是同一段落的双语版本，必须同步改写，否则中英文档语义冲突（`documentation.md`：语义冲突以中文深度文档为准，
  但本处要求两边一致）。
- **Task 8 ← 全部**：守卫测试的 0 违规、索引幂等、时点记录零改写、链接可达四项是本阶段的收口门禁。
- 各任务写文件集合互不重叠（见「精确文件清单」的任务分组），因此 Task 3-7 可以并行执行；
  Task 1 必须最先完成（提供红灯基线），Task 8 必须最后完成（收口门禁）。

## 整体验证

```bash
# 守卫测试：Task 1 红灯 29 → Task 8 绿灯 0
go test ./scripts/ -count=1

# 仓库门禁（AGENTS.md「必须执行的验证」）
cd web && npm ci && npm run build && cd ..   # 只为满足 internal/server/web_test.go 的 embed 断言
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -timeout 30m
git diff --check

# 文档门禁
python3 scripts/gen_doc_index.py            # 幂等；再跑一次应无 diff
git diff --name-status codex/vpn-task0-feasibility-plan..HEAD -- docs/superpowers docs/pull-requests docs/architecture/adr
```

前端未改动，`npm test -- --run` 不是本阶段的门禁；执行 `npm run build` 只为产出 embed 目录。

## 回滚注意事项

- 本阶段只改文档与一个测试文件，回滚 = `git revert` 对应提交，无数据、无 Schema、无配置状态需要补偿，
  5 分钟内可完成。
- **ADR 编号永不复用**（`adr/README.md` 约定）。若决策被推翻，不得删除 ADR 0002，而应新增一条 ADR
  并把 0002 的 Status 改为 `Superseded by NNNN`；守卫测试 `TestADR0002RecordsTheConstraintReversal`
  断言 `- Status: Accepted`，届时必须同步调整该测试，且调整理由写进新 ADR。
- 回滚文档改写时必须一并回滚 Task 1 的守卫测试，否则 CI 会因为活文档重新出现禁用短语而红灯。
- 本分支 stacked 在 `codex/vpn-task0-feasibility-plan` 上：Task 0 的 PR 合并进 `main` 后，
  本分支需要 `git rebase --onto main codex/vpn-task0-feasibility-plan` 再推送，避免出现重复提交。
- 阶段 2（Task 0）已完成，本阶段回滚不影响 Task 0 的结论与 spike 产物；但阶段 3-8 全部以
  ADR 0002 为前提，回滚即冻结后续所有阶段。
