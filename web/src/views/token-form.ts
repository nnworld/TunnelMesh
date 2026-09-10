import type { Agent, TokenScope, TokenType } from '../api/client'

type TokenFormInput = {
  type: TokenType
  agentId: string
  nodeId: string
  serverNodeIds?: string[]
  expiresAt: string
  agentIds: string[]
  cidrsText: string
  portsText: string
  selectedAgent?: Agent
  selectedAgents?: Agent[]
  scope: Pick<TokenScope, 'protocols'>
}

type AgentPage = { items: Agent[]; nextCursor?: string }

type TokenScopePatchInput = {
  type: TokenType
  agentIds?: string[]
  serverNodeIds?: string[]
  protocols?: string[]
  cidrsText: string
  portsText: string
}

export async function loadAgentsForSelection(loadPage: (params: { cursor?: string; limit?: number }) => Promise<AgentPage>) {
  const agents: Agent[] = []
  let cursor: string | undefined
  do {
    const page = await loadPage({ cursor, limit: 500 })
    agents.push(...page.items)
    cursor = page.nextCursor || undefined
  } while (cursor)
  return agents
}

export function filterAgents(agents: Agent[], query: string) {
  const fragment = query.trim().toLowerCase()
  return agents.filter(agent => agent.enabled && (!fragment || agent.name.toLowerCase().includes(fragment) || agent.id.toLowerCase().includes(fragment)))
}

export function defaultTokenExpiration(now = new Date()) {
  const expiresAt = new Date(now)
  expiresAt.setUTCFullYear(expiresAt.getUTCFullYear() + 1)
  return formatTokenDate(expiresAt)
}

export function tokenPayloadFromForm(input: TokenFormInput) {
  const ports = parseTokenPorts(input.portsText)
  const ownerUserId = tokenOwnerUserId(input)
  const scope: TokenScope = {
    ...input.scope,
    agentIds: [...input.agentIds],
    targetCIDRs: input.cidrsText.split(',').map(value => value.trim()).filter(Boolean),
    targetPorts: ports,
  }
  if (input.type === 'server_node') scope.serverNodeIds = [...(input.serverNodeIds || [])]
  return {
    type: input.type,
    ...(ownerUserId ? { ownerUserId } : {}),
    ...(input.agentId ? { agentId: input.agentId } : {}),
    ...(input.type !== 'server_node' && input.nodeId ? { nodeId: input.nodeId } : {}),
    scope,
    ...(input.expiresAt ? { expiresAt: input.expiresAt } : {}),
  }
}

function tokenOwnerUserId(input: TokenFormInput) {
  if (input.type === 'agent') return input.selectedAgent?.ownerUserId
  if (input.type !== 'client') return undefined

  const owners = new Set((input.selectedAgents || []).map(agent => agent.ownerUserId))
  if (owners.size > 1) throw new Error('selected agents must have the same owner')
  return owners.values().next().value
}

export function tokenScopePatchFromForm(input: TokenScopePatchInput) {
  const ports = parseTokenPorts(input.portsText)
  return {
    ...(input.type === 'client' ? { agentIds: [...(input.agentIds || [])] } : {}),
    ...(input.type === 'server_node' ? { serverNodeIds: [...(input.serverNodeIds || [])] } : {}),
    ...(input.protocols ? { protocols: input.protocols } : {}),
    targetCIDRs: input.cidrsText.split(',').map(value => value.trim()).filter(Boolean),
    targetPorts: ports,
  }
}

function parseTokenPorts(raw: string) {
  const ports: number[] = []
  for (const value of raw.split(',').map(item => item.trim())) {
    if (!value) continue
    const port = Number(value)
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      throw new Error('invalid target port')
    }
    ports.push(port)
  }
  return ports
}

function formatTokenDate(value: Date) {
  const pad = (input: number) => String(input).padStart(2, '0')
  return `${value.getUTCFullYear()}-${pad(value.getUTCMonth() + 1)}-${pad(value.getUTCDate())}T${pad(value.getUTCHours())}:${pad(value.getUTCMinutes())}:${pad(value.getUTCSeconds())}+00:00`
}
