# Client 配置示例

`tunnelmesh-client run` 支持在同一个进程内启动多个本地入口。本地 listener 先启动，随后按 `agent_id` 建立逻辑 Agent 连接池：

- 不同 Agent 使用不同 WebSocket 连接池；
- 同一 Agent 的多条 stream 复用池内 WebSocket；
- 新 stream 优先选择活跃数最少的 WebSocket；活跃数相同时按最近心跳 RTT 选择；
- 所有 WebSocket 达到 `high_watermark` 时扩容，最多扩到 `max`；
- 所有 WebSocket 活跃数降到 `low_watermark` 以下时缩容回 `min`；
- WebSocket 重连不会关闭或重建本地 listener。

以下示例覆盖 TCP、UDP、HTTP、SOCKS5、远程 SOCKS5、标准 HTTP 代理、认证、远程校验和多 Agent 连接池：

```yaml
mode: local

client:
  server_url: wss://tunnel.example.com/ws/client
  # 可省略。run 首次启动会生成小写 client-<32hex> 并持久化。
  instance_id: client-0123456789abcdef0123456789abcdef
  # 可自定义状态文件。默认：
  # Linux: /var/lib/tunnelmesh-client/client-instance-id
  # macOS: ~/Library/Application Support/TunnelMesh/client-instance-id
  # Windows: %ProgramData%\TunnelMesh\client-instance-id
  instance_id_path: /var/lib/tunnelmesh-client/client-instance-id
  connections:
    min: 1
    max: 4
    high_watermark: 16
    low_watermark: 2
    evaluation_interval: 10s
    cooldown: 30s
  remote_validation:
    positive_ttl: 15s
    negative_ttl: 2s
    timeout: 3s
    max_entries: 10000
  # 显式 allowlist 后才会上报自定义观测字段；file 必须使用绝对路径。
  metadata:
    - name: device_id
      source: file
      path: /etc/machine-id
    - name: region
      source: env
      key: TUNNELMESH_CLIENT_REGION
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
      target_host: web.internal
      target_port: 8080
    - name: socks-loopback
      protocol: socks5
      listen: 127.0.0.1:10866
      agent_id: agent-devbox
      auth_mode: none
    - name: socks-remote
      protocol: socks5
      listen: 0.0.0.0:10867
      agent_id: agent-devbox
      allow_remote: true
      auth_mode: password
      auth_url: http://auth.internal/validate
    - name: http-proxy
      protocol: http-proxy
      listen: 127.0.0.1:18081
      agent_id: agent-devbox
      auth_mode: none
```

Client service token 通过环境变量注入，不写入 YAML：

```bash
TUNNELMESH_CLIENT_TOKEN='replace-with-client-service-token'
TUNNELMESH_CLIENT_REGION='cn-north'
```

远程 SOCKS5 与远程 HTTP 代理必须显式开启远程监听，并使用本地入口认证。凭据同样通过环境变量注入：

```bash
TUNNELMESH_SOCKS5_USERNAME='replace-with-socks5-username'
TUNNELMESH_SOCKS5_PASSWORD='replace-with-socks5-password'
TUNNELMESH_HTTP_PROXY_USERNAME='replace-with-http-proxy-username'
TUNNELMESH_HTTP_PROXY_PASSWORD='replace-with-http-proxy-password'
```

远程 HTTP 代理示例：

```yaml
client:
  tunnels:
    - name: http-proxy-remote
      protocol: http-proxy
      listen: 0.0.0.0:18082
      agent_id: agent-devbox
      allow_remote: true
      auth_mode: basic
      auth_url: http://auth.internal/validate
```

`tcp`、`udp`、`http` 三种隧道也受非回环监听门禁约束：`listen` 不是回环地址时必须写 `allow_remote: true`，否则配置校验和启动都会直接拒绝，而不是悄悄绑上一个公网可读的入口。

```yaml
client:
  tunnels:
    - name: lan-postgres
      protocol: tcp
      listen: 0.0.0.0:15433
      agent_id: agent-db
      target_host: db.internal
      target_port: 5432
      # 这三种隧道没有 socks5/http-proxy 那样的本地认证（密码或 Basic），
      # 因此 allow_remote 等价于把目标服务无凭据发布到该网段：
      # 只应在本机与网段本身已经是可信边界时使用。
      allow_remote: true
```

`auth_url` 会收到包含 `protocol`、`agentId`、`targetHost`、`targetPort` 和本地认证凭据的 POST 请求；返回 `2xx` 表示允许，其它状态、超时或网络错误都表示拒绝。该 URL 可以是 HTTP，但必须部署在可信网络中。

如果只想为不同 Agent 提供固定入口，保留 `connections.min: 1`、`max: 1` 即可。例如：

```yaml
client:
  connections:
    min: 1
    max: 1
  tunnels:
    - name: devbox-socks
      protocol: socks5
      listen: 127.0.0.1:10866
      agent_id: agent-devbox
    - name: db-postgres
      protocol: tcp
      listen: 127.0.0.1:15432
      agent_id: agent-db
      target_host: db.internal
      target_port: 5432
```

执行检查和启动：

```bash
tunnelmesh-client --config /etc/tunnelmesh/client.yaml check-config
tunnelmesh-client --config /etc/tunnelmesh/client.yaml run
```
