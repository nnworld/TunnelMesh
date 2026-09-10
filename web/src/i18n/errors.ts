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
} as const

export function accountErrorMessage(error: unknown) {
  const domain = error instanceof APIError ? error.domain : undefined
  const key = domain && domain in errorKeys ? errorKeys[domain as keyof typeof errorKeys] : 'errors.unknown'
  return i18n.global.t(key)
}
