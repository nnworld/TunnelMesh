import { beforeEach, describe, expect, it, vi } from 'vitest'
import { changePassword, createUser, deleteUser, listUsers, resetUserPassword, restoreUser, updateUserMFARequired, updateUserStatus } from '../api/client'
import { listUserDevices, resetUserMFA, revokeUserDevice } from '../api/auth'
import { readFileSync } from 'node:fs'

describe('account management client and views', () => {
  beforeEach(() => vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { items: [] } }) })))

  it('uses the approved account API methods', async () => {
    await listUsers({ status: 'deleted', limit: 20 }); await createUser('operator'); await updateUserStatus('u1', true)
    await resetUserPassword('u1'); await deleteUser('u1'); await restoreUser('u1'); await changePassword('old', 'new-password-12')
    const calls = vi.mocked(fetch).mock.calls.map(([url, init]) => [url, init?.method || 'GET'])
    expect(calls).toEqual([
      ['/api/v1/users?status=deleted&limit=20', 'GET'], ['/api/v1/users', 'POST'], ['/api/v1/users/u1', 'PATCH'],
      ['/api/v1/users/u1/reset-password', 'POST'], ['/api/v1/users/u1', 'DELETE'], ['/api/v1/users/u1/restore', 'POST'], ['/api/v1/auth/password', 'PUT'],
    ])
  })

  it('clears one-time password state and keeps password change in-session', () => {
    const users = readFileSync('src/views/Users.vue', 'utf8')
    const security = readFileSync('src/views/AccountSecurity.vue', 'utf8')
    expect(users).toContain('clearTemporaryPassword')
    expect(users).toContain('restoreUser')
    expect(security).toContain('changePassword')
    expect(security).not.toContain('logout(')
  })
})

describe('administrator MFA and device support', () => {
  beforeEach(() => vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { items: [] } }) })))

  // Resetting MFA and revoking a trusted device are audited admin mutations, so
  // they must carry an idempotency key like every other admin write.
  it('uses the approved admin MFA, device, and account-flag endpoints', async () => {
    await updateUserMFARequired('u1', true)
    await resetUserMFA('u1', 'reset-key-1')
    await listUserDevices('u1', { limit: 10 })
    await revokeUserDevice('u1', 'd1', 'revoke-key-1')

    const calls = vi.mocked(fetch).mock.calls.map(([url, init]) => [url, init?.method || 'GET', new Headers(init?.headers).get('Idempotency-Key')])
    expect(calls).toEqual([
      ['/api/v1/users/u1', 'PATCH', null],
      ['/api/v1/users/u1/mfa/reset', 'POST', 'reset-key-1'],
      ['/api/v1/users/u1/devices?limit=10', 'GET', null],
      ['/api/v1/users/u1/devices/d1', 'DELETE', 'revoke-key-1'],
    ])
    expect(JSON.parse(String(vi.mocked(fetch).mock.calls[0][1]?.body))).toEqual({ mfaRequired: true })
  })

  it('exposes an MFA requirement switch and a confirmed MFA reset in the users view', () => {
    const users = readFileSync('src/views/Users.vue', 'utf8')
    for (const marker of [
      'updateUserMFARequired', 'resetUserMFA', "t('users.mfaRequired')", "t('users.resetMFA')",
      "t('users.confirmResetMFA')", 'toggleMfaRequired', 'resetMfa(row)', 'crypto.randomUUID()',
    ]) expect(users, marker).toContain(marker)
    // A destructive reset must be confirmed before it reaches the API.
    expect(users.indexOf('ElMessageBox.confirm')).toBeLessThan(users.indexOf('resetUserMFA('))
    expect(users).not.toContain('localStorage')
  })
})
