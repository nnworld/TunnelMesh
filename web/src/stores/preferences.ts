import { defineStore } from 'pinia'
import { i18n, setAppLocale, type SupportedLocale } from '../i18n'

export const usePreferencesStore = defineStore('preferences', {
  state: () => ({ locale: i18n.global.locale.value as SupportedLocale, navigationCollapsed: false }),
  actions: {
    setLocale(locale: SupportedLocale) { this.locale = locale; setAppLocale(locale) },
    toggleNavigation() { this.navigationCollapsed = !this.navigationCollapsed },
  },
})
