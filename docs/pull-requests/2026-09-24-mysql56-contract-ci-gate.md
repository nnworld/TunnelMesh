# MySQL 5.6 契约测试 CI 门禁

## Title

`test(storage): gate MySQL 5.6 SQL behind a CI contract job`

## Target branch

`main`

## Summary

把「MySQL 方言的真实 SQL 能否执行」从生产报错前置为 CI 门禁。CI 新增 `mysql56` job，起一个
`mysql:5.6`（项目声明的最低支持版本，生产实例 `5.6.51-91.0-log`）服务容器，用它执行与 SQLite
**完全相同**的 repository/registry 契约函数；同时补上此前没有任何方言覆盖的客户端观测契约
`runClientRepositoryContract`。实施计划见
`docs/superpowers/plans/2026-09-24-mysql56-contract-ci-gate.md`。

## 问题与根因

1. `internal/storage` 与 `internal/registry` 的 MySQL 契约测试由 `TUNNELMESH_TEST_MYSQL_DSN`
   网关控制，CI 中没有 MySQL，因此永远 skip。`v0014_to_v0015/mysql.sql` 在 5.6 上的
   `Error 1064`（PR #23）与 `connection_epoch` 的 `INTEGER`/`BIGINT` 漂移（PR #22）都只能由
   生产库暴露，`docs/operations/schema-upgrades.md` 记录了同一结论。
2. 即使打开该网关也不够：`runRepositoryContract` 从不访问 `ClientInstances()` 与
   `ClientConnections()`，客户端观测这条 SQL（presence `EXISTS`、`metadata='{}'` 时效轴、
   `Summarize` 关联聚合、带守卫的 `DELETE`、64 位 fencing token）在**两种方言下都没有契约级
   验证**，只有不连真实 MySQL 的 SQLite 单测。

## User impact

无运行时行为变更，用户不可见。影响的是发布质量：任何 MySQL 专属 SQL 语法/语义错误（含
5.6 与 8.x 的差异）在合并前就会被红灯拦下，而不是让控制台再次显示错误的客户端状态。

## API、Schema 与配置影响

- 无 API、无 Schema 变更：不新增列、不改 `migrations/ddl.sql`、不提升 `SchemaVersion`，
  因此无增量脚本、无升级/回滚章节。
- 配置无变更。CI 侧新增 `TUNNELMESH_TEST_MYSQL_DSN`（仅存在于 job 环境，不落库、不入日志）。
- `.github/workflows/ci.yml` 新增 job `mysql56`（context 名 `mysql56`）。既有 `build-test`、
  `packaging` 不变，且未把此前移除的 `go test ./...`、`-race`、`go vet` 加回任何 job。
- main 的分支保护 `required_status_checks.contexts` 追加 `mysql56`。

## 安全与授权影响

- 新增的只有测试与 CI 文件，未扩大任何 API 面。
- CI 凭据是本地服务容器的占位口令（`tunnelmesh/tunnelmesh`、root `ci-root`），不含真实
  DSN、Token 或生产地址；生产库本次仅做只读核验（`SELECT`/`EXPLAIN`/`PREPARE`），未执行任何
  写入。
- 契约断言仍只使用 allowlist 常量数据，不写入 metadata 真实值或凭据。

## 测试证据

红灯（先失败）：

- `go test ./internal/storage -run TestSQLiteRepositoryContract -count=1` →
  `undefined: runClientRepositoryContract`（编译失败）。

绿灯（本地，SQLite 方言）：

- `go test ./internal/storage -run 'TestSQLiteRepositoryContract|TestClient' -count=1` → ok。
  过程中暴露并修正了一处断言口径：`expired` 分组的 `ActiveStreams` 只应统计该分组行的租约（3 而非 4）。

### CI 证据（真实 MySQL 5.6，`mysql56` job）

门禁共跑 3 轮，前两轮暴露的都是**从未真正执行过**的 MySQL 测试自身缺陷，正是本 PR 要消除的盲区：

| 轮次 | 结果 | 暴露的问题 | 处理 |
| --- | --- | --- | --- |
| 1 | `internal/registry` 契约 pass；`internal/storage` 3 个 FAIL | `TestMySQLRepositoryContract` 与 `TestMySQLV8ToV9…` 报 `Error 1062 Duplicate entry '1'`：`go test` 并行跑两个包，二者对同一个库同时 auto-init，抢同一行 `schema_meta(id=1)`；DDL 重放用裸 `ExecContext`/`strings.Split`，不容重复且 naive 切分会重新踩注释里的分号；`TestMySQLAutoInitDisabled…` 把版本钉在 3，被版本守卫先拒 | CI 步骤加 `-p 1`；改用 `applySchemaStatements`（与生产同一条容错、可识别注释的路径）；版本参数改 `SchemaVersion`，与 SQLite 同名测试对齐 |
| 2 | 仅剩 `TestMySQLV8ToV9…` FAIL | cleanup 复用 body 已 `Close()` 的句柄 → `sql: database is closed`；此前不可见，因为同一测试更早就在 DDL 重放上失败 | cleanup 自己 `sql.Open` 一个句柄执行恢复 |
| 3 | pass 1m29s；`build-test` pass 1m2s；`packaging` pass 14s | — | — |

第 3 轮 `TestMySQLRepositoryContract` 不再 skip，`runRepositoryContract` → `runClientRepositoryContract`
首次在 `mysql:5.6`（`mysqladmin ping` 健康检查通过）上执行，与 SQLite 共用同一套断言全部通过：
`FOR UPDATE` upsert 与主键复用、`capabilities` 归一化、presence `EXISTS`/`NOT EXISTS`、
`INSTR` 的 agent 精确匹配、`Summarize` 关联聚合、带 `NOT EXISTS` 守卫的两条 `DELETE`
（未出现 MySQL 1093）、64 位 `connection_epoch`、复合游标分页。`TestMySQLIdentityRepositoryContract`、
`TestMySQLAutoInit*`、`TestMySQLV8ToV9AuthorizationRevisionMigration` 与 registry 契约同轮通过。

本地全量门禁（合并前执行）：

- `go vet ./...` → 通过。
- `go test ./... -count=1` → 全部 ok。
- `go test -race ./... -count=1 -timeout 30m` → 全部 ok（`internal/server` 467s）。
- `git diff --check` → 无空白错误。
- 前端未变更，`web` 门禁与 `scripts/verify-web-embed.sh` 不在影响面内（`build-test` 仍会在 CI 执行）。

## 发布步骤

1. 合并后确认 `mysql56` 出现在 main 的必需检查里且为绿。
2. 无需重启、无需迁移、无需配置分发。

## 回滚步骤

- 代码回滚：`git revert` 合并提交（无 Schema、无生产代码路径）。
- 若需立刻恢复合并能力而不回滚：从 `required_status_checks.contexts` 摘掉 `mysql56`，
  保留 job 继续产出红灯以便排查。5.6 下限验证不得降级为 8.x。

## Reviewer 关注点

- `internal/storage/client_contract_test.go` 的断言是否只依赖契约自己写入的行：全局写
  `PurgeUnreported`/`MarkExpired` 只断言 `>= 1`，其余按主键与 owner 过滤校验。
- fixture 时间用真实 `now` 并截断到秒：`List`/`Summarize` 内部使用自己的时钟判断 presence，
  且游标依赖 `updated_at` 的字典序比较。
- DSN 必须带 `multiStatements=true`，数据库必须为空（`OpenMySQL` 默认 `auto-init=true`）。
- 已知边界：CI 服务容器是服务端默认 charset（latin1），生产是 `utf8mb4_general_ci`；所有被索引列
  为 `VARBINARY` 或 `VARCHAR(191)`，两种 charset 都满足 InnoDB 767-byte 前缀限制，因此 CI 只会更
  宽松、不会漏判。charset 端到端矩阵已作为 deferred 项记入 `docs/operations/completeness-checklist.md`。

## 集成状态

- 分支：`codex/mysql56-contract-ci-gate`。
- 检查：`build-test`、`packaging`、`mysql56` 在第 3 轮全部通过；`mysql56` 已登记为 main 的必需状态检查。
  远端 PR：https://github.com/nnworld/TunnelMesh/pull/27
