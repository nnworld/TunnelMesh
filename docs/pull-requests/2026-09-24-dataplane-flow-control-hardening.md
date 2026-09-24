# Agent / Server / Client 数据面流控与阻塞隔离加固

## Title

`fix(dataplane): isolate per-stream flow control`

## Target branch

`main`

## 摘要

数据面三方审计出的 3 个 P0、3 个 P1、3 个 P2 全部修复。共同根因是**共享接收循环里存在同步阻塞点，且所有等待 credit 的 pump 都没有超时**：任何一处窗口算术或队列回收的死角都会变成永久停摆，而不是变慢。

| 编号 | 级别 | 症状 | 修复 | 位置 |
| --- | --- | --- | --- | --- |
| P0-1 | P0 | 每流发送队列永不回收，`nextDataLocked` 全量扫描随连接寿命线性变长，拖满有界队列后单流 `RESET`、大响应截断 | 一轮公平内摘除空队列，查找与入队在同一临界区内完成 | `internal/session/fair_writer.go` |
| P0-2 | P0 | Agent 在唯一接收循环内同步写目标，一个停读目标冻结该会话全部流、`OPEN_STREAM` 准入与 PONG，表现为"列表在线、流量为 0" | 每流一条下行泵 + 字节有界队列，队列满只 `RESET` 本流 | `internal/agent/session.go` |
| P0-3 | P0 | 拨号期间早到的 DATA 不归还 credit，两侧在刷新阈值上永久互等 | 早到数据与队列数据同样计入 `releaseReceiveWindow`；剩余窗口不足一帧时强制刷新 | `internal/agent/session.go`、`internal/protocol/window.go` |
| P1-1 | P1 | Server 的 client 帧循环内直接写 Agent relay，慢 Agent 阻塞整条 Client 会话（含 PING 与其它流） | 上行改为每流泵，`HALF_CLOSE` 作为哨兵帧排在同队列以保持顺序 | `internal/server/ws_client.go` |
| P1-2 | P1 | `bufferedStream` 未实现 `CloseWrite`，升级/半关闭退化为一侧关闭；Hijack 前排队的客户端字节被丢弃 | 委托 `CloseWrite`；101 之前先排空 `rw.Reader` 缓冲 | `internal/client/forward.go` |
| P1-3 | P1 | 响应被截断时没有任何日志，`ERR_CONTENT_LENGTH_MISMATCH` 无法归因 | `copyResponse` 返回错误，四处输出 `WARN` 结构化事件（不含内容、地址与凭据） | `internal/client/forward.go`、`internal/server/http_proxy.go` |
| P2-1 | P2 | 用于建缓冲的窗口数值无夹紧：过小的数值永远装不下一个整帧，过大的数值撑爆缓冲 | `NegotiateReceiveWindow` 统一夹紧，三端共用一份实现 | `internal/protocol/window.go` |
| P2-2 | P2 | `server.stream.*` 与 `*.inbound_buffer_bytes` 可写入但运行时不生效，文档却仍在承诺 | 接入实际使用点并补校验不变量，新增 ADR 记录决策 | `internal/server/{runtime,agent_relay_transport}.go`、`internal/config/config.go` |
| P2-3 | P2 | Client UDP association 无上限，本机任意进程可耗尽 Agent 拨号池 | 默认 1024 条上限 + LRU 淘汰，全部同时活跃时丢包而非无界增长 | `internal/client/udp_assoc.go` |

审计中的第 10 项（`internal/server/ws_client.go` 的 `seen` 集合与 GOAWAY 后仍可收帧）**判定为设计如此，未改动**：该路径不复用 wire ID，每流队列由 `openLimit` 间接限定条数，GOAWAY 后到达的帧只会在既有上限内被淘汰，不构成无界增长或串扰。

## 用户影响

- 经托管路由与本地转发的大体积 JavaScript、下载等固定长度响应不再偶发截断；即使仍被截断，也能在对应进程日志里查到 `client_response_truncated` / `proxy_response_truncated`。
- 一个停读的目标端只影响它自己的流，不再冻结同连接上的其它流、新建流准入和心跳，"Agent 在线但零流量"的静默死锁消失。
- 慢 Agent 不再阻塞同一 Client 会话上的其它流与 `PING`/`PONG`。
- 升级/长连接场景保留半关闭语义，且 101 之前已读入的客户端字节不再丢失。
- 现网默认值不变（Server 通告 262144、阈值 131072、单帧 32768、Agent/Client 每流缓冲 262144），无需修改配置即可获得上述修复。

## API、Schema 与配置影响

- **无** API、`schema_meta.version`、OpenAPI、frame 类型、能力名与协议版本变化；三端可任意混版滚动升级。
- 配置语义变化（详见 [ADR 0002](../architecture/adr/0002-dataplane-window-credit-invariants.md) 迁移影响）：
  - `server.stream.initial_window` / `window_update_threshold`、`agent.streams.inbound_buffer_bytes`、`client.stream.inbound_buffer_bytes` 从"接受但无效"变为真实生效。
  - 校验同步收紧：`server.stream.max_frame_payload` 只接受 `32768`；必须满足 `initial_window - window_update_threshold >= 32768`；`inbound_buffer_bytes >= 65536`。此前按文档写过非法组合的部署会在 `check-config` 阶段失败，属于预期行为。
- 文档同步：`docs/operations/configuration.md`、`docs/operations/logging.md`（新增数据面事件表）、`docs/protocol/proxy-modules.md`、`docs/user-guide/client.md`（UDP association 上限）。

## 安全与授权影响

- 新增日志事件的字段限于协议、流 ID、状态码、声明长度、实际字节数与错误分类，不含地址、响应内容、Token、DSN 与凭据。
- 每流队列与 UDP association 上限是内存/句柄放大防护；策略校验位置不变，目标地址仍在 Agent 侧二次校验。
- 不新增权限面，不放宽任何授权判定。

## 测试证据

按任务先写失败测试、记录红灯，再做最小实现转绿。新增测试：

| 任务 | 测试 |
| --- | --- |
| P0-1 | `TestFairFrameWriterRetiresEmptyStreamQueues`、`TestFairFrameWriterPreservesOrderAcrossQueueRetirement`、`TestFairFrameWriterScanStaysBoundedAcrossManyStreams`、`TestFairFrameWriterConcurrentEnqueueDuringRetirement` |
| P0-2 | `TestAgentInboundWriteReturnsBeforeTargetConsumes`、`TestAgentInboundWindowAbuseResetsOnlyOffendingStream`、`TestAgentStreamResetUnblocksTargetWritePump` |
| P0-3 | `TestShouldFlushWindowUpdate`、`TestNegotiateReceiveWindow`、`TestAgentCreditsEarlyDataReceivedWhileDialing`、`TestAgentPendingBufferOverflowResetsStream` |
| P1-1 | `TestServeClientSessionUploadToStalledAgentDoesNotBlockOtherStreams`、`TestServeClientSessionAnswersPingDuringStalledUpload`、`TestServeClientSessionUploadQueueOverflowResetsSingleStream`、`TestBoundedFrameQueueCloseAfterDrainDeliversQueuedFrames`、`TestBoundedFrameQueueCloseDiscardsQueuedFrames` |
| P1-2 | `TestBufferedStreamCloseWriteDelegates`、`TestForwardHTTPUpgradeForwardsBufferedClientBytes`、`TestForwardHTTPUpgradeKeepsDownloadOpenAfterLocalHalfClose` |
| P1-3 | `TestForwardHTTPLogsTruncatedResponseBody`、`TestCopyResponseReportsShortBody` |
| P2-1/P2-2 | `TestAgentRelayClampsUndersizedPeerWindow`、`TestAgentRelayHonoursConfiguredWindow`、`TestAgentRelayRejectsWindowWithoutFrameHeadroom`、`TestAgentInboundBufferComesFromConfiguration`、`TestClientInboundBufferComesFromConfiguration`，以及 `TestValidateStreamLatencyLimits` 新增 3 条表驱动用例 |
| P2-3 | `TestUDPAssociationLimitEvictsLeastRecentlyUsed`、`TestUDPAssociationLimitDropsWhenNothingIdle` |

红灯摘要（实现前）：

- P0-1 `TestFairFrameWriterScanStaysBoundedAcrossManyStreams` → `stream queues = 2000 after 2000 finished streams, want a bounded number`。
- P0-2 `TestAgentInboundWriteReturnsBeforeTargetConsumes` → 断言在 2 s 超时内不返回（`Handle` 与目标写同步）；`TestAgentStreamResetUnblocksTargetWritePump` → `pumpDone` 永不关闭。
- P0-3 `TestAgentCreditsEarlyDataReceivedWhileDialing` → `early DATA returned 0 credit`；`TestShouldFlushWindowUpdate`/`TestNegotiateReceiveWindow` 编译失败（`undefined: ShouldFlushWindowUpdate`）。
- P1-1 `TestServeClientSessionUploadToStalledAgentDoesNotBlockOtherStreams` → 第二条流的 DATA 永不到达 Agent；`TestServeClientSessionAnswersPingDuringStalledUpload` → 无 `PONG`。
- P1-2 `TestForwardHTTPUpgradeForwardsBufferedClientBytes` → 预读的客户端字节丢失；`TestBufferedStreamCloseWriteDelegates` → `bufferedStream has no CloseWrite`。
- P1-3 `TestForwardHTTPLogsTruncatedResponseBody` → 日志无 `client_response_truncated`；`TestCopyResponseReportsShortBody` → `copyResponse` 无返回值。
- P2-1/P2-2 `TestAgentRelayClampsUndersizedPeerWindow` → 通告窗口原样透传；`TestValidateStreamLatencyLimits` 的 `server window headroom` 与 `client inbound buffer` → 非法配置通过校验；`TestAgentInboundBufferComesFromConfiguration` → 队列容量恒为编译期默认值。
- P2-3 `TestUDPAssociationLimitEvictsLeastRecentlyUsed` → 上限关闭时 `Len() = 3` 且无错误。

配套调整（实现后必要）：`TestServeClientSessionRelayHonorsClientWindow`、`TestServeClientSessionResetsStreamAfterShortRelayWrite` 与两条 Agent relay 窗口用例改为断言新语义——**Client 通告的窗口一律按原样兑现，既不放大也不当作无限**，`relayToClient` 把每次读取限制在当前可用 credit 内，因此小于整帧的窗口只会变慢而不会永久停摆；上行改为每流泵后，短写产生的 `RESET` 是异步的，该用例改为在会话仍存活时观测。

本地全量门禁（合并前执行）：

- `go build ./...`、`go vet ./...` → 通过。
- `go test ./... -count=1 -timeout 30m` → 退出码 0，29 个包全部 ok。
- `go test -race ./internal/session ./internal/protocol ./internal/agent ./internal/client -count=1` → 退出码 0。
- `go test -race ./internal/server -count=1 -timeout 25m` → 退出码 0（467 s 量级）。
- `go test -race ./internal/relay ./internal/e2e -count=1 -timeout 25m` → 退出码 0，覆盖三端串联的端到端链路。
- 逐提交 `go build ./...`：8 个代码提交全部独立可构建，支持二分定位与单点 `git revert`。
- `git diff --check`、`gofmt -l internal/ cmd/` → 无输出。
- 稳定性：`go test ./internal/session ./internal/protocol ./internal/agent ./internal/client -count=1` 连续 6 轮全绿（此前 `TestAgentPendingBufferOverflowResetsStream` 因拨号立即完成而与每流泵竞争，已改为让拨号真正保持在待定状态，测试语义与名称一致）。
- Web 未变更，`npm test -- --run` / `npm run build` 与 embed 校验不在影响面内（`build-test` 仍会在 CI 执行）。

## 发布步骤

1. 合并后先在一台 Server 灰度：`tunnelmesh-server --config ... check-config` 通过即可确认现有配置未踩收紧后的不变量。
2. 再灰度一个 Agent、一个 Client，观察 `agent_stream_send_reset`、`client_stream_queue_full_reset` 是否只出现在真实滥用/故障流上。
3. 三端可混版滚动升级，无协议协商变化；无需迁移、无需重启顺序约束。
4. 容量核对：每流新增队列上限为夹紧后的缓冲窗口（≤ 512 KiB，Agent 侧 256 KiB），按"并发流 × 窗口"估算，仍受 `max_concurrent_dials` 与 `openLimit` 约束。

## 回滚步骤

- `git revert` 合并提交即可，无 Schema、无数据、无协议变化，5 分钟内可完成。
- 回滚后 `server.stream.*` 与 `*.inbound_buffer_bytes` 重新变为"可写但无效"，收紧的校验同时消失，因此不会阻塞回滚。
- 代价是恢复上行/下行头阻塞、早到数据死锁与截断无日志三个故障模式，回滚仅用于止损。

## Reviewer 关注点

- **内存上界**：每流队列是否真按"≥ 用于建缓冲的窗口"取尺寸（小于窗口会把普通背压变成截断，大于窗口只是浪费）；`NegotiateReceiveWindow` 的夹紧区间 `[160 KiB, 512 KiB]` 与配置校验下界 `65536` 是否一致可接受。
- **窗口语义**：Client 通告窗口按原样兑现 + 按可用 credit 限制读取，与 Agent relay 侧的 `windowFor`（本地缓冲与请求取小）方向不同，请确认这两处不构成矛盾——后者限制的是 Server 自己愿意缓冲的量。
- **退出路径**：`retireClientStream` / `closeStream` / `removeAndClose` 是否在所有分支都关闭每流队列，否则泵 goroutine 会泄漏；`CloseAfterDrain` 与 `Close` 的选用是否符合"会话结束丢字节、半关闭保顺序"。
- **PONG 与准入**：Agent 侧接收循环不再写目标，确认心跳与 `OPEN_STREAM` 在慢目标下仍可推进（`TestServeClientSessionAnswersPingDuringStalledUpload`、`TestAgentInboundWindowAbuseResetsOnlyOffendingStream`）。
- **技术债（本次未处理，已确认存在）**：`internal/agent/session.go` 在 ctx 取消时丢弃 `readBack` 已读缓冲且不发 `RESET`/`HALF_CLOSE`；`waitForSendWindow`、`waitForClientWindow` 在流已被摘除时返回 `nil` 让调用方继续发帧；`internal/relay/transport.go` 的 `grpcStreamConn.Close()` 即 `CloseSend()`，无法唤醒阻塞中的 `RecvMsg`；`internal/server/http_proxy.go` 的 `go io.Copy(stream, clientConn)` 无半关闭；`tcpConnAdapter`、`streamNetConn` 是未被使用的 deadline 空实现适配器；会话退出时 `writer.Drain()` 可能停在已停止读取的对端上。

## 集成状态

- 分支：`codex/mysql56-compose-profile`，按任务边界拆为 8 个代码提交 + 1 个文档提交：
  `fix(session): recycle idle stream send queues`、`fix(session): drain queued frames before close`、
  `fix(protocol): keep window credit refillable`、`fix(agent): queue inbound data per stream`、
  `fix(client): isolate uploads and bound inbound state`、`fix(config): enforce stream window invariants`、
  `fix(server): report truncated managed responses`、`fix(server): pump client uploads off the frame loop`、
  `docs(dataplane): record window credit invariants`。
- PR body 是本文件的副本，链接见下。
- 关联记录：[实施计划](../superpowers/plans/2026-09-24-dataplane-flow-control-hardening.md)、[ADR 0002](../architecture/adr/0002-dataplane-window-credit-invariants.md)。
