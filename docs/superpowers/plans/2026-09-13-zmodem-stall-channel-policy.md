# ZMODEM 停滞处置策略与进度节流实施计划

状态：补记计划（原始实现已完成）

补记说明：本计划对应一次线上缺陷止损。用户报告“偶现 sz 卡住后，SSH 通道被关闭”（整页变成“SSH 通道已关闭”空态）。按 AGENTS.md 的紧急修复条款先修复再补记；红灯证据为实现过程中的真实观察，绿灯与门禁结果在补记时重新执行，见第 7 节。

## 1. 目标

1. 传输期间的出站写失败（含 5 秒零进度停滞保护）只终止传输，不再关闭整条 SSH 通道；通道存活性归读循环管。
2. 消除 progress 事件对主线程的洪泛：按字节步进（64 KiB）或安静间隔（500 ms）上报，而不是每个 ZDATA 子包一次。
3. 为“传输因传输层失败而本地结束”提供不向对端发 ZABORT 的桥接原语，避免向已坏的管道递归写入。

## 2. 非目标

- 不修改停滞保护本身（5 秒零进度仍判失败）：死隧道必须及时 surfaced。
- 不修改 Server/Agent/relay 的 Go 代码与流控常量。
- 不实现 ZMODEM 断点续传或 ZRPOS 重传：zmodem.js 不支持，超出本轮范围。

## 3. 根因与证据

“卡住”与“通道关闭”是同一条因果链的两段：

1. **卡住**：lrzsz `sz` 按 ZMODEM 流控发满一个窗口后等待 ZACK。浏览器侧 progress 事件原先每个 ZDATA 子包触发一次（17 MiB ≈ 上万次 Vue 响应式更新与 el-progress 重绘），主线程被占用时 ZACK 的写出被推迟，`sz` 暂停，进度停在任意百分比。
2. **关闭**：`sz` 暂停期间不再读 stdin，出站方向一旦窗口收紧，浏览器的协议写会零进度；第十一轮加入的 5 秒停滞保护抛错后，视图的 `toPeer` catch 无条件 `closeTerminal()`，于是整条 SSH 通道被关闭，用户看到“SSH 通道已关闭”空态。通道本身并没有死：读循环仍可能随后收到 `sz` 超时退出后的提示符。

证据：

- 红灯测试 `throttles progress events for bulk transfers`：200 KiB 载荷在修复前产生约 200 个 progress 事件（每子包一个）。
- 视图源码中 `toPeer: (bytes) => { void channel?.write(bytes).catch(() => { void closeTerminal() }) }` 对任何写失败一律关通道，与用户截图的空态一致。
- 停滞保护抛错路径（`SSH shell write stalled after N of M bytes`）经 `toPeer` 的 catch 进入 `closeTerminal`，代码路径唯一且确定。

## 4. 架构决策

1. **处置策略分层**：写失败时若 ZMODEM 会话仍活跃，则终止传输并提示错误、保留通道；否则维持原有的关通道行为。通道存活的最终裁决权在读循环（EOF/错误会走 `notifyShellExit`），写路径不再越权。
2. **`abortLocal()` 原语**：传输层已坏时 ZABORT 不可能送达，向坏管道再写一次只会递归进同一个失败处理；因此本地结束会话、置 `abortedByUser` 抑制 pending `accept()` 的二次报错、发 `aborted` 事件收面板，不碰 `toPeer`。
3. **进度节流双阈值**：字节步进保证大文件的事件数与大小成比例（17 MiB ≈ 272 次而非上万次）；安静间隔保证慢链路上面板仍有可见进展；首个子包无条件上报，保证小文件面板立即离开 0 B（既有测试依赖该行为）。

## 5. 文件清单

- `web/src/webssh/zmodem.ts`：`progressByteThreshold = 64 KiB`、`progressIntervalMs = 500`；offer `input` 回调按双阈值上报；接口与实现新增 `abortLocal()`。
- `web/src/views/WebSSHTerminal.vue`：`toPeer` 的 catch 改为“传输活跃则 `abortLocal()` + `ElMessage.error(webssh.zmodem.failed)`，否则 `closeTerminal()`”。
- `web/src/webssh/zmodem.spec.ts`：新增 `throttles progress events for bulk transfers`、`abortLocal ends the transfer without sending to the peer`。
- `web/src/tests/zmodem-terminal.spec.ts`：新增源码断言 `fails the transfer instead of the channel when a protocol write fails`。
- `web/dist/`、`internal/server/web_dist/`：重新构建并同步的嵌入产物。
- 文档：本文件、`docs/operations/troubleshooting.md` ZMODEM 小节增补两条、`docs/pull-requests/2026-09-12-admin-webssh-sftp.md` 第十四轮小节与 Reviewer 关注点。

## 6. TDD 步骤

红灯（实现期间观察）：

1. `throttles progress events for bulk transfers`：修复前 200 KiB 产生约 200 个 progress 事件，断言 `< 10` 失败。
2. `abortLocal ends the transfer without sending to the peer`：修复前 `abortLocal` 不存在，调用抛 TypeError。
3. 既有测试 `receives a file offered by a remote sz and reports progress` 在“纯字节阈值”的第一版实现下转红（34 B 载荷一个事件都没有），据此修正为“首子包无条件上报”，该测试回绿——这是一次由既有测试捕获的实现偏差。

最小实现：第 4 节三条，未做超出目标的重构。

绿灯：webssh 两个 spec 与视图 spec 72/72；全量 30 文件 / 229 用例；E2E 15/15。

## 7. 验证命令与结果（补记时重新执行）

```bash
cd web && npx vitest run src/webssh/ src/tests/zmodem-terminal.spec.ts
cd web && npm test -- --run && npm run build
rsync -a --delete web/dist/ internal/server/web_dist/ && ./scripts/verify-web-embed.sh
TM_E2E_DIR=/tmp/tm-e2e-r14 node test/e2e/webssh/run.mjs
```

结果：定向 spec 72/72；全量 30 文件 / 229 用例通过（本轮 +3）；构建成功且 `web/dist and internal/server/web_dist match`；E2E 15/15 PASS（4 MiB `sz`、1.5 MiB `rz`、3 MiB SFTP 逐字节校验，控制台洁净）。本轮未改任何 Go 代码，当天早些时候的 Go 门禁结果继续有效。

## 8. 发布与回滚注意事项

- 修复只在前端 bundle：发布需 `npm run build` + `rsync` 后重建 Server 二进制。
- 回滚即回退 bundle；回滚后“传输写失败连带关通道”与进度洪泛会恢复。
- 行为变化需要知会使用者：传输因传输层失败中止时，终端保留并弹出 `ZMODEM 传输失败：…`；若通道真的已死，读循环会随后给出正常的结束态，不会静默悬挂。
- 已知残留：lrzsz 自身超时（约十秒级）内终端仍可能短暂无新输出，属对端行为；进度面板在慢链路上最坏每 500 ms 或每 64 KiB 更新一次。
