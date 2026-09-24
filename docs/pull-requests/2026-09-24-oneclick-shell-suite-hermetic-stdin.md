# 一键安装函数级套件不再挂在交互提示上

## 标题

fix(deploy): stop the one-click shell suite from blocking on interactive input

## 目标分支

`main`（本次直接落在 `codex/vpn-phase6-server-data-plane`，随 VPN 阶段 6 一起集成）

## 关联记录

- 实施计划：[一键安装函数级套件交互挂死修复实施计划](../superpowers/plans/2026-09-24-oneclick-shell-suite-hermetic-stdin.md)（补记计划）
- 引入方：[三端一键安装脚本](2026-09-23-one-click-install-scripts.md)（v1.2.4，`main` 的 `90c5888`）
- 发现方：[VPN 网关 阶段 6：Server 数据面](2026-09-23-vpn-phase6-server-data-plane.md) 合并 `main` 后跑全量门禁时

## 摘要

`deploy/install/oneclick/testdata/run_tests.sh` 会在任何「输入通道打开着但没有人回答」的环境里
永久阻塞。合并 `main` 之后，`go test ./...` 在带 PTY 的终端里必然挂满 10 分钟包级超时，panic
只报 `running tests: TestShellFunctionSuite (10m0s)`，栈顶是 `syscall.Wait4`，指不出是哪个脚本。

根因是测试驱动脚本没有切断共享库的交互通道：

1. 脚本第 46 行为了测 `tm_tty_init` 真的调用了它，有控制终端时 `TM_TTY=/dev/tty`。
2. 后面 `tm_validate_listen 0.0.0.0:18080` 会走到 `tm_ask_choice auth_mode`，
   `tm_read_line` 于是 `read < /dev/tty`，永久阻塞。
3. 没有控制终端时 `TM_TTY=""`，读的是调用方 stdin；脚本又 `export TM_ONECLICK_ALLOW_STDIN=1`
   让 `tm_require_input_channel` 无条件放行。`go test` 恰好把 stdin 接到 `/dev/null`，
   `read` 立刻 EOF 回落默认值 `password`，测试「通过」——这是环境巧合，不是脚本性质。

顺带修掉一条假断言：`assert_contains "tty/value" "/dev/tty " "${TM_TTY} "` 的 needle 是
`"${TM_TTY} "`，`TM_TTY` 为空时退化成单个空格，对任意取值都成立。

## 用户影响

**零产品影响。** 没有改动 `tunnelmesh-install-common.sh`、三个 `install-<role>.sh` 或
`install-<role>.ps1`：`tm_tty_init` 探测 `/dev/tty`、`tm_read_line` 回落 stdin 对真人安装是
正确行为，缺陷在测试脚本没有声明「我不是交互环境」。发布归档内容不变。

受益方是贡献者与 CI：

- `go test ./...` 不再因为在终端里手跑而挂死 10 分钟。
- `bash deploy/install/oneclick/testdata/run_tests.sh` 在交互终端里可以直接跑完（修复前会卡在
  `认证方式` 提示上）。
- 将来谁在测试驱动脚本里引入一条交互提示，会在 60s 内得到点名脚本的失败信息。

## API、Schema 与配置影响

无。不改 API 路由、OpenAPI、`migrations/`、`SchemaVersion`、配置键、协议 frame、错误码与前端产物。

## 安全影响

无新增攻击面。`runBash` 只收紧测试执行：给每个 bash 脚本一个硬期限，并在超时时杀掉进程。
`TestNoHardcodedSecrets` 等既有安全断言未改动。

## 不变量

1. 测试驱动的 shell 脚本必须是非交互的：切断 stdin（`exec </dev/null`）、在调用 `tm_tty_init`
   之后立刻置空 `TM_TTY`、需要具体输入的断言用 `printf '...\n' | tm_ask_*_stdio` 在子 shell 里
   覆盖、走安装/渲染路径时预置答案或传 `--yes`。
2. 任何跑 bash 脚本的 Go 测试都必须有硬期限，且期限到点后 `Wait` 必须能返回
   （`cmd.WaitDelay`），否则「防止挂死」的机制自己会挂死。
3. `tm_tty_init` 的取值是二值的：`/dev/tty` 或空串，没有第三种。

## 改动

| 文件 | 改动 |
| --- | --- |
| `deploy/install/oneclick/testdata/run_tests.sh` | 开头 `exec </dev/null`；`tty/value` 改成精确的二值 `case` 判定；`tm_tty_init` 断言后立刻 `TM_TTY=""`；新增 `ask/hermetic-channel-falls-back`（证明通道已断、回落默认值而不是阻塞）；收尾新增 `tty/hermetic-at-exit` 与 `tty/stdin-is-not-a-terminal` 复核 |
| `deploy/install/oneclick/oneclick_scripts_test.go` | 新增 `bashTimeout`(3m) / `hermeticSuiteTimeout`(60s) / `bashWaitDelay`(5s) 与 `runBash` helper；`TestShellFunctionSuite` 改走 helper；新增 `TestShellFunctionSuiteNeverReadsTheCallersStdin`；imports 增加 `bytes`、`context`、`io`、`time` |
| `deploy/install/oneclick/oneclick_e2e_test.go` | `runRoleInstaller` 改走 `runBash`；移除不再使用的 `bytes` import |
| `deploy/install/oneclick/oneclick_config_test.go` | `renderYAML` 改走 `runBash`，stderr 一并进失败信息；`os.WriteFile` 收 `[]byte(out)` |
| `docs/development/testing.md` | 部署产物校验表补齐 oneclick 三个测试文件（此前该包完全没登记）；新增「测试驱动的 shell 脚本必须是非交互的」小节 |

## 测试证据

### 红灯（实测，非推断）

不依赖 PTY 的稳定复现——用一条「打开着但永不写入」的管道当 stdin：

| 步骤 | 命令 | 实测结果 |
| --- | --- | --- |
| 纯 shell 复现（修复前） | `sleep 300 \| bash testdata/run_tests.sh`，20s alarm | 被杀（exit 142），日志 131 行，末行是 `==> 认证方式（socks5 只支持 none|password） (password none) [password]:` |
| Go 红灯 | `go test . -run TestShellFunctionSuiteNeverReadsTheCallersStdin -count=1` | `--- FAIL: TestShellFunctionSuiteNeverReadsTheCallersStdin (65.00s)`，信息 `testdata/run_tests.sh timed out after 1m0s: 脚本极可能停在 tm_ask*/tm_read_line 的交互提示上。...` |
| 原始形态复现（修复前） | PTY 下 `go test ./deploy/... -count=1` | 挂满 10m，`panic: test timed out after 10m0s / running tests: TestShellFunctionSuite (10m0s)`，栈顶 `syscall.Wait4` |

红灯过程中还暴露了 helper 自身的缺陷：只用 `exec.CommandContext` 时 120s 仍不返回，`ps` 里留着
两个 PPID=1 的 `run_tests.sh`——stdout/stderr 是 `bytes.Buffer`，`os/exec` 自建管道并起转发
goroutine，`$(...)` 子 shell 继承写端，只杀直接子进程会让 `Wait` 永久阻塞。补上
`cmd.WaitDelay = 5s` 后才得到上面那条干净的 65.00s 失败。

### 绿灯（实测）

| 命令 | 结果 |
| --- | --- |
| `sleep 300 \| bash testdata/run_tests.sh`（修复后） | 跑完，`0 failure(s)`，`tty/value`、`ask/hermetic-channel-falls-back`、`tty/hermetic-at-exit`、`tty/stdin-is-not-a-terminal` 全 ok |
| **PTY 下**（原挂死环境）`go test ./deploy/install/oneclick/ -run TestShellFunctionSuite -count=1 -v` | `TestShellFunctionSuite 0.63s` PASS、`TestShellFunctionSuiteNeverReadsTheCallersStdin 0.63s` PASS |
| 脱离控制终端 `go test ./deploy/install/oneclick/ -run TestShell -count=1 -v` | 5 个 `TestShell*` 全 PASS，包 1.756s |
| PTY 下 `TM_ONECLICK_E2E=1 go test ./deploy/... -count=1` | 4 个包全 ok，`oneclick` 8.063s（门控端到端安装/升级/卸载未回归） |

### 门禁

| 命令 | 结果 |
| --- | --- |
| `go build ./...` / `go build -tags vpn ./...` | 通过 |
| `go test ./... -count=1` | 全绿 |
| `go test -tags vpn ./... -count=1` | 全绿 |
| `go vet ./...` / `go vet -tags vpn ./...` | 通过 |
| `gofmt -l internal scripts cmd deploy` | 无输出 |
| `git diff --check` | 无输出 |
| `bash -n deploy/install/oneclick/*.sh`、`bash -n testdata/run_tests.sh` | 通过 |
| `python3 scripts/gen_doc_index.py`（两跑） | 幂等 |

## 发布步骤

无。随分支正常集成即可，不需要迁移、不需要改配置、不需要重启已部署的服务。

## 回滚步骤

`git revert` 本次提交。没有数据库、配置或运行态需要补偿；回滚后唯一的后果是
`go test ./...` 在带控制终端的环境里重新挂死。

## Reviewer 关注点

1. **`exec </dev/null` 会不会掩盖真实的交互缺陷？** 不会：它只作用于测试驱动脚本，
   发布用的 `install-<role>.sh` 完全没改，真人安装仍然从 `/dev/tty` 问答。
2. **`ask/hermetic-channel-falls-back` 是否与 `ask/stdin-empty-uses-default` 重复？**
   不重复。后者在子 shell 里显式传 `TM_TTY=` 并用 `printf '\n'` 喂一行，测的是
   `tm_ask_choice_stdio` 的输入解析；前者不传任何覆盖、不给任何输入，测的是**套件全局**
   的交互通道确实已断——谁把 `TM_TTY` 重新武装起来或删掉 `exec </dev/null`，它就会卡住并被
   Go 侧 60s 上限点名。
3. **60s / 3m 两个期限是否合理？** 函数级套件实测 0.6–4s，60s 已是 15 倍以上余量；
   安装与渲染脚本要走 `tar`/`install`/服务 stub，给 3m。两者都远小于 10m 包级超时，
   因此失败信息永远来自 `runBash` 而不是 panic dump。
4. **为什么不用 `setsid` 让 Go 测试脱离控制终端？** `setsid()` 在调用进程已是进程组组长时返回
   EPERM，交互 shell 的作业控制会让这种情况偶发出现，等于用一个偶发失败换掉一个稳定失败。
   用静默管道复现既稳定又不挑环境。
5. `cmd.WaitDelay` 只关闭管道、不杀孙子进程；被孤立的子 shell 再写就收到 SIGPIPE 自行退出。
   该路径只在失败时走到，5s 内收敛。

## 集成状态

- 分支：`codex/vpn-phase6-server-data-plane`
- 变更规模：5 个文件（4 个测试/testdata + 1 份开发文档），无产品代码
- 时点记录：[三端一键安装脚本 PR 记录](2026-09-23-one-click-install-scripts.md) 与其计划、规格
  **只链接、未修改**；本次的事实更正只写在本文件与计划文件
- 活文档：`docs/development/testing.md` 补齐 `deploy/install/oneclick` 三个测试文件的登记
  （该包由 v1.2.4 引入时漏登记），并新增「测试驱动的 shell 脚本必须是非交互的」小节
- 索引：`scripts/gen_doc_index.py` 重新生成，两跑幂等
