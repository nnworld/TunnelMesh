# 概览“最近事件”补全审计属性

## Title

`feat(dashboard): show actor, resource and details in recent events`

## Target branch

`main`

## 摘要

概览的“最近事件”每行只有动作、资源类型和时间，两条同名动作无法区分。审计数据本身是完整的（`audit_logs` 含操作者、资源 ID、详情），只是 `handleDashboard` 手写了一个字段更少的投影。改为复用审计页使用的 `publicAudit` 投影，并把操作者、资源 ID、详情摘要与审计页入口放到界面上。

## 用户影响

- 每行变成两行信息：动作 + `资源类型 · 资源 ID` + 时间；操作者 + 详情摘要（前 3 组 `key=value`，悬停看完整 JSON）。
- 系统事件（无操作者）显示“系统事件”，不再留空白。
- 卡片右上角新增“查看审计日志”，直接跳到可筛选、可看逐条详情的完整列表。
- 非管理员仍只看到自己造成的事件；管理员看到全部。可见性规则没有变化。

## API、Schema 与配置影响

- `GET /api/v1/dashboard/summary` 的 `data.recentEvents[]` 新增 `actorUserId` 与 `details` 两个字段（纯增字段，旧客户端忽略即可）；条数上限仍是 10。
- OpenAPI 此前只写了 `200: Dashboard summary`，没有响应体 schema；本次补 `DashboardSummaryEnvelope`，其 `recentEvents.items` 复用既有 `AuditLog`，避免两处各自描述同一实体。
- 无数据库 Schema 变更、无 `SchemaVersion` 变化、无配置项变化。

## 安全与授权影响

- 权限过滤仍发生在存储层 SQL（`WHERE actor_user_id=?`），不是取回后再裁剪。
- `details` 是审计页对同一读者群已经暴露的字段，写入侧已排除 secret、密码、私钥与完整凭据；本次不新增泄露面，日志与响应都不含会话字节。

## 测试证据

红灯（实现前）：

- `TestDashboardSummaryToleratesActorlessAuditEvents`（扩展后）→ `actorless event lost its details`；新增 `TestDashboardSummaryCarriesAuditAttributes` → `event attributes = {… ActorUserID: … Details:[]}, want actor "user-…"`。
- `npm test -- --run src/tests/dashboard-view.spec.ts` → 3 条全失败（无操作者/资源 ID/详情文本，无 `systemActor` 标签，无跳转按钮）。

绿灯：

- `go test ./internal/server -run TestDashboardSummary -count=1` → ok。
- `npm test -- --run` → 42 文件 / 355 用例通过（含 `layout-overflow.spec.ts` 对新 grid 规则的静态检查）。
- 全量 Go 门禁与 race 结果见“集成状态”。

## 发布步骤

1. 合并后发布 Server（控制台产物随二进制 embed）。
2. 打开概览：确认同名动作的两条事件现在能靠操作者/资源 ID/详情区分；用无操作者的系统事件（例如代理入口鉴权失败）确认显示“系统事件”。

## 回滚步骤

- `git revert` 本次合并：无数据影响。
- 服务端回退后 `recentEvents` 少两个字段，新版前端按“空操作者 + 无详情”渲染，不报错，因此可单向回退，无需前端同步降级。

## Reviewer 关注点

- 详情摘要在视图层拼装（3 组上限、悬停看全文）。若希望摘要口径唯一，可下沉为组件或服务端字段，但那会让概览与审计页重新分叉，本次刻意选择共用投影。
- 概览不做用户名反查，展示的就是 `actorUserId`；若要“人名”，应在审计页与概览一起改，属另一项变更。
- `details` 为 `json.RawMessage`：非法 JSON 会经 `defaultJSON` 兜底成对象，前端拿到的永远是对象或 null。

## 集成状态

- 实施计划：`docs/superpowers/plans/2026-09-29-dashboard-recent-event-attributes.md`。
- `go test ./... -count=1 -timeout 30m` → 退出码 0（与同分支的发行管理页还原改动一起跑）。
- `go test -race ./internal/server -count=1 -timeout 25m` → ok，590.9s，无 `DATA RACE`，退出码 0。
- `go vet ./...` 无输出；`gofmt -l internal cmd` 为空；`git diff --check` 干净。
- `docs/api/openapi.yaml` 经 YAML 解析校验通过，新增的 `DashboardSummaryEnvelope` 指向既有 `AuditLog`。
