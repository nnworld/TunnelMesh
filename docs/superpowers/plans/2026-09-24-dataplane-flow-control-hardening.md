# Agent / Server / Client 数据面流控与阻塞隔离加固 Implementation Plan

**Goal:** 修复三方数据面审计出的 3 个 P0、3 个 P1、3 个 P2 缺陷：发送侧每流队列永不回收、接收循环内同步写目标导致跨流头阻塞、dial 期间早到 DATA 不归还信用可致流永久死锁、`bufferedStream` 丢失半关闭、升级路径丢失 Hijack 缓冲字节、截断响应无日志、对端通告 window 无夹紧、窗口类配置项为空转、UDP association 无上限。

**Architecture:** 不改帧格式与协议版本，不新增能力协商。所有"阻塞"从三个共享接收循环（`StreamDispatcher.Handle`、`serveClientSession` 帧循环、`Session.Run`）移到每流泵 goroutine + 字节有界队列；队列容量始终 ≥ 对端通告的 credit，沿用 `internal/protocol/window.go:26-40` 已文档化的不变量，使队列满只对单流 RESET。信用回收规则统一为"消费即累计、达阈值或剩余窗口低于单帧上限即刷新"，由 `internal/protocol` 提供单一实现供三端复用。分层保持 Handler → Service → Repository/Adapter，不动存储与 API 层。

**Tech Stack:** Go 标准库（新增 `log/slog` 于 `internal/client`、`internal/agent`）、`golang.org/x/net/websocket`、`github.com/prometheus/client_golang`（本次不新增标签维度）、stdlib `testing`。

**规格引用:** 无新增设计规格；约束来源为 `AGENTS.md`（协议演进约束、高可用与容错、可观测性）与 `docs/protocol/websocket-frame.md`。关联记录：`docs/superpowers/specs/2026-09-10-socks5-web-page-latency-design.md`、`docs/superpowers/plans/2026-09-11-socks5-web-page-latency-implementation.md`（本计划继承其"阻塞不出接收循环""队列 ≥ credit"两条不变量并补齐执行点）。

## 全局约束

- 不做 worktree，直接在 `/Users/z-yuchangjun/.codex/worktrees/6215/TunnelMesh` 上实现。
- TDD 强制：每个任务先写失败测试、运行并记录红→再最小实现→再跑聚焦与全包测试。
- 不改变任何帧类型、能力名、`schema_meta.version`、OpenAPI 语义；因此本计划**不触发** `migrations/` 与 `docs/api/openapi.yaml` 变更（`docs/operations/configuration.md` 除外，见 T8）。
- 新增日志字段不得包含 secret、密码、私钥、完整 Authorization、DSN、原始字节、目标主机名以外的敏感值；允许 `agent_id`、`route`、`protocol`、`stream_id`、字节数与状态码（与现有 `proxy_tunnel_closed` 一致）。
- Prometheus 标签不得新增 `stream_id`/`connection_id`/目标地址维度；本计划只做日志与既有指标，不加新指标。
- 不引入新第三方依赖；不使用 `goleak`，goroutine 泄漏用显式退出信道与 `WaitGroup` 断言。
- 队列/窗口常量若可配置，必须满足并校验不变量：`advertisedWindow - windowUpdateThreshold >= maxStreamFrame` 且 `queueBytes >= advertisedWindow`。
- 每个任务必须独立编译、独立通过其聚焦测试后才进入下一个。
- 未经用户明确授权，不执行 commit / push / merge / 创建远端 PR。
- 顺序：T1、T2、T6、T7、T9 互相独立；T3 → T4 → T5 有依赖（T4 复用 T3 的下行投递路径，T5 复用 T3/T4 确立的 `NegotiateReceiveWindow` 与泵形态）；T8 依赖 T3、T5 的队列尺寸入口。

---

## T1 FairFrameWriter 回收空闲每流队列（P0-1）

**症状与根因**：`internal/session/fair_writer.go:145-172` 为每个 `streamID` 懒建队列并追加 `w.order`，`removeStream` 只在 `TryPush` 失败且 `created` 时调用；流正常结束/RESET/半关闭均无回收，`nextDataLocked`（`:275-290`）在持有 `w.mu` 时全量扫描 `w.order`，而 wire ID 单调不复用（`internal/server/agent_relay_transport.go:106-113`），于是每帧成本随连接生命周期线性增长，最终把有界队列拖满 → 拒帧 → 单流 RESET → 大响应截断。

**文件清单**
- 修改 `internal/session/fair_writer.go`
- 修改 `internal/session/fair_writer_test.go`

**接口约定**
- `func (w *FairFrameWriter) nextDataLocked() (protocol.Frame, bool)` 语义变更：一次公平轮内，弹出后仍为空的流队列被就地摘除（`streams` 与 `order` 同步收缩），且摘除必须在持有 `w.mu` 的同一临界区内完成。
- `func (w *FairFrameWriter) removeStreamLocked(streamID uint32, stream *fairStreamQueue)`：新增（不取锁的内部实现）；`removeStream` 保留为加锁包装，供 `EnqueueData` 失败分支使用。
- `func (w *FairFrameWriter) EnqueueData(streamID uint32, frame protocol.Frame) error`：查找队列 + `TryPush` 必须整体在 `w.mu` 下完成，消除"已取到 queue 指针但已被摘除并 Close"导致的一次伪 `ErrStreamQueueFull`。
- 新增 `func (w *FairFrameWriter) StreamQueueCount() int`：只读观测，供测试与后续运维使用（无锁内调用）。

**TDD 步骤**
1. 先在 `fair_writer_test.go` 写 4 个测试：
   - `TestFairWriterRetiresEmptyStreamQueues`：64 个流各推 1 帧，跑 `Run` 发完后断言 `StreamQueueCount()==0` 且 `len(w.order)==0`。
   - `TestFairWriterPreservesOrderAcrossQueueRetirement`：流 7 推 1 帧发出、被回收，再推 3 帧；断言 sender 观测到的流 7 帧顺序与 payload 序列一致。
   - `TestFairWriterScanStaysBoundedAcrossManyStreams`：顺序开闭 20000 个流，断言 `StreamQueueCount() <= 4`，并用带计数的 sender 断言总发送调用次数与帧数同阶（线性而非二次）。
   - `TestFairWriterConcurrentEnqueueDuringRetirement`：8 goroutine 对同一批流 ID 持续 `EnqueueData`，在窗口内不得出现 `ErrStreamQueueFull`；用 `-race` 跑。
2. 运行 `go test ./internal/session -run 'FairWriter(Retires|Preserves|ScanStays|Concurrent)' -count=1`，确认前 3 个红：预期 `StreamQueueCount()` 恒等于历史流数、`len(w.order)` 线性增长，第 3 个测试因二次扫描显著超阈值失败；第 4 个在实现前也应通过（记录为回归护栏）。
3. 最小实现：抽出 `removeStreamLocked`；在 `nextDataLocked` 中弹出后若 `stream.queue.Len() == 0` 则收集 id，轮结束后统一摘除（摘除时按现有逻辑修正 `w.cursor`）；`EnqueueData` 把 `TryPush` 移入 `w.mu` 临界区。不改变控制帧优先级与 quantum 语义。
4. 再跑同一条聚焦命令至全绿，随后 `go test ./internal/session -count=1`、`go test -race ./internal/session -count=1`。

**回滚注意事项**：纯进程内调度改动，无协议/存储影响，`git revert` 即可；回滚后表现为长连接吞吐衰减但不产生数据错误。

---

## T2 `serveClientSession` 的 `seen` 有界化 + 并发上限改判 live（P0-1 同类）

**症状与根因**：`internal/server/ws_client.go:263-270` 的 `seen` 只增不删，且第 265 行以 `len(seen) >= openLimit` 触发整会话 `GOAWAY`（`maxClientOpenAttempts = 1<<16`，`internal/server/ws_client.go:22`）。因此该上限实际是"累计开流数"，一个长期存活的 client 会话会被自己的历史流量判死并踢下线。

**文件清单**
- 修改 `internal/server/ws_client.go`
- 修改 `internal/server/ws_client_test.go`

**接口约定**
- `seen` 变为有界 FIFO：保留 `seen map[uint32]struct{}` + 新增 `seenOrder []uint32`，插入新 ID 时若 `len(seenOrder) > openLimit` 则淘汰最旧 ID（`delete(seen, oldest)`），保持"最近 `openLimit` 个 ID 拒绝重用"。
- `GOAWAY` 判据改为实时并发：`len(streams)+len(openings) >= openLimit`。
- 会话结束路径（现有 `defer` 清理，`internal/server/ws_client.go:108-131`）不需额外改动。

**TDD 步骤**
1. 写 `TestServeClientSessionAllowsManySequentialStreamIDs`：`serveClientSessionWithOpenLimit(ctx, ..., openLimit=4, ...)`，顺序打开并正常关闭 12 条流，断言会话不被 `GOAWAY` 终止、12 次 open 全部成功（当前实现第 5 次即 GOAWAY → 红）。
2. 写 `TestServeClientSessionGoAwayOnConcurrentOpenLimit`：同时保持 4 条活流（不关闭），第 5 条 open 触发 `GOAWAY`（断言 `Serve` 返回 nil 且收到 GOAWAY 帧）。
3. 写 `TestServeClientSessionRejectsReusedStreamIDWithinWindow`：在 `openLimit` 窗口内重用 ID → 收到 `RESET`，行为不变（护栏）。
4. 运行 `go test ./internal/server -run 'TestServeClientSession(AllowsManySequential|GoAwayOnConcurrent|RejectsReused)' -count=1` 观察第 1 项红。
5. 最小实现上述 FIFO + live 判据，复跑至绿；再 `go test ./internal/server -run TestServeClientSession -count=1`。

**回滚注意事项**：只影响单 client 会话的准入判定；回滚即恢复"累计开流 GOAWAY"。若现网依赖旧行为做硬限流，需改用 T3/T5 的每流隔离 + 观测计数，不额外记录为债务（并发上限仍由 `len(streams)` 提供）。

---

## T3 Agent 下行（Server→Agent→目标）写隔离（P0-2）

**症状与根因**：`internal/agent/session.go:220` 在 `StreamDispatcher.Handle` 内同步 `entry.conn.Write(f.Payload)`，而 `Handle` 由 `Session.Run` 的唯一接收循环内联调用（`internal/agent/session.go:930-935`）；全仓 `internal/agent/` 无任何 `SetWriteDeadline`。一个停读的目标会冻结该连接上所有流，并让 `internal/agent/session.go:907-912` 的 PONG 也发不出去，而服务端没有心跳超时清扫（`internal/server/session_manager.go:96` 仅记录 `lastHeartbeat`），最终表现为"列表在线、转发为 0"的静默死锁。这与 `internal/client/session.go:776-780` 自述的不变量直接冲突。

**文件清单**
- 修改 `internal/agent/session.go`
- 修改 `internal/agent/session_test.go`
- 只读复用 `internal/session/bounded_queue.go`（`TryPush`/`Pop`/`Close` 已具备所需语义，无需改动）

**接口约定**
- `streamEntry` 新增字段：`inbound *streamsession.BoundedFrameQueue`、`inboundClosed atomic.Bool`（避免重复 `Close`）、`pumpDone chan struct{}`（泵退出信号，供测试与 `Close` 断言）。
- `Handle` 的 `protocol.FrameData` 分支：不再写目标、不再做 `receiveState.Handle`、不再调用 `releaseReceiveWindow`；只做 `localHalf` 校验 + `entry.inbound.TryPush(frame)`。`TryPush` 失败（仅可能由不守 credit 的对端造成）→ `rejectAndClose`。
- 新增 `func (d *StreamDispatcher) pumpTarget(id uint32, entry *streamEntry)`：`Pop` → `receiveState.Handle(dataFrame)`（错误 → `rejectAndClose`）→ `conn.Write` → 短写/错误 → `rejectAndClose` → `releaseReceiveWindow(id, entry, written)`；`Pop` 返回 `false`（队列被 Close）即退出并 `close(entry.pumpDone)`。
- `completeDial` 创建 entry 时按 `defaultAgentReceiveWindow` 建 `inbound`，`d.readers` 计入两个 goroutine（`readBack` + `pumpTarget`）；`removeAndClose`、`handleHalfClose` 完成分支、`Close()`、`FrameReset` 分支统一 `entry.inbound.Close()`（幂等由 `inboundClosed` 保证）。
- `releaseReceiveWindow`、`waitForSendWindow` 签名不变（实现改动见 T4）。

**TDD 步骤**
1. 写 `TestAgentTargetWriteDoesNotBlockOtherStreams`：流 A 的 `io.ReadWriteCloser` 的 `Write` 阻塞在信道直到测试放行；对同一 dispatcher 的流 B 完整双向传输并断言完成（当前实现：`Handle` 卡死 → B 永不完成，测试超时红）。
2. 写 `TestAgentInboundQueueOverflowResetsOnlyOffendingStream`：对端在 `defaultAgentReceiveWindow` 之外继续灌 DATA（不发 WINDOW_UPDATE 也不等信用），断言仅该流被 RESET、dispatcher 仍可服务新流、`ActiveStreams()` 收敛。
3. 写 `TestAgentStreamResetUnblocksTargetWritePump`：流被 `FrameReset` 后关闭目标 conn，断言 `pumpDone` 被关闭且 `Close()` 不在 `readers.Wait()` 上挂死。
4. 运行 `go test ./internal/agent -run 'TestAgent(TargetWriteDoesNot|InboundQueueOverflow|StreamResetUnblocks)' -count=1 -timeout 60s`，记录第 1、3 项红（第 1 项预期 `test timed out` 或断言 B 未完成；第 3 项预期 `pumpDone` 永不关闭）。
5. 最小实现上述改动，复跑至绿；再 `go test ./internal/agent -count=1`、`go test -race ./internal/agent -count=1`。

**回滚注意事项**：单进程行为，无协议变化；回滚 `git revert` 立即生效。内存上界变化需在回滚说明中写明：每流新增 `inbound` 队列上限 `256 KiB`（与既有 `streamQueueBytes` 同量级），1024 条并发流最坏 `256 MiB`，由 Agent 侧既有 `max_concurrent_dials` 与 Server `openLimit` 间接限制；T8 会把该尺寸接到 `agent.streams.inbound_buffer_bytes`。

---

## T4 早到 DATA 归还信用 + 剩余窗口不足一帧时强制刷新（P0-3）

**症状与根因**：dial 未完成时 DATA 走 `internal/agent/session.go:202-207` 存入 `pending.data`，拨号完成后在 `internal/agent/session.go:396-401` 直接写目标，全程不触发 `releaseReceiveWindow`（唯一调用点在 `Handle`，见 `internal/agent/session.go:228`）。Server 侧 `sendState` 初值 `DefaultAgentReceiveWindow=256 KiB`（`internal/server/agent_relay_transport.go:129-132`），刷新阈值 128 KiB（`internal/protocol/window.go:22-24`），而 `waitForSendWindow`（`internal/agent/session.go:566-596`）与 `consumeSendWindow`（`internal/server/agent_relay_transport.go:492-570`）都无超时 → 早到数据 >128 KiB 时两侧永久互等。

**文件清单**
- 修改 `internal/protocol/window.go`（新增统一刷新判据与通告窗口夹紧）
- 新建 `internal/protocol/window_test.go`
- 修改 `internal/agent/session.go`
- 修改 `internal/agent/session_test.go`
- 修改 `internal/client/session.go`（`frameStream.releaseReceiveWindow` 复用同一判据）
- 修改 `internal/client/session_test.go`
- 修改 `internal/server/agent_relay_transport.go`（`releaseReceiveWindow` 复用同一判据）
- 修改 `internal/server/agent_relay_transport_test.go`

**接口约定**
- `internal/protocol/window.go` 新增：
  - `func ShouldFlushWindowUpdate(remaining uint32, unacked uint32, threshold uint32) bool`：`unacked > 0 && (unacked >= threshold || remaining < MaxStreamFrame)`。这是三端唯一实现。
  - `func NegotiateReceiveWindow(advertised uint32) uint32`：`0` 或 `< 2*DefaultWindowUpdateThreshold` 或 `> DefaultServerReceiveWindow` 一律回退 `DefaultServerReceiveWindow`，保证 `advertised - threshold >= MaxStreamFrame` 且 `receiveBudget` 有界（本条同时服务 T5、T8）。
- `releaseReceiveWindow` 三端实现改为：读 `state.ReceiveWindow()` 得到 remaining，调用 `ShouldFlushWindowUpdate`，其余（累计、清零、`AddReceiveWindow`、发帧）不变。
- Agent 早到数据：`completeDial` 不再自己 `conn.Write(earlyData)`，改为把 `pending.data` 依次 `entry.inbound.TryPush`（由 `pumpTarget` 完成 receive 记账 + 写 + 归还信用）；`pending.data` 增加字节上限 `defaultAgentReceiveWindow`，超限立即 `cancelPending` + `sendReset`（防御不守 credit 的对端）。

**TDD 步骤**
1. `internal/protocol/window_test.go`：`TestShouldFlushWindowUpdate`（表驱动：达阈值 / 剩余 <32 KiB / 剩余充足且未达阈值不刷 / `unacked==0` 不刷）、`TestNegotiateReceiveWindow`（0、1024、4 GiB、合法 256 KiB、512 KiB）。先跑 `go test ./internal/protocol -run 'TestShouldFlushWindowUpdate|TestNegotiateReceiveWindow' -count=1` → 编译失败即红（函数不存在）。
2. `internal/agent/session_test.go` 新增 `TestAgentCreditsEarlyDataAfterSlowDial`：拨号阻塞期间喂入 200 KiB，放行拨号后继续要求 Agent 归还 ≥200 KiB 信用，并断言后续 200 KiB 能在超时内送达目标（当前实现红：无 WINDOW_UPDATE 且流停摆）。
3. `internal/agent/session_test.go` 新增 `TestAgentPendingBufferOverflowResetsStream`：dial 挂起时灌入 > `defaultAgentReceiveWindow` 的 DATA，断言该流被 RESET 且 `pending` 清空。
4. `internal/server/agent_relay_transport_test.go` 新增 `TestAgentRelayStreamFlushesCreditNearWindowFloor`：把 `sendState` 压到剩余 < `MaxStreamFrame`（小通告窗口 + 对端不刷）时，断言一旦消费即发出 WINDOW_UPDATE。
5. `internal/client/session_test.go` 新增 `TestClientFrameStreamFlushesCreditNearWindowFloor`：同判据的 client 侧断言。
6. 依次 `go test ./internal/protocol ./internal/agent ./internal/server ./internal/client -run '...(上列用例名)' -count=1` 记录红，再最小实现，复跑至绿；随后 `go test ./internal/protocol ./internal/agent ./internal/client -count=1` 与 `go test -race ./internal/agent ./internal/session ./internal/protocol -count=1`。

**回滚注意事项**：`ShouldFlushWindowUpdate` 只改变 WINDOW_UPDATE 发送时机（帧类型与语义不变），新旧版本互通；回滚后重新暴露早到数据死锁。若只回滚 Agent 早到数据改动而保留刷新判据，行为仍安全（更保守的刷新不是协议依赖）。

---

## T5 Server client 会话上行（Client→Server→Agent）写隔离（P0-2 同类）

**症状与根因**：`internal/server/ws_client.go:368` 在共享帧循环里 `stream.conn.Write(frame.Payload)`，其内部 `consumeSendWindow` 会无超时阻塞（`internal/server/agent_relay_transport.go:492-570`），因此单个 Agent 停读会冻结该 client 会话的全部流、`OPEN_STREAM` 准入与 PONG 回复。

**文件清单**
- 修改 `internal/server/ws_client.go`
- 修改 `internal/server/ws_client_test.go`

**接口约定**
- `clientRelayStream` 新增 `inbound *streamsession.BoundedFrameQueue`、`pumpDone chan struct{}`、`inboundClosed atomic.Bool`；`newRelayStream` 按 `NegotiateReceiveWindow(initialWindow)` 的结果设置队列字节（T8 接入配置前先用该夹紧值），并 `go` 起 `pumpClientStream(id, stream, mu, streams, reset)`。
- `protocol.FrameData` 分支：只查流、查 `clientHalfClosed`、`TryPush`；失败 → `closeStream` + `reset`。记账与写入（含 `ObserveBytes`、短写判定、错误 → `closeStream`+`reset`）全部移入泵。
- `protocol.FrameHalfClose` 分支保持同步语义：置 `clientHalfClosed` 后仍走 `CloseWrite`；泵在写侧错误时只 `closeStream`，不升级成会话错误。
- `closeStream` 追加 `inbound.Close()`（幂等），确保泵随流退出。
- `FrameReset`/`FrameWindowUpdate`/`default` 分支不变。

**TDD 步骤**
1. 写 `TestServeClientSessionUploadToStalledAgentDoesNotBlockOtherStreams`：桩 `relay.NodeTransport` 的首条流 `Write` 阻塞（不返回信用），第二条流必须完成 open + 双向传输（当前实现红：帧循环卡死）。
2. 写 `TestServeClientSessionAnswersPingDuringStalledUpload`：在流 A 停读时向会话发 `FramePing`，断言在 2 s 内收到 PONG（当前实现红）。
3. 写 `TestServeClientSessionUploadQueueOverflowResetsSingleStream`：client 无视自身通告窗口连续灌 DATA，断言仅该流 RESET 且会话仍可用。
4. 运行 `go test ./internal/server -run 'TestServeClientSession(UploadToStalled|AnswersPing|UploadQueueOverflow)' -count=1 -timeout 120s` 记录红；实现后转绿；再 `go test ./internal/server -run 'TestServeClientSession' -count=1`。

**回滚注意事项**：仅 Server 单进程；回滚恢复上行头阻塞。每流新增队列上界 = 夹紧后的通告窗口（≤512 KiB），与 T3 同量级；`openLimit` 提供总量界。

---

## T6 `bufferedStream` 转发 `CloseWrite` + 升级前排空 Hijack 读缓冲（P1-4 / P1-5）

**症状与根因**
- `internal/client/forward.go:448-454` 只实现 `Close()`；`bridge`（`internal/client/forward.go:399-441`）探测 `CloseWrite` 失败即 `halfClosed=false`（`:404-408`），随后同时关闭两端（`:422-430`），丢弃仍存活方向，与 `:395-398` 自述相反；被包的 `frameStream` 本身实现了 `CloseWrite`（`internal/client/session.go:829`）。
- `internal/client/forward.go:361-390` 的 101 分支只处理远端侧 `br.Buffered()`，从不看 `rw.Reader.Buffered()`；net/http 可能已在 101 之前把浏览器紧跟的第一个 WS 帧读进缓冲 → 静默丢字节。服务端两处已正确实现并注明原因（`internal/server/http_proxy.go:136-140`、`internal/server/proxy_entry.go:630-637`）。

**文件清单**
- 修改 `internal/client/forward.go`
- 修改 `internal/client/forward_test.go`

**接口约定**
- `func (s *bufferedStream) CloseWrite() error`：对 `s.Writer` 做 `interface{ CloseWrite() error }` 断言并委托；不支持时返回 `errors.New("client: stream does not support half-close")`（使 `bridge` 明确降级为整体关闭，而非静默）。
- 101 分支在 `_ = rw.Flush()` 之后、`br.Buffered()` 搬运之前，插入 `if buffered := rw.Reader.Buffered(); buffered > 0 { if _, err := io.CopyN(stream, rw.Reader, int64(buffered)); err != nil { _ = conn.Close(); return } }`。

**TDD 步骤**
1. `TestBufferedStreamCloseWriteDelegates`：底层为记录调用的 stub writer，断言 `CloseWrite` 命中且类型断言路径正确（当前红：方法不存在 → 编译失败）。
2. `TestForwardHTTPUpgradeForwardsBufferedClientBytes`：真实 `httptest` server + 真实 hijack，客户端先发 101 请求再立刻写入 `first-frame`，桩远端 stream 读到的必须是 `first-frame` 起始字节（当前红：读到 0 字节/EOF）。
3. `TestForwardHTTPUpgradeKeepsUploadOpenAfterRemoteEOF`：远端在 101 后写完即 EOF；断言本地后续写入仍能到达远端（用可写 spy stream 断言收到），即方向未被整体关闭（当前红）。
4. 运行 `go test ./internal/client -run 'TestBufferedStreamCloseWriteDelegates|TestForwardHTTPUpgrade' -count=1` 记录红；实现后转绿；再 `go test ./internal/client -count=1`。

**回滚注意事项**：纯 client 行为，无兼容性影响；回滚重新引入升级流截断与首帧丢失。

---

## T7 截断响应与静默丢帧补日志（P1-6）

**症状与根因**：`internal/client/forward.go:392` 与 `internal/server/http_proxy.go:175` 都是 `_, _ = io.Copy(...)`；已发 `Content-Length` 后短写只让浏览器报 `ERR_CONTENT_LENGTH_MISMATCH`，服务端/客户端不留痕迹。丢帧点同样无日志：`internal/server/ws_client.go:495-510`（队列满 → RESET）、`internal/agent/session.go:523-530`（信用等待失败 / 发送失败 → RESET）。

**文件清单**
- 修改 `internal/client/forward.go`（新增 `log/slog` 导入）
- 修改 `internal/agent/session.go`（新增 `log/slog` 导入）
- 修改 `internal/server/http_proxy.go`（`copyResponse` 返回 error 并记日志）
- 修改 `internal/server/ws_client.go`（补一条 `slog.Warn`）
- 修改 `internal/client/forward_test.go`、`internal/server/http_proxy_test.go`

**接口约定**
- `func copyResponse(w http.ResponseWriter, resp *http.Response) error`：返回 `io.Copy` 的错误；调用点（`internal/server/http_proxy.go` 内 3 处）改为 `if err := copyResponse(...); err != nil { ... }`，不改 HTTP 状态与响应体。
- 事件名固定小写下划线：`client_response_truncated`、`proxy_response_truncated`、`client_stream_queue_full_reset`、`agent_stream_send_reset`。字段：`protocol`、`status_code`、`content_length`、`bytes_copied`、`error_class`（用 `observability.NormalizeErrorClass`）。`internal/client` 不 import `internal/observability` 之外的新包，仅用 stdlib。
- 不改任何返回码、帧序、重试语义；只增加 `slog.Warn`。

**TDD 步骤**
1. `internal/client/forward_test.go` 新增 `TestForwardHTTPLogsTruncatedResponseBody`：用 `slog.New(slog.NewTextHandler(buf, nil))` 与可切换的包级 `clientLogger`（新增 `var clientLogger = slog.Default()`，测试内替换）驱动一次"响应体短于 Content-Length"的 `forwardHTTP`，断言出现 `client_response_truncated` 且 `bytes_copied < content_length`（当前红：无日志）。
2. `internal/server/http_proxy_test.go` 新增 `TestCopyResponseReportsShortBody`：桩 `resp.Body` 在读到一半时返回错误，断言 `copyResponse` 返回该错误（当前红：函数无返回值 → 编译失败）。
3. 运行 `go test ./internal/client -run TestForwardHTTPLogsTruncatedResponseBody -count=1` 与 `go test ./internal/server -run TestCopyResponseReportsShortBody -count=1` 记录红。
4. 最小实现四处日志/返回值改造，复跑至绿。
5. 追加一次性人工验证（不入库）：本地以 `tunnelmesh-client` 固定端口映射复现大 JS 资源加载，确认截断时控制台出现 `client_response_truncated`。

**回滚注意事项**：仅日志与函数签名（`copyResponse` 为包内私有，无外部调用方）。回滚 `git revert` 无兼容风险。新增日志级别为 Warn，需同步确认 `docs/operations/logging.md` 是否需要登记事件名（本计划包含该更新）。

---

## T8 通告窗口夹紧 + 窗口类配置落地/纠错（P2-7 / P2-8）

**症状与根因**
- `internal/server/ws_client.go:332`/`:340` 与 `internal/server/agent_relay_transport.go:124-131` 直接使用对端通告的 `frame.Window`：过小（本仓测试用 4096，见 `internal/server/agent_relay_transport_test.go:49`）与固定 128 KiB 刷新阈值组合成永久停摆；过大（`uint32` 可到 4 GiB）直接把 `receiveBudget` 变成无界缓冲许可。
- `internal/config/config.go:181-183`、`:246`、`:291`、默认值 `:493-495`、校验 `:684-688`、`:794`、`:805` 声明并校验 `server.stream.initial_window` / `window_update_threshold` / `max_frame_payload`、`*.stream.inbound_buffer_bytes`，但 `internal/server/runtime.go:273-275` 只消费 `max_concurrent_opens`/`max_pending_opens`/`open_timeout`，其余键在运行时完全不生效；`docs/operations/configuration.md`、`docs/operations/config-examples.md` 仍在承诺它们。

**文件清单**
- 修改 `internal/config/config.go`（`validateServerStream` 增加 `initial_window - window_update_threshold >= max_frame_payload`；`max_frame_payload != 32768` 直接报错"不支持"；`inbound_buffer_bytes >= 2*32768`）
- 修改 `internal/config/config_test.go`
- 修改 `internal/server/runtime.go`（把 `cfg.Server.Stream` 传入 `NewAgentRelayTransport`）
- 修改 `internal/server/agent_relay_transport.go`（构造参数 `agentRelayWindowConfig{advertised, threshold}`；`OpenStream` 通告窗口与 `releaseReceiveWindow` 阈值改为该配置；对端 `request.InitialWindow` 走 `protocol.NegotiateReceiveWindow`）
- 修改 `internal/server/agent_relay_transport_test.go`（构造点与新增用例）
- 修改 `internal/server/ws_client.go`（`newRelayStream` 与 `relayToClient` 使用夹紧窗口；刷新阈值走 `protocol.ShouldFlushWindowUpdate`）
- 修改 `internal/agent/session.go`、`internal/cli/root.go`（`inbound_buffer_bytes` 作为 T3 `inbound` 队列字节；常量继续作为默认值）
- 修改 `internal/client/session.go`、`internal/cli/root.go`（client `inbound_buffer_bytes` → `frameStream.readQueue` 与 writer `StreamQueueBytes` 的下界校验）
- 修改 `docs/operations/configuration.md`、`docs/operations/config-examples.md`、`docs/protocol/websocket-frame.md`（写明夹紧规则与生效范围）、`docs/operations/logging.md`（T7 事件名登记）
- 新建 `docs/architecture/adr/0007-dataplane-window-credit-invariants.md`（记录"队列 ≥ 通告窗口、窗口 − 阈值 ≥ 单帧上限、对端通告一律夹紧"三条不变量与被否决的替代方案：新增协商帧 / 每流协商）

**接口约定**
- `func NewAgentRelayTransport(manager *AgentSessionManager, windows AgentRelayWindowConfig) *AgentRelayTransport`（唯一构造点：`internal/server/runtime.go`；`AgentRelayWindowConfig{AdvertisedWindow, UpdateThreshold uint32}`，零值回落 `protocol` 默认，保证既有测试与 embedder 兼容）。
- `internal/protocol/window.go` 的 `NegotiateReceiveWindow` 为唯一夹紧入口，Server 与 Agent 都调用它，禁止本地另写 min/max。

**TDD 步骤**
1. `internal/config/config_test.go` 扩表驱动：`initial_window=65536 + threshold=131072` 必须报不变量错误；`max_frame_payload=65536` 必须报"not supported"；`inbound_buffer_bytes=32768` 必须报下界错误（当前红：全部通过校验）。
2. `internal/server/agent_relay_transport_test.go` 新增 `TestAgentRelayClampsAdvertisedWindowFromPeer`：`request.InitialWindow=1024` 时 `stream.receiveBudget` 等于夹紧值，且 >1 MiB 数据可在超时内流过（当前红：停在 1 KiB）。
3. 新增 `TestAgentRelayHonoursConfiguredWindow`：`AgentRelayWindowConfig{AdvertisedWindow: 1<<20, UpdateThreshold: 128<<10}` 时 OPEN_STREAM 帧 `Window==1<<20`（当前红：恒为常量）。
4. `internal/agent`/`internal/client` 各加一条 `inbound_buffer_bytes` 生效断言（构造 dispatcher/session 后读取 `entry.inbound` / `frameStream.readQueue` 容量）。
5. 逐条 `go test ./internal/config ./internal/server ./internal/agent ./internal/client -run '<上列用例>' -count=1` 记录红→实现→绿；ADR 与文档手工核对无孤儿链接后运行 `python3 scripts/gen_doc_index.py` 并检查 `git diff`。

**回滚注意事项**：这是本计划唯一改动配置语义的任务，必须最后合入。默认值不变（262144/131072/32768 与现常量等价），因此现网配置文件无需修改即可回滚；若运维已按文档设置过这些键，回滚后配置继续被接受但重新变为空转——需在回滚说明中显式写出。DB/Schema 无关。

---

## T9 UDP association 数量上限（P2-9）

**症状与根因**：`internal/client/udp_assoc.go:86-127` 按 `source.String()` 无界建立 association，每条对应一条隧道流；只有空闲过期（`:155-171`）与包大小上限（`:83-85`），本地任意进程可无限放大 Agent 拨号池占用。

**文件清单**
- 修改 `internal/client/udp_assoc.go`
- 修改 `internal/client/forward.go`（`UDPForwardConfig` 增 `MaxAssociations`，零值取默认）
- 修改 `internal/client/udp_assoc_test.go`
- 修改 `docs/user-guide/client.md`（如已有 UDP 转发段落则补该上限）

**接口约定**
- `const defaultUDPAssociationLimit = 1024`（不新增配置键，避免与 T8 的配置面变更混在一起）。
- `UDPAssociationConfig.MaxAssociations int`；`HandleDatagram` 在达到上限时先淘汰"最久未使用且非当前包来源"的 association，全部仍在使用则返回 `errors.New("client: UDP association limit reached")` 并丢弃该包（UDP 语义下丢包优于无界增长）。

**TDD 步骤**
1. `TestUDPAssociationLimitEvictsLeastRecentlyUsed`：塞满 `MaxAssociations=4` 后从新来源发包，断言 `Len()==4`、被淘汰者 `stream` 已 `Close`、新 association 建立成功。
2. `TestUDPAssociationLimitDropsWhenNothingIdle`：4 条同一时刻 touch，第 5 个来源返回错误且不新建。
3. 运行 `go test ./internal/client -run TestUDPAssociationLimit -count=1` 记录红（当前无上限 → 第 1 项断言失败），实现后转绿；再 `go test ./internal/client -count=1`。

**回滚注意事项**：仅 client 本地策略，回滚无数据影响；如现网依赖大量并发 UDP 源（如 DNS 递归），1024 上限需通过 `MaxAssociations` 显式调大（默认值保守）。

---

## 验证（全部任务完成后依次执行）

```bash
go build ./...
go test ./internal/session ./internal/protocol -count=1
go test ./internal/agent ./internal/client -count=1
go test ./internal/server -count=1
go test ./internal/relay ./internal/e2e -count=1
go test ./... -count=1
go test -race ./internal/session ./internal/protocol ./internal/agent ./internal/client -count=1
go test -race ./internal/server -count=1 -timeout 20m
go test -race ./... -count=1 -timeout 30m
go vet ./...
git diff --check
python3 scripts/gen_doc_index.py && git diff --stat docs/
```

Web 未被触碰，因此不需要 `npm test -- --run` / `npm run build`；如 T7 之后决定在管理后台暴露截断事件，则另开计划并补 embed 验证。

## 提交与 PR

- 完成后再向用户请求授权；授权后按任务边界拆分提交：`fix(session):`、`fix(server):`、`fix(agent):`、`fix(client):`、`docs(config):` 等，subject 祈使句 ≤50 字，body 写"为什么"。
- PR 描述文档：`docs/pull-requests/2026-09-24-dataplane-flow-control-hardening.md`（英文正文，面向 Reviewer），必须含目标分支、用户影响、无 Schema/OpenAPI 影响的显式声明、每任务测试证据（含红→绿输出摘要）、灰度与回滚步骤、Reviewer 关注点（T3/T5 的每流队列内存上界、T8 的配置语义变化）。
- 已知遗留（不在本计划范围，需在 PR 中记为技术债务）：`internal/agent/session.go:515-517` 取消时丢弃已读缓冲；`internal/agent/session.go:586-590`、`internal/server/ws_client.go:575-581` 流被移除时返回 `nil` 继续发帧；`internal/relay/transport.go:781-782` 的 `Close()` 不唤醒 `RecvMsg`；`internal/server/http_proxy.go:139-141` 的 `go io.Copy(stream, clientConn)` 无半关闭。
