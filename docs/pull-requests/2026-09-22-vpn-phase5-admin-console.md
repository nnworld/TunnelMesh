# VPN 网关 阶段 5：管理后台

## 标题

`feat(web): land the vpn gateway console`

本记录覆盖分支上阶段 5 的全部提交（计划 → API 客户端 → peer 管理视图 → 路由与双语文案 →
节点页网关状态 → 用户文档 → 幂等键修复 → 本记录），而不只是最后一个提交。

## 目标分支

`main`

本分支 stacked 在 `codex/vpn-phase4-pure-logic-and-api` 之上。阶段 1、3、4 合并进 `main` 后，
必须先 `git rebase --onto main codex/vpn-phase4-pure-logic-and-api` 再更新本 PR，
否则 diff 会重复包含阶段 3（Schema v15）与阶段 4（`internal/vpn` + 管理 API）的产物。

## 关联记录

- 实施计划：[VPN 网关 阶段 5：管理后台 Implementation Plan](../superpowers/plans/2026-09-22-vpn-phase5-admin-console.md)
- 设计规格：[内嵌 VPN 网关（WireGuard）设计](../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md)（§4.3 后台入口、§10.1–§10.8 管理后台、§12.1 稳定码、§15 阶段 5）
- 决策载体：[ADR 0002: Open a public UDP ingress for an embedded VPN gateway](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)（`Status: Accepted`）
- 前置阶段：[VPN 网关 阶段 4：纯逻辑包与管理 API](2026-09-21-vpn-phase4-pure-logic-and-api.md)（本阶段唯一输入：9 个操作 + 10 个稳定码 + `server.vpn` 配置）
- 接口契约：[docs/api/openapi.yaml](../api/openapi.yaml) 的 6 条 `/api/v1/vpn-*` 路径（本阶段零改动）
- 用户文档：[Server 管理后台使用指南 · VPN 网关 peer 管理](../user-guide/server-admin.md#vpn-网关-peer-管理)、[配置参考 · 内嵌 VPN 网关](../operations/configuration.md)

## 摘要

阶段 5 交付规格 §10 的全部条目：VPN 网关的**管理后台**。左侧菜单新增“VPN 网关”（`/vpn`），
可以签发、编辑、轮换、吊销 WireGuard peer，并在“使用说明”抽屉里一次性 reveal 客户端配置；
节点页（`/servers`）新增网关运行状态区块。本阶段**零 Go 代码改动、零 OpenAPI 改动、零 Schema 改动、
零新前端依赖**：`go.mod`/`go.sum`/`web/package.json`/`web/package-lock.json` 四个文件的
`git status --porcelain` 为空，阶段 4 的服务端契约逐字作为输入消费。

五类产物（相对 `codex/vpn-phase4-pure-logic-and-api`：20 个文件 `+2114/-10`，含本阶段计划文档，不含本记录与两份生成索引）：

1. **API 客户端**（`web/src/api/vpn.ts`，199 行）：9 个操作全部落地
   （`listVpnPeers`/`createVpnPeer`/`getVpnPeer`/`patchVpnPeer`/`revokeVpnPeer`/`rotateVpnPeer`/
   `revealVpnPeerConfig`/`listVpnPeerFlows`/`listVpnNodes`），范式照 `api/credentials.ts`。
   `VpnPeer` 类型**刻意不声明四个密封列**（ciphertext/nonce/key id/key version），
   与服务端 projection 的 `additionalProperties: false` 对齐；两个数据面类型
   （`VpnNodeStatus`/`VpnFlow`）的每个字段都是可选的，因为它们由阶段 6 定形。
2. **表单规则包**（`web/src/views/vpn-form.ts`，218 行 + 7 个测试）：CIDR 与端口解析、
   到期时间归一、`VpnPeerInput` 构造、`VpnPeerPatch` 差分。照 `token-form.ts` 先例把规则从
   组件里抽出来，使它们不需要 DOM 就能穷举测试，且对话框无法接受服务端会拒绝的输入。
3. **peer 管理视图**（`web/src/views/VpnPeers.vue`，531 行 + 9 个测试）：列表（cursor 分页、
   关键词搜索、状态分段筛选）、创建/编辑对话框、详情抽屉、使用说明抽屉（含一次性 reveal）、
   IP 池概览（条件渲染）、常驻的“本版没有数据面”说明。
4. **稳定码文案映射**（`web/src/views/vpn-errors.ts`，36 行）：10 个稳定码各有专属文案，
   并把 `vpn_not_implemented`/`vpn_node_disabled`/`vpn_agent_capability_missing` 三者标为
   “状态而非错误”，由视图渲染成 info 级说明，不弹全局错误提示。
5. **入口、路由、双语文案与节点页区块**：`router.ts` 一行（`meta:{auth:true}`，不加 admin 门禁）、
   `AppShell.vue` 一个菜单项（在 `/credentials` 之后）、`breadcrumbs.ts` 一行、
   两份 i18n 文案（`vpn.*` 每侧 **130** 个叶子键 + `servers.vpn.*` **15** 个）、
   `Servers.vue` 的网关状态区块（+75 行）。

## 用户影响

- **默认零影响。** 阶段 4 的 `server.vpn.enabled` 仍是总开关：关闭时列表页返回 200 空列表、
  写操作返回 `409 vpn_node_disabled`，`/vpn` 页面据此显示空态或说明，而不是报错。
  不需要迁移、不需要新环境变量、不需要改 nginx/`deploy/`/防火墙。
- **本版能做什么**：在后台完成 peer 的整个生命周期——签发（分配 VPN 地址、限定网段与端口、
  设定并发流与包速率上限、设定到期时间）、编辑（只提交改动过的字段）、轮换密钥（IP 不变）、
  吊销（终态、记录保留）、按名称或备注搜索、按状态筛选、查看审计友好的详情。
  普通用户只看自己的 peer，管理员看全部；两者是同一个页面，可见范围由服务端决定。
- **本版不能做什么（页面必须如实告知，且已经告知）**：
  - **不能连通。** 页面顶部常驻 `vpn.dataPlanePending` 说明：网关数据面尚未随发行版提供，
    导入客户端后还不能建立隧道。文案由 `scripts/doc_claims_test.go` 的同一套纪律约束，
    不得写成“已可用”。
  - **拿不到配置文件。** reveal 需要输入 `REVEAL` 并勾选风险确认，但本版服务端恒返回
    `409 vpn_node_disabled`；抽屉把这条事实渲染成 info 级说明（“节点还没有网关身份”），
    而不是通用的“操作失败，请重试”。
  - **看不到活跃流。** `GET /api/v1/vpn-peers/{peerId}/flows` 返回 `501 vpn_not_implemented`，
    因此列表**没有**活跃流列（源码标记测试断言视图里不出现 `listVpnPeerFlows`），
    抽屉只给出“到 Grafana 的 VPN Gateway Row 查看”的指引。
  - **签不出 ICMP peer。** `icmpEnabled` 开关存在且可勾选，但本版服务端一律返回
    `409 vpn_agent_capability_missing`；表单帮助文案直接写明该能力属阶段 7。
  - **IP 池水位未知。** `GET /api/v1/vpn-nodes` 返回 501，节点页与列表页都显示
    “本版未提供网关运行时状态”，**不会**显示成“已分配 0”——那是一个看起来可操作、
    实际是假数据的数字。
- **签发需要 `TUNNELMESH_TOKEN_ENCRYPTION_KEY`**（与凭据密文同一个变量，非本版新增）。
  缺失时返回 `503 credential_secret_unavailable`，页面显示专属文案，不降级为明文存储。
- **新增一处后台入口**：`docs/user-guide/server-admin.md` 的“VPN 网关 peer 管理”与
  “Server 节点”章节补充了网关状态区块的读法，`docs/README.md` 的分类索引同步加了入口。

## API、Schema 与配置影响

**API**：零改动。`docs/api/openapi.yaml` 无 diff；本阶段只消费阶段 4 已发布的 6 条路径 / 9 个操作。
前端调用与服务端契约的对应关系（实测，非推断）：

| 操作 | 前端调用点 | 本版服务端应答 | 页面呈现 |
| --- | --- | --- | --- |
| `GET /vpn-peers` | 列表加载、搜索、筛选、加载更多 | 200 cursor 分页 | 表格；`enabled:false` 时为空态 |
| `POST /vpn-peers` | 创建对话框保存 | 201 / 4xx-5xx | 成功即关闭对话框并重载；失败按稳定码分类 |
| `PATCH /vpn-peers/{id}` | 编辑对话框保存 | 200 | 只提交差分字段 |
| `DELETE /vpn-peers/{id}` | 吊销（二次确认） | 200 返回吊销后的行 | 就地更新该行，不整页重载 |
| `POST /vpn-peers/{id}/rotate` | 轮换（二次确认） | 200 返回 `configRevealPath` | 提示“请重新下发配置” |
| `POST /vpn-peers/{id}/config:reveal` | 使用说明抽屉 | **恒 `409 vpn_node_disabled`** | info 级说明，不弹错误提示 |
| `GET /vpn-peers/{id}/flows` | 客户端已实现，**视图不调用** | `501 vpn_not_implemented` | 抽屉指向 Grafana |
| `GET /vpn-nodes` | 列表页 IP 池概览、节点页状态区块 | `501 vpn_not_implemented` | “本版未提供网关运行时状态” |
| `GET /vpn-peers/{id}` | 客户端已实现，视图不调用 | 200 | 列表行已带完整投影，详情不再多一次往返 |

- `Idempotency-Key` 覆盖 create/patch/rotate/revoke/reveal 五个写操作。**表单里的键按“对话框会话”
  生成并在重试间复用**（`resetForm()` 里 `crypto.randomUUID()`）：一次因响应丢失而失败的重试
  必须是同一次操作，否则服务端无法去重，用户会得到两个 peer 和一个被白占的地址。
  rotate/revoke/reveal 仍每次点击生成新键——它们是新的显式确认，不是上一次的重试；
  且 reveal 的键按阶段 4 的取舍只作确认信号、不作缓存键（响应体含私钥，不入幂等库）。
- reveal 的两个前置条件（`X-VPN-Config-Reveal-Confirm` 非空、`acknowledgeRisk=true`）在
  **请求存在之前**就镜像到界面上：确认词未输入或风险未勾选时按钮不触发请求，
  与服务端的校验顺序一致，避免用户先看到一次 400 才知道要输入 `REVEAL`。

**Schema**：零改动。`migrations/` 无 diff，`SchemaVersion` 保持 `15`，无迁移、无维护窗口、无备份窗口。

**配置**：零改动。不新增任何环境变量、命令行参数或配置文件键；`server.vpn.*` 的 16 个键
仍是阶段 4 的形状与默认值。前端构建产物路径不变（`web/dist` → `internal/server/web_dist`，
两者均 gitignore，`scripts/verify-web-embed.sh` 在漂移时失败）。

## 安全与授权影响

- **私钥只活在组件内存里。** reveal 返回的完整 wg-quick ini（含 peer 私钥）只存进一个 `ref`，
  抽屉关闭即清空；源码标记测试断言 `VpnPeers.vue` 里**不出现** `localStorage` 与 `sessionStorage`，
  并有挂载测试断言关闭抽屉后 `document.body.textContent` 不再含明文、`localStorage.length === 0`。
  不写临时文件、不进 Pinia 持久层、不进日志、不上报。
- **不自动 reveal。** 每次显示配置都要求用户显式点击、输入 `REVEAL`、勾选风险确认；
  抽屉关闭后重新打开需要重做三步。这与既有 `tokens/{tokenId}/reveal` 的取舍一致。
- **`/vpn` 不加前端 admin 门禁**（`meta:{auth:true}`，`meta.admin` 未设置，有测试锁定）。
  谁能看到哪些 peer 由服务端在**分页语义内**按 principal 过滤（阶段 4 已实现），
  前端再判一次角色只会产生第二套可能不一致的真相。管理员与普通用户看到的是同一个页面、
  同一套操作，差别只在服务端返回的行集。
- **前端校验不是信任边界。** 名称长度、CIDR、端口、到期时间的规则与服务端逐字对齐
  （255/255、IPv4 CIDR、1–65535、必须晚于当前时间），但服务端会再校验一次；
  校验失败时**不发请求**，省一次往返并把错误落在具体字段上。
- **裸 IP 归一为 `/32`** 是安全相关的一次前端让步：服务端用 `net.ParseCIDR` 解析 `allowed_ips`，
  不接受无前缀写法。若前端原样提交，用户会看到一个指向错误字段的 400；归一后语义与
  “单个主机”一致，且不会悄悄扩大授权范围。IPv6 一律拒绝，因为出口策略是 IPv4-only，
  一条 IPv6 规则会看起来像规则却什么也不匹配。
- **审计与错误面不含密钥材料。** 页面显示的错误文案来自稳定码映射，不回显服务端消息里的
  敏感细节；`vpnErrorMessage` 在非 `APIError` 时退化为通用文案，不会把异常对象序列化上屏。
- **文档纪律**：本阶段新增的用户文档与前端文案都不得宣称数据面可用。
  `scripts/doc_claims_test.go` 扫描 `docs/`（时点记录目录除外）、`deploy/`、两份 README 与 `AGENTS.md`，
  出现被 ADR 0002 反转的旧主张即构建失败；本版实测绿灯。

## 测试证据

TDD 全程实测，每个实现提交的红灯原文如下（当时终端输出，非估算）：

| 提交 | 任务 | 红灯原文（首行） |
| --- | --- | --- |
| `472023e` | 计划 | —（文档提交，无红灯步骤） |
| `95d036a` | T1 API 客户端 | `Failed to resolve import "../api/vpn" from "src/tests/vpn-api.spec.ts"` |
| `04662ed` | T2 视图与错误映射 | `Failed to resolve import "../views/vpn-form" from "src/tests/vpn-form.spec.ts"`；`Failed to resolve import "../views/VpnPeers.vue" from "src/tests/vpn.spec.ts"` |
| `6b17582` | T3 路由、导航与 i18n | `expected undefined to be truthy (the /vpn route did not exist)` |
| `851a7c7` | T4 节点页网关状态 | `src/tests/servers-vpn.spec.ts (5 tests | 5 failed)`；`[intlify] Not found 'servers.vpn.title' key in 'en' locale messages.` |
| `ff4fe63` | 用户文档 | —（文档提交，无红灯步骤） |
| `47aea17` | 幂等键复用修复 | `expected '7eb42d9f-1175-4ef8-bbc5-a4b256585761' to be '64d78b6c-b0e2-44b7-8d7e-3c3ca649204f' // Object.is equality` |

新增 5 个 spec 文件共 **35** 个测试（`vpn-api` 8、`vpn-form` 7、`vpn` 9、`vpn-i18n` 6、
`servers-vpn` 5），前端总量从 33 文件 / 288 测试增至 **38 文件 / 323 测试**。

绿灯与门禁（全部实测，非引用）：

- `cd web && npm test -- --run`：`Test Files 38 passed (38)`、`Tests 323 passed (323)`，0 失败。
- `npm run build`：`✓ built in 3.20s`，`sync-web-dist: mirrored web/dist -> internal/server/web_dist`。
- `bash scripts/verify-web-embed.sh`：`web/dist and internal/server/web_dist match`。
- `go build ./...`、`go vet ./...` 干净；`go test ./scripts/ -count=1` → `ok`（文档主张守卫）。
- `git status --porcelain go.mod go.sum web/package.json web/package-lock.json` **为空**（依赖零改动）。
- `git diff --check` 干净；工作树在本记录之前无未提交改动。
- `python3 scripts/gen_doc_index.py` 连跑两次输出一致（幂等），四个生成索引
  （plans/specs/pull-requests/adr）无手工编辑。
- 完整 `go test ./...` 与 `-race` 本阶段不重跑：Go 源文件零改动，阶段 4 已对同一份 Go 代码
  验证过（`internal/server` 443.684s 全绿），重跑不产生新信息。

测试设计上的三条硬约束：

1. **`stubApi` 对未声明的请求抛错**，因此视图漏接或改错端点会在测试里立刻暴露，
   不会退化成“空渲染也通过”。节点页测试因此必须同时 stub `/server-nodes` 与 `/vpn-nodes`。
2. **501 不是 0。** `servers-vpn.spec.ts` 断言 501 时区块里没有 `table`、没有 `dl`、
   没有 `.vpn-metric` 单元格，且文本里不存在“独立的 0”
   （正则 `/(^|\D)0(\D|$)/`；文案里 `501` 的 0 夹在数字之间，因此不被误判）。
3. **文案守卫**：`vpn-i18n.spec.ts` 走 `vpn.*` 的每个叶子，空串即失败，两侧结构必须完全相同，
   并禁止 vue-i18n 保留字符 `@ | { }`（度量标签与 systemd 单元名里出现过这个 bug）；
   10 个稳定码通过读 `vpn-errors.ts` 的映射反查每个 locale 的文案，缺一条即失败。
   `i18n.spec.ts` 的既有双向键守卫同时覆盖新增的 `servers.vpn.*`（每侧 15 个叶子）。

## 与计划的偏差（全部为收紧或事实更正，无功能缩水）

1. **`vpn.*` 文案随 T2 交付，计划写在 T3。** 挂载式视图测试断言的是本地化后的文案
   （`expect(text).toContain(t('vpn.poolUnavailable'))`），没有键就测不了视图；
   因此 T2 提交里已包含 `vpn.*` 树，T3 只补 `navigation.vpn`、路由、菜单与守卫测试。
   计划是时点记录，不改写，差异记在这里。
2. **多出 `vpn-form.ts` + `vpn-form.spec.ts`（计划 §6 文件清单未列）。** 照 `token-form.ts`
   先例把表单规则从组件里抽出来：218 行实现 + 7 个测试，覆盖 CIDR/端口解析、到期归一、
   input 构造与 patch 差分。放在 `views/` 而不是 `api/`，因为它属于表单语义而非传输。
3. **名称与备注上限是 255/255，不是计划 D4 写的 128/256。** 权威来源是服务端
   `internal/vpn/peerspec.go:18-19` 的 `MaxPeerNameLength = 255` / `MaxPeerDescriptionLength = 255`
   （对齐 `VARCHAR(255)`）。前端常量逐字取同一对值，`el-input` 的 `maxlength` 也改成 255。
4. **裸 IPv4 在前端归一为 `/32`。** 计划只说“CIDR 与 `parseCIDRList` 同构”，实现时发现服务端用
   `net.ParseCIDR`，无前缀写法会被拒绝并把错误报在错误字段上，因此 `parseVpnCidrList` 补 `/32`。
   同时只接受 IPv4（`routing.Policy.Validate` 是 IPv4-only，IPv6 规则会静默不匹配）。
5. **字段级错误用 `el-form-item` 的 `:error` 绑定，不是计划 D4 说的 `rules`。**
   规则已经集中在 `vpn-form.ts` 里，再写一份 `rules` 就是同一套判断的第二处定义；
   `:error` 让 element-plus 只负责呈现（`.is-error` 类与消息文本由它渲染，测试两者都断言）。
6. **`el-form-item` 的校验态有 100ms 防抖**，断言本地化消息前必须等待，测试里用 `settle(160)`。
   `.is-error` 类是即时的，消息文本才是“这段文案是我们的”的证据。
7. **T3 多改一处：`web/src/layouts/breadcrumbs.ts`**（计划文件清单未列）。
   `/vpn` 需要一个面包屑标题，否则 `PageHeader` 显示空；改动是一行三元分支，
   并由 `vpn-i18n.spec.ts` 断言 `breadcrumbsFor('/vpn')` 等于 `['shell.console','vpn.title']`。
8. **T4 交付 5 个用例，计划只写了 200 与 501 各一例。** 补上：部分字段缺失（逐项显示 `—`
   而不是空单元格，因为 `VpnNodeStatus` 每个字段都可选）、空 items（“当前没有节点提供 VPN 网关”
   是一个真实答案，与 501 不同）、以及两侧 locale 的 `servers.vpn.*` 键完整性与结构一致。
9. **多出两个提交，都在计划之外**：
   - `ff4fe63 docs(user-guide)`：`docs/user-guide/server-admin.md` 逐个枚举后台页面，
     菜单里已经有 `/vpn` 而指南里没有，属于活文档漂移（`AGENTS.md` 要求用户使用文档与
     `docs/README.md` 索引同步，且不得出现孤儿文档）。
   - `47aea17 fix(web)`：T5 复审时发现表单每次点击都新生成幂等键，重试会造出第二个 peer。
     这是本阶段自己引入的缺陷，按“根因修复”纪律在本阶段内修掉，不留到阶段 6。
10. **共享测试 harness 扩了一个字段**：`src/tests/api-stub.ts` 增加可选 `statuses?: number[]`
    （与既有 `sequence` 同样的消费语义：逐项出队、末项重复）。一个 stub 只有一个 status 时，
    “先失败后成功”这个唯一能观察到重试的形状无法表达。纯增量，既有 spec 零改动、全部仍绿。

## 顺带发现、本阶段刻意不修的既有问题

1. **`Credentials.vue` 与 `Tokens.vue` 仍是“每次点击新生成幂等键”**
   （`web/src/views/Credentials.vue:336-337`、`web/src/views/Tokens.vue:256`），
   与本阶段修掉的 VPN 表单缺陷同形。不在本阶段修：属于既有特性的行为变更，
   会牵动它们各自的测试与审计语义，且 `Routes.vue:599`、`AgentDetail.vue:439` 已经用了正确范式，
   说明这是一处待统一的债务而不是一处未知缺陷。建议独立小计划一次改齐三处。
2. **`Servers.vue` 在 jsdom 下会打一条 `[intlify] Not found 'servers.status.undefined'` 警告。**
   来自 el-table 的占位行调用 `statusLabel(undefined)`，不是真实状态；本阶段未修，
   因为修法（给 `statusLabel` 加空值分支）会改动与 VPN 无关的既有渲染路径。
3. **稳定码 → 文案的映射在每个特性里各写一份**（`remote-server-errors.ts`、`vpn-errors.ts`）。
   两套形状相同、互不引用。出现第三套时应抽一个公共映射，本阶段照既有范式实现，不提前抽象。
4. **`web/src/api/client.ts` 的 `APIError.domain` 是 `string`**，没有按特性收窄的联合类型，
   因此映射表写错一个码不会在编译期被发现（只有测试能发现）。收窄需要给每个特性定义
   域枚举，属独立的类型层改造。
5. **`getVpnPeer` 与 `listVpnPeerFlows` 在客户端已实现但视图不调用。** 前者是因为列表行已带完整
   投影（多一次往返没有信息增益），后者是因为本版 501。两者都保留：9 个操作是规格 §10.1 的
   交付面，阶段 6 的活跃流表格会直接用 `listVpnPeerFlows`。

## 发布步骤

1. **合并顺序不可颠倒**：阶段 1 → 3 → 4 → 5。本阶段前端只调用阶段 4 的端点；
   若前端先于阶段 4 上线，`/vpn` 会收到 404 并渲染通用错误。核心模块改动需独立复审。
2. 本版**不需要**数据库迁移、不需要备份窗口、不需要新环境变量、不需要改 nginx/`deploy/`/防火墙。
   发布物是 Server 二进制里嵌入的前端产物（`internal/server/web_dist`），
   因此**必须**在 CI 或本机跑 `npm run build`，让 `scripts/verify-web-embed.sh` 通过后再打包。
3. 默认发布即安全：`server.vpn.enabled` 保持 `false` 时，菜单里仍有“VPN 网关”入口，
   但列表为空态、写操作返回 409 并被渲染成说明。若产品要求“开关关闭时不显示入口”，
   需要服务端先提供一个能力探测端点，本版按 YAGNI 未做（见 Reviewer 关注点 3）。
4. 灰度顺序：先升一个节点 → 用管理员账号打开 `/vpn` 与 `/servers` 验证区块渲染 →
   打开 `server.vpn.enabled` 并注入 `TUNNELMESH_TOKEN_ENCRYPTION_KEY` → 签发一个测试 peer
   （应得到 201 与 `vpnIp`）→ 点开“使用说明”（应得到 `409 vpn_node_disabled` 的 info 说明，
   而不是错误提示）→ 吊销测试 peer → 再滚动其余节点。
5. **不要在本版对外宣布 VPN 可用**，也不要为它放行公网 UDP：`listen` 即使配置了也没有进程监听。
   对外措辞统一为“已批准、分阶段实施中、当前版本提供管理面与后台，数据面尚未随发行版提供”。
6. 升级后校验：`/vpn` 列表在 `enabled:false` 时为空态、`enabled:true` 时可签发；
   表单在名称为空、CIDR 非法、端口非法、到期时间在过去时**不发请求**且错误落在具体字段；
   编辑只改名称时 `PATCH` 的 body 只含 `name`；轮换后提示重新下发配置；吊销需二次确认；
   节点页在 501 时显示说明且不显示“已分配 0”；切换语言后 `vpn.*` 与 `servers.vpn.*` 无键路径裸露；
   浏览器 `localStorage` 里 reveal 前后都不出现私钥或配置文本。
7. 清理灰度数据：吊销测试 peer（`DELETE`）而不是删行——吊销是终态且没有 Delete 路径。

## 回滚步骤

- **代码侧**：本阶段是 5 个新增文件 + 6 处受控修改（`router.ts` 一行、`AppShell.vue` 一个数组项、
  `breadcrumbs.ts` 一行、两份 i18n 文案、`Servers.vue` 一个区块与一个 refresh 函数），
  `git revert` 对应提交即可，无数据迁移、无配置迁移。回滚以任务边界为单位成组进行
  （T4 → T3 → T2 → T1）：只回滚 T1 会让 `VpnPeers.vue` 的 import 指向不存在的模块，
  **构建直接失败**；只回滚 T3 会留下一个不在菜单里的可达路由（`/vpn` 仍能直接输入 URL 访问）。
- **5 分钟止损**：`server.vpn.enabled: false` 即可关掉全部写操作，前端随之显示空态与说明；
  不需要回滚二进制。若问题只在前端（例如文案或渲染），回滚到上一版嵌入产物即可，
  服务端行为零变化。
- **数据侧**：已签发的 peer 行不受前端回滚影响，重新上线也**不需要重新签发**。
- **构建产物侧**：`web/dist` 与 `internal/server/web_dist` 都不入库，回滚源码后必须重跑
  `npm run build`；两者漂移时 `scripts/verify-web-embed.sh` 会失败，因此漏跑会被门禁发现。
- **文案侧**：i18n 键一旦发布即被 `i18n.spec.ts` 的双向守卫锁定，删除 `vpn.*` 或
  `servers.vpn.*` 的任何键必须两侧同时删，否则构建期失败。

## Reviewer 关注点

1. **`/vpn` 不加 admin 门禁是否符合预期？** 普通用户能进入页面并只看到自己的 peer
   （服务端在分页语义内过滤）。若产品要求“未开启 VPN 时对普通用户完全隐藏入口”，
   需要服务端提供能力探测或把可见性放进 `/api/v1/me`，本版按 YAGNI 未做。
2. **reveal 的三步前置（点击 → 输入 `REVEAL` → 勾选风险）是否够重？** 本版恒 409，
   因此这道门槛现在拦住的是“用户以为拿到了配置”。阶段 6 让它可用后，
   同一道门槛就是私钥出网的唯一人工确认点，请确认措辞与交互在真正可用时仍然成立。
3. **501 的呈现方式**：三处（列表页 IP 池概览、节点页状态区块、抽屉里的活跃流指引）
   都是 info 级说明 + 指向 Grafana，而不是空表格或 0。请确认“宁可说不知道，也不给假数字”
   在产品上可接受；这是 `AGENTS.md`“优雅降级不得返回兜底假数据”的前端对应物。
4. **幂等键的作用域**：表单按对话框会话复用、rotate/revoke/reveal 每次点击新生成。
   请确认这个不对称是有意的（重试 vs. 新确认），以及 `resetForm()` 同时承担
   “清空表单”和“换键”两件事是否可读——它由 `openCreate`/`openEdit`/`@closed` 三处调用，
   因此“每次打开对话框必然是一个新键”这条不变量只在这一个函数里成立。
5. **`VpnPeer` 类型不声明四个密封列**是契约而非疏忽。若将来有人为了“前端调试方便”
   把它们加回类型，服务端 projection 的 `additionalProperties: false` 会让字段永远是 `undefined`，
   而类型系统会说它存在——请确认这个陷阱值得保留注释（`api/vpn.ts:5-12`）。
6. **表单校验与服务端规则的耦合点**是 `vpn-form.ts` 里的两个常量（255/255）与 IPv4-only 判定。
   服务端改上限时前端不会自动跟随，只会表现为“前端放行、服务端 400”。
   阶段 6/7 若要改 `PeerSpec`，请把这两个常量列进 checklist。
7. **`api-stub.ts` 的 `statuses` 扩展**是共享 harness 改动。请确认消费语义
   （逐项出队、末项重复）与既有 `sequence` 一致到不会让后来者困惑，
   以及它没有削弱任何既有断言（既有 spec 零改动、全部仍绿）。
8. **时点记录零改写核验**：`git diff --name-status codex/vpn-phase4-pure-logic-and-api..HEAD --
   docs/superpowers docs/pull-requests docs/architecture/adr` 只出现本阶段计划与本记录的 `A`
   以及生成索引 `docs/superpowers/plans/README.md`、`docs/pull-requests/README.md` 的 `M`；
   **没有任何既有 spec/plan/PR 记录或已接受 ADR 被修改**。对计划的事实更正
   （255/255、`rules` vs `:error`、文件清单缺项）全部只记录在本文件。
9. **活文档措辞**：`README.md`/`README.zh-CN.md` 关于 VPN 的句子仍是阶段 1 写的
   “已批准并分阶段实施，当前版本尚未提供”，本版未改动，因为该措辞仍然为真；
   `docs/operations/configuration.md` 的“当前版本只提供管理面”同样为真
   （管理面现在多了一个后台入口，配置项与能力边界未变）。新增的用户文档段落见 `ff4fe63`。

## 集成状态

阶段 5 的全部任务已在 `codex/vpn-phase5-admin-console` 上完成并推送：API 客户端、表单规则包、
peer 管理视图、稳定码文案映射、路由与菜单、双语文案、节点页网关状态区块、用户文档、
幂等键复用修复、全量门禁与本记录。前端门禁全绿（38 文件 / 323 测试、构建与嵌入校验通过），
Go 侧 `build`/`vet`/文档主张守卫绿灯，依赖与契约零改动，Schema 零改动，
文档索引由 `scripts/gen_doc_index.py` 生成且幂等。

后续阶段与本阶段的关系：**阶段 7**（Agent ICMP echo 能力）会让 `icmpEnabled` 真正可签发，
届时表单帮助文案与 `vpn.errors.agentCapabilityMissing` 的措辞需要随之更新；
**阶段 6**（Server 数据面）会让 `config:reveal`、`flows`、`vpn-nodes` 三个端点真正可用，
届时列表页可以加活跃流表格、IP 池概览与节点页区块会自动从“说明”切到数值
（两者的条件渲染已经按 200 的形状写好，字段全部可选），
常驻的 `vpn.dataPlanePending` 说明必须删除，`docs/user-guide/server-admin.md` 与
两份 README 的“数据面尚未提供”措辞必须同步改写；**阶段 8**（可观测性与部署）补指标、
Grafana row、告警与 `deploy/` 样例，抽屉里“到 Grafana 查看”的指引届时应变成可点击的链接。
回滚本阶段只影响后台入口，不影响任何既有能力，也不影响阶段 4 已落地的管理 API。
PR 复审与合并待进行；合并顺序上必须在阶段 1、3、4 之后。
