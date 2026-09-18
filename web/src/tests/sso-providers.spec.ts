import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { readFileSync } from 'node:fs'
import SSOProviders from '../views/SSOProviders.vue'
import router from '../router'
import { i18n } from '../i18n'
import { breadcrumbsFor } from '../layouts/breadcrumbs'
import { callsTo, check, click, flush, inputOf, setValue, stubApi, type ApiStub } from './api-stub'

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRoute: () => ({ path: '/sso-providers', params: {}, query: {} }) }
})

// A leaked secret in the response must never reach the DOM: the poisoned field
// below is not part of ProviderView, so rendering it would be a console bug.
const provider = {
  id: 'p-okta', name: 'okta', displayName: 'Okta', issuer: 'https://idp.example.com',
  clientId: 'client-1', scopes: ['openid', 'profile', 'email'],
  redirectUri: 'https://mesh.example.com/api/v1/auth/oidc/okta/callback',
  idTokenAlgs: ['RS256'], usernameClaim: 'preferred_username',
  roleMappings: [{ claim: 'groups', value: 'mesh-admins', role: 'admin' }],
  defaultRole: 'user', autoCreateUsers: true, publicListed: true, authoritativeRoles: false,
  enabled: true, hasSecret: true, createdAt: '2026-09-01T00:00:00Z', updatedAt: '2026-09-10T00:00:00Z',
  clientSecret: 'poison-secret-value',
}
const disabledProvider = { ...provider, id: 'p-entra', name: 'azure-ad', displayName: 'Entra ID', issuer: 'https://entra.example.com', enabled: false, hasSecret: false, publicListed: false }
const policy = {
  mfaMode: 'optional', deviceTrustEnabled: true, deviceTrustTtlSeconds: 2592000,
  allowTrustedDeviceBypass: true, maxTrustedDevices: 5, sessionTokenTtlSeconds: 43200,
}
const testReport = {
  discoveryOk: true, jwksOk: true, algorithms: ['RS256', 'ES256'],
  endpoints: { authorization: 'https://idp.example.com/authorize', token: 'https://idp.example.com/token', jwks: 'https://idp.example.com/keys' },
  error: '',
}

function stubSso(overrides: ApiStub[] = []) {
  return stubApi([
    ...overrides,
    { path: '/auth/policy', data: policy },
    { path: '/sso/providers', data: { items: [provider, disabledProvider] } },
  ])
}

async function mountSso() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const pinia = createPinia()
  setActivePinia(pinia)
  const app = createApp(SSOProviders)
  app.use(pinia)
  app.use(i18n)
  app.mount(container)
  await flush()
  return { container, unmount: () => { app.unmount(); container.remove() } }
}

const t = (key: string) => i18n.global.t(key)

beforeEach(() => {
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('SSO provider administration', () => {
  it('lists providers with a secret capability flag and never a secret value', async () => {
    stubSso()
    const { container, unmount } = await mountSso()
    try {
      expect(container.textContent).toContain('Okta')
      expect(container.textContent).toContain('https://idp.example.com')
      expect(container.textContent).toContain('client-1')
      expect(container.textContent).toContain(t('sso.secretStored'))
      expect(container.textContent).toContain(t('sso.secretNotStored'))
      expect(container.textContent).not.toContain('poison-secret-value')
    } finally {
      unmount()
    }
  })

  it('creates a provider with an idempotency key and wipes the secret from the form', async () => {
    const calls = stubSso([{ method: 'POST', path: '/sso/providers', data: provider }])
    const { container, unmount } = await mountSso()
    try {
      await click('[data-test="provider-create"]', container)
      await setValue('[data-test="provider-name"]', container, 'okta')
      await setValue('[data-test="provider-display-name"]', container, 'Okta')
      await setValue('[data-test="provider-issuer"]', container, 'https://idp.example.com')
      await setValue('[data-test="provider-client-id"]', container, 'client-1')
      await setValue('[data-test="provider-secret"]', container, 'super-secret')
      await setValue('[data-test="provider-scopes"]', container, 'openid, profile')
      await setValue('[data-test="provider-redirect-uri"]', container, 'https://mesh.example.com/api/v1/auth/oidc/okta/callback')
      await setValue('[data-test="provider-algs"]', container, 'RS256')
      await setValue('[data-test="provider-username-claim"]', container, 'preferred_username')
      await click('[data-test="provider-save"]', container)

      const create = callsTo(calls, 'POST', '/sso/providers')[0]
      expect(create?.headers.get('Idempotency-Key')).toBeTruthy()
      expect(create?.body).toEqual({
        name: 'okta', displayName: 'Okta', issuer: 'https://idp.example.com', clientId: 'client-1',
        clientSecret: 'super-secret', scopes: ['openid', 'profile'],
        redirectUri: 'https://mesh.example.com/api/v1/auth/oidc/okta/callback',
        idTokenAlgs: ['RS256'], usernameClaim: 'preferred_username', roleMappings: [],
        defaultRole: 'user', autoCreateUsers: false, publicListed: false, authoritativeRoles: false, enabled: true,
      })
      // The plaintext secret lives only in the form and is dropped after submit.
      expect(inputOf('[data-test="provider-secret"]', container).value).toBe('')
      expect(container.textContent).not.toContain('super-secret')
    } finally {
      unmount()
    }
  })

  it('rejects a provider whose scopes omit openid without calling the API', async () => {
    const calls = stubSso([{ method: 'POST', path: '/sso/providers', data: provider }])
    const { container, unmount } = await mountSso()
    try {
      await click('[data-test="provider-create"]', container)
      await setValue('[data-test="provider-name"]', container, 'okta')
      await setValue('[data-test="provider-issuer"]', container, 'https://idp.example.com')
      await setValue('[data-test="provider-client-id"]', container, 'client-1')
      await setValue('[data-test="provider-scopes"]', container, 'profile,email')
      await setValue('[data-test="provider-redirect-uri"]', container, 'https://mesh.example.com/api/v1/auth/oidc/okta/callback')
      await click('[data-test="provider-save"]', container)

      expect(callsTo(calls, 'POST', '/sso/providers')).toHaveLength(0)
      expect(container.querySelector('[data-test="provider-form-error"]')?.textContent).toContain(t('sso.scopesRequired'))
    } finally {
      unmount()
    }
  })

  it('rejects a weak id_token algorithm list', async () => {
    const calls = stubSso([{ method: 'POST', path: '/sso/providers', data: provider }])
    const { container, unmount } = await mountSso()
    try {
      await click('[data-test="provider-create"]', container)
      await setValue('[data-test="provider-name"]', container, 'okta')
      await setValue('[data-test="provider-issuer"]', container, 'https://idp.example.com')
      await setValue('[data-test="provider-client-id"]', container, 'client-1')
      await setValue('[data-test="provider-scopes"]', container, 'openid')
      await setValue('[data-test="provider-redirect-uri"]', container, 'https://mesh.example.com/api/v1/auth/oidc/okta/callback')
      await setValue('[data-test="provider-algs"]', container, 'HS256, none')
      await click('[data-test="provider-save"]', container)

      expect(callsTo(calls, 'POST', '/sso/providers')).toHaveLength(0)
      expect(container.querySelector('[data-test="provider-form-error"]')?.textContent).toContain(t('sso.invalidAlgs'))
    } finally {
      unmount()
    }
  })

  it('edits with an immutable name and an empty secret meaning keep', async () => {
    const calls = stubSso([{ method: 'PATCH', path: '/sso/providers/p-okta', data: provider }])
    const { container, unmount } = await mountSso()
    try {
      await click('[data-test="provider-edit-p-okta"]', container)
      expect(inputOf('[data-test="provider-name"]', container).value).toBe('okta')
      expect(inputOf('[data-test="provider-name"]', container).disabled).toBe(true)
      expect(inputOf('[data-test="provider-secret"]', container).value).toBe('')
      expect(inputOf('[data-test="provider-secret"]', container).placeholder).toBe(t('sso.secretPlaceholderKeep'))
      expect(inputOf('[data-test="mapping-claim-0"]', container).value).toBe('groups')
      await setValue('[data-test="provider-display-name"]', container, 'Okta workforce')
      await click('[data-test="provider-save"]', container)

      const update = callsTo(calls, 'PATCH', '/sso/providers/p-okta')[0]
      expect(update?.headers.get('Idempotency-Key')).toBeTruthy()
      expect(update?.body).not.toHaveProperty('clientSecret')
      expect(update?.body).toMatchObject({
        displayName: 'Okta workforce', issuer: 'https://idp.example.com', clientId: 'client-1',
        scopes: ['openid', 'profile', 'email'], idTokenAlgs: ['RS256'],
        roleMappings: [{ claim: 'groups', value: 'mesh-admins', role: 'admin' }],
        autoCreateUsers: true, publicListed: true, enabled: true,
      })
    } finally {
      unmount()
    }
  })

  it('runs a connectivity test and renders the report', async () => {
    const calls = stubSso([{ method: 'POST', path: '/sso/providers/p-okta/test', data: testReport }])
    const { container, unmount } = await mountSso()
    try {
      await click('[data-test="provider-test-p-okta"]', container)
      await flush()
      const test = callsTo(calls, 'POST', '/sso/providers/p-okta/test')[0]
      expect(test?.headers.get('Idempotency-Key')).toBeTruthy()
      const report = container.querySelector('[data-test="test-report"]')
      expect(report?.textContent).toContain('RS256')
      expect(report?.textContent).toContain('https://idp.example.com/authorize')
      expect(report?.textContent).toContain(t('sso.testOk'))
      expect(report?.textContent).not.toContain('poison-secret-value')
    } finally {
      unmount()
    }
  })

  it('renders a failing connectivity test with the server reason', async () => {
    stubSso([{ method: 'POST', path: '/sso/providers/p-okta/test', data: { discoveryOk: false, jwksOk: false, algorithms: [], endpoints: {}, error: 'dial tcp 203.0.113.5:443: i/o timeout' } }])
    const { container, unmount } = await mountSso()
    try {
      await click('[data-test="provider-test-p-okta"]', container)
      await flush()
      expect(container.querySelector('[data-test="test-error"]')?.textContent).toContain('dial tcp 203.0.113.5:443: i/o timeout')
      expect(container.querySelector('[data-test="test-report"]')?.textContent).toContain(t('sso.testFailed'))
    } finally {
      unmount()
    }
  })

  it('deletes a provider only after an explicit confirmation', async () => {
    const calls = stubSso([
      { path: '/sso/providers', sequence: [{ items: [provider, disabledProvider] }, { items: [disabledProvider] }] },
      { method: 'DELETE', path: '/sso/providers/p-okta', data: { id: 'p-okta' } },
    ])
    const { container, unmount } = await mountSso()
    try {
      await click('[data-test="provider-delete-p-okta"]', container)
      expect(callsTo(calls, 'DELETE', '/sso/providers/p-okta')).toHaveLength(0)
      await click('[data-test="confirm-delete-provider"]', container)

      const remove = callsTo(calls, 'DELETE', '/sso/providers/p-okta')[0]
      expect(remove?.headers.get('Idempotency-Key')).toBeTruthy()
      await flush()
      expect(container.textContent).not.toContain('https://idp.example.com')
    } finally {
      unmount()
    }
  })

  it('reads and saves the authentication policy with an idempotency key', async () => {
    const calls = stubSso([{ method: 'PUT', path: '/auth/policy', data: { ...policy, mfaMode: 'required' } }])
    const { container, unmount } = await mountSso()
    try {
      expect(inputOf('[data-test="policy-mfa-optional"]', container).checked).toBe(true)
      expect(container.textContent).toContain(t('sso.policyTitle'))
      await check('[data-test="policy-mfa-required"]', container)
      await click('[data-test="save-policy"]', container)

      const save = callsTo(calls, 'PUT', '/auth/policy')[0]
      expect(save?.headers.get('Idempotency-Key')).toBeTruthy()
      expect(save?.body).toEqual({ ...policy, mfaMode: 'required' })
    } finally {
      unmount()
    }
  })

  it('registers an admin-only route, navigation entry, and breadcrumb', () => {
    const route = router.getRoutes().find(entry => entry.path === '/sso-providers')
    expect(route?.meta).toMatchObject({ auth: true, admin: true })
    expect(breadcrumbsFor('/sso-providers')).toEqual([{ key: 'shell.console' }, { key: 'sso.title' }])
    expect(readFileSync('src/layouts/AppShell.vue', 'utf8')).toContain("['/sso-providers', 'navigation.sso']")
    for (const file of ['src/i18n/messages/zh-CN.ts', 'src/i18n/messages/en-US.ts']) {
      expect(readFileSync(file, 'utf8')).toContain('sso:')
    }
  })
})
