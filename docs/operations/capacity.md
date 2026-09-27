# 容量与压测

容量规划至少覆盖并发 WebSocket 数、逻辑 stream 数、每秒新连接、TCP/UDP 吞吐、心跳频率和数据库写入速率。压测时分别测本地 SQLite、MySQL 集群和跨节点 relay，不要用单机结果推导集群容量。

建议记录：Server CPU/内存、连接数、P95/P99 建连耗时、heartbeat RTT、bytes rate、stream error rate、SQLite busy、MySQL pool wait 和 relay 重传。达到任一资源 70% 时扩容或限流，达到 85% 时进入保护模式。

压测前应明确各层的准入水位，它们都是可配置项（默认值见 `docs/operations/configuration.md`）：单 Agent
活跃流上限 `server.stream.max_active_per_agent` 与 Agent 进程侧 `agent.streams.max_active`（两者均以
`queue_full` 拒绝，不排队）、单 Agent 物理连接数 `server.agents.max_connections_per_agent`、管理监听器
连接数 `server.http.max_connections`（0 表示不限，超限立即关闭并计入
`tunnelmesh_connections_total{mode="http",result="rejected"}`）。压测里出现 `rejected`/`queue_full`
说明撞到的是自设水位而不是后端容量，先区分两者再谈扩容。

故障注入至少包括 Agent 断网、Server 重启、数据库不可用、lease 过期、relay 节点摘除、Nginx Upgrade 丢失和高延迟/丢包。恢复目标应验证旧 epoch 的 stats 不会覆盖新 lease。
