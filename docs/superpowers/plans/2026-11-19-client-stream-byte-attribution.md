# Client 字节归因与诊断指标实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让一台生产 Client 能按“隧道 / 流 / 线路”回答“这 6 MB 是从哪条隧道出去的、有多少是被丢弃的帧、线路字节与流字节差多少”，从而定位 `wl.yawen.fun` 上 443 端口出向 6.36 MB 而本地隧道只解释 0.9 MB 的差额来源。

**Architecture:** 观测点分三层，互不依赖：线路层在 WebSocket 收发处统计整帧字节；流层在 `Session.sendFrame` / 接收循环按 `tunnel` 标签统计 DATA 载荷；丢弃层在接收循环中统计“找不到 stream/datagram”“stream 已关闭”“队列/窗口耗尽”而没有被交付的字节。三层相减即可把差额归类为协议开销、控制面、丢弃或未归属。指标通过 Client 进程内新增的 loopback-only `/metrics` 端点暴露，默认关闭，不影响现有 `client run` 行为。

**Tech Stack:** Go 标准库（`net/http`、`sync`）、现有 `internal/observability` Prometheus 注册表、`internal/client` 的 `Runtime`/`Session`/`WebSocketTransport`、`internal/config`、`promhttp`。不新增第三方依赖，不改协议帧、Schema 和前端。

**Spec:** 无新增协议与 Schema。关联证据与结论：[Client 带宽诊断](#背景现场证据)、[可观测性运维文档](../../operations/observability.md)、[配置说明](../../operations/configuration.md)。AGENTS.md 约束：核心逻辑必须有单元测试；指标标签必须有界；外部输入在边界校验；未获授权不执行 commit/push/merge。

## 背景：现场证据（2026-11-19，wl.yawen.fun）

- 代码基线 `main@9f5c2fd`；线上 Client 已替换为该提交构建的二进制（`dev commit=9f5c2fdc11c10076b49f2755a3cb8f525ca7e38c`），`internal/client`、`internal/session`、`internal/protocol`、`internal/transport` 与旧版无差异，因此下列代码路径对现场成立。
- 60 秒全协议抓包：443 出 6.36 MB / 入 1.28 MB；6021/6022/6025 各约 130 KB 但来自 `45.148.10.195` 每分钟 60+ 次短连接；21116 仅约 2 KB。
- 同一窗口内本地隧道入向合计约 0.9 MB，与 443 的 6.36 MB 相差 6–7 倍；`sent ≈ acked`、TCP 重传为 0，排除传输层重传放大。
- 现有 `Session.sendFrame`（`internal/client/session.go:167`）与接收循环（`internal/client/session.go:660`）已经调用 `ObserveBytes`，但只按 `protocol` 聚合，且 Client 进程没有 `/metrics` 端点，生产上读不到。
- 接收循环在 `FrameData` 找不到 `stream`/`datagram` 时直接 `continue`（`internal/client/session.go:648-651`），这些字节被 TCP 确认但既不计数也不交付，是当前最大的未知项；`frameStream.push`（`internal/client/session.go:976`）在 `done` 已关闭、窗口耗尽或队列满时同样静默丢弃。

## Global Constraints

- 不改协议帧格式、能力协商、数据库 Schema 和现有配置默认语义；`client.metrics.listen` 默认空字符串表示完全关闭，不产生监听端口。
- 指标标签必须有界：`tunnel` 只来自本地配置的隧道名，经 `observability.label` 归一化并截断到 64 字节；禁止把 `stream_id`、对端地址或任意远端字符串作为标签。
- `/metrics` 只允许绑定回环地址；非回环监听在配置校验阶段报错，错误信息包含字段名 `client.metrics.listen`。
- 不得在指标、日志或错误中输出 Token、Authorization、会话内容或明文载荷。
- 现有 `ObserveBytes("client", ...)` 聚合序列必须继续存在，避免破坏已有仪表盘。
- 所有新增/修改逻辑先写失败测试并观察红灯，再做最小实现；并发路径由 `-race` 覆盖。
- 未获用户明确授权，不执行 commit、push、merge 或创建远端 PR。

## 精确文件清单

| 文件 | 变更 |
| --- | --- |
| `internal/observability/metrics.go` | 新增三类 CounterVec 与 `ObserveTunnelBytes`、`ObserveWireBytes`、`ObserveDroppedBytes` 方法，并在 `NewMetrics` 中注册。 |
| `internal/observability/metrics_test.go` | 断言三类指标的标签、累加与 nil 安全性。 |
| `internal/client/session.go` | `StreamRequest` 新增 `Tunnel string`；`frameStream`/`frameDatagramStream` 记录隧道名；`sendFrame` 与接收循环按隧道计数；`push` 返回丢弃原因并计数。 |
| `internal/client/session_test.go` | 覆盖按隧道归属、未知流丢弃、关闭流丢弃、窗口耗尽丢弃。 |
| `internal/client/forward.go` | `TCPForwardConfig`/`HTTPForwardConfig`/`UDPForwardConfig` 新增 `TunnelName string`，在 `OpenStream` 请求中透传；`NewConfiguredForward` 填充隧道名。 |
| `internal/client/forward_test.go` | 断言各类 forward 打开流时携带正确的 `Tunnel`。 |
| `internal/client/websocket.go` | 在 `clientWebSocketTransport.Send`/`Receive` 统计线路字节；`WebSocketOptions` 复用 `Metrics`。 |
| `internal/client/websocket_test.go` | 用 fake transport 断言收发线路字节计数。 |
| `internal/client/runtime.go` | `Runtime.Start` 在 `client.metrics.listen` 非空时启动 `/metrics` HTTP 服务；`Stop`/`fail` 关闭；监听失败返回启动错误。 |
| `internal/client/runtime_test.go` | 覆盖默认不监听、回环监听可抓取、端口占用时启动失败、Stop 后端口释放。 |
| `internal/config/config.go` | 新增 `ClientMetricsConfig{Listen string}`、默认值 `client.metrics.listen: ""`、校验回环地址。 |
| `internal/config/config_test.go` | 覆盖默认关闭、非回环拒绝、错误信息字段名。 |
| `internal/cli/client_run.go` | 构造 `RuntimeOptions.Metrics`，仅当监听开启时创建注册表。 |
| `internal/cli/client_runtime_test.go` | 断言 `client run` 在开启监听时暴露指标、默认不额外监听。 |
| `docs/operations/observability.md` | 记录三类指标语义、抓取方式与“如何用指标定位差额”。 |
| `docs/operations/configuration.md` | 记录 `client.metrics.listen` 的安全约束与示例。 |
| `docs/superpowers/plans/README.md` | 由 `scripts/gen_doc_index.py` 重新生成，禁止手工编辑。 |

## 任务间接口

- `observability.Metrics` 新增方法签名：
  - `ObserveTunnelBytes(component, tunnel, direction, protocol string, n int64)`
  - `ObserveWireBytes(component, transport, direction string, n int64)`
  - `ObserveDroppedBytes(component, reason string, n int64)`
- `client.StreamRequest` 新增字段 `Tunnel string`；空值在指标中归一化为 `unattributed`。
- `frameStream.push(p []byte) (dropReason string)`：空字符串表示已入队；调用方负责把非空原因交给 `ObserveDroppedBytes`。
- `client.RuntimeOptions` 不新增字段；监听地址由 `config.Client.Metrics.Listen` 经 `r.cfg` 读取，保证 CLI 与托盘同一套行为。托盘当前不设置该配置，因此默认不监听。

## Task 1: 新增三类 Prometheus 指标

**Files:** `internal/observability/metrics.go`、`internal/observability/metrics_test.go`

- [ ] 先写 `TestMetricsTunnelBytesWireBytesAndDrops`：注册到独立 registry，调用三个新方法后 `Gather`，断言 `tunnelmesh_client_stream_bytes_total` 的 `tunnel/direction/protocol` 标签与值、`tunnelmesh_client_wire_bytes_total` 的 `transport/direction`、`tunnelmesh_client_dropped_bytes_total` 的 `reason`，以及空 tunnel 归一化为 `unattributed`、负值按 0 处理。
- [ ] 运行 `go test ./internal/observability -run TestMetricsTunnelBytesWireBytesAndDrops -count=1`，预期失败：未定义方法/采集器。
- [ ] 最小实现：
  - `streamBytesTotal`：`tunnelmesh_client_stream_bytes_total{component,tunnel,direction,protocol}`。
  - `wireBytesTotal`：`tunnelmesh_client_wire_bytes_total{component,transport,direction}`。
  - `droppedBytesTotal`：`tunnelmesh_client_dropped_bytes_total{component,reason}`。
  - 三个方法均使用 `label()`/`NormalizeProtocol()`，`n <= 0` 直接返回，nil 接收者安全。
  - 在 `NewMetrics` 的 `MustRegister` 列表中注册。
- [ ] 再运行同一条测试，预期通过；跑 `go test ./internal/observability -count=1`。

## Task 2: 流层按隧道归因

**Files:** `internal/client/session.go`、`internal/client/session_test.go`

- [ ] 先写测试 `TestSessionAttributesDataBytesToTunnel`：构造带 `Metrics` 的 `Session` 与内存 transport，打开 `Tunnel: "edge-https"` 的流，发送 DATA 后断言 `tunnelmesh_client_stream_bytes_total{tunnel="edge-https",direction="outbound"}` 增加；注入 DATA 帧后断言 `inbound` 增加。
- [ ] 运行该测试，预期失败：`StreamRequest` 无 `Tunnel` 字段、指标缺失。
- [ ] 最小实现：
  - `StreamRequest` 增加 `Tunnel string`；`newFrameStream`/`newFrameDatagramStream` 接收并保存隧道名（`frameStream.tunnel`、`frameDatagramStream.tunnel`）。
  - `OpenStream`/`OpenStreamResult` 创建流时传入 `req.Tunnel`。
  - `sendFrame` 与接收循环在保留现有 `ObserveBytes` 的同时调用 `ObserveTunnelBytes`，隧道名取 `stream.tunnel` / `datagram.tunnel`。
- [ ] 再运行测试，预期通过；跑 `go test ./internal/client -run TestSessionAttributesDataBytesToTunnel -count=1`。

## Task 3: 丢弃字节可归因

**Files:** `internal/client/session.go`、`internal/client/session_test.go`

- [ ] 先写测试 `TestSessionCountsDroppedDataBytes`，三个子用例：
  - 注入 `StreamID` 不存在的 DATA → `tunnelmesh_client_dropped_bytes_total{reason="unknown_stream"}` 等于 payload 长度。
  - 关闭流后再注入 DATA → `reason="closed_stream"`。
  - 用零窗口/满队列触发 `push` 失败 → `reason="window_or_queue"`，且仍发送 RESET。
- [ ] 运行测试，预期失败：`push` 无返回值、无丢弃计数。
- [ ] 最小实现：
  - `push` 改为 `(dropReason string)`：`done` 已关闭返回 `closed_stream`；流控 `Handle` 失败返回 `window_or_queue`；`TryPush` 失败返回 `window_or_queue`；成功返回 `""`。
  - 接收循环在 `stream == nil && datagram == nil` 时调用 `ObserveDroppedBytes("client", "unknown_stream", len(f.Payload))` 后 `continue`；`push` 返回非空原因时计数。
  - 保持现有 `finish`/`removeStream`/RESET 行为不变。
- [ ] 再运行测试，预期通过；跑 `go test ./internal/client -run TestSessionCountsDroppedDataBytes -count=1`。

## Task 4: forward 透传隧道名

**Files:** `internal/client/forward.go`、`internal/client/forward_test.go`、`internal/client/runtime.go`

- [ ] 先写测试 `TestConfiguredForwardPropagatesTunnelName`：对 TCP/HTTP/UDP 三种隧道，用记录型 opener 断言 `StreamRequest.Tunnel` 等于配置中的 `name`；`name` 为空时回退为 `listen>target` 形式（与 `TunnelDisplayName` 一致）。
- [ ] 运行测试，预期失败：配置结构无 `TunnelName`、请求无 `Tunnel`。
- [ ] 最小实现：
  - 三个 ForwardConfig 增加 `TunnelName string`；各 `handleConn`/HTTP/UDP 打开流时填 `Tunnel: cfg.TunnelName`。
  - `NewConfiguredForward` 用 `TunnelDisplayName(tunnel)` 填充 `TunnelName`。
- [ ] 再运行测试，预期通过；跑 `go test ./internal/client -run TestConfiguredForwardPropagatesTunnelName -count=1`。

## Task 5: 线路层字节统计

**Files:** `internal/client/websocket.go`、`internal/client/websocket_test.go`

- [ ] 先写 `TestWebSocketTransportCountsWireBytes`：fake `FrameTransport`/`ReceiveTransport`，发送两个帧后断言 `wire outbound` 等于编码后消息字节数（帧头 + 载荷），接收一帧后 `wire inbound` 等于消息字节数。
- [ ] 运行测试，预期失败：无线路计数。
- [ ] 最小实现：
  - `clientWebSocketTransport.Send` 在成功写入后调用 `ObserveWireBytes("client", "websocket", "outbound", len(msg))`。
  - `Receive` 在成功读到消息后调用 `ObserveWireBytes("client", "websocket", "inbound", len(msg))`。
  - 失败路径不计数，避免把重试流量重复计入。
- [ ] 再运行测试，预期通过；跑 `go test ./internal/client -run TestWebSocketTransportCountsWireBytes -count=1`。

## Task 6: Client 指标端点与配置

**Files:** `internal/config/config.go`、`internal/config/config_test.go`、`internal/client/runtime.go`、`internal/client/runtime_test.go`、`internal/cli/client_run.go`、`internal/cli/client_runtime_test.go`

- [ ] 先写配置测试：默认 `client.metrics.listen == ""`；设置为 `0.0.0.0:9101` 时 `Validate` 返回含 `client.metrics.listen` 的错误；`127.0.0.1:9101` 通过；`TUNNELMESH_CLIENT_METRICS_LISTEN` 能覆盖默认值。
- [ ] 先写 runtime 测试：监听地址为空时 `Start` 不新增监听；设置为 `127.0.0.1:0` 时 `GET /metrics` 返回 200 且包含 `tunnelmesh_client_stream_bytes_total`；端口被占用时 `Start` 返回错误；`Stop` 后端口释放。
- [ ] 运行上述测试，预期失败：配置键不存在、行为未实现。
- [ ] 最小实现：
  - `ClientMetricsConfig` 加入 `ClientConfig`，默认值映射新增 `client.metrics.listen: ""`；`Validate` 复用 `requireLoopbackListen` 语义（仅回环、允许 `:0`）。
  - `Runtime.Start` 在监听非空时于隧道监听之前启动 `http.Server`，仅注册 `/metrics`（`promhttp.HandlerFor` 独立 registry）；监听失败立即使 `Start` 失败并释放已获资源；`Stop`/`fail` 关闭并等待退出。
  - `internal/cli/client_run.go`：`client.metrics.listen` 非空时创建 `prometheus.NewRegistry()` 与 `observability.NewMetrics()` 并传入 `RuntimeOptions.Metrics`；为空时保持 `nil`，不分配指标。
- [ ] 再运行测试，预期通过；跑 `go test ./internal/client ./internal/config ./internal/cli -count=1`。

## Task 7: 文档与索引

**Files:** `docs/operations/observability.md`、`docs/operations/configuration.md`

- [ ] `observability.md` 增加“Client 字节归因”小节：三类指标语义、建议抓取周期、判读公式：
  - `wire_out - stream_out - control ≈ 协议/TLS 开销`
  - `wire_in - stream_in > 0` 时优先看 `dropped_bytes{reason="unknown_stream"}`。
- [ ] `configuration.md` 增加 `client.metrics.listen`：默认关闭、仅回环、示例 `127.0.0.1:9101`、systemd 部署下的 `curl` 验证命令。
- [ ] 运行 `python3 scripts/gen_doc_index.py`，确认只改动生成索引。

## 验证命令

```bash
go test ./internal/observability ./internal/client ./internal/config ./internal/cli -count=1
go test -race ./internal/observability ./internal/client
go vet ./...
git diff --check
python3 scripts/gen_doc_index.py && git diff --stat docs/superpowers/plans/README.md
```

## 现场验证步骤（实现合并后）

1. 在 `/etc/tunnelmesh/client.yaml` 增加 `client.metrics.listen: 127.0.0.1:9101`，`systemctl restart tunnelmesh-client`。
2. 连续抓取 3 个 60 秒窗口：`curl -s 127.0.0.1:9101/metrics | grep -E 'stream_bytes|wire_bytes|dropped_bytes'`，同时用 `tcpdump` 统计 443 字节。
3. 判读：若 `dropped_bytes{reason="unknown_stream"}` 与差额同量级，则根因是服务端向已关闭流继续发送；若 `wire_out >> stream_out` 且丢弃为 0，则检查控制面/重连风暴；若 `stream_out` 已接近线路值，则差额来自本地抓包统计口径。
4. 与服务端 `agent_runtime_stats.bytes_in/bytes_out` 对照，确认对应 Agent 与隧道。

## 回滚注意事项

- 代码回滚：新增指标与端点均为附加能力，回滚到上一版本即可；`client.metrics.listen` 在旧版本中是未知键，部署前需同时移除该配置行，避免严格配置校验拒绝启动。
- 现场回滚：删除 `client.metrics.listen` 后重启 Client；二进制回滚使用安装时保留的 `/usr/local/bin/tunnelmesh-client.bak-<timestamp>`。
- 指标端点仅回环监听，不新增公网攻击面；若误配非回环地址，配置校验会阻止启动而不是静默暴露。

## 补记：实施中发现并修复元数据放大（2026-11-19）

计划三任务（隧道归因、丢弃归因、线路计数）落地后，现场 60 秒实测显示出向仍有 1.41 MB，其中 `client_hello` 175 帧/60 s（约 514 KB）与 `client_metadata_update` 205 帧/60 s（约 602 KB）占 78%。补充帧类型指标后定位到 `internal/client/session.go` 的 `Session.Start()`：它被每次 `OpenStreamConn` 调用，却在 `recvOnce` 之外无条件发送 `CLIENT_HELLO` 并为每个调用启动一个 30 秒元数据定时器，导致“每条代理连接 2.9 KB + 每连接一个永久定时器”。

修复：新增 `startOnce sync.Once` 包裹元数据握手、liveness 与接收循环启动；`TestSessionStartIsIdempotent` 先红后绿。修复后同一负载下 60 秒出向从 1,410,608 B 降至 369,406 B（-73.8%），`client_hello` 不再按流出现，`client_metadata_update` 回到 13 会话 × 2 次/分钟的预期值。

验证记录：`go test ./... -count=1`、`go vet ./...`、`go test -race ./internal/client ./internal/observability ./internal/config` 全部通过；`go test -race ./...` 在 `internal/server` 触发既有 10 分钟超时（`panic: test timed out after 10m0s`，非 DATA RACE，与本次改动无关）。
