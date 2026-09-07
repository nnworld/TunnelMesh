# Task 1 Report: Scoped service Token schema and ADR

## Status

Implemented schema v3, portable v2→v3 automatic migration coverage, strict-schema validation, service Token storage models, and the credential-boundary ADR. No commit, push, or merge was performed.

## Implementation

- Added the authoritative `service_tokens` table and its owner/type, agent/type, and node/type composite indexes to `migrations/ddl.sql`.
- Bumped `storage.SchemaVersion` from 2 to 3 and added `service_tokens` to `requireSchemaTables`.
- Retained the existing portable migration ordering: `initializeSchema` applies the embedded authoritative DDL before it advances `schema_meta`, so a v2 database receives the new table family before version 3 is recorded.
- Added `storage.TokenType`, the three required Token-type constants, and `storage.ServiceToken` with timestamp-derived lifecycle fields.
- Added SQLite and optional live-MySQL tests for v2→v3 migration, exact ordered columns, exact composite-index columns, and `autoInit=false` rejection when `service_tokens` is absent.
- Added ADR 0001 covering the separate management/service credential boundary, timestamp-derived state, Server-node mTLS plus scoped Token, and secret omission on idempotency replay.

## TDD Evidence

### RED

Command:

```text
go test ./internal/storage -run 'Schema|ServiceToken' -count=1
```

Key output before production changes:

```text
--- FAIL: TestSQLiteAutoInitMigratesSchemaV2ToServiceTokens
service_tokens columns = [], want [id token_type owner_user_id agent_id node_id token_prefix token_hash scope expires_at revoked_at last_used_at created_at updated_at]
--- FAIL: TestSQLiteAutoInitDisabledRejectsMissingServiceTokensTable
schema version mismatch: database has version 3, application requires version 2
FAIL
```

The first attempted RED run exposed an invalid simplified v2 fixture (`no such column: owner_user_id`). The fixture was corrected to apply the real pre-migration schema and then remove only `service_tokens`; RED was rerun until it failed solely for the missing feature as shown above.

### GREEN

Commands:

```text
go test ./internal/storage -run 'Schema|ServiceToken' -count=1
git diff --check
```

Key output:

```text
ok github.com/tunnelmesh/tunnelmesh/internal/storage 0.522s
exit code 0
```

## Full Verification

```text
go test ./... -count=1       PASS
go test -race ./...          PASS
go vet ./...                 PASS
git diff --check             PASS
```

Live MySQL schema tests were also invoked explicitly with verbose output. They were skipped because `TUNNELMESH_TEST_MYSQL_DSN` is not configured in this environment; both tests compiled and the package passed. SQLite executed both migration and strict-schema paths.

## Modified Files

- `docs/architecture/adr/0001-scoped-service-tokens.md` (new)
- `migrations/ddl.sql`
- `internal/storage/db.go`
- `internal/storage/models.go`
- `internal/storage/sqlite_test.go`
- `internal/storage/mysql_test.go`
- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-report.md` (this report)

Unrelated pre-existing heartbeat/metadata, documentation, and `.gitignore` changes were preserved and not edited by this task.

## Self-review

- Requirements: all Task 1 table columns, index names/order, model fields/constants, schema-version behavior, and ADR decisions are present.
- Portability: the DDL uses types and statements already shared by SQLite/MySQL; migration and validation branch by driver only for catalog inspection.
- Security: schema stores only a Token prefix and one-way hash; the ADR explicitly rejects recoverable ciphertext and replaying secrets.
- Test quality: expected columns and index layouts are independent literal fixtures, and tests inspect real database catalogs rather than source text or mocks.
- Scope: no repository/API behavior, second DDL source, commit, push, merge, or unrelated cleanup was introduced.

## Risks and Follow-up

- A live MySQL instance was unavailable, so MySQL catalog assertions and the v2→v3 execution path remain environment-skipped. Run the targeted tests with a dedicated `TUNNELMESH_TEST_MYSQL_DSN` before merge.
- The live-MySQL tests intentionally mutate the dedicated test database schema and restore it during cleanup; the configured DSN must never point to a non-test database.

## Commits

none (not authorized)

## Fix Round 1/5

### Review finding addressed

Updated ADR 0001 to define effective service Token availability as a derived authorization-time decision based on both credential timestamps (`revoked_at`, `expires_at`) and the current state of every bound principal/resource. The ADR now explicitly states that disabling, deleting, or otherwise making an owner user, Agent, Server node, or other scoped resource unavailable makes the Token unavailable immediately, without adding a persisted `status` field.

The deferred MySQL context-deadline finding was intentionally left unchanged in this round.

### Coverage check

Commands:

```text
git diff --check
go test ./internal/storage -run 'Schema|ServiceToken' -count=1
```

Key output:

```text
git diff --check: no output (exit 0)
ok github.com/tunnelmesh/tunnelmesh/internal/storage 0.503s
combined exit code 0
```
