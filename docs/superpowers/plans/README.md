# 实施计划索引

<!-- 本文件由 scripts/gen_doc_index.py 生成，请勿手工编辑。 -->

本目录保存 `AGENTS.md`「Plan 与 PR 要求」规定的实施计划，文件名固定为
`YYYY-MM-DD-<feature-name>.md`。计划是实现前的设计契约：实现完成后不重写正文，
需要修正时新增计划或在正文标注补记。规则权威来源是
[AGENTS.md](../../../AGENTS.md)，文档组织约定见
[文档规范](../../development/documentation.md)。

共 41 份计划，按日期倒序排列。

## 计划清单

| 日期 | 实施计划 | 关联规格 | 关联 PR / ADR |
| --- | --- | --- | --- |
| 2026-09-23 | [三端一键安装脚本 Implementation Plan](2026-09-23-one-click-install-scripts.md) | [规格：三端一键安装脚本](../specs/2026-09-23-one-click-install-scripts-design.md) | [PR：三端一键安装脚本（server / agent / client）](../../pull-requests/2026-09-23-one-click-install-scripts.md) |
| 2026-09-23 | [托管路由大响应体截断修复实施计划](2026-09-23-managed-route-response-truncation.md) | — | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md)<br>[PR：Managed-route large response truncation](../../pull-requests/2026-09-23-managed-route-response-truncation.md) |
| 2026-09-18 | [SSO, MFA, and device trust implementation plan (Phase A)](2026-09-18-sso-mfa-device-trust-implementation.md) | [规格：Enterprise capability roadmap design](../specs/2026-09-18-enterprise-capability-roadmap-design.md)<br>[规格：SSO, MFA, and device trust design (Phase …](../specs/2026-09-18-sso-mfa-device-trust-design.md) | [PR：Phase A: enterprise identity foundation (…](../../pull-requests/2026-09-18-sso-mfa-device-trust.md) |
| 2026-09-18 | [P1–P3 growth and product hardening implementation plan](2026-09-18-p1-p3-growth-product-hardening.md) | — | [PR：P1–P3 growth and product hardening](../../pull-requests/2026-09-18-p1-p3-growth-product-hardening.md) |
| 2026-09-17 | [P0/P1 Growth Acceleration Implementation Plan](2026-09-17-p0-p1-growth-acceleration-implementation.md) | [规格：P0/P1 Growth Acceleration](../specs/2026-09-17-p0-p1-growth-acceleration-design.md) | [PR：P0/P1 growth acceleration](../../pull-requests/2026-09-17-p0-p1-growth-acceleration.md) |
| 2026-09-17 | [GitHub Growth Optimization Implementation Plan](2026-09-17-github-growth-optimization.md) | [规格：GitHub Growth Optimization](../specs/2026-09-17-github-growth-optimization-design.md) | [PR：Improve GitHub growth presentation](../../pull-requests/2026-09-17-github-growth-optimization.md) |
| 2026-09-17 | [GitHub growth install and template plan](2026-09-17-github-growth-install-and-templates.md) | — | [PR：GitHub growth install and templates](../../pull-requests/2026-09-17-github-growth-install-and-templates.md) |
| 2026-09-13 | [ZMODEM 出站写入串行化修复实施计划](2026-09-13-zmodem-write-serialization.md) | — | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md) |
| 2026-09-13 | [ZMODEM 停滞处置策略与进度节流实施计划](2026-09-13-zmodem-stall-channel-policy.md) | — | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md) |
| 2026-09-13 | [ZMODEM Sentry 生命周期与停滞豁免实施计划](2026-09-13-zmodem-sentry-lifecycle-stall-policy.md) | — | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md) |
| 2026-09-13 | [WebSSH 死通道判定窗口与传输尾部关会话止损实施计划](2026-09-13-webssh-dead-channel-window.md) | — | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md) |
| 2026-09-13 | [WebSFTP 上传失败修复实施计划](2026-09-13-sftp-upload-partial-write.md) | — | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md) |
| 2026-09-13 | [托管路由 HTTP 代理入口（tp-*）Implementation Plan](2026-09-13-managed-route-http-proxy-entry-implementation.md) | [规格：托管路由 HTTP 代理入口（tp-*）](../specs/2026-09-13-managed-route-http-proxy-entry-design.md) | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md)<br>[PR：Managed-route HTTP proxy entry (tp-*)](../../pull-requests/2026-09-13-managed-route-http-proxy-entry.md) |
| 2026-09-12 | [WebSSH lrzsz(ZMODEM) 支持与凭据自动认证 实施计划](2026-09-12-webssh-zmodem-credential-auto-auth.md) | — | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md) |
| 2026-09-12 | [WebSSH 会话管理实施计划](2026-09-12-webssh-session-management.md) | — | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md) |
| 2026-09-12 | [WebSSH 大文件传输流控修复实施计划](2026-09-12-webssh-bulk-transfer-flow-control.md) | — | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md) |
| 2026-09-12 | [Admin WebSSH/SFTP Implementation Plan](2026-09-12-admin-webssh-sftp-implementation.md) | [规格：管理后台远程服务器与浏览器 SSH/SFTP](../specs/2026-09-12-admin-webssh-sftp-design.md) | [PR：Admin Remote Servers, WebSSH, and WebSFTP](../../pull-requests/2026-09-12-admin-webssh-sftp.md) |
| 2026-09-11 | [SOCKS5 网页首屏延迟优化 Implementation Plan](2026-09-11-socks5-web-page-latency-implementation.md) | [规格：SOCKS5 网页首屏延迟优化](../specs/2026-09-10-socks5-web-page-latency-design.md) | [PR：Reduce SOCKS5 web-page latency](../../pull-requests/2026-09-11-socks5-web-page-latency.md) |
| 2026-09-11 | [Client YAML 多 SOCKS5 入口实施计划](2026-09-11-client-yaml-multi-socks5.md) | — | — |
| 2026-09-11 | [Client Observability and Release Downloads Implementation Plan](2026-09-11-client-observability-release-downloads-implementation.md) | [规格：客户端运行观测与跨平台发布](../specs/2026-09-11-client-observability-release-design.md) | [PR：Expose client observability](../../pull-requests/2026-09-11-client-observability.md)<br>[PR：Publish platform release downloads](../../pull-requests/2026-09-11-platform-release-downloads.md) |
| 2026-09-11 | [Client Agent Session Pool Implementation Plan](2026-09-11-client-agent-session-pool.md) | [规格：Client Agent Session Pool](../specs/2026-09-11-client-agent-session-pool-design.md) | — |
| 2026-09-11 | [Agent Policy 逻辑删除、恢复与编辑强化实施计划](2026-09-11-agent-policy-lifecycle.md) | — | — |
| 2026-09-11 | [Agent Metadata 逻辑状态展示调整实施计划](2026-09-11-agent-metadata-logical-summary.md) | [规格：Logical Agent Connection Pool](../specs/2026-09-09-logical-agent-connection-pool-design.md) | — |
| 2026-09-10 | [Server 节点共享令牌与后台管理实施计划](2026-09-10-server-node-fleet-token-admin-implementation.md) | [规格：Server Node Fleet Token and Administration](../specs/2026-09-10-server-node-fleet-token-admin-design.md) | [PR：Server 节点共享令牌与后台管理](../../pull-requests/2026-09-10-server-node-fleet-token-admin.md) |
| 2026-09-10 | [Relay Endpoint 自动推导与明文模式实施计划](2026-09-10-relay-endpoint-autodetect-plaintext.md) | — | [PR：Server 节点共享令牌与后台管理](../../pull-requests/2026-09-10-server-node-fleet-token-admin.md) |
| 2026-09-10 | [Agent Policy 后台展示与编辑入口实施计划](2026-09-10-agent-policy-admin-ui.md) | — | — |
| 2026-09-09 | [Token Scope and Managed Route Editing Implementation Plan](2026-09-09-token-scope-route-edit-audit-implementation.md) | [规格：Token 范围与托管路由修改补记规格](../specs/2026-09-09-token-scope-route-edit-audit-design.md) | [PR：edit token scope and managed routes](../../pull-requests/2026-09-09-token-scope-route-edit-audit.md) |
| 2026-09-09 | [Managed Route Domain and HTTPS Implementation Plan](2026-09-09-managed-route-domain-https-implementation.md) | — | [PR：Support domain-only and HTTPS upstream se…](../../pull-requests/2026-09-09-managed-route-domain-https.md) |
| 2026-09-09 | [Logical Agent Connection Pool Implementation Plan](2026-09-09-logical-agent-connection-pool-implementation.md) | [规格：Logical Agent Connection Pool](../specs/2026-09-09-logical-agent-connection-pool-design.md) | [PR：逻辑 Agent 连接池](../../pull-requests/2026-09-09-logical-agent-connection-pool.md) |
| 2026-09-09 | [Cross-Server Agent Connection Management Implementation Plan](2026-09-09-cross-server-agent-connection-management.md) | [规格：Cross-Server Agent Connection Management](../specs/2026-09-09-cross-server-agent-connection-management-design.md) | [PR：Cross-Server Agent Connection Management](../../pull-requests/2026-09-09-cross-server-agent-connection-management.md) |
| 2026-09-09 | [Client SOCKS5 CONNECT Implementation Plan](2026-09-09-client-socks5-implementation.md) | [规格：Client SOCKS5 CONNECT](../specs/2026-09-09-client-socks5-design.md) | [PR：Client SOCKS5 CONNECT](../../pull-requests/2026-09-09-client-socks5.md) |
| 2026-09-09 | [Audit Log Ordering Implementation Plan](2026-09-09-audit-log-ordering-implementation.md) | [规格：Token 范围与托管路由修改补记规格](../specs/2026-09-09-token-scope-route-edit-audit-design.md) | [PR：edit token scope and managed routes](../../pull-requests/2026-09-09-token-scope-route-edit-audit.md) |
| 2026-09-09 | [Audit Log Filtering Implementation Plan](2026-09-09-audit-log-filtering-implementation.md) | [规格：Token 范围与托管路由修改补记规格](../specs/2026-09-09-token-scope-route-edit-audit-design.md) | [PR：edit token scope and managed routes](../../pull-requests/2026-09-09-token-scope-route-edit-audit.md) |
| 2026-09-08 | [TunnelMesh 管理后台国际化与账号管理 Implementation Plan](2026-09-08-admin-i18n-account-management.md) | [规格：TunnelMesh 管理后台国际化、账号管理与 UI](../specs/2026-09-08-admin-i18n-account-management-design.md) | — |
| 2026-09-07 | [Protocol Modules and Traceroute Implementation Plan](2026-09-07-protocol-modules-traceroute-implementation.md) | [规格：Protocol Modules and Secure Traceroute](../specs/2026-09-07-protocol-modules-traceroute-design.md) | — |
| 2026-09-06 | [TunnelMesh Platform Implementation Plan](2026-09-06-tunnelmesh-platform-implementation.md) | [规格：TunnelMesh 平台架构](../specs/2026-09-06-tunnelmesh-platform-design.md) | — |
| 2026-09-06 | [Scoped Token and Connection Authorization Implementation Plan](2026-09-06-scoped-token-auth-implementation.md) | [规格：TunnelMesh 安全授权、稳定性与发布运维](../specs/2026-09-06-security-observability-release-design.md) | [ADR：Separate scoped service credentials from …](../../architecture/adr/0001-scoped-service-tokens.md) |
| 2026-09-06 | [Observability and Protocol Enhancements Implementation Plan](2026-09-06-observability-protocol-implementation.md) | [规格：TunnelMesh 可观测性与代理协议增强](../specs/2026-09-06-observability-protocol-design.md) | — |
| 2026-09-06 | [Cross-Platform Release and Operations Implementation Plan](2026-09-06-cross-platform-release-ops-implementation.md) | [规格：TunnelMesh 安全授权、稳定性与发布运维](../specs/2026-09-06-security-observability-release-design.md) | — |
| 2026-09-06 | [Agent Observability and Network Stability Implementation Plan](2026-09-06-agent-observability-stability-implementation.md) | [规格：TunnelMesh 安全授权、稳定性与发布运维](../specs/2026-09-06-security-observability-release-design.md) | — |
| 2026-09-06 | [Agent Metadata and SSH Access Implementation Plan](2026-09-06-agent-metadata-ssh-implementation.md) | [规格：Agent 主机信息上报与 SSH 访问](../specs/2026-09-06-agent-metadata-ssh-design.md) | — |

## 补记计划

以下计划按 AGENTS.md 的紧急修复条款先止损、后补记，正文已标注“补记计划”：

- [托管路由大响应体截断修复实施计划](2026-09-23-managed-route-response-truncation.md)（2026-09-23）
- [ZMODEM 出站写入串行化修复实施计划](2026-09-13-zmodem-write-serialization.md)（2026-09-13）
- [ZMODEM 停滞处置策略与进度节流实施计划](2026-09-13-zmodem-stall-channel-policy.md)（2026-09-13）
- [ZMODEM Sentry 生命周期与停滞豁免实施计划](2026-09-13-zmodem-sentry-lifecycle-stall-policy.md)（2026-09-13）
- [WebSSH 死通道判定窗口与传输尾部关会话止损实施计划](2026-09-13-webssh-dead-channel-window.md)（2026-09-13）
- [WebSFTP 上传失败修复实施计划](2026-09-13-sftp-upload-partial-write.md)（2026-09-13）

## 新增计划的步骤

1. 按 `YYYY-MM-DD-<feature-name>.md` 命名，正文包含 AGENTS.md 要求的全部小节。
2. 若已有设计规格或后续提交了 PR，在计划正文中直接链接对应文件，交叉引用会被本索引自动识别。
3. 执行 `python3 scripts/gen_doc_index.py` 重新生成本索引，并检查 `git diff`。
