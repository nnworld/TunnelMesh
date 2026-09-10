# Server 节点共享令牌与后台管理实施计划

> **给执行代理的要求：** 必须使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans，按任务逐项执行。所有步骤使用复选框 `- [ ]` 跟踪。

**目标：** 支持一个 `server_node` 令牌服务多个 Server 节点，支持缺失 `node.id` 时自动生成并回写 YAML，并在后台提供 Server 节点管理、状态和生命周期操作。

**架构：** 在 `TokenScope.serverNodeIds` 中保存 Server 节点允许列表，空数组表示 fleet token。Relay 继续使用每个节点独立的 mTLS 证书 SAN 和 epoch 做身份校验。`server_nodes` 增加逻辑管理字段，Server 启动时自注册并持续心跳。后台新增管理员专属 `/servers` 页面，Token 创建表单支持多选 Server 节点。

**令牌配置位置：** Server 配置文件继续使用现有字段 `server.relay.node_token`。示例：

```yaml
server:
  relay:
    enabled: true
    node_token: "<server-node-service-token>"
```

**技术栈：** Go、SQLite、MySQL 5.6、Vue 3、TypeScript、Element Plus、Vite、OpenAPI 3。

**设计文档：** `docs/superpowers/specs/2026-09-10-server-node-fleet-token-admin-design.md`

## 全局约束

- 保留 relay mTLS 和精确证书 SAN 校验。
- 不做旧单节点 `server_node` token 的行为兼容；旧 token 需要重建为 fleet 或多节点 token。
- 不共享 mTLS 证书；每个 Server 节点仍使用自己的证书。
- `scope.serverNodeIds` 为空数组时表示允许所有 Server 节点。
- `scope.serverNodeIds` 非空时只允许列表内节点使用。
- Server 进程从现有 `server.relay.node_token` 字段读取共享令牌。
- Server 节点令牌创建和 Server 节点管理仅管理员可用。
- 删除为逻辑删除，可通过恢复操作重新置为有效。
- Schema 版本从 7 升级到 8，必须同时更新全量 DDL、MySQL 增量脚本和 SQLite 增量脚本。
- MySQL 迁移语法必须兼容 MySQL 5.6。
- API 和 UI 响应不得暴露 token secret 或私钥。
- 所有生命周期变更必须写审计日志。
- 完成前必须执行完整 Go 和前端验证。

---

### 任务 1：Schema 与存储模型

**文件：**

- 修改：`internal/storage/db.go`
- 修改：`internal/storage/models.go`
- 修改：`internal/storage/repository.go`
- 修改：`migrations/ddl.sql`
- 新增：`migrations/incremental/v0007_to_v0008/mysql.sql`
- 新增：`migrations/incremental/v0007_to_v0008/sqlite.sql`
- 修改：`migrations/embed.go`
- 测试：`internal/storage/storage_contract_test.go`
- 测试：`internal/storage/sqlite_test.go`
- 测试：`internal/storage/mysql_test.go`

**接口：**

- 产出： `storage.ServerNode{Name, Enabled, DeletedAt}` 字段。
- 产出： `storage.NodeRepository.Ensure(ctx, storage.ServerNode) error`。
- 产出： `storage.NodeRepository.Touch(ctx, id string, lastSeen, expires time.Time) error`。
- 产出： `storage.ServerNodeStats{NodeID, ActiveConnections, ActiveStreams, HealthScore}`。
- 产出： `storage.NodeRepository.StatsByNodeIDs(ctx, ids []string) (map[string]ServerNodeStats, error)`。

- [x] **步骤 1：编写失败的存储测试**

覆盖以下行为：

```go
node := storage.ServerNode{
    ID: "server-a", Name: "edge-a", Address: "127.0.0.1:9443",
    Epoch: 1, Enabled: true,
}
if err := nodes.Ensure(ctx, node); err != nil {
    t.Fatal(err)
}
found, err := nodes.Get(ctx, node.ID)
if err != nil {
    t.Fatal(err)
}
if found.Name != "edge-a" || !found.Enabled || found.DeletedAt != nil {
    t.Fatalf("unexpected node: %+v", found)
}

disabled := found
disabled.Enabled = false
deletedAt := time.Now().UTC()
disabled.DeletedAt = &deletedAt
if err := nodes.Update(ctx, disabled); err != nil {
    t.Fatal(err)
}
if err := nodes.Ensure(ctx, node); err != nil {
    t.Fatal(err)
}
found, _ = nodes.Get(ctx, node.ID)
if found.Enabled || found.DeletedAt == nil {
    t.Fatalf("Ensure 不应重新启用被管理的节点: %+v", found)
}
```

同时断言 `StatsByNodeIDs` 只按未过期的 `agent_connection_leases.server_node_id` 聚合。

- [x] **步骤 2：运行聚焦存储测试并确认失败**

```bash
go test ./internal/storage -run 'TestStorageContract|TestSQLite' -count=1
```

预期：因字段和方法不存在而编译失败。

- [x] **步骤 3：实现 Schema 与 Repository**

将 `SchemaVersion` 更新为 8。为 `server_nodes` 增加：

- `name VARCHAR(255)`：展示名，默认回填为节点 ID。
- `enabled INTEGER NOT NULL DEFAULT 1`。
- `deleted_at TEXT`：逻辑删除时间。

更新全量 DDL 和两种增量脚本。扩展 `ServerNode` 模型、列列表、扫描函数、`Create`、`Get`、`Update`、`List`，并实现 `Ensure`、`Touch`、`StatsByNodeIDs`。

- [x] **步骤 4：运行聚焦存储测试**

```bash
go test ./internal/storage -run 'TestStorageContract|TestSQLite' -count=1
```

预期：全部通过。

### 任务 2：多节点令牌范围

**文件：**

- 修改：`internal/auth/credential_service.go`
- 测试：`internal/auth/credential_service_test.go`

**接口：**

- 产出： `auth.TokenScope.ServerNodeIDs []string`。
- 产出： `auth.TokenIdentity.ServerNodeIDs []string`。
- 消费： `storage.NodeRepository.Get`。

- [x] **步骤 1：编写失败测试**

覆盖：

```go
input := auth.CreateTokenInput{
    Type:        storage.TokenTypeServerNode,
    OwnerUserID: owner.ID,
    Scope: auth.TokenScope{
        ServerNodeIDs: []string{"server-a", "server-b"},
    },
}
created, err := service.Create(ctx, input)
if err != nil {
    t.Fatal(err)
}
identity, err := service.ValidateAs(ctx, created.Secret, storage.TokenTypeServerNode)
if err != nil {
    t.Fatal(err)
}
if len(identity.ServerNodeIDs) != 2 {
    t.Fatalf("ServerNodeIDs = %v", identity.ServerNodeIDs)
}
```

另需覆盖：

- 空数组表示 fleet token。
- 非空数组中的每个节点必须存在、启用且未逻辑删除。
- 数量上限 128。
- 单个节点 ID 最大 255 字节。
- owner 禁用或逻辑删除时 token 不可用。
- `server_node` token 不允许绑定 `agentId`。

- [x] **步骤 2：运行聚焦测试并确认失败**

```bash
go test ./internal/auth -run TestCredentialService -count=1
```

预期：因 `ServerNodeIDs` 不存在而失败。

- [x] **步骤 3：实现范围校验**

为 `TokenScope`、`TokenIdentity` 和 `CreateTokenInput` 增加 `ServerNodeIDs`。实现：

- 去重并保持稳定顺序。
- 空列表合法，表示所有 Server 节点。
- 非空列表逐项调用 `nodes.Get` 校验。
- 禁止 `server_node` token 同时携带 `AgentID`。
- 不再要求 `NodeID`，也不做旧 `NodeID` 回退。

- [x] **步骤 4：运行聚焦测试**

```bash
go test ./internal/auth -run TestCredentialService -count=1
```

预期：全部通过。

### 任务 3：Relay 认证

**文件：**

- 修改：`internal/relay/server_node_auth.go`
- 测试：`internal/relay/server_node_auth_test.go`

**接口：**

- 消费： `auth.TokenIdentity.ServerNodeIDs`。
- 产出： 保持 `relay.ServerNodePrincipal` 不变。

- [x] **步骤 1：编写失败测试**

创建 fleet token 和两个启用节点，分别使用两套 mTLS 身份认证，均应成功。再使用第三个节点认证，应返回 `PermissionDenied`。

另需覆盖：

- 显式允许列表拒绝未列入节点。
- 禁用或逻辑删除节点拒绝。
- SAN 不匹配拒绝。
- epoch 不匹配拒绝。
- token 过期或撤销拒绝。

- [x] **步骤 2：运行聚焦测试并确认失败**

```bash
go test ./internal/relay -run TestAuthenticatedGRPCRelay -count=1
```

预期：fleet token 场景失败。

- [x] **步骤 3：实现允许列表校验**

在 token 校验和节点查询后执行：

```go
allowed := identity.ServerNodeIDs
if len(allowed) > 0 && !containsNodeID(allowed, callerNodeID) {
    return ServerNodePrincipal{}, status.Error(
        codes.PermissionDenied,
        "relay credential denied",
    )
}
```

保留现有证书精确 SAN 校验和节点 epoch 校验。

- [x] **步骤 4：运行聚焦测试**

```bash
go test ./internal/relay -run TestAuthenticatedGRPCRelay -count=1
```

预期：全部通过。

### 任务 4：`node.id` 自动生成并回写 YAML

**文件：**

- 修改：`internal/config/config.go`
- 修改：`internal/config/config_test.go`
- 修改：`internal/config/ownership_unix_test.go`
- 修改：`internal/cli/root.go`
- 测试：`internal/cli/root_test.go`
- 修改：`deploy/systemd/tunnelmesh-server.service`

**接口：**

- 产出： `config.ConfigOptions.PersistGeneratedNodeIDToConfig bool`。
- 消费： 现有 `config.InitializeNodeID`。

- [x] **步骤 1：编写失败测试**

断言：

- cluster 模式下 `run` 且 YAML 缺少 `node.id` 时，会生成并写回 YAML。
- `check-config` 不修改配置文件。
- YAML 中已有 `node.id` 时不覆盖。
- CLI `--node-id` 或环境变量提供 ID 时不覆盖。
- 写回时保留 YAML 注释、文件权限和 owner。

- [x] **步骤 2：运行聚焦测试并确认失败**

```bash
go test ./internal/config ./internal/cli -run 'TestLoad|TestInitializeNodeID|TestServerCommands' -count=1
```

预期：新的 YAML 回写测试失败。

- [x] **步骤 3：实现配置回写**

在读取配置文件后、应用环境变量和 CLI 覆盖前，记录文件中的 `node.id`。当满足：

- 模式为 cluster；
- 生效 `node.id` 为空；
- 文件中也没有 `node.id`；
- `PersistGeneratedNodeIDToConfig=true`；

则调用 `InitializeNodeID` 并使用返回值。只有 Server 的 `run` 命令开启该选项。

systemd 单元改为：

```ini
ExecStartPre=+/usr/local/bin/tunnelmesh-server --config /etc/tunnelmesh/server.yaml init-node-id
ExecStartPre=/usr/local/bin/tunnelmesh-server --config /etc/tunnelmesh/server.yaml check-config
```

第一个 `+` 表示以 root 执行初始化，服务本体仍以 `tunnelmesh` 用户运行，`/etc/tunnelmesh` 对服务用户保持只读。

- [x] **步骤 4：运行聚焦测试**

```bash
go test ./internal/config ./internal/cli -run 'TestLoad|TestInitializeNodeID|TestServerCommands' -count=1
```

预期：全部通过。

### 任务 5：Server 自注册与心跳

**文件：**

- 修改：`internal/server/runtime.go`
- 新增：`internal/server/server_node_lifecycle.go`
- 测试：`internal/server/runtime_test.go`
- 测试：`internal/server/server_node_lifecycle_test.go`

**接口：**

- 消费： `storage.NodeRepository.Ensure`、`Touch`、`StatsByNodeIDs`。
- 产出： `server.NewServerNodeLifecycle(db, nodeID, address, interval, ttl)`。
- 产出： `(l *ServerNodeLifecycle) Start(ctx context.Context) error`。
- 产出： `(l *ServerNodeLifecycle) Close() error`。

- [x] **步骤 1：编写失败测试**

断言：

- 启动时创建启用的 Server 节点。
- 重复启动不会重新启用被禁用或逻辑删除的节点。
- 心跳更新 `last_seen_at` 和 `expires_at`。
- `Close` 停止后台任务且不泄漏 goroutine。

- [x] **步骤 2：运行聚焦测试并确认失败**

```bash
go test ./internal/server -run TestServerNodeLifecycle -count=1
```

预期：符号不存在。

- [x] **步骤 3：实现自注册与心跳**

在 cluster runtime 构造时，先确保本地 Server 节点存在，再启动 relay。心跳间隔 30 秒，租约有效期 90 秒。节点地址使用 relay endpoint。若节点被禁用或逻辑删除，启动时返回明确错误。

- [x] **步骤 4：运行聚焦测试**

```bash
go test ./internal/server -run TestServerNodeLifecycle -count=1
```

预期：全部通过。

### 任务 6：Server 节点管理 API

**文件：**

- 新增：`internal/server/server_node_api.go`
- 新增：`internal/server/server_node_service.go`
- 修改：`internal/server/api.go`
- 测试：`internal/server/server_node_api_test.go`
- 修改：`docs/api/openapi.yaml`

**接口：**

- 产出： `GET /api/v1/server-nodes`。
- 产出： `GET /api/v1/server-nodes/{id}`。
- 产出： `PATCH /api/v1/server-nodes/{id}`。
- 产出： `DELETE /api/v1/server-nodes/{id}`。
- 产出： `POST /api/v1/server-nodes/{id}/restore`。

- [x] **步骤 1：编写失败测试**

覆盖：

- 管理员可列表和查看详情。
- 普通用户返回 403。
- cursor 分页。
- `name` 和 `enabled` 校验。
- 逻辑删除。
- 恢复。
- 审计日志。
- 禁用节点状态。
- 活跃连接数、活跃流和健康分聚合。

- [x] **步骤 2：运行聚焦测试并确认失败**

```bash
go test ./internal/server -run TestServerNodeAPI -count=1
```

预期：路由不存在。

- [x] **步骤 3：实现 Service 与 Handler**

遵守 Handler → Service → Repository 分层：

- `PATCH` 只接受 `name` 和 `enabled`。
- `DELETE` 设置 `deleted_at` 且 `enabled=false`。
- `restore` 清空 `deleted_at` 且 `enabled=true`。
- 状态派生为 `deleted`、`disabled`、`online`、`offline`。
- 审计 action 为 `server_node.updated`、`server_node.deleted`、`server_node.restored`。

- [x] **步骤 4：运行聚焦测试**

```bash
go test ./internal/server -run TestServerNodeAPI -count=1
```

预期：全部通过。

### 任务 7：后台管理页面

**文件：**

- 修改：`web/src/api/client.ts`
- 新增：`web/src/views/Servers.vue`
- 修改：`web/src/router.ts`
- 修改：`web/src/layouts/AppShell.vue`
- 修改：`web/src/views/Tokens.vue`
- 修改：`web/src/i18n/messages/zh-CN.ts`
- 修改：`web/src/i18n/messages/en-US.ts`
- 测试：`web/src/tests/servers.spec.ts`
- 测试：`web/src/tests/views.spec.ts`
- 测试：`web/src/tests/agent-token-workflows.spec.ts`

**接口：**

- 产出： `type ServerNode`。
- 产出： `getServerNodes`、`getServerNode`、`updateServerNode`、`deleteServerNode`、`restoreServerNode`。
- 产出： 管理员路由 `/servers`。

- [x] **步骤 1：编写失败测试**

断言：

- 管理员菜单出现 Server 节点入口。
- 普通用户不能访问 `/servers`。
- 列表渲染状态、地址、epoch、最后心跳、租约到期、活跃连接和活跃流。
- 启用、禁用、逻辑删除和恢复调用正确 API。
- Token 创建表单中 Server 节点为多选。
- 多选留空表示所有节点。

- [x] **步骤 2：运行聚焦测试并确认失败**

```bash
cd web && npm test -- --run src/tests/servers.spec.ts src/tests/views.spec.ts src/tests/agent-token-workflows.spec.ts
```

预期：路由、页面或 API 客户端缺失。

- [x] **步骤 3：实现 UI**

使用现有 Element Plus 页面、卡片、状态标签、`DataState` 和 i18n 模式。新增：

- Server 节点列表。
- 详情抽屉。
- 启用/禁用按钮。
- 逻辑删除/恢复按钮。
- Token 创建时的 Server 节点多选框，数据来自 `getServerNodes`。

- [x] **步骤 4：运行聚焦测试**

```bash
cd web && npm test -- --run src/tests/servers.spec.ts src/tests/views.spec.ts src/tests/agent-token-workflows.spec.ts
```

预期：全部通过。

### 任务 8：文档与 PR 说明

**文件：**

- 修改：`docs/README.md`
- 修改：`docs/operations/configuration.md`
- 修改：`docs/operations/config-examples.md`
- 修改：`docs/operations/connection-pool.md`
- 修改：`docs/operations/troubleshooting.md`
- 修改：`docs/deployment/linux-systemd.md`
- 修改：`docs/user-guide/server-admin.md`
- 修改：`docs/api/openapi.yaml`
- 新增：`docs/pull-requests/2026-09-10-server-node-fleet-token-admin.md`

- [x] **步骤 1：更新运维文档**

明确：

- fleet token 语义。
- 显式允许列表语义。
- `node.id` 自动生成和 YAML 回写。
- systemd 初始化顺序。
- mTLS SAN 必须与最终 `node.id` 精确匹配。
- Server 状态派生规则。
- 逻辑删除和恢复。
- v7 到 v8 的升级与回滚步骤。

- [x] **步骤 2：更新 OpenAPI**

新增 Server 节点接口和 Schema。Token 请求中：

- `nodeId` 标记为 deprecated。
- `scope.serverNodeIds` 表示允许列表。
- 空数组表示所有节点。

- [x] **步骤 3：编写 PR 说明**

包含：

- 用户可见变化。
- 数据库迁移影响。
- 回滚步骤。
- 运维初始化顺序。
- 验证结果。

### 任务 9：完整验证

**文件：**

- 不新增源码文件。

- [x] **步骤 1：Go 验证**

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

- [x] **步骤 2：前端验证**

```bash
cd web
npm test -- --run
npm run build
```

- [ ] **步骤 3：部署验证**

```bash
systemd-analyze verify deploy/systemd/tunnelmesh-server.service
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /tmp/tunnelmesh-server ./cmd/tunnelmesh-server
```

> 当前 macOS 验证环境缺少 `systemd-analyze`，且没有 Docker/Podman/nerdctl/VM 可用；Linux 构建已通过，systemd unit 已做人工结构检查，真实 `systemd-analyze verify` 需在 Linux 环境补跑。

- [x] **步骤 4：对照计划复核**

逐项检查本计划要求，并在 PR 说明中记录任何有意偏差。

## 自检

- 已覆盖 token 范围、relay 认证、YAML 初始化、自注册、心跳、API、UI、迁移、文档和测试。
- 无占位项。
- 已按用户要求移除旧单节点 token 兼容。
- 安全边界仍由 mTLS SAN 和节点 epoch 保证。
