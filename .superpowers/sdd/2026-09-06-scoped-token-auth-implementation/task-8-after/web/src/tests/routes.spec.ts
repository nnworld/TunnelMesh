import { describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { getAgentMetadata } from '../api/client'
import router from '../router'

describe('admin routes', () => {
  it('defines login and managed resources', () => {
    const paths = router.getRoutes().map(route => route.path)
    expect(paths).toEqual(expect.arrayContaining(['/login', '/', '/agents', '/routes', '/tunnels', '/audit-logs']))
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
    expect(source).toContain('Create token')
    expect(source).toContain('Rotate')
    expect(source).toContain('Revoke')
    expect(source).toContain('secret')
    expect(source).toContain('clearSecret')
    expect(source).toContain('scope')
  })

  it('calls the metadata endpoint with the agent id and stale option', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { agentId: 'a1' } }) })
    vi.stubGlobal('fetch', fetchMock)
    await getAgentMetadata('a1', true)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/agents/a1/metadata?includeStale=true', expect.anything())
  })

  it('agent detail view renders metadata states without edit controls', () => {
    const source = readFileSync('src/views/AgentDetail.vue', 'utf8')
    expect(source).toContain('Metadata')
    expect(source).toContain('Source')
    expect(source).toContain('Value')
    expect(source).toContain('Stale')
    expect(source).toContain('redacted')
    expect(source).not.toMatch(/Edit metadata|编辑 metadata/i)
  })
})
