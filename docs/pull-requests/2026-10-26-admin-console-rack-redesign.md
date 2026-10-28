# 管理后台界面重设计「机架 Rack」

- 标题：重做管理后台的设计系统、外壳与核心页面，新增深色主题与命令面板
- 目标分支：`main`
- 相关记录：[实施计划](../superpowers/plans/2026-10-26-admin-console-rack-redesign.md)

## 摘要

用一套设计令牌（`web/src/styles/tokens.css`）统一颜色、字号、间距、圆角与动效，浅色 / 深色两套主题，主题切换在 `<html data-theme>` 上完成，
`index.html` 里的同步内联脚本避免首屏闪白。Element Plus 保留为行为层，由 `element.css` 全量换肤。
外壳重做为分组导航（可折叠）、顶栏、账户菜单与命令面板；仪表盘新增指标条与 Agent 配线板；Agents、令牌、审计日志页使用共用的
`IdCell`（中间截断、可复制）、`PagerBar`、`DetailDrawer`、`SecretBlock`；其余视图通过令牌自动继承，并清除了全部写死的颜色。

## 用户影响

- 新增“浅色 / 深色 / 跟随系统”主题，选择保存在浏览器（`tunnelmesh_theme`）；WebSSH 终端跟随主题。
- 语言切换与退出移入右上角账户菜单；侧栏分组、可折叠（`[` 键），折叠状态保存在浏览器（`tunnelmesh_nav_collapsed`）。
- `Ctrl/⌘ + K` 打开命令面板，只搜导航与少量操作。
- 令牌密钥弹窗默认遮罩，可显示与复制，仍只显示一次。
- 审计日志行点击打开右侧抽屉，已应用的筛选条件以可移除标签显示。
- 浏览器下限提高到 Chrome 111 / Safari 16.2 / Firefox 113（`color-mix()`）。
- 无接口、配置或数据库变更；Go 侧只有嵌入的前端产物变化。

## 依赖与许可

- 新增内置字体 Instrument Sans、Martian Mono（latin 子集，合计约 54 KB，SIL OFL 1.1），许可证文本在 `web/src/assets/fonts/`，已写入 `NOTICE`。
- 移除未使用的 `@element-plus/icons-vue`。

## 验证

- `npm test -- --run`：60 个文件 / 467 个用例通过（新增主题、令牌对比度、字体体积、Element 皮肤、图标、基础部件、导航、命令面板、
  配线板、列表部件、终端主题等测试）；`npm run build`、`./scripts/verify-web-embed.sh`、`go test ./internal/server -run TestEmbeddedWebDist` 通过。
- 视觉：`web/scripts/visual-qa/` 对 5 个页面 × 2 主题 × 2 视口截图并逐张评审。
- 过程中发现并修正：Agent 列表接口拒绝 `sort=name`、忽略 `status` / `enabled`（见计划“偏差”），已加回归测试。

## 回滚

纯前端改动：回退本分支提交并重新 `npm run build` 同步嵌入产物即可；浏览器里遗留的 `tunnelmesh_theme` / `tunnelmesh_nav_collapsed` 键无副作用。

## 未完成

- 无。`docs/assets/` 已重截（`demo.gif` 为五张静态截图轮播）；WebSSH / SFTP e2e 已通过（`TM_E2E_LOCALE=en-US` 可指定界面语言，并修复了英文界面下指纹确认按钮匹配不到的问题）。
