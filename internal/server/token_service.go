package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var (
	errInvalidTokenRequest       = errors.New("invalid token request")
	errTokenConflict             = errors.New("token mutation conflict")
	errIdempotencyConflict       = errors.New("idempotency conflict")
	errIdempotencyInProgress     = errors.New("idempotency request is in progress")
	errTokenSecretUnavailable    = errors.New("token secret recovery is not configured")
	errTokenSecretNotRecoverable = errors.New("token secret is not recoverable; rotate the token")
)

// TokenView is the only service-token representation exposed by management
// APIs. It deliberately has no field capable of carrying a stored hash.
type TokenView struct {
	ID          string            `json:"id"`
	Type        storage.TokenType `json:"type"`
	OwnerUserID string            `json:"ownerUserId"`
	AgentID     string            `json:"agentId,omitempty"`
	NodeID      string            `json:"nodeId,omitempty"`
	Prefix      string            `json:"prefix"`
	Scope       auth.TokenScope   `json:"scope"`
	Status      string            `json:"status"`
	ExpiresAt   *time.Time        `json:"expiresAt,omitempty"`
	RevokedAt   *time.Time        `json:"revokedAt,omitempty"`
	LastUsedAt  *time.Time        `json:"lastUsedAt,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
	Secret      string            `json:"secret,omitempty"`
	Replayed    bool              `json:"replayed"`
}

type TokenRevealView struct {
	TokenID    string            `json:"tokenId"`
	Type       storage.TokenType `json:"type"`
	Secret     string            `json:"secret"`
	ExpiresAt  *time.Time        `json:"expiresAt,omitempty"`
	RevealedAt time.Time         `json:"revealedAt"`
	OneTime    bool              `json:"oneTime"`
}

type tokenPage struct {
	Items      []TokenView
	NextCursor string
	HasMore    bool
}

type tokenReplayRecord struct {
	Operation   string    `json:"operation"`
	Fingerprint string    `json:"fingerprint"`
	Token       TokenView `json:"token"`
}

type tokenScopePatch struct {
	Protocols   *[]string `json:"protocols,omitempty"`
	TargetCIDRs *[]string `json:"targetCIDRs,omitempty"`
	TargetPorts *[]int    `json:"targetPorts,omitempty"`
}

type tokenUpdate struct {
	ExpiresAt    *time.Time
	HasExpiresAt bool
	Scope        *tokenScopePatch
}

// TokenService orchestrates credential lifecycle operations while leaving SQL
// and transaction mechanics in storage repositories.
type TokenService struct {
	db          *storage.DB
	tokens      storage.ServiceTokenRepository
	users       storage.UserRepository
	agents      storage.AgentRepository
	nodes       storage.NodeRepository
	policies    storage.PolicyRepository
	audits      storage.AuditRepository
	secretStore *auth.SecretStore
}

// NewTokenService wires management lifecycle orchestration to storage.
func NewTokenService(db *storage.DB) *TokenService {
	if db == nil {
		return nil
	}
	service := &TokenService{
		db: db, tokens: db.ServiceTokens(), users: db.Users(), agents: db.Agents(),
		nodes: db.Nodes(), policies: db.Policies(), audits: db.Audits(),
	}
	if encodedKey := strings.TrimSpace(os.Getenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY")); encodedKey != "" {
		keyID := strings.TrimSpace(os.Getenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID"))
		if keyID == "" {
			keyID = "default"
		}
		if store, err := auth.NewSecretStore(encodedKey, keyID); err == nil {
			service.secretStore = store
		}
	}
	return service
}

func (s *TokenService) SetSecretStore(store *auth.SecretStore) {
	if s != nil {
		s.secretStore = store
	}
}

func (s *TokenService) Reveal(ctx context.Context, actorUserID, id string) (TokenRevealView, error) {
	if s == nil || s.secretStore == nil {
		return TokenRevealView{}, errTokenSecretUnavailable
	}
	record, err := s.tokens.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return TokenRevealView{}, err
	}
	if record.SecretCiphertext == "" || record.SecretNonce == "" || record.SecretKeyID == "" || record.SecretVersion == 0 {
		return TokenRevealView{}, errTokenSecretNotRecoverable
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(record.SecretCiphertext)
	if err != nil {
		return TokenRevealView{}, errors.New("stored token secret is invalid")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(record.SecretNonce)
	if err != nil {
		return TokenRevealView{}, errors.New("stored token nonce is invalid")
	}
	secret, err := s.secretStore.Decrypt(ciphertext, nonce, record.SecretKeyID, record.SecretVersion)
	if err != nil {
		return TokenRevealView{}, err
	}
	readAt := time.Now().UTC()
	if marker, ok := s.tokens.(interface {
		MarkSecretRead(context.Context, string, time.Time) error
	}); ok {
		if err := marker.MarkSecretRead(ctx, record.ID, readAt); err != nil {
			return TokenRevealView{}, err
		}
	}
	if s.audits != nil {
		if err := s.audits.Create(ctx, tokenAudit(actorUserID, "token.secret_revealed", record.ID, record, "", "one_time=true")); err != nil {
			return TokenRevealView{}, err
		}
	}
	return TokenRevealView{TokenID: record.ID, Type: record.Type, Secret: secret, ExpiresAt: record.ExpiresAt, RevealedAt: readAt, OneTime: true}, nil
}

// GetAgent exposes the resource fact needed by handler-level owner checks.
func (s *TokenService) GetAgent(ctx context.Context, id string) (storage.Agent, error) {
	return s.agents.Get(ctx, strings.TrimSpace(id))
}

// Get returns both the internal ownership fact and its redacted API view.
func (s *TokenService) Get(ctx context.Context, id string) (storage.ServiceToken, TokenView, error) {
	record, err := s.tokens.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return storage.ServiceToken{}, TokenView{}, err
	}
	view, err := s.view(ctx, record)
	return record, view, err
}

// List applies repository-side filters before deriving public Token status.
func (s *TokenService) List(ctx context.Context, filter storage.ServiceTokenFilter, cursor string, limit int) (tokenPage, error) {
	page, err := s.tokens.List(ctx, filter, cursor, limit)
	if err != nil {
		return tokenPage{}, err
	}
	out := tokenPage{Items: make([]TokenView, 0, len(page.Items)), NextCursor: page.NextCursor, HasMore: page.HasMore}
	for _, record := range page.Items {
		view, err := s.view(ctx, record)
		if err != nil {
			return tokenPage{}, err
		}
		out.Items = append(out.Items, view)
	}
	return out, nil
}

// Create performs a fast preflight before repeating the authoritative
// authorization checks and credential write in one database transaction.
func (s *TokenService) Create(ctx context.Context, actorUserID, idempotencyKey string, input auth.CreateTokenInput) (TokenView, error) {
	fingerprint, err := mutationFingerprint("token.create", input)
	if err != nil {
		return TokenView{}, err
	}
	if replay, found, err := s.completedReplay(ctx, actorUserID, idempotencyKey, "token.create", fingerprint); found || err != nil {
		return replay, err
	}
	planner := &plannedTokenRepository{base: s.tokens}
	credentials := auth.NewCredentialService(planner, s.users, s.agents, s.nodes, s.policies)
	credentials.SetSecretStore(s.secretStore)
	defer credentials.Close()
	_, err = credentials.Create(ctx, input)
	if err != nil {
		return TokenView{}, invalidCredentialRequest(err)
	}
	if planner.created == nil {
		return TokenView{}, errors.New("credential planner did not produce a token")
	}
	return s.persistMutation(ctx, actorUserID, idempotencyKey, "token.create", fingerprint, func(repos storage.ServiceTokenMutationRepositories) (TokenView, error) {
		credentials := auth.NewCredentialService(repos.Tokens, repos.Users, repos.Agents, repos.Nodes, s.policies)
		credentials.SetSecretStore(s.secretStore)
		defer credentials.Close()
		created, err := credentials.Create(ctx, input)
		if err != nil {
			return TokenView{}, invalidCredentialRequest(err)
		}
		record, err := repos.Tokens.Get(ctx, created.TokenID)
		if err != nil {
			return TokenView{}, err
		}
		view, err := tokenView(ctx, record, repos.Users, repos.Agents, repos.Nodes)
		if err != nil {
			return TokenView{}, err
		}
		view.Secret = created.Secret
		if err := repos.Audits.Create(ctx, tokenAudit(actorUserID, "token.created", record.ID, record, "", "")); err != nil {
			return TokenView{}, err
		}
		return view, nil
	})
}

// Rotate creates one replacement, atomically revokes the old Token, and safely
// resolves concurrent same-key requests to a non-secret replay.
func (s *TokenService) Rotate(ctx context.Context, actorUserID, id, idempotencyKey string) (TokenView, error) {
	id = strings.TrimSpace(id)
	operation := "token.rotate:" + id
	fingerprint, err := mutationFingerprint(operation, struct {
		TokenID string `json:"tokenId"`
	}{TokenID: id})
	if err != nil {
		return TokenView{}, err
	}
	if replay, found, err := s.completedReplay(ctx, actorUserID, idempotencyKey, operation, fingerprint); found || err != nil {
		return replay, err
	}
	if _, err := s.tokens.Get(ctx, id); err != nil {
		return TokenView{}, err
	}
	planner := &plannedTokenRepository{base: s.tokens}
	credentials := auth.NewCredentialService(planner, s.users, s.agents, s.nodes, s.policies)
	credentials.SetSecretStore(s.secretStore)
	defer credentials.Close()
	_, err = credentials.Rotate(ctx, id)
	if err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, storage.ErrServiceTokenRevoked) {
			// A concurrent winner may have committed the old-token revocation and
			// replay metadata between the initial lookup and lifecycle planning.
			if replay, found, replayErr := s.completedReplay(ctx, actorUserID, idempotencyKey, operation, fingerprint); found || replayErr != nil {
				return replay, replayErr
			}
			return TokenView{}, fmt.Errorf("%w: token cannot be rotated", errTokenConflict)
		}
		return TokenView{}, err
	}
	if planner.rotation == nil {
		return TokenView{}, errors.New("credential planner did not produce a rotation")
	}
	view, err := s.persistMutation(ctx, actorUserID, idempotencyKey, operation, fingerprint, func(repos storage.ServiceTokenMutationRepositories) (TokenView, error) {
		original, err := repos.Tokens.Get(ctx, id)
		if err != nil {
			return TokenView{}, err
		}
		credentials := auth.NewCredentialService(repos.Tokens, repos.Users, repos.Agents, repos.Nodes, s.policies)
		credentials.SetSecretStore(s.secretStore)
		defer credentials.Close()
		created, err := credentials.Rotate(ctx, id)
		if err != nil {
			return TokenView{}, err
		}
		replacement, err := repos.Tokens.Get(ctx, created.TokenID)
		if err != nil {
			return TokenView{}, err
		}
		view, err := tokenView(ctx, replacement, repos.Users, repos.Agents, repos.Nodes)
		if err != nil {
			return TokenView{}, err
		}
		view.Secret = created.Secret
		if err := repos.Audits.Create(ctx, tokenAudit(actorUserID, "token.rotated", id, original, replacement.ID, "")); err != nil {
			return TokenView{}, err
		}
		return view, nil
	})
	if err == nil {
		return view, nil
	}
	if errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, storage.ErrServiceTokenRevoked) || errors.Is(err, sql.ErrNoRows) {
		if replay, found, replayErr := s.completedReplay(ctx, actorUserID, idempotencyKey, operation, fingerprint); found || replayErr != nil {
			return replay, replayErr
		}
		return TokenView{}, fmt.Errorf("%w: token cannot be rotated", errTokenConflict)
	}
	return TokenView{}, err
}

// Revoke commits the lifecycle timestamp and audit record together.
func (s *TokenService) Revoke(ctx context.Context, actorUserID, id string) (TokenView, error) {
	id = strings.TrimSpace(id)
	record, err := s.tokens.Get(ctx, id)
	if err != nil {
		return TokenView{}, err
	}
	if record.RevokedAt != nil {
		return s.view(ctx, record)
	}
	var result storage.ServiceToken
	err = s.db.ServiceTokenMutationTransaction(ctx, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository, _ storage.IdempotencyRepository) error {
		current, err := tokens.Get(ctx, id)
		if err != nil {
			return err
		}
		if current.RevokedAt != nil {
			result = current
			return nil
		}
		when := time.Now().UTC()
		if err := tokens.Revoke(ctx, id, when); err != nil {
			return err
		}
		if err := audits.Create(ctx, tokenAudit(actorUserID, "token.revoked", id, current, "", "")); err != nil {
			return err
		}
		current.RevokedAt = &when
		current.UpdatedAt = when
		result = current
		return nil
	})
	if err != nil {
		return TokenView{}, err
	}
	return s.view(ctx, result)
}

// UpdateExpiration changes only the active credential's deadline. Clearing the
// value makes the Token non-expiring; an expired Token must be rotated instead.
func (s *TokenService) UpdateExpiration(ctx context.Context, actorUserID, id string, expiresAt *time.Time) (TokenView, error) {
	return s.Update(ctx, actorUserID, id, tokenUpdate{ExpiresAt: expiresAt, HasExpiresAt: true})
}

// Update applies expiration and authorization-scope changes in one transaction.
// Scope fields not present in the patch remain unchanged; empty arrays remove
// the corresponding restriction.
func (s *TokenService) Update(ctx context.Context, actorUserID, id string, update tokenUpdate) (TokenView, error) {
	id = strings.TrimSpace(id)
	if !update.HasExpiresAt && update.Scope == nil {
		return TokenView{}, fmt.Errorf("%w: expiresAt or scope is required", errInvalidTokenRequest)
	}
	if update.HasExpiresAt && update.ExpiresAt != nil && !update.ExpiresAt.After(time.Now().UTC()) {
		return TokenView{}, fmt.Errorf("%w: expiresAt must be in the future", errInvalidTokenRequest)
	}
	var result storage.ServiceToken
	err := s.db.ServiceTokenMutationTransaction(ctx, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository, _ storage.IdempotencyRepository) error {
		current, err := tokens.Get(ctx, id)
		if err != nil {
			return err
		}
		if current.RevokedAt != nil {
			return storage.ErrServiceTokenRevoked
		}
		if current.ExpiresAt != nil && !current.ExpiresAt.After(time.Now().UTC()) {
			return storage.ErrServiceTokenExpired
		}
		when := time.Now().UTC()
		if update.Scope != nil {
			scope, err := mergeTokenScope(current.Scope, *update.Scope)
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(scope)
			if err != nil {
				return fmt.Errorf("encode token scope: %w", err)
			}
			if err := tokens.UpdateScope(ctx, id, string(encoded), when); err != nil {
				return err
			}
		}
		if update.HasExpiresAt {
			if err := tokens.UpdateExpiration(ctx, id, update.ExpiresAt, when); err != nil {
				return err
			}
		}
		updated, err := tokens.Get(ctx, id)
		if err != nil {
			return err
		}
		if update.Scope != nil {
			if err := audits.Create(ctx, tokenAudit(actorUserID, "token.scope_updated", id, updated, "", "")); err != nil {
				return err
			}
		}
		if update.HasExpiresAt {
			if err := audits.Create(ctx, tokenAudit(actorUserID, "token.expiration_updated", id, updated, "", "")); err != nil {
				return err
			}
		}
		result = updated
		return nil
	})
	if err != nil {
		return TokenView{}, err
	}
	return s.view(ctx, result)
}

func mergeTokenScope(raw string, patch tokenScopePatch) (auth.TokenScope, error) {
	var scope auth.TokenScope
	if err := json.Unmarshal([]byte(raw), &scope); err != nil {
		return auth.TokenScope{}, fmt.Errorf("decode token scope: %w", err)
	}
	if patch.Protocols != nil {
		scope.Protocols = *patch.Protocols
	}
	if patch.TargetCIDRs != nil {
		scope.TargetCIDRs = *patch.TargetCIDRs
	}
	if patch.TargetPorts != nil {
		scope.TargetPorts = *patch.TargetPorts
	}
	normalized, err := auth.NormalizeTokenScope(scope)
	if err != nil {
		return auth.TokenScope{}, invalidCredentialRequest(err)
	}
	return normalized, nil
}

// RecordAuthorizationDenied persists only allowlisted non-secret identifiers.
func (s *TokenService) RecordAuthorizationDenied(ctx context.Context, actorUserID string, record storage.ServiceToken, reasonCode string) error {
	resourceID := record.ID
	if resourceID == "" {
		if record.AgentID != "" {
			resourceID = record.AgentID
		} else {
			resourceID = record.NodeID
		}
	}
	return s.audits.Create(ctx, tokenAudit(actorUserID, "token.authorization_denied", resourceID, record, "", reasonCode))
}

func (s *TokenService) persistMutation(ctx context.Context, actorUserID, key, operation, fingerprint string, mutate func(storage.ServiceTokenMutationRepositories) (TokenView, error)) (TokenView, error) {
	var result TokenView
	err := s.db.ServiceTokenAuthorizedMutationTransaction(ctx, func(repos storage.ServiceTokenMutationRepositories) error {
		if key != "" {
			atomic, ok := repos.Idempotency.(storage.AtomicIdempotencyRepository)
			if !ok {
				return errors.New("atomic idempotency repository is required")
			}
			existing, claimed, err := atomic.Claim(ctx, storage.IdempotencyRecord{Key: key, UserID: actorUserID})
			if err != nil {
				return err
			}
			if !claimed {
				replay, err := decodeTokenReplay(existing, actorUserID, operation, fingerprint)
				if err != nil {
					return err
				}
				result = replay
				return nil
			}
		}
		issued, err := mutate(repos)
		if err != nil {
			return err
		}
		result = issued
		if key == "" {
			return nil
		}
		replay := issued
		replay.Secret = ""
		replay.Replayed = true
		encoded, err := json.Marshal(tokenReplayRecord{Operation: operation, Fingerprint: fingerprint, Token: replay})
		if err != nil {
			return err
		}
		expiresAt := time.Now().UTC().Add(24 * time.Hour)
		return repos.Idempotency.(storage.AtomicIdempotencyRepository).Update(ctx, storage.IdempotencyRecord{
			Key: key, UserID: actorUserID, Response: string(encoded), StatusCode: 201, ExpiresAt: &expiresAt,
		})
	})
	return result, err
}

func decodeTokenReplay(record storage.IdempotencyRecord, actorUserID, operation, fingerprint string) (TokenView, error) {
	if record.UserID != "" && record.UserID != actorUserID {
		return TokenView{}, fmt.Errorf("%w: key belongs to another user", errIdempotencyConflict)
	}
	if record.StatusCode == 102 {
		return TokenView{}, errIdempotencyInProgress
	}
	var replay tokenReplayRecord
	if err := json.Unmarshal([]byte(record.Response), &replay); err != nil {
		return TokenView{}, fmt.Errorf("%w: invalid replay metadata", errIdempotencyConflict)
	}
	if replay.Operation != operation || replay.Fingerprint != fingerprint || replay.Token.ID == "" {
		return TokenView{}, fmt.Errorf("%w: key was used for another request", errIdempotencyConflict)
	}
	replay.Token.Secret = ""
	replay.Token.Replayed = true
	return replay.Token, nil
}

func (s *TokenService) completedReplay(ctx context.Context, actorUserID, key, operation, fingerprint string) (TokenView, bool, error) {
	if key == "" {
		return TokenView{}, false, nil
	}
	record, err := s.db.Idempotency().Get(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenView{}, false, nil
	}
	if err != nil {
		return TokenView{}, false, err
	}
	replay, err := decodeTokenReplay(record, actorUserID, operation, fingerprint)
	return replay, true, err
}

func mutationFingerprint(operation string, payload any) (string, error) {
	encoded, err := json.Marshal(struct {
		Operation string `json:"operation"`
		Payload   any    `json:"payload"`
	}{Operation: operation, Payload: payload})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (s *TokenService) view(ctx context.Context, record storage.ServiceToken) (TokenView, error) {
	return tokenView(ctx, record, s.users, s.agents, s.nodes)
}

func tokenView(ctx context.Context, record storage.ServiceToken, users storage.UserRepository, agents storage.AgentRepository, nodes storage.NodeRepository) (TokenView, error) {
	var scope auth.TokenScope
	if err := json.Unmarshal([]byte(record.Scope), &scope); err != nil {
		return TokenView{}, fmt.Errorf("decode token scope: %w", err)
	}
	status, err := tokenStatus(ctx, record, scope, users, agents, nodes)
	if err != nil {
		return TokenView{}, err
	}
	return TokenView{
		ID: record.ID, Type: record.Type, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID,
		Prefix: record.Prefix, Scope: scope, Status: status, ExpiresAt: record.ExpiresAt,
		RevokedAt: record.RevokedAt, LastUsedAt: record.LastUsedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}

func tokenStatus(ctx context.Context, record storage.ServiceToken, scope auth.TokenScope, users storage.UserRepository, agents storage.AgentRepository, nodes storage.NodeRepository) (string, error) {
	now := time.Now().UTC()
	if record.RevokedAt != nil {
		return "revoked", nil
	}
	if record.ExpiresAt != nil && !record.ExpiresAt.After(now) {
		return "expired", nil
	}
	owner, err := users.Get(ctx, record.OwnerUserID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner.Disabled) {
		return "unavailable", nil
	}
	if err != nil {
		return "", err
	}
	switch record.Type {
	case storage.TokenTypeAgent:
		agent, err := agents.Get(ctx, record.AgentID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (!agent.Enabled || agent.OwnerUserID != record.OwnerUserID)) {
			return "unavailable", nil
		}
		if err != nil {
			return "", err
		}
	case storage.TokenTypeClient:
		for _, agentID := range scope.AgentIDs {
			agent, err := agents.Get(ctx, agentID)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && (!agent.Enabled || agent.OwnerUserID != record.OwnerUserID)) {
				return "unavailable", nil
			}
			if err != nil {
				return "", err
			}
		}
	case storage.TokenTypeServerNode:
		node, err := nodes.Get(ctx, record.NodeID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && node.ExpiresAt != nil && !node.ExpiresAt.After(now)) {
			return "unavailable", nil
		}
		if err != nil {
			return "", err
		}
	default:
		return "unavailable", nil
	}
	return "active", nil
}

func invalidCredentialRequest(err error) error {
	if errors.Is(err, auth.ErrInvalidTokenType) || errors.Is(err, auth.ErrInvalidTokenScope) || errors.Is(err, auth.ErrUnauthenticated) ||
		strings.Contains(strings.ToLower(err.Error()), "expiration") || strings.Contains(strings.ToLower(err.Error()), "requires") || strings.Contains(strings.ToLower(err.Error()), "bound") {
		return fmt.Errorf("%w: %v", errInvalidTokenRequest, err)
	}
	return err
}

func tokenAudit(actorUserID, action, resourceID string, record storage.ServiceToken, replacementTokenID, reasonCode string) storage.AuditLog {
	details := map[string]any{}
	if record.Type != "" {
		details["type"] = record.Type
	}
	if record.Prefix != "" {
		details["prefix"] = record.Prefix
	}
	if record.OwnerUserID != "" {
		details["ownerUserId"] = record.OwnerUserID
	}
	if record.AgentID != "" {
		details["agentId"] = record.AgentID
	}
	if record.NodeID != "" {
		details["nodeId"] = record.NodeID
	}
	if record.ID != "" {
		details["tokenId"] = record.ID
	}
	if replacementTokenID != "" {
		details["replacementTokenId"] = replacementTokenID
	}
	if reasonCode != "" {
		details["reasonCode"] = reasonCode
	}
	encoded, _ := json.Marshal(details)
	return storage.AuditLog{ActorUserID: actorUserID, Action: action, ResourceType: "service_token", ResourceID: resourceID, Details: string(encoded)}
}

type plannedRotation struct {
	replacement storage.ServiceToken
	when        time.Time
}

// plannedTokenRepository lets CredentialService perform its complete domain
// validation and secret generation before the real database transaction. Only
// the transaction-bound repository later persists the captured mutation.
type plannedTokenRepository struct {
	base     storage.ServiceTokenRepository
	created  *storage.ServiceToken
	rotation *plannedRotation
}

func (r *plannedTokenRepository) Create(_ context.Context, token storage.ServiceToken) error {
	r.created = &token
	return nil
}

func (r *plannedTokenRepository) Get(ctx context.Context, id string) (storage.ServiceToken, error) {
	return r.base.Get(ctx, id)
}

func (r *plannedTokenRepository) GetByHash(ctx context.Context, hash string) (storage.ServiceToken, error) {
	return r.base.GetByHash(ctx, hash)
}

func (r *plannedTokenRepository) List(ctx context.Context, filter storage.ServiceTokenFilter, cursor string, limit int) (storage.Page[storage.ServiceToken], error) {
	return r.base.List(ctx, filter, cursor, limit)
}

func (r *plannedTokenRepository) Revoke(context.Context, string, time.Time) error {
	return errors.New("planned repository cannot revoke")
}

func (r *plannedTokenRepository) UpdateExpiration(context.Context, string, *time.Time, time.Time) error {
	return errors.New("planned repository cannot update expiration")
}

func (r *plannedTokenRepository) UpdateScope(context.Context, string, string, time.Time) error {
	return errors.New("planned repository cannot update scope")
}

func (r *plannedTokenRepository) TouchLastUsed(context.Context, string, time.Time) error {
	return nil
}

func (r *plannedTokenRepository) Rotate(_ context.Context, _ string, replacement storage.ServiceToken, when time.Time) error {
	r.rotation = &plannedRotation{replacement: replacement, when: when}
	return nil
}

var _ storage.ServiceTokenRepository = (*plannedTokenRepository)(nil)
