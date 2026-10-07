# TunnelMesh Client 系统托盘（Windows）

## 目标
- 在不动 macOS 托盘行为的前提下，为 Windows 复刻同一套托盘：通知区域图标 + 三项菜单
  （打开主界面 / 打开官网首页 / 退出）、约屏幕 50% 的主窗口、四 tab（通用 / 统计 / 路由配置 /
  关于）、左键快捷小窗、开机启动、关闭最小化到托盘、与 `tunnelmesh-client run` 互斥。
- 实现方式是**只替换原生壳层**：`internal/tray`（配置、loopback API、检测、统计）与
  `web-tray/`（界面）原样复用，新增 `internal/tray/native` 的 Windows 实现（纯 Go + WebView2，
  无 cgo，可交叉编译），打包为 NSIS 每用户安装包 + 绿色 zip，进 GitHub Release 矩阵与 CI。

## 架构决策
- **一层原生壳、两个实现、一份契约**：把平台无关的类型（`Config`/`Handlers`/`SystemInfo`）与
  默认值（`DefaultConfig`、`fraction` 钳位）抽到 `internal/tray/native/types.go`
  （`//go:build tray && (darwin || windows)`），两端导出的 API 面逐一对齐，
  因此 `cmd/tunnelmesh-client-tray/main.go` 不需要按平台分叉，只分叉信号处理。
  对齐本身由 `scripts/tray_build_tag_test.go` 用源码解析断言（两端符号集必须相等）。
- **不用 systray 类库**：托盘的 `NSApplication`/消息循环必须只有一个所有者。第三方托盘库各自
  起线程泵消息，结果是菜单不响应与 webview 覆盖自己的窗口；macOS 侧已经手写，Windows 侧沿用
  同一决定，Win32 + WebView2 直接用 `golang.org/x/sys/windows`。
- **纯 Go 而非 cgo**：这是 Windows 半边的关键取舍。WebView2 的 COM 接口由
  `github.com/wailsapp/go-webview2` 用 `go-winloader` 动态加载（`WebView2Loader.dll` 内嵌），
  因此 `CGO_ENABLED=0 GOOS=windows go build -tags tray ./...` 成立，Linux/macOS 开发机与 CI 都
  不需要 Windows 主机或 C 工具链。
- **设置界面仍由托盘自己的 loopback HTTP 服务提供**：`go:embed` 的前端 + `127.0.0.1` 随机端口 +
  每次启动随机 secret 逐请求校验，两平台同一实现，Windows 不新增端点、不改鉴权。
- **WebView2 运行时缺失是降级而不是失败**：托盘照常启动，tooltip 追加提示，“打开主界面”改用系统
  默认浏览器打开同一个带 secret 的地址，关于页显示降级横幅。不内嵌微软运行时安装包（那是分发
  决策，且会引入一份需要独自跟踪更新的第三方二进制）。
- **互斥按 config 路径派生锁**：Windows 用 `LockFileEx(LOCKFILE_EXCLUSIVE_LOCK |
  LOCKFILE_FAIL_IMMEDIATELY)`，与 Unix `flock` 语义等价（进程退出或被杀由内核回收）。效果是
  Windows 上托盘与指向同一 `client.yaml` 的 `client run` 真正互斥，CLI 由此获得与 mac 一致的
  “已有实例则拒启”语义。
- **UI 偏好继续只写 `tray.json`**：`client.yaml` 要过 `config.Validate`，界面键不进配置模型。
- **进 CI 与发行矩阵，但不进交叉编译矩阵**：`build-release.sh` 永远不出现 `-tags tray`，托盘由
  专属作业产出、由 `merge-tray-dist.sh` 合并；Windows 作业能在 ubuntu runner 上完成，因为它只需
  要 Go 工具链 + `makensis`。

## 技术栈
- Go（`internal/tray`、`internal/tray/native`、`internal/client`、`cmd/tunnelmesh-client-tray`）。
- Win32 API（`golang.org/x/sys/windows`，`LazySystemDLL` + `NewCallback`）与
  `github.com/wailsapp/go-webview2`（MIT，WebView2/COM 绑定，传递依赖
  `github.com/jchv/go-winloader`）。
- `github.com/tc-hib/go-winres`（`tool` 指令引入）生成 exe 的图标 / 版本信息 / 应用清单 `.syso`。
- NSIS（`makensis`，Debian/Ubuntu 的 `nsis` 包）产出每用户安装包。
- Vue 3 + TypeScript + Element Plus + vue-i18n + Pinia（`web-tray/`），vitest 单测；本次零新增
  前端依赖。

## 规格引用
- `docs/superpowers/plans/2026-10-05-client-system-tray-macos.md`：托盘总体设计、共用配置与互斥
  约定、loopback + secret 的安全边界、四 tab 的功能定义。
- `docs/superpowers/plans/2026-10-06-tray-quick-panel.md`：快捷小窗的路由约定（`#/panel` 由
  `tray.PanelRoute` 与 `web-tray/src/panelRoute.ts` 两端钉死）。
- `docs/user-guide/client-tray.md`、`docs/deployment/macos-client-tray.md`、
  `docs/deployment/binary-release.md`。

## 全局约束
- 不改 `tunnelmesh-client` CLI 的命令面与外部行为；`internal/tray` 的逻辑、`client.yaml` /
  `tray.json` / `client.lock` 的结构与本地 API 均复用，不新增行为分支。
- 所有新的原生代码必须在 `//go:build tray && windows` 之后；Windows 侧**不得** `import "C"`，
  默认 `go build/test ./...`（`CGO_ENABLED=0`、无标签）不编译、不触达 Win32/WebView2。
- `cmd/tunnelmesh-client-tray` 必须同时保留“不支持平台的回退入口”，否则无标签构建会丢一个命令。
- 生成的 `.syso` 是构建输入，绝不入库（`.gitignore` + 守卫测试双重固定）。
- 图标一律由 `scripts/trayicon` 生成，不接受手工放图；两个平台的图标必须同源同几何。
- 日志与错误信息不得输出 Token；本地 API 不返回 Token 全文。

## 文件清单
新增：
- `internal/tray/native/types.go`：两端共享的类型与默认值。
- `internal/tray/native/winapi_windows.go`：x/sys 未提供的结构（`wndClassEx`、`notifyIconData`、
  `msg`、`minMaxInfo`、`monitorInfo`、`point`、`bitmapInfoHeader`）、`WM_*` 等常量与
  user32/kernel32/shell32/shcore/dwmapi 的 proc 表。
- `internal/tray/native/icon_windows.go`：`go:embed` 的 `TunnelMeshTray.ico` → `HICON`（解析
  ICO 目录，支持 BMP 与 PNG 条目，按 DPI 选帧并缓存句柄）。
- `internal/tray/native/webview_windows.go`：WebView2 宿主（`edge.NewChromium` + `Embed(hwnd)` +
  `Navigate`/`Resize`）、设置项裁剪、运行时探测。
- `internal/tray/native/tray_windows.go`：消息循环、`Shell_NotifyIconW`、`TrackPopupMenu`、
  主窗口、快捷小窗、`WM_CLOSE` 语义、单实例互斥、AppUserModelID、`OpenURL`。
- `internal/tray/native/autostart_windows.go`：`HKCU\...\Run` 登录项。
- `internal/tray/native/hostinfo_windows.go`：系统版本（注册表 `NT\CurrentVersion`）、架构、
  工作区、DWM 标题栏深浅色。
- `internal/tray/native/icons/TunnelMeshTray.ico`：被 embed 的通知区域图标（与 deploy 下那份逐
  字节一致）。
- `internal/client/runtime_lock_windows.go` + `runtime_lock_windows_test.go`：`LockFileEx` 互斥。
- `internal/tray/platform.go` + `platform_test.go`：`SettingsView.platform` 的来源。
- `cmd/tunnelmesh-client-tray/shutdown.go`、`signals_darwin.go`、`signals_windows.go`。
- `deploy/windows/{TunnelMeshClient.ico,TunnelMeshTray.ico,winres.json,installer.nsi}`。
- `deploy/windows/tray_bundle_test.go`。
- `scripts/package-windows-tray.sh`、`scripts/ci_workflow_test.go`。
- `web-tray/src/platform.ts`、`web-tray/src/tests/platform.spec.ts`。
- `docs/deployment/windows-client-tray.md`、本文件与对应 PR 记录。

修改：
- `internal/tray/native/native.go`（只留 darwin/cgo 实现，共享部分移入 `types.go`）、
  `internal/tray/{app.go,api.go,prefs.go,server.go,...}`（renderer 上报与 platform 字段）。
- `internal/client/runtime_lock_other.go`（约束收窄为 `!unix && !windows`）。
- `cmd/tunnelmesh-client-tray/main.go`、`main_unsupported.go`（两个平台 + 回退）。
- `scripts/{trayicon/main.go,generate-tray-icon.sh,merge-tray-dist.sh,merge_tray_dist_test.go,build-release.sh,tray_build_tag_test.go,release_workflow_test.go}`。
- `.gitignore`、`Makefile`、`go.mod`、`go.sum`、`deploy/README.md`、`docs/README.md`、
  `docs/deployment/binary-release.md`、`docs/user-guide/client-tray.md`、`README.md`、
  `README.zh-CN.md`、`.github/workflows/{ci.yml,release.yml}`、`web-tray/src/{api/types.ts,i18n/*,views/*,styles/tokens.css}`。

## 任务间接口
- `native` 对 `cmd` 的契约就是 `scripts/tray_build_tag_test.go` 里的 `shellAPISurface`，两平台
  必须逐个同名同形：`Run(Config) error`、`Stop()`、`ShowWindow()`、`HideWindow()`、
  `SetMinimizeToTray(bool)`、`SetQuickPanel(bool)`、`SetHandlers(Handlers)`、
  `DefaultConfig(url string) Config`、`OpenURL(string) error`、`System() SystemInfo`、
  `PreferredLanguage() string`、`RendererName() string`、`RendererDetail() string`、
  `NewAutostart() *Autostart`。登录项是两平台各自实现的 `Autostart` 类型，方法集相同：
  `Supported() bool`、`Enabled() (bool, error)`、`SetEnabled(bool) error`；macOS 的状态来自
  `LoginItemStatus`（能表达“系统拒绝注册”的原因），Windows 恒为支持。
- `Config` 新增消费项：`PanelURL`（快捷小窗地址，等于 `api.PanelURL()`）、
  `WebViewDataDir`（Windows 上设为 `<PrefsDir>\webview2`，macOS 忽略）。
- `internal/client`：`StartRuntime` 取锁失败返回 `ErrRuntimeAlreadyRunning`；Windows 实现与 Unix
  实现共享同一错误值，托盘与 CLI 的提示文案不变。
- `internal/tray`：`SettingsView.platform` ∈ {`macos`,`windows`}（由 `runtime.GOOS` 得出，不依赖
  原生层，因此无标签构建与单测同样能验证）；`SystemInfo.renderer` / `rendererDetail` 由
  `native.RendererName()` / `RendererDetail()` 填充。
- 前端按 `platform` 取词的键：`general.theme.hint`、`general.launchAtLogin.hint`、
  `general.launchAtLogin.failed`、`general.quickPanel.hint`，每键取值为
  `{macos, windows, other}` 三元组，未知平台回落 `other`；文案不得再硬编码“macOS/菜单栏”。
- 打包脚本对 CI 的契约：输出目录里必有 `SHA256SUMS` 与 `manifest.json`，且
  `manifest.json.assets[]` 每项带 `platform`/`archive`/`extension`/`installerLayout`。
  `merge-tray-dist.sh` 只认这两份清单文件，其余已发布文件一律复制。

## TDD 步骤
1. 红：扩展 `scripts/tray_build_tag_test.go`——两端符号集必须相等、`*_windows.go` 必须带 `tray`
   且不得 `import "C"`、不得提交 `.syso`。此时 Windows 文件尚不存在，断言失败。
2. 绿：抽 `types.go`，Windows 实现补齐 API 面。
3. 红：`internal/client/runtime_lock_windows_test.go`（`//go:build windows`）写“第二个句柄取锁
   必须失败、释放后可重取”，`runtime_lock_windows.go` 未实现时 `GOOS=windows go vet` 失败。
4. 红：`deploy/windows/tray_bundle_test.go` 解析 `.ico` 目录、`winres.json` 三层结构、
   `installer.nsi` 的每用户属性、打包脚本关键参数、`PANEL_ROUTE` 对齐、
   `build-release.sh` 不含 `-tags tray`。
5. 绿：`scripts/trayicon` 增加 `-ico` 输出与 `WINDOWS_ONLY=1` 生成路径，产出两个 `.ico`；
   `winres.json`、`installer.nsi`、`scripts/package-windows-tray.sh` 落位。
6. 红→绿：`internal/tray/platform_test.go` 与前端 `src/tests/platform.spec.ts`（平台词表分支、
   四 tab 与快捷小窗回归）。
7. 红→绿：`scripts/merge_tray_dist_test.go` 混合形状与 `--manifest-name`；
   `scripts/ci_workflow_test.go`；`scripts/release_workflow_test.go` 的 windows 作业与
   “release 作业必须同时需要两个托盘作业”。
8. 重构：合并两份托盘的构建清单到 `docs/deployment/windows-client-tray.md` 与
   `docs/user-guide/client-tray.md` 的对应小节，重跑 `scripts/gen_doc_index.py`。

## 验证命令
```bash
go test ./... -count=1
go test -race ./internal/tray ./internal/client
go vet ./...
gofmt -l internal cmd scripts
git diff --check
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags tray ./...
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -tags tray ./...
GOOS=windows go vet -tags tray ./...
CGO_ENABLED=1 go vet -tags tray ./...
cd web-tray && npx vitest run && npm run build
scripts/generate-tray-icon.sh            # 重跑后 .icns 与两个 .ico 字节不变
VERSION=vX.Y.Z ./scripts/package-macos-tray.sh
VERSION=vX.Y.Z ./scripts/package-windows-tray.sh
makensis -DVERSION=0.0.0-ci -DARCH=amd64 -DBUILD_DIR=<stage> \
  -DICON_FILE=deploy/windows/TunnelMeshClient.ico \
  -DOUTFILE=/tmp/setup.exe deploy/windows/installer.nsi   # 安装脚本冒烟编译
```

## 回滚注意事项
- 托盘是**新增产物**，不是既有路径：回滚一次发行只需把 GitHub Release 指向上一版
  （`gh release edit <prev> --latest`），已下载的安装包与配置不受影响。
- 代码层面回滚本特性 = 删除 `internal/tray/native/*_windows.go`、`autostart_windows.go`、
  `hostinfo_windows.go`、`icon_windows.go`、`winapi_windows.go`、`webview_windows.go`、
  `internal/client/runtime_lock_windows.go`、`scripts/package-windows-tray.sh`、
  `deploy/windows/` 下四个新文件与 `cmd` 的两个信号文件，并把 `types.go` 的类型放回
  `native.go`。默认构建、CLI 与 macOS 托盘的行为不依赖其中任何一项。
- 唯一影响既有文件的语义变化：`internal/client` 的锁在 Windows 上生效，因此 Windows 的
  `tunnelmesh-client run` 在已有同配置实例时会拒启（这正是“互斥”的要求）。若需要退回旧行为，
  删除 `runtime_lock_windows.go` 并把 `runtime_lock_other.go` 的约束改回 `!unix`，锁在 Windows 上
  退化为“无锁”，托盘仍受单实例 mutex 保护。
- Windows 注册表侧只有 `HKCU\...\Run` 一个值；卸载程序删除它，也可手工
  `reg delete "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" /v TunnelMeshClient /f`。
  配置目录 `%USERPROFILE%\.config\tunnelmesh\` 不被安装或卸载触碰。

## 假设与边界
- 本机没有 Windows 主机，也没有可运行的 `makensis`（Homebrew 无 `nsis` 公式，容器镜像源不可达）。
  因此自动门禁 = 交叉编译 + 守卫/单元测试 + 源码结构断言，安装脚本另用 NSIS 3.10 自带的
  `Contrib/Modern UI 2` 与 `Examples` 逐个核对了所引用的宏与语法；GUI 行为由
  `docs/deployment/windows-client-tray.md` 的真机冒烟清单兜底。
- 默认配置目录沿用 `%USERPROFILE%\.config\tunnelmesh\`，与 Linux/macOS 及既有文档一致。
- exe 名统一 `TunnelMeshClient.exe`（zip 与 setup 同名），`GOARCH` 只有 amd64/arm64，不出 386。
- 不做 Authenticode 签名与 SmartScreen 例外申请（文档记为发布后续项）；不内嵌 WebView2 运行库。
- 本次不改配置模型：`client.tunnels` 及其全部已支持字段（含 `allow_remote`）沿用现状。
