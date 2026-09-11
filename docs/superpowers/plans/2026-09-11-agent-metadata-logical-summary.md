# Agent Metadata 逻辑状态展示调整实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Agent 详情页的元数据概览符合“逻辑 Agent + 多实例连接池”架构，状态、活跃实例数和活跃连接数都从健康连接推导，不再被单实例元数据租约误导。

**Architecture:** 保留现有 `/api/v1/agents/{agentId}/metadata` 和 `/api/v1/agents/{agentId}/connections` 契约，只调整管理后台展示层。页面顶部改为逻辑 Agent 聚合概览，实例级字段继续留在实例列表中；逻辑在线状态由至少一条健康连接决定，实例在线状态由该实例至少一条健康连接决定。

**Tech Stack:** Vue 3 `<script setup>`、TypeScript、Element Plus、Vitest、Vite。

**Spec:** `docs/superpowers/specs/2026-09-09-logical-agent-connection-pool-design.md`

## Global Constraints

- 逻辑 Agent 在线状态必须满足规格中的规则：**A logical Agent is online when at least one pooled connection is healthy.**
- 实例在线状态必须满足规格中的规则：**A disconnected instance does not affect other instances.**
- 不修改后端 API 契约、数据库 Schema 或协议帧。
- 不引入新的后端字段；只使用现有的 `instances`、`connections`、`healthy`、`instanceId`、`reportedAt`、`updatedAt`。
- 顶部概览不再展示 `instanceId`、`nodeId`、`epoch`、`revision` 这类单实例字段；这些字段继续在实例列表中展示。
- 页面必须保持中英文国际化完整。
- 不改变现有权限模型和审计行为。

---

### Task 1: 增加失败测试，锁定逻辑状态语义

**Files:**
- Modify: `web/src/tests/agent-detail.spec.ts`

**Interfaces:**
- Consumes: `AgentMetadata`、`ClusterAgentConnection` 现有类型。
- Produces: 测试用例 `derives logical agent status from healthy pooled connections`，用于约束后续实现。

- [x] **步骤 1：编写失败测试**

在 `agent-detail.spec.ts` 的 `describe('agent detail cluster connections')` 中新增用例，核心断言如下：

```ts
it('derives logical agent status from healthy pooled connections', async () => {
  vi.mocked(listAgentConnections).mockReset().mockResolvedValue([
    { ...connections[0], instanceId: 'instance-live', healthy: true },
    { ...connections[1], instanceId: 'instance-offline', healthy: false },
  ])
  vi.mocked(getAgentMetadata).mockReset().mockResolvedValue({
    ...metadata,
    stale: true,
    instances: [
      {
        instanceId: 'instance-live', nodeId: 'node-live', epoch: 2, revision: 1, stale: true,
        reportedAt: '2026-09-09T10:00:00Z', updatedAt: '2026-09-09T10:00:00Z', items: [], connectionCount: 1,
      },
      {
        instanceId: 'instance-offline', nodeId: 'node-offline', epoch: 1, revision: 1, stale: true,
        reportedAt: '2026-09-08T10:00:00Z', updatedAt: '2026-09-08T10:00:00Z', items: [], connectionCount: 0,
      },
    ],
  })

  const { container, unmount } = await mountAgentDetail()
  try {
    const summary = container.querySelector('[data-test="agent-logical-summary"]')
    expect(summary).not.toBeNull()
    expect(summary?.textContent).toContain(i18n.global.t('agentDetail.fresh'))
    expect(summary?.textContent).not.toContain(i18n.global.t('agentDetail.instanceId'))
    expect(summary?.textContent).not.toContain(i18n.global.t('agentDetail.nodeId'))
    expect(summary?.textContent).not.toContain(i18n.global.t('agentDetail.epoch'))
    expect(summary?.textContent).not.toContain(i18n.global.t('agentDetail.revision'))

    const cells = [...summary?.querySelectorAll('td.el-descriptions__cell') || []]
    const activeInstances = cells.find(cell => cell.textContent?.trim() === i18n.global.t('agentDetail.activeInstances'))
    expect(activeInstances?.nextElementSibling?.textContent?.trim()).toBe('1')
    const activeConnections = cells.find(cell => cell.textContent?.trim() === i18n.global.t('agentDetail.activeConnections'))
    expect(activeConnections?.nextElementSibling?.textContent?.trim()).toBe('1')
  } finally {
    unmount()
  }
})
```

同文件中将已有 `counts only non-stale agent instances` 用例替换为实例状态用例：

```ts
it('shows instance status from healthy connections', async () => {
  vi.mocked(listAgentConnections).mockReset().mockResolvedValue([
    { ...connections[0], instanceId: 'instance-live', healthy: true },
    { ...connections[1], instanceId: 'instance-offline', healthy: false },
  ])
  vi.mocked(getAgentMetadata).mockReset().mockResolvedValue({
    ...metadata,
    instances: [
      { instanceId: 'instance-live', nodeId: 'node-live', epoch: 2, revision: 1, stale: true, reportedAt: '2026-09-09T10:00:00Z', updatedAt: '2026-09-09T10:00:00Z', items: [], connectionCount: 1 },
      { instanceId: 'instance-offline', nodeId: 'node-offline', epoch: 1, revision: 1, stale: true, reportedAt: '2026-09-08T10:00:00Z', updatedAt: '2026-09-08T10:00:00Z', items: [], connectionCount: 0 },
    ],
  })
  const { container, unmount } = await mountAgentDetail()
  try {
    const table = container.querySelector('[data-test="agent-instance-table"]')
    expect(table).not.toBeNull()
    expect(table?.textContent).toContain(i18n.global.t('agentDetail.fresh'))
    expect(table?.textContent).toContain(i18n.global.t('agentDetail.stale'))
  } finally {
    unmount()
  }
})
```

并在实例表上增加可测试选择器：

```html
<el-table :data="metadata.instances || []" class="metadata-table" data-test="agent-instance-table">
```

- [x] **步骤 2：运行测试确认失败**

```bash
cd web
npm test -- --run agent-detail.spec.ts
```

预期失败：

- `[data-test="agent-logical-summary"]` 不存在。
- 顶部状态仍显示 `Stale` / `已过期`。
- `activeInstances` 仍按 `!instance.stale` 统计。

---

### Task 2: 调整 Agent 详情页为逻辑 Agent 概览

**Files:**
- Modify: `web/src/views/AgentDetail.vue`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`

**Interfaces:**
- Consumes: `metadata`、`clusterConnections` 两个现有响应式状态。
- Produces:
  - `logicalOnline: boolean`
  - `activeInstanceCount: number`
  - `activeConnectionCount: number`
  - `instanceOnline(instanceId: string): boolean`
  - `latestReportedAt: string`
  - `latestUpdatedAt: string`

- [x] **步骤 1：实现聚合逻辑**

在 `<script setup>` 中新增：

```ts
const healthyConnections = computed(() => clusterConnections.value.filter(connection => connection.healthy))
const activeInstanceIds = computed(() => new Set(healthyConnections.value.map(connection => connection.instanceId)))
const logicalOnline = computed(() => healthyConnections.value.length > 0)
const activeInstanceCount = computed(() => activeInstanceIds.value.size)
const activeConnectionCount = computed(() => healthyConnections.value.length)

function instanceOnline(instanceId: string) {
  return activeInstanceIds.value.has(instanceId)
}
```

时间字段使用“最新非空值”：

```ts
const latestReportedAt = computed(() => {
  const values = (metadata.value?.instances || []).map(instance => instance.reportedAt).filter(Boolean)
  return values.length ? values.reduce((a, b) => (Date.parse(a) >= Date.parse(b) ? a : b)) : metadata.value?.reportedAt || ''
})
```

`latestUpdatedAt` 使用同样规则，但比较 `updatedAt`。

- [x] **步骤 2：调整顶部概览模板**

顶部卡片增加：

```html
<div data-test="agent-logical-summary">
  <div class="card-header">
    <span>{{ t('agentDetail.logicalSummary') }}</span>
    <StatusTag
      :kind="logicalOnline ? 'success' : 'warning'"
      :label="logicalOnline ? t('agentDetail.fresh') : t('agentDetail.stale')"
    />
  </div>
  <el-descriptions :column="3" border>
    <el-descriptions-item label="Agent ID">{{ metadata.agentId }}</el-descriptions-item>
    <el-descriptions-item :label="t('agentDetail.activeInstances')">{{ activeInstanceCount }}</el-descriptions-item>
    <el-descriptions-item :label="t('agentDetail.activeConnections')">{{ activeConnectionCount }}</el-descriptions-item>
    <el-descriptions-item :label="t('agentDetail.reportedAt')">{{ formatDate(latestReportedAt) }}</el-descriptions-item>
    <el-descriptions-item :label="t('agentDetail.updatedAt')">{{ formatDate(latestUpdatedAt) }}</el-descriptions-item>
  </el-descriptions>
</div>
```

概览字段只保留：

- `Agent ID`
- `Status`
- `Active instances`
- `Active connections`
- `Reported at`
- `Updated at`

删除顶部的：

- `Instance ID`
- `Node ID`
- `Epoch`
- `Revision`

- [x] **步骤 3：调整实例列表状态**

实例表状态列改为：

```html
<el-table-column :label="t('agentDetail.status')" width="110">
  <template #default="scope">
    <StatusTag
      :kind="instanceOnline(scope.row.instanceId) ? 'success' : 'warning'"
      :label="instanceOnline(scope.row.instanceId) ? t('agentDetail.fresh') : t('agentDetail.stale')"
    />
  </template>
</el-table-column>
```

实例表继续展示 `instanceId`、`nodeId`、`epoch`、`revision`、`connectionCount`、`reportedAt`。

- [x] **步骤 4：补充国际化文案**

如需新增标题，使用：

- 中文：`logicalSummary: '逻辑 Agent 状态'`
- 英文：`logicalSummary: 'Logical Agent status'`

如继续复用现有 `metadata` 标题，则不新增 key；本计划默认将顶部卡片标题改为 `logicalSummary`，避免用户把元数据租约状态误解为逻辑 Agent 在线状态。

- [x] **步骤 5：运行测试确认通过**

```bash
cd web
npm test -- --run agent-detail.spec.ts
```

预期通过：

- 有健康连接时，顶部和对应实例显示 `Online` / `在线`。
- 无健康连接时，顶部和实例显示 `Stale` / `已过期`。
- 顶部不再出现单实例字段。
- 活跃实例数按健康连接的 `instanceId` 去重统计。

---

### Task 3: 更新文档、构建和生产产物

**Files:**
- Modify: `docs/user-guide/server-admin.md`
- Modify: `internal/server/web_dist/` 下的 Vite 构建产物

**Interfaces:**
- Consumes: Task 2 的展示语义。
- Produces: 用户文档和生产静态资源。

- [x] **步骤 1：更新用户文档**

在 `docs/user-guide/server-admin.md` 中补充：

```markdown
Agent 详情页的“逻辑 Agent 状态”按连接池健康状态推导：至少一条健康连接即在线。元数据租约只表示最近一次元数据上报是否过期，不再单独作为逻辑 Agent 的在线状态。实例状态同样按该实例是否存在健康连接推导。
```

- [x] **步骤 2：运行前端完整验证**

```bash
cd web
npm test -- --run
npm run build
```

- [x] **步骤 3：同步生产产物**

确认 `npm run build` 输出到项目约定的 embed 目录，并确保 `internal/server/web_dist/` 更新。

- [x] **步骤 4：运行嵌入资源验证**

```bash
go test ./internal/server -run 'TestWeb|TestEmbedded' -count=1
```

如项目没有匹配的嵌入资源测试，则运行：

```bash
go test ./internal/server -count=1
```

- [x] **步骤 5：运行基础质量检查**

```bash
git diff --check
```

---

## 预期失败结果

在实现前，新增测试应失败，原因是：

1. 页面没有 `data-test="agent-logical-summary"`。
2. 顶部状态使用 `metadata.stale`，而不是健康连接。
3. 活跃实例数使用 `!instance.stale`，而不是健康连接的 `instanceId` 去重。
4. 顶部仍展示单实例字段。

## 最小实现

只修改 `AgentDetail.vue`、国际化文案、测试和用户文档，不修改后端接口和数据结构。

## 预期通过结果

1. 逻辑 Agent 有健康连接时，顶部状态为在线。
2. 实例有健康连接时，实例状态为在线。
3. 活跃实例数等于健康连接对应的去重实例数。
4. 顶部概览不再混入单实例字段。
5. 前端测试和构建通过。

## 验证命令

```bash
cd web
npm test -- --run
npm run build
```

```bash
go test ./internal/server -run 'TestWeb|TestEmbedded' -count=1
```

```bash
git diff --check
```

## 回滚注意事项

- 本变更只涉及前端展示、文案、测试和文档，回滚时恢复 `AgentDetail.vue`、国际化文件、测试和文档即可。
- 生产产物回滚需同时恢复 `internal/server/web_dist/` 中对应文件。
- 不涉及数据库迁移和后端协议变更，无需回滚服务端数据。

---

## Follow-up: 实例连接数修正

### Task 4: 按健康连接统计实例连接数

**Files:**
- Modify: `web/src/views/AgentDetail.vue`
- Modify: `web/src/tests/agent-detail.spec.ts`
- Modify: `docs/user-guide/server-admin.md`

**Interfaces:**
- Consumes: `healthyConnections`、`instanceId`。
- Produces: `instanceConnectionCount(instanceId: string): number`。

- [x] **步骤 1：编写失败测试**

```ts
it('shows per-instance connection counts from healthy connections', async () => {
  // mock 两个实例：
  // instance-live 有 2 条 healthy=true 的连接；
  // instance-offline 只有 1 条 healthy=false 的连接。
  // 断言实例表连接数分别为 2 和 0，而不是后端 connectionCount 的 999。
})
```

- [x] **步骤 2：运行测试确认失败**

```bash
cd web
npm test -- --run agent-detail.spec.ts
```

预期失败：实例表连接数仍显示 `999`。

- [x] **步骤 3：实现最小修改**

在 `AgentDetail.vue` 中新增按实例聚合健康连接数的 `connectionsByInstance`，并把实例表连接数列改为调用 `instanceConnectionCount(scope.row.instanceId)`。

- [x] **步骤 4：运行测试确认通过**

```bash
cd web
npm test -- --run agent-detail.spec.ts
```

预期通过：实例表连接数显示健康连接数。
