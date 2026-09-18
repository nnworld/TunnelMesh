package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// Login states are the four outcomes the console branches on. They are strings
// rather than a boolean pair so the throttled case, which must not look like a
// credential failure to the client, stays distinct.
const (
	LoginStateAuthenticated = "authenticated"
	LoginStateMFARequired   = "mfa_required"
	LoginStateThrottled     = "throttled"
	LoginStateInvalid       = "invalid"
)

// TokenIssuer mints the console bearer token. *AuthService satisfies it, which
// keeps LoginService from owning the api_tokens table directly.
type TokenIssuer interface {
	IssueToken(ctx context.Context, userID string, ttl time.Duration) (string, error)
}

// LoginDependencies bundles every collaborator the login flow needs. Injecting
// them keeps the orchestration testable and makes the layering explicit: the
// service never touches SQL.
type LoginDependencies struct {
	Store      IdentityStore
	Users      storage.UserRepository
	Tokens     TokenIssuer
	Challenges *ChallengeStore
	Attempts   storage.AuthLoginAttemptRepository
	MFA        *MFAService
	Devices    *DeviceService
}

// LoginConfig carries the process-level knobs. Runtime policy (MFA mode, device
// trust, session TTL) comes from auth_settings instead, because an operator
// decision made through the console must survive a restart and a config edit.
type LoginConfig struct {
	// MaxAttempts, Window, and Block define the brute-force budget for one
	// username+IP bucket.
	MaxAttempts int
	Window      time.Duration
	Block       time.Duration
	// ChallengeTTL and ChallengeMaxAttempts bound one MFA guess window.
	ChallengeTTL         time.Duration
	ChallengeMaxAttempts int
	// Defaults seed auth_settings the first time it is read.
	Defaults storage.AuthSettings
}

// DefaultLoginConfig matches the documented defaults in the design spec.
func DefaultLoginConfig() LoginConfig {
	return LoginConfig{
		MaxAttempts:          10,
		Window:               5 * time.Minute,
		Block:                10 * time.Minute,
		ChallengeTTL:         5 * time.Minute,
		ChallengeMaxAttempts: 5,
		Defaults:             DefaultAuthSettings(storage.MFAModeDisabled, true, true, 30*24*time.Hour, 10, 0),
	}
}

func (c LoginConfig) withDefaults() LoginConfig {
	base := DefaultLoginConfig()
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = base.MaxAttempts
	}
	if c.Window <= 0 {
		c.Window = base.Window
	}
	if c.Block <= 0 {
		c.Block = base.Block
	}
	if c.ChallengeTTL <= 0 {
		c.ChallengeTTL = base.ChallengeTTL
	}
	if c.ChallengeMaxAttempts <= 0 {
		c.ChallengeMaxAttempts = base.ChallengeMaxAttempts
	}
	if c.Defaults.MFAMode == "" {
		c.Defaults = base.Defaults
	}
	return c
}

// LoginRequest is one password login attempt.
type LoginRequest struct {
	Username string
	Password string
	ClientIP string
	// UserAgent labels a newly trusted device in the console list.
	UserAgent string
	// TrustDevice asks for a long-lived device token on success.
	TrustDevice bool
	// DeviceToken is the presented trusted-device cookie, if any.
	DeviceToken string
}

// LoginOutcome is the single result type for every entry point. A caller
// switches on State and reads only the fields that State documents.
type LoginOutcome struct {
	State       string
	Token       string
	User        storage.User
	ChallengeID string
	Methods     []string
	ExpiresAt   time.Time
	// Method records which factor authenticated the session, for audit and
	// metrics.
	Method string
	// DeviceTrusted reports that this request is covered by a trusted device,
	// either a pre-existing one or one issued now.
	DeviceTrusted bool
	// DeviceToken is returned exactly once, when a device was trusted now.
	DeviceToken string
	// DeviceExpiresAt is the stored expiry of that device row. The caller sets
	// the cookie lifetime from it so the browser credential can never outlive
	// the row it refers to, even when the operator shortened the policy after
	// this process started.
	DeviceExpiresAt time.Time
	// RecoveryCodesExhausted lets the console prompt for a reset.
	RecoveryCodesExhausted bool
	RetryAfter             time.Duration
}

// LoginService orchestrates password login, the MFA step, and the OIDC
// hand-off. It replaces no existing behaviour: AuthService.Login stays as the
// compatibility path and this service is the new entry point.
type LoginService struct {
	deps LoginDependencies
	cfg  LoginConfig
	now  func() time.Time
}

// dummyHashOnce caches a real Argon2id hash so an unknown username costs the
// same CPU as a known one. Without it, response timing enumerates accounts.
var dummyHashOnce struct {
	sync.Once
	hash string
}

func dummyPasswordHash() string {
	dummyHashOnce.Do(func() {
		hash, err := hashPassword("tunnelmesh-timing-equalizer")
		if err != nil {
			// Falling back to an unparseable string is safe: verifyPassword
			// rejects it. Only the timing guarantee is lost.
			dummyHashOnce.hash = storage.PasswordHashNone
			return
		}
		dummyHashOnce.hash = hash
	})
	return dummyHashOnce.hash
}

// NewLoginService builds the service. Every dependency is required except
// Devices, which may be nil when device trust is compiled out of a deployment.
func NewLoginService(deps LoginDependencies, cfg LoginConfig) *LoginService {
	return &LoginService{deps: deps, cfg: cfg.withDefaults(), now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the time source for tests.
func (s *LoginService) SetClock(now func() time.Time) {
	if s != nil && now != nil {
		s.now = now
	}
}

// ErrInvalidLogin is the single failure value for a bad username, a disabled
// account, a deleted account, a passwordless SSO-only account, and a wrong
// password. Collapsing them is what stops account enumeration.
var ErrInvalidLogin = errors.New("invalid_credentials")

// Login performs one password authentication attempt.
func (s *LoginService) Login(ctx context.Context, req LoginRequest) (LoginOutcome, error) {
	if s == nil || s.deps.Users == nil || s.deps.Tokens == nil || s.deps.Challenges == nil || s.deps.Attempts == nil || s.deps.MFA == nil {
		return LoginOutcome{}, errors.New("login dependencies are required")
	}
	username := strings.TrimSpace(req.Username)
	bucket := loginBucketKey(username, req.ClientIP)

	// The throttle is checked before any credential work so a blocked bucket
	// costs one indexed read instead of one Argon2id hash.
	blocked, retryAfter, err := s.deps.Attempts.IsBlocked(ctx, bucket)
	if err != nil {
		return LoginOutcome{}, err
	}
	if blocked {
		if err := s.audit(ctx, "", "auth.login.throttled", "auth_login_attempt", bucket, map[string]any{"username": username, "ip": req.ClientIP}); err != nil {
			return LoginOutcome{}, err
		}
		return LoginOutcome{State: LoginStateThrottled, RetryAfter: retryAfter}, ErrLoginThrottled
	}

	user, err := s.deps.Users.GetByUsername(ctx, username)
	if err != nil {
		verifyPassword(req.Password, dummyPasswordHash())
		return s.failLogin(ctx, bucket, "", username, req.ClientIP)
	}
	// A passwordless account authenticates through its identity provider only;
	// accepting a password for it would create a second, weaker factor.
	if user.Disabled || user.DeletedAt != nil || user.PasswordHash == storage.PasswordHashNone || !verifyPassword(req.Password, user.PasswordHash) {
		return s.failLogin(ctx, bucket, user.ID, username, req.ClientIP)
	}
	// The password is dropped as early as possible; nothing below this line may
	// reference it, and it is never placed in a challenge payload.
	req.Password = ""
	if err := s.deps.Attempts.RegisterSuccess(ctx, bucket); err != nil {
		return LoginOutcome{}, err
	}
	return s.complete(ctx, user, "password", req.ClientIP, req.UserAgent, req.TrustDevice, req.DeviceToken)
}

// failLogin records the attempt and returns the uniform failure. The block the
// failure may have triggered is deliberately not surfaced here: the client sees
// the same 401 as every other wrong password, and only the next request is
// throttled.
func (s *LoginService) failLogin(ctx context.Context, bucket, userID, username, clientIP string) (LoginOutcome, error) {
	if _, _, err := s.deps.Attempts.RegisterFailure(ctx, bucket, s.cfg.Window, s.cfg.MaxAttempts, s.cfg.Block); err != nil {
		return LoginOutcome{}, err
	}
	if err := s.audit(ctx, userID, "auth.login.failure", "user", userID, map[string]any{"username": username, "ip": clientIP}); err != nil {
		return LoginOutcome{}, err
	}
	return LoginOutcome{State: LoginStateInvalid}, ErrInvalidLogin
}

// VerifyMFA completes a login_mfa challenge with a TOTP or recovery code.
func (s *LoginService) VerifyMFA(ctx context.Context, challengeID, code, clientIP, userAgent string, trustDevice bool) (LoginOutcome, error) {
	if s == nil || s.deps.Users == nil || s.deps.Tokens == nil || s.deps.Challenges == nil || s.deps.MFA == nil {
		return LoginOutcome{}, errors.New("login dependencies are required")
	}
	challenge, err := s.deps.Challenges.Load(ctx, strings.TrimSpace(challengeID), storage.ChallengeKindLoginMFA)
	if err != nil {
		return LoginOutcome{State: LoginStateInvalid}, err
	}
	user, err := s.deps.Users.Get(ctx, challenge.UserID)
	if err != nil || user.Disabled || user.DeletedAt != nil {
		return LoginOutcome{State: LoginStateInvalid}, ErrInvalidLogin
	}
	result, err := s.deps.MFA.VerifyCode(ctx, user.ID, code)
	if err != nil {
		// Only a wrong second factor burns the challenge budget. A missing
		// enrollment is a setup problem, and charging it would lock out a user
		// the administrator just forced into required mode.
		if errors.Is(err, ErrMFACodeInvalid) || errors.Is(err, ErrMFAReplayDetected) {
			if _, exhausted, ferr := s.deps.Challenges.Fail(ctx, challenge.ID); ferr != nil {
				return LoginOutcome{State: LoginStateInvalid}, ferr
			} else if exhausted {
				if aerr := s.audit(ctx, user.ID, "auth.login.failure", "user", user.ID, map[string]any{"method": "mfa", "ip": clientIP, "reason": "attempts_exceeded"}); aerr != nil {
					return LoginOutcome{State: LoginStateInvalid}, aerr
				}
				return LoginOutcome{State: LoginStateInvalid}, ErrChallengeExhausted
			}
			if aerr := s.audit(ctx, user.ID, "auth.login.failure", "user", user.ID, map[string]any{"method": "mfa", "ip": clientIP, "reason": "code_invalid"}); aerr != nil {
				return LoginOutcome{State: LoginStateInvalid}, aerr
			}
		}
		return LoginOutcome{State: LoginStateInvalid}, err
	}
	// Consuming after verification, and refusing to continue when the row was
	// already consumed, is what makes a challenge single-use under a race.
	consumed, err := s.deps.Challenges.Consume(ctx, challenge.ID)
	if err != nil {
		return LoginOutcome{}, err
	}
	if !consumed {
		return LoginOutcome{State: LoginStateInvalid}, ErrChallengeInvalid
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return LoginOutcome{}, err
	}
	policy := ResolvePolicy(settings, user, true)
	outcome, err := s.issue(ctx, user, result.Method, clientIP, userAgent, trustDevice, false, policy)
	if err != nil {
		return LoginOutcome{}, err
	}
	outcome.RecoveryCodesExhausted = result.RecoveryCodesExhausted
	return outcome, nil
}

// IssueForAuthenticatedUser is the OIDC callback entry point. A verified IdP
// assertion is one factor, so the account's policy is applied again here and
// may still demand a second one.
func (s *LoginService) IssueForAuthenticatedUser(ctx context.Context, user storage.User, method, clientIP, userAgent string, trustDevice bool, deviceToken string) (LoginOutcome, error) {
	if s == nil || s.deps.Users == nil || s.deps.Tokens == nil || s.deps.Challenges == nil || s.deps.MFA == nil {
		return LoginOutcome{}, errors.New("login dependencies are required")
	}
	if strings.TrimSpace(method) == "" {
		method = "oidc"
	}
	return s.complete(ctx, user, method, clientIP, userAgent, trustDevice, deviceToken)
}

// complete applies the effective policy to an already-authenticated identity.
func (s *LoginService) complete(ctx context.Context, user storage.User, method, clientIP, userAgent string, trustDevice bool, deviceToken string) (LoginOutcome, error) {
	settings, err := s.settings(ctx)
	if err != nil {
		return LoginOutcome{}, err
	}
	mfaEnabled, err := s.deps.MFA.IsEnabled(ctx, user.ID)
	if err != nil {
		return LoginOutcome{}, err
	}
	policy := ResolvePolicy(settings, user, mfaEnabled)
	if !policy.Required {
		return s.issue(ctx, user, method, clientIP, userAgent, trustDevice, false, policy)
	}
	// A trusted device is a remembered second factor. It is honoured only when
	// the operator left the bypass on, and only for the device's own account.
	if policy.AllowTrustedDeviceBypass && s.deps.Devices != nil && strings.TrimSpace(deviceToken) != "" {
		device, valid, err := s.deps.Devices.Validate(ctx, deviceToken)
		if err != nil {
			return LoginOutcome{}, err
		}
		if valid && device.UserID == user.ID {
			return s.issue(ctx, user, method, clientIP, userAgent, false, true, policy)
		}
	}
	return s.requireMFA(ctx, user, method, clientIP, mfaEnabled)
}

// requireMFA opens a login_mfa challenge and reports the methods the account can
// actually satisfy. An account that never finished enrollment is told "totp"
// only, so the console routes it to setup rather than to a dead end.
func (s *LoginService) requireMFA(ctx context.Context, user storage.User, method, clientIP string, mfaEnabled bool) (LoginOutcome, error) {
	methods := []string{"totp"}
	if mfaEnabled {
		methods = append(methods, "recovery")
	}
	id, err := s.deps.Challenges.Create(ctx, storage.ChallengeKindLoginMFA, user.ID, map[string]string{"method": method, "ip": clientIP}, s.cfg.ChallengeTTL, s.cfg.ChallengeMaxAttempts)
	if err != nil {
		return LoginOutcome{}, err
	}
	// The challenge identifier is never audited: it is a bearer secret for the
	// remainder of the login, and the audit table is widely readable.
	if err := s.audit(ctx, user.ID, "auth.login.mfa_required", "user", user.ID, map[string]any{"method": method, "ip": clientIP, "methods": strings.Join(methods, ",")}); err != nil {
		return LoginOutcome{}, err
	}
	return LoginOutcome{
		State:       LoginStateMFARequired,
		User:        user,
		ChallengeID: id,
		Methods:     methods,
		ExpiresAt:   s.now().Add(s.cfg.ChallengeTTL),
	}, nil
}

// issue creates the console token and, on request, a trusted device. Device
// issuance is best-effort: a deployment with trust disabled must still be able
// to log a user in.
func (s *LoginService) issue(ctx context.Context, user storage.User, method, clientIP, userAgent string, trustDevice, alreadyTrusted bool, policy AuthPolicy) (LoginOutcome, error) {
	token, err := s.deps.Tokens.IssueToken(ctx, user.ID, policy.SessionTokenTTL)
	if err != nil {
		return LoginOutcome{}, err
	}
	outcome := LoginOutcome{State: LoginStateAuthenticated, Token: token, User: user, Method: method, DeviceTrusted: alreadyTrusted}
	if trustDevice && s.deps.Devices != nil {
		deviceToken, device, err := s.deps.Devices.Issue(ctx, user.ID, userAgent, clientIP)
		switch {
		case errors.Is(err, ErrDeviceTrustDisabled):
			// Trust is off; the login itself is still valid.
		case err != nil:
			return LoginOutcome{}, err
		default:
			outcome.DeviceToken = deviceToken
			outcome.DeviceExpiresAt = device.ExpiresAt
			outcome.DeviceTrusted = true
		}
	}
	if err := s.audit(ctx, user.ID, "auth.login.success", "user", user.ID, map[string]any{"method": method, "ip": clientIP, "deviceTrusted": outcome.DeviceTrusted}); err != nil {
		return LoginOutcome{}, err
	}
	return outcome, nil
}

// settings reads the authoritative policy row, seeding it from configuration
// the first time. Seeding is a transaction so two nodes starting together
// cannot both insert.
func (s *LoginService) settings(ctx context.Context) (storage.AuthSettings, error) {
	if s.deps.Store == nil {
		return storage.AuthSettings{}, errors.New("identity store is required")
	}
	var settings storage.AuthSettings
	err := s.deps.Store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		seeded, err := repos.Settings.SeedIfMissing(ctx, s.cfg.Defaults)
		settings = seeded
		return err
	})
	return settings, err
}

// audit writes one structured row. Details are built from an explicit map so a
// secret can never be interpolated by accident, and the caller controls every
// key.
func (s *LoginService) audit(ctx context.Context, actorID, action, resourceType, resourceID string, details map[string]any) error {
	if s.deps.Store == nil {
		return errors.New("identity store is required")
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	return s.deps.Store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  actorID,
			Action:       action,
			ResourceType: resourceType,
			ResourceID:   resourceID,
			Details:      string(encoded),
		})
	})
}

// loginBucketKey hashes the username and client IP into the throttle bucket.
// Hashing means the table cannot be read as a list of who is attacking whom,
// and lowercasing stops case variants from multiplying the budget.
func loginBucketKey(username, clientIP string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(username) + "|" + strings.TrimSpace(clientIP)))
	return hex.EncodeToString(sum[:])
}

// PrepareOIDCLogin decides what an already-authenticated OIDC subject still owes
// before a session can be minted. The authorization callback cannot carry a
// bearer token in a redirect URL, so this returns the decision only; IssueSession
// completes it after the browser posts the single-use ticket back.
func (s *LoginService) PrepareOIDCLogin(ctx context.Context, user storage.User, clientIP, deviceToken string) (LoginOutcome, error) {
	if s == nil || s.deps.Users == nil || s.deps.Challenges == nil || s.deps.MFA == nil {
		return LoginOutcome{}, errors.New("login dependencies are required")
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return LoginOutcome{}, err
	}
	mfaEnabled, err := s.deps.MFA.IsEnabled(ctx, user.ID)
	if err != nil {
		return LoginOutcome{}, err
	}
	policy := ResolvePolicy(settings, user, mfaEnabled)
	if !policy.Required {
		return LoginOutcome{State: LoginStateAuthenticated, User: user, Method: "oidc"}, nil
	}
	if policy.AllowTrustedDeviceBypass && s.deps.Devices != nil && strings.TrimSpace(deviceToken) != "" {
		device, valid, err := s.deps.Devices.Validate(ctx, deviceToken)
		if err != nil {
			return LoginOutcome{}, err
		}
		if valid && device.UserID == user.ID {
			return LoginOutcome{State: LoginStateAuthenticated, User: user, Method: "oidc", DeviceTrusted: true}, nil
		}
	}
	return s.requireMFA(ctx, user, "oidc", clientIP, mfaEnabled)
}

// IssueSession mints a console session for a user who has already satisfied every
// required factor. It is the second half of the OIDC ticket exchange, and is
// deliberately separate from Login so a ticket cannot be replayed into a password
// check.
func (s *LoginService) IssueSession(ctx context.Context, user storage.User, method, clientIP, userAgent string, trustDevice, alreadyTrusted bool) (LoginOutcome, error) {
	if s == nil || s.deps.Tokens == nil {
		return LoginOutcome{}, errors.New("login dependencies are required")
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return LoginOutcome{}, err
	}
	mfaEnabled := alreadyTrusted
	if !mfaEnabled && s.deps.MFA != nil {
		if mfaEnabled, err = s.deps.MFA.IsEnabled(ctx, user.ID); err != nil {
			return LoginOutcome{}, err
		}
	}
	return s.issue(ctx, user, method, clientIP, userAgent, trustDevice, alreadyTrusted, ResolvePolicy(settings, user, mfaEnabled))
}
