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

### 访问依赖域名 Host 的 HTTP 服务

`target_host` 只决定 Agent 从内网拨号的地址，不会自动改写 HTTP 请求的 `Host`。如果上游是明文 HTTP 服务，但根据域名做虚拟主机路由，可以在本地请求中显式携带原始域名 Host：

```bash
tunnelmesh-client forward http \
  --listen 127.0.0.1:18080 \
  --agent agent-web \
  --target-host 10.0.0.10 \
  --target-port 80
```

访问本地映射端口：

```bash
curl -v \
  -H 'Host: service.internal.example.com' \
  http://127.0.0.1:18080/
```

链路如下：

```text
curl Host: service.internal.example.com
client 本地监听: 127.0.0.1:18080
Agent 拨号: 10.0.0.10:80
上游收到 Host: service.internal.example.com
```

也可以使用 `curl --resolve` 保留 URL 中的域名：

```bash
curl -v \
  --resolve service.internal.example.com:18080:127.0.0.1 \
  http://service.internal.example.com:18080/
```

注意这种方式发送的 Host 可能包含端口，例如 `service.internal.example.com:18080`。部分虚拟主机服务会拒绝带端口的 Host；这种情况下优先使用 `-H 'Host: service.internal.example.com'`。

### 访问 HTTPS-only 服务

如果目标服务只在 443 等端口提供 HTTPS，使用 `forward tcp` 做透明 TCP 映射，让 curl 或浏览器直接与目标服务完成 TLS 握手：

```bash
tunnelmesh-client forward tcp \
  --listen 127.0.0.1:18443 \
  --agent agent-web \
  --target-host 10.0.0.10 \
  --target-port 443
```

访问时保留原始域名，并将该域名解析到本地映射端口：

```bash
curl -v \
  --resolve service.internal.example.com:18443:127.0.0.1 \
  https://service.internal.example.com:18443/
```

链路如下：

```text
curl TLS SNI: service.internal.example.com
curl HTTP Host: service.internal.example.com
client TCP 监听: 127.0.0.1:18443
Agent 拨号: 10.0.0.10:443
上游 TLS SNI: service.internal.example.com
```

浏览器场景可以将 `service.internal.example.com` 写入本机 hosts 并指向 `127.0.0.1`，再访问 `https://service.internal.example.com:18443/`。TLS 握手和证书校验都由浏览器完成，Agent 只转发 TCP 字节。

当前 `forward http` 不支持配置 `targetScheme=https`、`hostHeader` 或 `tlsServerName`，因此不要把 `forward http` 直接指向 HTTPS-only 服务的 443 端口；该场景应使用 `forward tcp`。如需 Server 侧固定这些上游参数，请使用显式托管路由，参见 [托管 HTTP 路由](managed-http-route.md)。

## 8. 标准 HTTP 代理

`forward http-proxy` 在本机启动一个标准 HTTP 代理入口，浏览器或命令行工具可以按请求动态选择 Agent 侧目标：

```bash
tunnelmesh-client forward http-proxy \
  --listen 127.0.0.1:8080 \
  --agent agent-devbox
```

使用 curl：

```bash
curl -x http://127.0.0.1:8080 \
  http://service.internal.example.com/
```

HTTPS 会自动通过 `CONNECT` 建立隧道：

```bash
curl -x http://127.0.0.1:8080 \
  https://service.internal.example.com/
```

支持的代理协议：

- 明文 HTTP：absolute-form 请求
- HTTPS：通过 `CONNECT` 隧道
- WebSocket：HTTP/HTTPS 上的 `Upgrade`
- 任意 TCP：通过 `CONNECT`
- 仅 HTTP/1.1

默认行为：

- 默认只监听 `127.0.0.1`
- 认证默认为 `none`
- 不做本机 DNS 解析，目标域名交给 Agent 侧处理
- 复用现有 Token、Agent、CIDR 和端口策略

如需启用标准 HTTP 代理 Basic 认证：

```bash
TUNNELMESH_HTTP_PROXY_USERNAME='alice' \
TUNNELMESH_HTTP_PROXY_PASSWORD='local-proxy-secret' \
tunnelmesh-client forward http-proxy \
  --listen 127.0.0.1:8080 \
  --agent agent-devbox \
  --auth basic
```

非回环监听必须同时显式开启远程监听和 Basic 认证：

```bash
TUNNELMESH_HTTP_PROXY_USERNAME='alice' \
TUNNELMESH_HTTP_PROXY_PASSWORD='local-proxy-secret' \
tunnelmesh-client forward http-proxy \
  --listen 0.0.0.0:8080 \
  --agent agent-devbox \
  --allow-remote \
  --auth basic
```

说明：

- Basic 认证只保护本地入口，不替代 Client service token，也不会绕过 Agent 侧目标策略。
- 凭据只从环境变量读取，不要写入配置文件或命令行参数。
- Basic 是明文编码，只适合本机或可信内网；公网暴露还应加 TLS 或网络 ACL。
- 不支持 HTTP/2 proxy mode、UDP、FTP、SMTP、DNS、ICMP、透明代理或代理链。

### 远程校验

`forward http-proxy` 支持在每次代理请求打开 Agent stream 之前，先调用一个远程校验服务：

```bash
tunnelmesh-client forward http-proxy \
  --listen 127.0.0.1:8080 \
  --agent agent-devbox \
  --auth-url http://auth.internal/validate
```

远程校验服务可以是 HTTP 或 HTTPS，TunnelMesh 不强制使用 HTTPS。请求方法为 `POST`，`Content-Type` 为 `application/json`，上下文字段包含：

- `protocol`：固定为 `http-proxy`
- `agentId`
- `targetHost`
- `targetPort`
- `username`、`password`：仅在本地认证模式提供这些值时携带

远程服务返回：

- `2xx`：允许
- 非 `2xx`：拒绝
- 超时或网络错误：拒绝

远程校验失败时，本地 HTTP 代理返回 `403 Forbidden`。请求体、响应体、用户名、密码和目标地址不会写入日志。

## 9. SOCKS5 转发

`forward socks5` 在本机启动一个 SOCKS5 CONNECT 入口，浏览器或支持 SOCKS5 的工具可以按请求动态选择 Agent 侧目标：

```bash
tunnelmesh-client forward socks5 \
  --listen 127.0.0.1:1080 \
  --agent agent-devbox
```

使用 curl 时推荐 `--socks5-hostname`，让域名目标直接透传到 Agent 侧解析和授权：

```bash
curl --socks5-hostname 127.0.0.1:1080 \
  http://service.internal.example.com/
```

浏览器可将 SOCKS5 代理设置为 `127.0.0.1:1080`，并启用“通过 SOCKS 代理解析 DNS”或同等选项，避免在本机提前解析内网域名。

默认行为：

- 仅支持 SOCKS5 `CONNECT`。
- 支持 IPv4、IPv6 和域名目标。
- 域名不在 Client 本机解析，而是透传给 Agent，由 Agent 侧完成解析和目标策略校验。
- 默认只监听 `127.0.0.1`，不暴露到其他网卡。
- 认证默认为 `none`，仅适合本机使用。

如需让局域网内其他机器访问该 SOCKS5 入口，必须同时显式开启远程监听和密码认证：

```bash
TUNNELMESH_SOCKS5_USERNAME='alice' \
TUNNELMESH_SOCKS5_PASSWORD='local-ingress-secret' \
tunnelmesh-client forward socks5 \
  --listen 0.0.0.0:1080 \
  --agent agent-devbox \
  --allow-remote \
  --auth password
```

说明：

- SOCKS5 用户名密码只保护本地入口，不替代 Client service token，也不会绕过 Agent 侧目标策略。
- 凭据只从环境变量读取，不要写入配置文件或命令行参数。
- RFC 1929 用户名和密码各自最多 255 字节。
- `BIND`、`UDP ASSOCIATE` 和 GSSAPI 不支持。
- Server 公网入口不新增 SOCKS5 监听，公网仍只提供 HTTP/HTTPS/WebSocket。

### 远程校验

`forward socks5` 也支持同样的远程校验机制：

```bash
tunnelmesh-client forward socks5 \
  --listen 127.0.0.1:1080 \
  --agent agent-devbox \
  --auth-url http://auth.internal/validate
```

上下文字段包含：

- `protocol`：固定为 `socks5`
- `agentId`
- `targetHost`
- `targetPort`
- `username`、`password`：仅在密码认证模式提供这些值时携带

远程服务返回：

- `2xx`：允许
- 非 `2xx`：拒绝
- 超时或网络错误：拒绝

远程校验失败时，SOCKS5 返回 `0x02`（connection not allowed）。请求体、响应体、用户名、密码和目标地址不会写入日志。

## 10. 原始 TCP 代理

`proxy tcp` 从 stdin 读取字节并把响应写回 stdout，适用于 SSH：

```bash
tunnelmesh-client proxy tcp \
  --agent agent-devbox \
  --target-host 127.0.0.1 \
  --target-port 22
```

更常见的公网 SSH 场景参见 [SSH over WebSocket](tcp-over-websocket-ssh.md)。

## 11. SSH 公钥和远程命令

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

## 12. 停止和查看状态

```bash
tunnelmesh-client tunnel status
tunnelmesh-client tunnel stop
tunnelmesh-client stop
```

## 13. 配置文件中的多个隧道

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
```

配置文件不会展开 `${...}` 占位符；请通过环境变量或命令行注入 token：

```bash
export TUNNELMESH_CLIENT_TOKEN='one-time-client-secret'
```

Token scope 只能缩小 Agent Policy。每个 TCP、UDP、HTTP 或 proxy stream 都会重新校验 Agent、协议、目标 CIDR 和端口；轮换/撤销后重新连接即可使用新凭据。
## 逻辑 traceroute

管理 API 可从已认证的 client 路径发起到指定 Agent 的逻辑 traceroute：

```bash
curl -sS -H "Authorization: Bearer $TM_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"maxHops":16,"timeoutMs":3000}' \
  https://mesh.example.com/api/v1/agents/agent-a/trace
```

结果中的 hop 是 TunnelMesh 的 client、server/relay、服务端节点和 agent，不是 ICMP 路由器。普通用户只看到脱敏拓扑；管理员如需私网地址、真实 peer 地址或证书元数据，才设置 `includeSensitive=true`。Bearer secret 永远不通过 traceroute 返回。
