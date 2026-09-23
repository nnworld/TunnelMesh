import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createPinia } from 'pinia'
import { readFileSync } from 'node:fs'
import Clients from '../views/Clients.vue'
import { i18n } from '../i18n'
import { getClientDetail, listClientConnections, listClients, type ClientConnection, type ClientInstance } from '../api/client'
import { useAuthStore } from '../stores/auth'

vi.mock('vue-router', () => ({ useRoute: () => ({ path: '/clients', params: {} }) }))
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return { ...actual, listClients: vi.fn(), getClientDetail: vi.fn(), listClientConnections: vi.fn() }
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
    hostname: 'host-a', processStartAt: '2026-09-23T10:00:00Z', status: 'online',
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

// The summary cards and the table rows used to disagree because both were
// derived from leases whose epoch-predicated heartbeat write silently matched
// zero rows on MySQL. The card set now accounts for every status the table can
// show, so a "metadata expired" row is visible in the summary too.
describe('client observability summary', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(listClients).mockReset()
    vi.mocked(getClientDetail).mockReset()
    vi.mocked(listClientConnections).mockReset()
  })

  it('counts online clients, connections, streams, and both metadata states', async () => {
    vi.mocked(listClients).mockResolvedValue({
      items: [
        instance({ id: 'a', instanceId: 'client-a', status: 'online', activeConnections: 2, activeStreams: 5 }),
        instance({ id: 'b', instanceId: 'client-b', status: 'stale', activeConnections: 0, activeStreams: 0, serverNodeIds: [] }),
        instance({ id: 'c', instanceId: 'client-c', status: 'metadata_unavailable', activeConnections: 1, activeStreams: 0 }),
        instance({ id: 'd', instanceId: 'client-d', status: 'offline', activeConnections: 0, activeStreams: 0, serverNodeIds: [] }),
      ],
      nextCursor: '',
    })
    const { container, unmount } = await mountClients()
    try {
      const cards = summaryCards(container)
      expect(cards.map(card => card.value)).toEqual(['2', '3', '5', '1', '1'])
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

  it('keeps every summary card at zero when no client holds a live lease', async () => {
    vi.mocked(listClients).mockResolvedValue({
      items: [instance({ status: 'stale', activeConnections: 0, activeStreams: 0, serverNodeIds: [] })],
      nextCursor: '',
    })
    const { container, unmount } = await mountClients()
    try {
      expect(summaryCards(container).map(card => card.value)).toEqual(['0', '0', '0', '0', '1'])
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
        activeStreams: 0, healthScore: 100, acquiredAt: new Date(now - 600_000).toISOString(),
        lastHeartbeatAt: new Date(now - 300_000).toISOString(), expiresAt: new Date(now - 240_000).toISOString(),
        local: true,
      },
    ]
    vi.mocked(listClients).mockResolvedValue({ items: [instance()], nextCursor: '' })
    vi.mocked(getClientDetail).mockResolvedValue(instance())
    vi.mocked(listClientConnections).mockResolvedValue(connections)

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
    } finally {
      unmount()
    }
  })
})
