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

当前 Schema 版本为 v15。启用 `auto_init` 时会按 `schema_meta.version` 顺序执行 `migrations/incremental/` 中对应驱动的增量脚本；发布前先备份数据库并确认 DDL 权限，升级步骤、锁表影响与回滚注意事项见 [Schema 升级与回滚](../operations/schema-upgrades.md)。

## 单点登录、两步验证与受信任设备

管理后台的企业身份能力集中在两个入口：管理员在左侧菜单**单点登录**（`/sso-providers`）配置 OIDC
提供商与全局认证策略；所有用户在右上角头像菜单**安全设置**（`/account/security`）绑定 TOTP 两步验证、
保存一次性恢复码、查看和撤销自己的受信任设备、管理已关联的单点登录账号。

管理员还可以在**子账号**页对单个账号强制两步验证（`mfaRequired`），在账号详情侧查看其 MFA 状态、
受信任设备和外部身份关联，并执行 `mfa/reset` 帮助丢失验证器的用户恢复登录。

启用前必须先注入 `TUNNELMESH_TOKEN_ENCRYPTION_KEY`（集群所有节点一致），并确保
`security.allowed_origins` 或 `security.allowed_hosts` 已配置——OIDC 回调地址白名单由它们推导，
两者都为空时无法注册任何提供商。缺少密钥时 MFA 绑定与提供商创建返回
`503 secret_storage_unavailable`，不会退化成明文存储。

完整操作流程、字段取值范围、登录时序和按 `data.error` 归类的排障表见
[单点登录与两步验证](sso-and-mfa.md)。配置键含义见
[配置说明](../operations/configuration.md#管理台身份认证sso--mfa--受信任设备)，身份指标见
[可观测性](../operations/observability.md#身份认证sso--mfa--受信任设备)。

## Agent 列表与详情

Agents 页面展示 Agent ID、名称、启用状态和能力。点击 Details 可查看 Agent 详情及运行时 metadata：

- 逻辑 Agent 在线/离线状态；
- 活跃实例数按健康连接的实例 ID 去重统计；实例列表仍会显示历史过期实例；
- 实例列表中的连接数按该实例的健康连接数统计，不使用后端兼容字段；
- 最新上报时间和更新时间；实例列表继续展示 node、epoch、revision 等实例级字段；
- 集群内所有物理连接的 instance、connection、connection epoch、所属 Server 节点、Server 地址、活跃流、最后心跳和租约到期时间；
- 字段名称、来源类型（`file` 或 `env`）和值；
- 敏感字段显示为 Redacted，后台没有编辑上报值的入口。

Agent metadata 只能由 Agent 按 allowlist 上报。修改字段必须修改 Agent 配置并等待下一次上报；管理员不能通过 API 伪造上报数据。

Agent 详情中的连接列表来自数据库连接租约，并用当前 Server 的本地会话状态覆盖本节点连接，因此可以看到其他 Server 节点上的连接。点击“刷新”重新查询集群状态。每行可通过“关闭”断开一条精确的物理 WebSocket 连接；确认框会显示 Agent ID、连接 ID、connection epoch、所属 Server 节点和活跃流数量。关闭只影响该物理连接，不会禁用 Agent，也不会阻止 Agent 按 `connections.min` 自动重连。若要长期停止访问，应禁用 Agent 或撤销其 token。

远端 Server 节点不可达时，关闭请求返回 `503`，页面提示租约已保留。此时不要手工删除数据库租约；应先恢复远端 relay，再刷新列表重试。`409` 表示 connection epoch 已过期，通常发生在连接被替换后；刷新列表并使用新 epoch 即可。

## Client 运行观测与连接管理

Clients 页面用于查看当前用户权限范围内的客户端实例、metadata 和物理 WebSocket 连接。管理员可以按 Owner、Token、Server 节点、状态、Agent 和关键词筛选；普通用户的 Owner 过滤固定为自己的用户 ID，并在数据库查询内完成，不能通过分页绕过。

客户端会通过 metadata 子协议上报稳定实例 ID、版本、commit、平台、主机名、进程启动时间、本地监听入口、允许的 Agent 和非敏感自定义字段。自定义字段必须写入 Client 配置的 `client.metadata` allowlist，来源只能是绝对路径文件或指定环境变量。最多 32 项，单项 4 KiB，总 payload 32 KiB；包含 password、passphrase、token、secret、private key、api key、credential、authorization、cookie 或 DSN 语义的名称会被拒绝。metadata 只用于观测，不参与任何授权判断。

新版客户端鉴权后必须先发送 `CLIENT_HELLO`，之后才能发送 `CLIENT_METADATA_UPDATE`。未按顺序发送的更新会被拒绝，不会更新实例或连接租约。旧客户端不会协商 metadata 子协议，Server 会显示为 `metadata_unavailable`。

状态含义如下：

- `online`：至少一条连接租约未过期；
- `offline`：当前没有未过期连接租约；
- `stale`：metadata 已超过 TTL；
- `metadata_unavailable`：旧客户端未协商 metadata 子协议，仅能看到基础连接信息。

连接列表展示每条物理 WebSocket 的 connection ID、connection epoch、所属 Server 节点、Token、活跃流、健康分、获取时间、最后心跳和租约到期时间。关闭连接必须携带列表中的 connection epoch；本地连接直接关闭，远端连接通过已认证的 Server-node relay 控制通道转发。请求只影响这一条物理连接，客户端连接池可能自动重连。

关闭返回 `409` 表示 epoch 已过期，应刷新列表后使用新值；返回 `503` 表示目标 Server 节点不可达，durable lease 会保留，不要手工删除数据库记录；连接已经不存在时返回幂等成功。

## Agent Policy

Policy 页面限制 Agent 可访问的协议、目标 CIDR 和端口。建议按最小权限创建规则，例如 SSH 只允许 `tcp/22` 和明确的内网 CIDR。拒绝规则会在 Agent 侧返回 policy 错误，不会关闭其他隧道。

## Explicit Route 与 Wildcard Route

Routes 页面支持将某个域名/路径绑定到 Agent 的目标主机和端口。域名与路径组合必须唯一，写请求建议携带稳定的 `Idempotency-Key`。

动态泛域名格式为：

```text
<agent-id>-<ip-encoding>-<port>.apps.example.com
```

IP 和端口使用明文编码，便于排查；动态域名复用 Server 既有的 80/443 入口，不需要为每个 Agent 新开端口。

## Tunnel 状态

Tunnels 页面显示本地 forward、publish route 和连接状态。异常时先查看 Agent online/lease 状态，再检查 policy、目标端口和 Server 审计事件。停止或重试操作应使用同一隧道 ID，避免重复创建。Agent metadata 的 stale 状态由 WebSocket 会话租约决定：正常在线 Agent 会通过协议级 `PING/PONG` 自动续期，断线或心跳停止超过 metadata TTL 后才显示为 stale。

Agent 详情页的“逻辑 Agent 状态”按连接池健康状态推导：至少一条健康连接即在线。元数据租约只表示最近一次元数据上报是否过期，不再单独作为逻辑 Agent 的在线状态。实例状态同样按该实例是否存在健康连接推导，活跃实例数按健康连接的实例 ID 去重统计。

## Server 节点

管理员可在“Server 节点”页面查看集群 Server 清单。表格显示名称、节点 ID、relay 地址、epoch、状态、活跃连接、活跃流、健康分、最后心跳和租约到期时间；详情抽屉展示全部非敏感字段。

状态派生规则为 `deleted`、`disabled`、`online`、`offline`：逻辑删除优先，其次为禁用；启用且心跳租约未过期为 online，启用但租约已过期为 offline。Server 会在完成节点身份初始化后自动注册，并每 30 秒心跳一次，租约有效期 90 秒。

启用/禁用、逻辑删除和恢复都是管理员操作，会写入审计日志。删除只设置 `deleted_at` 并禁用节点，记录可恢复；恢复会清空 `deleted_at` 并重新启用。节点被禁用或删除后，即使持有有效 fleet token 和 mTLS 证书，relay 认证也会拒绝。

`node.id` 缺失时，Server `run` 会自动生成并回写 YAML。systemd 会在非特权服务前以 root 执行 `init-node-id`，因此 `/etc/tunnelmesh` 可以保持只读。每个节点仍必须使用独立 mTLS 证书，证书 SAN 与最终 `node.id` 精确一致。

## 远程服务器与浏览器 SSH/SFTP

左侧菜单包含“远程服务器”和“密钥管理”。远程服务器用于保存通过 Agent 通道访问的 SSH 目标；密钥管理用于维护 SSH 认证方式（公钥或密码）。两个页面都支持新建、编辑、详情、逻辑删除和恢复，列表使用 cursor 分页。

远程服务器字段包括名称、主机、端口、默认用户名、认证凭据、Agent 和启用状态。主机必须是 IPv4 地址，并且目标地址与端口必须命中所选 Agent 的访问策略；Server 会在保存时校验，Agent 建立连接时再次校验。列表会联查显示 Agent 名称、凭据名称和 Agent 在线状态；当绑定的凭据带有已保存的认证秘密时，凭据列会额外标记“自动认证”，表示点击“SSH 连接”或“SFTP 文件”可以直接进入而无需再输入密码。解绑凭据请使用“编辑”并把凭据清空后保存。

密钥管理支持两种类型：

- `ssh_public_key`（SSH 公钥）：可以直接粘贴 OpenSSH 公钥，也可以在受信任的 HTTPS 管理台中选择“提取公钥”，一次性上传私钥和可选口令让 Server 在内存中提取公钥。提取完成后私钥与口令立即丢弃，不写入数据库、日志或审计详情；数据库只保存公钥和指纹。不要在不信任的网络或浏览器环境中使用该功能。该类型额外提供“保存私钥用于自动认证”开关：开启后私钥与口令以 AES-256-GCM 加密保存在 Server，浏览器认证时一次性下发到内存，从而实现免密登录。
- `password`（密码）：只保存密码，加密方式与私钥相同。此类凭据没有公钥与指纹，列表中这两列为空。

保存认证秘密（密码、私钥、私钥口令）需要 Server 配置 `TUNNELMESH_TOKEN_ENCRYPTION_KEY`；未配置时创建请求返回 `503`，Server 不会静默降级为明文存储。秘密以密文入库，列表和详情 API 只返回 `hasSecret` 布尔值，任何接口都不会回显密文或明文。编辑时秘密输入框留空表示保持不变，填写后覆盖；切换凭据类型会清空已保存的秘密。凭据秘密只有所有者本人可以使用，管理员也无法读取他人凭据的秘密。

“SSH 连接”的认证方式取决于远程服务器绑定的凭据：

- **自动认证**：服务器绑定了 `password` 凭据，或绑定了已保存私钥的 `ssh_public_key` 凭据。此时不弹认证窗口，Server 在创建会话的响应中一次性下发解密后的秘密（响应头 `Cache-Control: no-store`，并写入 `credential.revealed` 审计事件，审计只记录凭据 ID）。浏览器把它保存在当前页面内存中完成 libssh2 认证，关闭页面、断开连接或发起新会话后即丢弃；Server 不缓存、不写入日志、不进入幂等重放响应。
- **手动输入密码**：服务器未绑定凭据、绑定的是未保存私钥的 `ssh_public_key` 凭据、Server 未配置 `TUNNELMESH_TOKEN_ENCRYPTION_KEY`，或自动认证失败时，页面回退到认证窗口。默认用户名来自服务器配置；密码每次连接时输入，只保存在当前浏览器内存中。也可以选择一个 SSH 公钥，浏览器使用本地对应私钥完成认证，这种模式下 TunnelMesh 不会保存或上传该私钥。

认证始终在浏览器内由 libssh2 完成，Server 只转发加密字节，不做服务端 SSH 认证。使用带保存秘密的凭据时请注意：私钥和密码保存在 Server 数据库中（加密），因此只在受信任的 HTTPS 管理台中配置，并配合最小权限账号与定期轮换。

连接前页面会显示目标 SSH 服务器的 SHA-256 指纹，必须人工确认后才会继续握手。指纹被拒绝时连接立即终止。终端使用浏览器内 SSH 客户端，Server 仅通过一次性 ticket 建立 `/ws/webssh/<session-id>` 二进制 WebSocket 并转发加密字节，不解析 SSH 协议，也不保存终端输出。

### 终端内使用 lrzsz（ZMODEM）

终端内置 ZMODEM 收发能力，远端主机安装 `lrzsz` 后即可直接使用，无需额外配置：

- `sz <文件>`：远端向浏览器发送文件。浏览器检测到 ZMODEM 会话后自动接受（不弹确认框），接收完成后按远端给出的文件名触发浏览器下载，文件落在浏览器默认下载目录。
- `rz`：远端从浏览器接收文件。页面自动打开文件选择框，支持一次选择多个文件，选定后按顺序上传到远端 `rz` 所在的工作目录。

传输期间终端上方显示传输面板，包含方向（正在上传 / 正在下载）、文件名、进度和“取消传输”按钮；取消会发送 ZABORT 终止会话。传输期间键盘输入不会发送到远端，避免把按键当作协议字节破坏传输；传输结束或取消后自动恢复输入。ZMODEM 协议字节与 lrzsz 的尾随提示不会污染终端画面，正常的 shell 输出照常显示。

### 大文件传输与通道流控

`sz`/`rz` 与 SFTP 传输都是长连接上的批量字节流，链路上每一跳都必须把背压传回发送方，否则快的一端会打爆慢的一端的缓冲区。TunnelMesh 的三层背压是内置的，**无需任何配置项**：

| 链路段 | 机制 | 默认额度 |
| --- | --- | --- |
| Agent → Server（下载方向） | Server 在 `OPEN_STREAM` 中通告接收窗口，Agent 按窗口发送；Server 在消费者真正读走字节后才回补 `WINDOW_UPDATE` | 窗口 512 KiB，单帧上限 32 KiB，每消费 128 KiB 回补一次 |
| Server → Agent（上传方向） | Agent 通告 256 KiB 接收窗口并对超额直接 `RESET`；Server 发送前扣减窗口，窗口耗尽时阻塞等待 `WINDOW_UPDATE` | 窗口 256 KiB，每消费 128 KiB 回补一次 |
| 浏览器 ↔ Server | WebSocket `bufferedAmount` 超过 1 MiB 时写入方等待排空（写入按 FIFO 串行化，保证字节顺序），SSH 接收队列上限 64 MiB | 高水位 1 MiB，硬上限 64 MiB |

Server 的中继接收缓冲按**字节**而不是按帧计数：窗口以字节为单位，如果对端用大量小帧填满同一个窗口，按帧计数的缓冲会先溢出并把一条健康的传输判死。这也是传输几 MB 文件时最常见的“中途断开、退出码 0”根因。

浏览器侧的出站方向同样有顺序保证：终端按键、ZMODEM 协议帧和 SFTP 请求共用同一条 SSH 通道，而 libssh2 的非阻塞写在窗口紧张时只消费部分字节、剩余部分需要续写。多个写者并发时续写会互相插队，在真实网络上表现为 `rz` 报 `ZRPOS`、`sz` 收不到 `OO` 等坏帧症状。前端因此对每条通道施加单写者闸门：任意时刻只有一个任务调用 libssh2 的写路径，排队顺序即调用顺序；SFTP 的目录列举等由多次请求组成的操作整体持闸，避免上传续写插进列举请求中间。闸门只排队微任务，不引入可感知延迟，也没有任何配置项。

集群模式下 Server 节点之间走 mTLS gRPC relay，背压由 gRPC 的阻塞式 `Write` 与 HTTP/2 流控天然提供，不需要额外的窗口协商。

排障要点：

- 传输中断时先看终端上方是否出现红色告警。出现告警说明是浏览器侧通道失败，不是远端退出。
- Server 日志中出现 `relay: backpressure` 或 `protocol: window exhausted` 说明有一端没有遵守窗口，通常意味着对端是旧版本，需要把 Server 与 Agent 升级到同一版本。
- 经 Nginx 反代时，`proxy_buffering off` 与足够长的 `proxy_read_timeout` 是批量传输不被中间层掐断的前提，见 [Nginx/WSS 推荐配置](../deployment/nginx.md)。

ZMODEM 协议栈完全在浏览器内运行（`zmodem.js`），Server 只转发字节，不落盘、不解析、不统计文件内容，单个 WebSocket 帧仍受 `server.webssh.max_message_bytes` 约束（默认 64 KiB，lrzsz 的数据块远小于该值）。ZMODEM 没有 SFTP 的 1 GiB 单文件限制，但接收方向会在浏览器内存中缓冲整个文件后才触发下载，超大文件请改用 SFTP 或其它带外通道。传输期间键盘输入按设计不会发往远端（面板已提示），这不是卡顿；传输结束或取消后立即恢复。远端未安装 `lrzsz` 时 `sz`/`rz` 会报“command not found”，请改用 SFTP 文件页面传输。

远端 shell 退出时终端会保留在页面上，先显示“远端会话已结束”，随后给出退出码（`远端退出码：N`）或终止信号（`远端被信号终止：SIGxxx`）。退出信息来自浏览器内 libssh2 通道，构建无法报告时只显示通用文案，不会伪造退出码。如果是浏览器侧 WebSocket 通道自身中断（例如网络切换、代理超时或传输缓冲耗尽），页面不会伪装成远端退出，而是在终端上方显示红色告警并给出真实原因（`SSH 通道异常中断（本地 WebSocket 传输失败），并非远端退出`），终端内同时写入该原因，便于与远端 `exit` 区分。终端下方提供“重新连接”按钮，会带着目标服务器 ID 返回“远程服务器”页并自动打开认证窗口；密码不做任何保留，需要重新输入。

同一已认证会话可切换到 SFTP 文件页面，支持目录浏览、上级目录、路径打开、上传、下载、重命名和删除。删除与重命名都有确认框。文件权限、路径权限、umask、chroot 和审计完全由目标 `sshd` 与操作系统决定。上传和下载在浏览器内分块传输，默认单文件限制为 1 GiB；超过限制会在传输开始前拒绝。Server 不提供文件 API，也不接触文件内容。

浏览器内的 SSH 会话是非阻塞的，单次写入允许只被消费一部分字节（返回值是已写入长度，不是错误）。客户端会按已写入偏移继续补写剩余数据，只有在连续 5 秒没有任何进度时才判定通道停滞并报错，因此大文件上传不会因为窗口暂时收紧而失败。上传失败时请先区分两种提示：`文件传输失败` 表示写入停滞或目标返回了 SFTP 错误，`SFTP 通道已关闭` 表示中继流被关闭，排查步骤见 [WebSFTP 上传失败](../operations/troubleshooting.md#websftp-上传失败)。

列表中的“SFTP 文件”也可以不经过终端直接打开文件管理：页面会先完成自己的 SSH 握手（同样需要确认主机指纹），并把已认证连接交给终端复用，因此之后点“返回终端”不需要重新输入密码。反过来，从终端切到 SFTP 也复用同一条连接，不会再次弹出指纹确认。握手或目录读取失败时，文件管理区域会显示带“重试”的错误提示，而不是空白目录；错误文案会带上浏览器内 libssh2 返回的原因，便于区分目标未开启 SFTP 子系统、Agent 策略拒绝或通道中断。

终端与文件管理页的“断开连接”会关闭会话并返回“远程服务器”列表：一次性 ticket 已消费，主动断开后停留在已关闭的页面上刷新无法重连，这是预期行为。

意外刷新（F5、浏览器崩溃恢复、误触刷新）不再是死路。一次性 ticket 与密码只存在于页面内存中，刷新后必然丢失，因此终端与 SFTP 页面会把**非敏感的目标服务器 ID** 记入 `sessionStorage`；重新加载时若发现 ticket 已失效，页面会自动跳转到“远程服务器”列表并带上 `?ssh=<服务器 ID>`，列表页据此自动重新发起连接（绑定自动认证凭据时直接进终端，否则弹出认证窗口），不会再停在“找不到当前浏览器会话中的 SSH 凭据”。密码、私钥和口令从不写入 `sessionStorage`。点击“断开连接”会同时清除该记录，避免刷新后把用户主动关闭的会话又拉起来；关闭标签页时 `sessionStorage` 自动失效。

`server.webssh.ticket_ttl` 控制一次性 ticket 从创建到首次 WebSocket 连接的等待时间；连接后由 `session_ttl`、`idle_timeout` 和用户关闭控制。`max_active_sessions_per_user` 限制每个用户的活动会话数，`max_message_bytes` 限制单个二进制帧大小。生产配置示例见[配置说明](../operations/configuration.md#websshsftp-会话)。

活动会话配额在桥接结束时立即释放：浏览器关闭页面、远端 shell 退出或服务端主动关闭都会由持有桥接的 Server 节点回写 `closed`，不需要浏览器调用任何接口。`close_reason` 会区分 `remote_closed`（目标 sshd 主动断开，例如主机侧准入拒绝）、`client_disconnected`（浏览器断开）、`user_closed`（在管理后台点击“断开”）、`server_closed`（Server 侧主动关闭桥接）、`bridge_closed`（桥接结束但未识别到具体原因）、`node_restarted`（Server 启动时清理本节点遗留会话）和 `agent_unreachable`。如果历史版本遗留了未回收的 `active` 会话导致“活跃 SSH 会话数已达上限”，重启 Server 会在启动时清理本节点遗留会话并输出 `webssh_stale_sessions_closed`。

“远程服务器”页面服务器列表下方的“活跃会话”卡片提供自助解套入口，对应 `GET /api/v1/ssh-sessions`（cursor 分页）。卡片只列出当前账号自己的活跃会话：不包含其他账号的会话，管理员同样只看到自己的会话，也不包含已关闭或已过期的历史记录。每行显示目标服务器、创建时间和到期时间；服务器名称按当前列表解析，若服务器已被逻辑删除或不在当前筛选结果中，则回退显示会话记录里的服务器 ID 并标记“未知服务器”。

点击“断开”等价于调用 `DELETE /api/v1/ssh-sessions/{id}`：Server 关闭对应桥接、把会话写为 `closed`（原因 `user_closed`）并立即释放活动会话配额，不必等待 `session_ttl` 到期。遇到“活跃 SSH 会话数已达上限”时，先在这里断开闲置会话再重连即可。卡片只反映当前活跃状态，历史会话需要追溯时查审计日志的 `webssh.session.created` 与 `webssh.session.closed` 事件。

创建会话失败时，认证窗口会显示 Server 返回的具体原因，而不是统一的“SSH 会话创建失败”。常见原因与处理方式如下：远程服务器不可用表示记录已被逻辑删除或禁用；Agent 不可用表示绑定的 Agent 已被禁用或不属于当前账号；Agent 离线表示该 Agent 当前没有有效租约，需要先检查 Agent 与 Server 的 WebSocket 连接；所选 SSH 公钥不可用表示公钥已删除、已禁用或不属于当前账号；活跃 SSH 会话数已达上限表示需要先断开闲置会话，或调大 `server.webssh.max_active_sessions_per_user`。

Server 进程启动时会把本节点遗留的 `active` 会话统一关闭（原因记为 `node_restarted`），避免异常退出后残留会话长期占用活动会话配额；关闭数量通过 `webssh_stale_sessions_closed` 日志输出。集群模式下如果某个节点被永久下线且不再重启，其残留会话仍由 `session_ttl` 到期回收。

## 审计日志

Audit Logs 记录登录、凭据恢复、Agent/Policy/Route/Tunnel/远程服务器/WebSSH 会话操作、metadata 读取和 SSH/TCP proxy 上下文。列表按事件时间倒序显示，同一时间使用审计 ID 倒序作为稳定排序；分页继续沿用 cursor。表格上方可按时间范围、操作者、动作、资源类型和资源 ID 做服务端筛选，点击“查询”后从第一页加载，点击“重置”清空条件并恢复默认列表。列表显示时间、操作者、动作、资源类型和资源 ID；点击“详情”可查看该事件的结构化 JSON 详情。路由创建、更新和删除会记录 Agent、域名、路径、目标地址、端口和状态。日志不记录 metadata 明文、SSH 私钥、SSH 密码、Token 明文、终端字节或 SFTP 文件内容。

## 角色与权限

- `admin`：管理所有 Agent、Policy、Route、Tunnel、Server 节点、用户和审计日志。
- 普通用户：查看和操作自己拥有的 Agent 及其隧道，不能读取其他用户的 metadata。
- 任何角色都不能通过管理 API 修改 Agent 上报值。

所有 API 使用统一响应 `{ code, msg, data }`。分页使用 cursor；写请求应携带 `Idempotency-Key`，重试时复用相同 key。

## Service Tokens

Tokens 页面用于创建、查看、轮换和撤销 `agent`、`client`、`server_node` 三类服务凭据。普通用户只能为自己拥有且启用的 Agent 创建 Agent/Client token；`server_node` 仅管理员可创建。

创建 `client` token 时，Agent ID 为多选框，可按名称或 ID 模糊搜索。留空表示允许该 Token owner 有权访问的所有 Agent；选择多个 Agent 会写入 `scope.agentIds` 显式允许列表。一个 Client Token 只能选择同一 owner 的 Agent；管理员选择其他用户的 Agent 时会自动携带对应 `ownerUserId`，混选不同 owner 会被拒绝。

创建 `server_node` token 时，Server 节点为多选框。留空表示 fleet token，可被所有启用且未逻辑删除的 Server 节点使用；选择节点则生成显式允许列表，仅列表内节点可用。Server 配置文件使用 `server.relay.node_token` 字段读取该 token。共享 token 不替代节点身份，每个 Server 仍使用独立 mTLS 证书并校验 SAN 和 epoch。

列表中的“详情”会调用 `GET /api/v1/tokens/{id}` 展示完整的脱敏元数据，包括 ID、类型、所有者、绑定、授权范围、状态、时间戳和幂等重放标记；它不返回 Token 明文或哈希。列表的“绑定”列会直接展示 Client Token 的 Agent 允许列表和 server_node Token 的 Server 节点允许列表；空列表分别显示为“所有 Agent”和“所有 Server 节点”。对于仍处于 `active` 状态的 Token，可以使用“有效期”入口调用 `PATCH /api/v1/tokens/{id}` 修改过期时间。请求必须显式携带 `expiresAt`；传 `null` 表示永不过期，传未来时间表示缩短或延长有效期。也可以使用“修改范围”入口更新协议、目标 CIDR、目标端口、Client Token 的 Agent 允许列表和 server_node Token 的 Server 节点允许列表；未提交的字段保持不变，空数组表示不限制。该接口不能修改 Token 类型、所有者或 Agent/Node 绑定。已撤销或已过期的 Token 不能通过修改有效期或范围复活，应先轮换出新 Token。

创建或轮换成功后，明文 secret 只在对话框显示一次。配置 `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 后，数据库保存 AES-GCM 密文，管理员仍须通过显式 reveal API、确认头和审计流程读取；未配置密钥时保持 hash-only，旧 token 无法恢复。请立即复制到 Secret 管理系统；轮换会使旧 token 失效；撤销适用于泄露或设备退役。

## 发行管理

“发行管理”是左侧菜单的最后一项，仅管理员可见。页面展示当前 Server 的版本、Commit、构建时间、Schema 版本和仓库地址，以及 SHA256SUMS 和 manifest 链接，并通过固定入口打开 GitHub Release：<https://github.com/nnworld/TunnelMesh/releases>。页面不再展示各平台下载、压缩包和校验命令。升级前请先备份数据库，并阅读当前版本的 Schema 升级与回滚说明。

## 配置与排障建议

优先级为命令行参数 > 环境变量 > 配置文件 > 默认值。上线前执行 `check-config`，确认本地 SQLite 或集群 MySQL、注册中心、80/443 地址和 TLS 配置正确。出现 401/403 时检查 token 与角色；出现 404 时检查 Agent ID、路由 Host/path 和 wildcard DNS；出现 stale 时检查 Agent WebSocket、租约和系统时间。

### Token secret reveal

The token list and normal token detail APIs never return bearer secrets. With the encryption key configured, an administrator can call `POST /api/v1/tokens/{tokenId}/reveal` with `X-Token-Reveal-Confirm` and a unique `Idempotency-Key`. The response is audited and marked `no-store`; treat the returned value as sensitive. Legacy tokens created before encryption must be rotated first.

### Logical traceroute

Run `POST /api/v1/agents/{agentId}/trace` to inspect the authenticated path from client through server/relay nodes to the agent, then read the result with `GET /api/v1/traces/{traceId}`. Regular users receive topology-safe hops. Administrators may set `includeSensitive=true` to see private addresses and peer certificate metadata. `includeSecrets` is rejected; use the dedicated reveal endpoint instead.
