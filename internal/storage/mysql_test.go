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
