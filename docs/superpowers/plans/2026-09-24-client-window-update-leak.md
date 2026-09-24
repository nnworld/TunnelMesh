# Client WINDOW_UPDATE 泄漏修复实施计划

状态：补记计划（原始实现已完成）

补记说明：用户在本机 Client 更新后仍能复现大体积 JavaScript 资源 `ERR_CONTENT_LENGTH_MISMATCH`。按 `AGENTS.md` 紧急修复条款先止损、后补记；红灯证据为实现过程中的真实观察，绿灯与门禁在补记时重新执行，见第 7 节。

## 1. 目标

Client 发送给 Server 的 `WINDOW_UPDATE` 只影响 Server→Client 发送窗口，不得泄漏到 Agent→Server 链路，避免 Agent 被重复授予额度后冲破 Server 接收缓冲。

## 2. 非目标

- 不新增协议 frame、不修改窗口大小、阈值或队列容量。
- 不改变 Agent 与 Server 之间已有的带外 `WINDOW_UPDATE` 语义。
- 不调整授权、策略、API、Schema 或配置。

## 3. 根因与证据

本地 TCP 转发链路存在两跳独立流控：

1. Agent→Server：Server 在 `OPEN_STREAM.Window` 通告额度，并在 `agentRelayStream.Read` 消费字节后向 Agent 回补。
2. Server→Client：Client 在 `OPEN_STREAM.Window` 通告额度，并在应用消费字节后向 Server 回补。

`serveClientSessionWithService` 处理 Client `WINDOW_UPDATE` 时，先把额度加入 `clientRelayStream.sendState`，随后又把同一帧通过 `agentRelayStream.WriteControl` 发给 Agent。Agent 将其误认为 Agent→Server 方向的新额度，于是可在 Server 尚未真正消费对应字节时继续发送。Server 的 `agentRelayStream.enqueue` 最终命中 `relay.ErrBackpressure`，删除流并发送 `RESET`；Client 只收到短于 `Content-Length` 的响应。

红灯证据：

1. `TestServeClientSessionRelayHonorsClientWindow` 修复前失败：`Client WINDOW_UPDATE leaked to Agent relay: {Type:5 StreamID:16 Window:8}`。
2. `TestSOCKS5WebPageLatency/local_tcp_forward_delivers_large_fixed-length_response` 修复前失败：响应体不完整，并出现 `protocol: stream is reset`。
3. 临时诊断日志定位到 Server 侧先出现 `relay: backpressure`，随后 Client 收到 `RESET`。

## 4. 架构决策

两级流控额度都在 Server 终止：

- Client `WINDOW_UPDATE` 只更新 `clientRelayStream.sendState`，供 `relayToClient` 继续向 Client 发送。
- Agent `WINDOW_UPDATE` 仍由 `agentRelayStream.Read` 消费字节后产生，供 Agent 继续向 Server 发送。
- Server 不再把 Client 额度镜像给 Agent，也不把 Agent 额度误用于 Client 方向。

## 5. 技术与规格引用

- `internal/protocol/window.go` 的 `queued <= initialWindow` 不变量。
- `docs/protocol/proxy-modules.md` 的 per-stream 队列与带外 `WINDOW_UPDATE` 说明。
- 相关 Client 修复：`docs/superpowers/plans/2026-09-24-client-receive-window-credit.md`。

## 6. 文件清单

- `internal/server/ws_client.go`：删除 Client `WINDOW_UPDATE` 到 Agent relay 的镜像转发。
- `internal/server/ws_client_test.go`：改写契约，断言 Client 更新不会泄漏到 Agent，同时后续 DATA 仍能发给 Client。
- `internal/e2e/socks5_web_page_latency_test.go`：新增本地 TCP 转发大体积固定长度响应的端到端回归。
- `internal/client/forward.go`：记录 TCP bridge 的非 EOF 错误，避免短包再次静默。
- `docs/protocol/proxy-modules.md`：记录两级流控额度终止于 Server 的不变量。
- 本文件与 `docs/pull-requests/2026-09-24-client-window-update-leak.md`。

## 7. TDD 与验证

红灯见第 3 节。最小实现为删除 `serveClientSessionWithService` 中的 `controlWriter.WriteControl(frame)` 分支。

验证命令：

```bash
go test ./internal/server -run '^TestServeClientSessionRelayHonorsClientWindow$' -count=1
go test ./internal/e2e -run '^TestSOCKS5WebPageLatency$/^local_tcp_forward_delivers_large_fixed-length_response$' -count=10
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

补记时前两项已通过，其中端到端用例连续执行 10 次均通过；全量门禁结果记录在 PR 记录中。

## 8. 回滚注意事项

回退 Server 提交并重新部署旧 Server 即可恢复原行为；数据库、配置和协议格式无需回滚。回滚后本地 Client 大响应会重新出现偶发 `ERR_CONTENT_LENGTH_MISMATCH`。
