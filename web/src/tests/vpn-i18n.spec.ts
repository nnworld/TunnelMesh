import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import router from '../router'
import zhCN from '../i18n/messages/zh-CN'
import enUS from '../i18n/messages/en-US'
import { breadcrumbsFor } from '../layouts/breadcrumbs'

// Every leaf of a message tree, as dotted paths. An empty or missing string is
// the failure mode a bilingual console actually ships with: the key renders
// verbatim, or the label renders as nothing at all.
function leaves(value: unknown, prefix = ''): string[] {
  if (typeof value === 'string') return [prefix]
  if (value && typeof value === 'object') {
    return Object.entries(value as Record<string, unknown>).flatMap(([key, child]) => leaves(child, prefix ? `${prefix}.${key}` : key))
  }
  return []
}

function resolve(tree: unknown, path: string) {
  return path.split('.').reduce<unknown>((node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined), tree)
}

describe('vpn console navigation and copy', () => {
  it('routes /vpn behind authentication but not behind the admin role', () => {
    const vpn = router.getRoutes().find(route => route.path === '/vpn')
    expect(vpn).toBeTruthy()
    expect(vpn?.meta.auth).toBe(true)
    // Visibility is decided by the server's paginated owner filter, so an admin
    // gate here would be a second, disagreeing source of truth.
    expect(vpn?.meta.admin).toBeUndefined()
  })

  it('lists the vpn entry in the menu right after credentials', () => {
    const source = readFileSync('src/layouts/AppShell.vue', 'utf8')
    expect(source).toContain("['/vpn', 'navigation.vpn']")
    expect(source.indexOf("['/credentials', 'navigation.credentials']")).toBeLessThan(source.indexOf("['/vpn', 'navigation.vpn']"))
  })

  it('names the vpn page in the breadcrumb', () => {
    expect(breadcrumbsFor('/vpn').map(crumb => crumb.key)).toEqual(['shell.console', 'vpn.title'])
  })

  it('translates the navigation entry and every vpn leaf in both locales', () => {
    for (const locale of [zhCN, enUS]) {
      expect(locale.navigation.vpn.trim().length).toBeGreaterThan(0)
      const paths = leaves(locale.vpn)
      expect(paths.length).toBeGreaterThan(60)
      for (const path of paths) {
        const value = resolve(locale.vpn, path)
        expect(typeof value, `vpn.${path}`).toBe('string')
        expect((value as string).trim().length, `vpn.${path}`).toBeGreaterThan(0)
      }
    }
    // The two trees must be structurally identical, or one locale silently shows
    // a key path where the other shows a sentence.
    expect(leaves(zhCN.vpn).sort()).toEqual(leaves(enUS.vpn).sort())
  })

  it('covers every stable management code with a message in both locales', () => {
    const codes = ['vpn_peer_invalid', 'vpn_ip_pool_invalid', 'vpn_peer_not_found', 'vpn_peer_conflict',
      'vpn_agent_capability_missing', 'vpn_ip_pool_exhausted', 'vpn_node_disabled',
      'credential_secret_unavailable', 'vpn_capacity_exhausted', 'vpn_not_implemented']
    const source = readFileSync('src/views/vpn-errors.ts', 'utf8')
    for (const code of codes) {
      expect(source, code).toContain(code)
      const key = source.slice(source.indexOf(`${code}:`) + code.length + 1).split("'")[1]
      expect(key, code).toBeTruthy()
      for (const locale of [zhCN, enUS]) {
        const value = resolve(locale, key as string)
        expect(typeof value, `${code} -> ${key}`).toBe('string')
        expect((value as string).trim().length, `${code} -> ${key}`).toBeGreaterThan(0)
      }
    }
  })

  it('keeps vue-i18n reserved syntax out of the vpn copy', () => {
    // "@" starts a linked message, "|" selects a plural form and braces
    // interpolate, so a metric label or a systemd unit name in the copy would
    // be parsed rather than printed.
    for (const locale of [zhCN, enUS]) {
      for (const path of leaves(locale.vpn)) {
        const value = resolve(locale.vpn, path) as string
        expect(value.includes('@'), `vpn.${path}`).toBe(false)
        expect(value.includes('|'), `vpn.${path}`).toBe(false)
        expect(value.includes('{'), `vpn.${path}`).toBe(false)
        expect(value.includes('}'), `vpn.${path}`).toBe(false)
      }
    }
  })
})
