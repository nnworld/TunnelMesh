# 审计日志信息量增强（Phase P0）实施计划

## 1. 目标

- 审计日志的**写入侧**太薄：`API.audit()` 把 `details` 写死成 `{}`，凭据与远程服务器的 `writeAudit` 同样只写 `{}`，导致代理节点改名、策略增删、traceroute 触发这类关键动作在审计里只剩一个动作名，事后无法复盘“改了什么、改成了什么”。
- 审计行**无法与日志关联**：管理 API 请求链路没有 trace 上下文，`internal/observability` 只在代理入口解析 `Traceparent`。排障时拿到一条审计，无法跳到同一请求的结构化日志。
- 审计列表**可读性差**：操作者列只有不透明的用户 ID，详情要逐行点开弹窗才能看到 `details`，而绝大多数行根本没有 `details`。

## 2. 非目标

- 不改 `audit_logs` 表结构，不新增 `source_ip`/`user_agent`/`request_id` 列（那属于 P1，涉及 Schema 版本与个人信息留存合规口径，需单独确认）。
- 不做 CSV/JSONL 审计导出与资源深链（P2）。
- 不引入动作中文标签字典：动作名是稳定协议标识，翻译会形成第二套口径。
- 不改动 `token_service.tokenAudit`（它已携带结构化事实，且构造签名没有 `context`，为塞 traceId 改一条事务链的函数签名属于越界）。

## 3. 架构决策

- **事实写进 `details`，而不是加列**：`details` 是审计表里唯一的结构化自由槽位，P0 用它换取零 Schema 变更、零迁移风险，滚动升级期间新旧 Server 读写完全兼容。
- **签名强制而非可选**：`audit(ctx, p, action, type, id, details)` 新增必填参数，编译器迫使 10 个调用点逐个表态（有事实就写，确实没有就传 `nil`），杜绝新代码继续默默写 `{}`。
- **trace 关联放在写审计的最后一环**：`observability.StampTrace` 在 details 编码前注入 `traceId`；只有上下文里确有 trace 时才注入，因此 `context.Background()` 的单测与后台任务行为逐字节不变。
- **单一投影覆盖两个界面**：操作者用户名由服务端解析后放进 `publicAudit` 输出，概览与审计页共用同一投影，不会各造一套“操作者”口径。本条**推翻**上一份计划中“不引入用户名反查”的非目标：当时反对的是“概览另造一个用户名”，本次是在唯一投影里加字段，分歧前提不成立。
- **权限口径不变**：用户名解析只作用于调用者**已经能读到**的审计行（审计页仍是 admin-only，概览仍在 SQL 内按 `actor_user_id` 过滤），不把用户表变成新的读取面。
- **批量解析用可选接口**：新增 `storage.UserBatchRepository`（可选实现，仿 `AtomicIdempotencyRepository`），避免为审计列表做 N 次单行查询；不给 `UserRepository` 加方法，从而不打破既有测试替身。

## 4. 技术栈与规格引用

- Go `net/http`、`crypto/rand`（W3C trace/span id）、`encoding/json`；SQLite/MySQL 共用 `userRepo`（`IN (?,?,…)` 占位符，MySQL 5.6 兼容）。
- Vue 3 `<script setup>` + Element Plus（`el-table-column`/`el-descriptions`）。
- 接口：`GET /api/v1/audit-logs`、`GET /api/v1/dashboard/summary`（`docs/api/openapi.yaml`），管理 API 新增回显 `Traceparent` 响应头。

## 5. 全局约束

- `details` 禁止出现 secret、密码、私钥、Token 明文、完整 Authorization、会话字节；凭据只记 `fingerprint` 与 `hasSecret` 布尔。
- 变更前后统一约定：值字段表示“当前值”，`xxxBefore` 仅在**确实发生变化**时记录旧值；未变字段不写，避免噪声。
- 空 `details` 仍是合法 JSON 对象 `{}`，前端对空对象显示“无额外详情”。
- 用户名解析失败（用户已删、查询报错）必须降级为空字符串，绝不影响审计列表可用性。

## 6. 文件清单

- `internal/observability/trace.go`：`NewTraceContext`、`EnsureTraceContext`、`TraceIDFromContext`、`StampTrace`。
- `internal/observability/trace_test.go`：随机 id 合法、提取优先于生成、`StampTrace` 不覆盖已有 `traceId`、无 trace 时不改写。
- `internal/storage/repository.go`：`UserBatchRepository` 接口 + `userRepo.GetByIDs`。
- `internal/storage/account_repository_test.go`：`GetByIDs` 命中/去重/缺失 id 行为。
- `internal/server/api.go`：`ServeHTTP` 安装 trace 上下文并回显 `Traceparent`；`audit` 新签名与 10 个调用点事实；`auditRoute` 走 `StampTrace`；`publicAudit(v, actorName)`；`handleAudits` 解析操作者名。
- `internal/server/audit_view.go`（新增）：`auditActorNames` —— 去重、批量、失败降级。
- `internal/server/credential_service.go`、`internal/server/remote_server_service.go`：`writeAudit` 携带结构化事实与 trace。
- `internal/server/client_api.go`、`internal/server/agent_connection_audit.go`、`internal/server/server_node_service.go`、`internal/server/vpn_peer_service.go`、`internal/server/webssh_session_service.go`、`internal/server/proxy_entry.go`、`internal/server/vpn_denials.go`：已有结构化 `details` 的写入点统一经 `StampTrace`；其中 webssh 两处由字符串拼接改为 map + `json.Marshal`，消除手工拼 JSON。
- `internal/server/account_api.go`：概览同样解析操作者名。
- `internal/auth/login_service.go`、`internal/auth/identity_service.go`、`internal/auth/account_service.go`：登录/MFA/限流、OIDC 即时建号与账号生命周期审计同样盖上 `traceId`（执行中扩大的范围：这三处 ctx 与 details 都在手边，而登录类事件恰恰最需要与日志对齐）。
- `internal/server/api_test.go`、`internal/server/account_api_test.go`：新增/扩展断言。
- `docs/api/openapi.yaml`：`AuditLog.actorUsername`、`details` 已知键位说明、`Traceparent` 响应头。
- `web/src/api/client.ts`：`AuditLog.actorUsername`。
- `web/src/utils/audit.ts`（新增）：`auditDetailsEntries`、`auditDetailsSummary`、`auditActorLabel`（概览与审计页共用）。
- `web/src/views/Dashboard.vue`：改用共用 helper，删除本地重复实现。
- `web/src/views/AuditLogs.vue`：操作者列显示用户名（ID 作次要信息）、新增“详情摘要”列、弹窗补 traceId 行。
- `web/src/i18n/messages/{zh-CN,en-US}.ts`：`audits.summary`、`audits.actorSystem`、`audits.traceId`、`audits.noTrace`。
- `web/src/tests/audit-utils.spec.ts`（新增）、`web/src/tests/audit-logs.spec.ts`（扩展挂载用例）、`web/src/tests/dashboard-view.spec.ts`（沿用）。
- `docs/user-guide/server-admin.md`、`docs/operations/observability.md`。

## 7. 任务间接口

- `observability.StampTrace(ctx, details map[string]any) map[string]any`：无 trace 时原样返回（含 `nil`）；有 trace 且 `details` 为 `nil` 时新建 map；已有 `traceId` 键不覆盖。
- `publicAudit(v storage.AuditLog, actorName string)` 输出新增 `actorUsername`，无解析结果时为 `""`。
- `storage.UserBatchRepository.GetByIDs(ctx, ids []string) (map[string]User, error)`：入参去重、忽略空 id、结果只含存在的用户；空入参返回空 map 且不发 SQL。
- 前端 `auditDetailsSummary(details, limit)`：`key=value` 以 ` · ` 连接，超出 `limit` 以 ` …` 结尾；`auditActorLabel(audit, fallback)` 优先用户名，其次 `actorUserId`，最后 fallback（系统事件）。

## 8. TDD 步骤与预期

1. 先写 `internal/observability/trace_test.go` 断言新 API → 红灯（未定义）。
2. 写 Go 服务端用例并确认红灯：
   - `TestAgentAuditRecordsChangedFacts`：改名 + 停用后 `agent.updated` 的 `details` 含 `name`/`nameBefore`/`enabled`/`enabledBefore`；只改启用态时不得出现 `nameBefore`。
   - `TestPolicyAuditRecordsFacts`：`policy.created`/`policy.updated`/`policy.deleted` 携带 `agentId`、`targetHost`、`targetPort`、`protocol`。
   - `TestManagementAuditCarriesTraceID`：带合法 `Traceparent` 的请求产生的审计 `details.traceId` 等于该 trace id，且响应回显 `Traceparent`；不带该头时仍生成新 id。
   - `TestAuditListResolvesActorUsernames`：列表返回 `actorUsername`，未知操作者返回空串且不 500。
   - `TestCredentialAuditRecordsFactsWithoutSecrets`：凭据写入后的审计 `details` 不含私钥/口令字段。
   - `TestLoginAuditCarriesTraceID`：经 `/api/v1/auth/login` 写入的 `auth.login.success` 带同一个 trace id。
3. 实现最小改动后 `go test ./internal/observability ./internal/storage ./internal/server -count=1` 通过。
4. Web 先写 `audit-utils.spec.ts` 与审计页挂载用例（断言用户名、摘要列、traceId 行）→ 红灯；改 helper/视图/语言包后转绿。
5. 全量门禁。

## 9. 验证命令

```bash
go test ./... -count=1 -timeout 30m
go test -race ./internal/server ./internal/observability ./internal/storage -count=1 -timeout 25m
go vet ./...
gofmt -l internal cmd
cd web && npm test -- --run && npm run build
cd .. && bash scripts/verify-web-embed.sh
git diff --check
```

## 10. 回滚

- 无 Schema、无配置变更，`git revert` 即可。
- 前端读到旧服务端缺少 `actorUsername` 时回落到 `actorUserId`，读到空 `details` 时显示“无额外详情”，因此服务端可单独回退。
- 审计历史数据不受影响：老行 `details={}` 继续按“无额外详情”渲染。
