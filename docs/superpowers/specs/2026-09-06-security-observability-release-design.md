# TunnelMesh 安全授权、稳定性与发布运维设计

**日期：** 2026-09-06  
**状态：** 已确认设计，待实施计划  
**范围：** 独立作用域 Token、TLS/WSS、Agent 运行质量、网络探针、线路保活、结构化日志、进程保活、多平台发布和 Nginx 部署。

## 1. 目标与非目标

### 1.1 目标

1. 让 Agent、Client、Server-node 和管理用户使用相互隔离的凭据。
2. 让所有公网长连接在 TLS 保护下运行，并对每一条逻辑流重复执行授权和 Policy 检查。
3. 为 Agent 提供可观测的活跃状态、质量、带宽、延迟、重连和探针结果。
4. 让异常网络可以按连接阶段定位，并让长连接在网络抖动下自动保活、降级和重连。
5. 提供 Linux、macOS、Windows 和 Docker 的可重复发布、安装、升级和回滚文档。

### 1.2 非目标

- 不实现自定义加密算法或第二套 SSH 协议。
- 不把业务 payload、SSH 字节、Token 明文或 metadata 敏感值写入日志和指标。
- 不让 Server 进程自行 daemon 化；进程保活交给 systemd、launchd、Windows Service 或容器编排器。
- 不把 Prometheus 变成管理数据权威来源；Agent、Token、Policy 和 Tunnel 仍以数据库为权威。

## 2. 子项目拆分

该需求拆为三个可以独立验收的子项目：

| 子项目 | 主要交付 | 依赖 |
| --- | --- | --- |
| A. 安全凭据与连接授权 | Token 生命周期、作用域、Client WS 鉴权、TLS 配置、审计 | 现有 auth、API、Agent WS |
| B. 可观测性与网络稳定性 | PING/PONG、连接阶段、探针、质量聚合、metrics、诊断 API、结构化日志 | A 的 token identity；现有 relay/session |
| C. 发布和运维 | GoReleaser/Makefile、多平台归档、Nginx、systemd/launchd/Windows Service、升级回滚 | A/B 的配置和健康端点 |

每个子项目都必须先写失败测试，再实现最小行为，最后运行跨包和竞态验证。

## 3. 安全模型

### 3.1 Token 类型

新增独立的长期凭据实体，数据库只保存不可逆哈希：

- `agent_token`：绑定单个 Agent，只能用于 `/ws/agent`。
- `client_token`：绑定用户，用于 `/ws/client`、`proxy` 和转发连接。
- `server_node_token`：绑定 Server node，只用于集群节点注册、续租和节点间转发。
- `user_token`：现有管理 API 登录凭据，只用于管理 API。

Token 元数据包含：ID、类型、owner、前缀、哈希、scope JSON、创建时间、过期时间、最后使用时间、撤销时间、状态和审计引用。Token 明文只在创建/轮换响应中显示一次。

### 3.2 Scope 与授权顺序

`client_token` 的权限不能超过用户拥有的 Agent Policy：

```text
TLS/WSS
  -> Token 哈希和状态校验
  -> Token 类型校验
  -> owner/resource scope 校验
  -> Agent Policy 校验
  -> 创建逻辑流
```

Token 可以收紧 Agent、协议、目标 CIDR 和端口范围，但不能放宽数据库中的 Agent Policy。每个逻辑流 OPEN 都必须重新执行 scope 和 Policy 检查，不能只依赖 WebSocket 建连时的授权。

### 3.3 TLS 与 WebSocket

- 公网只暴露 80/443；80 只重定向到 443。
- 管理 API、后台、Agent WS 和 Client WS 使用 HTTPS/WSS。
- 默认 TLS 1.2+，推荐 TLS 1.3；客户端默认校验证书。
- Server 可配置原生 TLS 监听；生产推荐 Nginx/负载均衡终止边缘 TLS。
- Nginx 到 Server 可选内网 HTTP 或 HTTPS；Server-node 链路强制 mTLS，应用层仍校验 `server_node_token`。
- Token 只允许放在 `Authorization: Bearer`，禁止 query string、Cookie 和 URL path。
- WebSocket 校验 Host/Origin 白名单和 Upgrade 头，禁止不受信任来源复用后台会话。

### 3.4 Token 后台管理

后台和 API 提供：创建、列表、查看元数据、撤销、轮换、设置过期时间和 scope。创建接口使用 `Idempotency-Key`，轮换产生新 Token 并原子撤销旧 Token。所有变更和授权拒绝写审计日志，日志只包含 token ID/前缀。

## 4. 连接协议与稳定性

### 4.1 心跳和租约

- Agent/Client 与 Server 默认每 30 秒发送协议 `PING`，Server 回 `PONG`。
- 连续 3 个心跳周期无响应，连接状态置为 `degraded`，随后主动关闭并按指数退避 + jitter 重连。
- Agent metadata lease 由心跳刷新，不改变 metadata revision；旧 epoch 不能续租新 epoch。
- Server-node 使用独立租约周期和 epoch fencing。
- TCP socket 启用操作系统 keepalive；UDP association 使用 idle timeout，并支持协议级 keepalive。
- 正常关闭先发送 GOAWAY，等待受限 drain；超时后强制关闭。

### 4.2 连接阶段诊断

每条长连接维护以下阶段：

```text
resolve -> tcp_connect -> tls_handshake -> websocket_upgrade
-> token_auth -> agent_hello -> heartbeat -> logical_stream
```

每阶段记录 `trace_id`、`connection_id`、`agent_id`、`token_id`、开始/结束时间、耗时、错误分类和重试次数。日志和 API 不返回目标服务响应内容。

### 4.3 主动网络探针

诊断 API 支持 Server → Agent → 目标地址的 TCP connect、HTTP HEAD/GET 和 UDP echo。探针必须经过同一 Agent Policy，默认不使用需要 root 权限的 ICMP。探针结果只返回状态码、耗时、错误分类和时间戳；不返回业务响应正文。

## 5. 运行指标与后台展示

### 5.1 实时状态

Agent 详情页展示：在线状态、版本、系统、架构、节点、启动时间、连接时长、Token 状态、epoch、最后心跳、重连原因、活跃流数、总字节、当前带宽、控制 RTT、目标探针 RTT、心跳成功率和最近错误分类。

状态枚举为 `online`、`degraded`、`offline`、`stale`。质量页同时展示原始指标，不用单一分数掩盖异常：

- heartbeat availability；
- control RTT P50/P95；
- reconnect rate；
- probe success rate；
- stream error rate；
- inbound/outbound bps 和峰值。

### 5.2 存储和指标

- Server 内存保存短窗口统计。
- 每 60 秒写一条 `agent_runtime_stats` 聚合快照，默认保留 7 天，支持配置。
- 快照包含 `node_id + agent_id + epoch`，旧 epoch 不能覆盖新连接。
- Prometheus 指标提供长期监控来源；数据库仍是管理 API 的权威来源。
- 不保存业务 payload、SSH 内容、Token 明文或敏感 metadata。

### 5.3 管理接口

```text
GET  /api/v1/agents/{id}/runtime
GET  /api/v1/agents/{id}/metrics?range=5m
GET  /api/v1/agents/{id}/probes
POST /api/v1/agents/{id}/diagnose
```

接口仅允许 Agent owner/admin 访问；诊断请求必须再次经过 Agent Policy。

## 6. 日志、指标和进程保活

### 6.1 结构化日志

JSON 日志字段统一包含时间、级别、组件、事件名、trace_id、connection_id、agent_id、node_id、token_id、epoch、错误分类和耗时。关键事件包括启动、配置来源、schema 初始化、Token 管理、连接重连、探针失败、目标不可达、Server-node 租约、GOAWAY 和优雅退出。

严禁记录 Token、密码、私钥、完整硬件指纹、metadata 敏感值、SSH 字节和业务 payload。日志级别支持 `error`、`warn`、`info`、`debug`，生产默认 `info`。

### 6.2 进程生命周期

程序响应 SIGTERM/CTRL_BREAK：停止接受新连接、发送 GOAWAY、等待有限时间、关闭数据库和 listener。进程不自行 fork 或后台化：

- Linux 使用 systemd `Restart=on-failure`、Watchdog 和 `ExecStartPre` 配置校验；
- macOS 使用 launchd `KeepAlive` 和标准输出/错误日志路径；
- Windows 使用 Windows Service recovery actions；
- Docker/Compose 使用非 root、healthcheck 和 `restart: unless-stopped`。

## 7. 多平台发布

使用 Makefile + GoReleaser 构建三个二进制：

```text
make release VERSION=v0.1.0
```

目标为 Linux `amd64/arm64`、macOS `amd64/arm64`、Windows `amd64/arm64`。每个平台输出归档包、SHA256 校验文件、版本/commit/build time 信息；构建默认 `CGO_ENABLED=0`。Token、配置和数据库不进入发行物。

文档覆盖安装、首次启动、配置文件、升级、回滚、Token 轮换、日志目录和卸载；Docker 仍提供 server/agent/client 三种镜像。

## 8. Nginx 部署约束

Nginx 必须分别配置 `/api/`、`/`、`/ws/agent` 和 `/ws/client`。WebSocket location 使用 HTTP/1.1、Upgrade/Connection、Authorization 和 `X-Forwarded-*` 透传，关闭 buffering，`proxy_read_timeout`/`proxy_send_timeout` 大于心跳周期。80 端口只做 HTTPS 重定向；不得把 Token 拼到 URL；泛域名只由 Server 解析。

## 9. 失败处理与兼容性

- 数据库不可用：管理 API 进入 not-ready；已有数据流按已有连接策略运行，禁止伪造授权结果。
- Token 服务错误：拒绝新连接，保留已有连接直到租约/心跳失败。
- 探针失败：只标记质量和错误分类，不自动修改用户 Policy。
- 统计写入失败：保留内存窗口并告警，不阻断转发主链路。
- 新增协议帧和 API 必须版本化；旧 Agent 对未知诊断帧应安全忽略，不得关闭正常数据流。

## 10. 验收与测试

### 安全

- Token 类型、owner、scope、过期、撤销、轮换和幂等测试；
- 管理 API 权限、审计和明文只返回一次测试；
- Agent/Client/Server-node 错误 Token 互拒测试；
- 每条 logical stream 重复执行 Policy 测试；
- TLS/WSS、Origin、Authorization 透传和 query token 拒绝测试。

### 稳定性

- PING/PONG、三次心跳超时、重连退避和 epoch fencing 测试；
- TCP/UDP/HTTP 探针成功、超时、拒绝和不泄露响应正文测试；
- 带宽、RTT、P95、重连和活跃流聚合测试；
- Server 重启、数据库短暂不可用、网络抖动和优雅退出 E2E 测试；
- 结构化日志脱敏、trace 字段和错误分类测试。

### 发布

- Linux/macOS/Windows 交叉构建和归档校验；
- Nginx 配置语法和 WebSocket Upgrade 冒烟测试；
- systemd/launchd/Windows Service/Docker 文档命令可验证；
- `go test ./...`、`go test -race ./...`、`go vet ./...`、前端测试/build、`git diff --check`。
