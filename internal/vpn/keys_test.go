package vpn_test

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// RFC 7748 §6.1 official X25519 test vectors, in the base64 form WireGuard
// uses. Pinning them proves the stdlib derives the same public key a real
// WireGuard peer would accept, which is the whole reason keys.go can avoid a
// third-party dependency.
const (
	rfcAlicePrivate = "dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo="
	rfcAlicePublic  = "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo="
	rfcBobPrivate   = "XasIfmJKikt54X+Lg4AO5m87sSkmGLb9HC+LJ/+I4Os="
	rfcBobPublic    = "3p7bfXt9wbTTW2HC7OQ1Nz+DQ8hbeGdNrfx+FG+IK08="
)

func TestDerivePublicKeyMatchesRFC7748Vectors(t *testing.T) {
	for _, tc := range []struct{ private, public string }{
		{rfcAlicePrivate, rfcAlicePublic},
		{rfcBobPrivate, rfcBobPublic},
	} {
		got, err := vpn.DerivePublicKey(tc.private)
		if err != nil {
			t.Fatalf("DerivePublicKey: %v", err)
		}
		if got != tc.public {
			t.Errorf("public key = %q, want %q", got, tc.public)
		}
	}
}

func TestGenerateKeyPairProducesUsableWireGuardKeys(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		pair, err := vpn.GenerateKeyPair()
		if err != nil {
			t.Fatalf("GenerateKeyPair: %v", err)
		}
		for name, key := range map[string]string{"private": pair.PrivateKey, "public": pair.PublicKey} {
			if len(key) != 44 {
				t.Fatalf("%s key length = %d chars, want 44", name, len(key))
			}
			raw, err := base64.StdEncoding.DecodeString(key)
			if err != nil {
				t.Fatalf("%s key is not standard base64: %v", name, err)
			}
			if len(raw) != 32 {
				t.Fatalf("%s key decodes to %d bytes, want 32", name, len(raw))
			}
		}
		if err := vpn.ValidatePrivateKey(pair.PrivateKey); err != nil {
			t.Fatalf("ValidatePrivateKey: %v", err)
		}
		if err := vpn.ValidatePublicKey(pair.PublicKey); err != nil {
			t.Fatalf("ValidatePublicKey: %v", err)
		}
		derived, err := vpn.DerivePublicKey(pair.PrivateKey)
		if err != nil {
			t.Fatalf("DerivePublicKey: %v", err)
		}
		if derived != pair.PublicKey {
			t.Fatal("the returned public key must be the one derived from the returned private key")
		}
		if _, dup := seen[pair.PublicKey]; dup {
			t.Fatal("GenerateKeyPair repeated a public key")
		}
		seen[pair.PublicKey] = struct{}{}
	}
}

// TestEncodeDecodeKeyRoundTrip covers the canonical wire form. Every WireGuard
// client expects standard base64 with padding, so a URL-safe or unpadded
// encoding here would silently break every peer config we hand out.
func TestEncodeDecodeKeyRoundTrip(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i*7 + 1)
	}
	encoded := vpn.EncodeKey(raw)
	if !strings.HasSuffix(encoded, "=") {
		t.Fatalf("encoded key %q must keep standard base64 padding", encoded)
	}
	got, err := vpn.DecodeKey(encoded)
	if err != nil {
		t.Fatalf("DecodeKey: %v", err)
	}
	if !equalBytes(got, raw) {
		t.Fatal("round trip changed the key bytes")
	}
}

func TestDecodeKeyRejectsMalformedInput(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"whitespace": "   ",
		"43 chars":   strings.TrimSuffix(rfcAlicePublic, "="),
		"45 chars":   rfcAlicePublic + "A",
		// The vector key contains "/" but no "+", so the URL-safe variant has
		// to be built from the "/" character.
		"url safe alphabet": strings.Replace(rfcAlicePublic, "/", "_", 1),
		"not base64":        strings.Repeat("!", 44),
		// 31 and 33 bytes both encode to 44 characters, so character count is
		// not a sufficient check: the decoded length must be 32.
		"31 bytes": base64.StdEncoding.EncodeToString(make([]byte, 31)),
		"33 bytes": base64.StdEncoding.EncodeToString(make([]byte, 33)),
	}
	for name, input := range cases {
		if _, err := vpn.DecodeKey(input); err == nil {
			t.Errorf("%s: DecodeKey(%q) accepted malformed input", name, input)
		}
	}
}

func TestValidatePublicKeyRejectsMalformedAndDegenerateKeys(t *testing.T) {
	reject := map[string]string{
		"empty":        "",
		"43 chars":     strings.TrimSuffix(rfcAlicePublic, "="),
		"45 chars":     rfcAlicePublic + "A",
		"not base64":   strings.Repeat("!", 44),
		"31 bytes":     base64.StdEncoding.EncodeToString(make([]byte, 31)),
		"33 bytes":     base64.StdEncoding.EncodeToString(make([]byte, 33)),
		"all zero key": vpn.EncodeKey(make([]byte, 32)),
	}
	for name, input := range reject {
		err := vpn.ValidatePublicKey(input)
		if err == nil {
			t.Errorf("%s: ValidatePublicKey accepted %q", name, input)
			continue
		}
		var apiErr *vpn.Error
		if !errors.As(err, &apiErr) {
			t.Errorf("%s: error %v is not a *vpn.Error", name, err)
			continue
		}
		if apiErr.Code != "vpn_peer_invalid" || apiErr.Status != http.StatusBadRequest {
			t.Errorf("%s: got %d/%s, want 400/vpn_peer_invalid", name, apiErr.Status, apiErr.Code)
		}
	}
	if err := vpn.ValidatePublicKey(rfcAlicePublic); err != nil {
		t.Errorf("a valid RFC 7748 public key was rejected: %v", err)
	}
}

// TestAllZeroPrivateKeyDerivesAnAcceptablePublicKey documents a deliberate
// asymmetry. crypto/ecdh only checks the X25519 scalar length, so an all-zero
// private key is mathematically usable and derives a non-degenerate public key;
// the public key must therefore pass validation. It is the private key that is
// rejected, because an all-zero node identity is guessable by anyone.
func TestAllZeroPrivateKeyDerivesAnAcceptablePublicKey(t *testing.T) {
	zeroPrivate := vpn.EncodeKey(make([]byte, 32))
	public, err := vpn.DerivePublicKey(zeroPrivate)
	if err != nil {
		t.Fatalf("DerivePublicKey: %v", err)
	}
	if public == vpn.EncodeKey(make([]byte, 32)) {
		t.Fatal("an all-zero scalar must not derive an all-zero public key")
	}
	if err := vpn.ValidatePublicKey(public); err != nil {
		t.Fatalf("the derived public key must validate: %v", err)
	}
	if err := vpn.ValidatePrivateKey(zeroPrivate); err == nil {
		t.Fatal("an all-zero private key must be rejected")
	}
	if err := vpn.ValidatePrivateKey(rfcAlicePrivate); err != nil {
		t.Fatalf("a valid private key was rejected: %v", err)
	}
	// A public key and a private scalar are the same 32 opaque bytes on the
	// wire, so no format check can tell them apart; ValidatePrivateKey can only
	// reject malformed or degenerate scalars, not "the wrong kind of key".
	for name, input := range map[string]string{
		"empty":    "",
		"31 bytes": base64.StdEncoding.EncodeToString(make([]byte, 31)),
		"43 chars": strings.TrimSuffix(rfcAlicePrivate, "="),
	} {
		if err := vpn.ValidatePrivateKey(input); err == nil {
			t.Errorf("%s: ValidatePrivateKey accepted %q", name, input)
		}
	}
}

// TestDerivedPublicKeyIsImportableByStdlib is the compatibility guarantee the
// phase-6 wireguard-go endpoint will rely on: every key this package hands out
// imports into crypto/ecdh as a valid X25519 public key.
func TestDerivedPublicKeyIsImportableByStdlib(t *testing.T) {
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	raw, err := vpn.DecodeKey(pair.PublicKey)
	if err != nil {
		t.Fatalf("DecodeKey: %v", err)
	}
	if _, err := ecdh.X25519().NewPublicKey(raw); err != nil {
		t.Fatalf("the generated public key does not import: %v", err)
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
