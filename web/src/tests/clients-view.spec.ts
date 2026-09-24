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
describe('client observability summary', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(listClients).mockReset()
    vi.mocked(getClientDetail).mockReset()
    vi.mocked(listClientConnections).mockReset()
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
