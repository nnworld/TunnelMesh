import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { readFileSync } from 'node:fs'
import AccountSecurity from '../views/AccountSecurity.vue'
import { i18n } from '../i18n'
import { callsTo, check, click, flush, setValue, stubApi, type ApiStub } from './api-stub'

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRoute: () => ({ path: '/account/security', params: {}, query: {} }) }
})

const statusNone = { status: 'none', enabledAt: null, lastUsedAt: null, remainingRecoveryCodes: 0, policy: { mode: 'optional', required: false } }
const statusEnabled = { status: 'enabled', enabledAt: '2026-09-01T08:00:00Z', lastUsedAt: '2026-09-17T09:30:00Z', remainingRecoveryCodes: 8, policy: { mode: 'optional', required: false } }
const statusRequired = { ...statusEnabled, policy: { mode: 'required', required: true } }
const enrollment = {
  secret: 'JBSWY3DPEHPK3PXP',
  otpauthUrl: 'otpauth://totp/TunnelMesh:admin?secret=JBSWY3DPEHPK3PXP&issuer=TunnelMesh',
  recoveryCodes: ['tmrc-aaaa-1111', 'tmrc-bbbb-2222'],
  expiresAt: '2026-09-18T10:15:00Z',
}
const currentDevice = {
  id: 'dev-1', name: 'MacBook Pro', userAgent: 'Mozilla/5.0 (Macintosh)', ip: '203.0.113.7',
  trustedAt: '2026-09-10T08:00:00Z', expiresAt: '2026-12-09T08:00:00Z', lastSeenAt: '2026-09-18T07:00:00Z', current: true,
}
const otherDevice = { ...currentDevice, id: 'dev-2', name: 'ThinkPad', userAgent: 'Mozilla/5.0 (X11)', ip: '198.51.100.9', current: false }
const identity = {
  id: 'id-1', providerId: 'p-okta', providerName: 'Okta', subject: '00u1abc',
  username: 'ada', email: 'ada@example.com', displayName: 'Ada Lovelace',
  linkedAt: '2026-09-11T08:00:00Z', lastLoginAt: '2026-09-18T06:00:00Z',
}

// The three self-service reads always happen on mount; overrides win because
// `stubApi` matches the first declared stub for a method and path.
function stubSecurity(overrides: ApiStub[] = []) {
  return stubApi([
    ...overrides,
    { path: '/auth/mfa', data: statusNone },
    { path: '/auth/devices', data: { items: [] } },
    { path: '/auth/identities', data: { items: [] } },
  ])
}

async function mountSecurity() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const pinia = createPinia()
  setActivePinia(pinia)
  const app = createApp(AccountSecurity)
  app.use(pinia)
  app.use(i18n)
  app.mount(container)
  await flush()
  return { container, unmount: () => { app.unmount(); container.remove() } }
}

const t = (key: string) => i18n.global.t(key)

async function openEnrollment(container: HTMLElement) {
  await click('[data-test="mfa-enroll"]', container)
  await setValue('[data-test="enroll-password"]', container, 'local-password')
  await click('[data-test="mfa-enroll-submit"]', container)
}

beforeEach(() => {
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('account security two-factor enrollment', () => {
  it('reports the MFA status and offers enrollment while it is off', async () => {
    stubSecurity()
    const { container, unmount } = await mountSecurity()
    try {
      expect(container.querySelector('[data-test="mfa-status"]')?.textContent).toContain(t('security.mfa.statusNone'))
      expect(container.querySelector('[data-test="mfa-enroll"]')).not.toBeNull()
      expect(container.querySelector('[data-test="enrollment"]')).toBeNull()
      expect(container.querySelector('[data-test="mfa-disable"]')).toBeNull()
    } finally {
      unmount()
    }
  })

  it('enrolls with the current password and shows the otpauth URL with a copy action', async () => {
    const calls = stubSecurity([
      { method: 'POST', path: '/auth/mfa/enroll', data: enrollment },
    ])
    const { container, unmount } = await mountSecurity()
    try {
      await openEnrollment(container)
      expect(callsTo(calls, 'POST', '/auth/mfa/enroll')).toHaveLength(1)
      expect(callsTo(calls, 'POST', '/auth/mfa/enroll')[0]?.body).toEqual({ currentPassword: 'local-password' })
      expect(container.querySelector('[data-test="mfa-otpauth"]')?.textContent).toContain('otpauth://totp/TunnelMesh:admin')
      expect(container.querySelector('[data-test="mfa-secret"]')?.textContent).toContain('JBSWY3DPEHPK3PXP')
      expect(container.querySelector('[data-test="copy-otpauth"]')).not.toBeNull()
      expect(container.querySelector('[data-test="copy-secret"]')).not.toBeNull()
    } finally {
      unmount()
    }
  })

  it('keeps recovery codes behind an acknowledgement and blocks activation until it is given', async () => {
    stubSecurity([{ method: 'POST', path: '/auth/mfa/enroll', data: enrollment }])
    const { container, unmount } = await mountSecurity()
    try {
      await openEnrollment(container)
      expect(container.querySelector('[data-test="recovery-codes"]')).toBeNull()
      expect(container.textContent).not.toContain('tmrc-aaaa-1111')
      await setValue('[data-test="mfa-enable-code"]', container, '123456')
      expect((container.querySelector('[data-test="mfa-enable"]') as HTMLButtonElement).disabled).toBe(true)

      await check('[data-test="recovery-ack"]', container)
      const codes = container.querySelector('[data-test="recovery-codes"]')
      expect(codes?.textContent).toContain('tmrc-aaaa-1111')
      expect(codes?.textContent).toContain('tmrc-bbbb-2222')
      expect((container.querySelector('[data-test="mfa-enable"]') as HTMLButtonElement).disabled).toBe(false)
    } finally {
      unmount()
    }
  })

  it('activates MFA and then forgets the one-time material', async () => {
    const calls = stubSecurity([
      { path: '/auth/mfa', sequence: [statusNone, statusEnabled] },
      { method: 'POST', path: '/auth/mfa/enroll', data: enrollment },
      { method: 'POST', path: '/auth/mfa/enable', data: { status: 'enabled', enabledAt: '2026-09-18T10:20:00Z' } },
    ])
    const { container, unmount } = await mountSecurity()
    try {
      await openEnrollment(container)
      await check('[data-test="recovery-ack"]', container)
      await setValue('[data-test="mfa-enable-code"]', container, '123456')
      await click('[data-test="mfa-enable"]', container)

      expect(callsTo(calls, 'POST', '/auth/mfa/enable')[0]?.body).toEqual({ code: '123456' })
      expect(callsTo(calls, 'GET', '/auth/mfa')).toHaveLength(2)
      expect(container.querySelector('[data-test="mfa-status"]')?.textContent).toContain(t('security.mfa.statusEnabled'))
      expect(container.querySelector('[data-test="remaining-codes"]')?.textContent).toContain('8')
      // The secret and the codes are shown exactly once and never re-rendered.
      expect(container.querySelector('[data-test="recovery-codes"]')).toBeNull()
      expect(container.querySelector('[data-test="mfa-otpauth"]')).toBeNull()
      expect(container.textContent).not.toContain('JBSWY3DPEHPK3PXP')
      expect(container.textContent).not.toContain('tmrc-aaaa-1111')
      expect(container.querySelector('[data-test="mfa-disable"]')).not.toBeNull()
    } finally {
      unmount()
    }
  })

  it('explains why MFA cannot be disabled while policy requires it', async () => {
    const calls = stubSecurity([
      { path: '/auth/mfa', data: statusRequired },
      { method: 'DELETE', path: '/auth/mfa', status: 409, data: { error: 'mfa_required_by_policy' } },
    ])
    const { container, unmount } = await mountSecurity()
    try {
      expect(container.textContent).toContain(t('security.mfa.policyRequiredHint'))
      await click('[data-test="mfa-disable"]', container)
      await setValue('[data-test="mfa-disable-code"]', container, '123456')
      await click('[data-test="mfa-disable-confirm"]', container)

      expect(callsTo(calls, 'DELETE', '/auth/mfa')[0]?.body).toEqual({ code: '123456' })
      expect(container.querySelector('[data-test="mfa-error"]')?.textContent).toContain(t('security.mfa.requiredByPolicy'))
      expect(container.querySelector('[data-test="mfa-status"]')?.textContent).toContain(t('security.mfa.statusEnabled'))
    } finally {
      unmount()
    }
  })

  it('disables MFA with a current code and returns to the enrollment state', async () => {
    stubSecurity([
      { path: '/auth/mfa', sequence: [statusEnabled, statusNone] },
      { method: 'DELETE', path: '/auth/mfa', data: { status: 'disabled' } },
    ])
    const { container, unmount } = await mountSecurity()
    try {
      await click('[data-test="mfa-disable"]', container)
      await setValue('[data-test="mfa-disable-code"]', container, 'tmrc-aaaa-1111')
      await click('[data-test="mfa-disable-confirm"]', container)

      expect(container.querySelector('[data-test="mfa-status"]')?.textContent).toContain(t('security.mfa.statusNone'))
      expect(container.querySelector('[data-test="mfa-enroll"]')).not.toBeNull()
      expect(container.querySelector('[data-test="mfa-disable"]')).toBeNull()
    } finally {
      unmount()
    }
  })

  it('regenerates recovery codes and refreshes the remaining count', async () => {
    const calls = stubSecurity([
      { path: '/auth/mfa', sequence: [statusEnabled, { ...statusEnabled, remainingRecoveryCodes: 10 }] },
      { method: 'POST', path: '/auth/mfa/recovery-codes', data: { recoveryCodes: ['tmrc-new-1', 'tmrc-new-2'] } },
    ])
    const { container, unmount } = await mountSecurity()
    try {
      await click('[data-test="mfa-regenerate"]', container)
      await setValue('[data-test="mfa-regenerate-code"]', container, '123456')
      await click('[data-test="mfa-regenerate-confirm"]', container)

      expect(callsTo(calls, 'POST', '/auth/mfa/recovery-codes')[0]?.body).toEqual({ code: '123456' })
      await check('[data-test="recovery-ack"]', container)
      expect(container.querySelector('[data-test="recovery-codes"]')?.textContent).toContain('tmrc-new-1')
      expect(container.querySelector('[data-test="remaining-codes"]')?.textContent).toContain('10')
    } finally {
      unmount()
    }
  })

  it('never writes enrollment material to browser storage', async () => {
    stubSecurity([{ method: 'POST', path: '/auth/mfa/enroll', data: enrollment }])
    const { container, unmount } = await mountSecurity()
    try {
      await openEnrollment(container)
      await check('[data-test="recovery-ack"]', container)
      expect(Object.keys(localStorage)).toEqual([])
      const source = readFileSync('src/views/AccountSecurity.vue', 'utf8')
      expect(source).not.toContain('localStorage')
      expect(source).not.toContain('sessionStorage')
    } finally {
      unmount()
    }
  })
})

describe('account security trusted devices and linked identities', () => {
  it('lists devices, marks the current one, renames and revokes', async () => {
    const calls = stubSecurity([
      { path: '/auth/devices', sequence: [{ items: [currentDevice, otherDevice] }, { items: [currentDevice] }] },
      { method: 'PATCH', path: '/auth/devices/dev-2', data: { id: 'dev-2', name: 'Office desktop' } },
      { method: 'DELETE', path: '/auth/devices/dev-2', data: { revoked: true } },
    ])
    const { container, unmount } = await mountSecurity()
    try {
      expect(container.textContent).toContain('MacBook Pro')
      expect(container.textContent).toContain('203.0.113.7')
      expect(container.querySelector('[data-test="device-current-dev-1"]')?.textContent).toContain(t('security.devices.current'))
      expect(container.querySelector('[data-test="device-current-dev-2"]')).toBeNull()

      await click('[data-test="device-rename-dev-2"]', container)
      await setValue('[data-test="device-name-dev-2"]', container, 'Office desktop')
      await click('[data-test="device-rename-save-dev-2"]', container)
      expect(callsTo(calls, 'PATCH', '/auth/devices/dev-2')[0]?.body).toEqual({ name: 'Office desktop' })

      await click('[data-test="device-revoke-dev-2"]', container)
      expect(callsTo(calls, 'DELETE', '/auth/devices/dev-2')).toHaveLength(1)
      await flush()
      expect(container.textContent).not.toContain('ThinkPad')
    } finally {
      unmount()
    }
  })

  it('reports a missing device instead of silently reloading', async () => {
    stubSecurity([
      { path: '/auth/devices', data: { items: [otherDevice] } },
      { method: 'DELETE', path: '/auth/devices/dev-2', status: 404, data: { error: 'device_not_found' } },
    ])
    const { container, unmount } = await mountSecurity()
    try {
      await click('[data-test="device-revoke-dev-2"]', container)
      expect(container.querySelector('[data-test="device-error"]')?.textContent).toContain(t('security.devices.notFound'))
    } finally {
      unmount()
    }
  })

  it('lists linked identities and unlinks one', async () => {
    const calls = stubSecurity([
      { path: '/auth/identities', sequence: [{ items: [identity] }, { items: [] }] },
      { method: 'DELETE', path: '/auth/identities/id-1', data: { unlinked: true } },
    ])
    const { container, unmount } = await mountSecurity()
    try {
      expect(container.textContent).toContain('Okta')
      expect(container.textContent).toContain('ada@example.com')
      expect(container.textContent).toContain('00u1abc')

      await click('[data-test="identity-unlink-id-1"]', container)
      expect(callsTo(calls, 'DELETE', '/auth/identities/id-1')).toHaveLength(1)
      await flush()
      expect(container.textContent).toContain(t('security.identities.empty'))
    } finally {
      unmount()
    }
  })

  it('surfaces the conflict when an identity is the only way to sign in', async () => {
    stubSecurity([
      { path: '/auth/identities', data: { items: [identity] } },
      { method: 'DELETE', path: '/auth/identities/id-1', status: 409, data: { error: 'identity_required_for_login' } },
    ])
    const { container, unmount } = await mountSecurity()
    try {
      await click('[data-test="identity-unlink-id-1"]', container)
      expect(container.querySelector('[data-test="identity-error"]')?.textContent).toContain(t('security.identities.requiredForLogin'))
      expect(container.textContent).toContain('ada@example.com')
    } finally {
      unmount()
    }
  })
})
