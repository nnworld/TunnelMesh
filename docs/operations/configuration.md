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

## 监听地址与 TLS

Server 只监听一个地址：`server.http_addr`。管理 API、Web 后台、Agent WebSocket（`/ws/agent`）、Client WebSocket（`/ws/client`）、WebSSH（`/ws/webssh/<session-id>`）、TCP bridge（`/ws/tcp`）和动态 HTTP 路由都复用该监听器。是否加密由 `tls` 段决定，作用在同一个监听器上：

```yaml
server:
  # 反向代理终止 HTTPS 时使用非特权端口，例如 127.0.0.1:8080。
  http_addr: 127.0.0.1:8080

tls:
  # Server 直接对公网提供 HTTPS 时设为 true，并同时给出证书和私钥。
  enabled: false
  cert_file: ""
  key_file: ""
  # 只接受 1.2 或 1.3。
  min_version: "1.2"
```

`tls.enabled: true` 但缺少 `cert_file`/`key_file`，或 `min_version` 不是 `1.2`/`1.3`，`run` 会直接失败而不是降级为明文。生产部署推荐由 Nginx/Caddy 终止公网 TLS，Server 保持 `tls.enabled: false` 并只监听 loopback。

以下键仍被配置加载器接受，但当前版本不会建立额外监听器，属于历史遗留，不要依赖它们：

- `server.https_addr`
- `server.agent_ws_addr`
- `server.client_ws_addr`

`server.tcp_bridge_enabled` 是 `server.tcp_bridge.enabled` 的扁平兼容写法，只在配置文件中生效；命令行、环境变量或 `Set` 覆盖中任一处出现规范键或扁平键时，扁平别名不再二次应用。环境变量两种写法都映射到 `TUNNELMESH_SERVER_TCP_BRIDGE_ENABLED`。

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
  # 单监听器；对公网直接提供 HTTPS 时改为 :443 并开启 tls.enabled。
  http_addr: 127.0.0.1:8080
  # 动态托管域名格式：<agent-id>-<a>-<b>-<c>-<d>-<port>.<dynamic_suffix>
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

# 后台“发行管理”页展示的 GitHub 发行仓库，格式固定为 owner/name。
downloads:
  github_repository: nnworld/TunnelMesh

security:
  # 管理 API 与 WebSocket 的 Host 白名单；留空表示不限制。
  allowed_hosts: []
  # 浏览器 Origin 白名单；开启 WebSSH/SFTP 时必须包含后台精确 Origin。
  allowed_origins: []
  # Deprecated: 仅用于迁移期兼容旧管理 token 连接 Agent，计划在 v0.3.0 移除。
  allow_legacy_connection_tokens: false
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

## tp-* HTTP 代理入口

托管路由的 HTTP 代理入口让浏览器/系统直接把 `https://tp-<name>.<domain_suffix>` 当作标准 HTTPS 代理使用，出口 agent、Basic 认证与来源 ACL 全部由管理后台的 `http-proxy` 路由决定，用户机器上不需要安装 `tunnelmesh-client`。

公网侧仍然只有 443：OpenResty 上的 tp-* server 块用打过 `ngx_http_proxy_connect_module` 补丁的内核接管 CONNECT，只搬字节并注入可信头，然后把请求送到下面这个**进程内明文监听**。所有策略判断（路由身份、来源 ACL、Basic 认证、目标校验、并发限额、审计、指标）都在 Server 里完成，Lua 不含任何授权逻辑。部署与渲染步骤见 [OpenResty tp-* 代理入口](../deployment/openresty-proxy-entry.md)，使用方式见 [HTTP 代理入口](../user-guide/http-proxy-entry.md)。

```yaml
server:
  proxy_entry:
    enabled: false
    listen: 127.0.0.1:8089
    trusted_proxies: ["127.0.0.1/32", "::1/128"]
    domain_suffix: tm.example.com
    connect_timeout: 10s
    idle_timeout: 300s
    shutdown_timeout: 30s
    max_concurrent_tunnels: 512
    max_header_bytes: 16384
    auth_backoff_threshold: 5
```

| 键 | 默认值 | 含义 | 是否必填 |
|---|---|---|---|
| `server.proxy_entry.enabled` | `false` | 总开关。关闭时不创建内部监听，行为与旧版本完全一致 | 否 |
| `server.proxy_entry.listen` | `127.0.0.1:8089` | 内部明文监听地址，必须与 OpenResty 模板的 `__INTERNAL_UPSTREAM__` 渲染结果一致 | 否 |
| `server.proxy_entry.trusted_proxies` | `["127.0.0.1/32","::1/128"]` | 允许写入可信头的对端 CIDR。不在白名单的对端在读请求之前直接断开 | 否 |
| `server.proxy_entry.domain_suffix` | 空 | tp-* 路由的域名后缀，必须与泛解析 DNS 和通配证书一致 | `enabled=true` 时必填 |
| `server.proxy_entry.route_header` | `X-TunnelMesh-Route` | 携带路由身份（来自 SNI）的可信头名 | 否 |
| `server.proxy_entry.client_ip_header` | `X-TunnelMesh-Client-IP` | 携带真实来源 IP 的可信头名；缺失或非法一律 403，绝不回退到对端 IP | 否 |
| `server.proxy_entry.client_port_header` | `X-TunnelMesh-Client-Port` | 携带真实来源端口的可信头名 | 否 |
| `server.proxy_entry.connect_timeout` | `10s` | 开流（到 agent 建立目标连接）的超时，超时返回 504 | 否 |
| `server.proxy_entry.idle_timeout` | `300s` | 隧道双向空闲超时；OpenResty 侧 `read_timeout_ms` 必须等于该值 + 30s | 否 |
| `server.proxy_entry.shutdown_timeout` | `30s` | 进程退出时等待在途隧道排空的时间，可为 0 | 否 |
| `server.proxy_entry.max_concurrent_tunnels` | `512` | 全局并发隧道上限，超限返回 503 + `Retry-After: 5`；0 表示不限 | 否 |
| `server.proxy_entry.max_header_bytes` | `16384` | 单个请求头上限，超出直接断开 | 否 |
| `server.proxy_entry.auth_backoff_threshold` | `5` | 同一路由连续认证失败达到该次数后进入退避（30s 起翻倍，上限 15m）。退避期内不做密码比对，直接返回 407 与稳定错误码 `proxy_auth_backoff` | 否 |

`trusted_proxies` 支持 `0.0.0.0/0` 或 `::/0`。若 `listen` 不是回环地址，这表示信任所有能访问该端口的主机，它们都可能伪造路由身份与来源 IP；生产环境必须用防火墙、安全组或专线限制内部入口的访问范围。命令行与环境变量等价（`TUNNELMESH_SERVER_PROXY_ENTRY_*`），例如：

```bash
tunnelmesh-server --server.proxy_entry.enabled=true \
  --server.proxy_entry.domain_suffix=tm.example.com \
  --server.proxy_entry.listen=127.0.0.1:8089 \
  --server.proxy_entry.idle_timeout=300s \
  --server.proxy_entry.max_concurrent_tunnels=512
```

改完先用 `tunnelmesh-server check-config` 校验再重启。路由本身（出口 agent、认证方式、来源 ACL、目标限制）不在这个配置文件里，全部由管理后台的托管路由维护，创建后 5 秒内生效，不需要重启 Server 或改 nginx。

## 内嵌 VPN 网关（WireGuard）

内嵌 VPN 网关让没有安装 `tunnelmesh-client` 的用户直接用系统自带的 WireGuard 客户端接入内网，出口仍由 Agent 承担。决策背景与边界见 [ADR 0002](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)。

**当前版本只提供管理面。** `server.vpn` 会被完整加载与校验，管理 API 可以签发、列出、修改、轮换、吊销并审计 peer，但 WireGuard 端点与内存态 TUN 设备仍在分阶段实施中，尚未随发行版提供。客户端配置下载（`POST /api/v1/vpn-peers/{peerId}/config:reveal`）在本版返回 409 `vpn_node_disabled`：节点还没有自己的网关身份，渲染出来的 `[Peer] PublicKey` 会是空的，与其下发一个导入即失败的配置文件，不如明确拒绝。也就是说：现在签发的 peer 可以被管理和审计，但拿不到配置文件，即使拿到也还不能建立隧道。对外通知里不要把它描述成已经可用。

与 tp-* 代理入口不同，VPN 端点是一个**独立的公网 UDP 端口**：不经反向代理、不参与 HTTP 路由，必须在防火墙或安全组里单独放行，并单独限流与监控。

```yaml
server:
  vpn:
    enabled: false
    listen: 0.0.0.0:51820
    endpoint_host: gw-1.mesh.example.com
    ip_pool: 10.64.0.0/16
    node_subnet_size: 24
    mtu: 1420
    max_peers: 0
    max_flows_per_peer: 128
    max_flows_total: 0
    packet_rate_per_peer: 0
    connect_timeout: 10s
    idle_timeout: 120s
    shutdown_timeout: 15s
    icmp_enabled: true
    icmp_timeout: 5s
    icmp_max_concurrent: 64
```

| 键 | 默认值 | 含义 | 是否必填 |
|---|---|---|---|
| `server.vpn.enabled` | `false` | 总开关。关闭时进程不创建任何 VPN 资源，行为与旧版本完全一致；管理 API 的写操作返回 409 `vpn_node_disabled`，读操作返回空列表 | 否 |
| `server.vpn.listen` | `0.0.0.0:51820` | WireGuard 端点的公网 UDP 监听地址，需独立放行 | 否 |
| `server.vpn.endpoint_host` | `gw-1.mesh.example.com` | 下发给用户的 `Endpoint` 域名，**只写主机名、不带端口**；端口始终取自 `listen`，二者不会互相矛盾 | `enabled=true` 时必填 |
| `server.vpn.ip_pool` | `10.64.0.0/16` | peer 地址池。必须是 IPv4，且不得落在链路本地、未指定或组播段；可以使用 CGNAT 段（如 `100.64.0.0/16`） | 否 |
| `server.vpn.node_subnet_size` | `24` | 每个 Server 节点从池里切出的子网前缀。必须严格窄于 `ip_pool` 的前缀且不窄于 `/30`；切出的子网总数上限 4096 | 否 |
| `server.vpn.mtu` | `1420` | 隧道 MTU，取值 576–1500。1420 = 1500 − WireGuard 的 72 字节开销 | 否 |
| `server.vpn.max_peers` | `0` | 单节点 peer 上限，触顶时签发返回 503 `vpn_capacity_exhausted`；0 表示不限 | 否 |
| `server.vpn.max_flows_per_peer` | `128` | 单 peer 并发流上限，超限计入 `capacity_exhausted` | 否 |
| `server.vpn.max_flows_total` | `0` | 节点并发流总上限；0 表示不限 | 否 |
| `server.vpn.packet_rate_per_peer` | `0` | 单 peer 每秒包数上限，超限计入 `rate_limited`；0 表示不限 | 否 |
| `server.vpn.connect_timeout` | `10s` | 开流（经 Agent 建立目标连接）的超时 | 否 |
| `server.vpn.idle_timeout` | `120s` | 流空闲回收时间 | 否 |
| `server.vpn.shutdown_timeout` | `15s` | 进程退出时等待在途流排空的上限，可为 0 | 否 |
| `server.vpn.icmp_enabled` | `true` | 节点级 ICMP echo 总开关。这是上限而不是承诺：peer 还要单独开启，且出口 Agent 必须协商到该能力，否则签发返回 409 `vpn_agent_capability_missing` | 否 |
| `server.vpn.icmp_timeout` | `5s` | 单次 echo 应答超时，超时计入 `icmp_timeout` | 否 |
| `server.vpn.icmp_max_concurrent` | `64` | 节点并发 echo 上限 | 否 |

三个「0 表示不限」的计数（`max_peers`、`max_flows_total`、`packet_rate_per_peer`）沿用 `max_concurrent_tunnels` 的既有约定。`icmp_timeout` 与 `icmp_max_concurrent` 只在 `icmp_enabled=true` 时校验，因此关掉 ICMP 不会因为遗留的占位取值而无法启动。

`ip_pool` 与 `node_subnet_size` 由 `internal/vpn` 的 `ParsePool` 直接校验，加载器和分配器共用同一套规则：「能启动」就等价于「真的能分出地址」，启动时读到的报错与首次签发时读到的报错是同一条。每个子网的第一个可用地址保留给节点自己的 VPN 接口，不会分给 peer，所以 `/24` 子网实际可分配 253 个地址、`/30` 子网只剩 1 个。

节点自身的 WireGuard 私钥**不是配置项**，只从环境变量注入：

```bash
TUNNELMESH_VPN_NODE_PRIVATE_KEY=<base64 编码的 32 字节私钥>
```

配置文件会被复制、备份、打进支持包、提交进版本库，而环境变量可以从 Secret Manager 取值且永不落盘，所以私钥只走后者。**本版还不读取该变量**：它由数据面（阶段 6）消费，届时 `enabled: true` 而变量缺失会在启动时快速失败；现在设置它不会有任何效果，也不会让 `config:reveal` 变成可用。下发给每个 peer 的私钥用既有的 `TUNNELMESH_TOKEN_ENCRYPTION_KEY`（AES-256-GCM）密封，与凭据密文同构；密钥不可用时签发与 reveal 返回 503 `credential_secret_unavailable`，不会降级为明文。两类私钥都不会出现在日志、审计与指标里。

命令行与环境变量等价（`TUNNELMESH_SERVER_VPN_*`）：

```bash
tunnelmesh-server --server.vpn.enabled=true \
  --server.vpn.listen=0.0.0.0:51820 \
  --server.vpn.endpoint_host=gw-1.mesh.example.com \
  --server.vpn.ip_pool=10.64.0.0/16 \
  --server.vpn.node_subnet_size=24
```

改完先用 `tunnelmesh-server check-config` 校验再重启。集群里**每个节点必须配置相同的 `ip_pool` 与 `node_subnet_size`**，否则子网租约会互相拒绝；节点通过 `vpn_ip_leases` 各自抢占一个子网，租约由 epoch fencing 保护，失去租约的节点无法继续从该子网分配地址。

## Agent 连接池

Agent 保持一个 `server_url`，但可以复用同一个逻辑 Agent 身份建立多条物理 WebSocket 连接。默认配置禁用扩容：

```yaml
agent:
  server_url: wss://tunnel.example.com/ws/agent
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

## 安全白名单与兼容开关

```yaml
security:
  # 管理 API 与 WebSocket 的 Host 精确白名单；空列表表示不限制。不支持通配符。
  allowed_hosts:
    - tunnel.example.com
  # 浏览器 Origin 精确白名单；空列表表示不限制。不支持通配符。
  allowed_origins:
    - https://tunnel.example.com
  # Deprecated: 迁移期开关，计划在 v0.3.0 移除。
  allow_legacy_connection_tokens: false
```

`allowed_hosts` 按 `Host` 头精确匹配，动态托管域名由路由表匹配 `server.dynamic_suffix`，不需要写进白名单。`allowed_origins` 按规范化后的 Origin 精确匹配；Agent 连接 `wss://tunnel.example.com/ws/agent` 时发送 `https://tunnel.example.com`，Client 连接 `wss://tunnel.example.com/ws/client` 时发送 `https://tunnel.example.com`，启用 WebSSH/SFTP 时还必须包含管理后台的精确 Origin，否则握手直接被拒绝。

WebSocket 握手始终要求请求带且只带一个合法 `Origin` 头，与白名单是否为空无关；用 `websocat` 等工具直连时必须显式指定 `-H 'Origin: https://tunnel.example.com'`。

`allow_legacy_connection_tokens` 是唯一的例外开关：Agent 连接默认只接受 `agent` 类型 service token。设为 `true` 时旧版管理登录 token 也可用于运维接管，该路径写入弃用审计日志，并且计划在 v0.3.0 移除。仅在有存量 Agent 尚未换发 service token 的迁移窗口内临时开启，迁移完成后必须改回 `false`。Client 连接不受该开关影响。

## 管理台身份认证（SSO / MFA / 受信任设备）

管理后台的单点登录、TOTP 两步验证和受信任设备由 `security.auth` 段配置：

```yaml
server:
  # 反向代理地址白名单；只有来自这些地址的 X-Forwarded-For 才可信。
  # 默认空列表：始终使用直连对端地址，伪造头无法影响登录限流和审计。
  trusted_proxies: []

security:
  auth:
    # 管理台 bearer token 有效期；0 表示沿用历史行为（不过期）。
    session_token_ttl: 0s
    login_throttle:
      # 同一 username+IP 桶在 window 内允许失败的次数，超过后封禁 block。
      max_attempts: 10
      window: 5m
      block: 10m
    mfa:
      # 验证器 App 显示的标签，会写进 otpauth:// URL，不能包含冒号。
      issuer: TunnelMesh
      digits: 6
      period: 30s
      # 接受的前后时间步数；0 表示只接受当前步。
      skew: 1
      # 登录二次验证 challenge 的有效期与最大尝试次数。
      challenge_ttl: 5m
      max_attempts: 5
      # 每次绑定发放的一次性恢复码数量。
      recovery_codes: 10
    device_trust:
      enabled: true
      cookie_name: tm_device
      cookie_secure: true
      # lax / strict / none；none 必须同时开启 cookie_secure。
      cookie_same_site: lax
      # 未过期的受信任设备是否可以跳过二次验证。
      bypass_mfa: true
    oidc:
      # 是否在登录页公开列出可用的身份提供商按钮。
      public_providers: true
      http_timeout: 10s
      state_ttl: 10m
      # 回调后换取会话的一次性 ticket 有效期，硬性上限 300s。
      login_ticket_ttl: 60s
      jwks_cache_ttl: 1h
      # 依赖方所有 HTTP 响应体上限（discovery / JWKS / userinfo / token 共用）。
      max_discovery_body_bytes: 1048576
```

### 字段参考

| 键 | 类型 | 默认值 | 取值范围 | 含义 |
| --- | --- | --- | --- | --- |
| `security.auth.session_token_ttl` | duration | `0s` | ≥ 0 | 管理台 bearer token 有效期。`0` 保留升级前的“不过期”行为 |
| `security.auth.login_throttle.max_attempts` | int | `10` | 1–1000 | 单个 username+IP 桶在一个 window 内允许的失败次数 |
| `security.auth.login_throttle.window` | duration | `5m` | 1s–24h | 失败计数窗口 |
| `security.auth.login_throttle.block` | duration | `10m` | 1s–24h | 超过阈值后的封禁时长，响应带 `Retry-After` |
| `security.auth.mfa.issuer` | string | `TunnelMesh` | ≤ 64 字符，禁止 `:` | otpauth 标签中显示的发行方名称 |
| `security.auth.mfa.digits` | int | `6` | `6` 或 `8` | TOTP 位数 |
| `security.auth.mfa.period` | duration | `30s` | 15s–120s | TOTP 时间步长 |
| `security.auth.mfa.skew` | int | `1` | 0–2 | 允许的前后时间步数，用于容忍客户端时钟漂移 |
| `security.auth.mfa.challenge_ttl` | duration | `5m` | 30s–30m | 登录二次验证 challenge 的有效期 |
| `security.auth.mfa.max_attempts` | int | `5` | 1–20 | 单个 challenge 允许的验证次数 |
| `security.auth.mfa.recovery_codes` | int | `10` | 1–50 | 每次绑定发放的一次性恢复码数量 |
| `security.auth.device_trust.enabled` | bool | `true` | — | 是否允许签发受信任设备（仅作为数据库首启种子） |
| `security.auth.device_trust.cookie_name` | string | `tm_device` | ≤ 64 字符，合法 cookie token | 受信任设备 cookie 名称 |
| `security.auth.device_trust.cookie_secure` | bool | `true` | — | cookie 是否只通过 HTTPS 发送 |
| `security.auth.device_trust.cookie_same_site` | string | `lax` | `lax`/`strict`/`none` | cookie SameSite 属性；`none` 必须搭配 `cookie_secure: true` |
| `security.auth.device_trust.bypass_mfa` | bool | `true` | — | 未过期的受信任设备是否跳过二次验证（仅作为首启种子） |
| `security.auth.oidc.public_providers` | bool | `true` | — | 是否公开 `GET /api/v1/auth/oidc/providers`；关闭后返回 `404` |
| `security.auth.oidc.http_timeout` | duration | `10s` | ≤ 5m | 访问 issuer discovery、JWKS、token 和 userinfo 端点的超时 |
| `security.auth.oidc.state_ttl` | duration | `10m` | ≤ 1h | authorization state / nonce / PKCE verifier 的有效期 |
| `security.auth.oidc.login_ticket_ttl` | duration | `60s` | ≤ 300s | 回调后换取会话的一次性 ticket 有效期 |
| `security.auth.oidc.jwks_cache_ttl` | duration | `1h` | ≤ 24h | JWKS 公钥缓存时长 |
| `security.auth.oidc.max_discovery_body_bytes` | int64 | `1048576` | 1024–16777216 | 依赖方**所有** HTTP 响应体的字节上限。实际覆盖 discovery、JWKS、userinfo 与 token 四个端点（共用同一限制），键名里的 `discovery` 是历史命名；超限直接判为失败，不会截断后继续解析 |
| `server.trusted_proxies` | []string | `[]` | 每项为 IP 或 CIDR | 允许被信任 `X-Forwarded-For` 的反向代理地址 |

所有叶子键都可用环境变量覆盖：前缀 `TUNNELMESH_`，点号换成下划线，例如
`TUNNELMESH_SECURITY_AUTH_MFA_ISSUER`、`TUNNELMESH_SECURITY_AUTH_LOGIN_THROTTLE_BLOCK`、
`TUNNELMESH_SERVER_TRUSTED_PROXIES`。这些键没有对应的命令行参数。

未设置的叶子在加载时补齐默认值；已设置但越界的值不会被静默改写，而是由 `check-config` 直接报错。
`security.auth.mfa.skew: 0` 是合法取值，表示“只接受当前时间步”，不会被当作未配置。

`server.trusted_proxies` 只影响管理 API 的客户端 IP 解析（登录限流桶、审计记录、受信任设备的
`ip` 字段）。它独立于 `server.proxy_entry.trusted_proxies`（tp-* 代理入口专用，默认
`127.0.0.1/32`、`::1/128`），两者不要混用。Server 直接对公网暴露时必须保持
`server.trusted_proxies` 为空，否则任何客户端都能伪造 `X-Forwarded-For` 自选限流桶。经过
Nginx 反代时应只填写反代实际来源网段，例如 `["127.0.0.1/32", "::1/128"]`。

### 配置只是种子，数据库才是权威

**`security.auth` 中的策略项只在首次启动时写入 `auth_settings` 表；此后数据库行是唯一权威来源。**

具体来说：

- `session_token_ttl`、`device_trust.enabled`、`device_trust.bypass_mfa` 会在 `auth_settings`
  行缺失时作为种子写入一次（`mfa_mode` 固定以 `disabled` 起播，保证升级后的部署行为与升级前完全一致）。
- 管理员通过 `PUT /api/v1/auth/policy` 或后台“单点登录”页修改策略后，**再改配置文件或环境变量都不会生效**，
  重新部署也不会把数据库里的决定覆盖回去。
- 设备有效期与单账号最大设备数没有配置键，只能由 `auth_settings.device_trust_ttl_seconds`
  （1 小时–90 天，默认 30 天）和 `auth_settings.max_trusted_devices`（1–100，默认 10）决定。
- OIDC 提供商的 issuer、client id、client secret、role mapping 全部存在 `oidc_providers` 表，
  配置文件里没有任何 per-provider 键。
- TOTP 参数（`issuer`、`digits`、`period`）在绑定时即固化进 otpauth URL，修改配置只影响之后的新绑定，
  不会追溯已绑定账号。

需要临时全量关闭两步验证时，改数据库而不是改配置：把 `auth_settings.mfa_mode` 置为 `disabled`
并停用所有 OIDC 提供商即可，无需重新发布。步骤见
[Schema 升级与回滚](schema-upgrades.md#v13-to-v14)。

### 身份密钥与 fail-closed

SSO 与 MFA 需要存储可恢复的秘密：OIDC `client_secret`、TOTP 共享密钥、登录 challenge payload
（state / nonce / PKCE verifier）和 login ticket。它们复用 service token reveal 的同一把密钥：

```bash
# base64（RawStd / Std）或 hex，解码后必须是 16、24 或 32 字节；只能由环境变量或 Secret Manager 注入
export TUNNELMESH_TOKEN_ENCRYPTION_KEY='<from your secret manager>'
# 可选；未设置时 key id 固定为 default。轮换密钥时必须同步更换
export TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID='<key id from your secret manager>'
```

未配置该密钥时 Server 仍能启动，密码登录和已绑定账号的 MFA 校验也照常工作，但任何需要落盘秘密的
操作会 fail-closed：

- `POST /api/v1/auth/mfa/enroll`（MFA 绑定）返回 `503`，`data.error=secret_storage_unavailable`；
- `POST /api/v1/sso/providers` 和 `PUT/PATCH /api/v1/sso/providers/{id}` 在**请求带了非空
  `clientSecret` 时**返回 `503`，`data.error=secret_storage_unavailable`。只靠 PKCE 的公共客户端
  （`clientSecret` 留空）可以创建，因为它没有需要落盘的秘密；
- 但**任何 OIDC 登录都不可用**：authorize 需要创建加密的 state challenge，`ChallengeStore.Create`
  在密钥不可用时直接 fail-closed，所以 `GET /api/v1/auth/oidc/<provider>/authorize` 也返回 `503`；
- 已存有 client secret 的提供商在密钥缺失或 key id 不匹配时无法解析，同样返回 `503`。

系统不会退化成明文存储。集群中所有 Server 节点必须配置同一把密钥和同一个 key id，否则一个节点
签发的 challenge 或 ticket 在另一个节点上无法解密。

`TUNNELMESH_TRACE_SIGNING_KEY` 与身份认证无关，只用于跨节点 traceroute hop 签名链校验；集群所有
Server/relay 节点同样必须一致。两个密钥都不得写入提交的配置文件、日志或工单。

完整的启用流程、后台操作和排障表见[单点登录与两步验证](../user-guide/sso-and-mfa.md)。

## 发行下载源

管理后台“发行管理”页展示的仓库地址来自 `downloads.github_repository`：

```yaml
downloads:
  # 格式固定为 owner/name，默认 nnworld/TunnelMesh。
  github_repository: nnworld/TunnelMesh
```

该键没有对应的命令行参数，只能通过配置文件或环境变量 `TUNNELMESH_DOWNLOADS_GITHUB_REPOSITORY` 覆盖。使用 GitHub Enterprise 或内部镜像时改成对应的 `owner/name`；格式非法（缺少 `/`、包含多个 `/`、空 owner 或 name、含空格或 `?#@` 等字符）时 `check-config` 会失败。发行包命名由发布契约生成，不由该配置决定；Server 只展示下载信息和 SHA256 校验和，不代理 GitHub 凭据，也不缓存发行文件。发布流程见[二进制发行](../deployment/binary-release.md)。

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

The same key and key id also seal the console identity secrets: OIDC `client_secret`, the TOTP shared
secret, and every `auth_challenges` payload (OIDC state/nonce/PKCE verifier and the post-callback login
ticket). Without it, MFA enrollment and OIDC provider creation fail closed with `503` and
`data.error=secret_storage_unavailable` instead of storing plaintext; see
[管理台身份认证](#管理台身份认证sso--mfa--受信任设备).

For multi-server deployments set the same high-entropy `TUNNELMESH_TRACE_SIGNING_KEY` on every server/relay node so hop signatures can be verified across the cluster. It is never returned in traceroute output.
