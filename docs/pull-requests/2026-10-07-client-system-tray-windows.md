# feat(tray): Windows 系统托盘客户端与 NSIS 发行打包

- **目标分支**：`main`
- **实施计划**：`docs/superpowers/plans/2026-10-07-client-system-tray-windows.md`

## 摘要
把 macOS 已有的托盘客户端复刻到 Windows：**只替换原生壳层**。`internal/tray`（配置读写、
loopback API、检测、统计、互斥）与 `web-tray/`（四 tab 设置界面 + 快捷小窗）原样复用，新增
`internal/tray/native` 的 Windows 实现——纯 Go 的 Win32 + WebView2，无 cgo，可从 Linux 交叉编译。
发行侧新增 `scripts/package-windows-tray.sh`：`go-winres` 把品牌图标、版本信息与应用清单戳进
exe，`makensis` 产出每用户安装包，与绿色 `.zip` 一起进 GitHub Release 与 CI。

托盘菜单三项（打开主界面 / 打开官网首页 / 退出）、主窗口取工作区 50%、通知区域左键快捷小窗、
开机启动、关闭最小化到托盘、与 `tunnelmesh-client run` 按配置文件互斥——两平台语义一致。

## 用户影响
- Windows 用户第一次有了图形客户端；macOS 行为**逐条未变**（含 `installerLayout`、磁盘映像与
  登录项语义）。
- `tunnelmesh-client` 的 CLI 命令面与参数不变。Windows 上的 `tunnelmesh-client run` 新增一条
  语义：同一份 `client.yaml` 已被另一个实例（托盘或另一个 CLI）持有时拒启并给出明确错误。
  这与 macOS 上已发布的行为一致，是“互斥”要求的直接结果。
- 设置界面里与平台相关的文案（主题跟随、开机启动、快捷小窗）改为按 `/api/settings` 返回的
  `platform` 取词，不再硬编码 “macOS/菜单栏”；未知平台回落中性词。
- Windows 需要 Microsoft Edge WebView2 运行时（Win11 与近年 Win10 已预装）。缺失时托盘仍启动，
  tooltip 提示、“打开主界面”改用系统浏览器打开同一个本地地址、关于页显示降级横幅。

## API / Schema / 配置影响
- 无 HTTP API 变更、无数据库 Schema 变更、无 `config.Validate` 模型变更（`client.tunnels` 及其
  全部已支持字段沿用现状）。
- 本地接口 `GET /api/settings` 响应新增 `platform`（`macos` / `windows`，由 `runtime.GOOS` 得出），
  `GET /api/system` 新增 `renderer` / `rendererDetail`。两者都是只读展示字段，不参与写路径。
- 配置文件与目录结构不变：`client.yaml`、`tray.json`、`client.lock`、`tray.log`。Windows 默认
  目录同为 `%USERPROFILE%\.config\tunnelmesh\`。Windows 新增 `<PrefsDir>\webview2`（WebView2 自己的
  profile，不与系统 Edge 配置共享）。
- 锁实现按平台分文件：Unix `flock`、Windows `LockFileEx(LOCKFILE_EXCLUSIVE_LOCK |
  LOCKFILE_FAIL_IMMEDIATELY)`，错误值仍是同一个 `ErrRuntimeAlreadyRunning`；
  `runtime_lock_other.go` 的约束收窄为 `!unix && !windows`，即**其他**平台仍无锁（现状不变）。
- `go.mod`：新增直接依赖 `github.com/wailsapp/go-webview2`、`golang.org/x/sys` 由 indirect 转
  direct，新增 `tool github.com/tc-hib/go-winres`。默认（无标签）构建不链接任何一个。

## 安全与授权
- 本地 API 仍是 `127.0.0.1` 随机端口 + 每次启动随机 secret 逐请求常量时间比较 + Origin 校验 +
  CSP/`X-Frame-Options`；Windows 复用同一实现，未新增免鉴权端点，也未把 secret 放进 URL 之外的
  持久位置。
- Token 只在 Go 侧持有完整值，`/api/settings` 仍返回掩码；日志与错误信息不含 Token。
- WebView2 显式关闭：状态栏、默认右键菜单、表单自动填充、密码自动保存；开发者工具仅在
  `TUNNELMESH_TRAY_DEVTOOLS=1` 时开放——设置页里唯一敏感字段是 Token，不允许宿主顺手保存它。
- 开机启动写 `HKCU`（当前用户），不需要管理员，也不会把隧道带到别的用户会话里。
- exe 清单为 `asInvoker`（不申请提权）+ per-monitor-v2 DPI；单实例由
  `Local\TunnelMeshClientTray` 命名 mutex 保证。
- 交付物**未做 Authenticode 签名**：首次下载会触发 SmartScreen。Release 说明与 `README.txt`
  给出“更多信息 → 仍要运行”，并要求先按 `SHA256SUMS` 核对。签名/公证是发布动作，本 PR 不擅自
  假设证书存在。

## 测试证据
- `go test ./... -count=1`：全绿（含 `deploy/windows` 新包、`scripts` 新断言、
  `internal/tray/platform_test.go`）。
- `go test -race ./... -count=1`：除 `internal/e2e` 的
  `TestSOCKS5WebPageLatency/one_blocked_dial_does_not_delay_other_opens` 外全绿。该子测试断言
  发往 `10.255.255.1:18081` 的拨号必须失败；单独重跑三次全部通过，非 race 全量跑也通过，本机
  `nc -z -G 2 10.255.255.1 18081` 当前确实不可达——它是开发机网络环境（默认网关应答出站 SYN）
  导致的既有抖动，与本改动无关（本改动不触碰 Agent 侧拨号与 SSRF 策略）。
- `go vet ./...`、`gofmt -l internal cmd scripts deploy`、`git diff --check`：均无输出。
- 交叉编译门禁：`CGO_ENABLED=0 GOOS=windows GOARCH=amd64|arm64 go build -tags tray ./...` 与
  `GOOS=windows go vet -tags tray ./...` 通过；`CGO_ENABLED=1 go vet -tags tray ./...`（macOS 半）
  通过——两平台同时绿才说明 `types.go` 的抽离没把任何一端拆坏。
- 前端：`cd web-tray && npx vitest run` 11 文件 97 用例全绿（新增 `platform.spec.ts` 8 例：
  macos/darwin/windows/win32 归一、未知平台回落中性词、四 tab 与快捷小窗回归）；
  `npm run build` 通过并镜像到 `internal/tray/webdist/dist`。
- 资源戳记实测：`VERSION=v1.4.2 ./scripts/package-windows-tray.sh` 产出两个 `.zip` +
  `SHA256SUMS` + `manifest.json`；`go version -m` 读回 `GOOS=windows`、`GOARCH=amd64`、
  `tags=tray`、`CGO_ENABLED=0`；`go-winres extract` 在 exe 里读到**恰好一份** `RT_MANIFEST`
  （per-monitor-v2 + asInvoker + common controls v6）、一份 `RT_GROUP_ICON/APP`（372526 字节的
  品牌 `.ico`）与 `RT_VERSION`（`OriginalFilename=TunnelMeshClient.exe`、版本 `1.4.2`）；
  PE `Subsystem` 读回 2（GUI）。
- 失败分支实测：`VERSION=1.2`、`ARCHES=386`、`INSTALLER_ARCH=x86` 各自在编译前退出并给出原因；
  移走 `installer.nsi` 时报 “missing build input”；`makensis` 退出 0 但没写出文件时报
  “makensis did not produce …setup.exe” 且退出码 1（不会发布空安装包）。
- 互斥：`GOOS=windows go vet ./internal/client/` 通过（Windows 锁实现编译进 `client`），
  `runtime_lock_windows_test.go` 带 `//go:build windows`，只能在 Windows 上执行（见下）。
- 图标确定性：`WINDOWS_ONLY=1 scripts/generate-tray-icon.sh` 重跑后
  `TunnelMeshTray.ico` 仍为 7886 字节、`TunnelMeshClient.ico` 仍为 372526 字节，且
  `deploy/macos/TunnelMeshClient.icns` 保持 111303 字节未变；`deploy` 与 `internal/tray/native/icons`
  两份 `.ico` 逐字节一致（测试断言）。
- macOS 回归：`VERSION=v0.0.0-wincheck ./scripts/package-macos-tray.sh` 完整跑通两个 `.dmg`，
  `codesign --verify` 与 `hdiutil verify` 通过。
- 文档：`python3 scripts/gen_doc_index.py` 重建索引；`deploy/README.md` 产物清单补齐
  `deploy/windows/` 四个新文件与（此前遗漏的）菜单栏 PNG。

### 未能在本机自动执行的部分
- **`makensis` 冒烟编译**：本机 `makensis` 不可得（Homebrew 无 `nsis` 公式、容器镜像源被网络策略
  挡住）。改为静态核对：安装脚本引用的每个 MUI2 宏与语法（`MUI_FINISHPAGE_SHOWREADME_FUNCTION`
  等）都对照 NSIS 3.10 发行包自带的 `Contrib/Modern UI 2` 与 `Examples` 逐个确认存在，
  `MUI_UNGETFILENAME` 这类只在 MUI1 存在的宏已删除。`.github/workflows/ci.yml` 的新 `windows-tray`
  作业会在每次 PR 上用真实 `makensis` 编译一次——这是该文件的第一道可执行门禁。
- **Windows 真机冒烟**：本机无 Windows。清单在
  `docs/deployment/windows-client-tray.md#真机冒烟清单`（12 项），需由使用者执行一次并把结果记回
  本文件；`runtime_lock_windows_test.go` 与 WebView2 行为只有在那台机器上才成立。

## 发布步骤
1. 合并后打标签 `vMAJOR.MINOR.PATCH`（或手动 dispatch `release` workflow）。
2. `macos-tray` 与 `windows-tray` 两个作业分别产出 `.dmg` 与 `.zip`+`setup.exe` 并上传 artifact；
   `release` 作业下载两者、跑跨平台矩阵、合并成**一个** Release（11 个资产、一份 `SHA256SUMS`、
   三份 manifest）。
3. 下载页任一资产缺失即作业失败（数量断言 + `if-no-files-found: error`），不会发布半成品。
4. 需要签名时：对 `TunnelMeshClient.exe` 与 `setup.exe` 追加 Authenticode 签名后重传，
   NSIS 脚本与打包脚本无需改动；同时在 Release 说明里移除 SmartScreen 段落。

## 回滚步骤
- 发行层：`gh release edit <上一版> --draft=false --latest`（必要时
  `gh release delete <坏版本> --cleanup-tag --yes`）。托盘不改数据库、不迁移配置，回滚没有数据面。
- 代码层：删除 `internal/tray/native/*_windows.go`、`internal/client/runtime_lock_windows.go`、
  `scripts/package-windows-tray.sh`、`deploy/windows/` 四个新文件与 `cmd/` 的两个信号文件，并把
  `types.go` 内容并回 `native.go`。默认构建、CLI、macOS 托盘与跨平台矩阵均不依赖它们。
- 已装机器：卸载程序删除 exe、快捷方式与两处 `HKCU` 登记；配置目录保留。手工清理
  `reg delete "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" /v TunnelMeshClient /f`。

## Reviewer 关注点
1. **`build-release.sh` 的托盘合并改成了循环**：两个 hand-off 走同一段逻辑，`mergeScript` 的调用
   点仍是两处（早校验、晚合并），测试对此有计数断言。请确认 `${!handoff_var}` 的间接展开在
   bash 3.2（macOS 默认）上按预期工作——脚本已显式给两个变量赋值，因此不会在 `set -u` 下炸。
2. **`merge-tray-dist.sh` 不再按 `.dmg` 白名单挑文件**，而是排除 `SHA256SUMS`/`manifest.json`、
   点开头文件与 `.log`/`.txt`/`.json`。这意味着托盘输出目录里“任何别的已发布文件”都会进 Release，
   请确认这是想要的默认（打包脚本自身只写 zip/exe/清单）。
3. **Windows 上的 `client run` 语义变化**：拒启而非并存。这是“互斥”要求的落地，但它是 Windows
   用户可感知的新行为。
4. **WebView2 的 `Embed(hwnd)` 必须在消息循环内调用**（它自己泵消息），因此嵌入动作经
   `wmMsgEmbedMain`/`wmMsgEmbedPanel` 投递回 UI 线程；go-webview2 内部失败会 `os.Exit(1)`，
   所以运行库探测必须发生在 `Embed` 之前。改这段时请先读 `webview_windows.go` 顶部注释。
5. **`.syso` 是构建输入**：只由打包脚本临时生成。守卫测试同时检查“不入库”和“Windows 侧不得
   `import "C"`”，不要为了让 CI 绿而放宽。
6. `winres.json` 的三层结构（类型→名称→**语言 ID**→资源）不要回退成两层：那是本次实现踩过的坑
   （报 `invalid language identifier`，且只在打包时才暴露）。

## 集成状态
- 分支：`codex/tray-windows`（自 `main` 的 `e86a6ed` 切出），两个提交：
  `feat(tray): 任务栏快捷小窗与平台中立的托盘共享层`（macOS 轮次与共享层）与
  `feat(tray): Windows 系统托盘与 NSIS 发行打包`（Windows 壳、打包、CI 与文档）。
- 分支已推送；合并 `main` 需用户授权后由作业或维护者执行。
- Windows GUI 行为与 Windows 真机冒烟为**未闭环项**，分别由 ci.yml 作业（含 `makensis` 冒烟编译）
  与 `docs/deployment/windows-client-tray.md` 的清单兜底。

## 追加：CI 首跑反馈的一处构建隔离缺陷

`ci.yml` 的 `windows-tray` 作业首次运行即失败，两条编译错误：

```
internal/server/web.go:19:12: pattern all:web_dist: no matching files found
internal/tray/webdist/embed.go:32:12: pattern all:dist: no matching files found
```

根因与修复：

- 该作业用 `go build/vet -tags tray ./...`。`./...` 会连带编译 `internal/server`，而它的
  `//go:embed all:web_dist` 需要管理后台产物；作业从未构建 `web/`，所以在干净检出里必然失败。
  本地跑不出这一条，是因为开发机上那份产物早就存在——这类“本机有产物、CI 没有”的差异正是
  `build-test` 作业先跑 `npm ci`+`npm run build` 的原因。
- `-tags tray` 同时让 `internal/tray/webdist/embed.go` 生效，托盘前端产物也必须存在。
- 修法两头都做：作业显式 `cd web-tray && npm ci` + `npm run build`（托盘二进制不依赖管理后台），
  并把包清单从 `./...` 收窄到带 `tray` 标签的目录加 client 锁
  （`./cmd/tunnelmesh-client-tray ./internal/tray/...`、`./internal/client`）。后者顺带带来一个
  额外收益：`go vet` 会读测试文件，`runtime_lock_windows_test.go` 因此在没有 Windows 主机的
  情况下也被编译校验。
- `scripts/ci_workflow_test.go` 同时断言“必须出现收窄后的清单”和“不得再出现 `./...`”，避免有人
  为了省事把它改回去；`docs/development/testing.md` 与 `docs/deployment/windows-client-tray.md`
  里的命令块同步改写并写明为何不用 `./...`。
- 复验方式：把 `HEAD` 用 `git archive` 导到干净目录（**不**预置 `internal/server/web_dist`，
  也没有 `internal/tray/webdist/dist`），按作业顺序跑 `npm run build` → `gofmt` → 两架构
  `go build` → 两条 `go vet` → 守卫与逻辑测试，全部通过；`release.yml` 的 `windows-tray` 作业
  不受影响，因为 `package-windows-tray.sh` 只编译 `./cmd/tunnelmesh-client-tray` 且已构建
  `web-tray`。

## 追加：CI 首跑反馈的第二处——快捷小窗测试在数微任务

Release 作业的 macOS runner 与 Linux runner 同时红在 4 个 `quick-panel` 用例上，本机 97 用例全绿：

```
× renders the run summary and one row per tunnel   expected '已停止' to contain '运行中'
× reports a blocked lock instead of an empty panel  expected '已停止' to contain '另一个'
× names the live session ...                       expected '未配置' to be '已连接'
× keeps saying 不可达 ...                           expected '未配置' to be '不可达'
```

- 根因：测试用 `flush(6)` 这类手调的微任务圈数等接口回填 DOM。小窗是**串行两跳**
  （`settings` 应用语言/主题 → `stats` 渲染摘要），一跳花几拍由运行时的 `fetch` 实现决定：
  本机 Node 24 恰好够，runner 的 Node 22 不够，于是渲染出“取数之前”的状态。属于测试写法问题，
  不是小窗功能问题——三处 `已停止`/`未配置` 正是组件的初始值。
- 修法按根因来：`src/tests/helpers.ts` 增加 `flushUntil(谓词)`（微任务自旋到条件成立，默认
  4000 轮；只用微任务，因为有 3 个用例跑在 fake timers 下），小窗的 11 处等待全部改为看条件；
  `flush(n)` 保留给“点一下再看调用记录”的场合，实现改成每单元排空一轮，不再假设一跳一拍。
- 为什么不是把 6 改成更大的数：新增的 `src/tests/panel-timing.spec.ts` 给每个响应人为加 500 拍，
  用固定圈数（即便 6×64）必红、用 `flushUntil` 必绿——把这条教训钉成可执行断言。
- 顺带补缺口：`ci.yml` 的 `windows-tray` 作业此前只构建不测试托盘前端，所以托盘前端要等到
  Release 作业才被第一次跑到。现在该作业必须执行 `cd web-tray && npm test -- --run`，
  `scripts/ci_workflow_test.go` 对这一步有断言。
- 复验：`cd web-tray && npx vitest run` 12 文件 98 用例全绿（新增 1 个守卫用例）；
  `npm run build` 重新镜像托盘产物；`go test ./scripts` 绿；`go test ./... -count=1` 绿。
