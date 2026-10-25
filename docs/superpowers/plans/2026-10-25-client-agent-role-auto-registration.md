# Client/Agent 角色融合与自动注册实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在现有 `client` 配置中增加可选的 `client.agent_id`，配置后让 client 进程同时承载 client 隧道和 Agent 会话，并让独立 `tunnelmesh-agent` 在缺少 `agent.id` 时使用已绑定的 Agent token 查询服务端并回写 ID；macOS 与 Windows 托盘界面同步展示和编辑该配置。

**Architecture:** 复用现有 client 的 Server URL、client token、transport、连接池、stream、metadata 和 instance identity；client 的 Agent 角色使用独立 Agent WebSocket 会话和既有 Agent dispatcher，但不新增独立 Agent 配置块。Server 在 Agent WebSocket 首次收到 client token+agent_id 时按 token owner 创建同名 Agent，已存在时执行 owner、enabled、scope 校验；独立 Agent 仍使用 `agent.token`，通过新增 Agent 作用域只读身份接口获取 token 绑定的 ID。托盘只扩展共享配置读写和提示，不分叉 macOS/Windows 原生壳。

**Tech Stack:** Go、Viper/mapstructure、YAML AST 原子写回、现有 WebSocket protocol、SQLite/MySQL Repository、Vue 3 + TypeScript + Element Plus + vue-i18n、现有 macOS WKWebView/Windows WebView2 托盘壳。

**Spec:** 用户确认的 `client.agent_id` 角色融合、首次连接自动创建同名 Agent、独立 Agent 读取已绑定 ID 后回写、agent-only client 允许启动、托盘 UI 标注说明。

## Global Constraints

- 基于当前 `origin/main` 实现；不创建新的 git worktree。
- `client.agent_id` 为空时，client 行为保持现状；配置后 client 可仅运行 Agent 角色，即 `client.tunnels` 可以为空。
- client Agent 角色复用 `client.token`；空 `scope.AgentIDs` 代表该 token owner 的全部启用 Agent，有 scope 时必须包含 `client.agent_id`。
- 独立 `tunnelmesh-agent` 继续复用 `agent.token`；缺少 `agent.id` 时只查询 token 已绑定的 Agent，不生成新 ID、不创建新 Agent、不覆盖非空且不一致的 ID。
- Server 自动创建的 Agent 使用 `ID=client.agent_id`、`Name=client.agent_id`、当前 token owner、`Enabled=true`，并写入审计日志；并发注册必须幂等且不能越权。
- 所有 API 保持 `/api/v1`、`{code,msg,data}`、服务端权限校验和敏感信息不回显；OpenAPI、用户文档和 `docs/README.md` 索引同步更新。
- 必须先写失败测试并观察红灯，再写最小实现；不提交 Token、构建缓存或 `node_modules`。
- 不执行 commit、push、merge 或创建远端 MR，除非用户另行明确授权。

---

### Task 1: 配置模型与 YAML 安全回写

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/tray/configstore.go`
- Modify: `internal/tray/app.go`
- Modify: `web-tray/src/api/types.ts`
- Modify: `web-tray/src/stores/routing.ts`
- Test: `internal/config/config_test.go`, `internal/tray/configstore_test.go`, `internal/tray/app_test.go`

**Interfaces:**
- `config.ClientConfig.AgentID string` 使用 `mapstructure:"agent_id" json:"agent_id" yaml:"agent_id"`。
- `tray.ClientSettings.AgentID string`、`tray.RoutingView.AgentID string`、`tray.RoutingUpdate.AgentID string`。
- `client.agent_id` 可为空；当 `client.tunnels` 为空且 `client.agent_id` 非空时配置加载和运行时预校验通过；两者都为空时仍拒绝启动。
- `ConfigStore.Load/Save/BuildConfig` 必须读写顶层 `client.agent_id`，保留未知根键、注释、权限和 token。

- [ ] **Step 1: 写失败测试**：覆盖 `config.Load` 读取 `client.agent_id`、空 tunnels+agent_id 通过、空 tunnels+空 agent_id 失败；覆盖 tray round-trip、0600、旧配置无字段默认空、未知字段和注释保留。
- [ ] **Step 2: 运行红灯**：`go test ./internal/config ./internal/tray -run 'AgentID|AgentOnly|ConfigStore' -count=1`；预期失败于缺少字段/校验分支。
- [ ] **Step 3: 最小实现**：增加配置字段和默认/校验规则；在 YAML AST 的 client mapping 中安全更新 `agent_id`，空值删除或保持不写入；扩展 tray 视图和 pending routing payload。
- [ ] **Step 4: 运行绿灯**：重复上述定向测试，确认通过且 token 不出现在 JSON/视图。
- [ ] **Step 5: 重构**：复用已有 tunnel YAML 节点更新和原子写回逻辑，避免重复序列化路径。

### Task 2: Server Agent 作用域鉴权与首次自动注册

**Files:**
- Modify: `internal/server/middleware.go`, `internal/server/runtime.go`
- Modify: `internal/server/api.go` 或新增 `internal/server/agent_scoped_api.go`
- Modify: `internal/server/client_scoped_api.go`
- Test: `internal/server/runtime_test.go`, `internal/server/client_scoped_api_test.go`, 新增 Agent 作用域 API/注册测试
- Modify: `docs/api/openapi.yaml`

**Interfaces:**
- 新增只读 `GET /api/v1/agent/self`，仅接受 `TokenTypeAgent`，返回最小 `{id,name}`，不返回 token、owner 或 metadata。
- 新增 `AgentTokenValidator` 接口，或复用窄接口注入 `ValidateAs(context.Context,string,storage.TokenType)`；缺少 validator 返回 503，错误 token 返回 401。
- Agent WebSocket 鉴权上下文扩展为 `Agent`/`Client` 两种身份；hello 后统一进入 `authenticateAgentConnection`。
- client token 注册规则：目标 ID 非空且格式受限；空 Agent scope 允许 owner 的启用 Agent；非空 scope 必须包含目标 ID；已有记录必须 owner 相同且 enabled；缺失记录按 ID 创建同名启用 Agent；重复创建竞态回读并再次校验。

- [ ] **Step 1: 写失败测试**：覆盖 `/api/v1/agent/self` 未授权、client token/console token 拒绝、有效 agent token 返回绑定 ID；覆盖 client token Agent WS 首次连接自动创建、重连幂等、scope 拒绝、跨 owner 拒绝、disabled 拒绝、并发创建不重复；覆盖已有 Agent token 行为不变。
- [ ] **Step 2: 运行红灯**：`go test ./internal/server -run 'AgentSelf|ClientToken.*Agent|AutoRegister|AgentWebSocket' -count=1`；预期新接口/鉴权路径失败。
- [ ] **Step 3: 最小实现**：在 `ServeHTTP` pre-auth 段加入 Agent 作用域路由；在 runtime 的 agent handshake 中保留 agent token 绑定校验，并为 client token 增加 owner/scope/自动创建分支；自动创建写 `agent.created` 或专用 `agent.auto_registered` 审计，审计只记录 ID/name/owner，不记录 token。
- [ ] **Step 4: 运行绿灯**：执行定向 Server 测试，确认原 agent WS、client WS、client scoped agents 测试全部通过。
- [ ] **Step 5: 文档契约**：同步 OpenAPI operation、security scheme、响应 schema、401/403/503；增加 endpoint 契约测试，确保没有敏感字段。

### Task 3: client Runtime 同时承载 Agent 角色

**Files:**
- Modify: `internal/client/runtime.go`, `internal/client/runtime_test.go`
- Modify: `internal/agent/connection_pool.go` 或新增可复用配置适配文件
- Modify: `internal/cli/client_run.go`, `internal/cli/root.go`
- Test: `internal/client/runtime_test.go`, `internal/cli/client_runtime_test.go`

**Interfaces:**
- `NewRuntimeFromConfig` 接受 `cfg.Client.AgentID`；client tunnels 为空但 AgentID 非空时仍构造 Runtime。
- Runtime 启动/停止同时管理 local forwarders 和 Agent connection pool；任一角色停止时通过同一 context 优雅退出，互斥锁仍按 config 路径只占一份。
- Agent 角色使用 client `server_url`、`token`、transport、connections、stream、metadata、instance identity；NodeID/Agent InstanceID 使用同一稳定 client instance identity，不能生成第二套身份。
- `Runtime.Stats` 增加 Agent-role 状态/错误可观测信息，但保留现有 client tunnel stats 字段兼容性。

- [ ] **Step 1: 写失败测试**：覆盖 agent-only runtime 能构造并 Start；client tunnels+agent role 同时启动；无 agent_id 时不启动 agent role；两角色 Stop 可释放锁；client run 的原 tunnel 行为和错误文本保持兼容。
- [ ] **Step 2: 运行红灯**：`go test ./internal/client ./internal/cli -run 'AgentRole|AgentOnly|Runtime' -count=1`。
- [ ] **Step 3: 最小实现**：引入 Agent pool 的适配装配，使用现有 `agent.NewAgentDispatcherFactory` 需要的 stream 参数；将 Agent pool goroutine 纳入 Runtime supervisor；把“至少一个 tunnel 或 agent_id”作为唯一启动前置条件。
- [ ] **Step 4: 运行绿灯**：执行定向 runtime/CLI 测试，确认锁、停止、重连和错误传播正常。
- [ ] **Step 5: 重构**：隔离 client forward 和 embedded agent 的生命周期代码，避免让 CLI、tray 各自维护一套装配逻辑。

### Task 4: 独立 tunnelmesh-agent 自动读取并回写 ID

**Files:**
- Modify: `internal/cli/root.go`
- Modify: `internal/config/config.go`（新增通用安全 `InitializeAgentID`/YAML 回写 helper）
- Test: `internal/cli/agent_runtime_test.go`, `internal/config/config_test.go`
- Modify: `docs/user-guide/agent.md`

**Interfaces:**
- `GET /api/v1/agent/self` 返回的 `id` 是 `agent.token` 的服务端绑定 ID。
- `agent run` 在 `cfg.Agent.ID` 为空时，先使用 `agent.server_url`、`agent.token` 查询 self；成功后原子回写 `agent.id` 并继续现有连接池启动。
- 非空 `agent.id` 不查询或覆盖；若服务端绑定 ID 与配置 ID 不一致，启动失败并明确指出冲突。
- 查询失败、token 类型错误、服务端不支持接口或回写失败均快速失败，不生成随机 Agent ID。

- [ ] **Step 1: 写失败测试**：覆盖空 ID 查询成功并回写 YAML、保留注释/权限/其他字段；空 ID 查询失败；非空 ID 不被覆盖；配置 ID 与 token 绑定 ID 不一致时拒绝。
- [ ] **Step 2: 运行红灯**：`go test ./internal/cli ./internal/config -run 'Agent.*ID|AgentSelf|Backfill' -count=1`。
- [ ] **Step 3: 最小实现**：在 CLI run 之前调用 self endpoint，复用 `client.HTTPURLFromWebSocket` 生成 API URL；新增 YAML AST 原子回写 `agent.id`，不改变 token 处理和现有 Agent pool。
- [ ] **Step 4: 运行绿灯**：执行定向测试和既有 Agent runtime 测试。
- [ ] **Step 5: 文档**：更新最小配置、运行和故障排查，明确“agent.id 可省略，首次运行从 agent.token 对应节点回写；不会自动生成节点”。

### Task 5: macOS/Windows 托盘路由配置与说明

**Files:**
- Modify: `internal/tray/app.go`, `internal/tray/configstore.go`, `internal/tray/validate.go`, `internal/tray/api.go`
- Modify: `web-tray/src/api/types.ts`, `web-tray/src/stores/routing.ts`, `web-tray/src/views/RoutingView.vue`
- Modify: `web-tray/src/i18n/zh-CN.ts`, `web-tray/src/i18n/en-US.ts`
- Test: `internal/tray/*_test.go`, `web-tray/src/tests/routing-view.spec.ts`, `web-tray/src/tests/api.spec.ts`

**Interfaces:**
- `GET /api/routing` 返回 `agentId` 和配置摘要；`PUT /api/routing` 接受 `agentId`，保存前执行真实 config validation。
- 路由 Tab 在 server URL/token 下方增加 `client.agent_id` 输入框/下拉；优先从 `/api/agents` 读取作用域内 Agent，允许输入服务端尚未存在的 ID 以触发首次自动注册。
- UI 明确标注：`client.agent_id` 配置后，客户端会同时作为 Agent；复用当前 client token；首次连接时若不存在会自动创建同名 Agent；留空则仅运行 client 隧道。
- `client.agent_id` 非空且 tunnels 为空时，保存、检测和启动按钮均可用；agent-only 状态在统计页显示。

- [ ] **Step 1: 写失败测试**：Go API round-trip 不泄露 token；Vue 渲染字段、说明文案、保存 payload、agent-only 空 tunnels、已有 Agent 下拉和手工输入回归。
- [ ] **Step 2: 运行红灯**：`go test ./internal/tray -run 'Routing|AgentID' -count=1`；`cd web-tray && npx vitest run src/tests/routing-view.spec.ts`。
- [ ] **Step 3: 最小实现**：扩展本地 API、store 和表单；保持 token masked；复用已有 agent picker，新增“自定义 agent_id/首次连接自动注册”提示。
- [ ] **Step 4: 运行绿灯**：定向 Go/Vitest 测试通过。
- [ ] **Step 5: UI 复核**：确认 macOS WKWebView 与 Windows WebView2 使用同一 bundle，未新增平台分叉；检查中英文、浅深色和窄窗口布局。

### Task 6: 文档、OpenAPI 索引与全量验证

**Files:**
- Modify: `docs/api/openapi.yaml`
- Modify: `docs/user-guide/client-tray.md`, `docs/user-guide/agent.md`, `docs/user-guide/client.md`（若存在）
- Modify: `docs/README.md`（仅由索引脚本生成）
- Create: `docs/pull-requests/2026-10-25-client-agent-role.md`

**Interfaces:**
- 文档必须描述 `client.agent_id`、agent-only 模式、client token scope、首次连接自动注册、独立 Agent ID 回写和失败回滚/排障。
- PR 描述包含目标分支 `main`、用户影响、API/配置/授权影响、安全边界、测试证据、发布和回滚步骤；不包含真实 token、DSN 或完整 Authorization。

- [ ] **Step 1: 写文档/契约测试**：补 OpenAPI schema 断言、配置示例和 UI 说明测试。
- [ ] **Step 2: 运行文档检查**：`python3 scripts/gen_doc_index.py`，随后 `git diff --check`，确认索引没有孤儿入口。
- [ ] **Step 3: 全量验证**：依次运行 `go test ./... -count=1`、`go test -race ./...`、`go vet ./...`、`cd web-tray && npm test -- --run && npm run build`、`git diff --check`。
- [ ] **Step 4: 平台构建验证**：在可用环境运行 `CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -tags tray ./cmd/tunnelmesh-client-tray`，并运行 Windows 交叉编译/守卫测试；无法在当前主机完成的原生 GUI 冒烟必须明确记录为未执行。
- [ ] **Step 5: 结果复核**：检查 `git diff --cached` 前不进行提交；确认没有 `.syso`、`node_modules`、token 或构建缓存进入变更集。

## 回滚注意事项

- Server 先发布 API/鉴权兼容改动，再发布 client/agent；旧 client/agent 仍按原 token 类型工作。
- 若自动注册引发错误资源创建，可关闭 client 的 `agent_id` 或回退 client/agent 二进制；已创建 Agent 记录必须由管理 API 显式禁用/删除，不由客户端自动删除。
- 若托盘保存失败，YAML 原子写回保留旧文件；若运行时启动失败，保留用户配置并在统计/检测页显示错误，不静默清空 `agent_id`。
