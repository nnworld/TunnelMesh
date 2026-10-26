import { defineStore } from 'pinia'
import { i18n, setAppLocale, type SupportedLocale } from '../i18n'
import { applyTheme, readStoredTheme, resolveTheme, THEME_KEY, type ThemePreference } from '../utils/theme'

const NAV_COLLAPSED_KEY = 'tunnelmesh_nav_collapsed'
const DARK_QUERY = '(prefers-color-scheme: dark)'

function readNavigationCollapsed(): boolean {
  try { return localStorage.getItem(NAV_COLLAPSED_KEY) === '1' } catch { return false }
}

// Preferences are conveniences, so a storage that refuses writes must never turn a
// click into an error: the choice still applies for this session.
function remember(key: string, value: string) {
  try { localStorage.setItem(key, value) } catch { /* the choice just is not remembered */ }
}

export const usePreferencesStore = defineStore('preferences', {
  state: () => ({
    locale: i18n.global.locale.value as SupportedLocale,
    navigationCollapsed: readNavigationCollapsed(),
    theme: readStoredTheme(localStorage) as ThemePreference,
    systemDark: false,
    themeListening: false,
  }),
  getters: {
    resolvedTheme: (state) => resolveTheme(state.theme, state.systemDark),
  },
  actions: {
    setLocale(locale: SupportedLocale) { this.locale = locale; setAppLocale(locale) },
    toggleNavigation() {
      this.navigationCollapsed = !this.navigationCollapsed
      remember(NAV_COLLAPSED_KEY, this.navigationCollapsed ? '1' : '0')
    },
    /** initTheme applies the stored theme and starts following the system setting. */
    initTheme() {
      if (this.themeListening) return
      this.themeListening = true
      const query = typeof window.matchMedia === 'function' ? window.matchMedia(DARK_QUERY) : null
      this.systemDark = !!query?.matches
      query?.addEventListener('change', (event) => {
        this.systemDark = event.matches
        this.paintTheme()
      })
      this.paintTheme()
    },
    setTheme(theme: ThemePreference) {
      this.theme = theme
      remember(THEME_KEY, theme)
      this.paintTheme()
    },
    paintTheme() { applyTheme(document.documentElement, this.resolvedTheme) },
  },
})
