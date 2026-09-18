# deploy/ 部署产物

本目录只存放**可直接使用的部署产物**：服务模板、安装脚本、Prometheus 配置与 Grafana Dashboard。
操作步骤、参数解释和排障流程一律写在 [docs/](../docs/README.md)。两边不重复内容——
产物在这里，说明在 docs。

## 目录内容

| 路径 | 内容 | 对应文档 |
| --- | --- | --- |
| `systemd/tunnelmesh-server.service`、`tunnelmesh-agent.service`、`tunnelmesh-client.service` | Linux systemd unit，每角色一份，脚本直接安装不做替换 | [Linux systemd 安装](../docs/deployment/linux-systemd.md) |
| `macos/tunnelmesh.plist` | launchd 用户级服务模板，三角色共用，占位符渲染 | [macOS launchd 安装](../docs/deployment/macos-launchd.md) |
| `windows/tunnelmesh-service.xml` | WinSW 服务模板，三角色共用，占位符渲染 | [Windows Service 安装](../docs/deployment/windows-service.md) |
| `install/linux-install.sh`、`macos-install.sh`、`windows-install.ps1`、`windows-uninstall.ps1` | 安装与卸载脚本 | 同上三篇 |
| `openresty/tunnelmesh_proxy_entry.lua`、`openresty/tunnelmesh-proxy.conf.example`、`openresty/Dockerfile.proxy-connect` | tp-* HTTP 代理入口的 OpenResty 搬运层、server 块模板与补丁内核镜像 | [OpenResty 代理入口部署](../docs/deployment/openresty-proxy-entry.md) |
| `prometheus/prometheus.yml.example` | Prometheus 抓取起点配置（单节点与集群两种形态） | [可观测性](../docs/operations/observability.md) |
| `prometheus/recording-rules.yaml`、`alert-rules.yaml` | 录制规则与告警规则 | 同上 |
| `grafana/dashboards/tunnelmesh.json` | 唯一 Dashboard，内部按 Overview / Agent / Network / Cluster / Security / HTTP Proxy Entry 六个 Row 组织 | 同上 |
| `grafana/provisioning/dashboards.yml`、`datasources.yml` | Grafana 自动装载配置 | 同上 |
| `grafana/dashboard_schema_test.go`、`install/install_templates_test.go`、`openresty/openresty_artifacts_test.go` | 产物一致性测试，随 `go test ./deploy/...` 执行 | [测试与验证](../docs/development/testing.md) |

## 模板约定

- `macos/tunnelmesh.plist` 占位符：`__ROLE__`、`__HOME__`、`__BINARY__`、`__CONFIG__`。
- `windows/tunnelmesh-service.xml` 占位符：`__ROLE__`、`__ROLE_TITLE__`、`__BINARY__`、`__CONFIG__`、`__INSTALL_DIR__`。
- 每个角色只允许有一份模板来源：安装脚本只做占位符替换，不得内联生成第二份模板。历史上
  client 用 checked-in 模板、server/agent 用脚本内联生成，结果 client 分支原样拷贝模板并
  静默忽略调用方传入的配置路径，Windows 的日志目录也与文档不一致。
- 模板与脚本中不得出现 Token、密码、私钥或生产 DSN；敏感值只通过配置文件或环境变量注入。SSO / MFA 所需的 `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 同样只走环境注入，见[身份密钥注入](#身份密钥注入sso--mfa-必需)。
- `openresty/tunnelmesh-proxy.conf.example` 占位符：`__LISTEN__`、`__SERVER_NAME_REGEX__`、
  `__SSL_CERT__`、`__SSL_CERT_KEY__`、`__LUA_FILE__`、`__INTERNAL_UPSTREAM__`、`__EDGE_ALLOW__`。
- `openresty/tunnelmesh_proxy_entry.lua` 的 `CONFIG` 表用行尾标记定位替换：`-- __TM_INTERNAL_HOST__`、
  `-- __TM_INTERNAL_PORT__`、`-- __TM_READ_TIMEOUT_MS__`；只替换值，不要删除标记本身。这些默认值
  必须与 `server.proxy_entry.*` 保持一致，由 `openresty/openresty_artifacts_test.go` 守护。

## 监控产物约定

- `prometheus/prometheus.yml.example` 必须为每个 Server target 注入 `cluster` 与 `node_id`
  两个 target label。应用自身不输出这两个标签，Dashboard 的 `$cluster`/`$node_id` 变量完全依赖
  它们；`node_id` 取值必须等于 `server.node.id` / `TUNNELMESH_NODE_ID`。
- target label 不得与应用标签同名，否则 `honor_labels: false` 会把原标签改写成 `exported_*`，
  使 recording 与 alert 规则失效。已占用的标签名清单见
  [可观测性](../docs/operations/observability.md#prometheus-抓取与规则)。
- Dashboard 变量与面板筛选标签必须一一对应：`cluster`→`cluster`、`node_id`→`node_id`、
  `component`→`component`、`agent_id`→`agent_id`。该契约由
  `deploy/grafana/dashboard_schema_test.go` 守护，新增变量时同步更新测试里的 `variableLabels`。

## 身份密钥注入（SSO / MFA 必需）

启用管理台单点登录或两步验证时，Server 进程必须能读到 `TUNNELMESH_TOKEN_ENCRYPTION_KEY`。它同时
服务三类可恢复秘密：service token reveal、浏览器 SSH/SFTP 凭据自动认证，以及 OIDC `client_secret`、
TOTP 共享密钥和 `auth_challenges` payload（state、nonce、PKCE verifier、login ticket）。缺失时
Server 仍能启动、密码登录照常，但 MFA 绑定与 OIDC 提供商创建返回 `503 secret_storage_unavailable`，
不会退化成明文存储。

`deploy/` 下的产物**刻意不携带任何密钥值**，但已经预留了注入通道；密钥值必须由部署方从
Secret Manager 注入。两种形态的做法：

| 形态 | 预留通道 | 部署方需要做什么 |
| --- | --- | --- |
| systemd（`deploy/systemd/tunnelmesh-server.service`） | unit 已带 `EnvironmentFile=-/etc/tunnelmesh/server.env`（前缀 `-` 表示文件缺失不阻塞启动） | 创建环境文件即可，不要用 drop-in 改 unit，也不要把密钥写进 unit 或 YAML：<br>`sudo install -o root -g tunnelmesh -m 0640 /dev/null /etc/tunnelmesh/server.env`，写入 `TUNNELMESH_TOKEN_ENCRYPTION_KEY=...`（集群再加 `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID`、`TUNNELMESH_TRACE_SIGNING_KEY`），然后 `sudo systemctl restart tunnelmesh-server` |
| Compose（`docker-compose.cluster.yml`、`docker-compose.local.yml`） | 两份编排都已把 `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 与 `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID`（集群还包含 `TUNNELMESH_TRACE_SIGNING_KEY`）以 `${VAR:-}` 形式透传给 Server | 在 `.env`（已 gitignore）或编排层导出这些变量。这里刻意用 `${VAR:-}` 而不是 `${VAR:?}`：未启用 SSO/MFA 的部署不应因为缺少身份密钥而无法启动，缺失时由 Server 在写入路径 fail-closed 返回 `503`。要求「缺失即启动失败」的部署，可在自己的 override 文件中改成 `:?` |

集群中所有 Server 节点必须配置**完全一致**的密钥与 `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID`（未设置时
key id 固定为 `default`），否则一个节点签发的 challenge 或 login ticket 在另一个节点上无法解密，
表现为跨节点的 SSO 登录间歇性失败。密钥文件权限与 `client.env` 同级（`0640`，属主 root、属组
tunnelmesh），不进发布归档、不进镜像层、不进日志。

`TUNNELMESH_TRACE_SIGNING_KEY` 同样必须在所有 Server/relay 节点一致，但它只用于跨节点 traceroute hop
签名，与身份认证无关，两者不要混为一谈。

模板与脚本中不得出现密钥值这条既有约定对身份密钥同样适用；密钥只能通过环境注入。取值范围、
轮换注意事项和 fail-closed 行为见
[配置说明](../docs/operations/configuration.md#管理台身份认证sso--mfa--受信任设备)，启用流程见
[单点登录与两步验证](../docs/user-guide/sso-and-mfa.md)。

## 证书目录

`deploy/certs/` 是运行时生成物，已 gitignore，仓库里不存在：

| 路径 | 来源 | 用途 |
| --- | --- | --- |
| `certs/mysql-ca.pem` | 运维自备 | `docker-compose.cluster.yml` 挂载为 MySQL TLS CA |
| `certs/relay/relay-ca.pem` | `scripts/gen-relay-certs.sh` | relay mTLS 的 CA 证书，所有 Server 节点共用 |
| `certs/relay/<node-id>/relay.pem`、`relay-key.pem` | `scripts/gen-relay-certs.sh` | 单个节点的证书与私钥，私钥 0600 |
| `certs/private/relay-ca-key.pem` | `scripts/gen-relay-certs.sh` | CA 私钥，0600，只留在签发机 |

CA 私钥刻意放在 `relay/` 之外：Compose 只挂载 CA 证书和该节点自己的目录，任一容器都读不到 CA
私钥或其它节点的私钥。签发流程、SAN 要求和轮换步骤见
[Relay mTLS 证书生成与配置](../docs/operations/relay-mtls.md)；Compose 引导顺序见
[Docker 部署](../docs/deployment/docker.md)。

`scripts/gen-relay-certs.sh` 是签发工具而不是部署产物，因此不进发布归档。

## 发布包内容

`scripts/build-release.sh` 决定了归档布局，属于对外契约（见
[跨平台可执行文件打包](../docs/deployment/binary-release.md)）：

| 平台 | 归档内的 `deploy/` 内容 |
| --- | --- |
| Linux | `deploy/install`、`deploy/systemd` |
| macOS | `deploy/install`、`deploy/macos` |
| Windows | `deploy/install`、`deploy/windows` |

归档同时包含 `README.md`、完整 `docs/`、`LICENSE` 和 `NOTICE`（Apache-2.0 的 4(a)/4(d) 要求随
分发附带许可文本与 NOTICE，缺失时 `build-release.sh` 直接失败）。`prometheus/`、`grafana/`、
`openresty/` 与 `certs/` 不进归档：`openresty/` 与 `prometheus/`、`grafana/` 同属运维自行挂载或
自行构建的产物，`certs/` 是本地生成物且包含私钥。调整目录名或文件名前必须
先更新 `scripts/build-release.sh` 和上述发布文档。

## 校验

```bash
go test ./deploy/... -count=1                  # Dashboard schema、模板与脚本一致性
go test ./deploy/openresty -count=1            # OpenResty 产物与 server.proxy_entry 默认值一致
plutil -lint deploy/macos/tunnelmesh.plist     # macOS
bash -n deploy/install/linux-install.sh deploy/install/macos-install.sh
bash -n scripts/gen-relay-certs.sh
bash -n deploy/openresty/spike-connect-check.sh
promtool check config deploy/prometheus/prometheus.yml.example
promtool check rules deploy/prometheus/recording-rules.yaml deploy/prometheus/alert-rules.yaml
systemd-analyze verify deploy/systemd/tunnelmesh-server.service   # 需要 Linux
TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs   # 需要 docker，缺前置条件时打印原因并 skip
```

`windows-install.ps1` 与 `windows-uninstall.ps1` 无法在 macOS/Linux 上静态校验；改动后必须在
Windows 上实际执行一次安装与卸载，并确认渲染出的 `*-service.xml` 中 `arguments`、
`workingdirectory`、`logpath` 与传入参数一致。
