package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var (
	ErrConfirmRequired    = errors.New("credential regeneration requires --confirm")
	ErrRecoveryInProgress = errors.New("credential recovery is already in progress")
	ErrAdminAlreadyExists = errors.New("admin account already exists")
)

type Credentials struct {
	Username string
	Password string
}

type BootstrapService struct {
	users  storage.UserRepository
	tokens storage.TokenRepository
	audits storage.AuditRepository
	mu     *sync.Mutex
	tx     func(context.Context, func(storage.UserRepository, storage.TokenRepository, storage.AuditRepository) error) error
}

var processRecoveryMu sync.Mutex

func NewBootstrapService(source any, repos ...any) *BootstrapService {
	// Passing *storage.DB enables the transactional, cross-process lock path.
	// Repository-only construction remains useful for isolated tests and simple
	// embedders, but cannot coordinate writers outside the current process.
	s := &BootstrapService{mu: &processRecoveryMu}
	switch v := source.(type) {
	case *storage.DB:
		s.users, s.tokens, s.audits = v.Users(), v.Tokens(), v.Audits()
		s.tx = v.AuthTransaction
	case storage.UserRepository:
		s.users = v
	}
	for _, repo := range repos {
		switch v := repo.(type) {
		case storage.UserRepository:
			s.users = v
		case storage.TokenRepository:
			s.tokens = v
		case storage.AuditRepository:
			s.audits = v
		}
	}
	return s
}

func (s *BootstrapService) EnsureAdmin(ctx context.Context) (Credentials, error) {
	if s == nil || s.users == nil {
		return Credentials{}, errors.New("user repository is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var result Credentials
	work := func(users storage.UserRepository, _ storage.TokenRepository, _ storage.AuditRepository) error {
		admin, err := findAdmin(ctx, users)
		if err != nil || admin.ID != "" {
			return err
		}
		username, err := randomUsername()
		if err != nil {
			return err
		}
		password, err := randomToken(24)
		if err != nil {
			return err
		}
		hash, err := hashPassword(password)
		if err != nil {
			return err
		}
		if err := users.Create(ctx, storage.User{Username: username, Role: "admin", PasswordHash: hash}); err != nil {
			return err
		}
		result = Credentials{Username: username, Password: password}
		return nil
	}
	if s.tx != nil {
		if err := s.tx(ctx, work); err != nil {
			return Credentials{}, err
		}
		return result, nil
	}
	return result, work(s.users, s.tokens, s.audits)
}

// BootstrapAdmin creates the first administrator and returns its one-time
// credentials. It refuses to act when an administrator already exists so a
// mistakenly selected database cannot silently create or replace credentials.
func (s *BootstrapService) BootstrapAdmin(ctx context.Context) (Credentials, error) {
	creds, err := s.EnsureAdmin(ctx)
	if err != nil {
		return Credentials{}, err
	}
	if creds.Username == "" {
		return Credentials{}, ErrAdminAlreadyExists
	}
	return creds, nil
}

func (s *BootstrapService) RegenerateCredentials(ctx context.Context, confirm bool) (Credentials, error) {
	if !confirm {
		return Credentials{}, ErrConfirmRequired
	}
	if s == nil || s.users == nil || s.tokens == nil || s.audits == nil {
		return Credentials{}, errors.New("bootstrap repositories are required")
	}
	if !s.mu.TryLock() {
		return Credentials{}, ErrRecoveryInProgress
	}
	defer s.mu.Unlock()
	var result Credentials
	work := func(users storage.UserRepository, tokens storage.TokenRepository, audits storage.AuditRepository) error {
		admin, err := findAdmin(ctx, users)
		if err != nil {
			return err
		}
		if admin.ID == "" {
			return errors.New("admin account does not exist")
		}
		password, err := randomToken(24)
		if err != nil {
			return err
		}
		hash, err := hashPassword(password)
		if err != nil {
			return err
		}
		admin.PasswordHash, admin.UpdatedAt = hash, time.Now().UTC()
		if err := users.Update(ctx, admin); err != nil {
			return err
		}
		now, cursor := time.Now().UTC(), ""
		for {
			page, err := tokens.List(ctx, cursor, 500)
			if err != nil {
				return err
			}
			for _, tok := range page.Items {
				if tok.UserID == admin.ID && tok.RevokedAt == nil {
					if err := tokens.Revoke(ctx, tok.ID, now); err != nil {
						return err
					}
				}
			}
			if !page.HasMore || page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		details := fmt.Sprintf(`{"username":%q}`, admin.Username)
		if err := audits.Create(ctx, storage.AuditLog{ActorUserID: admin.ID, Action: "admin.credentials_regenerated", ResourceType: "user", ResourceID: admin.ID, Details: details}); err != nil {
			return err
		}
		result = Credentials{Username: admin.Username, Password: password}
		return nil
	}
	if s.tx != nil {
		if err := s.tx(ctx, work); err != nil {
			return Credentials{}, err
		}
		return result, nil
	}
	return result, work(s.users, s.tokens, s.audits)
}

func findAdmin(ctx context.Context, users storage.UserRepository) (storage.User, error) {
	cursor := ""
	for {
		page, err := users.List(ctx, cursor, 500)
		if err != nil {
			return storage.User{}, err
		}
		for _, u := range page.Items {
			if strings.EqualFold(u.Role, "admin") {
				return u, nil
			}
		}
		if !page.HasMore || page.NextCursor == "" {
			return storage.User{}, nil
		}
		cursor = page.NextCursor
	}
}

func randomUsername() (string, error) {
	tok, err := randomToken(6)
	if err != nil {
		return "", err
	}
	return "admin-" + tok[:8], nil
}
