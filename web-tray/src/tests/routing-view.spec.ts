import { describe, expect, it } from 'vitest'
import type { VueWrapper } from '@vue/test-utils'
import RoutingView from '../views/RoutingView.vue'
import type { TunnelView } from '../api/types'
import { useRoutingStore } from '../stores/routing'
import { useSettingsStore } from '../stores/settings'
import { fixtures, flush, installFakeTray, mountView } from './helpers'

function seed(overrides: Parameters<typeof fixtures.routing>[0] = {}) {
  useSettingsStore().adopt(fixtures.settings())
  useRoutingStore().adopt(fixtures.routing(overrides))
}

async function click(wrapper: VueWrapper, target: string) {
  await wrapper.find(target).trigger('click')
  await flush(6)
}

describe('routing tab', () => {
  it('renders the server address, a masked token and every tunnel', () => {
    const tray = installFakeTray({ 'GET /api/agents': fixtures.agents() })
    const { wrapper } = mountView(RoutingView, { locale: 'zh-CN', prepare: () => seed() })

    // ElInput forwards unknown attributes to the inner <input>, so the data-test hook is
    // the element under test.
    const serverUrl = wrapper.find('[data-test="server-url"]')
    expect(serverUrl.exists()).toBe(true)
    expect((serverUrl.element as HTMLInputElement).value).toBe('wss://mesh.example.com/ws/client')

    // The token field is a password input: the stored secret is never rendered, and the
    // placeholder says a token exists rather than echoing it.
    const token = wrapper.find('[data-test="token"]')
    expect(token.attributes('type')).toBe('password')
    expect((token.element as HTMLInputElement).value).toBe('')
    expect((token.element as HTMLInputElement).placeholder).toContain('已保存 token')
    expect(wrapper.find('[data-test="token-state"]').text()).toContain('已保存')

    expect(wrapper.find('[data-test="tunnel-0"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="tunnel-1"]').exists()).toBe(true)
    // Requirement: entries are separated by a rule.
    expect(wrapper.findAll('[data-test="tunnel-divider"]')).toHaveLength(1)
    tray.restore()
    wrapper.unmount()
  })

  it('offers every protocol the runtime supports', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(RoutingView, { prepare: () => seed() })
    const select = wrapper.findComponent('[data-test="protocol-0"]')
    expect(select.exists()).toBe(true)
    const options = wrapper.findAllComponents({ name: 'ElOption' })
    const values = options.map((option) => (option.props('value') as string)).filter((value) =>
      ['tcp', 'udp', 'http', 'socks5', 'http-proxy'].includes(value),
    )
    expect(new Set(values)).toEqual(new Set(['tcp', 'udp', 'http', 'socks5', 'http-proxy']))
    tray.restore()
    wrapper.unmount()
  })

  it('shows the target only for the raw forwarders', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(RoutingView, { prepare: () => seed() })
    expect(wrapper.find('[data-test="target-host-0"]').exists()).toBe(true)
    // Tunnel 1 is socks5: it resolves its destination per request, so it has no target.
    expect(wrapper.find('[data-test="target-host-1"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="auth-mode-1"]').exists()).toBe(true)
    tray.restore()
    wrapper.unmount()
  })

  it('loads the agent picker from the server and marks who is online', async () => {
    const tray = installFakeTray({ 'GET /api/agents': fixtures.agents() })
    const { wrapper } = mountView(RoutingView, { prepare: () => seed() })
    // The mount-time fetch has to settle first: while it is in flight the button renders
    // as loading, and Element Plus disables a loading button, so clicking it would be a
    // no-op rather than the explicit refresh this test is about.
    await flush(6)
    await click(wrapper, '[data-test="load-agents"]')

    // One call from the mount-time prefetch (a configured server plus a stored token is
    // enough to make it worthwhile) and one from the explicit refresh.
    const calls = tray.calls.filter((call) => call.path === '/api/agents')
    expect(calls).toHaveLength(2)
    expect(useRoutingStore().agents).toHaveLength(2)

    const select = wrapper.findComponent('[data-test="agent-0"]')
    const options = select.findAllComponents({ name: 'ElOption' })
    expect(options.map((option) => option.props('value'))).toEqual(['agent-a', 'agent-b'])
    expect(options[0].text()).toContain('office')
    expect(options[0].text()).toContain('online')
    expect(options[1].text()).toContain('offline')
    tray.restore()
    wrapper.unmount()
  })

  it('surfaces an agent list failure without losing the form', async () => {
    const tray = installFakeTray(
      {},
      { 'GET /api/agents': { status: 400, msg: 'tray: the server rejected the client token' } },
    )
    const { wrapper } = mountView(RoutingView, { prepare: () => seed() })
    await click(wrapper, '[data-test="load-agents"]')
    expect(wrapper.find('[data-test="routing-error"]').text()).toContain('rejected the client token')
    expect(useRoutingStore().tunnels).toHaveLength(2)
    tray.restore()
    wrapper.unmount()
  })

  it('adds and removes tunnels', async () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(RoutingView, { prepare: () => seed() })
    expect(wrapper.findAll('.tm-tunnel')).toHaveLength(2)

    await click(wrapper, '[data-test="add-tunnel"]')
    expect(wrapper.findAll('.tm-tunnel')).toHaveLength(3)
    expect(wrapper.findAll('[data-test="tunnel-divider"]')).toHaveLength(2)
    // A new entry defaults to a TCP forward on loopback.
    const added = useRoutingStore().tunnels[2]
    expect(added.protocol).toBe('tcp')
    expect(added.listen).toBe('127.0.0.1:')

    await click(wrapper, '[data-test="remove-1"]')
    expect(useRoutingStore().tunnels).toHaveLength(2)
    expect(useRoutingStore().tunnels[1].protocol).toBe('tcp')
    tray.restore()
    wrapper.unmount()
  })

  it('renders an itemised validation report', async () => {
    const tray = installFakeTray({
      'POST /api/validate': fixtures.report(),
      'GET /api/agents': fixtures.agents(),
    })
    const { wrapper } = mountView(RoutingView, { prepare: () => seed() })
    expect(wrapper.find('[data-test="validation-empty"]').exists()).toBe(true)

    await click(wrapper, '[data-test="validate"]')

    const rows = wrapper.findAll('[data-test="validation-results"] .tm-check')
    expect(rows).toHaveLength(4)
    const summary = wrapper.find('[data-test="validation-summary"]')
    expect(summary.exists()).toBe(true)
    // The data-test attribute lands on the alert's transition wrapper, so the type class
    // is read from the rendered alert inside it.
    expect(summary.find('.el-alert').classes()).toContain('el-alert--error')
    // The Go side sends stable identifiers and English detail; the labels come from i18n.
    expect(wrapper.text()).toContain('Token accepted')
    expect(wrapper.text()).toContain('the server rejected the client token')
    expect(wrapper.text()).toContain('Warning')
    expect(wrapper.text()).toContain('Skipped')
    tray.restore()
    wrapper.unmount()
  })

  it('reports a valid configuration as valid', async () => {
    const tray = installFakeTray({
      'POST /api/validate': fixtures.report({ valid: true, checks: [{ id: 'config.valid', status: 'passed' }] }),
      'GET /api/agents': fixtures.agents(),
    })
    const { wrapper } = mountView(RoutingView, { prepare: () => seed() })
    await click(wrapper, '[data-test="validate"]')
    const summary = wrapper.find('[data-test="validation-summary"]')
    expect(summary.find('.el-alert').classes()).toContain('el-alert--success')
    expect(summary.text()).toContain('Every check passed')
    tray.restore()
    wrapper.unmount()
  })

  it('validates the pending form, including an untouched token', async () => {
    const tray = installFakeTray({
      'POST /api/validate': fixtures.report({ valid: true, checks: [] }),
      'GET /api/agents': fixtures.agents(),
    })
    const { wrapper } = mountView(RoutingView, { prepare: () => seed() })
    await click(wrapper, '[data-test="validate"]')
    const validate = tray.calls.filter((call) => call.path === '/api/validate')
    expect(validate).toHaveLength(1)
    const body = validate[0].body as { mode: string; serverUrl: string; token: unknown; tunnels: unknown[] }
    expect(body.mode).toBe('local')
    expect(body.serverUrl).toBe('wss://mesh.example.com/ws/client')
    expect(body.token).toBeNull()
    expect(body.tunnels).toHaveLength(2)
    tray.restore()
    wrapper.unmount()
  })

  it('sends a typed token as a replacement and an explicit clear as an empty string', async () => {
    const tray = installFakeTray({
      'PUT /api/routing': (body: unknown) => fixtures.saveResult({ routing: fixtures.routing() }),
    })
    const { wrapper } = mountView(RoutingView, { prepare: () => seed() })
    const store = useRoutingStore()

    const writes = () => tray.calls.filter((call) => call.path === '/api/routing')

    store.tokenEdit = 'new-secret'
    store.tokenTouched = true
    await click(wrapper, '[data-test="save-routing"]')
    expect(writes()).toHaveLength(1)
    expect((writes()[0].body as { token: unknown }).token).toBe('new-secret')

    store.clearToken = true
    await click(wrapper, '[data-test="save-routing"]')
    expect(writes()).toHaveLength(2)
    expect((writes()[1].body as { token: unknown }).token).toBe('')
    tray.restore()
    wrapper.unmount()
  })

  it('reports a restart triggered by the save', async () => {
    const tray = installFakeTray({
      'PUT /api/routing': () => fixtures.saveResult({ restarted: true, routing: fixtures.routing({ running: true }) }),
    })
    const { wrapper } = mountView(RoutingView, { prepare: () => seed() })
    await click(wrapper, '[data-test="save-routing"]')
    expect(wrapper.find('[data-test="routing-notice"]').text()).toContain('restarted')
    tray.restore()
    wrapper.unmount()
  })

  it('shows a rejected save without pretending the file changed', async () => {
    const tray = installFakeTray(
      {},
      { 'PUT /api/routing': { status: 422, msg: 'tray: the routing configuration is not usable: bad listen' } },
    )
    const { wrapper } = mountView(RoutingView, { prepare: () => seed() })
    await click(wrapper, '[data-test="save-routing"]')
    expect(wrapper.find('[data-test="routing-error"]').text()).toContain('bad listen')
    expect(wrapper.find('[data-test="routing-notice"]').exists()).toBe(false)
    tray.restore()
    wrapper.unmount()
  })

  it('renders the empty state before anything is configured', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(RoutingView, {
      locale: 'zh-CN',
      prepare: () => seed({ tunnels: [], tokenPresent: false, serverUrl: '' }),
    })
    expect(wrapper.find('[data-test="tunnels-empty"]').text()).toContain('还没有隧道')
    expect(wrapper.find('[data-test="token-state"]').text()).toContain('未设置')
    expect((wrapper.find('[data-test="token"]').element as HTMLInputElement).placeholder).toContain('粘贴')
    tray.restore()
    wrapper.unmount()
  })
})

describe('routing tab text input hygiene', () => {
  // macOS text assists (auto-capitalisation, spelling correction, text substitution) and
  // the browser's own autofill both rewrite what the operator typed, and every one of them
  // is wrong for a server URL, a token or a host:port pair. Element Plus forwards unknown
  // attributes to the inner <input>, so the data-test hook is the element under test.
  const technical = [
    'server-url',
    'token',
    'name-0',
    'listen-0',
    'target-host-0',
  ]
  // The auth URL only exists for a proxy tunnel that authenticates against a remote
  // service, so the fixture has to be moved into that state to reach the field.
  const conditional = 'auth-url-1'

  it('turns the system text assists off on every technical field', async () => {
    const tray = installFakeTray({ 'GET /api/agents': fixtures.agents() })
    const { wrapper } = mountView(RoutingView, { locale: 'zh-CN', prepare: () => seed() })
    useRoutingStore().tunnels[1].authMode = 'remote'
    await flush(4)

    for (const hook of technical) {
      const field = wrapper.find(`[data-test="${hook}"]`)
      expect(field.exists(), hook).toBe(true)
      expect(field.attributes('autocapitalize'), hook).toBe('none')
      expect(field.attributes('autocorrect'), hook).toBe('off')
      expect(field.attributes('spellcheck'), hook).toBe('false')
      expect(field.attributes('autocomplete'), hook).toBe('off')
    }
    const authUrl = wrapper.find(`[data-test="${conditional}"]`)
    expect(authUrl.exists(), conditional).toBe(true)
    expect(authUrl.attributes('autocapitalize'), conditional).toBe('none')
    expect(authUrl.attributes('spellcheck'), conditional).toBe('false')
    tray.restore()
    wrapper.unmount()
  })
})

describe('routing tab allow_remote', () => {
  // config.Validate rejects a non-loopback listener without allow_remote, for the raw
  // forwarders as well as for the proxies, so the switch has to be reachable on every
  // tunnel rather than only inside the proxy-only block.
  it('offers the switch on every tunnel and sends it when the form is saved', async () => {
    const tray = installFakeTray({
      'PUT /api/routing': () => fixtures.saveResult({ routing: fixtures.routing() }),
    })
    const { wrapper } = mountView(RoutingView, { locale: 'zh-CN', prepare: () => seed() })

    const switches = wrapper.findAll('[data-test^="allow-remote-"]')
    expect(switches).toHaveLength(2)
    expect(wrapper.text()).toContain('允许远程连接')

    useRoutingStore().tunnels[0].allowRemote = true
    await click(wrapper, '[data-test="save-routing"]')
    const writes = tray.calls.filter((call) => call.path === '/api/routing')
    expect(writes.length).toBeGreaterThan(0)
    const body = writes[writes.length - 1].body as { tunnels: Array<{ allowRemote: boolean }> }
    expect(body.tunnels[0].allowRemote).toBe(true)
    // The other row keeps the value the file had, so editing one tunnel cannot silently
    // widen the exposure of another.
    expect(body.tunnels[1].allowRemote).toBe(false)
    tray.restore()
    wrapper.unmount()
  })
})

describe('routing tab proxy authentication', () => {
  // config.Validate accepts exactly none|password for socks5 and none|basic for http-proxy.
  // "remote" is not an auth mode anywhere in the product: remote validation is a separate
  // key (auth_url) that composes with any of them, and offering it produced a configuration
  // the tray's own 检测 rejected and `client run` refused to start.
  function proxyTunnels(): TunnelView[] {
    return [
      { name: 'socks', protocol: 'socks5', listen: '127.0.0.1:1080', agentId: 'agent-a', targetHost: '', targetPort: 0, authMode: 'none', allowRemote: false, authUrl: '' },
      { name: 'proxy', protocol: 'http-proxy', listen: '127.0.0.1:18081', agentId: 'agent-b', targetHost: '', targetPort: 0, authMode: 'none', allowRemote: false, authUrl: '' },
    ]
  }

  it('offers only the auth modes the configuration accepts', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(RoutingView, { prepare: () => seed({ tunnels: proxyTunnels() }) })

    const values = wrapper
      .findAllComponents({ name: 'ElOption' })
      .map((option) => option.props('value') as unknown)
    expect(values).not.toContain('remote')
    expect(values).toEqual(expect.arrayContaining(['none', 'password', 'basic']))
    tray.restore()
    wrapper.unmount()
  })

  it('keeps the remote validation URL independent of the auth mode', async () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(RoutingView, { prepare: () => seed({ tunnels: proxyTunnels() }) })

    // Both tunnels are on auth_mode "none", and remote validation still applies to them.
    expect(wrapper.find('[data-test="auth-url-0"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="auth-url-1"]').exists()).toBe(true)
    // A raw forwarder has no validator to call, so the field must not appear there.
    useRoutingStore().tunnels[0].protocol = 'tcp'
    await flush(4)
    expect(wrapper.find('[data-test="auth-url-0"]').exists()).toBe(false)
    tray.restore()
    wrapper.unmount()
  })
})

describe('routing tab agent picker', () => {
  const agentCalls = (tray: { calls: Array<{ path: string }> }) =>
    tray.calls.filter((call) => call.path === '/api/agents').length

  // Only the settings store is seeded before mount: adopting it re-applies the stored
  // language, which would overwrite the locale these assertions are written in.
  const seedSettings = () => () => useSettingsStore().adopt(fixtures.settings())

  it('loads the list once the stored configuration arrives after mount', async () => {
    // el-tab-pane renders its content eagerly, so RoutingView mounts before App.vue's
    // initial load resolves: in the real window the stored configuration always arrives
    // after onMounted. A fetch scheduled only in onMounted therefore never runs, the
    // picker stays empty, and the panel blames the token's scope for a request nobody
    // made.
    const tray = installFakeTray({ 'GET /api/agents': fixtures.agents() })
    const { wrapper } = mountView(RoutingView, { locale: 'zh-CN', prepare: seedSettings() })
    await flush(6)
    // Nothing is configured yet, so mounting must not fire a request that can only fail.
    expect(agentCalls(tray)).toBe(0)

    useRoutingStore().adopt(fixtures.routing())
    await flush(6)
    expect(agentCalls(tray)).toBe(1)
    expect(useRoutingStore().agents).toHaveLength(2)
    expect(wrapper.find('[data-test="agents-notice"]').exists()).toBe(false)

    // One request per window: the trigger has to latch, because serverUrl is the live form
    // field and re-reading the picker on every keystroke would be a request storm.
    useRoutingStore().serverUrl = 'wss://other.example.com/ws/client'
    await flush(6)
    expect(agentCalls(tray)).toBe(1)

    tray.restore()
    wrapper.unmount()
  })

  it('separates a list nobody fetched from a genuinely empty scope', async () => {
    const tray = installFakeTray({ 'GET /api/agents': [] })
    const { wrapper } = mountView(RoutingView, { locale: 'zh-CN', prepare: seedSettings() })
    await flush(6)
    const state = wrapper.find('[data-test="agent-state"]')
    expect(state.text()).toContain('尚未拉取')
    expect(state.text()).not.toContain('没有可用 Agent')

    useRoutingStore().adopt(fixtures.routing())
    await flush(6)
    // Only a completed, successful fetch may claim the scope itself is empty.
    expect(wrapper.find('[data-test="agent-state"]').text()).toContain('该 token 作用域内没有可用 Agent')

    tray.restore()
    wrapper.unmount()
  })

  it('shows the fetch failure instead of an empty-scope claim', async () => {
    const tray = installFakeTray({}, { 'GET /api/agents': { status: 401, msg: 'unauthenticated' } })
    const { wrapper } = mountView(RoutingView, { locale: 'zh-CN', prepare: seedSettings() })
    useRoutingStore().adopt(fixtures.routing())
    await flush(8)
    const state = wrapper.find('[data-test="agent-state"]')
    expect(state.text()).toContain('失败')
    expect(state.text()).not.toContain('该 token 作用域内没有可用 Agent')
    expect(wrapper.find('[data-test="agents-notice"]').exists()).toBe(false)

    tray.restore()
    wrapper.unmount()
  })
})
