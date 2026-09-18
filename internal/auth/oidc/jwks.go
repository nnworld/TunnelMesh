package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
)

// jwk is one JSON Web Key. Only the public members needed for verification are
// decoded; a private member in the document is ignored rather than trusted.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	// RSA
	N string `json:"n"`
	E string `json:"e"`
	// EC and OKP
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type jwkSet struct {
	Keys []jwk `json:"keys"`
}

// signingKey is a parsed, ready-to-verify public key.
type signingKey struct {
	Kid       string
	Alg       string
	PublicKey interface{}
}

// keyCacheEntry bounds how often one JWKS URI is refetched. IdP signing keys
// rotate on the order of days, so an hourly cache removes a round trip from
// nearly every login without meaningfully delaying a rotation.
type keyCacheEntry struct {
	keys      []signingKey
	expiresAt time.Time
}

// keysFor returns the signing keys for a provider, refreshing when the cache has
// expired. The JWKS URI comes from the resolved endpoints so an override is
// honoured.
func (rp *RelyingParty) keysFor(ctx context.Context, p Provider, jwksURI string, force bool) ([]signingKey, error) {
	if !force {
		rp.mu.Lock()
		entry, ok := rp.keys[jwksURI]
		rp.mu.Unlock()
		if ok && rp.now().Before(entry.expiresAt) {
			return entry.keys, nil
		}
	}
	body, err := rp.get(ctx, jwksURI, "application/json")
	if err != nil {
		return nil, errors.Join(ErrJWKSFetchFailed, err)
	}
	var set jwkSet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, errors.Join(ErrJWKSFetchFailed, err)
	}
	keys := make([]signingKey, 0, len(set.Keys))
	for _, candidate := range set.Keys {
		// A key published for encryption cannot verify a signature, and a key
		// whose type is unknown is skipped rather than treated as an error:
		// IdPs routinely publish both signing and wrapping keys together.
		if candidate.Use != "" && candidate.Use != "sig" {
			continue
		}
		key, err := parseJWK(candidate)
		if err != nil {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.Join(ErrJWKSFetchFailed, errors.New("no usable signing keys"))
	}
	rp.mu.Lock()
	rp.keys[jwksURI] = &keyCacheEntry{keys: keys, expiresAt: rp.now().Add(rp.cfg.JWKSCacheTTL)}
	rp.mu.Unlock()
	return keys, nil
}

// findKey resolves one key by kid, performing exactly one forced refresh when
// the cached set does not contain it. Bounding the refresh at one prevents a
// caller from turning an invalid token into a JWKS amplification attack against
// the IdP.
func (rp *RelyingParty) findKey(ctx context.Context, p Provider, jwksURI, kid string) (signingKey, error) {
	keys, err := rp.keysFor(ctx, p, jwksURI, false)
	if err != nil {
		return signingKey{}, err
	}
	if key, ok := selectKey(keys, kid); ok {
		return key, nil
	}
	keys, err = rp.keysFor(ctx, p, jwksURI, true)
	if err != nil {
		return signingKey{}, err
	}
	if key, ok := selectKey(keys, kid); ok {
		return key, nil
	}
	return signingKey{}, ErrNoMatchingKey
}

// selectKey matches on kid. A single-key set with no kid on either side is
// accepted, which is what many IdPs publish for a non-rotated key.
func selectKey(keys []signingKey, kid string) (signingKey, bool) {
	for _, key := range keys {
		if key.Kid == kid {
			return key, true
		}
	}
	if kid == "" && len(keys) == 1 {
		return keys[0], true
	}
	return signingKey{}, false
}

// parseJWK converts one document entry into a crypto public key.
func parseJWK(candidate jwk) (signingKey, error) {
	switch strings.ToUpper(strings.TrimSpace(candidate.Kty)) {
	case "RSA":
		modulus, err := decodeBigInt(candidate.N)
		if err != nil {
			return signingKey{}, err
		}
		exponentBytes, err := decodeBigInt(candidate.E)
		if err != nil {
			return signingKey{}, err
		}
		if !exponentBytes.IsInt64() || exponentBytes.Int64() <= 0 {
			return signingKey{}, errors.New("invalid rsa exponent")
		}
		return signingKey{Kid: candidate.Kid, Alg: strings.ToUpper(candidate.Alg), PublicKey: &rsa.PublicKey{N: modulus, E: int(exponentBytes.Int64())}}, nil
	case "EC":
		curve, err := namedCurve(candidate.Crv)
		if err != nil {
			return signingKey{}, err
		}
		x, err := decodeBigInt(candidate.X)
		if err != nil {
			return signingKey{}, err
		}
		y, err := decodeBigInt(candidate.Y)
		if err != nil {
			return signingKey{}, err
		}
		key := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
		// A point that is not on the curve would make every verification fail in
		// a confusing way; rejecting it here names the real problem.
		if !curve.IsOnCurve(x, y) {
			return signingKey{}, errors.New("ec point is not on the curve")
		}
		return signingKey{Kid: candidate.Kid, Alg: strings.ToUpper(candidate.Alg), PublicKey: key}, nil
	case "OKP":
		if !strings.EqualFold(strings.TrimSpace(candidate.Crv), "Ed25519") {
			return signingKey{}, errors.New("unsupported okp curve")
		}
		raw, err := base64.RawURLEncoding.DecodeString(candidate.X)
		if err != nil {
			return signingKey{}, err
		}
		if len(raw) != ed25519.PublicKeySize {
			return signingKey{}, errors.New("invalid ed25519 key length")
		}
		return signingKey{Kid: candidate.Kid, Alg: strings.ToUpper(candidate.Alg), PublicKey: ed25519.PublicKey(raw)}, nil
	default:
		return signingKey{}, errors.New("unsupported kty")
	}
}

func namedCurve(crv string) (elliptic.Curve, error) {
	switch strings.TrimSpace(crv) {
	case "P-256":
		return elliptic.P256(), nil
	case "P-384":
		return elliptic.P384(), nil
	case "P-521":
		return elliptic.P521(), nil
	default:
		return nil, errors.New("unsupported ec curve")
	}
}

func decodeBigInt(value string) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("empty jwk component")
	}
	return new(big.Int).SetBytes(raw), nil
}

// InvalidateKeys drops the cached key set for one JWKS URI.
func (rp *RelyingParty) InvalidateKeys(jwksURI string) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	delete(rp.keys, jwksURI)
}

// keyCacheLen reports how many JWKS URIs are cached. It exists for tests that
// assert the cache is actually being used.
func (rp *RelyingParty) keyCacheLen() int {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return len(rp.keys)
}

// ProbeKeys fetches and parses the JWKS document without verifying a token. It
// backs the administrator connectivity test, which must confirm that signing keys
// are reachable and parseable without performing a token exchange.
func (rp *RelyingParty) ProbeKeys(ctx context.Context, p Provider, jwksURI string) ([]string, error) {
	keys, err := rp.keysFor(ctx, p, jwksURI, true)
	if err != nil {
		return nil, err
	}
	algorithms := make([]string, 0, len(keys))
	seen := map[string]bool{}
	for _, key := range keys {
		if key.Alg == "" || seen[key.Alg] {
			continue
		}
		seen[key.Alg] = true
		algorithms = append(algorithms, key.Alg)
	}
	return algorithms, nil
}
