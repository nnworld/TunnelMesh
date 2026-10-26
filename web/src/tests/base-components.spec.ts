import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import { createI18n } from 'vue-i18n'
import messages from '../i18n/messages/en-US'
import BrandMark from '../components/BrandMark.vue'
import StatusLed from '../components/StatusLed.vue'
import CodeBlock from '../components/CodeBlock.vue'
import SecretBlock from '../components/SecretBlock.vue'
import DescriptionList from '../components/DescriptionList.vue'

function mount(component: object, props: Record<string, unknown> = {}) {
  const host = document.createElement('div')
  document.body.append(host)
  const i18n = createI18n({ legacy: false, locale: 'en-US', messages: { 'en-US': messages } })
  createApp({ render: () => h(component, props) }).use(i18n).mount(host)
  return host
}

describe('base components', () => {
  beforeEach(() => {
    document.body.replaceChildren()
    Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } })
  })

  it('StatusLed pairs every state with text and a state attribute', () => {
    for (const state of ['online', 'offline', 'disabled'] as const) {
      const el = mount(StatusLed, { state, label: state.toUpperCase() }).querySelector('.status-led')!
      expect(el.getAttribute('data-state')).toBe(state)
      expect(el.textContent).toContain(state.toUpperCase())
      expect(el.querySelector('.status-led__dot')!.getAttribute('aria-hidden')).toBe('true')
    }
  })

  it('StatusLed only pulses while online', () => {
    expect(mount(StatusLed, { state: 'online', label: 'x' }).querySelector('.is-pulsing')).not.toBeNull()
    document.body.replaceChildren()
    expect(mount(StatusLed, { state: 'offline', label: 'x' }).querySelector('.is-pulsing')).toBeNull()
  })

  it('BrandMark renders the wordmark with an accessible name', () => {
    const host = mount(BrandMark, { name: 'TunnelMesh' })
    expect(host.textContent).toContain('TunnelMesh')
    expect(host.querySelector('svg')!.getAttribute('aria-hidden')).toBe('true')
  })

  it('CodeBlock shows the code verbatim and copies it', async () => {
    const host = mount(CodeBlock, { code: 'sha256sum -c SHA256SUMS' })
    expect(host.querySelector('pre')!.textContent).toBe('sha256sum -c SHA256SUMS')
    host.querySelector<HTMLButtonElement>('[data-test="copy"]')!.click()
    await nextTick()
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('sha256sum -c SHA256SUMS')
  })

  it('SecretBlock masks by default but keeps the value copyable', async () => {
    const host = mount(SecretBlock, { value: 'tm_secret_123' })
    const field = host.querySelector('[data-test="secret-value"]')!
    expect(field.textContent).not.toContain('tm_secret_123')
    expect(host.querySelector('[data-test="secret-toggle"]')!.getAttribute('aria-pressed')).toBe('false')
    host.querySelector<HTMLButtonElement>('[data-test="secret-toggle"]')!.click()
    await nextTick()
    expect(host.querySelector('[data-test="secret-value"]')!.textContent).toContain('tm_secret_123')
    host.querySelector<HTMLButtonElement>('[data-test="copy"]')!.click()
    await nextTick()
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('tm_secret_123')
  })

  it('SecretBlock can start revealed for flows where hiding gets in the way', () => {
    const host = mount(SecretBlock, { value: 'abc', revealed: true })
    expect(host.querySelector('[data-test="secret-value"]')!.textContent).toContain('abc')
  })

  it('DescriptionList renders term/definition pairs and skips nothing silently', () => {
    const host = mount(DescriptionList, { items: [{ label: 'Name', value: 'edge-1' }, { label: 'Region', value: '' }] })
    expect(host.querySelectorAll('dt')).toHaveLength(2)
    expect(host.querySelectorAll('dd')[0].textContent).toBe('edge-1')
    expect(host.querySelectorAll('dd')[1].textContent).toBe('—')
  })
})
