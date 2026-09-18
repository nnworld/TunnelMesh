// Package oidc implements a minimal OpenID Connect relying party for the
// TunnelMesh management console.
//
// It is deliberately self-contained: no database, no HTTP handler, and no
// third-party dependency. Signature and claim validation are written out
// explicitly so every rejection reason is testable and reviewable.
//
// Accepted id_token signature algorithms are RS256, RS384, RS512, PS256,
// PS384, PS512, ES256, ES384, ES512, and EdDSA. The "none" algorithm and every
// HMAC algorithm (HS256, HS384, HS512) are rejected unconditionally, both when
// an operator configures a provider allowlist and again while verifying a token.
// No configuration value can enable them: an HMAC key would be the client
// secret, which any party able to read the provider row also possesses, so
// accepting HS* would turn a readable configuration into a forging capability.
//
// Issuers must be absolute https URLs. The package has no insecure escape
// hatch; tests use an httptest TLS server and inject its client.
package oidc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Sentinel errors. The text is the stable machine-readable code a handler puts
// in data.error, matching the convention used across internal/auth. Every
// message is deliberately free of the client secret, authorization code, PKCE
// verifier, and raw tokens.
var (
	ErrDiscoveryFailed      = errors.New("oidc_discovery_failed")
	ErrJWKSFetchFailed      = errors.New("oidc_jwks_fetch_failed")
	ErrTokenExchangeFailed  = errors.New("oidc_token_exchange_failed")
	ErrIDTokenInvalid       = errors.New("oidc_id_token_invalid")
	ErrUnsupportedAlgorithm = errors.New("oidc_unsupported_algorithm")
	ErrNoMatchingKey        = errors.New("oidc_no_matching_key")
	ErrClaimMissing         = errors.New("oidc_claim_missing")
	ErrRoleMappingInvalid   = errors.New("oidc_role_mapping_invalid")
	ErrProviderInvalid      = errors.New("oidc_provider_invalid")
)

// acceptedAlgorithms is the closed set of signature algorithms this package can
// verify. It excludes "none" and every HS* value by construction.
var acceptedAlgorithms = []string{
	"RS256", "RS384", "RS512",
	"PS256", "PS384", "PS512",
	"ES256", "ES384", "ES512",
	"EdDSA",
}

// defaultAlgorithms mirrors the oidc_providers.id_token_algs schema default so a
// provider row that somehow carries an empty allowlist still resolves to one
// safe algorithm instead of an empty set that would reject every token.
var defaultAlgorithms = []string{"RS256"}

// SecretProvider opens a sealed client secret. It is the same shape
// internal/auth.SecretProvider uses; redeclaring it here keeps this package free
// of an import cycle and free of any knowledge of how keys are managed.
type SecretProvider interface {
	Available() bool
	Decrypt(ciphertext, nonce []byte, keyID string, version int) (string, error)
}

// SealedSecret is the at-rest form of a client secret. The plaintext never
// lives in a Provider value, so printing or logging one cannot leak it.
type SealedSecret struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
	Version    int
}

// IsZero reports whether no secret is configured, which makes the provider a
// public client using PKCE alone.
func (s SealedSecret) IsZero() bool { return len(s.Ciphertext) == 0 }

// RoleMapping is one ordered claim-to-role rule. The first match wins.
type RoleMapping struct {
	// The tags keep the read model identical to the request body the console
	// submits. Without them encoding/json capitalizes the keys and the
	// role-mapping editor renders every stored mapping as empty.
	Claim string `json:"claim"`
	Value string `json:"value"`
	Role  string `json:"role"`
}

// Provider is the non-secret configuration of one identity provider. It carries
// no database identity beyond an opaque ID used for logging and error context.
type Provider struct {
	ID          string
	Name        string
	DisplayName string
	// Issuer must be an absolute https URL and must equal the id_token iss claim.
	Issuer   string
	ClientID string
	// ClientSecret is the sealed form. Secret, when set, wins and lets a caller
	// supply the value lazily from a vault without it ever being copied here.
	ClientSecret SealedSecret
	Secret       func(ctx context.Context) (string, error)

	Scopes      []string
	RedirectURI string

	// Endpoint overrides win over discovery, which lets an operator point at a
	// provider whose discovery document is unreachable from this network.
	AuthorizationEndpoint string
	TokenEndpoint         string
	UserinfoEndpoint      string
	JWKSURI               string

	IDTokenAlgs   []string
	UsernameClaim string
	RoleMappings  []RoleMapping
	DefaultRole   string
}

// ParseAlgorithms normalizes and validates an operator-supplied allowlist.
// "none" and every HS* value are rejected here, at configuration time, so a
// dangerous allowlist cannot be stored in the first place.
func ParseAlgorithms(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		canonical, ok := canonicalAlgorithm(trimmed)
		if !ok {
			return nil, ErrUnsupportedAlgorithm
		}
		out = append(out, canonical)
	}
	if len(out) == 0 {
		return append([]string(nil), defaultAlgorithms...), nil
	}
	return out, nil
}

// canonicalAlgorithm maps an algorithm name onto the closed set and returns its
// canonical spelling. Normalizing here is what lets "EdDSA", "EDDSA", and
// "eddsa" all resolve to one value, so a later equality check against the
// allowlist cannot fail on spelling alone.
func canonicalAlgorithm(alg string) (string, bool) {
	upper := strings.ToUpper(strings.TrimSpace(alg))
	// The dangerous families are rejected before the membership lookup so the
	// reasoning stays visible even if the accepted set grows.
	if upper == "NONE" || strings.HasPrefix(upper, "HS") {
		return "", false
	}
	for _, candidate := range acceptedAlgorithms {
		if strings.ToUpper(candidate) == upper {
			return candidate, true
		}
	}
	return "", false
}

// isAcceptedAlgorithm reports membership in the closed set.
func isAcceptedAlgorithm(alg string) bool {
	_, ok := canonicalAlgorithm(alg)
	return ok
}

// scopes returns the requested scopes with "openid" guaranteed first, because an
// authorization request without it is not an OIDC request and some IdPs then omit
// the id_token entirely.
func (p Provider) scopes() []string {
	out := []string{"openid"}
	seen := map[string]bool{"openid": true}
	for _, raw := range p.Scopes {
		scope := strings.TrimSpace(raw)
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	return out
}

// algorithms returns the validated allowlist for this provider.
func (p Provider) algorithms() []string {
	algs, err := ParseAlgorithms(p.IDTokenAlgs)
	if err != nil {
		return append([]string(nil), defaultAlgorithms...)
	}
	return algs
}

// usernameClaimName falls back to the conventional OIDC claim so an unset column
// still produces a usable account name.
func (p Provider) usernameClaimName() string {
	if claim := strings.TrimSpace(p.UsernameClaim); claim != "" {
		return claim
	}
	return "preferred_username"
}

// defaultRoleValue falls back to the least privileged role.
func (p Provider) defaultRoleValue() string {
	if role := strings.ToLower(strings.TrimSpace(p.DefaultRole)); role == "admin" || role == "user" {
		return role
	}
	return "user"
}

// Config carries the process-level knobs. Every duration is positive in
// practice; withDefaults fills anything an operator left unset so a partially
// built configuration cannot produce a zero timeout, which in net/http means
// "no timeout at all".
type Config struct {
	HTTPTimeout    time.Duration
	StateTTL       time.Duration
	LoginTicketTTL time.Duration
	JWKSCacheTTL   time.Duration
	MaxBodyBytes   int64
	// ClockSkew bounds how far an id_token's exp and nbf may drift from the
	// local clock. It is a tolerance for NTP drift, not a licence to accept
	// stale assertions.
	ClockSkew time.Duration
}

// DefaultConfig matches the documented defaults in the design spec.
func DefaultConfig() Config {
	return Config{
		HTTPTimeout:    10 * time.Second,
		StateTTL:       10 * time.Minute,
		LoginTicketTTL: 60 * time.Second,
		JWKSCacheTTL:   time.Hour,
		MaxBodyBytes:   1 << 20,
		ClockSkew:      60 * time.Second,
	}
}

func (c Config) withDefaults() Config {
	base := DefaultConfig()
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = base.HTTPTimeout
	}
	if c.StateTTL <= 0 {
		c.StateTTL = base.StateTTL
	}
	if c.LoginTicketTTL <= 0 {
		c.LoginTicketTTL = base.LoginTicketTTL
	}
	if c.JWKSCacheTTL <= 0 {
		c.JWKSCacheTTL = base.JWKSCacheTTL
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = base.MaxBodyBytes
	}
	if c.ClockSkew < 0 {
		c.ClockSkew = base.ClockSkew
	}
	return c
}

// RelyingParty performs discovery, key retrieval, the authorization-code
// exchange, and id_token verification. It holds no per-user state: the state,
// nonce, and PKCE verifier are owned by the caller's challenge store so any
// cluster node can finish a redirect that started on another one.
type RelyingParty struct {
	http    *http.Client
	cfg     Config
	secrets SecretProvider
	now     func() time.Time

	mu    sync.Mutex
	keys  map[string]*keyCacheEntry
	metas map[string]*metaCacheEntry
}

// NewRelyingParty builds the relying party. A nil client is replaced with one
// that always carries the configured timeout, because an untimed client would
// let a slow IdP hold a login goroutine open indefinitely.
func NewRelyingParty(httpClient *http.Client, cfg Config, secrets SecretProvider) *RelyingParty {
	cfg = cfg.withDefaults()
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.HTTPTimeout}
	} else if httpClient.Timeout <= 0 {
		// Copy rather than mutate the caller's client.
		clone := *httpClient
		clone.Timeout = cfg.HTTPTimeout
		httpClient = &clone
	}
	return &RelyingParty{
		http:    httpClient,
		cfg:     cfg,
		secrets: secrets,
		now:     func() time.Time { return time.Now().UTC() },
		keys:    map[string]*keyCacheEntry{},
		metas:   map[string]*metaCacheEntry{},
	}
}

// Config exposes the effective configuration so callers can reuse the TTLs for
// the challenge rows they own.
func (rp *RelyingParty) Config() Config { return rp.cfg }

// SetClock overrides the time source for tests.
func (rp *RelyingParty) SetClock(now func() time.Time) {
	if rp != nil && now != nil {
		rp.now = now
	}
}

// clientSecret resolves the plaintext client secret at the moment it is needed
// and never stores it on the Provider. An empty result means the provider is a
// public client and the token request is authenticated by PKCE alone.
func (rp *RelyingParty) clientSecret(ctx context.Context, p Provider) (string, error) {
	if p.Secret != nil {
		return p.Secret(ctx)
	}
	if p.ClientSecret.IsZero() {
		return "", nil
	}
	if rp.secrets == nil || !rp.secrets.Available() {
		return "", errors.New("oidc_secret_storage_unavailable")
	}
	return rp.secrets.Decrypt(p.ClientSecret.Ciphertext, p.ClientSecret.Nonce, p.ClientSecret.KeyID, p.ClientSecret.Version)
}

// get performs one bounded GET and returns the body. Every read is capped so a
// hostile or misconfigured IdP cannot exhaust memory.
func (rp *RelyingParty) get(ctx context.Context, url string, accept string) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, rp.cfg.HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := rp.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Read one byte past the limit so an oversized body is distinguishable
	// from one that exactly fits.
	body, err := io.ReadAll(io.LimitReader(resp.Body, rp.cfg.MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > rp.cfg.MaxBodyBytes {
		return nil, errResponseTooLarge
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%w: status %d", errUnexpectedStatus, resp.StatusCode)
	}
	return body, nil
}

// errUnexpectedStatus and errResponseTooLarge are internal markers. They are
// wrapped into the public sentinel by the caller so the message stays generic.
var (
	errUnexpectedStatus = errors.New("unexpected status")
	errResponseTooLarge = errors.New("response too large")
)
