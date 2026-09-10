# 逻辑 Agent 连接池运维指南

本文说明从 Schema v6 升级到 v7 后，如何启用和运维“多物理连接、一个逻辑 Agent”的连接池。默认配置保持 `min=1,max=1`，行为与旧版单连接 Agent 一致。

## 身份模型

- `agent_id`：逻辑 Agent，路由、token、RBAC 和管理页面继续使用这个 ID。
- `instance_id`：一个物理 Agent 进程或部署节点的稳定身份；未显式配置时 Agent 会生成并持久化，Linux 打包部署默认路径为 `/var/lib/tunnelmesh-agent/agent-instance-id`。
- `connection_id`：每次成功建立的 WebSocket 连接；一个 instance 可以有多个 connection。
- `server_node_id`：接收该 WebSocket 的 Server 节点。

同一个逻辑 Agent 可以由多个 Agent instance 使用同一个 Agent token 建立连接。token 仍必须绑定该 Agent 并属于同一个 owner；Server 会为每个连接独立认证和审计，不保存明文 token。

## Agent 配置

默认不扩容：

```yaml
agent:
  server_url: wss://tunnel.example.com/ws/agent/v1
  id: agent-devbox
  connections:
    min: 1
    max: 1
```

灰度开启：

```yaml
agent:
  connections:
    min: 1
    max: 8
    high_watermark: 16
    low_watermark: 2
    evaluation_interval: 10s
    cooldown: 30s
```

Agent 只使用一个 `server_url`。多个 WebSocket 都连接该 URL；多 `server_urls` 不属于当前实现。

## Server 节点与共享令牌

`server.relay.node_token` 是 Server 节点服务令牌的唯一配置字段。管理员可以在 Tokens 页面创建：

- **fleet token**：`scope.serverNodeIds` 留空，所有启用、未逻辑删除的 Server 节点都可使用；
- **显式允许列表**：选择一个或多个 Server 节点，只有列表内节点可以使用。

共享 token 只解决凭据分发，不降低授权边界。mTLS 模式下每个 Server 使用独立证书，证书 SAN 必须精确包含自己的 `node.id`，并包含所有节点共同的 `server_name`；数据库中的节点 epoch 也必须与调用方一致。禁用或逻辑删除 Server 后，即使 token 和证书仍有效，relay 认证也会拒绝。

Server 启动时会把自身注册到 `server_nodes`，并每 30 秒更新一次心跳，租约有效期为 90 秒。没有 Agent 连接的节点也会显示 online/offline；有连接时，管理页额外汇总活跃连接、活跃流和健康分。

`node.id` 缺失时由 `run` 自动生成并回写 YAML。systemd 会在非特权服务启动前以 root 执行 `init-node-id`，因此 mTLS 证书签发顺序是：安装配置 → 初始化 `node.id` → 按 `node.id` 和共同 `server_name` 签发 relay 证书 → 配置共享 token 和 mTLS → 启动。证书生成步骤见 [Relay mTLS 证书生成与配置](relay-mtls.md)。

## 升级与发布

1. 备份 MySQL/SQLite 数据库，确认备份可恢复。
2. 停止或滚动升级 Server；`storage.auto_init=true` 时会按顺序执行 v6→v7 增量迁移。
3. 保持 Agent `connections.max=1` 并升级 Agent。
4. 验证 `/health/ready`、管理页 Agent 详情、metadata、Tunnel 和 route。
5. 确认所有 Server 节点都支持连接池后再逐步提高 Agent `max`。
6. 每批只放大少量 Agent，观察连接数、stream 错误和 relay 选择指标。

Schema v7 新增 `agent_connection_leases` 和 `agent_instance_metadata`。迁移会从 v6 的 `agent_runtime_leases` 和 `agent_runtime_metadata` 回填旧数据，旧表保留用于回滚；新装数据库不会创建旧表。该方案避免重建 MySQL 5.6 大主键，索引键长度兼容默认 767-byte 前缀限制。

迁移预计只会创建新表、新索引并复制现有行；表大小决定复制时间。失败时 `schema_meta.version` 不会推进，MySQL DDL 隐式提交后可按日志检查已完成对象并重试。不要手工把版本改成 7。

## 回滚

- 应用回滚到支持 v6 的版本时，保留 v7 新表和旧表；不要删除新表。
- 新版代码回写的新连接/新实例只存在于 v7 表，旧版会回到 `agent_runtime_*` 数据。
- 若业务已经依赖 v7 数据且需要精确回滚，使用升级前备份恢复，而不是自动生成反向 DDL。
- 回滚前停止写入并保留 5 分钟内可执行的备份恢复步骤。

## Schema v8 升级与回滚

v8 为 `server_nodes` 增加 `name`、`enabled` 和 `deleted_at`，用于后台逻辑管理。升级前：

1. 备份 MySQL/SQLite，并验证备份可恢复。
2. 确认当前 `schema_meta.version=7`，且 `migrations/incremental/v0007_to_v0008/` 中对应驱动脚本存在。
3. 确认数据库账号有 `ALTER TABLE` 和 `UPDATE` 权限；MySQL 5.6 需保持默认 767-byte 索引前缀限制。
4. 停止或滚动停止 Server 写入，避免迁移期间同时更新节点心跳。

`storage.auto_init=true` 时会执行增量脚本并推进到 v8。脚本会为存量节点回填 `name=id`、`enabled=1`，`deleted_at` 保持空。ALTER TABLE 可能短暂持有元数据锁，预计锁表时间取决于 `server_nodes` 行数；本表只保存 Server 清单，通常远小于 Agent 连接租约表。失败时版本不会提前推进；MySQL DDL 可能已隐式提交，按日志确认已添加的列后可重试。

回滚应用到 v7 时保留 v8 新增列，不执行反向 DDL。新版写入的 `name/enabled/deleted_at` 对旧版无影响。如果必须精确恢复 Schema，使用升级前备份；不要手工删除列或修改 `schema_meta.version`。

## 监控

新增 Prometheus 指标：

```text
tunnelmesh_agent_connections
tunnelmesh_agent_connection_capacity
tunnelmesh_agent_active_streams
tunnelmesh_agent_connection_rtt_seconds
tunnelmesh_agent_connection_errors_total
tunnelmesh_agent_connection_scale_decisions_total
tunnelmesh_agent_selection_total
```

常用告警：

- 连接全部为 0：逻辑 Agent 离线。
- `connection_errors_total` 快速增长：认证失败、网络抖动或节点故障。
- `active_streams` 接近高水位且 `scale_decisions_total{decision="scale-up"}` 无增长：已达 max 或 Server 未确认能力。
- `selection_total{scope="remote"}` 突增：本地连接不可用或流量集中在其他 Server 节点。

## 跨 Server 连接查询与关闭

集群内每个 Server 在接受 Agent WebSocket 时都会注册一条 `agent_connection_leases` 记录，并通过心跳续期和更新活跃流统计。管理 API 汇总这些租约，并把当前节点能看到的本地会话状态覆盖到对应行，因此可以看到逻辑 Agent 在多个 Server 上的物理连接。

### 前置条件

- 每个 Server 配置唯一且稳定的 `node.id`；不要让两个进程使用同一个 ID。
- 启用 `server.relay.enabled`，并配置 server-node token。
- 生产环境配置同一 CA 签发的 mTLS 证书、私钥和 `server_name`；受控内网可省略这些证书字段运行明文模式。
- `server.relay.endpoint` 可以留空并自动推导，也可以显式配置为其他 Server 节点可路由的地址。连接租约会保存该地址，远端关闭通过它发起 authenticated relay 控制调用。
- 所有参与节点都必须运行支持连接租约和 relay close RPC 的版本；旧节点上的连接不会出现在完整集群视图中。
- 集群节点时间同步，否则租约到期和 epoch 判断可能异常。

### API 示例

查询一个逻辑 Agent 的所有物理连接：

```bash
curl -sS \
  -H 'Authorization: Bearer <management-token>' \
  'https://tunnel.example.com/api/v1/agents/agent-devbox/connections'
```

关闭列表中某一条精确连接：

```bash
curl -sS -X DELETE \
  -H 'Authorization: Bearer <management-token>' \
  'https://tunnel.example.com/api/v1/agents/agent-devbox/connections/conn-1?connectionEpoch=7'
```

`connectionEpoch` 是连接列表返回的数据库租约 epoch，不是 Agent 协议里的本地连接 epoch。拥有该 WebSocket 的 Server 会在本地把租约 epoch 解析为当前会话 epoch，再发送 `GOAWAY` 并关闭传输。

### 语义与止损

- 关闭一条物理连接不会禁用逻辑 Agent，也不会阻止该 Agent 自动重连；长期停止访问应禁用 Agent 或撤销 token。
- 精确连接已经消失时，删除请求幂等成功。
- 连接 ID 被替换但传入旧 epoch 时返回 `409`，不会误关新连接。
- 远端 owner Server 不可达时返回 `503`，数据库租约保留。不要手工删除租约；恢复 relay 后刷新列表并重试。
- 管理端成功、失败和 stale epoch 尝试都会写审计日志，只记录连接身份、epoch、节点、负载和错误类别，不记录 token 或目标地址。

### 发布顺序

1. 备份数据库并确认 `schema_meta.version` 已到 v7。
2. 滚动升级所有 Server，保持 Agent `connections.max=1`。
3. 验证每个节点 `node.id` 唯一、relay mTLS 可互通、`server.relay.endpoint` 可从其他节点访问。
4. 在管理页确认所有 Agent 连接都可见，再逐步放大 Agent 连接数。

## 当前限制

- 流固定在其打开时的 connection 上，不做在线迁移。
- Agent 队列压力信号尚未从真实发送 backpressure 中采样，控制器目前主要使用活跃流数、RTT 和 Server 能力确认。
- Remote relay transport 由运行时注入；当前版本不会从配置自动拨号其它 Server 节点。默认选择器优先本地连接，本地无健康连接时会查询连接注册表。
- `max=1` 时不需要所有 Server 同时升级；`max>1` 前必须完成所有入口节点的滚动升级。
