package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strings"
	"testing"
	"time"
)

const contractClientOwner = "contract-client-owner"

// runClientRepositoryContract exercises the Client observability SQL behind the
// console list, the detail drawer and the summary cards: the EXISTS presence
// predicates, the metadata freshness axis, the correlated aggregates, the
// guarded deletes and the 64-bit connection epoch. It is called from
// runRepositoryContract, so SQLite and a real MySQL server run byte-identical
// assertions. Both the connection_epoch clamping defect and the JSON "null"
// capabilities defect survived on MySQL only because nothing here ever talked to
// a MySQL driver, which is what the mysql56 CI job now prevents.
//
// Every assertion is scoped to contractClientOwner so the counters stay exact
// even when other tests already wrote Client rows into the same database.
func runClientRepositoryContract(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	// List and Summarize evaluate presence against their own clock, so the
	// fixtures must be anchored to real now rather than a frozen date. Truncating
	// to seconds keeps every RFC3339Nano column value the same width, which is
	// what makes the lexicographic ordering used by the cursor valid.
	now := time.Now().UTC().Truncate(time.Second)
	live := now.Add(time.Minute)
	dead := now.Add(-time.Minute)
	// The Server derives connection_epoch from 8 random bytes, so a fencing token
	// above MaxInt32 is the normal production case, not an edge case.
	const epoch = int64(math.MaxInt32) + 24242
	const agentID = "contract-agent-1"
	metadataFor := func(instanceID, agent string) string {
		return `{"instance_id":"` + instanceID + `","agentIds":["` + agent + `"]"}`
	}

	onlineID := putContractClientInstance(t, db, "contract-client-online", metadataFor("contract-client-online", agentID), false, now, live)
	unreportedID := putContractClientInstance(t, db, "contract-client-unreported", `{}`, false, now, live)
	orphanID := putContractClientInstance(t, db, "contract-client-orphan", `{}`, true, dead, dead)
	staleID := putContractClientInstance(t, db, "contract-client-stale", metadataFor("contract-client-stale", agentID), true, dead, dead)
	putContractClientInstance(t, db, "contract-client-neighbour", metadataFor("contract-client-neighbour", "contract-agent-12"), false, now, live)

	// The capabilities field is `capabilities,omitempty` on the wire, so a Client
	// that never advertises sends JSON null, and json.Marshal of a nil slice is
	// "null" too. Only "[]" is a usable array, so the repository must normalise
	// both literals rather than let a non-JSON literal reach every reader.
	for _, raw := range []string{"null", ""} {
		stored, err := db.ClientInstances().Upsert(ctx, ClientInstance{
			OwnerUserID: contractClientOwner, InstanceID: "contract-client-online",
			Metadata: metadataFor("contract-client-online", agentID), Capabilities: raw,
			ReportedAt: now, LastSeenAt: now, ExpiresAt: &live, UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("re-upsert capabilities %q: %v", raw, err)
		}
		// The primary key must be reused: MySQL takes a FOR UPDATE row lock here
		// and a second INSERT would fork the durable install identity.
		if stored.ID != onlineID {
			t.Fatalf("re-upsert with capabilities %q created %s, want existing %s", raw, stored.ID, onlineID)
		}
		got, err := db.ClientInstances().GetByOwnerAndInstance(ctx, contractClientOwner, "contract-client-online")
		if err != nil {
			t.Fatalf("get after re-upsert with capabilities %q: %v", raw, err)
		}
		if got.Capabilities != "[]" {
			t.Fatalf("capabilities %q stored as %q, want []", raw, got.Capabilities)
		}
	}

	onlineLease := putContractClientLease(t, db, "contract-connection-online", onlineID, 3, epoch, live, now)
	putContractClientLease(t, db, "contract-connection-unreported", unreportedID, 1, epoch+1, live, now)

	// The counters must describe the whole filtered population, not the page a
	// caller happens to be looking at, and must agree with the list rows.
	assertClientContractSummary(t, db, "", ClientInstanceSummary{
		Total: 5, Online: 2, ActiveConnections: 2, ActiveStreams: 4, MetadataUnavailable: 2, MetadataStale: 1,
	})
	assertClientContractSummary(t, db, "fresh", ClientInstanceSummary{Total: 2, Online: 1, ActiveConnections: 1, ActiveStreams: 3})
	assertClientContractSummary(t, db, "reported", ClientInstanceSummary{
		Total: 3, Online: 1, ActiveConnections: 1, ActiveStreams: 3, MetadataStale: 1,
	})
	assertClientContractSummary(t, db, "unavailable", ClientInstanceSummary{
		Total: 2, Online: 1, ActiveConnections: 1, ActiveStreams: 1, MetadataUnavailable: 2,
	})
	assertClientContractSummary(t, db, "expired", ClientInstanceSummary{Total: 1, MetadataStale: 1})

	assertContractClientRows(t, db, ClientInstanceFilter{Status: "online"}, "contract-client-online,contract-client-unreported")
	assertContractClientRows(t, db, ClientInstanceFilter{Status: "offline"}, "contract-client-neighbour,contract-client-orphan,contract-client-stale")
	assertContractClientRows(t, db, ClientInstanceFilter{MetadataState: "fresh"}, "contract-client-neighbour,contract-client-online")
	assertContractClientRows(t, db, ClientInstanceFilter{MetadataState: "unavailable"}, "contract-client-orphan,contract-client-unreported")
	assertContractClientRows(t, db, ClientInstanceFilter{MetadataState: "expired"}, "contract-client-stale")
	// The agent filter matches the quoted JSON string, so contract-agent-1 must
	// not also select contract-agent-12.
	assertContractClientRows(t, db, ClientInstanceFilter{AgentID: agentID}, "contract-client-online,contract-client-stale")
	assertContractClientRows(t, db, ClientInstanceFilter{Keyword: "contract-client-neighbour"}, "contract-client-neighbour")

	// Presence and metadata freshness are orthogonal axes: marking the snapshot
	// stale must not change which Clients are online, and a metadata refresh must
	// clear the stale flag without touching the lease.
	if err := db.ClientInstances().MarkStale(ctx, onlineID, now); err != nil {
		t.Fatalf("mark client instance stale: %v", err)
	}
	assertClientContractSummary(t, db, "expired", ClientInstanceSummary{
		Total: 2, Online: 1, ActiveConnections: 1, ActiveStreams: 3, MetadataStale: 2,
	})
	assertContractClientRows(t, db, ClientInstanceFilter{Status: "online"}, "contract-client-online,contract-client-unreported")
	if err := db.ClientInstances().TouchInstance(ctx, contractClientOwner, "contract-client-online", now, live); err != nil {
		t.Fatalf("touch client instance: %v", err)
	}
	assertClientContractSummary(t, db, "", ClientInstanceSummary{
		Total: 5, Online: 2, ActiveConnections: 2, ActiveStreams: 4, MetadataUnavailable: 2, MetadataStale: 1,
	})

	storedLease, err := db.ClientConnections().Get(ctx, onlineLease.ConnectionID)
	if err != nil {
		t.Fatalf("get client lease: %v", err)
	}
	if storedLease.ConnectionEpoch != epoch {
		t.Fatalf("stored connection_epoch = %d, want %d (the column clamped a 64-bit fencing token)", storedLease.ConnectionEpoch, epoch)
	}
	if err := db.ClientConnections().Renew(ctx, onlineLease.ConnectionID, epoch, 90*time.Second); err != nil {
		t.Fatalf("renew with the authoritative epoch: %v", err)
	}
	if err := db.ClientConnections().UpdateStats(ctx, ClientConnectionLease{
		ConnectionID: onlineLease.ConnectionID, ConnectionEpoch: epoch, ActiveStreams: 7, HealthScore: 80, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("update stats with the authoritative epoch: %v", err)
	}
	// The cards and the table read the same population, so a stats write must be
	// visible in the aggregate immediately.
	assertClientContractSummary(t, db, "", ClientInstanceSummary{
		Total: 5, Online: 2, ActiveConnections: 2, ActiveStreams: 8, MetadataUnavailable: 2, MetadataStale: 1,
	})
	if err := db.ClientConnections().Release(ctx, onlineLease.ConnectionID, epoch+1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("release with a stale epoch error = %v, want sql.ErrNoRows", err)
	}
	if err := db.ClientConnections().Release(ctx, onlineLease.ConnectionID, epoch); err != nil {
		t.Fatalf("release with the authoritative epoch: %v", err)
	}
	if leases, err := db.ClientConnections().ListByInstance(ctx, onlineID); err != nil {
		t.Fatalf("list leases for released client: %v", err)
	} else if len(leases) != 0 {
		t.Fatalf("leases after release = %+v, want none", leases)
	}
	assertClientContractSummary(t, db, "", ClientInstanceSummary{
		Total: 5, Online: 1, ActiveConnections: 1, ActiveStreams: 1, MetadataUnavailable: 2, MetadataStale: 1,
	})

	// A row that accepted a CLIENT_HELLO is durable install identity and must
	// survive a late Release; a never-reported row must be deletable by id.
	if err := db.ClientInstances().DeleteUnreported(ctx, orphanID); err != nil {
		t.Fatalf("delete unreported client instance: %v", err)
	}
	if _, err := db.ClientInstances().Get(ctx, orphanID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted unreported row error = %v, want sql.ErrNoRows", err)
	}
	if err := db.ClientInstances().DeleteUnreported(ctx, staleID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("delete reported row error = %v, want sql.ErrNoRows", err)
	}
	if _, err := db.ClientInstances().Get(ctx, staleID); err != nil {
		t.Fatalf("reported client instance was deleted: %v", err)
	}

	// The reaper drops only lease-less never-reported rows whose TTL lapsed: one
	// mid-handshake row and one connected row must both survive it.
	purgedID := putContractClientInstance(t, db, "contract-client-purged", `{}`, true, dead, dead)
	handshakeID := putContractClientInstance(t, db, "contract-client-handshake", `{}`, false, now, live)
	purged, err := db.ClientInstances().PurgeUnreported(ctx, now)
	if err != nil {
		t.Fatalf("purge unreported client instances: %v", err)
	}
	if purged < 1 {
		t.Fatalf("purged = %d, want at least the expired unreported row", purged)
	}
	assertContractClientPresent(t, db, purgedID, false)
	assertContractClientPresent(t, db, handshakeID, true)
	assertContractClientPresent(t, db, unreportedID, true)
	assertContractClientPresent(t, db, staleID, true)

	// Lapsed rows that still claim fresh metadata are swept into expired.
	lapsedID := putContractClientInstance(t, db, "contract-client-lapsed", metadataFor("contract-client-lapsed", agentID), false, dead, dead)
	changed, err := db.ClientInstances().MarkExpired(ctx, now)
	if err != nil {
		t.Fatalf("mark expired client instances: %v", err)
	}
	if changed < 1 {
		t.Fatalf("MarkExpired changed %d rows, want at least the lapsed fixture", changed)
	}
	if got, err := db.ClientInstances().Get(ctx, lapsedID); err != nil {
		t.Fatalf("get lapsed client instance: %v", err)
	} else if !got.Stale {
		t.Fatal("lapsed client instance was not marked stale")
	}

	// A full walk of the owner population proves the composite cursor
	// (updated_at, id) terminates and repeats nothing while several rows share
	// the same timestamp.
	assertContractClientRows(t, db, ClientInstanceFilter{}, strings.Join([]string{
		"contract-client-handshake", "contract-client-lapsed", "contract-client-neighbour",
		"contract-client-online", "contract-client-stale", "contract-client-unreported",
	}, ","))
}

// putContractClientInstance stores one Client metadata row with an explicit
// payload and returns the generated primary key. Metadata is the only signal
// separating "CLIENT_HELLO accepted" from the literal {} written by the legacy
// register path, so a contract has to place both kinds side by side.
func putContractClientInstance(t *testing.T, db *DB, instanceID, metadata string, stale bool, at, expiresAt time.Time) string {
	t.Helper()
	stored, err := db.ClientInstances().Upsert(context.Background(), ClientInstance{
		OwnerUserID: contractClientOwner, InstanceID: instanceID, Metadata: metadata,
		Capabilities: `["client_metadata.v1"]`, ReportedAt: at, LastSeenAt: at,
		ExpiresAt: &expiresAt, Stale: stale, UpdatedAt: at,
	})
	if err != nil {
		t.Fatalf("upsert client instance %s: %v", instanceID, err)
	}
	return stored.ID
}

// putContractClientLease stores a connection lease with an explicit fencing
// token and stream count so the contract can prove the epoch survives the
// driver's integer width and that aggregates count real leases.
func putContractClientLease(t *testing.T, db *DB, connectionID, clientInstanceID string, activeStreams, epoch int64, expiresAt, at time.Time) ClientConnectionLease {
	t.Helper()
	lease, err := db.ClientConnections().Register(context.Background(), ClientConnectionLease{
		ConnectionID: connectionID, ClientInstanceID: clientInstanceID,
		TokenID: "contract-token-" + connectionID, OwnerUserID: contractClientOwner,
		ServerNodeID: "contract-server-a", ConnectionEpoch: epoch, ActiveStreams: activeStreams,
		AcquiredAt: at.Add(-time.Hour), ExpiresAt: expiresAt, UpdatedAt: at,
	})
	if err != nil {
		t.Fatalf("register client lease %s: %v", connectionID, err)
	}
	return lease
}

// assertClientContractSummary compares the aggregate counters for one metadata
// state over the contract owner's population.
func assertClientContractSummary(t *testing.T, db *DB, metadataState string, want ClientInstanceSummary) {
	t.Helper()
	got, err := db.ClientInstances().Summarize(context.Background(), ClientInstanceFilter{
		OwnerUserID: contractClientOwner, MetadataState: metadataState,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("summarize metadata state %q: %v", metadataState, err)
	}
	if got != want {
		t.Fatalf("summary %q = %+v, want %+v", metadataState, got, want)
	}
}

// assertContractClientRows compares the instance ids a filter selects, so a
// predicate can neither over- nor under-match without failing the contract.
func assertContractClientRows(t *testing.T, db *DB, filter ClientInstanceFilter, want string) {
	t.Helper()
	var ids []string
	for _, instance := range walkContractClientInstances(t, db, filter) {
		ids = append(ids, instance.InstanceID)
	}
	sort.Strings(ids)
	if got := strings.Join(ids, ","); got != want {
		t.Fatalf("client instances for %+v = %q, want %q", filter, got, want)
	}
}

// assertContractClientPresent checks one row by primary key.
func assertContractClientPresent(t *testing.T, db *DB, id string, want bool) {
	t.Helper()
	_, err := db.ClientInstances().Get(context.Background(), id)
	if present := err == nil; present != want {
		t.Fatalf("client instance %s present = %v (err %v), want %v", id, present, err, want)
	}
}

// walkContractClientInstances reads the whole filtered owner population through
// the cursor API with a page size of two, which is small enough to force several
// pages for the fixtures in this contract.
func walkContractClientInstances(t *testing.T, db *DB, filter ClientInstanceFilter) []ClientInstance {
	t.Helper()
	filter.OwnerUserID = contractClientOwner
	var out []ClientInstance
	cursor := ""
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("client instance cursor never terminated")
		}
		got, err := db.ClientInstances().List(context.Background(), filter, cursor, 2)
		if err != nil {
			t.Fatalf("list client instances (page %d): %v", page, err)
		}
		for _, instance := range got.Items {
			for _, seen := range out {
				if seen.ID == instance.ID {
					t.Fatalf("cursor page %d repeated client instance %s", page, instance.ID)
				}
			}
			out = append(out, instance)
		}
		if !got.HasMore {
			return out
		}
		if got.NextCursor == "" {
			t.Fatalf("client instance page %d reports more rows without a cursor", page)
		}
		cursor = got.NextCursor
	}
}
