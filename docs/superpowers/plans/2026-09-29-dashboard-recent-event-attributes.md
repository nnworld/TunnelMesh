# 概览“最近事件”补全审计属性实施计划

## 1. 目标

- 概览的“最近事件”只显示 `action`、`resourceType`、时间三条属性，两条同名动作（例如两次 `agent.updated`）无法区分。补出操作者、资源 ID 与详情摘要，并提供到审计日志页的入口。

## 2. 非目标

- 不改审计表结构、不新增列（`audit_logs` 现有 `id/actor_user_id/action/resource_type/resource_id/details/created_at`，可用属性已全部落库，缺的只是展示与投影）。
- 不在概览做筛选、分页、逐条详情弹层——这些属于审计日志页。
- 不引入用户名反查：审计页展示的就是 `actorUserId`，概览另造一个“用户名”会形成两个口径。

## 3. 架构决策

- **单一投影**：`handleDashboard` 之前手写了一个只含 5 个字段的 map，与 `publicAudit` 并行维护同一实体。改为直接复用 `publicAudit`，概览与审计列表从此不可能漂移（DRY + 单一数据源）。
- **权限口径不变**：非管理员的 `recentEvents` 仍由存储层按 `actor_user_id=?` 在 SQL 内过滤，本次没有放宽任何可见性；`details` 本就是审计页对同一批读者暴露的字段，不新增泄露面（写入侧已按规范排除 secret/密码/Token）。
- **详情做摘要而非原文**：概览每行只有一行宽，`key=value` 取前 3 组 + `title` 悬停看完整 JSON，避免整段 JSON 把卡片撑破。
- **无操作者要显式命名**：系统事件（代理入口鉴权等）的 `actor_user_id` 为 NULL，界面标为“系统事件”而不是空白列。

## 4. 技术栈与规格引用

- Go `net/http` + 既有 `publicAudit`；Vue 3 `<script setup>` + Element Plus（`el-button`/`el-empty`）；`vue-router` 的 `useRouter().push`。
- 接口：`GET /api/v1/dashboard/summary`（`docs/api/openapi.yaml`），本次补齐其响应 schema `DashboardSummaryEnvelope`，`recentEvents.items` 复用 `AuditLog`。

## 5. 全局约束

- 响应仍是 `{code,msg,data}`；列表上限仍是 10 条，不引入分页。
- 不输出 secret、密码、私钥、完整 Authorization 或会话字节；`details` 沿用写入侧约束。
- 布局必须受约束：`web/src/tests/layout-overflow.spec.ts` 会扫描无列定义的 `display:grid`。

## 6. 文件清单

- `internal/server/account_api.go`：`recentEvents` 改用 `publicAudit(event)`。
- `internal/server/account_api_test.go`：扩展 `TestDashboardSummaryToleratesActorlessAuditEvents`（NULL 操作者必须输出空串字段而不是丢字段）；新增 `TestDashboardSummaryCarriesAuditAttributes`。
- `web/src/api/client.ts`：`DashboardSummary.recentEvents` 复用 `AuditLog[]`。
- `web/src/views/Dashboard.vue`：两行式事件行（动作 + 资源类型·资源 ID + 时间 / 操作者 + 详情摘要）、卡片头“查看审计日志”、`detailsSummary`/`detailsTitle`、样式。
- `web/src/tests/dashboard-view.spec.ts`（新增）：3 条挂载用例。
- `web/src/i18n/messages/{zh-CN,en-US}.ts`：新增 `dashboard.systemActor`、`dashboard.viewAudits`。
- `docs/api/openapi.yaml`、`docs/user-guide/server-admin.md`。

## 7. 任务间接口

- `publicAudit` 输出 `id/actorUserId/action/resourceType/resourceId/details/createdAt`，与前端 `AuditLog` 类型逐字段一致。
- `details` 在前端为 `Record<string, unknown> | null`；摘要函数对非对象值 `String(value)`、对对象值 `JSON.stringify`。

## 8. TDD 步骤与结果

1. Go 先写两条断言 → 红灯：`actorless event lost its details`、`event attributes = {Action:token.rotated ActorUserID: ResourceType:service_token ResourceID:token-9 Details:[]}, want actor "user-…"`（当时响应里没有 `actorUserId`/`details`）。
2. 服务端改用 `publicAudit` → `go test ./internal/server -run TestDashboardSummary` ok。
3. Web 先写 3 条挂载用例 → 全部失败（无操作者/资源 ID/详情文本，无 `systemActor`，无跳转按钮）。
4. 改视图与双语言包 → 3 passed；全量 `npm test -- --run` → 42 文件 / 355 用例通过。

## 9. 验证命令

```bash
go test ./internal/server -run TestDashboardSummary -count=1
go test ./... -count=1 -timeout 30m
go test -race ./internal/server -count=1 -timeout 20m
go vet ./...
cd web && npm test -- --run && npm run build
cd .. && bash scripts/verify-web-embed.sh
git diff --check
```

## 10. 回滚

- 无 Schema、无配置变更，`git revert` 即可。
- 服务端回滚会让 `recentEvents` 少掉 `actorUserId`/`details` 两个字段；前端旧版不读它们，新版读到缺失字段时按空操作者与无详情渲染，不报错，因此可单向回退。
