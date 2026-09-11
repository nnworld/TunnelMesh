import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createAgentPolicy, deleteAgentPolicy, listAgentPolicies, restoreAgentPolicy, updateAgentPolicy } from '../api/client'

describe('agent policy API client', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('lists policies scoped to an agent', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { items: [] } }) })
    vi.stubGlobal('fetch', fetchMock)

    await listAgentPolicies('agent/one')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/agents/agent%2Fone/policies')
  })

  it('creates a policy with an idempotency key and complete fields', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: {} }) })
    vi.stubGlobal('fetch', fetchMock)
    const input = {
      protocol: 'tcp',
      targetHost: '*',
      targetPort: 0,
      allowedCIDRs: [],
      allowedPorts: [],
    }

    await createAgentPolicy('agent-1', input, 'policy-create-001')

    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('/api/v1/agents/agent-1/policies')
    expect(init?.method).toBe('POST')
    expect(new Headers(init?.headers).get('Idempotency-Key')).toBe('policy-create-001')
    expect(init?.body).toBe(JSON.stringify(input))
  })

  it('lists deleted policies when requested', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { items: [] } }) })
    vi.stubGlobal('fetch', fetchMock)

    await listAgentPolicies('agent-1', { status: 'deleted' })

    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/agents/agent-1/policies?status=deleted')
  })

  it('deletes and restores a policy through the nested resource API', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: {} }) })
    vi.stubGlobal('fetch', fetchMock)

    await deleteAgentPolicy('agent/one', 'policy/two')
    await restoreAgentPolicy('agent/one', 'policy/two')

    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/agents/agent%2Fone/policies/policy%2Ftwo')
    expect(fetchMock.mock.calls[0][1]?.method).toBe('DELETE')
    expect(fetchMock.mock.calls[1][0]).toBe('/api/v1/agents/agent%2Fone/policies/policy%2Ftwo/restore')
    expect(fetchMock.mock.calls[1][1]?.method).toBe('POST')
  })

  it('updates a policy through the nested resource API', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: {} }) })
    vi.stubGlobal('fetch', fetchMock)
    const input = {
      protocol: 'tcp',
      targetHost: 'service.internal',
      targetPort: 443,
      allowedCIDRs: ['10.20.0.0/16'],
      allowedPorts: [443, 8443],
    }

    await updateAgentPolicy('agent/one', 'policy/two', input)

    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('/api/v1/agents/agent%2Fone/policies/policy%2Ftwo')
    expect(init?.method).toBe('PATCH')
    expect(init?.body).toBe(JSON.stringify(input))
  })
})
