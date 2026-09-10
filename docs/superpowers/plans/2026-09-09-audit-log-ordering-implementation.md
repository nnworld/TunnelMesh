# Audit Log Ordering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Return audit logs newest first with deterministic pagination.

**Architecture:** Ordering belongs in the audit repository because audit IDs are random and the existing `(created_at, id)` index already defines the intended stable key. The repository returns an opaque composite `(created_at, id)` cursor and resolves legacy ID-only cursors by looking up the referenced row's creation time.

**Tech Stack:** Go, SQL, SQLite/MySQL, Vitest-compatible repository contract tests.

**Spec:** `docs/superpowers/specs/2026-09-09-token-scope-route-edit-audit-design.md`

## Global Constraints

- Audit list order is `created_at DESC, id DESC`.
- The same timestamp must produce a deterministic order.
- Cursor values remain opaque to API clients.
- Existing ID-only cursors must not strand a user mid-page after upgrade.
- No database schema change is required because `idx_audit_created(created_at, id)` already exists.

---

### Task 1: Repository ordering and stable cursor

**Files:**

- Test: `internal/storage/audit_repository_test.go`
- Modify: `internal/storage/repository.go`

**Interfaces:**

- Consumes: `AuditRepository.List(ctx context.Context, cursor string, limit int) (Page[AuditLog], error)`.
- Produces: `encodeAuditCursor(createdAt time.Time, id string) string`.
- Produces: `(*auditRepo).decodeAuditCursor(ctx context.Context, cursor string) (createdAt string, id string, err error)`.

- [x] **Step 1: Write the failing repository test.**

  `TestAuditRepositoryListsNewestFirstWithStablePagination` creates four records whose ID lexicographic order differs from creation-time order. It asserts:

  ```text
  page 1 = [newest, newer tie with higher ID]
  page 2 = [newer tie with lower ID, oldest]
  legacy ID cursor after the first tie = [lower-ID tie, oldest]
  ```

- [x] **Step 2: Verify the red phase.**

  ```bash
  go test ./internal/storage -run TestAuditRepositoryListsNewestFirstWithStablePagination -count=1
  ```

  Actual failure before the fix:

  ```text
  audit IDs = [a-audit-newest b-audit-tie], want [a-audit-newest m-audit-tie]
  ```

- [x] **Step 3: Implement keyset ordering.**

  Encode cursors as `created_at + NUL + id`, filter with `(created_at < cursor_time OR (created_at = cursor_time AND id < cursor_id))`, and order by `created_at DESC, id DESC`. For a legacy cursor without NUL, load `created_at` by audit ID before applying the same keyset filter.

- [x] **Step 4: Verify the focused test.**

  ```bash
  go test ./internal/storage -run TestAuditRepositoryListsNewestFirstWithStablePagination -count=1
  ```

  Expected result: PASS.

### Task 2: Contract documentation and verification

**Files:**

- Modify: `docs/api/openapi.yaml`
- Modify: `docs/user-guide/server-admin.md`
- Modify: `docs/pull-requests/2026-09-09-token-scope-route-edit-audit.md`

**Interfaces:**

- Consumes: `GET /api/v1/audit-logs?cursor=&limit=`.
- Produces: documented newest-first order and opaque-cursor behavior.

- [x] **Step 1: Document API order.**

  Add `summary: List audit logs newest first` and describe `createdAt DESC, id DESC`, opaque cursors, and legacy-cursor compatibility.

- [x] **Step 2: Document the management UI behavior.**

  Explain in the Server guide that Audit Logs displays newest events first and uses a deterministic ID tie-breaker.

- [x] **Step 3: Run Go verification.**

  ```bash
  go test ./... -count=1
  go test -race ./...
  go vet ./...
  git diff --check
  ```

  Expected result: all commands pass.

## Completion Record

This is a retroactive plan for a bug fix completed in this session. The red-phase output above is the actual observed failure; no earlier red run is claimed. No commit, push, merge, or remote PR was performed.
