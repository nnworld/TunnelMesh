package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var (
	errInvalidTokenRequest   = errors.New("invalid token request")
	errTokenConflict         = errors.New("token mutation conflict")
	errIdempotencyConflict   = errors.New("idempotency conflict")
	errIdempotencyInProgress = errors.New("idempotency request is in progress")
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

// TokenService orchestrates credential lifecycle operations while leaving SQL
// and transaction mechanics in storage repositories.
type TokenService struct {
	db       *storage.DB
	tokens   storage.ServiceTokenRepository
	users    storage.UserRepository
	agents   storage.AgentRepository
	nodes    storage.NodeRepository
	policies storage.PolicyRepository
	audits   storage.AuditRepository
}

// NewTokenService wires management lifecycle orchestration to storage.
func NewTokenService(db *storage.DB) *TokenService {
	if db == nil {
		return nil
	}
	return &TokenService{
		db: db, tokens: db.ServiceTokens(), users: db.Users(), agents: db.Agents(),
		nodes: db.Nodes(), policies: db.Policies(), audits: db.Audits(),
	}
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

// Create validates and plans credential material before atomically committing
// the Token, audit event, and non-secret replay metadata.
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
	defer credentials.Close()
	created, err := credentials.Create(ctx, input)
	if err != nil {
		return TokenView{}, invalidCredentialRequest(err)
	}
	if planner.created == nil {
		return TokenView{}, errors.New("credential planner did not produce a token")
	}
	record := *planner.created
	view, err := s.view(ctx, record)
	if err != nil {
		return TokenView{}, err
	}
	view.Secret = created.Secret
	return s.persistMutation(ctx, actorUserID, idempotencyKey, "token.create", fingerprint, view, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository) error {
		if err := tokens.Create(ctx, record); err != nil {
			return err
		}
		return audits.Create(ctx, tokenAudit(actorUserID, "token.created", record.ID, record, "", ""))
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
	original, err := s.tokens.Get(ctx, id)
	if err != nil {
		return TokenView{}, err
	}
	planner := &plannedTokenRepository{base: s.tokens}
	credentials := auth.NewCredentialService(planner, s.users, s.agents, s.nodes, s.policies)
	defer credentials.Close()
	created, err := credentials.Rotate(ctx, id)
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
	replacement := planner.rotation.replacement
	view, err := s.view(ctx, replacement)
	if err != nil {
		return TokenView{}, err
	}
	view.Secret = created.Secret
	return s.persistMutation(ctx, actorUserID, idempotencyKey, operation, fingerprint, view, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository) error {
		if err := tokens.Rotate(ctx, id, replacement, planner.rotation.when); err != nil {
			return err
		}
		return audits.Create(ctx, tokenAudit(actorUserID, "token.rotated", id, original, replacement.ID, ""))
	})
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
	when := time.Now().UTC()
	err = s.db.ServiceTokenMutationTransaction(ctx, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository, _ storage.IdempotencyRepository) error {
		if err := tokens.Revoke(ctx, id, when); err != nil {
			return err
		}
		return audits.Create(ctx, tokenAudit(actorUserID, "token.revoked", id, record, "", ""))
	})
	if err != nil {
		return TokenView{}, err
	}
	record.RevokedAt = &when
	record.UpdatedAt = when
	return s.view(ctx, record)
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

func (s *TokenService) persistMutation(ctx context.Context, actorUserID, key, operation, fingerprint string, issued TokenView, mutate func(storage.ServiceTokenRepository, storage.AuditRepository) error) (TokenView, error) {
	var result TokenView
	err := s.db.ServiceTokenMutationTransaction(ctx, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository, idempotency storage.IdempotencyRepository) error {
		if key != "" {
			atomic, ok := idempotency.(storage.AtomicIdempotencyRepository)
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
		if err := mutate(tokens, audits); err != nil {
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
		return idempotency.(storage.AtomicIdempotencyRepository).Update(ctx, storage.IdempotencyRecord{
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
	var scope auth.TokenScope
	if err := json.Unmarshal([]byte(record.Scope), &scope); err != nil {
		return TokenView{}, fmt.Errorf("decode token scope: %w", err)
	}
	return TokenView{
		ID: record.ID, Type: record.Type, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID,
		Prefix: record.Prefix, Scope: scope, Status: s.status(ctx, record, scope), ExpiresAt: record.ExpiresAt,
		RevokedAt: record.RevokedAt, LastUsedAt: record.LastUsedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}

func (s *TokenService) status(ctx context.Context, record storage.ServiceToken, scope auth.TokenScope) string {
	now := time.Now().UTC()
	if record.RevokedAt != nil {
		return "revoked"
	}
	if record.ExpiresAt != nil && !record.ExpiresAt.After(now) {
		return "expired"
	}
	owner, err := s.users.Get(ctx, record.OwnerUserID)
	if err != nil || owner.Disabled {
		return "unavailable"
	}
	switch record.Type {
	case storage.TokenTypeAgent:
		agent, err := s.agents.Get(ctx, record.AgentID)
		if err != nil || !agent.Enabled || agent.OwnerUserID != record.OwnerUserID {
			return "unavailable"
		}
	case storage.TokenTypeClient:
		for _, agentID := range scope.AgentIDs {
			agent, err := s.agents.Get(ctx, agentID)
			if err != nil || !agent.Enabled || agent.OwnerUserID != record.OwnerUserID {
				return "unavailable"
			}
		}
	case storage.TokenTypeServerNode:
		node, err := s.nodes.Get(ctx, record.NodeID)
		if err != nil || (node.ExpiresAt != nil && !node.ExpiresAt.After(now)) {
			return "unavailable"
		}
	default:
		return "unavailable"
	}
	return "active"
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

func (r *plannedTokenRepository) TouchLastUsed(context.Context, string, time.Time) error {
	return nil
}

func (r *plannedTokenRepository) Rotate(_ context.Context, _ string, replacement storage.ServiceToken, when time.Time) error {
	r.rotation = &plannedRotation{replacement: replacement, when: when}
	return nil
}

var _ storage.ServiceTokenRepository = (*plannedTokenRepository)(nil)
