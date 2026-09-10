# TunnelMesh 管理后台国际化、账号管理与 UI 设计

## 1. 背景与目标

当前管理后台已具备登录、Agent、路由、隧道、Token 和审计等基础页面，但页面缺少统一布局与视觉规范，用户可见文本主要硬编码为英文，也没有当前用户改密和子账号管理入口。

本期目标是：

- 保留 Vue 3、TypeScript、Element Plus、Pinia 和 Vue Router 技术栈；
- 建立参考 Ant Design Vue 信息架构的浅色 SaaS 管理台视觉体系；
- 第一阶段支持简体中文和英文，按浏览器语言自动识别并允许用户切换；
- 支持当前登录用户修改自己的密码；
- 支持管理员创建、查询、启用、禁用、重置密码和删除普通子账号；
- 保持既有 RBAC、资源所有权、安全审计和敏感信息保护边界。

这是应用 `MINOR` 级功能迭代。`users` 表新增可空 `deleted_at`，Schema 从 v5 升级到 v6；发布必须同时提供 MySQL 5.6 与 SQLite 的相邻版本增量脚本和更新后的全量 DDL。

## 2. 范围与非目标

### 2.1 本期范围

- 全部现有后台页面的统一 App Shell、设计 token 和响应式样式；
- 全部现有后台页面及新增页面的中文、英文翻译；
- 浏览器语言识别、语言切换、语言偏好本地持久化；
- 当前用户修改密码；
- 管理员管理普通子账号；
- Dashboard 权限范围内的准确资源摘要；
- 新增 API、OpenAPI、帮助文档、单元测试、集成测试和前端测试。

### 2.2 非目标

- 不引入 Ant Design Vue，不替换 Element Plus；
- 不增加组织、租户、部门或多级账号树；
- 不允许自定义角色、权限点或把普通用户提升为管理员；
- 不实现强制首次登录改密、密码过期、MFA 或邮件找回；
- 修改或重置密码后不撤销已有登录 Token；
- 不在前端保存、缓存或重复展示临时明文密码；
- 不改变 Agent、Client、server-node service token 的认证模型。

## 3. 已确认的产品行为

### 3.1 语言

支持 `zh-CN` 和 `en-US`。初始语言按以下优先级选择：

1. localStorage 中用户已明确选择的受支持语言；
2. 按顺序扫描 `navigator.languages`，任何 `zh-*` 映射到 `zh-CN`；
3. 其他语言映射到 `en-US`。

登录页和登录后的顶部栏均提供语言切换。切换立即更新业务文案、Element Plus 内置组件、日期时间格式和校验提示，并写入 localStorage。语言偏好属于浏览器本地 UI 设置，不写数据库。

### 3.2 当前用户修改密码

所有已登录用户均可在“个人设置 / 安全设置”中修改自己的密码。请求必须包含当前密码和新密码，前端另行要求确认新密码。服务端重新校验当前密码并使用 Argon2id 保存新哈希。

修改成功后：

- 已有登录 Token 保持有效，包括发起操作的当前会话；
- 后续新登录必须使用新密码；
- 写入不含密码或哈希的审计记录；
- UI 显示成功提示，不跳回登录页。

账号被管理员禁用后，`ValidateToken` 每次鉴权读取用户状态，因此既有 Token 也不能继续通过鉴权。

### 3.3 子账号管理

第一期“子账号”是 `role=user` 的普通用户，不新增父子关系字段。管理员可以管理所有普通用户，但不能通过这些接口创建、修改、禁用、重置或删除管理员账号，也不能通过接口修改角色。

创建子账号时管理员只输入用户名。服务端生成高强度随机临时密码，响应只返回一次并设置 `Cache-Control: no-store`。前端使用一次性密码弹窗展示；弹窗关闭、路由离开或组件卸载时清空内存变量。

管理员重置子账号密码时，服务端生成新的高强度随机临时密码并只返回一次。重置不撤销该账号已有登录 Token。

管理员可以启用或禁用普通账号。禁用账号立即阻止新登录及已有 Token 的后续鉴权，但不删除其业务资源。

删除普通账号采用逻辑删除：设置 `deleted_at=当前 UTC 时间` 且 `disabled=1`，保留用户、登录 Token、service token、Agent、路由、隧道和审计记录。已删除账号不能登录，已有登录 Token 与其拥有的 service token 在后续鉴权时也必须失效。

管理员可以在“已删除”筛选中恢复普通账号。恢复时清空 `deleted_at` 并设置 `disabled=0`，原用户名和关联资源继续使用。用户名唯一约束继续覆盖已删除记录，因此删除后的用户名不能被新账号复用。逻辑删除和恢复都不能作用于管理员账号。

## 4. 架构设计

### 4.1 后端分层

新增独立 `AccountService`，避免把账号生命周期继续堆入登录认证职责：

```text
Account HTTP Handler
        ↓
AccountService
        ↓
UserRepository / AuditRepository
```

Handler 只负责：

- 解析和限制 JSON 请求体；
- 获取已认证 Principal；
- 执行管理员或本人边界检查；
- 将领域错误映射到 HTTP 状态和统一响应结构。

`AccountService` 负责：

- 用户名与密码策略校验；
- 当前密码验证；
- Argon2id 哈希和随机临时密码生成；
- 禁止管理管理员账号；
- 账号状态变更；
- 逻辑删除和恢复；
- 事务编排和结构化审计。

Repository 负责 SQL、cursor 分页、锁和事务绑定。Handler 和 Service 不直接执行 SQL。

### 4.2 前端模块

前端新增以下边界：

- `i18n/`：locale 解析、偏好持久化、翻译资源和翻译 key 类型；
- `layouts/AppShell.vue`：顶部栏、侧栏、面包屑、移动端抽屉和内容区域；
- `components/PageHeader.vue`：统一页面标题、说明与操作区；
- `components/StatusTag.vue`：统一状态色和标签；
- `components/OneTimePasswordDialog.vue`：创建/重置后的临时密码展示与内存清理；
- `views/Users.vue`：管理员子账号列表与管理动作；
- `views/AccountSecurity.vue`：当前用户修改密码；
- `stores/preferences.ts`：仅管理语言和导航折叠等本地 UI 偏好；
- `styles/`：颜色、字号、间距、阴影、圆角和响应式断点。

认证 Store 继续只管理当前用户和登录 Token。账号列表和修改操作通过 API client 调用，不把临时密码放入 Pinia 或 localStorage。

## 5. API 设计

所有接口使用 `/api/v1` 和统一响应 `{ code, msg, data }`。公开用户对象只包含 `id`、`username`、`role`、`disabled`、`deletedAt`、`createdAt`、`updatedAt`，永不返回 `passwordHash`。

### 5.1 修改当前用户密码

```http
PUT /api/v1/auth/password
Authorization: Bearer <token>
Content-Type: application/json

{
  "currentPassword": "...",
  "newPassword": "..."
}
```

成功返回公开用户信息或空数据。已有登录 Token 不撤销。错误语义：

- `400`：字段缺失或新密码不符合策略；
- `401`：未登录；
- `403`：当前密码错误。

### 5.2 查询子账号

```http
GET /api/v1/users?cursor=<cursor>&limit=<limit>&status=<active|deleted|all>
```

仅管理员可用，使用现有 cursor 分页语义。`status` 默认 `active`。只返回 `role=user` 的普通账号，避免在该页面暴露或误操作管理员账号。

### 5.3 创建子账号

```http
POST /api/v1/users
Content-Type: application/json

{
  "username": "operator-cn"
}
```

服务端固定创建 `role=user`，生成临时密码。响应设置 `Cache-Control: no-store`：

```json
{
  "code": 201,
  "msg": "Created",
  "data": {
    "user": { "id": "...", "username": "operator-cn", "role": "user", "disabled": false },
    "temporaryPassword": "one-time-value"
  }
}
```

重复用户名返回 `409`。该接口不持久化明文响应；如果创建成功但响应丢失，管理员可通过账号列表确认用户并执行一次密码重置。

### 5.4 启用或禁用子账号

```http
PATCH /api/v1/users/{userId}
Content-Type: application/json

{ "disabled": true }
```

只接受 `disabled` 字段。重复设置相同目标状态应成功并返回当前公开用户，保证自然幂等。目标不存在返回 `404`，目标是管理员返回 `403`。

### 5.5 重置子账号密码

```http
POST /api/v1/users/{userId}/reset-password
```

仅管理员可操作普通账号。服务端生成临时密码、更新哈希并返回一次，响应设置 `Cache-Control: no-store`。不撤销已有登录 Token。目标不存在返回 `404`，目标是管理员返回 `403`。

### 5.6 删除和恢复子账号

```http
DELETE /api/v1/users/{userId}
```

仅管理员可操作普通账号。删除设置 `deleted_at` 和 `disabled=1`，不物理删除任何账号或关联记录。重复删除保持逻辑删除状态并返回当前公开用户。

```http
POST /api/v1/users/{userId}/restore
```

恢复清空 `deleted_at` 并设置 `disabled=0`。重复恢复保持有效状态并返回当前公开用户。目标不存在返回 `404`，目标是管理员返回 `403`。

### 5.7 Dashboard 摘要

```http
GET /api/v1/dashboard/summary
```

返回调用者权限范围内的准确聚合：Agent 总数和在线数、活动隧道数、托管路由数、有效 service token 数以及最近非敏感事件。管理员查询全局，普通用户按 owner 过滤。聚合在 Repository 内完成，禁止读取全表后在 Handler 中计数。

## 6. 校验与错误模型

用户名规范：

- 3–64 个 ASCII 字符；
- 只允许字母、数字、点、下划线和连字符；
- 去除首尾空白后校验；
- 唯一性由数据库约束保证。

用户设置的新密码规范：

- 12–128 个 Unicode 字符；
- 不得与当前密码相同；
- 服务端是最终校验边界，前端只提供即时反馈。

服务端生成的临时密码使用 `crypto/rand`，熵不低于现有管理员随机凭据。明文只存在于请求生命周期和前端弹窗内存中。

新增账号 API 使用稳定领域错误标识，例如：

- `username_invalid`；
- `username_conflict`；
- `password_policy_violation`；
- `current_password_invalid`；
- `admin_account_protected`；
- `account_deleted`。

前端按领域错误标识显示本地化文案。未知错误显示本地化通用提示并保留 HTTP 状态用于排障，不直接展示 SQL、DSN 或服务端内部错误。

## 7. UI 与视觉规范

保留 Element Plus，参考 Ant Design Vue 的信息层级与密度：

- 页面背景 `#f5f7fa`，内容卡片白色；
- 主色使用清晰的 SaaS 蓝色，成功、警告、危险遵循一致状态色；
- 顶部栏 48–56px，桌面侧栏约 200px；
- 页面使用面包屑、标题、辅助说明和右侧主操作；
- 卡片使用轻边框和小圆角，避免厚重阴影；
- 表格紧凑但保持可读性，操作按主次排序；
- 空状态、加载骨架、失败重试和危险确认均使用统一组件；
- 低于移动端断点时侧栏变为抽屉，表格允许横向滚动，关键操作不隐藏。

Dashboard 只展示真实聚合值。接口失败时对应区域显示不可用和重试，不用 `0` 或静态数字掩盖错误。

## 8. 国际化设计

使用 `vue-i18n`，不维护自制字符串替换器。翻译资源按领域拆分：

```text
common
auth
navigation
dashboard
users
agents
routes
tunnels
tokens
audits
```

中文和英文必须具有相同 key 集合。用户可见标题、按钮、表单标签、占位符、状态、确认框、成功/失败提示、空状态和校验提示全部通过翻译 key 获取。协议名、ID、用户名、域名和后端数据保持原值。

日期时间统一通过 locale-aware formatter 渲染。测试使用固定时区或只断言语义，避免依赖开发机时区。

## 9. 安全与审计

审计动作至少包括：

- `account.created`；
- `account.enabled`；
- `account.disabled`；
- `account.password_changed`；
- `account.password_reset`；
- `account.deleted`；
- `account.restored`。

审计 details 只包含目标用户 ID、用户名和非敏感状态，不包含当前密码、新密码、临时密码或密码哈希。

一次性密码 API 必须设置 `Cache-Control: no-store`。前端不得将临时密码写入日志、错误监控、URL、Pinia、localStorage、sessionStorage 或剪贴板以外的持久位置。复制操作由用户显式触发。

逻辑删除必须同时设置 `disabled`，确保已有登录 Token 在下一次鉴权时立即失效。service token 鉴权也必须将 owner 的 `deleted_at`/`disabled` 作为不可用状态。外部输入全部在 Handler 边界限制大小，并在 Service 再做领域校验。

## 10. 测试策略

### 10.1 Go

- `AccountService` 单元测试：用户名、密码策略、本人改密、管理员保护、随机密码、启禁用、逻辑删除、恢复和审计脱敏；
- Repository contract 测试：SQLite 与 MySQL 的 active/deleted/all 分页、逻辑删除和恢复；
- HTTP API 测试：未认证、非管理员越权、管理员成功路径、404、筛选、恢复、`no-store` 和统一响应；
- Dashboard 聚合测试：管理员全局与普通用户 owner 过滤；
- 日志和响应断言：不得包含密码、密码哈希、Authorization 或 DSN。

### 10.2 前端

- 浏览器语言识别与 fallback；
- 手动切换和 localStorage 持久化；
- 中文、英文 key 集合一致；
- Element Plus locale 与业务 locale 同步；
- 登录页、导航和所有现有页面不残留硬编码用户可见文本；
- `/users` 管理员路由和普通用户拒绝；
- `/account/security` 当前用户改密表单；
- 创建、重置、启禁用、逻辑删除、已删除筛选和恢复；
- 一次性密码弹窗关闭和组件卸载后清理；
- 响应式导航与关键页面渲染。

## 11. 文档与发布

同步更新：

- `docs/api/openapi.yaml`；
- `docs/README.md`；
- `docs/user-guide/server-admin.md`；
- 管理员恢复与账号排障文档；
- 前端构建和 embed 同步说明（如命令发生变化）。

发布顺序：先备份数据库并执行/验证 v5→v6 增量迁移，再发布向后兼容的新后端 API，最后发布新前端资源。前端构建后清理并同步 `web/dist/` 到 `internal/server/web_dist/`，确保 Go embed 不残留旧哈希资源。

回滚优先回滚前端和应用；v6 的可空新增列对旧应用向后兼容，回滚应用时保留该列，不自动执行破坏性反向 DDL。灰度阶段重点观察迁移结果、登录失败率、账号 API 4xx/5xx、前端资源加载失败和管理 API P99。

## 12. 验收标准

- 中文浏览器首次打开显示中文，非中文浏览器显示英文；手动切换刷新后保持；
- 所有现有和新增页面均有完整中文、英文文案；
- 当前用户可验证旧密码后修改密码，已有 Token 保持有效；
- 管理员可完整管理普通子账号，不能管理管理员账号或修改角色；
- 创建和重置只展示一次服务端随机密码，响应和前端均不缓存；
- 子账号可逻辑删除和恢复；删除期间保留全部关联资源，但账号及其 Token 不再通过鉴权；
- Dashboard 显示权限范围内的真实数据；
- 页面符合已确认的 Element Plus 浅色 SaaS 风格并支持窄屏；
- v5→v6 的 MySQL 5.6/SQLite 增量脚本、全量 DDL、OpenAPI、用户文档、Go 测试、前端测试、构建、race、vet、diff 检查和 embed 校验全部通过。
