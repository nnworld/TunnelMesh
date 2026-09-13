# WebSSH 大文件传输流控修复实施计划

状态：已完成（T1-T8 全部落地并验证通过）

执行结果：

- T1-T3：`internal/protocol/window.go` 统一窗口常量；Server 侧补齐发送窗口扣减与接收窗口回补；新增窗口阻塞/唤醒/关闭竞态测试。
- 计划外必修项：中继入站缓冲原先按**帧数**计（16 槽），而窗口按**字节**计。对端在窗口内发送大量小帧时仍会溢出并被判致命，实测在 16384 字节处失败。已改为按字节预算的 FIFO（预算等于通告窗口），回归测试 `TestAgentRelayStreamAcceptsManySmallFramesWithinWindow`。
- T4：集群 gRPC relay 路径经审计**无需改动**——阻塞式 `Write` 加 HTTP/2 流控已经形成背压，叠加第二层窗口反而有死锁风险。结论已写入 PR 文档。
- T5：浏览器写入改为等待排空（1 MiB 高水位、64 MiB 硬上限）并按 FIFO 串行化以防字节乱序；SSH 接收队列上限由 4 MiB 提到 64 MiB。
- T6：`ShellExitInfo` 增加 `transportError`，终端以红色告警呈现真实原因，不再伪造“远端退出码：0”；刷新后经 `sessionStorage` 中**仅非敏感的目标服务器 ID** 自动回到 `/remote-servers?ssh=<id>` 重连，主动断开时清除该记录。
- T7：E2E harness 从 `/tmp/tm-e2e` 迁入仓库 `test/e2e/webssh/`（零 npm 依赖、参数化、自动生成 fixture、含 README）；throwaway SSH 主机作为**嵌套 Go module**，保证 `creack/pty`、`pkg/sftp`、`x/crypto/ssh` 不进入根 `go.mod`。检查项由 12 增至 14（新增刷新恢复与浏览器控制台洁净门禁）。
- T8：文档已更新（`docs/user-guide/server-admin.md` 新增“大文件传输与通道流控”、`docs/operations/configuration.md`、`docs/operations/troubleshooting.md`、`docs/README.md` 新增“测试与验证”、PR 文档第十轮）。
- 偏差记录：默认窗口在 `AgentRelayTransport.OpenStream` 中对 `InitialWindow == 0` 统一补齐，而不是由 WebSSH broker 设置，使所有本地 relay 调用方共用一条路径。
- 验证：`go test ./... -count=1`、`go test -race ./...`、`go vet ./...`、`git diff --check`；前端 `npm test -- --run`（30 文件 / 219 用例）、`npm run build`、`scripts/verify-web-embed.sh`；仓库内 E2E `node test/e2e/webssh/run.mjs` 14/14 PASS（4 MiB `sz` 下载与 1.5 MiB `rz` 上传均逐字节校验通过，无控制台报错）。所有临时 `[diag]` 埋点已移除。
- 上述计数为本轮收尾时的结果；第十一轮（见 `docs/superpowers/plans/2026-09-13-sftp-upload-partial-write.md`）新增 SFTP 上传检查与 3 个前端用例后，E2E 为 15/15 PASS、前端为 30 文件 / 222 用例。

## 1. 目标

修复三个同源缺陷（用户报告 + E2E 复现）：

1. `sz <大文件>`（实测 ≥1 MiB，4 MiB zip 必现）传到中途会话被拆：终端显示“远端会话已结束 / 远端退出码：0”，浏览器下载不到文件。
2. `rz` 选择文件后传输失败：远端打印 `rz: <name> removed.`，终端随后出现大段“乱码”（实为 pty 回显：rz 退出后仍在途的上传字节被 shell 当作键入回显）。
3. 通道关闭后刷新页面只得到死路错误“找不到当前浏览器会话中的 SSH 凭据，请重新发起连接”，没有自助恢复入口。

非目标：不改变 ZMODEM 协议栈位置（仍在浏览器）；不引入新的 frame 类型；不修改 Agent 的流控实现（Agent 侧已正确，见根因）。

## 2. 根因（已用 E2E + 服务端诊断日志证实）

本地 Agent relay 路径（`internal/server/agent_relay_transport.go`）**完全没有参与流控**，而协议与 Agent 侧的流控原语都已存在：

- 下载方向（`sz`）：Server 在 `OPEN_STREAM.Window` 里通告 0（WebSSH broker 的 `relay.StreamRequest.InitialWindow` 未设置）。Agent 侧 `StreamDispatcher.completeDial` 只在 `initialWindow > 0` 时创建 `sendState`；为 0 时 `waitForSendWindow` 直接返回 nil，即**不限速**。于是 Agent 以全速把 pty 输出灌进连接，Server 侧 `agentRelayStream.readCh`（16 帧 × ≤32 KiB = 512 KiB）被灌满，`enqueue` 返回 `relay.ErrBackpressure`，dispatch 将其当致命错误：`stream.fail + RESET` → bridge 关闭 → 远端 shell 收到 EOF/HUP 退出（退出码 0）。服务端桥接诊断日志实测：`first=relay: backpressure`。
- 上传方向（`rz`）：Agent 用 `defaultAgentReceiveWindow = 256 KiB` 建立 `receiveState` 并**强制执行**（超限 `rejectAndClose` + RESET），同时按 128 KiB 阈值回 `WINDOW_UPDATE`；但 Server 的 `agentRelayStream.Write` 从不 `ConsumeSend`、也从不消费 controlCh 里已有的 `WINDOW_UPDATE`，即 Server 侧不限速 → 冲破 Agent 接收窗口或撑满 Agent 的 fair-writer 队列（256 KiB，`session: writer backpressure`）→ Agent RESET → 上传中断；在途字节在 rz 退出后被 pty 回显，形成终端乱码。
- 旁证：`internal/client/session.go:739`、`internal/server/ws_client.go:568` 的 client 路径都做了 `ConsumeSend`，唯独本地 Agent relay 路径缺失；`relay/transport.go:352` 的跨节点 gRPC 路径有 `WriteControl` 通道，需一并审计。
- 浏览器侧两个 4 MiB 硬上限（`ssh-client.ts` 接收队列、`byte-stream.ts` `bufferedAmount`）在快速链路上会把“瞬时积压”变成“杀死会话”：它们无法真正省内存（WS 内部缓冲同样占内存），却会在突发时误杀。应改为等待式背压 + 更高的 OOM 兜底。
- 传输被拆后 UI 只报“远端退出码：0”，掩盖了真实原因；刷新后 `missingSession` 是死路。

## 3. 架构决策

### 3.1 让本地 Agent relay 路径补齐双向流控（不改协议、不改 Agent）

- 下载方向：Server 在 `OPEN_STREAM.Window` 通告接收窗口 `W_recv`；`W_recv` 取 `readCh` 的字节容量（16 × 32 KiB = 512 KiB），保证“Agent 在窗口内发送”时 `enqueue` 永不溢出。Server 在 `agentRelayStream.Read` 消费字节后按 128 KiB 阈值回 `WINDOW_UPDATE`（复用现有 `WriteControl`），Agent 已有的 `waitForSendWindow` 会据此阻塞/恢复，背压自然传导到 `sz` 的 pty 写。
- 上传方向：Server 以 Agent 的已知常量 `defaultAgentReceiveWindow`（256 KiB）初始化每流 `sendState`（该常量从 `internal/agent` 下沉到 `internal/protocol` 共享，保持单一来源）；`Write` 先 `ConsumeSend`，窗口耗尽时**阻塞等待**（由 controlCh 已有的 `WINDOW_UPDATE` 帧 `AddSendWindow` 并唤醒），连接关闭/RESET 时唤醒并返回错误。阻塞发生在每流的写路径上，不阻塞共享 dispatch goroutine，避免 head-of-line。
- `enqueue` 保留 `ErrBackpressure` 作为防御性致命错误（窗口正确时不应触发），并补一条 `window > readCh 容量` 的启动期断言/测试。
- 跨节点 gRPC relay 路径审计：确认 `grpcStreamConn` 的读侧同样释放窗口、写侧同样消费窗口；若缺失按同一模式补齐（同一常量、同一阈值）。

### 3.2 浏览器侧：等待式背压 + 更高的兜底 + 真实错误

- `byte-stream.ts`：`bufferedAmount` 超过高水位（1 MiB）时**await 回落**（轮询 `bufferedAmount` + `onclose` 唤醒），仅在绝对兜底（64 MiB，对端真正停死）才抛错；抛错文案可被 UI 识别。
- `ssh-client.ts`：接收队列兜底从 4 MiB 提到 64 MiB（仅作 OOM 防线）；触发时把 `transportError` 暴露给通道退出信息，而不是让 UI 误报“远端退出码：0”。
- `WebSSHTerminal.vue`：通道因传输错误结束时显示错误横幅（含原因类别），与“远端正常退出”区分；i18n 双语。

### 3.3 刷新自愈

- `WebSSHTerminal.vue` 的 `missingSession` 不再是死路：sessionStorage 中已有 `remoteServerId` 时，自动走“重新连接”既有路径（有自动认证凭据则直接重建会话，否则打开认证弹窗）；无法解析服务器时才回退到 `/remote-servers` 列表。

## 4. 技术栈与规格引用

- Go：`internal/protocol/stream.go`（已有 `ConsumeSend`/`AddSendWindow`/`ErrWindowExhausted`）、`internal/server/agent_relay_transport.go`、`internal/server/webssh_broker.go`、`internal/relay/transport.go`（审计）。
- 前端：`web/src/webssh/byte-stream.ts`、`web/src/webssh/ssh-client.ts`、`web/src/views/WebSSHTerminal.vue`、i18n。
- 协议：不新增 frame 类型、不改 codec；仅启用既有 `Window`/`WINDOW_UPDATE` 语义，旧 Agent（窗口通告为 0 的历史行为）不受影响——新 Server 对旧 Agent 仍通告窗口，旧 Agent 忽略窗口即不限速，等价于现状，不会更差。

## 5. 全局约束

- 阻塞等待必须有唤醒路径：连接关闭、RESET、ctx 取消；禁止无超时的裸阻塞。
- 不在共享 dispatch goroutine 上阻塞；每流独立等待。
- 所有新行为先写失败测试（红），再最小实现（绿）。
- 秘密/终端字节不进日志；诊断日志只记字节数与错误类别。

## 6. 精确文件清单

修改：
- `internal/protocol/stream.go`（如需补 `TryConsumeSend`/等待辅助）、`internal/protocol/window.go` 新增共享常量 `DefaultAgentReceiveWindow`、`DefaultServerReceiveWindow`、`WindowUpdateThreshold`。
- `internal/agent/session.go`：改用 `protocol.DefaultAgentReceiveWindow` 等共享常量（行为不变）。
- `internal/server/agent_relay_transport.go`：`sendState` 建立与消费、controlCh `WINDOW_UPDATE` 应用与唤醒、`Read` 释放窗口、`Write` 阻塞等待。
- `internal/server/webssh_broker.go`：`StreamRequest.InitialWindow = protocol.DefaultServerReceiveWindow`。
- `internal/relay/transport.go`：gRPC 路径窗口审计结论落地（补齐或记录已覆盖）。
- `web/src/webssh/byte-stream.ts`、`web/src/webssh/ssh-client.ts`、`web/src/views/WebSSHTerminal.vue`、`web/src/i18n/messages/{zh-CN,en-US}.ts`。
- 测试：`internal/server/agent_relay_transport_test.go`、`internal/server/webssh_broker_test.go`、`internal/relay/transport_test.go`、`web/src/webssh/byte-stream.spec.ts`、`web/src/webssh/ssh-client.spec.ts`、`web/src/tests/zmodem-terminal.spec.ts`。
- 文档：`docs/user-guide/server-admin.md`（刷新自愈与传输错误提示）、`docs/operations/configuration.md`（流控常量说明）、`docs/pull-requests/2026-09-12-admin-webssh-sftp.md`（第十轮）。

## 7. TDD 任务

### T1 共享窗口常量与单测（红→绿）
- 新增 `internal/protocol/window.go` 常量；`internal/agent/session.go` 替换本地常量。
- 红：断言 `protocol.DefaultServerReceiveWindow == 16*32KiB` 且与 `agentRelayStream.readCh` 容量一致的测试先失败。

### T2 Server 下载方向流控（红→绿）
- 红测试：构造慢读者（broker 读得慢），断言 Agent 侧发送被窗口限制、`enqueue` 不再返回 `ErrBackpressure`、读者恢复后 `WINDOW_UPDATE` 使发送继续；当前代码先失败（RESET）。
- 实现：`Read` 消费后按阈值 `WriteControl(WINDOW_UPDATE)`；`webssh_broker` 设置 `InitialWindow`。

### T3 Server 上传方向流控（红→绿）
- 红测试：Server `Write` 超过 256 KiB 且无 `WINDOW_UPDATE` 时阻塞而非冲破；收到 `WINDOW_UPDATE` 后继续；连接关闭时唤醒返回错误。当前代码先失败（不限速/Agent RESET）。
- 实现：`sendState` + controlCh 应用 `AddSendWindow` + 条件变量/chan 唤醒。

### T4 gRPC relay 路径审计与对齐
- 用 T2/T3 的跨节点变体测试覆盖；缺失则同模式补齐。

### T5 浏览器等待式背压（红→绿）
- 红：`byte-stream.spec.ts` 造一个 `bufferedAmount` 恒高于 1 MiB 的假 socket，断言 `write` 等待回落而非抛错；64 MiB 兜底才抛错。`ssh-client.spec.ts` 断言 4→64 MiB 兜底且错误可识别。
- 实现两处改动。

### T6 终端错误可见 + 刷新自愈（红→绿）
- 红：视图测试断言传输错误显示错误横幅（非“远端退出码：0”）；`missingSession` 且 sessionStorage 有 `remoteServerId` 时触发重连/弹窗而非死路错误。
- 实现 + i18n 双语键。

### T7 E2E 与门禁
- harness（`/tmp/tm-e2e`，保留 4 MiB zip 与 1.5 MiB 文本载荷）12/12 全绿，含 `sz`/`rz` 字节一致；追加“刷新终端页自动重连”检查。
- `go test ./... -count=1`、`go test -race ./...`、`go vet ./...`、`git diff --check`、`cd web && npm test -- --run && npm run build`、`rsync + verify-web-embed.sh`。

### T8 文档与 PR 第十轮

## 8. 验证命令

```bash
go test ./internal/server ./internal/relay ./internal/protocol ./internal/agent -count=1
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web && npm test -- --run && npm run build
rsync -a --delete web/dist/ internal/server/web_dist/ && ./scripts/verify-web-embed.sh
cd /tmp/tm-e2e && node run9.mjs   # 4 MiB sz + 1.5 MiB rz，12/12
```

## 9. 回滚注意事项

- 流控为双侧常量约定：回退 Server 后 `OPEN_STREAM.Window` 回到 0，Agent 行为退回不限速（现状），不会引入新故障。
- 浏览器改动纯前端，回退产物即消失。
- 无 Schema、无配置项、无 API 变更。
