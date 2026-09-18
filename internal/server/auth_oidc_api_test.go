package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/auth/oidc"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// testIDP is a minimal OpenID provider over TLS. TLS is used rather than an
// insecure escape hatch because the production rule is "the issuer must be
// https", and a test that bypassed it would not be exercising the real path.
//
// Only RS256 is implemented here: the full algorithm allowlist, the JWKS refresh
// behaviour, and the claim validation rules are covered by the relying-party tests
// in internal/auth/oidc. These tests are about the HTTP surface.
type testIDP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string

	mu          sync.Mutex
	tokenStatus int
	tokenBody   string
	// idTokenOverride lets a case supply a hand-built assertion, for example one
	// whose subject the provider will not provision.
	idTokenOverride   string
	lastNonce         string
	lastCodeChallenge string
}

func newTestIDP(t *testing.T) *testIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &testIDP{key: key, kid: "rsa-1", tokenStatus: http.StatusOK}
	idp.server = httptest.NewTLSServer(http.HandlerFunc(idp.handle))
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *testIDP) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"):
		writeJSONBody(w, map[string]any{
			"issuer":                 idp.server.URL,
			"authorization_endpoint": idp.server.URL + "/authorize",
			"token_endpoint":         idp.server.URL + "/token",
			"userinfo_endpoint":      idp.server.URL + "/userinfo",
			"jwks_uri":               idp.server.URL + "/jwks.json",
		})
	case r.URL.Path == "/jwks.json":
		publicKey := idp.key.Public().(*rsa.PublicKey)
		writeJSONBody(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": idp.kid, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
		}}})
	case r.URL.Path == "/token":
		if err := r.ParseForm(); err != nil {
			t := http.StatusBadRequest
			w.WriteHeader(t)
			return
		}
		idp.mu.Lock()
		status, body, override := idp.tokenStatus, idp.tokenBody, idp.idTokenOverride
		idp.mu.Unlock()
		if override == "" {
			_ = body
			subject := "idp-subject-1"
			idToken, err := idp.signIDToken(subject, "sso-user", idp.lastNonce)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			payload, _ := json.Marshal(map[string]any{
				"access_token": "access-token", "token_type": "Bearer", "expires_in": 3600, "id_token": idToken,
			})
			body = string(payload)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeJSONBody(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (idp *testIDP) setTokenResponse(status int, body string) {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	idp.tokenStatus, idp.tokenBody = status, body
}

// signIDToken mints an RS256 assertion carrying the claims the provider maps a
// username and a role from.
func (idp *testIDP) signIDToken(subject, username, nonce string) (string, error) {
	now := time.Now().Unix()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": idp.kid}
	payload := map[string]any{
		"iss": idp.server.URL, "aud": "tunnelmesh-console", "sub": subject,
		"exp": now + 300, "iat": now, "nbf": now - 5,
		"preferred_username": username, "email": username + "@corp.example.com",
		"groups": []string{"tm-users"},
	}
	if nonce != "" {
		payload["nonce"] = nonce
	}
	encodedHeader, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(encodedHeader) + "." + base64.RawURLEncoding.EncodeToString(encodedPayload)
	digest := sha256.Sum256([]byte(signingInput))
	// The hash algorithm must be declared to the signer: passing 0 would produce
	// a bare PKCS#1 signature over the digest with no DigestInfo prefix, which is
	// not RS256 and would be rejected by any conforming verifier.
	signature, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// oidcFixture is an API with the identity services installed and one provider
// registered against the local identity provider.
type oidcFixture struct {
	*identityFixture
	idp        *testIDP
	providerID string
}

func newOIDCFixture(t *testing.T, cfg config.AuthConfig) *oidcFixture {
	t.Helper()
	api, admin, user := apiTestServer(t)
	idp := newTestIDP(t)
	services := installTestIdentityWith(t, api, auth.NewAuthService(api.DB), cfg, IdentityRuntimeConfig{
		HTTPClient: idp.server.Client(),
	})
	// Every service shares the fixture clock so a state or ticket cannot expire
	// between the moment it is minted and the moment it is presented.
	fixture := &identityFixture{api: api, db: api.DB, admin: admin, user: user, now: time.Now().UTC()}
	services.MFA.SetClock(func() time.Time { return fixture.now })
	services.Logins.SetClock(func() time.Time { return fixture.now })
	services.Devices.SetClock(func() time.Time { return fixture.now })
	services.Identities.SetClock(func() time.Time { return fixture.now })
	services.Challenges.SetClock(func() time.Time { return fixture.now })
	services.Providers.SetClock(func() time.Time { return fixture.now })

	view, err := services.Providers.Create(context.Background(), admin.ID, auth.OIDCProviderInput{
		Name:         "corpid",
		DisplayName:  "Corp IdP",
		Issuer:       idp.server.URL,
		ClientID:     "tunnelmesh-console",
		ClientSecret: "sup3r-s3cr3t",
		Scopes:       []string{"openid", "profile", "email"},
		// The callback base is inside the allowlist the fixture installs; the
		// provider name in the path is what the router matches on.
		RedirectURI:        "https://tm.example.com/api/v1/auth/oidc/corpid/callback",
		IDTokenAlgs:        []string{"RS256"},
		UsernameClaim:      "preferred_username",
		RoleMappings:       []oidc.RoleMapping{{Claim: "groups", Value: "tm-admins", Role: "admin"}},
		DefaultRole:        "user",
		AutoCreateUsers:    boolPointer(true),
		AuthoritativeRoles: boolPointer(true),
		PublicListed:       boolPointer(true),
		Enabled:            boolPointer(true),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	return &oidcFixture{identityFixture: fixture, idp: idp, providerID: view.ID}
}

func boolPointer(value bool) *bool { return &value }

// oidcRequest issues a request without following redirects, which is what the
// authorize and callback assertions need.
func oidcRequest(t *testing.T, handler http.Handler, method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// startOIDCLogin follows GET /auth/oidc/{name}/authorize and returns the state the
// handler stored, which the callback must present.
func (f *oidcFixture) startOIDCLogin(t *testing.T) (string, *httptest.ResponseRecorder) {
	t.Helper()
	response := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/corpid/authorize")
	if response.Code != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302: %s", response.Code, response.Body.String())
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := location.Query().Get("state")
	if state == "" {
		t.Fatalf("authorize redirect carries no state: %s", location.String())
	}
	// The relying party signs the assertion against the nonce it put in the
	// authorization request, so the test IdP has to echo the same value back.
	f.idp.mu.Lock()
	f.idp.lastNonce = location.Query().Get("nonce")
	f.idp.lastCodeChallenge = location.Query().Get("code_challenge")
	f.idp.mu.Unlock()
	return state, response
}

// TestOIDCPublicProviderListIsGatedAndMinimal proves the login page can render SSO
// buttons without disclosing anything beyond a label, and that the listing can be
// turned off entirely.
func TestOIDCPublicProviderListIsGatedAndMinimal(t *testing.T) {
	f := newOIDCFixture(t, config.DefaultAuthConfig())

	response := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/providers")
	if response.Code != http.StatusOK {
		t.Fatalf("provider list status = %d: %s", response.Code, response.Body.String())
	}
	items, _ := envelopeData(t, response)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("providers = %d, want 1: %s", len(items), response.Body.String())
	}
	entry := items[0].(map[string]any)
	for _, allowed := range []string{"id", "name", "displayName"} {
		if _, present := entry[allowed]; !present {
			t.Fatalf("public provider is missing %q: %s", allowed, response.Body.String())
		}
	}
	// Nothing that would let a caller complete a flow or read the configuration.
	for _, forbidden := range []string{"issuer", "clientId", "clientSecret", "redirectUri", "jwksUri", "tokenEndpoint", "hasSecret"} {
		if _, present := entry[forbidden]; present {
			t.Fatalf("public provider list exposed %q: %s", forbidden, response.Body.String())
		}
	}

	// With the global switch off the route is indistinguishable from one that
	// never existed.
	hidden := config.DefaultAuthConfig()
	hidden.OIDC.PublicProviders = false
	off := newOIDCFixture(t, hidden)
	if response := oidcRequest(t, off.handler(), http.MethodGet, "/api/v1/auth/oidc/providers"); response.Code != http.StatusNotFound {
		t.Fatalf("gated provider list status = %d, want 404", response.Code)
	}
}

// TestOIDCAuthorizeRedirectCarriesStateNonceAndPKCE proves the authorization
// request is complete and uncacheable, and that an unknown or disabled provider is
// not distinguishable from one that never existed.
func TestOIDCAuthorizeRedirectCarriesStateNonceAndPKCE(t *testing.T) {
	f := newOIDCFixture(t, config.DefaultAuthConfig())

	state, response := f.startOIDCLogin(t)
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store on a URL carrying a single-use state", got)
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(location.String(), f.idp.server.URL) {
		t.Fatalf("authorize redirect = %q, want the IdP authorization endpoint", location.String())
	}
	query := location.Query()
	if query.Get("nonce") == "" {
		t.Fatal("authorize redirect carries no nonce")
	}
	if query.Get("code_challenge") == "" {
		t.Fatal("authorize redirect carries no PKCE challenge")
	}
	if query.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", query.Get("code_challenge_method"))
	}
	if query.Get("redirect_uri") == "" {
		t.Fatal("authorize redirect carries no redirect_uri")
	}
	if !strings.Contains(query.Get("scope"), "openid") {
		t.Fatalf("scope = %q, want it to include openid", query.Get("scope"))
	}
	if query.Get("response_type") != "code" {
		t.Fatalf("response_type = %q, want code", query.Get("response_type"))
	}
	if len(state) < 32 {
		t.Fatalf("state = %q, want a high-entropy single-use value", state)
	}

	if unknown := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/nosuch/authorize"); unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d, want 404", unknown.Code)
	}

	// A disabled provider is refused, and the refusal does not say whether the
	// provider exists.
	services := f.api.IdentityServices()
	if _, err := services.Providers.Update(context.Background(), f.admin.ID, f.providerID, auth.OIDCProviderInput{Enabled: boolPointer(false)}, ""); err != nil {
		t.Fatal(err)
	}
	if disabled := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/corpid/authorize"); disabled.Code != http.StatusNotFound {
		t.Fatalf("disabled provider status = %d, want 404: %s", disabled.Code, disabled.Body.String())
	}
}

// TestOIDCCallbackIssuesSingleUseTicketAndExchangesIt covers the happy path end to
// end: the redirect hands the browser a ticket on a relative path, and posting
// that ticket back yields a session.
func TestOIDCCallbackIssuesSingleUseTicketAndExchangesIt(t *testing.T) {
	f := newOIDCFixture(t, config.DefaultAuthConfig())
	state, _ := f.startOIDCLogin(t)

	callback := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/corpid/callback?code=auth-code&state="+url.QueryEscape(state))
	if callback.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302: %s", callback.Code, callback.Body.String())
	}
	if got := callback.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store on a redirect carrying a ticket", got)
	}
	location := callback.Header().Get("Location")
	assertRelativeLoginRedirect(t, location)
	ticket := loginRedirectValue(t, location, "ticket")
	if ticket == "" {
		t.Fatalf("callback redirect carries no ticket: %s", location)
	}

	// The ticket is a bearer credential. It must not reach the audit log, which is
	// readable by every administrator and exportable.
	if details := auditDetailsString(t, f.db); strings.Contains(details, ticket) {
		t.Fatalf("the login ticket was written to the audit log: %s", details)
	}

	first := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/oidc/exchange", "", "", map[string]any{"ticket": ticket})
	if first.Code != http.StatusOK {
		t.Fatalf("exchange status = %d: %s", first.Code, first.Body.String())
	}
	data := envelopeData(t, first)
	if issued, _ := data["token"].(string); issued == "" {
		t.Fatalf("exchange response has no token: %s", first.Body.String())
	}
	user, _ := data["user"].(map[string]any)
	if user["username"] != "sso-user" {
		t.Fatalf("provisioned username = %v, want sso-user: %s", user["username"], first.Body.String())
	}
	if user["authSource"] != string(storage.AuthSourceOIDC) {
		t.Fatalf("authSource = %v, want oidc", user["authSource"])
	}

	// The ticket is single-use: a captured redirect cannot be replayed.
	second := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/oidc/exchange", "", "", map[string]any{"ticket": ticket})
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("replayed exchange status = %d, want 401: %s", second.Code, second.Body.String())
	}
	if got := errorCode(t, second); got != "login_ticket_invalid" {
		t.Fatalf("data.error = %q, want login_ticket_invalid", got)
	}
}

// assertRelativeLoginRedirect proves the callback only ever redirects to the
// console's own login route. An absolute or attacker-influenced target would make
// the callback an open redirect on an origin users already trust.
func assertRelativeLoginRedirect(t *testing.T, location string) {
	t.Helper()
	if !strings.HasPrefix(location, "/login") {
		t.Fatalf("redirect Location = %q, want a relative /login path", location)
	}
	if strings.Contains(location, "//") || strings.Contains(location, "\\") {
		t.Fatalf("redirect Location = %q must not be a protocol-relative URL", location)
	}
	if parsed, err := url.Parse(location); err != nil || parsed.IsAbs() || parsed.Host != "" {
		t.Fatalf("redirect Location = %q parsed as absolute (%v)", location, err)
	}
}

func loginRedirectValue(t *testing.T, location, key string) string {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query().Get(key)
}

// TestOIDCCallbackRedirectIsRelativeEvenWithAForeignRedirectURI proves the stored
// provider configuration cannot influence where the browser is sent.
func TestOIDCCallbackRedirectIsRelativeEvenWithAForeignRedirectURI(t *testing.T) {
	f := newOIDCFixture(t, config.DefaultAuthConfig())
	services := f.api.IdentityServices()
	// A second provider whose callback base is still allowed, but whose stored
	// redirect URI names a host this deployment does not serve.
	_, err := services.Providers.Create(context.Background(), f.admin.ID, auth.OIDCProviderInput{
		Name: "foreign", DisplayName: "Foreign", Issuer: f.idp.server.URL,
		ClientID: "tunnelmesh-console", ClientSecret: "sup3r-s3cr3t",
		Scopes: []string{"openid"}, RedirectURI: "https://tm.example.com/elsewhere",
		IDTokenAlgs: []string{"RS256"}, UsernameClaim: "preferred_username", DefaultRole: "user",
		AutoCreateUsers: boolPointer(true), PublicListed: boolPointer(true), Enabled: boolPointer(true),
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	response := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/foreign/authorize")
	if response.Code != http.StatusFound {
		t.Fatalf("authorize status = %d: %s", response.Code, response.Body.String())
	}
	state := loginRedirectValue(t, response.Header().Get("Location"), "state")
	f.idp.mu.Lock()
	f.idp.lastNonce = loginRedirectValue(t, response.Header().Get("Location"), "nonce")
	f.idp.mu.Unlock()

	callback := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/foreign/callback?code=auth-code&state="+url.QueryEscape(state))
	if callback.Code != http.StatusFound {
		t.Fatalf("callback status = %d: %s", callback.Code, callback.Body.String())
	}
	assertRelativeLoginRedirect(t, callback.Header().Get("Location"))
	if strings.Contains(callback.Header().Get("Location"), "elsewhere") {
		t.Fatalf("the redirect followed the provider configuration: %s", callback.Header().Get("Location"))
	}
}

// TestOIDCCallbackRequiresMFAWhenPolicyRequires proves the redirect hands the
// console an MFA challenge instead of a ticket when a second factor is owed.
func TestOIDCCallbackRequiresMFAWhenPolicyRequires(t *testing.T) {
	f := newOIDCFixture(t, config.DefaultAuthConfig())

	// Provision the account first, then enroll MFA on it and require the factor.
	state, _ := f.startOIDCLogin(t)
	first := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/corpid/callback?code=auth-code&state="+url.QueryEscape(state))
	ticket := loginRedirectValue(t, first.Header().Get("Location"), "ticket")
	exchanged := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/oidc/exchange", "", "", map[string]any{"ticket": ticket})
	ssoToken := envelopeData(t, exchanged)["token"].(string)
	ssoUserID := envelopeData(t, exchanged)["user"].(map[string]any)["id"].(string)

	enroll := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/enroll", ssoToken, "", nil)
	if enroll.Code != http.StatusOK {
		t.Fatalf("enroll status = %d: %s", enroll.Code, enroll.Body.String())
	}
	secret := envelopeData(t, enroll)["secret"].(string)
	if enable := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/enable", ssoToken, "", map[string]any{"code": f.totp(t, secret)}); enable.Code != http.StatusOK {
		t.Fatalf("enable status = %d: %s", enable.Code, enable.Body.String())
	}
	f.advance(30 * time.Second)
	setAccountMFARequired(t, f.db, ssoUserID, true)
	setMFAMode(t, f.db, storage.MFAModeRequired, false)

	state, _ = f.startOIDCLogin(t)
	callback := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/corpid/callback?code=auth-code&state="+url.QueryEscape(state))
	if callback.Code != http.StatusFound {
		t.Fatalf("callback status = %d: %s", callback.Code, callback.Body.String())
	}
	location := callback.Header().Get("Location")
	assertRelativeLoginRedirect(t, location)
	challengeID := loginRedirectValue(t, location, "mfa")
	if challengeID == "" {
		t.Fatalf("callback did not hand over an MFA challenge: %s", location)
	}
	if loginRedirectValue(t, location, "ticket") != "" {
		t.Fatalf("a session ticket was issued although a second factor is owed: %s", location)
	}

	// The challenge completes into a session through the same endpoint a password
	// login uses.
	verify := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/mfa/verify", "", "", map[string]any{
		"challengeId": challengeID, "code": f.totp(t, secret),
	})
	if verify.Code != http.StatusOK {
		t.Fatalf("verify status = %d: %s", verify.Code, verify.Body.String())
	}
	if issued, _ := envelopeData(t, verify)["token"].(string); issued == "" {
		t.Fatalf("verify response has no token: %s", verify.Body.String())
	}
}

// TestOIDCCallbackRejectsBadStateAndProviderError proves every way a redirect can
// arrive wrong is refused with a stable code, without a redirect and without
// echoing IdP-controlled text.
func TestOIDCCallbackRejectsBadStateAndProviderError(t *testing.T) {
	f := newOIDCFixture(t, config.DefaultAuthConfig())

	cases := []struct {
		name   string
		target string
		code   string
	}{
		{name: "unknown state", target: "/api/v1/auth/oidc/corpid/callback?code=auth-code&state=nobody-issued-this", code: "oidc_state_invalid"},
		{name: "missing state", target: "/api/v1/auth/oidc/corpid/callback?code=auth-code", code: "oidc_state_invalid"},
		{name: "missing code", target: "/api/v1/auth/oidc/corpid/callback?state=x", code: "oidc_state_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := oidcRequest(t, f.handler(), http.MethodGet, tc.target)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
			}
			if got := errorCode(t, response); got != tc.code {
				t.Fatalf("data.error = %q, want %q", got, tc.code)
			}
			if location := response.Header().Get("Location"); location != "" {
				t.Fatalf("a refused callback redirected to %q", location)
			}
		})
	}

	t.Run("state cannot be replayed", func(t *testing.T) {
		state, _ := f.startOIDCLogin(t)
		target := "/api/v1/auth/oidc/corpid/callback?code=auth-code&state=" + url.QueryEscape(state)
		if first := oidcRequest(t, f.handler(), http.MethodGet, target); first.Code != http.StatusFound {
			t.Fatalf("first callback status = %d: %s", first.Code, first.Body.String())
		}
		second := oidcRequest(t, f.handler(), http.MethodGet, target)
		if second.Code != http.StatusBadRequest {
			t.Fatalf("replayed state status = %d, want 400: %s", second.Code, second.Body.String())
		}
		if got := errorCode(t, second); got != "oidc_state_invalid" {
			t.Fatalf("data.error = %q, want oidc_state_invalid", got)
		}
	})

	t.Run("state is bound to its provider", func(t *testing.T) {
		state, _ := f.startOIDCLogin(t)
		// Presenting a corpid state to a different provider's callback is refused,
		// so a state captured on one flow cannot complete another.
		response := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/nosuch/callback?code=auth-code&state="+url.QueryEscape(state))
		if response.Code == http.StatusFound {
			t.Fatalf("a cross-provider state was accepted: %s", response.Header().Get("Location"))
		}
	})

	t.Run("provider error is not echoed", func(t *testing.T) {
		marker := "reflected-<script>-marker"
		response := oidcRequest(t, f.handler(), http.MethodGet,
			"/api/v1/auth/oidc/corpid/callback?error=access_denied&error_description="+url.QueryEscape(marker))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
		}
		if got := errorCode(t, response); got != "oidc_provider_error" {
			t.Fatalf("data.error = %q, want oidc_provider_error", got)
		}
		if strings.Contains(response.Body.String(), marker) {
			t.Fatalf("the IdP error_description was reflected: %s", response.Body.String())
		}
		if response.Header().Get("Location") != "" {
			t.Fatal("a refused callback redirected")
		}
	})
}

// TestOIDCCallbackTokenExchangeFailureIsReportedAsUpstream proves a broken IdP is
// reported as a bad gateway with a stable code rather than as an internal error.
func TestOIDCCallbackTokenExchangeFailureIsReportedAsUpstream(t *testing.T) {
	f := newOIDCFixture(t, config.DefaultAuthConfig())
	state, _ := f.startOIDCLogin(t)
	f.idp.setTokenResponse(http.StatusBadRequest, `{"error":"invalid_grant"}`)

	response := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/corpid/callback?code=auth-code&state="+url.QueryEscape(state))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", response.Code, response.Body.String())
	}
	if got := errorCode(t, response); !strings.HasPrefix(got, "oidc_") {
		t.Fatalf("data.error = %q, want a stable oidc_ code", got)
	}
}

// TestOIDCExchangeRejectsAnUnknownTicket proves the exchange endpoint cannot be
// used to mint a session from a guessed value.
func TestOIDCExchangeRejectsAnUnknownTicket(t *testing.T) {
	f := newOIDCFixture(t, config.DefaultAuthConfig())
	response := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/oidc/exchange", "", "", map[string]any{"ticket": "not-a-ticket"})
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", response.Code, response.Body.String())
	}
	if got := errorCode(t, response); got != "login_ticket_invalid" {
		t.Fatalf("data.error = %q, want login_ticket_invalid", got)
	}
	if malformed := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/oidc/exchange", "", "", `not-json`); malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", malformed.Code)
	}
}

// TestOIDCProvisioningRefusesARoleEscalation proves the role mapping cannot be
// used to mint an administrator from a claim the IdP does not actually carry.
func TestOIDCProvisioningRefusesARoleEscalation(t *testing.T) {
	f := newOIDCFixture(t, config.DefaultAuthConfig())
	state, _ := f.startOIDCLogin(t)

	callback := oidcRequest(t, f.handler(), http.MethodGet, "/api/v1/auth/oidc/corpid/callback?code=auth-code&state="+url.QueryEscape(state))
	if callback.Code != http.StatusFound {
		t.Fatalf("callback status = %d: %s", callback.Code, callback.Body.String())
	}
	ticket := loginRedirectValue(t, callback.Header().Get("Location"), "ticket")
	exchanged := apiJSON(t, f.handler(), http.MethodPost, "/api/v1/auth/oidc/exchange", "", "", map[string]any{"ticket": ticket})
	if exchanged.Code != http.StatusOK {
		t.Fatalf("exchange status = %d: %s", exchanged.Code, exchanged.Body.String())
	}
	user := envelopeData(t, exchanged)["user"].(map[string]any)
	// The assertion carried groups=[tm-users], which maps to nothing, so the
	// provider default applies rather than the admin mapping.
	if user["role"] != "user" {
		t.Fatalf("provisioned role = %v, want the provider default user", user["role"])
	}
	if user["id"] == f.admin.ID {
		t.Fatal("provisioning reused the bootstrap administrator account")
	}
}

// auditDetailsString concatenates every audit detail blob so a test can assert a
// credential never reached the log.
func auditDetailsString(t *testing.T, db *storage.DB) string {
	t.Helper()
	page, err := db.Audits().List(context.Background(), storage.AuditFilter{}, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	var builder strings.Builder
	for _, entry := range page.Items {
		builder.WriteString(entry.Action)
		builder.WriteString(" ")
		builder.WriteString(entry.Details)
		builder.WriteString("\n")
	}
	return builder.String()
}

// setAccountMFARequired flips the per-account second-factor override through the
// same writer the administrator API uses, so a test cannot drift from production
// behaviour.
func setAccountMFARequired(t *testing.T, db *storage.DB, userID string, required bool) {
	t.Helper()
	ctx := context.Background()
	err := db.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		return repos.UserIdentity.SetMFARequired(ctx, userID, required)
	})
	if err != nil {
		t.Fatal(err)
	}
}
