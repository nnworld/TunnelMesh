# PR 记录索引

<!-- 本文件由 scripts/gen_doc_index.py 生成，请勿手工编辑。 -->

本目录保存 `AGENTS.md` 要求的 PR 描述文档，文件名固定为 `YYYY-MM-DD-<feature-name>.md`，
内容包含标题、目标分支、摘要、用户影响、API/Schema/配置影响、安全影响、测试证据、
发布与回滚步骤、Reviewer 关注点和集成状态。

共 22 份记录，按日期倒序排列。

## 记录清单

| 日期 | PR 记录 | 关联计划 | 关联规格 / ADR |
| --- | --- | --- | --- |
| 2026-09-23 | [三端一键安装脚本（server / agent / client）](2026-09-23-one-click-install-scripts.md) | [计划：三端一键安装脚本](../superpowers/plans/2026-09-23-one-click-install-scripts.md) | [规格：三端一键安装脚本](../superpowers/specs/2026-09-23-one-click-install-scripts-design.md) |
| 2026-09-23 | [Managed-route large response truncation](2026-09-23-managed-route-response-truncation.md) | [计划：托管路由大响应体截断修复](../superpowers/plans/2026-09-23-managed-route-response-truncation.md) | — |
| 2026-09-23 | [Managed-route hosts shadowed by control-plane path prefixes](2026-09-23-managed-route-reserved-path-shadowing.md) | [计划：托管路由被控制面保留路径遮蔽修复](../superpowers/plans/2026-09-23-managed-route-reserved-path-shadowing.md) | — |
| 2026-09-23 | [Agent list connectivity status and empty metadata view](2026-09-23-agent-online-status-and-empty-metadata.md) | [计划：代理节点在线状态与空元数据修复](../superpowers/plans/2026-09-23-agent-online-status-and-empty-metadata.md) | — |
| 2026-09-18 | [Phase A: enterprise identity foundation (SSO, MFA, device trust)](2026-09-18-sso-mfa-device-trust.md) | [计划：SSO, MFA, and device trust implementation…](../superpowers/plans/2026-09-18-sso-mfa-device-trust-implementation.md) | [规格：SSO, MFA, and device trust design (Phase …](../superpowers/specs/2026-09-18-sso-mfa-device-trust-design.md) |
| 2026-09-18 | [P1–P3 growth and product hardening](2026-09-18-p1-p3-growth-product-hardening.md) | [计划：P1–P3 growth and product hardening implem…](../superpowers/plans/2026-09-18-p1-p3-growth-product-hardening.md) | — |
| 2026-09-17 | [P0/P1 growth acceleration](2026-09-17-p0-p1-growth-acceleration.md) | [计划：P0/P1 Growth Acceleration](../superpowers/plans/2026-09-17-p0-p1-growth-acceleration-implementation.md) | [规格：P0/P1 Growth Acceleration](../superpowers/specs/2026-09-17-p0-p1-growth-acceleration-design.md) |
| 2026-09-17 | [PR: Improve GitHub growth presentation](2026-09-17-github-growth-optimization.md) | [计划：GitHub Growth Optimization](../superpowers/plans/2026-09-17-github-growth-optimization.md) | [规格：GitHub Growth Optimization](../superpowers/specs/2026-09-17-github-growth-optimization-design.md) |
| 2026-09-17 | [GitHub growth install and templates](2026-09-17-github-growth-install-and-templates.md) | [计划：GitHub growth install and template plan](../superpowers/plans/2026-09-17-github-growth-install-and-templates.md) | — |
| 2026-09-13 | [Managed-route HTTP proxy entry (tp-*)](2026-09-13-managed-route-http-proxy-entry.md) | [计划：托管路由 HTTP 代理入口（tp-*）](../superpowers/plans/2026-09-13-managed-route-http-proxy-entry-implementation.md) | [规格：托管路由 HTTP 代理入口（tp-*）](../superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md) |
| 2026-09-12 | [Admin Remote Servers, WebSSH, and WebSFTP](2026-09-12-admin-webssh-sftp.md) | [计划：Admin WebSSH/SFTP](../superpowers/plans/2026-09-12-admin-webssh-sftp-implementation.md)<br>[计划：WebSSH 大文件传输流控修复](../superpowers/plans/2026-09-12-webssh-bulk-transfer-flow-control.md)<br>[计划：WebSSH 会话管理](../superpowers/plans/2026-09-12-webssh-session-management.md)<br>[计划：WebSSH lrzsz(ZMODEM) 支持与凭据自动认证](../superpowers/plans/2026-09-12-webssh-zmodem-credential-auto-auth.md)<br>[计划：WebSFTP 上传失败修复](../superpowers/plans/2026-09-13-sftp-upload-partial-write.md)<br>[计划：WebSSH 死通道判定窗口与传输尾部关会话止损](../superpowers/plans/2026-09-13-webssh-dead-channel-window.md)<br>[计划：ZMODEM Sentry 生命周期与停滞豁免](../superpowers/plans/2026-09-13-zmodem-sentry-lifecycle-stall-policy.md)<br>[计划：ZMODEM 停滞处置策略与进度节流](../superpowers/plans/2026-09-13-zmodem-stall-channel-policy.md)<br>[计划：ZMODEM 出站写入串行化修复](../superpowers/plans/2026-09-13-zmodem-write-serialization.md) | [规格：管理后台远程服务器与浏览器 SSH/SFTP](../superpowers/specs/2026-09-12-admin-webssh-sftp-design.md) |
| 2026-09-11 | [PR: Reduce SOCKS5 web-page latency](2026-09-11-socks5-web-page-latency.md) | [计划：SOCKS5 网页首屏延迟优化](../superpowers/plans/2026-09-11-socks5-web-page-latency-implementation.md) | [规格：SOCKS5 网页首屏延迟优化](../superpowers/specs/2026-09-10-socks5-web-page-latency-design.md) |
| 2026-09-11 | [PR: Publish platform release downloads](2026-09-11-platform-release-downloads.md) | — | — |
| 2026-09-11 | [PR: Expose client observability](2026-09-11-client-observability.md) | — | — |
| 2026-09-10 | [PR：Server 节点共享令牌与后台管理](2026-09-10-server-node-fleet-token-admin.md) | [计划：Server 节点共享令牌与后台管理](../superpowers/plans/2026-09-10-server-node-fleet-token-admin-implementation.md) | [规格：Server Node Fleet Token and Administration](../superpowers/specs/2026-09-10-server-node-fleet-token-admin-design.md) |
| 2026-09-09 | [feat(admin): edit token scope and managed routes](2026-09-09-token-scope-route-edit-audit.md) | [计划：Token Scope and Managed Route Editing](../superpowers/plans/2026-09-09-token-scope-route-edit-audit-implementation.md) | [规格：Token 范围与托管路由修改补记规格](../superpowers/specs/2026-09-09-token-scope-route-edit-audit-design.md) |
| 2026-09-09 | [PR: Support domain-only and HTTPS upstream services for managed routes](2026-09-09-managed-route-domain-https.md) | [计划：Managed Route Domain and HTTPS](../superpowers/plans/2026-09-09-managed-route-domain-https-implementation.md) | — |
| 2026-09-09 | [PR：逻辑 Agent 连接池](2026-09-09-logical-agent-connection-pool.md) | [计划：Logical Agent Connection Pool](../superpowers/plans/2026-09-09-logical-agent-connection-pool-implementation.md) | [规格：Logical Agent Connection Pool](../superpowers/specs/2026-09-09-logical-agent-connection-pool-design.md) |
| 2026-09-09 | [PR: Cross-Server Agent Connection Management](2026-09-09-cross-server-agent-connection-management.md) | [计划：Cross-Server Agent Connection Management](../superpowers/plans/2026-09-09-cross-server-agent-connection-management.md) | [规格：Cross-Server Agent Connection Management](../superpowers/specs/2026-09-09-cross-server-agent-connection-management-design.md) |
| 2026-09-09 | [Client SOCKS5 CONNECT](2026-09-09-client-socks5.md) | [计划：Client SOCKS5 CONNECT](../superpowers/plans/2026-09-09-client-socks5-implementation.md) | [规格：Client SOCKS5 CONNECT](../superpowers/specs/2026-09-09-client-socks5-design.md) |
| 2026-09-09 | [Client Proxy Remote Validation](2026-09-09-client-proxy-remote-validation.md) | — | — |
| 2026-09-09 | [Client Standard HTTP Proxy](2026-09-09-client-http-proxy.md) | — | — |

## 新增记录的步骤

1. 按 `YYYY-MM-DD-<feature-name>.md` 命名，覆盖 AGENTS.md 列出的全部小节。
2. 在正文中链接对应的实施计划与设计规格，交叉引用会被本索引自动识别。
3. 执行 `python3 scripts/gen_doc_index.py` 重新生成本索引。
