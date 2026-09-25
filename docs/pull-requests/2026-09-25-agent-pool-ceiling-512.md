# Agent 连接池上限抬到 512 与「0」语义定版

- **状态**：已实施（本分支），等待 review
- **类型**：配置契约 / 文档
- **关联计划**：[Agent 连接池上限抬到 512 与 0 语义定版](../superpowers/plans/2026-09-25-agent-pool-ceiling-512.md)

## 目标分支

`main`（源分支 `codex/vpn-phase6-server-data-plane`，基线 `c599cbd`）。

## 摘要

上一轮记录「与计划的两处偏离」时，`server.agents.max_connections_per_agent` 的默认值取 64 的理由是
「`agent.connections.max` 的上限就是 64，降到 16 会拒掉既有合规部署」。本轮按运维要求把 Agent 侧
上限抬到 512，并把「0 表示不限/永久」写成成文规则：

1. `agent.connections.max` 校验上界 `64` → `512`，改为具名常量 `config.AgentConnectionsCeiling`，
   拒绝文案随常量生成（`agent connections max must be at most 512`）。
2. 保留与容量类键的 `0` 统一为「关掉这条限制」：`server.audit.retention_days=0` 永久保留、
   `server.http.max_connections=0` 不限连接、`server.stream.max_active_per_agent=0` 与
   `agent.streams.max_active=0` 不限活跃流。
3. `server.agents.max_connections_per_agent=0` **保持**「取默认 64」，作为成文规则的例外并在文档写明理由。

## 用户影响

- 之前 `agent.connections.max` 大于 64 的配置在启动期就被拒绝，现在合法；`min`/水位/间隔等其余约束不变。
- 只有把池子抬过 Server 侧默认 64 的部署需要动 `server.agents.max_connections_per_agent`，
  否则会看到新连接被拒并退避重连（既有连接不受影响）。
- 未配置任何新键的部署行为完全不变。

## API / Schema / 配置影响

- 无 Schema 变更（`schema_meta.version` 保持 16），无协议帧或枚举变化。
- 无新增配置键，只改一个既有键（本轮新增、未发布）的合法区间。
- `client.connections.max` 的 16 上限**未动**：扩大 Client 扇出会牵动选路 RTT 与 stream 分布，是另一个问题。
- 文档已同步：`docs/operations/configuration.md`（「`0` 的规则」、两键关系与抬池配方、
  `Agent 连接池` 区间说明）、`docs/user-guide/agent.md`、`docs/en/user-guide/agent.md`。

## 安全与授权影响

- 未放宽任何鉴权。`max_connections_per_agent` 仍是按 **Agent 身份** 计数的守卫：一条泄漏的凭据
  不能占满整节点。它不给「不限」取值，因为 Server 需要一个有限数才能回 ACK、才能作为
  `tunnelmesh_agent_connection_capacity` 水位暴露。
- 该键不设上界校验是有意的：一个逻辑身份可由多个物理进程共享（`instance_id` 区分），
  容量要按「同身份进程数 × 每进程 `connections.max`」估算，允许高于 512。
- 两键不启动期交叉校验（不同主机、生效顺序不保证），约束以文档 + 指标水位表达。

## 测试证据

先红后绿的真实过程：`Max=512`/`Max=513` 两条新用例在当前实现上失败，原因为
`agent connections max must be at most 64`；改完上界后同两条通过。

```bash
go test ./internal/config -count=1 -run AgentConnection      # 先红（at most 64），后绿
go build ./... ; go build -tags vpn ./...                    # 通过
go vet ./... ; go vet -tags vpn ./...                        # 通过
go test ./internal/config ./internal/cli ./internal/agent -count=1   # 通过
go test ./... -count=1                                       # 通过
gofmt -l internal cmd scripts deploy ; git diff --check       # 无输出
python3 scripts/gen_doc_index.py && go test ./scripts -count=1   # 通过
```

未跑真实 MySQL 5.6（本机无 Docker）；本轮不涉及 SQL，排序与保留逻辑无改动。

## 发布步骤

替换二进制即可。若某部署打算用超过 64 的 Agent 池，先改 Server 的
`server.agents.max_connections_per_agent`（或按身份共享情况估算），再改 Agent 的
`agent.connections.max`，然后重启两侧；观察 `tunnelmesh_agent_connection_capacity` 与
`agent connection capacity reached` 是否成对出现。

## 回滚步骤

无数据回滚。注意回滚到旧二进制后，`agent.connections.max > 64` 会被旧版本**拒绝启动**
（不是静默降级），所以回滚前先把 Agent 侧调回 ≤64。

## Reviewer 关注点

- `internal/config/config.go` 的 `AgentConnectionsCeiling` 是否只被 Agent 侧用到（Client 仍是 16）。
- 为什么 `max_connections_per_agent` 的 `0` 不跟随「不限」惯例：见「与计划的两处偏离」的 D2 论证。
- 文档里两键关系的措辞：不得暗示存在启动期交叉校验。

## 集成状态

已提交并推送到 `codex/vpn-phase6-server-data-plane`，未合入 `main`。
