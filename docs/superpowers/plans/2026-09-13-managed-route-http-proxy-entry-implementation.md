# 托管路由 HTTP 代理入口（tp-*）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让用户把 `https://tp-<name>.<domain>:443` 直接当作标准 HTTP 代理使用，出口 agent、Basic 认证与源 IP ACL 全部由管理后台配置的托管路由决定，无需在用户机器上安装 tunnelmesh-client。

**Architecture:** OpenResty 在既有 443 上新增一个 `tp-*` server 块，用 server 级 `access_by_lua_block` 接管 CONNECT（打过 proxy_connect 补丁的内核会对 CONNECT 跳过 location 匹配，因此不能用 `content_by_lua_block`），把请求原样搬到 tunnelmesh-server 进程内新增的明文内部监听 `127.0.0.1:8089`，并注入 `X-TunnelMesh-Route`（来自 SNI）与 `X-TunnelMesh-Client-IP`（来自 `$remote_addr`）。Server 是唯一策略执行点：身份解析、源 ACL、Basic 认证、目标校验、限额、审计、指标，然后经既有 `relay.NodeTransport.OpenStream` 把流量送到路由指定的 agent 出口，集群模式下自动跨节点。Lua 不含任何策略。

**Tech Stack:** Go 标准库（`net/http`、`crypto/tls` 不涉及、`net/netip` 不用，统一 `net.IP`）、既有 `internal/relay`、`internal/routing`、`internal/storage`、`internal/observability`；OpenResty（ngx_http_lua_module + 已安装的 ngx_http_proxy_connect_module 补丁内核）；Vue 3 + TypeScript + Element Plus；Prometheus/Grafana 既有单 Dashboard。

**Spec:** `docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md`

## Global Constraints

- Schema 保持 `v13`，本计划不新增迁移；`migrations/ddl.sql` 与 `migrations/incremental/` 不得改动。
- 路由存储复用 `tunnels` 表：`protocol = "http-proxy"`、`domain = tp-<name>.<domain_suffix>`、`path_prefix = "/"`、`agent_id` 为出口 agent、`target_host = "*"`、`target_port = 0`（哨兵，导出常量 `storage.ProxyTargetWildcard = "*"`）。
- 凭据复用 `credentials` 表，新增 `credential_type = "proxy_basic"`：`public_key` 存 username，`fingerprint = sha256(username)` 的 hex，加密 secret 存 `{"password":"..."}`。
- 内部入口默认 `server.proxy_entry.listen = 127.0.0.1:8089`、`trusted_proxies = ["127.0.0.1/32","::1/128"]`；peer 不在白名单时在读请求之前直接关闭连接。
- 可信头名称固定：`X-TunnelMesh-Route`、`X-TunnelMesh-Client-IP`、`X-TunnelMesh-Client-Port`；client IP 缺失或非法一律 403，绝不回退到 peer IP。
- 默认值：`connect_timeout = 10s`、`idle_timeout = 300s`、`shutdown_timeout = 30s`、`max_concurrent_tunnels = 512`、`max_header_bytes = 16384`、`auth_backoff_threshold = 5`；退避起始 30s，每次翻倍，上限 15m。
- 稳定错误码（响应体 `{"code":<http>,"msg":"<错误码>","data":null}`）：`proxy_route_identity_invalid`、`proxy_route_unavailable`、`proxy_source_denied`、`proxy_auth_required`、`proxy_auth_failed`、`proxy_auth_backoff`、`proxy_target_denied`、`proxy_target_invalid`、`proxy_egress_unavailable`、`proxy_egress_timeout`、`proxy_capacity_exhausted`、`credential_secret_unavailable`。
- 未知路由、停用路由与 ACL 拒绝对外表现一致（403 + 同一文案），只有日志与指标区分 reason。
- 认证失败响应 407 必须带 `Proxy-Authenticate: Basic realm="TunnelMesh", charset="UTF-8"`；容量超限响应 503 必须带 `Retry-After: 5`。
- 日志、审计、指标不得包含密码、完整 `Proxy-Authorization` 头或响应体。
- CONNECT 使用 `relay.StreamRequest.Protocol = "tcp"`（与 `internal/client/socks5_forward.go:250` 的裸隧道语义一致）；绝对形式使用 `Protocol = "http"`，转发方式与既有 `internal/server/http_proxy.go` 的 `ServeRoute` 完全一致：`upstreamReq.Write(stream)` + `http.ReadResponse(bufio.NewReader(stream), upstreamReq)` + `copyResponse`（已核实 `ServeRoute` 不使用 `http.Client`；WebSocket 升级走 `handleUpgrade` 的 `Hijack()` + `io.Copy`；`streamNetConn` 是预留适配器，当前在 `internal/` 下没有任何构造点，两条分支都不经过它，本计划不引用它）。
- OpenResty 的 `tp-*` server 块禁止 `http2 on`，只协商 http/1.1；Lua 侧所有 cosocket 超时显式设置。
- 分层保持 Handler -> Service -> Repository；管理 API 前缀 `/api/v1`，响应 `{code,msg,data}`，列表用 cursor 分页。
- 每个任务先红后绿（TDD），完成后运行本任务列出的验证命令。
- 未经用户在当前任务中明确授权，不得执行 `git commit` / `git push`；步骤中的 commit 只在已获授权时执行。
- 本计划基线为 `main` @ `b3acd34`（此前另一会话的大规模暂存改动已在 `6202b5b` / `b3acd34` 落盘，当前工作树除本计划与 spec 外干净）。开始 Task 0 前先用 `git log --oneline -1` 与 `git status --short` 复核基线；若 HEAD 已前进，必须重新核对本计划里所有以 `internal/server/api.go:NNNN` 形式给出的行号锚点（行号会随其它提交漂移，函数名与代码片段才是稳定锚点）。
- 无论工作树是否干净，执行本计划时只 `git add` 本任务 `**Files:**` 列出的文件，禁止 `git add -A`、`git commit -a`、`git stash`、`git reset`：同一仓库可能随时出现其它会话的改动，全量暂存会把无关变更混进本 PR。

## File Structure

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/proxyentry/route.go` | Create | `Route` 快照结构、`RouteSource` 接口、认证模式常量 |
| `internal/proxyentry/identity.go` | Create | `RouteKey`、`RouteIdentity` 接口、`HeaderRouteIdentity`、`SNIRouteIdentity`（预留） |
| `internal/proxyentry/acl.go` | Create | 源 IP ACL 解析与匹配、`ParseClientIP` |
| `internal/proxyentry/auth.go` | Create | Basic 认证、`SecretResolver` 接口、失败退避 |
| `internal/proxyentry/target.go` | Create | 目标 host:port 校验（危险地址恒拒、路由 allowlist、私网开关） |
| `internal/proxyentry/errors.go` | Create | 稳定错误码与 `Error` 类型（HTTP 状态 + code 字符串） |
| `internal/server/proxy_entry.go` | Create | `ProxyEntry` handler：CONNECT 与绝对形式转发、限额、指标、审计 |
| `internal/server/proxy_entry_listener.go` | Create | 内部监听生命周期、trusted peer 前置校验、优雅退出 |
| `internal/server/proxy_entry_routes.go` | Create | 从 `ManagedRouteTable` 快照构建 `proxyentry.RouteSource` |
| `internal/server/runtime.go` | Modify | `ServerRuntime` 增加 `proxyEntry` 字段；仅在 `server.proxy_entry.enabled` 时构造，按 relay listener 同一模式启动并汇入 `errCh`，shutdown 时一并取消（Task 9） |
| `internal/server/managed_route_handler.go` | Modify | `loadManagedRoutes` 跳过 `http-proxy` 行；同一次查询构建 proxy 索引 |
| `internal/storage/models.go` | Modify | `CredentialTypeProxyBasic` 常量、`ProxyTargetWildcard` 常量 |
| `internal/storage/credential_repository.go` | Modify | 类型 switch 增加 `proxy_basic` 分支 |
| `internal/server/credential_service.go` | Modify | `validateCredentialShape` 支持 `proxy_basic`；username/password 落位 |
| `internal/server/credential_api.go` | Modify | `credentialRequest.Username` / `credentialResponse.Username` 透传（响应永不回显 secret）；`credentialErrorStatus(err)` 从 `writeCredentialError` 抽出复用（Task 8 / Task 12） |
| `internal/server/api.go` | Modify | `/api/v1/routes` 支持 `protocol=http-proxy`、config 字段、`proxyURL` |
| `internal/config/config.go` | Modify | `ProxyEntryConfig` 结构、默认值、允许键、`validateProxyEntry` |
| `internal/cli/root.go` | Modify | `rootOptions` 字段、flag 绑定、`values[...]` 回填 |
| `internal/observability/metrics.go` | Modify | 5 个 proxy entry 指标 + 访问器 + 注册 |
| `web/src/api/client.ts` | Modify | `ManagedRoute`/`ManagedRouteCreateInput` 新字段、`proxyURL` |
| `web/src/views/Routes.vue` | Modify | 类型选择、ACL 编辑、凭据选择、使用说明抽屉、列表新列 |
| `web/src/views/Credentials.vue` | Modify | `proxy_basic` 类型选项与 username 字段 |
| `web/src/i18n/messages/zh-CN.ts` / `en-US.ts` | Modify | `routes.*`、`credentials.*` 新键（`schema.ts` 由 zh-CN 推导，无需改） |
| `deploy/openresty/tunnelmesh_proxy_entry.lua` | Create | CONNECT 搬运与 splice（无策略） |
| `deploy/openresty/tunnelmesh-proxy.conf.example` | Create | tp-* server 块模板（7 个占位符，渲染步骤见 Task 14/15） |
| `deploy/openresty/spike-connect-check.sh` | Create | 诊断脚本：验证目标 OpenResty 的补丁内核能否把 CONNECT 交给 server 级 access_by_lua；部署前置检查复用 |
| `deploy/openresty/Dockerfile.proxy-connect` | Create | 带 proxy_connect 补丁的 OpenResty 镜像：E2E 冒烟用，也可供没有该内核的主机部署 |
| `deploy/openresty/README.md` | Create | 四个产物的用途、占位符与渲染标记、构建与校验命令 |
| `deploy/openresty/openresty_artifacts_test.go` | Create | 产物一致性测试：Lua/conf 与 `server.proxy_entry` 默认值的契约、Dockerfile 版本与构建顺序 |
| `test/e2e/proxy-entry/run.mjs` | Create | 冒烟场景（8 项断言）：需 `TM_PROXY_E2E_NGINX=1` 与 docker，否则打印原因 skip |
| `test/e2e/proxy-entry/lib/harness.mjs` | Create | 渲染仓库产物、生成自签证书、拉起 OpenResty 容器、等待监听 |
| `test/e2e/proxy-entry/lib/stub.mjs` | Create | 内部入口替身：按 CONNECT 目标编排 200/403/407/503 与回声 |
| `test/e2e/proxy-entry/README.md` | Create | 断言表、前置条件、环境变量、skip 语义与排障入口 |
| `docs/deployment/openresty-proxy-entry.md` | Create | 部署、验证、容量、reload 影响、回滚 |
| `docs/user-guide/http-proxy-entry.md` | Create | 管理员与用户双侧使用说明 |
| `docs/pull-requests/2026-09-13-managed-route-http-proxy-entry.md` | Create | PR 描述记录（章节对齐既有 PR 文档） |
| `internal/storage/repository.go` | 不改（已核实） | `tunnelRepo.Create` 只补 ID/时间戳后 INSERT，不校验 `target_host`/`target_port`；DDL 对 tunnels 无 CHECK 约束，哨兵 `"*"`/`0` 直接可写，因此存储层无需特例 |
| `web/src/api/credentials.ts` | Modify | `CredentialType` 增加 `proxy_basic`、`CredentialInput.username`、`Credential.username` |
| `web/src/tests/routes.spec.ts` / `credentials-view.spec.ts` / `i18n.spec.ts` | Modify | 表单校验、类型切换、列表列、i18n 键集合对齐回归 |
| `web/src/tests/layout-overflow.spec.ts` | 不改（自动守卫） | 该用例自动扫描 `src/views/*.vue`，Task 13 的新容器必须自行满足 `grid-template-columns` 与不引入整页横向滚动条，无需修改测试本身 |
| `internal/server/web_dist` | Modify | Task 13 Step 5 `npm run build` + `scripts/verify-web-embed.sh` 同步的 Go embed 产物 |
| `deploy/grafana/dashboards/tunnelmesh.json` | Modify | 追加第 6 个 Row `HTTP Proxy Entry` 与 7 个面板 |
| `deploy/grafana/dashboard_schema_test.go` | Modify | Row 数量 5 -> 6、新 Row 标题、5 个必需指标 |
| `deploy/prometheus/alert-rules.yaml` | Modify | 代理入口认证失败与拒绝率两条告警 |
| `deploy/README.md` | Modify | `openresty/` 产物行、模板约定、Row 数量、校验命令 |
| `docs/operations/observability.md` | Modify | Row 结构、`route`/`reason` 标签基数、告警责任 |
| `docs/operations/completeness-checklist.md` | Modify | 已完成项追加 tp-* 代理入口 |
| `docs/deployment/nginx.md`、`docs/operations/configuration.md`、`docs/operations/troubleshooting.md`、`docs/api/openapi.yaml`、`docs/README.md` | Modify | 同步文档 |
| `docs/user-guide/managed-http-route.md` | Modify | 交叉引用：`http-proxy` 不是反代路由 |
| `docs/development/testing.md`、`docs/development/README.md` | Modify | 验证层级表与索引补 proxy-entry 端到端冒烟 |
| `docs/architecture/overview.md` | Modify | tp-* 入口拓扑链路（文字版）与“不新增公网端口、唯一策略执行点是 Server”的说明，spec 第 16 节要求 |
| `docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md` | Modify | 实现期偏差回写：Task 13 Step 6（活跃隧道数改为指向 Grafana 面板）、Task 14 Step 15（容器与 stub 修正）、Task 15 Step 15(b)（第 13 / 14 / 15 节） |
| `docs/superpowers/plans/2026-09-13-managed-route-http-proxy-entry-implementation.md` | Modify | 本计划自身：Task 0 Step 5 在文件末尾追加 `## Task 0 验证记录`（spike 命令、实际输出与“继续 A3 / 回退 A2”结论） |
| `README.md`、`README.zh-CN.md` | Modify | 能力清单、Edge 部署链接、用户指南索引 |
| Go 测试文件（`internal/proxyentry/{route,identity,acl,auth,target,errors}_test.go`、`internal/server/{proxy_entry,proxy_entry_absolute,proxy_entry_listener,proxy_entry_routes,api_proxy_route,credential_api,credential_service}_test.go`、`internal/storage/credential_repository_test.go`、`internal/config/config_test.go`、`internal/observability/metrics_proxy_entry_test.go`） | Create/Modify | TDD 红灯测试；精确清单见各任务的 `**Files:**` 列表，此处只为文件总览完整性列出 |
| `docs/pull-requests/README.md`、`docs/superpowers/plans/README.md`、`docs/superpowers/specs/README.md`、`docs/architecture/adr/README.md` | Regenerate | `python3 scripts/gen_doc_index.py` 生成的索引，Task 15 Step 12 统一重生成并验证幂等，不手工编辑 |

---

### Task 0: OpenResty CONNECT 可行性 spike（阻塞后续所有任务）

**Files:**

- Create: `deploy/openresty/spike-connect-check.sh`（诊断脚本：只读 `nginx -V` 并在 `mktemp -d` 目录里起一个最小 server，不改任何生产配置；Task 15 的部署文档把它列为前置检查命令）
- Modify: `docs/superpowers/plans/2026-09-13-managed-route-http-proxy-entry-implementation.md`（在文件末尾追加 `## Task 0 验证记录` 小节）

**Interfaces:**

- Consumes: 目标机上已安装的 OpenResty（含 ngx_http_proxy_connect_module 补丁内核）
- Produces: 一份五问五答的验证记录，以及“继续 A3 / 回退 A2”的决策

- [ ] **Step 1: 写诊断脚本**

```bash
#!/usr/bin/env bash
# Diagnostic for the tp-* HTTP proxy entry.
#
# 写于 Task 0 的可行性 spike，之后保留为部署前置检查：它回答“这台机器的
# OpenResty 到底能不能把 CONNECT 交给 server 级 access_by_lua”。
# 只读取 nginx 构建信息并在临时目录起一个最小 server，不修改任何生产配置。
set -euo pipefail
PORT="${1:-18443}"
WORKDIR="$(mktemp -d)"
echo "== (a) build flags =="
nginx -V 2>&1 | tr ' ' '\n' | grep -i "proxy_connect\|add-module\|with-http_ssl" || true
cat > "$WORKDIR/lua.lua" <<'LUA'
if ngx.req.get_method() ~= "CONNECT" then return end
ngx.log(ngx.WARN, "spike connect_host=", tostring(ngx.var.connect_host),
        " connect_port=", tostring(ngx.var.connect_port),
        " sni=", tostring(ngx.var.ssl_server_name))
local sock = ngx.req.socket(true)
if not sock then ngx.log(ngx.ERR, "spike: no raw socket") return ngx.exit(500) end
sock:send("HTTP/1.1 200 Connection Established\r\n\r\n")
sock:send("spike-ok\r\n")
return ngx.exit(444)
LUA
cat > "$WORKDIR/nginx.conf" <<CONF
events {}
http {
  error_log $WORKDIR/error.log warn;
  server {
    listen 127.0.0.1:$PORT;
    lua_check_client_abort on;
    access_by_lua_file $WORKDIR/lua.lua;
    location / { return 403 "non-connect"; }
  }
}
CONF
echo "== (b)(c)(d) plain-text CONNECT through access_by_lua =="
nginx -p "$WORKDIR" -c "$WORKDIR/nginx.conf"
sleep 1
printf 'CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n' \
  | timeout 5 nc 127.0.0.1 "$PORT" || true
sleep 1
echo "== error.log =="
cat "$WORKDIR/error.log"
nginx -p "$WORKDIR" -c "$WORKDIR/nginx.conf" -s quit || true
echo "workdir kept for inspection: $WORKDIR"
```

- [ ] **Step 2: 在目标 OpenResty 主机上运行**

Run: `bash deploy/openresty/spike-connect-check.sh 18443`
Expected: 输出包含 `spike connect_host=example.com connect_port=443`，且 nc 侧收到 `HTTP/1.1 200 Connection Established` 与 `spike-ok`。

- [ ] **Step 3: 验证 TLS + SNI 与 h2 行为**

在目标主机用现有通配证书复制一个 `listen 18443 ssl;`（不写 `http2`）的临时 server 块，`server_name ~^tp-[a-z0-9-]+\.<suffix>$;`，然后运行：

```bash
curl -sv --proxy-insecure -x https://tp-spike.<suffix>:18443 https://example.com 2>&1 | head -30
```

Expected: error.log 中 `sni=tp-spike.<suffix>`；curl 报告代理返回 200 后尝试与 example.com 建 TLS（会因为拿到 `spike-ok` 而失败，这是预期的）。同时确认 curl 协商的是 `HTTP/1.1` 而不是 h2。

- [ ] **Step 4: 验证 (e) 非 CONNECT 分支的 hop-by-hop 头**

Run: `curl -sv -x http://tp-spike.<suffix>:18443 http://example.com/ -H 'Proxy-Authorization: Basic dTpw' 2>&1 | head -20`，并在 `location /` 中临时 `proxy_pass` 到 `nc -l 18080`，观察 nc 收到的原始请求头。
Expected: 若 nc 侧看不到 `Proxy-Authorization`，则确认必须显式 `proxy_set_header Proxy-Authorization $http_proxy_authorization;`（本计划已按“必须”编写）。

- [ ] **Step 5: 记录结论并做决策**

在本计划文件末尾追加 `## Task 0 验证记录`，逐条粘贴 (a)-(e) 的命令与实际输出，并写明结论行：`决策：继续 A3` 或 `决策：回退 A2（原因：…）`。
中止判据：若 Step 2 的 `access_by_lua` 收不到 CONNECT，或 Step 3 拿不到 raw socket / SNI，则停止本计划，按 spec 第 15 节回退 A2，并把决策写回 spec 第 17 节。

- [ ] **Step 6: Commit（需授权）**

```bash
git add deploy/openresty/spike-connect-check.sh docs/superpowers/plans/2026-09-13-managed-route-http-proxy-entry-implementation.md
git commit -m "test(deploy): add openresty connect diagnostic and spike record"
```

脚本必须提交而不是留在工作区：仓库当前有另一个会话的大规模暂存改动，未跟踪文件很容易被误扫进别人的 commit；而且它是部署前置检查的唯一可执行证据，运维需要能直接跑。这与 spec 第 15 节“spike 产物不进入生产代码”不冲突——它是只读诊断工具，不参与请求处理，Task 15 Step 15 会把这一条偏差写回 spec。

---

### Task 1: 配置项 `server.proxy_entry`

**Files:**

- Modify: `internal/config/config.go`（`ServerConfig` 结构、默认值表、允许键表、`Validate` 调用、新增 `validateProxyEntry`）
- Modify: `internal/config/config_test.go`
- Modify: `internal/cli/root.go`（`rootOptions` 字段、flag 绑定、`values[...]` 回填）
- Modify: `docs/operations/configuration.md`

**Interfaces:**

- Consumes: 既有 `config.Load` / `config.Validate` / `validateWebSSH` 的写法
- Produces:

```go
type ProxyEntryConfig struct {
    Enabled               bool          `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
    Listen                string        `mapstructure:"listen" json:"listen" yaml:"listen"`
    TrustedProxies        []string      `mapstructure:"trusted_proxies" json:"trusted_proxies" yaml:"trusted_proxies"`
    DomainSuffix          string        `mapstructure:"domain_suffix" json:"domain_suffix" yaml:"domain_suffix"`
    RouteHeader           string        `mapstructure:"route_header" json:"route_header" yaml:"route_header"`
    ClientIPHeader        string        `mapstructure:"client_ip_header" json:"client_ip_header" yaml:"client_ip_header"`
    ClientPortHeader      string        `mapstructure:"client_port_header" json:"client_port_header" yaml:"client_port_header"`
    ConnectTimeout        time.Duration `mapstructure:"connect_timeout" json:"connect_timeout" yaml:"connect_timeout"`
    IdleTimeout           time.Duration `mapstructure:"idle_timeout" json:"idle_timeout" yaml:"idle_timeout"`
    ShutdownTimeout       time.Duration `mapstructure:"shutdown_timeout" json:"shutdown_timeout" yaml:"shutdown_timeout"`
    MaxConcurrentTunnels  int           `mapstructure:"max_concurrent_tunnels" json:"max_concurrent_tunnels" yaml:"max_concurrent_tunnels"`
    MaxHeaderBytes        int           `mapstructure:"max_header_bytes" json:"max_header_bytes" yaml:"max_header_bytes"`
    AuthBackoffThreshold  int           `mapstructure:"auth_backoff_threshold" json:"auth_backoff_threshold" yaml:"auth_backoff_threshold"`
}
```

`ServerConfig` 增加字段 `ProxyEntry ProxyEntryConfig \`mapstructure:"proxy_entry" json:"proxy_entry" yaml:"proxy_entry"\``。

- [ ] **Step 1: 写失败测试**

在 `internal/config/config_test.go` 追加：

```go
func TestProxyEntryDefaultsAndFlagOverride(t *testing.T) {
    cfg, err := config.Load(context.Background(), config.ConfigOptions{})
    if err != nil {
        t.Fatalf("load defaults: %v", err)
    }
    if cfg.Server.ProxyEntry.Enabled {
        t.Fatal("proxy entry must default to disabled")
    }
    if cfg.Server.ProxyEntry.Listen != "127.0.0.1:8089" {
        t.Fatalf("listen default = %q", cfg.Server.ProxyEntry.Listen)
    }
    if got := cfg.Server.ProxyEntry.TrustedProxies; len(got) != 2 || got[0] != "127.0.0.1/32" || got[1] != "::1/128" {
        t.Fatalf("trusted proxies default = %v", got)
    }
    if cfg.Server.ProxyEntry.RouteHeader != "X-TunnelMesh-Route" ||
        cfg.Server.ProxyEntry.ClientIPHeader != "X-TunnelMesh-Client-IP" ||
        cfg.Server.ProxyEntry.ClientPortHeader != "X-TunnelMesh-Client-Port" {
        t.Fatal("trusted header defaults changed")
    }
    if cfg.Server.ProxyEntry.ConnectTimeout != 10*time.Second ||
        cfg.Server.ProxyEntry.IdleTimeout != 300*time.Second ||
        cfg.Server.ProxyEntry.ShutdownTimeout != 30*time.Second {
        t.Fatal("timeout defaults changed")
    }
    if cfg.Server.ProxyEntry.MaxConcurrentTunnels != 512 ||
        cfg.Server.ProxyEntry.MaxHeaderBytes != 16384 ||
        cfg.Server.ProxyEntry.AuthBackoffThreshold != 5 {
        t.Fatal("limit defaults changed")
    }
}

func TestValidateProxyEntryRejectsUnsafeCombinations(t *testing.T) {
    base := func() config.Config {
        cfg, err := config.Load(context.Background(), config.ConfigOptions{})
        if err != nil {
            t.Fatalf("load: %v", err)
        }
        cfg.Server.ProxyEntry.Enabled = true
        cfg.Server.ProxyEntry.DomainSuffix = "tm.example.com"
        return cfg
    }
    ok := base()
    if err := config.Validate(ok); err != nil {
        t.Fatalf("valid config rejected: %v", err)
    }
    missingSuffix := base()
    missingSuffix.Server.ProxyEntry.DomainSuffix = ""
    if err := config.Validate(missingSuffix); err == nil {
        t.Fatal("enabled proxy entry requires domain_suffix")
    }
    badCIDR := base()
    badCIDR.Server.ProxyEntry.TrustedProxies = []string{"10.0.0.0/33"}
    if err := config.Validate(badCIDR); err == nil {
        t.Fatal("invalid trusted proxy CIDR must fail")
    }
    wildcardTrust := base()
    wildcardTrust.Server.ProxyEntry.Listen = "0.0.0.0:8089"
    wildcardTrust.Server.ProxyEntry.TrustedProxies = []string{"0.0.0.0/0"}
    if err := config.Validate(wildcardTrust); err == nil {
        t.Fatal("non-loopback listen must not trust 0.0.0.0/0")
    }
}
```

- 两个用例都用 `context.Background()` 而不是 `t.Context()`：`go.mod` 声明 `go 1.23`，而 `t.Context()` 是 Go 1.24 才加入的 API，`go vet` 的 stdversion 分析器会把它判成门禁失败；`internal/config/config_test.go` 现有用例也一律用 `context.Background()`。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/config/ -run 'TestProxyEntry|TestValidateProxyEntry' -count=1`
Expected: 编译失败，`cfg.Server.ProxyEntry undefined`。

- [ ] **Step 3: 最小实现**

在 `internal/config/config.go` 中：新增 `ProxyEntryConfig` 结构（见 Interfaces）、在 `ServerConfig` 中加 `ProxyEntry` 字段、在默认值表中加入：

```go
"server.proxy_entry.enabled":                false,
"server.proxy_entry.listen":                 "127.0.0.1:8089",
"server.proxy_entry.trusted_proxies":        []string{"127.0.0.1/32", "::1/128"},
"server.proxy_entry.domain_suffix":          "",
"server.proxy_entry.route_header":           "X-TunnelMesh-Route",
"server.proxy_entry.client_ip_header":       "X-TunnelMesh-Client-IP",
"server.proxy_entry.client_port_header":     "X-TunnelMesh-Client-Port",
"server.proxy_entry.connect_timeout":        10 * time.Second,
"server.proxy_entry.idle_timeout":           300 * time.Second,
"server.proxy_entry.shutdown_timeout":       30 * time.Second,
"server.proxy_entry.max_concurrent_tunnels": 512,
"server.proxy_entry.max_header_bytes":       16384,
"server.proxy_entry.auth_backoff_threshold": 5,
```

在允许键表中加入同样 13 个键，并按 `validateWebSSH` 的写法新增：

```go
func validateProxyEntry(cfg ProxyEntryConfig) []string {
    var problems []string
    if !cfg.Enabled {
        return nil
    }
    host, port, err := net.SplitHostPort(strings.TrimSpace(cfg.Listen))
    if err != nil || port == "" {
        problems = append(problems, "proxy entry listen must be a host:port value")
    }
    if strings.TrimSpace(cfg.DomainSuffix) == "" {
        problems = append(problems, "proxy entry requires domain_suffix when enabled")
    } else if !isDomainSuffix(cfg.DomainSuffix) {
        problems = append(problems, "proxy entry domain_suffix must be a valid domain")
    }
    if len(cfg.TrustedProxies) == 0 {
        problems = append(problems, "proxy entry requires at least one trusted proxy CIDR")
    }
    for _, cidr := range cfg.TrustedProxies {
        if _, _, err := net.ParseCIDR(strings.TrimSpace(cidr)); err != nil {
            problems = append(problems, "proxy entry trusted proxy "+cidr+" is not a valid CIDR")
        }
    }
    // A non-loopback listener is reachable from the network, so a wildcard
    // trust list would let any host forge the route and client-IP headers.
    if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "" {
        for _, cidr := range cfg.TrustedProxies {
            trimmed := strings.TrimSpace(cidr)
            if trimmed == "0.0.0.0/0" || trimmed == "::/0" {
                problems = append(problems, "proxy entry trusted_proxies must not be 0.0.0.0/0 or ::/0 when listen is not loopback")
            }
        }
    }
    if cfg.ConnectTimeout <= 0 || cfg.IdleTimeout <= 0 || cfg.ShutdownTimeout < 0 {
        problems = append(problems, "proxy entry timeouts must be positive (shutdown_timeout may be zero)")
    }
    if cfg.MaxConcurrentTunnels < 0 || cfg.MaxHeaderBytes <= 0 || cfg.AuthBackoffThreshold <= 0 {
        problems = append(problems, "proxy entry limits must be positive (max_concurrent_tunnels may be zero)")
    }
    return problems
}
```

`isDomainSuffix` 若仓库已有等价实现则复用，否则在本文件中按 `net` + 字符白名单实现（不允许通配符与前导点）。在 `Validate` 中按既有顺序追加 `problems = append(problems, validateProxyEntry(cfg.Server.ProxyEntry)...)`。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/config/ -count=1 && go vet ./internal/config/`
Expected: PASS。

- [ ] **Step 5: 绑定命令行参数**

在 `internal/cli/root.go` 的 `rootOptions` 中加字段（`proxyEntryEnabled bool`、`proxyEntryListen string`、`proxyEntryDomainSuffix string`、`proxyEntryConnectTimeout time.Duration`、`proxyEntryIdleTimeout time.Duration`、`proxyEntryMaxConcurrentTunnels int`），按既有 webssh 的三处写法各加一段：

```go
flags.BoolVar(&opts.proxyEntryEnabled, "server.proxy_entry.enabled", false, "enable the tp-* managed HTTP proxy entry")
flags.StringVar(&opts.proxyEntryListen, "server.proxy_entry.listen", "", "internal plaintext listener for the proxy entry")
flags.StringVar(&opts.proxyEntryDomainSuffix, "server.proxy_entry.domain_suffix", "", "domain suffix for tp-* proxy routes")
flags.DurationVar(&opts.proxyEntryConnectTimeout, "server.proxy_entry.connect_timeout", 0, "proxy entry stream open timeout")
flags.DurationVar(&opts.proxyEntryIdleTimeout, "server.proxy_entry.idle_timeout", 0, "proxy entry tunnel idle timeout")
flags.IntVar(&opts.proxyEntryMaxConcurrentTunnels, "server.proxy_entry.max_concurrent_tunnels", 0, "maximum concurrent proxy tunnels, 0 means unlimited")
```

```go
if flags.Changed("server.proxy_entry.enabled") {
    values["server.proxy_entry.enabled"] = opts.proxyEntryEnabled
}
if flags.Changed("server.proxy_entry.listen") {
    values["server.proxy_entry.listen"] = opts.proxyEntryListen
}
if flags.Changed("server.proxy_entry.domain_suffix") {
    values["server.proxy_entry.domain_suffix"] = opts.proxyEntryDomainSuffix
}
if flags.Changed("server.proxy_entry.connect_timeout") {
    values["server.proxy_entry.connect_timeout"] = opts.proxyEntryConnectTimeout
}
if flags.Changed("server.proxy_entry.idle_timeout") {
    values["server.proxy_entry.idle_timeout"] = opts.proxyEntryIdleTimeout
}
if flags.Changed("server.proxy_entry.max_concurrent_tunnels") {
    values["server.proxy_entry.max_concurrent_tunnels"] = opts.proxyEntryMaxConcurrentTunnels
}
```

在 `docs/operations/configuration.md` 的 server 小节追加 13 个键的说明表（键名、默认值、含义、是否必填）。

- [ ] **Step 6: 运行验证**

Run: `go test ./internal/config/ ./internal/cli/ -count=1 && go vet ./internal/config/ ./internal/cli/ && gofmt -l internal/config internal/cli`
Expected: 全部通过，gofmt 无输出。

- [ ] **Step 7: Commit（需授权）**

```bash
git add internal/config/config.go internal/config/config_test.go internal/cli/root.go docs/operations/configuration.md
git commit -m "feat(config): add server proxy entry settings"
```

---

### Task 2: proxyentry 错误模型与路由快照类型

**Files:**

- Create: `internal/proxyentry/errors.go`
- Create: `internal/proxyentry/errors_test.go`
- Create: `internal/proxyentry/route.go`
- Create: `internal/proxyentry/route_test.go`

**Interfaces:**

- Consumes: 无（新包起点）
- Produces:

```go
package proxyentry

const (
    AuthModeNone  = "none"
    AuthModeBasic = "basic"

    StatusActive   = "active"
    StatusDisabled = "disabled"
)

// Error pairs an HTTP status with the stable error code returned to clients.
type Error struct {
    Status  int
    Code    string
    message string
}

func (e *Error) Error() string
func (e *Error) HTTPHeaders() map[string]string   // 例如 407 的 Proxy-Authenticate

func NewError(status int, code, message string, headers ...map[string]string) *Error

```go
var (
    ErrRouteIdentityInvalid = NewError(http.StatusForbidden, "proxy_route_identity_invalid", "proxy route identity is invalid")
    ErrRouteUnavailable     = NewError(http.StatusForbidden, "proxy_route_unavailable", "proxy route is unavailable")
    ErrSourceDenied         = NewError(http.StatusForbidden, "proxy_source_denied", "source address is not allowed by this proxy route")
    ErrAuthRequired         = NewError(http.StatusProxyAuthRequired, "proxy_auth_required", "proxy authentication required", proxyAuthenticateHeader())
    ErrAuthFailed           = NewError(http.StatusProxyAuthRequired, "proxy_auth_failed", "proxy authentication failed", proxyAuthenticateHeader())
    ErrAuthBackoff          = NewError(http.StatusProxyAuthRequired, "proxy_auth_backoff", "too many failed proxy authentication attempts", proxyAuthenticateHeader())
    ErrTargetDenied         = NewError(http.StatusForbidden, "proxy_target_denied", "target address is denied by policy")
    ErrTargetInvalid        = NewError(http.StatusBadRequest, "proxy_target_invalid", "target host or port is invalid")
    ErrEgressUnavailable    = NewError(http.StatusBadGateway, "proxy_egress_unavailable", "egress agent is unavailable")
    ErrEgressTimeout        = NewError(http.StatusGatewayTimeout, "proxy_egress_timeout", "egress agent did not answer in time")
    ErrCapacityExhausted    = NewError(http.StatusServiceUnavailable, "proxy_capacity_exhausted", "proxy tunnel capacity exhausted", map[string]string{"Retry-After": "5"})
    ErrSecretUnavailable    = NewError(http.StatusServiceUnavailable, "credential_secret_unavailable", "credential secret storage is unavailable")
)

// proxyAuthenticateHeader keeps the 407 challenge identical on every failure path.
func proxyAuthenticateHeader() map[string]string {
    return map[string]string{"Proxy-Authenticate": `Basic realm="TunnelMesh", charset="UTF-8"`}
}
```

// Route is the policy-relevant snapshot of one tp-* managed route.
type Route struct {
    ID                   string
    Domain               string // lowercase full host, e.g. tp-demo.tm.example.com
    Name                 string // demo
    AgentID              string
    AuthMode             string
    CredentialID         string
    SourceCIDRs          []string
    TargetCIDRs          []string
    TargetPorts          []int
    AllowPrivateTargets  bool
    MaxConcurrentTunnels int
    Status               string
}

func (r Route) Active() bool
func (r Route) RequiresAuth() bool

// RouteSource is implemented by the server-side managed-route snapshot.
type RouteSource interface {
    ProxyRoute(ctx context.Context, domain string) (Route, bool)
}
```

- [ ] **Step 1: 写失败测试**

```go
package proxyentry_test

import (
    "errors"
    "net/http"
    "testing"

    "github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
)

func TestErrorsCarryStableCodesAndStatus(t *testing.T) {
    cases := []struct {
        err    error
        status int
        code   string
    }{
        {proxyentry.ErrRouteIdentityInvalid, http.StatusForbidden, "proxy_route_identity_invalid"},
        {proxyentry.ErrRouteUnavailable, http.StatusForbidden, "proxy_route_unavailable"},
        {proxyentry.ErrSourceDenied, http.StatusForbidden, "proxy_source_denied"},
        {proxyentry.ErrAuthRequired, http.StatusProxyAuthRequired, "proxy_auth_required"},
        {proxyentry.ErrAuthFailed, http.StatusProxyAuthRequired, "proxy_auth_failed"},
        {proxyentry.ErrAuthBackoff, http.StatusProxyAuthRequired, "proxy_auth_backoff"},
        {proxyentry.ErrTargetDenied, http.StatusForbidden, "proxy_target_denied"},
        {proxyentry.ErrTargetInvalid, http.StatusBadRequest, "proxy_target_invalid"},
        {proxyentry.ErrEgressUnavailable, http.StatusBadGateway, "proxy_egress_unavailable"},
        {proxyentry.ErrEgressTimeout, http.StatusGatewayTimeout, "proxy_egress_timeout"},
        {proxyentry.ErrCapacityExhausted, http.StatusServiceUnavailable, "proxy_capacity_exhausted"},
        {proxyentry.ErrSecretUnavailable, http.StatusServiceUnavailable, "credential_secret_unavailable"},
    }
    for _, tc := range cases {
        var pe *proxyentry.Error
        if !errors.As(tc.err, &pe) {
            t.Fatalf("%v is not a proxyentry.Error", tc.err)
        }
        if pe.Status != tc.status || pe.Code != tc.code {
            t.Fatalf("got %d/%s want %d/%s", pe.Status, pe.Code, tc.status, tc.code)
        }
    }
    if got := (&proxyentry.Error{}).HTTPHeaders(); got != nil {
        // zero value must not invent headers
        t.Fatalf("empty error produced headers %v", got)
    }
    var authErr *proxyentry.Error
    errors.As(proxyentry.ErrAuthFailed, &authErr)
    if authErr.HTTPHeaders()["Proxy-Authenticate"] == "" {
        t.Fatal("407 must advertise Proxy-Authenticate")
    }
    var capErr *proxyentry.Error
    errors.As(proxyentry.ErrCapacityExhausted, &capErr)
    if capErr.HTTPHeaders()["Retry-After"] != "5" {
        t.Fatal("503 must carry Retry-After: 5")
    }
}

func TestRouteActiveAndRequiresAuth(t *testing.T) {
    active := proxyentry.Route{Status: proxyentry.StatusActive, AuthMode: proxyentry.AuthModeBasic}
    if !active.Active() || !active.RequiresAuth() {
        t.Fatal("active basic route must require auth")
    }
    if (proxyentry.Route{Status: proxyentry.StatusDisabled}).Active() {
        t.Fatal("disabled route must not be active")
    }
    if (proxyentry.Route{Status: proxyentry.StatusActive, AuthMode: proxyentry.AuthModeNone}).RequiresAuth() {
        t.Fatal("none mode must not require auth")
    }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/proxyentry/ -count=1`
Expected: FAIL，`package internal/proxyentry` 不存在或符号未定义。

- [ ] **Step 3: 最小实现**

`errors.go` 用 `*Error` 结构承载 status/code/message/headers，导出变量用 `NewError` 构造；407 三个变量带 `Proxy-Authenticate: Basic realm="TunnelMesh", charset="UTF-8"`，503 带 `Retry-After: 5`。`route.go` 定义 `Route`、`Active()`（`Status == StatusActive`）、`RequiresAuth()`（`AuthMode == AuthModeBasic`）与 `RouteSource` 接口。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/proxyentry/ -count=1 && go vet ./internal/proxyentry/ && gofmt -l internal/proxyentry`
Expected: PASS，无 vet/gofmt 输出。

- [ ] **Step 5: Commit（需授权）**

```bash
git add internal/proxyentry/errors.go internal/proxyentry/errors_test.go internal/proxyentry/route.go internal/proxyentry/route_test.go
git commit -m "feat(proxy): add entry error and route types"
```

---

### Task 3: 路由身份解析（header 与 SNI 两种实现）

**Files:**

- Create: `internal/proxyentry/identity.go`
- Create: `internal/proxyentry/identity_test.go`

**Interfaces:**

- Consumes: Task 2 的 `ErrRouteIdentityInvalid`
- Produces:

```go
// RouteKey is the normalized lowercase full proxy host, e.g. tp-demo.tm.example.com.
type RouteKey string

func (k RouteKey) Name() string   // 去掉 tp- 前缀与域名后缀后的名称

type RouteIdentity interface {
    Resolve(r *http.Request) (RouteKey, error)
}

func NewHeaderRouteIdentity(header, domainSuffix string) HeaderRouteIdentity
type HeaderRouteIdentity struct{ Header, DomainSuffix string }
func (h HeaderRouteIdentity) Resolve(r *http.Request) (RouteKey, error)

func NewSNIRouteIdentity(domainSuffix string) SNIRouteIdentity
type SNIRouteIdentity struct{ DomainSuffix string }
func (s SNIRouteIdentity) Resolve(r *http.Request) (RouteKey, error)
```

- [ ] **Step 1: 写失败测试**

```go
package proxyentry_test

import (
    "crypto/tls"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
)

func TestHeaderRouteIdentityNormalizesAndRejects(t *testing.T) {
    identity := proxyentry.NewHeaderRouteIdentity("X-TunnelMesh-Route", "tm.example.com")
    cases := []struct {
        values []string
        want   proxyentry.RouteKey
        ok     bool
    }{
        {[]string{"tp-demo.tm.example.com"}, "tp-demo.tm.example.com", true},
        {[]string{"TP-Demo.TM.Example.COM"}, "tp-demo.tm.example.com", true},
        {[]string{"demo"}, "tp-demo.tm.example.com", true},
        {[]string{"tp-demo"}, "tp-demo.tm.example.com", true},
        {[]string{" tp-demo "}, "tp-demo.tm.example.com", true},
        {[]string{"tp-demo.other.com"}, "", false},
        {[]string{"other.com"}, "", false},
        {[]string{""}, "", false},
        {[]string{"tp-.tm.example.com"}, "", false},
        {[]string{"tp-de mo.tm.example.com"}, "", false},
        {[]string{"tp-a.tm.example.com", "tp-b.tm.example.com"}, "", false},
        {nil, "", false},
    }
    for _, tc := range cases {
        r := httptest.NewRequest(http.MethodGet, "http://target.example/", nil)
        r.Header.Del("X-TunnelMesh-Route")
        for _, v := range tc.values {
            r.Header.Add("X-TunnelMesh-Route", v)
        }
        got, err := identity.Resolve(r)
        if tc.ok && (err != nil || got != tc.want) {
            t.Fatalf("values=%q got %q err %v want %q", tc.values, got, err, tc.want)
        }
        if !tc.ok && err == nil {
            t.Fatalf("values=%q must be rejected, got %q", tc.values, got)
        }
    }
}

func TestRouteKeyName(t *testing.T) {
    if got := proxyentry.RouteKey("tp-demo.tm.example.com").Name(); got != "demo" {
        t.Fatalf("Name() = %q", got)
    }
}

func TestSNIRouteIdentityUsesTLSHandshake(t *testing.T) {
    identity := proxyentry.NewSNIRouteIdentity("tm.example.com")
    plain := httptest.NewRequest(http.MethodConnect, "https://target.example:443", nil)
    if _, err := identity.Resolve(plain); err == nil {
        t.Fatal("plaintext request must not resolve a route")
    }
    withSNI := httptest.NewRequest(http.MethodConnect, "https://target.example:443", nil)
    withSNI.TLS = &tls.ConnectionState{ServerName: "TP-Demo.tm.example.com"}
    got, err := identity.Resolve(withSNI)
    if err != nil || got != "tp-demo.tm.example.com" {
        t.Fatalf("got %q err %v", got, err)
    }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/proxyentry/ -run 'Identity|RouteKey' -count=1`
Expected: FAIL，`undefined: proxyentry.NewHeaderRouteIdentity`。

- [ ] **Step 3: 最小实现**

```go
package proxyentry

import (
    "crypto/tls"
    "net/http"
    "regexp"
    "strings"
)

var proxyRouteNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

type RouteKey string

func (k RouteKey) Name() string {
    host := string(k)
    host = strings.TrimPrefix(host, "tp-")
    if idx := strings.IndexByte(host, '.'); idx > 0 {
        host = host[:idx]
    }
    return host
}

type RouteIdentity interface {
    Resolve(r *http.Request) (RouteKey, error)
}

// HeaderRouteIdentity trusts a header that only the OpenResty front end may
// set. The listener rejects untrusted peers before parsing, so this header is
// never attacker-controlled in a correctly deployed topology.
type HeaderRouteIdentity struct {
    Header       string
    DomainSuffix string
}

func NewHeaderRouteIdentity(header, domainSuffix string) HeaderRouteIdentity {
    return HeaderRouteIdentity{Header: header, DomainSuffix: strings.ToLower(strings.TrimSpace(domainSuffix))}
}

func (h HeaderRouteIdentity) Resolve(r *http.Request) (RouteKey, error) {
    values := r.Header.Values(h.Header)
    if len(values) != 1 {
        return "", ErrRouteIdentityInvalid
    }
    return normalizeRouteKey(values[0], h.DomainSuffix)
}

// SNIRouteIdentity is the A2 fallback path: the Server terminates TLS and the
// route name comes from the handshake instead of a header.
type SNIRouteIdentity struct{ DomainSuffix string }

func NewSNIRouteIdentity(domainSuffix string) SNIRouteIdentity {
    return SNIRouteIdentity{DomainSuffix: strings.ToLower(strings.TrimSpace(domainSuffix))}
}

func (s SNIRouteIdentity) Resolve(r *http.Request) (RouteKey, error) {
    var state *tls.ConnectionState
    if r.TLS != nil {
        state = r.TLS
    }
    if state == nil || strings.TrimSpace(state.ServerName) == "" {
        return "", ErrRouteIdentityInvalid
    }
    return normalizeRouteKey(state.ServerName, s.DomainSuffix)
}

// normalizeRouteKey accepts either the bare name, the name with the tp- prefix,
// or the full host, and always returns the lowercase full host.
func normalizeRouteKey(raw, suffix string) (RouteKey, error) {
    value := strings.ToLower(strings.TrimSpace(raw))
    if value == "" || suffix == "" {
        return "", ErrRouteIdentityInvalid
    }
    name := strings.TrimPrefix(value, "tp-")
    if strings.Contains(name, ".") {
        if name == value {
            // A full host was supplied: it must end with the configured suffix.
            dot := strings.IndexByte(value, '.')
            name = strings.TrimPrefix(value[:dot], "tp-")
            if !strings.EqualFold(value[dot+1:], suffix) {
                return "", ErrRouteIdentityInvalid
            }
        } else {
            return "", ErrRouteIdentityInvalid
        }
    }
    if !proxyRouteNamePattern.MatchString(name) {
        return "", ErrRouteIdentityInvalid
    }
    return RouteKey("tp-" + name + "." + suffix), nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/proxyentry/ -count=1 && go vet ./internal/proxyentry/`
Expected: PASS。

- [ ] **Step 5: Commit（需授权）**

```bash
git add internal/proxyentry/identity.go internal/proxyentry/identity_test.go
git commit -m "feat(proxy): resolve tp route identity"
```

---

### Task 4: 源 IP ACL

**Files:**

- Create: `internal/proxyentry/acl.go`
- Create: `internal/proxyentry/acl_test.go`

**Interfaces:**

- Consumes: Task 2 的 `ErrSourceDenied`
- Produces:

```go
type SourceACL struct{ /* unexported */ }

func NewSourceACL(cidrs []string) (SourceACL, error)  // 写入路径使用：非法条目返回 error
func DenyAllSourceACL() SourceACL                     // 运行期构造失败时的 fail-closed 值
func (a SourceACL) Allow(ip net.IP) bool               // 空 ACL 一律 false
func (a SourceACL) Broken() bool                       // 构造时含非法条目
func ParseClientIP(raw string) (net.IP, error)         // 严格解析，拒绝 "ip:port" 与空串
func NormalizeCIDR(raw string) (string, error)          // 单 IP 自动补 /32 或 /128
```

- [ ] **Step 1: 写失败测试**

```go
package proxyentry_test

import (
    "net"
    "testing"

    "github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
)

func TestSourceACLMatching(t *testing.T) {
    acl, err := proxyentry.NewSourceACL([]string{"11.71.85.0/24", "10.1.2.3", "2001:db8::/32"})
    if err != nil {
        t.Fatalf("NewSourceACL: %v", err)
    }
    allow := []string{"11.71.85.7", "10.1.2.3", "2001:db8::1"}
    deny := []string{"11.71.86.1", "10.1.2.4", "2001:db9::1", ""}
    for _, raw := range allow {
        if !acl.Allow(net.ParseIP(raw)) {
            t.Fatalf("%s should be allowed", raw)
        }
    }
    for _, raw := range deny {
        if acl.Allow(net.ParseIP(raw)) {
            t.Fatalf("%s should be denied", raw)
        }
    }
    if acl.Allow(nil) {
        t.Fatal("nil IP must be denied")
    }
}

func TestSourceACLDefaultsToDeny(t *testing.T) {
    empty, err := proxyentry.NewSourceACL(nil)
    if err != nil {
        t.Fatalf("empty ACL must parse: %v", err)
    }
    if empty.Allow(net.ParseIP("8.8.8.8")) {
        t.Fatal("empty ACL must deny everything")
    }
    wildcard, err := proxyentry.NewSourceACL([]string{"0.0.0.0/0", "::/0"})
    if err != nil {
        t.Fatalf("wildcard ACL must parse: %v", err)
    }
    if !wildcard.Allow(net.ParseIP("8.8.8.8")) || !wildcard.Allow(net.ParseIP("2001:db8::1")) {
        t.Fatal("0.0.0.0/0 and ::/0 must allow everything")
    }
}

func TestSourceACLRejectsInvalidEntries(t *testing.T) {
    if _, err := proxyentry.NewSourceACL([]string{"10.0.0.0/33"}); err == nil {
        t.Fatal("invalid CIDR must fail at write time")
    }
    if _, err := proxyentry.NewSourceACL([]string{"not-an-ip"}); err == nil {
        t.Fatal("invalid literal must fail at write time")
    }
    broken := proxyentry.DenyAllSourceACL()
    if !broken.Broken() || broken.Allow(net.ParseIP("8.8.8.8")) {
        t.Fatal("deny-all ACL must report broken and deny")
    }
}

func TestParseClientIP(t *testing.T) {
    if ip, err := proxyentry.ParseClientIP(" 11.71.85.176 "); err != nil || ip.String() != "11.71.85.176" {
        t.Fatalf("got %v err %v", ip, err)
    }
    for _, raw := range []string{"", "11.71.85.176:4321", "abc", "11.71.85.176, 10.0.0.1"} {
        if _, err := proxyentry.ParseClientIP(raw); err == nil {
            t.Fatalf("%q must be rejected", raw)
        }
    }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/proxyentry/ -run 'SourceACL|ParseClientIP' -count=1`
Expected: FAIL，`undefined: proxyentry.NewSourceACL`。

- [ ] **Step 3: 最小实现**

`acl.go`：内部字段 `networks []*net.IPNet` 与 `broken bool`。`NormalizeCIDR` 对不含 `/` 的输入按 `net.ParseIP` 结果补 `/32`（IPv4）或 `/128`（IPv6），再交给 `net.ParseCIDR`。`NewSourceACL` 遇到任一非法条目返回 error 且返回 `DenyAllSourceACL()` 的值。`Allow` 在 `broken` 或 `len(networks)==0` 或 `ip == nil` 时返回 false，否则对每个 network 调 `Contains`（先把 IPv4 规范化为 4 字节形式，避免 v4-in-v6 映射不匹配）。`ParseClientIP` 用 `net.ParseIP(strings.TrimSpace(raw))`，为 nil 时返回 error；显式拒绝含 `:` 且不是合法 IPv6 的 `ip:port` 形式与逗号列表。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/proxyentry/ -count=1 && go vet ./internal/proxyentry/ && gofmt -l internal/proxyentry`
Expected: PASS。

- [ ] **Step 5: Commit（需授权）**

```bash
git add internal/proxyentry/acl.go internal/proxyentry/acl_test.go
git commit -m "feat(proxy): add source IP allowlist"
```

---

### Task 5: Basic 认证与失败退避

**Files:**

- Create: `internal/proxyentry/auth.go`
- Create: `internal/proxyentry/auth_test.go`

**Interfaces:**

- Consumes: Task 2 的 `Route`、`ErrAuthRequired`、`ErrAuthFailed`、`ErrAuthBackoff`、`ErrSecretUnavailable`
- Produces:

```go
type CredentialSecret struct{ Username, Password string }

// SecretResolver is implemented by the server on top of storage.CredentialRepository
// plus auth.SecretStore. It never returns plaintext to callers outside this package.
type SecretResolver interface {
    ProxyBasicSecret(ctx context.Context, credentialID string) (CredentialSecret, error)
}

// ErrSecretStoreUnavailable is returned by SecretResolver implementations when
// decryption cannot even be attempted (missing key, store failure).
var ErrSecretStoreUnavailable = errors.New("proxyentry: credential secret store unavailable")

type Authenticator struct{ /* unexported */ }

func NewAuthenticator(resolver SecretResolver, backoffThreshold int) *Authenticator
func (a *Authenticator) SetClock(now func() time.Time)
func (a *Authenticator) Authorize(ctx context.Context, route Route, clientIP net.IP, header string) error
func (a *Authenticator) Attempts(routeID, clientIP string) int   // 测试与诊断用
```

- [ ] **Step 1: 写失败测试**

```go
package proxyentry_test

import (
    "context"
    "encoding/base64"
    "errors"
    "net"
    "testing"
    "time"

    "github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
)

type stubSecrets struct {
    secret proxyentry.CredentialSecret
    err    error
    calls  int
}

func (s *stubSecrets) ProxyBasicSecret(context.Context, string) (proxyentry.CredentialSecret, error) {
    s.calls++
    return s.secret, s.err
}

func basic(user, pass string) string {
    return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func TestAuthenticatorModes(t *testing.T) {
    secrets := &stubSecrets{secret: proxyentry.CredentialSecret{Username: "demo", Password: "s3cret"}}
    auth := proxyentry.NewAuthenticator(secrets, 5)
    route := proxyentry.Route{ID: "r1", AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1"}
    ip := net.ParseIP("11.71.85.7")

    if err := auth.Authorize(context.Background(), route, ip, basic("demo", "s3cret")); err != nil {
        t.Fatalf("valid credentials rejected: %v", err)
    }
    if err := auth.Authorize(context.Background(), proxyentry.Route{ID: "r2", AuthMode: proxyentry.AuthModeNone}, ip, ""); err != nil {
        t.Fatalf("none mode must skip auth: %v", err)
    }
    if err := auth.Authorize(context.Background(), route, ip, ""); !errors.Is(err, proxyentry.ErrAuthRequired) {
        t.Fatalf("missing header = %v", err)
    }
    if err := auth.Authorize(context.Background(), route, ip, "Bearer abc"); !errors.Is(err, proxyentry.ErrAuthRequired) {
        t.Fatalf("wrong scheme = %v", err)
    }
    if err := auth.Authorize(context.Background(), route, ip, basic("demo", "wrong")); !errors.Is(err, proxyentry.ErrAuthFailed) {
        t.Fatalf("wrong password = %v", err)
    }
    if err := auth.Authorize(context.Background(), route, ip, basic("DEMO", "s3cret")); !errors.Is(err, proxyentry.ErrAuthFailed) {
        t.Fatalf("username must match exactly, got %v", err)
    }
}

func TestAuthenticatorSecretStoreFailure(t *testing.T) {
    secrets := &stubSecrets{err: proxyentry.ErrSecretStoreUnavailable}
    auth := proxyentry.NewAuthenticator(secrets, 5)
    route := proxyentry.Route{ID: "r1", AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1"}
    err := auth.Authorize(context.Background(), route, net.ParseIP("10.0.0.1"), basic("demo", "s3cret"))
    if !errors.Is(err, proxyentry.ErrSecretUnavailable) {
        t.Fatalf("got %v", err)
    }
    if auth.Attempts("r1", "10.0.0.1") != 0 {
        t.Fatal("store failures must not count against the user")
    }
}

func TestAuthenticatorBackoff(t *testing.T) {
    secrets := &stubSecrets{secret: proxyentry.CredentialSecret{Username: "demo", Password: "s3cret"}}
    auth := proxyentry.NewAuthenticator(secrets, 3)
    now := time.Unix(1_800_000_000, 0)
    auth.SetClock(func() time.Time { return now })
    route := proxyentry.Route{ID: "r1", AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1"}
    ip := net.ParseIP("10.0.0.9")
    for i := 0; i < 3; i++ {
        if err := auth.Authorize(context.Background(), route, ip, basic("demo", "bad")); !errors.Is(err, proxyentry.ErrAuthFailed) {
            t.Fatalf("attempt %d = %v", i, err)
        }
    }
    callsBefore := secrets.calls
    if err := auth.Authorize(context.Background(), route, ip, basic("demo", "s3cret")); !errors.Is(err, proxyentry.ErrAuthBackoff) {
        t.Fatalf("after threshold got %v", err)
    }
    if secrets.calls != callsBefore {
        t.Fatal("backoff must short-circuit before comparing passwords")
    }
    now = now.Add(31 * time.Second)
    if err := auth.Authorize(context.Background(), route, ip, basic("demo", "s3cret")); err != nil {
        t.Fatalf("after 30s backoff got %v", err)
    }
    if auth.Attempts("r1", "10.0.0.9") != 0 {
        t.Fatal("success must reset the counter")
    }
}

func TestAuthenticatorMissingCredentialID(t *testing.T) {
    auth := proxyentry.NewAuthenticator(&stubSecrets{}, 5)
    route := proxyentry.Route{ID: "r1", AuthMode: proxyentry.AuthModeBasic}
    if err := auth.Authorize(context.Background(), route, net.ParseIP("10.0.0.1"), basic("demo", "x")); !errors.Is(err, proxyentry.ErrAuthFailed) {
        t.Fatalf("missing credentialId = %v", err)
    }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/proxyentry/ -run Authenticator -count=1`
Expected: FAIL，`undefined: proxyentry.NewAuthenticator`。

- [ ] **Step 3: 最小实现**

`auth.go` 内部：

```go
type authAttempt struct {
    failures int
    until    time.Time
}

type Authenticator struct {
    resolver  SecretResolver
    threshold int
    now       func() time.Time
    mu        sync.Mutex
    attempts  map[string]*authAttempt
}
```

`Authorize` 顺序：`route.RequiresAuth()` 为假直接返回 nil；`route.CredentialID == ""` 记一次失败并返回 `ErrAuthFailed`；查退避（`until.After(now)` 返回 `ErrAuthBackoff`，且不调用 resolver）；解析 header（必须 `Basic ` 前缀 + 合法 base64 + 恰好一个 `:`，否则记失败并返回 `ErrAuthRequired`）；调用 `resolver.ProxyBasicSecret`，`errors.Is(err, ErrSecretStoreUnavailable)` 返回 `ErrSecretUnavailable` 且**不计数**，其它 error 记失败并返回 `ErrAuthFailed`；用户名与密码分别用 `sha256.Sum256` 后 `subtle.ConstantTimeCompare` 比较，任一不匹配记失败并返回 `ErrAuthFailed`；成功则清空该 key 的计数并返回 nil。

退避时长：`30 * time.Second << min(failures-threshold, 5)`，上限 `15 * time.Minute`。key 为 `routeID + "|" + clientIP.String()`。`SetClock` 仅用于测试；`attempts` map 在成功或过期后删除条目，避免无界增长（每次写入前顺带清理已过期的 key，单次最多扫描 64 条）。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/proxyentry/ -count=1 && go test -race ./internal/proxyentry/ -count=1 && go vet ./internal/proxyentry/`
Expected: PASS。

- [ ] **Step 5: Commit（需授权）**

```bash
git add internal/proxyentry/auth.go internal/proxyentry/auth_test.go
git commit -m "feat(proxy): verify basic auth with backoff"
```

---

### Task 6: 目标地址校验

**Files:**

- Create: `internal/proxyentry/target.go`
- Create: `internal/proxyentry/target_test.go`

**Interfaces:**

- Consumes: `routing.NewPolicy(cidrs []string, ports any) (routing.Policy, error)`、`routing.Policy.Validate(ip net.IP, port int) error`、`routing.IsDangerousAddress(ip net.IP) bool`、Task 2 的 `ErrTargetDenied` / `ErrTargetInvalid`
- Produces:

```go
type TargetPolicy struct{ /* unexported */ }

func NewTargetPolicy(cidrs []string, ports []int, allowPrivateTargets bool) (TargetPolicy, error)
func (p TargetPolicy) Validate(host string, port int) error     // host 可为域名或 IP 字面量
func (p TargetPolicy) ValidateIP(ip net.IP, port int) error
func IsPrivateTarget(ip net.IP) bool
```

- [ ] **Step 1: 写失败测试**

```go
package proxyentry_test

import (
    "errors"
    "net"
    "testing"

    "github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
)

func TestTargetPolicyIPLiterals(t *testing.T) {
    open, err := proxyentry.NewTargetPolicy(nil, nil, true)
    if err != nil {
        t.Fatalf("NewTargetPolicy: %v", err)
    }
    if err := open.Validate("93.184.216.34", 443); err != nil {
        t.Fatalf("public target rejected: %v", err)
    }
    if err := open.Validate("10.0.0.5", 8080); err != nil {
        t.Fatalf("private target must be allowed by default: %v", err)
    }
    if err := open.Validate("169.254.169.254", 80); !errors.Is(err, proxyentry.ErrTargetDenied) {
        t.Fatalf("metadata address = %v", err)
    }
    // routing.IsDangerousAddress deliberately keeps loopback reachable: the
    // target is dialed from the Agent host, so 127.0.0.1 is a valid
    // agent-local service address. Only multicast/reserved ranges are denied.
    if err := open.Validate("127.0.0.1", 22); err != nil {
        t.Fatalf("loopback must follow routing.Policy and stay allowed: %v", err)
    }
    if err := open.Validate("224.0.0.1", 80); !errors.Is(err, proxyentry.ErrTargetDenied) {
        t.Fatalf("multicast = %v", err)
    }
    if err := open.Validate("2001:db8::1", 443); !errors.Is(err, proxyentry.ErrTargetDenied) {
        t.Fatalf("IPv6 literal follows routing.Policy and is denied = %v", err)
    }
    if err := open.Validate("93.184.216.34", 0); !errors.Is(err, proxyentry.ErrTargetInvalid) {
        t.Fatalf("port 0 = %v", err)
    }
    if err := open.Validate("93.184.216.34", 70000); !errors.Is(err, proxyentry.ErrTargetInvalid) {
        t.Fatalf("port 70000 = %v", err)
    }
    if err := open.Validate("", 80); !errors.Is(err, proxyentry.ErrTargetInvalid) {
        t.Fatalf("empty host = %v", err)
    }
}

func TestTargetPolicyPrivateSwitchAndAllowlist(t *testing.T) {
    strict, err := proxyentry.NewTargetPolicy(nil, nil, false)
    if err != nil {
        t.Fatalf("NewTargetPolicy: %v", err)
    }
    if err := strict.Validate("10.0.0.5", 80); !errors.Is(err, proxyentry.ErrTargetDenied) {
        t.Fatalf("private target with allowPrivateTargets=false = %v", err)
    }
    if err := strict.Validate("127.0.0.1", 22); !errors.Is(err, proxyentry.ErrTargetDenied) {
        t.Fatalf("loopback with allowPrivateTargets=false = %v", err)
    }
    allowlisted, err := proxyentry.NewTargetPolicy([]string{"10.10.0.0/16"}, []int{443, 8443}, true)
    if err != nil {
        t.Fatalf("NewTargetPolicy: %v", err)
    }
    if err := allowlisted.Validate("10.10.1.1", 443); err != nil {
        t.Fatalf("allowlisted target rejected: %v", err)
    }
    if err := allowlisted.Validate("10.10.1.1", 80); !errors.Is(err, proxyentry.ErrTargetDenied) {
        t.Fatalf("port outside allowlist = %v", err)
    }
    if err := allowlisted.Validate("10.20.1.1", 443); !errors.Is(err, proxyentry.ErrTargetDenied) {
        t.Fatalf("CIDR outside allowlist = %v", err)
    }
}

func TestTargetPolicyDefersDomainsToAgent(t *testing.T) {
    policy, err := proxyentry.NewTargetPolicy([]string{"10.10.0.0/16"}, nil, true)
    if err != nil {
        t.Fatalf("NewTargetPolicy: %v", err)
    }
    // A CIDR allowlist cannot be evaluated before resolution, so domains are
    // rejected here exactly like routing.Policy does for agent policies.
    if err := policy.Validate("intranet.example.com", 443); !errors.Is(err, proxyentry.ErrTargetDenied) {
        t.Fatalf("domain with CIDR allowlist = %v", err)
    }
    open, err := proxyentry.NewTargetPolicy(nil, nil, true)
    if err != nil {
        t.Fatalf("NewTargetPolicy: %v", err)
    }
    if err := open.Validate("intranet.example.com", 443); err != nil {
        t.Fatalf("domain without allowlist must be deferred to the agent: %v", err)
    }
    for _, host := range []string{"bad host", "host/path", "a.b.c." + string(make([]byte, 250))} {
        if err := open.Validate(host, 443); !errors.Is(err, proxyentry.ErrTargetInvalid) {
            t.Fatalf("%q = %v", host, err)
        }
    }
}

func TestIsPrivateTarget(t *testing.T) {
    for _, raw := range []string{"10.0.0.1", "192.168.1.1", "172.16.0.1", "127.0.0.1", "169.254.1.1", "fd00::1", "::1"} {
        if !proxyentry.IsPrivateTarget(net.ParseIP(raw)) {
            t.Fatalf("%s should count as private", raw)
        }
    }
    if proxyentry.IsPrivateTarget(net.ParseIP("93.184.216.34")) {
        t.Fatal("public address must not count as private")
    }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/proxyentry/ -run 'TargetPolicy|IsPrivateTarget' -count=1`
Expected: FAIL，`undefined: proxyentry.NewTargetPolicy`。

- [ ] **Step 3: 最小实现**

`target.go`：

```go
type TargetPolicy struct {
    policy              routing.Policy
    allowPrivateTargets bool
    restrictCIDRs       bool
}

func NewTargetPolicy(cidrs []string, ports []int, allowPrivateTargets bool) (TargetPolicy, error) {
    policy, err := routing.NewPolicy(cidrs, ports)
    if err != nil {
        return TargetPolicy{}, err
    }
    return TargetPolicy{policy: policy, allowPrivateTargets: allowPrivateTargets, restrictCIDRs: len(cidrs) > 0}, nil
}

func (p TargetPolicy) Validate(host string, port int) error {
    if port < 1 || port > 65535 {
        return ErrTargetInvalid
    }
    trimmed := strings.TrimSpace(host)
    if trimmed == "" || len(trimmed) > 253 || strings.ContainsAny(trimmed, " /\t\r\n") {
        return ErrTargetInvalid
    }
    if ip := net.ParseIP(trimmed); ip != nil {
        return p.ValidateIP(ip, port)
    }
    if !validProxyHostname(trimmed) {
        return ErrTargetInvalid
    }
    // Domains cannot be matched against a CIDR allowlist before resolution, so
    // an allowlist behaves like the existing agent policy and denies them. The
    // agent re-validates the resolved address before dialing.
    if p.restrictCIDRs {
        return ErrTargetDenied
    }
    return nil
}

func (p TargetPolicy) ValidateIP(ip net.IP, port int) error {
    if ip == nil {
        return ErrTargetInvalid
    }
    if routing.IsDangerousAddress(ip) {
        return ErrTargetDenied
    }
    if !p.allowPrivateTargets && IsPrivateTarget(ip) {
        return ErrTargetDenied
    }
    if err := p.policy.Validate(ip, port); err != nil {
        return ErrTargetDenied
    }
    return nil
}

func IsPrivateTarget(ip net.IP) bool {
    if ip == nil {
        return false
    }
    return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
```

`validProxyHostname` 用 `regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$`)` 且要求不含 `..`、不以 `.` 结尾。

**注意**：`routing.Policy.Validate` 现有实现对 `ip.To4() == nil` 返回 `ErrDangerousAddress`，即 IPv6 目标一律拒绝。本任务沿用该语义（agent 侧策略同样如此），不修改 `internal/routing`。源 ACL 的 IPv6 支持在 Task 4 中独立实现，两者不冲突。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/proxyentry/ ./internal/routing/ -count=1 && go vet ./internal/proxyentry/`
Expected: PASS。

- [ ] **Step 5: Commit（需授权）**

```bash
git add internal/proxyentry/target.go internal/proxyentry/target_test.go
git commit -m "feat(proxy): validate proxy target addresses"
```

---

### Task 7: proxy 路由快照（与 HTTP 反代路由分流）

**Files:**

- Modify: `internal/storage/models.go`（新增 `ProtocolHTTPProxy`、`ProxyTargetWildcard` 常量）
- Modify: `internal/server/managed_route_handler.go`（`loadManagedRoutes` 显式跳过 `http-proxy`；`ManagedRouteTable` 增加 proxy 快照字段）
- Create: `internal/server/proxy_entry_routes.go`（`loadProxyRoutes`、`ManagedRouteTable.ProxyRoute`）
- Create: `internal/server/proxy_entry_routes_test.go`

**Interfaces:**

- Consumes: `storage.Open(ctx, storage.DriverSQLite, dsn, true)`、`db.Tunnels().Create/List`、Task 2 的 `proxyentry.Route`
- Produces:

```go
// storage 常量
const ProtocolHTTPProxy = "http-proxy"
const ProxyTargetWildcard = "*"

// ManagedRouteTable 新增方法（沿用既有 5s TTL 与 stale-on-error 策略）
func (t *ManagedRouteTable) ProxyRoute(ctx context.Context, domain string) (proxyentry.Route, bool)

// 新增包级函数
func loadProxyRoutes(ctx context.Context, db *storage.DB) ([]proxyentry.Route, error)
```

- [ ] **Step 1: 写失败测试**

```go
package server

import (
    "context"
    "testing"
    "time"

    "github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func createProxyRouteFixture(t *testing.T, db *storage.DB, id, domain, status, config string) {
    t.Helper()
    err := db.Tunnels().Create(context.Background(), storage.Tunnel{
        ID: id, AgentID: "agent-" + id, Protocol: storage.ProtocolHTTPProxy, Domain: domain,
        PathPrefix: "/", TargetHost: storage.ProxyTargetWildcard, TargetPort: 0, Status: status,
        Config: config, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
    })
    if err != nil {
        t.Fatal(err)
    }
}

func TestLoadProxyRoutesDecodesPolicyConfig(t *testing.T) {
    db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:proxy-route-snapshot?mode=memory&cache=shared", true)
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = db.Close() })
    createProxyRouteFixture(t, db, "p1", "tp-demo.tm.example.com", "active",
        `{"authMode":"basic","credentialId":"c1","sourceCIDRs":["11.71.85.0/24"],"targetCIDRs":["10.10.0.0/16"],"targetPorts":[443],"allowPrivateTargets":false,"maxConcurrentTunnels":7,"description":"demo"}`)

    routes, err := loadProxyRoutes(context.Background(), db)
    if err != nil {
        t.Fatal(err)
    }
    if len(routes) != 1 {
        t.Fatalf("routes = %#v", routes)
    }
    got := routes[0]
    if got.ID != "p1" || got.Domain != "tp-demo.tm.example.com" || got.Name != "demo" || got.AgentID != "agent-p1" {
        t.Fatalf("identity = %#v", got)
    }
    if got.AuthMode != "basic" || got.CredentialID != "c1" || got.AllowPrivateTargets || got.MaxConcurrentTunnels != 7 {
        t.Fatalf("policy = %#v", got)
    }
    if len(got.SourceCIDRs) != 1 || got.SourceCIDRs[0] != "11.71.85.0/24" || len(got.TargetCIDRs) != 1 || len(got.TargetPorts) != 1 {
        t.Fatalf("allowlists = %#v", got)
    }
}

func TestLoadProxyRoutesDefaultsAndInvalidConfig(t *testing.T) {
    db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:proxy-route-defaults?mode=memory&cache=shared", true)
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = db.Close() })
    createProxyRouteFixture(t, db, "p2", "tp-plain.tm.example.com", "active", `{}`)
    routes, err := loadProxyRoutes(context.Background(), db)
    if err != nil {
        t.Fatal(err)
    }
    if routes[0].AuthMode != "none" || !routes[0].AllowPrivateTargets || routes[0].MaxConcurrentTunnels != 0 {
        t.Fatalf("defaults = %#v", routes[0])
    }
    createProxyRouteFixture(t, db, "p3", "tp-bad.tm.example.com", "active", `{"authMode":`)
    if _, err := loadProxyRoutes(context.Background(), db); err == nil {
        t.Fatal("invalid config JSON must fail the snapshot")
    }
}

func TestProxyRoutesStayOutOfManagedHTTPRoutes(t *testing.T) {
    db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:proxy-route-isolation?mode=memory&cache=shared", true)
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = db.Close() })
    createManagedRouteFixture(t, db, "http-route", `{"targetScheme":"https"}`)
    // A proxy route with a valid target port must still not leak into the
    // reverse-proxy resolver.
    err = db.Tunnels().Create(context.Background(), storage.Tunnel{
        ID: "leak", AgentID: "agent-leak", Protocol: storage.ProtocolHTTPProxy,
        Domain: "tp-leak.example.com", PathPrefix: "/", TargetHost: "10.0.0.9", TargetPort: 443,
        Status: "active", Config: `{}`, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
    })
    if err != nil {
        t.Fatal(err)
    }
    routes, err := loadManagedRoutes(context.Background(), db)
    if err != nil {
        t.Fatal(err)
    }
    if len(routes) != 1 || routes[0].ID != "http-route" {
        t.Fatalf("managed HTTP routes = %#v", routes)
    }
}

func TestManagedRouteTableProxyRouteLookupAndTTL(t *testing.T) {
    db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:proxy-route-table?mode=memory&cache=shared", true)
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = db.Close() })
    table := NewManagedRouteTable(db, "apps.example.com", 20*time.Millisecond)
    if _, ok := table.ProxyRoute(context.Background(), "tp-demo.tm.example.com"); ok {
        t.Fatal("unknown route must not resolve")
    }
    createProxyRouteFixture(t, db, "p1", "tp-demo.tm.example.com", "active", `{}`)
    route, ok := table.ProxyRoute(context.Background(), "TP-DEMO.tm.example.com")
    if !ok || route.ID != "p1" {
        t.Fatalf("lookup = %v %v", ok, route)
    }
    // Disabled routes stay in the snapshot so the handler can answer 403 with
    // the same shape as an unknown route.
    createProxyRouteFixture(t, db, "p9", "tp-off.tm.example.com", "disabled", `{}`)
    time.Sleep(40 * time.Millisecond)
    off, ok := table.ProxyRoute(context.Background(), "tp-off.tm.example.com")
    if !ok || off.Active() {
        t.Fatalf("disabled route = %v %v", ok, off)
    }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/server/ -run 'ProxyRoute|LoadProxyRoutes' -count=1`
Expected: FAIL，`undefined: storage.ProtocolHTTPProxy`、`undefined: loadProxyRoutes`。

- [ ] **Step 3: 最小实现**

`internal/storage/models.go` 在 `CredentialType` 常量附近加：

```go
// ProtocolHTTPProxy marks a managed route that terminates a forward-proxy
// request instead of reverse-proxying a fixed target.
const ProtocolHTTPProxy = "http-proxy"

// ProxyTargetWildcard is the sentinel stored in tunnels.target_host for
// http-proxy routes, whose real target comes from each request.
const ProxyTargetWildcard = "*"
```

`internal/server/managed_route_handler.go`：在 `loadManagedRoutes` 的分页循环里，`managedTunnelActive` 判断之前加入

```go
if tunnel.Protocol == storage.ProtocolHTTPProxy {
    // Proxy entries are resolved by ManagedRouteTable.ProxyRoute; they must
    // never reach the Host/path reverse-proxy resolver.
    continue
}
```

并在 `ManagedRouteTable` 结构中增加 `proxyRoutes map[string]proxyentry.Route` 字段（由既有 `mu` 保护）。

`internal/server/proxy_entry_routes.go`：

```go
package server

import (
    "context"
    "encoding/json"
    "errors"
    "log/slog"
    "strings"
    "time"

    "github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
    "github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// ProxyRoute returns one tp-* route from the cached snapshot. Lookups are
// case-insensitive and refresh on the same TTL as the HTTP resolver.
func (t *ManagedRouteTable) ProxyRoute(ctx context.Context, domain string) (proxyentry.Route, bool) {
    if t == nil {
        return proxyentry.Route{}, false
    }
    key := strings.ToLower(strings.TrimSpace(domain))
    if key == "" {
        return proxyentry.Route{}, false
    }
    now := time.Now()
    t.mu.RLock()
    if t.proxyRoutes != nil && now.Sub(t.lastAttemptAt) < t.ttl {
        route, ok := t.proxyRoutes[key]
        t.mu.RUnlock()
        return route, ok
    }
    t.mu.RUnlock()

    t.mu.Lock()
    defer t.mu.Unlock()
    now = time.Now()
    if t.proxyRoutes != nil && now.Sub(t.lastAttemptAt) < t.ttl {
        route, ok := t.proxyRoutes[key]
        return route, ok
    }
    routes, err := loadProxyRoutes(ctx, t.db)
    if err != nil {
        if t.proxyRoutes != nil {
            slog.WarnContext(ctx, "proxy route refresh failed; using cached routes", "error", err.Error())
            route, ok := t.proxyRoutes[key]
            return route, ok
        }
        slog.WarnContext(ctx, "proxy route snapshot unavailable", "error", err.Error())
        return proxyentry.Route{}, false
    }
    snapshot := make(map[string]proxyentry.Route, len(routes))
    for _, route := range routes {
        snapshot[route.Domain] = route
    }
    t.proxyRoutes = snapshot
    t.loadedAt = now
    route, ok := snapshot[key]
    return route, ok
}

// loadProxyRoutes pages the same tunnels table used by managed HTTP routes and
// decodes the proxy-specific config JSON.
func loadProxyRoutes(ctx context.Context, db *storage.DB) ([]proxyentry.Route, error) {
    if db == nil || db.Tunnels() == nil {
        return nil, errors.New("proxy route repository unavailable")
    }
    var routes []proxyentry.Route
    cursor := ""
    seenCursors := make(map[string]struct{})
    for {
        page, err := db.Tunnels().List(ctx, cursor, managedRoutePageSize)
        if err != nil {
            return nil, err
        }
        for _, tunnel := range page.Items {
            if tunnel.Protocol != storage.ProtocolHTTPProxy {
                continue
            }
            if tunnel.Domain == "" || tunnel.AgentID == "" {
                continue
            }
            var cfg struct {
                AuthMode             string   `json:"authMode"`
                CredentialID         string   `json:"credentialId"`
                SourceCIDRs          []string `json:"sourceCIDRs"`
                TargetCIDRs          []string `json:"targetCIDRs"`
                TargetPorts          []int    `json:"targetPorts"`
                AllowPrivateTargets  *bool    `json:"allowPrivateTargets"`
                MaxConcurrentTunnels int      `json:"maxConcurrentTunnels"`
            }
            cfg.AllowPrivateTargets = boolPtr(true)
            if tunnel.Config != "" {
                if err := json.Unmarshal([]byte(tunnel.Config), &cfg); err != nil {
                    return nil, errors.New("proxy route config is invalid: " + tunnel.ID)
                }
            }
            authMode := strings.ToLower(strings.TrimSpace(cfg.AuthMode))
            if authMode == "" {
                authMode = proxyentry.AuthModeNone
            }
            domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(tunnel.Domain), "."))
            routes = append(routes, proxyentry.Route{
                ID: tunnel.ID, Domain: domain, Name: proxyentry.RouteKey(domain).Name(),
                AgentID: tunnel.AgentID, AuthMode: authMode, CredentialID: strings.TrimSpace(cfg.CredentialID),
                SourceCIDRs: cfg.SourceCIDRs, TargetCIDRs: cfg.TargetCIDRs, TargetPorts: cfg.TargetPorts,
                AllowPrivateTargets: *cfg.AllowPrivateTargets, MaxConcurrentTunnels: cfg.MaxConcurrentTunnels,
                Status: tunnel.Status,
            })
        }
        if !page.HasMore {
            return routes, nil
        }
        if page.NextCursor == "" || page.NextCursor == cursor {
            return nil, errors.New("proxy route pagination did not advance")
        }
        if _, ok := seenCursors[page.NextCursor]; ok {
            return nil, errors.New("proxy route pagination repeated a cursor")
        }
        seenCursors[page.NextCursor] = struct{}{}
        cursor = page.NextCursor
    }
}

func boolPtr(v bool) *bool { return &v }
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/server/ ./internal/storage/ -count=1 && go vet ./internal/server/ ./internal/storage/`
Expected: PASS（含既有 managed route 测试不回归）。

- [ ] **Step 5: Commit（需授权）**

```bash
git add internal/storage/models.go internal/server/managed_route_handler.go internal/server/proxy_entry_routes.go internal/server/proxy_entry_routes_test.go
git commit -m "feat(server): snapshot tp proxy routes"
```

---

### Task 8: 凭据类型 `proxy_basic`

**Files:**

- Modify: `internal/storage/models.go`（`CredentialTypeProxyBasic` 常量）
- Modify: `internal/storage/credential_repository.go`（`validateCredential` 增加分支）
- Modify: `internal/storage/credential_repository_test.go`
- Modify: `internal/server/credential_service.go`（`validateCredentialShape`、`Create`、`Update` 的 username/password 落位）
- Modify: `internal/server/credential_service_test.go`
- Modify: `internal/server/credential_api.go`（`credentialRequest.Username` / `credentialResponse.Username` 透传，见 Task 12；`credentialResponse.Username` 取 `credential.PublicKey`，前端不必理解 username 复用该列的实现细节）
- 不改: `internal/server/api.go`（本任务只动凭据链路；`/api/v1/routes` 的 `http-proxy` 校验分支与 `proxyURL` 由 Task 12 负责）

**Interfaces:**

- Consumes: `auth.SecretStore.Encrypt(secret string) ([]byte, []byte, string, int, error)`、`auth.SecretStore.Decrypt(ciphertext, nonce []byte, keyID string, version int) (string, error)`、既有 `credentialSecretBlob`
- Produces:

```go
const CredentialTypeProxyBasic CredentialType = "proxy_basic"

// CredentialService 新增：为 proxy_basic 读取 username 与 password。
func (s *CredentialService) ProxyBasicSecret(ctx context.Context, actor auth.Principal, id string) (username string, password string, err error)
```

`CredentialInput` 增加字段 `Username string`（`proxy_basic` 必填，写入 `credential.PublicKey`，`Fingerprint = hex(sha256(username))`）。
`CredentialPatch` 增加字段 `Username *string`（`nil` 表示不变；非 nil 时改写 `credential.PublicKey` 并重算 fingerprint）。Task 12 的 `credential_api.go` 依赖这两个字段，必须在本任务一并加上。

- [ ] **Step 1: 写失败测试**

在 `internal/storage/credential_repository_test.go` 追加：

```go
func TestCredentialRepositoryAcceptsProxyBasic(t *testing.T) {
    db, err := OpenSQLite(context.Background(), "file:credential-proxy-basic?mode=memory&cache=shared")
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = db.Close() })
    created, err := db.Credentials().Create(context.Background(), Credential{
        ID: "cred-proxy", OwnerUserID: "user-1", Name: "proxy demo",
        Type: CredentialTypeProxyBasic, PublicKey: "demo", Fingerprint: "fp-demo",
        SecretCiphertext: "ct", SecretNonce: "nonce", SecretKeyID: "key-1", SecretVersion: 1,
        Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
    })
    if err != nil {
        t.Fatalf("proxy_basic credential rejected: %v", err)
    }
    if !created.HasSecret() || created.Type != CredentialTypeProxyBasic {
        t.Fatalf("created = %#v", created)
    }
    if _, err := db.Credentials().Create(context.Background(), Credential{
        ID: "cred-bad", OwnerUserID: "user-1", Name: "no username",
        Type: CredentialTypeProxyBasic, Enabled: true,
        CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
    }); err == nil {
        t.Fatal("proxy_basic without username must be rejected")
    }
}
```

在 `internal/server/credential_service_test.go` 追加（沿用该文件既有的 `newCredentialSecretFixture`，它返回 `(*storage.DB, *CredentialService)` 且已注入 AES-GCM secret store）：

```go
func TestCredentialServiceCreatesProxyBasicWithEncryptedPassword(t *testing.T) {
    db, service := newCredentialSecretFixture(t)
    actor := auth.Principal{UserID: "admin-a", Username: "root", Role: "admin"}
    created, err := service.Create(context.Background(), actor, CredentialInput{
        Name: "proxy demo", Type: storage.CredentialTypeProxyBasic, Username: "demo",
        Enabled: true, Secret: &CredentialSecret{Password: "s3cret"},
    })
    if err != nil {
        t.Fatalf("create: %v", err)
    }
    if created.PublicKey != "demo" || created.Fingerprint == "" || !created.HasSecret() {
        t.Fatalf("created = %#v", created)
    }
    username, password, err := service.ProxyBasicSecret(context.Background(), actor, created.ID)
    if err != nil {
        t.Fatalf("ProxyBasicSecret: %v", err)
    }
    if username != "demo" || password != "s3cret" {
        t.Fatalf("secret = %q/%q", username, password)
    }
    if _, _, err := service.Create(context.Background(), actor, CredentialInput{
        Name: "missing password", Type: storage.CredentialTypeProxyBasic, Username: "demo", Enabled: true,
    }); err == nil {
        t.Fatal("proxy_basic requires a password")
    }
    if _, _, err := service.Create(context.Background(), actor, CredentialInput{
        Name: "missing username", Type: storage.CredentialTypeProxyBasic, Enabled: true,
        Secret: &CredentialSecret{Password: "s3cret"},
    }); err == nil {
        t.Fatal("proxy_basic requires a username")
    }
    _ = db
}
```

哨兵 target 的 HTTP 层校验（`apiService.CreateTunnel` 与 `API.createTunnel` 的必填分支）连同 `/api/v1/routes` 的端到端断言一起放在 Task 12，避免同一个文件被两个任务反复改写。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/storage/ -run ProxyBasic -count=1; go test ./internal/server/ -run ProxyBasic -count=1`
Expected: FAIL，`undefined: CredentialTypeProxyBasic`、`service.ProxyBasicSecret undefined`。

- [ ] **Step 3: 最小实现**

`internal/storage/models.go`：

```go
// CredentialTypeProxyBasic stores an HTTP proxy username/password pair. The
// username lives in PublicKey so list views can show it; the password only
// exists inside the encrypted secret blob.
const CredentialTypeProxyBasic CredentialType = "proxy_basic"
```

`internal/storage/credential_repository.go` 的 `validateCredential` switch 增加：

```go
case CredentialTypeProxyBasic:
    if strings.TrimSpace(credential.PublicKey) == "" || strings.TrimSpace(credential.Fingerprint) == "" {
        return errors.New("credential username and fingerprint are required")
    }
    if credential.SecretCiphertext == "" {
        return errors.New("proxy credential requires an encrypted password")
    }
```

`internal/server/credential_service.go`：`CredentialInput` 加 `Username string`；`validateCredentialShape` 的 switch 增加 `case storage.CredentialTypeProxyBasic`（要求 `PublicKey` 非空、`secret.Password` 非空或已有 stored secret）；`Create` 中对 `proxy_basic` 设置 `credential.PublicKey = strings.TrimSpace(input.Username)` 与 `credential.Fingerprint = hex(sha256(username))`；`Update` 中禁止把 `proxy_basic` 的 `PublicKey` 清空。新增：

```go
// ProxyBasicSecret decrypts one proxy credential. It is the only path that
// turns the stored blob back into a password, and it never logs the value.
func (s *CredentialService) ProxyBasicSecret(ctx context.Context, actor auth.Principal, id string) (string, string, error) {
    credential, err := s.Get(ctx, actor, id)
    if err != nil {
        return "", "", err
    }
    if credential.Type != storage.CredentialTypeProxyBasic || !credential.Enabled || credential.DeletedAt != nil {
        return "", "", ErrCredentialInvalid
    }
    blob, err := s.decryptSecret(credential)
    if err != nil {
        return "", "", err
    }
    return credential.PublicKey, blob.Password, nil
}
```

`decryptSecret` 若尚不存在，则从既有 WebSSH 密码读取路径抽出同一函数（`auth.SecretStore.Decrypt` + `json.Unmarshal` 到 `credentialSecretBlob`），并在 secret store 为 nil 或解密失败时返回可被 `errors.Is(err, ErrCredentialSecretUnavailable)` 判定的错误。

本任务不改 `internal/server/api.go` 的路由校验分支。`internal/server/credential_api.go` 中：`credentialRequest` 增加 `Username string \`json:"username"\`` 并透传到 `CredentialInput.Username`；`credentialResponse` 增加 `Username string \`json:"username,omitempty"\``，取值来自 `credential.PublicKey`（前端不必理解 username 复用 PublicKey 列这一实现细节）；`Secret` 仍走既有 `credentialSecretRequest.Password`，响应永不回显。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/storage/ ./internal/server/ -count=1 && go vet ./internal/storage/ ./internal/server/`
Expected: PASS。

- [ ] **Step 5: Commit（需授权）**

```bash
git add internal/storage/models.go internal/storage/credential_repository.go internal/storage/credential_repository_test.go internal/server/credential_service.go internal/server/credential_service_test.go internal/server/credential_api.go
git commit -m "feat(credentials): add proxy basic credential type"
```

---

### Task 9: 内部监听与可信 peer 前置校验

**Files:**

- Create: `internal/server/proxy_entry_listener.go`
- Create: `internal/server/proxy_entry_listener_test.go`
- Modify: `internal/server/runtime.go`（`ServerRuntime` 增加 `proxyEntry *ProxyEntryListener` 字段、构造与 `ServeListener` 中的启动/停止）
- Modify: `internal/cli/root.go`（启动日志追加一行 proxy entry 状态）

**Interfaces:**

- Consumes: Task 1 的 `config.ProxyEntryConfig`、Task 10 的 `ProxyEntry.Handler()`（本任务先用一个占位 `http.HandlerFunc` 注入，Task 10 换成真实 handler）
- Produces:

```go
type ProxyEntryListener struct{ /* unexported */ }

func NewProxyEntryListener(cfg config.ProxyEntryConfig, handler http.Handler, metrics *observability.Metrics) (*ProxyEntryListener, error)
func (l *ProxyEntryListener) Serve(ctx context.Context) error   // 阻塞直到 ctx 取消或监听失败
func (l *ProxyEntryListener) Addr() net.Addr                    // 未开始监听时返回 nil
```

- [ ] **Step 1: 写失败测试**

```go
package server

import (
    "context"
    "io"
    "net"
    "net/http"
    "testing"
    "time"

    "github.com/tunnelmesh/tunnelmesh/internal/config"
)

func proxyEntryTestConfig(trusted []string) config.ProxyEntryConfig {
    cfg := config.ProxyEntryConfig{
        Enabled: true, Listen: "127.0.0.1:0", TrustedProxies: trusted,
        RouteHeader: "X-TunnelMesh-Route", ClientIPHeader: "X-TunnelMesh-Client-IP",
        ClientPortHeader: "X-TunnelMesh-Client-Port",
        ConnectTimeout: time.Second, IdleTimeout: 2 * time.Second, ShutdownTimeout: time.Second,
        MaxConcurrentTunnels: 2, MaxHeaderBytes: 16384, AuthBackoffThreshold: 5,
    }
    return cfg
}

func startProxyEntryListener(t *testing.T, cfg config.ProxyEntryConfig, handler http.Handler) *ProxyEntryListener {
    t.Helper()
    listener, err := NewProxyEntryListener(cfg, handler, nil)
    if err != nil {
        t.Fatal(err)
    }
    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan error, 1)
    go func() { done <- listener.Serve(ctx) }()
    deadline := time.Now().Add(2 * time.Second)
    for listener.Addr() == nil && time.Now().Before(deadline) {
        time.Sleep(5 * time.Millisecond)
    }
    if listener.Addr() == nil {
        t.Fatal("listener did not bind")
    }
    t.Cleanup(func() { cancel(); <-done })
    return listener
}

func TestProxyEntryListenerServesTrustedPeer(t *testing.T) {
    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("X-Echo-Route", r.Header.Get("X-TunnelMesh-Route"))
        w.WriteHeader(http.StatusOK)
        _, _ = io.WriteString(w, "ok")
    })
    listener := startProxyEntryListener(t, proxyEntryTestConfig([]string{"127.0.0.1/32"}), handler)
    conn, err := net.Dial("tcp", listener.Addr().String())
    if err != nil {
        t.Fatal(err)
    }
    defer conn.Close()
    _, _ = conn.Write([]byte("GET http://target.example/ HTTP/1.1\r\nHost: target.example\r\nX-TunnelMesh-Route: tp-demo.tm.example.com\r\n\r\n"))
    _ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
    body, err := io.ReadAll(conn)
    if err != nil {
        t.Fatalf("read response: %v", err)
    }
    if !bytesContains(body, "200 OK") || !bytesContains(body, "X-Echo-Route: tp-demo.tm.example.com") {
        t.Fatalf("response = %q", body)
    }
}

func TestProxyEntryListenerClosesUntrustedPeerBeforeReading(t *testing.T) {
    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Error("handler must not run for an untrusted peer")
    })
    listener := startProxyEntryListener(t, proxyEntryTestConfig([]string{"10.9.9.9/32"}), handler)
    conn, err := net.Dial("tcp", listener.Addr().String())
    if err != nil {
        t.Fatal(err)
    }
    defer conn.Close()
    _, _ = conn.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"))
    _ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
    body, err := io.ReadAll(conn)
    if err != nil && err != io.EOF {
        t.Fatalf("unexpected read error: %v", err)
    }
    if len(body) != 0 {
        t.Fatalf("untrusted peer received %q", body)
    }
}

func TestProxyEntryListenerStopsOnContextCancel(t *testing.T) {
    cfg := proxyEntryTestConfig([]string{"127.0.0.1/32"})
    listener, err := NewProxyEntryListener(cfg, http.NotFoundHandler(), nil)
    if err != nil {
        t.Fatal(err)
    }
    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan error, 1)
    go func() { done <- listener.Serve(ctx) }()
    for listener.Addr() == nil {
        time.Sleep(5 * time.Millisecond)
    }
    addr := listener.Addr().String()
    cancel()
    select {
    case err := <-done:
        if err != nil {
            t.Fatalf("Serve returned %v", err)
        }
    case <-time.After(3 * time.Second):
        t.Fatal("Serve did not return after cancel")
    }
    if conn, err := net.Dial("tcp", addr); err == nil {
        _ = conn.Close()
        t.Fatal("listener still accepts after shutdown")
    }
}

func bytesContains(haystack []byte, needle string) bool {
    return len(haystack) >= len(needle) && string(haystack) != "" && contains(string(haystack), needle)
}

func contains(haystack, needle string) bool {
    for i := 0; i+len(needle) <= len(haystack); i++ {
        if haystack[i:i+len(needle)] == needle {
            return true
        }
    }
    return false
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/server/ -run ProxyEntryListener -count=1`
Expected: FAIL，`undefined: NewProxyEntryListener`。

- [ ] **Step 3: 最小实现**

```go
package server

// trustedPeerListener drops connections from hosts outside the configured
// allowlist before any request bytes are parsed. The internal proxy listener
// carries forged-able route and client-IP headers, so this check is the only
// thing standing between the LAN and the policy engine.
type trustedPeerListener struct {
    inner   net.Listener
    trusted []*net.IPNet
    onDeny  func(remote net.Addr)
}

func (l *trustedPeerListener) Accept() (net.Conn, error) {
    for {
        conn, err := l.inner.Accept()
        if err != nil {
            return nil, err
        }
        if peerAllowed(conn.RemoteAddr(), l.trusted) {
            return conn, nil
        }
        l.onDeny(conn.RemoteAddr())
        _ = conn.Close()
    }
}

func (l *trustedPeerListener) Addr() net.Addr { return l.inner.Addr() }
func (l *trustedPeerListener) Close() error   { return l.inner.Close() }

func peerAllowed(remote net.Addr, trusted []*net.IPNet) bool {
    host, _, err := net.SplitHostPort(remote.String())
    if err != nil {
        host = remote.String()
    }
    ip := net.ParseIP(host)
    if ip == nil {
        return false
    }
    for _, network := range trusted {
        if network.Contains(ip) {
            return true
        }
    }
    return false
}
```

`NewProxyEntryListener` 解析 `cfg.TrustedProxies`（`net.ParseCIDR`，任一非法返回 error），构造 `http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: cfg.MaxHeaderBytes}`。`Serve` 按 Interfaces 描述启动、在 `ctx.Done()` 时先 `Shutdown(ShutdownTimeout)` 再 `Close()`，`http.ErrServerClosed` 归一化为 nil；`onDeny` 记 `slog.WarnContext(ctx, "proxy_entry_untrusted_peer", "remote", addr)` 并在 metrics 非 nil 时调用 `ObserveProxyEntryRequest("", "connect", "untrusted_peer", "")`（Task 10 提供该方法）。`Addr()` 用 `atomic.Pointer[net.Addr]` 或 mutex 保护，未监听时返回 nil。

`internal/server/runtime.go`：`ServerRuntime` 增加 `proxyEntry *ProxyEntryListener` 字段；构造处仅当 `r.config.Server.ProxyEntry.Enabled` 时 `NewProxyEntryListener(cfg, entry.Handler(), r.metrics)`；在 `ServeListener` 中按 relay listener 的同一模式启动 goroutine，并把它的错误汇入既有 `errCh` 选择逻辑，shutdown 时一并取消。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/server/ -run ProxyEntryListener -count=1 && go test -race ./internal/server/ -run ProxyEntryListener -count=1`
Expected: PASS。

- [ ] **Step 5: Commit（需授权）**

```bash
git add internal/server/proxy_entry_listener.go internal/server/proxy_entry_listener_test.go internal/server/runtime.go internal/cli/root.go
git commit -m "feat(server): add trusted proxy entry listener"
```

---

### Task 10: CONNECT 隧道处理、限额、指标与审计

**Files:**

- Create: `internal/server/proxy_entry.go`
- Create: `internal/server/proxy_entry_test.go`
- Modify: `internal/observability/metrics.go`（5 个 collector + 5 个访问器 + 注册）
- Create: `internal/observability/metrics_proxy_entry_test.go`

**Interfaces:**

- Consumes: `proxyentry.RouteSource`（Task 7 的 `ManagedRouteTable`）、`proxyentry.RouteIdentity`（Task 3）、`proxyentry.SourceACL`（Task 4）、`proxyentry.Authenticator`（Task 5）、`proxyentry.TargetPolicy`（Task 6）、`relay.NodeTransport.OpenStream(context.Context, relay.StreamRequest) (io.ReadWriteCloser, error)`、`storage.AuditRepository.Create(context.Context, storage.AuditLog) error`
- Produces:

```go
type ProxyEntry struct{ /* unexported */ }

func NewProxyEntry(cfg config.ProxyEntryConfig, routes proxyentry.RouteSource, opener relay.NodeTransport, secrets proxyentry.SecretResolver, metrics *observability.Metrics, audits storage.AuditRepository) *ProxyEntry
func (p *ProxyEntry) Handler() http.Handler
func (p *ProxyEntry) ServeHTTP(w http.ResponseWriter, r *http.Request)
func (p *ProxyEntry) ActiveTunnels() int

// observability 新增访问器
func (m *Metrics) ObserveProxyEntryRequest(route, mode, result, errorClass string)
func (m *Metrics) ObserveProxyEntryTunnel(route string, active bool)
func (m *Metrics) ObserveProxyEntryTunnelDuration(route, result string, duration time.Duration)
func (m *Metrics) ObserveProxyEntryAuthFailure(route, reason string)
func (m *Metrics) ObserveProxyEntryACLDenied(route string)
```

- [ ] **Step 1: 写指标失败测试**

```go
package observability

import (
    "testing"
    "time"

    "github.com/prometheus/client_golang/prometheus"
)

func TestProxyEntryMetricsAreRegistered(t *testing.T) {
    reg := prometheus.NewRegistry()
    metrics := NewMetrics(reg)
    metrics.ObserveProxyEntryRequest("tp-demo", "connect", "success", "")
    metrics.ObserveProxyEntryTunnel("tp-demo", true)
    metrics.ObserveProxyEntryTunnel("tp-demo", false)
    metrics.ObserveProxyEntryTunnelDuration("tp-demo", "success", 1500*time.Millisecond)
    metrics.ObserveProxyEntryAuthFailure("tp-demo", "bad_password")
    metrics.ObserveProxyEntryACLDenied("tp-demo")

    families, err := reg.Gather()
    if err != nil {
        t.Fatal(err)
    }
    want := map[string]bool{
        "tunnelmesh_proxy_entry_requests_total":         false,
        "tunnelmesh_proxy_entry_tunnels_active":         false,
        "tunnelmesh_proxy_entry_tunnel_duration_seconds": false,
        "tunnelmesh_proxy_entry_auth_failures_total":    false,
        "tunnelmesh_proxy_entry_acl_denied_total":       false,
    }
    for _, family := range families {
        if _, ok := want[family.GetName()]; ok {
            want[family.GetName()] = true
        }
    }
    for name, seen := range want {
        if !seen {
            t.Fatalf("metric %s was not registered", name)
        }
    }
}
```

- [ ] **Step 2: 运行指标测试确认失败**

Run: `go test ./internal/observability/ -run ProxyEntry -count=1`
Expected: FAIL，`metrics.ObserveProxyEntryRequest undefined`。

- [ ] **Step 3: 实现指标**

在 `internal/observability/metrics.go` 中按既有 webssh 指标的同一三处写法添加：`Metrics` 结构字段 `proxyEntryRequests *prometheus.CounterVec`、`proxyEntryTunnels *prometheus.GaugeVec`、`proxyEntryTunnelDuration *prometheus.HistogramVec`、`proxyEntryAuthFailures *prometheus.CounterVec`、`proxyEntryACLDenied *prometheus.CounterVec`；`NewMetrics` 中用 `prometheus.NewCounterVec/GaugeVec/HistogramVec` 构造（label 分别为 `{route,mode,result,error_class}`、`{route}`、`{route,result}`、`{route,reason}`、`{route}`），并把 5 个 collector 追加到 `reg.MustRegister(...)` 参数列表末尾；访问器全部经既有 `label(...)` 归一化空值，`ObserveProxyEntryTunnel(route, active)` 按 `active` 做 `Inc`/`Dec`。

- [ ] **Step 4: 运行指标测试确认通过**

Run: `go test ./internal/observability/ -count=1`
Expected: PASS。

- [ ] **Step 5: 写 CONNECT 失败测试**

```go
package server

import (
    "bufio"
    "context"
    "encoding/base64"
    "errors"
    "fmt"
    "io"
    "net"
    "net/http"
    "net/http/httptest"
    "os"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/tunnelmesh/tunnelmesh/internal/config"
    "github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
    "github.com/tunnelmesh/tunnelmesh/internal/relay"
)

type stubProxyRoutes map[string]proxyentry.Route

func (s stubProxyRoutes) ProxyRoute(_ context.Context, domain string) (proxyentry.Route, bool) {
    route, ok := s[domain]
    return route, ok
}

type stubProxySecrets struct{ username, password string }

func (s stubProxySecrets) ProxyBasicSecret(context.Context, string) (proxyentry.CredentialSecret, error) {
    return proxyentry.CredentialSecret{Username: s.username, Password: s.password}, nil
}

// stubEgress echoes everything the client sends, standing in for an Agent
// connection to the real target.
type stubEgress struct {
    delay    time.Duration
    err      error
    requests []relay.StreamRequest
}

func (s *stubEgress) OpenStream(ctx context.Context, req relay.StreamRequest) (io.ReadWriteCloser, error) {
    s.requests = append(s.requests, req)
    if s.err != nil {
        return nil, s.err
    }
    if s.delay > 0 {
        select {
        case <-ctx.Done():
            return nil, ctx.Err()
        case <-time.After(s.delay):
        }
    }
    client, upstream := net.Pipe()
    go func() {
        scanner := bufio.NewReader(upstream)
        for {
            line, err := scanner.ReadString('\n')
            if err != nil {
                return
            }
            if _, err := io.WriteString(client, "echo:"+line); err != nil {
                return
            }
        }
    }()
    return client, nil
}

func (s *stubEgress) Close() error { return nil }

// proxyEntryServer exposes the httptest listener address so tests can dial it
// and hand-write proxy requests (the Go HTTP client will not emit a CONNECT
// carrying our trusted-peer headers).
type proxyEntryServer struct{ addr string }

func (s proxyEntryServer) Addr() string { return s.addr }

// egress is typed as relay.NodeTransport so later tasks can substitute a
// scripted or blocking transport without touching the call sites.
func newProxyEntryFixture(t *testing.T, routes stubProxyRoutes, egress relay.NodeTransport, cfg config.ProxyEntryConfig) (proxyEntryServer, *ProxyEntry) {
    t.Helper()
    entry := NewProxyEntry(cfg, routes, egress, stubProxySecrets{username: "demo", password: "s3cret"}, nil, nil)
    srv := httptest.NewServer(entry.Handler())
    t.Cleanup(srv.Close)
    return proxyEntryServer{addr: srv.Listener.Addr().String()}, entry
}

func dialProxyAndConnect(t *testing.T, addr, target, route, clientIP, proxyAuth string) (net.Conn, *bufio.ReadWriter) {
    t.Helper()
    conn, err := net.Dial("tcp", addr)
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = conn.Close() })
    var b strings.Builder
    fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
    fmt.Fprintf(&b, "X-TunnelMesh-Route: %s\r\nX-TunnelMesh-Client-IP: %s\r\nX-TunnelMesh-Client-Port: 41234\r\n", route, clientIP)
    if proxyAuth != "" {
        fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", base64.StdEncoding.EncodeToString([]byte(proxyAuth)))
    }
    b.WriteString("\r\n")
    if _, err := io.WriteString(conn, b.String()); err != nil {
        t.Fatal(err)
    }
    _ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
    return conn, bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
}

func readStatusLine(t *testing.T, rw *bufio.ReadWriter) (int, http.Header) {
    t.Helper()
    resp, err := http.ReadResponse(rw.Reader, nil)
    if err != nil {
        t.Fatalf("read proxy response: %v", err)
    }
    return resp.StatusCode, resp.Header
}

func TestProxyEntryConnectTunnelRoundTrip(t *testing.T) {
    routes := stubProxyRoutes{"tp-demo.tm.example.com": {
        ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
        AuthMode: proxyentry.AuthModeNone, SourceCIDRs: []string{"11.71.85.0/24"},
        AllowPrivateTargets: true, Status: proxyentry.StatusActive,
    }}
    egress := &stubEgress{}
    srv, entry := newProxyEntryFixture(t, routes, egress, proxyEntryTestConfig([]string{"127.0.0.1/32"}))
    conn, rw := dialProxyAndConnect(t, srv.Addr(), "93.184.216.34:443", "tp-demo.tm.example.com", "11.71.85.7", "")
    status, _ := readStatusLine(t, rw)
    if status != http.StatusOK {
        t.Fatalf("status = %d", status)
    }
    if _, err := conn.Write([]byte("ping\n")); err != nil {
        t.Fatal(err)
    }
    line, err := rw.ReadString('\n')
    if err != nil {
        t.Fatalf("read echo: %v", err)
    }
    if line != "echo:ping\n" {
        t.Fatalf("echo = %q", line)
    }
    if len(egress.requests) != 1 {
        t.Fatalf("egress requests = %#v", egress.requests)
    }
    req := egress.requests[0]
    if req.AgentID != "agent-1" || req.Protocol != "tcp" || req.TargetHost != "93.184.216.34" || req.TargetPort != 443 {
        t.Fatalf("stream request = %#v", req)
    }
    _ = entry
}

func TestProxyEntryDenials(t *testing.T) {
    basic := proxyentry.Route{
        ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
        AuthMode: proxyentry.AuthModeBasic, CredentialID: "c1",
        SourceCIDRs: []string{"11.71.85.0/24"}, AllowPrivateTargets: true, Status: proxyentry.StatusActive,
    }
    routes := stubProxyRoutes{"tp-demo.tm.example.com": basic}
    cases := []struct {
        name      string
        route     string
        clientIP  string
        auth      string
        target    string
        wantCode  int
        wantError string
    }{
        {"unknown route", "tp-nope.tm.example.com", "11.71.85.7", "", "93.184.216.34:443", http.StatusForbidden, "proxy_route_unavailable"},
        {"missing client ip", "tp-demo.tm.example.com", "", "", "93.184.216.34:443", http.StatusForbidden, "proxy_route_identity_invalid"},
        {"acl deny", "tp-demo.tm.example.com", "10.0.0.9", "", "93.184.216.34:443", http.StatusForbidden, "proxy_source_denied"},
        {"auth missing", "tp-demo.tm.example.com", "11.71.85.7", "", "93.184.216.34:443", http.StatusProxyAuthRequired, "proxy_auth_required"},
        {"auth wrong", "tp-demo.tm.example.com", "11.71.85.7", "demo:bad", "93.184.216.34:443", http.StatusProxyAuthRequired, "proxy_auth_failed"},
        {"metadata target", "tp-demo.tm.example.com", "11.71.85.7", "demo:s3cret", "169.254.169.254:80", http.StatusForbidden, "proxy_target_denied"},
        {"bad port", "tp-demo.tm.example.com", "11.71.85.7", "demo:s3cret", "93.184.216.34:0", http.StatusBadRequest, "proxy_target_invalid"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            srv, _ := newProxyEntryFixture(t, routes, &stubEgress{}, proxyEntryTestConfig([]string{"127.0.0.1/32"}))
            _, rw := dialProxyAndConnect(t, srv.Addr(), tc.target, tc.route, tc.clientIP, tc.auth)
            status, header := readStatusLine(t, rw)
            if status != tc.wantCode {
                t.Fatalf("status = %d want %d", status, tc.wantCode)
            }
            if status == http.StatusProxyAuthRequired && header.Get("Proxy-Authenticate") == "" {
                t.Fatal("407 must carry Proxy-Authenticate")
            }
            body, _ := io.ReadAll(rw.Reader)
            if !strings.Contains(string(body), tc.wantError) {
                t.Fatalf("body = %q want code %q", body, tc.wantError)
            }
        })
    }
}

func TestProxyEntryEgressErrors(t *testing.T) {
    routes := stubProxyRoutes{"tp-demo.tm.example.com": {
        ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
        AuthMode: proxyentry.AuthModeNone, AllowPrivateTargets: true, Status: proxyentry.StatusActive,
    }}
    cfg := proxyEntryTestConfig([]string{"127.0.0.1/32"})

    failing := &stubEgress{err: fmt.Errorf("agent offline")}
    srv, _ := newProxyEntryFixture(t, routes, failing, cfg)
    _, rw := dialProxyAndConnect(t, srv.Addr(), "93.184.216.34:443", "tp-demo.tm.example.com", "10.0.0.1", "")
    status, _ := readStatusLine(t, rw)
    body, _ := io.ReadAll(rw.Reader)
    if status != http.StatusBadGateway || !strings.Contains(string(body), "proxy_egress_unavailable") {
        t.Fatalf("egress failure = %d %q", status, body)
    }

    cfg.ConnectTimeout = 50 * time.Millisecond
    slow := &stubEgress{delay: time.Second}
    slowSrv, _ := newProxyEntryFixture(t, routes, slow, cfg)
    _, slowRW := dialProxyAndConnect(t, slowSrv.Addr(), "93.184.216.34:443", "tp-demo.tm.example.com", "10.0.0.1", "")
    status, _ = readStatusLine(t, slowRW)
    body, _ = io.ReadAll(slowRW.Reader)
    if status != http.StatusGatewayTimeout || !strings.Contains(string(body), "proxy_egress_timeout") {
        t.Fatalf("timeout = %d %q", status, body)
    }
}

// blockingEgress keeps every opened stream alive until the test closes it, so
// the per-route concurrency limit can be exercised deterministically.
type blockingEgress struct {
    opened chan struct{}

    mu    sync.Mutex
    conns []io.ReadWriteCloser
}

func (b *blockingEgress) OpenStream(context.Context, relay.StreamRequest) (io.ReadWriteCloser, error) {
    client, upstream := net.Pipe()
    b.mu.Lock()
    b.conns = append(b.conns, client, upstream)
    b.mu.Unlock()
    select {
    case b.opened <- struct{}{}:
    default:
    }
    return client, nil
}

func (b *blockingEgress) Close() error { return nil }

func TestProxyEntryCapacityLimit(t *testing.T) {
    routes := stubProxyRoutes{"tp-demo.tm.example.com": {
        ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
        AuthMode: proxyentry.AuthModeNone, AllowPrivateTargets: true,
        MaxConcurrentTunnels: 1, Status: proxyentry.StatusActive,
    }}
    blocking := &blockingEgress{opened: make(chan struct{}, 4)}
    srv, entry := newProxyEntryFixture(t, routes, blocking, proxyEntryTestConfig([]string{"127.0.0.1/32"}))

    first, firstRW := dialProxyAndConnect(t, srv.Addr(), "93.184.216.34:443", "tp-demo.tm.example.com", "10.0.0.1", "")
    if status, _ := readStatusLine(t, firstRW); status != http.StatusOK {
        t.Fatalf("first tunnel status = %d", status)
    }
    <-blocking.opened
    if got := entry.ActiveTunnels(); got != 1 {
        t.Fatalf("ActiveTunnels while connected = %d", got)
    }

    _, secondRW := dialProxyAndConnect(t, srv.Addr(), "93.184.216.34:443", "tp-demo.tm.example.com", "10.0.0.2", "")
    status, header := readStatusLine(t, secondRW)
    if status != http.StatusServiceUnavailable || header.Get("Retry-After") != "5" {
        t.Fatalf("second tunnel = %d Retry-After=%q", status, header.Get("Retry-After"))
    }
    body, _ := io.ReadAll(secondRW.Reader)
    if !strings.Contains(string(body), "proxy_capacity_exhausted") {
        t.Fatalf("body = %q", body)
    }

    // Ending the first tunnel must release its slot again.
    _ = first.Close()
    deadline := time.Now().Add(3 * time.Second)
    for entry.ActiveTunnels() != 0 && time.Now().Before(deadline) {
        time.Sleep(10 * time.Millisecond)
    }
    if got := entry.ActiveTunnels(); got != 0 {
        t.Fatalf("slot not released, ActiveTunnels = %d", got)
    }
}

func TestClassifyTunnelResult(t *testing.T) {
    dnsTimeout := &net.DNSError{IsTimeout: true}
    cases := []struct {
        name      string
        idleFired bool
        errs      []error
        want      string
    }{
        {"both directions clean", false, []error{nil, io.EOF}, "success"},
        {"shutdown artifacts are clean", false, []error{net.ErrClosed, os.ErrDeadlineExceeded}, "success"},
        {"closed pipe is clean", false, []error{io.ErrClosedPipe, nil}, "success"},
        {"deadline error is clean", false, []error{dnsTimeout, nil}, "success"},
        {"transport fault is an error", false, []error{nil, errors.New("relay reset by peer")}, "error"},
        {"idle watchdog wins", true, []error{errors.New("relay reset by peer")}, "timeout"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := classifyTunnelResult(tc.idleFired, tc.errs...); got != tc.want {
                t.Fatalf("classifyTunnelResult(%v, %v) = %q, want %q", tc.idleFired, tc.errs, got, tc.want)
            }
        })
    }
}
```

- [ ] **Step 6: 运行 CONNECT 测试确认失败**

Run: `go test ./internal/server/ -run 'TestProxyEntry' -count=1`
Expected: FAIL，`undefined: NewProxyEntry`。

- [ ] **Step 7: 最小实现**

本任务只交付 CONNECT 分支。`handleAbsoluteForm` 先以编译占位存在，Task 11 在同一 PR 内立即替换为真实实现并补测试：

```go
// handleAbsoluteForm is a compile-time placeholder in Task 10; Task 11 replaces
// the body. The message never reaches production because both tasks land in the
// same release.
func (p *ProxyEntry) handleAbsoluteForm(w http.ResponseWriter, r *http.Request, route proxyentry.Route, clientIP net.IP, mode string, started time.Time) {
    p.deny(w, r, route.Domain, mode, started, proxyentry.NewError(http.StatusNotImplemented, "proxy_absolute_form_unimplemented", "absolute-form forwarding is not implemented yet"), "absolute_unimplemented")
}
```

`internal/server/proxy_entry.go` 的 `ServeHTTP` 决策链（顺序不可调整，任何一步失败都必须先记指标再写响应）：

```go
func (p *ProxyEntry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    started := time.Now()
    mode := "absolute"
    if r.Method == http.MethodConnect {
        mode = "connect"
    }
    clientIP, ipErr := proxyentry.ParseClientIP(r.Header.Get(p.config.ClientIPHeader))
    key, routeErr := p.identity.Resolve(r)
    if ipErr != nil || routeErr != nil {
        p.deny(w, r, "", mode, started, proxyentry.ErrRouteIdentityInvalid, "identity")
        return
    }
    routeName := string(key)
    route, ok := p.routes.ProxyRoute(r.Context(), routeName)
    if !ok || !route.Active() {
        p.deny(w, r, routeName, mode, started, proxyentry.ErrRouteUnavailable, "route_unavailable")
        return
    }
    acl, err := proxyentry.NewSourceACL(route.SourceCIDRs)
    if err != nil {
        acl = proxyentry.DenyAllSourceACL()
    }
    if !acl.Allow(clientIP) {
        p.metricsACLDenied(route)
        p.audit(r.Context(), "proxy_route_denied", route, clientIP, "", 0, "source_acl")
        p.deny(w, r, routeName, mode, started, proxyentry.ErrSourceDenied, "source_acl")
        return
    }
    if err := p.authenticator.Authorize(r.Context(), route, clientIP, r.Header.Get("Proxy-Authorization")); err != nil {
        p.metricsAuthFailure(route, err)
        p.audit(r.Context(), "proxy_auth_failed", route, clientIP, "", 0, errorCode(err))
        p.deny(w, r, routeName, mode, started, err, "auth")
        return
    }
    release, err := p.reserve(route)
    if err != nil {
        p.deny(w, r, routeName, mode, started, err, "capacity")
        return
    }
    defer release()
    if r.Method == http.MethodConnect {
        p.handleConnect(w, r, route, clientIP, mode, started)
        return
    }
    p.handleAbsoluteForm(w, r, route, clientIP, mode, started)
}
```

`deny` 写 `{"code":<status>,"msg":"<固定文案>","data":null}`（复用既有 `writeAPIError` 的信封格式，但错误码取自 `proxyentry.Error.Code`），设置 `Error.HTTPHeaders()` 中的每个头，`Cache-Control: no-store`，并调用 `ObserveProxyEntryRequest(route, mode, "denied", errorClass)`。

`handleConnect`：

1. `host, port, err := net.SplitHostPort(r.Host)`；`err` 时回退 `host = r.Host; port = 443`；`port` 非法 → `ErrTargetInvalid`。
2. `policy, err := proxyentry.NewTargetPolicy(route.TargetCIDRs, route.TargetPorts, route.AllowPrivateTargets)`；`err` → `ErrTargetDenied`；`policy.Validate(host, port)` 失败按错误类型回 400/403。
3. `ctx, cancel := context.WithTimeout(r.Context(), p.config.ConnectTimeout)`；`stream, err := p.opener.OpenStream(ctx, relay.StreamRequest{AgentID: route.AgentID, Protocol: "tcp", TargetHost: host, TargetPort: port, Metadata: []byte(traceparent)})`；`errors.Is(err, context.DeadlineExceeded)` → `ErrEgressTimeout`，其它 → `ErrEgressUnavailable`。
4. `hj, ok := w.(http.Hijacker)`；不 ok 时 `stream.Close()` 并回 500。
5. `conn, buf, err := hj.Hijack()`；写 `HTTP/1.1 200 Connection Established\r\n\r\n`；若 `buf.Reader.Buffered() > 0` 先 `io.CopyN(stream, buf.Reader, int64(buffered))`（与 `internal/server/http_proxy.go` 既有做法一致）。
6. `p.audit(ctx, "proxy_tunnel_opened", route, clientIP, host+":"+port, 0, "")`，`ObserveProxyEntryTunnel(route.Domain, true)`，`ActiveTunnels` 计数 +1。
7. `fromClient, fromUpstream, reason := spliceWithIdleTimeout(conn, stream, p.config.IdleTimeout)`；结束后 `ObserveProxyEntryTunnel(route.Domain, false)`、`ObserveProxyEntryTunnelDuration(route.Domain, reason, elapsed)`、`ObserveBytes("proxy_entry", "upstream"/"downstream", "tcp", n)`、`p.audit(ctx, "proxy_tunnel_closed", ...)`（Details 的 `reason` 用同一个值，另含 bytesUp/bytesDown/durationMs）。`reason` 只有三个取值：`success`（两个方向都干净结束）、`timeout`（idle 看门狗触发）、`error`（任一方向的 copy 返回非干净关闭的错误），由 `classifyTunnelResult` 计算；Task 15 的 Grafana 面板按 `result` 分组出图，因此这三个取值就是该标签的完整枚举。

```go
// spliceWithIdleTimeout copies both directions and closes the pair once neither
// side has produced bytes for the whole timeout. The mesh stream ignores
// SetDeadline, so the watchdog owns idle detection.
//
// reason is one of "success", "timeout" and "error". It labels
// tunnelmesh_proxy_entry_tunnel_duration_seconds and the proxy_tunnel_closed
// audit event, so an agent dropping mid-tunnel stays visible instead of being
// averaged into one success bucket.
func spliceWithIdleTimeout(down net.Conn, up io.ReadWriteCloser, idle time.Duration) (fromClient, fromUpstream int64, reason string) {
    var last atomic.Int64
    last.Store(time.Now().UnixNano())
    var idleFired atomic.Bool
    done := make(chan struct{})
    var once sync.Once
    closeAll := func() {
        once.Do(func() {
            _ = down.SetDeadline(time.Now())
            _ = up.Close()
            close(done)
        })
    }

    // 字节数经带缓冲的 channel 交回主 goroutine，不写命名返回值：closeAll 只保证
    // 有一侧先结束，另一侧此刻可能仍在 io.Copy 里，主 goroutine 直接读命名返回值
    // 会被 go test -race 判成数据竞争。容量为 2，两个方向的发送都不会阻塞。
    type copyResult struct {
        toUpstream bool
        n          int64
        err        error
    }
    results := make(chan copyResult, 2)

    go func() {
        n, err := io.Copy(up, &activityReader{r: down, last: &last})
        results <- copyResult{toUpstream: true, n: n, err: err}
        closeAll()
    }()
    go func() {
        n, err := io.Copy(down, &activityReader{r: up, last: &last})
        results <- copyResult{toUpstream: false, n: n, err: err}
        closeAll()
    }()
    go func() {
        ticker := time.NewTicker(min(idle/4, 15*time.Second))
        defer ticker.Stop()
        for {
            select {
            case <-done:
                return
            case <-ticker.C:
                if time.Since(time.Unix(0, last.Load())) >= idle {
                    idleFired.Store(true)
                    closeAll()
                    return
                }
            }
        }
    }()

    // 两个方向都收完再返回。closeAll 已经让 down 的 deadline 过期、up 关闭，
    // 两次 copy 都会很快返回，因此这里不会把调用方挂住。
    errs := make([]error, 0, 2)
    for i := 0; i < 2; i++ {
        res := <-results
        if res.toUpstream {
            fromClient = res.n
        } else {
            fromUpstream = res.n
        }
        errs = append(errs, res.err)
    }
    return fromClient, fromUpstream, classifyTunnelResult(idleFired.Load(), errs...)
}

// classifyTunnelResult maps the idle watchdog flag and the two copy errors onto
// the low-cardinality result label. Anything that is the expected consequence of
// closeAll counts as a clean shutdown; only a real transport fault is an error.
// The watchdog wins because it already explains both copy failures.
func classifyTunnelResult(idleFired bool, errs ...error) string {
    if idleFired {
        return "timeout"
    }
    for _, err := range errs {
        switch {
        case err == nil,
            errors.Is(err, io.EOF),
            errors.Is(err, io.ErrClosedPipe),
            errors.Is(err, net.ErrClosed),
            errors.Is(err, os.ErrDeadlineExceeded):
        default:
            var netErr net.Error
            if errors.As(err, &netErr) && netErr.Timeout() {
                continue
            }
            return "error"
        }
    }
    return "success"
}
```

`activityReader` 在每次成功 `Read` 后 `last.Store(time.Now().UnixNano())`；`reserve(route)` 用互斥锁维护 `activeTotal` 与 `activeByRoute[route.ID]`，超过 `config.MaxConcurrentTunnels`（0 表示不限）或 `route.MaxConcurrentTunnels` 时返回 `ErrCapacityExhausted`，返回的 `release` 幂等。`audit` 通过 `p.audits.Create(ctx, storage.AuditLog{Action: action, ResourceType: "proxy_route", ResourceID: route.ID, Details: <JSON>})` 写入，Details 含 `routeDomain`、`agentId`、`clientIp`、`target`、`bytesUp`、`bytesDown`、`durationMs`、`reason`；写失败只记 `slog.WarnContext`，不得影响隧道。

`internal/server/proxy_entry.go` 的 import 需要 `errors`、`os`、`sync/atomic`（`classifyTunnelResult` 用前两个，`spliceWithIdleTimeout` 用第三个）；`internal/server/proxy_entry_test.go` 的 import 已在 Step 5 一并给出。管理侧的 `proxy_route_created|updated|deleted` 审计不新增事件名：既有 `auditRoute` 已经为所有 `tunnels` 行写 `route.created` / `route.updated` / `route.deleted`，Task 12 直接复用，spec 第 13 节的三个名字按此对齐（Task 15 Step 15 回写）。

- [ ] **Step 8: 运行测试确认通过**

Run: `go test ./internal/server/ ./internal/observability/ -count=1 && go test -race ./internal/server/ -run 'TestProxyEntry|TestClassifyTunnelResult' -count=1 && go vet ./internal/server/ ./internal/observability/`
Expected: PASS。

- [ ] **Step 9: Commit（需授权）**

```bash
git add internal/server/proxy_entry.go internal/server/proxy_entry_test.go internal/observability/metrics.go internal/observability/metrics_proxy_entry_test.go
git commit -m "feat(server): tunnel connect proxy entries"
```

---

### Task 11: 绝对形式（非 CONNECT）HTTP 转发

**Files:**

- Modify: `internal/server/proxy_entry.go`（替换 Task 10 的 `handleAbsoluteForm` 占位实现）
- Create: `internal/server/proxy_entry_absolute_test.go`

**Interfaces:**

- Consumes: `internal/server/http_proxy.go` 的 `copyResponse(w http.ResponseWriter, resp *http.Response)`（已核实签名）、Task 10 的 `ProxyEntry`/`deny`/`reserve`/`metricsRequest`/`audit`、`proxyentry.NewTargetPolicy`、`relay.NodeTransport.OpenStream`
- Produces:

```go
// handleAbsoluteForm forwards a non-CONNECT proxy request over one logical
// stream. Field-for-field it mirrors HTTPProxyHandler.ServeRoute: write the
// normalized request into the stream, read one response back, stream it out.
func (p *ProxyEntry) handleAbsoluteForm(w http.ResponseWriter, r *http.Request, route proxyentry.Route, clientIP net.IP, mode string, started time.Time)

// normalizeProxyEntryRequest rewrites an absolute-form proxy request into the
// origin-form the target expects and strips hop-by-hop plus trusted-peer
// headers. Mirrors internal/client.normalizeProxyRequest and additionally
// removes the X-TunnelMesh-* headers so route metadata never reaches a target.
func normalizeProxyEntryRequest(r *http.Request, host string, port int) *http.Request

// splitProxyTarget resolves the real target of a non-CONNECT proxy request.
// It accepts both request shapes the entry can see: an absolute-form URL
// (direct hit on the internal listener, used by tests and curl) and the
// origin-form rewrite OpenResty's "location /" produces, where the authority
// survives only in the Host header.
func splitProxyTarget(r *http.Request) (host string, port int, scheme string, err error)
```

- [ ] **Step 1: 写失败测试**

```go
package server

import (
    "bufio"
    "context"
    "fmt"
    "io"
    "net"
    "net/http"
    "net/http/httptest"
    "net/url"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
    "github.com/tunnelmesh/tunnelmesh/internal/relay"
)

// scriptedEgress records the exact request bytes the entry forwarded and
// answers with a canned response, so origin-form rewriting and header stripping
// can be asserted without dialing a real target.
type scriptedEgress struct {
    response string
    err      error

    mu       sync.Mutex
    got      string
    requests []relay.StreamRequest
}

func (s *scriptedEgress) OpenStream(_ context.Context, req relay.StreamRequest) (io.ReadWriteCloser, error) {
    s.mu.Lock()
    s.requests = append(s.requests, req)
    err := s.err
    s.mu.Unlock()
    if err != nil {
        return nil, err
    }
    client, upstream := net.Pipe()
    go func() {
        defer func() { _ = upstream.Close() }()
        reader := bufio.NewReader(upstream)
        var b strings.Builder
        for {
            line, readErr := reader.ReadString('\n')
            if readErr != nil {
                return
            }
            b.WriteString(line)
            if line == "\r\n" {
                break
            }
        }
        s.mu.Lock()
        s.got = b.String()
        s.mu.Unlock()
        _, _ = io.WriteString(upstream, s.response)
    }()
    return client, nil
}

func (s *scriptedEgress) Close() error { return nil }

func (s *scriptedEgress) forwarded() string {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.got
}

func (s *scriptedEgress) streamRequest() relay.StreamRequest {
    s.mu.Lock()
    defer s.mu.Unlock()
    if len(s.requests) == 0 {
        return relay.StreamRequest{}
    }
    return s.requests[0]
}

// dialProxyAbsolute hand-writes an absolute-form proxy request. net/http cannot
// be used because it refuses to emit the trusted-peer headers on a plain
// request and would normalize the absolute-form URL away.
func dialProxyAbsolute(t *testing.T, addr, target, route, clientIP string, extra map[string]string) *http.Response {
    t.Helper()
    conn, err := net.Dial("tcp", addr)
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = conn.Close() })
    var b strings.Builder
    fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\n", target, strings.TrimPrefix(strings.TrimPrefix(target, "http://"), "https://"))
    fmt.Fprintf(&b, "X-TunnelMesh-Route: %s\r\nX-TunnelMesh-Client-IP: %s\r\nX-TunnelMesh-Client-Port: 41234\r\n", route, clientIP)
    for key, value := range extra {
        fmt.Fprintf(&b, "%s: %s\r\n", key, value)
    }
    b.WriteString("\r\n")
    if _, err := io.WriteString(conn, b.String()); err != nil {
        t.Fatal(err)
    }
    _ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
    resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
    if err != nil {
        t.Fatalf("read absolute-form response: %v", err)
    }
    return resp
}

func proxyAbsoluteRoutes() stubProxyRoutes {
    return stubProxyRoutes{"tp-demo.tm.example.com": {
        ID: "r1", Domain: "tp-demo.tm.example.com", Name: "demo", AgentID: "agent-1",
        AuthMode: proxyentry.AuthModeNone, AllowPrivateTargets: true,
        Status: proxyentry.StatusActive,
    }}
}

func TestProxyEntryAbsoluteFormRewritesToOriginForm(t *testing.T) {
    egress := &scriptedEgress{response: "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nX-Target: real\r\n\r\nhello"}
    srv, _ := newProxyEntryFixture(t, proxyAbsoluteRoutes(), egress, proxyEntryTestConfig([]string{"127.0.0.1/32"}))

    resp := dialProxyAbsolute(t, srv.Addr(), "http://93.184.216.34:8080/status", "tp-demo.tm.example.com", "11.71.85.7", map[string]string{
        "Proxy-Authorization": "Basic ZGVtbzpzM2NyZXQ=",
        "Proxy-Connection":      "keep-alive",
        "X-TunnelMesh-Extra":    "must-not-leak",
    })
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        t.Fatalf("status = %d", resp.StatusCode)
    }
    if got := resp.Header.Get("X-Target"); got != "real" {
        t.Fatalf("upstream header = %q", got)
    }
    body, err := io.ReadAll(resp.Body)
    if err != nil {
        t.Fatal(err)
    }
    if string(body) != "hello" {
        t.Fatalf("body = %q", body)
    }

    forwarded := egress.forwarded()
    if !strings.HasPrefix(forwarded, "GET /status HTTP/1.1\r\n") {
        t.Fatalf("request line not rewritten to origin-form: %q", forwarded)
    }
    if !strings.Contains(forwarded, "\r\nHost: 93.184.216.34:8080\r\n") {
        t.Fatalf("Host header = %q", forwarded)
    }
    for _, banned := range []string{"Proxy-Authorization", "Proxy-Connection", "X-TunnelMesh-Route", "X-TunnelMesh-Client-IP", "X-TunnelMesh-Client-Port", "X-TunnelMesh-Extra"} {
        if strings.Contains(forwarded, banned) {
            t.Fatalf("%s leaked upstream: %q", banned, forwarded)
        }
    }

    req := egress.streamRequest()
    if req.AgentID != "agent-1" || req.Protocol != "http" || req.TargetHost != "93.184.216.34" || req.TargetPort != 8080 || req.TargetScheme != "http" {
        t.Fatalf("stream request = %#v", req)
    }
}

func TestProxyEntryAbsoluteFormDefaultsPortByScheme(t *testing.T) {
    cases := []struct {
        raw        string
        hostHeader string
        wantHost   string
        wantPort   int
        wantScheme string
    }{
        {"http://intranet.example.com/", "intranet.example.com", "intranet.example.com", 80, "http"},
        {"https://intranet.example.com/", "intranet.example.com", "intranet.example.com", 443, "https"},
        {"https://[fd00::1]:8443/api", "[fd00::1]:8443", "fd00::1", 8443, "https"},
        // OpenResty's location / rewrites the request line to origin-form and
        // forwards the authority in Host; HTTPS targets never take this path
        // because they always arrive as CONNECT.
        {"/status", "93.184.216.34:8080", "93.184.216.34", 8080, "http"},
        {"/", "intranet.example.com", "intranet.example.com", 80, "http"},
    }
    for _, tc := range cases {
        req := httptest.NewRequest(http.MethodGet, tc.raw, nil)
        req.Host = tc.hostHeader
        host, port, scheme, err := splitProxyTarget(req)
        if err != nil {
            t.Fatalf("%s (Host %s): %v", tc.raw, tc.hostHeader, err)
        }
        if host != tc.wantHost || port != tc.wantPort || scheme != tc.wantScheme {
            t.Fatalf("%s (Host %s) -> %s:%d/%s", tc.raw, tc.hostHeader, host, port, scheme)
        }
    }
    if _, _, _, err := splitProxyTarget(httptest.NewRequest(http.MethodGet, "/relative", nil)); err == nil {
        t.Fatal("origin-form without a Host authority must be rejected")
    }
}

func TestProxyEntryAbsoluteFormDeniesAndReportsEgress(t *testing.T) {
    denied := &scriptedEgress{response: "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"}
    srv, _ := newProxyEntryFixture(t, proxyAbsoluteRoutes(), denied, proxyEntryTestConfig([]string{"127.0.0.1/32"}))
    resp := dialProxyAbsolute(t, srv.Addr(), "http://169.254.169.254/latest/meta-data/", "tp-demo.tm.example.com", "11.71.85.7", nil)
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusForbidden {
        t.Fatalf("metadata target status = %d", resp.StatusCode)
    }
    body, _ := io.ReadAll(resp.Body)
    if !strings.Contains(string(body), "proxy_target_denied") {
        t.Fatalf("body = %q", body)
    }

    failing := &scriptedEgress{err: fmt.Errorf("agent offline")}
    failSrv, _ := newProxyEntryFixture(t, proxyAbsoluteRoutes(), failing, proxyEntryTestConfig([]string{"127.0.0.1/32"}))
    failResp := dialProxyAbsolute(t, failSrv.Addr(), "http://93.184.216.34:8080/status", "tp-demo.tm.example.com", "11.71.85.7", nil)
    defer failResp.Body.Close()
    if failResp.StatusCode != http.StatusBadGateway {
        t.Fatalf("egress failure status = %d", failResp.StatusCode)
    }
    failBody, _ := io.ReadAll(failResp.Body)
    if !strings.Contains(string(failBody), "proxy_egress_unavailable") {
        t.Fatalf("body = %q", failBody)
    }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/server/ -run 'TestProxyEntryAbsoluteForm' -count=1`
Expected: FAIL。前两条因为 `undefined: splitProxyTarget` 直接编译失败；即使先补上 `splitProxyTarget`，`TestProxyEntryAbsoluteFormRewritesToOriginForm` 也会因为 Task 10 占位返回 501 而失败（`status = 501`）。

- [ ] **Step 3: 最小实现**

替换 `internal/server/proxy_entry.go` 中 Task 10 的占位 `handleAbsoluteForm`：

```go
func (p *ProxyEntry) handleAbsoluteForm(w http.ResponseWriter, r *http.Request, route proxyentry.Route, clientIP net.IP, mode string, started time.Time) {
    host, port, scheme, err := splitProxyTarget(r)
    if err != nil {
        p.deny(w, r, route.Domain, mode, started, proxyentry.ErrTargetInvalid, "target")
        return
    }
    policy, err := proxyentry.NewTargetPolicy(route.TargetCIDRs, route.TargetPorts, route.AllowPrivateTargets)
    if err != nil {
        p.deny(w, r, route.Domain, mode, started, proxyentry.ErrTargetDenied, "target_policy")
        return
    }
    if err := policy.Validate(host, port); err != nil {
        p.deny(w, r, route.Domain, mode, started, targetError(err), "target")
        return
    }
    ctx, cancel := context.WithTimeout(r.Context(), p.config.ConnectTimeout)
    defer cancel()
    stream, err := p.opener.OpenStream(ctx, relay.StreamRequest{
        AgentID: route.AgentID, Protocol: "http",
        TargetHost: host, TargetPort: port, TargetScheme: scheme, HostHeader: host,
        Metadata: []byte(traceparentFromContext(ctx)),
    })
    if err != nil {
        p.deny(w, r, route.Domain, mode, started, egressError(err), "egress")
        return
    }
    defer stream.Close()

    upstreamReq := normalizeProxyEntryRequest(r, host, port)
    if err := upstreamReq.Write(stream); err != nil {
        p.deny(w, r, route.Domain, mode, started, proxyentry.ErrEgressUnavailable, "egress_write")
        return
    }
    resp, err := http.ReadResponse(bufio.NewReader(stream), upstreamReq)
    if err != nil {
        p.deny(w, r, route.Domain, mode, started, proxyentry.ErrEgressUnavailable, "egress_read")
        return
    }
    defer resp.Body.Close()
    p.audit(ctx, "proxy_request_forwarded", route, clientIP, net.JoinHostPort(host, strconv.Itoa(port)), 0, "")
    p.metrics.ObserveProxyEntryRequest(route.Domain, mode, "success", "")
    copyResponse(w, resp)
}

func splitProxyTarget(r *http.Request) (string, int, string, error) {
    if r == nil {
        return "", 0, "", proxyentry.ErrTargetInvalid
    }
    target, scheme := r.URL, r.URL.Scheme
    if target.Host == "" {
        // Origin-form: the authority survives only in Host. Defaulting the
        // scheme to http is correct, not a guess - an https:// target always
        // reaches this entry as CONNECT and is handled by handleConnect.
        authority := strings.TrimSpace(r.Host)
        if authority == "" {
            return "", 0, "", proxyentry.ErrTargetInvalid
        }
        parsed, err := url.Parse("http://" + authority)
        if err != nil {
            return "", 0, "", proxyentry.ErrTargetInvalid
        }
        target, scheme = parsed, "http"
    }
    if scheme != "http" && scheme != "https" {
        return "", 0, "", proxyentry.ErrTargetInvalid
    }
    host := target.Hostname()
    portText := target.Port()
    if portText == "" {
        if scheme == "https" {
            portText = "443"
        } else {
            portText = "80"
        }
    }
    port, err := strconv.Atoi(portText)
    if err != nil || port < 1 || port > 65535 || host == "" {
        return "", 0, "", proxyentry.ErrTargetInvalid
    }
    return host, port, scheme, nil
}

func normalizeProxyEntryRequest(r *http.Request, host string, port int) *http.Request {
    upstream := r.Clone(r.Context())
    upstream.RequestURI = ""
    upstream.URL.Scheme = ""
    upstream.URL.Host = ""
    upstream.Host = net.JoinHostPort(host, strconv.Itoa(port))
    // Hop-by-hop headers are meaningful only between the client and this entry.
    upstream.Header.Del("Proxy-Authorization")
    upstream.Header.Del("Proxy-Connection")
    upstream.Header.Del("Connection")
    upstream.Header.Del("Keep-Alive")
    upstream.Header.Del("Te")
    upstream.Header.Del("Trailer")
    upstream.Header.Del("Transfer-Encoding")
    upstream.Header.Del("Upgrade")
    // Route metadata is injected by OpenResty and must never reach the target.
    // Map keys are already canonical, so one prefix sweep covers every
    // X-TunnelMesh-* header without enumerating them.
    for key := range upstream.Header {
        if strings.HasPrefix(key, "X-Tunnelmesh-") {
            upstream.Header.Del(key)
        }
    }
    return upstream
}
```

实现约束：

- `targetError(err)` 与 `egressError(err)` 是 Task 10 已定义的映射函数：`targetError` 把 `proxyentry.ErrTargetInvalid` 原样返回、其它一律 `ErrTargetDenied`；`egressError` 把 `context.DeadlineExceeded` 映射为 `ErrEgressTimeout`、其它映射为 `ErrEgressUnavailable`。若 Task 10 中它们仍是内联判断，本任务先把它们抽成这两个函数再复用，避免 CONNECT 与绝对形式两条分支的映射漂移。
- `Connection`/`Upgrade` 等头必须删除：绝对形式分支不支持协议升级（WebSocket 走托管路由反代，不走代理入口），留着会让 `http.ReadResponse` 期待 101。
- 删除 `Transfer-Encoding` 后 `upstreamReq.Write` 会按 body 实际长度写 `Content-Length`；请求体转发依赖 `r.Body`，因此 listener 必须保持 `proxy_request_buffering off`（nginx 侧已在 Task 14 配置）。
- 不设置 `http.Client`：与 `ServeRoute` 一致，直接写 stream 再读一个响应，避免额外的连接池与重定向语义。重定向原样透传给客户端（`copyResponse` 复制 3xx 与 `Location`），由浏览器自己决定是否再次经过代理。
- `traceparentFromContext` 供两条分支共用：Task 10 的 `handleConnect` 已在 `Metadata` 里写 traceparent，本任务把那段内联逻辑抽成 `traceparentFromContext(ctx) string`（返回空串表示无 trace 上下文），CONNECT 分支同步改为调用它，避免两份实现漂移。
- `p.metrics.ObserveProxyEntryRequest` 是 Task 10 已注册的访问器；若 Task 10 已把它包成私有 helper（例如 `p.metricsRequest`），本任务复用该 helper 而不是直接访问字段。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/server/ -run 'TestProxyEntry' -count=1 && go test -race ./internal/server/ -run 'TestProxyEntry' -count=1 && go vet ./internal/server/`
Expected: PASS（Task 10 的 CONNECT 测试同时不回归）。

- [ ] **Step 5: Commit（需授权）**

```bash
git add internal/server/proxy_entry.go internal/server/proxy_entry_absolute_test.go
git commit -m "feat(server): forward absolute-form proxy requests"
```

---

### Task 12: 管理 API（`/api/v1/routes` 与 `/api/v1/credentials` 扩展）

**Files:**

- Modify: `internal/server/api.go`（`tunnelRequest`、`tunnelUpdateRequest`、`createTunnel`、`updateTunnel`、`apiService.CreateTunnel`、`publicTunnel`）
- Modify: `internal/server/proxy_entry_routes.go`（把 Task 7 内联的匿名 config 结构提为具名 `proxyRouteConfig`，新增 `normalizeProxyRouteConfig`、`proxyRouteConfigOf`）
- Modify: `internal/server/credential_api.go`（`credentialRequest.Username`、`credentialResponse.Username`、create/patch 透传）
- Create: `internal/server/api_proxy_route_test.go`
- Modify: `internal/server/credential_api_test.go`（追加 `proxy_basic` 用例）
- Modify: `docs/api/openapi.yaml`

**Interfaces:**

- Consumes: `storage.ProtocolHTTPProxy` / `storage.ProxyTargetWildcard`（Task 7）、`storage.CredentialTypeProxyBasic`（Task 8）、`proxyentry.NormalizeCIDR` / `proxyentry.NewSourceACL`（Task 4）、`proxyentry.NewTargetPolicy`（Task 6）、`routing.ValidateDomainPattern`、`a.credentialService.Get(ctx, auth.Principal, id)`、既有测试夹具 `apiTestServer` / `apiToken` / `apiJSON` / `setCredentialSecretStore`
- Produces:

```go
// proxyRouteConfig is the single authority for the tp-* config JSON shape:
// loadProxyRoutes decodes it and normalizeProxyRouteConfig encodes it.
type proxyRouteConfig struct {
    AuthMode             string   `json:"authMode"`
    CredentialID         string   `json:"credentialId,omitempty"`
    SourceCIDRs          []string `json:"sourceCIDRs"`
    TargetCIDRs          []string `json:"targetCIDRs"`
    TargetPorts          []int    `json:"targetPorts"`
    AllowPrivateTargets  *bool    `json:"allowPrivateTargets"`
    MaxConcurrentTunnels int      `json:"maxConcurrentTunnels,omitempty"`
    Description          string   `json:"description,omitempty"`
}

// normalizeProxyRouteConfig validates one proxy route policy and returns the
// canonical JSON stored in tunnels.config. The second return value is the
// caller-facing 400 message, empty when the policy is valid.
func normalizeProxyRouteConfig(in proxyRouteConfig) (string, string)

// proxyRouteConfigOf decodes a stored tunnels.config back into the typed shape,
// applying the documented defaults (authMode=none, allowPrivateTargets=true).
func proxyRouteConfigOf(raw string) (proxyRouteConfig, error)

// validateProxyRouteDomain enforces the tp-<name>.<suffix> shape for
// protocol=http-proxy routes without needing the configured domain suffix.
func validateProxyRouteDomain(domain string) error

// tunnelRouteConflict gains domainOnly: an http-proxy route claims a whole
// hostname, so its conflict check ignores path_prefix. Callers are createTunnel
// (id == "") and updateTunnel (id == the route being patched).
func (a *API) tunnelRouteConflict(ctx context.Context, id, domain, pathPrefix string, domainOnly bool) (bool, error)
```

`tunnelRequest` 与 `tunnelUpdateRequest` 新增字段（JSON 名与 spec §9 一致）：

```go
// tunnelRequest 追加
AuthMode             string   `json:"authMode"`
CredentialID         string   `json:"credentialId"`
SourceCIDRs          []string `json:"sourceCIDRs"`
TargetCIDRs          []string `json:"targetCIDRs"`
TargetPorts          []int    `json:"targetPorts"`
AllowPrivateTargets  *bool    `json:"allowPrivateTargets"`
MaxConcurrentTunnels *int     `json:"maxConcurrentTunnels"`
Description          string   `json:"description"`

// tunnelUpdateRequest 追加（全指针，PATCH 才能区分“未提供”与“显式清空”）
AuthMode             *string   `json:"authMode"`
CredentialID         *string   `json:"credentialId"`
SourceCIDRs          *[]string `json:"sourceCIDRs"`
TargetCIDRs          *[]string `json:"targetCIDRs"`
TargetPorts          *[]int    `json:"targetPorts"`
AllowPrivateTargets  *bool     `json:"allowPrivateTargets"`
MaxConcurrentTunnels *int      `json:"maxConcurrentTunnels"`
Description          *string   `json:"description"`
```

`credentialRequest` 追加 `Username string \`json:"username"\``；`credentialResponse` 追加 `Username string \`json:"username"\``。

- [ ] **Step 1: 写失败测试**

`internal/server/api_proxy_route_test.go`：

```go
package server

import (
    "encoding/json"
    "net/http"
    "testing"
)

// createProxyTestAgent registers one Agent and returns its ID; every proxy
// route needs an egress Agent.
func createProxyTestAgent(t *testing.T, h http.Handler, token, name string) string {
    t.Helper()
    created := apiJSON(t, h, http.MethodPost, "/api/v1/agents", token, "proxy-agent-"+name, map[string]any{"name": name})
    if created.Code != http.StatusCreated {
        t.Fatalf("agent create status=%d: %s", created.Code, created.Body.String())
    }
    var agent struct {
        Data struct {
            ID string `json:"id"`
        } `json:"data"`
    }
    if err := json.Unmarshal(created.Body.Bytes(), &agent); err != nil {
        t.Fatal(err)
    }
    return agent.Data.ID
}

// createProxyTestCredential registers one enabled proxy_basic credential.
func createProxyTestCredential(t *testing.T, h http.Handler, token, name, username, password string) string {
    t.Helper()
    created := apiJSON(t, h, http.MethodPost, "/api/v1/credentials", token, "proxy-cred-"+name, map[string]any{
        "name": name, "type": "proxy_basic", "username": username, "enabled": true,
        "secret": map[string]any{"password": password},
    })
    if created.Code != http.StatusCreated {
        t.Fatalf("credential create status=%d: %s", created.Code, created.Body.String())
    }
    var cred struct {
        Data struct {
            ID       string `json:"id"`
            Username string `json:"username"`
            HasSecret bool  `json:"hasSecret"`
        } `json:"data"`
    }
    if err := json.Unmarshal(created.Body.Bytes(), &cred); err != nil {
        t.Fatal(err)
    }
    if cred.Data.Username != username || !cred.Data.HasSecret {
        t.Fatalf("credential response = %+v", cred.Data)
    }
    return cred.Data.ID
}

type proxyRouteResponse struct {
    Data struct {
        ID                   string   `json:"id"`
        Protocol             string   `json:"protocol"`
        Domain               string   `json:"domain"`
        PathPrefix           string   `json:"pathPrefix"`
        TargetHost           string   `json:"targetHost"`
        TargetPort           int      `json:"targetPort"`
        ProxyURL             string   `json:"proxyUrl"`
        AuthMode             string   `json:"authMode"`
        CredentialID         string   `json:"credentialId"`
        SourceCIDRs          []string `json:"sourceCIDRs"`
        TargetCIDRs          []string `json:"targetCIDRs"`
        TargetPorts          []int    `json:"targetPorts"`
        AllowPrivateTargets  bool     `json:"allowPrivateTargets"`
        MaxConcurrentTunnels int      `json:"maxConcurrentTunnels"`
        Description          string   `json:"description"`
        Status               string   `json:"status"`
    } `json:"data"`
}

func TestAPICreatesProxyRouteWithSentinelTarget(t *testing.T) {
    api, admin, _ := apiTestServer(t)
    setCredentialSecretStore(t, api)
    h := api.Handler()
    token := apiToken(t, api, admin.Username, "admin-pass")
    agentID := createProxyTestAgent(t, h, token, "egress")
    credentialID := createProxyTestCredential(t, h, token, "proxy demo", "demo", "s3cret")

    created := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-route-1", map[string]any{
        "agentId": agentID, "protocol": "http-proxy", "domain": "tp-demo.tm.example.com",
        "authMode": "basic", "credentialId": credentialID,
        "sourceCIDRs": []string{"11.71.85.0/24", "10.1.2.3"},
        "targetCIDRs": []string{"10.10.0.0/16"}, "targetPorts": []int{443, 443, 8443},
        "allowPrivateTargets": true, "maxConcurrentTunnels": 8, "description": "demo egress",
    })
    if created.Code != http.StatusCreated {
        t.Fatalf("create status=%d: %s", created.Code, created.Body.String())
    }
    var got proxyRouteResponse
    if err := json.Unmarshal(created.Body.Bytes(), &got); err != nil {
        t.Fatal(err)
    }
    d := got.Data
    if d.Protocol != "http-proxy" || d.TargetHost != "*" || d.TargetPort != 0 || d.PathPrefix != "/" {
        t.Fatalf("sentinel target not enforced: %+v", d)
    }
    if d.ProxyURL != "https://tp-demo.tm.example.com" {
        t.Fatalf("proxyUrl = %q", d.ProxyURL)
    }
    if d.AuthMode != "basic" || d.CredentialID != credentialID || d.Description != "demo egress" {
        t.Fatalf("policy fields = %+v", d)
    }
    if len(d.SourceCIDRs) != 2 || d.SourceCIDRs[1] != "10.1.2.3/32" {
        t.Fatalf("bare IP was not normalized to a CIDR: %#v", d.SourceCIDRs)
    }
    if len(d.TargetPorts) != 2 || d.TargetPorts[0] != 443 || d.TargetPorts[1] != 8443 {
        t.Fatalf("target ports not deduped and sorted: %#v", d.TargetPorts)
    }
    if !d.AllowPrivateTargets || d.MaxConcurrentTunnels != 8 {
        t.Fatalf("policy switches = %+v", d)
    }
    if json.Valid(created.Body.Bytes()) == false {
        t.Fatal("response is not valid JSON")
    }
}

// tp-* 域名是“整机代理端点”，一条域名只能有一条路由。这个用例锁住三层语义：
// 大小写变体算同一条、同域名的路径级反代路由不允许共存、update 改域名同样受限。
func TestAPIProxyRouteRejectsDuplicateDomain(t *testing.T) {
    api, admin, _ := apiTestServer(t)
    setCredentialSecretStore(t, api)
    h := api.Handler()
    token := apiToken(t, api, admin.Username, "admin-pass")
    agentID := createProxyTestAgent(t, h, token, "egress-dup")

    first := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-dup-1", map[string]any{
        "agentId": agentID, "protocol": "http-proxy", "domain": "tp-dup.tm.example.com",
        "authMode": "none", "sourceCIDRs": []string{"10.0.0.0/8"},
    })
    if first.Code != http.StatusCreated {
        t.Fatalf("create status=%d: %s", first.Code, first.Body.String())
    }
    var created proxyRouteResponse
    if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
        t.Fatal(err)
    }

    upper := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-dup-2", map[string]any{
        "agentId": agentID, "protocol": "http-proxy", "domain": "TP-DUP.tm.example.com",
        "authMode": "none", "sourceCIDRs": []string{"10.0.0.0/8"},
    })
    if upper.Code != http.StatusConflict {
        t.Fatalf("case-variant duplicate status=%d body=%s, want 409", upper.Code, upper.Body.String())
    }

    reverse := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-dup-3", map[string]any{
        "agentId": agentID, "protocol": "http", "domain": "tp-dup.tm.example.com", "pathPrefix": "/admin",
        "targetHost": "10.0.0.9", "targetPort": 8080,
    })
    if reverse.Code != http.StatusConflict {
        t.Fatalf("reverse-proxy route on a tp-* domain status=%d body=%s, want 409", reverse.Code, reverse.Body.String())
    }

    other := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-dup-4", map[string]any{
        "agentId": agentID, "protocol": "http-proxy", "domain": "tp-other.tm.example.com",
        "authMode": "none", "sourceCIDRs": []string{"10.0.0.0/8"},
    })
    if other.Code != http.StatusCreated {
        t.Fatalf("second proxy route status=%d: %s", other.Code, other.Body.String())
    }
    var otherRoute proxyRouteResponse
    if err := json.Unmarshal(other.Body.Bytes(), &otherRoute); err != nil {
        t.Fatal(err)
    }
    if otherRoute.Data.ID == created.Data.ID {
        t.Fatal("two distinct proxy routes share an ID")
    }

    renamed := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+otherRoute.Data.ID, token, "", map[string]any{
        "domain": "tp-dup.tm.example.com",
    })
    if renamed.Code != http.StatusConflict {
        t.Fatalf("rename onto an existing tp-* domain status=%d body=%s, want 409", renamed.Code, renamed.Body.String())
    }
}

// 反向顺序也必须被拒：反代路由先占用 tp-* 域名时，后建的 proxy 路由不能成功，
// 否则它会被 nginx 的精确 server_name 永久遮蔽，且后台看不出原因。
func TestAPIProxyRouteRejectsDomainClaimedByReverseProxy(t *testing.T) {
    api, admin, _ := apiTestServer(t)
    setCredentialSecretStore(t, api)
    h := api.Handler()
    token := apiToken(t, api, admin.Username, "admin-pass")
    agentID := createProxyTestAgent(t, h, token, "egress-shadow")

    reverse := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-shadow-1", map[string]any{
        "agentId": agentID, "protocol": "http", "domain": "tp-shadow.tm.example.com", "pathPrefix": "/admin",
        "targetHost": "10.0.0.9", "targetPort": 8080,
    })
    if reverse.Code != http.StatusCreated {
        t.Fatalf("reverse-proxy create status=%d: %s", reverse.Code, reverse.Body.String())
    }

    shadowed := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-shadow-2", map[string]any{
        "agentId": agentID, "protocol": "http-proxy", "domain": "tp-shadow.tm.example.com",
        "authMode": "none", "sourceCIDRs": []string{"10.0.0.0/8"},
    })
    if shadowed.Code != http.StatusConflict {
        t.Fatalf("proxy route on a claimed domain status=%d body=%s, want 409", shadowed.Code, shadowed.Body.String())
    }
}

func TestAPIProxyRouteValidationFailures(t *testing.T) {
    api, admin, _ := apiTestServer(t)
    setCredentialSecretStore(t, api)
    h := api.Handler()
    token := apiToken(t, api, admin.Username, "admin-pass")
    agentID := createProxyTestAgent(t, h, token, "egress-bad")
    credentialID := createProxyTestCredential(t, h, token, "proxy bad", "demo", "s3cret")

    base := func() map[string]any {
        return map[string]any{
            "agentId": agentID, "protocol": "http-proxy", "domain": "tp-ok.tm.example.com",
            "authMode": "none",
        }
    }
    cases := []struct {
        name  string
        mutate func(map[string]any)
    }{
        {"missing tp- prefix", func(m map[string]any) { m["domain"] = "demo.tm.example.com" }},
        {"missing suffix", func(m map[string]any) { m["domain"] = "tp-demo" }},
        {"underscore in name", func(m map[string]any) { m["domain"] = "tp-de_mo.tm.example.com" }},
        {"empty name", func(m map[string]any) { m["domain"] = "tp-.tm.example.com" }},
        {"name too long", func(m map[string]any) {
            m["domain"] = "tp-0123456789012345678901234567890123.tm.example.com"
        }},
        {"client supplied targetHost", func(m map[string]any) { m["targetHost"] = "10.0.0.1" }},
        {"client supplied targetPort", func(m map[string]any) { m["targetPort"] = 8080 }},
        {"client supplied config map", func(m map[string]any) { m["config"] = map[string]any{"authMode": "none"} }},
        {"basic without credential", func(m map[string]any) { m["authMode"] = "basic" }},
        {"unknown credential", func(m map[string]any) {
            m["authMode"] = "basic"
            m["credentialId"] = "cred-does-not-exist"
        }},
        {"unknown auth mode", func(m map[string]any) { m["authMode"] = "digest" }},
        {"invalid source cidr", func(m map[string]any) { m["sourceCIDRs"] = []string{"11.71.85.0/33"} }},
        {"invalid target cidr", func(m map[string]any) { m["targetCIDRs"] = []string{"not-a-cidr"} }},
        {"invalid target port", func(m map[string]any) { m["targetPorts"] = []int{70000} }},
        {"negative concurrency", func(m map[string]any) { m["maxConcurrentTunnels"] = -1 }},
    }
    for i, tc := range cases {
        body := base()
        tc.mutate(body)
        resp := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "", body)
        if resp.Code != http.StatusBadRequest && resp.Code != http.StatusForbidden && resp.Code != http.StatusNotFound {
            t.Fatalf("%s: status=%d body=%s", tc.name, resp.Code, resp.Body.String())
        }
        _ = i
    }
}

func TestAPIProxyRouteRejectsForeignCredential(t *testing.T) {
    api, admin, user := apiTestServer(t)
    setCredentialSecretStore(t, api)
    h := api.Handler()
    adminToken := apiToken(t, api, admin.Username, "admin-pass")
    userToken := apiToken(t, api, user.Username, "alice-pass")
    agentID := createProxyTestAgent(t, h, adminToken, "egress-owner")
    credentialID := createProxyTestCredential(t, h, adminToken, "admin proxy", "demo", "s3cret")

    // The credential belongs to the admin, so a non-admin caller must not be
    // able to bind it to their own route.
    resp := apiJSON(t, h, http.MethodPost, "/api/v1/routes", userToken, "proxy-foreign-cred", map[string]any{
        "agentId": agentID, "protocol": "http-proxy", "domain": "tp-alice.tm.example.com",
        "authMode": "basic", "credentialId": credentialID,
    })
    if resp.Code != http.StatusForbidden && resp.Code != http.StatusNotFound {
        t.Fatalf("foreign credential status=%d body=%s", resp.Code, resp.Body.String())
    }
}

func TestAPIProxyRouteUpdateKeepsSentinelAndRejectsProtocolSwitch(t *testing.T) {
    api, admin, _ := apiTestServer(t)
    setCredentialSecretStore(t, api)
    h := api.Handler()
    token := apiToken(t, api, admin.Username, "admin-pass")
    agentID := createProxyTestAgent(t, h, token, "egress-update")

    created := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-route-upd", map[string]any{
        "agentId": agentID, "protocol": "http-proxy", "domain": "tp-upd.tm.example.com",
        "authMode": "none", "sourceCIDRs": []string{"10.0.0.0/8"},
    })
    if created.Code != http.StatusCreated {
        t.Fatalf("create status=%d: %s", created.Code, created.Body.String())
    }
    var route proxyRouteResponse
    if err := json.Unmarshal(created.Body.Bytes(), &route); err != nil {
        t.Fatal(err)
    }

    patched := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+route.Data.ID, token, "", map[string]any{
        "sourceCIDRs": []string{}, "allowPrivateTargets": false, "status": "disabled",
        "description": "closed for now",
    })
    if patched.Code != http.StatusOK {
        t.Fatalf("patch status=%d: %s", patched.Code, patched.Body.String())
    }
    var updated proxyRouteResponse
    if err := json.Unmarshal(patched.Body.Bytes(), &updated); err != nil {
        t.Fatal(err)
    }
    if updated.Data.TargetHost != "*" || updated.Data.TargetPort != 0 {
        t.Fatalf("sentinel lost on update: %+v", updated.Data)
    }
    if len(updated.Data.SourceCIDRs) != 0 {
        t.Fatalf("sourceCIDRs not cleared: %#v", updated.Data.SourceCIDRs)
    }
    if updated.Data.AllowPrivateTargets || updated.Data.Status != "disabled" || updated.Data.Description != "closed for now" {
        t.Fatalf("patched fields = %+v", updated.Data)
    }

    switched := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+route.Data.ID, token, "", map[string]any{"protocol": "http"})
    if switched.Code != http.StatusBadRequest {
        t.Fatalf("protocol switch away from http-proxy status=%d body=%s", switched.Code, switched.Body.String())
    }
    toProxy := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "plain-route", map[string]any{
        "agentId": agentID, "protocol": "http", "domain": "app.tm.example.com", "pathPrefix": "/",
        "targetHost": "10.0.0.9", "targetPort": 8080,
    })
    if toProxy.Code != http.StatusCreated {
        t.Fatalf("plain route create status=%d: %s", toProxy.Code, toProxy.Body.String())
    }
    var plain proxyRouteResponse
    if err := json.Unmarshal(toProxy.Body.Bytes(), &plain); err != nil {
        t.Fatal(err)
    }
    upgraded := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+plain.Data.ID, token, "", map[string]any{"protocol": "http-proxy"})
    if upgraded.Code != http.StatusBadRequest {
        t.Fatalf("protocol switch to http-proxy status=%d body=%s", upgraded.Code, upgraded.Body.String())
    }
}

func TestAPIProxyRouteIdempotentReplay(t *testing.T) {
    api, admin, _ := apiTestServer(t)
    setCredentialSecretStore(t, api)
    h := api.Handler()
    token := apiToken(t, api, admin.Username, "admin-pass")
    agentID := createProxyTestAgent(t, h, token, "egress-idem")
    body := map[string]any{
        "agentId": agentID, "protocol": "http-proxy", "domain": "tp-idem.tm.example.com", "authMode": "none",
    }
    first := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-idem-key", body)
    if first.Code != http.StatusCreated {
        t.Fatalf("first status=%d: %s", first.Code, first.Body.String())
    }
    replay := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "proxy-idem-key", body)
    if replay.Code != http.StatusCreated {
        t.Fatalf("replay status=%d: %s", replay.Code, replay.Body.String())
    }
    var a, b proxyRouteResponse
    if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
        t.Fatal(err)
    }
    if err := json.Unmarshal(replay.Body.Bytes(), &b); err != nil {
        t.Fatal(err)
    }
    if a.Data.ID != b.Data.ID {
        t.Fatalf("idempotent replay created a second route: %s vs %s", a.Data.ID, b.Data.ID)
    }
}

func TestNormalizeProxyRouteConfigDefaults(t *testing.T) {
    encoded, message := normalizeProxyRouteConfig(proxyRouteConfig{})
    if message != "" {
        t.Fatalf("empty policy must be valid, got %q", message)
    }
    decoded, err := proxyRouteConfigOf(encoded)
    if err != nil {
        t.Fatal(err)
    }
    if decoded.AuthMode != "none" || decoded.AllowPrivateTargets == nil || !*decoded.AllowPrivateTargets {
        t.Fatalf("defaults = %+v", decoded)
    }
    if decoded.SourceCIDRs == nil || decoded.TargetCIDRs == nil || decoded.TargetPorts == nil {
        t.Fatalf("empty allowlists must round-trip as [] not null: %s", encoded)
    }
    if _, message := normalizeProxyRouteConfig(proxyRouteConfig{AuthMode: "basic"}); message == "" {
        t.Fatal("basic without credentialId must be rejected")
    }
    if _, message := normalizeProxyRouteConfig(proxyRouteConfig{Description: string(make([]byte, 300))}); message == "" {
        t.Fatal("oversized description must be rejected")
    }
}
```

`internal/server/credential_api_test.go` 追加：

```go
func TestCredentialAPIProxyBasicNeverEchoesPassword(t *testing.T) {
    api, _, user := apiTestServer(t)
    setCredentialSecretStore(t, api)
    token := apiToken(t, api, user.Username, "alice-pass")
    h := api.Handler()

    created := apiJSON(t, h, http.MethodPost, "/api/v1/credentials", token, "cred-proxy-basic", map[string]any{
        "name": "proxy demo", "type": "proxy_basic", "username": "demo", "enabled": true,
        "secret": map[string]any{"password": "s3cret"},
    })
    if created.Code != http.StatusCreated {
        t.Fatalf("create status=%d: %s", created.Code, created.Body.String())
    }
    if strings.Contains(created.Body.String(), "s3cret") {
        t.Fatalf("password leaked into the response: %s", created.Body.String())
    }
    var envelope struct {
        Data struct {
            ID        string `json:"id"`
            Type      string `json:"type"`
            Username  string `json:"username"`
            PublicKey string `json:"publicKey"`
            HasSecret bool   `json:"hasSecret"`
        } `json:"data"`
    }
    if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil {
        t.Fatal(err)
    }
    if envelope.Data.Username != "demo" || envelope.Data.PublicKey != "demo" || !envelope.Data.HasSecret {
        t.Fatalf("response = %+v", envelope.Data)
    }

    missing := apiJSON(t, h, http.MethodPost, "/api/v1/credentials", token, "cred-proxy-basic-no-user", map[string]any{
        "name": "no username", "type": "proxy_basic", "enabled": true,
        "secret": map[string]any{"password": "s3cret"},
    })
    if missing.Code != http.StatusBadRequest {
        t.Fatalf("proxy_basic without username status=%d body=%s", missing.Code, missing.Body.String())
    }

    noSecret := apiJSON(t, h, http.MethodPost, "/api/v1/credentials", token, "cred-proxy-basic-no-secret", map[string]any{
        "name": "no secret", "type": "proxy_basic", "username": "demo2", "enabled": true,
    })
    if noSecret.Code != http.StatusBadRequest {
        t.Fatalf("proxy_basic without password status=%d body=%s", noSecret.Code, noSecret.Body.String())
    }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/server/ -run 'ProxyRoute|ProxyBasic|NormalizeProxyRouteConfig' -count=1`
Expected: FAIL，`undefined: normalizeProxyRouteConfig`、`undefined: proxyRouteConfigOf`（编译期即失败）。补上两个函数后，HTTP 用例仍失败：创建返回 400 `agentId, targetHost and valid targetPort are required`，因为哨兵特例还没加。

- [ ] **Step 3: 最小实现**

**(a) `internal/server/proxy_entry_routes.go`**

把 Task 7 `loadProxyRoutes` 里的匿名 config 结构替换为具名 `proxyRouteConfig`（字段与 Task 7 一致，另加 `Description`），`loadProxyRoutes` 改为：

```go
cfg, err := proxyRouteConfigOf(tunnel.Config)
if err != nil {
    return nil, errors.New("proxy route config is invalid: " + tunnel.ID)
}
```

新增：

```go
var proxyRouteNamePattern = regexp.MustCompile(`^tp-[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?\..+$`)

func proxyRouteConfigOf(raw string) (proxyRouteConfig, error) {
    cfg := proxyRouteConfig{AuthMode: proxyentry.AuthModeNone, AllowPrivateTargets: boolPtr(true)}
    trimmed := strings.TrimSpace(raw)
    if trimmed != "" && trimmed != "{}" {
        if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
            return proxyRouteConfig{}, err
        }
    }
    if cfg.AllowPrivateTargets == nil {
        cfg.AllowPrivateTargets = boolPtr(true)
    }
    if strings.TrimSpace(cfg.AuthMode) == "" {
        cfg.AuthMode = proxyentry.AuthModeNone
    }
    return cfg, nil
}

func normalizeProxyRouteConfig(in proxyRouteConfig) (string, string) {
    authMode := strings.ToLower(strings.TrimSpace(in.AuthMode))
    if authMode == "" {
        authMode = proxyentry.AuthModeNone
    }
    if authMode != proxyentry.AuthModeNone && authMode != proxyentry.AuthModeBasic {
        return "", "authMode must be none or basic"
    }
    credentialID := strings.TrimSpace(in.CredentialID)
    if authMode == proxyentry.AuthModeBasic && credentialID == "" {
        return "", "credentialId is required when authMode is basic"
    }
    if authMode == proxyentry.AuthModeNone {
        credentialID = ""
    }
    sourceCIDRs, message := normalizeCIDRList(in.SourceCIDRs, "sourceCIDRs")
    if message != "" {
        return "", message
    }
    targetCIDRs, message := normalizeCIDRList(in.TargetCIDRs, "targetCIDRs")
    if message != "" {
        return "", message
    }
    targetPorts := normalizePortList(in.TargetPorts)
    for _, port := range in.TargetPorts {
        if port < 1 || port > 65535 {
            return "", "targetPorts must be between 1 and 65535"
        }
    }
    if in.MaxConcurrentTunnels < 0 {
        return "", "maxConcurrentTunnels must be >= 0"
    }
    description := strings.TrimSpace(in.Description)
    if len(description) > 256 {
        return "", "description must be at most 256 characters"
    }
    allowPrivate := true
    if in.AllowPrivateTargets != nil {
        allowPrivate = *in.AllowPrivateTargets
    }
    // Reuse the runtime policy so an accepted write can never produce a route
    // the data plane would refuse to build.
    if _, err := proxyentry.NewTargetPolicy(targetCIDRs, targetPorts, allowPrivate); err != nil {
        return "", "target policy is invalid: " + err.Error()
    }
    if _, err := proxyentry.NewSourceACL(sourceCIDRs); err != nil && len(sourceCIDRs) > 0 {
        return "", "sourceCIDRs are invalid"
    }
    out := proxyRouteConfig{
        AuthMode: authMode, CredentialID: credentialID,
        SourceCIDRs: sourceCIDRs, TargetCIDRs: targetCIDRs, TargetPorts: targetPorts,
        AllowPrivateTargets: &allowPrivate, MaxConcurrentTunnels: in.MaxConcurrentTunnels,
        Description: description,
    }
    encoded, err := json.Marshal(out)
    if err != nil {
        return "", "invalid proxy route config"
    }
    return string(encoded), ""
}

// normalizeCIDRList accepts bare IPs by adding the host mask, drops duplicates
// and keeps the caller's order so the UI list stays stable.
func normalizeCIDRList(raw []string, field string) ([]string, string) {
    out := make([]string, 0, len(raw))
    seen := make(map[string]struct{}, len(raw))
    for _, item := range raw {
        normalized, err := proxyentry.NormalizeCIDR(item)
        if err != nil {
            return nil, field + " contains an invalid entry: " + strings.TrimSpace(item)
        }
        if _, dup := seen[normalized]; dup {
            continue
        }
        seen[normalized] = struct{}{}
        out = append(out, normalized)
    }
    return out, ""
}

// normalizePortList dedupes and sorts so two equivalent writes produce
// byte-identical config JSON, which keeps idempotent replays comparable.
func normalizePortList(raw []int) []int {
    out := make([]int, 0, len(raw))
    seen := make(map[int]struct{}, len(raw))
    for _, port := range raw {
        if _, dup := seen[port]; dup {
            continue
        }
        seen[port] = struct{}{}
        out = append(out, port)
    }
    sort.Ints(out)
    return out
}

func validateProxyRouteDomain(domain string) error {
    if !proxyRouteNamePattern.MatchString(domain) {
        return errors.New("http-proxy domain must look like tp-<name>.<suffix> with a lowercase name of at most 32 characters")
    }
    return nil
}
```

`normalizeCIDRList` / `normalizePortList` 若仓库已有等价实现（例如 agent policy 的 CIDR 归一化），改为复用既有函数，不新增第二份实现；本步骤先按新增写，Step 5 的复审确认无重复后再定稿。

**(b) `internal/server/api.go`：`createTunnel`**

在 `decodeJSON` 之后、既有必填校验之前插入 proxy 分支：

```go
protocol := strings.ToLower(strings.TrimSpace(req.Protocol))
isProxyRoute := protocol == storage.ProtocolHTTPProxy
if isProxyRoute {
    if req.AgentID == "" {
        writeAPIError(w, http.StatusBadRequest, "agentId is required")
        return
    }
    if strings.TrimSpace(req.TargetHost) != "" && strings.TrimSpace(req.TargetHost) != storage.ProxyTargetWildcard {
        writeAPIError(w, http.StatusBadRequest, "targetHost must be omitted for http-proxy routes")
        return
    }
    if req.TargetPort != 0 {
        writeAPIError(w, http.StatusBadRequest, "targetPort must be omitted for http-proxy routes")
        return
    }
    if len(req.Config) > 0 {
        writeAPIError(w, http.StatusBadRequest, "http-proxy routes use dedicated fields; config must be omitted")
        return
    }
    req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
    if err := routing.ValidateDomainPattern(req.Domain); err != nil {
        writeAPIError(w, http.StatusBadRequest, err.Error())
        return
    }
    if err := validateProxyRouteDomain(req.Domain); err != nil {
        writeAPIError(w, http.StatusBadRequest, err.Error())
        return
    }
    if _, err := a.service.GetAgent(r.Context(), req.AgentID); err != nil {
        writeStorageError(w, err)
        return
    }
    encoded, status, message := a.buildProxyRoutePolicy(r.Context(), p, proxyRouteConfig{
        AuthMode: req.AuthMode, CredentialID: req.CredentialID,
        SourceCIDRs: req.SourceCIDRs, TargetCIDRs: req.TargetCIDRs, TargetPorts: req.TargetPorts,
        AllowPrivateTargets: req.AllowPrivateTargets, MaxConcurrentTunnels: concurrencyOrZero(req.MaxConcurrentTunnels),
        Description: req.Description,
    })
    if message != "" {
        writeAPIError(w, status, message)
        return
    }
    req.Protocol = storage.ProtocolHTTPProxy
    req.PathPrefix = "/"
    req.TargetHost = storage.ProxyTargetWildcard
    req.TargetPort = 0
    req.Config = nil
    // Fall through to the shared conflict check and audit path with the
    // sentinel already in place.
    proxyConfigJSON = encoded
}
```

其中 `proxyConfigJSON` 是分支前声明的 `string`（`var proxyConfigJSON string`），`concurrencyOrZero(*int) int` 是 nil 安全取值件，随后替换既有的 `configBytes, message := normalizeUpstreamRouteConfig(config)`：

```go
configBytes := proxyConfigJSON
if !isProxyRoute {
    config := req.Config
    if config == nil {
        config = map[string]any{}
    }
    config["hostHeader"] = strings.TrimSpace(req.HostHeader)
    config["targetScheme"] = strings.ToLower(strings.TrimSpace(req.TargetScheme))
    config["tlsServerName"] = strings.TrimSpace(req.TLSServerName)
    var message string
    configBytes, message = normalizeUpstreamRouteConfig(config)
    if message != "" {
        writeAPIError(w, http.StatusBadRequest, message)
        return
    }
}
```

凭据归属校验与 config 归一化拆成两个函数：创建路径用组合版 `buildProxyRoutePolicy`，更新路径单独复用 `validateProxyCredential`（更新时策略来自“已存配置 + 指针覆盖”，不能整块重建）。Handler 不直接查库，凭据一律走 `credentialService`：

```go
// validateProxyCredential checks that a basic-auth route references a
// proxy_basic credential the caller may actually see. The message is empty on
// success; status is the HTTP code to use when it is not. Unknown and
// unauthorized IDs share one message so credential IDs cannot be enumerated.
func (a *API) validateProxyCredential(ctx context.Context, p auth.Principal, authMode, credentialID string) (int, string) {
    if strings.ToLower(strings.TrimSpace(authMode)) != proxyentry.AuthModeBasic {
        return 0, ""
    }
    if a.credentialService == nil {
        return http.StatusServiceUnavailable, "credential service unavailable"
    }
    id := strings.TrimSpace(credentialID)
    if id == "" {
        return http.StatusBadRequest, "credentialId is required when authMode is basic"
    }
    credential, err := a.credentialService.Get(ctx, p, id)
    if err != nil {
        return credentialErrorStatus(err), "credential is not usable"
    }
    if credential.Type != storage.CredentialTypeProxyBasic || !credential.Enabled || credential.DeletedAt != nil {
        return http.StatusBadRequest, "credentialId must reference an enabled proxy_basic credential"
    }
    return 0, ""
}

// buildProxyRoutePolicy is the create-path composition: validate the credential
// reference, then normalize the whole policy into the stored config JSON.
func (a *API) buildProxyRoutePolicy(ctx context.Context, p auth.Principal, policy proxyRouteConfig) (string, int, string) {
    status, message := a.validateProxyCredential(ctx, p, policy.AuthMode, policy.CredentialID)
    if message != "" {
        return "", status, message
    }
    encoded, message := normalizeProxyRouteConfig(policy)
    if message != "" {
        return "", http.StatusBadRequest, message
    }
    return encoded, 0, ""
}

func concurrencyOrZero(v *int) int {
    if v == nil {
        return 0
    }
    return *v
}
```

`credentialErrorStatus(err)` 复用 `credential_api.go` 中 `writeCredentialError` 已有的错误到状态码映射：把那段 switch 抽成 `credentialErrorStatus(err) int`，`writeCredentialError` 改为调用它，避免两份映射。未找到与无权访问都必须映射为 403/404（不得回 200），并统一对外文案 `credential is not usable`，防止凭据 ID 枚举。

**路由域名冲突判定：`http-proxy` 按域名整体互斥（不看 `path_prefix`）**

仓库现状（`main` @ `b3acd34`，已核实；`internal/server/api.go` 自 `f0ca22c` 起未变更，行号同样适用）：`a.tunnelRouteConflict(ctx, id, domain, pathPrefix)`（`internal/server/api.go:1246`，`updateTunnel` 调用）与 `createTunnel`（`internal/server/api.go:1262`）里内联的同语义循环（`internal/server/api.go:1287`）都用 `strings.EqualFold(route.Domain, domain) && route.PathPrefix == pathPrefix` 判冲突；`tunnels` 表另有 `UNIQUE(domain, path_prefix)`。

这个条件对反代路由是正确的（同域名不同前缀是合法的多路径路由），但对 `http-proxy` 不成立：tp-* 域名的 `path_prefix` 恒为 `"/"` 且没有语义，它是一个整机代理端点。若允许它与同域名的路径级反代路由共存，nginx 的精确 `server_name` 会优先于 tp-* 的正则 `server_name`，那条 proxy 路由永远不可达，而 `UNIQUE(domain, path_prefix)` 也拦不住（两行前缀不同）。因此必须在应用层按域名整体互斥。

改法（两处重复 + 本次新增语义 = 三处，按 DRY 收敛成一个函数）：

1. `tunnelRouteConflict` 增加 `domainOnly bool` 形参：为 true 时只比较 `strings.EqualFold(route.Domain, domain)`，跳过 `PathPrefix` 比较；`domain == ""` 仍直接返回 `false, nil`。
2. `createTunnel` 删掉 1287 起的内联循环，改调 `a.tunnelRouteConflict(r.Context(), "", req.Domain, req.PathPrefix, isProxyRoute)`。`id` 传空串代表新建，既有 `route.ID != id` 判断对空串天然成立；冲突时沿用既有文案与状态码 `writeAPIError(w, http.StatusConflict, "route already exists")`，不新增错误码。
3. `updateTunnel` 的调用处传 `domainOnly = current.Protocol == storage.ProtocolHTTPProxy || protocol == storage.ProtocolHTTPProxy`。协议切换已被 (d) 的守卫拦掉，写全两侧只是让“任一端是 proxy 路由”的语义显式，避免以后放开切换时漏改。
4. 不新增查询：`listAllTunnels` 的调用次数与既有一致（create 路径本来就调了一次）。

集群模式下多个 Server 并发创建时 `a.routeMu` 只在进程内生效，跨节点竞争最终由 `UNIQUE(domain, path_prefix)` 兜底，`writeStorageError` 已把含 `unique` / `constraint` 的驱动错误映射为 409。注意两点：该兜底**只覆盖前缀也相同**的情况，跨协议的域名遮蔽必须靠上面的 `domainOnly` 判定在进库前拦住；兜底会把驱动原文写进 `msg`，这是所有路由共享的既有行为，本计划不改（改动会牵动全部路由的错误契约，属于独立议题）。

**(c) `internal/server/api.go`：`apiService.CreateTunnel`**

```go
func (s *apiService) CreateTunnel(ctx context.Context, v storage.Tunnel) (storage.Tunnel, error) {
    if v.Protocol == storage.ProtocolHTTPProxy {
        // The real target comes from each proxied request, so the sentinel
        // target is the only valid stored value.
        if v.AgentID == "" || v.TargetHost != storage.ProxyTargetWildcard || v.TargetPort != 0 {
            return storage.Tunnel{}, errors.New("http-proxy routes require an agentId and the wildcard sentinel target")
        }
    } else if v.AgentID == "" || v.TargetHost == "" || v.TargetPort < 1 || v.TargetPort > 65535 {
        return storage.Tunnel{}, errors.New("agentId, targetHost and valid targetPort are required")
    }
    ...
}
```

其余部分（`ValidateDomainPattern`、`Create`、回读最新行）保持不变。

**(d) `internal/server/api.go`：`updateTunnel`**

- `full`（PUT）必填校验：`protocol == http-proxy` 时只要求 `AgentID`，不要求 `TargetHost`/`TargetPort`。
- `req.Protocol != nil` 分支的允许值加上 `storage.ProtocolHTTPProxy`；并在解析出 `protocol` 后立即判断“协议切换”：

```go
if next.Protocol != current.Protocol && (protocol == storage.ProtocolHTTPProxy || current.Protocol == storage.ProtocolHTTPProxy) {
    writeAPIError(w, http.StatusBadRequest, "protocol cannot be changed to or from http-proxy; create a new route")
    return
}
```

- 在既有 `req.TargetHost` / `req.TargetPort` 校验之前加 proxy 守卫：

```go
isProxyRoute := current.Protocol == storage.ProtocolHTTPProxy
if isProxyRoute {
    if req.TargetHost != nil && strings.TrimSpace(*req.TargetHost) != storage.ProxyTargetWildcard {
        writeAPIError(w, http.StatusBadRequest, "targetHost is fixed for http-proxy routes")
        return
    }
    if req.TargetPort != nil && *req.TargetPort != 0 {
        writeAPIError(w, http.StatusBadRequest, "targetPort is fixed for http-proxy routes")
        return
    }
    if req.Config != nil || req.HostHeader != nil || req.TargetScheme != nil || req.TLSServerName != nil {
        writeAPIError(w, http.StatusBadRequest, "http-proxy routes use dedicated fields; config, hostHeader, targetScheme and tlsServerName must be omitted")
        return
    }
    next.TargetHost = storage.ProxyTargetWildcard
    next.TargetPort = 0
    next.PathPrefix = "/"
}
```

- proxy 路由的策略字段更新：先用 `proxyRouteConfigOf(next.Config)` 取当前策略，再按指针逐字段覆盖，最后 `normalizeProxyRouteConfig` 写回。凭据变化时同样走 `buildProxyRoutePolicy` 的凭据校验（把该方法拆成 `validateProxyCredential(ctx, p, authMode, credentialID) (int, string)` 与 `normalizeProxyRouteConfig` 两步，update 分支只在前者通过后才归一化）。

```go
if isProxyRoute && (req.AuthMode != nil || req.CredentialID != nil || req.SourceCIDRs != nil ||
    req.TargetCIDRs != nil || req.TargetPorts != nil || req.AllowPrivateTargets != nil ||
    req.MaxConcurrentTunnels != nil || req.Description != nil) {
    // `policy` is deliberately not named `current`: updateTunnel already takes
    // a `current storage.Tunnel` parameter, and shadowing it would silently
    // change the meaning of every later `current.` reference.
    policy, err := proxyRouteConfigOf(next.Config)
    if err != nil {
        writeAPIError(w, http.StatusBadRequest, "stored proxy route config is invalid")
        return
    }
    if req.AuthMode != nil {
        policy.AuthMode = strings.ToLower(strings.TrimSpace(*req.AuthMode))
    }
    if req.CredentialID != nil {
        policy.CredentialID = strings.TrimSpace(*req.CredentialID)
    }
    if status, message := a.validateProxyCredential(r.Context(), p, policy.AuthMode, policy.CredentialID); message != "" {
        writeAPIError(w, status, message)
        return
    }
    if req.SourceCIDRs != nil {
        policy.SourceCIDRs = *req.SourceCIDRs
    }
    if req.TargetCIDRs != nil {
        policy.TargetCIDRs = *req.TargetCIDRs
    }
    if req.TargetPorts != nil {
        policy.TargetPorts = *req.TargetPorts
    }
    if req.AllowPrivateTargets != nil {
        policy.AllowPrivateTargets = req.AllowPrivateTargets
    }
    if req.MaxConcurrentTunnels != nil {
        policy.MaxConcurrentTunnels = *req.MaxConcurrentTunnels
    }
    if req.Description != nil {
        policy.Description = *req.Description
    }
    encoded, message := normalizeProxyRouteConfig(policy)
    if message != "" {
        writeAPIError(w, http.StatusBadRequest, message)
        return
    }
    next.Config = encoded
}
```

- 既有 `req.Config != nil || req.HostHeader != nil || ...` 的 upstream 归一化块必须包在 `if !isProxyRoute { ... }` 中，否则会用 `normalizeUpstreamRouteConfig` 覆盖 proxy 策略 JSON。

**(e) `internal/server/api.go`：`publicTunnel`**

```go
func publicTunnel(v storage.Tunnel) map[string]any {
    if v.Protocol == storage.ProtocolHTTPProxy {
        return publicProxyTunnel(v)
    }
    ... 既有实现不变 ...
}

// publicProxyTunnel renders a tp-* route. The sentinel target is exposed so the
// UI can show why targetHost/targetPort are not editable, and proxyURL is
// derived from the stored domain so it needs no server configuration.
func publicProxyTunnel(v storage.Tunnel) map[string]any {
    cfg, err := proxyRouteConfigOf(v.Config)
    if err != nil {
        cfg = proxyRouteConfig{AuthMode: proxyentry.AuthModeNone, AllowPrivateTargets: boolPtr(true)}
    }
    allowPrivate := true
    if cfg.AllowPrivateTargets != nil {
        allowPrivate = *cfg.AllowPrivateTargets
    }
    return map[string]any{
        "id": v.ID, "agentId": v.AgentID, "protocol": v.Protocol, "domain": v.Domain,
        "pathPrefix": v.PathPrefix, "targetHost": v.TargetHost, "targetPort": v.TargetPort,
        "publicPort": v.PublicPort, "status": v.Status,
        "proxyUrl": "https://" + v.Domain,
        "authMode": cfg.AuthMode, "credentialId": cfg.CredentialID,
        "sourceCIDRs": cfg.SourceCIDRs, "targetCIDRs": cfg.TargetCIDRs, "targetPorts": cfg.TargetPorts,
        "allowPrivateTargets": allowPrivate, "maxConcurrentTunnels": cfg.MaxConcurrentTunnels,
        "description": cfg.Description,
        "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt,
    }
}
```

`hostHeader` / `targetScheme` / `tlsServerName` 对 proxy 路由无意义，`publicProxyTunnel` 不返回这三个键（不要复用 `publicUpstreamRouteConfig`，否则会把哨兵 `"*"` 当成默认 HostHeader 与 TLSServerName 回显给前端）。

**(f) `internal/server/credential_api.go`**

- `credentialRequest` 加 `Username string \`json:"username"\``。
- `handleCredentialListCreate` 的 POST 分支：`input.Username = strings.TrimSpace(req.Username)`。
- `credentialPatchFromRequest`：`if strings.TrimSpace(req.Username) != "" { username := strings.TrimSpace(req.Username); patch.Username = &username }`。
- `credentialResponse` 加 `Username string \`json:"username"\``；`publicCredential` 设置 `Username: credentialUsername(v)`，其中

```go
// credentialUsername exposes the non-secret half of a proxy_basic credential.
// Every other type keeps publicKey as its only identifier.
func credentialUsername(v storage.Credential) string {
    if v.Type == storage.CredentialTypeProxyBasic {
        return v.PublicKey
    }
    return ""
}
```

- 凭据类型校验（`proxy_basic` 必须有 username 与 password、不得携带 privateKey/passphrase）由 Task 8 的 `validateCredentialShape` 负责，本任务只打通传输层，不重复实现。

- [ ] **Step 4: 同步 OpenAPI**

`docs/api/openapi.yaml`：

- `components.schemas.TunnelRequest`：`protocol` 的 enum 改为 `[http, websocket, http-proxy]`；`required` 改为 `[agentId]` 并在 `targetHost`/`targetPort` 的 `description` 里写明“`http-proxy` 路由必须省略，服务端强制写入哨兵 `*` / `0`”；新增 `authMode`（enum `[none, basic]`, default `none`）、`credentialId`、`sourceCIDRs`（`type: array, items: {type: string}`）、`targetCIDRs`、`targetPorts`（`items: {type: integer, minimum: 1, maximum: 65535}`）、`allowPrivateTargets`（`type: boolean, default: true`）、`maxConcurrentTunnels`（`type: integer, minimum: 0`）、`description`（`maxLength: 256`）。
- `components.schemas.TunnelUpdateRequest`：同样把 `protocol` enum 加上 `http-proxy`，并新增上述 8 个字段（全部可选，语义为“省略即保持”）；在 `description` 中写明协议不得在 `http-proxy` 与其它值之间切换。
- `/api/v1/routes` 的 `post.responses` 增加 `'400': {description: Invalid proxy route policy, credential or domain shape}`；`/api/v1/routes/{routeId}` 的 `patch.responses` 增加 `'400': {description: Invalid field, or a protocol switch involving http-proxy}`。
- `post` 与 `patch` 的 `'409'` 响应 description 补一句 `http-proxy` 的域名互斥语义：一条 tp-* 域名只能对应一条路由，与同域名的路径级反代路由（任意 `pathPrefix`）冲突时返回 409 `route already exists`；大小写变体视为同一条。
- `components.schemas.CredentialCreateRequest` / `CredentialPatchRequest` / `Credential` 的 `type` enum 改为 `[ssh_public_key, password, proxy_basic]`；三者新增 `username: {type: string, maxLength: 255, description: proxy_basic only; stored in publicKey and never secret}`；`CredentialSecretInput.password` 的 description 补上 “Required for `password` and `proxy_basic`”。
- 新增一节说明 407/403/502/503/504 由代理入口（非 `/api/v1`）返回，指向 `docs/user-guide/http-proxy-entry.md` 的错误码表，避免读者以为管理 API 会返回 407。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/server/ ./internal/storage/ -count=1 && go test -race ./internal/server/ -count=1 && go vet ./internal/server/ && gofmt -l internal/ cmd/`
Expected: PASS，`gofmt -l` 无输出。特别确认既有 `TestAPIRouteUpstreamDomainAndTLSConfig`、`TestAPIAuthAndRBAC`、credential 相关测试与 managed route 测试全部不回归。
- `TestAPIProxyRouteRejectsDuplicateDomain` 与 `TestAPIProxyRouteRejectsDomainClaimedByReverseProxy` 必须 PASS；同时确认既有路由冲突用例（同域名同前缀 409、同域名不同前缀仍可创建）没有因为 `domainOnly` 参数而回归——反代路由的 `domainOnly` 恒为 false，语义与改动前完全一致。

- [ ] **Step 6: 复审确认无重复实现**

检查 `normalizeCIDRList` / `normalizePortList` / `credentialErrorStatus` 是否与仓库既有函数重复，并确认 `createTunnel` 里原来那段内联的域名冲突循环已被删除、只剩 `tunnelRouteConflict` 一个实现：

Run: `grep -rn "ParseCIDR\|normalizePorts\|sort.Ints" internal/server/ internal/routing/ internal/proxyentry/ | grep -v _test.go`
Expected: 若发现等价的既有实现，删除本任务新增的版本并改为复用；否则保留并在代码注释中说明为何不能复用（例如既有实现的错误文案不适合作为 400 响应）。

- [ ] **Step 7: Commit（需授权）**

```bash
git add internal/server/api.go internal/server/proxy_entry_routes.go internal/server/credential_api.go internal/server/api_proxy_route_test.go internal/server/credential_api_test.go docs/api/openapi.yaml
git commit -m "feat(api): manage tp proxy routes and proxy_basic credentials"
```

---

### Task 13: 管理后台（Routes.vue / Credentials.vue / i18n）

**Files:**

- Modify: `web/src/api/client.ts`（`ManagedRoute`、`ManagedRouteCreateInput`、`ManagedRouteUpdateInput`）
- Modify: `web/src/api/credentials.ts`（`CredentialType`、`CredentialInput`、`Credential`）
- Modify: `web/src/views/Routes.vue`（类型切换表单、ACL 编辑、凭据选择、列表列、使用说明抽屉）
- Modify: `web/src/views/Credentials.vue`（`proxy_basic` 类型与 username 字段）
- Modify: `web/src/i18n/messages/zh-CN.ts`、`web/src/i18n/messages/en-US.ts`（`schema.ts` 由 `typeof zhCN` 推导，不改）
- Modify: `web/src/tests/routes.spec.ts`、`web/src/tests/credentials-view.spec.ts`、`web/src/tests/i18n.spec.ts`
- Modify: `internal/server/web_dist`（Step 5 `npm run build` 后经 `scripts/verify-web-embed.sh` 校验的 Go embed 产物）
- Modify: `docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md`（Step 6 偏差回写）
- 不改: `web/src/tests/layout-overflow.spec.ts`（自动扫描 `src/views/*.vue` 的守卫，见下方约束）

**Interfaces:**

- Consumes: Task 12 的 API 响应字段（`proxyUrl`、`authMode`、`credentialId`、`sourceCIDRs`、`targetCIDRs`、`targetPorts`、`allowPrivateTargets`、`maxConcurrentTunnels`、`description`）、既有 `listCredentials`、`loadAgentsForSelection` / `filterAgents`（`web/src/views/token-form.ts`）、`DataState` / `PageHeader` 组件
- Produces: 无新导出符号；`ManagedRoute` 与 `ManagedRouteCreateInput` 扩展后的类型即前端契约。

约束：

- `web/src/tests/i18n.spec.ts` 第 44 行断言 `messageKeys(zhCN)` 与 `messageKeys(enUS)` 完全相等，新增键必须两个文件同时加，且键名逐字一致。
- `web/src/tests/layout-overflow.spec.ts` 会自动扫描 `src/views/*.vue`：新加的容器如果用 `display:grid` 必须显式声明 `grid-template-columns`；表格列沿用 `min-width` + `fixed="right"`，不得引入整页横向滚动条。
- 现有 `form` 是 `reactive` 对象，字段全部为标量；ACL 与端口列表用 `string[]` / `number[]` 会破坏 `resetCreateForm` 的逐字段赋值风格，因此新增 `form.sourceCIDRs: string`、`form.targetCIDRs: string`、`form.targetPorts: string` 三个**逗号分隔字符串**，提交前在 `submit()` 里解析成数组，与 `agentDetail` 策略表单已有的 CIDR/端口输入风格一致。

- [ ] **Step 1: 写失败测试**

`web/src/tests/routes.spec.ts` 追加：

```ts
it('supports the tp-* http proxy entry route type', () => {
  const source = readFileSync('src/views/Routes.vue', 'utf8')
  for (const marker of [
    'value="http-proxy"',
    "t('routes.protocolHttpProxy')",
    'form.authMode',
    "value=\"basic\"",
    'form.credentialId',
    'listCredentials',
    'proxy_basic',
    'form.sourceCIDRs',
    'form.allowPrivateTargets',
    'form.maxConcurrentTunnels',
    'proxyUrl',
    "t('routes.proxyUsageTitle')",
    'isProxyRoute',
    'proxyDomainPreview',
  ]) expect(source, marker).toContain(marker)
})

it('validates proxy route input before submitting', () => {
  const source = readFileSync('src/views/Routes.vue', 'utf8')
  expect(source).toContain("t('routes.proxyNameInvalid')")
  expect(source).toContain("t('routes.proxyCredentialRequired')")
  expect(source).toContain("t('routes.proxyCIDRInvalid')")
  expect(source).toContain("t('routes.proxyPortInvalid')")
  // The sentinel target is owned by the server, so the form must never send it.
  expect(source).toContain('buildProxyRouteInput')
  expect(source).not.toMatch(/targetHost:\s*'\*'/)
})

it('sends proxy routes without upstream-only fields', async () => {
  const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { id: 'r1' } }) })
  vi.stubGlobal('fetch', fetchMock)

  await createRoute({
    agentId: 'agent-1',
    domain: 'tp-demo.tm.example.com',
    pathPrefix: '/',
    protocol: 'http-proxy',
    targetHost: '',
    targetPort: 0,
    authMode: 'basic',
    credentialId: 'cred-1',
    sourceCIDRs: ['11.71.85.0/24'],
    targetCIDRs: [],
    targetPorts: [443],
    allowPrivateTargets: true,
    maxConcurrentTunnels: 8,
    description: 'demo',
  }, 'proxy-key')

  const call = fetchMock.mock.calls[0]
  const body = JSON.parse(call[1].body as string)
  expect(body.protocol).toBe('http-proxy')
  expect(body.authMode).toBe('basic')
  expect(body.credentialId).toBe('cred-1')
  expect(body.sourceCIDRs).toEqual(['11.71.85.0/24'])
  expect(body.targetPorts).toEqual([443])
  expect(body).not.toHaveProperty('hostHeader')
  expect(body).not.toHaveProperty('targetScheme')
  expect(body).not.toHaveProperty('tlsServerName')
  expect(body).not.toHaveProperty('config')
  vi.unstubAllGlobals()
})
```

`web/src/tests/credentials-view.spec.ts` 追加：

```ts
it('supports the proxy_basic credential type', () => {
  const source = readFileSync('src/views/Credentials.vue', 'utf8')
  for (const marker of [
    'value="proxy_basic"',
    "t('credentials.types.proxyBasic')",
    'form.username',
    "t('credentials.username')",
    "t('credentials.usernameRequired')",
  ]) expect(source, marker).toContain(marker)
  // A proxy_basic password is write-only like every other secret.
  expect(source).toContain('form.secretPassword')
  expect(source).not.toContain('credential.password')
})
```

`web/src/tests/i18n.spec.ts` 追加（沿用文件内既有的 `messageKeys` 与遍历两份 messages 的写法）：

```ts
it('translates every proxy entry key in both locales', () => {
  for (const locale of [zhCN, enUS]) {
    for (const key of [
      'protocolHttpProxy', 'proxyName', 'proxyNameHelp', 'proxyDomainPreview', 'proxyAuthMode',
      'proxyAuthNone', 'proxyAuthBasic', 'proxyCredential', 'proxyCreateCredential',
      'proxySourceCIDRs', 'proxySourceCIDRsHelp', 'proxyAllowAll', 'proxyTargetCIDRs',
      'proxyTargetPorts', 'proxyAllowPrivateTargets', 'proxyMaxConcurrentTunnels',
      'proxyDescription', 'proxyUrl', 'proxyCopy', 'proxyCopied', 'proxyUsage', 'proxyUsageTitle',
      'proxyActiveTunnelsHint', 'proxyTargetDynamic',
      'proxyNameInvalid', 'proxyCredentialRequired', 'proxyCIDRInvalid', 'proxyPortInvalid',
    ]) expect(Object.keys(locale.routes).sort(), key).toContainEqual(key)
    for (const key of ['proxyBasic', 'username', 'usernameRequired']) {
      expect(Object.keys(locale.credentials).sort(), key).toContainEqual(key)
    }
    expect(Object.keys(locale.credentials.types).sort()).toContainEqual('proxyBasic')
  }
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd web && npm test -- --run`
Expected: FAIL。新增三条 `routes.spec.ts` 用例与一条 `credentials-view.spec.ts`、一条 `i18n.spec.ts` 用例失败；`createRoute` 用例还会因 TypeScript 类型不接受 `authMode` 等字段而在 `npm run build` 阶段报类型错误。

- [ ] **Step 3: 最小实现**

**(a) `web/src/api/client.ts`**

```ts
export type ManagedRouteAuthMode = 'none' | 'basic'

export type ManagedRoute = {
  // ...既有字段保持不变...
  authMode?: ManagedRouteAuthMode
  credentialId?: string
  sourceCIDRs?: string[]
  targetCIDRs?: string[]
  targetPorts?: number[]
  allowPrivateTargets?: boolean
  maxConcurrentTunnels?: number
  description?: string
  // Read-only, derived server-side from the stored domain.
  proxyUrl?: string
}

export type ManagedRouteCreateInput = {
  agentId: string
  domain: string
  pathPrefix: string
  protocol: string
  targetHost: string
  targetPort: number
  hostHeader?: string
  targetScheme?: string
  tlsServerName?: string
  authMode?: ManagedRouteAuthMode
  credentialId?: string
  sourceCIDRs?: string[]
  targetCIDRs?: string[]
  targetPorts?: number[]
  allowPrivateTargets?: boolean
  maxConcurrentTunnels?: number
  description?: string
}
```

`ManagedRouteUpdateInput` 已是 `Partial<ManagedRouteCreateInput & { status: string }>`，无需改动。

**(b) `web/src/api/credentials.ts`**

```ts
export type CredentialType = 'ssh_public_key' | 'password' | 'proxy_basic'
```

`Credential` 追加 `username?: string`；`CredentialInput` 追加 `username?: string`。`listCredentials` 已支持 `type` 过滤，Routes.vue 用 `listCredentials({ type: 'proxy_basic', status: 'active', limit: 200 })` 取候选凭据。

**(c) `web/src/views/Routes.vue`**

- `protocol` 的 `el-select` 增加 `<el-option :label="t('routes.protocolHttpProxy')" value="http-proxy" />`。
- 新增 `const isProxyRoute = computed(() => form.protocol === 'http-proxy')`；模板中把 domain/path/target/hostHeader/targetScheme/tlsServerName 这些反代专属项包进 `v-if="!isProxyRoute"`，proxy 专属项包进 `v-if="isProxyRoute"`。
- proxy 表单项（顺序即渲染顺序）：
  1. 名称 `form.proxyName`（只填 `<name>` 部分），下方 `proxyDomainPreview` 实时预览 `tp-{{ form.proxyName }}.{{ proxyDomainSuffix }}`；`proxyDomainSuffix` 从任一条已有 proxy 路由的 `domain` 推导，推导不到时回退为把完整域名交给用户填写（此时预览隐藏，`form.domain` 直接可编辑）。这一回退必须在代码注释里写明原因：前端不读取服务端配置，避免为拿 suffix 新增一个配置接口。
  2. 出口 Agent 选择框：复用既有 `el-select` + `filterAgents`。
  3. 认证模式 `el-radio-group`：`none` / `basic`。
  4. 凭据选择 `el-select`（`v-if="form.authMode === 'basic'"`），选项来自 `listCredentials({ type: 'proxy_basic', status: 'active', limit: 200 })`，label 为 `name · username`；旁边一个 `el-button link` 跳 `/credentials`（`router.push`）用于新建。
  5. 源 IP ACL `el-input` + `field-help`，附一个 `el-button link` “放开全部” 一键填入 `0.0.0.0/0`。
  6. 目标 CIDR、目标端口（都可选）。
  7. `el-switch` 允许内网目标（默认开）。
  8. 并发上限 `el-input-number`（`:min="0"`）。
  9. 备注 `el-input type="textarea" :maxlength="256" show-word-limit`。
- `form` 追加字段：`proxyName: ''`、`authMode: 'none'`、`credentialId: ''`、`sourceCIDRs: ''`、`targetCIDRs: ''`、`targetPorts: ''`、`allowPrivateTargets: true`、`maxConcurrentTunnels: 0`、`description: ''`；`resetCreateForm()` 与 `openEdit()` 同步补齐（`openEdit` 把数组用 `join(', ')` 还原成字符串）。
- 校验函数 `validateProxyForm()`：名称正则 `/^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/`；`authMode === 'basic'` 时 `credentialId` 必填；CIDR 用 `parseCIDRList()` 逐条校验（正则 + `prefix <= 128`，IPv4 `<= 32`），非法时 `ElMessage.warning(t('routes.proxyCIDRInvalid'))`；端口用 `parsePortList()` 校验 1..65535。`validateForm()` 开头分流：`if (isProxyRoute.value) return validateProxyForm()`，且 proxy 分支不校验 `targetHost`。
- `buildProxyRouteInput()` 返回只含 proxy 字段的对象（不含 `hostHeader`/`targetScheme`/`tlsServerName`/`config`，`targetHost: ''`、`targetPort: 0`、`pathPrefix: '/'`）；`submit()` 按 `isProxyRoute` 选择 `buildProxyRouteInput()` 或既有 `input`。编辑态提交时用 `updateRoute(id, { ...buildProxyRouteInput(), status: form.status })`。
- 列表新增列（放在 `protocol` 之后、`target` 之前，全部带 `min-width`）：
  - 代理地址：`row.proxyUrl ?? ''`，配一个复制按钮（`navigator.clipboard.writeText` + `ElMessage.success(t('routes.proxyCopied'))`），仅 proxy 行渲染。
  - 认证模式：`row.authMode === 'basic' ? t('routes.proxyAuthBasic') : t('routes.proxyAuthNone')`，非 proxy 行显示 `-`。
  - ACL 条数：`(row.sourceCIDRs ?? []).length`。
  - `formatTarget()` 对 proxy 行返回 `t('routes.proxyTargetDynamic')`（值为“按请求动态决定”），不要显示 `*:0`。
- 使用说明抽屉：`el-drawer` + `el-descriptions`，内容包含 `proxyUrl`、macOS（`networksetup -setsecurewebproxy`）、Windows（Internet 选项 → 连接 → 局域网设置 → 使用代理服务器，勾选“对 HTTPS 使用相同代理”）、Chrome PAC（`function FindProxyForURL(url, host) { return "HTTPS tp-demo.tm.example.com:443"; }`）、curl（`curl -x https://tp-demo.tm.example.com --proxy-user demo:****** https://ifconfig.me`）四段示例，以及错误码对照表（403/407/502/503/504 → 稳定错误码）。抽屉只在 proxy 行的“详情”按钮上出现，按钮加在既有 `actions` 列里。
- 活跃隧道数列**不加**：`tunnelmesh_proxy_entry_tunnels_active` 只存在于 Prometheus，管理 API 未暴露该聚合；改为在抽屉里用 `t('routes.proxyActiveTunnelsHint')` 放一句“活跃隧道数见 Grafana「HTTP Proxy Entry」行的 Proxy tunnels active 面板”，避免为 UI 新增一个只读聚合接口。Row 与面板名保持英文字面量、中文界面也不翻译，取值以 Task 15 Step 3 写进 Dashboard 的 JSON 为准，否则用户按中文 Row 名在 Grafana 里找不到对应视图。（spec §10 列了该列，此处按“不新增无来源数据的列”收紧，并在 Step 6 记录该偏差。）

**(d) `web/src/views/Credentials.vue`**

- 类型 `el-select` 增加 `<el-option :label="t('credentials.types.proxyBasic')" value="proxy_basic" />`。
- `form` 追加 `username: ''`；`proxy_basic` 时渲染 username 输入框（`v-if="form.type === 'proxy_basic'"`），并把公钥/私钥/口令这些 SSH 专属项包进 `v-if="form.type !== 'proxy_basic'"`。
- `createCredential` / `updateCredential` 的入参加 `username: form.type === 'proxy_basic' ? form.username.trim() : ''`。
- 提交校验：`proxy_basic` 时 `username` 与 `secretPassword` 必填（编辑态 password 留空表示不变，沿用既有 `secretKept` 文案）。
- 列表 `publicKey` 列对 `proxy_basic` 显示 username（服务端已把 username 写进 `publicKey`），并在 `type` 列显示 `t('credentials.types.proxyBasic')`。
- 敏感输入清理：`clearSensitiveKeyInput()` 必须在 `proxy_basic` 分支同样被调用，保持 `credentials-view.spec.ts` 中 `>= 3` 次调用的既有断言不回归。

**(e) i18n**

`zh-CN.ts` 的 `routes` 对象追加（键名与 Step 1 测试逐字一致）：`protocolHttpProxy: 'HTTP 代理入口 (tp-)'`、`proxyName: '代理名称'`、`proxyNameHelp: '小写字母、数字与中划线，最长 32 字符；最终域名为 tp-<名称>.<后缀>'`、`proxyDomainPreview: '完整代理域名'`、`proxyAuthMode: '认证方式'`、`proxyAuthNone: '无认证'`、`proxyAuthBasic: '用户名密码'`、`proxyCredential: '代理凭据'`、`proxyCreateCredential: '新建凭据'`、`proxySourceCIDRs: '允许的来源 IP/网段'`、`proxySourceCIDRsHelp: '逗号分隔；默认拒绝所有来源，填 0.0.0.0/0 表示放开全部'`、`proxyAllowAll: '放开全部来源'`、`proxyTargetCIDRs: '目标网段限制（可选）'`、`proxyTargetPorts: '目标端口限制（可选）'`、`proxyAllowPrivateTargets: '允许访问内网目标'`、`proxyMaxConcurrentTunnels: '并发隧道上限'`、`proxyDescription: '备注'`、`proxyUrl: '代理地址'`、`proxyCopy: '复制'`、`proxyCopied: '代理地址已复制'`、`proxyUsage: '使用说明'`、`proxyUsageTitle: 'HTTP 代理入口使用说明'`、`proxyTargetDynamic: '按请求动态决定'`、`proxyNameInvalid: '代理名称只能包含小写字母、数字与中划线，且不超过 32 字符'`、`proxyCredentialRequired: '请选择代理凭据'`、`proxyCIDRInvalid: 'IP/网段格式无效'`、`proxyPortInvalid: '端口必须是 1-65535 的整数'`。

上面这段之外再追加一个键（Step 1 的 i18n 测试已经断言它）：`proxyActiveTunnelsHint: '活跃隧道数见 Grafana「HTTP Proxy Entry」行的 Proxy tunnels active 面板'`。`en-US.ts` 对应 `proxyActiveTunnelsHint: 'Active tunnels are on the Grafana dashboard, row "HTTP Proxy Entry", panel "Proxy tunnels active"'`。

`credentials` 对象追加：`types` 内加 `proxyBasic: '代理账号'`，外层加 `username: '用户名'`、`usernameRequired: '请输入用户名'`。

`en-US.ts` 按同样键名补英文（`protocolHttpProxy: 'HTTP proxy entry (tp-)'`、`proxyNameInvalid: 'Proxy name may contain lowercase letters, digits and hyphens, up to 32 characters'` 等），键集合必须与 zh-CN 完全一致。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd web && npm test -- --run`
Expected: PASS（含 `layout-overflow.spec.ts`、`i18n.spec.ts`、`credentials-view.spec.ts` 全部既有用例不回归）。

- [ ] **Step 5: 构建并同步嵌入产物**

Run: `cd web && npm run build && cd .. && ./scripts/verify-web-embed.sh`
Expected: 构建成功，`internal/server/web_dist` 已更新，嵌入校验脚本通过。

- [ ] **Step 6: 记录与 spec 的偏差**

在 `docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md` 第 10 节末尾追加一行说明：列表“活跃隧道数”列改为抽屉内指向 Grafana 面板（Row `HTTP Proxy Entry`、面板 `Proxy tunnels active`，与 Task 15 Step 3 的 Dashboard JSON 一致），原因是管理 API 未暴露该聚合，避免为纯展示新增只读接口；同时在第 17 节决策表补一行记录该收紧。

- [ ] **Step 7: Commit（需授权）**

```bash
git add web/src/api/client.ts web/src/api/credentials.ts web/src/views/Routes.vue web/src/views/Credentials.vue web/src/i18n/messages/zh-CN.ts web/src/i18n/messages/en-US.ts web/src/tests/routes.spec.ts web/src/tests/credentials-view.spec.ts web/src/tests/i18n.spec.ts internal/server/web_dist docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md
git commit -m "feat(web): manage tp proxy routes in the admin console"
```

---

### Task 14: OpenResty 产物（Lua 搬运层、conf 模板、镜像、E2E 冒烟）

**Files:**

- Create: `deploy/openresty/tunnelmesh_proxy_entry.lua`
- Create: `deploy/openresty/tunnelmesh-proxy.conf.example`
- Create: `deploy/openresty/Dockerfile.proxy-connect`
- Create: `deploy/openresty/README.md`
- Create: `deploy/openresty/openresty_artifacts_test.go`
- Create: `test/e2e/proxy-entry/lib/stub.mjs`
- Create: `test/e2e/proxy-entry/lib/harness.mjs`
- Create: `test/e2e/proxy-entry/run.mjs`
- Create: `test/e2e/proxy-entry/README.md`
- Modify: `deploy/README.md`（目录内容表、模板约定、发布归档说明）
- Modify: `docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md`（第 2 节补丁引用与构建顺序、第 17 节决策补充）

**Interfaces:**

- Consumes: Task 0 的 spike 结论（server 级 `access_by_lua_file` 能收到 CONNECT、`$connect_host`/`$connect_port` 可读、`ngx.exit(444)` 不补发响应）；Task 1 的 `config.Load(context.Background(), config.ConfigOptions{})` 默认值（`Server.ProxyEntry.Listen`、`RouteHeader`、`ClientIPHeader`、`ClientPortHeader`、`IdleTimeout`）
- Produces:
  - OpenResty -> Server 的头契约：`X-TunnelMesh-Route`（取自 SNI）、`X-TunnelMesh-Client-IP`、`X-TunnelMesh-Client-Port`；三者只由 OpenResty 写入，客户端同名头一律丢弃
  - Lua 渲染标记（行尾注释，部署与 E2E 按标记替换值，不改结构）：`-- __TM_INTERNAL_HOST__`、`-- __TM_INTERNAL_PORT__`、`-- __TM_READ_TIMEOUT_MS__`
  - conf 占位符：`__LISTEN__`、`__SERVER_NAME_REGEX__`、`__SSL_CERT__`、`__SSL_CERT_KEY__`、`__LUA_FILE__`、`__INTERNAL_UPSTREAM__`、`__EDGE_ALLOW__`
  - `deploy/openresty/openresty_artifacts_test.go`：把上述默认值与 Go 配置默认值绑成一个可执行契约
  - `test/e2e/proxy-entry/`：只验证 OpenResty 层的冒烟脚本，`TM_PROXY_E2E_NGINX=1` 且 docker 可用时才运行

**外部事实（已核实上游 README v0.0.7 与补丁源码，写产物前必须知道）**

| 事实 | 证据 | 影响 |
|---|---|---|
| `$connect_host`/`$connect_port` 由补丁注册进 nginx core 变量表 | `patch/proxy_connect_rewrite_102101.patch` 的 `src/http/ngx_http_variables.c` 段在 `ngx_http_core_variables[]` 头部新增两项 `ngx_http_variable_request` | Lua 直接读 `ngx.var.connect_host`，不自己解析请求行 |
| 补丁同时删掉了 nginx 对 CONNECT 的 405 拒绝 | 同补丁 `src/http/ngx_http_request.c` 段删除 `client sent CONNECT method` -> `NGX_HTTP_NOT_ALLOWED` 分支 | 只装模块不打补丁，CONNECT 一律 405 |
| `NGX_HTTP_PROXY_CONNECT` 宏由模块 config 定义 | 模块 `config` 末行 `have=NGX_HTTP_PROXY_CONNECT . auto/have` | 补丁与模块必须同时存在，`--add-module` 不可省 |
| CONNECT 跳过 location 匹配 | 同补丁 `src/http/ngx_http_core_module.c` 段：`ngx_http_update_location_config(r); r->phase_handler++; return NGX_AGAIN;` | `access_by_lua_file` 必须写在 server 级；location 级 `content_by_lua_block` 永远收不到 CONNECT |
| 模块不受 `proxy_pass`/`upstream` 影响，无法链式转发 | 模块 README：“Any `location {}` block, `upstream {}` block and any other standard backend/upstream directives, such as `proxy_pass`, do not impact the functionality of this module.” | 本设计不启用 `proxy_connect;` 指令，CONNECT 完全由 Lua 搬到内部入口 |
| HTTP/2 下不支持 CONNECT | 模块 README “Known Issues”：`In HTTP/2, the CONNECT method is not supported.` | conf 模板禁止 `http2`，ALPN 只协商 http/1.1 |
| OpenResty 构建顺序是先 configure 再 patch | 模块 README “Build OpenResty”：`./configure --add-module=...` -> `patch -d build/nginx-1.19.3/ -p 1 < ...` -> `make` | `configure` 才会解出 `build/nginx-<ver>/`，顺序反了补丁无处可打 |
| 版本与补丁必须成对 | 模块 README Compatibility：OpenResty 1.25.3（version 1.25.3.1）与 nginx 1.25.0~1.26.x 都用 `proxy_connect_rewrite_102101.patch`；模块最新 tag 为 `v0.0.7`（2024-08-18） | Dockerfile 默认 pin 这一组合，换版本必须同时换补丁名 |

- [ ] **Step 1: 写失败的产物一致性测试**

`deploy/openresty/openresty_artifacts_test.go`（与 `deploy/grafana/dashboard_schema_test.go` 同一套路：目录里只有测试文件，`go test ./deploy/...` 执行；缩进用 tab，与仓库其它 Go 文件一致）：

```go
package openresty_test

import (
    "context"
    "net"
    "os"
    "regexp"
    "strconv"
    "strings"
    "testing"
    "time"

    "github.com/tunnelmesh/tunnelmesh/internal/config"
)

// http2Directive matches the nginx directive only, never the prose that forbids
// it in the template header comment.
var http2Directive = regexp.MustCompile(`(?m)^\s*http2\s+on;`)

// readArtifact loads a shipped OpenResty artifact. Tests run with the package
// directory as working directory, so names are relative.
func readArtifact(t *testing.T, name string) string {
    t.Helper()
    b, err := os.ReadFile(name)
    if err != nil {
        t.Fatalf("read %s: %v", name, err)
    }
    return string(b)
}

// TestProxyEntryArtifactsMatchServerContract pins the values that break a
// deployment silently when only one side changes: the trusted header names, the
// internal entry address and the Lua read timeout. Lua and nginx cannot be unit
// tested from Go, but the constants they must agree with can.
func TestProxyEntryArtifactsMatchServerContract(t *testing.T) {
    defaults, err := config.Load(context.Background(), config.ConfigOptions{})
    if err != nil {
        t.Fatalf("load defaults: %v", err)
    }
    entry := defaults.Server.ProxyEntry
    host, port, err := net.SplitHostPort(entry.Listen)
    if err != nil {
        t.Fatalf("default listen %q: %v", entry.Listen, err)
    }

    lua := readArtifact(t, "tunnelmesh_proxy_entry.lua")
    conf := readArtifact(t, "tunnelmesh-proxy.conf.example")

    for _, header := range []string{entry.RouteHeader, entry.ClientIPHeader, entry.ClientPortHeader} {
        if !strings.Contains(lua, header) {
            t.Errorf("lua does not inject trusted header %q", header)
        }
        if !strings.Contains(conf, "proxy_set_header "+header+" ") {
            t.Errorf("conf example does not forward trusted header %q", header)
        }
    }

    wantReadTimeout := int64((entry.IdleTimeout + 30*time.Second) / time.Millisecond)
    for _, marker := range []string{
        `host = "` + host + `", -- __TM_INTERNAL_HOST__`,
        `port = ` + port + `, -- __TM_INTERNAL_PORT__`,
        `read_timeout_ms = ` + strconv.FormatInt(wantReadTimeout, 10) + `, -- __TM_READ_TIMEOUT_MS__`,
    } {
        if !strings.Contains(lua, marker) {
            t.Errorf("lua CONFIG drifted from the server defaults; want marker %q", marker)
        }
    }

    if http2Directive.MatchString(conf) {
        t.Error("the tp-* server block must not enable http2: ngx.req.socket(true) is unavailable on HTTP/2 downstreams and proxy_connect does not support CONNECT over HTTP/2")
    }
    if !strings.Contains(conf, "listen __LISTEN__ ssl;") {
        t.Error("conf example must keep the listen placeholder together with ssl")
    }
    // Proxy-Authorization is hop-by-hop: nginx drops it on the way upstream
    // unless the config re-adds it explicitly.
    if !strings.Contains(conf, "proxy_set_header Proxy-Authorization $http_proxy_authorization;") {
        t.Error("conf example must forward Proxy-Authorization explicitly, otherwise every non-CONNECT proxy request fails with 407")
    }
    if !strings.Contains(conf, "access_by_lua_file __LUA_FILE__;") {
        t.Error("conf example must run the Lua mover from the server-level access phase")
    }
    if !strings.Contains(conf, "lua_check_client_abort on;") {
        t.Error("conf example must enable lua_check_client_abort so an aborted client releases the tunnel")
    }
    for _, placeholder := range []string{"__LISTEN__", "__SERVER_NAME_REGEX__", "__SSL_CERT__", "__SSL_CERT_KEY__", "__LUA_FILE__", "__INTERNAL_UPSTREAM__", "__EDGE_ALLOW__"} {
        if !strings.Contains(conf, placeholder) {
            t.Errorf("conf example lost placeholder %q", placeholder)
        }
    }

    if !strings.Contains(lua, "settimeouts(") {
        t.Error("lua must set cosocket timeouts explicitly; the 60s default kills long tunnels")
    }
    if !strings.Contains(lua, "ngx.exit(444)") {
        t.Error("lua must finish tunnels with ngx.exit(444) so nginx closes the connection without appending its own response")
    }
    if !strings.Contains(lua, "ngx.on_abort") {
        t.Error("lua must register ngx.on_abort so a client disconnect closes the upstream socket immediately")
    }
    // The mover must stay policy-free: any of these means logic leaked out of Go.
    for _, forbidden := range []string{"proxy_basic", "ParseCIDR", "sha256", "ngx.shared", "credentials"} {
        if strings.Contains(lua, forbidden) {
            t.Errorf("lua must not carry policy logic; found %q", forbidden)
        }
    }
}

// TestProxyConnectDockerfilePinsCompatibleVersions guards the pairing rule from
// the upstream module README: an OpenResty release only builds with the patch
// listed for its nginx core, and that patch only takes effect when the module
// config defines NGX_HTTP_PROXY_CONNECT. Bumping one without the other yields a
// binary that answers 405 to every CONNECT.
func TestProxyConnectDockerfilePinsCompatibleVersions(t *testing.T) {
    dockerfile := readArtifact(t, "Dockerfile.proxy-connect")
    for _, want := range []string{
        "OPENRESTY_VERSION=1.25.3.1",
        "PROXY_CONNECT_VERSION=v0.0.7",
        "PROXY_CONNECT_PATCH=proxy_connect_rewrite_102101.patch",
        "--add-module=/build/ngx_http_proxy_connect_module",
    } {
        if !strings.Contains(dockerfile, want) {
            t.Errorf("Dockerfile.proxy-connect must pin %q", want)
        }
    }
    configureIdx := strings.Index(dockerfile, "./configure")
    patchIdx := strings.Index(dockerfile, "patch -d")
    makeIdx := strings.Index(dockerfile, "make -j")
    if configureIdx < 0 || patchIdx < 0 || makeIdx < 0 {
        t.Fatalf("Dockerfile must contain ./configure, patch -d and make -j")
    }
    if !(configureIdx < patchIdx && patchIdx < makeIdx) {
        t.Error("OpenResty build order must be ./configure -> patch -> make; configure is what unpacks build/nginx-<ver>/")
    }
    if !strings.Contains(dockerfile, "USER openresty:openresty") {
        t.Error("runtime stage must not run as root")
    }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./deploy/openresty/ -count=1`
Expected: FAIL，`read tunnelmesh_proxy_entry.lua: open tunnelmesh_proxy_entry.lua: no such file or directory`。

- [ ] **Step 3: 写 Lua 搬运层**

`deploy/openresty/tunnelmesh_proxy_entry.lua`（缩进 4 空格；文件里不得出现任何 ACL、密码、路由查询逻辑）：

```lua
-- TunnelMesh tp-* HTTP 代理入口的 CONNECT 搬运层。
--
-- 本文件只搬字节，不做任何策略判断。路由身份、源 IP ACL、Basic 认证、目标
-- 校验、并发限额、审计与指标全部由 tunnelmesh-server 的内部入口
-- （server.proxy_entry.listen，默认 127.0.0.1:8089）执行。把策略写进 Lua 会形成
-- 第二份权威，并与 Go 侧校验静默漂移。
--
-- 为什么必须是 server 级 access_by_lua_file：proxy_connect 补丁在
-- ngx_http_core_find_config_phase 里对 CONNECT 直接执行
-- ngx_http_update_location_config(r); r->phase_handler++; 跳过 location 匹配，
-- 所以 location 级的 content_by_lua_block 永远收不到 CONNECT。
--
-- CONFIG 表的默认值必须与 Server 配置默认值一致，由
-- deploy/openresty/openresty_artifacts_test.go 守护。部署时若内部入口地址或
-- idle_timeout 与默认值不同，只改这张表；行尾标记供部署脚本与 E2E 定位替换，
-- 不要删除标记本身。

local CONFIG = {
    host = "127.0.0.1", -- __TM_INTERNAL_HOST__
    port = 8089, -- __TM_INTERNAL_PORT__
    connect_timeout_ms = 10000,
    send_timeout_ms = 30000,
    -- Server 的 idle_timeout + 30s：让 Server 先判定空闲并关闭，Lua 只兜底。
    read_timeout_ms = 330000, -- __TM_READ_TIMEOUT_MS__
    route_header = "X-TunnelMesh-Route",
    client_ip_header = "X-TunnelMesh-Client-IP",
    client_port_header = "X-TunnelMesh-Client-Port",
    chunk_size = 65536,
    max_error_body = 8192,
}

-- 非 CONNECT 请求直接放行，由 server 块的 location / 走 proxy_pass。
if ngx.req.get_method() ~= "CONNECT" then
    return
end

local started = ngx.now()
local host = ngx.var.connect_host
local port = ngx.var.connect_port
local route = ngx.var.ssl_server_name
local client_ip = ngx.var.remote_addr
local client_port = ngx.var.remote_port

if not host or host == "" or not port or port == "" then
    -- 变量为空只有一种可能：内核没有打 proxy_connect 补丁（未打补丁时 nginx 在
    -- 解析阶段就对 CONNECT 回 405，根本走不到这里）。
    ngx.log(ngx.ERR, "tunnelmesh: CONNECT target missing; nginx lacks the proxy_connect patch")
    return ngx.exit(403)
end
if not route or route == "" then
    -- 路由身份只来自 SNI。没有 SNI 时不回退到 Host 头，否则客户端可以用一个
    -- Host 头冒充另一条路由。
    ngx.log(ngx.WARN, "tunnelmesh: CONNECT without SNI target=", host, ":", port)
    return ngx.exit(403)
end

local upstream = ngx.socket.tcp()
upstream:settimeouts(CONFIG.connect_timeout_ms, CONFIG.send_timeout_ms, CONFIG.read_timeout_ms)

-- 客户端断开时立刻释放上游 socket 与两条 pump 线程。lua_check_client_abort 必须
-- 为 on，否则要等到读超时（默认 330s）才回收，半开隧道会堆满 worker_connections。
local abort_ok, abort_err = pcall(ngx.on_abort, function()
    ngx.log(ngx.INFO, "tunnelmesh: client aborted route=", route, " target=", host, ":", port,
            " client=", client_ip, ":", client_port)
    pcall(function() upstream:close() end)
end)
if not abort_ok then
    ngx.log(ngx.WARN, "tunnelmesh: cannot register abort handler: ", tostring(abort_err))
end

local ok, err = upstream:connect(CONFIG.host, CONFIG.port)
if not ok then
    ngx.log(ngx.ERR, "tunnelmesh: dial internal entry failed: ", err)
    return ngx.exit(502)
end

-- 只写白名单头。绝不整体透传客户端请求头：客户端可以自带 X-TunnelMesh-Route
-- 冒充别的路由，也可以塞任意 hop-by-hop 头干扰 Server 解析。
local lines = {
    "CONNECT " .. host .. ":" .. port .. " HTTP/1.1",
    "Host: " .. host .. ":" .. port,
    CONFIG.route_header .. ": " .. route,
    CONFIG.client_ip_header .. ": " .. client_ip,
    CONFIG.client_port_header .. ": " .. client_port,
}
local proxy_auth = ngx.var.http_proxy_authorization
if proxy_auth and proxy_auth ~= "" then
    -- 原样透传，不解码不校验；认证结果由 Server 决定。日志里永远不打印它。
    table.insert(lines, 3, "Proxy-Authorization: " .. proxy_auth)
end

ok, err = upstream:send(table.concat(lines, "\r\n") .. "\r\n\r\n")
if not ok then
    ngx.log(ngx.ERR, "tunnelmesh: forward CONNECT failed: ", err)
    upstream:close()
    return ngx.exit(502)
end

local status_line
status_line, err = upstream:receive()
if not status_line then
    ngx.log(ngx.ERR, "tunnelmesh: read internal entry status failed: ", err)
    upstream:close()
    return ngx.exit(502)
end

local status = tonumber(status_line:match("^HTTP/1%.%d (%d%d%d)"))
if not status then
    ngx.log(ngx.ERR, "tunnelmesh: malformed internal entry status line")
    upstream:close()
    return ngx.exit(502)
end

-- 收集上游响应头。成功分支只需要状态码；失败分支要原样交还客户端，让 curl 与
-- 浏览器看到 Server 给的稳定错误码、Proxy-Authenticate 与 Retry-After。
local headers, content_length = {}, 0
while true do
    local line
    line, err = upstream:receive()
    if not line or line == "" then
        break
    end
    headers[#headers + 1] = line
    local name, value = line:match("^([^:]+):%s*(.*)$")
    if name and name:lower() == "content-length" then
        content_length = tonumber(value) or 0
    end
end

local downstream, derr = ngx.req.socket(true)
if not downstream then
    ngx.log(ngx.ERR, "tunnelmesh: cannot take raw downstream socket: ", derr)
    upstream:close()
    return ngx.exit(502)
end
downstream:settimeouts(CONFIG.connect_timeout_ms, CONFIG.send_timeout_ms, CONFIG.read_timeout_ms)

if status ~= 200 then
    local out = { status_line }
    for i = 1, #headers do
        out[#out + 1] = headers[i]
    end
    out[#out + 1] = ""
    out[#out + 1] = ""
    if content_length > 0 then
        local body
        body, err = upstream:receive(math.min(content_length, CONFIG.max_error_body))
        if body then
            out[#out + 1] = body
        end
    end
    local _, werr = downstream:send(table.concat(out, "\r\n"))
    if werr then
        ngx.log(ngx.WARN, "tunnelmesh: relay rejection to client failed: ", werr)
    end
    ngx.log(ngx.WARN, "tunnelmesh: internal entry rejected CONNECT route=", route,
            " target=", host, ":", port, " status=", status, " client=", client_ip)
    upstream:close()
    -- 444：nginx 立即关闭连接且不再补发响应，避免在已写出的原始响应之后又追加
    -- 一个 nginx 错误页。
    return ngx.exit(444)
end

local _, serr = downstream:send("HTTP/1.1 200 Connection Established\r\n\r\n")
if serr then
    ngx.log(ngx.ERR, "tunnelmesh: announce tunnel to client failed: ", serr)
    upstream:close()
    return ngx.exit(444)
end

local function pump(from, to, label, stat)
    while true do
        local data, rerr, partial = from:receiveany(CONFIG.chunk_size)
        if data then
            stat.bytes = stat.bytes + #data
            local _, werr = to:send(data)
            if werr then
                stat.reason = "send:" .. werr
                return
            end
        else
            -- 超时或关闭时 partial 里可能还有数据，先冲刷再结束。读超时已经比
            -- Server 的 idle_timeout 长 30s，走到这里说明对端不再发数据。
            if partial and #partial > 0 then
                stat.bytes = stat.bytes + #partial
                local _, werr = to:send(partial)
                if werr then
                    stat.reason = "send:" .. werr
                    return
                end
            end
            stat.reason = label .. ":" .. tostring(rerr or "eof")
            return
        end
    end
end

local to_upstream = { bytes = 0 }   -- 客户端 -> 内部入口 -> agent -> 目标
local to_client = { bytes = 0 }     -- 目标 -> agent -> 内部入口 -> 客户端

local push = ngx.thread.spawn(pump, downstream, upstream, "downstream", to_upstream)
local pull = ngx.thread.spawn(pump, upstream, downstream, "upstream", to_client)

-- 任一方向结束就收尾。ngx.thread.kill 让另一条线程的 cosocket 操作立刻失败，
-- 否则半开隧道会一直占着 worker connection 直到读超时。
local thread_ok, thread_err = ngx.thread.wait(push, pull)
pcall(ngx.thread.kill, push)
pcall(ngx.thread.kill, pull)
upstream:close()

-- 只输出 route/target/client/字节数/时长/结束原因。Proxy-Authorization 是凭据，
-- 任何情况下都不得进入 error.log。
ngx.log(ngx.INFO, "tunnelmesh: tunnel closed route=", route, " target=", host, ":", port,
        " client=", client_ip, ":", client_port,
        " upstream_bytes=", to_upstream.bytes, " downstream_bytes=", to_client.bytes,
        " duration=", string.format("%.3f", ngx.now() - started),
        " reason=", thread_ok and tostring(to_upstream.reason or to_client.reason)
                               or ("thread:" .. tostring(thread_err)))

return ngx.exit(444)
```

实现约束（复审时逐条对照）：

- 每一条 `return` 路径都必须先 `upstream:close()`（`ngx.exit` 之前），或依赖 `ngx.on_abort`；泄漏的上游连接表现为 Server 侧隧道数持续上涨。
- 不得调用 `ngx.print` / `ngx.say` / `ngx.header[...]`：拿到 raw downstream socket 之后响应已经由本文件手写，混用会产生第二个响应头。
- `pump` 里对 `send` 的错误必须立即结束循环，否则对端已关闭时会忙轮询打满 CPU。
- 日志级别：正常结束用 `ngx.INFO`（部署文档要求 error_log 至少 info 才能看到隧道统计），拒绝与错误用 `ngx.WARN`/`ngx.ERR`。

- [ ] **Step 4: 写 conf 模板**

`deploy/openresty/tunnelmesh-proxy.conf.example`（缩进 4 空格，与 `docs/deployment/nginx.md` 的既有示例一致）：

```nginx
# TunnelMesh tp-* HTTP 代理入口 server 块模板。
#
# 这是模板而不是可以直接 include 的配置：占位符必须由部署者渲染，含义、生产渲染
# 示例与验证步骤见 docs/deployment/openresty-proxy-entry.md。渲染后放进 http{}
# 内，与现有 admin / 托管路由 server 块并列；两块共用 443，由 SNI 选择。
#
# 占位符：
#   __LISTEN__             监听地址。生产写 443；本地验证写 127.0.0.1:18443。
#   __SERVER_NAME_REGEX__  tp-* 主机名正则，生产渲染示例：
#                          ~^tp-[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?\.tm\.example\.com$
#   __SSL_CERT__           证书路径，必须覆盖 *.<domain_suffix>
#   __SSL_CERT_KEY__       私钥路径
#   __LUA_FILE__           tunnelmesh_proxy_entry.lua 的绝对路径
#   __INTERNAL_UPSTREAM__  必须等于 server.proxy_entry.listen，默认 127.0.0.1:8089
#   __EDGE_ALLOW__         粗粒度来源白名单，生产渲染示例：
#                          allow 10.0.0.0/8; allow 11.0.0.0/8; deny all;
#
# 两条硬约束（由 deploy/openresty/openresty_artifacts_test.go 守护）：
#   1. 本 server 块禁止 h2。ngx.req.socket(true) 在 HTTP/2 下游不可用，
#      proxy_connect 模块的 Known Issues 也明确不支持 HTTP/2 的 CONNECT。
#      现有 admin server 块用 per-server 的 `http2 on;`，两块互不影响
#      （要求 OpenResty 基于 nginx >= 1.25.1，Task 0 已验证）。
#   2. Proxy-Authorization 是 hop-by-hop 头，nginx 默认不转发给上游；
#      location / 里少这一行，非 CONNECT 的 HTTP 代理请求会全部 407。
#
# worker_shutdown_timeout 300s; 属于 main 上下文，写在 nginx.conf 而不是这里：
# reload 时给在途隧道留排空时间，否则发布瞬间所有代理连接被立刻掐断。

upstream tunnelmesh_proxy_entry {
    server __INTERNAL_UPSTREAM__;
    keepalive 32;
}

server {
    listen __LISTEN__ ssl;
    server_name __SERVER_NAME_REGEX__;

    ssl_certificate     __SSL_CERT__;
    ssl_certificate_key __SSL_CERT_KEY__;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_session_cache   shared:TLS:10m;

    # 粗粒度前置，只挡明显的公网扫描；按路由的细粒度 ACL 在 Server 侧执行。
    __EDGE_ALLOW__

    # 客户端断开时立刻结束 Lua 请求，避免半开隧道占用 worker connection。
    lua_check_client_abort on;
    access_by_lua_file __LUA_FILE__;

    # 非 CONNECT（绝对形式）请求。nginx 会把绝对形式改写成 origin-form 再交给
    # location，所以 Server 侧从 Host 头恢复目标（见 splitProxyTarget）。
    location / {
        proxy_pass http://tunnelmesh_proxy_entry;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $http_host;
        proxy_set_header Proxy-Authorization $http_proxy_authorization;
        proxy_set_header X-TunnelMesh-Route $ssl_server_name;
        proxy_set_header X-TunnelMesh-Client-IP $remote_addr;
        proxy_set_header X-TunnelMesh-Client-Port $remote_port;
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_connect_timeout 10s;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }
}
```

- [ ] **Step 5: 写 Dockerfile**

`deploy/openresty/Dockerfile.proxy-connect`（风格对齐仓库根 `Dockerfile`：`# syntax`、ARG pin 版本、多阶段、非 root）：

```dockerfile
# syntax=docker/dockerfile:1.7
#
# OpenResty + ngx_http_proxy_connect_module 补丁内核，供 tp-* HTTP 代理入口使用。
#
# 为什么必须自己构建：没有补丁时 nginx 在 ngx_http_process_request_header 里直接
# 对 CONNECT 回 405，$connect_host / $connect_port 这两个变量也由补丁注册进
# nginx core variables。模块与补丁来自同一个 tag，缺一都会让 CONNECT 失效。
#
# 版本必须成对选择：模块 README 的 Compatibility 表列出了 OpenResty 版本与补丁
# 文件的对应关系（1.25.3.1 -> proxy_connect_rewrite_102101.patch）。换版本前先读
# https://github.com/chobits/ngx_http_proxy_connect_module#select-patch ，
# 并同步 deploy/openresty/openresty_artifacts_test.go 里的 pin 断言。

ARG ALPINE_VERSION=3.20
ARG OPENRESTY_VERSION=1.25.3.1
ARG PROXY_CONNECT_VERSION=v0.0.7
ARG PROXY_CONNECT_PATCH=proxy_connect_rewrite_102101.patch

FROM alpine:${ALPINE_VERSION} AS build
ARG OPENRESTY_VERSION
ARG PROXY_CONNECT_VERSION
ARG PROXY_CONNECT_PATCH
RUN apk add --no-cache build-base perl linux-headers openssl-dev pcre2-dev zlib-dev \
        bash patch wget
WORKDIR /build
RUN wget -qO openresty.tar.gz "https://openresty.org/download/openresty-${OPENRESTY_VERSION}.tar.gz" \
    && tar -xzf openresty.tar.gz
RUN wget -qO proxy-connect.tar.gz \
        "https://github.com/chobits/ngx_http_proxy_connect_module/archive/refs/tags/${PROXY_CONNECT_VERSION}.tar.gz" \
    && tar -xzf proxy-connect.tar.gz \
    && mv "ngx_http_proxy_connect_module-${PROXY_CONNECT_VERSION#v}" ngx_http_proxy_connect_module
WORKDIR /build/openresty-${OPENRESTY_VERSION}
# 替换生产 OpenResty 之前，先在目标机执行 nginx -V，把输出里的 --with-* 参数补进
# 下面这条 configure，否则现有 server 块可能因为缺模块起不来。E2E 只需要 ssl。
RUN ./configure \
        --prefix=/usr/local/openresty \
        --with-http_ssl_module \
        --with-http_v2_module \
        --with-http_realip_module \
        --with-http_gzip_static_module \
        --with-pcre-jit \
        --add-module=/build/ngx_http_proxy_connect_module \
        -j"$(nproc)" \
    && patch -d "build/nginx-${OPENRESTY_VERSION%.*}" -p1 \
        < "/build/ngx_http_proxy_connect_module/patch/${PROXY_CONNECT_PATCH}" \
    && make -j"$(nproc)" \
    && make install \
    && /usr/local/openresty/nginx/sbin/nginx -V 2>&1 | grep -q proxy_connect

FROM alpine:${ALPINE_VERSION} AS runtime
LABEL org.opencontainers.image.title="TunnelMesh OpenResty (proxy_connect)" \
      org.opencontainers.image.description="OpenResty with the ngx_http_proxy_connect_module patch for the tp-* HTTP proxy entry" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="https://github.com/nnworld/TunnelMesh"
RUN apk add --no-cache libgcc openssl pcre2 zlib \
    && addgroup -S openresty && adduser -S -G openresty openresty
COPY --from=build /usr/local/openresty /usr/local/openresty
# 非 root 运行需要让 openresty 用户可写 logs 与各类 temp 目录；生产环境若沿用
# 宿主机既有的 OpenResty 用户，按实际情况调整 chown 目标。
RUN mkdir -p /var/run/openresty \
    && chown -R openresty:openresty /usr/local/openresty/nginx /var/run/openresty
ENV PATH=/usr/local/openresty/bin:/usr/local/openresty/nginx/sbin:${PATH}
# 443 需要 root 或 cap_net_bind_service，因此本镜像默认非 root 时只适合监听
# 1024 以上端口（E2E 用 18443）。要在容器里直接监听 443，改用 root 运行或
# setcap cap_net_bind_service=ep /usr/local/openresty/nginx/sbin/nginx。
EXPOSE 443
USER openresty:openresty
ENTRYPOINT ["/usr/local/openresty/nginx/sbin/nginx"]
CMD ["-g", "daemon off;"]
```

构建与验证（写进 `deploy/openresty/README.md`，不在计划里执行）：

```bash
docker build -f deploy/openresty/Dockerfile.proxy-connect -t tunnelmesh/openresty-proxy-connect:1.25.3.1 .
docker run --rm tunnelmesh/openresty-proxy-connect:1.25.3.1 -V 2>&1 | tr ' ' '\n' | grep proxy_connect
```

中止判据：若 `docker build` 在当前环境不可用（无 docker、无外网拉取 openresty.org 与 github.com），本步骤降级为“手工步骤”，在 `deploy/openresty/README.md` 标注镜像未经本机构建验证，并让 Step 12 的 E2E 走 skip 分支；不得因为镜像构建失败而修改 Lua 或 conf 模板的设计。

- [ ] **Step 6: 写 `deploy/openresty/README.md`**

必须包含（与 `deploy/README.md` 的“产物在这里、说明在 docs”分工一致，本文件只讲产物本身，操作步骤指向 docs）：

- 四个文件各自用途：`tunnelmesh_proxy_entry.lua`（搬运层，无策略）、`tunnelmesh-proxy.conf.example`（server 块模板）、`Dockerfile.proxy-connect`（补丁内核镜像）、`openresty_artifacts_test.go`（产物与 Go 配置默认值的一致性契约）。
- 占位符与 Lua 渲染标记两张表，取值来源分别是 `server.proxy_entry.*` 配置项与通配证书路径。
- 构建镜像、`nginx -V` 校验补丁、`nginx -t` 校验渲染结果的三条命令。
- 一句话指向 [OpenResty 代理入口部署](../../docs/deployment/openresty-proxy-entry.md) 与 [E2E 冒烟](../../test/e2e/proxy-entry/README.md)。
- 明确声明：本目录不进 `scripts/build-release.sh` 的发布归档（与 `prometheus/`、`grafana/` 同属运维自行挂载的产物）。

- [ ] **Step 7: 运行产物测试确认通过**

Run: `go test ./deploy/... -count=1 && go vet ./deploy/... && gofmt -l deploy/`
Expected: PASS，`gofmt -l` 无输出；`deploy/grafana` 既有测试不回归。

- [ ] **Step 8: 写 E2E 的内部入口替身**

`test/e2e/proxy-entry/lib/stub.mjs`（Node 20+，无 npm 依赖，风格对齐 `test/e2e/webssh/lib/`）：

```js
// Scripted stand-in for the tunnelmesh-server internal proxy entry.
//
// 这不是 tunnelmesh-server：Server 的策略与转发已由 Task 9-12 的 Go 测试覆盖。
// 这里只需要一个行为可预期的回声入口，用来断言 OpenResty 搬运层做对了三件事：
// 请求头白名单、CONNECT 双向 splice、非 200 响应原样透传。
//
// 编排方式是 CONNECT 目标主机名（绝对形式则看 Host 头），不是请求头：Lua 只透传
// 白名单头，用请求头编排根本传不进来——而这正是要验证的行为。
import http from 'node:http'

const SCRIPTS = {
  'denied.test': { status: 403, reason: 'Forbidden', code: 'proxy_source_denied' },
  'authfail.test': {
    status: 407,
    reason: 'Proxy Authentication Required',
    code: 'proxy_auth_failed',
    extra: ['Proxy-Authenticate: Basic realm="TunnelMesh", charset="UTF-8"'],
  },
  'capacity.test': {
    status: 503,
    reason: 'Service Unavailable',
    code: 'proxy_capacity_exhausted',
    extra: ['Retry-After: 5'],
  },
}

const scriptFor = (host) => SCRIPTS[String(host || '').split(':')[0].toLowerCase()] || null
const envelope = (status, code) => JSON.stringify({ code: status, msg: code, data: null })

export function startStub(port, address = '0.0.0.0') {
  const seen = []

  const server = http.createServer((req, res) => {
    const record = { kind: 'http', method: req.method, url: req.url, headers: req.headers }
    seen.push(record)
    const script = scriptFor(req.headers.host)
    const body = script ? envelope(script.status, script.code) : JSON.stringify({ url: req.url, host: req.headers.host })
    const headers = { 'content-type': 'application/json' }
    for (const line of script?.extra || []) {
      const idx = line.indexOf(': ')
      headers[line.slice(0, idx).toLowerCase()] = line.slice(idx + 2)
    }
    res.writeHead(script ? script.status : 200, headers)
    res.end(body)
  })

  server.on('connect', (req, socket, head) => {
    const record = { kind: 'connect', url: req.url, headers: req.headers, bytesUp: 0, bytesDown: 0, closed: false }
    seen.push(record)
    socket.on('close', () => { record.closed = true })
    socket.on('error', () => {})

    const script = scriptFor(req.url)
    if (script) {
      const body = envelope(script.status, script.code)
      const lines = [
        `HTTP/1.1 ${script.status} ${script.reason}`,
        ...(script.extra || []),
        'content-type: application/json',
        `content-length: ${Buffer.byteLength(body)}`,
        'cache-control: no-store',
        '',
        body,
      ]
      socket.end(lines.join('\r\n'))
      return
    }

    socket.write('HTTP/1.1 200 Connection Established\r\n\r\n')
    if (head && head.length) {
      record.bytesDown += head.length
      socket.write(head)
    }
    // 回声：客户端发来的每个字节原样送回，用来验证双向 splice 与字节完整性。
    socket.on('data', (chunk) => {
      record.bytesUp += chunk.length
      socket.write(chunk, () => { record.bytesDown += chunk.length })
    })
  })

  return new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(port, address, () => resolve({
      port: server.address().port,
      seen,
      find: (predicate) => seen.find(predicate),
      reset: () => { seen.length = 0 },
      close: () => new Promise((done) => { server.closeAllConnections?.(); server.close(done) }),
    }))
  })
}
```

- [ ] **Step 9: 写 E2E 的 OpenResty 运行时**

`test/e2e/proxy-entry/lib/harness.mjs`：

```js
// OpenResty runtime for the tp-* proxy entry smoke test.
//
// 渲染的对象就是 deploy/openresty/ 下的真实产物：conf 按占位符替换，Lua 按行尾
// 标记替换值。测试因此覆盖发布物本身，而不是一份手抄副本。
import { execFileSync, spawn } from 'node:child_process'
import fs from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
export const repoRoot = path.resolve(here, '..', '..', '..', '..')

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const number = (value, fallback) => (Number.isFinite(Number(value)) && value !== '' && value != null ? Number(value) : fallback)
const runs = (cmd, args) => { try { execFileSync(cmd, args, { stdio: 'ignore' }); return true } catch { return false } }

/** resolveConfig merges environment overrides onto safe local defaults. */
export function resolveConfig(env = process.env) {
  const root = env.TM_PROXY_E2E_REPO || repoRoot
  const workDir = env.TM_PROXY_E2E_DIR || path.join(os.tmpdir(), 'tunnelmesh-proxy-entry-e2e')
  const domainSuffix = env.TM_PROXY_E2E_DOMAIN || 'proxy.test'
  const routeName = env.TM_PROXY_E2E_ROUTE || 'e2e'
  return {
    enabled: env.TM_PROXY_E2E_NGINX === '1',
    repoRoot: root,
    workDir,
    listenPort: number(env.TM_PROXY_E2E_PORT, 18443),
    stubPort: number(env.TM_PROXY_E2E_STUB_PORT, 18089),
    image: env.TM_PROXY_E2E_IMAGE || 'tunnelmesh/openresty-proxy-connect:1.25.3.1',
    skipBuild: env.TM_PROXY_E2E_SKIP_BUILD === '1',
    containerName: env.TM_PROXY_E2E_CONTAINER || `tunnelmesh-proxy-entry-e2e-${process.pid}`,
    domainSuffix,
    routeName,
    proxyHost: `tp-${routeName}.${domainSuffix}`,
    artifacts: {
      lua: path.join(root, 'deploy/openresty/tunnelmesh_proxy_entry.lua'),
      conf: path.join(root, 'deploy/openresty/tunnelmesh-proxy.conf.example'),
      dockerfile: path.join(root, 'deploy/openresty/Dockerfile.proxy-connect'),
    },
  }
}

/** missingPrerequisites lists everything that would make the run impossible. */
export function missingPrerequisites(config) {
  const missing = []
  if (!runs('docker', ['info'])) missing.push('docker CLI with a reachable daemon')
  if (!runs('openssl', ['version'])) missing.push('openssl')
  for (const [name, file] of Object.entries(config.artifacts)) {
    if (!fs.existsSync(file)) missing.push(`${name} artifact ${file}`)
  }
  return missing
}

export function buildImage(config, log) {
  if (config.skipBuild) {
    log(`TM_PROXY_E2E_SKIP_BUILD=1; using image ${config.image} as-is`)
    return
  }
  log(`building ${config.image} (the first build compiles OpenResty and takes minutes)`)
  execFileSync('docker', ['build', '-f', config.artifacts.dockerfile, '-t', config.image, config.repoRoot], { stdio: 'inherit' })
}

export function renderArtifacts(config, log) {
  fs.rmSync(config.workDir, { recursive: true, force: true })
  fs.mkdirSync(config.workDir, { recursive: true })

  execFileSync('openssl', [
    'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
    '-keyout', path.join(config.workDir, 'tls.key'),
    '-out', path.join(config.workDir, 'tls.crt'),
    '-subj', `/CN=${config.proxyHost}`,
    '-addext', `subjectAltName=DNS:${config.proxyHost},DNS:*.${config.domainSuffix}`,
  ], { stdio: 'ignore' })

  // 容器里的 OpenResty 通过 host.docker.internal 回到宿主机上的 stub。
  const lua = fs.readFileSync(config.artifacts.lua, 'utf8')
    .replace(/host = "[^"]*", -- __TM_INTERNAL_HOST__/, 'host = "host.docker.internal", -- __TM_INTERNAL_HOST__')
    .replace(/port = \d+, -- __TM_INTERNAL_PORT__/, `port = ${config.stubPort}, -- __TM_INTERNAL_PORT__`)
  fs.writeFileSync(path.join(config.workDir, 'tunnelmesh_proxy_entry.lua'), lua)

  const escapedSuffix = config.domainSuffix.replaceAll('.', '\\.')
  const conf = fs.readFileSync(config.artifacts.conf, 'utf8')
    .replaceAll('__LISTEN__', String(config.listenPort))
    .replaceAll('__SERVER_NAME_REGEX__', `~^tp-[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?\\.${escapedSuffix}$`)
    .replaceAll('__SSL_CERT__', '/etc/tunnelmesh/tls.crt')
    .replaceAll('__SSL_CERT_KEY__', '/etc/tunnelmesh/tls.key')
    .replaceAll('__LUA_FILE__', '/etc/tunnelmesh/tunnelmesh_proxy_entry.lua')
    .replaceAll('__INTERNAL_UPSTREAM__', `host.docker.internal:${config.stubPort}`)
    .replaceAll('__EDGE_ALLOW__', 'allow all;')
  fs.writeFileSync(path.join(config.workDir, 'tunnelmesh-proxy.conf'), conf)

  // 这份 wrapper 是测试夹具而不是仓库产物：生产环境把渲染后的 server 块并进既有
  // nginx.conf。日志走 stdout/stderr 并由 harness 落成 container.log，挂载目录
  // 因此可以保持只读。
  fs.writeFileSync(path.join(config.workDir, 'nginx.conf'), [
    'worker_processes 1;',
    'error_log /dev/stderr info;',
    'pid /tmp/nginx.pid;',
    'events { worker_connections 256; }',
    'http {',
    '    access_log /dev/stdout;',
    '    client_body_temp_path /tmp/nginx-client-body;',
    '    proxy_temp_path /tmp/nginx-proxy;',
    '    fastcgi_temp_path /tmp/nginx-fastcgi;',
    '    uwsgi_temp_path /tmp/nginx-uwsgi;',
    '    scgi_temp_path /tmp/nginx-scgi;',
    '    include /etc/tunnelmesh/tunnelmesh-proxy.conf;',
    '}',
    '',
  ].join('\n'))

  log(`rendered artifacts into ${config.workDir}`)
}

export function startNginx(config, log) {
  const logFile = path.join(config.workDir, 'container.log')
  const out = fs.openSync(logFile, 'a')
  const child = spawn('docker', [
    'run', '--rm', '--name', config.containerName,
    '-p', `127.0.0.1:${config.listenPort}:${config.listenPort}`,
    '-v', `${config.workDir}:/etc/tunnelmesh:ro`,
    '--add-host', 'host.docker.internal:host-gateway',
    config.image,
    '-c', '/etc/tunnelmesh/nginx.conf', '-g', 'daemon off;',
  ], { stdio: ['ignore', out, out] })
  child.on('exit', (code) => log(`nginx container exited with code ${code}`))
  return {
    child,
    logFile,
    tail: (lines = 80) => {
      try { return fs.readFileSync(logFile, 'utf8').split('\n').slice(-lines).join('\n') } catch { return '' }
    },
    stop: async () => {
      try { execFileSync('docker', ['rm', '-f', config.containerName], { stdio: 'ignore' }) } catch { /* already gone */ }
      await sleep(200)
      try { fs.closeSync(out) } catch { /* already closed */ }
    },
  }
}

export async function waitListening(port, timeoutMs = 60000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const ok = await new Promise((resolve) => {
      const probe = net.connect(port, '127.0.0.1')
      probe.once('connect', () => { probe.destroy(); resolve(true) })
      probe.once('error', () => { probe.destroy(); resolve(false) })
    })
    if (ok) return true
    await sleep(300)
  }
  return false
}
```

- [ ] **Step 10: 写 E2E 场景脚本**

`test/e2e/proxy-entry/run.mjs`：

```js
// tp-* HTTP proxy entry: OpenResty layer smoke test.
//
// 只验证 OpenResty 搬运层（deploy/openresty/ 的产物）。Server 的路由解析、ACL、
// 认证、目标校验、限额与转发由 internal/proxyentry 与 internal/server 的 Go 测试
// 覆盖，这里不重复。
//
// 用 Node 的 tls 模块而不是 curl：路由身份来自 SNI，Node 可以在连 127.0.0.1 的
// 同时指定 servername，不必改 /etc/hosts，也不依赖各版本 curl 对 https 代理的
// --resolve 行为。
//
// 运行条件与用法见同目录 README.md。
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import tls from 'node:tls'
import { buildImage, missingPrerequisites, renderArtifacts, resolveConfig, startNginx, waitListening } from './lib/harness.mjs'
import { startStub } from './lib/stub.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (...args) => console.log('[e2e]', ...args)

const config = resolveConfig()
const results = []
const check = (name, ok, detail) => {
  results.push({ name, ok: !!ok, detail })
  log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` :: ${detail}` : ''}`)
}

const credential = `Basic ${Buffer.from('e2e:s3cret').toString('base64')}`

function connectTLS() {
  return new Promise((resolve, reject) => {
    const socket = tls.connect({
      host: '127.0.0.1',
      port: config.listenPort,
      servername: config.proxyHost,
      rejectUnauthorized: false,
      ALPNProtocols: ['http/1.1'],
    })
    socket.once('secureConnect', () => resolve(socket))
    socket.once('error', reject)
  })
}

// readHead consumes everything up to the blank line that ends the proxy
// response head and hands back whatever body bytes rode along in the same read.
function readHead(socket, timeoutMs = 15000) {
  return new Promise((resolve, reject) => {
    const chunks = []
    const timer = setTimeout(() => { cleanup(); reject(new Error('timeout waiting for the proxy response head')) }, timeoutMs)
    const onData = (chunk) => {
      chunks.push(chunk)
      const buf = Buffer.concat(chunks)
      const idx = buf.indexOf('\r\n\r\n')
      if (idx < 0) return
      cleanup()
      resolve({ head: buf.subarray(0, idx).toString('utf8'), rest: buf.subarray(idx + 4) })
    }
    const onError = (err) => { cleanup(); reject(err) }
    const cleanup = () => { clearTimeout(timer); socket.off('data', onData); socket.off('error', onError) }
    socket.on('data', onData)
    socket.on('error', onError)
  })
}

function readBytes(socket, count, timeoutMs = 20000) {
  return new Promise((resolve, reject) => {
    const chunks = []
    let total = 0
    const timer = setTimeout(() => { cleanup(); reject(new Error(`timeout after ${total}/${count} bytes`)) }, timeoutMs)
    const onData = (chunk) => {
      chunks.push(chunk)
      total += chunk.length
      if (total >= count) { cleanup(); resolve(Buffer.concat(chunks).subarray(0, count)) }
    }
    const onError = (err) => { cleanup(); reject(err) }
    const cleanup = () => { clearTimeout(timer); socket.off('data', onData); socket.off('error', onError) }
    socket.on('data', onData)
    socket.on('error', onError)
  })
}

const parseHead = (head) => {
  const [statusLine, ...lines] = head.split('\r\n')
  const headers = {}
  for (const line of lines) {
    const idx = line.indexOf(':')
    if (idx > 0) headers[line.slice(0, idx).trim().toLowerCase()] = line.slice(idx + 1).trim()
  }
  return { statusLine, status: Number(statusLine.split(' ')[1]), headers }
}

async function readBody(socket, rest, headers) {
  const want = Number(headers['content-length'] || 0)
  if (!want || rest.length >= want) return rest.subarray(0, want).toString('utf8')
  const more = await readBytes(socket, want - rest.length)
  return Buffer.concat([rest, more]).toString('utf8')
}

async function sendConnect(target, extraHeaders = []) {
  const socket = await connectTLS()
  socket.write([
    `CONNECT ${target} HTTP/1.1`,
    `Host: ${target}`,
    `Proxy-Authorization: ${credential}`,
    ...extraHeaders,
    '', '',
  ].join('\r\n'))
  return socket
}

let stub
let nginx
let exitCode = 0

if (!config.enabled) {
  results.push({ name: 'openresty proxy entry smoke', ok: true, skipped: true, detail: 'TM_PROXY_E2E_NGINX is not 1' })
  log('SKIP openresty proxy entry smoke :: set TM_PROXY_E2E_NGINX=1 to run (needs docker and openssl)')
  console.log('\n== summary ==')
  for (const r of results) console.log(`SKIP  ${r.name}`)
  process.exit(0)
}

const missing = missingPrerequisites(config)
if (missing.length) {
  results.push({ name: 'openresty proxy entry smoke', ok: true, skipped: true, detail: missing.join('; ') })
  log(`SKIP openresty proxy entry smoke :: missing ${missing.join('; ')}`)
  console.log('\n== summary ==')
  for (const r of results) console.log(`SKIP  ${r.name}`)
  process.exit(0)
}

try {
  buildImage(config, log)
  renderArtifacts(config, log)
  stub = await startStub(config.stubPort)
  nginx = startNginx(config, log)
  if (!await waitListening(config.listenPort)) {
    throw new Error(`openresty did not listen on 127.0.0.1:${config.listenPort}\n${nginx.tail(60)}`)
  }
  log(`openresty listening on 127.0.0.1:${config.listenPort}, stub on :${config.stubPort}`)

  // 1. CONNECT 搬运与请求头白名单
  stub.reset()
  {
    const socket = await sendConnect('ok.test:443', [
      'X-TunnelMesh-Route: tp-forged.example.com',
      'X-TunnelMesh-Client-IP: 203.0.113.7',
      'X-Evil: dropped',
    ])
    const { head } = await readHead(socket)
    const { status } = parseHead(head)
    const record = stub.find((item) => item.kind === 'connect')
    check('CONNECT is answered with 200 Connection Established',
      status === 200 && head.startsWith('HTTP/1.1 200'), `status=${status}`)
    check('route identity comes from SNI, not from a client header',
      record?.headers['x-tunnelmesh-route'] === config.proxyHost, `got=${record?.headers['x-tunnelmesh-route']}`)
    check('a forged client IP is replaced by the real peer',
      !!record?.headers['x-tunnelmesh-client-ip'] && record.headers['x-tunnelmesh-client-ip'] !== '203.0.113.7',
      `got=${record?.headers['x-tunnelmesh-client-ip']}`)
    check('Proxy-Authorization is passed through verbatim',
      record?.headers['proxy-authorization'] === credential,
      record?.headers['proxy-authorization'] ? 'value differs' : 'header missing')
    check('non-whitelisted client headers are dropped',
      record?.headers['x-evil'] === undefined, `got=${record?.headers['x-evil']}`)

    // 2. 双向字节完整性
    const payload = crypto.randomBytes(256 * 1024)
    const echoed = readBytes(socket, payload.length)
    socket.write(payload)
    const received = await echoed
    check('the tunnel carries 256 KiB in both directions byte-for-byte',
      received.equals(payload), `bytes=${received.length} upstream=${record?.bytesUp}`)

    // 3. 客户端断开后隧道被回收
    socket.destroy()
    await sleep(1500)
    check('the tunnel is released when the client goes away',
      record?.closed === true, `closed=${record?.closed}`)
  }

  // 4. 非 200 响应原样透传（状态行、头、body）
  for (const [target, wantStatus, wantHeader] of [
    ['denied.test:443', 403, null],
    ['authfail.test:443', 407, 'proxy-authenticate'],
    ['capacity.test:443', 503, 'retry-after'],
  ]) {
    const socket = await sendConnect(target)
    const { head, rest } = await readHead(socket)
    const { status, headers } = parseHead(head)
    const body = await readBody(socket, rest, headers)
    const ok = status === wantStatus
      && body.includes(`"code":${wantStatus}`)
      && (!wantHeader || !!headers[wantHeader])
    check(`${wantStatus} from the internal entry is relayed verbatim (${target})`, ok,
      `status=${status} ${wantHeader ? `${wantHeader}=${headers[wantHeader] || '<missing>'} ` : ''}body=${body.slice(0, 120)}`)
    socket.destroy()
  }

  // 5. 绝对形式（非 CONNECT）经 location / 到达内部入口
  stub.reset()
  {
    const socket = await connectTLS()
    socket.write([
      'GET http://ok.test/probe HTTP/1.1',
      'Host: ok.test',
      `Proxy-Authorization: ${credential}`,
      'X-TunnelMesh-Route: tp-forged.example.com',
      'Connection: close',
      '', '',
    ].join('\r\n'))
    const { head } = await readHead(socket)
    const { status } = parseHead(head)
    const record = stub.find((item) => item.kind === 'http')
    check('absolute-form requests arrive as origin-form with the trusted headers',
      status === 200
        && record?.url === '/probe'
        && record?.headers['host'] === 'ok.test'
        && record?.headers['proxy-authorization'] === credential
        && record?.headers['x-tunnelmesh-route'] === config.proxyHost,
      `status=${status} url=${record?.url} host=${record?.headers?.host} route=${record?.headers?.['x-tunnelmesh-route']}`)
    socket.destroy()
  }

  // 6. 日志既要能排障，也不能泄漏凭据
  {
    const text = nginx.tail(400)
    check('the error log records tunnel close with route and byte counters',
      text.includes(`tunnelmesh: tunnel closed route=${config.proxyHost}`), 'no tunnel close line found')
    check('the error log never contains the proxy credential',
      !text.includes(credential.split(' ')[1]), 'base64 credential leaked into the log')
  }

  exitCode = results.every((r) => r.ok) ? 0 : 1
} catch (error) {
  log('scenario crashed:', error?.stack || error)
  if (nginx) log(nginx.tail(60))
  exitCode = 1
} finally {
  console.log('\n== summary ==')
  for (const r of results) console.log(`${r.skipped ? 'SKIP' : r.ok ? 'PASS' : 'FAIL'}  ${r.name}`)
  fs.writeFileSync(path.join(config.workDir, 'results.json'), JSON.stringify({ results }, null, 2))
  if (nginx) await nginx.stop()
  if (stub) await stub.close()
  log(`workdir ${config.workDir} (rendered artifacts, container.log and results.json kept)`)
  process.exit(exitCode)
}
```

- [ ] **Step 11: 写 `test/e2e/proxy-entry/README.md`**

必须包含（结构对齐 `test/e2e/webssh/README.md`）：

- 一句话定位：只覆盖 OpenResty 搬运层，不覆盖 Server 策略；不属于 `go test ./...` 与 `npm test`。
- 断言表：逐条列出 Step 10 的 check 名称与“为什么存在”（头白名单防路由冒充、hop-by-hop 头透传、非 200 原样回传保证稳定错误码与 `Proxy-Authenticate`/`Retry-After` 不被吞、客户端断开后隧道回收防连接泄漏、日志不得出现凭据）。
- 前置条件：Node 20+、docker（含可用 daemon）、openssl；首次运行会编译 OpenResty 镜像（数分钟）。
- 运行命令与环境变量表：`TM_PROXY_E2E_NGINX`、`TM_PROXY_E2E_IMAGE`、`TM_PROXY_E2E_SKIP_BUILD`、`TM_PROXY_E2E_PORT`、`TM_PROXY_E2E_STUB_PORT`、`TM_PROXY_E2E_DOMAIN`、`TM_PROXY_E2E_ROUTE`、`TM_PROXY_E2E_DIR`、`TM_PROXY_E2E_REPO`。
- skip 语义：未设置 `TM_PROXY_E2E_NGINX=1` 或前置条件缺失时打印原因并以 0 退出，不得静默通过。
- 失败排查：先看 `<workdir>/container.log`（error_log 走 stderr，级别 info），再看 `<workdir>/results.json`；渲染后的 conf 与 Lua 也在 `<workdir>` 里，可直接 `nginx -t` 复查。

- [ ] **Step 12: 验证 skip 分支**

Run: `node test/e2e/proxy-entry/run.mjs`
Expected: 输出 `SKIP openresty proxy entry smoke :: set TM_PROXY_E2E_NGINX=1 to run (needs docker and openssl)`，退出码 0。

- [ ] **Step 13: 运行完整冒烟（需要 docker）**

Run: `TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs`
Expected: 全部 check PASS，退出码 0。

若当前环境没有 docker 或无法访问 `openresty.org` / `github.com`：把本步骤标记为“未在本机执行”，在 `test/e2e/proxy-entry/README.md` 顶部注明镜像与冒烟尚未经本机构建验证，并在 PR 记录的 Test Evidence 里如实写明“OpenResty 层为未验证项，需在具备 docker 的环境执行一次”。不得为了让脚本通过而放宽断言。

- [ ] **Step 14: 更新 `deploy/README.md`**

- “目录内容”表新增一行：`openresty/tunnelmesh_proxy_entry.lua`、`openresty/tunnelmesh-proxy.conf.example`、`openresty/Dockerfile.proxy-connect` | tp-* HTTP 代理入口的 OpenResty 搬运层、server 块模板与补丁内核镜像 | [OpenResty 代理入口部署](../docs/deployment/openresty-proxy-entry.md)。
- “目录内容”表的产物一致性测试行补上 `openresty/openresty_artifacts_test.go`。
- “模板约定”小节新增：`openresty/tunnelmesh-proxy.conf.example` 占位符 `__LISTEN__`、`__SERVER_NAME_REGEX__`、`__SSL_CERT__`、`__SSL_CERT_KEY__`、`__LUA_FILE__`、`__INTERNAL_UPSTREAM__`、`__EDGE_ALLOW__`；`tunnelmesh_proxy_entry.lua` 的 `CONFIG` 表用行尾标记 `-- __TM_INTERNAL_HOST__`、`-- __TM_INTERNAL_PORT__`、`-- __TM_READ_TIMEOUT_MS__` 定位替换；默认值与 `server.proxy_entry.*` 的一致性由 `openresty_artifacts_test.go` 守护。
- “发布包内容”小节补一句：`openresty/` 与 `prometheus/`、`grafana/` 同属运维自行挂载的产物，不进 `scripts/build-release.sh` 归档。
- “校验”小节补两行：`go test ./deploy/openresty -count=1` 与 `TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs`（需要 docker）。

- [ ] **Step 15: 修正 spec 的外部引用**

`docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md`：

- 第 2 节表格第 1 行的证据列把 `patch/proxy_connect.patch` 改为“上游模块 `chobits/ngx_http_proxy_connect_module` v0.0.7 的 `patch/proxy_connect_rewrite_102101.patch`（仓库内不存在该文件，Dockerfile 构建时拉取）”，并保留原有的 `ngx_http_core_find_config_phase` 代码引用。
- 第 2 节表格第 2 行的证据列补上补丁的准确文件名，并新增三行事实：补丁删除 `ngx_http_process_request_header` 中对 CONNECT 的 405 拒绝；`NGX_HTTP_PROXY_CONNECT` 宏由模块 `config` 的 `have=NGX_HTTP_PROXY_CONNECT . auto/have` 定义；OpenResty 构建顺序为 `./configure` -> `patch -d build/nginx-<ver>/` -> `make`。
- 第 2 节表格末尾新增一行：模块 README “Known Issues” 明确 HTTP/2 下不支持 CONNECT，与 `ngx.req.socket` 的 HTTP/2 限制互为双重理由。
- 第 5.1 节的示例配置改为引用模板产物，并说明它使用占位符（渲染示例见 `docs/deployment/openresty-proxy-entry.md`），避免同一份 nginx 配置在 spec、模板与文档里各存一份。
- 第 14 节第 11 条把“Go stub 上游”改为“Node stub 上游（`test/e2e/proxy-entry/lib/stub.mjs`）”，并写明冒烟只覆盖 OpenResty 层、Server 策略由 Go 测试覆盖。
- 第 17 节决策表新增一行：`OpenResty 产物形态 | 占位符模板 + Go 侧产物一致性测试 | 直接写死生产值：E2E 只能测一份手抄副本，模板与 Go 默认值漂移无人发现`。

- [ ] **Step 16: Commit（需授权）**

```bash
git add deploy/openresty/tunnelmesh_proxy_entry.lua deploy/openresty/tunnelmesh-proxy.conf.example deploy/openresty/Dockerfile.proxy-connect deploy/openresty/README.md deploy/openresty/openresty_artifacts_test.go deploy/README.md test/e2e/proxy-entry/run.mjs test/e2e/proxy-entry/README.md test/e2e/proxy-entry/lib/harness.mjs test/e2e/proxy-entry/lib/stub.mjs docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md
git commit -m "feat(deploy): ship openresty tp proxy entry artifacts"
```

---

### Task 15: 观测面板、告警、文档与 PR 记录（收尾）

**Files:**

- Modify: `deploy/grafana/dashboard_schema_test.go`（row 数量 5 -> 6、新 row 标题、5 个新必需指标）
- Modify: `deploy/grafana/dashboards/tunnelmesh.json`（追加 `HTTP Proxy Entry` row 与 7 个面板）
- Modify: `deploy/prometheus/alert-rules.yaml`（2 条代理入口告警）
- Modify: `docs/operations/observability.md`（Row 结构、`route`/`reason` 标签基数、告警责任）
- Create: `docs/deployment/openresty-proxy-entry.md`
- Create: `docs/user-guide/http-proxy-entry.md`
- Modify: `docs/deployment/nginx.md`
- Modify: `docs/operations/troubleshooting.md`
- Modify: `docs/README.md`、`docs/user-guide/managed-http-route.md`、`docs/operations/completeness-checklist.md`
- Modify: `README.md`、`README.zh-CN.md`、`deploy/README.md`
- Create: `docs/pull-requests/2026-09-13-managed-route-http-proxy-entry.md`
- Regenerate: `docs/pull-requests/README.md`、`docs/superpowers/plans/README.md`、`docs/superpowers/specs/README.md`、`docs/architecture/adr/README.md`（`python3 scripts/gen_doc_index.py`）
- Modify: `docs/development/testing.md`、`docs/development/README.md`（Step 11：验证层级表与索引补 proxy-entry 端到端冒烟）
- Modify: `docs/architecture/overview.md`（Step 11：tp-* 入口拓扑链路，spec 第 16 节要求）
- Modify: `docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md`（Step 15(b)：第 13 / 14 / 15 节偏差回写）
- Modify: `docs/operations/configuration.md`（`server.proxy_entry` 配置表已由 Task 1 写入，Step 16 仅作为兜底出现在 `git add`，通常无变化）

**Interfaces:**

- Consumes: Task 10 的 5 个指标与标签（`tunnelmesh_proxy_entry_requests_total{route,mode,result,error_class}`、`_tunnels_active{route}`、`_tunnel_duration_seconds{route,result}`、`_auth_failures_total{route,reason}`、`_acl_denied_total{route}`）与 `result` 取值 `success`/`denied`/`untrusted_peer`；Task 1 的 `server.proxy_entry.*` 配置项；Task 12 的管理 API 字段与 `docs/api/openapi.yaml`；Task 14 的 OpenResty 产物、占位符与部署约束
- Produces: 无。本任务是收尾，不被其它任务依赖。

- [ ] **Step 1: 先改 Grafana schema 测试（红灯）**

`deploy/grafana/dashboard_schema_test.go` 的 `TestTunnelMeshDashboardSchema` 改三处：

```go
    if strings.Count(string(b), `"type": "row"`) != 6 {
        t.Fatalf("dashboard must contain exactly six row panels")
    }
    for _, row := range []string{"Overview", "Agent", "Network", "Cluster", "Security", "HTTP Proxy Entry"} {
        if !strings.Contains(string(b), `"title": "`+row+`"`) {
            t.Fatalf("missing row %q", row)
        }
    }
```

并在函数末尾的必需指标切片里追加 5 项（放在既有 10 项之后）：

```go
        "tunnelmesh_proxy_entry_requests_total",
        "tunnelmesh_proxy_entry_tunnels_active",
        "tunnelmesh_proxy_entry_tunnel_duration_seconds",
        "tunnelmesh_proxy_entry_auth_failures_total",
        "tunnelmesh_proxy_entry_acl_denied_total",
```

为什么先改测试：Dashboard 是这个仓库里唯一“改错了也不会报错”的产物，schema 测试是它的唯一门禁；先加断言才能保证新 row 不是靠肉眼检查通过的。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./deploy/grafana/ -count=1`
Expected: FAIL，`dashboard must contain exactly six row panels`。

- [ ] **Step 3: 追加 Dashboard row**

在 `deploy/grafana/dashboards/tunnelmesh.json` 的 `panels` 数组末尾（`Security` row 之后）追加下面这一段。保持仓库既有写法：每个面板单独一行、`datasource` 固定 `${DS_PROMETHEUS}`、不写 `id`、table 面板带 `"format": "table"`、row 本身不带 `gridPos`。

```json
    {"type": "row", "title": "HTTP Proxy Entry", "collapsed": false, "panels": [
      {"type": "stat", "title": "Proxy denials", "datasource": "${DS_PROMETHEUS}", "targets": [{"expr": "sum(increase(tunnelmesh_proxy_entry_requests_total{cluster=~\"$cluster\", node_id=~\"$node_id\", result!=\"success\"}[15m]))", "refId": "A"}], "gridPos": {"h": 4, "w": 6, "x": 0, "y": 80}},
      {"type": "timeseries", "title": "Proxy requests", "datasource": "${DS_PROMETHEUS}", "targets": [{"expr": "sum by (mode, result) (rate(tunnelmesh_proxy_entry_requests_total{cluster=~\"$cluster\", node_id=~\"$node_id\"}[5m]))", "refId": "A"}], "gridPos": {"h": 8, "w": 9, "x": 6, "y": 80}},
      {"type": "timeseries", "title": "Proxy tunnels active", "datasource": "${DS_PROMETHEUS}", "targets": [{"expr": "sum by (route) (tunnelmesh_proxy_entry_tunnels_active{cluster=~\"$cluster\", node_id=~\"$node_id\"})", "refId": "A"}], "gridPos": {"h": 8, "w": 9, "x": 15, "y": 80}},
      {"type": "timeseries", "title": "Proxy tunnel duration p95", "datasource": "${DS_PROMETHEUS}", "targets": [{"expr": "histogram_quantile(0.95, sum by (le, route, result) (rate(tunnelmesh_proxy_entry_tunnel_duration_seconds_bucket{cluster=~\"$cluster\", node_id=~\"$node_id\"}[5m])))", "refId": "A"}], "gridPos": {"h": 8, "w": 12, "x": 0, "y": 88}},
      {"type": "timeseries", "title": "Proxy throughput", "datasource": "${DS_PROMETHEUS}", "targets": [{"expr": "sum by (direction) (rate(tunnelmesh_bytes_total{cluster=~\"$cluster\", node_id=~\"$node_id\", component=\"proxy_entry\"}[5m]))", "refId": "A"}], "gridPos": {"h": 8, "w": 12, "x": 12, "y": 88}},
      {"type": "timeseries", "title": "Proxy auth failures and ACL denials", "datasource": "${DS_PROMETHEUS}", "targets": [{"expr": "sum by (route, reason) (rate(tunnelmesh_proxy_entry_auth_failures_total{cluster=~\"$cluster\", node_id=~\"$node_id\"}[5m]))", "refId": "A"}, {"expr": "sum by (route) (rate(tunnelmesh_proxy_entry_acl_denied_total{cluster=~\"$cluster\", node_id=~\"$node_id\"}[5m]))", "refId": "B"}], "gridPos": {"h": 8, "w": 12, "x": 0, "y": 96}},
      {"type": "table", "title": "Proxy entry denials", "datasource": "${DS_PROMETHEUS}", "targets": [{"expr": "sum by (route, mode, result, error_class) (increase(tunnelmesh_proxy_entry_requests_total{cluster=~\"$cluster\", node_id=~\"$node_id\", result!=\"success\"}[15m]))", "refId": "A", "format": "table"}], "gridPos": {"h": 8, "w": 12, "x": 12, "y": 96}}
    ]}
```

注意 JSON 语法：前一个 row（`Security`）的结尾 `]}` 之后要补逗号。

同时把顶层 `description` 从 `Unified TunnelMesh control-plane, agent, network, cluster, and security observability.` 改为 `Unified TunnelMesh control-plane, agent, network, cluster, security, and HTTP proxy entry observability.`，避免描述与 Row 结构不一致（当前仓库实测为 5 个 row：Overview / Agent / Network / Cluster / Security，本步骤后为 6 个）。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./deploy/... -count=1`
Expected: PASS（含 Task 14 的 `deploy/openresty` 与既有 `deploy/install`）。

- [ ] **Step 5: 追加告警规则**

`deploy/prometheus/alert-rules.yaml` 的 `tunnelmesh-alerts` 组末尾追加：

```yaml
      - alert: TunnelMeshProxyEntryAuthFailures
        expr: sum(rate(tunnelmesh_proxy_entry_auth_failures_total[10m])) > 0.2
        for: 10m
        labels: {severity: warning}
        annotations: {summary: TunnelMesh tp proxy entry sees sustained Basic auth failures}
      - alert: TunnelMeshProxyEntryDenials
        expr: sum(rate(tunnelmesh_proxy_entry_requests_total{result!="success"}[10m])) > 0.5
        for: 10m
        labels: {severity: warning}
        annotations: {summary: TunnelMesh tp proxy entry is denying proxy requests}
```

与既有规则不同，这两条**不加** `absent(...) or` 前缀：`server.proxy_entry.enabled` 默认 false，未启用时指标根本不存在，加 `absent()` 会让所有未启用该功能的部署持续告警。阈值取“10 分钟平均每秒 0.2 次认证失败 / 0.5 次拒绝”，低于该量级的零星扫描不值得叫人。

Run: `promtool check rules deploy/prometheus/alert-rules.yaml`
Expected: `SUCCESS: 9 rules found`（既有 7 条 + 新增 2 条）。若本机没有 `promtool`，改用 `python3 -c "import yaml,sys; yaml.safe_load(open('deploy/prometheus/alert-rules.yaml'))"` 校验语法，并在 PR 记录里注明 promtool 未执行。

- [ ] **Step 6: 更新可观测性文档**

`docs/operations/observability.md`：

- “Grafana” 小节第一段把“按 Overview、Agent、Network、Cluster、Security 五个 Row 组织”改为六个 Row 并加上 `HTTP Proxy Entry`。
- 该小节列 Row 内容的列表追加一条：`HTTP Proxy Entry Row：代理请求速率与结果、活跃隧道数（按 route）、隧道时长 p95、代理入口上下行字节、认证失败与 ACL 拒绝、拒绝明细表。`
- 补一句：`server.proxy_entry.enabled=false` 时该行所有面板为空，属正常现象。
- “标签和留存”小节：在允许的低基数标签清单里加入 `route` 与 `reason`，并写明基数边界——`route` 取值是 `tp-<name>.<domain_suffix>` 完整域名，数量等于管理后台创建的代理路由数（人工创建，非请求维度）；`reason` 取值固定为认证失败原因枚举。同时重申目标地址、客户端 IP、`Proxy-Authorization` 不得进入 label。
- “告警责任”小节补一句：代理入口的认证失败与拒绝告警归业务负责人（与路由拒绝同类），值班需要能区分“ACL 配错”和“密码爆破”，判据是 `tunnelmesh_proxy_entry_acl_denied_total` 与 `_auth_failures_total` 的相对量级。
- 指标清单处（若有）补 5 个 `tunnelmesh_proxy_entry_*` 指标及其标签，与 Task 10 的 `NewMetrics` 注册一致。

- [ ] **Step 7: 写 `docs/deployment/openresty-proxy-entry.md`**

必须包含（操作步骤写在这里，`deploy/openresty/README.md` 只讲产物，两边不重复）：

- **适用前提**：OpenResty（nginx >= 1.25.1 内核 + lua-nginx-module + 已打 `proxy_connect_rewrite_102101.patch`）；通配证书覆盖 `*.<domain_suffix>`；泛解析 DNS `tp-*.<domain_suffix>` 指向入口 IP；推荐 OpenResty 与 tunnelmesh-server 同机（内部入口只绑回环）。给出两级检查命令：先 `nginx -V 2>&1 | tr ' ' '\n' | grep proxy_connect` 确认模块编进了内核，再 `bash deploy/openresty/spike-connect-check.sh 18443`（Task 0 产物）确认补丁真的生效。必须写清为什么两级都要：模块单独编译进去时 `nginx -V` 一样能看到 `--add-module`，但没打补丁的内核会对每个 CONNECT 回 405，`$connect_host` 也不存在。
- **Server 侧开关**：`server.proxy_entry.enabled=true`、`domain_suffix`、`listen`、`trusted_proxies` 的最小配置片段（配置文件与环境变量两种写法），并强调改完先跑 `tunnelmesh-server --config <file> check-config`。
- **渲染模板**：7 个占位符与 3 个 Lua 行尾标记的取值来源表；随后贴出**渲染完成的完整 server 块**（用 `tm.example.com` 作为示例后缀），让读者可以直接对照，不必自己推演替换结果。
- **安装步骤**：复制 Lua 到 `/etc/openresty/lua/tunnelmesh_proxy_entry.lua`；把渲染后的 `upstream` 与 `server` 块并入既有 `nginx.conf` 的 `http{}`；在 main 上下文加 `worker_shutdown_timeout 300s;`；`nginx -t`；`nginx -s reload`。明确 `worker_shutdown_timeout` 不能写在 server 块里。
- **容器方式（可选）**：`docker build -f deploy/openresty/Dockerfile.proxy-connect -t tunnelmesh/openresty-proxy-connect:1.25.3.1 .`；替换生产 OpenResty 前必须先比对目标机 `nginx -V` 的 `--with-*` 参数并补进 Dockerfile 的 `configure`，否则现有 server 块会因缺模块起不来；镜像默认非 root，监听 443 需要 root 或 `setcap cap_net_bind_service=ep`。
- **验证**：`curl -sv --proxy-insecure -x https://tp-demo.<domain_suffix> --proxy-user 'u:p' https://ifconfig.me`（自签证书时用 `--proxy-insecure`），预期返回的出口 IP 属于 agent 所在网络；再给一条 ACL 外主机的预期 403 与错误密码的预期 407。
- **容量评估**：每条隧道 = 1 个 nginx 请求 + 2 个 socket（下游 + 内部入口），`worker_connections` 与 `worker_rlimit_nofile` 按 `server.proxy_entry.max_concurrent_tunnels × 2` 起评；Server 先拒绝超限请求（503 + Retry-After），nginx 侧 `limit_conn` 只是兜底，不要配得比 Server 更紧，否则排障时看到的是 nginx 的 503 而不是稳定错误码。
- **reload 与发布影响**：`nginx -s reload` 会让旧 worker 停止接受新连接，在途隧道在 `worker_shutdown_timeout` 窗口内排空，超时后被强制关闭；客户端表现为连接断开并重连。给出发布窗口建议（低峰期、先扩 Server 再 reload nginx）。
- **回滚（5 分钟内）**：删除渲染进去的 `upstream` 与 `server` 块后 `nginx -s reload`，入口立刻消失且不影响既有 admin/托管路由；或把 `server.proxy_entry.enabled` 置 false 并重启 Server（内部入口关闭后 nginx 侧表现为 502）。路由数据无需回滚：`tunnels` 里的 `http-proxy` 行可以直接停用或删除，Schema 仍是 v13。
- **跨机部署内部入口**：`listen` 绑内网地址、`trusted_proxies` 收紧到 OpenResty 主机 IP、禁止 `0.0.0.0/0`（`check-config` 会拒绝）；标注“不推荐，仅在无法同机时使用”，并说明此时内部段是明文，链路上的 Basic 凭据可被嗅探。

- [ ] **Step 8: 写 `docs/user-guide/http-proxy-entry.md`**

必须包含（面向管理员与使用者两类读者，与 `managed-http-route.md` 分工：那篇讲反代路由，这篇讲正向代理入口）：

- **一句话定位**：把 `https://tp-<name>.<domain>` 填进浏览器或操作系统的“HTTPS 代理”，即可从任意允许的来源 IP 经指定 agent 出网或访问其内网服务，用户机器上不需要安装 tunnelmesh-client。
- **与 client 本地代理的区别表**：`tunnelmesh-client forward socks5` / `forward http-proxy`（本机监听、需装 client、支持 SOCKS5）vs 托管入口（无需安装、只支持 HTTP/HTTPS 代理、出口与 ACL 由管理员集中控制）。
- **管理员：后台操作**：路由管理 -> 新建 -> 类型选“HTTP 代理入口 (tp-)”，逐项说明名称（只填 `<name>`，域名自动拼 `tp-` 前缀并实时预览）、出口 Agent、认证方式（无 / 用户名密码）、代理凭据（需先在“密钥管理”里建 `proxy_basic` 类型凭据，填 username 与 password）、源 IP ACL（默认拒绝所有，`0.0.0.0/0` 放开，提供“放开全部来源”按钮）、目标网段/端口限制、允许访问内网目标开关、并发上限、备注。写明生效时间 <= 5 秒，不需要重启 Server，也不需要改 nginx。
- **管理员：API 方式**：`POST /api/v1/routes` 的完整 JSON 示例（`protocol: "http-proxy"`、`agentId`、`domain` 由服务端按 name 拼装或按 spec 直接给完整域名——以 Task 12 的实现为准，写文档前对照 `docs/api/openapi.yaml`），以及三类 400 场景：传了 `targetHost`/`targetPort`、在 `http-proxy` 与其它协议之间切换、CIDR 或端口非法。
- **使用者：各端配置**：macOS（系统设置 -> 网络 -> 详细信息 -> 代理 -> 安全网页代理 HTTPS）、Windows（Internet 选项 -> 连接 -> 局域网设置 -> 代理服务器，勾选“对 HTTPS 使用相同代理”）、Chrome/Firefox PAC 示例、`curl -x` 示例、SwitchyOmega 之类插件的填法。明确写出**只能填 `http://` 代理地址的旧客户端无法使用本入口**（路由身份来自 TLS SNI，凭据也必须加密传输）。
- **错误码自助排查表**：12 个稳定错误码逐条给出“你会看到什么 / 为什么 / 你能做什么”，与 Task 2 的 `errors.go` 一一对应，不得漏项也不得新增未实现的码。
- **限制**：不支持 SOCKS5；不支持公网 UDP 入口；一条路由只有一个出口 agent（无负载与故障转移）；Basic 认证是路由级共享账号，没有 per-user 配额；隧道建立之后的失败只会表现为连接断开，不会再有状态码。

- [ ] **Step 9: 追加 `docs/deployment/nginx.md`**

- 在“关键约束”之前新增一节 `## tp-* HTTP 代理入口（OpenResty）`：说明它与本文既有 server 块共用 443、由 SNI 分流；本文不重复模板内容，指向 [OpenResty 代理入口部署](openresty-proxy-entry.md) 与 `deploy/openresty/`；写明它要求 OpenResty（纯 nginx 没有 lua-nginx-module 就无法实现），以及 tp-* server 块必须省略 `http2`，与既有块的 per-server `http2 on;` 互不影响。
- “关键约束”小节追加 3 条：
  1. tp-* server 块禁止 `http2`：`ngx.req.socket(true)` 在 HTTP/2 下游不可用，proxy_connect 模块的 Known Issues 也明确不支持 HTTP/2 的 CONNECT。
  2. `location /` 必须显式 `proxy_set_header Proxy-Authorization $http_proxy_authorization;`：它是 hop-by-hop 头，nginx 默认不转发给上游，漏掉会让所有非 CONNECT 请求 407。
  3. 不能用 `proxy_connect;` + `proxy_pass` 做链式转发：模块 README 明确 “Any `location {}` block, `upstream {}` block and any other standard backend/upstream directives, such as `proxy_pass`, do not impact the functionality of this module.”，模块会自己直连目标，路由身份与 `Proxy-Authorization` 全部丢失。本项目的做法是不启用 `proxy_connect;` 指令，只用补丁提供的 `$connect_host`/`$connect_port` 变量与 server 级 `access_by_lua_file` 把 CONNECT 搬到 Server 内部入口。
- “验证命令”小节补一条：`curl -sv --proxy-insecure -x https://tp-demo.<domain_suffix> --proxy-user 'u:p' https://ifconfig.me`。

- [ ] **Step 10: 追加 `docs/operations/troubleshooting.md`**

在文件末尾（`## 管理后台打开是 Nginx / OpenResty 欢迎页` 之后）新增 `## tp-* HTTP 代理入口失败`，按“现象 -> 定位命令 -> 根因 -> 处理”四段式，至少覆盖：

- `curl` 报 `Received HTTP code 403 from proxy after CONNECT`：三类原因（路由身份非法、路由不存在或停用、源 IP 不在 ACL）对外文案一致，只能靠 Server 侧区分——查 `tunnelmesh_proxy_entry_requests_total{result="denied"}` 的 `error_class`，或看 Server 日志与审计事件 `proxy_route_denied` 的 `reason`。特别提醒：Server 看到的是 OpenResty 传来的 `$remote_addr`，如果入口前面还有一层 LB，需要让 LB 透传真实客户端 IP 并把 `server.proxy_entry.client_ip_header` 改成对应头，否则 ACL 会按 LB 的地址判定。
- 407 且连续失败后长时间不恢复：`auth_backoff_threshold`（默认 5）触发退避，30s 起每次翻倍、上限 15m；退避期内不做密码比对，直接 407。
- 405 Method Not Allowed：OpenResty 内核没有打 proxy_connect 补丁（`nginx -V` 检查），或模块与补丁版本不匹配。
- 502：内部入口不可达。依次确认 `server.proxy_entry.enabled`、`listen` 与 conf 模板 `__INTERNAL_UPSTREAM__` 渲染结果一致、同机回环可达（`curl -sv http://127.0.0.1:8089/` 应得到 Server 的 403 而不是 connection refused）。
- 503 + `Retry-After: 5`：并发超限，查全局 `max_concurrent_tunnels` 与该路由 `config.maxConcurrentTunnels`，以及 `tunnelmesh_proxy_entry_tunnels_active`。
- 504：`connect_timeout` 内没有开流成功，查 agent 是否在线、集群模式下 relay 是否正常。
- 隧道建立后很快断开：Lua 的 `read_timeout_ms` 必须等于 Server `idle_timeout` + 30s，配反了会出现 nginx 先掐断；确认 `deploy/openresty/openresty_artifacts_test.go` 是通过的。
- reload 后所有代理连接断开：main 上下文缺 `worker_shutdown_timeout 300s;`。
- 浏览器提示代理不支持 / 协商到 h2 失败：tp-* server 块被加了 `http2`，或客户端只能填 `http://` 代理地址。
- error.log 里大量 `client aborted`：属正常（客户端主动断开）；但如果只有 `client aborted` 而没有对应的 `tunnel closed`，说明 `lua_check_client_abort` 未开或 `ngx.on_abort` 未注册，隧道会挂到读超时才回收。
- 排障顺序固定为：OpenResty `error_log`（info 级，含 route/target/client/字节数/时长/结束原因）-> Server 日志 `proxy_entry_*` -> Prometheus `tunnelmesh_proxy_entry_*` -> 审计日志 `proxy_tunnel_opened` / `proxy_tunnel_closed`。三处都不得出现密码或完整 `Proxy-Authorization` 头，若出现按安全事故处理。

- [ ] **Step 11: 更新入口索引与能力矩阵**

`docs/README.md`：

- “使用者”列表在 `[托管 HTTP 路由]` 之后加一行：`- [HTTP 代理入口（tp-*）](user-guide/http-proxy-entry.md)：把 https://tp-<name>.<domain> 填进浏览器或系统代理，无需安装 client`。
- “部署”列表在 `[Nginx/WSS 推荐配置]` 之后加一行：`- [OpenResty tp-* 代理入口](deployment/openresty-proxy-entry.md)：模板渲染、镜像构建、容量评估、reload 影响与回滚`。
- “文档地图”表 `deployment/` 行的“内容”列补上 `OpenResty 代理入口`。

`docs/user-guide/managed-http-route.md`：在开头段落后加一句交叉引用——`http-proxy` 类型的路由不是反向代理，它由 SNI 选路由、目标由请求决定、`target_host`/`target_port` 存的是哨兵 `*`/`0`，使用说明见 [HTTP 代理入口](http-proxy-entry.md)。

`docs/operations/completeness-checklist.md`：已完成项末尾追加 `- [x] tp-* 托管 HTTP 代理入口（OpenResty 搬运层 + Server 策略内核 + 管理后台）`；未完成项不动（SOCKS5 托管入口、单路由多出口池化仍属 deferred，不得写成已支持）。

`docs/architecture/overview.md`（spec 第 16 节要求的“入口拓扑图”，用紧凑文字链路代替图片，避免图与代码之间出现第二处需要同步的地方）。在第一段（三个可执行程序的职责）之后插入：

```text
浏览器 / curl / 系统代理（HTTPS 代理方案，只能填 https://）
  └─ TLS + SNI: tp-<name>.<domain_suffix>          复用既有 443，不新增公网端口
       └─ OpenResty tp-* server 块：access_by_lua_block
            · 打过 proxy_connect 补丁的内核对 CONNECT 跳过 location 匹配
            · 只搬字节，不含任何策略判断
            · 注入可信头 X-TunnelMesh-Route（来自 SNI）/ -Client-IP / -Client-Port
            └─ 明文回环 127.0.0.1:8089（server.proxy_entry.listen）
                 └─ tunnelmesh-server：ProxyEntryListener
                      · trusted_proxies 前置校验，不在白名单则读请求前直接关闭
                      · proxyentry 策略链：身份解析 → 源 IP ACL → Basic 认证 → 目标校验 → 并发限额
                      └─ relay.NodeTransport.OpenStream（集群模式下自动跨节点）
                           └─ Agent 出口：目标侧二次 SSRF / 私网 / 端口校验后建立真实连接
```

同时在该文件“公网模式只需要 HTTP/HTTPS/WSS 入口”那段补一句：tp-* 代理入口不新增公网监听端口，与既有 443 由 SNI 分流；唯一策略执行点是 Server，OpenResty 与 Lua 不含授权逻辑。

不新建 ADR 文件：spec 第 16 节已声明 `docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md` 本身即 ADR 载体，`docs/architecture/adr/README.md` 由 Step 12 的索引脚本重生成；只有在 Task 0 判定回退 A2 或后续发生重大设计回退时才另行补记 ADR。

`README.md`：

- 能力列表（`forward socks5` / `forward http-proxy` 那一段）之后加一条 bullet：`- Managed HTTP proxy entry — set \`https://tp-<name>.<domain>\` as a browser or OS proxy; the egress agent, Basic auth and source ACL are configured in the admin console, and nothing has to be installed on the user machine.`
- `## Deployment` 的 `- Edge:` 行补上 `[OpenResty tp-* proxy entry](docs/deployment/openresty-proxy-entry.md)`。
- `## Documentation` 的 `**User guides**` 列表加一条 `- [HTTP proxy entry](docs/user-guide/http-proxy-entry.md) — browser/OS proxy without installing the client`。
- ASCII 架构图不改：tp-* 入口复用既有 443、既有 Server 进程与既有 agent 出口路径，图中已有的 Server 框已经覆盖它，加框只会让图与代码产生第二处需要同步的地方。

`README.zh-CN.md`：按同样三处补中文（能力 bullet、Edge 行、User guides 列表），措辞与 `docs/user-guide/http-proxy-entry.md` 的定位句保持一致。

`deploy/README.md`：`grafana/dashboards/tunnelmesh.json` 那一行的“五个 Row 组织”改为“六个 Row 组织（Overview / Agent / Network / Cluster / Security / HTTP Proxy Entry）”。

`docs/development/testing.md`：

- 验证层级表在“浏览器端到端”行之后加一行：`| OpenResty 端到端 | TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs | 真实 OpenResty 容器 + 内部入口替身，验证 CONNECT 搬运、请求头白名单、非 200 响应原样透传、绝对形式改写、客户端断开后隧道回收、日志不含凭据 |`。
- 表后那段说明补一句：该冒烟需要 docker 与 openssl，未设置 `TM_PROXY_E2E_NGINX=1` 时打印原因并以 0 退出；修改 `deploy/openresty/` 下任一产物后发布前必须跑一次，详见 [test/e2e/proxy-entry/README.md](../../test/e2e/proxy-entry/README.md)。

`docs/development/README.md`：在 WebSSH 端到端那一行之后加 `- [tp-* 代理入口 OpenResty 端到端测试](../../test/e2e/proxy-entry/README.md)：真实 OpenResty 容器 + 内部入口替身`。

- [ ] **Step 12: 重新生成文档索引**

Run: `python3 scripts/gen_doc_index.py && git diff --stat docs/pull-requests/README.md docs/superpowers/plans/README.md docs/superpowers/specs/README.md docs/architecture/adr/README.md`
Expected: 四份索引出现本 feature 的 plan / spec / pr 三条交叉引用；`docs/README.md` 不在生成范围内（手工维护，见 Step 11）。

再跑一次确认幂等：

Run: `python3 scripts/gen_doc_index.py && git diff --check && git diff --stat docs/pull-requests/README.md`
Expected: 第二次执行后 diff 不再变化。

- [ ] **Step 13: 写 PR 记录**

`docs/pull-requests/2026-09-13-managed-route-http-proxy-entry.md`，章节与 `docs/pull-requests/2026-09-12-admin-webssh-sftp.md` 完全一致：

- **Title**：`Managed-route HTTP proxy entry (tp-*)`。
- **Target Branch**：`main`。
- **Summary**：三段——用户视角（无需安装 client 即可用浏览器/系统代理）、架构视角（OpenResty 只搬字节，Server 是唯一策略执行点，出口经既有 relay 到 agent）、实现视角（`internal/proxyentry` 策略内核 + `internal/server/proxy_entry*.go` 入口 + 复用 `tunnels`/`credentials` 表，Schema 保持 v13）。
- **User Impact**：新增能力清单（tp-* 路由、两种认证方式、源 IP ACL、目标 CIDR/端口限制、允许内网目标开关、并发上限、后台使用说明抽屉）；明确不受影响的部分（既有托管路由、client 本地 SOCKS5/HTTP 代理、WebSSH）。
- **API / Schema / Configuration Impact**：`/api/v1/routes` 与 `/api/v1/credentials` 的新字段与 `http-proxy` / `proxy_basic` 枚举；Schema 无变更（v13，零迁移）；新增 `server.proxy_entry` 的 13 个配置项及默认值；`TUNNELMESH_SERVER_PROXY_ENTRY_*` 环境变量与 `--server.proxy_entry.*` 命令行等价。
- **Security and Authorization Impact**：可信头只由 OpenResty 写入且客户端同名头被丢弃；内部入口只绑回环 + `trusted_proxies` 前置校验 + 启动期禁止 `0.0.0.0/0`；Basic 密码复用既有 AES-256-GCM secret store，列表与日志永不回显；`routing.IsDangerousAddress` 恒拒 + agent 侧二次校验；认证失败指数退避；未知路由/停用路由/ACL 拒绝对外同文案防枚举。
- **Test Evidence**：逐条贴出 Step 14 的命令与实际输出摘要（通过数、耗时），并明确写出哪些项**未**在本机执行（例如需要 docker 的 E2E、需要 `promtool` 的规则校验、需要 `TUNNELMESH_TEST_MYSQL_DSN` 的 MySQL contract），不得把未执行项写成已通过。
- **Release Steps**：先升级 Server（`server.proxy_entry.enabled=false` 时行为与旧版完全一致）-> 部署 OpenResty 产物并 `nginx -t` -> 配 DNS 泛解析与通配证书 -> 打开 `enabled` 并 `check-config` -> 后台建一条 tp-* 路由验证 -> 导入更新后的 Grafana Dashboard。
- **Rollback Steps**：5 分钟止损顺序——删除 nginx 里的 tp-* server 块并 reload（入口立即消失）；或 `enabled=false` 重启 Server；路由行可直接停用；Schema 无变更因此不涉及数据回滚。
- **Reviewer Focus**：`internal/proxyentry` 的 fail-closed 语义（ACL 空列表=拒绝、非法条目=拒绝）、`internal/server/proxy_entry.go` 的 hijack 与 `spliceWithIdleTimeout` 半关闭处理、哨兵 `target_host="*"` 在 `loadManagedRoutes` 中被正确跳过（否则 `location /` 的泛域名反代会误匹配 tp-*）、Lua 里没有任何策略逻辑、日志与指标不含凭据。
- **Integration Status**：写明基线 commit（`git merge-base HEAD main`）与本 PR 触及的文件清单来源（Task 0-15 的 commit 步骤）；若期间有其它会话的提交进入 `main`，逐条列出与本 PR 重叠的文件及处理方式，无重叠则明确写“无重叠”。
- 全文不得出现密码、Token、私钥、生产 DSN 或未脱敏日志。

- [ ] **Step 14: 全量门禁**

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
gofmt -l internal/ cmd/ deploy/ test/
git diff --check
go test ./deploy/... -count=1
promtool check rules deploy/prometheus/alert-rules.yaml
node test/e2e/proxy-entry/run.mjs
cd web && npm test -- --run && npm run build && cd .. && ./scripts/verify-web-embed.sh
```

Expected: 全部通过；`gofmt -l` 与 `git diff --check` 无输出；`node test/e2e/proxy-entry/run.mjs` 打印 SKIP 并以 0 退出。有 docker 的环境再执行一次 `TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs`，并把结果写进 PR 记录的 Test Evidence。

`promtool` 与 docker 都是可选依赖，缺失不算门禁失败：`promtool` 未安装时按 Step 5 的说明改用 `python3 -c "import yaml,sys; yaml.safe_load(open('deploy/prometheus/alert-rules.yaml'))"` 校验 YAML 语法；docker 缺失时 E2E 保持 SKIP。两者都必须在 PR 记录的 Test Evidence 里写成“未执行 + 原因”，不得写成已通过。`go test -race ./...` 与 `go test ./... -count=1` 是硬性门禁，不允许跳过。

- [ ] **Step 15: 验收对照与 spec 偏差回写**

(a) 逐条核对 spec 第 18 节的 6 条验收标准，把结论写进 PR 记录的 Test Evidence：

| spec 第 18 节 | 验证方式 | 证据 |
|---|---|---|
| 1. 后台建路由后 5 秒内生效，不重启 Server、不改 nginx | Task 7 的 `TestManagedRouteTable*`（`http-proxy` 行不进反代路由表、proxy 索引与 revision 正确）+ Task 12 的 `TestAPIRouteProxyProtocol*`（创建即可被快照读到）；5s TTL 沿用既有 `loadManagedRoutes` 缓存，不新增测试 | `go test ./internal/server/ -run 'ManagedRoute\|APIRouteProxy' -count=1 -v` 输出 |
| 2. ACL 内可用且出口 IP 属 agent 网络；ACL 外 403；密码错 407 且第 6 次进退避 | Task 4 的 ACL 用例、Task 5 的认证与退避用例、Task 10 的 handler 用例覆盖 403/407/退避；“出口 IP 属 agent 网络”只能在有真实 agent 的环境手工验证，命令写在 `docs/user-guide/http-proxy-entry.md` | 单测输出 + 手工 curl 记录 |
| 3. 无认证模式带错误凭据同样可用，ACL 仍生效 | Task 10 中 `authMode=none` 的用例（不带凭据成功、带错误凭据成功、ACL 外仍 403） | 单测输出 |
| 4. `allowPrivateTargets=true` 可访问内网；169.254.169.254 恒 403 | Task 6 的 `TestTargetPolicy*` 全部用例 | 单测输出 |
| 5. 集群模式下 agent 挂在别的节点时隧道正常，trace hop 完整 | 隧道部分复用既有 relay 夹具的 Task 10 用例；`POST /api/v1/agents/{agentId}/trace` 是既有能力且本 PR 不改其实现，hop 完整性只能手工验证 | 单测输出 + 手工 trace 记录 |
| 6. 门禁全通过、第 16 节文档齐全、OpenAPI 与实现一致 | Step 14 的门禁命令 + Step 12 的索引重生成 + 把 `docs/api/openapi.yaml` 与 Task 12 实现的字段逐条对照 | 门禁输出 + 对照结论 |

任一条无法在本机验证的，必须在 PR 记录里写成“未验证”并给出验证前提（例如需要真实 agent、需要 docker、需要 `TUNNELMESH_TEST_MYSQL_DSN`），不得写成已通过。

(b) 回写 spec 的两处偏差（`docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md`）：

- 第 13 节：管理操作审计事件从 `proxy_route_created|updated|deleted` 改为“复用既有 `route.created` / `route.updated` / `route.deleted`”。理由：`internal/server/api.go` 的 `auditRoute` 已经为所有 `tunnels` 行写这三个动作，再加一套 `proxy_route_*` 会让同一次操作产生两条语义重复的审计记录，也让审计检索多一套命名。运行时的 `proxy_route_denied`、`proxy_auth_failed`、`proxy_tunnel_opened`、`proxy_tunnel_closed`、`proxy_request_forwarded` 五个事件名不变。
- 第 14 节：测试文件名改成计划实际使用的名字——第 5 条为 `internal/server/proxy_entry_test.go` 与 `internal/server/proxy_entry_absolute_test.go`，第 6 条为 `internal/server/api_proxy_route_test.go`，第 7 条为 `internal/storage/credential_repository_test.go` 与 `internal/server/credential_api_test.go`，并补一条 `internal/observability/metrics_proxy_entry_test.go`；门禁命令的 `gofmt -l` 参数补上 `deploy/`（Task 14 在 `deploy/openresty/` 下新增了 Go 测试文件）。第 11 条的容器与 stub 修正已由 Task 14 Step 15 写入，本步骤不重复。
- 第 15 节：把“spike 产物只作为结论文档记录，不进入生产代码”补一句例外——`deploy/openresty/spike-connect-check.sh` 作为只读诊断脚本提交进仓库并被部署文档引用，因为它不参与请求处理，而且是“这台机器的内核到底有没有打补丁”的唯一可执行判据；结论记录仍然写在本计划文件末尾的 `## Task 0 验证记录`。

- [ ] **Step 16: Commit（需授权）**

```bash
git add deploy/grafana/dashboards/tunnelmesh.json deploy/grafana/dashboard_schema_test.go deploy/prometheus/alert-rules.yaml deploy/README.md docs/operations/observability.md docs/operations/troubleshooting.md docs/operations/configuration.md docs/operations/completeness-checklist.md docs/deployment/openresty-proxy-entry.md docs/deployment/nginx.md docs/user-guide/http-proxy-entry.md docs/user-guide/managed-http-route.md docs/architecture/overview.md docs/development/testing.md docs/development/README.md docs/README.md docs/pull-requests/2026-09-13-managed-route-http-proxy-entry.md docs/pull-requests/README.md docs/superpowers/plans/README.md docs/superpowers/specs/README.md docs/architecture/adr/README.md docs/superpowers/specs/2026-09-13-managed-route-http-proxy-entry-design.md README.md README.zh-CN.md
git commit -m "feat(observability): add proxy entry dashboard row, alerts and docs"
```

`docs/operations/configuration.md` 的 `server.proxy_entry` 配置表已由 Task 1 写入，这里只作为兜底加入 `git add` 列表；若 Task 1 已提交，该文件在本次 commit 中不会有变化，属正常。

---

## Task 0 验证记录

执行环境：`Darwin arm64`，Go `go1.27.1`，Node `v24.15.0`，基线 `main` @ `b3acd34`。

### (a) build flags —— 未执行

Run: `nginx -V 2>&1 | tr ' ' '\n' | grep -i "proxy_connect\|add-module\|with-http_ssl"`

未执行，原因是本机没有 OpenResty，也没有任何容器运行时可用来自建一个：

```text
nginx      (not found)
openresty  (not found)
resty      (not found)
docker     (not found)
podman     (not found)
colima     (not found)
nerdctl    (not found)
```

曾尝试 `brew install openresty/brew/openresty` 以后台方式安装，但后台进程随工具会话结束被回收（日志 0 字节）。即使安装成功，Homebrew 的 openresty formula **不带** `ngx_http_proxy_connect_module` 补丁，CONNECT 会被 nginx 直接回 405，跑出来的结果只反映本机安装方式、不反映目标机内核，因此没有继续。

### (b)(c)(d) 明文 CONNECT 经 server 级 access_by_lua —— 未执行

Run: `bash deploy/openresty/spike-connect-check.sh 18443`

未执行（同上，缺 OpenResty 内核）。脚本本身已按 Step 1 写入 `deploy/openresty/spike-connect-check.sh`，并通过 `bash -n` 语法检查；`shellcheck` 本机未安装，未执行。

与计划的一处偏差：脚本用 `run_with_timeout` 包装了 `nc`，因为 macOS 没有 `timeout(1)`（只有 coreutils 的 `gtimeout`）。该脚本会作为部署前置检查在管理员本机运行，缺 `timeout` 时退化为“后台执行 + 定时 kill”，语义不变。

### (e) 非 CONNECT 分支的 hop-by-hop 头 —— 未执行

未执行（同上）。计划已按“必须显式 `proxy_set_header Proxy-Authorization $http_proxy_authorization;`”编写，且该约束由 `deploy/openresty/openresty_artifacts_test.go`（Task 14）以契约测试守护，不依赖本次 spike 的结果。

### 已由上游源码核实的事实（Task 14 外部事实表，非本机经验验证）

- `proxy_connect_rewrite_102101.patch`（模块 tag `v0.0.7`）把 `$connect_host` / `$connect_port` 注册进 `ngx_http_core_variables[]`，删除 nginx 对 CONNECT 的 405 拒绝，并在 `ngx_http_core_find_config_phase` 中 `r->phase_handler++` 跳过 location 匹配 —— 这正是 server 级 `access_by_lua` 能看到 CONNECT、而 `content_by_lua_block` 永不触发的机制。
- `NGX_HTTP_PROXY_CONNECT` 宏由模块 `config` 定义，补丁与模块缺一都会让 CONNECT 失效。
- OpenResty `1.25.3.1` 与该补丁成对（模块 README 的 Compatibility 表）。

### 决策：继续 A3（附强制前置条件）

不触发中止判据：中止判据是“spike 跑出来 `access_by_lua` 收不到 CONNECT / 拿不到 raw socket 或 SNI”，而本次是**无法执行**，不是执行后失败。机制层面已由上游补丁源码核实，A3 的核心假设成立。

但因为缺少经验验证，落地必须满足以下强制前置条件，缺一不可：

1. `server.proxy_entry.enabled` 默认 `false`（Task 1 已实现并有测试守护），代码合入后线上行为与旧版本完全一致，不存在“未验证即生效”的风险。
2. 在目标 OpenResty 主机上启用之前，**必须**先执行 `bash deploy/openresty/spike-connect-check.sh 18443`，确认输出包含 `spike connect_host=example.com connect_port=443` 且 nc 侧收到 `HTTP/1.1 200 Connection Established` 与 `spike-ok`；把实际输出补记到本小节。
3. 若目标机内核没有补丁（收到 405 或 error.log 无 spike 行），改用 `deploy/openresty/Dockerfile.proxy-connect` 构建的内核，或按 spec 第 15 节回退 A2，并把决策写回 spec 第 17 节。
4. Task 14 的 OpenResty 端到端冒烟（`TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs`）需要 docker，本机同样无法执行，按 skip 语义退出 0；必须在有 docker 的环境补跑，并把结果写进 PR 记录的 Test Evidence。

以上第 2、4 条在 PR 记录中必须标注为“未执行 + 原因”，不得写成已通过。

---

## 实施结果与偏离记录（Task 0-15 收尾回写）

执行环境：`Darwin arm64`，Go `go1.27.1`，Node `v24.15.0`。开发基线 `main` @ `b3acd34`；
推送前发现另一会话已向 `origin/main` 推入 4 个提交（`91553a1`、`da994b3`、`6227c17`、`50b4130`），
本 feature 的 17 个提交已 rebase 到 `origin/main` @ `50b4130` 之上，**无冲突**，下表哈希为 rebase
后的最终值。重叠文件与处理方式见 PR 记录的 Integration Status。
本节是全计划的权威执行记录；PR 记录见
[docs/pull-requests/2026-09-13-managed-route-http-proxy-entry.md](../../pull-requests/2026-09-13-managed-route-http-proxy-entry.md)。

计划正文里的 `- [ ]` 复选框**不作为完成度跟踪手段**（Task 0-12 期间也未勾选），完成情况以本节的
提交对照表为准；这样避免“复选框已勾但实际未执行”与“实际已执行但忘记勾选”两类不一致。

### 提交对照

| Task | 提交 | 说明 |
|---|---|---|
| 计划细化 + Task 0 spike | `9981401` | 含 Task 0 验证记录 |
| Task 0 诊断脚本 | `cb1abfe` | `deploy/openresty/spike-connect-check.sh` |
| Task 1 配置项 | `3dce406` | `server.proxy_entry` 13 个键 |
| Task 2 错误模型与路由类型 | `4332a24` | `internal/proxyentry/errors.go`、`route.go` |
| Task 3 路由身份解析 | `97535be` | header 与 SNI 两种实现 |
| Task 4 源 IP ACL | `2122f21` | fail-closed |
| Task 5 Basic 认证与退避 | `d54e786` | 常量时间比较 + 指数退避 |
| Task 6 目标地址校验 | `342e739` | SSRF / 私网 / 端口策略 |
| Task 7 proxy 路由快照 | `700ecf0` | `http-proxy` 行不进反代路由表 |
| Task 8 `proxy_basic` 凭据 | `4c98af3` | 复用既有 secret store |
| Task 9 内部监听与可信 peer | `3503fd2` | 读请求前直接关闭不可信 peer |
| Task 10 CONNECT 隧道 | `3911b0b` | 限额、指标、审计 |
| Task 11 绝对形式转发 | `c7d9fc9` | 非 CONNECT 分支 |
| Task 12 管理 API | `72416d7` | 同时修掉 Task 9 的 `-race` 缺陷（见下） |
| Task 13 管理后台 | `dbc0fc5` | Vue + i18n + 前端测试 |
| Task 14 OpenResty 产物 | `22527be` | Lua / conf / Dockerfile / E2E |
| Task 15 收尾 | 本次提交（HEAD） | Dashboard row、告警、全部文档、PR 记录；本表自身也在这次提交里更新，因此不写死哈希 |

### 门禁结果（Task 15 Step 14）

| 命令 | 结果 |
|---|---|
| `go test ./... -count=1` | 20 个包全部 ok，0 FAIL |
| `go test -race -timeout 40m ./...` | 退出码 0，20 个包全部 ok；最慢 `internal/server` 291.594s |
| `go vet ./...` | 退出码 0，无输出 |
| `gofmt -l internal/ cmd/ deploy/ test/` | 无输出 |
| `git diff --check` | 无输出 |
| `go test ./deploy/... -count=1` | grafana / install / openresty 全部 ok |
| `cd web && npm test -- --run` | 30 文件、243 用例通过 |
| `cd web && npm run build` | 成功并镜像到 `internal/server/web_dist` |
| `./scripts/verify-web-embed.sh` | `web/dist and internal/server/web_dist match` |
| `python3 scripts/gen_doc_index.py` | 四份索引重生成，二次执行幂等 |
| `node test/e2e/proxy-entry/run.mjs` | SKIP（未设 `TM_PROXY_E2E_NGINX`），退出码 0 |
| `TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs` | SKIP（缺 docker），退出码 0 |

### 未执行项（必须在具备条件的环境补跑，不得记为已通过）

1. **Task 14 Step 13**：`TM_PROXY_E2E_NGINX=1` 的完整 13 条断言。原因是本机无 docker 且拉不到
   `openresty.org` / `github.com`。已按计划的中止判据降级为手工步骤，并在
   `deploy/openresty/README.md` 与 `test/e2e/proxy-entry/README.md` 顶部标注验证状态。未放宽任何断言。
2. **Task 14 Step 5 的镜像构建**：同上，`Dockerfile.proxy-connect` 未经本机构建验证；版本 pin 与
   `./configure → patch → make` 顺序由 `openresty_artifacts_test.go` 静态守护。
3. **Task 15 Step 5 的 `promtool check rules`**：本机无 `promtool`，改用 `ruby -ryaml` 解析，
   结果 `groups=1 rules=9`（既有 7 条 + 新增 2 条）。
4. **真实 MySQL contract**：未设置 `TUNNELMESH_TEST_MYSQL_DSN`，只跑了 SQLite 方言。
5. **spec 第 18 节验收标准第 1、2、5 条的现网部分**：需要真实 Agent、真实 OpenResty 与 DNS，
   已在 PR 记录的验收对照表里逐条标注“部分验证 / 未验证”。
6. **Task 0 强制前置条件第 2 条**（在目标 OpenResty 主机执行 `spike-connect-check.sh` 并把输出
   补记到 `## Task 0 验证记录`）仍然待办：启用 `server.proxy_entry.enabled` 之前必须执行。

### 逐任务偏离

- **Task 12**：额外修复 Task 9 引入的 `-race` 缺陷——`runtime.go` 的 `ServeListener` 返回前没有
  等待代理入口解绑，新增 `entryDone` 同步。
- **Task 13**：① 列表“活跃隧道数”列改为抽屉内指向 Grafana（Row `HTTP Proxy Entry`、面板
  `Proxy tunnels active`），因管理 API 未暴露该聚合，已回写 spec §10 与 §17；② 代理地址/认证方式/
  ACL 三列用 `v-if="hasProxyRoutes"` 只在存在 tp-* 路由时渲染，避免整页横向滚动条；
  ③ `buildProxyRouteUpdate()` 只提交策略字段，不带 `targetHost`/`targetPort`（PATCH 只接受哨兵值）；
  ④ 顶层 i18n 补 `credentials.proxyBasic` 以镜像既有 `sshPublicKey` 约定；⑤ 抽屉补充
  macOS/Windows/PAC/curl/错误码等 i18n 键（中英对齐），超出计划列出的键清单；
  ⑥ 计划文件清单里的 `internal/server/web_dist` **未纳入提交**：该路径被 `.gitignore` 的
  `internal/server/web_dist/*` 忽略且历史上从未被跟踪，构建产物由 `npm run build` +
  `scripts/verify-web-embed.sh` 本地生成校验。
- **Task 14**：① `deploy/openresty/README.md` 收录了 Task 0 留下的 `spike-connect-check.sh`，
  计划的“四个文件”实际是五个产物，`deploy/README.md` 的校验小节同步补了 `bash -n` 一行；
  ② spec §5.1 不再内联完整 nginx 配置，改为引用模板产物 + 结构约束清单；③ spec §14 第 11 条除改成
  Node stub 外，把镜像来源从 `openresty/openresty:alpine` 更正为自建 `Dockerfile.proxy-connect`
  镜像——未打补丁的官方镜像对 CONNECT 一律 405；④ `test/e2e/proxy-entry/README.md` 用中文
  （与 `docs/`、`deploy/README.md` 一致），章节结构对齐 `test/e2e/webssh/README.md`；
  ⑤ Lua/conf/Dockerfile/mjs 由脚本从本计划的代码块逐字提取以保证与已批准计划一致，Go 测试文件
  提取后执行 `gofmt -w`（计划代码块是 4 空格缩进）。
- **Task 15**：① `docs/operations/observability.md` 的“应用已占用标签名”清单补了 `route`
  （计划只要求改“标签和留存”小节，但 `route` 是新引入的应用标签，不进这份清单会让 target label
  覆盖告警失效）；② `docs/development/testing.md` 开头“三层验证”改为“各层验证”，并在产物一致性
  测试表补 `deploy/openresty/openresty_artifacts_test.go` 一行（计划未列，但该表就是这份清单的
  权威来源）；③ `docs/README.md` 的文档地图同时在 `user-guide/` 行补了“HTTP 代理入口”
  （计划只要求 `deployment/` 行）；④ 顺带修正 `docs/operations/configuration.md` 中
  `auth_backoff_threshold` 的错误描述——Task 1 写成“返回 429”，实现返回的是 407 与稳定错误码
  `proxy_auth_backoff`；⑤ `docs/architecture/overview.md` 的链路图把 `access_by_lua_block` 更正为
  `server 级 access_by_lua_file`，与实际产物一致；⑥ 不新建 ADR 文件，spec 本身即 ADR 载体
  （与计划一致）。

### rebase 后的复验

rebase 引入了远端对 `web/src/i18n/messages/{zh-CN,en-US}.ts`（`downloads` 键）、
`web/src/views/Downloads.vue`、`web/src/layouts/AppShell.vue` 与两份前端测试的修改，因此重跑了前端
门禁：`npm test -- --run` 30 文件 / **244** 用例通过（rebase 前为 243，远端新增 1 例）、
`npm run build` 成功并镜像到 `internal/server/web_dist`、`./scripts/verify-web-embed.sh` 输出
`web/dist and internal/server/web_dist match`。Go 侧未受远端提交影响（远端只改了 web、CI workflow
与一篇文档），`go vet ./...`、`gofmt -l`、`git diff --check` 与 `go test ./deploy/... -count=1`
在 rebase 后重跑均通过。
