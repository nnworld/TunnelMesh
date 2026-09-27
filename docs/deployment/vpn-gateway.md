# VPN 网关部署

内嵌 VPN 网关让没有安装 `tunnelmesh-client` 的用户直接用系统自带的 WireGuard 客户端接入内网。
它与既有 443 入口**完全独立**：不经 OpenResty/Nginx，由 Server 进程直接持有一个公网 UDP socket。

决策背景、被解除的旧约束和特权边界结论见
[ADR 0002](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)。
配置键的完整取值范围见 [配置说明](../operations/configuration.md#内嵌-vpn-网关wireguard)。
使用者视角的导入与排障见 [VPN 使用者帮助](../user-guide/vpn.md)。

## 适用前提

| 前提 | 说明 |
| --- | --- |
| 二进制带 `-tags vpn` | 数据面在 build tag 后面，默认构建里没有。见下节 |
| 一个公网 UDP 端口 | 默认 51820，需在云安全组与主机防火墙**单独**放行 |
| 节点私钥由环境变量注入 | `TUNNELMESH_VPN_NODE_PRIVATE_KEY`，不是配置项 |
| 出口 Agent 在线 | VPN 只到网关，网关到内网的那一腿仍由 Agent 承担 |
| `TUNNELMESH_TOKEN_ENCRYPTION_KEY` | peer 私钥的密封存储复用它，缺失时签发返回 503 |

不需要 `CAP_NET_ADMIN`，也不需要 `CAP_NET_RAW`。网关的 TUN 设备是进程内的内存设备
（gvisor 的 `channel.Endpoint`），没有内核对象、没有 `/dev/net/tun` 描述符，
`File()` 返回 nil。ICMP echo 由 Agent 侧的非特权 ping socket 发出，同样不需要特权。
容器里以非 root 运行即可，`deploy/` 下的 unit 与 Compose 文件不需要为 VPN 改动权限。

## 构建变体与体积

数据面的两个依赖（`gvisor.dev/gvisor` 与 `golang.zx2c4.com/wireguard`）只被带
`//go:build vpn` 的文件引用，因此默认构建的依赖图里根本没有它们——隔离由构建保证，不靠代码评审。

```bash
go build -o tunnelmesh-server ./cmd/tunnelmesh-server              # 不含数据面
go build -tags vpn -o tunnelmesh-server ./cmd/tunnelmesh-server    # 含数据面
```

自检隔离是否成立：

```bash
go list -deps ./cmd/tunnelmesh-server | grep -Ec 'gvisor|golang.zx2c4.com/wireguard'   # 必须是 0
go list -deps -tags vpn ./cmd/tunnelmesh-server | grep -Ec 'gvisor'                    # 必须 > 0
```

本机实测（`darwin/arm64`，`go1.27.1`，未 strip、未加 `-ldflags "-s -w"`）：

| 构建 | 字节数 |
| --- | --- |
| 默认 | 39,822,754 |
| `-tags vpn` | 45,243,154 |
| 增量 | **+5,420,400（约 5.17 MiB，+13.6%）** |

发行包要不要带 VPN 变体是一个取舍：带上多 5 MiB，不带则用户必须自己编译。
当前 [跨平台可执行文件打包](binary-release.md) 的构建矩阵**不含** `-tags vpn`，
需要数据面的部署自行构建。

## 无 tag 构建 + `enabled: true`：启动即失败

这是刻意设计。一个不含数据面的进程如果照常报告 ready，用户看到的会是“隧道配置导入成功但全部超时”，
而线索 nowhere near 构建方式。所以装配点在 `internal/server/runtime.go` 直接拒绝启动：

```
server runtime: vpn gateway: this binary was built without VPN support; rebuild with -tags vpn
```

进程退出，不监听任何端口。同理，节点私钥缺失或不可用时也是启动失败，原文分别是：

```
server runtime: vpn gateway: vpn: TUNNELMESH_VPN_NODE_PRIVATE_KEY is not set, so the gateway has no wireguard identity
server runtime: vpn gateway: vpn: TUNNELMESH_VPN_NODE_PRIVATE_KEY is not a usable wireguard private key
```

`server.vpn.enabled: false` 时**根本不读这个环境变量**，所以不使用 VPN 的部署不必注入一个用不上的密钥。

一个例外值得知道：管理 API 不会因为密钥缺失而挂掉。`config:reveal` 在没有节点身份时返回
409 `vpn_node_disabled`，而不是渲染一个 `[Peer] PublicKey` 为空、导入即失败的配置文件。
这样操作员仍然能用后台吊销旧密钥签发的 peer。

## 节点私钥注入

只从环境变量注入，禁止写进配置文件或代码库，禁止出现在日志、审计与指标中。
base64（`wg(8)` 的写法）和 64 字符 hex（多数 keygen 一行命令的写法）都接受，
因为工具链不统一，只认一种会让操作员拿到一条无法据以行动的报错。

```bash
# 生成（任选一种）
wg genkey                                   # 输出 base64
openssl rand 32 | xxd -p -c 64              # 输出 hex
```

systemd：

```ini
[Service]
EnvironmentFile=/etc/tunnelmesh/vpn-node.env   # chmod 600，属主与 Server 进程一致
```

`/etc/tunnelmesh/vpn-node.env`：

```
TUNNELMESH_VPN_NODE_PRIVATE_KEY=<base64 or 64-char hex>
```

Docker Compose 用 `environment:` 或 `env_file:`，不要把值写进 `docker-compose.*.yml`
（那些文件在仓库里）。Kubernetes 用 Secret 挂成环境变量。

**私钥一旦更换，所有已下发配置立即失效**，必须全部重新 reveal 导入。轮换前先想清楚影响面，
并准备好重新下发的通道。节点公钥本身不是秘密，`config:reveal` 渲染的
`[Peer] PublicKey` 就是它。

## `listen`：通配还是指定 host

上游 wireguard-go 的默认 bind 监听 `":"+port`，也就是所有接口。网关换成了自己的 bind，
因为 `server.vpn.listen` 写了 host 就必须真的在那个 host 上——一个被操作员放在内网地址上的网关，
如果实际监听在所有接口，就会在防火墙规则从未覆盖的接口上应答握手。

```yaml
server:
  vpn:
    listen: 0.0.0.0:51820        # 通配：单网卡机器、容器内，通常就是它
    # listen: 203.0.113.10:51820 # 指定：多网卡机器上只暴露公网那一块
```

- 容器里几乎总是用 `0.0.0.0`，由 `-p 51820:51820/udp` 决定对外映射。
- 多网卡裸机上用具体公网地址，让内网网卡根本不监听。
- **只支持 IPv4**。地址池、包解析和出口策略都是 IPv4，一个 v6 监听器只会接受它无法承载任何包的握手。
- bind 不支持 socket mark。WireGuard 的配置协议里有 `fwmark` 这一行，但网关选择明确拒绝
  （`vpn: this bind cannot set a socket mark`）而不是静默忽略：忽略会让一条被相信生效的路由策略实际不存在。
  目前没有配置键会设置它，这条只是防御。

## `endpoint_host` 与 DNS

下发给用户的 `Endpoint` 用**域名**而不是裸 IP，这样故障转移换 IP 时用户配置不必重发。

```yaml
server:
  vpn:
    endpoint_host: gw-1.mesh.example.com   # 只写主机名，不带端口
    listen: 0.0.0.0:51820                  # 端口只从这里取
```

端口始终来自 `listen`，`endpoint_host` 里写了端口是配置错误。这样两者不可能互相矛盾。

DNS 侧要求：

- A 记录指向网关实际监听的公网地址。
- TTL 建议 ≤ 60s，故障转移时用户端才会及时跟随。
- 用户侧解析失败表现为握手 `latest handshake: never`，与端口被封无法区分，
  所以 DNS 变更要当成一次发布来对待。
- 一个节点一个 `endpoint_host`。多节点部署下每个节点写自己的域名，
  用户配置绑定在签发它的那个节点上。

## IP 池与子网规划

```yaml
server:
  vpn:
    ip_pool: 10.64.0.0/16
    node_subnet_size: 24
```

每个 Server 节点从池里**租约**一个子网（`vpn_ip_leases`，epoch fencing 保护），
peer 的 `/32` 从本节点子网里分配。集群里所有节点必须配置**相同的
`ip_pool` 与 `node_subnet_size`**，否则子网租约会互相拒绝。

容量算法：

| `ip_pool` | `node_subnet_size` | 节点数上限 | 每节点可分配 peer 数 |
| --- | --- | --- | --- |
| `10.64.0.0/16` | `24` | 256 | 253 |
| `10.64.0.0/16` | `22` | 64 | 1021 |
| `100.64.0.0/16`（CGNAT） | `24` | 256 | 253 |

每个子网的**第一个可用地址保留给节点自己的 VPN 接口**，不分给 peer，所以 `/24` 实际 253 个、
`/30` 只剩 1 个。切出的子网总数上限 4096，`node_subnet_size` 必须严格窄于 `ip_pool` 的前缀且不窄于 `/30`。

选段注意：

- 可以用 CGNAT 段（`100.64.0.0/16`）避开与用户内网重叠。
- **不要**与用户实际要访问的内网重叠。`AllowedIPs` 是路由决策，重叠会让用户本机流量走向错乱，
  而且现象与“隧道不通”一致，极难排查。
- 不要落在链路本地、未指定或组播段，加载时会直接拒绝。

`ip_pool` 与 `node_subnet_size` 由 `internal/vpn` 的 `ParsePool` 校验，加载器和分配器共用同一套规则：
“能启动”就等价于“真的能分出地址”，启动时的报错与首次签发时的报错是同一条。

改完先 `tunnelmesh-server check-config` 再重启。

## 防火墙放行

VPN 端口不经反向代理，Nginx 的配置对它没有任何作用。必须单独放行：

```bash
# nftables
nft add rule inet filter input udp dport 51820 iifname "eth0" accept

# firewalld
firewall-cmd --permanent --add-port=51820/udp && firewall-cmd --reload

# ufw
ufw allow 51820/udp
```

云安全组同理，单独加一条 UDP 入站规则。

放行之外还要**单独限流与监控**：这个端口不共享 443 入口的限流桶，
一个被滥用的 peer 只会消耗它自己的 `packet_rate_per_peer` 预算，但 UDP 放大与握手风暴
需要在这个端口上单独设防。运维侧的指标与告警见 [VPN 网关运维](../operations/vpn.md)。

只放行 UDP。网关不监听任何 TCP 端口用于 VPN，公网 UDP 也不是既有 HTTP/HTTPS/WSS 入口的一部分。

## 验证

```bash
# 1. 配置合法
tunnelmesh-server check-config

# 2. 启动日志里应出现（顺序即装配顺序）
#    vpn_gateway_started  node_id=... listen=... port=51820 mtu=1420 peers=N
#    vpn_gateway_enabled  node_id=... listen=... endpoint_host=gw-1.mesh.example.com
#    vpn_peers_loaded     node_id=... peers=N refused=M

# 3. 端口在监听（IPv4 UDP）
ss -lunp | grep 51820

# 4. 节点身份就绪：后台签发一个 peer 并 reveal，配置里的 [Peer] PublicKey 非空

# 5. 真实客户端接入后，网关日志应出现节点地址学习
#    vpn_node_address_learned  node_id=... subnet=10.64.3.0/24

# 6. 节点状态接口
curl -sS -H "Authorization: Bearer <token from the console>" \
  https://tunnel.example.com/api/v1/vpn-nodes
```

`/api/v1/vpn-nodes` 返回本节点的 `enabled`、`listen`、`endpointHost`、`subnet`、
`allocated`、`capacity`、`peers`、`icmpCapable`。计数器读自子网租约与 peer 表，
不是从前缀推算的——推算出来的容量是签发方未必能兑现的承诺。

## 回滚（5 分钟内）

不回滚代码也能止损，两条路，按代价从小到大：

1. `server.vpn.enabled: false` + 重启。进程不创建任何 VPN 资源，行为与旧版本完全一致；
   管理 API 的写操作返回 409 `vpn_node_disabled`，读操作返回空列表。
2. 换回不带 `-tags vpn` 的二进制。这是最强的止损：数据面根本不在进程里。

数据面**没有持久化状态**：流表、监听器注册表、peer 内存视图、拒绝聚合器全在内存。
回滚不需要数据补偿、不需要迁移、不需要清理。数据库里的 `vpn_peers` / `vpn_ip_leases` 行
在回滚后仍可被管理 API 读写。

退出时 `shutdown_timeout`（默认 15s）是 drain 上限，超时则强制关闭在途流并在
`vpn_gateway_stopped` 里报告 `in_flight_at_timeout` 与 `flows_forced`。
把 `shutdown_timeout` 设成 0 表示不等待。

## 相关文档

- [配置说明 · 内嵌 VPN 网关](../operations/configuration.md#内嵌-vpn-网关wireguard)：全部键、取值范围、默认值
- [配置示例](../operations/config-examples.md)：单机 SQLite、集群 MySQL、集群 etcd 三份示例
- [VPN 网关运维](../operations/vpn.md)：容量、租约、`error_class`、指标与故障处理
- [VPN 使用者帮助](../user-guide/vpn.md)：导入步骤、能力边界、用户侧排障
- [ADR 0002](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)：为什么解除“公网只开 HTTP/HTTPS/WS”
