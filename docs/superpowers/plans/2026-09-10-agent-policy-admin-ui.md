# Agent Policy 后台展示与编辑入口实施计划

## 目标

在管理后台的 Agent 详情页展示当前 Agent 的访问策略，并为管理员提供创建和编辑入口。页面必须能直观表达以下语义：

- `targetHost: "*"` 表示任意目标主机；
- `targetPort: 0` 表示任意目标端口；
- `allowedCIDRs` 和 `allowedPorts` 为空数组表示不限制；
- Agent Policy 是服务端最终授权策略，Token scope 只能在其基础上进一步缩小权限。

## 架构决策

- 不修改后端 API、数据库 Schema、认证授权逻辑或 Agent Policy 判定逻辑；现有 `/api/v1/agents/{agentId}/policies` 已满足需求。
- 策略入口放在 Agent 详情页，而不是 Tokens 页面或 Routes 页面，因为 Agent Policy 的权威归属是 Agent，避免与 Token scope、托管路由目标混淆。
- 管理员可以看到“添加策略”和“编辑”；普通用户如果拥有该 Agent，只读展示策略，不显示写操作按钮，保持后端 RBAC 与 UI 一致。
- 创建使用 `Idempotency-Key`；编辑使用 PATCH 并提交完整策略字段，避免省略 allowlist 字段造成语义歧义。
- 不实现删除策略入口；当前需求只要求显示和编辑，避免扩大授权管理风险。

## 技术栈

- Vue 3 + TypeScript + Pinia + Vue I18n + Element Plus。
- 组件遵循现有浅色 SaaS 管理台风格：`tm-card`、`DataState`、`el-table`、`el-dialog`、`el-form`。
- 前端测试使用 Vitest + jsdom，生产包由 Vite 构建并同步到 `internal/server/web_dist`。

## 规格引用

- `docs/api/openapi.yaml` 中 `PolicyRequest` 与 `/api/v1/agents/{agentId}/policies`。
- `docs/user-guide/client.md` 中 SOCKS5 通配 Agent Policy 示例。
- `internal/server/api.go` 中 Agent Policy 创建、更新和管理员校验行为。

## 全局约束

- 不提交或展示 Token、密码、私钥和生产 DSN。
- 策略表单必须在客户端做基础校验，但服务端仍是权威校验边界。
- 中文和英文文案必须同时维护，不能出现只存在于一种语言下的 UI 文案。
- 保持现有 Element Plus 视觉体系，不引入新的 UI 库或全局样式体系。
- 现有工作区中已有未提交的 Agent Policy 通配后端改动；本计划只在其上追加前端能力，不回退或重写后端改动。

## 精确文件清单

1. `web/src/api/client.ts`
   - 新增 `AgentPolicy`、`AgentPolicyInput`、`AgentPolicyPage` 类型。
   - 新增 `listAgentPolicies(agentId)`。
   - 新增 `createAgentPolicy(agentId, input, idempotencyKey)`。
   - 新增 `updateAgentPolicy(agentId, policyId, input)`。

2. `web/src/views/AgentDetail.vue`
   - 新增独立的“访问策略”卡片和加载、错误、空态。
   - 新增策略表格，展示协议、目标主机、目标端口、允许 CIDR、允许端口、更新时间和操作。
   - `*`、`0`、空数组在表格中显示为“任意主机”“任意端口”“不限制”。
   - 管理员显示“添加策略”和“编辑”，普通用户只读。
   - 新增创建/编辑共用弹窗，字段包含协议、目标主机、目标端口、允许 CIDR、允许端口。
   - 创建成功后刷新策略列表；编辑成功后更新列表数据。

3. `web/src/i18n/messages/zh-CN.ts`
   - 新增访问策略相关中文文案。

4. `web/src/i18n/messages/en-US.ts`
   - 新增访问策略相关英文文案。

5. `web/src/tests/agent-detail.spec.ts`
   - 补充 API mock 与 Pinia 初始化。
   - 覆盖策略展示、通配语义、管理员入口、普通用户只读、创建和编辑行为。

6. `web/src/tests/agent-policy-api.spec.ts`
   - 验证策略 API client 的 URL、方法、请求体和 `Idempotency-Key`。

7. `docs/user-guide/client.md`
   - 将“通过 API 创建通配策略”的说明补充为“可在后台 Agent 详情页管理，也可通过 API 管理”。

8. `internal/server/web_dist/*`
   - 前端构建成功后同步新的生产产物。

9. `internal/server/web_test.go`
   - 嵌入资源冒烟关键词增加“访问策略”或对应英文产物关键词，确保新 UI 进入 Go embed 包。

## 任务间接口

### API 类型

```ts
export type AgentPolicy = {
  id: string
  agentId: string
  protocol: string
  targetHost: string
  targetPort: number
  allowedCIDRs: string[]
  allowedPorts: number[]
  createdAt: string
  updatedAt: string
}

export type AgentPolicyInput = {
  protocol: string
  targetHost: string
  targetPort: number
  allowedCIDRs: string[]
  allowedPorts: number[]
}
```

### UI 状态

- `policies`：当前页策略数组。
- `policiesLoading`、`policiesError`：独立于 metadata 的加载和错误状态。
- `policyDialogVisible`、`policySaving`、`editingPolicy`：共用弹窗状态。
- `policyForm`：协议、目标主机、目标端口、CIDR 文本、端口文本。
- CIDR 和端口输入使用逗号分隔文本框，提交前转换为数组；空文本转换为空数组。

## TDD 步骤

### 1. API client 测试先行

新增 `web/src/tests/agent-policy-api.spec.ts`，先断言：

- `listAgentPolicies('agent-1')` 请求 `GET /agents/agent-1/policies`；
- `createAgentPolicy` 请求 `POST /agents/agent-1/policies`，带 `Idempotency-Key` 和完整 JSON；
- `updateAgentPolicy` 请求 `PATCH /agents/agent-1/policies/policy-1`，提交完整 JSON。

预期失败结果：函数和类型不存在，TypeScript/Vitest 编译失败。

### 2. 实现 API client 最小变更

在 `web/src/api/client.ts` 增加类型和三个函数，只做请求封装，不加入 UI 逻辑。

预期通过结果：API client 测试全部通过。

### 3. Agent 详情页测试先行

扩展 `web/src/tests/agent-detail.spec.ts`：

- mock 策略列表包含 `targetHost: "*"`、`targetPort: 0`、空 allowlist；
- 断言页面显示“任意主机”“任意端口”“不限制”；
- 管理员状态下显示添加和编辑入口；
- 普通用户状态下不显示写入口；
- 打开编辑弹窗后表单回填原值；
- 保存编辑时调用 `updateAgentPolicy` 并刷新或更新列表；
- 打开创建弹窗后保存时调用 `createAgentPolicy`，请求体包含空数组和 `targetPort: 0`。

预期失败结果：组件缺少策略区块和弹窗，相关断言找不到元素或函数调用。

### 4. 实现 Agent 详情页最小 UI

在 `AgentDetail.vue` 增加独立策略卡片、表格、共用弹窗和状态管理。只实现计划内字段和操作，不做删除、批量编辑或高级表达式解析。

预期通过结果：Agent 详情页新增测试全部通过，原有 metadata、连接列表和关闭连接测试保持通过。

### 5. i18n 与用户文档

- 补充中英文文案。
- 更新 `docs/user-guide/client.md`，说明后台入口与 API 管理方式等价。

预期失败结果：测试中出现缺失文案或文档检查不满足时失败；实现后通过。

### 6. 构建并同步嵌入资源

- 执行前端测试和构建。
- 将 `web/dist` 同步到 `internal/server/web_dist`。
- 更新 Go embed 冒烟测试关键词。

预期通过结果：前端测试、前端构建、Go embed 测试全部通过。

## 最小实现

- 仅新增策略展示、创建、编辑能力。
- 协议选择限定为后端已支持的 `tcp`、`udp`、`http`、`ws`。
- 目标端口使用数字输入，允许 `0` 到 `65535`。
- 目标主机允许精确 IP/域名或字面量 `*`；不允许 `foo*`、`*.example.com` 等部分通配。
- CIDR 和端口分别用逗号分隔文本输入，空值表示不限制。
- 不新增删除按钮、分页加载按钮或策略复制功能。

## 预期通过结果

1. 管理员打开 Agent 详情页可以看到访问策略卡片和当前策略。
2. 通配策略显示为“任意主机”“任意端口”，空 allowlist 显示为“不限制”。
3. 管理员可以创建和编辑策略，普通用户只读。
4. 创建和编辑请求与现有 API 契约一致。
5. 中文和英文界面均可正常显示。
6. 生产构建产物包含新功能，Go embed 冒烟测试能验证。

## 验证命令

```bash
cd web
npm test -- --run
npm run build
cd ..
go test ./internal/server -run TestEmbeddedWebDist -count=1
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

## 回滚注意事项

- 前端源码回滚后，需要重新构建并同步 `internal/server/web_dist`，否则 Go 二进制仍会嵌入旧 UI。
- 本功能无数据库迁移和后端行为变更，回滚不涉及数据补偿。
- 若只回滚 UI，不影响已存在的 Agent Policy 数据和现有 API 调用。
