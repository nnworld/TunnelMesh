# MySQL 5.6 契约测试 CI 门禁实施计划

状态：已确认并完成实施，实现记录见 `docs/pull-requests/2026-09-24-mysql56-contract-ci-gate.md`。

日期：2026-09-24

## 1. 目标

让「MySQL 方言的真实 SQL 可执行性」成为 CI 门禁，而不是发布后靠生产库报错发现：CI 中固定起一个 `mysql:5.6` 服务容器，跑与 SQLite 完全相同的 Repository/registry 契约测试，并把该检查登记为 main 分支的必需状态检查。

## 2. 为什么必须做

- 生产库是 `MySQL 5.6.51-91.0-log`（Percona），是项目声明的下限版本。
- `v0014_to_v0015/mysql.sql` 在 5.6 上因语句切分把注释里的 `;` 当成语句分隔而报 `Error 1064`（已由 #23 修复解析器）。同一个迁移的 `INTEGER` → `BIGINT` 类型漂移（#22）也只能在生产上暴露。
- 根因相同：`internal/storage` 与 `internal/registry` 的 MySQL 契约测试由 `TUNNELMESH_TEST_MYSQL_DSN` 网关控制，CI 里没有 MySQL，因此**永远 skip**。`docs/operations/schema-upgrades.md` 与 `docs/operations/completeness-checklist.md` 都记录了这条缺口。
- 只补 CI 还不够：`runRepositoryContract` 从不触碰 `ClientInstances()`/`ClientConnections()`，即使把 MySQL 打开，客户端观测这条 SQL（`EXISTS`/`NOT EXISTS`、`INSTR`、关联子查询聚合、带守卫的 `DELETE`）依然没有任何一方言验证。

## 3. 非目标

- 不做 Schema 变更：不新增列、不改 `migrations/ddl.sql`、不提升 `SchemaVersion`，因此无增量脚本、无升级文档章节。
- 不改动生产代码路径（`internal/storage/*.go` 的实现文件、`internal/server`）。本次只加测试与 CI。
- 不把用户此前明确要求移除的 `go test ./...`、`go test -race ./...`、`go vet ./...` 重新加回 `build-test` job。MySQL job 只跑 MySQL 相关测试，是新增门禁而非恢复旧门禁。
- 不引入 etcd 集成测试、不做 charset 端到端验证（见第 8 节已知边界）。

## 4. 架构决策

- **D1 复用同一契约函数，而不是复制一份 MySQL 专用测试**：新增 `runClientRepositoryContract(t, db *DB)` 并由 `runRepositoryContract` 调用，与既有 `runIdentityRepositoryContract` 同构。SQLite 与 MySQL 执行逐字节相同的断言，方言差异无处藏身（对扩展开放、对修改关闭）。
- **D2 断言按 owner 作用域收敛**：契约自带 `contract-client-owner`，所有计数只覆盖自己写入的行。`PurgeUnreported`/`MarkExpired` 是全局写，因此只断言 `>= 1` 并逐个按主键校验自己那行的存在性，不断言全局行数，避免与同库其他测试互相污染。
- **D3 独立 job 而非塞进 `build-test`**：`build-test` 只做前端与嵌入产物校验，保持轻量；MySQL job 只 checkout + Go + 两个包，失败信号清晰（单一职责），也不拖慢既有检查。
- **D4 版本选 `mysql:5.6` 而非 `mysql:8.4`**：CI 必须验证**下限**。8.x 会放行 5.6 拒绝的语法（`utf8mb4_0900_*`、CTE、窗口函数、JSON 函数、`CREATE INDEX IF NOT EXISTS`），而 5.6 通过即代表向上兼容。`docker-compose.cluster.yml` 的 `mysql:8.4` 继续作为开发环境默认，两者不冲突。
- **D5 时间基准用真实 now 而非冻结日期**：`List`/`Summarize` 内部用自己的时钟判断 presence，fixture 必须围绕 `time.Now()`；同时把时间戳截断到秒，保证 RFC3339Nano 串等宽，字典序比较（游标分页依赖它）才等价于时间序。
- **D6 门禁登记为必需检查**：只有检查存在但仍非必需，仍可被绕过合并。绿色后把 context `mysql56` 加入 `required_status_checks.contexts`。

## 5. 技术栈与规格引用

- Go 1.26 `database/sql`；驱动 `github.com/go-sql-driver/mysql`、`modernc.org/sqlite`。
- GitHub Actions service containers（`services.mysql`，`mysql:5.6`，`ports: 3306`，`--health-cmd mysqladmin ping`）。
- 被守护的接口：`ClientInstanceRepository`（`internal/storage/repository.go:230`）、`ClientConnectionRepository`（`internal/storage/repository.go:248`）。
- 权威 Schema：`migrations/ddl.sql`（空库 auto-init 全量脚本），`migrations/incremental/v00NN_to_v00NN/mysql.sql`（升级路径）。
- 兼容性下限依据：`docs/operations/schema-upgrades.md`、`docs/operations/connection-pool.md`（InnoDB 767-byte 前缀）。

## 6. 文件清单

| 文件 | 变更 |
| --- | --- |
| `internal/storage/client_contract_test.go` | 新增：`runClientRepositoryContract` 与 `putContractClientInstance`/`putContractClientLease`/`assertClientContractSummary`/`assertContractClientRows`/`assertContractClientPresent`/`walkContractClientInstances` 辅助函数 |
| `internal/storage/storage_contract_test.go` | `runRepositoryContract` 中调用 `runClientRepositoryContract(t, db)` |
| `.github/workflows/ci.yml` | 新增 `mysql56` job（`mysql:5.6` service + `TUNNELMESH_TEST_MYSQL_DSN` + `go test ./internal/storage ./internal/registry -count=1 -run MySQL`） |
| `docs/development/testing.md` | 「MySQL 与 etcd 相关验证」改写为 CI 门禁事实，并补本地复跑命令 |
| `docs/operations/completeness-checklist.md` | 勾掉真实 MySQL contract 项，补剩余边界（charset、8.4） |
| `docs/operations/observability.md` | 更新末段「不能标记为 MySQL 已验证」为 CI 已在 5.6 上验证 |
| `docs/operations/schema-upgrades.md` | 保留「当初为何存活」的历史叙述，补充门禁已建立 |
| `docs/superpowers/plans/2026-09-24-mysql56-contract-ci-gate.md` | 本文件 |
| `docs/pull-requests/2026-09-24-mysql56-contract-ci-gate.md` | PR 记录 |
| 四份生成的 `docs/**/README.md` | 由 `python3 scripts/gen_doc_index.py` 重新生成，不手工编辑 |

## 7. 任务间接口

- `runClientRepositoryContract(t *testing.T, db *storage.DB)`：无返回值，失败即 `t.Fatal`。调用方只提供已 auto-init 的空库，函数自带全部数据，不依赖调用方的 seed（与 `runIdentityRepositoryContract` 的约定一致）。
- `putContractClientInstance(t, db, instanceID, metadata string, stale bool, at, expiresAt time.Time) string`：返回生成的主键，供后续按 id 断言。
- `putContractClientLease(t, db, connectionID, clientInstanceID string, activeStreams, epoch int64, expiresAt, at time.Time) ClientConnectionLease`。
- `assertClientContractSummary(t, db, metadataState string, want ClientInstanceSummary)`、`assertContractClientRows(t, db, filter ClientInstanceFilter, want string)`（want 为排序后 instance_id 以 `,` 连接）。
- CI 侧契约：`TUNNELMESH_TEST_MYSQL_DSN` 必须含 `multiStatements=true`（迁移测试用一次 `ExecContext` 执行整段 DDL），且数据库必须为空（`OpenMySQL` 默认 auto-init=true）。

## 8. 覆盖范围（必须被真实 MySQL 执行的 SQL）

1. `Upsert` 的 `SELECT ... FOR UPDATE` 存在性判定与 INSERT/UPDATE 分支；`capabilities` 的 `"null"`/`""` → `"[]"` 归一化，且主键必须复用（不能插出第二条身份行）。
2. `clientInstanceConditions` 全部分支：`EXISTS`/`NOT EXISTS` presence、`metadata='{}'` / `metadata<>'{}'` / `stale` 组合、`INSTR(metadata, ?)>0` 的 agent 精确匹配（`agent-1` 不得命中 `agent-12`）、`Keyword` 的三列 `INSTR`。
3. `Summarize` 两条聚合 SELECT：含关联子查询 `CASE WHEN EXISTS(...)` 与 `client_instance_id IN (SELECT id FROM ...)`，并与 `List` 同源。
4. `MarkStale`/`TouchInstance` 的时效轴往返，且 presence 不受影响（两轴正交）。
5. 租约全链路：`Register`（含 `FOR UPDATE`）、`Get` 的 64 位 `connection_epoch` 原样回读、`Renew`、`UpdateStats`（改后聚合立即反映活跃流）、错误 epoch 的 `Release` 必须 `sql.ErrNoRows`、正确 epoch 的 `Release` 成功、`ListByInstance` 收敛为空。
6. `DeleteUnreported`（`id=? AND metadata='{}'`）与 `PurgeUnreported`（`NOT EXISTS` 相关子查询 + TTL 守卫）：MySQL 1093「Can't specify target table for update in FROM clause」正是这类语句的经典风险。
7. `MarkExpired` 的批量 UPDATE。
8. 复合游标 `(updated_at, id)` 分页：整群 walk，断言终止且不重复。
9. registry 侧 `TestMySQLDatabaseRegistryContract` 与 `TestMySQLRepositoryContract`/`TestMySQLIdentityRepositoryContract`/`TestMySQLAutoInit*`/`TestMySQLV8ToV9AuthorizationRevisionMigration` 一起执行，覆盖全量 DDL 与 v8→v9 增量在 5.6 上真实可执行。

已知边界（明确不宣称已验证）：CI 服务容器使用服务端默认字符集（latin1），生产是 `utf8mb4_general_ci`。差异只可能让 CI 比生产更宽松（键长度、大小写/重音折叠），不会反向漏判语法问题；所有被索引列均为 `VARBINARY` 或 `VARCHAR(191)`，两种 charset 下都在 767-byte 限制内。

## 9. TDD 步骤与预期结果

1. **红灯**：在 `runRepositoryContract` 中调用尚不存在的 `runClientRepositoryContract` → `internal/storage` 编译失败（`undefined: runClientRepositoryContract`）。
2. **最小实现**：新增 `internal/storage/client_contract_test.go`，实现契约与辅助函数。
3. **预期通过**：`go test ./internal/storage -run 'TestSQLiteRepositoryContract|TestClient' -count=1` 通过；计数不匹配时按第 8 节的真实语义修正期望值（例如 `expired` 分组内 `ActiveStreams` 只统计该分组行的租约）。
4. **门禁**：推送后观察 PR 的 `mysql56` 检查；若 5.6 报出语法/语义错误，按「先修 SQL/迁移，不改断言口径」处理，并把结论写进 PR 记录。
5. **登记**：`mysql56` 变绿后加入 main 的 `required_status_checks.contexts`。
6. **回归**：`go test ./... -count=1`、`go test -race ./... -timeout 30m`、`go vet ./...`、`git diff --check`。本次不改前端，因此 `web` 门禁与 `verify-web-embed.sh` 不在变更影响面内（`build-test` 仍会在 CI 跑）。

## 10. 回滚注意事项

- 全部变更是测试 + CI + 文档：无 Schema、无生产代码路径，回滚即 `git revert` 合并提交，或从 `required_status_checks.contexts` 中摘掉 `mysql56` 并回退 workflow。
- 不存在数据回填或不可逆 DDL；已升级到 v15 的库不受影响。
- 若 `mysql:5.6` 镜像在 runner 上拉取失败导致 CI 红灯，摘除该 job 属于止损，不作为长期方案：下限验证不能因此降级为 8.x。
