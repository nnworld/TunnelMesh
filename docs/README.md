# TunnelMesh 文档

## 快速入口

- [客户端使用帮助](user-guide/client.md)
- [Agent 使用帮助](user-guide/agent.md)
- [Server 管理后台](user-guide/server-admin.md)
- [托管 HTTP 路由](user-guide/managed-http-route.md)
- [SSH / websocat TCP 代理](user-guide/tcp-over-websocket-ssh.md)
- [Docker 部署](deployment/docker.md)
- [Nginx/WSS 推荐配置](deployment/nginx.md)
- [跨平台可执行文件打包](deployment/binary-release.md)
- [Linux systemd 安装](deployment/linux-systemd.md)
- [macOS launchd 安装](deployment/macos-launchd.md)
- [Windows Service 安装](deployment/windows-service.md)
- [配置说明](operations/configuration.md)
- [可观测性与统一 Grafana Dashboard](operations/observability.md)
- [全链路网络探针](operations/network-probes.md)
- [日志位置与查看方式](operations/logging.md)
- [SLO](operations/slo.md)
- [容量与压测](operations/capacity.md)
- [项目完整性清单](operations/completeness-checklist.md)
- [故障排查](operations/troubleshooting.md)
- [WebSocket 协议](protocol/websocket.md)
- [代理协议模块](protocol/proxy-modules.md)
- [协议与安全设计规格](superpowers/specs/2026-09-07-protocol-modules-traceroute-design.md)
- [架构概览](architecture/overview.md)
- [集群架构](architecture/cluster.md)
- [OpenAPI](api/openapi.yaml)

## 模式选择

- 本地模式使用 SQLite，适合单机试用和小规模部署。
- 集群模式使用 MySQL 管理数据；注册发现默认使用 MySQL lease，也可以切换到 etcd。

所有部署都应先执行 `check-config`，再执行 `run`。生产环境建议通过环境变量或外部配置文件注入敏感配置。
