package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestGenerateDeviceTokenIsUniqueAndHashed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		token, hash, err := GenerateDeviceToken()
		if err != nil {
			t.Fatal(err)
		}
		if len(token) != 43 {
			t.Fatalf("token length = %d, want 43", len(token))
		}
		if len(hash) != 64 || strings.Contains(hash, token) {
			t.Fatalf("hash = %q", hash)
		}
		if seen[token] {
			t.Fatalf("duplicate token %q", token)
		}
		seen[token] = true
		if HashDeviceToken(token) != hash {
			t.Fatal("hash is not reproducible")
		}
	}
	if HashDeviceToken("  abc  ") != HashDeviceToken("abc") {
		t.Fatal("hash must ignore surrounding whitespace so a cookie value matches")
	}
}

func TestDeviceIssueStoresOnlyTheHash(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")
	service := NewDeviceService(db, DefaultDeviceTrustConfig())

	token, device, err := service.Issue(ctx, "user-1", "Mozilla/5.0 (Macintosh)", "10.0.0.9")
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || device.ID == "" {
		t.Fatalf("token = %q, device = %+v", token, device)
	}
	stored, err := db.UserDevices().GetByTokenHash(ctx, HashDeviceToken(token))
	if err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash == token || strings.Contains(stored.TokenHash, token[:16]) {
		t.Fatal("plaintext token was persisted")
	}
	if stored.UserAgent != "Mozilla/5.0 (Macintosh)" || stored.IP != "10.0.0.9" || stored.LastSeenAt == nil {
		t.Fatalf("stored device = %+v", stored)
	}
	if !stored.ExpiresAt.After(time.Now().UTC().Add(29 * 24 * time.Hour)) {
		t.Fatalf("expiry = %v, want roughly 30 days out", stored.ExpiresAt)
	}
	if !contains(auditActions(t, db), "auth.device.trust") {
		t.Fatalf("audit actions = %v", auditActions(t, db))
	}
	if details := auditDetails(t, db); strings.Contains(details, token) || strings.Contains(details, stored.TokenHash) {
		t.Fatalf("audit details leaked the device token: %s", details)
	}
}

func TestDeviceIssueRejectsWhenDisabled(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")
	cfg := DefaultDeviceTrustConfig()
	cfg.Enabled = false
	service := NewDeviceService(db, cfg)

	if _, _, err := service.Issue(ctx, "user-1", "ua", "ip"); !errors.Is(err, ErrDeviceTrustDisabled) {
		t.Fatalf("err = %v, want ErrDeviceTrustDisabled", err)
	}
	if count, err := db.UserDevices().CountActive(ctx, "user-1"); err != nil || count != 0 {
		t.Fatalf("devices = %d, err = %v, want 0", count, err)
	}
	// Validation also refuses while the feature is off, so a cookie left over
	// from an earlier deployment cannot bypass MFA.
	if _, trusted, err := service.Validate(ctx, "anything"); err != nil || trusted {
		t.Fatalf("trusted = %v, err = %v, want false", trusted, err)
	}
	if _, _, err := service.Issue(ctx, "", "ua", "ip"); err == nil {
		t.Fatal("empty user id accepted")
	}
}

func TestDeviceValidateHandlesExpiryRevocationAndUnknownToken(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")
	service := NewDeviceService(db, DefaultDeviceTrustConfig())
	base := time.Now().UTC().Truncate(time.Second)
	service.SetClock(func() time.Time { return base })

	token, device, err := service.Issue(ctx, "user-1", "ua", "ip")
	if err != nil {
		t.Fatal(err)
	}
	found, trusted, err := service.Validate(ctx, token)
	if err != nil || !trusted || found.ID != device.ID {
		t.Fatalf("trusted = %v, device = %+v, err = %v", trusted, found, err)
	}
	for _, invalid := range []string{"", "   ", "not-a-token", token + "x"} {
		if _, trusted, err := service.Validate(ctx, invalid); err != nil || trusted {
			t.Fatalf("token %q trusted = %v, err = %v", invalid, trusted, err)
		}
	}
	// Expiry invalidates the cookie even though the row still exists.
	service.SetClock(func() time.Time { return base.Add(DefaultDeviceTrustConfig().TTL + time.Minute) })
	if _, trusted, err := service.Validate(ctx, token); err != nil || trusted {
		t.Fatalf("expired token trusted = %v, err = %v", trusted, err)
	}
	service.SetClock(func() time.Time { return base })
	if err := service.Revoke(ctx, "user-1", "user-1", device.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, trusted, err := service.Validate(ctx, token); err != nil || trusted {
		t.Fatalf("revoked token trusted = %v, err = %v", trusted, err)
	}
}

func TestDeviceValidateThrottlesLastSeenWrites(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")
	service := NewDeviceService(db, DeviceTrustConfig{Enabled: true, TTL: time.Hour, MaxPerUser: 3, TouchThrottle: 5 * time.Minute})
	base := time.Now().UTC().Truncate(time.Second)
	service.SetClock(func() time.Time { return base })
	token, device, err := service.Issue(ctx, "user-1", "ua", "ip")
	if err != nil {
		t.Fatal(err)
	}
	first := *device.LastSeenAt
	// Inside the throttle window the stored value must not move.
	service.SetClock(func() time.Time { return base.Add(time.Minute) })
	if _, trusted, err := service.Validate(ctx, token); err != nil || !trusted {
		t.Fatalf("trusted = %v, err = %v", trusted, err)
	}
	stored, err := db.UserDevices().GetByTokenHash(ctx, HashDeviceToken(token))
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LastSeenAt.Equal(first) {
		t.Fatalf("last_seen_at moved inside the throttle window: %v -> %v", first, stored.LastSeenAt)
	}
	service.SetClock(func() time.Time { return base.Add(10 * time.Minute) })
	if _, trusted, err := service.Validate(ctx, token); err != nil || !trusted {
		t.Fatalf("trusted = %v, err = %v", trusted, err)
	}
	stored, err = db.UserDevices().GetByTokenHash(ctx, HashDeviceToken(token))
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastSeenAt.Equal(first) {
		t.Fatal("last_seen_at was not refreshed after the throttle window")
	}
}

func TestDeviceIssueEvictsLeastRecentlySeenAtCap(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")
	service := NewDeviceService(db, DeviceTrustConfig{Enabled: true, TTL: 24 * time.Hour, MaxPerUser: 2, TouchThrottle: time.Minute})
	base := time.Now().UTC().Truncate(time.Second)
	service.SetClock(func() time.Time { return base })

	firstToken, first, err := service.Issue(ctx, "user-1", "ua-1", "ip")
	if err != nil {
		t.Fatal(err)
	}
	service.SetClock(func() time.Time { return base.Add(time.Minute) })
	if _, second, err := service.Issue(ctx, "user-1", "ua-2", "ip"); err != nil {
		t.Fatal(err)
	} else if second.ID == first.ID {
		t.Fatal("second issue reused the first device")
	}
	// Touch the first device so the second becomes the least recently seen.
	service.SetClock(func() time.Time { return base.Add(2 * time.Minute) })
	if _, trusted, err := service.Validate(ctx, firstToken); err != nil || !trusted {
		t.Fatalf("first device trusted = %v, err = %v", trusted, err)
	}
	service.SetClock(func() time.Time { return base.Add(3 * time.Minute) })
	if _, _, err := service.Issue(ctx, "user-1", "ua-3", "ip"); err != nil {
		t.Fatal(err)
	}
	count, err := db.UserDevices().CountActive(ctx, "user-1")
	if err != nil || count != 2 {
		t.Fatalf("active devices = %d, err = %v, want the cap of 2", count, err)
	}
	if _, trusted, err := service.Validate(ctx, firstToken); err != nil || !trusted {
		t.Fatalf("the most recently seen device was evicted: trusted = %v, err = %v", trusted, err)
	}
}

func TestDeviceListMarksCurrentAndHidesRevoked(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")
	service := NewDeviceService(db, DefaultDeviceTrustConfig())
	currentToken, current, err := service.Issue(ctx, "user-1", "ua-current", "ip")
	if err != nil {
		t.Fatal(err)
	}
	if _, other, err := service.Issue(ctx, "user-1", "ua-other", "ip"); err != nil {
		t.Fatal(err)
	} else if err := service.Rename(ctx, "user-1", other.ID, "Old laptop"); err != nil {
		t.Fatal(err)
	}
	views, err := service.List(ctx, "user-1", currentToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("views = %+v", views)
	}
	markedCurrent := 0
	for _, view := range views {
		if view.ID == current.ID {
			if !view.Current {
				t.Fatal("the presented device was not marked current")
			}
			markedCurrent++
		} else if view.Current {
			t.Fatal("a different device was marked current")
		}
		if view.Name == "" && view.UserAgent == "" {
			t.Fatalf("view lost its metadata: %+v", view)
		}
	}
	if markedCurrent != 1 {
		t.Fatalf("marked %d devices current", markedCurrent)
	}
	if err := service.Revoke(ctx, "user-1", "user-1", current.ID, false); err != nil {
		t.Fatal(err)
	}
	views, err = service.List(ctx, "user-1", currentToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("revoked device still listed: %+v", views)
	}
	if err := service.Rename(ctx, "user-1", "missing", "x"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("err = %v, want ErrDeviceNotFound", err)
	}
}

func TestDeviceRevokeEnforcesOwnership(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")
	seedLocalUser(t, db, "admin-1", "root", "admin", "admin-password")
	service := NewDeviceService(db, DefaultDeviceTrustConfig())
	_, device, err := service.Issue(ctx, "user-1", "ua", "ip")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(ctx, "user-2", "user-1", device.ID, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if err := service.Revoke(ctx, "user-1", "user-1", "missing", false); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("err = %v, want ErrDeviceNotFound", err)
	}
	if err := service.Revoke(ctx, "admin-1", "user-1", device.ID, true); err != nil {
		t.Fatalf("administrator revoke failed: %v", err)
	}
	if count, err := db.UserDevices().CountActive(ctx, "user-1"); err != nil || count != 0 {
		t.Fatalf("active devices = %d, err = %v", count, err)
	}
	if !contains(auditActions(t, db), "auth.device.revoke") {
		t.Fatalf("audit actions = %v", auditActions(t, db))
	}
}

func TestDeviceRevokeAllRequiresOwnershipAndSkipsEmptyAudit(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")
	service := NewDeviceService(db, DefaultDeviceTrustConfig())
	if _, _, err := service.Issue(ctx, "user-1", "ua", "ip"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Issue(ctx, "user-1", "ua", "ip"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RevokeAll(ctx, "user-2", "user-1", false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	revoked, err := service.RevokeAll(ctx, "user-1", "user-1", false)
	if err != nil || revoked != 2 {
		t.Fatalf("revoked = %d, err = %v, want 2", revoked, err)
	}
	again, err := service.RevokeAll(ctx, "user-1", "user-1", false)
	if err != nil || again != 0 {
		t.Fatalf("second revoke = %d, err = %v, want 0", again, err)
	}
	actions := auditActions(t, db)
	count := 0
	for _, action := range actions {
		if action == "auth.device.revoke_all" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("revoke_all audited %d times, want exactly 1 for the non-empty call", count)
	}
	total, err := service.CountActive(ctx)
	if err != nil || total != 0 {
		t.Fatalf("global active = %d, err = %v", total, err)
	}
}

func TestDeviceConfigDefaultsAreApplied(t *testing.T) {
	db := newIdentityStore(t)
	service := NewDeviceService(db, DeviceTrustConfig{})
	cfg := service.Config()
	// Enabled has no default: a partially built configuration must fail closed
	// rather than start trusting browsers by accident.
	if cfg.Enabled {
		t.Fatal("an unset DeviceTrustConfig must not enable device trust")
	}
	if cfg.TTL != 30*24*time.Hour || cfg.MaxPerUser != 10 || cfg.TouchThrottle != 5*time.Minute {
		t.Fatalf("config = %+v", cfg)
	}
	explicit := NewDeviceService(db, DefaultDeviceTrustConfig()).Config()
	if !explicit.Enabled || explicit.TTL != 30*24*time.Hour {
		t.Fatalf("default config = %+v", explicit)
	}
}

func TestDeviceTokenNeverAppearsInAnyPersistedField(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")
	service := NewDeviceService(db, DefaultDeviceTrustConfig())
	token, device, err := service.Issue(ctx, "user-1", "ua", "ip")
	if err != nil {
		t.Fatal(err)
	}
	row := db.SQL().QueryRowContext(ctx, `SELECT id,user_id,token_hash,COALESCE(name,''),COALESCE(user_agent,''),COALESCE(ip,''),trusted_at,expires_at FROM user_devices WHERE id=?`, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fields [8]string
	if err := row.Scan(&fields[0], &fields[1], &fields[2], &fields[3], &fields[4], &fields[5], &fields[6], &fields[7]); err != nil {
		t.Fatal(err)
	}
	for i, field := range fields {
		if strings.Contains(field, token) {
			t.Fatalf("field %d leaks the device token: %q", i, field)
		}
	}
	if _, err := db.UserDevices().Create(ctx, storage.UserDevice{UserID: "user-1", TokenHash: HashDeviceToken(token), ExpiresAt: time.Now().UTC().Add(time.Hour)}); !errors.Is(err, storage.ErrIdentityConflict) {
		t.Fatalf("duplicate token hash err = %v, want ErrIdentityConflict", err)
	}
}

// setDevicePolicy writes the authoritative auth_settings row, which is what an
// operator does through PUT /auth/policy.
func setDevicePolicy(t *testing.T, db *storage.DB, enabled bool, ttlSeconds int64, maxDevices int) {
	t.Helper()
	ctx := context.Background()
	err := db.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		settings, err := repos.Settings.SeedIfMissing(ctx, DefaultAuthSettings(storage.MFAModeDisabled, true, true, 30*24*time.Hour, 10, 0))
		if err != nil {
			return err
		}
		settings.DeviceTrustEnabled = enabled
		settings.DeviceTrustTTLSeconds = ttlSeconds
		settings.MaxTrustedDevices = maxDevices
		return repos.Settings.Update(ctx, settings)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestDevicePolicyIsReadFromDatabase proves the stored auth_settings row wins
// over the process configuration. Without this, an operator who shortens the
// device trust window or disables the feature in the console would see the
// change ignored on every node until a restart with an edited config file, which
// is exactly the silent-revert behaviour the design forbids.
func TestDevicePolicyIsReadFromDatabase(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")

	// The process configuration is deliberately permissive and long-lived.
	cfg := DefaultDeviceTrustConfig()
	cfg.TTL = 90 * 24 * time.Hour
	cfg.MaxPerUser = 50
	service := NewDeviceService(db, cfg)

	setDevicePolicy(t, db, true, 3600, 2)

	token, device, err := service.Issue(ctx, "user-1", "ua", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if got := device.ExpiresAt.Sub(device.TrustedAt); got != time.Hour {
		t.Fatalf("device lifetime = %v, want the stored 1h rather than the configured 90d", got)
	}

	// The stored cap of two evicts the least recently seen device on the third
	// grant, even though the process configuration allows fifty.
	if _, _, err := service.Issue(ctx, "user-1", "ua", "10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Issue(ctx, "user-1", "ua", "10.0.0.3"); err != nil {
		t.Fatal(err)
	}
	count, err := db.UserDevices().CountActive(ctx, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("active devices = %d, want the stored cap of 2", count)
	}
	if _, trusted, err := service.Validate(ctx, token); err != nil {
		t.Fatal(err)
	} else if trusted {
		t.Fatal("the evicted device is still trusted")
	}

	// Disabling the feature in the database stops both new grants and existing
	// bypasses immediately, with no restart.
	setDevicePolicy(t, db, false, 3600, 2)
	if _, _, err := service.Issue(ctx, "user-1", "ua", "10.0.0.4"); !errors.Is(err, ErrDeviceTrustDisabled) {
		t.Fatalf("err = %v, want ErrDeviceTrustDisabled after the database disabled device trust", err)
	}
	if _, trusted, err := service.Validate(ctx, token); err != nil || trusted {
		t.Fatalf("trusted = %v, err = %v, want false after the database disabled device trust", trusted, err)
	}
}

// TestDevicePolicySeedUsesProcessConfiguration proves the first committed read
// writes the process configuration into auth_settings, so a fresh deployment
// starts with the operator's file values rather than hardcoded ones.
//
// The seed is part of the same transaction as the device row, so a decision that
// fails leaves no partial policy behind: the row appears only once a device
// decision has actually committed.
func TestDevicePolicySeedUsesProcessConfiguration(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")

	cfg := DefaultDeviceTrustConfig()
	cfg.TTL = 48 * time.Hour
	cfg.MaxPerUser = 3
	service := NewDeviceService(db, cfg)

	_, device, err := service.Issue(ctx, "user-1", "ua", "ip")
	if err != nil {
		t.Fatal(err)
	}
	if got := device.ExpiresAt.Sub(device.TrustedAt); got != 48*time.Hour {
		t.Fatalf("device lifetime = %v, want the configured 48h", got)
	}

	var settings storage.AuthSettings
	err = db.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		stored, err := repos.Settings.Get(ctx)
		settings = stored
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.DeviceTrustEnabled {
		t.Fatal("seeded device trust is disabled, want the configured true")
	}
	if settings.DeviceTrustTTLSeconds != int64(48*time.Hour/time.Second) {
		t.Fatalf("seeded TTL = %d, want 172800", settings.DeviceTrustTTLSeconds)
	}
	if settings.MaxTrustedDevices != 3 {
		t.Fatalf("seeded max devices = %d, want 3", settings.MaxTrustedDevices)
	}
	// Seeding the device policy must not invent an MFA requirement: the mode
	// stays disabled so an upgraded deployment behaves exactly as before.
	if settings.MFAMode != storage.MFAModeDisabled {
		t.Fatalf("seeded MFA mode = %q, want disabled", settings.MFAMode)
	}
}

// TestDevicePolicySeedIsNotPersistedByAFailedDecision proves atomicity: a
// rejected grant leaves the policy row untouched rather than half-written.
func TestDevicePolicySeedIsNotPersistedByAFailedDecision(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "password")

	cfg := DefaultDeviceTrustConfig()
	cfg.Enabled = false
	service := NewDeviceService(db, cfg)

	if _, _, err := service.Issue(ctx, "user-1", "ua", "ip"); !errors.Is(err, ErrDeviceTrustDisabled) {
		t.Fatalf("err = %v, want ErrDeviceTrustDisabled", err)
	}

	err := db.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		_, err := repos.Settings.Get(ctx)
		return err
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("settings err = %v, want sql.ErrNoRows because the failed decision rolled back", err)
	}
}
