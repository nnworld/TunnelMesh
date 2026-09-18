package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth/oidc"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// IdentityView is one linked external identity. The subject is included because
// an administrator unlinking the wrong row is unrecoverable without it, and a
// subject is not a credential.
type IdentityView struct {
	ID           string     `json:"id"`
	ProviderID   string     `json:"providerId"`
	ProviderName string     `json:"providerName"`
	Subject      string     `json:"subject"`
	Username     string     `json:"username"`
	Email        string     `json:"email"`
	DisplayName  string     `json:"displayName"`
	LinkedAt     time.Time  `json:"linkedAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt"`
}

// IdentityService owns the link between external subjects and local accounts, and
// the just-in-time provisioning decision that an OIDC login triggers.
type IdentityService struct {
	store IdentityStore
	now   func() time.Time
}

// NewIdentityService builds the service.
func NewIdentityService(store IdentityStore) *IdentityService {
	return &IdentityService{store: store, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the time source for tests.
func (s *IdentityService) SetClock(now func() time.Time) {
	if s != nil && now != nil {
		s.now = now
	}
}

// List returns the identities linked to one account, resolving each provider's
// display name so the console does not have to join them client-side.
func (s *IdentityService) List(ctx context.Context, userID string) ([]IdentityView, error) {
	views := []IdentityView{}
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		rows, err := repos.Identities.ListByUser(ctx, userID)
		if err != nil {
			return err
		}
		// The username is not stored on the link row; every link belongs to the
		// same account, so it is read once rather than per row.
		username := ""
		if user, err := repos.Users.Get(ctx, userID); err == nil {
			username = user.Username
		}
		names := map[string]string{}
		for _, row := range rows {
			name := names[row.ProviderID]
			if _, cached := names[row.ProviderID]; !cached {
				provider, err := repos.Providers.Get(ctx, row.ProviderID)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				name = provider.Name
				names[row.ProviderID] = name
			}
			views = append(views, IdentityView{
				ID:           row.ID,
				ProviderID:   row.ProviderID,
				ProviderName: name,
				Subject:      row.Subject,
				Username:     username,
				Email:        row.Email,
				DisplayName:  row.DisplayName,
				LinkedAt:     row.CreatedAt,
				LastLoginAt:  row.LastLoginAt,
			})
		}
		return nil
	})
	return views, err
}

// Unlink removes one external identity. It refuses to remove the only credential
// of an account that has no local password, because that account would then be
// unreachable except through a database edit.
func (s *IdentityService) Unlink(ctx context.Context, actorID, userID, identityID string, actorIsAdmin bool) error {
	if strings.TrimSpace(identityID) == "" {
		return errors.New("identity id is required")
	}
	return s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		user, err := repos.Users.Get(ctx, userID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrAccountNotFound
			}
			return err
		}
		rows, err := repos.Identities.ListByUser(ctx, userID)
		if err != nil {
			return err
		}
		target := ""
		for _, row := range rows {
			if row.ID == identityID {
				target = row.ID
				break
			}
		}
		if target == "" {
			return ErrIdentityNotFound
		}
		if user.PasswordHash == storage.PasswordHashNone && len(rows) <= 1 {
			return ErrIdentityRequiredForLogin
		}
		if err := repos.Identities.Delete(ctx, target); err != nil {
			return err
		}
		remaining, err := repos.Identities.CountByUser(ctx, userID)
		if err != nil {
			return err
		}
		// An account that keeps a local password and no longer has any external
		// identity is simply local again; the mixed label would be a lie.
		if remaining == 0 && user.PasswordHash != storage.PasswordHashNone && user.AuthSource == storage.AuthSourceMixed {
			if err := repos.UserIdentity.SetAuthSource(ctx, userID, storage.AuthSourceLocal); err != nil {
				return err
			}
		}
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  actorID,
			Action:       "auth.identity.unlink",
			ResourceType: "user_identity",
			ResourceID:   target,
			Details:      fmt.Sprintf(`{"userId":%q,"byAdmin":%t}`, userID, actorIsAdmin),
		})
	})
}

// ErrIdentityNotFound reports an unlink target that does not belong to the
// account. It is distinct from a permission failure so a stale console refresh
// produces a 404 rather than a 403.
var ErrIdentityNotFound = errors.New("identity_not_found")

// ErrAccountNotFound reports a missing account.
var ErrAccountNotFound = errors.New("account_not_found")

// ProvisionRequest is everything an OIDC callback knows about the authenticated
// subject. It is a value type so the handler cannot accidentally pass a pointer
// that later mutates.
type ProvisionRequest struct {
	Provider storage.OIDCProvider
	Claims   oidc.Claims
	Username string
	Role     string
	ClientIP string
}

// ProvisionResult reports what provisioning decided, so the handler can audit and
// meter it without re-deriving the reasoning.
type ProvisionResult struct {
	User       storage.User
	Created    bool
	Linked     bool
	RoleChange string
}

// ProvisionFromOIDC resolves an external subject to a local account.
//
// The order matters: an existing link wins, then a username match links the
// identity onto the existing local account, then just-in-time creation. Checking
// the link first is what makes a username change at the IdP harmless, and
// checking the username second is what lets an organisation adopt SSO without
// re-creating every account.
func (s *IdentityService) ProvisionFromOIDC(ctx context.Context, in ProvisionRequest) (ProvisionResult, error) {
	if strings.TrimSpace(in.Provider.ID) == "" || strings.TrimSpace(in.Claims.Subject) == "" {
		return ProvisionResult{}, errors.New("provider and subject are required")
	}
	if !validProvisionUsername(in.Username) {
		return ProvisionResult{}, fmt.Errorf("%w: resolved username does not satisfy the account rule", ErrOIDCUserNotProvisioned)
	}
	var result ProvisionResult
	now := s.now()
	err := s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		link, err := repos.Identities.GetByProviderSubject(ctx, in.Provider.ID, in.Claims.Subject)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && link.ID != "" {
			user, err := repos.Users.Get(ctx, link.UserID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					// The account was hard-deleted but the link survived; treat it as
					// an unlinked subject so provisioning can recreate it.
					if err := repos.Identities.Delete(ctx, link.ID); err != nil {
						return err
					}
				} else {
					return err
				}
			} else {
				if user.Disabled || user.DeletedAt != nil {
					return ErrAccountDisabled
				}
				roleChange, err := applyAuthoritativeRole(ctx, repos, user, in)
				if err != nil {
					return err
				}
				result = ProvisionResult{User: user, RoleChange: roleChange}
				if err := repos.Identities.UpdateLastLogin(ctx, link.ID, now, in.Claims.Email, in.Claims.Username); err != nil {
					return err
				}
				return auditProvision(ctx, repos, user.ID, in, result)
			}
		}

		existing, err := repos.Users.GetByUsername(ctx, in.Username)
		switch {
		case err == nil && existing.ID != "":
			if existing.Disabled || existing.DeletedAt != nil {
				return ErrAccountDisabled
			}
			roleChange, err := applyAuthoritativeRole(ctx, repos, existing, in)
			if err != nil {
				return err
			}
			if _, err := repos.Identities.Create(ctx, storage.UserIdentity{
				UserID:      existing.ID,
				ProviderID:  in.Provider.ID,
				Subject:     in.Claims.Subject,
				Email:       in.Claims.Email,
				DisplayName: in.Claims.Username,
				CreatedAt:   now,
				LastLoginAt: &now,
			}); err != nil {
				if storage.IsDuplicateError(err) {
					return storage.ErrIdentityConflict
				}
				return err
			}
			if existing.PasswordHash != storage.PasswordHashNone && existing.AuthSource == storage.AuthSourceLocal {
				if err := repos.UserIdentity.SetAuthSource(ctx, existing.ID, storage.AuthSourceMixed); err != nil {
					return err
				}
				existing.AuthSource = storage.AuthSourceMixed
			}
			result = ProvisionResult{User: existing, Linked: true, RoleChange: roleChange}
			return auditProvision(ctx, repos, existing.ID, in, result)
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}

		if !in.Provider.AutoCreateUsers {
			return ErrOIDCUserNotProvisioned
		}
		created := storage.User{
			Username:     in.Username,
			Role:         in.Role,
			PasswordHash: storage.PasswordHashNone,
			AuthSource:   storage.AuthSourceOIDC,
		}
		if err := repos.Users.Create(ctx, created); err != nil {
			if storage.IsDuplicateError(err) {
				// A concurrent login for the same subject created the account first.
				// Returning the conflict makes the caller retry the whole flow, which
				// then takes the link path.
				return storage.ErrIdentityConflict
			}
			return err
		}
		user, err := repos.Users.GetByUsername(ctx, in.Username)
		if err != nil {
			return err
		}
		if _, err := repos.Identities.Create(ctx, storage.UserIdentity{
			UserID:      user.ID,
			ProviderID:  in.Provider.ID,
			Subject:     in.Claims.Subject,
			Email:       in.Claims.Email,
			DisplayName: in.Claims.Username,
			CreatedAt:   now,
			LastLoginAt: &now,
		}); err != nil {
			return err
		}
		result = ProvisionResult{User: user, Created: true}
		return auditProvision(ctx, repos, user.ID, in, result)
	})
	if err != nil {
		return ProvisionResult{}, err
	}
	return result, nil
}

// applyAuthoritativeRole enforces the IdP as the source of truth for roles when
// the provider says so. Removing a user from the IdP admin group must remove
// their TunnelMesh admin rights on their next login, or the mapping is decoration.
//
// The one refusal is the last administrator: downgrading them would leave a
// deployment with no way to administer itself, and the recovery path requires the
// console they can no longer reach.
func applyAuthoritativeRole(ctx context.Context, repos storage.IdentityRepositories, user storage.User, in ProvisionRequest) (string, error) {
	if !in.Provider.AuthoritativeRoles || user.Role == in.Role {
		return "", nil
	}
	if user.Role == "admin" && in.Role != "admin" {
		admins, err := repos.Admins.CountActiveAdmins(ctx)
		if err != nil {
			return "", err
		}
		if admins <= 1 {
			return "", ErrLastAdminProtected
		}
	}
	if err := repos.Users.Update(ctx, storage.User{
		ID:           user.ID,
		Username:     user.Username,
		Role:         in.Role,
		PasswordHash: user.PasswordHash,
		Disabled:     user.Disabled,
		DeletedAt:    user.DeletedAt,
		AuthSource:   user.AuthSource,
	}); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s->%s", user.Role, in.Role), nil
}

// auditProvision records the outcome. Only booleans and the provider id are
// stored: the subject and email are personal data with no audit value once the
// link row itself records them.
func auditProvision(ctx context.Context, repos storage.IdentityRepositories, userID string, in ProvisionRequest, result ProvisionResult) error {
	details := map[string]any{
		"providerId": in.Provider.ID,
		"created":    result.Created,
		"linked":     result.Linked,
	}
	if result.RoleChange != "" {
		details["roleChange"] = result.RoleChange
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return repos.Audits.Create(ctx, storage.AuditLog{
		ActorUserID:  userID,
		Action:       "auth.oidc.provision",
		ResourceType: "user",
		ResourceID:   userID,
		Details:      string(encoded),
	})
}

// validProvisionUsername mirrors the account rule so a JIT account can never be
// created in a shape the console and the local user repository would reject.
func validProvisionUsername(username string) bool {
	if len(username) < 3 || len(username) > 64 {
		return false
	}
	for _, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
