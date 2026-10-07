# TunnelMesh 托盘：任务栏快捷小窗（macOS）

## 目标
- 在托盘「通用」tab 增加「任务栏快捷小窗」单选项（关闭 / 开启，默认**关闭**）。
- 开启后，点击菜单栏图标不再直接弹菜单，而是弹出**快捷小窗**：紧凑显示运行状态、服务端连通、隧道与流量汇总、每条隧道状态，并提供「打开主界面 / 启动或停止 / 退出」操作。
- 小窗关闭后不影响隧道；菜单栏菜单在小窗开启时改由**右键（或 ⌃ 左键）**打开，三个原菜单项（打开主界面 / 打开官网首页 / 退出）全部保留。
- 关闭该设置时行为与现状逐字节一致：任意点击都弹出菜单。

## 架构决策
- **小窗是同一份前端的一个路由，而不是第二套界面**：WKWebView 加载 `api.URL() + "#/panel"`，`main.ts` 按 hash 选择根组件。复用 `/api/stats`、`/api/settings`、`/api/actions/*`、i18n 与主题逻辑，避免第二个数据源。
- **小窗只走已存在的本地 API**：`show-window`、`quit`、`start`、`stop` 早已在 `internal/tray/api.go` 注册，本次首次把它们接到原生层（`SetWindowHandlers` 此前只在测试里被调用），因此小窗不需要任何新的原生回调面。
- **原生侧只加“什么时候显示什么”**：左键/右键分流与 `NSPopover` 归属 macOS shell；配置项真值的唯一来源仍是 `tray.json`，Go 在保存后同时推送给 shell（`TMTraySetQuickPanel`），避免 shell 反向查询造成卡顿。
- 采用 `NSPopover`（`transient`）而非自建 `NSPanel`：定位、指向箭头、点击外部自动收起由系统负责，少一套窗口生命周期代码（KISS）。
- 保持既有隔离约束：全部 Cocoa 代码仍在 `//go:build tray && darwin` 之后，默认构建不触达 WebKit。

## 技术栈
- Go 1.x（`internal/tray`、`internal/tray/native`、`cmd/tunnelmesh-client-tray`）。
- Objective-C + AppKit/WebKit（`tray_darwin.m`）。
- Vue 3 + TypeScript + Element Plus + vue-i18n + Pinia（`web-tray/`），vitest 单测。

## 规格引用
- `docs/superpowers/plans/2026-10-05-client-system-tray-macos.md`（托盘总体设计与互斥/配置约定）。
- `docs/user-guide/client-tray.md`、`docs/deployment/macos-client-tray.md`。

## 全局约束
- 不改 `tunnelmesh-client` CLI 命令面与外部行为。
- `tray.json` 只放 UI 偏好，不污染 `client.yaml` 校验；文件 0600。
- 本地 API 仍仅绑 loopback 并逐请求校验 secret；小窗与主窗口共用同一 secret，不新增免鉴权端点。
- 新字段向后兼容：旧 `tray.json` 缺 `quickPanel` 必须按“关闭”加载。

## 文件清单
- `internal/tray/prefs.go`：`Preferences.QuickPanel` + 默认值。
- `internal/tray/prefs_test.go`、`internal/tray/app_test.go`、`internal/tray/api_test.go`：红→绿测试。
- `internal/tray/app.go`：`SettingsView.QuickPanel`、`SettingsUpdate.QuickPanel`、保存与变更通知。
- `internal/tray/api.go`：`APIServer.PanelURL()`。
- `internal/tray/native/native.go`：`TMTrayConfig.panelURL/quickPanel`、`SetQuickPanel`、`Config`/`DefaultConfig` 字段。
- `internal/tray/native/tray_darwin.m`：状态项左右键分流、`NSPopover` + 面板 WKWebView、`TMTraySetQuickPanel`、面板导航策略与失败重载。
- `cmd/tunnelmesh-client-tray/main.go`：面板 URL 注入、偏好变更推送、`SetWindowHandlers` 接线。
- `deploy/macos/tray_shim_test.go`：原生外壳守卫测试（源码断言）。
- `web-tray/src/panelRoute.ts`（新）、`web-tray/src/QuickPanelApp.vue`（新）：hash 路由与紧凑界面。
- `web-tray/src/main.ts`：按 hash 挂载根组件。
- `web-tray/src/views/GeneralView.vue`、`web-tray/src/stores/settings.ts`、`web-tray/src/api/types.ts`：设置项与表单。
- `web-tray/src/i18n/zh-CN.ts`、`web-tray/src/i18n/en-US.ts`：文案。
- `web-tray/src/tests/helpers.ts`、`general-view.spec.ts`、`quick-panel.spec.ts`（新）：前端测试。
- `docs/user-guide/client-tray.md`、`docs/deployment/macos-client-tray.md`、`docs/pull-requests/2026-10-06-tray-quick-panel.md`、`docs/README.md`（索引重建）。

## 任务间接口
- `tray.Preferences{ QuickPanel bool \`json:"quickPanel"\` }`，默认 false。
- `SettingsView.QuickPanel bool \`json:"quickPanel"\``；`SettingsUpdate.QuickPanel *bool \`json:"quickPanel,omitempty"\``（nil=不改）。
- `func (s *APIServer) PanelURL() string` == `URL() + "#/panel"`。
- 前端：`isPanelRoute(hash: string): boolean`（仅接受 `#/panel`）；`SettingsView.quickPanel: boolean`、`SettingsUpdate.quickPanel?: boolean`。
- 原生：`TMTrayConfig{ const char *panelURL; int quickPanel; }`、`void TMTraySetQuickPanel(int enabled)`。

## TDD 步骤
1. 红：`prefs_test.go` 断言默认 false、显式 true 能落盘回读；`app_test.go` 断言 `Settings` 暴露该值且 `SaveSettings` 触发 `OnPreferencesChanged`；`api_test.go` 断言 `PUT /api/settings {"quickPanel":true}` 生效、`PanelURL()` 带 `#/panel`。预期：编译失败/断言失败（字段不存在）。
2. 绿（Go）：加字段与默认值 → 视图/更新处理 → `PanelURL()`。
3. 红→绿（原生）：`tray_shim_test.go` 增加守卫（面板 URL 载入、`NSPopover`、左右键分流、`TMTraySetQuickPanel`、`statusItem.menu` 不再常驻导致左键无法接管）；随后实现 `.m`/`native.go`。预期先失败于缺少符号/字符串，实现后通过。
4. 红→绿（前端）：`general-view.spec.ts` 断言出现「任务栏快捷小窗」单选组、默认「关闭」、保存携带 `quickPanel`；新增 `quick-panel.spec.ts` 断言 `#/panel` 渲染紧凑小窗、2s 轮询 stats、三个动作按钮调用对应本地 API。
5. 文档与索引：`python3 scripts/gen_doc_index.py`。
6. 全量验证：`go test ./... -count=1`、`go vet ./...`、`CGO_ENABLED=1 go vet -tags tray ./...`、`cd web-tray && npm test -- --run && npm run build`、`bash -n scripts/package-macos-tray.sh`、`git diff --check`。
7. macOS 实机冒烟：打包并替换 `/Applications/TunnelMesh Client.app`，验证左键出小窗、右键出菜单、开关关闭后左键回菜单、小窗内三按钮可用、浅色/深色与中英跟随。

## 验证命令
见上第 6 步；macOS 打包：`bash scripts/package-macos-tray.sh`（产出 `.app` 与拖拽安装 `.dmg`）。

## 回滚注意事项
- 纯增量：删除 `tray.json` 中的 `quickPanel` 或把设置切回「关闭」即恢复旧行为；`.app` 用备份包覆盖即可回滚，隧道配置与 token 不受影响。
- 无数据库、无协议、无 API 破坏性变更；`/api/v1/*` 未改动。

## 追加记录：macOS 实机验证中发现并修掉的四件事

以下都不是推演出来的，是在 macOS 14.6 主机上驱动真实状态项（System Events 点击 + CGEvent
右键 + 窗口列表核对）时暴露的，实现时已一并修掉并补了守卫测试。

1. **动作必须挂在 mouse-up。** 最初把 action 配成 `NSEventMaskLeftMouseDown |
   NSEventMaskRightMouseDown`，实测主线程会永久停在 `-[NSCell
   trackMouse:inRect:ofView:untilMouseUp:]` 里：状态按钮的 tracking 从按下一直占住主线程到
   松开，而我们在它内部弹菜单；菜单自己的模态跟踪吃掉了那个 mouse-up，按钮的 tracking 就
   再也等不到结束事件。后果是图标失灵，且 `TMTrayStop` 排在主队列上永远不执行——SIGTERM
   收到后进程不退出，锁与隧道一直被占着。改成 mouse-up 触发后，实测「左键→右键→Esc」再
   SIGTERM 约 500ms 内干净退出。
2. **popover 需要成为 key 窗口。** accessory 应用平时没有 key window，WKWebView 只把键盘
   输入路由给 key window，非 key 窗口的控件还会吞掉第一次点击。显示后取
   `panelWebView.window` 调 `makeKeyAndOrderFront:`（拿不到就下一轮主队列再试一次）。
3. **Esc 要能关掉小窗。** 用局部事件监视，且只在 `panelPopover.isShown` 时接管，避免把
   主窗口里 Esc 关下拉/对话框的语义抢走。
4. **不可见窗口的 close 必须忽略。** `windowShouldClose:` 是「关窗=隐藏还是退出」的判
   据点；一次经响应链误传到隐藏窗口的 cancel 会让「关闭时最小化到托盘=关」的配置直接退出托
   盘、断掉所有隧道。先判 `sender.isVisible`。

另外加了 `shutdownWatchdog`（3s）：信号处理里停完隧道与本地接口后，若 run loop 因任何
AppKit 嵌套循环没能返回，就记一条日志并退出，而不是把退出交给系统的强杀。

## 追加记录：实机反馈「快捷小窗没有变化」的定位与修复

用户反馈：开启「任务栏快捷小窗」后，菜单栏行为似乎没有变化。按 TDD 先复现、再修：

1. **原生侧确认可用。** 用 CGEvent 向真实状态项发一次左键（mouse-down/up），`CGWindowList` 读到
   366×446 的 popover；再用无障碍树读它的 `AXWebArea`，内容是小窗本体（状态、服务端、运行时长、
   隧道数、活跃流、已接收、重连、逐隧道行、三个动作按钮、更新时间）。因此「弹小窗」这条链路是通的。
2. **Go 侧排除。** `internal/tray` 的 `TestAppQuickPanelIsReportedAndObservable` 与
   `TestAPISettingsQuickPanelRoundTrip` 覆盖 GET/PUT 与 `tray.json` 落盘；安装到
   `/Applications` 的二进制里 `quickPanel` 的 json tag 与当前前端产物（`index-BROhsCFn.js`）都在，
   不是旧包。
3. **前端排除，但补了一个真空洞。** 原有用例都在挂载**之前**把 store 灌好（`prepare: seed`），
   没有覆盖真实顺序：窗口先渲染、`/api/settings` 之后才回来。新增
   `app.spec.ts > general tab reflects the tray after an asynchronous load`，按真实顺序断言单选框
   选中项（用**索引**而不是文案，避免语言解析影响断言）。该用例通过，说明读取链路没问题。
4. **真正的原因：改动不落盘，而界面不说。** 「通用」页所有选项都要点底部**保存**才写
   `tray.json`；只点单选框时表单变了、托盘行为不变，而保存按钮在 50% 窗口里位于折叠线以下，
   看不见就以为设置无效。这是可观测性缺陷，不是功能缺陷。

修复（保持"表单级保存"的既有设计，不改成点击即写盘）：

- `GeneralView.vue` 增加 `dirty` 计算属性，有未保存改动时在保存按钮旁显示
  `general.unsaved`（zh：有未保存的更改，点“保存”后生效。/ en：Unsaved changes — press Save to
  apply them.），保存成功后自动消失。
- `.tm-actions` 改为 `position: sticky; bottom: 0` 并带背景色，窗口再小保存栏也不会被滚走。
- 「任务栏快捷小窗」的提示文案补上"点保存后对下一次点击生效，无需重启"，与
  `docs/user-guide/client-tray.md` 的表格、FAQ 同步。
- 新增 `general-view.spec.ts > unsaved changes`：未改动时不显示、点开启后显示、保存后消失。

验证：`npx vitest run` 87 通过（新增 3）；`npm run build` 通过并镜像 `internal/tray/webdist/dist`。

## 追加记录：小窗显示"服务端不可达"而隧道在跑

继续实机核对小窗内容时发现第二个问题，与图标无关，但同样会让用户认为"设置没生效"：

- 现象：小窗「服务端」一行显示**不可达**，而 `curl -x socks5h://127.0.0.1:18081 http://www.baidu.com`
  返回 200，隧道明显在工作。
- 根因：可达性来自 `GET /health/ready`。本机 Server 部署在 openresty 之后，代理对该路径返回
  **403**（`https://tunnelmesh.claw.qihoo.net/health/ready` 实测 403，站点根路径 200），
  而 `/ws/client` 与 `/api/v1` 是放行的。原实现把"应答但被拒"和"完全无应答"合并成同一个
  `err != nil`，于是报告"不可达"，并且**检测**把整份配置判为不可用。

修复按 TDD 分三层，先红后绿：

1. `internal/tray/server.go`：新增 `AnsweredError{Status}`，`CheckHealth` 对非 2xx 返回该类型，
   传输层失败仍返回普通错误。用例
   `TestServerClientCheckHealthDistinguishesAnAnswer` / `TestServerClientCheckHealthTransportFailureIsNotAnAnswer`。
2. `internal/tray/validate.go`：应答但被拒 → `serverUrl.reachable` 记为 **warning**（消息保留原始
   `unexpected status 403`），并且不再跳过后续检查，`token.valid` 用真正可用的 API 路径给出结论。
   用例 `TestValidateWarnsWhenTheHealthEndpointIsRefused`（含 `blockedHealthServer` 夹具：健康端点 403、
   API 正常）。
3. `internal/tray/app.go`：`refreshServerProbe` 把"有应答"计为可达，原始结论继续放在
   `serverProbe` 明细里。用例 `TestStatsProbeCountsAnAnsweredRefusalAsReachable`。
4. 前端：小窗只有一行，因此**有会话时直接显示「已连接」**（会话是比探针更强的证据）；统计页在汇总
   卡片下面显示健康检查原始结论；**检测**的总标题不再对警告说"全部检测通过"，改为
   「检测通过，但有 N 项警告」。用例见 `quick-panel.spec.ts`、`stats-view.spec.ts`、
   `routing-view.spec.ts`。

实机复验（v1.3.6-quickpanel，macOS 14.6）：

- 左键 → 小窗「服务端」= **已连接**，隧道数 1/1、活跃流随流量变化。
- 检测 → 总标题「检测通过，但有 1 项警告。」，`服务端可达性` 行 = 警告 + `unexpected status 403`，
  `token.valid` 等其余项 = 通过。
- 统计 → 汇总下方出现「健康检查结果：server health check: unexpected status 403」。
- 通用 → 「任务栏快捷小窗」= 开启；点「关闭」立刻显示「有未保存的更改，点"保存"后生效。」，
  改回「开启」后提示消失，`tray.json` 未被改动。

运维口径：要消除该警告，应在反向代理上放行 `/health/ready`；托盘不再因为代理策略而误判配置不可用。
