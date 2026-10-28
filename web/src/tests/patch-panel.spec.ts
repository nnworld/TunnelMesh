import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import { createI18n } from 'vue-i18n'
import messages from '../i18n/messages/en-US'
import PatchPanel from '../components/PatchPanel.vue'
import type { Agent } from '../api/client'

const push = vi.fn()
vi.mock('vue-router', () => ({ useRouter: () => ({ push }), useRoute: () => ({ path: '/' }) }))

const agent = (n: number, over: Partial<Agent> = {}): Agent => ({ id: `agent-${n}`, name: `edge-${n}`, enabled: true, status: 'offline', ...over })

function mount(props: Record<string, unknown>) {
  const host = document.createElement('div'); document.body.append(host)
  createApp({ render: () => h(PatchPanel, props) }).use(createI18n({ legacy: false, locale: 'en-US', messages: { 'en-US': messages } })).mount(host)
  return host
}
const ports = (host: HTMLElement) => [...host.querySelectorAll<HTMLButtonElement>('[data-test="port"]')]

describe('PatchPanel', () => {
  beforeEach(() => { document.body.replaceChildren(); push.mockReset() })

  it('maps agent state to the LED state: disabled beats online beats offline', () => {
    const host = mount({ agents: [agent(1, { status: 'online' }), agent(2), agent(3, { enabled: false, status: 'online' })], total: 3, online: 1 })
    expect(ports(host).map(p => p.getAttribute('data-state'))).toEqual(['online', 'offline', 'disabled'])
  })

  it('names each port with its state so the LED is never the only signal', () => {
    const host = mount({ agents: [agent(1, { status: 'online' })], total: 1, online: 1 })
    expect(ports(host)[0].getAttribute('aria-label')).toBe('edge-1, Online')
  })

  it('opens the agent on click', () => {
    const host = mount({ agents: [agent(1)], total: 1, online: 0 })
    ports(host)[0].click()
    expect(push).toHaveBeenCalledWith('/agents/agent-1')
  })

  it('collapses the overflow into a +K cell using the real total, not the list length', () => {
    const host = mount({ agents: Array.from({ length: 32 }, (_, i) => agent(i)), total: 40, online: 8 })
    expect(ports(host)).toHaveLength(32)
    const more = host.querySelector<HTMLButtonElement>('[data-test="port-more"]')!
    expect(more.textContent).toContain('+8')
    more.click()
    expect(push).toHaveBeenCalledWith('/agents')
  })

  it('shows the legend counts from the totals', () => {
    const host = mount({ agents: [agent(1)], total: 40, online: 8 })
    expect(host.querySelector('[data-test="legend"]')!.textContent).toContain('8')
    expect(host.querySelector('[data-test="legend"]')!.textContent).toContain('40')
  })

  it('uses roving tabindex and moves focus with the arrow keys', async () => {
    const host = mount({ agents: [agent(1), agent(2), agent(3)], total: 3, online: 0 })
    expect(ports(host).filter(p => p.tabIndex === 0)).toHaveLength(1)
    ports(host)[0].focus()
    ports(host)[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
    await nextTick()
    expect(document.activeElement).toBe(ports(host)[1])
    expect(ports(host)[1].tabIndex).toBe(0)
    expect(ports(host)[0].tabIndex).toBe(-1)
  })
})
