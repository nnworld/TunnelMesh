# TunnelMesh Server 管理后台使用指南

## 登录与首次凭据

首次启动 Server 会在控制台输出一次管理员用户名和高强度密码。请立即保存并通过 HTTPS 访问后台登录页。数据库只保存密码哈希，不会再次显示明文密码。

如果凭据遗失，在 Server 主机执行：

```bash
tunnelmesh-server admin regenerate-credentials --config tunnelmesh.yaml --confirm
```

命令会撤销旧会话并输出新凭据。生产环境建议限制控制台和数据库访问权限。

## Dashboard

Dashboard 用于查看 Server 健康状态、在线 Agent 数量、活动隧道和近期审计事件。部署或扩容后先检查数据库、注册中心和 WebSocket 接入状态，再创建路由。

## Agent 列表与详情

Agents 页面展示 Agent ID、名称、启用状态和能力。点击 Details 可查看 Agent 详情及运行时 metadata：

- 当前在线/过期（Stale）状态；
- node、epoch、revision、最后上报时间和更新时间；
- 字段名称、来源类型（`file` 或 `env`）和值；
- 敏感字段显示为 Redacted，后台没有编辑上报值的入口。

Agent metadata 只能由 Agent 按 allowlist 上报。修改字段必须修改 Agent 配置并等待下一次上报；管理员不能通过 API 伪造上报数据。

## Agent Policy

Policy 页面限制 Agent 可访问的协议、目标 CIDR 和端口。建议按最小权限创建规则，例如 SSH 只允许 `tcp/22` 和明确的内网 CIDR。拒绝规则会在 Agent 侧返回 policy 错误，不会关闭其他隧道。

## Explicit Route 与 Wildcard Route

Routes 页面支持将某个域名/路径绑定到 Agent 的目标主机和端口。域名与路径组合必须唯一，写请求建议携带稳定的 `Idempotency-Key`。

动态泛域名格式为：

```text
<agent-id>-<ip-encoding>-<port>.apps.example.com
```

IP 和端口使用明文编码，便于排查；公网 Server 仍只暴露 80/443，不提供公网 UDP。

## Tunnel 状态

Tunnels 页面显示本地 forward、publish route 和连接状态。异常时先查看 Agent online/lease 状态，再检查 policy、目标端口和 Server 审计事件。停止或重试操作应使用同一隧道 ID，避免重复创建。Agent metadata 的 stale 状态由 WebSocket 会话租约决定：正常在线 Agent 会通过协议级 `PING/PONG` 自动续期，断线或心跳停止超过 metadata TTL 后才显示为 stale。

## 审计日志

Audit Logs 记录登录、凭据恢复、Agent/Policy/Route/Tunnel 操作、metadata 读取和 SSH/TCP proxy 上下文。日志记录身份、资源、epoch/revision 和错误码，不记录 metadata 明文、SSH 私钥或会话字节。

## 角色与权限

- `admin`：管理所有 Agent、Policy、Route、Tunnel、用户和审计日志。
- 普通用户：查看和操作自己拥有的 Agent 及其隧道，不能读取其他用户的 metadata。
- 任何角色都不能通过管理 API 修改 Agent 上报值。

所有 API 使用统一响应 `{ code, msg, data }`。分页使用 cursor；写请求应携带 `Idempotency-Key`，重试时复用相同 key。

## 配置与排障建议

优先级为命令行参数 > 环境变量 > 配置文件 > 默认值。上线前执行 `check-config`，确认本地 SQLite 或集群 MySQL、注册中心、80/443 地址和 TLS 配置正确。出现 401/403 时检查 token 与角色；出现 404 时检查 Agent ID、路由 Host/path 和 wildcard DNS；出现 stale 时检查 Agent WebSocket、租约和系统时间。
