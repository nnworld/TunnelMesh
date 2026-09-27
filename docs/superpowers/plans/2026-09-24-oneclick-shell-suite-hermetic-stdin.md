# 一键安装函数级套件交互挂死修复实施计划

状态：补记计划（原始实现已完成）

补记说明：合并 `main` 后跑全量门禁时发现 `go test ./...` 在**有控制终端**的环境下会卡死在
`deploy/install/oneclick` 的 `TestShellFunctionSuite`，直到 10 分钟包级超时才 panic。按
`AGENTS.md` 的补记条款记录：红灯证据为实现过程中的真实观察，绿灯与门禁结果在补记时重新执行，
见第 7 节。本次改动只落在测试文件、`testdata/` 与一份开发文档，**不含任何产品代码**。

## 1. 目标

1. `deploy/install/oneclick/testdata/run_tests.sh` 在任何环境下都不会因为等待交互输入而阻塞：
   有控制终端、stdin 是终端、stdin 是「打开着但永不写入」的管道，三种情况都必须跑完。
2. 任何测试驱动的 bash 脚本一旦真的停在交互提示上，`go test` 必须在分钟级内失败，并且失败信息
   点名是哪个脚本，而不是等包级 10m 超时后只留一份指不到脚本的 goroutine dump。
3. `tty/value` 断言真的能判定 `tm_tty_init` 的二值契约。

## 2. 非目标

- 不改 `deploy/install/oneclick/tunnelmesh-install-common.sh`：`tm_tty_init` 探测 `/dev/tty`、
  `tm_read_line` 回落 stdin 是给真人安装用的正确行为，缺陷在测试驱动脚本没有切断通道。
- 不改三个 `install-<role>.sh` / `install-<role>.ps1` 发布产物。
- 不引入新的第三方依赖（不用 pty 库造控制终端）。
- 不改 `deploy/install/`（legacy 安装脚本）下的其它测试。

## 3. 根因与证据

共享库的输入通道有三层，测试脚本一层都没设防：

1. `run_tests.sh:46` 为了测 `tm_tty_init` 真的调用了它。有控制终端时 `TM_TTY=/dev/tty`
   （`tunnelmesh-install-common.sh:76-84` 的 `{ : >/dev/tty; } 2>/dev/null` 探测）。
2. `run_tests.sh:342`（修复前行号）`TM_ONECLICK_ALLOW_REMOTE=1 tm_validate_listen "0.0.0.0:18080"`
   → `tm_validate_listen` 清空 `auth_mode` 答案后调用 `tm_ask_choice`
   （`tunnelmesh-install-common.sh:1212,1221`）→ `tm_read_line` → `read -r reply <"$TM_TTY"`
   → 真的去读终端，永久阻塞。
3. 没有控制终端时 `TM_TTY=""`，`tm_read_line` 回落到 stdin；而 `run_tests.sh:122`
   `export TM_ONECLICK_ALLOW_STDIN=1` 让 `tm_require_input_channel` 无条件放行，于是读的是
   **调用方给的 stdin**。`go test` 恰好把 stdin 接到 `/dev/null`，`read` 立刻 EOF 回落默认值
   `password`，测试「通过」——这是环境巧合，不是脚本性质。

因此缺陷有两种触发形态，同一个根因：

- 有控制终端：卡在 `/dev/tty`。实测 `go test ./deploy/...` 挂满 10m，panic 只报
  `running tests: TestShellFunctionSuite (10m0s)`，栈顶是 `syscall.Wait4`。
- 无控制终端但 stdin 打开着没数据：卡在 stdin。**不依赖 PTY 的稳定复现**：
  `sleep 300 | bash testdata/run_tests.sh`，20s 后被 alarm 杀掉，日志停在
  `==> 认证方式（socks5 只支持 none|password） (password none) [password]:`，共 131 行、
  最后一条断言是 `listen/remote-refused`。

附带缺陷：`assert_contains "tty/value" "/dev/tty " "${TM_TTY} "` 的 needle 是 `"${TM_TTY} "`，
`TM_TTY` 为空时 needle 退化成单个空格，`/dev/tty ` 当然包含它；`TM_TTY=tty` 之类的前缀也照样
通过。这条断言对任意取值都成立，等于没有断言。

Go 侧：`TestShellFunctionSuite` 用 `exec.Command(...).CombinedOutput()`，没有任何期限，
唯一的兜底是包级超时。

## 4. 架构决策

1. **修复点放在测试驱动脚本，不放在共享库。** 交互提示对真人安装是特性；把它改成非阻塞会削弱
   产品行为。测试自己声明「我不是交互环境」才是正确的责任划分。
2. **两道防线，各自独立成立。** shell 侧 `exec </dev/null` + `TM_TTY=""` 让阻塞在源头不可能发生；
   Go 侧硬上限让「将来有人重新武装交互通道」变成分钟级的、可定位的失败而不是 10m 挂死。
   只做 shell 侧则回归无告警，只做 Go 侧则开发者手工跑脚本仍然挂死。
3. **红灯测试不依赖 PTY。** 用 `os.Pipe()` 造一条永不写入也永不关闭的 stdin，精确复现「通道打开
   但没有数据」，因此本机和 CI 上判定一致；不需要 pty 依赖，也不需要 `setsid`（`setsid` 在进程
   已是进程组组长时会 EPERM，会造成偶发失败）。
4. **三个 bash 生成点统一走一个 helper。** `TestShellFunctionSuite`、`runRoleInstaller`、
   `renderYAML` 都是「跑一个 source 了共享库的 bash 脚本」，按 DRY 抽 `runBash`，
   期限与 stdin 只在一处定义。
5. **`cmd.WaitDelay` 是必需的，不是可选优化。** stdout/stderr 是 `bytes.Buffer` 而非 `*os.File`，
   `os/exec` 会自建管道并起转发 goroutine，`Wait` 要等写端全部关闭才返回；脚本里 `$(...)` 命令
   替换留下的子 shell 继承了写端，只杀直接子进程会让 `Wait` 永久阻塞——那正好重现了本次要修的
   挂死。实测：不加 `WaitDelay` 时红灯跑到 120s 仍未返回，`ps` 里留着两个 PPID=1 的
   `run_tests.sh`；加上 `WaitDelay = 5s` 后在 65.00s 干净失败。

## 5. 文件清单

| 文件 | 改动 |
| --- | --- |
| `deploy/install/oneclick/testdata/run_tests.sh` | 开头 `exec </dev/null`；`tty/value` 改成精确的二值 `case` 判定；`tm_tty_init` 断言后立刻 `TM_TTY=""`；新增 `ask/hermetic-channel-falls-back` 正面证据；收尾新增 `tty/hermetic-at-exit` 与 `tty/stdin-is-not-a-terminal` 复核 |
| `deploy/install/oneclick/oneclick_scripts_test.go` | 新增 `bashTimeout`/`hermeticSuiteTimeout`/`bashWaitDelay` 与 `runBash` helper；`TestShellFunctionSuite` 改走 helper；新增 `TestShellFunctionSuiteNeverReadsTheCallersStdin`；imports 增加 `bytes`、`context`、`io`、`time` |
| `deploy/install/oneclick/oneclick_e2e_test.go` | `runRoleInstaller` 改走 `runBash`；移除不再使用的 `bytes` import |
| `deploy/install/oneclick/oneclick_config_test.go` | `renderYAML` 改走 `runBash`，stderr 一并进失败信息；`os.WriteFile` 收 `[]byte(out)` |
| `docs/development/testing.md` | 部署产物校验表补齐 oneclick 三个测试文件；新增「测试驱动的 shell 脚本必须是非交互的」小节 |

不改：`tunnelmesh-install-common.sh`、`install-*.sh`、`install-*.ps1`、`testdata/render_yaml.sh`、
`testdata/bin/*`、任何 `internal/` 产品代码、`migrations/`、`web/`。

## 6. TDD 步骤

1. **红灯**：先只加 `runBash` helper 与 `TestShellFunctionSuiteNeverReadsTheCallersStdin`，
   不动 `run_tests.sh`。
   预期失败：脚本读调用方的静默管道，60s 上限到点后 `runBash` 以诊断信息 `t.Fatalf`。
   实测：`--- FAIL: TestShellFunctionSuiteNeverReadsTheCallersStdin (65.00s)`，
   信息为 `testdata/run_tests.sh timed out after 1m0s: 脚本极可能停在 tm_ask*/tm_read_line 的交互提示上。...`，
   stdout 末尾停在 `==> downloading tunnelmesh-v9.9.9-darwin-arm64.tar.gz` 之后的缓存命中行。
   （第一次红灯尝试还额外暴露了 `Wait` 阻塞问题：未设 `WaitDelay` 时 120s 仍不返回，
   于是把 `cmd.WaitDelay` 纳入 helper 后重跑，才得到上面这条干净的 65.00s 失败。）
2. **最小实现**：给 `run_tests.sh` 加 `exec </dev/null` 与 `TM_TTY=""`。
   预期通过：新测试与原有 `TestShellFunctionSuite` 都在 1s 内跑完。
   实测：脱离控制终端 `TestShellFunctionSuite 0.65s`、
   `TestShellFunctionSuiteNeverReadsTheCallersStdin 0.62s`；
   **在 PTY 下**（原挂死环境）分别 `0.63s` / `0.63s`。
3. **补强**：加 `tty/value` 精确判定、`ask/hermetic-channel-falls-back` 正面证据、
   收尾两条复核断言；把 `runRoleInstaller` 与 `renderYAML` 也切到 `runBash`。
   预期：套件断言数增加且全绿，`TM_ONECLICK_E2E=1` 的门控端到端测试仍然通过。
4. **文档**：`docs/development/testing.md` 记录不变量与两道防线，避免下一个测试驱动脚本重犯。

## 7. 验证命令与结果（补记时重新执行）

| 命令 | 结果 |
| --- | --- |
| `sleep 300 \| bash testdata/run_tests.sh`（修复前，20s alarm） | 挂死，日志 131 行，停在 `认证方式` 提示 |
| 同上（修复后，60s alarm） | 跑完，`0 failure(s)`，新增 4 条断言全 ok |
| `go test ./deploy/install/oneclick/ -run TestShellFunctionSuite -count=1 -v`（PTY 下） | 两个用例 `0.63s` PASS |
| `go test ./deploy/install/oneclick/ -count=1 -v -run TestShell`（脱离控制终端） | 5 个 `TestShell*` 全 PASS，包 1.756s |
| `TM_ONECLICK_E2E=1 go test ./deploy/... -count=1`（PTY 下） | 4 个包全 ok，`oneclick` 8.063s |
| `go test ./... -count=1` | 全绿 |
| `go test -tags vpn ./... -count=1` | 全绿 |
| `go vet ./...` / `gofmt -l internal scripts cmd deploy` / `git diff --check` | 无输出 |

## 8. 发布与回滚注意事项

- 无发布影响：改动只在测试文件、`testdata/` 与开发文档，`testdata/run_tests.sh` 不被任何发布
  脚本引用（`rg run_tests.sh` 在 `*.sh`/`*.ps1` 里零命中），发布归档内容不变。
- 无 Schema、无配置项、无 API、无协议、无前端产物变化。
- 回滚：`git revert` 本次提交即可，没有需要补偿的状态。
- 残留风险：`exec.CommandContext` 只杀直接子进程，超时路径下被孤立的子 shell 要等
  `WaitDelay` 关闭管道后收到 SIGPIPE 才退出。该路径只在失败时走到，且 5s 内收敛。
