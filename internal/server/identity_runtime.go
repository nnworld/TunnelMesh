package server

import (
	"net/http"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/auth/oidc"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// DefaultLoginRedirectPath is the console route the OIDC callback returns to. It
// is a fixed relative path, never derived from request data, which is what makes
// an open redirect through the callback impossible.
const DefaultLoginRedirectPath = "/login"

// IdentityRuntimeConfig carries everything needed to build the identity
// container from a loaded configuration. It exists so the wiring lives in one
// place instead of being repeated by the server runtime and the test harnesses,
// which is what keeps a test exercising the same construction path production
// uses.
type IdentityRuntimeConfig struct {
	// Auth is the normalized security.auth block.
	Auth config.AuthConfig
	// Secrets seals OIDC client secrets, TOTP shared secrets, login tickets, and
	// challenge payloads. When it reports unavailable, enrollment and provider
	// creation fail closed rather than storing plaintext.
	Secrets auth.SecretProvider
	// Metrics is optional; a nil value simply records nothing.
	Metrics AuthMetrics
	// AllowedRedirectBases bounds where an OIDC callback may point. It is derived
	// from security.allowed_origins so a provider cannot be registered against a
	// host this deployment does not serve.
	AllowedRedirectBases []string
	// HTTPClient overrides the relying-party transport. Tests point it at an
	// httptest identity provider; production leaves it nil so the client is
	// built with the configured timeout.
	HTTPClient *http.Client
	// Now overrides the clock for tests.
	Now func() time.Time
}

// NewIdentityServices builds the Phase A identity container. Every service shares
// one storage.DB and one SecretProvider, so the encryption key is resolved once
// and a missing key produces one consistent fail-closed behaviour everywhere.
//
// The returned container is safe to install with SetIdentityServices. It never
// returns nil: a deployment without an encryption key still gets working
// password login and MFA verification, and only the operations that would have
// to seal a secret report secret_storage_unavailable.
func NewIdentityServices(db *storage.DB, tokens auth.TokenIssuer, cfg IdentityRuntimeConfig) *IdentityServices {
	if db == nil {
		return nil
	}
	authConfig := config.NormalizeAuth(cfg.Auth)
	secrets := cfg.Secrets
	if secrets == nil {
		secrets = NewSecretProvider()
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	// The seed row is written the first time the policy is read. MFA mode stays
	// disabled so an upgraded deployment behaves exactly as it did before this
	// feature existed until an operator opts in.
	defaults := auth.DefaultAuthSettings(
		storage.MFAModeDisabled,
		authConfig.DeviceTrust.Enabled,
		authConfig.DeviceTrust.BypassMFA,
		30*24*time.Hour,
		10,
		authConfig.SessionTokenTTL,
	)

	mfa := auth.NewMFAService(db, secrets, auth.MFAConfig{
		Issuer: authConfig.MFA.Issuer,
		Params: auth.TOTPParams{
			Digits: authConfig.MFA.Digits,
			Period: authConfig.MFA.Period,
			Skew:   authConfig.MFA.Skew,
		},
		RecoveryCodes: authConfig.MFA.RecoveryCodes,
	})
	mfa.SetClock(now)

	devices := auth.NewDeviceService(db, auth.DeviceTrustConfig{
		Enabled: authConfig.DeviceTrust.Enabled,
		TTL:     time.Duration(defaults.DeviceTrustTTLSeconds) * time.Second,
	})
	devices.SetClock(now)

	challenges := auth.NewChallengeStore(db, secrets)

	logins := auth.NewLoginService(auth.LoginDependencies{
		Store:      db,
		Users:      db.Users(),
		Tokens:     tokens,
		Challenges: challenges,
		Attempts:   db.AuthLoginAttempts(),
		MFA:        mfa,
		Devices:    devices,
	}, auth.LoginConfig{
		MaxAttempts:          authConfig.LoginThrottle.MaxAttempts,
		Window:               authConfig.LoginThrottle.Window,
		Block:                authConfig.LoginThrottle.Block,
		ChallengeTTL:         authConfig.MFA.ChallengeTTL,
		ChallengeMaxAttempts: authConfig.MFA.MaxAttempts,
		Defaults:             defaults,
	})
	logins.SetClock(now)

	relyingParty := oidc.NewRelyingParty(cfg.HTTPClient, oidc.Config{
		HTTPTimeout:    authConfig.OIDC.HTTPTimeout,
		StateTTL:       authConfig.OIDC.StateTTL,
		LoginTicketTTL: authConfig.OIDC.LoginTicketTTL,
		JWKSCacheTTL:   authConfig.OIDC.JWKSCacheTTL,
		MaxBodyBytes:   authConfig.OIDC.MaxDiscoveryBodyByte,
	}, secrets)
	relyingParty.SetClock(now)

	return &IdentityServices{
		Logins:       logins,
		MFA:          mfa,
		Devices:      devices,
		Identities:   auth.NewIdentityService(db),
		Providers:    auth.NewOIDCProviderService(db, secrets, relyingParty, cfg.AllowedRedirectBases),
		Policy:       auth.NewAuthPolicyService(db, defaults),
		Challenges:   challenges,
		RelyingParty: relyingParty,
		OIDC: OIDCHTTPConfig{
			PublicProviders:   authConfig.OIDC.PublicProviders,
			StateTTL:          authConfig.OIDC.StateTTL,
			LoginTicketTTL:    authConfig.OIDC.LoginTicketTTL,
			LoginRedirectPath: DefaultLoginRedirectPath,
		},
		DeviceCookie: DeviceCookieConfig{
			Name:     authConfig.DeviceTrust.CookieName,
			Secure:   authConfig.DeviceTrust.CookieSecure,
			SameSite: authConfig.DeviceTrust.CookieSameSite,
			Path:     "/",
			TTL:      time.Duration(defaults.DeviceTrustTTLSeconds) * time.Second,
		},
		Metrics: cfg.Metrics,
	}
}

// AllowedRedirectBasesFromSecurity derives the OIDC callback allowlist from the
// host and origin allowlists the deployment already maintains. Reusing them
// means an operator cannot configure a provider whose callback points at a host
// this server would reject anyway.
func AllowedRedirectBasesFromSecurity(security config.SecurityConfig) []string {
	bases := make([]string, 0, len(security.AllowedOrigins)+len(security.AllowedHosts))
	bases = append(bases, security.AllowedOrigins...)
	for _, host := range security.AllowedHosts {
		normalized, ok := config.NormalizeAllowedHost(host)
		if !ok {
			continue
		}
		// Only an https base is accepted by the provider validation, so a bare
		// host is expanded rather than passed through and silently rejected.
		bases = append(bases, "https://"+normalized)
	}
	return bases
}
