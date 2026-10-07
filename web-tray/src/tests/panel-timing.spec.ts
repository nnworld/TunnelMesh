import { afterEach, describe, expect, it } from 'vitest'
import QuickPanelApp from '../QuickPanelApp.vue'
import { fixtures, flushUntil, installFakeTray, mountView } from './helpers'

/**
 * The panel waits for two sequential round trips (settings, then statistics), and the
 * number of promise turns one round trip costs is a property of the runtime's fetch
 * implementation, not of this code base. Counting microtasks therefore worked locally on
 * Node 24 and failed on the Node 22 runners of both the macOS and the Linux jobs, where
 * the panel rendered its pre-fetch state. `deepTray` makes that dependency visible: a
 * fixed number of awaited turns cannot survive it, so the waits have to look at the DOM.
 */
function deepTray(ticks: number, routes: Parameters<typeof installFakeTray>[0]) {
  const tray = installFakeTray(routes)
  const previous = globalThis.fetch
  globalThis.fetch = (async (...args: Parameters<typeof previous>) => {
    const response = await (previous as unknown as (...values: unknown[]) => Promise<Response>)(...args)
    let chain: Promise<void> = Promise.resolve()
    for (let index = 0; index < ticks; index += 1) {
      chain = chain.then(() => Promise.resolve())
    }
    await chain
    return response
  }) as typeof fetch
  return {
    restore: () => {
      globalThis.fetch = previous
      tray.restore()
    },
    calls: tray.calls,
  }
}

describe('quick panel waits survive a slower fetch chain', () => {
  let tray: { restore: () => void } | null = null

  afterEach(() => {
    tray?.restore()
    tray = null
  })

  it('still shows the run summary when a round trip costs hundreds of turns', async () => {
    tray = deepTray(500, {
      'GET /api/settings': fixtures.settings({ language: 'zh-CN', quickPanel: true }),
      'GET /api/stats': fixtures.stats(),
    })
    const { wrapper } = mountView(QuickPanelApp, { locale: 'zh-CN' })
    await flushUntil(() => wrapper.findAll('[data-test="panel-tunnel"]').length === 2)

    expect(wrapper.find('[data-test="panel-status"]').text()).toContain('运行中')
    expect(wrapper.find('[data-test="panel-server"]').text()).toBe('已连接')
    expect(wrapper.findAll('[data-test="panel-tunnel"]')).toHaveLength(2)
    wrapper.unmount()
  })
})
