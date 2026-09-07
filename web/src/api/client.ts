export type APIResponse<T> = { code: number; msg: string; data: T }

export type Agent = { id: string; name: string; enabled: boolean; ownerUserId?: string; capabilities?: string[] }
export type AgentMetadataItem = { name: string; source: 'file' | 'env'; value?: string; redacted: boolean }
export type AgentMetadata = {
  agentId: string; nodeId: string; epoch: number; revision: number; stale: boolean
  reportedAt: string; updatedAt: string; items: AgentMetadataItem[]
}

export type TokenType = 'agent' | 'client' | 'server_node'
export type TokenScope = { agentIds?: string[]; protocols?: string[]; targetCIDRs?: string[]; targetPorts?: number[] }
export type ServiceToken = {
  id: string; type: TokenType; ownerUserId: string; agentId?: string; nodeId?: string
  prefix: string; scope: TokenScope; status: string; expiresAt?: string; revokedAt?: string
  lastUsedAt?: string; createdAt: string; updatedAt: string; secret?: string; replayed?: boolean
}

export type TokenPage = { items: ServiceToken[]; nextCursor?: string; hasMore?: boolean }

let token = localStorage.getItem('tunnelmesh_token') || ''
export function setToken(value: string) { token = value; value ? localStorage.setItem('tunnelmesh_token', value) : localStorage.removeItem('tunnelmesh_token') }
export function getToken() { return token }

export function getAgents() { return api<{items: Agent[]; nextCursor?: string}>('/agents') }
export function listTokens(params: { type?: TokenType; cursor?: string } = {}) {
  const query = new URLSearchParams()
  if (params.type) query.set('type', params.type)
  if (params.cursor) query.set('cursor', params.cursor)
  const suffix = query.toString() ? `?${query.toString()}` : ''
  return api<TokenPage>(`/tokens${suffix}`)
}
export function createToken(input: { type: TokenType; agentId?: string; nodeId?: string; scope?: TokenScope; expiresAt?: string }, idempotencyKey: string) {
  return api<ServiceToken>('/tokens', { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify(input) })
}
export function rotateToken(id: string, idempotencyKey: string) {
  return api<ServiceToken>(`/tokens/${encodeURIComponent(id)}/rotate`, { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey } })
}
export function revokeToken(id: string) {
  return api<ServiceToken>(`/tokens/${encodeURIComponent(id)}/revoke`, { method: 'POST' })
}
export function revealToken(id: string, confirmation: string, idempotencyKey: string) {
  return api<{ tokenId: string; type: TokenType; secret: string; expiresAt?: string; revealedAt: string; oneTime: boolean }>(`/tokens/${encodeURIComponent(id)}/reveal`, {
    method: 'POST', headers: { 'X-Token-Reveal-Confirm': confirmation, 'Idempotency-Key': idempotencyKey }, body: JSON.stringify({ acknowledgeRisk: true })
  })
}
export function getAgentMetadata(agentId: string, includeStale = false) {
  const query = includeStale ? '?includeStale=true' : ''
  return api<AgentMetadata>(`/agents/${encodeURIComponent(agentId)}/metadata${query}`)
}

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  if (token) headers.set('Authorization', `Bearer ${token}`)
  const response = await fetch(`/api/v1${path}`, {...init, headers})
  const payload = await response.json().catch(() => ({msg: response.statusText}))
  if (!response.ok) throw new Error(payload.msg || 'request failed')
  return payload.data as T
}
