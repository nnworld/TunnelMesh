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

## Prometheus 抓取与规则

只有 Server 暴露 `/metrics`；Agent 与 Client 没有本地 metrics 端点，它们的运行观测通过协议上报
给 Server 后由 Server 输出。因此不要为 Agent/Client 配置单独的 scrape job。

起点配置见 [deploy/prometheus/prometheus.yml.example](../../deploy/prometheus/prometheus.yml.example)，
其中包含单节点与集群两种形态。集群模式下必须为每个节点单独列出 target，并逐节点标注
`node_id`，取值与 `server.node.id` / `TUNNELMESH_NODE_ID` 完全一致，否则 Cluster Row 与按节点
聚合的查询无法对齐。`cluster` 与 `node_id` 都不是应用输出的标签，只能由 Prometheus 侧注入。

**不要在 target labels 里覆盖应用自身的标签名。** 默认 `honor_labels: false`，同名 target label
会把指标原标签改写成 `exported_*`，直接让 `recording-rules.yaml` 与 `alert-rules.yaml` 中依赖
这些标签的表达式失效。应用已占用的标签名：

```text
component, mode, result, error_class, stage, direction, protocol,
probe_kind, operation, cache, scope, decision, reason, route,
agent_id, instance_id, server_node_id
```

`/metrics` 只应对 Prometheus 所在内网开放；公网入口通过 Nginx 只暴露 `/health/live` 与
`/health/ready`。确实需要认证时优先用来源网段放行，其次才使用只读 token 的
`authorization.credentials_file`，不要把 token 写进配置文件正文。

校验：

```bash
promtool check config deploy/prometheus/prometheus.yml.example
promtool check rules deploy/prometheus/recording-rules.yaml deploy/prometheus/alert-rules.yaml
```

## Grafana

导入 [统一 Dashboard](../../deploy/grafana/dashboards/tunnelmesh.json)。这是项目唯一的 Dashboard 文件，内部按 Overview、Agent、Network、Cluster、Security、HTTP Proxy Entry 六个 Row 组织全部图表。Prometheus datasource 使用 `${DS_PROMETHEUS}`。

Dashboard 顶部有四个变量，全部面板的查询都按它们过滤：

| 变量 | 取值来源 | 说明 |
| --- | --- | --- |
| `cluster` | `label_values(tunnelmesh_ready, cluster)` | 由 Prometheus target label 注入 |
| `node_id` | `label_values(tunnelmesh_ready, node_id)` | 由 Prometheus target label 注入，取值等于 `server.node.id` |
| `component` | `label_values(tunnelmesh_connections_active, component)` | 应用输出标签 |
| `agent_id` | `label_values(tunnelmesh_agent_connections, agent_id)` | 应用输出标签 |

四个变量都开启 All 且 `allValue` 为 `.*`，因此即使抓取配置没有注入 `cluster`/`node_id`，选择 All
时面板仍然有数据；但一旦选择具体取值，未注入的标签会筛出空结果。`Readiness`、`Ready nodes` 和
`Active agent connections` 按 `node_id` 分组，逐节点展示。变量与面板筛选标签的一致性由
`deploy/grafana/dashboard_schema_test.go` 守护。

延迟和稳定性相关的视图集中在同一 Dashboard：

- Agent Row：连接池扩缩容决策、Agent 连接选择、活跃连接和错误。
- Network Row：stream 打开结果、阶段耗时、队列等待、窗口等待、backpressure、探针延迟。
- Cluster Row：授权缓存命中率、远端校验缓存、授权 revision 和 revision 轮询结果。
- HTTP Proxy Entry Row：代理请求速率与结果、活跃隧道数（按 route）、隧道时长 p95、代理入口上下行字节、认证失败与 ACL 拒绝、拒绝明细表。

HTTP Proxy Entry Row 依赖的 5 个指标（注册位置 `internal/observability/metrics.go`，标签契约由
`internal/observability/metrics_proxy_entry_test.go` 守护）：

| 指标 | 类型 | 标签 |
| --- | --- | --- |
| `tunnelmesh_proxy_entry_requests_total` | Counter | `route`、`mode`、`result`、`error_class` |
| `tunnelmesh_proxy_entry_tunnels_active` | Gauge | `route` |
| `tunnelmesh_proxy_entry_tunnel_duration_seconds` | Histogram | `route`、`result` |
| `tunnelmesh_proxy_entry_auth_failures_total` | Counter | `route`、`reason` |
| `tunnelmesh_proxy_entry_acl_denied_total` | Counter | `route` |

`server.proxy_entry.enabled=false`（默认）时这 5 个指标根本不会被写入，该行所有面板为空，属正常
现象，不是抓取故障；对应的两条告警也刻意不带 `absent()` 前缀，见下方“告警责任”。

容器部署时挂载 `deploy/grafana`，并同时加载 recording 与 alert 规则：

```yaml
volumes:
  - ./deploy/grafana/dashboards:/var/lib/grafana/dashboards/tunnelmesh:ro
  - ./deploy/grafana/provisioning:/etc/grafana/provisioning:ro
  - ./deploy/prometheus:/etc/prometheus/tunnelmesh:ro
```

`provisioning/datasources.yml` 中的 Prometheus 地址默认是 `http://prometheus:9090`，与实际部署
不一致时必须修改；手工导入 Dashboard 时选择 Prometheus 数据源即可绑定 `${DS_PROMETHEUS}`。

产物一致性校验（Dashboard schema、安装模板与脚本）：

```bash
go test ./deploy/... -count=1
```

`deploy/` 只放产物，目录内容与发布包布局见 [deploy/README.md](../../deploy/README.md)。

## 标签和留存

只允许使用 component、mode、protocol、stage、result、error_class、probe_kind、route、reason 等低基数标签。Token、连接 ID、stream ID、目标地址、客户端 IP、`Proxy-Authorization` 和 metadata 不得进入 Prometheus label。默认保留周期由 Prometheus 决定，建议生产环境 15–30 天，长期趋势使用 recording rules 或远端时序存储。

新增的 `route` 与 `reason` 的基数边界：

- `route` 取值是 `tp-<name>.<domain_suffix>` 完整域名，数量等于管理后台创建的代理路由数。路由由
  人工创建，不是请求维度，因此基数受管理员行为约束；仍然禁止把目标主机名或客户端 IP 拼进这个标签。
- `reason` 取值直接复用 `internal/proxyentry/errors.go` 的稳定错误码（完整清单见
  [HTTP 代理入口](../user-guide/http-proxy-entry.md)），认证失败路径上只可能是 `proxy_auth_required`（缺失或
  格式非法的 `Proxy-Authorization`）、`proxy_auth_failed`（用户名或密码不匹配、凭据被停用/软删）、
  `proxy_auth_backoff`（退避中，不做密码比对）、`credential_secret_unavailable`（secret store 不可用）
  四个之一，不随请求内容变化。

## 告警责任

平台值班负责 Server readiness、scrape 缺失、存储失败和 relay 失败；网络值班负责 heartbeat miss、探针成功率和 RTT；业务负责人负责 Agent 离线、路由拒绝，以及 tp-* 代理入口的
`TunnelMeshProxyEntryAuthFailures` 与 `TunnelMeshProxyEntryDenials`（与路由拒绝同类）。值班需要能
区分“ACL 配错”和“密码爆破”，判据是 `tunnelmesh_proxy_entry_acl_denied_total` 与
`tunnelmesh_proxy_entry_auth_failures_total` 的相对量级：前者独大通常是来源网段没加进 ACL，后者
独大且集中在单条 `route` 上是爆破。告警中不得放置 Token 或原始目标地址。

探针只保存状态、耗时和 allowlist error class，不保存响应体。探针目标必须经过资源所有者或管理员授权。

探针当前的可用边界、指标查询和端到端 API 状态见 [全链路网络探针](network-probes.md)。

真实 MySQL contract 测试依赖 `TUNNELMESH_TEST_MYSQL_DSN`；未配置时只能运行 SQLite contract，不能将其标记为 MySQL 已验证。
