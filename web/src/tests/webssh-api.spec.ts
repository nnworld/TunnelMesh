import { beforeEach, describe, expect, it, vi } from 'vitest'
import { closeWebSSHSession, createWebSSHSession, getWebSSHSession, listWebSSHSessions } from '../api/webssh'

describe('WebSSH API', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: {} }) }))
  })

  it('creates a one-time ticket for a remote server', async () => {
    await createWebSSHSession('server/one', { username: 'deploy', credentialId: 'credential/one' }, 'webssh-create')

    expect(fetch).toHaveBeenCalledWith('/api/v1/remote-servers/server%2Fone/ssh-sessions', expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ 'Idempotency-Key': 'webssh-create' }), body: JSON.stringify({ username: 'deploy', credentialId: 'credential/one' }),
    }))
  })

  it('reads and closes a session', async () => {
    await getWebSSHSession('session/one')
    await closeWebSSHSession('session/one')

    expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/ssh-sessions/session%2Fone', expect.anything())
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/ssh-sessions/session%2Fone', expect.objectContaining({ method: 'DELETE' }))
  })

  it('lists the caller own active sessions with cursor pagination', async () => {
    await listWebSSHSessions({ limit: 50 })
    await listWebSSHSessions({ cursor: 'cursor/one', limit: 20 })
    await listWebSSHSessions()

    expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/ssh-sessions?limit=50', expect.anything())
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/ssh-sessions?cursor=cursor%2Fone&limit=20', expect.anything())
    expect(fetch).toHaveBeenNthCalledWith(3, '/api/v1/ssh-sessions', expect.anything())
  })

  it('returns the session page payload', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        code: 200,
        msg: 'OK',
        data: {
          items: [{
            id: 'webssh-1', remoteServerId: 'server-a', agentId: 'agent-a', status: 'active',
            createdAt: '2026-09-12T00:00:00Z', expiresAt: '2026-09-12T08:00:00Z',
          }],
          nextCursor: 'next', hasMore: true,
        },
      }),
    }))

    const page = await listWebSSHSessions({ limit: 1 })

    expect(page.items.map(item => item.id)).toEqual(['webssh-1'])
    expect(page.nextCursor).toBe('next')
    expect(page.hasMore).toBe(true)
  })
})
