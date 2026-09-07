# tunnelmesh-client 使用帮助

## 1. 准备配置

`tunnelmesh-client` 支持配置文件、环境变量和命令行参数，优先级为：命令行参数 > 环境变量 > 配置文件 > 默认值。

最小配置示例：

```yaml
mode: local
client:
  server_url: wss://tunnel.example.com/client
```

使用配置文件执行登录命令时会自动加载并校验配置：

```bash
tunnelmesh-client --config tunnelmesh.yaml login
```

常用环境变量：

```bash
export TUNNELMESH_MODE=local
export TUNNELMESH_CLIENT_SERVER_URL=wss://tunnel.example.com/client
```

## 2. 登录和查看 Agent

```bash
tunnelmesh-client --config tunnelmesh.yaml login
tunnelmesh-client --config tunnelmesh.yaml agent
tunnelmesh-client --config tunnelmesh.yaml status
```

## 3. 支持能力总览

| 命令 | 协议 | 方向 | 说明 |
| --- | --- | --- | --- |
| `forward tcp` | TCP | 本地监听 → Agent 内网服务 | 有序字节流 |
| `forward udp` | UDP | 本地监听 → Agent 内网服务 | 保留 datagram 边界，按源地址复用 association |
| `forward http` | HTTP | 本地监听 → Agent 内网 HTTP 服务 | 适合本地 Web 服务 |
| `publish http` | HTTP/HTTPS/WebSocket | Server 公网入口 → Agent 服务 | 通过管理路由或 wildcard 域名访问 |
| `proxy tcp` | TCP | stdin/stdout ↔ Agent 内网服务 | 用于 SSH `ProxyCommand` 和 websocat |
| `tunnel status/stop` | - | 本地隧道管理 | 查看或停止配置的隧道 |

公网 Server 入口不开放公网 UDP；UDP 仅支持通过 `forward udp` 从用户侧发起到 Agent 内网。

## 4. TCP 转发

把本地 `127.0.0.1:15432` 转发到 Agent 所在内网的 `db.internal:5432`：

```bash
tunnelmesh-client forward tcp \
  --listen 127.0.0.1:15432 \
  --agent <agent-id> \
  --target-host db.internal \
  --target-port 5432
```

## 5. UDP 转发

UDP 使用 datagram 边界，每个 UDP 报文保持独立；同一个本地源地址会复用 association：

```bash
tunnelmesh-client forward udp \
  --listen 127.0.0.1:15353 \
  --agent <agent-id> \
  --target-host 10.0.0.53 \
  --target-port 53
```

公网入口不直接监听 UDP；需要公网 UDP 时，应在目标网络内放置 UDP 网关，再通过 TCP/HTTP 或其他受支持入口接入。

## 6. TCP 和 UDP 同时转发

TCP 和 UDP 使用不同的操作系统传输层命名空间，因此可以同时使用相同的数字端口。例如下面两个命令可以并行运行：

```bash
# TCP/5432：数据库连接
tunnelmesh-client forward tcp \
  --listen 127.0.0.1:5432 \
  --agent agent-db \
  --target-host db.internal \
  --target-port 5432

# UDP/5432：另一个 UDP 服务
tunnelmesh-client forward udp \
  --listen 127.0.0.1:5432 \
  --agent agent-net \
  --target-host dns.internal \
  --target-port 5432
```

注意：

- `TCP/5432` 和 `UDP/5432` 可以并存；两个 TCP 或两个 UDP 监听不能占用同一地址端口。
- 若使用同一个端口，必须确认两个目标服务的协议不同；客户端不会把 TCP 数据转成 UDP，也不会把 UDP 数据转成 TCP。
- IPv4/IPv6 双栈、容器网络和系统安全策略可能改变端口绑定行为，出现 bind 错误时请明确指定监听地址。
- 配置文件中也可以定义两条隧道，只要 `protocol` 分别是 `tcp` 和 `udp`。

## 7. HTTP 转发和发布

本地 HTTP 服务可以通过 `forward http` 或管理后台创建托管路由：

```bash
tunnelmesh-client forward http \
  --listen 127.0.0.1:18080 \
  --agent <agent-id> \
  --target-host 127.0.0.1 \
  --target-port 8080
```

托管域名、路径和目标端口等长期配置建议通过 Web 管理后台或 `/api/v1/routes` 管理，避免把生产路由写入临时命令行历史。

`publish` 当前用于 HTTP/HTTPS/WebSocket 托管路由；不能把 `tcp` 或 `udp` 作为公网 Server 监听协议。需要 TCP 访问时使用 `proxy tcp` 或 Server 的 TCP-over-WebSocket bridge。

## 8. 原始 TCP 代理

`proxy tcp` 从 stdin 读取字节并把响应写回 stdout，适用于 SSH：

```bash
tunnelmesh-client proxy tcp \
  --agent agent-devbox \
  --target-host 127.0.0.1 \
  --target-port 22
```

更常见的公网 SSH 场景参见 [SSH over WebSocket](tcp-over-websocket-ssh.md)。

## 9. SSH 公钥和远程命令

TunnelMesh 不保存 SSH 私钥，也不替代目标主机的 `sshd`。先在目标主机安装公钥，再用本地私钥或 `ssh-agent` 完成认证：

```bash
ssh-copy-id -i ~/.ssh/id_ed25519.pub devuser@target-host
ssh-add ~/.ssh/id_ed25519
```

通过现有 TCP proxy 建立 SSH：

```bash
ssh \
  -o 'ProxyCommand=tunnelmesh-client --config tunnelmesh.yaml proxy tcp --agent agent-devbox --target-host 127.0.0.1 --target-port 22' \
  devuser@agent-devbox
```

远程命令仍由 SSH 负责解析、授权和返回退出码；TunnelMesh 只转发字节。例如：

```bash
ssh \
  -o 'ProxyCommand=tunnelmesh-client --config tunnelmesh.yaml proxy tcp --agent agent-devbox --target-host 127.0.0.1 --target-port 22' \
  devuser@agent-devbox 'uname -a && systemctl is-active sshd'
```

Server policy 应只允许目标 Agent、目标地址和 TCP/22。SSH 连接失败时先检查 `authorized_keys`、`ssh-agent`、目标主机用户和 policy；TunnelMesh 不会把密码或私钥写入日志。

`command-exec`（由 Server 直接执行任意命令）不属于当前协议，也没有隐藏入口。若未来需要该能力，必须另行设计命令白名单、RBAC、审批、PTY、超时、输出上限和审计。

## 10. 停止和查看状态

```bash
tunnelmesh-client tunnel status
tunnelmesh-client tunnel stop
tunnelmesh-client stop
```

## 11. 配置文件中的多个隧道

```yaml
mode: local
client:
  server_url: wss://tunnel.example.com/client
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
```

## 安全建议

- 生产环境必须使用 `wss://`，并校验证书链。
- Agent ID、Token 和配置文件权限应限制为服务用户可读。
- 不要把 Token、密码和 MySQL DSN 提交到 Git。
- 调整目标网段、端口和 CIDR 策略时，同时检查服务端 policy。

生产环境使用后台创建的 `client` service token，并通过配置、环境变量或 Secret 注入：

```yaml
client:
  server_url: wss://tunnel.example.com/ws/client
  token: ${TUNNELMESH_CLIENT_TOKEN}
```

Token scope 只能缩小 Agent Policy。每个 TCP、UDP、HTTP 或 proxy stream 都会重新校验 Agent、协议、目标 CIDR 和端口；轮换/撤销后重新连接即可使用新凭据。
