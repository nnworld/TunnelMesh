package server

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// handleTokens routes only the versioned management Token collection and
// resource actions after management-session authentication.
func (a *API) handleTokens(w http.ResponseWriter, r *http.Request, principal auth.Principal, parts []string) {
	if a.tokenService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "token service unavailable")
		return
	}
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			a.listTokens(w, r, principal)
		case http.MethodPost:
			a.createToken(w, r, principal)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	record, view, err := a.tokenService.Get(r.Context(), parts[0])
	if err != nil {
		writeTokenError(w, err)
		return
	}
	if !isAdmin(principal) && record.OwnerUserID != principal.UserID {
		a.denyToken(r, principal, record, "token_not_owned")
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	switch parts[1] {
	case "rotate":
		key, ok := tokenIdempotencyKey(w, r)
		if !ok {
			return
		}
		rotated, err := a.tokenService.Rotate(r.Context(), principal.UserID, record.ID, key)
		if err != nil {
			writeTokenError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, rotated)
	case "revoke":
		revoked, err := a.tokenService.Revoke(r.Context(), principal.UserID, record.ID)
		if err != nil {
			writeTokenError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, revoked)
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

// listTokens fixes non-admin filtering to the caller before repository paging.
func (a *API) listTokens(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	tokenType := storage.TokenType(strings.TrimSpace(r.URL.Query().Get("type")))
	if tokenType != "" && !validManagementTokenType(tokenType) {
		writeAPIError(w, http.StatusBadRequest, "invalid token type")
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	if !isAdmin(principal) {
		if owner != "" && owner != principal.UserID {
			a.denyToken(r, principal, storage.ServiceToken{Type: tokenType, OwnerUserID: owner}, "owner_filter_forbidden")
			writeAPIError(w, http.StatusForbidden, "forbidden")
			return
		}
		owner = principal.UserID
	}
	page, err := a.tokenService.List(r.Context(), storage.ServiceTokenFilter{OwnerUserID: owner, Type: tokenType}, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		writeTokenError(w, err)
		return
	}
	items := make([]any, len(page.Items))
	for index := range page.Items {
		items[index] = page.Items[index]
	}
	writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
}

// createToken owns transport decoding and caller/resource authorization; the
// service owns lifecycle validation and transaction orchestration.
func (a *API) createToken(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	var request struct {
		Type        storage.TokenType `json:"type"`
		OwnerUserID string            `json:"ownerUserId"`
		AgentID     string            `json:"agentId"`
		NodeID      string            `json:"nodeId"`
		Scope       auth.TokenScope   `json:"scope"`
		ExpiresAt   *time.Time        `json:"expiresAt"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON or expiresAt")
		return
	}
	request.OwnerUserID = strings.TrimSpace(request.OwnerUserID)
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.NodeID = strings.TrimSpace(request.NodeID)
	if !validManagementTokenType(request.Type) {
		writeAPIError(w, http.StatusBadRequest, "invalid token type")
		return
	}
	if request.OwnerUserID == "" {
		request.OwnerUserID = principal.UserID
	}
	requested := storage.ServiceToken{Type: request.Type, OwnerUserID: request.OwnerUserID, AgentID: request.AgentID, NodeID: request.NodeID}
	if !isAdmin(principal) {
		if request.OwnerUserID != principal.UserID {
			a.denyToken(r, principal, requested, "owner_not_self")
			writeAPIError(w, http.StatusForbidden, "forbidden")
			return
		}
		if request.Type == storage.TokenTypeServerNode {
			a.denyToken(r, principal, requested, "server_node_admin_required")
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		if request.Type == storage.TokenTypeAgent && !a.callerOwnsEnabledAgent(r, principal, request.AgentID, requested, "agent_not_owned_or_enabled") {
			writeAPIError(w, http.StatusForbidden, "forbidden")
			return
		}
		if request.Type == storage.TokenTypeClient {
			for _, agentID := range request.Scope.AgentIDs {
				requested.AgentID = strings.TrimSpace(agentID)
				if !a.callerOwnsEnabledAgent(r, principal, requested.AgentID, requested, "scoped_agent_not_owned_or_enabled") {
					writeAPIError(w, http.StatusForbidden, "forbidden")
					return
				}
			}
		}
	}
	key, ok := tokenIdempotencyKey(w, r)
	if !ok {
		return
	}
	created, err := a.tokenService.Create(r.Context(), principal.UserID, key, auth.CreateTokenInput{
		Type: request.Type, OwnerUserID: request.OwnerUserID, AgentID: request.AgentID, NodeID: request.NodeID,
		Scope: request.Scope, ExpiresAt: request.ExpiresAt,
	})
	if err != nil {
		writeTokenError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// callerOwnsEnabledAgent performs the non-admin resource boundary check.
func (a *API) callerOwnsEnabledAgent(r *http.Request, principal auth.Principal, agentID string, requested storage.ServiceToken, reason string) bool {
	agent, err := a.tokenService.GetAgent(r.Context(), strings.TrimSpace(agentID))
	if err == nil && agent.Enabled && agent.OwnerUserID == principal.UserID {
		return true
	}
	a.denyToken(r, principal, requested, reason)
	return false
}

func (a *API) denyToken(r *http.Request, principal auth.Principal, record storage.ServiceToken, reason string) {
	if err := a.tokenService.RecordAuthorizationDenied(r.Context(), principal.UserID, record, reason); err != nil {
		resourceID := record.ID
		if resourceID == "" {
			if record.AgentID != "" {
				resourceID = record.AgentID
			} else {
				resourceID = record.NodeID
			}
		}
		slog.ErrorContext(r.Context(), "token_authorization_denied_audit_failed",
			"action", "token.authorization_denied",
			"actor_user_id", principal.UserID,
			"resource_type", "service_token",
			"resource_id", resourceID,
			"reason_code", reason,
		)
	}
}

// tokenIdempotencyKey enforces the storage contract's bounded key size.
func tokenIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > 255 {
		writeAPIError(w, http.StatusBadRequest, "Idempotency-Key is too long")
		return "", false
	}
	return key, true
}

func validManagementTokenType(tokenType storage.TokenType) bool {
	return tokenType == storage.TokenTypeAgent || tokenType == storage.TokenTypeClient || tokenType == storage.TokenTypeServerNode
}

func writeTokenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeAPIError(w, http.StatusNotFound, "token not found")
	case errors.Is(err, errInvalidTokenRequest):
		writeAPIError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errTokenConflict), errors.Is(err, errIdempotencyConflict), errors.Is(err, errIdempotencyInProgress), errors.Is(err, storage.ErrServiceTokenRevoked):
		writeAPIError(w, http.StatusConflict, err.Error())
	default:
		writeStorageError(w, err)
	}
}
