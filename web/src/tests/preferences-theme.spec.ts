import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { usePreferencesStore } from '../stores/preferences'
import { THEME_KEY } from '../utils/theme'

const NAV_KEY = 'tunnelmesh_nav_collapsed'

// A matchMedia whose answer and change events the test controls.
function mockMedia(initialDark: boolean) {
  let dark = initialDark
  const listeners = new Set<(event: { matches: boolean }) => void>()
  vi.stubGlobal('matchMedia', (query: string) => ({
    get matches() { return query.includes('dark') ? dark : false },
    media: query,
    addEventListener: (_: string, cb: (event: { matches: boolean }) => void) => listeners.add(cb),
    removeEventListener: (_: string, cb: (event: { matches: boolean }) => void) => listeners.delete(cb),
  }))
  return {
    setDark(value: boolean) {
      dark = value
      for (const cb of listeners) cb({ matches: value })
    },
    listenerCount: () => listeners.size,
  }
}

describe('preferences: theme', () => {
  beforeEach(() => {
    localStorage.clear()
    document.documentElement.removeAttribute('data-theme')
    document.documentElement.classList.remove('dark')
    setActivePinia(createPinia())
  })

  it('defaults to following the system and resolves it', () => {
    mockMedia(true)
    const preferences = usePreferencesStore()
    preferences.initTheme()
    expect(preferences.theme).toBe('system')
    expect(preferences.resolvedTheme).toBe('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('persists an explicit choice and applies it at once', () => {
    mockMedia(false)
    const preferences = usePreferencesStore()
    preferences.initTheme()
    preferences.setTheme('dark')
    expect(localStorage.getItem(THEME_KEY)).toBe('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(preferences.resolvedTheme).toBe('dark')
  })

  it('restores the stored choice on the next start', () => {
    mockMedia(true)
    localStorage.setItem(THEME_KEY, 'light')
    const preferences = usePreferencesStore()
    preferences.initTheme()
    expect(preferences.theme).toBe('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('tracks the system while set to "system" and ignores it otherwise', () => {
    const media = mockMedia(false)
    const preferences = usePreferencesStore()
    preferences.initTheme()
    media.setDark(true)
    expect(preferences.resolvedTheme).toBe('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')

    preferences.setTheme('light')
    media.setDark(false)
    media.setDark(true)
    expect(preferences.resolvedTheme).toBe('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('registers the system listener once however often it is initialised', () => {
    const media = mockMedia(false)
    const preferences = usePreferencesStore()
    preferences.initTheme()
    preferences.initTheme()
    expect(media.listenerCount()).toBe(1)
  })

  it('does not break when storage refuses writes', () => {
    mockMedia(false)
    const preferences = usePreferencesStore()
    preferences.initTheme()
    const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota') })
    expect(() => preferences.setTheme('dark')).not.toThrow()
    expect(preferences.theme).toBe('dark')
    spy.mockRestore()
  })
})

describe('preferences: navigation collapse', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('starts expanded and remembers a collapse', () => {
    const preferences = usePreferencesStore()
    expect(preferences.navigationCollapsed).toBe(false)
    preferences.toggleNavigation()
    expect(preferences.navigationCollapsed).toBe(true)
    expect(localStorage.getItem(NAV_KEY)).toBe('1')
    preferences.toggleNavigation()
    expect(localStorage.getItem(NAV_KEY)).toBe('0')
  })

  it('restores the collapsed state', () => {
    localStorage.setItem(NAV_KEY, '1')
    setActivePinia(createPinia())
    expect(usePreferencesStore().navigationCollapsed).toBe(true)
  })
})
