# OpenResty tp-* HTTP 代理入口部署

本文讲怎么把 `deploy/openresty/` 的产物装到生产 OpenResty 上，让 `https://tp-<name>.<domain_suffix>`
成为标准 HTTPS 代理入口。产物本身的用途、占位符清单与一致性测试见
[deploy/openresty/README.md](../../deploy/openresty/README.md)；使用者视角的各端配置与错误码见
[HTTP 代理入口](../user-guide/http-proxy-entry.md)；既有 admin 与托管路由的 server 块见
[Nginx 推荐配置](nginx.md)。

## 为什么必须是 OpenResty

公网入口只允许 HTTP/HTTPS/WebSocket，而浏览器与操作系统的 HTTPS 代理会先发 `CONNECT`。纯 nginx
做不到“按 SNI 选路由再把 CONNECT 搬进 Server”：CONNECT 跳过 location 匹配，`proxy_pass` 与
`proxy_connect;` 链式转发又会丢掉路由身份和 `Proxy-Authorization`。可行方案只有打过
`ngx_http_proxy_connect_module` 补丁的 OpenResty 内核 + server 级 `access_by_lua_file`，用补丁
注册的 `$connect_host`/`$connect_port` 变量读出隧道目标，再用 `ngx.req.socket(true)` 做双向 splice。

tp-* server 块与既有 admin/托管路由 server 块**共用 443**，由 SNI 分流，不新增公网监听端口。

## 适用前提

- OpenResty，内核 nginx >= 1.25.1，已编入 `lua-nginx-module`，并且已打
  `proxy_connect_rewrite_102101.patch`（对应 OpenResty 1.25.3.1 与模块 tag `v0.0.7`）。
- 通配证书覆盖 `*.<domain_suffix>`。
- 泛解析 DNS：`tp-*.<domain_suffix>` 指向入口 IP。
- 推荐 OpenResty 与 `tunnelmesh-server` 同机部署，内部入口只绑回环；跨机部署见文末。

上线前必须做**两级**内核检查，两级都过才算具备条件：

```bash
# 第一级：模块有没有编进内核
nginx -V 2>&1 | tr ' ' '\n' | grep proxy_connect

# 第二级：补丁有没有真的打上（CONNECT 能不能进到 server 级 access_by_lua）
bash deploy/openresty/spike-connect-check.sh 18443
```

两级都要，是因为只看第一级会误判：模块单独 `--add-module` 编进去时 `nginx -V` 一样能看到
`proxy_connect`，但**没打补丁的内核会在解析阶段就对每个 CONNECT 回 405**，`$connect_host` 这个
变量也根本不存在，`access_by_lua` 永远收不到请求。第二级的判读方式写在脚本头部注释里：nc 侧收到
`HTTP/1.1 200 Connection Established` 且 error.log 里出现 `spike connect_host=...` 才算通过。

## Server 侧开关

内部入口默认关闭，`enabled=false` 时行为与旧版本完全一致。最小配置：

```yaml
server:
  proxy_entry:
    enabled: true
    domain_suffix: tm.example.com
    listen: 127.0.0.1:8089
    trusted_proxies: ["127.0.0.1/32", "::1/128"]
```

环境变量等价写法：

```bash
export TUNNELMESH_SERVER_PROXY_ENTRY_ENABLED=true
export TUNNELMESH_SERVER_PROXY_ENTRY_DOMAIN_SUFFIX=tm.example.com
export TUNNELMESH_SERVER_PROXY_ENTRY_LISTEN=127.0.0.1:8089
```

全部 13 个键的默认值与含义见[配置说明](../operations/configuration.md#tp--http-代理入口)。改完
**先校验再重启**：

```bash
tunnelmesh-server --config /etc/tunnelmesh/server.yaml check-config
```

`enabled=true` 时 `domain_suffix` 必填；`listen` 不是回环地址时 `trusted_proxies` 不允许出现
`0.0.0.0/0` 或 `::/0`，否则任何主机都能伪造路由身份与来源 IP，`check-config` 会直接失败。

## 渲染模板

模板是 `deploy/openresty/tunnelmesh-proxy.conf.example`，不能直接 include，必须先渲染 7 个占位符：

| 占位符 | 取值来源 | 生产示例 |
| --- | --- | --- |
| `__LISTEN__` | 监听地址 | `443` |
| `__SERVER_NAME_REGEX__` | 由 `server.proxy_entry.domain_suffix` 推导的 tp-* 主机名正则 | `~^tp-[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?\.tm\.example\.com$` |
| `__SSL_CERT__` | 覆盖 `*.<domain_suffix>` 的通配证书 | `/data/ssl/tm.example.com_bundle.crt` |
| `__SSL_CERT_KEY__` | 对应私钥，0600 | `/data/ssl/tm.example.com.key` |
| `__LUA_FILE__` | 搬运层 Lua 的绝对路径 | `/etc/openresty/lua/tunnelmesh_proxy_entry.lua` |
| `__INTERNAL_UPSTREAM__` | 必须等于 `server.proxy_entry.listen` | `127.0.0.1:8089` |
| `__EDGE_ALLOW__` | 粗粒度来源白名单指令 | `allow 10.0.0.0/8; allow 11.0.0.0/8; deny all;` |

Lua 侧另有 3 个行尾标记，只在内部入口地址或 `idle_timeout` 与默认值不同时才需要改：

| 行尾标记 | 取值来源 | 默认值 |
| --- | --- | --- |
| `-- __TM_INTERNAL_HOST__` | `server.proxy_entry.listen` 的 host | `127.0.0.1` |
| `-- __TM_INTERNAL_PORT__` | `server.proxy_entry.listen` 的 port | `8089` |
| `-- __TM_READ_TIMEOUT_MS__` | `server.proxy_entry.idle_timeout + 30s`，毫秒 | `330000` |

`read_timeout_ms` 必须**比 Server 的 `idle_timeout` 长**（默认 +30s），让 Server 先判定空闲并关闭，
Lua 只兜底；配反了会表现为 nginx 先掐断隧道，客户端看到莫名断开。这三项默认值与 Go 配置默认值的
一致性由 `deploy/openresty/openresty_artifacts_test.go` 守护，改配置默认值必须同步改 Lua。

渲染完成后的完整 server 块（后缀 `tm.example.com`，可直接对照）：

```nginx
upstream tunnelmesh_proxy_entry {
    server 127.0.0.1:8089;
    keepalive 32;
}

server {
    listen 443 ssl;
    server_name ~^tp-[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?\.tm\.example\.com$;

    ssl_certificate     /data/ssl/tm.example.com_bundle.crt;
    ssl_certificate_key /data/ssl/tm.example.com.key;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_session_cache   shared:TLS:10m;

    # 粗粒度前置，只挡明显的公网扫描；按路由的细粒度 ACL 在 Server 侧执行。
    allow 10.0.0.0/8;
    allow 11.0.0.0/8;
    deny  all;

    # 客户端断开时立刻结束 Lua 请求，避免半开隧道占用 worker connection。
    lua_check_client_abort on;
    access_by_lua_file /etc/openresty/lua/tunnelmesh_proxy_entry.lua;

    # 非 CONNECT（绝对形式）请求。nginx 会把绝对形式改写成 origin-form 再交给
    # location，所以 Server 侧从 Host 头恢复目标（见 splitProxyTarget）。
    location / {
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
        proxy_connect_timeout 10s;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }
}
```

## 安装步骤

```bash
# 1. 搬运层
install -m 0644 deploy/openresty/tunnelmesh_proxy_entry.lua \
    /etc/openresty/lua/tunnelmesh_proxy_entry.lua

# 2. 渲染模板（占位符替换方式自选，渲染结果建议纳入配置管理）
#    把渲染出的 upstream 与 server 块并入既有 nginx.conf 的 http{}，
#    与 admin / 托管路由 server 块并列。

# 3. main 上下文加排空窗口（不能写在 server 块里）
#    worker_shutdown_timeout 300s;

# 4. 校验并热加载
nginx -t
nginx -s reload
```

`worker_shutdown_timeout` 属于 **main 上下文**，写在 server 块里 `nginx -t` 会直接报错。它决定
reload 时在途隧道有多久排空窗口，缺省值会让发布瞬间所有代理连接被立刻掐断。

`error_log` 至少要 `info` 级，否则看不到隧道关闭统计（route/target/client/字节数/时长/结束原因），
排障时无从下手。

## 容器方式（可选）

```bash
docker build -f deploy/openresty/Dockerfile.proxy-connect \
    -t tunnelmesh/openresty-proxy-connect:1.25.3.1 .
docker run --rm tunnelmesh/openresty-proxy-connect:1.25.3.1 -V 2>&1 \
    | tr ' ' '\n' | grep proxy_connect
```

替换生产 OpenResty 之前，**必须先在目标机执行 `nginx -V`，把输出里的 `--with-*` 参数补进
Dockerfile 的 `./configure`**，否则现有 server 块会因为缺模块起不来（Dockerfile 里默认只带了
ssl/v2/realip/gzip_static 与 pcre-jit，够跑冒烟但未必够跑你的生产配置）。

镜像默认以非 root 的 `openresty` 用户运行，因此只适合监听 1024 以上端口。要在容器里直接监听 443，
改用 root 运行，或者 `setcap cap_net_bind_service=ep /usr/local/openresty/nginx/sbin/nginx`。

## 验证

```bash
# ACL 内主机，凭据正确：出口 IP 应当属于 agent 所在网络
curl -sv --proxy-insecure -x https://tp-demo.tm.example.com \
     --proxy-user 'u:p' https://ifconfig.me
```

`--proxy-insecure` 只用于自签证书场景，生产用正式通配证书时应去掉。三条预期结果：

| 场景 | 预期 |
| --- | --- |
| ACL 内 + 凭据正确 | 200，返回的出口 IP 属于出口 agent 所在网络 |
| ACL 外主机 | `Received HTTP code 403 from proxy after CONNECT` |
| 密码错误 | `Received HTTP code 407 from proxy after CONNECT`，响应带 `Proxy-Authenticate: Basic realm="TunnelMesh", charset="UTF-8"` |

创建或修改路由后**不需要重启 Server，也不需要 reload nginx**，5 秒内生效（沿用既有
`loadManagedRoutes` 快照 TTL）。

## 容量评估

每条隧道在 nginx 侧占用 1 个请求 + 2 个 socket（下游客户端 + 内部入口），在 Server 侧占用 1 条到
agent 的 stream。因此：

- `worker_connections` 与 `worker_rlimit_nofile` 按 `server.proxy_entry.max_concurrent_tunnels × 2`
  起评，再叠加既有 admin/托管路由的量。
- **Server 先拒绝超限请求**（503 + `Retry-After: 5`，稳定错误码 `proxy_capacity_exhausted`）。
  nginx 侧的 `limit_conn` 只是兜底，不要配得比 Server 更紧，否则排障时看到的是 nginx 自己的 503
  而不是稳定错误码，审计与指标里也不会有对应记录。
- 单路由的并发上限走后台的“并发上限”字段（`config.maxConcurrentTunnels`），0 表示只受全局上限约束。

## reload 与发布影响

`nginx -s reload` 会让旧 worker 停止接受新连接，在途隧道在 `worker_shutdown_timeout` 窗口内排空，
超时后被强制关闭；客户端表现为连接断开并重连。发布窗口建议：

1. 选低峰期。
2. 集群模式先扩 Server 节点，确认新节点 ready 再动 nginx。
3. `nginx -t` 通过后再 reload；reload 后立刻看 `tunnelmesh_proxy_entry_tunnels_active` 是否回升。
4. Server 侧升级同理：`shutdown_timeout`（默认 30s）内等待在途隧道排空，超时强制关闭。

## 回滚（5 分钟内）

两条独立的止损路径，任选其一，都不涉及数据回滚：

1. **删 nginx 配置**：把渲染进去的 `upstream tunnelmesh_proxy_entry` 与 tp-* `server` 块删掉，
   `nginx -s reload`。入口立刻消失，既有 admin 与托管路由不受影响。
2. **关 Server 开关**：`server.proxy_entry.enabled=false` 后重启 Server。内部入口关闭，nginx 侧
   表现为 502（`proxy_egress_unavailable`），效果等价但会留下一层 nginx 错误日志。

路由数据无需回滚：`tunnels` 表里的 `http-proxy` 行可以直接在后台停用或删除，Schema 仍是 v13，
本次特性零 DDL。

## 跨机部署内部入口（不推荐）

只有在 OpenResty 与 Server 无法同机时才这么做。此时内部段是**明文**，链路上的 Basic 凭据可被
嗅探，必须满足：

- `listen` 绑内网地址而不是 `0.0.0.0`。
- `trusted_proxies` 收紧到 OpenResty 主机的具体 IP（`/32`）。
- 禁止 `0.0.0.0/0` 与 `::/0`——`check-config` 在非回环 `listen` 下会直接拒绝这种组合。
- 内部网段走专线或 IPSec，不要跨公网。

同机部署时内部入口只绑回环，`trusted_proxies` 保持默认的 `["127.0.0.1/32", "::1/128"]` 即可。
