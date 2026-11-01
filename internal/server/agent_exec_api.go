package server

import (
	"net/http"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// SetAgentExec attaches the script-run broker and the server-side switch. A nil broker
// or a disabled switch makes the endpoint refuse, rather than silently doing nothing.
func (a *API) SetAgentExec(broker *agentExecBroker, enabled bool) {
	if a == nil {
		return
	}
	a.agentExec = broker
	a.agentExecEnabled = enabled
}

// handleAgentExec issues a one-time ticket for a script run. Only administrators may
// request one, and the agent must be enabled, online, and currently advertising
// agent_exec.v1 on a live session. The capability is checked against the live session,
// not the administrator-entered capabilities field, because only the live handshake says
// what the agent will actually do.
func (a *API) handleAgentExec(w http.ResponseWriter, r *http.Request, p auth.Principal, agentID string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !isAdmin(p) {
		writeAPIError(w, http.StatusForbidden, "admin_required")
		return
	}
	if a.agentExec == nil || !a.agentExecEnabled {
		writeAPIError(w, http.StatusServiceUnavailable, "script_execution_disabled")
		return
	}
	agent, err := a.service.GetAgent(r.Context(), agentID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if !agent.Enabled {
		writeAPIError(w, http.StatusConflict, "agent_disabled")
		return
	}
	if !liveSessionsSupportExec(a.agentSessions, agentID) {
		if a.agentSessions == nil || len(a.agentSessions.List(agentID)) == 0 {
			writeAPIError(w, http.StatusConflict, ErrAgentExecOffline.Error())
			return
		}
		writeAPIError(w, http.StatusConflict, ErrAgentExecUnsupported.Error())
		return
	}
	issued, err := a.agentExec.tickets.issue(agentID, p.UserID, agentExecTicketTTL)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "ticket_unavailable")
		return
	}
	if a.DB != nil {
		_ = a.DB.Audits().Create(r.Context(), storage.AuditLog{
			ActorUserID: p.UserID, Action: "agent.exec.session.created", ResourceType: "agent",
			ResourceID: agentID, Details: auditDetailsJSON(r.Context(), map[string]any{"expires_at": issued.ExpiresAt.Format(time.RFC3339)}),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessionId":     issued.SessionID,
		"ticket":        issued.Ticket,
		"websocketPath": agentExecPath + issued.SessionID,
		"expiresAt":     issued.ExpiresAt,
	})
}

// liveSessionsSupportExec reports whether any live connection for the agent negotiated
// the exec capability. The check is by capability, never by name or version.
func liveSessionsSupportExec(sessions *AgentSessionManager, agentID string) bool {
	if sessions == nil {
		return false
	}
	for _, session := range sessions.List(agentID) {
		if session.Supports(protocol.CapabilityAgentExec) {
			return true
		}
	}
	return false
}
