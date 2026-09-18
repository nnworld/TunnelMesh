package server

import (
	"net/http"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

// The handlers in this file serve the administrator view of another account's
// identity: its trusted devices, its second factor, and its external identity
// links. handleUsers has already established that the caller holds the admin
// role, so every call below passes the actor as an administrator and the service
// layer still re-checks ownership where a normal user could reach the same code.
//
// The user id always comes from the path, never from a request body, so an
// administrator cannot be tricked into acting on an account the URL does not name.

// handleUserDevices serves GET /users/{id}/devices and
// DELETE /users/{id}/devices/{deviceId}.
func (a *API) handleUserDevices(w http.ResponseWriter, r *http.Request, p auth.Principal, userID string, parts []string) {
	if !a.identity.devicesReady() {
		writeAPIError(w, http.StatusServiceUnavailable, "identity_services_unavailable")
		return
	}
	ctx := r.Context()
	switch len(parts) {
	case 0:
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// An administrator is not the subject of the request, so no device is
		// marked current: the list describes somebody else's browsers.
		views, err := a.identity.Devices.List(ctx, userID, "")
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, devicePage(views))
	case 1:
		if r.Method != http.MethodDelete {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.revokeUserDevice(w, r, p, userID, parts[0])
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

// revokeUserDevice untrusts one of another account's devices. It is the
// incident-response action for a lost laptop, so it is idempotent and audited.
func (a *API) revokeUserDevice(w http.ResponseWriter, r *http.Request, p auth.Principal, userID, deviceID string) {
	if _, ok := requireIdempotencyKey(w, r); !ok {
		return
	}
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		if err := a.identity.Devices.Revoke(r.Context(), p.UserID, userID, deviceID, true); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"revoked": true, "id": deviceID}, nil
	})
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeStored(w, status, data)
}

// handleUserMFA serves GET /users/{id}/mfa and POST /users/{id}/mfa/reset.
func (a *API) handleUserMFA(w http.ResponseWriter, r *http.Request, p auth.Principal, userID string, parts []string) {
	if !requireIdentity(w, a.identity) {
		return
	}
	ctx := r.Context()
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		view, err := a.identity.MFA.Status(ctx, userID)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":                 mfaStatusName(view.Status),
			"remainingRecoveryCodes": view.RemainingRecoveryCodes,
		})
		return
	}
	if len(parts) != 1 || parts[0] != "reset" {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a.resetUserMFA(w, r, p, userID)
}

// resetUserMFA clears another account's second factor and every device that was
// trusted under it. It is the recovery path for a user who lost their
// authenticator, and it is deliberately destructive: the account must re-enroll,
// which is the only way to be sure the person re-enrolling is the account owner.
func (a *API) resetUserMFA(w http.ResponseWriter, r *http.Request, p auth.Principal, userID string) {
	if _, ok := requireIdempotencyKey(w, r); !ok {
		return
	}
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		if err := a.identity.MFA.AdminReset(r.Context(), p.UserID, userID); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"reset": true, "userId": userID}, nil
	})
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeStored(w, status, data)
}

// handleUserIdentities serves GET /users/{id}/identities and
// DELETE /users/{id}/identities/{identityId}.
func (a *API) handleUserIdentities(w http.ResponseWriter, r *http.Request, p auth.Principal, userID string, parts []string) {
	if a.identity == nil || a.identity.Identities == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "identity_services_unavailable")
		return
	}
	ctx := r.Context()
	switch len(parts) {
	case 0:
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		views, err := a.identity.Identities.List(ctx, userID)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, identityPage(views))
	case 1:
		if r.Method != http.MethodDelete {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.unlinkUserIdentity(w, r, p, userID, parts[0])
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

// unlinkUserIdentity removes one external login from another account. The
// service refuses to unlink the only credential of a passwordless account, which
// is what stops an administrator from locking a colleague out by accident.
func (a *API) unlinkUserIdentity(w http.ResponseWriter, r *http.Request, p auth.Principal, userID, identityID string) {
	if _, ok := requireIdempotencyKey(w, r); !ok {
		return
	}
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		if err := a.identity.Identities.Unlink(r.Context(), p.UserID, userID, identityID, true); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"unlinked": true, "id": identityID}, nil
	})
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeStored(w, status, data)
}

// devicePage and identityPage wrap a complete list in the cursor envelope the
// rest of the API uses. Both lists are bounded by policy (at most
// max_trusted_devices devices, at most one link per provider), so a single page
// is always complete and no cursor is offered.
func devicePage(views []auth.DeviceView) map[string]any {
	items := make([]any, 0, len(views))
	for _, view := range views {
		items = append(items, view)
	}
	return map[string]any{"items": items, "nextCursor": "", "hasMore": false}
}

func identityPage(views []auth.IdentityView) map[string]any {
	items := make([]any, 0, len(views))
	for _, view := range views {
		items = append(items, view)
	}
	return map[string]any{"items": items, "nextCursor": "", "hasMore": false}
}
