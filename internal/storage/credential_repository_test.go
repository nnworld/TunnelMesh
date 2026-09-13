package storage

import (
	"context"
	"testing"
	"time"
)

func TestCredentialRepositoryLifecycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	credential := Credential{
		OwnerUserID: "user-a", Name: "team-key", Type: CredentialTypeSSHPublicKey,
		PublicKey: "ssh-ed25519 AAAATEST", Fingerprint: "SHA256:test", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := db.Credentials().Create(ctx, credential); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	page, err := db.Credentials().List(ctx, CredentialFilter{OwnerUserID: "user-a"}, "", 10)
	if err != nil {
		t.Fatalf("list credentials: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Name != "team-key" || page.Items[0].Type != CredentialTypeSSHPublicKey {
		t.Fatalf("credential page = %+v", page.Items)
	}
	credentialID := page.Items[0].ID

	if err := db.Credentials().Delete(ctx, credentialID, now.Add(time.Second)); err != nil {
		t.Fatalf("delete credential: %v", err)
	}
	active, err := db.Credentials().List(ctx, CredentialFilter{OwnerUserID: "user-a"}, "", 10)
	if err != nil {
		t.Fatalf("list active credentials: %v", err)
	}
	if len(active.Items) != 0 {
		t.Fatalf("active credentials = %+v, want none", active.Items)
	}
	deleted, err := db.Credentials().List(ctx, CredentialFilter{OwnerUserID: "user-a", Status: CredentialStatusDeleted}, "", 10)
	if err != nil {
		t.Fatalf("list deleted credentials: %v", err)
	}
	if len(deleted.Items) != 1 || deleted.Items[0].DeletedAt == nil {
		t.Fatalf("deleted credentials = %+v, want one with deleted timestamp", deleted.Items)
	}

	if err := db.Credentials().Restore(ctx, credentialID); err != nil {
		t.Fatalf("restore credential: %v", err)
	}
	found, err := db.Credentials().Get(ctx, credentialID)
	if err != nil || found.DeletedAt != nil {
		t.Fatalf("restored credential = %+v, err = %v", found, err)
	}
}

// Password credentials carry an encrypted secret blob and no public key, so
// the repository must round-trip the secret columns and keep them empty for
// credentials that never stored one.
func TestCredentialSecretColumnsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	withSecret := Credential{
		OwnerUserID: "user-a", Name: "vault-password", Type: CredentialTypePassword,
		Enabled:          true,
		SecretCiphertext: "Y2lwaGVydGV4dA==", SecretNonce: "bm9uY2U=", SecretKeyID: "key-1", SecretVersion: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	created, err := db.Credentials().Create(ctx, withSecret)
	if err != nil {
		t.Fatalf("create password credential: %v", err)
	}
	found, err := db.Credentials().Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get password credential: %v", err)
	}
	if found.Type != CredentialTypePassword || found.PublicKey != "" || found.Fingerprint != "" {
		t.Fatalf("password credential identity = %+v, want empty public key", found)
	}
	if found.SecretCiphertext != withSecret.SecretCiphertext || found.SecretNonce != withSecret.SecretNonce ||
		found.SecretKeyID != withSecret.SecretKeyID || found.SecretVersion != 1 {
		t.Fatalf("secret fields = %+v, want round trip of %+v", found, withSecret)
	}

	plain := Credential{
		OwnerUserID: "user-a", Name: "plain-key", Type: CredentialTypeSSHPublicKey,
		PublicKey: "ssh-ed25519 AAAATEST", Fingerprint: "SHA256:test", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	createdPlain, err := db.Credentials().Create(ctx, plain)
	if err != nil {
		t.Fatalf("create public key credential: %v", err)
	}
	foundPlain, err := db.Credentials().Get(ctx, createdPlain.ID)
	if err != nil {
		t.Fatalf("get public key credential: %v", err)
	}
	if foundPlain.SecretCiphertext != "" || foundPlain.SecretNonce != "" || foundPlain.SecretKeyID != "" || foundPlain.SecretVersion != 0 {
		t.Fatalf("secret fields = %+v, want zero values", foundPlain)
	}

	// Rotation and removal both go through Update: replacing the ciphertext
	// must not resurrect the old nonce, and clearing every secret column must
	// stick.
	rotated := found
	rotated.SecretCiphertext = "bmV3Y2lwaGVy"
	rotated.SecretNonce = "bmV3bm9uY2U="
	if err := db.Credentials().Update(ctx, rotated); err != nil {
		t.Fatalf("rotate credential secret: %v", err)
	}
	afterRotate, err := db.Credentials().Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get rotated credential: %v", err)
	}
	if afterRotate.SecretCiphertext != "bmV3Y2lwaGVy" || afterRotate.SecretNonce != "bmV3bm9uY2U=" {
		t.Fatalf("rotated secret = %+v", afterRotate)
	}

	cleared := afterRotate
	cleared.SecretCiphertext, cleared.SecretNonce, cleared.SecretKeyID, cleared.SecretVersion = "", "", "", 0
	if err := db.Credentials().Update(ctx, cleared); err != nil {
		t.Fatalf("clear credential secret: %v", err)
	}
	afterClear, err := db.Credentials().Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get cleared credential: %v", err)
	}
	if afterClear.SecretCiphertext != "" || afterClear.SecretVersion != 0 {
		t.Fatalf("cleared secret = %+v, want zero values", afterClear)
	}
}

// A password credential without a public key is valid; a public-key
// credential without one still is not.
func TestCredentialValidationByType(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := db.Credentials().Create(ctx, Credential{OwnerUserID: "user-a", Name: "bad", Type: CredentialTypeSSHPublicKey, Enabled: true, CreatedAt: now, UpdatedAt: now}); err == nil {
		t.Fatal("public key credential without public key was accepted")
	}
	if _, err := db.Credentials().Create(ctx, Credential{OwnerUserID: "user-a", Name: "ok", Type: CredentialTypePassword, Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("password credential without public key rejected: %v", err)
	}
}
