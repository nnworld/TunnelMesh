import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import router from '../router'
import { deleteServerNode, getServerNode, getServerNodes, restoreServerNode, updateServerNode } from '../api/client'

describe('server node management', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: {} }) }))
  })

  it('exposes an admin-only /servers route and navigation entry', () => {
    const route = router.getRoutes().find(item => item.path === '/servers')
    expect(route?.meta.admin).toBe(true)
    expect(readFileSync('src/layouts/AppShell.vue', 'utf8')).toContain('navigation.servers')
  })

  it('calls the server-node resource API', async () => {
    await getServerNodes({ cursor: 'server-a', limit: 20 })
    await getServerNode('server-a')
    await updateServerNode('server-a', { name: 'edge-a', enabled: false })
    await deleteServerNode('server-a')
    await restoreServerNode('server-a')

    const calls = vi.mocked(fetch).mock.calls.map(([url, init]) => [url, init?.method, init?.body ? JSON.parse(String(init.body)) : undefined])
    expect(calls).toEqual([
      ['/api/v1/server-nodes?cursor=server-a&limit=20', undefined, undefined],
      ['/api/v1/server-nodes/server-a', undefined, undefined],
      ['/api/v1/server-nodes/server-a', 'PATCH', { name: 'edge-a', enabled: false }],
      ['/api/v1/server-nodes/server-a', 'DELETE', undefined],
      ['/api/v1/server-nodes/server-a/restore', 'POST', undefined],
    ])
  })

  it('renders inventory, status, load, lifecycle, and detail state', () => {
    const source = readFileSync('src/views/Servers.vue', 'utf8')
    for (const marker of [
      'getServerNodes', 'getServerNode', 'updateServerNode', 'deleteServerNode', 'restoreServerNode',
      'activeConnections', 'activeStreams', 'healthScore', 'lastSeenAt', 'expiresAt', 'el-drawer',
    ]) {
      expect(source, marker).toContain(marker)
    }
  })

  it('uses a multi-select server-node allowlist in token creation', () => {
    const source = readFileSync('src/views/Tokens.vue', 'utf8')
    expect(source).toContain('serverNodeIds')
    expect(source).toContain('getServerNodes')
    expect(source).toContain('multiple')
    expect(source).toContain('allServerNodes')
  })
})
