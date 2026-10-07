# 跨平台可执行文件打包

TunnelMesh 的发行物包含三个可执行文件：`tunnelmesh-server`、`tunnelmesh-agent`、`tunnelmesh-client`。发行构建默认使用 `CGO_ENABLED=0`，不把配置、Token、密码、DSN、证书、私钥、数据库和日志放进归档。

系统托盘客户端 `tunnelmesh-client-tray` 同样进 Release，但**不由这个脚本编译**，两个平台各有各
的原因：macOS 那半通过 cgo 链接 Cocoa 与 WebKit，只能在 macOS 主机上构建，由 `macos-tray` 作业
调用 [macOS 托盘客户端打包](macos-client-tray.md) 产出 `.dmg`；Windows 那半是纯 Go 的 Win32 与
WebView2 绑定，能从 Linux 交叉编译，但资源戳记与 NSIS 安装包属于独立的打包步骤，由
`windows-tray` 作业调用 [Windows 托盘客户端打包](windows-client-tray.md) 产出绿色 `.zip` 与
`setup.exe`。两份产物都由 `scripts/build-release.sh` 合并进同一个 Release 目录（见
[托盘产物的合并](#托盘产物的合并)）。

跨平台矩阵始终不出现 `-tags tray`：托盘的原生壳要么需要一台 Mac，要么需要一套发行打包流程，
把它们混进矩阵只会让六个归档的构建去承担第八个作业的失败。这条边界由
`scripts/tray_build_tag_test.go`、`deploy/macos/tray_bundle_test.go`、
`deploy/windows/tray_bundle_test.go` 与 `scripts/release_workflow_test.go` 共同守卫。

## GitHub Release

正式发行包维护在 GitHub Release：

```text
https://github.com/nnworld/TunnelMesh/releases
```

Release 由以下两种方式触发：

- 推送完整 `vMAJOR.MINOR.PATCH` 标签，例如 `v1.2.3`。
- 管理员手动执行 `release` workflow，并输入完整版本号。

版本号只在 `version` 作业里解析一次并作为 output 传给另外两个作业：标签推送与手动输入的取值
方式不同，两处各自解析就会出现“Release 标题是 v1.2.3、资产名是 v1.2.2”这种事故，而且非法版本号
应当在任何编译开始之前就失败。workflow 只生成不可变 Release，不会创建或移动 `v1`、`v1.2`
这类可变标签。

| 作业 | Runner | 职责 |
| --- | --- | --- |
| `version` | ubuntu-latest | 解析并校验 `^v[0-9]+\.[0-9]+\.[0-9]+$`，输出版本号 |
| `macos-tray` | macos-15 | `web-tray` 的 `npm ci` / `npm test` / `npm run build`；`-tags tray` + `CGO_ENABLED=1` 打包两个 darwin 架构的 `.dmg`；校验磁盘映像与 `SHA256SUMS`；上传 artifact `macos-tray` |
| `windows-tray` | ubuntu-latest | 装 `nsis`；同样的 `web-tray` 前端步骤；`CGO_ENABLED=0` 交叉编译两个 windows 架构 + `go-winres` 资源 + `makensis`；校验两个 `.zip`、`setup.exe`、`SHA256SUMS` 与 `manifest.json`；上传 artifact `windows-tray` |
| `release` | ubuntu-latest | `web` 前端测试/构建、embed 一致性检查、跨平台矩阵、下载并合并两份托盘 artifact、校验全部资产、`gh release create` |

`release` 作业不编译任何托盘（它只合并别人编译好的东西），两个托盘作业都不碰跨平台矩阵。
两个作业用同一份 `go.mod` 里的 Go 版本。Go 单测、race 与 vet 不在 `release` workflow 里执行，
按 [AGENTS.md](../../AGENTS.md) 属于发布前必须手工完成的验证；`ci` workflow 目前只跑前端与
MySQL 5.6 契约测试，通用 Go 构建作业是既有缺口。

## 构建矩阵

| 平台 | 架构 | 归档 |
| --- | --- | --- |
| Linux | amd64、arm64 | `.tar.gz` |
| macOS | amd64、arm64 | `.tar.gz` |
| Windows | amd64、arm64 | `.zip` |

托盘不参与上表的交叉编译，单独由 macOS runner 产出：

| 产物 | 架构 | 归档 | 内容 |
| --- | --- | --- | --- |
| `tunnelmesh-client-tray` | amd64、arm64 | `.dmg` | 安装窗口：`TunnelMesh Client.app` 与 `Applications` 别名并排 |

在仓库根目录执行：

```bash
VERSION=v0.1.0 ./scripts/build-release.sh
sha256sum -c dist/v0.1.0/SHA256SUMS
```

等价的 Make 入口：

```bash
make release VERSION=v0.1.0
```

归档命名固定为：

```text
tunnelmesh-vMAJOR.MINOR.PATCH-PLATFORM.ARCHIVE
```

例如 `tunnelmesh-v1.2.3-darwin-arm64.tar.gz` 和
`tunnelmesh-v1.2.3-windows-amd64.zip`。每个 Release 都包含：

- 六个平台归档；
- 两个 macOS 托盘磁盘映像，命名 `tunnelmesh-client-tray-vMAJOR.MINOR.PATCH-darwin-<arch>.dmg`；
- 两个 Windows 绿色归档与一个安装包，命名
  `TunnelMeshClient-vMAJOR.MINOR.PATCH-windows-<arch>.zip` 与
  `TunnelMeshClient-vMAJOR.MINOR.PATCH-windows-amd64-setup.exe`；
- `SHA256SUMS`，覆盖以上全部十一个资产；
- `manifest.json`；
- `manifest-tray.json` 与 `manifest-windows-tray.json`。

`manifest.json` 记录版本、主版本、Commit、UTC 构建时间、Schema 版本、三个二进制、六个平台和资产清单。其中 `schemaVersion` 不是打包脚本自己的常量，而是构建时从 `internal/storage/db.go` 的 `SchemaVersion` 读取，因此永远与二进制内的实际 Schema 版本一致；当前值为 16。

`manifest.json` 的 schema 是对外契约：`internal/server/download_api.go` 把它作为 `manifestUrl`
暴露给后台“发行管理”页，因此托盘**不**并进去，而是把每个打包脚本产出的清单原样发布为
`manifest-tray.json` / `manifest-windows-tray.json`——额外记录 bundle identifier、可执行文件名、
最低系统版本、签名身份与 `notarized`，`assets[]` 每项带 `installerLayout`：磁盘映像的安装器布局
由 Finder 写入，没有 window server 会话的 runner 写不出来，该字段说明这份映像到底是安装窗口还是
普通文件夹窗口；Windows 侧它是“这份资产是不是安装包”的事实（`.zip` 为 false、`setup.exe` 为
true）。两份清单字段形状对齐，是为了让同一个解析器读得懂两个平台的发行记录。

需要知道的现状：`download_api.go` 里的资产列表是六个跨平台归档的**固定命名**，它不解析
`manifest.json`，也还不包含托盘资产。管理员在后台看到的仍是六个归档，桌面使用者从 GitHub
Release 页面下载磁盘映像、绿色归档或安装包。把托盘接进下载接口是独立的后续决策（涉及后台 UI 与“平台”语义），
本次不做。

## 托盘产物的合并

`scripts/build-release.sh` 接受两个可选环境变量，各自指向一份托盘输出目录：`TRAY_DIST_DIR`
（`scripts/package-macos-tray.sh`）与 `WINDOWS_TRAY_DIST_DIR`（`scripts/package-windows-tray.sh`）：

```bash
# macOS 主机上打包 macOS 托盘；任意装了 nsis 的主机上打包 Windows 托盘
VERSION=v1.2.3 ./scripts/package-macos-tray.sh
VERSION=v1.2.3 ./scripts/package-windows-tray.sh
# 任意主机上打包跨平台矩阵并合并两份托盘
VERSION=v1.2.3 \
  TRAY_DIST_DIR=dist/v1.2.3/macos-tray \
  WINDOWS_TRAY_DIST_DIR=dist/v1.2.3/windows-tray \
  ./scripts/build-release.sh
```

等价的一行入口是 `make release-with-trays VERSION=v1.2.3`（需要 macOS 主机 + `makensis`）；
`make release-with-tray` 仍然只管 macOS 一份。

合并逻辑在 `scripts/merge-tray-dist.sh` 里，行为是固定的：

- 先校验来源目录自己的 `SHA256SUMS`。磁盘映像要经过 artifact 上传与下载，文件名相同不等于内容
  相同，这是唯一能拦住截断或改写的地方。
- 把来源目录里**除** `SHA256SUMS`/`manifest.json` **外的已发布文件**复制到 Release 目录，重算
  校验和并**追加**到同一个 `SHA256SUMS`，因此运维侧一条 `sha256sum -c` 覆盖全部资产。扩展名不是
  白名单也不是黑名单：两个平台发布形状不同（`.dmg` 对 `.zip` + `.exe`），而“清单以外的东西都要
  发布”这条规则对两边一致。以点开头的文件与 `.log`/`.txt`/`.json` 不会进 Release，构建暂存目录
  同理。
- 托盘的 `manifest.json` 按 `--manifest-name` 复制为 `manifest-tray.json` 或
  `manifest-windows-tray.json`，不覆盖跨平台契约，也互不覆盖。
- 来源不是打包脚本的输出（缺 `SHA256SUMS`、`manifest.json`，或除这两个文件之外什么都没有）、
  校验失败，或 Release 目录已有同名资产时立即非零退出，不留半成品：两个构建争用同一个资产名时，
  不能由复制顺序决定运维下载到哪一个。

`TRAY_DIST_DIR` 与 `WINDOWS_TRAY_DIST_DIR` 未设置时脚本行为与从前完全一致，本地手工打跨平台包
不受影响。两份握手都在矩阵**之前**校验，不完整时几秒内失败，而不是等十八次交叉编译跑完才发现
少了某个平台的托盘产物。
`scripts/merge_tray_dist_test.go` 用 fixture 直接执行该脚本，覆盖上述每条分支。

在 Release 目录内校验：

```bash
cd dist/v0.1.0
sha256sum -c SHA256SUMS
```

macOS 没有 `sha256sum` 时脚本会自动使用 `shasum -a 256`。单个平台也可以直接交叉编译：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o tunnelmesh-agent_linux_arm64 ./cmd/tunnelmesh-agent
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o tunnelmesh-client_darwin_arm64 ./cmd/tunnelmesh-client
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o tunnelmesh-server_windows_amd64.exe ./cmd/tunnelmesh-server
```

无论走 `build-release.sh` 还是手工交叉编译，`tunnelmesh-server` 都会在**编译时**嵌入
`internal/server/web_dist/` 的当前内容，因此必须先执行 `cd web && npm run build`
（该命令已内置同步到嵌入目录）。两种失败模式都要避免：干净 clone 的仓库里没有这个
目录（被 gitignore，不受版本控制），直接编译会以 embed 模式匹配失败报错；发布机上残留
旧 bundle 则更危险——编译成功，但嵌入的是上一版前端。`scripts/build-release.sh` 现在
会在编译前校验嵌入目录存在且与 `web/dist` 完全一致，不满足时以非零退出并提示执行
`cd web && npm run build`。交叉编译参数不影响嵌入：`CGO_ENABLED=0`、`GOOS`、
`GOARCH`、`-trimpath` 只影响目标平台与路径记录，`-ldflags='-s -w'` 只剥离符号表与
DWARF 调试信息，不触碰嵌入数据。

## 安装方式

- 三平台 × 三角色的一键安装（下载校验、生成配置、注册服务、升级与卸载）：
  `deploy/install/oneclick/install-{server,agent,client}.sh`（Linux/macOS）与
  `install-{server,agent,client}.ps1`（Windows），见[一键安装脚本](oneclick-install.md)。
- Linux：使用 [linux-install.sh](../../deploy/install/linux-install.sh)，由 systemd 管理 Server/Agent。
- macOS：使用 [macos-install.sh](../../deploy/install/macos-install.sh)，由 launchd 管理用户级服务；Server 建议监听 8080 并由反向代理接管 80/443。
- Windows：使用 [windows-install.ps1](../../deploy/install/windows-install.ps1)，通过 WinSW 注册 Windows Service；不要把 Token 写入脚本参数，放在受 ACL 保护的配置文件或环境变量中。

安装脚本只安装二进制和进程管理配置，不自动生成业务配置，也不会覆盖已有配置和数据。
一键安装脚本相反：它会渲染角色配置，覆盖前自动备份为 `<path>.bak-<UTC时间戳>`（最多保留最近 3 份），
升级时默认保留现有配置，只有显式 `--reconfigure` 才重新生成。

归档内的 `deploy/` 内容按平台裁剪：

| 平台 | 归档内的 `deploy/` 内容 |
| --- | --- |
| Linux | `deploy/install`（含 `oneclick/`）、`deploy/systemd`、`deploy/systemd-user` |
| macOS | `deploy/install`（含 `oneclick/`）、`deploy/macos` |
| Windows | `deploy/install`（含 `oneclick/`）、`deploy/windows` |

`deploy/install/oneclick/` 含三个角色入口、共享库与 `winsw-checksums.txt`；
`deploy/systemd-user/` 是 Linux 默认（user 模式）的单元模板，一键脚本从**已校验的归档**里取模板，
因此模板版本必然与所装二进制一致。所有归档都包含三个二进制、安装脚本、`README.md`、`docs/`、
`LICENSE` 和 `NOTICE`。

`*_test.go` 与 `testdata/` 不进归档：前者是需要 Go module 的仓库一致性检查，后者是测试 fixture
（`curl`/`systemctl`/`launchctl`/`loginctl`/`plutil` 桩与现场生成的归档），运维侧的归档里没有 `go.mod`。
`scripts/build-release.sh` 在打包前显式删除这两类内容。

`LICENSE` 与 `NOTICE` 必须随每个归档分发：项目采用 Apache License 2.0，其 4(a) 要求随作品附带
许可文本，4(d) 要求附带 NOTICE。`scripts/build-release.sh` 会在仓库根目录缺少这两个文件时直接
失败，而不是产出一个缺少许可声明的归档。

## 回滚

1. 在后台“发行管理”页或 GitHub Release 页面选择上一个版本。
2. 下载并校验上一个版本的 `SHA256SUMS`。
3. 停止当前进程，替换二进制和 service 模板，但保留配置、数据和密钥。
4. 启动服务并检查健康状态、日志、客户端连接和核心链路。
5. 如涉及数据库迁移，按 `docs/operations/schema-upgrades.md` 的版本说明执行兼容回滚；不能自动猜测反向 DDL。

## 私有仓库权限

若仓库为私有，只有具备仓库下载权限的账号或 CI 凭据可以访问 GitHub Release。管理员后台只展示下载 URL，不代理或缓存 GitHub 凭据。当前配置只支持 `github.com` 上的 `owner/name` 仓库；如需企业内网镜像，应另行实现独立的下载分发服务，不能把凭据写入 TunnelMesh 配置。
