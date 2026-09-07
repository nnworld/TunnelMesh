### Task 1: Record the credential-boundary ADR and schema migration

**Files:**
- Create: `docs/architecture/adr/0001-scoped-service-tokens.md`
- Modify: `migrations/ddl.sql`
- Modify: `internal/storage/db.go`
- Modify: `internal/storage/models.go`
- Test: `internal/storage/sqlite_test.go`
- Test: `internal/storage/mysql_test.go`

**Interfaces:**
- Produces: `storage.ServiceToken`, `storage.TokenType`, schema v3, and portable v2→v3 migration behavior.

- [ ] **Step 1: Write failing schema tests**

Add tests that open an existing schema-v2 database, run `Open(..., autoInit=true)`, and assert the following table and indexes exist:

```sql
service_tokens(
  id, token_type, owner_user_id, agent_id, node_id,
  token_prefix, token_hash, scope, expires_at,
  revoked_at, last_used_at, created_at, updated_at
)
```

Also assert `autoInit=false` fails when `service_tokens` is absent.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./internal/storage -run 'Schema|ServiceToken' -count=1
```

Expected: failure because schema v3 and `service_tokens` do not exist.

- [ ] **Step 3: Add the portable schema and migration**

Add only the following new table family to `migrations/ddl.sql`:

```sql
CREATE TABLE IF NOT EXISTS service_tokens (
    id VARCHAR(255) PRIMARY KEY,
    token_type VARCHAR(32) NOT NULL,
    owner_user_id VARCHAR(255),
    agent_id VARCHAR(255),
    node_id VARCHAR(255),
    token_prefix VARCHAR(32) NOT NULL,
    token_hash VARCHAR(255) NOT NULL UNIQUE,
    scope TEXT NOT NULL,
    expires_at TEXT,
    revoked_at TEXT,
    last_used_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_service_tokens_owner_type ON service_tokens(owner_user_id, token_type, id);
CREATE INDEX idx_service_tokens_agent ON service_tokens(agent_id, token_type, id);
CREATE INDEX idx_service_tokens_node ON service_tokens(node_id, token_type, id);
```

Set `SchemaVersion = 3`, require the table in `requireSchemaTables`, and make the v2→v3 path execute the same authoritative DDL before updating `schema_meta`.

Define:

```go
type TokenType string
const (
    TokenTypeAgent TokenType = "agent"
    TokenTypeClient TokenType = "client"
    TokenTypeServerNode TokenType = "server_node"
)

type ServiceToken struct {
    ID, OwnerUserID, AgentID, NodeID, Prefix, TokenHash, Scope string
    Type TokenType
    ExpiresAt, RevokedAt, LastUsedAt *time.Time
    CreatedAt, UpdatedAt time.Time
}
```

- [ ] **Step 4: Write the ADR**

Record the decision to keep management sessions in `api_tokens`, store service credentials in `service_tokens`, derive state from timestamps, use mTLS plus Token for Server-node, and omit secrets on idempotency replay. Include alternatives rejected: one shared Token namespace, custom encryption, and recoverable Token ciphertext.

- [ ] **Step 5: Verify GREEN**

Run:

```bash
go test ./internal/storage -run 'Schema|ServiceToken' -count=1
git diff --check
```

