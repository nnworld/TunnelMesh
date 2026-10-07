import { afterEach, describe, expect, it, vi } from 'vitest'
import QuickPanelApp from '../QuickPanelApp.vue'
import { isPanelRoute, PANEL_ROUTE } from '../panelRoute'
import { useSettingsStore } from '../stores/settings'
import { useStatsStore } from '../stores/stats'
import { STATS_POLL_MS } from '../stores/stats'
import { fixtures, flush, installFakeTray, mountView } from './helpers'

function mountPanel(routes: Parameters<typeof installFakeTray>[0] = {}, locale: 'zh-CN' | 'en-US' = 'zh-CN') {
  const tray = installFakeTray(
    {
      // The panel applies the stored language on mount, exactly as the settings window does,
      // so a Chinese-localised assertion has to say which language the tray reports.
      'GET /api/settings': fixtures.settings({ language: 'zh-CN', quickPanel: true }),
      'GET /api/stats': fixtures.stats(),
      ...routes,
    },
  )
  const mounted = mountView(QuickPanelApp, { locale })
  return { tray, ...mounted }
}

describe('panel route', () => {
  it('recognises only the panel fragment', () => {
    expect(PANEL_ROUTE).toBe('#/panel')
    expect(isPanelRoute('#/panel')).toBe(true)
    for (const hash of ['', '#/', '#/routing', '#panel', '#/panel/deep']) {
      expect(isPanelRoute(hash), hash).toBe(false)
    }
  })
})

describe('quick panel', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('renders the run summary and one row per tunnel', async () => {
    const { tray, wrapper } = mountPanel()
    await flush(6)

    expect(wrapper.find('[data-test="panel-status"]').text()).toContain('运行中')
    const text = wrapper.text()
    // The panel is a glance, so it carries the numbers that answer "is it working":
    // the server line (a live session outranks a health probe, which a proxy in front of
    // the Server can refuse), uptime, how many tunnels are listening, and the traffic total.
    for (const expected of ['已连接', '1h 2m 5s', '2.00 KB', 'web', 'proxy', '监听中', '失败']) {
      expect(text, expected).toContain(expected)
    }
    expect(wrapper.findAll('[data-test="panel-tunnel"]')).toHaveLength(2)
    // The last error is the one thing an operator needs before opening the full window.
    expect(wrapper.text()).toContain('dial refused')
    tray.restore()
    wrapper.unmount()
  })

  it('follows the stored language and theme', async () => {
    const { tray, wrapper } = mountPanel({
      'GET /api/settings': fixtures.settings({ language: 'zh-CN', theme: 'dark', quickPanel: true }),
    })
    await flush(6)
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(wrapper.find('[data-test="panel-open-main"]').text()).toBe('打开主界面')
    tray.restore()
    wrapper.unmount()
  })

  it('asks the tray to open the window, toggle the client and quit', async () => {
    const { tray, wrapper } = mountPanel({ 'POST /api/actions/stop': fixtures.stats({ running: false }) })
    await flush(6)

    await wrapper.find('[data-test="panel-open-main"]').trigger('click')
    await flush(4)
    expect(tray.calls.some((call) => call.method === 'POST' && call.path === '/api/actions/show-window')).toBe(true)

    // Running, so the single toggle button stops the hosted client.
    await wrapper.find('[data-test="panel-toggle"]').trigger('click')
    await flush(6)
    expect(tray.calls.some((call) => call.path === '/api/actions/stop')).toBe(true)

    await wrapper.find('[data-test="panel-quit"]').trigger('click')
    await flush(4)
    expect(tray.calls.some((call) => call.method === 'POST' && call.path === '/api/actions/quit')).toBe(true)
    tray.restore()
    wrapper.unmount()
  })

  it('offers 启动 when the client is stopped', async () => {
    const { tray, wrapper } = mountPanel({ 'GET /api/stats': fixtures.stats({ running: false }) })
    await flush(6)
    expect(wrapper.find('[data-test="panel-toggle"]').text()).toBe('启动客户端')
    expect(wrapper.find('[data-test="panel-status"]').text()).toContain('已停止')
    tray.restore()
    wrapper.unmount()
  })

  it('reports a blocked lock instead of an empty panel', async () => {
    const { tray, wrapper } = mountPanel({ 'GET /api/stats': fixtures.stats({ running: false, lockBlocked: true }) })
    await flush(6)
    expect(wrapper.find('[data-test="panel-status"]').text()).toContain('另一个')
    tray.restore()
    wrapper.unmount()
  })

  it('keeps polling while it is on screen', async () => {
    vi.useFakeTimers()
    let served = 0
    const tray = installFakeTray({
      'GET /api/settings': fixtures.settings(),
      'GET /api/stats': () => {
        served += 1
        return fixtures.stats({ uptimeSeconds: served })
      },
    })
    const { wrapper } = mountView(QuickPanelApp)
    await vi.advanceTimersByTimeAsync(2)
    expect(served).toBe(1)
    await vi.advanceTimersByTimeAsync(STATS_POLL_MS)
    expect(served).toBe(2)

    // Closing the panel unmounts the component, and a timer left behind would keep waking
    // a web view the operator already dismissed.
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(STATS_POLL_MS * 3)
    expect(served).toBe(2)
    tray.restore()
  })

  it('surfaces a tray that answers nothing', async () => {
    const tray = installFakeTray({}, { 'GET /api/stats': { status: 500, msg: 'tray exploded' } })
    const { wrapper } = mountView(QuickPanelApp)
    await flush(8)
    expect(wrapper.find('[data-test="panel-error"]').text()).toContain('tray exploded')
    tray.restore()
    wrapper.unmount()
  })
})

describe('quick panel server line', () => {
  /**
   * The deployment this tray runs against answers /health/ready with 403 from a reverse
   * proxy while its tunnels carry traffic. A panel that said 不可达 next to a live tunnel
   * is the contradiction that made the whole feature look broken, so the session state -
   * the strongest evidence there is - wins the one line the panel has.
   */
  it('names the live session instead of calling a blocking proxy an unreachable server', async () => {
    const { tray, wrapper } = mountPanel({
      'GET /api/stats': fixtures.stats({
        connected: true,
        serverReachable: false,
        serverProbe: 'server health check: unexpected status 403',
      }),
    })
    await flush(6)

    expect(wrapper.find('[data-test="panel-server"]').text()).toBe('已连接')
    tray.restore()
    wrapper.unmount()
  })

  it('keeps saying 不可达 when nothing is connected and nothing answered', async () => {
    const { tray, wrapper } = mountPanel({
      'GET /api/stats': fixtures.stats({ connected: false, serverReachable: false, serverProbe: '' }),
    })
    await flush(6)

    expect(wrapper.find('[data-test="panel-server"]').text()).toBe('不可达')
    tray.restore()
    wrapper.unmount()
  })
})
