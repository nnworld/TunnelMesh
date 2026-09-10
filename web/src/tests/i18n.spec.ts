import { describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { normalizeLocale, resolveInitialLocale } from '../i18n'
import zhCN from '../i18n/messages/zh-CN'
import enUS from '../i18n/messages/en-US'

describe('locale preferences', () => {
  it('normalizes every Chinese browser locale and falls back to English', () => {
    expect(normalizeLocale('zh-Hant')).toBe('zh-CN')
    expect(normalizeLocale('zh-CN')).toBe('zh-CN')
    expect(normalizeLocale('fr-FR')).toBe('en-US')
  })

  it('prefers a stored supported locale over browser languages', () => {
    expect(resolveInitialLocale('en-US', ['zh-CN'])).toBe('en-US')
    expect(resolveInitialLocale(null, ['fr-FR', 'zh-Hans'])).toBe('zh-CN')
  })

  it('does not persist an automatically detected locale', async () => {
    localStorage.removeItem('tunnelmesh_locale')
    vi.resetModules()

    await import('../i18n')

    expect(localStorage.getItem('tunnelmesh_locale')).toBeNull()
  })

  it('persists only a manually selected locale and keeps UI state synchronized', async () => {
    const { i18n, setAppLocale } = await import('../i18n')
    setAppLocale('zh-CN')
    setActivePinia(createPinia())
    const { usePreferencesStore } = await import('../stores/preferences')
    const preferences = usePreferencesStore()

    preferences.setLocale('en-US')

    expect(localStorage.getItem('tunnelmesh_locale')).toBe('en-US')
    expect(preferences.locale).toBe('en-US')
    expect(i18n.global.locale.value).toBe('en-US')
    expect(document.documentElement.lang).toBe('en-US')
  })

  it('keeps locale message keys structurally identical', () => {
    expect(Object.keys(zhCN).sort()).toEqual(Object.keys(enUS).sort())
  })
})
