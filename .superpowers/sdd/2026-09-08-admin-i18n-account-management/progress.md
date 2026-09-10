# SDD ledger — plan: docs/superpowers/plans/2026-09-08-admin-i18n-account-management.md

## Pre-flight

| Tasks | Producer / consumer or overlap | Finding |
|---|---|---|
| 1 → 2 | AccountService、AccountRepositories、Schema v6 由 Task 1 提供，Task 2 消费 | 接口一致；管理员变更必须显式传 actorUserID 才能满足可归因审计 |
| 1 → 6 | v5→v6 迁移由 Task 1 实现，Task 6 文档化 | 一致；只允许相邻迁移，缺失更旧链时快速失败 |
| 2 → 4 | 账号 API 由 Task 2 提供，前端账号页由 Task 4 消费 | 一致 |
| 2 → 5 | Dashboard summary API 由 Task 2 提供，Task 5 消费 | 一致 |
| 3 → 4 | i18n、AppShell、路由元数据由 Task 3 提供，Task 4 消费 | 一致 |
| 3 → 5 | 翻译资源与公共 UI 组件由 Task 3 提供，Task 5 扩展 | 一致 |
| 4 ↔ 5 | API client 与两套 locale 文件均会修改 | 顺序执行，Task 5 在 Task 4 结果上增量扩展 |
| 5 → 6 | 前端最终构建产物由 Task 5 形成，Task 6 同步 embed | 一致 |
| 1 | 测试、迁移、Repository、Service 的内部一致性 | 原计划残留“资源依赖检查”，已删除；逻辑删除不阻止关联资源存在 |
| 2 | API 测试与 Handler/Service 边界 | 一致；Handler 传 actorUserID，Service 写审计 |
| 3 | i18n 测试与 AppShell 实现 | 一致 |
| 4 | 账号页面测试与一次性密码内存语义 | 一致 |
| 5 | 现有页面迁移测试与 Dashboard 数据源 | 一致 |
| 6 | 文档、embed 校验与发布门禁 | 一致 |

Ruling: 用户已明确允许在当前 main 分支开发，因此不创建 worktree；若判断错误，代价是变更与当前未提交工作混合，但所有修改保持未提交且可逐文件审查。

Ruling: 用户未授权 commit/push/merge，覆盖技能默认的按任务提交流程；任务审查使用任务范围文件和 working-tree diff，若判断错误，代价是缺少按提交隔离的审查范围，但不会产生未经授权的 Git 历史。

Ruling: 管理员账号操作方法显式接收 actorUserID，以满足 Service 层事务内审计归因；若判断错误，代价是 Task 2 Handler 需多传一个已有 Principal 字段，但不改变 HTTP API。

Ruling: v5 以前数据库没有完整相邻增量链，auto-init 不再伪装升级而是报告缺失迁移；若判断错误，代价是旧测试/旧库不能直接跃迁，但符合项目“禁止跳版本、链不完整快速失败”的数据库规范。

Task 1: review infrastructure failed twice before producing a verdict; controller performed the task-scoped review and added a RED test for wrong existing migration index before fixing it.

Task 1: complete (working-tree changes, no commits by user instruction; focused auth/storage tests and `go test ./... -count=1 -vet=off` pass).
