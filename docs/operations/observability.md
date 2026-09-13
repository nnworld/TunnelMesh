# 可观测性

Server 在管理监听器暴露 `GET /metrics`、`/health/live` 和 `/health/ready`。生产环境建议只允许 Prometheus 所在内网访问 `/metrics`，公网入口通过 Nginx 仅暴露健康检查。

## WebSSH/SFTP

WebSSH 会话暴露以下指标。指标只包含状态、方向和稳定错误类别，不包含用户名、目标地址、终端内容、SFTP 路径或文件内容：

- `tunnelmesh_webssh_sessions_active`
- `tunnelmesh_webssh_tickets_created_total`
- `tunnelmesh_webssh_ticket_reuse_total{reason}`
- `tunnelmesh_webssh_streams_errors_total{error_class}`
- `tunnelmesh_webssh_stream_duration_seconds`
- `tunnelmesh_webssh_bytes_total{direction}`

`reason` 只使用 `expired`、`reused`、`invalid` 等稳定枚举；`error_class` 使用统一错误归一化结果。后台清理器每 60 秒收敛过期 pending ticket 和 active session，进程退出或 Runtime Close 时会停止清理器。

## Grafana

导入 [统一 Dashboard](../../deploy/grafana/dashboards/tunnelmesh.json)。这是项目唯一的 Dashboard 文件，内部按 Overview、Agent、Network、Cluster、Security 五个 Row 组织全部图表。Prometheus datasource 使用 `${DS_PROMETHEUS}`。

延迟和稳定性相关的视图集中在同一 Dashboard：

- Agent Row：连接池扩缩容决策、Agent 连接选择、活跃连接和错误。
- Network Row：stream 打开结果、阶段耗时、队列等待、窗口等待、backpressure、探针延迟。
- Cluster Row：授权缓存命中率、远端校验缓存、授权 revision 和 revision 轮询结果。

推荐同时加载：

- `deploy/prometheus/recording-rules.yaml`
- `deploy/prometheus/alert-rules.yaml`
- `deploy/prometheus/prometheus.yml.example`

## 标签和留存

只允许使用 component、mode、protocol、stage、result、error_class、probe_kind 等低基数标签。Token、连接 ID、stream ID、目标地址和 metadata 不得进入 Prometheus label。默认保留周期由 Prometheus 决定，建议生产环境 15–30 天，长期趋势使用 recording rules 或远端时序存储。

## 告警责任

平台值班负责 Server readiness、scrape 缺失、存储失败和 relay 失败；网络值班负责 heartbeat miss、探针成功率和 RTT；业务负责人负责 Agent 离线和路由拒绝。告警中不得放置 Token 或原始目标地址。

探针只保存状态、耗时和 allowlist error class，不保存响应体。探针目标必须经过资源所有者或管理员授权。

探针当前的可用边界、指标查询和端到端 API 状态见 [全链路网络探针](network-probes.md)。

真实 MySQL contract 测试依赖 `TUNNELMESH_TEST_MYSQL_DSN`；未配置时只能运行 SQLite contract，不能将其标记为 MySQL 已验证。
