package server

import (
	"errors"
	"net/http"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type createWebSSHSessionRequest struct {
	Username     string `json:"username"`
	CredentialID string `json:"credentialId"`
}

type websshSessionResponse struct {
	ID             string                      `json:"id"`
	RemoteServerID string                      `json:"remoteServerId"`
	AgentID        string                      `json:"agentId"`
	Status         storage.WebSSHSessionStatus `json:"status"`
	CreatedAt      string                      `json:"createdAt"`
	ExpiresAt      string                      `json:"expiresAt"`
	ConnectedAt    *string                     `json:"connectedAt"`
	ClosedAt       *string                     `json:"closedAt"`
	CloseReason    string                      `json:"closeReason"`
}

type websshTicketResponse struct {
	SessionID     string `json:"sessionId"`
	Ticket        string `json:"ticket"`
	WebSocketPath string `json:"websocketPath"`
	// Auth is attached after the idempotency record has been written, so the
	// one-time secret is never persisted and a replay returns no auth material.
	Auth *WebSSHAuthPayload `json:"auth,omitempty"`
}

func publicWebSSHSession(v storage.WebSSHSession) websshSessionResponse {
	return websshSessionResponse{
		ID: v.ID, RemoteServerID: v.RemoteServerID, AgentID: v.AgentID, Status: v.Status,
		CreatedAt: tmString(v.CreatedAt), ExpiresAt: tmString(v.ExpiresAt),
		ConnectedAt: timeString(v.ConnectedAt), ClosedAt: timeString(v.ClosedAt), CloseReason: v.CloseReason,
	}
}

func (a *API) createWebSSHSession(w http.ResponseWriter, r *http.Request, p auth.Principal, serverID string) {
	var req createWebSSHSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	var response websshTicketResponse
	var authPayload *WebSSHAuthPayload
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		ticket, err := a.websshService.Create(r.Context(), p, serverID, CreateWebSSHSessionInput{
			Username: req.Username, CredentialID: req.CredentialID,
		})
		if err != nil {
			return 0, nil, err
		}
		response = websshTicketResponse{
			SessionID: ticket.SessionID, Ticket: ticket.Ticket, WebSocketPath: ticket.WebSocketPath,
		}
		authPayload = ticket.Auth
		return http.StatusCreated, response, nil
	})
	if err != nil {
		writeWebSSHError(w, err)
		return
	}
	// Deliver the plaintext exactly once, outside the stored response body.
	if authPayload != nil {
		response.Auth = authPayload
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, status, response)
		return
	}
	writeStored(w, status, data)
}

// listWebSSHSessions returns the caller's own active sessions so the console can
// offer a self-service disconnect when the per-user quota is exhausted. The
// owner scope is enforced in the service, never from a request parameter.
func (a *API) listWebSSHSessions(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	page, err := a.websshService.List(r.Context(), p, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		writeWebSSHError(w, err)
		return
	}
	items := make([]any, len(page.Items))
	for i := range page.Items {
		items[i] = publicWebSSHSession(page.Items[i])
	}
	writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
}

func (a *API) getWebSSHSession(w http.ResponseWriter, r *http.Request, p auth.Principal, sessionID string) {
	session, err := a.websshService.Get(r.Context(), p, sessionID)
	if err != nil {
		writeWebSSHError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicWebSSHSession(session))
}

func (a *API) closeWebSSHSession(w http.ResponseWriter, r *http.Request, p auth.Principal, sessionID string) {
	if a.websshCloser != nil {
		if _, err := a.websshService.Get(r.Context(), p, sessionID); err != nil {
			writeWebSSHError(w, err)
			return
		}
		if err := a.websshCloser.Close(r.Context(), sessionID); err != nil {
			writeWebSSHError(w, err)
			return
		}
	}
	if err := a.websshService.Close(r.Context(), p, sessionID, "user_closed"); err != nil {
		writeWebSSHError(w, err)
		return
	}
	session, err := a.websshService.Get(r.Context(), p, sessionID)
	if err != nil {
		writeWebSSHError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicWebSSHSession(session))
}

func writeWebSSHError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrWebSSHConnectionNodeUnavailable) {
		writeAPIError(w, http.StatusServiceUnavailable, "webssh connection owner node unavailable")
		return
	}
	// Cross-owner access must answer 403 like the remote-server and credential
	// APIs; a 500 hides the real reason from the admin console.
	if errors.Is(err, ErrResourceForbidden) || errors.Is(err, auth.ErrForbidden) {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	// openapi.yaml documents 409 for the active-session limit.
	if errors.Is(err, ErrWebSSHActiveSessionLimit) {
		writeAPIError(w, http.StatusConflict, ErrWebSSHActiveSessionLimit.Error())
		return
	}
	writeStorageError(w, err)
}
