package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tunnelmesh/tunnelmesh/migrations"
)

// vpnSchemaTables are the two tables schema v15 adds for the embedded VPN
// gateway. They are asserted together everywhere because a migration that
// creates one without the other leaves the node unable to serve peers.
var vpnSchemaTables = []string{"vpn_peers", "vpn_ip_leases"}

// prepareV14Base builds a database that really is at v14: the full DDL minus
// the two VPN tables, with schema_meta pinned to 14. DROP TABLE removes the
// table's indexes with it, so no DROP INDEX statement is needed (or valid).
func prepareV14Base(t *testing.T, dsn string) {
	t.Helper()
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{migrations.DDL}
	for _, table := range vpnSchemaTables {
		statements = append(statements, `DROP TABLE `+table)
	}
	statements = append(statements,
		`DELETE FROM schema_meta WHERE id=1`,
		`INSERT INTO schema_meta(id,version) VALUES(1,14)`,
	)
	for _, statement := range statements {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("prepare v14 base: %v (%s)", err, firstLine(statement))
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
}

func firstLine(statement string) string {
	if i := strings.IndexByte(statement, '\n'); i >= 0 {
		return statement[:i]
	}
	return statement
}

func assertSQLiteTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var name string
	if err := db.QueryRowContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
		t.Fatalf("table %s missing: %v", table, err)
	}
}

// TestSQLiteV14ToV15VPNMigration upgrades a real v14 database and asserts the
// migration is additive, preserves existing rows, and is safe to re-run.
func TestSQLiteV14ToV15VPNMigration(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "v14-to-v15.sqlite")
	prepareV14Base(t, dsn)

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO users(id,username,role,password_hash,disabled,created_at,updated_at,auth_source,mfa_required)
		VALUES('user-legacy','legacy','admin','$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','local',0)`); err != nil {
		raw.Close()
		t.Fatalf("seed legacy user: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatalf("migrate v14 to v15: %v", err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	for _, table := range vpnSchemaTables {
		assertSQLiteTableExists(t, db.SQL(), table)
	}
	user, err := db.Users().Get(context.Background(), "user-legacy")
	if err != nil {
		t.Fatalf("legacy user after migration: %v", err)
	}
	if user.Role != "admin" || user.Username != "legacy" {
		t.Fatalf("legacy user = %+v, want the row seeded before the migration", user)
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

// TestSQLiteFreshSchemaMatchesIncrementalVPNChain is the drift guard between
// migrations/ddl.sql and migrations/incremental: one database built from the
// full DDL and one upgraded from a real v14 base must expose the same columns
// and the same indexes. Unlike the identity-chain equivalent, this test
// actually runs the migration, so a missing incremental statement fails here.
func TestSQLiteFreshSchemaMatchesIncrementalVPNChain(t *testing.T) {
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
	indexes := func(t *testing.T, dsn, table string) map[string][]string {
		t.Helper()
		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		rows, err := raw.Query(`SELECT name FROM pragma_index_list(?) WHERE origin='c'`, table)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			names = append(names, name)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for _, name := range names {
			info, err := raw.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, name)
			if err != nil {
				t.Fatal(err)
			}
			var cols []string
			for info.Next() {
				var col sql.NullString
				if err := info.Scan(&col); err != nil {
					info.Close()
					t.Fatal(err)
				}
				cols = append(cols, col.String)
			}
			info.Close()
			out[name] = cols
		}
		return out
	}
	uniqueIndexes := func(t *testing.T, dsn, table string) int {
		t.Helper()
		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		var count int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM pragma_index_list(?) WHERE "unique"=1`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	dir := t.TempDir()
	fresh := "file:" + filepath.Join(dir, "fresh.sqlite")
	freshDB, err := OpenSQLite(context.Background(), fresh, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := freshDB.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded := "file:" + filepath.Join(dir, "upgraded.sqlite")
	prepareV14Base(t, upgraded)
	upgradedDB, err := OpenSQLite(context.Background(), upgraded, true)
	if err != nil {
		t.Fatalf("run the incremental chain over the v14 base: %v", err)
	}
	if version, err := upgradedDB.SchemaVersion(context.Background()); err != nil || version != SchemaVersion {
		upgradedDB.Close()
		t.Fatalf("schema version after the incremental chain = %d, err = %v, want %d", version, err, SchemaVersion)
	}
	if err := upgradedDB.Close(); err != nil {
		t.Fatal(err)
	}

	for _, table := range vpnSchemaTables {
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
		wantIdx := indexes(t, fresh, table)
		gotIdx := indexes(t, upgraded, table)
		if len(wantIdx) != len(gotIdx) {
			t.Fatalf("table %s index count differs: full DDL %d, incremental %d", table, len(wantIdx), len(gotIdx))
		}
		for name, wantCols := range wantIdx {
			gotCols, ok := gotIdx[name]
			if !ok {
				t.Fatalf("table %s is missing index %s after the incremental chain", table, name)
			}
			if strings.Join(wantCols, ",") != strings.Join(gotCols, ",") {
				t.Fatalf("index %s on %s has columns %v after the incremental chain, want %v", name, table, gotCols, wantCols)
			}
		}
		if want, got := uniqueIndexes(t, fresh, table), uniqueIndexes(t, upgraded, table); want != got {
			t.Fatalf("table %s has %d unique indexes after the incremental chain, want %d", table, got, want)
		}
	}
}

// TestSQLiteAutoInitDisabledRejectsMissingVPNTables proves the two new tables
// are part of the startup gate: with auto-init off, a database that skipped the
// v15 migration fails fast and names the missing table.
func TestSQLiteAutoInitDisabledRejectsMissingVPNTables(t *testing.T) {
	for _, table := range vpnSchemaTables {
		t.Run(table, func(t *testing.T) {
			dsn := "file:schema-v15-missing-" + strings.ReplaceAll(table, "_", "-") + "?mode=memory&cache=shared"
			raw, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(migrations.DDL); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`DROP TABLE ` + table); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`DELETE FROM schema_meta WHERE id=1; INSERT INTO schema_meta(id,version) VALUES (1,?)`, SchemaVersion); err != nil {
				t.Fatal(err)
			}
			_, err = OpenSQLite(context.Background(), dsn, false)
			if err == nil {
				t.Fatalf("opening a database without %s must fail when auto-init is off", table)
			}
			if !strings.Contains(err.Error(), table) {
				t.Fatalf("error = %v, want it to name %s", err, table)
			}
		})
	}
}

// vpnPeerFixture returns a complete, valid peer record. Every validation case
// mutates exactly one field so a failure names the rule that broke.
func vpnPeerFixture() VPNPeer {
	return VPNPeer{
		Name:                "edge-gateway",
		OwnerID:             "user-1",
		PublicKey:           "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		VPNIP:               "10.66.0.2",
		NodeID:              "node-1",
		AgentID:             "agent-1",
		AllowedIPs:          "10.10.0.0/16",
		AllowedPorts:        "22,443",
		AllowPrivateTargets: true,
		ICMPEnabled:         true,
		MaxConcurrentFlows:  128,
		Status:              VPNPeerStatusActive,
	}
}

func TestVPNPeerModelDefaultsAndValidation(t *testing.T) {
	if err := validateVPNPeer(vpnPeerFixture()); err != nil {
		t.Fatalf("a complete peer record must validate: %v", err)
	}
	// All three persisted statuses are legal, so a caller that reads a row back
	// and re-validates it never trips on a value the database already holds.
	for _, status := range []VPNPeerStatus{VPNPeerStatusActive, VPNPeerStatusDisabled, VPNPeerStatusRevoked} {
		peer := vpnPeerFixture()
		peer.Status = status
		if err := validateVPNPeer(peer); err != nil {
			t.Fatalf("status %q must validate: %v", status, err)
		}
	}
	cases := []struct {
		name   string
		mutate func(*VPNPeer)
	}{
		{"empty owner", func(peer *VPNPeer) { peer.OwnerID = "" }},
		{"blank name", func(peer *VPNPeer) { peer.Name = "   " }},
		{"empty public key", func(peer *VPNPeer) { peer.PublicKey = "" }},
		{"empty vpn ip", func(peer *VPNPeer) { peer.VPNIP = "" }},
		{"empty node", func(peer *VPNPeer) { peer.NodeID = "" }},
		{"empty agent", func(peer *VPNPeer) { peer.AgentID = "" }},
		{"empty status", func(peer *VPNPeer) { peer.Status = "" }},
		{"unknown status", func(peer *VPNPeer) { peer.Status = VPNPeerStatus("paused") }},
		{"negative flow cap", func(peer *VPNPeer) { peer.MaxConcurrentFlows = -1 }},
		{"negative rate limit", func(peer *VPNPeer) { peer.PacketRateLimit = -1 }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			peer := vpnPeerFixture()
			testCase.mutate(&peer)
			if err := validateVPNPeer(peer); err == nil {
				t.Fatalf("validateVPNPeer(%+v) = nil, want an error", peer)
			}
		})
	}
	// A sealed private key is all-or-nothing: AES-GCM ciphertext without its
	// nonce can never be opened, so the pair is rejected together rather than
	// stored as an unusable row. The key id stays optional because
	// TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID is optional by design.
	peer := vpnPeerFixture()
	peer.PrivateKeyCiphertext = "sealed-ciphertext"
	if err := validateVPNPeer(peer); err == nil {
		t.Fatal("private key ciphertext without its nonce must be rejected")
	}
	peer.PrivateKeyNonce = "sealed-nonce"
	if err := validateVPNPeer(peer); err != nil {
		t.Fatalf("a sealed private key without a key id must validate: %v", err)
	}
	peer = vpnPeerFixture()
	peer.PrivateKeyNonce = "orphan-nonce"
	if err := validateVPNPeer(peer); err == nil {
		t.Fatal("a private key nonce without its ciphertext must be rejected")
	}
}

func vpnIPLeaseFixture() VPNIPLease {
	return VPNIPLease{
		NodeID:      "node-1",
		Subnet:      "10.66.0.0/24",
		LeaseHolder: "holder-1",
		TTL:         time.Minute,
	}
}

func TestVPNIPLeaseModelValidation(t *testing.T) {
	if err := validateVPNIPLease(vpnIPLeaseFixture()); err != nil {
		t.Fatalf("a complete lease must validate: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*VPNIPLease)
	}{
		{"empty node", func(lease *VPNIPLease) { lease.NodeID = "" }},
		{"blank subnet", func(lease *VPNIPLease) { lease.Subnet = "  " }},
		{"empty holder", func(lease *VPNIPLease) { lease.LeaseHolder = "" }},
		{"negative allocated count", func(lease *VPNIPLease) { lease.AllocatedCount = -1 }},
		{"negative epoch", func(lease *VPNIPLease) { lease.Epoch = -1 }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			lease := vpnIPLeaseFixture()
			testCase.mutate(&lease)
			if err := validateVPNIPLease(lease); err == nil {
				t.Fatalf("validateVPNIPLease(%+v) = nil, want an error", lease)
			}
		})
	}
	// A non-positive TTL must default to one minute, matching leaseRepo, so no
	// caller can write a lease that is already expired and instantly stealable.
	for _, ttl := range []time.Duration{0, -time.Second} {
		if got := vpnIPLeaseTTL(ttl); got != time.Minute {
			t.Fatalf("vpnIPLeaseTTL(%v) = %v, want %v", ttl, got, time.Minute)
		}
	}
	if got := vpnIPLeaseTTL(90 * time.Second); got != 90*time.Second {
		t.Fatalf("vpnIPLeaseTTL(90s) = %v, want it unchanged", got)
	}
}

// TestVPNSentinelErrorsAreDistinct keeps the four v15 sentinels separable: a
// caller that maps one of them to an HTTP status must not accidentally match
// another through error wrapping.
func TestVPNSentinelErrorsAreDistinct(t *testing.T) {
	sentinels := []error{ErrVPNPeerConflict, ErrVPNPeerRevoked, ErrVPNIPLeaseHeld, ErrVPNIPLeaseStaleEpoch}
	for i, target := range sentinels {
		if target == nil {
			t.Fatalf("sentinel %d is nil", i)
		}
		if !errors.Is(target, target) {
			t.Fatalf("%v does not match itself", target)
		}
		for j, other := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(target, other) {
				t.Fatalf("%v must not match %v", target, other)
			}
			if errors.Is(fmt.Errorf("wrapped: %w", target), other) {
				t.Fatalf("a wrapped %v must not match %v", target, other)
			}
		}
	}
}

// newVPNTestDB returns a database only this test can see. The shared in-memory
// contract database would let another test's rows leak into the pagination and
// count assertions below, so the VPN contract uses a private file.
func newVPNTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenSQLite(context.Background(), "file:"+filepath.Join(t.TempDir(), "vpn.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSQLiteVPNPeerRepositoryContract(t *testing.T) {
	runVPNPeerRepositoryContract(t, newVPNTestDB(t))
}

func mustCreateVPNPeer(t *testing.T, db *DB, peer VPNPeer) VPNPeer {
	t.Helper()
	created, err := db.VPNPeers().Create(context.Background(), peer)
	if err != nil {
		t.Fatalf("create vpn peer %s: %v", peer.ID, err)
	}
	return created
}

// vpnPeerIDs drains every page of a List call and returns the peer IDs in page
// order. Draining matters: a filter that leaks rows shows up on a later page,
// not on the first one.
func vpnPeerIDs(t *testing.T, db *DB, filter VPNPeerFilter, limit int) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for {
		page, err := db.VPNPeers().List(context.Background(), filter, cursor, limit)
		if err != nil {
			t.Fatalf("list vpn peers: %v", err)
		}
		for _, peer := range page.Items {
			ids = append(ids, peer.ID)
		}
		if !page.HasMore || page.NextCursor == "" {
			return ids
		}
		cursor = page.NextCursor
	}
}

// assertVPNPeerIDs compares ID sets, ignoring order, and names the difference so
// a leaking or over-narrow filter is obvious from the failure alone.
func assertVPNPeerIDs(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	gotSet := map[string]bool{}
	for _, id := range got {
		gotSet[id] = true
	}
	wantSet := map[string]bool{}
	for _, id := range want {
		wantSet[id] = true
	}
	if len(gotSet) != len(got) {
		t.Fatalf("%s returned duplicate rows: %v", label, got)
	}
	for _, id := range want {
		if !gotSet[id] {
			t.Fatalf("%s is missing %s; got %v, want %v", label, id, got, want)
		}
	}
	for _, id := range got {
		if !wantSet[id] {
			t.Fatalf("%s returned unexpected row %s; got %v, want %v", label, id, got, want)
		}
	}
}

// assertVPNPeerIDsInOrder is assertVPNPeerIDs for callers that promise a stable
// ordering, which is what lets a gateway rebuild a config without re-sorting.
func assertVPNPeerIDsInOrder(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s order = %v, want %v", label, got, want)
	}
}

func assertVPNPeerEqual(t *testing.T, want, got VPNPeer) {
	t.Helper()
	if got.ID != want.ID || got.Name != want.Name || got.OwnerID != want.OwnerID ||
		got.PublicKey != want.PublicKey || got.PrivateKeyCiphertext != want.PrivateKeyCiphertext ||
		got.PrivateKeyNonce != want.PrivateKeyNonce || got.PrivateKeyKeyID != want.PrivateKeyKeyID ||
		got.PrivateKeyVersion != want.PrivateKeyVersion || got.VPNIP != want.VPNIP ||
		got.NodeID != want.NodeID || got.AgentID != want.AgentID ||
		got.AllowedIPs != want.AllowedIPs || got.AllowedPorts != want.AllowedPorts ||
		got.AllowPrivateTargets != want.AllowPrivateTargets || got.ICMPEnabled != want.ICMPEnabled ||
		got.MaxConcurrentFlows != want.MaxConcurrentFlows || got.PacketRateLimit != want.PacketRateLimit ||
		got.Status != want.Status || got.Description != want.Description {
		t.Fatalf("peer round trip mismatch:\nwant %+v\ngot  %+v", want, got)
	}
	switch {
	case want.ExpiresAt == nil && got.ExpiresAt != nil:
		t.Fatalf("peer %s expires_at = %v, want nil", got.ID, *got.ExpiresAt)
	case want.ExpiresAt != nil && got.ExpiresAt == nil:
		t.Fatalf("peer %s expires_at = nil, want %v", got.ID, *want.ExpiresAt)
	case want.ExpiresAt != nil && !got.ExpiresAt.Equal(*want.ExpiresAt):
		t.Fatalf("peer %s expires_at = %v, want %v", got.ID, *got.ExpiresAt, *want.ExpiresAt)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("peer %s timestamps = %v/%v, want %v/%v", got.ID, got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
	}
}

// runVPNPeerRepositoryContract asserts the behaviour that must hold on both
// SQLite and MySQL: round-trip fidelity of every column, the two unique
// constraints, the terminal revoked state, permission filtering inside
// pagination, and the derived counters. SQLite runs it against a private file
// database; MySQL runs the same function against a database shared with the
// other contract tests, so every assertion is scoped to IDs created here.
func runVPNPeerRepositoryContract(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	peers := db.VPNPeers()

	t.Run("round trip", func(t *testing.T) {
		expires := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
		sealed := VPNPeer{
			ID: "vpn-peer-roundtrip-sealed", Name: "sealed-gateway", OwnerID: "vpn-user-roundtrip",
			PublicKey: "vpn-roundtrip-sealed-key", PrivateKeyCiphertext: "sealed-ciphertext",
			PrivateKeyNonce: "sealed-nonce", PrivateKeyKeyID: "key-1", PrivateKeyVersion: 3,
			VPNIP: "10.66.1.2", NodeID: "vpn-node-roundtrip", AgentID: "vpn-agent-roundtrip",
			AllowedIPs: "10.10.0.0/16,192.168.0.0/24", AllowedPorts: "22,443,8080-8090",
			AllowPrivateTargets: true, ICMPEnabled: true, MaxConcurrentFlows: 256, PacketRateLimit: 1000,
			ExpiresAt: &expires, Status: VPNPeerStatusActive, Description: "sealed round trip",
		}
		created, err := peers.Create(ctx, sealed)
		if err != nil {
			t.Fatal(err)
		}
		if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
			t.Fatalf("Create must fill timestamps, got %+v", created)
		}
		if created.CreatedAt.Location() != time.UTC || created.UpdatedAt.Location() != time.UTC {
			t.Fatalf("timestamps must be UTC, got %v and %v", created.CreatedAt, created.UpdatedAt)
		}
		sealed.CreatedAt, sealed.UpdatedAt = created.CreatedAt, created.UpdatedAt
		byID, err := peers.Get(ctx, sealed.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertVPNPeerEqual(t, sealed, byID)
		byKey, err := peers.GetByPublicKey(ctx, sealed.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		assertVPNPeerEqual(t, sealed, byKey)
		byIP, err := peers.GetByNodeAndIP(ctx, sealed.NodeID, sealed.VPNIP)
		if err != nil {
			t.Fatal(err)
		}
		assertVPNPeerEqual(t, sealed, byIP)

		// The other legal shape: no expiry and no sealed private key, which is
		// how a peer looks before the gateway stores its key material.
		open := VPNPeer{
			ID: "vpn-peer-roundtrip-open", Name: "open-gateway", OwnerID: "vpn-user-roundtrip",
			PublicKey: "vpn-roundtrip-open-key", VPNIP: "10.66.1.3", NodeID: "vpn-node-roundtrip",
			AgentID: "vpn-agent-roundtrip", AllowedIPs: "0.0.0.0/0", AllowedPorts: "",
			MaxConcurrentFlows: 128, Status: VPNPeerStatusDisabled,
		}
		openCreated, err := peers.Create(ctx, open)
		if err != nil {
			t.Fatal(err)
		}
		open.CreatedAt, open.UpdatedAt = openCreated.CreatedAt, openCreated.UpdatedAt
		got, err := peers.Get(ctx, open.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertVPNPeerEqual(t, open, got)
		if got.ExpiresAt != nil {
			t.Fatalf("expires_at = %v, want nil", *got.ExpiresAt)
		}
		if got.PrivateKeyCiphertext != "" || got.PrivateKeyNonce != "" || got.PrivateKeyKeyID != "" || got.PrivateKeyVersion != 0 {
			t.Fatalf("unsealed private key columns = %q/%q/%q/%d, want all empty",
				got.PrivateKeyCiphertext, got.PrivateKeyNonce, got.PrivateKeyKeyID, got.PrivateKeyVersion)
		}
		// AllowedIPs and AllowedPorts are opaque text owned by internal/vpn, so
		// storage must return them byte for byte instead of re-rendering them.
		if got.AllowedIPs != "0.0.0.0/0" || got.AllowedPorts != "" {
			t.Fatalf("opaque policy text = %q/%q, want it unchanged", got.AllowedIPs, got.AllowedPorts)
		}
		if byID.AllowedIPs != sealed.AllowedIPs || byID.AllowedPorts != sealed.AllowedPorts {
			t.Fatalf("opaque policy text = %q/%q, want %q/%q", byID.AllowedIPs, byID.AllowedPorts, sealed.AllowedIPs, sealed.AllowedPorts)
		}
		if _, err := peers.Get(ctx, "vpn-peer-absent"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("Get on an unknown peer err = %v, want sql.ErrNoRows", err)
		}
		if _, err := peers.GetByPublicKey(ctx, "vpn-absent-key"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("GetByPublicKey on an unknown key err = %v, want sql.ErrNoRows", err)
		}
		if _, err := peers.GetByNodeAndIP(ctx, sealed.NodeID, "10.66.1.254"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("GetByNodeAndIP on an unknown address err = %v, want sql.ErrNoRows", err)
		}
	})

	t.Run("generated id and timestamps", func(t *testing.T) {
		created, err := peers.Create(ctx, VPNPeer{
			Name: "generated", OwnerID: "vpn-user-generated", PublicKey: "vpn-generated-key",
			VPNIP: "10.66.7.2", NodeID: "vpn-node-generated", AgentID: "vpn-agent-generated",
			AllowedIPs: "10.10.0.0/16", AllowedPorts: "443", Status: VPNPeerStatusActive,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(created.ID, "vpn-peer-") {
			t.Fatalf("generated ID = %q, want a vpn-peer- prefix", created.ID)
		}
		if created.CreatedAt.IsZero() || !created.CreatedAt.Equal(created.UpdatedAt) {
			t.Fatalf("generated timestamps = %v/%v, want equal non-zero UTC values", created.CreatedAt, created.UpdatedAt)
		}
		got, err := peers.Get(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertVPNPeerEqual(t, created, got)
	})

	t.Run("unique constraints", func(t *testing.T) {
		base := VPNPeer{
			ID: "vpn-peer-unique-1", Name: "unique-1", OwnerID: "vpn-user-unique",
			PublicKey: "vpn-unique-key-1", VPNIP: "10.66.2.2", NodeID: "vpn-node-unique",
			AgentID: "vpn-agent-unique", AllowedIPs: "10.10.0.0/16", AllowedPorts: "443",
			Status: VPNPeerStatusActive,
		}
		if _, err := peers.Create(ctx, base); err != nil {
			t.Fatal(err)
		}
		duplicateKey := base
		duplicateKey.ID, duplicateKey.VPNIP, duplicateKey.NodeID = "vpn-peer-unique-2", "10.66.2.3", "vpn-node-unique-other"
		if _, err := peers.Create(ctx, duplicateKey); !errors.Is(err, ErrVPNPeerConflict) {
			t.Fatalf("duplicate public key err = %v, want ErrVPNPeerConflict", err)
		}
		duplicateIP := base
		duplicateIP.ID, duplicateIP.PublicKey = "vpn-peer-unique-3", "vpn-unique-key-3"
		if _, err := peers.Create(ctx, duplicateIP); !errors.Is(err, ErrVPNPeerConflict) {
			t.Fatalf("duplicate node and address err = %v, want ErrVPNPeerConflict", err)
		}
		// The same address on another node is legal: UNIQUE(node_id, vpn_ip)
		// scopes the pool per node, which is what lets every node carve its own
		// /24 out of a shared ip_pool without coordinating addresses.
		otherNode := base
		otherNode.ID, otherNode.PublicKey, otherNode.NodeID = "vpn-peer-unique-4", "vpn-unique-key-4", "vpn-node-unique-2"
		if _, err := peers.Create(ctx, otherNode); err != nil {
			t.Fatalf("the same vpn_ip on another node must succeed: %v", err)
		}
	})

	t.Run("update", func(t *testing.T) {
		created, err := peers.Create(ctx, VPNPeer{
			ID: "vpn-peer-update", Name: "before", OwnerID: "vpn-user-update",
			PublicKey: "vpn-update-key", VPNIP: "10.66.3.2", NodeID: "vpn-node-update",
			AgentID: "vpn-agent-update", AllowedIPs: "10.10.0.0/16", AllowedPorts: "443",
			MaxConcurrentFlows: 64, PacketRateLimit: 10, Status: VPNPeerStatusActive, Description: "before",
		})
		if err != nil {
			t.Fatal(err)
		}
		expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		rotated := created
		rotated.Name = "after"
		rotated.PrivateKeyCiphertext, rotated.PrivateKeyNonce = "rotated-ciphertext", "rotated-nonce"
		rotated.PrivateKeyKeyID, rotated.PrivateKeyVersion = "key-2", 7
		rotated.VPNIP = "10.66.3.9"
		rotated.AllowedIPs, rotated.AllowedPorts = "172.16.0.0/12", "22"
		rotated.AllowPrivateTargets, rotated.ICMPEnabled = true, true
		rotated.MaxConcurrentFlows, rotated.PacketRateLimit = 512, 0
		rotated.ExpiresAt, rotated.Description = &expires, "after"
		rotated.UpdatedAt = created.UpdatedAt.Add(time.Second)
		if err := peers.Update(ctx, rotated); err != nil {
			t.Fatal(err)
		}
		got, err := peers.Get(ctx, rotated.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertVPNPeerEqual(t, rotated, got)
		if !got.UpdatedAt.After(created.UpdatedAt) {
			t.Fatalf("updated_at = %v, want it after %v", got.UpdatedAt, created.UpdatedAt)
		}
		// The address moved, so the old one must stop resolving; a stale lookup
		// would route traffic to a peer that no longer owns the address.
		if _, err := peers.GetByNodeAndIP(ctx, rotated.NodeID, "10.66.3.2"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("the previous vpn_ip must no longer resolve, err = %v", err)
		}
		moved, err := peers.GetByNodeAndIP(ctx, rotated.NodeID, rotated.VPNIP)
		if err != nil || moved.ID != rotated.ID {
			t.Fatalf("GetByNodeAndIP after the move = %+v, err = %v", moved, err)
		}
		missing := rotated
		missing.ID = "vpn-peer-update-absent"
		if err := peers.Update(ctx, missing); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("updating an unknown peer err = %v, want sql.ErrNoRows", err)
		}
	})

	t.Run("status transitions", func(t *testing.T) {
		created, err := peers.Create(ctx, VPNPeer{
			ID: "vpn-peer-status", Name: "status", OwnerID: "vpn-user-status",
			PublicKey: "vpn-status-key", VPNIP: "10.66.4.2", NodeID: "vpn-node-status",
			AgentID: "vpn-agent-status", AllowedIPs: "10.10.0.0/16", AllowedPorts: "443",
			Status: VPNPeerStatusActive,
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Second)
		if err := peers.SetStatus(ctx, created.ID, VPNPeerStatusDisabled, now); err != nil {
			t.Fatal(err)
		}
		if got, err := peers.Get(ctx, created.ID); err != nil || got.Status != VPNPeerStatusDisabled {
			t.Fatalf("status = %q, err = %v, want disabled", got.Status, err)
		}
		if err := peers.SetStatus(ctx, created.ID, VPNPeerStatusActive, now.Add(time.Second)); err != nil {
			t.Fatalf("disabled back to active must be allowed: %v", err)
		}
		if err := peers.SetStatus(ctx, created.ID, VPNPeerStatusRevoked, now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		// Revoked is terminal. The guard lives in the SQL WHERE clause rather
		// than in a read-modify-write, so two concurrent revocations cannot race
		// one of them back to active.
		if err := peers.SetStatus(ctx, created.ID, VPNPeerStatusActive, now.Add(3*time.Second)); !errors.Is(err, ErrVPNPeerRevoked) {
			t.Fatalf("reactivating a revoked peer err = %v, want ErrVPNPeerRevoked", err)
		}
		if got, err := peers.Get(ctx, created.ID); err != nil || got.Status != VPNPeerStatusRevoked {
			t.Fatalf("status = %q, err = %v, want it to stay revoked", got.Status, err)
		}
		if err := peers.SetStatus(ctx, "vpn-peer-status-absent", VPNPeerStatusActive, now); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("SetStatus on an unknown peer err = %v, want sql.ErrNoRows", err)
		}
	})

	t.Run("list filters within pagination", func(t *testing.T) {
		// The MySQL contract database is shared with the other contract tests,
		// so the admin view is asserted as baseline+6 rather than a literal 6.
		// On SQLite the baseline is zero and the assertion is exactly six.
		baseline := len(vpnPeerIDs(t, db, VPNPeerFilter{}, 100))
		const ownerA, ownerB = "vpn-page-owner-a", "vpn-page-owner-b"
		var aIDs, bIDs []string
		for i := 1; i <= 3; i++ {
			a := mustCreateVPNPeer(t, db, VPNPeer{
				ID: fmt.Sprintf("vpn-peer-page-a-%d", i), Name: fmt.Sprintf("page-a-%d", i), OwnerID: ownerA,
				PublicKey: fmt.Sprintf("vpn-page-a-key-%d", i), VPNIP: fmt.Sprintf("10.66.5.%d", i),
				NodeID: "vpn-node-page-a", AgentID: "vpn-agent-page", AllowedIPs: "10.10.0.0/16",
				AllowedPorts: "443", Status: VPNPeerStatusActive,
			})
			aIDs = append(aIDs, a.ID)
			b := mustCreateVPNPeer(t, db, VPNPeer{
				ID: fmt.Sprintf("vpn-peer-page-b-%d", i), Name: fmt.Sprintf("page-b-%d", i), OwnerID: ownerB,
				PublicKey: fmt.Sprintf("vpn-page-b-key-%d", i), VPNIP: fmt.Sprintf("10.66.5.%d", i+10),
				NodeID: "vpn-node-page-b", AgentID: "vpn-agent-page", AllowedIPs: "10.10.0.0/16",
				AllowedPorts: "443", Status: VPNPeerStatusActive,
			})
			bIDs = append(bIDs, b.ID)
		}
		first, err := peers.List(ctx, VPNPeerFilter{OwnerUserID: ownerA}, "", 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Items) != 2 {
			t.Fatalf("first page has %d rows, want 2", len(first.Items))
		}
		if !first.HasMore || first.NextCursor == "" {
			t.Fatalf("first page = %+v, want HasMore and a cursor", first)
		}
		for _, peer := range first.Items {
			if peer.OwnerID != ownerA {
				t.Fatalf("the first page leaked a row owned by %q", peer.OwnerID)
			}
		}
		second, err := peers.List(ctx, VPNPeerFilter{OwnerUserID: ownerA}, first.NextCursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(second.Items) != 1 || second.Items[0].ID != aIDs[2] {
			t.Fatalf("second page = %+v, want only %s", second.Items, aIDs[2])
		}
		if second.HasMore || second.NextCursor != "" {
			t.Fatalf("second page = %+v, want the end of the result set", second)
		}
		if second.Items[0].OwnerID != ownerA {
			t.Fatalf("the second page leaked a row owned by %q", second.Items[0].OwnerID)
		}
		// Draining A's pages must yield exactly A's three peers and none of B's:
		// the owner filter has to be part of the WHERE clause, because filtering
		// after pagination silently truncates a user's own result set.
		gotA := vpnPeerIDs(t, db, VPNPeerFilter{OwnerUserID: ownerA}, 2)
		assertVPNPeerIDs(t, "owner A pages", gotA, aIDs...)
		gotB := vpnPeerIDs(t, db, VPNPeerFilter{OwnerUserID: ownerB}, 2)
		assertVPNPeerIDs(t, "owner B pages", gotB, bIDs...)
		if total := len(vpnPeerIDs(t, db, VPNPeerFilter{}, 10)); total != baseline+6 {
			t.Fatalf("the unfiltered admin view has %d rows, want %d (baseline %d plus the 6 created here)", total, baseline+6, baseline)
		}
	})

	t.Run("filter combinations", func(t *testing.T) {
		const owner = "vpn-filter-owner"
		for _, spec := range []struct {
			id, name, description, node, agent string
			ip                                 string
			status                             VPNPeerStatus
		}{
			{"vpn-peer-filter-1", "alpha-gateway", "primary", "vpn-filter-node-x", "vpn-filter-agent-x", "10.66.8.2", VPNPeerStatusActive},
			{"vpn-peer-filter-2", "beta-gateway", "secondary", "vpn-filter-node-x", "vpn-filter-agent-y", "10.66.8.3", VPNPeerStatusDisabled},
			{"vpn-peer-filter-3", "gamma-edge", "alpha-backup", "vpn-filter-node-y", "vpn-filter-agent-x", "10.66.8.4", VPNPeerStatusActive},
		} {
			mustCreateVPNPeer(t, db, VPNPeer{
				ID: spec.id, Name: spec.name, Description: spec.description, OwnerID: owner,
				PublicKey: "vpn-filter-key-" + spec.id, VPNIP: spec.ip, NodeID: spec.node, AgentID: spec.agent,
				AllowedIPs: "10.10.0.0/16", AllowedPorts: "443", Status: spec.status,
			})
		}
		cases := []struct {
			label  string
			filter VPNPeerFilter
			want   []string
		}{
			{"status active", VPNPeerFilter{OwnerUserID: owner, Status: VPNPeerStatusActive}, []string{"vpn-peer-filter-1", "vpn-peer-filter-3"}},
			{"status disabled", VPNPeerFilter{OwnerUserID: owner, Status: VPNPeerStatusDisabled}, []string{"vpn-peer-filter-2"}},
			{"unknown status", VPNPeerFilter{OwnerUserID: owner, Status: VPNPeerStatus("paused")}, nil},
			{"node", VPNPeerFilter{OwnerUserID: owner, NodeID: "vpn-filter-node-x"}, []string{"vpn-peer-filter-1", "vpn-peer-filter-2"}},
			{"agent", VPNPeerFilter{OwnerUserID: owner, AgentID: "vpn-filter-agent-x"}, []string{"vpn-peer-filter-1", "vpn-peer-filter-3"}},
			{"keyword in name", VPNPeerFilter{OwnerUserID: owner, Keyword: "gamma"}, []string{"vpn-peer-filter-3"}},
			{"keyword in description", VPNPeerFilter{OwnerUserID: owner, Keyword: "alpha"}, []string{"vpn-peer-filter-1", "vpn-peer-filter-3"}},
			{"owner and status and node", VPNPeerFilter{OwnerUserID: owner, Status: VPNPeerStatusActive, NodeID: "vpn-filter-node-x"}, []string{"vpn-peer-filter-1"}},
		}
		for _, testCase := range cases {
			t.Run(testCase.label, func(t *testing.T) {
				assertVPNPeerIDs(t, testCase.label, vpnPeerIDs(t, db, testCase.filter, 50), testCase.want...)
			})
		}
		// An unknown status must narrow to nothing. Degrading to "return
		// everything" would turn a typo in a filter into a cross-tenant read.
		t.Run("unknown status returns nothing", func(t *testing.T) {
			page, err := peers.List(ctx, VPNPeerFilter{OwnerUserID: owner, Status: VPNPeerStatus("paused")}, "", 50)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 0 || page.HasMore {
				t.Fatalf("unknown status page = %+v, want an empty result set", page)
			}
		})
		// An over-long keyword must be truncated and still execute, so a hostile
		// filter value cannot build an oversized query.
		t.Run("over long keyword is bounded", func(t *testing.T) {
			page, err := peers.List(ctx, VPNPeerFilter{OwnerUserID: owner, Keyword: strings.Repeat("x", 4096)}, "", 50)
			if err != nil {
				t.Fatalf("an over-long keyword must still execute: %v", err)
			}
			if len(page.Items) != 0 {
				t.Fatalf("an over-long keyword matched %d rows, want none", len(page.Items))
			}
		})
	})

	t.Run("list by node and counts", func(t *testing.T) {
		const node, owner = "vpn-list-node", "vpn-list-owner"
		for i, spec := range []struct {
			id     string
			status VPNPeerStatus
		}{
			{"vpn-peer-list-1", VPNPeerStatusActive},
			{"vpn-peer-list-2", VPNPeerStatusDisabled},
			{"vpn-peer-list-3", VPNPeerStatusRevoked},
		} {
			mustCreateVPNPeer(t, db, VPNPeer{
				ID: spec.id, Name: fmt.Sprintf("list-%d", i+1), OwnerID: owner,
				PublicKey: "vpn-list-key-" + spec.id, VPNIP: fmt.Sprintf("10.66.6.%d", i+2),
				NodeID: node, AgentID: "vpn-list-agent", AllowedIPs: "10.10.0.0/16",
				AllowedPorts: "443", Status: spec.status,
			})
		}
		// A revoked peer keeps its row for audit but must never be handed to the
		// gateway again, so ListByNode drops it while CountByOwner keeps it.
		got, err := peers.ListByNode(ctx, node)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, peer := range got {
			ids = append(ids, peer.ID)
		}
		assertVPNPeerIDsInOrder(t, "ListByNode", ids, "vpn-peer-list-1", "vpn-peer-list-2")
		if count, err := peers.CountByNode(ctx, node); err != nil || count != 2 {
			t.Fatalf("CountByNode = %d, err = %v, want 2", count, err)
		}
		// CountByOwner deliberately includes revoked peers: whether a revoked
		// peer still consumes quota is a phase 4 product decision, and the
		// storage layer reports the fact instead of pre-deciding it.
		if count, err := peers.CountByOwner(ctx, owner); err != nil || count != 3 {
			t.Fatalf("CountByOwner = %d, err = %v, want 3", count, err)
		}
		empty, err := peers.ListByNode(ctx, "vpn-node-absent")
		if err != nil {
			t.Fatal(err)
		}
		if len(empty) != 0 {
			t.Fatalf("ListByNode on an unknown node = %v, want an empty slice", empty)
		}
		if count, err := peers.CountByNode(ctx, "vpn-node-absent"); err != nil || count != 0 {
			t.Fatalf("CountByNode on an unknown node = %d, err = %v, want 0", count, err)
		}
	})
}
