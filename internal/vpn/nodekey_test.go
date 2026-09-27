package vpn_test

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// The node key is the one VPN secret that never lives in a configuration file,
// so these tests pin the environment contract: which encodings an operator may
// paste, what is refused, and above all that a refusal never echoes the key.
func TestNodeIdentityFromEnvironmentRequiresTheKey(t *testing.T) {
	t.Setenv(vpn.NodePrivateKeyEnv, "")
	_, err := vpn.NodeIdentityFromEnvironment()
	if !errors.Is(err, vpn.ErrNodeKeyMissing) {
		t.Fatalf("error = %v, want ErrNodeKeyMissing", err)
	}
	if !strings.Contains(err.Error(), vpn.NodePrivateKeyEnv) {
		t.Errorf("error %q does not name %s, so the operator cannot tell which variable to set", err, vpn.NodePrivateKeyEnv)
	}

	t.Setenv(vpn.NodePrivateKeyEnv, "   ")
	if _, err := vpn.NodeIdentityFromEnvironment(); !errors.Is(err, vpn.ErrNodeKeyMissing) {
		t.Errorf("a whitespace-only key gave %v, want ErrNodeKeyMissing", err)
	}
}

func TestNodeIdentityFromEnvironmentAcceptsBase64AndHex(t *testing.T) {
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(pair.PrivateKey)
	if err != nil {
		t.Fatalf("generated key is not base64: %v", err)
	}
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"base64", pair.PrivateKey},
		{"base64 with surrounding whitespace", "  " + pair.PrivateKey + "\n"},
		{"hex", hex.EncodeToString(raw)},
		{"hex upper case", strings.ToUpper(hex.EncodeToString(raw))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(vpn.NodePrivateKeyEnv, tc.value)
			identity, err := vpn.NodeIdentityFromEnvironment()
			if err != nil {
				t.Fatalf("NodeIdentityFromEnvironment: %v", err)
			}
			if identity.PrivateKey != pair.PrivateKey {
				t.Errorf("private key was not normalised to the canonical base64 form")
			}
			if identity.PublicKey != pair.PublicKey {
				t.Errorf("public key = %q, want %q", identity.PublicKey, pair.PublicKey)
			}
			if err := vpn.ValidatePublicKey(identity.PublicKey); err != nil {
				t.Errorf("derived public key is not usable: %v", err)
			}
		})
	}
}

func TestNodeIdentityFromEnvironmentRefusesUnusableKeys(t *testing.T) {
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(pair.PrivateKey)
	if err != nil {
		t.Fatalf("generated key is not base64: %v", err)
	}
	short := raw[:31]
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"all zero base64", vpn.EncodeKey(make([]byte, 32))},
		{"all zero hex", hex.EncodeToString(make([]byte, 32))},
		{"31 bytes", vpn.EncodeKey(short)},
		{"33 bytes", vpn.EncodeKey(append(append([]byte{}, raw...), 0x01))},
		{"not an encoding", "correct-horse-battery-staple"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(vpn.NodePrivateKeyEnv, tc.value)
			identity, err := vpn.NodeIdentityFromEnvironment()
			if !errors.Is(err, vpn.ErrNodeKeyInvalid) {
				t.Fatalf("error = %v, want ErrNodeKeyInvalid", err)
			}
			if identity.PrivateKey != "" || identity.PublicKey != "" {
				t.Errorf("a refused key must not be returned, got %+v", identity)
			}
			// The whole point of refusing at startup is that the operator can
			// read why. Echoing the key into a log line to achieve that would
			// turn a diagnostic into a leak, so the message is asserted clean.
			if strings.Contains(err.Error(), tc.value) {
				t.Errorf("error message echoes the supplied key material")
			}
			if len(tc.value) == 44 && strings.Contains(err.Error(), tc.value[:16]) {
				t.Errorf("error message echoes a prefix of the supplied key material")
			}
			if !strings.Contains(err.Error(), vpn.NodePrivateKeyEnv) {
				t.Errorf("error %q does not name %s", err, vpn.NodePrivateKeyEnv)
			}
		})
	}
}

// The identity is passed through log-capable code paths, so its default
// formatting is asserted to be leak-proof rather than left to chance.
func TestNodeIdentityStringNeverCarriesThePrivateKey(t *testing.T) {
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	identity := vpn.NodeIdentity{PrivateKey: pair.PrivateKey, PublicKey: pair.PublicKey}
	rendered := identity.String()
	if strings.Contains(rendered, pair.PrivateKey) {
		t.Errorf("String() leaks the private key: %q", rendered)
	}
	if !strings.Contains(rendered, pair.PublicKey) {
		t.Errorf("String() = %q, want it to name the public key so an operator can tell nodes apart", rendered)
	}
}
