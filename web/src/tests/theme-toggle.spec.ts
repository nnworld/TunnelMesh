import { beforeEach, describe, expect, it } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import messages from '../i18n/messages/en-US'
import ThemeToggle from '../components/ThemeToggle.vue'
import { usePreferencesStore } from '../stores/preferences'

function mount() {
  const pinia = createPinia(); setActivePinia(pinia)
  const host = document.createElement('div'); document.body.append(host)
  createApp({ render: () => h(ThemeToggle) }).use(pinia).use(createI18n({ legacy: false, locale: 'en-US', messages: { 'en-US': messages } })).mount(host)
  return { host, preferences: usePreferencesStore(pinia) }
}

describe('ThemeToggle', () => {
  beforeEach(() => { localStorage.clear(); document.body.replaceChildren() })

  it('is a radiogroup whose checked radio follows the stored preference', async () => {
    const { host, preferences } = mount()
    expect(host.querySelector('[role="radiogroup"]')).not.toBeNull()
    expect(host.querySelector('[data-test="theme-system"]')!.getAttribute('aria-checked')).toBe('true')
    host.querySelector<HTMLButtonElement>('[data-test="theme-dark"]')!.click()
    await nextTick()
    expect(preferences.theme).toBe('dark')
    expect(host.querySelector('[data-test="theme-dark"]')!.getAttribute('aria-checked')).toBe('true')
    expect(host.querySelector('[data-test="theme-system"]')!.getAttribute('aria-checked')).toBe('false')
  })

  it('moves through the options with the arrow keys and keeps one tab stop', async () => {
    const { host, preferences } = mount()
    host.querySelector('[data-test="theme-system"]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
    await nextTick()
    expect(preferences.theme).toBe('light')
    expect([...host.querySelectorAll('button')].filter(b => b.tabIndex === 0)).toHaveLength(1)
  })
})
