import { beforeEach, describe, expect, it } from 'vitest'
import { createApp, h } from 'vue'
import TmIcon from '../components/icons/TmIcon.vue'
import { ICON_NAMES, iconPaths } from '../components/icons/paths'

function mount(props: Record<string, unknown>) {
  const host = document.createElement('div')
  document.body.append(host)
  createApp({ render: () => h(TmIcon, props) }).mount(host)
  return host
}

describe('TmIcon', () => {
  beforeEach(() => document.body.replaceChildren())

  it('ships a path for every declared name and nothing with colour literals', () => {
    expect(ICON_NAMES.length).toBeGreaterThanOrEqual(28)
    for (const name of ICON_NAMES) {
      const markup = iconPaths[name]
      expect(markup, name).toBeTruthy()
      expect(markup, name).not.toMatch(/#[0-9a-f]{3,8}\b|rgb\(/i)
      expect(markup, name).not.toContain('<script')
    }
  })

  it('is decorative by default: hidden from assistive tech, not focusable', () => {
    const svg = mount({ name: 'agents' }).querySelector('svg')!
    expect(svg.getAttribute('aria-hidden')).toBe('true')
    expect(svg.getAttribute('focusable')).toBe('false')
    expect(svg.getAttribute('role')).toBeNull()
  })

  it('becomes an image with an accessible name when a label is given', () => {
    const svg = mount({ name: 'copy', label: 'Copy' }).querySelector('svg')!
    expect(svg.getAttribute('role')).toBe('img')
    expect(svg.getAttribute('aria-label')).toBe('Copy')
    expect(svg.getAttribute('aria-hidden')).toBeNull()
  })

  it('draws on a 24 grid with a currentColor stroke so it follows the theme', () => {
    const svg = mount({ name: 'search', size: 20 }).querySelector('svg')!
    expect(svg.getAttribute('viewBox')).toBe('0 0 24 24')
    expect(svg.getAttribute('stroke')).toBe('currentColor')
    expect(svg.getAttribute('stroke-width')).toBe('1.5')
    expect(svg.getAttribute('stroke-linecap')).toBe('square')
    expect(svg.getAttribute('width')).toBe('20')
  })

  it('renders nothing for an unknown name instead of throwing', () => {
    expect(mount({ name: 'nope' }).querySelector('svg')).toBeNull()
  })
})
