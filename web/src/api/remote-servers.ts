import { api } from './client'
import type { CredentialType } from './credentials'

export type RemoteServerStatus = 'enabled' | 'disabled' | 'deleted'

export type RemoteServer = {
  id: string
  ownerUserId: string
  name: string
  host: string
  port: number
  defaultUsername: string
  credentialId: string
  credentialName?: string
  // Capability flags used to decide whether SSH/SFTP can authenticate without
  // prompting. Both are null when no credential is bound.
  credentialType?: CredentialType | null
  credentialHasSecret?: boolean | null
  agentId: string
  agentName?: string
  enabled: boolean
  status: RemoteServerStatus
  agentOnline?: boolean
  lastConnectedAt?: string | null
  lastResult?: string | null
  lastErrorClass?: string | null
  deletedAt?: string | null
  createdAt: string
  updatedAt: string
}

export type RemoteServerPage = { items: RemoteServer[]; nextCursor?: string; hasMore?: boolean }
export type RemoteServerListParams = {
  agentId?: string
  status?: RemoteServerStatus | 'all'
  keyword?: string
  cursor?: string
  limit?: number
}
export type RemoteServerInput = {
  name: string
  host: string
  port: number
  defaultUsername: string
  credentialId?: string
  agentId: string
  enabled: boolean
}

export function listRemoteServers(params: RemoteServerListParams = {}) {
  const query = new URLSearchParams()
  if (params.agentId) query.set('agentId', params.agentId)
  if (params.status && params.status !== 'all') query.set('status', params.status)
  if (params.keyword) query.set('keyword', params.keyword)
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  return api<RemoteServerPage>(`/remote-servers${query.size ? `?${query}` : ''}`)
}

export function createRemoteServer(input: RemoteServerInput, idempotencyKey: string) {
  return api<RemoteServer>('/remote-servers', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

export function getRemoteServer(id: string) {
  return api<RemoteServer>(`/remote-servers/${encodeURIComponent(id)}`)
}

export function updateRemoteServer(id: string, input: RemoteServerInput, idempotencyKey: string) {
  return api<RemoteServer>(`/remote-servers/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

export function deleteRemoteServer(id: string) {
  return api<RemoteServer>(`/remote-servers/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function restoreRemoteServer(id: string) {
  return api<RemoteServer>(`/remote-servers/${encodeURIComponent(id)}/restore`, { method: 'POST' })
}
