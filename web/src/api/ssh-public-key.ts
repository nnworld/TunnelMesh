import { api } from './client'

export type ExtractedSSHPublicKey = {
  publicKey: string
  fingerprint: string
}

export function extractSSHPublicKey(privateKey: string, passphrase?: string): Promise<ExtractedSSHPublicKey> {
  return api<ExtractedSSHPublicKey>('/credentials/extract-ssh-public-key', {
    method: 'POST',
    body: JSON.stringify(passphrase ? { privateKey, passphrase } : { privateKey }),
  })
}
