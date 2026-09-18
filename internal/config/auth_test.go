package config_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// TestLoadAuthDefaultsMatchesSpec pins the documented security.auth block. A
// silent default change here would alter MFA strength or the trusted-device
// cookie contract for every deployment, so the values are asserted explicitly.
func TestLoadAuthDefaultsMatchesSpec(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	auth := cfg.Security.Auth

	if auth.SessionTokenTTL != 0 {
		t.Fatalf("SessionTokenTTL = %v, want 0", auth.SessionTokenTTL)
	}
	if auth.LoginThrottle.MaxAttempts != 10 {
		t.Fatalf("LoginThrottle.MaxAttempts = %d, want 10", auth.LoginThrottle.MaxAttempts)
	}
	if auth.LoginThrottle.Window != 5*time.Minute {
		t.Fatalf("LoginThrottle.Window = %v, want 5m", auth.LoginThrottle.Window)
	}
	if auth.LoginThrottle.Block != 10*time.Minute {
		t.Fatalf("LoginThrottle.Block = %v, want 10m", auth.LoginThrottle.Block)
	}
	if auth.MFA.Issuer != "TunnelMesh" {
		t.Fatalf("MFA.Issuer = %q, want TunnelMesh", auth.MFA.Issuer)
	}
	if auth.MFA.Digits != 6 {
		t.Fatalf("MFA.Digits = %d, want 6", auth.MFA.Digits)
	}
	if auth.MFA.Period != 30*time.Second {
		t.Fatalf("MFA.Period = %v, want 30s", auth.MFA.Period)
	}
	if auth.MFA.Skew != 1 {
		t.Fatalf("MFA.Skew = %d, want 1", auth.MFA.Skew)
	}
	if auth.MFA.ChallengeTTL != 5*time.Minute {
		t.Fatalf("MFA.ChallengeTTL = %v, want 5m", auth.MFA.ChallengeTTL)
	}
	if auth.MFA.MaxAttempts != 5 {
		t.Fatalf("MFA.MaxAttempts = %d, want 5", auth.MFA.MaxAttempts)
	}
	if auth.MFA.RecoveryCodes != 10 {
		t.Fatalf("MFA.RecoveryCodes = %d, want 10", auth.MFA.RecoveryCodes)
	}
	if !auth.DeviceTrust.Enabled {
		t.Fatal("DeviceTrust.Enabled = false, want true")
	}
	if auth.DeviceTrust.CookieName != "tm_device" {
		t.Fatalf("DeviceTrust.CookieName = %q, want tm_device", auth.DeviceTrust.CookieName)
	}
	if !auth.DeviceTrust.CookieSecure {
		t.Fatal("DeviceTrust.CookieSecure = false, want true")
	}
	if auth.DeviceTrust.CookieSameSite != "lax" {
		t.Fatalf("DeviceTrust.CookieSameSite = %q, want lax", auth.DeviceTrust.CookieSameSite)
	}
	if !auth.DeviceTrust.BypassMFA {
		t.Fatal("DeviceTrust.BypassMFA = false, want true")
	}
	if !auth.OIDC.PublicProviders {
		t.Fatal("OIDC.PublicProviders = false, want true")
	}
	if auth.OIDC.HTTPTimeout != 10*time.Second {
		t.Fatalf("OIDC.HTTPTimeout = %v, want 10s", auth.OIDC.HTTPTimeout)
	}
	if auth.OIDC.StateTTL != 10*time.Minute {
		t.Fatalf("OIDC.StateTTL = %v, want 10m", auth.OIDC.StateTTL)
	}
	if auth.OIDC.LoginTicketTTL != 60*time.Second {
		t.Fatalf("OIDC.LoginTicketTTL = %v, want 60s", auth.OIDC.LoginTicketTTL)
	}
	if auth.OIDC.JWKSCacheTTL != time.Hour {
		t.Fatalf("OIDC.JWKSCacheTTL = %v, want 1h", auth.OIDC.JWKSCacheTTL)
	}
	if auth.OIDC.MaxDiscoveryBodyByte != 1<<20 {
		t.Fatalf("OIDC.MaxDiscoveryBodyByte = %d, want 1048576", auth.OIDC.MaxDiscoveryBodyByte)
	}
	if len(cfg.Server.TrustedProxies) != 0 {
		t.Fatalf("Server.TrustedProxies = %v, want empty so X-Forwarded-For is never believed by default", cfg.Server.TrustedProxies)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate(defaults) = %v", err)
	}
}

// TestLoadAuthPrecedence proves the identity block follows the same
// CLI > env > file > default order as the rest of the configuration.
func TestLoadAuthPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.yaml")
	contents := "security:\n  auth:\n    mfa:\n      issuer: file-issuer\n      digits: 8\n    oidc:\n      public_providers: false\nserver:\n  trusted_proxies: [10.0.0.0/8]\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUNNELMESH_SECURITY_AUTH_MFA_ISSUER", "env-issuer")

	cfg, err := config.Load(context.Background(), config.ConfigOptions{
		ConfigFile: path,
		CLI:        map[string]any{"security.auth.mfa.digits": 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.Auth.MFA.Issuer != "env-issuer" {
		t.Fatalf("MFA.Issuer = %q, want env-issuer", cfg.Security.Auth.MFA.Issuer)
	}
	if cfg.Security.Auth.MFA.Digits != 6 {
		t.Fatalf("MFA.Digits = %d, want CLI value 6", cfg.Security.Auth.MFA.Digits)
	}
	if cfg.Security.Auth.OIDC.PublicProviders {
		t.Fatal("OIDC.PublicProviders = true, want the file value false")
	}
	if len(cfg.Server.TrustedProxies) != 1 || cfg.Server.TrustedProxies[0] != "10.0.0.0/8" {
		t.Fatalf("Server.TrustedProxies = %v, want [10.0.0.0/8]", cfg.Server.TrustedProxies)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

// TestValidateRejectsUnsafeAuthConfiguration covers every documented bound.
// Each case must fail: a permissive value that slips through would weaken MFA
// or let a spoofable header drive the login throttle.
func TestValidateRejectsUnsafeAuthConfiguration(t *testing.T) {
	base := config.Config{
		Mode:     config.ModeLocal,
		Storage:  config.StorageConfig{Driver: config.StorageSQLite},
		Registry: config.RegistryConfig{Type: config.RegistryDatabase},
		Security: config.SecurityConfig{Auth: config.DefaultAuthConfig()},
	}
	applyStreamLatencyDefaults(&base)
	if err := config.Validate(base); err != nil {
		t.Fatalf("Validate(default auth) = %v", err)
	}

	cases := []struct {
		name string
		edit func(*config.Config)
		want string
	}{
		{name: "negative session ttl", edit: func(cfg *config.Config) {
			cfg.Security.Auth.SessionTokenTTL = -time.Second
		}, want: "session token ttl"},
		{name: "throttle attempts out of range", edit: func(cfg *config.Config) {
			cfg.Security.Auth.LoginThrottle.MaxAttempts = 1001
		}, want: "login throttle max attempts"},
		{name: "throttle window too long", edit: func(cfg *config.Config) {
			cfg.Security.Auth.LoginThrottle.Window = 48 * time.Hour
		}, want: "login throttle window"},
		{name: "throttle block too long", edit: func(cfg *config.Config) {
			cfg.Security.Auth.LoginThrottle.Block = 48 * time.Hour
		}, want: "login throttle block"},
		{name: "negative session ttl rejected", edit: func(cfg *config.Config) {
			cfg.Security.Auth.SessionTokenTTL = -time.Hour
		}, want: "session token ttl"},
		{name: "issuer with colon", edit: func(cfg *config.Config) {
			cfg.Security.Auth.MFA.Issuer = "Tunnel:Mesh"
		}, want: "mfa issuer"},
		{name: "issuer too long", edit: func(cfg *config.Config) {
			cfg.Security.Auth.MFA.Issuer = strings.Repeat("a", 65)
		}, want: "mfa issuer"},
		{name: "digits seven", edit: func(cfg *config.Config) {
			cfg.Security.Auth.MFA.Digits = 7
		}, want: "mfa digits"},
		{name: "period too short", edit: func(cfg *config.Config) {
			cfg.Security.Auth.MFA.Period = 5 * time.Second
		}, want: "mfa period"},
		{name: "period too long", edit: func(cfg *config.Config) {
			cfg.Security.Auth.MFA.Period = 5 * time.Minute
		}, want: "mfa period"},
		{name: "skew three", edit: func(cfg *config.Config) {
			cfg.Security.Auth.MFA.Skew = 3
		}, want: "mfa skew"},
		{name: "challenge ttl too short", edit: func(cfg *config.Config) {
			cfg.Security.Auth.MFA.ChallengeTTL = time.Second
		}, want: "challenge ttl"},
		{name: "mfa attempts too many", edit: func(cfg *config.Config) {
			cfg.Security.Auth.MFA.MaxAttempts = 21
		}, want: "mfa max attempts"},
		{name: "recovery codes too many", edit: func(cfg *config.Config) {
			cfg.Security.Auth.MFA.RecoveryCodes = 51
		}, want: "recovery codes"},
		{name: "cookie name too long", edit: func(cfg *config.Config) {
			cfg.Security.Auth.DeviceTrust.CookieName = strings.Repeat("d", 65)
		}, want: "cookie name"},
		{name: "cookie name with delimiter", edit: func(cfg *config.Config) {
			cfg.Security.Auth.DeviceTrust.CookieName = "tm;device"
		}, want: "cookie name"},
		{name: "unknown same site", edit: func(cfg *config.Config) {
			cfg.Security.Auth.DeviceTrust.CookieSameSite = "disabled"
		}, want: "same site"},
		{name: "same site none without secure", edit: func(cfg *config.Config) {
			cfg.Security.Auth.DeviceTrust.CookieSameSite = "none"
			cfg.Security.Auth.DeviceTrust.CookieSecure = false
		}, want: "same site none requires cookie secure"},
		{name: "oidc http timeout too long", edit: func(cfg *config.Config) {
			cfg.Security.Auth.OIDC.HTTPTimeout = 10 * time.Minute
		}, want: "oidc http timeout"},
		{name: "oidc state ttl too long", edit: func(cfg *config.Config) {
			cfg.Security.Auth.OIDC.StateTTL = 2 * time.Hour
		}, want: "oidc state ttl"},
		{name: "oidc login ticket ttl one hour", edit: func(cfg *config.Config) {
			cfg.Security.Auth.OIDC.LoginTicketTTL = time.Hour
		}, want: "login ticket ttl"},
		{name: "oidc jwks cache ttl too long", edit: func(cfg *config.Config) {
			cfg.Security.Auth.OIDC.JWKSCacheTTL = 48 * time.Hour
		}, want: "jwks cache ttl"},
		{name: "oidc discovery body too small", edit: func(cfg *config.Config) {
			cfg.Security.Auth.OIDC.MaxDiscoveryBodyByte = 16
		}, want: "max discovery body bytes"},
		{name: "malformed trusted proxy cidr", edit: func(cfg *config.Config) {
			cfg.Server.TrustedProxies = []string{"10.0.0.0/33"}
		}, want: "trusted proxy"},
		{name: "hostname trusted proxy", edit: func(cfg *config.Config) {
			cfg.Server.TrustedProxies = []string{"proxy.internal"}
		}, want: "trusted proxy"},
		{name: "empty trusted proxy entry", edit: func(cfg *config.Config) {
			cfg.Server.TrustedProxies = []string{""}
		}, want: "trusted proxies"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Security.Auth = config.DefaultAuthConfig()
			tc.edit(&cfg)
			err := config.Validate(cfg)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestValidateAcceptsTrustedProxyForms proves a plain IP and a CIDR block are
// both accepted, so an operator does not have to write /32 by hand.
func TestValidateAcceptsTrustedProxyForms(t *testing.T) {
	cfg := config.Config{
		Mode:     config.ModeLocal,
		Storage:  config.StorageConfig{Driver: config.StorageSQLite},
		Registry: config.RegistryConfig{Type: config.RegistryDatabase},
		Security: config.SecurityConfig{Auth: config.DefaultAuthConfig()},
		Server:   config.ServerConfig{TrustedProxies: []string{"127.0.0.1", "::1", "10.0.0.0/8", "fd00::/8"}},
	}
	applyStreamLatencyDefaults(&cfg)
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

// TestValidateAcceptsSameSiteNoneWithSecure confirms the secure combination is
// allowed, because a cross-site console embedding is a legitimate deployment.
func TestValidateAcceptsSameSiteNoneWithSecure(t *testing.T) {
	cfg := config.Config{
		Mode:     config.ModeLocal,
		Storage:  config.StorageConfig{Driver: config.StorageSQLite},
		Registry: config.RegistryConfig{Type: config.RegistryDatabase},
		Security: config.SecurityConfig{Auth: config.DefaultAuthConfig()},
	}
	applyStreamLatencyDefaults(&cfg)
	cfg.Security.Auth.DeviceTrust.CookieSameSite = "none"
	cfg.Security.Auth.DeviceTrust.CookieSecure = true
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

// TestValidateAcceptsUnsetAuthBlock documents that a zero-valued security.auth
// block means "not configured" and validates as the documented defaults. This is
// what lets an embedder build a config.Config by hand without repeating every
// identity leaf, and it matches how the runtime fills unset values.
func TestValidateAcceptsUnsetAuthBlock(t *testing.T) {
	cfg := config.Config{
		Mode:     config.ModeLocal,
		Storage:  config.StorageConfig{Driver: config.StorageSQLite},
		Registry: config.RegistryConfig{Type: config.RegistryDatabase},
	}
	applyStreamLatencyDefaults(&cfg)
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate(unset auth) = %v", err)
	}

	normalized := config.NormalizeAuth(config.AuthConfig{})
	want := config.DefaultAuthConfig()
	// Every leaf that has a non-zero default must be filled in. Skew is the one
	// exception: 0 is a valid explicit setting ("current step only") and cannot
	// be distinguished from unset, so normalization leaves it alone.
	if normalized.MFA.Issuer != want.MFA.Issuer || normalized.MFA.Digits != want.MFA.Digits ||
		normalized.MFA.Period != want.MFA.Period ||
		normalized.MFA.ChallengeTTL != want.MFA.ChallengeTTL || normalized.MFA.MaxAttempts != want.MFA.MaxAttempts ||
		normalized.MFA.RecoveryCodes != want.MFA.RecoveryCodes {
		t.Fatalf("NormalizeAuth MFA = %+v, want %+v", normalized.MFA, want.MFA)
	}
	if normalized.MFA.Skew != 0 {
		t.Fatalf("NormalizeAuth MFA.Skew = %d, want 0 preserved", normalized.MFA.Skew)
	}
	if normalized.LoginThrottle != want.LoginThrottle {
		t.Fatalf("NormalizeAuth LoginThrottle = %+v, want %+v", normalized.LoginThrottle, want.LoginThrottle)
	}
	if normalized.DeviceTrust.CookieName != want.DeviceTrust.CookieName ||
		normalized.DeviceTrust.CookieSameSite != want.DeviceTrust.CookieSameSite {
		t.Fatalf("NormalizeAuth DeviceTrust = %+v, want cookie %+v", normalized.DeviceTrust, want.DeviceTrust)
	}
	// Booleans are compared only where false is not a legitimate operator
	// choice; PublicProviders=false and Enabled=false are both meaningful, so
	// normalization cannot infer them and they stay as written.
	if normalized.OIDC.HTTPTimeout != want.OIDC.HTTPTimeout || normalized.OIDC.StateTTL != want.OIDC.StateTTL ||
		normalized.OIDC.LoginTicketTTL != want.OIDC.LoginTicketTTL || normalized.OIDC.JWKSCacheTTL != want.OIDC.JWKSCacheTTL ||
		normalized.OIDC.MaxDiscoveryBodyByte != want.OIDC.MaxDiscoveryBodyByte {
		t.Fatalf("NormalizeAuth OIDC = %+v, want %+v", normalized.OIDC, want.OIDC)
	}
	if normalized.OIDC.PublicProviders {
		t.Fatal("NormalizeAuth must not turn a disabled public provider listing on")
	}
}
