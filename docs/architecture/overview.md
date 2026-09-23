# TunnelMesh 架构概览

Server 负责控制面、管理 API、Web 管理后台、HTTP/WSS 公网入口和 relay；Agent 通过 TLS WebSocket 主动连接 Server 并访问其宿主机/内网服务；Client 通过 TLS WebSocket 发起用户侧 forward、publish 和 SSH/stdin 代理。

tp-* HTTP 代理入口的链路（复用既有 443，不新增公网端口；唯一策略执行点是 Server）：

```text
浏览器 / curl / 系统代理（HTTPS 代理方案，只能填 https://）
  └─ TLS + SNI: tp-<name>.<domain_suffix>          复用既有 443，不新增公网端口
       └─ OpenResty tp-* server 块：server 级 access_by_lua_file
            · 打过 proxy_connect 补丁的内核对 CONNECT 跳过 location 匹配
            · 只搬字节，不含任何策略判断
            · 注入可信头 X-TunnelMesh-Route（来自 SNI）/ -Client-IP / -Client-Port
            └─ 明文回环 127.0.0.1:8089（server.proxy_entry.listen）
                 └─ tunnelmesh-server：ProxyEntryListener
                      · trusted_proxies 前置校验，不在白名单则读请求前直接关闭
                      · proxyentry 策略链：身份解析 → 源 IP ACL → Basic 认证 → 目标校验 → 并发限额
                      └─ relay.NodeTransport.OpenStream（集群模式下自动跨节点）
                           └─ Agent 出口：目标侧二次 SSRF / 私网 / 端口校验后建立真实连接
```

请求链路遵循 Handler → Service → Repository。管理数据以 SQLite 或 MySQL 为权威来源，租约/注册发现默认使用 MySQL，可切换 etcd。运行时连接和流状态不写入 Prometheus 高基数标签。

公网模式的既有入口是 HTTP/HTTPS/WSS；启用内嵌 VPN 网关后 Server 额外持有一个公网 UDP 端口（WireGuard 端点，见 [ADR 0002](adr/0002-public-ingress-and-embedded-vpn.md)，数据面在 `-tags vpn` 构建里）。tp-* 代理入口同样不新增公网监听端口，与既有 443 由 SNI 分流；OpenResty 与 Lua 只搬字节，不含任何授权逻辑，唯一策略执行点是 Server。Nginx 配置见 [Nginx 推荐配置](../deployment/nginx.md)，代理入口的模板渲染与上线步骤见 [OpenResty tp-* 代理入口](../deployment/openresty-proxy-entry.md)。

## 内嵌 VPN 网关

用户用原生 WireGuard 客户端导入后台签发的 peer 配置，无需安装 `tunnelmesh-client`。
决策背景与被解除的旧约束见 [ADR 0002](adr/0002-public-ingress-and-embedded-vpn.md)，
设计定义见[设计规格](../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md)，
技术前提由 [Task 0 可行性报告](../../test/spike/vpn-task0/REPORT.md)实测（Task 1-4 全 PASS）。

数据面在 `//go:build vpn` 后面：不带该 tag 的二进制只有管理面，且在该 tag 缺失而
`server.vpn.enabled: true` 时**启动失败**，不会静默不工作。部署变体与体积实测见
[VPN 网关部署](../deployment/vpn-gateway.md)，容量、租约与 `error_class` 对照见
[VPN 网关运维](../operations/vpn.md)。

```text
原生 WireGuard 客户端（用户机器，无需安装 tunnelmesh-client）
  └─ 加密 UDP → Server 公网 IP:server.vpn.listen（独立端口，不经 Nginx/OpenResty）
       └─ tunnelmesh-server 进程内 VPN 网关
            · vpnBind：只绑 listen 写明的 host（上游默认绑 ":"+port，即所有接口），仅 IPv4
            · wireguard-go device：Noise 握手、密钥、重放保护，按 peer 的 AllowedIPs 过滤源地址
            · 内存态 tun.Device ←→ gVisor netstack（无 /dev/net/tun，无 CAP_NET_ADMIN）
            · handlePacket 逐包决策，顺序固定：
                 1. 解析 IPv4 与传输头，失败即计数丢弃
                 2. 非首片分片 → fragment_dropped（不重组；先于 peer 解析，因为非首片没有传输头）
                 3. 按源 IP 反查 peer → peer_unknown / peer_revoked / peer_expired
                 4. 令牌桶限速 → rate_limited
                 5. 目标是网关自己的 VPN 地址 → target_denied
                 6. entry.Policy.Allow（每包执行，不缓存到流上）→ target_denied / metadata_denied / port_denied
                 7. 按 IP 协议分派，不在 TCP/UDP/ICMP-echo 白名单内 → protocol_unsupported
            ├─ TCP：流表按四元组去重 → 同步注册监听器（AddProtocolAddress + gonet.ListenTCP + accept 泵）
            │        → 然后才 InjectInbound，所以首个 SYN 必然命中 demux，用户不会白等一次客户端 RTO
            │        → netstack 终结 → relay.NodeTransport.OpenStream(protocol="tcp")
            │        → Agent Dialer.DialTCP → 内网目标
            ├─ UDP：流表按 (peer, 源, 目标) 建 association → OpenStream(protocol="udp") → Agent Dialer.DialUDP
            │        回程数据报的 IPv4 头与 UDP 校验和由网关自己组装，经 device.emit 直接上线，不经 netstack 路由
            └─ ICMP echo：仅 echo request 被中继（网关承诺绝不发出不是自己构造的 ICMP）
                     → 流表按 (identifier, sequence) 去重，占用 icmp_max_concurrent 名额
                     → OpenStream(protocol="icmp-echo") → Agent 非特权 ping socket → 内网目标
                     → 回程 reply 由网关还原 peer 的 identifier/sequence 并重算校验和
            · 拒绝路径：error_class 计数 + 按 (peer, reason, 目标 /24) 聚合，窗口过后落 vpn_packet_denied 审计；
              一律静默，不回 RST、不回 ICMP 错误——回一个不可达就等于承认该地址存在
            · 后台循环：idle reaper（idle_timeout）、子网租约续租（TTL 1 分钟，间隔 TTL/3，epoch fencing）
            · 退出：shutdown_timeout 内 drain，超时强制关闭在途流并报告 in_flight_at_timeout 与 flows_forced
```

三条腿都经 `relay.NodeTransport.OpenStream`，所以集群模式下出口 Agent 挂在别的 Server 节点上时
自动走既有 relay，不需要额外接线。目标访问控制逐包在 Server 侧执行并包装既有 `routing.Policy`，
Agent 不新增特权、不维护防火墙规则、不改内网路由，因此内网侧看到的源地址是 Agent 宿主机地址
（能力边界见规格第 4.3 节与 [VPN 使用者帮助](../user-guide/vpn.md#能力边界)）。
