import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const src = resolve(__dirname, '..')
const read = (file: string) => readFileSync(resolve(src, file), 'utf8')
const skin = read('styles/element.css')

// Element Plus only gets one look: ours. Its variables are mapped onto the tokens, so
// both themes (and any future one) come from tokens.css alone.
describe('Element Plus skin', () => {
  it('is loaded after the tokens and before the utilities', () => {
    const index = read('styles/index.css')
    const order = ['tokens.css', 'element.css', 'base.css', 'utilities.css'].map((file) => index.indexOf(file))
    expect(order.every((position) => position >= 0)).toBe(true)
    expect([...order].sort((a, b) => a - b)).toEqual(order)
  })

  it('wins over the component CSS that lazy route chunks inject later', () => {
    // Element declares its variables on :root; a doubled selector outranks it whatever the load order.
    expect(skin).toContain(':root:root')
  })

  it('maps the colour, text, border, fill, shadow, radius and size variables', () => {
    const required = [
      '--el-color-primary', '--el-color-primary-light-3', '--el-color-primary-light-5', '--el-color-primary-light-7',
      '--el-color-primary-light-8', '--el-color-primary-light-9', '--el-color-primary-dark-2',
      '--el-color-success', '--el-color-warning', '--el-color-danger', '--el-color-error', '--el-color-info',
      '--el-text-color-primary', '--el-text-color-regular', '--el-text-color-secondary', '--el-text-color-placeholder', '--el-text-color-disabled',
      '--el-border-color', '--el-border-color-light', '--el-border-color-lighter', '--el-border-color-extra-light', '--el-border-color-hover',
      '--el-fill-color', '--el-fill-color-light', '--el-fill-color-lighter', '--el-fill-color-blank',
      '--el-bg-color', '--el-bg-color-page', '--el-bg-color-overlay', '--el-mask-color',
      '--el-box-shadow', '--el-box-shadow-light', '--el-box-shadow-dark',
      '--el-border-radius-base', '--el-border-radius-small', '--el-font-family', '--el-font-size-base',
      '--el-disabled-bg-color', '--el-disabled-text-color', '--el-disabled-border-color',
    ]
    const missing = required.filter((name) => !new RegExp(`${name}\\s*:`).test(skin))
    expect(missing).toEqual([])
  })

  it('styles every Element component the console uses', () => {
    const components = [
      '.el-button', '.el-input__wrapper', '.el-select__wrapper', '.el-textarea__inner', '.el-table', '.el-dialog', '.el-drawer',
      '.el-tag', '.el-pagination', '.el-alert', '.el-message', '.el-message-box', '.el-descriptions', '.el-switch',
      '.el-popper', '.el-dropdown-menu', '.el-menu', '.el-form-item__label', '.el-skeleton', '.el-empty', '.el-tabs',
      '.el-checkbox', '.el-radio', '.el-picker-panel', '.el-breadcrumb', '.el-progress', '.el-segmented',
    ]
    expect(components.filter((selector) => !skin.includes(selector))).toEqual([])
  })

  it('takes colours from tokens only', () => {
    expect(skin).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
    expect(skin).not.toContain('element-plus/dist')
  })

  it('never reintroduces Element default blue', () => {
    expect(skin.toLowerCase()).not.toContain('409eff')
  })
})

// Colours live in tokens.css and the terminal theme. Every other file refers to a token.
// Files that have not moved over yet are listed here; the list can only shrink, so the
// migration is tracked by the suite instead of by memory.
const UNMIGRATED = [
]
const ALLOWED = new Set(['styles/tokens.css', 'webssh/terminalTheme.ts'])

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) return entry === 'tests' || entry === 'assets' ? [] : sourceFiles(path)
    return /\.(vue|ts|css)$/.test(entry) && !entry.endsWith('.spec.ts') && !entry.endsWith('.d.ts') ? [path] : []
  })
}

describe('colour literals', () => {
  const withHex = sourceFiles(src)
    .map((file) => relative(src, file))
    .filter((file) => !ALLOWED.has(file))
    .filter((file) => /#[0-9a-fA-F]{3,8}\b/.test(read(file).replace(/&#[0-9a-fA-F]+;/g, '')))
    .sort()

  it('appear only in files that have not been migrated yet', () => {
    expect(withHex.filter((file) => !UNMIGRATED.includes(file))).toEqual([])
  })

  it('are gone from every file the list claims is still unmigrated', () => {
    expect(UNMIGRATED.filter((file) => !withHex.includes(file))).toEqual([])
  })
})

// One copy of a shared rule, not fourteen: the page-size select width lives in utilities.css.
describe('shared list rules', () => {
  it('keeps .page-size out of views', () => {
    const offenders = sourceFiles(src)
      .filter((file) => file.includes('/views/'))
      .filter((file) => /\.page-size\s*\{/.test(readFileSync(file, 'utf8')))
    expect(offenders).toEqual([])
    expect(readFileSync(join(src, 'styles/utilities.css'), 'utf8')).toMatch(/\.page-size\s*\{/)
  })
})
