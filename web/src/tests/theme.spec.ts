import { describe, expect, it } from 'vitest'
import { applyTheme, readStoredTheme, resolveTheme, THEME_KEY } from '../utils/theme'

describe('theme resolution', () => {
  it('uses an explicit choice as is', () => {
    expect(resolveTheme('light', true)).toBe('light')
    expect(resolveTheme('dark', false)).toBe('dark')
  })

  it('follows the system preference only for "system"', () => {
    expect(resolveTheme('system', true)).toBe('dark')
    expect(resolveTheme('system', false)).toBe('light')
  })

  it('stores the preference under a stable key', () => {
    // The inline script in index.html reads the same key before the app loads.
    expect(THEME_KEY).toBe('tunnelmesh_theme')
  })
})

describe('stored preference', () => {
  const storageWith = (value: string | null): Pick<Storage, 'getItem'> => ({ getItem: () => value })

  it('reads a valid value', () => {
    expect(readStoredTheme(storageWith('dark'))).toBe('dark')
    expect(readStoredTheme(storageWith('light'))).toBe('light')
    expect(readStoredTheme(storageWith('system'))).toBe('system')
  })

  it('falls back to "system" for anything else', () => {
    expect(readStoredTheme(storageWith(null))).toBe('system')
    expect(readStoredTheme(storageWith('solarized'))).toBe('system')
    expect(readStoredTheme(storageWith(''))).toBe('system')
  })

  // Safari in a private window and locked-down browsers throw on storage access.
  it('survives storage that throws', () => {
    expect(readStoredTheme({ getItem: () => { throw new Error('denied') } })).toBe('system')
  })
})

describe('applying a theme', () => {
  it('sets the attribute, the class Element Plus keys off, and the native colour scheme', () => {
    const root = document.createElement('html')
    applyTheme(root, 'dark')
    expect(root.getAttribute('data-theme')).toBe('dark')
    expect(root.classList.contains('dark')).toBe(true)
    expect(root.style.colorScheme).toBe('dark')

    applyTheme(root, 'light')
    expect(root.getAttribute('data-theme')).toBe('light')
    expect(root.classList.contains('dark')).toBe(false)
    expect(root.style.colorScheme).toBe('light')
  })
})
