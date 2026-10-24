# 后台管理列表体验与 Client/Agent 链路加固

## Title

feat(platform): harden client agent runtime

## Target branch

`main`（实现分支 `codex/admin-console-list-baseline`）。

## Summary

本次合并包含用户已确认纳入的后台管理页面改造，以及 Client/Agent 便携性与容错加固：

- 后台管理列表统一支持 cursor 分页、默认倒序排序、允许列排序、常用字段模糊查询和多选筛选；抽取列表页壳、筛选栏与公共分页状态，避免页面出现超长表单。
- Windows Client 托盘默认使用 `%LOCALAPPDATA%\\TunnelMesh` 的用户身份目录；服务配置继续使用 `%ProgramData%\\TunnelMesh`，设置页和文档明确两者边界。
- Client Runtime 支持单隧道监听异常的退避重建，托盘在非主动退出时监督并重启 Runtime，统计中暴露重启次数；CLI 与托盘仍按配置文件共享锁互斥。
- Agent/Client WebSocket 统一支持直连、HTTP CONNECT、SOCKS5 和系统代理；代理密码仅从环境变量或权限收紧的文件读取。
- 远端 `ws://` 默认拒绝，回环地址仍允许，兼容场景必须显式设置 `allow_insecure_transport: true`；生产示例统一使用 `wss://`。

## User impact

- 管理员在所有主要资源页可直接分页、排序和组合筛选，列表请求继续使用 cursor，不改变既有 API 响应结构。
- Windows 普通用户运行托盘不再因写入 `%ProgramData%` 失败；若需与 Windows 服务共用配置，必须在托盘设置中显式指向同一配置文件。
- Client/Agent 在代理网络和短暂本地监听故障下具备更好的恢复能力；远端明文 WebSocket 配置会在启动前给出明确错误。
- 现有 CLI 命令、隧道配置模型和 macOS 托盘行为保持兼容；新增传输配置字段为 additive。

## API, schema, and configuration impact

- 列表 API 新增排序/筛选查询参数和统一校验，仍返回 `{code,msg,data}` 与 cursor 分页；服务端使用 allowlist 防止排序字段进入 SQL。
- 新增数据库迁移 `v0016_to_v0017`，同步更新 `migrations/ddl.sql`、SchemaVersion 和迁移测试。
- 新增 `agent.transport` 与 `client.transport`：`proxy_mode`、`proxy_url`、`proxy_username`、`proxy_password_env`、`proxy_password_file`、`allow_insecure_transport`。
- 未新增敏感字段明文持久化；Token、代理密码和现有密钥字段仍遵循原有脱敏/外部注入规则。

## Security impact

- Server 端排序字段、筛选枚举和资源范围均由服务端 allowlist/权限过滤控制。
- WebSocket 出站代理不会记录完整凭据 URL；代理密码文件要求无 group/other 权限。
- 非回环远端 `ws://` 默认不可用，降低 Client/Agent 在不加密链路上泄露 Token 的风险。
- Windows 用户配置和服务配置分离，避免普通用户意外扩大机器级 Token 的 ACL 范围。

## Tests run

已在 macOS 本机执行并通过：

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web && npm test -- --run && npm run build
cd ../web-tray && npm test -- --run && npm run build
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags tray ./cmd/tunnelmesh-client-tray
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -tags tray ./cmd/tunnelmesh-client-tray
GOOS=windows GOARCH=amd64 go vet -tags tray ./...
```

Windows 交叉目标的 `go test` 未在 macOS 执行 `.exe`，因此只采用交叉编译和目标平台 `go vet`；Windows 真机 GUI 冒烟仍需在 Windows 环境验证。

## Release steps

1. 合并到 `main` 后按现有 CI 构建管理后台嵌入资源和托盘资源。
2. 发布前执行 SQLite/MySQL 迁移链校验，确认 `schema_meta.version` 到目标版本。
3. Windows 托盘发布继续使用现有 NSIS/zip 发行流程，并在真机验证用户目录、服务目录、代理和 WebView2 行为。

## Rollback steps

- 本次包含数据库 Schema 增量迁移，回滚优先回退应用代码并保留向后兼容的新增结构；禁止直接猜测反向 SQL。
- 如 Runtime 自动恢复出现异常，可先停止托盘 Runtime，使用 CLI `client run` 作为临时运行路径，再回滚对应应用版本。
- 如代理或明文 WebSocket 配置导致兼容性问题，可临时使用直连和 `wss://`，不回退权限校验。
- 后台页面可独立回退前端资源；API 新增查询参数对旧客户端保持兼容。

## Reviewer focus

1. 列表 cursor 与排序字段 allowlist 是否在所有资源 Repository 中保持一致，且权限过滤发生在分页语义内。
2. Runtime 重建、主动 Stop、锁冲突和配置错误之间的边界，以及关闭路径的资源释放。
3. HTTP CONNECT/SOCKS5/system 代理在取消、TLS、凭据来源和日志脱敏方面的行为。
4. Windows 用户配置目录与服务配置目录是否始终保持可解释且不发生静默迁移。
5. `v0016_to_v0017` 在 SQLite 与 MySQL 下的升级顺序和可重试性。

## Integration status

- 计划：`docs/superpowers/plans/2026-10-24-client-agent-portability-resilience.md`
- 后台页面计划：`docs/superpowers/plans/2026-10-24-admin-console-redesign.md`
- 目标分支：`main`
- 本次包含当前分支既有未提交的后台页面改动。
