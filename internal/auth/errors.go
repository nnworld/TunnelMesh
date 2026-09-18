package auth

import "errors"

// Sentinel errors for the identity flows introduced with SSO, MFA, and device
// trust. Following the account-service convention, the error text is the stable
// machine-readable code that handlers place in data.error, so clients branch on
// the string instead of parsing prose. Errors that could reveal whether an
// account exists deliberately collapse into one value.
var (
	// ErrSecretStorageUnavailable means TUNNELMESH_TOKEN_ENCRYPTION_KEY is not
	// configured. Features that must store a recoverable secret fail closed
	// rather than degrading to plaintext.
	ErrSecretStorageUnavailable = errors.New("secret_storage_unavailable")

	ErrMFANotEnrolled         = errors.New("mfa_not_enrolled")
	ErrMFAPendingNotConfirmed = errors.New("mfa_not_confirmed")
	ErrMFACodeInvalid         = errors.New("mfa_code_invalid")
	ErrMFAReplayDetected      = errors.New("mfa_code_reused")
	ErrMFARequiredByPolicy    = errors.New("mfa_required_by_policy")
	ErrMFAAlreadyEnabled      = errors.New("mfa_already_enabled")

	ErrCurrentPasswordRequired = errors.New("current_password_required")
	ErrLastAdminProtected      = errors.New("last_admin_protected")

	ErrDeviceTrustDisabled = errors.New("device_trust_disabled")
	ErrDeviceNotFound      = errors.New("device_not_found")

	ErrLoginThrottled     = errors.New("login_throttled")
	ErrChallengeInvalid   = errors.New("challenge_invalid")
	ErrChallengeExhausted = errors.New("challenge_attempts_exceeded")

	ErrOIDCProviderNotFound     = errors.New("oidc_provider_not_found")
	ErrOIDCProviderDisabled     = errors.New("oidc_provider_disabled")
	ErrOIDCProviderInvalid      = errors.New("oidc_provider_invalid")
	ErrOIDCProviderInUse        = errors.New("oidc_provider_in_use")
	ErrOIDCUserNotProvisioned   = errors.New("oidc_user_not_provisioned")
	ErrOIDCLoginTicketInvalid   = errors.New("login_ticket_invalid")
	ErrOIDCStateInvalid         = errors.New("oidc_state_invalid")
	ErrOIDCProviderError        = errors.New("oidc_provider_error")
	ErrIdentityRequiredForLogin = errors.New("identity_required_for_login")

	ErrAccountDisabled = errors.New("account_disabled")
)
