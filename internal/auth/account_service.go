package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var (
	ErrUsernameInvalid         = errors.New("username_invalid")
	ErrUsernameConflict        = errors.New("username_conflict")
	ErrPasswordPolicyViolation = errors.New("password_policy_violation")
	ErrCurrentPasswordInvalid  = errors.New("current_password_invalid")
	ErrAdminAccountProtected   = errors.New("admin_account_protected")
	ErrAccountDeleted          = errors.New("account_deleted")
	ErrAccountStatusInvalid    = errors.New("account_status_invalid")
)

var accountUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{3,64}$`)

type AccountService struct {
	users    storage.AccountUserRepository
	identity storage.UserIdentityWriter
	audits   storage.AuditRepository
	tx       func(context.Context, func(storage.AccountRepositories) error) error
}

func NewAccountService(source any, repos ...any) *AccountService {
	service := &AccountService{}
	bindAccountRepository(service, source)
	for _, repo := range repos {
		bindAccountRepository(service, repo)
	}
	return service
}

func bindAccountRepository(service *AccountService, source any) {
	switch value := source.(type) {
	case *storage.DB:
		service.users, _ = value.Users().(storage.AccountUserRepository)
		service.identity, _ = value.Users().(storage.UserIdentityWriter)
		service.audits = value.Audits()
		service.tx = value.AccountTransaction
	case storage.AccountUserRepository:
		service.users = value
	case storage.AuditRepository:
		service.audits = value
	}
	// The identity writer is checked after the type switch so a *storage.DB, which
	// satisfies both interfaces, is not shadowed by the narrower case.
	if service.identity == nil {
		service.identity, _ = source.(storage.UserIdentityWriter)
	}
}

func (s *AccountService) CreateChild(ctx context.Context, actorUserID, username string) (storage.User, string, error) {
	username = strings.TrimSpace(username)
	if !accountUsernamePattern.MatchString(username) {
		return storage.User{}, "", ErrUsernameInvalid
	}
	temporaryPassword, err := randomToken(24)
	if err != nil {
		return storage.User{}, "", fmt.Errorf("generate temporary password: %w", err)
	}
	passwordHash, err := hashPassword(temporaryPassword)
	if err != nil {
		return storage.User{}, "", err
	}
	var result storage.User
	err = s.withTransaction(ctx, func(repos storage.AccountRepositories) error {
		if err := repos.Users.Create(ctx, storage.User{Username: username, Role: "user", PasswordHash: passwordHash}); err != nil {
			if isUniqueConstraintError(err) {
				return ErrUsernameConflict
			}
			return err
		}
		result, err = repos.Users.GetByUsername(ctx, username)
		if err != nil {
			return err
		}
		return createAccountAudit(ctx, repos.Audits, actorUserID, "account.created", result)
	})
	if err != nil {
		return storage.User{}, "", err
	}
	return result, temporaryPassword, nil
}

func (s *AccountService) ListChildren(ctx context.Context, status, cursor string, limit int) (storage.Page[storage.User], error) {
	if s == nil || s.users == nil {
		return storage.Page[storage.User]{}, errors.New("account user repository is required")
	}
	accountStatus := storage.AccountStatus(strings.ToLower(strings.TrimSpace(status)))
	switch accountStatus {
	case "", storage.AccountStatusActive, storage.AccountStatusDeleted, storage.AccountStatusAll:
	default:
		return storage.Page[storage.User]{}, ErrAccountStatusInvalid
	}
	return s.users.ListChildren(ctx, accountStatus, cursor, limit)
}

func (s *AccountService) SetDisabled(ctx context.Context, actorUserID, userID string, disabled bool) (storage.User, error) {
	return s.mutateChild(ctx, actorUserID, userID, func(repos storage.AccountRepositories, user *storage.User) (string, error) {
		if user.DeletedAt != nil {
			return "", ErrAccountDeleted
		}
		user.Disabled = disabled
		if err := updateAccountUser(ctx, repos.Users, user); err != nil {
			return "", err
		}
		if disabled {
			return "account.disabled", nil
		}
		return "account.enabled", nil
	})
}

func (s *AccountService) ResetPassword(ctx context.Context, actorUserID, userID string) (storage.User, string, error) {
	temporaryPassword, err := randomToken(24)
	if err != nil {
		return storage.User{}, "", fmt.Errorf("generate temporary password: %w", err)
	}
	passwordHash, err := hashPassword(temporaryPassword)
	if err != nil {
		return storage.User{}, "", err
	}
	user, err := s.mutateChild(ctx, actorUserID, userID, func(repos storage.AccountRepositories, user *storage.User) (string, error) {
		if user.DeletedAt != nil {
			return "", ErrAccountDeleted
		}
		user.PasswordHash = passwordHash
		if err := updateAccountUser(ctx, repos.Users, user); err != nil {
			return "", err
		}
		return "account.password_reset", nil
	})
	if err != nil {
		return storage.User{}, "", err
	}
	return user, temporaryPassword, nil
}

func (s *AccountService) ChangeOwnPassword(ctx context.Context, userID, currentPassword, newPassword string) (storage.User, error) {
	if err := validateAccountPassword(newPassword); err != nil {
		return storage.User{}, err
	}
	var result storage.User
	err := s.withTransaction(ctx, func(repos storage.AccountRepositories) error {
		user, err := repos.Users.Get(ctx, userID)
		if err != nil {
			return err
		}
		if user.DeletedAt != nil || user.Disabled {
			return ErrAccountDeleted
		}
		if !verifyPassword(currentPassword, user.PasswordHash) {
			return ErrCurrentPasswordInvalid
		}
		if verifyPassword(newPassword, user.PasswordHash) {
			return ErrPasswordPolicyViolation
		}
		user.PasswordHash, err = hashPassword(newPassword)
		if err != nil {
			return err
		}
		if err := updateAccountUser(ctx, repos.Users, &user); err != nil {
			return err
		}
		result = user
		return createAccountAudit(ctx, repos.Audits, user.ID, "account.password_changed", user)
	})
	return result, err
}

func (s *AccountService) DeleteChild(ctx context.Context, actorUserID, userID string) (storage.User, error) {
	return s.mutateChild(ctx, actorUserID, userID, func(repos storage.AccountRepositories, user *storage.User) (string, error) {
		if err := repos.Users.SoftDelete(ctx, user.ID, time.Now().UTC()); err != nil {
			return "", err
		}
		return "account.deleted", nil
	})
}

func (s *AccountService) RestoreChild(ctx context.Context, actorUserID, userID string) (storage.User, error) {
	return s.mutateChild(ctx, actorUserID, userID, func(repos storage.AccountRepositories, user *storage.User) (string, error) {
		if err := repos.Users.Restore(ctx, user.ID); err != nil {
			return "", err
		}
		return "account.restored", nil
	})
}

// SetMFARequired flips the per-account second-factor override. It is a floor
// rather than a suggestion: an administrator can protect one account even while
// the global policy is disabled, and ResolvePolicy keeps enforcing the flag.
//
// The write goes through SetMFARequired on the repository instead of Update so a
// partially populated User value can never clear the column by accident.
func (s *AccountService) SetMFARequired(ctx context.Context, actorUserID, userID string, required bool) (storage.User, error) {
	return s.mutateChild(ctx, actorUserID, userID, func(repos storage.AccountRepositories, user *storage.User) (string, error) {
		if user.DeletedAt != nil {
			return "", ErrAccountDeleted
		}
		if repos.Identity == nil {
			return "", errors.New("account identity repository is required")
		}
		if err := repos.Identity.SetMFARequired(ctx, user.ID, required); err != nil {
			return "", err
		}
		if required {
			return "account.mfa_required", nil
		}
		return "account.mfa_optional", nil
	})
}

func (s *AccountService) mutateChild(ctx context.Context, actorUserID, userID string, mutate func(storage.AccountRepositories, *storage.User) (string, error)) (storage.User, error) {
	var result storage.User
	err := s.withTransaction(ctx, func(repos storage.AccountRepositories) error {
		user, err := repos.Users.Get(ctx, userID)
		if err != nil {
			return err
		}
		if strings.EqualFold(user.Role, "admin") {
			return ErrAdminAccountProtected
		}
		action, err := mutate(repos, &user)
		if err != nil {
			return err
		}
		result, err = repos.Users.Get(ctx, userID)
		if err != nil {
			return err
		}
		return createAccountAudit(ctx, repos.Audits, actorUserID, action, result)
	})
	return result, err
}

func (s *AccountService) withTransaction(ctx context.Context, work func(storage.AccountRepositories) error) error {
	if s == nil || s.users == nil || s.audits == nil {
		return errors.New("account repositories are required")
	}
	if s.tx != nil {
		return s.tx(ctx, work)
	}
	return work(storage.AccountRepositories{Users: s.users, Identity: s.identity, Audits: s.audits})
}

// updateAccountUser refreshes the timestamp at the service boundary so callers
// cannot accidentally persist the value loaded before the mutation.
func updateAccountUser(ctx context.Context, users storage.AccountUserRepository, user *storage.User) error {
	user.UpdatedAt = time.Now().UTC()
	return users.Update(ctx, *user)
}

func validateAccountPassword(password string) error {
	if !utf8.ValidString(password) {
		return ErrPasswordPolicyViolation
	}
	length := utf8.RuneCountInString(password)
	if length < 12 || length > 128 {
		return ErrPasswordPolicyViolation
	}
	return nil
}

func createAccountAudit(ctx context.Context, audits storage.AuditRepository, actorUserID, action string, user storage.User) error {
	details, err := json.Marshal(struct {
		UserID      string `json:"userId"`
		Username    string `json:"username"`
		Disabled    bool   `json:"disabled"`
		Deleted     bool   `json:"deleted"`
		MFARequired bool   `json:"mfaRequired"`
		AuthSource  string `json:"authSource"`
	}{UserID: user.ID, Username: user.Username, Disabled: user.Disabled, Deleted: user.DeletedAt != nil, MFARequired: user.MFARequired, AuthSource: string(user.AuthSource)})
	if err != nil {
		return err
	}
	return audits.Create(ctx, storage.AuditLog{ActorUserID: actorUserID, Action: action, ResourceType: "user", ResourceID: user.ID, Details: string(details)})
}

func isUniqueConstraintError(err error) bool {
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") || strings.Contains(message, "duplicate")
}
