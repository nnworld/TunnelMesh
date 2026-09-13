package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type remoteServerRequest struct {
	Name            string `json:"name"`
	Host            string `json:"host"`
	Port            *int   `json:"port"`
	DefaultUsername string `json:"defaultUsername"`
	CredentialID    string `json:"credentialId"`
	AgentID         string `json:"agentId"`
	Enabled         *bool  `json:"enabled"`
}

type remoteServerResponse struct {
	ID              string  `json:"id"`
	OwnerUserID     string  `json:"ownerUserId"`
	Name            string  `json:"name"`
	Host            string  `json:"host"`
	Port            int     `json:"port"`
	DefaultUsername string  `json:"defaultUsername"`
	CredentialID    string  `json:"credentialId"`
	AgentID         string  `json:"agentId"`
	AgentName       *string `json:"agentName,omitempty"`
	CredentialName  *string `json:"credentialName,omitempty"`
	AgentOnline     *bool   `json:"agentOnline,omitempty"`
	// Capability flags only: the credential secret is never part of this payload.
	CredentialType      *string `json:"credentialType"`
	CredentialHasSecret *bool   `json:"credentialHasSecret"`
	Enabled             bool    `json:"enabled"`
	Status              string  `json:"status"`
	LastConnectedAt     *string `json:"lastConnectedAt"`
	LastResult          string  `json:"lastResult"`
	LastErrorClass      string  `json:"lastErrorClass"`
	DeletedAt           *string `json:"deletedAt"`
	CreatedAt           string  `json:"createdAt"`
	UpdatedAt           string  `json:"updatedAt"`
}

func publicRemoteServer(v storage.RemoteServer, display RemoteServerDisplay) remoteServerResponse {
	status := "enabled"
	switch {
	case v.DeletedAt != nil:
		status = "deleted"
	case !v.Enabled:
		status = "disabled"
	}
	return remoteServerResponse{
		ID: v.ID, OwnerUserID: v.OwnerUserID, Name: v.Name, Host: v.Host, Port: v.Port,
		DefaultUsername: v.DefaultUsername, CredentialID: v.CredentialID, AgentID: v.AgentID,
		Enabled: v.Enabled, Status: status, LastConnectedAt: timeString(v.LastConnectedAt),
		LastResult: v.LastResult, LastErrorClass: v.LastErrorClass, DeletedAt: timeString(v.DeletedAt),
		CreatedAt: tmString(v.CreatedAt), UpdatedAt: tmString(v.UpdatedAt),
		AgentName: display.AgentName, CredentialName: display.CredentialName, AgentOnline: display.AgentOnline,
		CredentialType: display.CredentialType, CredentialHasSecret: display.CredentialHasSecret,
	}
}

func (a *API) publicRemoteServer(ctx context.Context, server storage.RemoteServer) (remoteServerResponse, error) {
	display, err := a.remoteServerService.Display(ctx, server)
	if err != nil {
		return remoteServerResponse{}, err
	}
	return publicRemoteServer(server, display), nil
}

func (a *API) handleRemoteServers(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	switch {
	case len(parts) == 0:
		a.handleRemoteServerListCreate(w, r, p)
	case len(parts) == 2 && parts[1] == "ssh-sessions" && r.Method == http.MethodPost:
		a.createWebSSHSession(w, r, p, parts[0])
	case len(parts) == 1:
		a.handleRemoteServerItem(w, r, p, parts[0])
	case len(parts) == 2 && parts[1] == "restore":
		a.handleRemoteServerRestore(w, r, p, parts[0])
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

func (a *API) handleRemoteServerListCreate(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	switch r.Method {
	case http.MethodGet:
		filter := storage.RemoteServerFilter{
			AgentID: r.URL.Query().Get("agentId"), Keyword: r.URL.Query().Get("keyword"),
		}
		if raw := r.URL.Query().Get("status"); raw != "" {
			filter.Status = storage.RemoteServerStatus(raw)
		}
		page, err := a.remoteServerService.List(r.Context(), p, filter, r.URL.Query().Get("cursor"), queryLimit(r))
		if err != nil {
			writeRemoteServerError(w, err)
			return
		}
		items := make([]any, len(page.Items))
		for i := range page.Items {
			item, err := a.publicRemoteServer(r.Context(), page.Items[i])
			if err != nil {
				writeRemoteServerError(w, err)
				return
			}
			items[i] = item
		}
		writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
	case http.MethodPost:
		var req remoteServerRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		input, err := remoteServerInput(req)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		status, data, err := a.mutate(r, p, func() (int, any, error) {
			created, err := a.remoteServerService.Create(r.Context(), p, input)
			if err != nil {
				return 0, nil, err
			}
			response, err := a.publicRemoteServer(r.Context(), created)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusCreated, response, nil
		})
		if err != nil {
			writeRemoteServerError(w, err)
			return
		}
		writeStored(w, status, data)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleRemoteServerItem(w http.ResponseWriter, r *http.Request, p auth.Principal, id string) {
	switch r.Method {
	case http.MethodGet:
		server, err := a.remoteServerService.Get(r.Context(), p, id)
		if err != nil {
			writeRemoteServerError(w, err)
			return
		}
		response, err := a.publicRemoteServer(r.Context(), server)
		if err != nil {
			writeRemoteServerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPut:
		var req remoteServerRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		input, err := remoteServerInput(req)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		status, data, err := a.mutate(r, p, func() (int, any, error) {
			updated, err := a.remoteServerService.Update(r.Context(), p, id, remoteServerFullPatch(input))
			if err != nil {
				return 0, nil, err
			}
			response, err := a.publicRemoteServer(r.Context(), updated)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, response, nil
		})
		if err != nil {
			writeRemoteServerError(w, err)
			return
		}
		writeStored(w, status, data)
	case http.MethodPatch:
		var req remoteServerRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if _, err := a.remoteServerService.Update(r.Context(), p, id, remoteServerPatchFromRequest(req)); err != nil {
			writeRemoteServerError(w, err)
			return
		}
		updated, err := a.remoteServerService.Get(r.Context(), p, id)
		if err != nil {
			writeRemoteServerError(w, err)
			return
		}
		response, err := a.publicRemoteServer(r.Context(), updated)
		if err != nil {
			writeRemoteServerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodDelete:
		if err := a.remoteServerService.Delete(r.Context(), p, id); err != nil {
			writeRemoteServerError(w, err)
			return
		}
		deleted, err := a.remoteServerService.Get(r.Context(), p, id)
		if err != nil {
			writeRemoteServerError(w, err)
			return
		}
		response, err := a.publicRemoteServer(r.Context(), deleted)
		if err != nil {
			writeRemoteServerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleRemoteServerRestore(w http.ResponseWriter, r *http.Request, p auth.Principal, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	restored, err := a.remoteServerService.Restore(r.Context(), p, id)
	if err != nil {
		writeRemoteServerError(w, err)
		return
	}
	response, err := a.publicRemoteServer(r.Context(), restored)
	if err != nil {
		writeRemoteServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func remoteServerInput(req remoteServerRequest) (RemoteServerInput, error) {
	input := RemoteServerInput{
		Name: req.Name, Host: req.Host, DefaultUsername: req.DefaultUsername,
		CredentialID: req.CredentialID, AgentID: req.AgentID, Enabled: req.Enabled == nil || *req.Enabled,
	}
	if req.Port != nil {
		input.Port = *req.Port
	}
	if input.Name == "" || input.Host == "" || input.DefaultUsername == "" || input.AgentID == "" || req.Port == nil {
		return input, errors.New("name, host, port, username and agent are required")
	}
	return input, nil
}

func remoteServerFullPatch(input RemoteServerInput) RemoteServerPatch {
	return RemoteServerPatch{
		Name: &input.Name, Host: &input.Host, Port: &input.Port, DefaultUsername: &input.DefaultUsername,
		CredentialID: &input.CredentialID, AgentID: &input.AgentID, Enabled: &input.Enabled,
	}
}

func remoteServerPatchFromRequest(req remoteServerRequest) RemoteServerPatch {
	patch := RemoteServerPatch{}
	if req.Name != "" {
		patch.Name = &req.Name
	}
	if req.Host != "" {
		patch.Host = &req.Host
	}
	if req.Port != nil {
		patch.Port = req.Port
	}
	if req.DefaultUsername != "" {
		patch.DefaultUsername = &req.DefaultUsername
	}
	if req.CredentialID != "" {
		patch.CredentialID = &req.CredentialID
	}
	if req.AgentID != "" {
		patch.AgentID = &req.AgentID
	}
	if req.Enabled != nil {
		patch.Enabled = req.Enabled
	}
	return patch
}

func writeRemoteServerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrResourceForbidden), errors.Is(err, auth.ErrForbidden):
		writeAPIError(w, http.StatusForbidden, "forbidden")
	default:
		writeStorageError(w, err)
	}
}
