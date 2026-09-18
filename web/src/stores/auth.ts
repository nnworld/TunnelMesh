import { defineStore } from 'pinia'
import { APIError, api, getToken, setToken } from '../api/client'
import { exchangeOIDCTicket } from '../api/auth'
import type { LoginPayload, SessionUser } from '../api/auth'

export type User = SessionUser

// `login` resolves instead of throwing for every outcome the login view has to
// render: a second factor is a normal next step and a throttled bucket carries a
// retry delay. Genuine failures (bad credentials, network) still throw so the
// caller keeps one generic error path.
export type LoginResult =
  | { state: 'authenticated'; token: string; user: User; deviceTrusted: boolean }
  | { state: 'mfa_required'; challengeId: string; methods: string[]; expiresAt: string }
  | { state: 'throttled'; retryAfter: number }

export type MfaVerifyResult = { deviceTrusted: boolean; recoveryCodesExhausted: boolean }

// Only the console bearer token is persisted. The trusted-device token stays in
// the HttpOnly cookie the server set, and an OIDC login ticket is never stored.
function adopt(store: { token: string; user: User | null }, result: LoginResult): LoginResult {
  if (result.state === 'authenticated') { store.token = result.token; store.user = result.user; setToken(result.token) }
  return result
}

function toLoginResult(data: LoginPayload): LoginResult {
  if (data.mfaRequired) {
    return { state: 'mfa_required', challengeId: data.challengeId ?? '', methods: data.methods ?? [], expiresAt: data.expiresAt ?? '' }
  }
  if (!data.token || !data.user) throw new APIError('login response is missing a session', 200, 200, 'invalid_login_response')
  return { state: 'authenticated', token: data.token, user: data.user, deviceTrusted: Boolean(data.deviceTrusted) }
}

// A throttled bucket is reported as 429 with a stable domain and a retry delay in
// seconds; anything else stays an error the caller has to handle.
function throttleRetryAfter(error: unknown): number | undefined {
  if (!(error instanceof APIError) || error.domain !== 'login_throttled') return undefined
  const seconds = Number(error.details?.retryAfter)
  return Number.isFinite(seconds) && seconds > 0 ? Math.ceil(seconds) : 0
}

export const useAuthStore = defineStore('auth', {
  state: () => ({ token: getToken(), user: null as User | null }),
  getters: { isAdmin: (state) => state.user?.role === 'admin' },
  actions: {
    async login(username: string, password: string, trustDevice = false): Promise<LoginResult> {
      // trustDevice is omitted when false so an upgrade keeps sending exactly the
      // body a pre-MFA server accepted.
      const body: Record<string, unknown> = { username, password }
      if (trustDevice) body.trustDevice = true
      try {
        return adopt(this, toLoginResult(await api<LoginPayload>('/auth/login', { method: 'POST', body: JSON.stringify(body) })))
      } catch (error) {
        const retryAfter = throttleRetryAfter(error)
        if (retryAfter === undefined) throw error
        return { state: 'throttled', retryAfter }
      }
    },
    async verifyMfa(challengeId: string, code: string, trustDevice = false): Promise<MfaVerifyResult> {
      const body: Record<string, unknown> = { challengeId, code }
      if (trustDevice) body.trustDevice = true
      const data = await api<LoginPayload>('/auth/mfa/verify', { method: 'POST', body: JSON.stringify(body) })
      const result = adopt(this, toLoginResult(data))
      if (result.state !== 'authenticated') throw new APIError('mfa verification did not return a session', 200, 200, 'mfa_challenge_invalid')
      return { deviceTrusted: result.deviceTrusted, recoveryCodesExhausted: Boolean(data.recoveryCodesExhausted) }
    },
    async exchangeTicket(ticket: string, trustDevice = false): Promise<LoginResult> {
      return adopt(this, toLoginResult(await exchangeOIDCTicket(ticket, trustDevice)))
    },
    async load() { if (!this.token) return; try { this.user = await api<User>('/auth/me') } catch { this.logout() } },
    logout() { this.token = ''; this.user = null; setToken('') }
  }
})
