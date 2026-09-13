# WebSSH 死通道判定窗口与传输尾部关会话止损实施计划

状态：补记计划（原始实现已完成）

补记说明：本计划对应一次线上缺陷止损。用户报告“sz 下载中偶现 ssh 关闭”，页面进入“已断开 / SSH 通道已关闭”空态，并明确要求“检测通道时间太短，30s 以上才认为通道关闭”。按 AGENTS.md 的紧急修复条款先修复再补记；红灯证据为实现前的真实观测，门禁结果在补记时重新执行，见第 7 节。

## 1. 目标

1. 把浏览器侧“通道已死”的判定窗口从 5 秒零进度提升到 30 秒零进度，shell 写与 SFTP 写共用同一常量。
2. 移除“传输已结束但协议写仍在途”的迟到拒绝关闭整条会话的路径；通道存活性归读循环与按键路径裁决。

## 2. 非目标

- 不修改 Server/Agent/relay 的 Go 代码：全链路盘点后，Server 侧唯一的时间型判定是 `server.webssh.idle_timeout`，默认 5 分钟（见 `internal/config/config.go` 默认表），已远大于 30 秒，无需调整。
- 不给第十五轮引入的无期限协议写加封顶：lrzsz 自身重传超时远短于任何合理封顶值，加封顶只会重新引入“传输中途被判死”。
- 不实现 WebSocket 自动重连：ticket 一次性消费，重连等价于新会话，超出本轮范围。
- 不新增配置项：判定窗口与流控额度同属内置策略，暴露参数会造成线上各节点行为漂移。

## 3. 根因与证据

截图空态的渲染条件是 `status === 'closed' && !remoteEnded`，且没有红色传输告警、没有“远端会话已结束”块，因此只能由 `closeTerminal()`（非 keepTerminal 分支）产生。传输期间按键被丢弃、`toPeer` 的拒绝走 `abortLocal` 分支，于是只剩两条路径：

1. **5 秒停滞保护把拥塞当死亡。** `writeStallLimit = 5000` 是浏览器侧唯一的亚 30 秒时间型判定（shell 写与 SFTP 写共用）。大文件 `sz` 的尾部会让出站窗口拥塞数秒以上：传输结束瞬间用户敲键，按键写排在仍在等窗口额度的协议写之后，轮到它时窗口仍满，5 秒零进度即抛 `SSH shell write stalled after N of M bytes`，按键路径的 catch 直接 `closeTerminal()`，页面进入空态。SFTP 上传共用同一常量，同样偏激。
2. **在途协议写的迟到拒绝关会话。** 第十五轮让 `toPeer` 的写以 `stallLimitMs: 0` 无期限等待窗口额度；传输因 `ended`/`aborted` 结束后，这条写可能仍在 gate 中。它随后若拿到 libssh2 负值错误而拒绝，`toPeer` catch 的“非活跃”分支会 `closeTerminal()`——一次与 shell 健康无关的迟到拒绝，杀掉了活着的会话。

证据：

- 时间型判定盘点：浏览器侧仅 `writeStallLimit = 5000`（shell 与 SFTP 共用）；Server 侧仅 `server.webssh.idle_timeout`，默认 5m；其余保护（64 MiB 接收队列、64 MiB WebSocket 背压）是容量型而非时间型。唯一亚 30 秒者即 5 秒停滞保护，与用户“检测通道时间太短”的判断一致。
- 红灯测试 `only reports a stalled shell write after thirty seconds of zero progress`：修复前 20 秒处 `failure` 已为 `SSH shell write stalled after 0 of 3 bytes`。
- 红灯测试 `fails an SFTP write only after thirty seconds of zero progress`：修复前 20 秒处已抛 `SFTP write stalled after 0 of 64 bytes`。
- 红灯源码断言 `fails the transfer instead of the channel when a protocol write fails`：修复前 `toPeer` 片段包含 `closeTerminal()`。

## 4. 架构决策

1. **判定窗口即策略常量**：`writeStallLimit = 30_000`，注释写明它是“通道已死”的判定而非延迟预算。真正死掉的隧道仍会由 WebSocket close 与读循环更早 surfaced，30 秒只是兜底上限。
2. **存活性裁决权收敛**：`toPeer` 的 catch 只负责“传输活跃则中止传输并提示”，非活跃分支不再关会话，理由与第十四轮一致——通道存活性归读循环。按键路径保留“零进度达到判定窗口即关终端”，因为按键写不出字节且读循环也无数据时，会话确实已死。
3. **不引入新参数**：窗口是内置策略，与流控额度一样不提供配置面，避免集群内节点行为不一致。

## 5. 文件清单

- `web/src/webssh/ssh-client.ts`：`writeStallLimit` 由 5000 改为 30_000，并补策略注释。
- `web/src/views/WebSSHTerminal.vue`：`toPeer` catch 的非活跃分支移除 `closeTerminal()`，改为说明性注释。
- `web/src/webssh/ssh-client.spec.ts`：两个停滞用例重写为“20 秒不报、35/36 秒报”。
- `web/src/tests/zmodem-terminal.spec.ts`：源码断言改为 `toPeer` 片段不得包含 `closeTerminal()`。
- `web/dist/`、`internal/server/web_dist/`：重新构建并同步的嵌入产物。
- 文档：本文件、`docs/operations/troubleshooting.md` 增补一节、`docs/pull-requests/2026-09-12-admin-webssh-sftp.md` 第十六轮小节与 Reviewer 关注点。

## 6. TDD 步骤

红灯（实现前 `npx vitest run` 两个 spec，结果 3 failed / 44 passed）：

1. `only reports a stalled shell write after thirty seconds of zero progress` → 20 秒处 `expected Error: SSH shell write stalled after 0 of 3 bytes to be null`。
2. `fails an SFTP write only after thirty seconds of zero progress` → 20 秒处 `expected SFTPError: SFTP write stalled after 0 of 64 bytes to be null`。
3. `fails the transfer instead of the channel when a protocol write fails` → `expected 'toPeer: (bytes) => {…' not to contain 'closeTerminal()'`。

最小实现：第 4 节两条，未做超出目标的重构。

绿灯：定向 spec 60/60；全量 30 文件 / 234 用例；E2E 15/15。

## 7. 验证命令与结果（补记时重新执行）

```bash
cd web && npx vitest run src/webssh/ssh-client.spec.ts src/webssh/zmodem.spec.ts src/tests/zmodem-terminal.spec.ts
cd web && npm test -- --run && npm run build
rsync -a --delete web/dist/ internal/server/web_dist/ && ./scripts/verify-web-embed.sh
TM_E2E_DIR=/tmp/tm-e2e-r16 node test/e2e/webssh/run.mjs
go build ./... && go vet ./... && gofmt -l internal/ cmd/ test/ && git diff --check
go test ./... -count=1
```

结果：

- 定向 spec 60/60 通过；前端全量 30 文件 / 234 用例通过（用例数与上一轮持平：重写 3 个、未新增）。
- `npm run build` 成功；`verify-web-embed.sh` 输出 `web/dist and internal/server/web_dist match`。
- E2E 15/15 PASS（工作目录 `/tmp/tm-e2e-r16`）：4 MiB `sz`、1.5 MiB `rz`、3 MiB SFTP 均逐字节校验，浏览器控制台洁净。
- Go 门禁：`go build ./...`、`go vet ./...`、`gofmt -l internal/ cmd/ test/`、`git diff --check` 均无输出；`go test ./... -count=1` FAIL 计数 0，18 个包 ok、5 个包无测试文件。本轮未改任何 Go 代码。

## 8. 发布与回滚注意事项

- 修复全部在前端 bundle：发布需 `npm run build` + `rsync` 到 `internal/server/web_dist/` 后重建 Server 二进制。
- 回滚即回退 bundle；回滚后恢复 5 秒判定与“迟到拒绝关会话”两条旧行为。
- 行为变化需知会使用者：真正死掉的隧道在最坏情况下要等 30 秒零进度才由写路径 surfaced；通常 WebSocket 关闭或读循环 EOF 会更早给出结束态，30 秒只是兜底。
- Server 侧无需改配置；若曾为排查问题把 `server.webssh.idle_timeout` 调小，请恢复默认（5m）或保证不小于传输最久暂停时间。
- 已知残留：第十五轮的无期限协议写在传输结束后仍可能短暂占住写闸门，排在其后的按键写需等它退场；断开连接不受影响（`close()` 不排队）。若后续观测到“传输结束后敲键无回显但页面仍显示连接”，再为无期限写引入可取消语义，不在本轮扩大范围。
