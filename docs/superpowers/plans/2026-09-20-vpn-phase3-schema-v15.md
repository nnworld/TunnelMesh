# VPN 网关 阶段 3：Schema v15 与 VPN Repository Implementation Plan

- 日期：2026-09-20
- 状态：**待用户确认**。按 `AGENTS.md`「Plan 与 PR 要求」，未获明确确认前不得开始实现、不得修改生产代码。
- 规格：[内嵌 VPN 网关（WireGuard）设计](../specs/2026-09-19-embedded-vpn-gateway-design.md) §7 数据模型（Schema v15）、§5.3 控制面与持久化、§14 测试策略、§15 阶段 3
- 决策载体：[ADR 0002](../../architecture/adr/0002-public-ingress-and-embedded-vpn.md)（`Status: Accepted`）
- 前置阶段：[阶段 1：约束反转与 ADR 0002](2026-09-20-vpn-phase1-constraint-reversal.md)（已完成，守卫测试绿灯 0）
- 分支：`codex/vpn-phase3-schema-v15`，stacked 在 `codex/vpn-phase1-constraint-reversal`（`4d443a6`）之上
- 交付物：Schema v15（全量 DDL + 双方言增量）、`VPNPeer`/`VPNIPLease` 存储模型、两个 Repository 实现与装配、
  双方言契约测与迁移测、`docs/operations/schema-upgrades.md` 的 v14→v15 升级与回滚章节

## 目标

把规格 §7 的数据模型落成可运行、可迁移、可测试的存储层，使阶段 4（`internal/vpn/` 纯逻辑与管理 API）
可以直接依赖 Repository 接口而不触碰 SQL。本阶段是**存储层专属**：

1. `SchemaVersion` 14 → 15，新增 `vpn_peers` 与 `vpn_ip_leases` 两张表。
2. `migrations/ddl.sql`（全量权威）与 `migrations/incremental/v0014_to_v0015/{mysql,sqlite}.sql`（升级路径）同步落地。
3. `VPNPeerRepository` 与 `VPNIPLeaseRepository` 接口 + 实现 + `DB` 装配。
4. 契约测（SQLite 直跑、MySQL 由 `TUNNELMESH_TEST_MYSQL_DSN` 门控跑同一函数）、迁移测、方言静态断言测。
5. 升级文档：预计锁表时间、容量影响、灰度顺序、5 分钟止损方案、最低源版本与回滚路径。

非目标（本阶段明确不做，属阶段 4-8）：

- 不建 `internal/vpn/`（密钥、IP 池纯函数、逐包策略、ini 渲染、错误码）。
- 不建 `internal/server/vpn_peer_service.go`、`vpn_peer_api.go`，不改 `internal/server/api.go:331` 的分派 switch。
- 不改 `docs/api/openapi.yaml`（阶段 4 随接口一起交付）。
- 不新增任何配置项：`server.vpn.*` 与 `TUNNELMESH_VPN_NODE_PRIVATE_KEY` 属阶段 4/6；本阶段不读任何 VPN 配置。
- 不动数据面（`vpn_device.go`/`vpn_stack.go`/`vpn_packet.go`/`vpn_flows.go`/`vpn_icmp.go`/`vpn_lifecycle.go`）、
  Agent ICMP、前端、Grafana、发布脚本。
- **不新增 Go 依赖**：`go.mod`/`go.sum` 零改动，因此本阶段不需要抬 `Dockerfile` 的 `ARG GO_VERSION`
  （那是阶段 6 引入 gVisor 时的事）。
- 不建 `vpn_flows` 表：运行时流状态不入库、不进 Prometheus 高基数标签（规格 §7.1 明确「不建表」）。

## 技术栈

Go 标准库 `database/sql` + 既有驱动（`modernc.org/sqlite`、`github.com/go-sql-driver/mysql`），
复用 `internal/storage` 现有 helper（`stamp`、`tm`、`boolInt`、`nullableTime`、`pageArgs`、
`encodeCursor`/`decodeCursor`、`checkAffected`、`isDuplicateError`、`parseTime`）。
迁移脚本为纯 SQL，经 `migrations/embed.go` 的 `//go:embed` 注入。不引入任何新模块。

## Global Constraints

1. **每次 Schema 变更四件套同步**（`AGENTS.md:51`）：全量 DDL、增量 DDL、`SchemaVersion`、迁移测试与升级文档
   必须在同一批提交内完成；禁止只改 `migrations/ddl.sql` 或只抬版本号。
2. **已发布增量脚本不可变**（`AGENTS.md:53`）：`v0005_to_v0006` … `v0013_to_v0014` 一个字符都不改。
3. **只维护相邻版本**（`AGENTS.md:54`）：本阶段只新增 `v0014_to_v0015`；最低可升级源版本是 v14，
   跨版本升级由 `initializeSchema` 的 `for version < SchemaVersion` 循环逐个执行，升级文档必须写明。
4. **expand-only，向后兼容**（`AGENTS.md:66` MINOR 规则）：只新增表与索引，不 `ALTER` 既有表、不 `DROP`、
   不改列类型，保证滚动升级期间新旧 Server 可同时访问数据库。
5. **迁移可安全重试**（`AGENTS.md:58`）：脚本用 `CREATE TABLE IF NOT EXISTS`，SQLite 索引用
   `CREATE INDEX IF NOT EXISTS`，MySQL 索引用裸 `CREATE INDEX`（MySQL 不支持索引级 `IF NOT EXISTS`，
   重复对象由 `applySchemaStatements(ctx, db, script, version >= 6)` 的 `isDuplicateError` 容忍）。
   MySQL DDL 隐式提交，脚本不得依赖整体事务回滚；恢复步骤写进升级文档。
   **脚本必须以 `;` 结尾，且任何两个 `;` 之间不得留下只含注释的片段**：
   `applySchemaStatements`（`internal/storage/db.go:437`）按 `;` 切分后逐条 `ExecContext`，
   纯注释片段会被当成一条空语句送给数据库。已实测现有 19 份脚本全部满足该规则，本阶段两份新脚本同样必须满足，
   并由 Task 1 的静态断言测守护。
6. **唯一性由数据库约束保证**（`AGENTS.md:75`）：`public_key` 全局唯一、`(node_id, vpn_ip)` 唯一、
   `(node_id, subnet)` 唯一，全部落 DB 约束，不依赖进程内锁；租约接管用 epoch fencing + 单赢家 CAS。
7. **分页内权限过滤**（`AGENTS.md:78`）：`List` 的 owner 条件必须进 `WHERE`，禁止先全局分页再丢弃无权数据。
8. **双方言兼容，MySQL 基线是 5.6**（`AGENTS.md:112`）。5.6 默认 Antelope 文件格式 + COMPACT 行格式 +
   `innodb_large_prefix=OFF` + utf8mb4，由此得到 7 条硬规则，每条都有仓库既有先例：
   1. **索引键 ≤767 字节**：被索引的 `VARCHAR` 单列 ≤191 字符（191×4=764），复合索引各列字节数之和 ≤767
      （仓库既有的 `VARCHAR(191) UNIQUE` 约定正是为此，见 `migrations/ddl.sql:15`、`:83`）。
   2. **被索引的时间戳用 `VARCHAR(32)`，不用 `TEXT`**：MySQL 对 BLOB/TEXT 建索引必须写显式前缀长度，
      否则 `Error 1170`（`docs/operations/schema-upgrades.md:43`、`v0013_to_v0014` 头注释已记录）。
   3. **行内行尺寸 <8126 字节**：COMPACT 下每个变长列本地最多存 768 字节前缀 + 20 字节溢出指针，
      16KB 页的最大内联行是 8126 字节，超限即 `Error 1118`。按最悲观模型
      `Σ min(列最大字节数, 788) + 定长列` 核算，新表必须 <8126 且留 ≥15% 余量（见「MySQL 5.6 预算核算」）。
   4. **不用 `ADD COLUMN IF NOT EXISTS`**：MySQL 不支持（`internal/storage/account_repository_test.go:125`
      已把这条固化成断言）。本阶段只 `CREATE TABLE`，不 `ALTER` 任何既有表。
   5. **不用 `JSON`、`WITH RECURSIVE`、`ON DUPLICATE KEY`**：分别是 5.7+、8.0+ 与 SQLite 不可移植片段
      （`internal/storage/mysql_test.go:50` 已固化）。
   6. **MySQL 脚本不得写 `CREATE INDEX IF NOT EXISTS`**（MySQL 全版本不支持索引级 `IF NOT EXISTS`），
      SQLite 脚本必须写（重试安全）；重复对象由 `applySchemaStatements` 的 `isDuplicateError` 容忍。
   7. **不指定 `ROW_FORMAT`/`CHARSET`/`ENGINE`**：与既有 DDL 一致，让同一份语句在 SQLite 与 MySQL 都能执行；
      预算按 utf8mb4 + COMPACT 的最坏情况核算，因此部署选择更宽松的行格式只会更安全。
   另外 TEXT/BLOB 列不得带 `DEFAULT`（MySQL 全版本禁止），本阶段两张表的 TEXT 列均无默认值。
9. **明文密钥永不入库**（`AGENTS.md:107`、`AGENTS.md:143`、规格 §8）：peer 私钥只以 `private_key_ciphertext`/`nonce`/`key_id`/
   `version` 四列存放，形状照 `credentials` 与 `user_mfa`；本阶段只负责存取字节，加解密与 reveal 属阶段 4。
10. **TDD**（`AGENTS.md:116`）：每个任务先写失败测试并实测红灯输出，再写最小实现，最后跑绿；
    禁止实现完成后补测试。
11. **提交纪律**：每个任务一次提交，`<type>(<scope>): <subject>`，祈使句、≤50 字符、不加句号；
    未经用户明确授权不 push/merge。
12. **诚实记录未执行的验证**：本环境无 `docker`、无 `mysqld`、`TUNNELMESH_TEST_MYSQL_DSN` 未设置，
    MySQL 运行时验证**必然 SKIP**；PR 记录必须写「未执行 + 原因 + 由谁在何环境补跑」，不得写成已通过。

## 本计划的前置核实结论（撰写计划时已实测）

以下全部是在 `codex/vpn-phase1-constraint-reversal`（`4d443a6`）上实测得到的事实，不是估计值：

1. **当前 Schema 版本是 14**：`internal/storage/db.go:23` `SchemaVersion = 14`；最新增量目录是
   `migrations/incremental/v0013_to_v0014/`（含 `mysql.sql` 与 `sqlite.sql`）。
2. **迁移链是硬编码 switch**：`internal/storage/db.go:351` 的 `for version < SchemaVersion` 内按
   `case 5…13` 逐个选择脚本，`default` 返回 `missing adjacent migration vNNNN_to_vNNNN`。
   因此新增 v15 必须同时改 `migrations/embed.go`、`db.go` 的 switch 和 `SchemaVersion`，缺一即启动失败。
3. **表存在性是启动门禁**：`internal/storage/db.go:465` 的 `requireSchemaTables` 硬编码 19 张表名，
   `auto_init` 关闭时缺表快速失败；新增两张表必须追加进该清单。
4. **有两处测试把版本号写成字面量 14**，抬版本时必须同步：`internal/storage/client_repository_test.go:122`
   `TestSchemaVersionIs14`、`internal/storage/mysql_test.go:66`。其余测试都用 `SchemaVersion` 常量比较，无需改。
5. **`migrations/ddl.sql` 共 433 行、29 张表**，表顺序按领域分组，`audit_logs`(415) 与 `idempotency_keys`(426)
   固定在末尾；`webssh_sessions` 块结束于第 379 行（`CREATE INDEX idx_webssh_sessions_ticket …`），
   新表插在它与 `agent_runtime_stats`(381) 之间最符合现有分组。
6. **双方言脚本的唯一差异是索引语法**：`v0013_to_v0014/mysql.sql` 用裸 `CREATE INDEX`，
   `sqlite.sql` 用 `CREATE INDEX IF NOT EXISTS`，其余语句逐字相同；两份都带同一段 11 行头注释
   （additive-only、非滚动升级窗口、备份回滚、重试安全、`VARCHAR(32)` 时间戳理由）。
   `internal/storage/mysql_test.go:325` `TestMySQLV13ToV14IdentityMigrationIsAdjacent` 把这些约定固化成断言。
7. **Repository 范式**：`internal/storage/credential_repository.go` 是最贴近的样板（用户拥有的、带密封秘密的、
   逻辑删除的实体）：列清单常量、`Create/Get/Update/Delete/Restore/List`、`validateX` 只做字段存在性校验、
   `List` 用 `pageArgs`+`decodeCursor`+`ORDER BY id LIMIT limit+1`+`encodeCursor` 的 cursor 分页。
   租约与 epoch fencing 的样板是 `internal/storage/repository.go:1878` `leaseRepo.RegisterConnection`
   （8 次重试、MySQL 才加 `FOR UPDATE`、compare-and-set 保证单赢家），并发单赢家已有测试
   `internal/storage/sqlite_test.go:187` `TestSQLiteConcurrentExpiredLeaseTakeoverHasSingleOwner`。
8. **契约测的双方言接线方式**：共享函数 `runIdentityRepositoryContract(t, db)`
   （`internal/storage/identity_repository_test.go:717`）由 SQLite 侧
   `TestSQLiteIdentityRepositoryContract`(:875) 直接调用、由 MySQL 侧
   `TestMySQLIdentityRepositoryContract`(`mysql_test.go:381`) 在 `TUNNELMESH_TEST_MYSQL_DSN` 存在时调用同一函数。
   本阶段照此新增 `runVPNRepositoryContract`。
9. **MySQL 运行时测试在本环境全部 SKIP**：`command -v docker mysqld mysql mariadbd` 均无输出，
   `TUNNELMESH_TEST_MYSQL_DSN` 未设置；`.github/workflows/ci.yml` 也没有 MySQL service，
   因此 MySQL 侧今天只有静态脚本断言在 CI 真正执行（`docs/development/testing.md:50` 已记录此约定）。
10. **既有缺陷（本阶段不修，只记录）**：`internal/storage/sqlite_test.go:763`
    `TestSQLiteFreshSchemaMatchesIncrementalIdentityChain` 构造 `upgraded` 库后**从未调用 `OpenSQLite`
    执行增量链**，实际比较的是「全量 DDL vs 全量 DDL」，因此它并没有守护它声称守护的漂移。
    本阶段的同类测试必须真正跑迁移；该既有缺陷记为发现项，另行处理（`AGENTS.md`：不修不相关缺陷）。
11. **`docs/deployment/binary-release.md:54` 已经滞后**：正文写「`schemaVersion` … 当前值为 13」，
    而 `SchemaVersion` 已是 14。该值由 `scripts/build-release.sh:51` 从 `db.go` 读取，文档里的数字是手写副本。
    本阶段把它改成 15（同时修正既有滞后）。
12. **`docs/operations/configuration.md:445`、`docs/en/operations/security.md:149`、
    `docs/en/user-guide/sso-and-mfa.md` 的 `schema-upgrades.md#v13-to-v14` 锚点不改**：
    它们指向身份特性自己的升级章节，v15 只是在其上方新增一节，锚点依然有效且语义正确。
13. **MySQL 5.6 行内行尺寸预算是真实的紧约束，不是理论担忧**：用同一套最悲观模型
    （变长列计 `min(最大字节数, 788)`，定长列计 8 字节）扫描 `migrations/ddl.sql` 的 29 张表，
    实测结果：`oidc_providers` **12,439 字节（8126 的 153%，超 4,313 字节）**，
    其余 28 张表全部 <8126，最大的是 `service_tokens` 6,163（76%）、`remote_servers` 5,616、
    `credentials` 5,361、`webssh_sessions` 5,066。也就是说仓库的既有事实标准是「单表内联预算 ≤ ~6,200」。
14. **本计划初稿的 `vpn_peers` 会超标**：按规格 §7.1 的列清单直接翻译成
    `name VARCHAR(191)`/`public_key VARCHAR(191)`/`private_key_nonce TEXT`/`private_key_key_id VARCHAR(128)`/
    `description VARCHAR(512)`/`created_at TEXT` 时，实测内联预算 **8,448 字节 > 8126**，
    在 MySQL 5.6 COMPACT 下会以 `Error 1118` 建表失败。列宽必须按 D11 收敛。
15. **既有风险（本阶段不修，只记录）**：`oidc_providers` 的 12,439 字节意味着它可能在真实 MySQL 5.6
    （Antelope + COMPACT + `innodb_large_prefix=OFF`）上建表失败。本仓库的 MySQL 测试全部由
    `TUNNELMESH_TEST_MYSQL_DSN` 门控且本机无 MySQL（前置核实结论 9），CI 也没有 MySQL service，
    因此该表从未在真实 5.6 上验证过。修复它需要一次独立的 v16 迁移（收窄 6 个 `VARCHAR(512)` 端点列
    或改 `ROW_FORMAT=DYNAMIC`），属独立缺陷，按 `AGENTS.md`「不修不相关缺陷」记为发现项。

## 架构决策

### D1：新增两张表，`vpn_flows` 不建表

`vpn_peers` 承载 peer 的权威事实（身份、密钥密文、VPN IP、出口 Agent、策略与限额），
`vpn_ip_leases` 承载节点子网租约与已分配计数。运行时流状态不入库（规格 §7.1），
因此没有第三张表，也没有需要清理的高频写入。

### D2：/32 的唯一性是数据库约束，不是应用逻辑

`UNIQUE(node_id, vpn_ip)` 让「同一节点把同一个 VPN IP 签给两个 peer」在并发下也不可能发生；
`public_key VARCHAR(64) NOT NULL UNIQUE` 让公钥冲突在 DB 层失败（宽度取 64 的理由见 D11）。Repository 把
`isDuplicateError` 映射成 `ErrVPNPeerConflict`，Service（阶段 4）再映射成 409 `vpn_peer_conflict`。
这是 `AGENTS.md:75`「唯一性必须由数据库约束保证」的直接落实。

### D3：peer 名称不加唯一约束

`credentials`（同为「用户拥有的、命名的、带密封秘密的实体」）没有名称唯一约束，
`remote_servers` 亦然；名称只是展示标签，重复不影响安全与路由。规格 §12.1 的
409 `vpn_peer_conflict`（名称冲突）由阶段 4 的 Service 在事务内做 best-effort 校验，
并在文档里写明「名称冲突是尽力而为，公钥与 VPN IP 冲突是强约束」。
替代方案（新增 `name_key VARBINARY(64)` 摘要列 + 唯一约束，照 `auth_login_attempts.bucket_key`）
被拒绝：它为一个纯展示字段引入一列、一处摘要算法和一个 v15→v16 的后续迁移风险，违反 YAGNI。

### D4：吊销是状态迁移，不是删行

`vpn_peers` 没有 `deleted_at` 列（规格 §7.1）。吊销 = `status='revoked'`，行永久保留以支撑审计与
「吊销后重签」的取证。因此 Repository **不提供 `Delete`**，只提供 `SetStatus`；
`revoked` 是终态，对已吊销 peer 再调 `SetStatus` 返回 `ErrVPNPeerRevoked`（照 `ErrServiceTokenRevoked` 范式）。
`expired` 不是存储状态，由 `expires_at` 派生（照 service token 的 `ErrServiceTokenExpired` 范式）。

### D5：`allowed_ips`/`allowed_ports` 在存储层是不透明 TEXT

规范化、CIDR 解析、端口区间展开属阶段 4 的 `internal/vpn`。存储层只保证非空与可读写，
**不解析**，避免同一套解析规则出现两处真相（`AGENTS.md` 单一数据源）。
编码格式由阶段 4 定稿并写进 `internal/vpn` 的黄金文件测试；本阶段的契约测用固定字面量往返验证。

### D6：密封私钥四列照 `credentials` 范式，可空

`private_key_ciphertext`/`private_key_nonce`/`private_key_key_id`/`private_key_version` 全部可空，
因为签发瞬间可能存在「已生成公钥、密钥尚未密封」的中间态（`TUNNELMESH_TOKEN_ENCRYPTION_KEY` 缺失时
阶段 4 会 fail closed，但存储层不得因此拒写公钥）。列的**形状**照 `oidc_providers.client_secret_*` 与
`credentials.secret_*`（密文 / nonce / key id / version 四件套），但**宽度**按 D11 收敛：
`TEXT`/`VARCHAR(64)`/`VARCHAR(64)`/`INTEGER`。密文保留 `TEXT` 是因为密封载荷长度会随格式演进，
nonce 与 key id 是有界值，收窄它们换来 788 字节的行内预算，理由写进 DDL 注释防止被「规范化」回去。

### D7：`vpn_ip_leases` 用 epoch fencing + 单赢家 CAS，照 `leaseRepo.RegisterConnection`

`AcquireSubnet` 的语义：不存在 → 插入 `epoch=1`；存在且未过期且持有者不同 → `ErrVPNIPLeaseHeld`；
存在且已过期（或持有者相同）→ CAS 更新为 `epoch+1`、新持有者、新过期时间。
MySQL 分支在 `SELECT` 上加 `FOR UPDATE`，SQLite 靠 compare-and-set（`WHERE epoch=?`）保证单赢家，
最多重试 8 次，与 `agent_connection_leases` 完全同构。
`Renew`/`Release`/`AddAllocated` 都带 `epoch` 条件，过期 epoch 返回 `ErrVPNIPLeaseStaleEpoch`
（照 `ErrRuntimeStatsStaleEpoch`），防止已被接管的旧持有者写回计数。

### D8：`AddAllocated` 是带围栏的增量计数，不是分配器

它只做 `UPDATE … SET allocated_count=allocated_count+? WHERE node_id=? AND subnet=? AND lease_holder=? AND epoch=?`
并返回新值；`delta<0` 时附加 `AND allocated_count+?>=0` 防止计数变负。
「哪个 /32 空闲」的判定属阶段 4 的 `internal/vpn/ippool.go`（纯函数）+ `UNIQUE(node_id, vpn_ip)`（DB 事实），
`allocated_count` 只是给指标与快速耗尽判定用的派生计数，**不是权威来源**。

### D9：MySQL 验证用「静态方言断言 + DSN 门控运行时测」双层兜底

本环境与 CI 都没有 MySQL（前置核实结论 9），因此：
静态断言测（无门控，永远执行）覆盖 additive-only、双方言索引语法差异、`VARCHAR(32)` 时间戳、
禁用片段、全量 DDL 与增量脚本表集合一致；DSN 门控测覆盖真实 MySQL 上的迁移与契约，
由持有 MySQL 的运维/开发机补跑，PR 记录如实写「未执行 + 原因」。
这不是降低标准，而是把「本机可执行的部分」做到最强，并把不可执行的部分显式挂账。

### D10：新增 `requireSchemaTables` 条目，即使 VPN 默认关闭

`server.vpn.enabled` 默认 false，但两张表随全量 DDL 与增量迁移必然存在，
把它们加进启动门禁可以在「迁移被手工跳过」时快速失败，与 `auth_settings` 等特性表的处理一致。
`AGENTS.md:57` 要求 `auto-init` 关闭时缺表快速失败并给出缺失信息，本阶段沿用该机制、不新造检查。

### D11：列宽按 MySQL 5.6 COMPACT 行内预算收敛，并加静态预算守卫测试

前置核实结论 13/14 是实测数字：直译规格 §7.1 的列宽会让 `vpn_peers` 的内联行尺寸达到 8,448 字节，
在 MySQL 5.6 默认行格式下建表即失败。因此本计划在**不改变语义**的前提下收敛列宽，
把预算压到 6,460 字节（79%，余量 1,666 字节），落回仓库既有事实标准（≤ ~6,200）同一量级：

| 列 | 规格 §7.1 直译 | 本计划采用 | 理由 |
| --- | --- | --- | --- |
| `public_key` | `VARCHAR(191)`（764） | `VARCHAR(64)`（256） | WireGuard 公钥是固定的 44 字符 base64，64 已含余量；且它是 UNIQUE 索引列，越短索引越省 |
| `private_key_nonce` | `TEXT`（788） | `VARCHAR(64)`（256） | AES-GCM nonce 固定 12 字节、base64 后 16 字符；这是唯一一处偏离既有 `*_nonce TEXT` 约定的列，理由写进 DDL 注释与升级文档 |
| `private_key_key_id` | `VARCHAR(128)`（512） | `VARCHAR(64)`（256） | 与 `credentials.secret_key_id VARCHAR(64)`（`migrations/ddl.sql:331`）一致，`oidc_providers` 的 128 是另一种既有写法 |
| `vpn_ip` | `VARCHAR(64)`（256） | `VARCHAR(45)`（180） | 45 字符是 IPv6 文本形式（含 IPv4-mapped）的上界，IPv4 /32 只需 15；参与 `UNIQUE(node_id, vpn_ip)`，越短越好 |
| `created_at` | `TEXT`（788） | `VARCHAR(32)`（128） | 与 v14 全部新表一致（`oidc_providers.created_at VARCHAR(32) NOT NULL`）；`TEXT` 是更早的写法 |
| `name` / `description` | `VARCHAR(191)` / `VARCHAR(512)` | `VARCHAR(255)` / `VARCHAR(255)` | 与 `credentials.name`、`remote_servers.name` 等用户可见命名列一致；两者都不建索引，故不受 767 约束 |

`private_key_ciphertext`、`allowed_ips`、`allowed_ports` 保留 `TEXT`：密文长度随密封格式演进，
两个 allowlist 是无界列表（`agent_policies.allowed_cidrs`/`allowed_ports` 是既有先例，`migrations/ddl.sql:205`）。

**收敛只是必要条件，不是充分条件**：列宽决策如果只写在计划里，下一个人加一列就会重新超标。
因此 Task 1 增加一个**永远执行的静态预算守卫测试** `TestVPNMigrationsFitMySQL56Budgets`，
直接解析 `migrations.V14ToV15MySQL` 与 `migrations.DDL` 的 `CREATE TABLE` 文本，
按最悲观模型计算内联行尺寸与每个索引键长度，断言 `<8126 且余量 ≥15%`、`索引键 ≤767`，
失败时打印实际字节数。它不需要 MySQL，因此在 CI 与本机都是真门禁；
它只覆盖 v15 的两张新表（不追溯既有表，避免把 `oidc_providers` 的既有缺陷变成本阶段的红灯）。

## 精确文件清单

### 新增（6 个）

| 文件 | 任务 | 内容 |
| --- | --- | --- |
| `migrations/incremental/v0014_to_v0015/mysql.sql` | Task 1 | 头注释 + 两张表 + 5 个裸 `CREATE INDEX` |
| `migrations/incremental/v0014_to_v0015/sqlite.sql` | Task 1 | 同语句，索引用 `CREATE INDEX IF NOT EXISTS` |
| `internal/storage/vpn_peer_repository.go` | Task 3 | `vpnPeerRepo`：列常量、CRUD、`SetStatus`、cursor 分页、按节点装载、计数 |
| `internal/storage/vpn_ip_lease_repository.go` | Task 4 | `vpnIPLeaseRepo`：`AcquireSubnet`/`Renew`/`Release`/`Get`/`ListByHolder`/`AddAllocated` |
| `internal/storage/vpn_repository_test.go` | Task 1/3/4 | SQLite 侧迁移测、等价测、契约测、并发单赢家测、auto-init 关闭缺表测 |
| `docs/pull-requests/2026-09-20-vpn-phase3-schema-v15.md` | Task 6 | PR 记录（11 个小节齐全） |

### 修改（12 个）

| 文件 | 任务 | 修改点 |
| --- | --- | --- |
| `migrations/ddl.sql` | Task 1 | 在第 379 行（`idx_webssh_sessions_ticket` 之后、`agent_runtime_stats` 之前）插入两张表与 5 个索引 |
| `migrations/embed.go` | Task 1 | 追加 `V14ToV15MySQL`、`V14ToV15SQLite` 两个 `//go:embed` 变量 |
| `internal/storage/db.go` | Task 1/3/4 | `:23` `SchemaVersion = 15`；`:394` 的 `case 13` 之后新增 `case 14`；`:465` 表清单追加两项；`DB` struct 两个字段、`newRepositories` 两行、两个 accessor |
| `internal/storage/models.go` | Task 2 | `VPNPeer`、`VPNPeerStatus` 常量、`VPNPeerFilter`、`VPNIPLease` |
| `internal/storage/repository.go` | Task 2/3/4 | `VPNPeerRepository`、`VPNIPLeaseRepository` 接口；`ErrVPNPeerConflict`、`ErrVPNPeerRevoked`、`ErrVPNIPLeaseHeld`、`ErrVPNIPLeaseStaleEpoch` |
| `internal/storage/client_repository_test.go` | Task 1 | `:122` `TestSchemaVersionIs14` → `TestSchemaVersionIs15`，字面量 14 → 15 |
| `internal/storage/mysql_test.go` | Task 1/3/4 | `:66` 字面量 14 → 15；新增静态方言断言测、DSN 门控迁移测与契约测 |
| `docs/operations/schema-upgrades.md` | Task 5 | 在 `## v13 to v14`(第 3 行) 之前插入完整的 `## v14 to v15` 章节 |
| `docs/user-guide/server-admin.md` | Task 5 | `:33`「当前 Schema 版本为 v14」→ v15 |
| `docs/deployment/binary-release.md` | Task 5 | `:54`「当前值为 13」→ 15（同时修正既有滞后） |
| `docs/pull-requests/README.md` | Task 6 | `scripts/gen_doc_index.py` 生成 |
| `docs/superpowers/plans/README.md`、`docs/superpowers/specs/README.md` | Task 6 | 同上（建立计划↔规格↔PR↔ADR 关联） |

### 明确不改（评审时确认判断正确）

- 已发布的 `migrations/incremental/v0005_to_v0006` … `v0013_to_v0014`（Global Constraint 2）。
- `internal/storage/sqlite_test.go:763` 的既有缺陷测试（前置核实结论 10）。
- `docs/operations/configuration.md:445`、`docs/en/operations/security.md:149`、
  `docs/en/user-guide/sso-and-mfa.md` 的 `#v13-to-v14` 锚点（前置核实结论 12）。
- `go.mod`、`go.sum`、`Dockerfile`、`scripts/build-release.sh`（版本值从 `db.go` 动态读取，无需改）。
- `web/`、`internal/server/api.go`、`docs/api/openapi.yaml`、`internal/config/`（阶段 4+）。
- `deploy/`（Grafana Row 与告警规则属阶段 8）。

## DDL 定稿（Task 1 逐字使用）

`migrations/ddl.sql` 插入内容与 `v0014_to_v0015/{mysql,sqlite}.sql` 的表语句完全一致：

```sql
CREATE TABLE IF NOT EXISTS vpn_peers (
    id VARBINARY(255) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    owner_id VARBINARY(255) NOT NULL,
    -- public_key is the fixed 44-character base64 WireGuard key, so VARCHAR(64)
    -- is generous. The width is also a MySQL 5.6 budget decision: this column is
    -- UNIQUE-indexed, and COMPACT row format keeps up to 768 bytes of every
    -- variable-length column inline (see docs/operations/schema-upgrades.md).
    public_key VARCHAR(64) NOT NULL UNIQUE,
    private_key_ciphertext TEXT,
    -- private_key_nonce is VARCHAR(64) rather than TEXT on purpose. An AES-GCM
    -- nonce is 12 bytes (16 base64 characters), and TEXT would cost 788 bytes of
    -- the 8126-byte MySQL 5.6 COMPACT inline row budget instead of 256. Do not
    -- "normalize" it back to TEXT without recomputing that budget.
    private_key_nonce VARCHAR(64),
    private_key_key_id VARCHAR(64),
    private_key_version INTEGER,
    vpn_ip VARCHAR(45) NOT NULL,
    node_id VARBINARY(255) NOT NULL,
    agent_id VARBINARY(255) NOT NULL,
    allowed_ips TEXT NOT NULL,
    allowed_ports TEXT NOT NULL,
    allow_private_targets INTEGER NOT NULL DEFAULT 0,
    icmp_enabled INTEGER NOT NULL DEFAULT 0,
    max_concurrent_flows INTEGER NOT NULL DEFAULT 128,
    packet_rate_limit INTEGER NOT NULL DEFAULT 0,
    expires_at VARCHAR(32),
    status VARCHAR(32) NOT NULL,
    description VARCHAR(255),
    created_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    UNIQUE(node_id, vpn_ip)
);
CREATE INDEX idx_vpn_peers_owner ON vpn_peers(owner_id, id);
CREATE INDEX idx_vpn_peers_node ON vpn_peers(node_id, status);
CREATE INDEX idx_vpn_peers_expires ON vpn_peers(expires_at);

CREATE TABLE IF NOT EXISTS vpn_ip_leases (
    id VARBINARY(255) PRIMARY KEY,
    node_id VARBINARY(255) NOT NULL,
    subnet VARCHAR(64) NOT NULL,
    allocated_count INTEGER NOT NULL DEFAULT 0,
    lease_holder VARBINARY(255) NOT NULL,
    lease_expires_at VARCHAR(32) NOT NULL,
    epoch INTEGER NOT NULL DEFAULT 0,
    acquired_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    UNIQUE(node_id, subnet)
);
CREATE INDEX idx_vpn_ip_leases_holder ON vpn_ip_leases(lease_holder, lease_expires_at);
CREATE INDEX idx_vpn_ip_leases_expires ON vpn_ip_leases(lease_expires_at);
```

### MySQL 5.6 预算核算（实测值，`TestVPNMigrationsFitMySQL56Budgets` 守护）

索引键预算（utf8mb4 = 4 字节/字符，`VARBINARY(n)` = n 字节；5.6 默认 `innodb_large_prefix=OFF`
时上限 767 字节）：

| 索引 | 组成 | 字节 | 结论 |
| --- | --- | --- | --- |
| `PRIMARY KEY (id)` | `VARBINARY(255)` | 255 | ✓ |
| `public_key` 列内 UNIQUE | `VARCHAR(64)`×4 | 256 | ✓ |
| `UNIQUE(node_id, vpn_ip)` | 255 + 45×4 | 435 | ✓ |
| `idx_vpn_peers_owner` | 255 + 255 | 510 | ✓ |
| `idx_vpn_peers_node` | 255 + 32×4 | 383 | ✓ |
| `idx_vpn_peers_expires` | 32×4 | 128 | ✓ |
| `UNIQUE(node_id, subnet)` | 255 + 64×4 | 511 | ✓ |
| `idx_vpn_ip_leases_holder` | 255 + 32×4 | 383 | ✓ |
| `idx_vpn_ip_leases_expires` | 32×4 | 128 | ✓ |

最大索引键 511 字节，距 767 上限还有 256 字节余量；没有任何索引落在 `TEXT` 列上（否则触发 `Error 1170`）。

行内行尺寸预算（COMPACT：变长列计 `min(最大字节数, 788)`，定长列计 8 字节，上限 8126）：

| 表 | 列数 | 内联字节 | 占比 | 余量 |
| --- | --- | --- | --- | --- |
| `vpn_peers` | 22 | 6,460 | 79% | 1,666 |
| `vpn_ip_leases` | 9 | 1,421 | 17% | 6,705 |
| 对照：`service_tokens`（既有最大者，`oidc_providers` 除外） | 13 | 6,163 | 76% | 1,963 |
| 对照：`oidc_providers`（既有超标项，见前置核实结论 15） | 26 | 12,439 | 153% | −4,313 |

`vpn_peers` 的 6,460 字节与仓库既有事实标准同量级，余量 1,666 字节 ≈ 2 个 `TEXT` 列或 6 个
`VARCHAR(64)` 列，足够阶段 4-6 增补 `last_handshake_at VARCHAR(32)`（128 字节）这类列。
**阶段 4+ 新增任何变长列前必须重算该预算**，守卫测试会在超标时直接红灯。

`declared_var_sum`（MySQL 的 65,535 字节声明上限，TEXT/BLOB 不计）：`vpn_peers` 4,520、
`vpn_ip_leases` 1,421，均远低于上限。

### 双方言脚本差异

两份增量脚本的**表语句逐字相同**，唯一差异是索引行：

- `mysql.sql`：`CREATE INDEX idx_vpn_peers_owner ON vpn_peers(owner_id, id);`（裸形式，共 5 行）
- `sqlite.sql`：`CREATE INDEX IF NOT EXISTS idx_vpn_peers_owner ON vpn_peers(owner_id, id);`（重试安全形式，共 5 行）

两份脚本都必须以与 `v0013_to_v0014` 同构的 11 行头注释开头，说明：本次为 additive-only 的两张新表、
不 `ALTER` 任何既有表；这不是滚动升级窗口（`db.go` 拒绝打开版本号高于二进制 `SchemaVersion` 的库）；
回滚靠升级前备份而不是就地降级二进制；`applySchemaStatements` 自 v6 起容忍重复对象，故部分应用可重试；
被索引的时间戳用 `VARCHAR(32)` 而非 `TEXT`，以便 MySQL 无需显式键长度；
列宽受 MySQL 5.6 COMPACT 行内预算约束，改动前必须重算。

## 接口定稿（Task 2/3/4 逐字使用）

```go
// repository.go
var ErrVPNPeerConflict = errors.New("vpn peer already exists")
var ErrVPNPeerRevoked = errors.New("vpn peer is already revoked")
var ErrVPNIPLeaseHeld = errors.New("vpn ip subnet lease is held by another node")
var ErrVPNIPLeaseStaleEpoch = errors.New("vpn ip lease epoch is stale")

type VPNPeerRepository interface {
	Create(context.Context, VPNPeer) (VPNPeer, error)
	Get(context.Context, string) (VPNPeer, error)
	GetByPublicKey(context.Context, string) (VPNPeer, error)
	GetByNodeAndIP(context.Context, string, string) (VPNPeer, error)
	Update(context.Context, VPNPeer) error
	SetStatus(context.Context, string, VPNPeerStatus, time.Time) error
	List(context.Context, VPNPeerFilter, string, int) (Page[VPNPeer], error)
	ListByNode(context.Context, string) ([]VPNPeer, error)
	CountByNode(context.Context, string) (int, error)
	CountByOwner(context.Context, string) (int, error)
}

type VPNIPLeaseRepository interface {
	AcquireSubnet(context.Context, VPNIPLease) (VPNIPLease, error)
	Renew(context.Context, string, string, string, int64, time.Duration) error
	Release(context.Context, string, string, string, int64) error
	Get(context.Context, string, string) (VPNIPLease, error)
	ListByHolder(context.Context, string) ([]VPNIPLease, error)
	AddAllocated(context.Context, string, string, string, int64, int) (int, error)
}
```

```go
// models.go
type VPNPeerStatus string

const (
	VPNPeerStatusActive   VPNPeerStatus = "active"
	VPNPeerStatusDisabled VPNPeerStatus = "disabled"
	VPNPeerStatusRevoked  VPNPeerStatus = "revoked"
)

// VPNPeer is the persistence record for one WireGuard peer. AllowedIPs and
// AllowedPorts are opaque canonical text produced by internal/vpn (phase 4);
// storage never parses them, so the encoding has exactly one owner.
// PrivateKey* mirror credentials.SecretCiphertext: sealed by auth.SecretStore,
// never plaintext, empty until the key is sealed.
type VPNPeer struct {
	ID                 string
	Name               string
	OwnerID            string
	PublicKey          string
	PrivateKeyCiphertext string
	PrivateKeyNonce      string
	PrivateKeyKeyID      string
	PrivateKeyVersion    int
	VPNIP              string
	NodeID             string
	AgentID            string
	AllowedIPs         string
	AllowedPorts       string
	AllowPrivateTargets bool
	ICMPEnabled        bool
	MaxConcurrentFlows int
	PacketRateLimit    int
	ExpiresAt          *time.Time
	Status             VPNPeerStatus
	Description        string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// VPNPeerFilter scopes List. OwnerUserID is applied inside the SQL WHERE clause
// so permission filtering happens within pagination semantics, never after it.
type VPNPeerFilter struct {
	OwnerUserID string
	NodeID      string
	AgentID     string
	Status      VPNPeerStatus
	Keyword     string
}

// VPNIPLease is one node's claim on a /24 carved out of server.vpn.ip_pool.
// AllocatedCount is a derived counter for metrics and fast exhaustion checks;
// the authoritative fact that a /32 is taken is vpn_peers.UNIQUE(node_id, vpn_ip).
type VPNIPLease struct {
	ID             string
	NodeID         string
	Subnet         string
	AllocatedCount int
	LeaseHolder    string
	LeaseExpiresAt time.Time
	Epoch          int64
	AcquiredAt     time.Time
	UpdatedAt      time.Time
	TTL            time.Duration // not persisted; bounds LeaseExpiresAt on acquire/renew
}
```

`DB` 装配（`db.go`）：struct 新增 `vpnPeers VPNPeerRepository`、`vpnIPLeases VPNIPLeaseRepository`；
`newRepositories` 新增 `vpnPeers: NewVPNPeerRepository(db)`、`vpnIPLeases: NewVPNIPLeaseRepository(db)`；
accessor `func (d *DB) VPNPeers() VPNPeerRepository`、`func (d *DB) VPNIPLeases() VPNIPLeaseRepository`。

## 任务分解

### Task 1: Schema v15 迁移链（红灯 → 绿灯）

**Files:** `migrations/ddl.sql`、`migrations/incremental/v0014_to_v0015/{mysql,sqlite}.sql`、
`migrations/embed.go`、`internal/storage/db.go`（仅版本/switch/表清单三处）、
`internal/storage/client_repository_test.go`、`internal/storage/mysql_test.go`、
`internal/storage/vpn_repository_test.go`（迁移与等价测试部分）

- [x] **Step 1: 先写失败测试**

  在 `internal/storage/vpn_repository_test.go` 新增 4 个测试，在 `internal/storage/mysql_test.go` 新增 3 个：

  1. `TestSQLiteV14ToV15VPNMigration`：`raw.Exec(migrations.DDL)` 建 v15 全量库 →
     `DROP TABLE vpn_peers`、`DROP TABLE vpn_ip_leases` → 插入一条 `users` 存量行 →
     `DELETE FROM schema_meta WHERE id=1` + `INSERT … VALUES(1,14)` → `raw.Close()` →
     `OpenSQLite(ctx, dsn, true)`。断言：`db.SchemaVersion(ctx) == SchemaVersion`、
     两张表在 `sqlite_master` 中存在、存量 user 行仍可读、再次 `OpenSQLite` 是 no-op（重试安全）。
  2. `TestSQLiteFreshSchemaMatchesIncrementalVPNChain`：`fresh` 用 `OpenSQLite(…, true)` 建；
     `upgraded` 用 `raw.Exec(migrations.DDL)` + `DROP TABLE vpn_peers` + `DROP TABLE vpn_ip_leases`
     + `schema_meta=14` 构造真实 v14 基线（`DROP TABLE` 会连带删除该表的索引，因此**不要**再写
     `DROP INDEX`，否则 SQLite 报 `no such index`），然后**必须调用 `OpenSQLite(ctx, upgraded, true)` 执行增量链**
     （前置核实结论 10：既有同类测试漏了这一步，本测试不得重复该错误），
     断言迁移后 `SchemaVersion(ctx) == 15`，再用 `pragma_table_info` 比较两库的列集合、
     用 `pragma_index_list`/`pragma_index_info` 比较两库的索引名与索引列顺序。
  3. `TestSQLiteAutoInitDisabledRejectsMissingVPNTables`：照 `sqlite_test.go:91` 范式，
     建全量库后 `DROP TABLE vpn_peers`、`schema_meta=SchemaVersion`，
     `OpenSQLite(ctx, dsn, false)` 必须报错且错误文本含 `vpn_peers`；对 `vpn_ip_leases` 重复一次。
  4. `TestMySQLV14ToV15VPNMigrationsAreAdjacentAndDialectSafe`（写在 `mysql_test.go`，无 DSN 门控，
     照 `:325` 范式）：对两份脚本断言 additive-only（不含 `DROP COLUMN`/`MODIFY COLUMN`/`DROP TABLE`/
     `JSON`/`ON DUPLICATE KEY`/`WITH RECURSIVE`/`ADD COLUMN IF NOT EXISTS`/`ALTER TABLE`）；
     都含 `CREATE TABLE IF NOT EXISTS vpn_peers` 与 `CREATE TABLE IF NOT EXISTS vpn_ip_leases`；
     都含 `UNIQUE(node_id, vpn_ip)`、`UNIQUE(node_id, subnet)`、`public_key VARCHAR(64) NOT NULL UNIQUE`；
     被索引时间戳都是 `VARCHAR(32)`，且没有任何索引落在 `TEXT` 列上（`Error 1170`）；
     `private_key_nonce VARCHAR(64)`、`private_key_key_id VARCHAR(64)`、`vpn_ip VARCHAR(45)`、
     `created_at VARCHAR(32) NOT NULL` 五个宽度约定逐字存在（防止后续「规范化」成 TEXT 而破坏 D11 的预算）；
     两份脚本都以 `;` 结尾，且按 `;` 切分后不存在只含注释的片段（Global Constraint 5）；
     MySQL 脚本**不含** `CREATE INDEX IF NOT EXISTS`，SQLite 脚本 5 个索引**全部**是 `IF NOT EXISTS` 形式；
     `migrations.DDL` 同时含两张表与 5 个索引名；两份脚本的表语句在去掉索引行后逐字相同。
  5. `TestVPNMigrationsFitMySQL56Budgets`（写在 `mysql_test.go`，**无 DSN 门控**，因此在本机与 CI 都是真门禁）：
     解析 `migrations.V14ToV15MySQL` 与 `migrations.DDL` 中两张新表的 `CREATE TABLE` 文本，按 D11 的最悲观模型
     （`VARBINARY(n)`/`BINARY(n)` 计 n 字节，`VARCHAR(n)`/`CHAR(n)` 计 n×4 字节，`TEXT`/`BLOB` 与其它变长列计
     `min(最大字节数, 788)`，定长列计 8 字节）计算内联行尺寸，断言 `< 8126` 且余量 `>= 8126*0.15`；
     再解析每个 `CREATE INDEX` 与 `UNIQUE(...)` 的列组成，断言索引键字节数 `<= 767`，
     并断言索引列中没有 `TEXT`/`BLOB`。失败信息必须打印表名、实际字节数与超限的索引名，
     使超标时不需要重新推导公式。预期实测值：`vpn_peers` 6,460、`vpn_ip_leases` 1,421、最大索引键 511。
     该测试**只覆盖 v15 的两张新表**，不追溯既有表（`oidc_providers` 的既有超标见前置核实结论 15）。
  6. `TestMySQLV14ToV15VPNMigration`（`mysql_test.go`，`TUNNELMESH_TEST_MYSQL_DSN` 门控，照 `:145` 范式，
     含 `t.Cleanup` 恢复 schema 与版本号）。
  7. `TestMySQLVPNRepositoryContract`（`mysql_test.go`，DSN 门控，调用 Task 3/4 的 `runVPNRepositoryContract`）。
  8. 把 `client_repository_test.go:122` 的 `TestSchemaVersionIs14` 改名为 `TestSchemaVersionIs15`
     并断言 `SchemaVersion != 15` 时失败；把 `mysql_test.go:66` 的字面量 14 改成 15。

  Run: `go test ./internal/storage/ -run 'VPN|SchemaVersionIs' -count=1`
  Expected: **编译失败**，`undefined: migrations.V14ToV15MySQL`（以及 `V14ToV15SQLite`）。
  这是本任务的红灯基线，必须实测并把首行错误原文记进提交信息或 PR 记录。

- [x] **Step 2: 写增量脚本**

  新建 `migrations/incremental/v0014_to_v0015/mysql.sql` 与 `sqlite.sql`，
  内容为「DDL 定稿」小节的头注释 + 两张表 + 5 个索引（索引形式按方言区分）。

- [x] **Step 3: 最小实现**

  1. `migrations/embed.go` 末尾追加：
     `//go:embed incremental/v0014_to_v0015/mysql.sql` → `var V14ToV15MySQL string`；
     `//go:embed incremental/v0014_to_v0015/sqlite.sql` → `var V14ToV15SQLite string`。
  2. `internal/storage/db.go:23`：`SchemaVersion = 15`。
  3. `internal/storage/db.go` 的迁移 switch 在 `case 13` 之后新增：
     `case 14: script = migrations.V14ToV15SQLite; if driver == DriverMySQL { script = migrations.V14ToV15MySQL }`。
  4. `internal/storage/db.go:465` 的表清单末尾追加 `"vpn_peers", "vpn_ip_leases"`。
  5. `migrations/ddl.sql` 第 379 行后插入「DDL 定稿」小节的两张表与 5 个索引（裸 `CREATE INDEX` 形式）。

- [x] **Step 4: 跑绿**

  Run: `go test ./internal/storage/ -run 'VPN|SchemaVersionIs|Migration|AutoInit' -count=1`
  Expected: 全部 `PASS`；`TestMySQLV14ToV15VPNMigration` 与 `TestMySQLVPNRepositoryContract` 输出
  `--- SKIP`（DSN 未设置），SKIP 原因文本为 `TUNNELMESH_TEST_MYSQL_DSN is not set`。
  Run: `go test ./internal/storage/ -count=1`
  Expected: `ok`，无 FAIL（其余既有测试必须不受影响）。

- [x] **Step 5: 提交**

  `git add migrations internal/storage/db.go internal/storage/client_repository_test.go internal/storage/mysql_test.go internal/storage/vpn_repository_test.go`
  `git commit -m "feat(storage): add schema v15 vpn tables"`

### Task 2: 存储模型与 sentinel 错误

**Files:** `internal/storage/models.go`、`internal/storage/repository.go`、`internal/storage/vpn_repository_test.go`

- [x] **Step 1: 先写失败测试**

  在 `vpn_repository_test.go` 新增 `TestVPNPeerModelDefaultsAndValidation`：
  断言 `validateVPNPeer`（Task 3 实现，本步只声明期望行为）对以下输入返回错误——
  空 `OwnerID`、空 `Name`、空 `PublicKey`、空 `VPNIP`、空 `NodeID`、空 `AgentID`、
  空 `Status`、未知 `Status`（如 `"paused"`）、`MaxConcurrentFlows < 0`、`PacketRateLimit < 0`；
  对一条完整合法记录返回 nil。
  再新增 `TestVPNIPLeaseModelValidation`：空 `NodeID`、空 `Subnet`、空 `LeaseHolder`、
  `AllocatedCount < 0`、`Epoch < 0` 返回错误；`TTL <= 0` 时被规范化为默认 1 分钟（照 `leaseRepo` 的 `TTL` 处理）。
  断言四个 sentinel 错误互不相等且 `errors.Is` 只匹配自身。

  Run: `go test ./internal/storage/ -run 'VPNPeerModel|VPNIPLeaseModel' -count=1`
  Expected: **编译失败**，`undefined: VPNPeer`（以及 `VPNPeerStatus`、`VPNIPLease`、`validateVPNPeer`、
  `ErrVPNPeerConflict`）。记录红灯原文。

- [x] **Step 2: 最小实现**

  按「接口定稿」小节把 `VPNPeerStatus` 常量、`VPNPeer`、`VPNPeerFilter`、`VPNIPLease` 加进 `models.go`
  （放在 `Credential`/`RemoteServer` 之后、`Page[T]` 之前，保持领域分组）；
  把两个接口与四个 sentinel 错误加进 `repository.go`（接口放在 `WebSSHSessionRepository` 之后，
  错误放在 `ErrRuntimeStatsStaleEpoch` 附近）；
  在 `vpn_peer_repository.go` 里写 `validateVPNPeer`、在 `vpn_ip_lease_repository.go` 里写 `validateVPNIPLease`
  （本任务只建文件与校验函数，Repository 方法体在 Task 3/4 实现）。

- [x] **Step 3: 跑绿**

  Run: `go test ./internal/storage/ -run 'VPNPeerModel|VPNIPLeaseModel' -count=1` → Expected: `ok`
  Run: `go vet ./internal/storage/` → Expected: 无输出
  注意：此时 `vpnPeerRepo`/`vpnIPLeaseRepo` 尚未实现接口，`newRepositories` 也还没接线，
  所以不得在本任务里加 accessor（否则编译不过）。若 `go vet` 报未使用函数，把校验函数留到 Task 3/4 一起提交，
  并在提交信息里说明原因。

- [x] **Step 4: 提交**

  `git commit -m "feat(storage): model vpn peers and ip leases"`

### Task 3: `VPNPeerRepository`

**Files:** `internal/storage/vpn_peer_repository.go`、`internal/storage/db.go`（struct/构造/accessor）、
`internal/storage/vpn_repository_test.go`、`internal/storage/mysql_test.go`（接线已有门控测）

- [x] **Step 1: 先写失败测试**

  在 `vpn_repository_test.go` 新增 `runVPNPeerRepositoryContract(t *testing.T, db *DB)` 与
  `TestSQLiteVPNPeerRepositoryContract`（用 `newVPNTestDB(t)`，即 `OpenSQLite(ctx, "file:"+t.TempDir()+"/vpn.sqlite", true)`）。
  契约必须覆盖：

  1. **往返**：`Create` 后 `Get`/`GetByPublicKey`/`GetByNodeAndIP` 三个入口读回的字段与写入完全一致，
     包括可空的 `ExpiresAt`（nil 与非 nil 各一次）、四个密封私钥列（含「全空」与「全有」两种形态）、
     `AllowedIPs`/`AllowedPorts` 的字面量往返（验证 D5 的「不透明」承诺）。
  2. **ID 与时间戳**：`Create` 未提供 ID 时生成带 `vpn-peer` 前缀的 ID（照 `stamp` 范式）；
     `CreatedAt`/`UpdatedAt` 自动填充为 UTC。
  3. **唯一约束**：重复 `public_key` → `errors.Is(err, ErrVPNPeerConflict)`；
     同 `node_id` 重复 `vpn_ip` → `ErrVPNPeerConflict`；不同 `node_id` 相同 `vpn_ip` → 成功（D2 的作用域是节点内）。
  4. **`Update`**：改全部可变列（含轮换密钥四列与 `vpn_ip`）后读回一致；`UpdatedAt` 前进；
     对不存在的 ID → `sql.ErrNoRows`（经 `checkAffected`）。
  5. **`SetStatus` 终态**：`active → disabled → active` 允许；`active → revoked` 允许；
     对已 `revoked` 的 peer 再 `SetStatus(active)` → `errors.Is(err, ErrVPNPeerRevoked)`（D4）。
  6. **分页内权限过滤（Global Constraint 7）**：为 owner A 建 3 个 peer、owner B 建 3 个，
     `List(VPNPeerFilter{OwnerUserID:"A"}, "", 2)` → 2 条且全部属于 A、`HasMore=true`、`NextCursor!=""`；
     用 `NextCursor` 续页 → 第 3 条属于 A、`HasMore=false`；
     **全程不得出现 B 的任何一行**。再加一条：`List(VPNPeerFilter{}, "", 10)`（管理员视角）返回 6 条。
  7. **过滤组合**：`Status`、`NodeID`、`AgentID`、`Keyword` 各自单独生效，与 `OwnerUserID` 组合时是 AND 关系。
     `Keyword` 是对 `name` 与 `description` 的 `INSTR` 子串匹配，并断言超长关键字经 `boundedAgentFilter`
     截断后仍能安全执行；未知 `Status` 取值必须走 `1=0`（照 `credentialConditions` 的 default 分支），
     不得退化为「返回全部」。
  8. **`ListByNode`**：返回该节点全部非 `revoked` peer（`active` + `disabled`），`revoked` 不出现；
     按 `id` 稳定排序。
  9. **计数**：`CountByNode` 只数非 `revoked`；`CountByOwner` 数该 owner 全部（含 `revoked`，
     因为配额语义由阶段 4 决定，存储层只报告事实）——把这一取舍写进函数注释。

  同时在 `mysql_test.go` 的 `TestMySQLVPNRepositoryContract` 里调用
  `runVPNPeerRepositoryContract`（Task 4 会再追加 lease 部分）。

  Run: `go test ./internal/storage/ -run 'VPNPeerRepository' -count=1`
  Expected: **编译失败**，`db.VPNPeers undefined`。记录红灯原文。

- [x] **Step 2: 最小实现**

  `vpn_peer_repository.go` 照 `credential_repository.go` 结构实现：
  `vpnPeerColumns` 常量（21 列，顺序与 DDL 一致）、`vpnPeerRepo{db *sql.DB}`、
  `NewVPNPeerRepository(db *sql.DB) VPNPeerRepository`、`Create/Get/GetByPublicKey/GetByNodeAndIP/Update/
  SetStatus/List/ListByNode/CountByNode/CountByOwner`、`scanVPNPeer(scanner)`、`vpnPeerConditions(filter)`。
  要点：

  - 所有 `INSERT`/`UPDATE` 的重复键错误经 `isDuplicateError` 映射为 `ErrVPNPeerConflict`。
  - `List` 用 `pageArgs`+`decodeCursor`+`id>?`+`ORDER BY id LIMIT limit+1`+`encodeCursor`，
    与 `credentialRepo.List` 逐行同构；`Keyword` 用 `(INSTR(name,?)>0 OR INSTR(description,?)>0)`
    并经 `boundedAgentFilter` 限长（照 `credential_repository.go:152`），**不使用 `LIKE`**，
    因此与既有实体检索行为一致，也不存在 `%`/`_` 通配符逃逸问题。
  - `SetStatus` 的 SQL 必须把终态守卫写进 `WHERE`：
    `UPDATE vpn_peers SET status=?,updated_at=? WHERE id=? AND status<>?`（最后一个参数是 `revoked`），
    `RowsAffected==0` 时先 `Get` 判断是「不存在」还是「已吊销」，分别返回 `sql.ErrNoRows` 与 `ErrVPNPeerRevoked`。
  - `db.go` 三处接线（struct 字段、`newRepositories`、accessor）。

- [x] **Step 3: 跑绿**

  Run: `go test ./internal/storage/ -run 'VPNPeerRepository' -count=1` → Expected: `ok`
  Run: `go test ./internal/storage/ -count=1` → Expected: `ok`，无 FAIL
  Run: `go test ./internal/storage/ -run 'VPNPeerRepository' -race -count=1` → Expected: `ok`

- [x] **Step 4: 提交**

  `git commit -m "feat(storage): add vpn peer repository"`

### Task 4: `VPNIPLeaseRepository`

**Files:** `internal/storage/vpn_ip_lease_repository.go`、`internal/storage/db.go`（struct/构造/accessor）、
`internal/storage/vpn_repository_test.go`、`internal/storage/mysql_test.go`

- [x] **Step 1: 先写失败测试**

  在 `vpn_repository_test.go` 新增 `runVPNIPLeaseRepositoryContract(t, db)`、
  `TestSQLiteVPNIPLeaseRepositoryContract`、`TestSQLiteConcurrentVPNIPLeaseAcquireHasSingleOwner`。
  契约必须覆盖：

  1. **首次获取**：`AcquireSubnet` 对不存在的 `(node, subnet)` 插入行，`Epoch==1`、`AllocatedCount==0`、
     `LeaseHolder` 与 `LeaseExpiresAt` 按入参与 `TTL` 落库，`AcquiredAt`/`UpdatedAt` 为 UTC。
  2. **同持有者续约式重取**：同一 `LeaseHolder` 在未过期时再次 `AcquireSubnet` → 成功且 `Epoch` 递增
     （照 `leaseRepo.RegisterConnection` 的 `autoEpoch` 语义），`AllocatedCount` 保持不变（不得被清零）。
  3. **他人持有且未过期**：不同 `LeaseHolder` → `errors.Is(err, ErrVPNIPLeaseHeld)`，
     且原持有者的 `Epoch` 与 `LeaseExpiresAt` 未被改动。
  4. **过期接管**：把 `TTL` 设为 1ms 并 sleep 后，他人 `AcquireSubnet` → 成功、`Epoch==旧+1`、
     `LeaseHolder` 变更、`AllocatedCount` 保留（接管不得丢计数）。
  5. **epoch 围栏**：用旧 `Epoch` 调 `Renew`/`Release`/`AddAllocated` → `errors.Is(err, ErrVPNIPLeaseStaleEpoch)`；
     用当前 `Epoch` 调 → 成功。`Renew` 只延长 `LeaseExpiresAt` 与 `UpdatedAt`，不改 `Epoch`/`AllocatedCount`。
     `Release` 把 `LeaseExpiresAt` 置为过去时刻（使下一次 `AcquireSubnet` 可立即接管），不删行、不清计数。
  6. **`AddAllocated`（D8）**：`+1` 三次后 `Get` 得到 `AllocatedCount==3`；`-1` 一次得到 2；
     在 `AllocatedCount==0` 时 `-1` → 返回错误（不得变负）；
     holder 不匹配（伪造成别的持有者）→ `ErrVPNIPLeaseStaleEpoch`。
  7. **`ListByHolder`**：只返回该持有者的租约，按 `subnet` 稳定排序；不存在的持有者返回空切片而非 nil 错误。
  8. **并发单赢家**：8 个 goroutine 用**不同 holder** 对同一个已过期的 `(node, subnet)` 并发 `AcquireSubnet`
     → 恰好 1 个成功，其余 7 个得到 `ErrVPNIPLeaseHeld`；成功者的 `Epoch == 旧 Epoch + 1`
     （照 `sqlite_test.go:187` 的断言形状）。
  9. **唯一约束**：绕过 Repository 直接 `INSERT` 第二条相同 `(node_id, subnet)` 的行 → 数据库拒绝
     （验证 `UNIQUE(node_id, subnet)` 真的存在，而不只是 Repository 逻辑正确）。

  在 `mysql_test.go` 的 `TestMySQLVPNRepositoryContract` 追加 `runVPNIPLeaseRepositoryContract`。

  Run: `go test ./internal/storage/ -run 'VPNIPLease' -count=1`
  Expected: **编译失败**，`db.VPNIPLeases undefined`。记录红灯原文。

- [x] **Step 2: 最小实现**

  `vpn_ip_lease_repository.go` 照 `leaseRepo.RegisterConnection`（`repository.go:1878`）实现：
  `vpnIPLeaseColumns` 常量、`vpnIPLeaseRepo{db *sql.DB; driver string}`、
  `NewVPNIPLeaseRepository(db *sql.DB) VPNIPLeaseRepository`（内部经 `NewVPNIPLeaseRepositoryWithDriver(db, driver)`
  传播 driver，照 `NewClientInstanceRepositoryWithDriver` 的做法，使 MySQL 分支能加 `FOR UPDATE`）、
  `AcquireSubnet` 的 8 次重试 CAS 循环、`Renew`/`Release`/`AddAllocated` 的 epoch 条件更新、
  `Get`/`ListByHolder`、`scanVPNIPLease`。
  要点：

  - 事务内的 `SELECT` 仅在 `driver == DriverMySQL` 时追加 `FOR UPDATE`（SQLite 不支持，靠 CAS）。
  - 每次重试重新读取当前行，避免用过期的 `oldEpoch` 做 CAS。
  - `db.go` 三处接线；`newRepositories` 里用 `NewVPNIPLeaseRepositoryWithDriver(db, driver)`。

- [x] **Step 3: 跑绿**

  Run: `go test ./internal/storage/ -run 'VPNIPLease' -count=1` → Expected: `ok`
  Run: `go test ./internal/storage/ -run 'VPNIPLease' -race -count=1` → Expected: `ok`（并发单赢家测在 `-race` 下必须干净）
  Run: `go test ./internal/storage/ -count=1` → Expected: `ok`

- [x] **Step 4: 提交**

  `git commit -m "feat(storage): add vpn ip lease repository"`

### Task 5: 升级与回滚文档

**Files:** `docs/operations/schema-upgrades.md`、`docs/user-guide/server-admin.md`、`docs/deployment/binary-release.md`

- [x] **Step 1: `docs/operations/schema-upgrades.md` 新增 `## v14 to v15`**

  插在第 3 行 `## v13 to v14` 之前，结构照 v13→v14 章节，必须包含：

  1. 一句话说明 v15 是内嵌 VPN 网关的存储基础（两张新表、零既有表改动、expand-only），
     并链接 ADR 0002 与设计规格（在 `docs/operations/` 下写作 `../architecture/adr/0002-public-ingress-and-embedded-vpn.md`
     与 `../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md`）。
  2. 新表表格：`vpn_peers`（主键、`public_key VARCHAR(64) UNIQUE`、`UNIQUE(node_id, vpn_ip)`、
     四个密封私钥列、`allowed_ips`/`allowed_ports` 为不透明 TEXT、`status`、`expires_at`）与
     `vpn_ip_leases`（主键、`UNIQUE(node_id, subnet)`、`allocated_count`、`lease_holder`、
     `lease_expires_at`、`epoch`），并写明 `vpn_flows` 刻意不建表及其理由。
  3. 新索引清单（5 个）、「被索引时间戳用 `VARCHAR(32)` 以避免 MySQL `Error 1170`」的既有约定复述，
     以及**一段 MySQL 5.6 兼容性说明**：5.6 默认 Antelope + COMPACT + `innodb_large_prefix=OFF`，
     因此索引键上限 767 字节（最大者 `UNIQUE(node_id, subnet)` = 511）、变长列本地最多存 768 字节前缀
     使内联行上限为 8126 字节（`vpn_peers` 6,460、`vpn_ip_leases` 1,421）；
     这两项由 `TestVPNMigrationsFitMySQL56Budgets` 静态守护，并写明「后续新增变长列前必须重算预算」。
     同时说明 `private_key_nonce`/`private_key_key_id` 为何是 `VARCHAR(64)` 而不是既有表的 `TEXT`/`VARCHAR(128)`
     （行内预算，不是随意不一致），避免运维或后续贡献者把它当作笔误改回去。
  4. **升级前检查**：备份并验证可恢复；确认 `schema_meta.version=14`；确认
     `migrations/incremental/v0014_to_v0015/{mysql,sqlite}.sql` 存在且中间版本链完整；
     确认账号可 `CREATE TABLE`/`CREATE INDEX`/更新 `schema_meta`；
     **最低可升级源版本是 v14**（仓库只维护相邻增量，v13 及更早必须先升到 v14）。
     明确本版本**不需要**注入任何新环境变量：`TUNNELMESH_VPN_NODE_PRIVATE_KEY` 属阶段 6，
     `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 在 v15 仍只被既有身份/凭据特性使用。
  5. **预计锁表时间与容量影响**：只 `CREATE TABLE`/`CREATE INDEX` 两张空表，不触碰既有表，
     因此没有既有表的锁表与复制成本；MySQL 上耗时取决于实例 DDL 延迟，通常在秒级；
     新增存储开销为每 peer 一行（约 1 KB 量级）与每节点每子网一行租约。
     必须与 v13→v14 对照说明：那一版含 `ALTER TABLE users ADD COLUMN`，在 MySQL 5.6/5.7 上走 `INPLACE`
     但仍需重建表并阻塞并发 DML（本文档第 74 行已记录），**v15 没有任何 `ALTER`，因此不存在该成本**，
     这是把 VPN 存储独立成两张新表的运维收益之一。
  6. **灰度顺序**：先升级一个 Server 节点并观察 `schema_meta.version`、健康检查与关键链路
     （登录、Agent 注册、托管路由、`tp-*`），再滚动其余节点；
     由于 `db.go` 拒绝打开版本号高于二进制 `SchemaVersion` 的库，
     **这不是可以新旧混跑的滚动窗口**：一旦某节点把库迁到 v15，未升级的节点重启即失败，
     因此必须在同一维护窗口内完成全部节点升级（与 v13→v14 章节的结论一致）。
  7. **升级后校验**：`schema_meta.version=15`；两张表与 5 个索引存在；
     `requireSchemaTables` 通过（Server 能启动即证明）；健康检查与关键链路冒烟；
     升级日志不含 DSN、密码、Token。
  8. **回滚**：优先「回退应用 + 保留 v15 Schema」——两张新表对 v14 二进制不可见，
     v14 代码不读不写它们，因此**回退二进制即可，无需反向 DDL**；
     但 v14 二进制会因 `version > SchemaVersion` 拒绝启动，所以真正的回滚路径是
     **恢复升级前备份**，或前进修复。禁止手工 `UPDATE schema_meta SET version=14`
     （会留下 v15 表与 v14 版本号的不一致状态，并使下一次升级误判）。
     若必须在保留数据的前提下退回 v14，需要人工确认 `vpn_peers`/`vpn_ip_leases` 为空后再
     `DROP TABLE`（写清这是人工决策、不由应用自动执行）。
  9. **5 分钟止损**：本版本没有运行时开关（`server.vpn.*` 尚不存在），
     止损 = 停止升级、保留已迁移的库、回滚二进制到升级前版本并按第 8 条处理；
     因为迁移是 additive-only，未升级节点在窗口内继续服务不受影响。
  10. **失败重试**：MySQL DDL 隐式提交，脚本可能部分应用；
      `applySchemaStatements` 自 v6 起容忍重复对象，因此修复前置状态后重跑同一脚本是安全的；
      `schema_meta.version` 只在整段脚本成功后推进，失败时保持 14。

- [x] **Step 2: 同步另外两处版本陈述**

  - `docs/user-guide/server-admin.md:33`：「当前 Schema 版本为 v14」→「v15」。
  - `docs/deployment/binary-release.md:54`：「当前值为 13」→「当前值为 15」
    （前置核实结论 11：该值原本已滞后一位）。

- [x] **Step 3: 校验**

  Run: `rg -n 'v14|v15|当前值为' docs/operations/schema-upgrades.md docs/user-guide/server-admin.md docs/deployment/binary-release.md`
  Expected: 新版本陈述一致；`docs/operations/configuration.md:445` 等身份特性锚点未被改动
  （`git diff --name-only` 不含这些文件）。
  Run: `go test ./scripts/ -count=1`
  Expected: `ok`（阶段 1 的文档主张守卫测试仍然绿灯；本阶段不得重新引入被 ADR 0002 反转的主张）。

- [x] **Step 4: 提交**

  `git commit -m "docs(operations): document the v15 schema upgrade"`

### Task 6: 全量门禁、索引、PR 记录与提交

**Files:** `docs/pull-requests/2026-09-20-vpn-phase3-schema-v15.md`、四份生成的索引 README

- [x] **Step 1: 全量门禁**

```bash
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -timeout 30m -count=1
gofmt -l internal/storage migrations scripts
git diff --check
```

  Expected: 全部通过，`gofmt -l` 无输出。`go test ./...` 需要 `internal/server/web_dist` 存在；
  本阶段不改前端，若目录缺失先执行 `cd web && npm ci && npm run build && cd ..` 再跑。
  MySQL 侧测试必须如实记录为 SKIP（Global Constraint 12）。

- [x] **Step 2: 双方言一致性专项核验**

```bash
# 全量 DDL 建库与 v14→v15 增量升级必须得到同一 Schema（SQLite 侧已由 Task 1 的等价测覆盖）
go test ./internal/storage/ -run 'FreshSchemaMatchesIncrementalVPNChain|V14ToV15' -v -count=1
# 静态方言断言
go test ./internal/storage/ -run 'DialectSafe' -v -count=1
```

  Expected: PASS，且 `-v` 输出中 `TestMySQLV14ToV15VPNMigration`、`TestMySQLVPNRepositoryContract`
  为 `--- SKIP: TUNNELMESH_TEST_MYSQL_DSN is not set`。
  若运维提供了 MySQL DSN，补跑：
  `TUNNELMESH_TEST_MYSQL_DSN='user:pass@tcp(host:3306)/tm_test?parseTime=true' go test ./internal/storage/ -run 'MySQL' -count=1`
  并把真实输出写进 PR 记录。

- [x] **Step 3: 写 PR 记录**

  新增 `docs/pull-requests/2026-09-20-vpn-phase3-schema-v15.md`，覆盖 `AGENTS.md` 要求的全部小节
  （标题、目标分支、摘要、用户影响、API/Schema/配置影响、安全与授权影响、测试证据、发布步骤、
  回滚步骤、Reviewer 关注点、集成状态），正文必须链接本计划、设计规格与 ADR 0002（否则索引无法建立关联）。
  测试证据必须写明：每个任务的红灯原文（`undefined: migrations.V14ToV15MySQL` 等）、
  绿灯输出、SKIP 项与原因、以及 `SchemaVersion` 14→15 的断言更新点。

- [x] **Step 4: 重新生成索引并确认幂等**

  Run: `python3 scripts/gen_doc_index.py && python3 scripts/gen_doc_index.py && git status --porcelain`
  Expected: 第一次生成后 `docs/pull-requests/README.md`（共 21 份记录）、
  `docs/superpowers/plans/README.md`、`docs/superpowers/specs/README.md` 出现关联；第二次无新 diff。

- [x] **Step 5: 时点记录零改写核验**

```bash
BASE=codex/vpn-phase1-constraint-reversal
git diff --name-status "$BASE"..HEAD -- docs/superpowers docs/pull-requests docs/architecture/adr
```

  Expected: 只出现 `A`（本计划、本阶段 PR 记录）与生成索引的 `M`；
  **不得出现任何既有 spec/plan/PR 记录或已接受 ADR 被 `M`**。

- [x] **Step 6: 提交并（获授权后）推送**

```bash
git add docs/pull-requests/2026-09-20-vpn-phase3-schema-v15.md docs/pull-requests/README.md \
        docs/superpowers/plans/README.md docs/superpowers/specs/README.md \
        docs/superpowers/plans/2026-09-20-vpn-phase3-schema-v15.md
git commit -m "docs(pull-requests): record vpn schema v15"
git push -u origin codex/vpn-phase3-schema-v15
```

## 任务间接口

- **Task 1 → Task 2-6**：`SchemaVersion = 15`、`migrations.V14ToV15{MySQL,SQLite}`、
  `requireSchemaTables` 中的两个表名，是后续所有测试能建出库的前提；
  表名或列名一旦改动，Task 3/4 的 SQL 与 Task 1 的等价测必须同步。
- **Task 2 → Task 3/4**：`VPNPeer`/`VPNIPLease` 字段名与四个 sentinel 错误是 Repository 的契约；
  阶段 4 的 Service 只依赖这两个模型与接口，因此字段增删必须回改本计划并重新评审。
- **Task 3 → Task 4**：两个 Repository 各自独立文件、独立测试函数，写集合不重叠，可并行实现；
  但都改 `db.go` 的同一处 struct/`newRepositories`/accessor 区域，串行提交以避免冲突。
- **Task 1/3/4 → `mysql_test.go`**：`TestMySQLVPNRepositoryContract` 在 Task 1 建立骨架、
  Task 3 追加 peer 契约、Task 4 追加 lease 契约；三次改动都只在该函数体内追加一行调用。
- **Task 5 ← Task 1**：升级文档里的表结构、索引名、最低源版本必须与 Task 1 落地的 DDL 逐字一致；
  文档不得出现 DDL 里不存在的列或索引。
- **Task 6 ← 全部**：门禁全绿、索引幂等、时点记录零改写、红灯证据齐全是本阶段收口条件。

## 整体验证

```bash
# 阶段门禁
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -timeout 30m -count=1
gofmt -l internal/storage migrations scripts
git diff --check

# 存储层专项
go test ./internal/storage/ -count=1
go test ./internal/storage/ -run 'VPN' -v -count=1
go test ./internal/storage/ -run 'VPN' -race -count=1

# 阶段 1 的文档主张守卫必须仍然绿灯
go test ./scripts/ -count=1

# 文档门禁
python3 scripts/gen_doc_index.py
git diff --name-status codex/vpn-phase1-constraint-reversal..HEAD -- docs/superpowers docs/pull-requests docs/architecture/adr

# 依赖零改动核验
git status --porcelain go.mod go.sum        # 必须为空
```

前端未改动，`npm test -- --run` 与 `npm run build` 不是本阶段门禁（仅当 `internal/server/web_dist`
缺失时执行 `npm run build` 以满足 embed 断言）。无 Docker/Compose 改动，容器验证不适用。

## 回滚注意事项

- 本阶段只新增两张空表、两个 Repository 文件与文档，`git revert` 对应提交即可回滚代码；
  数据库侧**不需要反向 DDL**：v15 的两张表对 v14 二进制不可见，但 v14 二进制会因
  `version > SchemaVersion` 拒绝打开已迁移的库，因此真正的回滚是**恢复升级前备份**（见 Task 5 第 8 条）。
- 已发布的增量脚本不可修改：若 v15 的 DDL 需要修正，必须新增 `v0015_to_v0016`，
  不得回改 `v0014_to_v0015`（`AGENTS.md:53`）。
- 回滚 Task 1 而不回滚 Task 3/4 会留下引用不存在表的 Repository，编译能过但运行时必然报错；
  因此回滚必须以任务边界为单位成组进行（Task 3/4 → Task 2 → Task 1）。
- 阶段 4-8 全部以 v15 的两张表为前提：回滚 Schema 即冻结 VPN 特性的后续所有阶段，
  但不影响任何既有能力（本阶段零既有表改动、零配置、零 API、零前端）。
- `SchemaVersion` 一旦被发布使用就不得回退编号；若本阶段未合并即废弃，
  后续特性仍应从 14 起算，不得占用 15（与 ADR 编号永不复用同理）。
