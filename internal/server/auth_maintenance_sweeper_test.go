package server

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func newSweeperDB(t *testing.T) *storage.DB {
	t.Helper()
	dsn := "file:" + t.TempDir() + "/sweeper.sqlite"
	db, err := storage.OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedSweeperRows writes one live and one expired challenge, one active device,
// one expired device, one recently revoked device, and one long-revoked device,
// so a single sweep can be asserted against every retention boundary at once.
func seedSweeperRows(t *testing.T, db *storage.DB, now time.Time, retention time.Duration) {
	t.Helper()
	ctx := context.Background()
	challenge := func(id string, expiresAt time.Time) storage.AuthChallenge {
		return storage.AuthChallenge{
			ID: id, Kind: storage.ChallengeKindLoginMFA, UserID: "user-1",
			PayloadCiphertext: "ciphertext", PayloadNonce: "nonce", PayloadKeyID: "default", PayloadVersion: 1,
			MaxAttempts: 5, ExpiresAt: expiresAt, CreatedAt: now,
		}
	}
	device := func(id string, expiresAt time.Time, revokedAt *time.Time) storage.UserDevice {
		return storage.UserDevice{
			ID: id, UserID: "user-1", TokenHash: "hash-" + id, UserAgent: "ua", IP: "10.0.0.1",
			TrustedAt: now, ExpiresAt: expiresAt, RevokedAt: revokedAt,
		}
	}
	recentlyRevoked := now.Add(-time.Hour)
	longRevoked := now.Add(-retention - time.Hour)

	err := db.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		if err := repos.Challenges.Create(ctx, challenge("challenge-live", now.Add(time.Hour))); err != nil {
			return err
		}
		if err := repos.Challenges.Create(ctx, challenge("challenge-expired", now.Add(-time.Minute))); err != nil {
			return err
		}
		if _, err := repos.Devices.Create(ctx, device("device-active", now.Add(24*time.Hour), nil)); err != nil {
			return err
		}
		// An expired device is kept for the retention window so the audit trail
		// can still resolve the identifier it references.
		if _, err := repos.Devices.Create(ctx, device("device-expired-recent", now.Add(-time.Hour), nil)); err != nil {
			return err
		}
		if _, err := repos.Devices.Create(ctx, device("device-revoked-recent", now.Add(24*time.Hour), &recentlyRevoked)); err != nil {
			return err
		}
		if _, err := repos.Devices.Create(ctx, device("device-revoked-old", now.Add(24*time.Hour), &longRevoked)); err != nil {
			return err
		}
		// Two throttle buckets, each blocked on its first failure because the
		// budget is one attempt.
		if _, _, err := repos.LoginAttempts.RegisterFailure(ctx, "bucket-one", time.Minute, 1, time.Minute); err != nil {
			return err
		}
		if _, _, err := repos.LoginAttempts.RegisterFailure(ctx, "bucket-two", time.Minute, 1, time.Minute); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// deviceExists reports whether the row is still present. It uses Get rather than
// ListByUser because the list hides revoked devices, and the retention boundary
// for a revoked row is exactly what these tests assert.
func deviceExists(t *testing.T, db *storage.DB, id string) bool {
	t.Helper()
	ctx := context.Background()
	err := db.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		_, err := repos.Devices.Get(ctx, "user-1", id)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func challengeExists(t *testing.T, db *storage.DB, id string) bool {
	t.Helper()
	ctx := context.Background()
	err := db.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		_, err := repos.Challenges.Get(ctx, id)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

// TestAuthMaintenanceSweepOnceDeletesExpiredRows proves the retention boundaries:
// an expired challenge goes immediately, an expired device is kept for the
// retention window, and a device revoked longer ago than the retention is
// collected.
func TestAuthMaintenanceSweepOnceDeletesExpiredRows(t *testing.T) {
	db := newSweeperDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	retention := 7 * 24 * time.Hour
	seedSweeperRows(t, db, now, retention)

	sweeper := NewAuthMaintenanceSweeper(db, time.Minute, retention)
	sweeper.SetClock(func() time.Time { return now })

	challenges, devices, err := sweeper.SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if challenges != 1 {
		t.Fatalf("deleted challenges = %d, want 1", challenges)
	}
	// Only the device revoked longer ago than the retention window is collected;
	// the recently expired and recently revoked rows are still inside it.
	if devices != 1 {
		t.Fatalf("deleted devices = %d, want 1", devices)
	}

	if !challengeExists(t, db, "challenge-live") {
		t.Fatal("a live challenge was deleted")
	}
	if challengeExists(t, db, "challenge-expired") {
		t.Fatal("an expired challenge survived the sweep")
	}
	if !deviceExists(t, db, "device-active") {
		t.Fatal("an active device was deleted")
	}
	if !deviceExists(t, db, "device-expired-recent") {
		t.Fatal("a recently expired device was deleted before its retention window elapsed")
	}
	if !deviceExists(t, db, "device-revoked-recent") {
		t.Fatal("a recently revoked device was deleted before its retention window elapsed")
	}
	if deviceExists(t, db, "device-revoked-old") {
		t.Fatal("a device revoked past the retention window survived the sweep")
	}
}

// TestAuthMaintenanceSweepOnceCollectsExpiredDeviceAfterRetention proves the
// second half of the device boundary: once the retention window has elapsed, the
// expired row is collected on the next pass.
func TestAuthMaintenanceSweepOnceCollectsExpiredDeviceAfterRetention(t *testing.T) {
	db := newSweeperDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	retention := 24 * time.Hour
	seedSweeperRows(t, db, now, retention)

	sweeper := NewAuthMaintenanceSweeper(db, time.Minute, retention)
	// Advance past the retention window relative to the seeded expiry.
	sweeper.SetClock(func() time.Time { return now.Add(retention + 2*time.Hour) })

	if _, _, err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if deviceExists(t, db, "device-expired-recent") {
		t.Fatal("an expired device survived past its retention window")
	}
	if deviceExists(t, db, "device-revoked-recent") {
		t.Fatal("a revoked device survived past its retention window")
	}
	if !deviceExists(t, db, "device-active") {
		t.Fatal("an active device was deleted")
	}
}

// TestAuthMaintenanceSweepOnceIsIdempotent proves a duplicate pass deletes
// nothing. Every cluster node runs its own sweeper, so a second node passing over
// an already-clean table must be a no-op rather than an error.
func TestAuthMaintenanceSweepOnceIsIdempotent(t *testing.T) {
	db := newSweeperDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	seedSweeperRows(t, db, now, 7*24*time.Hour)

	sweeper := NewAuthMaintenanceSweeper(db, time.Minute, 7*24*time.Hour)
	sweeper.SetClock(func() time.Time { return now })
	ctx := context.Background()
	if _, _, err := sweeper.SweepOnce(ctx); err != nil {
		t.Fatal(err)
	}
	challenges, devices, err := sweeper.SweepOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if challenges != 0 || devices != 0 {
		t.Fatalf("second pass deleted challenges=%d devices=%d, want 0 and 0", challenges, devices)
	}
}

// TestAuthMaintenanceSweepOnceConcurrent proves two goroutines may sweep at the
// same time, which is what happens when two cluster nodes tick together.
func TestAuthMaintenanceSweepOnceConcurrent(t *testing.T) {
	db := newSweeperDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	seedSweeperRows(t, db, now, 7*24*time.Hour)

	sweeper := NewAuthMaintenanceSweeper(db, time.Minute, 7*24*time.Hour)
	sweeper.SetClock(func() time.Time { return now })

	ctx := context.Background()
	var waitGroup sync.WaitGroup
	errs := make([]error, 4)
	for index := range errs {
		waitGroup.Add(1)
		go func(slot int) {
			defer waitGroup.Done()
			_, _, errs[slot] = sweeper.SweepOnce(ctx)
		}(index)
	}
	waitGroup.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent sweep %d failed: %v", index, err)
		}
	}
	if challengeExists(t, db, "challenge-expired") {
		t.Fatal("an expired challenge survived concurrent sweeps")
	}
}

// TestAuthMaintenanceSweeperDefaults proves a partially filled configuration
// cannot turn the job into a hot loop or delete rows immediately.
func TestAuthMaintenanceSweeperDefaults(t *testing.T) {
	db := newSweeperDB(t)
	sweeper := NewAuthMaintenanceSweeper(db, 0, 0)
	if sweeper.interval != DefaultAuthMaintenanceSweepInterval {
		t.Fatalf("interval = %v, want %v", sweeper.interval, DefaultAuthMaintenanceSweepInterval)
	}
	if sweeper.retention != DefaultAuthMaintenanceRetention {
		t.Fatalf("retention = %v, want %v", sweeper.retention, DefaultAuthMaintenanceRetention)
	}
	if _, _, err := NewAuthMaintenanceSweeper(nil, time.Minute, time.Hour).SweepOnce(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("err = %v, want ErrSessionClosed without a database", err)
	}
}

// TestAuthMaintenanceSweeperRunStopsOnCancel proves the loop terminates promptly
// so a shutdown is not delayed by a background hygiene job.
func TestAuthMaintenanceSweeperRunStopsOnCancel(t *testing.T) {
	db := newSweeperDB(t)
	sweeper := NewAuthMaintenanceSweeper(db, 10*time.Millisecond, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(ctx) }()

	// Let at least one tick run before cancelling.
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after the context was cancelled")
	}
}

// recordingGauges captures what the sweeper publishes.
type recordingGauges struct {
	mu         sync.Mutex
	devices    float64
	challenges float64
	blocked    float64
}

func (g *recordingGauges) SetAuthTrustedDevices(value float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.devices = value
}

func (g *recordingGauges) SetAuthPendingChallenges(value float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.challenges = value
}

func (g *recordingGauges) SetAuthBlockedBuckets(value float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.blocked = value
}

// TestAuthMaintenanceSweeperPublishesGauges proves the population gauges are
// sampled on every pass, including the one that runs at startup.
func TestAuthMaintenanceSweeperPublishesGauges(t *testing.T) {
	db := newSweeperDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	seedSweeperRows(t, db, now, 7*24*time.Hour)

	gauges := &recordingGauges{}
	sweeper := NewAuthMaintenanceSweeper(db, time.Minute, 7*24*time.Hour)
	sweeper.SetClock(func() time.Time { return now })
	sweeper.SetGauges(gauges)

	if _, _, err := sweeper.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	gauges.mu.Lock()
	defer gauges.mu.Unlock()
	// Three devices remain active: the active one, the recently expired one is
	// not active, and the two revoked ones are not active.
	if gauges.devices != 1 {
		t.Fatalf("trusted devices gauge = %v, want 1", gauges.devices)
	}
	if gauges.challenges != 1 {
		t.Fatalf("pending challenges gauge = %v, want 1", gauges.challenges)
	}
	if gauges.blocked != 2 {
		t.Fatalf("blocked buckets gauge = %v, want 2", gauges.blocked)
	}
}

// TestAuthMaintenanceSweepCollectsStaleThrottleBuckets proves the brute-force
// counters are collected. A bucket is a rolling counter rather than an audit
// record, so leaving it behind would grow the table with every username an
// attacker guesses.
func TestAuthMaintenanceSweepCollectsStaleThrottleBuckets(t *testing.T) {
	db := newSweeperDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	seedSweeperRows(t, db, now, 7*24*time.Hour)

	ctx := context.Background()
	blockedBefore := countBlocked(t, db)
	if blockedBefore != 2 {
		t.Fatalf("blocked buckets before = %d, want 2", blockedBefore)
	}

	// Retention of one hour with the clock three hours ahead puts the cutoff past
	// the buckets' last update, which is the state a stale bucket reaches in
	// production without the test having to backdate a row.
	sweeper := NewAuthMaintenanceSweeper(db, time.Minute, time.Hour)
	sweeper.SetClock(func() time.Time { return now.Add(3 * time.Hour) })
	if _, _, err := sweeper.SweepOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countBlocked(t, db); got != 0 {
		t.Fatalf("blocked buckets after = %d, want 0", got)
	}
}

func countBlocked(t *testing.T, db *storage.DB) int {
	t.Helper()
	ctx := context.Background()
	count := 0
	err := db.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		found, err := repos.LoginAttempts.CountBlocked(ctx)
		count = found
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}
