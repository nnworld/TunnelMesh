package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// ssoProviderBody renders a valid create or update body. The redirect URI must sit
// inside the base installTestIdentityWith allows, and the secret value is a literal
// the assertions search for so a leak is unambiguous.
func ssoProviderBody(name string) map[string]any {
	return map[string]any{
		"name":          name,
		"displayName":   "Corporate SSO",
		"issuer":        "https://idp.example.com",
		"clientId":      "tunnelmesh-console",
		"clientSecret":  ssoTestClientSecret,
		"scopes":        []string{"openid", "profile", "email"},
		"redirectUri":   "https://tm.example.com/api/v1/auth/oidc/" + name + "/callback",
		"idTokenAlgs":   []string{"RS256"},
		"usernameClaim": "preferred_username",
		"roleMappings":  []map[string]any{{"claim": "groups", "value": "tm-admins", "role": "admin"}},
		"defaultRole":   "user",
	}
}

const ssoTestClientSecret = "sso-smoke-client-secret-value"

// createSSOProvider posts a provider and returns its identifier.
func createSSOProvider(t *testing.T, f *identityFixture, token, name, key string) string {
	t.Helper()
	response := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/sso/providers", token, key, ssoProviderBody(name))
	if response.Code != http.StatusCreated {
		t.Fatalf("create provider status = %d: %s", response.Code, response.Body.String())
	}
	id, _ := envelopeData(t, response)["id"].(string)
	if id == "" {
		t.Fatalf("create provider returned no id: %s", response.Body.String())
	}
	return id
}

// TestSSOProviderRoundTripsRoleMappingsWithRequestCasing pins the read model to the
// same key casing the write model accepts. The console submits claim/value/role and
// renders what it reads back, so a response that capitalizes those keys silently
// empties the role-mapping editor.
func TestSSOProviderRoundTripsRoleMappingsWithRequestCasing(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "admin", "admin-pass")
	id := createSSOProvider(t, f, token, "corp", "idem-role-mappings")

	response := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/sso/providers/"+id, token, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("get provider status = %d: %s", response.Code, response.Body.String())
	}
	mappings, ok := envelopeData(t, response)["roleMappings"].([]any)
	if !ok || len(mappings) != 1 {
		t.Fatalf("roleMappings = %v, want exactly one mapping", envelopeData(t, response)["roleMappings"])
	}
	first, ok := mappings[0].(map[string]any)
	if !ok {
		t.Fatalf("roleMappings[0] is not an object: %v", mappings[0])
	}
	for key, want := range map[string]string{"claim": "groups", "value": "tm-admins", "role": "admin"} {
		got, present := first[key]
		if !present {
			t.Errorf("roleMappings[0] is missing the %q key; got keys %v", key, first)
			continue
		}
		if got != want {
			t.Errorf("roleMappings[0].%s = %v, want %q", key, got, want)
		}
	}
}

// TestSSOProviderCollectionIsAdminOnly covers the authorization boundary: a normal
// user must not be able to read or create identity providers.
func TestSSOProviderCollectionIsAdminOnly(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	userToken := f.token(t, "alice", "alice-pass")

	list := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/sso/providers", userToken, "", nil)
	if list.Code != http.StatusForbidden {
		t.Errorf("list as user status = %d, want 403: %s", list.Code, list.Body.String())
	}
	create := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/sso/providers", userToken, "idem-user", ssoProviderBody("corp"))
	if create.Code != http.StatusForbidden {
		t.Errorf("create as user status = %d, want 403: %s", create.Code, create.Body.String())
	}
	anonymous := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/sso/providers", "", "", nil)
	if anonymous.Code != http.StatusUnauthorized {
		t.Errorf("list anonymously status = %d, want 401: %s", anonymous.Code, anonymous.Body.String())
	}
}

// TestSSOProviderNeverReturnsClientSecret proves the secret is write-only. It is
// sealed at rest, so no read path may echo it back, and hasSecret is the only
// signal a client receives.
func TestSSOProviderNeverReturnsClientSecret(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "admin", "admin-pass")

	created := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/sso/providers", token, "idem-secret", ssoProviderBody("corp"))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), ssoTestClientSecret) {
		t.Errorf("create response leaked the client secret: %s", created.Body.String())
	}
	if hasSecret, _ := envelopeData(t, created)["hasSecret"].(bool); !hasSecret {
		t.Errorf("create response hasSecret = false, want true")
	}

	id := envelopeData(t, created)["id"].(string)
	for _, path := range []string{"/api/v1/sso/providers/" + id, "/api/v1/sso/providers"} {
		response := apiJSON(t, f.handler(), http.MethodGet, path, token, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d: %s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), ssoTestClientSecret) {
			t.Errorf("GET %s leaked the client secret: %s", path, response.Body.String())
		}
	}
}

// TestSSOProviderMutationRequiresIdempotencyKey covers the documented rule that
// every administrative identity mutation carries one.
func TestSSOProviderMutationRequiresIdempotencyKey(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "admin", "admin-pass")

	response := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/sso/providers", token, "", ssoProviderBody("corp"))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("create without key status = %d, want 400: %s", response.Code, response.Body.String())
	}
	if code := errorCode(t, response); code != "idempotency_key_required" {
		t.Errorf("data.error = %q, want idempotency_key_required", code)
	}
}

// TestSSOProviderIdempotencyReplayReturnsStoredResponse proves a retried create is
// not applied twice, which is the whole point of the key.
func TestSSOProviderIdempotencyReplayReturnsStoredResponse(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "admin", "admin-pass")

	first := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/sso/providers", token, "idem-replay", ssoProviderBody("corp"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first create status = %d: %s", first.Code, first.Body.String())
	}
	replay := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/sso/providers", token, "idem-replay", ssoProviderBody("corp"))
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay status = %d, want the stored 201: %s", replay.Code, replay.Body.String())
	}
	if envelopeData(t, first)["id"] != envelopeData(t, replay)["id"] {
		t.Errorf("replay created a second provider: %v vs %v", envelopeData(t, first)["id"], envelopeData(t, replay)["id"])
	}
	list := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/sso/providers", token, "", nil)
	items, _ := envelopeData(t, list)["items"].([]any)
	if len(items) != 1 {
		t.Errorf("provider count = %d, want 1 after a replay", len(items))
	}
}

// TestSSOProviderIdempotencyKeyOwnedByAnotherAdminIsConflict covers key reuse across
// principals. It must be a 409 the console can explain, not a 500 that reads as a
// server defect.
func TestSSOProviderIdempotencyKeyOwnedByAnotherAdminIsConflict(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "admin", "admin-pass")
	if _, err := auth.NewAuthService(f.db).CreateUser(context.Background(), "root2", "root2-pass", "admin"); err != nil {
		t.Fatal(err)
	}
	otherToken := f.token(t, "root2", "root2-pass")

	if response := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/sso/providers", token, "idem-shared", ssoProviderBody("corp")); response.Code != http.StatusCreated {
		t.Fatalf("first admin create status = %d: %s", response.Code, response.Body.String())
	}
	response := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/sso/providers", otherToken, "idem-shared", ssoProviderBody("other"))
	if response.Code != http.StatusConflict {
		t.Fatalf("cross-admin key reuse status = %d, want 409: %s", response.Code, response.Body.String())
	}
}

// TestSSOProviderRejectsReservedName prevents an operator from creating a provider
// that the public OIDC router can never address, because the literal "providers"
// segment is matched before the provider name.
func TestSSOProviderRejectsReservedName(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "admin", "admin-pass")

	response := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/sso/providers", token, "idem-reserved", ssoProviderBody("providers"))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("reserved name status = %d, want 400: %s", response.Code, response.Body.String())
	}
}

// TestBareSSORouteIsNotFound covers the empty collection path. Returning a 200 with
// no body makes a mistyped URL look like a successful call.
func TestBareSSORouteIsNotFound(t *testing.T) {
	f := newIdentityFixture(t, config.DefaultAuthConfig())
	token := f.token(t, "admin", "admin-pass")

	response := apiJSON(t, f.handler(), http.MethodGet, "/api/v1/sso", token, "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/sso status = %d, want 404: %s", response.Code, response.Body.String())
	}
}

// TestAuthErrorStatusMapsAccountDisabledToForbidden pins the mapping for a disabled
// account. It is returned by the OIDC provisioning path, where a 500 would tell the
// operator the server is broken instead of telling them the account is off.
func TestAuthErrorStatusMapsAccountDisabledToForbidden(t *testing.T) {
	status, code := authErrorStatus(auth.ErrAccountDisabled)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", status)
	}
	if code != "account_disabled" {
		t.Errorf("code = %q, want account_disabled", code)
	}
	// An unrelated error must still fall through to the opaque 500, so the new
	// branch cannot widen into a catch-all.
	status, code = authErrorStatus(errUnrelatedForMapping)
	if status != http.StatusInternalServerError || code != "internal_error" {
		t.Errorf("unrelated error mapped to %d %q, want 500 internal_error", status, code)
	}
}

// errUnrelatedForMapping is an error no sentinel matches.
var errUnrelatedForMapping = errors.New("something unexpected happened")
