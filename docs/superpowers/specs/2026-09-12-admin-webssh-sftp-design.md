# 管理后台远程服务器与浏览器 SSH/SFTP 设计

## 1. 目标

在 TunnelMesh 管理后台中新增两个菜单：

1. **远程服务器**：维护可管理的服务器列表，并从浏览器发起 SSH 交互终端与 SFTP 文件操作。
2. **密钥管理**：维护 SSH 认证公钥，支持直接粘贴公钥，以及一次性上传私钥到 Server 提取公钥。

整体链路为：

```text
Admin browser
  → browser SSH/SFTP client
  → WebSSH/WebSFTP transport WebSocket
  → Server stream broker
  → existing Agent relay TCP stream
  → Agent
  → target server sshd
```

Server 在 SSH 会话中只转发字节流，不解析 SSH 协议，不保存 SSH 密码，不实现特权文件 API。私钥只允许在“提取公钥”接口中一次性进入 Server 内存，提取完成后立即丢弃，不写入数据库、日志或审计详情。目标 `sshd` 继续负责认证、授权、PTY、命令执行、SFTP 权限和审计。

## 2. 范围

### 本期包含

- 远程服务器 CRUD、逻辑删除、恢复、详情和状态展示。
- 密钥管理 CRUD、逻辑删除、恢复、详情、公钥格式校验和指纹展示。
- 从浏览器发起 SSH 会话，用户名可覆盖默认用户名。
- 密码认证每次连接时输入，只保存在浏览器内存中。
- 公钥认证使用已保存的公钥元数据；浏览器可支持用户提供一次性的本地私钥。
- 浏览器本地 SSH/SFTP 客户端，终端使用 xterm.js（官方包 `@xterm/xterm`）。
- SFTP 浏览、上传、下载、删除、重命名和目录切换，权限完全由目标 `sshd` 控制。
- 一次性 WebSocket ticket、会话生命周期、并发限制和审计。
- OpenAPI、用户手册、管理后台文档和 Schema 升级文档。

### 明确不包含

- Server 或 Agent 端 SSH 协议终止。
- Agent 原生 PTY、shell、任意命令执行 API。
- 持久化 SSH 密码、SSH 私钥、终端输出、SFTP 文件内容或文件暂存。
- Server 端单独的 SFTP 文件 API。
- 密钥的私钥托管、轮换和代用户签名服务。
- 对目标服务器的主动安全扫描。

## 3. 已选方案

### 浏览器本地 SSH 客户端

浏览器负责 SSH 协议和认证，Server 只提供经过授权的 WebSocket 字节流。这个方案的安全边界最清晰：

- Server 不接触 SSH 私钥、密码或会话密钥。
- 即使 Server 被攻破，攻击者也只能看到加密的 SSH 字节流，不能直接获得认证材料。
- SSH 算法、host key 校验、认证结果和 SFTP 授权都由目标 `sshd` 最终决定。

前端实现必须是浏览器可运行包：SSH/SFTP 协议客户端、`@xterm/xterm` 与 `@xterm/addon-fit` 终端组件。浏览器 WebSocket 是消息流，不是原始 TCP，因此必须实现一个双工二进制流适配层，并为 SSH 客户端提供兼容 Node `stream.Duplex` 的适配 socket。

依赖引入前必须先完成 Vite 生产构建和浏览器冒烟验证。若候选 SSH/SFTP 包需要模拟 `net`、`dns`、文件系统或其它不安全的 Node polyfill 才能运行，视为方案不满足 Scheme A，应停止实现并重新选型，而不是把风险带入后台。

### 私钥提取

用户可以把私钥一次性上传到 Server 提取公钥。该请求必须使用管理后台 HTTPS 连接，私钥和可选 passphrase 只存在于请求处理内存中；解析成功或失败后立即释放，不持久化、不记录、不下发给 Agent。响应返回规范化公钥和指纹，前端立即清空私钥和 passphrase 输入框。最终保存凭据时仍只提交公钥。

提取接口：

```http
POST /api/v1/credentials/extract-ssh-public-key
```

请求字段为 `privateKey` 和可选 `passphrase`，私钥正文限制为 64 KiB。解析使用 Go `golang.org/x/crypto/ssh.ParsePrivateKey` 与 `ParsePrivateKeyWithPassphrase`，响应必须设置 `Cache-Control: no-store`。

### 默认密码

远程服务器配置只保存 `default_username`，不保存默认密码。每次 SSH/SFTP 连接时由用户输入密码；密码仅存在于当前页面内存和 SSH 客户端会话中，关闭页面或断开连接后即丢弃。

## 4. 权限模型

- 管理员可以查看和管理所有远程服务器和密钥。
- 普通用户只能查看和管理自己拥有的资源。
- 一个远程服务器只能绑定同一 owner 的 Agent 和公钥。
- 逻辑删除后的远程服务器、Agent、密钥不能创建新 SSH 会话。
- Agent 禁用或离线时不能创建新 SSH 会话。
- Agent policy 必须允许所选协议、目标地址和端口；实际连接仍由 Agent 二次校验。
- 所有权限判断在 Server 端完成，前端只做可用性提示。

## 5. 数据模型

当前 Schema 版本为 11，本次升级为 12。变更必须同时维护：

- `migrations/ddl.sql`
- `migrations/incremental/v0011_to_v0012/mysql.sql`
- `migrations/incremental/v0011_to_v0012/sqlite.sql`
- `migrations/embed.go`
- `internal/storage/db.go`
- 迁移测试
- `docs/operations/schema-upgrades.md`

### credentials

```sql
CREATE TABLE credentials (
    id VARBINARY(255) PRIMARY KEY,
    owner_user_id VARBINARY(255) NOT NULL,
    name VARCHAR(191) NOT NULL,
    type VARCHAR(32) NOT NULL,
    public_key TEXT NOT NULL,
    fingerprint VARCHAR(128) NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    deleted_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(owner_user_id, name)
);
CREATE INDEX idx_credentials_owner ON credentials(owner_user_id, enabled, deleted_at, id);
```

约束：

- `type` 当前只允许 `ssh_public_key`。
- `public_key` 是公开材料，可以明文保存。
- 不添加私钥列，不添加密码列。
- 逻辑删除使用 `deleted_at`。
- 唯一性只在未删除数据中有效，因此应用层必须校验同名活动记录；SQL 唯一索引保留在当前扩展语义下，升级前如遇历史脏数据必须先人工清洗。

### remote_servers

```sql
CREATE TABLE remote_servers (
    id VARBINARY(255) PRIMARY KEY,
    owner_user_id VARBINARY(255) NOT NULL,
    name VARCHAR(191) NOT NULL,
    host VARCHAR(255) NOT NULL,
    port INTEGER NOT NULL,
    default_username VARCHAR(128) NOT NULL,
    credential_id VARBINARY(255),
    agent_id VARBINARY(255) NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    deleted_at TEXT,
    last_connected_at TEXT,
    last_result VARCHAR(32),
    last_error_class VARCHAR(64),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(owner_user_id, name)
);
CREATE INDEX idx_remote_servers_owner ON remote_servers(owner_user_id, enabled, deleted_at, id);
CREATE INDEX idx_remote_servers_agent ON remote_servers(agent_id, deleted_at, id);
CREATE INDEX idx_remote_servers_credential ON remote_servers(credential_id, deleted_at, id);
```

约束：

- `port` 范围 1–65535。
- `default_username` 不包含换行、控制字符，长度 1–128。
- `host` 由 Agent 侧 SSRF 和目标策略再次校验。
- `last_connected_at`、`last_result`、`last_error_class` 只保存摘要，不保存 SSH 错误原文。

> 设计取舍：逻辑删除与唯一索引在不同数据库中的组合语义不完全一致。本设计将唯一性作为应用层权威校验，并要求迁移前清洗活动重名数据；索引保留以加速查询和防止普通并发插入冲突。

### webssh_sessions

```sql
CREATE TABLE webssh_sessions (
    id VARBINARY(255) PRIMARY KEY,
    owner_user_id VARBINARY(255) NOT NULL,
    remote_server_id VARBINARY(255) NOT NULL,
    agent_id VARBINARY(255) NOT NULL,
    owner_node_id VARBINARY(255) NOT NULL,
    ticket_hash VARBINARY(255) NOT NULL,
    ticket_expires_at TEXT NOT NULL,
    status VARCHAR(32) NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    connected_at TEXT,
    closed_at TEXT,
    close_reason VARCHAR(64)
);
CREATE INDEX idx_webssh_sessions_owner ON webssh_sessions(owner_user_id, status, created_at, id);
CREATE INDEX idx_webssh_sessions_server ON webssh_sessions(remote_server_id, status, created_at, id);
CREATE INDEX idx_webssh_sessions_expiry ON webssh_sessions(expires_at);
```

会话采用乐观生命周期：

- `pending`：API 创建成功，ticket 尚未使用。
- `active`：ticket 已消费，WebSocket 已接入。
- `closed`：任一端关闭、超时或被用户/管理员终止。
- `expired`：ticket 或会话超时。

ticket 只保存 SHA-256 哈希，有效期默认 30 秒，只能消费一次。活动会话有效期默认 8 小时。

集群模式下 `owner_node_id` 记录实际接入 WebSocket 的 Server 节点。关闭活动会话时先更新数据库状态，再通过现有 mTLS Server-node relay 控制通道路由到 owner 节点关闭本地连接；owner 节点不可用时返回 503，由 sweeper 做最终超时收敛。

## 6. Repository 契约

新增三个接口和两个过滤器：

```go
type CredentialFilter struct {
    OwnerUserID string
    Type        string
    Status      string // active | deleted | all
    Keyword     string
}

type CredentialRepository interface {
    Create(context.Context, Credential) error
    Get(context.Context, string) (Credential, error)
    Update(context.Context, Credential) error
    Delete(context.Context, string, time.Time) error
    Restore(context.Context, string) error
    List(context.Context, CredentialFilter, string, int) (Page[Credential], error)
}

type RemoteServerFilter struct {
    OwnerUserID string
    AgentID     string
    Status      string
    Keyword     string
}

type RemoteServerRepository interface {
    Create(context.Context, RemoteServer) error
    Get(context.Context, string) (RemoteServer, error)
    Update(context.Context, RemoteServer) error
    Delete(context.Context, string, time.Time) error
    Restore(context.Context, string) error
    List(context.Context, RemoteServerFilter, string, int) (Page[RemoteServer], error)
    UpdateConnectionResult(context.Context, string, string, string, time.Time) error
}
```

`WebSSHSessionRepository` 提供创建、读取、原子消费 ticket、关闭、统计/列出活动会话和清理过期会话。原子消费必须在存储层使用条件更新，避免并发重复使用。创建会话时必须写入当前 Server 节点 `owner_node_id`，空值直接拒绝，保证集群关闭路由不歧义。

所有列表查询必须在 Repository 的过滤条件内完成 owner 过滤和逻辑删除过滤，不能先全局分页再丢弃无权数据。

## 7. API 设计

所有 API 前缀为 `/api/v1`，统一响应 `{ code, msg, data }`，列表使用 cursor。创建和替换请求使用 `Idempotency-Key`；PATCH、逻辑删除、恢复和关闭保持现有管理 API 约定。

### Credentials

```http
GET    /api/v1/credentials
POST   /api/v1/credentials
GET    /api/v1/credentials/{id}
PUT    /api/v1/credentials/{id}
PATCH  /api/v1/credentials/{id}
DELETE /api/v1/credentials/{id}
POST   /api/v1/credentials/{id}/restore
```

创建和替换请求字段：

```json
{
  "name": "team-ed25519",
  "type": "ssh_public_key",
  "publicKey": "ssh-ed25519 AAAA...",
  "enabled": true
}
```

响应包含 `id`、`name`、`type`、`publicKey`、`fingerprint`、`enabled`、`status`、`deletedAt`、`createdAt`、`updatedAt`。指纹使用 OpenSSH 格式的 SHA-256 表示。

### Remote servers

```http
GET    /api/v1/remote-servers
POST   /api/v1/remote-servers
GET    /api/v1/remote-servers/{id}
PUT    /api/v1/remote-servers/{id}
PATCH  /api/v1/remote-servers/{id}
DELETE /api/v1/remote-servers/{id}
POST   /api/v1/remote-servers/{id}/restore
```

创建和替换请求字段：

```json
{
  "name": "prod-web-01",
  "host": "10.0.0.12",
  "port": 22,
  "defaultUsername": "deploy",
  "credentialId": "cred-...",
  "agentId": "agent-...",
  "enabled": true
}
```

列表和详情额外返回：

- `credentialName`
- `agentName`
- `status`: `enabled`、`disabled`、`deleted`
- `agentOnline`: 布尔值，仅作为展示提示
- `lastConnectedAt`
- `lastResult`
- `lastErrorClass`

### WebSSH sessions

```http
POST   /api/v1/remote-servers/{id}/ssh-sessions
GET    /api/v1/ssh-sessions/{sessionId}
DELETE /api/v1/ssh-sessions/{sessionId}
```

创建请求：

```json
{
  "username": "deploy",
  "credentialId": "cred-..."
}
```

响应：

```json
{
  "sessionId": "webssh-...",
  "ticket": "wss-...",
  "websocketPath": "/ws/webssh/webssh-..."
}
```

浏览器随后用 `ticket` 作为一次性查询参数打开：

```text
GET /ws/webssh/{sessionId}?ticket=...
```

浏览器 WebSocket 不能设置自定义 `Authorization` 头，因此使用短效一次性 ticket。禁止把管理登录 token 放入 URL 或 Cookie。ticket 必须绑定用户、远程服务器和 Agent，且消费后立即失效。

## 8. WebSocket 传输协议

路径：`/ws/webssh/{sessionId}`

握手要求：

- `Origin` 必须命中 Server 配置的 allowlist。
- ticket 哈希必须匹配 `webssh_sessions` 记录。
- ticket 未过期且 `status=pending`。
- 会话 owner、远程服务器、Agent 必须仍有效。
- 当前用户活动会话数不超过默认值 5。

握手成功后，WebSocket 使用二进制消息承载 SSH 字节。Server 将 WebSocket 视为双工字节流，并在 WebSocket 与 Agent relay stream 之间双向复制。

协议语义：

- 浏览器发送的 binary message 是 SSH 客户端发出的上行字节。
- Server 发送给浏览器的 binary message 是从 Agent relay 读到的下行字节。
- 连接级关闭、读写错误和 context 取消必须同时关闭 WebSocket 和 relay stream。
- WebSocket 缓冲区写入失败时立即关闭，不无限排队。
- 半关闭语义由 SSH 客户端和目标 `sshd` 处理；Server 只传播字节流关闭。
- 不为 SSH 协议增加新的应用层 frame 类型。

### 浏览器流适配

浏览器端定义：

```ts
export interface DuplexByteStream {
  read(onChunk: (chunk: Uint8Array) => void): () => void
  write(chunk: Uint8Array): Promise<void>
  close(): void
  onClose(handler: () => void): () => void
}
```

`WebSocketByteStream` 将 WebSocket binary message 转为 `read` 事件，将 `write` 转为 `send()`。它必须处理：

- `ArrayBuffer` 与 `Blob` 输入统一转换。
- `bufferedAmount` 背压阈值，默认 4 MiB。
- `close()` 幂等。
- 重复 close 回调只触发一次。
- 打开前写入返回错误，不静默丢数据。

## 9. 前端设计

### 远程服务器

- 顶部筛选：名称/主机关键字、Agent、状态。
- 表格字段：名称、主机、端口、默认用户名、公钥、Agent、状态、在线状态、最近连接、操作。
- 操作：编辑、详情、SSH 连接、逻辑删除。
- 详情展示完整配置、最近连接摘要和 Agent 状态。
- 逻辑删除列表可通过状态筛选查看并恢复。
- SSH 连接前弹出配置确认：用户名、认证方式、密码或本地私钥。

### 密钥管理

- 顶部筛选：名称关键字、类型、状态。
- 表格字段：名称、类型、指纹、状态、创建时间、操作。
- 操作：编辑、详情、逻辑删除、恢复。
- 表单提供两个标签：
  - **粘贴公钥**：直接粘贴 OpenSSH authorized_keys 格式公钥。
  - **从私钥提取**：粘贴私钥，浏览器调用选定的浏览器版 `sshpk.parsePrivateKey()` 提取公钥，成功后立即清空私钥。
- 私钥输入区显示固定提示：`私钥只在本浏览器解析，不会上传或保存。`

### 终端页面

- 使用 xterm.js 渲染 PTY 输出。
- 支持窗口 resize，并把新尺寸通知 SSH channel。
- 支持关闭连接、重新创建会话和复制连接错误。
- 页面关闭或路由离开时主动关闭 WebSocket。
- 不记录终端输出。

### SFTP 页面

- 支持目录切换、文件/目录列表、上传、下载、删除、重命名。
- 上传与下载显示进度和错误。
- 大文件使用分块读写，避免一次性读取导致内存峰值不可控。
- 下载优先使用浏览器 File System Access API；不支持时使用 Blob 下载。
- 删除和重命名必须二次确认。
- 不在 Server 暂存文件，不显示或记录文件内容。

## 10. 可观测性

新增审计事件：

- `credential.create`
- `credential.update`
- `credential.delete`
- `credential.restore`
- `remote_server.create`
- `remote_server.update`
- `remote_server.delete`
- `remote_server.restore`
- `webssh_session.create`
- `webssh_session.connect`
- `webssh_session.close`

审计详情只包含资源 ID、Agent ID、用户 ID、结果类别、错误类别和时间，不包含密码、私钥、终端字节、文件内容或完整 SFTP 路径。

Prometheus 指标：

- `tunnelmesh_webssh_sessions_active`
- `tunnelmesh_webssh_tickets_created_total`
- `tunnelmesh_webssh_ticket_reuse_total`
- `tunnelmesh_webssh_streams_errors_total`
- `tunnelmesh_webssh_stream_duration_seconds`
- `tunnelmesh_webssh_bytes_total{direction}`

日志输出使用结构化事件，并携带 trace ID。禁止输出 SSH 认证材料、会话字节和文件内容。

## 11. 并发、超时与稳定性

- ticket 有效期默认 30 秒，一次性消费。
- pending ticket 超时后标记 `expired`。
- active 会话默认 8 小时超时。
- 单用户 active 会话默认最多 5 个。
- Server 与 Agent relay 打开超时默认 10 秒。
- WebSocket 空闲超时默认 5 分钟；实现必须支持连接级 read deadline。若现有 `WSConn` 接口无法表达该能力，则增加一个可选的 `SetReadDeadline(time.Time) error` 扩展接口，并为 x/net WebSocket 适配器实现它。SSH 协议自身的 keepalive 也会产生流量。
- 管理员或 owner 可主动关闭会话。
- 关闭操作必须幂等，重复关闭返回当前状态或 `204`。
- 会话清理器每 60 秒将过期 pending/active 会话标记为 expired/closed。
- 进程退出时通过 context 取消关闭所有本地 active 会话。

## 12. 安全要求

- 管理后台必须使用 HTTPS/WSS。
- WebSocket 握手必须校验 Origin allowlist。
- ticket 只保存哈希，不落库明文。
- 禁止在 URL 中携带管理登录 token。
- 禁止持久化 SSH 私钥、密码、终端输出和文件内容。
- Server 独立校验公钥格式和指纹。
- Agent 连接前再次执行 SSRF、回环、私网、链路本地、CIDR 和端口策略校验。
- Host key 校验必须默认启用；首次连接的 host key 变更必须显式确认。
- 所有资源读取按 owner 过滤，禁止信任客户端传入的 owner。
- API 错误使用稳定错误码，不透出目标系统原始错误细节。
- 上传和下载大小由浏览器与 SSH channel 可配置上限控制，默认单文件 1 GiB。

## 13. 测试策略

### 后端

- SQLite/MySQL Repository contract：CRUD、逻辑删除、恢复、owner 过滤、cursor、唯一名冲突。
- 迁移测试：v11 → v12、空库全量 DDL、`SchemaVersion=12`、缺迁移失败。
- API 测试：认证、越权、分页、幂等、校验、逻辑删除、恢复和错误响应。
- ticket 测试：过期、重复使用、错误用户、错误服务器、Origin 拒绝。
- WebSocket broker 测试：二进制透传、半关闭、关闭传播、背压、并发限制、panic 恢复。
- Agent policy 测试：禁用 Agent、离线 Agent、禁用密钥、禁止端口、禁止地址。

### 前端

- API client 单元测试。
- 表单校验和私钥提取清空测试。
- WebSocket 流适配测试：binary、背压、幂等 close。
- SSH 会话状态机测试。
- SFTP 操作与权限错误展示测试。
- 路由、菜单、i18n 和组件渲染测试。
- 生产构建测试。

### E2E

使用本地 SSH 测试容器：

1. Agent 连接 Server。
2. 管理后台创建服务器与公钥。
3. 打开 WebSSH 并完成密码认证。
4. 执行简单命令并验证终端输出。
5. 打开 SFTP，上传、下载、删除一个小文件。
6. 断开 Agent，验证会话关闭和错误提示。

## 14. 文档

必须同步更新：

- `docs/api/openapi.yaml`
- `docs/README.md`
- `docs/user-guide/server-admin.md`
- `docs/operations/schema-upgrades.md`
- `docs/deployment/nginx.md`，补充 `/ws/webssh/` 的 WSS 代理配置
- `docs/pull-requests/2026-09-12-admin-webssh-sftp.md`
