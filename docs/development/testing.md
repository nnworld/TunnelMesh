# 测试与验证

三层验证各自覆盖不同范围，不能互相替代。必须执行的命令清单以
[AGENTS.md](../../AGENTS.md)「必须执行的验证」为准，本文件补充执行细节和已知成本。

| 层级 | 命令 | 覆盖范围 |
| --- | --- | --- |
| Go 单元与集成 | `go test ./... -count=1`、`go test -race ./...`、`go vet ./...` | 协议状态机、流控、Repository 契约（SQLite 与 MySQL 双方言）、迁移、API 授权与分页、跨层集成 |
| 前端单元 | `cd web && npm test -- --run`、`npm run build` | SSH/SFTP/ZMODEM 客户端逻辑、WebSocket 字节流背压、store、路由、视图交互 |
| 浏览器端到端 | `node test/e2e/webssh/run.mjs` | 真实 Chrome + 真实 Server/Agent/SSH 主机，验证凭据自动认证、pty 终端、ZMODEM 双向传输、SFTP 复用与上传逐字节完整性、刷新恢复、浏览器控制台洁净 |

端到端测试不在 `go test` 与 `npm test` 中，需要 Chrome 与 `lrzsz`，详见
[test/e2e/webssh/README.md](../../test/e2e/webssh/README.md)。修改中继流控、WebSSH broker、
Agent 流分发或终端/SFTP 前端后，发布前必须跑一次。

## race 测试的超时要求

`go test -race ./...` 需要显式放宽超时：`internal/server` 单包在 `-race` 下约需 8-10 分钟，
已接近 Go 默认的 10 分钟单包超时，整仓并行时会被 CPU 争用推过线并报
`panic: test timed out after 10m0s`。使用：

```bash
go test -race ./... -timeout 30m
```

超时的成因是既有测试基建成本，不是被测逻辑变慢：共享 fixture `apiTestServer` 每个用例都用
**生产级 Argon2id 参数**创建 2-3 个账号，单次哈希约 1 秒，`-race` 下更慢，于是上百个 API 测试
各自固定消耗约 7 秒。改进方向是给测试注入一套低成本的 Argon2 参数（只改 fixture，不改生产
默认值），可把该包 `-race` 时间压到分钟级；此项属于独立的测试基建改造，需要单独的实施计划。

## 嵌入产物校验

Server 通过 Go embed 提供管理后台静态资源，`internal/server/web_dist/` 是构建产物且不入库。
前端改动后执行：

```bash
cd web && npm run build && cd ..
./scripts/verify-web-embed.sh
```

`npm run build` 已内置 `web/dist` → `internal/server/web_dist` 的镜像同步；`verify-web-embed.sh`
只做一致性校验，不一致时说明同步步骤被绕过。缺失该目录会让 `go build ./cmd/...` 在
`//go:embed all:web_dist` 处直接失败。

## MySQL 与 etcd 相关验证

- Repository contract test 默认跑 SQLite 方言；设置 `TUNNELMESH_TEST_MYSQL_DSN` 后才会对真实
  MySQL 执行同一套契约测试。
- etcd 注册发现当前只保留实现与文档接口，真实集成测试属于 deferred 项，见
  [项目完整性清单](../operations/completeness-checklist.md)。

## 部署产物校验

`deploy/` 下的一致性测试是普通 Go 测试，已包含在 `go test ./... -count=1` 中，也可单独执行：

```bash
go test ./deploy/... -count=1
```

| 测试 | 守护的不变量 |
| --- | --- |
| `deploy/grafana/dashboard_schema_test.go` | 唯一 Dashboard 的 JSON 结构、Row 划分与 datasource 变量 |
| `deploy/install/install_templates_test.go` | 每个角色只有一份模板来源，安装脚本引用共享模板且替换全部占位符 |

以下校验依赖平台工具，CI 与本机不一定具备，改动对应产物后需要在目标平台补跑：

```bash
plutil -lint deploy/macos/tunnelmesh.plist                                  # macOS
bash -n deploy/install/linux-install.sh deploy/install/macos-install.sh
promtool check config deploy/prometheus/prometheus.yml.example
promtool check rules deploy/prometheus/recording-rules.yaml deploy/prometheus/alert-rules.yaml
systemd-analyze verify deploy/systemd/tunnelmesh-server.service              # Linux
```

`windows-install.ps1` 与 `windows-uninstall.ps1` 无法在 macOS/Linux 上静态校验；改动后必须在
Windows 上实际执行一次安装与卸载，并确认渲染出的 `*-service.xml` 中 `arguments`、
`workingdirectory`、`logpath` 与传入参数一致。产物清单与发布归档布局见
[deploy/README.md](../../deploy/README.md)。

## 文档索引校验

文档结构或记录变更后，重新生成索引并校验链接：

```bash
python3 scripts/gen_doc_index.py
git diff --check
```
