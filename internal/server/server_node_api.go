package server

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

func (a *API) handleServerNodes(w http.ResponseWriter, r *http.Request, principal auth.Principal, parts []string) {
	if !isAdmin(principal) {
		writeAPIError(w, http.StatusForbidden, "admin role required")
		return
	}
	if a.serverNodes == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "server node service unavailable")
		return
	}
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		page, err := a.serverNodes.List(r.Context(), r.URL.Query().Get("cursor"), queryLimit(r))
		if err != nil {
			writeServerNodeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	id := parts[0]
	if id == "" {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	if len(parts) == 2 && parts[1] == "restore" {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		view, err := a.serverNodes.Restore(r.Context(), id, principal)
		if err != nil {
			writeServerNodeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	if len(parts) != 1 {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		view, err := a.serverNodes.Get(r.Context(), id)
		if err != nil {
			writeServerNodeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	case http.MethodPatch:
		var request struct {
			Name    *string `json:"name"`
			Enabled *bool   `json:"enabled"`
		}
		if err := decodeJSON(r, &request); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		view, err := a.serverNodes.Update(r.Context(), id, UpdateServerNodeInput{Name: request.Name, Enabled: request.Enabled}, principal)
		if err != nil {
			writeServerNodeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	case http.MethodDelete:
		view, err := a.serverNodes.Delete(r.Context(), id, principal)
		if err != nil {
			writeServerNodeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func writeServerNodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrInvalidServerNodeInput) {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	writeStorageError(w, err)
}
