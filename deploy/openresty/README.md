# deploy/openresty

本目录只放 tp-* HTTP 代理入口的 OpenResty 产物。部署步骤、生产渲染示例、nginx 推荐配置与
排障流程写在 [OpenResty 代理入口部署](../../docs/deployment/openresty-proxy-entry.md)；冒烟
脚本的用法写在 [test/e2e/proxy-entry](../../test/e2e/proxy-entry/README.md)。此处不重复。

## 文件用途

| 路径 | 内容 |
| --- | --- |
| `tunnelmesh_proxy_entry.lua` | CONNECT 搬运层，只搬字节不做策略判断；由 server 级 `access_by_lua_file` 调用 |
| `tunnelmesh-proxy.conf.example` | tp-* server 块模板，渲染占位符后 include 进既有 `http{}` |
| `Dockerfile.proxy-connect` | OpenResty + `ngx_http_proxy_connect_module` 补丁内核镜像 |
| `spike-connect-check.sh` | 部署前置检查：确认目标机的 OpenResty 能把 CONNECT 交给 server 级 `access_by_lua` |
| `openresty_artifacts_test.go` | 产物与 Go 配置默认值的一致性契约，随 `go test ./deploy/...` 执行 |

策略全部在 Server（Go）：路由身份、源 IP ACL、Basic 认证、目标校验、并发限额、审计与指标。
Lua 里出现任何 ACL、密码或路由查询逻辑都是缺陷——那会形成第二份权威并与 Go 侧校验静默漂移，
`openresty_artifacts_test.go` 会直接失败。

## conf 模板占位符

| 占位符 | 取值来源 |
| --- | --- |
| `__LISTEN__` | 监听地址；生产写 `443`，本地验证写 `127.0.0.1:18443` |
| `__SERVER_NAME_REGEX__` | 由 `server.proxy_entry.domain_suffix` 推导的 tp-* 主机名正则 |
| `__SSL_CERT__`、`__SSL_CERT_KEY__` | 覆盖 `*.<domain_suffix>` 的通配证书与私钥路径 |
| `__LUA_FILE__` | `tunnelmesh_proxy_entry.lua` 的绝对路径 |
| `__INTERNAL_UPSTREAM__` | 必须等于 `server.proxy_entry.listen`，默认 `127.0.0.1:8089` |
| `__EDGE_ALLOW__` | 粗粒度来源白名单（`allow`/`deny` 指令）；按路由的细粒度 ACL 在 Server 侧执行 |

## Lua 渲染标记

`tunnelmesh_proxy_entry.lua` 的 `CONFIG` 表用行尾标记定位替换：

| 行尾标记 | 取值来源 |
| --- | --- |
| `-- __TM_INTERNAL_HOST__` | `server.proxy_entry.listen` 的 host |
| `-- __TM_INTERNAL_PORT__` | `server.proxy_entry.listen` 的 port |
| `-- __TM_READ_TIMEOUT_MS__` | `server.proxy_entry.idle_timeout + 30s` 换算成毫秒（默认 330000） |

只替换值，不要删除标记本身：部署渲染与 E2E 都靠标记定位。默认值与 Go 配置默认值的一致性由
`openresty_artifacts_test.go` 守护，改配置默认值必须同步改 Lua。

## 构建与校验

```bash
docker build -f deploy/openresty/Dockerfile.proxy-connect -t tunnelmesh/openresty-proxy-connect:1.25.3.1 .
docker run --rm tunnelmesh/openresty-proxy-connect:1.25.3.1 -V 2>&1 | tr ' ' '\n' | grep proxy_connect
nginx -t -c /path/to/rendered/nginx.conf
```

第二条确认内核带了 `proxy_connect`（没有它 nginx 会对每个 CONNECT 回 405），第三条校验渲染后
的模板。版本必须成对：OpenResty `1.25.3.1` 对应 `proxy_connect_rewrite_102101.patch`，模块 tag
`v0.0.7`。换版本前先读上游
[Select patch](https://github.com/chobits/ngx_http_proxy_connect_module#select-patch)，并同步
`openresty_artifacts_test.go` 里的 pin 断言。

> **验证状态**：开发环境没有 docker，`Dockerfile.proxy-connect` 的构建与上面的 `nginx -V` 校验
> 属于**未在本机执行的手工步骤**；Dockerfile 里的版本 pin 与构建顺序由
> `openresty_artifacts_test.go` 静态守护（上游 README 的 Compatibility 表与补丁源码已核实）。
> 首次部署前必须在目标环境实际构建一次，并用 `spike-connect-check.sh` 确认 CONNECT 能进到
> server 级 `access_by_lua`。

## 发布归档

本目录不进 `scripts/build-release.sh` 的发布归档，与 `prometheus/`、`grafana/` 同属运维自行
挂载或自行构建的产物。
