# TunnelMesh 可观测性与代理协议增强设计

## 目标

为 TunnelMesh 建立可生产使用的 Prometheus 指标、Grafana 观测视图、告警规则和协议级诊断能力，同时补齐项目在健康检查、探针、集群 relay、发布运维和容量验证方面的完整性缺口。

## 范围

本设计覆盖：

1. Server、Agent、Client 的 Prometheus 指标注册、暴露与受控标签。
2. `/health/live`、`/health/ready`、`/metrics` 端点及管理 API 的观测数据边界。
3. 一份统一 Grafana dashboard JSON model，按 Overview、Agent、Network、Cluster、Security 五个 Row 组织，并配套 Prometheus recording/alerting rules。
4. 代理协议的能力协商、结构化 PING/PONG、OPEN 结果、稳定错误码、窗口更新、UDP association 和 capability-gated probe 扩展。
5. 文档、E2E、故障注入和项目完整性清单。

不在本阶段实现完整 OpenTelemetry collector、分布式 trace 存储、公网 UDP 监听或任意命令执行。

## 指标约束

Prometheus 指标必须使用 `prometheus/client_golang`，支持注入独立 `prometheus.Registry` 以便测试。指标名称使用 `tunnelmesh_` 前缀。

允许的低基数标签：`component`、`mode`、`node_id`、`agent_id`、`protocol`、`stage`、`result`、`error_class`、`status`、`probe_kind`。

禁止标签：`token_id`、`connection_id`、`stream_id`、原始目标 IP/域名、用户输入、Bearer token、metadata value、响应内容。高基数和敏感信息只能进入结构化日志或 trace context，且必须脱敏。

核心指标族：

| 指标 | 类型 | 目的 |
| --- | --- | --- |
| `tunnelmesh_connections_total` | counter | Agent/Client/relay 连接结果 |
| `tunnelmesh_connections_active` | gauge | 当前连接数 |
| `tunnelmesh_connection_stage_duration_seconds` | histogram | DNS/TCP/TLS/WS/Auth/Hello/Heartbeat 阶段耗时 |
| `tunnelmesh_heartbeat_total` | counter | PING/PONG、miss、timeout |
| `tunnelmesh_heartbeat_rtt_seconds` | histogram | 心跳 RTT |
| `tunnelmesh_bytes_total` | counter | 入站/出站字节 |
| `tunnelmesh_streams_active` | gauge | 当前逻辑 stream |
| `tunnelmesh_streams_total` | counter | stream 创建、成功、拒绝、reset |
| `tunnelmesh_stream_errors_total` | counter | 协议、授权、拨号和目标错误 |
| `tunnelmesh_probe_total` | counter | 探针结果 |
| `tunnelmesh_probe_duration_seconds` | histogram | 探针延迟 |
| `tunnelmesh_registry_lease_total` | counter | lease 获取/续期/丢失 |
| `tunnelmesh_relay_total` | counter | 跨节点 relay 结果 |
| `tunnelmesh_storage_operation_duration_seconds` | histogram | DB/registry 操作耗时 |
| `tunnelmesh_storage_errors_total` | counter | 存储错误 |
| `tunnelmesh_ready` | gauge | readiness 状态 |
| `tunnelmesh_config_reload_total` | counter | 配置加载/失败 |

## HTTP 端点

- `GET /health/live`：只表示进程事件循环可响应，不依赖数据库。
- `GET /health/ready`：检查数据库、registry、必要 relay listener 和 embed 资源；失败返回 503，响应不泄露 DSN、证书路径或 Token。
- `GET /metrics`：默认绑定管理地址；生产环境要求网络 allowlist 或反向代理认证。响应只包含 Prometheus 文本格式。
- `GET /api/v1/agents/{id}/metrics?range=5m`：只返回资源所有者/admin 可见的聚合统计，不返回目标响应内容或凭据。
- `GET /api/v1/agents/{id}/probes`：分页返回探针摘要与错误分类。

## Grafana dashboard

统一 Dashboard 文件为 `deploy/grafana/dashboards/tunnelmesh.json`，必须使用 Prometheus datasource 变量 `${DS_PROMETHEUS}`，并包含变量 `cluster`、`node_id`、`agent_id`。Dashboard 内使用五个 Row：Overview、Agent、Network、Cluster、Security；每个 Row 至少包含状态/可用性、吞吐或资源量、延迟、错误率和最近异常面板。全局共享一个 time picker，面板查询只使用低基数标签。

## Nginx 推荐配置

独立文档为 `docs/deployment/nginx.md`。配置必须明确区分 `/api/`、`/ws/agent`、`/ws/client`、`/ws/tcp`、健康检查和受保护的 `/metrics`，并保证 API/WebSocket 路径优先于 SPA fallback。

## 协议增强

协议版本保持向后兼容；新增 frame 使用 capability gating。新增能力包括：

- `hello.v2`：协议版本、capabilities、最大 payload/datagram、窗口参数。
- `heartbeat.v1`：`PING/PONG` 携带单调 sequence、发送时间和可选 nonce，payload 上限 128 bytes。
- `open_result.v1`：OPEN 后返回 `accepted`、阶段、稳定错误码、`retry_after_ms` 和 message。
- `flow_control.v1`：双向 `WINDOW_UPDATE`，发送端不得超过 peer advertised window。
- `udp_assoc.v1`：association ID、空闲 TTL、datagram 上限、EMSGSIZE 和关闭原因。
- `probe.v1`：能力协商后才允许 TCP/HTTP/UDP echo probe；结果只包含状态、耗时和错误分类，不返回响应体。
- `relay.v1`：Server-node relay 收敛为正式 protobuf schema，替代动态 struct payload。

未知 frame/extension：已知版本内必须安全忽略非关键扩展；关键扩展未协商时返回稳定 `unsupported_capability` 错误并保持连接可用。

## 项目完整性验收

本阶段完成后必须具备：Prometheus scrape、Grafana JSON 导入、告警规则、健康检查、指标测试、协议兼容测试、MySQL/etcd 集成记录、Docker/Nginx/systemd 文档、跨平台构建说明、故障注入测试和容量/SLO 文档。

真实 MySQL contract 如果环境没有 `TUNNELMESH_TEST_MYSQL_DSN`，必须明确记录为 deferred，不得伪造通过。
