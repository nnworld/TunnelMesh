package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/migrations"
	_ "modernc.org/sqlite"
)

func TestApplySchemaStatementsIgnoresSemicolonsInComments(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "comment-splitter.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	script := `-- production incident: comment text contains a separator;
CREATE TABLE comment_probe(id INTEGER PRIMARY KEY);
/* block comment; also contains a separator */
CREATE TABLE comment_probe_two(id INTEGER PRIMARY KEY);`

	if err := applySchemaStatements(context.Background(), db, script, false); err != nil {
		t.Fatalf("apply statements with comment semicolons: %v", err)
	}
	for _, table := range []string{"comment_probe", "comment_probe_two"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
}

func TestSplitSQLStatementsParsesPublishedV14ToV15Migration(t *testing.T) {
	statements := splitSQLStatements(migrations.V14ToV15MySQL)
	want := []string{
		"ALTER TABLE client_connection_leases MODIFY connection_epoch BIGINT NOT NULL",
		"ALTER TABLE agent_connection_leases MODIFY connection_epoch BIGINT NOT NULL",
	}
	if len(statements) != len(want) {
		t.Fatalf("statement count = %d, want %d: %#v", len(statements), len(want), statements)
	}
	for index, statement := range want {
		if statements[index] != statement {
			t.Fatalf("statement %d = %q, want %q", index, statements[index], statement)
		}
	}
}

func TestSplitSQLStatementsPreservesSemicolonsInQuotes(t *testing.T) {
	script := `CREATE TABLE quote_probe(value TEXT);
INSERT INTO quote_probe(value) VALUES('a;b');
INSERT INTO quote_probe(value) VALUES('It''s;c');
INSERT INTO quote_probe(value) VALUES("d;e");`

	statements := splitSQLStatements(script)
	if len(statements) != 4 {
		t.Fatalf("statement count = %d, want 4: %#v", len(statements), statements)
	}
	if statements[1] != `INSERT INTO quote_probe(value) VALUES('a;b')` {
		t.Fatalf("single-quoted statement = %q", statements[1])
	}
	if statements[2] != `INSERT INTO quote_probe(value) VALUES('It''s;c')` {
		t.Fatalf("escaped quote statement = %q", statements[2])
	}
	if statements[3] != `INSERT INTO quote_probe(value) VALUES("d;e")` {
		t.Fatalf("double-quoted statement = %q", statements[3])
	}
}
