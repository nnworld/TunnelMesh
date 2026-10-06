import { afterEach, describe, expect, it, vi } from 'vitest'
import { applyTheme, resolveTheme, systemPrefersDark, watchSystemTheme } from '../theme'

type Listener = (event: { matches: boolean }) => void

/** Replaces window.matchMedia with a controllable stub and returns its controls. */
function stubMatchMedia(matches: boolean) {
  const listeners = new Set<Listener>()
  let current = matches
  const query = {
    get matches() {
      return current
    },
    media: '(prefers-color-scheme: dark)',
    addEventListener: (_: string, listener: Listener) => listeners.add(listener),
    removeEventListener: (_: string, listener: Listener) => listeners.delete(listener),
  }
  vi.stubGlobal('matchMedia', (input: string) => {
    if (input !== query.media) throw new Error(`unexpected media query ${input}`)
    return query
  })
  return {
    set(value: boolean) {
      current = value
      for (const listener of [...listeners]) listener({ matches: value })
    },
    listenerCount: () => listeners.size,
  }
}

describe('appearance', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    document.documentElement.classList.remove('dark')
    delete document.documentElement.dataset.theme
  })

  it('resolves an explicit choice without consulting the system', () => {
    stubMatchMedia(true)
    expect(resolveTheme('light')).toBe('light')
    expect(resolveTheme('dark')).toBe('dark')
    expect(resolveTheme('system')).toBe('dark')
  })

  it('resolves system against the media query', () => {
    stubMatchMedia(false)
    expect(resolveTheme('system')).toBe('light')
    expect(resolveTheme(undefined)).toBe('light')
  })

  it('toggles html.dark for each preference', () => {
    stubMatchMedia(false)
    expect(applyTheme('dark')).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(document.documentElement.dataset.theme).toBe('dark')

    expect(applyTheme('light')).toBe('light')
    expect(document.documentElement.classList.contains('dark')).toBe(false)

    stubMatchMedia(true)
    expect(applyTheme('system')).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
  })

  it('follows system changes only while the preference is system', () => {
    const media = stubMatchMedia(false)
    const onChange = vi.fn()
    let unsubscribe = watchSystemTheme('system', onChange)
    expect(media.listenerCount()).toBe(1)
    media.set(true)
    expect(onChange).toHaveBeenCalledWith('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    unsubscribe()
    expect(media.listenerCount()).toBe(0)

    // An explicit theme must not react to the OS.
    unsubscribe = watchSystemTheme('light', onChange)
    expect(media.listenerCount()).toBe(0)
    onChange.mockClear()
    media.set(false)
    expect(onChange).not.toHaveBeenCalled()
    unsubscribe()
  })

  it('reports the system preference without a media query implementation', () => {
    vi.stubGlobal('matchMedia', undefined)
    expect(systemPrefersDark()).toBe(false)
    expect(applyTheme('system')).toBe('light')
  })
})
