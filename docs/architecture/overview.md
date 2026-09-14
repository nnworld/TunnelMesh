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

公网模式只需要 HTTP/HTTPS/WSS 入口；公网 UDP 不作为 Server 监听能力。tp-* 代理入口同样不新增公网监听端口，与既有 443 由 SNI 分流；OpenResty 与 Lua 只搬字节，不含任何授权逻辑，唯一策略执行点是 Server。Nginx 配置见 [Nginx 推荐配置](../deployment/nginx.md)，代理入口的模板渲染与上线步骤见 [OpenResty tp-* 代理入口](../deployment/openresty-proxy-entry.md)。
