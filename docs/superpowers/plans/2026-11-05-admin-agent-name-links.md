# 管理后台 Agent 引用统一显示名称并可跳转实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 后台所有展示 Agent 引用的位置（托管路由列表、客户端详情的关联 Agent 与监听入口、服务令牌绑定列与详情、VPN peer 列表与详情、远程服务器列表与详情、审计日志中 `resourceType=agent` 的资源 ID）一律显示 Agent **名称**，并且点击可跳转到该 Agent 的详情页 `/agents/{id}`；解析不到名称时回退显示原始 ID。请求参数、分页、排序、权限语义、协议与 API 契约全部不变。

**Architecture:** 改动全部在 `web/` 前端，分三层，互不依赖。第一层 `web/src/stores/agentIndex.ts`：Pinia store，把“当前调用者可见的 Agent 清单”收敛为控制台里唯一一份 Agent 身份索引，加载复用既有 `loadAgentsForSelection(getAgents)`（游标分页、每页 500），并发去重、失败静默降级、`refresh()` 就地覆盖。第二层 `web/src/components/AgentRef.vue`：唯一渲染出口，用 `router-link` 指向 `/agents/{id}`，标签优先级为“调用方显式传入的名称 > 索引命中的名称 > 原始 ID”，`title` 恒为完整 ID。第三层各视图：把裸 `prop="agentId"` 列和 `formatList(agentIds)` 文本替换成 `<AgentRef>`。不动 Go 代码、DDL、OpenAPI 与协议帧，因此不触发 Schema 版本与升级文档流程，属于前端展示层的向后兼容变更。

**Tech Stack:** Vue 3 `<script setup>` + TypeScript、Pinia 4（`defineStore` setup 风格，与 `web/src/stores/auth.ts` 一致）、vue-router `router-link`、Element Plus 表格列插槽、vue-i18n（zh-CN / en-US 双份）、Vitest + jsdom（既有两种风格：挂载行为断言与 `readFileSync` 源码断言）。不新增第三方依赖。

**Spec:** 无新增设计规格与 ADR。引用的既有事实源与约束：
- AGENTS.md §3「单一数据源」、§8「三处以上重复必须抽象」「函数/复杂逻辑必须注释为什么」、Web 管理后台交互基线（服务端排序、cursor 分页、筛选参数化）、§13「核心逻辑必须有测试」、§12 发布回滚要求。
- `docs/user-guide/agent.md:44`：Agent ID 是路由与客户端引用 Agent 的稳定标识；名称只是标签，不得取代 ID 成为标识。
- `migrations/ddl.sql:191-201`：`agents.name` 无唯一索引，名称可重复，因此任何以名称替代 ID 的展示都必须保留可读到 ID 的通道（本计划用 `title` + 详情页 `IdCell`）。
- `docs/user-guide/server-admin.md:63`、`:83`、`:246`、`:266`：活文档分别描述 Agent 列表、Client 观测、审计日志、Service Token 页面，必须随本次显示层变更同步。
- 既有实现范式：`web/src/views/VpnPeers.vue:292` 的本地 `agentName(id)` 解析、`web/src/views/RemoteServers.vue:559` 的服务端 `agentName` 字段、`web/src/components/IdCell.vue` 的“中间截断 + 悬停完整值”。

## 背景：逐处盘点（HEAD `80de13c`）

需要变更的展示点（当前都输出原始 ID，或只输出名称但不可点击）：

| # | 页面 | 位置 | 当前渲染 | 目标 |
| --- | --- | --- | --- | --- |
| 1 | 托管路由列表 | `web/src/views/Routes.vue:31` | `<el-table-column prop="agentId">` 原样 ID | 名称 + 链接 |
| 2 | 客户端详情 · 监听入口 | `web/src/views/Clients.vue:124` | `prop="agentId"` 原样 ID | 名称 + 链接 |
| 3 | 客户端详情 · 关联 Agent | `web/src/views/Clients.vue:106` | `formatList(detailClient.agentIds)` 顿号拼 ID | 每个 ID 一个名称链接，空则 `—` |
| 4 | 服务令牌列表 · 绑定列 | `web/src/views/Tokens.vue:20` → `tokenBinding()` `:185-189` | `row.agentId \|\| row.nodeId`、client 型走 `formatAgentScope()` `:190-193` 拼 ID | agent/client 型显示名称链接；server_node 型不变 |
| 5 | 服务令牌详情 · Agent ID | `web/src/views/Tokens.vue:77` | `detailToken.agentId \|\| '—'` | 名称 + 链接 |
| 6 | 服务令牌详情 · 授权范围 | `web/src/views/Tokens.vue:87` | `formatList(detailToken.scope.agentIds)` | 每个 ID 一个名称链接 |
| 7 | VPN peer 列表 · 出口 Agent | `web/src/views/VpnPeers.vue:39` | `agentName(row.agentId)` 纯文本 | 名称 + 链接（复用组件） |
| 8 | VPN peer 详情 | `web/src/views/VpnPeers.vue:122` | 同上 | 名称 + 链接 |
| 9 | 远程服务器列表 | `web/src/views/RemoteServers.vue:33` | `agentName(row)` 纯文本，值来自服务端 `agentName` | 名称 + 链接，服务端名称优先 |
| 10 | 远程服务器详情 | `web/src/views/RemoteServers.vue:157` | 同上（`<dt>/<dd>` 区块） | 名称 + 链接 |
| 11 | 审计日志列表 · 资源 ID | `web/src/views/AuditLogs.vue:61` | 所有资源类型共用 `IdCell` | 仅 `resourceType === 'agent'` 时改用名称链接 |
| 12 | 审计日志详情 · 资源 ID | `web/src/views/AuditLogs.vue:90` | 纯文本 ID | 同上条件 |
| 13 | 客户端筛选 · Agent | `web/src/views/Clients.vue:30` | 要求手填 ID 的文本框 | 可搜索下拉（值仍是 ID，见任务 7） |

明确**不改**的地方（保持现状，避免扩大范围）：
- `web/src/views/Agents.vue:20-24` 代理节点列表：名称列 + `IdCell` 是 ID 的权威展示页。
- `web/src/views/AgentDetail.vue:13` 与 `:135`：详情页的 “Agent ID” 与关闭连接确认框必须显示精确 ID（`docs/user-guide/server-admin.md:79` 已把它写成操作前置条件）。
- 令牌/VPN/远程服务器**表单里的 Agent 下拉**（`Tokens.vue:42,53,123`、`RemoteServers.vue:110`、`VpnPeers.vue:74`）：option 标签已是 `${name} · ${id}`，本身可搜索。
- `web/src/views/Tunnels.vue`：列表没有 Agent 列，本次不新增列。
- 客户端连接租约表、Server 节点列、Token 列：不是 Agent 引用。
- 概览页 Patch 面板（`Dashboard.vue:73`）：已按名称展示，且用途是选择目标，不改。

## 架构决策

1. **前端身份索引，而不是给每个接口加 `agentName`。** 后台在改动前就已经为下拉框整表拉取 Agent 清单：`Tokens.vue:248`、`Routes.vue:381`、`VpnPeers.vue:433` 用 `loadAgentsForSelection(getAgents)`（每页 500 直到取尽），`RemoteServers.vue:268` 拉 200，`Dashboard.vue:73` 拉 32。既然数据已经在浏览器里，索引就是唯一新增成本，而且能把这四处重复收敛为一次带缓存的请求（AGENTS.md §8）。反过来走后端 join 要给 `/api/v1/routes`、`/api/v1/clients`、`/api/v1/tokens`、`/api/v1/vpn/peers` 四个接口新增派生字段，连带 OpenAPI、`docs/README.md`、Go handler/service 契约测试与前端类型，改动面大一个数量级且仍然需要前端持有 ID 才能跳转。`RemoteServers` 已有的服务端 `agentName` 不删除：`AgentRef` 接受显式名称入参并优先使用，避免把一次已经付过成本的 join 变废。
2. **名称是标签，ID 仍是标识。** `title` 恒等于完整 Agent ID，解析失败时标签直接退回 ID（等宽字体，和现在一模一样），因此“两个人重名”“Agent 已删除”“读不到权限外 Agent”三种情况都不会误导。跳转仍用 ID，绝不做“点名称按名称搜索”。
3. **解析不到就降级，绝不阻塞列表。** `ensureLoaded()` 只在 `status === 'idle'` 时发请求；失败置 `status = 'error'` 并吞掉错误，本会话内不再自动重试（避免每页 N 个单元格对同一个 5xx 重试 N 次），显式刷新走 `refresh()`。这样身份索引挂掉时页面退化为今天的行为，而不是变成空白页。
4. **权限边界不变。** 索引来自 `GET /api/v1/agents`，服务端已按 owner 过滤；对读取者不可见的 Agent 引用只会显示裸 ID，不会泄露他人 Agent 的名称。这是选择“前端索引”而非“后端 join 到新接口”的安全理由之一：不新增任何跨 owner 的信息出口。
5. **`router-link` 而不是 `router.push`。** 真实 `<a href>` 才能中键/新标签打开，也符合 `web/src/layouts/NavRail.vue:3` 的既有做法；测试断言可以直接查 `a[href="/agents/agent-abc"]`。代价是挂载测试必须提供 Pinia 与 router 插件，本计划把这一点写成明确的测试任务（任务 8）。
6. **筛选条件继续是 ID。** `filters.agentId`、`scope.agentIds`、`peer.agentId`、`route.agentId` 全部保持 ID 值；`docs/user-guide/server-admin.md:85` 描述的筛选参数与 `internal/storage/client_repository.go:468-471` 的精确匹配语义（`INSTR` 引号化，`agent-1` 不会命中 `agent-12`）都不受影响。

## Global Constraints

- 不修改任何 `internal/` Go 代码、`migrations/`、`docs/api/openapi.yaml`、协议帧与能力协商。
- 不修改 `web/src/api/client.ts` 的请求参数与类型定义（`getAgents` 签名保持现状）。
- 不新增 i18n 键，除非任务明确列出；已新增的必须同时写 `web/src/i18n/messages/zh-CN.ts` 与 `en-US.ts`，因为 `web/src/tests/i18n.spec.ts:44` 断言两份键树结构完全一致。
- 空值仍渲染 `—`（`AgentRef` 的 `agentId` 为空时），保持与 `VpnPeers.vue:293`、`Tokens.vue:77` 现有观感一致。
- 列表列的 `prop` 与服务端排序字段不改：`Routes.vue:31`、`Clients.vue:124`、`AuditLogs.vue:61` 都不是 `sortable="custom"` 列，`onSortChange` 白名单（`Routes.vue:376`）也不含 `agentId`，因此不存在“按名称排序”的隐含契约。
- 每个任务先写失败测试并观察红灯输出，再做最小实现；不得为通过新测试而放宽既有断言（只允许按任务描述精确替换失效断言）。
- 未获得用户明确授权，不执行 commit、push、merge 或创建远端 PR。

## 文件清单

新增：
- `web/src/stores/agentIndex.ts`
- `web/src/components/AgentRef.vue`
- `web/src/tests/agent-index.spec.ts`
- `web/src/tests/agent-ref.spec.ts`

修改：
- `web/src/views/Routes.vue`、`web/src/views/Clients.vue`、`web/src/views/Tokens.vue`、`web/src/views/VpnPeers.vue`、`web/src/views/RemoteServers.vue`、`web/src/views/AuditLogs.vue`、`web/src/views/Agents.vue`
- `web/src/views/token-form.ts`（仅把 `loadAgentsForSelection` 的调用方收敛到 store；`filterAgents` 保留）
- `web/src/tests/audit-logs-view.spec.ts`、`web/src/tests/clients-view.spec.ts`、`web/src/tests/routes.spec.ts`、`web/src/tests/agent-token-workflows.spec.ts`、`web/src/tests/vpn.spec.ts`、`web/src/tests/remote-servers-view.spec.ts`
- `web/src/i18n/messages/zh-CN.ts`、`web/src/i18n/messages/en-US.ts`
- `docs/user-guide/server-admin.md`
- `docs/superpowers/plans/README.md`（由 `python3 scripts/gen_doc_index.py` 生成，禁止手改）

---

### 任务 1: Agent 身份索引 store

**Files:**
- Create: `web/src/stores/agentIndex.ts`
- Test: `web/src/tests/agent-index.spec.ts`

**Interfaces:**
- `export type AgentIndexStatus = 'idle' | 'loading' | 'ready' | 'error'`
- `export const useAgentIndex: () => { agents: Ref<Agent[]>; status: Ref<AgentIndexStatus>; ensureLoaded: () => Promise<void>; refresh: () => Promise<void>; nameOf: (id?: string | null) => string }`
- 内部加载器固定为 `loadAgentsForSelection`（`web/src/views/token-form.ts:27`）+ `getAgents`（`web/src/api/client.ts:250`），两者签名不改；因此 `web/src/tests/agent-token-workflows.spec.ts:20` 对分页拉全的覆盖继续有效。
- 复用 `web/src/stores/auth.ts` 的 setup-store 写法与 `defineStore('agent-index', () => {...})` 命名。

**TDD:**
- [x] 写失败测试 `web/src/tests/agent-index.spec.ts`，用 `setActivePinia(createPinia())` 与 `vi.mock('../api/client', ...)`（沿用 `agents-view.spec.ts:9` 的 actual-spread 风格），断言五件事：① 首次 `nameOf('agent-a')` 为 `''` 且只发一次 `{ limit: 500 }` 请求；② 并发 `ensureLoaded()` 两次只触发一次列表请求；③ 游标分页时 `nextCursor` 会继续拉取直到取尽；④ `getAgents` reject 后 `status === 'error'` 且后续 `ensureLoaded()` 不再发请求；⑤ `refresh()` 强制重新拉取并在成功前保留旧名称。
- [x] 运行 `cd web && npx vitest run src/tests/agent-index.spec.ts`，预期红灯：`Cannot find module '../stores/agentIndex'`。
- [x] 最小实现：`agents` + `names` computed（`new Map(agents.map(a => [a.id, a.name]))`）+ 模块内 `inflight` 变量做去重；`ensureLoaded` 判断 `status.value !== 'idle'` 直接返回；`refresh` 走同一条加载路径但跳过状态判断；catch 只写 `status.value = 'error'`。
- [x] 预期通过：`cd web && npx vitest run src/tests/agent-index.spec.ts`。

**为什么这样设计：** `status === 'error'` 后不再自动重试，是因为一次列表页可能有上百个引用单元；失败即静默降级为裸 ID，重试交由用户点页面刷新按钮（任务 8 在 `Agents.vue` 的改名路径显式调用 `refresh()`）。

---

### 任务 2: `AgentRef` 单元格组件

**Files:**
- Create: `web/src/components/AgentRef.vue`
- Modify: `web/src/tests/agent-ref.spec.ts`（新建，本任务写组件部分）

**Interfaces:**
- `defineProps<{ agentId?: string | null; name?: string | null }>()`
- 渲染规则：`agentId` 为假值 → `<span class="agent-ref agent-ref--empty">—</span>`；否则 `<router-link :to="\`/agents/${agentId}\`" :title="agentId" class="agent-ref" :class="{ 'tm-mono': !resolved }">{{ label }}</router-link>`，其中 `label = props.name || store.nameOf(props.agentId) || props.agentId`，`resolved = Boolean(props.name || store.nameOf(props.agentId))`。
- 组件自身在 `onMounted` 调用 `void useAgentIndex().ensureLoaded()`（同任务 1 的去重保证 N 个单元格只发一次请求）。
- 样式使用既有 token：默认 `color: var(--tm-brand)`，`:hover { color: var(--tm-brand-hover); text-decoration: underline; }`，`.tm-mono` 沿用 `web/src/styles/*.css` 中的等宽类；未解析时不显示下划线，视觉与 `IdCell.vue` 的 ID 文本一致。

**TDD:**
- [x] 写失败测试 `web/src/tests/agent-ref.spec.ts`：本地 `createRouter({ history: createMemoryHistory(), routes: [{ path: '/agents/:id', component: { render: () => null } }] })` + `createPinia()` 挂载；桩 `getAgents` 返回 `{ items: [{ id: 'agent-edge', name: 'edge-01', ... }], nextCursor: '' }`。断言：① 解析成功时文本是 `edge-01`、`href === '/agents/agent-edge'`、`title === 'agent-edge'`；② 桩 `getAgents` reject 时文本回退为 `agent-edge` 且 `href` 仍存在；③ 传入 `name` 时优先使用它，即使索引里没有这个 Agent；④ `agentId` 为空时输出 `—` 且没有 `<a>`。
- [x] 运行 `cd web && npx vitest run src/tests/agent-ref.spec.ts`，预期红灯：`Cannot find module '../components/AgentRef.vue'`。
- [x] 最小实现后预期通过。

---

### 任务 3: 托管路由与客户端详情接入

**Files:**
- Modify: `web/src/views/Routes.vue`（`:31`）
- Modify: `web/src/views/Clients.vue`（`:106`、`:124`）
- Modify: `web/src/tests/routes.spec.ts`、`web/src/tests/clients-view.spec.ts`

**Interfaces:**
- `Routes.vue:31` 改为带默认插槽的列：`<el-table-column :label="t('routes.agent')" min-width="180"><template #default="{ row }"><AgentRef :agent-id="row.agentId" /></template></el-table-column>`。列继续绑定 `ManagedRoute.agentId`（`web/src/api/client.ts:60`），不引入新字段。
- `Clients.vue` 监听入口子表（`:124`）同样换成 `AgentRef`；关联 Agent 描述项（`:106`）改为 `<span v-for>` 渲染 `detailClient.agentIds`（`web/src/api/client.ts:215`）的名称链接，空数组仍走 `—`。`formatList`（`Clients.vue:362`）继续服务 token / Server 节点两列，不改签名。

**TDD:**
- [x] 先在 `routes.spec.ts` 加源码断言：托管路由列必须出现 `<AgentRef :agent-id="row.agentId" />` 且不再出现裸 `prop="agentId"`；在 `clients-view.spec.ts` 增加挂载断言：桩 `listClients` 返回带 `listeners: [{ protocol: 'tcp', listenAddress: '127.0.0.1:6021', agentId: 'agent-edge', enabled: true }]` 与 `agentIds: ['agent-edge']` 的实例，桩 `getAgents` 返回名为 `edge-01` 的 Agent，打开详情后断言 `.agent-ref` 文本为 `edge-01`、`href` 为 `/agents/agent-edge`。
- [x] 运行 `cd web && npx vitest run src/tests/routes.spec.ts src/tests/clients-view.spec.ts`，预期红灯：`expected ... to contain '<AgentRef :agent-id="row.agentId" />'`；`clients-view.spec.ts` 因组件依赖 Pinia/router 报 `getActivePinia() was called but there was no active Pinia`。
- [x] 实现两视图改动；把 `clients-view.spec.ts:11` 的 `vi.mock('vue-router', ...)` 改为 actual-spread 并 `app.use(memoryRouter)`、`app.use(createPinia())`（沿用 `vpn.spec.ts:13` 的写法），把桩改为 `{ ...actual, listClients, getClientDetail, listClientConnections, getAgents }` 且 `getAgents` 默认 `mockResolvedValue({ items: [], nextCursor: '' })`。
- [x] 预期通过：同一条命令绿灯。

---

### 任务 4: 服务令牌列表与详情接入

**Files:**
- Modify: `web/src/views/Tokens.vue`（`:20` 列、`:77` 详情、`:87` 详情 scope、`:185-193` 两个格式化函数）
- Modify: `web/src/tests/agent-token-workflows.spec.ts`

**Interfaces:**
- 绑定列（`Tokens.vue:20`，值来自 `tokenBinding()` `:185-189`）改为分支渲染：`row.type === 'agent'` → `AgentRef(:agent-id="row.agentId")`；`row.type === 'client'` → `row.scope.agentIds?.length` 时渲染名称链接列表，否则 `t('tokens.allAgents')`；`server_node` → 继续 `formatServerNodeScope(row)`。列容器保留 `binding` 类以维持现有省略与悬停样式。
- `formatAgentScope()`（`:190-193`）从“返回拼好的 ID 串”改为“返回 ID 数组”（`agentScopeIds(row): string[]`），文本渲染交给模板里的 `AgentRef`；这是唯一被删除的字符串拼接路径。
- 详情两项（`:77`、`:87`）改为 `AgentRef` 与名称链接列表。

**TDD:**
- [x] 在 `agent-token-workflows.spec.ts` 新增挂载或源码断言：令牌列表 source 必须出现 `formatServerNodeScope` 与 `<AgentRef`，且不得出现 `row.scope.agentIds.join(', ')`；断言 `agent` 型令牌详情显示名称而非 `agent-` 前缀 ID。
- [x] 运行 `cd web && npx vitest run src/tests/agent-token-workflows.spec.ts`，预期红灯：`expected source to contain '<AgentRef'`。
- [x] 实现视图改动，预期通过。
- [x] 回归 `cd web && npx vitest run src/tests/tokens-contrast.spec.ts`（同页样式基线），预期保持绿灯。

---

### 任务 5: VPN peer 与远程服务器接入

**Files:**
- Modify: `web/src/views/VpnPeers.vue`（`:39`、`:122`、`:292-294`）
- Modify: `web/src/views/RemoteServers.vue`（`:33`、`:157`、`:559`）
- Modify: `web/src/tests/vpn.spec.ts`、`web/src/tests/remote-servers-view.spec.ts`

**Interfaces:**
- 删除 `VpnPeers.vue:292` 的本地 `agentName(agentId)`（它的职责已由 `AgentRef` 承担，属于 AGENTS.md §8 的重复抽象）；`:290` 的 `visibleAgents` 继续用 `filterAgents` 服务表单下拉，不受影响。
- `RemoteServers.vue:559` 的 `agentName(row)` 改为把服务端 `agentName` 作为显式入参传给组件：`<AgentRef :agent-id="row.agentId" :name="row.agentName" />`；`RemoteServer.agentName?: string` 已存在于 `web/src/api/remote-servers.ts:20`，服务端投影（`internal/server/remote_server_api.go:32`）不改。

**TDD:**
- [x] 在 `vpn.spec.ts` 与 `remote-servers-view.spec.ts` 先加断言：两个列表列必须出现 `<AgentRef`，`RemoteServers.vue` 必须出现 `:name="row.agentName`；VPN 断言不得再出现 `agentName(row.agentId)`。
- [x] 运行 `cd web && npx vitest run src/tests/vpn.spec.ts src/tests/remote-servers-view.spec.ts`，预期红灯：`expected source to contain '<AgentRef'`。
- [x] 实现两视图改动，预期通过。

---

### 任务 6: 审计日志按资源类型接入

**Files:**
- Modify: `web/src/views/AuditLogs.vue`（`:61`、`:90`）
- Modify: `web/src/tests/audit-logs-view.spec.ts`

**Interfaces:**
- 仅当 `row.resourceType === 'agent'` 时把 `IdCell` 换成 `<AgentRef :agent-id="row.resourceId" />`，其余资源类型（`service_token`、`client`、`credential`、`webssh_session`、`user`、`server_node`、`remote_server`、`oidc_provider`、`user_device`、`user_mfa` 等，来源见 `internal/server/agent_connection_audit.go:42`、`internal/server/token_service.go:698`）保持 `IdCell`。
- 详情抽屉（`:90`）用同一条件；判定收敛为视图内一个 `isAgentResource(row)` 辅助函数，避免列表与抽屉两处各写一遍字符串比较。

**TDD:**
- [x] 先扩展 `audit-logs-view.spec.ts`：`beforeEach` 补 `createPinia()`，`vi.mock('vue-router')` 改 actual-spread 并 `app.use(memoryRouter)`；桩 `getAgents` 返回 `{ id: 'agent-abc', name: 'edge-01' }`；沿用既有 `agent.updated` / `agent-abc` 用例，断言该行文本含 `edge-01` 且 `a[href="/agents/agent-abc"]` 存在，而 `proxy_route` 行仍渲染裸 `route-1`（`agent-abc` 不再出现在该单元格可见文本里）。
- [x] 运行 `cd web && npx vitest run src/tests/audit-logs-view.spec.ts`，预期红灯：先是 `getActivePinia() was called but there was no active Pinia`，补插件后是 `expected 'agent-abc' to contain 'edge-01'`。
- [x] 实现条件渲染，预期通过。

---

### 任务 7: 客户端 Agent 筛选改为可搜索下拉

**Files:**
- Modify: `web/src/views/Clients.vue`（`:30`、`:206`、`:299`）
- Modify: `web/src/i18n/messages/zh-CN.ts`、`web/src/i18n/messages/en-US.ts`
- Modify: `web/src/tests/clients-view.spec.ts`

**Interfaces:**
- `filters.agentId` 值语义不变（仍是 Agent ID，直接进 `listClients({ agentId })` → `internal/server/client_repository.go:468-471` 的精确匹配），控件由 `el-input` 换成 `el-select filterable clearable`，选项来自 `filterAgents(agentIndex.agents, keyword)`，与 `VpnPeers.vue:74` 的 `setAgentFilter` 用法一致；`reset()`（`:299`）继续清空该字段。
- 新增 i18n 键 `clients.searchAgents`：zh `输入名称或 ID 搜索`、en `Search by name or ID`（与 `tokens.searchAgents`、`vpn.form.searchAgents` 同措辞，两份必须同时添加以满足 `i18n.spec.ts:44`）。

**TDD:**
- [x] 先在 `clients-view.spec.ts` 断言筛选区出现 `v-model="filters.agentId"` 的 `el-select`、`t('clients.searchAgents')`，并断言选择某个 Agent 后 `listClients` 被以 `agentId: 'agent-edge'` 调用。
- [x] 运行 `cd web && npx vitest run src/tests/clients-view.spec.ts`，预期红灯：`expected source to contain 'clients.searchAgents'`。
- [x] 实现控件替换与两份 i18n 文案，预期通过；再跑 `npx vitest run src/tests/i18n.spec.ts` 确认键树结构一致。

---

### 任务 8: 收敛重复的 Agent 清单加载并在改名后刷新

**Files:**
- Modify: `web/src/views/Tokens.vue`（`:173`、`:248`）、`web/src/views/Routes.vue`（`:381`）、`web/src/views/VpnPeers.vue`（`:433`）、`web/src/views/RemoteServers.vue`（`:268`）
- Modify: `web/src/views/Agents.vue`（`createAgent` / `updateAgent` 成功路径）
- Modify: `web/src/tests/routes.spec.ts:109`、`web/src/tests/remote-servers-view.spec.ts`、`web/src/tests/agents-view.spec.ts`

**Interfaces:**
- 四个视图不再各自 `getAgents(...)`，改为读取 `agentIndex.agents`（下拉选项用 `filterAgents(agentIndex.agents, agentFilter.value)`，`agentsLoading` 由 `agentIndex.status === 'loading'` 派生）；`loadAgentsForSelection` 只在 `agentIndex.ts` 内部使用。
- `Agents.vue` 创建与改名（含启用状态切换）成功后调用 `void agentIndex.refresh()`，使后台其他已打开页面的名称标签在下一次渲染即可更新；索引不清空旧值，因此刷新期间不会出现裸 ID 闪烁。
- `routes.spec.ts:109` 的源码断言由 `getAgents` 改为 `useAgentIndex`（这条断言的意图是“路由表单必须能选到 Agent”，收敛加载方式后仍成立，属精确替换而非放宽）。

**TDD:**
- [x] 先把 `routes.spec.ts:109` 改为 `expect(source).toContain('useAgentIndex')`；在 `agents-view.spec.ts` 断言改名成功后调用 `refresh`（桩 `getAgents` 被再次调用一次）；在 `remote-servers-view.spec.ts` 断言 source 不再出现 `getAgents({ limit: 200 })` 而出现 `agentIndex.agents`。
- [x] 运行 `cd web && npx vitest run src/tests/routes.spec.ts src/tests/agents-view.spec.ts src/tests/remote-servers-view.spec.ts`，预期红灯：三条断言分别报 `expected source to contain 'useAgentIndex'`、`expected getAgents to have been called 2 times`、`expected source not to contain 'getAgents({ limit: 200 })'`（后者按现有源码即失败）。
- [x] 按接口改四个视图与 `Agents.vue`，预期通过。
- [x] 全量回归 `cd web && npx vitest run`：唯一允许的既有变化是 `agent-token-workflows.spec.ts`（继续直接测 `getAgents` 分页与 `filterAgents`，不受影响）保持绿灯。

---

### 任务 9: 导航守卫扩面与活文档

**Files:**
- Modify: `web/src/tests/routes.spec.ts`（`in-app navigation` describe，`:266-312`）
- Modify: `docs/user-guide/server-admin.md`（`:63`、`:85`、`:108` 后、`:246`、`:266` 邻段）
- Modify: `docs/superpowers/plans/README.md`（生成）

**Interfaces:**
- 现有守卫只扫描 `src/views` 与 `src/layouts` 里的 `router.push(`（`navigationLiterals()` `:277-287`），而本计划把跳转写进 `src/components/AgentRef.vue` 的 `router-link :to="..."`，会绕过守卫。因此把扫描目录加 `src/components`，并把匹配集扩到 `to="..."` / `` to="`...`" `` 属性；`isDeclared()` 与 `segments()` 不改，`checked.length` 下限从 7 提到 9（新增计数来自 `AgentRef.vue` 与 `NavRail.vue` 的 `router-link`）。
- 活文档需要反映“显示名称 + 可跳转”，同时保留“ID 才是标识”的既有说法。具体改动：`Agent 列表与详情` 段补一句“其他页面引用 Agent 时显示名称并可跳转到本页详情；名称可重复，鼠标悬停显示 Agent ID”；`Client 运行观测与连接管理` 段把“Agent 筛选”描述改为可搜索下拉且值仍是 ID；`审计日志` 段把“资源 ID”描述补上“资源类型为 agent 时显示名称链接”；`Service Tokens` 与远程服务器段各补一句名称链接。

**TDD:**
- [x] 先跑 `cd web && npx vitest run src/tests/routes.spec.ts` 观察当前守卫对 `src/components` 完全不感知（`checked` 里没有 `AgentRef.vue` 的断言基础），把下限断言改为 9 后应红灯：`expected 7 to be greater than or equal to 9`。
- [x] 扩面实现后应绿灯，且 `checked` 至少包含 `src/components/AgentRef.vue /agents/*`。
- [x] 文档改动无需测试，但必须执行 `python3 scripts/gen_doc_index.py` 与 `git diff --check`，并确认 `docs/README.md` 未出现孤儿链接。

---

## 任务间依赖

- 任务 1 → 任务 2（组件依赖 store 的 `nameOf` / `ensureLoaded`）。
- 任务 2 → 任务 3/4/5/6/7（各视图只依赖 `AgentRef` 的 props 契约）。
- 任务 3/4/5/6/7 之间互不依赖，可并行；但都必须在任务 8 之前完成，否则任务 8 的“加载收敛”会与视图仍在直接调用 `getAgents` 的中间状态冲突。
- 任务 9 依赖任务 2（`AgentRef.vue` 必须存在，守卫扩面才有新增计数）。

## 验证命令

```bash
cd web && npx vitest run src/tests/agent-index.spec.ts src/tests/agent-ref.spec.ts
cd web && npm test -- --run
npm run build
./scripts/verify-web-embed.sh
git diff --check
```

前端产物按 `web/package.json:9` 的 `build` 脚本经 `scripts/sync-web-dist.mjs` 同步到 `internal/server/web_dist/`，因此 `./scripts/verify-web-embed.sh` 必须纳入本次验证；Go 侧无改动，无需 `go test ./...`，但 `go vet ./internal/server` 可作为嵌入产物仍能被 embed 的旁证（可选）。

## 回滚注意事项

- 变更全部位于 `web/`，回滚即 `git revert` 本次提交后重新 `cd web && npm run build` 并 `./scripts/verify-web-embed.sh`，不需要回滚数据库、配置或 API。
- 单点风险是 `AgentRef` 与 `agentIndex`：若线上出现索引请求风暴或名称错乱，可把任一视图的 `<AgentRef>` 还原为原 `prop=` 列（任务 3-7 各自独立可 revert），行为立即回到今天的裸 ID 显示，且不影响其他视图。
- 任务 8 是唯一带行为副作用的改动（下拉框数据源换成共享索引）。若某个视图的筛选出现回归，可单独回退该视图到本地 `loadAgentsForSelection` 调用，索引与新组件都不需要回退。
- 灰度顺序：本变更只影响管理后台静态资源，随 Server 二进制发布；发布后先核对“客户端详情、托管路由列表、审计日志”三个页面的跳转与降级，再确认全量。5 分钟内止损方案就是回退 `web/dist` 并重启 Server。

---

## 执行记录（2026-11-05）

计划全部任务已实现并通过验证：`cd web && npm test -- --run` 503 项全绿（新增
`agent-index.spec.ts` 6 项、`agent-ref.spec.ts` 6 项），`npm run build` 与
`./scripts/verify-web-embed.sh` 通过，`git diff --check` 干净。与计划的偏差与精确替换：

- 任务 4：`formatAgentScope()` 更名为 `bindingAgentIds()` 并返回 ID 数组，覆盖 `agent` 型
  单绑定与 `client` 型 scope 两种来源；`agent-token-workflows.spec.ts` 里失效的
  `toContain('formatAgentScope')` 按同一意图替换为 `toContain('bindingAgentIds')`。
- 任务 7：Agent 清单在**下拉框展开时**才 `ensureLoaded()`，而不是挂载即拉取。理由与任务 1
  一致：不碰筛选的控制台不应为一份用不到的清单付出请求；`clients-view.spec.ts` 里
  “没有 Agent 引用就不请求”的断言因此继续成立。
- 任务 8：`agents-view.spec.ts` 的“改名后重载列表”调用次数断言由 2 改为 3（挂载列表、
  改名后列表、共享索引），并新增“索引以 `{ limit: 500 }` 重新拉取”的断言。
- 任务 9：导航守卫新增按文件名点名的断言
  `src/components/AgentRef.vue /agents/${agentId}`，避免只靠计数下限而出现“扫描器什么也
  没看到仍然通过”的空转。
- 类型基线：`vue-tsc` 在本仓库不是发布门禁，改动前后 `src/` 报错为既有同源基线；本次新增
  2 条 `Tokens.vue` 的 `DefaultRow → ServiceToken`，与同文件其余 10 条行内 helper 调用完全
  同类（Element Plus 表格作用域行类型），未额外放宽 helper 签名。
