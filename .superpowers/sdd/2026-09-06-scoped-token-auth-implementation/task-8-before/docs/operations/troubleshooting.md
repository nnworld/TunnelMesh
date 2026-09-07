# 故障排查

## 启动失败

先执行：

```bash
tunnelmesh-server --config tunnelmesh.yaml check-config
tunnelmesh-server --config tunnelmesh.yaml print-config
```

`print-config` 输出的是脱敏后的有效配置。重点检查 mode、storage.driver、MySQL DSN、TLS、node.id 和 registry.type。

## schema 错误

- 自动初始化关闭时，确认数据库已经执行 `migrations/ddl.sql`。
- 检查 `schema_meta` 版本是否与当前程序一致。
- SQLite 检查挂载目录是否可写；容器中通常是 `/var/lib/tunnelmesh`。
- MySQL 检查账号是否有建表、索引和事务权限。

## Agent 不在线

1. 确认 Agent 能访问 Server 的 `wss://` 地址。
2. 确认证书链、SNI 和系统时间正确。
3. 检查 Agent ID 是否稳定且没有重复注册。
4. 检查 Server 节点租约是否过期，以及集群节点时间是否同步。
5. 确认目标服务从 Agent 所在网络可达，而不是只在 Server 主机可达。

## HTTP 路由失败

- 404：检查域名、pathPrefix、wildcard DNS 和 API 路由是否匹配。
- 401/403：检查 Token、角色和资源 owner。
- 409：域名和路径已存在，重用原配置或删除旧路由。
- 502/504：检查 Agent session、目标地址、目标端口和 policy。

## SSH / websocat 失败

- 必须使用 `websocat --binary`。
- 检查 ProxyCommand URL 是否使用 `wss://` 和正确的域名/端口编码。
- 看到 WebSocket 文本帧错误时，检查客户端是否误用了 text 模式。
- 使用 `ssh -vvv` 和 `websocat -v` 获取握手与关闭原因。

## 数据安全

排障日志中可以保留 trace ID、Agent ID、路由 ID 和错误码，但不能输出密码、Bearer Token、私钥或完整 DSN。
