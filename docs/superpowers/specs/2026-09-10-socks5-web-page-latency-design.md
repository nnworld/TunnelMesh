# SOCKS5 网页首屏延迟优化设计

## 状态

2026-09-10 已确认设计方向。本文覆盖 P0、P1、P2 的完整优化，不改变 TunnelMesh 公网只开放 HTTP、HTTPS 和 WebSocket 的约束，也不新增公网 SOCKS5 监听。

## 背景

浏览器通过 `tunnelmesh-client forward socks5` 打开网页时，一次页面加载通常会并发创建多条 TCP 连接。当前每条连接依次经过本地 SOCKS5 握手、可选远程校验、Client WebSocket、Server 授权、连接选择、可选跨节点 relay、Agent DNS/TCP Dial，最后才到目标服务。

当前实现存在以下首屏延迟放大点：

- Agent 在单一 WebSocket 读循环内同步执行 DNS 和 TCP Dial，一个慢目标会阻塞同一连接上的后续 `OPEN_STREAM`。
- Agent 的单流拨号、目标写入等错误会从 dispatcher 冒泡到 Session，可能关闭整条 WebSocket。
- Client 发出 `OPEN_STREAM` 后立即把 SOCKS5 success 返回给浏览器，没有等待 Agent 完成目标连接。
- Server 对每条浏览器 TCP 连接重新查询 Token、Agent 和 Policy；MySQL 集群下数据库 RTT 会被页面并发连接放大。
- 可选 `auth_url` 对每条连接同步调用一次，没有结果缓存和同键并发合并。
- Client、Server、Agent 的中央 reader 可能被某条慢流的队列或 socket write 阻塞。
- 协议已定义 `WINDOW_UPDATE` 状态模型，但生产数据面没有真正执行逐流窗口控制和公平调度。
- Agent 连接池已有基础实现，但默认保持一条连接，缺少与首屏延迟相关的队列、拨号和 TTFB 信号。

## 目标

- Agent 并行处理不同 Stream 的 DNS 和 TCP Dial，同时保持有界并发和内存。
- 单个 Stream 的拨号、读写、超时、拒绝和背压错误只终止该 Stream。
- 新协议链路只有在 Agent 目标连接成功后才向 SOCKS5 客户端返回 success。
- 为旧 Client、Server 和 Agent 保留滚动升级兼容性。
- 本地 SQLite 使用短授权缓存；集群 MySQL 使用较长授权缓存并通过共享修订号快速失效。
- 可选远程校验支持短缓存、同键并发合并和严格的敏感数据边界。
- 中央 reader 不因一个慢 Stream 阻塞；各 Stream 有独立有界队列。
- 实际启用逐流窗口、控制帧优先和数据帧公平调度。
- 连接选择优先同 Server 节点，必要时再走 mTLS relay。
- 补齐能够解释首屏慢点的低基数指标、日志和排障文档。

## 非目标

- 不把单条 TCP Stream 拆分到多条 WebSocket。
- 不迁移已经建立的 Stream。
- 不实现 HTTP/2 或 HTTP/3 语义代理；SOCKS5 仍转发透明 TCP 字节流。
- 不实现 SOCKS5 `BIND` 或 `UDP ASSOCIATE`。
- 不在 Agent 中实现独立递归 DNS 缓存；DNS 缓存优先交给宿主机或容器基础设施。
- 不新增 Redis 依赖。授权一致性以现有 SQLite/MySQL 权威库和修订号为准。
- 不改变 Token、证书、目标地址和内网信息的现有脱敏约束。

## 总体方案

采用兼容式协议增强：旧链路继续使用现有乐观打开行为；只有 Client、Server 和 Agent 都协商到对应 capability 后，才启用严格打开确认、逐流窗口和新调度语义。

```text
Browser
  │ SOCKS5 CONNECT
  ▼
Client
  │ OPEN_STREAM
  ▼
Server ── authorization cache/revision ── MySQL or SQLite
  │ selected local session or mTLS relay
  ▼
Agent bounded dial executor
  │ DNS + TCP Dial
  ├── OPEN_OK  ──► Client returns SOCKS5 success
  └── OPEN_ERR ──► Client returns mapped SOCKS5 failure
```

数据传输阶段采用三层隔离：

```text
one WebSocket reader
  → non-blocking frame demux
    → bounded per-stream inbound queue
      → per-stream target/local writer

per-stream outbound queues
  → fair connection writer
    → one serialized WebSocket writer
```

## 协议与兼容性

### Capability 名称

新增以下稳定能力名称：

```text
stream_open_result.v1
stream_flow_control.v1
stream_fair_writer.v1
```

`stream_fair_writer.v1` 只描述本端调度能力，不改变 Frame 格式；另外两项改变交互语义，必须完成双端协商。

### WebSocket 连接协商

Client 在 WebSocket Upgrade 的 `Sec-WebSocket-Protocol` 中按优先级提供组合协议：

```text
tunnelmesh.v1.open-result.flow-control
tunnelmesh.v1.open-result
tunnelmesh.v1
```

Server 选择一个完整组合。未选择或旧 Server 不返回子协议时，Client 将连接标记为 legacy，不等待打开结果，也不发送窗口受控 Frame。禁止通过短超时猜测 Server 能力，因为猜测会把网络抖动错误解释为旧协议。

Agent 的能力通过 `AgentMetadataPayload.capabilities` 上报，Server 在 `AgentMetadataAckPayload.capabilities` 返回交集。旧 JSON 解码器忽略新增字段，因此旧 Agent/Server 仍可建立连接。

Server 只有在以下条件全部满足时对某个 Client Stream 启用严格模式：

- Client WebSocket 子协议包含 `open-result`。
- 选中的 Agent Session 声明 `stream_open_result.v1`。
- 如果经过 relay，目标 Server 和 relay 请求版本支持打开结果。

任一条件不满足时使用 legacy 行为，并记录一次低基数兼容性计数；不得为严格 Client 静默伪造 Agent 已连接成功。

### Frame 扩展

新增 Frame：

```text
OPEN_RESULT
```

`OPEN_RESULT` 使用原 `stream_id`，Payload 为有界 JSON：

```json
{
  "accepted": true,
  "stage": "connect",
  "code": "ok",
  "retryable": false,
  "retry_after_ms": 0
}
```

失败示例：

```json
{
  "accepted": false,
  "stage": "dns",
  "code": "host_unreachable",
  "retryable": true,
  "retry_after_ms": 1000
}
```

约束：

- Payload 最大 1 KiB。
- `message` 不进入线协议，避免泄露 DNS、系统调用、内网地址或策略细节。
- 稳定 `stage` 仅允许 `authorization`、`selection`、`relay`、`queue`、`dns`、`connect`、`policy`、`protocol`。
- 稳定 `code` 仅允许 `ok`、`forbidden`、`agent_offline`、`queue_full`、`timeout`、`network_unreachable`、`host_unreachable`、`connection_refused`、`unsupported_capability`、`internal_error`。
- 未知 code 在 Client 端映射成一般失败，不向浏览器返回内部错误文本。

### SOCKS5 错误映射

| OPEN_RESULT code | SOCKS5 reply |
| --- | --- |
| `ok` | succeeded |
| `forbidden` | connection not allowed |
| `network_unreachable` | network unreachable |
| `host_unreachable`、`agent_offline` | host unreachable |
| `connection_refused` | connection refused |
| `timeout`、`queue_full` | TTL expired/general failure |
| 其他 | general failure |

严格模式下，Client 在收到 `OPEN_RESULT accepted=true` 之前不发送 SOCKS5 success，也不接受该 Stream 的普通 DATA。打开超时只 Reset 当前 Stream。

legacy 模式维持现有行为，保证滚动升级，但指标和日志必须标注 `open_mode=legacy`，帮助运维确认升级完成度。

## P0：打开路径和错误隔离

### Agent 有界拨号执行器

Agent Session 创建一个 `DialExecutor`，职责只有排队、并发限制、拨号取消和结果分类。

默认配置：

```yaml
agent:
  streams:
    max_concurrent_dials: 32
    max_pending_dials: 128
    connect_timeout: 5s
    open_timeout: 8s
```

规则：

- WebSocket reader 解码并校验 `OPEN_STREAM` 后，只执行一次非阻塞入队。
- 队列已满时立即返回 `OPEN_RESULT(queue_full)`；legacy peer 收到当前 Stream 的 `RESET`。
- executor 最多同时运行 `max_concurrent_dials` 个任务。
- 每个任务使用独立 Context；Client Reset、Session 关闭或打开超时必须取消 DNS/Dial。
- 目标 Policy 检查属于任务的一部分，但不得在 WebSocket reader 中同步执行外部 I/O。
- 成功后先安装 Stream 状态，再发送 `OPEN_RESULT(ok)`，防止 ACK 到达后 DATA 找不到 Stream。
- 失败后只清理该 Stream，不返回会关闭 Session 的错误。

### Stream 错误分类

新增内部分类，不把原始系统错误发送到对端：

```go
type ErrorScope uint8

const (
    ErrorScopeStream ErrorScope = iota + 1
    ErrorScopeSession
)
```

Stream 级错误包括：

- DNS、TCP Dial、目标拒绝和目标超时。
- 单 Stream Policy 拒绝。
- Stream 队列满、窗口超限、重复 Stream ID。
- 目标 socket 读写失败和半关闭失败。

Session 级错误仅包括：

- WebSocket 读写失败。
- 认证失效、错误的连接身份或 fencing 失败。
- 无法恢复的 Frame 编码/版本错误。
- 超出连接级资源限制且无法归属于某个 Stream。

dispatcher 对 Stream 错误完成 Reset/OpenResult 后返回 `nil`；Session 只消费 Session 级错误。一个网页子资源不可达不能触发 Agent 重连。

### Server 异步打开

严格 Client 的 `OPEN_STREAM` 不允许阻塞 Client WebSocket reader：

- Server 为该 Stream 创建 `opening` 状态。
- 授权和 `NodeTransport.OpenStream` 在有界 open executor 中运行。
- Client 在 opening 状态发送 DATA 视为协议错误，只 Reset 当前 Stream。
- Agent 接受后，Server 先把 relay stream 安装为 `open`，再向 Client 发送 `OPEN_RESULT(ok)`。
- Server/Agent 失败结果使用稳定错误码向 Client 传播。

legacy Client 保留同步打开语义，避免在没有 ACK 的情况下引入无限 pre-open 数据缓冲。legacy 路径同样必须把目标错误限制在当前 Stream。

### 打开状态机

```text
idle
  → opening
    → open
    → reset
open
  → half_closed_local / half_closed_remote
  → closed
  → reset
```

`OPEN_RESULT` 只能在 `opening` 状态接收一次；重复、越序或错误 Stream ID 只 Reset 相关 Stream，除非构成无法归属的连接级协议攻击。

## P1：授权与远程校验缓存

### Server Stream 授权缓存

缓存位于 Service 层，包裹 `CredentialService.AuthorizeStream`；Handler 不直接访问缓存或 Repository。

缓存键使用规范化后的：

```text
token_id
agent_id
protocol
target_host
target_port
authorization_revision
```

`target_host` 按现有 Policy 语义规范化；域名转小写并去除尾部点，IP 使用标准文本格式。缓存不得包含原始 bearer secret。

默认 TTL：

| 模式 | 正向结果 | 拒绝结果 | revision poll |
| --- | ---: | ---: | ---: |
| SQLite local | 5s | 1s | 本进程即时失效，5s 安全复核 |
| MySQL cluster | 5m | 3s | 2s |

同一个 key 的并发 miss 使用 singleflight 合并。缓存设置最大条目数，默认 100,000；淘汰采用近似 LRU，任何缓存压力不得阻塞主授权链路。

### 共享授权修订号

Schema 版本从 8 升级到 9。`migrations/ddl.sql` 增加当前版本表，`migrations/incremental/v0008_to_v0009/mysql.sql` 与 `migrations/incremental/v0008_to_v0009/sqlite.sql` 提供从 v8 的原子升级路径：

```sql
CREATE TABLE IF NOT EXISTS authorization_revision (
    id INTEGER PRIMARY KEY,
    revision INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);
```

固定只使用 `id=1`。以下影响授权结果的写操作必须在与业务变更相同的数据库事务中原子递增 revision：

- service token 创建、轮换、Scope/过期时间更新和撤销。
- user 禁用、恢复、删除及所有者关系变更。
- Agent 创建、启停、所有者变更和删除。
- Agent Policy 创建、更新和删除。

Repository 提供小接口：

```go
type AuthorizationRevisionRepository interface {
    Current(context.Context) (uint64, error)
}
```

修订号递增属于各授权资源 Repository 写事务的内部职责，Service 不允许在业务提交后再单独 bump，否则崩溃窗口会产生永久陈旧缓存。

迁移要求：

- `SchemaVersion` 从 8 更新为 9，并在迁移注册表中加入 v8→v9。
- 增量脚本先校验/确保 `schema_meta.version=8`，创建并初始化 `authorization_revision`，成功后再推进版本到 9。
- 全量 DDL、增量 DDL、SQLite/MySQL contract test、迁移文档和 `migrations/embed.go` 必须同任务更新。
- 旧应用读取 v9 数据库时可以忽略新增表；回滚应用保留表，不执行反向 DDL。

每个 Server：

- 启动时必须成功读取 revision，否则 readiness 失败且不启用授权缓存。
- MySQL 模式每 2 秒读取一次 revision。
- revision 增大后原子切换 cache generation，旧 generation 异步释放。
- 本节点写操作提交成功后通过进程内通知立即失效，无需等待下一次 poll。
- revision 回退、缺失或溢出视为存储一致性错误，清空缓存并使 readiness 失败。
- poll 失败后最多允许使用距上次成功 poll 不超过 5 秒的正向缓存；超过后所有正向缓存 miss 并回源。数据库也不可用时授权失败关闭，不能长期使用 5 分钟旧权限。
- 拒绝缓存可按 TTL 继续使用，但恢复数据库后仍受 generation 约束。

因此 MySQL 的 5 分钟 TTL 用于减少稳定期查询，不代表撤销需要等待 5 分钟；正常情况下跨节点生效窗口约为 2 秒。

### 可选远程 auth_url 缓存

Client `RemoteValidator` 增加：

```yaml
client:
  remote_validation:
    positive_ttl: 15s
    negative_ttl: 2s
    timeout: 3s
    max_entries: 10000
```

规则：

- endpoint 为空时仍为零开销直通。
- 同键并发调用使用 singleflight。
- key 包含 protocol、agent、目标和本地入口认证身份摘要。
- 用户名、密码不以明文存入 key；使用进程启动时生成的随机 HMAC key 对认证字段计算摘要。
- 日志、指标和错误禁止输出请求正文、用户名、密码、目标全地址或响应正文。
- endpoint 或认证配置变化时清空缓存并关闭旧 Transport 的 idle connections。
- 网络错误和超时属于拒绝结果，但最多缓存 1 秒，避免瞬时故障持续放大。
- HTTP Transport 保持连接池；TLS endpoint 可复用 HTTP/2。

## P2：逐流隔离、流控与连接选择

### 中央 Reader 与逐流队列

Client、Server 和 Agent 统一遵守：

- 中央 reader 只做 Frame 校验、状态查询和有界入队。
- 中央 reader 不执行 DNS、数据库、relay Dial、目标 socket write 或本地 socket write。
- 每条 Stream 拥有独立 inbound queue，默认容量按字节控制为 256 KiB，而不是只按 Frame 数控制。
- 入队不得无限阻塞。严格流控模式下窗口保证队列不会溢出；若 peer 违反窗口则 Reset 当前 Stream。
- legacy 模式队列满时 Reset 当前 Stream并记录 `legacy_backpressure`，不能阻塞其他 Stream。
- DATA Payload 所有权清晰：入队时只复制一次，消费后释放引用，避免反复 `append`。

### 逐流窗口

默认参数：

```yaml
stream:
  initial_window: 262144
  window_update_threshold: 131072
  max_frame_payload: 32768
```

规则：

- `OPEN_STREAM.Window` 声明接收方初始窗口。
- 发送 DATA 前原子扣减 send window；不足时只暂停该 Stream 的 producer。
- 消费端把 DATA 真正写入目标或本地 socket 后才增加可用 receive window。
- 累计消费达到 threshold 后发送 `WINDOW_UPDATE`，避免逐 Frame ACK。
- Window 超发、溢出或零值更新只 Reset 当前 Stream。
- 控制 Frame 不受 Stream DATA 窗口限制，但受独立的小型连接级控制队列限制。
- legacy peer 不启用 Window 语义，沿用有界队列保护。

### 公平 Writer

每条物理 WebSocket 只有一个 writer goroutine：

- PING/PONG、GOAWAY、RESET、OPEN_RESULT、WINDOW_UPDATE 使用高优先级控制队列。
- DATA 以 Stream 为单位进入 active ring，使用 deficit round-robin。
- 默认 quantum 为 32 KiB，每轮每个活跃 Stream 至多发送一个 quantum。
- 一个大下载不能长期压住 HTML、CSS、JS 等小响应。
- 所有队列有字节和条目上限；控制队列耗尽属于 Session 级异常，DATA 队列耗尽只影响对应 Stream。

### Agent 连接池

保持安全默认值：

```yaml
agent:
  connections:
    min: 1
    max: 1
```

网页低延迟部署建议显式配置：

```yaml
agent:
  connections:
    min: 2
    max: 4
    high_watermark: 16
    low_watermark: 2
    evaluation_interval: 10s
    cooldown: 30s
```

不默认增加长期连接，避免小部署无意扩大文件描述符、内存和心跳开销。连接控制器增加以下输入：

- pending dial queue depth 和等待时间。
- Stream open P95。
- TTFB P95。
- writer queue wait。
- active streams、heartbeat RTT 和最近错误率。

新 Stream 使用加权最少连接选择；已经建立的 Stream 不迁移。扩容只能改善不同 TCP 连接的并发首屏延迟，不宣传为单流加速。

### 同节点优先和跨节点 Relay

候选过滤仍先检查 Token scope、Agent Policy、能力、Session 健康和 lease。排序顺序为：

1. 当前 ingress Server 的健康 Agent 连接。
2. 相同可用区或 operator 配置的 locality。
3. 其他 Server 节点的健康连接，经 mTLS relay。

locality 只影响同等授权候选的性能排序，不能绕过 Policy。没有本地候选时必须正常回退 remote，不能为了亲和性返回错误。

## 配置模型

新增配置统一遵守 CLI > 环境变量 > 配置文件 > 默认值：

```yaml
server:
  stream:
    max_concurrent_opens: 256
    max_pending_opens: 1024
    initial_window: 262144
    window_update_threshold: 131072
    max_frame_payload: 32768
  authorization_cache:
    enabled: true
    local_positive_ttl: 5s
    cluster_positive_ttl: 5m
    negative_ttl: 3s
    revision_poll_interval: 2s
    max_stale_on_poll_error: 5s
    max_entries: 100000

agent:
  streams:
    max_concurrent_dials: 32
    max_pending_dials: 128
    connect_timeout: 5s
    open_timeout: 8s
    inbound_buffer_bytes: 262144

client:
  stream:
    open_timeout: 8s
    inbound_buffer_bytes: 262144
  remote_validation:
    positive_ttl: 15s
    negative_ttl: 2s
    timeout: 3s
    max_entries: 10000
```

所有容量值必须有上限校验，禁止负数、零窗口、超过协议 MaxPayload 的 Frame 或导致整数溢出的字节计算。配置输出继续隐藏 Token、密码、DSN 和密钥。

## 分层与组件边界

### Protocol

- 只负责 capability、Frame、OpenResult 和窗口状态机。
- 不依赖网络、配置、数据库或日志实现。

### Client

- SOCKS5 Handler 只处理本地协议和错误映射。
- `Session` 负责打开状态、逐流队列、窗口和公平 writer。
- `RemoteValidator` 通过独立 cache/singleflight 组件完成可选外部验证。

### Server

- WebSocket Handler 只负责认证后的 Frame 边界和连接生命周期。
- `ClientStreamService` 负责授权、异步打开和 OpenResult 编排。
- `CachingStreamAuthorizer` 是 Service 装饰器，依赖 `StreamAuthorizer` 与 revision source。
- `AgentRelayTransport` 负责 local/remote Stream 打开确认，不直接查询 SQL。

### Storage

- 授权资源 Repository 写事务负责原子递增 revision。
- `AuthorizationRevisionRepository` 只暴露当前修订号。
- schema 变更必须同时修改全量 DDL、v8→v9 增量 DDL、`SchemaVersion`、嵌入注册和迁移测试。

禁止 Handler 直接读缓存或 SQL，禁止 Protocol 包导入 Service/Repository。

## 安全与资源限制

- 新 Frame、窗口、队列、timeout 和 config 都必须进行边界校验。
- Stream 错误不得包含原始 `net.OpError` 文本发送给用户。
- 缓存键不得包含 bearer token、明文密码或未经规范化的敏感请求体。
- 长授权缓存必须依赖共享 revision；revision 不健康时 fail closed，而不是继续相信陈旧 allow。
- 每连接、每 Token 和每 Agent 继续执行 Stream/连接限额。
- 异步 open executor 必须公平，不能由单个 Client 占满所有拨号槽；第一版按 Client Session 设置并发上限，再受 Server 全局上限约束。
- 所有 goroutine 必须由 Session/Stream Context 管理，关闭后不得泄漏 Dial、writer 或 timer。
- 指标禁止使用目标主机、Token ID、Stream ID、Connection ID 作为无界标签。

## 可观测性

在现有指标上增加：

```text
tunnelmesh_stream_stage_duration_seconds
tunnelmesh_stream_open_total
tunnelmesh_stream_queue_wait_seconds
tunnelmesh_stream_window_stall_seconds
tunnelmesh_stream_backpressure_total
tunnelmesh_authorization_cache_requests_total
tunnelmesh_authorization_revision
tunnelmesh_authorization_revision_poll_total
tunnelmesh_remote_validation_cache_requests_total
```

允许标签：

```text
component=client|server|agent
stage=socks5_handshake|remote_validation|authorization|selection|relay|queue|dns|connect|open|ttfb
result=success|failure|timeout|rejected
error_class=<bounded enum>
protocol=tcp|udp|http|websocket
scope=local|remote
cache=hit|miss|shared|bypass
open_mode=strict|legacy
```

不得把 hostname、IP、port、Token、用户名或密码放入 Prometheus 标签。结构化日志可包含脱敏 Agent ID、Server Node ID、trace ID、阶段、稳定错误码和微秒耗时。

TTFB 定义为 Server 接受 Client `OPEN_STREAM` 到该 Stream 第一帧反向 DATA 的时间；SOCKS5 本地 TTFB 定义为 Client 返回 success 后到第一帧目标数据的时间。两者分开记录，避免混淆目标服务响应时间和隧道打开时间。

Grafana 单一 Dashboard 的 Network Row 增加：

- Stream open P50/P95/P99，按 stage。
- 严格/legacy 打开比例。
- authorization cache hit ratio 和 revision poll 状态。
- dial queue wait、窗口阻塞和 backpressure。
- local/remote relay 比例及 TTFB。

## 失败与降级行为

- Agent 拨号池满：当前 Stream 快速失败，Session 保持在线。
- Server open 池满：当前 Stream 返回 `queue_full`，不无限排队。
- Agent 目标不可达：当前 Stream 返回稳定错误，其他页面资源继续加载。
- revision poll 失败：5 秒内允许使用最近确认 generation；之后 allow 缓存停止服务并回源，回源失败则拒绝。
- remote auth endpoint 故障：按现有 fail-closed 语义拒绝，网络错误最多缓存 1 秒。
- 严格 capability 不完整：回退 legacy；运维通过指标识别，不在同一 Stream 中途切换语义。
- Window 违规：Reset 当前 Stream；重复大规模违规达到连接级防滥用阈值后关闭 Session并审计。
- 公平 writer 控制队列耗尽：关闭 Session，避免无法发送 GOAWAY/RESET 的失控状态。
- 跨节点 relay 不可用：若无其他候选则当前 Stream 失败；不影响本地 Agent Stream。

## 升级和回滚

升级顺序：

1. 备份数据库并确认 `schema_meta.version=8`、v8→v9 增量链完整。
2. 部署包含 v9 DDL 和 revision reader 的 Server，但保持新 capability 未宣告。
3. 验证 revision 行初始化、poll、readiness 和缓存旁路。
4. 滚动升级所有 Server，启用 WebSocket 子协议和 relay OpenResult。
5. 升级 Agent，仍保持 `connections.min=1/max=1`。
6. 升级 Client，观察 strict/legacy 比例和 Stream open 指标。
7. 根据容量逐步启用 Agent `min=2/max=4`。
8. 最后启用 flow control 和 fair writer capability。

回滚要求：

- v9 为向前兼容新增表，旧版本可以忽略，回滚应用不删除表；如必须精确恢复 Schema，使用升级前备份。
- capability 可独立关闭；关闭后新连接使用 legacy，已有严格连接自然 drain。
- 授权缓存可以通过配置关闭，关闭后直接走现有数据库授权路径。
- Agent 连接池可回退 `min=1/max=1`。
- 不进行协议中途降级；需要回滚时发送 GOAWAY 并重连。

## TDD 与测试策略

### Protocol

- Frame 编解码、1 KiB OpenResult 上限、稳定 code/stage。
- WebSocket 子协议选择和旧 Server 无子协议兼容。
- 旧/new Agent hello capability JSON 兼容。
- 打开状态机的重复、越序、Reset、超时和取消。
- Window 消耗、更新、溢出、超发与 property/fuzz 测试。

### Agent

- 两个阻塞 Dial 不阻塞第三个已就绪 Stream 的 reader。
- 最大并发和最大排队严格有界。
- Reset/Session close 取消正在执行的 Dial。
- 单 Stream DNS、Dial、写失败不会终止 Session。
- 成功安装状态早于 OPEN_OK。
- 队列满和 legacy backpressure 只 Reset 当前 Stream。
- race 测试覆盖 close/open/result 并发。

### Server

- 严格 Client 异步授权/打开不阻塞 PING 和其他 Stream。
- legacy Client 保持原行为。
- local 和 remote relay 正确传播 OPEN_OK/OPEN_ERR。
- 单 Stream 失败不关闭 Client/Agent Session。
- 公平 writer 在大流持续发送时仍及时发送控制帧和小流 DATA。
- TTFB、队列等待和 backpressure 指标标签受控。

### Authorization cache

- SQLite 5 秒和 MySQL 5 分钟默认值。
- 同键并发 miss 只执行一次 Repository 授权。
- revision 变化立即使旧 generation 失效。
- Token 撤销、Agent 禁用、Policy 更新与 revision 在同一事务。
- poll 失败超过 5 秒后不能继续接受陈旧 allow。
- revision 回退、缺行和溢出 fail closed。
- SQLite 与真实 MySQL contract；没有 `TUNNELMESH_TEST_MYSQL_DSN` 时明确记录 deferred。

### Remote validation

- allow/deny/network error 使用不同 TTL。
- 同键并发合并。
- 不同认证摘要不共享缓存。
- endpoint 更新和 Close 清理连接与缓存。
- 日志与指标不泄露 credentials/target。

### E2E 与性能验收

- 一页模拟 32 条并发 CONNECT，其中一个 DNS/Dial 阻塞，其余连接继续完成。
- 一个目标拒绝不会引起 Agent reconnect。
- 新三端严格确认；三种混合旧版本组合回退 legacy。
- 同节点和跨节点 relay 均完成严格 OpenResult。
- 授权撤销在多 Server MySQL 环境 3 秒内阻断新 Stream。
- 大流与 31 个小流并发时，小流 open/TTFB 不发生无界增长。
- goroutine、heap 和队列在连接关闭后回到基线。

性能门槛以可重复的基准环境记录，不硬编码绝对公网延迟。相对当前 main 基线：

- 32 并发 CONNECT 的 P95 open latency 至少降低 50%。
- 单个 5 秒慢 Dial 不得让无关 Stream 的 open latency增加 1 秒以上。
- MySQL 稳态授权查询数至少降低 90%。
- 权限变更在 revision poll 正常时 3 秒内对所有 Server 生效。
- 公平调度下小流 P95 TTFB 不超过无大流基线的 2 倍。

## 文档更新

实现时同步维护：

- `docs/user-guide/client.md`：严格 SOCKS5 打开、远程 DNS、auth_url 缓存及错误语义。
- `docs/user-guide/agent.md`：拨号执行器、连接池和低延迟配置。
- `docs/operations/configuration.md`：所有新增配置和优先级。
- `docs/operations/connection-pool.md`：网页负载建议与回滚。
- `docs/operations/troubleshooting.md`：open stage、TTFB、DNS、relay 和缓存排查。
- `docs/operations/network-probes.md`：如何把探针和 Stream stage 指标结合使用。
- `docs/protocol/proxy-modules.md`：capability、OpenResult、Window 与 legacy 规则。
- `docs/api/openapi.yaml`：仅在管理 API/配置视图发生行为变化时更新。
- `deploy/grafana/dashboards/tunnelmesh.json`：仍只维护一个 Dashboard。
- `docs/README.md`：更新索引。

## 完成定义

- P0、P1、P2 的测试全部先失败再实现通过，并保留 RED 证据在开发记录中。
- SQLite 与可用的 MySQL contract test 通过。
- `go test ./... -count=1`、`go test -race ./...`、`go vet ./...`、`go build ./cmd/...` 和 `git diff --check` 通过。
- 若修改前端，`npm test -- --run` 和 `npm run build` 通过，且 Go embed 资源已同步。
- Docker/Compose 配置可解析，非 root 启动和健康检查不回退。
- 文档、Grafana、全量 DDL、v8→v9 增量 DDL、配置示例和回滚步骤保持一致。
- 独立代码评审没有未处理的 Critical 或 Important 问题。
- 未经用户明确授权不执行 commit、push 或 merge。
