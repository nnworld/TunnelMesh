package storage

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/tunnelmesh/tunnelmesh/migrations"
)

func TestMySQLRepositoryContract(t *testing.T) {
	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
	}
	db, err := OpenMySQL(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runRepositoryContract(t, db)
}

func TestMySQLV6ToV7ConnectionLeaseMigrationUsesCompatibleDDL(t *testing.T) {
	script := migrations.V6ToV7MySQL
	if strings.Contains(strings.ToUpper(script), "DROP TABLE") || strings.Contains(strings.ToUpper(script), "ON DUPLICATE KEY") {
		t.Fatalf("migration must not rewrite or upsert the legacy lease table: %s", script)
	}
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS agent_connection_leases",
		"PRIMARY KEY (agent_id, connection_id)",
		"INSERT INTO agent_connection_leases",
		"FROM agent_runtime_leases old",
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("migration missing required fragment %q", fragment)
		}
	}
}

func TestMySQLV10ToV11ClientObservabilityMigrationUsesCompatibleDDL(t *testing.T) {
	script := migrations.V10ToV11MySQL
	upper := strings.ToUpper(script)
	for _, forbidden := range []string{"JSON", "WITH RECURSIVE", "ON DUPLICATE KEY"} {
		if strings.Contains(upper, forbidden) {
			t.Fatalf("migration must not use MySQL 5.6-incompatible fragment %q: %s", forbidden, script)
		}
	}
	for _, fragment := range []string{
		"last_seen_at VARCHAR(32) NOT NULL",
		"expires_at VARCHAR(32) NOT NULL",
		"CREATE INDEX idx_client_instance_metadata_owner ON client_instance_metadata(owner_user_id, stale, last_seen_at)",
		"CREATE INDEX idx_client_connection_leases_expires ON client_connection_leases(expires_at)",
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("migration missing required fragment %q: %s", fragment, script)
		}
	}
}

func TestMySQLV11ToV12WebSSHMigrationsAreAdjacent(t *testing.T) {
	if SchemaVersion != 15 {
		t.Fatalf("SchemaVersion = %d, want 15", SchemaVersion)
	}
	if !strings.Contains(migrations.V11ToV12SQLite, "CREATE TABLE IF NOT EXISTS credentials") {
		t.Fatal("SQLite migration lacks credentials")
	}
	if !strings.Contains(migrations.V11ToV12MySQL, "CREATE TABLE IF NOT EXISTS remote_servers") {
		t.Fatal("MySQL migration lacks remote_servers")
	}
	if !strings.Contains(migrations.V11ToV12MySQL, "CREATE TABLE IF NOT EXISTS webssh_sessions") {
		t.Fatal("MySQL migration lacks webssh_sessions")
	}
	ddlStart := strings.Index(migrations.DDL, "CREATE TABLE IF NOT EXISTS credentials")
	if ddlStart < 0 {
		t.Fatal("full DDL lacks credentials")
	}
	ddlEnd := strings.Index(migrations.DDL[ddlStart:], "CREATE TABLE IF NOT EXISTS agent_runtime_stats")
	if ddlEnd < 0 {
		t.Fatal("full DDL lacks the schema section following WebSSH tables")
	}
	ddlSection := migrations.DDL[ddlStart : ddlStart+ddlEnd]
	for _, script := range []string{migrations.V11ToV12MySQL, ddlSection} {
		for _, fragment := range []string{
			"deleted_at VARCHAR(32),",
			"updated_at VARCHAR(32) NOT NULL",
			"ticket_expires_at VARCHAR(32) NOT NULL,",
			"expires_at VARCHAR(32) NOT NULL,",
		} {
			if !strings.Contains(script, fragment) {
				t.Fatalf("schema must use index-compatible timestamp type; missing %q", fragment)
			}
		}
		for _, forbidden := range []string{
			"deleted_at TEXT,",
			"updated_at TEXT NOT NULL,",
			"ticket_expires_at TEXT NOT NULL,",
			"expires_at TEXT NOT NULL,",
		} {
			if strings.Contains(script, forbidden) {
				t.Fatalf("schema uses TEXT for an indexed timestamp; forbidden %q", forbidden)
			}
		}
	}
	for _, fragment := range []string{
		"ALTER TABLE credentials MODIFY COLUMN deleted_at VARCHAR(32), MODIFY COLUMN updated_at VARCHAR(32) NOT NULL;",
		"ALTER TABLE remote_servers MODIFY COLUMN deleted_at VARCHAR(32), MODIFY COLUMN updated_at VARCHAR(32) NOT NULL;",
		"ALTER TABLE webssh_sessions MODIFY COLUMN ticket_expires_at VARCHAR(32) NOT NULL, MODIFY COLUMN expires_at VARCHAR(32) NOT NULL;",
	} {
		if !strings.Contains(migrations.V11ToV12MySQL, fragment) {
			t.Fatalf("MySQL migration must repair a partially applied TEXT table; missing %q", fragment)
		}
	}
}

// The credential secret columns must exist in both dialects as nullable adds:
// SQLite cannot modify a column, so the migration may only append, and a
// partially applied retry must be tolerated by the duplicate-column guard.
func TestMySQLV12ToV13CredentialSecretMigrationIsAdjacent(t *testing.T) {
	for _, column := range []string{"secret_ciphertext", "secret_nonce", "secret_key_id", "secret_version"} {
		if !strings.Contains(migrations.V12ToV13MySQL, column) {
			t.Fatalf("MySQL v12 to v13 migration lacks %s", column)
		}
		if !strings.Contains(migrations.V12ToV13SQLite, "ADD COLUMN "+column) {
			t.Fatalf("SQLite v12 to v13 migration lacks ADD COLUMN %s", column)
		}
	}
	for _, script := range []string{migrations.V12ToV13MySQL, migrations.V12ToV13SQLite} {
		for _, forbidden := range []string{"DROP COLUMN", "MODIFY COLUMN", "NOT NULL"} {
			if strings.Contains(script, forbidden) {
				t.Fatalf("credential secret migration must only add nullable columns; found %q", forbidden)
			}
		}
	}
	ddlStart := strings.Index(migrations.DDL, "secret_ciphertext TEXT,")
	if ddlStart < 0 {
		t.Fatal("full DDL lacks the credential secret columns")
	}
}

func TestMySQLV8ToV9AuthorizationRevisionMigration(t *testing.T) {
	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
	}
	raw, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.PingContext(context.Background()); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := applySchemaStatements(context.Background(), raw, migrations.DDL, true); err != nil {
		raw.Close()
		t.Fatalf("prepare MySQL schema: %v", err)
	}
	statements := []string{
		`DROP TABLE IF EXISTS authorization_revision`,
		`UPDATE schema_meta SET version=8 WHERE id=1`,
	}
	for _, statement := range statements {
		if _, err := raw.ExecContext(context.Background(), statement); err != nil {
			raw.Close()
			t.Fatalf("prepare v8 schema: %v", err)
		}
	}
	// The restore needs a handle of its own. The body closes raw so the migrated
	// database is the only pool while the upgrade runs, and a closed
	// database/sql handle cannot execute the cleanup statements.
	t.Cleanup(func() {
		restore, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Errorf("reopen MySQL schema test database: %v", err)
			return
		}
		defer func() { _ = restore.Close() }()
		if err := applySchemaStatements(context.Background(), restore, migrations.DDL, true); err != nil {
			t.Errorf("restore MySQL schema: %v", err)
		}
		if _, err := restore.ExecContext(context.Background(), `UPDATE schema_meta SET version=? WHERE id=1`, SchemaVersion); err != nil {
			t.Errorf("restore MySQL schema version: %v", err)
		}
	})
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenMySQL(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("migrate v8 to v9: %v", err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	revision, err := db.AuthorizationRevisions().Current(context.Background())
	if err != nil || revision != 1 {
		t.Fatalf("authorization revision = %d, err = %v, want 1", revision, err)
	}
}

func TestMySQLAutoInitRejectsMissingV2MigrationChain(t *testing.T) {
	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
	}
	prepareMySQLServiceTokenSchemaTest(t, dsn, 2)

	if _, err := OpenMySQL(context.Background(), dsn, true); err == nil || !strings.Contains(err.Error(), "missing adjacent migration v0002_to_v0003") {
		t.Fatalf("error=%v, want missing v2 to v3 migration", err)
	}
}

// TestMySQLAutoInitDisabledRejectsMissingServiceTokensTable keeps the schema at
// the current version so the only remaining defect is the missing table: with an
// old version the version guard rejects the database first, which is what the
// SQLite twin asserts and what the MySQL variant must assert too.
func TestMySQLAutoInitDisabledRejectsMissingServiceTokensTable(t *testing.T) {
	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
	}
	prepareMySQLServiceTokenSchemaTest(t, dsn, SchemaVersion)

	if _, err := OpenMySQL(context.Background(), dsn, false); err == nil || !strings.Contains(err.Error(), "service_tokens") {
		t.Fatalf("error=%v, want missing service_tokens table", err)
	}
}

func prepareMySQLServiceTokenSchemaTest(t *testing.T, dsn string, version int) *sql.DB {
	t.Helper()
	raw, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.PingContext(context.Background()); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	// applySchemaStatements is the same duplicate-tolerant, comment-aware path
	// production uses. Executing the DDL statement by statement without skipping
	// duplicates makes this helper unusable on a database another MySQL test in
	// this package already seeded, and strings.Split re-introduces the bug that
	// splitSQLStatements exists to fix.
	if err := applySchemaStatements(context.Background(), raw, migrations.DDL, true); err != nil {
		raw.Close()
		t.Fatalf("prepare MySQL schema: %v", err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{query: `DROP TABLE IF EXISTS service_tokens`},
		{query: `DELETE FROM schema_meta WHERE id=1`},
		{query: `INSERT INTO schema_meta(id,version) VALUES(1,?)`, args: []any{version}},
	}
	for _, statement := range statements {
		if _, err := raw.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			raw.Close()
			t.Fatalf("prepare MySQL schema version: %v", err)
		}
	}
	t.Cleanup(func() {
		if err := applySchemaStatements(context.Background(), raw, migrations.DDL, true); err != nil {
			t.Errorf("restore MySQL schema: %v", err)
		}
		if _, err := raw.ExecContext(context.Background(), `UPDATE schema_meta SET version=? WHERE id=1`, SchemaVersion); err != nil {
			t.Errorf("restore MySQL schema version: %v", err)
		}
		if err := raw.Close(); err != nil {
			t.Errorf("close MySQL schema test database: %v", err)
		}
	})
	return raw
}

func assertMySQLTableColumns(t *testing.T, db *sql.DB, table string, want []string) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT column_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	assertStringSlice(t, table+" columns", got, want)
}

func assertMySQLIndexColumns(t *testing.T, db *sql.DB, index string, want []string) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT column_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='service_tokens' AND index_name=? ORDER BY seq_in_index`, index)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	assertStringSlice(t, index+" columns", got, want)
}

// The v14 identity tables must be additive in both dialects: SQLite cannot
// rewrite a column, so the users change may only append, and every indexed
// timestamp must stay VARCHAR(32) so MySQL can build the index without an
// explicit key length.
func TestMySQLV13ToV14IdentityMigrationIsAdjacent(t *testing.T) {
	for _, script := range []string{migrations.V13ToV14MySQL, migrations.V13ToV14SQLite} {
		for _, forbidden := range []string{"DROP COLUMN", "MODIFY COLUMN", "DROP TABLE", "JSON", "ON DUPLICATE KEY"} {
			if strings.Contains(strings.ToUpper(script), forbidden) {
				t.Fatalf("identity migration must be additive only; found %q", forbidden)
			}
		}
		for _, table := range []string{
			"auth_settings", "oidc_providers", "user_identities", "user_mfa",
			"user_recovery_codes", "user_devices", "auth_challenges", "auth_login_attempts",
		} {
			if !strings.Contains(script, "CREATE TABLE IF NOT EXISTS "+table) {
				t.Fatalf("identity migration lacks table %s", table)
			}
		}
		for _, fragment := range []string{
			"expires_at VARCHAR(32) NOT NULL",
			"revoked_at VARCHAR(32)",
			"blocked_until VARCHAR(32)",
			"created_at VARCHAR(32) NOT NULL",
		} {
			if !strings.Contains(script, fragment) {
				t.Fatalf("identity migration must use index-compatible timestamps; missing %q", fragment)
			}
		}
	}
	for _, column := range []string{"auth_source", "mfa_required"} {
		if !strings.Contains(migrations.V13ToV14MySQL, column) {
			t.Fatalf("MySQL v13 to v14 migration lacks %s", column)
		}
		if !strings.Contains(migrations.V13ToV14SQLite, "ADD COLUMN "+column) {
			t.Fatalf("SQLite v13 to v14 migration lacks ADD COLUMN %s", column)
		}
		if !strings.Contains(migrations.DDL, column) {
			t.Fatalf("full DDL lacks %s", column)
		}
	}
	// MySQL cannot create an index named with IF NOT EXISTS, so the MySQL script
	// must use the bare form while SQLite keeps the retry-safe form.
	if strings.Contains(migrations.V13ToV14MySQL, "CREATE INDEX IF NOT EXISTS") {
		t.Fatal("MySQL migration must not use CREATE INDEX IF NOT EXISTS")
	}
	if !strings.Contains(migrations.V13ToV14SQLite, "CREATE INDEX IF NOT EXISTS idx_auth_challenges_expires") {
		t.Fatal("SQLite migration lacks the retry-safe challenge expiry index")
	}
	// The full DDL and the incremental script must define the same tables.
	for _, table := range []string{
		"auth_settings", "oidc_providers", "user_identities", "user_mfa",
		"user_recovery_codes", "user_devices", "auth_challenges", "auth_login_attempts",
	} {
		if !strings.Contains(migrations.DDL, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("full DDL lacks table %s", table)
		}
	}
}

func TestMySQLIdentityRepositoryContract(t *testing.T) {
	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
	}
	db, err := OpenMySQL(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runIdentityRepositoryContract(t, db)
}

// TestMySQLV14ToV15WidensConnectionEpoch guards the drift that made every
// Client lease unwritable on MySQL: newClientConnectionEpoch produces a random
// int64 fencing token, but a 32-bit INTEGER column clamps it to 2147483647, so
// Renew/UpdateStats/Release never match their epoch predicate. The assertions
// are textual on purpose — the MySQL-backed contract tests are skipped unless
// TUNNELMESH_TEST_MYSQL_DSN is set, which is exactly why the original
// INTEGER/BIGINT mismatch between migrations/ddl.sql and v0006_to_v0007
// survived unnoticed.
func TestMySQLV14ToV15WidensConnectionEpoch(t *testing.T) {
	if SchemaVersion != 15 {
		t.Fatalf("SchemaVersion = %d, want 15", SchemaVersion)
	}
	script := migrations.V14ToV15MySQL
	upper := strings.ToUpper(script)
	for _, forbidden := range []string{"JSON", "WITH RECURSIVE", "ON DUPLICATE KEY", "CREATE INDEX IF NOT EXISTS", "DROP TABLE"} {
		if strings.Contains(upper, forbidden) {
			t.Fatalf("migration must not use MySQL 5.6-incompatible fragment %q: %s", forbidden, script)
		}
	}
	// Both lease tables carry a connection_epoch fencing token, and both must be
	// widened in the same step so a fresh install and an upgraded install agree.
	for _, table := range []string{"client_connection_leases", "agent_connection_leases"} {
		fragment := "ALTER TABLE " + table + " MODIFY connection_epoch BIGINT NOT NULL"
		if !strings.Contains(script, fragment) {
			t.Fatalf("migration missing required fragment %q: %s", fragment, script)
		}
	}
	// SQLite INTEGER is already 64 bits and SQLite cannot ALTER a column type,
	// so the SQLite step must stay structurally inert but still be executable:
	// applySchemaStatements splits on ';' and runs every non-empty statement.
	sqlite := migrations.V14ToV15SQLite
	if strings.Contains(strings.ToUpper(sqlite), "ALTER TABLE") {
		t.Fatalf("SQLite v14 to v15 migration must not alter columns: %s", sqlite)
	}
	if strings.TrimSpace(sqlite) == "" {
		t.Fatal("SQLite v14 to v15 migration must not be empty")
	}
	// The full DDL is the authoritative current schema for an empty database and
	// must not reintroduce the narrow type on either lease table.
	for _, table := range []string{"client_connection_leases", "agent_connection_leases"} {
		start := strings.Index(migrations.DDL, "CREATE TABLE IF NOT EXISTS "+table)
		if start < 0 {
			t.Fatalf("full DDL lacks table %s", table)
		}
		end := strings.Index(migrations.DDL[start:], ");")
		if end < 0 {
			t.Fatalf("full DDL table %s is not terminated", table)
		}
		section := migrations.DDL[start : start+end]
		if !strings.Contains(section, "connection_epoch BIGINT NOT NULL") {
			t.Fatalf("full DDL %s must declare connection_epoch BIGINT: %s", table, section)
		}
		if strings.Contains(section, "connection_epoch INTEGER") {
			t.Fatalf("full DDL %s still declares a 32-bit connection_epoch: %s", table, section)
		}
	}
}
