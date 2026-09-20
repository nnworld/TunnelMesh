# TunnelMesh 文档

按角色分类的文档索引。项目总览、架构图、快速开始和能力矩阵见仓库根目录的
[README.md](../README.md)（英文）和 [README.zh-CN.md](../README.zh-CN.md)（中文）；
工程规范的权威来源是 [AGENTS.md](../AGENTS.md)。

## 文档地图

| 目录 | 面向 | 内容 |
| --- | --- | --- |
| [`user-guide/`](user-guide/) | 使用者 | Client、Agent、管理后台、单点登录与两步验证、托管路由、HTTP 代理入口、SSH over WebSocket |
| [`deployment/`](deployment/) | 部署者 | Docker、前端构建、Nginx、OpenResty 代理入口、systemd/launchd/Windows Service、发行打包 |
| [`operations/`](operations/) | 运维 | 配置、Schema 升级、relay mTLS、连接池、可观测性、探针、日志、SLO、容量、排障 |
| [`architecture/`](architecture/) | 架构 | 架构概览、集群架构、ADR |
| [`protocol/`](protocol/) | 协议实现 | WebSocket frame、代理协议模块 |
| [`api/`](api/) | 接口 | `openapi.yaml` |
| [`en/`](en/) | 英文读者 | 生产部署、安全加固、三端使用入口、SSO 与 MFA 指南 |
| [`community/`](community/) | 社区与增长 | GitHub 元数据、技术文章、分发计划 |
| [`index.md`](index.md) | 文档站入口 | GitHub Pages 双语文档导航 |
| [`assets/`](assets/) | 所有读者 | 管理后台截图、演示 GIF、社交卡片 |
| [`development/`](development/) | 贡献者 | 测试与验证、文档规范、变更记录索引 |
| [`superpowers/plans/`](superpowers/plans/)、[`superpowers/specs/`](superpowers/specs/) | 贡献者 | 实施计划与设计规格（时点记录） |
| [`pull-requests/`](pull-requests/) | 贡献者 | PR 描述记录（时点记录） |

目录职责、命名约定和索引维护规则见[文档规范](development/documentation.md)。

可直接使用的部署产物（systemd unit、launchd/WinSW 模板、安装脚本、Prometheus 配置与规则、
Grafana Dashboard）不在 `docs/` 下，而在仓库根目录的 [`deploy/`](../deploy/README.md)。
`docs/` 只写操作步骤和参数说明，`deploy/` 只放产物，两边不重复内容。

## 使用者

- [Client 使用帮助](user-guide/client.md)：TCP/UDP/HTTP 转发、SOCKS5、HTTP 代理、发布、`proxy tcp`、隧道管理
- [Agent 使用帮助](user-guide/agent.md)：注册、连接池运行、受控 metadata 上报、网络与 TLS 要求
- [Server 管理后台](user-guide/server-admin.md)：Dashboard、账号与语言、Agent 列表与详情、Agent Policy、审计日志、角色与 Service Token
  - [单点登录与两步验证](user-guide/sso-and-mfa.md)：OIDC 提供商配置与 role mapping、TOTP 绑定与恢复码、受信任设备、认证策略、按 `data.error` 归类的排障表（英文版见 [SSO and MFA](en/user-guide/sso-and-mfa.md)）
  - [远程服务器与浏览器 SSH/SFTP](user-guide/server-admin.md#远程服务器与浏览器-sshsftp)
  - [Client 运行观测与连接管理](user-guide/server-admin.md#client-运行观测与连接管理)
  - [Server 节点与共享令牌](user-guide/server-admin.md#server-节点)
  - [发行管理](user-guide/server-admin.md#发行管理)
- [托管 HTTP 路由](user-guide/managed-http-route.md)：显式路由、通配域名、HTTPS 上游
- [HTTP 代理入口（tp-*）](user-guide/http-proxy-entry.md)：把 `https://tp-<name>.<domain>` 填进浏览器或系统代理，无需安装 client
- [SSH / websocat TCP 代理](user-guide/tcp-over-websocket-ssh.md)：`ProxyCommand`、stdio 字节桥

## 部署

- [Docker 部署](deployment/docker.md)、[docker-compose.local.yml](../docker-compose.local.yml)、[docker-compose.cluster.yml](../docker-compose.cluster.yml)
- [管理后台前端构建与部署](deployment/frontend.md)：embed 模式与 Nginx 独立静态文件模式
- [Nginx/WSS 推荐配置](deployment/nginx.md)：`/api/`、`/ws/*` 反代优先级与 Upgrade 透传
- [OpenResty tp-* 代理入口](deployment/openresty-proxy-entry.md)：模板渲染、镜像构建、容量评估、reload 影响与回滚
- [跨平台可执行文件打包](deployment/binary-release.md)：构建矩阵、`SHA256SUMS`、`manifest.json`
- [Linux systemd 安装](deployment/linux-systemd.md)、[macOS launchd 安装](deployment/macos-launchd.md)、[Windows Service 安装](deployment/windows-service.md)
- [部署产物清单](../deploy/README.md)：`deploy/` 下每个文件的用途、模板占位符约定和发布归档布局

## 运维

**配置**

- [配置说明](operations/configuration.md)：配置模型、优先级、密钥注入、`auto-init`、`security.auth`（SSO / MFA / 受信任设备）与 `server.trusted_proxies`
- [Server / Agent / Client 配置示例](operations/config-examples.md)：单机 SQLite、集群 MySQL、集群 etcd、启用 SSO 与 MFA 的集群示例
- [Client 全协议与连接池配置示例](operations/client-configuration-examples.md)

**升级与集群**

- [Schema 升级与回滚](operations/schema-upgrades.md)：增量链校验、锁表影响、5 分钟止损
- [Relay mTLS 证书生成与配置](operations/relay-mtls.md)：`scripts/gen-relay-certs.sh` 快速签发、手工 openssl 流程、SAN 要求与轮换
- [逻辑 Agent 连接池](operations/connection-pool.md)

**观测与排障**

- [可观测性与统一 Grafana Dashboard](operations/observability.md)：连接、stream、探针、WebSSH、tp-* 代理入口与身份认证指标及告警阈值
- Prometheus 抓取示例与规则见 [deploy/prometheus](../deploy/prometheus)，Dashboard 与 provisioning 见 [deploy/grafana](../deploy/grafana)
- [全链路网络探针](operations/network-probes.md)：逻辑 traceroute 与 TCP/HTTP/UDP 探针
- [日志位置与查看方式](operations/logging.md)
- [故障排查](operations/troubleshooting.md)
- [SLO](operations/slo.md)、[容量与压测](operations/capacity.md)
- [项目完整性清单](operations/completeness-checklist.md)：已交付能力与显式 deferred 项

## 架构与协议

- [架构概览](architecture/overview.md)、[集群架构](architecture/cluster.md)
- [架构决策记录（ADR）索引](architecture/adr/README.md)
- [WebSocket 协议](protocol/websocket.md)：frame、能力协商、流控、UDP association、GOAWAY/drain
- [代理协议模块](protocol/proxy-modules.md)：HTTP CONNECT、SOCKS5、PROXY protocol v2
- [OpenAPI](api/openapi.yaml)

## 开发与变更记录

- [开发文档入口](development/README.md)
- [测试与验证](development/testing.md)：三层验证、`-race` 超时、嵌入产物校验
- [文档规范](development/documentation.md)
- [实施计划索引](superpowers/plans/README.md)、[设计规格索引](superpowers/specs/README.md)、[PR 记录索引](pull-requests/README.md)

## 模式选择

- 本地模式使用 SQLite，适合单机试用和小规模部署。
- 集群模式使用 MySQL 管理数据；注册发现默认使用 MySQL lease，也可以切换到 etcd。

所有部署都应先执行 `check-config`，再执行 `run`。生产环境建议通过环境变量或外部配置文件注入
敏感配置。公网入口以 HTTP/HTTPS/WSS 为主；内嵌 VPN 网关启用后会额外监听一个公网 UDP 端口（[ADR 0002](architecture/adr/0002-public-ingress-and-embedded-vpn.md)，实施中，当前版本尚未提供）。

## 英文与社区

- [Documentation site](https://nnworld.github.io/TunnelMesh/)：GitHub Pages 文档入口
- [English documentation](en/README.md)：生产部署、安全加固、Agent、Client、Server 管理
- [SSO and MFA](en/user-guide/sso-and-mfa.md)：OIDC 单点登录、TOTP 两步验证、受信任设备与认证策略的英文完整指南
- [Why TunnelMesh needs a control plane](community/why-tunnelmesh-needs-a-control-plane.md)：技术文章与产品取舍
- [Comparison with other tunneling models](community/comparison.md)：不同隧道模型的边界与取舍
- [Roadmap](community/roadmap.md)：当前、下一步和长期方向
- [Distribution plan](community/distribution-plan.md)：三日发布序列与渠道策略
- [GitHub repository metadata](community/github-metadata.md)：About、Topics、Website 与社交卡片设置
- [Visual assets](assets/README.md)：截图、GIF 与社交卡片的来源和脱敏检查
