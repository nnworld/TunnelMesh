# Admin Console List Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 将管理后台的列表页统一为可分页、可服务端排序、可组合筛选且不泄露敏感数据的交互模式。

**Architecture:** 保留现有 Repository 接口和测试 fake，通过可选的 `ListSorted` 能力向 SQL repository 提供排序；API 层统一校验排序白名单、多选枚举和游标参数。Vue 页面通过 `useListQuery`、`FilterBar`、`ListPageShell` 复用查询状态、筛选布局和游标分页。

**Tech Stack:** Go、SQLite/MySQL、标准库 SQL、Vue 3、TypeScript、Element Plus、Vitest。

**Spec:** 用户提出的“所有管理页面分页、关键字段排序、默认倒序、常用字段模糊查询和多选筛选、合理 UI、写入 AGENTS.md”要求。

## Global Constraints

- API 继续使用 `/api/v1`、`{code,msg,data}` 和 cursor 分页。
- 所有排序列必须来自服务端 allowlist，SQL 不接受用户原始列名。
- token、密码、私钥和 secret 只显示摘要或状态，不进入列表响应和日志。
- 保留旧 Repository 方法，新增排序能力使用可选接口，避免破坏既有 fake。
- 默认排序为业务时间倒序并以 `id` 稳定收尾；不支持排序的旧接口继续按既有稳定顺序返回。
- 前端列表默认每页 20 条，可选 50/100；筛选通过逗号分隔多选值传输。

---

### Task 1: Complete sorted repository capabilities

**Files:**
- Modify: `internal/storage/repository.go`
- Modify: `internal/storage/credential_repository.go`
- Modify: `internal/storage/remote_server_repository.go`
- Modify: `internal/storage/vpn_peer_repository.go`
- Modify: `internal/storage/identity_repository.go`
- Modify: `internal/storage/sort.go`
- Test: `internal/storage/*_repository_test.go`

- [ ] Add optional sorted repository interfaces with exact signatures for users, service tokens, tunnels, nodes, credentials, remote servers, VPN peers, and OIDC providers.
- [ ] Add `Sort SortSpec` and bounded multi-value fields to filters where an existing filter already exists.
- [ ] Implement allowlisted `ORDER BY`, composite cursor predicates, and matching next cursors in SQL repositories.
- [ ] Add tests proving descending default, ascending order, tie-breaking by id, and cursor continuation for each changed repository.

### Task 2: Complete API query contracts

**Files:**
- Modify: `internal/server/account_api.go`
- Modify: `internal/server/token_api.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/server_node_api.go`
- Modify: `internal/server/credential_api.go`
- Modify: `internal/server/remote_server_api.go`
- Modify: `internal/server/vpn_peer_api.go`
- Modify: `internal/server/sso_api.go`
- Modify: `internal/server/listquery.go`
- Test: `internal/server/*_api_test.go`, `internal/server/listquery_test.go`

- [ ] Parse keyword, comma-separated multi-select status/type/role/protocol fields with length bounds and single-value compatibility.
- [ ] Return 422 with allowed values for invalid sort/order and enum values.
- [ ] Forward `ListSort.Spec()` to optional repository capabilities and retain legacy fallback behavior.
- [ ] Add handler tests for sorting, filtering, cursor consistency, validation errors, and sensitive-field omission.

### Task 3: Standardize remaining list pages

**Files:**
- Modify: `web/src/api/client.ts`, `web/src/api/auth.ts`, `web/src/api/credentials.ts`, `web/src/api/remote-servers.ts`, `web/src/api/vpn.ts`
- Modify: `web/src/views/Users.vue`, `web/src/views/Tokens.vue`, `web/src/views/Routes.vue`, `web/src/views/Servers.vue`
- Modify: `web/src/views/Credentials.vue`, `web/src/views/RemoteServers.vue`, `web/src/views/VpnPeers.vue`, `web/src/views/SSOProviders.vue`
- Modify: `web/src/components/ListPageShell.vue`, `web/src/components/FilterBar.vue`, `web/src/composables/useListQuery.ts`
- Test: `web/src/**/*.test.ts`

- [ ] Replace load-more and one-off toolbars with shared cursor pager and page-size controls.
- [ ] Add sortable table headers for key fields, defaulting to descending business time.
- [ ] Add primary keyword/status filters and a “more filters” popover with multi-select controls.
- [ ] Keep long forms in dialogs or sections; do not widen list rows with secrets or verbose metadata.
- [ ] Add focused component/composable tests for query serialization, reset, cursor navigation, sorting, and filter rendering.

### Task 4: Documentation and index

**Files:**
- Modify: `docs/api/openapi.yaml`
- Modify: `docs/operations/schema-upgrades.md`
- Modify: relevant `docs/user-guide/` list/help pages
- Modify: `docs/README.md` via `python3 scripts/gen_doc_index.py`

- [ ] Document `sort`, `order`, cursor semantics, supported multi-select filters, and 422 errors.
- [ ] Document Schema v17 indexes, preflight checks, rollback, and five-minute stop-loss procedure.
- [ ] Rebuild the generated documentation index and verify no orphan entry is introduced.

### Task 5: Full verification

- [ ] Run `go test ./... -count=1`.
- [ ] Run `go test -race ./...`.
- [ ] Run `go vet ./...`.
- [ ] Run `git diff --check`.
- [ ] Run `cd web && npm test -- --run`.
- [ ] Run `cd web && npm run build`.
