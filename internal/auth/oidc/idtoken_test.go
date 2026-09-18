package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// atHashFor computes the at_hash claim the way an IdP would, for a given
// algorithm family.
func atHashFor(t *testing.T, alg, accessToken string) string {
	t.Helper()
	var digest []byte
	switch hashFor(alg) {
	case crypto.SHA256:
		sum := sha256.Sum256([]byte(accessToken))
		digest = sum[:]
	case crypto.SHA384:
		sum := sha512.Sum384([]byte(accessToken))
		digest = sum[:]
	case crypto.SHA512:
		sum := sha512.Sum512([]byte(accessToken))
		digest = sum[:]
	default:
		t.Fatalf("no at_hash for %s", alg)
	}
	return base64.RawURLEncoding.EncodeToString(digest[:len(digest)/2])
}

func TestVerifyIDTokenAcceptsEveryAllowedAlgorithm(t *testing.T) {
	cases := []struct {
		alg string
		kid string
	}{
		{"RS256", "rsa-1"},
		{"RS384", "rsa-1"},
		{"RS512", "rsa-1"},
		{"PS256", "rsa-1"},
		{"PS384", "rsa-1"},
		{"PS512", "rsa-1"},
		{"ES256", "ec-1"},
		{"ES384", "ec-384"},
		{"ES512", "ec-521"},
		{"EdDSA", "ed-1"},
	}
	for _, tc := range cases {
		t.Run(tc.alg, func(t *testing.T) {
			idp := newTestIDP(t, DefaultConfig())
			provider := idp.provider
			provider.IDTokenAlgs = []string{tc.alg}
			token := idp.signToken(t, tc.alg, tc.kid, idp.validClaims(nil))
			claims, err := idp.rp.VerifyIDToken(context.Background(), provider, token, "test-nonce", "")
			if err != nil {
				t.Fatalf("verify %s: %v", tc.alg, err)
			}
			if claims.Subject != "idp-subject-0001" {
				t.Fatalf("claims = %+v", claims)
			}
		})
	}
}

func TestVerifyIDTokenRejectsNoneUnconditionally(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	ctx := context.Background()
	// Even when an operator somehow stores "none" in the allowlist, both the
	// parser and the verifier refuse it.
	if _, err := ParseAlgorithms([]string{"none"}); !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Fatalf("ParseAlgorithms(none) err = %v", err)
	}
	if _, err := ParseAlgorithms([]string{"NONE"}); !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Fatalf("ParseAlgorithms(NONE) err = %v", err)
	}
	for _, algs := range [][]string{{"none", "RS256"}, {"RS256", "none"}} {
		if _, err := ParseAlgorithms(algs); !errors.Is(err, ErrUnsupportedAlgorithm) {
			t.Fatalf("ParseAlgorithms(%v) err = %v", algs, err)
		}
	}
	provider := idp.provider
	provider.IDTokenAlgs = []string{"none", "RS256"}
	token := idp.signToken(t, "none", "", idp.validClaims(nil))
	if _, err := idp.rp.VerifyIDToken(ctx, provider, token, "test-nonce", ""); !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Fatalf("alg=none err = %v, want ErrUnsupportedAlgorithm", err)
	}
}

// TestVerifyIDTokenRejectsHMACWithClientSecret is the algorithm-confusion
// attack: an attacker signs with the shared secret and hopes the verifier treats
// it as an HMAC key. It must be rejected even when HS256 is allowlisted.
func TestVerifyIDTokenRejectsHMACWithClientSecret(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	ctx := context.Background()
	for _, alg := range []string{"HS256", "HS384", "HS512"} {
		if _, err := ParseAlgorithms([]string{alg}); !errors.Is(err, ErrUnsupportedAlgorithm) {
			t.Fatalf("ParseAlgorithms(%s) err = %v", alg, err)
		}
		provider := idp.provider
		provider.IDTokenAlgs = []string{alg}
		provider.Secret = func(context.Context) (string, error) { return "tunnelmesh-console-secret", nil }
		token := idp.signToken(t, alg, "", idp.validClaims(nil))
		if _, err := idp.rp.VerifyIDToken(ctx, provider, token, "test-nonce", ""); !errors.Is(err, ErrUnsupportedAlgorithm) {
			t.Fatalf("%s err = %v, want ErrUnsupportedAlgorithm", alg, err)
		}
	}
}

func TestVerifyIDTokenRejectsAlgorithmOutsideAllowlist(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	provider := idp.provider
	provider.IDTokenAlgs = []string{"ES256"}
	token := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(nil))
	if _, err := idp.rp.VerifyIDToken(context.Background(), provider, token, "test-nonce", ""); !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Fatalf("err = %v, want ErrUnsupportedAlgorithm", err)
	}
}

func TestVerifyIDTokenRejectsUnknownAlgorithmName(t *testing.T) {
	if _, err := ParseAlgorithms([]string{"RS999"}); !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Fatalf("err = %v", err)
	}
	if _, err := ParseAlgorithms([]string{"ES256K"}); !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyIDTokenClaimValidation(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		claims   map[string]any
		nonce    string
		wantText string
	}{
		{"wrong issuer", map[string]any{"iss": "https://evil.example.com"}, "test-nonce", "iss mismatch"},
		{"missing issuer", map[string]any{"iss": nil}, "test-nonce", "iss claim missing"},
		{"wrong audience", map[string]any{"aud": "someone-else"}, "test-nonce", "aud mismatch"},
		{"missing audience", map[string]any{"aud": nil}, "test-nonce", "aud mismatch"},
		{"audience array without client", map[string]any{"aud": []any{"other", "third"}}, "test-nonce", "aud mismatch"},
		{"expired", map[string]any{"exp": now.Add(-2 * time.Hour).Unix()}, "test-nonce", "token expired"},
		{"missing expiry", map[string]any{"exp": nil}, "test-nonce", "exp claim missing"},
		{"not yet valid", map[string]any{"nbf": now.Add(2 * time.Hour).Unix()}, "test-nonce", "token not yet valid"},
		{"wrong nonce", nil, "attacker-nonce", "nonce mismatch"},
		{"missing subject", map[string]any{"sub": ""}, "test-nonce", "sub claim missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := newTestIDP(t, DefaultConfig())
			claims := idp.validClaims(tc.claims)
			token := idp.signToken(t, "RS256", idp.rsaKid, claims)
			_, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, token, tc.nonce, "")
			if !errors.Is(err, ErrIDTokenInvalid) {
				t.Fatalf("err = %v, want ErrIDTokenInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("err %q does not mention %q", err, tc.wantText)
			}
		})
	}
}

// TestVerifyIDTokenAcceptsAudienceArray covers IdPs that address several clients
// with one token.
func TestVerifyIDTokenAcceptsAudienceArray(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	token := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(map[string]any{"aud": []any{"other-client", idp.provider.ClientID}}))
	if _, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, token, "test-nonce", ""); err != nil {
		t.Fatalf("err = %v", err)
	}
}

// TestVerifyIDTokenClockSkewTolerance bounds how much drift is forgiven, which is
// the difference between tolerating NTP jitter and accepting stale assertions.
func TestVerifyIDTokenClockSkewTolerance(t *testing.T) {
	now := time.Now()
	idp := newTestIDP(t, DefaultConfig())
	inside := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(map[string]any{"exp": now.Add(-30 * time.Second).Unix()}))
	if _, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, inside, "test-nonce", ""); err != nil {
		t.Fatalf("30s past exp should be inside the 60s skew: %v", err)
	}
	outside := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(map[string]any{"exp": now.Add(-10 * time.Minute).Unix()}))
	if _, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, outside, "test-nonce", ""); !errors.Is(err, ErrIDTokenInvalid) {
		t.Fatalf("err = %v, want ErrIDTokenInvalid", err)
	}
	future := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(map[string]any{"nbf": now.Add(10 * time.Minute).Unix()}))
	if _, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, future, "test-nonce", ""); !errors.Is(err, ErrIDTokenInvalid) {
		t.Fatalf("nbf err = %v, want ErrIDTokenInvalid", err)
	}
}

func TestVerifyIDTokenAtHash(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	ctx := context.Background()
	accessToken := "the-access-token"
	good := atHashFor(t, "RS256", accessToken)
	token := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(map[string]any{"at_hash": good}))
	if _, err := idp.rp.VerifyIDToken(ctx, idp.provider, token, "test-nonce", accessToken); err != nil {
		t.Fatalf("matching at_hash rejected: %v", err)
	}
	// A different access token must not be bindable to this id_token.
	if _, err := idp.rp.VerifyIDToken(ctx, idp.provider, token, "test-nonce", "another-access-token"); !errors.Is(err, ErrIDTokenInvalid) {
		t.Fatalf("wrong access token err = %v", err)
	}
	bad := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(map[string]any{"at_hash": atHashFor(t, "RS256", "forged")}))
	if _, err := idp.rp.VerifyIDToken(ctx, idp.provider, bad, "test-nonce", accessToken); !errors.Is(err, ErrIDTokenInvalid) {
		t.Fatalf("wrong at_hash err = %v", err)
	}
	// When the IdP omits at_hash the check is skipped rather than failing every
	// login against a conformant-but-minimal provider.
	omitted := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(map[string]any{"at_hash": nil}))
	if _, err := idp.rp.VerifyIDToken(ctx, idp.provider, omitted, "test-nonce", accessToken); err != nil {
		t.Fatalf("omitted at_hash rejected: %v", err)
	}
}

func TestVerifyIDTokenWrongKeySameKidFails(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	// Sign with a key that is not in the published set but advertise the known
	// kid, which is exactly what a stolen-kid forgery looks like.
	forged := newTestIDP(t, DefaultConfig())
	token := forged.signToken(t, "RS256", idp.rsaKid, idp.validClaims(nil))
	if _, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, token, "test-nonce", ""); !errors.Is(err, ErrIDTokenInvalid) {
		t.Fatalf("err = %v, want ErrIDTokenInvalid", err)
	}
}

func TestVerifyIDTokenUnknownKidFails(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	token := idp.signToken(t, "RS256", "unknown-kid", idp.validClaims(nil))
	_, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, token, "test-nonce", "")
	if !errors.Is(err, ErrNoMatchingKey) {
		t.Fatalf("err = %v, want ErrNoMatchingKey", err)
	}
}

func TestVerifyIDTokenMalformedTokensDoNotPanic(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	ctx := context.Background()
	good := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(nil))
	parts := strings.Split(good, ".")
	cases := []string{
		"",
		"   ",
		"not-a-jwt",
		"one.two",
		"one.two.three.four",
		"!!!." + parts[1] + "." + parts[2],
		parts[0] + ".!!!." + parts[2],
		parts[0] + "." + parts[1] + ".!!!",
		base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"`)) + "." + parts[1] + "." + parts[2],
		".",
		"..",
	}
	for index, token := range cases {
		if _, err := idp.rp.VerifyIDToken(ctx, idp.provider, token, "test-nonce", ""); !errors.Is(err, ErrIDTokenInvalid) {
			t.Fatalf("case %d (%q) err = %v, want ErrIDTokenInvalid", index, token, err)
		}
	}
}

// TestVerifyIDTokenRejectsCriticalHeaderExtensions covers a JOSE feature this
// implementation does not evaluate, which must therefore not be honoured.
func TestVerifyIDTokenRejectsCriticalHeaderExtensions(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": idp.rsaKid, "crit": []string{"http://example.invalid/undefined"}}
	payload, err := json.Marshal(idp.validClaims(nil))
	if err != nil {
		t.Fatal(err)
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := b64(headerJSON) + "." + b64(payload)
	digest := digest(t, "RS256", []byte(signingInput))
	signature, err := signRSA(idp, digest)
	if err != nil {
		t.Fatal(err)
	}
	token := signingInput + "." + b64(signature)
	if _, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, token, "test-nonce", ""); !errors.Is(err, ErrIDTokenInvalid) {
		t.Fatalf("err = %v, want ErrIDTokenInvalid", err)
	}
}

func TestVerifyIDTokenIssuerTrailingSlashIsTolerated(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	provider := idp.provider
	provider.Issuer = idp.server.URL + "/"
	token := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(nil))
	if _, err := idp.rp.VerifyIDToken(context.Background(), provider, token, "test-nonce", ""); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyIDTokenJWKSUnavailableFails(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	token := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(nil))
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jwks.json" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		idp.handle(w, r)
	})
	idp.rp.InvalidateKeys(idp.server.URL + "/jwks.json")
	idp.rp.InvalidateMetadata(idp.provider.Issuer)
	if _, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, token, "test-nonce", ""); !errors.Is(err, ErrJWKSFetchFailed) {
		t.Fatalf("err = %v, want ErrJWKSFetchFailed", err)
	}
}

// TestVerifyIDTokenNonceOnlyCheckedWhenExpected guards the machine-to-machine
// case where no redirect nonce exists, while still binding browser logins.
func TestVerifyIDTokenNonceOnlyCheckedWhenExpected(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	token := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(map[string]any{"nonce": nil}))
	if _, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, token, "", ""); err != nil {
		t.Fatalf("no expected nonce should pass: %v", err)
	}
	if _, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, token, "required-nonce", ""); !errors.Is(err, ErrIDTokenInvalid) {
		t.Fatalf("err = %v, want ErrIDTokenInvalid", err)
	}
}

// TestVerifyIDTokenSealedSecretIsUsed covers the storage-shaped secret path,
// where the plaintext never lives on the Provider value.
func TestVerifyIDTokenSealedSecretIsUsed(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	}))
	defer server.Close()
	secrets := fixedSecretProvider{plain: "sealed-secret-value"}
	rp := NewRelyingParty(server.Client(), DefaultConfig(), secrets)
	provider := Provider{ClientSecret: SealedSecret{Ciphertext: []byte("ct"), Nonce: []byte("n"), KeyID: "k", Version: 1}}
	got, err := rp.clientSecret(context.Background(), provider)
	if err != nil || got != "sealed-secret-value" {
		t.Fatalf("secret = %q err = %v", got, err)
	}
	// A sealed secret with no available provider must fail closed.
	closed := NewRelyingParty(server.Client(), DefaultConfig(), nil)
	if _, err := closed.clientSecret(context.Background(), provider); err == nil {
		t.Fatal("sealed secret opened without a secret provider")
	}
	// A public client has no secret and no error.
	if got, err := rp.clientSecret(context.Background(), Provider{}); err != nil || got != "" {
		t.Fatalf("public client secret = %q err = %v", got, err)
	}
}

type fixedSecretProvider struct{ plain string }

func (p fixedSecretProvider) Available() bool { return true }
func (p fixedSecretProvider) Decrypt(ciphertext, nonce []byte, keyID string, version int) (string, error) {
	if len(ciphertext) == 0 {
		return "", errors.New("empty ciphertext")
	}
	return p.plain, nil
}

func signRSA(idp *testIDP, digest []byte) ([]byte, error) {
	return rsa.SignPKCS1v15(rand.Reader, idp.rsaKey, crypto.SHA256, digest)
}

func TestVerifyIDTokenErrorNeverContainsToken(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	token := idp.signToken(t, "RS256", idp.rsaKid, idp.validClaims(map[string]any{"aud": "wrong"}))
	_, err := idp.rp.VerifyIDToken(context.Background(), idp.provider, token, "test-nonce", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), strings.Split(token, ".")[1]) {
		t.Fatalf("error leaked the token: %v", err)
	}
	_ = fmt.Sprint(err)
}
