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

func TestMySQLAutoInitMigratesSchemaV2ToServiceTokens(t *testing.T) {
	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
	}
	raw := prepareMySQLServiceTokenSchemaTest(t, dsn, 2)

	db, err := OpenMySQL(context.Background(), dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	assertMySQLTableColumns(t, raw, "service_tokens", []string{
		"id", "token_type", "owner_user_id", "agent_id", "node_id",
		"token_prefix", "token_hash", "scope", "expires_at", "revoked_at",
		"last_used_at", "created_at", "updated_at",
	})
	assertMySQLIndexColumns(t, raw, "idx_service_tokens_owner_type", []string{"owner_user_id", "token_type", "id"})
	assertMySQLIndexColumns(t, raw, "idx_service_tokens_agent", []string{"agent_id", "token_type", "id"})
	assertMySQLIndexColumns(t, raw, "idx_service_tokens_node", []string{"node_id", "token_type", "id"})

	version, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, SchemaVersion)
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
		if _, err := raw.ExecContext(context.Background(), `UPDATE schema_meta SET version=2 WHERE id=1`); err != nil {
			t.Errorf("restore MySQL schema version: %v", err)
			_ = raw.Close()
			return
		}
		db, err := OpenMySQL(context.Background(), dsn, true)
		if err != nil {
			t.Errorf("restore MySQL service_tokens schema: %v", err)
		} else if err := db.Close(); err != nil {
			t.Errorf("close restored MySQL database: %v", err)
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
