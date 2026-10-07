# 测试与验证

各层验证各自覆盖不同范围，不能互相替代。必须执行的命令清单以
[AGENTS.md](../../AGENTS.md)「必须执行的验证」为准，本文件补充执行细节和已知成本。

| 层级 | 命令 | 覆盖范围 |
| --- | --- | --- |
| Go 单元与集成 | `go test ./... -count=1`、`go test -race ./...`、`go vet ./...` | 协议状态机、流控、Repository 契约（SQLite 与 MySQL 双方言）、迁移、API 授权与分页、跨层集成 |
| 前端单元 | `cd web && npm test -- --run`、`npm run build` | SSH/SFTP/ZMODEM 客户端逻辑、WebSocket 字节流背压、store、路由、视图交互 |
| 托盘前端单元 | `cd web-tray && npm test -- --run`、`npm run build` | 设置界面四个 tab、i18n 跟随与切换、主题浅/深/跟随、路由表单与检测渲染、token 掩码、本地 API 客户端、Agent 选择器（配置在挂载后才到位也要自动拉取一次、每窗口一次、未拉取/失败/确实为空三种空态不得混写）、按 `platform` 取词（macOS / Windows / 未知平台回落中性文案）、任务栏快捷小窗（两跳串行
  settings→stats 后渲染运行摘要、锁被占用与错误态、失焦隐藏的路由判定） |
| 浏览器端到端 | `node test/e2e/webssh/run.mjs` | 真实 Chrome + 真实 Server/Agent/SSH 主机，验证凭据自动认证、pty 终端、ZMODEM 双向传输、SFTP 复用与上传逐字节完整性、刷新恢复、浏览器控制台洁净 |
| OpenResty 端到端 | `TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs` | 真实 OpenResty 容器 + 内部入口替身，验证 CONNECT 搬运、请求头白名单、非 200 响应原样透传、绝对形式改写、客户端断开后隧道回收、日志不含凭据 |

端到端测试不在 `go test` 与 `npm test` 中，需要 Chrome 与 `lrzsz`，详见
[test/e2e/webssh/README.md](../../test/e2e/webssh/README.md)。修改中继流控、WebSSH broker、
Agent 流分发或终端/SFTP 前端后，发布前必须跑一次。

tp-* 代理入口的冒烟同样不在 `go test` 与 `npm test` 中，需要 docker 与 openssl；未设置
`TM_PROXY_E2E_NGINX=1` 或前置条件缺失时打印原因并以 0 退出。修改 `deploy/openresty/` 下任一产物后
发布前必须跑一次，详见 [test/e2e/proxy-entry/README.md](../../test/e2e/proxy-entry/README.md)。

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

## macOS 托盘的构建隔离与嵌入产物

托盘的设置界面同样走 Go embed，但产物在 `internal/tray/webdist/dist/`，由
`cd web-tray && npm run build` 通过 `web-tray/scripts/sync-tray-dist.mjs` 镜像同步，与管理后台
的 `internal/server/web_dist/` 互不影响。

与管理后台不同的地方在于**嵌入被构建标签分成两半**：`embed.go`（`//go:build tray`）持有真正的
`//go:embed all:dist`，`embed_stub.go`（`//go:build !tray`）提供同名 API 并返回“无产物”。
原因是 `//go:embed` 的模式匹配不到任何文件时是编译错误，而托盘产物在干净检出里并不存在；
若不拆开，每次 `go build ./...` 都会要求先装 Node 工具链，包括永远跑不了托盘的 Linux 检出。
拆开后：

```bash
go build ./... && go test ./... -count=1        # 无需前端产物，走 stub 半
cd web-tray && npm test -- --run && npm run build
# 需要 macOS 工具链与托盘前端产物；./... 会连带要求管理后台产物，故点名包
CGO_ENABLED=1 go vet -tags tray ./cmd/tunnelmesh-client-tray ./internal/tray/...
```

无标签时 `webdist.FS()` 必须返回 `nil` 而不是一个空 `fs.FS`：`internal/tray/api.go` 正是用这个
nil 选择“去跑 npm run build”的占位页面，返回非 nil 的空 FS 会渲染出一个看起来像渲染 bug 的
空白窗口。`internal/tray/webdist/embed_test.go` 在两种模式下都断言这条契约。

原生代码的隔离由 `scripts/tray_build_tag_test.go` 守卫（已包含在 `go test ./...` 中）：含 cgo
的文件、import `internal/tray/native` 的文件、`//go:embed` 托盘产物的文件都必须带 `tray` 标签；
含 `.m`/`.c` 等原生源码的目录里**所有** Go 文件都必须带标签（cgo 要么整包编译这些源文件，
要么在它们存在却没人 `import "C"` 时直接拒绝该包）；`internal/tray/` 本体禁止 cgo，好让托盘
逻辑能在 `CGO_ENABLED=0` 的 Linux CI 上跑测试。构建约束用 `go/build/constraint` 求值而不是
字符串匹配，因此 `tray || linux` 这类“看着带标签、其实仍会编译”的写法同样会被抓到。

托盘无法在 `go test` 里执行原生行为（需要真实 Cocoa run loop），因此分三层兜：
`deploy/macos/tray_shim_test.go` 把 Objective-C shim 当文本守——必须安装应用主菜单（否则
WKWebView 收不到任何 ⌘ 编辑快捷键）、必须用 `NSEventMaskKeyDown` 注册 ⌃ 变体的 local monitor
（传事件类型值时 handler 一次都不触发）、必须同时 `registerDefaults:` 与写本 bundle 持久域
（自动大写只认后者）；`deploy/macos/tray_bundle_test.go` 守 app bundle 与磁盘映像资产，含
`.icns` 里 16/128/512/1024 四档表示与卷图标必须落在布局之后。剩下的一半靠实测：这些约束都
先在 macOS 主机上用合成 `NSEvent` 打给真实 WKWebView 跑出红/绿，再写成守卫，打包后的手工冒烟
清单见 [macOS 托盘客户端打包](../deployment/macos-client-tray.md#验证)。

图标不是手工资产：`scripts/generate-tray-icon.sh` 从 `scripts/trayicon` 的几何重新生成
`deploy/macos/TunnelMeshClient.icns` 与 `deploy/windows/*.ico`，改图标即改代码，产物可复现。

## Windows 托盘的构建隔离

Windows 的原生壳是**纯 Go**，因此它的隔离方式与 macOS 不同，但目的相同：默认构建绝不能需要
一个 Windows 主机。`scripts/tray_build_tag_test.go` 为此额外断言三件事：

- `internal/tray/native/*_windows.go` 必须带 `tray` 构建标签，且**不得** `import "C"`；
- 两端 `native` 的导出符号集必须逐个相等（`shellAPISurface`），否则共享的 `main.go` 就得按平台
  分叉，而分叉的入口是标签隔离最先失守的地方；
- 树里不得出现 `.syso`（`go-winres` 的产物一旦被提交，就会被之后的每次 `go build` 静默链接）。

因此这四条必须同时绿，缺一条就等于把 Windows 托盘从 CI 里摘掉：

```bash
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=windows GOARCH="$arch" go build -tags tray ./cmd/tunnelmesh-client-tray ./internal/tray/...
done
GOOS=windows go vet -tags tray ./cmd/tunnelmesh-client-tray ./internal/tray/...
GOOS=windows go vet -tags tray ./internal/client    # 顺带编译 windows 侧的锁测试
go test ./scripts ./deploy/windows ./internal/tray ./internal/client -count=1
```

包清单只列带 `tray` 标签的目录加 client 锁：用 `./...` 会把 `internal/server` 拖进来，它的
`//go:embed all:web_dist` 在未构建管理后台的树里直接编译失败，`.github/workflows/ci.yml` 的
`windows-tray` 作业因此构建 `web-tray` 而不构建 `web`。

`deploy/windows/tray_bundle_test.go` 把 Windows 侧的“资产即契约”守成可执行断言：解析两个
`.ico` 的目录（尺寸齐全、全 BMP 条目、32 bpp）、`deploy` 与 `embed` 两份
`TunnelMeshTray.ico` 逐字节一致、`winres.json` 的 类型→名称→语言 ID→资源 三层结构与兄弟文件
引用、`installer.nsi` 的每用户属性（`RequestExecutionLevel user`、`$LOCALAPPDATA`、卸载必须删
`HKCU` 启动值）、打包脚本的关键参数与失败分支、`PANEL_ROUTE` 与前端 `panelRoute.ts` 对齐、
以及 `scripts/build-release.sh` 里不得出现 `-tags tray`。

`internal/client` 的 Windows 文件锁测试（`runtime_lock_windows_test.go`）带 `//go:build windows`，
只能在 Windows 上执行；Linux/macOS 侧由 `runtime_lock_test.go` 覆盖同一组语义（第二个持有者取锁
失败、释放后可重取）。这是本次交付中唯一无法在本机自动执行的测试，其余 Windows 逻辑
（`platformName`、`fraction` 钳位、锁文件路径派生、清单字段）都是平台无关的普通单测。

Windows GUI 行为同样无法在 `go test` 里执行，兜底方式与 macOS 一致：交叉编译 + 结构断言 +
真机冒烟清单，见 [Windows 托盘客户端打包](../deployment/windows-client-tray.md#真机冒烟清单)。

## 托盘前端等条件，不等微任务数

`web-tray` 的组件测试原先靠 `flush(6)` 这类**手调的微任务圈数**等接口回来再断言 DOM。圈数不够
就渲染出“取数之前”的状态：快捷小窗要等**串行两跳**（先 `settings` 应用语言/主题，再 `stats`），
一跳花掉几拍取决于运行时 `fetch` 的实现，本机 Node 24 够用、GitHub runner 的 Node 22 不够，
于是 macOS 与 Linux 两个作业同时红在 4 个用例上（状态停在 `已停止`、服务行进停在 `未配置`），
本地全绿。

规则因此是：**断言渲染结果的等待必须看条件**，即 `src/tests/helpers.ts` 的
`flushUntil(() => ...)`（微任务自旋到谓词成立，默认放宽到 4000 轮——一次_yield_ 是纳秒级，
而被等的链长不由本仓库决定；只用微任务是因为有 3 个作业跑在 fake timers 下，宏任务等待永远不会
返回）。`flush(n)` 仍用于“点一下按钮再看调用记录”的场合，它的实现已经改成每单元排空一轮，
不再假设一跳等于一拍。

`src/tests/panel-timing.spec.ts` 把这条不变量钉住：它给每个响应人为加上 500 拍，如果谁再把
小窗的等待退回固定圈数，这个用例就先红。同一次反馈还补了 CI 缺口——`ci.yml` 的 `windows-tray`
作业此前只构建不测试，托盘前端只在 Release 作业里第一次被跑到，故现在该作业必须跑
`cd web-tray && npm test -- --run`。

## MySQL 与 etcd 相关验证

- CI 的 `mysql56` job 起一个 `mysql:5.6` 服务容器（项目声明的最低支持版本，生产实例为
  `5.6.51-91.0-log`），用它执行与 SQLite **同一个** contract 函数：
  `runRepositoryContract`（含 `runClientRepositoryContract`）、`runIdentityRepositoryContract`
  与 registry 的 `runRegistryContract`。因此 MySQL 方言专属的 SQL 错误（`FOR UPDATE`、
  `EXISTS` 相关子查询、带守卫的 `DELETE`、`connection_epoch` 位宽）不再只能靠生产报错发现。
  `mysql56` 是 main 的必需状态检查。
- 本地复跑用 `docker-compose.cluster.yml` 里 opt-in 的 `mysql56` profile 起同一个下限版本。它是
  一次性测试库：无 TLS、只绑 `127.0.0.1:3307`、独立 volume，且不会随集群栈启动。

```bash
docker compose -f docker-compose.cluster.yml --profile mysql56 up -d mysql56
TUNNELMESH_TEST_MYSQL_DSN='tunnelmesh:tunnelmesh@tcp(127.0.0.1:3307)/tunnelmesh_test?parseTime=true&tls=false' \
  go test -p 1 ./internal/storage ./internal/registry -count=1 -run MySQL
```

  - `OpenMySQL` 默认 `auto-init=true`，所以需要**空库**和建表权限；要重来一遍就
    `docker compose -f docker-compose.cluster.yml --profile mysql56 down -v mysql56`。
  - **不需要** `multiStatements`：整段 DDL 与增量脚本一律由 `applySchemaStatements` 拆成单语句逐条
    执行，测试与 auto-init 走同一条路径。该参数在 `4fc8213`/`e8b7c0d` 之前确实是必需的（当时把整段
    DDL 交给一次 `ExecContext`）；`TestMySQLGatedTestsNeverExecWholeDDLScripts` 负责拦住它的回归，
    所以不要为了「保险」把它加回 DSN——那只会让下一份文档继续宣称一个不存在的前提。
  - `-p 1` 是正确性要求，不是调优：两个包对同一个库各自 auto-init，并行会抢
    `schema_meta(id=1)` 这一行并报 `Error 1062`。
  - 口令用 `MYSQL56_TEST_PASSWORD` / `MYSQL56_ROOT_PASSWORD` 覆盖默认占位值即可；这两者只服务
    本地测试库，不是生产凭据。
- CI 与本地 profile 的分工：CI 服务容器只能用镜像默认 charset（5.6 是 latin1），能证明 SQL
  语法/语义，证明不了 utf8mb4 下的 InnoDB 767-byte 前缀；`mysql56` profile 挂载
  `deploy/mysql56/utf8mb4.cnf` 复现生产的 `utf8mb4_general_ci`（不放宽 `innodb_large_prefix`），
  所以「DDL 在生产 charset 下建得起来」由本地 profile 覆盖。CI 镜像与 profile 镜像的一致性由
  `deploy/mysql56/mysql56_service_test.go` 守护。
- `docker-compose.cluster.yml` 的 `mysql:8.4` 是集群开发环境默认，**不能**替代 5.6 下限验证：
  8.x 会放行 5.6 拒绝的语法。8.4 版本矩阵仍属 deferred，见
  [项目完整性清单](../operations/completeness-checklist.md)。
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
| `deploy/macos/tray_bundle_test.go` | 托盘 app bundle 的 Info.plist 三个静默故障键（`LSUIElement`、`NSAllowsLocalNetworking`、`NSRequiresAquaSystemAppearance=false`）与 bundle id / 可执行名 / 最低系统版本；模板与 `scripts/package-macos-tray.sh` 不得漂移；打包脚本必须产出 `.dmg` 且不得再出现 zip 参数；映像必须两段式产出（`-format UDRW` → 挂载 → Finder 布局 → detach → `hdiutil convert -format UDZO`），布局失败时响亮降级并把 `installerLayout` 写进清单；`deploy/macos/tray-dmg-layout.applescript` 必须真的驱动 Finder（`icon view`、`not arranged`、`set position of item`、`close installerWindow`）且不得写死挂载点或卷名；`scripts/build-release.sh` 不得**编译**托盘（不得出现托盘可执行名、`-tags tray`、`CGO_ENABLED=1` 或 `hdiutil`）；打包脚本必须把最低系统版本同时钉进产物（`MACOSX_DEPLOYMENT_TARGET` 与 `-mmacosx-version-min`）并用 `otool` 读回 `LC_BUILD_VERSION` 校验，版本字面量只许出现在 plist 里 |
| `deploy/openresty/openresty_artifacts_test.go` | Lua/conf/Dockerfile 与 `server.proxy_entry.*` 默认值一致、可信头齐全、tp-* server 块不开 http2、版本 pin 与 `configure → patch → make` 构建顺序、Lua 不含策略逻辑 |
| `deploy/install/oneclick/oneclick_scripts_test.go` | 一键安装脚本的严格模式、无硬编码秘密、与 `scripts/install.sh` 共享同一份 Release 契约，以及 `testdata/run_tests.sh` 的函数级套件 |
| `deploy/install/oneclick/oneclick_config_test.go` | 脚本渲染出的 YAML 能被真实的 `config.Load` 解析，且不含秘密 |
| `deploy/install/oneclick/oneclick_e2e_test.go` | `--archive --yes` 走完安装/升级/卸载（`TM_ONECLICK_E2E=1` 门控） |

### 测试驱动的 shell 脚本必须是非交互的

`deploy/install/oneclick/tunnelmesh-install-common.sh` 的 `tm_ask*` 是给真人用的：`tm_tty_init`
探测到控制终端就把 `TM_TTY` 设成 `/dev/tty`，否则回落到 stdin。测试里没有人会回答，因此凡是
`source` 这个共享库并可能走到 `tm_ask*` 的测试驱动脚本，都必须自己切断交互通道，做法见
`deploy/install/oneclick/testdata/run_tests.sh` 开头：

- `exec </dev/null`：任何意外走到 `read` 的调用立刻拿到 EOF 并回落默认值，而不是阻塞；
- 需要具体输入的断言用 `printf '...\n' | tm_ask_*_stdio ...` 在子 shell 里覆盖 stdin；
- 调用过 `tm_tty_init` 之后立刻 `TM_TTY=""`，并在套件收尾用 `tty/hermetic-at-exit` 复核没有被
  重新武装；
- 走安装/渲染路径时预置答案（`tm_ans_set`）或传 `--yes`。

Go 侧有第二道防线：`runBash` 用 `exec.CommandContext` 给每个脚本一个硬上限（函数级套件 60s，
安装/渲染 3m），并设置 `cmd.WaitDelay` 让被杀脚本的子 shell 不再拖住 `Wait`。真出现交互提示时
失败信息会点名是哪个脚本卡住，而不是让 `go test` 挂到包级 10m 超时只留一份 goroutine dump。
`TestShellFunctionSuiteNeverReadsTheCallersStdin` 用一条永不写入也永不关闭的管道复现「stdin
打开着但没有数据」的环境，因此无论本机有没有控制终端都能稳定判定这个不变量。

以下校验依赖平台工具，CI 与本机不一定具备，改动对应产物后需要在目标平台补跑：

```bash
# 模板含 __ENVIRONMENT__ 占位符，未渲染时不是合法 XML；lint 的是「渲染为空」的结果。
sed 's/^__ENVIRONMENT__$//' deploy/macos/tunnelmesh.plist | plutil -lint -    # macOS
bash -n deploy/install/linux-install.sh deploy/install/macos-install.sh
bash -n deploy/install/oneclick/*.sh
promtool check config deploy/prometheus/prometheus.yml.example
promtool check rules deploy/prometheus/recording-rules.yaml deploy/prometheus/alert-rules.yaml
systemd-analyze verify deploy/systemd/tunnelmesh-server.service              # Linux
```

`windows-install.ps1` 与 `windows-uninstall.ps1` 无法在 macOS/Linux 上静态校验；改动后必须在
Windows 上实际执行一次安装与卸载，并确认渲染出的 `*-service.xml` 中 `arguments`、
`workingdirectory`、`logpath` 与传入参数一致。产物清单与发布归档布局见
[deploy/README.md](../../deploy/README.md)。

## 发行链路校验

发行链路跨三种构建：`scripts/build-release.sh` 在 Linux 上用 `CGO_ENABLED=0` 交叉编译三个
二进制，`scripts/package-macos-tray.sh` 在 macOS 上打包托盘 `.dmg`，
`scripts/package-windows-tray.sh` 在任意装了 `makensis` 的主机上打包 Windows 托盘的绿色 `.zip`
与 `setup.exe`，`scripts/merge-tray-dist.sh` 把后两者并进前者的输出目录。这条链路上的错误全是
静默的——少某个平台的资产、`SHA256SUMS` 不再覆盖全部资产、几个作业各自解析出不同版本号——因此
有普通 Go 测试守卫，包含在 `go test ./... -count=1` 里：

| 测试 | 守护的不变量 |
| --- | --- |
| `scripts/release_workflow_test.go` | 解析 `.github/workflows/release.yml`：版本号只在 `version` 作业解析一次并被三个构建作业共用；托盘只由 `macos-tray`（macOS runner）与 `windows-tray`（Linux runner + `nsis`）产出，各自校验产物、汇总 `installerLayout`、上传 artifact；Linux 的 `release` 作业必须下载**两个** artifact，以 `TRAY_DIST_DIR` 与 `WINDOWS_TRAY_DIST_DIR` 交给 `build-release.sh`，并断言资产清点（2 个 `.dmg`、1 个 `.exe`、8 个 `tar.gz|zip`）与三份 manifest，且不得自己打包托盘 |
| `scripts/ci_workflow_test.go` | 解析 `.github/workflows/ci.yml`：必须有 `-tags tray` 的 `go build`/`go vet`（amd64 + arm64）、托盘前端 `npm ci` + `npm test -- --run` + `npm run build`、`gofmt` 检查、托盘守卫测试与 `makensis` 冒烟编译；包清单必须限定在带 `tray` 标签的目录（断言里同时禁止 `./...`，否则该作业会去要求一份它根本不用的管理后台产物），且该作业不得发布 |
| `scripts/merge_tray_dist_test.go` | **执行** `scripts/merge-tray-dist.sh`：合并后一条 `sha256sum -c` 覆盖全部资产；清单按 `--manifest-name` 发布为 `manifest-tray.json` / `manifest-windows-tray.json` 而不覆盖 `manifest.json`，也不互相覆盖；形状无关（`.dmg` 与 `.zip`+`.exe` 同样通过）；来源缺 `SHA256SUMS`/`manifest.json` 或除这两个文件外什么都没有、资产与校验和不符、目标已有同名资产时失败且不留半成品；`.build/`、`*.log` 之类不会被误发布；`--check` 只校验不写 |
| `scripts/tray_build_tag_test.go` | 任何含 cgo、import `internal/tray/native`、`//go:embed` 托盘产物的文件都必须带 `tray` 标签，默认构建不触达 WebKit；Windows 侧额外要求纯 Go、两端符号集相等、不提交 `.syso` |

`merge_tray_dist_test.go` 用 fixture 真跑脚本，而不是像其他 shell 守卫那样做字符串匹配：“合并后
校验和文件仍然可用”这件事证明不了就只能靠人。它只依赖 `bash` 与 `sha256sum`/`shasum` 之一，
因此 Linux CI 与 macOS 本机都能跑。

两个打包脚本本身需要真实的主机能力（macOS 侧的 `codesign`/`plutil`/`ditto`/`hdiutil` 与 cgo
工具链、Windows 侧的 `makensis`），不进 `go test`；产物行为按
[.github/workflows/release-test.md](../../.github/workflows/release-test.md) 与
[Windows 托盘客户端打包](../deployment/windows-client-tray.md#真机冒烟清单) 在目标平台手工走一遍。

## 文档索引校验

文档结构或记录变更后，重新生成索引并校验链接：

```bash
python3 scripts/gen_doc_index.py
git diff --check
```
