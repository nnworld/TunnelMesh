import { APIError } from '../api/client'
import { i18n } from '.'

const errorKeys = {
  username_invalid: 'errors.usernameInvalid',
  username_conflict: 'errors.usernameConflict',
  password_policy_violation: 'errors.passwordPolicy',
  current_password_invalid: 'errors.currentPassword',
  admin_account_protected: 'errors.adminProtected',
  account_deleted: 'errors.accountDeleted',
  account_status_invalid: 'errors.accountStatus',
  idempotency_key_conflict: 'errors.idempotencyConflict',
  idempotency_in_progress: 'errors.idempotencyInProgress',
} as const

// Stable identity domains. They are mapped here, in one place, because the same
// reason can surface on the login page, in account security, and in SSO
// administration, and each of them must translate identically.
const authErrorKeys = {
  invalid_credentials: 'auth.failed',
  login_throttled: 'auth.throttled',
  login_ticket_invalid: 'auth.ticketInvalid',
  account_disabled: 'auth.accountDisabled',
  oidc_user_not_provisioned: 'auth.oidcUserNotProvisioned',
  mfa_challenge_invalid: 'auth.mfaErrorChallenge',
  mfa_attempts_exceeded: 'auth.mfaErrorExceeded',
  mfa_code_invalid: 'auth.mfaErrorInvalid',
  mfa_code_reused: 'auth.mfaErrorReused',
  mfa_not_enrolled: 'auth.mfaErrorNotEnrolled',
  mfa_required_by_policy: 'security.mfa.requiredByPolicy',
  current_password_required: 'security.mfa.currentPasswordRequired',
  device_not_found: 'security.devices.notFound',
  identity_required_for_login: 'security.identities.requiredForLogin',
  identity_not_found: 'security.identities.notFound',
  secret_storage_unavailable: 'errors.secretStorageUnavailable',
} as const

export function accountErrorMessage(error: unknown) {
  const domain = error instanceof APIError ? error.domain : undefined
  const key = domain && domain in errorKeys ? errorKeys[domain as keyof typeof errorKeys] : 'errors.unknown'
  return i18n.global.t(key)
}

export function authErrorMessage(error: unknown, fallbackKey = 'errors.unknown') {
  const domain = error instanceof APIError ? error.domain : undefined
  const key = domain && domain in authErrorKeys ? authErrorKeys[domain as keyof typeof authErrorKeys] : undefined
  if (key) return i18n.global.t(key)
  // A 4xx reason written by the server (OIDC provider validation, policy range
  // checks) is more actionable than a generic label; 5xx text is not.
  if (error instanceof APIError && error.status >= 400 && error.status < 500 && error.message) return error.message
  return i18n.global.t(fallbackKey)
}
