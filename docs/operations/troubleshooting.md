# 故障排查

## `admin account does not exist`

该错误表示当前配置连接的数据库中没有 `role=admin` 的用户。新库首次初始化应执行：

```bash
tunnelmesh-server --config /etc/tunnelmesh/server.yaml admin bootstrap
```

管理员已存在但凭据遗失时才使用 `admin regenerate-credentials --confirm`。如果预期管理员已经存在，请先用 `print-config` 核对当前命令与 systemd 服务是否使用同一配置文件和数据库；不要反复执行 bootstrap 掩盖连错数据库的问题。

## 启动失败

先执行：

```bash
tunnelmesh-server --config tunnelmesh.yaml check-config
tunnelmesh-server --config tunnelmesh.yaml print-config
```

`print-config` 输出的是脱敏后的有效配置。重点检查 mode、storage.driver、MySQL DSN、TLS、node.id 和 registry.type。

服务日志位置和 systemd/Docker/macOS/Windows 查看命令见 [日志位置与查看方式](logging.md)。

## schema 错误

- 自动初始化关闭时，确认数据库已经执行 `migrations/ddl.sql`。
- 检查 `schema_meta` 版本是否与当前程序一致。
- SQLite 检查挂载目录是否可写；容器中通常是 `/var/lib/tunnelmesh`。
- MySQL 检查账号是否有建表、索引和事务权限。

### MySQL 5.6：`Error 1071 Specified key was too long`

MySQL 5.6 在默认 InnoDB 配置下通常只有 767 字节的索引前缀上限。旧版
DDL 中的 `utf8mb4 VARCHAR(255)` 主键或联合索引可能超过该上限；如果不能
修改 MySQL 服务参数（例如 `innodb_large_prefix`），请使用包含字节长度受控
键列的新版 `tunnelmesh-server`，其 DDL 不依赖 `innodb_large_prefix`。

首次初始化失败后，先停止服务并确认该数据库没有需要保留的数据：

```bash
systemctl stop tunnelmesh-server
mysql -h 10.228.128.81 -P 4963 -u '<user>' -p tunnelmesh \\
  -e "SHOW TABLES;"
```

如果只是失败初始化留下的空库，备份后清理该 TunnelMesh 数据库（或创建一个
新的空数据库）再启动服务，让 `storage.auto_init: true` 重新执行新版 DDL。
已有业务数据时不要直接 `DROP DATABASE`；先做完整备份，并安排停机窗口执行
经过验证的表结构迁移。`CREATE TABLE IF NOT EXISTS` 不会自动把已有旧列改成
新的键类型。

不要在命令行、日志或工单中粘贴真实密码；此前已经暴露过的数据库凭据应立即
轮换，并通过 systemd EnvironmentFile 或 Secret Manager 注入 DSN。

## Agent 不在线

1. 确认 Agent 能访问 Server 的 `wss://` 地址。
2. 确认证书链、SNI 和系统时间正确。
3. 检查 Agent ID 是否稳定且没有重复注册。
4. 检查 Server 节点租约是否过期，以及集群节点时间是否同步。
5. 确认目标服务从 Agent 所在网络可达，而不是只在 Server 主机可达。

连接池部署中，Agent 详情应同时展示 instance 和 connection。一个 instance 离线不代表逻辑 Agent 离线；只要还有健康连接，流量会选择剩余连接。若全部连接为 0，检查 Agent token、`instance_id` 文件权限和 `server_url`，并观察 `tunnelmesh_agent_connection_errors_total`。

全链路探针可通过 `POST /api/v1/agents/{agentId}/diagnose` 发起 TCP/HTTP/UDP 诊断，并通过 `GET /api/v1/agents/{agentId}/probes` 查看受限结果摘要；目标响应体不会被保存或返回。详见 [全链路网络探针](network-probes.md)。

## 集群连接查询或关闭失败

- 看不到远端连接：确认所有 Server 都已升级到支持连接租约的版本，`node.id` 唯一且稳定，`server.relay.enabled=true`，集群时间同步。
- 关闭返回 `503`：owner Server 的 relay 不可达。检查连接列表中的 Server 地址是否可从当前节点路由、mTLS 证书 SAN 是否精确匹配、CA 是否一致、server-node token 是否有效、节点 epoch 是否一致。数据库租约会保留，不要手工删除。
- 关闭返回 `409`：传入的 `connectionEpoch` 已过期，说明该连接 ID 被替换。刷新 `GET /api/v1/agents/{agentId}/connections`，使用新的 epoch 重试。
- 关闭成功后连接又出现：这是预期行为。关闭只断开一条物理 WebSocket，不会禁用 Agent 或阻止其按连接池策略重连；需要长期停止时禁用 Agent 或撤销 token。
- 查询返回 `503`：检查数据库连接注册表可用性和管理 API 日志；不要用本地 metadata 连接数推断集群状态。

## HTTP 路由失败

- 404：检查域名、pathPrefix、wildcard DNS 和 API 路由是否匹配。
- 401/403：检查 Token、角色和资源 owner。
- 409：域名和路径已存在，重用原配置或删除旧路由。
- 502/504：检查 Agent session、目标地址、目标端口和 policy。

## SSH / websocat 失败

- 必须使用 `websocat --binary`。
- 检查 ProxyCommand URL 是否使用 `wss://` 和正确的域名/端口编码。
- 看到 WebSocket 文本帧错误时，检查客户端是否误用了 text 模式。
- 使用 `ssh -vvv` 和 `websocat -v` 获取握手与关闭原因。

## Service token 失败

- 401：检查 token 是否为空、类型是否正确、是否已过期或已撤销；`agent` 连接 `/ws/agent`，`client` 连接 `/ws/client`。
- 403：检查 token owner、Agent 绑定、scope 与 Agent Policy 的交集。
- 409：轮换/撤销并发冲突时，复用同一个 `Idempotency-Key` 重试；不要期待再次返回 secret。
- Server-node relay 失败：检查 `server.relay.node_token` 是否有效、节点是否在 `scope.serverNodeIds` 允许列表内、节点是否被禁用或逻辑删除、epoch 是否一致，以及 `server.relay.listen` 是否已监听。mTLS 模式还需检查证书 SAN 是否精确匹配、共同 `server_name` 是否存在、CA 是否正确；明文模式需确认网络确实处于受控内网。

明文 secret 只在创建/轮换响应出现一次。不要从日志、审计记录或数据库恢复 token；遗失时直接轮换并撤销旧 token。

## 数据安全

排障日志中可以保留 trace ID、Agent ID、路由 ID 和错误码，但不能输出密码、Bearer Token、私钥或完整 DSN。

## 子账号与 Schema v6

- `username_invalid`：用户名需为 3–64 个 ASCII 字符，只允许字母、数字、`.`、`_`、`-`。
- `account_deleted`：已删除账号只能在管理员页面的“已删除”筛选中恢复；恢复不会改变原用户名或关联资源。
- `admin_account_protected`：管理员账号不能通过子账号接口禁用、重置、删除或恢复。
- 升级前确认 `schema_meta.version=5`、增量链完整且数据库账号可执行 `ALTER TABLE`/`CREATE INDEX`。脚本失败时不会推进版本；MySQL DDL 可能隐式提交，按日志修复前置状态后可安全重试。
- v6 的 `users.deleted_at` 是可空列，应用回滚时保留该列；不要直接删除列或复用已删除用户名。

## 子账号与 Schema v7 连接池

- 升级前确认 `schema_meta.version=6`、v6→v7 增量脚本存在且数据库账号可执行 `CREATE TABLE`/`CREATE INDEX`。
- v7 新增 `agent_connection_leases` 与 `agent_instance_metadata`；旧 `agent_runtime_leases`、`agent_runtime_metadata` 保留用于回滚，不要手工删除。
- 迁移失败时先查看具体 SQL 错误和已创建对象；MySQL DDL 可能已隐式提交，修复后可重试，版本号不会在失败前推进。
- 回滚应用可保留 v7 新表；如需精确回滚业务数据，使用升级前备份恢复。
- 连接池扩容发布顺序、指标和止血步骤见[逻辑 Agent 连接池运维指南](connection-pool.md)。

## Server 节点与 Schema v8

- Server 未出现在 `/servers` 页面：确认已完成 `node.id` 初始化并成功启动；自注册失败会在 journal 中输出 `register server node` 相关错误。
- 节点显示 offline：检查 Server 进程、数据库连通性、集群时间同步，以及 `last_seen_at/expires_at` 是否持续更新。
- relay 认证拒绝：同时检查共享 token、允许列表、节点启用状态、逻辑删除状态和 epoch；mTLS 模式还需检查 SAN。任意一项不满足都会失败。
- 升级前确认 `schema_meta.version=7`、v7→v8 增量脚本存在且数据库账号可执行 `ALTER TABLE`/`UPDATE`。
- v8 只为 `server_nodes` 增加 `name`、`enabled`、`deleted_at` 并回填名称；失败时版本不会提前推进，MySQL DDL 可能已隐式提交，确认已完成的列后可重试。
- 回滚应用时保留 v8 新增列；如需精确恢复 Schema，使用升级前备份，不要手工删除列或修改版本号。
