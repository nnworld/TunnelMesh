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
