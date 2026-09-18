package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth/oidc"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// ErrAuthPolicyInvalid is returned for any out-of-range authentication policy
// value. The offending field is named in the wrapped message so an operator can
// fix the request, while the stable code keeps the client contract.
var ErrAuthPolicyInvalid = errors.New("auth_policy_invalid")

// providerNamePattern is the URL-safe slug rule. The name is part of the public
// callback path, so it must be usable in a URL without escaping and must not be
// confusable with another provider.
var providerNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// reservedProviderNames are path segments the public OIDC router matches literally
// before it treats a segment as a provider name. A provider registered under one of
// them would be stored but permanently unreachable, so the name is refused instead
// of silently creating a dead configuration.
var reservedProviderNames = map[string]bool{"providers": true}

// OIDCProviderInput is one create or update request. ClientSecret is plaintext
// only inside this value: an empty secret on update means "keep the stored one",
// which matches the credential editing convention and avoids a read-modify-write
// that would have to expose the secret.
type OIDCProviderInput struct {
	Name                  string
	DisplayName           string
	Issuer                string
	ClientID              string
	ClientSecret          string
	Scopes                []string
	RedirectURI           string
	AuthorizationEndpoint string
	TokenEndpoint         string
	UserinfoEndpoint      string
	JWKSURI               string
	IDTokenAlgs           []string
	UsernameClaim         string
	RoleMappings          []oidc.RoleMapping
	DefaultRole           string
	// The toggles are pointers so a PATCH can distinguish "not supplied" from an
	// explicit false. A plain bool would silently switch a provider off whenever a
	// client omitted the field.
	AuthoritativeRoles *bool
	AutoCreateUsers    *bool
	FetchUserinfo      *bool
	PublicListed       *bool
	Enabled            *bool
}

// ProviderView is the only representation of a provider that leaves the service.
// It carries hasSecret rather than the secret, so no serialization path can leak
// the ciphertext either.
type ProviderView struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	DisplayName        string             `json:"displayName"`
	Issuer             string             `json:"issuer"`
	ClientID           string             `json:"clientId"`
	Scopes             []string           `json:"scopes"`
	RedirectURI        string             `json:"redirectUri"`
	AuthorizationEP    string             `json:"authorizationEndpoint"`
	TokenEP            string             `json:"tokenEndpoint"`
	UserinfoEP         string             `json:"userinfoEndpoint"`
	JWKSURI            string             `json:"jwksUri"`
	IDTokenAlgs        []string           `json:"idTokenAlgs"`
	UsernameClaim      string             `json:"usernameClaim"`
	RoleMappings       []oidc.RoleMapping `json:"roleMappings"`
	DefaultRole        string             `json:"defaultRole"`
	AuthoritativeRoles bool               `json:"authoritativeRoles"`
	AutoCreateUsers    bool               `json:"autoCreateUsers"`
	FetchUserinfo      bool               `json:"fetchUserinfo"`
	PublicListed       bool               `json:"publicListed"`
	Enabled            bool               `json:"enabled"`
	HasSecret          bool               `json:"hasSecret"`
	CreatedAt          time.Time          `json:"createdAt"`
	UpdatedAt          time.Time          `json:"updatedAt"`
}

// PublicProviderView is the unauthenticated listing. It exposes only what a
// login page needs to render a button, so an anonymous caller cannot enumerate
// issuers or client ids.
type PublicProviderView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

// TestReport is the outcome of an operator-triggered connectivity check. It
// never contains the client secret and never performs a token exchange, because
// a test must not be able to mint a session.
type TestReport struct {
	DiscoveryOK bool              `json:"discoveryOk"`
	JWKSOK      bool              `json:"jwksOk"`
	Algorithms  []string          `json:"algorithms"`
	Endpoints   map[string]string `json:"endpoints"`
	Error       string            `json:"error"`
}

// OIDCProviderService owns provider CRUD, validation, and the connectivity test.
// It is the only place that turns a stored row into an oidc.Provider, which keeps
// the secret-opening logic in one audited location.
type OIDCProviderService struct {
	store   IdentityStore
	secrets SecretProvider
	rp      *oidc.RelyingParty
	// allowedRedirectBases bounds where a callback may point. Without it an
	// administrator typo, or a compromised admin account, could send assertion
	// codes to an attacker-controlled host.
	allowedRedirectBases []string
	now                  func() time.Time
}

// NewOIDCProviderService builds the service. allowedRedirectBases is matched as a
// scheme://host[:port] prefix; an empty list means any absolute https URL on the
// same host as the issuer is rejected, so callers should always supply one.
func NewOIDCProviderService(store IdentityStore, secrets SecretProvider, rp *oidc.RelyingParty, allowedRedirectBases []string) *OIDCProviderService {
	if secrets == nil {
		secrets = NewSecretProvider(nil)
	}
	bases := make([]string, 0, len(allowedRedirectBases))
	for _, base := range allowedRedirectBases {
		if trimmed := strings.TrimSuffix(strings.TrimSpace(base), "/"); trimmed != "" {
			bases = append(bases, trimmed)
		}
	}
	return &OIDCProviderService{store: store, secrets: secrets, rp: rp, allowedRedirectBases: bases, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the time source for tests.
func (s *OIDCProviderService) SetClock(now func() time.Time) {
	if s != nil && now != nil {
		s.now = now
	}
}

// validate checks every field an operator supplies. Each rule names the field so
// the API can return an actionable message, and validation runs before any write
// so an invalid request persists nothing.
func (s *OIDCProviderService) validate(in OIDCProviderInput, isUpdate bool) error {
	name := strings.TrimSpace(in.Name)
	if !isUpdate {
		if !providerNamePattern.MatchString(name) {
			return fmt.Errorf("%w: name must match %s", ErrOIDCProviderInvalid, providerNamePattern.String())
		}
		if reservedProviderNames[name] {
			return fmt.Errorf("%w: name %q is reserved by the OIDC router", ErrOIDCProviderInvalid, name)
		}
	} else if name != "" && !providerNamePattern.MatchString(name) {
		return fmt.Errorf("%w: name must match %s", ErrOIDCProviderInvalid, providerNamePattern.String())
	} else if reservedProviderNames[name] {
		return fmt.Errorf("%w: name %q is reserved by the OIDC router", ErrOIDCProviderInvalid, name)
	}
	if strings.TrimSpace(in.ClientID) == "" {
		return errors.New("clientId is required")
	}
	if err := validateIssuer(in.Issuer); err != nil {
		return err
	}
	if err := s.validateRedirectURI(in.RedirectURI); err != nil {
		return err
	}
	if len(in.Scopes) == 0 {
		return errors.New("scopes must include openid")
	}
	// The check runs on what the operator supplied. normalizeScopes adds openid
	// later, so validating the normalized set could never fail and the rule would
	// be decoration.
	found := false
	for _, scope := range in.Scopes {
		if strings.EqualFold(strings.TrimSpace(scope), "openid") {
			found = true
			break
		}
	}
	if !found {
		return errors.New("scopes must include openid")
	}
	if _, err := oidc.ParseAlgorithms(in.IDTokenAlgs); err != nil {
		return fmt.Errorf("%w: idTokenAlgs must be one of RS256/384/512, PS256/384/512, ES256/384/512, EdDSA", ErrOIDCProviderInvalid)
	}
	if claim := strings.TrimSpace(in.UsernameClaim); claim != "" && len(claim) > 64 {
		return errors.New("usernameClaim must be at most 64 characters")
	}
	if role := strings.ToLower(strings.TrimSpace(in.DefaultRole)); role != "admin" && role != "user" {
		return errors.New("defaultRole must be admin or user")
	}
	for _, mapping := range in.RoleMappings {
		if strings.TrimSpace(mapping.Claim) == "" {
			return errors.New("roleMappings[].claim is required")
		}
		if role := strings.ToLower(strings.TrimSpace(mapping.Role)); role != "admin" && role != "user" {
			return fmt.Errorf("%w: roleMappings[].role must be admin or user", oidc.ErrRoleMappingInvalid)
		}
	}
	for _, endpoint := range []struct {
		field string
		value string
	}{
		{"authorizationEndpoint", in.AuthorizationEndpoint},
		{"tokenEndpoint", in.TokenEndpoint},
		{"userinfoEndpoint", in.UserinfoEndpoint},
		{"jwksUri", in.JWKSURI},
	} {
		if strings.TrimSpace(endpoint.value) == "" {
			continue
		}
		parsed, err := url.Parse(strings.TrimSpace(endpoint.value))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return fmt.Errorf("%s must be an absolute http(s) URL", endpoint.field)
		}
	}
	return nil
}

func validateIssuer(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return errors.New("issuer must be an absolute URL")
	}
	if parsed.Scheme != "https" {
		return errors.New("issuer must use https")
	}
	return nil
}

// validateRedirectURI keeps the callback inside the operator's own deployment.
func (s *OIDCProviderService) validateRedirectURI(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return errors.New("redirectUri must be an absolute URL")
	}
	if parsed.Scheme != "https" {
		return errors.New("redirectUri must use https")
	}
	base := parsed.Scheme + "://" + parsed.Host
	for _, allowed := range s.allowedRedirectBases {
		// The stored base may itself carry a path prefix, so compare as a prefix
		// of the normalized origin plus path.
		normalized := strings.TrimSuffix(parsed.Scheme+"://"+parsed.Host+parsed.Path, "/")
		if strings.EqualFold(base, allowed) || strings.HasPrefix(normalized, allowed+"/") || strings.EqualFold(normalized, allowed) {
			return nil
		}
	}
	if len(s.allowedRedirectBases) == 0 {
		return errors.New("redirectUri is not inside an allowed base")
	}
	return errors.New("redirectUri is not inside an allowed base")
}

// normalizeScopes deduplicates the requested scopes and stores openid first.
// Keeping the stored order identical to the order the authorization request uses
// means what an operator reads in the console is what the IdP receives.
func normalizeScopes(scopes []string) []string {
	out := []string{"openid"}
	seen := map[string]bool{"openid": true}
	for _, raw := range scopes {
		scope := strings.TrimSpace(raw)
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	return out
}

// Create stores a new provider. A client secret is sealed before the row is
// written, and the operation fails closed when no encryption key is configured.
func (s *OIDCProviderService) Create(ctx context.Context, actorID string, in OIDCProviderInput, idempotencyKey string) (ProviderView, error) {
	if err := s.validate(in, false); err != nil {
		return ProviderView{}, fmt.Errorf("%w: %s", ErrOIDCProviderInvalid, err)
	}
	name := strings.TrimSpace(in.Name)
	row := storage.OIDCProvider{
		Name:               name,
		DisplayName:        strings.TrimSpace(in.DisplayName),
		Issuer:             strings.TrimSpace(in.Issuer),
		ClientID:           strings.TrimSpace(in.ClientID),
		Scopes:             encodeStrings(normalizeScopes(in.Scopes)),
		RedirectURI:        strings.TrimSpace(in.RedirectURI),
		IDTokenAlgs:        encodeStrings(normalizeAlgorithms(in.IDTokenAlgs)),
		UsernameClaim:      strings.TrimSpace(in.UsernameClaim),
		RoleMappings:       encodeRoleMappings(in.RoleMappings),
		DefaultRole:        strings.ToLower(strings.TrimSpace(in.DefaultRole)),
		AuthoritativeRoles: derefBool(in.AuthoritativeRoles, true),
		AutoCreateUsers:    derefBool(in.AutoCreateUsers, true),
		FetchUserinfo:      derefBool(in.FetchUserinfo, false),
		PublicListed:       derefBool(in.PublicListed, true),
		Enabled:            derefBool(in.Enabled, true),
	}
	if row.DisplayName == "" {
		row.DisplayName = row.Name
	}
	if row.UsernameClaim == "" {
		row.UsernameClaim = "preferred_username"
	}
	row.AuthorizationEndpoint = strings.TrimSpace(in.AuthorizationEndpoint)
	row.TokenEndpoint = strings.TrimSpace(in.TokenEndpoint)
	row.UserinfoEndpoint = strings.TrimSpace(in.UserinfoEndpoint)
	row.JWKSURI = strings.TrimSpace(in.JWKSURI)

	if secret := strings.TrimSpace(in.ClientSecret); secret != "" {
		if !s.secrets.Available() {
			return ProviderView{}, ErrSecretStorageUnavailable
		}
		ciphertext, nonce, keyID, version, err := s.secrets.Encrypt(secret)
		if err != nil {
			return ProviderView{}, err
		}
		row.ClientSecretCiphertext = base64.StdEncoding.EncodeToString(ciphertext)
		row.ClientSecretNonce = base64.StdEncoding.EncodeToString(nonce)
		row.ClientSecretKeyID = keyID
		row.ClientSecretVersion = version
	}

	var view ProviderView
	err := s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		existing, err := repos.Providers.GetByName(ctx, name)
		if err == nil && existing.ID != "" {
			return storage.ErrIdentityConflict
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		created, err := repos.Providers.Create(ctx, row)
		if err != nil {
			return err
		}
		row = created
		view = providerView(row)
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  actorID,
			Action:       "auth.oidc.provider.create",
			ResourceType: "oidc_provider",
			ResourceID:   created.ID,
			Details:      providerAuditDetails(created, idempotencyKey, "name", "issuer", "clientId", "redirectUri", "enabled"),
		})
	})
	if err != nil {
		return ProviderView{}, err
	}
	return view, nil
}

// Update applies a partial change. The name is immutable because it is part of
// the published callback URL, and an empty client secret keeps the stored one.
func (s *OIDCProviderService) Update(ctx context.Context, actorID, id string, in OIDCProviderInput, idempotencyKey string) (ProviderView, error) {
	current, err := s.get(ctx, id)
	if err != nil {
		return ProviderView{}, err
	}
	if strings.TrimSpace(in.Name) != "" && strings.TrimSpace(in.Name) != current.Name {
		return ProviderView{}, fmt.Errorf("%w: name is part of the callback URL and cannot be changed", ErrOIDCProviderInvalid)
	}
	merged := inputFromProvider(current)
	applyProviderInput(&merged, in)
	if err := s.validate(merged, true); err != nil {
		return ProviderView{}, fmt.Errorf("%w: %s", ErrOIDCProviderInvalid, err)
	}

	row := current
	row.DisplayName = strings.TrimSpace(merged.DisplayName)
	row.Issuer = strings.TrimSpace(merged.Issuer)
	row.ClientID = strings.TrimSpace(merged.ClientID)
	row.Scopes = encodeStrings(normalizeScopes(merged.Scopes))
	row.RedirectURI = strings.TrimSpace(merged.RedirectURI)
	row.AuthorizationEndpoint = strings.TrimSpace(merged.AuthorizationEndpoint)
	row.TokenEndpoint = strings.TrimSpace(merged.TokenEndpoint)
	row.UserinfoEndpoint = strings.TrimSpace(merged.UserinfoEndpoint)
	row.JWKSURI = strings.TrimSpace(merged.JWKSURI)
	row.IDTokenAlgs = encodeStrings(normalizeAlgorithms(merged.IDTokenAlgs))
	row.UsernameClaim = strings.TrimSpace(merged.UsernameClaim)
	row.RoleMappings = encodeRoleMappings(merged.RoleMappings)
	row.DefaultRole = strings.ToLower(strings.TrimSpace(merged.DefaultRole))
	if merged.AuthoritativeRoles != nil {
		row.AuthoritativeRoles = *merged.AuthoritativeRoles
	}
	if merged.AutoCreateUsers != nil {
		row.AutoCreateUsers = *merged.AutoCreateUsers
	}
	if merged.FetchUserinfo != nil {
		row.FetchUserinfo = *merged.FetchUserinfo
	}
	if merged.PublicListed != nil {
		row.PublicListed = *merged.PublicListed
	}
	if merged.Enabled != nil {
		row.Enabled = *merged.Enabled
	}
	changed := changedProviderFields(current, row)

	if secret := strings.TrimSpace(in.ClientSecret); secret != "" {
		if !s.secrets.Available() {
			return ProviderView{}, ErrSecretStorageUnavailable
		}
		ciphertext, nonce, keyID, version, err := s.secrets.Encrypt(secret)
		if err != nil {
			return ProviderView{}, err
		}
		row.ClientSecretCiphertext = base64.StdEncoding.EncodeToString(ciphertext)
		row.ClientSecretNonce = base64.StdEncoding.EncodeToString(nonce)
		row.ClientSecretKeyID = keyID
		row.ClientSecretVersion = version
		changed = append(changed, "clientSecret")
	}

	// Disabling the last provider is treated like deleting it: accounts whose
	// only credential is external would lose every way in.
	if current.Enabled && !row.Enabled {
		if err := s.assertNotLastProvider(ctx, id); err != nil {
			return ProviderView{}, err
		}
	}

	var view ProviderView
	err = s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		if err := repos.Providers.Update(ctx, row); err != nil {
			return err
		}
		view = providerView(row)
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  actorID,
			Action:       "auth.oidc.provider.update",
			ResourceType: "oidc_provider",
			ResourceID:   row.ID,
			Details:      changedFieldsDetails(changed, idempotencyKey),
		})
	})
	if err != nil {
		return ProviderView{}, err
	}
	return view, nil
}

// Delete removes a provider. It refuses when the provider is the last enabled one
// and at least one account has no other credential, because that would lock those
// accounts out with no recovery path short of a database edit.
func (s *OIDCProviderService) Delete(ctx context.Context, actorID, id string, idempotencyKey string) error {
	current, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	if current.Enabled {
		if err := s.assertNotLastProvider(ctx, id); err != nil {
			return err
		}
	}
	return s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		if err := repos.Providers.Delete(ctx, id); err != nil {
			return err
		}
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  actorID,
			Action:       "auth.oidc.provider.delete",
			ResourceType: "oidc_provider",
			ResourceID:   id,
			Details:      changedFieldsDetails([]string{"name"}, idempotencyKey),
		})
	})
}

func (s *OIDCProviderService) assertNotLastProvider(ctx context.Context, excludeID string) error {
	var externalOnly int
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		enabled, err := repos.Providers.CountEnabled(ctx)
		if err != nil {
			return err
		}
		// The row being changed is still counted as enabled at this point.
		remaining := enabled
		if excludeID != "" {
			remaining = enabled - 1
		}
		if remaining > 0 {
			return nil
		}
		count, err := repos.ExternalOnly.CountExternalOnly(ctx)
		if err != nil {
			return err
		}
		externalOnly = count
		return nil
	})
	if err != nil {
		return err
	}
	if externalOnly > 0 {
		return fmt.Errorf("%w: %d account(s) authenticate only through this provider", ErrOIDCProviderInUse, externalOnly)
	}
	return nil
}

// Get returns one provider view.
func (s *OIDCProviderService) Get(ctx context.Context, id string) (ProviderView, error) {
	row, err := s.get(ctx, id)
	if err != nil {
		return ProviderView{}, err
	}
	return providerView(row), nil
}

func (s *OIDCProviderService) get(ctx context.Context, id string) (storage.OIDCProvider, error) {
	var row storage.OIDCProvider
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		found, err := repos.Providers.Get(ctx, strings.TrimSpace(id))
		row = found
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return storage.OIDCProvider{}, ErrOIDCProviderNotFound
	}
	return row, err
}

// List returns a cursor page of providers.
func (s *OIDCProviderService) List(ctx context.Context, cursor string, limit int) (storage.Page[ProviderView], error) {
	var page storage.Page[storage.OIDCProvider]
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		found, err := repos.Providers.List(ctx, cursor, limit)
		page = found
		return err
	})
	if err != nil {
		return storage.Page[ProviderView]{}, err
	}
	views := make([]ProviderView, 0, len(page.Items))
	for _, row := range page.Items {
		views = append(views, providerView(row))
	}
	return storage.Page[ProviderView]{Items: views, NextCursor: page.NextCursor, HasMore: page.HasMore}, nil
}

// ListPublic returns the providers an unauthenticated login page may show.
func (s *OIDCProviderService) ListPublic(ctx context.Context) ([]PublicProviderView, error) {
	var rows []storage.OIDCProvider
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		found, err := repos.Providers.ListEnabledPublic(ctx)
		rows = found
		return err
	})
	if err != nil {
		return nil, err
	}
	views := make([]PublicProviderView, 0, len(rows))
	for _, row := range rows {
		display := row.DisplayName
		if display == "" {
			display = row.Name
		}
		views = append(views, PublicProviderView{ID: row.ID, Name: row.Name, DisplayName: display})
	}
	return views, nil
}

// Test performs discovery and a JWKS retrieval against the live IdP. It reports
// the resolved algorithms and endpoints and never returns a secret.
func (s *OIDCProviderService) Test(ctx context.Context, actorID, id string) (TestReport, error) {
	row, err := s.get(ctx, id)
	if err != nil {
		return TestReport{}, err
	}
	provider, err := s.relyingPartyProvider(ctx, row)
	if err != nil {
		return TestReport{}, err
	}
	report := TestReport{Endpoints: map[string]string{}, Algorithms: normalizeAlgorithms(decodeStringList(row.IDTokenAlgs))}
	if s.rp == nil {
		report.Error = "oidc_relying_party_unavailable"
		return report, s.recordTestAudit(ctx, actorID, id, report)
	}
	// Drop any cached metadata so the operator sees this run, not a previous one.
	s.rp.InvalidateMetadata(row.Issuer)
	endpoints, err := s.rp.Discover(ctx, provider)
	if err != nil {
		report.Error = oidc.ErrDiscoveryFailed.Error()
	} else {
		report.DiscoveryOK = true
		report.Endpoints["authorization"] = endpoints.Authorization
		report.Endpoints["token"] = endpoints.Token
		report.Endpoints["jwks"] = endpoints.JWKS
		if endpoints.Userinfo != "" {
			report.Endpoints["userinfo"] = endpoints.Userinfo
		}
	}
	if report.DiscoveryOK {
		s.rp.InvalidateKeys(endpoints.JWKS)
		if _, err := s.rp.ProbeKeys(ctx, provider, endpoints.JWKS); err != nil {
			report.Error = oidc.ErrJWKSFetchFailed.Error()
		} else {
			report.JWKSOK = true
		}
	}
	if err := s.recordTestAudit(ctx, actorID, id, report); err != nil {
		return TestReport{}, err
	}
	return report, nil
}

// recordTestAudit writes the connectivity-test row. Only the boolean outcomes are
// recorded: the IdP's own error text is attacker-influenced and must not become
// part of an operator's audit trail.
func (s *OIDCProviderService) recordTestAudit(ctx context.Context, actorID, id string, report TestReport) error {
	return s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  actorID,
			Action:       "auth.oidc.provider.test",
			ResourceType: "oidc_provider",
			ResourceID:   id,
			Details:      fmt.Sprintf(`{"discoveryOk":%t,"jwksOk":%t}`, report.DiscoveryOK, report.JWKSOK),
		})
	})
}

// ResolveRelyingParty builds the runtime provider for one enabled, publicly
// reachable name. It is the single place a stored secret is opened for a login.
func (s *OIDCProviderService) ResolveRelyingParty(ctx context.Context, name string) (oidc.Provider, error) {
	var row storage.OIDCProvider
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		found, err := repos.Providers.GetByName(ctx, strings.TrimSpace(name))
		row = found
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return oidc.Provider{}, ErrOIDCProviderNotFound
	}
	if err != nil {
		return oidc.Provider{}, err
	}
	if !row.Enabled {
		return oidc.Provider{}, ErrOIDCProviderDisabled
	}
	return s.relyingPartyProvider(ctx, row)
}

// ResolveStored returns the stored row for a provider name. The callback handler
// needs the provisioning flags, which are deliberately not part of oidc.Provider.
func (s *OIDCProviderService) ResolveStored(ctx context.Context, name string) (storage.OIDCProvider, error) {
	var row storage.OIDCProvider
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		found, err := repos.Providers.GetByName(ctx, strings.TrimSpace(name))
		row = found
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return storage.OIDCProvider{}, ErrOIDCProviderNotFound
	}
	return row, err
}

// StoredProvider returns the row behind a view so the OIDC callback can read the
// provisioning flags without a second lookup path.
func (s *OIDCProviderService) StoredProvider(ctx context.Context, id string) (storage.OIDCProvider, error) {
	return s.get(ctx, id)
}

// relyingPartyProvider converts a stored row into the runtime form. The secret is
// resolved through a closure so the plaintext is fetched at use time and is never
// held on the returned value.
func (s *OIDCProviderService) relyingPartyProvider(ctx context.Context, row storage.OIDCProvider) (oidc.Provider, error) {
	provider := oidc.Provider{
		ID:                    row.ID,
		Name:                  row.Name,
		DisplayName:           row.DisplayName,
		Issuer:                row.Issuer,
		ClientID:              row.ClientID,
		Scopes:                decodeStringList(row.Scopes),
		RedirectURI:           row.RedirectURI,
		AuthorizationEndpoint: row.AuthorizationEndpoint,
		TokenEndpoint:         row.TokenEndpoint,
		UserinfoEndpoint:      row.UserinfoEndpoint,
		JWKSURI:               row.JWKSURI,
		IDTokenAlgs:           decodeStringList(row.IDTokenAlgs),
		UsernameClaim:         row.UsernameClaim,
		RoleMappings:          decodeRoleMappings(row.RoleMappings),
		DefaultRole:           row.DefaultRole,
	}
	if !row.HasSecret() {
		return provider, nil
	}
	sealed, err := decodeSealed(row)
	if err != nil {
		return oidc.Provider{}, err
	}
	provider.ClientSecret = sealed
	if s.secrets == nil || !s.secrets.Available() {
		return oidc.Provider{}, ErrSecretStorageUnavailable
	}
	return provider, nil
}

func decodeSealed(row storage.OIDCProvider) (oidc.SealedSecret, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(row.ClientSecretCiphertext)
	if err != nil {
		return oidc.SealedSecret{}, fmt.Errorf("decode client secret ciphertext: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(row.ClientSecretNonce)
	if err != nil {
		return oidc.SealedSecret{}, fmt.Errorf("decode client secret nonce: %w", err)
	}
	return oidc.SealedSecret{Ciphertext: ciphertext, Nonce: nonce, KeyID: row.ClientSecretKeyID, Version: row.ClientSecretVersion}, nil
}

// providerView strips every secret field. It is the only conversion from the
// storage row to the API shape, so the invariant is easy to audit.
func providerView(row storage.OIDCProvider) ProviderView {
	mappings := decodeRoleMappings(row.RoleMappings)
	if mappings == nil {
		mappings = []oidc.RoleMapping{}
	}
	return ProviderView{
		ID:                 row.ID,
		Name:               row.Name,
		DisplayName:        row.DisplayName,
		Issuer:             row.Issuer,
		ClientID:           row.ClientID,
		Scopes:             decodeStringList(row.Scopes),
		RedirectURI:        row.RedirectURI,
		AuthorizationEP:    row.AuthorizationEndpoint,
		TokenEP:            row.TokenEndpoint,
		UserinfoEP:         row.UserinfoEndpoint,
		JWKSURI:            row.JWKSURI,
		IDTokenAlgs:        decodeStringList(row.IDTokenAlgs),
		UsernameClaim:      row.UsernameClaim,
		RoleMappings:       mappings,
		DefaultRole:        row.DefaultRole,
		AuthoritativeRoles: row.AuthoritativeRoles,
		AutoCreateUsers:    row.AutoCreateUsers,
		FetchUserinfo:      row.FetchUserinfo,
		PublicListed:       row.PublicListed,
		Enabled:            row.Enabled,
		HasSecret:          row.HasSecret(),
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}

func normalizeAlgorithms(algs []string) []string {
	parsed, err := oidc.ParseAlgorithms(algs)
	if err != nil {
		return []string{"RS256"}
	}
	return parsed
}

func encodeStrings(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func decodeStringList(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" {
		return []string{}
	}
	if strings.HasPrefix(trimmed, "[") {
		var values []string
		if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
			return []string{}
		}
		return values
	}
	// Comma-separated is accepted for rows written before the JSON convention.
	parts := strings.Split(trimmed, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func encodeRoleMappings(mappings []oidc.RoleMapping) string {
	if len(mappings) == 0 {
		return "[]"
	}
	normalized := make([]oidc.RoleMapping, 0, len(mappings))
	for _, mapping := range mappings {
		normalized = append(normalized, oidc.RoleMapping{
			Claim: strings.TrimSpace(mapping.Claim),
			Value: mapping.Value,
			Role:  strings.ToLower(strings.TrimSpace(mapping.Role)),
		})
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func decodeRoleMappings(raw string) []oidc.RoleMapping {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" {
		return []oidc.RoleMapping{}
	}
	var mappings []oidc.RoleMapping
	if err := json.Unmarshal([]byte(trimmed), &mappings); err != nil {
		return []oidc.RoleMapping{}
	}
	return mappings
}

// inputFromProvider renders a stored row as an input so an update can merge a
// partial request over the current state. The secret is deliberately omitted:
// it cannot be read back, and an empty secret on the merged input means "keep".
func inputFromProvider(row storage.OIDCProvider) OIDCProviderInput {
	authoritative := row.AuthoritativeRoles
	autoCreate := row.AutoCreateUsers
	fetchUserinfo := row.FetchUserinfo
	publicListed := row.PublicListed
	enabled := row.Enabled
	return OIDCProviderInput{
		Name:                  row.Name,
		DisplayName:           row.DisplayName,
		Issuer:                row.Issuer,
		ClientID:              row.ClientID,
		Scopes:                decodeStringList(row.Scopes),
		RedirectURI:           row.RedirectURI,
		AuthorizationEndpoint: row.AuthorizationEndpoint,
		TokenEndpoint:         row.TokenEndpoint,
		UserinfoEndpoint:      row.UserinfoEndpoint,
		JWKSURI:               row.JWKSURI,
		IDTokenAlgs:           decodeStringList(row.IDTokenAlgs),
		UsernameClaim:         row.UsernameClaim,
		RoleMappings:          decodeRoleMappings(row.RoleMappings),
		DefaultRole:           row.DefaultRole,
		AuthoritativeRoles:    &authoritative,
		AutoCreateUsers:       &autoCreate,
		FetchUserinfo:         &fetchUserinfo,
		PublicListed:          &publicListed,
		Enabled:               &enabled,
	}
}

// applyProviderInput overwrites only the fields the request actually carried, so
// a PATCH is a partial update rather than a full replace.
func applyProviderInput(target *OIDCProviderInput, in OIDCProviderInput) {
	if v := strings.TrimSpace(in.DisplayName); v != "" {
		target.DisplayName = v
	}
	if v := strings.TrimSpace(in.Issuer); v != "" {
		target.Issuer = v
	}
	if v := strings.TrimSpace(in.ClientID); v != "" {
		target.ClientID = v
	}
	if len(in.Scopes) > 0 {
		target.Scopes = in.Scopes
	}
	if v := strings.TrimSpace(in.RedirectURI); v != "" {
		target.RedirectURI = v
	}
	if v := strings.TrimSpace(in.AuthorizationEndpoint); v != "" {
		target.AuthorizationEndpoint = v
	}
	if v := strings.TrimSpace(in.TokenEndpoint); v != "" {
		target.TokenEndpoint = v
	}
	if v := strings.TrimSpace(in.UserinfoEndpoint); v != "" {
		target.UserinfoEndpoint = v
	}
	if v := strings.TrimSpace(in.JWKSURI); v != "" {
		target.JWKSURI = v
	}
	if len(in.IDTokenAlgs) > 0 {
		target.IDTokenAlgs = in.IDTokenAlgs
	}
	if v := strings.TrimSpace(in.UsernameClaim); v != "" {
		target.UsernameClaim = v
	}
	if len(in.RoleMappings) > 0 {
		target.RoleMappings = in.RoleMappings
	}
	if v := strings.TrimSpace(in.DefaultRole); v != "" {
		target.DefaultRole = v
	}
	copyBoolPointer(&target.AuthoritativeRoles, in.AuthoritativeRoles)
	copyBoolPointer(&target.AutoCreateUsers, in.AutoCreateUsers)
	copyBoolPointer(&target.FetchUserinfo, in.FetchUserinfo)
	copyBoolPointer(&target.PublicListed, in.PublicListed)
	copyBoolPointer(&target.Enabled, in.Enabled)
}

// copyBoolPointer overwrites the target only when the request carried a value, so
// an omitted toggle keeps its stored state instead of being switched off.
func copyBoolPointer(target **bool, source *bool) {
	if source == nil {
		return
	}
	value := *source
	*target = &value
}

// derefBool applies the documented default for a toggle a create request omitted.
func derefBool(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

// changedProviderFields names the fields whose value actually differs, so the
// audit row records what moved without recording secret material.
func changedProviderFields(before, after storage.OIDCProvider) []string {
	changed := []string{}
	add := func(field string, differs bool) {
		if differs {
			changed = append(changed, field)
		}
	}
	add("displayName", before.DisplayName != after.DisplayName)
	add("issuer", before.Issuer != after.Issuer)
	add("clientId", before.ClientID != after.ClientID)
	add("scopes", before.Scopes != after.Scopes)
	add("redirectUri", before.RedirectURI != after.RedirectURI)
	add("authorizationEndpoint", before.AuthorizationEndpoint != after.AuthorizationEndpoint)
	add("tokenEndpoint", before.TokenEndpoint != after.TokenEndpoint)
	add("userinfoEndpoint", before.UserinfoEndpoint != after.UserinfoEndpoint)
	add("jwksUri", before.JWKSURI != after.JWKSURI)
	add("idTokenAlgs", before.IDTokenAlgs != after.IDTokenAlgs)
	add("usernameClaim", before.UsernameClaim != after.UsernameClaim)
	add("roleMappings", before.RoleMappings != after.RoleMappings)
	add("defaultRole", before.DefaultRole != after.DefaultRole)
	add("authoritativeRoles", before.AuthoritativeRoles != after.AuthoritativeRoles)
	add("autoCreateUsers", before.AutoCreateUsers != after.AutoCreateUsers)
	add("fetchUserinfo", before.FetchUserinfo != after.FetchUserinfo)
	add("publicListed", before.PublicListed != after.PublicListed)
	add("enabled", before.Enabled != after.Enabled)
	return changed
}

func providerAuditDetails(row storage.OIDCProvider, idempotencyKey string, fields ...string) string {
	return changedFieldsDetails(fields, idempotencyKey)
}

// changedFieldsDetails renders the audit payload as a list of field names plus
// the idempotency key. Values are intentionally omitted: a field list is enough
// to reconstruct what an operator did, and omitting values removes any chance of
// recording a secret.
func changedFieldsDetails(fields []string, idempotencyKey string) string {
	details := map[string]any{"fields": fields}
	if key := strings.TrimSpace(idempotencyKey); key != "" {
		details["idempotencyKey"] = key
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return `{"fields":[]}`
	}
	return string(encoded)
}
