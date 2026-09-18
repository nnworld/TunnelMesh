package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// publicAuthPathPrefixes are the identity routes reachable without a bearer
// token. They are matched before the authentication middleware, and everything
// else under /auth/ still requires a session.
var publicAuthPaths = map[string]bool{
	"/api/v1/auth/login":         true,
	"/api/v1/auth/mfa/verify":    true,
	"/api/v1/auth/oidc/exchange": true,
}

// handlePublicAuth serves the unauthenticated identity endpoints. It reports
// whether it handled the request so ServeHTTP can fall through to the normal
// authenticated dispatch for everything else.
func (a *API) handlePublicAuth(w http.ResponseWriter, r *http.Request, path string) bool {
	if publicAuthPaths[path] {
		switch path {
		case "/api/v1/auth/login":
			if r.Method != http.MethodPost {
				writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
				return true
			}
			a.loginWithMFA(w, r)
			return true
		case "/api/v1/auth/mfa/verify":
			if r.Method != http.MethodPost {
				writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
				return true
			}
			a.verifyLoginMFA(w, r)
			return true
		case "/api/v1/auth/oidc/exchange":
			if r.Method != http.MethodPost {
				writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
				return true
			}
			a.exchangeOIDCTicket(w, r)
			return true
		}
	}
	// The OIDC discovery and redirect endpoints are namespaced rather than
	// enumerated, because the provider name is part of the path.
	if strings.HasPrefix(path, "/api/v1/auth/oidc/") {
		a.handlePublicOIDC(w, r, strings.TrimPrefix(path, "/api/v1/auth/oidc/"))
		return true
	}
	return false
}

// loginWithMFA is the v2 login handler. When MFA is not triggered it returns the
// exact legacy shape, so an existing client or script keeps working unchanged.
func (a *API) loginWithMFA(w http.ResponseWriter, r *http.Request) {
	if !requireIdentity(w, a.identity) {
		return
	}
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		TrustDevice bool   `json:"trustDevice"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	defer clearString(&req.Password)
	outcome, err := a.identity.Logins.Login(r.Context(), auth.LoginRequest{
		Username:    req.Username,
		Password:    req.Password,
		ClientIP:    clientIP(r, a.trustedProxies()),
		UserAgent:   r.UserAgent(),
		TrustDevice: req.TrustDevice,
		DeviceToken: a.deviceCookie(r),
	})
	a.recordLoginMetric("password", outcome, err)
	if err != nil {
		if outcome.State == auth.LoginStateThrottled {
			writeThrottled(w, outcome.RetryAfter)
			return
		}
		writeAuthError(w, err)
		return
	}
	a.writeLoginOutcome(w, r, outcome)
}

// verifyLoginMFA completes a challenge created by a password or OIDC login.
func (a *API) verifyLoginMFA(w http.ResponseWriter, r *http.Request) {
	if !requireIdentity(w, a.identity) {
		return
	}
	var req struct {
		ChallengeID string `json:"challengeId"`
		Code        string `json:"code"`
		TrustDevice bool   `json:"trustDevice"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	defer clearString(&req.Code)
	outcome, err := a.identity.Logins.VerifyMFA(r.Context(), req.ChallengeID, req.Code, clientIP(r, a.trustedProxies()), r.UserAgent(), req.TrustDevice)
	a.recordMFAVerifyMetric(outcome, err)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	a.writeLoginOutcome(w, r, outcome)
}

// writeLoginOutcome renders the two success shapes and sets the trusted-device
// cookie. The cookie is HttpOnly so a script-injection bug in the console cannot
// read the long-lived credential.
func (a *API) writeLoginOutcome(w http.ResponseWriter, r *http.Request, outcome auth.LoginOutcome) {
	if outcome.State == auth.LoginStateMFARequired {
		writeJSON(w, http.StatusOK, map[string]any{
			"mfaRequired": true,
			"challengeId": outcome.ChallengeID,
			"methods":     outcome.Methods,
			"expiresAt":   outcome.ExpiresAt.UTC().Format(time.RFC3339),
		})
		return
	}
	if outcome.DeviceToken != "" {
		a.setDeviceCookie(w, outcome.DeviceToken, outcome.DeviceExpiresAt)
	}
	data := map[string]any{
		"token":         outcome.Token,
		"user":          publicUser(outcome.User),
		"deviceTrusted": outcome.DeviceTrusted,
	}
	if outcome.RecoveryCodesExhausted {
		// Surfacing exhaustion here is the only moment the user knows a recovery
		// login just consumed their last bypass code.
		data["recoveryCodesExhausted"] = true
	}
	writeJSON(w, http.StatusOK, data)
}

func writeThrottled(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(retryAfter / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeAPIError(w, http.StatusTooManyRequests, "login_throttled")
}

// handleAuth routes the authenticated /api/v1/auth/* surface.
func (a *API) handleAuth(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	if len(parts) == 0 {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	switch parts[0] {
	case "me":
		if len(parts) != 1 || r.Method != http.MethodGet {
			writeAPIError(w, http.StatusNotFound, "not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": p.UserID, "username": p.Username, "role": p.Role})
	case "password":
		if len(parts) != 1 {
			writeAPIError(w, http.StatusNotFound, "not found")
			return
		}
		a.changeOwnPassword(w, r, p)
	case "policy":
		if len(parts) != 1 {
			writeAPIError(w, http.StatusNotFound, "not found")
			return
		}
		a.handleAuthPolicy(w, r, p)
	case "mfa":
		a.handleSelfMFA(w, r, p, parts[1:])
	case "devices":
		a.handleSelfDevices(w, r, p, parts[1:])
	case "identities":
		a.handleSelfIdentities(w, r, p, parts[1:])
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

func (a *API) handleSelfMFA(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	if !requireIdentity(w, a.identity) {
		return
	}
	ctx := r.Context()
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			view, err := a.identity.MFA.Status(ctx, p.UserID)
			if err != nil {
				writeAuthError(w, err)
				return
			}
			policy, err := a.effectivePolicy(ctx, p.UserID)
			if err != nil {
				writeAuthError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status":                 mfaStatusName(view.Status),
				"enabledAt":              view.EnabledAt,
				"lastUsedAt":             view.LastUsedAt,
				"remainingRecoveryCodes": view.RemainingRecoveryCodes,
				"policy":                 map[string]any{"mode": string(policy.Mode), "required": policy.Required},
			})
		case http.MethodDelete:
			var req struct {
				Code string `json:"code"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			defer clearString(&req.Code)
			policy, err := a.effectivePolicy(ctx, p.UserID)
			if err != nil {
				writeAuthError(w, err)
				return
			}
			if err := a.identity.MFA.Disable(ctx, p.UserID, req.Code, policy); err != nil {
				writeAuthError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "none"})
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if len(parts) != 1 || r.Method != http.MethodPost {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	switch parts[0] {
	case "enroll":
		var req struct {
			CurrentPassword string `json:"currentPassword"`
		}
		// The body is optional: an SSO-only account has no password to re-prove.
		if r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			defer clearString(&req.CurrentPassword)
			enrollment, err := a.identity.MFA.Enroll(ctx, p.UserID, req.CurrentPassword)
			if err != nil {
				writeAuthError(w, err)
				return
			}
			writeEnrollment(w, enrollment)
			return
		}
		enrollment, err := a.identity.MFA.Enroll(ctx, p.UserID, "")
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeEnrollment(w, enrollment)
	case "enable":
		var req struct {
			Code string `json:"code"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		defer clearString(&req.Code)
		if err := a.identity.MFA.Enable(ctx, p.UserID, req.Code); err != nil {
			writeAuthError(w, err)
			return
		}
		view, err := a.identity.MFA.Status(ctx, p.UserID)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": mfaStatusName(view.Status), "enabledAt": view.EnabledAt})
	case "recovery-codes":
		var req struct {
			Code string `json:"code"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		defer clearString(&req.Code)
		codes, err := a.identity.MFA.RegenerateRecoveryCodes(ctx, p.UserID, req.Code)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		// Recovery codes are shown exactly once; the response is marked no-store
		// so no intermediary caches them.
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

func writeEnrollment(w http.ResponseWriter, enrollment auth.Enrollment) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"secret":        enrollment.Secret,
		"otpauthUrl":    enrollment.OTPAuthURL,
		"recoveryCodes": enrollment.RecoveryCodes,
		"expiresAt":     enrollment.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func mfaStatusName(status storage.MFAStatus) string {
	switch status {
	case storage.MFAStatusEnabled:
		return "enabled"
	case storage.MFAStatusPending:
		return "pending"
	default:
		return "none"
	}
}

// effectivePolicy resolves the policy that applies to one account. Handlers need
// it for the MFA status response and for the disable guard, and computing it in
// one place keeps the two from disagreeing.
func (a *API) effectivePolicy(ctx context.Context, userID string) (auth.AuthPolicy, error) {
	if a.identity == nil || a.identity.Policy == nil {
		return auth.ResolvePolicy(storage.AuthSettings{}, storage.User{}, false), nil
	}
	settings, err := a.identity.Policy.Get(ctx)
	if err != nil {
		return auth.AuthPolicy{}, err
	}
	var user storage.User
	if a.DB != nil {
		user, err = a.DB.Users().Get(ctx, userID)
		if err != nil {
			return auth.AuthPolicy{}, err
		}
	}
	enabled := false
	if a.identity.MFA != nil {
		if enabled, err = a.identity.MFA.IsEnabled(ctx, userID); err != nil {
			return auth.AuthPolicy{}, err
		}
	}
	return auth.ResolvePolicy(settings, user, enabled), nil
}

// recordLoginMetric publishes one login attempt. Metrics are optional, so a test
// or an embedder that does not wire the registry still gets a working API. The
// internal state names are
// translated onto the documented metric results so the series stays stable even
// if a state is renamed, and so an error without a state still counts as a
// failure rather than disappearing from the denominator.
func (a *API) recordLoginMetric(method string, outcome auth.LoginOutcome, err error) {
	if a.identity == nil || a.identity.Metrics == nil {
		return
	}
	result := "failure"
	switch outcome.State {
	case auth.LoginStateAuthenticated:
		if err == nil {
			result = "success"
		}
	case auth.LoginStateMFARequired:
		result = "mfa_required"
	case auth.LoginStateThrottled:
		result = "throttled"
	}
	a.identity.Metrics.AuthLogin(method, result)
}

func (a *API) recordMFAVerifyMetric(outcome auth.LoginOutcome, err error) {
	if a.identity == nil || a.identity.Metrics == nil {
		return
	}
	result := "invalid"
	switch {
	case err == nil && outcome.State == auth.LoginStateAuthenticated:
		result = "success"
	case errors.Is(err, auth.ErrChallengeInvalid):
		result = "expired"
	case errors.Is(err, auth.ErrChallengeExhausted):
		result = "attempts_exceeded"
	}
	method := outcome.Method
	if method == "" {
		method = "totp"
	}
	a.identity.Metrics.AuthMFAVerify(method, result)
}

func (a *API) handleSelfDevices(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	if !a.identity.devicesReady() {
		writeAPIError(w, http.StatusServiceUnavailable, "identity_services_unavailable")
		return
	}
	ctx := r.Context()
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		views, err := a.identity.Devices.List(ctx, p.UserID, a.deviceCookie(r))
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": views, "nextCursor": "", "hasMore": false})
		return
	}
	if len(parts) != 1 {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	switch r.Method {
	case http.MethodPatch:
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := a.identity.Devices.Rename(ctx, p.UserID, id, req.Name); err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": req.Name})
	case http.MethodDelete:
		if err := a.identity.Devices.Revoke(ctx, p.UserID, p.UserID, id, false); err != nil {
			writeAuthError(w, err)
			return
		}
		// Clearing the cookie when the revoked device is the one making the
		// request stops a browser from re-presenting a dead token.
		if a.deviceCookie(r) != "" {
			a.clearDeviceCookie(w)
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true, "id": id})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleSelfIdentities(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	if a.identity == nil || a.identity.Identities == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "identity_services_unavailable")
		return
	}
	ctx := r.Context()
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		views, err := a.identity.Identities.List(ctx, p.UserID)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": views, "nextCursor": "", "hasMore": false})
		return
	}
	if len(parts) != 1 || r.Method != http.MethodDelete {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := a.identity.Identities.Unlink(ctx, p.UserID, p.UserID, parts[0], false); err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unlinked": true, "id": parts[0]})
}

// deviceCookie reads the trusted-device token. The value is a bearer secret, so
// it is never logged and never echoed in a response body.
func (a *API) deviceCookie(r *http.Request) string {
	name := "tm_device"
	if a.identity != nil && a.identity.DeviceCookie.Name != "" {
		name = a.identity.DeviceCookie.Name
	}
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

// setDeviceCookie writes the trusted-device credential. expiresAt is the stored
// expiry of the device row; it is preferred over the configured TTL so a cookie
// can never outlive the row that authorizes it. The configured TTL is the
// fallback for a caller that issued a device out of band.
func (a *API) setDeviceCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	cfg := a.identity.DeviceCookie
	if cfg.Name == "" {
		cfg.Name = "tm_device"
	}
	if cfg.Path == "" {
		cfg.Path = "/"
	}
	maxAge := int(cfg.TTL / time.Second)
	if !expiresAt.IsZero() {
		if remaining := int(time.Until(expiresAt) / time.Second); remaining < maxAge {
			maxAge = remaining
		}
	}
	if maxAge < 1 {
		maxAge = 1
	}
	cookie := &http.Cookie{
		Name:     cfg.Name,
		Value:    token,
		Path:     cfg.Path,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: sameSiteAttribute(cfg.SameSite),
	}
	http.SetCookie(w, cookie)
}

func (a *API) clearDeviceCookie(w http.ResponseWriter) {
	cfg := a.identity.DeviceCookie
	if cfg.Name == "" {
		cfg.Name = "tm_device"
	}
	path := cfg.Path
	if path == "" {
		path = "/"
	}
	http.SetCookie(w, &http.Cookie{Name: cfg.Name, Value: "", Path: path, MaxAge: -1, HttpOnly: true, Secure: cfg.Secure, SameSite: sameSiteAttribute(cfg.SameSite)})
}

// trustedProxies returns the configured proxy list. An empty list means no
// X-Forwarded-For value is believed, which is the safe default for a server that
// is exposed directly.
func (a *API) trustedProxies() []string {
	if a == nil {
		return nil
	}
	return a.trustedProxyList
}
