# Agent Policy 逻辑删除、恢复与编辑强化实施计划

## 目标

补齐 Agent Policy 的生命周期管理：管理员可以编辑、逻辑删除和恢复策略；逻辑删除后的策略立即退出授权判定，但数据保留，可重新置为有效。该方案同时保留并回归验证现有编辑能力。

## 根因与现状

- 后台 Agent 详情页已有“添加策略”和“编辑”按钮，线上静态资源中也包含 `edit-agent-policy` 逻辑；本次需要补充回归测试，防止部署或权限状态造成误判。
- 当前前端没有删除按钮，也没有 `deleteAgentPolicy` API 封装。
- 后端已有 `DELETE /api/v1/agents/{agentId}/policies/{policyId}`，但存储层执行的是物理 `DELETE FROM agent_policies`。
- 用户已明确全局约束：删除必须是逻辑删除，非物理删除，并支持重新置为有效。
- 因此不能直接把现有物理删除接口接到 UI；必须新增 `deleted_at` 字段、过滤逻辑、恢复 API 和后台入口。

## 架构决策

- `agent_policies.deleted_at` 为可空时间字段：`NULL` 表示有效，非 `NULL` 表示已删除。
- 授权链路只读取有效策略；逻辑删除后的策略不参与 `AuthorizeStream`。
- 默认策略列表只返回有效策略；管理员可通过 `status=all` 或 `status=deleted` 查看已删除策略并恢复。
- `DELETE` 保持原路径，但语义改为逻辑删除；新增 `POST /policies/{policyId}/restore` 恢复。
- 已删除策略不允许 PATCH 修改，必须先恢复，避免管理员误以为修改了仍在生效的策略。
- 普通用户保持只读；创建、编辑、删除、恢复均仅管理员可用，与服务端 RBAC 一致。
- 不引入新表，不改变 policy 字段含义，只做向后兼容的 Schema 扩展。

## 技术栈

- 后端：Go、database/sql、MySQL 5.6 兼容 DDL、SQLite 兼容 DDL。
- API：`/api/v1`，统一 envelope，管理员 RBAC 和审计日志。
- 前端：Vue 3、TypeScript、Element Plus、Pinia、Vue I18n。
- 测试：Go 单元/契约测试、Vitest + jsdom、Go embed 冒烟测试。

## 规格引用

- `AGENTS.md` 中 Schema 变更必须同时更新全量 DDL、增量 DDL、`SchemaVersion`、迁移测试和升级文档的要求。
- `docs/api/openapi.yaml` 中 Agent Policy API。
- `docs/user-guide/client.md` 中 Agent Policy 与 SOCKS5 通配策略说明。
- `docs/operations/schema-upgrades.md` 和 `docs/operations/troubleshooting.md` 中升级、失败重试和回滚要求。

## 全局约束

- 删除必须是逻辑删除，禁止物理删除。
- 已删除策略必须能恢复。
- 授权必须 fail closed：读取策略失败、策略状态异常或过滤不明确时拒绝。
- MySQL 与 SQLite 必须同时兼容。
- `SchemaVersion` 从 9 升级到 10，只维护相邻 v9→v10 增量脚本。
- 已发布 v8→v9 脚本不得修改。
- 前端中英文文案同步维护。
- 不提交密码、Token、DSN 或生产敏感数据。
- 未经用户明确授权，不执行 commit、push、merge。

## 精确文件清单

1. `migrations/ddl.sql`
   - `agent_policies` 增加 `deleted_at TEXT`。

2. `migrations/incremental/v0009_to_v0010/mysql.sql`
   - 新增 MySQL 增量脚本。

3. `migrations/incremental/v0009_to_v0010/sqlite.sql`
   - 新增 SQLite 增量脚本。

4. `migrations/embed.go`
   - 嵌入 v9→v10 MySQL/SQLite 脚本。

5. `internal/storage/db.go`
   - `SchemaVersion` 改为 10。
   - 迁移 switch 增加 case 9。

6. `internal/storage/models.go`
   - `AgentPolicy` 增加 `DeletedAt *time.Time`。

7. `internal/storage/repository.go`
   - `PolicyRepository` 增加状态过滤、逻辑删除、恢复接口。
   - `ListByAgent` 默认过滤 `deleted_at IS NULL`。
   - `Delete` 改为更新 `deleted_at`。
   - 新增 `Restore` 清空 `deleted_at`。
   - 读写列增加 `deleted_at`。

8. `internal/auth/credential_service.go`
   - 授权策略列表继续走默认有效策略过滤。
   - `policyAllows` 对 `DeletedAt != nil` 的记录 fail closed，作为纵深防御。

9. `internal/server/api.go`
   - `publicPolicy` 输出 `deletedAt`。
   - 列表支持 `status=active|deleted|all`，仅管理员可使用非 active。
   - PATCH 已删除策略返回 409。
   - DELETE 改为逻辑删除。
   - 新增 restore 路由和处理函数。
   - 审计动作增加 `policy.restored`。

10. `docs/api/openapi.yaml`
    - 更新 Policy 响应字段、状态过滤参数、DELETE 语义和 restore API。

11. `docs/user-guide/client.md`
    - 补充后台删除、恢复和编辑说明。

12. `docs/operations/schema-upgrades.md`
    - 增加 v9→v10 升级、锁表影响、失败重试和回滚说明。

13. `docs/operations/troubleshooting.md`
    - 增加逻辑删除策略不生效、恢复流程和版本不匹配排查。

14. `web/src/api/client.ts`
    - `AgentPolicy` 增加 `deletedAt`。
    - `listAgentPolicies` 支持 status。
    - 新增 `deleteAgentPolicy`。
    - 新增 `restoreAgentPolicy`。

15. `web/src/views/AgentDetail.vue`
    - 策略表格增加状态列。
    - 管理员可见编辑、删除、恢复。
    - 已删除策略不显示编辑和删除，显示恢复。
    - 删除使用确认弹窗。
    - 增加状态筛选：有效、已删除、全部；仅管理员可见。

16. `web/src/i18n/messages/zh-CN.ts`
    - 增加删除、恢复、状态、确认弹窗等中文文案。

17. `web/src/i18n/messages/en-US.ts`
    - 增加对应英文文案。

18. `internal/storage/storage_contract_test.go`
    - 覆盖逻辑删除、默认过滤、恢复、授权 revision 递增。

19. `internal/storage/sqlite_test.go`
    - 覆盖 SQLite v9→v10 迁移和 DDL 列存在性。

20. `internal/storage/mysql_test.go`
    - 覆盖 MySQL v9→v10 迁移和重试兼容。

21. `internal/auth/credential_service_test.go`
    - 覆盖删除后拒绝授权、恢复后重新允许。

22. `internal/server/api_test.go`
    - 覆盖管理员删除/恢复、普通用户禁止、已删除策略 PATCH 409、状态过滤。

23. `web/src/tests/agent-policy-api.spec.ts`
    - 覆盖 DELETE 和 restore API 封装。

24. `web/src/tests/agent-detail.spec.ts`
    - 覆盖编辑入口、删除确认、恢复入口和普通用户只读。

25. `internal/server/web_test.go`
    - 嵌入资源关键词增加删除/恢复相关文案。

26. `internal/server/web_dist/*`
    - 前端构建后同步最新生产产物。

## 任务间接口

### 存储接口

```go
type PolicyRepository interface {
    Create(ctx context.Context, v AgentPolicy) error
    Get(ctx context.Context, id string) (AgentPolicy, error)
    ListByAgent(ctx context.Context, agentID, cursor string, limit int) (Page[AgentPolicy], error)
    ListByAgentStatus(ctx context.Context, agentID, cursor string, limit int, status PolicyStatus) (Page[AgentPolicy], error)
    Update(ctx context.Context, v AgentPolicy) error
    Delete(ctx context.Context, id string) error
    Restore(ctx context.Context, id string) error
}
```

若希望避免接口膨胀，可以保留 `ListByAgent` 并新增可选参数结构；本计划采用显式 `ListByAgentStatus`，避免改变现有调用方签名。

### API 响应

```json
{
  "id": "policy-xxx",
  "agentId": "agent-xxx",
  "protocol": "tcp",
  "targetHost": "*",
  "targetPort": 0,
  "allowedCIDRs": [],
  "allowedPorts": [],
  "createdAt": "2026-09-11T00:00:00Z",
  "updatedAt": "2026-09-11T00:00:00Z",
  "deletedAt": null
}
```

### 前端 API

```ts
export function deleteAgentPolicy(agentId: string, policyId: string)
export function restoreAgentPolicy(agentId: string, policyId: string)
export function listAgentPolicies(
  agentId: string,
  params: { cursor?: string; limit?: number; status?: 'active' | 'deleted' | 'all' } = {},
)
```

## TDD 步骤

### 任务 1：Schema v10 与存储逻辑删除

1. 先修改测试：
   - `storage_contract_test.go` 断言：
     - `Delete` 后 `Get` 返回 `DeletedAt != nil`；
     - 默认 `ListByAgent` 不返回已删除策略；
     - `ListByAgentStatus(..., deleted)` 返回已删除策略；
     - `Restore` 后 `DeletedAt == nil` 且默认列表重新返回；
     - Delete 和 Restore 都递增 authorization revision。
   - `sqlite_test.go` 和 `mysql_test.go` 断言 v9 数据库自动迁移到 v10 后 `agent_policies.deleted_at` 存在。

2. 运行：

```bash
go test ./internal/storage -run 'Test.*Policy.*|Test.*v0009|Test.*SchemaVersion' -count=1
```

预期失败：字段、方法和 v10 迁移不存在。

3. 最小实现：
   - 增加 v10 增量脚本。
   - 更新全量 DDL 和 embed。
   - `SchemaVersion=10`。
   - 模型和 repository 增加逻辑删除。

MySQL 增量脚本：

```sql
ALTER TABLE agent_policies ADD COLUMN deleted_at TEXT;
```

SQLite 增量脚本：

```sql
ALTER TABLE agent_policies ADD COLUMN deleted_at TEXT;
```

迁移 runner 已从 v6 起容忍 duplicate object/column 错误，因此 MySQL DDL 部分成功后重试是安全的。

4. 重新运行聚焦测试，预期通过。

### 任务 2：授权过滤与 fail closed

1. 先修改 `credential_service_test.go`：
   - 创建允许 `10.0.0.1:22` 的策略；
   - 断言授权通过；
   - 逻辑删除策略；
   - 断言同一请求返回 `ErrForbidden`；
   - 恢复策略；
   - 断言授权重新通过。

2. 运行：

```bash
go test ./internal/auth -run 'TestAgentPolicy.*Delete|TestAgentPolicy.*Restore' -count=1
```

预期失败：测试辅助或策略状态字段不存在。

3. 最小实现：
   - 授权列表只取 active。
   - `policyAllows` 遇到 `DeletedAt != nil` 返回 `ErrForbidden`。

4. 聚焦测试通过。

### 任务 3：管理 API 生命周期

1. 先修改 `api_test.go`：
   - 管理员创建策略；
   - DELETE 后响应包含 `deletedAt`；
   - 默认列表不包含该策略；
   - `status=deleted` 列表包含该策略；
   - PATCH 已删除策略返回 409；
   - restore 后默认列表重新包含；
   - 普通用户 DELETE/restore 返回 403；
   - 普通用户请求 `status=deleted` 返回 403；
   - 审计日志包含 `policy.deleted` 和 `policy.restored`。

2. 运行：

```bash
go test ./internal/server -run 'TestAPIAgentPolicy.*Delete|TestAPIAgentPolicy.*Restore|TestAPIAgentPolicy.*Status' -count=1
```

预期失败：restore 路由、状态过滤和 409 语义不存在。

3. 最小实现：
   - API 路由增加 restore。
   - DELETE 调用逻辑删除。
   - PATCH 检查 `DeletedAt`。
   - 列表解析并校验 status。
   - `publicPolicy` 输出 `deletedAt`。

4. 聚焦测试通过。

### 任务 4：前端 API 与页面入口

1. 先修改 `agent-policy-api.spec.ts`：
   - DELETE 请求 `DELETE /agents/{id}/policies/{policyId}`；
   - restore 请求 `POST /agents/{id}/policies/{policyId}/restore`；
   - status 查询拼装 `?status=deleted`。

2. 运行：

```bash
cd web
npm test -- --run src/tests/agent-policy-api.spec.ts
```

预期失败：函数不存在。

3. 实现 API 封装。

4. 修改 `agent-detail.spec.ts`：
   - 有效策略显示编辑和删除；
   - 点击删除出现确认弹窗；
   - 确认后调用 `deleteAgentPolicy` 并刷新列表；
   - 已删除策略显示恢复，不显示编辑/删除；
   - 点击恢复调用 `restoreAgentPolicy` 并刷新；
   - 普通用户看不到删除/恢复；
   - 管理员编辑弹窗可打开并保存。

5. 运行：

```bash
cd web
npm test -- --run src/tests/agent-detail.spec.ts
```

预期失败：页面缺少状态列、删除/恢复控件和确认流程。

6. 实现页面、i18n 和状态筛选。

7. 聚焦测试通过。

### 任务 5：文档、OpenAPI 与嵌入资源

1. 更新 OpenAPI：
   - `Policy` 响应增加 `deletedAt`。
   - 列表增加 `status` 参数。
   - DELETE 描述为逻辑删除。
   - 新增 restore path。

2. 更新用户文档和运维文档：
   - 后台操作步骤；
   - v9→v10 升级前置检查；
   - MySQL DDL 失败重试；
   - 回滚保留新增列；
   - 逻辑删除策略不参与授权。

3. 更新 `web_test.go` 关键词。

4. 构建并同步：

```bash
cd web
npm test -- --run
npm run build
cd ..
rsync -a --delete web/dist/ internal/server/web_dist/
go test ./internal/server -run TestEmbeddedWebDist -count=1
```

## 最小实现

- 只实现逻辑删除、恢复、状态过滤和编辑回归，不做批量删除、权限委托或策略版本历史。
- 不改变 Token scope 语义。
- 不删除任何现有 API 路径。
- 不修改已发布迁移。
- 不为 `deleted_at` 创建新索引；现有 `idx_agent_policies_agent` 足够支撑当前数据规模和过滤语义。

## 预期通过结果

1. 管理员可以编辑有效策略。
2. 管理员可以删除策略；删除后策略立即不参与授权。
3. 默认列表只显示有效策略。
4. 管理员可以筛选已删除策略并恢复。
5. 恢复后策略重新参与授权。
6. 普通用户始终只读。
7. MySQL 与 SQLite 均能从 v9 迁移到 v10。
8. 后台生产包包含删除和恢复入口。

## 验证命令

```bash
go test ./internal/storage -count=1
go test ./internal/auth -count=1
go test ./internal/server -count=1
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check

cd web
npm test -- --run
npm run build
cd ..
rsync -a --delete web/dist/ internal/server/web_dist/
go test ./internal/server -run TestEmbeddedWebDist -count=1
```

MySQL 集成迁移测试如需执行，需要提供：

```bash
TUNNELMESH_TEST_MYSQL_DSN='<测试库 DSN>'
go test ./internal/storage -run TestMySQL -count=1
```

## 回滚注意事项

- 应用可回滚到支持 Schema v9 的版本；v10 新增 `deleted_at` 列对旧版无害，不要手工删除列。
- 回滚后旧版会把已删除策略当成有效策略，因此如果生产已经执行过逻辑删除，必须先恢复需要生效的策略，或将数据库恢复到升级前备份。
- 不允许手工把 `schema_meta.version` 从 10 改回 9。
- 如需精确恢复 Schema，使用升级前备份；不生成自动反向 DDL。
- 前端回滚后必须重新构建并同步 `internal/server/web_dist`，否则 Go 二进制仍嵌入旧 UI。
