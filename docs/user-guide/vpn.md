# 内嵌 VPN 网关：使用者帮助

不安装 `tunnelmesh-client`，直接用系统自带的 WireGuard 客户端接入内网。管理员在后台签发一个 peer，
你导入一份配置文件，隧道建立后按 `AllowedIPs` 指定的网段访问内网服务，出口由 Agent 承担。

与 [HTTP 代理入口（tp-*）](http-proxy-entry.md) 的区别是层次：tp-* 工作在 HTTP 层，只能转发 HTTP(S)；
VPN 工作在 IP 层，TCP、UDP、ICMP echo 都能走，代价是必须导入一份系统级配置。

部署侧的放行、密钥注入与 IP 池规划见 [VPN 网关部署](../deployment/vpn-gateway.md)；
运维侧的容量、指标与故障处理见 [VPN 网关运维](../operations/vpn.md)。

> **前提：Server 必须是 `-tags vpn` 构建，且 `server.vpn.enabled: true`。** 数据面在 `//go:build vpn` 后面，
> 官方发行包与镜像的构建矩阵尚不含该 tag；不带它的 Server 会在启动时直接拒绝而不是静默不工作。
> 后台能签发配置不等于隧道能连通，构建变体与放行见 [VPN 网关部署](../deployment/vpn-gateway.md)。

## 拿到配置文件

配置文件由管理员签发，**私钥只显示一次**，服务端以 AES-256-GCM 密封保存，之后只能由管理员显式 reveal。

后台路径：VPN 网关 → 选中 peer → “使用说明”抽屉 → 输入 `REVEAL` 并勾选风险确认。抽屉关闭即清除，
不写 `localStorage`，也不进前端持久层。

对应的 API（管理员或该 peer 的所有者）：

```bash
curl -sS -X POST "https://tunnel.example.com/api/v1/vpn-peers/<peerId>/config:reveal" \
  -H "Authorization: Bearer <token from the console>" \
  -H "X-VPN-Config-Reveal-Confirm: REVEAL" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "Content-Type: application/json" \
  -d '{"acknowledgeRisk": true}'
```

响应带 `Cache-Control: no-store`，`data` 里是完整的 wg-quick 配置文本。每次 reveal 都写审计日志，
审计只记录 peer ID 与动作，不含任何密钥材料。

拿到后立刻保存到你自己的密码管理器：窗口关掉就要再 reveal 一次，而 reveal 是有审计痕迹的操作。

## 配置文件长什么样

```ini
[Interface]
PrivateKey = <你的 peer 私钥，只显示一次>
Address = 10.64.3.17/32
MTU = 1420

[Peer]
PublicKey = <网关节点的公钥>
AllowedIPs = 10.0.0.0/8, 192.168.0.0/16
Endpoint = gw-1.mesh.example.com:51820
PersistentKeepalive = 25
```

| 字段 | 含义 | 能不能改 |
| --- | --- | --- |
| `PrivateKey` | 这个 peer 的身份。改了就是另一个人，网关不认 | 不能 |
| `Address` | 你在隧道里的 `/32` 地址，由节点子网池分配，全网唯一 | 不能 |
| `MTU` | 隧道 MTU，1420 = 1500 − WireGuard 的 72 字节开销 | 不建议；调小只会更慢 |
| `PublicKey` | 网关节点的公钥。故障转移换 IP 时它不变 | 不能 |
| `AllowedIPs` | 哪些目的网段走隧道。**这就是你的访问范围**，不在里面的地址根本不会被送进隧道 | 改不了：网关侧同一份策略会再判一次 |
| `Endpoint` | 网关的 `域名:端口`。用域名而不是裸 IP，是为了故障转移换 IP 时你不用重新导入 | 端口不能改；域名解析问题见排障 |
| `PersistentKeepalive` | 25 秒一次保活，让 NAT 映射不被回收 | 可以调小，不要调大到 NAT 超时以上 |

`AllowedIPs` 不是“全流量走 VPN”。默认只包含管理员授权的网段，其余流量仍走你本机的默认路由。
想要全流量接管，得由管理员在 peer 策略里放开，并且注意这会把你自己的公网流量也送进内网出口。

## 各平台导入

### Windows

1. 从 <https://www.wireguard.com/install/> 安装官方客户端。
2. 右下角托盘图标 → `Add Tunnel`（或 `Manage tunnel` 窗口里的 `Add Tunnel`）。
3. 选择 `From file` 或直接粘贴上面那段配置，保存。
4. 点 `Activate`。状态显示 `Connected` 且列出 `Address` 即成功。

命令行等价（管理员 PowerShell）：把配置存成 `mesh.conf`，`wg-quick up mesh.conf` 建隧道、
`wg-quick down mesh.conf` 拆掉，`wg show` 看当前状态与最近握手时间。

### macOS

App Store 搜索 `WireGuard` 安装官方客户端 → `Import tunnel(s) from file` 或 `Create from scratch`
粘贴 → 保存 → 右侧开关拨到绿。

菜单栏图标里可以只让这一个隧道生效，不影响系统其它网络。命令行用法与 Windows 的 `wg-quick` 相同
（`brew install wireguard-tools`）。

### iOS / Android

安装官方 `WireGuard` App → `+` → `Import from file` / `Create from file` → 粘贴配置 → 保存 → 打开开关。
系统会提示“添加 VPN 配置”，允许即可。

移动端有两点要注意：`PersistentKeepalive = 25` 是必须的，否则切回前台时 NAT 映射可能已经失效，
表现为“刚唤醒时第一个连接失败”；蜂窝网络下运营商 NAT 超时更短，掉线重连是正常现象。

### Linux（服务器 / 无图形界面）

```bash
sudo apt-get install wireguard-tools        # 或 dnf/pacman 对应包
sudo install -m 600 mesh.conf /etc/wireguard/mesh.conf
sudo wg-quick up mesh
sudo wg show mesh                            # 看握手时间与流量计数
```

开机自启用 `systemctl enable --now wg-quick@mesh`。

### 校验隧道真的通了

```bash
ping <你的 Address 去掉 /32>            # 本端接口地址，通只说明接口起来了
ping <AllowedIPs 里的一个内网地址>       # 通说明端到端通了（需要 peer 开了 ICMP 且 Agent 支持）
curl -sS -m 5 http://<内网服务地址>:<端口>/   # 最直接的判据
```

`wg show` 里的 `latest handshake` 在 2 分钟内有值，说明 Noise 握手成功；一直是
`never` 或几分钟前，说明 UDP 端口不通或公钥不对。

## 能力边界

以下是设计边界，不是 bug，也不是配置能改的。逐条对应设计规格 §4.3。

支持：**IPv4 的 TCP、UDP、ICMP echo**。

不支持：

- **源地址不保留**。内网服务看到的源地址是 Agent 宿主机的地址，不是你的 VPN IP。因此内网侧无法按
  VPN 用户做 ACL，也无法在应用日志里把请求归因到某个 peer。需要归因时走 tp-* 代理入口或 client 转发，
  那两条链路在应用层携带身份。
- **只有 ICMP echo**。`ping` 能用；`traceroute` 依赖的 TTL 超时、目的不可达、分片需要等其它 ICMP 类型
  不会回，所以 `traceroute` 在隧道里表现为一直 `* * *`。这是非特权 ping socket 的边界，网关没有
  `CAP_NET_RAW`，也不打算要。用后台的[逻辑 traceroute](../operations/network-probes.md) 代替。
- **只有 TCP / UDP / ICMP-echo 三种 IP 协议**。GRE、SCTP、IPsec 嵌套、组播一律丢弃并计数。
  也就是说不能在 VPN 里再套一层站点到站点 VPN。
- **不支持 IP 分片**。分片包丢弃并计数。TCP 因为在网关侧终结、两条腿各自协商 MSS，不存在 PMTUD 黑洞；
  UDP 数据报超过路径 MTU 时会被丢弃，且**不会**回 `Fragmentation Needed`（见下一节）。
- **Agent 不能主动向用户侧发起连接**。隧道只能由你这一侧发起，内网服务无法回连你的机器。

### 失败是静默的

IP 层没有响应通道，网关**无法把错误码回传给你**。所有拒绝都表现为“连不通”或“ping 不通”，
不会有 RST、不会有 ICMP 错误、不会有提示页。这与 tp-* 代理入口的行为一致
（见 [HTTP 代理入口](http-proxy-entry.md#错误码自助排查)）。

为什么故意这样：回一个 `Destination Unreachable` 就等于告诉对端“这个地址存在，只是不让你访问”，
而这正是出口策略要隐藏的事实。静默是唯一不泄露信息的答复。

代价是排障只能靠服务端。被拒绝的原因、目标地址与计数全部落在网关的 `error_class` 指标和
`vpn_packet_denied` 审计事件里，运维侧的对照表见 [VPN 网关运维](../operations/vpn.md#拒绝原因对照)。
你自己能判断的只有“在不在 `AllowedIPs` 里”和“端口在不在允许列表里”。

### UDP 校验和不会被二次校验

入向 UDP 数据报的校验和网关**不重复校验**。WireGuard 已经对每个数据报做了 AEAD 认证，
能通过认证的载荷不可能在传输中被偶然破坏；剩下能构造坏校验和的只有持有合法 peer 私钥的一方，
后果是内网服务忽略一个坏数据报——和它自己收到一个坏数据报的处理完全一样。
为此新增一个 `error_class` 不值得：已发布的类集合是对外契约。

实际影响：不要指望网关帮你发现应用层的 UDP 数据损坏，端到端校验仍由应用自己做。

## 排障

### `wg show` 里 `latest handshake` 一直是 never

握手没成功，问题在你和网关之间，与内网策略无关。按顺序查：

1. **UDP 端口通不通**。`Endpoint` 的端口（默认 51820）必须能从你的网络出去。公司网络封 UDP 出站时
   表现就是这样。用 `nc -vzu gw-1.mesh.example.com 51820` 粗测，或让管理员确认安全组已放行。
2. **域名解析对不对**。`Endpoint` 是域名，解析到的 IP 必须是网关实际监听的公网地址。故障转移后
   DNS 未生效会连到旧 IP。`dig +short gw-1.mesh.example.com` 核对。
3. **配置是不是旧的**。管理员轮换过密钥后，旧配置立即失效且不会自动更新，必须重新 reveal 导入。
   被吊销的 peer 同样表现为握手不成功。
4. **私钥有没有粘全**。base64 里换行或空格丢失会被客户端拒绝，也可能被静默截断。

### 握手成功但什么都不通

隧道起来了，问题在策略或 Agent。依次确认：

1. **目标在不在 `AllowedIPs` 里**。不在的话流量根本没进隧道，本机路由直接发出去了——现象和
   “隧道不通”一模一样，这是最容易误判的一条。`ip route get <目标>`（Linux）或
   `route get <目标>`（macOS）看下一跳是不是隧道接口。
2. **端口在不在允许列表里**。peer 可以配端口白名单，白名单外的端口静默丢弃。
3. **内网服务本身可达吗**。让管理员在 Agent 宿主机上直接 `curl` 一次目标，区分“网关没转发”和
   “服务本来就down”。
4. **Agent 在线吗**。出口由 Agent 承担，Agent 掉线时所有新开连接都失败。后台 Agent 列表能看状态。

### `ping` 不通，但 TCP 能通

`ping` 需要三个条件同时成立，缺一个就静默失败：

- peer 的 `icmp_enabled` 为 true（管理员在签发或编辑时设置）；
- 节点级 `server.vpn.icmp_enabled` 为 true；
- 出口 Agent 在线且已协商 `stream_icmp_echo.v1`（Agent 侧 `agent.streams.icmp_enabled: true`
  且宿主机放开了 `net.ipv4.ping_group_range`）。

集群里还有一个已知限制：能力只记录在 Agent 当前连接的那个节点的会话里，签发请求落到别的节点时
无法核实，此时签发仍会成功但 `icmp_enabled` 实际不可用，运行时按 `icmp_unsupported` 拒绝。
详见 [Server 管理后台](server-admin.md#vpn-网关-peer-管理)。

TCP 能通说明隧道和策略都没问题，只是 echo 这一条链路的能力没凑齐。

### 大文件传输慢或卡住

- **UDP 大包被丢**：不支持分片，超过路径 MTU 的 UDP 数据报直接丢弃且无 ICMP 反馈。把应用的
  UDP 报文控制在 1420 字节以内，或用 TCP。
- **TCP 不受影响**：TCP 在网关侧终结，两条腿各自协商 MSS，不存在 PMTUD 黑洞。TCP 慢更可能是
  内网服务本身或 Agent 宿主机的带宽。

### 需要管理员配合查的

你自己看不到服务端计数。把这些信息给管理员，能直接定位：你的 `Address`（`/32` 那个）、
目标地址与端口、协议、大致时间点。管理员用 peer ID 查 `vpn_packet_denied` 审计与
`tunnelmesh_streams_total{error_class=...}`，几秒内就能说出是哪一类拒绝。
