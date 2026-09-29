import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import Dashboard from '../views/Dashboard.vue'
import { i18n } from '../i18n'
import { getDashboardSummary, type AuditLog, type DashboardSummary } from '../api/client'

const push = vi.fn()

vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/dashboard', params: {} }),
  useRouter: () => ({ push }),
}))
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return { ...actual, getDashboardSummary: vi.fn() }
})

async function flush() {
  await nextTick()
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

async function mountDashboard() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const app = createApp(Dashboard)
  app.use(i18n)
  app.mount(container)
  await flush()
  return { container, unmount: () => { app.unmount(); container.remove() } }
}

function summary(recentEvents: AuditLog[]): DashboardSummary {
  return {
    agentsTotal: 4, agentsOnline: 2, activeTunnels: 3, managedRoutes: 5, validServiceTokens: 6,
    recentEvents,
  }
}

function buttonByText(scope: ParentNode, label: string) {
  return Array.from(scope.querySelectorAll('button')).find((node) => (node.textContent || '').trim() === label)
}

// The overview listed action, resource type and time. Two `agent.updated` rows a
// minute apart were indistinguishable, so the card now carries the same
// attributes the audit list shows: who acted, on which resource, and why.
describe('dashboard recent events', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    push.mockReset()
    vi.mocked(getDashboardSummary).mockReset()
  })

  it('shows actor, resource id and details for each event', async () => {
    vi.mocked(getDashboardSummary).mockResolvedValue(summary([
      {
        id: 'audit-1', actorUserId: 'user-7', action: 'agent.updated', resourceType: 'agent',
        resourceId: 'agent-abc', details: { name: 'edge', enabled: true }, createdAt: '2026-09-29T08:00:00Z',
      },
    ]))
    const { container, unmount } = await mountDashboard()
    try {
      const row = container.querySelector('.event')!
      expect(row, 'one event row').toBeTruthy()
      const text = row.textContent || ''
      expect(text).toContain('agent.updated')
      expect(text).toContain('user-7')
      expect(text).toContain('agent-abc')
      // Details are summarized as key=value pairs, not dumped as raw JSON.
      expect(text).toContain('name=edge')
      expect(text).toContain('enabled=true')
      expect(text).not.toContain('{"name"')
      // The full payload stays reachable for a hover.
      expect(row.querySelector('.event-details')!.getAttribute('title')).toContain('"enabled"')
    } finally {
      unmount()
    }
  })

  // Proxy entry and other system audits are persisted with a NULL actor; they must
  // read as a system event instead of an empty, meaningless column.
  it('labels actorless system events', async () => {
    vi.mocked(getDashboardSummary).mockResolvedValue(summary([
      {
        id: 'audit-2', action: 'proxy_auth_failed', resourceType: 'proxy_route',
        resourceId: 'route-1', details: { reason: 'bad_password' }, createdAt: '2026-09-29T08:05:00Z',
      },
    ]))
    const { container, unmount } = await mountDashboard()
    try {
      expect(container.textContent).toContain(i18n.global.t('dashboard.systemActor'))
    } finally {
      unmount()
    }
  })

  // The server resolves the actor name once, for both surfaces, so the overview
  // must prefer it over the raw identifier.
  it('prefers the resolved actor name over the identifier', async () => {
    vi.mocked(getDashboardSummary).mockResolvedValue(summary([
      {
        id: 'audit-4', actorUserId: 'user-7', actorUsername: 'alice', action: 'agent.created',
        resourceType: 'agent', resourceId: 'agent-1', details: { name: 'edge' }, createdAt: '2026-09-29T08:10:00Z',
      },
    ]))
    const { container, unmount } = await mountDashboard()
    try {
      const text = container.querySelector('.event')!.textContent || ''
      expect(text).toContain('alice')
      expect(text).not.toContain('user-7')
    } finally {
      unmount()
    }
  })

  it('links the card to the full audit log', async () => {
    vi.mocked(getDashboardSummary).mockResolvedValue(summary([]))
    const { container, unmount } = await mountDashboard()
    try {
      buttonByText(container, i18n.global.t('dashboard.viewAudits'))!.click()
      await flush()
      expect(push).toHaveBeenCalledWith('/audits')
    } finally {
      unmount()
    }
  })
})
