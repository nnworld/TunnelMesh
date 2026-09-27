# 三端配置文件示例

TunnelMesh 三端共用同一配置模型，但每个进程只使用与自身职责相关的配置段。配置优先级为：命令行参数 > 环境变量 > 配置文件 > 默认值。YAML 不展开 `${VAR}`，密码、Token 和密钥应通过环境变量或 Secret Manager 注入。

Server 只监听 `server.http_addr` 一个地址，管理 API、Web 后台、`/ws/agent`、`/ws/client`、`/ws/webssh/<session-id>`、`/ws/tcp` 和动态 HTTP 路由共用它；是否在同一个监听器上启用 TLS 由 `tls.enabled` 决定。`server.https_addr`、`server.agent_ws_addr`、`server.client_ws_addr` 仍会被加载但不会建立额外监听器，属于历史遗留键，示例中不再出现。

## Server：单机 SQLite

适合单节点部署。若 systemd 使用 `tunnelmesh` 用户运行，需保证 `/var/lib/tunnelmesh` 对该用户可写。

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
  # 非特权端口，建议由 Nginx/Caddy 对外终止 HTTPS。
  http_addr: 127.0.0.1:8080
  # 动态托管域名后缀；生产环境替换为实际 wildcard 域名。
  dynamic_suffix: apps.example.com
  tcp_bridge:
    enabled: true
    path: /ws/tcp
    max_bytes: 65536
  webssh:
    enabled: true
    ticket_ttl: 30s
    session_ttl: 8h
    max_active_sessions_per_user: 5
    open_timeout: 10s
    idle_timeout: 5m
    max_message_bytes: 65536
  stream:
    max_concurrent_opens: 256
    # 存量水位：同一 Agent 此刻保有的活跃流上限，0 = 不限。
    max_active_per_agent: 1024
    max_pending_opens: 1024
    initial_window: 262144
    window_update_threshold: 131072
    max_frame_payload: 32768
  authorization_cache:
    enabled: true
    local_positive_ttl: 5s
    negative_ttl: 3s
    revision_poll_interval: 2s
    max_stale_on_poll_error: 5s
    max_entries: 100000
  # 管理监听器的请求边界：一个端口同时承载后台页面、/api/v1 与已升级的
  # WebSocket，所以这里只约束请求头、请求体与 keep-alive 空闲，绝不设连接级
  # read/write 超时。取值依据与和 Nginx 的关系见
  # configuration.md「管理入口的超时、上限与保留策略」。
  http:
    read_header_timeout: 10s
    idle_timeout: 120s
    max_header_bytes: 1048576
    body_timeout: 30s
    # 0 = 不限制。Nginx 在前时容量由 limit_conn 决定；Server 直接暴露公网时
    # 必须设置与进程容量匹配的正数，超限新连接立即关闭而不排队。
    max_connections: 0
  agents:
    # 单个 Agent 身份在本节点可保有的物理 WebSocket 数量。
    max_connections_per_agent: 64
  metrics:
    # 空 = /metrics 匿名可抓取（与历史行为一致）。二选一：反向代理按来源网段
    # 放行，或用 TUNNELMESH_SERVER_METRICS_TOKEN 注入不少于 16 字符的 Bearer。
    # 该字段不会出现在 config dump 输出里。
    token: ""
  audit:
    # 0 = 永久保留。审计日志是证据，删除必须是显式决策；正数才会启动清理器，
    # 每小时按 1000 行一批删除，避免首次清理历史大表时长时间锁表。
    retention_days: 0
  # 内嵌 VPN 网关（WireGuard）。数据面在 -tags vpn 构建里：带 tag 且 enabled: true
  # 时进程真的监听 listen 指定的公网 UDP 端口并承载隧道；不带 tag 时只有管理面
  # （签发、列出、修改、轮换、吊销、审计），config:reveal 返回 409 vpn_node_disabled。
  # 不带 tag 却配 enabled: true 会启动失败并提示 rebuild with -tags vpn，不会静默不工作。
  # 全部键的取值范围见 configuration.md 的「内嵌 VPN 网关」一节，
  # 放行、密钥注入与 IP 池规划见 deployment/vpn-gateway.md。
  vpn:
    enabled: false
    # 独立的公网 UDP 端口，不经反向代理，需在防火墙单独放行并单独限流。
    listen: 0.0.0.0:51820
    # 下发给用户的 Endpoint 主机名，只写主机名不写端口，端口取自 listen。
    endpoint_host: gw-1.mesh.example.com
    # 集群里每个节点必须配置相同的 ip_pool 与 node_subnet_size，
    # 节点各自从池中租约一个 /24 子网（vpn_ip_leases，epoch fencing）。
    ip_pool: 10.64.0.0/16
    node_subnet_size: 24
    # 1500 - WireGuard 的 72 字节开销。
    mtu: 1420
    # 下面三个 0 表示不限。
    max_peers: 0
    max_flows_per_peer: 128
    max_flows_total: 0
    packet_rate_per_peer: 0
    connect_timeout: 10s
    idle_timeout: 120s
    shutdown_timeout: 15s
    # 节点级上限而非承诺：peer 还需单独开启，且出口 Agent 要协商到该能力，
    # 否则签发返回 409 vpn_agent_capability_missing。
    icmp_enabled: true
    icmp_timeout: 5s
    icmp_max_concurrent: 64
    # 节点自身的 WireGuard 私钥不是配置项，只由 TUNNELMESH_VPN_NODE_PRIVATE_KEY 注入。

security:
  allowed_hosts:
    - tunnel.example.com
  allowed_origins:
    - https://tunnel.example.com
  # Deprecated: 仅在存量 Agent 尚未换发 service token 的迁移窗口临时开启。
  allow_legacy_connection_tokens: false

# 后台“发行管理”页展示的仓库，格式 owner/name，无命令行参数。
downloads:
  github_repository: nnworld/TunnelMesh

tls:
  enabled: false
  min_version: "1.2"
```

## Server：集群 MySQL 明文 Relay

`node.id` 可省略。`run` 首次启动时会生成稳定 ID，复用 `/var/lib/tunnelmesh/node-id`，并把最终值回写到 YAML 的 `node.id`。需要提前初始化时，以有权限修改配置文件的用户执行：

```bash
tunnelmesh-server --config /etc/tunnelmesh/server.yaml init-node-id
```

systemd 单元会在服务用户运行前以 root 执行一次初始化，因此配置文件可以保持只读。

受控内网可以运行明文 relay：`endpoint` 留空时自动使用本机可用 IP 和 `listen` 端口；`ca/cert/key/server_name` 全部省略。明文模式仍要求 server-node token，并继续校验节点状态和 epoch，但不提供传输加密或证书级节点身份，只适合受控内网。

```yaml
# /etc/tunnelmesh/server.yaml
mode: cluster

storage:
  driver: mysql
  auto_init: true
  mysql:
    dsn: ""
    tls: false

registry:
  type: database

node:
  id: ""

server:
  http_addr: 127.0.0.1:8080
  dynamic_suffix: apps.example.com
  relay:
    enabled: true
    listen: 0.0.0.0:9443
    # 可省略；自动推导结果不回写 YAML。多网卡环境建议显式配置。
    endpoint: ""
    # 由 TUNNELMESH_SERVER_RELAY_NODE_TOKEN 注入。
    node_token: ""
  # 管理监听器的请求边界：一个端口同时承载后台页面、/api/v1 与已升级的
  # WebSocket，所以这里只约束请求头、请求体与 keep-alive 空闲，绝不设连接级
  # read/write 超时。取值依据与和 Nginx 的关系见
  # configuration.md「管理入口的超时、上限与保留策略」。
  http:
    read_header_timeout: 10s
    idle_timeout: 120s
    max_header_bytes: 1048576
    body_timeout: 30s
    # 0 = 不限制。Nginx 在前时容量由 limit_conn 决定；Server 直接暴露公网时
    # 必须设置与进程容量匹配的正数，超限新连接立即关闭而不排队。
    max_connections: 0
  agents:
    # 单个 Agent 身份在本节点可保有的物理 WebSocket 数量。
    max_connections_per_agent: 64
  metrics:
    # 空 = /metrics 匿名可抓取（与历史行为一致）。二选一：反向代理按来源网段
    # 放行，或用 TUNNELMESH_SERVER_METRICS_TOKEN 注入不少于 16 字符的 Bearer。
    # 该字段不会出现在 config dump 输出里。
    token: ""
  audit:
    # 0 = 永久保留。审计日志是证据，删除必须是显式决策；正数才会启动清理器，
    # 每小时按 1000 行一批删除，避免首次清理历史大表时长时间锁表。
    retention_days: 0
  # 内嵌 VPN 网关（WireGuard）。数据面在 -tags vpn 构建里：带 tag 且 enabled: true
  # 时进程真的监听 listen 指定的公网 UDP 端口并承载隧道；不带 tag 时只有管理面
  # （签发、列出、修改、轮换、吊销、审计），config:reveal 返回 409 vpn_node_disabled。
  # 不带 tag 却配 enabled: true 会启动失败并提示 rebuild with -tags vpn，不会静默不工作。
  # 全部键的取值范围见 configuration.md 的「内嵌 VPN 网关」一节，
  # 放行、密钥注入与 IP 池规划见 deployment/vpn-gateway.md。
  vpn:
    enabled: false
    # 独立的公网 UDP 端口，不经反向代理，需在防火墙单独放行并单独限流。
    listen: 0.0.0.0:51820
    # 下发给用户的 Endpoint 主机名，只写主机名不写端口，端口取自 listen。
    endpoint_host: gw-1.mesh.example.com
    # 集群里每个节点必须配置相同的 ip_pool 与 node_subnet_size，
    # 节点各自从池中租约一个 /24 子网（vpn_ip_leases，epoch fencing）。
    ip_pool: 10.64.0.0/16
    node_subnet_size: 24
    # 1500 - WireGuard 的 72 字节开销。
    mtu: 1420
    # 下面三个 0 表示不限。
    max_peers: 0
    max_flows_per_peer: 128
    max_flows_total: 0
    packet_rate_per_peer: 0
    connect_timeout: 10s
    idle_timeout: 120s
    shutdown_timeout: 15s
    # 节点级上限而非承诺：peer 还需单独开启，且出口 Agent 要协商到该能力，
    # 否则签发返回 409 vpn_agent_capability_missing。
    icmp_enabled: true
    icmp_timeout: 5s
    icmp_max_concurrent: 64
    # 节点自身的 WireGuard 私钥不是配置项，只由 TUNNELMESH_VPN_NODE_PRIVATE_KEY 注入。
```

## Server：集群 MySQL mTLS Relay

以下模板面向“Nginx 终止公网 HTTPS、Server 监听本机 HTTP、relay 使用独立 mTLS”的生产部署。MySQL DSN 和 server-node token 不写入 YAML，由环境变量注入。证书生成、安装、校验和轮换步骤见 [Relay mTLS 证书生成与配置](relay-mtls.md)。

```yaml
# /etc/tunnelmesh/server.yaml
mode: cluster

storage:
  driver: mysql
  # 首次启动自动初始化空库，或按 schema_meta.version 执行增量迁移。
  auto_init: true
  mysql:
    # 由 TUNNELMESH_STORAGE_MYSQL_DSN 注入；不要把密码提交到配置文件。
    dsn: ""
    # MySQL 5.6 不支持 TLS 时保持 false，并确保数据库端口仅在内网可达。
    tls: false

registry:
  # 集群默认使用 MySQL lease；不需要额外配置 endpoints。
  type: database

# 可省略。systemd 会在非特权启动前执行 init-node-id；
# 生成后会回写为 node.id: server-<32位小写十六进制>。
# relay 证书 SAN 必须包含最终 node.id。
node:
  id: ""

server:
  # 仅监听本机，由 Nginx/Caddy 转发；Agent/Client WebSocket 也走该监听器。
  http_addr: 127.0.0.1:8080
  # 只有来自 Nginx 的 X-Forwarded-For 才可信，用于登录限流桶和审计 IP。
  # Server 直接对公网暴露时必须留空，否则客户端可伪造该头自选限流桶。
  trusted_proxies:
    - 127.0.0.1/32
    - ::1/128
  # 必须与 DNS、证书和 Nginx server_name 的动态域名后缀一致，不要写 *。
  dynamic_suffix: apps.example.com
  tcp_bridge:
    enabled: true
    path: /ws/tcp
    max_bytes: 65536
  webssh:
    enabled: true
    ticket_ttl: 30s
    session_ttl: 8h
    max_active_sessions_per_user: 5
    open_timeout: 10s
    idle_timeout: 5m
    max_message_bytes: 65536

  # Server 节点间 relay。每个节点使用独立证书和私钥；
  # 所有节点使用同一 relay CA，并复用同一个 fleet token。
  relay:
    enabled: true
    # 本节点 relay gRPC 监听地址。
    listen: 0.0.0.0:9443
    # 其他 Server 节点可访问的本节点地址；不要填 127.0.0.1。
    endpoint: server-1.internal.example.com:9443
    ca: /etc/tunnelmesh/certs/relay-ca.pem
    cert: /etc/tunnelmesh/certs/server-1-relay.pem
    key: /etc/tunnelmesh/certs/server-1-relay-key.pem
    # 出站 relay 连接校验对端证书使用的 TLS ServerName。
    # 对端证书需要同时包含该名称和该节点最终 node.id SAN。
    server_name: relay.internal.example.com
    # 由 TUNNELMESH_SERVER_RELAY_NODE_TOKEN 注入；留空会导致 check-config 失败。
    node_token: ""
  # MySQL 集群可用较长正向缓存；共享 revision 保证权限变更快速失效。
  stream:
    max_concurrent_opens: 256
    # 存量水位：同一 Agent 此刻保有的活跃流上限，0 = 不限。
    max_active_per_agent: 1024
    max_pending_opens: 1024
    initial_window: 262144
    window_update_threshold: 131072
    max_frame_payload: 32768
  authorization_cache:
    enabled: true
    cluster_positive_ttl: 5m
    negative_ttl: 3s
    revision_poll_interval: 2s
    max_stale_on_poll_error: 5s
    max_entries: 100000
  # 管理监听器的请求边界：一个端口同时承载后台页面、/api/v1 与已升级的
  # WebSocket，所以这里只约束请求头、请求体与 keep-alive 空闲，绝不设连接级
  # read/write 超时。取值依据与和 Nginx 的关系见
  # configuration.md「管理入口的超时、上限与保留策略」。
  http:
    read_header_timeout: 10s
    idle_timeout: 120s
    max_header_bytes: 1048576
    body_timeout: 30s
    # 0 = 不限制。Nginx 在前时容量由 limit_conn 决定；Server 直接暴露公网时
    # 必须设置与进程容量匹配的正数，超限新连接立即关闭而不排队。
    max_connections: 0
  agents:
    # 单个 Agent 身份在本节点可保有的物理 WebSocket 数量。
    max_connections_per_agent: 64
  metrics:
    # 空 = /metrics 匿名可抓取（与历史行为一致）。二选一：反向代理按来源网段
    # 放行，或用 TUNNELMESH_SERVER_METRICS_TOKEN 注入不少于 16 字符的 Bearer。
    # 该字段不会出现在 config dump 输出里。
    token: ""
  audit:
    # 0 = 永久保留。审计日志是证据，删除必须是显式决策；正数才会启动清理器，
    # 每小时按 1000 行一批删除，避免首次清理历史大表时长时间锁表。
    retention_days: 0
  # 内嵌 VPN 网关（WireGuard）。数据面在 -tags vpn 构建里：带 tag 且 enabled: true
  # 时进程真的监听 listen 指定的公网 UDP 端口并承载隧道；不带 tag 时只有管理面
  # （签发、列出、修改、轮换、吊销、审计），config:reveal 返回 409 vpn_node_disabled。
  # 不带 tag 却配 enabled: true 会启动失败并提示 rebuild with -tags vpn，不会静默不工作。
  # 全部键的取值范围见 configuration.md 的「内嵌 VPN 网关」一节，
  # 放行、密钥注入与 IP 池规划见 deployment/vpn-gateway.md。
  vpn:
    enabled: false
    # 独立的公网 UDP 端口，不经反向代理，需在防火墙单独放行并单独限流。
    listen: 0.0.0.0:51820
    # 下发给用户的 Endpoint 主机名，只写主机名不写端口，端口取自 listen。
    endpoint_host: gw-1.mesh.example.com
    # 集群里每个节点必须配置相同的 ip_pool 与 node_subnet_size，
    # 节点各自从池中租约一个 /24 子网（vpn_ip_leases，epoch fencing）。
    ip_pool: 10.64.0.0/16
    node_subnet_size: 24
    # 1500 - WireGuard 的 72 字节开销。
    mtu: 1420
    # 下面三个 0 表示不限。
    max_peers: 0
    max_flows_per_peer: 128
    max_flows_total: 0
    packet_rate_per_peer: 0
    connect_timeout: 10s
    idle_timeout: 120s
    shutdown_timeout: 15s
    # 节点级上限而非承诺：peer 还需单独开启，且出口 Agent 要协商到该能力，
    # 否则签发返回 409 vpn_agent_capability_missing。
    icmp_enabled: true
    icmp_timeout: 5s
    icmp_max_concurrent: 64
    # 节点自身的 WireGuard 私钥不是配置项，只由 TUNNELMESH_VPN_NODE_PRIVATE_KEY 注入。

security:
  # 管理 API 和 WebSocket Host 白名单；动态 HTTP 路由由路由表匹配。
  allowed_hosts:
    - tunnel.example.com
  # Agent/Client 连接 wss://tunnel.example.com 时发送该 Origin。
  # 启用 WebSSH/SFTP 时还必须包含管理后台的精确 Origin。
  allowed_origins:
    - https://tunnel.example.com
  # 管理台身份认证。这里只写“进程级默认值与旋钮”：
  # session_token_ttl / device_trust.enabled / device_trust.bypass_mfa 只在
  # auth_settings 行首次创建时作为种子写入，之后数据库行是权威来源，
  # 改这里不会覆盖管理员在后台做出的决定。OIDC 提供商的 issuer、
  # client id、client secret 和 role mapping 全部存在数据库，不在配置文件中。
  auth:
    # 管理台 token 有效期；0 表示沿用升级前的不过期行为。
    session_token_ttl: 12h
    login_throttle:
      max_attempts: 10
      window: 5m
      block: 10m
    mfa:
      # 会写进 otpauth:// 标签，不能包含冒号。
      issuer: TunnelMesh
      digits: 6
      period: 30s
      skew: 1
      challenge_ttl: 5m
      max_attempts: 5
      recovery_codes: 10
    device_trust:
      enabled: true
      cookie_name: tm_device
      cookie_secure: true
      cookie_same_site: lax
      # 关掉它表示“每次登录都要二次验证”，但仍保留设备清单与撤销能力。
      bypass_mfa: true
    oidc:
      # 关闭后登录页不再列出 SSO 按钮，GET /api/v1/auth/oidc/providers 返回 404。
      public_providers: true
      http_timeout: 10s
      state_ttl: 10m
      login_ticket_ttl: 60s
      jwks_cache_ttl: 1h
      max_discovery_body_bytes: 1048576

# 后台“发行管理”页展示的仓库，格式 owner/name。
# 内网镜像可改为 mirror-owner/TunnelMesh，或用
# TUNNELMESH_DOWNLOADS_GITHUB_REPOSITORY 覆盖（该键没有命令行参数）。
downloads:
  github_repository: nnworld/TunnelMesh

# 公网 TLS 已由 Nginx 终止，这里保持关闭。
# 若 Server 直接暴露 HTTPS，需同时提供 cert_file 和 key_file。
tls:
  enabled: false
  cert_file: ""
  key_file: ""
  min_version: "1.2"
```

配套的 systemd 环境文件示例：

```bash
TUNNELMESH_STORAGE_MYSQL_DSN='db_user:db_password@tcp(mysql.internal.example.com:3306)/tunnelmesh?parseTime=true'
TUNNELMESH_STORAGE_MYSQL_TLS=false
TUNNELMESH_SERVER_RELAY_NODE_TOKEN='replace-with-server-node-service-token'

# 管理台 token reveal、SSH 凭据自动认证，以及 SSO/MFA 必需：
# OIDC client_secret、TOTP 共享密钥和 auth_challenges payload 都用它加密。
# 长度为 16/24/32 字节，base64 或 hex；集群所有 Server 节点必须完全一致。
# 未配置时 Server 仍可启动，但 MFA 绑定和 OIDC 提供商创建返回
# 503 secret_storage_unavailable，不会退化成明文存储。
TUNNELMESH_TOKEN_ENCRYPTION_KEY="${TUNNELMESH_TOKEN_ENCRYPTION_KEY:?inject from your secret manager}"
# 可选；轮换密钥时随之更换。未设置时 key id 固定为 default。
TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID="${TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID:-default}"
# 多 Server 集群所有节点必须一致，用于跨节点 traceroute 签名。
TUNNELMESH_TRACE_SIGNING_KEY="${TUNNELMESH_TRACE_SIGNING_KEY:?inject from your secret manager}"
```

MySQL 支持 TLS 时改为：

```yaml
storage:
  driver: mysql
  auto_init: true
  mysql:
    dsn: ""
    tls: true
```

并在 DSN 中追加 `tls=true`：

```bash
TUNNELMESH_STORAGE_MYSQL_DSN='db_user:db_password@tcp(db.example.com:3306)/tunnelmesh?parseTime=true&tls=true'
```

### 在集群上启用 SSO 与 MFA

配置文件只负责种子。真正打开这两项能力是对数据库的两次管理 API 调用，任何 Server 节点都可以执行，
集群立即一致生效。以下命令中的 secret 一律来自环境变量，示例里只有占位符。

```bash
# 1) 打开全局两步验证策略（mfaMode: disabled / optional / required）
curl -sS -X PUT "https://tunnel.example.com/api/v1/auth/policy" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H 'Idempotency-Key: auth-policy-enable-mfa-001' \
  -H 'Content-Type: application/json' \
  -d '{
        "mfaMode": "required",
        "deviceTrustEnabled": true,
        "deviceTrustTtlSeconds": 2592000,
        "allowTrustedDeviceBypass": true,
        "maxTrustedDevices": 10,
        "sessionTokenTtlSeconds": 43200
      }'

# 2) 注册 OIDC 提供商。redirectUri 必须精确等于
#    https://<对外域名>/api/v1/auth/oidc/<name>/callback，
#    且落在 security.allowed_origins / allowed_hosts 推导出的 base 之内。
curl -sS -X POST "https://tunnel.example.com/api/v1/sso/providers" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H 'Idempotency-Key: sso-provider-okta-001' \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "okta",
        "displayName": "Okta",
        "issuer": "https://example.okta.com",
        "clientId": "'"${OIDC_CLIENT_ID}"'",
        "clientSecret": "'"${OIDC_CLIENT_SECRET}"'",
        "scopes": ["openid", "profile", "email", "groups"],
        "redirectUri": "https://tunnel.example.com/api/v1/auth/oidc/okta/callback",
        "idTokenAlgs": ["RS256"],
        "usernameClaim": "preferred_username",
        "roleMappings": [
          {"claim": "groups", "value": "tunnelmesh-admins", "role": "admin"}
        ],
        "defaultRole": "user",
        "authoritativeRoles": true,
        "autoCreateUsers": true,
        "fetchUserinfo": false,
        "publicListed": true,
        "enabled": true
      }'

# 3) 在不影响用户的前提下验证 issuer discovery 与 JWKS 可达性。
#    PROVIDER_ID 取第 2 步响应 data.id（不透明 ID，不是 name）。
PROVIDER_ID='<上一步返回的 data.id>'
curl -sS -X POST "https://tunnel.example.com/api/v1/sso/providers/${PROVIDER_ID}/test" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H 'Idempotency-Key: sso-provider-okta-test-001'
```

三条命令里的 `ADMIN_TOKEN`、`OIDC_CLIENT_ID`、`OIDC_CLIENT_SECRET` 都必须先在当前 shell 里从
Secret Manager 导出；`-d` 用单引号包裹，所以凡是需要展开的字段都写成 `"'"${VAR}"'"` 形式，
直接写 `"${VAR}"` 会把字面量 `${VAR}` 发出去。

字段取值范围、role mapping 语义和 `data.error` 排障表见
[单点登录与两步验证](../user-guide/sso-and-mfa.md)；键含义见
[配置说明](configuration.md#管理台身份认证sso--mfa--受信任设备)。

## Agent

Agent 主动连接 Server 的 `/ws/agent`，无需开放入站端口。`agent.id` 必须在设备生命周期内稳定，并与后台创建的 Agent 资源和 `agent` 类型 service token 绑定。

```yaml
mode: local

agent:
  server_url: wss://tunnel.example.com/ws/agent
  id: agent-devbox
  # 可省略；首次运行会生成并持久化稳定 instance ID。
  # Linux 打包部署默认保存到 /var/lib/tunnelmesh-agent/agent-instance-id。
  instance_id: agent-devbox-host-a
  # 默认保持单连接。扩容配置见 operations/connection-pool.md。
  connections:
    min: 1
    max: 1
  streams:
    max_concurrent_dials: 32
    # Agent 自己的活跃流水位（纵深防御第二层），0 = 不限。
    max_active: 1024
    max_pending_dials: 128
    connect_timeout: 5s
    open_timeout: 8s
    inbound_buffer_bytes: 262144
    # VPN 网关的 ICMP echo 出口。开启前主机必须先执行一次：
    #   sysctl -w net.ipv4.ping_group_range='0 2147483647'
    # 否则 socket 打不开、能力不通告，Server 会拒签 ICMP peer，
    # 但 Agent 仍会继续服务 TCP/UDP/HTTP 隧道。
    icmp_enabled: false
    icmp_bind_address: 0.0.0.0
    icmp_timeout: 5s
    icmp_max_concurrent: 64
  # token 由 TUNNELMESH_AGENT_TOKEN 注入。
  metadata:
    - name: device_id
      source: file
      path: /etc/machine-id
    - name: region
      source: env
      key: TUNNELMESH_REGION
```

环境文件示例：

```bash
TUNNELMESH_AGENT_TOKEN='replace-with-agent-service-token'
TUNNELMESH_REGION='shanghai'
```

## Client

Client 通过 `/ws/client` 建立会话。`client.tunnels` 可同时配置多个本地入口；执行 `run` 时，本地 listener 先启动，再按 `agent_id` 建立独立 WebSocket 连接池。同一 Agent 的 stream 复用该 Agent 的 WebSocket，断线时只重连 WebSocket，不重建本地 listener。临时转发仍可用 `forward`/`proxy` 命令。

```yaml
mode: local

client:
  server_url: wss://tunnel.example.com/ws/client
  # 可省略；首次 run 生成小写 client-<32hex> 并持久化。
  # Linux 默认 /var/lib/tunnelmesh-client/client-instance-id，
  # macOS 默认 ~/Library/Application Support/TunnelMesh/client-instance-id，
  # Windows 默认 %ProgramData%\TunnelMesh\client-instance-id。
  instance_id: client-0123456789abcdef0123456789abcdef
  instance_id_path: /var/lib/tunnelmesh-client/client-instance-id
  connections:
    min: 1
    max: 4
    high_watermark: 16
    low_watermark: 2
    evaluation_interval: 10s
    cooldown: 30s
  stream:
    open_timeout: 8s
    inbound_buffer_bytes: 262144
  remote_validation:
    positive_ttl: 15s
    negative_ttl: 2s
    timeout: 3s
    max_entries: 10000
  metadata:
    - name: device_id
      source: file
      path: /etc/machine-id
    - name: region
      source: env
      key: TUNNELMESH_CLIENT_REGION
  # token 由 TUNNELMESH_CLIENT_TOKEN 注入。
  tunnels:
    - name: postgres
      protocol: tcp
      listen: 127.0.0.1:15432
      agent_id: agent-db
      target_host: db.internal
      target_port: 5432
    - name: dns
      protocol: udp
      listen: 127.0.0.1:15353
      agent_id: agent-net
      target_host: 10.0.0.53
      target_port: 53
    - name: internal-web
      protocol: http
      listen: 127.0.0.1:18080
      agent_id: agent-web
      target_host: 127.0.0.1
      target_port: 8080
    - name: socks-a
      protocol: socks5
      listen: 127.0.0.1:10866
      agent_id: agent-a
    - name: socks-b
      protocol: socks5
      listen: 127.0.0.1:10867
      agent_id: agent-b
```

环境文件示例：

```bash
TUNNELMESH_CLIENT_TOKEN='replace-with-client-service-token'
TUNNELMESH_CLIENT_REGION='cn-north'
```

`client.metadata` 最多 32 项，单项 4 KiB，总 payload 32 KiB。名称只能使用字母、数字、`.`、`_`、`-`，且不能包含 password、passphrase、token、secret、private key、api key、credential、authorization、cookie 或 DSN 语义。字段值会在启动时读取一次并随 metadata 帧上报；读取失败只记录为该字段的 metadata 错误，不会泄露文件路径或值。

### SOCKS5 本地入口

SOCKS5 可写入 `client.tunnels`，随 `run` 命令启动。默认监听 loopback 且不启用本地认证：

```yaml
client:
  tunnels:
    - name: socks-a
      protocol: socks5
      listen: 127.0.0.1:10866
      agent_id: agent-a
    - name: socks-b
      protocol: socks5
      listen: 127.0.0.1:10867
      agent_id: agent-b
```

```bash
tunnelmesh-client --config /etc/tunnelmesh/client.yaml run
```

需要临时启动单个入口时，也可使用命令行：

```bash
tunnelmesh-client forward socks5 \
  --listen 127.0.0.1:1080 \
  --agent agent-devbox
```

如需监听非 loopback 地址，配置文件必须同时设置 `allow_remote: true` 和 `auth_mode: password`，并从环境变量注入本地入口凭据。命令行模式对应 `--allow-remote` 和 `--auth password`：

```yaml
client:
  tunnels:
    - name: remote-socks
      protocol: socks5
      listen: 0.0.0.0:1080
      agent_id: agent-devbox
      allow_remote: true
      auth_mode: password
      auth_url: http://auth.internal/validate
```

```bash
TUNNELMESH_SOCKS5_USERNAME='alice' \
TUNNELMESH_SOCKS5_PASSWORD='local-ingress-secret' \
tunnelmesh-client forward socks5 \
  --listen 0.0.0.0:1080 \
  --agent agent-devbox \
  --allow-remote \
  --auth password
```

这两个环境变量只用于本地 SOCKS5 入口认证，不用于 Server 或 Agent 的 service token。

如需启用远程校验，配置文件可设置 `auth_url`；临时命令可额外指定 `--auth-url`：

```bash
tunnelmesh-client forward socks5 \
  --listen 127.0.0.1:1080 \
  --agent agent-devbox \
  --auth-url http://auth.internal/validate
```

### 标准 HTTP 代理

标准 HTTP 代理当前作为临时 `forward http-proxy` 命令提供，不写入 `client.tunnels` 配置。默认监听 loopback 且不启用本地认证：

```bash
tunnelmesh-client forward http-proxy \
  --listen 127.0.0.1:8080 \
  --agent agent-devbox
```

如需监听非 loopback 地址，必须同时开启 `--allow-remote` 和 `--auth basic`，并从环境变量注入本地代理凭据：

```bash
TUNNELMESH_HTTP_PROXY_USERNAME='alice' \
TUNNELMESH_HTTP_PROXY_PASSWORD='local-proxy-secret' \
tunnelmesh-client forward http-proxy \
  --listen 0.0.0.0:8080 \
  --agent agent-devbox \
  --allow-remote \
  --auth basic
```

这两个环境变量只用于本地 HTTP 代理认证，不用于 Server 或 Agent 的 service token。

如需启用远程校验，可额外指定 `--auth-url`：

```bash
tunnelmesh-client forward http-proxy \
  --listen 127.0.0.1:8080 \
  --agent agent-devbox \
  --auth-url http://auth.internal/validate
```

### Client 全协议配置示例

连接池参数是全局配置，作用于每个逻辑 Agent：多个 Agent 至少各有一条 WebSocket，同一 Agent 可按负载扩容到 `max`。`high_watermark` 表示单条 WebSocket 的活跃 stream 数，达到后扩容；`low_watermark` 表示缩容阈值。完整示例见 [Client 配置示例](client-configuration-examples.md)。

## 检查与文件权限

```bash
tunnelmesh-server --config /etc/tunnelmesh/server.yaml check-config
tunnelmesh-agent --config /etc/tunnelmesh/agent.yaml check-config
tunnelmesh-client --config /etc/tunnelmesh/client.yaml login
```

配置和环境文件只应允许 root 与对应服务组读取：

```bash
chmod 640 /etc/tunnelmesh/*.yaml /etc/tunnelmesh/*.env
```

`check-config` 只验证配置结构，不测试数据库、证书或 WebSocket 的实际连通性。Server 的依赖状态应通过 `/health/ready` 检查。
