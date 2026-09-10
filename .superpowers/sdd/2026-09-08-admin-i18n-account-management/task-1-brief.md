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
