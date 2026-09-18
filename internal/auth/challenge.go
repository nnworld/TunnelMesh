package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// Challenge is the decrypted, validated view of one auth_challenges row.
//
// A challenge is the only cross-node state in the identity flows: a login that
// starts on one server node may finish on another, so the MFA challenge, the
// OIDC state/nonce/PKCE verifier, and the post-callback login ticket all live
// in the database rather than in process memory. The payload is stored as
// AES-GCM ciphertext because it carries single-use secrets.
type Challenge struct {
	ID          string
	Kind        storage.ChallengeKind
	UserID      string
	Payload     map[string]string
	Attempts    int
	MaxAttempts int
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

// ChallengeStore seals and opens challenge payloads. It has no transport
// knowledge and never returns a partially validated challenge: an expired,
// consumed, or wrong-kind row is indistinguishable from a missing one so a
// caller cannot probe which identifiers were once valid.
type ChallengeStore struct {
	store   IdentityStore
	secrets SecretProvider
	now     func() time.Time
}

// NewChallengeStore builds the store. A nil or unavailable SecretProvider makes
// Create fail closed, because writing a plaintext payload would defeat the
// encryption guarantee the whole design rests on.
func NewChallengeStore(store IdentityStore, secrets SecretProvider) *ChallengeStore {
	if secrets == nil {
		secrets = NewSecretProvider(nil)
	}
	return &ChallengeStore{store: store, secrets: secrets, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the time source for tests.
func (s *ChallengeStore) SetClock(now func() time.Time) {
	if s != nil && now != nil {
		s.now = now
	}
}

// defaultChallengeTTL bounds a challenge when a caller passes a non-positive
// TTL. A short bound is deliberate: a challenge is a guess window, and leaving
// one open indefinitely would widen it.
const defaultChallengeTTL = 5 * time.Minute

// Create seals the payload and stores one challenge row, returning its
// identifier. The identifier is the only value the client ever sees.
func (s *ChallengeStore) Create(ctx context.Context, kind storage.ChallengeKind, userID string, payload any, ttl time.Duration, maxAttempts int) (string, error) {
	if s == nil || s.store == nil {
		return "", errors.New("challenge store is required")
	}
	if kind == "" {
		return "", errors.New("challenge kind is required")
	}
	// Fail before touching the database: an unavailable key must not produce a
	// row that a later reader would have to reject or, worse, treat as plain.
	if !s.secrets.Available() {
		return "", ErrSecretStorageUnavailable
	}
	values := map[string]string{}
	switch typed := payload.(type) {
	case nil:
	case map[string]string:
		for key, value := range typed {
			values[key] = value
		}
	default:
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", fmt.Errorf("encode challenge payload: %w", err)
		}
		if err := json.Unmarshal(encoded, &values); err != nil {
			return "", fmt.Errorf("challenge payload must flatten to string values: %w", err)
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode challenge payload: %w", err)
	}
	ciphertext, nonce, keyID, version, err := s.secrets.Encrypt(string(encoded))
	if err != nil {
		return "", err
	}
	raw, err := randomToken(24)
	if err != nil {
		return "", fmt.Errorf("generate challenge id: %w", err)
	}
	id := "ch_" + raw
	if ttl <= 0 {
		ttl = defaultChallengeTTL
	}
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	now := s.now()
	row := storage.AuthChallenge{
		ID:                id,
		Kind:              kind,
		UserID:            userID,
		PayloadCiphertext: base64.StdEncoding.EncodeToString(ciphertext),
		PayloadNonce:      base64.StdEncoding.EncodeToString(nonce),
		PayloadKeyID:      keyID,
		PayloadVersion:    version,
		MaxAttempts:       maxAttempts,
		ExpiresAt:         now.Add(ttl),
		CreatedAt:         now,
	}
	err = s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		return repos.Challenges.Create(ctx, row)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Load returns a challenge only when it exists, matches the requested kind, is
// unconsumed, and has not expired. Every rejection collapses into
// ErrChallengeInvalid so the reason is never observable from outside.
func (s *ChallengeStore) Load(ctx context.Context, id string, kind storage.ChallengeKind) (Challenge, error) {
	if s == nil || s.store == nil {
		return Challenge{}, errors.New("challenge store is required")
	}
	var row storage.AuthChallenge
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		found, err := repos.Challenges.Get(ctx, id)
		row = found
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Challenge{}, ErrChallengeInvalid
	}
	if err != nil {
		return Challenge{}, err
	}
	if row.Kind != kind || row.ConsumedAt != nil || !row.ExpiresAt.After(s.now()) {
		return Challenge{}, ErrChallengeInvalid
	}
	if !s.secrets.Available() {
		return Challenge{}, ErrSecretStorageUnavailable
	}
	ciphertext, err := base64.StdEncoding.DecodeString(row.PayloadCiphertext)
	if err != nil {
		return Challenge{}, ErrChallengeInvalid
	}
	nonce, err := base64.StdEncoding.DecodeString(row.PayloadNonce)
	if err != nil {
		return Challenge{}, ErrChallengeInvalid
	}
	plain, err := s.secrets.Decrypt(ciphertext, nonce, row.PayloadKeyID, row.PayloadVersion)
	if err != nil {
		// A payload sealed under a rotated-away key is unusable, not readable.
		return Challenge{}, ErrChallengeInvalid
	}
	payload := map[string]string{}
	if plain != "" {
		if err := json.Unmarshal([]byte(plain), &payload); err != nil {
			return Challenge{}, ErrChallengeInvalid
		}
	}
	return Challenge{
		ID:          row.ID,
		Kind:        row.Kind,
		UserID:      row.UserID,
		Payload:     payload,
		Attempts:    row.Attempts,
		MaxAttempts: row.MaxAttempts,
		ExpiresAt:   row.ExpiresAt,
		CreatedAt:   row.CreatedAt,
	}, nil
}

// Consume marks a challenge used. It reports false when the row was already
// consumed, which is what makes a login ticket and an OIDC state single-use
// even when two nodes race.
func (s *ChallengeStore) Consume(ctx context.Context, id string) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("challenge store is required")
	}
	var consumed bool
	err := s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		ok, err := repos.Challenges.Consume(ctx, id, s.now())
		consumed = ok
		return err
	})
	return consumed, err
}

// Fail records one wrong guess. The repository performs the increment with a
// conditional UPDATE, so concurrent guesses share one budget instead of each
// getting a full one. It reports the new count and whether the budget is spent.
func (s *ChallengeStore) Fail(ctx context.Context, id string) (int, bool, error) {
	if s == nil || s.store == nil {
		return 0, false, errors.New("challenge store is required")
	}
	var attempts int
	var exhausted bool
	err := s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		count, consumed, err := repos.Challenges.IncrementAttempts(ctx, id)
		attempts, exhausted = count, consumed
		return err
	})
	return attempts, exhausted, err
}

// CountPending reports live challenges. It backs the pending-challenge gauge.
func (s *ChallengeStore) CountPending(ctx context.Context) (int, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("challenge store is required")
	}
	var count int
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		found, err := repos.Challenges.CountPending(ctx)
		count = found
		return err
	})
	return count, err
}

// DeleteExpired drops rows past the cutoff. Sweeping is an idempotent DELETE,
// so every node may run it without a distributed lock.
func (s *ChallengeStore) DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("challenge store is required")
	}
	var removed int64
	err := s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		count, err := repos.Challenges.DeleteExpired(ctx, cutoff)
		removed = count
		return err
	})
	return removed, err
}
