package server

import (
	"database/sql"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/auth/oidc"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// DeviceCookieConfig describes the trusted-device cookie. It lives in the server
// package because cookie attributes are a transport concern, not a policy one:
// the policy that decides whether a device may be trusted at all is auth_settings.
type DeviceCookieConfig struct {
	Name     string
	Secure   bool
	SameSite string
	Path     string
	// TTL mirrors auth_settings.device_trust_ttl_seconds so the cookie expires
	// with the row it refers to instead of outliving it.
	TTL time.Duration
}

// OIDCHTTPConfig carries the process-level OIDC knobs the handlers need.
type OIDCHTTPConfig struct {
	// PublicProviders gates the unauthenticated provider listing. Turning it off
	// hides the SSO buttons from anyone who is not already told the URL.
	PublicProviders bool
	StateTTL        time.Duration
	LoginTicketTTL  time.Duration
	// LoginRedirectPath is the fixed relative path the callback redirects to. It
	// is never derived from request data, which is what makes an open redirect
	// through the callback impossible.
	LoginRedirectPath string
}

// IdentityServices bundles the Phase A identity collaborators. It is installed
// through SetIdentityServices so existing NewAPI(db, auth) callers and the whole
// existing test suite keep compiling, and so a deployment that has not wired the
// services answers 503 instead of panicking.
type IdentityServices struct {
	Logins       *auth.LoginService
	MFA          *auth.MFAService
	Devices      *auth.DeviceService
	Identities   *auth.IdentityService
	Providers    *auth.OIDCProviderService
	Policy       *auth.AuthPolicyService
	Challenges   *auth.ChallengeStore
	RelyingParty *oidc.RelyingParty
	OIDC         OIDCHTTPConfig
	DeviceCookie DeviceCookieConfig
	// Metrics is optional. Declaring it as an interface keeps the server package
	// from depending on the concrete registry and lets tests leave it nil.
	Metrics AuthMetrics
}

// AuthMetrics is the observability seam for the identity flows.
type AuthMetrics interface {
	AuthLogin(method, result string)
	AuthMFAVerify(method, result string)
	AuthOIDCStep(step, result string)
}

// ready reports whether the core login services are present.
func (s *IdentityServices) ready() bool {
	return s != nil && s.Logins != nil && s.MFA != nil && s.Challenges != nil
}

// devicesReady reports whether device trust is wired. It is separate because a
// deployment may legitimately run without device trust while still using MFA.
func (s *IdentityServices) devicesReady() bool { return s != nil && s.Devices != nil }

// ssoReady reports whether the administrative SSO surface is wired.
func (s *IdentityServices) ssoReady() bool { return s != nil && s.Providers != nil && s.Policy != nil }

// SetIdentityServices installs the identity collaborators after construction.
func (a *API) SetIdentityServices(services *IdentityServices) {
	if a == nil {
		return
	}
	a.identity = services
}

// IdentityServices exposes the installed container so an embedder can inspect it.
func (a *API) IdentityServices() *IdentityServices {
	if a == nil {
		return nil
	}
	return a.identity
}

// requireIdentity answers 503 with a stable code when the services are missing.
// Failing closed here matches the credential service posture: a half-configured
// deployment must not silently accept an unverified login.
func requireIdentity(w http.ResponseWriter, services *IdentityServices) bool {
	if services.ready() {
		return true
	}
	writeAPIError(w, http.StatusServiceUnavailable, "identity_services_unavailable")
	return false
}

func requireSSO(w http.ResponseWriter, services *IdentityServices) bool {
	if services.ssoReady() {
		return true
	}
	writeAPIError(w, http.StatusServiceUnavailable, "identity_services_unavailable")
	return false
}

// writeAuthError maps every identity sentinel onto a status code and a stable
// data.error code. Keeping the mapping in one place is what makes the client
// contract testable and stops two handlers from disagreeing about a status.
func writeAuthError(w http.ResponseWriter, err error) {
	status, code := authErrorStatus(err)
	writeAPIError(w, status, code)
}

func authErrorStatus(err error) (int, string) {
	switch {
	case err == nil:
		return http.StatusOK, ""
	case errors.Is(err, auth.ErrUnauthenticated):
		return http.StatusUnauthorized, "unauthenticated"
	case errors.Is(err, auth.ErrForbidden), errors.Is(err, auth.ErrAdminAccountProtected),
		errors.Is(err, auth.ErrCurrentPasswordInvalid), errors.Is(err, auth.ErrAccountDisabled):
		// A disabled account reaching the OIDC provisioning path is an operator
		// decision, so it is a 403 with a stable code rather than a 500 that reads
		// as a server defect. The password path still folds this into
		// invalid_credentials because there it would enumerate accounts.
		return http.StatusForbidden, firstErrorCode(err, "forbidden")
	case errors.Is(err, auth.ErrInvalidLogin), errors.Is(err, auth.ErrInvalidCredentials):
		// One code for every credential failure, so the response cannot be used
		// to enumerate accounts.
		return http.StatusUnauthorized, "invalid_credentials"
	case errors.Is(err, auth.ErrLoginThrottled):
		return http.StatusTooManyRequests, "login_throttled"
	case errors.Is(err, auth.ErrChallengeExhausted):
		return http.StatusUnauthorized, "mfa_attempts_exceeded"
	case errors.Is(err, auth.ErrChallengeInvalid):
		return http.StatusUnauthorized, "mfa_challenge_invalid"
	case errors.Is(err, auth.ErrMFAReplayDetected):
		return http.StatusUnauthorized, "mfa_code_reused"
	case errors.Is(err, auth.ErrMFACodeInvalid):
		return http.StatusUnauthorized, "mfa_code_invalid"
	case errors.Is(err, auth.ErrMFANotEnrolled):
		return http.StatusUnauthorized, "mfa_not_enrolled"
	case errors.Is(err, auth.ErrMFAPendingNotConfirmed), errors.Is(err, auth.ErrMFAAlreadyEnabled),
		errors.Is(err, auth.ErrMFARequiredByPolicy), errors.Is(err, auth.ErrLastAdminProtected),
		errors.Is(err, auth.ErrOIDCProviderInUse), errors.Is(err, auth.ErrIdentityRequiredForLogin),
		errors.Is(err, auth.ErrDeviceTrustDisabled), errors.Is(err, storage.ErrIdentityConflict):
		return http.StatusConflict, firstErrorCode(err, "conflict")
	case errors.Is(err, auth.ErrCurrentPasswordRequired), errors.Is(err, auth.ErrAuthPolicyInvalid),
		errors.Is(err, auth.ErrOIDCProviderInvalid), errors.Is(err, auth.ErrUsernameInvalid),
		errors.Is(err, oidc.ErrRoleMappingInvalid), errors.Is(err, oidc.ErrProviderInvalid):
		return http.StatusBadRequest, firstErrorCode(err, "bad_request")
	case errors.Is(err, auth.ErrOIDCStateInvalid), errors.Is(err, auth.ErrOIDCProviderError):
		return http.StatusBadRequest, firstErrorCode(err, "oidc_state_invalid")
	case errors.Is(err, auth.ErrOIDCLoginTicketInvalid):
		return http.StatusUnauthorized, "login_ticket_invalid"
	case errors.Is(err, auth.ErrOIDCUserNotProvisioned):
		return http.StatusForbidden, "oidc_user_not_provisioned"
	case errors.Is(err, auth.ErrOIDCProviderNotFound), errors.Is(err, auth.ErrOIDCProviderDisabled),
		errors.Is(err, auth.ErrDeviceNotFound), errors.Is(err, auth.ErrIdentityNotFound),
		errors.Is(err, auth.ErrAccountNotFound), errors.Is(err, sql.ErrNoRows):
		// A missing identity or device is reported as not found rather than
		// forbidden, so an identifier cannot be used to probe which accounts have
		// an external login or a trusted browser.
		return http.StatusNotFound, firstErrorCode(err, "not_found")
	case errors.Is(err, auth.ErrSecretStorageUnavailable):
		return http.StatusServiceUnavailable, "secret_storage_unavailable"
	case errors.Is(err, ErrIdempotencyKeyConflict):
		// A replayed administrative mutation is a client-visible conflict. Mapping it
		// to 500 would tell the operator the server failed while it correctly refused
		// to apply the same key twice. The codes are literal rather than derived from
		// the sentinel text, which reads as prose and is not a stable code.
		return http.StatusConflict, "idempotency_key_conflict"
	case errors.Is(err, ErrIdempotencyInProgress):
		return http.StatusConflict, "idempotency_in_progress"
	case errors.Is(err, oidc.ErrDiscoveryFailed), errors.Is(err, oidc.ErrJWKSFetchFailed),
		errors.Is(err, oidc.ErrTokenExchangeFailed):
		// The IdP is unreachable or misbehaving. 502 says "upstream", and the
		// stable code lets the console show a retry rather than a bug report.
		return http.StatusBadGateway, firstErrorCode(err, "oidc_upstream_error")
	case errors.Is(err, oidc.ErrIDTokenInvalid), errors.Is(err, oidc.ErrUnsupportedAlgorithm),
		errors.Is(err, oidc.ErrNoMatchingKey), errors.Is(err, oidc.ErrClaimMissing):
		return http.StatusUnauthorized, firstErrorCode(err, "oidc_id_token_invalid")
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

// firstErrorCode returns the text of the first sentinel in the chain that looks
// like a stable snake_case code. Services wrap sentinels with a human-readable
// suffix, and only the code belongs in data.error.
func firstErrorCode(err error, fallback string) string {
	for candidate := err; candidate != nil; candidate = errors.Unwrap(candidate) {
		text := candidate.Error()
		if idx := strings.IndexAny(text, ": "); idx >= 0 {
			text = text[:idx]
		}
		if isStableErrorCode(text) {
			return text
		}
		if unwrap := errors.Unwrap(candidate); unwrap == nil {
			break
		}
	}
	return fallback
}

// isStableErrorCode recognizes the lowercase snake_case convention every sentinel
// in internal/auth uses, so prose from a wrapped error is never echoed to a client.
func isStableErrorCode(text string) bool {
	if text == "" || len(text) > 64 || strings.HasPrefix(text, "oidc_") == false && !strings.Contains(text, "_") {
		// A code must contain an underscore; prose rarely matches that shape and
		// never matches the rest of the rules below.
		if !strings.Contains(text, "_") {
			return false
		}
	}
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// envSecretStore builds the AES-GCM store from the environment-injected key. It
// returns nil when the key is absent or unusable, which is how every caller
// reaches the same fail-closed conclusion. This replaces the three identical
// copies of the lookup that CredentialService, TokenService, and the identity
// services would otherwise each carry.
func envSecretStore() *auth.SecretStore {
	encodedKey := strings.TrimSpace(os.Getenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY"))
	if encodedKey == "" {
		return nil
	}
	keyID := strings.TrimSpace(os.Getenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID"))
	if keyID == "" {
		keyID = "default"
	}
	store, err := auth.NewSecretStore(encodedKey, keyID)
	if err != nil {
		return nil
	}
	return store
}

// NewSecretProvider exposes the environment-derived key as the SecretProvider
// seam the identity services depend on. A nil store yields a provider whose
// Available reports false, so enrollment and provider creation fail closed with
// 503 secret_storage_unavailable rather than storing plaintext.
func NewSecretProvider() auth.SecretProvider {
	return auth.NewSecretProvider(envSecretStore())
}

// clientIP resolves the address used for throttling and audit. X-Forwarded-For is
// honoured only when the direct peer is a configured trusted proxy; otherwise the
// header is attacker-controlled and using it would let a client pick its own
// throttle bucket.
func clientIP(r *http.Request, trustedProxies []string) string {
	peer := remoteIP(r.RemoteAddr)
	if peer != "" && isTrustedProxy(peer, trustedProxies) {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			// The leftmost entry is the original client; every entry to its right
			// was appended by a proxy we already decided to trust.
			if first := strings.TrimSpace(strings.Split(forwarded, ",")[0]); first != "" {
				return first
			}
		}
		if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
			return real
		}
	}
	if peer != "" {
		return peer
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func remoteIP(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// isTrustedProxy matches a peer against CIDR or literal entries. An empty list
// trusts nobody, which is the safe default for a server exposed directly.
func isTrustedProxy(peer string, trustedProxies []string) bool {
	if len(trustedProxies) == 0 || peer == "" {
		return false
	}
	address := net.ParseIP(peer)
	for _, entry := range trustedProxies {
		value := strings.TrimSpace(entry)
		if value == "" {
			continue
		}
		if !strings.Contains(value, "/") {
			if value == peer {
				return true
			}
			continue
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			continue
		}
		if address != nil && network.Contains(address) {
			return true
		}
	}
	return false
}

// sameSiteAttribute maps the configured string onto the http package constants.
// An unrecognized value falls back to Lax, the strictest setting that still lets
// a top-level navigation carry the cookie.
func sameSiteAttribute(value string) http.SameSite {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}
