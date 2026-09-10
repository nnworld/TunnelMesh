# 托管 HTTP 路由

托管路由由 Server 接收公网 HTTP/HTTPS/WebSocket 请求，再通过 Agent 连接到指定内网服务。公网只需要暴露 Server 的 80/443，不需要为每个 Agent 新开端口。

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

其中 IP 和端口使用明文编码，便于人工配置和排查；`127.0.0.1` 表示 Agent 所在主机上的本机服务。DNS Host 不区分大小写，因此动态域名中的 Agent ID 会与在线 Agent 做大小写不敏感匹配；如果存在多个仅大小写不同的 Agent ID，Server 会拒绝该动态请求，避免路由到错误 Agent。不要把 Token 或其他敏感信息放进域名。动态 wildcard 只适用于服务端支持的 HTTP/HTTPS/WebSocket 入口，不提供公网 UDP。

动态泛域名当前只支持 IPv4 目标和明文 HTTP 上游，不支持 `hostHeader`、`targetScheme=https` 或 `tlsServerName`。如需这些能力，请创建显式托管路由。

## 显式泛域名

显式托管路由只支持单层 `tm-*` 前缀。API 中将域名模式写为
`tm-*.example.com`，实际请求例如 `tm-git.example.com`。通配符不能直接写成
`*.example.com`，也不能匹配多级子域名；不符合规则的域名会被 Server 拒绝。

## DNS 与 TLS

1. 为 `*.apps.example.com` 配置 wildcard DNS，指向 Server/LB；若使用显式泛域名，额外配置 `*.tunnel.example.com`。
2. 为相应 wildcard 域名配置证书，或在 LB 层终止 TLS。
3. 将 HTTP/HTTPS 流量转发到 Server 的 80/443。
4. 在后台创建明确路由，或将 `server.dynamic_suffix` 配置为实际动态域名后缀。
5. 用浏览器和 `curl -v` 验证 Host、路径和 WebSocket Upgrade。

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
