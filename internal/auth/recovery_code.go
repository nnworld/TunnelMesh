package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
)

// Recovery codes are single-use MFA bypass credentials. Each code carries 128
// bits of entropy, so a fast hash is appropriate: Argon2id would add 64 MB of
// work per verification without protecting against any realistic attack on a
// value the attacker cannot guess. Only the SHA-256 digest is stored, and the
// plaintext code is shown to the user exactly once at enrollment.
const (
	recoveryCodePrefix  = "tmrc-"
	recoveryCodeEntropy = 16
	recoveryCodeDigits  = 20
)

// ErrRecoveryCodeCount reports a request for a non-positive number of codes.
var ErrRecoveryCodeCount = errors.New("recovery code count must be positive")

// GenerateRecoveryCodes returns count plaintext codes together with their
// SHA-256 digests. The caller persists only the digests and renders the
// plaintext once.
func GenerateRecoveryCodes(count int) (codes []string, hashes []string, err error) {
	if count <= 0 {
		return nil, nil, ErrRecoveryCodeCount
	}
	codes = make([]string, 0, count)
	hashes = make([]string, 0, count)
	seen := make(map[string]struct{}, count)
	for len(codes) < count {
		buffer := make([]byte, recoveryCodeEntropy)
		if _, err := rand.Read(buffer); err != nil {
			return nil, nil, fmt.Errorf("generate recovery code: %w", err)
		}
		body := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer)[:recoveryCodeDigits]
		code := recoveryCodePrefix + body
		if _, duplicate := seen[code]; duplicate {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
		hashes = append(hashes, string(HashRecoveryCode(code)))
	}
	return codes, hashes, nil
}

// HashRecoveryCode returns the hex-free raw digest that is safe to persist.
func HashRecoveryCode(code string) []byte {
	sum := sha256.Sum256([]byte(normalizeRecoveryCode(code)))
	return sum[:]
}

// RecoveryCodeMatches compares a candidate against a stored digest in constant
// time so response latency cannot reveal a partial match.
func RecoveryCodeMatches(storedHash []byte, candidate string) bool {
	if len(storedHash) != sha256.Size {
		return false
	}
	return hmac.Equal(storedHash, HashRecoveryCode(candidate))
}

// IsRecoveryCode distinguishes a recovery code from a numeric TOTP code so the
// login flow can pick the right verification path from one input field.
func IsRecoveryCode(candidate string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(candidate)), recoveryCodePrefix)
}

// normalizeRecoveryCode folds case and strips whitespace and separators, so a
// user who retypes a code with spaces still matches the stored digest.
func normalizeRecoveryCode(code string) string {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	return normalized
}

// RecoveryCodePrefix exposes the display prefix without leaking the layout.
func RecoveryCodePrefix() string { return recoveryCodePrefix }
