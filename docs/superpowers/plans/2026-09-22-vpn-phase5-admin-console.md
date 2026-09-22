# VPN 网关 阶段 5：管理后台 Implementation Plan

- 日期：2026-09-22
- 分支：`codex/vpn-phase5-admin-console`（stacked 在 `codex/vpn-phase4-pure-logic-and-api` 之上）
- 规格：[内嵌 VPN 网关（WireGuard）设计](../specs/2026-09-19-embedded-vpn-gateway-design.md) §4.3、§10.1–§10.8、§12.1、§15 阶段 5
- 决策载体：[ADR 0002](../../architecture/adr/0002-public-ingress-and-embedded-vpn.md)
- 前置阶段：[阶段 4 PR 记录](../../pull-requests/2026-09-21-vpn-phase4-pure-logic-and-api.md)（9 个操作、10 个稳定码、`server.vpn` 配置）

## 1. 目标

交付规格 §10 的全部条目：

1. `web/src/api/vpn.ts`：9 个操作的 API 客户端与类型，范式照 `web/src/api/credentials.ts`。
2. `web/src/views/VpnPeers.vue`：列表 + 创建/编辑对话框 + 使用说明抽屉（含一次性 reveal）。
3. `web/src/router.ts` 与 `web/src/layouts/AppShell.vue`：`/vpn` 路由与导航项（不按 admin 门禁）。
4. `web/src/i18n/messages/{zh-CN,en-US}.ts`：`navigation.vpn` 与完整 `vpn.*` 文案，两侧键集合一致。
5. `web/src/views/Servers.vue`：节点 VPN 状态区块，数据来自 `GET /api/v1/vpn-nodes`。
6. 前端测试与构建产物同步（`npm test -- --run`、`npm run build`、`scripts/verify-web-embed.sh`）。

## 2. 非目标

- 不写数据面（阶段 6）、不改 Agent（阶段 7）、不加指标/Grafana/告警（阶段 8）。
- **不新增任何前端依赖**：规格 §10.5 已按 YAGNI 排除二维码，`package.json` 零改动。
- 不改 Go 代码、不改 OpenAPI、不改 Schema：阶段 4 的契约就是本阶段的输入。
  `web/dist` 与 `internal/server/web_dist` 都在 `.gitignore` 里，因此构建产物不入库。
- 不做活跃流列表页：`GET /api/v1/vpn-peers/{peerId}/flows` 属阶段 6，本版按规格 §10.3
  把活跃流排除在列表之外，只在详情抽屉里给出「看 Grafana」的指引。

## 3. 前置核实结论（实测，非推断）

1. `web/node_modules` 已安装；`package.json` 的脚本是 `test: vitest run`、
   `build: vite build && node scripts/sync-web-dist.mjs`，构建会把 `web/dist` 同步进
   `internal/server/web_dist`（`//go:embed all:web_dist`，两者均被 `.gitignore` 忽略）。
2. `web/src/api/client.ts:412` 抛出
   `new APIError(payload.msg, response.status, payload.code, data?.error, data)`，
   因此阶段 4 的稳定码在 `error.domain` 上可直接分支，`error.details` 是整个 `data`。
3. i18n 键对齐有双向守卫：`src/tests/i18n.spec.ts:44`（结构相同）与 `:79-85`
   （分别报告缺失与多余，并断言长度相等）。新增键必须两侧同时加。
4. 菜单在 `src/layouts/AppShell.vue:38` 的 `menuItems()`；非 admin 段落以
   `['/credentials','navigation.credentials']` 结尾，规格 §10.1 要求在其后插入 `['/vpn','navigation.vpn']`。
5. 路由集中在 `src/router.ts` 的一行式数组里，`meta:{auth:true}` 表示需登录、
   `meta:{admin:true}` 表示需管理员；`/routes` 与 `/credentials` 都只有 `auth`。
6. **本版服务端事实**（阶段 4 PR 记录已实测）：`GET /api/v1/vpn-nodes` 与
   `GET /api/v1/vpn-peers/{id}/flows` 返回 `501 vpn_not_implemented`；
   `POST .../config:reveal` 返回 `409 vpn_node_disabled`；`icmpEnabled: true` 的签发返回
   `409 vpn_agent_capability_missing`；`enabled: false` 时读操作返回空列表、写操作返回 409。
   前端必须把这四种情况渲染成**明确的、指向服务端配置或后续版本的说明**，
   而不是通用「操作失败，请重试」。
7. 视图测试有两种既有范式：源码标记断言（`credentials-view.spec.ts`）与挂载 +
   `stubApi`（`sso-providers.spec.ts`，harness 在 `src/tests/api-stub.ts`）。
   `stubApi` 对未声明的请求**抛错**，因此漏接端点会在测试里立刻暴露。
8. Agent 下拉复用 `src/views/token-form.ts` 的 `loadAgentsForSelection(getAgents)` 与
   `filterAgents`（`Routes.vue:227` 已这么用）；CIDR 多值输入复用 `Routes.vue:468`
   的 `parseCIDRList` 范式（逗号分隔文本 + 客户端校验，非法即阻止提交）。
9. 一次性密钥展示的既有范式是 `src/components/TokenSecretDialog.vue`（23 行）与
   `Credentials.vue` 的 `clearSensitiveKeyInput`：离开即清空，不落 `localStorage`/`sessionStorage`。

## 4. 架构决策

### D1：`api/vpn.ts` 只做传输与类型，不做文案

稳定码到文案的映射放在视图层（`vpnErrorMessage(error, t)`，范式照
`src/views/remote-server-errors.ts`），因为同一个码在列表、表单和 reveal 三处需要不同措辞。
`api/vpn.ts` 只负责：查询串拼装、幂等键透传、reveal 的确认头与 `acknowledgeRisk`、
以及把服务端投影类型逐字声明出来（**不声明四个密封列**，与 `VPNPeer` projection 的
`additionalProperties: false` 一致）。

### D2：reveal 的私钥只活在组件内存里，且关抽屉即清空

`config:reveal` 返回完整 ini（含 peer 私钥）。前端纪律：

- 私钥不进 `localStorage`/`sessionStorage`/`pinia` 持久层，只存 `ref`。
- 抽屉关闭、路由离开、复制完成后都不保留明文；`revealVisible` 置 false 时同步清空。
- 每次显示都要求用户显式点「显示」并确认风险，不自动 reveal。
- 复制用 `navigator.clipboard`，失败时降级为选中文本提示，不写临时文件。

### D3：数据面缺席是**状态**，不是错误

501 `vpn_not_implemented`、409 `vpn_node_disabled`、409 `vpn_agent_capability_missing`
三者都渲染成 `el-alert type="info"`（而非 `error`）加专属文案，因为用户没有做错任何事：
能力属后续版本或需要服务端开关。`Servers.vue` 的 VPN 区块在 501 时显示
「本版未提供网关运行时状态」，不弹全局错误提示、不重试风暴。

### D4：表单校验与服务端逐字对齐，校验失败不提交

名称：1–128 字符（服务端 `PeerSpec.ValidateFields`）；CIDR：IPv4，`parseCIDRList` 同构；
端口：1–65535 整数，逗号分隔，空 = 不限；`expiresAt`：必须晚于当前时间；
备注 ≤256。字段级错误用 `el-form-item` 的 `rules` 呈现，任何一项非法就不发请求
（服务端仍会再校验一次，前端校验只是省一次往返，不是信任边界）。

### D5：列表不显示活跃流，IP 池概览来自 `vpn-nodes`

规格 §10.3 要求列表顶部展示 IP 池使用概览，但该数据只由 `GET /api/v1/vpn-nodes` 提供，
而它在本版是 501。因此概览区做成**条件渲染**：拿到数据就显示总量/已分配/剩余，
拿不到（501）就显示一行说明。不把 501 缓存成「0 已分配」这种假数据。

### D6：`/vpn` 不加 admin 门禁，可见范围由服务端决定

与 `/routes`、`/credentials` 一致：普通用户只看自己的 peer（阶段 4 已在分页语义内过滤），
管理员看全部。前端不做角色分支，避免与服务端过滤产生两套真相。

## 5. 全局约束

1. 每个任务先写失败测试，实测红灯原文，再写最小实现（`AGENTS.md` TDD 流程）。
2. `web/package.json` 与 `package-lock.json` 零改动（不新增依赖）。
3. Go 代码、`docs/api/openapi.yaml`、`migrations/`、`internal/**` 零改动。
4. i18n 两侧键集合必须完全一致；不得留 `TODO` 或英文占位。
5. 一个任务一个提交，`<type>(<scope>): <subject>`，subject ≤50 字、祈使句、无句号。
6. 私钥、幂等键、确认头一律不落日志；测试里不得断言明文私钥被写入任何持久层。
7. 文案不得宣称数据面可用（阶段 4 的文档纪律同样适用于前端）。
8. 时点记录零改写：阶段 4 的计划与 PR 记录只链接不修改。

## 6. 文件清单

| 文件 | 任务 | 内容 |
| --- | --- | --- |
| `docs/superpowers/plans/2026-09-22-vpn-phase5-admin-console.md` | — | 本计划 |
| `web/src/api/vpn.ts` | T1 | 类型 + 9 个操作的客户端 |
| `web/src/tests/vpn-api.spec.ts` | T1 | 查询串、幂等键、reveal 三前置、类型不含密封列 |
| `web/src/views/vpn-errors.ts` | T2 | 稳定码 → i18n 文案映射（照 `remote-server-errors.ts`） |
| `web/src/views/VpnPeers.vue` | T2 | 列表 + 创建/编辑 + 使用说明抽屉 |
| `web/src/tests/vpn.spec.ts` | T2 | 挂载测（`stubApi`）+ 源码标记断言 |
| `web/src/router.ts` | T3 | `{path:'/vpn', component:VpnPeers, meta:{auth:true}}` |
| `web/src/layouts/AppShell.vue` | T3 | `menuItems()` 插入 `['/vpn','navigation.vpn']` |
| `web/src/i18n/messages/zh-CN.ts`、`en-US.ts` | T3 | `navigation.vpn` 与 `vpn.*` 全量文案 |
| `web/src/tests/vpn-i18n.spec.ts` | T3 | `vpn.*` 键在两侧都存在且非空 |
| `web/src/views/Servers.vue` | T4 | 节点 VPN 状态区块（含 501 状态） |
| `web/src/tests/servers-vpn.spec.ts` | T4 | 200 渲染与 501 降级各一例 |
| `docs/pull-requests/2026-09-22-vpn-phase5-admin-console.md` | T5 | PR 记录 |
| `docs/pull-requests/README.md`、`docs/superpowers/plans/README.md` | T5 | `scripts/gen_doc_index.py` 生成 |

### 明确不改

- `web/package.json`、`web/package-lock.json`（零新依赖）。
- 任何 Go 文件、`docs/api/openapi.yaml`、`migrations/`、`deploy/`。
- 阶段 4 的计划与 PR 记录、ADR 0002、既有 spec（时点记录零改写）。
- `web/src/api/client.ts`（`APIError` 已能承载 `domain`，不需要扩展）。

## 7. 任务分解

### T1：`api/vpn.ts`

**Step 1（红灯）**：`web/src/tests/vpn-api.spec.ts` 用 `stubApi` 断言
`listVpnPeers({status,keyword,cursor,limit})` 拼出的查询串、`createVpnPeer` 带
`Idempotency-Key`、`revokeVpnPeer` 用 `DELETE`、`rotateVpnPeer` 用 `POST .../rotate`、
`revealVpnPeerConfig` 同时带 `X-VPN-Config-Reveal-Confirm` 与 `Idempotency-Key`
且 body 为 `{acknowledgeRisk:true}`、`listVpnNodes` 与 `listVpnPeerFlows` 存在。
Run `npm test -- --run` → 失败 `Cannot find module '../api/vpn'`。

**Step 2（实现）**：`web/src/api/vpn.ts`；类型逐字对齐 `VPNPeer` projection。

**Step 3（绿灯 + 提交）**：`feat(web): add the vpn api client`。

### T2：`VpnPeers.vue` + 错误映射

**Step 1（红灯）**：`web/src/tests/vpn.spec.ts` 挂载视图，断言：列表渲染名称与 VPN IP；
创建表单校验非法 CIDR 时不发请求；reveal 返回 409 时渲染 info 级说明且不抛全局错误；
抽屉关闭后私钥 `ref` 被清空（用 `container.textContent` 不再包含明文断言）。
Run → 失败 `Cannot find module '../views/VpnPeers.vue'`。

**Step 2（实现）**：`vpn-errors.ts` + `VpnPeers.vue`（复用 `PageHeader`/`DataState`/`StatusTag`、
`token-form.ts` 的 agent 选择、`Routes.vue` 的 CIDR 解析范式）。

**Step 3（绿灯 + 提交）**：`feat(web): manage vpn peers from the console`。

### T3：路由、导航与 i18n

**Step 1（红灯）**：`vpn-i18n.spec.ts` 断言 `navigation.vpn` 与 `vpn.*` 关键键在 zh/en 两侧都存在且非空；
`routes.spec.ts` 风格断言 `router.getRoutes()` 含 `/vpn` 且 `meta.auth === true`、`meta.admin` 未设置；
断言 `AppShell.vue` 的 `menuItems()` 在 `/credentials` 之后含 `['/vpn','navigation.vpn']`。
Run → 失败（键与路由都不存在）。

**Step 2（实现）**：三处最小改动 + 两份文案。

**Step 3（绿灯 + 提交）**：`feat(web): route and translate the vpn console`。

### T4：`Servers.vue` 节点 VPN 状态

**Step 1（红灯）**：`servers-vpn.spec.ts` 用 `stubApi` 分别给出 200（节点启用、子网、已分配、peer 数、
ICMP 能力）与 501 `vpn_not_implemented` 两种响应，断言前者渲染数值、后者渲染说明文案且**不**渲染 `0`。
Run → 失败（`listVpnNodes` 未被 `Servers.vue` 调用 / 断言的文案键不存在）。

**Step 2（实现）**：`Servers.vue` 增加区块，条件渲染，501 走 D3 的状态而非错误。

**Step 3（绿灯 + 提交）**：`feat(web): show vpn status on the node page`。

### T5：构建、嵌入产物、门禁与 PR 记录

`npm test -- --run` 全绿 → `npm run build` → `scripts/verify-web-embed.sh` →
`go build ./...`（确认 embed 目录仍可编译）→ `go test ./scripts/ -count=1`（文档主张守卫）→
`python3 scripts/gen_doc_index.py` 两次确认幂等 → 死链核验 →
写 `docs/pull-requests/2026-09-22-vpn-phase5-admin-console.md` →
提交 `docs(pull-requests): record vpn admin console`。

## 8. 整体验证

```bash
cd web && npm test -- --run && npm run build && cd ..
bash scripts/verify-web-embed.sh
go build ./... && go vet ./... && go test ./scripts/ -count=1
git status --porcelain go.mod go.sum web/package.json web/package-lock.json   # 必须为空
git diff --check
python3 scripts/gen_doc_index.py && python3 scripts/gen_doc_index.py && git status --porcelain
git diff --name-status codex/vpn-phase4-pure-logic-and-api..HEAD -- docs/superpowers docs/pull-requests docs/architecture/adr
```

Go 侧只跑 `go build`/`go vet`/`scripts` 守卫：本阶段不改任何 Go 源文件，
完整 `go test ./...` 与 `-race` 在阶段 4 已对同一份 Go 代码验证过，重跑不产生新信息。
无 Docker/Compose 与 Schema 改动，容器与迁移验证不适用。

## 9. 回滚注意事项

- 全部是新增文件 + 四处受控修改（`router.ts` 一行、`AppShell.vue` 一个数组项、
  两份 i18n 文案、`Servers.vue` 一个区块），`git revert` 对应提交即可，无数据与配置迁移。
- 构建产物未入库，回滚后需要重跑 `npm run build` 让 `internal/server/web_dist` 与源码一致；
  `scripts/verify-web-embed.sh` 会在这两者漂移时失败，因此漏跑会被门禁发现。
- 本阶段不改变任何服务端行为：即使前端已合并而阶段 4 被回滚，`/vpn` 页面会收到 404
  并渲染通用错误——因此合并顺序必须是阶段 4 在前（规格 §15 的依赖链 4 → 5）。
- i18n 键一旦发布即被两侧守卫锁定，删除键必须同时删两侧，否则 `i18n.spec.ts` 失败。
