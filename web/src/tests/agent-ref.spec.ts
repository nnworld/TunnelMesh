import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import AgentRef from '../components/AgentRef.vue'
import { getAgents } from '../api/client'
import { i18n } from '../i18n'

vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return { ...actual, getAgents: vi.fn() }
})

async function mountRefs(props: Array<{ agentId?: string | null; name?: string | null }>) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { render: () => null } },
      { path: '/agents/:id', component: { render: () => null } },
    ],
  })
  await router.push('/')
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({
    render: () => h('div', props.map(entry => h(AgentRef, entry))),
  }).use(pinia).use(router).use(i18n)
  app.mount(host)
  await nextTick()
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
  return { host, links: () => Array.from(host.querySelectorAll('a')) }
}

// Names are labels and ids stay the authority, so every rendering of a
// reference has to keep the id reachable: as the link target, as the hover
// title, or as the text itself when nothing resolved a name.
describe('AgentRef', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(getAgents).mockReset()
  })

  it('renders an em dash without an id', async () => {
    const { host } = await mountRefs([{ agentId: '' }])
    expect(host.textContent?.trim()).toBe('—')
    expect(host.querySelector('a')).toBeNull()
    expect(getAgents).not.toHaveBeenCalled()
  })

  it('labels a known agent and links to its detail page', async () => {
    vi.mocked(getAgents).mockResolvedValue({ items: [{ id: 'agent-a', name: 'edge-01', enabled: true, status: 'online' }], nextCursor: '' })
    const { links } = await mountRefs([{ agentId: 'agent-a' }])
    expect(links()[0].textContent).toBe('edge-01')
    expect(links()[0].getAttribute('href')).toBe('/agents/agent-a')
    expect(links()[0].getAttribute('title')).toBe('agent-a')
    expect(links()[0].classList.contains('tm-mono')).toBe(false)
  })

  it('prefers a server-supplied name over the index', async () => {
    vi.mocked(getAgents).mockResolvedValue({ items: [{ id: 'agent-a', name: 'edge-01', enabled: true, status: 'online' }], nextCursor: '' })
    const { links } = await mountRefs([{ agentId: 'agent-a', name: 'audit name' }])
    expect(links()[0].textContent).toBe('audit name')
  })

  it('falls back to the raw id when the index cannot name it', async () => {
    vi.mocked(getAgents).mockResolvedValue({ items: [], nextCursor: '' })
    const { links } = await mountRefs([{ agentId: 'agent-unknown' }])
    expect(links()[0].textContent).toBe('agent-unknown')
    expect(links()[0].classList.contains('tm-mono')).toBe(true)
  })

  it('fetches the index once for a whole page of references', async () => {
    vi.mocked(getAgents).mockResolvedValue({ items: [], nextCursor: '' })
    await mountRefs(Array.from({ length: 12 }, (_, index) => ({ agentId: `agent-${index}` })))
    expect(getAgents).toHaveBeenCalledTimes(1)
  })

  it('keeps linking after the index request fails', async () => {
    vi.mocked(getAgents).mockRejectedValue(new Error('offline'))
    const { links } = await mountRefs([{ agentId: 'agent-a' }])
    expect(links()[0].textContent).toBe('agent-a')
    expect(links()[0].getAttribute('href')).toBe('/agents/agent-a')
  })
})
