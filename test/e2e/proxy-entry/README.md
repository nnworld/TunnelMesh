# tp-* HTTP 代理入口 E2E（OpenResty 层冒烟）

> **验证状态**：`Dockerfile.proxy-connect` 构建出的镜像与下表 13 条断言**尚未在本机执行过**——
> 开发环境没有 docker，也拉不到 `openresty.org` / `github.com`。已在本机验证的只有 skip 分支
> （未设置 `TM_PROXY_E2E_NGINX` 与缺 docker 两种）与 `deploy/openresty` 的产物一致性测试。
> 合并前必须在具备 docker 的环境跑一次 `TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs`，
> 断言不得为了让脚本通过而放宽。

只覆盖 `deploy/openresty/` 的搬运层：请求头白名单、CONNECT 双向 splice、非 200 响应原样透传。
Server 侧的路由身份解析、源 IP ACL、Basic 认证与退避、目标校验、并发限额、审计与指标由
`internal/proxyentry` 与 `internal/server` 的 Go 测试覆盖，这里不重复。

本脚本**不属于** `go test ./...`，也不属于 `web` 的 `npm test`；改动 Lua、conf 模板或
Dockerfile 后需要单独执行一次。

## 断言

| # | Check | 为什么存在 |
| --- | --- | --- |
| 1 | `CONNECT is answered with 200 Connection Established` | 内核补丁与 server 级 `access_by_lua_file` 生效的最小证明；没打补丁时 nginx 直接回 405 |
| 2 | `route identity comes from SNI, not from a client header` | 客户端自带 `X-TunnelMesh-Route` 时必须被丢弃，否则可冒充另一条路由绕过其 ACL 与凭据 |
| 3 | `a forged client IP is replaced by the real peer` | 源 IP ACL 的唯一输入；伪造 `X-TunnelMesh-Client-IP` 等于 ACL 形同虚设 |
| 4 | `Proxy-Authorization is passed through verbatim` | hop-by-hop 头，nginx 默认不转发；漏掉它所有请求都会 407 |
| 5 | `non-whitelisted client headers are dropped` | 搬运层只写白名单头，杜绝客户端注入任意头干扰 Server 解析 |
| 6 | `the tunnel carries 256 KiB in both directions byte-for-byte` | 双向 splice 与字节完整性；同时验证 cosocket 超时没有截断长连接 |
| 7 | `the tunnel is released when the client goes away` | `lua_check_client_abort` + `ngx.on_abort` 回归：泄漏表现为 Server 侧隧道数持续上涨 |
| 8 | `403 from the internal entry is relayed verbatim` | 稳定错误码 `proxy_source_denied` 必须原样到达客户端 |
| 9 | `407 from the internal entry is relayed verbatim` | `Proxy-Authenticate: Basic realm="TunnelMesh", charset="UTF-8"` 不能被吞，否则浏览器/curl 不弹认证 |
| 10 | `503 from the internal entry is relayed verbatim` | `Retry-After: 5` 不能被吞，否则客户端不会退避 |
| 11 | `absolute-form requests arrive as origin-form with the trusted headers` | 非 CONNECT 的 `GET http://host/path` 分支：nginx 改写成 origin-form，可信头由 `location /` 注入 |
| 12 | `the error log records tunnel close with route and byte counters` | 排障依赖：error_log 至少 info 级才能看到 route/target/字节数/时长/结束原因 |
| 13 | `the error log never contains the proxy credential` | 凭据泄漏防线：`Proxy-Authorization` 任何情况下都不得进日志 |

## 前置条件

- Node.js **20 或更新**（脚本无 npm 依赖，只用内置模块）。
- docker，且 daemon 可达（`docker info` 成功）。
- `openssl`（生成自签通配证书）。
- 首次运行会 `docker build` 编译 OpenResty 补丁内核，耗时数分钟；之后可用
  `TM_PROXY_E2E_SKIP_BUILD=1` 复用镜像。
- 本机空闲端口 `18443`（OpenResty）与 `18089`（内部入口替身）。

不满足前置条件时脚本打印缺失项并以 0 退出（见下方 skip 语义），不会误报通过。

## 运行

```bash
TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs
```

只有全部 check 通过时退出码才是 `0`。渲染后的 conf、Lua、`container.log` 与 `results.json`
都留在工作目录（默认 `$TMPDIR/tunnelmesh-proxy-entry-e2e`），最后一行会打印路径。

快速迭代：

```bash
TM_PROXY_E2E_NGINX=1 TM_PROXY_E2E_SKIP_BUILD=1 node test/e2e/proxy-entry/run.mjs
```

## 配置

全部可选，默认值可直接在干净检出上运行。

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `TM_PROXY_E2E_NGINX` | 未设置 | 必须为 `1` 才真正运行，否则 skip |
| `TM_PROXY_E2E_IMAGE` | `tunnelmesh/openresty-proxy-connect:1.25.3.1` | 使用的镜像 tag |
| `TM_PROXY_E2E_SKIP_BUILD` | 未设置 | `1` 表示跳过 `docker build`，直接用现成镜像 |
| `TM_PROXY_E2E_PORT` | `18443` | OpenResty 监听端口（绑定到 `127.0.0.1`） |
| `TM_PROXY_E2E_STUB_PORT` | `18089` | 内部入口替身端口，容器内经 `host.docker.internal` 回连 |
| `TM_PROXY_E2E_DOMAIN` | `proxy.test` | 域名后缀，用于自签证书 SAN 与 `server_name` 正则 |
| `TM_PROXY_E2E_ROUTE` | `e2e` | 路由名，最终 SNI 为 `tp-<route>.<domain>` |
| `TM_PROXY_E2E_DIR` | `$TMPDIR/tunnelmesh-proxy-entry-e2e` | 工作目录 |
| `TM_PROXY_E2E_REPO` | 由本文件推导 | 仓库根，用于定位 `deploy/openresty/` 产物 |

## skip 语义

未设置 `TM_PROXY_E2E_NGINX=1`，或 docker/openssl/产物文件缺失时，脚本打印
`SKIP openresty proxy entry smoke :: <原因>` 并以退出码 `0` 结束。skip 一定会打印原因，
不会静默通过；CI 里要强制执行就显式设置 `TM_PROXY_E2E_NGINX=1` 并保证 docker 可用。

## 目录

```
run.mjs          场景脚本：上面的断言自上而下
lib/harness.mjs  配置解析、镜像构建、产物渲染、OpenResty 容器生命周期
lib/stub.mjs     内部入口替身：按 CONNECT 目标主机名编排 200/403/407/503 与回声
```

替身按**目标主机名**编排（`ok.test` 回声、`denied.test` 403、`authfail.test` 407、
`capacity.test` 503），不按请求头编排——Lua 只透传白名单头，用请求头编排根本传不进来，
而这正是要验证的行为。

## 失败排查

1. 先看 `<workdir>/container.log`：容器里 `error_log /dev/stderr info`，隧道关闭统计与拒绝原因
   都在里面。
2. 再看 `<workdir>/results.json` 与 `== summary ==`，定位第一条失败的 check。
3. 渲染后的 `tunnelmesh-proxy.conf`、`tunnelmesh_proxy_entry.lua` 与 `nginx.conf` 也在
   `<workdir>`，可直接 `nginx -t -c <workdir>/nginx.conf` 复查。
4. 全部 CONNECT 都回 405：镜像内核没打 `proxy_connect` 补丁，用
   `docker run --rm <image> -V | tr ' ' '\n' | grep proxy_connect` 确认。
5. check 2/3 失败：说明 `access_by_lua_file` 写在了 location 级，CONNECT 会跳过 location 匹配。

## 已知限制

- 需要 docker；无 docker 或无法访问 `openresty.org` / `github.com` 的环境只能跑 skip 分支。
- 只覆盖 OpenResty 层，不覆盖 Server 与 Agent；完整链路（浏览器 → OpenResty → Server →
  Agent → 目标）需要在具备 docker 的环境按
  [OpenResty 代理入口部署](../../../docs/deployment/openresty-proxy-entry.md) 手工验证一次。
- macOS 与 Linux 已验证；Windows 需要可用的 docker daemon 与 POSIX `openssl`。
