import { beforeEach, describe, expect, it, vi } from 'vitest'
import { changePassword, createUser, deleteUser, listUsers, resetUserPassword, restoreUser, updateUserStatus } from '../api/client'
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
