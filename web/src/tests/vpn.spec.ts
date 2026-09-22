import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { readFileSync } from 'node:fs'
import VpnPeers from '../views/VpnPeers.vue'
import { i18n } from '../i18n'
import { check, click, flush, setValue, stubApi, type ApiStub } from './api-stub'

// PageHeader reads the current route for its breadcrumb, and the view is
// mounted on its own rather than through the router.
vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRoute: () => ({ path: '/vpn', params: {}, query: {} }) }
})

const peer = {
  id: 'p-1', ownerUserId: 'u-1', name: 'laptop', description: 'road warrior',
  publicKey: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=', vpnIp: '10.64.0.2', nodeId: 'n-1',
  agentId: 'a-1', allowedIps: ['10.0.0.0/8'], allowedPorts: [443], allowPrivateTargets: false,
  icmpEnabled: false, maxConcurrentFlows: 64, packetRateLimit: 0, expiresAt: null,
  status: 'active', createdAt: '2026-09-22T00:00:00Z', updatedAt: '2026-09-22T00:00:00Z',
}
const agent = { id: 'a-1', name: 'edge-1', enabled: true, status: 'active', ownerUserId: 'u-1' }
const rendered = '[Interface]\nPrivateKey = SECRET-PLAINTEXT\nAddress = 10.64.0.2/32\n'

function stubVpn(overrides: ApiStub[] = []) {
  return stubApi([
    ...overrides,
    { path: '/vpn-peers', data: { items: [peer], nextCursor: '', hasMore: false } },
    { path: '/vpn-nodes', status: 501, data: { error: 'vpn_not_implemented' } },
    { path: '/agents', data: { items: [agent] } },
  ])
}

async function mountVpn() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const pinia = createPinia()
  setActivePinia(pinia)
  const app = createApp(VpnPeers)
  app.use(pinia)
  app.use(i18n)
  app.mount(container)
  await flush()
  return { container, unmount: () => { app.unmount(); container.remove() } }
}

const t = (key: string) => i18n.global.t(key)

// el-form-item debounces its validation state by 100ms before it renders the
// message, so an assertion on the localized text has to wait for it. The
// is-error class is immediate; the message is what proves the text is ours.
async function settle(milliseconds = 160) {
  await new Promise(resolve => setTimeout(resolve, milliseconds))
  await flush()
}

beforeEach(() => {
  document.body.innerHTML = ''
  localStorage.clear()
  vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText: vi.fn(async () => {}) } })
})

describe('vpn peers console', () => {
  it('lists peers and says plainly that this release has no data plane', async () => {
    stubVpn()
    const { container, unmount } = await mountVpn()
    try {
      expect(container.textContent).toContain('laptop')
      expect(container.textContent).toContain('10.64.0.2')
      expect(container.textContent).toContain(t('vpn.dataPlanePending'))
    } finally { unmount() }
  })

  it('reports the pool overview as unavailable rather than as zero allocated', async () => {
    stubVpn()
    const { container, unmount } = await mountVpn()
    try {
      expect(container.querySelector('.pool-overview')).toBeNull()
      expect(container.textContent).toContain(t('vpn.poolUnavailable'))
    } finally { unmount() }
  })

  it('renders the pool overview when the gateway runtime reports one', async () => {
    stubVpn([{ path: '/vpn-nodes', data: { items: [{ nodeId: 'n-1', enabled: true, subnet: '10.64.0.0/24', allocated: 3, capacity: 253, peers: 3, icmpCapable: false }] } }])
    const { container, unmount } = await mountVpn()
    try {
      expect(container.querySelector('.pool-overview')?.textContent).toContain('10.64.0.0/24')
      expect(container.querySelector('.pool-overview')?.textContent).toContain('3')
    } finally { unmount() }
  })

  it('refuses to submit a create form whose cidr list is malformed', async () => {
    const calls = stubVpn()
    const { unmount } = await mountVpn()
    try {
      await click('.vpn-create', document.body)
      await setValue('.vpn-name input', document.body, 'laptop')
      await setValue('.vpn-cidrs input', document.body, 'not-a-cidr')
      await click('.vpn-submit', document.body)
      expect(calls.filter(call => call.method === 'POST' && call.path === '/vpn-peers')).toHaveLength(0)
      expect(document.body.querySelector('.vpn-cidrs')?.closest('.el-form-item')?.classList.contains('is-error')).toBe(true)
      await settle()
      expect(document.body.textContent).toContain(t('vpn.form.cidrInvalid'))
    } finally { unmount() }
  })

  it('reveals a configuration only after a typed confirmation and a risk acknowledgement', async () => {
    const calls = stubVpn([{ method: 'POST', path: '/vpn-peers/p-1/config:reveal', data: { id: 'p-1', name: 'laptop', vpnIp: '10.64.0.2', format: 'wg-quick', config: rendered } }])
    const { unmount } = await mountVpn()
    try {
      await click('.vpn-usage', document.body)
      // The button stays inert until both preconditions the server enforces are
      // visible on screen as filled in.
      await click('.vpn-reveal-submit', document.body)
      expect(calls.filter(call => call.path === '/vpn-peers/p-1/config:reveal')).toHaveLength(0)

      await check('.vpn-reveal-ack', document.body)
      await setValue('.vpn-reveal-confirm input', document.body, 'REVEAL')
      await click('.vpn-reveal-submit', document.body)

      expect(calls.filter(call => call.path === '/vpn-peers/p-1/config:reveal')).toHaveLength(1)
      expect(document.body.textContent).toContain('SECRET-PLAINTEXT')
    } finally { unmount() }
  })

  it('clears the revealed private key when the usage drawer closes', async () => {
    stubVpn([{ method: 'POST', path: '/vpn-peers/p-1/config:reveal', data: { id: 'p-1', name: 'laptop', vpnIp: '10.64.0.2', format: 'wg-quick', config: rendered } }])
    const { unmount } = await mountVpn()
    try {
      await click('.vpn-usage', document.body)
      await check('.vpn-reveal-ack', document.body)
      await setValue('.vpn-reveal-confirm input', document.body, 'REVEAL')
      await click('.vpn-reveal-submit', document.body)
      expect(document.body.textContent).toContain('SECRET-PLAINTEXT')

      await click('.vpn-usage-close', document.body)
      await flush()
      expect(document.body.textContent).not.toContain('SECRET-PLAINTEXT')
      expect(localStorage.length).toBe(0)
    } finally { unmount() }
  })

  it('renders a node without a gateway identity as information, not as a failure', async () => {
    stubVpn([{ method: 'POST', path: '/vpn-peers/p-1/config:reveal', status: 409, data: { error: 'vpn_node_disabled' } }])
    const { unmount } = await mountVpn()
    try {
      await click('.vpn-usage', document.body)
      await check('.vpn-reveal-ack', document.body)
      await setValue('.vpn-reveal-confirm input', document.body, 'REVEAL')
      await click('.vpn-reveal-submit', document.body)
      expect(document.body.textContent).toContain(t('vpn.errors.nodeDisabled'))
    } finally { unmount() }
  })

  it('keeps the one-time key material out of persistent storage and out of the list', async () => {
    const source = readFileSync('src/views/VpnPeers.vue', 'utf8')
    expect(source).not.toContain('localStorage')
    expect(source).not.toContain('sessionStorage')
    expect(source).toContain('clearRevealedConfig')
    expect(source).toContain('ElMessageBox.confirm')
    expect(source).toContain('rotateVpnPeer')
    expect(source).toContain('revokeVpnPeer')
    // Active flows are a data-plane fact; the list must not grow a column that
    // this release can only answer with 501.
    expect(source).not.toContain('listVpnPeerFlows')
    expect(source).toContain("t('vpn.usage.flowsHint')")
  })
})
