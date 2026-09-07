### Task 2: Implement the ServiceToken repository contract

**Files:**
- Modify: `internal/storage/repository.go`
- Modify: `internal/storage/db.go`
- Test: `internal/storage/storage_contract_test.go`
- Create: `internal/storage/service_token_test.go`

**Interfaces:**
- Produces:

```go
type ServiceTokenFilter struct { OwnerUserID string; Type TokenType; AgentID string; NodeID string }
type ServiceTokenRepository interface {
    Create(context.Context, ServiceToken) error
    Get(context.Context, string) (ServiceToken, error)
    GetByHash(context.Context, string) (ServiceToken, error)
    List(context.Context, ServiceTokenFilter, string, int) (Page[ServiceToken], error)
    Revoke(context.Context, string, time.Time) error
    TouchLastUsed(context.Context, string, time.Time) error
    Rotate(context.Context, string, ServiceToken, time.Time) error
}
```

- [ ] **Step 1: Write failing repository contract tests**

Cover create/get-by-hash, SQL-level owner/type filtering before cursor pagination, expiry fields, revocation, last-used touch, atomic rotation, duplicate hash, and rotation rollback when replacement insert fails.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/storage -run ServiceTokenRepository -count=1
```

- [ ] **Step 3: Implement explicit-column SQLite/MySQL SQL**

Use no `SELECT *`. `Rotate` must start one transaction, lock the old row on MySQL with `FOR UPDATE`, reject an already revoked Token, insert the replacement, revoke the old row, and commit. SQLite uses the existing single-writer connection and transaction.

- [ ] **Step 4: Add DB wiring**

Add `serviceTokens ServiceTokenRepository` to `storage.DB`, initialize it in `newDB`, and expose `ServiceTokens()`. Add a transaction runner that supplies `ServiceTokenRepository` and `AuditRepository` for lifecycle operations.

- [ ] **Step 5: Verify GREEN on both contracts**

```bash
go test ./internal/storage -run ServiceTokenRepository -count=1
TUNNELMESH_TEST_MYSQL_DSN="$TUNNELMESH_TEST_MYSQL_DSN" go test ./internal/storage -run MySQL -count=1
```

Skip the MySQL command with an explicit report when the DSN is unavailable.

