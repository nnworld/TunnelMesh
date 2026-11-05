# Client 缩容误终止会话池导致服务静默退出实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `tunnelmesh-client run` 只有在“确实被要求停止”时才以退出码 0 结束：连接池缩容不得终止整个会话池，会话角色意外结束时进程必须返回非 0，使内置 supervisor 重启 worker、systemd 拉起服务。

**Architecture:** 三个层次各自收紧，互不依赖。第一层修根因：`agentSessionPool` 用 slot 上的“主动缩容”标记区分“缩容取消”和“链路终止故障”，只有后者才允许投递 `errCh`。第二层修语义：`Runtime.runRoles` 不再把“上下文仍在运行而角色已退出”上报为成功，返回新的哨兵错误 `ErrRunEndedUnexpectedly`，从而经 `cli.ExitCodeFor` 得到非 0 退出码，supervisor 按既有崩溃路径退避重启 worker。第三层修运维兜底：交付的 systemd 单元对 client/agent 使用 `Restart=always`，因为 exit 0 不再能证明进程是“按预期结束”的，`RestartPreventExitStatus=78 77` 继续阻止对配置错误和凭据致命错误的无限拉起。

**Tech Stack:** Go 标准库（`context`、`errors`、`sync/atomic`）、现有 `internal/client` 会话池与 `Runtime`、`internal/cli` 退出码映射、`deploy/systemd*` 单元模板、`scripts/gen_doc_index.py`。不新增第三方依赖，不涉及协议帧、Schema、配置项或前端。

**Spec:** 无新增协议与 Schema。行为契约与背景引用：[ADR 0003 受限自更新与进程守护](../../architecture/adr/0003-constrained-self-update-and-supervision.md)、会话池设计计划 [2026-09-11 client-agent-session-pool](2026-09-11-client-agent-session-pool.md)、[退出码与升级运维文档](../../operations/configuration.md)、[Linux systemd 部署](../../deployment/linux-systemd.md)。AGENTS.md 相关约束：外部依赖必设超时与退避重试、会话/流必须正确处理关闭与 stale generation、故障 5 分钟内可回滚、核心逻辑必须有单元测试。

## 背景：现场证据（2026-11-05，一台生产 Client，v1.3.8）

- 代码基线：现场部署的二进制是 `v1.3.8`，与拉取后的 HEAD（`66863b4`，tag `v1.3.8`）一致，下列行号即现场行为，无需再区分“部署版”和“开发版”。
- `v1.3.7..v1.3.8` 的新增内容——agent 原始 TCP/QUIC 传输（ADR 0004）、admin 脚本执行（ADR 0005）、client 系统清单与状态目录回退——都没有触及 `evaluate`/`startSlot`/`runRoles`/`ExitCodeFor` 的判定逻辑，本计划结论在新代码上逐处复核后成立。
- 现象：`tunnelmesh-client.service` 自 12:03:40 起 `inactive (dead)`，6021-6028/18080/10866/10867 全部无人监听，systemd 未做任何重启尝试。
- 单元状态：`Restart=on-failure`、`Result=success`、`ExecMainCode=1`(exited)、`ExecMainStatus=0`、`NRestarts=0`。`enabled` 只影响开机自启，进程退出后的拉起完全由 `Restart=` 决定，因此 exit 0 不会被拉起。
- 现场共有两次同签名退出（`11-05 03:59:42`、`11-05 12:03:40`），都发生在公网 SSH 扫描流量造成的批量 `client: tcp forward bridge failed: ... client session closed` 之后数秒至数十秒；两次 supervisor 都没有打印 `worker exited; restarting`，说明 worker 的退出码是 0。对照 `10-31 17:37:33` 的 `exit_code=1`，supervisor 正常退避重启，最终连续 5 次不就绪时以 70 放弃，链路本身工作正常。
- 现场配置 `client.connections = {min: 1, max: 4, high_watermark: 16, low_watermark: 2, evaluation_interval: 10s, cooldown: 30s}`：扫描 burst 触发扩容，burst 结束后的 idle 触发缩容，缩容是每次退出的直接前因。
- 暴露面判定：`client.connections.max` 默认为 1（`internal/config/config.go:865`），默认配置下 `slotCount > Min` 永远不成立、不会缩容，因此该缺陷只影响显式配置 `max > min` 的部署。所有开了连接池弹性的 Client 都在窗口期内，不是这一台机器的偶发问题。
- 现场未复现的对照：`16:14` 施加 `Restart=always` 后至今 `NRestarts=0` 且无 `bridge failed`，说明触发依赖 burst→idle 的时机；兜底生效后，即使再次触发也只会中断约 5 秒而不是永久失联。
- 已执行的现场止血（与本计划独立，可随时删除回退）：`/etc/systemd/system/tunnelmesh-client.service.d/50-restart-always.conf` 提供 `Restart=always` + `RestartSec=5s` + `RestartPreventExitStatus=78 77`；服务已恢复，`6021/6022/6028` 经隧道抓到真实 SSH banner。

## 根因链路（代码事实）

1. `internal/client/session_pool.go:416` —— `evaluate` 缩容时只调用 `scaleDownSlot.cancel()`，没有任何“这是一次主动缩容”的记录。
2. `internal/client/session_pool.go:361-377` —— slot 的 runner 返回后，goroutine 先无条件执行 `p.removeSlot(slotID)`（`internal/client/session_pool.go:457` 内部会 `slot.cancel()`），再用 `runCtx.Err() == nil` 作为唯一判据决定是否投递 `p.errCh`。此时 `runCtx`（连接池上下文）在缩容场景仍然存活，而 `slotCtx` 必然已被取消，因此“缩容取消”产生的 `context.Canceled` 与真实链路故障无法区分，会被当作终止错误投递。
3. `internal/client/session_pool.go:257` 与 `:269` —— `agentSessionPool.run` 把 `errCh` 的任何值视为终止失败，执行 `p.cancelAll()` 后返回，连带拆除本应继续存活的那个 slot。
4. `internal/client/session_pool.go:89-113` —— `SessionPoolManager.Run` 原样返回 `context.Canceled`。
5. `internal/client/runtime.go:344-356` —— `runRoles` 用 `isContextErr` 丢弃 context 类错误，末尾 `return ctx.Err()`；外层运行上下文并未取消，于是返回 `nil`。
6. `internal/cli/client_run.go:46` —— `runClientTunnels` 把 `runtime.Wait` 的 `nil` 透传；`internal/cli/exit.go:38` —— `ExitCodeFor(nil, ...)` 得到 `workerproto.ExitOK`。
7. `internal/supervisor/supervisor.go:205-210` —— supervisor 见 worker `ExitOK` 即“跟随正常结束”，主进程以 0 退出；`deploy/systemd/tunnelmesh-client.service:14` 的 `Restart=on-failure` 对 exit 0 不重启，服务静默死亡。

## Global Constraints

- 不改协议帧、能力协商、数据库 Schema、配置项集合与默认值（`update.supervise` 保持默认 `true`）。
- 正常停止语义必须原样保留：`Stop()`、SIGINT、SIGTERM 仍以退出码 0 结束，`internal/client/runtime_test.go:451` 与 `internal/cli/exit_test.go:77` 是既有契约，不得为通过新测试而修改它们。
- 缩容只允许取消被选中的那一个 slot；其余会话必须继续服务，且后续仍可再次扩容。
- 真实终止错误（`parseClientWebSocketURL` 失败、`ErrClientTokenRequired`、`onReady` 返回 `ErrSessionClosed`、metadata 采集失败）必须继续上抛并结束运行，不得被新守卫吞掉。
- `runRoles` 中“嵌入 agent 角色失败不拖垮隧道”的既有语义（`internal/client/runtime.go:330-338`）保持不变。
- 注释只解释“为什么”，遵循现有包内风格。
- 每个任务先写失败测试并观察红灯，再做最小实现；`go test -race` 必须覆盖并发取消路径。
- 未获用户明确授权，不执行 commit、push、merge 或创建远端 MR。

---

### Task 1: 缩容取消不再终止连接池

**Files:**
- Modify: `internal/client/session_pool.go`
- Test: `internal/client/session_pool_test.go`

**Interfaces:**
- `type sessionPoolSlot struct { id int; cancel context.CancelFunc; scaledDown atomic.Bool }`（`internal/client/session_pool.go:194`，新增包内私有字段；`sync/atomic` 已在本文件使用）
- `func (p *agentSessionPool) removeSlot(slotID int) *sessionPoolSlot`（当前无返回值；改为返回被移除的 slot，slot 不存在时返回 `nil`）
- 签名保持不变：`func (p *agentSessionPool) startSlot(ctx context.Context)`、`func (p *agentSessionPool) evaluate(ctx context.Context)`、`SessionPoolManagerOptions`、`SessionPoolRunner`。

**TDD:**
- [ ] 写失败测试 `TestSessionPoolScaleDownDoesNotStopPool`，复用 `newPoolTestRunner`、`waitPoolTransport`、`waitForCondition`（均已存在于 `internal/client/session_pool_test.go`）与 `SessionPoolConfig{Min: 1, Max: 2, HighWatermark: 1, LowWatermark: 0, EvaluationInterval: 10 * time.Millisecond, Cooldown: 10 * time.Millisecond}`：先按 `TestSessionPoolScalesUpAndDown` 的方式开一条 stream 触发扩容到 2 个 slot，记录幸存的第一个 transport；随后关闭 stream 让 active 回落到 `LowWatermark` 以下触发缩容。断言三件事：`manager.Run(ctx)` 的 `done` 通道在缩容后 500ms 内没有返回；`manager.OpenCount("agent-a") == 1` 且 `ReadyCount("agent-a") == 1` 保持稳定；用同一 `opener` 再次 `OpenStream` 仍然成功且落在第一个 transport 上。
- [ ] 运行 `go test ./internal/client -run 'TestSessionPoolScaleDown' -count=1`，确认红灯。

**预期失败结果：** `done` 立即收到 `context.Canceled`（`errCh` 被投递了缩容取消的错误），`OpenCount`/`ReadyCount` 归 0，第二次 `OpenStream` 返回“无可用会话”类错误——与现场退出前的行为一致。

**最小实现：**
- [ ] `evaluate` 在缩容分支先 `scaleDownSlot.scaledDown.Store(true)` 再 `scaleDownSlot.cancel()`，并加注释说明“标记先于取消，runner 侧才能据此区分缩容与故障”。
- [ ] `removeSlot` 返回被移除的 slot。
- [ ] `startSlot` 的 runner goroutine 改为 `slot := p.removeSlot(slotID)`，投递条件收紧为 `err != nil && runCtx != nil && runCtx.Err() == nil && (slot == nil || !slot.scaledDown.Load())`。
- [ ] 重跑 Task 1 测试与 `go test ./internal/client -count=1`，确认绿灯且 `TestSessionPoolRunnerErrorFailsRun`、`TestSessionPoolScalesUpAndDown` 不退化。

**预期通过结果：** 缩容后连接池继续运行、剩余会话保持就绪并可再次扩容；真实 runner 故障仍然使 `manager.Run` 返回该故障错误。

---

### Task 2: 会话角色意外结束必须以非 0 退出

**Files:**
- Modify: `internal/client/runtime.go`
- Test: `internal/client/runtime_test.go`
- Test: `internal/cli/client_runtime_test.go`

**Interfaces:**
- 新增 `var ErrRunEndedUnexpectedly = errors.New("client: tunnel session ended while the client was still running")`（`internal/client/runtime.go`，与 `internal/client/session.go:17` 的 `ErrSessionClosed` 同风格）。
- `func (r *Runtime) runRoles(ctx context.Context) error` 结尾语义调整为：`firstErr != nil` 时返回 `firstErr`；否则 `ctx.Err() != nil` 时返回 `ctx.Err()`；否则返回包装了“被 `isContextErr` 丢弃的最后一个角色错误”的 `fmt.Errorf("%w: %v", ErrRunEndedUnexpectedly, dropped)`，用于日志定位。
- 无 CLI 签名变化：`runClientTunnels` 继续透传 `runtime.Wait`，`ExitCodeFor` 在 `ctx.Err() == nil` 时经 `workerproto.ExitCodeOf` 得到 `workerproto.ExitGeneric`(1)。

**TDD:**
- [ ] 写失败测试 `TestRuntimeWaitFailsWhenSessionRoleEndsUnexpectedly`（`internal/client/runtime_test.go`）：用既有 `runtimeTestConfig` 与 `RuntimeOptions{Stdout: &stdout, SkipLock: true, Runner: ...}`，Runner 直接 `return context.Canceled` 模拟“被取消的 slot 上报 context 错误”；`Start` 后 `Wait(ctx)` 必须在 1s 内返回 `errors.Is(err, ErrRunEndedUnexpectedly)`。
- [ ] 写失败测试 `TestClientRunExitsNonZeroWhenSessionRoleEndsUnexpectedly`（`internal/cli/client_runtime_test.go`）：沿用 `runClientSessionPool` 替换 + `reserveLoopbackListenAddress` + 临时 `client.yaml` 的既有模式，Runner 返回 `context.Canceled`，断言 `workerproto.ExitCodeOf(Execute(ctx, root)) == workerproto.ExitGeneric`。
- [ ] 运行 `go test ./internal/client ./internal/cli -run 'Unexpected' -count=1`，确认两处红灯（当前分别得到 `nil` 与 0）。

**预期失败结果：** `Runtime.Wait` 返回 `nil`；`Execute` 返回的 error 映射为 `workerproto.ExitOK`。这正是服务静默死亡的可观测形式。

**最小实现：**
- [ ] 按上述接口改 `runRoles` 尾部，并在 `for i := 0; i < roles; i++` 循环里记录被丢弃的 context 类错误；不改 `isContextErr` 本身。
- [ ] 重跑 `go test ./internal/client ./internal/cli -count=1`，确认绿灯；特别确认 `internal/client/runtime_test.go:451`（`Stop` 后为 `nil` 或 `context.Canceled`）与 `internal/cli/exit_test.go:88`（“cancelled error but not asked to stop”→ `ExitGeneric`）未被削弱。

**预期通过结果：** 任何“角色自己退出但没人要求停止”都产生非 0 退出码；`update.supervise=true` 时 supervisor 走既有崩溃分支打印 `worker exited; restarting` 并退避重启 worker，`supervise=false` 时 systemd `Restart=on-failure` 也能拉起。

---

### Task 3: systemd 单元模板与活文档对齐

**Files:**
- Modify: `deploy/systemd/tunnelmesh-client.service`
- Modify: `deploy/systemd/tunnelmesh-agent.service`
- Modify: `deploy/systemd-user/tunnelmesh-client.service`
- Modify: `deploy/systemd-user/tunnelmesh-agent.service`
- Modify: `docs/deployment/linux-systemd.md`
- Modify: `docs/operations/configuration.md`
- Regenerate: `docs/superpowers/plans/README.md`（`python3 scripts/gen_doc_index.py`）

**Interfaces:**
- client/agent 四个模板：`Restart=on-failure` → `Restart=always`，保留 `RestartSec=5s` 与 `RestartPreventExitStatus=78 77`，各加一行中文注释说明 exit 0 不再能证明进程是自愿结束。
- `deploy/systemd/tunnelmesh-server.service` 与 `deploy/systemd-user/tunnelmesh-server.service` 不改：Server 的 exit 0 语义不在本计划审计范围。
- `docs/operations/configuration.md:519` 退出码表 `0` 行的 systemd 列：由「否」改为「是（`Restart=always`）；`systemctl stop` 是停止任务，不会重启」。
- `docs/deployment/linux-systemd.md:57` 段落同步：client/agent 采用 `Restart=always` + `RestartPreventExitStatus=78 77`，并说明 supervisor 以 70 放弃时 `Restart=always` 仍会重试一次全新进程。

**TDD:** 无自动化测试可用（模板与文档）。以命令验证代替：
- [ ] `grep -n 'Restart' deploy/systemd/*.service deploy/systemd-user/*.service` 输出符合预期（四个 client/agent 为 `always`，两个 server 保持 `on-failure`）。
- [ ] `go test ./scripts -count=1`（`scripts/install_script_test.go` 校验发布归档包含单元模板）。
- [ ] `python3 scripts/gen_doc_index.py` 后 `git diff --stat docs/superpowers/plans/README.md` 含本计划条目，`docs/README.md` 的分类索引有入口指向。

**预期通过结果：** 新装机器不再依赖 exit 0 的可信性；已存在的 `on-failure` 单元由运维侧 drop-in 覆盖（现场已施加）。

---

## 任务间依赖

- Task 1 与 Task 2 互不依赖，可并行评审，但必须按 1 → 2 顺序合并：Task 2 的红测试用“Runner 直接返回 `context.Canceled`”构造，不依赖 Task 1 的实现细节。
- Task 3 独立于 1、2；即便 Task 1/2 回退，`Restart=always` 仍是现场止损层。
- 三个任务都不改动 `internal/agent` 的连接池实现（`internal/agent/connection_pool.go:42`），agent 侧风险由 Task 3 的单元模板兜底；`internal/agent` 是否存在同类“意外结束上报成功”需另案审计，本计划不宣称其已修复。

## 验证命令

```bash
go test ./internal/client ./internal/cli -count=1
go test -race ./internal/client ./internal/cli ./internal/supervisor
go test ./... -count=1
go vet ./...
git diff --check
```

无前端改动，`web/` 的 `npm test`/`npm run build` 不需要执行。合并后在现场主机验证：`systemctl restart tunnelmesh-client`，确认全部监听端口重新绑定、隧道能取到远端 SSH banner，然后在一个 `evaluation_interval` 周期内不出现 `Deactivated successfully`。

## 回滚注意事项

- 纯代码 + 模板 + 文档变更，不涉及数据库、协议和配置文件；回滚即还原二进制与四个单元模板，5 分钟内可完成，无需数据补偿。
- 若 `Restart=always` 引发重启风暴：还原为 `Restart=on-failure` 即可，风暴本身由 `RestartPreventExitStatus=78 77`（配置/凭据致命）与 supervisor 的 `GiveUpAfter`（exit 70）双重限制。
- 若 Task 2 的语义变化影响到复用 `Runtime` 的系统托盘（`cmd/tunnelmesh-client-tray`）：托盘只把 `Wait` 的错误用于展示停止原因，不参与退出码决策；出现异常时优先回退 Task 2 并保留 Task 1，根因层修复单独生效且不改变停止语义。
- 现场 drop-in `/etc/systemd/system/tunnelmesh-client.service.d/50-restart-always.conf` 与本计划解耦：删除该文件并 `systemctl daemon-reload` 即回到旧行为，不影响后续升级。
