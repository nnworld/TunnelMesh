package config

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// AuthConfig configures the management-console identity layer: password login
// throttling, TOTP multi-factor authentication, trusted devices, and OIDC
// single sign-on.
//
// These values are process-level defaults and knobs only. The runtime policy an
// operator edits through the console lives in the auth_settings table, which is
// seeded once from these defaults and is authoritative afterwards. Editing this
// block never silently overrides a decision made in the UI.
type AuthConfig struct {
	// SessionTokenTTL bounds a console bearer token. Zero keeps the historical
	// non-expiring token so an upgraded deployment behaves exactly as before.
	SessionTokenTTL time.Duration     `mapstructure:"session_token_ttl" json:"session_token_ttl" yaml:"session_token_ttl"`
	LoginThrottle   LoginThrottleConf `mapstructure:"login_throttle" json:"login_throttle" yaml:"login_throttle"`
	MFA             MFAConf           `mapstructure:"mfa" json:"mfa" yaml:"mfa"`
	DeviceTrust     DeviceTrustConf   `mapstructure:"device_trust" json:"device_trust" yaml:"device_trust"`
	OIDC            OIDCConf          `mapstructure:"oidc" json:"oidc" yaml:"oidc"`
}

// LoginThrottleConf bounds brute-force guessing per username+IP bucket.
type LoginThrottleConf struct {
	MaxAttempts int           `mapstructure:"max_attempts" json:"max_attempts" yaml:"max_attempts"`
	Window      time.Duration `mapstructure:"window" json:"window" yaml:"window"`
	Block       time.Duration `mapstructure:"block" json:"block" yaml:"block"`
}

// MFAConf describes the TOTP parameters an authenticator app is enrolled with.
// They are fixed at enrollment time, so changing them later does not retro-fit
// already-enrolled accounts; it only changes new enrollments.
type MFAConf struct {
	// Issuer is the label shown by authenticator apps. It is embedded in an
	// otpauth:// URL, so it must not contain a colon.
	Issuer        string        `mapstructure:"issuer" json:"issuer" yaml:"issuer"`
	Digits        int           `mapstructure:"digits" json:"digits" yaml:"digits"`
	Period        time.Duration `mapstructure:"period" json:"period" yaml:"period"`
	Skew          int           `mapstructure:"skew" json:"skew" yaml:"skew"`
	ChallengeTTL  time.Duration `mapstructure:"challenge_ttl" json:"challenge_ttl" yaml:"challenge_ttl"`
	MaxAttempts   int           `mapstructure:"max_attempts" json:"max_attempts" yaml:"max_attempts"`
	RecoveryCodes int           `mapstructure:"recovery_codes" json:"recovery_codes" yaml:"recovery_codes"`
}

// DeviceTrustConf describes the long-lived "remember this browser" credential.
type DeviceTrustConf struct {
	Enabled        bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	CookieName     string `mapstructure:"cookie_name" json:"cookie_name" yaml:"cookie_name"`
	CookieSecure   bool   `mapstructure:"cookie_secure" json:"cookie_secure" yaml:"cookie_secure"`
	CookieSameSite string `mapstructure:"cookie_same_site" json:"cookie_same_site" yaml:"cookie_same_site"`
	// BypassMFA lets a still-valid trusted device skip the second factor. An
	// operator who wants MFA on every login turns it off without disabling
	// device tracking.
	BypassMFA bool `mapstructure:"bypass_mfa" json:"bypass_mfa" yaml:"bypass_mfa"`
}

// OIDCConf carries the process-level relying-party knobs. Per-provider issuer,
// client ID, and secret live in the database, not here.
type OIDCConf struct {
	// PublicProviders gates the unauthenticated provider listing that renders
	// the SSO buttons on the login page.
	PublicProviders      bool          `mapstructure:"public_providers" json:"public_providers" yaml:"public_providers"`
	HTTPTimeout          time.Duration `mapstructure:"http_timeout" json:"http_timeout" yaml:"http_timeout"`
	StateTTL             time.Duration `mapstructure:"state_ttl" json:"state_ttl" yaml:"state_ttl"`
	LoginTicketTTL       time.Duration `mapstructure:"login_ticket_ttl" json:"login_ticket_ttl" yaml:"login_ticket_ttl"`
	JWKSCacheTTL         time.Duration `mapstructure:"jwks_cache_ttl" json:"jwks_cache_ttl" yaml:"jwks_cache_ttl"`
	MaxDiscoveryBodyByte int64         `mapstructure:"max_discovery_body_bytes" json:"max_discovery_body_bytes" yaml:"max_discovery_body_bytes"`
}

// DefaultAuthConfig matches the documented defaults in the design spec.
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{
		SessionTokenTTL: 0,
		LoginThrottle:   LoginThrottleConf{MaxAttempts: 10, Window: 5 * time.Minute, Block: 10 * time.Minute},
		MFA: MFAConf{
			Issuer: "TunnelMesh", Digits: 6, Period: 30 * time.Second, Skew: 1,
			ChallengeTTL: 5 * time.Minute, MaxAttempts: 5, RecoveryCodes: 10,
		},
		DeviceTrust: DeviceTrustConf{Enabled: true, CookieName: "tm_device", CookieSecure: true, CookieSameSite: "lax", BypassMFA: true},
		OIDC: OIDCConf{
			PublicProviders: true, HTTPTimeout: 10 * time.Second, StateTTL: 10 * time.Minute,
			LoginTicketTTL: 60 * time.Second, JWKSCacheTTL: time.Hour, MaxDiscoveryBodyByte: 1 << 20,
		},
	}
}

// authDefaults flattens DefaultAuthConfig into the viper key map so the
// CLI > env > file > default precedence keeps working for every leaf.
func authDefaults() map[string]any {
	d := DefaultAuthConfig()
	return map[string]any{
		"security.auth.session_token_ttl":             d.SessionTokenTTL,
		"security.auth.login_throttle.max_attempts":   d.LoginThrottle.MaxAttempts,
		"security.auth.login_throttle.window":         d.LoginThrottle.Window,
		"security.auth.login_throttle.block":          d.LoginThrottle.Block,
		"security.auth.mfa.issuer":                    d.MFA.Issuer,
		"security.auth.mfa.digits":                    d.MFA.Digits,
		"security.auth.mfa.period":                    d.MFA.Period,
		"security.auth.mfa.skew":                      d.MFA.Skew,
		"security.auth.mfa.challenge_ttl":             d.MFA.ChallengeTTL,
		"security.auth.mfa.max_attempts":              d.MFA.MaxAttempts,
		"security.auth.mfa.recovery_codes":            d.MFA.RecoveryCodes,
		"security.auth.device_trust.enabled":          d.DeviceTrust.Enabled,
		"security.auth.device_trust.cookie_name":      d.DeviceTrust.CookieName,
		"security.auth.device_trust.cookie_secure":    d.DeviceTrust.CookieSecure,
		"security.auth.device_trust.cookie_same_site": d.DeviceTrust.CookieSameSite,
		"security.auth.device_trust.bypass_mfa":       d.DeviceTrust.BypassMFA,
		"security.auth.oidc.public_providers":         d.OIDC.PublicProviders,
		"security.auth.oidc.http_timeout":             d.OIDC.HTTPTimeout,
		"security.auth.oidc.state_ttl":                d.OIDC.StateTTL,
		"security.auth.oidc.login_ticket_ttl":         d.OIDC.LoginTicketTTL,
		"security.auth.oidc.jwks_cache_ttl":           d.OIDC.JWKSCacheTTL,
		"security.auth.oidc.max_discovery_body_bytes": d.OIDC.MaxDiscoveryBodyByte,
		"server.trusted_proxies":                      []string{},
	}
}

// authEnvKeys lists every leaf bound to a TUNNELMESH_* environment variable.
func authEnvKeys() []string {
	keys := make([]string, 0, len(authDefaults()))
	for key := range authDefaults() {
		keys = append(keys, key)
	}
	return keys
}

// NormalizeAuth fills every unset leaf with its documented default. A
// zero-valued AuthConfig therefore means "not configured" rather than
// "configured with zeros", which matches how the rest of the codebase treats an
// absent block and lets a hand-built Config validate the same way a loaded one
// does. Values that are present but out of range are left untouched so
// validateAuth can report them.
func NormalizeAuth(cfg AuthConfig) AuthConfig {
	d := DefaultAuthConfig()
	if cfg.SessionTokenTTL < 0 {
		cfg.SessionTokenTTL = d.SessionTokenTTL
	}
	if cfg.LoginThrottle.MaxAttempts <= 0 {
		cfg.LoginThrottle.MaxAttempts = d.LoginThrottle.MaxAttempts
	}
	if cfg.LoginThrottle.Window <= 0 {
		cfg.LoginThrottle.Window = d.LoginThrottle.Window
	}
	if cfg.LoginThrottle.Block <= 0 {
		cfg.LoginThrottle.Block = d.LoginThrottle.Block
	}
	if strings.TrimSpace(cfg.MFA.Issuer) == "" {
		cfg.MFA.Issuer = d.MFA.Issuer
	}
	if cfg.MFA.Digits <= 0 {
		cfg.MFA.Digits = d.MFA.Digits
	}
	if cfg.MFA.Period <= 0 {
		cfg.MFA.Period = d.MFA.Period
	}
	if cfg.MFA.Skew < 0 {
		cfg.MFA.Skew = d.MFA.Skew
	}
	// Skew 0 is a legitimate operator choice meaning "accept only the current
	// step", so it is deliberately not treated as unset. A zero-valued block
	// therefore keeps skew 0 while every other leaf takes its default; the
	// viper-loaded path always supplies the documented default of 1.
	if cfg.MFA.ChallengeTTL <= 0 {
		cfg.MFA.ChallengeTTL = d.MFA.ChallengeTTL
	}
	if cfg.MFA.MaxAttempts <= 0 {
		cfg.MFA.MaxAttempts = d.MFA.MaxAttempts
	}
	if cfg.MFA.RecoveryCodes <= 0 {
		cfg.MFA.RecoveryCodes = d.MFA.RecoveryCodes
	}
	if strings.TrimSpace(cfg.DeviceTrust.CookieName) == "" {
		cfg.DeviceTrust.CookieName = d.DeviceTrust.CookieName
	}
	if strings.TrimSpace(cfg.DeviceTrust.CookieSameSite) == "" {
		cfg.DeviceTrust.CookieSameSite = d.DeviceTrust.CookieSameSite
	}
	if cfg.OIDC.HTTPTimeout <= 0 {
		cfg.OIDC.HTTPTimeout = d.OIDC.HTTPTimeout
	}
	if cfg.OIDC.StateTTL <= 0 {
		cfg.OIDC.StateTTL = d.OIDC.StateTTL
	}
	if cfg.OIDC.LoginTicketTTL <= 0 {
		cfg.OIDC.LoginTicketTTL = d.OIDC.LoginTicketTTL
	}
	if cfg.OIDC.JWKSCacheTTL <= 0 {
		cfg.OIDC.JWKSCacheTTL = d.OIDC.JWKSCacheTTL
	}
	if cfg.OIDC.MaxDiscoveryBodyByte <= 0 {
		cfg.OIDC.MaxDiscoveryBodyByte = d.OIDC.MaxDiscoveryBodyByte
	}
	return cfg
}

// validateAuth rejects a configuration that would weaken the identity layer.
// Every bound is checked here rather than at use time so a typo fails the
// process start instead of silently downgrading MFA. Unset leaves are normalized
// to their defaults first, so only values an operator actually wrote are judged.
func validateAuth(cfg AuthConfig) []string {
	var problems []string
	if cfg.SessionTokenTTL < 0 {
		problems = append(problems, "security auth session token TTL must not be negative")
	}
	cfg = NormalizeAuth(cfg)
	problems = append(problems, validateLoginThrottle(cfg.LoginThrottle)...)
	problems = append(problems, validateMFAConf(cfg.MFA)...)
	problems = append(problems, validateDeviceTrust(cfg.DeviceTrust)...)
	problems = append(problems, validateOIDCConf(cfg.OIDC)...)
	return problems
}

func validateLoginThrottle(cfg LoginThrottleConf) []string {
	var problems []string
	if cfg.MaxAttempts < 1 || cfg.MaxAttempts > 1000 {
		problems = append(problems, "security auth login throttle max attempts must be between 1 and 1000")
	}
	if cfg.Window < time.Second || cfg.Window > 24*time.Hour {
		problems = append(problems, "security auth login throttle window must be between 1s and 24h")
	}
	if cfg.Block < time.Second || cfg.Block > 24*time.Hour {
		problems = append(problems, "security auth login throttle block must be between 1s and 24h")
	}
	return problems
}

func validateMFAConf(cfg MFAConf) []string {
	var problems []string
	issuer := strings.TrimSpace(cfg.Issuer)
	if len(issuer) > 64 {
		problems = append(problems, "security auth mfa issuer must be at most 64 characters")
	}
	if strings.Contains(issuer, ":") {
		problems = append(problems, `security auth mfa issuer must not contain ":" because it is embedded in an otpauth:// label`)
	}
	if cfg.Digits != 6 && cfg.Digits != 8 {
		problems = append(problems, "security auth mfa digits must be 6 or 8")
	}
	if cfg.Period < 15*time.Second || cfg.Period > 120*time.Second {
		problems = append(problems, "security auth mfa period must be between 15s and 120s")
	}
	if cfg.Skew < 0 || cfg.Skew > 2 {
		problems = append(problems, "security auth mfa skew must be between 0 and 2")
	}
	if cfg.ChallengeTTL < 30*time.Second || cfg.ChallengeTTL > 30*time.Minute {
		problems = append(problems, "security auth mfa challenge TTL must be between 30s and 30m")
	}
	if cfg.MaxAttempts < 1 || cfg.MaxAttempts > 20 {
		problems = append(problems, "security auth mfa max attempts must be between 1 and 20")
	}
	if cfg.RecoveryCodes < 1 || cfg.RecoveryCodes > 50 {
		problems = append(problems, "security auth mfa recovery codes must be between 1 and 50")
	}
	return problems
}

func validateDeviceTrust(cfg DeviceTrustConf) []string {
	var problems []string
	name := strings.TrimSpace(cfg.CookieName)
	if len(name) > 64 {
		problems = append(problems, "security auth device trust cookie name must be at most 64 characters")
	}
	if name != "" && strings.ContainsAny(name, " \t\r\n;,\"\\") {
		problems = append(problems, "security auth device trust cookie name must be a valid cookie token")
	}
	switch strings.ToLower(cfg.CookieSameSite) {
	case "lax", "strict", "none":
	default:
		problems = append(problems, "security auth device trust cookie same site must be lax, strict, or none")
	}
	// SameSite=None without Secure is rejected by every modern browser and, more
	// importantly, would put a long-lived credential on the wire in plaintext.
	if strings.EqualFold(cfg.CookieSameSite, "none") && !cfg.CookieSecure {
		problems = append(problems, "security auth device trust cookie same site none requires cookie secure")
	}
	return problems
}

func validateOIDCConf(cfg OIDCConf) []string {
	var problems []string
	if cfg.HTTPTimeout > 5*time.Minute {
		problems = append(problems, "security auth oidc http timeout must be at most 5m")
	}
	if cfg.StateTTL > time.Hour {
		problems = append(problems, "security auth oidc state TTL must be at most 1h")
	}
	// A login ticket is a bearer credential in a URL, so its lifetime is capped
	// hard: long enough to survive one redirect, short enough that a leaked
	// referer is useless within a minute.
	if cfg.LoginTicketTTL > 300*time.Second {
		problems = append(problems, "security auth oidc login ticket TTL must be at most 300s")
	}
	if cfg.JWKSCacheTTL > 24*time.Hour {
		problems = append(problems, "security auth oidc jwks cache TTL must be at most 24h")
	}
	// A discovery document smaller than 1 KiB cannot carry the endpoints the
	// relying party needs, so a tiny cap is a misconfiguration rather than a
	// hardening choice.
	if cfg.MaxDiscoveryBodyByte < 1024 || cfg.MaxDiscoveryBodyByte > 16<<20 {
		problems = append(problems, "security auth oidc max discovery body bytes must be between 1024 and 16777216")
	}
	return problems
}

// validateTrustedProxies guards the X-Forwarded-For trust decision. Believing a
// spoofable header would let any client forge the IP used for login throttling
// and audit records, so every entry must parse as an IP or a CIDR.
func validateTrustedProxies(proxies []string) []string {
	var problems []string
	for _, raw := range proxies {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			problems = append(problems, "server trusted proxies must not contain an empty entry")
			continue
		}
		if _, _, err := net.ParseCIDR(entry); err == nil {
			continue
		}
		if net.ParseIP(entry) != nil {
			continue
		}
		problems = append(problems, fmt.Sprintf("server trusted proxy %q must be an IP address or a CIDR block", raw))
	}
	return problems
}
