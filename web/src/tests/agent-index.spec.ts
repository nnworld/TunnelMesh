import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { getAgents, type Agent } from '../api/client'
import { useAgentIndex } from '../stores/agentIndex'

vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return { ...actual, getAgents: vi.fn() }
})

function agent(id: string, name: string): Agent {
  return { id, name, enabled: true, status: 'online' }
}

// Every console list page can hold hundreds of Agent references, so a single
// shared index is the whole cost of resolving names. These tests pin down that
// cost: one request per page load no matter how many cells ask, and no retry
// storm once the request has failed.
describe('agent index store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.mocked(getAgents).mockReset()
  })

  it('resolves no name before the index is loaded', () => {
    const store = useAgentIndex()
    expect(store.nameOf('agent-a')).toBe('')
    expect(store.status).toBe('idle')
    expect(getAgents).not.toHaveBeenCalled()
  })

  it('loads once and then answers names from the index', async () => {
    vi.mocked(getAgents).mockResolvedValue({ items: [agent('agent-a', 'edge-01')], nextCursor: '' })
    const store = useAgentIndex()
    await store.ensureLoaded()
    expect(store.nameOf('agent-a')).toBe('edge-01')
    expect(store.status).toBe('ready')
    expect(getAgents).toHaveBeenCalledTimes(1)
    expect(getAgents).toHaveBeenCalledWith({ cursor: undefined, limit: 500 })
    await store.ensureLoaded()
    expect(getAgents).toHaveBeenCalledTimes(1)
  })

  it('collapses concurrent loads into one request', async () => {
    let resolvePage: (value: { items: Agent[]; nextCursor: string }) => void = () => {}
    vi.mocked(getAgents).mockImplementation(() => new Promise(resolver => { resolvePage = resolver }))
    const store = useAgentIndex()
    const first = store.ensureLoaded()
    const second = store.ensureLoaded()
    expect(getAgents).toHaveBeenCalledTimes(1)
    resolvePage({ items: [agent('agent-a', 'edge-01')], nextCursor: '' })
    await Promise.all([first, second])
    expect(getAgents).toHaveBeenCalledTimes(1)
    expect(store.nameOf('agent-a')).toBe('edge-01')
  })

  it('follows the cursor until the whole inventory is indexed', async () => {
    vi.mocked(getAgents)
      .mockResolvedValueOnce({ items: [agent('agent-a', 'edge-01')], nextCursor: 'page-2' })
      .mockResolvedValueOnce({ items: [agent('agent-b', 'edge-02')], nextCursor: '' })
    const store = useAgentIndex()
    await store.ensureLoaded()
    expect(getAgents).toHaveBeenNthCalledWith(2, { cursor: 'page-2', limit: 500 })
    expect(store.agents.map(item => item.id)).toEqual(['agent-a', 'agent-b'])
  })

  it('drops to bare ids after a failure and stops retrying', async () => {
    vi.mocked(getAgents).mockRejectedValue(new Error('offline'))
    const store = useAgentIndex()
    await store.ensureLoaded()
    expect(store.status).toBe('error')
    expect(store.nameOf('agent-a')).toBe('')
    await store.ensureLoaded()
    expect(getAgents).toHaveBeenCalledTimes(1)
  })

  it('refreshes in place and keeps the old names until the new page lands', async () => {
    vi.mocked(getAgents).mockResolvedValue({ items: [agent('agent-a', 'edge-01')], nextCursor: '' })
    const store = useAgentIndex()
    await store.ensureLoaded()
    vi.mocked(getAgents).mockResolvedValue({ items: [agent('agent-a', 'renamed')], nextCursor: '' })
    const pending = store.refresh()
    expect(store.nameOf('agent-a')).toBe('edge-01')
    await pending
    expect(store.nameOf('agent-a')).toBe('renamed')
    expect(getAgents).toHaveBeenCalledTimes(2)
  })
})
