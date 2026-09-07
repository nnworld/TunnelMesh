# Task 2 Report: ServiceToken repository contract

## Status

Implemented the `ServiceTokenRepository` contract, SQLite/MySQL-compatible explicit-column SQL, atomic rotation, DB wiring, and a shared ServiceToken/Audit transaction runner. No commit was created (`none`, not authorized).

## Implementation

- Added `ServiceTokenFilter` and `ServiceTokenRepository` with create, lookup, filtered cursor listing, revoke, last-used touch, and rotate operations.
- Added explicit service-token column selection; no `SELECT *` is used.
- `List` constructs one SQL `WHERE` clause containing owner, token type, agent, node, and decoded cursor predicates before `ORDER BY ... LIMIT`.
- Direct `Rotate` begins one transaction. It locks the old row with `FOR UPDATE` on MySQL, relies on the configured SQLite single-writer connection for SQLite, rejects revoked tokens, inserts the replacement, revokes the old token, and commits.
- Extracted `rotateServiceToken` over `dbExecutor`; transaction-scoped repositories call it directly and therefore never start a nested transaction.
- Added `DB.ServiceTokens()` and `DB.ServiceTokenTransaction`, which supplies `ServiceTokenRepository` and `AuditRepository` bound to the same `*sql.Tx`.
- Added repository contract coverage to the shared SQLite/MySQL contract runner.

## TDD evidence

### RED

Command:

```text
go test ./internal/storage -run ServiceTokenRepository -count=1
```

Expected failure observed before production implementation:

```text
internal/storage/service_token_test.go:25:13: db.ServiceTokens undefined
internal/storage/service_token_test.go:73:12: undefined: ServiceTokenFilter
internal/storage/service_token_test.go:174:15: db.ServiceTokenTransaction undefined
internal/storage/service_token_test.go:174:56: undefined: ServiceTokenRepository
FAIL github.com/tunnelmesh/tunnelmesh/internal/storage [build failed]
```

### GREEN

Command:

```text
go test ./internal/storage -run ServiceTokenRepository -count=1
```

Final output:

```text
ok github.com/tunnelmesh/tunnelmesh/internal/storage 0.603s
```

MySQL command was not run because `TUNNELMESH_TEST_MYSQL_DSN` is unavailable in this environment. The shared `TestMySQLRepositoryContract` invokes the same ServiceToken contract when a DSN is provided.

## Test coverage

- Create, get, and get-by-hash preserve identity, scope, type, ownership, binding, expiry, and create/update timestamps.
- Duplicate token hashes fail via the database unique constraint.
- Interleaved non-matching owner/type/agent/node rows prove filtering occurs before cursor pagination.
- Revoke and touch-last-used persist their timestamps.
- Direct rotation inserts the replacement and revokes the old token atomically.
- Already-revoked tokens cannot be rotated and do not create a replacement.
- Duplicate replacement hash causes rotation failure while the old token remains active and the replacement remains absent.
- Transaction runner commit persists both rotation and audit; callback failure rolls back token rotation and audit together.

## Full verification

```text
go test ./... -count=1
PASS: all packages; internal/storage 1.356s

go test -race ./...
PASS: all packages; internal/storage 1.768s

go vet ./...
PASS: exit 0, no output

git diff --check
PASS: exit 0, no output
```

## Modified files

- `internal/storage/repository.go`
- `internal/storage/db.go`
- `internal/storage/storage_contract_test.go`
- `internal/storage/service_token_test.go` (new)
- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-report.md` (this report)

Pre-existing Task 1 schema/model changes and unrelated heartbeat/metadata working-tree changes were preserved and not reverted or overwritten.

## Self-review

No high-confidence correctness or instruction-compliance findings remain. The implementation stays inside the storage layer, uses the existing unique hash constraint, applies all authorization-relevant filters in SQL, and keeps direct and lifecycle-transaction rotation paths atomic without nested transactions.

## Risks / concerns

- MySQL runtime behavior was not executed locally because the test DSN is unavailable. The SQL uses MySQL-compatible placeholders and `FOR UPDATE`, and the shared MySQL contract is wired for execution when CI provides the DSN.
- `NewServiceTokenRepository` defaults to SQLite semantics, matching the repository's existing constructor convention; MySQL callers must use `NewServiceTokenRepositoryWithDriver` or the driver-aware `DB.ServiceTokens()` wiring.

## Commits

none (not authorized)
