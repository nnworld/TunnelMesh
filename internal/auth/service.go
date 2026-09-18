package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthenticated    = errors.New("unauthenticated")
	ErrForbidden          = errors.New("forbidden")
	ErrInvalidRole        = errors.New("invalid role")
	ErrInvalidTokenType   = errors.New("invalid token type")
	ErrInvalidTokenScope  = errors.New("invalid token scope")
)

type Principal struct {
	UserID   string
	Username string
	Role     string
}

type LoginResult struct {
	User  storage.User
	Token string
}

type AuthService struct {
	users  storage.UserRepository
	tokens storage.TokenRepository
}

// NewAuthService accepts either a *storage.DB or the repository interfaces,
// keeping the service independent from SQL and straightforward to unit test.
func NewAuthService(source any, repos ...any) *AuthService {
	s := &AuthService{}
	switch v := source.(type) {
	case *storage.DB:
		s.users, s.tokens = v.Users(), v.Tokens()
	case storage.UserRepository:
		s.users = v
	}
	for _, repo := range repos {
		switch v := repo.(type) {
		case storage.UserRepository:
			s.users = v
		case storage.TokenRepository:
			s.tokens = v
		}
	}
	return s
}

func (s *AuthService) CreateUser(ctx context.Context, username, password, role string) (storage.User, error) {
	if s == nil || s.users == nil {
		return storage.User{}, errors.New("user repository is required")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return storage.User{}, errors.New("username must not be empty")
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		role = "user"
	}
	if role != "admin" && role != "user" {
		return storage.User{}, ErrInvalidRole
	}
	hash, err := hashPassword(password)
	if err != nil {
		return storage.User{}, err
	}
	u := storage.User{Username: username, Role: role, PasswordHash: hash}
	if err := s.users.Create(ctx, u); err != nil {
		return storage.User{}, err
	}
	return s.users.GetByUsername(ctx, username)
}

func (s *AuthService) Login(ctx context.Context, username, password string) (LoginResult, error) {
	if s == nil || s.users == nil || s.tokens == nil {
		return LoginResult{}, errors.New("auth repositories are required")
	}
	u, err := s.users.GetByUsername(ctx, strings.TrimSpace(username))
	if err != nil || u.Disabled || u.DeletedAt != nil || !verifyPassword(password, u.PasswordHash) {
		return LoginResult{}, ErrInvalidCredentials
	}
	plain, err := s.IssueToken(ctx, u.ID, 0)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{User: u, Token: plain}, nil
}

// IssueToken creates one console bearer token. A non-positive ttl reproduces the
// historical non-expiring token so existing callers and previously issued
// sessions keep working; a positive ttl is what an operator-set session lifetime
// uses. Only the one-way hash is persisted.
func (s *AuthService) IssueToken(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	if s == nil || s.tokens == nil {
		return "", errors.New("token repository is required")
	}
	if strings.TrimSpace(userID) == "" {
		return "", errors.New("user id is required")
	}
	plain, err := randomToken(32)
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := storage.APIToken{UserID: userID, TokenHash: hashToken(plain)}
	if ttl > 0 {
		expiresAt := time.Now().UTC().Add(ttl)
		token.ExpiresAt = &expiresAt
	}
	if err := s.tokens.Create(ctx, token); err != nil {
		return "", err
	}
	return plain, nil
}

func (s *AuthService) ValidateToken(ctx context.Context, plain string) (Principal, error) {
	if s == nil || s.users == nil || s.tokens == nil || strings.TrimSpace(plain) == "" {
		return Principal{}, ErrUnauthenticated
	}
	t, err := s.tokens.GetByHash(ctx, hashToken(plain))
	if err != nil || t.RevokedAt != nil || (t.ExpiresAt != nil && !t.ExpiresAt.After(time.Now().UTC())) {
		return Principal{}, ErrUnauthenticated
	}
	u, err := s.users.Get(ctx, t.UserID)
	if err != nil || u.Disabled || u.DeletedAt != nil {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{UserID: u.ID, Username: u.Username, Role: u.Role}, nil
}

func (s *AuthService) RequireRole(p Principal, role string) error {
	if p.UserID == "" {
		return ErrUnauthenticated
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if p.Role != role && !(p.Role == "admin" && role == "user") {
		return ErrForbidden
	}
	return nil
}

func (s *AuthService) HasRole(p Principal, role string) bool { return s.RequireRole(p, role) == nil }

// Authorize is an alias used by HTTP handlers to make RBAC checks read
// naturally at the call site.
func (s *AuthService) Authorize(p Principal, role string) error { return s.RequireRole(p, role) }
