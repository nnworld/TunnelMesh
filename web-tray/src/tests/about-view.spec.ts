import { describe, expect, it } from 'vitest'
import AboutView from '../views/AboutView.vue'
import { useAboutStore } from '../stores/about'
import { useRoutingStore } from '../stores/routing'
import { useStatsStore } from '../stores/stats'
import { fixtures, flush, installFakeTray, mountView } from './helpers'

function seed() {
  useAboutStore().view = fixtures.about()
  useRoutingStore().adopt(fixtures.routing())
  useStatsStore().view = fixtures.stats()
}

describe('about tab', () => {
  it('shows the build identity, host, configuration paths and links', () => {
    const tray = installFakeTray({ 'GET /api/about': fixtures.about() })
    const { wrapper } = mountView(AboutView, { prepare: seed })
    const text = wrapper.text()

    expect(text).toContain('1.4.0')
    expect(text).toContain('abc1234')
    expect(text).toContain('darwin')
    expect(text).toContain('arm64')
    expect(text).toContain('14.6.1')
    expect(text).toContain('/Users/tester/.config/tunnelmesh/client.yaml')
    expect(text).toContain('/Users/tester/.config/tunnelmesh/tray.json')
    expect(text).toContain('/Users/tester/.config/tunnelmesh/client.lock')
    expect(text).toContain('client-instance-1')
    expect(text).toContain('Apache-2.0')
    tray.restore()
    wrapper.unmount()
  })

  it('never renders the token, only whether one is stored', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(AboutView, { prepare: seed })
    expect(wrapper.text()).toContain('Stored')
    expect(wrapper.text()).not.toContain('stored-token')
    tray.restore()
    wrapper.unmount()
  })

  it('opens the release page from the check-for-updates button', async () => {
    const tray = installFakeTray({ 'POST /api/actions/open-releases': { opened: true } })
    const { wrapper } = mountView(AboutView, { prepare: seed })
    await wrapper.find('[data-test="check-updates"]').trigger('click')
    await flush(6)
    expect(tray.calls.some((call) => call.path === '/api/actions/open-releases')).toBe(true)
    tray.restore()
    wrapper.unmount()
  })

  it('opens the website and the documentation', async () => {
    const tray = installFakeTray({
      'POST /api/actions/open-website': { opened: true },
      'POST /api/actions/open-docs': { opened: true },
    })
    const { wrapper } = mountView(AboutView, { prepare: seed })
    await wrapper.find('[data-test="open-website"]').trigger('click')
    await wrapper.find('[data-test="open-docs"]').trigger('click')
    await flush(8)
    const paths = tray.calls.map((call) => call.path)
    expect(paths).toContain('/api/actions/open-website')
    expect(paths).toContain('/api/actions/open-docs')
    tray.restore()
    wrapper.unmount()
  })

  it('reports a link the tray could not open', async () => {
    const tray = installFakeTray(
      {},
      { 'POST /api/actions/open-releases': { status: 400, msg: 'tray: opening a browser needs the native tray shell' } },
    )
    const { wrapper } = mountView(AboutView, { prepare: seed })
    await wrapper.find('[data-test="check-updates"]').trigger('click')
    await flush(8)
    expect(wrapper.find('[data-test="about-error"]').text()).toContain('native tray shell')
    tray.restore()
    wrapper.unmount()
  })

  it('loads the view on mount when it was not fetched yet', async () => {
    const tray = installFakeTray({ 'GET /api/about': fixtures.about({ version: '9.9.9' }) })
    const { wrapper } = mountView(AboutView, { prepare: () => {
      useRoutingStore().adopt(fixtures.routing())
      useStatsStore().view = fixtures.stats()
    } })
    await flush(8)
    expect(tray.calls.some((call) => call.path === '/api/about')).toBe(true)
    expect(wrapper.text()).toContain('9.9.9')
    tray.restore()
    wrapper.unmount()
  })
})
