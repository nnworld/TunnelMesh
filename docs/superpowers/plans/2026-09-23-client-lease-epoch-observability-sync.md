# 客户端运行观测状态不同步修复实施计划

状态：已确认（用户指令“所有问题处理完，创建 mr 合并 main”）

## 1. 现象

客户端运行观测页（`/clients`）出现三处互相矛盾的数据：

1. 列表行的「活跃 WS 连接」「活跃流」恒为 `0`，「Server 节点」为 `—`。
2. 列表行的状态显示「metadata 已过期」（`stale`），但客户端进程确实在线且持续收发。
3. 打开详情抽屉，「WS 连接」表格却列出 4 条连接，且「连接 Epoch」全部是 `2147483647`。
4. 表格上方的汇总卡片「在线客户端 / 活跃 WS 连接 / 活跃流 / metadata 未上报」全为 `0`，与详情抽屉不一致。

## 2. 根因

### 根因 A：MySQL 把 64 位连接 Epoch 截断成 32 位

- `internal/server/ws_client.go:553` 的 `newClientConnectionEpoch()` 用 8 字节随机数生成 `int64` epoch，作为 Client 物理连接的 fencing token。
- `migrations/ddl.sql:310` 与 `migrations/incremental/v0010_to_v0011/mysql.sql:23` 把 `client_connection_leases.connection_epoch` 声明为 `INTEGER`。MySQL 的 `INTEGER` 是有符号 32 位，最大 `2147483647`。
- 非严格模式下 MySQL 把超范围值**钳制**为 `2147483647` 写入，这正是详情抽屉里 4 条连接 Epoch 全部相同的原因。
- `clientConnectionRepo.Renew`（`internal/storage/client_repository.go:226`）与 `UpdateStats`、`Release` 都以 `WHERE connection_id=? AND connection_epoch=?` 作为条件，传入的是内存中的真实 int64 epoch，与库里的 `2147483647` 永远不相等 → `RowsAffected()=0` → `checkAffected` 返回 `sql.ErrNoRows`。
- 对照组：`agent_connection_leases.connection_epoch` 在 `migrations/incremental/v0006_to_v0007/mysql.sql:8` 是 `BIGINT`，且 agent 的 epoch 是从小整数自增（`internal/agent/websocket.go:132`），所以 Agent 侧没有触发该缺陷。

### 根因 B：租约续期失败会连带吞掉 metadata 续期

`ClientObservabilityService.Heartbeat`（`internal/server/client_observability.go:134`）：

```go
if err := s.leases.Heartbeat(ctx, record); err != nil {
    return err          // ← 提前返回
}
return s.instances.TouchInstance(ctx, ...)
```

根因 A 让 `s.leases.Heartbeat` 每次都返回 `sql.ErrNoRows`，于是 `TouchInstance` **永远不会执行**：

- `client_instance_metadata.expires_at` 停留在上一次 metadata 上报时刻，超过 `DefaultClientMetadataTTL = 5 * time.Minute` 后被 `ClientMetadataSweeper` 标记 `stale=1`（`internal/server/client_metadata_sweeper.go:43`）→ `newClientView` 返回 `status="stale"`（`internal/server/client_api.go:270`）→ 前端显示「metadata 已过期」。
- `client_connection_leases.expires_at` 同样不再续期，超过 `DefaultClientConnectionLeaseTTL = 90 * time.Second` 后，`newClientView` 的 `connection.ExpiresAt.After(now)` 判定为假 → `activeConnections`、`activeStreams`、`serverNodeIDs` 全部为空。
- 汇总卡片（`web/src/views/Clients.vue:196`）完全由列表行派生，因此一起归零。

一个 32 位列宽问题，经由「提前返回」放大成了整页观测数据失效。

### 根因 C：详情抽屉把已过期租约渲染成活跃连接

`GET /api/v1/clients/{id}/connections` 返回 `ListByInstance` 的全部租约行，不区分是否过期；`ClientConnectionView` 已经带有 `ExpiresAt`（`internal/server/client_api.go:57`），但前端没有据此标注状态，也仍然允许对已过期租约点「关闭连接」。这就是「列表 0 条活跃、详情 4 条连接」这一自相矛盾的观感来源。

### 根因 D：全量 DDL 与增量脚本漂移

`migrations/ddl.sql:261` 把 `agent_connection_leases.connection_epoch` 写成 `INTEGER`，而权威增量 `v0006_to_v0007/mysql.sql:8` 是 `BIGINT`。空库 auto-init 走 `migrations.DDL`（`internal/storage/db.go:325`），因此**全新 MySQL 部署拿到的是窄类型，升级上来的库反而是宽类型**，违反「`migrations/ddl.sql` 是当前 Schema 状态的唯一权威全量脚本」。该漂移没有被发现，是因为 MySQL 相关测试由 `TUNNELMESH_TEST_MYSQL_DSN` 网关控制，CI 中不执行，而 SQLite 的 `INTEGER` 本身就是 64 位。

## 3. 目标

1. MySQL 下 `connection_epoch` 能无损保存 int64 fencing token，租约续期、统计更新与释放恢复生效。
2. 已经连接中的 Client 在 Server 升级后**无需重连**即可自愈。
3. metadata 存活判定不再被租约写入失败连带拖垮：物理 WebSocket 在线本身就是实例存活的证据。
4. 详情抽屉如实区分「活跃租约」与「已过期租约」，过期租约不再提供关闭入口。
5. 汇总卡片覆盖表格中实际出现的 `stale` 状态，卡片与行不再各说各话。
6. 消除全量 DDL 与增量脚本在 epoch 列宽上的漂移，并补上 SQLite 侧就能执行的防漂移测试。

## 4. 非目标

- 不缩小 epoch 取值范围去迁就 32 位列。epoch 是 fencing token，削弱它等于削弱 stale generation 防护。
- 不在迁移脚本里回写已被钳制的历史行。那些租约早已过期，活跃连接由心跳自愈负责修正。
- 不改 Agent 侧的 epoch 生成与租约机制（仅同步列宽，保持 DDL 与增量一致）。
- 不引入新配置项，不改心跳间隔与 TTL 默认值。
- 不新增 API，`ClientConnectionView` 字段保持不变。

## 5. 设计决策

### 决策 1：Schema 变更走 v14 → v15，MySQL 加宽、SQLite 空操作

新增 `migrations/incremental/v0014_to_v0015/`：

- `mysql.sql`：`ALTER TABLE client_connection_leases MODIFY connection_epoch BIGINT NOT NULL;` 与 `ALTER TABLE agent_connection_leases MODIFY connection_epoch BIGINT NOT NULL;`。`MODIFY` 只放宽类型，不重写数据语义，MySQL 5.6+ 均支持；对已经是 `BIGINT` 的库重复执行是幂等的（类型相同，不报错），满足「增量迁移必须可安全重试」。
- `sqlite.sql`：SQLite 的 `INTEGER` 已是 64 位，且不支持 `ALTER COLUMN`，因此结构上无需变更。脚本执行一条幂等空操作 `UPDATE schema_meta SET version=version WHERE id=1;` 并在注释中说明原因，避免 `applySchemaStatements` 收到纯注释脚本。

同步更新 `migrations/ddl.sql` 两处 `connection_epoch` 为 `BIGINT`（`:261` agent、`:310` client），并把 `internal/storage/db.go:23` 的 `SchemaVersion` 从 `14` 提升到 `15`，在 `initializeSchema` 的 `switch` 中补 `case 14`，在 `migrations/migrations.go` 补 `V14ToV15MySQL` / `V14ToV15SQLite` 的 `go:embed`。

版本语义：这是向后兼容的列宽扩展（旧二进制读 `BIGINT` 列仍得到 int64），按 AGENTS.md 属于 **MINOR** 级别 Schema 变更，允许滚动升级期间新旧 Server 同时访问数据库。

### 决策 2：心跳自愈——续期匹配不到行就用权威 epoch 重新登记

`ClientConnectionLeaseController.Heartbeat`（`internal/server/client_connection_lease.go:59`）在 `Renew` 返回 `sql.ErrNoRows` 时，回退调用 `Register` 用内存中的真实 epoch 覆盖该行：

- 内存 `ClientSessionManager` 是活跃连接计数的权威来源，租约只是「有界持久快照」，用权威值修正快照符合既有的单一数据源约束。
- `Register` 已有 `connection_id` 归属校验与 `connection_epoch >= existingEpoch` 的单调性保护，重登记不会破坏 fencing，也不会跨实例串号。
- 这让生产环境在**执行迁移之前**就已连接中的 Client 也能在下一次心跳（≤30s）恢复，无需等待客户端重连。
- 自愈还要求 `record.ClientInstanceID` 非空。`CLIENT_HELLO`（或 legacy 降级）之前租约行本就还不存在，此时建行会留下一条无法被任何按实例查询归因的孤儿记录，破坏「每条租约都指向一个已知客户端实例」的既有不变量。
- 只有当 `Renew` 返回的是其他错误（连接中断、SQL 失败）时才向上抛出，避免把真实故障伪装成自愈。

### 决策 3：metadata 续期与租约续期解耦

`ClientObservabilityService.Heartbeat` 改为两步都执行、用 `errors.Join` 汇总错误：

- 租约写入失败不再阻止 `TouchInstance`。物理 WebSocket 仍在收帧，实例存活是既成事实，`stale` 标记应只反映「客户端真的不再上报」，而不是「某张表的写入路径出过问题」。
- 仍然返回错误，让调用方与日志能观察到租约侧异常，不做静默降级。
- `TouchInstance` 自身失败（例如实例行尚未由 metadata hello 建立）也照常上报，保持与现有语义一致。

### 决策 4：详情抽屉按 `expiresAt` 标注租约状态

- `web/src/views/Clients.vue` 的「WS 连接」表格新增一列，用 `StatusTag` 渲染 `live` / `expired`（复用现有 `success` / `info` 语义），判定依据是 `expiresAt > now`，与后端 `newClientView` 的 `activeConnections` 口径完全一致。
- 已过期租约不再渲染「关闭连接」按钮：关闭一条不存在的连接只会得到 `stale_epoch` 或 `remote_node_unavailable`，是无效操作。
- 判定使用一个随详情加载刷新的 `now` 基准，避免抽屉长时间停留时状态漂移。

### 决策 5：汇总卡片覆盖 `stale`

`summaryCards` 增加第 5 张「metadata 已过期」卡片，统计 `status === 'stale'` 的行数。CSS 网格从 `repeat(4, ...)` 调整为 `repeat(5, ...)`，`760px` 以下仍回落为两列。这样卡片集合与表格状态列的取值域一一对应，用户看到的每个状态都能在汇总里找到对应量。

### 决策 6：补 SQLite 侧可执行的防漂移测试

现有 `TestSQLiteFreshSchemaMatchesIncrementalIdentityChain` 只比对 identity 相关列名，MySQL 类型断言又全部被 `TUNNELMESH_TEST_MYSQL_DSN` 网关挡住，这正是漂移能长期存活的原因。新增纯文本断言测试：

- `migrations.DDL` 中 `agent_connection_leases` 与 `client_connection_leases` 的 `connection_epoch` 必须是 `BIGINT`。
- `migrations.V14ToV15MySQL` 必须同时加宽两张表，且不得使用 MySQL 5.6 不兼容的片段。
- `SchemaVersion` 与增量目录数量、`initializeSchema` 的 `case` 分支保持连续。

## 6. 精确文件清单

### 新增

- `migrations/incremental/v0014_to_v0015/mysql.sql`
- `migrations/incremental/v0014_to_v0015/sqlite.sql`
- `docs/superpowers/plans/2026-09-23-client-lease-epoch-observability-sync.md`（本文件）
- `docs/pull-requests/2026-09-23-client-lease-epoch-observability-sync.md`
- `web/src/tests/clients-view.spec.ts`

### 修改

- `migrations/ddl.sql`：两处 `connection_epoch INTEGER` → `BIGINT`
- `migrations/migrations.go`：新增 `V14ToV15MySQL` / `V14ToV15SQLite` embed
- `internal/storage/db.go`：`SchemaVersion` 15；`initializeSchema` 补 `case 14`
- `internal/storage/client_repository_test.go`：`TestSchemaVersionIs14` → `TestSchemaVersionIs15`；新增 int64 epoch 往返测试与 DDL 漂移断言
- `internal/storage/sqlite_test.go`：新增 v14 → v15 升级链测试
- `internal/storage/mysql_test.go`：Schema 版本断言 14 → 15
- `internal/server/client_connection_lease.go`：`Heartbeat` 自愈回退
- `internal/server/client_connection_lease_test.go`：自愈测试
- `internal/server/client_observability.go`：`Heartbeat` 解耦 + `errors.Join`
- `internal/server/client_observability_test.go`：租约失败仍续期 metadata 的测试
- `web/src/views/Clients.vue`：租约状态列、过期禁止关闭、第 5 张卡片、网格列数
- `web/src/i18n/messages/zh-CN.ts` / `en-US.ts`：`clients.leaseState`、`clients.leaseStateLabel.{live,expired}`、`clients.metadataStale`
- `web/src/i18n/schema.ts`：同步消息 schema（若该文件枚举键）
- `docs/operations/schema-upgrades.md`：新增 v14 → v15 章节
- `docs/operations/troubleshooting.md`：新增排查条目
- `docs/superpowers/plans/README.md`、`docs/pull-requests/README.md`、`docs/README.md`：由 `scripts/gen_doc_index.py` 重新生成

## 7. TDD 步骤

### 步骤 1：Schema 与漂移断言（先红）

在 `internal/storage/client_repository_test.go` 新增：

- `TestSchemaVersionIs15`（替换 `TestSchemaVersionIs14`）。
- `TestClientConnectionLeaseStoresFullInt64Epoch`：用 `math.MaxInt32+12345` 这类超出 32 位的 epoch 执行 `Register` → `Renew` → `UpdateStats` → `Release`，断言每一步都不返回 `sql.ErrNoRows`，且 `Get` 读回的 epoch 与写入值完全相等。
- `TestMigrationDDLSetsConnectionEpochBigInt`：文本断言 `migrations.DDL` 两张租约表的 `connection_epoch` 为 `BIGINT`。
- `TestV14ToV15MySQLWidensBothLeaseTables`：断言脚本包含两条 `MODIFY connection_epoch BIGINT NOT NULL`，且不含 MySQL 5.6 不兼容片段。

预期失败：`SchemaVersion = 14, want 15`；DDL 断言报 `INTEGER`；`V14ToV15MySQL` 未定义导致编译失败。

### 步骤 2：最小实现 Schema 变更（转绿）

按决策 1 修改 `ddl.sql`、`migrations.go`、`db.go`，新增两个增量脚本。运行 `go test ./internal/storage/ -run 'SchemaVersion|Epoch|Migration|V14' -count=1` 预期全绿；再跑 `go test ./internal/storage/ -count=1` 确认 `TestSQLiteFreshSchemaMatchesIncrementalIdentityChain` 与升级链测试未被破坏。

### 步骤 3：心跳自愈与解耦（先红）

- `internal/server/client_connection_lease_test.go` 新增 `TestClientConnectionLeaseHeartbeatSelfHealsEpochMismatch`：用一个 `Renew` 对真实 epoch 返回 `sql.ErrNoRows`、但接受重登记的 fake repository，断言 `Heartbeat` 最终无错，且 `Register` 收到的 epoch 等于内存中的真实 epoch。
- `internal/server/client_observability_test.go` 新增 `TestClientObservabilityHeartbeatTouchesMetadataWhenLeaseFails`：让租约控制器持续失败，断言 `TouchInstance` 仍被调用且返回的错误中包含租约侧错误。

预期失败：自愈测试报 `sql.ErrNoRows`；解耦测试报 `TouchInstance` 调用次数为 0。

### 步骤 4：最小实现服务端修复（转绿）

按决策 2、3 修改 `client_connection_lease.go` 与 `client_observability.go`。运行 `go test ./internal/server/ -run 'ClientConnectionLease|ClientObservability' -count=1` 预期全绿。

### 步骤 5：前端（先红）

新增 `web/src/tests/clients-view.spec.ts`，参照 `web/src/tests/agents-view.spec.ts` 的挂载方式：

- 汇总卡片渲染 5 项，且 `stale` 行的数量被正确统计。
- 详情抽屉中 `expiresAt` 在未来的连接标记为活跃、在过去连接标记为已过期。
- 已过期连接不渲染「关闭连接」按钮。

预期失败：卡片数量断言为 4；租约状态列文案不存在。

### 步骤 6：最小实现前端（转绿）

按决策 4、5 修改 `Clients.vue` 与两份 i18n 消息，`cd web && npm test -- --run` 预期全绿。

## 8. 验证命令

```bash
go build ./...
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web && npm test -- --run && npm run build && cd ..
rsync -a --delete web/dist/ internal/server/web_dist/
./scripts/verify-web-embed.sh
python3 scripts/gen_doc_index.py
```

MySQL 侧如具备环境，额外执行：

```bash
TUNNELMESH_TEST_MYSQL_DSN='<dsn>' go test ./internal/storage/ -run MySQL -count=1
```

## 9. 发布与回滚

- **发布范围**：仅 Server 需要升级。Agent 与 Client 二进制无改动，协议帧、能力协商与 API 契约均未变化，不需要重新分发客户端。
- **升级顺序**：备份 → 确认 `schema_meta.version=14` → 启动新版 Server（`storage.auto_init: true`）自动执行 `v0014_to_v0015` → 校验 `SHOW COLUMNS FROM client_connection_leases LIKE 'connection_epoch'` 返回 `bigint`。
- **自愈窗口**：迁移完成后，已连接的 Client 在下一个心跳周期（默认 30s）内由决策 2 修正被钳制的 epoch，列表与卡片随即恢复真实数值，无需重启客户端。
- **锁表影响**：`MODIFY` 到更宽的整型在 MySQL 8.0 可走 `ALGORITHM=INPLACE`；5.7 及以下会重建表。`client_connection_leases` 行数上界为活跃物理连接数（通常百级），`agent_connection_leases` 同量级，预计锁表时间秒级，可在业务低峰执行。
- **回滚**：列宽收窄回 `INTEGER` 会再次触发钳制，属于不可逆风险，因此回滚策略是**保留 v15 Schema 并回退应用版本**；旧版 Server 读 `BIGINT` 列得到 int64，功能正常。确需还原结构时只能通过升级前备份恢复。
- **5 分钟止损**：若新版 Server 启动失败，直接回退二进制即可，v15 Schema 对旧版本向后兼容，无需回滚数据库。

## 10. Reviewer 关注点

1. `MODIFY connection_epoch BIGINT` 在 MySQL 5.6/5.7/8.0 上的重复执行是否确实幂等（升级中断重试路径）。
2. 决策 2 的自愈是否可能与并发 `Release` 竞争，把一条正在关闭的连接重新登记成活跃租约。
3. 决策 3 用 `errors.Join` 后，`ws_client.go:173` 的 `_ = clientObservability.Heartbeat(...)` 仍然丢弃错误，是否需要补日志或指标。
4. 详情抽屉的 `now` 基准刷新时机是否足够，长时间停留的抽屉会不会重新产生状态漂移。
