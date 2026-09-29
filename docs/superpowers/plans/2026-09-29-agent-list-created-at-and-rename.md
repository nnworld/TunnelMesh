# 代理节点列表时间列、翻页、名称筛选与重命名实施计划

状态：**补记计划**。实现与验证先于本文完成，第 6 节的红灯输出为实现过程中真实记录的结果，不含推测。

日期：2026-09-29

## 1. 目标

- 列表展示创建时间与更新时间，默认按创建时间倒排，最新注册的节点在第一页。
- 列表按每页 20 条翻页，支持下一页/上一页。
- 增加名称筛选框。
- 操作列增加“编辑”，管理员可改名称与启用状态，不必删除重建节点。

## 2. 非目标

- 不改 `Agent` 表结构，不提升 `SchemaVersion`，不新增索引（`agents` 是管理面小表，全表扫描成本可接受；索引优化另议）。
- 不新增协议 frame、不改 API 响应字段：`createdAt`/`updatedAt` 与 `PATCH /api/v1/agents/{agentId}` 早已存在。
- 不做 offset 分页：列表接口一律 cursor 分页。
- 不放开非管理员改名：授权在服务端，UI 不制造必然 403 的入口。

## 3. 根因与决策

三点诉求互相牵制，决定了改动必须落到服务端：

1. `agentRepo.List` 原本是 `ORDER BY id` + `WHERE id>?` 游标。若只在浏览器排序，第一页取到的是**id 最小**的一批节点，再按创建时间倒排也只是这一页内部的顺序——与“最新接入的节点排最前”相反。
2. 一旦每页 20 条，浏览器侧的“筛选名称”同样失真：命中项若不在当前页就看不见。
3. 因此排序、过滤、分页必须在同一条 SQL 里成立。

决策：

- 页键取 `(created_at DESC, id DESC)`。`created_at` 不唯一，`id` 必须参与键，否则同一秒内的多行在页边界上会被重复或漏掉；游标即这一对的编码，与 `client_instance_metadata` 的复合游标同形（复用 `decodeCompositeCursor`）。
- 两列都是存放 RFC3339 UTC 的 TEXT，数据库比较顺序与界面展示顺序一致，MySQL/SQLite 同构。
- `keyword` 走 `INSTR(name, ?)>0 OR INSTR(id, ?)>0`，复用 `boundedAgentFilter` 截断 128 字节并剔除 `\x00`。大小写敏感性随驱动不同（SQLite 二进制比较、MySQL 随列 collation），与既有客户端列表口径一致。
- 非管理员的可见性改为 SQL 谓词。旧 `listAgentsForOwner` 每次拉 500 行、在内存里丢掉别人的行，既可能漏掉 500 行之外属于该用户的节点，又会返回一个指向其看不到的行的游标；这与 `AGENTS.md`“权限过滤必须在分页语义内完成”相悖，本次一并消除。
- 翻页游标栈放在视图里：cursor 分页没有页号，`上一页` 只能靠记住每页用过的游标原样重放，而不是重新扫描。

## 4. 文件清单

- `internal/storage/agent_contract_test.go`（新增）、`internal/storage/storage_contract_test.go`：`runAgentListRepositoryContract`，随契约套件在 SQLite 与 MySQL 5.6 上同跑。
- `internal/storage/repository.go`：`AgentListFilter{OwnerUserID,Keyword}`、`AgentRepository.List` 签名、`agentRepo.List` 的过滤 + `(created_at,id)` 倒排序与游标。
- `internal/server/agent_list_status_test.go`：倒排翻页不重不漏、`keyword` 在 SQL 内过滤、非管理员分页只见自己的节点；另加 `TestUpdateAgentNameAndEnabledViaPatch`，钉住控制台依赖的 `PATCH` 合并语义（名称与开关同时提交、省略 `name` 不清空、非管理员 403）。
- `internal/server/api.go`：`ListAgentsFiltered`、`listAgentsForOwner` 改谓词版、`listAgents` 读取 `keyword`。
- `internal/auth/credential_service_test.go`：`memoryAgents.List` 跟随接口签名（接口不匹配在该处是运行时判定，必须同步）。
- `web/src/api/client.ts`：`getAgents` 增 `keyword`；`updateAgent` 走 `PATCH`，载荷类型 `Partial<AgentCreateInput>`（已含 `enabled`）。
- `web/src/views/Agents.vue`：筛选表单、创建/更新时间两列、每页 20 的游标翻页、编辑对话框（名称 + 启用开关）、`<style scoped>`；创建对话框的开关标签由 `agents.status` 归一为 `agents.enabledColumn`，避免与“连通性状态”列混淆。
- `web/src/i18n/messages/{zh-CN,en-US}.ts`（新增 `agents.enabledHint`）、`web/src/tests/agents-view.spec.ts`。
- `docs/user-guide/server-admin.md`、`docs/api/openapi.yaml`（`GET` 的 `keyword` 与排序说明；`PATCH /api/v1/agents/{agentId}` 改用新增的 `AgentPatchRequest` 并写明禁用的实际后果）。

## 5. 接口约定

- `GET /api/v1/agents?limit=20&cursor=<opaque>&keyword=<substring>`；响应字段不变，`nextCursor` 为 `(created_at,id)` 编码。
- `PATCH /api/v1/agents/{agentId}` 仅管理员；请求体省略的字段保持原值（沿用既有实现），控制台每次同时提交 `name` 与 `enabled`，把界面显示的完整意图一次说清，避免“改名被理解成顺带重新启用”。
- `storage.AgentRepository.List(ctx, AgentListFilter, cursor, limit)`：`filter` 零值即全量；游标与过滤条件成对使用。

## 6. TDD 步骤与结果

1. 存储契约先写 `runAgentListRepositoryContract` → `go vet ./internal/storage` 报 `undefined: AgentListFilter`（红）。实现过程中该套件又抓出两处**测试自身**的错误：夹具里 `agent-list-c` 的所有者与过滤值写成同一常量；“跨所有者重放游标”的期望值不成立（别人的行更新于游标位置，返回空页才是正确行为）。改为“带过滤条件逐页走完不重不漏”后才是要断言的性质。
2. server 层三条用例实现后全绿；为取真实红灯证据，临时把 handler 的 `keyword` 透传摘掉 → `TestListAgentsKeywordFiltersInSQL` 失败：`keyword page = [agent-keyword-3@… agent-keyword-2@… agent-keyword-1@…], want the two searchable agents`，恢复后转绿。
3. Web 先写 2 条用例 → `requests 20 rows per page…` 报 `expected last "spy" call to have been called with [ ObjectContaining {"limit": 20} ]`，`sends the name filter…` 报 `Cannot read properties of undefined (reading 'click')`（实现前无翻页控件）。实现后 5 passed；期间修正一处测试前提：空结果渲染空态、没有翻页控件，用例需带一行数据才能验证“筛选后回到第一页”。
4. 编辑框加启用开关：先写 2 条用例 → `carries the administrative switch through the edit dialog` 报 `switch in the edit dialog: expected null to be truthy`，改名用例报载荷缺 `enabled`（红）。实现后转绿。`TestUpdateAgentNameAndEnabledViaPatch` 覆盖的是既有服务端行为，属回归护栏而非新实现，一次通过。
5. `memoryAgents` 未随接口更新时，`internal/auth` 以 `credential repositories are required` 失败——该 fake 是运行时类型判定，编译期不报错，已同步签名。

## 7. 验证命令

```bash
npm ci   # web 依赖随上游升级（vitest 5、vue 3.5.43、jsdom 30），换基线后必须先装
go test ./internal/storage ./internal/server ./internal/auth -count=1
go test ./... -count=1 -timeout 30m
go test -race ./... -count=1 -timeout 25m   # 必须 </dev/null：oneclick 脚本套件有从 stdin 读取的用例，带控制终端会阻塞
go vet ./...
git diff --check
cd web && npm test -- --run && npm run build
cd .. && bash scripts/verify-web-embed.sh
```

## 8. 回滚

- 前端与文档可直接 `git revert`。
- 服务端回滚会同时恢复旧的非管理员内存过滤分页；游标格式变化是**单向**的：新版本可读旧游标（`decodeCompositeCursor` 对单值游标退化为整表首页之后的位置），旧版本读到新复合游标会按非法游标处理并回到首页，不产生错误数据。发布前无需清空游标。
