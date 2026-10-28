import { describe, expect, it } from 'vitest'
import router from '../router'
import { iconPaths } from '../components/icons/paths'
import { flatNavigation, navigationGroups, paletteEntries } from '../layouts/navigation'
import zhCN from '../i18n/messages/zh-CN'
import enUS from '../i18n/messages/en-US'

const MEMBER_ORDER = ['/', '/agents', '/clients', '/routes', '/tokens', '/remote-servers', '/credentials', '/vpn']
const ADMIN_ORDER = ['/servers', '/users', '/sso-providers', '/audit-logs', '/downloads']

function lookup(tree: Record<string, any>, key: string): unknown {
  return key.split('.').reduce<any>((node, part) => node?.[part], tree)
}

describe('navigation model', () => {
  it('keeps the flat order the sidebar always had', () => {
    expect(flatNavigation(false).map(item => item.path)).toEqual(MEMBER_ORDER)
    expect(flatNavigation(true).map(item => item.path)).toEqual([...MEMBER_ORDER, ...ADMIN_ORDER])
  })

  it('never lists /tunnels, which stays reachable only by URL', () => {
    expect(flatNavigation(true).some(item => item.path === '/tunnels')).toBe(false)
    expect(router.getRoutes().some(route => route.path === '/tunnels')).toBe(true)
  })

  it('shows non-admins no admin entries and no empty group headings', () => {
    const groups = navigationGroups(false)
    expect(groups.every(group => group.items.length > 0)).toBe(true)
    expect(groups.flatMap(group => group.items).some(item => item.admin)).toBe(false)
    expect(groups.map(group => group.key)).not.toContain('platform')
    expect(groups.map(group => group.key)).not.toContain('operations')
  })

  it('marks as admin exactly the routes the router guards as admin', () => {
    for (const item of flatNavigation(true)) {
      const route = router.getRoutes().find(entry => entry.path === item.path)
      expect(route, item.path).toBeTruthy()
      expect(Boolean(route?.meta.admin), item.path).toBe(Boolean(item.admin))
    }
  })

  it('places releases after audit logs', () => {
    const paths = flatNavigation(true).map(item => item.path)
    expect(paths.indexOf('/downloads')).toBeGreaterThan(paths.indexOf('/audit-logs'))
  })

  it('uses only known icons and translated labels in both locales', () => {
    for (const group of navigationGroups(true)) {
      expect(lookup(zhCN, group.labelKey), group.labelKey).toBeTruthy()
      expect(lookup(enUS, group.labelKey), group.labelKey).toBeTruthy()
      for (const item of group.items) {
        expect(Object.keys(iconPaths), item.icon).toContain(item.icon)
        expect(lookup(zhCN, item.labelKey), item.labelKey).toBeTruthy()
        expect(lookup(enUS, item.labelKey), item.labelKey).toBeTruthy()
      }
    }
  })

  it('offers page entries plus a few actions to the command palette, scoped by role', () => {
    const member = paletteEntries(false)
    expect(member.filter(entry => entry.kind === 'page').map(entry => entry.path)).toEqual([...MEMBER_ORDER, '/account/security'])
    expect(member.some(entry => entry.kind === 'action' && entry.action === 'theme-dark')).toBe(true)
    expect(paletteEntries(true).some(entry => entry.kind === 'page' && entry.path === '/audit-logs')).toBe(true)
    expect(member.some(entry => entry.kind === 'page' && entry.path === '/audit-logs')).toBe(false)
  })
})
