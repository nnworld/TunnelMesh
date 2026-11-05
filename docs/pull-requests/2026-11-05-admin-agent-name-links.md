# 管理后台 Agent 引用统一显示名称并可跳转

- 标题：后台所有引用 Agent 的列表与详情显示 Agent 名称，点击进入代理节点详情
- 目标分支：`main`
- 相关记录：[实施计划](../superpowers/plans/2026-11-05-admin-agent-name-links.md)

## 摘要

管理后台此前在多个列表里把 `agent_id` 原样打印成裸 ID（托管路由列表、客户端详情的关联
Agent 与监听入口、服务令牌的绑定列与详情、审计日志中 `resourceType=agent` 的资源 ID），
另有一些位置只显示名称但没有任何回到 Agent 的入口（VPN peer 列表与详情、远程服务器列表与
详情）。运维要判断“这条路由/这个令牌到底挂在哪个节点上”只能读一串 ID，或者切回代理节点
页面对号。

本次把 Agent 引用的展示收敛到两个出口：

1. `web/src/stores/agentIndex.ts`：控制台内唯一一份 Agent 身份索引（Pinia setup store），
   加载复用既有 `loadAgentsForSelection(getAgents)` 游标分页；并发去重、失败后静默降级为
   裸 ID 且不再自动重试、`refresh()` 就地覆盖且成功前不清空旧名称。
2. `web/src/components/AgentRef.vue`：唯一渲染出口，`router-link` 指向 `/agents/{id}`，
   标签优先级为“调用方显式传入的名称 > 索引命中的名称 > 原始 ID”，`title` 恒为完整 ID，
   未解析时保持等宽 ID 观感。

随后 7 个视图改为使用该组件，并把四处各自下载 Agent 清单的重复请求收敛到共享索引；代理节点
页改名后显式 `refresh()`，其他页面下一次渲染即可看到新名称。客户端列表的 Agent 筛选由自由
文本框换成按名称/ID 可搜索的下拉框，提交给服务端的值仍是精确 Agent ID。

改动全部在前端展示层：不修改 Go 代码、API、OpenAPI、DDL、`SchemaVersion` 与协议帧，因此
不触发 Schema 升级与回滚流程。

## 用户影响

- 托管路由列表、客户端详情（关联 Agent 与监听入口）、服务令牌绑定列与详情、VPN peer 列表与
  详情、远程服务器列表与详情、审计日志的 agent 资源行统一显示 Agent 名称，单击进入该节点详情。
- 名称没有唯一约束，因此 ID 仍是权威标识：悬停显示完整 ID，筛选值、请求参数、排序与分页语义
  全部不变；审计日志中非 agent 资源类型继续显示原始 ID 与复制按钮。
- 读不到的 Agent（无权限或已删除）与索引请求失败时回退显示原始 ID，链接仍然可点，不会把一行
  数据隐藏或直接报错。
- 打开任意一个含 Agent 引用的页面，Agent 清单只请求一次；不展开客户端页的 Agent 下拉就不请求。

## API / Schema / 配置影响

- 无 API、OpenAPI、配置项与 Schema 版本变更；`GET /api/v1/agents` 的权限过滤保证索引不会
  暴露调用者本来看不到的 Agent。
- 新增前端模块：`web/src/stores/agentIndex.ts`、`web/src/components/AgentRef.vue`；
  `web/src/components.d.ts` 为 `unplugin-vue-components` 自动生成。
- 新增 i18n 键 `clients.searchAgents`（zh `输入名称或 ID 搜索` / en `Search by name or ID`），
  与既有 `tokens.searchAgents`、`vpn.form.searchAgents` 同措辞。
- 移除 `VpnPeers.vue` 的本地 `agentName()` 与 `RemoteServers.vue` 的本地 `agentName(row)`，
  以及 `Tokens.vue` 的 `formatAgentScope()` 字符串拼接路径（改为 `bindingAgentIds()` 返回 ID 数组）。
- 活文档同步：`docs/user-guide/server-admin.md` 的 Agent 列表、Client 观测、托管路由、远程服务器、
  VPN peer、审计日志与 Service Tokens 段落。

## 安全与授权影响

- 不新增数据来源：索引读的仍是权限过滤后的 `GET /api/v1/agents`，跳转目标 `/agents/:id` 为既有
  路由，越权访问由服务端详情接口拒绝。
- 名称来自服务端既有字段；远程服务器继续优先使用服务端投影的 `agentName`，前端不做二次判定。
- 日志、URL 与请求参数不新增任何 secret；Agent ID 本就可读，未扩大可见范围。

## 测试证据

红灯（实现前实测输出）：

```text
FAIL  src/tests/agent-index.spec.ts       Failed to resolve import "../stores/agentIndex"
FAIL  src/tests/agent-ref.spec.ts         Failed to resolve import "../components/AgentRef.vue"
FAIL  routes.spec.ts        > expected '<template>…' to contain '<AgentRef :agent-id="row.agentId" />'
FAIL  clients-view.spec.ts  > expected undefined to be 'edge-01'
FAIL  agent-token-workflows.spec.ts > expected '<template>…' to contain '<AgentRef'
FAIL  agent-token-workflows.spec.ts > expected '<template>…' to contain 'bindingAgentIds'
FAIL  vpn.spec.ts           > expected undefined to be 'edge-01'
FAIL  remote-servers-view.spec.ts > expected '<template>…' not to contain 'getAgents({ limit: 200 })'
FAIL  audit-logs-view.spec.ts > expected undefined to be truthy
FAIL  agents-view.spec.ts   > expected "spy" to be called with arguments: [ { cursor: undefined, limit: 500 } ]
FAIL  routes.spec.ts        > expected '<template>…' to contain 'useAgentIndex'
```

绿灯与门禁：

- `cd web && npm test -- --run`：66 个文件 503 项全部通过，含新增 `agent-index.spec.ts`（6 项）
  与 `agent-ref.spec.ts`（6 项）。
- 行为断言覆盖：名称与 `href=/agents/{id}`、`title` 为完整 ID、显式名称优先于索引、解析失败回退
  裸 ID 且仍可点击、一页 12 个引用只发一次清单请求、失败后不重试、改名后索引重新拉取、客户端
  筛选选中后以 `agentId: 'agent-edge'` 查询、审计行仅 agent 类型变为名称链接。
- `npm run build` 通过，`./scripts/verify-web-embed.sh` 报告 `web/dist` 与
  `internal/server/web_dist` 一致。
- `git diff --check` 干净。Go 侧无改动，`go test ./...`/`go vet ./...` 不在本次范围。
- `vue-tsc` 不是本仓库门禁；相对改动前基线新增 2 条 `Tokens.vue` 的
  `DefaultRow → ServiceToken`，与同文件其余 10 条同类 helper 调用完全同源（Element Plus 表格
  作用域行类型），未放宽 helper 签名。

## 发布步骤

1. 变更只影响管理后台静态资源，随 Server 二进制或镜像发布，无 DB、无配置、无迁移。
2. 发布后先核对三个页面：客户端详情（名称 + 跳转）、托管路由列表、审计日志（agent 行为名称链接、
   其余资源类型仍为可复制 ID）。
3. 再核对降级路径：把一个无权访问的 Agent 引用留在页面上（或用 DevTools 阻断 `/api/v1/agents`），
   确认显示为裸 ID、链接仍可点、不出现空白单元格。
4. 观察 `GET /api/v1/agents?limit=500` 的调用量：每个控制台会话应为 1 次，而不是此前四页各拉一份。

## 回滚步骤

- `git revert` 本次提交后重新 `cd web && npm run build` 并 `./scripts/verify-web-embed.sh`，
  替换 `web_dist` 并重启 Server 即可，5 分钟内完成；无数据与配置补偿。
- 局部止损：任务 3–7 的每个视图相互独立，可单独把 `<AgentRef>` 还原为原 `prop=` 列；索引与新组件
  不需要回退。
- 若出现索引请求风暴或名称错乱，优先回退任务 8（四处加载收敛 + 改名后 `refresh()`），它是唯一带
  行为副作用的改动；回退后视图继续各自 `loadAgentsForSelection`。

## Reviewer 关注点

- `agentIndex` 的 `error` 是终态：失败后不再自动重试，降级为裸 ID，恢复依赖用户刷新页面或
  `Agents.vue` 的显式 `refresh()`。请确认这个取舍（列表页可能有上百个引用单元，重试风暴比降级更难排查）。
- 模块级 `inflight` 变量做并发去重，跨 store 实例共享，settled 后立即置空；请确认 Pinia 的
  HMR/多实例场景不会残留。
- 空引用的渲染选择：`detailClient.agentIds` 为空时渲染 `<AgentRef :agent-id="''" />` 复用组件的
  `—`，而不是在视图里重复一份 em dash 标记。
- 客户端筛选的清单请求延后到下拉框展开（`@visible-change`）而非挂载即拉取，代价是首次展开可能有
   一次网络等待；换来的是不碰筛选的会话零额外请求。
- `Agents.vue` 改名后 `Promise.all([load(), agentIndex.refresh()])` 是两次独立分页拉取，未做
  “就地改名单条”优化；若清单规模成为问题，应在服务端提供按 ID 的批量投影而不是前端拼接。
- 名称可重复（`migrations/ddl.sql` 的 `agents.name` 无唯一索引），因此本次不改任何以 ID 为准的
  语义：筛选、排序、请求参数与详情页 `IdCell` 全部保持原样。

## 集成状态

- 实施计划已获用户确认后执行，9 个任务全部完成，勾选与偏差记录已补记在计划末尾。
- `npm test -- --run`、`npm run build`、`./scripts/verify-web-embed.sh`、`git diff --check` 通过。
- 文档索引已由 `python3 scripts/gen_doc_index.py` 重新生成。
- 尚未 commit、push 或创建远端 PR，等待用户授权。
