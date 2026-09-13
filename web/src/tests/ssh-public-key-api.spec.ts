import { beforeEach, describe, expect, it, vi } from 'vitest'
import { extractSSHPublicKey } from '../api/ssh-public-key'

describe('SSH public-key extraction API', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { publicKey: 'ssh-ed25519 AAAA', fingerprint: 'SHA256:test' } }) }))
  })

  it('extracts a public key without an optional passphrase', async () => {
    const result = await extractSSHPublicKey('private-key')

    expect(result).toEqual({ publicKey: 'ssh-ed25519 AAAA', fingerprint: 'SHA256:test' })
    expect(fetch).toHaveBeenCalledWith('/api/v1/credentials/extract-ssh-public-key', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ privateKey: 'private-key' }),
    }))
  })
})
