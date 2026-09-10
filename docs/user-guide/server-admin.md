# TunnelMesh Server 管理后台使用指南

## 登录与首次凭据

首次初始化数据库后，在 Server 主机执行以下命令创建管理员：

```bash
tunnelmesh-server admin bootstrap --config tunnelmesh.yaml
```

命令只在数据库中不存在管理员时成功，并在控制台输出一次管理员用户名和高强度密码。请立即保存并通过 HTTPS 访问后台登录页。数据库只保存密码哈希，不会再次显示明文密码。若管理员已存在，命令会拒绝执行。

如果凭据遗失，在 Server 主机执行：

```bash
tunnelmesh-server admin regenerate-credentials --config tunnelmesh.yaml --confirm
```

命令会撤销旧会话并输出新凭据。生产环境建议限制控制台和数据库访问权限。

## Dashboard

Dashboard 用于查看当前权限范围内的 Agent、在线租约、活动隧道、托管路由、有效 service token 和近期审计事件。接口失败时页面显示不可用并提供重试，不用 0 掩盖故障。

## 语言与账号安全

登录页会按浏览器语言自动选择简体中文或英文；右上角语言菜单可随时切换，选择保存在当前浏览器中。

“安全设置”可验证当前密码并修改新密码。密码修改不会撤销当前或其他已有登录 Token；账号被管理员禁用或逻辑删除后，后续鉴权会立即失败。

管理员可在“子账号”中创建、启用、禁用、重置密码、逻辑删除和恢复普通账号。创建/重置返回的临时密码只显示一次，响应使用 `Cache-Control: no-store`，不要写入工单、日志或浏览器存储。删除只设置 `deleted_at` 和禁用状态，Agent、路由、隧道、Token 与审计记录都会保留；恢复会清空 `deleted_at`。已删除用户名不能复用，管理员账号不能被这些接口操作。

本期 Schema 从 v5 升至 v6。启用 `auto_init` 时会执行 `migrations/incremental/v0005_to_v0006/` 中对应驱动的增量脚本；发布前先备份数据库并确认 DDL 权限。回滚应用时保留新增可空列，不执行破坏性反向 DDL。

## Agent 列表与详情

Agents 页面展示 Agent ID、名称、启用状态和能力。点击 Details 可查看 Agent 详情及运行时 metadata：

- 当前在线/过期（Stale）状态；
- node、epoch、revision、最后上报时间和更新时间；
- 集群内所有物理连接的 instance、connection、connection epoch、所属 Server 节点、Server 地址、活跃流、最后心跳和租约到期时间；
- 字段名称、来源类型（`file` 或 `env`）和值；
- 敏感字段显示为 Redacted，后台没有编辑上报值的入口。

Agent metadata 只能由 Agent 按 allowlist 上报。修改字段必须修改 Agent 配置并等待下一次上报；管理员不能通过 API 伪造上报数据。

Agent 详情中的连接列表来自数据库连接租约，并用当前 Server 的本地会话状态覆盖本节点连接，因此可以看到其他 Server 节点上的连接。点击“刷新”重新查询集群状态。每行可通过“关闭”断开一条精确的物理 WebSocket 连接；确认框会显示 Agent ID、连接 ID、connection epoch、所属 Server 节点和活跃流数量。关闭只影响该物理连接，不会禁用 Agent，也不会阻止 Agent 按 `connections.min` 自动重连。若要长期停止访问，应禁用 Agent 或撤销其 token。

远端 Server 节点不可达时，关闭请求返回 `503`，页面提示租约已保留。此时不要手工删除数据库租约；应先恢复远端 relay，再刷新列表重试。`409` 表示 connection epoch 已过期，通常发生在连接被替换后；刷新列表并使用新 epoch 即可。

## Agent Policy

Policy 页面限制 Agent 可访问的协议、目标 CIDR 和端口。建议按最小权限创建规则，例如 SSH 只允许 `tcp/22` 和明确的内网 CIDR。拒绝规则会在 Agent 侧返回 policy 错误，不会关闭其他隧道。

## Explicit Route 与 Wildcard Route

Routes 页面支持将某个域名/路径绑定到 Agent 的目标主机和端口。域名与路径组合必须唯一，写请求建议携带稳定的 `Idempotency-Key`。

动态泛域名格式为：

```text
<agent-id>-<ip-encoding>-<port>.apps.example.com
```

IP 和端口使用明文编码，便于排查；公网 Server 仍只暴露 80/443，不提供公网 UDP。

## Tunnel 状态

Tunnels 页面显示本地 forward、publish route 和连接状态。异常时先查看 Agent online/lease 状态，再检查 policy、目标端口和 Server 审计事件。停止或重试操作应使用同一隧道 ID，避免重复创建。Agent metadata 的 stale 状态由 WebSocket 会话租约决定：正常在线 Agent 会通过协议级 `PING/PONG` 自动续期，断线或心跳停止超过 metadata TTL 后才显示为 stale。

## 审计日志

Audit Logs 记录登录、凭据恢复、Agent/Policy/Route/Tunnel 操作、metadata 读取和 SSH/TCP proxy 上下文。列表按事件时间倒序显示，同一时间使用审计 ID 倒序作为稳定排序；分页继续沿用 cursor。表格上方可按时间范围、操作者、动作、资源类型和资源 ID 做服务端筛选，点击“查询”后从第一页加载，点击“重置”清空条件并恢复默认列表。列表显示时间、操作者、动作、资源类型和资源 ID；点击“详情”可查看该事件的结构化 JSON 详情。路由创建、更新和删除会记录 Agent、域名、路径、目标地址、端口和状态。日志不记录 metadata 明文、SSH 私钥、Token 明文或会话字节。

## 角色与权限

- `admin`：管理所有 Agent、Policy、Route、Tunnel、用户和审计日志。
- 普通用户：查看和操作自己拥有的 Agent 及其隧道，不能读取其他用户的 metadata。
- 任何角色都不能通过管理 API 修改 Agent 上报值。

所有 API 使用统一响应 `{ code, msg, data }`。分页使用 cursor；写请求应携带 `Idempotency-Key`，重试时复用相同 key。

## Service Tokens

Tokens 页面用于创建、查看、轮换和撤销 `agent`、`client`、`server_node` 三类服务凭据。普通用户只能为自己拥有且启用的 Agent 创建 Agent/Client token；`server_node` 仅管理员可创建。

列表中的“详情”会调用 `GET /api/v1/tokens/{id}` 展示完整的脱敏元数据，包括 ID、类型、所有者、绑定、授权范围、状态、时间戳和幂等重放标记；它不返回 Token 明文或哈希。对于仍处于 `active` 状态的 Token，可以使用“有效期”入口调用 `PATCH /api/v1/tokens/{id}` 修改过期时间。请求必须显式携带 `expiresAt`；传 `null` 表示永不过期，传未来时间表示缩短或延长有效期。也可以使用“修改范围”入口更新协议、目标 CIDR 和目标端口；未提交的字段保持不变，空数组表示不限制。该接口不能修改 Token 类型、所有者或 Agent/Node 绑定。已撤销或已过期的 Token 不能通过修改有效期或范围复活，应先轮换出新 Token。

创建或轮换成功后，明文 secret 只在对话框显示一次。配置 `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 后，数据库保存 AES-GCM 密文，管理员仍须通过显式 reveal API、确认头和审计流程读取；未配置密钥时保持 hash-only，旧 token 无法恢复。请立即复制到 Secret 管理系统；轮换会使旧 token 失效；撤销适用于泄露或设备退役。

## 配置与排障建议

优先级为命令行参数 > 环境变量 > 配置文件 > 默认值。上线前执行 `check-config`，确认本地 SQLite 或集群 MySQL、注册中心、80/443 地址和 TLS 配置正确。出现 401/403 时检查 token 与角色；出现 404 时检查 Agent ID、路由 Host/path 和 wildcard DNS；出现 stale 时检查 Agent WebSocket、租约和系统时间。

### Token secret reveal

The token list and normal token detail APIs never return bearer secrets. With the encryption key configured, an administrator can call `POST /api/v1/tokens/{tokenId}/reveal` with `X-Token-Reveal-Confirm` and a unique `Idempotency-Key`. The response is audited and marked `no-store`; treat the returned value as sensitive. Legacy tokens created before encryption must be rotated first.

### Logical traceroute

Run `POST /api/v1/agents/{agentId}/trace` to inspect the authenticated path from client through server/relay nodes to the agent, then read the result with `GET /api/v1/traces/{traceId}`. Regular users receive topology-safe hops. Administrators may set `includeSensitive=true` to see private addresses and peer certificate metadata. `includeSecrets` is rejected; use the dedicated reveal endpoint instead.
