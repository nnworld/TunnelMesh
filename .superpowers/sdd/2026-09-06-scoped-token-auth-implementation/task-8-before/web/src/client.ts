export type APIResponse<T> = { code: number; msg: string; data: T }

export type Agent = { id: string; name: string; enabled: boolean; ownerUserId?: string; capabilities?: string[] }
export type AgentMetadataItem = { name: string; source: 'file' | 'env'; value?: string; redacted: boolean }
export type AgentMetadata = {
  agentId: string; nodeId: string; epoch: number; revision: number; stale: boolean
  reportedAt: string; updatedAt: string; items: AgentMetadataItem[]
}

let token = localStorage.getItem('tunnelmesh_token') || ''
export function setToken(value: string) { token = value; value ? localStorage.setItem('tunnelmesh_token', value) : localStorage.removeItem('tunnelmesh_token') }
export function getToken() { return token }

export function getAgents() { return api<{items: Agent[]; nextCursor?: string}>('/agents') }
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
