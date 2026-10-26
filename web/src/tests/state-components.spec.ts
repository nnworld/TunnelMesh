import { beforeEach, describe, expect, it } from 'vitest'
import { createApp, h } from 'vue'
import EmptyState from '../components/EmptyState.vue'
import ErrorState from '../components/ErrorState.vue'
import StatusTag from '../components/StatusTag.vue'
import DataState from '../components/DataState.vue'

function mount(component: object, props: Record<string, unknown> = {}, slots: Record<string, () => unknown> = {}) {
  const host = document.createElement('div')
  document.body.append(host)
  createApp({ render: () => h(component, props, slots) }).mount(host)
  return host
}

describe('state components', () => {
  beforeEach(() => document.body.replaceChildren())

  it('EmptyState draws a decorative illustration with the message as text', () => {
    const host = mount(EmptyState, { label: 'No agents yet', hint: 'Create one to get started.' })
    expect(host.querySelector('svg')!.getAttribute('aria-hidden')).toBe('true')
    expect(host.textContent).toContain('No agents yet')
    expect(host.textContent).toContain('Create one to get started.')
  })

  it('EmptyState exposes an action slot', () => {
    const host = mount(EmptyState, { label: 'x' }, { default: () => h('button', { id: 'go' }, 'Create') })
    expect(host.querySelector('#go')).not.toBeNull()
  })

  it('ErrorState is an alert with a retry that emits', () => {
    let retried = 0
    const host = document.createElement('div'); document.body.append(host)
    createApp({ render: () => h(ErrorState, { label: 'Data failed to load', retryLabel: 'Retry', onRetry: () => retried++ }) }).mount(host)
    const alert = host.querySelector('[role="alert"]')!
    expect(alert.textContent).toContain('Data failed to load')
    host.querySelector<HTMLButtonElement>('button')!.click()
    expect(retried).toBe(1)
  })

  it('ErrorState omits the retry button when there is nothing to retry', () => {
    expect(mount(ErrorState, { label: 'Failed' }).querySelector('button')).toBeNull()
  })

  it('StatusTag keeps the el-tag base and adds an LED that is never the only signal', () => {
    for (const kind of ['success', 'warning', 'info', 'danger'] as const) {
      document.body.replaceChildren()
      const host = mount(StatusTag, { kind, label: kind })
      expect(host.querySelector('.el-tag')).not.toBeNull()
      expect(host.querySelector('.tag-led')!.getAttribute('aria-hidden')).toBe('true')
      expect(host.textContent).toContain(kind)
    }
  })

  it('DataState renders the shared empty and error components', () => {
    expect(mount(DataState, { empty: true, emptyLabel: 'No data' }).querySelector('.empty-state')).not.toBeNull()
    expect(mount(DataState, { error: true, errorLabel: 'Boom' }).querySelector('[role="alert"]')).not.toBeNull()
  })
})
