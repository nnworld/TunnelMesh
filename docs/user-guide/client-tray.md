# 系统托盘 Client（macOS / Windows）

常驻在菜单栏（macOS）或通知区域（Windows）的 TunnelMesh Client。它在自己的进程里承载隧道，
能力等价于 `tunnelmesh-client run`，并把 `client.yaml` 的编辑、运行观测和配置检测做成了
图形界面。

> **平台范围**：macOS 13 及以上、Windows 10/11。两个平台共用同一套托盘逻辑与同一个设置界面，
> 只有承载网页的原生壳不同（macOS 用 WKWebView，Windows 用 Microsoft Edge WebView2）。
> Linux 的托盘尚未实现，该平台继续使用[命令行 Client](client.md)
> 或 [systemd 服务](../deployment/linux-systemd.md)。

下文按主题组织，**只在两平台行为不同的地方**标注 macOS / Windows 差异；打包侧的完整差异见
[macOS 托盘客户端打包](../deployment/macos-client-tray.md) 与
[Windows 托盘客户端打包](../deployment/windows-client-tray.md)。

## 1. 与命令行 Client 的关系

托盘是新增的第四个程序 `tunnelmesh-client-tray`，**不替换也不修改**原来的
`tunnelmesh-client`：`check-config`、`print-config`、`run`、`publish`、`proxy tcp` 等子命令
和它们的参数、退出码全部保持原样。

两者的关系是三条：

| 关系 | 说明 |
| --- | --- |
| 共用配置 | 都读写同一个 `~/.config/tunnelmesh/client.yaml`。在托盘里改完，命令行 `print-config` 立刻能看到；反之亦然。 |
| 互斥运行 | 同一份配置同时只能有一个 Client 在跑。互斥由配置目录下的 `client.lock` 保证——macOS/Unix 上是 `flock` advisory 锁，Windows 上是 `LockFileEx` 独占锁——进程退出或被杀掉时都由内核自动释放。 |
| 能力等价 | 托盘进程内承载的隧道与 `run` 完全一致，包括 TCP、UDP、HTTP、SOCKS5、HTTP 代理与连接池。 |
| 原生壳 | macOS 是 `.app` bundle 里的 WKWebView；Windows 是 `TunnelMeshClient.exe` 里的 WebView2，GUI 子系统、无控制台窗口。 |

第二个启动方会拿到明确的错误，而不是静默失败：

- 命令行：`client: another TunnelMesh client is already running with this configuration`，退出码非 0。
- 托盘：统计页显示“已有实例在运行”，隧道不启动，界面其余部分仍可用来查看和编辑配置。

锁按**配置文件路径**派生，所以指向不同 `client.yaml` 的两个 Client 互不影响，可以并行运行。

## 2. 安装与首次启动

打包、签名与从源码构建见 [macOS 托盘客户端打包](../deployment/macos-client-tray.md) 与
[Windows 托盘客户端打包](../deployment/windows-client-tray.md)。Release 页面上每个平台都有
两种形态：**绿色包**（`.dmg` / `.zip`）和**安装包**（`.dmg` 本身就是拖拽安装；Windows 另有
`TunnelMeshClient-<版本>-windows-amd64-setup.exe`）。

**macOS**：双击 `.dmg`，会弹出装着两个图标的安装窗口，把左边的 `TunnelMesh Client` 拖到右边
的 `Applications` 就装好了，之后从启动台或 `/Applications` 打开它。映像里只有 app 与这个别名，
没有安装器进程，也没有后台守护。从浏览器下载的构建默认是 ad-hoc 签名，Gatekeeper 会拦一次：在
**系统设置 → 隐私与安全性**里点“仍要打开”，或执行
`xattr -d com.apple.quarantine "/Applications/TunnelMesh Client.app"`。

**Windows**：`setup.exe` 是**每用户**安装——全程不提示 UAC、不需要管理员，装进
`%LOCALAPPDATA%\Programs\TunnelMesh Client`，写开始菜单项与“应用和功能”里的卸载登记。
绿色用法是解开 `TunnelMeshClient-<版本>-windows-<架构>.zip`，双击里面的
`TunnelMeshClient.exe`：它是 GUI 子系统程序，不会留一个控制台窗口。未做 Authenticode 签名时
SmartScreen 会显示“Windows 已保护你的电脑”，点“更多信息 → 仍要打开”即可，建议先按
`SHA256SUMS` 核对下载。

**Windows 还需要 Microsoft Edge WebView2 运行时**（Windows 11 与近年的 Windows 10 已预装）。
缺它时托盘不会崩：通知区域 tooltip 会提示需要安装该运行时，“打开主界面”改为用系统默认浏览器
打开同一个本地地址，关于页显示降级说明——设置界面始终打得开。运行时可从
[微软官方页面](https://developer.microsoft.com/microsoft-edge/webview2/) 安装，本仓库不代它打包。

首次启动后菜单栏 / 通知区域出现品牌图标（与 `TunnelMesh Client.app` / `TunnelMeshClient.exe`
同一枚标记；macOS 的菜单栏版本是单色着色版），配置目录里还没有 `client.yaml`，因此隧道不会
自动起来。按下一节填完路由配置并保存即可。

## 3. 托盘菜单与主窗口

点击菜单栏 / 通知区域图标（下表左键那一列）：

| 菜单项 | 行为 |
| --- | --- |
| 打开主界面 | 显示并前置设置窗口 |
| 打开官网首页 | 用系统浏览器打开 <https://github.com/nnworld/TunnelMesh> |
| 退出 | 停止隧道、释放锁、隐藏窗口并结束进程 |

**左键还是菜单**，因为“退出”必须是永远点得到的出口。若在**通用**页开启了「任务栏快捷小窗」，
左键改为弹出小窗，菜单仍然由**右键**打开（macOS 上也可以用 ⌃ 点击）；关闭该设置后左键恢复为菜单。

快捷小窗是同一套界面的紧凑视图，用来一眼回答“在不在跑、有没有流量”：

| 区块 | 内容 |
| --- | --- |
| 状态 | 运行中 / 已停止 / 被其它实例占用（锁） |
| 汇总 | 服务端连接状态（有会话时显示「已连接」，否则显示健康检查结论）、运行时长、监听中/隧道数、活跃流、已接收字节、重连次数 |
| 隧道 | 每条隧道的名称、协议、监听地址、状态；失败时直接显示最近错误 |
| 操作 | 打开主界面、启动或停止客户端、退出 |

小窗每 2 秒读取一次托盘的本地接口，与**统计**页同源，不会出现两处数字对不上的情况。收起
小窗（点别处或再点一次图标）不影响隧道；托盘进程退出时小窗一并关闭。

主窗口默认约为屏幕可视区域的 50%，居中显示，最小 720×480，最大不超过屏幕的 90%。两平台对
“可视区域”的取法一致：macOS 用 `visibleFrame`（扣掉菜单栏与 Dock），Windows 用
`SPI_GETWORKAREA`（扣掉任务栏），因此 150%/200% 缩放下窗口不会顶到任务栏后面。

**macOS** 的文本框支持标准编辑快捷键：⌘X / ⌘C / ⌘V / ⌘Z / ⇧⌘Z / A，以及 ⌘Q（等同菜单里的
“退出”）。考虑到 Windows 习惯，⌃X / ⌃C / ⌃V 也被映射到同一组动作；⌃A 与 ⌃E 保持 Cocoa
原有的“行首 / 行尾”语义，不会被改写成全选。这两处映射都是必要的修补：accessory 应用默认没有
主菜单，而 AppKit 是拿**主菜单**去匹配 ⌘ 组合键的，没有主菜单时 WKWebView 里的输入框就收不到
粘贴；“首字母自动大写”也是 macOS 的文本辅助功能，不是界面缺陷（见
[排障](#10-排障)）。

**Windows** 不需要这些修补：WebView2 就是 Microsoft Edge，⌘/Ctrl 系快捷键与自动大写都由内核
自己处理，`Ctrl+V` 粘贴与中文输入法直接可用。托盘同时显式关掉了状态栏、右键菜单、表单自动
填充与密码自动保存（设置界面里唯一的敏感字段就是 Token），并且只在
`TUNNELMESH_TRAY_DEVTOOLS=1` 时才开放开发者工具。

窗口关闭按钮的行为由**通用**页的“关闭时最小化到系统托盘”决定：开启（默认）时关闭窗口只是
隐藏，隧道继续跑；关闭时点窗口等于点菜单里的“退出”。

界面语言与外观主题都支持“跟随系统”，并且是默认值。

## 4. 通用

| 设置项 | 取值 | 默认 | 说明 |
| --- | --- | --- | --- |
| 界面语言 | 跟随系统 / 中文 / 英文 | 跟随系统 | 跟随系统时不写入偏好文件，由 WebView 的 `navigator.language` 决定 |
| 外观主题 | 浅色 / 深色 / 跟随系统 | 跟随系统 | 深色使用 Element Plus 的 dark css-vars，跟随系统时响应 `prefers-color-scheme` |
| 配置目录 | 任意路径，支持 `~` | `~/.config/tunnelmesh/` | 见下方说明 |
| Client 模式 | local / cluster | local | 写入 `client.yaml` 顶层的 `mode` |
| 开机启动 | 开 / 关 | 开（首次打开即注册） | macOS 登录项（`SMAppService`）；Windows 的 `HKCU\...\CurrentVersion\Run` 值 |
| 关闭时最小化到系统托盘 | 开 / 关 | 开 | 决定窗口关闭按钮的语义 |
| 任务栏快捷小窗 | 关闭 / 开启 | 关闭 | 决定菜单栏 / 通知区域图标左键弹小窗还是弹菜单；右键始终是菜单。**点「保存」后对下一次点击生效**，不需要重启 |

**配置目录**只重指向，不迁移。切换后托盘会先停止当前承载的 Client（否则界面显示的目录和
实际在跑的隧道会不一致），然后读取新目录下的 `client.yaml`；新目录里没有这个文件时界面显示
默认空值，保存时才写入。旧目录的文件原样保留，需要的话自己复制过去。

**Client 模式**选 `cluster` 时，`client.yaml` 还需要对应的 MySQL 配置才能通过
`config.Validate`。保存前会先做这次校验，配置不完整会直接报错而不是静默写坏文件。

**开机启动**在系统拒绝注册时不会假装成功：开关会弹回原位，并把系统的原始错误显示在页面上。

- **macOS** 登录项按 bundle 的 cdhash 记录，因此把 `.app` 移动位置或重新签名之后需要再开一次。
  可以在**系统设置 → 通用 → 登录项**里看到并管理它。未签名的构建偶尔会被系统拒绝注册，此时
  界面报错但隧道功能不受影响。
- **Windows** 写的是当前用户的 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`，值名
  `TunnelMeshClient`，内容是带引号的 exe 路径，因此不需要管理员。这项按**路径**记账：把
  `TunnelMeshClient.exe` 挪走之后开关会显示为关闭，下次启动托盘按新路径自愈重写。可以在
  **任务管理器 → 启动应用**里看到它；卸载程序会删除这个值。

## 5. 路由配置

对应 `client.yaml` 的 `client.server_url`、`client.token` 与 `client.tunnels`。

| 字段 | 对应配置 | 说明 |
| --- | --- | --- |
| 服务地址 | `client.server_url` | Server 的 `wss://` 地址 |
| 授权 Token | `client.token` | **只写不读**：界面永远只显示“已配置/未配置”，不回显明文 |
| 隧道列表 | `client.tunnels` | 卡片式增删，条目之间用横杠分隔 |

每条隧道：

| 字段 | 对应配置 | 说明 |
| --- | --- | --- |
| 协议 | `protocol` | `tcp` / `udp` / `http` / `socks5` / `http-proxy` |
| 名称 | `name` | 展示与日志用 |
| 监听地址 | `listen` | `host:port` |
| Agent | `agent_id` | 下拉选择，选项来自 Server，显示名称并标注在线状态 |
| 目标地址 / 端口 | `target_host` / `target_port` | `tcp`、`udp`、`http` 必填 |
| 鉴权模式 | `auth_mode` | 仅代理协议可用：`socks5` 取 `none`/`password`，`http-proxy` 取 `none`/`basic`；监听非回环地址时必须选带凭据的那一个 |
| 允许远程访问 | `allow_remote` | 监听地址不是回环地址时必须开启，所有协议通用 |
| 远程认证 URL | `auth_url` | 可选，仅 `socks5` / `http-proxy` 会读取；填写后每条代理请求先向该地址二次校验，与 `auth_mode` 相互独立 |

代理凭据（`socks5` 的 password、`http-proxy` 的 basic）不写进 `client.yaml`，由环境变量
`TUNNELMESH_SOCKS5_USERNAME/PASSWORD` 与 `TUNNELMESH_HTTP_PROXY_USERNAME/PASSWORD` 注入；
“检测”会在缺失时点名缺少哪一个。

### 未进界面的配置项

`client.yaml` 还支持 `client.instance_id`、`client.instance_id_path`、`client.connections.*`
（连接池）、`client.stream.*`、`client.remote_validation.*`（远程认证的缓存与超时）与
`client.metadata`，以及顶层的 `storage` 等 Server 侧配置。这些项**不在托盘界面里编辑**，
但托盘保存时按 YAML 节点树改写文件，不会丢掉手写的键与注释——手改的值会原样保留并生效。

`client.instance_id` 留空时，身份沿用已有的 instance-id 文件；“关于”页显示当前生效的值。

Agent 下拉通过服务端新增的
[`GET /api/v1/client/agents`](../api/openapi.yaml) 拉取，用的是**当前这份 client token**，
因此只会列出这个 token 有权访问、且处于启用状态的 Agent，并带上 `online` 标记。接口不返回
能力、metadata 或归属信息，只够渲染一个选择器。

打开“路由配置”时，只要配置里已有 `server_url` 与 token，列表就**自动拉取一次**（每个窗口一次，
不会因为你正在编辑地址而反复请求）；要重新拉一次点“从服务端拉取 Agent”。Agent 选择框下面那一行
会说明现在到底是哪种状态：尚未拉取、正在拉取、拉取失败（带服务端给出的原因）、还是**确实**
“该 token 作用域内没有可用 Agent”。只有最后一种才是作用域的问题。

**保存**会先跑一遍完整校验，通过后写入 `client.yaml`（权限 `0600`，原子替换）；如果隧道正在
运行，会自动按新配置重启，并在页面上报告是否重启成功。校验不通过时不写文件。

### 检测

“检测”按钮逐项验证当前表单，每项给出 通过 / 失败 / 警告 / 跳过 之一：

| 检查项 | 内容 |
| --- | --- |
| `serverUrl.format` | 服务地址非空且可解析 |
| `serverUrl.reachable` | Server 是否应答健康检查。**警告**表示地址是对的、但该端点被拒绝（例如反向代理对 `/health/ready` 返回 403）：此时后续检查照常执行，不会因此判定配置不可用 |
| `token.present` | 已填写 Token |
| `token.valid` | Token 被服务端接受（`GET /api/v1/client/agents` 返回 200） |
| `agents.available` | Token 作用域内至少有一个可用 Agent |
| `tunnels.present` | 至少配置了一条隧道 |
| `tunnel.protocol` | 协议在支持列表内 |
| `tunnel.listen` | 监听地址是合法的 `host:port`，且可以绑定 |
| `tunnel.agent` | 已选择 Agent，且该 Agent 在 Token 作用域内 |
| `tunnel.target` | 目标主机与端口合法（需要目标的协议才检查） |
| `tunnel.credentials` | 代理凭据环境变量已设置（按 `auth_mode` 决定检查哪一组） |
| `tunnel.authUrl` | 填了 `auth_url` 时：必须是绝对 http(s) URL；写在 `tcp`/`udp`/`http` 上会给出警告 |
| `config.valid` | 整份配置通过 `config.Validate` |

“警告”不会让检测整体失败，它表示配置本身正确、但在托盘之外的条件满足之前不会通流量，
例如选中的 Agent 当前离线。

托盘自己持有监听端口时，绑定探测会自动关闭，否则会把正在运行的隧道误报为端口被占用。

## 6. 统计

约每 2 秒轮询一次，也可以手动刷新。包含：

- **运行态**：是否在运行、启动时间、已运行时长、服务端是否可达、是否与 Server 建立连接、
  是否被互斥锁挡住、以及最近一次启动失败原因。
  汇总卡片下面还会给出健康检查的原始结论（例如 `server health check: unexpected status 403`），
  便于区分「地址错了」与「地址对但 `/health/ready` 被代理挡了」。
- **每条隧道**：名称、协议、监听地址、Agent、目标、状态、最近一次错误。
- **每个 Agent 的连接池**：已开连接数、就绪会话数、活跃流数。
- **汇总**：隧道总数、正在监听数、失败数、Agent 数、连接池合计、重连次数、入站字节数。

入站字节数只统计 Client 从 Server 读到的 frame 负载：目前只有这一个方向被埋点，因此界面
如实只给一个计数器，而不是配一个伪造的出站值。更完整的指标口径见
[可观测性](../operations/observability.md)。

## 7. 关于

版本、commit、构建时间，操作系统版本与 CPU 架构（macOS 显示 “macOS 14.6”，Windows 按内部版本号
区分 “Windows 10” / “Windows 11”，两者都取 `HKLM\...\NT\CurrentVersion`），**渲染内核**
（`WKWebView` 或 `WebView2`，以及运行时缺失时的具体原因），配置目录以及 `client.yaml`、
`tray.json`、`client.lock`、instance-id 的完整路径，当前配置摘要（模式、服务地址、隧道数量、
Token 是否已配置、是否在运行），以及官网、文档、发行版和许可证（Apache-2.0）链接。

“检查更新”只是在浏览器里打开 GitHub Releases 页面，托盘不会自己联网查询版本。

## 8. 文件位置

| 文件 | 内容 | 权限 |
| --- | --- | --- |
| `~/.config/tunnelmesh/client.yaml` | 与命令行 Client 共用的配置，含 `client.token` | `0600` |
| `<webview2>`（Windows） | WebView2 自己的 profile 目录，在配置目录下，不写进任何别的 Edge 配置 | — |
| `~/.config/tunnelmesh/tray.json` | 纯界面偏好：语言、主题、配置目录、开机启动、最小化、快捷小窗 | `0600` |
| `~/.config/tunnelmesh/client.lock` | 互斥锁，进程退出自动释放 | `0600` |
| `~/.config/tunnelmesh/tray.log` | 托盘自身日志：监听地址、启动失败原因、关机过程 | `0600` |

界面偏好刻意不写进 `client.yaml`：那个文件要过 `config.Validate`，塞进未定义的界面键
只会被拒绝或被静默忽略。

`tray.json` 损坏或缺失时托盘按默认值启动，而不是拒绝运行——否则偏好文件出问题时，
操作者连接一个能修它的界面都没有。

## 9. 安全边界

- 设置界面由托盘进程在 **127.0.0.1 的随机端口**上提供，只绑定回环地址。
- 每次启动生成一个随机 secret，随首屏 URL 注入 WebView，之后每个 `/api` 请求都要带上它，
  比较使用常量时间。本机其他进程既猜不到端口，也拿不到 secret。
- 校验请求来源 Origin，并带 `X-Frame-Options: DENY` 与 CSP，持有 Token 的页面不能被别的站点框住。
- Token 只在 Go 侧持有完整值，任何界面接口都不返回它；`print-config` 与 `RedactedJSON`
  也仍然不输出它。
- WebView 只允许加载托盘自己的回环源，页面里的外部链接一律交给系统浏览器，不会把这个
  持有 secret 的 WebView 变成一个通用浏览器。
- 静态资源不需要 secret（它们就是仓库里公开的 JavaScript，不含凭据），而首屏文档带
  `?secret=`、随后请求资源时不带，给静态资源加密只会让首屏加载失败。

## 10. 排障

| 现象 | 原因与处理 |
| --- | --- |
| 窗口空白 | 构建时没有嵌入前端产物。用 `scripts/package-macos-tray.sh` 重新打包，脚本会在缺少产物时直接拒绝 |
| 提示“已有 TunnelMesh client 在运行” | 命令行 `run` 或另一个托盘持有同一份配置的锁。停掉对方，或让其中一个改用别的配置目录 |
| 隧道不启动，提示缺少 `client.server_url` / `client.token` | 路由配置还没填完；填好并保存 |
| 保存报配置不可用 | 页面会给出 `config.Validate` 的原始错误。常见于选了 `cluster` 模式但没有 MySQL 配置 |
| 开机启动开关弹回 | 系统拒绝了登录项注册，页面上会显示系统的原始错误。确认 `.app` 没有被移动过，必要时重新打开开关 |
| Agent 下拉是空的 | 先看选择框下面那行说明。**尚未拉取**：点“从服务端拉取 Agent”。**拉取失败**：那行会带原因，常见的是 token 无效/已撤销、服务端版本过旧没有 `GET /api/v1/client/agents`、地址或服务不可达。**该 token 作用域内没有可用 Agent**：这才是真的没有——去管理后台确认 Agent 是否启用、是否属于这个 token 的 owner 与作用域 |
| 输入框里首字母自动变大写 | macOS 的“自动大写字词的首字母”等文本辅助功能。托盘启动时会把这几项写进**自己**的偏好域（`com.tunnelmesh.client-tray`）关掉它们，不改动系统级设置；界面里每个技术字段也标了 `autocapitalize=none`/`spellcheck=false`。若仍复现，多半是中文输入法的拼音上屏显示（系统行为，与输入法有关），或系统设置 > 键盘 > 文本 里的开关被别的程序改过 |
| ⌘V 粘贴无效 | 确认用的是最新的发行包。文本框的编辑快捷键来自应用主菜单，早期版本没有安装主菜单，因此任何剪贴板快捷键都不会生效（⌃V 同样） |
| 点托盘图标弹的是小窗而不是菜单 | 这是**通用**页里「任务栏快捷小窗」被开启的结果，不是故障：右键仍然打开菜单（macOS 上也可以用 ⌃ 点击），把该设置改回「关闭」并**点「保存」**即恢复左键出菜单 |
| 输入框里 `Ctrl+V` 在 macOS 上不生效 | 托盘已把 ⌃C/⌃V/⌃X 映射到同一组编辑动作，若仍不生效请确认焦点在输入框内；Windows 上 `Ctrl+V` 由 WebView2 原生处理，不经过任何映射 |
| 改了通用页的选项，但托盘行为没有任何变化 | 这一页的所有选项都要点底部的**保存**才写入 `tray.json`，只点单选框不会生效。有未保存的改动时，保存按钮旁会显示「有未保存的更改，点“保存”后生效」；保存栏在窗口较小时会一直贴在页面底部，不会被滚走 |
| 统计页显示服务端「不可达」或 403，但隧道是通的 | 健康检查打的是 `GET /health/ready`，反向代理（如 nginx/openresty）默认可能不放行这个路径，而 `/ws/client` 与 `/api/v1` 是通的。此时**检测**里 `serverUrl.reachable` 是**警告**而不是失败，后续检查照常跑；小窗的「服务端」一行在有会话时直接显示**已连接**。要消除警告，请在代理上放行 `/health/ready` |
| 小窗里数字不更新 | 小窗只在显示时轮询本地接口。收起再点开即可刷新；若主窗口也拿不到数据，看**统计**页顶部的错误提示 |
| Windows 上托盘图标没出现，也没有报错 | 先看关于页/日志里的渲染内核提示。缺 WebView2 运行时是**唯一**被静默容忍的启动差异：托盘照常运行，但“打开主界面”会改用系统浏览器。装上运行时即恢复原生窗口 |
| Windows 上 `client run` 说“已有 TunnelMesh client 在运行” | 托盘正在承载同一份 `client.yaml`，这是互斥锁的正常结果，不是残留文件。退出托盘或改用另一份配置即可；进程被强杀时锁由内核回收，不需要手动删 `client.lock` |
| 双击 app 报“这个版本不能与此版本的 macOS 配合使用…要求 macOS 15.0 或更高版本” | v1.3.1 的托盘包在 macOS 15 的 runner 上打包，二进制里记录的最低系统版本被写成了宿主版本（`Info.plist` 与清单里仍写 13.0，但拦你的是二进制）。打包脚本现已把下限同时钉进产物并用 `otool` 读回校验：改用修复之后的发行包，或在 macOS 主机上 `./scripts/package-macos-tray.sh` 自行打包 |
| 想看托盘自己的日志 | `~/.config/tunnelmesh/tray.log` |
