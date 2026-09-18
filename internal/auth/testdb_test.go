package auth

import (
	"context"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// newIdentityStore opens a throwaway SQLite database with the current schema so
// identity services are tested against the real repositories. Faking eight
// repositories would mostly test the fakes, and the SQL dialect is where these
// features actually break.
func newIdentityStore(t *testing.T) *storage.DB {
	t.Helper()
	dsn := "file:" + t.TempDir() + "/identity.sqlite"
	db, err := storage.OpenSQLite(context.Background(), dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedLocalUser creates a password account and returns its id and password.
func seedLocalUser(t *testing.T, db *storage.DB, id, username, role, password string) storage.User {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	user := storage.User{ID: id, Username: username, Role: role, PasswordHash: hash, AuthSource: storage.AuthSourceLocal}
	if err := db.Users().Create(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	created, err := db.Users().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// seedExternalUser creates a provisioned-only account that has no local
// password, which is what an OIDC just-in-time account looks like.
func seedExternalUser(t *testing.T, db *storage.DB, id, username, role string) storage.User {
	t.Helper()
	user := storage.User{ID: id, Username: username, Role: role, PasswordHash: storage.PasswordHashNone, AuthSource: storage.AuthSourceOIDC}
	if err := db.Users().Create(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	created, err := db.Users().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// testSecretProvider returns a real AES-GCM provider backed by an ephemeral key.
func testSecretProvider(t *testing.T) SecretProvider {
	t.Helper()
	t.Setenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID", "test-key")
	provider := SecretProviderFromEnv()
	if !provider.Available() {
		t.Fatal("test secret provider is unavailable")
	}
	return provider
}

// setMFARequired flips the per-account second-factor override through the same
// writer the admin API uses, so a test cannot drift from production behaviour.
func setMFARequired(t *testing.T, db *storage.DB, userID string, required bool) {
	t.Helper()
	ctx := context.Background()
	err := db.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		return repos.UserIdentity.SetMFARequired(ctx, userID, required)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// auditActions returns every recorded audit action in insertion order.
func auditActions(t *testing.T, db *storage.DB) []string {
	t.Helper()
	page, err := db.Audits().List(context.Background(), storage.AuditFilter{}, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	actions := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		actions = append(actions, item.Action)
	}
	return actions
}

// auditDetails returns the concatenated details payload of every audit row so a
// test can assert that no secret ever reached the log.
func auditDetails(t *testing.T, db *storage.DB) string {
	t.Helper()
	page, err := db.Audits().List(context.Background(), storage.AuditFilter{}, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	out := ""
	for _, item := range page.Items {
		out += item.Details + "\n"
	}
	return out
}
