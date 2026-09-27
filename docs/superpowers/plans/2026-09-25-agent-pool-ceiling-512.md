# 实施计划：Agent 连接池上限抬到 512 与 0 语义定版

- **状态**：已确认执行（用户指令「`agent.connections.max` 上限改到 512」，并沿用既有「无需再次确认」授权）
- **规格引用**：上一轮计划 [Review P1–P3 加固](2026-09-24-review-p1-p3-hardening.md) 的 T7；[PR 记录](../../pull-requests/2026-09-25-review-p1-p3-hardening.md)「与计划的两处偏离」
- **分支**：`codex/vpn-phase6-server-data-plane`，基线 `c599cbd`

## 目标

让 Agent 侧连接池上限不再是 64，并把「容量/保留类键的 `0` 表示不限/永久」写成成文规则。

## 架构决策

- **D1 只抬 Agent 侧上限**：`agent.connections.max` 的校验上界 64 → 512（新增导出常量
  `AgentConnectionsCeiling`）。Client 侧 `client.connections.max` 的 16 不动——本轮只解决
  Server↔Agent 的池子瓶颈，扩大 Client 扇出会牵动选路 RTT 与 stream 分布，属另一问题。
- **D2 不改 `server.agents.max_connections_per_agent` 的 0 语义**：该键的 `0` 继续表示
  「取默认 64」而非「不限」。理由是它不是一个可以安全关闭的守卫：Server 必须有一个有限数
  才能回 ACK、才能给 `tunnelmesh_agent_connection_capacity` 一个水位、也才能界定单个身份
  能占的 map 槽位。需要更大池子的部署按 D3 显式取值即可。
- **D3 成文「0 的规则」并给出配套配方**：`retention_days=0` 永久、`http.max_connections=0`
  不限、stream 上限 `0` 不限；`max_connections_per_agent=0` 是唯一例外，取默认。抬升
  `agent.connections.max` 时的配方是「把 `max_connections_per_agent` 设成不小于它、至多 512」。
  两个键跨主机、生效顺序不保证，因此刻意不在启动时交叉校验，只以文档 + 可观测水位约束。

## 全局约束

- 无 Schema 变更（`schema_meta.version` 保持 16）；不改协议帧与枚举。
- 新语义必须同时进默认值表、校验文案、`configuration.md` 键表/章节与中英文档，并重跑文档索引。
- 1..512 仍是合法闭区间，513 必须给出「上限是多少」的可执行错误信息。

## 文件清单与任务间接口

| # | 文件 | 产出 |
| --- | --- | --- |
| A1 | `internal/config/config.go` | `const AgentConnectionsCeiling = 512`；`validateAgentConnections` 用它并更新文案；修正 `DefaultAgentMaxConnectionsPerAgent` 上方已失效的注释 |
| A2 | `internal/config/config_test.go` | 边界用例：`Max=64`（旧上界）必须通过、`Max=512` 通过、`Max=513` 拒绝，且 `min` 联动仍报错 |
| A3 | `docs/operations/configuration.md` | 「0 的规则」小节；`Agent 连接池` 章节补上限与配套配方；`max_connections_per_agent` 条目改写例外说明 |
| A4 | `docs/user-guide/agent.md`、`docs/en/user-guide/agent.md` | 各 1 句：上限 512，以及必须同步 Server 侧 |
| A5 | `docs/pull-requests/2026-09-25-agent-pool-ceiling-512.md` | 本轮增量记录（时点记录，不改写上一份） |

## TDD 步骤

1. A2 先改期望：`Max=64` 与 `Max=512` 期望通过、`Max=513` 期望 `agent connections max must be at most 512`，
   确认因当前上界 64 而红灯。
2. A1 引入常量并替换两处字面量，使其绿。
3. 复核没有别的地方假设「Agent 池 ≤ 64」（`internal/agent`、`internal/server` 已 grep 确认）。

## 验证命令

```
go build ./... ; go build -tags vpn ./... ; go vet ./... ; go vet -tags vpn ./...
go test ./internal/config ./internal/server ./internal/agent -count=1
go test ./... -count=1
gofmt -l internal cmd scripts deploy ; git diff --check
python3 scripts/gen_doc_index.py ; go test ./scripts -count=1
```

## 回滚注意事项

纯校验边界 + 文档：回滚即回退二进制，被抬高的 `agent.connections.max` 会重新被旧版本拒绝
（启动失败，不是静默降级），因此回滚前先把 Agent 侧调回 ≤64。无数据回滚。
