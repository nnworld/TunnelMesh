import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createPinia } from 'pinia'
import { readFileSync } from 'node:fs'
import { createMemoryHistory, createRouter } from 'vue-router'
import Clients from '../views/Clients.vue'
import { i18n } from '../i18n'
import { getAgents, getClientDetail, listClientConnections, listClients, type ClientConnection, type ClientInstance } from '../api/client'
import { useAuthStore } from '../stores/auth'

// Agent references render through AgentRef, which needs a real router for its
// links and Pinia for the shared index; a bare useRoute stub cannot satisfy
// either, so the module keeps its actual exports here.
vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRoute: () => ({ path: '/clients', params: {} }) }
})
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return { ...actual, listClients: vi.fn(), getClientDetail: vi.fn(), listClientConnections: vi.fn(), getAgents: vi.fn() }
})

async function flush() {
  await nextTick()
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
}

function instance(overrides: Partial<ClientInstance> = {}): ClientInstance {
  return {
    id: 'client-instance-1', instanceId: 'client-local-1', ownerUserId: 'owner-1',
    tokenIds: ['token-1'], agentIds: [], version: 'v1.0.0', commit: 'abc', platform: 'linux',
    hostname: 'host-a', processStartAt: '2026-09-23T10:00:00Z', status: 'online', metadataState: 'fresh',
    activeConnections: 2, activeStreams: 3, serverNodeIds: ['server-1'],
    lastSeenAt: '2026-09-23T10:05:00Z', metadata: {}, capabilities: ['client_metadata.v1'], listeners: [],
    ...overrides,
  }
}

async function mountClients() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const app = createApp(Clients)
  const pinia = createPinia()
  app.use(pinia)
  const auth = useAuthStore(pinia)
  auth.user = { id: 'user-1', username: 'tester', role: 'admin' }
  app.use(createRouter({ history: createMemoryHistory(), routes: [{ path: '/agents/:id', component: { render: () => null } }] }))
  app.use(i18n)
  app.mount(container)
  await flush()
  return { container, unmount: () => { app.unmount(); container.remove() } }
}

function summaryCards(container: HTMLElement) {
  return Array.from(container.querySelectorAll('.summary-card')).map(card => ({
    label: card.querySelector('span')?.textContent?.trim() ?? '',
    value: card.querySelector('strong')?.textContent?.trim() ?? '',
  }))
}

// The cards used to be recomputed from the rows on the current cursor page, so
// they contradicted the table and each other: an online client carrying lapsed
// metadata was counted as "metadata expired" while still being counted as
// online, and a client whose metadata was never reported was counted as neither
// online nor offline. The numbers now come from the server's aggregate over the
// whole filtered population, and presence and metadata freshness are separate
// columns.
// The Agent filter took a raw id because the list filters on the exact id.
// Operators do not keep ids in their head, so the control is now a searchable
// select over the shared index while the request parameter stays the id.
describe('client agent filter', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(listClients).mockReset()
    vi.mocked(getAgents).mockReset()
  })

  function agentFilterParams() {
    const calls = vi.mocked(listClients).mock.calls
    return calls[calls.length - 1]?.[0]?.agentId
  }

  // The filter grid holds several selects, so the agent control is found by the
  // label the operator reads rather than by position.
  function agentFilterSelect(container: HTMLElement) {
    const item = Array.from(container.querySelectorAll('.el-form-item'))
      .find(entry => (entry.querySelector('.el-form-item__label')?.textContent || '').trim() === i18n.global.t('clients.agent'))!
    return item.querySelector('.el-select')!
  }

  it('searches agents by name and queries by their id', async () => {
    vi.mocked(getAgents).mockResolvedValue({ items: [{ id: 'agent-edge', name: 'edge-01', enabled: true, status: 'online' }], nextCursor: '' })
    vi.mocked(listClients).mockResolvedValue({ items: [], nextCursor: '' })
    const { container, unmount } = await mountClients()
    try {
      const select = agentFilterSelect(container)
      expect(select).toBeTruthy()
      expect(select.classList.contains('el-select')).toBe(true)
      select.querySelector('input')!.click()
      await flush()
      const option = Array.from(document.querySelectorAll('.el-select-dropdown__item'))
        .find(item => (item.textContent || '').includes('edge-01'))
      expect(option).toBeTruthy()
      option!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      await flush()
      // Selecting narrows the pending filter; the query button is what submits
      // it, exactly as the free-text control it replaced did.
      Array.from(container.querySelectorAll('button'))
        .find(button => (button.textContent || '').trim() === i18n.global.t('clients.query'))!
        .dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      await flush()
      await flush()
      expect(agentFilterParams()).toBe('agent-edge')
    } finally {
      unmount()
    }
  })

  it('asks for agents with the shared placeholder in both locales', async () => {
    const source = readFileSync('src/views/Clients.vue', 'utf8')
    expect(source).toContain('v-model="filters.agentId"')
    expect(source).toContain("t('clients.searchAgents')")
    const { default: zhCN } = await import('../i18n/messages/zh-CN')
    const { default: enUS } = await import('../i18n/messages/en-US')
    expect(zhCN.clients.searchAgents).toBeTruthy()
    expect(enUS.clients.searchAgents).toBeTruthy()
  })
})

// The drawer used to print agent ids in both the related-agent line and the
// listener table, which is the one place an operator learns which agent owns a
// port. Names now, with the id still reachable by hover and by clicking through.
describe('agent references in the client detail', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(listClients).mockReset()
    vi.mocked(getClientDetail).mockReset()
    vi.mocked(listClientConnections).mockReset()
    vi.mocked(getAgents).mockReset()
  })

  it('names the agent of a listener and links to its page', async () => {
    const withAgent = instance({
      agentIds: ['agent-edge'],
      listeners: [{ protocol: 'tcp', listenAddress: '127.0.0.1:6021', agentId: 'agent-edge', enabled: true }],
    })
    vi.mocked(listClients).mockResolvedValue({ items: [withAgent], nextCursor: '' })
    vi.mocked(getClientDetail).mockResolvedValue(withAgent)
    vi.mocked(listClientConnections).mockResolvedValue({ connections: [], truncated: false })
    vi.mocked(getAgents).mockResolvedValue({ items: [{ id: 'agent-edge', name: 'edge-01', enabled: true, status: 'online' }], nextCursor: '' })

    const { container, unmount } = await mountClients()
    try {
      const row = Array.from(container.querySelectorAll('.el-table__body tr'))
        .find(entry => entry.textContent?.includes('client-local-1'))
      row?.querySelector('button')?.click()
      await flush()
      await flush()
      const refs = Array.from(document.body.querySelectorAll('.agent-ref'))
      expect(refs.length).toBeGreaterThanOrEqual(2)
      for (const ref of refs) {
        expect(ref.textContent).toBe('edge-01')
        expect(ref.getAttribute('href')).toBe('/agents/agent-edge')
        expect(ref.getAttribute('title')).toBe('agent-edge')
      }
    } finally {
      unmount()
    }
  })

  it('still links an agent the index cannot name', async () => {
    const withAgent = instance({ agentIds: ['agent-gone'], listeners: [] })
    vi.mocked(listClients).mockResolvedValue({ items: [withAgent], nextCursor: '' })
    vi.mocked(getClientDetail).mockResolvedValue(withAgent)
    vi.mocked(listClientConnections).mockResolvedValue({ connections: [], truncated: false })
    vi.mocked(getAgents).mockResolvedValue({ items: [], nextCursor: '' })

    const { container, unmount } = await mountClients()
    try {
      const row = Array.from(container.querySelectorAll('.el-table__body tr'))
        .find(entry => entry.textContent?.includes('client-local-1'))
      row?.querySelector('button')?.click()
      await flush()
      await flush()
      const ref = document.body.querySelector('.agent-ref')
      expect(ref?.textContent).toBe('agent-gone')
      expect(ref?.getAttribute('href')).toBe('/agents/agent-gone')
    } finally {
      unmount()
    }
  })

  it('renders an em dash when the client has no agents', async () => {
    vi.mocked(listClients).mockResolvedValue({ items: [instance()], nextCursor: '' })
    vi.mocked(getClientDetail).mockResolvedValue(instance())
    vi.mocked(listClientConnections).mockResolvedValue({ connections: [], truncated: false })

    const { container, unmount } = await mountClients()
    try {
      const row = Array.from(container.querySelectorAll('.el-table__body tr'))
        .find(entry => entry.textContent?.includes('client-local-1'))
      row?.querySelector('button')?.click()
      await flush()
      await flush()
      expect(document.querySelector('.agent-ref--empty')).not.toBeNull()
      // The shared index has nothing to resolve, so it must not be fetched.
      expect(getAgents).not.toHaveBeenCalled()
    } finally {
      unmount()
    }
  })
})

describe('client observability summary', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(listClients).mockReset()
    vi.mocked(getClientDetail).mockReset()
    vi.mocked(listClientConnections).mockReset()
    vi.mocked(getAgents).mockReset()
    // Views that resolve agent names share one index; a test that cares about
    // the names it overwrites this with the page it wants.
    vi.mocked(getAgents).mockResolvedValue({ items: [], nextCursor: '' })
  })

  it('renders the server summary for the whole population, not the loaded page', async () => {
    vi.mocked(listClients).mockResolvedValue({
      items: [instance({ id: 'a', instanceId: 'client-a' })],
      summary: { total: 27, online: 6, activeConnections: 16, activeStreams: 233, metadataUnavailable: 1, metadataStale: 0 },
      nextCursor: 'cursor-1',
      hasMore: true,
    })
    const { container, unmount } = await mountClients()
    try {
      const cards = summaryCards(container)
      expect(cards.map(card => card.value)).toEqual(['6', '16', '233', '1', '0'])
      expect(cards.map(card => card.label)).toEqual([
        i18n.global.t('clients.onlineClients'),
        i18n.global.t('clients.activeConnections'),
        i18n.global.t('clients.activeStreams'),
        i18n.global.t('clients.metadataUnavailable'),
        i18n.global.t('clients.metadataStale'),
      ])
    } finally {
      unmount()
    }
  })

  it('keeps every summary card at zero when the server reports no activity', async () => {
    vi.mocked(listClients).mockResolvedValue({
      items: [instance({ status: 'offline', metadataState: 'expired', activeConnections: 0, activeStreams: 0 })],
      summary: { total: 0, online: 0, activeConnections: 0, activeStreams: 0, metadataUnavailable: 0, metadataStale: 0 },
      nextCursor: '',
    })
    const { container, unmount } = await mountClients()
    try {
      expect(summaryCards(container).map(card => card.value)).toEqual(['0', '0', '0', '0', '0'])
    } finally {
      unmount()
    }
  })

  it('shows an online client with lapsed metadata as online plus an expired tag', async () => {
    vi.mocked(listClients).mockResolvedValue({
      items: [
        instance({ id: 'a', instanceId: 'client-a', status: 'online', metadataState: 'expired' }),
        instance({ id: 'b', instanceId: 'client-b', status: 'online', metadataState: 'unavailable' }),
        instance({ id: 'c', instanceId: 'client-c', status: 'offline', metadataState: 'unavailable', serverNodeIds: [] }),
      ],
      summary: { total: 3, online: 2, activeConnections: 2, activeStreams: 2, metadataUnavailable: 2, metadataStale: 1 },
      nextCursor: '',
    })
    const { container, unmount } = await mountClients()
    try {
      const rows = Array.from(container.querySelectorAll('.el-table__body tr'))
      const byInstanceId = new Map(rows.map(row => [row.querySelector('td')?.textContent?.trim() ?? '', row.textContent ?? '']))
      expect(byInstanceId.get('client-a')).toContain(i18n.global.t('clients.statusLabel.online'))
      expect(byInstanceId.get('client-a')).toContain(i18n.global.t('clients.metadataStateLabel.expired'))
      expect(byInstanceId.get('client-b')).toContain(i18n.global.t('clients.metadataStateLabel.unavailable'))
      expect(byInstanceId.get('client-c')).toContain(i18n.global.t('clients.statusLabel.offline'))
      // A dead row must never be presented as merely "metadata expired".
      expect(byInstanceId.get('client-c')).not.toContain(i18n.global.t('clients.metadataStateLabel.expired'))
    } finally {
      unmount()
    }
  })
})

// The detail drawer lists every stored lease row, including rows whose TTL
// already lapsed. Rendering them without a liveness marker is what made the
// drawer contradict the list ("0 active connections" beside four connections),
// and offering Close on an expired lease can only ever return a stale epoch.
describe('client connection lease state', () => {
  it('marks expired leases and hides the close action for them', () => {
    const source = readFileSync('src/views/Clients.vue', 'utf8')
    expect(source).toContain("t('clients.leaseState')")
    expect(source).toContain('leaseStateLabel')
    expect(source).toContain('leaseLive')
    // The predicate must match the server-side activeConnections rule in
    // internal/server/client_api.go, which compares expiresAt against now.
    expect(source).toMatch(/expiresAt/)
    expect(source).toMatch(/v-if="[^"]*leaseLive/)
  })

  it('refreshes the liveness baseline when the drawer reloads connections', () => {
    const source = readFileSync('src/views/Clients.vue', 'utf8')
    expect(source).toContain('connectionsNow')
  })

  it('translates the lease state and stale summary in both locales', async () => {
    const { default: zhCN } = await import('../i18n/messages/zh-CN')
    const { default: enUS } = await import('../i18n/messages/en-US')
    for (const locale of [zhCN, enUS]) {
      expect(Object.keys(locale.clients)).toContain('metadataStale')
      expect(Object.keys(locale.clients)).toContain('leaseState')
      expect(Object.keys(locale.clients.leaseStateLabel).sort()).toEqual(['expired', 'live'])
    }
  })

  it('renders a live lease and an expired lease differently', async () => {
    const now = Date.now()
    const connections: ClientConnection[] = [
      {
        connectionId: 'conn-live', clientInstanceId: 'client-instance-1', tokenId: 'token-1',
        ownerUserId: 'owner-1', serverNodeId: 'server-1', connectionEpoch: 2147483647,
        activeStreams: 2, healthScore: 100, acquiredAt: new Date(now - 60_000).toISOString(),
        lastHeartbeatAt: new Date(now - 5_000).toISOString(), expiresAt: new Date(now + 60_000).toISOString(),
        local: true,
      },
      {
        connectionId: 'conn-expired', clientInstanceId: 'client-instance-1', tokenId: 'token-1',
        ownerUserId: 'owner-1', serverNodeId: 'server-1', connectionEpoch: 2147483647,
        activeStreams: 84, healthScore: 100, acquiredAt: new Date(now - 600_000).toISOString(),
        lastHeartbeatAt: new Date(now - 300_000).toISOString(), expiresAt: new Date(now - 240_000).toISOString(),
        local: true,
      },
    ]
    vi.mocked(listClients).mockResolvedValue({ items: [instance()], nextCursor: '' })
    vi.mocked(getClientDetail).mockResolvedValue(instance())
    vi.mocked(listClientConnections).mockResolvedValue({ connections, truncated: false })

    const { container, unmount } = await mountClients()
    try {
      const detail = Array.from(container.querySelectorAll('.el-table__body tr'))
        .find(row => row.textContent?.includes('client-local-1'))
      detail?.querySelector('button')?.click()
      await flush()
      await flush()
      const text = document.body.textContent || ''
      expect(text).toContain(i18n.global.t('clients.leaseStateLabel.live'))
      expect(text).toContain(i18n.global.t('clients.leaseStateLabel.expired'))
      const closeLabels = Array.from(document.body.querySelectorAll('button'))
        .filter(button => button.textContent?.includes(i18n.global.t('clients.closeConnection')))
      expect(closeLabels).toHaveLength(1)
      // The expired row still carries the counter it last reported, and showing it
      // as an active-stream number is what made the drawer look like it was
      // counting clients that had already gone away.
      const leaseRows = Array.from(document.body.querySelectorAll('.el-table__body tr'))
      const liveRow = leaseRows.find(row => row.textContent?.includes('conn-live'))
      const expiredRow = leaseRows.find(row => row.textContent?.includes('conn-expired'))
      expect(liveRow?.textContent).toContain('2')
      expect(expiredRow?.textContent).not.toContain('84')
    } finally {
      unmount()
    }
  })
})

// Expired leases used to arrive in the same unbounded list as live ones, so a
// Client that was killed instead of closed left "已过期" rows in every drawer
// forever. The default view is live-only now, and history is an explicit,
// capped request that the operator opts into.
describe('client lease history', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(listClients).mockReset()
    vi.mocked(getClientDetail).mockReset()
    vi.mocked(listClientConnections).mockReset()
    vi.mocked(getAgents).mockReset()
    vi.mocked(getAgents).mockResolvedValue({ items: [], nextCursor: '' })
  })

  function lease(connectionId: string, heartbeatAgoMs: number): ClientConnection {
    const now = Date.now()
    return {
      connectionId, clientInstanceId: 'client-instance-1', tokenId: 'token-1',
      ownerUserId: 'owner-1', serverNodeId: 'server-1', connectionEpoch: 7,
      activeStreams: 1, healthScore: 100,
      acquiredAt: new Date(now - heartbeatAgoMs - 60_000).toISOString(),
      lastHeartbeatAt: new Date(now - heartbeatAgoMs).toISOString(),
      expiresAt: new Date(now - heartbeatAgoMs + 60_000).toISOString(),
      local: true,
    }
  }

  async function openDrawer(container: HTMLElement) {
    const row = Array.from(container.querySelectorAll('.el-table__body tr'))
      .find(entry => entry.textContent?.includes('client-local-1'))
    row?.querySelector('button')?.click()
    await flush()
    await flush()
  }

  function historySwitch() {
    return document.querySelector('.connection-history .el-switch') as HTMLElement
  }

  it('lists live leases by default and history only when asked', async () => {
    const live = lease('conn-live', 5_000)
    const history = [live, lease('conn-dead-1', 90_000), lease('conn-dead-2', 120_000)]
    vi.mocked(listClients).mockResolvedValue({ items: [instance()], nextCursor: '' })
    vi.mocked(getClientDetail).mockResolvedValue(instance())
    vi.mocked(listClientConnections).mockImplementation(async (_id, params = {}) => (
      params.state === 'all'
        ? { connections: history, truncated: false }
        : { connections: [live], truncated: false }
    ))

    const { container, unmount } = await mountClients()
    try {
      await openDrawer(container)
      let text = document.body.textContent || ''
      expect(text).toContain('conn-live')
      expect(text).not.toContain('conn-dead-1')
      expect(listClientConnections).toHaveBeenLastCalledWith('client-instance-1', { state: 'live', limit: 50 })

      historySwitch().dispatchEvent(new MouseEvent('click'))
      await flush()
      await flush()
      text = document.body.textContent || ''
      expect(text).toContain('conn-dead-1')
      expect(text).toContain('conn-dead-2')
      expect(listClientConnections).toHaveBeenLastCalledWith('client-instance-1', { state: 'all', limit: 50 })
      // The lease-state column keeps its meaning in the history view: two of
      // these rows are expired facts, not connections.
      const expiredTags = Array.from(document.body.querySelectorAll('.status-tag'))
        .filter(tag => (tag.textContent || '').trim() === i18n.global.t('clients.leaseStateLabel.expired'))
      expect(expiredTags).toHaveLength(2)
    } finally {
      unmount()
    }
  })

  it('widens the window when the history is capped', async () => {
    vi.mocked(listClients).mockResolvedValue({ items: [instance()], nextCursor: '' })
    vi.mocked(getClientDetail).mockResolvedValue(instance())
    vi.mocked(listClientConnections).mockResolvedValue({ connections: [lease('conn-dead-1', 90_000)], truncated: true })

    const { container, unmount } = await mountClients()
    try {
      await openDrawer(container)
      expect(document.body.textContent || '').toContain(i18n.global.t('clients.connectionsTruncated', { count: 50 }))

      const loadMore = Array.from(document.body.querySelectorAll('button'))
        .find(button => (button.textContent || '').trim() === i18n.global.t('clients.loadMore'))!
      loadMore.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      await flush()
      await flush()
      expect(listClientConnections).toHaveBeenLastCalledWith('client-instance-1', { state: 'live', limit: 100 })
    } finally {
      unmount()
    }
  })
})
