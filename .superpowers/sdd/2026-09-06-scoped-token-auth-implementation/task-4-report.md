# Task 4 Report: Token management API, lifecycle audit, and OpenAPI

## Status

Implemented the scoped service Token management API with management-session authentication, admin/owner authorization, cursor pagination, redacted metadata, dynamic status, atomic create/rotate/revoke audit persistence, safe idempotency replay, concurrent same-key fencing, and OpenAPI documentation. No commit, push, or merge was performed.

## Implementation

- Added `GET/POST /api/v1/tokens`, `GET /api/v1/tokens/{id}`, and `POST /api/v1/tokens/{id}/rotate|revoke` routing under the existing `/api/v1` management authentication boundary.
- Kept transport decoding and owner/admin decisions in `token_api.go`; lifecycle planning, redacted views, dynamic status, idempotency replay, audit construction, and transaction orchestration live in `token_service.go`.
- Reused `auth.CredentialService` validation and secret generation through a non-persisting planning repository. Only transaction-bound storage repositories write the real Token lifecycle facts.
- Added `DB.ServiceTokenMutationTransaction`, which injects `ServiceTokenRepository`, `AuditRepository`, and `IdempotencyRepository` bound to the same SQL transaction. The Task 2 `ServiceTokenTransaction` signature remains compatible and delegates to the stronger entry point.
- Create and rotate claim/update their idempotency record in the same transaction as Token mutation and audit. Stored replay metadata contains operation/request digest and redacted Token metadata only; it never contains plaintext secret or Token hash.
- First create/rotate response includes the one-time `secret` and `replayed=false`; same-key replay returns identical Token ID/metadata with no `secret` and `replayed=true`.
- Concurrent same-key create/rotate requests produce one secret-bearing winner. Other requests receive the completed non-secret replay or an explicit in-progress conflict; they never create another Token.
- Enforced non-admin rules: caller-owned enabled Agent binding for `agent`, only caller-owned enabled scoped Agents for `client`, and admin-only `server_node`. Admins may create all types.
- Lists/details never expose secret/hash. Status is derived as `active`, `revoked`, `expired`, or `unavailable` from timestamps and current owner/resource state.
- Added `token.created`, `token.rotated`, `token.revoked`, and `token.authorization_denied` audits. Details are constructed from an allowlist of type, prefix, owner/resource IDs, and reason code.
- Documented Token types, bindings, scope, one-time secret/replay semantics, pagination, dynamic status, and 400/401/403/404/409 responses in `docs/api/openapi.yaml`.

## TDD Evidence

### RED: missing API

Command:

```text
go test ./internal/server -run TokenAPI -count=1
```

Key output before implementation:

```text
--- FAIL: TestTokenAPIAdminCreatesAllTypesAndReplaysWithoutSecret
create agent token status = 404: {"code":404,"msg":"Not Found","data":{"error":"not found"}}
--- FAIL: TestTokenAPIOwnerAuthorizationAndValidation
owner create status = 404
--- FAIL: TestTokenAPIConcurrentIdempotencyCreatesOnlyOneToken
concurrent request status = 404, want 201 or 409
FAIL
```

The existing authentication branch still returned 401, while all new authenticated routes failed specifically because the Token API was absent.

### RED: missing transaction contract

Command:

```text
go test ./internal/storage -run ServiceTokenMutationTransaction -count=1
```

Key output:

```text
internal/storage/service_token_test.go:222:15: db.ServiceTokenMutationTransaction undefined
internal/storage/service_token_test.go:243:12: db.ServiceTokenMutationTransaction undefined
FAIL
```

### RED: concurrent rotate ambiguity

Command:

```text
go test ./internal/server -run TestTokenAPIConcurrentIdempotencyRotatesOnlyOnce -count=10
```

Key output before the race fix:

```text
rotation loser returned an ambiguous conflict:
{"code":409,"msg":"Conflict","data":{"error":"token mutation conflict: token cannot be rotated"}}
FAIL
```

This proved that a loser could observe the winner's old-Token revocation before reading the winner's replay metadata.

### GREEN

Commands and results:

```text
go test ./internal/server -run TestTokenAPIConcurrentIdempotencyRotatesOnlyOnce -count=10
ok github.com/tunnelmesh/tunnelmesh/internal/server 3.780s

go test ./internal/server -run TokenAPI -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 2.373s

go test ./internal/server ./internal/auth ./internal/storage -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 4.074s
ok github.com/tunnelmesh/tunnelmesh/internal/auth 0.832s
ok github.com/tunnelmesh/tunnelmesh/internal/storage 0.643s
```

## Full Verification

```text
go test ./... -count=1       PASS
go test -race ./...          PASS
go vet ./...                 PASS
git diff --check             PASS
OpenAPI YAML parse           PASS
```

The focused race run `go test -race ./internal/server -run TokenAPI -count=1` also passed.

## Files

- `internal/server/token_service.go` (new)
- `internal/server/token_api.go` (new)
- `internal/server/token_api_test.go` (new)
- `internal/server/api.go`
- `internal/storage/db.go`
- `internal/storage/repository.go` (transaction-compatible idempotency executor only)
- `internal/storage/service_token_test.go`
- `docs/api/openapi.yaml`
- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-report.md` (this report)

All unrelated pre-existing Agent metadata, SSH, documentation, schema, auth, and workspace changes were preserved.

## Self-review

- Instruction compliance: Handler → Service → Repository is preserved; Server handler/service contains no SQL; no second DDL source or persisted status field was added.
- Authorization: non-admin owner/resource checks occur before lifecycle mutation and are repeated by `CredentialService` validation; management APIs still accept only management `api_tokens`.
- Secret safety: only the first mutation view can carry plaintext; repository records, audit details, list/detail views, replay records, and replay responses cannot carry Token hash/plaintext fields. Tests inspect both HTTP JSON and real database rows.
- Atomicity: winner claim, Token mutation, audit, and replay metadata update share one database transaction. Any callback error rolls all three stores back.
- Concurrency: real concurrent HTTP tests prove one create winner and one rotate replacement, with exactly one secret-bearing response for each operation.
- Pagination: owner/type filtering is passed to the repository before cursor limiting; the HTTP test includes another owner's Token ahead of page traversal.
- Audit: all four required action names are observed in the real audit repository and details are checked against an explicit field allowlist.
- No high-confidence defects remained after the final diff review.

## Concerns

- `TUNNELMESH_TEST_MYSQL_DSN` is unavailable, so live MySQL transaction/locking execution was not run. The MySQL code compiled and the full suite passed with its existing environment-gated tests skipped; run the targeted storage/server suite against a dedicated MySQL test database before merge.
- No OpenAPI semantic validator is installed in the workspace. The document was parsed successfully as YAML and its references were reviewed manually.

## Commits

none (not authorized)

## Fix Round 1/5

### Review findings resolved

- Closed the create/rotate authorization TOCTOU window. The existing
  transaction-external CredentialService planning remains as a fast preflight,
  but the authoritative CredentialService Create/Rotate operation now runs
  inside `ServiceTokenAuthorizedMutationTransaction` with transaction-bound
  Token, audit, idempotency, User, Agent, and node repositories.
- SQLite acquires a writer lock at the start of every service-token mutation
  transaction through the `schema_meta` no-op update. MySQL transaction-bound
  Token/User/Agent/node reads append `FOR UPDATE`, so mutable authorization and
  lifecycle facts are fenced before credential persistence.
- Changed service-token revoke to compare-and-set
  (`WHERE revoked_at IS NULL`). A zero-row transition re-reads the Token and
  distinguishes missing records from `ErrServiceTokenRevoked`; the first
  timestamp is never overwritten.
- Revoke now re-reads and decides inside the mutation transaction. Only the CAS
  winner writes `token.revoked`; concurrent losers return the already-revoked
  view with the winner's timestamp. Rotate and revoke therefore cannot both
  audit a successful lifecycle transition for the same source Token.
- Authorization-denied audit write failures are now observable through the
  structured `token_authorization_denied_audit_failed` slog event while the API
  retains its 403 response. The log contains only allowlisted action, actor,
  resource, and reason identifiers; it deliberately omits the underlying error
  text.
- Dynamic Token status now maps only missing/disabled/mismatched/expired
  resource facts to `unavailable`. Unexpected User/Agent/node repository
  errors propagate to the API as 500 instead of being silently hidden.

The review's N+1 status-read minor remains deferred. A later performance task
can add a request-level resource cache or repository batch query without
weakening correctness or changing status semantics.

### RED evidence

Authorization state changed after the transaction-external Agent read:

```text
go test ./internal/server -run TestTokenAPIAuthorizationChangeBeforeCommitCannotCreate -count=1
Token created from stale owner/enabled authorization:
{"code":201,...,"status":"unavailable",...,"secret":"..."}
FAIL
```

The combined API regression run also proved that denied-audit failures emitted
no log, status repository failures returned 200 `unavailable`, concurrent
revokes returned multiple timestamps, and rotate plus revoke both wrote
lifecycle audits:

```text
go test ./internal/server -run 'AuthorizationDenied|Status|AuthorizationChange|Concurrent.*(Revoke|Rotate)' -count=1
FAIL
```

The storage regression initially failed because the stronger transaction
contract did not exist:

```text
go test ./internal/storage -run 'ServiceToken.*Revoke|ServiceTokenAuthorizedMutationTransaction|ServiceTokenMutationTransaction' -count=1
db.ServiceTokenAuthorizedMutationTransaction undefined
undefined: ServiceTokenMutationRepositories
FAIL
```

### GREEN evidence

```text
go test ./internal/storage -run 'ServiceToken.*Revoke|ServiceTokenMutationTransaction|ServiceTokenAuthorizedMutationTransaction' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/storage 0.514s

go test ./internal/server -run 'AuthorizationDenied|Status|AuthorizationChange|Concurrent.*(Revoke|Rotate)' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 3.439s

go test ./internal/server -run TokenAPI -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 5.082s

go test ./internal/server -run 'Concurrent.*(Revoke|Rotate)|AuthorizationDenied|Status' -count=10
ok github.com/tunnelmesh/tunnelmesh/internal/server 27.778s

go test ./internal/server ./internal/auth ./internal/storage -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 9.023s
ok github.com/tunnelmesh/tunnelmesh/internal/auth 1.574s
ok github.com/tunnelmesh/tunnelmesh/internal/storage 0.478s

go test -race ./internal/server -run 'TokenAPI|Concurrent.*(Revoke|Rotate)' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 52.804s
```

### Full verification after fix round 1

```text
go test ./... -count=1       PASS
go test -race ./...          PASS
go vet ./...                 PASS
git diff --check             PASS
```

The full ordinary run passed every package, including server in 8.518s and
storage in 2.239s. The full race run passed server in 69.429s and storage in
2.766s.

### Fix-round files

- `internal/storage/db.go`
- `internal/storage/repository.go`
- `internal/storage/service_token_test.go`
- `internal/server/token_service.go`
- `internal/server/token_api.go`
- `internal/server/token_api_test.go`
- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-report.md`

### Fix-round concerns

- `TUNNELMESH_TEST_MYSQL_DSN` remains unavailable, so the MySQL `FOR UPDATE`
  behavior could not be exercised against a live server in this workspace.
  Driver-specific code compiled and all environment-independent tests passed;
  run the focused storage/server suite with a dedicated MySQL DSN before merge.
- Dynamic status still performs per-Token owner/resource reads. This is a
  bounded performance concern for large pages, not a correctness exception;
  address it with request-scoped caching or batch repository methods in a
  separate measured change.

### Fix-round commits

none (not authorized)
