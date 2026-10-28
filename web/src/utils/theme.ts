// Theme resolution is kept as pure functions so it can be tested without a
// browser and shared by the preferences store and the inline script in index.html.

export const THEME_KEY = 'tunnelmesh_theme'

export type ThemePreference = 'light' | 'dark' | 'system'
export type ResolvedTheme = 'light' | 'dark'

/** resolveTheme turns the user's choice into the theme that is actually shown. */
export function resolveTheme(preference: ThemePreference, systemPrefersDark: boolean): ResolvedTheme {
  if (preference === 'system') return systemPrefersDark ? 'dark' : 'light'
  return preference
}

/**
 * readStoredTheme returns the saved choice, or "system" when there is none, when it
 * is not a known value, or when storage cannot be read at all (private windows and
 * locked-down browsers throw on access).
 */
export function readStoredTheme(storage: Pick<Storage, 'getItem'>): ThemePreference {
  try {
    const value = storage.getItem(THEME_KEY)
    return value === 'light' || value === 'dark' || value === 'system' ? value : 'system'
  } catch {
    return 'system'
  }
}

/**
 * applyTheme writes the theme where CSS and third parties look for it: the
 * data-theme attribute our tokens key off, the "dark" class Element Plus and
 * libraries key off, and color-scheme so scrollbars and form controls follow.
 */
export function applyTheme(root: HTMLElement, theme: ResolvedTheme) {
  root.setAttribute('data-theme', theme)
  root.classList.toggle('dark', theme === 'dark')
  root.style.colorScheme = theme
}
