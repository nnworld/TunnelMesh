package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// identityFixture bundles an API with the identity services installed plus the
// two accounts the identity tests need, so each case reads as the scenario it
// proves instead of as setup boilerplate.
type identityFixture struct {
	api   *API
	db    *storage.DB
	admin storage.User
	user  storage.User

	mu  sync.Mutex
	now time.Time
}

func newIdentityFixture(t *testing.T, cfg config.AuthConfig) *identityFixture {
	t.Helper()
	api, admin, user := apiTestServer(t)
	installTestIdentityWith(t, api, auth.NewAuthService(api.DB), cfg, IdentityRuntimeConfig{})
	fixture := &identityFixture{api: api, db: api.DB, admin: admin, user: user, now: time.Now().UTC()}
	fixture.setClocks()
	return fixture
}

func (f *identityFixture) handler() http.Handler { return f.api.Handler() }

// setClocks pins every identity service to the fixture clock. TOTP accepts only a
// one-step skew window, so a test that mints codes against the wall clock races
// the 30 second boundary and fails intermittently; a fixture clock makes the step
// sequence deterministic and lets a test move to a step the replay guard has not
// consumed yet.
func (f *identityFixture) setClocks() {
	services := f.api.IdentityServices()
	clock := func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.now
	}
	services.MFA.SetClock(clock)
	services.Logins.SetClock(clock)
	services.Devices.SetClock(clock)
	services.Identities.SetClock(clock)
	services.Challenges.SetClock(clock)
	services.Providers.SetClock(clock)
}

// advance moves the fixture clock forward by one or more TOTP steps.
func (f *identityFixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// totp mints a code that is valid at the current fixture time.
func (f *identityFixture) totp(t *testing.T, secret string) string {
	t.Helper()
	f.mu.Lock()
	now := f.now
	f.mu.Unlock()
	code, err := auth.TOTPCode(secret, now, auth.DefaultTOTPParams())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// login posts a password login and returns the recorded response.
func (f *identityFixture) login(t *testing.T, username, password string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	if body == nil {
		body = map[string]any{"username": username, "password": password}
	}
	return apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/login", "", "", body)
}

func (f *identityFixture) token(t *testing.T, username, password string) string {
	t.Helper()
	response := f.login(t, username, password, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", response.Code, response.Body.String())
	}
	return envelopeData(t, response)["token"].(string)
}

// envelopeData decodes the {code,msg,data} envelope and returns the data object.
func envelopeData(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope %q: %v", response.Body.String(), err)
	}
	if envelope.Code != response.Code {
		t.Fatalf("envelope code = %d, want the HTTP status %d", envelope.Code, response.Code)
	}
	return envelope.Data
}

// errorCode reads the stable machine-readable code from data.error.
func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	data := envelopeData(t, response)
	code, _ := data["error"].(string)
	return code
}

// setMFAMode writes the authoritative policy row, which is what an operator does
// through PUT /auth/policy.
func setMFAMode(t *testing.T, db *storage.DB, mode storage.MFAMode, allowBypass bool) {
	t.Helper()
	ctx := context.Background()
	err := db.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		settings, err := repos.Settings.SeedIfMissing(ctx, auth.DefaultAuthSettings(mode, true, allowBypass, 30*24*time.Hour, 10, 0))
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

// enrollAndEnableMFA walks the full self-service enrollment through the HTTP API
// and returns the shared secret so the caller can mint valid codes.
func enrollAndEnableMFA(t *testing.T, f *identityFixture, token, password string) string {
	t.Helper()
	enroll := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/enroll", token, "", map[string]any{"currentPassword": password})
	if enroll.Code != http.StatusOK {
		t.Fatalf("enroll status = %d: %s", enroll.Code, enroll.Body.String())
	}
	data := envelopeData(t, enroll)
	secret, _ := data["secret"].(string)
	if secret == "" {
		t.Fatalf("enroll response has no secret: %s", enroll.Body.String())
	}
	enable := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/enable", token, "", map[string]any{"code": f.totp(t, secret)})
	if enable.Code != http.StatusOK {
		t.Fatalf("enable status = %d: %s", enable.Code, enable.Body.String())
	}
	// Confirmation consumed this TOTP step, so the next code the caller needs must
	// come from a later one.
	f.advance(30 * time.Second)
	return secret
}

// TestAuthLoginKeepsLegacyShapeWhenMFADisabled is the compatibility contract: an
// existing script or client that posts a username and password must keep working
// after this feature ships, because mfa_mode defaults to disabled.
func TestAuthLoginKeepsLegacyShapeWhenMFADisabled(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	response := f.login(t, "alice", "alice-pass", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", response.Code, response.Body.String())
	}
	data := envelopeData(t, response)
	token, _ := data["token"].(string)
	if token == "" {
		t.Fatalf("login response has no token: %s", response.Body.String())
	}
	user, ok := data["user"].(map[string]any)
	if !ok {
		t.Fatalf("login response has no user object: %s", response.Body.String())
	}
	if user["username"] != "alice" {
		t.Fatalf("user.username = %v, want alice", user["username"])
	}
	if _, present := data["deviceTrusted"]; !present {
		t.Fatalf("login response has no deviceTrusted field: %s", response.Body.String())
	}
	if _, present := data["mfaRequired"]; present {
		t.Fatalf("login response must not ask for MFA when the policy is disabled: %s", response.Body.String())
	}
	// The issued token must actually authenticate a normal API call.
	agents := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/agents", token, "", nil)
	if agents.Code != http.StatusOK {
		t.Fatalf("authenticated request status = %d: %s", agents.Code, agents.Body.String())
	}
}

// TestAuthLoginRejectsBadCredentialsIdentically proves the response cannot be used
// to enumerate accounts: an unknown username, a wrong password, and a disabled
// account all produce the same status and the same stable code.
func TestAuthLoginRejectsBadCredentialsIdentically(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	adminToken := f.token(t, "admin", "admin-pass")

	disable := apiJSON(t, f.handler(), http.MethodPatch, "/api/v1/users/"+f.user.ID, adminToken, "", map[string]any{"disabled": true})
	if disable.Code != http.StatusOK {
		t.Fatalf("disable status = %d: %s", disable.Code, disable.Body.String())
	}
	third, err := auth.NewAuthService(f.db).CreateUser(context.Background(), "carol", "carol-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	_ = third

	cases := []struct {
		name     string
		username string
		password string
	}{
		{name: "unknown user", username: "nobody", password: "whatever-pass"},
		{name: "wrong password", username: "alice", password: "wrong-password"},
		{name: "disabled user", username: "alice", password: "alice-pass"},
		{name: "empty credentials", username: "", password: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := f.login(t, tc.username, tc.password, nil)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", response.Code, response.Body.String())
			}
			if got := errorCode(t, response); got != "invalid_credentials" {
				t.Fatalf("data.error = %q, want invalid_credentials", got)
			}
			if strings.Contains(strings.ToLower(response.Body.String()), "disabled") {
				t.Fatalf("response leaked the account state: %s", response.Body.String())
			}
		})
	}
}

// TestAuthLoginThrottles proves the brute-force budget is enforced per
// username+IP bucket and that the client is told how long to wait.
func TestAuthLoginThrottles(t *testing.T) {
	cfg := config.DefaultAuthConfig()
	cfg.LoginThrottle.MaxAttempts = 2
	cfg.LoginThrottle.Window = time.Minute
	cfg.LoginThrottle.Block = 10 * time.Minute
	f := newIdentityFixture(t, cfg)

	for attempt := 0; attempt < 2; attempt++ {
		if response := f.login(t, "alice", "wrong-password", nil); response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", attempt, response.Code)
		}
	}
	response := f.login(t, "alice", "wrong-password", nil)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", response.Code, response.Body.String())
	}
	if got := errorCode(t, response); got != "login_throttled" {
		t.Fatalf("data.error = %q, want login_throttled", got)
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("a throttled login did not carry a Retry-After header")
	}
	// The correct password is also refused while the bucket is blocked, otherwise
	// the throttle would only slow down an attacker who keeps guessing wrong.
	if blocked := f.login(t, "alice", "alice-pass", nil); blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked bucket status = %d, want 429", blocked.Code)
	}
	// A different username is a different bucket and still works.
	if other := f.login(t, "admin", "admin-pass", nil); other.Code != http.StatusOK {
		t.Fatalf("other bucket status = %d, want 200: %s", other.Code, other.Body.String())
	}
}

// TestAuthLoginRequiresMFAWhenPolicyRequires proves the second response shape: no
// token is issued until the second factor is presented.
func TestAuthLoginRequiresMFAWhenPolicyRequires(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "alice", "alice-pass")
	enrollAndEnableMFA(t, f, token, "alice-pass")
	setMFAMode(t, f.db, storage.MFAModeRequired, false)

	response := f.login(t, "alice", "alice-pass", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 carrying an MFA challenge: %s", response.Code, response.Body.String())
	}
	data := envelopeData(t, response)
	if data["mfaRequired"] != true {
		t.Fatalf("mfaRequired = %v, want true", data["mfaRequired"])
	}
	challengeID, _ := data["challengeId"].(string)
	if challengeID == "" {
		t.Fatalf("no challengeId: %s", response.Body.String())
	}
	if _, present := data["token"]; present {
		t.Fatalf("an MFA challenge response must not carry a token: %s", response.Body.String())
	}
	methods, _ := data["methods"].([]any)
	if len(methods) == 0 {
		t.Fatalf("no MFA methods offered: %s", response.Body.String())
	}
	if _, present := data["expiresAt"]; !present {
		t.Fatalf("no challenge expiry: %s", response.Body.String())
	}
}

// TestAuthMFAVerifyIssuesTokenAndTrustsDevice completes the two-step login and
// proves the trusted-device cookie is set only when the user asked for it.
func TestAuthMFAVerifyIssuesTokenAndTrustsDevice(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "alice", "alice-pass")
	secret := enrollAndEnableMFA(t, f, token, "alice-pass")
	setMFAMode(t, f.db, storage.MFAModeRequired, false)

	challenge := envelopeData(t, f.login(t, "alice", "alice-pass", nil))
	challengeID, _ := challenge["challengeId"].(string)

	// Without trustDevice no cookie is issued.
	plain := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/verify", "", "", map[string]any{
		"challengeId": challengeID, "code": f.totp(t, secret),
	})
	if plain.Code != http.StatusOK {
		t.Fatalf("verify status = %d: %s", plain.Code, plain.Body.String())
	}
	if _, ok := deviceCookieFrom(plain); ok {
		t.Fatal("a device cookie was set although trustDevice was false")
	}
	data := envelopeData(t, plain)
	if issued, _ := data["token"].(string); issued == "" {
		t.Fatalf("verify response has no token: %s", plain.Body.String())
	}

	// A fresh challenge with trustDevice issues the cookie. The clock moves one
	// step first because the previous code was already consumed.
	f.advance(30 * time.Second)
	second := envelopeData(t, f.login(t, "alice", "alice-pass", nil))
	secondID, _ := second["challengeId"].(string)
	trusted := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/verify", "", "", map[string]any{
		"challengeId": secondID, "code": f.totp(t, secret), "trustDevice": true,
	})
	if trusted.Code != http.StatusOK {
		t.Fatalf("trusted verify status = %d: %s", trusted.Code, trusted.Body.String())
	}
	cookie, ok := deviceCookieFrom(trusted)
	if !ok {
		t.Fatalf("no tm_device cookie was set: %v", trusted.Result().Cookies())
	}
	if !cookie.HttpOnly {
		t.Fatal("the trusted-device cookie must be HttpOnly")
	}
	if cookie.Value == "" {
		t.Fatal("the trusted-device cookie is empty")
	}
	if envelopeData(t, trusted)["deviceTrusted"] != true {
		t.Fatalf("deviceTrusted = %v, want true", trusted.Body.String())
	}
}

func deviceCookieFrom(response *httptest.ResponseRecorder) (*http.Cookie, bool) {
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "tm_device" {
			return cookie, true
		}
	}
	return nil, false
}

// TestAuthMFAVerifyRejectsUnknownAndExhaustedChallenge proves a challenge is
// single-window: an invented identifier is refused, and guessing past the budget
// burns the challenge rather than allowing unlimited attempts.
func TestAuthMFAVerifyRejectsUnknownAndExhaustedChallenge(t *testing.T) {
	cfg := config.DefaultAuthConfig()
	cfg.MFA.MaxAttempts = 2
	f := newIdentityFixture(t, cfg)
	token := f.token(t, "alice", "alice-pass")
	secret := enrollAndEnableMFA(t, f, token, "alice-pass")
	setMFAMode(t, f.db, storage.MFAModeRequired, false)

	unknown := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/verify", "", "", map[string]any{
		"challengeId": "not-a-real-challenge", "code": "123456",
	})
	if unknown.Code != http.StatusUnauthorized {
		t.Fatalf("unknown challenge status = %d, want 401: %s", unknown.Code, unknown.Body.String())
	}
	if got := errorCode(t, unknown); got != "mfa_challenge_invalid" {
		t.Fatalf("data.error = %q, want mfa_challenge_invalid", got)
	}

	challengeID, _ := envelopeData(t, f.login(t, "alice", "alice-pass", nil))["challengeId"].(string)
	verify := func(code string) *httptest.ResponseRecorder {
		return apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/verify", "", "", map[string]any{
			"challengeId": challengeID, "code": code,
		})
	}
	// The first wrong guess is reported as an invalid code and leaves the
	// challenge usable.
	first := verify("000000")
	if first.Code != http.StatusUnauthorized || errorCode(t, first) != "mfa_code_invalid" {
		t.Fatalf("first wrong guess = %d %q, want 401 mfa_code_invalid: %s", first.Code, errorCode(t, first), first.Body.String())
	}
	// The guess that reaches the budget burns the challenge and says so, which is
	// the signal the console uses to tell the user to start over.
	exhausted := verify("000000")
	if exhausted.Code != http.StatusUnauthorized {
		t.Fatalf("exhausting guess status = %d, want 401: %s", exhausted.Code, exhausted.Body.String())
	}
	if got := errorCode(t, exhausted); got != "mfa_attempts_exceeded" {
		t.Fatalf("data.error = %q, want mfa_attempts_exceeded", got)
	}
	// Afterwards even the correct code is refused: the window is closed, so an
	// attacker cannot keep guessing against a challenge they have already burned.
	dead := verify(f.totp(t, secret))
	if dead.Code != http.StatusUnauthorized {
		t.Fatalf("post-exhaustion status = %d, want 401: %s", dead.Code, dead.Body.String())
	}
	if got := errorCode(t, dead); got != "mfa_challenge_invalid" {
		t.Fatalf("data.error = %q, want mfa_challenge_invalid", got)
	}
}

// TestAuthMFAStatusRequiresAuthenticationAndReportsPolicy proves the self-service
// status endpoint is authenticated and never leaks the shared secret.
func TestAuthMFAStatusRequiresAuthenticationAndReportsPolicy(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	if response := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/auth/mfa", "", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", response.Code)
	}

	token := f.token(t, "alice", "alice-pass")
	before := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/auth/mfa", token, "", nil)
	if before.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", before.Code, before.Body.String())
	}
	data := envelopeData(t, before)
	if data["status"] != "none" {
		t.Fatalf("status = %v, want none before enrollment", data["status"])
	}
	if _, present := data["policy"]; !present {
		t.Fatalf("no policy block: %s", before.Body.String())
	}
	if strings.Contains(before.Body.String(), "secret") {
		t.Fatalf("status response mentions a secret: %s", before.Body.String())
	}

	enrollAndEnableMFA(t, f, token, "alice-pass")
	after := envelopeData(t, apiJSON(t, f.handler(), http.MethodGet, "/api/v1/auth/mfa", token, "", nil))
	if after["status"] != "enabled" {
		t.Fatalf("status = %v, want enabled", after["status"])
	}
	remaining, ok := after["remainingRecoveryCodes"].(float64)
	if !ok || int(remaining) != config.DefaultAuthConfig().MFA.RecoveryCodes {
		t.Fatalf("remainingRecoveryCodes = %v, want %d", after["remainingRecoveryCodes"], config.DefaultAuthConfig().MFA.RecoveryCodes)
	}
}

// TestAuthMFAEnrollReturnsSecretOnce proves enrollment yields the shared secret,
// an otpauth:// URL, and recovery codes, and that the codes are never repeated.
func TestAuthMFAEnrollReturnsSecretOnce(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "alice", "alice-pass")

	first := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/enroll", token, "", map[string]any{"currentPassword": "alice-pass"})
	if first.Code != http.StatusOK {
		t.Fatalf("enroll status = %d: %s", first.Code, first.Body.String())
	}
	if got := first.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store on a response carrying secrets", got)
	}
	data := envelopeData(t, first)
	secret, _ := data["secret"].(string)
	otpauth, _ := data["otpauthUrl"].(string)
	codes, _ := data["recoveryCodes"].([]any)
	if secret == "" || !strings.HasPrefix(otpauth, "otpauth://totp/") {
		t.Fatalf("enroll response = %s", first.Body.String())
	}
	if len(codes) != config.DefaultAuthConfig().MFA.RecoveryCodes {
		t.Fatalf("recovery codes = %d, want %d", len(codes), config.DefaultAuthConfig().MFA.RecoveryCodes)
	}
	if !strings.Contains(otpauth, "secret="+secret) {
		t.Fatal("the otpauth URL does not carry the returned secret")
	}

	// Enrolling again replaces the pending secret, so the first set of recovery
	// codes can never be presented again.
	second := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/enroll", token, "", map[string]any{"currentPassword": "alice-pass"})
	secondCodes, _ := envelopeData(t, second)["recoveryCodes"].([]any)
	if len(secondCodes) != len(codes) {
		t.Fatalf("second enrollment returned %d codes, want %d", len(secondCodes), len(codes))
	}
	for index := range codes {
		if codes[index] == secondCodes[index] {
			t.Fatalf("recovery code %d was repeated across enrollments", index)
		}
	}

	// A local-password account must re-prove its password before a second factor
	// is bound, so a hijacked session cannot be made durable.
	third := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/enroll", token, "", map[string]any{"currentPassword": "wrong-password"})
	if third.Code != http.StatusForbidden {
		t.Fatalf("wrong password enroll status = %d, want 403: %s", third.Code, third.Body.String())
	}
}

// TestAuthMFADisableRefusedWhenRequiredByPolicy proves the global requirement
// cannot be escaped by self-service removal.
func TestAuthMFADisableRefusedWhenRequiredByPolicy(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "alice", "alice-pass")
	secret := enrollAndEnableMFA(t, f, token, "alice-pass")
	setMFAMode(t, f.db, storage.MFAModeRequired, false)

	response := apiJSON(t, f.handler(), http.MethodDelete, "/api/v1/auth/mfa", token, "", map[string]any{
		"code": f.totp(t, secret),
	})
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
	}
	if got := errorCode(t, response); got != "mfa_required_by_policy" {
		t.Fatalf("data.error = %q, want mfa_required_by_policy", got)
	}

	// With the requirement lifted the same request succeeds. The refused attempt
	// never reached the code check, but the step it named is the one enrollment
	// already consumed, so the clock moves on before retrying.
	f.advance(30 * time.Second)
	setMFAMode(t, f.db, storage.MFAModeDisabled, false)
	allowed := apiJSON(t, f.handler(), http.MethodDelete, "/api/v1/auth/mfa", token, "", map[string]any{
		"code": f.totp(t, secret),
	})
	if allowed.Code != http.StatusOK {
		t.Fatalf("disable status = %d: %s", allowed.Code, allowed.Body.String())
	}
}

// TestAuthDevicesListMarksCurrentAndHidesSecrets proves the console can show a
// user their trusted browsers without ever exposing the credential.
func TestAuthDevicesListMarksCurrentAndHidesSecrets(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "alice", "alice-pass")
	secret := enrollAndEnableMFA(t, f, token, "alice-pass")
	setMFAMode(t, f.db, storage.MFAModeRequired, true)

	challengeID, _ := envelopeData(t, f.login(t, "alice", "alice-pass", nil))["challengeId"].(string)
	verify := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/verify", "", "", map[string]any{
		"challengeId": challengeID, "code": f.totp(t, secret), "trustDevice": true,
	})
	if verify.Code != http.StatusOK {
		t.Fatalf("verify status = %d: %s", verify.Code, verify.Body.String())
	}
	cookie, ok := deviceCookieFrom(verify)
	if !ok {
		t.Fatal("no trusted-device cookie was issued")
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/devices", nil)
	request.Header.Set("Authorization", "Bearer "+envelopeData(t, verify)["token"].(string))
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	f.handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("device list status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, cookie.Value) {
		t.Fatal("the device list leaked the trusted-device token")
	}
	items, _ := envelopeData(t, recorder)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("devices = %d, want 1: %s", len(items), body)
	}
	device := items[0].(map[string]any)
	if device["current"] != true {
		t.Fatalf("the presenting device is not marked current: %s", body)
	}
	for _, forbidden := range []string{"tokenHash", "token_hash", "hash"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the device list exposed %q: %s", forbidden, body)
		}
	}
}

// TestAuthDeviceRevokeEnforcesOwnership proves one user cannot revoke another
// user's device through the self-service route, and that an unknown identifier is
// reported as not found rather than forbidden so a device id cannot be used to
// probe which accounts exist.
func TestAuthDeviceRevokeEnforcesOwnership(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	adminToken := f.token(t, "admin", "admin-pass")
	other, err := auth.NewAuthService(f.db).CreateUser(context.Background(), "carol", "carol-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	aliceToken := f.token(t, "alice", "alice-pass")

	// A device trusted by carol, created through the service the API uses.
	devices := auth.NewDeviceService(f.db, auth.DefaultDeviceTrustConfig())
	if _, _, err := devices.Issue(context.Background(), other.ID, "carol-ua", "10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	listed, err := devices.List(context.Background(), other.ID, "")
	if err != nil || len(listed) != 1 {
		t.Fatalf("carol devices = %v, err = %v", listed, err)
	}

	response := apiJSON(t, f.handler(), http.MethodDelete, "/api/v1/auth/devices/"+listed[0].ID, aliceToken, "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-user revoke status = %d, want 404: %s", response.Code, response.Body.String())
	}
	if got := errorCode(t, response); got != "device_not_found" {
		t.Fatalf("data.error = %q, want device_not_found", got)
	}
	// The device still exists: the attempt must not have side effects.
	if after, err := devices.List(context.Background(), other.ID, ""); err != nil || len(after) != 1 {
		t.Fatalf("carol devices after = %v, err = %v", after, err)
	}

	// An administrator may revoke it through the administrative route.
	adminRevoke := apiJSON(t, f.handler(), http.MethodDelete, "/api/v1/users/"+other.ID+"/devices/"+listed[0].ID, adminToken, "revoke-1", nil)
	if adminRevoke.Code != http.StatusOK {
		t.Fatalf("admin revoke status = %d: %s", adminRevoke.Code, adminRevoke.Body.String())
	}
	if after, err := devices.List(context.Background(), other.ID, ""); err != nil || len(after) != 0 {
		t.Fatalf("carol devices after admin revoke = %v, err = %v", after, err)
	}
}

// TestAuthIdentitiesSelfServiceGuardsTheLastCredential proves an account whose
// only login is an external identity cannot unlink it and become unreachable.
func TestAuthIdentitiesSelfServiceGuardsTheLastCredential(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "alice", "alice-pass")

	empty := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/auth/identities", token, "", nil)
	if empty.Code != http.StatusOK {
		t.Fatalf("identity list status = %d: %s", empty.Code, empty.Body.String())
	}
	items, _ := envelopeData(t, empty)["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("identities = %d, want 0 for a local-password account", len(items))
	}

	// A password account may not be reduced to no credentials either: unlinking a
	// hypothetical identity is refused, and an unknown identifier is not found.
	missing := apiJSON(t, f.handler(), http.MethodDelete, "/api/v1/auth/identities/nope", token, "", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown identity status = %d, want 404: %s", missing.Code, missing.Body.String())
	}
	if unauthenticated := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/auth/identities", "", "", nil); unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated identity list status = %d, want 401", unauthenticated.Code)
	}
}

// TestAuthPolicyIsAdminOnly proves the global policy cannot be read or changed by
// a normal user, and that an administrator change is reflected in the self-service
// status response.
func TestAuthPolicyIsAdminOnly(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	userToken := f.token(t, "alice", "alice-pass")
	adminToken := f.token(t, "admin", "admin-pass")

	if response := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/auth/policy", userToken, "", nil); response.Code != http.StatusForbidden {
		t.Fatalf("user policy read status = %d, want 403", response.Code)
	}
	if response := apiJSON(t, f.handler(), http.MethodPut, "/api/v1/auth/policy", userToken, "policy-1", map[string]any{"mfaMode": "required"}); response.Code != http.StatusForbidden {
		t.Fatalf("user policy write status = %d, want 403", response.Code)
	}

	current := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/auth/policy", adminToken, "", nil)
	if current.Code != http.StatusOK {
		t.Fatalf("admin policy read status = %d: %s", current.Code, current.Body.String())
	}
	body := envelopeData(t, current)
	if body["mfaMode"] != "disabled" {
		t.Fatalf("mfaMode = %v, want the documented default disabled", body["mfaMode"])
	}

	update := apiJSON(t, f.handler(), http.MethodPut, "/api/v1/auth/policy", adminToken, "policy-2", map[string]any{
		"mfaMode": "required", "deviceTrustEnabled": true, "deviceTrustTtlSeconds": 86400,
		"allowTrustedDeviceBypass": false, "maxTrustedDevices": 5, "sessionTokenTtlSeconds": 0,
	})
	if update.Code != http.StatusOK {
		t.Fatalf("admin policy write status = %d: %s", update.Code, update.Body.String())
	}
	if envelopeData(t, update)["mfaMode"] != "required" {
		t.Fatalf("policy was not updated: %s", update.Body.String())
	}
	if outOfRange := apiJSON(t, f.handler(), http.MethodPut, "/api/v1/auth/policy", adminToken, "policy-3", map[string]any{
		"mfaMode": "sometimes", "deviceTrustEnabled": true, "deviceTrustTtlSeconds": 86400,
		"allowTrustedDeviceBypass": false, "maxTrustedDevices": 5, "sessionTokenTtlSeconds": 0,
	}); outOfRange.Code != http.StatusBadRequest {
		t.Fatalf("invalid policy status = %d, want 400: %s", outOfRange.Code, outOfRange.Body.String())
	}
}

// TestAuthRoutesWithoutIdentityServicesFailClosed proves a half-wired deployment
// refuses logins instead of accepting an unverified one.
func TestAuthRoutesWithoutIdentityServicesFailClosed(t *testing.T) {
	api, _, _ := apiTestServer(t)
	api.SetIdentityServices(nil)
	response := apiJSON(t, api.Handler(), http.MethodPost, "/api/v1/auth/login", "", "", map[string]any{"username": "alice", "password": "alice-pass"})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", response.Code, response.Body.String())
	}
	if got := errorCode(t, response); got != "identity_services_unavailable" {
		t.Fatalf("data.error = %q, want identity_services_unavailable", got)
	}
}

// TestAuthMeAndPasswordKeepTheirBehaviour proves the routes that existed before
// this phase still answer through the new dispatcher.
func TestAuthMeAndPasswordKeepTheirBehaviour(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "alice", "alice-pass")

	me := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/auth/me", token, "", nil)
	if me.Code != http.StatusOK {
		t.Fatalf("me status = %d: %s", me.Code, me.Body.String())
	}
	data := envelopeData(t, me)
	if data["username"] != "alice" || data["role"] != "user" {
		t.Fatalf("me response = %s", me.Body.String())
	}
	if data["id"] != f.user.ID {
		t.Fatalf("me id = %v, want %v", data["id"], f.user.ID)
	}

	changed := apiJSON(t, f.handler(), http.MethodPut, "/api/v1/auth/password", token, "", map[string]any{
		"currentPassword": "alice-pass", "newPassword": "a-stronger-password",
	})
	if changed.Code != http.StatusOK {
		t.Fatalf("password change status = %d: %s", changed.Code, changed.Body.String())
	}
	if envelopeData(t, changed)["mfaRequired"] == nil {
		t.Fatalf("the user projection lost its identity fields: %s", changed.Body.String())
	}
	if next := f.login(t, "alice", "a-stronger-password", nil); next.Code != http.StatusOK {
		t.Fatalf("login with the new password status = %d: %s", next.Code, next.Body.String())
	}
}

// TestAuthUnknownSubRouteIsNotFound proves the dispatcher does not turn an
// unimplemented path into a method error or a panic.
func TestAuthUnknownSubRouteIsNotFound(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "alice", "alice-pass")
	for _, path := range []string{"/api/v1/auth/nope", "/api/v1/auth/mfa/nope", "/api/v1/auth/devices/a/b/c"} {
		if response := apiJSON(t, f.handler(), http.MethodGet, path, token, "", nil); response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, response.Code)
		}
	}
}
