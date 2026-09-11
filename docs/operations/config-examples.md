# 三端配置文件示例

TunnelMesh 三端共用同一配置模型，但每个进程只使用与自身职责相关的配置段。配置优先级为：命令行参数 > 环境变量 > 配置文件 > 默认值。YAML 不展开 `${VAR}`，密码、Token 和密钥应通过环境变量或 Secret Manager 注入。

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
  stream:
    max_concurrent_opens: 256
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

security:
  allowed_hosts:
    - tunnel.example.com
  allowed_origins:
    - https://tunnel.example.com

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
  # 必须与 DNS、证书和 Nginx server_name 的动态域名后缀一致，不要写 *。
  dynamic_suffix: apps.example.com
  tcp_bridge:
    enabled: true
    path: /ws/tcp
    max_bytes: 65536

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

security:
  # 管理 API 和 WebSocket Host 白名单；动态 HTTP 路由由路由表匹配。
  allowed_hosts:
    - tunnel.example.com
  # Agent/Client 连接 wss://tunnel.example.com 时发送该 Origin。
  allowed_origins:
    - https://tunnel.example.com

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

# 管理台 token reveal 使用；长度为 16/24/32 字节，base64 或 hex。
TUNNELMESH_TOKEN_ENCRYPTION_KEY='replace-with-aes-256-gcm-key'
# 多 Server 集群所有节点必须一致，用于跨节点 traceroute 签名。
TUNNELMESH_TRACE_SIGNING_KEY='replace-with-high-entropy-signing-key'
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
    max_pending_dials: 128
    connect_timeout: 5s
    open_timeout: 8s
    inbound_buffer_bytes: 262144
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

Client 通过 `/ws/client` 建立会话。隧道条目可写入配置文件，也可以用 `forward`/`proxy` 命令临时指定。

```yaml
mode: local

client:
  server_url: wss://tunnel.example.com/ws/client
  stream:
    open_timeout: 8s
    inbound_buffer_bytes: 262144
  remote_validation:
    positive_ttl: 15s
    negative_ttl: 2s
    timeout: 3s
    max_entries: 10000
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
```

环境文件示例：

```bash
TUNNELMESH_CLIENT_TOKEN='replace-with-client-service-token'
```

### SOCKS5 本地入口

SOCKS5 当前作为临时 `forward socks5` 命令提供，不写入 `client.tunnels` 配置。默认监听 loopback 且不启用本地认证：

```bash
tunnelmesh-client forward socks5 \
  --listen 127.0.0.1:1080 \
  --agent agent-devbox
```

如需监听非 loopback 地址，必须同时开启 `--allow-remote` 和 `--auth password`，并从环境变量注入本地入口凭据：

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

如需启用远程校验，可额外指定 `--auth-url`：

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
