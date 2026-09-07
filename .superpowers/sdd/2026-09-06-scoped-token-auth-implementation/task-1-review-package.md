# Task 1 review package

Base HEAD: `163fe12121d2f839ea4bf4907f55a8e7dd835057`

## Changed files

```text
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/docs/architecture/adr: 0001-scoped-service-tokens.md
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/docs/architecture/adr: 0001-scoped-service-tokens.md.__ABSENT__
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/db.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/db.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/models.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/models.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/mysql_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/mysql_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/sqlite_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/sqlite_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/migrations/ddl.sql and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/migrations/ddl.sql differ
```

## Full task-only diff

```diff
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/docs/architecture/adr/0001-scoped-service-tokens.md .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/docs/architecture/adr/0001-scoped-service-tokens.md
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/docs/architecture/adr/0001-scoped-service-tokens.md	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/docs/architecture/adr/0001-scoped-service-tokens.md	2026-09-06 14:15:01
@@ -0,0 +1,48 @@
+# ADR 0001: Separate scoped service credentials from management sessions
+
+- Status: Accepted
+- Date: 2026-09-06
+
+## Context
+
+TunnelMesh authenticates both interactive management users and long-lived service principals. Management sessions belong to users, while Agent, Client, and Server-node credentials have different ownership, scope, rotation, and revocation requirements. Treating these credentials as one namespace would blur authorization boundaries and make lifecycle policy harder to audit.
+
+Service credentials must remain non-recoverable after issuance. Their active, expired, revoked, and recently used states also need portable semantics across SQLite and MySQL without driver-specific generated columns or duplicated state flags.
+
+## Decision
+
+Management sessions remain in `api_tokens`. Agent, Client, and Server-node credentials are stored in the separate `service_tokens` table with an explicit `token_type`, optional principal bindings, a non-secret prefix for lookup and display, a one-way Token hash, and an authorization scope.
+
+Credential state is derived from timestamps rather than persisted as another mutable status field:
+
+- `revoked_at` records explicit revocation.
+- `expires_at` records optional expiry.
+- `last_used_at` records the latest successful use.
+
+A service Token is usable only when it is not revoked and has not expired. This keeps the database as the single source of truth and avoids contradictory status and timestamp combinations.
+
+Server-node authentication requires both transport identity and application credentials: mutually authenticated TLS establishes the node-to-node channel, and a scoped Server-node Token authorizes the requested TunnelMesh operation. Neither factor replaces the other.
+
+Token plaintext is returned only at successful issuance. Idempotency records and replay responses omit the Token secret; a replay may return non-secret resource metadata but never reproduces credential material.
+
+## Consequences
+
+- Management and service credential policies can evolve independently.
+- A leaked database does not directly reveal usable Token plaintext.
+- Callers must retain newly issued Token plaintext because it cannot be recovered later.
+- Authorization must validate Token type, principal binding, scope, revocation, and expiry for each service request.
+- Schema v3 introduces `service_tokens`; automatic initialization upgrades schema v2 by applying the authoritative portable DDL before advancing `schema_meta`.
+
+## Rejected alternatives
+
+### One shared Token namespace
+
+Keeping management sessions and service credentials in `api_tokens` was rejected because their principals, scopes, issuance paths, and revocation policies differ. A shared namespace would encourage ambiguous authorization branches and broaden the effect of future schema changes.
+
+### Custom encryption
+
+Implementing application-specific reversible encryption was rejected because it creates key-management, rotation, nonce, and misuse risks without a requirement to recover service credentials.
+
+### Recoverable Token ciphertext
+
+Storing encrypted Token plaintext for later display or idempotency replay was rejected because compromise of the ciphertext and decryption key would expose every credential. One-way hashes keep credential verification possible while limiting recovery risk.
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/db.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/db.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/db.go	2026-09-06 11:38:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/db.go	2026-09-06 14:15:01
@@ -17,7 +17,7 @@
 const (
 	DriverSQLite  = "sqlite"
 	DriverMySQL   = "mysql"
-	SchemaVersion = 2
+	SchemaVersion = 3
 )
 
 var ErrSchemaVersionMismatch = errors.New("schema version mismatch")
@@ -168,7 +168,7 @@
 }
 
 func requireSchemaTables(ctx context.Context, db *sql.DB, driver string) error {
-	for _, table := range []string{"schema_meta", "users", "agents", "agent_runtime_metadata"} {
+	for _, table := range []string{"schema_meta", "users", "agents", "agent_runtime_metadata", "service_tokens"} {
 		var found string
 		var err error
 		if driver == DriverMySQL {
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/models.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/models.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/models.go	2026-09-06 10:23:11
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/models.go	2026-09-06 14:15:10
@@ -27,6 +27,33 @@
 // Token is retained as a concise alias for callers that use the domain term.
 type Token = APIToken
 
+// TokenType identifies the service principal that may present a credential.
+type TokenType string
+
+const (
+	TokenTypeAgent      TokenType = "agent"
+	TokenTypeClient     TokenType = "client"
+	TokenTypeServerNode TokenType = "server_node"
+)
+
+// ServiceToken stores a one-way service credential and its authorization
+// scope. Lifecycle state is derived from the timestamp fields.
+type ServiceToken struct {
+	ID          string
+	OwnerUserID string
+	AgentID     string
+	NodeID      string
+	Prefix      string
+	TokenHash   string
+	Scope       string
+	Type        TokenType
+	ExpiresAt   *time.Time
+	RevokedAt   *time.Time
+	LastUsedAt  *time.Time
+	CreatedAt   time.Time
+	UpdatedAt   time.Time
+}
+
 type Agent struct {
 	ID           string
 	Name         string
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/mysql_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/mysql_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/mysql_test.go	2026-09-06 08:25:14
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/mysql_test.go	2026-09-06 14:16:03
@@ -2,8 +2,14 @@
 
 import (
 	"context"
+	"database/sql"
 	"os"
+	"strings"
 	"testing"
+
+	_ "github.com/go-sql-driver/mysql"
+
+	"github.com/tunnelmesh/tunnelmesh/migrations"
 )
 
 func TestMySQLRepositoryContract(t *testing.T) {
@@ -17,4 +23,142 @@
 	}
 	defer db.Close()
 	runRepositoryContract(t, db)
+}
+
+func TestMySQLAutoInitMigratesSchemaV2ToServiceTokens(t *testing.T) {
+	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
+	if dsn == "" {
+		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
+	}
+	raw := prepareMySQLServiceTokenSchemaTest(t, dsn, 2)
+
+	db, err := OpenMySQL(context.Background(), dsn, true)
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+
+	assertMySQLTableColumns(t, raw, "service_tokens", []string{
+		"id", "token_type", "owner_user_id", "agent_id", "node_id",
+		"token_prefix", "token_hash", "scope", "expires_at", "revoked_at",
+		"last_used_at", "created_at", "updated_at",
+	})
+	assertMySQLIndexColumns(t, raw, "idx_service_tokens_owner_type", []string{"owner_user_id", "token_type", "id"})
+	assertMySQLIndexColumns(t, raw, "idx_service_tokens_agent", []string{"agent_id", "token_type", "id"})
+	assertMySQLIndexColumns(t, raw, "idx_service_tokens_node", []string{"node_id", "token_type", "id"})
+
+	version, err := db.SchemaVersion(context.Background())
+	if err != nil {
+		t.Fatal(err)
+	}
+	if version != 3 {
+		t.Fatalf("schema version = %d, want 3", version)
+	}
+}
+
+func TestMySQLAutoInitDisabledRejectsMissingServiceTokensTable(t *testing.T) {
+	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
+	if dsn == "" {
+		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
+	}
+	prepareMySQLServiceTokenSchemaTest(t, dsn, 3)
+
+	if _, err := OpenMySQL(context.Background(), dsn, false); err == nil || !strings.Contains(err.Error(), "service_tokens") {
+		t.Fatalf("error=%v, want missing service_tokens table", err)
+	}
+}
+
+func prepareMySQLServiceTokenSchemaTest(t *testing.T, dsn string, version int) *sql.DB {
+	t.Helper()
+	raw, err := sql.Open("mysql", dsn)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := raw.PingContext(context.Background()); err != nil {
+		raw.Close()
+		t.Fatal(err)
+	}
+	for _, statement := range strings.Split(migrations.DDL, ";") {
+		statement = strings.TrimSpace(statement)
+		if statement == "" {
+			continue
+		}
+		if _, err := raw.ExecContext(context.Background(), statement); err != nil && !isDuplicateError(err) {
+			raw.Close()
+			t.Fatalf("prepare MySQL schema: %v", err)
+		}
+	}
+	statements := []struct {
+		query string
+		args  []any
+	}{
+		{query: `DROP TABLE IF EXISTS service_tokens`},
+		{query: `DELETE FROM schema_meta WHERE id=1`},
+		{query: `INSERT INTO schema_meta(id,version) VALUES(1,?)`, args: []any{version}},
+	}
+	for _, statement := range statements {
+		if _, err := raw.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
+			raw.Close()
+			t.Fatalf("prepare MySQL schema version: %v", err)
+		}
+	}
+	t.Cleanup(func() {
+		if _, err := raw.ExecContext(context.Background(), `UPDATE schema_meta SET version=2 WHERE id=1`); err != nil {
+			t.Errorf("restore MySQL schema version: %v", err)
+			_ = raw.Close()
+			return
+		}
+		db, err := OpenMySQL(context.Background(), dsn, true)
+		if err != nil {
+			t.Errorf("restore MySQL service_tokens schema: %v", err)
+		} else if err := db.Close(); err != nil {
+			t.Errorf("close restored MySQL database: %v", err)
+		}
+		if err := raw.Close(); err != nil {
+			t.Errorf("close MySQL schema test database: %v", err)
+		}
+	})
+	return raw
+}
+
+func assertMySQLTableColumns(t *testing.T, db *sql.DB, table string, want []string) {
+	t.Helper()
+	rows, err := db.QueryContext(context.Background(), `SELECT column_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position`, table)
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer rows.Close()
+	var got []string
+	for rows.Next() {
+		var name string
+		if err := rows.Scan(&name); err != nil {
+			t.Fatal(err)
+		}
+		got = append(got, name)
+	}
+	if err := rows.Err(); err != nil {
+		t.Fatal(err)
+	}
+	assertStringSlice(t, table+" columns", got, want)
+}
+
+func assertMySQLIndexColumns(t *testing.T, db *sql.DB, index string, want []string) {
+	t.Helper()
+	rows, err := db.QueryContext(context.Background(), `SELECT column_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='service_tokens' AND index_name=? ORDER BY seq_in_index`, index)
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer rows.Close()
+	var got []string
+	for rows.Next() {
+		var name string
+		if err := rows.Scan(&name); err != nil {
+			t.Fatal(err)
+		}
+		got = append(got, name)
+	}
+	if err := rows.Err(); err != nil {
+		t.Fatal(err)
+	}
+	assertStringSlice(t, index+" columns", got, want)
 }
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/sqlite_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/sqlite_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/internal/storage/sqlite_test.go	2026-09-06 11:38:25
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/internal/storage/sqlite_test.go	2026-09-06 14:14:08
@@ -10,6 +10,8 @@
 	"time"
 
 	_ "modernc.org/sqlite"
+
+	"github.com/tunnelmesh/tunnelmesh/migrations"
 )
 
 func TestSQLiteRepositoryContract(t *testing.T) { runRepositoryContract(t, newTestDB(t)) }
@@ -64,19 +66,139 @@
 	}
 }
 
+func TestSQLiteAutoInitMigratesSchemaV2ToServiceTokens(t *testing.T) {
+	dsn := "file:schema-v2-service-tokens?mode=memory&cache=shared"
+	raw, err := sql.Open("sqlite", dsn)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if _, err := raw.Exec(migrations.DDL); err != nil {
+		raw.Close()
+		t.Fatal(err)
+	}
+	if _, err := raw.Exec(`DROP TABLE IF EXISTS service_tokens; DELETE FROM schema_meta WHERE id=1; INSERT INTO schema_meta(id,version) VALUES (1,2)`); err != nil {
+		raw.Close()
+		t.Fatal(err)
+	}
+	defer raw.Close()
+
+	db, err := OpenSQLite(context.Background(), dsn, true)
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+
+	assertSQLiteTableColumns(t, db.SQL(), "service_tokens", []string{
+		"id", "token_type", "owner_user_id", "agent_id", "node_id",
+		"token_prefix", "token_hash", "scope", "expires_at", "revoked_at",
+		"last_used_at", "created_at", "updated_at",
+	})
+	assertSQLiteIndexColumns(t, db.SQL(), "idx_service_tokens_owner_type", []string{"owner_user_id", "token_type", "id"})
+	assertSQLiteIndexColumns(t, db.SQL(), "idx_service_tokens_agent", []string{"agent_id", "token_type", "id"})
+	assertSQLiteIndexColumns(t, db.SQL(), "idx_service_tokens_node", []string{"node_id", "token_type", "id"})
+
+	version, err := db.SchemaVersion(context.Background())
+	if err != nil {
+		t.Fatal(err)
+	}
+	if version != 3 {
+		t.Fatalf("schema version = %d, want 3", version)
+	}
+}
+
+func TestSQLiteAutoInitDisabledRejectsMissingServiceTokensTable(t *testing.T) {
+	dsn := "file:schema-v3-missing-service-tokens?mode=memory&cache=shared"
+	raw, err := sql.Open("sqlite", dsn)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if _, err := raw.Exec(migrations.DDL); err != nil {
+		raw.Close()
+		t.Fatal(err)
+	}
+	if _, err := raw.Exec(`DROP TABLE IF EXISTS service_tokens; DELETE FROM schema_meta WHERE id=1; INSERT INTO schema_meta(id,version) VALUES (1,3)`); err != nil {
+		raw.Close()
+		t.Fatal(err)
+	}
+	defer raw.Close()
+
+	if _, err := OpenSQLite(context.Background(), dsn, false); err == nil || !strings.Contains(err.Error(), "service_tokens") {
+		t.Fatalf("error=%v, want missing service_tokens table", err)
+	}
+}
+
 func TestSQLiteAutoInitDisabledRejectsMissingRuntimeMetadataTable(t *testing.T) {
 	dsn := "file:schema-missing-metadata?mode=memory&cache=shared"
 	raw, err := sql.Open("sqlite", dsn)
 	if err != nil {
 		t.Fatal(err)
 	}
-	if _, err := raw.Exec(`CREATE TABLE schema_meta (id INTEGER PRIMARY KEY, version INTEGER NOT NULL); INSERT INTO schema_meta(id,version) VALUES (1,2); CREATE TABLE users (id TEXT PRIMARY KEY); CREATE TABLE agents (id TEXT PRIMARY KEY)`); err != nil {
+	if _, err := raw.Exec(`CREATE TABLE schema_meta (id INTEGER PRIMARY KEY, version INTEGER NOT NULL); INSERT INTO schema_meta(id,version) VALUES (1,3); CREATE TABLE users (id TEXT PRIMARY KEY); CREATE TABLE agents (id TEXT PRIMARY KEY)`); err != nil {
 		raw.Close()
 		t.Fatal(err)
 	}
 	defer raw.Close()
 	if _, err := OpenSQLite(context.Background(), dsn, false); err == nil || !strings.Contains(err.Error(), "agent_runtime_metadata") {
 		t.Fatalf("error=%v, want missing runtime metadata table", err)
+	}
+}
+
+func assertSQLiteTableColumns(t *testing.T, db *sql.DB, table string, want []string) {
+	t.Helper()
+	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer rows.Close()
+
+	var got []string
+	for rows.Next() {
+		var cid, notNull, primaryKey int
+		var name, columnType string
+		var defaultValue any
+		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
+			t.Fatal(err)
+		}
+		got = append(got, name)
+	}
+	if err := rows.Err(); err != nil {
+		t.Fatal(err)
+	}
+	assertStringSlice(t, table+" columns", got, want)
+}
+
+func assertSQLiteIndexColumns(t *testing.T, db *sql.DB, index string, want []string) {
+	t.Helper()
+	rows, err := db.Query(fmt.Sprintf(`PRAGMA index_info(%s)`, index))
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer rows.Close()
+
+	var got []string
+	for rows.Next() {
+		var sequence, cid int
+		var name string
+		if err := rows.Scan(&sequence, &cid, &name); err != nil {
+			t.Fatal(err)
+		}
+		got = append(got, name)
+	}
+	if err := rows.Err(); err != nil {
+		t.Fatal(err)
+	}
+	assertStringSlice(t, index+" columns", got, want)
+}
+
+func assertStringSlice(t *testing.T, label string, got, want []string) {
+	t.Helper()
+	if len(got) != len(want) {
+		t.Fatalf("%s = %v, want %v", label, got, want)
+	}
+	for i := range want {
+		if got[i] != want[i] {
+			t.Fatalf("%s = %v, want %v", label, got, want)
+		}
 	}
 }
 
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/migrations/ddl.sql .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/migrations/ddl.sql
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-before/migrations/ddl.sql	2026-09-06 10:25:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/migrations/ddl.sql	2026-09-06 14:15:01
@@ -24,6 +24,25 @@
 );
 CREATE INDEX idx_api_tokens_user_id ON api_tokens(user_id);
 
+CREATE TABLE IF NOT EXISTS service_tokens (
+    id VARCHAR(255) PRIMARY KEY,
+    token_type VARCHAR(32) NOT NULL,
+    owner_user_id VARCHAR(255),
+    agent_id VARCHAR(255),
+    node_id VARCHAR(255),
+    token_prefix VARCHAR(32) NOT NULL,
+    token_hash VARCHAR(255) NOT NULL UNIQUE,
+    scope TEXT NOT NULL,
+    expires_at TEXT,
+    revoked_at TEXT,
+    last_used_at TEXT,
+    created_at TEXT NOT NULL,
+    updated_at TEXT NOT NULL
+);
+CREATE INDEX idx_service_tokens_owner_type ON service_tokens(owner_user_id, token_type, id);
+CREATE INDEX idx_service_tokens_agent ON service_tokens(agent_id, token_type, id);
+CREATE INDEX idx_service_tokens_node ON service_tokens(node_id, token_type, id);
+
 CREATE TABLE IF NOT EXISTS agents (
     id VARCHAR(255) PRIMARY KEY,
     name VARCHAR(255) NOT NULL,
```
