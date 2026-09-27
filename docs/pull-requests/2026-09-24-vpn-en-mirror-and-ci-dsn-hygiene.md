# VPN 英文镜像补齐与 `multiStatements` 遗留说法清理

- **状态**：已实施（本分支），等待 review
- **类型**：docs / chore(ci)
- **补记说明**：本节计划写在实现之后，只记录已验证事实。

## 目标分支

`main`（源分支 `codex/vpn-phase6-server-data-plane`）。

## 摘要

上一轮 review 留下两条待办，本轮把它们做完：

1. `docs/en/` 完全没有 VPN 内容——中文已有 `docs/user-guide/vpn.md`、
   `docs/deployment/vpn-gateway.md`、`docs/operations/vpn.md` 三份深文档，英文读者却无从入口。
2. `.github/workflows/ci.yml` 与 `docs/development/testing.md` 仍宣称
   `multiStatements=true` 是 MySQL 测试 DSN 的必需参数，而该前提在 main 的
   `4fc8213`/`e8b7c0d` 把整段 DDL 改成逐句执行后已经不成立。

## 改动明细

### 英文镜像

`docs/en/` 的定位是简明入口、中文为权威深文档（`docs/development/documentation.md`），
因此按主题补段，不整页翻译：

- `docs/en/user-guide/server-admin.md`：新增 `## VPN gateway peers`，覆盖签发对象与可见性过滤、
  **管理面随本版本发布、数据面取决于构建变体**这一口径、表单与服务端逐字对齐的校验、
  ICMP 能力门禁与 `icmpCapability: unverified` 的集群限制、轮换/吊销的终态语义、
  reveal 的三重确认与 `no-store`、活跃流抽屉的快照语义与 `501` 的区别、IP 池概览不从前缀推算。
- `docs/en/user-guide/agent.md`：新增 `## ICMP echo for the VPN gateway`，给出
  `agent.streams.icmp_enabled` 的默认关闭、`net.ipv4.ping_group_range` 与
  `agent icmp echo is unavailable` 这条启动期 Error、只处理 echo、目标仍受 Agent policy 约束，
  以及中文 `agent.md` 同款的排障顺序（最后落到 Server 构建变体）。
- `docs/en/user-guide/client.md`：UDP 段补一句——`forward udp` 公网侧没有对应监听，
  Server 的 WireGuard 入口是独立通道且仅 `-tags vpn` 构建提供。
- `docs/en/README.md`：显式说明 VPN 尚无英文页并给出三份中文深文档链接，避免英文读者以为不存在该能力。

未翻译 `docs/user-guide/vpn.md` 整页：它是终端用户的 wg-quick 操作手册，与后台文案、
`docs/deployment/vpn-gateway.md` 的体积实测和放行步骤连在一起，整页翻译会立即产生一份
需要与中文同步维护的第二事实源。若需要，另立一轮把它升级为正式英文页。

### `multiStatements` 清理

- 先确认前提：全仓扫描 MySQL 门控路径，`raw.Exec(多语句)` 只剩 SQLite 夹具
  （`account_repository_test.go`、`sqlite_test.go`、`vpn_repository_test.go` 全部
  `sql.Open("sqlite"`），MySQL 侧与 auto-init 都走 `applySchemaStatements` 单语句执行。
- `.github/workflows/ci.yml`：DSN 去掉 `&multiStatements=true`，注释改为说明该参数为何曾必需、
  现在由 `TestMySQLGatedTestsNeverExecWholeDDLScripts` 防止回归，并明确不要为「保险」加回。
- `docs/development/testing.md`：本地复跑命令同步去掉该参数，要点从「是必需的」改为「不需要」并保留因果。
- 未改动：pull-requests 与 plans 两处目录下同名的 `2026-09-24-mysql56-contract-ci-gate.md`
  仍保留当时的旧说法，按时点记录不可改写原则不动。

## 用户影响

英文读者第一次能在英文页面里读到 VPN 网关的管理面范围与构建变体前提；
无功能与接口变化。

## API / Schema / 配置影响

无。生产 DSN 无需 `multiStatements`，此前也未被任何文档要求，因此不存在需要变更的部署。

## 安全与授权影响

无新增暴露。英文镜像沿用已统一的交付状态口径，不宣称发行版包含数据面。

## 测试证据

本地实测（darwin/arm64，go1.27.1）：

```
go build ./... ; go vet ./...                       # 通过
go test ./deploy/... ./scripts/ ./internal/storage/ ./internal/config/ -count=1   # 全 ok
go test ./... -count=1                              # 无 FAIL（含 internal/e2e、internal/server）
python3 scripts/gen_doc_index.py                    # 幂等
git diff --check                                    # 干净
相对链接检查                                        # 5 个 md，0 死链
ruby -e 'YAML.load_file(".github/workflows/ci.yml")'  # 解析通过，DSN 读回正确
```

`deploy/mysql56/mysql56_service_test.go` 直接读取 `ci.yml`，它的通过说明 workflow 形状未破。

**仍需 CI 确认**：去掉 `multiStatements` 后的 `mysql56` job 是该参数真正的使用现场；
本地无 docker/MySQL，无法执行。若它在 CI 变红，说明还有一条我未扫到的多语句路径，
恢复该参数即可，无数据风险。

## 发布步骤

无。

## 回滚步骤

`git revert` 本轮提交。`ci.yml` 与 `testing.md` 可单独回滚（恢复 DSN 参数即完全复原旧行为）。

## Reviewer 关注点

1. 英文镜像刻意只补到「主题级摘要」，若判断应整页翻译 `docs/user-guide/vpn.md`，请指明后另起一轮。
2. `ci.yml` 去参数这条依赖 CI 的 `mysql56` job 绿；这是本轮唯一未被本地证明的断言。
3. `docs/en/user-guide/server-admin.md` 新增段落的口径必须与中文保持一致，后续改中文时要同步。

## 集成状态

已推送到 `origin/codex/vpn-phase6-server-data-plane`。
