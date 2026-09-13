# WebSSH lrzsz(ZMODEM) 支持与凭据自动认证 实施计划

日期：2026-09-12。状态：已实现（用户于 2026-09-12 确认 T1–T8 全量执行）。

实现与本计划一致，另有两处计划外但必要的补充：一是修复 `internal/agent` 中既有的时序脆弱测试
`TestConnectionControllerRequiresServerAckAndRestartsFailedConnection`（固定 20ms sleep 与 `supervise`
的 20ms 退避对撞，约 2/3 概率失败），改为按截止时间轮询重启计数，使 `go test ./...` 门禁可复现；
二是 E2E harness 的 sshhost 升级为真实 pty shell（`creack/pty` + `/bin/zsh`，PATH 含真实 `sz`/`rz`），
以便用真实 lrzsz 二进制验证收发字节一致。

## 1. 目标

两个用户需求，均落在 WebSSH/SFTP 链路：

1. **终端支持 lrzsz**：远端执行 `sz`/`rz` 时，浏览器侧完成 ZMODEM 收发（`sz` → 浏览器下载文件；`rz` → 浏览器选择本地文件上传到远端），传输过程有可见进度与取消入口，传输期间终端输入不被当作 shell 输入发送。
2. **凭据类型扩展 + 自动认证**：密钥管理新增 `password` 类型；`ssh_public_key` 类型可选保存私钥（加密）。远程服务器绑定凭据后，进入 WebSSH 终端或 SFTP 页面时直接用凭据完成认证，不再弹认证窗口；仅当主机未配置凭据、或凭据不含可用秘密（例如只存了公钥）时，才回退到现有的手动输入密码窗口。

非目标：不实现服务器端 SSH 认证（认证始终在浏览器内 libssh2 完成，Server 只转发加密字节）；不做 ZMODEM 协议自研（使用 `zmodem.js`）；不做凭据共享/授权给其他用户（凭据仍仅所有者可用）。

## 2. 架构决策

### 2.1 ZMODEM：浏览器内协议栈 + 字节级拦截

- 依赖 `zmodem.js@0.1.10`（纯 JS、零运行时依赖、BSD 风格许可）。其 `Sentry` 正是为"终端字节流"设计：`consume(remoteBytes)` 把非 ZMODEM 字节交给 `to_terminal`，把协议字节留给内部状态机，回包经 `sender` 回调发出。这与本项目"channel 字节 ↔ xterm"的结构一一对应，不需要自研协议。
- 新增 `web/src/webssh/zmodem.ts`（`createZmodemBridge`）作为唯一封装点：输入远端字节、输出终端字节/通道字节/UI 事件。`WebSSHTerminal.vue` 只消费事件与调用 `sendFiles/abort`，不直接接触 `zmodem.js`。
- 方向语义：远端 `sz`（远端发送）→ Sentry 检测到 ZRQINIT → 接收会话 → `offer` 事件 → 浏览器组装 Blob 下载；远端 `rz`（远端接收）→ 检测到 ZRINIT → 发送会话 → UI 打开文件选择 → `Zmodem.Browser.send_files(session, files)`。
- 检测即确认（`detection.confirm()`）：终端里出现 ZMODEM 魔数只可能是用户主动运行了 rz/sz；误报可用"取消传输"止损（`session.abort()` 发送 ZABORT）。不做弹窗确认，避免打断脚本化传输。
- 传输期间 `terminal.onData` 的输入被丢弃（ZMODEM 对端期望协议字节而非键入），UI 显示传输面板；`session_end`/abort 后恢复。
- 终端字节管道改为保留 `Uint8Array`：`channel.read` 的原始字节先交给 bridge，bridge 的 `to_terminal` 再用流式 `TextDecoder`（`decode(chunk, {stream:true})`）写入 xterm，顺带修复多字节字符被 chunk 边界切断的隐患。
- 下载复用 SFTP 的 Blob+anchor 模式，抽公共helper `web/src/webssh/download.ts`（`downloadBlob(name, blob)`），SFTP 与 ZMODEM 共用，满足 DRY。

### 2.2 凭据秘密：复用现有 AES-GCM SecretStore，一次一密文交付浏览器

- 秘密（密码 / 私钥 / 私钥口令）以**单个加密 JSON blob** 入库：`{"password":...}` 或 `{"privateKey":...,"passphrase":...}`，使用现有 `internal/auth.SecretStore`（AES-256-GCM，key id + version，密钥来自 `TUNNELMESH_TOKEN_ENCRYPTION_KEY`）。选 blob 而非 9 列明文字段：一次加解密路径、一次迁移、后续加类型不改表。
- 列表/详情 API 只返回 `hasSecret` 布尔，绝不返回密文或明文；明文只在 `POST /api/v1/remote-servers/{id}/ssh-sessions` 的创建响应中出现一次（`auth` 字段），响应头 `Cache-Control: no-store`，并写审计 `credential.revealed`。浏览器沿用现有内存态（`PendingWebSSHSession`），关闭即丢弃。
- 未配置 `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 时 SecretStore 为 nil：创建带秘密的凭据直接报错（快速失败），自动认证不可用并回退手动窗口；不允许静默降级为明文存储。
- 所有权不变：远程服务器使用凭据前已有 `credential.OwnerUserID == actor` 校验，秘密只交付给凭据所有者本人。
- `ssh_public_key` 且未存私钥的凭据：无法自动认证，UI 回退手动窗口（保留"选择公钥 + 输入密码"现状）。

### 2.3 Schema：v12 → v13，只加可空列

- `credentials` 新增 4 个可空列：`secret_ciphertext TEXT`、`secret_nonce TEXT`、`secret_key_id VARCHAR(64)`、`secret_version INTEGER`。
- 密码类型凭据的 `public_key`/`fingerprint` 存空字符串（两列保持 NOT NULL），避免 SQLite 无法 MODIFY 列的问题；MySQL 与 SQLite 增量脚本因此完全同构（仅 ADD COLUMN）。
- 同步更新：`migrations/ddl.sql`、`migrations/incremental/v0012_to_v0013/{mysql.sql,sqlite.sql}`、`internal/storage/db.go` 的 `SchemaVersion = 13`、迁移测试、`docs/operations/schema-upgrades.md`。

## 3. 技术栈与规格引用

- Go：现有分层 Handler → Service → Repository；秘密加解密只在 Service（`CredentialService` / `WebSSHSessionService`），Repository 只存 base64 字符串。
- 前端：Vue 3 + Element Plus；新模块 `web/src/webssh/zmodem.ts`、`web/src/webssh/download.ts`；视图改动限 `WebSSHTerminal.vue`、`Credentials.vue`、`RemoteServers.vue`。
- 依赖：`web/package.json` 增加 `zmodem.js: 0.1.10`（精确版本）。
- API 规格：`docs/api/openapi.yaml` 同步 credentials 的 `secret`/`hasSecret`、remote-servers 的 `credentialType`/`credentialHasSecret`、ssh-sessions 响应的 `auth`。

## 4. 全局约束

- 秘密不得出现在：列表/详情 API、审计详情、日志、Prometheus、traceroute、错误消息。审计只记 `credentialId` 与动作。
- 长度上限：密码 4 KiB、私钥复用 `maxSSHPrivateKeyBytes`(64 KiB)、口令 1 KiB；超限在边界拒绝。
- 所有新接口行为补 OpenAPI 与用户文档；前端补 i18n 双语键（`i18n.spec.ts` 强制对齐）。
- TDD：每个任务先写失败测试并记录红输出，再最小实现。

## 5. 精确文件清单

新增：
- `web/src/webssh/zmodem.ts`、`web/src/webssh/zmodem.spec.ts`
- `web/src/webssh/download.ts`
- `web/src/tests/zmodem-terminal.spec.ts`
- `migrations/incremental/v0012_to_v0013/mysql.sql`、`migrations/incremental/v0012_to_v0013/sqlite.sql`

修改：
- `migrations/ddl.sql`、`internal/storage/db.go`、`internal/storage/models.go`、`internal/storage/credential_repository.go`（+contract 测试）
- `internal/server/credential_service.go`、`internal/server/credential_api.go`、`internal/server/credential_service_test.go`、`internal/server/credential_api_test.go`
- `internal/server/webssh_session_service.go`、`internal/server/webssh_api.go`、`internal/server/webssh_session_service_test.go`、`internal/server/webssh_api_test.go`
- `internal/server/remote_server_service.go`、`internal/server/remote_server_api.go`（响应补 `credentialType`/`credentialHasSecret`）+ 对应测试
- `internal/storage/mysql_test.go`、`internal/storage/sqlite_test.go`（迁移链测试）
- `web/package.json`、`web/package-lock.json`
- `web/src/api/credentials.ts`、`web/src/api/remote-servers.ts`、`web/src/api/webssh.ts`
- `web/src/views/Credentials.vue`、`web/src/views/RemoteServers.vue`、`web/src/views/WebSSHTerminal.vue`
- `web/src/views/WebSFTP.vue`（改用共享 download helper）
- `web/src/tests/credentials-view.spec.ts`、`web/src/tests/remote-servers-view.spec.ts`、`web/src/tests/webssh-terminal.spec.ts`、`web/src/tests/web-sftp-view.spec.ts`
- `web/src/i18n/messages/zh-CN.ts`、`web/src/i18n/messages/en-US.ts`
- `docs/api/openapi.yaml`、`docs/user-guide/server-admin.md`、`docs/operations/schema-upgrades.md`、`docs/operations/configuration.md`、`docs/pull-requests/2026-09-12-admin-webssh-sftp.md`

## 6. 任务拆解（TDD）

### T1 Schema v13 与 Credential 秘密字段（存储层）

接口：`storage.Credential` 增加 `SecretCiphertext/SecretNonce/SecretKeyID string`、`SecretVersion int`（零值=无秘密）；Repository Create/Update/List/Get 往返不丢字段；`CredentialFilter` 不变。
红：`credential_repository_test.go` 新增"秘密字段往返"用例，先红于 `unknown field SecretCiphertext`；迁移测试先红于 `schema_meta.version = 12, want 13`。
实现：ddl.sql 加 4 列；增量脚本两方言同构 ADD COLUMN；`SchemaVersion=13`；models/repository 补字段与扫描。
绿：`go test ./internal/storage/... -count=1`。

### T2 CredentialService 秘密写入与读取（服务层）

接口：`CredentialInput` 增加 `Secret CredentialSecretInput{Password, PrivateKey, Passphrase string}`；`Create/Patch` 按类型校验（password 必须有 Password；ssh_public_key 必须有 PublicKey，PrivateKey 可选）；加密为 JSON blob 后落库；`GetWithSecret(ctx, actor, id)` 仅所有者可解密返回明文结构；无 SecretStore 时创建带秘密凭据返回 `ErrSecretStoreUnavailable`。
红：先红于 `undefined: CredentialSecretInput` 与 `s.credentials.GetWithSecret`；再红于"无加密密钥时创建成功"（期望报错）。
绿：`go test ./internal/server/... -run Credential -count=1`。

### T3 凭据 API：secret 入参、hasSecret 出参、reveal 审计

接口：`POST/PATCH /api/v1/credentials` 接受 `secret:{password?,privateKey?,passphrase?}`；响应 `credentialResponse` 增加 `HasSecret bool`，永不含密文/明文；写入秘密时审计 `credential.secret-set`。
红：API 测试先红于 400/字段缺失与 `hasSecret` 断言。
绿：`go test ./internal/server/... -count=1`。

### T4 远程服务器响应补凭据能力位

接口：`remoteServerResponse` 增加 `CredentialType *string`、`CredentialHasSecret *bool`（list/get 均联查凭据表；凭据已删除时为 null）。
红：`remote_server_api_test.go` 断言新字段先红。
绿：同上。

### T5 WebSSH 会话创建交付一次性认证材料

接口：`websshTicketResponse` 增加 `Auth *WebSSHAuthPayload{Kind string; Password, PrivateKey, Passphrase string}`；仅当服务器绑定凭据且凭据有秘密且所有者匹配时填充；响应写 `Cache-Control: no-store`；审计 `credential.revealed`（details 只含 credentialId）。
红：`webssh_api_test.go` 先红于响应无 `auth`；越权用例（非凭据所有者）先红于 201（期望 403/409 现有语义）。
绿：`go test ./internal/server/... -count=1`。

### T6 前端凭据页与自动认证流程

- `Credentials.vue`：类型选择（ssh_public_key/password）；password 只显示密码输入（show-password）；ssh_public_key 保留公钥 textarea，并新增"保存私钥用于自动认证（可选）"textarea + 口令输入；提交后清空秘密输入。
- `RemoteServers.vue`：`openSSH/openSFTP` 先判断 `row.credentialHasSecret`：是 → 直接 `createWebSSHSession({username: row.defaultUsername, credentialId})`，用响应 `auth` 组装 `PendingWebSSHSession` 并跳转；否 → 现有认证窗口。创建失败仍显示 Server 原因。
- 红：`credentials-view.spec.ts`、`remote-servers-view.spec.ts` 标记断言（`credentialHasSecret`、`auth.kind`、`secret.password` 等）先红。
绿：`npm test -- --run` 相关 spec。

### T7 ZMODEM bridge 与终端集成

- `zmodem.ts`：`createZmodemBridge({toTerminal, toPeer, onEvent})`；事件：`detected(role)`、`send-required`、`receive-offer{name,size}`、`progress{transferred,total}`、`completed{name}`、`aborted`、`ended`、`error`；方法：`consumeFromRemote`、`sendFiles(File[])`、`abort()`、`active`、`dispose()`。
- `WebSSHTerminal.vue`：`channel.read` 原始字节 → bridge；bridge.toTerminal → 流式解码写 xterm；bridge.toPeer → `channel.write`；`terminal.onData` 在 `bridge.active` 时丢弃；传输面板（角色、文件名、进度、取消）；`send-required` 触发隐藏 `<input type=file multiple>`；接收完成调用 `downloadBlob`。
- 红：`zmodem.spec.ts` 先红于 `createZmodemBridge is not a function`；`zmodem-terminal.spec.ts` 标记先红。
- 用例：普通字节原样到终端且不发通道；ZRINIT 帧（用 `zmodem.js/src/zheader` 的 Header 序列化构造，保证 CRC 正确）→ 角色 send + `send-required` + 字节不进终端；ZRQINIT 帧 → 角色 receive，`start()` 后 `toPeer` 收到以 `**\x18B` 开头的 ZRINIT 回包；`abort()` 后 `active=false` 且 `toPeer` 收到 ZABORT；`session_end` 后终端输入恢复发送。
绿：`npm test -- --run`。

### T8 文档、OpenAPI、i18n、PR 记录

- `docs/user-guide/server-admin.md`：lrzsz 使用（sz/rz、传输面板、取消）、凭据类型与自动认证行为、回退条件。
- `docs/operations/configuration.md`：自动认证依赖 `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 的说明。
- `docs/operations/schema-upgrades.md`：v13 升级/回滚说明（只加可空列，滚动升级安全）。
- `docs/api/openapi.yaml` 与 PR 文档第九轮。

## 7. 验证命令

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web && npm test -- --run && npm run build
rsync -a --delete web/dist/ internal/server/web_dist/ && ./scripts/verify-web-embed.sh
```

浏览器端到端（复用 `/tmp/tm-e2e` harness，升级 sshhost 为真实 pty shell：`creack/pty` + `/bin/zsh`，PATH 含 homebrew lrzsz）：
1. 终端输入 `sz run.mjs` → CDP `Page.setDownloadBehavior` 指向临时目录 → 断言落盘文件与源文件字节一致（真实 ZMODEM 接收）。
2. 远端 `rz` → 页面出现文件选择 → CDP `DOM.setFileInputFiles` 注入本地文件 → 断言远端 cwd 出现同字节文件（真实 ZMODEM 发送）。
3. 配置 password 凭据的服务器：点"SSH 连接"不弹认证窗口直达终端；点"SFTP 文件"直达文件列表。
4. 未配置凭据的服务器：仍弹认证窗口（回退路径）。

## 8. 回滚注意事项

- v13 只加可空列：回退应用后旧二进制忽略新列，无需反向 DDL；秘密凭据在旧版本表现为"无秘密"，自动认证自动退化为手动窗口。
- ZMODEM 纯浏览器侧：回退前端产物即消失，Server 无开关、无状态。
- 已交付过明文的会话不受回滚影响（密码只在浏览器内存）。
