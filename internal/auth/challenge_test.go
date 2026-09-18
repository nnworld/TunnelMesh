package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func newChallengeStore(t *testing.T) (*ChallengeStore, *storage.DB) {
	t.Helper()
	db := newIdentityStore(t)
	return NewChallengeStore(db, testSecretProvider(t)), db
}

// TestChallengeRoundTripEncryptsPayload proves the payload survives a write and
// read cycle and that the database never holds a plaintext value.
func TestChallengeRoundTripEncryptsPayload(t *testing.T) {
	store, db := newChallengeStore(t)
	ctx := context.Background()
	payload := map[string]string{"nonce": "super-secret-nonce", "verifier": "pkce-verifier-value"}

	id, err := store.Create(ctx, storage.ChallengeKindOIDCState, "user-1", payload, time.Minute, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == "" {
		t.Fatal("expected a challenge id")
	}

	loaded, err := store.Load(ctx, id, storage.ChallengeKindOIDCState)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.UserID != "user-1" {
		t.Fatalf("user id = %q", loaded.UserID)
	}
	for key, want := range payload {
		if loaded.Payload[key] != want {
			t.Fatalf("payload[%s] = %q, want %q", key, loaded.Payload[key], want)
		}
	}

	var row storage.AuthChallenge
	if err := db.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		found, err := repos.Challenges.Get(ctx, id)
		row = found
		return err
	}); err != nil {
		t.Fatalf("read row: %v", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(row.PayloadCiphertext)
	if err != nil {
		t.Fatalf("ciphertext is not base64: %v", err)
	}
	for _, secret := range []string{"super-secret-nonce", "pkce-verifier-value"} {
		if strings.Contains(row.PayloadCiphertext, secret) || strings.Contains(string(ciphertext), secret) {
			t.Fatalf("plaintext %q leaked into the stored payload", secret)
		}
	}
}

// TestChallengeLoadWrongKindRejected keeps one flow from replaying another
// flow's identifier, which is why the kind is part of the lookup contract.
func TestChallengeLoadWrongKindRejected(t *testing.T) {
	store, _ := newChallengeStore(t)
	ctx := context.Background()
	id, err := store.Create(ctx, storage.ChallengeKindLoginMFA, "user-1", map[string]string{"k": "v"}, time.Minute, 3)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.Load(ctx, id, storage.ChallengeKindLoginTicket); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("err = %v, want ErrChallengeInvalid", err)
	}
}

// TestChallengeLoadAfterExpiryRejected asserts an expired row is
// indistinguishable from a missing one so the response cannot leak which
// identifier was once valid.
func TestChallengeLoadAfterExpiryRejected(t *testing.T) {
	store, _ := newChallengeStore(t)
	ctx := context.Background()
	id, err := store.Create(ctx, storage.ChallengeKindLoginMFA, "user-1", nil, 50*time.Millisecond, 3)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	_, err = store.Load(ctx, id, storage.ChallengeKindLoginMFA)
	if !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("err = %v, want ErrChallengeInvalid", err)
	}
	if strings.Contains(err.Error(), "expir") {
		t.Fatalf("error %q leaks the expiry reason", err)
	}
}

func TestChallengeConsumeIsSingleUse(t *testing.T) {
	store, _ := newChallengeStore(t)
	ctx := context.Background()
	id, err := store.Create(ctx, storage.ChallengeKindLoginTicket, "user-1", map[string]string{"k": "v"}, time.Minute, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := store.Consume(ctx, id)
	if err != nil || !first {
		t.Fatalf("first consume = %v, %v", first, err)
	}
	second, err := store.Consume(ctx, id)
	if err != nil || second {
		t.Fatalf("second consume = %v, %v", second, err)
	}
	if _, err := store.Load(ctx, id, storage.ChallengeKindLoginTicket); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("load after consume = %v, want ErrChallengeInvalid", err)
	}
}

// TestChallengeFailCountsAttempts covers the budget that stops an offline
// guessing loop against a six-digit code.
func TestChallengeFailCountsAttempts(t *testing.T) {
	store, _ := newChallengeStore(t)
	ctx := context.Background()
	id, err := store.Create(ctx, storage.ChallengeKindLoginMFA, "user-1", nil, time.Minute, 3)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for want := 1; want <= 2; want++ {
		attempts, exhausted, err := store.Fail(ctx, id)
		if err != nil {
			t.Fatalf("fail %d: %v", want, err)
		}
		if attempts != want {
			t.Fatalf("attempts = %d, want %d", attempts, want)
		}
		if exhausted {
			t.Fatalf("exhausted after %d attempts", attempts)
		}
	}
	attempts, exhausted, err := store.Fail(ctx, id)
	if err != nil {
		t.Fatalf("final fail: %v", err)
	}
	if attempts != 3 || !exhausted {
		t.Fatalf("attempts,exhausted = %d,%v; want 3,true", attempts, exhausted)
	}
	if _, err := store.Load(ctx, id, storage.ChallengeKindLoginMFA); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("load after exhaustion = %v, want ErrChallengeInvalid", err)
	}
}

// TestChallengeRequiresSecretProvider proves the store fails closed rather than
// writing a plaintext payload when the encryption key is absent.
func TestChallengeRequiresSecretProvider(t *testing.T) {
	db := newIdentityStore(t)
	store := NewChallengeStore(db, NewSecretProvider(nil))
	ctx := context.Background()
	if _, err := store.Create(ctx, storage.ChallengeKindLoginMFA, "user-1", map[string]string{"k": "v"}, time.Minute, 3); !errors.Is(err, ErrSecretStorageUnavailable) {
		t.Fatalf("err = %v, want ErrSecretStorageUnavailable", err)
	}
	var count int
	if err := db.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		pending, err := repos.Challenges.CountPending(ctx)
		count = pending
		return err
	}); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("wrote %d challenges while secrets were unavailable", count)
	}
}

func TestChallengeCountPendingAndDeleteExpired(t *testing.T) {
	store, _ := newChallengeStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, storage.ChallengeKindLoginMFA, "user-1", nil, time.Hour, 3); err != nil {
		t.Fatal(err)
	}
	expiring, err := store.Create(ctx, storage.ChallengeKindOIDCState, "user-2", nil, 20*time.Millisecond, 1)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.CountPending(ctx)
	if err != nil || pending != 2 {
		t.Fatalf("pending = %d, %v; want 2", pending, err)
	}
	time.Sleep(50 * time.Millisecond)
	removed, err := store.DeleteExpired(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := store.Load(ctx, expiring, storage.ChallengeKindOIDCState); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("expired challenge still loadable: %v", err)
	}
}
