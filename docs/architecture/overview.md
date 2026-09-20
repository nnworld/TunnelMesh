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

公网模式的既有入口是 HTTP/HTTPS/WSS；启用内嵌 VPN 网关后 Server 额外持有一个公网 UDP 端口（WireGuard 端点，见 [ADR 0002](adr/0002-public-ingress-and-embedded-vpn.md)，实施中）。tp-* 代理入口同样不新增公网监听端口，与既有 443 由 SNI 分流；OpenResty 与 Lua 只搬字节，不含任何授权逻辑，唯一策略执行点是 Server。Nginx 配置见 [Nginx 推荐配置](../deployment/nginx.md)，代理入口的模板渲染与上线步骤见 [OpenResty tp-* 代理入口](../deployment/openresty-proxy-entry.md)。

## 内嵌 VPN 网关（已批准，实施中）

当前版本尚未提供。本节描述 [ADR 0002](adr/0002-public-ingress-and-embedded-vpn.md) 批准、
[设计规格](../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md)定义的目标架构；
技术前提已由 [Task 0 可行性报告](../../test/spike/vpn-task0/REPORT.md)实测（Task 1-4 全 PASS）。
用户用原生 WireGuard 客户端导入后台签发的 peer 配置，无需安装 `tunnelmesh-client`：

```text
原生 WireGuard 客户端（用户机器，无需安装 tunnelmesh-client）
  └─ 加密 UDP → Server 公网 IP:server.vpn.listen
       └─ tunnelmesh-server 进程内 VPN 网关（build tag: vpn）
            · wireguard-go device：握手、密钥、重放保护
            · 内存态 tun.Device ←→ gVisor netstack（无 /dev/net/tun，无 CAP_NET_ADMIN）
            · vpn_packet：按源 IP 反查 peer → 策略链 → 分派
                 ├─ TCP/UDP → netstack 终结 → relay.NodeTransport.OpenStream
                 │              （protocol="tcp"|"udp"，集群自动跨节点）
                 │                └─ Agent 现有 Dialer.DialTCP/DialUDP → 内网目标
                 └─ ICMP echo → relay.NodeTransport.OpenStream（protocol="icmp-echo"）
                                  └─ Agent 非特权 ping socket → 内网目标
```

Server 进程内的 VPN 数据面用 `//go:build vpn` 隔离，不打开 `/dev/net/tun`、不要求 `CAP_NET_ADMIN`，
只持有一个公网 UDP 端口（默认 51820）；该端口不经 Nginx/OpenResty，需要在云安全组与主机防火墙单独放行。
目标访问控制逐包在 Server 侧执行并包装既有 `routing.Policy`，Agent 不新增特权、不维护防火墙规则、
不改内网路由，因此内网侧看到的源地址是 Agent 宿主机地址（能力边界见规格第 4.3 节）。
