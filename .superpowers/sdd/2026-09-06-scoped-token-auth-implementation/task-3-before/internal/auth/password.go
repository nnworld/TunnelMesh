package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory  = 64 * 1024
	argonTime    = 3
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

func hashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(hash)), nil
}

// HashPassword returns an Argon2id PHC string suitable for persistence.
func HashPassword(password string) (string, error) { return hashPassword(password) }

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	params := map[string]uint32{}
	for _, p := range strings.Split(parts[3], ",") {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			return false
		}
		n, err := strconv.ParseUint(kv[1], 10, 32)
		if err != nil {
			return false
		}
		params[kv[0]] = uint32(n)
	}
	memory, okM := params["m"]
	timeCost, okT := params["t"]
	threads, okP := params["p"]
	// Bound parameters read from storage so a corrupted record cannot force an
	// unbounded memory/time allocation during login.
	if !okM || !okT || !okP || memory < 8*1024 || memory > 1024*1024 || timeCost == 0 || timeCost > 10 || threads == 0 || threads > 32 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[4])
	want, err2 := enc.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(salt) > 64 || len(want) == 0 || len(want) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, timeCost, memory, uint8(threads), uint32(len(want)))
	return constantTimeEqual(got, want)
}

// VerifyPassword checks a clear-text candidate against an Argon2id PHC string.
func VerifyPassword(password, encoded string) bool { return verifyPassword(password, encoded) }

func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}

func randomToken(n int) (string, error) {
	if n < 32 {
		n = 32
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
