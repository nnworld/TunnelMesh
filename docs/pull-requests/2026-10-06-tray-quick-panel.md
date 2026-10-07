# feat(tray): 任务栏快捷小窗与统一的菜单栏图标

- **目标分支**：`main`
- **实施计划**：`docs/superpowers/plans/2026-10-06-tray-quick-panel.md`

## 摘要
托盘「通用」页新增「任务栏快捷小窗」单选项（关闭 / 开启，默认**关闭**）。开启后点击菜单栏图标
弹出紧凑小窗，显示运行状态、服务端可达性、运行时长、监听中/隧道数、活跃流、已接收字节、重连
次数与逐隧道状态（含最近错误），并提供「打开主界面 / 启动或停止 / 退出」三个动作；右键（或
⌃ 点击）始终打开原菜单。同时把菜单栏图标从系统符号 `network` 换成与 Finder 同一枚品牌标记的
单色 template 版。

小窗不是第二套界面：它是同一份 `web-tray` 产物按 URL fragment（`#/panel`）选出的另一个根组件，
读的是同一个 `/api/stats`、同一份本地接口与同一个启动 secret，因此不新增任何免鉴权面。

## 用户影响
- 默认行为与升级前逐字节一致（左键仍是菜单）。
- 开启小窗后：左键=小窗，右键/⌃点击=菜单，⌘Q 与「退出」语义不变。
- 菜单栏图标随系统深浅色自动着色，形状与 Finder/Dock 中的 app 图标一致。
- 收起小窗不影响隧道；托盘退出时小窗随之关闭。

## API / Schema / 配置影响
- `tray.json` 新增布尔 `quickPanel`，默认 `false`；缺该键的旧文件按关闭加载（向后兼容）。
- 本地接口 `GET/PUT /api/settings` 新增 `quickPanel`（更新用指针语义，未携带即不改）。
- `GET /api/v1/*` 与数据库 Schema 无改动；`tunnelmesh-client` CLI 命令面与外部行为无改动。
- `TMTrayConfig` 新增 `panelURL`/`quickPanel` 与 `TMTraySetQuickPanel`，仅存在于 `-tags tray` 的
  macOS 构建；默认 `CGO_ENABLED=0` 构建不编译这些文件（`scripts/tray_build_tag_test.go` 仍绿）。

## 安全与授权
- 小窗与主窗口同源、同端口、同 secret，逐请求校验仍生效；没有新增监听地址或跨源面。
- `/api/actions/show-window` 与 `/api/actions/quit` 早已注册但从未被原生层接线（会返回 501）。
  本次由 `cmd/tunnelmesh-client-tray` 通过 `SetWindowHandlers` 接上，二者都只在带 secret 的
  loopback 请求下可达。

## 测试证据
- `go test ./... -count=1`：全绿（28 包含新断言）。
- `go test -race ./...`：全绿。
- `go vet ./...` 与 `CGO_ENABLED=1 go vet -tags tray ./...`：无输出。
- `cd web-tray && npm test -- --run`：84 用例通过（新增 11：快捷小窗设置 3、小窗渲染/动作/轮询/错误 7、路由 1）。
- `npm run build` 通过并镜像到 `internal/tray/webdist/dist`（embed 目录被 .gitignore 排除）。
- `deploy/macos`：新增外壳守卫（左右键分流、`NSPopover`、`TMTraySetQuickPanel`、菜单不再常驻
  `statusItem.menu`）与图标守卫（三档 PNG 尺寸/纯黑/覆盖率、打包与生成链、Go↔TS fragment 配对）。
- `bash scripts/generate-tray-icon.sh` 重跑后 `TunnelMeshClient.icns` 字节不变（111303），
  证明生成链确定性；菜单栏 PNG 由同一次运行产出。
- macOS 实机冒烟（见下）：打包脚本自校验通过，`otool -l` 读回 `minos 13.0`，`codesign -v` 通过。

## 发布步骤
1. `cd web-tray && npm ci && npm run build`
2. `bash scripts/package-macos-tray.sh`（产出 `.app` 与 `.dmg`，`deploy/macos/TunnelMeshMenuBar*.png`
   会被拷进 `Contents/Resources`）
3. 覆盖 `/Applications/TunnelMesh Client.app`，重新登录或手动启动；已注册登录项的 bundle 路径
   未变，无需重开「开机启动」。

## 回滚步骤
- 把「任务栏快捷小窗」改回「关闭」即恢复左键出菜单；或直接用上一版发行包覆盖 `.app`。
- 删除 `tray.json` 中的 `quickPanel` 键等价于默认值。隧道配置与 token 不受影响，无数据迁移。

## Reviewer 关注点
- `internal/tray/native/tray_darwin.m`：`NSStatusItem.menu` 改为自持 `statusMenu` 是必要代价——
  菜单常驻在按钮上时 `NSStatusBarButton` 吞掉所有点击、action 永不触发。请确认右键/⌃点击在
  你的机器上仍能稳定弹菜单。
- `TMPanelReopenGuard`：transient popover 会在同一次 mouse-down 里自行关闭，没有这个时间戳
  就会出现「再点一次图标关掉又立刻弹开」。
- 小窗的启动/停止复用 `useStatsStore`，与统计页同一份轮询实现；`endPolling` 挂在
  `onBeforeUnmount`，收起即停表。
- **状态项动作挂在 mouse-up**（见计划文档的追加记录）：挂在 mouse-down 会把主线程关进
  `-[NSCell trackMouse:...untilMouseUp:]`，菜单弹出后再也等不到松开事件，图标失灵且
  SIGTERM 退不掉。这条只能靠实机点出来，守卫测试已把两个 mask 都钉住。
- 小窗显示后主动 `makeKeyAndOrderFront:`；Esc 关闭小窗；`windowShouldClose:` 忽略不可见
  窗口的关闭，避免一次误传的 cancel 在「关闭即退出」配置下断掉隧道。

## 实机验证（macOS 14.6）
用 System Events 驱动真实状态项、CGWindowList 核对窗口层：

- 左键 → 出现 366×446 的 popover（小窗），再点一次关闭，第三次再打开（toggle 正常）。
- 右键 → 出现 layer=101 的菜单窗口，同时小窗自动收起；菜单项读回为
  `打开主界面 / 打开官网首页 / (分隔线) / 退出`。
- Esc → 菜单关闭；随后 SIGTERM 约 500ms 内退出并记录 `menu bar shell stopped`。
- 新包（v1.3.3-quickpanel）替换 `/Applications/TunnelMesh Client.app` 后，
  `curl -x socks5h://127.0.0.1:18081 http://www.baidu.com` → 200，隧道未中断。
- `codesign --verify --deep --strict` 通过；`otool -l` 读回 `minos 13.0`。
- 息屏期间 `screencapture` 只能拿到旧帧，因此以上验证以窗口列表与无障碍树为准。

## 集成状态
- 分支 `codex/tray-quick-panel`，基于 `main`。
- 未签名/未公证：仍为 ad-hoc `codesign -s -`，Developer ID 与公证是独立后续项。

## 追加：实机反馈后的两处修复

用户反馈「任务栏快捷小窗似乎没有变化」。定位过程与结论见
`docs/superpowers/plans/2026-10-06-tray-quick-panel.md` 的两条追加记录；摘要如下。

**A. 设置改了但界面不说"还没保存"。** 小窗本身经 CGEvent 真实左键 + 无障碍树复验为可用
（366×446 popover，内容为小窗而非主界面）。真正的问题是「通用」页所有选项都要点底部**保存**
才写 `tray.json`，而保存按钮在 50% 窗口里位于折叠线以下：点了单选框、看不到保存、菜单栏行为不变，
就只能理解为"没变化"。

- `GeneralView.vue`：新增 `dirty`，有未保存改动时在保存按钮旁显示「有未保存的更改，点"保存"后生效。」，
  保存成功后消失；`.tm-actions` 改为 `position: sticky; bottom: 0` 带背景，窗口再小也不会滚走。
- 「任务栏快捷小窗」提示文案补"点保存后对下一次点击生效，无需重启"。
- 新增用例：`app.spec.ts`（**先挂载、后异步加载**的真实顺序下，单选框反映已存值；断言用选中项索引，
  不依赖语言）、`general-view.spec.ts > unsaved changes`。

**B. 反向代理挡了 `/health/ready`，托盘把"应答但被拒"当成"不可达"。** 本机 Server 在 openresty 之后，
`GET /health/ready` 返回 403 而 `/ws/client`、`/api/v1` 正常，于是小窗在隧道跑着的情况下显示
「服务端：不可达」，**检测**还会把整份配置判为不可用。

- `internal/tray/server.go`：新增 `AnsweredError`，把 HTTP 应答与无应答分开。
- `internal/tray/validate.go`：`serverUrl.reachable` 变为 **warning**（保留原始消息），后续检查照常执行。
- `internal/tray/app.go`：`refreshServerProbe` 计"有应答"为可达，原始结论留在 `serverProbe`。
- 前端：小窗在有会话时直接显示「已连接」；统计页汇总下方显示健康检查原始结论；**检测**总标题改为
  「检测通过，但有 N 项警告。」而不是对警告说"全部通过"。

### 追加后的用户影响
- 默认行为不变；`GET /api/stats` 的 `serverReachable` 语义放宽为"该地址有应答"，明细在 `serverProbe`。
- 检测不再因为代理策略把可用配置判为不可用；需要放行 `/health/ready` 才能消除该警告（文档已写明）。

### 追加的测试证据
- `cd web-tray && npx vitest run`：**91 通过**（本轮新增 4：异步加载下的单选框、未保存提示、
  小窗「已连接」优先、检测警告标题）。`npm run build` 通过并镜像 `internal/tray/webdist/dist`。
- `go test ./internal/tray/ ./internal/client/ ./scripts/ ./deploy/macos/ -count=1`：全绿；
  `go test ./... -count=1`：全绿；`go vet ./...`、`gofmt -l`、`git diff --check`：干净。
- 实机（v1.3.6-quickpanel，arm64，`/Applications` 已替换）：小窗「服务端=已连接」、
  检测「检测通过，但有 1 项警告。」+ `unexpected status 403`、通用页未保存提示出现/消失、
  `curl -x socks5h://127.0.0.1:18081 http://www.baidu.com` → 200、`codesign --verify --deep --strict` 通过。

### 追加的回滚
- 纯 UI/展示语义：回滚 `internal/tray/{server,validate,app}.go` 与 `web-tray` 产物即可，
  不涉及 `tray.json`、`client.yaml` 结构或任何接口签名。
