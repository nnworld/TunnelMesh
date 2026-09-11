import { describe, expect, it, vi } from 'vitest'
import { closeClientConnection, getClientDetail, getDownloads, listClientConnections, listClients } from '../api/client'

describe('clients API', () => {
  it('lists clients with encoded filters', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { items: [] } }) })
    vi.stubGlobal('fetch', fetchMock)

    await listClients({ ownerUserId: 'user 1', status: 'online', limit: 50 })

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/clients?ownerUserId=user+1&status=online&limit=50', expect.anything())
  })

  it('reads client detail and connections', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { connections: [] } }) })
    vi.stubGlobal('fetch', fetchMock)

    await getClientDetail('client/one')
    await listClientConnections('client/one')

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/clients/client%2Fone', expect.anything())
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/clients/client%2Fone/connections', expect.anything())
  })

  it('closes one exact client connection', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { closed: true } }) })
    vi.stubGlobal('fetch', fetchMock)

    await closeClientConnection('client/one', 'client_connection 1', 7)

    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('/api/v1/clients/client%2Fone/connections/client_connection%201?connectionEpoch=7')
    expect(init?.method).toBe('DELETE')
  })

  it('reads the current release downloads', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { version: 'v1.2.3' } }) })
    vi.stubGlobal('fetch', fetchMock)

    const result = await getDownloads()

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/downloads', expect.anything())
    expect(result.version).toBe('v1.2.3')
  })
})
