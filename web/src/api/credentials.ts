import { api } from './client'
import { extractSSHPublicKey } from './ssh-public-key'

export type CredentialType = 'ssh_public_key' | 'password'
export type ResourceStatus = 'active' | 'deleted'

// Write-only secret material. The server seals it into one AES-GCM blob and
// answers with hasSecret only, so the value can never be read back.
export type CredentialSecretInput = {
  password?: string
  privateKey?: string
  passphrase?: string
}

export type Credential = {
  id: string
  ownerUserId: string
  name: string
  type: CredentialType
  publicKey: string
  fingerprint: string
  enabled: boolean
  hasSecret?: boolean
  status: ResourceStatus
  deletedAt?: string | null
  createdAt: string
  updatedAt: string
}

export type CredentialPage = { items: Credential[]; nextCursor?: string; hasMore?: boolean }
export type CredentialListParams = {
  type?: CredentialType
  status?: ResourceStatus | 'all'
  keyword?: string
  cursor?: string
  limit?: number
}
export type CredentialInput = {
  name: string
  type: CredentialType
  publicKey: string
  enabled: boolean
  secret?: CredentialSecretInput
}

export function listCredentials(params: CredentialListParams = {}) {
  const query = new URLSearchParams()
  if (params.type) query.set('type', params.type)
  if (params.status && params.status !== 'all') query.set('status', params.status)
  if (params.keyword) query.set('keyword', params.keyword)
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  return api<CredentialPage>(`/credentials${query.size ? `?${query}` : ''}`)
}

export function createCredential(input: CredentialInput, idempotencyKey: string) {
  return api<Credential>('/credentials', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

export function getCredential(id: string) {
  return api<Credential>(`/credentials/${encodeURIComponent(id)}`)
}

export function updateCredential(id: string, input: CredentialInput, idempotencyKey: string) {
  return api<Credential>(`/credentials/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

export function deleteCredential(id: string) {
  return api<Credential>(`/credentials/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function restoreCredential(id: string) {
  return api<Credential>(`/credentials/${encodeURIComponent(id)}/restore`, { method: 'POST' })
}

export { extractSSHPublicKey }
