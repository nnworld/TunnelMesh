import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import AccountSecurity from '../views/AccountSecurity.vue'
import { i18n } from '../i18n'
import { flush, stubApi } from './api-stub'

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRoute: () => ({ path: '/account/security', params: {}, query: {} }) }
})

// The server encodes an empty Go slice as `null`, not `[]`. An account with no
// trusted devices or linked identities used to crash the whole page on render.
describe('account security with empty lists serialised as null', () => {
  beforeEach(() => { document.body.replaceChildren() })

  it('renders the empty states instead of a blank page', async () => {
    stubApi([
      { path: '/auth/mfa', data: { status: 'none', enabledAt: null, lastUsedAt: null, remainingRecoveryCodes: 0, policy: { mode: 'optional', required: false } } },
      { path: '/auth/devices', data: { items: null } },
      { path: '/auth/identities', data: { items: null } },
    ])
    const container = document.createElement('div')
    document.body.appendChild(container)
    const pinia = createPinia()
    setActivePinia(pinia)
    const app = createApp(AccountSecurity)
    const errors: unknown[] = []
    app.config.errorHandler = (error) => { errors.push(error) }
    app.use(pinia).use(i18n).mount(container)
    await flush()

    expect(errors).toEqual([])
    expect(container.querySelector('h1')?.textContent).toContain(i18n.global.t('security.title'))
    expect(container.textContent).toContain(i18n.global.t('security.devices.empty'))
    expect(container.textContent).toContain(i18n.global.t('security.identities.empty'))
    app.unmount()
  })
})
