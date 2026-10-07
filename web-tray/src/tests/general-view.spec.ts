import { describe, expect, it } from 'vitest'
import type { VueWrapper } from '@vue/test-utils'
import GeneralView from '../views/GeneralView.vue'
import { useRoutingStore } from '../stores/routing'
import { useSettingsStore } from '../stores/settings'
import { fixtures, flush, installFakeTray, mountView } from './helpers'

function seed() {
  useSettingsStore().adopt(fixtures.settings())
  useRoutingStore().adopt(fixtures.routing())
}

/** Clicks the nth radio button in a group, which is how an operator changes it. */
async function pickRadio(wrapper: VueWrapper, group: string, index: number) {
  const buttons = wrapper.findAll(`${group} .el-radio-button`)
  expect(buttons.length, group).toBeGreaterThan(index)
  await buttons[index].trigger('click')
  await wrapper.vm.$nextTick()
}

/** Drives the mode select through its v-model event rather than reaching into the ref. */
async function pickMode(wrapper: VueWrapper, mode: string) {
  const select = wrapper.findComponent('[data-test="mode-select"]')
  select.vm.$emit('update:modelValue', mode)
  await wrapper.vm.$nextTick()
}

describe('general tab', () => {
  it('renders every required setting and the resolved file paths', () => {
    const tray = installFakeTray({ 'GET /api/settings': fixtures.settings() })
    const { wrapper } = mountView(GeneralView, { locale: 'zh-CN', prepare: seed })

    const text = wrapper.text()
    // Requirement: language, appearance, configuration directory, client mode,
    // launch at login and minimize-to-tray all live on this tab.
    for (const expected of [
      '界面语言',
      '外观主题',
      '配置目录',
      'client 模式',
      '开机启动',
      '关闭时最小化到系统托盘',
      '任务栏快捷小窗',
      fixtures.settings().clientConfigPath,
    ]) {
      expect(text, expected).toContain(expected)
    }
    for (const target of ['language-group', 'theme-group', 'mode-select', 'launch-at-login', 'minimize-to-tray', 'quick-panel-group']) {
      expect(wrapper.find(`[data-test="${target}"]`).exists(), target).toBe(true)
    }
    tray.restore()
    wrapper.unmount()
  })

  it('offers follow-system, Chinese and English for both language and theme', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(GeneralView, { locale: 'zh-CN', prepare: seed })
    expect(wrapper.findAll('[data-test="language-group"] .el-radio-button')).toHaveLength(3)
    expect(wrapper.findAll('[data-test="theme-group"] .el-radio-button')).toHaveLength(3)
    const labels = wrapper.findAll('[data-test="theme-group"] .el-radio-button').map((node) => node.text())
    expect(labels).toEqual(['跟随系统', '浅色', '深色'])
    tray.restore()
    wrapper.unmount()
  })

  it('switches the interface language immediately on selection', async () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(GeneralView, { locale: 'en-US', prepare: seed })
    expect(wrapper.text()).toContain('Interface language')
    await pickRadio(wrapper, '[data-test="language-group"]', 1)
    await flush(6)
    expect(wrapper.text()).toContain('界面语言')
    expect(document.documentElement.lang).toBe('zh-CN')
    tray.restore()
    wrapper.unmount()
  })

  it('applies the dark theme immediately and stops following the system afterwards', async () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(GeneralView, { prepare: seed })
    await flush()
    expect(document.documentElement.classList.contains('dark')).toBe(false)
    await pickRadio(wrapper, '[data-test="theme-group"]', 2)
    await flush(6)
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(document.documentElement.dataset.theme).toBe('dark')
    tray.restore()
    wrapper.unmount()
  })

  it('saves the preferences and reports success', async () => {
    const tray = installFakeTray({
      'PUT /api/settings': (body: unknown) =>
        fixtures.settings({ ...(body as Record<string, unknown>) } as never),
    })
    const { wrapper } = mountView(GeneralView, { prepare: seed })
    await flush()

    const settings = useSettingsStore()
    settings.draft.language = 'en-US'
    settings.draft.theme = 'light'
    settings.draft.minimizeToTray = false
    // The form mirrors the store through a watcher, which flushes before the next paint.
    await flush(2)
    await wrapper.find('[data-test="save-general"]').trigger('click')
    await flush(10)

    expect(tray.calls).toHaveLength(1)
    expect(tray.calls[0].body).toMatchObject({
      language: 'en-US',
      theme: 'light',
      minimizeToTray: false,
      launchAtLogin: true,
    })
    expect(wrapper.find('[data-test="general-notice"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="general-error"]').exists()).toBe(false)
    tray.restore()
    wrapper.unmount()
  })

  it('reports a refused login item instead of pretending it worked', async () => {
    const tray = installFakeTray(
      {},
      { 'PUT /api/settings': { status: 400, msg: 'tray: cannot change launch at login: SMAppService refused' } },
    )
    const { wrapper } = mountView(GeneralView, { prepare: seed })
    await flush()

    useSettingsStore().draft.launchAtLogin = false
    await flush(2)
    await wrapper.find('[data-test="save-general"]').trigger('click')
    await flush(10)

    const alert = wrapper.find('[data-test="general-error"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('SMAppService refused')
    tray.restore()
    wrapper.unmount()
  })

  it('commits the client mode through the routing save because it lives in client.yaml', async () => {
    const tray = installFakeTray({
      'PUT /api/settings': fixtures.settings(),
      'PUT /api/routing': (body: unknown) =>
        fixtures.saveResult({ routing: fixtures.routing({ mode: (body as { mode: string }).mode as never }) }),
    })
    const { wrapper } = mountView(GeneralView, { prepare: seed })
    await flush()

    await pickMode(wrapper, 'cluster')
    await wrapper.find('[data-test="save-general"]').trigger('click')
    await flush(12)

    const writes = tray.calls.filter((call) => call.path === '/api/routing')
    expect(writes).toHaveLength(1)
    expect(writes[0].body).toMatchObject({ mode: 'cluster', serverUrl: 'wss://mesh.example.com/ws/client' })
    // The token was never sent back by the tray, so the save must keep it rather than
    // silently clearing it.
    expect((writes[0].body as { token: unknown }).token).toBeNull()
    expect(useRoutingStore().mode).toBe('cluster')
    tray.restore()
    wrapper.unmount()
  })

  it('restores the previous mode when client.yaml rejects the change', async () => {
    const tray = installFakeTray(
      { 'PUT /api/settings': fixtures.settings() },
      { 'PUT /api/routing': { status: 422, msg: 'cluster mode requires mysql DSN' } },
    )
    const { wrapper } = mountView(GeneralView, { prepare: seed })
    await flush()

    await pickMode(wrapper, 'cluster')
    await wrapper.find('[data-test="save-general"]').trigger('click')
    await flush(12)

    expect(useRoutingStore().mode).toBe('local')
    expect(wrapper.find('[data-test="general-error"]').text()).toContain('cluster mode requires mysql DSN')
    tray.restore()
    wrapper.unmount()
  })

  it('disables the login-item switch where the platform cannot register one', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(GeneralView, { prepare: () => {
      useSettingsStore().adopt(fixtures.settings({ launchAtLoginSupported: false }))
      useRoutingStore().adopt(fixtures.routing())
    } })
    expect(wrapper.text()).toContain('Launch at login is not available in this build.')
    tray.restore()
    wrapper.unmount()
  })

  it('shows the stored login-item failure from the tray', () => {
    const tray = installFakeTray()
    const { wrapper } = mountView(GeneralView, { prepare: () => {
      useSettingsStore().adopt(fixtures.settings({ launchAtLoginError: 'code signature invalid' }))
      useRoutingStore().adopt(fixtures.routing())
    } })
    expect(wrapper.find('[data-test="launch-at-login-error"]').text()).toContain('code signature invalid')
    tray.restore()
    wrapper.unmount()
  })
})

describe('general tab text input hygiene', () => {
  it('turns the system text assists off on the configuration directory', () => {
    const tray = installFakeTray({ 'GET /api/settings': fixtures.settings() })
    const { wrapper } = mountView(GeneralView, { locale: 'zh-CN', prepare: seed })

    const dir = wrapper.find('[data-test="config-dir"]')
    expect(dir.exists()).toBe(true)
    expect(dir.attributes('autocapitalize')).toBe('none')
    expect(dir.attributes('autocorrect')).toBe('off')
    expect(dir.attributes('spellcheck')).toBe('false')
    expect(dir.attributes('autocomplete')).toBe('off')
    tray.restore()
    wrapper.unmount()
  })
})

describe('quick panel setting', () => {
  it('renders an off-by-default radio pair for the menu-bar quick panel', () => {
    installFakeTray({ 'GET /api/settings': fixtures.settings() })
    const { wrapper } = mountView(GeneralView, { locale: 'zh-CN', prepare: seed })

    const group = wrapper.find('[data-test="quick-panel-group"]')
    expect(group.exists(), '任务栏快捷小窗 control').toBe(true)
    const options = group.findAll('.el-radio-button')
    expect(options).toHaveLength(2)
    expect(options.map((node) => node.text())).toEqual(['关闭', '开启'])
    // The requirement is "default off": the checked button has to be 关闭, and it is the
    // store that decides that, not the order of the options.
    expect(group.find('.el-radio-button.is-active').text()).toBe('关闭')
    expect(useSettingsStore().draft.quickPanel).toBe(false)
    wrapper.unmount()
  })

  it('saves the quick panel choice with the rest of the preferences', async () => {
    const tray = installFakeTray({
      'PUT /api/settings': (body: unknown) =>
        fixtures.settings({ ...(body as Record<string, unknown>) } as never),
    })
    const { wrapper } = mountView(GeneralView, { locale: 'zh-CN', prepare: seed })
    await flush()

    await pickRadio(wrapper, '[data-test="quick-panel-group"]', 1)
    await wrapper.find('[data-test="save-general"]').trigger('click')
    await flush(10)

    const write = tray.calls.find((call) => call.path === '/api/settings')
    expect(write?.body).toMatchObject({ quickPanel: true })
    // The shell is told through the same save, so the next menu-bar click already follows
    // the new value without a relaunch.
    expect(useSettingsStore().view?.quickPanel).toBe(true)
    tray.restore()
    wrapper.unmount()
  })

  it('keeps the panel off when the operator picks 关闭 again', async () => {
    installFakeTray({
      'GET /api/settings': fixtures.settings({ quickPanel: true }),
      'PUT /api/settings': (body: unknown) =>
        fixtures.settings({ ...(body as Record<string, unknown>) } as never),
    })
    const { wrapper } = mountView(GeneralView, { locale: 'zh-CN', prepare: () => {
      useSettingsStore().adopt(fixtures.settings({ quickPanel: true }))
      useRoutingStore().adopt(fixtures.routing())
    } })
    await flush()
    expect(wrapper.find('[data-test="quick-panel-group"] .el-radio-button.is-active').text()).toBe('开启')

    await pickRadio(wrapper, '[data-test="quick-panel-group"]', 0)
    await wrapper.find('[data-test="save-general"]').trigger('click')
    await flush(10)
    expect(useSettingsStore().draft.quickPanel).toBe(false)
    wrapper.unmount()
  })
})

describe('unsaved changes', () => {
  /**
   * The form writes nothing until 保存 is pressed, which is exactly the gap that made the
   * quick panel look like a setting that does nothing: the radio moved, the menu bar did
   * not, and the button that would have made it real was below the fold.
   */
  it('says a choice is pending and stops saying it after the save', async () => {
    const tray = installFakeTray({
      'GET /api/settings': fixtures.settings(),
      'PUT /api/settings': (body: unknown) =>
        fixtures.settings({ ...(body as Record<string, unknown>) } as never),
    })
    const { wrapper } = mountView(GeneralView, { locale: 'zh-CN', prepare: seed })
    await flush()

    expect(wrapper.find('[data-test="general-unsaved"]').exists(), 'untouched form').toBe(false)

    await pickRadio(wrapper, '[data-test="quick-panel-group"]', 1)
    await flush()
    const pending = wrapper.find('[data-test="general-unsaved"]')
    expect(pending.exists(), 'form with a pending choice').toBe(true)
    expect(pending.text()).toContain('未保存')

    await wrapper.find('[data-test="save-general"]').trigger('click')
    await flush(10)
    expect(wrapper.find('[data-test="general-unsaved"]').exists(), 'after the save').toBe(false)
    expect(useSettingsStore().draft.quickPanel).toBe(true)

    tray.restore()
    wrapper.unmount()
  })
})
