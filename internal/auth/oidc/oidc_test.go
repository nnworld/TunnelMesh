package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testIDP is a minimal OpenID provider over TLS. TLS is used rather than an
// insecure escape hatch because the production rule is "issuer must be https"
// and a test that bypasses it would not be testing the real path.
type testIDP struct {
	t        *testing.T
	server   *httptest.Server
	rp       *RelyingParty
	provider Provider

	rsaKey   *rsa.PrivateKey
	ecKey    *ecdsa.PrivateKey
	ec384Key *ecdsa.PrivateKey
	ec521Key *ecdsa.PrivateKey
	edKey    ed25519.PrivateKey

	rsaKid   string
	ecKid    string
	ec384Kid string
	ec521Kid string
	edKid    string

	jwksRequests  atomic.Int64
	tokenRequests atomic.Int64

	mu          sync.Mutex
	tokenStatus int
	tokenBody   string
	omitJWKSURI bool
	badJWKS     bool
}

func newTestIDP(t *testing.T, cfg Config) *testIDP {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ec384Key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ec521Key, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	idp := &testIDP{t: t, rsaKey: rsaKey, ecKey: ecKey, ec384Key: ec384Key, ec521Key: ec521Key, edKey: edKey,
		rsaKid: "rsa-1", ecKid: "ec-1", ec384Kid: "ec-384", ec521Kid: "ec-521", edKid: "ed-1", tokenStatus: http.StatusOK}
	idp.server = httptest.NewTLSServer(http.HandlerFunc(idp.handle))
	t.Cleanup(idp.server.Close)
	idp.provider = Provider{
		ID:            "prov-1",
		Name:          "corpid",
		DisplayName:   "Corp IdP",
		Issuer:        idp.server.URL,
		ClientID:      "tunnelmesh-console",
		Scopes:        []string{"openid", "profile", "email"},
		RedirectURI:   "https://tm.example.com/api/v1/auth/oidc/corpid/callback",
		IDTokenAlgs:   []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"},
		UsernameClaim: "preferred_username",
		DefaultRole:   "user",
	}
	// NewRelyingParty fills any unset field, so a test that sets exactly one knob
	// keeps that knob instead of having the whole configuration replaced.
	// The httptest client trusts the server certificate; a timeout is set so a
	// hung handler fails the test instead of hanging it.
	client := idp.server.Client()
	client.Timeout = 5 * time.Second
	idp.rp = NewRelyingParty(client, cfg, nil)
	return idp
}

func (idp *testIDP) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, wellKnownPath):
		idp.mu.Lock()
		omit := idp.omitJWKSURI
		idp.mu.Unlock()
		doc := map[string]any{
			"issuer":                 idp.server.URL,
			"authorization_endpoint": idp.server.URL + "/authorize",
			"token_endpoint":         idp.server.URL + "/token",
			"userinfo_endpoint":      idp.server.URL + "/userinfo",
		}
		if !omit {
			doc["jwks_uri"] = idp.server.URL + "/jwks.json"
		}
		writeJSON(w, doc)
	case r.URL.Path == "/jwks.json":
		idp.jwksRequests.Add(1)
		idp.mu.Lock()
		bad := idp.badJWKS
		idp.mu.Unlock()
		if bad {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"keys": idp.jwks()})
	case r.URL.Path == "/token":
		idp.tokenRequests.Add(1)
		idp.mu.Lock()
		status, body := idp.tokenStatus, idp.tokenBody
		idp.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (idp *testIDP) setTokenResponse(status int, body string) {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	idp.tokenStatus, idp.tokenBody = status, body
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func b64(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

func (idp *testIDP) jwks() []map[string]any {
	rsaPub := idp.rsaKey.Public().(*rsa.PublicKey)
	return []map[string]any{
		{"kty": "RSA", "kid": idp.rsaKid, "use": "sig", "alg": "RS256", "n": b64(rsaPub.N.Bytes()), "e": b64(big.NewInt(int64(rsaPub.E)).Bytes())},
		{"kty": "EC", "kid": idp.ecKid, "use": "sig", "alg": "ES256", "crv": "P-256", "x": b64(idp.ecKey.X.Bytes()), "y": b64(idp.ecKey.Y.Bytes())},
		{"kty": "EC", "kid": idp.ec384Kid, "use": "sig", "alg": "ES384", "crv": "P-384", "x": b64(idp.ec384Key.X.Bytes()), "y": b64(idp.ec384Key.Y.Bytes())},
		{"kty": "EC", "kid": idp.ec521Kid, "use": "sig", "alg": "ES512", "crv": "P-521", "x": b64(padLeft(idp.ec521Key.X.Bytes(), curveSize(idp.ec521Key.Curve))), "y": b64(padLeft(idp.ec521Key.Y.Bytes(), curveSize(idp.ec521Key.Curve)))},
		{"kty": "OKP", "kid": idp.edKid, "use": "sig", "alg": "EdDSA", "crv": "Ed25519", "x": b64(idp.edKey.Public().(ed25519.PublicKey))},
		{"kty": "oct", "kid": "sym-1", "use": "enc", "k": b64([]byte("ignored"))},
	}
}

// signToken mints a JWS with the requested algorithm. It supports the full
// accepted set so the allowlist can be exercised per algorithm.
func (idp *testIDP) signToken(t *testing.T, alg, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": alg, "typ": "JWT"}
	if kid != "" {
		header["kid"] = kid
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := b64(headerJSON) + "." + b64(payloadJSON)
	var signature []byte
	switch {
	case strings.HasPrefix(alg, "RS"):
		digest := digest(t, alg, []byte(signingInput))
		signature, err = rsa.SignPKCS1v15(rand.Reader, idp.rsaKey, hashFor(alg), digest)
	case strings.HasPrefix(alg, "PS"):
		digest := digest(t, alg, []byte(signingInput))
		signature, err = rsa.SignPSS(rand.Reader, idp.rsaKey, hashFor(alg), digest, &rsa.PSSOptions{SaltLength: sha256.Size, Hash: hashFor(alg)})
	case strings.HasPrefix(alg, "ES"):
		// The key must match the curve the algorithm names, so each ES variant is
		// signed with its own generated key.
		key := idp.ecKey
		switch alg {
		case "ES384":
			key = idp.ec384Key
		case "ES512":
			key = idp.ec521Key
		}
		digest := digest(t, alg, []byte(signingInput))
		r, s, serr := ecdsa.Sign(rand.Reader, key, digest)
		if serr != nil {
			t.Fatal(serr)
		}
		size := curveSize(key.Curve)
		signature = append(padLeft(r.Bytes(), size), padLeft(s.Bytes(), size)...)
		err = nil
	case alg == "EdDSA":
		signature = ed25519.Sign(idp.edKey, []byte(signingInput))
	case alg == "none":
		signature = nil
	case strings.HasPrefix(alg, "HS"):
		mac := hmacSign(alg, []byte("tunnelmesh-console-secret"), []byte(signingInput))
		signature = mac
	default:
		t.Fatalf("test harness cannot sign %s", alg)
	}
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + b64(signature)
}

func curveSize(curve elliptic.Curve) int {
	bits := curve.Params().BitSize
	return (bits + 7) / 8
}

func padLeft(value []byte, size int) []byte {
	out := make([]byte, size)
	copy(out[size-len(value):], value)
	return out
}

func digest(t *testing.T, alg string, input []byte) []byte {
	t.Helper()
	var hasher hash.Hash
	switch hashFor(alg) {
	case crypto.SHA256:
		hasher = sha256.New()
	case crypto.SHA384:
		hasher = sha512.New384()
	case crypto.SHA512:
		hasher = sha512.New()
	default:
		t.Fatalf("no digest for %s", alg)
	}
	hasher.Write(input)
	return hasher.Sum(nil)
}

func hmacSign(alg string, key, input []byte) []byte {
	var newHash func() hash.Hash
	switch alg {
	case "HS256":
		newHash = sha256.New
	case "HS384":
		newHash = sha512.New384
	case "HS512":
		newHash = sha512.New
	default:
		return nil
	}
	mac := hmac.New(newHash, key)
	mac.Write(input)
	return mac.Sum(nil)
}

func (idp *testIDP) validClaims(overrides map[string]any) map[string]any {
	now := time.Now()
	claims := map[string]any{
		"iss":                idp.server.URL,
		"sub":                "idp-subject-0001",
		"aud":                idp.provider.ClientID,
		"exp":                now.Add(time.Hour).Unix(),
		"iat":                now.Unix(),
		"nonce":              "test-nonce",
		"preferred_username": "alice",
		"email":              "alice@example.com",
		"email_verified":     true,
	}
	for key, value := range overrides {
		if value == nil {
			delete(claims, key)
			continue
		}
		claims[key] = value
	}
	return claims
}

// TestEndToEndAuthorizationCodeFlow walks the whole relying-party path against a
// live TLS IdP: build the redirect, exchange the code, verify the id_token, and
// resolve the account identity.
func TestEndToEndAuthorizationCodeFlow(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	ctx := context.Background()

	verifier, challenge, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	state, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	authURL, err := idp.rp.BuildAuthURL(ctx, idp.provider, AuthRequest{State: state, Nonce: "test-nonce", CodeChallenge: challenge})
	if err != nil {
		t.Fatalf("build auth url: %v", err)
	}

	accessToken := "access-token-value"
	sum := sha256.Sum256([]byte(accessToken))
	idp.setTokenResponse(http.StatusOK, fmt.Sprintf(`{"id_token":%q,"access_token":%q,"token_type":"Bearer","expires_in":3600}`,
		idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(map[string]any{"at_hash": b64(sum[:len(sum)/2])})), accessToken))

	tokens, err := idp.rp.Exchange(ctx, idp.provider, "authorization-code", verifier, "")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if tokens.AccessToken != accessToken || tokens.TokenType != "Bearer" || tokens.ExpiresIn != time.Hour {
		t.Fatalf("tokens = %+v", tokens)
	}
	claims, err := idp.rp.VerifyIDToken(ctx, idp.provider, tokens.IDToken, "test-nonce", accessToken)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "idp-subject-0001" || claims.Email != "alice@example.com" || !claims.EmailVerified {
		t.Fatalf("claims = %+v", claims)
	}
	username, role, err := ResolveIdentity(idp.provider, claims)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if username != "alice" || role != "user" {
		t.Fatalf("identity = %q/%q", username, role)
	}
	_ = authURL
}

func TestBuildAuthURLCarriesRequiredParameters(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	verifier, challenge, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := idp.rp.BuildAuthURL(context.Background(), idp.provider, AuthRequest{State: "state-1", Nonce: "nonce-1", CodeChallenge: challenge})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/authorize" {
		t.Fatalf("path = %q", parsed.Path)
	}
	query := parsed.Query()
	want := map[string]string{
		"response_type":         "code",
		"client_id":             idp.provider.ClientID,
		"redirect_uri":          idp.provider.RedirectURI,
		"state":                 "state-1",
		"nonce":                 "nonce-1",
		"code_challenge":        challenge,
		"code_challenge_method": "S256",
	}
	for key, expected := range want {
		if got := query.Get(key); got != expected {
			t.Fatalf("%s = %q, want %q", key, got, expected)
		}
	}
	scopes := strings.Fields(query.Get("scope"))
	if len(scopes) == 0 || scopes[0] != "openid" {
		t.Fatalf("scope = %q, want openid first", query.Get("scope"))
	}
	// The challenge must be the base64url SHA-256 of the verifier, which is what
	// makes S256 rather than plain PKCE.
	expected := sha256.Sum256([]byte(verifier))
	if base64.RawURLEncoding.EncodeToString(expected[:]) != challenge {
		t.Fatal("code challenge is not the S256 of the verifier")
	}
}

func TestBuildAuthURLRequiresStateNonceAndChallenge(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	cases := []AuthRequest{
		{Nonce: "n", CodeChallenge: "c"},
		{State: "s", CodeChallenge: "c"},
		{State: "s", Nonce: "n"},
	}
	for index, req := range cases {
		if _, err := idp.rp.BuildAuthURL(context.Background(), idp.provider, req); !errors.Is(err, ErrProviderInvalid) {
			t.Fatalf("case %d err = %v, want ErrProviderInvalid", index, err)
		}
	}
}

func TestNewPKCEIsS256AndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 16; i++ {
		verifier, challenge, err := NewPKCE()
		if err != nil {
			t.Fatal(err)
		}
		if len(verifier) != 43 {
			t.Fatalf("verifier length = %d", len(verifier))
		}
		if seen[verifier] {
			t.Fatal("verifier repeated")
		}
		seen[verifier] = true
		sum := sha256.Sum256([]byte(verifier))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			t.Fatal("challenge is not S256 of the verifier")
		}
		if verifier == challenge {
			t.Fatal("challenge equals verifier, which would be plain PKCE")
		}
	}
}

func TestExchangePostsFormAndUsesBasicAuthOnlyWithSecret(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	var captured *http.Request
	var capturedBody []byte
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			captured = r.Clone(context.Background())
			capturedBody, _ = readAll(r)
		}
		idp.handle(w, r)
	})
	idp.setTokenResponse(http.StatusOK, `{"id_token":"a.b.c","access_token":"tok","token_type":"Bearer"}`)

	// Public client: no secret configured.
	if _, err := idp.rp.Exchange(context.Background(), idp.provider, "code-1", "verifier-1", ""); err != nil {
		t.Fatalf("public exchange: %v", err)
	}
	if captured == nil {
		t.Fatal("token endpoint was not called")
	}
	if _, _, ok := captured.BasicAuth(); ok {
		t.Fatal("basic auth sent for a public client")
	}
	form, err := url.ParseQuery(string(capturedBody))
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code-1" || form.Get("code_verifier") != "verifier-1" {
		t.Fatalf("form = %v", form)
	}
	if form.Get("client_id") != idp.provider.ClientID {
		t.Fatalf("client_id missing from the body for a public client: %v", form)
	}
	if captured.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("content type = %q", captured.Header.Get("Content-Type"))
	}

	// Confidential client: a secret closure makes basic auth appear.
	confidential := idp.provider
	confidential.Secret = func(context.Context) (string, error) { return "s3cr3t", nil }
	if _, err := idp.rp.Exchange(context.Background(), confidential, "code-2", "verifier-2", ""); err != nil {
		t.Fatalf("confidential exchange: %v", err)
	}
	username, password, ok := captured.BasicAuth()
	if !ok || username != idp.provider.ClientID || password != "s3cr3t" {
		t.Fatalf("basic auth = %q/%q/%v", username, password, ok)
	}
}

func TestExchangeFailuresDoNotLeakSecrets(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	provider := idp.provider
	provider.Secret = func(context.Context) (string, error) { return "top-secret-value", nil }

	idp.setTokenResponse(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"code was already redeemed by an attacker at /redeem?code=abc"}`)
	_, err := idp.rp.Exchange(context.Background(), provider, "authorization-code-value", "verifier-value", "")
	if !errors.Is(err, ErrTokenExchangeFailed) {
		t.Fatalf("err = %v, want ErrTokenExchangeFailed", err)
	}
	message := err.Error()
	for _, secret := range []string{"top-secret-value", "authorization-code-value", "verifier-value", "already redeemed"} {
		if strings.Contains(message, secret) {
			t.Fatalf("error %q leaked %q", message, secret)
		}
	}
	if !strings.Contains(message, "invalid_grant") {
		t.Fatalf("error %q dropped the OAuth error code", message)
	}

	idp.setTokenResponse(http.StatusInternalServerError, `not json`)
	if _, err := idp.rp.Exchange(context.Background(), provider, "code", "verifier", ""); !errors.Is(err, ErrTokenExchangeFailed) {
		t.Fatalf("5xx err = %v", err)
	}

	idp.setTokenResponse(http.StatusOK, `{"access_token":"tok"}`)
	if _, err := idp.rp.Exchange(context.Background(), provider, "code", "verifier", ""); !errors.Is(err, ErrTokenExchangeFailed) {
		t.Fatalf("missing id_token err = %v", err)
	}

	idp.setTokenResponse(http.StatusOK, `{"id_token":`)
	if _, err := idp.rp.Exchange(context.Background(), provider, "code", "verifier", ""); !errors.Is(err, ErrTokenExchangeFailed) {
		t.Fatalf("malformed json err = %v", err)
	}

	if _, err := idp.rp.Exchange(context.Background(), provider, "", "verifier", ""); !errors.Is(err, ErrTokenExchangeFailed) {
		t.Fatalf("empty code err = %v", err)
	}
}

func TestClientSecretIsResolvedLazilyAndNeverStored(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	var calls atomic.Int32
	provider := idp.provider
	provider.Secret = func(context.Context) (string, error) { calls.Add(1); return "lazy-secret", nil }
	if provider.ClientSecret.IsZero() != true {
		t.Fatal("plaintext secret was stored on the Provider")
	}
	idp.setTokenResponse(http.StatusOK, `{"id_token":"a.b.c"}`)
	if _, err := idp.rp.Exchange(context.Background(), provider, "code", "verifier", ""); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("secret resolver called %d times", calls.Load())
	}
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}
