# PR：逻辑 Agent 连接池

## 变更摘要

- 协议 hello/ack 增加 `instance_id`、`connection_id` 和连接池能力协商。
- Agent 生成并持久化稳定 `instance_id`，支持 `connections.min/max` 自适应连接池；默认仍为 `min=1,max=1`。
- Server 会话管理从单 Agent 会话改为按 `(agent_id, connection_id)` 管理多连接，流按 connection epoch 和 stream ID 隔离并固定归属。
- 数据库和 etcd 注册表支持连接级租约与列表。
- 路由选择支持本地健康连接优先、最少活跃流和远端连接候选。
- metadata 按 `(agent_id, instance_id)` 独立保存、续约和标记 stale；API 兼容旧顶层字段并新增 instances/connections。
- 新增连接池指标、连接生命周期审计事件和 Agent 详情实例/连接表格。
- 更新 v6→v7 迁移、运维指南、用户指南和 OpenAPI。

## 数据库变更

Schema v7 新增：

- `agent_connection_leases`
- `agent_instance_metadata`

v6 数据分别从 `agent_runtime_leases`、`agent_runtime_metadata` 回填。旧表保留用于应用回滚，新装数据库只创建 v7 表。这样避免在 MySQL 5.6 上重建大主键，并兼容默认 767-byte 索引前缀限制。

## 与已批准设计的差异

1. 连接租约没有修改 `agent_runtime_leases` 主键，而是新增 `agent_connection_leases` 并迁移旧数据。原因是降低 MySQL 5.6 的 DDL/锁风险，并保留更安全的回滚路径。
2. Agent metadata 没有修改旧表，而是新增 `agent_instance_metadata` 并迁移旧数据，原因同上。
3. Agent 连接参数校验仅在配置了 `agent.server_url` 时启用，避免 server/client 配置中的空 Agent 字段被误判。
4. Agent 队列压力目前是控制器统计结构中的信号，但尚未从真实 Session 发送 backpressure 采样；实际扩容主要依赖活跃流数、RTT 和 Server 能力确认。
5. Remote relay 选择目标包含 `server_node_id/server_epoch`，运行时也支持注入远程 transport，但当前不会从配置自动拨号其它 Server 节点。
6. UI 显示实例与连接的健康状态、活跃流、心跳、Server 节点等当前可得字段；RTT 和队列压力暂未进入管理 API 响应。
7. 错误历史已纳入控制器统计结构，但尚未参与扩缩容决策。

## 已验证

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
npm --prefix web test -- --run
npm --prefix web run build
rsync -a --delete web/dist/ internal/server/web_dist/
./scripts/verify-web-embed.sh
```

全部通过。前端构建存在 Vite chunk 大于 500 KiB 的既有警告，未阻塞构建；可另行做代码拆分优化。

## 回滚

- 停止新版 Server/Agent。
- 恢复升级前应用版本，保留 v7 新表；旧应用继续访问 v6 表。
- 如需精确回滚 v7 期间产生的连接/实例数据，使用升级前备份恢复数据库。
- `max>1` 回滚后 Agent 应恢复 `max=1`。

## 审核重点

- v6→v7 MySQL/SQLite 迁移可重试性。
- 多连接 epoch fencing 和 stale-generation fail-closed。
- instance metadata 的独立续约与 stale 标记。
- token owner、Agent enabled 状态和 target policy 不因连接池放宽。
- Prometheus label 基数控制。
