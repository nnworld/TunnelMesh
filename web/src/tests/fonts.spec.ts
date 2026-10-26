import { existsSync, readFileSync, statSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// The console runs on intranets, so fonts are bundled rather than fetched, and they
// are embedded in the Go binary, so their size is a budget.
const fontsDir = resolve(__dirname, '../assets/fonts')
const files = ['instrument-sans-latin-wght-normal.woff2', 'martian-mono-latin-wght-normal.woff2']

describe('bundled fonts', () => {
  it('ships both faces as woff2 under the size budget', () => {
    let total = 0
    for (const file of files) {
      const path = resolve(fontsDir, file)
      expect(existsSync(path), `${file} exists`).toBe(true)
      total += statSync(path).size
    }
    expect(total).toBeLessThanOrEqual(140 * 1024)
  })

  it('declares both families with swap and the bundled files', () => {
    const css = readFileSync(resolve(__dirname, '../styles/fonts.css'), 'utf8')
    for (const family of ['Instrument Sans', 'Martian Mono']) expect(css).toContain(`font-family: "${family}"`)
    for (const file of files) expect(css).toContain(file)
    expect(css.match(/font-display:\s*swap/g)?.length).toBe(2)
    expect(css).not.toMatch(/https?:\/\//)
  })

  it('carries the licences the OFL requires', () => {
    for (const file of ['OFL-InstrumentSans.txt', 'OFL-MartianMono.txt']) {
      expect(readFileSync(resolve(fontsDir, file), 'utf8')).toContain('SIL OPEN FONT LICENSE')
    }
    const notice = readFileSync(resolve(__dirname, '../../../NOTICE'), 'utf8')
    expect(notice).toContain('Instrument Sans')
    expect(notice).toContain('Martian Mono')
  })
})
