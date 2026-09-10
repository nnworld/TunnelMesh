package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestAccountServiceCreateChildValidatesUsernameAndGeneratesPassword(t *testing.T) {
	ctx := context.Background()
	db := newAccountTestDB(t, "create")
	service := NewAccountService(db)

	for _, username := range []string{"ab", strings.Repeat("a", 65), "bad name", "bad/name", "用户"} {
		if _, _, err := service.CreateChild(ctx, "admin-1", username); !errors.Is(err, ErrUsernameInvalid) {
			t.Errorf("CreateChild(%q) error = %v, want ErrUsernameInvalid", username, err)
		}
	}
	user, temporaryPassword, err := service.CreateChild(ctx, "admin-1", " operator.one ")
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "operator.one" || user.Role != "user" || user.Disabled || user.DeletedAt != nil {
		t.Fatalf("created user = %+v", user)
	}
	if len(temporaryPassword) < 32 || !VerifyPassword(temporaryPassword, user.PasswordHash) {
		t.Fatal("temporary password is not a high-entropy credential matching the stored hash")
	}
	audits, err := db.Audits().List(ctx, storage.AuditFilter{}, "", 10)
	if err != nil || len(audits.Items) != 1 || audits.Items[0].Action != "account.created" || audits.Items[0].ActorUserID != "admin-1" {
		t.Fatalf("audits = %+v, err = %v", audits, err)
	}
	if strings.Contains(audits.Items[0].Details, temporaryPassword) || strings.Contains(audits.Items[0].Details, user.PasswordHash) {
		t.Fatal("audit details contain password material")
	}
}

func TestAccountServiceChangeOwnPasswordPreservesExistingToken(t *testing.T) {
	ctx := context.Background()
	db := newAccountTestDB(t, "password")
	authService := NewAuthService(db)
	user, err := authService.CreateUser(ctx, "member.one", "old-password-12", "user")
	if err != nil {
		t.Fatal(err)
	}
	login, err := authService.Login(ctx, user.Username, "old-password-12")
	if err != nil {
		t.Fatal(err)
	}
	service := NewAccountService(db)
	if _, err := service.ChangeOwnPassword(ctx, user.ID, "old-password-12", "new-password-12"); err != nil {
		t.Fatal(err)
	}
	if _, err := authService.ValidateToken(ctx, login.Token); err != nil {
		t.Fatalf("existing token was revoked: %v", err)
	}
	if _, err := authService.Login(ctx, user.Username, "old-password-12"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password login error = %v", err)
	}
	if _, err := authService.Login(ctx, user.Username, "new-password-12"); err != nil {
		t.Fatalf("new password login: %v", err)
	}
	if _, err := service.ChangeOwnPassword(ctx, user.ID, "new-password-12", "new-password-12"); !errors.Is(err, ErrPasswordPolicyViolation) {
		t.Fatalf("same password error = %v", err)
	}
	if _, err := service.ChangeOwnPassword(ctx, user.ID, "new-password-12", "short"); !errors.Is(err, ErrPasswordPolicyViolation) {
		t.Fatalf("short password error = %v", err)
	}
}

func TestAccountServiceLogicalDeleteInvalidatesAndRestoreReenablesToken(t *testing.T) {
	ctx := context.Background()
	db := newAccountTestDB(t, "delete-restore")
	authService := NewAuthService(db)
	user, err := authService.CreateUser(ctx, "member.two", "member-password-12", "user")
	if err != nil {
		t.Fatal(err)
	}
	login, err := authService.Login(ctx, user.Username, "member-password-12")
	if err != nil {
		t.Fatal(err)
	}
	service := NewAccountService(db)
	deleted, err := service.DeleteChild(ctx, "admin-1", user.ID)
	if err != nil || deleted.DeletedAt == nil || !deleted.Disabled {
		t.Fatalf("deleted user = %+v, err = %v", deleted, err)
	}
	if _, err := authService.ValidateToken(ctx, login.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("deleted account token error = %v", err)
	}
	restored, err := service.RestoreChild(ctx, "admin-1", user.ID)
	if err != nil || restored.DeletedAt != nil || restored.Disabled {
		t.Fatalf("restored user = %+v, err = %v", restored, err)
	}
	if _, err := authService.ValidateToken(ctx, login.Token); err != nil {
		t.Fatalf("preserved token did not become valid after restore: %v", err)
	}
}

func TestAccountServiceProtectsAdministratorTargets(t *testing.T) {
	ctx := context.Background()
	db := newAccountTestDB(t, "protect-admin")
	authService := NewAuthService(db)
	admin, err := authService.CreateUser(ctx, "admin.one", "admin-password-12", "admin")
	if err != nil {
		t.Fatal(err)
	}
	service := NewAccountService(db)
	checks := []func() error{
		func() error { _, err := service.SetDisabled(ctx, "admin-1", admin.ID, true); return err },
		func() error { _, _, err := service.ResetPassword(ctx, "admin-1", admin.ID); return err },
		func() error { _, err := service.DeleteChild(ctx, "admin-1", admin.ID); return err },
		func() error { _, err := service.RestoreChild(ctx, "admin-1", admin.ID); return err },
	}
	for i, check := range checks {
		if err := check(); !errors.Is(err, ErrAdminAccountProtected) {
			t.Errorf("check %d error = %v, want ErrAdminAccountProtected", i, err)
		}
	}
}

func TestAccountServiceMutationRefreshesUpdatedAt(t *testing.T) {
	ctx := context.Background()
	db := newAccountTestDB(t, "updated-at")
	user, err := NewAuthService(db).CreateUser(ctx, "member.updated", "member-password-12", "user")
	if err != nil {
		t.Fatal(err)
	}
	originalUpdatedAt := time.Unix(1, 0).UTC()
	user.UpdatedAt = originalUpdatedAt
	if err := db.Users().Update(ctx, user); err != nil {
		t.Fatal(err)
	}

	updated, err := NewAccountService(db).SetDisabled(ctx, "admin-1", user.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.UpdatedAt.After(originalUpdatedAt) {
		t.Fatalf("updated_at = %s, want after %s", updated.UpdatedAt, originalUpdatedAt)
	}
}

func newAccountTestDB(t *testing.T, name string) *storage.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:auth-account-"+name+"?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
