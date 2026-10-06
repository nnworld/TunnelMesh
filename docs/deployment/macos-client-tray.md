# macOS 托盘客户端打包

把 `cmd/tunnelmesh-client-tray` 打成 `TunnelMesh Client.app`，装进一个 `.dmg` 磁盘映像。
使用者视角的功能说明见
[macOS 系统托盘 Client](../user-guide/client-tray.md)。

## 为什么单独一条构建路径

[跨平台可执行文件打包](binary-release.md)的矩阵用 `CGO_ENABLED=0` 从 Linux 交叉编译三个
二进制。托盘要通过 cgo 链接 Cocoa、WebKit 与 ServiceManagement，只能在装了 Xcode
命令行工具的 macOS 主机上构建。所以托盘**不由** `scripts/build-release.sh` 编译，而是由
`release` workflow 的 `macos-tray` 作业在 macOS runner 上调用本脚本，产出的 `.dmg` 再合并进
同一个 Release（见[进 GitHub Release](#进-github-release)）。两条构建路径分开有两个直接好处：
跨平台的十八次交叉编译永远不依赖一台 Mac，Mac 专属产物也不会假装自己可移植。

这个约束由 `scripts/tray_build_tag_test.go` 结构性守卫：任何含 cgo 的文件、任何 import
`internal/tray/native` 的文件、任何 `//go:embed` 设置界面产物的文件，都必须带 `tray` 构建
标签。因此默认的 `go build ./...` 与 `go test ./...` 在 Linux 上、在 `CGO_ENABLED=0` 下、
在没有前端产物的干净检出里都能跑通，完全不需要 WebKit 工具链。

“合并可以、编译不行”这条边界由两个测试分别守住：`deploy/macos/tray_bundle_test.go` 断言
`scripts/build-release.sh` 里不得出现托盘可执行名、`-tags tray`、`CGO_ENABLED=1` 或 `hdiutil`；
`scripts/release_workflow_test.go` 解析 `.github/workflows/release.yml`，断言只有 macOS 作业
调用本脚本，而 Linux 的 `release` 作业必须下载托盘 artifact 并以 `TRAY_DIST_DIR` 传给
`build-release.sh`。

## 前置条件

- macOS 13 及以上主机（登录项用的 `SMAppService` 从 13 开始提供）
- Xcode 命令行工具：`xcode-select --install`
- Go（版本见 `go.mod`）、Node.js 与 npm
- `codesign`、`plutil`、`ditto`、`hdiutil`（系统自带）
- 重新生成图标时才需要：`iconutil`、`SetFile`（随命令行工具提供）

## 构建

前端产物是编译期嵌入的，必须先构建；脚本在缺少产物或产物过期时会直接拒绝，而不是打出一个
窗口空白的 app：

```bash
cd web-tray && npm ci && npm run build   # 产物同步到 internal/tray/webdist/dist
cd ..
VERSION=v1.2.3 ./scripts/package-macos-tray.sh
```

脚本参数全部走环境变量：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `VERSION` | `dev` | 否则必须匹配 `vMAJOR.MINOR.PATCH`（允许预发布后缀） |
| `ARCHES` | `arm64 amd64` | 空格分隔的 darwin 架构 |
| `SIGN_IDENTITY` | `-` | codesign 身份，默认 ad-hoc |
| `DIST_DIR` | `dist/<VERSION>/macos-tray` | 输出目录 |
| `ICON_SOURCE` | `deploy/macos/TunnelMeshClient.icns` | 图标，可缺省 |
| `DMG_BACKGROUND` | 空 | 图标背后的 png；仓库不带背景，留空即无背景 |

`CFBundleShortVersionString` 用去掉 `v` 前缀的版本号（Apple 的约定是三段点分整数），而
二进制里的 `internal/build.Version` 保留完整 tag。

两个架构在同一台机器上一次产出：cgo 交叉编译 `darwin/amd64` 只需要 SDK，不需要第二台
Intel Mac，因此 CI 的 `macos-15`（arm64）runner 就能覆盖两种架构。

等价的 Make 入口（同样只在 macOS 主机上有效）：

```bash
make tray-web-build
make tray-release VERSION=v1.2.3
```

`make release` 不会调用它们：跨平台发行矩阵用 `CGO_ENABLED=0` 交叉编译，永远产不出托盘。
要把已经打好的磁盘映像并进跨平台产物，给 `build-release.sh` 传 `TRAY_DIST_DIR`：

```bash
VERSION=v1.2.3 TRAY_DIST_DIR=dist/v1.2.3/macos-tray ./scripts/build-release.sh
```

## 进 GitHub Release

推送 `vMAJOR.MINOR.PATCH` 标签（或手动执行 `release` workflow）后，`.github/workflows/release.yml`
用三个作业产出一个不可变 Release：

| 作业 | Runner | 做什么 |
| --- | --- | --- |
| `version` | ubuntu-latest | 解析并校验版本号，输出给另外两个作业 |
| `macos-tray` | macos-15 | `web-tray` 的 `npm ci`/`npm test`/`npm run build` → 本脚本打包两个架构 → 校验两个 `.dmg` 存在、`shasum -a 256 -c SHA256SUMS` 通过、`manifest.json` 可解析，并把 `installerLayout` 汇总进 job summary（缺失时 `::warning::`）→ 上传 artifact `macos-tray` |
| `release` | ubuntu-latest | `web` 前端测试/构建 + embed 校验 → 下载 artifact 到 `tray-dist/` → `TRAY_DIST_DIR=tray-dist ./scripts/build-release.sh` → 校验八个资产与两份 manifest → `gh release create` |

合并由 `scripts/merge-tray-dist.sh` 执行：先校验来源目录自己的 `SHA256SUMS`（磁盘映像要经过
artifact 上传下载，文件名相同不等于内容相同），再把 `.dmg` 复制进 Release 目录并把重算的校验和
**追加**到同一个 `SHA256SUMS`，托盘清单发布为 `manifest-tray.json`。因此运维侧一条
`sha256sum -c` 覆盖全部资产，跨平台 `manifest.json` 的 schema 保持不变。

`release` 作业里没有本脚本，也不允许有：Linux runner 上没有 WebKit 工具链，在那里编译托盘只会
以“另外三个二进制也失败了”的样子报错。这条边界由 `scripts/release_workflow_test.go` 与
`deploy/macos/tray_bundle_test.go` 守卫，`scripts/merge_tray_dist_test.go` 用 fixture 覆盖合并
脚本的每条失败分支。

## 产物布局

```
dist/<VERSION>/macos-tray/
├── tunnelmesh-client-tray-<VERSION>-darwin-arm64.dmg
├── tunnelmesh-client-tray-<VERSION>-darwin-amd64.dmg
├── SHA256SUMS
└── manifest.json
```

每个磁盘映像挂载后是一个装着 app 与 `Applications` 别名的卷（卷名 `TunnelMesh Client`），
安装就是把 app 拖过去：

```
TunnelMesh Client/
├── TunnelMesh Client.app
├── Applications -> /Applications
└── .VolumeIcon.icns                    # 卷自身的图标，Finder 读取该固定文件名
```

挂载时弹出的窗口也是产物的一部分：640×420 的图标视图、96px 图标、app 落在
`(170, 210)`、`Applications` 落在 `(470, 210)`，窗口在屏幕上居中。这些状态由 Finder 存在
卷根的 `.DS_Store` 里，`hdiutil` 写不出来，所以影像是**两段**产出的：

```
hdiutil create -format UDRW -fs HFS+ -size <内容+空余>   # 可读写中间映像
  → hdiutil attach -mountpoint <临时目录>                # 私有挂载点，不与同名卷抢
  → osascript deploy/macos/tray-dmg-layout.applescript   # 驱动 Finder 写布局
  → sync && hdiutil detach
  → hdiutil convert -format UDZO -imagekey zlib-level=9  # 即将发布的只读映像
```

窗口尺寸与图标坐标只有 `tray-dmg-layout.applescript` 一个来源，不在 shell 里重复一份。
读写映像必须预留空余，因为 `.DS_Store` 是 Finder 在拷贝之后才写的；预留的空白在转成
UDZO 时会被压缩掉，实测体积不升反降（`v0.0.0-review` 的 arm64 映像 5.1 MB → 4.5 MB）。

`DMG_BACKGROUND` 指向的 png 会被放进卷内的 `.background/background.png`，并在挂载后用
`chflags hidden` 打上隐藏标记——仅有前导点不会让 Finder 的图标视图忽略它。背景别名按
卷内相对路径记录，因此在别人机器上挂载同一映像时仍然解析得到；别名同时留有挂载源（构建主机
上那个中间映像文件）的路径作为解析提示。默认不带背景，因此发行映像的 `.DS_Store` 里只有窗口
边界、图标视图选项与两个图标坐标，没有构建主机路径。

app bundle 的内容：

```
TunnelMesh Client.app/Contents/
├── Info.plist                          # 由 deploy/macos/TunnelMeshClient-Info.plist 渲染
├── PkgInfo
├── MacOS/tunnelmesh-client-tray        # -tags tray，CGO_ENABLED=1
└── Resources/
    ├── AppIcon.icns                    # 来自 deploy/macos/TunnelMeshClient.icns
    ├── LICENSE                         # Apache-2.0 4(a)/4(d) 要求随分发附带
    └── NOTICE
```

## 应用图标

`deploy/macos/TunnelMeshClient.icns` 是仓库里的**生成物**，不是设计稿导出件：仓库没有可评审
的二进制源文件，也没有设计工具链，所以图标由 `scripts/trayicon` 用纯 Go 画出来，形状就是一
组数字，改品牌变成改代码。

```bash
scripts/generate-tray-icon.sh                 # 默认写 deploy/macos/TunnelMeshClient.icns
PREVIEW=/tmp/icons scripts/generate-tray-icon.sh   # 同时保留 1024px 主图与 .iconset
```

`iconutil` 负责写 `.icns` 里按尺寸命名的表示（`ic05`/`ic07`/`ic10` …），因此重新生成必须走
它而不是自己拼字节。`deploy/macos/tray_bundle_test.go` 会解析已提交的 `.icns`，断言 16 / 128
/ 512 / 1024 四档尺寸都在——只带大图会在 Finder 列表视图里糊，只带小图在 Cover Flow 里碎。

图标标记是「一个枢纽连三个节点」的拓扑，即 Client/Agent 经 Server 互联的形状；刻意没用闭合
三角，因为那会撞上一个友商商标。配色取 GitHub 社交卡片已在用的蓝。

卷图标（`.VolumeIcon.icns`）必须在 **Finder 布局之后**再拷进挂载点：`update volume` 会“消费”
已经存在的该文件——图标被应用、文件被删除，而被应用的那个图标又活不过 `hdiutil convert`。
实测：布局前拷入的发行映像挂载后是通用磁盘图标，布局后拷入并 `SetFile -a C` 才是品牌图标。

`manifest.json` 记录版本、commit、构建时间、bundle identifier、最低系统版本、签名身份，
以及 `notarized: false`；`assets[]` 里每项带 `extension: "dmg"`、`volumeName` 与
`installerLayout`。进 Release 后它被发布为 `manifest-tray.json`。

最终映像由 `hdiutil convert` 产出（`-format UDZO`，zlib 压缩只读），并在写入后立即
`hdiutil verify`：要校验的是**即将发布的映像**，而不是早已校验过的 bundle——被截断的映像能
通过之前所有检查，只在运维机器上挂载失败。`hdiutil` 在共享 runner 上偶发 `Resource busy`，
create/attach/convert 各重试三次，最后一次不再吞掉输出，失败必须响。

布局这一步是**尽力而为**，不是硬要求：它需要一个可被脚本驱动的 Finder，而没有 window server
会话的 runner 提供不了。这种情况下脚本重试三次、将警告打到 stderr、继续产出映像，并把该资产
记为 `installerLayout: false`——它仍然拖拽即装，只是挂载后是一个普通文件夹窗口。清单里必须
留下这个记录，否则一份没有安装器布局的产物会在发布说明里被说成有。`release` workflow 的
`macos-tray` 作业读取该字段，把结果写进 job summary 并在缺失时打 `::warning::`。

把 bundle 放进卷用 `ditto` 而不是 `cp -R`：签名 bundle 带的扩展属性与资源叉会被普通复制静默
丢掉，从而让签名失效。

## Info.plist 模板

`deploy/macos/TunnelMeshClient-Info.plist`，唯一占位符是 `__VERSION__`。三个键是硬要求，
删掉任何一个都是静默故障：

| 键 | 缺失后果 |
| --- | --- |
| `LSUIElement` | app 出现在 Dock 里，每次开窗都抢焦点 |
| `NSAppTransportSecurity → NSAllowsLocalNetworking` | ATS 拦掉 127.0.0.1 的 http 首屏，窗口一片空白且没有任何报错 |
| `LSMinimumSystemVersion` = `13.0` | 低于 13 的系统上登录项在运行时才抛 `Operation not permitted`，而不是安装时被拒 |

`deploy/macos/tray_bundle_test.go` 守卫这些键，同时守卫另外三件事：模板与打包脚本的
bundle id / 可执行文件名 / 最低系统版本必须一致（脚本里那份只能从 plist 读出来，不许再写第二处
字面量，且必须既设 `MACOSX_DEPLOYMENT_TARGET`/`-mmacosx-version-min` 又用 `otool` 读回
`LC_BUILD_VERSION` 校验，见下节）；打包脚本必须产出 `.dmg`（`hdiutil create`、
`-format UDZO`、`ln -s /Applications`、清单里的 `extension: "dmg"`）且不得再出现 zip 相关参数；
`scripts/build-release.sh` 里不得出现托盘二进制、`-tags tray`、`CGO_ENABLED=1` 或 `hdiutil`。

安装器布局另有两条守卫：打包脚本必须走完 create → attach → 布局 → detach → convert
（`-format UDRW`、`-mountpoint`、`hdiutil detach`、`hdiutil convert`、`-imagekey zlib-level=9`、
`chflags hidden`），并且必须在布局失败时既留下警告又把 `installerLayout` 写进清单——静默
降级等于撒谎；`tray-dmg-layout.applescript` 必须真的驱动 Finder（`icon view`、`not arranged`、
两个 `set position of item ...`、`update ... without registering applications`、
`close installerWindow`），且不得写死挂载点或卷名。

bundle identifier 是 `com.tunnelmesh.client-tray`，沿用 launchd 模板的
`com.tunnelmesh.<role>` 约定。它不能随意改：`SMAppService` 以 bundle identifier 记录
登录项，改一次就把所有已装用户的“开机启动”变成孤儿记录。

## 最低系统版本

`LSMinimumSystemVersion` 只是声明，真正决定能不能启动的是 Mach-O 里的
`LC_BUILD_VERSION.minos`。托盘走 cgo，链接由 clang 完成，而 clang 在没人指定目标版本时按
**构建主机**的 macOS 版本写 `minos`：CI 的托盘 job 跑在 `macos-15`，于是 v1.3.1 的 `.dmg` 里
记的是 `minos 15.0`，在 macOS 14.6 上打开直接报“该应用程序要求 macOS 15.0 或更高版本”，
而同一份包里的 `Info.plist` 和 `manifest.json` 都还写着 13.0 —— 三处互相矛盾，只有二进制说话。

`scripts/package-macos-tray.sh` 因此做两件事：

1. 用 `plutil -extract LSMinimumSystemVersion raw` 从 plist 读出下限，作为唯一来源；编译时同时设
   `MACOSX_DEPLOYMENT_TARGET` 与 `CGO_CFLAGS`/`CGO_LDFLAGS` 的 `-mmacosx-version-min`。只设一半会
   留下 `ld: warning: object file ... was built for newer 'macOS' version` 这类警告，另一半仍按宿主
   记录。
2. 链接完立刻 `otool -l` 读回 `minos` 并与 plist 比对，不一致就让打包失败。声明和产物对不上时，
   报错比发一个装不上的包便宜。

runner 之后升到更新的 macOS 也不会再把产物变成“只有同等或更高系统能用”。检查一个已经下载下来的
映像：

```bash
hdiutil attach -nobrowse -readonly tunnelmesh-client-tray-<version>-darwin-arm64.dmg
otool -l "/Volumes/TunnelMesh Client/TunnelMesh Client.app/Contents/MacOS/tunnelmesh-client-tray" \
  | grep -A4 LC_BUILD_VERSION
hdiutil detach "/Volumes/TunnelMesh Client"
```

`minos` 应当是 `13.0`。v1.3.1 的托盘映像记录的是 `15.0`，在 macOS 14 上不可用；修复只保证**之后**
发行的包正确，已经发出去的 v1.3.1 要重新发版才能替换。

## 签名与分发

默认 ad-hoc 签名（`codesign -s -`）。实测结论：

- 本机构建、本机运行没有问题，`codesign --verify` 通过。
- 登录项**可以**注册成功。系统按 bundle 的 cdhash 记录它，因此移动 `.app` 或重新签名会让
  已有注册失效；托盘每次启动都会用偏好文件里的意图重新对齐一次，界面不会撒谎。
- 从网络**下载**的 ad-hoc 副本会被 Gatekeeper 拦下，需要用户显式放行。

`SIGN_IDENTITY` 不是 `-` 时，磁盘映像本身也会用同一身份签名——那才是 `notarytool` 提交、
`stapler` 装订的对象。ad-hoc 模式下映像不签名：里面的 app 已经是 ad-hoc 签名，再给容器加一个
ad-hoc 签名不会多出任何可校验的东西。

要正式分发必须用 Developer ID 签名并公证，这属于后续的发布工作，本次不做：

```bash
SIGN_IDENTITY="Developer ID Application: Example Inc (TEAMID)" \
VERSION=v1.2.3 ./scripts/package-macos-tray.sh
# 之后对每个 .dmg 自行提交与装订：
xcrun notarytool submit dist/v1.2.3/macos-tray/tunnelmesh-client-tray-v1.2.3-darwin-arm64.dmg \
  --apple-id <apple-id> --team-id <team-id> --password <app-specific-password> --wait
xcrun stapler staple dist/v1.2.3/macos-tray/tunnelmesh-client-tray-v1.2.3-darwin-arm64.dmg
# 公证与装订会改变映像字节，因此之后必须重算 SHA256SUMS，并把 manifest-tray.json 的
# notarized 置为 true，再交给 merge-tray-dist.sh 合并
```

脚本会在 ad-hoc 模式下把这些限制打印到 stderr，避免发布者以为自己拿到了可分发的产物。
CI 里没有 Developer ID 证书与凭据，因此 workflow 产出的是 ad-hoc 映像：它能在本机运行，
但下载来的副本会被 Gatekeeper 拦一次。

## 安装与卸载

双击 `.dmg`，弹出的窗口里 app 与 `Applications` 别名并排，把前者拖到后者即完成安装；
或命令行安装：

```bash
hdiutil attach tunnelmesh-client-tray-<VERSION>-darwin-arm64.dmg
ditto "/Volumes/TunnelMesh Client/TunnelMesh Client.app" "/Applications/TunnelMesh Client.app"
hdiutil detach "/Volumes/TunnelMesh Client"
xattr -d com.apple.quarantine "/Applications/TunnelMesh Client.app"   # 仅下载来的副本需要
open "/Applications/TunnelMesh Client.app"
```

用 `ditto` 而不是 `cp -R`，理由与打包时相同：签名 bundle 的扩展属性与资源叉不能被静默丢掉。

卸载要先注销登录项，否则系统里会留下一条指向已删除路径的记录：先在**通用**页关掉
“开机启动”，再退出托盘，最后删除 app。已经删掉 app 的情况下，可在
**系统设置 → 通用 → 登录项**里手工移除 `TunnelMesh Client`。

`~/.config/tunnelmesh/` 下的配置、偏好与日志不随 app 删除，需要的话自行清理。

## 与命令行 Client 的共存

托盘与 `tunnelmesh-client run` 共用 `client.yaml`，并按配置路径派生的 `client.lock`
互斥。已经用 [launchd](macos-launchd.md) 或
[一键安装脚本](oneclick-install.md)把 client 装成服务的机器上，安装托盘之前要先停掉那个
服务，否则托盘会报“已有 TunnelMesh client 在运行”并且不承载隧道：

```bash
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.tunnelmesh.client.plist"
```

反过来，用托盘就不需要 launchd 单元：登录项由 `SMAppService` 管理，隧道在托盘进程内。

## 验证

```bash
# 默认构建路径：无 cgo、无前端产物依赖
go build ./... && go vet ./...
go test ./scripts/ ./deploy/... ./internal/tray/... -count=1

# 托盘构建路径：需要前端产物
cd web-tray && npm test -- --run && npm run build && cd ..
CGO_ENABLED=1 go vet -tags tray ./...

# 打包（macOS 主机）
ARCHES=arm64 VERSION=v0.0.0-local ./scripts/package-macos-tray.sh

# 发行路径：合并与 workflow 结构
go test ./scripts/ ./deploy/macos/ -count=1

# 端到端（macOS 主机，几分钟；产出八个资产与一份统一的 SHA256SUMS）
VERSION=v0.0.0-local ./scripts/package-macos-tray.sh
VERSION=v0.0.0-local TRAY_DIST_DIR=dist/v0.0.0-local/macos-tray ./scripts/build-release.sh
(cd dist/v0.0.0-local && shasum -a 256 -c SHA256SUMS)
```

打包后的手工冒烟：挂载 `.dmg` 确认卷里有 app 与 `Applications` 别名，
`codesign --verify --verbose=2` 通过；菜单栏三项都点一次；窗口约半屏；“关闭时最小化”开与关
两种情况下关窗行为不同；“开机启动”开关能在系统设置里看到对应变化；四个 tab 都能加载；
在路由配置的两个文本框里分别按 ⌘V 与 ⌃V，都能粘贴（早期版本没有安装应用主菜单，⌘V 无效）。

图标同样可以机器核对，不用肉眼判断 Finder 画了什么：`NSWorkspace` 解析到的就是 Finder 用的
那一份图标，把它画成 png 与源 `.icns` 比对即可。

```bash
image=dist/v0.0.0-local/macos-tray/tunnelmesh-client-tray-v0.0.0-local-darwin-arm64.dmg
hdiutil attach -readonly -noverify -noautoopen "$image"
plutil -extract CFBundleIconFile raw "/Volumes/TunnelMesh Client/TunnelMesh Client.app/Contents/Info.plist"
# AppIcon
ls -l "/Volumes/TunnelMesh Client/TunnelMesh Client.app/Contents/Resources/AppIcon.icns" \
      "/Volumes/TunnelMesh Client/.VolumeIcon.icns"
hdiutil detach "/Volumes/TunnelMesh Client" -quiet
```

`AppIcon.icns` 缺失而 `CFBundleIconFile` 仍写着 `AppIcon` 时，Finder 会缓存一个空图标，比
不带这个键更糟；打包脚本因此在没有图标时删掉该键（`ICON_SOURCE` 指错时的行为）。

安装器布局不必肉眼看，读回 Finder 记录的状态即可核对：

```bash
image=dist/v0.0.0-local/macos-tray/tunnelmesh-client-tray-v0.0.0-local-darwin-arm64.dmg
hdiutil attach -readonly -noverify -noautoopen "$image"
osascript -e 'tell application "Finder"' \
  -e 'set r to (POSIX file "/Volumes/TunnelMesh Client") as alias' -e 'open r' \
  -e 'set w to container window of r' \
  -e 'set out to ((position of item "TunnelMesh Client.app" of w) as text) & "|" & ((position of item "Applications" of w) as text) & "|" & (current view of w as text)' \
  -e 'close w' -e 'return out' -e 'end tell'
hdiutil detach "/Volumes/TunnelMesh Client" -quiet
# 170210|470210|icon view
```

两条降级与增强路径同样有实测：`DMG_BACKGROUND=<png>` 产出的映像里 `.background` 带
`hidden` 标记、`.DS_Store` 里有 `backgroundImageAlias`；把布局脚本换成必然失败的版本后，
构建仍然成功，stderr 出现三次重试与一条警告，清单里该资产是 `installerLayout: false`。

## 回滚

app bundle 是自包含的，回滚就是用上一版 `.dmg` 里的 app 覆盖 `/Applications` 里的目录，
5 分钟内可完成。Release 侧的回滚同样只是选上一个标签：资产不可变，`.dmg` 与 `.tar.gz` 都在
同一份 `SHA256SUMS` 里。
`client.yaml` 的格式没有变化，旧版托盘和命令行 Client 都能读新版写出的配置，因此不需要
配置回滚。唯一的状态残留是登录项：换 bundle 后重新打开一次“开机启动”即可。
