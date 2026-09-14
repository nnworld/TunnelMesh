package server

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type credentialRequest struct {
	Name      string                 `json:"name"`
	Type      storage.CredentialType `json:"type"`
	PublicKey string                 `json:"publicKey"`
	// Username is only meaningful for proxy_basic credentials. The password
	// still arrives through Secret and is never rendered back.
	Username string                   `json:"username"`
	Enabled  *bool                    `json:"enabled"`
	Secret   *credentialSecretRequest `json:"secret"`
}

// credentialSecretRequest carries write-only material. It is accepted on create
// and update, sealed by the service, and never rendered back to the caller.
type credentialSecretRequest struct {
	Password   string `json:"password"`
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase"`
}

func (r *credentialSecretRequest) toSecret() *CredentialSecret {
	if r == nil {
		return nil
	}
	return &CredentialSecret{Password: r.Password, PrivateKey: r.PrivateKey, Passphrase: r.Passphrase}
}

type extractSSHPublicKeyRequest struct {
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase"`
}

type extractedSSHPublicKeyResponse struct {
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
}

type credentialResponse struct {
	ID          string                 `json:"id"`
	OwnerUserID string                 `json:"ownerUserId"`
	Name        string                 `json:"name"`
	Type        storage.CredentialType `json:"type"`
	PublicKey   string                 `json:"publicKey"`
	Fingerprint string                 `json:"fingerprint"`
	// Username mirrors public_key for proxy_basic rows so the frontend does not
	// have to know that the two share a column.
	Username string `json:"username,omitempty"`
	Enabled  bool   `json:"enabled"`
	// HasSecret tells the UI whether auto-authentication is possible without
	// exposing the ciphertext, nonce, key id or plaintext.
	HasSecret bool             `json:"hasSecret"`
	Status    CredentialStatus `json:"status"`
	DeletedAt *string          `json:"deletedAt"`
	CreatedAt string           `json:"createdAt"`
	UpdatedAt string           `json:"updatedAt"`
}

type CredentialStatus = storage.CredentialStatus

func publicCredential(v storage.Credential) credentialResponse {
	response := credentialResponse{
		ID: v.ID, OwnerUserID: v.OwnerUserID, Name: v.Name, Type: v.Type, PublicKey: v.PublicKey,
		Fingerprint: v.Fingerprint, Enabled: v.Enabled, HasSecret: v.HasSecret(), Status: credentialStatus(v.DeletedAt),
		DeletedAt: timeString(v.DeletedAt), CreatedAt: tmString(v.CreatedAt), UpdatedAt: tmString(v.UpdatedAt),
	}
	if v.Type == storage.CredentialTypeProxyBasic {
		response.Username = v.PublicKey
	}
	return response
}

func credentialStatus(deleted *time.Time) CredentialStatus {
	if deleted == nil {
		return storage.CredentialStatusActive
	}
	return storage.CredentialStatusDeleted
}

func tmString(v time.Time) string {
	if v.IsZero() {
		return ""
	}
	return v.UTC().Format(time.RFC3339Nano)
}

func timeString(v *time.Time) *string {
	if v == nil {
		return nil
	}
	value := v.UTC().Format(time.RFC3339Nano)
	return &value
}

func (a *API) handleCredentials(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	switch {
	case len(parts) == 0:
		a.handleCredentialListCreate(w, r, p)
	case len(parts) == 1 && parts[0] == "extract-ssh-public-key":
		a.handleExtractSSHPublicKey(w, r, p)
	case len(parts) == 1:
		a.handleCredentialItem(w, r, p, parts[0])
	case len(parts) == 2 && parts[1] == "restore":
		a.handleCredentialRestore(w, r, p, parts[0])
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

func (a *API) handleExtractSSHPublicKey(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	// Private-key material must never be cached by an intermediary or browser.
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req extractSSHPublicKeyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	extracted, err := a.credentialService.ExtractSSHPublicKey(r.Context(), p, req.PrivateKey, req.Passphrase)
	if err != nil {
		writeCredentialError(w, err)
		return
	}
	req.PrivateKey = ""
	req.Passphrase = ""
	writeJSON(w, http.StatusOK, extractedSSHPublicKeyResponse{
		PublicKey: extracted.PublicKey, Fingerprint: extracted.Fingerprint,
	})
}

func (a *API) handleCredentialListCreate(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	switch r.Method {
	case http.MethodGet:
		filter := storage.CredentialFilter{Keyword: r.URL.Query().Get("keyword")}
		if raw := r.URL.Query().Get("type"); raw != "" {
			filter.Type = storage.CredentialType(raw)
		}
		if raw := r.URL.Query().Get("status"); raw != "" {
			filter.Status = storage.CredentialStatus(raw)
		}
		page, err := a.credentialService.List(r.Context(), p, filter, r.URL.Query().Get("cursor"), queryLimit(r))
		if err != nil {
			writeCredentialError(w, err)
			return
		}
		items := make([]any, len(page.Items))
		for i := range page.Items {
			items[i] = publicCredential(page.Items[i])
		}
		writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
	case http.MethodPost:
		var req credentialRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		input := CredentialInput{Name: req.Name, Type: req.Type, PublicKey: req.PublicKey, Username: req.Username, Enabled: req.Enabled == nil || *req.Enabled}
		input.Secret = req.Secret.toSecret()
		defer req.Secret.clear()
		status, data, err := a.mutate(r, p, func() (int, any, error) {
			created, err := a.credentialService.Create(r.Context(), p, input)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusCreated, publicCredential(created), nil
		})
		if err != nil {
			writeCredentialError(w, err)
			return
		}
		writeStored(w, status, data)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleCredentialItem(w http.ResponseWriter, r *http.Request, p auth.Principal, id string) {
	switch r.Method {
	case http.MethodGet:
		credential, err := a.credentialService.Get(r.Context(), p, id)
		if err != nil {
			writeCredentialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, publicCredential(credential))
	case http.MethodPut:
		var req credentialRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		enabled := req.Enabled == nil || *req.Enabled
		patch := CredentialPatch{Name: &req.Name, Type: &req.Type, PublicKey: &req.PublicKey, Enabled: &enabled}
		if req.Username != "" {
			// A blank username is never a valid PUT value, so it is treated as
			// "field absent" instead of "clear the username".
			patch.Username = &req.Username
		}
		patch.Secret = req.Secret.toSecret()
		defer req.Secret.clear()
		status, data, err := a.mutate(r, p, func() (int, any, error) {
			updated, err := a.credentialService.Update(r.Context(), p, id, patch)
			if err != nil {
				return 0, nil, err
			}
			return http.StatusOK, publicCredential(updated), nil
		})
		if err != nil {
			writeCredentialError(w, err)
			return
		}
		writeStored(w, status, data)
	case http.MethodPatch:
		var req credentialRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if _, err := a.credentialService.Update(r.Context(), p, id, credentialPatchFromRequest(req)); err != nil {
			writeCredentialError(w, err)
			return
		}
		updated, err := a.credentialService.Get(r.Context(), p, id)
		if err != nil {
			writeCredentialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, publicCredential(updated))
	case http.MethodDelete:
		if err := a.credentialService.Delete(r.Context(), p, id); err != nil {
			writeCredentialError(w, err)
			return
		}
		deleted, err := a.credentialService.Get(r.Context(), p, id)
		if err != nil {
			writeCredentialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, publicCredential(deleted))
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleCredentialRestore(w http.ResponseWriter, r *http.Request, p auth.Principal, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	restored, err := a.credentialService.Restore(r.Context(), p, id)
	if err != nil {
		writeCredentialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicCredential(restored))
}

func credentialPatchFromRequest(req credentialRequest) CredentialPatch {
	patch := CredentialPatch{}
	if req.Name != "" {
		patch.Name = &req.Name
	}
	if req.Type != "" {
		patch.Type = &req.Type
	}
	if req.PublicKey != "" {
		patch.PublicKey = &req.PublicKey
	}
	if req.Username != "" {
		patch.Username = &req.Username
	}
	if req.Enabled != nil {
		patch.Enabled = req.Enabled
	}
	if secret := req.Secret.toSecret(); secret != nil && !secret.isEmpty() {
		patch.Secret = secret
	}
	return patch
}

// clear wipes the plaintext held by the decoded request as soon as it has been
// sealed, so a later heap dump or debug print cannot recover it.
func (r *credentialSecretRequest) clear() {
	if r == nil {
		return
	}
	r.Password, r.PrivateKey, r.Passphrase = "", "", ""
}

// credentialError maps a credential-service error onto the API envelope. ok is
// false when the error belongs to the storage layer, so the driver text and the
// unique-constraint -> 409 mapping stay in writeStorageError alone.
func credentialError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, auth.ErrForbidden), errors.Is(err, ErrResourceForbidden):
		return http.StatusForbidden, "forbidden", true
	case errors.Is(err, ErrCredentialInvalid):
		return http.StatusBadRequest, err.Error(), true
	case errors.Is(err, ErrCredentialSecretUnavailable):
		return http.StatusServiceUnavailable, err.Error(), true
	case errors.Is(err, sql.ErrNoRows):
		return http.StatusNotFound, "not found", true
	default:
		return 0, "", false
	}
}

// credentialErrorStatus exposes just the status for callers that render their
// own message. Route validation needs it: an unknown and an unauthorized
// credential ID must answer identically so IDs cannot be enumerated through
// /api/v1/routes.
func credentialErrorStatus(err error) int {
	if status, _, ok := credentialError(err); ok {
		return status
	}
	return http.StatusInternalServerError
}

func writeCredentialError(w http.ResponseWriter, err error) {
	if status, message, ok := credentialError(err); ok {
		writeAPIError(w, status, message)
		return
	}
	writeStorageError(w, err)
}
