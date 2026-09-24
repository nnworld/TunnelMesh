# 文档规范

本文件只约定 `docs/` 的组织方式。架构约束、TDD 流程、Plan 与 PR 要求、验证命令的权威来源是
[AGENTS.md](../../AGENTS.md)，此处不复制其正文，避免两份规则漂移。

## 目录职责

| 目录 | 面向 | 内容 | 变更方式 |
| --- | --- | --- | --- |
| `docs/user-guide/` | 使用者 | Client、Agent、管理后台、托管路由、SSH over WebSocket | 手工维护 |
| `docs/deployment/` | 部署者 | Docker、前端构建、Nginx、systemd/launchd/Windows Service、发行打包 | 手工维护 |
| `docs/operations/` | 运维 | 配置、Schema 升级、relay mTLS、连接池、可观测性、探针、日志、SLO、容量、排障、完整性清单 | 手工维护 |
| `docs/architecture/` | 架构 | 架构概览、集群架构 | 手工维护 |
| `docs/architecture/adr/` | 架构 | 架构决策记录 | 记录 + 生成索引 |
| `docs/protocol/` | 协议实现 | WebSocket frame、代理协议模块 | 手工维护 |
| `docs/api/` | 接口 | `openapi.yaml`，接口行为变更必须同步 | 手工维护 |
| `docs/en/` | 英文读者 | 生产部署、安全加固、三端使用入口 | 手工维护 |
| `docs/community/` | 社区与增长 | GitHub 元数据、技术文章、分发计划 | 手工维护 |
| `docs/assets/` | 所有读者 | 管理后台截图、演示 GIF、社交卡片 | 手工维护 |
| `docs/development/` | 贡献者 | 测试与验证、文档规范 | 手工维护 |
| `docs/superpowers/plans/` | 贡献者 | 实施计划（时点记录） | 记录 + 生成索引 |
| `docs/superpowers/specs/` | 贡献者 | 设计规格（时点记录） | 记录 + 生成索引 |
| `docs/pull-requests/` | 贡献者 | PR 描述记录（时点记录） | 记录 + 生成索引 |

## docs/ 与 deploy/ 边界

仓库根目录的 `deploy/` 不属于 `docs/`，两者按“产物 vs 说明”划分，不重复内容：

| 位置 | 放什么 | 不放什么 |
| --- | --- | --- |
| `deploy/` | 可直接使用的部署产物：systemd unit、launchd/WinSW 模板、安装脚本、Prometheus 配置与规则、Grafana Dashboard 与 provisioning | 操作步骤、参数解释、排障流程 |
| `docs/` | 上述产物的使用说明、参数含义、安装步骤、升级与排障 | 第二份可执行产物或配置副本 |

`deploy/README.md` 是产物清单，逐条列出每个文件对应的 `docs/` 文档；新增或重命名产物时必须同步
更新该清单。发布归档的目录布局由 `scripts/build-release.sh` 决定，属于对外契约，重命名
`deploy/install`、`deploy/systemd`、`deploy/macos`、`deploy/windows` 前必须先改脚本和
[跨平台可执行文件打包](../deployment/binary-release.md)。

## 命名约定

- 记录类文件必须带日期前缀：计划 `YYYY-MM-DD-<feature-name>.md`、规格
  `YYYY-MM-DD-<feature-name>-design.md`、PR 记录 `YYYY-MM-DD-<feature-name>.md`、ADR
  `NNNN-<short-title>.md`。
- 说明类文件使用稳定语义名（`configuration.md`、`troubleshooting.md`），不加日期，因为它们是
  “当前状态”而不是“某次变更的记录”。
- 文件名一律英文短横线小写；正文标题使用中文。
- 每个目录都应有 `README.md` 作为入口，GitHub 打开目录时才能直接渲染导航。

## 索引维护

计划、规格、PR 记录和 ADR 的索引由脚本生成，不要手工编辑：

```bash
python3 scripts/gen_doc_index.py
```

脚本扫描四个记录目录，按文件名日期与 feature slug 建立交叉引用，并识别正文中显式链接的
关联记录。因此**新增记录时要在正文里链接对应的计划/规格/PR**，否则索引只能靠 slug 猜测同名
关联。生成后检查 `git diff`，确认新记录已出现在索引中。

`docs/README.md` 是面向读者的分类索引，手工维护；新增说明类文档时必须同步登记，否则会成为
没有任何入口指向的孤儿文档。

## 时点记录不可改写

计划、规格、PR 记录和已接受的 ADR 是决策时点的证据：

- 不重写结论、不删除失败尝试、不追改验证结果。
- 需要修正时新增记录，或在计划正文标注“补记计划”并只记录已验证事实（AGENTS.md 紧急修复条款）。
- 允许的历史文件改动只有两类：**链接维护**（目标文档移动或改名时更新路径，不改动其余正文）和**标识脱敏**（把真实内网域名、主机名、机器名换成 RFC 2606 示例域）。脱敏只替换标识符，结论、时间线与验证结果必须原样保留。
- 文件改名前必须评估历史记录的引用面：`rg -l '<old-name>.md' docs/` 命中历史记录时，优先考虑
  保留原名，避免为了命名美观而大面积改写历史文件。

## 语言与敏感信息

- 文档正文默认使用中文；命令、配置键、代码标识符、协议字段和错误码保持英文原文。
- `docs/en/` 与 `docs/community/` 面向英文读者和发布传播，可以使用英文；遇到语义冲突时以中文深度文档为准。
- 文档与示例中不得出现密码、Token、私钥、生产 DSN、完整凭据或未脱敏日志。示例一律使用
  `tunnel.example.com`、`<token from the console>` 这类占位符。
- 真实内网域名、内部主机名与开发者机器名同样属于内部信息，测试夹具、示例配置、i18n 文案和
  时点记录里都不得出现（历史上回归过两次：`91a030d` 清理后又被合并带回）。替换沿用固定映射：
  动态后缀用 `apps.example.com`，托管路由 `tm-*` 用 `tm-*.tm.example.com`，控制台用 `example.com`；
  新增文档前先 `rg '<internal-domain>'` 确认没有把真实域名带回来，且不复述被清理的字符串。
- 涉及公网入口的示例必须使用真实存在的路径（`/ws/agent`、`/ws/client`、`/ws/tcp`、
  `/ws/webssh/<session-id>`、`/api/v1`、`/health/live`、`/health/ready`、`/metrics`）；
  路径以 `internal/server/web.go` 和 `internal/server/health.go` 为准。

## 链接规范

- 使用仓库内相对路径，不写 `file://`、绝对路径或带机器名的路径。
- 链接目录时确保该目录有 `README.md`；否则链接在 GitHub 上只能看到文件列表。
- 提交前用 `git diff --check` 检查空白错误，并确认新增链接的目标文件存在。
