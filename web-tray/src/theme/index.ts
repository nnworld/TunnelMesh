import type { ThemePreference } from '../api/types'

export type ResolvedTheme = 'light' | 'dark'

const DARK_CLASS = 'dark'
const DARK_QUERY = '(prefers-color-scheme: dark)'

export function systemPrefersDark(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
  return window.matchMedia(DARK_QUERY).matches
}

export function resolveTheme(preference: ThemePreference | undefined | null): ResolvedTheme {
  if (preference === 'light' || preference === 'dark') return preference
  return systemPrefersDark() ? 'dark' : 'light'
}

/**
 * Applies a theme by toggling `html.dark`.
 *
 * Element Plus ships its dark palette as CSS variables scoped to `.dark`, so one class on
 * the root element is the whole integration. The media query is only consulted for the
 * 'system' preference, which is what keeps an explicit choice from being overridden by a
 * stylesheet.
 */
export function applyTheme(preference: ThemePreference | undefined | null): ResolvedTheme {
  const resolved = resolveTheme(preference)
  if (typeof document !== 'undefined') {
    document.documentElement.classList.toggle(DARK_CLASS, resolved === 'dark')
    document.documentElement.dataset.theme = resolved
  }
  return resolved
}

/**
 * Follows the operating system while the preference is 'system'.
 *
 * Returns the unsubscribe function. A listener is only registered for 'system' because an
 * explicit choice must not react to the OS, and leaving the listener attached would
 * silently override it.
 */
export function watchSystemTheme(
  preference: ThemePreference | undefined | null,
  onChange: (theme: ResolvedTheme) => void,
): () => void {
  if (preference !== 'system') return () => {}
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return () => {}
  const query = window.matchMedia(DARK_QUERY)
  const listener = () => onChange(applyTheme('system'))
  query.addEventListener('change', listener)
  return () => query.removeEventListener('change', listener)
}
