package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

const secretStoreVersion = 1

// SecretStore encrypts recoverable bearer secrets. The key is supplied by the
// process environment/secret manager and is never persisted by this package.
type SecretStore struct {
	aead  cipher.AEAD
	keyID string
}

func NewSecretStore(encodedKey, keyID string) (*SecretStore, error) {
	if keyID == "" {
		return nil, errors.New("secret key id is required")
	}
	key, err := decodeSecretKey(encodedKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create secret cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create secret AEAD: %w", err)
	}
	return &SecretStore{aead: aead, keyID: keyID}, nil
}

func decodeSecretKey(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("secret encryption key is required")
	}
	if key, err := base64.RawStdEncoding.DecodeString(value); err == nil && validAESKeyLen(len(key)) {
		return key, nil
	}
	if key, err := base64.StdEncoding.DecodeString(value); err == nil && validAESKeyLen(len(key)) {
		return key, nil
	}
	if key, err := hex.DecodeString(value); err == nil && validAESKeyLen(len(key)) {
		return key, nil
	}
	return nil, errors.New("secret encryption key must decode to 16, 24, or 32 bytes")
}

func validAESKeyLen(n int) bool { return n == 16 || n == 24 || n == 32 }

func (s *SecretStore) Encrypt(secret string) ([]byte, []byte, string, int, error) {
	if s == nil || s.aead == nil {
		return nil, nil, "", 0, errors.New("secret store is unavailable")
	}
	if secret == "" {
		return nil, nil, "", 0, errors.New("secret must not be empty")
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, "", 0, fmt.Errorf("generate secret nonce: %w", err)
	}
	ciphertext := s.aead.Seal(nil, nonce, []byte(secret), nil)
	return ciphertext, nonce, s.keyID, secretStoreVersion, nil
}

func (s *SecretStore) Decrypt(ciphertext, nonce []byte, keyID string, version int) (string, error) {
	if s == nil || s.aead == nil {
		return "", errors.New("secret store is unavailable")
	}
	if keyID != s.keyID {
		return "", errors.New("unknown secret key id")
	}
	if version != secretStoreVersion {
		return "", errors.New("unsupported secret version")
	}
	if len(nonce) != s.aead.NonceSize() {
		return "", errors.New("invalid secret nonce")
	}
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("secret ciphertext authentication failed")
	}
	return string(plaintext), nil
}
