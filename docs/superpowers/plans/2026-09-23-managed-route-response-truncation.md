# 托管路由大响应体截断修复实施计划

状态：补记计划（原始实现已完成）

补记说明：用户报告“通过代理请求的托管路由，大一点的 js 会加载中断”（`https://tm-6000d.tm.example.com/assets/mode-DouGTm1Q.js`）。按 `AGENTS.md` 的紧急修复条款先止损再补记；红灯证据为实现过程中的真实观察，绿灯与门禁结果在补记时重新执行，见第 7 节。

## 1. 目标

1. 托管路由下载方向（浏览器 → Server → Agent → 内网服务）的大响应体完整送达，不再在 256 KiB 附近被截断。
2. Client 侧本地发布与端口转发的批量写入在窗口耗尽时等待额度，而不是直接失败。
3. 中继泵入队失败时对端必须收到 `RESET`，把截断变成显式错误而不是静默短包。

## 2. 非目标

- 不改 `copyResponse` 中 `io.Copy` 吞错误的行为（可观测性缺口，列为后续改进项）。
- 不调整窗口、帧大小等协议常量的数值。
- 不引入新配置项：三层背压仍然零配置。

## 3. 根因与证据

链路：浏览器 → Server HTTP handler → WebSocket → Agent `readBack` → 内网目标。

1. Server 在 `OPEN_STREAM` 中为每条 Agent 流通告 `protocol.DefaultServerReceiveWindow`（512 KiB），见 `internal/server/agent_relay_transport.go:128`。
2. Agent 的 `FairFrameWriter` 每流队列却由空的 `FairWriterConfig{}` 构造，落到 `internal/session/fair_writer.go:89` 的默认 262144（256 KiB）。
3. `EnqueueData` 是非阻塞的，队列满即返回 `ErrStreamQueueFull`（`internal/session/fair_writer.go:147`）；`readBack` 把它当致命错误 `removeAndClose`，且不发 `RESET`。
4. 流于是在 Server 侧凭空消失，`copyResponse` 的 `_, _ = io.Copy(w, resp.Body)` 吞掉错误，浏览器在 `Content-Length` 完整的情况下收到短包，表现为“加载中断”。只有在途字节超过 256 KiB 的响应才会触发。

不变量推导：泵先扣额度再入队，对端只在真正读走字节后才回补 `WINDOW_UPDATE`，因此

```
queued = consumed - sent <= initialWindow + updates - sent <= initialWindow
```

即“每流出站队列 ≥ 对端通告的窗口”是队列满不可达的充要条件。队列小于窗口不是额外的安全边界，而是把正常背压变成截断传输。该不变量本轮写入 `internal/protocol/window.go` 的包注释，作为后续改动的约束。

同类缺陷此前已修过两次，可作 Reviewer 参照：`docs/pull-requests/2026-09-12-admin-webssh-sftp.md`、计划 `docs/superpowers/plans/2026-09-12-webssh-bulk-transfer-flow-control.md`、`docs/superpowers/plans/2026-09-13-sftp-upload-partial-write.md`。

红灯证据（实现期间观察）：

1. `TestStreamDispatcherLargeResponseSurvivesSlowWriter`：慢消费者下 1 MiB 响应只送达 0 字节，失败信息 `relay pump abandoned the response; only 0 of 1048576 bytes`。
2. `TestSessionWriterQueueCoversAdvertisedCredit`：断言 Agent 每流队列不小于 Server 通告窗口，修复前 262144 < 524288 失败。
3. `TestSessionFlowControlBulkWriteIsDeliveredIntact`：1 MiB 写入在旧行为下返回 `protocol: window exhausted`，字节不完整。

## 4. 架构决策

1. **队列从额度推导，而不是各自选取**：Agent 每流队列取 `protocol.DefaultServerReceiveWindow`，与 Server 通告值同源，常量集中在 `internal/agent/session.go` 并注明推导关系。
2. **写入等待额度**：`frameStream.Write` 按 `protocol.MaxStreamFrame`（32 KiB）切块逐块 `ConsumeSend`；额度不足时阻塞在 `windowSignal`（由 `WINDOW_UPDATE` 处理路径唤醒）加 100 ms 兜底轮询上，而不是返回错误。阻塞只发生在每流写路径，不占用共享接收循环，单条流停滞不会 head-of-line 阻塞其它流。切块本身也是必要的：编码器可接受 `MaxPayload`，但对端入站预算远小于该值并会拒绝超大帧。
3. **失败要响亮**：Agent `readBack` 的 DATA 与 HALF_CLOSE 发送失败、Server `relayToClient` 的入队失败，都补发 `RESET`，让对端知道字节流提前结束。
4. **远端 HALF_CLOSE 不终止写**：等待额度期间，只结束读方向的 `io.EOF` 不得中止仍欠额度的写；其余终态与会话关闭必须中止。

## 5. 文件清单

- `internal/agent/session.go`：新增常量 `streamQueueBytes` 并传入 `NewFairFrameWriter`；`readBack` 两处发送失败补 `sendReset`。
- `internal/client/session.go`：新增 `windowWaitInterval` 与 `frameStream.windowSignal`；`Write` 改为切块加 `waitForSendWindow`；`WINDOW_UPDATE` 处理路径唤醒等待者。
- `internal/server/ws_client.go`：`relayToClient` 入队失败补发 `FrameReset`。
- `internal/protocol/window.go`：记录 `queued <= initialWindow` 推导与队列下限不变量。
- 测试：`internal/agent/session_test.go`（新增 2 例）、`internal/client/session_test.go`（新增批量完整性用例，按新语义改写 2 处旧契约）。
- 文档：本文件、`docs/user-guide/server-admin.md`（流控表新增 Client 行与不变量段落）、`docs/user-guide/managed-http-route.md`（新增“大响应体与流控”）、`docs/pull-requests/2026-09-23-managed-route-response-truncation.md`。

## 6. TDD 步骤

红灯见第 3 节，最小实现见第 4 节，绿灯与门禁见第 7 节。既有测试 `TestSessionFlowControlAdvertisesWindowAndWaitsForCredit` 的原契约“超额发送必须被拒绝”本身就是缺陷，已改写为“超额发送必须等待额度”；`TestSessionFlowControlAppliesPeerWindowUpdate` 按 32 KiB 切块语义调整断言。

## 7. 验证命令与结果（补记时重新执行）

```bash
go build ./...
go test ./... -count=1
go test -race ./internal/agent ./internal/client ./internal/server ./internal/session ./internal/protocol -count=1
go vet ./...
git diff --check
```

结果：`go build ./...` 通过；`go test ./... -count=1` 全量通过，无失败包；`go vet ./...` 无输出；定向 race 全部通过（agent 4.2s、client 4.1s、server 452.9s、session 2.0s、protocol 1.3s）；`git diff --check` 无输出。本轮未改前端，故未执行 `npm test` 与 `npm run build`。

## 8. 发布与回滚注意事项

- Server 与 Agent/Client 需要同版本发布。灰度顺序为**先 Agent 后 Server**或同时升级：新 Agent 的大队列对旧 Server 无害，而旧 Agent 配新 Server 会重现截断。只升级 Agent 即可修复托管路由截断，Client 的批量写入修复需要升级 Client。
- 无 API、Schema 和配置变更，回滚即回退二进制；回滚后大响应体截断与 Client 批量写入失败会恢复。
- 已知残留：`copyResponse`（`internal/server/http_proxy.go`）与 `internal/client/forward.go` 仍吞掉 `io.Copy` 错误。截断现在会通过对端 `RESET` 显式化，但 Server 访问日志不会记录短包，列为后续可观测性改进项。
