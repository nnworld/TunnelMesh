import { createI18n } from 'vue-i18n'
import zhCN from './messages/zh-CN'
import enUS from './messages/en-US'

export type SupportedLocale = 'zh-CN' | 'en-US'
export const localeStorageKey = 'tunnelmesh_locale'

export function normalizeLocale(locale?: string | null): SupportedLocale {
  return locale?.toLowerCase().startsWith('zh-') || locale?.toLowerCase() === 'zh' ? 'zh-CN' : 'en-US'
}

export function resolveInitialLocale(stored: string | null, browserLanguages: readonly string[]): SupportedLocale {
  if (stored === 'zh-CN' || stored === 'en-US') return stored
  for (const language of browserLanguages) {
    if (normalizeLocale(language) === 'zh-CN') return 'zh-CN'
  }
  return 'en-US'
}

const initialLocale = resolveInitialLocale(localStorage.getItem(localeStorageKey), navigator.languages || [navigator.language])
export const i18n = createI18n({ legacy: false, locale: initialLocale, fallbackLocale: 'en-US', messages: { 'zh-CN': zhCN, 'en-US': enUS } })

export function setAppLocale(locale: SupportedLocale) {
  i18n.global.locale.value = locale
  document.documentElement.lang = locale
  localStorage.setItem(localeStorageKey, locale)
}

document.documentElement.lang = initialLocale
