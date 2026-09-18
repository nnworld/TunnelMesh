package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestJWKSParsesRSAECAndOKPAndSkipsUnknown(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ec384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	document := map[string]any{"keys": []any{
		map[string]any{"kty": "RSA", "kid": "r1", "alg": "RS256", "n": b64(rsaKey.N.Bytes()), "e": b64(big.NewInt(int64(rsaKey.E)).Bytes())},
		map[string]any{"kty": "EC", "kid": "e1", "alg": "ES384", "crv": "P-384", "x": b64(ec384.X.Bytes()), "y": b64(ec384.Y.Bytes())},
		map[string]any{"kty": "OKP", "kid": "d1", "alg": "EdDSA", "crv": "Ed25519", "x": b64(edKey.Public().(ed25519.PublicKey))},
		map[string]any{"kty": "RSA", "kid": "enc-only", "use": "enc", "n": b64(rsaKey.N.Bytes()), "e": b64(big.NewInt(65537).Bytes())},
		map[string]any{"kty": "unknown-type", "kid": "u1"},
		map[string]any{"kty": "EC", "kid": "bad-curve", "crv": "P-999", "x": "AA", "y": "AA"},
	}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, document) }))
	defer server.Close()
	rp := NewRelyingParty(server.Client(), DefaultConfig(), nil)

	keys, err := rp.keysFor(context.Background(), Provider{}, server.URL+"/jwks.json", false)
	if err != nil {
		t.Fatalf("keysFor: %v", err)
	}
	kids := map[string]bool{}
	for _, key := range keys {
		kids[key.Kid] = true
	}
	for _, want := range []string{"r1", "e1", "d1"} {
		if !kids[want] {
			t.Fatalf("missing key %q in %v", want, kids)
		}
	}
	// An encryption key cannot verify a signature and an unparseable key must not
	// abort the whole set.
	for _, unwanted := range []string{"enc-only", "u1", "bad-curve"} {
		if kids[unwanted] {
			t.Fatalf("key %q should have been skipped", unwanted)
		}
	}
	for _, key := range keys {
		switch key.Kid {
		case "r1":
			if _, ok := key.PublicKey.(*rsa.PublicKey); !ok {
				t.Fatalf("r1 parsed as %T", key.PublicKey)
			}
		case "e1":
			if _, ok := key.PublicKey.(*ecdsa.PublicKey); !ok {
				t.Fatalf("e1 parsed as %T", key.PublicKey)
			}
		case "d1":
			if _, ok := key.PublicKey.(ed25519.PublicKey); !ok {
				t.Fatalf("d1 parsed as %T", key.PublicKey)
			}
		}
	}
}

func TestJWKSCachingFetchesOnceWithinTTL(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := idp.rp.keysFor(ctx, idp.provider, idp.server.URL+"/jwks.json", false); err != nil {
			t.Fatal(err)
		}
	}
	if got := idp.jwksRequests.Load(); got != 1 {
		t.Fatalf("jwks fetched %d times, want 1", got)
	}
	if idp.rp.keyCacheLen() != 1 {
		t.Fatalf("cache length = %d", idp.rp.keyCacheLen())
	}
	if _, err := idp.rp.keysFor(ctx, idp.provider, idp.server.URL+"/jwks.json", true); err != nil {
		t.Fatal(err)
	}
	if got := idp.jwksRequests.Load(); got != 2 {
		t.Fatalf("forced refresh did not refetch: %d", got)
	}
}

func TestJWKSCacheExpires(t *testing.T) {
	idp := newTestIDP(t, Config{JWKSCacheTTL: 10 * time.Millisecond})
	ctx := context.Background()
	if _, err := idp.rp.keysFor(ctx, idp.provider, idp.server.URL+"/jwks.json", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := idp.rp.keysFor(ctx, idp.provider, idp.server.URL+"/jwks.json", false); err != nil {
		t.Fatal(err)
	}
	if got := idp.jwksRequests.Load(); got != 2 {
		t.Fatalf("cache did not expire: %d fetches", got)
	}
}

func TestJWKSFetchFailures(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	ctx := context.Background()
	uri := idp.server.URL + "/jwks.json"

	idp.mu.Lock()
	idp.badJWKS = true
	idp.mu.Unlock()
	if _, err := idp.rp.keysFor(ctx, idp.provider, uri, true); !errors.Is(err, ErrJWKSFetchFailed) {
		t.Fatalf("5xx err = %v", err)
	}
	idp.mu.Lock()
	idp.badJWKS = false
	idp.mu.Unlock()

	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	if _, err := idp.rp.keysFor(ctx, idp.provider, uri, true); !errors.Is(err, ErrJWKSFetchFailed) {
		t.Fatalf("malformed err = %v", err)
	}

	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": []any{}})
	})
	if _, err := idp.rp.keysFor(ctx, idp.provider, uri, true); !errors.Is(err, ErrJWKSFetchFailed) {
		t.Fatalf("empty set err = %v", err)
	}

	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": []any{map[string]any{"kty": "oct", "kid": "k", "k": "AA"}}})
	})
	if _, err := idp.rp.keysFor(ctx, idp.provider, uri, true); !errors.Is(err, ErrJWKSFetchFailed) {
		t.Fatalf("no usable keys err = %v", err)
	}
}

func TestJWKSOversizedBodyFails(t *testing.T) {
	idp := newTestIDP(t, Config{MaxBodyBytes: 32})
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "r", "n": b64(make([]byte, 4096)), "e": "AQAB"}}})
	})
	if _, err := idp.rp.keysFor(context.Background(), idp.provider, idp.server.URL+"/jwks.json", true); !errors.Is(err, ErrJWKSFetchFailed) {
		t.Fatalf("err = %v", err)
	}
}

func TestFindKeyUnknownKidRefreshesExactlyOnce(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	ctx := context.Background()
	// Warm the cache with a normal lookup so the counter starts from a known state.
	if _, err := idp.rp.findKey(ctx, idp.provider, idp.server.URL+"/jwks.json", idp.rsaKid); err != nil {
		t.Fatal(err)
	}
	before := idp.jwksRequests.Load()
	if _, err := idp.rp.findKey(ctx, idp.provider, idp.server.URL+"/jwks.json", "kid-that-does-not-exist"); !errors.Is(err, ErrNoMatchingKey) {
		t.Fatalf("err = %v, want ErrNoMatchingKey", err)
	}
	if got := idp.jwksRequests.Load() - before; got != 1 {
		t.Fatalf("unknown kid triggered %d refreshes, want exactly 1", got)
	}
}

// TestFindKeyPicksUpRotatedKey is the reason the single refresh exists: a key
// rotation must succeed on the next login without a restart.
func TestFindKeyPicksUpRotatedKey(t *testing.T) {
	idp := newTestIDP(t, DefaultConfig())
	ctx := context.Background()
	if _, err := idp.rp.findKey(ctx, idp.provider, idp.server.URL+"/jwks.json", idp.rsaKid); err != nil {
		t.Fatal(err)
	}
	rotated, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	handler := idp.server.Config.Handler
	idp.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jwks.json" {
			writeJSON(w, map[string]any{"keys": []any{map[string]any{
				"kty": "RSA", "kid": "rsa-2", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(rotated.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(rotated.E)).Bytes()),
			}}})
			return
		}
		handler.ServeHTTP(w, r)
	})
	key, err := idp.rp.findKey(ctx, idp.provider, idp.server.URL+"/jwks.json", "rsa-2")
	if err != nil {
		t.Fatalf("rotated key not found: %v", err)
	}
	if key.Kid != "rsa-2" {
		t.Fatalf("kid = %q", key.Kid)
	}
}

func TestSelectKeySingleKeyWithoutKid(t *testing.T) {
	keys := []signingKey{{Kid: "", PublicKey: &rsa.PublicKey{}}}
	if _, ok := selectKey(keys, ""); !ok {
		t.Fatal("single anonymous key was not selected")
	}
	if _, ok := selectKey(keys, "explicit"); ok {
		t.Fatal("anonymous key matched an explicit kid")
	}
	if _, ok := selectKey([]signingKey{{Kid: "a"}, {Kid: "b"}}, ""); ok {
		t.Fatal("ambiguous empty kid matched")
	}
}

func TestParseJWKRejectsMalformedComponents(t *testing.T) {
	cases := []jwk{
		{Kty: "RSA", Kid: "a", N: "!!!not-base64!!!", E: "AQAB"},
		{Kty: "RSA", Kid: "b", N: "", E: "AQAB"},
		{Kty: "RSA", Kid: "c", N: b64([]byte{1, 2, 3}), E: ""},
		{Kty: "EC", Kid: "d", Crv: "P-256", X: "AA", Y: "AA"},
		{Kty: "OKP", Kid: "e", Crv: "Ed25519", X: "AAAA"},
		{Kty: "OKP", Kid: "f", Crv: "X25519", X: b64(make([]byte, ed25519.PublicKeySize))},
		{Kty: "", Kid: "g"},
	}
	for _, candidate := range cases {
		if _, err := parseJWK(candidate); err == nil {
			t.Fatalf("parseJWK(%+v) unexpectedly succeeded", candidate)
		}
	}
}

func TestJWKSDocumentShapeIsTolerant(t *testing.T) {
	// Extra members must not break parsing; IdPs publish plenty of them.
	raw := `{"keys":[{"kty":"OKP","kid":"d1","alg":"EdDSA","crv":"Ed25519","x":"` +
		b64(make([]byte, ed25519.PublicKeySize)) + `","x5t":"ignored","unknown":{"a":1}}]}`
	var set jwkSet
	if err := json.Unmarshal([]byte(raw), &set); err != nil {
		t.Fatal(err)
	}
	if len(set.Keys) != 1 || set.Keys[0].Kid != "d1" {
		t.Fatalf("set = %+v", set)
	}
}
