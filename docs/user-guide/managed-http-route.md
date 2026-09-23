# 托管 HTTP 路由

托管路由由 Server 接收公网 HTTP/HTTPS/WebSocket 请求，再通过 Agent 连接到指定内网服务。公网只需要暴露 Server 的 80/443，不需要为每个 Agent 新开端口。

`protocol=http-proxy` 的路由**不是反向代理**：它由 TLS SNI 选路由、目标由每个代理请求决定，
`target_host`/`target_port` 存的是哨兵 `*`/`0`，域名固定为 `tp-<name>.<domain_suffix>`。
使用说明见 [HTTP 代理入口（tp-*）](http-proxy-entry.md)，部署见
[OpenResty tp-* 代理入口](../deployment/openresty-proxy-entry.md)。

## 明确子域名

在管理后台创建 Route，填写：

- `domain`：例如 `git.example.com`
- `pathPrefix`：例如 `/`
- `agentId`：目标 Agent
- `targetHost`：Agent 网络内的服务地址
- `targetPort`：目标服务端口
- `protocol`：`http` 或 `ws`
- `targetScheme`：Agent 到目标服务的上游协议，`http` 或 `https`，默认 `http`
- `hostHeader`：发送给目标服务的 Host，默认等于 `targetHost`
- `tlsServerName`：上游 HTTPS 的 SNI 和证书校验名，默认等于 `targetHost`

路由匹配按域名和路径执行；同一域名、同一路径不能重复创建。Server 会通过数据库唯一约束和 API 冲突检查共同保证这一点。

## 只支持域名访问的上游服务

`targetHost` 可以填写 IP 或域名。如果服务部署在固定 IP 上但只接受特定域名访问，可以分开配置拨号地址和 Host：

```json
{
  "agentId": "agent-devbox",
  "protocol": "http",
  "domain": "app.example.com",
  "pathPrefix": "/",
  "targetHost": "10.0.0.10",
  "targetPort": 443,
  "hostHeader": "service.internal.example.com",
  "targetScheme": "https",
  "tlsServerName": "service.internal.example.com"
}
```

实际链路为：

```text
客户端 Host: app.example.com
Agent 拨号: 10.0.0.10:443
上游 TLS SNI: service.internal.example.com
上游 HTTP Host: service.internal.example.com
```

`targetScheme=https` 时，Agent 会使用系统信任根校验目标证书，并要求 TLS 1.2 及以上；不能关闭证书校验。`tlsServerName` 只在 `targetScheme=https` 时允许配置。公网路由的 `protocol` 仍表示对外协议，`websocket` 路由也可以配置 `targetScheme=https` 来访问 `wss` 上游服务。

client 本地端口映射没有独立的 `hostHeader`、`targetScheme` 或 `tlsServerName` 配置。普通 HTTP 虚拟主机可在本地请求中携带原始 Host；HTTPS-only 服务可使用 TCP 透明映射并让本地应用完成 TLS。具体命令参见 [tunnelmesh-client 使用帮助](client.md)。

## 修改托管路由

在管理后台 Routes 页面点击“编辑”，可以修改 Agent、域名、路径、协议、目标地址、目标端口和状态。普通用户只能编辑自己拥有 Agent 的路由；管理员可以编辑所有路由。修改目标 Agent 时，后端会再次校验新 Agent 的归属，避免把路由转移到其他用户资源。

路由更新会写入 `route.updated` 审计日志，详情包含 Agent、域名、路径、目标地址、端口和状态，不包含凭据。Server 内部路由缓存默认 5 秒刷新一次，因此修改成功后最长 5 秒内生效。

## 动态泛域名

在 Server 配置 `server.dynamic_suffix`，并启用 wildcard DNS 后，可使用以下形式表达 Agent、目标 IP 和端口：

```text
<agent-id>-<ip-encoding>-<port>.apps.example.com
```

其中 IP 和端口使用明文编码，便于人工配置和排查；`127.0.0.1` 表示 Agent 所在主机上的本机服务。DNS Host 不区分大小写，因此动态域名中的 Agent ID 会与在线 Agent 做大小写不敏感匹配；如果存在多个仅大小写不同的 Agent ID，Server 会拒绝该动态请求，避免路由到错误 Agent。不要把 Token 或其他敏感信息放进域名。动态 wildcard 只适用于服务端支持的 HTTP/HTTPS/WebSocket 入口；VPN 网关的公网 UDP 端口是独立入口，不参与动态域名解析（[ADR 0002](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)，实施中）。

动态泛域名当前只支持 IPv4 目标和明文 HTTP 上游，不支持 `hostHeader`、`targetScheme=https` 或 `tlsServerName`。如需这些能力，请创建显式托管路由。

## 显式泛域名

显式托管路由只支持单层 `tm-*` 前缀。API 中将域名模式写为
`tm-*.example.com`，实际请求例如 `tm-git.example.com`。通配符不能直接写成
`*.example.com`，也不能匹配多级子域名；不符合规则的域名会被 Server 拒绝。

## 上游路径与控制面保留端点

托管路由按 **Host** 归属，不按路径前缀归属。一个域名要么整站属于某条路由，要么是 Server 的控制面 origin（管理后台、Agent 拨号、健康检查）。因此上游服务可以自由使用 `/api/...`、`/ws/...` 这类路径，Server 不会把它们当成自己的管理接口截走。

`tm-*` 命名空间（`tm-*.<domain_suffix>` 显式泛域名与动态泛域名）的域名把**全部路径**交给上游，控制面不保留任何路径：

| 请求 | 处理方 |
| --- | --- |
| `https://tm-6000d.example.com/api/skill/claw/cate` | 上游服务 |
| `https://tm-6000d.example.com/ws/chat` | 上游服务 |
| `https://tm-6000d.example.com/health`、`/metrics` | 上游服务 |
| `https://tm-6000d.example.com/skills` | 上游服务 |

显式域名路由（例如 `git.example.com`）同样把业务路径交给上游，但保留四个 TunnelMesh 自有端点，因为运营者填写的域名有可能与控制面 origin 或 Agent 拨号 origin 撞名，而这两个端点被接走的后果是整个 Agent 无法重连、LB 把健康节点摘除：

| 保留路径 | 归属 | 原因 |
| --- | --- | --- |
| `/ws/agent`、`/ws/client` | 控制面 | Agent/Client 握手端点，被劫持会导致该 Agent 背后所有路由一起失效 |
| `/health/` 前缀、`/metrics` | 控制面 | LB 与监控探针目标，必须先于路由解析可用 |

上游服务如果确实需要在显式域名上暴露 `/health`、`/metrics`、`/ws/agent`、`/ws/client`，请改用其它路径，或改用 [HTTP 代理入口（tp-*）](http-proxy-entry.md)（该入口按注入的身份头解析路由，不做路径前缀判定）。

**不要把管理后台 origin 或 Agent 拨号 origin 配成显式域名路由**，否则该 Host 的控制面（含后台静态资源）会被路由接管。`tm-*` 命名空间不存在这个风险，管理后台 origin 不会以 `tm-` 开头。

## DNS 与 TLS

1. 为 `*.apps.example.com` 配置 wildcard DNS，指向 Server/LB；若使用显式泛域名，额外配置 `*.tunnel.example.com`。
2. 为相应 wildcard 域名配置证书，或在 LB 层终止 TLS。
3. 将 HTTP/HTTPS 流量转发到 Server 的 80/443。
4. 在后台创建明确路由，或将 `server.dynamic_suffix` 配置为实际动态域名后缀。
5. 用浏览器和 `curl -v` 验证 Host、路径和 WebSocket Upgrade。

## 大响应体与流控

托管路由的响应体走 `浏览器 → Server → Agent → 内网服务` 隧道，与 WebSSH 共用同一套流控：Server 在 `OPEN_STREAM` 中通告 512 KiB 接收窗口，Agent 按窗口分帧发送（单帧上限 32 KiB），Server 在真正读走字节后才回补 `WINDOW_UPDATE`。前端打包产物（例如几 MB 的 `assets/*.js`）、文件下载和长轮询都能完整传输，**无需任何配置项**。流控全貌见 [大文件传输与通道流控](server-admin.md#大文件传输与通道流控)。

排障要点：

- 响应体在固定大小处被截断，浏览器报资源加载中断或 `net::ERR_CONTENT_LENGTH_MISMATCH`，而响应头里的 `Content-Length` 正常：说明 Agent 侧的流在队列满时被关闭，通常是 Agent 版本旧于 Server（每流发送队列小于 Server 通告的窗口）。把 Server 与 Agent 升级到同一版本即可。
- 经 Nginx/OpenResty 反代时，`proxy_buffering off` 与足够长的 `proxy_read_timeout` 是大响应体不被中间层掐断的前提，见 [Nginx/WSS 推荐配置](../deployment/nginx.md)。

## API 示例

```bash
curl -X POST https://tunnel.example.com/api/v1/routes \
  -H "Authorization: Bearer $TUNNELMESH_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: route-git-001" \
  -d '{
    "agentId": "agent-devbox",
    "protocol": "http",
    "domain": "git.example.com",
    "pathPrefix": "/",
    "targetHost": "127.0.0.1",
    "targetPort": 3000,
    "targetScheme": "http"
  }'
```

写操作应始终设置稳定的 `Idempotency-Key`，重试时复用同一个值。

更新已有路由使用 `PATCH /api/v1/routes/{routeId}`，只提交需要修改的字段：

```bash
curl -X PATCH https://tunnel.example.com/api/v1/routes/route-id \
  -H "Authorization: Bearer $TUNNELMESH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "agentId": "agent-devbox",
    "domain": "git.example.com",
    "pathPrefix": "/",
    "protocol": "websocket",
    "targetHost": "127.0.0.1",
    "targetPort": 3000,
    "targetScheme": "https",
    "tlsServerName": "service.internal.example.com",
    "hostHeader": "service.internal.example.com",
    "status": "active"
  }'
```
