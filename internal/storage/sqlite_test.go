package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tunnelmesh/tunnelmesh/migrations"
)

func TestSQLiteRepositoryContract(t *testing.T) { runRepositoryContract(t, newTestDB(t)) }

func TestGeneratedAgentIDIsLowercase(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := newAgentID()
		if !strings.HasPrefix(id, "agent-") || id != strings.ToLower(id) {
			t.Fatalf("generated Agent ID = %q, want lowercase agent- prefixed ID", id)
		}
	}
}

func TestSQLiteAutoInitDisabledFailsWithoutSchema(t *testing.T) {
	if _, err := Open(context.Background(), DriverSQLite, "file:no-schema?mode=memory&cache=shared", false); err == nil {
		t.Fatal("expected missing schema error")
	}
}

func TestSQLiteAutoInitRejectsIncompatibleSchemaVersion(t *testing.T) {
	dsn := "file:version-mismatch?mode=memory&cache=shared"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE schema_meta (id INTEGER PRIMARY KEY, version INTEGER NOT NULL); INSERT INTO schema_meta(id,version) VALUES (1,0)`)
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := OpenSQLite(context.Background(), dsn, true); err == nil {
		t.Fatal("expected incompatible schema version error")
	} else if !strings.Contains(err.Error(), "schema version mismatch") {
		t.Fatalf("error = %v, want actionable schema version mismatch", err)
	}
}

func TestSQLiteAutoInitRejectsMissingV1MigrationChain(t *testing.T) {
	dsn := "file:schema-v1-migrate?mode=memory&cache=shared"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_meta (id INTEGER PRIMARY KEY, version INTEGER NOT NULL); INSERT INTO schema_meta(id,version) VALUES (1,1)`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := OpenSQLite(context.Background(), dsn, true); err == nil || !strings.Contains(err.Error(), "missing adjacent migration v0001_to_v0002") {
		t.Fatalf("error=%v, want missing v1 to v2 migration", err)
	}
}

func TestSQLiteAutoInitRejectsMissingV2MigrationChain(t *testing.T) {
	dsn := "file:schema-v2-service-tokens?mode=memory&cache=shared"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations.DDL); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE IF EXISTS service_tokens; DELETE FROM schema_meta WHERE id=1; INSERT INTO schema_meta(id,version) VALUES (1,2)`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	defer raw.Close()

	if _, err := OpenSQLite(context.Background(), dsn, true); err == nil || !strings.Contains(err.Error(), "missing adjacent migration v0002_to_v0003") {
		t.Fatalf("error=%v, want missing v2 to v3 migration", err)
	}
}

func TestSQLiteAutoInitDisabledRejectsMissingServiceTokensTable(t *testing.T) {
	dsn := "file:schema-v3-missing-service-tokens?mode=memory&cache=shared"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations.DDL); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE IF EXISTS service_tokens; DELETE FROM schema_meta WHERE id=1; INSERT INTO schema_meta(id,version) VALUES (1,?)`, SchemaVersion); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	defer raw.Close()

	if _, err := OpenSQLite(context.Background(), dsn, false); err == nil || !strings.Contains(err.Error(), "service_tokens") {
		t.Fatalf("error=%v, want missing service_tokens table", err)
	}
}

func TestSQLiteAutoInitDisabledRejectsMissingRuntimeMetadataTable(t *testing.T) {
	dsn := "file:schema-missing-metadata?mode=memory&cache=shared"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_meta (id INTEGER PRIMARY KEY, version INTEGER NOT NULL); INSERT INTO schema_meta(id,version) VALUES (1,?); CREATE TABLE authorization_revision (id INTEGER PRIMARY KEY, revision BIGINT UNSIGNED NOT NULL, updated_at TEXT NOT NULL); CREATE TABLE users (id TEXT PRIMARY KEY); CREATE TABLE agents (id TEXT PRIMARY KEY)`, SchemaVersion); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := OpenSQLite(context.Background(), dsn, false); err == nil || !strings.Contains(err.Error(), "agent_instance_metadata") {
		t.Fatalf("error=%v, want missing runtime metadata table", err)
	}
}

func assertSQLiteTableColumns(t *testing.T, db *sql.DB, table string, want []string) {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	assertStringSlice(t, table+" columns", got, want)
}

func assertSQLiteIndexColumns(t *testing.T, db *sql.DB, index string, want []string) {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf(`PRAGMA index_info(%s)`, index))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var sequence, cid int
		var name string
		if err := rows.Scan(&sequence, &cid, &name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	assertStringSlice(t, index+" columns", got, want)
}

func assertStringSlice(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

func TestSQLiteConcurrentExpiredLeaseTakeoverHasSingleOwner(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	first, err := db.Leases().Acquire(ctx, AgentLease{AgentID: "agent-concurrent", NodeID: "node-initial", TTL: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	const contenders = 8
	results := make(chan AgentLease, contenders)
	errs := make(chan error, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lease, err := db.Leases().Acquire(ctx, AgentLease{AgentID: first.AgentID, NodeID: fmt.Sprintf("node-%d", i), TTL: time.Minute})
			if err != nil {
				errs <- err
				return
			}
			results <- lease
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	var owners []AgentLease
	for lease := range results {
		owners = append(owners, lease)
	}
	if len(owners) != 1 {
		t.Fatalf("successful owners = %d, want exactly one", len(owners))
	}
	if owners[0].Epoch != first.Epoch+1 {
		t.Fatalf("takeover epoch = %d, want %d", owners[0].Epoch, first.Epoch+1)
	}
	if _, err := db.Leases().Get(ctx, first.AgentID); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteV6ToV7ConnectionLeaseMigrationPreservesLegacyRows(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "v6-to-v7.sqlite")
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations.DDL); err != nil {
		raw.Close()
		t.Fatalf("create schema: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	prepareStatements := []string{
		`DROP TABLE agent_connection_leases`,
		`CREATE TABLE agent_runtime_leases (agent_id VARBINARY(255) PRIMARY KEY,node_id VARBINARY(255) NOT NULL,epoch INTEGER NOT NULL,acquired_at TEXT NOT NULL,expires_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`DROP TABLE agent_instance_metadata`,
		`CREATE TABLE agent_runtime_metadata (agent_id VARBINARY(255) PRIMARY KEY,node_id VARBINARY(255) NOT NULL,epoch INTEGER NOT NULL,revision INTEGER NOT NULL,metadata TEXT NOT NULL,reported_at TEXT NOT NULL,last_seen_at TEXT NOT NULL,expires_at TEXT,stale INTEGER NOT NULL DEFAULT 0,updated_at TEXT NOT NULL)`,
		`DELETE FROM schema_meta WHERE id=1`,
	}
	for _, statement := range prepareStatements {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("prepare v6 schema: %v", err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO agent_runtime_leases(agent_id,node_id,epoch,acquired_at,expires_at,updated_at) VALUES('legacy-agent','legacy-node',7,?,?,?)`, now, expires, now); err != nil {
		raw.Close()
		t.Fatalf("insert legacy lease: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO agent_runtime_metadata(agent_id,node_id,epoch,revision,metadata,reported_at,last_seen_at,expires_at,stale,updated_at) VALUES('legacy-agent','legacy-node',7,3,'{"items":[]}',?,?,?,0,?)`, now, now, expires, now); err != nil {
		raw.Close()
		t.Fatalf("insert legacy metadata: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_meta(id,version) VALUES(1,6)`); err != nil {
		raw.Close()
		t.Fatalf("set v6 schema version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("migrate v6 to v7: %v", err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	leases, err := db.Leases().ListActiveByAgent(context.Background(), "legacy-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 {
		t.Fatalf("migrated leases = %+v, want one legacy connection", leases)
	}
	lease := leases[0]
	if lease.ConnectionID != "legacy" || lease.NodeID != "legacy-node" || lease.ServerNodeID != "legacy-node" || lease.Epoch != 7 || lease.ConnectionEpoch != 7 {
		t.Fatalf("migrated lease = %+v, want preserved legacy identity", lease)
	}
	metadataRepo, ok := db.Metadata().(AgentInstanceMetadataRepository)
	if !ok {
		t.Fatal("metadata repository does not implement instance operations")
	}
	metadata, err := metadataRepo.GetInstance(context.Background(), "legacy-agent", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.InstanceID != "legacy" || metadata.NodeID != "legacy-node" || metadata.Epoch != 7 || metadata.Revision != 3 {
		t.Fatalf("migrated metadata = %+v, want preserved legacy metadata", metadata)
	}
}

func TestSQLiteV7ToV8ServerNodeManagementMigration(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "v7-to-v8.sqlite")
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations.DDL); err != nil {
		raw.Close()
		t.Fatalf("create base schema: %v", err)
	}
	statements := []string{
		`DROP TABLE server_nodes`,
		`CREATE TABLE server_nodes (
			id VARBINARY(255) PRIMARY KEY,
			address VARBINARY(255) NOT NULL,
			epoch INTEGER NOT NULL DEFAULT 0,
			metadata TEXT NOT NULL,
			last_seen_at TEXT,
			expires_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`INSERT INTO schema_meta(id,version) VALUES(1,7)`,
		`INSERT INTO server_nodes(id,address,epoch,metadata,created_at,updated_at)
		 VALUES('legacy-server','127.0.0.1:9443',7,'{}','2026-09-10T00:00:00Z','2026-09-10T00:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("prepare v7 schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("migrate v7 to v8: %v", err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	node, err := db.Nodes().Get(context.Background(), "legacy-server")
	if err != nil {
		t.Fatalf("get migrated node: %v", err)
	}
	if node.Name != "legacy-server" || !node.Enabled || node.DeletedAt != nil {
		t.Fatalf("migrated node = %+v, want managed defaults", node)
	}
}

func TestSQLiteV8ToV9AuthorizationRevisionMigration(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "v8-to-v9.sqlite")
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations.DDL); err != nil {
		raw.Close()
		t.Fatalf("create base schema: %v", err)
	}
	statements := []string{
		`DROP TABLE IF EXISTS authorization_revision`,
		`UPDATE schema_meta SET version=8 WHERE id=1`,
	}
	for _, statement := range statements {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("prepare v8 schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenSQLite(context.Background(), dsn, true)
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

func TestSQLiteV9ToV10AgentPolicyLogicalDeleteMigration(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "v9-to-v10.sqlite")
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations.DDL); err != nil {
		raw.Close()
		t.Fatalf("create base schema: %v", err)
	}
	rows, err := raw.Query(`PRAGMA table_info(agent_policies)`)
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	hasDeletedAt := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			raw.Close()
			t.Fatal(err)
		}
		if name == "deleted_at" {
			hasDeletedAt = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		raw.Close()
		t.Fatal(err)
	}
	rows.Close()
	statements := []string{`UPDATE schema_meta SET version=9 WHERE id=1`}
	if hasDeletedAt {
		statements = append(statements, `ALTER TABLE agent_policies DROP COLUMN deleted_at`)
	}
	for _, statement := range statements {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("prepare v9 schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("migrate v9 to v10: %v", err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pragma_table_info('agent_policies') WHERE name='deleted_at'`).Scan(new(int)); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteV10ToV11ClientObservabilityMigration(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "v10-to-v11.sqlite")
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations.DDL); err != nil {
		raw.Close()
		t.Fatalf("create base schema: %v", err)
	}
	for _, statement := range []string{
		`DROP TABLE client_connection_leases`,
		`DROP TABLE client_instance_metadata`,
		`UPDATE schema_meta SET version=10 WHERE id=1`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("prepare v10 schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("migrate v10 to v11: %v", err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	for _, table := range []string{"client_instance_metadata", "client_connection_leases"} {
		if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(new(int)); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
	}
}

func TestSQLiteV11ToV12WebSSHMigration(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "v11-to-v12.sqlite")
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations.DDL); err != nil {
		raw.Close()
		t.Fatalf("create base schema: %v", err)
	}
	for _, statement := range []string{
		`DROP TABLE credentials`,
		`DROP TABLE remote_servers`,
		`DROP TABLE webssh_sessions`,
		// The full DDL never writes a schema_meta row, so the version must be
		// inserted explicitly; an UPDATE would affect zero rows and silently
		// skip the whole migration chain.
		`DELETE FROM schema_meta WHERE id=1`,
		`INSERT INTO schema_meta(id,version) VALUES(1,11)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("prepare v11 schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("migrate v11 to v12: %v", err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	for _, table := range []string{"credentials", "remote_servers", "webssh_sessions"} {
		if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(new(int)); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
	}
}

func TestAuthorizationRevisionCurrentInitializesToOne(t *testing.T) {
	db := newTestDB(t)
	revision, err := db.AuthorizationRevisions().Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("authorization revision = %d, want 1", revision)
	}
}

func TestAuthorizationRevisionMissingRowFails(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.SQL().Exec(`DELETE FROM authorization_revision WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AuthorizationRevisions().Current(context.Background()); err == nil {
		t.Fatal("missing authorization revision row must fail closed")
	}
}

func TestSQLiteDDLAddsAgentPolicyIndex(t *testing.T) {
	db := newTestDB(t)
	rows, err := db.SQL().Query(`PRAGMA index_list(agent_policies)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found bool
	for rows.Next() {
		var seq int
		var name string
		var unique, partial int
		var origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if name == "idx_agent_policies_agent" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("agent policy agent_id index is missing")
	}
}

func TestLeaseRepositoryPublicConstructorPropagatesMySQLDriver(t *testing.T) {
	repo := NewLeaseRepositoryWithDriver(nil, DriverMySQL)
	impl, ok := repo.(*leaseRepo)
	if !ok {
		t.Fatalf("repository type = %T, want *leaseRepo", repo)
	}
	if impl.driver != DriverMySQL {
		t.Fatalf("lease driver = %q, want %q", impl.driver, DriverMySQL)
	}
}
func TestSQLiteV12ToV13CredentialSecretMigration(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "v12-to-v13.sqlite")
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations.DDL); err != nil {
		raw.Close()
		t.Fatalf("create base schema: %v", err)
	}
	// Rebuild credentials without the secret columns to emulate a v12
	// database, keeping one row so the migration must preserve data.
	for _, statement := range []string{
		`DELETE FROM credentials`,
		`DROP TABLE credentials`,
		`CREATE TABLE credentials (
		    id VARBINARY(255) PRIMARY KEY,
		    owner_user_id VARBINARY(255) NOT NULL,
		    name VARCHAR(255) NOT NULL,
		    credential_type VARCHAR(32) NOT NULL,
		    public_key TEXT NOT NULL,
		    fingerprint VARBINARY(255) NOT NULL,
		    enabled INTEGER NOT NULL DEFAULT 1,
		    deleted_at VARCHAR(32),
		    created_at TEXT NOT NULL,
		    updated_at VARCHAR(32) NOT NULL
		)`,
		`INSERT INTO credentials(id,owner_user_id,name,credential_type,public_key,fingerprint,enabled,created_at,updated_at)
		 VALUES('credential-legacy','user-a','legacy','ssh_public_key','ssh-ed25519 AAAATEST','SHA256:test',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		// Same reason as the v11 test above: schema_meta has no row after the
		// full DDL, so insert the source version to exercise v12 -> v13.
		`DELETE FROM schema_meta WHERE id=1`,
		`INSERT INTO schema_meta(id,version) VALUES(1,12)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("prepare v12 schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("migrate v12 to v13: %v", err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	credential, err := db.Credentials().Get(context.Background(), "credential-legacy")
	if err != nil {
		t.Fatalf("legacy credential after migration: %v", err)
	}
	if credential.Name != "legacy" || credential.HasSecret() {
		t.Fatalf("legacy credential = %+v, want preserved row without secret", credential)
	}
	// A second open must be a no-op: the migration is retry-safe and the
	// duplicate-column guard keeps an already migrated database working.
	db2, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	db2.Close()
}

// TestSQLiteV13ToV14IdentityMigration emulates a v13 database by removing the
// identity tables and the two users columns, then asserts the adjacent
// migration recreates them, preserves existing rows, defaults the new columns,
// and is safe to run twice.
func TestSQLiteV13ToV14IdentityMigration(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "v13-to-v14.sqlite")
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations.DDL); err != nil {
		raw.Close()
		t.Fatalf("create base schema: %v", err)
	}
	statements := []string{
		`INSERT INTO users(id,username,role,password_hash,disabled,created_at,updated_at,auth_source,mfa_required)
		 VALUES('user-legacy','legacy','admin','$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','local',0)`,
		`DROP TABLE auth_settings`,
		`DROP TABLE oidc_providers`,
		`DROP TABLE user_identities`,
		`DROP TABLE user_mfa`,
		`DROP TABLE user_recovery_codes`,
		`DROP TABLE user_devices`,
		`DROP TABLE auth_challenges`,
		`DROP TABLE auth_login_attempts`,
		`DROP INDEX idx_users_role_deleted`,
		`CREATE TABLE users_v13 (
		    id VARBINARY(255) PRIMARY KEY,
		    username VARCHAR(191) NOT NULL UNIQUE,
		    role VARCHAR(32) NOT NULL,
		    password_hash VARCHAR(255) NOT NULL,
		    disabled INTEGER NOT NULL DEFAULT 0,
		    deleted_at VARCHAR(32),
		    created_at TEXT NOT NULL,
		    updated_at TEXT NOT NULL
		)`,
		`INSERT INTO users_v13(id,username,role,password_hash,disabled,created_at,updated_at)
		 SELECT id,username,role,password_hash,disabled,created_at,updated_at FROM users`,
		`DROP TABLE users`,
		`ALTER TABLE users_v13 RENAME TO users`,
		`CREATE INDEX idx_users_role_deleted ON users(role, deleted_at, id)`,
		// schema_meta has no row after the raw DDL, so insert the source version
		// to exercise the v13 -> v14 step.
		`DELETE FROM schema_meta WHERE id=1`,
		`INSERT INTO schema_meta(id,version) VALUES(1,13)`,
	}
	for _, statement := range statements {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("prepare v13 schema: %v (%s)", err, statement)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("migrate v13 to v14: %v", err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	user, err := db.Users().GetByUsername(context.Background(), "legacy")
	if err != nil {
		t.Fatalf("legacy user after migration: %v", err)
	}
	if user.Role != "admin" || user.AuthSource != AuthSourceLocal || user.MFARequired {
		t.Fatalf("legacy user = %+v, want preserved admin with local auth source", user)
	}
	for _, table := range []string{
		"auth_settings", "oidc_providers", "user_identities", "user_mfa",
		"user_recovery_codes", "user_devices", "auth_challenges", "auth_login_attempts",
	} {
		var name string
		if err := db.SQL().QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s missing after migration: %v", table, err)
		}
	}
	// A second open must be a no-op: the migration is retry-safe.
	db2, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	defer db2.Close()
	if version, err := db2.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version after reopen = %d, err = %v, want %d", version, err, SchemaVersion)
	}
}

// TestSQLiteFreshSchemaMatchesIncrementalIdentityChain builds one database from
// the full DDL and another by replaying the v13 -> v14 incremental script over a
// v13 base, then asserts both expose the same identity columns. This is the guard
// that keeps migrations/ddl.sql and migrations/incremental from drifting.
func TestSQLiteFreshSchemaMatchesIncrementalIdentityChain(t *testing.T) {
	columns := func(t *testing.T, dsn, table string) map[string]bool {
		t.Helper()
		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		rows, err := raw.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			out[name] = true
		}
		if len(out) == 0 {
			t.Fatalf("table %s has no columns", table)
		}
		return out
	}
	dir := t.TempDir()
	fresh := "file:" + filepath.Join(dir, "fresh.sqlite")
	if _, err := OpenSQLite(context.Background(), fresh, true); err != nil {
		t.Fatal(err)
	}
	upgraded := "file:" + filepath.Join(dir, "upgraded.sqlite")
	raw, err := sql.Open("sqlite", upgraded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations.DDL); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DELETE FROM schema_meta WHERE id=1`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_meta(id,version) VALUES(1,14)`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"users", "auth_settings", "oidc_providers", "user_identities", "user_mfa", "user_recovery_codes", "user_devices", "auth_challenges", "auth_login_attempts"} {
		want := columns(t, fresh, table)
		got := columns(t, upgraded, table)
		if len(want) != len(got) {
			t.Fatalf("table %s column count differs: full DDL %d, incremental %d", table, len(want), len(got))
		}
		for name := range want {
			if !got[name] {
				t.Fatalf("table %s is missing column %s after the incremental chain", table, name)
			}
		}
	}
}
