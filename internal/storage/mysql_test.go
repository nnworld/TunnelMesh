package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
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
	if _, err := raw.ExecContext(context.Background(), migrations.DDL); err != nil {
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
	t.Cleanup(func() {
		if _, err := raw.ExecContext(context.Background(), migrations.DDL); err != nil {
			t.Errorf("restore MySQL schema: %v", err)
		}
		if _, err := raw.ExecContext(context.Background(), `UPDATE schema_meta SET version=? WHERE id=1`, SchemaVersion); err != nil {
			t.Errorf("restore MySQL schema version: %v", err)
		}
		if err := raw.Close(); err != nil {
			t.Errorf("close MySQL schema test database: %v", err)
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

func TestMySQLAutoInitDisabledRejectsMissingServiceTokensTable(t *testing.T) {
	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
	}
	prepareMySQLServiceTokenSchemaTest(t, dsn, 3)

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
	for _, statement := range strings.Split(migrations.DDL, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := raw.ExecContext(context.Background(), statement); err != nil && !isDuplicateError(err) {
			raw.Close()
			t.Fatalf("prepare MySQL schema: %v", err)
		}
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
		for _, statement := range strings.Split(migrations.DDL, ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			if _, err := raw.ExecContext(context.Background(), statement); err != nil && !isDuplicateError(err) {
				t.Errorf("restore MySQL schema: %v", err)
			}
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

// TestMySQLVPNRepositoryContract runs the v15 VPN repositories against a real
// MySQL server so a driver-specific SQL error cannot slip through the SQLite
// run. It is skipped when no server is configured.
func TestMySQLVPNRepositoryContract(t *testing.T) {
	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
	}
	db, err := OpenMySQL(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runVPNPeerRepositoryContract(t, db)
	runVPNIPLeaseRepositoryContract(t, db)
}

// vpnSchemaIndexes are the five secondary indexes schema v15 adds. The names are
// asserted literally because the upgrade runbook and ops tooling refer to them.
var vpnSchemaIndexes = []string{
	"idx_vpn_peers_owner",
	"idx_vpn_peers_node",
	"idx_vpn_peers_expires",
	"idx_vpn_ip_leases_holder",
	"idx_vpn_ip_leases_expires",
}

// MySQL 5.6 is the DDL baseline for this project. With Antelope, COMPACT row
// format and innodb_large_prefix=OFF, an index key may not exceed 767 bytes and
// the variable-length prefixes kept inline may not push a row past 8126 bytes.
const (
	mysql56InlineRowBudget = 8126
	mysql56InlineColumnCap = 788 // COMPACT stores at most 768 bytes plus a 20-byte pointer inline
	mysql56MaxIndexKey     = 767
	mysql56MinHeadroom     = 0.15
)

// TestMySQLV14ToV15VPNMigrationsAreAdjacentAndDialectSafe is an ungated static
// gate: it needs no MySQL instance, so it runs on every developer machine and in
// CI. The two v15 scripts must be additive, must agree with each other and with
// migrations/ddl.sql, and must respect the per-dialect index syntax.
func TestMySQLV14ToV15VPNMigrationsAreAdjacentAndDialectSafe(t *testing.T) {
	scripts := map[string]string{
		"mysql":  migrations.V14ToV15MySQL,
		"sqlite": migrations.V14ToV15SQLite,
	}
	for _, forbidden := range []string{
		"DROP COLUMN", "MODIFY COLUMN", "DROP TABLE", "JSON",
		"ON DUPLICATE KEY", "WITH RECURSIVE", "ADD COLUMN IF NOT EXISTS", "ALTER TABLE",
	} {
		for name, script := range scripts {
			if strings.Contains(strings.ToUpper(script), forbidden) {
				t.Fatalf("%s v15 migration must be additive only; found %q", name, forbidden)
			}
		}
	}
	// Column widths the MySQL 5.6 budget depends on, asserted verbatim so a
	// well-meaning "normalization" of a VARCHAR to TEXT fails loudly instead of
	// silently breaking the inline row budget.
	requiredFragments := []string{
		"public_key VARCHAR(64) NOT NULL UNIQUE",
		"private_key_nonce VARCHAR(64)",
		"private_key_key_id VARCHAR(64)",
		"vpn_ip VARCHAR(45) NOT NULL",
		"created_at VARCHAR(32) NOT NULL",
		"lease_expires_at VARCHAR(32) NOT NULL",
		"UNIQUE(node_id, vpn_ip)",
		"UNIQUE(node_id, subnet)",
	}
	for name, script := range scripts {
		for _, table := range vpnSchemaTables {
			if !strings.Contains(script, "CREATE TABLE IF NOT EXISTS "+table) {
				t.Fatalf("%s v15 migration lacks table %s", name, table)
			}
		}
		for _, fragment := range requiredFragments {
			if !strings.Contains(script, fragment) {
				t.Fatalf("%s v15 migration missing required fragment %q", name, fragment)
			}
		}
		// MySQL 5.6 rejects an index on a TEXT column without a prefix length
		// (Error 1170), so no indexed timestamp may ever become TEXT.
		for _, forbidden := range []string{"expires_at TEXT", "created_at TEXT", "updated_at TEXT", "acquired_at TEXT"} {
			if strings.Contains(script, forbidden) {
				t.Fatalf("%s v15 migration uses TEXT for an indexed timestamp; forbidden %q", name, forbidden)
			}
		}
		if !strings.HasSuffix(strings.TrimSpace(script), ";") {
			t.Fatalf("%s v15 migration must end with a semicolon", name)
		}
		assertNoCommentOnlyChunks(t, name+" v15 migration", script)
	}
	// MySQL cannot spell CREATE INDEX with IF NOT EXISTS, while SQLite must so a
	// partially applied migration stays retry-safe.
	if strings.Contains(scripts["mysql"], "CREATE INDEX IF NOT EXISTS") {
		t.Fatal("MySQL v15 migration must not use CREATE INDEX IF NOT EXISTS")
	}
	for _, index := range vpnSchemaIndexes {
		if !strings.Contains(scripts["sqlite"], "CREATE INDEX IF NOT EXISTS "+index+" ON ") {
			t.Fatalf("SQLite v15 migration lacks the retry-safe index %s", index)
		}
		if !strings.Contains(scripts["mysql"], "CREATE INDEX "+index+" ON ") {
			t.Fatalf("MySQL v15 migration lacks the index %s", index)
		}
		if !strings.Contains(migrations.DDL, "CREATE INDEX "+index+" ON ") {
			t.Fatalf("full DDL lacks the index %s", index)
		}
	}
	tableStatements := func(label, script string) string {
		t.Helper()
		i := strings.Index(script, "CREATE TABLE IF NOT EXISTS vpn_peers")
		if i < 0 {
			t.Fatalf("%s lacks the vpn_peers table statement", label)
		}
		var kept []string
		for _, line := range strings.Split(script[i:], "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "CREATE INDEX") {
				continue
			}
			kept = append(kept, line)
		}
		return strings.TrimSpace(strings.Join(kept, "\n"))
	}
	mysqlTables := tableStatements("MySQL v15 migration", scripts["mysql"])
	sqliteTables := tableStatements("SQLite v15 migration", scripts["sqlite"])
	if mysqlTables != sqliteTables {
		t.Fatalf("v15 table statements differ between dialects:\nmysql:\n%s\nsqlite:\n%s", mysqlTables, sqliteTables)
	}
	if !strings.Contains(tableStatements("migrations/ddl.sql", migrations.DDL), mysqlTables) {
		t.Fatal("migrations/ddl.sql does not contain the v15 table statements verbatim")
	}
}

// assertNoCommentOnlyChunks guards the statement splitter in
// internal/storage/db.go: it splits a script on ';' and executes every non-empty
// chunk, so a chunk holding only '--' comments would reach the server and fail.
func assertNoCommentOnlyChunks(t *testing.T, label, script string) {
	t.Helper()
	for _, chunk := range strings.Split(script, ";") {
		trimmed := strings.TrimSpace(chunk)
		if trimmed == "" {
			continue
		}
		var code bool
		for _, line := range strings.Split(trimmed, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "--") {
				code = true
				break
			}
		}
		if !code {
			t.Fatalf("%s has a comment-only statement chunk: %q", label, trimmed)
		}
	}
}

// vpnColumn is one parsed column of a v15 table plus its worst-case MySQL 5.6
// footprint.
type vpnColumn struct {
	name      string
	decl      string
	bytes     int
	indexable bool
}

// vpnIndexKey is a parsed index definition: a name and the columns it covers in
// index order.
type vpnIndexKey struct {
	name    string
	columns []string
}

// TestVPNMigrationsFitMySQL56Budgets proves the v15 tables fit the MySQL 5.6
// inline row and index key budgets. It parses the DDL text instead of querying a
// server, so it is a real gate on machines without MySQL. Only the two new
// tables are covered: pre-existing tables are out of scope, and the known
// oidc_providers overrun is tracked separately.
func TestVPNMigrationsFitMySQL56Budgets(t *testing.T) {
	sources := []struct{ label, script string }{
		{"migrations.V14ToV15MySQL", migrations.V14ToV15MySQL},
		{"migrations/ddl.sql", migrations.DDL},
	}
	budget := float64(mysql56InlineRowBudget)
	minHeadroom := int(budget * mysql56MinHeadroom)
	for _, source := range sources {
		for _, table := range vpnSchemaTables {
			block, err := extractCreateTable(source.script, table)
			if err != nil {
				t.Fatalf("%s: %v", source.label, err)
			}
			columns, inlineKeys, err := parseVPNTableBlock(block)
			if err != nil {
				t.Fatalf("%s: table %s: %v", source.label, table, err)
			}
			byName := make(map[string]vpnColumn, len(columns))
			inline := 0
			var detail []string
			for _, column := range columns {
				byName[column.name] = column
				inline += min(column.bytes, mysql56InlineColumnCap)
				detail = append(detail, fmt.Sprintf("%s %s=%d", column.name, column.decl, min(column.bytes, mysql56InlineColumnCap)))
			}
			headroom := mysql56InlineRowBudget - inline
			if inline >= mysql56InlineRowBudget || headroom < minHeadroom {
				t.Fatalf("%s: table %s needs %d inline bytes (budget %d, headroom %d, required >= %d): %s",
					source.label, table, inline, mysql56InlineRowBudget, headroom, minHeadroom, strings.Join(detail, ", "))
			}
			largestKey, largestName := 0, ""
			for _, key := range append(inlineKeys, parseVPNCreateIndexes(source.script, table)...) {
				size := 0
				for _, name := range key.columns {
					column, ok := byName[name]
					if !ok {
						t.Fatalf("%s: index %s on %s references unknown column %s", source.label, key.name, table, name)
					}
					if !column.indexable {
						t.Fatalf("%s: index %s on %s covers %s, declared %s; MySQL 5.6 cannot index that type without a prefix length (Error 1170)",
							source.label, key.name, table, name, column.decl)
					}
					size += column.bytes
				}
				if size > largestKey {
					largestKey, largestName = size, key.name
				}
				if size > mysql56MaxIndexKey {
					t.Fatalf("%s: index %s on %s is %d bytes over the %d-byte MySQL 5.6 limit (columns %v)",
						source.label, key.name, table, size, mysql56MaxIndexKey, key.columns)
				}
			}
			t.Logf("%s: %s inline=%d/%d bytes (headroom %d), largest index key %s=%d/%d bytes",
				source.label, table, inline, mysql56InlineRowBudget, headroom, largestName, largestKey, mysql56MaxIndexKey)
		}
	}
}

// extractCreateTable returns the body of a CREATE TABLE statement, without the
// opening line and without the closing ");".
func extractCreateTable(script, table string) (string, error) {
	marker := "CREATE TABLE IF NOT EXISTS " + table + " ("
	start := strings.Index(script, marker)
	if start < 0 {
		return "", fmt.Errorf("script lacks %q", marker)
	}
	lines := strings.Split(script[start:], "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == ");" {
			return strings.Join(lines[1:i], "\n"), nil
		}
	}
	return "", fmt.Errorf("script has an unterminated CREATE TABLE for %s", table)
}

// parseVPNTableBlock turns a CREATE TABLE body into its columns and the index
// keys declared inline (PRIMARY KEY and table-level UNIQUE).
func parseVPNTableBlock(block string) ([]vpnColumn, []vpnIndexKey, error) {
	var columns []vpnColumn
	var keys []vpnIndexKey
	for _, raw := range strings.Split(block, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		line = strings.TrimSuffix(strings.TrimSpace(line), ",")
		if strings.HasPrefix(line, "UNIQUE(") && strings.HasSuffix(line, ")") {
			keys = append(keys, vpnIndexKey{name: line, columns: splitVPNIndexColumns(line[len("UNIQUE(") : len(line)-1])})
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, nil, fmt.Errorf("unparseable column definition %q", line)
		}
		bytes, indexable, err := mysql56ColumnBytes(fields[1])
		if err != nil {
			return nil, nil, fmt.Errorf("column %s: %w", fields[0], err)
		}
		columns = append(columns, vpnColumn{name: fields[0], decl: fields[1], bytes: bytes, indexable: indexable})
		rest := strings.ToUpper(strings.Join(fields[2:], " "))
		if strings.Contains(rest, "PRIMARY KEY") {
			keys = append(keys, vpnIndexKey{name: "PRIMARY KEY (" + fields[0] + ")", columns: []string{fields[0]}})
		}
		if strings.HasSuffix(rest, "UNIQUE") {
			keys = append(keys, vpnIndexKey{name: "UNIQUE(" + fields[0] + ")", columns: []string{fields[0]}})
		}
	}
	if len(columns) == 0 {
		return nil, nil, fmt.Errorf("CREATE TABLE body declares no columns")
	}
	return columns, keys, nil
}

// parseVPNCreateIndexes collects the CREATE INDEX statements that target table.
func parseVPNCreateIndexes(script, table string) []vpnIndexKey {
	var keys []vpnIndexKey
	for _, raw := range strings.Split(script, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), ";"))
		if !strings.HasPrefix(line, "CREATE INDEX ") {
			continue
		}
		open := strings.Index(line, " ON "+table+"(")
		if open < 0 || !strings.HasSuffix(line, ")") {
			continue
		}
		keys = append(keys, vpnIndexKey{
			name:    strings.Fields(line)[2],
			columns: splitVPNIndexColumns(line[open+len(" ON "+table+"(") : len(line)-1]),
		})
	}
	return keys
}

func splitVPNIndexColumns(list string) []string {
	var columns []string
	for _, column := range strings.Split(list, ",") {
		if column = strings.TrimSpace(column); column != "" {
			columns = append(columns, column)
		}
	}
	return columns
}

// mysql56ColumnBytes returns the worst-case byte cost of a column type for an
// index key under utf8mb4, and whether MySQL 5.6 can index it at all without a
// prefix length. TEXT and BLOB families charge the inline cap but are reported
// as not indexable so an index over them fails the gate.
func mysql56ColumnBytes(decl string) (int, bool, error) {
	upper := strings.ToUpper(decl)
	kind := upper
	if i := strings.IndexByte(upper, '('); i >= 0 {
		kind = strings.TrimSpace(upper[:i])
	}
	switch {
	case kind == "VARBINARY" || kind == "BINARY":
		width, err := mysql56TypeWidth(upper)
		return width, true, err
	case kind == "VARCHAR" || kind == "CHAR":
		width, err := mysql56TypeWidth(upper)
		return width * 4, true, err
	case strings.HasPrefix(kind, "TEXT") || strings.HasPrefix(kind, "BLOB") ||
		strings.HasPrefix(kind, "TINYTEXT") || strings.HasPrefix(kind, "MEDIUMTEXT") ||
		strings.HasPrefix(kind, "LONGTEXT") || strings.HasPrefix(kind, "TINYBLOB") ||
		strings.HasPrefix(kind, "MEDIUMBLOB") || strings.HasPrefix(kind, "LONGBLOB"):
		return mysql56InlineColumnCap, false, nil
	default:
		return 8, true, nil
	}
}

func mysql56TypeWidth(decl string) (int, error) {
	open := strings.IndexByte(decl, '(')
	closing := strings.IndexByte(decl, ')')
	if open < 0 || closing < open {
		return 0, fmt.Errorf("type %q declares no width", decl)
	}
	width, err := strconv.Atoi(strings.TrimSpace(decl[open+1 : closing]))
	if err != nil {
		return 0, fmt.Errorf("type %q has a non-numeric width: %w", decl, err)
	}
	return width, nil
}

// TestMySQLV14ToV15VPNMigration runs the real migration against a MySQL server
// when one is provided. It is skipped otherwise; the ungated static gates above
// are what keep the scripts honest on machines without MySQL.
func TestMySQLV14ToV15VPNMigration(t *testing.T) {
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
	if _, err := raw.ExecContext(context.Background(), migrations.DDL); err != nil {
		raw.Close()
		t.Fatalf("prepare MySQL schema: %v", err)
	}
	statements := []string{`DROP TABLE IF EXISTS vpn_ip_leases`, `DROP TABLE IF EXISTS vpn_peers`, `UPDATE schema_meta SET version=14 WHERE id=1`}
	for _, statement := range statements {
		if _, err := raw.ExecContext(context.Background(), statement); err != nil {
			raw.Close()
			t.Fatalf("prepare v14 schema: %v", err)
		}
	}
	t.Cleanup(func() {
		if _, err := raw.ExecContext(context.Background(), migrations.DDL); err != nil {
			t.Errorf("restore MySQL schema: %v", err)
		}
		if _, err := raw.ExecContext(context.Background(), `UPDATE schema_meta SET version=? WHERE id=1`, SchemaVersion); err != nil {
			t.Errorf("restore MySQL schema version: %v", err)
		}
		if err := raw.Close(); err != nil {
			t.Errorf("close MySQL schema test database: %v", err)
		}
	})
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenMySQL(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("migrate v14 to v15: %v", err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	for _, table := range vpnSchemaTables {
		var name string
		if err := db.SQL().QueryRowContext(context.Background(),
			`SELECT table_name FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s missing after the migration: %v", table, err)
		}
	}
}
