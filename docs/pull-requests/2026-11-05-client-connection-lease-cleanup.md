# 客户端连接租约过期回收与历史读取上限

- 标题：Client 连接租约按过期时间回收，详情默认只显示未过期租约
- 目标分支：`main`
- 相关记录：[实施计划](../superpowers/plans/2026-11-05-client-connection-lease-cleanup.md)

## 摘要

起因是一次运维提问：「客户端详情中 ws，是不是有很多已经下线的」。答案是**是**，而且行数只增不减。
根因链条（每条都在改动前的代码里可验证）：租约只有两条删除路径——客户端正常关闭 WebSocket 时按
`connection_id + connection_epoch` 释放，以及管理员显式「关闭连接」；`kill -9`、宿主掉电、网络硬中断
都走不到前者，而 `ws_client.go` 对释放错误本就是显式忽略的。全仓没有一句按 `expires_at` 删除的语句，
`idx_client_connection_leases_expires` 建了但没有消费者；读取侧 `ListByInstance` 既不过滤也没有行数
上限，于是崩溃过的客户端把死行永久留在表里，抽屉把它们全部列成「已过期」的连接。

本次按三层修复，每层可独立验证：

1. 存储层：`clientConnectionRepo` 新增 `DeleteExpired(ctx, at, limit)`（先查 id、再按主键 `IN (...)`
   删除，命中既有 `expires_at` 索引），`ListByInstance`/`ListByInstances` 增加 `liveOnly` 与 `limit`
   参数；`liveOnly` 的判定收敛到唯一的 `clientConnectionLiveCondition`，「到界即过期」的 `<=` 取法与既有
   `MarkExpired`、`PurgeUnreported` 一致。
2. 服务层：新增 `internal/server/client_connection_sweeper.go`，每 1 分钟回收「过期超过 24 小时」的租约，
   每轮最多 1000 行，启动时先立即跑一轮；与既有 sweeper 一起在 `internal/server/runtime.go` 启停，错误经
   `errors.Join` 透出，并新增 `tunnelmesh_client_connection_lease_sweep_total{result,error_class}`。
   删除语句以时间戳为边界且幂等，因此集群每个节点各跑一份是安全的，不引入分布式锁。
3. 接口与前端：`GET /api/v1/clients/{id}/connections` 支持 `state=live|all`（默认 `live`）与 `limit`
   （默认 50、上限 200），响应新增 `truncated`（多取一行判定，不额外发 `COUNT`）；详情视图与列表页批量
   读取改为只取 live 行。后台抽屉加「显示历史租约」开关与被截断时的「加载更多历史」。

无 DDL 变更、无 `SchemaVersion` 变更、无新增配置项：回收的间隔、保留窗口与批量都是编译期常量，与该文件里
其他不可配置的清扫器常量保持一致。

## 用户影响

- 客户端详情的「WS 连接」默认只列未过期租约；意外退出的客户端不再堆积「已过期」行。
- 需要排障时勾选「显示历史租约」，可见 24 小时保留窗口内的过期行，默认 50 条、可翻倍到 200 条，超出时列表
  上方提示「仅显示最近 N 条租约历史」。
- 名称、租约状态列（活跃/已过期）、过期行活跃流显示 `—` 的规则与「关闭连接」入口的可见性全部保持原样，
  历史视图下这些标记正是它的用途。
- Server 启动后第一轮清扫即开始收敛遗留死行，按每分钟 1000 行的速度下降（10 万行约 100 分钟），无需手工
  `DELETE`。
- 统计卡片与 `activeConnections`/`activeStreams` 的口径不变（本来就只累加未过期租约），但读取量从「该页所有
  Client 的全部租约历史」降为「全部有效租约」。
- 连接读取失败不再把整个详情抽屉折叠成错误态，而是显示既有的「连接加载失败」提示条，metadata 与本地监听
  入口仍可读。

## API / Schema / 配置影响

- `GET /api/v1/clients/{clientInstanceId}/connections` 新增 `state`、`limit` 查询参数与 `data.truncated`
  字段；`docs/api/openapi.yaml` 同步。未传参数的旧调用不会得到更多行，而是得到更少的行（这正是本次的修复
  目标），响应字段向后兼容。
- 非法 `state` 与无法解析的 `limit` 返回 422，响应体沿用本仓库列表参数惯用法 `{"error":..., "allowed":[...]}`；
  `limit` 为数字但越界时按既有 `pageArgs`/`queryLimit` 的做法静默钳制。
- `internal/storage.ClientConnectionRepository` 接口新增 `DeleteExpired`，`ListByInstance`/`ListByInstances`
  签名增加 `liveOnly bool, limit int`。生产调用点只有 `internal/server/client_api.go` 与 runtime 接线。
- 新增指标 `tunnelmesh_client_connection_lease_sweep_total{result,error_class}`；`error_class` 复用既有归一化
  取值，不新增标签基数。
- 新增 i18n 键：`clients.showExpiredLeases`、`clients.connectionsTruncated`（带 `{count}` 具名插值），
  zh/en 两份同时写入；「加载更多历史」按钮复用既有 `clients.loadMore`，不新增第三个键。
- 无 DDL、无 `schema_meta.version` 变更，因此不涉及 `migrations/` 与升级文档动作；
  `idx_client_connection_leases_expires` 首次有了消费者。
- 活文档同步：`docs/user-guide/server-admin.md`（Client 观测段）、`docs/operations/troubleshooting.md`（新增
  「客户端详情里大量 WS 连接显示已过期」）、`docs/operations/observability.md`（新增「客户端连接租约回收」）。

## 安全与授权影响

- 权限判定不变且先于响应组装：仍由 `clientInstanceReadable` 决定可读性，`state=all` 不放宽可见范围，普通用户
  看不到他人的租约历史（新增 403 断言覆盖）。
- 清扫只按 `expires_at` 时间边界删除已过期的行，不读取也不删除任何未过期租约，也不触碰
  `client_instance_metadata` 里代表真实安装身份的记录。
- 日志与指标不含 connection ID、client instance ID、Token 或会话字节；清扫是后台聚合动作，因此不写审计日志
  （审计记录的是「谁做了什么」，这里没有操作者）。
- `limit` 有服务端上限 200，且判定截断靠多取一行而不是无限翻页，历史视图不能用于批量导出全部租约记录。

## 测试证据

红灯（本次实测输出，实现前）：

```text
$ go test ./internal/server -run TestClientAPIConnectionsDefaultToLiveLeases -count=1
--- FAIL: TestClientAPIConnectionsDefaultToLiveLeases (0.42s)
    client_api_test.go:109: default connections = []string{"client_connection_1", "client_connection_2", "client_connection_3"}, want only the live lease

$ cd web && npx vitest run src/tests/clients-api.spec.ts src/tests/clients-view.spec.ts
 Test Files  2 failed (2)
      Tests  9 failed | 14 passed (23)
     Errors  54 errors   # TypeError: rows is not iterable：el-table 收到 {connections,truncated} 而不是数组
   失败项包含 clients-api.spec.ts 的「asks for lease history with state and limit」与
   clients-view.spec.ts 的「lists live leases by default and history only when asked」
```

任务 1–3 的红灯输出产生于实现它们的当段会话，未随交接带走原文，因此这里只列可复核的部分（新增测试清单与
当前绿灯）：

- `internal/storage/client_repository_test.go`：`TestClientConnectionDeleteExpiredRemovesOnlyLapsedBounded`
  （只删过期、边界取 `<=`、`limit` 生效、二次调用只删剩下 1 行、有效行不动）与
  `TestClientConnectionListByInstanceLiveOnlyAndPerInstanceLimit`。
- `internal/storage/client_contract_test.go`：同一夹具在 MySQL/SQLite 两种驱动上跑
  `ListByInstance(..., true, 0)`、`(..., false, 2)` 与 `DeleteExpired` 的收敛。
- `internal/server/client_connection_sweeper_test.go`：5 项（cutoff 恰为 `now-24h`、默认值兜底、仓库错误透出且
  counter 记 `result=error`、`SetGauges(nil)` 不 panic、`Run` 在首个 tick 之前先扫一轮）。
- `internal/observability/metrics_lease_sweep_test.go`：2 项（counter 名称与标签集合、经 `promhttp` 的取值）。
- `internal/server/runtime_test.go:1619`：`TestNewServerRuntimeReapsExpiredClientConnectionLeases`，真实 SQLite
  里一条 10 天前的过期租约在 3 秒内被清扫掉。

绿灯与门禁：

- `go test ./... -count=1`：全部包 `ok`（`internal/server` 67.0s、`internal/storage` 3.6s、
  `internal/observability` 0.7s）。
- `go test -race ./internal/storage ./internal/observability -count=1`：`ok`。
- `go vet ./...` 无输出，`gofmt -l internal/` 无输出，`git diff --check` 干净。
- `cd web && npm test -- --run`：66 个文件 507 项通过（在 503 项基线上新增 4 项：API 2 项、视图 2 项）。
- `npm run build` 通过，`./scripts/verify-web-embed.sh` 报告 `web/dist` 与 `internal/server/web_dist` 一致。
- `go test -race ./internal/server` 仍受该包既有的长测试拖住，与本次改动无关（CI 未启用该目标）。
- 行为断言覆盖：默认只返回未过期行、`state=all` 返回三行且 `truncated=false`、`state=all&limit=1` 只剩最新一行
  且 `truncated=true`、`state=bogus` 与 `limit=abc` 返回 422、非属主 403；抽屉默认一行且请求为
  `{state:'live',limit:50}`，切换开关后三行含两个「已过期」标签并以 `{state:'all',limit:50}` 重新请求，
  截断提示可见且「加载更多历史」把窗口翻倍为 100。

## 发布步骤

1. 随 Server 二进制/镜像发布；无 DB 迁移、无配置项、无需重启 Agent 与 Client。
2. 升级后先看指标再看表：`sum(increase(tunnelmesh_client_connection_lease_sweep_total[15m]))` 应有非零增量，
   `result="error"` 分支为 0；`SELECT COUNT(*) FROM client_connection_leases` 逐轮下降。
3. 打开一个曾经崩溃过的客户端详情，确认默认列表只剩未过期连接；勾选「显示历史租约」确认当天历史仍可读。
4. 遗留行数极多时不要干预：首轮之后每分钟收敛 1000 行，发布窗口内不安排手工 `DELETE`。
5. 集群滚动升级期间新旧 Server 可同时访问数据库：旧节点不删除、新节点按时间边界删除，语义无冲突。

## 回滚步骤

- 数据是单向删除的，回滚代码不会恢复已回收的租约行；有效租约由客户端心跳在 90 秒内重建，历史行本就是过期
  事实，因此影响面限于排障证据。保留窗口取 24 小时正是为此。
- 分阶段止损（每步独立，最快 5 分钟内完成）：
  ① 摘掉 `internal/server/runtime.go` 的 sweeper 启动块并重启——立刻停止一切删除，无需数据补偿；
  ② API 退回不解析 `state`/`limit`（`live` 视图等价于旧版语义，只是不再有 `truncated`）；
  ③ 前端退回 `listClientConnections(id)` 单参数调用。
- 完整回滚：`git revert` 本次提交，重新 `cd web && npm run build` 与 `./scripts/verify-web-embed.sh`，替换
  `web_dist` 并重启 Server。
- 无 Schema 变更，因此不涉及 `schema_meta.version`、升级 SQL 与反向 DDL。

## Reviewer 关注点

- `DeleteExpired` 用「先查 id、再按主键 `IN (...)` 删除」而不是 `DELETE ... LIMIT`：后者是 MySQL 专有语法，
  前者两种驱动共用一份 SQL 且首轮可中断可续跑。代价是两次往返与一次 id 集合的物化，请确认这个取舍。
- 每节点各跑一份清扫器、不引入分布式锁：与 `audit_retention_sweeper.go` 同一论证（时间边界的幂等 `DELETE`，
  重复一轮只删 0 行）。请确认在超大表上「N 个节点各做一次索引扫描」的成本仍可接受。
- `Run` 在仓库返回错误时结束循环（照 `client_metadata_sweeper.go` 的形态，计划的 TDD 条目也这么写），因此启动
  初期一次数据库抖动会让本进程的回收停到重启为止，信号只有 `result="error"` 的一次计数与 `Stop()` 里的错误。
  仓库里另一个先例是 `audit_retention_sweeper.go` 的 log-and-continue。若这个取舍不可接受，应整体改成后者，
  而不是在清扫器里自己加退避重试。
- 保留窗口 24 小时与 TTL 90 秒是两个独立数字，回收不等于「过期即删」：排障需要当天历史，读取侧已限量。
- 列表页 `ListByInstances` 的 `limit` 是**每实例**上限，在 Go 侧用计数器实现（无可移植的 group-limit SQL）；
  API 层对列表页传 `0`（不限量），因为那里只要 live 行。
- `truncated` 由「多取一行」判定而不是 `COUNT(*)`：它表示「还有更多行」而非精确总数，请确认这一近似足够。
- `state` 非法走 422 + `allowed`、`limit` 越界走静默钳制：两类参数各自沿用本仓库既有先例，刻意没有统一。
- 前端 `showDetail` 的失败语义变化（连接读取失败不再折叠整个抽屉）是顺带修复，不在计划正文里，请确认。
- 清扫不写审计日志：如果将来要求「删除记录可追溯到操作者」，需要先定义系统 Actor 的语义，而不是给它加标签。

## 集成状态

- 实施计划已获用户确认后执行，6 个任务全部完成，勾选与偏差记录已补记在计划末尾。
- `go test ./... -count=1`、`go vet ./...`、`gofmt -l internal/`、`git diff --check`、`npm test -- --run`、
  `npm run build`、`./scripts/verify-web-embed.sh` 全部通过。
- 文档索引已由 `python3 scripts/gen_doc_index.py` 重新生成。
- 尚未 commit、push 或创建远端 PR，等待用户授权。
