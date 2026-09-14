package server

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

func (a *API) changeOwnPassword(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	if r.Method != http.MethodPut {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(r, &request); err != nil || request.CurrentPassword == "" || request.NewPassword == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON or missing password")
		return
	}
	user, err := a.accounts.ChangeOwnPassword(r.Context(), principal.UserID, request.CurrentPassword, request.NewPassword)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicUser(user))
}

func (a *API) handleUsers(w http.ResponseWriter, r *http.Request, principal auth.Principal, parts []string) {
	if !isAdmin(principal) {
		writeAPIError(w, http.StatusForbidden, "admin role required")
		return
	}
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			a.listUsers(w, r)
		case http.MethodPost:
			a.createUser(w, r, principal)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	userID := parts[0]
	if userID == "" {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "reset-password":
			if r.Method == http.MethodPost {
				a.resetUserPassword(w, r, principal, userID)
				return
			}
		case "restore":
			if r.Method == http.MethodPost {
				a.restoreUser(w, r, principal, userID)
				return
			}
		}
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if len(parts) != 1 {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		a.updateUserStatus(w, r, principal, userID)
	case http.MethodDelete:
		user, err := a.accounts.DeleteChild(r.Context(), principal.UserID, userID)
		if err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, publicUser(user))
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	page, err := a.accounts.ListChildren(r.Context(), r.URL.Query().Get("status"), r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		writeAccountError(w, err)
		return
	}
	items := make([]any, 0, len(page.Items))
	for _, user := range page.Items {
		items = append(items, publicUser(user))
	}
	writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	var request struct {
		Username string `json:"username"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	user, temporaryPassword, err := a.accounts.CreateChild(r.Context(), principal.UserID, request.Username)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"user": publicUser(user), "temporaryPassword": temporaryPassword})
}

func (a *API) updateUserStatus(w http.ResponseWriter, r *http.Request, principal auth.Principal, userID string) {
	var request struct {
		Disabled *bool `json:"disabled"`
	}
	if err := decodeJSON(r, &request); err != nil || request.Disabled == nil {
		writeAPIError(w, http.StatusBadRequest, "disabled is required")
		return
	}
	user, err := a.accounts.SetDisabled(r.Context(), principal.UserID, userID, *request.Disabled)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicUser(user))
}

func (a *API) resetUserPassword(w http.ResponseWriter, r *http.Request, principal auth.Principal, userID string) {
	user, temporaryPassword, err := a.accounts.ResetPassword(r.Context(), principal.UserID, userID)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(user), "temporaryPassword": temporaryPassword})
}

func (a *API) restoreUser(w http.ResponseWriter, r *http.Request, principal auth.Principal, userID string) {
	user, err := a.accounts.RestoreChild(r.Context(), principal.UserID, userID)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicUser(user))
}

func writeAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUsernameInvalid), errors.Is(err, auth.ErrPasswordPolicyViolation), errors.Is(err, auth.ErrAccountStatusInvalid):
		writeAPIError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrCurrentPasswordInvalid), errors.Is(err, auth.ErrAdminAccountProtected):
		writeAPIError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, sql.ErrNoRows):
		writeAPIError(w, http.StatusNotFound, "not found")
	case errors.Is(err, auth.ErrUsernameConflict), errors.Is(err, auth.ErrAccountDeleted):
		writeAPIError(w, http.StatusConflict, err.Error())
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal server error")
	}
}

func (a *API) handleDashboard(w http.ResponseWriter, r *http.Request, principal auth.Principal, parts []string) {
	if r.Method != http.MethodGet || len(parts) != 1 || parts[0] != "summary" {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	ownerUserID := principal.UserID
	if isAdmin(principal) {
		ownerUserID = ""
	}
	summary, err := a.dashboard.Summary(r.Context(), ownerUserID, 10)
	if err != nil {
		// The summary aggregates several tables; without this log a schema or
		// scan regression surfaces only as an opaque 500 in the console.
		slog.ErrorContext(r.Context(), "dashboard_summary_failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	recentEvents := make([]any, 0, len(summary.RecentEvents))
	for _, event := range summary.RecentEvents {
		recentEvents = append(recentEvents, map[string]any{
			"id": event.ID, "action": event.Action, "resourceType": event.ResourceType,
			"resourceId": event.ResourceID, "createdAt": event.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agentsTotal": summary.AgentsTotal, "agentsOnline": summary.AgentsOnline,
		"activeTunnels": summary.ActiveTunnels, "managedRoutes": summary.ManagedRoutes,
		"validServiceTokens": summary.ValidServiceTokens, "recentEvents": recentEvents,
	})
}
