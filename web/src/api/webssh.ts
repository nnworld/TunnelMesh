import { api } from './client'

export type WebSSHSessionStatus = 'pending' | 'active' | 'closed' | 'expired'

export type WebSSHTicket = {
  sessionId: string
  ticket: string
  websocketPath: string
  // One-time authentication material for a credential that stores a secret.
  // The server sends it only on the first create response, never on a replay,
  // and it is dropped from memory when the session ends.
  auth?: WebSSHAuth
}

export type WebSSHAuth = {
  kind: 'password' | 'private_key'
  credentialId: string
  password?: string
  privateKey?: string
  passphrase?: string
}

export type WebSSHSession = {
  id: string
  remoteServerId: string
  agentId: string
  status: WebSSHSessionStatus
  createdAt: string
  expiresAt: string
  connectedAt?: string | null
  closedAt?: string | null
  closeReason?: string
}

export type CreateWebSSHSessionInput = {
  username: string
  credentialId?: string
}

export type WebSSHSessionPage = { items: WebSSHSession[]; nextCursor?: string; hasMore?: boolean }
export type WebSSHSessionListParams = { cursor?: string; limit?: number }

// listWebSSHSessions returns only the caller's own active sessions. The Server
// enforces the owner scope, so the console cannot list another account's shells;
// it exists so a stuck active-session quota can be released from the UI.
export function listWebSSHSessions(params: WebSSHSessionListParams = {}) {
  const query = new URLSearchParams()
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  return api<WebSSHSessionPage>(`/ssh-sessions${query.size ? `?${query}` : ''}`)
}

export function createWebSSHSession(remoteServerId: string, input: CreateWebSSHSessionInput, idempotencyKey: string) {
  return api<WebSSHTicket>(`/remote-servers/${encodeURIComponent(remoteServerId)}/ssh-sessions`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

export function getWebSSHSession(sessionId: string) {
  return api<WebSSHSession>(`/ssh-sessions/${encodeURIComponent(sessionId)}`)
}

export function closeWebSSHSession(sessionId: string) {
  return api<WebSSHSession>(`/ssh-sessions/${encodeURIComponent(sessionId)}`, { method: 'DELETE' })
}
