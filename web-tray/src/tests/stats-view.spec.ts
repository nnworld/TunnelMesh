import { afterEach, describe, expect, it, vi } from 'vitest'
import StatsView from '../views/StatsView.vue'
import { useStatsStore } from '../stores/stats'
import { STATS_POLL_MS } from '../stores/stats'
import { fixtures, flush, installFakeTray, mountView } from './helpers'

function seed(view = fixtures.stats()) {
  const store = useStatsStore()
  store.view = view
  return store
}

describe('statistics tab', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('renders the run summary, the tunnel table and the connection pool', () => {
    const tray = installFakeTray({ 'GET /api/stats': fixtures.stats() })
    const { wrapper } = mountView(StatsView, { prepare: () => seed() })
    const text = wrapper.text()

    expect(text).toContain('Running')
    expect(text).toContain('1h 2m 5s')
    expect(text).toContain('Reachable')
    expect(text).toContain('Connected')
    expect(text).toContain('2.00 KB')

    const rows = wrapper.findAll('[data-test="tunnel-table"] tbody tr')
    expect(rows).toHaveLength(2)
    expect(text).toContain('127.0.0.1:8080')
    expect(text).toContain('10.0.0.8:80')
    expect(text).toContain('Listening')
    expect(text).toContain('dial refused')

    const pool = wrapper.findAll('[data-test="pool-table"] tbody tr')
    expect(pool).toHaveLength(1)
    expect(text).toContain('agent-a')
    tray.restore()
    wrapper.unmount()
  })

  it('explains a contested lock rather than only showing "stopped"', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(StatsView, {
      prepare: () => seed(fixtures.stats({ running: false, lockBlocked: true, uptimeSeconds: 0 })),
    })
    expect(wrapper.find('[data-test="lock-blocked"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="runtime-error"]').exists()).toBe(false)
    tray.restore()
    wrapper.unmount()
  })

  it('surfaces the reason the client is not running', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(StatsView, {
      prepare: () =>
        seed(
          fixtures.stats({
            running: false,
            uptimeSeconds: 0,
            runtimeError: 'client run requires client.server_url and client.token',
          }),
        ),
    })
    expect(wrapper.find('[data-test="runtime-error"]').text()).toContain('client.server_url')
    tray.restore()
    wrapper.unmount()
  })

  it('describes configured tunnels while the client is stopped', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(StatsView, {
      prepare: () =>
        seed(
          fixtures.stats({
            running: false,
            connected: false,
            uptimeSeconds: 0,
            agents: [],
            tunnels: [
              { name: 'web', protocol: 'tcp', listen: '127.0.0.1:8080', agentId: 'agent-a', target: '10.0.0.8:80', state: 'stopped' },
            ],
            totals: {
              tunnels: 1, listening: 0, failed: 0, agents: 0, openSlots: 0,
              readySessions: 0, activeStreams: 0, reconnects: 0, bytesInbound: 0,
            },
          }),
        ),
    })
    const text = wrapper.text()
    expect(text).toContain('Stopped')
    expect(text).toContain('web')
    expect(text).toContain('No per-agent pool is open.')
    expect(text).toContain('The client is stopped')
    tray.restore()
    wrapper.unmount()
  })

  it('refreshes on demand and polls while auto refresh is on', async () => {
    vi.useFakeTimers()
    let served = 0
    const tray = installFakeTray({
      'GET /api/stats': () => {
        served += 1
        return fixtures.stats({ uptimeSeconds: served })
      },
    })
    const { wrapper } = mountView(StatsView, { prepare: () => seed() })
    const store = useStatsStore()

    store.beginPolling()
    await vi.advanceTimersByTimeAsync(1)
    expect(served).toBe(1)

    await vi.advanceTimersByTimeAsync(STATS_POLL_MS)
    expect(served).toBe(2)

    await wrapper.find('[data-test="refresh-stats"]').trigger('click')
    await vi.advanceTimersByTimeAsync(1)
    expect(served).toBe(3)

    store.setAutoRefresh(false)
    await vi.advanceTimersByTimeAsync(STATS_POLL_MS * 3)
    expect(served).toBe(3)
    store.endPolling()
    tray.restore()
    wrapper.unmount()
  })

  it('reports a server it cannot reach without hiding the rest of the snapshot', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(StatsView, {
      prepare: () =>
        seed(fixtures.stats({ serverReachable: false, serverProbe: 'dial tcp 10.0.0.1:443: connect: refused' })),
    })
    expect(wrapper.text()).toContain('Unreachable')
    expect(wrapper.text()).toContain('Listening')
    tray.restore()
    wrapper.unmount()
  })
})
