package auth

import (
	"os"
	"strings"
)

// SecretProvider is the seam every identity service uses to seal and open a
// recoverable secret. It keeps the services testable without a real key and
// gives one place to enforce the fail-closed rule: when Available reports
// false, callers must refuse the operation instead of storing plaintext.
type SecretProvider interface {
	Available() bool
	Encrypt(plaintext string) (ciphertext, nonce []byte, keyID string, version int, err error)
	Decrypt(ciphertext, nonce []byte, keyID string, version int) (string, error)
}

// SecretEnvelope is the persisted form of a sealed secret.
type SecretEnvelope struct {
	Ciphertext string
	Nonce      string
	KeyID      string
	Version    int
}

// IsZero reports whether the envelope carries no secret.
func (e SecretEnvelope) IsZero() bool { return e.Ciphertext == "" }

type secretStoreProvider struct{ store *SecretStore }

// NewSecretProvider adapts a SecretStore to the SecretProvider contract. A nil
// store yields a provider whose Available reports false, which is how a
// deployment without TUNNELMESH_TOKEN_ENCRYPTION_KEY fails closed.
func NewSecretProvider(store *SecretStore) SecretProvider {
	if store == nil {
		return unavailableSecretProvider{}
	}
	return secretStoreProvider{store: store}
}

func (p secretStoreProvider) Available() bool { return p.store != nil }

func (p secretStoreProvider) Encrypt(plaintext string) ([]byte, []byte, string, int, error) {
	return p.store.Encrypt(plaintext)
}

func (p secretStoreProvider) Decrypt(ciphertext, nonce []byte, keyID string, version int) (string, error) {
	return p.store.Decrypt(ciphertext, nonce, keyID, version)
}

type unavailableSecretProvider struct{}

func (unavailableSecretProvider) Available() bool { return false }

func (unavailableSecretProvider) Encrypt(string) ([]byte, []byte, string, int, error) {
	return nil, nil, "", 0, ErrSecretStorageUnavailable
}

func (unavailableSecretProvider) Decrypt([]byte, []byte, string, int) (string, error) {
	return "", ErrSecretStorageUnavailable
}

// SecretProviderFromEnv builds the provider from the environment-injected key.
// The key is never persisted, logged, or returned; it is read once at startup
// exactly as the credential and token services already do.
func SecretProviderFromEnv() SecretProvider {
	encodedKey := strings.TrimSpace(os.Getenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY"))
	if encodedKey == "" {
		return NewSecretProvider(nil)
	}
	keyID := strings.TrimSpace(os.Getenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID"))
	if keyID == "" {
		keyID = "default"
	}
	store, err := NewSecretStore(encodedKey, keyID)
	if err != nil {
		return NewSecretProvider(nil)
	}
	return NewSecretProvider(store)
}
