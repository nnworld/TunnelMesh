import type { SupportedLocale } from '.'
import { useI18n } from 'vue-i18n'

export function formatDateTime(value?: string | null, locale: SupportedLocale = 'en-US', timeZone?: string) {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat(locale, {
    year: 'numeric', month: 'numeric', day: 'numeric',
    hour: 'numeric', minute: 'numeric', second: 'numeric', timeZone,
  }).format(date)
}

export function useFormatDateTime(timeZone?: string) {
  const { locale } = useI18n()
  return (value?: string | null) => formatDateTime(value, locale.value as SupportedLocale, timeZone)
}
