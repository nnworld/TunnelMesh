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

## 身份认证（SSO / MFA / 受信任设备）

管理台登录、两步验证、OIDC 单点登录和受信任设备暴露以下 6 个指标族。注册位置
`internal/observability/metrics.go`，标签契约由 `internal/observability/metrics_auth_test.go` 守护：

| 指标 | 类型 | 标签 |
| --- | --- | --- |
| `tunnelmesh_auth_login_total` | Counter | `method`、`result` |
| `tunnelmesh_auth_mfa_verify_total` | Counter | `method`、`result` |
| `tunnelmesh_auth_oidc_step_total` | Counter | `step`、`result` |
| `tunnelmesh_auth_trusted_devices` | Gauge | 无 |
| `tunnelmesh_auth_pending_challenges` | Gauge | 无 |
| `tunnelmesh_auth_login_blocked_buckets` | Gauge | 无 |

标签取值是一个**封闭枚举**，任何不在清单内的输入都归一化成 `unknown`，因此新增入口在指标里是
可见的，而不会静默并入已有取值：

| 标签 | 允许取值 | 含义 |
| --- | --- | --- |
| `login_total.method` | `password`、`oidc` | 登录入口类型 |
| `login_total.result` | `success`、`failure`、`mfa_required`、`throttled` | 登录结果；`mfa_required` 表示密码正确但还欠一个二次验证 |
| `mfa_verify_total.method` | `totp`、`recovery` | 用哪种第二因子通过校验 |
| `mfa_verify_total.result` | `success`、`invalid`、`expired`、`attempts_exceeded` | `invalid` 刻意同时覆盖“码不对”和“challenge 不可用”，否则指标会暴露哪些账号有活跃 challenge |
| `oidc_step_total.step` | `discovery`、`jwks`、`token`、`id_token`、`provision` | 依赖方流程阶段 |
| `oidc_step_total.result` | `ok`、`error` | 阶段结果 |

`oidc_step_total` 按阶段拆分是为了让“IdP 半坏”可诊断：`discovery` 成功而 `token` 失败指向 client
凭据配错，`token` 成功而 `id_token` 失败指向签名算法、`iss`/`aud`/`nonce` 校验或 JWKS 轮换。

三个 Gauge 由身份维护清理器在每轮清扫后重新采样，不是请求路径写入：

- `tunnelmesh_auth_trusted_devices`：当前所有账号的有效受信任设备总数。突降意味着一次清扫或批量撤销；
  持续爬升逼近 `auth_settings.max_trusted_devices × 账号数` 说明上限配置过小。
- `tunnelmesh_auth_pending_challenges`：当前未消费的 `auth_challenges` 行数（含 `login_mfa`、
  `oidc_state`、`login_ticket`）。在没有对应登录速率的情况下增长，说明登录页被弃单或被攻击。
- `tunnelmesh_auth_login_blocked_buckets`：当前处于封禁状态的 username+IP 桶数量。这是撞库最早期的信号。

清理器（`internal/server/auth_maintenance_sweeper.go`）每 **1 分钟**跑一次，启动时先立即跑一次：
删除已过期的 challenge（按各自 `expires_at`）、过期或已撤销且超过 **7 天**保留期的受信任设备、
以及闲置超过同一保留期的登录限流桶，然后重采样三个 Gauge。保留期存在的意义是审计可追溯——审计行
引用了 device id 或 challenge id，立即删除会在证据链里留下悬空标识符。每条语句都是带时间戳边界的
幂等 `DELETE`，所以集群里每个节点各跑一份清理器是安全的，重复的一轮只会删掉 0 行并花费一次索引扫描；
这比引入分布式锁更可靠。清扫间隔与保留期是编译期常量，当前不可配置。

指标里不含用户名、客户端 IP、provider id、challenge id、device token 或 TOTP 码。`/metrics` 无需
管理会话即可读取，把这些放进标签既会造成攻击者可控的基数，也会泄露账号名。

建议告警阈值（起点值，按实际登录量校准后再收紧）：

| 告警 | 表达式 | 阈值与判断依据 |
| --- | --- | --- |
| 登录失败率异常 | `sum(rate(tunnelmesh_auth_login_total{result="failure"}[5m])) / sum(rate(tunnelmesh_auth_login_total[5m]))` | 持续 10 分钟 > `0.3` 且分母 > `0.1/s`；低流量时用绝对量而不是比率，避免单次失败把比率打到 1 |
| 撞库 / 暴力破解 | `tunnelmesh_auth_login_blocked_buckets` | > `20` 持续 5 分钟，或 15 分钟内增幅 > `50`。桶数独大通常意味着分布式撞库 |
| 限流命中飙升 | `sum(rate(tunnelmesh_auth_login_total{result="throttled"}[5m]))` | 持续 5 分钟 > `0.05/s`；同时检查是否把正常用户挡在门外 |
| 恢复码被当作主因子 | `sum(rate(tunnelmesh_auth_mfa_verify_total{method="recovery",result="success"}[15m]))` | 15 分钟内 > `5` 次。恢复码是验证器丢失时的兜底，常态使用意味着批量丢设备或有人在批量接管 |
| 二次验证尝试耗尽 | `sum(rate(tunnelmesh_auth_mfa_verify_total{result="attempts_exceeded"}[10m]))` | 10 分钟内 > `10`；配合 `invalid` 一起看，单独升高更像是时钟漂移而非攻击 |
| OIDC 依赖方故障 | `sum(rate(tunnelmesh_auth_oidc_step_total{result="error"}[5m])) by (step)` | 任一 `step` 持续 5 分钟 > `0.02/s`。按 `step` 分组是定位关键：`discovery`/`jwks` 指向网络或 issuer，`token` 指向 client 凭据，`id_token` 指向签名与 claim，`provision` 指向 role mapping 或账号冲突 |
| challenge 堆积 | `tunnelmesh_auth_pending_challenges` | > `500` 且 `tunnelmesh_auth_login_total` 速率正常。清理器每分钟收敛，持续增长说明登录页被脚本刷 |
| 受信任设备异常波动 | `abs(delta(tunnelmesh_auth_trusted_devices[10m]))` | 10 分钟内下降 > `50%`，通常是一次批量撤销或 `device_trust_enabled` 被关掉 |

上述告警中不得放置 Token、用户名、IP 或 provider 明文；需要定位具体账号时改用审计日志
（`auth.login.*`、`auth.mfa.*`、`auth.device.*`、`auth.oidc.provider.*`、`auth.policy.update`），
而不是给指标加标签。

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
method, step, agent_id, instance_id, server_node_id
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

只允许使用 component、mode、protocol、stage、result、error_class、probe_kind、route、reason、method、step 等低基数标签；身份认证指标的 `method` 与 `step` 都是封闭枚举，越界输入归一化为 `unknown`，基数不会随请求内容增长。Token、连接 ID、stream ID、目标地址、客户端 IP、`Proxy-Authorization` 和 metadata 不得进入 Prometheus label。默认保留周期由 Prometheus 决定，建议生产环境 15–30 天，长期趋势使用 recording rules 或远端时序存储。

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

安全值班负责管理台身份链路：`tunnelmesh_auth_login_blocked_buckets`、`tunnelmesh_auth_login_total{result="failure"|"throttled"}`、
`tunnelmesh_auth_mfa_verify_total{method="recovery"}` 和 `tunnelmesh_auth_oidc_step_total{result="error"}`。判据同样是相对量级：
`blocked_buckets` 与 `failure` 同时升高是撞库；只有 `mfa_verify_total{result="invalid"}` 升高而 `failure` 平稳，更像是验证器时钟漂移
（检查 `security.auth.mfa.skew` 和服务器 NTP）；只有 `oidc_step_total` 某一个 `step` 升高是 IdP 侧配置问题，按
[单点登录与两步验证](../user-guide/sso-and-mfa.md) 的排障表定位。

探针只保存状态、耗时和 allowlist error class，不保存响应体。探针目标必须经过资源所有者或管理员授权。

探针当前的可用边界、指标查询和端到端 API 状态见 [全链路网络探针](network-probes.md)。

真实 MySQL contract 测试依赖 `TUNNELMESH_TEST_MYSQL_DSN`；未配置时只能运行 SQLite contract，不能将其标记为 MySQL 已验证。
