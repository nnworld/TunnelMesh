import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createPinia } from 'pinia'
import Agents from '../views/Agents.vue'
import { i18n } from '../i18n'
import { createAgent, getAgents, type Agent } from '../api/client'
import { useAuthStore } from '../stores/auth'

vi.mock('vue-router', () => ({ useRoute: () => ({ path: '/agents', params: {} }) }))
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return { ...actual, getAgents: vi.fn(), createAgent: vi.fn() }
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
      const body = rows(container)
      expect(body).toHaveLength(3)
      expect(body[0].textContent).toContain(i18n.global.t('agents.online'))
      expect(body[0].textContent).toContain(i18n.global.t('agents.active'))
      // An enabled agent that never connected must read as offline, not online.
      expect(body[1].textContent).toContain(i18n.global.t('agents.offline'))
      expect(body[1].textContent).not.toContain(i18n.global.t('agents.online'))
      expect(body[1].textContent).toContain(i18n.global.t('agents.active'))
      expect(body[2].textContent).toContain(i18n.global.t('agents.offline'))
      expect(body[2].textContent).toContain(i18n.global.t('agents.disabled'))
    } finally {
      unmount()
    }
  })
})
