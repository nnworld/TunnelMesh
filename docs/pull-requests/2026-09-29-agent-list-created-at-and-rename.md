# 代理节点列表时间列、翻页、名称筛选与编辑

## Title

`feat(agents): time columns, cursor paging, name filter and editing`

## Target branch

`main`

## 摘要

代理节点列表补齐运维需要的三件事：创建时间与更新时间两列、按创建时间倒排的每页 20 条翻页、名称/ID 筛选框；并在操作列增加“编辑”，管理员可改名称与启用状态。排序与过滤下沉到服务端 SQL，使分页、过滤、排序三者语义一致，同时消除非管理员列表“先全局分页再内存丢弃”的既有缺陷。启用开关此前只能在创建时设定，要把一个节点摘出服务必须删除重建（连带失去 token 绑定与访问策略），本次补上这个入口。

## 用户影响

- 第一页就是最新接入的节点，不必在几百行里翻找；两列时间可点表头排序（当前页内）。
- 每页 20 条 + 下一页/上一页，请求量从一次 500 行降到 20 行。
- 名称筛选框按名称或 ID 子串匹配，命中项不会因分页而被隐藏。
- 重命名与启停都不再需要删除重建（删除会连带 token 绑定与访问策略）；编辑框按行回填当前启用状态，改名不会变成一次隐式重新启用。
- 非管理员现在只按所有者条件取页，历史实现下其结果依赖全局前 500 行的顺序，可能看不到自己的节点。

## API、Schema 与配置影响

- `GET /api/v1/agents` 新增可选 `keyword`；排序由 `ORDER BY id` 变为 `ORDER BY created_at DESC, id DESC`，游标随之改为 `(created_at,id)` 复合值。响应字段、分页参数语义（cursor、limit）不变。
- 无 Schema 变更：不动 `agents` 表结构、不提升 `schema_meta.version`、无增量 DDL；也未新增索引（管理面小表，见计划第 2 节）。
- 无配置项变化。
- 无新增接口：启停复用既有 `PATCH /api/v1/agents/{agentId}`。OpenAPI 为该动词补了 `AgentPatchRequest`（原 `AgentRequest` 标注 `required: [name]`，与 PATCH“省略即保持原值”的语义矛盾），并写明禁用的实际后果。

## 安全与授权影响

- 权限过滤进入 SQL：非管理员的页只含 `owner_user_id = principal`，游标不再可能指向其看不到的行。
- `keyword` 经 `boundedAgentFilter` 截断 128 字节并剔除 `\x00`，只做参数绑定的 `INSTR` 比较，不拼接 SQL。
- 重命名与启停沿用管理员专属 `PATCH`，审计仍写 `agent.updated`；请求体只含名称与开关，不含凭据。非管理员在 UI 看不到入口，服务端返回 403（用例覆盖）。

## 测试证据

红灯（实现前）：

- `go vet ./internal/storage` → `undefined: AgentListFilter`（新契约先失败）。
- 摘掉 handler 的 `keyword` 透传后：`TestListAgentsKeywordFiltersInSQL` → `keyword page = [agent-keyword-3@… agent-keyword-2@… agent-keyword-1@…], want the two searchable agents`。
- Web 编辑框开关先写 2 条用例 → `carries the administrative switch through the edit dialog` 报 `switch in the edit dialog: expected null to be truthy`；改名用例断言 `{ name, enabled }` 载荷时报 `expected "spy" to be called with arguments: [ 'agent-newest', …(1) ]`（红）。实现后转绿。
- `npm test -- --run src/tests/agents-view.spec.ts` → `requests 20 rows per page and walks the cursor forward and back` 报 `expected last "spy" call to have been called with [ ObjectContaining {"limit": 20} ]`；`sends the name filter and returns to the first page` 报 `Cannot read properties of undefined (reading 'click')`。

绿灯：

- `TestUpdateAgentNameAndEnabledViaPatch` 钉住控制台依赖的既有 `PATCH` 合并语义（名称与开关同时提交、省略 `name` 不清空、非管理员 403）。它覆盖的是本轮之前就存在的服务端行为，属回归护栏，因此没有红灯阶段。
- 契约套件（SQLite 与 MySQL 5.6 同跑）新增断言：倒排首页为最新两条、游标第二页不重不漏、`Keyword` 与 `OwnerUserID` 在 SQL 内生效、按 1 行逐页走完带过滤的三行结果顺序正确。
- `go test ./internal/storage ./internal/server ./internal/auth -count=1` → ok。
- `npm test -- --run` → 40 文件 / 347 用例通过（vitest 5.0.1）；`npm run build` + `scripts/verify-web-embed.sh` → 产物一致。
- 全量门禁结果见“集成状态”一节。

过程中修正的测试自身问题（非实现缺陷）：夹具所有者常量写重、跨所有者游标的期望值不成立、空结果不渲染翻页控件导致“回到第一页”用例无法起步。另有一处真实连带：`internal/auth` 的 `memoryAgents` 未同步 `List` 签名时以 `credential repositories are required` 失败（该 fake 走运行时类型判定，编译期不报错）。

## 发布步骤

1. 合并后发布 Server；控制台产物随二进制 embed。
2. 观察一次管理后台的代理节点页：确认第一页为最新节点、翻页与筛选可用。
3. 游标由前端每次请求携带，灰度期间新旧页面并存也不会串页（见回滚）。

## 回滚步骤

- `git revert` 合并提交。前端列与控件同时消失，无数据影响。
- 游标格式变化是单向兼容的：新版本可读旧游标；旧版本把新复合游标当非法游标处理并回到首页，不会报错或给出错误集合，因此无需清浏览器状态。
- 代价是恢复“按 id 分页 + 非管理员内存过滤”的旧行为。

## Reviewer 关注点

- 编辑框每次同时提交 `name` 与 `enabled`：服务端 `PATCH` 已是合并语义，只发改名也不会误动开关；一次提交完整意图是为了“所见即所写”，也让审计记录与界面操作一一对应。若希望两个字段各自独立保存，需拆成两次交互。
- 创建对话框的启用开关标签由 `agents.status` 归一为 `agents.enabledColumn`，与表格“状态/启用”两列口径一致，属顺带的措辞修正，无行为变化。
- 页键为什么必须含 `id`：`created_at` 可同秒碰撞，只用它会在页边界重复或漏行；对照 `client_instance_metadata` 的复合游标实现是否一致。
- `created_at` 是 TEXT 存放的 RFC3339：SQLite 与 MySQL 都按文本比较。`ORDER BY` 与游标谓词用的是同一个文本比较，因此即便某行整秒、另一行同秒带小数（`tm()` 用 RFC3339Nano，尾随零被截断）导致二者文本序与真实时间序相反，翻页也不会在页边界重复或漏行，只是这一对的先后次序可能不符合直觉。统一为 `tmFixed()`（固定九位小数）需要同时改写既有行的存储格式，新旧混存反而会引入同类偏差，故留作独立的后续变更。
- `INSTR` 子串匹配的大小写敏感性随驱动/排序规则不同，是否与客户端列表的既有口径一致可接受。
- 未加 `agents(created_at)` 索引：管理面小表 + 现有 `idx_agents_owner`。若节点规模上到万级需要补索引，属 Schema 变更须另开版本。
- 翻页是游标栈方案（无总数、无法跳到任意页），若产品要页号跳转需服务端提供计数，属另一项变更。

## 集成状态

- 计划记录（补记）：`docs/superpowers/plans/2026-09-29-agent-list-created-at-and-rename.md`。
- 基线：最初基于 `34ca4fb`（已随 PR #29 进入 `main`）。实现期间 `main` 前进了 252 个文件（VPN phase 6、依赖升级等），已 `git rebase --onto origin/main` 重放到 `100f171`；只有两个由脚本生成的文档索引冲突，用 `scripts/gen_doc_index.py` 重新生成解决。下列结果均为重放之后。
- `go test ./... -count=1 -timeout 30m` → 30/30 包 ok，退出码 0。
- `go test -race ./internal/server ./internal/storage ./internal/auth -count=1 -timeout 25m` → 退出码 0，无 `DATA RACE`（server 589.9s、auth 102.2s、storage 15.9s）。
- 重放前另跑过 `go test -race ./... -count=1 -timeout 25m` → 29/29 包 ok。
- `go build ./...`、`go vet ./...` 无输出；`gofmt -l internal cmd` 为空；`git diff --check` 干净。
- Web 依赖随上游升级（vitest 5.0.1、vue 3.5.43、jsdom 30），需先 `npm ci`；`npm test -- --run` → 40 文件 / 347 用例通过；`npm run build` 后 `bash scripts/verify-web-embed.sh` → `web/dist and internal/server/web_dist match`。
- 语义合并复核：上游 `findAgent` 用 `CreatedAt.After` 显式择新、不依赖页序，排序改动对其无影响；`internal/server/token_api_test.go` 的两个 fake 以内嵌接口方式实现 `AgentRepository`，签名变更对其透明。
- `docs/api/openapi.yaml` 经 YAML 解析校验通过（回填期间曾发现新增描述里的 `test: ` 冒号加空格在明文标量中非法，已改写措辞修复；上游基线本身可解析）。
- 验证环境提示：`deploy/install/oneclick` 脚本套件有从标准输入读取的用例，在带控制终端的会话里跑会阻塞；用 `</dev/null` 脱离终端即与 CI 一致（1.9s 通过），非代码缺陷。
