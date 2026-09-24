# TunnelMesh Nginx 推荐配置

本文适用于 Nginx 作为公网 HTTPS/WSS 入口，TunnelMesh Server 运行在本机 `127.0.0.1:8080`。公网只开放 80/443；Agent、Client 和 SSH over WebSocket 都通过 HTTPS/WSS 复用入口。

管理后台前端有两种部署方式：默认由 Server 内嵌 `internal/server/web_dist` 提供，
或由 Nginx 独立托管 `web/dist`。独立静态文件的构建、发布和 `try_files` 配置见
[管理后台前端构建与部署](frontend.md)；下面的配置适用于 Server 内嵌模式，
也可作为独立静态模式的 API/WSS 反代部分。

## 推荐配置

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

# 速率与并发限制分三层，缺一层都会留下可被单点耗尽的路径：
#   1) /api/ 请求速率（认证与数据库开销最大）；
#   2) WebSocket 握手速率（每次握手都要过一次 token 校验并注册会话，
#      只有 limit_conn 挡不住"高频建连即断连"的抖动型消耗）；
#   3) 每来源与全局并发连接数（挡住慢速占位）。
# zone 大小按状态条目数估算：10m 约 16 万个 IP 条目，正常出口 NAT 场景足够。
limit_req_zone $binary_remote_addr zone=tunnelmesh_api:10m rate=20r/s;
limit_req_zone $binary_remote_addr zone=tunnelmesh_ws_handshake:10m rate=5r/s;
limit_req_zone $binary_remote_addr zone=tunnelmesh_page:10m rate=50r/s;
limit_req_zone $binary_remote_addr zone=tunnelmesh_health:10m rate=10r/s;
# tunnelmesh_ws 只统计 WebSocket 长连接，tunnelmesh_conn 统计普通 HTTP 并发，
# 两个 zone 分开，才能分别调参而不互相牵连（例如放大后台并发却收紧 SSH 会话）。
limit_conn_zone $binary_remote_addr zone=tunnelmesh_ws:10m;
limit_conn_zone $binary_remote_addr zone=tunnelmesh_conn:10m;
# 全局并发上限用 $server_name 作 key，即整个 server 块一个计数器：
# 它保护的是上游连接池与 Server 进程，而不是某个来源。
limit_conn_zone $server_name zone=tunnelmesh_ws_total:1m;

# 429 而不是默认 503：503 会被 LB 与 nginx 自身当成"上游不健康"从而重试/摘除节点，
# 而限流是容量策略，不该触发摘除。
limit_req_status 429;
limit_conn_status 429;

upstream tunnelmesh_server {
    server 127.0.0.1:8080;
    keepalive 32;
    # 必须不大于 server.http.idle_timeout（默认 120s），否则 Nginx 会复用一条
    # Server 已经关闭的 keep-alive 连接，表现为间歇性 502。
    keepalive_timeout 60s;
}

server {
    listen 80;
    server_name tunnel.example.com
        "~^tm-[a-z0-9](?:[a-z0-9-]{0,58}[a-z0-9])?\.tunnel\.example\.com$"
        "~^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?-(?:[0-9]{1,3}-){4}[0-9]{1,5}\.apps\.example\.com$";
    return 308 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    server_name tunnel.example.com
        "~^tm-[a-z0-9](?:[a-z0-9-]{0,58}[a-z0-9])?\.tunnel\.example\.com$"
        "~^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?-(?:[0-9]{1,3}-){4}[0-9]{1,5}\.apps\.example\.com$";

    ssl_certificate     /etc/letsencrypt/live/tunnel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/tunnel.example.com/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:TLS:10m;

    client_max_body_size 2m;
    proxy_read_timeout 180s;
    proxy_send_timeout 180s;
    proxy_connect_timeout 5s;

    # API must precede SPA fallback.
    location ^~ /api/ {
        limit_req zone=tunnelmesh_api burst=40 nodelay;
        limit_conn tunnelmesh_conn 32;
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Authorization $http_authorization;
    }

    # Agent and Client authenticated WebSockets.
    location = /ws/agent {
        limit_req zone=tunnelmesh_ws_handshake burst=10 nodelay;
        limit_conn tunnelmesh_ws 100;
        limit_conn tunnelmesh_ws_total 2000;
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header Origin $http_origin;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_buffering off;
        proxy_read_timeout 180s;
        proxy_send_timeout 180s;
    }

    location = /ws/client {
        limit_req zone=tunnelmesh_ws_handshake burst=10 nodelay;
        limit_conn tunnelmesh_ws 100;
        limit_conn tunnelmesh_ws_total 2000;
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header Origin $http_origin;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_buffering off;
        proxy_read_timeout 180s;
        proxy_send_timeout 180s;
    }

    # Browser WebSSH/SFTP one-time ticket bridge. The ticket is in the query
    # string for this request only; do not log query strings in access logs.
    location ^~ /ws/webssh/ {
        limit_req zone=tunnelmesh_ws_handshake burst=5 nodelay;
        limit_conn tunnelmesh_ws 20;
        limit_conn tunnelmesh_ws_total 2000;
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header Origin $http_origin;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }

    # SSH/websocat binary bridge. Keep exact path if it is enabled.
    location = /ws/tcp {
        limit_req zone=tunnelmesh_ws_handshake burst=10 nodelay;
        limit_conn tunnelmesh_ws 100;
        limit_conn tunnelmesh_ws_total 2000;
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header Origin $http_origin;
        proxy_set_header Authorization $http_authorization;
        proxy_buffering off;
        proxy_read_timeout 180s;
        proxy_send_timeout 180s;
    }

    # 健康检查必须能被外部监控与 LB 访问，因此只给一个宽松的速率兜底：
    # 10r/s 足够任何探测频率，同时挡住把健康检查当放大器的脚本。
    location = /health/live {
        limit_req zone=tunnelmesh_health burst=20 nodelay;
        proxy_pass http://tunnelmesh_server;
        proxy_set_header Host $host;
    }

    location = /health/ready {
        limit_req zone=tunnelmesh_health burst=20 nodelay;
        proxy_pass http://tunnelmesh_server;
        proxy_set_header Host $host;
    }

    # Prefer binding /metrics to an internal management address. If it must
    # pass through Nginx, protect it with network ACL or mTLS/basic auth.
    location = /metrics {
        # Prometheus 抓取会被 429 打断，因此兜底限流的速率要覆盖抓取间隔；
        # 更推荐直接放行内网管理地址（见"关键约束"）。
        limit_req zone=tunnelmesh_health burst=20 nodelay;
        allow 10.0.0.0/8;
        allow 192.168.0.0/16;
        deny all;
        proxy_pass http://tunnelmesh_server;
        proxy_set_header Host $host;
    }

    # Catch-all reverse proxy, and the one block that must never be deleted.
    # The Server serves the SPA history fallback, but every frontend path still
    # has to reach it: without this block `/` falls through to the Nginx or
    # OpenResty default site and renders its welcome page. It is the shortest
    # prefix, so the /api/ and /ws/* locations above still win.
    location / {
        # 管理后台首屏、静态资源与 SPA history fallback 都在这个兜底 location 里，
        # 且全部免认证：没有限流时它是唯一可被无凭据打满的路径。
        # 50r/s + burst 200 覆盖一次首屏并发拉取，又不足以支撑扫描器。
        limit_req zone=tunnelmesh_page burst=200 nodelay;
        limit_conn tunnelmesh_conn 64;
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
    }
}
```

## tp-* HTTP 代理入口（OpenResty）

托管路由的 `http-proxy` 类型提供的是**正向代理入口**（`https://tp-<name>.<domain_suffix>`），不是
反向代理。它与本文上面的 server 块**共用 443**，由 SNI 分流：既有块匹配 admin 与 `tm-*` 域名，
tp-* 块用正则 `server_name` 匹配 `tp-<name>.<domain_suffix>`，两块互不影响。

本文不重复模板内容。模板、占位符、渲染示例、容量评估、reload 影响与回滚步骤见
[OpenResty tp-* 代理入口](openresty-proxy-entry.md)，产物在
[`deploy/openresty/`](../../deploy/openresty/README.md)。

两点前提必须注意：

- **必须是 OpenResty**，纯 nginx 做不到：CONNECT 跳过 location 匹配，只有打过
  `ngx_http_proxy_connect_module` 补丁的内核才会注册 `$connect_host`/`$connect_port` 并允许
  server 级 `access_by_lua_file` 接管；`lua-nginx-module` 的 `ngx.req.socket(true)` 才能做双向
  splice。上线前用 `nginx -V` 加 `deploy/openresty/spike-connect-check.sh` 做两级检查。
- **tp-* server 块必须省略 `http2`**：`ngx.req.socket(true)` 在 HTTP/2 下游不可用，proxy_connect
  模块的 Known Issues 也明确不支持 HTTP/2 的 CONNECT。既有 admin 块用的是 per-server 的
  `http2 on;`，两块各自独立，因此新块省略即可，不需要改动既有块（要求内核 nginx >= 1.25.1）。

## 限流与容量取值

限流值不是玄学，下面的数字与本文其余配置以及 Server 自身的约束是一对一的：

| 位置 | 指令 | 依据 |
| --- | --- | --- |
| `/api/` | `limit_req rate=20r/s burst=40` | 每个请求都要过一次 token 校验与数据库访问；40 的突发覆盖控制台一次刷新拉取的多个接口。 |
| `/api/` | `limit_conn tunnelmesh_conn 32` | 单来源并发慢请求的上限，避免一个浏览器扩展占满上游 keepalive 池。 |
| `/ws/agent`、`/ws/client` | `limit_req zone=tunnelmesh_ws_handshake rate=5r/s` | Agent 池重启、网络抖动后的集中重连应当放行；持续高于 5 次/秒的握手只可能是脚本。 |
| `/ws/agent`、`/ws/client` | `limit_conn tunnelmesh_ws 100` | 单来源可能承载多个 NAT 后的 Agent；`server.agents.max_connections_per_agent`（默认 64）是**每个 Agent 身份**的上限，两者维度不同，不能相互替代。 |
| `/ws/webssh/` | `limit_conn tunnelmesh_ws 20`、握手 `burst=5` | 会话可长达 `server.webssh.session_ttl`，占位成本最高；单来源并发另受 `server.webssh.max_active_sessions_per_user` 约束。 |
| `location /` | `limit_req rate=50r/s burst=200` | 免认证的静态资源与 SPA fallback，是扫描器默认命中的路径。 |
| 全局 | `limit_conn tunnelmesh_ws_total 2000` | 保护上游与 Server 进程；必须小于 `worker_processes * worker_connections / 2`，且不小于 `limit_conn tunnelmesh_ws` 乘以预期活跃来源数。 |

三处必须同时看的约束：

- `proxy_read_timeout` 与 `server.http.idle_timeout`：前者 180s 覆盖 30s 心跳的三个周期，后者是 Server 侧 keep-alive 空闲上限（默认 120s）。要让 Nginx 主动回收空闲上游连接，就把 `keepalive_timeout tunnelmesh_server`（upstream 内）调到不大于 `server.http.idle_timeout`，否则 Nginx 会复用一条 Server 已经关闭的连接并回 502。
- `server.http.max_connections`（默认 0 表示不限制）：Nginx 在前时通常保持 0，容量由 `limit_conn` 决定；**Server 直接暴露公网**（无 Nginx、或 `location /` 直连）时必须设置一个与进程容量匹配的正数，否则只有 `ReadHeaderTimeout`/`MaxHeaderBytes` 这一层保护。超限的新连接会被立即关闭（表现为 502/连接重置），不会排队。
- 429 的处理：`limit_req`/`limit_conn` 命中后返回 429，不写上游、不计入 `proxy_next_upstream`。Agent 与 Client 的连接池按指数退避重试，因此偶发 429 只延长重连时间；控制台收到 429 会提示重新登录，若持续出现应当先核对上表而不是先放大 `burst`。

## 关键约束

- `proxy_http_version 1.1`、`Upgrade`、`Connection`、`proxy_buffering off` 是 WebSocket 必需项。
- 30 秒心跳下，`proxy_read_timeout` 建议至少 180 秒，避免短暂网络抖动被 Nginx 提前断开。
- 必须透传 `Authorization` 和 `Origin`；不要把 token 放到 URL query 或 Cookie。
- `/api/`、`/ws/agent`、`/ws/client`、`/ws/webssh/`、`/ws/tcp` 使用精确或 `^~` location，优先于 SPA fallback。
- 每个 `location` 都必须同时有速率（`limit_req`）和并发（`limit_conn`）两类限制：只有限速挡不住慢速长连接占位，只有限并发挡不住高频握手；`location /` 兜底也不例外。
- `limit_conn` 不会跨 location 累加，同一 location 内多条 `limit_conn` 才会同时生效；因此 WebSocket location 里 `tunnelmesh_ws`（单来源）与 `tunnelmesh_ws_total`（全局）要一起写，不能指望从 server 块继承。
- 管理后台的前端路由不需要在 Nginx 配置 `try_files` 或 rewrite：SPA history fallback 由 Server 内嵌静态服务完成，`location /` 只需 `proxy_pass` 到 Server。本文件中的 `/api/` 与 `/ws/*` location 只服务于反代优先级和 WebSocket 升级，与前端路由无关。仅当改用“Nginx 独立静态文件模式”直接托管 `web/dist` 时，才需要 `try_files $uri $uri/ /index.html;` 与 `application/wasm` 类型补充，见 [前端构建与发布](frontend.md)。
- 但 `location /` 兜底反代本身**不能删**：它不是前端路由配置，而是 `/` 与所有前端路径到达 Server 的唯一入口。删掉后请求落到 Nginx/OpenResty 默认 root 的 `index.html`，表现为 “Welcome to OpenResty!” 欢迎页，而 `/api/` 接口可能依旧正常——这是该误删最典型的识别特征。
- WebSSH/SFTP 会话可能持续数小时；`/ws/webssh/` 的读写超时建议不低于 `server.webssh.session_ttl`，并保持双向 `proxy_buffering off`。若缩短超时，需同步评估 SSH keepalive 和 `server.webssh.idle_timeout`。
- 静态资源默认由 Server 透传，Server 已固定 `.wasm` 的 `Content-Type: application/wasm`。若改为 Nginx 直接托管 `web/dist`，必须在 `types` 中补充 `application/wasm wasm;`，否则浏览器会拒绝 WASM 流式编译并回退到更慢的 ArrayBuffer 实例化。
- `/metrics` 不建议暴露公网；优先让 Prometheus 访问 Server 内网管理地址。
- 显式泛域名只允许单层 `tm-<name>.tunnel.example.com`；动态域名使用 `<agent>-<a>-<b>-<c>-<d>-<port>.<server.dynamic_suffix>`。Nginx 正则中的后缀必须与 `server.dynamic_suffix`、wildcard DNS 和证书一致；正则只做域名形状筛选，IP 八位组范围、端口范围、危险地址和 Agent 策略由 Server 再次校验。
- 泛域名只解决 HTTP/HTTPS/WSS 路由；VPN 网关的公网 UDP 端口不经 Nginx，需在云安全组与主机防火墙上单独放行（见 [ADR 0002](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)，仅 `-tags vpn` 构建提供）。
- tp-* server 块禁止 `http2`：`ngx.req.socket(true)` 在 HTTP/2 下游不可用，proxy_connect 模块的 Known Issues 也明确不支持 HTTP/2 的 CONNECT。
- tp-* 的 `location /` 必须显式写 `proxy_set_header Proxy-Authorization $http_proxy_authorization;`：它是 hop-by-hop 头，Nginx 默认不转发给上游，漏掉这一行会让所有非 CONNECT 的代理请求返回 407。
- 不能用 `proxy_connect;` + `proxy_pass` 做链式转发：模块 README 明确 “Any `location {}` block, `upstream {}` block and any other standard backend/upstream directives, such as `proxy_pass`, do not impact the functionality of this module.”，模块会自己直连目标，路由身份与 `Proxy-Authorization` 全部丢失。本项目的做法是**不启用** `proxy_connect;` 指令，只用补丁提供的 `$connect_host`/`$connect_port` 变量与 server 级 `access_by_lua_file`，把 CONNECT 原样搬到 Server 的内部入口（`server.proxy_entry.listen`），策略全部由 Server 执行。
- 修改配置后先执行 `nginx -t`，再 reload；证书轮换必须验证 WSS 和 API 登录。

## 验证命令

```bash
nginx -t
curl -fsS https://tunnel.example.com/health/live
curl -fsS https://tunnel.example.com/health/ready
curl -fsS https://tunnel.example.com/metrics
websocat --binary -H='Authorization: Bearer <client-service-token>' \
  wss://tunnel.example.com/ws/client

# 限流确实生效（超过 burst 后应出现 429，而不是 503）：
for i in $(seq 1 60); do
  curl -s -o /dev/null -w '%{http_code}\n' https://tunnel.example.com/health/live
done | sort | uniq -c
# 命中限流时 nginx 会记 limit_req/limit_conn 日志，用于区分"Nginx 挡掉"与"Server 拒绝"：
tail -n 50 /var/log/nginx/error.log | grep -E 'limit_req|limit_conn' || echo "no limit hits yet"
```

不要把真实 token、证书私钥或生产域名凭据写入配置仓库。
