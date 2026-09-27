# TunnelMesh 项目开发规范

## 项目目标

TunnelMesh 是一个 Go 实现的内网穿透与服务代理平台，包含三个可执行程序：

- `tunnelmesh-server`：控制面、路由入口、管理 API、Web 管理后台和中继。
- `tunnelmesh-agent`：部署在被访问主机或内网中的代理节点。
- `tunnelmesh-client`：用户侧客户端，负责本地端口转发、发布和 stdio/SSH 代理。

公网入口以 HTTP/HTTPS/WebSocket 为主；启用内嵌 VPN 网关（见 [ADR 0002](docs/architecture/adr/0002-public-ingress-and-embedded-vpn.md)）时，Server 额外监听一个公网 UDP 端口作为 WireGuard 端点，该端口不经反向代理、不参与 HTTP 路由，必须独立放行、限流与监控。内部转发支持 TCP、UDP、HTTP 和 WebSocket 场景。

## 技术基线

- Go：遵循当前 `go.mod` 版本和标准库优先原则。
- 本地模式：SQLite。
- 集群模式：MySQL 管理数据；节点租约/注册发现默认使用 MySQL，可配置切换 etcd。
- Agent/Client 与 Server：TLS WebSocket。
- Server 节点间：mTLS gRPC/HTTP2 relay。
- 管理后台：Vue 3、TypeScript、Vite、Pinia、Vue Router、Element Plus。
- 数据库 DDL：`migrations/ddl.sql` 是当前版本全量建库脚本；`migrations/incremental/` 保存不可变的版本间增量升级脚本。

## 目录职责

- `cmd/`：三个二进制的进程入口，只负责启动和退出码处理。
- `internal/cli/`：命令行参数、配置加载和子命令。
- `internal/config/`：配置模型、默认值和配置优先级。
- `internal/auth/`：用户、Token、Argon2id、RBAC 和管理员恢复。
- `internal/storage/`：数据库连接、DDL 初始化、版本迁移和 Repository 实现。
- `migrations/ddl.sql`：当前版本的全量 DDL，用于空库初始化和最终 Schema 校验。
- `migrations/incremental/`：已发布 Schema 版本之间的增量 DDL，按版本目录分别维护 MySQL 与 SQLite 脚本。
- `internal/registry/`：MySQL lease、etcd 注册发现和 epoch fencing。
- `internal/protocol/`：WebSocket frame、能力协商、稳定错误码、stream 状态机、UDP association 和逻辑 traceroute 协议。
- `internal/session/`、`internal/relay/`：会话管理、跨节点 relay 和流生命周期。
- `internal/routing/`、`internal/server/`：路由解析、HTTP/WS/TCP 接入和管理 API。
- `internal/client/`、`internal/agent/`：客户端转发、Agent 会话和内网服务连接。
- `internal/proxy/`：HTTP CONNECT、SOCKS5、PROXY protocol v2 等入口握手解析；解析器不得绕过路由策略。
- `internal/observability/`：Prometheus 指标、结构化事件和 W3C traceparent 上下文传播。
- `web/`：管理后台源码；生产产物由 Go embed 提供给服务端。
- `docs/`：架构、协议、部署、运维和用户帮助文档。

## 架构约束

### 分层

HTTP/WebSocket Handler 只负责协议解析、认证授权和响应；业务编排放在 Service；持久化只能通过 Repository。禁止 Handler 直接执行 SQL 或绕过 Service 调用其他领域实现。

### 数据库

- `migrations/ddl.sql` 是“当前 Schema 状态”的唯一权威全量脚本；`migrations/incremental/` 是“版本升级路径”的唯一权威历史。二者职责不同，不得维护其他影子 DDL。
- 每次 Schema 变更必须同时更新全量 DDL、对应增量 DDL、`SchemaVersion`、迁移测试和升级文档；禁止只修改 `migrations/ddl.sql` 或只提升版本号。
- 增量脚本目录命名为 `migrations/incremental/vNNNN_to_vNNNN/`，其中必须包含 `mysql.sql` 和 `sqlite.sql`；`NNNN` 与 `schema_meta.version` 一一对应并单调递增。
- 已发布的增量脚本禁止修改、重排或删除；修复已发布迁移必须新增下一个 Schema 版本，保证审计与升级结果可复现。
- 只要求维护相邻 Schema 版本的增量脚本；跨多个版本升级必须按版本号顺序逐个执行，不允许跳过中间迁移。
- 必须同时考虑 SQLite 和 MySQL 语法兼容性；涉及索引、租约和事务时补充两种驱动的测试或验证。
- `auto-init` 开启时：空库执行全量 DDL，旧库按 `schema_meta.version` 顺序执行增量 DDL；禁止使用 `CREATE TABLE IF NOT EXISTS` 代替真实的存量表迁移。
- `auto-init` 关闭时，缺表、版本不匹配或增量链不完整必须快速失败，并给出当前版本、目标版本和缺失迁移信息。
- 增量迁移必须可安全重试：执行前校验源版本和必要前置条件，执行成功后再推进 `schema_meta.version`。MySQL DDL 可能隐式提交，脚本必须避免依赖整体事务回滚，并提供失败后的恢复步骤。
- 破坏性变更不得与依赖它的代码在同一步发布；采用 expand → migrate/backfill → contract，先新增兼容结构，再迁移数据和代码，最后在允许的版本窗口删除旧结构。
- 管理数据以数据库为权威来源；缓存、租约和运行时状态不得替代持久化事实。

### 版本升级与迭代

- 应用版本遵循 `MAJOR.MINOR.PATCH`；应用版本与整数 Schema 版本分别管理，但发布说明必须明确二者映射、最低可升级版本和是否包含数据库迁移。
- **补丁版本（PATCH）**：原则上不得改变 Schema 结构；只允许代码修复和不改变数据库契约的数据修正。确需 DDL 时必须升级为 MINOR，不得把结构迁移隐藏在 PATCH 中。
- **小版本（MINOR）**：只允许向后兼容的 Schema 扩展，例如新增可空列、新表或不影响旧查询的索引；必须保证滚动升级期间新旧 Server 可同时访问数据库。
- **大版本（MAJOR）**：允许经过批准的不兼容 Schema 或数据语义变更，但必须有 ADR、升级前备份、停机或兼容窗口、数据迁移、回滚方案和明确的最低源版本。能分阶段完成的破坏性变更仍必须跨版本执行 expand/contract。
- 升级前必须检查当前应用版本、`schema_meta.version`、数据库类型、备份可恢复性、账号 DDL 权限和增量链完整性；不满足前置条件时拒绝升级。
- 升级后必须校验目标 Schema 版本、关键表/列/索引、数据回填结果、健康检查和关键链路；升级日志不得包含 DSN、密码、Token 或业务敏感数据。
- 回滚优先回退应用且保留向后兼容 Schema；涉及不可逆 DDL 或数据转换时必须通过备份恢复或经验证的补偿迁移处理，禁止自动猜测反向 SQL。
- 每个包含 Schema 变更的版本必须在 `docs/operations/` 更新升级与回滚步骤，说明预计锁表时间、容量影响、灰度顺序和 5 分钟止损方案。

### 一致性与并发

- 幂等键、路由唯一性、租约获取和 epoch fencing 必须由存储层或数据库约束保证，不能只依赖进程内锁。
- 外部依赖调用必须设置超时；重试应使用指数退避和 jitter。
- 会话、流和 UDP association 必须正确处理关闭、半关闭、EOF、重复 ID、超时和 stale generation。
- 用户资源查询必须在分页语义内完成权限过滤，不能先全局分页再简单丢弃无权数据。

### API

- API 版本前缀为 `/api/v1`。
- 响应统一为 `{ "code": number, "msg": string, "data": any }`。
- 列表接口使用 cursor 分页，不使用 offset 分页。
- 写操作支持 `Idempotency-Key` 时必须保证重放一致性和并发安全。
- 所有权限校验在服务端完成；不能信任客户端传入的 owner、agent 或 role。
- OpenAPI 文档位于 `docs/api/openapi.yaml`，每次接口行为变更必须同步更新。

### 已批准的管理 API

- `POST /api/v1/tokens/{tokenId}/reveal`：仅管理员可用，必须提供 `X-Token-Reveal-Confirm`、`Idempotency-Key` 和 `acknowledgeRisk=true`；响应必须 `Cache-Control: no-store`，并写入审计日志。
- `POST /api/v1/agents/{agentId}/trace`、`GET /api/v1/traces/{traceId}`：逻辑 traceroute，不是 ICMP traceroute。普通用户只能读取脱敏 hop；管理员可显式请求内网 IP、真实连接地址和证书元数据。
- `POST /api/v1/agents/{agentId}/diagnose`、`GET /api/v1/agents/{agentId}/probes`：TCP/HTTP/UDP 探针摘要，不返回或持久化响应体。

新增管理 API 必须同步更新 OpenAPI、`docs/README.md` 和对应用户帮助文档，并覆盖未授权、越权、分页、幂等和错误响应测试。

### Web 管理后台

- 使用 Element Plus 作为 UI 组件库。
- `/api/` 和 `/ws/` 路径必须优先于 SPA history fallback，不能被前端路由吞掉。
- 修改前端后运行 `npm test` 和 `npm run build`，生产资源需要重新生成并更新 Go embed 目录。

## 配置和运行

配置优先级为：命令行参数 > 环境变量 > 配置文件 > 默认值。

- 不在源码中硬编码密码、Token、私钥或生产 DSN。
- 首次启动管理员凭据只通过安全的控制台输出和恢复流程处理。
- 本地、集群 MySQL、集群 etcd 的配置示例和操作步骤必须同步维护到 `docs/`。
- 可恢复的 service-token secret 使用 `TUNNELMESH_TOKEN_ENCRYPTION_KEY`（AES-256-GCM，支持 base64/hex）和可选 `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID`；密钥只能由环境变量或外部 Secret Manager 注入。
- 集群中所有 Server/relay 节点必须配置一致的 `TUNNELMESH_TRACE_SIGNING_KEY`，用于 traceroute hop 签名链校验。
- 配置优先级、`auto-init`、密钥注入和 schema version 变更必须同时验证 SQLite 与 MySQL 兼容性。

## 开发流程：TDD

新增功能或修复缺陷必须先写失败测试，再写最小实现，最后重构：

1. 明确接口、错误语义、边界条件和并发行为。
2. 编写单元测试、Repository contract test 或集成测试，并确认测试先失败。
3. 实现最小变更。
4. 补充异常、超时、权限、重复请求和关闭竞态测试。
5. 运行完整验证后再提交。

核心模块禁止只靠手工验证；跨层行为必须有集成测试，关键用户链路必须有 E2E 冒烟测试。

## Plan 与 PR 要求

- 非平凡功能、缺陷修复、接口行为变更、Schema 变更和跨模块重构，必须在实现前编写实施计划；紧急修复可以在止损后 24 小时内补记，但必须明确标注“补记计划”并只记录已验证事实。
- 实施计划保存在 `docs/superpowers/plans/YYYY-MM-DD-<feature-name>.md`，文件名使用英文短横线命名。
- 创建实施计划后必须暂停并等待用户确认；未获得用户明确确认前，不得开始实现、修改生产代码或执行计划中的任务。
- 计划必须包含：目标、架构决策、技术栈、规格引用、全局约束、精确文件清单、任务间接口、TDD 步骤、预期失败结果、最小实现、预期通过结果、验证命令、回滚注意事项。
- 计划不得包含 `TBD`、`TODO`、“后续补充”等占位内容，不得引用不存在的类型、函数或文档；每个任务必须能独立测试和评审。
- 补记计划必须说明原始实现已完成，并记录实际验证结果；不得虚构当时的红灯测试输出。
- PR 面向的变更必须同步编写 PR 描述文档，保存在 `docs/pull-requests/YYYY-MM-DD-<feature-name>.md`。
- PR 描述必须包含：标题、目标分支、摘要、用户影响、API/Schema/配置影响、安全与授权影响、测试证据、发布步骤、回滚步骤、Reviewer 关注点和集成状态。
- PR 默认基于 `main`；如果因发布或长期迭代需要其他基线，必须由用户明确确认。
- 创建 PR 前必须完成对应范围的 `go test ./... -count=1`、`go test -race ./...`、`go vet ./...`、前端 `npm test -- --run` 和 `npm run build`、`git diff --check`；涉及 Web 产物时还必须执行嵌入资源验证。
- PR 描述、计划、提交信息和评论中不得包含密码、Token、私钥、生产 DSN、完整凭据或未脱敏日志。
- 未经用户明确授权，不得执行 commit、push、merge、创建远端 PR 或删除远端数据。

### 安全功能边界

- service-token 认证仍使用 hash 校验；为满足管理员显式 reveal 需求，新建/轮换 token 额外保存 AES-GCM 密文、nonce、key id 和版本。禁止明文入库。
- 普通 token 列表、审计日志、Prometheus、普通 traceroute 和错误日志不得包含 secret、密码、私钥、完整 Authorization header 或会话字节。
- `includeSensitive=true` 仅管理员可用；`includeSecrets=true` 不得通过 traceroute 返回，必须使用独立 reveal API。
- Agent 上报 metadata 只能来自 allowlist 的文件/环境变量项；名称匹配敏感模式时必须清空值并标记 `redacted=true`。
- 所有目标地址在 Agent 侧再次进行 SSRF、回环、私网、链路本地、CIDR 和端口策略校验。
- 继续不实现：P2P NAT traversal、任意远程命令执行。SSH 支持仅限现有 stdio/WebSocket 代理链路，不能扩展为通用命令执行 API。
- VPN 数据面按 [ADR 0002](docs/architecture/adr/0002-public-ingress-and-embedded-vpn.md) 实现：WireGuard 端点与内存态 TUN（gVisor netstack）运行在 `tunnelmesh-server` 进程内，不打开 `/dev/net/tun`、不要求 `CAP_NET_ADMIN`、不转发 L2 以太网帧；ICMP 只支持 echo（Agent 侧非特权 ping socket），Server 不主动构造其它 ICMP 类型；重依赖用 `//go:build vpn` 隔离，`server.vpn.enabled` 为 false 时不创建任何 VPN 资源。该能力分阶段实施，交付前活文档不得宣称可用。

### 协议演进约束

- 新 frame 必须加入 `internal/protocol/frame.go` 的版本化类型，并通过 capability negotiation 后才能使用；未知扩展必须安全拒绝。
- 已有协议基础包括 flow-control、UDP association、GOAWAY/drain、trace frame 和稳定错误码；任何状态机变化必须覆盖重复 ID、EOF、半关闭、超时、窗口耗尽和 stale epoch。
- 代理协议模块必须保持 Handler → Service → Repository/Adapter 分层；握手解析器只解析和校验，不直接建立未授权连接。

## 必须执行的验证

Go 代码变更至少执行：

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

前端变更至少执行：

```bash
cd web
npm test -- --run
npm run build
```

涉及 Docker、部署或配置时，还要执行对应 Compose/Docker 验证，并检查非 root 启动、健康检查和敏感配置注入。

## 文档要求

架构、协议、配置、部署、用户使用、SSH/websocat、故障排查和管理员恢复流程必须有对应文档。重大设计变化写 ADR，说明背景、决策、替代方案和迁移影响。

`docs/` 的目录职责、命名约定、索引维护和时点记录不可改写原则见 `docs/development/documentation.md`。实施计划、设计规格、PR 记录和 ADR 的目录索引由 `scripts/gen_doc_index.py` 生成，禁止手工编辑；新增或改名记录后必须重新生成索引，并保证 `docs/README.md` 的分类索引不出现无入口指向的孤儿文档。

## Git 规范

- 默认分支：`main`。
- 远程仓库：`git@github.com:nnworld/TunnelMesh.git`。
- 提交格式：`<type>(<scope>): <subject>`，subject 使用祈使句，简洁且不加句号。
- 提交前检查 `git diff --cached`，不得提交密钥、Token、`.env`、构建缓存或 `node_modules/`。
- 未获得用户明确授权时，不执行 commit、push、merge 或删除远程数据；获得授权后仍需先完成验证。
- 核心模块改动应经过独立代码复审；复审发现的问题必须修复或明确记录为技术债务。

## 安全与可观测性

- 所有外部输入在边界校验，防止注入、越权、资源耗尽和敏感信息泄露。
- 日志不得输出密码、Token、私钥或完整凭据；关键操作写结构化审计日志。
- 核心链路传递 trace ID，暴露健康检查、关键指标和必要的连接/流统计。
- 生产故障遵循先止损、再定位、后复盘；复盘必须包含时间线、根因、影响和可验证的改进项。
