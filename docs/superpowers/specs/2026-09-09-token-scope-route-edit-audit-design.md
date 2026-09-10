# Token 范围与托管路由修改补记规格

## 背景

2026-09-09 管理后台暴露出两个操作缺口：

1. Token 创建后不能修改 `protocols`、`targetCIDRs` 和 `targetPorts`，用户只能撤销并重建 Token。
2. 托管路由创建后不能修改；审计日志列表因后端返回 Go 字段名而前端期望 camelCase 字段，导致页面大量显示横线。

本规格是对已完成实现的补记，用于约束后续维护和复审，不引入新的 Schema。

## 目标

1. `PATCH /api/v1/tokens/{tokenId}` 支持更新授权范围中的协议、目标 CIDR 和目标端口。
2. `PATCH /api/v1/routes/{routeId}` 支持部分更新托管路由字段。
3. 审计日志 API 输出稳定的 camelCase 公共视图，前端能展示关键字段和结构化详情。
4. 保留现有 RBAC、统一响应结构和审计要求，不暴露任何凭据。

## 非目标

- 不修改 Token 类型、所有者、Agent/Node 绑定或明文 secret。
- 不新增或修改数据库表结构。
- 不把已撤销或已过期 Token 通过修改操作复活。
- 不改变公网入口协议或路由缓存刷新周期。

## 验收标准

### Token 范围更新

- 请求至少包含 `expiresAt` 或 `scope`；本次扩展 `scope`。
- `scope.protocols`、`scope.targetCIDRs`、`scope.targetPorts` 可分别提交。
- 字段省略时保持当前值；空数组表示移除该维度限制。
- 协议必须属于既有 allowlist，CIDR 必须可解析，端口必须在 1–65535。
- 只有 Token owner 或管理员可修改；普通用户访问他人资源返回 `403`。
- 已撤销或已过期 Token 返回 `409`，要求轮换。
- 成功修改写入 `token.scope_updated` 审计日志。
- 响应返回脱敏 Token 元数据，不返回哈希或明文。

### 托管路由更新

- `PATCH` 只更新提交的字段，`PUT` 继续要求完整必填字段。
- 可更新 Agent、协议、域名、路径、目标地址、目标端口、公网端口、状态和配置。
- 修改目标 Agent 时必须重新校验目标 Agent 的 owner 和启用状态。
- 域名必须符合现有明确域名或单层 `tm-*` 泛域名规则。
- 重复的 domain + pathPrefix 返回 `409`。
- 成功修改写入 `route.updated` 审计日志，详情只包含路由事实和状态。
- 运行时路由缓存沿用现有 5 秒 TTL，更新最长 5 秒后生效。

### 审计日志视图

- 公共 API 字段固定为 `id`、`actorUserId`、`action`、`resourceType`、`resourceId`、`details`、`createdAt`。
- 列表按 `createdAt` 倒序返回；同一时间按 `id` 倒序作为稳定 tie-breaker。
- 分页 cursor 必须基于 `(createdAt, id)`；旧版本只包含 ID 的 cursor 仍可继续翻页。
- 管理后台支持服务端筛选：时间范围、操作者 ID、动作、资源类型和资源 ID。
- 文本筛选使用精确匹配；`createdFrom` 和 `createdTo` 使用闭区间。
- `details` 是对象；空详情可显示本地化空态。
- 管理后台列表展示时间、操作者、动作、资源类型和资源 ID，并提供详情对话框。

## 安全边界

- 所有授权校验在服务端完成，前端只控制入口可见性。
- 请求和审计日志不得包含 Token 明文、哈希、密码、完整 Authorization header 或私有密钥。
- 路由审计详情不包含自由配置对象，避免间接记录敏感值。

## 验证要求

实现必须覆盖：

- Repository 状态机：active 可更新，revoked/expired 拒绝更新。
- API 错误语义：400、403、404、409。
- 审计事件和字段。
- 前端 API payload 与页面工作流。
- OpenAPI 和用户文档。
- Go 全量测试、race、vet、前端测试和构建。
