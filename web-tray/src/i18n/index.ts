import { createI18n } from 'vue-i18n'
import zhCN from './zh-CN'
import enUS from './en-US'

export type SupportedLocale = 'zh-CN' | 'en-US'
/** 'system' means "follow the operating system" and is never persisted as an override. */
export type LanguagePreference = SupportedLocale | 'system'

export function normalizeLocale(value?: string | null): SupportedLocale {
  const candidate = (value ?? '').toLowerCase()
  return candidate === 'zh' || candidate.startsWith('zh-') || candidate.startsWith('zh_') ? 'zh-CN' : 'en-US'
}

/**
 * Resolves the effective locale.
 *
 * 'system' deliberately resolves against the webview's languages on every launch rather
 * than being written back as a concrete locale: persisting the detection result would
 * freeze it, and an operator who changes the macOS language would keep the old one.
 */
export function resolveLocale(
  preference: LanguagePreference | undefined | null,
  browserLanguages: readonly string[],
): SupportedLocale {
  if (preference === 'zh-CN' || preference === 'en-US') return preference
  for (const language of browserLanguages) {
    if (normalizeLocale(language) === 'zh-CN') return 'zh-CN'
  }
  return 'en-US'
}

export function browserLanguages(): string[] {
  if (typeof navigator === 'undefined') return []
  return [...(navigator.languages ?? []), navigator.language].filter(Boolean)
}

export const i18n = createI18n({
  legacy: false,
  locale: resolveLocale('system', browserLanguages()),
  fallbackLocale: 'en-US',
  messages: { 'zh-CN': zhCN, 'en-US': enUS },
})

export function setLocale(locale: SupportedLocale): void {
  i18n.global.locale.value = locale
  if (typeof document !== 'undefined') document.documentElement.lang = locale
}

export function applyLanguagePreference(
  preference: LanguagePreference | undefined | null,
  languages: readonly string[] = browserLanguages(),
): SupportedLocale {
  const locale = resolveLocale(preference, languages)
  setLocale(locale)
  return locale
}

export { zhCN, enUS }
