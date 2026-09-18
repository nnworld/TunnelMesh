package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

// Endpoints is the resolved set of IdP URLs. Overrides supplied on the Provider
// always win, so an operator can route around a discovery document that is
// unreachable from the TunnelMesh network.
type Endpoints struct {
	Authorization string
	Token         string
	Userinfo      string
	JWKS          string
	Issuer        string
}

// discoveryDocument is the subset of the OIDC provider metadata this package
// needs. Unknown members are ignored on purpose: a provider that publishes extra
// metadata must still work.
type discoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// metaCacheEntry bounds how often one issuer's metadata is refetched. Discovery
// happens on every redirect, so without a cache each login would add a round trip
// to the IdP on the critical path.
type metaCacheEntry struct {
	endpoints Endpoints
	expiresAt time.Time
}

// wellKnownPath is appended to the issuer. When the issuer already carries a
// path component the well-known segment goes after it, per the OpenID Connect
// Discovery specification.
const wellKnownPath = "/.well-known/openid-configuration"

// Discover resolves the provider's endpoints. It fails closed with
// ErrDiscoveryFailed for a non-https issuer, a non-2xx response, an oversized or
// malformed body, a timeout, and a missing required endpoint, so the caller can
// return one generic error without revealing which check tripped.
func (rp *RelyingParty) Discover(ctx context.Context, p Provider) (Endpoints, error) {
	resolved := Endpoints{
		Authorization: strings.TrimSpace(p.AuthorizationEndpoint),
		Token:         strings.TrimSpace(p.TokenEndpoint),
		Userinfo:      strings.TrimSpace(p.UserinfoEndpoint),
		JWKS:          strings.TrimSpace(p.JWKSURI),
		Issuer:        strings.TrimSpace(p.Issuer),
	}
	if err := validateIssuerURL(resolved.Issuer); err != nil {
		return Endpoints{}, err
	}
	// A complete override set needs no network call at all.
	if resolved.Authorization != "" && resolved.Token != "" && resolved.JWKS != "" {
		return resolved, nil
	}

	if cached, ok := rp.cachedMetadata(resolved.Issuer); ok {
		return mergeOverrides(resolved, cached), nil
	}

	base := strings.TrimSuffix(resolved.Issuer, "/")
	endpoint := base + wellKnownPath
	if strings.HasSuffix(base, wellKnownPath) {
		// An issuer configured with the well-known path already included is used
		// verbatim instead of being doubled.
		endpoint = base
	}
	body, err := rp.get(ctx, endpoint, "application/json")
	if err != nil {
		return Endpoints{}, errors.Join(ErrDiscoveryFailed, err)
	}
	var doc discoveryDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return Endpoints{}, errors.Join(ErrDiscoveryFailed, err)
	}
	// A discovery document that names a different issuer is either a
	// misconfiguration or an IdP mix-up attempt; both must stop the flow.
	if published := strings.TrimSpace(doc.Issuer); published != "" && strings.TrimSuffix(published, "/") != strings.TrimSuffix(resolved.Issuer, "/") {
		return Endpoints{}, errors.Join(ErrDiscoveryFailed, errors.New("issuer mismatch"))
	}
	discovered := Endpoints{
		Authorization: strings.TrimSpace(doc.AuthorizationEndpoint),
		Token:         strings.TrimSpace(doc.TokenEndpoint),
		Userinfo:      strings.TrimSpace(doc.UserinfoEndpoint),
		JWKS:          strings.TrimSpace(doc.JWKSURI),
		Issuer:        resolved.Issuer,
	}
	merged := mergeOverrides(resolved, discovered)
	if merged.Authorization == "" || merged.Token == "" || merged.JWKS == "" {
		return Endpoints{}, errors.Join(ErrDiscoveryFailed, errors.New("required endpoint missing"))
	}
	for _, candidate := range []string{merged.Authorization, merged.Token, merged.JWKS} {
		if err := validateAbsoluteURL(candidate); err != nil {
			return Endpoints{}, errors.Join(ErrDiscoveryFailed, err)
		}
	}
	rp.storeMetadata(resolved.Issuer, discovered)
	return merged, nil
}

// mergeOverrides lets an explicit provider setting replace a discovered value.
func mergeOverrides(override, discovered Endpoints) Endpoints {
	out := discovered
	if override.Authorization != "" {
		out.Authorization = override.Authorization
	}
	if override.Token != "" {
		out.Token = override.Token
	}
	if override.JWKS != "" {
		out.JWKS = override.JWKS
	}
	if override.Userinfo != "" {
		out.Userinfo = override.Userinfo
	}
	out.Issuer = override.Issuer
	return out
}

func (rp *RelyingParty) cachedMetadata(issuer string) (Endpoints, bool) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	entry, ok := rp.metas[issuer]
	if !ok || !rp.now().Before(entry.expiresAt) {
		return Endpoints{}, false
	}
	return entry.endpoints, true
}

func (rp *RelyingParty) storeMetadata(issuer string, endpoints Endpoints) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.metas[issuer] = &metaCacheEntry{endpoints: endpoints, expiresAt: rp.now().Add(rp.cfg.JWKSCacheTTL)}
}

// InvalidateMetadata drops the cached discovery result for one issuer. The
// provider test action uses it so an operator re-testing a provider sees a fresh
// fetch rather than the previous outcome.
func (rp *RelyingParty) InvalidateMetadata(issuer string) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	delete(rp.metas, issuer)
}

// validateIssuerURL enforces the absolute-https rule. Loopback and private hosts
// are still allowed because an on-premises IdP legitimately lives on an internal
// address; the SSRF policy for tunnel targets is enforced elsewhere and does not
// apply to operator-configured identity providers.
func validateIssuerURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return errors.Join(ErrDiscoveryFailed, errors.New("issuer must be an absolute URL"))
	}
	if parsed.Scheme != "https" {
		return errors.Join(ErrDiscoveryFailed, errors.New("issuer must use https"))
	}
	return nil
}

// validateAbsoluteURL rejects a relative or scheme-less endpoint, which would
// otherwise be resolved against an attacker-influenced base.
func validateAbsoluteURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return errors.New("endpoint must be an absolute http(s) URL")
	}
	return nil
}
