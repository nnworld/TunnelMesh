# TunnelMesh 项目开发规范

## 项目目标

TunnelMesh 是一个 Go 实现的内网穿透与服务代理平台，包含三个可执行程序：

- `tunnelmesh-server`：控制面、路由入口、管理 API、Web 管理后台和中继。
- `tunnelmesh-agent`：部署在被访问主机或内网中的代理节点。
- `tunnelmesh-client`：用户侧客户端，负责本地端口转发、发布和 stdio/SSH 代理。

公网入口默认只使用 HTTP/HTTPS/WebSocket；公网 UDP 不作为服务端监听能力。内部转发支持 TCP、UDP、HTTP 和 WebSocket 场景。

## 技术基线

- Go：遵循当前 `go.mod` 版本和标准库优先原则。
- 本地模式：SQLite。
- 集群模式：MySQL 管理数据；节点租约/注册发现默认使用 MySQL，可配置切换 etcd。
- Agent/Client 与 Server：TLS WebSocket。
- Server 节点间：mTLS gRPC/HTTP2 relay。
- 管理后台：Vue 3、TypeScript、Vite、Pinia、Vue Router、Element Plus。
- 数据库 DDL：唯一权威文件为 `migrations/ddl.sql`。

## 目录职责

- `cmd/`：三个二进制的进程入口，只负责启动和退出码处理。
- `internal/cli/`：命令行参数、配置加载和子命令。
- `internal/config/`：配置模型、默认值和配置优先级。
- `internal/auth/`：用户、Token、Argon2id、RBAC 和管理员恢复。
- `internal/storage/`：数据库连接、DDL 初始化和 Repository 实现。
- `internal/registry/`：MySQL lease、etcd 注册发现和 epoch fencing。
- `internal/protocol/`：WebSocket frame、stream 状态机和 UDP association 协议。
- `internal/session/`、`internal/relay/`：会话管理、跨节点 relay 和流生命周期。
- `internal/routing/`、`internal/server/`：路由解析、HTTP/WS/TCP 接入和管理 API。
- `internal/client/`、`internal/agent/`：客户端转发、Agent 会话和内网服务连接。
- `web/`：管理后台源码；生产产物由 Go embed 提供给服务端。
- `docs/`：架构、协议、部署、运维和用户帮助文档。

## 架构约束

### 分层

HTTP/WebSocket Handler 只负责协议解析、认证授权和响应；业务编排放在 Service；持久化只能通过 Repository。禁止 Handler 直接执行 SQL 或绕过 Service 调用其他领域实现。

### 数据库

- 所有 schema 变更只修改 `migrations/ddl.sql`，禁止另建第二份 DDL。
- 必须同时考虑 SQLite 和 MySQL 语法兼容性；涉及索引、租约和事务时补充两种驱动的测试或验证。
- `auto-init` 开启时自动执行 DDL；关闭时缺表或版本不匹配必须快速失败。
- 管理数据以数据库为权威来源；缓存、租约和运行时状态不得替代持久化事实。

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

### Web 管理后台

- 使用 Element Plus 作为 UI 组件库。
- `/api/` 和 `/ws/` 路径必须优先于 SPA history fallback，不能被前端路由吞掉。
- 修改前端后运行 `npm test` 和 `npm run build`，生产资源需要重新生成并更新 Go embed 目录。

## 配置和运行

配置优先级为：命令行参数 > 环境变量 > 配置文件 > 默认值。

- 不在源码中硬编码密码、Token、私钥或生产 DSN。
- 首次启动管理员凭据只通过安全的控制台输出和恢复流程处理。
- 本地、集群 MySQL、集群 etcd 的配置示例和操作步骤必须同步维护到 `docs/`。

## 开发流程：TDD

新增功能或修复缺陷必须先写失败测试，再写最小实现，最后重构：

1. 明确接口、错误语义、边界条件和并发行为。
2. 编写单元测试、Repository contract test 或集成测试，并确认测试先失败。
3. 实现最小变更。
4. 补充异常、超时、权限、重复请求和关闭竞态测试。
5. 运行完整验证后再提交。

核心模块禁止只靠手工验证；跨层行为必须有集成测试，关键用户链路必须有 E2E 冒烟测试。

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
