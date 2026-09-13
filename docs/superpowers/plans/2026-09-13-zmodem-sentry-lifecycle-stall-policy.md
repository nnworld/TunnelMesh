# ZMODEM Sentry 生命周期与停滞豁免实施计划

状态：补记计划（原始实现已完成）

补记说明：本计划对应一次线上缺陷止损。用户报告“没传输完，又被关闭了”（153 MiB `sz` 传输中途结束）与“传输完后终端卡住、上方出现弹窗，敲回车弹窗报错”（同一条错误横幅堆叠三次，终端不再回显）。按 AGENTS.md 的紧急修复条款先修复再补记；红灯证据为实现前的真实观测，门禁结果在补记时重新执行，见第 7 节。

## 1. 目标

1. 协议解析失败后桥接必须换掉 Sentry 实例，使终端立刻恢复普通回显，且同一条错误不再重复上报。
2. `sz` 收尾阶段缺少 `OO` 触发的协议错误按“传输已完成”处理，只发 `ended`，不再弹错误横幅。
3. ZMODEM 协议写不再继承按键写的 5 秒停滞期限；同时通道 `close()` 不得排在无期限写之后，避免拆除期限后引入新的悬挂。

## 2. 非目标

- 不修改 Server/Agent/relay 的 Go 代码与流控常量。
- 不实现 ZMODEM 断点续传或 ZRPOS 重传：zmodem.js 不支持，超出本轮范围。
- 不修改 vendored `zmodem.js`：改上游库会让后续升级不可复现，Sentry 重建在桥接侧完成即可。
- 不放宽 SFTP 写的停滞期限：本轮无 SFTP 停滞报告，且 SFTP 有独立的请求/响应节奏。

## 3. 根因与证据

三个缺陷，两条用户可见症状。

### 缺陷一：Sentry 持有已死会话（终端卡死 + 弹窗堆叠）

`web/node_modules/zmodem.js/src/zsentry.js` 的 `consume()` 只要 `this._zsession` 非空就把全部输入交给它，而 `_zsession` 只在 `_after_session_end()` 中清空，后者绑定在会话自身的 `session_end` 事件上。`zsession.js` 的 `_consume_first()` 在 `_got_ZFIN` 之后收到非 `OO` 字节时是直接 `throw`（抛出的是字符串），既不触发 `session_end`，库也没有公开的复位入口。

因此桥接 catch 里把 `session` 置空之后，Sentry 仍持有那个死会话：此后每一个字节都被再次投喂、再次抛出。用户敲一次回车就多一条一模一样的横幅（截图里堆了三条），而字节全部进了死会话，终端彻底不回显。

### 缺陷二：`_got_ZFIN` 之后的错误只是收尾噪声（把已完成的传输报成失败）

ZMODEM 的 `ZFIN` 只在 `ZEOF` 之后交换，`OO` 又只在 `ZFIN` 之后打印。能走到 `PROTOCOL: Only thing after ZFIN should be “OO”` 这个 throw，说明文件字节早已全部 spool 完、`completed` 事件已发、浏览器已把文件存到本地。把它作为 `error` 上报等于告诉运维“一个已经下完的文件坏了”，并诱导重传一个 153 MiB 的文件。

### 缺陷三：停滞保护杀掉大文件传输（传输中途结束）

153 MiB 的 `sz` 会让浏览器回送的 ZACK 在出站方向堆积数兆字节；lrzsz 在等待重传确认期间暂停读取，通道窗口收紧后协议写出现零进度。第十一轮引入的 `writeStallLimit = 5000` 于是抛 `SSH shell write stalled after N of M bytes`，第十四轮的 `toPeer` catch 判定传输仍活跃 → `abortLocal()` → 面板消失并弹出失败提示。用户看到的就是“没传输完，又被关闭了”。

### 缺陷三的二阶风险：拆除期限会让 `close()` 悬挂

`close()` 原先走 `shellGate(() => closeShell())`。一旦协议写变成无期限等待，一个停滞的写者就会永久占住 gate，`closeTerminal()` 里的 `await channel?.close()` 永不返回，libssh2 句柄泄漏、页面无法离开。拆除期限与拆除排队必须在同一次变更中完成。

证据：

- 红灯测试 `waits for window credit without a deadline when the stall limit is disabled`：修复前 60 秒零进度后 `failure` 为 `SSH shell write stalled after 0 of 3 bytes`。
- 红灯测试 `returns to plain terminal passthrough after the parser session dies`：修复前二次投喂的字节既不进终端也不再产生新事件之外的任何输出。
- 既有测试 `returns the parser leftover to the terminal when a receive session dies after ZFIN` 断言 `error.message` 含 `Only thing after ZFIN`，与缺陷二的期望行为直接冲突，本轮按新语义重写。

## 4. 架构决策

1. **Sentry 一次性化**：抽出 `makeSentry()` 工厂，`let sentry`；catch、`abort()`、`abortLocal()` 三处失败/放弃路径都重建实例。不碰 vendored 库，语义收敛在桥接内部。
2. **收尾错误降级为 `ended`**：判据是 `role === 'receive' && session._got_ZFIN`，与既有 leftover 回放的判据同源（读的是同一份状态），不引入第二套判定。`stripNextOverAndOut` 照常武装，晚到的 `OO` 仍会被吃掉；`ended` 在 leftover 回放之后发出，保证面板消失时提示符已在屏上。
3. **停滞期限变成写调用的参数而非通道常量**：`write(data, { stallLimitMs })`，`0` 表示无期限，默认值仍是 5 秒。按键路径不变——按键写 5 秒不出字节就说明通道死了，视图依赖这个 rejection 关闭终端。只有 `toPeer`（定义上仅在传输活跃时被调用）传 `0`。
4. **拆除 `close()` 的排队语义并消除 use-after-free**：`close()` 直调 `closeShell()`，`shellClosed` 同步置位；在途写者在下一个检查点自行退场。为免 `closeShell()` 释放通道后停滞写者再次触碰句柄，`retryEAGAIN` 增加 `isCancelled` 谓词，取消时返回 `0`（写循环已理解的“无进度”值）而不再调用 operation。

## 5. 文件清单

- `web/src/webssh/zmodem.ts`：`makeSentry()` 工厂与 `let sentry`；catch 重写（`finishedBeforeThrow` 判定、状态复位、Sentry 重建、`ended` 补发）；`abort()` 与 `abortLocal()` 重建 Sentry。
- `web/src/webssh/ssh-client.ts`：`TerminalChannel.write` 增加 `options?: { stallLimitMs?: number }`；`retryEAGAIN` 增加可选 `isCancelled`；`writeShell(data, stallLimitMs)`；`close()` 直调 `closeShell()`。
- `web/src/views/WebSSHTerminal.vue`：`toPeer` 的写调用传 `{ stallLimitMs: 0 }`。
- `web/src/webssh/zmodem.spec.ts`：新增模块级 helper `receiveUntilMissingOverAndOut`，新增 3 个用例并按新语义重写原 leftover 用例。
- `web/src/webssh/ssh-client.spec.ts`：新增 2 个用例（无期限写 + 默认期限回归护栏）。
- `web/src/tests/zmodem-terminal.spec.ts`：新增源码断言 `writes protocol bytes without a stall deadline`。
- `web/dist/`、`internal/server/web_dist/`：重新构建并同步的嵌入产物。
- 文档：本文件、`docs/operations/troubleshooting.md` ZMODEM 小节增补、`docs/pull-requests/2026-09-12-admin-webssh-sftp.md` 第十五轮小节与 Reviewer 关注点。

## 6. TDD 步骤

红灯（实现前实测 `npx vitest run` 三个 spec，结果 4 failed / 56 passed）：

1. `reports a finished receive as ended when the peer skips the OO trailer` → `timed out waiting for ended; got ["detected","receive-offer","progress","completed","error"]`。
2. `returns to plain terminal passthrough after the parser session dies` → 同一超时（错误路径未换 Sentry）。
3. `waits for window credit without a deadline when the stall limit is disabled` → `expected Error: SSH shell write stalled after 0 of 3 bytes to be null`。
4. `writes protocol bytes without a stall deadline` → 视图源码中找不到 `stallLimitMs: 0`。

两个用例实现前即为绿，作为回归护栏保留：

- `parses shell output normally after abortLocal`：被放弃的 Receive 会话恰好把非法字节当 garbage 转回终端，旧实现也不报错。保留它是为了锁住“重建 Sentry 不得让 abortLocal 路径退化”，因为该路径同样不再向对端发任何东西、`session_end` 永不触发。
- `still reports a stalled shell write once the default deadline passes`：证明无期限是显式 opt-in，默认 5 秒语义未变（4 秒不报、6 秒报 `stalled`）。

最小实现：第 4 节四条，未做超出目标的重构。

绿灯：三个定向 spec 60/60；全量 30 文件 / 234 用例；E2E 15/15。

## 7. 验证命令与结果（补记时重新执行）

```bash
cd web && npx vitest run src/webssh/zmodem.spec.ts src/webssh/ssh-client.spec.ts src/tests/zmodem-terminal.spec.ts
cd web && npm test -- --run && npm run build
rsync -a --delete web/dist/ internal/server/web_dist/ && ./scripts/verify-web-embed.sh
TM_E2E_DIR=/tmp/tm-e2e-r15 node test/e2e/webssh/run.mjs
go build ./... && go vet ./... && gofmt -l internal/ cmd/ test/ && git diff --check
go test ./... -count=1
```

结果：

- 定向 spec 60/60 通过（本轮新增 5 个用例、重写 1 个）。
- 前端全量 30 文件 / 234 用例通过（上一轮 229，本轮 +5）。
- `npm run build` 成功；`verify-web-embed.sh` 输出 `web/dist and internal/server/web_dist match`，且嵌入产物中可检索到 `stallLimitMs`。
- E2E 15/15 PASS（工作目录 `/tmp/tm-e2e-r15`）：4 MiB `sz` 与 1.5 MiB `rz` 逐字节校验、`transfer panel closes and the prompt returns`、SFTP 列表与 3 MiB 上传逐字节校验、浏览器控制台洁净。
- `npx vue-tsc --noEmit` 项目级错误数维持基线 194，本轮改动文件未引入新错误。
- Go 门禁：`go build ./...`、`go vet ./...`、`gofmt -l internal/ cmd/ test/`、`git diff --check` 均无输出；`go test ./... -count=1` FAIL 计数 0，18 个包 ok、5 个包无测试文件（合计 23 个包）。本轮未改任何 Go 代码。

## 8. 发布与回滚注意事项

- 修复全部在前端 bundle：发布需 `npm run build` + `rsync` 到 `internal/server/web_dist/` 后重建 Server 二进制，只替换二进制而不重建嵌入产物等于没有发布。
- 回滚即回退 bundle。回滚后三项旧行为恢复：收尾缺 `OO` 弹错误、错误后终端不回显且横幅随按键堆叠、大文件传输在 lrzsz 暂停约 5 秒后被本地中止。
- 行为变化需知会使用者：`sz` 传输完成而对端没发 `OO` 时，面板直接收起、终端回到提示符，不再弹任何错误；文件此前已经下载完成。
- 无期限写只作用于 ZMODEM 协议方向。若隧道真的断了，libssh2 会返回负值错误码，经 `assertResult` 抛出并走 `abortLocal()`，不会静默悬挂；通道存活的最终裁决权仍在读循环。
- 已知残留：lrzsz 自身重传超时（约十秒级）内进度面板可能短暂无更新，属对端行为；接收方向仍在浏览器内存中缓冲整个文件后才触发下载，超大文件建议走 SFTP 页面。
