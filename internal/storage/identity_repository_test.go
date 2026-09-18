package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func newIdentityTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := "file:" + t.TempDir() + "/identity.sqlite"
	db, err := OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.Users().Create(ctx, User{ID: "user-a", Username: "alice", Role: "admin", PasswordHash: "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAuthSettingsSeedIfMissingReturnsStoredValue(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	defaults := AuthSettings{MFAMode: MFAModeOptional, DeviceTrustEnabled: true, DeviceTrustTTLSeconds: 3600, AllowTrustedDeviceBypass: true, MaxTrustedDevices: 3, SessionTokenTTLSeconds: 0}
	got, err := db.AuthSettings().SeedIfMissing(ctx, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if got.MFAMode != MFAModeOptional || got.MaxTrustedDevices != 3 || !got.DeviceTrustEnabled {
		t.Fatalf("seeded settings = %+v", got)
	}
	// A second seed with different values must not override an operator choice.
	again, err := db.AuthSettings().SeedIfMissing(ctx, AuthSettings{MFAMode: MFAModeRequired, MaxTrustedDevices: 99})
	if err != nil {
		t.Fatal(err)
	}
	if again.MFAMode != MFAModeOptional || again.MaxTrustedDevices != 3 {
		t.Fatalf("re-seed changed stored settings: %+v", again)
	}
	if err := db.AuthSettings().Update(ctx, AuthSettings{MFAMode: MFAModeRequired, MaxTrustedDevices: 7}); err != nil {
		t.Fatal(err)
	}
	updated, err := db.AuthSettings().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MFAMode != MFAModeRequired || updated.MaxTrustedDevices != 7 || updated.UpdatedAt.IsZero() {
		t.Fatalf("updated settings = %+v", updated)
	}
}

func TestAuthSettingsGetMissingRowReturnsNoRows(t *testing.T) {
	db := newIdentityTestDB(t)
	if _, err := db.AuthSettings().Get(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func newTestProvider(name string) OIDCProvider {
	return OIDCProvider{
		Name: name, DisplayName: "Corp SSO", Issuer: "https://idp.example.com",
		ClientID: "client-1", Scopes: "openid,profile,email", RedirectURI: "https://mesh.example.com/api/v1/auth/oidc/" + name + "/callback",
		IDTokenAlgs: "RS256", UsernameClaim: "preferred_username", RoleMappings: "[]", DefaultRole: "user",
		AuthoritativeRoles: true, AutoCreateUsers: true, PublicListed: true, Enabled: true,
	}
}

func TestOIDCProviderRoundTripAndCursorPaging(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		if _, err := db.OIDCProviders().Create(ctx, newTestProvider(name)); err != nil {
			t.Fatal(err)
		}
	}
	byName, err := db.OIDCProviders().GetByName(ctx, "bravo")
	if err != nil {
		t.Fatal(err)
	}
	if byName.DisplayName != "Corp SSO" || byName.HasSecret() {
		t.Fatalf("provider = %+v", byName)
	}
	page, err := db.OIDCProviders().List(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page = %+v", page)
	}
	next, err := db.OIDCProviders().List(ctx, page.NextCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 1 || next.HasMore {
		t.Fatalf("second page = %+v", next)
	}
	if next.Items[0].ID == page.Items[0].ID || next.Items[0].ID == page.Items[1].ID {
		t.Fatal("cursor page repeated a row")
	}
	// The name is unique, so a duplicate must surface as a typed conflict.
	if _, err := db.OIDCProviders().Create(ctx, newTestProvider("alpha")); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("duplicate name err = %v, want ErrIdentityConflict", err)
	}
	byID, err := db.OIDCProviders().Get(ctx, byName.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID.ClientSecretCiphertext = "sealed"
	byID.ClientSecretNonce = "nonce"
	byID.ClientSecretKeyID = "key-1"
	byID.ClientSecretVersion = 1
	byID.Enabled = false
	if err := db.OIDCProviders().Update(ctx, byID); err != nil {
		t.Fatal(err)
	}
	reloaded, err := db.OIDCProviders().Get(ctx, byID.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.HasSecret() || reloaded.ClientSecretVersion != 1 || reloaded.Enabled {
		t.Fatalf("reloaded provider = %+v", reloaded)
	}
	if count, err := db.OIDCProviders().CountEnabled(ctx); err != nil || count != 2 {
		t.Fatalf("enabled count = %d, err = %v, want 2", count, err)
	}
	if err := db.OIDCProviders().Delete(ctx, reloaded.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.OIDCProviders().Get(ctx, reloaded.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get after delete err = %v, want sql.ErrNoRows", err)
	}
}

func TestOIDCProviderListEnabledPublicFiltersBothFlags(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	hidden := newTestProvider("hidden")
	hidden.PublicListed = false
	disabled := newTestProvider("disabled")
	disabled.Enabled = false
	for _, provider := range []OIDCProvider{newTestProvider("listed"), hidden, disabled} {
		if _, err := db.OIDCProviders().Create(ctx, provider); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.OIDCProviders().ListEnabledPublic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "listed" {
		t.Fatalf("public providers = %+v", got)
	}
}

func TestUserIdentityUniqueSubjectPerProvider(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	first, err := db.UserIdentities().Create(ctx, UserIdentity{UserID: "user-a", ProviderID: "provider-1", Subject: "sub-1", Email: "alice@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.CreatedAt.IsZero() {
		t.Fatalf("created identity = %+v", first)
	}
	// The same subject on a different provider is a distinct identity.
	if _, err := db.UserIdentities().Create(ctx, UserIdentity{UserID: "user-a", ProviderID: "provider-2", Subject: "sub-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UserIdentities().Create(ctx, UserIdentity{UserID: "user-a", ProviderID: "provider-1", Subject: "sub-1"}); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("duplicate subject err = %v, want ErrIdentityConflict", err)
	}
	got, err := db.UserIdentities().GetByProviderSubject(ctx, "provider-1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "alice@example.com" || got.LastLoginAt != nil {
		t.Fatalf("identity = %+v", got)
	}
	when := time.Now().UTC().Truncate(time.Second)
	if err := db.UserIdentities().UpdateLastLogin(ctx, got.ID, when, "alice@corp.example.com", ""); err != nil {
		t.Fatal(err)
	}
	updated, err := db.UserIdentities().GetByProviderSubject(ctx, "provider-1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastLoginAt == nil || updated.Email != "alice@corp.example.com" || updated.DisplayName != "" {
		t.Fatalf("updated identity = %+v", updated)
	}
	if count, err := db.UserIdentities().CountByUser(ctx, "user-a"); err != nil || count != 2 {
		t.Fatalf("identity count = %d, err = %v, want 2", count, err)
	}
	listed, err := db.UserIdentities().ListByUser(ctx, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed identities = %d, want 2", len(listed))
	}
	if err := db.UserIdentities().Delete(ctx, listed[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := db.UserIdentities().DeleteByUser(ctx, "user-a"); err != nil {
		t.Fatal(err)
	}
	if count, err := db.UserIdentities().CountByUser(ctx, "user-a"); err != nil || count != 0 {
		t.Fatalf("identity count after delete = %d, err = %v, want 0", count, err)
	}
}

func TestUserMFAUpsertReplacesPendingAndGuardIsMonotonic(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	enrollment := UserMFA{UserID: "user-a", SecretCiphertext: "sealed-1", SecretNonce: "nonce-1", SecretKeyID: "key-1", SecretVersion: 1, Status: MFAStatusPending, LastUsedStep: -1}
	if err := db.UserMFA().Upsert(ctx, enrollment); err != nil {
		t.Fatal(err)
	}
	replacement := enrollment
	replacement.SecretCiphertext = "sealed-2"
	if err := db.UserMFA().Upsert(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	got, err := db.UserMFA().Get(ctx, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.SecretCiphertext != "sealed-2" || got.Status != MFAStatusPending || got.LastUsedStep != -1 {
		t.Fatalf("mfa row = %+v", got)
	}
	if err := db.UserMFA().SetStatus(ctx, "user-a", MFAStatusEnabled, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	enabled, err := db.UserMFA().Get(ctx, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Status != MFAStatusEnabled || enabled.EnabledAt == nil {
		t.Fatalf("enabled row = %+v", enabled)
	}
	if err := db.UserMFA().MarkUsed(ctx, "user-a", 100, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := db.UserMFA().MarkUsed(ctx, "user-a", 100, time.Now().UTC()); !errors.Is(err, ErrMFAStepReplay) {
		t.Fatalf("replayed step err = %v, want ErrMFAStepReplay", err)
	}
	if err := db.UserMFA().MarkUsed(ctx, "user-a", 99, time.Now().UTC()); !errors.Is(err, ErrMFAStepReplay) {
		t.Fatalf("older step err = %v, want ErrMFAStepReplay", err)
	}
	if err := db.UserMFA().MarkUsed(ctx, "user-a", 101, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	used, err := db.UserMFA().Get(ctx, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if used.LastUsedStep != 101 || used.LastUsedAt == nil {
		t.Fatalf("used row = %+v", used)
	}
	if count, err := db.UserMFA().CountEnabled(ctx); err != nil || count != 1 {
		t.Fatalf("enabled count = %d, err = %v, want 1", count, err)
	}
	if err := db.UserMFA().Delete(ctx, "user-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UserMFA().Get(ctx, "user-a"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get after delete err = %v, want sql.ErrNoRows", err)
	}
}

func TestUserRecoveryCodesReplaceConsumeAndCount(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	hashes := []string{"hash-1", "hash-2", "hash-3"}
	if err := db.UserRecoveryCodes().ReplaceAll(ctx, "user-a", hashes); err != nil {
		t.Fatal(err)
	}
	if count, err := db.UserRecoveryCodes().CountUnused(ctx, "user-a"); err != nil || count != 3 {
		t.Fatalf("unused count = %d, err = %v, want 3", count, err)
	}
	consumed, err := db.UserRecoveryCodes().Consume(ctx, "user-a", "hash-2")
	if err != nil || !consumed {
		t.Fatalf("consume = %v, err = %v, want true", consumed, err)
	}
	again, err := db.UserRecoveryCodes().Consume(ctx, "user-a", "hash-2")
	if err != nil || again {
		t.Fatalf("second consume = %v, err = %v, want false", again, err)
	}
	if unknown, err := db.UserRecoveryCodes().Consume(ctx, "user-a", "hash-9"); err != nil || unknown {
		t.Fatalf("unknown code consume = %v, err = %v, want false", unknown, err)
	}
	unused, err := db.UserRecoveryCodes().ListUnused(ctx, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(unused) != 2 {
		t.Fatalf("unused hashes = %v", unused)
	}
	// ReplaceAll drops the previous generation entirely.
	if err := db.UserRecoveryCodes().ReplaceAll(ctx, "user-a", []string{"hash-9"}); err != nil {
		t.Fatal(err)
	}
	if count, err := db.UserRecoveryCodes().CountUnused(ctx, "user-a"); err != nil || count != 1 {
		t.Fatalf("unused count after replace = %d, err = %v, want 1", count, err)
	}
	if err := db.UserRecoveryCodes().DeleteByUser(ctx, "user-a"); err != nil {
		t.Fatal(err)
	}
	if count, err := db.UserRecoveryCodes().CountUnused(ctx, "user-a"); err != nil || count != 0 {
		t.Fatalf("unused count after delete = %d, err = %v, want 0", count, err)
	}
}

func TestUserDeviceLifecycleAndExpirySweep(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	device, err := db.UserDevices().Create(ctx, UserDevice{UserID: "user-a", TokenHash: "device-hash-1", Name: "MacBook", UserAgent: "Mozilla/5.0", IP: "10.0.0.5", TrustedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: &now})
	if err != nil {
		t.Fatal(err)
	}
	if device.ID == "" {
		t.Fatalf("device = %+v", device)
	}
	if _, err := db.UserDevices().Create(ctx, UserDevice{UserID: "user-a", TokenHash: "device-hash-1", ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("duplicate token hash err = %v, want ErrIdentityConflict", err)
	}
	if _, err := db.UserDevices().Create(ctx, UserDevice{UserID: "user-a", TokenHash: "", ExpiresAt: now.Add(time.Hour)}); err == nil {
		t.Fatal("empty token hash must be rejected")
	}
	if _, err := db.UserDevices().Create(ctx, UserDevice{UserID: "user-a", TokenHash: "device-hash-x"}); err == nil {
		t.Fatal("missing expiry must be rejected")
	}
	got, err := db.UserDevices().GetByTokenHash(ctx, "device-hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "MacBook" || got.UserAgent != "Mozilla/5.0" || got.IP != "10.0.0.5" {
		t.Fatalf("device = %+v", got)
	}
	if _, err := db.UserDevices().GetByTokenHash(ctx, ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty hash err = %v, want sql.ErrNoRows", err)
	}
	if err := db.UserDevices().Rename(ctx, "user-a", got.ID, "Work laptop"); err != nil {
		t.Fatal(err)
	}
	if err := db.UserDevices().Touch(ctx, got.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	renamed, err := db.UserDevices().Get(ctx, "user-a", got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Work laptop" || renamed.LastSeenAt == nil {
		t.Fatalf("renamed device = %+v", renamed)
	}
	if _, err := db.UserDevices().Get(ctx, "user-b", got.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-user get err = %v, want sql.ErrNoRows", err)
	}
	if count, err := db.UserDevices().CountActive(ctx, "user-a"); err != nil || count != 1 {
		t.Fatalf("active count = %d, err = %v, want 1", count, err)
	}
	oldest, err := db.UserDevices().OldestActive(ctx, "user-a")
	if err != nil || oldest.ID != got.ID {
		t.Fatalf("oldest = %+v, err = %v", oldest, err)
	}
	if all, err := db.UserDevices().CountActiveAll(ctx); err != nil || all != 1 {
		t.Fatalf("global active count = %d, err = %v, want 1", all, err)
	}
	if err := db.UserDevices().Revoke(ctx, "user-a", got.ID, now); err != nil {
		t.Fatal(err)
	}
	if count, err := db.UserDevices().CountActive(ctx, "user-a"); err != nil || count != 0 {
		t.Fatalf("active count after revoke = %d, err = %v, want 0", count, err)
	}
	listed, err := db.UserDevices().ListByUser(ctx, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("list after revoke = %+v", listed)
	}
	second, err := db.UserDevices().Create(ctx, UserDevice{UserID: "user-a", TokenHash: "device-hash-2", ExpiresAt: now.Add(48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := db.UserDevices().RevokeAllForUser(ctx, "user-a", now)
	if err != nil || revoked != 1 {
		t.Fatalf("revoked = %d, err = %v, want 1", revoked, err)
	}
	// Only the freshly revoked row is old enough to be swept; the revoked row
	// from an hour ago survives because it is newer than the retention cutoff.
	deleted, err := db.UserDevices().DeleteExpired(ctx, now.Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("swept %d devices, want 0", deleted)
	}
	if _, err := db.UserDevices().Get(ctx, "user-a", second.ID); err != nil {
		t.Fatalf("recent device was swept: %v", err)
	}
	deleted, err = db.UserDevices().DeleteExpired(ctx, now.Add(72*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("swept %d devices, want 2", deleted)
	}
}

func TestAuthChallengeConsumeOnceAndAttemptBudget(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	challenge := AuthChallenge{ID: "challenge-1", Kind: ChallengeKindLoginMFA, UserID: "user-a", PayloadCiphertext: "sealed", PayloadNonce: "nonce", PayloadKeyID: "key-1", PayloadVersion: 1, MaxAttempts: 3, ExpiresAt: now.Add(5 * time.Minute), CreatedAt: now}
	if err := db.AuthChallenges().Create(ctx, challenge); err != nil {
		t.Fatal(err)
	}
	if err := db.AuthChallenges().Create(ctx, AuthChallenge{ID: "", Kind: ChallengeKindLoginMFA, PayloadCiphertext: "sealed", ExpiresAt: now.Add(time.Minute)}); err == nil {
		t.Fatal("empty challenge id must be rejected")
	}
	if err := db.AuthChallenges().Create(ctx, AuthChallenge{ID: "c2", Kind: ChallengeKindLoginMFA, PayloadCiphertext: "sealed"}); err == nil {
		t.Fatal("missing expiry must be rejected")
	}
	got, err := db.AuthChallenges().Get(ctx, "challenge-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ChallengeKindLoginMFA || got.PayloadCiphertext != "sealed" || got.Attempts != 0 {
		t.Fatalf("challenge = %+v", got)
	}
	if _, err := db.AuthChallenges().Get(ctx, ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty id err = %v, want sql.ErrNoRows", err)
	}
	if count, err := db.AuthChallenges().CountPending(ctx); err != nil || count != 1 {
		t.Fatalf("pending count = %d, err = %v, want 1", count, err)
	}
	for i := 1; i <= 2; i++ {
		attempts, exhausted, err := db.AuthChallenges().IncrementAttempts(ctx, "challenge-1")
		if err != nil {
			t.Fatal(err)
		}
		if attempts != i || exhausted {
			t.Fatalf("attempt %d = %d, exhausted %v", i, attempts, exhausted)
		}
	}
	attempts, exhausted, err := db.AuthChallenges().IncrementAttempts(ctx, "challenge-1")
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || !exhausted {
		t.Fatalf("final attempt = %d, exhausted %v, want 3 true", attempts, exhausted)
	}
	// An exhausted challenge is consumed, so further guesses cannot proceed.
	if _, exhausted, err := db.AuthChallenges().IncrementAttempts(ctx, "challenge-1"); err != nil || !exhausted {
		t.Fatalf("post-exhaust increment exhausted = %v, err = %v", exhausted, err)
	}
	if consumed, err := db.AuthChallenges().Consume(ctx, "challenge-1", now); err != nil || consumed {
		t.Fatalf("consume after exhaustion = %v, err = %v, want false", consumed, err)
	}
	fresh := challenge
	fresh.ID = "challenge-2"
	if err := db.AuthChallenges().Create(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if consumed, err := db.AuthChallenges().Consume(ctx, "challenge-2", now); err != nil || !consumed {
		t.Fatalf("first consume = %v, err = %v, want true", consumed, err)
	}
	if consumed, err := db.AuthChallenges().Consume(ctx, "challenge-2", now); err != nil || consumed {
		t.Fatalf("second consume = %v, err = %v, want false", consumed, err)
	}
	if _, exhausted, err := db.AuthChallenges().IncrementAttempts(ctx, "unknown"); err != nil || !exhausted {
		t.Fatalf("unknown challenge exhausted = %v, err = %v, want true", exhausted, err)
	}
	expired := challenge
	expired.ID = "challenge-expired"
	expired.ExpiresAt = now.Add(-time.Minute)
	if err := db.AuthChallenges().Create(ctx, expired); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.AuthChallenges().DeleteExpired(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted %d challenges, want 1", deleted)
	}
	if _, err := db.AuthChallenges().Get(ctx, "challenge-1"); err != nil {
		t.Fatalf("live challenge was swept: %v", err)
	}
}

func TestAuthLoginAttemptThrottleWindowAndReset(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	repo := db.AuthLoginAttempts()
	if blocked, _, err := repo.IsBlocked(ctx, "bucket-1"); err != nil || blocked {
		t.Fatalf("fresh bucket blocked = %v, err = %v", blocked, err)
	}
	for i := 1; i <= 2; i++ {
		blocked, retryAfter, err := repo.RegisterFailure(ctx, "bucket-1", 5*time.Minute, 3, 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if blocked || retryAfter != 0 {
			t.Fatalf("attempt %d blocked = %v, retryAfter = %v", i, blocked, retryAfter)
		}
	}
	blocked, retryAfter, err := repo.RegisterFailure(ctx, "bucket-1", 5*time.Minute, 3, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked || retryAfter <= 0 {
		t.Fatalf("third attempt blocked = %v, retryAfter = %v, want true positive", blocked, retryAfter)
	}
	isBlocked, wait, err := repo.IsBlocked(ctx, "bucket-1")
	if err != nil || !isBlocked || wait <= 0 {
		t.Fatalf("IsBlocked = %v, %v, err = %v", isBlocked, wait, err)
	}
	if count, err := repo.CountBlocked(ctx); err != nil || count != 1 {
		t.Fatalf("blocked count = %d, err = %v, want 1", count, err)
	}
	if err := repo.RegisterSuccess(ctx, "bucket-1"); err != nil {
		t.Fatal(err)
	}
	if isBlocked, _, err := repo.IsBlocked(ctx, "bucket-1"); err != nil || isBlocked {
		t.Fatalf("blocked after success = %v, err = %v", isBlocked, err)
	}
	if err := repo.RegisterSuccess(ctx, ""); err != nil {
		t.Fatalf("empty bucket success err = %v", err)
	}
	if _, _, err := repo.RegisterFailure(ctx, "", time.Minute, 1, time.Minute); err == nil {
		t.Fatal("empty bucket key must be rejected")
	}
	// A failure after the window rolls over restarts the counter, so a tiny
	// window never escalates to a block.
	for i := 0; i < 2; i++ {
		if _, _, err := repo.RegisterFailure(ctx, "bucket-2", time.Nanosecond, 3, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if blocked, _, err := repo.RegisterFailure(ctx, "bucket-2", time.Nanosecond, 3, time.Minute); err != nil || blocked {
		t.Fatalf("rolled-over window blocked = %v, err = %v, want false", blocked, err)
	}
	if _, err := db.SQL().ExecContext(ctx, `UPDATE auth_login_attempts SET updated_at=?`, tmFixed(time.Now().UTC().Add(-48*time.Hour))); err != nil {
		t.Fatal(err)
	}
	deleted, err := repo.DeleteExpired(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deleted == 0 {
		t.Fatal("expired buckets were not swept")
	}
}

func TestUserIdentityColumnsRoundTripThroughUserRepository(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	writer, ok := db.Users().(UserIdentityWriter)
	if !ok {
		t.Fatal("user repository does not implement UserIdentityWriter")
	}
	created := User{ID: "user-oidc", Username: "bob", Role: "user", PasswordHash: PasswordHashNone, AuthSource: AuthSourceOIDC, MFARequired: true}
	if err := db.Users().Create(ctx, created); err != nil {
		t.Fatal(err)
	}
	got, err := db.Users().Get(ctx, "user-oidc")
	if err != nil {
		t.Fatal(err)
	}
	if got.AuthSource != AuthSourceOIDC || !got.MFARequired || got.PasswordHash != PasswordHashNone {
		t.Fatalf("user = %+v", got)
	}
	// A legacy-shaped insert without an explicit source defaults to local.
	if err := db.Users().Create(ctx, User{ID: "user-legacy", Username: "carol", Role: "user", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	legacy, err := db.Users().GetByUsername(ctx, "carol")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.AuthSource != AuthSourceLocal || legacy.MFARequired {
		t.Fatalf("legacy user = %+v", legacy)
	}
	if err := writer.SetAuthSource(ctx, "user-legacy", AuthSourceMixed); err != nil {
		t.Fatal(err)
	}
	if err := writer.SetMFARequired(ctx, "user-legacy", true); err != nil {
		t.Fatal(err)
	}
	updated, err := db.Users().Get(ctx, "user-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if updated.AuthSource != AuthSourceMixed || !updated.MFARequired {
		t.Fatalf("updated user = %+v", updated)
	}
	if err := writer.SetAuthSource(ctx, "user-legacy", AuthSource("saml")); err == nil {
		t.Fatal("invalid auth source must be rejected")
	}
	if err := writer.SetMFARequired(ctx, "missing", true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing user err = %v, want sql.ErrNoRows", err)
	}
	listed, err := db.Users().List(ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 3 {
		t.Fatalf("listed %d users, want 3", len(listed.Items))
	}
	for _, user := range listed.Items {
		if user.AuthSource == "" {
			t.Fatalf("listed user lost its auth source: %+v", user)
		}
	}
}

func TestIdentityTransactionRollsBackEveryWrite(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	sentinel := errors.New("boom")
	err := db.IdentityTransaction(ctx, func(repos IdentityRepositories) error {
		if err := repos.MFA.Upsert(ctx, UserMFA{UserID: "user-a", SecretCiphertext: "sealed", SecretNonce: "nonce", SecretKeyID: "key", SecretVersion: 1, Status: MFAStatusPending, LastUsedStep: -1}); err != nil {
			return err
		}
		if _, err := repos.Devices.Create(ctx, UserDevice{UserID: "user-a", TokenHash: "device-hash-rollback", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
			return err
		}
		if err := repos.Audits.Create(ctx, AuditLog{ActorUserID: "user-a", Action: "auth.mfa.enroll", ResourceType: "user_mfa", ResourceID: "user-a", Details: "{}"}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("transaction err = %v, want sentinel", err)
	}
	if _, err := db.UserMFA().Get(ctx, "user-a"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mfa row survived rollback: %v", err)
	}
	if _, err := db.UserDevices().GetByTokenHash(ctx, "device-hash-rollback"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("device row survived rollback: %v", err)
	}
	page, err := db.Audits().List(ctx, AuditFilter{Action: "auth.mfa.enroll"}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("audit row survived rollback: %+v", page.Items)
	}
}

func TestIdentityTransactionCommitsAuditWithTheMutation(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	err := db.IdentityTransaction(ctx, func(repos IdentityRepositories) error {
		if _, err := repos.Devices.Create(ctx, UserDevice{UserID: "user-a", TokenHash: "device-hash-tx", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
			return err
		}
		return repos.Audits.Create(ctx, AuditLog{ActorUserID: "user-a", Action: "auth.device.trust", ResourceType: "user_device", ResourceID: "user-a", Details: "{}"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := db.UserDevices().CountActive(ctx, "user-a"); err != nil || count != 1 {
		t.Fatalf("active devices = %d, err = %v, want 1", count, err)
	}
	page, err := db.Audits().List(ctx, AuditFilter{Action: "auth.device.trust"}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(page.Items))
	}
}

func TestTimestampsSortChronologicallyInIdentityTables(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	// tm() trims trailing zeros, which would make the whole second sort after
	// the fractional second. Identity tables must use the fixed-width form.
	if tmFixed(base) >= tmFixed(base.Add(500*time.Millisecond)) {
		t.Fatalf("tmFixed ordering broken: %q >= %q", tmFixed(base), tmFixed(base.Add(500*time.Millisecond)))
	}
	if err := db.AuthChallenges().Create(ctx, AuthChallenge{ID: "c-second", Kind: ChallengeKindLoginTicket, PayloadCiphertext: "s", PayloadNonce: "n", PayloadKeyID: "k", PayloadVersion: 1, MaxAttempts: 1, ExpiresAt: base, CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if err := db.AuthChallenges().Create(ctx, AuthChallenge{ID: "c-fraction", Kind: ChallengeKindLoginTicket, PayloadCiphertext: "s", PayloadNonce: "n", PayloadKeyID: "k", PayloadVersion: 1, MaxAttempts: 1, ExpiresAt: base.Add(500 * time.Millisecond), CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.AuthChallenges().DeleteExpired(ctx, base.Add(250*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted %d challenges, want exactly the whole-second row", deleted)
	}
	if _, err := db.AuthChallenges().Get(ctx, "c-second"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected c-second to be swept, err = %v", err)
	}
	if _, err := db.AuthChallenges().Get(ctx, "c-fraction"); err != nil {
		t.Fatalf("c-fraction must survive: %v", err)
	}
}

// runIdentityRepositoryContract asserts the dialect-sensitive identity
// behaviour: unique constraints, CASE-based conditional updates, the TOTP
// replay guard, and the cluster-shared login throttle. SQLite runs it directly;
// MySQL runs the same function so a driver-specific SQL error cannot slip
// through. It expects an empty database and seeds its own account.
func runIdentityRepositoryContract(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	if err := db.Users().Create(ctx, User{ID: "contract-user", Username: "contract", Role: "admin", PasswordHash: PasswordHashNone, AuthSource: AuthSourceOIDC}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := db.Users().Get(ctx, "contract-user")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AuthSource != AuthSourceOIDC {
		t.Fatalf("auth source = %q, want oidc", reloaded.AuthSource)
	}
	writer := db.Users().(UserIdentityWriter)
	if err := writer.SetAuthSource(ctx, "contract-user", AuthSourceMixed); err != nil {
		t.Fatal(err)
	}
	if err := writer.SetMFARequired(ctx, "contract-user", true); err != nil {
		t.Fatal(err)
	}
	if updated, err := db.Users().Get(ctx, "contract-user"); err != nil || updated.AuthSource != AuthSourceMixed || !updated.MFARequired {
		t.Fatalf("updated user = %+v, err = %v", updated, err)
	}

	settings, err := db.AuthSettings().SeedIfMissing(ctx, AuthSettings{MFAMode: MFAModeRequired, DeviceTrustEnabled: true, DeviceTrustTTLSeconds: 60, MaxTrustedDevices: 2})
	if err != nil {
		t.Fatal(err)
	}
	if settings.MFAMode != MFAModeRequired {
		t.Fatalf("settings = %+v", settings)
	}

	if _, err := db.OIDCProviders().Create(ctx, newTestProvider("contract")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.OIDCProviders().Create(ctx, newTestProvider("contract")); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("duplicate provider err = %v, want ErrIdentityConflict", err)
	}
	provider, err := db.OIDCProviders().GetByName(ctx, "contract")
	if err != nil {
		t.Fatal(err)
	}
	provider.Enabled = false
	if err := db.OIDCProviders().Update(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if again, err := db.OIDCProviders().Get(ctx, provider.ID); err != nil || again.Enabled {
		t.Fatalf("provider after update = %+v, err = %v", again, err)
	}

	if _, err := db.UserIdentities().Create(ctx, UserIdentity{UserID: "contract-user", ProviderID: provider.ID, Subject: "sub-contract"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UserIdentities().Create(ctx, UserIdentity{UserID: "contract-user", ProviderID: provider.ID, Subject: "sub-contract"}); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("duplicate identity err = %v, want ErrIdentityConflict", err)
	}
	identity, err := db.UserIdentities().GetByProviderSubject(ctx, provider.ID, "sub-contract")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UserIdentities().UpdateLastLogin(ctx, identity.ID, time.Now().UTC(), "", "Contract User"); err != nil {
		t.Fatal(err)
	}
	if after, err := db.UserIdentities().GetByProviderSubject(ctx, provider.ID, "sub-contract"); err != nil || after.LastLoginAt == nil || after.DisplayName != "Contract User" {
		t.Fatalf("identity after login = %+v, err = %v", after, err)
	}

	if err := db.UserMFA().Upsert(ctx, UserMFA{UserID: "contract-user", SecretCiphertext: "sealed", SecretNonce: "nonce", SecretKeyID: "key", SecretVersion: 1, Status: MFAStatusPending, LastUsedStep: -1}); err != nil {
		t.Fatal(err)
	}
	if err := db.UserMFA().Upsert(ctx, UserMFA{UserID: "contract-user", SecretCiphertext: "sealed-2", SecretNonce: "nonce", SecretKeyID: "key", SecretVersion: 1, Status: MFAStatusPending, LastUsedStep: -1}); err != nil {
		t.Fatal(err)
	}
	if got, err := db.UserMFA().Get(ctx, "contract-user"); err != nil || got.SecretCiphertext != "sealed-2" || got.LastUsedStep != -1 {
		t.Fatalf("mfa = %+v, err = %v", got, err)
	}
	if err := db.UserMFA().SetStatus(ctx, "contract-user", MFAStatusEnabled, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := db.UserMFA().MarkUsed(ctx, "contract-user", 5, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := db.UserMFA().MarkUsed(ctx, "contract-user", 5, time.Now().UTC()); !errors.Is(err, ErrMFAStepReplay) {
		t.Fatalf("replay err = %v, want ErrMFAStepReplay", err)
	}

	if err := db.UserRecoveryCodes().ReplaceAll(ctx, "contract-user", []string{"h1", "h2"}); err != nil {
		t.Fatal(err)
	}
	if consumed, err := db.UserRecoveryCodes().Consume(ctx, "contract-user", "h1"); err != nil || !consumed {
		t.Fatalf("consume = %v, err = %v", consumed, err)
	}
	if consumed, err := db.UserRecoveryCodes().Consume(ctx, "contract-user", "h1"); err != nil || consumed {
		t.Fatalf("second consume = %v, err = %v", consumed, err)
	}
	if count, err := db.UserRecoveryCodes().CountUnused(ctx, "contract-user"); err != nil || count != 1 {
		t.Fatalf("unused = %d, err = %v, want 1", count, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.UserDevices().Create(ctx, UserDevice{UserID: "contract-user", TokenHash: "hash-contract", Name: "Contract", UserAgent: "ua", IP: "10.1.2.3", TrustedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: &now}); err != nil {
		t.Fatal(err)
	}
	oldest, err := db.UserDevices().OldestActive(ctx, "contract-user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UserDevices().Touch(ctx, oldest.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.UserDevices().Revoke(ctx, "contract-user", oldest.ID, now); err != nil {
		t.Fatal(err)
	}
	if count, err := db.UserDevices().CountActive(ctx, "contract-user"); err != nil || count != 0 {
		t.Fatalf("active devices = %d, err = %v, want 0", count, err)
	}

	challenge := AuthChallenge{ID: "contract-challenge", Kind: ChallengeKindLoginMFA, UserID: "contract-user", PayloadCiphertext: "sealed", PayloadNonce: "nonce", PayloadKeyID: "key", PayloadVersion: 1, MaxAttempts: 1, ExpiresAt: now.Add(5 * time.Minute), CreatedAt: now}
	if err := db.AuthChallenges().Create(ctx, challenge); err != nil {
		t.Fatal(err)
	}
	if _, exhausted, err := db.AuthChallenges().IncrementAttempts(ctx, "contract-challenge"); err != nil || !exhausted {
		t.Fatalf("exhausted = %v, err = %v, want true", exhausted, err)
	}
	if consumed, err := db.AuthChallenges().Consume(ctx, "contract-challenge", now); err != nil || consumed {
		t.Fatalf("consume exhausted challenge = %v, err = %v, want false", consumed, err)
	}

	repo := db.AuthLoginAttempts()
	for i := 0; i < 2; i++ {
		if blocked, _, err := repo.RegisterFailure(ctx, "contract-bucket", 5*time.Minute, 3, time.Minute); err != nil || blocked {
			t.Fatalf("attempt %d blocked = %v, err = %v", i, blocked, err)
		}
	}
	if blocked, retryAfter, err := repo.RegisterFailure(ctx, "contract-bucket", 5*time.Minute, 3, time.Minute); err != nil || !blocked || retryAfter <= 0 {
		t.Fatalf("blocked = %v, retryAfter = %v, err = %v", blocked, retryAfter, err)
	}
	if err := repo.RegisterSuccess(ctx, "contract-bucket"); err != nil {
		t.Fatal(err)
	}
	if blocked, _, err := repo.IsBlocked(ctx, "contract-bucket"); err != nil || blocked {
		t.Fatalf("blocked after success = %v, err = %v", blocked, err)
	}

	if err := db.IdentityTransaction(ctx, func(repos IdentityRepositories) error {
		return repos.Audits.Create(ctx, AuditLog{ActorUserID: "contract-user", Action: "auth.contract", ResourceType: "test", ResourceID: "contract-user", Details: "{}"})
	}); err != nil {
		t.Fatal(err)
	}
	page, err := db.Audits().List(ctx, AuditFilter{Action: "auth.contract"}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(page.Items))
	}
}

func TestSQLiteIdentityRepositoryContract(t *testing.T) {
	runIdentityRepositoryContract(t, newIdentityTestDB(t))
}

func TestCountActiveAdminsIgnoresDisabledAndDeleted(t *testing.T) {
	db := newIdentityTestDB(t)
	ctx := context.Background()
	if count, err := db.Admins().CountActiveAdmins(ctx); err != nil || count != 1 {
		t.Fatalf("active admins = %d, err = %v, want 1", count, err)
	}
	if err := db.Users().Create(ctx, User{ID: "user-b", Username: "bob", Role: "admin", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if count, err := db.Admins().CountActiveAdmins(ctx); err != nil || count != 2 {
		t.Fatalf("active admins = %d, err = %v, want 2", count, err)
	}
	disabled, err := db.Users().Get(ctx, "user-b")
	if err != nil {
		t.Fatal(err)
	}
	disabled.Disabled = true
	if err := db.Users().Update(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	if count, err := db.Admins().CountActiveAdmins(ctx); err != nil || count != 1 {
		t.Fatalf("active admins after disable = %d, err = %v, want 1", count, err)
	}
	if err := db.Users().Create(ctx, User{ID: "user-c", Username: "carol", Role: "user", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if count, err := db.Admins().CountActiveAdmins(ctx); err != nil || count != 1 {
		t.Fatalf("active admins with a non-admin = %d, err = %v, want 1", count, err)
	}
}
