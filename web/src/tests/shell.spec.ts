import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import router from '../router'
import { flatNavigation } from '../layouts/navigation'

describe('application shell', () => {
  it('registers account security and admin-only user management', () => {
    const users = router.getRoutes().find(route => route.path === '/users')
    const security = router.getRoutes().find(route => route.path === '/account/security')
    expect(users?.meta.admin).toBe(true)
    expect(security?.meta.auth).toBe(true)
  })

  it('registers downloads as an administrator page', () => {
    const downloads = router.getRoutes().find(route => route.path === '/downloads')
    expect(downloads?.meta.admin).toBe(true)
    const admin = flatNavigation(true).filter(item => item.admin).map(item => item.path)
    expect(admin).toEqual(['/servers', '/users', '/sso-providers', '/audit-logs', '/downloads'])
  })

  it('renames the downloads menu entry to release management and keeps it last', () => {
    const zh = readFileSync('src/i18n/messages/zh-CN.ts', 'utf8')
    const en = readFileSync('src/i18n/messages/en-US.ts', 'utf8')
    const paths = flatNavigation(true).map(item => item.path)
    expect(paths[paths.length - 1]).toBe('/downloads')
    expect(zh).toContain("downloads: '发行管理'")
    expect(en).toContain("downloads: 'Releases'")
  })

  it('hides the tunnels menu while keeping the route available', () => {
    const route = router.getRoutes().find(route => route.path === '/tunnels')
    expect(route).toBeTruthy()
    expect(flatNavigation(true).some(item => item.path === '/tunnels')).toBe(false)
  })

  it('contains the responsive mesh navigation shell', () => {
    const shell = readFileSync('src/layouts/AppShell.vue', 'utf8')
    const rail = readFileSync('src/layouts/NavRail.vue', 'utf8')
    expect(rail).toContain('mesh-brand')
    expect(shell).toContain('mobile-drawer')
    expect(flatNavigation(true).some(item => item.labelKey === 'navigation.users')).toBe(true)
  })

  it('contains client and download navigation entries', () => {
    const source = readFileSync('src/router.ts', 'utf8')
    const paths = flatNavigation(true).map(item => item.path)
    expect(paths).toContain('/clients')
    expect(paths).toContain('/downloads')
    expect(source).toContain("path:'/clients'")
    expect(source).toContain("path:'/downloads'")
  })

  it('contains remote server and credential navigation entries', () => {
    const source = readFileSync('src/router.ts', 'utf8')
    const items = flatNavigation(false)
    expect(items.find(item => item.path === '/remote-servers')?.labelKey).toBe('navigation.remoteServers')
    expect(items.find(item => item.path === '/credentials')?.labelKey).toBe('navigation.credentials')
    expect(source).toContain("path:'/remote-servers'")
    expect(source).toContain("path:'/credentials'")
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
    expect(source).toContain('metadataStateOptions')
    expect(source).toContain("'unavailable'")
  })
})
