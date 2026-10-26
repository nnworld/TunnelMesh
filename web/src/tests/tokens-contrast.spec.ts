import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// The palette is only as good as its legibility, so the contrast of every pair the
// UI actually uses is asserted here instead of being left to taste.
const css = readFileSync(resolve(__dirname, '../styles/tokens.css'), 'utf8')

function block(selector: RegExp): Record<string, string> {
  const match = selector.exec(css)
  if (!match) throw new Error(`tokens.css has no block matching ${selector}`)
  const start = css.indexOf('{', match.index) + 1
  const end = css.indexOf('}', start)
  const tokens: Record<string, string> = {}
  for (const declaration of css.slice(start, end).matchAll(/--tm-([a-z0-9-]+)\s*:\s*(#[0-9a-fA-F]{6})\s*;/g)) {
    tokens[declaration[1]] = declaration[2]
  }
  return tokens
}

const light = block(/:root\s*\{/)
const dark = block(/:root\[data-theme=["']dark["']\]\s*\{/)

function channel(value: number) {
  const c = value / 255
  return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
}
function luminance(hex: string) {
  const n = parseInt(hex.slice(1), 16)
  return 0.2126 * channel((n >> 16) & 255) + 0.7152 * channel((n >> 8) & 255) + 0.0722 * channel(n & 255)
}
function contrast(a: string, b: string) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

type Pair = [foreground: string, background: string, minimum: number]

function check(tokens: Record<string, string>, pairs: Pair[]) {
  for (const [fg, bg, minimum] of pairs) {
    expect(tokens[fg], `--tm-${fg} is defined`).toBeTruthy()
    expect(tokens[bg], `--tm-${bg} is defined`).toBeTruthy()
    const ratio = contrast(tokens[fg], tokens[bg])
    expect(ratio, `--tm-${fg} on --tm-${bg} is ${ratio.toFixed(2)}`).toBeGreaterThanOrEqual(minimum)
  }
}

describe('light theme contrast', () => {
  const surfaces = ['canvas', 'paper', 'raised']

  it('keeps body and secondary text readable on every surface', () => {
    check(light, surfaces.flatMap((s): Pair[] => [['ink', s, 7], ['ink-2', s, 4.5], ['ink-3', s, 4.5], ['brand', s, 4.5]]))
  })

  it('keeps status text readable on surfaces and on its own tint', () => {
    check(light, [
      ['ok-ink', 'paper', 4.5], ['warn-ink', 'paper', 4.5], ['danger-ink', 'paper', 4.5], ['danger', 'paper', 4.5],
      ['ok-ink', 'ok-tint', 4.5], ['warn-ink', 'warn-tint', 4.5], ['danger-ink', 'danger-tint', 4.5],
      ['danger-ink', 'canvas', 4.5], ['brand', 'brand-tint', 4.5],
    ])
  })

  it('keeps button text, LEDs and control borders perceivable', () => {
    check(light, [
      ['on-brand', 'brand-fill', 4.5], ['on-brand', 'brand-hover', 4.5], ['on-danger', 'danger-fill', 4.5],
      ['led-ok', 'paper', 3], ['warn', 'paper', 2], ['line-control', 'paper', 3], ['line-control', 'raised', 3],
    ])
  })
})

describe('dark theme contrast', () => {
  const surfaces = ['canvas', 'paper', 'raised']

  it('keeps text readable on every surface', () => {
    check(dark, surfaces.flatMap((s): Pair[] => [['ink', s, 7], ['ink-2', s, 4.5], ['ink-3', s, 4.5], ['brand', s, 4.5]]))
  })

  // ink-3 is below 4.5 on the float layer, so popovers and menus use ink-2 for meta text.
  it('keeps floating layers readable with ink and ink-2', () => {
    check(dark, [['ink', 'float', 7], ['ink-2', 'float', 4.5], ['brand', 'float', 4.5]])
  })

  it('keeps status text and button text readable', () => {
    check(dark, [
      ['ok', 'paper', 4.5], ['warn', 'paper', 4.5], ['danger', 'paper', 4.5],
      ['ok', 'ok-tint', 4.5], ['warn', 'warn-tint', 4.5], ['danger', 'danger-tint', 4.5],
      ['on-brand', 'brand-fill', 4.5], ['on-danger', 'danger-fill', 4.5], ['line-control', 'paper', 3],
    ])
  })
})

describe('token completeness', () => {
  it('defines the same roles in both themes', () => {
    const missing = Object.keys(light).filter((name) => !(name in dark) && !['float'].includes(name))
    expect(missing).toEqual([])
    expect(Object.keys(dark).filter((name) => !(name in light) && name !== 'float')).toEqual([])
  })
})
