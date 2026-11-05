# Client 缩容误终止会话池导致服务静默退出

- 标题：修复连接池缩容被当作链路终止故障导致的 client 静默 0 退出，并收紧 systemd 拉起策略
- 目标分支：`main`
- 相关记录：[实施计划](../superpowers/plans/2026-11-05-client-session-pool-silent-exit.md)

## 摘要

一台生产 Client 的四条隧道在 12:03 全部消失，systemd 显示 `inactive (dead)`、`Result=success`、`NRestarts=0`：进程以 0 退出，
`Restart=on-failure` 因此不认为需要拉起，而 `enabled` 只管开机自启。根因分三层：

1. `internal/client/session_pool.go` 的 `evaluate` 缩容时只取消被选中的 slot，而 slot 的 runner 无法区分“缩容取消”和“链路故障”
   （两条路径都会取消它的上下文），于是把 `context.Canceled` 投递给连接池的终止通道，`cancelAll()` 连带拆掉本应存活的会话。
2. `internal/client/runtime.go` 的 `runRoles` 用 `isContextErr` 丢弃这类错误后返回 `ctx.Err()`；此时运行上下文并未取消，于是整个 run 上报成功。
3. `internal/supervisor` 见 worker `ExitOK` 即“跟随正常结束”，主进程 0 退出，systemd 无失败可依据。

修复同样分三层：slot 记录“主动缩容”标记，只有非缩容的 runner 错误才能终止连接池；`runRoles` 在无人要求停止而角色已退出时返回新的
`ErrRunEndedUnexpectedly`，使进程非 0 退出并让 supervisor 走既有退避重启分支；交付的 systemd 单元对 client/agent 改用 `Restart=always`，
不再把退出码 0 当作“自愿结束”的证据。触发需要公网扫描造成 burst→idle 抖动，现场日志中两次同签名退出都与批量
`client session closed` 相隔数十秒内，且 supervisor 从未记录 `worker exited; restarting`。

## 用户影响

- 配置了 `client.connections.max > min` 的 Client/Agent 不再因为缩容而整体失联：隧道在扩缩容后继续服务，进程在异常结束时非 0 退出并被自动拉起。
- 默认配置（`connections.max=1`）行为不变，永不缩容，因此不受该缺陷影响，也不会感知本次改动。
- 运维侧：`systemctl stop`/`restart` 仍是停止任务，`Restart=always` 不会把它们顶回去；`78`（配置错误）与 `77`（凭据被永久拒绝）依旧不重启。
- 无接口、Web 管理后台与数据库变更。

## API / Schema / 配置影响

- 无 API 与 OpenAPI 变更，无 Schema 版本变更，无新增或废弃配置项。
- 新增导出错误 `client.ErrRunEndedUnexpectedly`，仅用于日志与退出码推导，不进入协议载荷。
- 单元模板变更：`deploy/systemd/tunnelmesh-{client,agent}.service`、`deploy/systemd-user/tunnelmesh-{client,agent}.service` 的 `Restart` 由 `on-failure` 改为 `always`；server 单元保持 `on-failure`。
- 活文档同步：`docs/operations/configuration.md` 退出码表及其说明、`docs/deployment/linux-systemd.md` 重启策略段、`docs/operations/troubleshooting.md` 新增“Client 隧道突然全部不可用，但服务显示正常停止”一节。

## 安全与授权影响

- 不涉及认证、授权、密钥或凭据处理；日志与退出码不新增任何敏感字段。
- 故障排查文档明确建议收紧 `allow_remote` 与来源 ACL：长期暴露的 SSH 端口被公网扫描是本问题的外部触发源，降低抖动频率同时缩小爆破面。
- 未改变“不实现任意远程命令执行”的边界，也未触及 ADR 0005 的脚本执行能力。

## 测试证据

红灯（实现前实测输出）：

```text
--- FAIL: TestSessionPoolScaleDownDoesNotStopPool
    session_pool_test.go:414: Run() returned after a scale-down: context canceled
--- FAIL: TestRuntimeWaitFailsWhenSessionRoleEndsUnexpectedly
    runtime_test.go:495: Wait() = <nil>, want ErrRunEndedUnexpectedly
--- FAIL: TestClientRunExitsNonZeroWhenSessionRoleEndsUnexpectedly
    client_runtime_test.go:66: client run exit code = 0 (err = <nil>), want 1 so a supervisor restarts it
```

绿灯与门禁：

- `go test ./internal/client ./internal/cli -count=1` 通过；包级 `-race` 通过，覆盖新增标记的并发取消路径。
- `go test ./... -count=1`：38 个包全部 `ok`，无失败。
- `go test -race ./... -count=1`：见下方集成状态。
- `go vet ./...` 无输出；`gofmt -l internal/client internal/cli` 无输出；`git diff --check` 干净。
- 既有契约未被削弱：`TestRuntime...Stop`（`internal/client/runtime_test.go:451`，`Stop` 后为 `nil` 或 `context.Canceled`）与 `TestExitCodeFor`（`internal/cli/exit_test.go:88`，“未被告知停止的取消错误”仍是 `ExitGeneric`）继续通过。
- 无前端改动，`web/` 的 `npm test`/`npm run build` 与嵌入产物校验不在本次范围。

## 发布步骤

1. 先合入代码，按既有灰度顺序发布 Client/Agent：只读配置目录、`ExecStartPre` 仍执行 `check-config`，二进制替换即可，无 DB 与配置迁移。
2. 灰度批次选择配置了 `connections.max > min` 的节点（本次事故的暴露面），观察一个 `evaluation_interval` 周期内不再出现 `Deactivated successfully`，并确认 `journalctl` 中出现的是 supervisor 重启而不是进程静默结束。
3. 核心指标：Client 隧道可用率与 `client session closed` 计数；升级期间 worker 崩溃重启次数应上升后收敛（异常结束从静默变为可见的重启）。
4. 单元模板变更只影响新装与显式重装；存量机器由运维 drop-in 覆盖，本仓库发布不远程改动生产机器。

## 回滚步骤

- 回滚即还原二进制与四个单元模板，无数据、无 Schema、无配置补偿，5 分钟内可完成。
- 若 `Restart=always` 引发重启风暴：还原为 `Restart=on-failure` 即可；风暴本身由 `RestartPreventExitStatus=78 77` 与 supervisor 的 `GiveUpAfter`（退出码 70）双重限制。
- 若 Task 2 的退出语义影响到复用 `Runtime` 的系统托盘：优先回退该改动并保留连接池标记修复，根因层修复单独生效且不改变停止语义。
- 现场止损 drop-in `/etc/systemd/system/tunnelmesh-client.service.d/50-restart-always.conf` 与本 PR 解耦，删除后 `systemctl daemon-reload` 即回到旧行为。

## Reviewer 关注点

- 缩容与真实故障的区分依赖 `evaluate` 中“先标记再取消”的顺序，请重点看这条 happens-before 是否可接受，以及是否应改为在 `p.mu` 下读取标记。
- `runRoles` 现在会在“所有角色返回而运行上下文未取消”时统一上报 `ErrRunEndedUnexpectedly`。请确认可是否存在合法的多角色自然结束场景（当前实现里连接池与 agent 角色都只在被取消或失败时返回，没有自然结束）。
- 连接池没有“补回 `Min`”的路径：slot 因真实故障消失后只能靠扩缩容判定恢复。本次不改这个语义，但值得单独立项。
- Agent 侧连接池（`internal/agent/connection_pool.go`）未审计是否存在同类“意外结束上报成功”，本 PR 不宣称已修复，只由单元模板兜底。
- `Restart=always` 对 Server 与容器编排不适用的理由已在文档中说明，若 reviewer 认为应统一，需要另案评估 Server 的退出语义。

## 集成状态

- 实施计划已获用户确认后执行，三个任务全部完成。
- `go test ./... -count=1`、`go vet ./...`、`gofmt`、`git diff --check` 通过；`go test -race ./...` 结果见补充。
- 尚未 commit、push 或创建远端 PR，等待用户授权。
