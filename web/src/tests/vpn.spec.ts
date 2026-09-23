import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { readFileSync } from 'node:fs'
import VpnPeers from '../views/VpnPeers.vue'
import { i18n } from '../i18n'
import zhCN from '../i18n/messages/zh-CN'
import enUS from '../i18n/messages/en-US'
import { callsTo, check, click, flush, setValue, stubApi, type ApiStub } from './api-stub'

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
// What the gateway runtime reports about the node this API speaks for. Every
// field is present because a partial payload has its own test below.
const node = {
  nodeId: 'n-1', enabled: true, listen: '0.0.0.0:51820', endpointHost: 'vpn.example.com',
  subnet: '10.64.0.0/24', allocated: 3, capacity: 253, peers: 3, icmpCapable: true,
}
const flow = {
  id: 'f-1', protocol: 'tcp', target: '10.0.0.7', port: 443,
  startedAt: '2026-09-23T08:00:00Z', bytesSent: 2048, bytesReceived: 4096,
}

function stubVpn(overrides: ApiStub[] = []) {
  return stubApi([
    ...overrides,
    { path: '/vpn-peers', data: { items: [peer], nextCursor: '', hasMore: false } },
    { path: '/vpn-peers/p-1/flows', data: { items: [flow] } },
    { path: '/vpn-nodes', data: { items: [node] } },
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

// The row action buttons carry no class except the usage one, and adding a
// selector hook to production markup just to reach a button is worse than
// finding it by the label the user reads.
async function clickButton(label: string, root: ParentNode) {
  const found = [...root.querySelectorAll('button')].find(button => button.textContent?.trim() === label)
  if (!found) throw new Error(`missing button ${label}`)
  found.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
  return flush()
}

beforeEach(() => {
  document.body.innerHTML = ''
  localStorage.clear()
  vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText: vi.fn(async () => {}) } })
})

describe('vpn peers console', () => {
  it('lists peers without a standing disclaimer about a missing data plane', async () => {
    stubVpn()
    const { container, unmount } = await mountVpn()
    try {
      expect(container.textContent).toContain('laptop')
      expect(container.textContent).toContain('10.64.0.2')
      expect(container.querySelector('.phase-notice')).toBeNull()
      // The key is gone from both locales rather than merely unrendered: a key
      // that survives a release which can carry traffic gets reused by the next
      // banner somebody adds, and then the console lies about the build again.
      for (const locale of [zhCN, enUS]) {
        expect(Object.keys(locale.vpn)).not.toContain('dataPlanePending')
      }
    } finally { unmount() }
  })

  it('reports the pool overview as unavailable when the node status cannot be read', async () => {
    stubVpn([{ path: '/vpn-nodes', status: 500, data: { error: 'internal' } }])
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

  it('renders the active flows of one peer in a drawer of their own', async () => {
    const calls = stubVpn()
    const { unmount } = await mountVpn()
    try {
      await clickButton(t('vpn.flowsOpen'), document.body)
      expect(callsTo(calls, 'GET', '/vpn-peers/p-1/flows')).toHaveLength(1)
      const drawer = document.body.querySelector('.flows-body')
      expect(drawer?.textContent).toContain('10.0.0.7')
      expect(drawer?.textContent).toContain('443')
      // Byte counters are printed exactly as the gateway reports them: the number
      // an operator compares against tunnelmesh_bytes_total must not be rounded.
      expect(drawer?.textContent).toContain('2048')
      expect(drawer?.textContent).toContain('4096')
      expect(drawer?.textContent).not.toContain(t('vpn.errors.notImplemented'))
    } finally { unmount() }
  })

  it('renders a peer this node cannot observe as information rather than as no traffic', async () => {
    stubVpn([{ path: '/vpn-peers/p-1/flows', status: 501, data: { error: 'vpn_not_implemented' } }])
    const { unmount } = await mountVpn()
    try {
      await clickButton(t('vpn.flowsOpen'), document.body)
      const notice = document.body.querySelector('.flows-notice')
      expect(notice?.classList.contains('el-alert--info')).toBe(true)
      expect(notice?.textContent).toContain(t('vpn.errors.notImplemented'))
      // An empty table here would read as "the peer is idle", which is the one
      // conclusion the 501 rules out.
      expect(document.body.querySelector('.flows-body')?.textContent).not.toContain('10.0.0.7')
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
    // Active flows are a data-plane fact this node can now answer, so they are
    // read from the gateway instead of being delegated to a dashboard.
    expect(source).toContain('listVpnPeerFlows')
    expect(source).toContain("t('vpn.usage.flowsHint')")
  })
  it('replays one idempotency key across save retries and mints a new one per dialog', async () => {
    const calls = stubVpn([
      { method: 'PATCH', path: '/vpn-peers/p-1', statuses: [500, 200], sequence: [{ error: 'internal' }, peer] },
    ])
    const { unmount } = await mountVpn()
    try {
      await clickButton(t('vpn.edit'), document.body)
      await click('.vpn-submit', document.body)
      await settle()
      await click('.vpn-submit', document.body)
      await settle()

      const patches = callsTo(calls, 'PATCH', '/vpn-peers/p-1')
      expect(patches).toHaveLength(2)
      const firstKey = patches[0]?.headers.get('Idempotency-Key')
      expect(firstKey).toBeTruthy()
      // A lost response must not become a second operation: the server's
      // idempotency store can only replay what it is asked to replay twice.
      expect(patches[1]?.headers.get('Idempotency-Key')).toBe(firstKey)

      await clickButton(t('vpn.edit'), document.body)
      await click('.vpn-submit', document.body)
      await settle()
      const third = callsTo(calls, 'PATCH', '/vpn-peers/p-1')[2]
      expect(third?.headers.get('Idempotency-Key')).toBeTruthy()
      expect(third?.headers.get('Idempotency-Key')).not.toBe(firstKey)
    } finally { unmount() }
  })

})
