# WebSSH 会话管理实施计划

日期：2026-09-12。状态：已实现（用户于 2026-09-12 确认 T1–T7 全量执行）。
实现与本计划一致，唯一偏差是 `WebSSHSessionPage.hasMore` 采用与仓库既有分页类型
（`CredentialPage`、`AgentPolicyPage`）一致的可选字段写法；验证证据见
`docs/pull-requests/2026-09-12-admin-webssh-sftp.md` 第六轮。

## 目标

让使用者在管理后台看到自己当前活跃的 WebSSH 会话并主动断开，解决遇到
“活跃 SSH 会话数已达上限”后没有自救手段的问题。可选任务 T7 把远端 shell 的
退出码透出到终端关闭态，并提供“重新连接”入口。

## 背景与证据

- 2026-09-12 已修复配额泄漏：桥接结束时 owning 节点回写 `closed` 并记录
  `close_reason`（`remote_closed` / `client_disconnected` / `server_closed` /
  `bridge_closed`）。但历史遗留行、以及浏览器在消费 ticket 后崩溃等极端路径仍可能
  留下 `active` 行，用户目前只能等 8h TTL 或重启 Server。
- 现有管理 API 只有 `POST /api/v1/remote-servers/{id}/ssh-sessions`、
  `GET|DELETE /api/v1/ssh-sessions/{id}`；前端 `closeWebSSHSession` 已存在但没有任何
  视图调用。
- `storage.WebSSHSessionRepository.ListActiveByOwner` 已存在，但无分页、未暴露到 API。
- `idle_timeout` 已在 `internal/server/webssh_broker.go` 的 `copyBrowserToAgent`
  （浏览器侧读 deadline）生效，本计划不重复实现服务端 idle 执行。

## 架构决策

1. 只暴露“自己的活跃会话”。跨用户会话管理需要新的授权模型与审计语义，超出本次问题
   范围；遵循 YAGNI，管理员同样只列自己的会话。
2. 不新增菜单与路由。在“远程服务器”页增加“活跃会话”卡片：会话的语义入口就是远程
   服务器，且避免同时改动 `AppShell.vue` 菜单断言、`breadcrumbs` 与 `router.ts`。
3. 不改 Schema。列表读现有 `webssh_sessions` 表，cursor 使用 `id`，与
   `credentialRepo.List` 的游标语义一致。
4. 复用现有分页与响应设施：`pageArgs`、`decodeCursor`、`storage.Page[T]`、
   `pageData`、`queryLimit`；响应统一 `{code,msg,data:{items,nextCursor,hasMore}}`。
5. 断开复用现有 `DELETE /api/v1/ssh-sessions/{id}`（Service.Close → broker
   CloseLocal → 桥接结束回写 closed），不新增关闭接口。

## 技术栈

Go（`internal/storage`、`internal/server`）、Vue 3 + TypeScript + Element Plus
（`web/`）、vitest、go test。无新依赖。

## 全局约束

- Handler → Service → Repository 分层；权限只在服务端判定，不信任客户端 owner。
- 列表接口 cursor 分页；统一响应包裹；OpenAPI 与用户文档同步；i18n 中英文同步。
- 严格 TDD：每个任务先写失败测试并确认失败，再最小实现，再补边界。
- 未经另行明确授权，不执行 commit / push / merge。

## 任务 T1：storage 分页列出某用户活跃会话

文件：`internal/storage/repository.go`、`internal/storage/webssh_session_repository.go`、
`internal/storage/webssh_session_repository_test.go`

接口：在 `WebSSHSessionRepository` 增加
`ListActivePage(ctx context.Context, ownerUserID string, now time.Time, cursor string, limit int) (Page[WebSSHSession], error)`。
实现镜像 `credentialRepo.List`：条件
`owner_user_id=? AND status='active' AND expires_at>?`，cursor 非空时追加 `id>?`，
`ORDER BY id LIMIT ?`（limit+1 前瞻），经 `pageArgs`/`decodeCursor` 处理参数。

TDD：
1. 红：新增 `TestWebSSHSessionListActivePage`：写入 3 条 active + 1 条 closed；
   limit=2 时 items=2、hasMore=true、nextCursor 非空；用 nextCursor 取第二页 items=1、
   hasMore=false；closed 行不出现在任何一页。预期失败原因：接口方法不存在，编译失败。
2. 最小实现接口与 SQLite/MySQL 通用 SQL（仅使用 `?` 占位与现有 `tm()` 时间格式化）。
3. 绿：`go test ./internal/storage -run TestWebSSHSession -count=1`。
4. 补边界：limit=0/负数走 `pageArgs` 默认；cursor 指向不存在的 id 时返回空页。

## 任务 T2：Service List 强制所有者范围

文件：`internal/server/webssh_session_service.go`、`internal/server/webssh_session_service_test.go`

接口：`func (s *WebSSHSessionService) List(ctx context.Context, actor auth.Principal, cursor string, limit int) (storage.Page[storage.WebSSHSession], error)`；
内部固定 `ownerUserID = actor.UserID`，nil receiver 返回 `ErrWebSSHSessionUnavailable`。

TDD：
1. 红：`TestWebSSHSessionServiceListIsOwnerScoped`：alice、bob 各 1 条 active；
   alice List 只含自己的会话；admin 身份 List 也只含 admin 自己的（本次不做跨用户）。
   预期失败原因：方法不存在，编译失败。
2. 最小实现：直接委托 T1 的 `ListActivePage`。
3. 绿：`go test ./internal/server -run TestWebSSHSessionService -count=1`。

## 任务 T3：API GET /api/v1/ssh-sessions

文件：`internal/server/api.go`、`internal/server/webssh_api.go`、
`internal/server/webssh_api_test.go`、`docs/api/openapi.yaml`

路由：`api.go` 的 `case "ssh-sessions"` 在现有 `len(parts)==2` 分支前增加
`len(parts)==1 && r.Method == http.MethodGet` → `a.listWebSSHSessions(w, r, p)`。
Handler：`queryLimit(r)` 与 `r.URL.Query().Get("cursor")` → `websshService.List` →
`writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))`，
items 逐个经现有 `publicWebSSHSession` 映射。
OpenAPI：新增 `/ssh-sessions` get，参数 cursor/limit，responses 200（items 复用
`WebSSHSessionEnvelope` 的 data 形状，参照 `/credentials` 的列表写法）与 401。

TDD：
1. 红：`TestWebSSHSessionAPIListOwnOnly`：无 token 401；alice 只看到自己；limit=1 时
   两页并集等于 alice 的全部 active；响应含 `nextCursor`/`hasMore`。预期失败：404。
2. 最小实现路由与 handler。
3. 绿：`go test ./internal/server -run TestWebSSHSession -count=1`。

## 任务 T4：前端 API 客户端

文件：`web/src/api/webssh.ts`、`web/src/tests/webssh-api.spec.ts`

接口：`export type WebSSHSessionPage = { items: WebSSHSession[]; nextCursor?: string; hasMore: boolean }`；
`export function listWebSSHSessions(params: { cursor?: string; limit?: number } = {})`，
query 拼接 `cursor`/`limit`，返回 `api<WebSSHSessionPage>('/ssh-sessions?...')`。

TDD：
1. 红：spec 断言 `fetch` 以 `/api/v1/ssh-sessions?limit=50` 与带 `cursor=` 的形式被调用。
2. 最小实现。
3. 绿：`cd web && npx vitest run src/tests/webssh-api.spec.ts`。

## 任务 T5：远程服务器页“活跃会话”卡片

文件：`web/src/views/RemoteServers.vue`、`web/src/tests/remote-servers-view.spec.ts`、
`web/src/i18n/messages/zh-CN.ts`、`web/src/i18n/messages/en-US.ts`

UI：在工具栏卡片之前插入 `tm-card`，标题 `remoteServers.sessions.title`；`el-table`
列：目标（`remoteServerId` 关联本页已加载服务器名称，缺失显示 id 并标记
`unknownTarget`）、创建时间、操作列“断开”（`el-popconfirm` 确认后调用现有
`closeWebSSHSession(id)`，成功后 `ElMessage` 提示并刷新列表）；空态
`remoteServers.sessions.empty`；失败态 `remoteServers.sessions.loadFailed` + 重试。
i18n 双语新增 `remoteServers.sessions`：`title, empty, loadFailed, disconnect,
disconnected, disconnectFailed, target, createdAt, unknownTarget`。

TDD：
1. 红：view spec 断言源码包含 `listWebSSHSessions`、`closeWebSSHSession`、
   `remoteServers.sessions.title`，且 zh/en 均含 `sessions:` 与 `disconnect:`。
2. 最小实现视图与文案。
3. 绿：`cd web && npx vitest run src/tests/remote-servers-view.spec.ts`。

## 任务 T6：文档

文件：`docs/user-guide/server-admin.md`

在“远程服务器与浏览器 SSH/SFTP”章节追加“活跃会话”段落：卡片含义、断开语义
（等价 DELETE 单条会话）、配额立即释放、历史会话以审计日志为准。

## 任务 T7（可选，需与计划一并确认）：终端关闭态透出退出码 + 重新连接

文件：`web/src/webssh/ssh-client.ts`、`web/src/webssh/ssh-client.spec.ts`、
`web/src/views/WebSSHTerminal.vue`、`web/src/tests/webssh-terminal.spec.ts`、
`web/src/i18n/messages/zh-CN.ts`、`web/src/i18n/messages/en-US.ts`

接口：`onExit(cb: (info: { status: number | null; signal: string | null }) => void)`；
ssh-client 在 channel 读到 EOF/关闭后调用 `@verdigris/libssh2.js@^0.1.9` 已导出的
`_ssh2_channel_get_exit_status(channel): number` 与
`_ssh2_channel_get_exit_signal(channel): string`（见
`web/node_modules/@verdigris/libssh2.js/dist/libssh2.d.ts:259-260`）读取并透传，
不需要重建或替换 WASM 产物。
UI：closed 态在 `webssh.remoteExited` 之后追加 `webssh.exitStatus` 行（含 status 或
signal），并提供“重新连接”按钮：携带当前 sessionId 对应的 remoteServerId 回到
`/remote-servers` 并自动打开连接对话框。i18n 双语新增 `webssh.exitStatus`、
`webssh.reconnect`。

TDD：
1. 红：ssh-client spec 断言 `onExit` 回调收到 `{status: 1, signal: null}`；terminal spec
   断言源码含 `webssh.reconnect` 与 exitStatus 渲染 marker。
2. 最小实现。
3. 绿：`cd web && npx vitest run src/tests/webssh-terminal.spec.ts src/webssh/ssh-client.spec.ts`。

## 任务间接口

T1 → T2：`ListActivePage` 签名与 `storage.Page[WebSSHSession]`；
T2 → T3：`WebSSHSessionService.List` 签名；
T3 → T4：`data:{items,nextCursor,hasMore}` 形状与 `publicWebSSHSession` 字段；
T4 → T5：`listWebSSHSessions` / `closeWebSSHSession`；
T5 依赖同页已加载的 `remoteServers` 列表做名称映射；
T7 独立于 T1-T6，可单独裁剪。

## 验证命令

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web && npm test -- --run
cd web && npm run build
rsync -a --delete web/dist/ internal/server/web_dist/
./scripts/verify-web-embed.sh
go build ./cmd/...
```

## 回滚注意事项

纯增量 API、视图与文案；无 Schema、无配置项、无迁移。回滚即回退对应提交；已建立的
会话与配额不受影响。OpenAPI、i18n 与文档必须与代码同提交回退，避免契约漂移。

## 风险与边界

- 列表只含 `active`：历史会话不可见，审计走既有 `webssh.session.created/closed` 事件。
- 名称映射依赖本页服务器列表；服务器被逻辑删除时显示 id 并标记 `unknownTarget`。
- T7 依赖的 exit status/signal 绑定已确认存在于当前 `@verdigris/libssh2.js@^0.1.9`
  构建中；若运行期 channel 已释放或取值异常，回调退化为 `null`，UI 只显示通用文案。
