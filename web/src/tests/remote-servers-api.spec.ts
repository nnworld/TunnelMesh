import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createRemoteServer, deleteRemoteServer, getRemoteServer, listRemoteServers, restoreRemoteServer, updateRemoteServer } from '../api/remote-servers'

describe('remote servers API', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: {} }) }))
  })

  it('lists remote servers with encoded filters', async () => {
    await listRemoteServers({ agentId: 'agent/one', status: 'disabled', keyword: 'web server', limit: 20 })

    expect(fetch).toHaveBeenCalledWith('/api/v1/remote-servers?agentId=agent%2Fone&status=disabled&keyword=web+server&limit=20', expect.anything())
  })

  it('creates and replaces remote servers with idempotency keys', async () => {
    const input = { name: 'web-1', host: '10.0.0.8', port: 22, defaultUsername: 'deploy', agentId: 'agent-a', enabled: true }
    await createRemoteServer(input, 'remote-create')
    await updateRemoteServer('server/one', input, 'remote-replace')

    expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/remote-servers', expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ 'Idempotency-Key': 'remote-create' }), body: JSON.stringify(input),
    }))
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/remote-servers/server%2Fone', expect.objectContaining({
      method: 'PUT', headers: expect.objectContaining({ 'Idempotency-Key': 'remote-replace' }), body: JSON.stringify(input),
    }))
  })

  it('reads, deletes, and restores remote servers', async () => {
    await getRemoteServer('server/one')
    await deleteRemoteServer('server/one')
    await restoreRemoteServer('server/one')

    expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/remote-servers/server%2Fone', expect.anything())
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/remote-servers/server%2Fone', expect.objectContaining({ method: 'DELETE' }))
    expect(fetch).toHaveBeenNthCalledWith(3, '/api/v1/remote-servers/server%2Fone/restore', expect.objectContaining({ method: 'POST' }))
  })
})
