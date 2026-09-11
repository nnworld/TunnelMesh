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
