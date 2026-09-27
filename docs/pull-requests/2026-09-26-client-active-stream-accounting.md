# Client 活跃流计数与租约排序

## 标题

fix(server): stop the Client active-stream gauge from leaking and stop showing stale counts as live

## 目标分支

`codex/vpn-phase6-server-data-plane`（最终合入 `main`）

## 摘要

运维反馈“客户端运行观测页的活跃流把已过期的也算进去了”。核对后分两个事实：

1. 列表聚合（实例行的 `activeStreams`、页头 `summary.activeStreams`）本来就只累加 `expires_at > now` 的租约，过期行不参与统计，SQL 与 Go 两侧都成立。
2. 真正的原因是租约里的 `active_streams` 数值本身是错的：它来自 Server 内存计数 `ClientSessionManager.record.ActiveStreams`，而该计数在一条流被 `relayToClient` 异常退役时只加不减，于是同一条长连接上每失败一次就永久 +1，直到 WebSocket 断开。HTTP/代理流量的正常结束走的正是这条路径，所以页面表现为“在线数正常、活跃流一直涨”，看上去像把过期连接也算进来了。

顺带修掉两个同源的记账缺陷：退役路径不归还槽位（泄漏），`closeStream` 对未知 stream id 仍会做一次递减（从同连接其它存活流身上“偷”计数），以及计数发生在流已经对退役 goroutine 可见之后（可能永远还不回去）。

排序部分（`last_seen_at DESC, id DESC` + 复合游标）在 `564b183` 已交付，本次补上连接租约子列表：`ListByInstances` 改为 `ORDER BY client_instance_id, updated_at DESC, connection_id`，抽屉里一个客户端的多条物理连接同样按最近心跳倒序。

## 用户影响

- “客户端运行观测”页的活跃流不再随时间单调虚高；一条流结束（正常 EOF 后的半关闭、上游 reset、写失败、窗口耗尽、队列满）都会立即归还槽位。
- 抽屉的连接表中，租约已过期的行，“活跃流”显示 `—` 而不再显示最后一次上报的数字。历史值仍可通过 API 字段读到。
- 抽屉连接表按最近心跳倒序，和列表页口径一致。
- 数字仍是心跳周期（默认 30 秒）粒度的快照，不是实时读数；跨节点连接由所属 Server 节点上报。

## API/Schema/配置影响

- 无 Schema 变更，无配置新增，无错误码变更。
- `GET /api/v1/clients/{id}/connections` 响应结构不变，仅数组顺序变为最近心跳优先；openapi 已在 `564b183` 说明排序，本次补充租约子列表排序说明。

## 安全与授权影响

- 无授权面变化。修复只让一个已有的观测计数变得准确；不涉及凭据、日志与敏感字段。

## 测试证据

红灯（修复前，三条全部失败）：

```
--- FAIL: TestServeClientSessionReleasesStreamCountWhenAgentReadFails (3.01s)
    ws_client_observability_test.go:145: active stream count = 1, want 0
--- FAIL: TestServeClientSessionStreamCountIsExactForMixedClosePaths (3.00s)
    ws_client_observability_test.go:164: active stream count = 2, want 1
--- FAIL: TestServeClientSessionStreamCountSurvivesRepeatedReleases (0.30s)
    ws_client_observability_test.go:198: active stream count = 1, want 0
```

```
--- FAIL: TestSQLiteRepositoryContract (0.00s)
    sqlite_test.go:18: lease order = "contract-lease-mid,contract-lease-new,contract-lease-old", want newest heartbeat first
```

绿灯与门禁（见提交信息中的验证记录）：`go test ./internal/server`、`go test ./internal/storage`、`go test ./... -count=1`、`go test -race ./internal/server`、`cd web && npm test -- --run && npm run build`、`./scripts/verify-web-embed.sh`。

## 发布步骤

常规发布，无迁移、无配置动作。发布后观察：一条稳定的浏览器/代理流量下，客户端观测页的活跃流应在心跳周期内回落到与并发相符的量级，而不再单调增长。

## 回滚步骤

`git revert` 本次两个提交即可，无数据影响：租约行里的 `active_streams` 只是历史数值，回滚后重新按旧行为累积。

## Reviewer 关注点

- `clientRelayStream.counted` 的归属：谁置位、谁清零、清零与 `release()` 的先后。计数只在持有槽位时归还，因此双路径关闭是同一条流的幂等操作。
- `StreamOpened` 在持有会话互斥锁时调用 `ClientSessionManager`：两者是不同锁且 manager 不会回调会话路径，注释已说明，请确认没有其它 manager→session 的调用边。
- 抽屉里“过期行显示 `—`”属于展示口径，若更希望保留最后已知值，只需要回退 `web/src/views/Clients.vue` 一列。

## 集成状态

未合并。分支 `codex/vpn-phase6-server-data-plane`，等待与 VPN 阶段 6~8 一并评审。
