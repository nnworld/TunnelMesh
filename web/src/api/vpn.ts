import { api } from './client'

export type VpnPeerStatus = 'active' | 'disabled' | 'revoked'

// One peer as the management plane reports it.
//
// The four sealed private-key columns are absent from this type on purpose: the
// server projection declares additionalProperties:false, so a field listed here
// that the server never sends would render as undefined forever and invite a
// future caller to put ciphertext, a nonce or a key id on screen. The only
// response in this module that carries key material is VpnPeerConfig, and it
// carries a rendered file rather than a column of the peer row.
export type VpnPeer = {
  id: string
  ownerUserId: string
  name: string
  description: string
  publicKey: string
  vpnIp: string
  nodeId: string
  agentId: string
  allowedIps: string[]
  allowedPorts: number[]
  allowPrivateTargets: boolean
  icmpEnabled: boolean
  maxConcurrentFlows: number
  packetRateLimit: number
  expiresAt: string | null
  status: VpnPeerStatus
  createdAt: string
  updatedAt: string
}

export type VpnPeerPage = { items: VpnPeer[]; nextCursor?: string; hasMore?: boolean }

export type VpnPeerListParams = {
  status?: VpnPeerStatus | 'all'
  keyword?: string
  nodeId?: string
  agentId?: string
  cursor?: string
  limit?: number
}

// The create body. allowedIps and allowedPorts travel as arrays: the server
// parses each entry on its own, so joining them here would let one entry
// containing a comma become two rules.
export type VpnPeerInput = {
  name: string
  description?: string
  agentId: string
  allowedIps: string[]
  allowedPorts: number[]
  allowPrivateTargets: boolean
  icmpEnabled: boolean
  maxConcurrentFlows: number
  packetRateLimit: number
  expiresAt?: string | null
}

// A patch carries presence, so every field is optional and an empty
// allowedIps array is a value with a meaning of its own: no reachable
// destination. Omitted keys are not sent at all, which is what keeps a rename
// from clearing a policy.
export type VpnPeerPatch = {
  name?: string
  description?: string
  agentId?: string
  allowedIps?: string[]
  allowedPorts?: number[]
  allowPrivateTargets?: boolean
  icmpEnabled?: boolean
  maxConcurrentFlows?: number
  packetRateLimit?: number
  expiresAt?: string | null
  status?: VpnPeerStatus
}

export type VpnPeerCreated = VpnPeer & { configRevealPath: string }

export type VpnPeerConfig = { id: string; name: string; vpnIp: string; format: string; config: string }

// Gateway node status, the fields §10.6 of the design spec asks the node page
// to show. The shape is owned by the data plane phase, which is why every
// field is optional: this release answers 501 vpn_not_implemented, and a
// partial payload from a later phase must render as "—" rather than crash.
export type VpnNodeStatus = {
  nodeId?: string
  enabled?: boolean
  listen?: string
  endpointHost?: string
  subnet?: string
  allocated?: number
  capacity?: number
  peers?: number
  icmpCapable?: boolean
}

// One active flow of one peer. Also owned by the data plane phase and also
// answered with 501 in this release; the console points at Grafana instead of
// rendering a table that would always be empty.
export type VpnFlow = {
  id?: string
  protocol?: string
  target?: string
  port?: number
  startedAt?: string
  bytesSent?: number
  bytesReceived?: number
}

export function listVpnPeers(params: VpnPeerListParams = {}) {
  const query = new URLSearchParams()
  if (params.status && params.status !== 'all') query.set('status', params.status)
  if (params.keyword) query.set('keyword', params.keyword)
  if (params.nodeId) query.set('nodeId', params.nodeId)
  if (params.agentId) query.set('agentId', params.agentId)
  if (params.cursor) query.set('cursor', params.cursor)
  if (params.limit) query.set('limit', String(params.limit))
  return api<VpnPeerPage>(`/vpn-peers${query.size ? `?${query}` : ''}`)
}

export function createVpnPeer(input: VpnPeerInput, idempotencyKey: string) {
  return api<VpnPeerCreated>('/vpn-peers', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(vpnPeerBody(input)),
  })
}

export function getVpnPeer(id: string) {
  return api<VpnPeer>(`/vpn-peers/${encodeURIComponent(id)}`)
}

export function patchVpnPeer(id: string, patch: VpnPeerPatch, idempotencyKey: string) {
  const body: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(patch)) if (value !== undefined) body[key] = value
  return api<VpnPeer>(`/vpn-peers/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(body),
  })
}

// Revoke is a DELETE that answers with the revoked row, so the console can
// update the row in place instead of reloading the page.
export function revokeVpnPeer(id: string, idempotencyKey: string) {
  return api<VpnPeer>(`/vpn-peers/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

// Rotation invalidates the configuration the user already downloaded, so it
// runs under an idempotency key: a retried rotation must not rotate twice.
export function rotateVpnPeer(id: string, idempotencyKey: string) {
  return api<VpnPeerCreated>(`/vpn-peers/${encodeURIComponent(id)}/rotate`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

// The confirmation string is what the operator typed into the prompt, mirroring
// revealToken: the server requires the header to be present and non-empty, and
// asking for a typed word is what makes the reveal deliberate.
export function revealVpnPeerConfig(id: string, confirmation: string, idempotencyKey: string) {
  return api<VpnPeerConfig>(`/vpn-peers/${encodeURIComponent(id)}/config:reveal`, {
    method: 'POST',
    headers: { 'X-VPN-Config-Reveal-Confirm': confirmation, 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify({ acknowledgeRisk: true }),
  })
}

export function listVpnPeerFlows(id: string) {
  return api<{ items: VpnFlow[] }>(`/vpn-peers/${encodeURIComponent(id)}/flows`)
}

export function listVpnNodes() {
  return api<{ items: VpnNodeStatus[] }>('/vpn-nodes')
}

// vpnPeerBody drops the keys a caller did not set, because the server rejects
// unknown fields and reads an explicit null as "clear this" rather than as
// "leave it alone".
function vpnPeerBody(input: VpnPeerInput) {
  const body: Record<string, unknown> = {
    name: input.name,
    agentId: input.agentId,
    allowedIps: input.allowedIps,
    allowedPorts: input.allowedPorts,
    allowPrivateTargets: input.allowPrivateTargets,
    icmpEnabled: input.icmpEnabled,
    maxConcurrentFlows: input.maxConcurrentFlows,
    packetRateLimit: input.packetRateLimit,
  }
  if (input.description !== undefined) body.description = input.description
  if (input.expiresAt !== undefined) body.expiresAt = input.expiresAt
  return body
}
