# Review P1–P3 加固与客户端观测页排序

- **状态**：已实施（本分支），等待 review
- **类型**：安全加固 / 容量护栏 / 可观测性 / 文档
- **关联计划**：[实施计划：Review P1–P3 加固与客户端观测页排序](../superpowers/plans/2026-09-24-review-p1-p3-hardening.md)

## 目标分支

`main`（源分支 `codex/vpn-phase6-server-data-plane`，基线 `a918f16`）。本轮不开 PR 链接，
按用户指令直接提交并推送到该分支。

## 摘要

修掉上一轮 review 的 9 项（P1×2、P2×4、P3×3），并让客户端运行观测页按最近心跳倒序：

| # | 优先级 | 结果 |
| --- | --- | --- |
| T1 | P1 | `tcp`/`udp`/`http` 隧道的非回环 `listen` 必须显式 `allow_remote: true`（CLI `--allow-remote`），配置校验与启动双向拒绝 |
| T2 | P1 | 单 Agent 活跃流上限 `server.stream.max_active_per_agent` + Agent 进程侧 `agent.streams.max_active`，超限以既有 `queue_full` 拒绝，不新增 wire 枚举 |
| T3 | P2 | 本地监听器 accept/read 循环永久失败经 `Err()` 上抛，`run` 与单命令 `forward` 立即非零退出，不再留下「端口在但收不到流量」的哑监听器 |
| T4 | P2 | 管理入口新增 `server.http.*`（读头/空闲 keep-alive/头部上限/请求体/并发连接准入），默认值不改变已升级部署的行为，WebSocket hijack 不受影响 |
| T5 | P2 | `docs/deployment/nginx.md` 补齐 WS/兜底 location 的 `limit_req`/`limit_conn`，`429` 显式化，新增「限流与容量取值」章节 |
| T6 | P2 | `server.audit.retention_days`（默认 `0` 永久保留）+ 分批幂等清理器 |
| T7 | P3 | `server.agents.max_connections_per_agent` 取代硬编码 64 并真正强制，ACK 回显真实上限 |
| T8 | P3 | Agent/Client WebSocket 在 hello 后按心跳周期重新授予空闲读预算（`3 × 30s`），不再清零后永不超时 |
| T9 | P3 | `server.metrics.token` 可选 Bearer 门禁（`401` + `WWW-Authenticate`），留空即旧行为；`prometheus.yml.example` 去掉「需要认证的管理入口」这一不存在暗示 |
| T10 | 需求 | Clients 列表按 `last_seen_at DESC, id DESC` 排序，游标编码 `last_seen_at + id` |

## 用户影响

- **可能被拒的两种新配置**：非回环 `tcp`/`udp`/`http` 监听（无 `allow_remote`）与负数上限。
  既有回环部署、既有 `0.0.0.0` 部署若不写 `allow_remote` 会在 `check-config` 阶段报错——这是有意的
  P1 门禁，修复方式是显式确认或改回回环。
- **观测页顺序变化**：最近心跳的实例在前，不再按「被清扫器碰过的时间」排。前端无接口变更。
- **`/metrics`**：默认仍匿名可抓取；只有显式配置 token 才开始要求 `Authorization: Bearer`。
- **审计**：默认永久保留，清理只在正数配置下发生。
- VPN 数据面口径不变：代码在树内、仅 `-tags vpn` 编译、`server.vpn.enabled` 默认 false、阶段 8 未完。

## API / Schema / 配置影响

- **无 DDL、无 `schema_meta.version` 变更**（保持 16），因此不需要增量脚本。
- 新增配置键（默认值均等价旧行为，除 T1 门禁外）：
  `server.http.read_header_timeout`(10s)、`server.http.idle_timeout`(120s)、
  `server.http.max_header_bytes`(1 MiB)、`server.http.body_timeout`(30s)、`server.http.max_connections`(0=不限)、
  `server.stream.max_active_per_agent`(1024)、`agent.streams.max_active`(1024)、
  `server.agents.max_connections_per_agent`(64)、`server.audit.retention_days`(0)、
  `server.metrics.token`(空，仅 env)、`{tcp,udp,http}.allow_remote`(false)。
- `GET /api/v1/clients` 的排序语义写入 `docs/api/openapi.yaml`，响应结构不变。
- 全部新键已进默认值表、校验、BindEnv 白名单、`docs/operations/configuration.md`
  键表与「管理入口的超时、上限与保留策略」章节，并在 `config-examples.md`、
  `client-configuration-examples.md`、中英 `user-guide` 同步。
- 新指标维度：`tunnelmesh_connections_total{component="server",mode="http",result="rejected",error_class="capacity"}`，
  标签仍是封闭枚举，`result` 值域已包含 `rejected`，未新增标签名。

## 与计划的两处偏离

1. **T7 默认 64 而非计划回滚段写的 16**：`agent.connections.max` 的允许上限就是 64，
   把 Server 侧默认降到 16 会直接拒掉既有合规部署（一个跑满 64 连接的 Agent 会掉线）。
   默认值改为「沿用现网事实上限 64」，运维可按需下调；强制逻辑与可配置性是计划要求的。
2. **T4/T6 的 0 语义**：`max_connections: 0` 与 `retention_days: 0` 都是「不限/永久」，
   即保持历史行为；正数才是显式收紧。计划正文 D5 已如此，回滚段一并说明。

## 安全与授权影响

- T1 关闭的是「无凭据把内网服务发布到非回环网段」这条路；`socks5`/`http-proxy` 的既有
  口令要求不变。
- `server.metrics.token` 只允许环境变量注入，`json/yaml` 序列化省略该字段，`config dump`
  与 `RedactedJSON` 输出 `[redacted]`，日志只记「是否配置」。健康探针不受该门禁影响。
- 审计清理只输出删除条数与耗时，不含行标识；`PurgeOlderThan` 带时间边界，多节点重复执行删 0 行。
- Token reveal、traceroute 脱敏、Agent metadata allowlist、Agent 侧 SSRF/私网校验语义均未改动。
- 文档与示例继续只用 `apps.example.com` / `tm.example.com` / `tunnel.example.com` 一类示例域。

## 测试证据

以下均为本轮实际执行过的命令与真实结果（本机无 Docker，未跑真实 MySQL 5.6，
MySQL 分支仅由既有契约测试与 `deploy/mysql56` 静态用例覆盖）：

```bash
go build ./... ; go build -tags vpn ./...             # 通过
go vet ./... ; go vet -tags vpn ./...                 # 通过
gofmt -l internal cmd scripts deploy                  # 无输出
go test ./... -count=1 -timeout 900s                  # 全部 ok
go test -tags vpn ./... -count=1 -timeout 1800s       # 第一次 internal/server FAIL（见下），修复后全部 ok
go test -tags vpn ./internal/server -count=10 -run '...新增用例'   # ok
go test ./internal/client ./internal/agent ./internal/cli ./internal/config ./internal/storage -count=10   # ok
go test -race ./... -count=1 -timeout 2400s            # 25 个包 ok，无 DATA RACE
cd web && npm test -- --run                           # 341 passed
cd web && npm run build && ./scripts/verify-web-embed.sh   # 产物与 embed 一致
python3 scripts/gen_doc_index.py && go test ./scripts -count=1   # ok
git diff --check                                      # 无空白错误
```

**过程中发现并修掉的两处测试竞态**（都不是产品缺陷，但都会在机器繁忙时误报）：

- `TestNewServerRuntimeStartsAuditRetentionOnlyWhenConfigured` 原先断言「显式 `SweepOnce`
  删除 1 行」，而 `NewServerRuntime` 的启动清扫是 goroutine，它先跑完时显式 pass 只能删 0 行。
  `-count=1` 下靠时序侥幸通过，`-tags vpn` 全量套件里被抓到一次失败。改为断言「配置产生了
  worker + 窗口取值正确 + 停止不挂起 + 最终无过期行」，行数语义留给 sweeper 自身的确定性单测。
- `internal/agent/stream_capacity_test.go` 原先用「2s 内没等到 dial」和「300ms 内没有第三次
  dial」做断言，`-race` 多包并发下被抓到一次误报（`-count=5` 也能稳定复现）。改为等 `OPEN_RESULT` 帧本身：接受必须是
  `Accepted=true/OK`，超限必须是 `Accepted=false/queue_full`，并用原子计数确认被拒的 open
  一个目标连接都没建。时限只作为 30s 的挂死保险，不再是时序假设。

## 发布步骤

1. 直接替换二进制（无 Schema 变更，不需要 expand/contract）。
2. 若部署里有非回环 `tcp`/`udp`/`http` 监听，先补 `allow_remote: true` 再升级，否则 `check-config` 会拒。
3. 需要收紧管理入口时按 `docs/operations/configuration.md` 的取值表逐项开，不要一次全开。
4. 观察 `tunnelmesh_connections_total{mode="http",result="rejected"}` 与 `queue_full` 流错误率 30 分钟。

## 回滚步骤

`git revert` 对应提交即可，无数据回滚。单项止血不必回滚代码：
`server.stream.max_active_per_agent: 0`、`agent.streams.max_active: 0`、
`server.http.max_connections: 0`、`server.audit.retention_days: 0`、`server.metrics.token: ""`
均恢复旧行为；`server.agents.max_connections_per_agent` 可设为 ≥ Agent 现网最大连接数。
T1/T3 是行为门禁，只能回滚代码来撤销。

## Reviewer 关注点

- `internal/client/serve_signal.go` 与 `internal/cli/serve_watch.go` 的关闭顺序：`Close` 不得被误判为故障，
  watcher 必须在 `Close` 时释放，否则 `run` 收尾挂起。
- `internal/server/http_limits.go` 只包 `r.API.Handler()`，`/ws/`、`/health/*`、SPA fallback 的语义未变；
  请求体 deadline 用 `ResponseController` 设置并在进入前清除，不能污染 hijack 后的连接。
- `internal/relay` 侧容量判定放在 `OpenStream`（跨该 Agent 的所有物理连接计数），不是单连接计数。
- `internal/storage/client_repository.go` 的游标改为 `last_seen_at + id` 复合：确认与
  `idx_client_instance_metadata_owner(owner_user_id, stale, last_seen_at)` 的配合，以及
  `MarkStale`/`MarkExpired` 单独抬 `updated_at` 的事实。
- `recording-rules.yaml` 的连接错误率表达式仍只看 `result=~"failed|error"`，准入丢弃（`rejected`）
  有意不计入错误率，避免自设水位触发告警。

## 遗留（未在本轮处理）

- `internal/agent/dialer.go:88` 的 HTTPS 上游探测复用 `http` policy 命名空间，与
  `docs/protocol/proxy-modules.md` 的按协议维度是否一致待核。
- `.github/workflows/ci.yml` 的 `-p 1` 与 `mysql56` job 注释本轮未再审。

## 集成状态

已提交并推送到 `codex/vpn-phase6-server-data-plane`，尚未合入 `main`。
