# 容量类拒绝的 error_class 与 Agent 侧上限错配告警

- **状态**：已实施（本分支），等待 review
- **类型**：可观测性缺陷修复 / 文档
- **补记说明**：本轮变更在验证上一份记录（[Agent 连接池上限抬到 512](2026-09-25-agent-pool-ceiling-512.md)）
  的过程中发现，先实现后补记，因此下文只记录已验证事实，不含未执行的预期。

## 目标分支

`main`（源分支 `codex/vpn-phase6-server-data-plane`，基线 `c583bdf`）。

## 摘要

两件事，都是上一轮加固留下的「看得见但看不懂」问题：

1. **准入拒绝被归成 `error_class="internal"`**。`NormalizeErrorClass` 没有 capacity 分支，
   于是 `session: agent connection capacity reached`、`agent relay: active stream capacity reached`、
   `agent: dial queue full` 全部落到 `default`。指标和结构化事件里，「运维该抬上限」与
   「代码有 bug」共用一个标签。现在统一归到 `capacity`（匹配 `capacity` 与 `queue full`），
   类别仍是封闭枚举，不引入请求可控的基数。
2. **Agent 收到 Server 回传的上限后把它丢了**。`connectionPoolHandler` 解析 metadata ack，
   但只读 `ConnectionPoolSupported` 与 `Capabilities`，`MaxConnectionsPerAgent` 无人使用。
   两个键在不同主机上、启动期刻意不交叉校验，因此这条 ack 是 Agent 侧唯一的自动发现点。
   现在它在「Server 强制值 < Agent 配置值」时输出一条 WARN，每个不同上限值只说一次。

### 上一轮的一条错误陈述

上一轮我在最终回答里给出的「顺带发现」引用了 `internal/server/dashboard.go`、
`applyAgentConnectionCapacity` 以及一句「当前已建立 N / 配置的 M」文案。核实结果：
**仓库里不存在这些文件、函数和文案**（Dashboard 后端在 `internal/server/api.go`，
Agent 详情的连接数由 `web/src/views/AgentDetail.vue` 渲染）。因此该建议不成立，本轮没有按它改 UI，
改为上面两项经代码核实的事实修复。

## 用户影响

- 只影响日志与指标标签，不影响任何协议、API 响应结构或授权判定。
- 依赖 `error_class="internal"` 兜底统计的自建看板，容量类拒绝会从 `internal` 移到 `capacity`。
  仓库内的 `recording-rules.yaml` 与 `alert-rules.yaml` 没有按 `error_class` 过滤，不受影响。
- Agent 侧多一条 WARN，仅在配置错配时出现，且每个不同上限值一次。

## API / Schema / 配置影响

- 无 Schema 变更（`schema_meta.version` 保持 16），无新配置键，无 API 字段变化。
- `tunnelmesh_agent_connection_errors_total`、`tunnelmesh_connections_total{mode="agent"}`
  与结构化事件的可能 `error_class` 取值集合新增 `capacity`。
- 文档同步：`docs/operations/observability.md`（新增「capacity 是所有准入拒绝的统一类别」段，
  并写明 `rejected` 是结果、`capacity` 是原因）、`docs/operations/configuration.md`
  （抬池配方补 WARN 文案与指标/审计去向）、`docs/user-guide/agent.md`、
  `docs/en/user-guide/agent.md`。

## 安全与授权影响

无授权面变化。WARN 只输出 Agent ID、连接 ID 与两个数字，不含 Token、目标地址或凭据；
审计条目沿用既有 `agent.connection.rejected`，其错误详情本就脱敏。

## 测试证据

先红后绿，均为实际输出：

```text
--- FAIL: TestNormalizeErrorClassIsBounded/connection_capacity
    NormalizeErrorClass() = "internal", want "capacity"
--- FAIL: TestNormalizeErrorClassIsBounded/stream_capacity
    NormalizeErrorClass() = "internal", want "capacity"
--- FAIL: TestNormalizeErrorClassIsBounded/dial_queue_full        （同类）
internal/agent/connection_pool_ack_test.go:40:30: unknown field agentID in struct literal
internal/agent/connection_pool_ack_test.go:40:55: unknown field configuredMax in struct literal
```

修复后：

```bash
go build ./... ; go build -tags vpn ./... ; go vet ./... ; go vet -tags vpn ./...   # 通过
go test ./... -count=1            # 25 个包 ok
go test -tags vpn ./... -count=1  # 通过
go test -race ./internal/agent ./internal/observability ./internal/server -count=1   # 无 DATA RACE
gofmt -l internal cmd scripts deploy ; git diff --check ; python3 scripts/gen_doc_index.py   # 干净
```

新增用例：`internal/observability/events_test.go` 三条类别断言；
`internal/agent/connection_pool_ack_test.go` 两条（错配必告警且去重；上限够用、上限相等、
老 Server 省略该字段、单连接池四种情形必须沉默）。未跑真实 MySQL 5.6（本机无 Docker），本轮不涉及 SQL。

## 发布步骤

替换二进制即可。上线后若要确认是否有部署踩到错配：
`journalctl -u tunnelmesh-agent | grep 'server ceiling'`，或
`increase(tunnelmesh_connections_total{component="server",mode="agent",result="failed",error_class="capacity"}[15m])`。

## 回滚步骤

`git revert` 对应提交即可；回滚只会把 `capacity` 类别退回 `internal`、并去掉那条 WARN，
不会改变任何连接是否被接受。无数据回滚。

## Reviewer 关注点

- `NormalizeErrorClass` 的分支顺序：`capacity`/`queue full` 必须在 `refused`（target_unavailable）
  之前、在 `policy` 之后，且不能吃掉既有类别。
- `checkServerCeiling` 用 `serverMax <= 0` 区分「老 Server 省略字段」与「上限为 0」，
  后者在协议上不可能出现（`omitempty` + Server 侧默认值归一）。
- WARN 是 per-connection handler 去重（一个物理连接一条），不是全局一次：
  刻意如此，避免跨连接串状态，也保证每条连接所属 Server 节点的上限都能被看见。

## 集成状态

已提交并推送到 `codex/vpn-phase6-server-data-plane`，未合入 `main`。
