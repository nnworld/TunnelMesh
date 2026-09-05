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
