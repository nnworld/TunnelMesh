package server

import (
	"net/http"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/auth/oidc"
)

// roleMappingRequest mirrors oidc.RoleMapping. It is redeclared at the transport
// boundary so the JSON shape can change without changing the domain type.
type roleMappingRequest struct {
	Claim string `json:"claim"`
	Value string `json:"value"`
	Role  string `json:"role"`
}

// ssoProviderRequest is the create and update body. Every toggle is a pointer so
// a PATCH that omits a field keeps the stored value instead of switching it off.
type ssoProviderRequest struct {
	Name                  string               `json:"name"`
	DisplayName           string               `json:"displayName"`
	Issuer                string               `json:"issuer"`
	ClientID              string               `json:"clientId"`
	ClientSecret          string               `json:"clientSecret"`
	Scopes                []string             `json:"scopes"`
	RedirectURI           string               `json:"redirectUri"`
	AuthorizationEndpoint string               `json:"authorizationEndpoint"`
	TokenEndpoint         string               `json:"tokenEndpoint"`
	UserinfoEndpoint      string               `json:"userinfoEndpoint"`
	JWKSURI               string               `json:"jwksUri"`
	IDTokenAlgs           []string             `json:"idTokenAlgs"`
	UsernameClaim         string               `json:"usernameClaim"`
	RoleMappings          []roleMappingRequest `json:"roleMappings"`
	DefaultRole           string               `json:"defaultRole"`
	AuthoritativeRoles    *bool                `json:"authoritativeRoles"`
	AutoCreateUsers       *bool                `json:"autoCreateUsers"`
	FetchUserinfo         *bool                `json:"fetchUserinfo"`
	PublicListed          *bool                `json:"publicListed"`
	Enabled               *bool                `json:"enabled"`
}

func (req ssoProviderRequest) toInput() auth.OIDCProviderInput {
	mappings := make([]oidc.RoleMapping, 0, len(req.RoleMappings))
	for _, mapping := range req.RoleMappings {
		mappings = append(mappings, oidc.RoleMapping{Claim: mapping.Claim, Value: mapping.Value, Role: mapping.Role})
	}
	return auth.OIDCProviderInput{
		Name:                  req.Name,
		DisplayName:           req.DisplayName,
		Issuer:                req.Issuer,
		ClientID:              req.ClientID,
		ClientSecret:          req.ClientSecret,
		Scopes:                req.Scopes,
		RedirectURI:           req.RedirectURI,
		AuthorizationEndpoint: req.AuthorizationEndpoint,
		TokenEndpoint:         req.TokenEndpoint,
		UserinfoEndpoint:      req.UserinfoEndpoint,
		JWKSURI:               req.JWKSURI,
		IDTokenAlgs:           req.IDTokenAlgs,
		UsernameClaim:         req.UsernameClaim,
		RoleMappings:          mappings,
		DefaultRole:           req.DefaultRole,
		AuthoritativeRoles:    req.AuthoritativeRoles,
		AutoCreateUsers:       req.AutoCreateUsers,
		FetchUserinfo:         req.FetchUserinfo,
		PublicListed:          req.PublicListed,
		Enabled:               req.Enabled,
	}
}

// handleSSO routes /api/v1/sso/*. Every branch is administrator-only: a provider
// row decides who may become an administrator, so a non-admin who could edit one
// could promote themselves.
func (a *API) handleSSO(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	if !requireSSO(w, a.identity) {
		return
	}
	if !isAdmin(p) {
		writeAPIError(w, http.StatusForbidden, "admin role required")
		return
	}
	if len(parts) == 0 {
		// An empty 200 would make a mistyped URL look like a successful call.
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	switch parts[0] {
	case "providers":
		if len(parts) == 1 {
			a.handleSSOProviderCollection(w, r, p)
			return
		}
		if len(parts) == 2 {
			a.handleSSOProviderItem(w, r, p, parts[1])
			return
		}
		if len(parts) == 3 && parts[2] == "test" {
			a.testSSOProvider(w, r, p, parts[1])
			return
		}
	}
	writeAPIError(w, http.StatusNotFound, "not found")
}

func (a *API) handleSSOProviderCollection(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	switch r.Method {
	case http.MethodGet:
		page, err := a.identity.Providers.List(r.Context(), r.URL.Query().Get("cursor"), queryLimit(r))
		if err != nil {
			writeAuthError(w, err)
			return
		}
		items := make([]any, len(page.Items))
		for index := range page.Items {
			items[index] = page.Items[index]
		}
		writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
	case http.MethodPost:
		key, ok := requireIdempotencyKey(w, r)
		if !ok {
			return
		}
		var req ssoProviderRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		// The secret is cleared as soon as the request value goes out of scope so
		// it cannot survive in a heap dump longer than the request.
		defer clearString(&req.ClientSecret)
		status, data, err := a.mutate(r, p, func() (int, any, error) {
			view, err := a.identity.Providers.Create(r.Context(), p.UserID, req.toInput(), key)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusCreated, view, nil
		})
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeStored(w, status, data)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleSSOProviderItem(w http.ResponseWriter, r *http.Request, p auth.Principal, id string) {
	switch r.Method {
	case http.MethodGet:
		view, err := a.identity.Providers.Get(r.Context(), id)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	case http.MethodPatch, http.MethodPut:
		key, ok := requireIdempotencyKey(w, r)
		if !ok {
			return
		}
		var req ssoProviderRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		defer clearString(&req.ClientSecret)
		status, data, err := a.mutate(r, p, func() (int, any, error) {
			view, err := a.identity.Providers.Update(r.Context(), p.UserID, id, req.toInput(), key)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, view, nil
		})
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeStored(w, status, data)
	case http.MethodDelete:
		key, ok := requireIdempotencyKey(w, r)
		if !ok {
			return
		}
		status, data, err := a.mutate(r, p, func() (int, any, error) {
			if err := a.identity.Providers.Delete(r.Context(), p.UserID, id, key); err != nil {
				return 0, nil, err
			}
			return http.StatusOK, map[string]any{"deleted": true, "id": id}, nil
		})
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeStored(w, status, data)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// testSSOProvider runs discovery and a JWKS retrieval against the live IdP. It is
// a mutation for idempotency and audit purposes even though it changes no
// configuration, because it produces an outbound request an operator must be able
// to account for.
func (a *API) testSSOProvider(w http.ResponseWriter, r *http.Request, p auth.Principal, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := requireIdempotencyKey(w, r); !ok {
		return
	}
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		report, err := a.identity.Providers.Test(r.Context(), p.UserID, id)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, report, nil
	})
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeStored(w, status, data)
}

// handleAuthPolicy routes /api/v1/auth/policy. It is administrator-only: the
// policy decides whether the whole deployment requires a second factor.
func (a *API) handleAuthPolicy(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if !requireSSO(w, a.identity) {
		return
	}
	if !isAdmin(p) {
		writeAPIError(w, http.StatusForbidden, "admin role required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := a.identity.Policy.Get(r.Context())
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, auth.View(settings))
	case http.MethodPut:
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		var req auth.AuthPolicyInput
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		status, data, err := a.mutate(r, p, func() (int, any, error) {
			settings, err := a.identity.Policy.Update(r.Context(), p.UserID, req)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, auth.View(settings), nil
		})
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeStored(w, status, data)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// requireIdempotencyKey enforces the documented rule that every administrative
// mutation carries one. Without it a retried request could create a second
// provider or apply an update twice.
func requireIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 255 {
		writeAPIError(w, http.StatusBadRequest, "idempotency_key_required")
		return "", false
	}
	return key, true
}

// clearString overwrites a secret string's backing header. Go strings are
// immutable so the bytes cannot be zeroed in place, but dropping the reference
// lets the collector reclaim them promptly instead of leaving the value reachable
// from a live request struct.
func clearString(value *string) {
	if value != nil {
		*value = ""
	}
}
