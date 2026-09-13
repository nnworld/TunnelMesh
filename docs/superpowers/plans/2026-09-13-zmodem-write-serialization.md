# ZMODEM 出站写入串行化修复实施计划

状态：补记计划（原始实现已完成）

补记说明：本计划对应一次线上缺陷止损。用户在真实网络（经 Nginx WSS 反代）上报告 `sz` 下载后敲回车弹 `PROTOCOL: Only thing after ZFIN should be "OO"`、`rz` 上传时终端乱码并弹 `Unhandled header: ZRPOS` / `Peer aborted session`。按 AGENTS.md 的紧急修复条款先修复再补记；红灯证据为实现过程中的真实观察，绿灯与门禁结果在补记时重新执行，见第 7 节。

## 1. 目标

1. 浏览器到远端的出站字节在任意通道窗口压力下都保持严格调用顺序，ZMODEM 帧不再被其它写者插队。
2. 传输异常结束时终端不得吞掉 shell 提示符（用户看到的现象是“终端卡死在空行”）。
3. 用单元测试固化“部分写不得交错”的不变式，因为该缺陷在 localhost E2E 中不可复现。

## 2. 非目标

- 不修改 Server/Agent/relay 的任何 Go 代码：本轮缺陷完全在浏览器侧出站路径。
- 不改变流控窗口常量、frame 类型或 ZMODEM 协议栈位置（仍在浏览器）。
- 不消除 lrzsz 自身在中止时产生的 pty 回显乱码：那是远端 shell 回显了未被消费的在途字节，属于 lrzsz 的固有行为，只能在“不再异常中止”的前提下消失。
- 不重写 zmodem.js：仅在其抛错后做残留字节的回放。

## 3. 根因与证据

### 3.1 并发出站写入在部分写发生时交错（生产根因）

终端视图把 ZMODEM 的同步 `sender` 回调接成 fire-and-forget：`toPeer: (bytes) => { void channel.write(bytes).catch(...) }`。zmodem.js 在一次 `consume()` 里会同步调用多次 `sender`（ZACK、ZFIN、数据子包），因此同一 SSH 通道上长期存在多个并发的 `write()` Promise。

第十一轮为修复短写截断，把 `write` 改成了"部分写续写循环"：一个 chunk 可能被拆成多次 `_ssh2_channel_write` 调用，调用之间有 `await`。于是并发写者的续写会互相插队——写者 A 的前半段、写者 B 的前半段、A 的后半段……在 wire 上形成坏帧。

证据：

- 红灯单元测试 `never interleaves concurrent shell writes when libssh2 consumes partially`：让 fake libssh2 每次只消费一半，两个并发 `write` 的线上字节序为 `[1,1,2,2,1,2,1,2]`，而不是 `[1,1,1,1,2,2,2,2]`。这正是远端看到的坏帧签名。
- 生产症状与该机制一一对应：`rz` 收到坏帧后 CRC 校验失败，回 `ZRPOS` 请求重传，浏览器侧 zmodem.js 不实现 `ZRPOS` → `Unhandled header: ZRPOS` → `Peer aborted session`；`sz` 方向则是浏览器的 ZFIN 回应被插队破坏，lrzsz 等不到合法收尾直接退出、不发 `OO`，浏览器接收会话在 post-ZFIN 状态收到 shell 提示符字节 → `Only thing after ZFIN should be "OO" (79,79), not: 27,93,...`（27,93 即 ESC ]，OSC 标题序列）。
- localhost E2E 全绿的原因：通道窗口从不紧张，libssh2 总是整块消费，续写循环只跑一轮，交错窗口不存在。该缺陷只在真实 RTT/带宽受限链路出现，因此回归防线必须放在单元测试而不是 E2E。

### 3.2 协议错误时终端吞掉提示符（次生缺陷）

`consumeFromRemote` 的 catch 在 zmodem.js 抛错后直接清空会话。抛错时解析器 `_input_buffer` 里还有未消费字节（post-ZFIN 场景下就是 shell 提示符），这些字节随会话一起被丢弃，终端停在空行，直到下一次按键才有输出，用户感知为"卡死"。

证据：红灯测试 `returns the parser leftover to the terminal when a receive session dies after ZFIN` 中，修复前 `h.terminal` 为 `[]`，提示符字节丢失。

## 4. 架构决策

1. **每通道单写者闸门**（`createWriteGate`）：一个 Promise 链保证同一 SSH 通道上任意时刻只有一个任务在调用 libssh2 的写路径；任务失败不污染队列（`tail` 只接 resolved 链）。闸门是异步的，不阻塞事件循环，fire-and-forget 调用方自动获得严格顺序。
2. **闸门粒度 = 逻辑操作**：多调用操作（SFTP handle 写循环、`readDirectory` 的 opendir/readdir/close 序列）必须整体持闸，否则部分写的续写仍会被同通道的其它请求插队；单请求操作（unlink/rename/realpath/open）按操作持闸即可。读操作也持闸，因为 libssh2 的读会顺带 flush 发送队列，且持闸可以避免"读触发 flush 与写续写并发"的边界争论。
3. **close 排队而非抢占**：shell `close` 与 SFTP `close` 走同一闸门，保证不会在某个部分写续写之前拆通道；写循环在续写前检查 `shellClosed`，通道已关时静默丢弃剩余字节而不是写坏 close 握手。
4. **残留回放只限 receive 且 post-ZFIN**：只有接收会话在 ZFIN 之后的缓冲区里才是 shell 输出；发送会话的残留是对端协议字节，回放只会制造乱码。同时回放“抛错 chunk 之后从未喂入的尾部 chunk”，避免大输出被截断。

## 5. 文件清单

- `web/src/webssh/ssh-client.ts`：新增 `createWriteGate()`；shell 的 `write`/`resize`/`close` 抽出 `writeShell`/`resizeShell`/`closeShell` 并经 `shellGate` 串行化，写循环续写前检查 `shellClosed`；SFTP 新增 `sftpGate`，覆盖 open、handle read/write/close、`readDirectory`、`unlink`、`rmdir`、`rename`、`realpath`、`close`。
- `web/src/webssh/zmodem.ts`：`consumeFromRemote` 记录抛错 chunk 偏移；catch 中按第 4.4 条规则把 `_input_buffer` 与未喂入尾部回放给终端。
- `web/src/webssh/ssh-client.spec.ts`：新增 2 个串行化不变式测试（shell 并发部分写、SFTP 写与并发 unlink）。
- `web/src/webssh/zmodem.spec.ts`：新增 1 个残留回放测试。
- `web/dist/`、`internal/server/web_dist/`：重新构建并同步的嵌入产物（gitignore，发布前必须重新生成）。
- 文档：本文件、`docs/operations/troubleshooting.md` 新增「ZMODEM 在真实网络上报 ZRPOS / OO 错误」、`docs/user-guide/server-admin.md` 流控小节补充单写者说明、`docs/pull-requests/2026-09-12-admin-webssh-sftp.md` 第十二轮小节。

## 6. TDD 步骤

红灯（实现期间观察）：

1. `npx vitest run src/webssh/ssh-client.spec.ts`：`never interleaves concurrent shell writes when libssh2 consumes partially` 失败，`expected [ 1, 1, 2, 2, 1, 2, 1, 2 ] to deeply equal [ 1, 1, 1, 1, 2, 2, 2, 2 ]`；`never interleaves an SFTP write with a concurrent SFTP request` 失败，`expected 'write:1' to be 'unlink'`。
2. `npx vitest run src/webssh/zmodem.spec.ts`：`returns the parser leftover to the terminal when a receive session dies after ZFIN` 失败，`h.terminal` 为 `[]` 而期望为提示符字节。

最小实现：第 4 节的闸门与回放，未做超出目标的重构。

绿灯：两个 spec 46/46；全量前端 30 文件 / 225 用例；E2E 15/15。

## 7. 验证命令与结果（补记时重新执行）

```bash
cd web && npx vitest run src/webssh/ssh-client.spec.ts src/webssh/zmodem.spec.ts
cd web && npm test -- --run && npm run build
rsync -a --delete web/dist/ internal/server/web_dist/ && ./scripts/verify-web-embed.sh
TM_E2E_DIR=/tmp/tm-e2e-r12 node test/e2e/webssh/run.mjs
```

结果：两个 spec 46/46 通过；全量 30 文件 / 225 用例通过（本轮 +3）；构建成功且 `web/dist and internal/server/web_dist match`；E2E 15/15 PASS（含 4 MiB `sz` 下载、1.5 MiB `rz` 上传、3 MiB SFTP 上传逐字节校验，浏览器控制台洁净）。本轮未改任何 Go 代码，当天早些时候的 `go test ./... -count=1`（23 包 ok）与 `go test -race ./internal/server ./internal/agent -count=1 -timeout 25m`（427.1 s / 4.3 s）结果继续有效。

`npx vue-tsc --noEmit` 非门禁：全仓当前 194 个既有错误（node_modules 类型、Element Plus 槽位类型、未类型化的 wasm mock 等类别），本轮新增代码所在行零报错；实现过程中一度引入的 2 个 `transfer possibly undefined` 已在同轮修掉。

## 8. 发布与回滚注意事项

- 修复只存在于前端 bundle：发布时必须重新 `npm run build` + `rsync` 并重建 Server 二进制；Server/Agent 二进制逻辑不变，但因为 embed 产物变化，Server 仍需重新构建发布。
- 无 Schema、配置、API 变更；回滚即回退前端 bundle 与 Server 二进制，回滚后真实网络上的 ZMODEM 会恢复坏帧症状。
- 闸门引入的额外延迟仅为微任务级排队；最坏情况下 `close` 会等待一个停滞写满 5 秒（停滞保护上限）后才执行，属于可接受的 teardown 延迟。
- 已知残留：对端真正中止时 lrzsz 的 pty 回显乱码仍会出现（固有行为）；此时终端会保留提示符并给出错误横幅，不再“卡死”。
