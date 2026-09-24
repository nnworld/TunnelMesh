# 阶段 6 第二轮合并 main 与未合并部分审查

- **状态**：已实施（本分支），等待 review
- **类型**：审查修复 / 文档口径统一 / 脱敏
- **补记说明**：本文同时承担「补记计划」职责。修复在审查发现后直接实施，
  因此下述内容只记录已验证事实，不含任何未执行的预期。

## 目标分支

`main`（源分支 `codex/vpn-phase6-server-data-plane`，基线 `b76352d`）。
关联计划：本分支的 [阶段 6 实施计划](../superpowers/plans/2026-09-23-vpn-phase6-server-data-plane.md)。
本文只覆盖其第二轮合并与审查轮次，未新增计划；文中的行级修复不构成新的实施计划。

## 摘要

第二轮把 `origin/main` 的 29 个提交（#23–#29）合入本分支（合并提交 `7abeb47`），
随后逐条审查「领先 main 但尚未合并回去」的部分。审查确认数据面与策略链的
实现与 ADR 0002 一致，同时发现 5 类问题并全部修复：

1. **F1（高）**：`TestMySQLV15ToV16VPNMigration` 在 main 新增的 `mysql56` CI job 下必然失败。
2. **F2（中高）**：`docs/user-guide/server-admin.md` 描述了一个不存在的后台横幅。
3. **F3（中）**：`docs/user-guide/agent.md` 把「数据面未交付」写成事实错误。
4. **F4（低中）**：14 处活文档与中英文镜像仍用「实施中/等待交付」，与 VPN 三份文档矛盾。
5. **F5（中）**：main 带回来的 5 个文件重新引入了真实内网域名（记作 `*.INTERNAL-DQ`，本文不复述）。

## 改动明细

### F1 MySQL 门控测试

`internal/storage/mysql_test.go`：

- setup 从 `raw.ExecContext(ctx, migrations.DDL)` 改为 `applySchemaStatements(ctx, raw, migrations.DDL, true)`。
  `migrations/ddl.sql` 含 `INSERT INTO authorization_revision(id, ...)`，而 `TestMySQLRepositoryContract`
  已在同一库 seed 过该行；CI 用 `-p 1` 串行跑同库，原写法必定 1062 duplicate key。
- `t.Cleanup` 不再复用已被 body `raw.Close()` 关掉的句柄，改为自行 `sql.Open` 一个 restore 句柄
  并 `defer restore.Close()`。原写法会 `sql: database is closed`，且 `raw.Close()` 被调用两次。
- 两处均与 main 已在 `4fc8213`、`e8b7c0d` 修好的 `TestMySQLV8ToV9*`、
  `prepareMySQLServiceTokenSchemaTest` 逐字同构。

新增静态守卫 `TestMySQLGatedTestsNeverExecWholeDDLScripts`（同文件）：扫描本包全部
`*_test.go`，凡函数体内出现 `sql.Open("mysql"` （含 helper），其执行整份 DDL 的行必须走
`applySchemaStatements`。守卫不依赖 MySQL，因此在没有服务器的机器上也是真门禁。
按仓库既有惯例（见 `TestMySQLV14ToV15WidensConnectionEpoch` 的注释）刻意做成纯文本断言。

### F2–F4 文档口径

统一口径为：**代码已在树内 → 只在 `-tags vpn` 构建中编译 → `server.vpn.enabled` 默认 false →
官方发行二进制与镜像的构建矩阵尚不含该 tag → 阶段 8 未完**。

- 9 处 `（[ADR 0002]…，实施中）` 标记改为 `（[ADR 0002]…，仅 \`-tags vpn\` 构建提供）`：
  `docs/user-guide/client.md`(2)、`http-proxy-entry.md`、`managed-http-route.md`、`sso-and-mfa.md`、
  `docs/deployment/nginx.md`、`docs/protocol/proxy-modules.md`(2)、`docs/operations/network-probes.md`。
  其中两处「等待内嵌 VPN 网关交付」的措辞改为「改用内嵌 VPN 网关」。
- `docs/user-guide/server-admin.md`：删除不存在的「页面顶部常驻这条说明」，改为说明数据面取决于
  构建变体、后台不探测该变体，并链接部署文档。
- `docs/user-guide/agent.md`：ICMP 排障段补上两个真实的 Server 侧前提（`-tags vpn` 构建 +
  `server.vpn.enabled`），并保留「缺 tag 时启动即失败」这一可观测事实。
- `docs/user-guide/vpn.md`：开头新增 3 行前提说明（构建 tag、默认关闭、发行包尚不含）。
- 中英文镜像与 README 同步：`README.md`、`README.zh-CN.md`、`docs/en/operations/security.md`、
  `docs/en/deployment/production.md`、`docs/en/user-guide/sso-and-mfa.md`。

未改动（刻意保留其真实历史）：pull-requests、plans、specs 三类时点记录里各阶段的
「实施中/尚未提供」表述。它们是时点记录，按规范不可改写。

### F5 真实域名脱敏

沿用 `91a030d` 的 RFC 2606 惯例与本包既有夹具命名，把 `*.INTERNAL-DQ` 换成同名前缀的示例域：

- `tm-6000d.INTERNAL-DQ` → `tm-6000d.tm.example.com`
- `tm-unclaimed.INTERNAL-DQ` → `tm-unclaimed.tm.example.com`
- `tunnelmesh-admin.INTERNAL-DQ` → `tunnelmesh-admin.example.com`

覆盖 `internal/server/web_dispatch_test.go`（6 行）与 4 份时点记录（8 行），共 14 处。
另把 plans/ 目录下 `2026-09-24-client-status-metadata-state.md` 里采样记录的真实笔记本主机名
改为 `devbook-pro`。仅替换标识符，不改写任何事实结论。

## 用户影响

无功能变化。管理员与使用者读到的 VPN 可用性描述现在与构建方式一致；
用发行版部署的人能立刻明白「能签发但连不通」的原因。

## API / Schema / 配置影响

无。Schema 仍为 v16，`server.vpn.*` 键集与默认值未变，OpenAPI 未改。

## 安全与授权影响

正向：仓库不再包含真实内网域名与内部机器主机名。
审查已确认并保持的既有边界：`internal/vpn/policy.go` 的 7 步策略链逐包执行（分片优先、
失败即拒）、`routing.IsDangerousAddress` 与 `IsPrivateTarget` 在 Server 侧强制执行、
`allow_private_targets` 默认 false、`max_peers`/`packet_rate_per_peer`/flow 上限均在锁内生效、
per-peer token bucket 随 peer 移除而 `delete`、reveal 走 `no-store` + 确认头 + 幂等键且**不经**
`mutate`（避免把 peer 私钥写进幂等表），审计只记 `nodeId`。

## 测试证据

本地实测（darwin/arm64，go1.27.1）：

```
go build ./...                      # 通过
go build -tags vpn ./...            # 通过
go vet ./... ; go vet -tags vpn     # 通过
go test ./... -count=1              # 25 包 ok
go test -tags vpn ./... -count=1    # 25 包 ok
gofmt -l internal cmd scripts deploy   # 无输出
git diff --check                      # 干净
python3 scripts/gen_doc_index.py      # 幂等，索引无变化
```

守卫的红灯证据：把 F1 的原写法临时塞回 `TestMySQLV15ToV16VPNMigration` 后
`go test -run TestMySQLGatedTestsNeverExecWholeDDLScripts` 失败并指名该函数与该行；
还原后通过。这是本次修复唯一可本地证明的验证方式。

**未能在本地验证**：环境无 docker/MySQL，`mysql56` job 的真实执行只能在 PR 的 CI 里完成。
因此 F1 的 CI 结论待 `go test -p 1 ./internal/storage ./internal/registry -run MySQL` 在
MySQL 5.6.51 上跑绿后才能宣称。

## 发布步骤

无发布动作：本分支未产出可发行变更，VPN 数据面仍不在发行矩阵内。

## 回滚步骤

`git revert` 本轮的修复提交即可，纯文档 + 单个测试文件 + 一个新增守卫测试，
无 Schema、无配置、无接口影响。

## Reviewer 关注点

1. **F1 只能靠 CI 最终确认**：合并前请看 `mysql56` job 是否真跑绿，不要只看本地绿灯。
2. **改了 main 拥有的文件**：`internal/server/web_dispatch_test.go` 与 4 份别的工作流的时点记录
   为脱敏被触碰，若 review 认为时点记录应保留原文，请回退这部分并改用「新增记录说明脱敏」。
3. **口径统一是否过松**：本分支选择写「`-tags vpn` 构建可用」而非「已交付」。阶段 8
   （`tunnelmesh_vpn_*` 指标、Grafana Row、告警、`test/e2e/vpn`、Docker、发行矩阵变体）仍未完成。
4. 未合并部分仍是 83 个提交 / 约 4.2 万行新增，建议按 VPN 阶段号顺序 review。

## 集成状态

`7abeb47` 及本轮修复尚未推送；`HEAD..origin/main` 为空，即本分支已包含 main 全部内容。
