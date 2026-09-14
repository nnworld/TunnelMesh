package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"golang.org/x/crypto/ssh"
)

func TestParseOpenSSHPublicKey(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		valid bool
	}{
		{name: "ed25519", key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB3VnH4xLmFhZ0l6c2V0dGVzdHB1YmxpY2tleQ==", valid: true},
		{name: "rsa", key: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQdGVzdA", valid: true},
		{name: "ecdsa-p256", key: "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTY= test", valid: true},
		{name: "wrong-algorithm", key: "ssh-dss AAAAB3NzaC1kc3MAAACBA==", valid: false},
		{name: "invalid-base64", key: "ssh-ed25519 !!!!", valid: false},
		{name: "comment-only", key: "hello", valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			normalized, fingerprint, err := ParseOpenSSHPublicKey(tc.key)
			if tc.valid {
				if err != nil || normalized == "" || fingerprint == "" {
					t.Fatalf("normalized=%q fingerprint=%q err=%v", normalized, fingerprint, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected invalid key to fail, got normalized=%q fingerprint=%q", normalized, fingerprint)
			}
		})
	}
}

func TestCredentialServiceOwnershipAndLogicalLifecycle(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:credential-service-"+t.Name()+"?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := NewCredentialService(db.Credentials(), db.Agents(), db.Audits())
	ctx := context.Background()
	admin := auth.Principal{UserID: "admin-a", Username: "admin", Role: "admin"}
	owner := auth.Principal{UserID: "user-a", Username: "alice", Role: "user"}
	other := auth.Principal{UserID: "user-b", Username: "bob", Role: "user"}

	created, err := service.Create(ctx, owner, CredentialInput{
		Name: "deploy-key", Type: storage.CredentialTypeSSHPublicKey,
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB3VnH4xLmFhZ0l6c2V0dGVzdHB1YmxpY2tleQ==",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	if _, err := service.Get(ctx, other, created.ID); err == nil {
		t.Fatal("other owner can read credential")
	}
	if _, err := service.Get(ctx, admin, created.ID); err != nil {
		t.Fatalf("admin cannot read credential: %v", err)
	}
	page, err := service.List(ctx, other, storage.CredentialFilter{OwnerUserID: other.UserID}, "", 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("other owner page = %+v, err = %v", page, err)
	}
	if err := service.Delete(ctx, owner, created.ID); err != nil {
		t.Fatalf("delete credential: %v", err)
	}
	if _, err := service.Restore(ctx, owner, created.ID); err != nil {
		t.Fatalf("restore credential: %v", err)
	}
	audits, err := db.Audits().List(ctx, storage.AuditFilter{ResourceID: created.ID}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits.Items) == 0 {
		t.Fatal("credential audit records are missing")
	}
}

func TestCredentialServiceExtractsPublicKeyFromPrivateKey(t *testing.T) {
	service := NewCredentialService(nil, nil, nil)
	privateKeyPEM, expected := testEd25519PrivateKeyPEM(t, "")
	extracted, err := service.ExtractSSHPublicKey(context.Background(), auth.Principal{UserID: "user-a"}, privateKeyPEM, "")
	if err != nil {
		t.Fatal(err)
	}
	if extracted.PublicKey != expected {
		t.Fatalf("public key = %q, want %q", extracted.PublicKey, expected)
	}
	if extracted.Fingerprint == "" {
		t.Fatal("fingerprint is empty")
	}
}

func TestCredentialServiceExtractsEncryptedPrivateKey(t *testing.T) {
	service := NewCredentialService(nil, nil, nil)
	privateKeyPEM, expected := testEd25519PrivateKeyPEM(t, "correct horse battery staple")
	extracted, err := service.ExtractSSHPublicKey(context.Background(), auth.Principal{UserID: "user-a"}, privateKeyPEM, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if extracted.PublicKey != expected || extracted.Fingerprint == "" {
		t.Fatalf("extracted = %+v, want public key %q", extracted, expected)
	}
}

func TestCredentialServiceRejectsInvalidAndOversizedPrivateKey(t *testing.T) {
	service := NewCredentialService(nil, nil, nil)
	actor := auth.Principal{UserID: "user-a"}
	if _, err := service.ExtractSSHPublicKey(context.Background(), actor, "not a private key", ""); err == nil {
		t.Fatal("invalid private key was accepted")
	}
	oversized := strings.Repeat("x", 64*1024+1)
	if _, err := service.ExtractSSHPublicKey(context.Background(), actor, oversized, ""); err == nil {
		t.Fatal("oversized private key was accepted")
	}
}

func testEd25519PrivateKeyPEM(t *testing.T, passphrase string) (string, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(private, "tunnelmesh-test")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(private, "tunnelmesh-test", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))
}

const credentialFixturePublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB3VnH4xLmFhZ0l6c2V0dGVzdHB1YmxpY2tleQ=="

// newCredentialSecretFixture returns a service with an AES-GCM secret store so
// credential secrets can be encrypted exactly like production does.
func newCredentialSecretFixture(t *testing.T) (*storage.DB, *CredentialService) {
	t.Helper()
	dsn := "file:credential-secret-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := storage.Open(context.Background(), storage.DriverSQLite, dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := NewCredentialService(db.Credentials(), db.Agents(), db.Audits())
	store, err := auth.NewSecretStore(base64.RawStdEncoding.EncodeToString(make([]byte, 32)), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretStore(store)
	return db, service
}

func TestCredentialServiceStoresPasswordSecretEncrypted(t *testing.T) {
	db, service := newCredentialSecretFixture(t)
	ctx := context.Background()
	owner := auth.Principal{UserID: "user-a", Username: "alice", Role: "user"}
	other := auth.Principal{UserID: "user-b", Username: "bob", Role: "user"}
	admin := auth.Principal{UserID: "admin-a", Username: "root", Role: "admin"}

	created, err := service.Create(ctx, owner, CredentialInput{
		Name: "web-host", Type: storage.CredentialTypePassword,
		Secret: &CredentialSecret{Password: "s3cr3t-password"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create password credential: %v", err)
	}
	if !created.HasSecret() {
		t.Fatalf("created credential = %+v, want HasSecret", created)
	}
	if created.PublicKey != "" || created.Fingerprint != "" {
		t.Fatalf("password credential must not carry a public key: %+v", created)
	}
	if strings.Contains(created.SecretCiphertext, "s3cr3t-password") {
		t.Fatal("plaintext password leaked into the ciphertext column")
	}
	// The secret is decryptable by its owner only, administrators included.
	if _, secret, err := service.GetWithSecret(ctx, other, created.ID); err == nil {
		t.Fatalf("other user read secret %+v", secret)
	}
	if _, _, err := service.GetWithSecret(ctx, admin, created.ID); err == nil {
		t.Fatal("admin must not decrypt another owner's credential secret")
	}
	credential, secret, err := service.GetWithSecret(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("owner read secret: %v", err)
	}
	if secret == nil || secret.Password != "s3cr3t-password" || secret.PrivateKey != "" || secret.Passphrase != "" {
		t.Fatalf("secret = %+v, want password only", secret)
	}
	if credential.ID != created.ID {
		t.Fatalf("credential = %+v, want id %s", credential, created.ID)
	}

	var ciphertext string
	if err := db.SQL().QueryRowContext(ctx, `SELECT secret_ciphertext FROM credentials WHERE id=?`, created.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, "s3cr3t-password") {
		t.Fatal("stored ciphertext contains the plaintext password")
	}
	page, err := service.List(ctx, owner, storage.CredentialFilter{OwnerUserID: owner.UserID}, "", 10)
	if err != nil || len(page.Items) != 1 || !page.Items[0].HasSecret() {
		t.Fatalf("page = %+v, err = %v", page, err)
	}

	audits, err := db.Audits().List(ctx, storage.AuditFilter{ResourceID: created.ID}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range audits.Items {
		if entry.Action != "credential.secret-set" {
			continue
		}
		found = true
		if strings.Contains(entry.Details, "s3cr3t-password") {
			t.Fatal("audit details contain the credential secret")
		}
	}
	if !found {
		t.Fatalf("credential.secret-set audit is missing: %+v", audits.Items)
	}
}

func TestCredentialServiceStoresPrivateKeySecret(t *testing.T) {
	_, service := newCredentialSecretFixture(t)
	ctx := context.Background()
	owner := auth.Principal{UserID: "user-a", Username: "alice", Role: "user"}
	privateKey, publicKey := testEd25519PrivateKeyPEM(t, "key-pass")

	created, err := service.Create(ctx, owner, CredentialInput{
		Name: "deploy", Type: storage.CredentialTypeSSHPublicKey, PublicKey: publicKey,
		Secret: &CredentialSecret{PrivateKey: privateKey, Passphrase: "key-pass"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create key credential with secret: %v", err)
	}
	if !created.HasSecret() || created.Fingerprint == "" {
		t.Fatalf("created credential = %+v, want secret and fingerprint", created)
	}
	_, secret, err := service.GetWithSecret(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("read key secret: %v", err)
	}
	if secret == nil || secret.PrivateKey != privateKey || secret.Passphrase != "key-pass" || secret.Password != "" {
		t.Fatal("decrypted secret does not match the stored private key")
	}

	// A public-key credential without a stored private key has no secret and
	// must fall back to the manual password prompt instead of failing.
	plain, err := service.Create(ctx, owner, CredentialInput{
		Name: "public-only", Type: storage.CredentialTypeSSHPublicKey,
		PublicKey: credentialFixturePublicKey, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create public-only credential: %v", err)
	}
	if plain.HasSecret() {
		t.Fatalf("public-only credential = %+v, want no secret", plain)
	}
	_, secret, err = service.GetWithSecret(ctx, owner, plain.ID)
	if err != nil || secret != nil {
		t.Fatalf("secret = %+v, err = %v, want nil secret without error", secret, err)
	}
}

func TestCredentialServiceValidatesTypeAndSecret(t *testing.T) {
	_, service := newCredentialSecretFixture(t)
	ctx := context.Background()
	owner := auth.Principal{UserID: "user-a", Username: "alice", Role: "user"}

	cases := []struct {
		name  string
		input CredentialInput
	}{
		{name: "password-without-secret", input: CredentialInput{Name: "a", Type: storage.CredentialTypePassword, Enabled: true}},
		{name: "password-with-private-key", input: CredentialInput{Name: "a", Type: storage.CredentialTypePassword, Secret: &CredentialSecret{PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----"}, Enabled: true}},
		{name: "public-key-without-key", input: CredentialInput{Name: "a", Type: storage.CredentialTypeSSHPublicKey, Enabled: true}},
		{name: "passphrase-without-private-key", input: CredentialInput{Name: "a", Type: storage.CredentialTypeSSHPublicKey, PublicKey: credentialFixturePublicKey, Secret: &CredentialSecret{Passphrase: "orphan"}, Enabled: true}},
		{name: "unknown-type", input: CredentialInput{Name: "a", Type: storage.CredentialType("kerberos"), PublicKey: credentialFixturePublicKey, Enabled: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.Create(ctx, owner, tc.input); !errors.Is(err, ErrCredentialInvalid) {
				t.Fatalf("err = %v, want ErrCredentialInvalid", err)
			}
		})
	}
}

func TestCredentialServiceSecretSizeLimits(t *testing.T) {
	_, service := newCredentialSecretFixture(t)
	ctx := context.Background()
	owner := auth.Principal{UserID: "user-a", Username: "alice", Role: "user"}

	if _, err := service.Create(ctx, owner, CredentialInput{
		Name: "big-password", Type: storage.CredentialTypePassword,
		Secret: &CredentialSecret{Password: strings.Repeat("a", maxCredentialPasswordBytes+1)}, Enabled: true,
	}); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("oversized password err = %v", err)
	}
	if _, err := service.Create(ctx, owner, CredentialInput{
		Name: "big-key", Type: storage.CredentialTypeSSHPublicKey, PublicKey: credentialFixturePublicKey,
		Secret: &CredentialSecret{PrivateKey: strings.Repeat("k", maxSSHPrivateKeyBytes+1)}, Enabled: true,
	}); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("oversized private key err = %v", err)
	}
	if _, err := service.Create(ctx, owner, CredentialInput{
		Name: "big-passphrase", Type: storage.CredentialTypeSSHPublicKey, PublicKey: credentialFixturePublicKey,
		Secret: &CredentialSecret{PrivateKey: "key", Passphrase: strings.Repeat("p", maxCredentialPassphraseBytes+1)}, Enabled: true,
	}); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("oversized passphrase err = %v", err)
	}
}

func TestCredentialServiceSecretRequiresEncryptionKey(t *testing.T) {
	dsn := "file:credential-no-secret-store-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := storage.Open(context.Background(), storage.DriverSQLite, dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := NewCredentialService(db.Credentials(), db.Agents(), db.Audits())
	ctx := context.Background()
	owner := auth.Principal{UserID: "user-a", Username: "alice", Role: "user"}

	if _, err := service.Create(ctx, owner, CredentialInput{
		Name: "web-host", Type: storage.CredentialTypePassword,
		Secret: &CredentialSecret{Password: "s3cr3t"}, Enabled: true,
	}); !errors.Is(err, ErrCredentialSecretUnavailable) {
		t.Fatalf("err = %v, want ErrCredentialSecretUnavailable", err)
	}
	var count int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM credentials`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("credentials rows = %d, want 0 after a rejected secret", count)
	}
	// Public-key credentials without a secret keep working without a key.
	if _, err := service.Create(ctx, owner, CredentialInput{
		Name: "public-only", Type: storage.CredentialTypeSSHPublicKey,
		PublicKey: credentialFixturePublicKey, Enabled: true,
	}); err != nil {
		t.Fatalf("public-only credential without secret store: %v", err)
	}
}

func TestCredentialServiceUpdatePreservesAndReplacesSecret(t *testing.T) {
	_, service := newCredentialSecretFixture(t)
	ctx := context.Background()
	owner := auth.Principal{UserID: "user-a", Username: "alice", Role: "user"}
	created, err := service.Create(ctx, owner, CredentialInput{
		Name: "web-host", Type: storage.CredentialTypePassword,
		Secret: &CredentialSecret{Password: "first"}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	name := "renamed"
	if _, err := service.Update(ctx, owner, created.ID, CredentialPatch{Name: &name}); err != nil {
		t.Fatalf("rename credential: %v", err)
	}
	_, secret, err := service.GetWithSecret(ctx, owner, created.ID)
	if err != nil || secret == nil || secret.Password != "first" {
		t.Fatalf("secret = %+v, err = %v, want the untouched password", secret, err)
	}

	if _, err := service.Update(ctx, owner, created.ID, CredentialPatch{Secret: &CredentialSecret{Password: "second"}}); err != nil {
		t.Fatalf("rotate secret: %v", err)
	}
	_, secret, err = service.GetWithSecret(ctx, owner, created.ID)
	if err != nil || secret == nil || secret.Password != "second" {
		t.Fatalf("secret = %+v, err = %v, want the rotated password", secret, err)
	}

	// Turning a password credential into a key credential requires a public key.
	keyType := storage.CredentialTypeSSHPublicKey
	if _, err := service.Update(ctx, owner, created.ID, CredentialPatch{Type: &keyType}); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("type switch err = %v, want ErrCredentialInvalid", err)
	}
	publicKey := credentialFixturePublicKey
	updated, err := service.Update(ctx, owner, created.ID, CredentialPatch{
		Type: &keyType, PublicKey: &publicKey, Secret: &CredentialSecret{PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----"},
	})
	if err != nil {
		t.Fatalf("switch to key credential: %v", err)
	}
	if updated.Type != storage.CredentialTypeSSHPublicKey || !updated.HasSecret() {
		t.Fatalf("updated credential = %+v", updated)
	}
	_, secret, err = service.GetWithSecret(ctx, owner, created.ID)
	if err != nil || secret == nil || secret.Password != "" || secret.PrivateKey == "" {
		t.Fatalf("secret = %+v, err = %v, want private key only", secret, err)
	}
}

// A proxy_basic credential pairs a username with a sealed password. The
// username lives in public_key so list views can render it; the password only
// ever leaves storage through ProxyBasicSecret.
func TestCredentialServiceCreatesProxyBasicWithEncryptedPassword(t *testing.T) {
	_, service := newCredentialSecretFixture(t)
	ctx := context.Background()
	actor := auth.Principal{UserID: "admin-a", Username: "root", Role: "admin"}

	created, err := service.Create(ctx, actor, CredentialInput{
		Name: "proxy demo", Type: storage.CredentialTypeProxyBasic, Username: "  demo  ",
		Enabled: true, Secret: &CredentialSecret{Password: "s3cret"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.PublicKey != "demo" || created.Fingerprint == "" || !created.HasSecret() {
		t.Fatalf("created = %#v", created)
	}
	if strings.Contains(created.SecretCiphertext, "s3cret") {
		t.Fatal("plaintext password leaked into the ciphertext column")
	}
	username, password, err := service.ProxyBasicSecret(ctx, actor, created.ID)
	if err != nil {
		t.Fatalf("ProxyBasicSecret: %v", err)
	}
	if username != "demo" || password != "s3cret" {
		t.Fatalf("secret = %q/%q", username, password)
	}

	if _, err := service.Create(ctx, actor, CredentialInput{
		Name: "missing password", Type: storage.CredentialTypeProxyBasic, Username: "demo", Enabled: true,
	}); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("missing password err = %v, want ErrCredentialInvalid", err)
	}
	if _, err := service.Create(ctx, actor, CredentialInput{
		Name: "missing username", Type: storage.CredentialTypeProxyBasic, Enabled: true,
		Secret: &CredentialSecret{Password: "s3cret"},
	}); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("missing username err = %v, want ErrCredentialInvalid", err)
	}
	if _, err := service.Create(ctx, actor, CredentialInput{
		Name: "key material rejected", Type: storage.CredentialTypeProxyBasic, Username: "demo", Enabled: true,
		Secret: &CredentialSecret{Password: "s3cret", PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----"},
	}); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("private key on proxy credential err = %v, want ErrCredentialInvalid", err)
	}
}

// Rotating the username must rewrite public_key and the fingerprint together,
// and it must never be possible to blank the username out.
func TestCredentialServiceUpdatesProxyBasicUsername(t *testing.T) {
	_, service := newCredentialSecretFixture(t)
	ctx := context.Background()
	actor := auth.Principal{UserID: "admin-a", Username: "root", Role: "admin"}
	created, err := service.Create(ctx, actor, CredentialInput{
		Name: "proxy demo", Type: storage.CredentialTypeProxyBasic, Username: "demo",
		Enabled: true, Secret: &CredentialSecret{Password: "s3cret"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	blank := "   "
	if _, err := service.Update(ctx, actor, created.ID, CredentialPatch{Username: &blank}); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("blank username err = %v, want ErrCredentialInvalid", err)
	}
	replaced := "demo2"
	updated, err := service.Update(ctx, actor, created.ID, CredentialPatch{Username: &replaced})
	if err != nil {
		t.Fatalf("update username: %v", err)
	}
	if updated.PublicKey != "demo2" || updated.Fingerprint == created.Fingerprint {
		t.Fatalf("updated = %#v", updated)
	}
	username, password, err := service.ProxyBasicSecret(ctx, actor, created.ID)
	if err != nil || username != "demo2" || password != "s3cret" {
		t.Fatalf("secret = %q/%q, err = %v", username, password, err)
	}
}

// A disabled or soft-deleted credential must stop authenticating immediately,
// because the proxy entry resolves it on every request.
func TestCredentialServiceProxyBasicSecretRejectsUnusableCredential(t *testing.T) {
	_, service := newCredentialSecretFixture(t)
	ctx := context.Background()
	actor := auth.Principal{UserID: "admin-a", Username: "root", Role: "admin"}
	created, err := service.Create(ctx, actor, CredentialInput{
		Name: "proxy demo", Type: storage.CredentialTypeProxyBasic, Username: "demo",
		Enabled: true, Secret: &CredentialSecret{Password: "s3cret"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := service.ProxyBasicSecret(ctx, actor, "missing-id"); err == nil {
		t.Fatal("missing credential resolved")
	}
	disabled := false
	if _, err := service.Update(ctx, actor, created.ID, CredentialPatch{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, _, err := service.ProxyBasicSecret(ctx, actor, created.ID); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("disabled credential err = %v, want ErrCredentialInvalid", err)
	}
	enabled := true
	if _, err := service.Update(ctx, actor, created.ID, CredentialPatch{Enabled: &enabled}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := service.Delete(ctx, actor, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := service.ProxyBasicSecret(ctx, actor, created.ID); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("deleted credential err = %v, want ErrCredentialInvalid", err)
	}
}
