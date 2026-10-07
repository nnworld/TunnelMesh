import { describe, expect, it } from 'vitest'
import AboutView from '../views/AboutView.vue'
import GeneralView from '../views/GeneralView.vue'
import { platformKey, platformMessage } from '../platform'
import { useAboutStore } from '../stores/about'
import { useRoutingStore } from '../stores/routing'
import { useSettingsStore } from '../stores/settings'
import { fixtures, installFakeTray, mountView } from './helpers'

function seed(overrides: Parameters<typeof fixtures.settings>[0] = {}) {
  return () => {
    useSettingsStore().adopt(fixtures.settings(overrides))
    useRoutingStore().adopt(fixtures.routing())
  }
}

describe('platform wording', () => {
  it('maps the names the shells report and falls back for anything else', () => {
    // The tray sends its own name; the about payload carries a GOOS value instead, and both
    // arrive here because one set of words has to serve both.
    expect(platformKey('macos')).toBe('macos')
    expect(platformKey('darwin')).toBe('macos')
    expect(platformKey('windows')).toBe('windows')
    expect(platformKey('WIN32')).toBe('windows')
    expect(platformKey('linux')).toBe('other')
    expect(platformKey(undefined)).toBe('other')
    expect(platformMessage('general.theme.hint', 'linux')).toBe('general.theme.hint.other')
  })

  it('names macOS on the macOS shell and the notification area on the Windows shell', () => {
    const tray = installFakeTray()
    const mac = mountView(GeneralView, { prepare: seed({ platform: 'macos' }) })
    expect(mac.wrapper.text()).toContain('menu-bar item')
    expect(mac.wrapper.text()).toContain('macOS login item')

    const windows = mountView(GeneralView, { prepare: seed({ platform: 'windows' }) })
    expect(windows.wrapper.text()).toContain('notification-area icon')
    expect(windows.wrapper.text()).toContain('registry Run entry')
    expect(windows.wrapper.text()).not.toContain('menu-bar item')

    // An unrecognised shell must not claim to be either one.
    const unknown = mountView(GeneralView, { prepare: seed() })
    expect(unknown.wrapper.text()).not.toContain('menu-bar item')
    expect(unknown.wrapper.text()).not.toContain('notification-area')

    tray.restore()
    mac.wrapper.unmount()
    windows.wrapper.unmount()
    unknown.wrapper.unmount()
  })

  it('translates the platform wording rather than showing English on a Chinese tray', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(GeneralView, {
      locale: 'zh-CN',
      prepare: seed({ platform: 'windows' }),
    })
    expect(wrapper.text()).toContain('通知区域')
    expect(wrapper.text()).not.toContain('菜单栏')
    tray.restore()
    wrapper.unmount()
  })
})

describe('about tab renderer', () => {
  it('lists the web view when the shell reports one', () => {
    const payload = fixtures.about({ system: { goos: 'darwin', arch: 'arm64', osVersion: 'macOS 14.6', renderer: 'wkwebview' } })
    const tray = installFakeTray({ 'GET /api/about': payload })
    const { wrapper } = mountView(AboutView, { prepare: () => { useAboutStore().view = payload } })
    expect(wrapper.text()).toContain('Interface renderer')
    expect(wrapper.text()).toContain('wkwebview')
    expect(wrapper.find('[data-test="renderer-fallback"]').exists()).toBe(false)
    tray.restore()
    wrapper.unmount()
  })

  it('warns when Windows had to fall back to the system browser', () => {
    const payload = fixtures.about({
      system: { goos: 'windows', arch: 'amd64', osVersion: 'Windows 11 · Build 26100', renderer: 'browser', rendererDetail: 'runtime not installed' },
    })
    const tray = installFakeTray({ 'GET /api/about': payload })
    const { wrapper } = mountView(AboutView, { prepare: () => { useAboutStore().view = payload } })
    expect(wrapper.text()).toContain('Microsoft Edge WebView2 runtime not found')
    expect(wrapper.text()).toContain('runtime not installed')
    tray.restore()
    wrapper.unmount()
  })

  it('stays quiet for a tray that predates the renderer field', () => {
    const payload = fixtures.about({ system: { goos: 'darwin', arch: 'arm64' } })
    const tray = installFakeTray({ 'GET /api/about': payload })
    const { wrapper } = mountView(AboutView, { prepare: () => { useAboutStore().view = payload } })
    expect(wrapper.text()).not.toContain('Interface renderer')
    expect(wrapper.find('[data-test="renderer-fallback"]').exists()).toBe(false)
    tray.restore()
    wrapper.unmount()
  })
})
