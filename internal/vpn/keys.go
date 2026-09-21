package vpn

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// keySize is the X25519 scalar and point size. Every WireGuard key, private or
// public, is exactly this many bytes rendered as 44 characters of standard
// base64 with padding.
const keySize = 32

// KeyPair is one WireGuard identity: the private scalar a peer keeps secret and
// the public point it publishes. Both halves are stored in the canonical base64
// text form, which is what wg(8), every client GUI and the [Interface] and
// [Peer] sections of a configuration file use, so no caller has to re-encode.
type KeyPair struct {
	PrivateKey string
	PublicKey  string
}

// GenerateKeyPair creates a fresh identity from crypto/rand.
//
// It uses the standard library rather than a WireGuard package because the key
// format is nothing more than an X25519 scalar: crypto/ecdh implements RFC 7748
// and produces byte-identical results to wireguard-go, which the package tests
// pin against the official RFC 7748 §6.1 vectors. Keeping this dependency-free
// is what lets the control plane build without the "vpn" tag.
func GenerateKeyPair() (KeyPair, error) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("vpn: generate x25519 key: %w", err)
	}
	return KeyPair{
		PrivateKey: EncodeKey(private.Bytes()),
		PublicKey:  EncodeKey(private.PublicKey().Bytes()),
	}, nil
}

// DerivePublicKey returns the public key for a base64 private key.
//
// It is pure mathematics and therefore accepts any 32-byte scalar, including a
// degenerate one; use ValidatePrivateKey when the key came from an operator or
// a configuration file and must be safe to trust.
func DerivePublicKey(privateKey string) (string, error) {
	raw, err := decodeKey(privateKey)
	if err != nil {
		return "", err
	}
	// X25519 clamping happens inside the scalar multiplication, exactly as
	// wireguard-go does it, so the caller must not pre-clamp the bytes.
	private, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("vpn: import x25519 private key: %w", err)
	}
	return EncodeKey(private.PublicKey().Bytes()), nil
}

// EncodeKey renders raw key bytes as the canonical WireGuard base64 text form.
func EncodeKey(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

// DecodeKey parses a canonical WireGuard key and returns its 32 raw bytes.
//
// The decoded length is checked rather than the encoded one: 31 and 33 bytes
// both render as 44 characters, so a length test on the string would accept
// keys that no WireGuard implementation can use.
func DecodeKey(encoded string) ([]byte, error) {
	return decodeKey(encoded)
}

// ValidatePublicKey reports whether a base64 string is usable as a peer's
// public key, returning the stable vpn_peer_invalid code so an HTTP caller can
// surface it directly.
//
// The all-zero point is rejected even though crypto/ecdh imports it happily: it
// is a low-order point that contributes nothing to the handshake, so accepting
// it would let a caller register a peer that can never connect while the row
// looks healthy.
func ValidatePublicKey(encoded string) error {
	raw, err := decodeKey(encoded)
	if err != nil {
		return ErrPeerInvalid.WithMessage(err.Error())
	}
	if isAllZero(raw) {
		return ErrPeerInvalid.WithMessage("vpn public key must not be the all-zero point")
	}
	if _, err := ecdh.X25519().NewPublicKey(raw); err != nil {
		return ErrPeerInvalid.WithMessage(fmt.Sprintf("vpn public key is not a valid x25519 point: %v", err))
	}
	return nil
}

// ValidatePrivateKey reports whether a base64 string is usable as a private
// key. It exists for the operator-supplied node identity, which arrives through
// an environment variable and must fail fast at startup rather than producing a
// gateway nobody can handshake with.
//
// The all-zero scalar is rejected: crypto/ecdh accepts it because X25519 only
// checks the scalar length, but an all-zero identity is guessable by anyone and
// must never be trusted as a node key.
func ValidatePrivateKey(encoded string) error {
	raw, err := decodeKey(encoded)
	if err != nil {
		return ErrPeerInvalid.WithMessage(err.Error())
	}
	if isAllZero(raw) {
		return ErrPeerInvalid.WithMessage("vpn private key must not be all zero")
	}
	if _, err := ecdh.X25519().NewPrivateKey(raw); err != nil {
		return ErrPeerInvalid.WithMessage(fmt.Sprintf("vpn private key is not a valid x25519 scalar: %v", err))
	}
	return nil
}

func decodeKey(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("vpn: key is not standard base64: %w", err)
	}
	if len(raw) != keySize {
		return nil, fmt.Errorf("vpn: key must decode to %d bytes, got %d", keySize, len(raw))
	}
	return raw, nil
}

func isAllZero(raw []byte) bool {
	for _, b := range raw {
		if b != 0 {
			return false
		}
	}
	return true
}
