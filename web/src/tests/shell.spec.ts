import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import router from '../router'

describe('application shell', () => {
  it('registers account security and admin-only user management', () => {
    const users = router.getRoutes().find(route => route.path === '/users')
    const security = router.getRoutes().find(route => route.path === '/account/security')
    expect(users?.meta.admin).toBe(true)
    expect(security?.meta.auth).toBe(true)
  })

  it('registers downloads as an administrator page', () => {
    const downloads = router.getRoutes().find(route => route.path === '/downloads')
    const shell = readFileSync('src/layouts/AppShell.vue', 'utf8')
    expect(downloads?.meta.admin).toBe(true)
    expect(shell).toContain("auth.isAdmin ? [['/downloads'")
  })

  it('contains the responsive mesh navigation shell', () => {
    const source = readFileSync('src/layouts/AppShell.vue', 'utf8')
    expect(source).toContain('mesh-brand')
    expect(source).toContain('mobile-drawer')
    expect(source).toContain('navigation.users')
  })

  it('contains client and download navigation entries', () => {
    const shell = readFileSync('src/layouts/AppShell.vue', 'utf8')
    const router = readFileSync('src/router.ts', 'utf8')
    expect(shell).toContain("'/clients'")
    expect(shell).toContain("'/downloads'")
    expect(router).toContain("path:'/clients'")
    expect(router).toContain("path:'/downloads'")
  })

  it('clients page contains filter, summary, table, and detail surface', () => {
    const source = readFileSync('src/views/Clients.vue', 'utf8')
    expect(source).toContain('listClients')
    expect(source).toContain('getClientDetail')
    expect(source).toContain('listClientConnections')
    expect(source).toContain('closeClientConnection')
    expect(source).toContain('el-table')
    expect(source).toContain('v-if="auth.isAdmin"')
    expect(source).toContain("t('clients.reset')")
    expect(source).toContain("t('clients.query')")
    expect(source).toContain('clientDetailTitle')
    expect(source).toContain('closeConnection')
    expect(source).toContain('connectionEpoch')
    expect(source).toContain('metadata_unavailable')
  })
})
