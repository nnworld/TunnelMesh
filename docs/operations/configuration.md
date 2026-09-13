# 配置说明

## 配置来源

支持 YAML、JSON、TOML 配置文件，也可以完全不使用配置文件：

```bash
tunnelmesh-server --mode local --storage.driver sqlite --storage.auto_init=true check-config
```

优先级从高到低：

1. 命令行参数
2. 环境变量（前缀 `TUNNELMESH_`，点号和连字符转换为下划线）
3. 配置文件
4. 内置默认值

Server、Agent 和 Client 的完整 YAML 可参考[三端配置文件示例](config-examples.md)。

## 本地模式示例

```yaml
mode: local
storage:
  driver: sqlite
  auto_init: true
  sqlite:
    path: /var/lib/tunnelmesh/tunnelmesh.db
registry:
  type: database
server:
  http_addr: :80
  https_addr: :443
  # 动态托管域名格式：<agent-id>-<a>-<b>-<c>-<d>-<port>.<dynamic_suffix>
  dynamic_suffix: apps.example.com
  tcp_bridge:
    enabled: true
    path: /ws/tcp
  webssh:
    enabled: true
    ticket_ttl: 30s
    session_ttl: 8h
    max_active_sessions_per_user: 5
    open_timeout: 10s
    idle_timeout: 5m
    max_message_bytes: 65536
```

```bash
tunnelmesh-server --config tunnelmesh.local.yaml check-config
tunnelmesh-server --config tunnelmesh.local.yaml run
```

## 集群模式示例

```yaml
mode: cluster
storage:
  driver: mysql
  auto_init: true
  mysql:
    dsn: tunnelmesh:${MYSQL_PASSWORD}@tcp(mysql:3306)/tunnelmesh?parseTime=true&tls=true
    # 可选；关闭时 DSN 中不要设置 tls=true
    tls: false
    ca: /run/secrets/mysql-ca.pem
registry:
  type: database
node:
  id: server-1
```

集群模式允许 MySQL 明文连接（`storage.mysql.tls: false`），但生产环境建议启用 TLS，并在 DSN 中设置 `tls=true`。关闭 TLS 时，DSN 里也不能残留 `tls=true`；程序会根据 `storage.mysql.tls` 覆盖 DSN 中的 `tls` 参数。

若配置文件中没有 `node.id`，且没有命令行或环境变量覆盖，Server 的 `run` 命令会生成小写 `server-<32位十六进制>` 身份，先复用 `/var/lib/tunnelmesh/node-id`，再把最终值回写到 YAML 的 `node.id`。回写采用原子替换，保留注释、权限和 owner；已有 `node.id` 或显式覆盖时不会改写。`check-config`、`print-config` 等只读命令不会修改文件。也可以显式执行：

```bash
tunnelmesh-server --config /etc/tunnelmesh/server.yaml init-node-id
```

systemd 打包单元会在非特权 `check-config` 和 `run` 之前，以 root 执行一次 `init-node-id`。因此 `/etc/tunnelmesh` 对服务用户可以保持只读，`/var/lib/tunnelmesh` 仍由服务用户写入。生成 `node.id` 后再签发 relay 证书；mTLS 证书 SAN 必须包含最终 `node.id` 的精确条目，并同时包含所有节点共同的 `server.relay.server_name`。

切换 etcd：

```yaml
registry:
  type: etcd
  endpoints:
    - https://etcd-1:2379
    - https://etcd-2:2379
```

## WebSSH/SFTP 会话

管理后台的 WebSSH/SFTP 使用一次性 ticket 建立 `/ws/webssh/<session-id>` 二进制 WebSocket。Server 只转发字节，不在服务端保存或解析 SSH 密码、私钥、终端输出或 SFTP 文件内容；浏览器负责 SSH/SFTP 协议。

```yaml
server:
  webssh:
    enabled: true
    ticket_ttl: 30s
    session_ttl: 8h
    max_active_sessions_per_user: 5
    open_timeout: 10s
    idle_timeout: 5m
    max_message_bytes: 65536
```

所有字段也可以通过命令行覆盖，例如：

```bash
tunnelmesh-server --server.webssh.enabled=true \
  --server.webssh.ticket_ttl=30s \
  --server.webssh.session_ttl=8h \
  --server.webssh.max_active_sessions_per_user=5 \
  --server.webssh.open_timeout=10s \
  --server.webssh.idle_timeout=5m \
  --server.webssh.max_message_bytes=65536
```

`ticket_ttl` 只表示 ticket 从创建到首次连接的等待时间；连接成功后由 `session_ttl`、`idle_timeout` 和用户主动关闭控制生命周期。`idle_timeout` 依赖底层 WebSocket 读超时能力；SSH keepalive 产生的流量会刷新该超时。`security.allowed_origins` 必须包含管理后台的精确 Origin，否则握手会被拒绝。

浏览器 SFTP 在客户端内分块传输，默认单文件上限为 1 GiB。该限制独立于 Nginx 的 `client_max_body_size`，因为文件不通过管理 API 上传；如果部署反向代理其它大请求路径，请分别评估限制。

终端内的 lrzsz（ZMODEM）收发不需要任何服务端开关：协议栈运行在浏览器中，Server 只转发字节。单个 WebSocket 帧仍受 `max_message_bytes` 约束（默认 64 KiB，上限 1 MiB），lrzsz 的数据块远小于该值，因此无需为 ZMODEM 调大该配置。ZMODEM 传输不走 SFTP 的 1 GiB 单文件限制，但接收方向会在浏览器内存中缓冲整个文件，超大文件请改用 SFTP 或其它带外通道。

### 凭据自动认证

远程服务器绑定带有认证秘密的凭据（`password` 类型，或已保存私钥的 `ssh_public_key` 类型）后，浏览器进入 WebSSH/SFTP 时可直接完成认证，不再弹出密码窗口。该能力依赖 Schema v13 与可恢复秘密加密密钥：

```bash
# base64（32 字节）或 hex；只能由环境变量或 Secret Manager 注入
TUNNELMESH_TOKEN_ENCRYPTION_KEY=$(openssl rand -base64 32)
TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID=prod-2026-09
```

密钥同时用于 service token 的可恢复密文和凭据秘密密文，集群内所有 Server 节点必须一致，否则某个节点无法解密在其它节点写入的秘密。未配置密钥时 Server 正常启动，但创建带秘密的凭据会快速失败并返回 `503`，自动认证不可用并回退到手动输入密码；Server 不会静默降级为明文存储。轮换密钥会使旧密文不可解密，受影响凭据需要重新填写秘密，因此请把密钥版本记录在 Secret Manager 中。

中继流控没有配置项，属于协议内置约定，改动会破坏 Server 与 Agent 的兼容性：

| 常量 | 值 | 位置 |
| --- | --- | --- |
| `MaxStreamFrame` | 32 KiB | `internal/protocol/window.go` |
| `DefaultServerReceiveWindow` | 512 KiB | 同上，Server 在 `OPEN_STREAM` 中通告 |
| `DefaultAgentReceiveWindow` | 256 KiB | 同上，Agent 通告并对超额直接 `RESET` |
| `DefaultWindowUpdateThreshold` | 128 KiB | 同上，两端每消费这么多字节回补一次窗口 |

浏览器侧的 1 MiB 发送高水位与 64 MiB 接收队列上限同样是内置值（`web/src/webssh/byte-stream.ts`、`web/src/webssh/ssh-client.ts`）。`server.webssh.max_message_bytes` 只限制单个 WebSocket 帧大小，与上述窗口无关，不要用它来“调大传输能力”。

## Agent 连接池

Agent 保持一个 `server_url`，但可以复用同一个逻辑 Agent 身份建立多条物理 WebSocket 连接。默认配置禁用扩容：

```yaml
agent:
  server_url: wss://tunnel.example.com/ws/agent/v1
  id: agent-devbox
  connections:
    min: 1
    max: 1
    high_watermark: 16
    low_watermark: 2
    evaluation_interval: 10s
    cooldown: 30s
```

`max` 大于 1 前必须完成所有入口 Server 的滚动升级。多实例和多连接的发布、监控与回滚步骤见[逻辑 Agent 连接池运维指南](connection-pool.md)。

## Stream 延迟与授权缓存

SOCKS5、HTTP 代理和 TCP 转发的打开行为由 capability 协商决定。旧版本链路继续使用 legacy 乐观打开；Client、Server 和 Agent 都支持 `stream_open_result.v1` 时，Agent 完成目标连接后才返回成功。

Server 默认配置：

```yaml
server:
  stream:
    max_concurrent_opens: 256
    max_pending_opens: 1024
    initial_window: 262144
    window_update_threshold: 131072
    max_frame_payload: 32768
  authorization_cache:
    enabled: true
    local_positive_ttl: 5s
    cluster_positive_ttl: 5m
    negative_ttl: 3s
    revision_poll_interval: 2s
    max_stale_on_poll_error: 5s
    max_entries: 100000
```

`authorization_cache` 使用数据库中的共享授权修订号失效。SQLite 正向缓存默认 5 秒；MySQL 集群默认 5 分钟，并通过 2 秒修订号轮询保证权限变更尽快生效。轮询失败超过 `max_stale_on_poll_error` 后缓存 fail-closed，新请求会回源数据库。所有 Token、用户、Agent 和策略变更必须与修订号更新处于同一数据库事务。

本节点创建、轮换或撤销 service token 时会立即通知本地缓存，因此本节点新流通常无需等待轮询周期。其它节点、直接数据库写入和旧版本节点仍依赖 revision 轮询。`/ready` 中的 `authorization_cache` 组件表示修订号源是否健康；轮询失败会让该组件变为 unhealthy，并在超过容忍时间后停止使用正向缓存。需要逐请求回源时可将 `authorization_cache.enabled` 设为 `false`，该配置只影响缓存，不会降低认证和授权检查强度。

Agent 默认使用有界拨号执行器，单个慢目标不会阻塞同一 WebSocket 上的其它 Stream：

```yaml
agent:
  streams:
    max_concurrent_dials: 32
    max_pending_dials: 128
    connect_timeout: 5s
    open_timeout: 8s
    inbound_buffer_bytes: 262144
```

Client 默认等待严格打开结果并限制入站缓冲：

```yaml
client:
  stream:
    open_timeout: 8s
    inbound_buffer_bytes: 262144
  remote_validation:
    positive_ttl: 15s
    negative_ttl: 2s
    timeout: 3s
    max_entries: 10000
```

`remote_validation` 只缓存外部校验的 allow/deny 决策，不缓存凭据或目标响应体；同键并发请求会合并。生产环境优先调小 `cluster_positive_ttl` 和 `remote_validation.positive_ttl`，在权限收敛速度和数据库/外部校验压力之间取得平衡。

## 管理员凭据

首次初始化数据库后，在同一数据库配置下创建管理员：

```bash
tunnelmesh-server --config tunnelmesh.yaml admin bootstrap
```

该命令仅在管理员不存在时创建账号并输出一次随机密码；已有管理员时会失败，不会覆盖现有账号。若凭据丢失，在同一数据库配置下执行：

```bash
tunnelmesh-server --config tunnelmesh.yaml admin regenerate-credentials --confirm
```

旧 Token 会被撤销。不要把命令输出写入共享日志或工单系统。

## Service token 配置

Service token 在后台 Tokens 页面创建，明文只返回一次。Agent 使用 `agent.token`，Client 使用 `client.token`，均可由环境变量覆盖：

```bash
export TUNNELMESH_AGENT_TOKEN='one-time-agent-secret'
export TUNNELMESH_CLIENT_TOKEN='one-time-client-secret'
```

`server_node` token 配置在 `server.relay.node_token` 字段。一个 token 可以服务多个 Server 节点：`scope.serverNodeIds` 为空表示 fleet token，允许所有启用且未逻辑删除的节点；非空时仅允许列表内节点。token 本身不替代节点身份，epoch 也必须一致。

`server.relay.endpoint` 可以留空。此时 Server 会读取 `server.relay.listen` 的端口：如果 listen 是具体 IP，则直接使用该 IP；如果是 `0.0.0.0`、`[::]` 或空 host，则选择本机第一个可用的非 loopback、非 link-local 地址，优先 IPv4。推导结果只保存在进程内，不回写 YAML。多网卡、容器 NAT 或跨网段环境应显式配置 endpoint。

relay 证书字段有两种合法状态：

- `ca/cert/key/server_name` 全部省略：明文模式，仍校验 token、节点状态和 epoch，但不提供传输加密或证书级节点身份；
- `ca/cert/key/server_name` 全部配置：mTLS 模式，路径必须为绝对路径，证书 SAN 必须包含本节点 `node.id` 和共同的 `server_name`。

部分填写 `ca/cert/key` 会被拒绝，避免意外降级。证书生成和轮换见 [Relay mTLS 证书生成与配置](relay-mtls.md)。生产环境不把 token 放进提交的配置文件；`print-config` 不会输出明文。

### Recoverable service-token secrets

Set `TUNNELMESH_TOKEN_ENCRYPTION_KEY` to a base64 or hexadecimal AES key (16, 24, or 32 bytes) and optionally set `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID`. The key must come from an environment-injected secret or secret manager and must not be committed to a file or database. New and rotated service tokens are encrypted with AES-GCM. Existing hash-only tokens cannot be revealed and must be rotated. Secret reveal is administrator-only, audited, requires `X-Token-Reveal-Confirm` plus `Idempotency-Key`, and returns `Cache-Control: no-store`.

The same key and key id also encrypt the SSH credential secrets (password, or private key plus passphrase) used by browser SSH/SFTP auto-authentication; see [WebSSH/SFTP 会话](#websshsftp-会话). Credential secrets are never exposed by a list or detail API and are decrypted only for the credential owner at session-creation time.

For multi-server deployments set the same high-entropy `TUNNELMESH_TRACE_SIGNING_KEY` on every server/relay node so hop signatures can be verified across the cluster. It is never returned in traceroute output.
