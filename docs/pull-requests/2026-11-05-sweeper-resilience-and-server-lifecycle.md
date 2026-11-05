# 后台清扫器容错统一与 Server 进程生命周期

- 标题：清扫器一轮失败不再永久停摆，Server 入口接管信号与退出码
- 目标分支：`main`
- 相关记录：[实施计划](../superpowers/plans/2026-11-05-sweeper-resilience-and-server-lifecycle.md)

## 摘要

稳定性复查里确认了两个缺陷，都属于「故障不会自己喊人」这一类。

**其一，后台清扫器会静默永久停摆。** Server 进程内有五个周期清理任务，其中三个
（客户端元数据、WebSSH 会话、客户端连接租约）的循环把第一轮数据库错误当致命错误 `return err`，
运行它的 goroutine 随即结束；这个错误只在 `ServerRuntime.Close()` 时才被读出来，而一个长期运行的
Server 永远不会走到那里。后果是一条瞬时故障（数据库重启、连接池抖动、单条语句超时）之后，清理能力
永久消失且没有任何信号，`client_connection_leases` 退回本次要修的无界增长。仓库里另外两个清扫器
（审计保留、身份维护）原先就是「记录并继续」——正确的策略早已存在，只是被抄了五份而漏掉了三份。

**其二，`tunnelmesh-server` 是唯一没走 `cli.Main` 的入口。** 它调用
`cli.Execute(context.Background(), …)` 再 `log.Fatal`，而全仓只有 `cli.Main` 安装 `SIGINT`/`SIGTERM`
处理器。于是优雅停机（`ServeListener` 里那条 proxy entry → Agent 流 → relay → HTTP drain → VPN 排空
的链路）早已实现却永远不会被触发：`systemctl stop` 直接以信号带走进程，实测退出码 143。同时
`log.Fatal` 把 78（配置错误）与 77（凭据被永久拒绝）压成 1，而这两个码正是交付单元要排除的，
结果坏配置在 `Restart=on-failure` 下每 5 秒重来一次、永不自愈。Server 单元也是全仓唯一还在
`Restart=on-failure` 的常驻单元——macOS 三个角色共用 `KeepAlive=true`。

修法分四层，各自可独立验证：

1. 新增 `internal/server/sweep_loop.go`：唯一的周期清扫策略 `runSweepLoop`。立即跑一轮、按 tick 补跑、
   **一轮失败只意味着这一轮没做成**（按 `<前缀>_sweep_failed` 记日志后等下一轮）、只有上下文取消才返回；
   已取消的上下文不开工（关停日志不被误报污染），非正间隔直接报错而不是让 `time.NewTicker` panic——
   goroutine 里的 panic 会带走整个 Server 进程，一个清理任务不该有这种爆炸半径。
2. 五个清扫器的 `Run` 全部委托该循环。仓储未接线是配置错误而非可重试的一轮，因此每个 `Run` 保留进循环
   **之前**的 nil 守卫并原样返回，避免退化成每 tick 刷一条日志的僵尸任务。SQL、间隔、批量、保留窗口、
   `Sweep`/`SweepOnce` 签名、`ServerRuntime.Close()` 的错误汇聚全部未动。
3. `cmd/tunnelmesh-server/main.go` 改为 `os.Exit(cli.Main(cli.NewServerRoot()))`，两个 Server systemd 单元
   与其余角色对齐为 `Restart=always` + `RestartPreventExitStatus=78 77`，并显式写出 `TimeoutStopSec=60s`、
   `KillSignal=SIGTERM`。同时加两个结构性守卫测试（`scripts/service_lifecycle_test.go`）：每个无构建约束的
   `cmd/*/main.go` 必须委托 `cli.Main`；每个常驻单元必须 `Restart=always` + `RestartPreventExitStatus=78 77`。
   同类漂移下次会在 CI 红灯，而不是在下一次停机时才被发现。
4. `deploy/prometheus/alert-rules.yaml` 补两条规则：`TunnelMeshLeaseSweepErrors`（15 分钟内多于 3 轮失败）
   与 `TunnelMeshLeaseSweepStalled`（10 分钟内 0 轮）。这两条**必须**排在第 1、2 层之后：循环会在首轮失败
   后死亡时，`result="error"` 只产生一个样本，`increase` 永远够不到阈值，指标形同虚设。

无 DDL、无 `SchemaVersion`、无 API、无新增配置项——属于 PATCH。

## 用户影响

- **Server 的停止行为变了**：`systemctl stop`/`restart` 现在会先执行优雅下线再退出，实测 0.03 秒内以
  退出码 0 结束；改动前是被 `SIGTERM` 直接杀死（143），在途请求被硬切断。
- 坏配置不再无限重启：`run` 与 `check-config` 以 78 退出（改动前是 1），被 `RestartPreventExitStatus=78 77`
  挡住；崩溃、监听器被意外关掉、入口返回 0 等情况一律由 `Restart=always` 拉起。
- 后台清理任务对数据库抖动的表现从「永久停摆且无信号」变成「本轮失败、下一轮重试、日志与指标都有痕迹」。
  反过来，运维判据也要跟着变：**进程存活不再是后台任务健康的证据**，要看两条新告警或
  `*_sweep_failed` 日志事件。
- 五个清扫任务的失败日志事件名统一为 `<前缀>_sweep_failed`：`client_metadata`、`webssh_session`、
  `client_connection_lease`、`audit_retention`、`auth_maintenance`。审计与身份维护原有的成功日志
  事件名与字段逐字未变。
- 容器部署是这次改动收益最大的一类：官方镜像以 exec 形式把 `cmd/tunnelmesh-server` 编成
  `/usr/local/bin/tunnelmesh` 作为 PID 1（distroless，无 init），而**内核不会把「默认行为是终止」的信号
  投递给 PID 1**——没有安装处理器的旧入口因此完全收不到 `SIGTERM`，`docker stop`/滚动更新必定等满
  `stop_grace_period`（默认 10 秒）再被 `SIGKILL`，drain 一行都没执行。现在处理器已安装，宽限期内能干净
  退出；启用 VPN 数据面且 `server.vpn.shutdown_timeout` 大于 10 秒时，把 compose 的
  `stop_grace_period` 相应调大。`docker-compose.{local,cluster}.yml` 早已是 `restart: unless-stopped`
  （0 退出也会拉起），这反过来印证了 systemd 的 `Restart=on-failure` 是全仓唯一的例外。
  WinSW（`onfailure action="restart"`）与 launchd 模板本次未改。

## API / Schema / 配置影响

- 无 HTTP 接口、无 `docs/api/openapi.yaml`、无 `migrations/` 变更。
- 无新增或改名的配置键；文档里出现的 `server.vpn.shutdown_timeout` 是既有键，用于解释 `TimeoutStopSec=60s`
  的预算构成。
- 部署产物：`deploy/systemd/tunnelmesh-server.service` 与 `deploy/systemd-user/tunnelmesh-server.service`
  的 `[Service]` 段变化，一键安装脚本按模板渲染，无需改脚本；已用手工 drop-in 覆盖过单元的环境，
  `systemctl edit` 生成的覆盖仍然优先，不受本次影响。
- 指标：不新增序列，只是让既有的 `tunnelmesh_client_connection_lease_sweep_total` 在故障期间持续出样本。
- 告警：新增 2 条，`deploy/prometheus/alert-rules.yaml` 由 9 条增至 11 条。
- 文档同步：`docs/deployment/linux-systemd.md`（三角色统一重启策略与 `TimeoutStopSec` 的理由）、
  `docs/operations/configuration.md`（退出码表与「server 仍用 on-failure」那段）、
  `docs/operations/observability.md`（新增「后台清扫任务」一节 + 两条告警口径与值班归属）、
  `docs/operations/troubleshooting.md`（新增「Server 停止后没有执行优雅下线」条目，并把租约回收的
  「确认清扫是否在跑」改成指标口径）。

## 安全与授权影响

- 无新增暴露面、无鉴权变化。清理任务的错误日志只含错误文本与计数，不含行标识符、DSN、Token；
  `sweep_loop_test.go` 里有一条断言专门盯这一点（失败日志中不得出现 `dsn`/`token`）。
- 关停期间不再产生新的错误日志：已取消的上下文不开工，避免 shutdown 日志里混进必然失败的数据库错误。
- 入口改动不影响 `ExecStartPre` 的密钥注入约定（`EnvironmentFile=-` 语义未变）。

## 测试证据

TDD 红灯（实现前实测输出）：

- `go test ./internal/server -run TestRunSweepLoop -count=1` →
  `internal/server/sweep_loop_test.go:82:22: undefined: runSweepLoop`（6 处）。
- `go test ./internal/server -run 'SweeperSurvivesFailed' -count=1` → 三条 FAIL，均为
  `Run error = database is not reachable, want context canceled after 1 rounds`，即旧实现在首轮失败就返回。
- `go test ./scripts -run TestEveryBinaryEntrypoint -count=1` →
  `../cmd/tunnelmesh-server/main.go does not call cli.Main, so it cannot handle SIGTERM or map exit codes`
  与 `calls log.Fatal inside main, which collapses every exit code to 1`。
- `go test ./scripts -run TestLongLivedUnits -count=1` → 两个 server 单元各报缺 `Restart=always`、
  缺 `RestartPreventExitStatus=78 77`、缺 `TimeoutStopSec=`。
- `TestServerMainReturnsConfigExitCode` 在实现前即为绿（`cli.Main` 的映射一直正确，缺的是入口没有使用它），
  本任务的实际红灯来自上面的入口守卫，已在计划的执行记录里如实标注。

新增测试（13 个）：

- `internal/server/sweep_loop_test.go`：`TestRunSweepLoopSweepsBeforeTheFirstTick`、
  `TestRunSweepLoopSurvivesFailedPasses`、`TestRunSweepLoopLogsFailuresWithoutNamingRows`、
  `TestRunSweepLoopStopsWhenCancellationLooksLikeAShutdown`、
  `TestRunSweepLoopSkipsPassesAfterCancellation`、`TestRunSweepLoopRejectsNonPositiveInterval`。
- 三个停摆回归：`TestClientMetadataSweeperSurvivesFailedRounds`、
  `TestWebSSHSweeperSurvivesFailedRounds`、`TestClientConnectionSweeperSurvivesFailedRounds`
  （最后一个还断言 `result="error"` 样本数 ≥ 3，即告警能工作的直接证据）。
- `internal/cli/exit_test.go:TestServerMainReturnsConfigExitCode`。
- `scripts/service_lifecycle_test.go`：`TestEveryBinaryEntrypointUsesCLIMain`、`TestLongLivedUnitsAlwaysRestart`。

既有测试保持绿色、未被放宽：`TestClientConnectionSweeperWithoutGaugesOrRepository`（仓储缺失必须立即返回）、
`TestClientMetadataSweeperMarksExpiredAndStops`、`TestWebSSHSweeperRunsUntilCancel`、
`TestServerRuntimeCloseStopsWebSSHSweeper`、`TestAuthMaintenanceSweeperRunStopsOnCancel`、
`TestNewServerRuntimeStartsAuditRetentionOnlyWhenConfigured`。

门禁实际输出：

| 命令 | 结果 |
| --- | --- |
| `go build ./...` | OK |
| `go test ./... -count=1` | 全部 `ok`（无 FAIL 行） |
| `go vet ./...` | OK |
| `gofmt -l internal/ cmd/ scripts/` | 无输出 |
| `git diff --check` | 无输出 |
| `go test -race ./internal/cli ./scripts -count=1` | `ok` 73.5s / `ok` 1.9s |
| `go test -race ./internal/server -run 'Sweep|Sweeper|AuditRetention|AuthMaintenance' -count=1` | `ok` 4.4s |
| `go test ./deploy/install/oneclick -run Server -count=1`（含 `TM_ONECLICK_E2E=1`） | `TestOneClickServerUserModeInstall` PASS 5.1s |
| `promtool check rules …` | **未执行**：本机无 `promtool` 且 docker daemon 未运行。回退 `ruby -ryaml` 解析通过，`rules=11`，两条新规则 `expr` 已回读核对 |

真实进程对照取证（本机无 systemd，用 `kill -TERM` 直接验证入口语义；「改动前」二进制来自 `main` 的
`git worktree`，两者都先确认 `/health/ready` 返回 200）：

| 场景 | 改动前（`238857d`） | 改动后 |
| --- | --- | --- |
| `run` 收到 `SIGTERM` | `exit=143`（被信号杀死，drain 未执行） | `exit=0`，0.03s 内结束 |
| `check-config` 遇坏配置 | `exit=1` | `exit=78` |
| `run` 遇坏配置 | `exit=1` | `exit=78` |

顺带修复的既有缺陷（测试基建，非生产代码）：`TestClientConnectionSweeperRunSweepsBeforeTheFirstTick`
的两个测试替身在改动前的 `main` 上就会被 `-race` 判为 data race（`Run` 的 goroutine 与测试协程无同步地读写
同一切片）。因为本次要在同一文件里新增按 tick 调用的夹具，顺手给它们加了互斥量与访问器。CI 从不跑
`go test -race ./internal/server`，所以它长期未暴露。

已知无关失败：`TestServerNodeAPIListDetailLifecycleAndAudit` 的既有 flake（已在 PR #88 记录），本次全量
`go test ./... -count=1` 未复现。

## 发布步骤

1. 合并后按常规灰度：先发一个 Server 节点，观察 `TunnelMeshLeaseSweepStalled`/`TunnelMeshLeaseSweepErrors`
   为绿、`*_sweep_failed` 事件不出现，再全量。
2. 已用一键脚本安装的环境执行 `systemctl daemon-reload` 后重启 Server，让新单元生效；只换二进制不 reload
   时，`Restart=always`/`RestartPreventExitStatus` 不会更新，但优雅停机与真实退出码已经生效。
3. 手工安装的环境需要把两个 Server 单元的改动同步过去，或直接 `curl | bash` 重跑安装脚本。
4. Prometheus 侧 `promtool check rules` 后 reload；Grafana 无需改动（两条告警是 warning 级）。
5. 遗留死行不需要手工清理：新 Server 启动后第一轮清扫即按每分钟 1000 行收敛。

## 回滚步骤

- 应用为 PATCH 且无数据动作：回滚 = 换回旧二进制 + 旧单元 + `daemon-reload`，5 分钟内可完成。
- **代码与单元必须一起回滚**：若保留 `Restart=always` 而退回 `log.Fatal` 入口，坏配置会重新变成 5 秒周期的
  无限重启（这正是旧行为里最坏的一条）。只想退单元时，`systemctl edit` 写 `Restart=on-failure` 即可先行止损。
- 分层可独立回退：`git revert` 任务 2/3 的提交即恢复「失败即退出」，`sweep_loop.go` 留着无副作用；
  两条告警规则删掉即可，指标本身由 PR #88 引入，不受影响。
- 回滚后需要重新确认的观测口径：`*_sweep_failed` 不再出现，租约回收健康与否只能看
  `result="error"` 是否有过样本（这也是为什么回滚后要一并撤掉告警，避免误判为「没告警就是好的」）。

## Reviewer 关注点

1. **「一轮失败只算一轮」是否是所有清理任务都该遵循的策略**：有没有哪个清扫器的失败其实是致命的？
   逐个看完后的结论是没有——五个任务的语句都以时间戳为边界且幂等，重试无副作用；但这值得独立确认。
2. `Restart=always` 对 Server 是否安全。这是本案推翻前一个 PR 遗留结论的地方（见
   `docs/pull-requests/2026-11-05-client-session-pool-silent-exit.md` 的 Reviewer 关注点）：判据是
   `ServeListener` 在 `http.ErrServerClosed` 时也返回 `nil`，即 0 退出确实可能意味着「监听器没了」。
3. 仓储缺失保留「进循环之前就返回」的不对称语义（配置错误 vs 可重试的一轮），确认这个边界划得对。
4. 跨节点 `sum(...)` 的告警语义：它表达「没有任何节点在回收」而不是「某个节点停了」，依据是清扫语句的
   幂等性；如果将来给清扫任务加分布式锁或按节点分区，这条口径必须重审。
5. `TimeoutStopSec=60s` 与 `server.vpn.shutdown_timeout`（可配，默认 15s）的关系：把后者调到 60 秒以上时，
   单元里的预算需要一并上调，否则 systemd 会先 `SIGKILL`。
6. 入口守卫只检查**无构建约束**的 `cmd/*/main.go`：`tunnelmesh-client-tray` 是 `//go:build tray` 的桌面程序，
   在主线程驱动 AppKit/WebView2 并自带信号处理，属有意排除，理由写在测试注释里。

## 集成状态

- 分支：`codex/sweeper-resilience-and-server-lifecycle`，基线 `238857d`（含 PR #88）。
- 本地门禁：上表全部执行，`promtool` 一项未执行并已在 Test Evidence 标注原因。
- 待办：CI（build-test / packaging / windows-tray / mysql56）与 `promtool check rules` 需在合并前由 CI 或
  reviewer 环境补上。
