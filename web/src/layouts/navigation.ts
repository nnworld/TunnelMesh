// Single source of truth for the sidebar, the mobile drawer and the command
// palette. Admin visibility is a convenience only: the router guard and the
// server remain the authority, and navigation.spec keeps both in agreement.
import type { IconName } from '../components/icons/paths'

export type NavItem = { path: string; labelKey: string; icon: IconName; admin?: boolean }
export type NavGroup = { key: string; labelKey: string; items: NavItem[] }

const GROUPS: NavGroup[] = [
  { key: 'overview', labelKey: 'shell.groups.overview', items: [
    { path: '/', labelKey: 'navigation.dashboard', icon: 'overview' },
  ] },
  { key: 'network', labelKey: 'shell.groups.network', items: [
    { path: '/agents', labelKey: 'navigation.agents', icon: 'agents' },
    { path: '/clients', labelKey: 'navigation.clients', icon: 'clients' },
    { path: '/routes', labelKey: 'navigation.routes', icon: 'routes' },
  ] },
  { key: 'access', labelKey: 'shell.groups.access', items: [
    { path: '/tokens', labelKey: 'navigation.tokens', icon: 'tokens' },
    { path: '/remote-servers', labelKey: 'navigation.remoteServers', icon: 'servers-remote' },
    { path: '/credentials', labelKey: 'navigation.credentials', icon: 'credentials' },
    { path: '/vpn', labelKey: 'navigation.vpn', icon: 'vpn' },
  ] },
  { key: 'platform', labelKey: 'shell.groups.platform', items: [
    { path: '/servers', labelKey: 'navigation.servers', icon: 'servers', admin: true },
    { path: '/users', labelKey: 'navigation.users', icon: 'users', admin: true },
    { path: '/sso-providers', labelKey: 'navigation.sso', icon: 'sso', admin: true },
  ] },
  { key: 'operations', labelKey: 'shell.groups.operations', items: [
    { path: '/audit-logs', labelKey: 'navigation.audits', icon: 'audit', admin: true },
    { path: '/downloads', labelKey: 'navigation.downloads', icon: 'releases', admin: true },
  ] },
]

/** Groups visible to the caller; a group with no visible item is dropped entirely. */
export function navigationGroups(isAdmin: boolean): NavGroup[] {
  return GROUPS
    .map(group => ({ ...group, items: group.items.filter(item => isAdmin || !item.admin) }))
    .filter(group => group.items.length > 0)
}

export function flatNavigation(isAdmin: boolean): NavItem[] {
  return navigationGroups(isAdmin).flatMap(group => group.items)
}

export type PaletteAction = 'theme-light' | 'theme-dark' | 'theme-system' | 'switch-language'
export type PaletteEntry =
  | { kind: 'page'; path: string; labelKey: string; icon: IconName; admin?: boolean }
  | { kind: 'action'; action: PaletteAction; labelKey: string; icon: IconName }

/** Everything the command palette can open or do. It reads no server data. */
export function paletteEntries(isAdmin: boolean): PaletteEntry[] {
  const pages: PaletteEntry[] = flatNavigation(isAdmin).map(item => ({ kind: 'page', ...item }))
  pages.push({ kind: 'page', path: '/account/security', labelKey: 'navigation.security', icon: 'shield' })
  const actions: PaletteEntry[] = [
    { kind: 'action', action: 'theme-light', labelKey: 'shell.themeLight', icon: 'sun' },
    { kind: 'action', action: 'theme-dark', labelKey: 'shell.themeDark', icon: 'moon' },
    { kind: 'action', action: 'theme-system', labelKey: 'shell.themeSystem', icon: 'monitor' },
    { kind: 'action', action: 'switch-language', labelKey: 'shell.switchLanguage', icon: 'globe' },
  ]
  return [...pages, ...actions]
}
