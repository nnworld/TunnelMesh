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

集群模式允许 MySQL 明文连接（`storage.mysql.tls: false`），但生产环境建议启用 TLS，并在 DSN 中设置 `tls=true`。关闭 TLS 时，DSN 里也不能残留 `tls=true`；程序会根据 `storage.mysql.tls` 覆盖 DSN 中的 `tls` 参数。若未配置 `node.id`，Server 在 `run` 时会生成并持久化到 `/var/lib/tunnelmesh/node-id`；也可以用以下命令将其写回 YAML（需要配置文件可写）：

```bash
tunnelmesh-server --config /etc/tunnelmesh/server.yaml init-node-id
```

切换 etcd：

```yaml
registry:
  type: etcd
  endpoints:
    - https://etcd-1:2379
    - https://etcd-2:2379
```

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

`server_node` token 必须与集群节点 ID 绑定，并和 relay mTLS 证书一起配置。启用 relay 时必须配置 `server.relay.endpoint`，且该地址要能被其他 Server 节点访问；连接租约会保存它用于跨节点查询和关闭。生产环境不把 token 放进提交的配置文件；`print-config` 不会输出明文。

### Recoverable service-token secrets

Set `TUNNELMESH_TOKEN_ENCRYPTION_KEY` to a base64 or hexadecimal AES key (16, 24, or 32 bytes) and optionally set `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID`. The key must come from an environment-injected secret or secret manager and must not be committed to a file or database. New and rotated service tokens are encrypted with AES-GCM. Existing hash-only tokens cannot be revealed and must be rotated. Secret reveal is administrator-only, audited, requires `X-Token-Reveal-Confirm` plus `Idempotency-Key`, and returns `Cache-Control: no-store`.

For multi-server deployments set the same high-entropy `TUNNELMESH_TRACE_SIGNING_KEY` on every server/relay node so hop signatures can be verified across the cluster. It is never returned in traceroute output.
