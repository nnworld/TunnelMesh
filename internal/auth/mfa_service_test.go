package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func newTestMFAService(t *testing.T, db *storage.DB, secrets SecretProvider) *MFAService {
	t.Helper()
	service := NewMFAService(db, secrets, DefaultMFAConfig())
	return service
}

func currentCode(t *testing.T, enrollment Enrollment, at time.Time) string {
	t.Helper()
	code, err := TOTPCode(enrollment.Secret, at, DefaultTOTPParams())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestMFAEnrollReturnsSecretOnceAndStoresOnlyCiphertext(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "correct horse battery staple")
	service := newTestMFAService(t, db, testSecretProvider(t))

	enrollment, err := service.Enroll(ctx, "user-1", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if len(enrollment.Secret) == 0 || !strings.HasPrefix(enrollment.OTPAuthURL, "otpauth://totp/TunnelMesh:alice?") {
		t.Fatalf("enrollment = %+v", enrollment)
	}
	if len(enrollment.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(enrollment.RecoveryCodes))
	}
	if enrollment.ExpiresAt.Before(time.Now().UTC()) {
		t.Fatalf("enrollment already expired: %v", enrollment.ExpiresAt)
	}

	row, err := db.UserMFA().Get(ctx, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != storage.MFAStatusPending || row.EnabledAt != nil {
		t.Fatalf("mfa row = %+v, want pending and unconfirmed", row)
	}
	if row.SecretCiphertext == "" || strings.Contains(row.SecretCiphertext, enrollment.Secret) {
		t.Fatal("stored secret is missing or plaintext")
	}
	if strings.Contains(row.SecretNonce, enrollment.Secret) || row.SecretKeyID != "test-key" || row.SecretVersion != 1 {
		t.Fatalf("mfa row metadata = %+v", row)
	}
	// A pending enrollment never satisfies a requirement.
	if enabled, err := service.IsEnabled(ctx, "user-1"); err != nil || enabled {
		t.Fatalf("pending enrollment reported enabled = %v, err = %v", enabled, err)
	}
	status, err := service.Status(ctx, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != storage.MFAStatusPending || status.RemainingRecoveryCodes != 10 || status.ExpiresAt == nil {
		t.Fatalf("status = %+v", status)
	}
	if !contains(auditActions(t, db), "auth.mfa.enroll") {
		t.Fatalf("audit actions = %v", auditActions(t, db))
	}
	if details := auditDetails(t, db); strings.Contains(details, enrollment.Secret) || strings.Contains(details, enrollment.RecoveryCodes[0]) {
		t.Fatalf("audit details leaked a secret: %s", details)
	}
}

func TestMFAEnrollRequiresCurrentPasswordForLocalAccounts(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "s3cret-password")
	service := newTestMFAService(t, db, testSecretProvider(t))

	if _, err := service.Enroll(ctx, "user-1", ""); !errors.Is(err, ErrCurrentPasswordRequired) {
		t.Fatalf("err = %v, want ErrCurrentPasswordRequired", err)
	}
	if _, err := service.Enroll(ctx, "user-1", "wrong-password"); !errors.Is(err, ErrCurrentPasswordInvalid) {
		t.Fatalf("err = %v, want ErrCurrentPasswordInvalid", err)
	}
	if _, err := db.UserMFA().Get(ctx, "user-1"); !errors.Is(err, context.DeadlineExceeded) && err == nil {
		t.Fatal("a rejected enrollment must not persist a row")
	}
	// An external-only account has no password to prove, so the check is skipped.
	seedExternalUser(t, db, "user-2", "bob", "user")
	if _, err := service.Enroll(ctx, "user-2", ""); err != nil {
		t.Fatalf("external enrollment failed: %v", err)
	}
}

func TestMFAEnrollFailsClosedWithoutSecretStorage(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedLocalUser(t, db, "user-1", "alice", "user", "s3cret-password")
	service := newTestMFAService(t, db, NewSecretProvider(nil))

	if _, err := service.Enroll(ctx, "user-1", "s3cret-password"); !errors.Is(err, ErrSecretStorageUnavailable) {
		t.Fatalf("err = %v, want ErrSecretStorageUnavailable", err)
	}
	if _, err := db.UserMFA().Get(ctx, "user-1"); err == nil {
		t.Fatal("nothing must be persisted when the secret store is unavailable")
	}
	if _, err := service.VerifyCode(ctx, "user-1", "123456"); !errors.Is(err, ErrMFANotEnrolled) {
		t.Fatalf("err = %v, want ErrMFANotEnrolled", err)
	}
}

func TestMFAEnrollTwiceReplacesPendingEnrollment(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedExternalUser(t, db, "user-1", "alice", "user")
	service := newTestMFAService(t, db, testSecretProvider(t))

	first, err := service.Enroll(ctx, "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Enroll(ctx, "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Secret == second.Secret {
		t.Fatal("re-enrollment reused the secret")
	}
	if first.RecoveryCodes[0] == second.RecoveryCodes[0] {
		t.Fatal("re-enrollment reused recovery codes")
	}
	// The first generation is gone: its code must not verify.
	if _, err := service.VerifyCode(ctx, "user-1", first.RecoveryCodes[0]); !errors.Is(err, ErrMFAPendingNotConfirmed) {
		t.Fatalf("err = %v, want ErrMFAPendingNotConfirmed", err)
	}
	row, err := db.UserMFA().Get(ctx, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if row.LastUsedStep != -1 {
		t.Fatalf("replay guard was not reset: %d", row.LastUsedStep)
	}
	unused, err := db.UserRecoveryCodes().ListUnused(ctx, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(unused) != 10 {
		t.Fatalf("unused codes = %d, want 10", len(unused))
	}
}

func TestMFAEnableConfirmsWithLiveCodeAndRejectsWrongCode(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedExternalUser(t, db, "user-1", "alice", "user")
	service := newTestMFAService(t, db, testSecretProvider(t))
	enrollment, err := service.Enroll(ctx, "user-1", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Enable(ctx, "user-1", "000000"); !errors.Is(err, ErrMFACodeInvalid) {
		t.Fatalf("err = %v, want ErrMFACodeInvalid", err)
	}
	if enabled, err := service.IsEnabled(ctx, "user-1"); err != nil || enabled {
		t.Fatalf("enabled after a wrong code = %v, err = %v", enabled, err)
	}
	if err := service.Enable(ctx, "user-1", currentCode(t, enrollment, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if enabled, err := service.IsEnabled(ctx, "user-1"); err != nil || !enabled {
		t.Fatalf("enabled = %v, err = %v, want true", enabled, err)
	}
	row, err := db.UserMFA().Get(ctx, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != storage.MFAStatusEnabled || row.EnabledAt == nil || row.LastUsedStep < 0 {
		t.Fatalf("row = %+v", row)
	}
	if err := service.Enable(ctx, "user-1", currentCode(t, enrollment, time.Now().UTC())); !errors.Is(err, ErrMFAAlreadyEnabled) {
		t.Fatalf("err = %v, want ErrMFAAlreadyEnabled", err)
	}
	if !contains(auditActions(t, db), "auth.mfa.enable") {
		t.Fatalf("audit actions = %v", auditActions(t, db))
	}
}

func TestMFAEnableRejectsUnknownAndExpiredEnrollment(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedExternalUser(t, db, "user-1", "alice", "user")
	service := newTestMFAService(t, db, testSecretProvider(t))

	if err := service.Enable(ctx, "user-1", "123456"); !errors.Is(err, ErrMFANotEnrolled) {
		t.Fatalf("err = %v, want ErrMFANotEnrolled", err)
	}
	enrollment, err := service.Enroll(ctx, "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	// Move the clock past the enrollment window: a stale QR code must not be
	// confirmable later.
	frozen := time.Now().UTC().Add(DefaultMFAConfig().EnrollmentTTL + time.Minute)
	service.SetClock(func() time.Time { return frozen })
	if err := service.Enable(ctx, "user-1", currentCode(t, enrollment, frozen)); !errors.Is(err, ErrMFANotEnrolled) {
		t.Fatalf("err = %v, want ErrMFANotEnrolled for an expired pending enrollment", err)
	}
	if enabled, err := service.IsEnabled(ctx, "user-1"); err != nil || enabled {
		t.Fatalf("expired pending enrollment reported enabled = %v", enabled)
	}
	status, err := service.Status(ctx, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "" {
		t.Fatalf("expired enrollment status = %+v, want empty", status)
	}
}

func TestMFAVerifyCodeTOTPReplayIsRejected(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedExternalUser(t, db, "user-1", "alice", "user")
	service := newTestMFAService(t, db, testSecretProvider(t))
	enrollment, err := service.Enroll(ctx, "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	code := currentCode(t, enrollment, now)
	if err := service.Enable(ctx, "user-1", code); err != nil {
		t.Fatal(err)
	}
	// Enable consumed the current step, so the same code must not verify again.
	if _, err := service.VerifyCode(ctx, "user-1", code); !errors.Is(err, ErrMFAReplayDetected) {
		t.Fatalf("err = %v, want ErrMFAReplayDetected", err)
	}
	next := now.Add(DefaultTOTPParams().Period)
	service.SetClock(func() time.Time { return next })
	result, err := service.VerifyCode(ctx, "user-1", currentCode(t, enrollment, next))
	if err != nil {
		t.Fatal(err)
	}
	if result.Method != "totp" || result.RecoveryCodesExhausted || result.RemainingRecoveryCodes != 10 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := service.VerifyCode(ctx, "user-1", currentCode(t, enrollment, next)); !errors.Is(err, ErrMFAReplayDetected) {
		t.Fatalf("err = %v, want ErrMFAReplayDetected", err)
	}
	if _, err := service.VerifyCode(ctx, "user-1", "999999"); !errors.Is(err, ErrMFACodeInvalid) {
		t.Fatalf("err = %v, want ErrMFACodeInvalid", err)
	}
}

func TestMFAVerifyCodeRecoveryCodesAreSingleUse(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedExternalUser(t, db, "user-1", "alice", "user")
	service := newTestMFAService(t, db, testSecretProvider(t))
	enrollment, err := service.Enroll(ctx, "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Enable(ctx, "user-1", currentCode(t, enrollment, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		result, err := service.VerifyCode(ctx, "user-1", enrollment.RecoveryCodes[i])
		if err != nil {
			t.Fatalf("code %d: %v", i, err)
		}
		if result.Method != "recovery" || result.RemainingRecoveryCodes != 9-i || result.RecoveryCodesExhausted {
			t.Fatalf("code %d result = %+v", i, result)
		}
		if _, err := service.VerifyCode(ctx, "user-1", enrollment.RecoveryCodes[i]); !errors.Is(err, ErrMFACodeInvalid) {
			t.Fatalf("code %d was accepted twice", i)
		}
	}
	last, err := service.VerifyCode(ctx, "user-1", enrollment.RecoveryCodes[9])
	if err != nil {
		t.Fatal(err)
	}
	if !last.RecoveryCodesExhausted || last.RemainingRecoveryCodes != 0 {
		t.Fatalf("last code result = %+v", last)
	}
	if _, err := service.VerifyCode(ctx, "user-1", "tmrc-AAAAAAAAAAAAAAAAAAAA"); !errors.Is(err, ErrMFACodeInvalid) {
		t.Fatalf("err = %v, want ErrMFACodeInvalid", err)
	}
	// Recovery verification is case and space tolerant.
	second, err := service.RegenerateRecoveryCodes(ctx, "user-1", currentCode(t, enrollment, time.Now().UTC().Add(DefaultTOTPParams().Period)))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 10 {
		t.Fatalf("regenerated %d codes", len(second))
	}
	folded := strings.ToLower(second[0][:5]) + " " + strings.ToLower(second[0][5:])
	result, err := service.VerifyCode(ctx, "user-1", folded)
	if err != nil {
		t.Fatalf("folded code rejected: %v", err)
	}
	if result.Method != "recovery" {
		t.Fatalf("method = %q", result.Method)
	}
}

func TestMFADisableRequiresProofAndHonoursPolicy(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedExternalUser(t, db, "user-1", "alice", "user")
	service := newTestMFAService(t, db, testSecretProvider(t))
	enrollment, err := service.Enroll(ctx, "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := service.Enable(ctx, "user-1", currentCode(t, enrollment, now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UserDevices().Create(ctx, storage.UserDevice{UserID: "user-1", TokenHash: "device-hash", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	required := AuthPolicy{Mode: storage.MFAModeRequired, Required: true}
	if err := service.Disable(ctx, "user-1", currentCode(t, enrollment, now.Add(30*time.Second)), required); !errors.Is(err, ErrMFARequiredByPolicy) {
		t.Fatalf("err = %v, want ErrMFARequiredByPolicy", err)
	}
	optional := AuthPolicy{Mode: storage.MFAModeOptional}
	if err := service.Disable(ctx, "user-1", "000000", optional); !errors.Is(err, ErrMFACodeInvalid) {
		t.Fatalf("err = %v, want ErrMFACodeInvalid", err)
	}
	if enabled, err := service.IsEnabled(ctx, "user-1"); err != nil || !enabled {
		t.Fatalf("MFA was removed by a failed disable")
	}
	service.SetClock(func() time.Time { return now.Add(time.Minute) })
	if err := service.Disable(ctx, "user-1", currentCode(t, enrollment, now.Add(time.Minute)), optional); err != nil {
		t.Fatal(err)
	}
	if enabled, err := service.IsEnabled(ctx, "user-1"); err != nil || enabled {
		t.Fatalf("enabled after disable = %v", enabled)
	}
	if _, err := db.UserMFA().Get(ctx, "user-1"); err == nil {
		t.Fatal("mfa row survived disable")
	}
	unused, err := db.UserRecoveryCodes().CountUnused(ctx, "user-1")
	if err != nil || unused != 0 {
		t.Fatalf("unused recovery codes = %d, err = %v", unused, err)
	}
	active, err := db.UserDevices().CountActive(ctx, "user-1")
	if err != nil || active != 0 {
		t.Fatalf("active devices = %d, err = %v, want 0 because trust was earned with the removed factor", active, err)
	}
	if !contains(auditActions(t, db), "auth.mfa.disable") {
		t.Fatalf("audit actions = %v", auditActions(t, db))
	}
	if err := service.Disable(ctx, "user-1", "123456", optional); !errors.Is(err, ErrMFANotEnrolled) {
		t.Fatalf("err = %v, want ErrMFANotEnrolled", err)
	}
}

func TestMFAAdminResetClearsFactorAndDevices(t *testing.T) {
	db := newIdentityStore(t)
	ctx := context.Background()
	seedExternalUser(t, db, "user-1", "alice", "user")
	seedLocalUser(t, db, "admin-1", "root", "admin", "admin-password")
	service := newTestMFAService(t, db, testSecretProvider(t))
	enrollment, err := service.Enroll(ctx, "user-1", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := service.Enable(ctx, "user-1", currentCode(t, enrollment, now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UserDevices().Create(ctx, storage.UserDevice{UserID: "user-1", TokenHash: "device-hash", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := service.AdminReset(ctx, "admin-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	if enabled, err := service.IsEnabled(ctx, "user-1"); err != nil || enabled {
		t.Fatalf("enabled after reset = %v", enabled)
	}
	if active, err := db.UserDevices().CountActive(ctx, "user-1"); err != nil || active != 0 {
		t.Fatalf("active devices = %d, err = %v", active, err)
	}
	// Resetting an account with no enrollment is a no-op, not an error, so an
	// administrator can run it blindly during incident recovery.
	if err := service.AdminReset(ctx, "admin-1", "user-1"); err != nil {
		t.Fatalf("second reset failed: %v", err)
	}
	if !contains(auditActions(t, db), "auth.mfa.reset") {
		t.Fatalf("audit actions = %v", auditActions(t, db))
	}
}

func TestResolvePolicyCombinesGlobalModeAndAccountOverride(t *testing.T) {
	base := storage.AuthSettings{MFAMode: storage.MFAModeDisabled, DeviceTrustEnabled: true, AllowTrustedDeviceBypass: true, MaxTrustedDevices: 5, SessionTokenTTLSeconds: 0}
	user := storage.User{ID: "u", Role: "user"}
	if policy := ResolvePolicy(base, user, false); policy.Required {
		t.Fatalf("disabled policy required MFA: %+v", policy)
	}
	if policy := ResolvePolicy(base, user, true); policy.Required {
		t.Fatal("disabled policy required MFA for an enrolled user")
	}
	if policy := ResolvePolicy(base, storage.User{ID: "u", MFARequired: true}, false); !policy.Required {
		t.Fatal("per-account override did not win over a disabled global policy")
	}
	optional := base
	optional.MFAMode = storage.MFAModeOptional
	if policy := ResolvePolicy(optional, user, false); policy.Required {
		t.Fatal("optional policy required MFA for an unenrolled user")
	}
	if policy := ResolvePolicy(optional, user, true); !policy.Required {
		t.Fatal("optional policy did not require MFA for an enrolled user")
	}
	required := base
	required.MFAMode = storage.MFAModeRequired
	if policy := ResolvePolicy(required, user, false); !policy.Required {
		t.Fatal("required policy did not require MFA")
	}
	// A bypass only makes sense while device trust itself is enabled.
	noDevices := required
	noDevices.DeviceTrustEnabled = false
	if policy := ResolvePolicy(noDevices, user, false); policy.AllowTrustedDeviceBypass {
		t.Fatal("bypass stayed enabled with device trust disabled")
	}
	if policy := ResolvePolicy(storage.AuthSettings{}, user, false); policy.Mode != storage.MFAModeDisabled {
		t.Fatalf("empty settings mode = %q, want disabled", policy.Mode)
	}
	withTTL := required
	withTTL.SessionTokenTTLSeconds = 3600
	if policy := ResolvePolicy(withTTL, user, false); policy.SessionTokenTTL != time.Hour {
		t.Fatalf("session ttl = %v", policy.SessionTokenTTL)
	}
}

func TestDefaultAuthSettingsAppliesSafeFallbacks(t *testing.T) {
	settings := DefaultAuthSettings("", true, true, 0, 0, -time.Hour)
	if settings.MFAMode != storage.MFAModeDisabled || !settings.DeviceTrustEnabled {
		t.Fatalf("settings = %+v", settings)
	}
	if settings.DeviceTrustTTLSeconds != int64((30*24*time.Hour)/time.Second) || settings.MaxTrustedDevices != 10 || settings.SessionTokenTTLSeconds != 0 {
		t.Fatalf("settings = %+v", settings)
	}
	custom := DefaultAuthSettings(storage.MFAModeRequired, true, true, time.Hour, 3, 12*time.Hour)
	if custom.MFAMode != storage.MFAModeRequired || custom.DeviceTrustTTLSeconds != 3600 || custom.MaxTrustedDevices != 3 || custom.SessionTokenTTLSeconds != 43200 {
		t.Fatalf("settings = %+v", custom)
	}
}

func TestMFAServiceDefaultsAreApplied(t *testing.T) {
	db := newIdentityStore(t)
	service := NewMFAService(db, testSecretProvider(t), MFAConfig{})
	if service.cfg.Issuer != "TunnelMesh" || service.cfg.RecoveryCodes != 10 || service.cfg.Params.Digits != 6 || service.cfg.EnrollmentTTL != 15*time.Minute {
		t.Fatalf("config = %+v", service.cfg)
	}
	nilSecrets := NewMFAService(db, nil, DefaultMFAConfig())
	if nilSecrets.secrets.Available() {
		t.Fatal("a nil secret provider must report unavailable")
	}
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
