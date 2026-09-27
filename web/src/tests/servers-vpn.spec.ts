import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import Servers from '../views/Servers.vue'
import { i18n } from '../i18n'
import { flush, stubApi, type ApiStub } from './api-stub'

// PageHeader reads the current route for its breadcrumb and the view is mounted
// on its own rather than through the router.
vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRoute: () => ({ path: '/servers', params: {}, query: {} }) }
})

const node = {
  id: 'n-1', name: 'edge-1', address: 'relay-a.internal:8443', epoch: 3, status: 'online',
  activeConnections: 2, activeStreams: 1, healthScore: 98, enabled: true,
  lastSeenAt: '2026-09-22T08:00:00Z', expiresAt: '2026-09-22T08:01:00Z',
  createdAt: '2026-09-20T00:00:00Z', updatedAt: '2026-09-22T08:00:00Z', deletedAt: null,
}

// Servers.vue loads its own table first, so the node list has to be stubbed
// alongside the gateway status: stubApi throws on an undeclared request.
function stubServers(vpnStub: ApiStub) {
  return stubApi([{ path: '/server-nodes', data: { items: [node], nextCursor: '', hasMore: false } }, vpnStub])
}

async function mountServers() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const pinia = createPinia()
  setActivePinia(pinia)
  const app = createApp(Servers)
  app.use(pinia)
  app.use(i18n)
  app.mount(container)
  await flush()
  return { container, unmount: () => { app.unmount(); container.remove() } }
}

const t = (key: string) => i18n.global.t(key)

beforeEach(() => {
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('vpn gateway status on the node page', () => {
  it('renders subnet, allocation, capacity, peers, and the icmp capability', async () => {
    const calls = stubServers({
      path: '/vpn-nodes',
      data: {
        items: [{
          nodeId: 'n-1', enabled: true, listen: '0.0.0.0:51820', endpointHost: 'vpn.example.com',
          subnet: '10.64.0.0/22', allocated: 18, capacity: 1022, peers: 4, icmpCapable: true,
        }],
      },
    })
    const { container, unmount } = await mountServers()
    try {
      const section = container.querySelector('.vpn-status')
      expect(section?.textContent).toContain(t('servers.vpn.title'))
      expect(section?.textContent).toContain('10.64.0.0/22')
      expect(section?.textContent).toContain('18')
      expect(section?.textContent).toContain('1022')
      expect(section?.textContent).toContain('4')
      expect(section?.textContent).toContain(t('common.yes'))
      expect(calls.filter(call => call.path === '/vpn-nodes')).toHaveLength(1)
    } finally { unmount() }
  })

  it('reports 501 as a state and never as an allocated count of zero', async () => {
    stubServers({ path: '/vpn-nodes', status: 501, data: { error: 'vpn_not_implemented' } })
    const { container, unmount } = await mountServers()
    try {
      const section = container.querySelector('.vpn-status')
      expect(section?.textContent).toContain(t('servers.vpn.unavailable'))
      // No grid at all: a missing runtime is not "zero of everything", and the
      // node table must stay readable, so the 501 is information, not an error.
      expect(section?.querySelectorAll('table, dl').length).toBe(0)
      expect(section?.querySelectorAll('.vpn-metric').length).toBe(0)
      // "501" in the copy keeps its zero between digits; a rendered counter
      // would leave one standing alone.
      expect(section?.textContent ?? '').not.toMatch(/(^|\D)0(\D|$)/)
      expect(container.textContent).not.toContain(t('common.loadFailed'))
    } finally { unmount() }
  })

  it('shows a placeholder for a partial payload instead of an empty cell', async () => {
    stubServers({ path: '/vpn-nodes', data: { items: [{ nodeId: 'n-1' }] } })
    const { container, unmount } = await mountServers()
    try {
      const section = container.querySelector('.vpn-status')
      expect(section?.textContent).toContain('n-1')
      expect(section?.textContent).toContain('—')
      expect(section?.textContent).toContain(t('servers.vpn.disabled'))
    } finally { unmount() }
  })

  it('says plainly when no node serves the gateway', async () => {
    stubServers({ path: '/vpn-nodes', data: { items: [] } })
    const { container, unmount } = await mountServers()
    try {
      expect(container.querySelector('.vpn-status')?.textContent).toContain(t('servers.vpn.empty'))
    } finally { unmount() }
  })

  it('translates the gateway block in both locales', async () => {
    const zhCN = (await import('../i18n/messages/zh-CN')).default
    const enUS = (await import('../i18n/messages/en-US')).default
    for (const locale of [zhCN, enUS]) {
      for (const key of ['title', 'description', 'node', 'listen', 'endpoint', 'subnet', 'allocated',
        'capacity', 'peers', 'icmp', 'enabled', 'disabled', 'empty', 'unavailable', 'loadFailed']) {
        expect(typeof locale.servers.vpn[key], key).toBe('string')
        expect(locale.servers.vpn[key].trim().length, key).toBeGreaterThan(0)
      }
    }
    expect(Object.keys(zhCN.servers.vpn).sort()).toEqual(Object.keys(enUS.servers.vpn).sort())
  })
})
