# 后台清扫器容错统一与 Server 进程生命周期实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 消除两个已经确认的稳定性缺陷：(1) Server 的三个后台清扫器在任意一轮数据库错误后**永久静默停止**，且没有任何信号暴露这件事；(2) `tunnelmesh-server` 进程入口是唯一没有走 `cli.Main` 的可执行程序，因此**没有信号处理**（`systemctl stop` 直接杀进程，不经过已实现的优雅停机与 drain）并且**退出码全部塌缩为 1**（`log.Fatal`），使交付的 systemd 单元既无法区分配置错误与崩溃、也无法保证崩溃后被拉起。交付物是一个共享的清扫循环、三个清扫器的行为收紧、Server 入口与单元的补齐，以及两条让"清扫没在跑"变成可告警事实的 Prometheus 规则。

**Architecture:** 四层，各自独立可验证，依赖关系见「任务依赖」。

1. 策略层（新增）：`internal/server/sweep_loop.go` 提供唯一的周期清扫循环 `runSweepLoop(ctx, name, interval, pass)`，把「先跑一轮、按 tick 补跑、失败只记录不退出、只有取消才返回」这条**每个清扫器都必须相同**的策略集中到一处（AGENTS.md §8 DRY：当前 5 个清扫器有 2 份正确实现 + 3 份错误实现，共 5 份重复的循环骨架）。
2. 清扫器层（改行为）：`ClientMetadataSweeper.Run`、`WebSSHSweeper.Run`、`ClientConnectionSweeper.Run` 改为委托共享循环，失败从 `return err` 变成 `slog.ErrorContext` + 继续下一轮；`AuditRetentionSweeper.Run`、`AuthMaintenanceSweeper.Run` 已经正确，纯重构到同一循环，成功日志与字段逐字保持不变。
3. 进程层（改入口与单元）：`cmd/tunnelmesh-server/main.go` 改用 `os.Exit(cli.Main(cli.NewServerRoot()))`，复用 `internal/cli/exit.go` 已有的 `signal.NotifyContext(SIGINT, SIGTERM)` 与 `ExitCodeFor` 语义；`deploy/systemd{,-user}/tunnelmesh-server.service` 与 agent/client 对齐为 `Restart=always` + `RestartPreventExitStatus=78 77`，并显式给出 `TimeoutStopSec`。用两个结构性守卫测试（`scripts/`）把「入口必须走 `cli.Main`」「常驻单元必须 `Restart=always`」固化成 CI 断言，防止同类漂移再次发生。
4. 可观测层（新增告警）：`deploy/prometheus/alert-rules.yaml` 增加租约回收「连续失败」与「彻底停摆」两条规则。**这两条规则的前提是第 1、2 层**：循环会在首次失败后死亡时，`result="error"` 只产生一个样本，`increase(...[15m]) > 3` 永远不会触发，指标形同虚设。

**Tech Stack:** Go 标准库（`context`、`time`、`log/slog`、`os`、`go/parser`、`go/ast`）、`github.com/spf13/cobra`、`prometheus/client_golang`、`internal/workerproto` 既有退出码契约、Prometheus recording/alerting rules（YAML）。不新增第三方依赖，不改前端。

**Spec:** 无新增设计规格与 ADR——本计划不改变协议、API、Schema 与配置面，只修复进程生命周期与后台任务的失败策略。引用的既有事实源与约束：

- AGENTS.md §5「超时与重试」「优雅降级」「健康检查：异常节点自动摘除」「快速恢复：故障 5 分钟内回滚」——清扫器属于"非核心后台任务"，按 §5「故障隔离」不应成为核心链路的故障源；一条数据库抖动的正确后果是这一轮没做成，而不是清理能力永久消失。
- AGENTS.md §7 可观测性：核心动作必须可观测；「清扫没在跑」当前既无日志也无指标之外的信号，本计划补齐。
- AGENTS.md §8「三处以上重复必须抽象」「代码注释不可少（说明为什么）」。
- AGENTS.md「一致性与并发」：清扫幂等、以时间戳为边界，因此单节点停摆可由其他节点补偿（见「架构决策」第 4 条，这也是告警取全局 `sum` 的依据）。
- AGENTS.md「版本升级与迭代」：**PATCH**——不改 Schema、不改配置、不改 API 契约，因此 `migrations/` 与 `docs/operations/configuration.md` 的字段表无动作；退出码语义变更属于运维可见行为，必须落文档。
- `docs/pull-requests/2026-11-05-client-session-pool-silent-exit.md:84` 明确遗留：「`Restart=always` 对 Server 与容器编排不适用的理由已在文档中说明，若 reviewer 认为应统一，**需要另案评估 Server 的退出语义**」——本计划即该案，结论是应当统一，理由见「架构决策」第 5 条。
- `internal/workerproto/workerproto_test.go:100` 的既有契约：「These numbers are a contract with systemd unit files (RestartPreventExitStatus)」——本计划让 Server 单元第一次真正兑现这个契约。

## 背景：现象与根因（HEAD = `238857d`，PR #88 合并后的 main）

| # | 已验证事实 | 位置 |
| --- | --- | --- |
| 1 | 三个清扫器的循环把一次数据库错误当作致命错误 `return err`，运行循环的 goroutine 随之结束，此后没有任何东西重试 | `internal/server/client_metadata_sweeper.go:27-43`、`internal/server/webssh_sweeper.go:26-42`、`internal/server/client_connection_sweeper.go:93-109` |
| 2 | 该错误只在 `ServerRuntime.Close()` 时才被读出，长期运行的进程永远不会打印它；`Close` 读到后又放进 `errors.Join`，只影响 `run` 的返回值 | `internal/server/runtime.go:450-499`（`done <- ...Run(ctx)`）、`:500-580`（`Close` 汇聚） |
| 3 | 另外两个清扫器已经是"记录并继续"，且显式把关停期间的 `context.Canceled` 排除在错误日志之外——正确策略在仓库里已存在，只是没被统一 | `internal/server/audit_retention_sweeper.go:111-150`、`internal/server/auth_maintenance_sweeper.go:86-120` |
| 4 | Server 是唯一用 `log.Fatal` 的入口，agent/client 已改为 `cli.Main`；全仓只有 `cli.Main` 安装信号处理器，因此 Server 进程在 `context.Background()` 下运行，`SIGTERM` 直接终止进程 | `cmd/tunnelmesh-server/main.go:11`、`cmd/tunnelmesh-agent/main.go:11`、`cmd/tunnelmesh-client/main.go:11`、`internal/cli/exit.go:25` |
| 5 | Server 的优雅停机**已经实现且正确**，只是没人调用它：`ServeListener` 在 `ctx.Done()` 时 drain（proxy entry、agent streams、relay、HTTP `srv.Shutdown` 5s、VPN `closeVPNGateway`）并返回 `nil`；`run` 也已经把 `cmd.Context()` 透传给 `storage.OpenConfig` 与 `runtime.Serve` | `internal/server/runtime.go:1072-1178`、`internal/cli/root.go:158-183` |
| 6 | 因为 `log.Fatal` 恒为 1，`configError` 的 78 与 `ExitAuthFatal` 的 77 在 Server 上不可能出现；`configCommand` 对所有角色统一调用 `configError(err)`，缺的只是入口不用 `Main` | `internal/cli/root.go:875-877`、`internal/cli/exit.go:29-34`、`internal/workerproto/workerproto.go:37-46` |
| 7 | 交付单元里只有 Server 是 `Restart=on-failure` 且无 `RestartPreventExitStatus`：崩溃/优雅退出后是否拉起取决于 1 还是 0，而 `ExecStartPre=check-config` 失败同样以 1 退出，因此**坏配置当前会以 5 秒周期无限重启** | `deploy/systemd/tunnelmesh-server.service`、`deploy/systemd-user/tunnelmesh-server.service`（对比 `deploy/systemd/tunnelmesh-agent.service`、`deploy/systemd/tunnelmesh-client.service`） |
| 8 | macOS 三个角色共用 `KeepAlive=true`，WinSW 为 `onfailure action="restart"`：launchd 侧早已是"总是拉起"，systemd 的 Server 单元是全仓唯一的例外 | `deploy/macos/tunnelmesh.plist:23-24`、`deploy/windows/tunnelmesh-service.xml:19` |
| 9 | `tunnelmesh_client_connection_lease_sweep_total{result,error_class}` 每轮必打点（成功与失败都打），所以"停摆"可由 `increase(...) == 0` 发现；但 `docs/operations/observability.md:97` 只是在正文里"建议"了阈值，`deploy/prometheus/alert-rules.yaml` 里没有这条规则 | `internal/server/client_connection_sweeper.go:118-133`、`internal/observability/metrics.go:90`、`deploy/prometheus/alert-rules.yaml`（现有 9 条） |
| 10 | 现有 9 条告警没有任何一条覆盖后台清扫任务 | `deploy/prometheus/alert-rules.yaml` |

## 架构决策

1. **抽象循环而不是复制补丁。** 三个清扫器的 bug 是同一段 12 行骨架被抄了 5 遍的产物；只把三处 `return err` 改成 `slog.Error(...)` 会留下 5 份仍然会各自漂移的策略实现，也不满足 AGENTS.md §8。共享 `runSweepLoop` 后，"失败是否致命"只有一个答案，且只有一处需要被测试覆盖。
2. **区分"配置错误"与"一轮失败"，前者必须仍然立即返回。** `NewClientMetadataSweeper(nil, ...)`/`NewClientConnectionSweeper(nil, ...)` 这类仓储缺失是接线错误，如果也变成"记录并继续"，就会退化成每 30 秒刷一条错误日志的僵尸 goroutine。因此每个 `Run` 保留进入循环**之前**的 nil 守卫并原样返回错误（既有测试 `client_connection_sweeper_test.go:145` 正是断言这一点，必须继续通过），循环内部只处理 `pass` 的错误。
3. **不新增配置项。** 间隔、批量、保留窗口沿用编译期常量（与 PR #88 的决策一致）；容错策略是正确性问题，不是可调偏好，给它一个开关只会让默认值和文档各写两套。
4. **多节点不需要分布式锁，且这正是告警可以用全局 `sum` 的理由。** 每条清扫语句都以时间戳为边界、幂等，任意一个节点的任意一轮都能收敛全表（`internal/server/client_connection_sweeper.go:38-47`、`audit_retention_sweeper.go:135-138` 的既有论证）。因此"某个节点的清扫器死了"在集群里是无害事实，`sum(increase(...[10m])) == 0` 表达的是"没有任何节点在回收"，语义恰好是我们想叫醒人的那一件事；单节点部署里它等价于逐节点检测。
5. **Server 采用 `Restart=always`，与 agent/client 及 launchd 对齐。** 反驳"Server 退出 0 就是自愿"的前提在 Server 上同样成立甚至更强：`ServeListener` 在 HTTP `srv.Serve` 返回 `http.ErrServerClosed` 时也返回 `nil`（`runtime.go:1160-1164`），此时进程 0 退出而没有任何人要求它停止，`Restart=on-failure` 不会拉起，公网入口就此消失。78/77 挡在 `RestartPreventExitStatus` 之后，其余退出交给 `RestartSec=5s` 拉起。
6. **`TimeoutStopSec` 对 Server 显式取值 60s。** 优雅停机链路最长约为 HTTP 5s（硬编码）+ VPN `shutdown_timeout`（默认 15s，可配），显式写 60s 既不改变 systemd 默认行为（默认 90s）的宽松度太多，又把"停止是有界的"这件事写进单元，供容量评审使用。
7. **两条告警阈值来自既有文档。** `result="error"` 用 15 分钟内 > 3 轮（间隔 1 分钟，即连续失败才算，单轮抖动不算），沿用 `docs/operations/observability.md:97`；停摆用 10 分钟内 0 轮 + `for: 5m`，即最迟约 15 分钟叫醒人，而 `TunnelMeshMetricsMissing` 已经负责"完全抓不到指标"这一层。
8. **结构性守卫放在 `scripts/` 包。** 该包已有用 `go/parser` 静态扫描全仓的先例（`scripts/tray_build_tag_test.go`、`scripts/vpn_build_tag_test.go`），用它守住"每个 `cmd/*/main.go` 必须委托 `cli.Main`"和"每个常驻单元必须 `Restart=always`"，成本是一次源码解析，收益是同类回退在 CI 里红灯而不是在下一次故障时被发现。

## 全局约束

- 严格 TDD：每个任务先写失败测试并观察红灯输出，再做最小实现，最后重构。清扫循环与入口退出码禁止只靠手工验证。
- 不得改变任何清扫器的 SQL、间隔、批量、保留窗口或 `Sweep`/`SweepOnce` 签名；本计划只改**失败策略**。
- 不得改变 `ServerRuntime.Close()` 的错误汇聚逻辑：`Run` 只会在取消或接线错误时返回非 `nil`，`Close` 现有的 `!errors.Is(err, context.Canceled)` 过滤继续成立，因此 `runtime.go` 不在文件清单里。
- 迁移到共享循环时，`audit_retention_sweep_failed`、`auth_maintenance_sweep_failed` 以及成功日志的字段名与条件必须逐字保持不变（日志是既有运维文档引用的对外契约）；新增事件名沿用同一命名法。
- 日志只输出错误与计数，不得包含 DSN、Token、行标识符（沿用 `audit_retention_sweeper.go:139-143` 的注释要求）。
- 无 Schema、无 API、无配置面变更，因此 `migrations/`、`docs/api/openapi.yaml` 无动作。
- 未获得用户明确授权，不执行 commit、push、merge 或创建远端 PR。

## 文件清单

新增：

- `internal/server/sweep_loop.go`（共享循环 `runSweepLoop`、`sweepPass`、`errInvalidSweepInterval`）
- `internal/server/sweep_loop_test.go`
- `scripts/service_lifecycle_test.go`（两个结构性守卫：入口必须 `cli.Main`；常驻单元必须 `Restart=always` + `RestartPreventExitStatus=78 77` + Server 单元必须有 `TimeoutStopSec`）

修改（Go）：

- `internal/server/client_metadata_sweeper.go`（`Run` 委托循环；`sweepAndLog` 形态的成功日志不需要，保持无成功日志）
- `internal/server/webssh_sweeper.go`（同上）
- `internal/server/client_connection_sweeper.go`（同上；`SweepOnce` 的打点逻辑不动）
- `internal/server/audit_retention_sweeper.go`（纯重构：`sweepAndLog` 的错误分支移入循环，`sweepAndLog` 保留为 `pass` 闭包的成功日志实现）
- `internal/server/auth_maintenance_sweeper.go`（同上）
- `cmd/tunnelmesh-server/main.go`（`os.Exit(cli.Main(cli.NewServerRoot()))`）
- `internal/server/client_metadata_sweeper_test.go`（新增失败容忍用例 + `err` 注入已具备）
- `internal/server/webssh_sweeper_test.go`（新增失败容忍用例 + `failingWebSSHRepo`）
- `internal/server/client_connection_sweeper_test.go`（新增失败容忍用例）
- `internal/cli/exit_test.go`（新增 `TestServerMainReturnsConfigExitCode`）

修改（部署与文档）：

- `deploy/systemd/tunnelmesh-server.service`、`deploy/systemd-user/tunnelmesh-server.service`
- `deploy/prometheus/alert-rules.yaml`（新增 2 条 → 共 11 条）
- `docs/operations/observability.md`（回收段落：把"建议告警"改为已交付规则名 + 新增停摆条目与四个清扫事件名）
- `docs/deployment/linux-systemd.md`（`:57` 段落末句「`server` 的单元保持 `Restart=on-failure`」改写为三角色统一策略 + `TimeoutStopSec`）
- `docs/operations/configuration.md`（`:515-523` 退出码表前后关于「`server` 仍用 `Restart=on-failure`」的段落；表头说明改为覆盖三个角色）
- `docs/operations/troubleshooting.md`（新增条目：Server 收到 `SIGTERM` 立即消失/未优雅 drain；租约行重新堆积且无回收日志）
- `docs/superpowers/plans/README.md`、`docs/pull-requests/README.md`（由 `python3 scripts/gen_doc_index.py` 生成，禁止手改）

计划外（明确不做，避免范围蔓延）：Windows WinSW 的 `onfailure` 语义、容器编排侧的 restartPolicy、client/agent 会话生命周期日志、`doctor` 经网关访问 `/health` 得到 403 的问题。

## 任务依赖

- 任务 1 → 任务 2、任务 3（`runSweepLoop` 是它们的唯一新增依赖）。
- 任务 2 → 任务 6（错误样本持续产生，`result="error"` 告警才有意义）。
- 任务 4 → 任务 5（`run`/`check-config` 真正以 78/77 退出之后，单元的 `RestartPreventExitStatus` 才有作用对象）。
- 任务 1/2/3/4/5 互不重叠，可并行实现；任务 7 汇总文档，最后执行。

---

### 任务 1: 共享周期清扫循环

**Files:**
- Create: `internal/server/sweep_loop.go`
- Test: `internal/server/sweep_loop_test.go`

- [x] **Step 1: 写失败测试**

创建 `internal/server/sweep_loop_test.go`（package `server`），用 `internal/server/runtime_test.go:1253-1256` 的既有方式捕获 `slog` 默认 logger 输出：

```go
func TestRunSweepLoopSweepsBeforeTheFirstTick(t *testing.T)
func TestRunSweepLoopSurvivesFailedPasses(t *testing.T)
func TestRunSweepLoopLogsFailuresWithoutNamingRows(t *testing.T)
func TestRunSweepLoopStopsWhenCancellationLooksLikeAShutdown(t *testing.T)
func TestRunSweepLoopSkipsPassesAfterCancellation(t *testing.T)
func TestRunSweepLoopRejectsNonPositiveInterval(t *testing.T)
```

关键断言（`pass` 用闭包计数器实现，不引入新类型）：

- `TestRunSweepLoopSurvivesFailedPasses`：`interval = 5*time.Millisecond`，前 3 轮返回 `errors.New("database is not reachable")`、之后返回 `nil`；轮询等到 `calls >= 5` 后 `cancel()`，断言 `Run` 返回 `context.Canceled` 且**失败之后的轮次仍然执行过**（`calls` 严格大于 3）。这是本任务的核心：实现前不存在该函数，编译失败即为红灯。
- `TestRunSweepLoopLogsFailuresWithoutNamingRows`：同一夹具，断言捕获的 JSON 日志里 `msg == "test_sweep_failed"`（`name` 传 `"test"`）至少 3 条，且日志行**不包含** `"database is not reachable"` 之外的敏感字段——具体做法是 `strings.Contains(logs.String(), `"error":"database is not reachable"')` 为真、`strings.Contains(logs.String(), "dsn")` 为假。
- `TestRunSweepLoopStopsWhenCancellationLooksLikeAShutdown`：`pass` 返回 `context.Canceled` 且 `ctx` 已取消 → `Run` 返回 `context.Canceled`，且捕获日志中**不含** `"_sweep_failed"`（关停不该以误报开场）。
- `TestRunSweepLoopSkipsPassesAfterCancellation`：传入一个已取消的 `ctx`，`pass` 断言自己从未被调用，`Run` 返回 `context.Canceled`。
- `TestRunSweepLoopRejectsNonPositiveInterval`：`interval = 0` → 返回错误而不是 panic（`time.NewTicker(0)` 会 panic，而 goroutine 里的 panic 会带走整个 Server 进程）。

- [x] **Step 2: 运行确认失败**

Run: `go test ./internal/server -run TestRunSweepLoop -count=1`
Expected: 编译失败，`undefined: runSweepLoop`。

- [x] **Step 3: 最小实现**

创建 `internal/server/sweep_loop.go`：

```go
package server

// sweepPass 是周期清扫循环唯一的可变步骤：一轮的具体工作，成功时自己负责日志与打点。
type sweepPass func(ctx context.Context) error

// errInvalidSweepInterval 保护循环不因 ticker 参数非法而 panic：goroutine 里的 panic
// 会终止整个 Server 进程，一个清理任务绝不能有这种爆炸半径。
var errInvalidSweepInterval = errors.New("sweep interval must be positive")

// runSweepLoop 实现全仓唯一的后台清扫策略：立即跑一轮，然后按 interval 补跑。
//
// 一轮失败只意味着"这一轮没做成"：下一次 tick 会重试，因此数据库抖动、连接池重启、
// 单条语句超时都不会永久取消清理能力。失败按 name 记录为 "<name>_sweep_failed"，
// 错误文本原样进日志但不含行标识符（由各 pass 保证）。
//
// 只有取消会让它返回：ctx 已取消时这一轮根本不该开始（必然失败且只会污染关停日志），
// 而 pass 返回 context.Canceled/context.DeadlineExceeded 且 ctx 也已取消时，
// 那是关停正好落在这一轮里，同样按取消返回，交由 ServerRuntime.Close 过滤。
func runSweepLoop(ctx context.Context, name string, interval time.Duration, pass sweepPass) error {
	if interval <= 0 {
		return errInvalidSweepInterval
	}
	if err := runSweepPass(ctx, name, pass); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := runSweepPass(ctx, name, pass); err != nil {
				return err
			}
		}
	}
}

// runSweepPass 跑一轮，并区分三种结果：成功、关停造成的取消、需要告警的失败。
// 只有取消会被返回给调用方；失败已被记录，调用方应继续。
func runSweepPass(ctx context.Context, name string, pass sweepPass) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	err := pass(ctx)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return ctx.Err()
	}
	slog.ErrorContext(ctx, name+"_sweep_failed", "error", err)
	return nil
}
```

`import` 为 `context`、`errors`、`log/slog`、`time`。注释按上述给出（AGENTS.md §8「说明为什么」）。

- [x] **Step 4: 运行确认通过**

Run: `go test ./internal/server -run TestRunSweepLoop -count=1`
Expected: `ok  	github.com/tunnelmesh/tunnelmesh/internal/server`

Run: `go vet ./internal/server && gofmt -l internal/server`
Expected: 无输出。

---

### 任务 2: 三个静默停止的清扫器改为失败可续

**Files:**
- Modify: `internal/server/client_metadata_sweeper.go:27-43`
- Modify: `internal/server/webssh_sweeper.go:26-42`
- Modify: `internal/server/client_connection_sweeper.go:88-109`
- Test: `internal/server/client_metadata_sweeper_test.go`、`internal/server/webssh_sweeper_test.go`、`internal/server/client_connection_sweeper_test.go`

- [x] **Step 1: 写失败测试**

三个用例共用形态：注入失败的仓储、`interval = 5*time.Millisecond`、断言"失败后仍有更多轮"且 `Run` 返回 `context.Canceled`。

1. `internal/server/client_metadata_sweeper_test.go` 新增 `TestClientMetadataSweeperSurvivesFailedRounds`：复用既有 `fakeClientInstanceRepo`（已有 `err error` 字段，`MarkExpired`/`PurgeUnreported` 都会返回它）。先置 `repo.err = errors.New("db down")`，等 `len(repo.expired) >= 2`（证明第一轮失败后仍在跑），再置 `repo.err = nil`，等出现成功轮，最后 `cancel()` 并断言 `Run` 返回 `context.Canceled`。
2. `internal/server/webssh_sweeper_test.go` 新增 `TestWebSSHSweeperSurvivesFailedRounds` 与夹具 `failingWebSSHRepo`：内嵌 `storage.WebSSHSessionRepository`（与 `recordingClientConnectionRepo` 同样的"单方法替身 + 其余方法调用即 panic"技巧），只实现 `ExpirePending`/`CloseExpiredActive` 返回注入错误并计数。断言同上。
3. `internal/server/client_connection_sweeper_test.go` 新增 `TestClientConnectionSweeperSurvivesFailedRounds`：`recordingClientConnectionRepo{err: ...}` + `recordingReapGauges`，断言 `gauges.calls` 里 `result=="error"` 的样本数 `>= 3`（这是告警能工作的直接证据），且 `Run` 返回 `context.Canceled`。

- [x] **Step 2: 运行确认失败**

Run: `go test ./internal/server -run 'SweeperSurvivesFailed' -count=1`
Expected: 三条 FAIL，形如 `Run error = db down, want context canceled` / `error samples = 1, want at least 3`（旧实现在首轮失败时立刻返回，`repo.cutoffs` 停在 1）。

- [x] **Step 3: 最小实现**

三处 `Run` 统一为「保留 nil 守卫 → 委托 `runSweepLoop`」，`name` 分别为 `client_metadata`、`webssh_session`、`client_connection_lease`：

```go
func (s *ClientConnectionSweeper) Run(ctx context.Context) error {
	// 仓储缺失是接线错误，不是一次可重试的失败：它必须在进循环之前就返回，
	// 否则一个永远不可能成功的 goroutine 会按 tick 周期刷错误日志。
	if s == nil || s.leases == nil {
		return errors.New("client connection lease sweeper is unavailable")
	}
	return runSweepLoop(ctx, "client_connection_lease", s.interval, func(ctx context.Context) error {
		_, err := s.SweepOnce(ctx)
		return err
	})
}
```

`ClientMetadataSweeper` 与 `WebSSHSweeper` 的 `Run` 用同样结构，`pass` 分别为 `func(ctx context.Context) error { _, err := s.Sweep(ctx, time.Now().UTC()); return err }`（`WebSSHSweeper` 的 nil 守卫返回 `ErrWebSSHSessionUnavailable`，`ClientMetadataSweeper` 返回 `ErrSessionClosed`，与 `Sweep` 现有哨兵保持一致）。三个 `pass` 都写成闭包而不是方法值：`Sweep`/`SweepOnce` 返回 `(int64, error)`，签名不匹配 `sweepPass`，而返回的行数在旧行为里从来没有被 `Run` 使用过（各清扫器成功时不打日志，只有 audit 与 auth 在成功分支自己打），所以不为本次重构修改它们的签名。

- [x] **Step 4: 运行确认通过**

Run: `go test ./internal/server -run 'Sweeper' -count=1`
Expected: 新用例通过，且既有 `TestClientConnectionSweeperWithoutGaugesOrRepository`（断言 `Run(context.Background())` 必须返回错误）、`TestClientMetadataSweeperMarksExpiredAndStops`、`TestWebSSHSweeperRunsUntilCancel`、`TestServerRuntimeCloseStopsWebSSHSweeper` 全部继续通过。

Run: `go test ./internal/server -count=1`
Expected: `ok`（整包不因循环改动而回归）。

---

### 任务 3: 已正确的两个清扫器收敛到同一策略（纯重构）

**Files:**
- Modify: `internal/server/audit_retention_sweeper.go:111-129`
- Modify: `internal/server/auth_maintenance_sweeper.go:86-107`

- [x] **Step 1: 确认基线并声明不变量**

Run: `go test ./internal/server -run 'AuditRetention|AuthMaintenance' -count=1`
Expected: `ok`——这是重构的安全网，若此处已红必须先停手排查，不得带着红灯重构。

不变量：日志事件名 `audit_retention_sweep_failed`/`auth_maintenance_sweep_failed` 与成功日志 `audit_retention_swept`（字段 `deleted_rows`、`retention_days`、`duration_ms`）、`auth_maintenance_sweep`（字段 `challenges`、`devices`）必须逐字保持，包括"仅在删除行数 > 0 时打印成功日志"的条件。

- [x] **Step 2: 实现**

两个 `Run` 改为「保留 nil 守卫 → 委托 `runSweepLoop(ctx, "audit_retention"|"auth_maintenance", s.interval, s.sweepAndLogWrapped)`」；把现有 `sweepAndLog` 改造成 `sweepPass` 形态：删除其中的错误 `switch`（取消判定与错误日志由 `runSweepLoop` 统一负责），只保留成功日志。具体是把 `func (s *AuditRetentionSweeper) sweepAndLog(ctx context.Context)` 改为 `func (s *AuditRetentionSweeper) sweepOnceAndLog(ctx context.Context) error`，末尾 `return err`，成功分支日志不变。

- [x] **Step 3: 运行确认通过**

Run: `go test ./internal/server -run 'AuditRetention|AuthMaintenance|Runtime' -count=1`
Expected: `ok`，包括 `TestAuthMaintenanceSweeperRunStopsOnCancel`（断言 `Run` 返回 `context.Canceled`）与 `TestNewServerRuntimeStartsAuditRetentionOnlyWhenConfigured`。

Run: `go test -race ./internal/server -run 'Sweep|Runtime' -count=1`
Expected: `ok`（竞态覆盖循环与 goroutine 交接）。

---

### 任务 4: Server 入口走 `cli.Main`

**Files:**
- Modify: `cmd/tunnelmesh-server/main.go`
- Test: `internal/cli/exit_test.go`、`scripts/service_lifecycle_test.go`

- [x] **Step 1: 写失败测试**

1. `internal/cli/exit_test.go` 新增 `TestServerMainReturnsConfigExitCode`：与既有 `TestRunWithUnreadableConfigExitsWithConfigCode` 同夹具（`mode: [unclosed` 的 YAML），但调用 `Main(root)` 而不是 `Execute`，断言返回值 `== workerproto.ExitConfig`。这是 Server 侧第一次真正覆盖 `Main` 的映射路径（`Main` 只读 `SetArgs`，进程信号处理器的安装与 `defer stop()` 在测试内是安全的）。
2. 新增 `scripts/service_lifecycle_test.go`（package `scripts`，与 `vpn_build_tag_test.go` 同为 `go/parser` 静态守卫），`TestEveryBinaryEntrypointUsesCLIMain`：解析仓库内 `cmd/*/main.go`（用 `filepath.WalkDir("..")`，只取 `cmd/` 下 `package main`），对每个 `main()` 函数体断言调用了 `cli.Main`，且文件不含 `log.Fatal`。错误信息要指出文件名与"入口必须把信号处理与退出码映射交给 cli.Main"。

- [x] **Step 2: 运行确认失败**

Run: `go test ./internal/cli -run TestServerMainReturnsConfigExitCode -count=1 && go test ./scripts -run TestEveryBinaryEntrypoint -count=1`
Expected: 前者 FAIL（`Main` 目前的行为没有 Server 用例覆盖，`NewServerRoot` 下返回 1 而不是 78 —— 若实现前 `Main` 已正确返回 78，则该用例在 Step 1 阶段即为绿灯，此时以 `scripts` 守卫的红灯为准，并在执行记录里注明）；后者 FAIL 并列出 `cmd/tunnelmesh-server/main.go`。

- [x] **Step 3: 实现**

`cmd/tunnelmesh-server/main.go` 全文替换为与 agent/client 同构：

```go
// Command tunnelmesh-server starts the TunnelMesh server process.
package main

import (
	"os"

	"github.com/tunnelmesh/tunnelmesh/internal/cli"
)

func main() {
	os.Exit(cli.Main(cli.NewServerRoot()))
}
```

不需要改 `internal/cli/root.go`：`run` 已把 `cmd.Context()` 交给 `storage.OpenConfig` 与 `runtime.Serve`，`ServeListener` 在取消时完成 drain 并返回 `nil`，`ExitCodeFor` 据此给出 0。

- [x] **Step 4: 运行确认通过**

Run: `go build ./... && go test ./internal/cli ./scripts -count=1`
Expected: `ok` 两个包。

- [x] **Step 5: 手工验证优雅停机（一次性，不入库）**

Run: `go build -o /tmp/tm-server ./cmd/tunnelmesh-server && (TUNNELMESH_NODE_ID_PATH=/tmp /tmp/tm-server --config <(printf 'mode: local\nstorage:\n  driver: sqlite\n  sqlite:\n    path: /tmp/tm-lifecycle.db\nserver:\n  http_addr: 127.0.0.1:18099\n') run &) ; sleep 3; pkill -TERM -f /tmp/tm-server; wait; echo "exit=$?"`
Expected: 输出 `exit=0`，且 journal 之外没有任何 `fatal` 字样。本机 macOS 无 `systemd` 时改用 `go run` + 直接 `kill -TERM` 的等价验证；无法验证时在 PR 记录的 Test Evidence 中写明"仅由单元测试覆盖取消路径"，不得写成已通过。

---

### 任务 5: Server 的 systemd 单元与其余角色对齐

**Files:**
- Modify: `deploy/systemd/tunnelmesh-server.service`
- Modify: `deploy/systemd-user/tunnelmesh-server.service`
- Test: `scripts/service_lifecycle_test.go`

- [x] **Step 1: 写失败测试**

在 `scripts/service_lifecycle_test.go` 新增 `TestLongLivedUnitsAlwaysRestart`：遍历 `deploy/systemd/tunnelmesh-*.service` 与 `deploy/systemd-user/tunnelmesh-*.service`，逐个断言包含 `Restart=always`、`RestartPreventExitStatus=78 77`；并对两个 `tunnelmesh-server.service` 额外断言包含 `TimeoutStopSec=60s`。注释说明契约来源：`internal/workerproto/workerproto_test.go` 里"退出码是与单元文件的契约"。

- [x] **Step 2: 运行确认失败**

Run: `go test ./scripts -run TestLongLivedUnits -count=1`
Expected: FAIL，列出两个 server 单元缺 `Restart=always`。

- [x] **Step 3: 实现**

两个 server 单元里把

```
Restart=on-failure
RestartSec=5s
```

替换为（与 `deploy/systemd/tunnelmesh-agent.service` 同样的两行中文注释，措辞按 Server 语境调整）：

```
# 长驻服务不以退出码表达意图：入口返回 0 同样可能只是监听器被意外关掉，所以任何自行退出都要拉起。
Restart=always
RestartSec=5s
# 78 是配置错误，77 是凭据被永久拒绝：重启修不好它们，不应被 systemd 无限拉起。
# 同一份契约也覆盖 ExecStartPre 的 check-config——它现在同样以 78 退出。
RestartPreventExitStatus=78 77
# 优雅停机上界：HTTP drain 5s + server.vpn.shutdown_timeout（默认 15s），留出裕量。
TimeoutStopSec=60s
```

- [x] **Step 4: 运行确认通过**

Run: `go test ./scripts -count=1`
Expected: `ok`。

- [x] **Step 5: 渲染回归**

Run: `go test ./deploy/install/oneclick -run Server -count=1`
Expected: `ok`（e2e 用真实模板文件渲染单元，改动若破坏占位符替换会在这里暴露；若本机 `docker`/`bash` 条件不满足导致 SKIP，在 PR 记录里注明 SKIP 原因）。

---

### 任务 6: 租约回收的两条告警规则

**Files:**
- Modify: `deploy/prometheus/alert-rules.yaml`
- Test: 无 Go 测试；以 `promtool` 或 YAML 解析校验

- [x] **Step 1: 实现**

在 `tunnelmesh-alerts` 组末尾追加，并按既有中文注释风格写明"为什么必须靠指标而不是进程存活"和"为什么全局 `sum` 是正确语义而不是妥协"：

```yaml
      # 租约回收是死行离开表的唯一路径：Release 需要对端的配合，被杀掉、掉电、断网都不会走到它。
      # 清扫循环遇到数据库错误只记录不退出，所以"没在回收"不再会让 Server 崩掉，只能靠指标发现：
      # 一条是持续失败，一条是彻底停摆。删除语句以时间戳为边界且幂等，任一节点跑成功一轮就够，
      # 因此这里的 sum 是跨节点求和——它表达的是"没有任何节点在回收"，正是需要叫醒人的那一件事。
      - alert: TunnelMeshLeaseSweepErrors
        expr: sum(increase(tunnelmesh_client_connection_lease_sweep_total{result="error"}[15m])) > 3
        for: 15m
        labels: {severity: warning}
        annotations: {summary: TunnelMesh client connection lease reaping keeps failing}
      - alert: TunnelMeshLeaseSweepStalled
        expr: absent(tunnelmesh_client_connection_lease_sweep_total) or sum(increase(tunnelmesh_client_connection_lease_sweep_total[10m])) == 0
        for: 5m
        labels: {severity: warning}
        annotations: {summary: TunnelMesh client connection lease reaping is not running}
```

- [x] **Step 2: 校验**

Run: `promtool check rules deploy/prometheus/recording-rules.yaml deploy/prometheus/alert-rules.yaml`
Expected: `SUCCESS: ... rules found`（alert-rules 由 9 条增至 11 条）。本机无 `promtool` 时改用 `python3 -c "import yaml; d=yaml.safe_load(open('deploy/prometheus/alert-rules.yaml')); print(len(d['groups'][0]['rules']))"`，期望输出 `11`，并在 PR 记录中注明 promtool 未执行。

---

### 任务 7: 文档与记录

**Files:**
- Modify: `docs/operations/observability.md`、`docs/deployment/linux-systemd.md`、`docs/operations/configuration.md`、`docs/operations/troubleshooting.md`
- Create: `docs/pull-requests/2026-11-05-sweeper-resilience-and-server-lifecycle.md`
- Regenerate: `docs/superpowers/plans/README.md`、`docs/pull-requests/README.md` 等索引

- [x] **Step 1: 活文档同步**

- `docs/deployment/linux-systemd.md:57`：把"`client` 与 `agent` 的单元使用 `Restart=always`…`server` 的单元保持 `Restart=on-failure`"改写为三个角色统一 `Restart=always` + `RestartPreventExitStatus=78 77`，补一句 Server 的 `TimeoutStopSec=60s` 与 drain 组成，并说明 Server 的 `run` 现在同样以 78/77 退出。
- `docs/operations/configuration.md:515-523`：退出码表下方"因此 client/agent 的单元使用 `Restart=always`…`server` 仍用 `Restart=on-failure`"改为三角色统一；表内 `0` 行的 systemd 列由「是（client/agent 用 `Restart=always`）」改为「是（三个角色都用 `Restart=always`）」。
- `docs/operations/observability.md`：在租约回收段落把"建议告警（起点值）"替换为已交付的 `TunnelMeshLeaseSweepErrors` / `TunnelMeshLeaseSweepStalled` 两条规则名与阈值；新增一句说明五个清扫事件名（`client_metadata_sweep_failed`、`webssh_session_sweep_failed`、`client_connection_lease_sweep_failed`、`audit_retention_sweep_failed`、`auth_maintenance_sweep_failed`）是"一轮失败"的唯一日志出口，出现即代表该任务还在重试而不是已经放弃。
- `docs/operations/troubleshooting.md`：新增两条排查条目——(a) Server 收到 `SIGTERM` 后立即消失、活跃隧道没有 drain（旧入口无信号处理，升级即修复，可用 `systemctl show -p ExecMainPID` 与日志时间差核对）；(b) 客户端详情里过期租约又开始堆积（先查 `TunnelMeshLeaseSweep*` 告警与 `client_connection_lease_sweep_failed` 日志，再查数据库可达性）。

- [x] **Step 2: 重建索引**

Run: `python3 scripts/gen_doc_index.py`
Expected: 生成的 README 里出现本计划与 PR 记录条目；禁止手工编辑这些文件。

- [x] **Step 3: 写 PR 描述**

按 AGENTS.md 要求创建 `docs/pull-requests/2026-11-05-sweeper-resilience-and-server-lifecycle.md`，包含标题、目标分支 `main`、摘要、用户影响（Server 停止/重启行为变更、坏配置不再无限重启）、API/Schema/配置影响（无）、安全与授权影响（无新增暴露；日志不含敏感值）、测试证据（逐条命令与实际输出）、发布步骤、回滚步骤、Reviewer 关注点、集成状态。

---

## 验证命令（合并前必须全部执行）

```bash
go build ./...
go test ./... -count=1
go test -race ./internal/server ./internal/cli ./internal/storage -count=1
go vet ./...
gofmt -l internal/ cmd/ scripts/
go test ./scripts -count=1
promtool check rules deploy/prometheus/recording-rules.yaml deploy/prometheus/alert-rules.yaml   # 缺 promtool 时用 Step 2 的 YAML 回退
python3 scripts/gen_doc_index.py && python3 scripts/gen_doc_index.py
git diff --check
```

索引生成器没有 `--check` 模式，因此用「连续跑两遍」验证幂等：第二次运行之后 `git status --porcelain docs` 里只应出现预期的 README 与记录文件，不应有反复变化的索引。

前端无改动，因此不执行 `npm test` / `npm build`；但必须在 PR 记录里写明"前端未触及"。已知与本计划无关的既有 flake：`TestServerNodeAPIListDetailLifecycleAndAudit`（已在 PR #88 记录），如遇到按既有事实说明，不算本改动回归。

## 回滚注意事项

- 应用版本为 **PATCH**，无 Schema、无配置、无 API 变更，回滚 = 重新部署旧二进制 + 旧单元文件，5 分钟内可完成，无数据动作。
- 清扫器改动可单独回滚：`git revert` 任务 2/3 的提交即恢复"失败即退出"的旧行为；`internal/server/sweep_loop.go` 保留不产生任何影响（无调用方）。
- 单元改动风险独立于代码：若 `Restart=always` 造成重启风暴，先 `systemctl edit` 加 drop-in 写回 `Restart=on-failure`（与 `docs/operations/troubleshooting.md` 已有的应急写法一致），再单独回滚两个 server 单元文件；风暴本身由 `RestartPreventExitStatus=78 77` 与 systemd 默认速率限制共同约束。
- 入口改动是唯一改变运维可见行为的部分：回滚后 Server 重新变成"无信号处理、恒以 1 退出"，因此若单元已更新为 `Restart=always`，必须一并回滚单元，否则坏配置的无限重启防护会失效。
- 告警规则可独立回滚：删除两条规则即可，指标本身（`tunnelmesh_client_connection_lease_sweep_total`）由 PR #88 引入，不受影响。

## 执行记录

分支 `codex/sweeper-resilience-and-server-lifecycle`，基线 `238857d`。7 个任务全部按计划完成，以下为
实际结果与计划的差异，只记录已验证事实。

- 任务 1：`runSweepLoop`/`runSweepPass`/`sweepPass`/`errInvalidSweepInterval` 与计划一致，6 个测试先以
  `undefined: runSweepLoop`（6 处）红灯，实现后绿灯。计划外发现：`recordingClientConnectionRepo` 与
  `recordingReapGauges` 两个测试替身被 `Run` 的 goroutine 和测试协程无同步地同时读写，
  `go test -race ./internal/server -run TestClientConnectionSweeperRunSweepsBeforeTheFirstTick` 在**改动前的
  `main` 上**就报 data race（既有测试自身的缺陷，因 CI 从不跑 `go test -race ./internal/server` 而长期未暴露）。
  由于本任务的夹具必须在同一文件里被循环调用，已给这两个替身加互斥量与 `rounds()`/`errorSamples()` 访问器；
  这是本计划范围内唯一的测试基建修复，未改动被测生产代码。
- 任务 2：三个红灯输出与计划预测一致（均为 `Run error = database is not reachable, want context canceled
  after 1 rounds`，即旧实现在首轮失败时返回）。`name` 取 `client_metadata`、`webssh_session`、
  `client_connection_lease`。计划正文「各清扫器成功时不打日志」一句不准确：`ClientConnectionSweeper.SweepOnce`
  每轮都打一条 INFO（含 `cutoff`/`reaped`/`elapsed_ms`/`error`，且已被 `docs/operations/troubleshooting.md`
  引用为运维入口），因此保持不动，代价是一轮失败同时出现 INFO 与 ERROR 两行——INFO 回答「这一轮做了什么」，
  ERROR 是新增的告警与 grep 出口。三个 `pass` 按计划写成闭包，未改任何 `Sweep`/`SweepOnce` 签名。
- 任务 3：`audit_retention`、`auth_maintenance` 收敛到同一循环，`sweepAndLog` 改为 `sweepPass` 形态
  （返回 `error`），成功日志的事件名与字段逐字未变；`audit_retention_sweeper.go` 的 `errors` 导入随
  `switch` 一并移除。重构前基线 `-run 'AuditRetention|AuthMaintenance'` 为绿，重构后仍为绿，测试未修改。
- 任务 4：`TestServerMainReturnsConfigExitCode` 在实现前即为绿，符合计划中预先声明的情形——`cli.Main`
  的映射本身一直正确，缺的是 Server 入口没有使用它；本任务的实际红灯来自 `scripts` 守卫
  （`../cmd/tunnelmesh-server/main.go does not call cli.Main` + `calls log.Fatal inside main`）。
  计划外调整：守卫最初覆盖 `cmd/*/main.go` 全部四个入口，`cmd/tunnelmesh-client-tray` 随即报错。它是
  `//go:build tray` 的桌面程序，在主线程驱动 AppKit/WebView2 事件循环并自带信号处理，套用 `cli.Main`
  会与那套设计冲突，因此守卫改为只检查**无构建约束**的入口（复用本包既有的 `buildConstraintOf`），
  理由写进了测试注释。
- 任务 4 Step 5：本机无 systemd，改用真实进程对照取证，方法是在 `main` 的 `git worktree` 里构建改动前的
  `tunnelmesh-server`，两者都以 `check-config` 通过、`/health/ready` 返回 200 后 `kill -TERM`：
  改动前 `sigterm_exit_code=143`（被信号直接带走，drain 未执行），改动后 `sigterm_exit_code=0`。
  同一对二进制用已知坏配置跑 `check-config` 与 `run`：改动前均为 `1`（在 `Restart=on-failure` 下即无限重启），
  改动后均为 `78`。
- 任务 5：红灯精确落在两个 Server 单元（缺 `Restart=always`、缺 `RestartPreventExitStatus=78 77`、缺
  `TimeoutStopSec=`）。按计划落地时额外写入 `KillSignal=SIGTERM`（与 client 单元一致，让「停止用的就是
  受支持的那个信号」在单元里显式可见）。`-run Server` 默认会 SKIP 真正的安装 e2e，补 `TM_ONECLICK_E2E=1`
  复跑，`TestOneClickServerUserModeInstall` 通过，说明改动后的用户级模板渲染路径仍然可用。
- 任务 6：本机与容器均无 `promtool`（docker daemon 未运行），因此**未执行** `promtool check rules`；
  按计划回退用 `ruby -ryaml` 解析，`rules=11`（既有 9 + 新增 2），两条新规则的 `expr` 已回读核对。
  这是本计划唯一未能在本地完成校验的项，需在 CI 或 reviewer 环境补 `promtool`。
- 任务 7：除计划列出的四处文档，另在 `docs/operations/observability.md` 新增「后台清扫任务」一节（五个
  任务、一份策略、五个日志事件名、为何进程存活不构成健康依据、为何不用分布式锁），并把两条新告警写进
  「告警责任」的值班归属；`docs/en/` 下没有这两篇的英文镜像，无需同步。
