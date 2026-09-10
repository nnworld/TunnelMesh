import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { createAgent, getAgents, getTokenDetail, updateTokenExpiration, updateTokenScope } from '../api/client'
import { defaultTokenExpiration, filterAgents, tokenPayloadFromForm, tokenScopePatchFromForm } from '../views/token-form'

describe('agent creation and token workflow', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: {} }) }))
  })

  it('creates an agent resource before token issuance is needed', async () => {
    await createAgent({ name: 'devbox', enabled: true })
    const request = vi.mocked(fetch).mock.calls[0]
    expect(request[0]).toBe('/api/v1/agents')
    expect(request[1]).toMatchObject({ method: 'POST', body: JSON.stringify({ name: 'devbox', enabled: true }) })
  })

  it('loads enough accessible agents for fuzzy selection', async () => {
    await getAgents({ limit: 500 })
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/v1/agents?limit=500')
  })

  it('follows cursor pages when loading agents for selection', async () => {
    const loadPage = vi.fn()
      .mockResolvedValueOnce({ items: [{ id: 'agent-1', name: 'one', enabled: true }], nextCursor: 'agent-1' })
      .mockResolvedValueOnce({ items: [{ id: 'agent-2', name: 'two', enabled: true }] })

    const { loadAgentsForSelection } = await import('../views/token-form')
    await expect(loadAgentsForSelection(loadPage)).resolves.toHaveLength(2)
    expect(loadPage).toHaveBeenNthCalledWith(1, { limit: 500 })
    expect(loadPage).toHaveBeenNthCalledWith(2, { cursor: 'agent-1', limit: 500 })
  })

  it('filters agents by case-insensitive name or ID fragments', () => {
    const agents = [
      { id: 'agent-DEVBOX-01', name: 'Dev Box', enabled: true },
      { id: 'agent-ci-02', name: 'Build Runner', enabled: true },
      { id: 'agent-disabled', name: 'Dev Disabled', enabled: false },
    ]
    expect(filterAgents(agents, 'dev b')).toEqual([agents[0]])
    expect(filterAgents(agents, 'CI')).toEqual([agents[1]])
    expect(filterAgents(agents, '')).toEqual([agents[0], agents[1]])
  })

  it('defaults expiration to one year and preserves all-ports scope', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-08T10:20:30Z'))

    expect(defaultTokenExpiration()).toBe('2027-09-08T10:20:30+00:00')

    const selectedAgent = { id: 'agent-1', name: 'devbox', enabled: true, ownerUserId: 'user-1' }
    expect(tokenPayloadFromForm({
      type: 'agent',
      agentId: 'agent-1',
      nodeId: '',
      expiresAt: '2027-09-08T10:20:30+00:00',
      agentIds: [],
      cidrsText: '',
      portsText: '',
      selectedAgent,
      selectedAgents: [],
      scope: { protocols: ['tcp'] },
    })).toEqual({
      type: 'agent',
      agentId: 'agent-1',
      ownerUserId: 'user-1',
      scope: { protocols: ['tcp'], agentIds: [], targetCIDRs: [], targetPorts: [] },
      expiresAt: '2027-09-08T10:20:30+00:00',
    })

    vi.useRealTimers()
  })

  it('builds fleet and explicit server-node token scopes', () => {
    const baseInput = {
      type: 'server_node' as const,
      agentId: '',
      nodeId: '',
      expiresAt: '',
      agentIds: [],
      cidrsText: '',
      portsText: '',
      selectedAgent: undefined,
      selectedAgents: [],
      scope: { protocols: ['tcp'] },
    }
    expect(tokenPayloadFromForm({ ...baseInput, serverNodeIds: [] })).toMatchObject({
      type: 'server_node',
      scope: { serverNodeIds: [] },
    })
    expect(tokenPayloadFromForm({ ...baseInput, serverNodeIds: ['server-a', 'server-b'] })).toMatchObject({
      type: 'server_node',
      scope: { serverNodeIds: ['server-a', 'server-b'] },
    })
  })

  it('builds client token scope from a multi-select Agent list', () => {
    const selectedAgents = [
      { id: 'agent-1', name: 'devbox', enabled: true, ownerUserId: 'user-1' },
      { id: 'agent-2', name: 'build runner', enabled: true, ownerUserId: 'user-1' },
    ]
    const baseInput = {
      type: 'client' as const,
      agentId: '',
      nodeId: '',
      expiresAt: '',
      cidrsText: '',
      portsText: '',
      selectedAgent: undefined,
      scope: { protocols: ['tcp'] },
    }

    expect(tokenPayloadFromForm({
      ...baseInput,
      agentIds: ['agent-1', 'agent-2'],
      selectedAgents,
    })).toEqual({
      type: 'client',
      ownerUserId: 'user-1',
      scope: { protocols: ['tcp'], agentIds: ['agent-1', 'agent-2'], targetCIDRs: [], targetPorts: [] },
    })

    expect(tokenPayloadFromForm({
      ...baseInput,
      agentIds: [],
      selectedAgents: [],
    })).toEqual({
      type: 'client',
      scope: { protocols: ['tcp'], agentIds: [], targetCIDRs: [], targetPorts: [] },
    })

    expect(() => tokenPayloadFromForm({
      ...baseInput,
      agentIds: ['agent-1', 'agent-other-owner'],
      selectedAgents: [...selectedAgents, { id: 'agent-other-owner', name: 'other', enabled: true, ownerUserId: 'user-2' }],
    })).toThrow('selected agents must have the same owner')
  })

  it('wires the workflow into management views', () => {
    const agents = readFileSync('src/views/Agents.vue', 'utf8')
    const tokens = readFileSync('src/views/Tokens.vue', 'utf8')
    expect(agents).toContain('createAgent')
    expect(agents).toContain('auth.isAdmin')
    expect(tokens).toContain('filterable')
    expect(tokens).toContain('filterAgents')
    expect(tokens).toContain('defaultTokenExpiration()')
    expect(tokens).toContain('ownerUserId')
    expect(tokens).toContain('v-model="form.agentIds"')
    expect(tokens).toContain('multiple filterable clearable')
    expect(tokens).toContain("t('tokens.allAgents')")
    expect(tokens).not.toContain('agentIdsText')
  })

  it('reads token details and updates expiration through the resource API', async () => {
    await getTokenDetail('token-1')
    await updateTokenExpiration('token-1', undefined)
    const calls = vi.mocked(fetch).mock.calls.map(([url, init]) => [url, init?.method, init?.body ? JSON.parse(String(init.body)) : undefined])
    expect(calls).toEqual([
      ['/api/v1/tokens/token-1', 'GET', undefined],
      ['/api/v1/tokens/token-1', 'PATCH', { expiresAt: null }],
    ])
  })

  it('updates token scope and preserves the current expiration contract', async () => {
    await updateTokenScope('token-1', tokenScopePatchFromForm({
      type: 'client',
      agentIds: ['agent-1', 'agent-2'],
      protocols: ['tcp', 'http'],
      cidrsText: '10.0.0.0/8',
      portsText: '22, 80',
    }))
    const calls = vi.mocked(fetch).mock.calls.map(([url, init]) => [url, init?.method, init?.body ? JSON.parse(String(init.body)) : undefined])
    expect(calls).toEqual([
      ['/api/v1/tokens/token-1', 'PATCH', { scope: { agentIds: ['agent-1', 'agent-2'], protocols: ['tcp', 'http'], targetCIDRs: ['10.0.0.0/8'], targetPorts: [22, 80] } }],
    ])
  })

  it('builds type-specific token scope patches', () => {
    expect(tokenScopePatchFromForm({
      type: 'server_node',
      serverNodeIds: ['server-a', 'server-b'],
      protocols: ['ws'],
      cidrsText: '',
      portsText: '',
    })).toEqual({
      serverNodeIds: ['server-a', 'server-b'],
      protocols: ['ws'],
      targetCIDRs: [],
      targetPorts: [],
    })

    expect(tokenScopePatchFromForm({
      type: 'agent',
      protocols: ['tcp'],
      cidrsText: '',
      portsText: '',
    })).toEqual({
      protocols: ['tcp'],
      targetCIDRs: [],
      targetPorts: [],
    })
  })

  it('rejects invalid port text instead of silently allowing all ports', () => {
    const baseInput = {
      type: 'client' as const,
      agentId: '',
      nodeId: '',
      expiresAt: '',
      agentIds: [],
      cidrsText: '',
      selectedAgent: undefined,
      selectedAgents: [],
      scope: { protocols: ['tcp'] },
    }
    expect(() => tokenPayloadFromForm({ ...baseInput, portsText: 'abc' })).toThrow('invalid target port')
    expect(() => tokenPayloadFromForm({ ...baseInput, portsText: '65536' })).toThrow('invalid target port')
    expect(() => tokenScopePatchFromForm({ type: 'client', protocols: ['tcp'], cidrsText: '', portsText: 'abc' })).toThrow('invalid target port')
  })

  it('shows complete token details and an expiration action', () => {
    const source = readFileSync('src/views/Tokens.vue', 'utf8')
    expect(source).toContain('showDetail')
    expect(source).toContain('detailToken')
    expect(source).toContain('updateExpiration')
    for (const field of ['id', 'type', 'ownerUserId', 'agentId', 'nodeId', 'prefix', 'scope', 'status', 'expiresAt', 'revokedAt', 'lastUsedAt', 'createdAt', 'updatedAt', 'replayed']) {
      expect(source).toContain(`detailToken.${field}`)
    }
    expect(source).toContain('openScope')
    expect(source).toContain('scopeVisible')
    expect(source).toContain('updateTokenScope')
    expect(source).toContain('label="ws"')
    expect(source).toContain('scopeForm.agentIds')
    expect(source).toContain('scopeForm.serverNodeIds')
    expect(source).toContain('formatAgentScope')
    expect(source).toContain('scopeToken.value?.ownerUserId')
  })
})
