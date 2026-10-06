# macOS 系统托盘 Client

关联实施计划：[2026-10-05-client-system-tray-macos](../superpowers/plans/2026-10-05-client-system-tray-macos.md)

## 标题

```
feat(client): add a macOS menu-bar tray client
```

## 目标分支

`main`

## 摘要

新增第四个可执行程序 `tunnelmesh-client-tray`（macOS 菜单栏客户端）。它在自己的进程里承载
隧道，能力等价于 `tunnelmesh-client run`，与命令行 Client **共用**同一份
`~/.config/tunnelmesh/client.yaml`，并通过按配置路径派生的 advisory 锁与之**互斥**。
原有 `tunnelmesh-client` 的命令面、参数与外部行为不变。

实现分四层，边界是刻意划的：

| 层 | 位置 | 约束 |
| --- | --- | --- |
| 共享运行时 | `internal/client/runtime.go`、`runtime_lock*.go` | 从 `internal/cli/client_run.go` 抽出装配逻辑，CLI 改为委托；无 cgo |
| 托盘逻辑 | `internal/tray/`（paths、prefs、configstore、server、validate、app、api） | 无 cgo、无 Cocoa，`CGO_ENABLED=0` 下可在 Linux 测试 |
| 原生外壳 | `internal/tray/native/`（`native.go` + `tray_darwin.m`） | `//go:build tray && darwin`，唯一触碰 Cocoa/WebKit 的地方 |
| 设置界面 | `web-tray/`（Vue 3 + TS + Element Plus + Pinia + vue-i18n） | 独立于管理后台 `web/`，产物嵌入 `internal/tray/webdist/dist` |

服务端只有一处 additive 变更：`GET /api/v1/client/agents`，用 client service token 鉴权，
返回该 token 作用域内可用 Agent 的 `{id, name, online}`，作为路由配置页 Agent 下拉的数据源。

打包走独立的 `scripts/package-macos-tray.sh`，**不进**跨平台 release 矩阵：托盘必须通过
cgo 链接 Cocoa/WebKit，只能在 macOS 主机构建，而矩阵用 `CGO_ENABLED=0` 从 Linux 交叉编译。

## 用户影响

- macOS 用户获得菜单栏客户端：三项菜单（打开主界面 / 打开官网首页 / 退出）、约半屏的四
  tab 设置窗口（通用 / 统计 / 路由配置 / 关于）、图形化编辑 `client.yaml`、Agent 下拉选择、
  逐项配置检测、运行统计、开机启动与关闭最小化。
- 命令行用户获得一条新语义：同一份配置已有 Client 在跑时，`run` 以退出码 1 拒绝启动并打印
  `client: another TunnelMesh client is already running with this configuration`。这是计划
  中“互斥”要求的直接结果，也是本次唯一一处对既有命令行为的改变。
- 非 macOS 用户不受影响：默认构建不编译任何原生代码，`tunnelmesh-client-tray` 在非
  `-tags tray` 构建里给出“仅 macOS”的说明而不是编译失败。
- 使用者文档：[macOS 系统托盘 Client](../user-guide/client-tray.md)；
  部署文档：[macOS 托盘客户端打包](../deployment/macos-client-tray.md)。

## API、Schema 与配置影响

**API**：新增 `GET /api/v1/client/agents`（additive，无破坏性变更）。

- 单数 `/api/v1/client/` 命名空间与既有复数 `/api/v1/clients/` 刻意分开：前者用
  service token（`service_tokens`，type=client）鉴权，后者用控制台 bearer（`api_tokens`）。
  合并会让一个隧道凭据获得管理视图。
- 鉴权在 `ServeHTTP` 的 pre-auth 段完成（仿 `handlePublicAuth`），因为 `API.authenticate`
  只解析 api_tokens。校验器由 runtime 注入既有 `credentialService` 实例，不新建，避免
  第二个 "last used" 后台 worker 泄漏。
- 过滤：owner（SQL）+ `Enabled` + 与 `scope.AgentIDs` 求交（空 scope = 不限）。enabled 与
  scope 过滤在**累积页时**进行而不是在截断后丢弃，否则会静默缩短结果并让 cursor 失步。
- 响应仅 `{id, name, online}`，`additionalProperties: false`，不含能力、metadata 或归属信息。
- 状态码：200 / 401（所有拒绝原因同一个不透明错误，避免成为 token oracle）/ 405 / 503
  （未注入校验器，属接线故障，不折进 401）。
- 同步更新 `docs/api/openapi.yaml`（新增 path、`clientTokenAuth` 安全方案、`ClientAgent`、
  `ClientAgentPage`、`ClientAgentPageEnvelope`）、`docs/README.md`、
  `docs/user-guide/client.md` 与 `docs/deployment/binary-release.md`。

**Schema**：无变更。`SchemaVersion` 未动，`migrations/` 未动，不需要数据库迁移。
`internal/storage/repository.go` 只新增了导出函数 `AgentCursor(Agent) string`，用于让
过滤型调用方能构造与 `agentRepo.List` 同构的复合 cursor；传裸 Agent ID 不会报错，
`decodeCursor` 会原样返回、复合切分找不到分隔符、`List` 静默从第一页重来并永远重复同一批行，
因此这个函数必须导出而不是让调用方自己拼。

**配置**：

- `client.yaml` 结构不变。`client.token` 的 `yaml:"-"` / `json:"-"` 标签**未改动**；viper 的
  mapstructure 路径仍会加载它，而 `print-config` 与 `RedactedJSON` 因为 `json:"-"` 依然不输出。
- 新增托盘专属偏好文件 `tray.json`（语言、主题、配置目录、开机启动、最小化）。刻意不写进
  `client.yaml`：那个文件要过 `config.Validate`，未定义的界面键只会被拒绝或被静默忽略。
- 新增 `client.lock`（`0600`），由 `client.LockPathForConfig` 从配置路径派生，托盘与 CLI 用
  同一个函数，因此不会各自算出不同的锁。
- `internal/cli/client_run.go` 从 243 行降到委托实现（净 -336/+188 行，含文档）。

## 安全与授权影响

- **回环 + 每次启动随机 secret**：设置界面由托盘进程在 `127.0.0.1` 随机端口提供，只绑回环。
  每次启动生成 32 字节 secret，随首屏 URL 注入 WebView，之后每个 `/api` 请求都要带
  （header / bearer / cookie / query 四种携带方式），比较使用常量时间。本机其他进程既猜不到
  端口也拿不到 secret，无法驱动托盘或读出 token。
- **Origin 校验 + 反嵌入**：`X-Frame-Options: DENY`、`X-Content-Type-Options: nosniff`、
  `default-src 'self'` CSP。持有 token 的页面不能被别的站点框住。
- **状态码边界**：401 只代表本地 secret 校验失败；锁竞争是 409，配置不可用是 422，其余 400。
  把服务端错误也映射成 401 会让界面误报“本机接口失效”。
- **token 不出 Go 进程**：`RoutingView` 只带 `TokenPresent`；`RoutingUpdate.Token` 是三态指针
  （`nil` 保留 / `""` 清除 / 有值替换），因为只有指针能区分“表单从未展示过 secret”和
  “操作者删掉了它”。任何视图接口都不返回 token 明文。
- **静态资源不要求 secret**：它们就是仓库里公开的 JavaScript，不含凭据；而首屏文档带
  `?secret=`、随后请求资源时不带，给静态资源加密只会让首屏加载失败。
- **WebView 只允许自己的回环源**：`decidePolicyForNavigationAction` 校验 scheme/host/port，
  外部链接交给 `NSWorkspace`，避免把一个持有 secret 的 WebView 变成通用浏览器。
- **文件权限**：`client.yaml`、`tray.json`、`tray.log`、`client.lock` 均 `0600`，目录 `0750`，
  写入走临时文件 + 原子替换。
- **登录项**：`SMAppService` 以 bundle cdhash 记录，移动或重签 `.app` 会使注册失效；托盘每次
  启动用偏好里的意图重新对齐一次，注册被拒时**不写偏好**，界面不会声称成功。
- **不实现**：P2P NAT traversal、任意远程命令执行；未新增任何远程执行面。
- 未引入新的敏感配置项，无硬编码凭据；`git diff --check` 与全量新增文件的尾随空白/冲突标记
  扫描均干净。

## 测试证据

自动化（本机 macOS Darwin arm64，Go 1.27.1，Node 24.15）：

| 命令 | 结果 |
| --- | --- |
| `go build ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go test ./... -count=1` | 退出码 0，28 个包 ok，0 个 FAIL |
| `go test -race ./...` | 退出码 0，28 个包 ok，无 DATA RACE |
| `CGO_ENABLED=1 go vet -tags tray ./...` | 通过 |
| `CGO_ENABLED=1 go test -tags tray ./internal/tray/... ./scripts/ ./deploy/...` | 全部 ok |
| `git diff --check` | 干净（另对 90 个新增文件扫描尾随空白、冲突标记、CRLF，均干净） |
| `cd web-tray && npm test -- --run` | 9 个文件 / 65 个用例全部通过 |
| `cd web-tray && npm run build` | 通过，产物与 `internal/tray/webdist/dist` `diff -qr` 一致 |
| `ARCHES="arm64 amd64" ./scripts/package-macos-tray.sh` | 两个架构均构建、签名、`codesign --verify` 通过（valid on disk / satisfies its Designated Requirement） |

本次新增 121 个 Go 测试函数，覆盖：服务端新接口的未授权、错误 token 类型（agent / api_token
均拒）、owner 越权、scope 过滤、分页与 cursor 可续、online 标记、方法/路径、缺校验器 503；
共享运行时的装配等价性与既有 client 测试全绿；互斥锁的同配置二次启动失败、释放后可重启、
崩溃后 flock 自动释放；`Stats` 字段；配置与 token 的加载、`RedactedJSON` 不泄露、`0600`、
`mode=cluster` 缺 MySQL 时被预校验拦截；本地 API 的设置/路由读写、Agent 代理、检测、统计、
secret 校验与回环绑定。

结构性守卫（均为“先证明会红”再落地）：

- `scripts/tray_build_tag_test.go`：含 cgo 的文件、import `internal/tray/native` 的文件、
  `//go:embed` 设置界面产物的文件都必须带 `tray` 标签；含原生源码的目录里所有 Go 文件都必须
  带标签；`internal/tray/` 本体禁止 cgo；`cmd/tunnelmesh-client-tray` 必须保留反向标签的
  fallback。**已用两个临时违规文件验证会失败**（`//go:build linux` + import native、
  无标签 `//go:embed`），随后删除恢复绿。约束判定用 `go/build/constraint` 求值而非字符串匹配，
  因此 `tray || linux` 这类“看着带标签其实仍会编译”的写法也会被抓到。
- `internal/server/client_scoped_openapi_test.go`：文档化的 client 作用域接口集合 == 实际派发的
  集合；GET 必须要求 `clientTokenAuth` 且不得接受 `bearerAuth`；响应状态集合固定；
  `ClientAgent` 的 `additionalProperties: false`、属性集合与 `clientAgentView` 实际输出逐字段
  相等、required 数量相等。**已用两处文档突变验证会失败**（多文档化一个 `capabilities` 字段、
  把 security 换成 `bearerAuth`），随后还原恢复绿。
- `deploy/macos/tray_bundle_test.go`：Info.plist 三个静默故障键（`LSUIElement`、
  `NSAllowsLocalNetworking`、`NSRequiresAquaSystemAppearance=false`）与 bundle id / 可执行名 /
  最低系统版本；模板与打包脚本不得漂移；`scripts/build-release.sh` 里不得出现托盘二进制或
  `-tags tray`。

打包后手工冒烟（ad-hoc 签名的 `TunnelMesh Client.app`，隔离 `HOME`）：

1. 进程启动，本地接口监听 `127.0.0.1` 随机端口；`GET /` 返回 200 且是**真实嵌入产物**
   （index.html 649 B，`/assets/index-*.js` 50.6 KB，`Content-Type` 正确），证明 `go:embed`、
   ATS 放行本地 http、WKWebView 加载链路都通。
2. `GET /api/stats` 不带 secret 与带错误 secret 均返回 401 `this request does not carry the
   tray secret`；响应头含 CSP、`X-Frame-Options: DENY`、`nosniff`、`Cache-Control: no-store`。
3. 配置齐全时**托盘进程内承载隧道**：`lsof` 显示 `127.0.0.1:18099 (LISTEN)` 属于
   `tunnelmesh-client-tray`，`nc` 连接成功。
4. **互斥实测**：托盘运行期间 `tunnelmesh-client --config <同一份> run` 退出码 1，输出
   `client: another TunnelMesh client is already running with this configuration`。
5. `kill -TERM` 后约 1 秒退出，日志出现 `received terminated, shutting down` 与
   `menu bar shell stopped`；监听端口随即释放。
6. 托盘退出后同一份配置的 CLI `run` 成功拿到锁并绑定同一个端口（`tcp tunnel smoke-tcp
   listening on 127.0.0.1:18099`），证明锁随进程退出释放。
7. 缺少 `client.yaml` 时不崩溃，日志给出可操作的
   `client run requires client.server_url and client.token`。
8. 登录项两个方向实测：开启后 `sfltool dumpbtm` 出现 `Name: TunnelMesh Client`、
   `Disposition: [enabled, ...]`；关闭后变为 `[disabled, ...]` 且不报错。
9. 文件权限实测：`client.lock`、`client.yaml`、`tray.log` 均 `0600`。

冒烟过程中发现并修复两个真实缺陷：

- **`[NSApp stop:]` 不唤醒 run loop**：菜单栏 app 窗口隐藏时没有待处理事件，`stop:` 只置标志位，
  进程永不退出——SIGTERM 与菜单“退出”都表现为“没反应但隧道还在跑”。改为 `stop:` 后补投一个
  application-defined 事件唤醒循环；同时 `TMTrayStop` 由 `dispatch_sync` 改为 `dispatch_async`，
  因为信号处理器在 `NSApp run` 之前就已安装，此时主线程还在 Go 里、不排空主队列，同步派发会死锁。
- **注销未注册的登录项报错**：`SMAppService` 对无记录的 bundle 返回 `NotFound` 而非
  `NotRegistered`，原实现只对后者提前返回，于是关闭“开机启动”会把 `Operation not permitted`
  抛到界面上。现在两种状态都视为“本来就没开”。

一处计划假设被实测推翻并已更新文档：计划认为“未签名/ad-hoc 可能不被 `SMAppService` 接受”，
实测 **ad-hoc 签名的 bundle 注册登录项成功**；被拒的是不含 bundle 的裸二进制。打包脚本的
提示信息与部署文档均按实测改写。

## 发布步骤

1. 常规发布不变：`release` workflow 与 `scripts/build-release.sh` 仍只产出三个二进制，
   不产出托盘。
2. 托盘在 macOS 主机上单独打包：

   ```bash
   cd web-tray && npm ci && npm test -- --run && npm run build && cd ..
   VERSION=vX.Y.Z ./scripts/package-macos-tray.sh
   shasum -a 256 -c dist/vX.Y.Z/macos-tray/SHA256SUMS
   ```

3. 产物为每架构一个 zip（内含 `.app`，`Contents/Resources/` 带 LICENSE 与 NOTICE，满足
   Apache-2.0 4(a)/4(d)）、`SHA256SUMS` 与 `manifest.json`（含 `notarized: false`）。
4. 上传到 GitHub Release 时必须在说明里写清是 ad-hoc 签名：下载副本会被 Gatekeeper 拦一次，
   需要用户显式放行。
5. 正式对外分发前需要补 Developer ID 签名 + 公证（`SIGN_IDENTITY="Developer ID Application: ..."`
   已支持，`notarytool` / `stapler` 步骤本次不做）。
6. 服务端无需迁移；新旧 Server 可与新 client 共存，但旧 Server 没有
   `GET /api/v1/client/agents`，此时路由配置页的 Agent 下拉为空——文档已写明这一现象。

## 回滚步骤

- **客户端**：app bundle 自包含，回滚就是用上一版 zip 覆盖 `/Applications` 里的目录，
  5 分钟内可完成。`client.yaml` 格式未变，旧版托盘与命令行 Client 都能读新版写出的配置，
  不需要配置回滚。唯一状态残留是登录项，换 bundle 后重新打开一次“开机启动”即可。
- **服务端**：`GET /api/v1/client/agents` 是 additive 的独立命名空间，回滚 Server 即消失；
  已发出该请求的托盘只会看到 Agent 下拉为空，不影响既有隧道。
- **共享运行时重构**：`internal/cli/client_run.go` 改为委托 `internal/client.Runtime`。
  回滚代码即恢复原实现，配置与磁盘状态无变化。
- 需要保留“CLI 可与托盘同时运行”的旧行为时，让两者指向不同的 `client.yaml` 即可，锁按配置
  路径派生。

## Reviewer 关注点

1. **互斥语义是不是可接受的破坏**：这是本次唯一改变既有命令行为的地方（`run` 在已有实例时
   拒启）。锁按配置路径派生，因此只影响指向同一份 `client.yaml` 的进程。
2. **权限过滤与分页的交互**：`listAgentsForClientToken` 在累积页时过滤，`HasMore` 取
   `page.HasMore || index < len(page.Items)-1`，cursor 用新导出的 `storage.AgentCursor`。
   请重点看“过滤后恰好取满 limit”和“过滤后本页为空但后续页有匹配”两种边界。
3. **响应面是否够窄**：`clientAgentView` 只有三个字段，且被 openapi 测试双向钉住。
   新增字段应被视为信息披露变更而不是排版变更。
4. **`client.token` 标签未动**：`yaml:"-"` / `json:"-"` 保持原样，明文只存在于 `0600` 的
   `client.yaml` 与 Go 进程内存中，界面永远拿不到。请确认没有新增路径能把它带出去。
5. **构建隔离是不是真的**：`scripts/tray_build_tag_test.go` 五条守卫 + `go vet ./...`
   （无 cgo）+ `go vet -tags tray ./...`（有 cgo）两条命令。
6. **`webdist` 的双半**：`embed.go`（`tray`）与 `embed_stub.go`（`!tray`）必须同时存在。
   无标签时 `FS()` 返回 `nil` 而不是一个空 FS——`api.go` 正是用这个 nil 选择占位页面，
   返回非 nil 空 FS 会渲染出空白窗口。
7. **原生 shim 的两处修复**（run loop 唤醒、`dispatch_async`、`NotFound` 视为未注册）
   都来自实测，注释里写了为什么。
8. **`internal/tray` 与 `internal/tray/native` 的分层**：前者无 cgo 以便在 Linux CI 跑测试，
   后者是唯一的 Cocoa 落点，依赖方向只能是 native → tray。
9. **文档口径**：统计页只给入站字节数（只有这个方向被埋点），没有为了对称而编造出站值。

## 集成状态

- 未提交、未推送、未创建远端 PR：按 AGENTS.md，未经明确授权不执行 commit / push / merge。
- 工作区状态：18 个既有文件被修改，79 个新增文件（含 `web-tray/` 前端与 `internal/tray/`）。
  `internal/tray/webdist/dist/` 与 `web-tray/dist/` 是构建产物，与 `web/dist`、
  `internal/server/web_dist` 一样被 `.gitignore` 的 `dist/` 规则排除，不入库。
- 修改的既有文件：`Makefile`（新增 `tray-web-build` / `tray-release` 两个目标）、
  `README.md` 与 `README.zh-CN.md`（组件表新增一行）、`deploy/README.md`（产物清单与模板约定）、
  `docs/README.md`、`docs/api/openapi.yaml`、`docs/deployment/binary-release.md`、
  `docs/development/testing.md`、`docs/user-guide/client.md`、
  `internal/cli/{client_run,root,serve_watch}.go`、`internal/client/session_pool.go`、
  `internal/server/{api,runtime}.go`、`internal/storage/repository.go`。
  另有 `docs/pull-requests/README.md` 与 `docs/superpowers/plans/README.md` 由
  `scripts/gen_doc_index.py` 重新生成——其中 `2026-09-29-audit-log-details-phase-p0` 一行多出的
  计划链接是既有索引相对生成器的漂移被本次重算纠正，与托盘无关。
- 本次**未改动**：管理后台 `web/`、`internal/server/web_dist`、`migrations/`、
  `SchemaVersion`、`scripts/build-release.sh` 的构建矩阵、`.github/workflows/`。
- 最终验证（全部本机执行）：`go build ./...`、`go vet ./...`、`git diff --check`、
  `CGO_ENABLED=1 go vet -tags tray ./...` 通过；`go test ./... -count=1` 退出码 0、28 个包 ok；
  `go test -race ./... -timeout 30m` 退出码 0、28 个包 ok、无 DATA RACE；
  `cd web-tray && npm test -- --run` 9 文件 / 65 用例通过，`npm run build` 产物与
  `internal/tray/webdist/dist` `diff -qr` 一致。
- 明确的后续项（本次不做）：Developer ID 签名与公证、`.icns` 图标资产、Windows / Linux 托盘、
  出站字节数埋点、把 `go build/test ./...` 与 `-tags tray` 构建加进 CI（当前 CI 无通用 Go 构建
  作业，属既有缺口）。

一处需要 Reviewer 决定的口径问题：`AGENTS.md` 的 project-doc 段仍写“包含三个可执行程序”。
本次没有改动 `AGENTS.md`（它是工程规范的权威来源，且托盘不进发行矩阵，就发行物而言“三个”
仍然成立）。如果希望把托盘登记为第四个程序，需要单独确认。

## 补记（2026-10-06）：进发行矩阵与安装映像

上文是当时的时点记录，不改写。本节只追加其后发生的两件事：**托盘进入 GitHub Release**，以及
**磁盘映像从“能装”变成“看起来就是安装包”**。自本节起，上文“本次未改动
`.github/workflows/`、`scripts/build-release.sh` 的构建矩阵”与“托盘不进发行矩阵”两处口径不再
成立；其余结论不变。

### 变更

- `.github/workflows/release.yml` 拆成三个作业：`version` 一次解析并校验版本号
  （`^v[0-9]+\.[0-9]+\.[0-9]+$`，tag 与手动 dispatch 走不同来源，因此必须只解析一次），输出给
  另外两个作业；`macos-tray` 在 macos-15 上构建 `web-tray` 并调用打包脚本，校验两个 `.dmg`、
  `shasum -a 256 -c SHA256SUMS`、清单可解析，上传 artifact `macos-tray`；`release` 在
  ubuntu-latest 上跑跨平台矩阵并以 `TRAY_DIST_DIR` 合并托盘，最后 `gh release create`。
  两个作业不可能对“同一个 Release 是哪个版本”产生分歧。
- 新增 `scripts/merge-tray-dist.sh`：先校验来源目录自己的 `SHA256SUMS`（artifact 经过上传下载，
  文件名相同不等于字节相同），再复制 `.dmg`、把重算的校验和**追加**进同一份 `SHA256SUMS`、把
  托盘清单发布为 `manifest-tray.json`。`scripts/build-release.sh` 在十八次交叉编译**之前**先
  `--check`，未设置 `TRAY_DIST_DIR` 时行为与此前完全一致；跨平台 `manifest.json` 的 schema 不变。
- `Makefile` 新增 `release-with-tray`。
- `scripts/package-macos-tray.sh` 的产物由 zip 改为 `.dmg`，再改为**两段式**安装映像：
  `hdiutil create -format UDRW -fs HFS+ -size <内容+空余>` → `hdiutil attach -mountpoint` →
  `osascript deploy/macos/tray-dmg-layout.applescript` → `sync` + `hdiutil detach` →
  `hdiutil convert -format UDZO -imagekey zlib-level=9` → `hdiutil verify`。
- 新增 `deploy/macos/tray-dmg-layout.applescript`：驱动 Finder 把挂载窗口摆成 640×420 居中的
  图标视图、96px 图标，app 落在 `(170, 210)`、`Applications` 落在 `(470, 210)`，并在关闭窗口
  前读回坐标作为成功判据。窗口几何只有这一个来源。
- 布局这一步是尽力而为：没有可脚本化 Finder 的主机会重试三次、把警告打到 stderr、继续产出
  映像，并把该资产记为 `installerLayout: false`；`release.yml` 读取该字段写进 job summary，
  缺失时打 `::warning::`。静默降级成普通文件夹窗口是这里唯一不可接受的失败形态。
- 新增 `DMG_BACKGROUND`（可选）：背景 png 落到卷内 `.background/background.png` 并打上
  `chflags hidden`；仓库自身不带背景，默认无。

### 测试（先红后绿）

`deploy/macos/tray_bundle_test.go` 新增 `TestPackageScriptLaysOutAnInstallerWindow` 与
`TestLayoutScriptDrivesFinderThroughTheInstallerWindow`，`scripts/release_workflow_test.go` 的
macOS 作业断言加入 `installerLayout`。三条守卫在实现前均按预期失败，实现后通过。

### 验证证据（本机 macOS 14.6 / arm64）

- `VERSION=v0.0.0-review ARCHES="arm64 amd64" ./scripts/package-macos-tray.sh` 退出 0，无布局
  警告；`manifest.json` 两个资产均为 `installerLayout: true`。映像体积比直接
  `-format UDZO` 更小（arm64 5,144,260 → 4,507,946 字节，amd64 5,725,896 → 4,992,000 字节）。
- 只读挂载产出的映像后向 Finder 读回：`170210|470210|icon view|96|4152681055688`，即两个坐标、
  图标视图、96px、窗口 `{415,268}–{1055,688}`（640×420 居中）。卷内 app 的
  `codesign --verify --verbose=2` 通过，`Contents/Resources` 里有 `LICENSE` 与 `NOTICE`。
- `DMG_BACKGROUND=<png>` 的一次性构建：卷内 `.background` 带 `hidden` 标记，`.DS_Store` 里有
  `backgroundImageAlias`。
- 把布局脚本换成必然失败的版本后的一次性构建：退出码仍为 0，stderr 出现三次
  `installer layout attempt N/3 failed` 与一条降级警告，清单记 `installerLayout: false`；
  布局脚本随后按 sha256 还原确认未改动。
- 端到端发行链路：`VERSION=v0.0.0-installer ./scripts/package-macos-tray.sh` 后
  `VERSION=v0.0.0-installer TRAY_DIST_DIR=dist/v0.0.0-installer/macos-tray ./scripts/build-release.sh`
  退出 0，产出 6 个归档 + 2 个 `.dmg` + 一份 `SHA256SUMS` + `manifest.json` +
  `manifest-tray.json`；`shasum -a 256 -c SHA256SUMS` 八项全 OK，`manifest.json` 仍为
  `schemaVersion: 16` / 6 platforms / 6 assets，合并后的 `.dmg` 再挂载读回仍是
  `170210|470210|icon view|96`。
- 重复合并被拒：对已含托盘的 Release 目录再次合并时，`merge-tray-dist.sh` 以
  `already exists; refusing to publish two builds under one asset name` 失败，不覆盖既有资产。
- 常规门禁：`go test ./... -count=1` 28 个包 ok；`go test -race ./... -timeout 30m` 28 个包
  ok、无 DATA RACE；`go vet ./...` 与 `CGO_ENABLED=1 go vet -tags tray ./...` 通过；
  `bash -n` 三个脚本通过；`git diff --check` 干净。全量并行跑 `go test ./...` 时
  `internal/e2e` 的 `TestSOCKS5WebPageLatency/refused_target_does_not_reconnect_agent_session`
  失败过一次，单独连续三次运行均通过；本次改动不含任何 Go 运行时代码，判定为既有的时序敏感
  用例在全量并发下抖动，未纳入本次修复范围。

### 仍待确认与后续

- GitHub macOS runner 上 Finder 是否总能被脚本驱动，本次**未在真实 runner 上验证**。不可用时
  发布不会失败，但产物会退化成普通文件夹窗口，并由 `installerLayout: false` 与 job summary
  如实标出——首个由 workflow 产出的 Release 需要人工看一眼这两处。
- Developer ID 签名与公证、`.icns` 图标资产、把托盘接进 `download_api.go` 的下载清单，仍沿用
  上文的后续项口径；公证与装订会改变映像字节，之后必须重算 `SHA256SUMS` 并把
  `manifest-tray.json` 的 `notarized` 置为 `true` 再合并。

## 补记（2026-10-06）：图标、键盘粘贴、文本辅助与 auth_mode 校正

### 背景

真机反馈两条：Finder 里 `TunnelMesh Client.app` 没有图标；主界面路由配置的输入框不能粘贴
（⌘V 与 ⌃V 都不生效），且首字母会被自动大写。顺着“已支持的配置项是否都进了界面”自查时，
又发现托盘表单提供了 `config.Validate` 并不接受的 `auth_mode: remote`。

### 变更

- **图标**：新增 `scripts/trayicon/main.go`（纯标准库绘制，几何即代码）、
  `scripts/generate-tray-icon.sh`（`go run` + `iconutil`）与产物
  `deploy/macos/TunnelMeshClient.icns`。打包脚本在 **Finder 布局之后**把同一份 `.icns` 作为
  `.VolumeIcon.icns` 写入挂载点并 `SetFile -a C`，安装器卷因此也带品牌图标。
- **键盘**：`internal/tray/native/tray_darwin.m` 安装应用主菜单——Edit 的 ⌘X/⌘C/⌘V/⌘Z/⇧⌘Z/
  ⌘A 走 responder chain，⌘Q 复用托盘自己的 `TMActionQuit` 路径（停运行时、释放锁再退出）；
  另加 local key-down monitor，把 ⌃X/⌃C/⌃V 直接派发到 responder chain。⌃A/⌃E 保持 Cocoa 原有
  的行首/行尾语义，不改写。
- **文本辅助**：`TMDisableSystemTextAssists` 同时 `registerDefaults:` 与写**本 bundle 持久域**，
  关掉自动大写、自动纠错、文本替换、引号替换与破折号替换；前端新增
  `web-tray/src/textInput.ts`，路由配置与配置目录每个技术字段绑定
  `autocapitalize=none`/`autocorrect=off`/`spellcheck=false`/`autocomplete=off`。
- **auth_mode 校正**：下拉去掉 `remote`（`socks5` 只有 `none`/`password`，`http-proxy` 只有
  `none`/`basic`）；`auth_url` 与认证方式解耦，代理协议下始终可填并带说明；store 里把空的
  `authMode` 归一为 `none`；检测新增 `tunnel.authUrl`——必须是绝对 http(s) URL，写在
  `tcp`/`udp`/`http` 上给警告，未填不报（远程校验本就关闭）。
- **配置项覆盖度**：隧道 9 个字段（`name`/`protocol`/`listen`/`agent_id`/`target_host`/
  `target_port`/`auth_mode`/`allow_remote`/`auth_url`）与 `server_url`/`token`/`mode` 均已进界面，
  协议列表取自 `client.SupportedTunnelProtocols()` 单一来源。`allow_remote` 原本就在，但两端都
  没有回归，故补 Go 的 `client.yaml` 往返断言与前端开关断言。未进界面的
  `instance_id`/`instance_id_path`/`connections.*`/`stream.*`/`remote_validation.*`/`metadata`
  由节点树式保存原样保留，这一点写进了用户文档。
- **文档**：`docs/user-guide/client-tray.md`（快捷键、字段表纠正 `listen_addr`→`listen`、未进界面
  的配置项、检测项、排障两条）、`docs/deployment/macos-client-tray.md`（应用图标章节、卷图标
  顺序与原因、可机器核对的图标验证片段）、`deploy/README.md` 一行。

### 实测证据（本机 macOS 14.6 / arm64，有 window server 会话）

- **粘贴因果链**：用合成 `NSEvent` 经 `-[NSApplication sendEvent:]` 打给真实 WKWebView，剪贴板
  预置 `pasted-ok`。无主菜单 → 字段仍为空（复现反馈）；装上带 Edit 动作的主菜单 → 同一 ⌘V
  粘贴成功；再装 monitor → ⌃V 也成功（`sendAction:paste:` 由 `WKWebView` 接受，返回 1）。
  monitor 的 mask 必须是 `NSEventMaskKeyDown`：传 `NSEventTypeKeyDown` 时 handler 一次都不触发，
  这个坑有守卫测试兜住。
- **首字母大写**：WebKit 侧未复现——当前输入源是拼音（`com.apple.inputmethod.SCIM.ITABC`）时，
  逐字符注入 `hello world` 原样落在字段里；合成事件绕过输入法转换，因此该实验只排除了 WebKit
  自身的大写逻辑，不能证明输入法上屏显示（拼音首字母显示为大写）不是来源。这台机器的系统开关
  确认是开的：`defaults read -g NSAutomaticCapitalizationEnabled` = 1。
- **两种覆盖手段各有作用域**：`NSSpellChecker` 类属性实测——`registerDefaults:` 关掉自动纠错，
  对自动大写**无效**；自动大写只认持久域。而 `objectForKey:` 会穿透到全局域，所以“已有值就不写”
  的判断恰好跳过了唯一需要写的那一项：首版实现只持久化了 4 个键，改为无条件写入后 5 个键齐全，
  且 `defaults read -g NSAutomaticCapitalizationEnabled` 仍为 1（不动用户的系统设置）。
- **图标**：`NSWorkspace iconForFile:` 对最终映像里的 app 与卷都解析出品牌图标；映像内
  `CFBundleIconFile=AppIcon`、`Resources/AppIcon.icns` 111303 字节、卷根带 `.VolumeIcon.icns`。
  顺序实测：布局前拷入 → 挂载后是通用磁盘图标（`update volume` 会应用并删除该文件，而被应用的
  图标活不过 `hdiutil convert`）；布局后拷入 + `SetFile -a C` → 品牌图标。
- **产物**：`VERSION=v0.0.0-trayfix ARCHES=arm64 ./scripts/package-macos-tray.sh` 产出 4654356
  字节映像，`installerLayout: true`，`codesign --verify` 通过；打包脚本的产物新鲜度检查通过，
  发行 JS 里 socks5 的认证方式已是 `` `none`,`password` ``，不再出现 `remote`。
- **本环境无法验证**：真键盘输入被 TCC 拒绝（`AXIsProcessTrusted=0`，`osascript` 发送按键报
  1002），因此 ⌘V/⌃V 与首字母大写的最终确认仍需在 GUI 里手点一次。

### 测试（先红后绿）

- 红：`TestShimInstallsAMainMenuForEditingShortcuts`、`TestShimMapsControlKeyEditingShortcuts`、
  `TestShimTurnsOffSystemTextAssists`、`TestBundleShipsABrandIcon`、
  `TestValidateRejectsUnusableAuthURL`、`TestValidateAcceptsAWellFormedAuthURL`、
  `TestValidateWarnsWhenAuthURLCannotApply`、`TestValidateStaysSilentWithoutAnAuthURL`，
  以及前端 proxy-auth 两条与文本辅助两条。
- 绿：`go test ./... -count=1` 全部 ok；`go test -race ./... -count=1` 全部 ok、无 DATA RACE；
  `go vet ./...` 与 `CGO_ENABLED=1 go vet -tags tray ./...` 通过；
  `cd web-tray && npm test -- --run` 70 passed、`npm run build` 通过；`bash -n` 四个脚本通过；
  `git diff --check` 干净；`gofmt -l internal scripts deploy cmd` 无输出。

### 顺带

`internal/server/api.go` 与 `internal/server/client_scoped_api_test.go` 的 gofmt 对齐（上一轮遗留，
纯空白）。上文“仍待确认与后续”里列的 `.icns` 图标资产一项，本次已交付。

## 补记（2026-10-06）：v1.3.1 的托盘映像装不上 macOS 14

### 现象与根因

合并进 `main` 后发出的 v1.3.1 里，`tunnelmesh-client-tray-v1.3.1-darwin-arm64.dmg` 在
macOS 14.6 上打开报“应用程序 “TunnelMesh Client” 的这个版本不能与此版本的 macOS 配合使用。
你使用的是 macOS 14.6。该应用程序要求 macOS 15.0 或更高版本”。

对已发布产物直接取证：

```bash
hdiutil attach -nobrowse -readonly tunnelmesh-client-tray-v1.3.1-darwin-arm64.dmg
otool -l "/Volumes/TunnelMesh Client/TunnelMesh Client.app/Contents/MacOS/tunnelmesh-client-tray" \
  | grep -A4 LC_BUILD_VERSION      # -> minos 15.0, sdk 15.5
plutil -extract LSMinimumSystemVersion raw \
  "/Volumes/TunnelMesh Client/TunnelMesh Client.app/Contents/Info.plist"   # -> 13.0
hdiutil detach "/Volumes/TunnelMesh Client"
```

`Info.plist` 与 `manifest.json` 都写着 13.0，但 LaunchServices 判定用的是 Mach-O 的
`LC_BUILD_VERSION.minos`。托盘走 cgo，链接由 clang 完成，clang 在没被指定目标版本时按**构建主机**
写 `minos`；`.github/workflows/release.yml` 的托盘作业跑在 `macos-15`，于是产物记成 15.0。
`scripts/package-macos-tray.sh` 里原本有一行 `MIN_MACOS="13.0"`，但它只被写进 `manifest.json`，
从未参与编译，也没有任何地方核对产物——三处可以同时“看起来正确”，而包根本装不上。

同一发行包里的 `tunnelmesh-v1.3.1-darwin-arm64.tar.gz`（server/agent/client 三个二进制）是 Linux
交叉编译、`CGO_ENABLED=0` 走 Go 内部链接器，实测 `minos 12.0`，不受影响。

### 变更

- `scripts/package-macos-tray.sh`：删掉脚本里那份 `MIN_MACOS` 字面量，改为
  `plutil -extract LSMinimumSystemVersion raw "$INFO_TEMPLATE"` 从 plist 读出（单一来源；读出的值
  不是 `X.Y` 就直接失败）；编译时同时设 `MACOSX_DEPLOYMENT_TARGET` 与 `CGO_CFLAGS`/`CGO_LDFLAGS`
  的 `-mmacosx-version-min`；新增 `verify_deployment_target`，每个架构链接完立刻 `otool -l` 读回
  `minos` 并与 plist 比对，不一致就让打包失败；`otool` 进必备工具清单。
- `deploy/macos/tray_bundle_test.go`：新增 `TestPackageScriptPinsTheMachOSDeploymentTarget`，
  守卫“从 plist 派生 + 两个编译开关 + 读回校验”，并禁止脚本再出现 `MIN_MACOS="<数字>` 字面量；
  `TestPackageScriptMatchesTheTemplate` 对脚本的要求从“含 `13.0` 字面量”改为“含
  `LSMinimumSystemVersion`”，即要求它去读 plist。
- 文档：`docs/deployment/macos-client-tray.md` 新增“最低系统版本”一节（含如何检查一个已经下载
  下来的映像）；`docs/user-guide/client-tray.md` 排障表新增该现象；`deploy/README.md` 与
  `docs/development/testing.md` 同步守卫口径。

### 实测证据（本机 macOS 14.6 / arm64）

- 对照实验说明为什么两个开关都要设：默认构建 `minos 14.0`（等于宿主）；只设
  `MACOSX_DEPLOYMENT_TARGET=13.0` 得到 `minos 13.0`，但伴随 16 条
  `ld: warning: object file ... was built for newer 'macOS' version (14.0) than being linked (13.0)`；
  再加 `CGO_CFLAGS`/`CGO_LDFLAGS` 后 `minos 13.0` 且零警告。
- 红：新测试在未改脚本时报 5 条 `must reference` 与 1 条字面量重复。
- 绿：`go test ./deploy/macos/ -count=1` 通过。
- 端到端：`VERSION=v0.0.0-minosfix ARCHES=arm64 ./scripts/package-macos-tray.sh` 打印
  `verified: ... runs on macOS 13.0 or later`；挂载产物后 `LC_BUILD_VERSION` 为
  `minos 13.0` / `sdk 15.0`，`LSMinimumSystemVersion` 为 13.0，`codesign --verify` 通过，
  映像 4660838 字节。
