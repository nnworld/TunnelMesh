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

  it('contains the responsive mesh navigation shell', () => {
    const source = readFileSync('src/layouts/AppShell.vue', 'utf8')
    expect(source).toContain('mesh-brand')
    expect(source).toContain('mobile-drawer')
    expect(source).toContain('navigation.users')
  })
})
