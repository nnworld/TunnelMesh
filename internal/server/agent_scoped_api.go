package server

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const agentScopedPrefix = "/api/v1/agent/"

// handleAgentScoped serves the small identity surface used by a standalone Agent
// when its local config omits agent.id. It deliberately accepts only Agent tokens;
// client tokens use the separate WebSocket auto-registration path.
func (a *API) handleAgentScoped(w http.ResponseWriter, r *http.Request, path string) bool {
	if !strings.HasPrefix(path, agentScopedPrefix) {
		return false
	}
	if a.agentTokens == nil || a.service == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "agent_token_validator_unavailable")
		return true
	}
	identity, err := a.agentTokens.ValidateAs(r.Context(), bearerToken(r), storage.TokenTypeAgent)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "unauthenticated")
		return true
	}
	rest := strings.Trim(strings.TrimPrefix(path, agentScopedPrefix), "/")
	if strings.HasPrefix(rest, "updates/") {
		a.serveUpdates(w, r, identity, strings.TrimPrefix(rest, "updates/"))
		return true
	}
	if r.Method != http.MethodGet || rest != "self" {
		writeAPIError(w, http.StatusNotFound, "not found")
		return true
	}
	if strings.TrimSpace(identity.AgentID) == "" {
		writeAPIError(w, http.StatusForbidden, "agent identity is not bound")
		return true
	}
	agent, err := a.service.GetAgent(context.WithoutCancel(r.Context()), identity.AgentID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeAPIError(w, http.StatusUnauthorized, "unauthenticated")
		} else {
			writeStorageError(w, err)
		}
		return true
	}
	if !agent.Enabled || agent.OwnerUserID != identity.OwnerUserID {
		// The response stays generic so a token holder cannot probe other owners'
		// Agents; the reason is logged for the operator.
		slog.Warn("agent self lookup refused", "agent_id", agent.ID, "enabled", agent.Enabled, "owner_match", agent.OwnerUserID == identity.OwnerUserID)
		writeAPIError(w, http.StatusForbidden, "agent is unavailable")
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": agent.ID, "name": agent.Name})
	return true
}
