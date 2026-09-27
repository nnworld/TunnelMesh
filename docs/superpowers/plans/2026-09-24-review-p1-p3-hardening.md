# 实施计划：Review P1–P3 加固与客户端观测页排序

- **状态**：已确认执行（用户指令「从 P1–P3 依次修复」，并沿用既有「无需再次确认」授权）
- **规格引用**：AGENTS.md §4 限流、§5 消除单点/健康检查、§9 边界校验、§12 配置与代码分离
- **分支**：`codex/vpn-phase6-server-data-plane`，基线 `a918f16`

## 目标

消除本轮 review 的 9 项问题，并让客户端运行观测页按最近心跳倒序。

## 架构决策

- **D1 非回环门禁统一到一处判定**：`forward tcp/udp/http` 与 socks5/http-proxy 共用
  「先 `SplitHostPort`，非回环即要求显式 `allow_remote`」。原始 TCP/UDP/HTTP 隧道没有认证层，
  因此只要求 `allow_remote`，不要求口令——与既有 socks5 校验的形状一致（`internal/config/config.go:1049`）。
- **D2 活跃流上限分两层**：Server 按 agent 计（跨该 agent 的所有连接，节点本地判定），
  Agent 按进程计。二者都以 `OpenResultCodeQueueFull` 拒绝，不新增 wire 枚举，
  因此不需要 capability 协商，老版本安全降级。0 表示不限，沿用 `max_peers` 等既有约定。
- **D3 监听器失败必须上抛**：本地入口的 `Serve()` 错误经 `Err()` 通道暴露给运行循环，
  由 `run` 决定退出；不在 client 库里直接 `os.Exit`。
- **D4 HTTP 超时不改 WS 语义**：只加 `IdleTimeout`（keep-alive 空闲，不影响已 hijack 的 WS）
  与 `/api/` 路由的 body 读 deadline；连接数准入用信号量，0 表示不限（默认保持现网行为）。
- **D5 审计删除必须显式**：`server.audit.retention_days` 默认 0=永久保留；>0 才按批删除。
  默认值不变，避免任何隐式的合规数据销毁。
- **D6 `/metrics` 保护用可选 token**：`server.metrics.token`（仅 env 注入）为空时行为不变；
  同时修正 `prometheus.yml.example` 里「需要认证的管理入口」这一不存在的能力暗示。
- **D7 观测页排序改在 Repository**：`ORDER BY updated_at DESC,id DESC` + 反向 cursor 谓词。
  排序是数据的权威属性，不能在前端做（前端只拿到一页）。心跳刷新写 `updated_at`
  （`internal/storage/client_repository.go:143`），故 `updated_at DESC` 即「最近心跳优先」。

## 全局约束

- 不引入第四个二进制；不改协议版本；不动已发布增量 DDL。
- 新配置键必须同时进 `docs/operations/configuration.md` 键表、默认值表、校验与中英文档。
- 指标标签仍是封闭枚举，不引入 agent 之外的新基数维度。

## 文件清单与任务间接口

| # | 优先级 | 文件 | 产出接口 |
| --- | --- | --- | --- |
| T1 | P1 | `internal/client/forward.go`、`internal/client/http_forward.go`、`internal/client/guard.go`、`internal/config/config.go`、`internal/cli/root.go` | `requireLoopbackListen(network, addr string, allowRemote bool) error` |
| T2 | P1 | `internal/server/agent_relay_transport.go`、`internal/agent/dial_executor.go`、`internal/config/config.go` | `ErrAgentRelayStreamCapacity`、`DialExecutor` 活跃流计数 |
| T3 | P2 | `internal/client/listeners.go`、`forward.go`、`socks5_forward.go`、`http_proxy_forward.go`、`internal/cli/client_run.go` | `Err() <-chan error` |
| T4 | P2 | `internal/server/runtime.go`、`internal/server/middleware.go`、`internal/config/config.go` | `server.http.{idle_timeout,body_timeout,max_connections}` |
| T5 | P2 | `docs/deployment/nginx.md` | WS/兜底 location 的 `limit_req` 覆盖 |
| T6 | P2 | `internal/server/audit_retention_sweeper.go`、`internal/storage/audit_repository.go`、`internal/config/config.go` | `DeleteOlderThan(ctx, cutoff, limit)` |
| T7 | P3 | `internal/server/session_manager.go`、`internal/server/runtime.go`、`internal/server/ws_agent.go`、`internal/config/config.go` | `server.agents.max_connections_per_agent`（替换硬编码 64）并强制 |
| T8 | P3 | `internal/server/runtime.go` | hello 后按心跳周期 re-arm 读超时 |
| T9 | P3 | `internal/server/health.go`、`internal/server/runtime.go`、`deploy/prometheus/*`、`internal/config/config.go` | `server.metrics.token` |
| T10 | 需求 | `internal/storage/client_repository.go`、`web/src/views/Clients.vue` | 倒序 + cursor |

## TDD 步骤（每项同构）

1. 先写失败测试（下表「测试」列），运行确认因新行为缺失而红，并记录实际失败原因。
2. 写最小实现使其绿。
3. 补边界：0/负值、非回环与回环、循环内不泄漏 goroutine、cursor 首尾边界。

| # | 测试 | 预期红灯 |
| --- | --- | --- |
| T1 | `internal/config/config_test.go`、`internal/client/forward_test.go` | 非回环 tcp/udp/http 隧道当前无告警；`NewTCPForward` 当前接受 `0.0.0.0` |
| T2 | `internal/server/agent_relay_transport_test.go`、`internal/agent/dial_executor_test.go` | 超上限当前仍放行 |
| T3 | `internal/client/listeners_test.go` | 当前 `Serve()` 错误被丢弃，`Err()` 不存在 |
| T4 | `internal/server/runtime_test.go` | 当前 `IdleTimeout==0` 且无准入 |
| T6 | `internal/storage/audit_*_test.go` | `DeleteOlderThan` 不存在 |
| T7 | `internal/server/session_manager_test.go` | ACK 当前恒为 64，且超限不拒绝 |
| T8 | `internal/server/runtime_test.go` | 当前读超时被清零后不再设置 |
| T9 | `internal/server/health_test.go` | 当前无 token 也返回指标 |
| T10 | `internal/storage/client_contract_test.go` | 当前按 `updated_at` 升序 |

## 验证命令

```
go build ./... ; go build -tags vpn ./...
go test ./... -count=1 ; go test -tags vpn ./... -count=1
go test -race ./internal/client/ ./internal/agent/ ./internal/server/ ./internal/storage/ -count=1
go vet ./... ; go vet -tags vpn ./... ; gofmt -l internal cmd scripts deploy ; git diff --check
cd web && npm test -- --run && npm run build ; ./scripts/verify-web-embed.sh
python3 scripts/gen_doc_index.py
```

## 回滚注意事项

- 全部为代码 + 文档；无 Schema 变更（`schema_meta.version` 保持 16），因此可逐提交 `git revert`。
- 行为变更均有「默认值」保险：`max_connections_per_agent` 默认 16（等于 Agent 自身上限，
  合规 Agent 不会触顶）；`retention_days` 默认 0；`metrics.token` 默认空；
  `http.idle_timeout` 默认 120s 只影响空闲 keep-alive。任一项引发问题都可单独把默认值调回旧行为而无需回滚代码。
- T2 的活跃流上限是本轮唯一可能改变现网拒绝行为的开关：默认 1024/agent，
  若某部署确有更高并发，调大或置 0，不需要回滚。
