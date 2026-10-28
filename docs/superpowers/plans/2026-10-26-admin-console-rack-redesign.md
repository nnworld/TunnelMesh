# 管理后台界面重设计「机架 Rack」实施计划

> 状态：已实施（未合并）。验证与取舍见 [PR 记录](../../pull-requests/2026-10-26-admin-console-rack-redesign.md)。

## 目标

管理后台（`web/`，Vue 3 + Element Plus）原先 Element 默认蓝与手写蓝并存，没有图标系统和暗色主题，容器一律白底描边卡片，
同类样式（筛选栏、分页、详情列表）在各视图里重复 5 到 14 份，另有约 35 处写死的颜色。目标是一套有明确风格的设计系统，
并让所有页面经由令牌与全局换肤自动继承。

## 决策

| 项 | 决定 |
|---|---|
| 范围 | 设计系统 + 外壳 + 登录页 + 仪表盘 / Agents / 令牌 / 审计日志精雕；其余视图通过令牌与全局皮肤继承，只做机械改动 |
| 主题 | 浅色 + 深色，可切换（浅色 / 深色 / 跟随系统），记住选择 |
| 组件库 | 保留 Element Plus 做行为层（AGENTS.md），深度换肤 + 自研关键部件 |
| 视觉 | 「机架 Rack」：亚麻灰底、石油青主色、等宽数据字体、LED 状态、配线板 |
| 浏览器下限 | Chrome 111 / Safari 16.2 / Firefox 113（`color-mix()`） |
| 字体 | Instrument Sans + Martian Mono，latin 子集 woff2 内置（合计 ≤140 KB，OFL，见 NOTICE），中文走系统字体 |

## 任务与完成情况

| # | 任务 | 结果 |
|---|---|---|
| 0 | 视觉验证脚手架 `web/scripts/visual-qa/`（本地 Server + 种子数据 + 无头 Chrome，5 页 × 2 主题 × 2 视口） | 完成 |
| 1 | 令牌、字体、主题切换（`tokens-contrast` 固化 WCAG 对比度） | 完成 |
| 2 | Element 全量换肤（`element.css`），十六进制颜色 lint 带“未迁移”名单 | 完成，名单已清空 |
| 3 | `TmIcon` 与基础部件：StatusLed、BrandMark、CodeBlock、SecretBlock、DescriptionList | 完成 |
| 4 | 外壳：`layouts/navigation.ts`、NavRail、TopBar、AccountMenu、ThemeToggle、CommandPalette | 完成 |
| 5 | PageHeader、StatusTag（LED 标签）、DataState、EmptyState、ErrorState | 完成 |
| 6 | 登录页（左侧静态机架线稿，不放虚构数据） | 完成 |
| 7 | 仪表盘：MetricStrip + PatchPanel + 最近事件 | 完成 |
| 8 | Agents 列表：内联筛选、IdCell、PagerBar、悬停行内操作 | 完成（见偏差） |
| 9 | 令牌页与密钥弹窗（SecretBlock 默认遮罩） | 完成 |
| 10 | 审计日志：已应用条件的可移除标签、行点击打开 DetailDrawer | 完成 |
| 11 | 其余视图清扫：颜色字面量换令牌、`.page-size` 去重、终端跟随主题、移除 `@element-plus/icons-vue` | 完成 |
| 12 | 文档与记录 | 完成；`docs/assets/*.png` 与 `demo.gif` 已用新界面重截 |

## 与原计划的偏差

- **Agents 页没有状态 / 启用筛选**：`GET /api/v1/agents` 只处理 `keyword`、`cursor`、`limit` 与按 `createdAt` / `updatedAt` / `id` 排序，
  会忽略 `status` 与 `enabled`。放上去只会是不起作用的控件，所以没有做；需要的话应先扩展服务端接口。
- **仪表盘配线板**取最新的 32 个 Agent 并在浏览器内按名称排序：接口拒绝 `sort=name`（422）。图例总数使用摘要里的 `agentsTotal`。
- `ListPageShell` / `FilterBar` / `useListQuery` 仍未接入任何视图；本次用 `PagerBar` 与 `IdCell` 抽出了确实被重复的部分，
  没有为了“用上”而硬接，遵守 YAGNI。
- 十六进制 lint 之外，Element 自己注入的默认变量块里仍含 `#409eff`，被 `:root:root` 覆盖，因此不能用 `grep 409eff web/dist` 为空来验收。

## 硬约束（由测试守护）

- `.tm-page` 的 grid 声明原样保留；任何 `display:grid` 必须带 `grid-template-columns`；列宽之和超过 1270 的表格有 `fixed="right"`。
- 所有 `data-test` 与被测试钉住的类名不变；`build.spec` 钉住的 Element 样式导入保留；Go 的嵌入测试要求的英文文案不改写。
- 两个语言包键树一致，只追加键，不重排现有块。
- 不提交、不推送。

## 验证

```
cd web && npm test -- --run && npm run build && cd .. && ./scripts/verify-web-embed.sh && git diff --check
go test ./internal/server -run TestEmbeddedWebDist
cd web && node scripts/visual-qa/run.mjs --locale en-US   # 20 张截图，输出到 $TM_QA_OUT
```
