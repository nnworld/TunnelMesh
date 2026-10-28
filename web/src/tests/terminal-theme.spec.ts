import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { terminalTheme } from '../webssh/terminalTheme'

const css = readFileSync('src/styles/tokens.css', 'utf8')
const tokenIn = (selector: RegExp) => {
  const block = css.match(selector)![0]
  return block.match(/--tm-terminal-bg:\s*(#[0-9A-Fa-f]{6})/)![1].toLowerCase()
}

describe('terminalTheme', () => {
  it('paints the same background as the terminal host token in each theme', () => {
    expect(terminalTheme('light').background!.toLowerCase()).toBe(tokenIn(/:root\s*\{[\s\S]*?\n\}/))
    expect(terminalTheme('dark').background!.toLowerCase()).toBe(tokenIn(/:root\[data-theme="dark"\][\s\S]*?\n\}/))
  })

  it('returns a fresh object so a caller cannot mutate the shared palette', () => {
    expect(terminalTheme('dark')).not.toBe(terminalTheme('dark'))
  })
})
