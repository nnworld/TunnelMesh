# HTTP 代理入口（tp-*）

把 `https://tp-<name>.<domain>` 填进浏览器或操作系统的“HTTPS 代理”，就能从任意被允许的来源 IP
经指定 Agent 出网或访问该 Agent 的内网服务，**用户机器上不需要安装 `tunnelmesh-client`**。
出口 Agent、认证方式与来源 ACL 全部由管理员在后台集中配置，创建后 5 秒内生效。

反向代理路由（显式子域名与 `tm-*` 泛域名）是另一件事，见[托管 HTTP 路由](managed-http-route.md)。

## 与 client 本地代理的区别

| | `tunnelmesh-client forward socks5` / `forward http-proxy` | 托管入口 `tp-*`（本文） |
| --- | --- | --- |
| 用户机器 | 必须安装并运行 client | 什么都不用装 |
| 监听位置 | 用户本机端口 | 管理员的公网 443（复用既有入口，不新增端口） |
| 支持协议 | SOCKS5 与 HTTP 代理都支持 | 只支持 HTTP/HTTPS 代理（`CONNECT` 与绝对形式请求） |
| 出口选择 | client 命令行指定 Agent | 管理员在路由上指定 Agent |
| 认证 | client 本地可选 | 管理员配置：无认证 / 用户名密码（Basic） |
| 来源控制 | 本机监听地址 | 源 IP/网段 ACL，默认拒绝所有来源 |
| 目标控制 | client 侧策略 | 管理员配置目标网段、端口、是否允许内网目标 |
| 审计与指标 | client 侧日志 | Server 侧审计事件 + Prometheus 指标 + Grafana Row |

两者可以并存：需要 SOCKS5、或需要用户自己掌控出口时用 client；要给一批人免安装、可集中管控的
出网通道时用 `tp-*`。

## 管理员：后台操作

路由管理 → 添加托管路由 → 协议选 **HTTP 代理入口 (tp-)**，表单会切换成代理专属字段：

| 字段 | 说明 |
| --- | --- |
| 代理名称 | 只填 `<name>`，小写字母、数字与中划线，最长 32 字符；最终域名自动拼成 `tp-<name>.<domain_suffix>`，表单下方实时预览完整域名 |
| 代理节点 | 出口 Agent。一条路由只有一个出口，没有负载与故障转移 |
| 认证方式 | `无认证` 或 `用户名密码` |
| 代理凭据 | 认证方式为“用户名密码”时必填。需要先在**密钥管理**里新建一个 `代理账号`（`proxy_basic`）类型的凭据，填 username 与 password；密码用 AES-256-GCM 加密保存，列表与日志都不会回显 |
| 允许的来源 IP/网段 | 逗号分隔的 CIDR。**默认拒绝所有来源**；`0.0.0.0/0` 表示放开全部，也可以直接点“放开全部来源”。裸 IP 会自动补 `/32`（IPv6 补 `/128`） |
| 目标网段限制 | 可选。留空表示不额外限制（仍受危险地址过滤约束） |
| 目标端口限制 | 可选。留空表示任意端口；对域名目标和 IP 字面量都生效 |
| 允许访问内网目标 | 默认开。关闭后回环、私网、链路本地与 ULA 目标一律拒绝；云 metadata 地址（如 `169.254.169.254`）**无论开关都恒拒** |
| 并发隧道上限 | 单路由上限，`0` 表示只受服务端全局上限约束 |
| 备注 | 256 字以内，只在后台展示 |

保存后列表会多出“代理地址”“认证方式”“ACL 条数”三列（只在存在 tp-* 路由时渲染，避免表格出现
横向滚动条），“目标”列显示“按请求动态决定”而不是哨兵 `*:0`。点“使用说明”打开抽屉，里面有完整
代理 URL 与各端配置示例，可直接复制发给使用者。活跃隧道数不在列表里，看 Grafana 统一 Dashboard
的 Row `HTTP Proxy Entry` → 面板 `Proxy tunnels active`。

生效时间 <= 5 秒，**不需要重启 Server，也不需要改 nginx**（前提是 OpenResty 侧已按
[OpenResty tp-* 代理入口](../deployment/openresty-proxy-entry.md) 部署好，且泛解析 DNS 与通配证书
覆盖 `*.<domain_suffix>`）。

## 管理员：API 方式

```bash
curl -sS -X POST https://tm.example.com/api/v1/routes \
  -H "Authorization: Bearer <token>" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: 3f2b8c1e-6d47-4a19-9c0e-2b7f0a5d9e11' \
  --data-raw '{
    "agentId": "agt_01HZX...",
    "protocol": "http-proxy",
    "domain": "tp-demo.tm.example.com",
    "authMode": "basic",
    "credentialId": "cred_01HZY...",
    "sourceCIDRs": ["11.71.85.0/24"],
    "targetPorts": [80, 443],
    "allowPrivateTargets": true,
    "maxConcurrentTunnels": 64,
    "description": "研发出网通道"
  }'
```

要点：

- `domain` 必须是**完整域名** `tp-<name>.<suffix>`，服务端不接受只传 `<name>`；`<name>` 小写、
  最长 32 字符。
- **不要传** `targetHost` / `targetPort` / `config` / `hostHeader` / `targetScheme` / `tlsServerName`。
  真实目标来自每个代理请求，服务端会把目标固定成哨兵 `*` / `0`。更新（PATCH）时同理，带上非哨兵值
  会被拒绝。
- `authMode` 为 `basic` 时 `credentialId` 必填，且必须指向一个调用者有权读取、处于启用状态的
  `proxy_basic` 凭据。
- 一条 `http-proxy` 路由**独占整个域名**：同域名下再建任何路由都会 409。
- 写操作支持 `Idempotency-Key`，重放同一个键返回已存的 201 响应而不是 409。

三类常见 400：

| 场景 | 服务端返回 |
| --- | --- |
| 传了 `targetHost` 或 `targetPort` | `targetHost must be omitted for http-proxy routes` / `targetPort must be omitted for http-proxy routes` |
| 在 `http-proxy` 与 `http`/`websocket` 之间切换协议 | `protocol cannot be changed to or from http-proxy; create a new route` |
| CIDR 或端口非法 | 对应的字段校验错误；非法 ACL 条目按“拒绝”处理，不会静默放开 |

字段级完整定义见 [openapi.yaml](../api/openapi.yaml)。

## 使用者：各端配置

代理地址形如 `https://tp-demo.tm.example.com`（**scheme 必须是 `https://`，端口 443 可省略**）。

> **只能填 `http://` 代理地址的旧客户端无法使用本入口。** 路由身份来自 TLS SNI，凭据也必须加密
> 传输；填 `http://` 意味着明文，服务端不会接受。

**macOS**：系统设置 → 网络 → 当前网卡 → 详细信息 → 代理 → 打开“安全网页代理（HTTPS）”，
服务器填 `tp-demo.tm.example.com`，端口 `443`，勾选“代理服务器要求密码”并填用户名密码。
命令行临时使用：

```bash
networksetup -setsecurewebproxy Wi-Fi tp-demo.tm.example.com 443
# 带 Basic 认证时，用户名与密码是第 4、5 个位置参数
networksetup -setsecurewebproxy Wi-Fi tp-demo.tm.example.com 443 '<username>' '<password>'
# 用完关闭
networksetup -setsecurewebproxystate Wi-Fi off
```

**Windows**：Internet 选项 → 连接 → 局域网设置 → 勾选“为 LAN 使用代理服务器”→ 高级，
在 **HTTPS** 一栏填 `tp-demo.tm.example.com:443`，并勾选“对 HTTPS 使用相同代理”。

**PAC（Chrome / Firefox / 系统自动配置）**：

```js
function FindProxyForURL(url, host) {
  if (host === "localhost" || host === "127.0.0.1" || isPlainHostName(host)) return "DIRECT";
  return "HTTPS tp-demo.tm.example.com:443";
}
```

**curl**：

```bash
# HTTPS 目标（走 CONNECT）
curl -x https://tp-demo.tm.example.com --proxy-user 'u:p' https://ifconfig.me

# 自签证书验证阶段加 --proxy-insecure；正式通配证书不需要
curl -sv --proxy-insecure -x https://tp-demo.tm.example.com --proxy-user 'u:p' https://ifconfig.me

# 明文 HTTP 目标（绝对形式请求，同样支持）
curl -x https://tp-demo.tm.example.com --proxy-user 'u:p' http://example.com/
```

**SwitchyOmega 之类浏览器插件**：代理协议选 `HTTPS`（不是 `HTTP`，也不是 `SOCKS5`），服务器填
`tp-demo.tm.example.com`，端口 `443`，认证填 Basic 用户名密码。

**Firefox** 可以单独设置代理而不影响系统：设置 → 网络设置 → 设置 → 手动配置代理 →
“也将此代理用于 HTTPS”勾选，SSL 代理填 `tp-demo.tm.example.com:443`。

## 错误码自助排查

所有拒绝都带稳定错误码，`curl -v` 或服务端审计日志里都能看到。隧道建立成功之后的失败只会表现为
连接断开，不会再有状态码。

| 状态码 | 错误码 | 你会看到什么 | 为什么 | 你能做什么 |
| --- | --- | --- | --- | --- |
| 400 | `proxy_target_invalid` | `Received HTTP code 400 from proxy` | 目标主机名或端口非法（端口不在 1-65535、主机名为空或含非法字符） | 检查请求的 URL；如果配了目标端口限制，确认端口在允许列表里 |
| 403 | `proxy_route_identity_invalid` | `Received HTTP code 403 from proxy after CONNECT` | 路由身份非法：SNI 缺失、不是 `tp-<name>.<suffix>` 形式，或后缀不匹配 | 确认代理地址填的是完整域名且 scheme 为 `https://`；确认 DNS 泛解析与证书覆盖该后缀 |
| 403 | `proxy_route_unavailable` | 同上 | 路由不存在或已停用 | 找管理员确认路由名与启用状态 |
| 403 | `proxy_source_denied` | 同上 | 你的来源 IP 不在该路由的 ACL 里 | 找管理员把来源网段加进 ACL；注意 NAT/多层代理下服务端看到的是出口地址 |
| 403 | `proxy_target_denied` | 同上 | 目标被策略拒绝：命中危险地址（云 metadata 恒拒）、目标网段限制，或关闭了“允许内网目标”却访问内网 | 换目标，或找管理员调整目标网段/端口/内网开关 |
| 407 | `proxy_auth_required` | 浏览器弹认证框，或 curl 报 407 | 该路由要求 Basic 认证，但请求没带 `Proxy-Authorization` 或格式非法 | 补上用户名密码（`--proxy-user 'u:p'`） |
| 407 | `proxy_auth_failed` | 同上 | 用户名或密码不匹配，或凭据被停用/删除 | 核对凭据；找管理员确认凭据状态 |
| 407 | `proxy_auth_backoff` | 连续失败后即使密码正确也持续 407 | 同一路由连续认证失败达到阈值（默认 5 次）后进入退避：30 秒起每次翻倍，上限 15 分钟。退避期内**不做密码比对**直接 407 | 停止重试，等退避窗口过去；管理员可在指标里看到 `reason="proxy_auth_backoff"` |
| 502 | `proxy_egress_unavailable` | `Received HTTP code 502 from proxy` | 出口不可用：Agent 离线、内部入口不可达，或集群跨节点 relay 失败 | 找管理员查 Agent 在线状态与 relay |
| 503 | `proxy_capacity_exhausted` | 503，响应带 `Retry-After: 5` | 并发隧道超限（全局或单路由） | 按 `Retry-After` 退避重试；找管理员调高上限 |
| 503 | `credential_secret_unavailable` | 503 | 服务端凭据密文存储不可用（未配置 `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 或解密失败） | 找管理员检查密钥注入，这不是客户端问题 |
| 504 | `proxy_egress_timeout` | 504 | `connect_timeout`（默认 10s）内没能通过 Agent 建立到目标的连接 | 确认目标可达、Agent 在线；必要时找管理员调整超时 |

三个 403 对外文案刻意一致，避免外部枚举哪些 `tp-*` 名字存在；只有服务端日志、审计事件
`proxy_route_denied` 的 `reason` 与指标 `error_class` 能区分具体原因。

## 限制

- **不支持 SOCKS5**。需要 SOCKS5 请用 `tunnelmesh-client forward socks5`。
- **不支持公网 UDP 入口**。公网侧只有 HTTP/HTTPS/WebSocket；UDP 只能通过 `forward udp` 从用户侧
  发起到 Agent 内网。
- **一条路由只有一个出口 Agent**，没有负载与故障转移；Agent 离线时该路由直接 502。
- Basic 认证是**路由级共享账号**，没有 per-user 配额，也无法区分是哪个人在用；需要区分请为不同
  人群建不同路由。
- 隧道建立之后的失败只会表现为连接断开，不会再有状态码——排障要看服务端日志与指标。
- 目标侧仍会做二次校验：Agent 侧对目标地址再做一遍 SSRF、回环、私网、链路本地、CIDR 与端口策略
  检查，管理端放开不等于 Agent 一定放行。
