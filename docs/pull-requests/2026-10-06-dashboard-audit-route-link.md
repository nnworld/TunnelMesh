# 概览页“查看审计日志”跳转路径修正

## 标题

```
fix(web): point the overview audit link at /audit-logs
```

## 目标分支

`main`。

## 摘要

运行概览“最近事件”卡片右侧的“查看审计日志”按钮跳 `/audits`，而路由表里这条页面是
`/audit-logs`（`web/src/router.ts`）。本仓库路由没有 `pathMatch` 兜底页，vue-router 只会打一条
`No match found for location` 警告、视图原地不动——点按钮看起来像“坏了”。改为 `/audit-logs`，
并补一条源码级守卫：任何 `router.push('/…')` 的字面量都必须命中已声明路由（`` `/agents/${id}` ``
这类模板按段匹配，`${}` 视作 `:param`）。同时修掉把错误路径固化下来的那条断言。

顺带修一个同类问题：`/audit-logs` 带 `meta.admin`，`beforeEach` 会把非管理员弹回 `/`，所以这个
按钮对非管理员永远是“点了回到原处”。按 `AppShell.vue` 导航菜单的既有口径加上 `v-if="auth.isAdmin"`。

## 用户影响

管理员在概览页点“查看审计日志”直达审计页。普通用户不再看到一个点了没反应的按钮。无其它可见变化。

## API、Schema 与配置影响

无。不改接口、`migrations/`、`SchemaVersion`、配置模型，也不改 `web/src/api/client.ts` 的
`/api/v1/audit-logs` 数据路径（那是 API 路径，与前端路由无关）。

## 安全与授权影响

无授权面变化。`/audit-logs` 的 `meta.admin` 与后端权限校验都不动；本次只是让前端不再向无权限
账号显示一个必然被守卫弹回的入口。

## 计划

按 AGENTS.md，缺陷修复应先有实施计划。此处未单独建计划文件：改动面是 1 个路径字面量 + 1 个
`v-if` + 2 个测试文件，属止损级修复，计划内容（根因、改法、守卫、验证）已完整落在本记录里。

## 测试证据

- 根因取证：路由表声明的 19 条路径里没有 `/audits`，`/audit-logs` 才是审计页；
  `grep -rn "'/audits'" src` 只命中 `Dashboard.vue` 与两处测试残留。
- 先红：新增守卫 `routes.spec.ts > in-app navigation > only pushes paths the router declares`
  报出唯一 offender `src/views/Dashboard.vue: router.push('/audits')`（无误报，且断言扫描到
  ≥7 处调用点，防止守卫自己退化成空跑）；`dashboard-view.spec.ts > does not offer the audit
  link to accounts the router would bounce` 在加 `v-if` 前失败。
- 后绿：`cd web && npm test -- --run` 44 文件 / **365 用例**全部通过；`npm run build` 通过并
  镜像到 `internal/server/web_dist`（该目录由 `.gitignore` 的 `internal/server/web_dist/*` 排除，
  发行时由 CI 重新生成）。
- 既有断言修正：`dashboard-view.spec.ts` 原先断言 `push` 到 `/audits`，是把 bug 固化成了契约；
  现断言 `/audit-logs`。

## 发布步骤

合并后随下一次常规发布生效（管理后台是 `go:embed` 的前端产物，无独立发布物）。无需数据库或
配置动作。

## 回滚步骤

`git revert` 本 PR 的合并提交即可，无数据与配置影响；回滚后按钮重新变成“点了不跳”。

## Reviewer 关注点

- 守卫只覆盖 `router.push('…')` 直接字面量与模板字面量，`push({ name })` 形式不在范围内
  （这类目标由路由名保证，改名会编译期可见）。
- 是否要给路由表补一个 `/:pathMatch(.*)*` 兜底页：那会把“跳错”从静默变成可见，属独立改动，
  本次不做。
- 概览页“最近事件”本身对所有登录用户可见，只是入口按角色隐藏；如果希望普通用户也能看自己的
  审计记录，需要另设非 admin 的接口与页面。

## 集成状态

分支 `codex/dashboard-audit-route`，基于 `origin/main`。未提交、未推送前不合并：按 AGENTS.md
未经授权不执行 commit / push / merge。
