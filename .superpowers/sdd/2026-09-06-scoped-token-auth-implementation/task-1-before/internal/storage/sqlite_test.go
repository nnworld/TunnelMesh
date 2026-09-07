package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSQLiteRepositoryContract(t *testing.T) { runRepositoryContract(t, newTestDB(t)) }

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

func TestSQLiteAutoInitMigratesSchemaV1ToCurrent(t *testing.T) {
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
	db, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	version, err := db.SchemaVersion(context.Background())
	if err != nil || version != SchemaVersion {
		t.Fatalf("version=%d err=%v, want %d", version, err, SchemaVersion)
	}
	if _, err := db.SQL().Exec(`INSERT INTO agent_runtime_metadata(agent_id,node_id,epoch,revision,metadata,reported_at,last_seen_at,stale,updated_at) VALUES ('a','n',1,1,'{}','now','now',0,'now')`); err != nil {
		t.Fatalf("metadata table unavailable after migration: %v", err)
	}
}

func TestSQLiteAutoInitDisabledRejectsMissingRuntimeMetadataTable(t *testing.T) {
	dsn := "file:schema-missing-metadata?mode=memory&cache=shared"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_meta (id INTEGER PRIMARY KEY, version INTEGER NOT NULL); INSERT INTO schema_meta(id,version) VALUES (1,2); CREATE TABLE users (id TEXT PRIMARY KEY); CREATE TABLE agents (id TEXT PRIMARY KEY)`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := OpenSQLite(context.Background(), dsn, false); err == nil || !strings.Contains(err.Error(), "agent_runtime_metadata") {
		t.Fatalf("error=%v, want missing runtime metadata table", err)
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
