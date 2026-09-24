# Client 观测状态模型修复

## Title

`fix(server): split client presence from metadata state`

## Target branch

`main`

## Summary

Clients 列表把“是否存在”和“metadata 是否新鲜”拆成两个字段：`status` 只由未过期连接租约决定，`metadataState` 由 `CLIENT_HELLO` 上报事实与 TTL 决定。同时修复了在线客户端被判成 `metadata_unavailable` 的推导缺陷、未上报实例记录永久残留成“已过期”幽灵行的问题，并把统计卡片改为服务端按筛选条件聚合的全量数字。

实施计划见 `docs/superpowers/plans/2026-09-24-client-status-metadata-state.md`。

## 问题与根因

线上 `tunnelmesh` 库 27 行 `client_instance_metadata` 与 `GET /api/v1/clients` 实测：

1. `persistMetadata` 对 `payload.Capabilities` 执行 `json.Marshal`。Client 协议字段是 `capabilities,omitempty` 且 Client 侧没有该字段的生产者，nil slice 序列化成字符串 `"null"`；`clientInstanceRepo.Upsert` 只把 `""` 归一化成 `"[]"`，于是 `"null"` 入库。`decodeStrings("null")` 返回 nil，`newClientView` 把 nil 当作“未上报 metadata”，因此 5 个带完整 hostname/version/listeners 的在线客户端全部显示 `metadata_unavailable`；反倒是 `metadata='{}'`、`capabilities="[]"` 的 legacy 行显示 `online`。
2. `RegisterLegacy` 使用 `"legacy-" + connectionID` 作为 instance_id，即每条物理连接一行。Client 连接池与重连每次产生新行，旧行 5 分钟后被 `MarkExpired` 置 `stale=1`，而 `ClientInstanceRepository` 没有任何删除能力，`ClientMetadataSweeper` 只标记不回收。21 行 `stale=1` 记录全部是 `legacy-%`，最早一条 `last_seen_at=2026-09-23T10:28:42Z`。同一台机器表现为“1 条在线 + N 条 metadata 已过期”。
3. `newClientView` 的优先级是 `stale` > `metadata_unavailable` > `online`，`offline` 只是兜底，导致已断开的记录显示“metadata 已过期”而不是“离线”，也让“在线”与“未上报”两个统计互相矛盾。
4. `web/src/views/Clients.vue` 的 5 张卡片由 `items.value.filter/reduce` 计算，即当前游标页，而不是筛选后的全量，因此与表格和真实总数不一致。

## User impact

- 在线客户端不再显示 `metadata 未上报`；presence 与 metadata 时效分列展示，`GET /api/v1/clients` 每个 item 新增 `metadataState`。
- 断开的历史行显示 `offline`，并在下次清扫时被回收，列表不再堆积“metadata 已过期”幽灵。
- 统计卡片与服务端聚合一致，翻页与筛选后仍然正确。

## API、Schema 与配置影响

- `GET /api/v1/clients` 响应新增 `summary` 对象与 item 字段 `metadataState`；`status` 值域收窄为 `online|offline`。
- 查询参数新增 `metadataState`（`fresh|expired|unavailable|reported`）。`status=stale|metadata_unavailable` 保留为 `metadataState=expired|unavailable` 的 Deprecated 别名，一个 MINOR 版本后再删除。`openapi.yaml` 已标注 `deprecated: true`。
- 无数据库 Schema 变更：不新增列、不修改 `migrations/ddl.sql`、不提升 `SchemaVersion`，因此 `docs/operations/schema-upgrades.md` 无需版本章节。
- 无配置项、无协议 frame 变更。

## 安全与授权影响

- `Summarize` 复用 `clientInstanceConditions`，与 `List` 同一 WHERE，统计不会越权暴露他人 Client。
- `metadataState` 判定依赖服务端持久事实：`persistMetadata` 写入的 payload 必含非空 `instance_id`，`{}` 只可能由 `RegisterLegacy` 写出，Client 无法伪造“已上报”。
- `DeleteUnreported`/`PurgeUnreported` 只匹配 `metadata='{}'`，且 purge 额外要求无未过期租约并已过 TTL，因此已上报实例与握手过程中的记录不会被删。
- 未新增日志字段，不输出 metadata 值、Token 或凭据。

## 测试证据

- 红灯：`TestClientInstanceUpsertNormalizesNullCapabilities` 修复前读回 `"null"`；`TestClientInstanceSummaryMatchesFilter`、`TestClientInstancePurgeUnreportedOrphans`、`TestClientInstanceStatusFilterIsPresenceOnly`、`TestClientInstanceDeleteUnreportedKeepsReportedRows` 因 `Summarize`/`PurgeUnreported`/`DeleteUnreported`/`MetadataState` 不存在而编译失败。
- 红灯：`TestNewClientViewSplitsPresenceFromMetadataState` 修复前 `connected and fresh` 用例返回 `metadata_unavailable`；`TestClientAPIListReportsWholePopulationSummary`、`TestClientAPIFiltersMetadataStateAndAcceptsDeprecatedStatus` 因 `metadataState`/`summary` 缺失失败；`TestClientObservabilityReleaseDropsUnreportedRow`、`TestClientMetadataSweeperPurgesUnreportedOrphans` 因未回收而失败。
- 绿灯：`go test ./internal/storage -run 'ClientInstance|Purge|Capabilities' -count=1`、`go test ./internal/server -run 'ClientView|ClientAPI|ClientObservability|ClientMetadataSweeper|PersistedClientHello' -count=1` 通过。
- 前端红灯：`clients-api.spec.ts` 三条新用例与 `clients-view.spec.ts` 汇总用例先失败；`web/src/tests/clients-view.spec.ts` 重写为断言卡片来自服务端 `summary`，并新增“在线 + metadata 已过期”双标签渲染用例。
- 前端绿灯：`npm test -- --run` 35 文件 302 用例通过；`npm run build` 成功且 `scripts/verify-web-embed.sh` 报告 `web/dist and internal/server/web_dist match`。
- `go test ./... -count=1` 通过。
- `go vet ./...` 通过。
- `git diff --check` 通过。

## 发布步骤

1. 多节点集群需整批部署同一版本的新版 `tunnelmesh-server` 后滚动重启；无需 DB 变更前置步骤。
2. `tunnelmesh-agent` 与 `tunnelmesh-client` 不需要升级：本次未改动协议、Agent 与 Client 代码路径。
3. 浏览器需强制刷新以获取新的管理后台产物（嵌入在 Server 二进制内）。
4. 若希望在升级前留档，可执行 `CREATE TABLE client_instance_metadata_backup_20260924 AS SELECT * FROM client_instance_metadata;`。
5. 上线后核验：`GET /api/v1/clients?limit=100` 的 `summary.total` 与 `items` 全量一致，且在线客户端 `status=online`。

## 回滚步骤

1. 回退 `tunnelmesh-server` 到上一版本并重启即可，Schema 未变更，不需要反向 SQL。
2. 被回收的 `legacy-%` 行是纯观测数据，不承载业务事实，删除不可逆但不影响功能；如需精确还原，使用发布步骤第 4 条的备份表。
3. 旧版 Server 读取新版数据无兼容问题（未使用 `metadataState` 列，判定字段 `stale`/`metadata` 语义未变）。

## Reviewer 关注点

- `internal/server/client_api.go` 的 `clientMetadataReported` 与 `internal/storage/client_repository.go` 的 `metadata='{}'` 条件必须是同一判定，二者不一致会让卡片与行标签矛盾。
- `Summarize` 的时间比较沿用仓库既有的 RFC3339 文本字典序约定，未引入数据库端时间函数；请确认对 `VARCHAR(32)`/`TEXT` 时间列的依赖符合预期。
- `PurgeUnreported` 每 30 秒扫描一次 `client_instance_metadata`，谓词 `metadata='{}'` 与 `expires_at` 都无索引，故为全表扫描。该表规模由 Client 安装数决定，且回收本身会让它收敛，因此未把 `stale=1` 加进谓词去蹭 `idx_client_instance_metadata_stale`：那会让删除正确性依赖“`MarkExpired` 必须先跑”的调用顺序。
- MySQL 语法兼容性：CI 未启用 `TUNNELMESH_TEST_MYSQL_DSN`，因此按项目下限 **MySQL 5.6** 在生产库（`VERSION()=5.6.51-91.0-log`，`sql_mode=NO_ENGINE_SUBSTITUTION`）做了只读核验。
  - `Summarize` 的两条聚合 SELECT 实际执行，返回 `total=27, online=6, metadataUnavailable=22, metadataStale=0, activeConnections=16, activeStreams=96`，与逐项解算一致；即 `SUM(CASE WHEN EXISTS (相关子查询) ...)` 与 `IN (SELECT ...)` 在 5.6 上可用。
  - 两条 DELETE 用 `EXPLAIN` 生成执行计划（不执行）：`PurgeUnreported` 计划为 `PRIMARY client_instance_metadata` + `DEPENDENT SUBQUERY c`（命中 `idx_client_connection_leases_instance`），未触发 1093“target table”限制；`DeleteUnreported` 走 `PRIMARY` range。
  - 对照组：`PREPARE` 一条引用不存在列的语句确实返回 `Error 1054`，证明该语法检查有效，而不是恒真。
  - 未使用 CTE、窗口函数、JSON 函数、`ALGORITHM=INSTANT`、8.0 专属语法，也不依赖 `utf8mb4` 索引前缀超过 767 byte。
- 因此合并后首次清扫预计把 `metadataUnavailable` 从 22 降到 1（仅剩当前仍在线、且未协商 metadata 子协议的旧客户端），`metadataStale` 保持 0。

## 集成状态

实施计划：`docs/superpowers/plans/2026-09-24-client-status-metadata-state.md`（已确认）。
