package storage

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tunnelmesh/tunnelmesh/migrations"
)

func TestAccountMigrationV5ToV6SQLite(t *testing.T) {
	dsn := "file:account-migration-v5-v6?mode=memory&cache=shared"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	legacyDDL := strings.ReplaceAll(migrations.DDL, "    disabled INTEGER NOT NULL DEFAULT 0,\n    deleted_at VARCHAR(32),\n", "    disabled INTEGER NOT NULL DEFAULT 0,\n")
	legacyDDL = strings.ReplaceAll(legacyDDL, "CREATE INDEX idx_users_role_deleted ON users(role, deleted_at, id);\n", "")
	if _, err := raw.Exec(legacyDDL); err != nil {
		t.Fatalf("create v5 schema: %v", err)
	}
	if _, err := raw.Exec(`DELETE FROM schema_meta; INSERT INTO schema_meta(id,version) VALUES(1,5)`); err != nil {
		t.Fatalf("set v5 schema version: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS agent_runtime_leases (agent_id VARBINARY(255) PRIMARY KEY,node_id VARBINARY(255) NOT NULL,epoch INTEGER NOT NULL,acquired_at TEXT NOT NULL,expires_at TEXT NOT NULL,updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create v5 runtime lease table: %v", err)
	}
	if _, err := raw.Exec(`DROP TABLE agent_instance_metadata; CREATE TABLE agent_runtime_metadata (agent_id VARBINARY(255) PRIMARY KEY,node_id VARBINARY(255) NOT NULL,epoch INTEGER NOT NULL,revision INTEGER NOT NULL,metadata TEXT NOT NULL,reported_at TEXT NOT NULL,last_seen_at TEXT NOT NULL,expires_at TEXT,stale INTEGER NOT NULL DEFAULT 0,updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create v5 runtime metadata table: %v", err)
	}

	db, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("migrate v5 to v6: %v", err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	assertSQLiteTableColumnsContains(t, db.SQL(), "users", "deleted_at")
	assertSQLiteIndexColumns(t, db.SQL(), "idx_users_role_deleted", []string{"role", "deleted_at", "id"})
}

func TestAccountMigrationDoesNotAdvanceVersionForWrongExistingIndex(t *testing.T) {
	dsn := "file:account-migration-wrong-index?mode=memory&cache=shared"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	legacyDDL := strings.ReplaceAll(migrations.DDL, "    disabled INTEGER NOT NULL DEFAULT 0,\n    deleted_at VARCHAR(32),\n", "    disabled INTEGER NOT NULL DEFAULT 0,\n")
	legacyDDL = strings.ReplaceAll(legacyDDL, "CREATE INDEX idx_users_role_deleted ON users(role, deleted_at, id);\n", "")
	if _, err := raw.Exec(legacyDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DELETE FROM schema_meta; INSERT INTO schema_meta(id,version) VALUES(1,5); CREATE INDEX idx_users_role_deleted ON users(role,id)`); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSQLite(context.Background(), dsn, true); err == nil || !strings.Contains(err.Error(), "idx_users_role_deleted") {
		t.Fatalf("error = %v, want invalid account index", err)
	}
	var version int
	if err := raw.QueryRow(`SELECT version FROM schema_meta WHERE id=1`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("schema version = %d, err = %v, want unchanged version 5", version, err)
	}
}

func TestAccountRepositoryListsAndRestoresLogicalDeletes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo, ok := db.Users().(AccountUserRepository)
	if !ok {
		t.Fatal("user repository does not implement AccountUserRepository")
	}
	for _, user := range []User{
		{ID: "admin-a", Username: "admin-a", Role: "admin", PasswordHash: "hash"},
		{ID: "user-a", Username: "user-a", Role: "user", PasswordHash: "hash"},
		{ID: "user-b", Username: "user-b", Role: "user", PasswordHash: "hash"},
	} {
		if err := repo.Create(ctx, user); err != nil {
			t.Fatalf("create %s: %v", user.ID, err)
		}
	}
	if err := db.Tokens().Create(ctx, APIToken{ID: "token-user-b", UserID: "user-b", TokenHash: "hash-user-b"}); err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC)
	if err := repo.SoftDelete(ctx, "user-b", deletedAt); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	active, err := repo.ListChildren(ctx, AccountStatusActive, "", 10)
	if err != nil || len(active.Items) != 1 || active.Items[0].ID != "user-a" {
		t.Fatalf("active page = %+v, err = %v", active, err)
	}
	deleted, err := repo.ListChildren(ctx, AccountStatusDeleted, "", 10)
	if err != nil || len(deleted.Items) != 1 || deleted.Items[0].ID != "user-b" || deleted.Items[0].DeletedAt == nil || !deleted.Items[0].Disabled {
		t.Fatalf("deleted page = %+v, err = %v", deleted, err)
	}
	all, err := repo.ListChildren(ctx, AccountStatusAll, "", 10)
	if err != nil || len(all.Items) != 2 {
		t.Fatalf("all page = %+v, err = %v", all, err)
	}
	if _, err := db.Tokens().Get(ctx, "token-user-b"); err != nil {
		t.Fatalf("related token was removed: %v", err)
	}

	if err := repo.Restore(ctx, "user-b"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restored, err := repo.Get(ctx, "user-b")
	if err != nil || restored.DeletedAt != nil || restored.Disabled {
		t.Fatalf("restored user = %+v, err = %v", restored, err)
	}
}

func TestMySQLV5ToV6MigrationUsesCompatibleDDL(t *testing.T) {
	if strings.Contains(strings.ToUpper(migrations.V5ToV6MySQL), "IF NOT EXISTS") {
		t.Fatal("MySQL 5.6 migration must not use ADD COLUMN IF NOT EXISTS")
	}
	for _, fragment := range []string{"ALTER TABLE users ADD COLUMN deleted_at VARCHAR(32)", "CREATE INDEX idx_users_role_deleted"} {
		if !strings.Contains(migrations.V5ToV6MySQL, fragment) {
			t.Fatalf("MySQL migration missing %q", fragment)
		}
	}
}

func assertSQLiteTableColumnsContains(t *testing.T, db *sql.DB, table, want string) {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == want {
			return
		}
	}
	t.Fatalf("table %s does not contain column %s", table, want)
}
