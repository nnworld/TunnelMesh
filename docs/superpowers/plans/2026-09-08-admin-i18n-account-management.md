# TunnelMesh 管理后台国际化与账号管理 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在保留 Element Plus 的前提下，交付中文/英文管理后台、浅色 SaaS App Shell、当前用户改密和管理员普通子账号管理能力。

**Architecture:** 后端新增 `AccountService`，由 API Handler 做协议和 RBAC 边界，Service 编排 Argon2id、账号状态、逻辑删除/恢复、事务和审计，Repository 负责 cursor、锁和 SQL。前端新增 `vue-i18n`、统一 App Shell 和账号视图，所有现有页面迁移到同一套翻译资源与视觉 token。

**Tech Stack:** Go 1.23、SQLite/MySQL、标准库 `net/http`、Argon2id、Vue 3、TypeScript、Element Plus、Pinia、Vue Router、Vite、Vitest、vue-i18n。

**Spec:** `docs/superpowers/specs/2026-09-08-admin-i18n-account-management-design.md`

## Global Constraints

- 保留 Vue 3、TypeScript、Element Plus、Pinia 和 Vue Router，不引入 Ant Design Vue。
- 支持 `zh-CN` 和 `en-US`；语言优先级为 localStorage 明确选择、`navigator.languages` 中的 `zh-*`、英文 fallback。
- 当前用户和管理员重置密码后不撤销已有登录 Token。
- 子账号固定为 `role=user`；管理员接口不得修改角色或操作管理员账号。
- 临时密码只返回一次，响应必须设置 `Cache-Control: no-store`，不得写日志、URL、Pinia 或 localStorage。
- 删除普通账号采用逻辑删除，设置 `deleted_at` 和 `disabled=1` 并保留全部关联资源；恢复清空 `deleted_at` 并设置 `disabled=0`。
- API 前缀为 `/api/v1`，响应统一 `{ code, msg, data }`，列表使用 cursor 分页。
- Schema 从 v5 升到 v6；必须同步全量 DDL 和 `migrations/incremental/v0005_to_v0006/{mysql,sqlite}.sql`。
- 所有账号操作审计但不得记录密码、密码哈希、Authorization、DSN 或临时密码。
- Handler → Service → Repository 分层；Handler 不直接执行 SQL。
- 修改前端后运行 `npm test -- --run`、`npm run build`，并同步 `internal/server/web_dist`。
- Go 验证命令为 `go test ./... -count=1`、`go test -race ./...`、`go vet ./...`、`git diff --check`。
- 未经用户明确授权不执行 commit、push、merge 或删除远程数据。

## File Map

- Create `internal/auth/account_service.go`: 用户名/密码策略、账号生命周期、逻辑删除/恢复和事务编排。
- Create `internal/auth/account_service_test.go`: AccountService 单元和并发测试。
- Modify `internal/storage/repository.go`: 增加账号状态筛选、逻辑删除/恢复和可组合事务所需接口。
- Create `internal/storage/account_repository.go`: SQLite/MySQL 账号状态筛选、逻辑删除和恢复 SQL。
- Create `migrations/incremental/v0005_to_v0006/mysql.sql`, `migrations/incremental/v0005_to_v0006/sqlite.sql`: `users.deleted_at` 增量迁移。
- Modify `migrations/ddl.sql`, `migrations/embed.go`, `internal/storage/db.go`: Schema v6 全量和增量执行。
- Modify `internal/storage/db.go`: 提供绑定 User/Token/Agent/资源 Repository 的账号事务。
- Create `internal/server/account_api.go`: 账号 HTTP Handler 和错误映射。
- Modify `internal/server/api.go`: 路由 `/auth/password`、`/users`、Dashboard summary。
- Modify `internal/server/api_test.go` and create `internal/server/account_api_test.go`: API 权限、事务、响应头和敏感信息测试。
- Modify `docs/api/openapi.yaml`, `docs/README.md`, `docs/user-guide/server-admin.md`, `docs/operations/troubleshooting.md`。
- Modify `web/package.json`, `web/package-lock.json`: 添加 `vue-i18n`。
- Create `web/src/i18n/index.ts`, `web/src/i18n/messages/zh-CN.ts`, `web/src/i18n/messages/en-US.ts`。
- Create `web/src/stores/preferences.ts`、`web/src/layouts/AppShell.vue`、`web/src/components/PageHeader.vue`、`web/src/components/StatusTag.vue`、`web/src/components/OneTimePasswordDialog.vue`。
- Create `web/src/views/Users.vue`, `web/src/views/AccountSecurity.vue`。
- Modify `web/src/main.ts`, `web/src/App.vue`, `web/src/router.ts`, `web/src/stores/auth.ts`, `web/src/api/client.ts` and all existing `web/src/views/*.vue`。
- Modify/create `web/src/styles/tokens.css`, `web/src/tests/i18n.spec.ts`, `web/src/tests/accounts.spec.ts`, `web/src/tests/shell.spec.ts`。
- Regenerate `web/dist/` and sync `internal/server/web_dist/` after frontend changes.

---

### Task 1: Account domain service and storage contracts

**Files:**

- Create: `internal/auth/account_service.go`
- Create: `internal/auth/account_service_test.go`
- Modify: `internal/auth/service.go`
- Modify: `internal/storage/repository.go`
- Modify: `internal/storage/db.go`
- Create: `internal/storage/account_repository.go`
- Test: `internal/storage/account_repository_test.go`

**Interfaces:**

- Consumes: `storage.UserRepository`, `storage.TokenRepository`, `storage.AuditRepository`, `auth.VerifyPassword`, `auth.HashPassword`。
- Produces: `NewAccountService(source any, repos ...any) *AccountService`; `CreateChild(ctx context.Context, actorUserID, username string) (storage.User, string, error)`; `ListChildren(ctx context.Context, status, cursor string, limit int) (storage.Page[storage.User], error)`; `SetDisabled(ctx context.Context, actorUserID, userID string, disabled bool) (storage.User, error)`; `ResetPassword(ctx context.Context, actorUserID, userID string) (storage.User, string, error)`; `ChangeOwnPassword(ctx context.Context, userID, currentPassword, newPassword string) (storage.User, error)`; `DeleteChild(ctx context.Context, actorUserID, userID string) (storage.User, error)`; `RestoreChild(ctx context.Context, actorUserID, userID string) (storage.User, error)`; and `DB.AccountTransaction(ctx context.Context, fn func(storage.AccountRepositories) error) error`。管理员操作显式传入 `actorUserID`，以便 Service 在同一事务内写入可归因审计记录。

- [ ] **Step 1: Write failing service tests.** Add tests for usernames shorter than 3 or longer than 64, invalid characters, 12-character password boundary, same old/new password rejection, child role fixed to `user`, administrator protection, temporary password generation, logical delete/restore, and audit detail redaction. Add tests that `ChangeOwnPassword` leaves an existing API token valid and logical deletion makes it invalid.

- [ ] **Step 2: Run the service tests and verify red.**

  ```bash
  go test ./internal/auth -run 'TestAccountService' -count=1 -vet=off
  ```

  Expected failure: `AccountService` and its methods are undefined.

- [ ] **Step 3: Write failing repository and migration tests.** Test v5→v6 for SQLite, assert MySQL 5.6-compatible incremental SQL, and cover child listing with `active`/`deleted`/`all`, logical deletion and restoration without removing related rows.

- [ ] **Step 4: Run repository tests and verify red.**

  ```bash
  go test ./internal/storage -run 'Test(Account|User)' -count=1 -vet=off
  ```

  Expected failure: missing account resource repository and transaction methods.

- [ ] **Step 5: Implement migration and repository contracts.** Add `users.deleted_at`, embed both v5→v6 scripts, apply only the driver-specific adjacent migration before advancing `schema_meta.version`, and implement cursor-filtered users (`role=user`) for active/deleted/all states. Add `DB.AccountTransaction(ctx, fn)` that binds user and audit repositories to one transaction and acquires the same schema metadata writer fence used by authentication transactions.

- [ ] **Step 6: Implement `AccountService`.** Validate and normalize usernames, use `crypto/rand` for temporary passwords, hash with Argon2id, reject administrator targets, preserve existing tokens on password changes, set `deleted_at` plus `disabled=1` on logical deletion, clear `deleted_at` plus `disabled=0` on restore, and emit redacted audit details.

- [ ] **Step 7: Run focused tests and refactor only after green.**

  ```bash
  go test ./internal/auth ./internal/storage -run 'Test(Account|User)' -count=1 -vet=off
  ```

  Expected result: all new service and repository tests pass with no password/hash in captured audit details.

### Task 2: Account and dashboard HTTP APIs

**Files:**

- Create: `internal/server/account_api.go`
- Create: `internal/server/account_api_test.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/api_test.go`
- Modify: `docs/api/openapi.yaml`

**Interfaces:**

- Consumes: Task 1 `AccountService`, `DB.AccountTransaction`, existing `authenticate`, `isAdmin`, `writeJSON`, `writeAPIError`.
- Produces: `PUT /api/v1/auth/password`, `GET/POST/PATCH/DELETE /api/v1/users`, `POST /api/v1/users/{id}/reset-password`, `POST /api/v1/users/{id}/restore`, `GET /api/v1/dashboard/summary`.

- [ ] **Step 1: Write failing API tests.** Cover unauthenticated requests, non-admin access to `/users`, admin protection, malformed JSON, stable 400/401/403/404 mappings, active/deleted/all cursor pagination, restore, `Cache-Control: no-store` on create/reset, and absence of `passwordHash`, passwords, DSNs and Authorization values in response bodies. Add a dashboard test proving admin/global and user/owner-filtered counts.

- [ ] **Step 2: Run API tests and verify red.**

  ```bash
  go test ./internal/server -run 'Test(Account|User|Dashboard)' -count=1 -vet=off
  ```

  Expected failure: routes and handlers are not registered.

- [ ] **Step 3: Implement route dispatch and handlers.** Keep JSON decoding and RBAC in Handler; call Service for all mutations. Set `Cache-Control: no-store` before create/reset responses. Return public user fields only and map domain error identifiers to the documented status/message envelope.

- [ ] **Step 4: Implement dashboard aggregation.** Add repository-backed counts with owner filtering and a bounded recent-event query. Do not count by loading the first cursor page in Handler.

- [ ] **Step 5: Update OpenAPI and run focused tests.**

  ```bash
  go test ./internal/server -run 'Test(Account|User|Dashboard)' -count=1 -vet=off
  ```

  Expected result: all API tests pass and OpenAPI contains every new operation, request schema, response schema and 400/401/403/404/409 behavior.

### Task 3: Frontend internationalization and App Shell

**Files:**

- Modify: `web/package.json`, `web/package-lock.json`
- Create: `web/src/i18n/index.ts`
- Create: `web/src/i18n/messages/zh-CN.ts`
- Create: `web/src/i18n/messages/en-US.ts`
- Create: `web/src/stores/preferences.ts`
- Create: `web/src/layouts/AppShell.vue`
- Create: `web/src/components/PageHeader.vue`
- Create: `web/src/components/StatusTag.vue`
- Create: `web/src/styles/tokens.css`
- Modify: `web/src/main.ts`, `web/src/App.vue`, `web/src/router.ts`, `web/src/stores/auth.ts`
- Create: `web/src/tests/i18n.spec.ts`, `web/src/tests/shell.spec.ts`

**Interfaces:**

- Consumes: `vue-i18n`, Element Plus locale modules, existing auth store and router.
- Produces: `i18n`, `usePreferencesStore`, `AppShell`, typed message keys, responsive layout and locale-aware date formatter.

- [ ] **Step 1: Write failing frontend tests.** Assert `zh-CN` detection for `zh-CN`/`zh-Hant`, English fallback, localStorage precedence, language persistence, matching key sets between locale files, and shell route/menu metadata. Assert admin-only users menu and mobile drawer classes exist.

- [ ] **Step 2: Run frontend tests and verify red.**

  ```bash
  cd web && npm test -- --run src/tests/i18n.spec.ts src/tests/shell.spec.ts
  ```

  Expected failure: i18n store, message files and AppShell are absent.

- [ ] **Step 3: Add `vue-i18n` and locale resources.** Implement supported locale normalization, localStorage key `tunnelmesh_locale`, browser detection, and Element Plus `zhCn`/`en` synchronization. Keep the locale files structurally identical.

- [ ] **Step 4: Implement AppShell and design tokens.** Replace the bare container with the confirmed light SaaS layout: `#f5f7fa` page background, white top bar/sidebar/cards, blue primary action, compact spacing, breadcrumb/title/action header, responsive drawer, user menu, language switcher and account-security link.

- [ ] **Step 5: Migrate router and auth bootstrap.** Add `/users` with `meta.admin`, `/account/security` with `meta.auth`, preserve login redirect behavior, and initialize locale before mount without reading credentials into UI preferences.

- [ ] **Step 6: Run focused tests and type/build checks.**

  ```bash
  cd web && npm test -- --run src/tests/i18n.spec.ts src/tests/shell.spec.ts
  npm run build
  ```

  Expected result: locale/shell tests pass and Vite build succeeds.

### Task 4: Frontend account and password pages

**Files:**

- Modify: `web/src/api/client.ts`
- Create: `web/src/components/OneTimePasswordDialog.vue`
- Create: `web/src/views/Users.vue`
- Create: `web/src/views/AccountSecurity.vue`
- Create: `web/src/tests/accounts.spec.ts`
- Modify: `web/src/i18n/messages/zh-CN.ts`, `web/src/i18n/messages/en-US.ts`

**Interfaces:**

- Consumes: Task 2 API paths and Task 3 i18n/AppShell.
- Produces: typed `listUsers`, `createUser`, `updateUserStatus`, `resetUserPassword`, `deleteUser`, `changePassword` client functions and account views.

- [ ] **Step 1: Write failing frontend tests.** Test API paths/methods, admin-only users route, create/reset one-time password display and cleanup on close/unmount, password form validation, no token clearing after password change, active/deleted filtering, confirmation before disable/delete, restore, and localized success/error messages.

- [ ] **Step 2: Run tests and verify red.**

  ```bash
  cd web && npm test -- --run src/tests/accounts.spec.ts
  ```

  Expected failure: account API functions, views and one-time password dialog are absent.

- [ ] **Step 3: Implement typed API client methods.** Use existing `api<T>` envelope handling; include cursor and limit for users, never write temporary passwords to storage, and preserve the auth token after `changePassword` succeeds.

- [ ] **Step 4: Implement `Users.vue`.** Add page header, active/deleted/all filter, cursor table, status tags, add-child dialog, reset-password action, enable/disable confirmation, logical delete confirmation, restore action, loading/empty/error states and one-time password dialog.

- [ ] **Step 5: Implement `AccountSecurity.vue`.** Add current/new/confirm password form, client-side length and equality checks, submit loading state, localized server errors, and success feedback without logout.

- [ ] **Step 6: Run focused tests and build.**

  ```bash
  cd web && npm test -- --run src/tests/accounts.spec.ts
  npm run build
  ```

  Expected result: account tests pass and temporary password state is cleared whenever the dialog closes or the view unmounts.

### Task 5: Migrate existing pages and Dashboard summary

**Files:**

- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/Dashboard.vue`, `Agents.vue`, `AgentDetail.vue`, `Routes.vue`, `Tunnels.vue`, `Tokens.vue`, `AuditLogs.vue`, `Login.vue`
- Modify: `web/src/i18n/messages/zh-CN.ts`, `web/src/i18n/messages/en-US.ts`
- Modify: `web/src/tests/routes.spec.ts`
- Create: `web/src/tests/views.spec.ts`

**Interfaces:**

- Consumes: Task 2 dashboard endpoint and Task 3 `PageHeader`, `StatusTag`, translation keys.
- Produces: fully localized existing pages with consistent loading, empty, error, table and action styles.

- [ ] **Step 1: Write failing migration tests.** Assert every existing view uses translation keys for user-visible labels, Dashboard calls `/dashboard/summary`, failures render unavailable/retry state instead of zero/static values, and router paths remain protected.

- [ ] **Step 2: Run tests and verify red.**

  ```bash
  cd web && npm test -- --run src/tests/routes.spec.ts src/tests/views.spec.ts
  ```

  Expected failure: existing views contain hard-coded English and Dashboard has no summary request.

- [ ] **Step 3: Add summary client and migrate Dashboard.** Define the summary response type, render four/five statistic cards and recent events from server data, and display a localized retry action on failure.

- [ ] **Step 4: Migrate each existing view.** Replace literal labels, buttons, statuses, empty descriptions, errors and confirmation text with `t(...)`; adopt PageHeader/cards/tables/status tags and responsive overflow without changing domain behavior.

- [ ] **Step 5: Migrate Login and language switch.** Add locale switch before authentication, localized validation and invalid-credential handling, and accessible labels/autocomplete attributes.

- [ ] **Step 6: Run frontend tests and build.**

  ```bash
  cd web && npm test -- --run
  npm run build
  ```

  Expected result: all frontend tests pass and no existing page renders untranslated user-visible English literals.

### Task 6: Documentation, embed synchronization and release verification

**Files:**

- Modify: `docs/api/openapi.yaml`, `docs/README.md`, `docs/user-guide/server-admin.md`, `docs/operations/configuration.md`, `docs/operations/troubleshooting.md`, `docs/deployment/frontend.md`
- Regenerate: `web/dist/`
- Synchronize: `internal/server/web_dist/`
- Create: `scripts/verify-web-embed.sh`
- Test: `internal/server/web_test.go`, `scripts/verify-web-embed.sh`

**Interfaces:**

- Consumes: Tasks 1–5 public behavior and built frontend assets.
- Produces: operator documentation, API reference, reproducible embed bundle and release verification evidence.

- [ ] **Step 1: Write failing embed/documentation checks.** Add a check that `web/dist` and `internal/server/web_dist` have identical file lists and bytes, and grep docs for language switching, password change, child account lifecycle, temporary password one-time display, and no-Token-revocation behavior.

- [ ] **Step 2: Run checks and verify red after frontend changes.**

  ```bash
  cd web && npm run build
  cd .. && git diff --check
  ```

  Expected failure: embed bundle differs until synchronization is performed.

- [ ] **Step 3: Update operator and user docs.** Document browser-language detection, manual switch, `/users`, `/account/security`, password semantics, logical deletion/restoration, API authorization, v5→v6 migration and rollback-by-retaining-column behavior.

- [ ] **Step 4: Synchronize embed output.** Copy the exact contents of `web/dist/` into `internal/server/web_dist/` using the documented workflow, then compare file lists and bytes.

- [ ] **Step 5: Run the complete verification gate.**

  ```bash
  go test ./... -count=1
  go test -race ./...
  go vet ./...
  git diff --check
  cd web && npm test -- --run && npm run build
  cd .. && go build ./cmd/tunnelmesh-server ./cmd/tunnelmesh-agent ./cmd/tunnelmesh-client
  ```

  Expected result: every command exits 0, frontend embed files match, and no generated bundle contains credentials or tokens.

## Plan Self-Review

- Spec coverage: language detection/switching is covered by Tasks 3 and 5; password behavior by Tasks 1, 2 and 4; child account lifecycle and resource-safe deletion by Tasks 1, 2 and 4; UI style by Tasks 3 and 5; Dashboard accuracy by Tasks 2 and 5; docs/embed/release by Task 6.
- Placeholder scan: no `TBD`, `TODO` or unspecified implementation step is required; every task names files, interfaces, tests, commands and expected outcomes.
- Type consistency: Task 1 produces `AccountService` and `DB.AccountTransaction`; Task 2 consumes those names; Task 3 produces the i18n/AppShell names consumed by Tasks 4 and 5; Task 2 summary response is consumed by Task 5.
- Migration check: Task 1 updates full DDL, immutable v5→v6 MySQL/SQLite scripts and `SchemaVersion` together.
- Security check: temporary passwords are one-time/no-store, existing tokens remain valid after password changes, disabled users are rejected by existing token validation, and audit data excludes secrets.
