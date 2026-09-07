package auth

import (
	"encoding/base64"
	"testing"
)

func TestSecretStoreRoundTripAndTamperRejection(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	store, err := NewSecretStore(base64.RawStdEncoding.EncodeToString(key), "k1")
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, nonce, keyID, version, err := store.Encrypt("tm_secret")
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Decrypt(ciphertext, nonce, keyID, version)
	if err != nil || got != "tm_secret" {
		t.Fatalf("decrypt = %q, %v", got, err)
	}
	ciphertext[0] ^= 0xff
	if _, err := store.Decrypt(ciphertext, nonce, keyID, version); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestNewSecretStoreRejectsInvalidKey(t *testing.T) {
	if _, err := NewSecretStore("not-a-key", "k1"); err == nil {
		t.Fatal("expected invalid key error")
	}
}

func TestSecretStoreRejectsUnknownKeyIDAndVersion(t *testing.T) {
	key := make([]byte, 32)
	store, err := NewSecretStore(base64.RawStdEncoding.EncodeToString(key), "k1")
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, nonce, _, version, err := store.Encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decrypt(ciphertext, nonce, "k2", version); err == nil {
		t.Fatal("unknown key id accepted")
	}
	if _, err := store.Decrypt(ciphertext, nonce, "k1", version+1); err == nil {
		t.Fatal("unknown version accepted")
	}
}
