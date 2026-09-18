package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strings"
	"time"
)

// jwtHeader is the decoded JOSE header. Only the members this package acts on
// are read; a "crit" member is rejected because honouring critical extensions
// this code does not understand would be unsafe.
type jwtHeader struct {
	Alg  string   `json:"alg"`
	Kid  string   `json:"kid"`
	Typ  string   `json:"typ"`
	Crit []string `json:"crit"`
}

// jwtTimeClaims holds the registered temporal and identity claims. aud is
// decoded separately because it may be a string or an array of strings.
type jwtTimeClaims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Audience  any    `json:"aud"`
	Expiry    *int64 `json:"exp"`
	NotBefore *int64 `json:"nbf"`
	IssuedAt  *int64 `json:"iat"`
	Nonce     string `json:"nonce"`
	AtHash    string `json:"at_hash"`
}

// VerifyIDToken validates the signature and every claim TunnelMesh depends on,
// then returns the decoded claims. It returns ErrIDTokenInvalid for any claim or
// signature problem, and a more specific sentinel for a configuration or key
// problem, so an operator can tell "the IdP lied" from "we are misconfigured".
//
// The token is never logged and never appears in an error message.
func (rp *RelyingParty) VerifyIDToken(ctx context.Context, p Provider, rawIDToken, expectedNonce, accessToken string) (Claims, error) {
	signingInput, payloadSegment, signature, header, err := splitJWS(rawIDToken)
	if err != nil {
		return Claims{}, err
	}
	// "crit" is rejected outright: an extension this package cannot evaluate
	// must not be silently ignored.
	if len(header.Crit) > 0 {
		return Claims{}, errors.Join(ErrIDTokenInvalid, errors.New("critical header extensions are not supported"))
	}
	alg, canonicalOK := canonicalAlgorithm(header.Alg)
	if !canonicalOK {
		// Rejected again here even though ParseAlgorithms rejects it at
		// configuration time, because the allowlist and the verification path are
		// separate defences and neither may be assumed to have run.
		return Claims{}, ErrUnsupportedAlgorithm
	}
	allowed := false
	for _, candidate := range p.algorithms() {
		if candidate == alg {
			allowed = true
			break
		}
	}
	if !allowed {
		return Claims{}, ErrUnsupportedAlgorithm
	}

	endpoints, err := rp.Discover(ctx, p)
	if err != nil {
		return Claims{}, err
	}
	key, err := rp.findKey(ctx, p, endpoints.JWKS, header.Kid)
	if err != nil {
		return Claims{}, err
	}
	if err := verifyJWS(alg, key.PublicKey, []byte(signingInput), signature); err != nil {
		return Claims{}, errors.Join(ErrIDTokenInvalid, err)
	}

	payload, err := base64.RawURLEncoding.DecodeString(payloadSegment)
	if err != nil {
		return Claims{}, errors.Join(ErrIDTokenInvalid, err)
	}
	var temporal jwtTimeClaims
	if err := json.Unmarshal(payload, &temporal); err != nil {
		return Claims{}, errors.Join(ErrIDTokenInvalid, err)
	}
	raw := map[string]any{}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Claims{}, errors.Join(ErrIDTokenInvalid, err)
	}
	if err := rp.validateClaims(p, temporal, raw, expectedNonce, accessToken, alg); err != nil {
		return Claims{}, err
	}
	claims := Claims{
		Subject:  strings.TrimSpace(temporal.Subject),
		Email:    stringClaim(raw, "email"),
		Raw:      raw,
		Username: stringClaim(raw, p.usernameClaimName()),
	}
	claims.EmailVerified = boolClaim(raw, "email_verified")
	if claims.Subject == "" {
		return Claims{}, errors.Join(ErrIDTokenInvalid, errors.New("sub claim missing"))
	}
	return claims, nil
}

// validateClaims applies the audience, issuer, lifetime, nonce, and access-token
// binding checks. Each one exists to stop a specific substitution attack, so the
// reasons are kept distinct internally while the caller still sees one sentinel.
func (rp *RelyingParty) validateClaims(p Provider, temporal jwtTimeClaims, raw map[string]any, expectedNonce, accessToken, alg string) error {
	if strings.TrimSpace(temporal.Issuer) == "" {
		return errors.Join(ErrIDTokenInvalid, errors.New("iss claim missing"))
	}
	if strings.TrimSuffix(temporal.Issuer, "/") != strings.TrimSuffix(strings.TrimSpace(p.Issuer), "/") {
		return errors.Join(ErrIDTokenInvalid, errors.New("iss mismatch"))
	}
	if !audienceContains(temporal.Audience, p.ClientID) {
		return errors.Join(ErrIDTokenInvalid, errors.New("aud mismatch"))
	}
	now := rp.now()
	skew := rp.cfg.ClockSkew
	if temporal.Expiry == nil {
		return errors.Join(ErrIDTokenInvalid, errors.New("exp claim missing"))
	}
	if now.After(time.Unix(*temporal.Expiry, 0).Add(skew)) {
		return errors.Join(ErrIDTokenInvalid, errors.New("token expired"))
	}
	if temporal.NotBefore != nil && now.Add(skew).Before(time.Unix(*temporal.NotBefore, 0)) {
		return errors.Join(ErrIDTokenInvalid, errors.New("token not yet valid"))
	}
	// The nonce binds this id_token to the redirect this process started, which
	// is what stops a captured token from being replayed into a new login.
	if expectedNonce != "" && temporal.Nonce != expectedNonce {
		return errors.Join(ErrIDTokenInvalid, errors.New("nonce mismatch"))
	}
	// at_hash is only checked when an access token was actually returned and the
	// IdP included the claim; requiring it unconditionally would break IdPs that
	// omit it, and omitting the check when present would allow token substitution.
	if accessToken != "" && temporal.AtHash != "" {
		expected, err := accessTokenHash(alg, accessToken)
		if err != nil {
			return errors.Join(ErrIDTokenInvalid, err)
		}
		if temporal.AtHash != expected {
			return errors.Join(ErrIDTokenInvalid, errors.New("at_hash mismatch"))
		}
	}
	return nil
}

// audienceContains handles both the string and array forms of aud.
func audienceContains(audience any, clientID string) bool {
	if strings.TrimSpace(clientID) == "" {
		return false
	}
	switch value := audience.(type) {
	case string:
		return value == clientID
	case []any:
		for _, entry := range value {
			if candidate, ok := entry.(string); ok && candidate == clientID {
				return true
			}
		}
	}
	return false
}

// accessTokenHash computes the JWS at_hash value: base64url of the left half of
// the hash the signing algorithm uses.
func accessTokenHash(alg, accessToken string) (string, error) {
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
		return "", errors.New("unsupported hash for at_hash")
	}
	return base64.RawURLEncoding.EncodeToString(digest[:len(digest)/2]), nil
}

// hashFor maps a JWS algorithm to its digest. EdDSA uses SHA-512 by definition.
func hashFor(alg string) crypto.Hash {
	switch strings.ToUpper(alg) {
	case "RS256", "PS256", "ES256":
		return crypto.SHA256
	case "RS384", "PS384", "ES384":
		return crypto.SHA384
	case "RS512", "PS512", "ES512", "EDDSA":
		return crypto.SHA512
	default:
		return 0
	}
}

// splitJWS validates the compact serialization shape before anything is decoded,
// so a malformed token cannot panic the verifier.
func splitJWS(raw string) (signingInput, payloadSegment string, signature []byte, header jwtHeader, err error) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return "", "", nil, jwtHeader{}, errors.Join(ErrIDTokenInvalid, errors.New("empty token"))
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", nil, jwtHeader{}, errors.Join(ErrIDTokenInvalid, errors.New("malformed token"))
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", nil, jwtHeader{}, errors.Join(ErrIDTokenInvalid, err)
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[1]); err != nil {
		return "", "", nil, jwtHeader{}, errors.Join(ErrIDTokenInvalid, err)
	}
	signature, err = base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", "", nil, jwtHeader{}, errors.Join(ErrIDTokenInvalid, err)
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return "", "", nil, jwtHeader{}, errors.Join(ErrIDTokenInvalid, err)
	}
	return parts[0] + "." + parts[1], parts[1], signature, header, nil
}

// verifyJWS dispatches to the right primitive. Each branch fails closed: an
// unexpected key type for the algorithm is an error, not a skip.
func verifyJWS(alg string, publicKey interface{}, signingInput, signature []byte) error {
	switch {
	case strings.HasPrefix(alg, "RS"):
		key, ok := publicKey.(*rsa.PublicKey)
		if !ok {
			return errors.New("key type does not match algorithm")
		}
		digest, err := digestFor(alg, signingInput)
		if err != nil {
			return err
		}
		return rsa.VerifyPKCS1v15(key, hashFor(alg), digest, signature)
	case strings.HasPrefix(alg, "PS"):
		key, ok := publicKey.(*rsa.PublicKey)
		if !ok {
			return errors.New("key type does not match algorithm")
		}
		digest, err := digestFor(alg, signingInput)
		if err != nil {
			return err
		}
		// SaltLengthAuto accepts whatever salt length the signer used, which is
		// what interoperability with mainstream IdPs requires.
		return rsa.VerifyPSS(key, hashFor(alg), digest, signature, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthAuto, Hash: hashFor(alg)})
	case strings.HasPrefix(alg, "ES"):
		key, ok := publicKey.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("key type does not match algorithm")
		}
		digest, err := digestFor(alg, signingInput)
		if err != nil {
			return err
		}
		// A JWS ECDSA signature is the fixed-width concatenation r||s, not the
		// ASN.1 sequence crypto/ecdsa produces, so it is split by hand.
		half := len(signature) / 2
		if len(signature)%2 != 0 || half == 0 {
			return errors.New("malformed ecdsa signature")
		}
		r := new(big.Int).SetBytes(signature[:half])
		s := new(big.Int).SetBytes(signature[half:])
		if !ecdsa.Verify(key, digest, r, s) {
			return errors.New("ecdsa verification failed")
		}
		return nil
	case strings.ToUpper(alg) == "EDDSA":
		key, ok := publicKey.(ed25519.PublicKey)
		if !ok {
			return errors.New("key type does not match algorithm")
		}
		// Ed25519 signs the message itself; there is no separate digest step.
		if !ed25519.Verify(key, signingInput, signature) {
			return errors.New("ed25519 verification failed")
		}
		return nil
	default:
		return ErrUnsupportedAlgorithm
	}
}

func digestFor(alg string, signingInput []byte) ([]byte, error) {
	var hasher hash.Hash
	switch hashFor(alg) {
	case crypto.SHA256:
		hasher = sha256.New()
	case crypto.SHA384:
		hasher = sha512.New384()
	case crypto.SHA512:
		hasher = sha512.New()
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedAlgorithm, alg)
	}
	hasher.Write(signingInput)
	return hasher.Sum(nil), nil
}
