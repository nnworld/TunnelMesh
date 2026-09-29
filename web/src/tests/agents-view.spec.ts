import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createPinia } from 'pinia'
import Agents from '../views/Agents.vue'
import { i18n } from '../i18n'
import { createAgent, getAgents, updateAgent, type Agent } from '../api/client'
import { useAuthStore } from '../stores/auth'

vi.mock('vue-router', () => ({ useRoute: () => ({ path: '/agents', params: {} }) }))
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return { ...actual, getAgents: vi.fn(), createAgent: vi.fn(), updateAgent: vi.fn() }
})

async function flush() {
  await nextTick()
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
}

async function mountAgents() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const app = createApp(Agents)
  const pinia = createPinia()
  app.use(pinia)
  const auth = useAuthStore(pinia)
  auth.user = { id: 'user-1', username: 'tester', role: 'admin' }
  app.use(i18n)
  app.mount(container)
  await flush()
  return { container, unmount: () => { app.unmount(); container.remove() } }
}

function rows(container: HTMLElement) {
  return Array.from(container.querySelectorAll('.el-table__body tr'))
}

// The list sorts by creation time now, so a row is addressed by its agent rather
// than by an index that only reflected the API page order.
function rowByText(scope: ParentNode, text: string) {
  return rows(scope as HTMLElement).find((row) => (row.textContent || '').includes(text))!
}

function buttonByText(scope: ParentNode, label: string) {
  return Array.from(scope.querySelectorAll('button')).find((node) => (node.textContent || '').trim() === label)
}

function dialog() {
  return document.querySelector('.el-dialog') as HTMLElement | null
}

function pagedAgent(id: string, name: string, created: string, updated: string): Agent {
  return { id, name, enabled: true, status: 'offline', createdAt: created, updatedAt: updated }
}

function agentsWithAge(): Agent[] {
  // Deliberately not in creation order: the list must sort, not echo the API page.
  return [
    { id: 'agent-mid', name: 'mid', enabled: true, status: 'offline', createdAt: '2026-09-21T08:00:00Z' },
    { id: 'agent-newest', name: 'newest', enabled: true, status: 'online', createdAt: '2026-09-29T02:00:00Z' },
    { id: 'agent-oldest', name: 'oldest', enabled: true, status: 'offline', createdAt: '2026-09-01T23:30:00Z' },
  ]
}

// The status column used to render the enabled flag with the wording "online",
// so an agent created in the console read as connected before it ever dialed
// in. Connectivity and the administrative switch are separate columns now.
describe('agent list connectivity status', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(getAgents).mockReset()
    vi.mocked(createAgent).mockReset()
  })

  it('renders connectivity and the enabled flag as separate columns', async () => {
    const agents: Agent[] = [
      { id: 'agent-live', name: 'live', enabled: true, status: 'online' },
      { id: 'agent-never', name: 'never', enabled: true, status: 'offline' },
      { id: 'agent-disabled', name: 'disabled', enabled: false, status: 'offline' },
    ]
    vi.mocked(getAgents).mockResolvedValue({ items: agents, nextCursor: '' })
    const { container, unmount } = await mountAgents()
    try {
      const text = container.textContent || ''
      expect(text).toContain(i18n.global.t('agents.enabledColumn'))
      expect(rows(container)).toHaveLength(3)
      const live = rowByText(container, 'agent-live').textContent || ''
      const never = rowByText(container, 'agent-never').textContent || ''
      const disabled = rowByText(container, 'agent-disabled').textContent || ''
      expect(live).toContain(i18n.global.t('agents.online'))
      expect(live).toContain(i18n.global.t('agents.active'))
      // An enabled agent that never connected must read as offline, not online.
      expect(never).toContain(i18n.global.t('agents.offline'))
      expect(never).not.toContain(i18n.global.t('agents.online'))
      expect(never).toContain(i18n.global.t('agents.active'))
      expect(disabled).toContain(i18n.global.t('agents.offline'))
      expect(disabled).toContain(i18n.global.t('agents.disabled'))
    } finally {
      unmount()
    }
  })
})

// Operators could not tell a stale agent from a freshly registered one, because
// the list showed no timestamps and kept the API page order, and renaming an
// agent meant deleting and recreating it (which invalidates its token binding).
describe('agent list time and rename', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(getAgents).mockReset()
    vi.mocked(updateAgent).mockReset()
  })

  it('shows the creation time and lists the newest agent first', async () => {
    vi.mocked(getAgents).mockResolvedValue({ items: agentsWithAge(), nextCursor: '' })
    const { container, unmount } = await mountAgents()
    try {
      expect(container.textContent).toContain(i18n.global.t('agents.createdAt'))
      const body = rows(container)
      expect(body.map((row) => row.textContent || '')).toEqual([
        expect.stringContaining('newest'),
        expect.stringContaining('mid'),
        expect.stringContaining('oldest'),
      ])
      // The formatted cell must carry the value, not a placeholder dash.
      expect(body[0].textContent).toContain('2026')
    } finally {
      unmount()
    }
  })

  it('renames an agent from the actions column and reloads the list', async () => {
    const listed = agentsWithAge()
    const renamed = listed.map((agent) => (agent.id === 'agent-newest' ? { ...agent, name: 'renamed' } : agent))
    vi.mocked(getAgents).mockResolvedValueOnce({ items: listed, nextCursor: '' })
      .mockResolvedValue({ items: renamed, nextCursor: '' })
    vi.mocked(updateAgent).mockResolvedValue(renamed[1])
    const { container, unmount } = await mountAgents()
    try {
      const target = rows(container).find((row) => (row.textContent || '').includes('newest'))!
      const trigger = buttonByText(target, i18n.global.t('agents.edit'))
      expect(trigger, 'edit button in the actions column').toBeTruthy()
      trigger!.click()
      await flush()

      const panel = dialog()
      expect(panel, 'edit dialog').toBeTruthy()
      const input = panel!.querySelector('input')!
      expect(input.value).toBe('newest')
      input.value = 'renamed'
      input.dispatchEvent(new Event('input'))
      await flush()

      buttonByText(panel!, i18n.global.t('common.save'))!.click()
      await flush()

      expect(updateAgent).toHaveBeenCalledWith('agent-newest', { name: 'renamed', enabled: true })
      expect(getAgents).toHaveBeenCalledTimes(2)
      expect(rows(container)[0].textContent).toContain('renamed')
    } finally {
      unmount()
    }
  })

  // The switch was set once, at creation: an operator had to delete and recreate
  // the node (losing its token binding and policies) to take it out of service.
  it('carries the administrative switch through the edit dialog', async () => {
    const listed: Agent[] = [
      { id: 'agent-on', name: 'kept-online', enabled: true, status: 'online', createdAt: '2026-09-28T00:00:00Z', updatedAt: '2026-09-28T00:00:00Z' },
      { id: 'agent-off', name: 'kept-offline', enabled: false, status: 'offline', createdAt: '2026-09-27T00:00:00Z', updatedAt: '2026-09-27T00:00:00Z' },
    ]
    vi.mocked(getAgents).mockResolvedValue({ items: listed, nextCursor: '' })
    const { container, unmount } = await mountAgents()
    try {
      // Opening a row must reflect what is stored, not a default of enabled.
      const offRow = rowByText(container, 'kept-offline')
      buttonByText(offRow, i18n.global.t('agents.edit'))!.click()
      await flush()
      let panel = dialog()!
      expect(panel.querySelector('.el-switch'), 'switch in the edit dialog').toBeTruthy()
      expect(panel.textContent, 'the operator-facing meaning of the switch').toContain(i18n.global.t('agents.enabledHint'))
      expect(panel.querySelector('.el-switch')!.classList.contains('is-checked'), 'disabled agent opens unchecked').toBe(false)
      buttonByText(panel, i18n.global.t('users.cancel'))!.click()
      await flush()

      const onRow = rowByText(container, 'kept-online')
      buttonByText(onRow, i18n.global.t('agents.edit'))!.click()
      await flush()
      panel = dialog()!
      expect(panel.querySelector('.el-switch')!.classList.contains('is-checked'), 'enabled agent opens checked').toBe(true)

      panel.querySelector('.el-switch')!.dispatchEvent(new MouseEvent('click'))
      await flush()
      buttonByText(panel, i18n.global.t('common.save'))!.click()
      await flush()

      // The name is untouched, so the request carries it unchanged: PATCH merges
      // server-side, and the dialog always knows both values.
      expect(updateAgent).toHaveBeenCalledWith('agent-on', { name: 'kept-online', enabled: false })
    } finally {
      unmount()
    }
  })

  // Saving without touching anything would be a write with no intent behind it.
  it('closes the edit dialog without a request when nothing changed', async () => {
    vi.mocked(getAgents).mockResolvedValue({ items: agentsWithAge(), nextCursor: '' })
    vi.mocked(updateAgent).mockResolvedValue(agentsWithAge()[1])
    const { container, unmount } = await mountAgents()
    try {
      const target = rows(container).find((row) => (row.textContent || '').includes('newest'))!
      buttonByText(target, i18n.global.t('agents.edit'))!.click()
      await flush()
      const panel = dialog()!
      buttonByText(panel, i18n.global.t('common.save'))!.click()
      await flush()
      expect(updateAgent).not.toHaveBeenCalled()
      expect(getAgents).toHaveBeenCalledTimes(1)
    } finally {
      unmount()
    }
  })
})

// The list used to fetch 500 rows and render all of them, so an operator with a
// few hundred nodes had to scan for their own. Paging is now the server's job:
// 20 rows per request, newest first, and a keyword that narrows before paging.
describe('agent list paging and name filter', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(getAgents).mockReset()
    vi.mocked(updateAgent).mockReset()
  })

  it('requests 20 rows per page and walks the cursor forward and back', async () => {
    const first = [pagedAgent('agent-p1', 'page-one', '2026-09-20T00:00:00Z', '2026-09-21T00:00:00Z')]
    const second = [pagedAgent('agent-p2', 'page-two', '2026-09-10T00:00:00Z', '2026-09-19T00:00:00Z')]
    vi.mocked(getAgents)
      .mockResolvedValueOnce({ items: first, nextCursor: 'cursor-page-2' })
      .mockResolvedValueOnce({ items: second, nextCursor: '' })
      .mockResolvedValue({ items: first, nextCursor: 'cursor-page-2' })
    const { container, unmount } = await mountAgents()
    try {
      expect(getAgents).toHaveBeenLastCalledWith(expect.objectContaining({ limit: 20 }))
      // The update time column exists, and it renders a date rather than a dash.
      expect(container.textContent).toContain(i18n.global.t('agents.updatedAt'))
      expect(rows(container)).toHaveLength(1)
      expect(rows(container)[0].textContent).toContain('page-one')

      buttonByText(container, i18n.global.t('agents.nextPage'))!.click()
      await flush()
      expect(getAgents).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: 'cursor-page-2', limit: 20 }))
      expect(rows(container)[0].textContent).toContain('page-two')
      // A page has no further cursor: the next control stops rather than
      // requesting an empty page.
      expect(buttonByText(container, i18n.global.t('agents.nextPage'))).toBeUndefined()

      buttonByText(container, i18n.global.t('agents.previousPage'))!.click()
      await flush()
      expect(getAgents).toHaveBeenLastCalledWith(expect.objectContaining({ limit: 20 }))
      expect(getAgents.mock.calls.at(-1)![0]).not.toHaveProperty('cursor')
      expect(rows(container)[0].textContent).toContain('page-one')
    } finally {
      unmount()
    }
  })

  it('sends the name filter and returns to the first page', async () => {
    // A row is needed for the pager to exist at all: an empty result renders the
    // empty state, which has no page to walk back from.
    vi.mocked(getAgents).mockResolvedValue({ items: [pagedAgent('agent-filter', 'edge-one', '2026-09-20T00:00:00Z', '2026-09-20T00:00:00Z')], nextCursor: 'cursor-page-2' })
    const { container, unmount } = await mountAgents()
    try {
      // Start on page two so the reset to page one is observable.
      buttonByText(container, i18n.global.t('agents.nextPage'))!.click()
      await flush()
      expect(getAgents).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: 'cursor-page-2' }))

      const field = container.querySelector('.filter-form input') as HTMLInputElement
      expect(field, 'keyword input').toBeTruthy()
      field.value = 'edge'
      field.dispatchEvent(new Event('input'))
      await flush()
      buttonByText(container, i18n.global.t('agents.query'))!.click()
      await flush()

      expect(getAgents).toHaveBeenLastCalledWith(expect.objectContaining({ keyword: 'edge', limit: 20 }))
      expect(getAgents.mock.calls.at(-1)![0]).not.toHaveProperty('cursor')
    } finally {
      unmount()
    }
  })
})
