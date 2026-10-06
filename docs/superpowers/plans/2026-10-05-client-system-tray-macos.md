# TunnelMesh Client 系统托盘（macOS）Implementation Plan

## 目标

在不改变现有 `tunnelmesh-client` CLI 命令面与外部行为的前提下，新增一个 macOS
系统托盘客户端 `tunnelmesh-client-tray`：

1. 菜单栏托盘，菜单为「打开主界面 / 打开官网首页 / 退出」。
2. 主界面为多 tab 设置窗口（通用 / 统计 / 路由配置 / 关于），默认约为屏幕 50%。
3. 托盘运行时**进程内**承载 client 隧道，与 CLI `client run` **互斥**，二者**共用**
   同一份 `client.yaml`。
4. 路由配置可从 Server 拉取当前 token 作用域内的 Agent 供选择，并提供逐项「检测」。

本期只交付 macOS；其它平台的托盘与互斥列为后续。

## 架构决策

- **ADR-沿用「重依赖 build tag 隔离」**：与 [ADR 0002](../../architecture/adr/0002-public-ingress-and-embedded-vpn.md)
  的 `//go:build vpn` 同构，所有 Cocoa/WebKit/cgo 代码置于 `//go:build tray && darwin`
  之后。默认 `go build ./...` / `go test ./...`（`CGO_ENABLED=0`、无 tag）不编译、不链接
  任何原生依赖，因此现有跨平台 release 矩阵与 CI 完全不受影响。
  由 `scripts/tray_build_tag_test.go` 结构性守卫（纯源码解析，不需要 cgo）。
- **共享运行时而非复制装配**：`internal/cli/client_run.go` 中 config→forward 的装配
  （`newConfiguredClientForward` / `clientRuntimeMetadata` / 监听失败监督）抽成
  `internal/client` 导出的 `Runtime`。CLI 与托盘共用同一实现，保证「共用现有配置」时
  语义一致、不漂移。`internal/client` 已 import `internal/config`（`metadata.go`），无环。
- **互斥用 advisory 文件锁**：锁路径由 config 路径派生（`<configDir>/client.lock`），
  Unix `flock(LOCK_EX|LOCK_NB)`。选 flock 而非 `O_EXCL` 哨兵文件，因为进程崩溃后内核自动
  释放，不会留下需要人工清理的陈旧锁。锁按 config 派生 ⇒ 「同一 client.yaml 互斥，
  不同 config 各自独立」，正是需求语义。
- **client 作用域 Agent 接口走 pre-auth 分支**：现有 `API.authenticate` 只认
  `api_tokens`（控制台登录态），client service token 在 `service_tokens`，无法复用。
  故新接口在 `ServeHTTP` 的 pre-auth 段（仿 `handlePublicAuth`）用
  `auth.CredentialService.ValidateAs(..., TokenTypeClient)` 自行鉴权 —— 这与
  `runtime.preauthenticateClientConnection` 认的是同一套凭据，语义统一。
- **窄接口注入而非新建实例**：`auth.NewCredentialService` 会起 `runLastUsedWorker`
  goroutine 且需 `Close()`，在 `NewAPI` 内再造一个会泄漏。故按 `SetVPNPeerService`
  既有先例，定义窄接口 `ClientTokenValidator` + setter，由 runtime 注入既有实例。
- **Token 写回 `client.yaml`**：`config.Load` 走 viper→mapstructure（`mapstructure:"token"`），
  因此文件里的 `client.token` 会被加载，尽管结构体带 `yaml:"-"`；而 `RedactedJSON`
  走 `json:"-"`，`print-config` 仍不泄露。现有 client 代码与标签零改动。
- **UI↔Go 用 loopback JSON API**：WKWebView 加载 `127.0.0.1` 随机端口，同一 server
  既发静态资源又提供 JSON API。每次启动生成随机 secret，随初始 URL 注入并逐请求校验，
  使本机其它进程无法驱动托盘或读取 token。

## 技术栈

- Go（`internal/client`、`internal/tray`、`internal/server`）。
- cgo + Objective-C（`NSApplication` / `NSStatusItem` / `NSWindow` / `WKWebView` /
  `SMAppService`），单一 shim 独占 Cocoa run loop，避免多库争抢主线程。
- Vue 3 + TypeScript + Vite + Element Plus + Pinia + vue-i18n（新 `web-tray/`，
  与 admin `web/` 完全独立）。

## 规格引用

- 需求：用户 2026-10-05 会话（托盘菜单、四 tab、通用项、路由配置项、检测按钮、
  窗口约屏幕 50%、token 写回 client.yaml、托盘替换原有 client 且互斥、共用现有配置）。
- 现有约束：`AGENTS.md`（分层、TDD、API 规范、build tag 隔离、文档同步）。
- 配置模型：`internal/config/config.go`（`ClientConfig`、`TunnelConfig`、`Validate`）。
- 运行时：`internal/client/session_pool.go`、`internal/client/forward.go`。

## 全局约束

- 不修改 `client.token` 的 `yaml:"-"` / `json:"-"` 标签。
- 不修改 admin `web/` 与其 embed 目录 `internal/server/web_dist`。
- 不把托盘二进制加入 `scripts/build-release.sh` 的 `CGO_ENABLED=0` 跨平台矩阵。
- 新增管理 API 必须同步 `docs/api/openapi.yaml`、`docs/README.md` 与用户帮助文档。
- 日志与响应不得包含 token、密码、私钥。
- 所有外部输入在边界校验；本地 API 绑定 loopback 且校验 secret。

## 精确文件清单

### 服务端（additive）
- `internal/server/client_scoped_api.go`（新）：`ClientTokenValidator` 接口、
  `SetClientTokenValidator`、`handleClientScoped`（pre-auth 分支）、`listClientAgents`。
- `internal/server/api.go`：`API` 增字段 `clientTokens`；`ServeHTTP` 在 `authenticate`
  前调用 `handleClientScoped`。
- `internal/server/runtime.go`：注入 `runtime.API.SetClientTokenValidator(credentialService)`。
- `internal/server/client_scoped_api_test.go`（新）：TDD 测试。
- `docs/api/openapi.yaml`、`docs/README.md`、`docs/help/`（用户帮助）。

### 共享运行时与互斥
- `internal/client/runtime.go`（新）：`Runtime`、`NewRuntimeFromConfig`、`Start`、
  `Stop`、`Wait`、`Stats`、`TunnelStatus`；装配逻辑自 cli 迁入。
- `internal/client/runtime_lock.go`（新）：`LockPathForConfig`、`acquireRuntimeLock`
  的平台无关入口与错误语义。
- `internal/client/runtime_lock_unix.go`（新，`//go:build unix`）：`flock` 实现。
- `internal/client/runtime_lock_other.go`（新，`//go:build !unix`）：明确「暂不支持」错误。
- `internal/client/runtime_test.go`、`internal/client/runtime_lock_test.go`（新）。
- `internal/cli/client_run.go`：`runClientTunnels` 改为委托 `client.NewRuntimeFromConfig`；
  删除已迁出的装配函数；外部行为不变。
- `internal/cli/serve_watch.go`：监督逻辑迁入 `internal/client` 后按需精简。

### 托盘 Go 核心（可移植、无 cgo，可常规单测）
- `internal/tray/paths.go`：配置目录解析（默认 `~/.config/tunnelmesh/`）。
- `internal/tray/prefs.go`：`tray.json` 偏好（语言/主题/开机启动/最小化/配置目录）。
- `internal/tray/configstore.go`：`client.yaml` 读写（含 token、0600、保留未知键）。
- `internal/tray/agents.go`：调用 `GET /api/v1/client/agents`。
- `internal/tray/validate.go`：「检测」逐项校验。
- `internal/tray/stats.go`：统计快照。
- `internal/tray/api.go`：loopback HTTP（静态资源 + JSON API + secret 校验）。
- `internal/tray/app.go`：编排（runtime 生命周期、动作、autostart 抽象）。
- `internal/tray/autostart.go`：`AutostartManager` 接口 + noop 实现。
- `internal/tray/webdist/embed.go` + `webdist/`（前端产物）。
- 对应 `*_test.go`。

### 原生外壳（`//go:build tray && darwin`）
- `internal/tray/native/native.go`、`internal/tray/native/tray_darwin.m`。
- `internal/tray/autostart_darwin.go`（SMAppService）。
- `cmd/tunnelmesh-client-tray/main.go`（tagged）与 `main_unsupported.go`（反向 tag，
  保证 `go build ./...` 在任何平台都通过）。

### 前端（新 `web-tray/`）
- `package.json`、`vite.config.ts`、`tsconfig.json`、`index.html`。
- `src/main.ts`、`src/App.vue`、`src/i18n/*`、`src/stores/*`、`src/api/*`、
  `src/views/{General,Stats,Routing,About}.vue`、`src/styles/*`、`src/tests/*`。

### 打包与守卫
- `scripts/package-macos-tray.sh`（新）、`scripts/tray_build_tag_test.go`（新）。
- `deploy/macos/TunnelMeshClient-Info.plist`（新）。

### 文档
- `docs/deployment/macos-client-tray.md`（新）、`docs/usage/` 客户端帮助更新、
  `docs/pull-requests/2026-10-05-client-system-tray-macos.md`（PR 阶段）。

## 任务间接口

```go
// internal/client
type RuntimeOptions struct {
    ConfigPath string          // 派生互斥锁路径；空则不加锁
    Stdout     io.Writer       // 每条隧道启动描述（CLI 复用现有输出）
    OnTunnel   func(TunnelStatus)
}
func NewRuntimeFromConfig(ctx context.Context, cfg config.Config, opts RuntimeOptions) (*Runtime, error)
func (r *Runtime) Start(ctx context.Context) error   // 先取锁，再起监听与会话池
func (r *Runtime) Wait(ctx context.Context) error    // 阻塞至结束，返回监督结果
func (r *Runtime) Stop() error
func (r *Runtime) Stats() RuntimeStats

type RuntimeStats struct {
    Running bool; StartedAt time.Time; Uptime time.Duration
    ServerURL string; Connected bool; Reconnects int64
    Tunnels []TunnelStatus
}
type TunnelStatus struct {
    Name, Protocol, ListenAddr, AgentID, Target string
    State string; ActiveStreams int; LastError string
}

// internal/server
type ClientTokenValidator interface {
    ValidateAs(ctx context.Context, raw string, expected storage.TokenType) (auth.TokenIdentity, error)
}
func (a *API) SetClientTokenValidator(v ClientTokenValidator)
// GET /api/v1/client/agents -> {code,msg,data:{items:[{id,name,online}],nextCursor,hasMore}}

// internal/tray
type AutostartManager interface {
    Enabled() (bool, error); SetEnabled(bool) error; Supported() bool
}
```

## TDD 步骤与预期结果

### Task 1 —— Server：client 作用域 Agent 接口
1. 写 `client_scoped_api_test.go`：未带 token→401；带 agent token→401；带 api_token→401；
   未注入 validator→503；owner 隔离（看不到他人 agent）；`scope.agentIds` 过滤；
   `enabled=false` 不返回；分页 `limit`/`cursor`/`hasMore`；`online` 由 lease 决定；
   非 GET→405。
   **预期失败**：`handleClientScoped` 未定义 / 路由返回 404 或 401。
2. 最小实现：`API.clientTokens` 字段 + setter；`ServeHTTP` pre-auth 分支；
   `listClientAgents` 复用 `a.service.ListAgents` 的 owner 过滤分页写法 + `onlineAgentIDs`。
   **预期通过**：上述用例全绿，`go test ./internal/server/... -count=1` 通过。
3. 重构：抽出 `clientAgentView`，补 OpenAPI 与文档。

### Task 2 —— Client：共享 Runtime 与互斥锁
1. 写 `runtime_lock_test.go`：同一 config 路径第二次 `acquireRuntimeLock` 返回
   `ErrRuntimeAlreadyRunning`；`Release` 后可再次获取；不同 config 路径互不影响；
   锁文件权限 0600；`ConfigPath` 为空时不加锁。
   写 `runtime_test.go`：用假 opener/本地回环 target 验证 `NewRuntimeFromConfig`
   对 tcp/udp/http/socks5/http-proxy 的装配与描述串和迁移前一致；缺 server_url/token
   或零隧道时报错文案不变；`Stats` 字段填充。
   **预期失败**：`client.NewRuntimeFromConfig` / `ErrRuntimeAlreadyRunning` 未定义。
2. 最小实现：迁移装配 + 加锁 + Stats。
3. 改 `runClientTunnels` 委托 Runtime，**现有 `internal/cli` 与 `internal/client`
   测试必须全绿**（行为保持证据）。
   **预期通过**：`go test ./internal/client/... ./internal/cli/... -count=1` 全绿。

### Task 3 —— Config/Token 回写
1. 写测试：含 `client.token` 的 yaml 经 `config.Load` 后 `cfg.Client.Token` 非空，
   且 `RedactedJSON` 输出不含该 token；tray 写出的 `client.yaml` 权限 0600；
   `mode: cluster` 且无 mysql DSN 时 `config.Validate` 报错（供 UI 预校验拦截）。
   **预期失败**：`internal/tray` 尚不存在。
2. 最小实现：`internal/tray/configstore.go`（yaml.Node 往返以保留注释与未知键）。
   **预期通过**：全绿。

### Task 4 —— Tray 本地 API / 偏好 / 检测 / 统计
1. 写 httptest 测试：无 secret→401；错误 secret→401；`GET/PUT /api/settings`；
   `GET/PUT /api/routing`（token 返回 masked，不回显明文）；`GET /api/agents` 代理；
   `POST /api/validate` 逐项结果；`GET /api/stats`；`POST /api/actions/{open-website}`；
   服务只绑定 loopback。
   **预期失败**：包不存在。
2. 最小实现 `internal/tray/{prefs,agents,validate,stats,api,app}.go`。
   **预期通过**：全绿，且 `go vet` 无告警。

### Task 5 —— 前端 `web-tray/`
1. 写 vitest：i18n 跟随系统/手动切换；主题浅/深/跟随（`html.dark` 与
   `prefers-color-scheme`）；四 tab 渲染；路由配置表单含协议下拉、agent 下拉、
   条目间横杠分隔、增删；检测按钮渲染逐项结果；token 输入 masked。
   **预期失败**：组件不存在。
2. 最小实现各视图与 store；`npm run build` 产物同步到 `internal/tray/webdist/`。
   **预期通过**：`npm test -- --run` 与 `npm run build` 均成功。

### Task 6 —— 原生外壳与打包
1. 写 `scripts/tray_build_tag_test.go`：任何 import `internal/tray/native` 或
   `import "C"` 的文件必须带 `//go:build tray`；`cmd/tunnelmesh-client-tray` 必须有
   反向 tag 的兜底文件。
   **预期失败**：守卫先于实现存在时会因缺失兜底文件而红。
2. 实现 ObjC shim、`autostart_darwin.go`、`main.go` / `main_unsupported.go`、
   `scripts/package-macos-tray.sh`、Info.plist 模板。
   **预期通过**：`go test ./scripts/... -count=1` 绿；macOS 上
   `CGO_ENABLED=1 go build -tags tray ./cmd/tunnelmesh-client-tray` 成功；
   打包脚本产出可启动的 `.app`（手动冒烟：菜单三项、窗口约 50%、关窗最小化/退出、
   开机启动开关、WKWebView 正常加载）。

## 验证命令

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web-tray && npm test -- --run && npm run build
# macOS 专属
CGO_ENABLED=1 go build -tags tray -o /tmp/tmtray ./cmd/tunnelmesh-client-tray
bash scripts/package-macos-tray.sh
python3 scripts/gen_doc_index.py
```

## 回滚注意事项

- 全部改动为 additive：删除 `internal/tray/`、`cmd/tunnelmesh-client-tray/`、
  `web-tray/`、`internal/server/client_scoped_api.go` 与两个 scripts 文件即可完全回退。
- 唯一触及既有代码的是 `internal/cli/client_run.go` 的委托重构与 `runtime.go` 的一行注入；
  回滚时用 `git checkout` 还原这两个文件即可，无数据迁移。
- **无 Schema 变更**，因此不涉及 `migrations/`、`SchemaVersion` 或增量脚本，
  属于 PATCH/MINOR 级代码变更，可随应用版本直接回退。
- 新接口未发布前无客户端依赖；若已发布需下线，先标记 Deprecated 一个版本再删除。
- 用户侧 `client.yaml` 新增 `client.token` 明文：回滚代码不会删除该文件，
  需在发布说明中提示用户可自行删除或改用 `TUNNELMESH_CLIENT_TOKEN` 环境变量。
