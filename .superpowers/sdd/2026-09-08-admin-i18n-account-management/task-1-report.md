# Task 1 report

Status: DONE

## Modified files

- `internal/auth/account_service.go`, `internal/auth/account_service_test.go`
- `internal/auth/service.go`, `internal/auth/credential_service.go`, `internal/auth/credential_service_test.go`
- `internal/storage/models.go`, `internal/storage/repository.go`, `internal/storage/db.go`
- `internal/storage/account_repository_test.go`, `internal/storage/sqlite_test.go`, `internal/storage/mysql_test.go`
- `migrations/ddl.sql`, `migrations/embed.go`
- `migrations/incremental/v0005_to_v0006/mysql.sql`, `sqlite.sql`

## RED evidence

1. `go test ./internal/storage -run 'Test(Account|MySQLV5)' -count=1 -vet=off`
   - Failed to compile because `AccountUserRepository`, account status constants and `migrations.V5ToV6MySQL` did not exist.
2. `go test ./internal/auth -run 'TestAccountService' -count=1 -vet=off`
   - Failed to compile because `NewAccountService` and the stable account domain errors did not exist.
3. `go test ./internal/auth -run 'TestCredentialServiceRejectsInvalidLifecycleStateAndRepositoryErrors/deleted_owner' -count=1 -vet=off`
   - Failed behaviorally: a service token owned by a logically deleted user still authenticated successfully.

## GREEN evidence

1. `go test ./internal/storage -run 'Test(Account|MySQLV5)' -count=1 -vet=off` — PASS.
2. `go test ./internal/auth -run 'TestAccountService' -count=1 -vet=off` — PASS.
3. `go test ./internal/auth -run 'TestCredentialServiceRejectsInvalidLifecycleStateAndRepositoryErrors/deleted_owner' -count=1 -vet=off` — PASS.
4. `go test ./internal/auth ./internal/storage -count=1 -vet=off` — PASS for both packages.

## Self-review

- Schema version advances only after the driver-specific v5→v6 script succeeds; duplicate column/index errors are tolerated so MySQL implicit-commit partial migrations can be retried.
- Full DDL uses `VARCHAR(32)` for indexed `deleted_at`, avoiding MySQL 5.6 TEXT-index prefix failures.
- Child listing is repository-filtered by `role=user` and active/deleted/all before cursor pagination.
- Logical deletion never calls physical `Delete`; API tokens and related records remain stored.
- Password change/reset does not revoke existing API tokens.
- Login, API-token validation and service-token owner validation reject deleted users.
- Account mutations and audit insertion share `DB.AccountTransaction`; audit details contain only user ID, username and state.

## Risks / follow-up

- MySQL integration execution requires `TUNNELMESH_TEST_MYSQL_DSN`; the script has static MySQL 5.6 compatibility coverage locally.
- HTTP error mapping and public-user redaction belong to Task 2.
- No commit, push or merge was performed.
