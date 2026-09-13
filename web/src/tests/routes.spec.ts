import { describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { createRoute, getAgentMetadata, listAuditLogs, updateRoute } from '../api/client'
import router from '../router'

describe('admin routes', () => {
  it('defines login and managed resources', () => {
    const paths = router.getRoutes().map(route => route.path)
    expect(paths).toEqual(expect.arrayContaining(['/login', '/', '/agents', '/routes', '/tunnels', '/audit-logs', '/credentials', '/remote-servers']))
  })

  it('defines an authenticated agent detail route', () => {
    const detail = router.getRoutes().find(route => route.path === '/agents/:id')
    expect(detail).toBeTruthy()
    expect(detail?.meta.auth).toBe(true)
  })

  it('defines an authenticated token management route', () => {
    const tokens = router.getRoutes().find(route => route.path === '/tokens')
    expect(tokens).toBeTruthy()
    expect(tokens?.meta.auth).toBe(true)
  })

  it('exposes token lifecycle controls without persisting one-time secrets', () => {
    const source = readFileSync('src/views/Tokens.vue', 'utf8')
    expect(source).toContain("t('tokens.create')")
    expect(source).toContain("t('tokens.rotate')")
    expect(source).toContain("t('tokens.revoke')")
    expect(source).toContain('secret')
    expect(source).toContain('clearSecret')
    expect(source).toContain('scope')
    expect(source).toContain("t('tokens.rotateFailed')")
    expect(source).toContain("t('tokens.revokeFailed')")
  })

  it('creates managed routes with an idempotency key', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: {} }) })
    vi.stubGlobal('fetch', fetchMock)

    await createRoute({
      agentId: 'agent-1',
      domain: 'tm-git.example.com',
      pathPrefix: '/',
      protocol: 'http',
      targetHost: '127.0.0.1',
      targetPort: 3000,
      hostHeader: 'service.internal.example.com',
      targetScheme: 'https',
      tlsServerName: 'service.internal.example.com',
    }, 'route-git-001')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('/api/v1/routes')
    expect(init?.method).toBe('POST')
    expect(new Headers(init?.headers).get('Idempotency-Key')).toBe('route-git-001')
    expect(init?.body).toBe(JSON.stringify({
      agentId: 'agent-1',
      domain: 'tm-git.example.com',
      pathPrefix: '/',
      protocol: 'http',
      targetHost: '127.0.0.1',
      targetPort: 3000,
      hostHeader: 'service.internal.example.com',
      targetScheme: 'https',
      tlsServerName: 'service.internal.example.com',
    }))
  })

  it('updates managed routes through the resource API', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: {} }) })
    vi.stubGlobal('fetch', fetchMock)

    await updateRoute('route/one', {
      agentId: 'agent-2',
      domain: 'after.example.com',
      pathPrefix: '/git',
      protocol: 'websocket',
      targetHost: '127.0.0.1',
      targetPort: 3001,
      hostHeader: 'service.internal.example.com',
      targetScheme: 'https',
      tlsServerName: 'service.internal.example.com',
      status: 'disabled',
    })

    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('/api/v1/routes/route%2Fone')
    expect(init?.method).toBe('PATCH')
    expect(init?.body).toBe(JSON.stringify({
      agentId: 'agent-2',
      domain: 'after.example.com',
      pathPrefix: '/git',
      protocol: 'websocket',
      targetHost: '127.0.0.1',
      targetPort: 3001,
      hostHeader: 'service.internal.example.com',
      targetScheme: 'https',
      tlsServerName: 'service.internal.example.com',
      status: 'disabled',
    }))
  })

  it('exposes a managed route creation workflow', () => {
    const source = readFileSync('src/views/Routes.vue', 'utf8')
    expect(source).toContain("t('routes.create')")
    expect(source).toContain('v-if="auth.isAdmin"')
    expect(source).toContain('createRoute')
    expect(source).toContain('getAgents')
    expect(source).toContain('filterable')
    expect(source).toContain('targetHost')
    expect(source).toContain('targetPort')
    expect(source).toContain("t('routes.targetScheme')")
    expect(source).toContain("t('routes.hostHeader')")
    expect(source).toContain("t('routes.tlsServerName')")
    expect(source).toContain("form.targetScheme === 'https'")
    expect(source).toContain('pathPrefix')
    expect(source).toContain('crypto.randomUUID()')
  })

  it('exposes a managed route edit workflow', () => {
    const source = readFileSync('src/views/Routes.vue', 'utf8')
    expect(source).toContain("t('routes.edit')")
    expect(source).toContain('openEdit')
    expect(source).toContain('editOpen')
    expect(source).toContain('updateRoute')
    expect(source).toContain('editingRoute')
  })

  it('lists audit logs with the public API contract', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { items: [] } }) })
    vi.stubGlobal('fetch', fetchMock)

    await listAuditLogs({ limit: 100 })

    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/v1/audit-logs?limit=100')
  })

  it('shows actionable audit log fields and details', () => {
    const source = readFileSync('src/views/AuditLogs.vue', 'utf8')
    expect(source).toContain('actorUserId')
    expect(source).toContain('resourceId')
    expect(source).toContain('details')
    expect(source).toContain('showAuditDetail')
  })

  it('calls the metadata endpoint with the agent id and stale option', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { agentId: 'a1' } }) })
    vi.stubGlobal('fetch', fetchMock)
    await getAgentMetadata('a1', true)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/agents/a1/metadata?includeStale=true', expect.anything())
  })

  it('agent detail view renders metadata states without edit controls', () => {
    const source = readFileSync('src/views/AgentDetail.vue', 'utf8')
    expect(source).toContain("t('agentDetail.logicalSummary')")
    expect(source).toContain("t('agentDetail.source')")
    expect(source).toContain("t('agentDetail.value')")
    expect(source).toContain("t('agentDetail.stale')")
    expect(source).toContain('redacted')
    expect(source).not.toMatch(/Edit metadata|编辑 metadata/i)
  })

  it('agent detail view renders instances and live connections', () => {
    const source = readFileSync('src/views/AgentDetail.vue', 'utf8')
    expect(source).toContain("t('agentDetail.instances')")
    expect(source).toContain("t('agentDetail.connections')")
    expect(source).toContain('instanceId')
    expect(source).toContain('connectionId')
    expect(source).toContain('activeStreams')
    expect(source).toContain('lastHeartbeatAt')
    expect(source).toContain('connectionCount')
    expect(source).not.toContain('password')
    expect(source).not.toContain('token')
  })

  it('keeps route pages lazy-loaded for smaller first-screen bundles', () => {
    const source = readFileSync('src/router.ts', 'utf8')
    expect(source).not.toMatch(/^import (Login|Dashboard|Agents|AgentDetail|Routes|Tunnels|AuditLogs|Tokens|Users|AccountSecurity) from/m)
    for (const view of ['Login', 'Dashboard', 'Agents', 'AgentDetail', 'Routes', 'Tunnels', 'AuditLogs', 'Tokens', 'Users', 'AccountSecurity', 'Credentials', 'RemoteServers']) {
      expect(source).toContain(`const ${view} = () => import('./views/${view}.vue')`)
    }
  })
})
