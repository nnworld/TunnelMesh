package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func testDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.OpenSQLite(context.Background(), "file:auth-test?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAuthServiceCreateLoginAndOpaqueToken(t *testing.T) {
	db := testDB(t)
	svc := NewAuthService(db)
	user, err := svc.CreateUser(context.Background(), "alice", "correct horse", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(user.PasswordHash, "$argon2id$") {
		t.Fatalf("password hash = %q", user.PasswordHash)
	}
	if strings.Contains(user.PasswordHash, "correct horse") {
		t.Fatal("clear password persisted")
	}
	res, err := svc.Login(context.Background(), "alice", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" {
		t.Fatal("missing opaque token")
	}
	principal, err := svc.ValidateToken(context.Background(), res.Token)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Role != "admin" || principal.UserID != user.ID {
		t.Fatalf("principal = %+v", principal)
	}
	if _, err := svc.Login(context.Background(), "alice", "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
}
