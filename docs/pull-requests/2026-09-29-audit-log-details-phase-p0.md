# 审计日志信息量增强（Phase P0）

## Title

`feat(audit): record request facts, trace ids and actor names`

## Target branch

`main`

## 摘要

审计表的 `details` 列一直是整条链路上最薄的一环：`API.audit()` 把 10 个管理动作的详情写死成 `{}`，凭据与远程服务器的 `writeAudit` 同样写 `{}`，拿一条审计只能看到“谁在什么时候做了 agent.updated”，看不到改了什么、改成什么；管理 API 也没有 trace 上下文，审计行和结构化日志之间没有桥梁；界面上操作者是纯 ID，`details` 只能逐行点开弹窗看，而绝大多数行本来就什么都没有。

本 PR 在不改 Schema 的前提下补齐这三件事：

- `audit()` 的 `details` 变成必填参数，10 个调用点逐个交出事实；凭据与远程服务器的 `writeAudit` 同步补齐。
- 统一约定：**变更字段记录当前值 + `*Before` 旧值，未变更字段不写**；策略事件额外记录完整生效规则（哪个 Agent、目标主机/端口、协议、CIDR/端口白名单）。
- 每个 `/api/v1` 请求获得一个 W3C trace（沿用合法入站 `Traceparent`，否则现生成），响应头回显同一值，
  处理该请求写下的审计在 `details.traceId` 带上同一个 ID——含身份认证链路（登录成功/失败/限流、MFA、
  受信任设备、账号生命周期、OIDC 建号）。
- 审计行解析出操作者用户名（一次批量查询），审计页与概览共用同一投影，新增“变更摘要”列与详情弹窗的链路 ID 行。

## 用户影响

- 审计页：操作者显示用户名（原始 ID 作为次要信息保留），新增“变更摘要”列（最多 3 组 `key=value`，悬停看完整 JSON）；详情弹窗新增“变更摘要”和“链路 ID”。
- 概览“最近事件”：操作者同样显示用户名（此前显示 ID）。
- 排障：控制台报错 → 响应头 `Traceparent` → 按 `details.traceId` 反查审计（或反向），不需要额外关联表。
- 老数据不受影响：历史行 `details={}` 仍按“无额外详情”渲染。

## API、Schema 与配置影响

- **无数据库 Schema 变更、无 `SchemaVersion` 变化、无新配置项**：全部信息落在既有的 `audit_logs.details` JSON 列。
- `GET /api/v1/audit-logs` 与 `GET /api/v1/dashboard/summary` 的审计对象新增 `actorUsername`（纯增字段，旧客户端忽略即可）。
- 所有 `/api/v1` 响应新增 `Traceparent` 响应头。
- 存储层新增可选接口 `storage.UserBatchRepository`（`userRepo.GetByIDs`，按 200 个 ID 分片），不给 `UserRepository` 加方法，既有认证替身无需改动。
- `internal/observability` 新增 `NewTraceContext`、`EnsureTraceContext`、`TraceIDFromContext`、`StampTrace` 与常量 `AuditTraceKey`。
- 凭据与远程服务器 `writeAudit` 的 Go 内部签名新增 `details` 参数（非公开 API）。

## 安全与授权影响

- 权限口径未变：审计页仍是 admin-only，概览的事件集仍在存储层 SQL 内按调用者范围过滤，用户名只解析**调用者本来就能读到的那些行**，用户表没有变成新的读取面。
- `details` 明确排除 secret、密码、私钥、Token 明文、完整 Authorization 与会话字节；凭据事件只写公钥指纹与 `hasSecret` 布尔（`TestCredentialAuditRecordsFactsWithoutSecrets` 同时断言“该写的写了”和“不该写的没写”）。WebSSH 原先用字符串拼 JSON，现改走 `json.Marshal`，顺带消除拼接注入面。
- `Traceparent` 是公开格式、不含身份信息；聚合类审计（代理入口拒绝聚合、VPN 拒绝聚合）**故意不盖章**，因为一行代表窗口内多次事件，写任何一个 trace 都是错的；无请求上下文的后台任务同样不盖章。
- 未盖章的写入点只有三个，且都是有意保留：`token_service.tokenAudit` 与两个低频配置构造器（全局认证策略
  `auth.policy.updated`、SSO provider 变更）——它们已携带结构化事实，且函数签名不持有 `context`，为塞一个
  traceId 去改事务链/配置链的签名属于越界，留待与各自的功能改动一起做。

## 测试证据

红灯（实现前实际观察到）：

- `TestAgentAuditRecordsChangedFacts` → `details map[string]interface {}{} has no key "name"`。
- `TestPolicyAuditRecordsFacts` → `details map[string]interface {}{} has no key "agentId"`。
- `TestManagementAuditCarriesTraceID` → `response Traceparent = ""`。
- `TestAuditListResolvesActorUsernames` → `actor usernames = map[agent.deleted: agent.updated: proxy_auth_failed:], want alice for agent.updated`。
- `internal/storage` → `undefined: UserBatchRepository`（构建失败）。
- `dashboard-view.spec.ts > prefers the resolved actor name over the identifier` → `expected '…user-7…' to contain 'alice'`。
- `TestLoginAuditCarriesTraceID` → `details map[string]interface {}{"deviceTrusted":false, "ip":"192.0.2.1", "method":"password"} has no key "traceId"`。

说明：`internal/observability` 的新 API、`audit-logs-view.spec.ts` 与 `audit-utils.spec.ts` 是同一轮里先写实现再跑用例，首次即绿，没有取到红灯，属于计划里 TDD 顺序的执行偏差，此处如实记录。

绿灯：见“集成状态”。

## 发布步骤

1. 合并后发布 Server（控制台产物随二进制 embed，前端无需单独部署）。
2. 在管理台改一次代理节点名称、新增/收紧一条策略：到审计日志确认“变更摘要”出现 `name=… · nameBefore=…`、`targetPort=… · targetPortBefore=…`；刷新页面时从响应头拿 `Traceparent`，在审计详情里应看到同一个链路 ID。

## 回滚步骤

- `git revert` 本次合并即可：无 Schema、无配置、无数据迁移，审计历史不受影响。
- 服务端单独回退时，`actorUsername` 与 `details` 新字段消失，新版前端回落显示 `actorUserId` 与“无额外详情”，不报错；前端单独回退也不影响服务端。

## Reviewer 关注点

- **`*Before` 约定**：更新事件只为真正改动的字段附带旧值，未改动字段不出现。若更希望“每条更新都快照完整资源”，需要改成写入当前全量值（策略已是这种口径，代理节点不是），这是有意的差异：策略的规则整体才有意义，代理节点的改名与启停是独立事实。
- **`details` 与 Schema 的取舍**：P1（`source_ip`/`user_agent`/`request_id` 提升为列）需要 `v0017` 增量脚本 + expand→contract + 双驱动契约测试，而且审计默认永久保留（`server.audit.retention_days=0`）意味着留存 IP/UA 是个人信息合规决策，需显式批准，故未包含。
- **一次批量查询换 N 次单行查询**：`auditActorNames` 在解析失败或用户不存在时降级为空标签，绝不把历史列表变成 500。本 PR 推翻 `docs/pull-requests/2026-09-29-dashboard-recent-event-attributes.md` “Reviewer 关注点”里“概览不做用户名反查”那一条：当时反对的是“概览另造一套口径”，现在字段落在唯一投影 `publicAudit` 上，前提不成立。
- `Traceparent` 回显只覆盖 `/api/v1`；代理入口与 Agent 控制连接各自有自己的 trace 传播路径，未改动。

## 集成状态

- `go test ./internal/... ./cmd/... ./scripts -count=1 -timeout 25m` → 退出码 0。
- `go test -race ./internal/server ./internal/auth ./internal/storage ./internal/observability -count=1 -timeout 25m` → 退出码 0（`internal/server` 约 610s，`internal/auth` 108.0s，`internal/storage` 16s，`internal/observability` 1.6s），无 `DATA RACE`。
- `go vet ./...` 无输出；`gofmt -l internal cmd` 为空；`git diff --check` 干净。
- `cd web && npm test -- --run` → 44 文件 / 363 用例通过；`npm run build` + `bash scripts/verify-web-embed.sh` → 产物与 embed 一致。
- `ruby -ryaml -e 'YAML.load_file("docs/api/openapi.yaml")'` 解析通过（`AuditLog` 新增 `actorUsername`，`details` 描述补上 `*Before` 与 `traceId` 约定）。
- 门禁过程中出现过一次 `TestEmbeddedWebDistContainsEverySourceFile` 失败：原因是我在 race 运行期间又跑了一次 `npm run build`，测试二进制里嵌入的是上一版产物。停掉并发构建后重跑全量 + race 均绿，与本次改动无关。
- GitHub Actions：`build-test`、`mysql56`、`packaging` 三项 checks 通过。
- 与同分支的另一项变更（还原发行管理页各平台下载地址，`docs/pull-requests/2026-09-29-downloads-platform-assets-restore.md`）一起提交、一起过门禁。
