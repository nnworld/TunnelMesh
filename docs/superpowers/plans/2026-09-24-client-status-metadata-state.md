# 客户端观测状态模型修复实施计划

状态：已确认并完成实施，实现记录见 `docs/pull-requests/2026-09-24-client-status-metadata-state.md`。第 7、8 节的接口命名已按最终实现同步。

日期：2026-09-24

## 1. 目标

让「客户端运行观测」列表的状态与服务端真实事实一致：在线客户端不再被标成「metadata 未上报」或「metadata 已过期」，断开的客户端不再留下永久「已过期」幽灵行，表格上方的统计量与表格同源。

## 2. 非目标

- 不新增协议 frame，不修改 metadata TTL（5 分钟）、心跳间隔（30 秒）或窗口/流控参数。
- 不做 Schema 变更：本次不新增列、不提升 `SchemaVersion`、不写增量脚本。
- 不改动 Agent 侧观测逻辑（`agent_instance_metadata` 已在 #21 修复）。
- 不改动授权、Token scope、路由与代理数据面。

## 3. 根因与线上证据

证据来源：生产库 `tunnelmesh`（`client_instance_metadata` 27 行）与 `GET /api/v1/clients` 实际响应。

### 3.1 在线客户端被判为「metadata 未上报」

- `internal/client/metadata.go` 的 `ClientMetadataOptions` 没有任何 `Capabilities` 生产者，Client 也从不发送 `capabilities`，因此 `protocol.ClientMetadataPayload.Capabilities` 恒为 nil。
- `persistMetadata`（`internal/server/client_observability.go:181` 起）执行 `json.Marshal(payload.Capabilities)`，nil slice 序列化成 4 字节字符串 `"null"`。
- `clientInstanceRepo.Upsert` 只对 `""` 归一化为 `"[]"`（`internal/storage/client_repository.go:35`），漏掉 `"null"`，于是库里存的是字符串 `"null"`。
- `newClientView`（`internal/server/client_api.go:272`）用 `decodeStrings(instance.Capabilities) == nil` 表示「未上报 metadata」：`json.Unmarshal("null", &v)` 成功且 `v` 为 nil，命中该分支。
- 结果：5 个带完整 hostname/version/listeners 的在线客户端全部显示「metadata 未上报」。线上采样：`client-1b0dee98…`（devbook-pro，v1.2.6-4-g6241fe3，4 连接 4 流）`capabilities` 字段为 `"null"`，API 返回 `"capabilities":null`、`"status":"metadata_unavailable"`；而 legacy 行（`capabilities="[]"` → `[]string{}` 非 nil）反而返回 `online`。

### 3.2 永久「metadata 已过期」幽灵行

- `RegisterLegacy`（`internal/server/client_observability.go:105`）用 `"legacy-" + principal.ConnectionID` 作为 instance_id，即**每条物理 WebSocket 一行**。
- Client 连接池/重连每次产生新行；旧行的 `expires_at` 在最后一次心跳 5 分钟后被 `MarkExpired`（`internal/storage/client_repository.go:152`）置 `stale=1`。
- `ClientInstanceRepository` 没有任何删除方法（`internal/storage/repository.go:230`），`ClientMetadataSweeper` 只标记不回收，因此幽灵行永不消失。
- 线上现状：27 行中 21 行 `stale=1`，且 21 行全部 `instance_id LIKE 'legacy-%'`，最早一条 `last_seen_at=2026-09-23T10:28:42Z`（约 23 小时前）。同一个物理客户端因此表现为「1 条在线 + N 条已过期」。

### 3.3 状态枚举把「presence」和「metadata 新鲜度」混在一起

- `newClientView` 优先级是 `stale` > `metadata_unavailable` > `online`，`offline` 只是兜底：一条连接已断开的行会显示「metadata 已过期」，而不是「离线」；语义上「已过期」描述的是 metadata 时效，不是节点存在性。
- 同一缺陷使统计量互斥失真：当前 6 个在线客户端里有 5 个同时被计入「metadata 未上报」。

### 3.4 统计量与表格不同源

- `web/src/views/Clients.vue:203` 的 5 张卡片全部基于 `items.value`（当前游标页、当前筛选），不是全量聚合；分页或筛选后与表格、与实际总数不一致。这正是上一轮反馈的「表格上方这些量也需要同步」。

## 4. 架构决策

- **D1 拆分正交事实（单一职责）**：`ClientView.status` 只表达 presence——有未过期 lease 即 `online`，否则 `offline`。metadata 时效独立为 `ClientView.metadataState`，取值 `reported` / `unavailable` / `expired`。二者各自只有一个权威来源：presence 来自 `client_connection_leases.expires_at`，时效来自 `client_instance_metadata.{metadata,stale}`。
- **D2 兼容性**：`status` 与 `metadataState` 都是新增语义，不删字段。`GET /api/v1/clients?status=stale|metadata_unavailable` 保留为 `metadataState=expired|unavailable` 的查询别名，OpenAPI 与代码注释标记 Deprecated，至少保留一个 MINOR 版本后再删。
- **D3 统计量服务端聚合**：新增 `ClientInstanceRepository.Summarize`，在**同一 filter** 下用一条 SQL 返回 `total/online/activeConnections/activeStreams/metadataUnavailable/metadataStale`。前端只渲染服务端数字，不再 `items.filter()`，保持单一数据源。
- **D4 判别「是否上报过 metadata」用持久事实**：`persistMetadata` 写入的 payload JSON 必含非空 `instance_id`（`ClientMetadataPayload.InstanceID` 无 `omitempty`，且 `validateClientMetadataPayload` 强制非空），而 `RegisterLegacy` 写入字面量 `{}`。因此以 `metadata='{}'` 判定「未上报」，客户端无法伪造，也不需要新增列。Go 侧对应 `payload.InstanceID == ""`。
- **D5 legacy 行随连接生命周期回收**：`Release` 调用 `DeleteUnreported` 删除该连接持有的行；`ClientMetadataSweeper` 每轮在 `MarkExpired` 之后调用 `PurgeUnreported`，清理崩溃残留与现存的 21 条幽灵。两者的删除条件都是 `metadata='{}'`，不依赖 `instance_id LIKE`：LIKE 的 `_` 是通配符，语义不稳定，而 `{}` 只有 `RegisterLegacy` 能写出。
- **D7 判定条件单点化**：Go 侧 `clientMetadataReported` 与 SQL 侧 `metadata='{}'` 必须表达同一事实，否则行标签与统计卡片会互相矛盾。
- **D6 在存储边界归一化 capabilities**：`Upsert` 把 `""` 与 `"null"` 一并归一化为 `"[]"`。放在 Repository 而非 Handler，使所有写入方（含未来的 capabilities 生产者）都不会再泄漏 JSON `null` 到 API。

## 5. 技术栈与规格引用

- Go 1.26 标准库 `database/sql` + `github.com/go-sql-driver/mysql` + `modernc.org/sqlite`；聚合 SQL 必须同时通过两种驱动（`migrations/ddl.sql:288` 起，时间列是 TEXT 存 RFC3339Nano，比较为字典序，因此只使用 `CASE WHEN EXISTS` 与既有时间串比较，不引入数据库端时间函数）。
- MySQL 下限是 5.6（`docs/operations/connection-pool.md` 与 `docs/operations/schema-upgrades.md` 已按 5.6 的 767-byte 前缀限制设计）：不使用 CTE、窗口函数、JSON 函数与任何 8.0 专属语法；生产实例 `VERSION()=5.6.51-91.0-log`，聚合 SELECT 与两条 DELETE 的兼容性按计划第 11 节只读核验。
- Vue 3 + TypeScript + Element Plus；`web/src/api/client.ts`、`web/src/views/Clients.vue`。
- 规范依据：`AGENTS.md`「分层」「API：统一响应/cursor 分页」「一致性」「TDD」「协议演进约束」；`docs/protocol/proxy-modules.md` 不受影响；文档索引由 `scripts/gen_doc_index.py` 生成。

## 6. 全局约束

- Handler 只做协议与授权，presence/时效判定下沉到 `newClientView` 与 Repository；禁止在 Handler 直接写 SQL。
- 普通用户仅能看到自己的客户端；`Summarize` 必须复用与 `List` 完全相同的 `clientInstanceConditions`，避免统计越权。
- 日志与响应不得包含 secret、Token 明文、完整 Authorization。
- 不修改 `migrations/ddl.sql`、不提升 `SchemaVersion`。

## 7. 文件清单

服务端：

- `internal/storage/repository.go` — `ClientInstanceRepository` 增加 `DeleteUnreported`、`PurgeUnreported`、`Summarize`；新增 `ClientInstanceSummary` 结构。
- `internal/storage/client_repository.go` — `Upsert` 归一化 `null`/`""` capabilities；实现三个新方法；`clientInstanceConditions` 的 `Status` 收窄为 presence，新增 `MetadataState` 条件。
- `internal/server/client_api.go` — `ClientView` 增加 `MetadataState`；`newClientView` 按 D1/D4 重写状态推导；`listClients` 输出 `summary` 并解析 `metadataState` 过滤与 Deprecated 别名。
- `internal/server/client_observability.go` — `Release` 删除本次连接持有的 legacy 行；暴露 `reported(instance)` 判定所需的最小 helper。
- `internal/server/client_metadata_sweeper.go` — 每轮在 `MarkExpired` 后调用 `PurgeUnreported`。
- `internal/server/api.go` — `decodeStrings` 保持不变；`newClientView` 局部把 nil capabilities 归一成 `[]`，避免影响其它列的既有解码语义。

前端：

- `web/src/api/client.ts` — `ClientStatus = 'online' | 'offline'`、新增 `ClientMetadataState`、`ClientListParams.metadataState`、`ClientListSummary` 与 `ClientListPage.summary`。
- `web/src/views/Clients.vue` — 状态列拆成 presence 标签 + metadata 标签；筛选器增加 metadata 维度；5 张卡片改读 `summary`。
- `web/src/i18n/messages/zh-CN.ts`、`web/src/i18n/messages/en-US.ts` — 新增 `clients.metadataStateLabel.*` 与筛选文案，`statusLabel.offline` 保持「离线」。

文档：

- `docs/api/openapi.yaml` — `/api/v1/clients` 增加 `metadataState` 查询参数与 `summary` 响应；`status` 的 `stale|metadata_unavailable` 标注 deprecated。
- `docs/README.md` — 索引再生成（`scripts/gen_doc_index.py`）。
- `docs/operations/upgrade-and-rollback.md`（若存在同名文件则追加，否则在 `docs/operations/` 现有升级文档中补一节）— 说明本次无 Schema 变更、可直接回滚应用。
- `docs/help/`（客户端观测用户帮助）— 更新状态含义：在线/离线与 metadata 三种时效各自独立。

测试：

- `internal/storage/client_repository_test.go`
- `internal/server/client_api_test.go`
- `internal/server/client_observability_test.go`
- `internal/server/client_metadata_sweeper_test.go`
- `web/src/tests/clients-api.spec.ts`
- `web/src/tests/views.spec.ts`

PR 记录：`docs/pull-requests/2026-09-24-client-status-metadata-state.md`

## 8. 任务与接口

任务顺序执行，T1 是后续所有断言的基础。

### T1 Repository：capabilities 归一化 + presence/时效聚合

接口：

```go
type ClientInstanceSummary struct {
	Total               int64
	Online              int64
	ActiveConnections   int64
	ActiveStreams       int64
	MetadataUnavailable int64
	MetadataStale       int64
}

func (ClientInstanceSummary) IsZero() bool

type ClientInstanceRepository interface {
	// ...
	DeleteUnreported(context.Context, string) error
	PurgeUnreported(context.Context, time.Time) (int64, error)
	Summarize(context.Context, ClientInstanceFilter, time.Time) (ClientInstanceSummary, error)
}
```

`ClientInstanceFilter` 增加 `MetadataState string`（`reported|unavailable|expired`）。`Summarize` 与 `List` 共用 `clientInstanceConditions`。

TDD：

1. 红：`TestClientInstanceUpsertNormalizesNullCapabilities` 断言写入 `capabilities:"null"` 后读回 `[]"`；当前实现返回 `"null"` 而失败。
2. 红：`TestClientInstanceSummaryMatchesFilter`（MySQL 与 SQLite 两个子测试）造 3 行：reported+live lease+2 流、legacy 活、legacy 死(`stale=1`)，断言 `Total=3,Online=3,ActiveConnections=…,ActiveStreams=…,MetadataUnavailable=2,MetadataStale=…`；`metadataState=reported` filter 下 `Total=1`。
3. 红：`TestClientInstancePurgeUnreportedOrphans` 与 `TestClientInstanceDeleteUnreportedKeepsReportedRows` 断言只删无未过期 lease 且已过 TTL 的 `metadata='{}'` 行。
4. 最小实现：`Upsert` 归一化条件改为 `capabilities == "" || capabilities == "null"`；`PurgeUnreported` 用 `DELETE ... WHERE metadata='{}' AND (expires_at IS NULL OR expires_at<=?) AND NOT EXISTS(SELECT 1 FROM client_connection_leases ... expires_at>?)`；`Summarize` 用一条实例级 `SELECT COUNT(*), SUM(CASE WHEN EXISTS(...))` 加一条同 WHERE 的租约级聚合，避免重复拼接三份 filter 参数。
5. 绿：两条命令见第 11 节。

### T2 Server：presence/时效状态模型

接口：`ClientView.MetadataState string \`json:"metadataState"\``，取值 `fresh|expired|unavailable`；`status` 语义收窄为 `online|offline`。filter 侧 `reported` 是 `fresh|expired` 的别名。

TDD：

1. 红：`TestNewClientViewSplitsPresenceFromMetadataState` 表驱动 5 例——(a) live lease + 已上报 + `stale=0` → `online`/`fresh`；(b) live lease + 已上报 + `stale=1` → `online`/`expired`；(c) live lease + `metadata='{}'` → `online`/`unavailable`；(d) 无 lease + `stale=1` + `metadata='{}'` → `offline`/`unavailable`（原实现返回 `stale`，即用户看到的「已过期」）；(e) 过期 lease + 已上报 → `offline`/`fresh`。原实现在 (a) 即失败。
2. 红：`TestListClientsReturnsSummaryAndMetadataStateFilter` 断言响应 `data.summary.online == 全量在线数` 且 `?metadataState=expired` 生效；`?status=stale` 仍可用（别名）。
3. 最小实现：按 D1/D4 重写 `newClientView` 推导；`listClients` 解析 `metadataState`、Deprecated 别名映射到 `MetadataState`、返回 `summary`。
4. 绿。

### T3 Server：legacy 行回收

TDD：

1. 红：`TestClientObservabilityReleaseDropsUnreportedRow` 断言 legacy 连接 `Release` 调用 `DeleteUnreported`；`TestClientObservabilityKeepsReportedRowOnRelease` 断言仓库返回 `sql.ErrNoRows`（行已上报）时 `Release` 仍然成功。
2. 红：`TestClientMetadataSweeperPurgesUnreportedOrphans` 断言每轮 `Sweep` 在 `MarkExpired` 之后调用 `PurgeUnreported`。回收量已由 `PurgeUnreported` 的返回值可观测，本次不新增指标名，避免与既有 `client_instance_metadata` 陈旧度指标重复。
3. 最小实现：`Release` 无条件尝试 `DeleteUnreported(record.ClientInstanceID)`，把「是否可删」的判断留在仓库的 `metadata='{}'` 条件里，避免内存态与真实行不一致；sweeper 在 `MarkExpired` 之后调用 `PurgeUnreported` 并返回其影响行数。
4. 绿。

### T4 Web：状态列与同源统计

TDD：

1. 红：`web/src/tests/clients-api.spec.ts` 断言 `listClients` 透传 `metadataState` 并解析 `summary`。
2. 红：`web/src/tests/views.spec.ts` 对 Clients 视图断言渲染「在线」+「metadata 未上报」两个标签，且卡片数字取自 `summary` 而非当页 items。
3. 最小实现：`statusKind` 只映射 presence；新增 `metadataStateKind/Label`；卡片 `value` 读 `summary.value?.[...] ?? 0`。
4. 绿：`npm test -- --run`、`npm run build`，随后重新生成 embed 产物并跑 `scripts/verify-web-embed.sh`。

### T5 文档与 OpenAPI

按第 7 节清单更新，执行 `python3 scripts/gen_doc_index.py`，不得手工编辑索引。

## 9. 预期结果

线上应看到：21 条 `legacy-%` 幽灵行被回收；6 个在线客户端的 `status` 全为 `online`；其中 5 个 `metadataState=reported`、1 个为 `unavailable`；`metadataState=expired` 计数为 0；卡片数字与全量一致。

## 10. 回滚注意事项

- 无 Schema 变更：回滚 = 回退应用版本并重启 `tunnelmesh-server`，5 分钟内可完成，不需要反向 SQL。
- 旧版 UI 读新版 API：`status` 值域收窄为 `online|offline`，旧前端会少显示两种状态但不报错；新版 UI 读旧版 API 时 `summary`/`metadataState` 缺失，前端以 `?? 0` / `'reported'` 兜底，因此可单侧先回滚 Server 或 Web。
- 已删除的 legacy 行是纯观测数据，无业务事实，删除不可逆但不影响功能；如需保留，回滚前 `CREATE TABLE client_instance_metadata_backup_20260924 AS SELECT * FROM client_instance_metadata`。
- 灰度：多 Server 节点必须整批升级到同一版本，避免新旧节点对同一行写入不同 `metadataState` 语义。

## 11. 验证命令

```bash
go test ./internal/storage -run 'ClientInstance|Purge|Capabilities' -count=1
go test ./internal/server -run 'ClientView|ClientMetadataSweeper|Release|ListClients' -count=1
go test ./... -count=1
go test -race ./...
go vet ./...
cd web && npm test -- --run && npm run build
./scripts/verify-web-embed.sh
git diff --check
```

MySQL 5.6 只读核验（在目标实例上执行，聚合 SELECT 可执行，DELETE 只允许 `EXPLAIN`/`PREPARE`，禁止 `EXECUTE`）：

```sql
SELECT VERSION();  -- 期望 >= 5.6
SELECT COUNT(*),
  COALESCE(SUM(CASE WHEN EXISTS (SELECT 1 FROM client_connection_leases c
      WHERE c.client_instance_id=client_instance_metadata.id AND c.expires_at>?) THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN metadata='{}' THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN stale=1 AND metadata<>'{}' THEN 1 ELSE 0 END),0)
  FROM client_instance_metadata;
EXPLAIN DELETE FROM client_instance_metadata
  WHERE metadata='{}' AND (expires_at IS NULL OR expires_at<=?)
  AND NOT EXISTS (SELECT 1 FROM client_connection_leases c
      WHERE c.client_instance_id=client_instance_metadata.id AND c.expires_at>?);
```

`EXPLAIN` 必须产出计划且不得返回 1093（`Can't specify target table ... for update in FROM clause`）。

部署后手工核验：

```bash
curl -sS 'https://tunnelmesh-admin.example.com/api/v1/clients?limit=50' -H 'accept: application/json' -H "authorization: Bearer $TM_TOKEN" | jq '.data.summary, [.data.items[] | {status, metadataState, activeConnections}]'
```
