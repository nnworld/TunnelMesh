# 设计规格索引

<!-- 本文件由 scripts/gen_doc_index.py 生成，请勿手工编辑。 -->

本目录保存实现前的设计规格（架构决策、接口契约、协议演进、安全边界），文件名固定为
`YYYY-MM-DD-<feature-name>-design.md`。规格记录“为什么这样设计”，与记录“怎么落地”的
[实施计划](../plans/README.md)分开维护。规则权威来源是
[AGENTS.md](../../../AGENTS.md)。

共 16 份规格，按日期倒序排列。

## 规格清单

| 日期 | 设计规格 | 关联计划 | 关联 PR / ADR |
| --- | --- | --- | --- |
| 2026-09-13 | [托管路由 HTTP 代理入口（tp-*）设计](2026-09-13-managed-route-http-proxy-entry-design.md) | [计划：Client SOCKS5 CONNECT](../plans/2026-09-09-client-socks5-implementation.md)<br>[计划：托管路由 HTTP 代理入口（tp-*）](../plans/2026-09-13-managed-route-http-proxy-entry-implementation.md) | — |
| 2026-09-12 | [管理后台远程服务器与浏览器 SSH/SFTP 设计](2026-09-12-admin-webssh-sftp-design.md) | [计划：Admin WebSSH/SFTP](../plans/2026-09-12-admin-webssh-sftp-implementation.md) | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md) |
| 2026-09-11 | [客户端运行观测与跨平台发布设计](2026-09-11-client-observability-release-design.md) | — | — |
| 2026-09-11 | [Client Agent Session Pool Design](2026-09-11-client-agent-session-pool-design.md) | [计划：Client Agent Session Pool](../plans/2026-09-11-client-agent-session-pool.md) | — |
| 2026-09-10 | [SOCKS5 网页首屏延迟优化设计](2026-09-10-socks5-web-page-latency-design.md) | [计划：SOCKS5 网页首屏延迟优化](../plans/2026-09-11-socks5-web-page-latency-implementation.md) | [PR：Reduce SOCKS5 web-page latency](../../pull-requests/2026-09-11-socks5-web-page-latency.md) |
| 2026-09-10 | [Server Node Fleet Token and Administration Design](2026-09-10-server-node-fleet-token-admin-design.md) | [计划：Server 节点共享令牌与后台管理](../plans/2026-09-10-server-node-fleet-token-admin-implementation.md) | [PR：Server 节点共享令牌与后台管理](../../pull-requests/2026-09-10-server-node-fleet-token-admin.md) |
| 2026-09-09 | [Token 范围与托管路由修改补记规格](2026-09-09-token-scope-route-edit-audit-design.md) | [计划：Token Scope and Managed Route Editing](../plans/2026-09-09-token-scope-route-edit-audit-implementation.md) | [PR：edit token scope and managed routes](../../pull-requests/2026-09-09-token-scope-route-edit-audit.md) |
| 2026-09-09 | [Logical Agent Connection Pool Design](2026-09-09-logical-agent-connection-pool-design.md) | [计划：Logical Agent Connection Pool](../plans/2026-09-09-logical-agent-connection-pool-implementation.md) | [PR：逻辑 Agent 连接池](../../pull-requests/2026-09-09-logical-agent-connection-pool.md) |
| 2026-09-09 | [Cross-Server Agent Connection Management Design](2026-09-09-cross-server-agent-connection-management-design.md) | [计划：Cross-Server Agent Connection Management](../plans/2026-09-09-cross-server-agent-connection-management.md) | [PR：Cross-Server Agent Connection Management](../../pull-requests/2026-09-09-cross-server-agent-connection-management.md) |
| 2026-09-09 | [Client SOCKS5 CONNECT Design](2026-09-09-client-socks5-design.md) | [计划：Client SOCKS5 CONNECT](../plans/2026-09-09-client-socks5-implementation.md) | [PR：Client SOCKS5 CONNECT](../../pull-requests/2026-09-09-client-socks5.md) |
| 2026-09-08 | [TunnelMesh 管理后台国际化、账号管理与 UI 设计](2026-09-08-admin-i18n-account-management-design.md) | [计划：TunnelMesh 管理后台国际化与账号管理](../plans/2026-09-08-admin-i18n-account-management.md) | — |
| 2026-09-07 | [Protocol Modules and Secure Traceroute Design](2026-09-07-protocol-modules-traceroute-design.md) | [计划：Protocol Modules and Traceroute](../plans/2026-09-07-protocol-modules-traceroute-implementation.md) | — |
| 2026-09-06 | [TunnelMesh 平台架构设计](2026-09-06-tunnelmesh-platform-design.md) | [计划：TunnelMesh Platform](../plans/2026-09-06-tunnelmesh-platform-implementation.md) | — |
| 2026-09-06 | [TunnelMesh 安全授权、稳定性与发布运维设计](2026-09-06-security-observability-release-design.md) | — | — |
| 2026-09-06 | [TunnelMesh 可观测性与代理协议增强设计](2026-09-06-observability-protocol-design.md) | [计划：Observability and Protocol Enhancements](../plans/2026-09-06-observability-protocol-implementation.md) | — |
| 2026-09-06 | [Agent 主机信息上报与 SSH 访问设计](2026-09-06-agent-metadata-ssh-design.md) | [计划：Agent Metadata and SSH Access](../plans/2026-09-06-agent-metadata-ssh-implementation.md) | — |

## 新增规格的步骤

1. 按 `YYYY-MM-DD-<feature-name>-design.md` 命名，写明背景、决策、替代方案和迁移影响。
2. 在正文中链接对应的实施计划或 PR，交叉引用会被本索引自动识别。
3. 执行 `python3 scripts/gen_doc_index.py` 重新生成本索引。
