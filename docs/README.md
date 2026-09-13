# TunnelMesh 文档

## 快速入口

- [客户端使用帮助](user-guide/client.md)
- [Agent 使用帮助](user-guide/agent.md)
- [Server 管理后台](user-guide/server-admin.md)
- [远程服务器与浏览器 SSH/SFTP](user-guide/server-admin.md#远程服务器与浏览器-ssh-sftp)
- [发行管理](user-guide/server-admin.md#发行管理)
- [Server 节点与共享令牌](user-guide/server-admin.md#server-节点)
- [Client 运行观测与连接管理](user-guide/server-admin.md#client-运行观测与连接管理)
- [托管 HTTP 路由](user-guide/managed-http-route.md)
- [SSH / websocat TCP 代理](user-guide/tcp-over-websocket-ssh.md)
- [Docker 部署](deployment/docker.md)
- [管理后台前端构建与部署](deployment/frontend.md)
- [Nginx/WSS 推荐配置](deployment/nginx.md)
- [跨平台可执行文件打包](deployment/binary-release.md)
- [Linux systemd 安装](deployment/linux-systemd.md)
- [macOS launchd 安装](deployment/macos-launchd.md)
- [Windows Service 安装](deployment/windows-service.md)
- [配置说明](operations/configuration.md)
- [Server / Agent / Client 配置示例](operations/config-examples.md)
- [Client 全协议与连接池配置示例](operations/client-configuration-examples.md)
- [Schema 升级与回滚](operations/schema-upgrades.md)
- [Relay mTLS 证书生成与配置](operations/relay-mtls.md)
- [逻辑 Agent 连接池](operations/connection-pool.md)
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
- [WebSSH 浏览器端到端测试](../test/e2e/webssh/README.md)

## 模式选择

- 本地模式使用 SQLite，适合单机试用和小规模部署。
- 集群模式使用 MySQL 管理数据；注册发现默认使用 MySQL lease，也可以切换到 etcd。

所有部署都应先执行 `check-config`，再执行 `run`。生产环境建议通过环境变量或外部配置文件注入敏感配置。

## 测试与验证

三层验证，各自覆盖不同范围，不能互相替代：

| 层级 | 命令 | 覆盖范围 |
| --- | --- | --- |
| Go 单元与集成 | `go test ./... -count=1`、`go test -race ./...`、`go vet ./...` | 协议状态机、流控、Repository 契约（SQLite 与 MySQL 双方言）、迁移、API 授权与分页、跨层集成 |
| 前端单元 | `cd web && npm test -- --run`、`npm run build` | SSH/SFTP/ZMODEM 客户端逻辑、WebSocket 字节流背压、store、路由、视图交互 |
| 浏览器端到端 | `node test/e2e/webssh/run.mjs` | 真实 Chrome + 真实 Server/Agent/SSH 主机，验证凭据自动认证、pty 终端、ZMODEM 双向传输、SFTP 复用与上传逐字节完整性、刷新恢复、浏览器控制台洁净 |

端到端测试不在 `go test` 与 `npm test` 中，需要 Chrome 与 `lrzsz`，详见 [test/e2e/webssh/README.md](../test/e2e/webssh/README.md)。修改中继流控、WebSSH broker、Agent 流分发或终端/SFTP 前端后，发布前必须跑一次。

`go test -race ./...` 需要显式放宽超时：`internal/server` 单包在 `-race` 下约需 8-10 分钟，已接近 Go 默认的 10 分钟单包超时，整仓并行时会被 CPU 争用推过线并报 `panic: test timed out after 10m0s`。使用：

```bash
go test -race ./... -timeout 30m
```

超时的成因是既有测试基建成本，不是被测逻辑变慢：共享 fixture `apiTestServer` 每个用例都用**生产级 Argon2id 参数**创建 2-3 个账号，单次哈希约 1 秒，`-race` 下更慢，于是上百个 API 测试各自固定消耗约 7 秒。改进方向是给测试注入一套低成本的 Argon2 参数（只改 fixture，不改生产默认值），可把该包 `-race` 时间压到分钟级；此项属于独立的测试基建改造，需要单独的实施计划。

嵌入产物校验：前端改动后执行 `rsync -a --delete web/dist/ internal/server/web_dist/ && ./scripts/verify-web-embed.sh`，确保 Go embed 的静态资源与 `web/dist` 一致。
