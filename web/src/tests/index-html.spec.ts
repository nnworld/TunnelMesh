import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { THEME_KEY } from '../utils/theme'

// Without this script a dark-theme user sees a flash of the light page on every load.
const html = readFileSync(resolve(__dirname, '../../index.html'), 'utf8')

describe('index.html theme bootstrap', () => {
  it('sets the theme synchronously before the app loads', () => {
    const script = html.indexOf('<script>')
    const appScript = html.indexOf('type="module"')
    expect(script).toBeGreaterThan(-1)
    expect(script).toBeLessThan(appScript)
  })

  it('reads the same storage key as the preferences store', () => {
    expect(html).toContain(THEME_KEY)
    expect(html).toContain('data-theme')
    expect(html).toContain('prefers-color-scheme')
  })

  it('declares both colour schemes to the browser', () => {
    expect(html).toMatch(/<meta name="color-scheme" content="light dark"/)
  })

  it('does not let a storage failure stop the page from loading', () => {
    expect(html).toContain('try')
    expect(html).toContain('catch')
  })
})
