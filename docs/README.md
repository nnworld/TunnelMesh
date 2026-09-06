# TunnelMesh 文档

## 快速入口

- [客户端使用帮助](user-guide/client.md)
- [Agent 使用帮助](user-guide/agent.md)
- [Server 管理后台](user-guide/server-admin.md)
- [托管 HTTP 路由](user-guide/managed-http-route.md)
- [SSH / websocat TCP 代理](user-guide/tcp-over-websocket-ssh.md)
- [Docker 部署](deployment/docker.md)
- [配置说明](operations/configuration.md)
- [故障排查](operations/troubleshooting.md)
- [OpenAPI](api/openapi.yaml)

## 模式选择

- 本地模式使用 SQLite，适合单机试用和小规模部署。
- 集群模式使用 MySQL 管理数据；注册发现默认使用 MySQL lease，也可以切换到 etcd。

所有部署都应先执行 `check-config`，再执行 `run`。生产环境建议通过环境变量或外部配置文件注入敏感配置。
