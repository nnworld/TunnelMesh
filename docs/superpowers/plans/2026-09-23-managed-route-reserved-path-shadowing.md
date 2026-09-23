# 托管路由被控制面保留路径遮蔽修复实施计划

状态：已确认并实现（用户修正了设计决策）

确认记录：初版计划在托管路由主机上仍保留 `/ws/agent`、`/ws/client`、`/health/*`、`/metrics` 四个控制面端点。用户明确要求“`tm-*` 开头的域名应该把所有 path 不作为控制面主机，不止 `/api/` 的 path”，据此改为第 5 节的分层方案：`tm-*` 命名空间不保留任何路径，其余 Host 才保留控制面协议与运维端点。

## 1. 现象

托管路由主机上所有 `/api/` 开头的接口返回 TunnelMesh 自己的管理 API 404 信封，而不是上游服务的响应：

```json
{"code":404,"msg":"Not Found","data":{"error":"not found"}}
```

响应体是管理 API 的统一格式，说明请求根本没有进入 Agent 转发链路。

## 2. 根因

`internal/server/web.go:38` 的 `NewWebHandlerWithManagedRoutes` 决策链是**先按路径、后按 Host**：

1. `/api/` 前缀 → 管理 API（`internal/server/web.go:42`，不检查 Host）
2. `/health/` 前缀或 `/metrics` → 健康与指标（`internal/server/web.go:50`）
3. `/ws/agent`、`/ws/client`、`/ws/webssh/` → 对应 WebSocket handler（`internal/server/web.go:58`、`:66`、`:74`）
4. 其余 `/ws/` 前缀 → 直接 `http.NotFound`（`internal/server/web.go:82`）
5. **之后**才是按 Host 匹配的托管路由 `managed.TryServeHTTP`（`internal/server/web.go:86`）
6. 最后是嵌入静态资源与 SPA history fallback

管理 API 对任何不以 `/api/v1/` 开头的路径统一返回 404 `{"error":"not found"}`（`internal/server/api.go:316`）。因此 `https://tm-6000d.claw.qihoo.net/api/xxx` 被第 1 步吃掉，返回上述信封。

同一根因还影响：

- 上游路径以 `/ws/` 开头的 WebSocket。`protocol: websocket` 的托管路由只要端点叫 `/ws/...` 就被第 4 步直接 404。
- 上游自己的 `/health/*` 与 `/metrics`，被第 2 步吃掉。

保留前缀的设计意图是“不让 SPA history fallback 吞掉 API 与 WebSocket”（`internal/server/web.go:10` 注释与 `AGENTS.md` 的 Web 管理后台约束）。这是**控制面自身**的需求，却被实现成了与 Host 无关的全局规则，于是劫持了托管路由主机上的用户流量。

`tp-*` HTTP 代理入口不受影响：`ProxyEntry.ServeHTTP` 按注入的身份头解析路由，不做路径前缀判定（`internal/server/proxy_entry.go:160`）。

## 3. 目标

1. 托管路由主机上的业务路径全部交给该路由，包括 `/api/*` 与 `/ws/*`。
2. 控制面主机（Host 不匹配任何路由）行为完全不变：`/api/` 与 `/ws/` 仍优先于 SPA history fallback。
3. TunnelMesh 自己的协议端点与运维端点在任何 Host 上保持可达，避免路由误配打挂 Agent 集群连通性或 LB 健康检查。

## 4. 非目标

- 不改路由匹配算法、动态域名格式与 `domainMatch` 语义。
- 不改管理 API 的路径、鉴权与响应格式。
- 不引入新配置项。
- 不处理 `copyResponse` 吞 `io.Copy` 错误的可观测性缺口（PR #19 已记为后续项）。

## 5. 设计决策

### 决策 1：`tm-*` 命名空间主机把**所有路径**交给路由

判定谓词是纯字符串匹配，不查路由表、不做 I/O：

```go
const managedNamespaceLabelPrefix = "tm-"

func isManagedNamespaceHost(host string) bool  // 去端口、去尾点、取首标签、判前缀
```

Host 落在该命名空间时，决策链在**任何**保留前缀之前就调用 `managed.TryServeHTTP`，因此 `/api/*`、`/ws/*`、`/health/*`、`/metrics` 全部由上游服务应答，控制面不保留任何路径。

理由：

- `tm-*` 是 TunnelMesh 自己分配的命名空间。`domainMatch` 的通配分支只接受首标签以 `tm-` 开头的 Host（`internal/routing/matcher.go:223`），动态域名要求首标签按 `-` 切分后至少 6 段（`internal/routing/parser.go:62`）。所以这类 Host 天然就是用户流量入口，不可能是管理后台 origin、Agent 拨号 origin 或 LB 健康检查目标。
- 既然不可能是控制面 origin，就不需要为它保留任何控制面端点；保留反而会挡住上游自己的 `/health`、`/metrics`。
- 谓词不做 I/O 是关键：控制面流量（尤其是 `/health/` 探针）因此不会依赖路由表可用性。`TryServeHTTP` 在路由表刷新失败且无缓存快照时会直接写 503（`internal/server/managed_route_handler.go:162`），若探针也要先过路由表，数据库故障就会让 LB 摘掉本来健康的节点。
- 未命中任何路由的 `tm-*` 名字继续下沉到既有控制面链，行为与修复前完全一致，谓词本身不会把 Host 从控制面手里夺走。

### 决策 2：其余 Host 命中路由即拥有该 Host，但保留四个控制面端点

非 `tm-*` 的 Host（显式域名路由，例如 `git.example.com`）同样按 Host 作用域分派：命中路由则该 Host 的 `/api/*`、`/ws/*` 等业务路径归路由。仅 `isControlPlaneReservedPath` 判定为真的路径留在控制面：

| 保留路径 | 理由 |
| --- | --- |
| `/ws/agent`、`/ws/client` | 被劫持的后果是 Agent/Client 无法重连，该 Agent 背后**所有**路由一起失效，爆炸半径远大于单个请求 |
| `/health/` 前缀、`/metrics` | LB 与监控探针目标；必须先于路由解析可用，否则路由表故障会连带摘掉健康节点 |

这四个路径**完全跳过**路由解析（不是“解析失败后回退”），因此控制面 origin 上的健康检查在任何情况下都不会触碰路由表或数据库。

显式域名由运营者自行填写，可能与控制面 origin 或 Agent 拨号 origin 撞名，所以这里保留纵深防御；`tm-*` 命名空间不存在这种可能，故不保留。两侧的差异是有意为之，各自有明确依据。

### 决策 3：不新增 admin host 配置项

现有设计就是“Host 命中路由 → 路由；否则 → 控制面”，代码里没有权威的控制面 origin 配置（`internal/config` 只有 `server.dynamic_suffix`）。新增配置会引入第二个事实来源，并带来默认值、校验与迁移成本；决策 1 的命名空间谓词已经能在不新增配置的前提下无损地区分两类 Host。若将来需要显式声明控制面 origin，另开 MINOR 版本处理。

### 决策 4：`ManagedRouteDispatcher` 只调用一次

`tm-*` 分支未命中时直接下沉到控制面链，不再二次调用 dispatcher，避免重复解析、重复日志与重复指标计数。原先位于 `/ws/` 兜底之后、静态资源之前的那次调用（`internal/server/web.go:86`）因此被移除。

## 6. 文件清单

- `internal/server/web.go`：新增 `managedNamespaceLabelPrefix`、`isManagedNamespaceHost`、`isControlPlaneReservedPath`；决策链改为“`tm-*` 全路径优先 → 其余 Host 保留四端点后按路由分派 → 控制面链”；移除原先靠后的 dispatcher 调用；更新 `ManagedRouteDispatcher` 与文件头注释。
- `internal/server/web_dispatch_test.go`：新增 6 个决策链测试，见第 7 节。
- 文档：`docs/user-guide/managed-http-route.md` 增补“上游路径与控制面保留端点”小节；`docs/operations/troubleshooting.md` 增补“托管路由返回管理 API 404 信封”排障条目；`docs/pull-requests/2026-09-23-managed-route-reserved-path-shadowing.md`；本文件；重新生成 `docs/` 索引。

## 7. TDD 步骤

红灯测试（`internal/server/web_dispatch_test.go`，用 `recordingDispatcher` 桩记录被分派的路径）：

1. `TestManagedNamespaceHostServesEveryPath`：`tm-6000d.claw.qihoo.net` 上的 `/api/skill/claw/cate`、`/api/v1/tokens`、`/ws/chat`、`/ws/agent`、`/ws/client`、`/health/live`、`/metrics`、`/skills` 必须全部由上游应答。
2. `TestExplicitDomainRouteServesUpstreamAPIAndWSPaths`：`git.example.com` 上的 `/api/v1/repos`、`/ws/git`、`/health`、`/readyz` 必须全部由上游应答。
3. `TestControlPlaneEndpointsReservedOnExplicitDomainRoute`：同一显式域名 Host 上 `/ws/agent`、`/ws/client`、`/health/live`、`/metrics` 仍由控制面处理，且 dispatcher 未被调用。
4. `TestControlPlaneHostKeepsReservedPathOrder`：控制面 origin 上 `/api/v1/tokens`→管理 API、`/health/live`与`/metrics`→健康、`/ws/agent`与`/ws/client`→对应 handler、`/ws/webssh/tick`→broker、未知 `/ws/unknown`→404、`/some/spa/route`→SPA fallback。
5. `TestUnmatchedManagedNamespaceHostFallsThrough`：未被任何路由认领的 `tm-unclaimed.claw.qihoo.net` 仍回落到管理 API 与健康端点。
6. `TestControlPlaneHealthSurvivesRouteTableOutage`：dispatcher 置为“路由表不可用”（写 503）时，控制面 origin 的 `/health/live`、`/metrics`、`/ws/agent`、`/ws/client` 仍返回 200，且 dispatcher 调用次数为 0。

实际红灯输出（修复前）：

```
--- FAIL: TestManagedNamespaceHostServesEveryPath
    /api/skill/claw/cate on a managed-route host was served by "api", want the upstream route
--- FAIL: TestExplicitDomainRouteServesUpstreamAPIAndWSPaths
    /api/v1/repos on an explicit-domain route was served by "api", want the upstream route
```

测试 3–6 在修复前即通过，属于防止重排引入回归的护栏（其中测试 6 专门锁定“健康检查不依赖路由表”）。

最小实现：决策 1、2、4，未做超出目标的重构。

绿灯判据：上述 6 个测试通过，且 `internal/server` 既有测试全部通过（含 `webssh_e2e_test.go`、`webssh_runtime_test.go`、`health_test.go`、`runtime_test.go` 对 `NewWebHandlerWithManagedRoutes` 的使用）。

## 8. 验证命令

```bash
go build ./...
go test ./internal/server -count=1
go test ./... -count=1
go test -race ./internal/server ./internal/routing -count=1
go vet ./...
git diff --check
```

本轮不改前端，无需执行 `npm test` 与 `npm run build`；不改嵌入产物，无需 `scripts/verify-web-embed.sh`。

结果：`go build ./...` 通过；`go test ./internal/server -count=1` 全包通过（46s，含 6 个新增决策链测试）；`go test ./... -count=1` 全量通过，无失败包；`go test -race ./internal/server ./internal/routing -count=1` 通过（server 449.7s、routing 1.4s）；`go vet ./...` 无输出；`git diff --check` 无输出。全量套件、race 与 vet 均在提交后的最终工作树上重新执行，避免与实现期间的中间状态混淆。

## 9. 发布与回滚注意事项

- 无 API、Schema、配置与协议变更，纯 handler 决策链重排。**Server 单独发布即可生效**，Agent 与 Client 无需升级。
- 回滚即回退 Server 二进制，无持久化状态需要清理；回滚后 `/api/*` 与 `/ws/*` 上游路径恢复 404。
- 行为变化需知会使用者：
  - `tm-*` 托管域名上的**所有**路径都归上游，包括 `/health/*`、`/metrics`、`/ws/agent`、`/ws/client`。控制面只在自己的 origin 上提供这些端点。
  - 显式域名路由上的 `/ws/agent`、`/ws/client`、`/health/*`、`/metrics` 仍归控制面，上游同名路径需改用其它路径或 `tp-*` 代理入口。
  - 不要把管理后台 origin 或 Agent 拨号 origin 配成显式域名路由，否则该 Host 的控制面（含后台静态资源）会被路由接管。
- 灰度建议：先在单个 Server 节点验证一个含 `/api/` 前缀的 `tm-*` 托管路由、一个 `protocol: websocket` 且端点为 `/ws/*` 的路由，并确认管理后台登录、Agent 重连与 LB 健康检查均正常后全量。
