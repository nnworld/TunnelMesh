# Windows 托盘客户端打包

把 `cmd/tunnelmesh-client-tray` 交叉编译成 `TunnelMeshClient.exe`，产出绿色 `.zip` 与一个
NSIS 每用户安装包。使用者视角的功能说明见
[系统托盘 Client](../user-guide/client-tray.md)，macOS 侧的对应文档见
[macOS 托盘客户端打包](macos-client-tray.md)。

## 为什么和 macOS 是两条路径，又和跨平台矩阵是两条路径

托盘在两个系统上都是**同一套 Go 逻辑 + 一层原生壳**：`internal/tray`（配置读写、loopback API、
检测、统计）与 `web-tray/`（设置界面）完全共用，只有 `internal/tray/native` 按平台分叉。

| 路径 | 主机 | 工具链 | 产出 |
| --- | --- | --- | --- |
| `scripts/build-release.sh` | Linux | `CGO_ENABLED=0`，无构建标签 | 三个二进制的六个跨平台归档 |
| `scripts/package-macos-tray.sh` | macOS | cgo + Xcode 命令行工具 + `hdiutil` | 两个 `.dmg` |
| `scripts/package-windows-tray.sh` | Linux 或 macOS | `CGO_ENABLED=0` 交叉编译 + `makensis` | 两个 `.zip` 与一个 `setup.exe` |

Windows 这一行能落在 Linux 上，是因为它的原生壳是**纯 Go 的 Win32/WebView2 绑定**：
`golang.org/x/sys/windows` 加 `github.com/wailsapp/go-webview2`，没有 cgo，因此
`GOOS=windows GOARCH=arm64 go build -tags tray ./cmd/tunnelmesh-client-tray ./internal/tray/...`
在任何主机上都成立；`makensis` 本身也是交叉编译器，在 Linux 上写出的就是能在 Windows 运行的安装包。

命令里点名包而不是用 `./...`：`-tags tray` 会让 `internal/tray/webdist/embed.go` 生效，`./...`
又顺带编译 `internal/server`，两者的 `//go:embed` 模式在干净检出里都匹配不到文件，于是
`npm run build` 没跑过的树上编译错误会先于任何被检查的代码出现。托盘二进制不依赖管理后台，
所以打包与 CI 只需要 `web-tray` 那一份产物。

三条路径的分界由测试守住而不是由说明守住：

- `scripts/tray_build_tag_test.go` 断言 `internal/tray/native/*_windows.go` 必须带 `tray`
  标签**且不得** `import "C"`，两端导出的 API 面必须逐个对齐（否则 `main.go` 就得按平台分叉），
  并断言仓库里不提交 `.syso`；
- `deploy/windows/tray_bundle_test.go` 断言 `.ico` 目录合法、`winres.json` 的三层结构、
  安装脚本的每用户属性、打包脚本的关键参数与失败分支、`build-release.sh` 里不得出现 `-tags tray`；
- `scripts/release_workflow_test.go` 与 `scripts/ci_workflow_test.go` 解析两份 workflow，断言
  托盘由专属作业产出、`release` 作业只合并、CI 里真的编译过 `-tags tray`。

## 前置条件

- Go（版本见 `go.mod`）、Node.js 与 npm
- `zip`（macOS 与 ubuntu runner 都自带）
- `makensis`：`apt-get install -y nsis`（Debian/Ubuntu）或从
  [NSIS 上游](https://nsis.sourceforge.io/Download) 取任意平台的发行件
- 重新生成图标时才需要：`scripts/generate-tray-icon.sh`（纯 Go，见[图标](#图标与-exe-资源)）

不需要 Windows 主机，也不需要 Visual Studio 或 MinGW。

## 构建

前端产物是编译期嵌入的，必须先构建；脚本在缺少产物或产物相对 `web-tray/dist` 过期时直接拒绝，
而不是打出一个窗口空白的 exe：

```bash
cd web-tray && npm ci && npm run build   # 产物同步到 internal/tray/webdist/dist
cd ..
VERSION=v1.2.3 ./scripts/package-windows-tray.sh
```

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `VERSION` | `dev` | 否则必须匹配 `vMAJOR.MINOR.PATCH`（允许预发布后缀） |
| `ARCHES` | `amd64 arm64` | 空格分隔的 windows 架构，只接受这两个值 |
| `INSTALLER_ARCH` | `amd64` | `setup.exe` 内嵌的架构，必须是 `ARCHES` 之一 |
| `DIST_DIR` | `dist/<VERSION>/windows-tray` | 输出目录 |
| `MAKENSIS` | `makensis` | NSIS 编译器路径 |

版本号有两处会被消费，二者语义不同：`internal/build.Version` 保留完整 tag（`v1.2.3`），
`go-winres` 的资源版本必须是四段点分数字（`1.2.3.0`），预发布后缀与 `v` 由脚本剥掉；
安装包传给 NSIS 的 `DisplayVersion` 同样用去掉 `v` 的值。归档文件名保留完整 tag，与 macOS
的 `.dmg` 一致。

打包脚本在压缩之前**读回**它即将压缩的东西：`go version -m` 里的 `GOOS=windows`、
`GOARCH=<arch>`、`tags=tray`、`CGO_ENABLED=0`，以及 PE 头里的 `Subsystem` 必须等于 2
（`IMAGE_SUBSYSTEM_WINDOWS_GUI`）。子系统这一步刻意不靠命令行断言：`go version -m` 在出现
`-X` 之后就不再记录 `-ldflags`，而"某个 flag 静默失效"正是这里唯一会产生一个带控制台窗口的
托盘的原因。

## 产物布局

```
dist/<VERSION>/windows-tray/
├── TunnelMeshClient-<VERSION>-windows-amd64.zip
├── TunnelMeshClient-<VERSION>-windows-arm64.zip
├── TunnelMeshClient-<VERSION>-windows-amd64-setup.exe
├── SHA256SUMS
└── manifest.json
```

绿色归档解开即是一个目录，成员平铺（不是套一层文件夹，`Expand-Archive` 与资源管理器
「全部解压缩」对后者的处理不同）：

```
TunnelMeshClient.exe
README.txt      # 启动方式、配置目录、WebView2 依赖、未签名时的 SmartScreen 处理
LICENSE         # Apache-2.0 4(a)/4(d) 要求随分发附带
NOTICE
```

安装包是 per-user 的：`RequestExecutionLevel user`、`InstallDir $LOCALAPPDATA\Programs\TunnelMesh Client`，
写开始菜单快捷方式、可选桌面快捷方式、`HKCU` 的卸载登记，卸载时删除 `HKCU\...\Run` 里的启动值。
全程不提示 UAC，也不需要管理员。

`manifest.json` 的字段形状与 macOS 那份对齐：`version`、`commit`、`buildTime`、
`bundleIdentifier`（App User Model ID 复用同一个反向域名）、`executable`、`platforms[]`、
`assets[]`（每项带 `extension` 与 `installerLayout`）、`signingIdentity` 为空串、
`notarized: false`。绿色归档的 `installerLayout` 为 `false`、安装包为 `true`，因此
「这个发行里到底有没有安装包」是一个可被读出来的事实，而不是靠人记住的约定。

## 图标与 exe 资源

Windows 没有 `Info.plist` 这样的旁路元数据：图标、版本信息和应用清单必须**编进** exe。
它们由 `deploy/windows/winres.json` 声明，由 `go run github.com/tc-hib/go-winres make` 在打包
时生成 `rsrc_windows_<arch>.syso` 放进 `cmd/tunnelmesh-client-tray/`，构建结束后删除
（`.gitignore` 忽略 `cmd/tunnelmesh-client-tray/*.syso`；留在树里的 `.syso` 会被之后每一次
`go build` 静默链接，包括测试那一次）。

`winres.json` 的层级是 类型 → 名称 → **语言 ID** → 资源，语言层是手写时最容易漏的一层
（漏掉就在打包时报 `invalid language identifier`）。资源类型用 `RT_GROUP_ICON` 而不是
`RT_ICON`：后者被 go-winres 直接拒绝，而 group 才让 Windows 按 DPI 挑帧。

图标与 macOS 那份同源：`scripts/trayicon` 用同一组数字画出 app 图标（16/24/32/48/64/128/256）
与通知区域图标（16/24/32），`scripts/generate-tray-icon.sh` 一次同时产出 `.icns`、菜单栏
PNG 与两个 `.ico`，并把 `TunnelMeshTray.ico` 复制一份到 `internal/tray/native/icons/` 供
`go:embed` 使用（`go:embed` 不能越出自己的目录）。两个副本不允许漂移，
`deploy/windows/tray_bundle_test.go` 逐字节比对它们。

`.ico` 全部用 BMP/DIB 条目而非 PNG 条目：PNG 条目自 Vista 起才被支持，老一点的 shell 在通知
区域会什么都不画。每帧 32 bpp，因为品牌块是带 alpha 的圆角方块，24 bpp 会被合成到黑底上。

## 开机启动与运行时依赖

启动项写 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` 的 `TunnelMeshClient` 值，内容
是带引号的 exe 路径——不需要管理员，且换用户登录不会带出别人的隧道。`Enabled()` 比较的是路径：
把 app 移个位置之后它会被判为「未启用」，通用页的开关因此显示关闭，而 `SyncAutostart` 在下次
启动时按当前路径自愈重写。这与 macOS 用 `SMAppService` 按 bundle identifier 记账是同一个语义，
只是键不同。

WebView2 运行时（`WebView2Loader.dll` 已静态内嵌，发行件里**不需要**带这个 DLL）在 Windows 11
与近年的 Windows 10 上预装。缺失时托盘不崩：日志记录一次，通知区域 tooltip 追加「需要安装
Microsoft Edge WebView2 运行时」，「打开主界面」改为用系统默认浏览器打开同一个 loopback 地址
（带启动 secret），关于页显示降级横幅。运维因此总有一个能打开的设置界面，而不是一片空白。

## 进 GitHub Release

推送 `vMAJOR.MINOR.PATCH` 标签后，`.github/workflows/release.yml` 用四个作业产出**一个**
不可变 Release：

| 作业 | Runner | 做什么 |
| --- | --- | --- |
| `version` | ubuntu-latest | 解析并校验版本号，输出给其余作业 |
| `macos-tray` | macos-15 | 前端测试/构建 → `.dmg` 打包与校验 → artifact `macos-tray` |
| `windows-tray` | ubuntu-latest | `apt-get install nsis` → 前端测试/构建 → 本脚本打包与校验 → artifact `windows-tray` |
| `release` | ubuntu-latest | 管理后台前端 + embed 校验 → 下载两个托盘 artifact → `TRAY_DIST_DIR=tray-dist WINDOWS_TRAY_DIST_DIR=windows-tray-dist ./scripts/build-release.sh` → 校验 `2 .dmg + 1 .exe + 8 (tar.gz|zip)` 与三份 manifest → `gh release create` |

合并由 `scripts/merge-tray-dist.sh` 执行，两个托盘作业共用它：它校验来源目录自己的
`SHA256SUMS`（文件名相同不等于内容相同），把来源目录里**除** `SHA256SUMS`/`manifest.json`
外的已发布文件复制进 Release 目录，重算校验和**追加**到同一个 `SHA256SUMS`，并按
`--manifest-name` 把两份清单分别发布为 `manifest-tray.json` 与
`manifest-windows-tray.json`。所以运维侧仍然是一条 `sha256sum -c` 覆盖全部资产，跨平台
`manifest.json` 的 schema 不变。

`release` 作业里没有本脚本，也不允许有：它只合并别人编译好的东西。

## 未签名与 SmartScreen

`TunnelMeshClient.exe` 与 `setup.exe` 都没有 Authenticode 签名，也没有申请过信誉。首次下载的
Windows 会显示「Windows 已保护你的电脑」，需要「更多信息 → 仍要运行」。这是**分发**问题而不是
构建缺陷，处理方式只有两条：

- 发布带签名：用 Developer ID 等价的代码签名证书对 exe 与 setup.exe 分别签名
  （`signtool sign /fd sha256 /tr <tsa> ...`），然后重新上传资产。NSIS 脚本本身不需要改。
- 继续不签名：在 Release 说明里保留 `README.txt` 的 SmartScreen 段落，并要求先核对
  `SHA256SUMS`。

签名与公证一样，是**发布**这一步的动作，不由本脚本擅自假设。

## 回滚

发布物不可变，回滚就是让下载页指向上一版：

```bash
gh release list --limit 5
gh release edit <上一个版本> --draft=false --latest
gh release delete <有问题的版本> --cleanup-tag --yes   # 只在确认无人已下载时
```

已经装了托盘的机器不受影响：托盘只读自己的 `client.yaml`，Windows 侧没有 schema 或配置迁移，
卸载程序删除 exe、快捷方式与 `HKCU` 的两处登记，不动配置目录（与 macOS 拖走 .app 的语义一致）。

## 真机冒烟清单

自动门禁覆盖不到的只有 GUI 行为本身，因此下列项需要在真机（Windows 11 + 一台 Windows 10）
上执行一次，结果记进 PR 文档：

1. 安装/卸载：`setup.exe` 全程不提示 UAC；开始菜单项可用；卸载后 `HKCU\...\Run` 与
   `...\Uninstall\com.tunnelmesh.client-tray` 都不再存在。
2. 通知区域：图标是品牌块（深色/浅色任务栏各看一次）；tooltip 文本正确；三项菜单在中英文系统
   下分别是「打开主界面/打开官网首页/退出」与英文名。
3. 主窗口：初次打开约为工作区（排除任务栏）的 50%，居中，最小尺寸 720×480；主题跟随系统的
   深色标题栏；四个 tab 都能渲染。
4. 关闭语义：「关闭时最小化到托盘」开→点 × 只隐藏、进程常驻、菜单仍可再打开；关→点 × 退出且
   隧道停止。
5. 快捷小窗：开启后左键弹小窗、点别处即消失；关闭后左键弹菜单；右键始终弹菜单。
6. 输入：路由配置页 `Ctrl+V` 粘贴可用、首字母不被自动大写、中文输入法可输入。
7. Agent 下拉能拉到列表（新接口）、检测按钮逐项给出结论；服务地址错误时给的是逐项失败原因而不是
   空白。
8. 统计页约 2s 自动刷新，断开服务端后连通性列会变。
9. 互斥：托盘运行时再开 `tunnelmesh-client run -c <同一个 client.yaml>` 必须报「已有 TunnelMesh
   client 在运行」并退出；反之亦然；杀掉进程后锁由内核回收。
10. 开机启动开关写入/删除 `HKCU` 值，注销再登录后托盘自启。
11. 显示缩放 150%/200% 与双屏（主屏在左/在右）下窗口与托盘图标都正常。
12. 卸载 WebView2 运行时后：托盘仍启动、tooltip 提示、菜单项打开系统浏览器且能正常使用设置页。
