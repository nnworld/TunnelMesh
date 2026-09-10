export type Breadcrumb = { key: string }

const routeKeys: Array<[prefix: string, key: string]> = [
  ['/agents/', 'agents.title'],
  ['/account/security', 'navigation.security'],
  ['/account/', 'common.account'],
]

export function breadcrumbsFor(path: string): Breadcrumb[] {
  const sectionKey = routeKeys.find(([prefix]) => path.startsWith(prefix))?.[1]
  const currentKey = path === '/' ? 'navigation.dashboard'
    : path === '/agents' ? 'agents.title'
    : path === '/routes' ? 'routes.title'
    : path === '/tunnels' ? 'tunnels.title'
    : path === '/tokens' ? 'tokens.title'
    : path === '/users' ? 'users.title'
    : path === '/audit-logs' ? 'audits.title'
    : path === '/account/security' ? 'navigation.security'
    : path.startsWith('/agents/') ? 'agentDetail.title'
    : undefined
  if (!currentKey) return []
  return [{key: 'shell.console'}, ...(sectionKey && sectionKey !== currentKey ? [{key: sectionKey}] : []), {key: currentKey}]
}
