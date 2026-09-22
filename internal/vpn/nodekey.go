package vpn

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// NodePrivateKeyEnv is the only channel through which the gateway's own
// WireGuard identity enters the process.
//
// It is an environment variable rather than a server.vpn key for the same
// reason TUNNELMESH_TOKEN_ENCRYPTION_KEY is: a configuration file is copied,
// backed up, rendered into a support bundle and committed to version control,
// while an environment variable can come from a secret manager and never touch
// disk. Losing the node key does not lose data - peers can be re-issued - but it
// does invalidate every configuration already handed to a user, so it is treated
// as a startup prerequisite rather than an optional tuning knob.
const NodePrivateKeyEnv = "TUNNELMESH_VPN_NODE_PRIVATE_KEY"

var (
	// ErrNodeKeyMissing means the gateway was enabled without an identity. It is
	// a distinct error from an unusable key because the remedy differs: one is
	// "inject the secret", the other is "the secret you injected is wrong".
	ErrNodeKeyMissing = errors.New("vpn: " + NodePrivateKeyEnv + " is not set, so the gateway has no wireguard identity")
	// ErrNodeKeyInvalid means a value was supplied but cannot be used. The
	// wrapped detail describes the shape of the problem and never the value.
	ErrNodeKeyInvalid = errors.New("vpn: " + NodePrivateKeyEnv + " is not a usable wireguard private key")
)

// NodeIdentity is the gateway's own key pair.
//
// PrivateKey is kept in the canonical base64 form even when the operator
// supplied hex, so every later consumer - the UAPI renderer, the ini renderer -
// sees one spelling and cannot produce two different configurations for one
// identity. PublicKey is derived rather than supplied: an operator who pastes
// both halves of a key pair can paste a mismatched pair, and a mismatch is only
// discoverable by a user whose handshake fails.
type NodeIdentity struct {
	PrivateKey string
	PublicKey  string
}

// String renders the identity without its private half.
//
// The method exists so that a stray "%v" on a value that happens to contain the
// node identity cannot write the key into a log line, an audit entry or an error
// message. The public key is included because it is the only way an operator can
// tell two gateways apart in a log, and it is not a secret: it is published in
// every peer configuration the gateway hands out.
func (n NodeIdentity) String() string {
	if n.PublicKey == "" {
		return "vpn node identity (unset)"
	}
	return "vpn node identity " + n.PublicKey
}

// NodeIdentityFromEnvironment reads and validates the node identity.
//
// It fails fast by design: a gateway that starts without an identity would
// accept no handshakes at all, and the symptom a user reports ("the tunnel is up
// but nothing is reachable") points nowhere near a missing environment variable.
func NodeIdentityFromEnvironment() (NodeIdentity, error) {
	return ParseNodeIdentity(os.Getenv(NodePrivateKeyEnv))
}

// ParseNodeIdentity validates a supplied key and derives its public half.
//
// Both the base64 form WireGuard canonically uses and the 64-character hex form
// are accepted, because tooling differs: wg(8) prints base64 while most key
// generation one-liners and every hex dump print hex. Refusing one of them would
// produce an error the operator cannot act on without knowing which spelling the
// product happens to want.
func ParseNodeIdentity(raw string) (NodeIdentity, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return NodeIdentity{}, ErrNodeKeyMissing
	}
	canonical, err := normalizeNodePrivateKey(value)
	if err != nil {
		return NodeIdentity{}, fmt.Errorf("%w: %s", ErrNodeKeyInvalid, err)
	}
	if err := ValidatePrivateKey(canonical); err != nil {
		// ValidatePrivateKey reports the shape of the problem ("must not be all
		// zero", "not a valid x25519 scalar") and never the key itself, so its
		// message is safe to carry into a startup failure.
		return NodeIdentity{}, fmt.Errorf("%w: %s", ErrNodeKeyInvalid, reasonOf(err))
	}
	publicKey, err := DerivePublicKey(canonical)
	if err != nil {
		return NodeIdentity{}, fmt.Errorf("%w: %s", ErrNodeKeyInvalid, reasonOf(err))
	}
	return NodeIdentity{PrivateKey: canonical, PublicKey: publicKey}, nil
}

// normalizeNodePrivateKey rewrites a hex-encoded key into the canonical base64
// form and passes a base64 key through unchanged.
//
// The returned reasons describe the expected shape only. Including the supplied
// value would put key material into a startup log line, which is the one place
// an operator is most likely to copy text out of and paste into a ticket.
func normalizeNodePrivateKey(value string) (string, error) {
	if len(value) == 2*keySize {
		if raw, err := hex.DecodeString(value); err == nil {
			return EncodeKey(raw), nil
		}
	}
	raw, err := decodeKey(value)
	if err != nil {
		return "", errors.New("expected 32 bytes as 44 characters of base64 or 64 characters of hex")
	}
	return EncodeKey(raw), nil
}

// reasonOf strips the vpn package's own error prefix so a wrapped message reads
// as one sentence instead of two nested ones.
func reasonOf(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if apiError, ok := err.(*Error); ok {
		message = apiError.Error()
	}
	return strings.TrimPrefix(strings.TrimSpace(message), "vpn: ")
}
