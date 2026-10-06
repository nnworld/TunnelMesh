import { describe, expect, it, vi } from 'vitest'
import App from '../App.vue'
import { useStatsStore } from '../stores/stats'
import { fixtures, flush, installFakeTray, mountView } from './helpers'

/** The full tray surface, as the window sees it on launch. */
function trayRoutes(overrides: Record<string, unknown> = {}) {
  return {
    'GET /api/settings': fixtures.settings(),
    'GET /api/routing': fixtures.routing(),
    'GET /api/about': fixtures.about(),
    'GET /api/stats': fixtures.stats(),
    'GET /api/agents': fixtures.agents(),
    ...overrides,
  }
}

describe('settings window', () => {
  it('renders the four required tabs in order', async () => {
    // The stored preference drives the language, so the fixture is what selects Chinese;
    // a 'system' preference would follow the (English) jsdom navigator.
    const tray = installFakeTray(trayRoutes({ 'GET /api/settings': fixtures.settings({ language: 'zh-CN' }) }))
    const { wrapper } = mountView(App, { locale: 'zh-CN' })
    await flush(10)

    const labels = wrapper.findAll('.el-tabs__item').map((tab) => tab.text())
    expect(labels).toEqual(['通用', '统计', '路由配置', '关于'])

    // The general tab is the landing page.
    expect(wrapper.find('[data-test="general-view"]').exists()).toBe(true)
    tray.restore()
    wrapper.unmount()
  })

  it('switches between the tabs', async () => {
    const tray = installFakeTray(trayRoutes())
    const { wrapper } = mountView(App)
    await flush(10)

    const tabs = wrapper.findAll('.el-tabs__item')
    for (const [index, marker] of [
      [1, 'stats-view'],
      [2, 'routing-view'],
      [3, 'about-view'],
      [0, 'general-view'],
    ] as Array<[number, string]>) {
      await tabs[index].trigger('click')
      await flush(6)
      expect(wrapper.find(`[data-test="${marker}"]`).exists(), marker).toBe(true)
    }
    tray.restore()
    wrapper.unmount()
  })

  it('shows the client state and drives start, stop and restart', async () => {
    const tray = installFakeTray(
      trayRoutes({
        'POST /api/actions/start': fixtures.stats({ running: true }),
        'POST /api/actions/stop': fixtures.stats({ running: false, uptimeSeconds: 0 }),
        'POST /api/actions/restart': fixtures.stats({ running: true }),
      }),
    )
    const { wrapper } = mountView(App)
    await flush(10)

    expect(wrapper.find('[data-test="status-tag"]').text()).toContain('Running')
    const start = wrapper.find('[data-test="start-client"]')
    expect((start.element as HTMLButtonElement).disabled).toBe(true)

    await wrapper.find('[data-test="stop-client"]').trigger('click')
    await flush(8)
    expect(wrapper.find('[data-test="status-tag"]').text()).toContain('Stopped')
    expect((wrapper.find('[data-test="start-client"]').element as HTMLButtonElement).disabled).toBe(false)

    await wrapper.find('[data-test="start-client"]').trigger('click')
    await flush(8)
    expect(tray.calls.some((call) => call.path === '/api/actions/start')).toBe(true)
    tray.restore()
    wrapper.unmount()
  })

  it('reports a start the tray refused because another client holds the lock', async () => {
    const tray = installFakeTray(
      trayRoutes({ 'GET /api/stats': fixtures.stats({ running: false, uptimeSeconds: 0 }) }),
      {
        'POST /api/actions/start': {
          status: 409,
          msg: 'client: another TunnelMesh client is already running with this configuration',
        },
      },
    )
    const { wrapper } = mountView(App)
    await flush(10)
    await wrapper.find('[data-test="start-client"]').trigger('click')
    await flush(10)
    expect(wrapper.find('[data-test="bootstrap-error"]').text()).toContain('already running')
    tray.restore()
    wrapper.unmount()
  })

  it('captures the launch secret and sends it with the first request', async () => {
    window.history.replaceState(null, '', '/?secret=launch-secret-123')
    const tray = installFakeTray(trayRoutes())
    const { wrapper } = mountView(App, { secret: '' })
    await flush(10)
    expect(window.location.search).not.toContain('launch-secret-123')
    const settings = tray.calls.find((call) => call.path === '/api/settings')
    expect(settings?.headers['x-tray-secret']).toBe('launch-secret-123')
    tray.restore()
    wrapper.unmount()
  })

  it('keeps polling statistics for the header badge while another tab is open', async () => {
    vi.useFakeTimers()
    let served = 0
    const tray = installFakeTray(
      trayRoutes({
        'GET /api/stats': () => {
          served += 1
          return fixtures.stats({ uptimeSeconds: served })
        },
      }),
    )
    const { wrapper } = mountView(App)
    await vi.advanceTimersByTimeAsync(20)
    const before = served
    expect(before).toBeGreaterThan(0)

    // Switch to the about tab: the badge is on every tab, so the polling must survive.
    await wrapper.findAll('.el-tabs__item')[3].trigger('click')
    await vi.advanceTimersByTimeAsync(2000 * 2)
    expect(served).toBeGreaterThan(before)

    useStatsStore().endPolling()
    vi.useRealTimers()
    tray.restore()
    wrapper.unmount()
  })

  it('survives a tray that answers nothing', async () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(App)
    await flush(10)
    expect(wrapper.find('[data-test="bootstrap-error"]').exists()).toBe(true)
    expect(wrapper.findAll('.el-tabs__item')).toHaveLength(4)
    tray.restore()
    wrapper.unmount()
  })
})
