import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import Login from '../views/Login.vue'
import { i18n } from '../i18n'
import { APIError, setToken } from '../api/client'
import { useAuthStore } from '../stores/auth'
import { callsTo, check, click, flush, inputOf, setValue, stubApi } from './api-stub'

// The login view only reads `?ticket=`/`?mfa=` and replaces the URL, so a stub
// router keeps the "exchanged exactly once" and "ticket removed" assertions
// precise without booting a real history.
const nav = vi.hoisted(() => ({
  query: {} as Record<string, string | undefined>,
  replace: vi.fn(),
  push: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/login', query: nav.query }),
  useRouter: () => ({ replace: nav.replace, push: nav.push }),
}))

const providerList = {
  items: [
    { id: 'p-okta', name: 'okta', displayName: 'Okta' },
    { id: 'p-entra', name: 'azure-ad', displayName: 'Entra ID' },
  ],
}
const session = { token: 'console-token-1', user: { id: 'u-1', username: 'admin', role: 'admin' }, deviceTrusted: true }
const challenge = { mfaRequired: true, challengeId: 'ch-1', methods: ['totp', 'recovery'], expiresAt: '2026-09-18T10:05:00Z' }
const noProviders = { path: '/auth/oidc/providers', status: 404, data: { error: 'not_found' } }

async function mountLogin() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const pinia = createPinia()
  setActivePinia(pinia)
  const app = createApp(Login)
  app.use(pinia)
  app.use(i18n)
  app.mount(container)
  await flush()
  return { container, unmount: () => { app.unmount(); container.remove() } }
}

beforeEach(() => {
  document.body.innerHTML = ''
  localStorage.clear()
  setToken('')
  nav.query = {}
  nav.replace.mockReset()
  nav.push.mockReset()
})

describe('login with single sign-on and a second factor', () => {
  it('offers one sign-in link per public OIDC provider', async () => {
    stubApi([{ path: '/auth/oidc/providers', data: providerList }])
    const { container, unmount } = await mountLogin()
    try {
      const links = [...container.querySelectorAll('[data-test="sso-link"]')]
      expect(links).toHaveLength(2)
      expect(links.map(link => link.getAttribute('href'))).toEqual([
        '/api/v1/auth/oidc/okta/authorize',
        '/api/v1/auth/oidc/azure-ad/authorize',
      ])
      expect(container.textContent).toContain('Okta')
      expect(container.textContent).toContain('Entra ID')
    } finally {
      unmount()
    }
  })

  it('hides single sign-on when the public provider list is disabled', async () => {
    stubApi([noProviders])
    const { container, unmount } = await mountLogin()
    try {
      expect(container.querySelectorAll('[data-test="sso-link"]')).toHaveLength(0)
      expect(container.querySelector('[data-test="login-error"]')).toBeNull()
      expect(container.querySelector('[data-test="login-submit"]')).not.toBeNull()
    } finally {
      unmount()
    }
  })

  it('renders the second factor step with a code field and a trust-device choice', async () => {
    const calls = stubApi([
      noProviders,
      { method: 'POST', path: '/auth/login', data: challenge },
    ])
    const { container, unmount } = await mountLogin()
    try {
      expect(container.querySelector('[data-test="mfa-step"]')).toBeNull()
      await setValue('[data-test="username"]', container, 'admin')
      await setValue('[data-test="password"]', container, 'correct horse battery')
      await click('[data-test="login-submit"]', container)

      expect(callsTo(calls, 'POST', '/auth/login')[0]?.body).toEqual({ username: 'admin', password: 'correct horse battery' })
      expect(container.querySelector('[data-test="mfa-step"]')).not.toBeNull()
      expect(inputOf('[data-test="mfa-code"]', container).getAttribute('autocomplete')).toBe('one-time-code')
      expect(container.querySelector('[data-test="trust-device"]')).not.toBeNull()
      expect(container.querySelector('[data-test="mfa-submit"]')).not.toBeNull()
      expect(localStorage.getItem('tunnelmesh_token')).toBeNull()
    } finally {
      unmount()
    }
  })

  it('verifies the code, stores only the console token, and forwards trust-device', async () => {
    const calls = stubApi([
      noProviders,
      { method: 'POST', path: '/auth/login', data: challenge },
      { method: 'POST', path: '/auth/mfa/verify', data: session },
    ])
    const { container, unmount } = await mountLogin()
    try {
      await setValue('[data-test="username"]', container, 'admin')
      await setValue('[data-test="password"]', container, 'correct horse battery')
      await click('[data-test="login-submit"]', container)
      await check('[data-test="trust-device"]', container)
      await setValue('[data-test="mfa-code"]', container, '123456')
      await click('[data-test="mfa-submit"]', container)

      expect(callsTo(calls, 'POST', '/auth/mfa/verify')[0]?.body).toEqual({ challengeId: 'ch-1', code: '123456', trustDevice: true })
      expect(localStorage.getItem('tunnelmesh_token')).toBe('console-token-1')
      expect(nav.push).toHaveBeenCalledWith('/')
    } finally {
      unmount()
    }
  })

  it('maps an invalid second factor to a localized reason and clears the input', async () => {
    stubApi([
      noProviders,
      { method: 'POST', path: '/auth/login', data: challenge },
      { method: 'POST', path: '/auth/mfa/verify', status: 401, data: { error: 'mfa_code_invalid' } },
    ])
    const { container, unmount } = await mountLogin()
    try {
      await setValue('[data-test="username"]', container, 'admin')
      await setValue('[data-test="password"]', container, 'correct horse battery')
      await click('[data-test="login-submit"]', container)
      await setValue('[data-test="mfa-code"]', container, '000000')
      await click('[data-test="mfa-submit"]', container)

      expect(container.querySelector('[data-test="mfa-error"]')?.textContent).toContain(i18n.global.t('auth.mfaErrorInvalid'))
      expect(inputOf('[data-test="mfa-code"]', container).value).toBe('')
      expect(nav.push).not.toHaveBeenCalled()
    } finally {
      unmount()
    }
  })

  it('exchanges a ticket from the query exactly once and never persists it', async () => {
    nav.query = { ticket: 'ticket-abc' }
    const calls = stubApi([{ method: 'POST', path: '/auth/oidc/exchange', data: { ...session, token: 'ticket-token' } }])
    const { container, unmount } = await mountLogin()
    try {
      await flush()
      await flush()

      const exchanges = callsTo(calls, 'POST', '/auth/oidc/exchange')
      expect(exchanges).toHaveLength(1)
      expect(exchanges[0]?.body).toEqual({ ticket: 'ticket-abc' })
      expect(localStorage.getItem('tunnelmesh_token')).toBe('ticket-token')
      expect(nav.replace).toHaveBeenCalledWith('/')
      const stored = Object.keys(localStorage).map(key => `${key}=${localStorage.getItem(key)}`).join('|')
      expect(stored).not.toContain('ticket-abc')
      expect(container.querySelector('[data-test="login-error"]')).toBeNull()
    } finally {
      unmount()
    }
  })

  it('reports a rejected ticket and drops it from the address bar', async () => {
    nav.query = { ticket: 'ticket-spent' }
    stubApi([
      { path: '/auth/oidc/providers', data: providerList },
      { method: 'POST', path: '/auth/oidc/exchange', status: 401, data: { error: 'login_ticket_invalid' } },
    ])
    const { container, unmount } = await mountLogin()
    try {
      expect(container.querySelector('[data-test="login-error"]')?.textContent).toContain(i18n.global.t('auth.ticketInvalid'))
      expect(nav.replace).toHaveBeenCalledWith('/login')
      expect(localStorage.getItem('tunnelmesh_token')).toBeNull()
    } finally {
      unmount()
    }
  })

  it('continues into the second factor when the callback asks for MFA', async () => {
    nav.query = { ticket: 'ticket-mfa' }
    stubApi([{ method: 'POST', path: '/auth/oidc/exchange', data: { ...challenge, challengeId: 'ch-oidc' } }])
    const { container, unmount } = await mountLogin()
    try {
      expect(container.querySelector('[data-test="mfa-step"]')).not.toBeNull()
      // The single-use ticket leaves the address bar; only the challenge stays.
      expect(nav.replace).toHaveBeenCalledWith({ path: '/login', query: { mfa: 'ch-oidc' } })
      expect(nav.replace).not.toHaveBeenCalledWith('/')
    } finally {
      unmount()
    }
  })

  it('pre-binds the second factor step to a challenge id from the query', async () => {
    nav.query = { mfa: 'ch-9' }
    const calls = stubApi([
      noProviders,
      { method: 'POST', path: '/auth/mfa/verify', data: session },
    ])
    const { container, unmount } = await mountLogin()
    try {
      expect(container.querySelector('[data-test="mfa-step"]')).not.toBeNull()
      await setValue('[data-test="mfa-code"]', container, '654321')
      await click('[data-test="mfa-submit"]', container)
      expect(callsTo(calls, 'POST', '/auth/mfa/verify')[0]?.body).toEqual({ challengeId: 'ch-9', code: '654321' })
      expect(nav.push).toHaveBeenCalledWith('/')
    } finally {
      unmount()
    }
  })

  it('blocks resubmission while a throttled login counts down', async () => {
    stubApi([noProviders, { method: 'POST', path: '/auth/login', status: 429, data: { error: 'login_throttled', retryAfter: 1 } }])
    const { container, unmount } = await mountLogin()
    try {
      await setValue('[data-test="username"]', container, 'admin')
      await setValue('[data-test="password"]', container, 'wrong')
      await click('[data-test="login-submit"]', container)

      const submit = container.querySelector('[data-test="login-submit"]') as HTMLButtonElement
      expect(submit.disabled).toBe(true)
      expect(container.querySelector('[data-test="login-error"]')?.textContent).toContain('1')

      await new Promise(resolve => setTimeout(resolve, 1200))
      await flush()
      expect((container.querySelector('[data-test="login-submit"]') as HTMLButtonElement).disabled).toBe(false)
      expect(container.querySelector('[data-test="login-error"]')).toBeNull()
    } finally {
      unmount()
    }
  })
})

describe('auth store login results', () => {
  it('resolves a discriminated result instead of throwing on MFA', async () => {
    stubApi([{ method: 'POST', path: '/auth/login', data: challenge }])
    setActivePinia(createPinia())
    const auth = useAuthStore()

    await expect(auth.login('admin', 'secret')).resolves.toEqual({
      state: 'mfa_required', challengeId: 'ch-1', methods: ['totp', 'recovery'], expiresAt: '2026-09-18T10:05:00Z',
    })
    expect(auth.token).toBe('')
    expect(auth.user).toBeNull()
  })

  it('resolves the throttled state with the server retry delay', async () => {
    stubApi([{ method: 'POST', path: '/auth/login', status: 429, data: { error: 'login_throttled', retryAfter: 42 } }])
    setActivePinia(createPinia())

    await expect(useAuthStore().login('admin', 'secret')).resolves.toEqual({ state: 'throttled', retryAfter: 42 })
  })

  it('stores the session when no second factor is required', async () => {
    stubApi([{ method: 'POST', path: '/auth/login', data: session }])
    setActivePinia(createPinia())
    const auth = useAuthStore()

    await expect(auth.login('admin', 'secret', true)).resolves.toEqual({
      state: 'authenticated', token: 'console-token-1', user: session.user, deviceTrusted: true,
    })
    expect(auth.token).toBe('console-token-1')
    expect(auth.isAdmin).toBe(true)
    expect(localStorage.getItem('tunnelmesh_token')).toBe('console-token-1')
  })

  it('keeps rejecting invalid credentials with the stable error domain', async () => {
    stubApi([{ method: 'POST', path: '/auth/login', status: 401, data: { error: 'invalid_credentials' } }])
    setActivePinia(createPinia())

    const error = await useAuthStore().login('admin', 'nope').catch((cause: unknown) => cause)
    expect(error).toBeInstanceOf(APIError)
    expect(error).toMatchObject({ status: 401, domain: 'invalid_credentials' })
  })

  it('reports exhausted recovery codes after a successful verification', async () => {
    stubApi([{ method: 'POST', path: '/auth/mfa/verify', data: { ...session, recoveryCodesExhausted: true } }])
    setActivePinia(createPinia())
    const auth = useAuthStore()

    await expect(auth.verifyMfa('ch-1', 'tmrc-aaaa')).resolves.toEqual({ deviceTrusted: true, recoveryCodesExhausted: true })
    expect(auth.token).toBe('console-token-1')
  })

  it('exchanges a ticket through the store without persisting the ticket', async () => {
    const calls = stubApi([{ method: 'POST', path: '/auth/oidc/exchange', data: session }])
    setActivePinia(createPinia())
    const auth = useAuthStore()

    await expect(auth.exchangeTicket('ticket-xyz', true)).resolves.toEqual({
      state: 'authenticated', token: 'console-token-1', user: session.user, deviceTrusted: true,
    })
    expect(calls[0]?.body).toEqual({ ticket: 'ticket-xyz', trustDevice: true })
    expect(Object.keys(localStorage)).toEqual(['tunnelmesh_token'])
  })
})
