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

路由匹配按域名和路径执行；同一域名、同一路径不能重复创建。Server 会通过数据库唯一约束和 API 冲突检查共同保证这一点。

## 动态泛域名

启用 wildcard DNS 后，可使用以下形式表达 Agent、目标 IP 和端口：

```text
<agent-id>-<ip-encoding>-<port>.apps.example.com
```

其中 IP 和端口使用明文编码，便于人工配置和排查；不要把 Token 或其他敏感信息放进域名。动态 wildcard 只适用于服务端支持的 HTTP/HTTPS/WebSocket 入口，不提供公网 UDP。

## DNS 与 TLS

1. 为 `*.apps.example.com` 配置 wildcard DNS，指向 Server/LB。
2. 为 wildcard 域名配置证书，或在 LB 层终止 TLS。
3. 将 HTTP/HTTPS 流量转发到 Server 的 80/443。
4. 在后台创建明确路由，或按约定启用动态 wildcard 解析。
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
    "targetPort": 3000
  }'
```

写操作应始终设置稳定的 `Idempotency-Key`，重试时复用同一个值。
