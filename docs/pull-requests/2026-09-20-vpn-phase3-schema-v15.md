# VPN 网关 阶段 3：Schema v15 存储层

## 标题

`feat(storage): land schema v15 and the vpn repositories`

本记录覆盖分支上阶段 3 的全部提交（计划 → v15 迁移链 → 模型与 sentinel → `VPNPeerRepository` →
`VPNIPLeaseRepository` → 升级文档 → 收口），而不只是最后一个提交。

## 目标分支

`main`

本分支 stacked 在 `codex/vpn-phase1-constraint-reversal` 之上（基点 `232338c`）。阶段 1 的 PR 合并进
`main` 后，必须先 `git rebase --onto main codex/vpn-phase1-constraint-reversal` 再更新本 PR，
否则 diff 会重复包含阶段 1 的产物。

## 关联记录

- 实施计划：[VPN 网关 阶段 3：Schema v15 Implementation Plan](../superpowers/plans/2026-09-20-vpn-phase3-schema-v15.md)
- 设计规格：[内嵌 VPN 网关（WireGuard）设计](../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md)（§7 存储、§7.1 `vpn_flows` 不建表、§15 阶段 3）
- 决策载体：[ADR 0002: Open a public UDP ingress for an embedded VPN gateway](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)（`Status: Accepted`）
- 前置阶段：[VPN 网关 阶段 1：约束反转与 ADR 0002](2026-09-20-vpn-phase1-constraint-reversal.md)
- 运维文档：[Schema Upgrade Guide § v14 to v15](../operations/schema-upgrades.md#v14-to-v15)

## 摘要

阶段 3 交付内嵌 VPN 网关的**存储基础**：Schema v15 的两张新表、两个 Repository、以及支撑二者的
迁移链、双方言门禁和升级/回滚文档。**本阶段不写一行数据面代码**：没有 `internal/vpn/`、没有管理 API、
没有配置项、没有前端改动、没有新增 Go 依赖（`go.mod`/`go.sum` 零改动）。

五类产物：

1. **Schema v15 迁移链**：`migrations/ddl.sql` 新增两张表与 5 个索引；
   `migrations/incremental/v0014_to_v0015/{mysql,sqlite}.sql` 是相邻增量脚本（表语句逐字相同，
   只有索引行按方言区分 `CREATE INDEX` 与 `CREATE INDEX IF NOT EXISTS`）；`migrations/embed.go`
   新增两个 embed；`internal/storage/db.go` 只改三处——`SchemaVersion` 14 → 15、迁移 switch 新增
   `case 14`、`requireSchemaTables` 追加两个表名。
2. **`vpn_peers`**：一个 WireGuard peer 一行。`public_key VARCHAR(64) NOT NULL UNIQUE` 是握手身份；
   `UNIQUE(node_id, vpn_ip)` 让「地址唯一」的作用域是**节点内**而不是全局，因此每个节点能从共享
   `ip_pool` 里各自切 `/24` 而不必协调地址；四个密封私钥列（`private_key_ciphertext`/`_nonce`/
   `_key_id`/`_version`）与 `credentials.secret_*` 同构，只存密文；`allowed_ips`/`allowed_ports`
   是 `internal/vpn` 拥有的**不透明文本**，存储层从不解析，因此编码只有一个所有者；
   `status` 是 `active`/`disabled`/`revoked` 闭集，`revoked` 是终态且**没有 Delete 路径**。
3. **`vpn_ip_leases`**：一个节点对一个 `/24` 的持有一行。`UNIQUE(node_id, subnet)` 是「一个子网一个
   持有者」的权威事实；`epoch` 围栏让失去租约的节点在 `Renew`/`Release`/`AddAllocated` 上自动失效，
   不需要任何人去通知它；`allocated_count` 只是**派生计数器**（指标与快速耗尽检查用），
   「某个 /32 已被占用」的权威事实是 `vpn_peers.UNIQUE(node_id, vpn_ip)`，所以计数器丢失永远不会
   把同一个地址发给两个 peer。**刻意不建 `vpn_flows` 表**：运行时流状态不入库、不进高基数指标。
4. **两个 Repository**：`VPNPeerRepository`（`internal/storage/vpn_peer_repository.go`，315 行）与
   `VPNIPLeaseRepository`（`internal/storage/vpn_ip_lease_repository.go`，365 行），照
   `credential_repository.go` 与 `leaseRepo.RegisterConnection` 的既有范式实现，经 `db.go` 的
   struct/构造/accessor 三处接线暴露为 `db.VPNPeers()` 与 `db.VPNIPLeases()`。
5. **MySQL 5.6 预算门禁与升级文档**：`TestVPNMigrationsFitMySQL56Budgets` 从 DDL 文本重算行内尺寸与
   索引键宽（**不需要 MySQL 实例**，因此在本机与 CI 都是真门禁）；
   `docs/operations/schema-upgrades.md` 新增 215 行 `## v14 to v15` 章节。

## 用户影响

- **运行时零影响。** 没有 API、配置项、前端或部署产物变更；两张新表在阶段 4 之前一直是空的，
  没有任何代码路径读写它们（只有 `requireSchemaTables` 在启动时确认它们存在）。
  已部署的 Server / Agent / Client 行为完全不变。
- **升级需要一次全量重启，不是滚动窗口。** `internal/storage/db.go` 拒绝打开 `schema_meta.version`
  高于二进制 `SchemaVersion` 的库，因此一旦任一节点把库迁到 v15，未升级节点重启即失败。
  这与 v13→v14 的结论一致，已写进升级文档。
- **升级本身很便宜。** 本版只 `CREATE TABLE`/`CREATE INDEX` 两张空表，**没有任何 `ALTER`**，
  因此不存在 v13→v14 那次 `ALTER TABLE users ADD COLUMN` 在 MySQL 5.6/5.7 上的 `INPLACE` 表重建
  与并发 DML 阻塞成本。这正是把 VPN 存储独立成两张新表、而不是扩既有表的运维收益。
- **不需要注入任何新环境变量。** `TUNNELMESH_VPN_NODE_PRIVATE_KEY` 属阶段 6、当前不存在；
  `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 保持 v14 语义，v15 没有引入任何新的密钥读取方。
- 对贡献者的影响：`go test ./internal/storage/` 多两组门禁——双方言静态断言与 MySQL 5.6 预算核算。
  把 `private_key_nonce` 之类的列「规范化」回 `TEXT` 会直接红灯，并打印实际字节数与超限索引名。

## API、Schema 与配置影响

- **API**：无。`docs/api/openapi.yaml` 未改动。规格 §9 的 VPN 管理接口属阶段 4/6。
- **Schema**：`SchemaVersion` 14 → 15；新增 `vpn_peers`（22 列）与 `vpn_ip_leases`（9 列）两张表、
  5 个 `idx_vpn_*` 索引、2 个表内 `UNIQUE` 约束与 2 个列内 `UNIQUE`（`vpn_peers.public_key`、
  两张表的主键）。**零既有表改动**，因此是 expand-only。
  最低可升级源版本是 **v14**（仓库只维护相邻增量，v13 及更早必须先升到 v14）。
  `migrations/ddl.sql` 与 `v0014_to_v0015` 的表语句逐字一致，由
  `TestMySQLV14ToV15VPNMigrationsAreAdjacentAndDialectSafe` 与
  `TestSQLiteFreshSchemaMatchesIncrementalVPNChain` 双向守护。
- **配置**：无。`internal/config/` 未改动，`server.vpn.*` 尚不存在。
- **依赖**：无。`go.mod`/`go.sum` 零改动；`gvisor.dev/gvisor` 与 `golang.zx2c4.com/wireguard-go`
  由阶段 6（`//go:build vpn` 数据面）引入，`Dockerfile` 的工具链抬升也属阶段 6。
- **前端**：无。`web/` 零 diff，`internal/server/web_dist` 已存在，embed 断言可跑。

## 安全与授权影响

- **私钥永不落明文。** `vpn_peers` 只存 `auth.SecretStore` 密封后的密文、nonce、key id 与版本，
  与 `credentials.secret_*`、`user_mfa.secret_*` 同一套约定。`validateVPNPeer` 强制密文与 nonce
  **同时存在或同时为空**：只有密文没有 nonce 的 AES-GCM 数据永远打不开，落库就会造出一个配置不可恢复
  的 peer。key id 保持可选，因为 `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID` 本身就是可选的。
- **权限过滤在分页语义内完成。** `VPNPeerFilter.OwnerUserID` 是 `WHERE` 条件之一，
  不是查完再丢弃；契约测试用「owner A 3 行 + owner B 3 行、每页 2 条、逐页抽干」断言 A 的任何一页
  都不出现 B 的行（`AGENTS.md`「用户资源查询必须在分页语义内完成权限过滤」）。
- **未知过滤值收窄而不是放宽。** `VPNPeerFilter.Status` 取到闭集之外的值时条件是 `1=0`，
  与 `credentialConditions` 的 default 分支一致；退化成「返回全部」会让一个拼写错误变成跨租户读取。
  关键字检索用 `INSTR` 而非 `LIKE`，并经 `boundedAgentFilter` 限长 128 字节，
  因此既没有通配符逃逸，也没有超长过滤值撑爆查询。
- **吊销是终态且不可绕过。** `SetStatus` 把 `status<>'revoked'` 写进 `WHERE`，
  而不是先读后写，因此两个并发请求不可能把已吊销的 peer 竞态回 `active`；
  `RowsAffected==0` 时再读一次以区分「不存在」（`sql.ErrNoRows`）与「已吊销」（`ErrVPNPeerRevoked`）。
  Repository 没有 `Delete`：退役的公钥与它占过的地址保持可审计，而不是被静默释放复用。
- **子网租约靠 epoch 围栏，不靠进程内锁。** `AcquireSubnet` 是 8 次重试的 compare-and-set，
  MySQL 分支额外加 `SELECT ... FOR UPDATE`；接管只在租约过期后允许，且 `UPDATE ... AND epoch=?`
  保证并发接管恰好一个赢家（`TestSQLiteConcurrentVPNIPLeaseAcquireHasSingleOwner`：8 个不同 holder
  并发抢同一个已过期子网 → 1 个成功、7 个 `ErrVPNIPLeaseHeld`、赢家 `Epoch == 旧 Epoch + 1`）。
  这满足 `AGENTS.md`「租约获取和 epoch fencing 必须由存储层或数据库约束保证，不能只依赖进程内锁」。
- **计数器不能变负。** `AddAllocated` 在事务内读锁定行后再算，`next < 0` 直接报错而不是写库；
  否则一个子网会看起来有它并没有的容量，而这正是一个地址被配到两个 peer 的成因。
- **不实现的能力仍然不实现。** 本阶段没有 ICMP、没有 TUN/L2、没有 P2P NAT traversal、
  没有任意远程命令执行；`icmp_enabled` 只是一个持久化的布尔策略位，语义由阶段 6/7 落地。
- 日志与错误文本不含 DSN、密码、Token 或私钥；新增的错误信息只带表名、子网、节点与字节数。

## 测试证据

TDD 全程实测，每个任务的红灯原文如下（不是估算，是当时终端输出的首行）：

| 提交 | 任务 | 红灯原文（首行） |
| --- | --- | --- |
| `9e5c4e0` | T1 迁移链 | `internal/storage/mysql_test.go:422:24: undefined: migrations.V14ToV15MySQL` |
| `72c2a26` | T2 模型与 sentinel | `internal/storage/vpn_repository_test.go:294:23: undefined: VPNPeer` |
| `2c00bf5` | T3 `VPNPeerRepository` | `internal/storage/vpn_repository_test.go:457:21: db.VPNPeers undefined (type *DB has no field or method VPNPeers)` |
| `9ccf5b8` | T4 `VPNIPLeaseRepository` | `internal/storage/vpn_repository_test.go:974:15: db.VPNIPLeases undefined (type *DB has no field or method VPNIPLeases)` |

T1 的红灯同时暴露了一个真实的编译错误（`cannot convert float64(...) (constant 1218.9) to type int`，
常量表达式截断），已在实现前修掉——红灯基线本身也必须是「只因缺少实现而失败」。

绿灯与门禁（全部实测）：

- 仓库门禁：`go build ./...` 通过；`go vet ./...` 干净；`go test ./... -count=1` **22 个包全 `ok`、
  5 个包 `no test files`、0 个 `FAIL`**（最慢 `internal/server` 36.974s、`internal/storage` 2.830s）；
  `go test -race ./... -timeout 30m -count=1` 全绿，无 `DATA RACE`、无 `FAIL`；
  `gofmt -l internal/storage migrations scripts` 无输出；`git diff --check` 干净；
  `git status --porcelain go.mod go.sum` 为空（依赖零改动）。
- 阶段 1 的文档主张守卫仍然绿灯：`go test ./scripts/ -count=1` → `ok`（本阶段没有重新引入被
  ADR 0002 反转的主张）。
- 新增测试：`internal/storage/vpn_repository_test.go`（1355 行）与 `internal/storage/mysql_test.go`
  新增 426 行。覆盖迁移与等价性（3 个 SQLite 测 + 1 个 DSN 门控 MySQL 测）、双方言静态断言、
  MySQL 5.6 预算核算、模型校验与 sentinel 唯一性、peer 契约（8 个子测试）、lease 契约
  （8 个子测试）与并发单赢家。
- MySQL 5.6 预算实测值（`-v` 输出，两份 DDL 来源各一遍，数值一致）：
  `vpn_peers inline=6460/8126 bytes (headroom 1666), largest index key idx_vpn_peers_owner=510/767`；
  `vpn_ip_leases inline=1421/8126 bytes (headroom 6705), largest index key UNIQUE(node_id, subnet)=511/767`。
  与计划预算表逐项吻合。
- **MySQL 侧测试如实记录为 SKIP**（本机无 MySQL：无 docker/mysqld，`TUNNELMESH_TEST_MYSQL_DSN` 未设置）。
  `go test ./internal/storage/ -run 'MySQL' -v -count=1` 实测：13 个 PASS、7 个 SKIP，
  SKIP 原因文本均为 `TUNNELMESH_TEST_MYSQL_DSN is not set`，其中本阶段新增的两个是
  `TestMySQLV14ToV15VPNMigration` 与 `TestMySQLVPNRepositoryContract`。
  **因此 v15 的 DDL 从未在真实 MySQL 上执行过**；MySQL 侧的保证全部来自静态断言
  （方言片段、`Error 1170` 规避、5.6 行内与索引键预算、`;` 切分后无纯注释片段）。
  运维提供 DSN 后应补跑
  `TUNNELMESH_TEST_MYSQL_DSN='user:pass@tcp(host:3306)/tm_test?parseTime=true' go test ./internal/storage/ -run 'MySQL' -count=1`
  并把真实输出追加到本记录。
- **变异校验（证明契约测试不是空跑）**：对两个 Repository 各做了一次故意的错误实现并复跑——
  把 `vpnPeerConditions` 的未知 status 分支从 `1=0` 改成 `1=1`、把 `ListByNode` 的 `AND status<>'revoked'`
  去掉 → `unknown status returned unexpected row vpn-peer-filter-1` 与
  `ListByNode order = [vpn-peer-list-1 vpn-peer-list-2 vpn-peer-list-3], want [...-1 ...-2]` 双双红灯；
  把接管 `UPDATE` 的 `AND epoch=?` 去掉、把「他人持有且未过期」检查短路 →
  `successful holders = 8, want exactly one` 与 `takeover of a live lease err = <nil>, want ErrVPNIPLeaseHeld`
  双双红灯。两处变异均已还原，还原后复跑全绿。
- 未执行的验证与原因：前端未改动 → `npm test -- --run` 与 `npm run build` 不适用；
  无 Docker/Compose 改动 → 容器验证不适用；真实 MySQL 5.6 实例上的 DDL 执行 → 见上一条 SKIP 说明。

## 与计划的偏差（全部为收紧或事实更正，无功能缩水）

1. **`TestMySQLVPNRepositoryContract` 建在 T3 而不是 T1。** 计划把它列进 T1 Step 1，但它要调用
   T3 才存在的 `runVPNPeerRepositoryContract`；在 T1 建它会让 T1 的红灯多一个与迁移无关的编译错误，
   污染红灯基线。改为 T3 建骨架 + peer 契约、T4 追加 lease 契约。
2. **计划 L380 的 `vpn_ip_leases` 内联字节数 1,405 是笔误，实测且与 DDL 一致的值是 1,421**
   （计划 L371 表格、L543 与 L772 本来就是 1,421）。已随本记录修正计划正文。
3. **计划说 `vpnPeerColumns` 是 21 列，`vpn_peers` 实际是 22 列**（`created_at` 与 `updated_at` 是两列）。
   实现按 DDL 的 22 列写，`VALUES` 占位符 22 个，契约测试的逐列往返断言覆盖全部 22 列。
4. **计划说「五个宽度约定」但只列了四个片段。** 补齐第五个 `lease_expires_at VARCHAR(32) NOT NULL`
   （另一个被索引的时间戳），使断言与计数一致。
5. **`NewVPNIPLeaseRepository(db)` 保留但标记 Deprecated，`db.go` 用
   `NewVPNIPLeaseRepositoryWithDriver(db, driver)` 接线。** 无 driver 的构造器不可能知道方言，
   默认成 SQLite 会在 MySQL 上静默丢掉 `FOR UPDATE` 行锁。做法与既有
   `NewLeaseRepository`/`NewLeaseRepositoryWithDriver` 完全一致。
6. **过期比较放在 Go 里，不放在 SQL 里**（`tryAcquireSubnet` 用
   `current.LeaseExpiresAt.After(now)`，而不是照 `leaseRepo` 追加 `AND expires_at<=?`）。
   原因是实测确认的事实：`time.RFC3339Nano` 会裁掉小数尾零，因此**存库后的时间戳字符串不是可靠有序的**。
   实测（`base = 2026-09-21T00:44:52Z`）：`52Z`、`52.1Z`、`52.15Z`、`52.9Z`、`53.5Z` 按字典序排成
   `52.15Z < 52.1Z < 52.9Z < 52Z < 53.5Z`，与时序不符（`52Z` 排到了 `52.9Z` 之后、`52.15Z` 排到了
   `52.1Z` 之前）。epoch 的 compare-and-set 已经足以保证唯一赢家，因此不需要那层 SQL 守卫。
7. **`Renew`/`Release`/`AddAllocated` 只由 `(node_id, subnet, lease_holder, epoch)` 围栏，
   不再叠加 `leaseRepo.RenewConnection` 那样的 `expires_at>now` 条件。** epoch 围栏已经是权威：
   租约一旦被他人接管，epoch 就前进，旧节点的三个写操作全部得到 `ErrVPNIPLeaseStaleEpoch`。
   少一个条件就少一类需要区分的失败，因此不需要引入第五个 sentinel。
8. **`AcquireSubnet` 忽略调用方传入的 `AllocatedCount`，新子网一律从 0 开始。**
   一个从未被租出的子网不可能已经发出过地址；计数器只能由 `AddAllocated` 推进。
9. **新增两个计划未逐条列举的断言/规则**，都由计划的既有意图直接推出：
   `TestVPNSentinelErrorsAreDistinct`（计划要求「四个 sentinel 互不相等且 `errors.Is` 只匹配自身」，
   独立成测比塞进模型测更清晰）；`validateVPNPeer` 的「密文与 nonce 同时存在或同时为空」
   （计划 T3 契约明确只测私钥四列的「全空」与「全有」两种形态）。

## 顺带发现、本阶段刻意不修的既有问题

按「只修根因、不顺手改无关代码」的纪律，以下三项只记录不修改：

1. **`leaseRepo` 与若干统计查询在 SQL 里直接比较 `VARCHAR` 时间戳**
   （`internal/storage/repository.go:1969` 的接管守卫 `AND expires_at<=?`、
   `:2033` 的 `RenewConnection`、`:2057` 的 `ListActiveByAgent`、`:2079` 的 `UpdateConnectionStats`、
   `:1550` 的按节点聚合），受上文偏差 6 的实测结论影响，在小数秒尾零不同的两个时刻之间可能判错方向。
   既有测试没有覆盖到该组合。修它需要独立计划（会改变既有租约语义），不属于阶段 3。
2. **`oidc_providers` 的内联行尺寸按同一模型测得 12,439 字节，超过 MySQL 5.6 的 8126 字节上限。**
   `TestVPNMigrationsFitMySQL56Budgets` **只覆盖 v15 的两张新表**，不追溯既有表，
   因此这条既有风险不会因为本阶段而变绿或变红。
3. **`TestSQLiteFreshSchemaMatchesIncrementalIdentityChain`（`internal/storage/sqlite_test.go:763`）
   从未真正执行增量链**，因此它并不能发现 v14 的增量脚本与全量 DDL 漂移。
   本阶段新增的 `TestSQLiteFreshSchemaMatchesIncrementalVPNChain` 没有复制该缺陷
   （它显式调用 `OpenSQLite(ctx, upgraded, true)` 跑完迁移再比对列与索引），但既有测试原样保留。

## 发布步骤

1. 合并前完成 PR 复审；核心模块（`internal/storage`）改动需独立复审。
2. **备份数据库并验证备份可恢复。** 本次升级不能靠回退应用撤销，见「回滚步骤」。
3. 确认 `schema_meta.version=14`；确认 `migrations/incremental/v0014_to_v0015/{mysql,sqlite}.sql`
   与从当前版本起的每一个相邻增量目录都在；确认账号可 `CREATE TABLE`/`CREATE INDEX`/更新 `schema_meta`。
4. 以 `storage.auto_init: true` 启动新版 Server。迁移成功后 `schema_meta.version` 才推进到 `15`。
5. **在同一维护窗口内完成全部节点升级**：先升一个节点，观察 `schema_meta.version`、健康检查与关键链路
   （登录、Agent 注册、托管路由、`tp-*`），再滚动其余节点。不要把未升级节点指向已迁移的库。
6. 升级后校验：`SELECT version FROM schema_meta WHERE id=1` 得 `15`；两张表与 5 个 `idx_vpn_*` 索引存在；
   两张表 `COUNT(*)` 为 0；Server 能启动即证明 `requireSchemaTables` 通过；升级日志不含 DSN、密码、Token。
7. 本版**不需要**注入任何新环境变量，也不需要改 `Dockerfile`、`deploy/` 或前端产物。
8. 完整的表结构、索引清单、5.6 预算、锁表影响与灰度顺序见
   [Schema Upgrade Guide § v14 to v15](../operations/schema-upgrades.md#v14-to-v15)。

## 回滚步骤

- **代码侧**：本阶段只新增两张空表、两个 Repository 文件与文档，`git revert` 对应提交即可。
  回滚必须以任务边界为单位成组进行（T4 → T3 → T2 → T1）：只回滚 T1 而留下 T3/T4 会留下引用
  不存在表的 Repository，编译能过但运行时必然报错。
- **数据库侧不需要反向 DDL**：v15 的两张表对 v14 二进制不可见，v14 代码不读不写它们。
  但这不等于降级免费——v14 二进制仍会因 `version > SchemaVersion` 拒绝打开已迁移的库。
  受支持的路径只有两条：**恢复升级前备份**，或在 v15 上**前进修复**（问题在应用代码而非迁移时优先）。
- **禁止手工 `UPDATE schema_meta SET version=14`**：那会留下「v15 的表 + v14 的版本号」的不一致状态，
  使下一次升级误判，并且恰好废掉那条让失败可被发现的版本门禁。
- 若必须在保留生产数据的前提下退回 v14，需运维**人工确认两张表都为空**后再 `DROP TABLE`。
  这是人工决策、不由应用自动执行；删掉非空的 `vpn_peers` 会销毁没有任何其他表能重建的 peer 记录。
- **5 分钟止损**：本版没有运行时开关（`server.vpn.*` 尚不存在），因此没有「关掉特性」这个选项。
  止损 = 停止升级、保留已迁移的库、把二进制回滚到升级前版本，再按上一条处理。
  因为迁移是 additive-only，窗口内**未被触碰的节点持续正常服务**；部分升级的集群只要没有未升级节点
  重启就仍然是可用的。
- **升级失败重试**：MySQL DDL 隐式提交，一次中断可能只建了一张表。`applySchemaStatements` 自 v6 起
  容忍重复对象，因此排除阻塞原因（几乎都是缺 `CREATE` 权限或同名对象已存在）后重跑同一脚本是安全的；
  `schema_meta.version` 只在整段脚本成功后推进，失败时保持 `14`。
  **不得为了让升级通过而修改已发布的增量脚本**——修复属于下一个 Schema 版本（`v0015_to_v0016`）。
- `SchemaVersion` 一旦被发布使用就不得回退编号。若本阶段未合并即废弃，后续特性仍应从 14 起算，
  不得占用 15（与 ADR 编号永不复用同理）。

## Reviewer 关注点

1. **列宽是不是被 5.6 预算绑架了？** `private_key_nonce VARCHAR(64)` 与
   `private_key_key_id VARCHAR(64)` 比 `user_mfa`/`auth_challenges`/`oidc_providers` 的同类列窄
   （那边是 `TEXT` 与 `VARCHAR(128)`）。这是行内预算决策而不是笔误：一个 AES-GCM nonce 是 12 字节
   （16 个 base64 字符），`TEXT` 会占 788 字节而不是 256 字节。升级文档里已写明理由与守卫测试名，
   请确认这个说明足以阻止后来者「顺手统一」。
2. **`vpn_peers` 内联 6460/8126 字节，余量 1666 字节（20%）。** 门禁要求余量 ≥15%，
   也就是最多还能加约 1218 字节的变长列。阶段 4+ 若要加列，请先重算；
   `TestVPNMigrationsFitMySQL56Budgets` 会在超标时打印实际字节数与超限索引名。
3. **`SetStatus` 的 `RowsAffected==0` 三态解析**：不存在 → `sql.ErrNoRows`；已吊销 →
   `ErrVPNPeerRevoked`；行存在且状态已等于目标（MySQL 报告 0 changed rows）→ `nil`。
   请确认第三态返回 `nil` 而不是错误是可接受的幂等语义。
4. **`Release` 之后原持有者仍是 holder of record**，直到有人接管把 epoch 推进，
   因此一个在途的 `AddAllocated` 仍可能落库。这是有意接受的窗口：防止一个地址被发给两个 peer 的
   权威约束是 `vpn_peers.UNIQUE(node_id, vpn_ip)`，不是这个计数器。函数注释里写了同样的话，
   请确认这个取舍在阶段 4 的分配逻辑里仍然成立。
5. **`AcquireSubnet` 的接管继承 `allocated_count`**（不清零）。理由：子网上已配置的 peer 还连着，
   新节点必须看得见它们，否则会把已占用的地址当空闲发出去。请确认阶段 4 的对账逻辑与此一致。
6. **`List` 的 cursor 是 `id>?` + `ORDER BY id`**，与 `credentialRepo.List` 逐行同构。
   `id` 是 `VARBINARY(255)`，两种方言都按字节序排，因此分页顺序在 SQLite 与 MySQL 上一致。
7. **本阶段没有 Service 层**：Repository 直接被契约测试驱动。阶段 4 的 Service 只依赖
   `VPNPeer`/`VPNIPLease` 两个模型、两个接口与四个 sentinel，因此**字段增删必须回改计划并重新评审**。
8. **`CountByOwner` 计入已吊销的 peer**，`CountByNode` 不计。这个不对称是有意的：
   「吊销是否仍占配额」是阶段 4 的产品决策，存储层只报告事实。请确认这个边界划得对。
9. 时点记录零改写：`git diff --name-status codex/vpn-phase1-constraint-reversal..HEAD --
   docs/superpowers docs/pull-requests docs/architecture/adr` 只出现本阶段计划与本记录的 `A`、
   生成索引的 `M`，以及本阶段计划自身的 `M`（勾选任务清单 + 修正 L380 的 1,405 笔误）；
   **没有任何既有 spec/plan/PR 记录或已接受 ADR 被修改**。

## 集成状态

阶段 3 的 6 个任务已在 `codex/vpn-phase3-schema-v15` 上完成并推送：Schema v15 迁移链、模型与
sentinel、`VPNPeerRepository`、`VPNIPLeaseRepository`、升级/回滚文档、全量门禁与本记录。
仓库门禁全绿，两个 Repository 的契约均经变异校验；MySQL 侧测试因本机无实例如实 SKIP。

阶段 4-8 全部以 v15 的两张表为前提：`internal/vpn/` 纯逻辑与管理 API（阶段 4）、管理后台（阶段 5）、
Server 数据面（阶段 6）、Agent ICMP 扩展（阶段 7）、可观测性与部署（阶段 8）。
回滚本阶段即冻结 VPN 特性的全部后续阶段，但不影响任何既有能力
（本阶段零既有表改动、零配置、零 API、零前端、零依赖）。
PR 复审与合并待进行；合并顺序上必须在阶段 1 之后。
