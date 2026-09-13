import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createCredential, deleteCredential, extractSSHPublicKey, getCredential, listCredentials, restoreCredential, updateCredential } from '../api/credentials'

describe('credentials API', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: {} }) }))
  })

  it('lists credentials with encoded filters', async () => {
    await listCredentials({ status: 'deleted', keyword: 'deploy key', limit: 20 })

    expect(fetch).toHaveBeenCalledWith('/api/v1/credentials?status=deleted&keyword=deploy+key&limit=20', expect.anything())
  })

  it('creates and replaces credentials with idempotency keys', async () => {
    const input = { name: 'deploy', publicKey: 'ssh-ed25519 AAAA', enabled: true }
    await createCredential(input, 'credential-create')
    await updateCredential('credential/one', input, 'credential-replace')

    expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/credentials', expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ 'Idempotency-Key': 'credential-create' }), body: JSON.stringify(input),
    }))
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/credentials/credential%2Fone', expect.objectContaining({
      method: 'PUT', headers: expect.objectContaining({ 'Idempotency-Key': 'credential-replace' }), body: JSON.stringify(input),
    }))
  })

  it('reads, deletes, and restores credentials', async () => {
    await getCredential('credential/one')
    await deleteCredential('credential/one')
    await restoreCredential('credential/one')

    expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/credentials/credential%2Fone', expect.anything())
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/credentials/credential%2Fone', expect.objectContaining({ method: 'DELETE' }))
    expect(fetch).toHaveBeenNthCalledWith(3, '/api/v1/credentials/credential%2Fone/restore', expect.objectContaining({ method: 'POST' }))
  })

  it('uploads a private key once for server-side public-key extraction', async () => {
    await extractSSHPublicKey('private-key-material', 'passphrase')

    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('/api/v1/credentials/extract-ssh-public-key')
    expect(init?.method).toBe('POST')
    expect(init?.body).toBe(JSON.stringify({ privateKey: 'private-key-material', passphrase: 'passphrase' }))
  })
})
