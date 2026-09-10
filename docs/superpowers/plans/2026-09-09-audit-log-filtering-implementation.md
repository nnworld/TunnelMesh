# Audit Log Filtering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add server-side audit-log filtering to the management console.

**Architecture:** The API decodes and validates query filters, the audit repository pushes every filter into SQL, and pagination continues to use the existing `(createdAt, id)` keyset. The Vue page keeps a filter form above the table and explicitly applies filters with the query action.

**Tech Stack:** Go `net/http`, SQL repositories, Vue 3, TypeScript, Element Plus, Vitest.

**Spec:** `docs/superpowers/specs/2026-09-09-token-scope-route-edit-audit-design.md`

## Global Constraints

- Filtering happens server-side so older pages are not incorrectly excluded.
- Text filters are exact matches after trimming.
- `createdFrom` and `createdTo` are inclusive and use RFC3339 date-time values.
- `createdFrom` must not be later than `createdTo`.
- Ordering remains `created_at DESC, id DESC`.
- The API is admin-only and returns no secrets.
- No database schema change is required.

---

### Task 1: Repository filter contract

**Files:**

- Test: `internal/storage/audit_repository_test.go`
- Modify: `internal/storage/models.go`
- Modify: `internal/storage/repository.go`

**Interfaces:**

- Produces: `AuditFilter{ActorUserID, Action, ResourceType, ResourceID string; CreatedFrom, CreatedTo *time.Time}`.
- Produces: `AuditRepository.List(ctx context.Context, filter AuditFilter, cursor string, limit int) (Page[AuditLog], error)`.

- [x] **Step 1: Write the failing repository test.**

  `TestAuditRepositoryAppliesFiltersWithStablePagination` creates target and noise records, applies all five filters, and asserts the first filtered page and its cursor.

- [x] **Step 2: Verify the red phase.**

  ```bash
  go test ./internal/storage -run TestAuditRepositoryAppliesFiltersWithStablePagination -count=1
  ```

  Actual failure before implementation: `AuditFilter` is undefined and `List` does not accept a filter argument.

- [x] **Step 3: Implement SQL filter pushdown.**

  Add exact-match conditions for actor, action, resource type, and resource ID; add inclusive `created_at` bounds; combine them with the keyset cursor condition and retain `created_at DESC, id DESC`.

- [x] **Step 4: Verify the focused repository test.**

  ```bash
  go test ./internal/storage -run TestAuditRepositoryAppliesFiltersWithStablePagination -count=1
  ```

  Expected result: PASS.

### Task 2: API query contract

**Files:**

- Test: `internal/server/api_test.go`
- Modify: `internal/server/api.go`
- Modify: `docs/api/openapi.yaml`

**Interfaces:**

- Consumes: `AuditFilter` and `AuditRepository.List`.
- Produces: `GET /api/v1/audit-logs` query parameters `actorUserId`, `action`, `resourceType`, `resourceId`, `createdFrom`, and `createdTo`.

- [x] **Step 1: Write the failing API test.**

  `TestAuditAPIFiltersAndValidatesTimeRange` verifies that all filters exclude noise, invalid timestamps return `400`, and a reversed time range returns `400`.

- [x] **Step 2: Verify the red phase.**

  ```bash
  go test ./internal/server -run TestAuditAPIFiltersAndValidatesTimeRange -count=1
  ```

  Actual failure before implementation: all records are returned because query filters are ignored.

- [x] **Step 3: Implement query decoding and validation.**

  Trim text filters, parse both time filters with `time.RFC3339Nano`, reject parse errors and reversed ranges, then pass the filter to the service.

- [x] **Step 4: Update OpenAPI.**

  Document all six optional filters, inclusive time semantics, exact text matching, and the `400` response.

- [x] **Step 5: Verify the focused API test.**

  ```bash
  go test ./internal/server -run TestAuditAPIFiltersAndValidatesTimeRange -count=1
  ```

  Expected result: PASS.

### Task 3: Management UI filter form

**Files:**

- Test: `web/src/tests/audit-logs.spec.ts`
- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/AuditLogs.vue`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`

**Interfaces:**

- Produces: `AuditLogFilter` and `listAuditLogs(params: AuditLogFilter & { cursor?: string; limit?: number })`.
- Produces: `auditFilter()`, `search()`, and `resetFilters()` in `AuditLogs.vue`.

- [x] **Step 1: Write the failing frontend tests.**

  Assert that all filter values are serialized into the API URL and that the view contains the filter card, datetime range, reset/query actions, and right-aligned action area.

- [x] **Step 2: Verify the red phase.**

  ```bash
  npm --prefix web test -- --run src/tests/audit-logs.spec.ts
  ```

  Actual failures before implementation: the API URL only contained `limit=100`, and the view had no filter card.

- [x] **Step 3: Implement the typed API client.**

  Add `AuditLogFilter`, serialize only non-empty values, and preserve cursor and limit parameters.

- [x] **Step 4: Implement the filter form.**

  Add a light card above the table with a datetime range and four exact-match inputs. Place Reset and Search at the bottom right; Search reloads from the first page and Reset clears all fields before reloading.

- [x] **Step 5: Verify the focused frontend test.**

  ```bash
  npm --prefix web test -- --run src/tests/audit-logs.spec.ts
  ```

  Expected result: PASS.

### Task 4: Full verification

**Files:**

- Modify: `docs/user-guide/server-admin.md`
- Modify: `docs/pull-requests/2026-09-09-token-scope-route-edit-audit.md`
- Modify: `internal/server/web_dist/` after building web assets.

- [x] **Step 1: Update user and PR documentation.**

  Document the five filters, query/reset behavior, server-side semantics, and inclusive time range.

- [x] **Step 2: Run full Go verification.**

  ```bash
  go test ./... -count=1
  go test -race ./...
  go vet ./...
  git diff --check
  ```

  Expected result: all commands pass.

- [x] **Step 3: Run full frontend verification and refresh embedded assets.**

  ```bash
  npm --prefix web test -- --run
  npm --prefix web run build
  rsync -a --delete web/dist/ internal/server/web_dist/
  ./scripts/verify-web-embed.sh
  ```

  Expected result: all frontend tests and the production build pass, and embedded assets verify.

## Completion Record

This is a retroactive plan for a bounded feature approved and completed in this session. The red-phase failures above are the actual observed results. No commit, push, merge, or remote PR was performed.
