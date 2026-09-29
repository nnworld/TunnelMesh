import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import AuditLogs from '../views/AuditLogs.vue'
import { i18n } from '../i18n'
import { listAuditLogs } from '../api/client'

vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/audits', params: {} }),
  useRouter: () => ({ push: vi.fn() }),
}))
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return { ...actual, listAuditLogs: vi.fn() }
})

// The list used to show an opaque actor id and hide every fact behind a dialog that
// was usually empty. It now labels the person, shows what changed inline, and keeps
// the request trace id reachable for log correlation.
describe('audit logs view', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(listAuditLogs).mockReset()
  })

  async function mountAuditLogs() {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const app = createApp(AuditLogs)
    app.use(i18n)
    app.mount(container)
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    return container
  }

  function buttonByText(scope: ParentNode, label: string) {
    return Array.from(scope.querySelectorAll('button')).find((node) => (node.textContent || '').trim() === label)
  }

  it('labels actors, summarizes what changed and exposes the trace id', async () => {
    vi.mocked(listAuditLogs).mockResolvedValue({
      items: [
        {
          id: 'audit-1', actorUserId: 'user-7', actorUsername: 'alice', action: 'agent.updated',
          resourceType: 'agent', resourceId: 'agent-abc',
          details: { name: 'edge', nameBefore: 'old-edge', traceId: '4bf92f3577b34da6a3ce929d0e0e4736' },
          createdAt: '2026-09-29T08:00:00Z',
        },
        {
          id: 'audit-2', action: 'proxy_auth_failed', resourceType: 'proxy_route', resourceId: 'route-1',
          details: { reason: 'bad_password' }, createdAt: '2026-09-29T08:01:00Z',
        },
      ],
      nextCursor: '', hasMore: false,
    })
    const container = await mountAuditLogs()
    const text = container.textContent || ''
    expect(text).toContain('alice')
    // The identifier stays visible: two accounts can share a display name.
    expect(text).toContain('user-7')
    expect(text).toContain('name=edge')
    expect(text).toContain('nameBefore=old-edge')
    // An event with no actor at all reads as the designed fallback, not a blank cell.
    expect(text).toContain(i18n.global.t('audits.actorSystem'))
    // A 32-character hex id is correlation data, not a summary; it belongs to the
    // detail view so it never pushes the real facts off the row.
    expect(container.querySelector('.audit-summary')!.textContent).not.toContain('traceId')

    buttonByText(container, i18n.global.t('audits.detail'))!.click()
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(document.body.textContent).toContain('4bf92f3577b34da6a3ce929d0e0e4736')
  })

  it('says so when an event carries no details', async () => {
    vi.mocked(listAuditLogs).mockResolvedValue({
      items: [{
        id: 'audit-3', actorUserId: 'user-7', action: 'agent.created', resourceType: 'agent',
        resourceId: 'agent-x', details: {}, createdAt: '2026-09-29T08:02:00Z',
      }],
      nextCursor: '', hasMore: false,
    })
    const container = await mountAuditLogs()
    expect((container.querySelector('.audit-summary')!.textContent || '').trim()).toBe(i18n.global.t('audits.emptyDetails'))
  })
})
