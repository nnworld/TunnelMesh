import { api } from './client'

// Identity endpoints for SSO, MFA, and device trust. Every secret crossing this
// module is write-only: the server answers with a capability flag (`hasSecret`)
// or a value that exists exactly once (TOTP secret, recovery codes), so nothing
// here may be cached, persisted, or sent back to the browser storage.

export type SessionUser = { id: string; username: string; role: 'admin' | 'user' }

// Raw body shared by password login, MFA verification, and ticket exchange.
// `mfaRequired` discriminates the two success shapes; the auth store narrows it
// into `LoginResult` so views never inspect a partial payload.
export type LoginPayload = {
  token?: string
  user?: SessionUser
  deviceTrusted?: boolean
  recoveryCodesExhausted?: boolean
  mfaRequired?: boolean
  challengeId?: string
  methods?: string[]
  expiresAt?: string
}

export type OIDCProviderSummary = { id: string; name: string; displayName: string }

export type MFAMode = 'disabled' | 'optional' | 'required'
export type MFAStatusValue = 'none' | 'pending' | 'enabled'
export type MFAStatus = {
  status: MFAStatusValue
  enabledAt?: string | null
  lastUsedAt?: string | null
  remainingRecoveryCodes: number
  policy: { mode: MFAMode; required: boolean }
}
export type MFAEnrollment = { secret: string; otpauthUrl: string; recoveryCodes: string[]; expiresAt: string }

export type TrustedDevice = {
  id: string
  name: string
  userAgent: string
  ip: string
  trustedAt: string
  expiresAt: string
  lastSeenAt?: string | null
  current: boolean
}
export type DevicePage = { items: TrustedDevice[]; nextCursor?: string; hasMore?: boolean }

export type LinkedIdentity = {
  id: string
  providerId: string
  providerName: string
  subject: string
  username?: string
  email?: string
  displayName?: string
  linkedAt: string
  lastLoginAt?: string | null
}

export type ProviderRole = 'admin' | 'user'
export type RoleMapping = { claim: string; value: string; role: ProviderRole }
// The read model never carries the client secret, only `hasSecret`.
export type SSOProvider = {
  id: string
  name: string
  displayName: string
  issuer: string
  clientId: string
  scopes: string[]
  redirectUri: string
  idTokenAlgs: string[]
  usernameClaim: string
  roleMappings: RoleMapping[]
  defaultRole: ProviderRole
  autoCreateUsers: boolean
  publicListed: boolean
  authoritativeRoles: boolean
  enabled: boolean
  hasSecret: boolean
  createdAt: string
  updatedAt: string
}
export type SSOProviderPage = { items: SSOProvider[]; nextCursor?: string; hasMore?: boolean }
// `clientSecret` is optional on purpose: an absent or empty value tells the
// server to keep the stored ciphertext, which is how an edit form avoids
// round-tripping a secret it can never read.
export type SSOProviderInput = {
  name: string
  displayName: string
  issuer: string
  clientId: string
  clientSecret?: string
  scopes: string[]
  redirectUri: string
  idTokenAlgs: string[]
  usernameClaim: string
  roleMappings: RoleMapping[]
  defaultRole: ProviderRole
  autoCreateUsers: boolean
  publicListed: boolean
  authoritativeRoles: boolean
  enabled: boolean
}
export type SSOProviderUpdate = Partial<SSOProviderInput>
export type SSOProviderTestReport = {
  discoveryOk: boolean
  jwksOk: boolean
  algorithms: string[]
  endpoints: Record<string, string>
  error?: string
}

export type AuthPolicy = {
  mfaMode: MFAMode
  deviceTrustEnabled: boolean
  deviceTrustTtlSeconds: number
  allowTrustedDeviceBypass: boolean
  maxTrustedDevices: number
  sessionTokenTtlSeconds: number
}

function pageQuery(params: { cursor?: string; limit?: number }) {
  const query = new URLSearchParams()
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  return query.size ? `?${query}` : ''
}

// --- unauthenticated login surface ---

// 404 means the deployment does not publish its providers; the caller hides the
// single sign-on section instead of showing an error.
export function listOIDCProviders() {
  return api<{ items: OIDCProviderSummary[] }>('/auth/oidc/providers').then(data => data.items)
}

// `ticket` is single-use and short-lived. It is only ever sent in this request
// body, never stored, and the caller must drop it from the address bar.
export function exchangeOIDCTicket(ticket: string, trustDevice = false) {
  return api<LoginPayload>('/auth/oidc/exchange', {
    method: 'POST',
    body: JSON.stringify(trustDevice ? { ticket, trustDevice: true } : { ticket }),
  })
}

// --- self-service MFA ---

export function getMFAStatus() { return api<MFAStatus>('/auth/mfa') }

export function enrollMFA(currentPassword?: string) {
  return api<MFAEnrollment>('/auth/mfa/enroll', {
    method: 'POST',
    body: JSON.stringify(currentPassword ? { currentPassword } : {}),
  })
}

export function enableMFA(code: string) {
  return api<{ status: MFAStatusValue; enabledAt: string }>('/auth/mfa/enable', { method: 'POST', body: JSON.stringify({ code }) })
}

export function disableMFA(code: string) {
  return api<{ status: 'disabled' }>('/auth/mfa', { method: 'DELETE', body: JSON.stringify({ code }) })
}

export function regenerateRecoveryCodes(code: string) {
  return api<{ recoveryCodes: string[] }>('/auth/mfa/recovery-codes', { method: 'POST', body: JSON.stringify({ code }) })
}

// --- self-service trusted devices ---

export function listDevices(params: { cursor?: string; limit?: number } = {}) {
  return api<DevicePage>(`/auth/devices${pageQuery(params)}`)
}

export function renameDevice(id: string, name: string) {
  return api<{ id: string; name: string }>(`/auth/devices/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify({ name }) })
}

export function revokeDevice(id: string) {
  return api<{ revoked: boolean }>(`/auth/devices/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// --- self-service linked identities ---

export function listIdentities() {
  return api<{ items: LinkedIdentity[] }>('/auth/identities').then(data => data.items)
}

export function unlinkIdentity(id: string) {
  return api<{ unlinked: boolean }>(`/auth/identities/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// --- administrator: OIDC providers ---

export function getSSOProviders(params: { cursor?: string; limit?: number } = {}) {
  return api<SSOProviderPage>(`/sso/providers${pageQuery(params)}`)
}

export function createSSOProvider(input: SSOProviderInput, idempotencyKey: string) {
  return api<SSOProvider>('/sso/providers', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

export function updateSSOProvider(id: string, input: SSOProviderUpdate, idempotencyKey: string) {
  return api<SSOProvider>(`/sso/providers/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

export function deleteSSOProvider(id: string, idempotencyKey: string) {
  return api<SSOProvider>(`/sso/providers/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

// Discovery plus JWKS retrieval only: the report never contains the secret and
// no token exchange is performed.
export function testSSOProvider(id: string, idempotencyKey: string) {
  return api<SSOProviderTestReport>(`/sso/providers/${encodeURIComponent(id)}/test`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

// --- administrator: authentication policy ---

export function getAuthPolicy() { return api<AuthPolicy>('/auth/policy') }

export function updateAuthPolicy(input: AuthPolicy, idempotencyKey: string) {
  return api<AuthPolicy>('/auth/policy', {
    method: 'PUT',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

// --- administrator: per-account MFA and devices ---

// Clearing MFA also revokes every trusted device, so the user re-enrolls and no
// bypass survives the reset.
export function resetUserMFA(userId: string, idempotencyKey: string) {
  return api<{ reset: boolean }>(`/users/${encodeURIComponent(userId)}/mfa/reset`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

export function listUserDevices(userId: string, params: { cursor?: string; limit?: number } = {}) {
  return api<DevicePage>(`/users/${encodeURIComponent(userId)}/devices${pageQuery(params)}`)
}

export function revokeUserDevice(userId: string, deviceId: string, idempotencyKey: string) {
  return api<{ revoked: boolean }>(`/users/${encodeURIComponent(userId)}/devices/${encodeURIComponent(deviceId)}`, {
    method: 'DELETE',
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}
