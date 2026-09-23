# 代理节点在线状态与空元数据修复实施计划

状态：已确认并实现（用户确认两列方案）

## 1. 现象

1. 代理节点列表把**从未连接过**的 agent 显示为“在线”。
2. 进入该 agent 详情页报“元数据加载失败”，元数据区域为空，只能点“重试”。

## 2. 根因

### 根因 A：列表的“在线”其实是 enabled 开关

- `web/src/views/Agents.vue:10` 的状态列渲染 `scope.row.enabled ? t('agents.active') : t('agents.disabled')`。
- `agents.active` 的中文文案是“在线”（`web/src/i18n/messages/zh-CN.ts:187`），英文为 “Active”。
- 后端 `publicAgent`（`internal/server/api.go:1750`）只返回 `id/name/ownerUserId/capabilities/enabled/createdAt/updatedAt`，**没有任何连通性字段**。
- 因此“在线”标签的真实语义是“该 agent 记录未被禁用”。控制台新建的 agent 默认 `enabled=true`，从未连接也显示“在线”。

### 根因 B：从未上报元数据的 agent 被当成 404

- 详情页调用 `GET /api/v1/agents/{id}/metadata?includeStale=true`（`web/src/views/AgentDetail.vue:275`）。
- `handleAgentMetadata`（`internal/server/api.go:659`）→ `GetAgentMetadataView` → `AgentMetadataService.GetView` → `repo.Get`；`agent_instance_metadata` 中没有该 agent 的行时，`scanAgentMetadata` 返回 `sql.ErrNoRows`（`internal/storage/repository.go:1540`、`:1607`）。
- `writeStorageError` 把 `sql.ErrNoRows` 映射为 404（`internal/server/api.go:1875`）。
- 前端 `load()` 的 catch 置 `error=true`，`DataState` 显示错误横幅“元数据加载失败”；为“暂无元数据”设计的空态（`web/src/views/AgentDetail.vue:56`）永远走不到。
- 404 同时承担了两个语义：“agent 不存在”（正确的 404）与“agent 存在但从未上报”（一个合法的空集合）。

## 3. 目标

1. 列表状态列反映真实连通性：存在未过期的 `agent_connection_leases` 租约 → 在线，否则 → 离线。
2. enabled/disabled 作为独立语义展示，不再冒充连通性。
3. 存在但从未上报元数据的 agent，元数据接口返回 200 空集合，前端走空态而非错误横幅。
4. “agent 不存在”与“stale 且未带 includeStale”仍返回 404，授权语义不变。

## 4. 非目标

- 不改租约、心跳、epoch 与连接关闭机制。
- 不改详情页的连接列表与访问策略区逻辑。
- 不引入新配置项。
- 列表不提供每 agent 的连接明细（详情页已有 `/agents/{id}/connections`）。

## 5. 设计决策

### 决策 1：连通性以 `agent_connection_leases` 的未过期租约为唯一权威

Dashboard 的 `agentsOnline` 已经使用该事实来源（`internal/storage/dashboard_repository.go:38`：`SELECT COUNT(DISTINCT l.agent_id) FROM agent_connection_leases ... WHERE l.expires_at>?`）。列表复用同一口径，避免“本节点进程内 session”与“集群租约”两套互相矛盾的在线判定。

### 决策 2：批量查询且严格限定在当前分页内

`LeaseRepository` 新增 `ListActiveAgentIDs(ctx context.Context, agentIDs []string) ([]string, error)`，实现为单条 `SELECT DISTINCT agent_id FROM agent_connection_leases WHERE expires_at>? AND agent_id IN (...)`。

- 只查当前页的 agent ID：owner 过滤已在 `listAgentsForOwner` 完成，批量查询不扩大可见范围，符合“用户资源查询必须在分页语义内完成权限过滤”。
- 空 ID 列表直接返回 nil，不产生 SQL。
- 单条查询避免 N+1；SQLite 与 MySQL 语法一致。

### 决策 3：`status` 与 `enabled` 并存，前端分列展示

`publicAgent` 的列表输出增加 `status: "online" | "offline"`，`enabled` 保留原义。前端状态列渲染 在线/离线 标签，另加“启用”列渲染 启用/已禁用 标签；创建弹窗开关文案同步改为 启用/已禁用。i18n 增加 `agents.online` / `agents.offline`，`agents.active` 文案改为“启用”/“Enabled”。

### 决策 4：元数据缺失返回 200 空视图

`handleAgentMetadata` 在 `errors.Is(err, sql.ErrNoRows)` 时返回 200，body 为合法空信封：`items: []`、`instances: []`、`stale: false`、`agentId` 回填。

- 授权仍先于该判定（先 `GetAgent` 再做 owner/admin 校验），调用者无法用空/404 差异探测他人资源。
- “stale 且未带 includeStale” 仍返回 404，保持既有契约。
- OpenAPI 的 404 描述同步收窄为 “Agent not found, or snapshot is stale without includeStale=true”。

## 6. 文件清单

- `internal/storage/repository.go`：`LeaseRepository` 增加 `ListActiveAgentIDs`；`leaseRepo` 实现。
- `internal/storage/storage_contract_test.go`：两种驱动下验证空入参不查询、未过期过滤、IN 过滤。
- `internal/server/api.go`：`listAgents` 组装 `status`；`handleAgentMetadata` 增加空视图分支。
- `internal/server/metadata_api_test.go`、`internal/server/api_test.go`：列表 status 与空元数据 200 的测试。
- `web/src/views/Agents.vue`：状态列改读 `status`，新增“启用”列。
- `web/src/i18n/messages/zh-CN.ts`、`web/src/i18n/messages/en-US.ts`：新增 `agents.online/offline`，`agents.active` 文案改为启用语义。
- `web/src/tests/agents-view.spec.ts`（新增）：列表按 `status` 渲染在线/离线，且 `enabled=true,status=offline` 不再显示“在线”。
- `web/src/tests/agent-detail.spec.ts`：空元数据信封走空态、无错误横幅。
- `docs/api/openapi.yaml`：agents 列表响应补 `status` 字段说明；metadata 的 404 描述收窄。
- 文档：`docs/user-guide/server-admin.md` 的 Agent 列表与详情小节、`docs/operations/troubleshooting.md` 增排障条目；本计划与 PR 记录；重新生成索引。
- 前端产物：`npm run build` 后同步 `internal/server/web_dist/` 并执行 `scripts/verify-web-embed.sh`。

## 7. TDD 步骤

红灯测试：

1. `TestListAgentsReportsConnectivityStatus`：创建 agent 且不建租约 → `status=offline`；插入未过期租约 → `online`；把租约 `expires_at` 改到过去 → `offline`。修复前响应没有 `status` 字段，断言失败。
2. `TestAgentMetadataReturnsEmptyViewWhenNeverReported`：agent 存在、`agent_instance_metadata` 无行、`includeStale=true` → 200 且 `items=[]`、`instances=[]`。修复前 404。
3. `web/src/tests/agents-view.spec.ts`：mock 列表返回 `{enabled:true,status:"offline"}` → 渲染“离线”与“启用”；`status:"online"` → 渲染“在线”。修复前渲染“在线”。
4. `web/src/tests/agent-detail.spec.ts`：metadata 返回 200 空信封 → 显示“暂无元数据”，无错误横幅。修复前（404）显示横幅。

说明：测试 3、4 在实现前即通过（前端用 mock 的 API 响应渲染），它们是把“空元数据走空态、连通性与启用分列”固定下来的护栏；本轮的红灯证据由后端两例承担，即列表缺 `status` 字段与元数据 404。

最小实现：决策 1–4，不做超出目标的重构。

绿灯判据：上述测试通过；`internal/server`、`internal/storage` 既有测试全绿；前端 `npm test -- --run` 全绿且构建成功、嵌入校验通过。

## 8. 验证命令

```bash
go build ./...
go test ./internal/server ./internal/storage -count=1
go test ./... -count=1
go test -race ./internal/server ./internal/storage -count=1
go vet ./...
cd web && npm test -- --run && npm run build
rsync -a --delete web/dist/ internal/server/web_dist/ && ./scripts/verify-web-embed.sh
git diff --check
```

结果：`go build ./...` 通过；`go test ./internal/server ./internal/storage -count=1` 通过（server 47.3s、storage 2.1s）；`go test ./... -count=1` 无失败包；`go test -race ./internal/server ./internal/storage -count=1` 通过（server 459.0s、storage 11.9s）；`go vet ./...` 无输出；前端 `npm test -- --run` 34 文件 / 290 用例通过；`npm run build` 成功且 `web/dist and internal/server/web_dist match`；`git diff --check` 无输出。

## 9. 发布与回滚注意事项

- Server 二进制与嵌入前端一起发布；Agent 与 Client 不需要升级，无 Schema 与配置变更。
- 回滚即回退 Server 二进制与嵌入产物；回滚后列表恢复 enabled 语义、详情页恢复 404 错误横幅。
- 行为变化需知会使用者：列表“在线/离线”现在表示连通性（未过期租约），启用状态单独成列；从未连接的 agent 详情页显示“暂无元数据”空态而不是加载失败。
- 灰度建议：先在一个节点核对列表在线数与 Dashboard 的 `agentsOnline` 一致，再全量。
