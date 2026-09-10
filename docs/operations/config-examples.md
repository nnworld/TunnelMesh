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

security:
  allowed_hosts:
    - tunnel.example.com
  allowed_origins:
    - https://tunnel.example.com

tls:
  enabled: false
  min_version: "1.2"
```

## Server：集群 MySQL

`node.id` 可省略。`run` 首次启动时会生成稳定 ID 并保存到 `/var/lib/tunnelmesh/node-id`。需要将 ID 明确写入 YAML 时，以有权限修改配置文件的用户执行：

```bash
tunnelmesh-server --config /etc/tunnelmesh/server.yaml init-node-id
```

以下示例使用不支持 TLS 的 MySQL 端口；内网明文连接必须由网络 ACL 隔离，生产环境优先启用 TLS。

```yaml
mode: cluster

storage:
  driver: mysql
  auto_init: true
  mysql:
    # DSN 由 TUNNELMESH_STORAGE_MYSQL_DSN 注入。
    dsn: ""
    tls: false

registry:
  type: database

# node:
#   id: server-1

server:
  http_addr: 127.0.0.1:8080
  # 必须与 DNS、证书和 Nginx server_name 的动态域名后缀一致。
  dynamic_suffix: apps.example.com
  tcp_bridge:
    enabled: true
    path: /ws/tcp
    max_bytes: 65536
  relay:
    enabled: false

security:
  allowed_hosts:
    - tunnel.example.com
  allowed_origins:
    - https://tunnel.example.com

tls:
  enabled: false
  min_version: "1.2"
```

配套的 systemd 环境文件示例：

```bash
TUNNELMESH_STORAGE_MYSQL_DSN='db_user:db_password@tcp(10.0.0.10:3306)/tunnelmesh?parseTime=true'
TUNNELMESH_STORAGE_MYSQL_TLS=false
```

MySQL 支持 TLS 时改为：

```yaml
storage:
  driver: mysql
  auto_init: true
  mysql:
    dsn: ""
    tls: true
    ca: /etc/tunnelmesh/certs/mysql-ca.pem
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
