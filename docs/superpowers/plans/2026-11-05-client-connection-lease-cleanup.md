# 客户端连接租约过期回收与历史读取上限实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让已经过期的客户端连接租约（`client_connection_leases`）不再无限堆积：Server 周期回收“过期已超过保留窗口”的租约行，读取路径只把仍有效的租约当作事实、把历史租约变成显式可选且有上限的视图，管理后台客户端详情默认只显示在线的 WS 连接并允许勾选查看近期历史。同时修正“详情里一大堆已下线的 WS”这一现象的根因，而不是在前端过滤掉显示。

**Architecture:** 三层，各自可独立验证。

1. 存储层：`internal/storage/client_repository.go` 的 `clientConnectionRepo` 新增 `DeleteExpired(ctx, at, limit) (int64, error)`，按 `expires_at<=?` 批量删除，命中既有 `idx_client_connection_leases_expires`（`migrations/ddl.sql:328`）；`ListByInstance`/`ListByInstances` 增加“只看有效租约 / 历史上限”参数。无 DDL 变更，无 `SchemaVersion` 变更。
2. 服务层：新增 `internal/server/client_connection_sweeper.go`，模板对齐 `internal/server/client_metadata_sweeper.go`（`Run` 先扫一遍再进 ticker、`Sweep(ctx, now)` 可单测、幂等、`errors.Join` 保留两个失败面）与 `internal/server/audit_retention_sweeper.go`（“同一批 DELETE 以时间为边界，重复执行只删 0 行，因此每个节点各自跑一份是安全的，不需要分布式锁”），在 `internal/server/runtime.go` 与既有 sweeper 一起启停。新增一个 Prometheus counter 记录回收结果。
3. 接口与前端：`GET /api/v1/clients/{clientInstanceId}/connections` 支持 `state=live|all`（默认 `live`）和 `limit`，`docs/api/openapi.yaml:910` 同步；`web/src/api/client.ts:458` 透传参数，`web/src/views/Clients.vue:145-167` 增加“显示历史租约”开关，保留 `leaseLive()`（`:383`）与租约状态列（`:159-160`）作为历史视图下的标注。

**Tech Stack:** Go 1.x 标准库（`database/sql`、`context`、`time`、`log/slog`）、`prometheus/client_golang`、既有 `internal/storage` 双驱动 contract test 框架、Vue 3 `<script setup>` + TypeScript + Element Plus + vue-i18n、Vitest + jsdom。不新增第三方依赖。

**Spec:** 无新增设计规格与 ADR（不改变协议、不改变租约语义，只补回收与读取边界）。引用的既有事实源与约束：
- AGENTS.md §5「MQ 容错：死信队列有告警；定时任务：分布式锁防重复」——本计划按 §5「快速恢复」与 §6「单一数据源」取舍，理由见「架构决策」第 3 条；§7 要求关键回收动作可观测；§12 要求 5 分钟止损。
- AGENTS.md「数据库」：无 Schema 变更，因此不需要 `migrations/` 与升级文档动作；`idx_client_connection_leases_expires` 首次被真实使用。
- AGENTS.md「一致性与并发」：过期判定只以时间戳为边界，不依赖进程内状态。
- `docs/user-guide/server-admin.md` 的「Client 运行观测与连接管理」段落（描述详情抽屉里的 WS 连接列表）与 `docs/operations/configuration.md` 的租约/TTL 说明，属活文档，必须随本次行为变更同步。

## 背景：现象与根因（HEAD 起于 `80de13c`，含本次 Agent 名称改动后的工作树）

现象：客户端详情抽屉里的 WS 连接列表出现大量“已过期”的行，且行数只增不减。根因链条如下，每一条都在当前代码里可验证：

| # | 事实 | 位置 |
| --- | --- | --- |
| 1 | 租约 TTL 默认 90 秒，心跳续约 | `internal/server/client_connection_lease.go:14`（`DefaultClientConnectionLeaseTTL = 90 * time.Second`） |
| 2 | 唯一的删除路径是“按 connection_id + connection_epoch 删除”的正常释放；进程被杀、宿主掉电、网络中断时不会走到 | `internal/storage/client_repository.go:325`（`Release`）、`internal/storage/repository.go:341`（接口声明） |
| 3 | 客户端 WebSocket 正常关闭时释放租约的错误被显式忽略，因此“没删掉”是设计内允许的残留 | `internal/server/ws_client.go:208`（`_ = clientObservability.Release(context.Background(), principal)`） |
| 4 | 没有任何按过期时间删除的语句；`idx_client_connection_leases_expires` 建了但无消费者 | `migrations/ddl.sql:328`；全仓 `DELETE FROM client_connection_leases` 只出现在 `client_repository.go:329` |
| 5 | 抽屉读取 `ListByInstance`，**没有** `expires_at` 过滤、没有行数上限 | `internal/storage/client_repository.go:371`→`:375`（`ListByInstances` 的 SQL 只有 `client_instance_id IN (...)`，排序 `updated_at DESC`）；调用方 `internal/server/client_api.go:117`、`:130` |
| 6 | 列表页对每页实例批量读同一份未过滤数据，再在内存里丢弃过期行 | `internal/server/client_api.go:218`（`ListByInstances`）、`:301`（`newClientView` 只在 `connection.ExpiresAt.After(now)` 分支里累加 `activeConnections`/`activeStreams`/`tokenIDs`/`serverNodeIDs`） |
| 7 | 因此：实例越长寿、重启次数越多，抽屉里的死行越多；`Summarize`（`internal/storage/client_repository.go:193`）统计正确（只数 `expires_at>?`），但详情读的是原始行集，两者看起来互相矛盾 | 同上 |

补充事实（避免误判）：
- `newClientView` 的 Token / Server 节点两列只从有效租约构造（`internal/server/client_api.go:312-319`），因此死行**不会**污染这两列，也**不会**把状态算成在线；本次不改这段判定，只减少它拿到的无效输入。
- 列表页的实例存在性判定来自 `clientInstanceConditions`（`internal/storage/client_repository.go:421`）与 `PurgeUnreported`（`:235`，`ClientMetadataSweeper.Sweep` 调用，`internal/server/client_metadata_sweeper.go` 末段），租约行本身不参与删除，所以“实例行还在但租约全过期”= 离线，符合预期。
- 前端已有正确的一半：`leaseLive()`（`web/src/views/Clients.vue:383`）按 `expiresAt` 与 `connectionsNow` 比较，过期行显示 `已过期`、活跃流显示 `—`（`:153`）并隐藏关闭按钮（`:165`）。所以这不是显示 bug，而是数据没有回收。

## 架构决策

1. **回收放在 Server 内周期任务，而不是客户端侧或读取侧过滤。** 租约是服务端事实，权威来源是数据库；在前端过滤只是把无界增长藏起来，`ListByInstances` 的行数与内存仍随历史增长。
2. **保留窗口而不是立即删除：过期满 `retention` 才回收（默认 24h）。** 一次 `Renew` 失败或一次进程崩溃不应该立刻抹掉“这一小时前的这条连接”的证据；运维排障需要它，`docs/user-guide/server-admin.md` 已经把它当作可观测事实描述。保留窗口把每实例的行数上限从“无界”变成“约 1 天内的连接次数”。
3. **不引入分布式锁。** 与 `AuditRetentionSweeper`（`internal/server/audit_retention_sweeper.go:22-30` 注释）和 `AuthMaintenanceSweeper` 同一论证：删除语句以时间为边界、天然幂等，多节点并发最多删 0 行；引入 lease/epoch 会让一个清理任务变成新的故障源。集群里每节点各跑一份是可接受的最终回收。
4. **不做配置项。** 间隔与保留窗口用常量默认值（对齐 `DefaultWebSSHSweepInterval`、`DefaultClientMetadataSweepInterval` 的做法），避免新增 `server.*` 配置面与 `docs/operations/configuration.md` 的字段表变更。若后续确实需要调整，再按 MINOR 加配置，不在本次范围。
5. **读取参数向后兼容：`state` 缺省为 `live`。** 现有调用方（CLI、脚本）读到的是“当前事实”，与它们对 `activeConnections` 的既有预期一致；需要历史时显式 `state=all&limit=N`。这是行为收紧，因此必须同步 OpenAPI、活文档与用户帮助，并在发布步骤里说明。
6. **`ListByInstances` 走“只看有效租约”而不是“有效 + 历史”。** 列表页只需要 `newClientView` 的有效租约分支，传 `liveOnly=true` 减少行数是纯收益，且不改变任何输出字段语义。

## 全局约束

- 严格 TDD：每个任务先写失败测试并观察红灯输出，再做最小实现，最后重构。核心模块（存储层 SQL、sweeper、API 参数解析）禁止只靠手工验证。
- 所有时间比较沿用既有约定：库中存 RFC3339 文本，比较在应用侧取 `time.Now().UTC()` 后用参数传入，禁止使用数据库端时间函数（`client_repository.go:193` 上方注释是既有依据）。
- 必须同时验证 SQLite 与 MySQL：`DeleteExpired` 与带过滤的 `ListByInstance*` 进入 `runClientRepositoryContract`（`internal/storage/client_contract_test.go:27`，由 `runRepositoryContract` 调用，两种驱动跑同一组断言）。
- 日志不得输出 DSN、Token、客户端实例 ID 之外的敏感内容；回收日志只输出条数、耗时与结果。
- 未获得用户明确授权，不执行 commit、push、merge 或创建远端 PR。

## 文件清单

新增：
- `internal/server/client_connection_sweeper.go`
- `internal/server/client_connection_sweeper_test.go`

修改（Go）：
- `internal/storage/repository.go`（`ClientConnectionRepository` 接口，`:337-346`）
- `internal/storage/client_repository.go`（`DeleteExpired`；`ListByInstance`/`ListByInstances` 签名；SQL 过滤与上限）
- `internal/storage/client_contract_test.go`（新断言）
- `internal/storage/client_repository_test.go`（聚焦用例）
- `internal/server/client_api.go`（`:117`、`:130`、`:218` 调用点；`state`/`limit` 解析与权限不变）
- `internal/server/client_api_test.go`（API 行为）
- `internal/server/client_connection_lease_test.go`（`fakeClientConnectionRepo`，`:48`、`:51`）
- `internal/server/runtime.go`（sweeper 字段、启动、停止与错误汇聚）
- `internal/observability/metrics.go`（新增 counter 的字段、构造、注册与 helper）

修改（前端与文档）：
- `web/src/api/client.ts`（`listClientConnections` 参数，`:458`）
- `web/src/views/Clients.vue`（连接区开关与文案，`:141-167`、`:345-351`）
- `web/src/i18n/messages/zh-CN.ts`、`web/src/i18n/messages/en-US.ts`（`clients.showExpiredLeases`、`clients.connectionsTruncated`）
- `web/src/tests/clients-view.spec.ts`、`web/src/tests/clients-api.spec.ts`
- `docs/api/openapi.yaml`（`:910` 的 `parameters`）
- `docs/user-guide/server-admin.md`（Client 观测段）、`docs/operations/troubleshooting.md`（新增“详情里大量已过期 WS 连接”条目）
- `docs/superpowers/plans/README.md`、`docs/pull-requests/README.md`（由 `python3 scripts/gen_doc_index.py` 生成，禁止手改）

---

### 任务 1: 存储层按过期时间删除与有效租约读取

**Files:**
- Modify: `internal/storage/repository.go`
- Modify: `internal/storage/client_repository.go`
- Test: `internal/storage/client_contract_test.go`、`internal/storage/client_repository_test.go`

**Interfaces:**
- `func (r *clientConnectionRepo) DeleteExpired(ctx context.Context, at time.Time, limit int) (int64, error)`；接口加入 `DeleteExpired(context.Context, time.Time, int) (int64, error)`。
  - `at.IsZero()` 时取 `time.Now().UTC()`，与 `MarkExpired`（`client_repository.go:178`）和 `PurgeUnreported`（`:235`）一致。
  - `limit <= 0` 时取 `DefaultClientConnectionDeleteBatch = 1000`，命名与理由对齐 `DefaultAuditRetentionBatchRows`。
  - SQL 用 `DELETE FROM client_connection_leases WHERE expires_at<=?` 加驱动差异的上限：MySQL 用 `LIMIT ?`，SQLite 用 `DELETE ... WHERE connection_id IN (SELECT connection_id FROM client_connection_leases WHERE expires_at<=? LIMIT ?)`；按 `r.driver` 分支（`normalizeDriver` 已有，`repository.go:398`），并在 contract test 里断言两种驱动结果一致。
- `ListByInstance(ctx, clientInstanceID string, liveOnly bool, limit int)` 与 `ListByInstances(ctx, clientInstanceIDs []string, liveOnly bool, limit int)`：
  - `liveOnly` 追加 `expires_at>?`（同一 `time.Now().UTC()`）；`limit > 0` 时追加 `LIMIT ?`。
  - `limit <= 0` 取 `DefaultClientConnectionHistoryLimit = 50`（每实例上限，`ListByInstances` 的批量语义改为“每实例最多 N 条”，用窗口函数不现实，因此按 `client_instance_id` 分组在 Go 侧截断，保持既有 `ORDER BY client_instance_id,updated_at DESC,connection_id`）。
  - `clientConnectionConditions`（`:480`）已含过期过滤逻辑的复用点：把 `expires_at>?` 抽成 `clientConnectionLiveCondition(now)` 供两处调用，避免同一条件写三遍（AGENTS.md §8 DRY）。
  - `limit` 的含义是“最多返回这么多行”，**不在仓库内部多取一行**：截断探测由调用方按 `List`（`:333`）已有的“取 limit+1 再切”模式自己做（见任务 4）。

**TDD:**
- [x] 先在 `client_repository_test.go` 追加 `TestClientConnectionDeleteExpiredRemovesOnlyLapsedBounded`：插入 3 条过期 + 1 条有效 + 1 条“过期但在 limit 之外”，断言 `DeleteExpired(ctx, at, 2)` 返回 `2`、再调用返回 `1`、有效行仍在；断言过期时间恰等于 `at` 的行被删（边界 `<=`）。
- [x] 在 `runClientRepositoryContract` 追加：`ListByInstance(ctx, id, true, 0)` 只返回未过期租约、`ListByInstance(ctx, id, false, 1)` 返回按 `updated_at DESC` 的最新 1 条；`ListByInstances` 对两个实例分别截断。
- [x] 运行 `go test ./internal/storage -run 'ClientConnection' -count=1`，预期红灯：`too many arguments in call to db.ClientConnections().ListByInstance` / `r.DeleteExpired undefined`。
- [x] 实现接口与两种驱动分支，预期绿灯；再跑 `go test ./internal/storage -count=1`。

**为什么这样设计：** 删除必须分批，否则一台长期运行的 Server 第一次回收就是全表扫 + 大事务，锁住管理 API 仍在写的表；上限参数放在 SQL 而不是读出来再删，避免“读—改—写”竞态。

---

### 任务 2: Client 连接租约回收器

**Files:**
- Create: `internal/server/client_connection_sweeper.go`
- Create/Test: `internal/server/client_connection_sweeper_test.go`
- Modify: `internal/observability/metrics.go`

**Interfaces:**
- `const DefaultClientConnectionSweepInterval = time.Minute`；`const DefaultClientConnectionLeaseRetention = 24 * time.Hour`。
- `type ClientConnectionSweeper struct { leases storage.ClientConnectionRepository; interval, retention time.Duration; batchSize int; now func() time.Time; onReap func(int64, error); gauges ClientConnectionReapGauges }`
- `func NewClientConnectionSweeper(leases storage.ClientConnectionRepository, interval, retention time.Duration) *ClientConnectionSweeper`，非正回落默认值（对齐 `NewAuthMaintenanceSweeper`，`auth_maintenance_sweeper.go:52-62`）。
- `SetClock(now func() time.Time)`、`SetGauges(gauges ClientConnectionReapGauges)`（nil 即不发布，测试与无 registry 部署可用）。
- `Run(ctx) error`：先扫一次再进 ticker，`ctx.Done()` 返回 `ctx.Err()`，`Sweep` 出错返回错误（对齐 `ClientMetadataSweeper.Run`）。
- `SweepOnce(ctx) (int64, error)`：`cutoff = now().UTC().Add(-retention)`，调用 `DeleteExpired(ctx, cutoff, batchSize)`；`retention <= 0` 视为“立即回收过期行”仍走同一条路径。
- `ClientConnectionReapGauges = func(rows int64, err error)`；`internal/observability/metrics.go` 新增 `clientConnectionLeaseSweep *prometheus.CounterVec`，名 `tunnelmesh_client_connection_lease_sweep_total`，label `result`、`error_class`（构造位置 `:88` 附近，注册加入 `reg.MustRegister(...)` `:176`，helper `IncClientConnectionLeaseSweep(result, errorClass)` 与 `m.registryLeaseTotal.WithLabelValues(label(...))` 同一风格 `:289`）。
- 日志：`slog.Info("client connection lease sweep", ...)` 字段 `reaped`、`elapsed_ms`、`error`；不写连接 ID 与实例 ID。

**TDD:**
- [x] 先写 `client_connection_sweeper_test.go`（用 `internal/server/client_connection_lease_test.go:48` 的 fake 仓库风格，新增 `deleteExpiredCalls []time.Time` 与 `rows []*storage.ClientConnectionLease`）：断言 ① `SweepOnce` 传的 cutoff 恰为 `now-24h`；② `retention` 默认值生效；③ fake 返回错误时 `Run` 把错误透出且 counter 记 `result=error`；④ `SetGauges(nil)` 不 panic；⑤ 连续两次 `SweepOnce` 各调用一次删除（幂等由存储层保证，这里只断言不重复计数）。
- [x] 运行 `go test ./internal/server -run ClientConnectionSweeper -count=1`，预期红灯：`undefined: NewClientConnectionSweeper`。
- [x] 实现，预期绿灯；`go test ./internal/observability -count=1` 校验新 counter 的名字与 label 已通过 `promhttp` 输出断言（沿用该包既有的 metrics 测试文件写法）。

**为什么这样设计：** 1 分钟一轮、每轮一批，代价是一次带索引的范围删除；把“过期”和“可回收”分开，是为了让保留窗口成为可测试的参数而不是隐式行为。

---

### 任务 3: runtime 启停与错误汇聚

**Files:**
- Modify: `internal/server/runtime.go`

**Interfaces:**
- 结构体字段：`clientConnectionSweeper *ClientConnectionSweeper`、`clientConnectionSweeperCancel context.CancelFunc`、`clientConnectionSweeperDone chan error`（紧邻 `:130-139` 既有 sweeper 字段组）。
- 启动：在 `runtime.clientMetadataSweeper` 启动块（`:445-450`）之后，用 `db.ClientConnections()` 构造、`SetGauges` 接 metrics、`context.WithCancel` + goroutine 写入 done channel（复制 `:452-458` webssh 的写法）。
- 停止：在 `Stop` 的 sweeper 收敛段（`:516-528`）追加，`errors.Is(err, context.Canceled)` 过滤，错误并入 `:493` 的 `errors.Join` 变量列表（新增 `clientConnectionSweepErr`）。
- 构造位置必须在 `clientConnectionLeases := NewClientConnectionLeaseController(...)`（`:191`）之后，确保回收器与续约器共用同一个 repository。

**TDD:**
- [x] 先加 `internal/server/client_connection_sweeper_test.go` 的一条集成断言：`NewRuntime` + `Start`/`Stop` 后 fake repository 收到至少一次 `DeleteExpired` 调用，且 `Stop` 返回 `nil`（不泄漏 goroutine）。若 `runtime_test.go` 已有可复用的 in-memory runtime 构造（`internal/server/runtime_test.go`），在其旁边扩展而不是另起一套。
- [x] 运行 `go test ./internal/server -run 'Runtime.*Sweep|ClientConnectionSweeper' -count=1`，预期红灯：sweeper 未启动导致 `deleteExpiredCalls` 为空。
- [x] 接线实现，预期通过；`go vet ./...` 无输出。

---

### 任务 4: 详情接口只读有效租约，历史显式可选且有上限

**Files:**
- Modify: `internal/storage/repository.go`（若任务 1 的默认值常量放此处则跳过）
- Modify: `internal/server/client_api.go`
- Modify: `internal/server/client_api_test.go`
- Modify: `internal/server/client_connection_lease_test.go`（fake 签名）
- Modify: `docs/api/openapi.yaml`

**Interfaces:**
- `client_api.go:117`（详情视图用的连接）与 `:218`（列表页批量）改为 `ListByInstance(ctx, id, true, 0)` / `ListByInstances(ctx, ids, true, 0)`：这两处只用有效租约，收紧读取不改变任何响应字段。
- `client_api.go:130`（`GET /api/v1/clients/{id}/connections`）解析查询参数：
  - `state`：`live`（默认）| `all`；其他值返回 `422` 且 `data.error=invalid_state`（与既有参数校验风格一致，不新增错误语义分支）。
  - `limit`：十进制正整数，缺省 `50`，上限 `200`；非法返回 `422`。
  - 权限判定不变：仍先 `clientInstanceReadable(p, instance)`（`client_api.go:297`），`state=all` 不放宽可见范围。
  - 响应仍为 `{"connections": [...]}`，并新增布尔 `truncated`：handler 向 `ListByInstance(ctx, id, liveOnly, limit+1)` 多取一行，若拿到的行数超过 `limit` 就截断到 `limit` 并置 `truncated=true`（`client_repository.go:333` 的 `List` 已有同样写法，直接沿用）。
- OpenAPI `docs/api/openapi.yaml:910` 的该 path 增加 `state`、`limit` 两个 query 参数与 `truncated` 字段描述。

**TDD:**
- [x] 先在 `client_api_test.go` 扩展 `TestClientAPIDetailAndConnectionsExposeObservability`（`:78`）：插入一条有效 + 两条已过期的租约，断言默认 `GET` 只含有效行的 `connectionId`；`?state=all` 含三条且 `truncated=false`；`?state=all&limit=1` 只剩最新一条且 `truncated=true`；`?state=bogus` 返回 422；非属主访问仍 403（复用 `TestClientAPIRejectsOtherOwner`，`:96`）。
- [x] 运行 `go test ./internal/server -run ClientAPI -count=1`，预期红灯：`connections` 长度不符（当前实现返回全部行）。
- [x] 实现 API 与 OpenAPI，预期通过。

---

### 任务 5: 前端详情抽屉的历史租约开关

**Files:**
- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/Clients.vue`
- Modify: `web/src/i18n/messages/zh-CN.ts`、`web/src/i18n/messages/en-US.ts`
- Modify: `web/src/tests/clients-api.spec.ts`、`web/src/tests/clients-view.spec.ts`

**Interfaces:**
- `listClientConnections(clientInstanceId: string, params: { state?: 'live' | 'all'; limit?: number } = {})`，拼 `?state=...&limit=...`，返回 `{ connections, truncated }`；既有调用点全部传默认参数，行为等价于服务端默认 `live`。
- `Clients.vue` 连接区头部（`:141-144`）加 `el-switch` 绑定 `showExpiredLeases`，切换即以 `state: showExpiredLeases ? 'all' : 'live'` 重新 `loadConnections()`（`:345-351`）；`truncated` 为真时在表格上方渲染一行提示（`clients.connectionsTruncated`），并给出“加载更多历史”按钮把 `limit` 翻倍至 200。
- `leaseLive()`、租约状态列与“关闭”按钮的可见性规则保持现状（`:153`、`:159-160`、`:165`）：默认视图下所有行都是 live，历史视图下 `已过期` 标签正是它的作用。
- 新增 i18n 键：`clients.showExpiredLeases`（zh `显示历史租约`／en `Show lease history`）、`clients.connectionsTruncated`（zh `仅显示最近 {count} 条租约历史`／en `Showing the most recent {count} lease rows`）。两个键必须同时写入两份 locale（`web/src/tests/i18n.spec.ts` 断言键树一致）。

**TDD:**
- [x] 先在 `clients-api.spec.ts` 断言 URL 组装：默认请求不带 `state`，`{ state: 'all', limit: 20 }` 生成 `/clients/c-1/connections?state=all&limit=20`，且 `instanceId` 经 `encodeURIComponent`。
- [x] 在 `clients-view.spec.ts` 的租约 describe 旁新增用例：桩 `listClientConnections` 第一次返回 1 条 live、第二次（`state: 'all'`）返回 3 条含 2 条 expired；挂载后连接表只有 1 行，切换开关后为 3 行且出现 2 个 `已过期` 标签；`truncated=true` 时提示可见。
- [x] 运行 `cd web && npx vitest run src/tests/clients-api.spec.ts src/tests/clients-view.spec.ts`，预期红灯：开关不存在导致 `listClientConnections` 只被调用一次。
- [x] 实现，预期通过；`npx vitest run src/tests/i18n.spec.ts` 绿。

---

### 任务 6: 活文档与索引

**Files:**
- Modify: `docs/user-guide/server-admin.md`（「Client 运行观测与连接管理」段）
- Modify: `docs/operations/troubleshooting.md`
- Modify: `docs/superpowers/plans/README.md`、`docs/pull-requests/README.md`（生成）

**内容约束：**
- server-admin：写明“详情默认只列未过期租约；勾选‘显示历史租约’后显示最近 50 条（可到 200）；过期租约由 Server 每分钟回收，保留窗口 24 小时；名称/ID 与关闭语义不变”。
- troubleshooting：新增条目“客户端详情里大量 WS 连接显示已过期”→ 检查是否为本修复版本之前遗留的死行（升级后一轮清扫即收敛）、检查 `tunnelmesh_client_connection_lease_sweep_total{result="error"}` 是否非零、以及为何不应手工 `DELETE`（与 `server-admin.md` 现有“远端节点不可达时不要手工删除租约”一致）。
- 计划与 PR 记录属时点文档，只追加不改写既有内容；PR 描述写入 `docs/pull-requests/2026-11-05-client-connection-lease-cleanup.md`。
- 索引只能由 `python3 scripts/gen_doc_index.py` 生成，并确认 `docs/README.md` 无孤儿入口。

**TDD:** 文档无自动化测试；验证为 `git diff --check` 与 `grep -c "client-connection-lease-cleanup" docs/superpowers/plans/README.md`。

---

## 任务间依赖

- 任务 1 → 任务 2（sweeper 依赖 `DeleteExpired`）。
- 任务 1 → 任务 3、任务 4（接口签名先定，runtime 接线与 API 才有编译基础）。
- 任务 4 → 任务 5（前端参数语义必须与服务端默认值一致）。
- 任务 2 与任务 4 可并行，但都必须在任务 3 之前完成，否则接线代码引用不存在的构造函数。
- 任务 6 在实现全部通过后执行。

## 验证命令

```bash
go test ./internal/storage ./internal/server ./internal/observability -count=1
go test ./... -count=1
go test -race ./internal/storage ./internal/server -count=1
go vet ./...
gofmt -l internal/storage internal/server internal/observability
cd web && npm test -- --run && npm run build
./scripts/verify-web-embed.sh
git diff --check
```

MySQL contract 侧由既有 CI 的 mysql56 job 覆盖（`runClientRepositoryContract` 同时跑两种驱动），本地只需 SQLite 绿灯 + 静态检查。

## 回滚注意事项

- 数据是单向被删除的，回滚代码不会恢复已回收的租约行；但有效租约由客户端心跳在 90 秒内重建，历史租约本就是过期事实，因此“误删”的影响面限于排障证据。为此保留窗口默认取 24h，且回收只针对过期行，不触碰 `expires_at>now`。
- 分阶段回滚顺序（每步独立）：① 前端退回 `listClientConnections` 单参数调用；② API 退回 `state`/`limit` 解析（`live` 行为等价于旧版视图语义，但旧版无 `truncated`）；③ runtime 摘掉 sweeper 启动即可停止任何删除，这是最有效的止损开关。
- 若 sweeper 引发 DB 压力：只注释 `runtime.go` 的 sweeper 启动块并重启，5 分钟内可完成，无需数据补偿。
- 若首次上线时历史行极多：第一轮清扫分批 1000 行、每分钟一批，收敛速度可预期（例如 10 万行约 100 分钟）；不要在发布窗口手工 `DELETE`。
- Schema 无变更，因此不涉及 `schema_meta.version` 与升级/回滚 SQL。

## 执行记录（2026-11-05）

六个任务全部实现并通过验证：`go test ./... -count=1` 全绿（含 `internal/server` 67.0s、
`internal/storage` 3.6s）、`go test -race ./internal/storage ./internal/observability -count=1` 绿、
`go vet ./...` 无输出、`gofmt -l internal/` 无输出、`cd web && npm test -- --run` 66 个文件 507 项全绿、
`npm run build` 与 `./scripts/verify-web-embed.sh` 通过、`git diff --check` 干净。
`go test -race ./internal/server` 仍受该包既有的长测试拖住（与本次改动无关，CI 未启用该目标）。

与计划的偏差与精确替换：

- 任务 1：`DeleteExpired` 改为「先查 `connection_id`、再按主键 `IN (...)` 删除」的两步式，而不是计划里
  按 `r.driver` 分支。理由：`DELETE ... LIMIT` 是 MySQL 专有语法，两步式在两种驱动上共用一份 SQL，且首轮
  面对数万行时每批都可中断可续跑。`limit <= 0` 定义为「不限量」而非默认 50——默认值属于 API 层，仓库层不能
  静默丢行。contract 夹具补了显式 `heartbeat` 字段，带上限的历史断言期望 `contract-reap-live,contract-reap-boundary`。
- 任务 3：runtime 接线测试不用 fake 计数，而是以真实 SQLite 注册一条 10 天前的过期租约，断言 3 秒内被回收
  （`TestNewServerRuntimeReapsExpiredClientConnectionLeases`，`internal/server/runtime_test.go:1619`）。
  只有真实驱动能证明 SQL 可执行，单元级的「cutoff/批量/错误透出」已由任务 2 的 fake 覆盖。DSN 必须是
  `"file:" + t.TempDir() + "/runtime-lease-reap.sqlite"` 的拼接形式。启动块插在 `websshSweeper` 之前、
  停止块在其之前，`Stop` 用 `errors.Join` 汇总清扫错误。
- 任务 4：422 不返回计划里的 `data.error=invalid_state`，改用本仓库既有的列表参数惯用法
  `newListQueryError(...).write(w)` → `{"error","allowed"}`（与 `parseListSort`、`queryMulti` 同源）。
  `limit` 的取舍：非数字 → 422，数字但非正或超过 200 → 静默钳制（与 `pageArgs`、`queryLimit` 一致），
  计划原文只写「非法返回 422」。断言放在新增的 `TestClientAPIConnectionsDefaultToLiveLeases`
  （`internal/server/client_api_test.go:99`）而非塞进既有可观测性测试，403 断言并入
  `TestClientAPIRejectsOtherOwner`。详情视图与列表页批量读取传 `limit=0`：两处只要 live 行，
  而 live 行的规模由 90 秒 TTL 自身约束。`fakeClientConnectionRepo` 的 `ListByInstance(s)` 继续忽略参数，
  该 fake 只服务租约控制器单测，API 测试走真实 SQLite。OpenAPI 的 422 不引用 schema（文档中不存在列表错误
  schema），只在描述里写明响应含 `error` 与 `allowed`。
- 任务 5：`listClientConnections` 返回 `{connections, truncated}` 对象后，`showDetail` 的失败语义随之修正——
  连接读取失败不再把整个抽屉折叠成错误态，而是显示既有的「连接加载失败」提示条，metadata 与监听入口仍可读。
  「加载更多历史」按钮复用既有 `clients.loadMore` 文案，因此新增键仍是计划列出的两个。
  视图显式发送 `state` 与 `limit`（默认 `{state:'live',limit:50}`）而不是省略参数依赖服务端默认，因为
  「仅显示最近 N 条」提示与「加载更多历史」必须知道当前窗口；API 层「不传参数即默认」仍在
  `clients-api.spec.ts` 中单独断言。开关容器用 `div.connection-history` 而非 `label`，避免 label 把点击转发给
  `.el-switch__input` 造成二次切换。
- 任务 6：除计划要求的 `server-admin.md` 与 `troubleshooting.md`，另补 `docs/operations/observability.md`
  的「客户端连接租约回收」段落（counter 口径、为何不用分布式锁、日志字段、告警起点值），对齐 AGENTS.md §7。
- 时点记录：任务 1–3 的红灯输出产生于实现它们的当段会话，本次交接未带走原文，因此 PR 记录只引用可复核的
  部分（新增测试清单与当前绿灯）；任务 4、5 的红灯为本次实测，原文见
  `docs/pull-requests/2026-11-05-client-connection-lease-cleanup.md`。
