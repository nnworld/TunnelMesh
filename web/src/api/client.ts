export type APIResponse<T> = { code: number; msg: string; data: T }

export type Agent = { id: string; name: string; enabled: boolean; status: 'online' | 'offline'; ownerUserId?: string; capabilities?: string[] }
export type AgentCreateInput = { name: string; enabled: boolean; capabilities?: string[] }
export type AgentMetadataItem = { name: string; source: 'file' | 'env'; value?: string; redacted: boolean }
export type AgentMetadataInstance = {
  instanceId: string; nodeId: string; epoch: number; revision: number; stale: boolean
  reportedAt: string; updatedAt: string; items: AgentMetadataItem[]; connectionCount: number
}
export type AgentConnection = {
  agentId: string; instanceId: string; nodeId: string; connectionId: string
  epoch: number; connectionEpoch: number; serverNodeId?: string; healthy: boolean
  activeStreams: number; lastHeartbeatAt: string
}
export type AgentMetadata = {
  agentId: string; instanceId?: string; nodeId: string; epoch: number; revision: number; stale: boolean
  reportedAt: string; updatedAt: string; items: AgentMetadataItem[]
  instances?: AgentMetadataInstance[]; connections?: AgentConnection[]
}
export type AgentPolicy = {
  id: string
  agentId: string
  protocol: string
  targetHost: string
  targetPort: number
  allowedCIDRs: string[]
  allowedPorts: number[]
  deletedAt?: string | null
  createdAt: string
  updatedAt: string
}
export type AgentPolicyInput = {
  protocol: string
  targetHost: string
  targetPort: number
  allowedCIDRs: string[]
  allowedPorts: number[]
}
export type AgentPolicyPage = { items: AgentPolicy[]; nextCursor?: string; hasMore?: boolean }
export type ClusterAgentConnection = {
  agentId: string
  instanceId: string
  connectionId: string
  connectionEpoch: number
  serverNodeId: string
  serverNodeEpoch: number
  serverNodeAddress?: string
  healthy: boolean
  activeStreams: number
  healthScore: number
  lastHeartbeatAt?: string
  leaseExpiresAt: string
  local: boolean
}

export type ManagedRouteAuthMode = 'none' | 'basic'

export type ManagedRoute = {
  id: string
  agentId: string
  protocol: string
  domain: string
  pathPrefix: string
  targetHost: string
  targetPort: number
  hostHeader?: string
  targetScheme?: string
  tlsServerName?: string
  publicPort?: number
  status: string
  createdAt: string
  updatedAt: string
  // Proxy-entry policy. Present only on protocol=http-proxy routes, whose
  // targetHost/targetPort are the server-owned * / 0 sentinel.
  authMode?: ManagedRouteAuthMode
  credentialId?: string
  sourceCIDRs?: string[]
  targetCIDRs?: string[]
  targetPorts?: number[]
  allowPrivateTargets?: boolean
  maxConcurrentTunnels?: number
  description?: string
  // Read-only and derived server-side from the stored domain.
  proxyUrl?: string
}
export type ManagedRouteCreateInput = {
  agentId: string
  domain: string
  pathPrefix: string
  protocol: string
  targetHost: string
  targetPort: number
  hostHeader?: string
  targetScheme?: string
  tlsServerName?: string
  authMode?: ManagedRouteAuthMode
  credentialId?: string
  sourceCIDRs?: string[]
  targetCIDRs?: string[]
  targetPorts?: number[]
  allowPrivateTargets?: boolean
  maxConcurrentTunnels?: number
  description?: string
}
export type ManagedRouteUpdateInput = Partial<ManagedRouteCreateInput & { status: string }>
export type ManagedRoutePage = { items: ManagedRoute[]; nextCursor?: string; hasMore?: boolean }
export type AuditLog = {
  id: string
  actorUserId?: string
  action: string
  resourceType: string
  resourceId?: string
  details?: Record<string, unknown> | null
  createdAt: string
}
export type AuditLogPage = { items: AuditLog[]; nextCursor?: string; hasMore?: boolean }
export type AuditLogFilter = {
  actorUserId?: string
  action?: string
  resourceType?: string
  resourceId?: string
  createdFrom?: string
  createdTo?: string
}

export type TokenType = 'agent' | 'client' | 'server_node'
export type TokenScope = { agentIds?: string[]; serverNodeIds?: string[]; protocols?: string[]; targetCIDRs?: string[]; targetPorts?: number[] }
export type TokenScopePatch = Partial<Pick<TokenScope, 'agentIds' | 'serverNodeIds' | 'protocols' | 'targetCIDRs' | 'targetPorts'>>
export type ServiceToken = {
  id: string; type: TokenType; ownerUserId: string; agentId?: string; nodeId?: string
  prefix: string; scope: TokenScope; status: string; expiresAt?: string; revokedAt?: string
  lastUsedAt?: string; createdAt: string; updatedAt: string; secret?: string; replayed?: boolean
}

export type TokenPage = { items: ServiceToken[]; nextCursor?: string; hasMore?: boolean }
// authSource records how the account can sign in; mfaRequired is the per-account
// override that wins over the global auth policy.
export type AuthSource = 'local' | 'oidc' | 'mixed'
export type UserAccount = { id: string; username: string; role: 'user'; disabled: boolean; authSource?: AuthSource; mfaRequired?: boolean; deletedAt?: string | null; createdAt: string; updatedAt: string }
export type UserPage = { items: UserAccount[]; nextCursor?: string; hasMore?: boolean }
export type TemporaryPasswordResult = { user: UserAccount; temporaryPassword: string }
export type DashboardSummary = { agentsTotal:number; agentsOnline:number; activeTunnels:number; managedRoutes:number; validServiceTokens:number; recentEvents:Array<{id:string;action:string;resourceType:string;resourceId:string;createdAt:string}> }
export type ServerNodeStatus = 'online' | 'offline' | 'disabled' | 'deleted'
export type ServerNode = {
  id: string
  name: string
  address: string
  epoch: number
  enabled: boolean
  deletedAt?: string | null
  lastSeenAt?: string | null
  expiresAt?: string | null
  createdAt: string
  updatedAt: string
  status: ServerNodeStatus
  activeConnections: number
  activeStreams: number
  healthScore: number
}
export type ServerNodePage = { items: ServerNode[]; nextCursor?: string; hasMore?: boolean }
export type ServerNodeUpdateInput = { name?: string; enabled?: boolean }
export type DownloadAsset = { platform: string; archive: string; url: string }
export type DownloadInfo = {
  version: string
  commit: string
  buildTime: string
  repository: string
  releaseUrl: string
  checksumUrl: string
  manifestUrl: string
  schemaVersion: number
  assets: DownloadAsset[]
}
// Client presence is decided by live WebSocket leases only. Metadata freshness
// is a separate axis, so an online client is never labelled by a lapsed or
// missing metadata snapshot (see internal/server/client_api.go newClientView).
export type ClientStatus = 'online' | 'offline'
export type ClientMetadataState = 'fresh' | 'expired' | 'unavailable'
/**
 * @deprecated 'stale' and 'metadata_unavailable' remain accepted by the API as
 * aliases of metadataState=expired|unavailable for one minor release. Filter on
 * ClientMetadataState instead.
 */
export type ClientStatusFilter = ClientStatus | 'stale' | 'metadata_unavailable'
export type ClientListSummary = {
  total: number
  online: number
  activeConnections: number
  activeStreams: number
  metadataUnavailable: number
  metadataStale: number
}
export type ClientListener = { protocol: string; listenAddress: string; agentId: string; enabled: boolean }
export type ClientConnection = {
  connectionId: string
  clientInstanceId: string
  tokenId: string
  ownerUserId: string
  serverNodeId: string
  connectionEpoch: number
  activeStreams: number
  healthScore: number
  acquiredAt: string
  lastHeartbeatAt: string
  expiresAt: string
  local: boolean
}
export type ClientInstance = {
  id: string
  instanceId: string
  ownerUserId: string
  tokenIds: string[]
  agentIds: string[]
  version: string
  commit: string
  platform: string
  hostname: string
  processStartAt: string
  status: ClientStatus
  metadataState: ClientMetadataState
  activeConnections: number
  activeStreams: number
  serverNodeIds: string[]
  lastSeenAt: string
  metadata: Record<string, string>
  capabilities: string[]
  listeners: ClientListener[]
}
export type ClientPage = { items: ClientInstance[]; nextCursor?: string; hasMore?: boolean; summary?: ClientListSummary }
export type ClientListParams = {
  ownerUserId?: string
  tokenId?: string
  serverNodeId?: string
  status?: ClientStatusFilter
  metadataState?: ClientMetadataState
  agentId?: string
  keyword?: string
  cursor?: string
  limit?: number
}

let token = localStorage.getItem('tunnelmesh_token') || ''
export function setToken(value: string) { token = value; value ? localStorage.setItem('tunnelmesh_token', value) : localStorage.removeItem('tunnelmesh_token') }
export function getToken() { return token }

export function getAgents(params: { cursor?: string; limit?: number } = {}) {
  const query = new URLSearchParams()
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  return api<{items: Agent[]; nextCursor?: string}>(`/agents${query.size ? `?${query}` : ''}`)
}
export function createAgent(input: AgentCreateInput) { return api<Agent>('/agents', { method: 'POST', body: JSON.stringify(input) }) }
export function listAgentPolicies(agentId: string, params: { cursor?: string; limit?: number; status?: 'active' | 'deleted' | 'all' } = {}) {
  const query = new URLSearchParams()
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  if (params.status) query.set('status', params.status)
  const suffix = query.toString() ? `?${query}` : ''
  return api<AgentPolicyPage>(`/agents/${encodeURIComponent(agentId)}/policies${suffix}`)
}
export function createAgentPolicy(agentId: string, input: AgentPolicyInput, idempotencyKey: string) {
  return api<AgentPolicy>(`/agents/${encodeURIComponent(agentId)}/policies`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}
export function updateAgentPolicy(agentId: string, policyId: string, input: AgentPolicyInput) {
  return api<AgentPolicy>(`/agents/${encodeURIComponent(agentId)}/policies/${encodeURIComponent(policyId)}`, {
    method: 'PATCH',
    body: JSON.stringify(input),
  })
}
export function deleteAgentPolicy(agentId: string, policyId: string) {
  return api<AgentPolicy>(`/agents/${encodeURIComponent(agentId)}/policies/${encodeURIComponent(policyId)}`, {
    method: 'DELETE',
  })
}
export function restoreAgentPolicy(agentId: string, policyId: string) {
  return api<AgentPolicy>(`/agents/${encodeURIComponent(agentId)}/policies/${encodeURIComponent(policyId)}/restore`, {
    method: 'POST',
  })
}
export function listRoutes(params: { cursor?: string; limit?: number } = {}) {
  const query = new URLSearchParams()
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  const suffix = query.toString() ? `?${query}` : ''
  return api<ManagedRoutePage>(`/routes${suffix}`)
}
export function createRoute(input: ManagedRouteCreateInput, idempotencyKey: string) {
  return api<ManagedRoute>('/routes', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}
export function updateRoute(id: string, input: ManagedRouteUpdateInput) {
  return api<ManagedRoute>(`/routes/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(input) })
}
export function listAuditLogs(params: AuditLogFilter & { cursor?: string; limit?: number } = {}) {
  const query = new URLSearchParams()
  if (params.actorUserId) query.set('actorUserId', params.actorUserId)
  if (params.action) query.set('action', params.action)
  if (params.resourceType) query.set('resourceType', params.resourceType)
  if (params.resourceId) query.set('resourceId', params.resourceId)
  if (params.createdFrom) query.set('createdFrom', params.createdFrom)
  if (params.createdTo) query.set('createdTo', params.createdTo)
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  return api<AuditLogPage>(`/audit-logs${query.size ? `?${query}` : ''}`)
}
export function listTokens(params: { type?: TokenType; cursor?: string } = {}) {
  const query = new URLSearchParams()
  if (params.type) query.set('type', params.type)
  if (params.cursor) query.set('cursor', params.cursor)
  const suffix = query.toString() ? `?${query.toString()}` : ''
  return api<TokenPage>(`/tokens${suffix}`)
}
export function createToken(input: { type: TokenType; ownerUserId?: string; agentId?: string; nodeId?: string; scope?: TokenScope; expiresAt?: string }, idempotencyKey: string) {
  return api<ServiceToken>('/tokens', { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify(input) })
}
export function getTokenDetail(id: string) {
  return api<ServiceToken>(`/tokens/${encodeURIComponent(id)}`, { method: 'GET' })
}
export function updateTokenExpiration(id: string, expiresAt: string | null | undefined) {
  return api<ServiceToken>(`/tokens/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify({ expiresAt: expiresAt ?? null }) })
}
export function updateTokenScope(id: string, scope: TokenScopePatch) {
  return api<ServiceToken>(`/tokens/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify({ scope }) })
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
export function listAgentConnections(agentId: string) {
  return api<{ connections: ClusterAgentConnection[] }>(`/agents/${encodeURIComponent(agentId)}/connections`)
    .then(data => data.connections)
}
export function closeAgentConnection(agentId: string, connectionId: string, connectionEpoch: number) {
  const query = new URLSearchParams({ connectionEpoch: String(connectionEpoch) })
  return api<{ agentId: string; connectionId: string; connectionEpoch: number; closed: boolean }>(
    `/agents/${encodeURIComponent(agentId)}/connections/${encodeURIComponent(connectionId)}?${query}`,
    { method: 'DELETE' },
  )
}
export function listUsers(params: { status?: 'active'|'deleted'|'all'; cursor?: string; limit?: number } = {}) {
  const query = new URLSearchParams()
  if (params.status) query.set('status', params.status)
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  return api<UserPage>(`/users${query.size ? `?${query}` : ''}`)
}
export function createUser(username: string) { return api<TemporaryPasswordResult>('/users', { method: 'POST', body: JSON.stringify({ username }) }) }
export function updateUserStatus(id: string, disabled: boolean) { return api<UserAccount>(`/users/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify({ disabled }) }) }
// Kept separate from updateUserStatus so a per-account MFA requirement is an
// explicit, auditable change instead of a side effect of a status toggle.
export function updateUserMFARequired(id: string, mfaRequired: boolean) { return api<UserAccount>(`/users/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify({ mfaRequired }) }) }
export function resetUserPassword(id: string) { return api<TemporaryPasswordResult>(`/users/${encodeURIComponent(id)}/reset-password`, { method: 'POST' }) }
export function deleteUser(id: string) { return api<UserAccount>(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' }) }
export function restoreUser(id: string) { return api<UserAccount>(`/users/${encodeURIComponent(id)}/restore`, { method: 'POST' }) }
export function changePassword(currentPassword: string, newPassword: string) { return api<UserAccount>('/auth/password', { method: 'PUT', body: JSON.stringify({ currentPassword, newPassword }) }) }
export function getDashboardSummary() { return api<DashboardSummary>('/dashboard/summary') }

export function getServerNodes(params: { cursor?: string; limit?: number } = {}) {
  const query = new URLSearchParams()
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  const suffix = query.toString() ? `?${query}` : ''
  return api<ServerNodePage>(`/server-nodes${suffix}`)
}
export function getServerNode(id: string) { return api<ServerNode>(`/server-nodes/${encodeURIComponent(id)}`) }
export function updateServerNode(id: string, input: ServerNodeUpdateInput) {
  return api<ServerNode>(`/server-nodes/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(input) })
}
export function deleteServerNode(id: string) { return api<ServerNode>(`/server-nodes/${encodeURIComponent(id)}`, { method: 'DELETE' }) }
export function restoreServerNode(id: string) { return api<ServerNode>(`/server-nodes/${encodeURIComponent(id)}/restore`, { method: 'POST' }) }

export function getDownloads() { return api<DownloadInfo>('/downloads') }

export function listClients(params: ClientListParams = {}) {
  const query = new URLSearchParams()
  if (params.ownerUserId) query.set('ownerUserId', params.ownerUserId)
  if (params.status) query.set('status', params.status)
  if (params.metadataState) query.set('metadataState', params.metadataState)
  if (params.tokenId) query.set('tokenId', params.tokenId)
  if (params.serverNodeId) query.set('serverNodeId', params.serverNodeId)
  if (params.agentId) query.set('agentId', params.agentId)
  if (params.keyword) query.set('keyword', params.keyword)
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  const suffix = query.toString() ? `?${query}` : ''
  return api<ClientPage>(`/clients${suffix}`)
}
export function getClientDetail(clientInstanceId: string) {
  return api<ClientInstance>(`/clients/${encodeURIComponent(clientInstanceId)}`)
}
export function listClientConnections(clientInstanceId: string) {
  return api<{ connections: ClientConnection[] }>(`/clients/${encodeURIComponent(clientInstanceId)}/connections`)
    .then(data => data.connections)
}
export function closeClientConnection(clientInstanceId: string, connectionId: string, connectionEpoch: number) {
  const query = new URLSearchParams({ connectionEpoch: String(connectionEpoch) })
  return api<{ clientInstanceId: string; connectionId: string; connectionEpoch: number; closed: boolean }>(
    `/clients/${encodeURIComponent(clientInstanceId)}/connections/${encodeURIComponent(connectionId)}?${query}`,
    { method: 'DELETE' },
  )
}

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {}
  if (init.headers instanceof Headers) init.headers.forEach((value, key) => { headers[key] = value })
  else if (Array.isArray(init.headers)) for (const [key, value] of init.headers) headers[key] = value
  else if (init.headers) Object.assign(headers, init.headers)
  headers.Accept = 'application/json'
  if (init.body && !Object.keys(headers).some(key => key.toLowerCase() === 'content-type')) headers['Content-Type'] = 'application/json'
  if (token && !Object.keys(headers).some(key => key.toLowerCase() === 'authorization')) headers.Authorization = `Bearer ${token}`
  const response = await fetch(`/api/v1${path}`, {...init, headers})
  const payload = await response.json().catch(() => ({msg: response.statusText}))
  if (!response.ok) {
    const data = payload.data as {error?: string} | undefined
    throw new APIError(payload.msg || 'request failed', response.status, payload.code, data?.error, data)
  }
  return payload.data as T
}

export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: number,
    readonly domain?: string,
    // The non-secret part of `data`. A stable `domain` string alone cannot
    // express details such as the `retryAfter` of a throttled login.
    readonly details?: Record<string, unknown>,
  ) { super(message) }
}
