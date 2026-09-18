package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// loginFixture bundles everything a login test needs so each case reads as the
// scenario it is proving instead of as setup boilerplate.
type loginFixture struct {
	db         *storage.DB
	auth       *AuthService
	logins     *LoginService
	challenges *ChallengeStore
	mfa        *MFAService
	devices    *DeviceService
	secrets    SecretProvider
	cfg        LoginConfig
}

func testLoginConfig() LoginConfig {
	return LoginConfig{
		MaxAttempts:          3,
		Window:               5 * time.Minute,
		Block:                10 * time.Minute,
		ChallengeTTL:         5 * time.Minute,
		ChallengeMaxAttempts: 3,
		Defaults:             DefaultAuthSettings(storage.MFAModeDisabled, true, true, 30*24*time.Hour, 10, 0),
	}
}

func newLoginFixture(t *testing.T, cfg LoginConfig) *loginFixture {
	t.Helper()
	db := newIdentityStore(t)
	secrets := testSecretProvider(t)
	auth := NewAuthService(db)
	mfa := NewMFAService(db, secrets, DefaultMFAConfig())
	devices := NewDeviceService(db, DefaultDeviceTrustConfig())
	challenges := NewChallengeStore(db, secrets)
	logins := NewLoginService(LoginDependencies{
		Store:      db,
		Users:      db.Users(),
		Tokens:     auth,
		Challenges: challenges,
		Attempts:   db.AuthLoginAttempts(),
		MFA:        mfa,
		Devices:    devices,
	}, cfg)
	return &loginFixture{db: db, auth: auth, logins: logins, challenges: challenges, mfa: mfa, devices: devices, secrets: secrets, cfg: cfg}
}

// setMFAMode writes the authoritative policy row directly, which is what the
// operator does through PUT /auth/policy.
func (f *loginFixture) setMFAMode(t *testing.T, mode storage.MFAMode, allowBypass bool) {
	t.Helper()
	ctx := context.Background()
	err := f.db.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		settings, err := repos.Settings.SeedIfMissing(ctx, f.cfg.Defaults)
		if err != nil {
			return err
		}
		settings.MFAMode = mode
		settings.AllowTrustedDeviceBypass = allowBypass
		return repos.Settings.Update(ctx, settings)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// enrollAndEnableMFA drives the real enrollment path and returns the shared
// secret so a test can mint a live code.
func (f *loginFixture) enrollAndEnableMFA(t *testing.T, userID, password string) string {
	t.Helper()
	ctx := context.Background()
	enrollment, err := f.mfa.Enroll(ctx, userID, password)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	code, err := TOTPCode(enrollment.Secret, time.Now().UTC(), DefaultTOTPParams())
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	if err := f.mfa.Enable(ctx, userID, code); err != nil {
		t.Fatalf("enable: %v", err)
	}
	return enrollment.Secret
}

// liveCode mints a code one step ahead of now. Confirming an enrollment already
// consumes the current step through the replay guard, and VerifyTOTP accepts one
// step of skew, so the next step is both valid and unused.
func (f *loginFixture) liveCode(t *testing.T, secret string) string {
	t.Helper()
	params := DefaultTOTPParams()
	code, err := TOTPCode(secret, time.Now().UTC().Add(params.Period), params)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func (f *loginFixture) issueDevice(t *testing.T, userID string) string {
	t.Helper()
	token, _, err := f.devices.Issue(context.Background(), userID, "agent", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// TestLoginIssuesTokenWhenMFADisabled is the compatibility contract: an upgraded
// deployment must behave exactly as it did before Phase A.
func TestLoginIssuesTokenWhenMFADisabled(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	seedLocalUser(t, f.db, "u1", "alice", "admin", "correct horse battery")
	ctx := context.Background()

	outcome, err := f.logins.Login(ctx, LoginRequest{Username: "alice", Password: "correct horse battery", ClientIP: "10.0.0.9", UserAgent: "curl"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if outcome.State != LoginStateAuthenticated {
		t.Fatalf("state = %q", outcome.State)
	}
	if outcome.Token == "" {
		t.Fatal("expected a console token")
	}
	principal, err := f.auth.ValidateToken(ctx, outcome.Token)
	if err != nil {
		t.Fatalf("issued token does not validate: %v", err)
	}
	if principal.UserID != "u1" || principal.Role != "admin" {
		t.Fatalf("principal = %+v", principal)
	}
	if outcome.DeviceTrusted || outcome.DeviceToken != "" {
		t.Fatal("device was trusted without being requested")
	}
}

// TestLoginInvalidIsUniform proves an attacker cannot enumerate accounts from
// the response or from the audit trail.
func TestLoginInvalidIsUniform(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	disabled := seedLocalUser(t, f.db, "u2", "bob", "user", "pw-good")
	if err := f.db.Users().Update(context.Background(), storage.User{ID: disabled.ID, Username: disabled.Username, Role: disabled.Role, PasswordHash: disabled.PasswordHash, Disabled: true, AuthSource: disabled.AuthSource}); err != nil {
		t.Fatal(err)
	}
	deleted := seedLocalUser(t, f.db, "u3", "carol", "user", "pw-good")
	if err := f.db.Users().Delete(context.Background(), deleted.ID); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		username string
		password string
	}{
		{"unknown user", "nobody", "pw-good"},
		{"disabled user", "bob", "pw-good"},
		{"deleted user", "carol", "pw-good"},
		{"wrong password", "alice", "pw-bad"},
		{"empty password", "alice", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, err := f.logins.Login(context.Background(), LoginRequest{Username: tc.username, Password: tc.password, ClientIP: "10.9.9.9"})
			if outcome.State != LoginStateInvalid {
				t.Fatalf("state = %q", outcome.State)
			}
			if !errors.Is(err, ErrInvalidLogin) {
				t.Fatalf("err = %v, want ErrInvalidLogin", err)
			}
			if outcome.Token != "" || outcome.ChallengeID != "" {
				t.Fatal("invalid login leaked a credential")
			}
		})
	}
	for _, action := range auditActions(t, f.db) {
		if action != "auth.login.failure" {
			t.Fatalf("unexpected audit action %q", action)
		}
	}
}

// TestLoginThrottlesAfterMaxAttempts covers the cluster-shared brute-force
// budget, and that one success clears it.
func TestLoginThrottlesAfterMaxAttempts(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	ctx := context.Background()
	ip := "203.0.113.7"

	for i := 0; i < f.cfg.MaxAttempts; i++ {
		outcome, err := f.logins.Login(ctx, LoginRequest{Username: "alice", Password: "pw-bad", ClientIP: ip})
		if outcome.State != LoginStateInvalid || !errors.Is(err, ErrInvalidLogin) {
			t.Fatalf("attempt %d: state=%q err=%v", i, outcome.State, err)
		}
	}
	outcome, err := f.logins.Login(ctx, LoginRequest{Username: "alice", Password: "pw-good", ClientIP: ip})
	if outcome.State != LoginStateThrottled {
		t.Fatalf("state = %q, want throttled", outcome.State)
	}
	if !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("err = %v, want ErrLoginThrottled", err)
	}
	if outcome.RetryAfter <= 0 {
		t.Fatalf("retryAfter = %v", outcome.RetryAfter)
	}
	if outcome.Token != "" {
		t.Fatal("throttled login issued a token")
	}

	// A different client IP is a different bucket, so it is not blocked.
	other, err := f.logins.Login(ctx, LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "198.51.100.4"})
	if err != nil || other.State != LoginStateAuthenticated {
		t.Fatalf("other bucket: state=%q err=%v", other.State, err)
	}
}

// TestLoginSuccessResetsThrottleBucket keeps a legitimate user who mistyped once
// from inheriting an attacker's counter.
func TestLoginSuccessResetsThrottleBucket(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	ctx := context.Background()
	ip := "203.0.113.9"
	for i := 0; i < f.cfg.MaxAttempts-1; i++ {
		if _, err := f.logins.Login(ctx, LoginRequest{Username: "alice", Password: "pw-bad", ClientIP: ip}); !errors.Is(err, ErrInvalidLogin) {
			t.Fatalf("err = %v", err)
		}
	}
	if _, err := f.logins.Login(ctx, LoginRequest{Username: "alice", Password: "pw-good", ClientIP: ip}); err != nil {
		t.Fatalf("login: %v", err)
	}
	for i := 0; i < f.cfg.MaxAttempts-1; i++ {
		if _, err := f.logins.Login(ctx, LoginRequest{Username: "alice", Password: "pw-bad", ClientIP: ip}); !errors.Is(err, ErrInvalidLogin) {
			t.Fatalf("after reset attempt %d: err = %v", i, err)
		}
	}
}

// TestLoginPerUserMFAOverrideAppliesWhenGloballyDisabled is the reason
// users.mfa_required exists: protecting one account without a global rollout.
func TestLoginPerUserMFAOverrideAppliesWhenGloballyDisabled(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	user := seedLocalUser(t, f.db, "u1", "alice", "admin", "pw-good")
	f.setMFAMode(t, storage.MFAModeDisabled, true)
	f.enrollAndEnableMFA(t, user.ID, "pw-good")
	setMFARequired(t, f.db, user.ID, true)
	user, err := f.db.Users().Get(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !user.MFARequired {
		t.Fatal("per-account MFA override was not persisted")
	}

	outcome, err := f.logins.Login(context.Background(), LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if outcome.State != LoginStateMFARequired {
		t.Fatalf("state = %q", outcome.State)
	}
	if outcome.Token != "" {
		t.Fatal("token issued before the second factor")
	}
	if outcome.ChallengeID == "" || len(outcome.Methods) == 0 || outcome.ExpiresAt.IsZero() {
		t.Fatalf("incomplete challenge: %+v", outcome)
	}
}

// TestLoginRequiredModeWithoutEnrollmentStillChallenges and then fails
// verification, so the console can point the user at enrollment.
func TestLoginRequiredModeWithoutEnrollmentStillChallenges(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	f.setMFAMode(t, storage.MFAModeRequired, true)

	outcome, err := f.logins.Login(context.Background(), LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if outcome.State != LoginStateMFARequired {
		t.Fatalf("state = %q", outcome.State)
	}
	if strings.Join(outcome.Methods, ",") != "totp" {
		t.Fatalf("methods = %v, want [totp]", outcome.Methods)
	}
	_, err = f.logins.VerifyMFA(context.Background(), outcome.ChallengeID, "123456", "10.0.0.1", "curl", false)
	if !errors.Is(err, ErrMFANotEnrolled) {
		t.Fatalf("err = %v, want ErrMFANotEnrolled", err)
	}
}

// TestLoginOptionalModeOffersRecoveryCodes distinguishes an enrolled account,
// which may fall back to a recovery code, from one that has not finished setup.
func TestLoginOptionalModeOffersRecoveryCodes(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	user := seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	f.setMFAMode(t, storage.MFAModeOptional, true)
	f.enrollAndEnableMFA(t, user.ID, "pw-good")

	outcome, err := f.logins.Login(context.Background(), LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if outcome.State != LoginStateMFARequired {
		t.Fatalf("state = %q", outcome.State)
	}
	if strings.Join(outcome.Methods, ",") != "totp,recovery" {
		t.Fatalf("methods = %v, want [totp recovery]", outcome.Methods)
	}
}

// TestLoginOptionalModeSkipsMFAWhenNotEnrolled keeps optional from meaning
// mandatory for accounts that never enrolled.
func TestLoginOptionalModeSkipsMFAWhenNotEnrolled(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	f.setMFAMode(t, storage.MFAModeOptional, true)
	outcome, err := f.logins.Login(context.Background(), LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1"})
	if err != nil || outcome.State != LoginStateAuthenticated {
		t.Fatalf("state = %q err = %v", outcome.State, err)
	}
}

func TestLoginTrustedDeviceBypassesMFAWhenAllowed(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	user := seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	f.setMFAMode(t, storage.MFAModeRequired, true)
	f.enrollAndEnableMFA(t, user.ID, "pw-good")
	device := f.issueDevice(t, user.ID)

	outcome, err := f.logins.Login(context.Background(), LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1", DeviceToken: device})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if outcome.State != LoginStateAuthenticated || !outcome.DeviceTrusted {
		t.Fatalf("state = %q trusted = %v", outcome.State, outcome.DeviceTrusted)
	}
	if outcome.Token == "" {
		t.Fatal("expected a console token")
	}
}

func TestLoginTrustedDeviceIgnoredWhenBypassDisabled(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	user := seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	f.setMFAMode(t, storage.MFAModeRequired, false)
	f.enrollAndEnableMFA(t, user.ID, "pw-good")
	device := f.issueDevice(t, user.ID)

	outcome, err := f.logins.Login(context.Background(), LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1", DeviceToken: device})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if outcome.State != LoginStateMFARequired || outcome.DeviceTrusted {
		t.Fatalf("state = %q trusted = %v", outcome.State, outcome.DeviceTrusted)
	}
}

// TestLoginTrustedDeviceOfAnotherUserIsRejected stops one account's cookie from
// satisfying a second account's challenge.
func TestLoginTrustedDeviceOfAnotherUserIsRejected(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	alice := seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	seedLocalUser(t, f.db, "u2", "bob", "user", "pw-good")
	f.setMFAMode(t, storage.MFAModeRequired, true)
	f.enrollAndEnableMFA(t, alice.ID, "pw-good")
	aliceDevice := f.issueDevice(t, alice.ID)

	outcome, err := f.logins.Login(context.Background(), LoginRequest{Username: "bob", Password: "pw-good", ClientIP: "10.0.0.1", DeviceToken: aliceDevice})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if outcome.State != LoginStateMFARequired {
		t.Fatalf("state = %q, want mfa_required", outcome.State)
	}
}

func TestVerifyMFAIssuesTokenAndOptionallyTrustsDevice(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	user := seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	f.setMFAMode(t, storage.MFAModeRequired, true)
	secret := f.enrollAndEnableMFA(t, user.ID, "pw-good")

	challenge, err := f.logins.Login(context.Background(), LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	outcome, err := f.logins.VerifyMFA(context.Background(), challenge.ChallengeID, f.liveCode(t, secret), "10.0.0.1", "curl", true)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if outcome.State != LoginStateAuthenticated {
		t.Fatalf("state = %q", outcome.State)
	}
	if outcome.Token == "" || !outcome.DeviceTrusted || outcome.DeviceToken == "" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if principal, err := f.auth.ValidateToken(context.Background(), outcome.Token); err != nil || principal.UserID != user.ID {
		t.Fatalf("principal = %+v err = %v", principal, err)
	}
	// The same device token must validate on the next login.
	if _, valid, err := f.devices.Validate(context.Background(), outcome.DeviceToken); err != nil || !valid {
		t.Fatalf("device valid = %v err = %v", valid, err)
	}
	// The challenge is single use.
	if _, err := f.logins.VerifyMFA(context.Background(), challenge.ChallengeID, f.liveCode(t, secret), "10.0.0.1", "curl", false); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("replay err = %v, want ErrChallengeInvalid", err)
	}
}

func TestVerifyMFAAcceptsRecoveryCode(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	user := seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	f.setMFAMode(t, storage.MFAModeRequired, true)
	enrollment, err := f.mfa.Enroll(context.Background(), user.ID, "pw-good")
	if err != nil {
		t.Fatal(err)
	}
	code, err := TOTPCode(enrollment.Secret, time.Now().UTC(), DefaultTOTPParams())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.mfa.Enable(context.Background(), user.ID, code); err != nil {
		t.Fatal(err)
	}
	challenge, err := f.logins.Login(context.Background(), LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := f.logins.VerifyMFA(context.Background(), challenge.ChallengeID, enrollment.RecoveryCodes[0], "10.0.0.1", "curl", false)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if outcome.State != LoginStateAuthenticated || outcome.Method != "recovery" {
		t.Fatalf("state = %q method = %q", outcome.State, outcome.Method)
	}
}

// TestVerifyMFAWrongCodeCountsAttempts is the online guessing cap.
func TestVerifyMFAWrongCodeCountsAttempts(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	user := seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	f.setMFAMode(t, storage.MFAModeRequired, true)
	f.enrollAndEnableMFA(t, user.ID, "pw-good")
	challenge, err := f.logins.Login(context.Background(), LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < f.cfg.ChallengeMaxAttempts-1; i++ {
		if _, err := f.logins.VerifyMFA(ctx, challenge.ChallengeID, "000000", "10.0.0.1", "curl", false); !errors.Is(err, ErrMFACodeInvalid) {
			t.Fatalf("attempt %d err = %v, want ErrMFACodeInvalid", i, err)
		}
	}
	if _, err := f.logins.VerifyMFA(ctx, challenge.ChallengeID, "000000", "10.0.0.1", "curl", false); !errors.Is(err, ErrChallengeExhausted) {
		t.Fatalf("final err = %v, want ErrChallengeExhausted", err)
	}
	if _, err := f.logins.VerifyMFA(ctx, challenge.ChallengeID, "000000", "10.0.0.1", "curl", false); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("after exhaustion err = %v, want ErrChallengeInvalid", err)
	}
}

func TestVerifyMFAUnknownChallengeRejected(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	if _, err := f.logins.VerifyMFA(context.Background(), "ch_missing", "123456", "10.0.0.1", "curl", false); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("err = %v, want ErrChallengeInvalid", err)
	}
}

// TestVerifyMFAExpiredChallengeRejected closes the window a stolen challenge id
// would otherwise have for the whole TTL.
func TestVerifyMFAExpiredChallengeRejected(t *testing.T) {
	cfg := testLoginConfig()
	cfg.ChallengeTTL = 20 * time.Millisecond
	f := newLoginFixture(t, cfg)
	user := seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	f.setMFAMode(t, storage.MFAModeRequired, true)
	secret := f.enrollAndEnableMFA(t, user.ID, "pw-good")
	challenge, err := f.logins.Login(context.Background(), LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := f.logins.VerifyMFA(context.Background(), challenge.ChallengeID, f.liveCode(t, secret), "10.0.0.1", "curl", false); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("err = %v, want ErrChallengeInvalid", err)
	}
}

// TestLoginSessionTokenTTLHonoursSettings covers the difference between the
// historical non-expiring console token and an operator-imposed lifetime.
func TestLoginSessionTokenTTLHonoursSettings(t *testing.T) {
	ctx := context.Background()
	t.Run("zero keeps no expiry", func(t *testing.T) {
		f := newLoginFixture(t, testLoginConfig())
		seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
		outcome, err := f.logins.Login(ctx, LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		if got := tokenExpiry(t, f.auth, outcome.Token); got != nil {
			t.Fatalf("expires_at = %v, want nil", got)
		}
	})
	t.Run("positive sets expiry", func(t *testing.T) {
		cfg := testLoginConfig()
		cfg.Defaults.SessionTokenTTLSeconds = 3600
		f := newLoginFixture(t, cfg)
		seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
		outcome, err := f.logins.Login(ctx, LoginRequest{Username: "alice", Password: "pw-good", ClientIP: "10.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		expires := tokenExpiry(t, f.auth, outcome.Token)
		if expires == nil {
			t.Fatal("expires_at is nil")
		}
		if delta := time.Until(*expires); delta < 55*time.Minute || delta > 61*time.Minute {
			t.Fatalf("expires in %v", delta)
		}
	})
}

func tokenExpiry(t *testing.T, svc *AuthService, plain string) *time.Time {
	t.Helper()
	row, err := svc.tokens.GetByHash(context.Background(), hashToken(plain))
	if err != nil {
		t.Fatal(err)
	}
	return row.ExpiresAt
}

// TestLoginAuditContainsNoSecrets is the leak guard the plan requires: nothing
// that would let a log reader authenticate may reach the audit table.
func TestLoginAuditContainsNoSecrets(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	user := seedLocalUser(t, f.db, "u1", "alice", "user", "super-secret-password")
	f.setMFAMode(t, storage.MFAModeRequired, true)
	secret := f.enrollAndEnableMFA(t, user.ID, "super-secret-password")
	challenge, err := f.logins.Login(context.Background(), LoginRequest{Username: "alice", Password: "super-secret-password", ClientIP: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	code := f.liveCode(t, secret)
	outcome, err := f.logins.VerifyMFA(context.Background(), challenge.ChallengeID, code, "10.0.0.1", "curl", true)
	if err != nil {
		t.Fatal(err)
	}

	details := auditDetails(t, f.db)
	for name, secret := range map[string]string{
		"password":      "super-secret-password",
		"totp code":     code,
		"console token": outcome.Token,
		"device token":  outcome.DeviceToken,
		"totp secret":   secret,
		"challenge id":  challenge.ChallengeID,
	} {
		if secret == "" {
			t.Fatalf("%s was empty; the test is not exercising the flow", name)
		}
		if strings.Contains(details, secret) {
			t.Fatalf("audit details contain the %s", name)
		}
	}
	actions := auditActions(t, f.db)
	want := map[string]bool{"auth.login.mfa_required": false, "auth.login.success": false, "auth.device.trust": false}
	for _, action := range actions {
		if _, ok := want[action]; ok {
			want[action] = true
		}
	}
	for action, seen := range want {
		if !seen {
			t.Fatalf("missing audit action %q (got %v)", action, actions)
		}
	}
}

// TestIssueForAuthenticatedUserRequiresMFAWhenPolicySays so is the OIDC
// callback entry point: an IdP assertion is one factor, not two.
func TestIssueForAuthenticatedUserRequiresMFAWhenPolicySays(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	user := seedLocalUser(t, f.db, "u1", "alice", "user", "pw-good")
	f.setMFAMode(t, storage.MFAModeRequired, true)
	f.enrollAndEnableMFA(t, user.ID, "pw-good")
	ctx := context.Background()

	outcome, err := f.logins.IssueForAuthenticatedUser(ctx, user, "oidc", "10.0.0.1", "curl", false, "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if outcome.State != LoginStateMFARequired || outcome.ChallengeID == "" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if strings.Join(outcome.Methods, ",") != "totp,recovery" {
		t.Fatalf("methods = %v", outcome.Methods)
	}
}

func TestIssueForAuthenticatedUserIssuesTokenWhenMFANotRequired(t *testing.T) {
	f := newLoginFixture(t, testLoginConfig())
	user := seedExternalUser(t, f.db, "u9", "oidc-user", "user")
	outcome, err := f.logins.IssueForAuthenticatedUser(context.Background(), user, "oidc", "10.0.0.1", "curl", true, "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if outcome.State != LoginStateAuthenticated || outcome.Token == "" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if !outcome.DeviceTrusted || outcome.DeviceToken == "" {
		t.Fatalf("device was not trusted: %+v", outcome)
	}
}

// TestAuthServiceIssueTokenKeepsLegacyBehaviour guards the compatibility
// promise for every existing caller of the token repository.
func TestAuthServiceIssueTokenKeepsLegacyBehaviour(t *testing.T) {
	db := newIdentityStore(t)
	svc := NewAuthService(db)
	seedLocalUser(t, db, "u1", "alice", "user", "pw")
	ctx := context.Background()

	plain, err := svc.IssueToken(ctx, "u1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if principal, err := svc.ValidateToken(ctx, plain); err != nil || principal.UserID != "u1" {
		t.Fatalf("principal = %+v err = %v", principal, err)
	}
	if exp := tokenExpiry(t, svc, plain); exp != nil {
		t.Fatalf("ttl 0 set expires_at = %v", exp)
	}

	short, err := svc.IssueToken(ctx, "u1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	exp := tokenExpiry(t, svc, short)
	if exp == nil {
		t.Fatal("ttl 1h left expires_at nil")
	}
}
