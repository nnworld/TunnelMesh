# 托管路由 HTTP 代理入口（tp-*）设计

- 日期：2026-09-13
- 状态：设计已确认，待编写实施计划
- 分类：Architectural（brainstorming 路径）
- 关联文档：`docs/deployment/nginx.md`、`docs/operations/configuration.md`、`docs/superpowers/plans/2026-09-09-client-socks5-implementation.md`、`docs/superpowers/specs/2026-09-12-admin-webssh-sftp-design.md`

## 1. 背景与目标

现有 SOCKS5/HTTP 代理能力只存在于 `tunnelmesh-client` 的本地监听：用户必须在本机运行 client 才能使用代理。本次新增“托管路由 HTTP 代理入口”，让用户把 `https://tp-<name>.<domain>:443` 直接配置为浏览器、操作系统或 curl 的 HTTP 代理，无需安装任何客户端；出口节点由管理后台在路由上指定（agent），访问控制由 Server 统一执行。

目标：

1. 托管路由新增类型 `http-proxy`，域名前缀 `tp-`，可选择出口 agent。
2. 两种安全方式：用户名/密码（HTTP Basic）与无认证。
3. 源 IP ACL：多条 IP/CIDR，默认拒绝，`0.0.0.0/0` 或 `::/0` 放开全部。
4. 入口复用现有 443 与 OpenResty，不新增公网端口。
5. 策略（路由解析、ACL、认证、目标校验、限额、审计、指标）全部在 Server 内实现并可被 Go 测试覆盖；OpenResty 只搬运字节。
6. 集群模式下出口 agent 挂在其它节点时，自动经既有 relay 转发。

非目标：

- 不支持 SOCKS5（本轮明确只做 HTTP 代理）。
- 不实现 Server 侧 TLS 终止（作为 A2 回退保留接口，本轮不交付）。
- 不支持公网 UDP 入口。
- 不做单路由多出口负载与故障转移（一路由一 agent；池化留待后续）。
- 不在 nginx/Lua 中放置任何策略逻辑（不查库、不判 ACL、不校验密码）。

## 2. 前置事实与硬约束（已核实）

| 事实 | 证据 | 影响 |
|---|---|---|
| proxy_connect 补丁对 CONNECT 跳过 location 匹配 | `patch/proxy_connect.patch` 中 `ngx_http_core_find_config_phase` 分支：`ngx_http_update_location_config(r); r->phase_handler++;` | `content_by_lua_block`（context 仅 `location, location-if`）接不到 CONNECT，必须使用 server 级 `access_by_lua_block` |
| `$connect_host` / `$connect_port` 是 nginx core 变量 | 补丁修改 `src/http/ngx_http_variables.c` 的 `ngx_http_core_variables[]` | Lua 无需在 location 中写 `proxy_connect;` 即可读到隧道目标 |
| `ngx.req.socket` 的 context 含 `access_by_lua*`，`raw=true` 返回全双工 socket | lua-nginx-module README `ngx.req.socket` 章节 | access 阶段即可完成 raw splice |
| `ngx.req.socket` 在 HTTP/2 下游不可用 | 同上 README 的 SPDY/HTTP2 限制说明 | tp-* server 块禁止 `http2 on`，ALPN 只提供 http/1.1 |
| proxy_connect 模块不会把 CONNECT 转发给上游 | 模块 README：“Any `location {}` block, `upstream {}` block and any other standard backend/upstream directives, such as `proxy_pass`, do not impact the functionality of this module.” | 不能依赖该模块做链式转发；本设计不使用它 |
| nginx 不向上游转发 hop-by-hop 头（含 `Proxy-Authorization`） | nginx 上游请求构造行为，Task 0 实测确认 | 非 CONNECT 分支必须显式 `proxy_set_header Proxy-Authorization $http_proxy_authorization;` |

## 3. 架构总览

```
浏览器/系统代理   https://tp-<name>.<domain_suffix>:443
      |  TLS，SNI = tp-<name>.<domain_suffix>，ALPN 只协商 http/1.1
      v
OpenResty  server{ listen 443 ssl; server_name ~^tp-...; access_by_lua_file ... }
      |  CONNECT   -> ngx.req.socket(true) 取下游 raw socket
      |               ngx.socket.tcp() -> 127.0.0.1:8089，重建 CONNECT 请求
      |               注入 X-TunnelMesh-Route / -Client-IP / -Client-Port
      |               上游回 200 -> 下游回 "200 Connection Established" -> 双向 splice
      |  非 CONNECT -> location / { proxy_pass http://tunnelmesh_proxy_entry; }
      v
tunnelmesh-server  proxy_entry（明文内部监听，仅回环/内网）
      trusted_proxies 校验 -> 路由解析(tp-*) -> 源 IP ACL -> Basic 认证
      -> 目标策略校验 -> relay.NodeTransport.OpenStream（集群自动跨节点）
      v
tunnelmesh-agent   出口拨号真实目标，并再次执行 SSRF/私网/端口校验
```

分层职责：

- OpenResty：TLS 终止、按 SNI 选 server 块、把 CONNECT 原样搬到 Server 内部端口并双向 splice，注入两个可信头。不含任何策略。
- Server proxy entry：唯一策略执行点（身份、ACL、认证、目标校验、限额、审计、指标）。
- `internal/session` / `internal/relay`：跨节点流转发（既有能力）。
- `tunnelmesh-agent`：真实出口拨号与目标二次校验（既有能力）。

## 4. 入口形态与域名

- 代理地址：`https://tp-<name>.<domain_suffix>:443`。`<name>` 允许 `[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?`，统一小写。
- `domain_suffix` 来自新配置 `server.proxy_entry.domain_suffix`，与 `server.dynamic_suffix` 解耦，必须与泛解析 DNS 和通配证书一致（单层 `tp-<name>`，`*. <domain_suffix>` 证书可覆盖）。
- 存储的 `tunnels.domain` 为完整主机名 `tp-<name>.<domain_suffix>`，`path_prefix` 恒为 `"/"`（哨兵，无语义）。一条 tp-* 域名只能对应一条路由，冲突检查沿用既有 `tunnelRouteConflict` 但按**域名整体**判定（`domainOnly`），不比较 `path_prefix`：数据库的 `UNIQUE(domain, path_prefix)` 只在两行前缀也相同时才拦得住，而 tp-* 域名与同域名的路径级反代路由（例如 `pathPrefix=/admin`）共存时前缀不同，约束不会触发，可 nginx 的精确 `server_name` 会优先于 tp-* 的正则 `server_name`，那条 proxy 路由将永远不可达。大小写变体（`TP-Demo` 与 `tp-demo`）视为同一条，因为 SNI 与 Host 都不区分大小写。创建与更新（改域名）两条路径都受此约束，冲突返回 409 `route already exists`。
- 客户端必须使用 `https://` 代理方案（TLS 直连代理）。原因：路由身份来自 SNI，且 Basic 凭据必须加密传输。macOS/Windows 系统代理的“安全 Web 代理 (HTTPS)”、Chrome PAC 的 `HTTPS host:port`、`curl --proxy https://...` 均支持；用户文档给出 PAC 与 curl 示例，并明确说明只能填 `http://` 代理地址的旧客户端无法使用本入口。

## 5. OpenResty 侧设计

### 5.1 server 块（产物 `deploy/openresty/tunnelmesh-proxy.conf.example`）

```nginx
upstream tunnelmesh_proxy_entry { server 127.0.0.1:8089; keepalive 32; }

server {
    listen 443 ssl;                 # 不得加 http2
    server_name ~^tp-[a-z0-9-]+\.example\.com$;
    ssl_certificate     /data/ssl/example.com_bundle.crt;   # 必须覆盖 *.example.com
    ssl_certificate_key /data/ssl/example.com.key;
    ssl_protocols TLSv1.2 TLSv1.3;

    allow 10.0.0.0/8;               # 粗粒度前置，细粒度 ACL 由 Server 执行
    allow 11.0.0.0/8;
    deny  all;

    lua_check_client_abort on;
    access_by_lua_file /etc/openresty/lua/tunnelmesh_proxy_entry.lua;

    location / {                    # 非 CONNECT：绝对形式 http:// 目标
        proxy_pass http://tunnelmesh_proxy_entry;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $http_host;
        proxy_set_header Proxy-Authorization $http_proxy_authorization;
        proxy_set_header X-TunnelMesh-Route $ssl_server_name;
        proxy_set_header X-TunnelMesh-Client-IP $remote_addr;
        proxy_set_header X-TunnelMesh-Client-Port $remote_port;
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }
}
```

现有 admin/托管路由 server 块保持不变；两块共用 443，由 SNI 选择。现有块使用 per-server 的 `http2 on;`，新块省略即互不影响（要求 OpenResty 基于 nginx >= 1.25.1，Task 0 验证）。

### 5.2 Lua 处理流程（产物 `deploy/openresty/tunnelmesh_proxy_entry.lua`）

1. `ngx.req.get_method() ~= "CONNECT"` 时立即 `return`，交给 `location /`。
2. 读取 `ngx.var.connect_host`、`ngx.var.connect_port`、`ngx.var.ssl_server_name`、`ngx.var.remote_addr`、`ngx.var.remote_port`；任一为空则 `ngx.log(WARN)` 后 `ngx.exit(403)`。
3. `ngx.socket.tcp()` 连接内部入口（地址、端口、超时取自文件顶部常量表，部署时按环境修改），并显式 `sock:settimeouts(connect, send, read)`。
4. 重建请求，只写白名单头，绝不整体透传客户端头：

   ```
   CONNECT <host>:<port> HTTP/1.1
   Host: <host>:<port>
   Proxy-Authorization: <客户端提供时原样透传>
   X-TunnelMesh-Route: <ssl_server_name>
   X-TunnelMesh-Client-IP: <remote_addr>
   X-TunnelMesh-Client-Port: <remote_port>
   ```

5. 读上游状态行：`200` 则取 `ngx.req.socket(true)`，写 `HTTP/1.1 200 Connection Established\r\n\r\n`；非 `200` 则把上游状态行、响应头与 body（上限 8KB）原样回写下游，随后 `ngx.exit(444)`。
6. splice：双向 `receiveany(65536)` + `send`，任一侧 EOF、超时或错误即结束；结束时 `ngx.log(INFO)` 输出 route、target、client IP、duration、上下行字节，不输出任何凭据。
7. 收尾：关闭上游 socket，`return ngx.exit(444)`（nginx 立即关闭连接，不再补发响应）。

约束：

- Lua 不做任何策略判断、不缓存路由、不解析密码。
- 所有超时显式设置（cosocket 默认 60s 会掐断长隧道）。读超时设为 Server `idle_timeout` + 30s，保证由 Server 先关闭连接。
- 每条隧道占用 1 个 nginx 请求与 2 个 socket；`worker_connections` 与 `worker_rlimit_nofile` 按并发隧道数 x2 评估，写入部署文档。
- `nginx -s reload` 会终止在途隧道；要求配置 `worker_shutdown_timeout 300s;`，并在运维文档说明发布窗口影响。

## 6. Server 侧设计

### 6.1 监听与前置校验（`internal/server/proxy_entry_listener.go`）

- `server.proxy_entry.enabled=true` 时，在既有 runtime 生命周期内启动第三个 listener（与 relay listener 同一模式），随 runtime 优雅退出。
- 明文 HTTP/1.1；`ReadHeaderTimeout=10s`；请求头受 `max_header_bytes` 限制。
- Accept 后立即校验 peer IP 是否属于 `trusted_proxies`；不匹配直接 `Close()`（不读请求、不回响应），并计入 `tunnelmesh_proxy_entry_requests_total{result="untrusted_peer"}`。
- 默认 `listen=127.0.0.1:8089`、`trusted_proxies=["127.0.0.1/32","::1/128"]`。跨机部署必须显式配置内网地址与白名单，文档标注“不推荐，仅在无法同机时使用”。

### 6.2 路由身份（`internal/proxyentry/identity.go`）

```go
// RouteIdentity abstracts how a proxy request is mapped to a tp-* managed
// route so the same policy core can serve a header-based ingress (OpenResty)
// and a future SNI-based ingress (native TLS listener).
type RouteIdentity interface {
    Resolve(r *http.Request) (RouteKey, error)
}
```

- `RouteKey` 是规范化后的路由主机名（小写完整 `tp-<name>.<domain_suffix>`），由 identity 实现产出，供 7.4 的 proxy 路由快照查询使用；本设计不引入其它未定义类型。
- `HeaderRouteIdentity`（本轮实现）：读 `route_header`，接受完整主机名或裸 `<name>`，去 `tp-` 前缀、转小写、校验 `domain_suffix` 一致。
- `SNIRouteIdentity`（预留，A2 回退用）：从 TLS `ClientHelloInfo.ServerName` 解析；本轮只提供接口与测试桩，不接 TLS 监听。
- 头缺失、非法或后缀不匹配返回 `ErrRouteIdentityMissing` / `ErrRouteIdentityInvalid`，统一 403，且绝不回退到 peer IP 或 `Host`。

### 6.3 客户端 IP

只信任 `client_ip_header`，且仅当 peer 属于 `trusted_proxies`（6.1 已保证）。解析失败或缺失即 403（fail-closed），绝不使用 peer IP 参与 ACL 判定。`client_port_header` 仅用于日志与指标。

### 6.4 源 IP ACL（`internal/proxyentry/acl.go`）

- 输入 `sourceCIDRs []string`，支持 IPv4/IPv6；单 IP 自动补 `/32` 或 `/128`；`0.0.0.0/0` 与 `::/0` 表示放开全部。
- 非法条目在 API 写入时拒绝；运行期若仍遇到非法条目，按“拒绝”处理并计数，保证 fail-closed。
- 空列表等于拒绝所有（默认拒绝语义）。
- 未命中返回 403，计数 `tunnelmesh_proxy_entry_acl_denied_total{route}`，写审计事件 `proxy_route_denied`（reason=`source_acl`，记录 route 与 client IP，不记录目标）。

### 6.5 认证（`internal/proxyentry/auth.go`）

- `authMode=none`：跳过认证，ACL 仍然执行。
- `authMode=basic`：解析 `Proxy-Authorization: Basic`（base64 解码为 `user:pass`，UTF-8）；username 必须与凭据 username 一致；密码以 sha256 摘要配合 `subtle.ConstantTimeCompare` 比对，与 client 侧 `httpProxyCredentialDigest` 的做法一致。
- 凭据来源：`credentials` 表中 `credential_type='proxy_basic'`，密文经既有 secret store（`TUNNELMESH_TOKEN_ENCRYPTION_KEY`，AES-256-GCM）解密。凭据 `enabled=0`、已软删或解密失败一律视为认证失败；仅当 secret store 整体不可用时返回 503（与既有 credential 行为一致）。
- 失败响应 407 + `Proxy-Authenticate: Basic realm="TunnelMesh", charset="UTF-8"`；按 (routeID, clientIP) 计数，连续 `auth_backoff_threshold` 次失败进入 30s 退避，之后每次翻倍，上限 15 分钟；退避期内直接返回 407 且不做密码比对。
- 指标 `tunnelmesh_proxy_entry_auth_failures_total{route,reason}`；失败写审计 `proxy_auth_failed`，成功只计数不写审计（避免高频噪声）。审计与日志均不含密码、不含完整 Authorization/Proxy-Authorization 头。

### 6.6 目标校验与出口

- CONNECT 目标 `host:port`：端口必须在 1..65535；host 为 IP 字面量时立即用 `routing.Policy.Validate` 校验（危险地址恒拒 + 路由级 `targetCIDRs`/`targetPorts`）；host 为域名时 Server 不解析，由 agent 解析后执行二次校验（与既有 client 转发行为一致）。
- `allowPrivateTargets=false` 时额外拒绝 RFC1918、ULA、回环与链路本地目标（IP 字面量在 Server 拒绝，域名由 agent 拒绝）。默认 `true`，因为出口在 agent，访问其内网服务正是本功能的核心价值。
- 开流复用 `relay.NodeTransport.OpenStream(ctx, relay.StreamRequest{...})`：`AgentID` 取自路由；CONNECT 使用 `Protocol: "tcp"`，与 `internal/client/socks5_forward.go:250` 的裸隧道语义一致；绝对形式使用 `Protocol: "http"`，转发方式与 `internal/server/http_proxy.go` 的 `ServeRoute` 一致（`upstreamReq.Write(stream)` + `http.ReadResponse(bufio.NewReader(stream), upstreamReq)` + `copyResponse`；已核实 `ServeRoute` 不使用 `http.Client`，WebSocket 升级走 `handleUpgrade` 的 `Hijack()` + `io.Copy`，`streamNetConn` 是无构造点的预留适配器，两条分支都不经过它，本设计不引用它）。集群模式下 agent 挂在其它节点时由既有 relay 路径转发，无需新增逻辑。
- CONNECT 成功后 hijack 下游连接，先写 `HTTP/1.1 200 Connection Established\r\n\r\n`，把 hijack 缓冲中已预读的字节先转发（复用 `internal/server/http_proxy.go` 的既有做法），再双向 `io.Copy`，受 flow-control 窗口与 idle 超时约束。
- 绝对形式（非 CONNECT）请求把 `Host` 当目标，规范化为 origin-form 后经同一条 stream 转发，响应流式回写（等价于 client 侧 `normalizeProxyRequest`）。

### 6.7 限额与生命周期

- `max_concurrent_tunnels`（全局）与 `config.maxConcurrentTunnels`（每路由，0 表示不限）；超限返回 503 + `Retry-After`。
- `connect_timeout`（默认 10s，覆盖开流与目标拨号）、`idle_timeout`（默认 300s，双向均无数据即关闭）。
- 必须覆盖半关闭、EOF、重复 stream ID、窗口耗尽、GOAWAY/drain、agent 掉线（在途隧道收到稳定错误并记录 `error_class`）。
- 优雅退出顺序：停止 accept -> 对在途隧道发 drain -> 等待 `shutdown_timeout` -> 强制关闭。nginx 侧表现为 EOF，客户端重连即可恢复。

## 7. 数据模型（无 DDL 变更，Schema 保持 v13）

### 7.1 复用 tunnels 表

| 字段 | http-proxy 路由取值 |
|---|---|
| protocol | `http-proxy` |
| domain | `tp-<name>.<domain_suffix>` |
| path_prefix | `/` |
| agent_id | 出口 agent（NOT NULL，语义天然匹配“选择代理节点”） |
| target_host / target_port | 哨兵 `"*"` / `0` |
| public_port | 不使用（0） |
| status | `active` / `disabled` |
| config(JSON) | 见 7.2 |

哨兵原因：`target_host`、`target_port` 均为 NOT NULL，且 `internal/storage` 与 `internal/server/api.go` 现有校验要求 `TargetHost != ""` 且 `TargetPort` 在 1..65535。因此在这两处为 `protocol == "http-proxy"` 增加特例（导出常量 `ProxyTargetWildcard = "*"`），不引入 v14 迁移。

### 7.2 config JSON 新增字段（全部可选，缺省即文档默认值）

```json
{
  "authMode": "none",
  "credentialId": "",
  "sourceCIDRs": ["10.0.0.0/8", "11.71.85.0/24"],
  "targetCIDRs": [],
  "targetPorts": [],
  "allowPrivateTargets": true,
  "maxConcurrentTunnels": 0,
  "description": ""
}
```

校验规则：`authMode=basic` 必须提供 `credentialId`，且该凭据存在、`enabled`、`type=proxy_basic`、属主合法；`sourceCIDRs`/`targetCIDRs` 逐条 `net.ParseCIDR`（单 IP 自动补掩码）；`targetPorts` 在 1..65535 且去重；未知字段拒绝，与既有 config 严格校验风格一致。

### 7.3 credentials 新增类型 proxy_basic

- `credential_type='proxy_basic'`（列为 `VARCHAR(32)`，无需迁移）。
- `public_key` 存 username（非机密，可列表展示）；`fingerprint = sha256(username)`；加密 secret 存 `{"password":"..."}`。
- `internal/storage/credential_repository.go` 的类型 switch 与 `internal/server/credential_service.go` 的校验各增加一个 case；创建与轮换沿用既有加密及 `secret_key_id`/`secret_version` 机制；列表与详情 API 永不返回 password。

### 7.4 路由快照

`loadManagedRoutes` 跳过 `protocol='http-proxy'` 的行，避免 `location /` 的泛域名反代误匹配 tp-*；同一次查询额外构建 proxy 路由索引 `map[lowercaseHost]ProxyRoute` 与 revision，沿用既有 5s TTL 快照与集群缓存策略（集群 MySQL 模式沿用既有更长 TTL 配置）。

## 8. 配置项

新增 `server.proxy_entry`，配置文件、环境变量（`TUNNELMESH_SERVER_PROXY_ENTRY_*`）、命令行（`--server.proxy_entry.*`）三路等价，优先级为命令行 > 环境变量 > 配置文件 > 默认值：

| key | 默认值 | 说明 |
|---|---|---|
| enabled | false | 关闭时不监听、不构建 proxy 路由索引 |
| listen | 127.0.0.1:8089 | 内部明文入口 |
| trusted_proxies | 127.0.0.1/32,::1/128 | peer 白名单，含非法 CIDR 时启动失败 |
| domain_suffix | 空（enabled=true 时必填） | tp-* 域名后缀，需与泛解析 DNS 和通配证书一致 |
| route_header | X-TunnelMesh-Route | 路由身份头 |
| client_ip_header | X-TunnelMesh-Client-IP | 真实客户端 IP 头 |
| client_port_header | X-TunnelMesh-Client-Port | 仅用于日志与指标 |
| connect_timeout | 10s | 开流与目标拨号超时 |
| idle_timeout | 300s | 双向无数据即关闭 |
| shutdown_timeout | 30s | 优雅退出等待在途隧道的时间 |
| max_concurrent_tunnels | 512 | 全局并发隧道上限，0 表示不限 |
| max_header_bytes | 16384 | 请求头上限 |
| auth_backoff_threshold | 5 | 连续失败进入退避的次数 |

启动校验（`internal/config`）：`enabled=true` 时 `listen` 必须是合法 host:port；`trusted_proxies` 至少一条且全部可解析；`domain_suffix` 非空且为合法域名；当 `listen` 绑定非回环地址时，`trusted_proxies` 不得包含 `0.0.0.0/0` 或 `::/0`，否则启动失败并给出明确原因（防止把头伪造面暴露到内网）。

## 9. 管理 API

复用既有 `/api/v1/routes`（GET 列表、POST 创建、PATCH `/routes/{id}`）与 `/api/v1/credentials`：

- `protocol` 允许值增加 `http-proxy`；创建与更新时 `targetHost`/`targetPort` 由服务端强制写哨兵，客户端传入非哨兵值返回 400。
- 请求与响应新增 `authMode`、`credentialId`、`sourceCIDRs`、`targetCIDRs`、`targetPorts`、`allowPrivateTargets`、`maxConcurrentTunnels`、`description`，响应额外返回只读的 `proxyURL`（服务端拼装的完整代理地址）。
- 统一响应 `{code,msg,data}`、cursor 分页、`Idempotency-Key` 重放一致性沿用既有实现；权限校验全部在服务端完成，非管理员不可见也不可修改他人路由（沿用既有 owner 过滤）。
- 凭据 API 的 `type` 允许值增加 `proxy_basic`，请求含 `username` 与 `secret.password`；响应含 `username` 与 `credentialHasSecret`，永不含 password。
- `docs/api/openapi.yaml` 同步：新 protocol 枚举、新字段、`proxy_basic` 类型，以及 400/403/407/503 错误响应示例。

## 10. 管理后台

`web/src/views/Routes.vue`：

- 类型选择新增“HTTP 代理入口 (tp-)”；选中后表单切换为：名称（实时预览完整域名，自动加 `tp-` 前缀）、出口 Agent 选择框（复用既有）、认证模式（无 / 用户名密码）、凭据选择框（列出 `type=proxy_basic` 且 enabled 的凭据，并提供“新建凭据”入口）、源 IP ACL 可增删列表（逐行 CIDR 校验，提供一键填入 `0.0.0.0/0`）、目标范围（可选 CIDR 与端口，允许内网目标开关）、并发上限、备注、状态。
- 列表页新增列：代理地址（可复制）、认证模式、ACL 条数、活跃隧道数；表格沿用既有 `min-width` 与容器内滚动策略，不得出现整页横向滚动条（既有缺陷已修复，需回归验证）。
- 详情抽屉新增“使用说明”：完整代理 URL、macOS/Windows/Chrome PAC/curl 四类配置示例、ACL 与认证说明、错误码对照。
- `web/src/views/Credentials.vue` 与 `web/src/api/credentials.ts`：类型选项增加 `proxy_basic`（表单字段 username 与 password，password 只写不回显）。
- i18n：`web/src/i18n/schema.ts`、`messages/zh-CN.ts`、`messages/en-US.ts` 同步新增键（schema 为强类型，缺键会编译失败）。
- 前端测试覆盖表单校验（CIDR 非法、basic 未选凭据、名称重复）、类型切换时字段重置、列表渲染；执行 `npm test -- --run` 与 `npm run build`（自动镜像到 `internal/server/web_dist`）。

- **实现偏差（Task 13 Step 6 回写）**：列表页不再新增“活跃隧道数”列，改为在“使用说明”抽屉内指向 Grafana 单 Dashboard 的 Row `HTTP Proxy Entry` → 面板 `Proxy tunnels active`，并在抽屉内补一行提示文案（i18n 键 `routes.proxyActiveTunnelsHint`，中英双语）。原因：管理 API 未暴露该聚合，为纯展示新增只读接口需要额外的权限校验与前端轮询，违背 KISS/YAGNI。Row 名与面板名保持英文字面量、中文界面也不翻译，取值以 Task 15 Step 3 写入 Dashboard JSON 的字面量为准，否则用户按中文 Row 名在 Grafana 里找不到对应视图。

## 11. 安全模型（威胁与对策）

| 威胁 | 对策 |
|---|---|
| 内网主机直连 8089 伪造 route 与 client IP | listener 只绑回环；peer 不属于 `trusted_proxies` 时在读请求前断开；启动校验禁止 `0.0.0.0/0` 白名单 |
| 客户端伪造 `X-TunnelMesh-*` 头 | Lua 重建请求时只写白名单头，客户端同名头被丢弃；非 CONNECT 分支由 nginx `proxy_set_header` 覆盖 |
| Basic 凭据明文暴露 | 入口强制 `https://`（TLS 终止于 OpenResty）；内部明文段仅限回环；日志、审计、指标不含密码与完整头 |
| 暴力破解密码 | 常量时间比较 + (route, clientIP) 指数退避 + 失败审计 + 失败指标 |
| 代理被用于访问云元数据或内网横扫 | `routing.IsDangerousAddress` 恒拒；`allowPrivateTargets` 可关闭；路由级 target allowlist；agent 侧二次校验 |
| 单路由耗尽资源 | 全局与每路由并发上限、connect/idle 超时、`max_header_bytes`，nginx 侧 `limit_conn` 与 allow/deny 粗粒度前置 |
| 路由枚举 | 未知路由、停用路由与 ACL 拒绝统一返回 403 且文案一致，仅在日志与指标中区分 reason |
| 凭据泄露 | 复用既有加密存储（AES-256-GCM + key id/version），列表接口永不返回 secret，轮换沿用既有流程 |

## 12. 错误语义

| 场景 | HTTP | 稳定错误码 |
|---|---|---|
| peer 不可信 | 直接关闭连接 | untrusted_peer（仅指标与日志） |
| route 头缺失或非法 | 403 | proxy_route_identity_invalid |
| 路由不存在或已停用 | 403 | proxy_route_unavailable |
| 源 IP 不在 ACL | 403 | proxy_source_denied |
| Basic 缺失 / 错误 / 退避中 | 407 | proxy_auth_required / proxy_auth_failed / proxy_auth_backoff |
| 目标被策略拒绝 | 403 | proxy_target_denied |
| 目标端口非法 | 400 | proxy_target_invalid |
| agent 不可达或无可用连接 | 502 | proxy_egress_unavailable |
| 开流或拨号超时 | 504 | proxy_egress_timeout |
| 并发超限 | 503 + Retry-After | proxy_capacity_exhausted |
| 凭据密文不可解密（密钥缺失） | 503 | credential_secret_unavailable |

CONNECT 场景下上述响应都发生在隧道建立之前，因此是标准 HTTP 响应；隧道建立后的失败表现为连接关闭，并记录结构化日志与指标（无法再回状态码）。

## 13. 可观测性

指标（`internal/observability/metrics.go`，沿用既有命名前缀与标签风格）：

- `tunnelmesh_proxy_entry_requests_total{route,mode,result,error_class}`，mode 取 `connect` 或 `absolute`
- `tunnelmesh_proxy_entry_tunnels_active{route}`
- `tunnelmesh_proxy_entry_tunnel_duration_seconds{route,result}`（histogram）
- `tunnelmesh_proxy_entry_auth_failures_total{route,reason}`
- `tunnelmesh_proxy_entry_acl_denied_total{route}`
- 字节复用 `tunnelmesh_bytes_total{component="proxy_entry",direction,protocol}`；阶段耗时复用 `tunnelmesh_stream_stage_duration_seconds`

审计事件（结构化，沿用既有 action/resource 风格）：`proxy_route_denied`、`proxy_auth_failed`、`proxy_tunnel_opened`、`proxy_tunnel_closed`，以及管理操作 `proxy_route_created|updated|deleted`。字段含 routeID、routeDomain、agentID、clientIP、targetHost、targetPort、bytesUp、bytesDown、duration、errorClass；不含凭据、不含完整请求头、不含响应体。

Trace：入口生成或继承 `traceparent`，随 stream 传播到 agent，使既有 `POST /api/v1/agents/{agentId}/trace`、探针与排障流程能覆盖代理链路。

Grafana：在既有单一 Dashboard 中新增一行“HTTP 代理入口”面板（请求速率与结果分布、活跃隧道、认证失败、ACL 拒绝、隧道时长 P95、上下行字节），沿用既有 datasource 变量与 JSON model 结构，不拆分 Dashboard。

## 14. 测试策略（TDD）

先红后绿，逐项列出预期失败点：

1. `internal/proxyentry/acl_test.go`：IPv4/IPv6 命中与未命中、单 IP 自动补掩码、空列表默认拒绝、`0.0.0.0/0` 与 `::/0` 全放开、非法条目按拒绝处理。
2. `internal/proxyentry/auth_test.go`：正确、错误、缺失、username 大小写、base64 非法、非 UTF-8；常量时间比较；退避阈值与指数上限；凭据 disabled、软删、解密失败；secret store 不可用返回 503。
3. `internal/proxyentry/identity_test.go`：完整域名、裸名、大写、后缀不匹配、头缺失、重复头。
4. `internal/server/proxy_entry_listener_test.go`：不可信 peer 立即关闭且不读请求；`enabled=false` 不监听；优雅退出等待在途隧道，超过 `shutdown_timeout` 后强制关闭。
5. `internal/server/proxy_entry_handler_test.go`：CONNECT 全链路（内存 agent stub）成功建隧与双向字节；绝对形式转发；12 节全部错误码；并发上限；idle 超时；半关闭与 EOF；GOAWAY drain；集群跨节点 relay（沿用既有 relay 测试夹具）。
6. `internal/server/api_test.go` 扩展：`protocol=http-proxy` 的创建与更新（哨兵强制、config 字段校验、credentialId 归属校验、domain 冲突）、非管理员越权、分页、幂等重放。
7. `internal/storage/credential_repository_test.go`：`proxy_basic` 写入、读取、软删、加解密往返，SQLite 与 MySQL 双驱动。
8. `internal/config/config_test.go`：新键默认值、三路优先级、启动校验失败用例（`domain_suffix` 缺失、`trusted_proxies` 非法、非回环 listen 搭配 `0.0.0.0/0`）。
9. `internal/server/managed_route_handler_test.go`：`http-proxy` 行不进入 HTTP 反代路由表；proxy 索引正确；TTL 生效；停用路由在下一次快照后不可用。
10. 前端：`web` 单测覆盖表单校验与类型切换；`npm run build` 后执行 `./scripts/verify-web-embed.sh`。
11. Lua/nginx 冒烟：`test/e2e/proxy-entry/`，使用 `openresty/openresty:alpine` 容器加 Go stub 上游；无 docker 时 skip 并打印原因；覆盖 CONNECT 成功、403、407 与非 CONNECT 绝对形式。

门禁命令：`go test ./... -count=1`、`go test -race ./...`、`go vet ./...`、`gofmt -l internal/ cmd/ test/`、`git diff --check`、`cd web && npm test -- --run && npm run build`、`./scripts/verify-web-embed.sh`。

## 15. 实施阶段与 Task 0 中止判据

Task 0（spike，先行且阻塞后续任务）在目标 OpenResty 上验证：

- (a) `nginx -V` 输出包含 proxy_connect 模块及其补丁变体；
- (b) server 级 `access_by_lua_block` 能收到 CONNECT，且 `$connect_host`、`$connect_port`、`$ssl_server_name` 可读；
- (c) raw socket splice 能跑通 `curl -x https://tp-test.<suffix>:443 https://<stub>`；
- (d) `ngx.exit(444)` 不产生额外响应，error.log 无异常噪声；
- (e) 非 CONNECT 分支中 `Proxy-Authorization` 是否被 nginx 丢弃，据此确认是否必须显式 `proxy_set_header`。

spike 产物只作为结论文档记录，不进入生产代码。

中止判据：若 (b) 或 (c) 不成立，停止 A3，回退 A2（Server 自终止 TLS + `SNIRouteIdentity`）。由于 identity 已抽象、Server 策略与转发逻辑不变，回退只需新增 TLS 监听与证书配置，nginx 改动归零；回退决定必须写回本文件第 17 节并重出实施计划。

后续阶段顺序：Server 策略内核（`internal/proxyentry`）-> 入口 listener 与 handler 及 E2E -> 数据模型、管理 API、凭据类型 -> 管理后台 -> OpenResty 产物与部署文档 -> 指标、Grafana、排障文档 -> 全量门禁与 PR 文档。

## 16. 文档交付

- `docs/deployment/openresty-proxy-entry.md`：前置检查（`nginx -V`、通配证书覆盖 tp-*、泛解析 DNS）、配置安装步骤、reload 对在途隧道的影响、容量评估、回滚方式（删除 server 块后 reload）。
- `docs/user-guide/http-proxy-entry.md`：管理员如何创建路由、凭据与 ACL；用户如何配置 macOS、Windows、Chrome PAC、curl；错误码自助排查。
- `docs/deployment/nginx.md`：追加 tp-* server 块说明，并记录“为什么不能用 proxy_connect 做链式转发”的结论。
- `docs/operations/configuration.md`：`server.proxy_entry` 全字段。
- `docs/operations/troubleshooting.md`：新增章节，覆盖 403/407/502/504、SNI 不匹配、h2 协商失败、Lua 超时、reload 断链。
- `docs/api/openapi.yaml`、`docs/README.md` 索引、监控文档中的 Grafana 面板说明。
- `docs/architecture/`：入口拓扑图与本设计的决策记录；本文件即 ADR 载体，发生重大回退时另行补记。

## 17. 已决策记录

| 决策 | 选择 | 被否方案与理由 |
|---|---|---|
| 入口承载 | OpenResty server 级 `access_by_lua_block` + raw socket | `content_by_lua_block`：CONNECT 跳过 location 匹配，永不触发；proxy_connect 链式转发：模块明确不受 `proxy_pass` 影响，自己直连目标，route 身份与 `Proxy-Authorization` 全部丢失 |
| 路由身份 | TLS SNI，由 nginx 转成可信头 | 用 Basic username 携带：无认证模式没有 username；每路由独立端口：新增路由都要改 nginx |
| 内部入口 | 同进程第三个 listener，默认 `127.0.0.1:8089` | 复用 `server.http_addr`：`trusted_proxies` 边界失效，长隧道与短请求互相拖累，CONNECT 与托管路由反代语义混淆 |
| 存储 | 复用 `tunnels` + 哨兵 target + config JSON，Schema 保持 v13 | 新增表或新增列：需要 v14 迁移与 expand/contract，收益不抵成本 |
| 凭据 | 复用 `credentials`，新增 `proxy_basic` 类型 | 复用 `password` 类型：与 SSH 密码语义混淆，username 无处存放 |
| 策略位置 | 全部在 Server（Go） | 放在 Lua：无法单测、形成双份权威、易与 Server 校验漂移 |
| SOCKS5 | 本轮不做 | 用户已明确只做 HTTP 代理；stream 模块因此也不再需要 |
| 域名冲突判定 | `http-proxy` 路由按域名整体互斥（`tunnelRouteConflict` 增加 `domainOnly`），create 与 update 共用一个实现 | 沿用 `domain + path_prefix` 复合判定：tp-* 的 `path_prefix` 恒为 `"/"`，与同域名不同前缀的反代路由不会被数据库 `UNIQUE` 拦住，会留下一条被 nginx 精确 `server_name` 永久遮蔽、后台看不出原因的不可达路由 |
| 前端“活跃隧道数” | 不新增列表列；抽屉内指向 Grafana Row `HTTP Proxy Entry` → 面板 `Proxy tunnels active` | 新增只读聚合接口：管理 API 未暴露该指标，为纯展示引入新端点、权限校验与前端轮询，成本高于收益 |

## 18. 验收标准

1. 管理员在后台创建 `tp-demo` 路由（指定出口 agent、Basic 凭据、ACL `11.71.85.0/24`）后，无需重启 Server、无需修改 nginx，5 秒内生效。
2. ACL 内主机执行 `curl -x https://tp-demo.<suffix> --proxy-user u:p https://ifconfig.me` 成功，且出口 IP 属于 agent 所在网络；ACL 外主机返回 403；密码错误返回 407，第 6 次进入退避。
3. 无认证模式路由：不带凭据可用，带错误凭据同样可用（不校验），但 ACL 仍然生效。
4. `allowPrivateTargets=true` 时可访问 agent 内网服务；访问 169.254.169.254 恒返回 403。
5. 集群模式下 agent 连接在另一节点时隧道正常，`POST /api/v1/agents/{agentId}/trace` 能看到完整 hop。
6. 全部门禁命令通过；第 16 节列出的文档全部更新；OpenAPI 与实现一致。
